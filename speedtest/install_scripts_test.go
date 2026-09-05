package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type installerScenario struct {
	name      string
	mode      string
	existing  bool
	success   bool
	directory bool
}

func installerScenarios() []installerScenario {
	return []installerScenario{
		{name: "下载中断保留旧版", mode: "download_failure", existing: true},
		{name: "摘要下载失败保留旧版", mode: "checksum_download_failure", existing: true},
		{name: "摘要条目缺失保留旧版", mode: "checksum_missing", existing: true},
		{name: "摘要不符保留旧版", mode: "checksum_mismatch", existing: true},
		{name: "首次安装失败不留半成品", mode: "download_failure"},
		{name: "升级成功替换旧版", mode: "success", existing: true, success: true},
		{name: "首次安装成功", mode: "success", success: true},
		{name: "拒绝把程序写入同名目录", mode: "success", directory: true},
	}
}

// 安装测试用本地文件模拟 Release 和下载响应，失败用例不得碰到真正的网络或现有程序。
func TestBashInstaller原子安装(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash 用例通过 POSIX 命令模拟下载，Windows 由 PowerShell 用例覆盖")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("本机未安装 bash")
	}
	script, err := filepath.Abs(filepath.Join("scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cases := append(installerScenarios(),
		installerScenario{name: "替换失败保留旧版", mode: "replace_failure", existing: true},
		installerScenario{name: "执行权限失败保留旧版", mode: "chmod_failure", existing: true})
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newInstallerFixture(t, scenario, "mmwx-speedtester", []byte("#!/bin/sh\nprintf 'installed successfully\\n'\n"))
			fixture.write(t, "mock-bin/uname", "#!/bin/sh\ncase \"$1\" in -s) echo Linux ;; -m) echo x86_64 ;; *) exit 1 ;; esac\n", 0700)
			fixture.write(t, "mock-bin/curl", `#!/bin/sh
output=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) output="$2"; shift 2 ;;
    *) url="$1"; shift ;;
  esac
done
case "$url" in
  */releases/latest) cat "$INSTALL_TEST_DIR/release.json" ;;
  */checksums.txt)
    if [ "$INSTALL_TEST_MODE" = checksum_download_failure ]; then exit 22; fi
    cp "$INSTALL_TEST_DIR/checksums.txt" "$output" ;;
  */mmwx-speedtester-linux-amd64)
    if [ "$INSTALL_TEST_MODE" = download_failure ]; then
      printf 'partial download' > "$output"
      exit 22
    fi
    cp "$INSTALL_TEST_DIR/payload" "$output" ;;
  *) echo "禁止访问未模拟的 URL: $url" >&2; exit 99 ;;
esac
`, 0700)
			for command, failureMode := range map[string]string{"mv": "replace_failure", "chmod": "chmod_failure"} {
				realCommand, err := exec.LookPath(command)
				if err != nil {
					t.Fatal(err)
				}
				fixture.write(t, "mock-bin/"+command, fmt.Sprintf("#!/bin/sh\nif [ \"$INSTALL_TEST_MODE\" = %s ]; then exit 1; fi\nexec %s \"$@\"\n", failureMode, installerShellQuote(realCommand)), 0700)
			}
			cmd := exec.Command(bash, script, "-master", "https://example.invalid", "-token", "installer-test-token")
			cmd.Dir = fixture.dir
			cmd.Env = fixture.environment(scenario.mode)
			output, err := cmd.CombinedOutput()
			fixture.verify(t, scenario, output, err)
		})
	}
}

// 有 PowerShell 时执行原始脚本；下载函数仅复制本地样本，Windows 额外验证真实文件锁。
func TestPowerShellInstaller原子安装(t *testing.T) {
	powershell, err := exec.LookPath("pwsh")
	if err != nil {
		powershell, err = exec.LookPath("powershell")
	}
	if err != nil {
		t.Skip("本机未安装 PowerShell，需在 Windows 或带 pwsh 的环境运行安装回归")
	}
	script, err := filepath.Abs(filepath.Join("scripts", "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("#!/bin/sh\nprintf 'installed successfully\\n'\n")
	if runtime.GOOS == "windows" {
		// 无参数 cmd.exe 在关闭标准输入后退出，能够验证安装成功后的真实启动而不需要编译辅助程序。
		payload, err = os.ReadFile(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"))
		if err != nil {
			t.Fatal(err)
		}
	}
	cases := installerScenarios()
	if runtime.GOOS == "windows" {
		cases = append(cases, installerScenario{name: "程序被锁定时保留旧版", mode: "locked_target", existing: true})
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newInstallerFixture(t, scenario, "mmwx-speedtester.exe", payload)
			fixture.write(t, "invoke-installer.ps1", `
$ErrorActionPreference = 'Stop'
function Invoke-RestMethod {
    param($Uri, $Headers)
    if ($Uri -notlike '*/releases/latest') { throw '禁止访问未模拟的 URL' }
    Get-Content -LiteralPath (Join-Path $env:INSTALL_TEST_DIR 'release.json') -Raw | ConvertFrom-Json
}
function Invoke-WebRequest {
    param($Uri, $OutFile)
    if ($Uri -like '*/checksums.txt') {
        if ($env:INSTALL_TEST_MODE -eq 'checksum_download_failure') { throw '模拟摘要下载失败' }
        Copy-Item -LiteralPath (Join-Path $env:INSTALL_TEST_DIR 'checksums.txt') -Destination $OutFile -Force
    } elseif ($Uri -like '*/mmwx-speedtester-windows-amd64.exe') {
        if ($env:INSTALL_TEST_MODE -eq 'download_failure') {
            [System.IO.File]::WriteAllText($OutFile, 'partial download')
            throw '模拟下载中断'
        }
        Copy-Item -LiteralPath (Join-Path $env:INSTALL_TEST_DIR 'payload') -Destination $OutFile -Force
        if ($env:INSTALL_TEST_OS -ne 'windows') { & /bin/chmod '+x' $OutFile }
    } else { throw '禁止访问未模拟的 URL' }
}
$LockedFile = $null
try {
    if ($env:INSTALL_TEST_MODE -eq 'locked_target') {
        $LockedFile = [System.IO.File]::Open((Join-Path $PWD 'mmwx-speedtester.exe'), [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::None)
    }
    & $env:INSTALL_TEST_SCRIPT -Master 'https://example.invalid' -Token 'installer-test-token'
} finally {
    if ($LockedFile) { $LockedFile.Dispose() }
}
`, 0600)
			cmd := exec.Command(powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(fixture.dir, "invoke-installer.ps1"))
			cmd.Dir = fixture.dir
			cmd.Env = append(fixture.environment(scenario.mode), "INSTALL_TEST_SCRIPT="+script, "PROCESSOR_ARCHITECTURE=AMD64", "INSTALL_TEST_OS="+runtime.GOOS)
			output, err := cmd.CombinedOutput()
			fixture.verify(t, scenario, output, err)
		})
	}
}

type installerFixture struct {
	dir        string
	outputName string
	payload    []byte
}

func newInstallerFixture(t *testing.T, scenario installerScenario, outputName string, payload []byte) installerFixture {
	t.Helper()
	fixture := installerFixture{dir: t.TempDir(), outputName: outputName, payload: payload}
	for _, directory := range []string{"mock-bin", "temporary"} {
		if err := os.Mkdir(filepath.Join(fixture.dir, directory), 0700); err != nil {
			t.Fatal(err)
		}
	}
	fixture.write(t, "payload", string(payload), 0700)
	fixture.write(t, "release.json", `{"tag_name":"speedtest-v0.2.5","assets":[{"name":"mmwx-speedtester-linux-amd64","browser_download_url":"https://example.invalid/mmwx-speedtester-linux-amd64"},{"name":"mmwx-speedtester-windows-amd64.exe","browser_download_url":"https://example.invalid/mmwx-speedtester-windows-amd64.exe"},{"name":"checksums.txt","browser_download_url":"https://example.invalid/checksums.txt"}]}`, 0600)
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	if scenario.mode == "checksum_mismatch" {
		digest = strings.Repeat("0", 64)
	}
	checksums := fmt.Sprintf("%s  ./mmwx-speedtester-linux-amd64\n%s  ./mmwx-speedtester-windows-amd64.exe\n", digest, digest)
	if scenario.mode == "checksum_missing" {
		checksums = digest + "  ./unrelated-asset\n"
	}
	fixture.write(t, "checksums.txt", checksums, 0600)
	if scenario.directory {
		if err := os.Mkdir(filepath.Join(fixture.dir, outputName), 0700); err != nil {
			t.Fatal(err)
		}
		fixture.write(t, filepath.Join(outputName, "user-file"), "preserve directory content", 0600)
	} else if scenario.existing {
		fixture.write(t, outputName, "previous working binary\n", 0700)
	}
	return fixture
}

func (fixture installerFixture) write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.dir, path), []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func (fixture installerFixture) environment(mode string) []string {
	temporary := filepath.Join(fixture.dir, "temporary") + string(os.PathSeparator)
	return append(os.Environ(),
		"PATH="+filepath.Join(fixture.dir, "mock-bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"INSTALL_TEST_DIR="+fixture.dir, "INSTALL_TEST_MODE="+mode,
		"TMPDIR="+temporary, "TMP="+temporary, "TEMP="+temporary)
}

func (fixture installerFixture) verify(t *testing.T, scenario installerScenario, output []byte, runErr error) {
	t.Helper()
	if (runErr == nil) != scenario.success {
		t.Fatalf("安装结果不符: err=%v, output=%s", runErr, output)
	}
	outputPath := filepath.Join(fixture.dir, fixture.outputName)
	if scenario.directory {
		entries, err := os.ReadDir(outputPath)
		if err != nil || len(entries) != 1 || entries[0].Name() != "user-file" {
			t.Fatalf("安装失败改变了用户同名目录: entries=%v err=%v", entries, err)
		}
	} else if scenario.success || scenario.existing {
		want := "previous working binary\n"
		if scenario.success {
			want = string(fixture.payload)
		}
		got, err := os.ReadFile(outputPath)
		if err != nil || string(got) != want {
			t.Fatalf("正式程序内容不符: err=%v, got SHA256=%x, want SHA256=%x", err, sha256.Sum256(got), sha256.Sum256([]byte(want)))
		}
	} else if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("首次安装失败残留正式程序: %v", err)
	}
	entries, err := os.ReadDir(fixture.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".mmwx-speedtester.") {
			t.Errorf("残留下载临时文件: %s", entry.Name())
		}
	}
	temporary, err := os.ReadDir(filepath.Join(fixture.dir, "temporary"))
	if err != nil || len(temporary) != 0 {
		t.Errorf("残留摘要临时文件: %v, err=%v", temporary, err)
	}
}

func installerShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
