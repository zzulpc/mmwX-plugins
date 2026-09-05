# mmwx-speedtester Windows 安装与启动脚本。
# 用法：.\install.ps1 -Master https://your-master-url -Token <token>
param(
    [Parameter(Mandatory=$true)][string]$Master,
    [Parameter(Mandatory=$true)][string]$Token
)

$ErrorActionPreference = "Stop"
$Repo = "zzulpc/mmwX-plugins"
$BinaryName = "mmwx-speedtester"

# 发布资产只提供 64 位版本，必须先拒绝不支持的平台。
$Arch = if ([Environment]::Is64BitOperatingSystem) {
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
} else {
    Write-Error "32-bit systems are not supported"; exit 1
}

$AssetName = "${BinaryName}-windows-${Arch}.exe"
Write-Host "Platform: windows/${Arch}"

# 二进制和摘要必须取自同一个 Release，避免发布更新期间版本混用。
Write-Host "Fetching latest release..."
$ReleaseUrl = "https://api.github.com/repos/${Repo}/releases/latest"
$Release = Invoke-RestMethod -Uri $ReleaseUrl -Headers @{ "User-Agent" = "mmwx-installer" }
Write-Host "Latest version: $($Release.tag_name)"

$Asset = $Release.assets | Where-Object { $_.name -eq $AssetName } | Select-Object -First 1
if (-not $Asset) {
    Write-Error "Asset ${AssetName} not found. Visit https://github.com/${Repo}/releases/latest to download manually."
    exit 1
}
$ChecksumsAsset = $Release.assets | Where-Object { $_.name -eq "checksums.txt" } | Select-Object -First 1
if (-not $ChecksumsAsset) {
    Write-Error "checksums.txt not found in release."
    exit 1
}

# 下载临时文件与正式文件同目录，避免跨卷移动破坏替换的原子性。
$OutputPath = Join-Path $PWD "${BinaryName}.exe"
$ChecksumsPath = $null
$DownloadPath = $null
try {
    if (Test-Path -LiteralPath $OutputPath -PathType Container) {
        throw "Install path is a directory: ${OutputPath}"
    }
    $CandidatePath = Join-Path $PWD ".${BinaryName}.$([Guid]::NewGuid().ToString('N')).download"
    # CreateNew 保证只取得本次新建文件的所有权，极小概率重名时也不能删除已有文件。
    $DownloadStream = [System.IO.File]::Open($CandidatePath, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
    $DownloadPath = $CandidatePath
    $DownloadStream.Dispose()
    $ChecksumsPath = [System.IO.Path]::GetTempFileName()
    Write-Host "Downloading ${AssetName}..."
    Invoke-WebRequest -Uri $Asset.browser_download_url -OutFile $DownloadPath
    Invoke-WebRequest -Uri $ChecksumsAsset.browser_download_url -OutFile $ChecksumsPath

    $ExpectedHash = $null
    foreach ($Line in Get-Content -Path $ChecksumsPath) {
        if ($Line -match '^\s*([0-9A-Fa-f]{64})\s+\*?(.+?)\s*$') {
            $ListedName = $Matches[2] -replace '^[.][\\/]', ''
            if ($ListedName -eq $AssetName) {
                $ExpectedHash = $Matches[1].ToLowerInvariant()
                break
            }
        }
    }
    if (-not $ExpectedHash) {
        throw "Checksum for ${AssetName} not found or invalid."
    }

    $ActualHash = (Get-FileHash -LiteralPath $DownloadPath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($ActualHash -ne $ExpectedHash) {
        throw "Checksum mismatch for ${AssetName}."
    }
    try {
        if ([System.IO.File]::Exists($OutputPath)) {
            # Windows 正在运行的程序通常被锁定；Replace 失败时保留旧文件，禁止先删后移。
            # PowerShell 会把普通 $null 绑定为空字符串；备份路径必须传真正的 .NET null。
            [System.IO.File]::Replace($DownloadPath, $OutputPath, [NullString]::Value)
        } else {
            # 首次安装只接受目标不存在；并发出现新文件时失败，不覆盖未验证的目标。
            [System.IO.File]::Move($DownloadPath, $OutputPath)
        }
    }
    catch {
        throw "Failed to replace ${OutputPath}; the previous binary was preserved. Stop the running tester and retry. $($_.Exception.Message)"
    }
    $DownloadPath = $null
    Write-Host "Saved to: ${OutputPath}"
}
catch {
    Write-Error $_.Exception.Message
    exit 1
}
finally {
    # 无论在哪一步失败，只清理本次创建的下载和摘要临时文件。
    if ($DownloadPath -and (Test-Path -LiteralPath $DownloadPath)) {
        Remove-Item -LiteralPath $DownloadPath -Force
    }
    if ($ChecksumsPath -and (Test-Path -LiteralPath $ChecksumsPath)) {
        Remove-Item -LiteralPath $ChecksumsPath -Force
    }
}

# 仅在完整安装成功后启动，退出后恢复调用方已有的配对环境变量。
Write-Host ""
Write-Host "========================================"
Write-Host "Master: ${Master}"
Write-Host "========================================"
Write-Host ""
$PreviousMaster = $env:MMWX_MASTER
$PreviousToken = $env:MMWX_SPEEDTEST_TOKEN
try {
    $env:MMWX_MASTER = $Master
    $env:MMWX_SPEEDTEST_TOKEN = $Token
    & $OutputPath
}
finally {
    if ($null -eq $PreviousMaster) {
        Remove-Item Env:MMWX_MASTER -ErrorAction SilentlyContinue
    } else {
        $env:MMWX_MASTER = $PreviousMaster
    }
    if ($null -eq $PreviousToken) {
        Remove-Item Env:MMWX_SPEEDTEST_TOKEN -ErrorAction SilentlyContinue
    } else {
        $env:MMWX_SPEEDTEST_TOKEN = $PreviousToken
    }
}
