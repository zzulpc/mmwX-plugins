package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// minSingBoxVersion 只表示运行时允许使用的最低兼容版本，不能随镜像升级自动抬高。
	minSingBoxVersion = "1.14.0-beta.17"
	// pinnedSingBoxVersion 表示 Docker 镜像固定携带的版本，可独立于最低兼容版本升级。
	pinnedSingBoxVersion = "1.14.0"
)

var (
	singBoxMu         sync.Mutex
	singBoxCachedPath string
	singBoxVerRe      = regexp.MustCompile(`v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)`)
)

// singBoxBinName 返回当前平台的 sing-box 可执行文件名。
func singBoxBinName() string {
	if runtime.GOOS == "windows" {
		return "sing-box.exe"
	}
	return "sing-box"
}

// singBoxVersion 运行版本命令并提取完整语义版本，保留 alpha、beta 或 rc 后缀。
func singBoxVersion(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, bin, "version").CombinedOutput()
	matched := singBoxVerRe.FindStringSubmatch(string(out))
	if matched == nil {
		return ""
	}
	return matched[1]
}

// singBoxSupported 保留已验证的兼容下限，镜像升级正式版不强制用户替换显式指定的合格内核。
func singBoxSupported(bin string) bool {
	version := singBoxVersion(bin)
	return version != "" && semanticVersionGTE(version, minSingBoxVersion)
}

// semanticVersionGTE 按语义版本规则比较 actual 是否不低于 minimum。
func semanticVersionGTE(actual, minimum string) bool {
	actualCore, actualPre, actualOK := splitSemanticVersion(actual)
	minimumCore, minimumPre, minimumOK := splitSemanticVersion(minimum)
	if !actualOK || !minimumOK {
		return false
	}
	for index := 0; index < 3; index++ {
		if actualCore[index] != minimumCore[index] {
			return actualCore[index] > minimumCore[index]
		}
	}
	if actualPre == "" {
		return true
	}
	if minimumPre == "" {
		return false
	}
	return comparePrerelease(actualPre, minimumPre) >= 0
}

// splitSemanticVersion 拆分 X.Y.Z-pre+build，构建元数据不参与优先级比较。
func splitSemanticVersion(version string) ([3]int, string, bool) {
	var core [3]int
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if buildIndex := strings.IndexByte(version, '+'); buildIndex >= 0 {
		version = version[:buildIndex]
	}
	prerelease := ""
	if prereleaseIndex := strings.IndexByte(version, '-'); prereleaseIndex >= 0 {
		prerelease = version[prereleaseIndex+1:]
		version = version[:prereleaseIndex]
	}
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return core, "", false
	}
	for index, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 {
			return core, "", false
		}
		core[index] = parsed
	}
	if strings.Contains(prerelease, "..") {
		return core, "", false
	}
	return core, prerelease, true
}

// comparePrerelease 比较语义版本预发布标识；数字标识低于非数字标识。
func comparePrerelease(left, right string) int {
	leftParts := strings.Split(left, ".")
	rightParts := strings.Split(right, ".")
	for index := 0; index < len(leftParts) && index < len(rightParts); index++ {
		leftNumber, leftErr := strconv.Atoi(leftParts[index])
		rightNumber, rightErr := strconv.Atoi(rightParts[index])
		switch {
		case leftErr == nil && rightErr == nil:
			if leftNumber < rightNumber {
				return -1
			}
			if leftNumber > rightNumber {
				return 1
			}
		case leftErr == nil:
			return -1
		case rightErr == nil:
			return 1
		default:
			if leftParts[index] < rightParts[index] {
				return -1
			}
			if leftParts[index] > rightParts[index] {
				return 1
			}
		}
	}
	switch {
	case len(leftParts) < len(rightParts):
		return -1
	case len(leftParts) > len(rightParts):
		return 1
	default:
		return 0
	}
}

// singBoxCandidatePaths 返回数据目录和 PATH 中的全部候选，而不是只取 PATH 的第一个命中。
// 第一个 PATH 文件可能仍可执行却已损坏或降级，继续扫描才能回退到后面的合格安装。
func singBoxCandidatePaths() []string {
	name := singBoxBinName()
	candidates := make([]string, 0, 4)
	seen := make(map[string]struct{})
	appendCandidate := func(candidate string) {
		if candidate == "" {
			return
		}
		key := filepath.Clean(candidate)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, candidate)
	}

	appendCandidate(filepath.Join(mihomoCacheDir, name))
	// 保留 LookPath 的平台规则和首选结果，再补扫后续 PATH 目录以支持失效后的回退。
	if pathValue, err := exec.LookPath(name); err == nil {
		appendCandidate(pathValue)
	}
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if directory == "" {
			continue
		}
		appendCandidate(filepath.Join(directory, name))
	}
	return candidates
}

// EnsureSingBox 定位 Snell v6 专用内核；Docker 镜像内已固定版本，不在运行时下载内核。
// 显式 SING_BOX_BIN 与 MIHOMO_BIN 一样采用失败关闭：候选里的 $MMWX_DATA_DIR/bin 是用户可写的
// 数据卷，静默回退等于允许卷里的文件顶掉镜像自带的内核，而调用方只会看到一次普通的测速失败。
func EnsureSingBox(_ context.Context) (string, error) {
	singBoxMu.Lock()
	defer singBoxMu.Unlock()

	if explicit := strings.TrimSpace(os.Getenv("SING_BOX_BIN")); explicit != "" {
		if !fileExists(explicit) {
			return "", fmt.Errorf("SING_BOX_BIN 指向的 sing-box 不存在: %s", explicit)
		}
		if !singBoxSupported(explicit) {
			return "", fmt.Errorf("SING_BOX_BIN 指向的 sing-box 不可执行或版本低于 %s: %s", minSingBoxVersion, explicit)
		}
		singBoxCachedPath = explicit
		return explicit, nil
	}
	// 数据目录和 PATH 都可能在进程运行期间被原位替换。缓存命中时必须重新执行版本校验，
	// 否则已降级、损坏或失去执行权限的文件会持续污染后续所有 Snell v6 任务。
	if singBoxCachedPath != "" {
		if fileExists(singBoxCachedPath) && singBoxSupported(singBoxCachedPath) {
			return singBoxCachedPath, nil
		}
		singBoxCachedPath = ""
	}
	for _, candidate := range singBoxCandidatePaths() {
		if !fileExists(candidate) || !singBoxSupported(candidate) {
			continue
		}
		singBoxCachedPath = candidate
		return candidate, nil
	}
	return "", fmt.Errorf("未找到 sing-box %s+；请使用双内核镜像或设置 SING_BOX_BIN", minSingBoxVersion)
}

// buildSingBoxConfig 将单个 Snell v6 Clash 节点转换成最小 sing-box 配置。
func buildSingBoxConfig(proxy map[string]any, mixedPort int) ([]byte, error) {
	server := strings.TrimSpace(stringValue(proxy["server"]))
	if server == "" {
		return nil, fmt.Errorf("Snell v6 缺少 server")
	}
	port, err := integerValue(proxy["port"])
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("Snell v6 port 必须在 1-65535 之间")
	}
	if mixedPort < 1 || mixedPort > 65535 {
		return nil, fmt.Errorf("本地 mixed 端口无效")
	}
	psk := stringValue(proxy["psk"])
	if length := len([]byte(psk)); length < 12 || length > 255 {
		return nil, fmt.Errorf("Snell v6 PSK 长度必须在 12-255 字节之间")
	}
	userKey := stringValue(proxy["_userkey"])
	if userKey == "" {
		userKey = stringValue(proxy["userkey"])
	}
	if len([]byte(userKey)) > 255 {
		return nil, fmt.Errorf("Snell v6 userkey 不能超过 255 字节")
	}
	mode := strings.ToLower(strings.TrimSpace(stringValue(proxy["mode"])))
	if mode == "" {
		mode = "default"
	}
	switch mode {
	case "default", "unshaped", "unsafe-raw":
	default:
		return nil, fmt.Errorf("Snell v6 mode 仅支持 default、unshaped 或 unsafe-raw")
	}

	outbound := map[string]any{
		"type":        "snell",
		"tag":         "snell-out",
		"server":      server,
		"server_port": port,
		"version":     6,
		"psk":         psk,
		"network":     "tcp",
		"mode":        mode,
	}
	if userKey != "" {
		outbound["userkey"] = userKey
	}
	// 测速任务会为单个节点启动短生命周期内核，出口 IP、延迟和下载探测随后连续建立连接。
	// 现场 reuse=true 时，大文件首请求在短探测后稳定收到 EOF；测速不依赖跨请求复用，因此固定省略该字段。
	if boolValue(proxy["tfo"]) || boolValue(proxy["tcp_fast_open"]) || boolValue(proxy["tcp-fast-open"]) {
		outbound["tcp_fast_open"] = true
	}

	config := map[string]any{
		"log": map[string]any{
			"level":     "warn",
			"timestamp": true,
		},
		"dns": map[string]any{
			"servers": []map[string]any{{"type": "local", "tag": "local"}},
			"final":   "local",
		},
		"inbounds": []map[string]any{{
			"type":        "mixed",
			"tag":         "mixed-in",
			"listen":      "127.0.0.1",
			"listen_port": mixedPort,
		}},
		"outbounds": []map[string]any{outbound},
		"route": map[string]any{
			"final":                   "snell-out",
			"default_domain_resolver": "local",
		},
	}
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("生成 sing-box 配置失败: %w", err)
	}
	return encoded, nil
}
