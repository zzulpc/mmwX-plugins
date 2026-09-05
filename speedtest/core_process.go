package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	coreLogTailBytes  = 32 << 10
	coreErrorMaxBytes = 4 << 10
	coreReadyTimeout  = 15 * time.Second
	coreKillWaitLimit = 2 * time.Second
)

var (
	ansiEscapePattern  = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	secretFieldPattern = regexp.MustCompile(
		`(?i)((?:psk|userkey|password|passwd|token|secret|private[-_ ]?key|authorization|cookie|api[-_ ]?key|uuid|client[-_ ]?id)["']?\s*[:=]\s*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\s,}\]]+)`,
	)
)

// boundedTailBuffer 只保留内核输出末尾，防止异常进程持续输出导致内存增长。
type boundedTailBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func newBoundedTailBuffer(limit int) *boundedTailBuffer {
	return &boundedTailBuffer{limit: limit, data: make([]byte, 0, limit)}
}

func (buffer *boundedTailBuffer) Write(content []byte) (int, error) {
	written := len(content)
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.limit <= 0 {
		return written, nil
	}
	if len(content) >= buffer.limit {
		buffer.data = append(buffer.data[:0], content[len(content)-buffer.limit:]...)
		return written, nil
	}
	overflow := len(buffer.data) + len(content) - buffer.limit
	if overflow > 0 {
		copy(buffer.data, buffer.data[overflow:])
		buffer.data = buffer.data[:len(buffer.data)-overflow]
	}
	buffer.data = append(buffer.data, content...)
	return written, nil
}

func (buffer *boundedTailBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return string(append([]byte(nil), buffer.data...))
}

// collectProxySecrets 递归收集可能出现在内核诊断里的精确密钥值，仅用于错误脱敏。
func collectProxySecrets(proxy map[string]any) []string {
	secrets := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)
	collectSecretValues(proxy, false, 0, seen, &secrets)
	// 先替换较长密钥，避免短密钥是长密钥前缀时留下后半段。
	sort.Slice(secrets, func(left, right int) bool {
		return len(secrets[left]) > len(secrets[right])
	})
	return secrets
}

// collectSecretValues 最多递归 32 层，避免畸形任务配置消耗过多调用栈。
func collectSecretValues(value any, parentSensitive bool, depth int, seen map[string]struct{}, secrets *[]string) {
	if depth > 32 {
		return
	}
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			collectSecretValues(child, parentSensitive || sensitiveProxyKey(key), depth+1, seen, secrets)
		}
	case []any:
		for _, child := range current {
			collectSecretValues(child, parentSensitive, depth+1, seen, secrets)
		}
	case string:
		if !parentSensitive || current == "" {
			return
		}
		if _, exists := seen[current]; exists {
			return
		}
		seen[current] = struct{}{}
		*secrets = append(*secrets, current)
	}
}

// sensitiveProxyKey 识别顶层及嵌套认证字段，保留字段名但隐藏实际值。
func sensitiveProxyKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.NewReplacer("-", "", "_", "", " ", "").Replace(normalized)
	fragments := []string{
		"psk", "password", "passwd", "token", "secret", "userkey", "privatekey",
		"presharedkey", "authorization", "cookie", "apikey", "uuid", "clientid", "shortid", "auth",
	}
	for _, fragment := range fragments {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return normalized == "headers"
}

// sanitizeCoreOutput 在错误回传主控前移除密钥、终端控制字符并限制长度。
func sanitizeCoreOutput(raw string, secrets []string) string {
	sanitized := ansiEscapePattern.ReplaceAllString(raw, "")
	for _, secret := range secrets {
		if secret != "" {
			sanitized = strings.ReplaceAll(sanitized, secret, "***")
			encoded, err := json.Marshal(secret)
			if err == nil && len(encoded) >= 2 {
				escaped := string(encoded[1 : len(encoded)-1])
				if escaped != secret {
					sanitized = strings.ReplaceAll(sanitized, escaped, "***")
				}
			}
		}
	}
	sanitized = secretFieldPattern.ReplaceAllString(sanitized, `${1}***`)
	sanitized = strings.Map(func(character rune) rune {
		switch character {
		case '\n', '\t':
			return character
		case '\r':
			return '\n'
		default:
			if character < 32 || character == 127 {
				return -1
			}
			return character
		}
	}, sanitized)
	sanitized = strings.TrimSpace(sanitized)
	if len(sanitized) > coreErrorMaxBytes {
		// 取尾部之后必须再按 rune 边界前移。上面的 strings.Map 已经把内核输出规整成合法 UTF-8，
		// 唯一的破坏点就是这里按字节切分:切在多字节字符中间时，这段会以半个字符开头，
		// 回传主控的 JSON 里就变成 U+FFFD —— 中文诊断几乎必然踩中。
		sanitized = sanitized[len(sanitized)-coreErrorMaxBytes:]
		for len(sanitized) > 0 && !utf8.RuneStart(sanitized[0]) {
			sanitized = sanitized[1:]
		}
	}
	return sanitized
}

// coreCommandArgs 返回不同代理内核的启动参数。
func coreCommandArgs(runtimeInfo proxyRuntime, workdir, configPath string, checkOnly bool) []string {
	if runtimeInfo.Core == coreSingBox {
		command := "run"
		if checkOnly {
			command = "check"
		}
		return []string{"-D", workdir, "-c", configPath, "--disable-color", command}
	}
	return []string{"-d", workdir, "-f", configPath}
}

// checkSingBoxConfig 在真正启动前执行官方配置校验，失败时返回已脱敏的诊断。
func checkSingBoxConfig(ctx context.Context, runtimeInfo proxyRuntime, workdir, configPath string, secrets []string) error {
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output := newBoundedTailBuffer(coreLogTailBytes)
	command := exec.CommandContext(checkCtx, runtimeInfo.Bin, coreCommandArgs(runtimeInfo, workdir, configPath, true)...)
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		detail := sanitizeCoreOutput(output.String(), secrets)
		if checkCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("sing-box 配置校验超时")
		}
		if detail == "" {
			return fmt.Errorf("sing-box 配置校验失败: %w", err)
		}
		return fmt.Errorf("sing-box 配置校验失败: %s", detail)
	}
	return nil
}

// startProxyCore 启动单次测速所需内核，并同时监控进程退出、端口就绪与上下文取消。
func startProxyCore(ctx context.Context, runtimeInfo proxyRuntime, workdir, configPath string, secrets []string, mixedPort int) (func(), error) {
	if runtimeInfo.Bin == "" {
		return nil, fmt.Errorf("%s 可执行文件路径为空", runtimeInfo.Core)
	}
	if runtimeInfo.Core == coreSingBox {
		if err := checkSingBoxConfig(ctx, runtimeInfo, workdir, configPath, secrets); err != nil {
			return nil, err
		}
	}

	output := newBoundedTailBuffer(coreLogTailBytes)
	command := exec.Command(runtimeInfo.Bin, coreCommandArgs(runtimeInfo, workdir, configPath, false)...)
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("%s 启动失败: %w", runtimeInfo.Core, err)
	}
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- command.Wait()
	}()

	stopProcess := func() {
		if command.Process == nil {
			return
		}
		if runtime.GOOS == "windows" {
			_ = command.Process.Kill()
		} else {
			_ = command.Process.Signal(syscall.SIGTERM)
		}
		select {
		case <-waitDone:
		case <-time.After(3 * time.Second):
			if err := command.Process.Kill(); err != nil {
				log.Printf("[warn] 无法强制停止 %s 子进程: %v", runtimeInfo.Core, err)
			}
			select {
			case <-waitDone:
			case <-time.After(coreKillWaitLimit):
				log.Printf("[warn] %s 子进程在强制停止后仍未退出，测速任务将继续收尾", runtimeInfo.Core)
			}
		}
	}

	address := fmt.Sprintf("127.0.0.1:%d", mixedPort)
	readyDeadline := time.NewTimer(coreReadyTimeout)
	defer readyDeadline.Stop()
	probeTicker := time.NewTicker(100 * time.Millisecond)
	defer probeTicker.Stop()
	for {
		select {
		case waitErr := <-waitDone:
			detail := sanitizeCoreOutput(output.String(), secrets)
			if detail == "" {
				return nil, fmt.Errorf("%s 在本地代理端口就绪前退出: %v", runtimeInfo.Core, waitErr)
			}
			return nil, fmt.Errorf("%s 在本地代理端口就绪前退出: %s", runtimeInfo.Core, detail)
		case <-ctx.Done():
			stopProcess()
			return nil, fmt.Errorf("%s 启动被取消: %w", runtimeInfo.Core, ctx.Err())
		case <-readyDeadline.C:
			stopProcess()
			detail := sanitizeCoreOutput(output.String(), secrets)
			if detail == "" {
				return nil, fmt.Errorf("%s 启动超时，本地端口 %d 在 15 秒内未就绪", runtimeInfo.Core, mixedPort)
			}
			return nil, fmt.Errorf("%s 启动超时: %s", runtimeInfo.Core, detail)
		case <-probeTicker.C:
			connection, err := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(ctx, "tcp", address)
			if err != nil {
				continue
			}
			_ = connection.Close()
			select {
			case waitErr := <-waitDone:
				detail := sanitizeCoreOutput(output.String(), secrets)
				if detail == "" {
					return nil, fmt.Errorf("%s 启动后立即退出: %v", runtimeInfo.Core, waitErr)
				}
				return nil, fmt.Errorf("%s 启动后立即退出: %s", runtimeInfo.Core, detail)
			default:
			}
			var once sync.Once
			return func() {
				once.Do(stopProcess)
			}, nil
		}
	}
}
