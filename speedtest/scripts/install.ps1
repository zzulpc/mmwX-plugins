# mmwx-speedtester Windows install & run script
# Usage: .\install.ps1 -Master https://your-master-url -Token <token>
param(
    [Parameter(Mandatory=$true)][string]$Master,
    [Parameter(Mandatory=$true)][string]$Token
)

$ErrorActionPreference = "Stop"
$Repo = "zzulpc/mmwX-plugins"
$BinaryName = "mmwx-speedtester"

# Detect architecture
$Arch = if ([Environment]::Is64BitOperatingSystem) {
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
} else {
    Write-Error "32-bit systems are not supported"; exit 1
}

$AssetName = "${BinaryName}-windows-${Arch}.exe"
Write-Host "Platform: windows/${Arch}"

# Get latest release
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

# Download
$OutputPath = Join-Path $PWD "${BinaryName}.exe"
$ChecksumsPath = [System.IO.Path]::GetTempFileName()
try {
    Write-Host "Downloading ${AssetName}..."
    Invoke-WebRequest -Uri $Asset.browser_download_url -OutFile $OutputPath
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

    $ActualHash = (Get-FileHash -Path $OutputPath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($ActualHash -ne $ExpectedHash) {
        throw "Checksum mismatch for ${AssetName}."
    }
    Write-Host "Saved to: ${OutputPath}"
}
catch {
    if (Test-Path -LiteralPath $OutputPath) {
        Remove-Item -LiteralPath $OutputPath -Force
    }
    Write-Error $_.Exception.Message
    exit 1
}
finally {
    if (Test-Path -LiteralPath $ChecksumsPath) {
        Remove-Item -LiteralPath $ChecksumsPath -Force
    }
}

# Run
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
