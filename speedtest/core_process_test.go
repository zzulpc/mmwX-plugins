package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestBoundedTailBufferKeepsOnlyTail(t *testing.T) {
	buffer := newBoundedTailBuffer(32)
	payload := strings.Repeat("a", 10<<20) + "EXPECTED_TAIL"
	if _, err := buffer.Write([]byte(payload)); err != nil {
		t.Fatalf("写入有界缓冲失败: %v", err)
	}
	content := buffer.String()
	if len(content) > 32 || !strings.HasSuffix(content, "EXPECTED_TAIL") {
		t.Fatalf("有界缓冲未正确保留末尾: 长度=%d 内容=%q", len(content), content)
	}
}

func TestSanitizeCoreOutput(t *testing.T) {
	secret := "TEST_PSK_SENTINEL"
	raw := "\x1b[31mpsk=" + secret + "\x1b[0m\nuserkey: another-secret\x00"
	actual := sanitizeCoreOutput(raw, []string{secret, "another-secret"})
	if strings.Contains(actual, secret) || strings.Contains(actual, "another-secret") {
		t.Fatalf("内核错误仍包含密钥: %q", actual)
	}
	if strings.Contains(actual, "\x1b") || strings.ContainsRune(actual, '\x00') {
		t.Fatalf("内核错误仍包含控制字符: %q", actual)
	}
}

func TestSanitizeNestedAndEscapedSecrets(t *testing.T) {
	privateKey := "line one\nline two"
	authorization := "Bearer TOKEN WITH SPACES"
	cookie := "session=abc-123"
	proxy := map[string]any{
		"plugin-opts": map[string]any{
			"headers": map[string]any{
				"Authorization": authorization,
				"Cookie":        cookie,
			},
			"private-key": privateKey,
		},
	}
	secrets := collectProxySecrets(proxy)
	raw := "authorization=\"" + authorization + "\" private_key=\"line one\\nline two\" cookie='" + cookie + "'"
	actual := sanitizeCoreOutput(raw, secrets)
	for _, secret := range []string{authorization, cookie, "line one", "line two", "abc-123"} {
		if strings.Contains(actual, secret) {
			t.Fatalf("嵌套或转义密钥仍出现在错误中: %q", actual)
		}
	}
}

func TestSanitizeOverlappingAndExistingAuthFields(t *testing.T) {
	shortSecret := "abc"
	longSecret := "abc def"
	preSharedKey := "pre-shared-key-value"
	proxy := map[string]any{
		"auth-str":       shortSecret,
		"token":          longSecret,
		"pre-shared-key": preSharedKey,
	}
	secrets := collectProxySecrets(proxy)
	actual := sanitizeCoreOutput("auth-str="+shortSecret+" token=\""+longSecret+"\" pre_shared_key="+preSharedKey, secrets)
	for _, secretPart := range []string{shortSecret, "def", preSharedKey} {
		if strings.Contains(actual, secretPart) {
			t.Fatalf("重叠密钥或既有认证字段仍出现在错误中: %q", actual)
		}
	}
}

func TestRunNodeTestFailureRedactsAndCleansTemp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("该测试使用 POSIX 测试脚本")
	}
	root := t.TempDir()
	scriptPath := filepath.Join(root, "fake-mihomo.sh")
	script := "#!/bin/sh\nprintf '%s\\n' 'password=TEST_PSK_SENTINEL' >&2\nexit 1\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0700); err != nil {
		t.Fatalf("写入测试脚本失败: %v", err)
	}
	taskTemp := filepath.Join(root, "tasks")
	if err := os.Mkdir(taskTemp, 0700); err != nil {
		t.Fatalf("创建任务临时根目录失败: %v", err)
	}
	t.Setenv("TMPDIR", taskTemp)
	raw := `{"name":"测试节点","type":"ss","server":"127.0.0.1","port":1,"cipher":"aes-128-gcm","password":"TEST_PSK_SENTINEL"}`
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := RunNodeTest(ctx, proxyRuntime{Core: coreMihomo, Bin: scriptPath}, raw, Options{LatencyOnly: true, Timeout: 2 * time.Second})
	if err == nil {
		t.Fatal("伪内核立即退出时应返回错误")
	}
	if strings.Contains(err.Error(), "TEST_PSK_SENTINEL") {
		t.Fatalf("返回错误泄露了密码: %v", err)
	}
	entries, readErr := os.ReadDir(taskTemp)
	if readErr != nil {
		t.Fatalf("读取任务临时根目录失败: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("失败路径残留临时目录: %#v", entries)
	}
}

func TestSanitizeCoreOutput截断保持UTF8边界(t *testing.T) {
	// 中文诊断超长时，按字节取尾部几乎必然切在某个汉字中间。
	// 这段要 JSON 回传主控，非法 UTF-8 会被 encoding/json 换成 U+FFFD，主控界面上就是乱码。
	const unit = "配置校验失败：本地端口被占用。"
	raw := strings.Repeat(unit, 400)
	sanitized := sanitizeCoreOutput(raw, nil)

	if len(sanitized) > coreErrorMaxBytes {
		t.Fatalf("截断后长度 %d 超过上限 %d", len(sanitized), coreErrorMaxBytes)
	}
	if !utf8.ValidString(sanitized) {
		t.Fatalf("截断切断了多字节字符，开头为 %q", sanitized[:min(16, len(sanitized))])
	}
	// 前移只应丢掉半个字符，不能顺手吃掉整段内容。
	if len(sanitized) < coreErrorMaxBytes-utf8.UTFMax {
		t.Fatalf("截断丢弃过多: 实际 %d，至少应保留 %d", len(sanitized), coreErrorMaxBytes-utf8.UTFMax)
	}
	if !strings.HasSuffix(sanitized, unit) {
		t.Fatalf("保留的应当是输出尾部，实际结尾为 %q", sanitized[max(0, len(sanitized)-len(unit)):])
	}
}

func TestSanitizeCoreOutput未超长时原样保留(t *testing.T) {
	raw := "配置校验失败：端口 7890 已被占用。"
	if sanitized := sanitizeCoreOutput(raw, nil); sanitized != raw {
		t.Fatalf("未超长的输出不应被改动: 实际 %q", sanitized)
	}
}
