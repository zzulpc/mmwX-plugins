// Package speedtest 在主控本机用 mihomo 内核对节点测速(PRO 功能 speed_test 的 Phase 1)。
package main

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

const legacyKernelCacheDir = "data/bin"

// minMihomoVersion:snell v4/v5 支持自 mihomo v1.19.26 起(v1.19.25 及更早会报 "snell version error: 4")。
// 定位到的 mihomo 若低于此版本则跳过、重新下载最新,确保能对 snell 节点测速。
const minMihomoVersion = "1.19.26"

// pinnedMihomoVersion 固定自动下载版本，避免 releases/latest 漂移后执行未经审核的新产物。
const pinnedMihomoVersion = "1.19.30"

type mihomoAssetSpec struct {
	name   string
	sha256 string
}

// pinnedMihomoAssets 的摘要来自 MetaCubeX/mihomo v1.19.30 官方 Release 资产。
var pinnedMihomoAssets = map[string]mihomoAssetSpec{
	"darwin/amd64": {
		name:   "mihomo-darwin-amd64-compatible-v1.19.30.gz",
		sha256: "6e75de0732e8afabe413ff7c235e8f16226ce136672371c60787cbf9607402c5",
	},
	"darwin/arm64": {
		name:   "mihomo-darwin-arm64-v1.19.30.gz",
		sha256: "2c7f3a7904fa1cee291e124123e630e7b1ebd13765dd9bf26c0a28432004d9f4",
	},
	"linux/amd64": {
		name:   "mihomo-linux-amd64-compatible-v1.19.30.gz",
		sha256: "db214c7a2517e63c150d123178d16d102e03a241ccdae4e5e07ffbe9cf56c6f9",
	},
	"linux/arm64": {
		name:   "mihomo-linux-arm64-v1.19.30.gz",
		sha256: "58896873736d28628f66de3677c8654fa0f180662523148e136cff4f6e890069",
	},
	"windows/amd64": {
		name:   "mihomo-windows-amd64-compatible-v1.19.30.zip",
		sha256: "289fde5e29d37a5b3326480590d8b3551c5bf7f8737290355c19bce74d57a563",
	},
	"windows/arm64": {
		name:   "mihomo-windows-arm64-v1.19.30.zip",
		sha256: "b37c4b0259e85b020edc4215aa4c86052e21071cf520d4800364b21b4e2fc162",
	},
}

var mihomoVerRe = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)`)

// probeMihomoVersion 运行 `<bin> -v`，同时区分“可执行但版本格式非标准”和“根本无法执行”。
func probeMihomoVersion(bin string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-v").CombinedOutput()
	if err != nil {
		return "", false
	}
	m := mihomoVerRe.FindStringSubmatch(string(out))
	if m == nil {
		return "", true
	}
	return m[1] + "." + m[2] + "." + m[3], true
}

// versionGTE 比较点分版本 a >= b(仅比 X.Y.Z 前三段)。
func versionGTE(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return true
}

// mihomoSupportsSnell 检查 mihomo 版本 >= minMihomoVersion(确保支持 snell v4/v5)。
// 命令能执行但版本解析不到时保守接受，避免误伤非标准构建；命令本身失败则必须拒绝。
func mihomoSupportsSnell(bin string) bool {
	v, executable := probeMihomoVersion(bin)
	if !executable {
		return false
	}
	if v == "" {
		return true
	}
	return versionGTE(v, minMihomoVersion)
}

// mihomoBinName 平台相关的 mihomo 可执行文件名(Windows 带 .exe)。
func mihomoBinName() string {
	if runtime.GOOS == "windows" {
		return "mihomo.exe"
	}
	return "mihomo"
}

var (
	mihomoMu       sync.Mutex // 串行化定位/下载,避免并发重复下载
	cachedPath     string
	mihomoCacheDir = defaultKernelCacheDir()
)

// defaultKernelCacheDir 把默认缓存锚定在可执行文件旁，避免启动 cwd 改变后重复下载。
// os.Executable 极少数平台失败时才退回旧相对路径，确保调用方仍能获得可用候选。
func defaultKernelCacheDir() string {
	executable, err := os.Executable()
	if err != nil || executable == "" {
		return legacyKernelCacheDir
	}
	return filepath.Join(filepath.Dir(executable), "data", "bin")
}

// resolveKernelCacheDir 解析数据根目录。显式参数优先于环境变量，内核统一放在其 bin 子目录。
// 未显式配置时，如果旧 cwd/data/bin 已有内核则沿用；否则使用可执行文件旁的 data/bin。
func resolveKernelCacheDir(dataDir string) (string, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		dataDir = strings.TrimSpace(os.Getenv("MMWX_DATA_DIR"))
	}
	if dataDir != "" {
		absolute, err := filepath.Abs(dataDir)
		if err != nil {
			return "", fmt.Errorf("解析数据目录 %q: %w", dataDir, err)
		}
		return filepath.Join(absolute, "bin"), nil
	}

	legacy, err := filepath.Abs(legacyKernelCacheDir)
	if err == nil && legacyKernelPresent(legacy) {
		return legacy, nil
	}
	return defaultKernelCacheDir(), nil
}

// legacyKernelPresent 仅在旧目录里至少有一个可运行内核时沿用，损坏占位文件不能绑住目录选择。
func legacyKernelPresent(cacheDir string) bool {
	mihomo := filepath.Join(cacheDir, mihomoBinName())
	if fileExists(mihomo) && mihomoSupportsSnell(mihomo) {
		return true
	}
	singBox := filepath.Join(cacheDir, singBoxBinName())
	return fileExists(singBox) && singBoxSupported(singBox)
}

// configureDataDir 在启动预热前一次性切换数据目录，并清空旧目录对应的定位缓存。
func configureDataDir(dataDir string) error {
	cacheDir, err := resolveKernelCacheDir(dataDir)
	if err != nil {
		return err
	}

	mihomoMu.Lock()
	singBoxMu.Lock()
	mihomoCacheDir = cacheDir
	cachedPath = ""
	singBoxCachedPath = ""
	singBoxMu.Unlock()
	mihomoMu.Unlock()
	return nil
}

// EnsureMihomo 返回可用的 mihomo 二进制路径;按序尝试:env MIHOMO_BIN → 配置的数据目录 →
// $PATH → 从 GitHub 固定版本 Release 自动下载到数据目录。显式 MIHOMO_BIN 采用失败关闭，
// 让内置内核的容器即使文件异常也不会偷偷回退到运行时网络下载。
func EnsureMihomo(ctx context.Context) (string, error) {
	mihomoMu.Lock()
	defer mihomoMu.Unlock()

	// 每个候选都要求版本支持 snell(>= minMihomoVersion),否则跳过、最终下载固定版本。
	if p := strings.TrimSpace(os.Getenv("MIHOMO_BIN")); p != "" {
		// 环境变量是容器内置内核的强约束，即使进程先前定位过其它路径，也不能绕过失败关闭。
		if !fileExists(p) {
			return "", fmt.Errorf("MIHOMO_BIN 指向的 mihomo 不存在: %s", p)
		}
		if !mihomoSupportsSnell(p) {
			return "", fmt.Errorf("MIHOMO_BIN 指向的 mihomo 不可执行或版本低于 %s: %s", minMihomoVersion, p)
		}
		cachedPath = p
		return p, nil
	}
	if cachedPath != "" && fileExists(cachedPath) {
		return cachedPath, nil
	}
	local := filepath.Join(mihomoCacheDir, mihomoBinName())
	if fileExists(local) && mihomoSupportsSnell(local) {
		cachedPath = local
		return local, nil
	}
	if p, err := exec.LookPath("mihomo"); err == nil && mihomoSupportsSnell(p) {
		cachedPath = p
		return p, nil
	}
	// 自动下载已校验的固定版本。若缓存目录里是旧版会被覆盖。
	if err := downloadMihomo(ctx, local); err != nil {
		return "", fmt.Errorf("mihomo 不可用且自动下载失败: %w", err)
	}
	cachedPath = local
	return local, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// downloadMihomo 从 MetaCubeX/mihomo 固定 Release 下载当前平台资产，校验压缩包后再解压到 dst。
func downloadMihomo(ctx context.Context, dst string) error {
	goos, goarch := runtime.GOOS, runtime.GOARCH
	spec, ok := pinnedMihomoAssets[goos+"/"+goarch]
	if !ok {
		return fmt.Errorf("mihomo 固定版本不支持平台 %s/%s", goos, goarch)
	}

	rel, err := fetchPinnedRelease(ctx)
	if err != nil {
		return err
	}
	if rel.TagName != "v"+pinnedMihomoVersion {
		return fmt.Errorf("mihomo release 标签不符: 收到 %q，期望 v%s", rel.TagName, pinnedMihomoVersion)
	}
	assetURL := ""
	for _, asset := range rel.Assets {
		if asset.Name == spec.name {
			assetURL = asset.BrowserDownloadURL
			break
		}
	}
	if assetURL == "" {
		return fmt.Errorf("mihomo v%s 未找到固定资产 %s", pinnedMihomoVersion, spec.name)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return fmt.Errorf("创建 mihomo 下载请求: %w", err)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("下载 %s: %w", spec.name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s HTTP %d", spec.name, resp.StatusCode)
	}

	archiveFile, err := os.CreateTemp(filepath.Dir(dst), ".mihomo-download-*")
	if err != nil {
		return fmt.Errorf("创建 mihomo 下载临时文件: %w", err)
	}
	archivePath := archiveFile.Name()
	defer os.Remove(archivePath)
	if _, err := io.Copy(archiveFile, resp.Body); err != nil {
		archiveFile.Close()
		return fmt.Errorf("读取 %s: %w", spec.name, err)
	}
	if err := archiveFile.Close(); err != nil {
		return fmt.Errorf("保存 %s: %w", spec.name, err)
	}
	return installMihomoArchive(archivePath, spec, dst)
}

// installMihomoArchive 先验证官方压缩资产摘要，再把可执行文件写入同目录临时文件并替换目标。
func installMihomoArchive(archivePath string, spec mihomoAssetSpec, dst string) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("打开 mihomo 压缩包: %w", err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, archive); err != nil {
		archive.Close()
		return fmt.Errorf("计算 mihomo SHA-256: %w", err)
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("关闭 mihomo 压缩包: %w", err)
	}
	actual := fmt.Sprintf("%x", hash.Sum(nil))
	if !strings.EqualFold(actual, spec.sha256) {
		return fmt.Errorf("mihomo 资产 %s SHA-256 不匹配: 实际 %s", spec.name, actual)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+"-extract-*")
	if err != nil {
		return fmt.Errorf("创建 mihomo 解压临时文件: %w", err)
	}
	tmpPath := tmp.Name()
	installed := false
	defer func() {
		if !installed {
			os.Remove(tmpPath)
		}
	}()

	if err := extractMihomoArchive(archivePath, spec.name, tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭 mihomo 解压临时文件: %w", err)
	}
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("设置 mihomo 可执行权限: %w", err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		return fmt.Errorf("安装 mihomo: %w", err)
	}
	installed = true
	return nil
}

func extractMihomoArchive(archivePath, assetName string, dst io.Writer) error {
	if strings.HasSuffix(strings.ToLower(assetName), ".zip") {
		zr, err := zip.OpenReader(archivePath)
		if err != nil {
			return fmt.Errorf("解析 mihomo zip: %w", err)
		}
		defer zr.Close()
		for _, entry := range zr.File {
			if !strings.HasSuffix(strings.ToLower(entry.Name), ".exe") {
				continue
			}
			source, err := entry.Open()
			if err != nil {
				return fmt.Errorf("打开 mihomo exe: %w", err)
			}
			_, copyErr := io.Copy(dst, source)
			closeErr := source.Close()
			if copyErr != nil {
				return fmt.Errorf("解压 mihomo exe: %w", copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("关闭 mihomo exe: %w", closeErr)
			}
			return nil
		}
		return fmt.Errorf("mihomo zip 内未找到 .exe")
	}

	archive, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("打开 mihomo gzip: %w", err)
	}
	defer archive.Close()
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("解析 mihomo gzip: %w", err)
	}
	_, copyErr := io.Copy(dst, gz)
	closeErr := gz.Close()
	if copyErr != nil {
		return fmt.Errorf("解压 mihomo gzip: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("关闭 mihomo gzip: %w", closeErr)
	}
	return nil
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func fetchPinnedRelease(ctx context.Context) (*ghRelease, error) {
	releaseURL := "https://api.github.com/repos/MetaCubeX/mihomo/releases/tags/v" + pinnedMihomoVersion
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建 mihomo release 请求: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "miaomiaowux-speedtest")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("查询 mihomo release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("查询 mihomo release HTTP 403: GitHub 匿名 API 限速为每 IP 每小时 60 次，可设置 MIHOMO_BIN 使用自带内核")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("查询 mihomo release HTTP %d", resp.StatusCode)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}
