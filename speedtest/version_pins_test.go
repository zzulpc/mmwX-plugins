package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 发布二进制和容器必须使用同一补丁工具链，否则仅检查 go.mod 的最低版本会漏掉构建差异。
func TestBuildToolchainVersions跨文件一致(t *testing.T) {
	dockerfile := readSpeedtestFile(t, "Dockerfile")
	pattern := regexp.MustCompile(`(?m)^FROM golang:([0-9]+\.[0-9]+\.[0-9]+)-alpine[0-9.]+@sha256:[0-9a-f]{64} AS builder$`)
	matched := pattern.FindStringSubmatch(dockerfile)
	if len(matched) != 2 {
		t.Fatal("Docker builder 必须固定完整 Go 补丁版本、Alpine 分支与镜像摘要")
	}
	goVersion := matched[1]
	assertCapturedVersions(t, "Dockerfile 实际 Go 版本检查", dockerfile,
		`(?m)^RUN test "\$\(go env GOVERSION\)" = "go([0-9]+\.[0-9]+\.[0-9]+)" && go mod download$`, goVersion, 1)
	for name, count := range map[string]int{"ci.yml": 2, "speedtest.yml": 1} {
		workflow := readSpeedtestFile(t, filepath.Join("..", ".github", "workflows", name))
		assertCapturedVersions(t, name+" Go 工具链", workflow,
			`(?m)^[ \t]+go-version: '([0-9]+\.[0-9]+\.[0-9]+)'[ \t]*$`, goVersion, count)
	}
}

// TestPinnedKernelVersions跨文件一致逐项解析会真正影响发布产物的字段：Docker 构建来源与
// 运行期检查、workflow 对应源码版本，以及三份用户可见文档。不能只查 Contains；旧版本若
// 留在第二个架构资产、下载 URL 或源码包文件名里，也必须让测试失败。
func TestPinnedKernelVersions跨文件一致(t *testing.T) {
	dockerfile := readSpeedtestFile(t, "Dockerfile")
	workflow := readSpeedtestFile(t, filepath.Join("..", ".github", "workflows", "speedtest.yml"))
	readme := contentBeforeChangelog(t, readSpeedtestFile(t, "README.md"))
	notices := readSpeedtestFile(t, "THIRD_PARTY_NOTICES.md")
	correspondingSource := readSpeedtestFile(t, "CORRESPONDING_SOURCE.md")

	// Dockerfile 中的每个有效字面量分别解析，数量也固定下来，避免删掉版本检查后仍然误绿。
	assertCapturedVersions(t, "Dockerfile sing-box 基础镜像", dockerfile,
		`(?m)^FROM[ \t]+ghcr\.io/sagernet/sing-box:v([0-9]+\.[0-9]+\.[0-9]+[-0-9A-Za-z.]*)@sha256:[0-9a-f]{64}[ \t]+AS[ \t]+singbox[ \t]*$`,
		pinnedSingBoxVersion, 1)
	assertCapturedVersions(t, "Dockerfile Mihomo 架构资产", dockerfile,
		`mihomo-linux-[0-9A-Za-z_-]+-v([0-9]+\.[0-9]+\.[0-9]+[-0-9A-Za-z.]*)\.gz`,
		pinnedMihomoVersion, 2)
	assertCapturedVersions(t, "Dockerfile Mihomo 下载 URL", dockerfile,
		`releases/download/v([0-9]+\.[0-9]+\.[0-9]+[-0-9A-Za-z.]*)/\$\{asset\}`,
		pinnedMihomoVersion, 1)
	assertCapturedVersions(t, "Dockerfile Mihomo 运行检查", dockerfile,
		`(?m)^[ \t]+test[ \t]+"\$mihomo_version"[ \t]+=[ \t]+"v([0-9]+\.[0-9]+\.[0-9]+[-0-9A-Za-z.]*)";[ \t]*\\$`,
		pinnedMihomoVersion, 1)
	assertCapturedVersions(t, "Dockerfile sing-box 运行检查", dockerfile,
		`(?m)^[ \t]+test[ \t]+"\$sing_box_version_line"[ \t]+=[ \t]+"sing-box version ([0-9]+\.[0-9]+\.[0-9]+[-0-9A-Za-z.]*)"[ \t]*$`,
		pinnedSingBoxVersion, 1)

	// Workflow 只从 env 字段决定 Release 附带哪一个源码包，锚定整行可排除注释误命中。
	assertCapturedVersions(t, "workflow sing-box 对应源码", workflow,
		`(?m)^[ \t]+SING_BOX_SOURCE_VERSION:[ \t]+v([0-9]+\.[0-9]+\.[0-9]+[-0-9A-Za-z.]*)[ \t]*$`,
		pinnedSingBoxVersion, 1)
	assertCapturedVersions(t, "workflow Mihomo 对应源码", workflow,
		`(?m)^[ \t]+MIHOMO_SOURCE_VERSION:[ \t]+v([0-9]+\.[0-9]+\.[0-9]+[-0-9A-Za-z.]*)[ \t]*$`,
		pinnedMihomoVersion, 1)

	// README 只核对更新日志之前的当前说明；历史发布记录本来就应保留旧版本。
	assertCapturedVersions(t, "README sing-box 固定版本", readme,
		"sing-box[ \\t]+`v([0-9]+\\.[0-9]+\\.[0-9]+[-0-9A-Za-z.]*)`",
		pinnedSingBoxVersion, 1)
	assertCapturedVersions(t, "README Mihomo 固定版本", readme,
		"Mihomo[ \\t]+`v([0-9]+\\.[0-9]+\\.[0-9]+[-0-9A-Za-z.]*)`",
		pinnedMihomoVersion, 1)

	// 许可证与对应源码文档按组件章节和字段解析，文件扩展名不会被误当成预发布版本的一部分。
	mihomoNotices := markdownSection(t, "THIRD_PARTY_NOTICES.md", notices, "Mihomo")
	assertCapturedVersions(t, "THIRD_PARTY_NOTICES.md Mihomo 固定版本", mihomoNotices,
		"固定版本：[\\x60]v([0-9]+\\.[0-9]+\\.[0-9]+[-0-9A-Za-z.]*)[\\x60]", pinnedMihomoVersion, 1)
	assertCapturedVersions(t, "THIRD_PARTY_NOTICES.md Mihomo 架构资产", mihomoNotices,
		`mihomo-linux-[0-9A-Za-z_-]+-v([0-9]+\.[0-9]+\.[0-9]+[-0-9A-Za-z.]*)\.gz`, pinnedMihomoVersion, 2)
	assertCapturedVersions(t, "THIRD_PARTY_NOTICES.md Mihomo 源码 URL", mihomoNotices,
		`github\.com/MetaCubeX/mihomo/tree/v([0-9]+\.[0-9]+\.[0-9]+[-0-9A-Za-z.]*)>`, pinnedMihomoVersion, 1)

	singBoxNotices := markdownSection(t, "THIRD_PARTY_NOTICES.md", notices, "sing-box")
	assertCapturedVersions(t, "THIRD_PARTY_NOTICES.md sing-box 固定版本", singBoxNotices,
		"固定版本：[\\x60]v([0-9]+\\.[0-9]+\\.[0-9]+[-0-9A-Za-z.]*)[\\x60]", pinnedSingBoxVersion, 1)
	assertCapturedVersions(t, "THIRD_PARTY_NOTICES.md sing-box 源码 URL", singBoxNotices,
		`github\.com/SagerNet/sing-box/tree/v([0-9]+\.[0-9]+\.[0-9]+[-0-9A-Za-z.]*)>`, pinnedSingBoxVersion, 1)

	mihomoSource := markdownSection(t, "CORRESPONDING_SOURCE.md", correspondingSource, "Mihomo")
	assertCapturedVersions(t, "CORRESPONDING_SOURCE.md Mihomo 固定版本", mihomoSource,
		"版本：[\\x60]v([0-9]+\\.[0-9]+\\.[0-9]+[-0-9A-Za-z.]*)[\\x60]", pinnedMihomoVersion, 1)
	assertCapturedVersions(t, "CORRESPONDING_SOURCE.md Mihomo 归档 URL", mihomoSource,
		`tags/v([0-9]+\.[0-9]+\.[0-9]+[-0-9A-Za-z.]*)\.tar\.gz>`, pinnedMihomoVersion, 1)
	assertCapturedVersions(t, "CORRESPONDING_SOURCE.md Mihomo 源码包名", mihomoSource,
		`mihomo-v([0-9]+\.[0-9]+\.[0-9]+[-0-9A-Za-z.]*)-source\.tar\.gz`, pinnedMihomoVersion, 1)

	singBoxSource := markdownSection(t, "CORRESPONDING_SOURCE.md", correspondingSource, "sing-box")
	assertCapturedVersions(t, "CORRESPONDING_SOURCE.md sing-box 固定版本", singBoxSource,
		"版本：[\\x60]v([0-9]+\\.[0-9]+\\.[0-9]+[-0-9A-Za-z.]*)[\\x60]", pinnedSingBoxVersion, 1)
	assertCapturedVersions(t, "CORRESPONDING_SOURCE.md sing-box 归档 URL", singBoxSource,
		`tags/v([0-9]+\.[0-9]+\.[0-9]+[-0-9A-Za-z.]*)\.tar\.gz>`, pinnedSingBoxVersion, 1)
	assertCapturedVersions(t, "CORRESPONDING_SOURCE.md sing-box 源码包名", singBoxSource,
		`sing-box-v([0-9]+\.[0-9]+\.[0-9]+[-0-9A-Za-z.]*)-source\.tar\.gz`, pinnedSingBoxVersion, 1)
}

// minSingBoxVersion 是运行时下限，pinnedSingBoxVersion 是容器固定携带的版本。
// 两者分开后可以升级镜像而不误抬兼容下限，但固定版本绝不能低于运行时要求。
func TestSingBox固定版本满足运行时下限(t *testing.T) {
	if !semanticVersionGTE(pinnedSingBoxVersion, minSingBoxVersion) {
		t.Fatalf("pinnedSingBoxVersion=%s 低于 minSingBoxVersion=%s，镜像内核会被自己拒收",
			pinnedSingBoxVersion, minSingBoxVersion)
	}
}

// minMihomoVersion 是运行时的下限，pinnedMihomoVersion 是自动下载的目标。
// 下限一旦高过目标，EnsureMihomo 会下完固定版本再判定它不合格，然后每次测速重下一遍。
func TestMihomo自动下载版本满足运行时下限(t *testing.T) {
	if !versionGTE(pinnedMihomoVersion, minMihomoVersion) {
		t.Fatalf("pinnedMihomoVersion=%s 低于 minMihomoVersion=%s，自动下载的内核会被自己拒收",
			pinnedMihomoVersion, minMihomoVersion)
	}
}

// releaseVersionRefRE 匹配随每次发布一起更新的镜像 tag 和 speedtest-vX.Y.Z。
// 第一组是引用类型，第二组才是版本；逐个比较可以抓住同一文件中的混合新旧版本。
var releaseVersionRefRE = regexp.MustCompile(`(ghcr\.io/zzulpc/mmwx-speedtester:|speedtest-v)([0-9]+\.[0-9]+\.[0-9]+)`)

const changelogMarker = "<summary>更新日志</summary>"

// speedtest/VERSION 会 //go:embed 进二进制上报给主控，而 README 与 Dockerfile 里的
// 镜像用法示例是手写的。这里验证全部当前引用及其数量，不让注释中的一个正确片段掩盖旧值。
func TestDocs引用当前发布版本(t *testing.T) {
	version := strings.TrimSpace(readSpeedtestFile(t, "VERSION"))
	if version == "" {
		t.Fatal("speedtest/VERSION 为空")
	}

	assertReleaseVersionRefs(t, "README.md（changelog 之前的正文）",
		contentBeforeChangelog(t, readSpeedtestFile(t, "README.md")), version, 2)
	assertReleaseVersionRefs(t, "Dockerfile", readSpeedtestFile(t, "Dockerfile"), version, 1)
}

// TestReleaseVersionTransform不推送通过脚本的内部测试入口验证真实 sed 变换；入口在分支检查、
// commit、tag 和 push 之前退出，只操作本用例创建的临时样本。
func TestReleaseVersionTransform不推送(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("当前环境没有 bash，跳过发布脚本变换测试")
	}
	fixtureDir := t.TempDir()
	readmePath := filepath.Join(fixtureDir, "README.md")
	dockerfilePath := filepath.Join(fixtureDir, "Dockerfile")
	readme := "当前 `speedtest-v1.2.3`\n" +
		"docker pull ghcr.io/zzulpc/mmwx-speedtester:1.2.3\n" +
		changelogMarker + "\n" +
		"历史 `speedtest-v1.2.3` 与 ghcr.io/zzulpc/mmwx-speedtester:1.2.3\n"
	dockerfile := "# ghcr.io/zzulpc/mmwx-speedtester:1.2.3\nFROM alpine:3.20\n"
	writeTestFixture(t, readmePath, readme)
	writeTestFixture(t, dockerfilePath, dockerfile)

	cmd := exec.Command(bashPath, "scripts/release.sh", "--verify-version-transform",
		"1.2.3", "1.2.4", readmePath, dockerfilePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("发布版本变换 helper 失败: %v\n%s", err, output)
	}

	updatedReadme := readSpeedtestPath(t, readmePath)
	currentReadme := contentBeforeChangelog(t, updatedReadme)
	if strings.Count(currentReadme, "speedtest-v1.2.4") != 1 ||
		strings.Count(currentReadme, "mmwx-speedtester:1.2.4") != 1 ||
		strings.Contains(currentReadme, "1.2.3") {
		t.Fatalf("README 当前说明没有完整替换: %q", currentReadme)
	}
	history := updatedReadme[strings.Index(updatedReadme, changelogMarker):]
	if strings.Count(history, "1.2.3") != 2 || strings.Contains(history, "1.2.4") {
		t.Fatalf("README 历史更新日志不应被改写: %q", history)
	}
	updatedDockerfile := readSpeedtestPath(t, dockerfilePath)
	if strings.Count(updatedDockerfile, "mmwx-speedtester:1.2.4") != 1 || strings.Contains(updatedDockerfile, "1.2.3") {
		t.Fatalf("Dockerfile 版本引用没有完整替换: %q", updatedDockerfile)
	}

	// 任一目标零命中都必须失败，不能继续到发布阶段。
	missingDockerfile := filepath.Join(fixtureDir, "Dockerfile-missing")
	writeTestFixture(t, missingDockerfile, "FROM alpine:3.20\n")
	cmd = exec.Command(bashPath, "scripts/release.sh", "--verify-version-transform",
		"1.2.4", "1.2.5", readmePath, missingDockerfile)
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("Dockerfile 零命中时 helper 仍返回成功: %s", output)
	}
}

// TestReleaseScript最终测试位于提交前守住执行顺序：文件和 changelog 都变换完成后才跑最后
// 一轮测试，且测试失败时还没有 commit、tag 或 push。
func TestReleaseScript最终测试位于提交前(t *testing.T) {
	script := readSpeedtestFile(t, filepath.Join("scripts", "release.sh"))
	transformIndex := strings.LastIndex(script, "replace_release_version_refs \"$CUR\"")
	changelogIndex := strings.LastIndex(script, "mv \"$README_TMP\" \"$PLUGIN_DIR/README.md\"")
	finalTestIndex := strings.LastIndex(script, "go test -mod=readonly ./... -count=1")
	commitIndex := strings.LastIndex(script, "git add speedtest/VERSION")
	if transformIndex < 0 || changelogIndex < 0 || finalTestIndex < 0 || commitIndex < 0 {
		t.Fatalf("无法在 release.sh 中定位版本变换、changelog、最终测试或 commit 阶段")
	}
	if !(transformIndex < changelogIndex && changelogIndex < finalTestIndex && finalTestIndex < commitIndex) {
		t.Fatalf("release.sh 顺序错误：transform=%d changelog=%d finalTest=%d commit=%d",
			transformIndex, changelogIndex, finalTestIndex, commitIndex)
	}
}

// TestReleaseScript只清理本次专属临时目录守住一次真实踩坑：固定的 README.md.tmp 与
// Dockerfile.tmp 可能本来就属于用户，而 EXIT trap 无论成功失败都会把它们删掉。
func TestReleaseScript只清理本次专属临时目录(t *testing.T) {
	script := readSpeedtestFile(t, filepath.Join("scripts", "release.sh"))
	cleanCheckIndex := strings.Index(script, `if [ -n "$(git status --porcelain)" ]; then`)
	ownedTempIndex := strings.Index(script, `RELEASE_TMP_DIR="$(mktemp -d "$PLUGIN_DIR/.release.XXXXXX")"`)
	if cleanCheckIndex < 0 || ownedTempIndex < 0 || cleanCheckIndex >= ownedTempIndex {
		t.Fatalf("发布专属临时目录必须在工作区 clean-check 后创建：clean=%d temp=%d",
			cleanCheckIndex, ownedTempIndex)
	}
	for _, forbidden := range []*regexp.Regexp{
		regexp.MustCompile(`(?m)^README_TMP="\$PLUGIN_DIR/README\.md\.tmp"[ \t]*$`),
		regexp.MustCompile(`(?m)^DOCKERFILE_TMP="\$PLUGIN_DIR/Dockerfile\.tmp"[ \t]*$`),
	} {
		if forbidden.MatchString(script) {
			t.Fatalf("release.sh 又使用了可能覆盖或删除用户文件的固定临时路径：%s", forbidden)
		}
	}
	for label, pattern := range map[string]string{
		"临时目录空值初始化":         `(?m)^RELEASE_TMP_DIR=""[ \t]*$`,
		"退出清理非空保护":          `(?m)^[ \t]+if \[ -n "\$RELEASE_TMP_DIR" \]; then[ \t]*$`,
		"README 临时文件归属":     `(?m)^README_TMP="\$RELEASE_TMP_DIR/README\.md"[ \t]*$`,
		"Dockerfile 临时文件归属": `(?m)^DOCKERFILE_TMP="\$RELEASE_TMP_DIR/Dockerfile"[ \t]*$`,
	} {
		if matches := regexp.MustCompile(pattern).FindAllString(script, -1); len(matches) != 1 {
			t.Errorf("release.sh 的%s应恰好出现一次，实际为 %d", label, len(matches))
		}
	}
}

func assertCapturedVersions(t *testing.T, sourceName, content, pattern, expected string, expectedCount int) {
	t.Helper()
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("%s 的测试正则无效: %v", sourceName, err)
	}
	matches := re.FindAllStringSubmatch(content, -1)
	if len(matches) != expectedCount {
		t.Errorf("%s 应有 %d 个有效版本字段，实际找到 %d 个", sourceName, expectedCount, len(matches))
	}
	for _, match := range matches {
		if len(match) != 2 {
			t.Fatalf("%s 的测试正则必须且只能包含一个捕获组", sourceName)
		}
		if match[1] != expected {
			t.Errorf("%s 的 %q 使用版本 %s，期望 %s", sourceName, match[0], match[1], expected)
		}
	}
}

func assertReleaseVersionRefs(t *testing.T, sourceName, content, expected string, expectedCount int) {
	t.Helper()
	matches := releaseVersionRefRE.FindAllStringSubmatch(content, -1)
	if len(matches) != expectedCount {
		t.Errorf("%s 应有 %d 个当前发布版本引用，实际找到 %d 个", sourceName, expectedCount, len(matches))
	}
	for _, match := range matches {
		if match[2] != expected {
			t.Errorf("%s 里的 %q 停在 %s，当前 VERSION 是 %s", sourceName, match[0], match[2], expected)
		}
	}
}

func markdownSection(t *testing.T, fileName, content, heading string) string {
	t.Helper()
	headingRE := regexp.MustCompile(`(?m)^##[ \t]+` + regexp.QuoteMeta(heading) + `[ \t]*$`)
	locations := headingRE.FindAllStringIndex(content, -1)
	if len(locations) != 1 {
		t.Fatalf("%s 的 ## %s 章节应出现一次，实际为 %d 次", fileName, heading, len(locations))
	}
	section := content[locations[0][1]:]
	nextHeadingRE := regexp.MustCompile(`(?m)^##[ \t]+`)
	if next := nextHeadingRE.FindStringIndex(section); next != nil {
		section = section[:next[0]]
	}
	return section
}

func contentBeforeChangelog(t *testing.T, content string) string {
	t.Helper()
	markerCount := strings.Count(content, changelogMarker)
	if markerCount != 1 {
		t.Fatalf("README 的更新日志 marker 应出现一次，实际为 %d 次", markerCount)
	}
	return content[:strings.Index(content, changelogMarker)]
}

func readSpeedtestFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", name, err)
	}
	return string(raw)
}

func readSpeedtestPath(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取测试样本 %s 失败: %v", path, err)
	}
	return string(raw)
}

func writeTestFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入测试样本 %s 失败: %v", path, err)
	}
}
