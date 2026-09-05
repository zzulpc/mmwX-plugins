package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPinnedMihomoAssets覆盖发布平台(t *testing.T) {
	expectedAssets := []struct {
		platform string
		name     string
	}{
		{"darwin/amd64", "mihomo-darwin-amd64-compatible-v" + pinnedMihomoVersion + ".gz"},
		{"darwin/arm64", "mihomo-darwin-arm64-v" + pinnedMihomoVersion + ".gz"},
		{"linux/amd64", "mihomo-linux-amd64-compatible-v" + pinnedMihomoVersion + ".gz"},
		{"linux/arm64", "mihomo-linux-arm64-v" + pinnedMihomoVersion + ".gz"},
		{"windows/amd64", "mihomo-windows-amd64-compatible-v" + pinnedMihomoVersion + ".zip"},
		{"windows/arm64", "mihomo-windows-arm64-v" + pinnedMihomoVersion + ".zip"},
	}
	if len(pinnedMihomoAssets) != len(expectedAssets) {
		t.Fatalf("固定资产数=%d，发布平台数=%d；新增平台必须同时补齐精确名称测试",
			len(pinnedMihomoAssets), len(expectedAssets))
	}
	for _, expected := range expectedAssets {
		spec, ok := pinnedMihomoAssets[expected.platform]
		if !ok {
			t.Fatalf("缺少平台 %s 的固定资产", expected.platform)
		}
		if spec.name != expected.name {
			t.Fatalf("平台 %s 的资产名=%q，期望精确等于 %q", expected.platform, spec.name, expected.name)
		}
		if len(spec.sha256) != 64 {
			t.Fatalf("平台 %s 的 SHA-256 长度为 %d", expected.platform, len(spec.sha256))
		}
	}
}

func TestInstallMihomoArchive拒绝摘要不匹配(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "mihomo-test.gz")
	if err := os.WriteFile(archivePath, []byte("not-the-pinned-asset"), 0600); err != nil {
		t.Fatalf("写入测试压缩包失败: %v", err)
	}
	dst := filepath.Join(dir, "mihomo")
	spec := mihomoAssetSpec{
		name:   "mihomo-test.gz",
		sha256: strings.Repeat("0", 64),
	}

	err := installMihomoArchive(archivePath, spec, dst)
	if err == nil || !strings.Contains(err.Error(), "SHA-256 不匹配") {
		t.Fatalf("期望 SHA-256 不匹配错误，实际为 %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("摘要失败后目标路径不应存在，stat 错误为 %v", err)
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".mihomo-extract-*"))
	if err != nil {
		t.Fatalf("检查解压临时文件失败: %v", err)
	}
	if len(temps) != 0 {
		t.Fatalf("摘要失败后残留了解压临时文件: %v", temps)
	}
}

func TestEnsureMihomo显式路径优先于数据目录(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("此用例用 POSIX 脚本模拟可执行内核")
	}
	originalCacheDir := mihomoCacheDir
	originalCachedPath := cachedPath
	t.Cleanup(func() {
		mihomoCacheDir = originalCacheDir
		cachedPath = originalCachedPath
	})

	dir := t.TempDir()
	explicit := filepath.Join(dir, "explicit-mihomo")
	localDir := filepath.Join(dir, "data", "bin")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatalf("创建测试缓存目录失败: %v", err)
	}
	createFakeMihomo(t, explicit, "1.19.30")
	createFakeMihomo(t, filepath.Join(localDir, mihomoBinName()), "1.19.30")
	t.Setenv("MIHOMO_BIN", explicit)
	mihomoCacheDir = localDir
	cachedPath = ""

	got, err := EnsureMihomo(context.Background())
	if err != nil {
		t.Fatalf("定位显式 Mihomo 失败: %v", err)
	}
	if got != explicit {
		t.Fatalf("显式 Mihomo 未优先: got=%q want=%q", got, explicit)
	}
}

func TestEnsureMihomo显式路径无效时失败关闭(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("此用例用 POSIX 脚本模拟可执行内核")
	}
	originalCacheDir := mihomoCacheDir
	originalCachedPath := cachedPath
	t.Cleanup(func() {
		mihomoCacheDir = originalCacheDir
		cachedPath = originalCachedPath
	})

	dir := t.TempDir()
	localDir := filepath.Join(dir, "data", "bin")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatalf("创建测试缓存目录失败: %v", err)
	}
	local := filepath.Join(localDir, mihomoBinName())
	createFakeMihomo(t, local, "1.19.30")
	missing := filepath.Join(dir, "missing-mihomo")
	t.Setenv("MIHOMO_BIN", missing)
	mihomoCacheDir = localDir
	cachedPath = local

	_, err := EnsureMihomo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "MIHOMO_BIN") || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("无效显式路径应直接失败，实际为 %v", err)
	}
	if cachedPath != local {
		t.Fatalf("失败关闭不应改写已有缓存状态: cachedPath=%q", cachedPath)
	}
}

func TestEnsureMihomo显式路径版本过低时失败关闭(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("此用例用 POSIX 脚本模拟可执行内核")
	}
	originalCacheDir := mihomoCacheDir
	originalCachedPath := cachedPath
	t.Cleanup(func() {
		mihomoCacheDir = originalCacheDir
		cachedPath = originalCachedPath
	})

	dir := t.TempDir()
	localDir := filepath.Join(dir, "data", "bin")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatalf("创建测试缓存目录失败: %v", err)
	}
	explicit := filepath.Join(dir, "old-mihomo")
	createFakeMihomo(t, explicit, "1.0.0")
	createFakeMihomo(t, filepath.Join(localDir, mihomoBinName()), "1.19.30")
	t.Setenv("MIHOMO_BIN", explicit)
	mihomoCacheDir = localDir
	cachedPath = ""

	_, err := EnsureMihomo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "版本低于") {
		t.Fatalf("过低的显式版本应直接失败，实际为 %v", err)
	}
	if cachedPath != "" {
		t.Fatalf("版本失败后不应回落到数据目录: cachedPath=%q", cachedPath)
	}
}

func TestEnsureMihomo显式路径不可执行时失败关闭(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("此用例检查 POSIX 执行权限")
	}
	originalCacheDir := mihomoCacheDir
	originalCachedPath := cachedPath
	t.Cleanup(func() {
		mihomoCacheDir = originalCacheDir
		cachedPath = originalCachedPath
	})

	dir := t.TempDir()
	broken := filepath.Join(dir, "broken-mihomo")
	if err := os.WriteFile(broken, []byte("不是可执行文件"), 0600); err != nil {
		t.Fatalf("创建不可执行 Mihomo 失败: %v", err)
	}
	t.Setenv("MIHOMO_BIN", broken)
	cachedPath = ""

	_, err := EnsureMihomo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "不可执行") {
		t.Fatalf("不可执行的显式内核应直接失败，实际为 %v", err)
	}
}

func TestDockerfile内置固定Mihomo(t *testing.T) {
	dockerfile, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatalf("读取 Dockerfile 失败: %v", err)
	}
	content := string(dockerfile)
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		spec := pinnedMihomoAssets[platform]
		for _, expected := range []string{spec.name, spec.sha256} {
			if !strings.Contains(content, expected) {
				t.Fatalf("Dockerfile 未固定 %s 的 %q", platform, expected)
			}
		}
	}
	if !strings.Contains(content, "MIHOMO_BIN=/usr/local/bin/mihomo") {
		t.Fatal("Dockerfile 未强制容器使用镜像内置 Mihomo")
	}
	for _, expected := range []string{
		"COPY --from=mihomo /out/mihomo /usr/local/bin/mihomo",
		"/usr/local/bin/mihomo -v",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("Dockerfile 缺少内置 Mihomo 验证步骤 %q", expected)
		}
	}
}

func createFakeMihomo(t *testing.T, path, version string) {
	t.Helper()
	script := []byte("#!/bin/sh\nprintf 'Mihomo Meta v" + version + "\\n'\n")
	if err := os.WriteFile(path, script, 0700); err != nil {
		t.Fatalf("创建假 Mihomo 失败: %v", err)
	}
}

func TestDataDir环境变量不依赖CWD(t *testing.T) {
	originalCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("读取当前目录失败: %v", err)
	}
	originalCacheDir := mihomoCacheDir
	originalCachedPath := cachedPath
	originalSingBoxCachedPath := singBoxCachedPath
	t.Cleanup(func() {
		_ = os.Chdir(originalCWD)
		mihomoCacheDir = originalCacheDir
		cachedPath = originalCachedPath
		singBoxCachedPath = originalSingBoxCachedPath
	})

	dataRoot := t.TempDir()
	firstCWD := t.TempDir()
	secondCWD := t.TempDir()
	t.Setenv("MMWX_DATA_DIR", dataRoot)
	if err := os.Chdir(firstCWD); err != nil {
		t.Fatalf("切换首次目录失败: %v", err)
	}
	cachedPath = "/旧/mihomo"
	singBoxCachedPath = "/旧/sing-box"
	if err := configureDataDir(""); err != nil {
		t.Fatalf("配置环境变量数据目录失败: %v", err)
	}
	want := filepath.Join(dataRoot, "bin")
	if mihomoCacheDir != want {
		t.Fatalf("缓存目录为 %q，期望 %q", mihomoCacheDir, want)
	}
	if cachedPath != "" || singBoxCachedPath != "" {
		t.Fatalf("切换数据目录后仍残留内核定位缓存: mihomo=%q sing-box=%q", cachedPath, singBoxCachedPath)
	}

	if err := os.Chdir(secondCWD); err != nil {
		t.Fatalf("切换第二个目录失败: %v", err)
	}
	if mihomoCacheDir != want {
		t.Fatalf("cwd 改变后缓存目录漂移为 %q，期望保持 %q", mihomoCacheDir, want)
	}
}

func TestDataDir显式参数优先于环境变量(t *testing.T) {
	explicitRoot := t.TempDir()
	t.Setenv("MMWX_DATA_DIR", t.TempDir())

	got, err := resolveKernelCacheDir(explicitRoot)
	if err != nil {
		t.Fatalf("解析显式数据目录失败: %v", err)
	}
	want := filepath.Join(explicitRoot, "bin")
	if got != want {
		t.Fatalf("显式目录未覆盖环境变量: got=%q want=%q", got, want)
	}
}

func TestDataDir默认使用可执行文件目录(t *testing.T) {
	originalCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("读取当前目录失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalCWD) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("切换空启动目录失败: %v", err)
	}
	t.Setenv("MMWX_DATA_DIR", "")

	got, err := resolveKernelCacheDir("")
	if err != nil {
		t.Fatalf("解析默认数据目录失败: %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("读取测试可执行文件路径失败: %v", err)
	}
	want := filepath.Join(filepath.Dir(executable), "data", "bin")
	if got != want {
		t.Fatalf("默认缓存未锚定可执行文件目录: got=%q want=%q", got, want)
	}
}

func TestDataDir默认沿用旧内核目录(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("此用例用 POSIX 脚本模拟可运行内核，Windows 由损坏文件拒绝用例覆盖失败分支")
	}
	originalCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("读取当前目录失败: %v", err)
	}
	originalCacheDir := mihomoCacheDir
	originalCachedPath := cachedPath
	originalSingBoxCachedPath := singBoxCachedPath
	t.Cleanup(func() {
		_ = os.Chdir(originalCWD)
		mihomoCacheDir = originalCacheDir
		cachedPath = originalCachedPath
		singBoxCachedPath = originalSingBoxCachedPath
	})

	legacyRoot := t.TempDir()
	if err := os.Chdir(legacyRoot); err != nil {
		t.Fatalf("切换旧目录失败: %v", err)
	}
	// macOS 的临时目录可能以 /var 与 /private/var 两种等价路径出现，按切换后的 cwd 构造期望值。
	activeLegacyRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("读取旧目录规范路径失败: %v", err)
	}
	t.Setenv("MMWX_DATA_DIR", "")
	legacyBin := filepath.Join(activeLegacyRoot, legacyKernelCacheDir)
	if err := os.MkdirAll(legacyBin, 0755); err != nil {
		t.Fatalf("创建旧内核目录失败: %v", err)
	}
	fakeMihomo := []byte("#!/bin/sh\nprintf 'Mihomo Meta v1.19.30\\n'\n")
	if err := os.WriteFile(filepath.Join(legacyBin, mihomoBinName()), fakeMihomo, 0700); err != nil {
		t.Fatalf("创建可运行的旧内核失败: %v", err)
	}

	if err := configureDataDir(""); err != nil {
		t.Fatalf("解析默认数据目录失败: %v", err)
	}
	if mihomoCacheDir != legacyBin {
		t.Fatalf("未沿用旧内核目录: got=%q want=%q", mihomoCacheDir, legacyBin)
	}
}

func TestDataDir不沿用损坏旧内核(t *testing.T) {
	originalCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("读取当前目录失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalCWD) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("切换旧目录失败: %v", err)
	}
	t.Setenv("MMWX_DATA_DIR", "")
	if err := os.MkdirAll(legacyKernelCacheDir, 0755); err != nil {
		t.Fatalf("创建旧内核目录失败: %v", err)
	}
	damaged := filepath.Join(legacyKernelCacheDir, mihomoBinName())
	if err := os.WriteFile(damaged, []byte("不是可执行内核"), 0700); err != nil {
		t.Fatalf("创建损坏旧内核失败: %v", err)
	}

	got, err := resolveKernelCacheDir("")
	if err != nil {
		t.Fatalf("解析默认数据目录失败: %v", err)
	}
	if got != defaultKernelCacheDir() {
		t.Fatalf("损坏旧内核错误绑定了旧目录: got=%q want=%q", got, defaultKernelCacheDir())
	}
	if mihomoSupportsSnell(damaged) {
		t.Fatal("损坏旧内核被判定为可用")
	}
}
