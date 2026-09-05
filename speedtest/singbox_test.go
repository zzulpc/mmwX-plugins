package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const testPSK = "TEST_PSK_1234567890"

func validSnellV6Proxy() map[string]any {
	return map[string]any{
		"name":          "测试 Snell v6",
		"type":          "snell",
		"server":        "2001:db8::10",
		"port":          443,
		"version":       6,
		"psk":           testPSK,
		"_userkey":      "测试-user-key",
		"reuse":         true,
		"mode":          "unshaped",
		"tcp-fast-open": true,
	}
}

func decodeConfigMap(t *testing.T, content []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(content, &decoded); err != nil {
		t.Fatalf("解析生成配置失败: %v", err)
	}
	return decoded
}

func TestBuildSingBoxConfigMapsSnellV6(t *testing.T) {
	content, err := buildSingBoxConfig(validSnellV6Proxy(), 19001)
	if err != nil {
		t.Fatalf("生成配置失败: %v", err)
	}
	decoded := decodeConfigMap(t, content)
	inbounds := decoded["inbounds"].([]any)
	inbound := inbounds[0].(map[string]any)
	if inbound["listen"] != "127.0.0.1" || inbound["listen_port"] != float64(19001) {
		t.Fatalf("mixed 入站未限制在预期回环端口: %#v", inbound)
	}
	outbounds := decoded["outbounds"].([]any)
	outbound := outbounds[0].(map[string]any)
	checks := map[string]any{
		"type":          "snell",
		"server":        "2001:db8::10",
		"server_port":   float64(443),
		"version":       float64(6),
		"psk":           testPSK,
		"userkey":       "测试-user-key",
		"mode":          "unshaped",
		"network":       "tcp",
		"tcp_fast_open": true,
	}
	for key, expected := range checks {
		if outbound[key] != expected {
			t.Errorf("字段 %s 不符: 实际 %#v，预期 %#v", key, outbound[key], expected)
		}
	}
	if _, exists := outbound["reuse"]; exists {
		t.Fatalf("短生命周期 Snell v6 测速配置不应透传 reuse=true: %#v", outbound)
	}
	route := decoded["route"].(map[string]any)
	if route["final"] != "snell-out" || route["default_domain_resolver"] != "local" {
		t.Fatalf("路由配置不符: %#v", route)
	}
	dnsConfig := decoded["dns"].(map[string]any)
	if dnsConfig["final"] != "local" {
		t.Fatalf("本地域名解析配置不符: %#v", dnsConfig)
	}
}

func TestBuildSingBoxConfigDefaultsAndEscapesSecrets(t *testing.T) {
	proxy := validSnellV6Proxy()
	delete(proxy, "mode")
	delete(proxy, "_userkey")
	proxy["userkey"] = "引号\"与反斜杠\\"
	proxy["psk"] = "PSK_带引号\"_123456789"
	content, err := buildSingBoxConfig(proxy, 19002)
	if err != nil {
		t.Fatalf("生成配置失败: %v", err)
	}
	decoded := decodeConfigMap(t, content)
	outbound := decoded["outbounds"].([]any)[0].(map[string]any)
	if outbound["mode"] != "default" {
		t.Fatalf("缺省 mode 应为 default，实际为 %#v", outbound["mode"])
	}
	if outbound["psk"] != proxy["psk"] || outbound["userkey"] != proxy["userkey"] {
		t.Fatal("JSON 序列化后密钥字段发生变化")
	}
}

func TestBuildSingBoxConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "缺少服务器", mutate: func(proxy map[string]any) { delete(proxy, "server") }},
		{name: "端口为零", mutate: func(proxy map[string]any) { proxy["port"] = 0 }},
		{name: "端口过大", mutate: func(proxy map[string]any) { proxy["port"] = 70000 }},
		{name: "PSK 太短", mutate: func(proxy map[string]any) { proxy["psk"] = "short" }},
		{name: "userkey 太长", mutate: func(proxy map[string]any) { proxy["_userkey"] = strings.Repeat("a", 256) }},
		{name: "未知 mode", mutate: func(proxy map[string]any) { proxy["mode"] = "unknown" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proxy := validSnellV6Proxy()
			test.mutate(proxy)
			if _, err := buildSingBoxConfig(proxy, 19003); err == nil {
				t.Fatal("无效配置不应生成成功")
			}
		})
	}
}

func TestBuildSingBoxConfigAcceptsServersAndModes(t *testing.T) {
	servers := []string{"198.51.100.10", "2001:db8::20", "snell.example.test"}
	modes := []string{"default", "unshaped", "unsafe-raw"}
	for _, server := range servers {
		for _, mode := range modes {
			proxy := validSnellV6Proxy()
			proxy["server"] = server
			proxy["mode"] = mode
			if _, err := buildSingBoxConfig(proxy, 19005); err != nil {
				t.Fatalf("服务器 %q、mode %q 应可生成配置: %v", server, mode, err)
			}
		}
	}
}

func TestSemanticVersionGTE(t *testing.T) {
	tests := []struct {
		version  string
		expected bool
	}{
		{version: "1.14.0-alpha.30", expected: false},
		{version: "1.14.0-beta.16", expected: false},
		{version: "1.14.0-beta.17", expected: true},
		{version: "1.14.0-beta.18", expected: true},
		{version: "1.14.0-rc.1", expected: true},
		{version: "1.14.0", expected: true},
		{version: "1.14.1-alpha.1", expected: true},
		{version: "1.13.99", expected: false},
	}
	for _, test := range tests {
		if actual := semanticVersionGTE(test.version, minSingBoxVersion); actual != test.expected {
			t.Errorf("版本 %s 判断错误: 实际 %v，预期 %v", test.version, actual, test.expected)
		}
	}
}

func TestGeneratedConfigWithInstalledSingBox(t *testing.T) {
	bin := installedTestSingBox(t)
	// 内核从测试版升级到正式版时逐个校验已支持的模式，避免只验证单个样本漏掉兼容变化。
	for _, mode := range []string{"default", "unshaped", "unsafe-raw"} {
		t.Run(mode, func(t *testing.T) {
			proxy := validSnellV6Proxy()
			proxy["mode"] = mode
			content, err := buildSingBoxConfig(proxy, 19004)
			if err != nil {
				t.Fatalf("生成配置失败: %v", err)
			}
			workdir := t.TempDir()
			configPath := filepath.Join(workdir, "config.json")
			if err := os.WriteFile(configPath, content, 0600); err != nil {
				t.Fatalf("写入测试配置失败: %v", err)
			}
			if err := checkSingBoxConfig(context.Background(), proxyRuntime{Core: coreSingBox, Bin: bin}, workdir, configPath, []string{testPSK}); err != nil {
				t.Fatalf("官方 sing-box 配置校验失败: %v", err)
			}
		})
	}
}

func TestStartInstalledSingBox(t *testing.T) {
	bin := installedTestSingBox(t)
	mixedPort, err := reserveLoopbackPort()
	if err != nil {
		t.Fatalf("分配测试端口失败: %v", err)
	}
	content, err := buildSingBoxConfig(validSnellV6Proxy(), mixedPort)
	if err != nil {
		t.Fatalf("生成配置失败: %v", err)
	}
	workdir := t.TempDir()
	configPath := filepath.Join(workdir, "config.json")
	if err := os.WriteFile(configPath, content, 0600); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stop, err := startProxyCore(ctx, proxyRuntime{Core: coreSingBox, Bin: bin}, workdir, configPath, []string{testPSK}, mixedPort)
	if err != nil {
		t.Fatalf("启动官方 sing-box 失败: %v", err)
	}
	stop()
}

func installedTestSingBox(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("SING_BOX_BIN")
	if bin == "" {
		var err error
		bin, err = exec.LookPath(singBoxBinName())
		if err != nil {
			t.Skip("本机未安装 sing-box，跳过官方内核配置校验")
		}
	}
	if !singBoxSupported(bin) {
		t.Skip("本机 sing-box 版本低于 1.14，跳过 Snell v6 配置校验")
	}
	return bin
}

// createFakeSingBox 用 POSIX 脚本模拟 `sing-box version` 的输出，
// 让内核定位用例不必真的安装官方内核（也就不会在 CI 里下载或访问外网）。
func createFakeSingBox(t *testing.T, path, version string) {
	t.Helper()
	script := []byte("#!/bin/sh\nprintf 'sing-box version " + version + "\\n'\n")
	if err := os.WriteFile(path, script, 0700); err != nil {
		t.Fatalf("创建假 sing-box 失败: %v", err)
	}
}

// withSingBoxLocatorState 隔离内核定位用到的全局状态，并清空 PATH 让候选顺序可预期。
func withSingBoxLocatorState(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("此用例用 POSIX 脚本模拟可执行内核")
	}
	originalCacheDir := mihomoCacheDir
	originalCachedPath := singBoxCachedPath
	t.Cleanup(func() {
		mihomoCacheDir = originalCacheDir
		singBoxCachedPath = originalCachedPath
	})
	dir := t.TempDir()
	// 置空 PATH，避免开发机上真的装了 sing-box 时用例结果随环境漂移。
	t.Setenv("PATH", filepath.Join(dir, "empty-path"))
	singBoxCachedPath = ""
	return dir
}

func TestEnsureSingBox显式路径优先于数据目录(t *testing.T) {
	dir := withSingBoxLocatorState(t)
	localDir := filepath.Join(dir, "data", "bin")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatalf("创建测试缓存目录失败: %v", err)
	}
	explicit := filepath.Join(dir, "explicit-sing-box")
	createFakeSingBox(t, explicit, minSingBoxVersion)
	createFakeSingBox(t, filepath.Join(localDir, singBoxBinName()), minSingBoxVersion)
	mihomoCacheDir = localDir
	t.Setenv("SING_BOX_BIN", explicit)

	got, err := EnsureSingBox(context.Background())
	if err != nil {
		t.Fatalf("定位显式 sing-box 失败: %v", err)
	}
	if got != explicit {
		t.Fatalf("显式 sing-box 未优先: got=%q want=%q", got, explicit)
	}
}

func TestEnsureSingBox显式路径不存在时失败关闭(t *testing.T) {
	dir := withSingBoxLocatorState(t)
	localDir := filepath.Join(dir, "data", "bin")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatalf("创建测试缓存目录失败: %v", err)
	}
	local := filepath.Join(localDir, singBoxBinName())
	createFakeSingBox(t, local, minSingBoxVersion)
	mihomoCacheDir = localDir
	singBoxCachedPath = local
	t.Setenv("SING_BOX_BIN", filepath.Join(dir, "missing-sing-box"))

	_, err := EnsureSingBox(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SING_BOX_BIN") || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("无效显式路径应直接失败，实际为 %v", err)
	}
	if singBoxCachedPath != local {
		t.Fatalf("失败关闭不应改写已有缓存状态: singBoxCachedPath=%q", singBoxCachedPath)
	}
}

func TestEnsureSingBox显式路径版本过低时失败关闭(t *testing.T) {
	dir := withSingBoxLocatorState(t)
	localDir := filepath.Join(dir, "data", "bin")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatalf("创建测试缓存目录失败: %v", err)
	}
	createFakeSingBox(t, filepath.Join(localDir, singBoxBinName()), minSingBoxVersion)
	explicit := filepath.Join(dir, "old-sing-box")
	createFakeSingBox(t, explicit, "1.13.99")
	mihomoCacheDir = localDir
	t.Setenv("SING_BOX_BIN", explicit)

	_, err := EnsureSingBox(context.Background())
	if err == nil || !strings.Contains(err.Error(), "版本低于") {
		t.Fatalf("过低的显式版本应直接失败，实际为 %v", err)
	}
	if singBoxCachedPath != "" {
		t.Fatalf("版本失败后不应回落到数据目录: singBoxCachedPath=%q", singBoxCachedPath)
	}
}

func TestEnsureSingBox显式路径不可执行时失败关闭(t *testing.T) {
	dir := withSingBoxLocatorState(t)
	broken := filepath.Join(dir, "broken-sing-box")
	if err := os.WriteFile(broken, []byte("不是可执行文件"), 0600); err != nil {
		t.Fatalf("创建不可执行 sing-box 失败: %v", err)
	}
	t.Setenv("SING_BOX_BIN", broken)

	_, err := EnsureSingBox(context.Background())
	if err == nil || !strings.Contains(err.Error(), "不可执行") {
		t.Fatalf("不可执行的显式内核应直接失败，实际为 %v", err)
	}
}

func TestEnsureSingBox未设显式路径时使用数据目录(t *testing.T) {
	dir := withSingBoxLocatorState(t)
	localDir := filepath.Join(dir, "data", "bin")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatalf("创建测试缓存目录失败: %v", err)
	}
	local := filepath.Join(localDir, singBoxBinName())
	createFakeSingBox(t, local, minSingBoxVersion)
	mihomoCacheDir = localDir
	t.Setenv("SING_BOX_BIN", "")

	got, err := EnsureSingBox(context.Background())
	if err != nil {
		t.Fatalf("定位数据目录 sing-box 失败: %v", err)
	}
	if got != local {
		t.Fatalf("未设显式路径时应使用数据目录: got=%q want=%q", got, local)
	}
}

func TestEnsureSingBox缓存原位失效后回退到合格候选(t *testing.T) {
	tests := []struct {
		name       string
		invalidate func(t *testing.T, path string)
	}{
		{
			name: "版本输出损坏",
			invalidate: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'not a semantic version\\n'\n"), 0700); err != nil {
					t.Fatalf("损坏假 sing-box 版本输出失败: %v", err)
				}
			},
		},
		{
			name: "版本降级",
			invalidate: func(t *testing.T, path string) {
				createFakeSingBox(t, path, "1.13.99")
			},
		},
		{
			name: "失去执行权限",
			invalidate: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatalf("撤销假 sing-box 执行权限失败: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := withSingBoxLocatorState(t)
			localDir := filepath.Join(dir, "data", "bin")
			fallbackDir := filepath.Join(dir, "fallback-bin")
			if err := os.MkdirAll(localDir, 0755); err != nil {
				t.Fatalf("创建测试缓存目录失败: %v", err)
			}
			if err := os.MkdirAll(fallbackDir, 0755); err != nil {
				t.Fatalf("创建回退内核目录失败: %v", err)
			}
			local := filepath.Join(localDir, singBoxBinName())
			fallback := filepath.Join(fallbackDir, singBoxBinName())
			createFakeSingBox(t, local, minSingBoxVersion)
			createFakeSingBox(t, fallback, minSingBoxVersion)
			mihomoCacheDir = localDir
			t.Setenv("SING_BOX_BIN", "")

			got, err := EnsureSingBox(context.Background())
			if err != nil {
				t.Fatalf("首次定位数据目录 sing-box 失败: %v", err)
			}
			if got != local {
				t.Fatalf("首次定位未使用数据目录: got=%q want=%q", got, local)
			}

			test.invalidate(t, local)
			// 失效文件仍位于原路径，只有重新执行版本命令才能识别并触发回退。
			t.Setenv("PATH", fallbackDir)
			got, err = EnsureSingBox(context.Background())
			if err != nil {
				t.Fatalf("缓存原位失效后未找到合格回退内核: %v", err)
			}
			if got != fallback {
				t.Fatalf("缓存原位失效后回退路径错误: got=%q want=%q", got, fallback)
			}
			if singBoxCachedPath != fallback {
				t.Fatalf("回退成功后未刷新缓存: singBoxCachedPath=%q", singBoxCachedPath)
			}
		})
	}
}

func TestEnsureSingBox首个PATH候选降级后继续寻找(t *testing.T) {
	dir := withSingBoxLocatorState(t)
	localDir := filepath.Join(dir, "data", "bin")
	firstDir := filepath.Join(dir, "first-bin")
	secondDir := filepath.Join(dir, "second-bin")
	for _, directory := range []string{localDir, firstDir, secondDir} {
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatalf("创建测试候选目录失败: %v", err)
		}
	}
	first := filepath.Join(firstDir, singBoxBinName())
	second := filepath.Join(secondDir, singBoxBinName())
	createFakeSingBox(t, first, minSingBoxVersion)
	createFakeSingBox(t, second, minSingBoxVersion)
	mihomoCacheDir = localDir
	t.Setenv("SING_BOX_BIN", "")
	t.Setenv("PATH", strings.Join([]string{firstDir, secondDir}, string(os.PathListSeparator)))

	got, err := EnsureSingBox(context.Background())
	if err != nil {
		t.Fatalf("首次定位 PATH 中的 sing-box 失败: %v", err)
	}
	if got != first {
		t.Fatalf("首次定位未选择 PATH 的第一个合格候选: got=%q want=%q", got, first)
	}

	createFakeSingBox(t, first, "1.13.99")
	got, err = EnsureSingBox(context.Background())
	if err != nil {
		t.Fatalf("首个 PATH 候选降级后未继续寻找: %v", err)
	}
	if got != second {
		t.Fatalf("首个 PATH 候选降级后回退错误: got=%q want=%q", got, second)
	}
}
