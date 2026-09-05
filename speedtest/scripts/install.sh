#!/bin/bash
# mmwx-speedtester 安装与启动脚本。
# 用法：curl -fsSL <url>/install.sh | bash -s -- -master https://your-master-url -token <token>
set -euo pipefail

REPO="zzulpc/mmwX-plugins"
BINARY_NAME="mmwx-speedtester"
INSTALL_DIR="."
ASSET_NAME=""
DOWNLOAD_URL=""
CHECKSUMS_URL=""
CHECKSUM_FILE=""
DOWNLOAD_FILE=""
BINARY_PATH=""

# 只清理本次创建的临时文件；下载或校验失败时，原有程序必须保持可用。
cleanup_install_files() {
  if [ -n "${CHECKSUM_FILE}" ]; then
    rm -f -- "${CHECKSUM_FILE}"
  fi
  if [ -n "${DOWNLOAD_FILE}" ]; then
    rm -f -- "${DOWNLOAD_FILE}"
  fi
}
trap cleanup_install_files EXIT

# 解析主控配对参数。
MASTER=""
TOKEN=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    -master) MASTER="$2"; shift 2 ;;
    -token) TOKEN="$2"; shift 2 ;;
    *) echo "Unknown argument: $1"; exit 1 ;;
  esac
done

if [ -z "$MASTER" ] || [ -z "$TOKEN" ]; then
  echo "Usage: bash install.sh -master <master-url> -token <token>"
  exit 1
fi

# 发布资产按操作系统与架构区分，必须拒绝未提供二进制的平台。
detect_platform() {
  OS="$(uname -s | tr 'A-Z' 'a-z')"
  ARCH="$(uname -m)"

  case "$OS" in
    linux) OS="linux" ;;
    darwin) OS="darwin" ;;
    mingw*|msys*|cygwin*) OS="windows" ;;
    *) echo "Unsupported OS: $OS"; exit 1 ;;
  esac

  case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
  esac
}

# 二进制和校验清单必须来自同一个 Release，避免发布更新期间版本混用。
get_download_url() {
  ASSET_NAME="${BINARY_NAME}-${OS}-${ARCH}"
  if [ "$OS" = "windows" ]; then
    ASSET_NAME="${ASSET_NAME}.exe"
  fi

  echo "Fetching latest release..."
  local release_url="https://api.github.com/repos/${REPO}/releases/latest"
  local release_json
  release_json=$(curl -fsSL "$release_url") || {
    echo "Failed to fetch release info"; exit 1
  }

  DOWNLOAD_URL=$(echo "$release_json" | grep -o "\"browser_download_url\": *\"[^\"]*${ASSET_NAME}\"" | head -1 | cut -d'"' -f4)
  if [ -z "$DOWNLOAD_URL" ]; then
    echo "Asset ${ASSET_NAME} not found."
    echo "Visit https://github.com/${REPO}/releases/latest to download manually."
    exit 1
  fi

  CHECKSUMS_URL=$(echo "$release_json" | grep -o '"browser_download_url": *"[^"]*/checksums.txt"' | head -1 | cut -d'"' -f4)
  if [ -z "$CHECKSUMS_URL" ]; then
    echo "checksums.txt not found in release."
    exit 1
  fi

  VERSION=$(echo "$release_json" | grep -o '"tag_name": *"[^"]*"' | head -1 | cut -d'"' -f4)
  echo "Latest version: ${VERSION}"
}

# verify_checksum 使用同一个 Release 的 checksums.txt 校验产物，兼容 Linux 与 macOS 的摘要工具。
verify_checksum() {
  local output="$1"
  local expected_hash
  local actual_hash

  CHECKSUM_FILE="$(mktemp)"
  if ! curl -fsSL -o "$CHECKSUM_FILE" "$CHECKSUMS_URL"; then
    echo "Failed to download checksums.txt"
    exit 1
  fi

  expected_hash=$(awk -v asset="$ASSET_NAME" '
    {
      path = $2
      sub(/^\.\//, "", path)
      if (path == asset) {
        print tolower($1)
        exit
      }
    }
  ' "$CHECKSUM_FILE")
  if [[ ! "$expected_hash" =~ ^[0-9a-f]{64}$ ]]; then
    echo "Checksum for ${ASSET_NAME} not found or invalid."
    exit 1
  fi

  if command -v sha256sum >/dev/null 2>&1; then
    actual_hash=$(sha256sum "$output" | awk '{print tolower($1)}')
  elif command -v shasum >/dev/null 2>&1; then
    actual_hash=$(shasum -a 256 "$output" | awk '{print tolower($1)}')
  else
    echo "Neither sha256sum nor shasum is available."
    exit 1
  fi

  if [ "$actual_hash" != "$expected_hash" ]; then
    echo "Checksum mismatch for ${ASSET_NAME}."
    exit 1
  fi

  rm -f -- "$CHECKSUM_FILE"
  CHECKSUM_FILE=""
}

# 下载文件与正式路径位于同一目录，校验和权限设置成功后才能原子替换旧版。
download_binary() {
  local output="${INSTALL_DIR}/${BINARY_NAME}"
  if [ "$OS" = "windows" ]; then
    output="${output}.exe"
  fi

  if [ -d "$output" ]; then
    echo "Install path is a directory: ${output}"
    exit 1
  fi
  DOWNLOAD_FILE="$(mktemp "${INSTALL_DIR}/.${BINARY_NAME}.XXXXXX")"
  echo "Downloading ${BINARY_NAME} (${OS}/${ARCH})..."
  curl -fsSL -o "$DOWNLOAD_FILE" "$DOWNLOAD_URL" || {
    echo "Download failed"; exit 1
  }
  verify_checksum "$DOWNLOAD_FILE"
  chmod +x "$DOWNLOAD_FILE"
  if ! mv -f -- "$DOWNLOAD_FILE" "$output"; then
    echo "Failed to replace ${output}; the previous binary was preserved. Stop the running tester and retry."
    exit 1
  fi
  DOWNLOAD_FILE=""
  echo "Saved to: ${output}"
  BINARY_PATH="$output"
}

# 仅在完整安装成功后启动，并通过环境变量向子进程传递令牌。
run_binary() {
  echo ""
  echo "========================================"
  echo "Master: ${MASTER}"
  echo "========================================"
  echo ""
  MMWX_MASTER="$MASTER" MMWX_SPEEDTEST_TOKEN="$TOKEN" exec "$BINARY_PATH"
}

detect_platform
get_download_url
download_binary
run_binary
