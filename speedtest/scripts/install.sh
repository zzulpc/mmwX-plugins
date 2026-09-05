#!/bin/bash
# mmwx-speedtester install & run script
# Usage: curl -fsSL <url>/install.sh | bash -s -- -master https://your-master-url -token <token>
set -euo pipefail

REPO="zzulpc/mmwX-plugins"
BINARY_NAME="mmwx-speedtester"
INSTALL_DIR="."
ASSET_NAME=""
DOWNLOAD_URL=""
CHECKSUMS_URL=""
CHECKSUM_FILE=""
BINARY_PATH=""

# 无论校验在哪一步退出，都清理下载到临时目录的校验清单。
cleanup_checksum_file() {
  if [ -n "${CHECKSUM_FILE}" ]; then
    rm -f -- "${CHECKSUM_FILE}"
  fi
}
trap cleanup_checksum_file EXIT

# Parse arguments
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

# Detect OS and architecture
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

# Get download URL from latest release
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
    rm -f -- "$output"
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
    rm -f -- "$output"
    echo "Checksum for ${ASSET_NAME} not found or invalid."
    exit 1
  fi

  if command -v sha256sum >/dev/null 2>&1; then
    actual_hash=$(sha256sum "$output" | awk '{print tolower($1)}')
  elif command -v shasum >/dev/null 2>&1; then
    actual_hash=$(shasum -a 256 "$output" | awk '{print tolower($1)}')
  else
    rm -f -- "$output"
    echo "Neither sha256sum nor shasum is available."
    exit 1
  fi

  if [ "$actual_hash" != "$expected_hash" ]; then
    rm -f -- "$output"
    echo "Checksum mismatch for ${ASSET_NAME}."
    exit 1
  fi

  rm -f -- "$CHECKSUM_FILE"
  CHECKSUM_FILE=""
}

# Download binary
download_binary() {
  local output="${INSTALL_DIR}/${BINARY_NAME}"
  if [ "$OS" = "windows" ]; then
    output="${output}.exe"
  fi

  echo "Downloading ${BINARY_NAME} (${OS}/${ARCH})..."
  curl -fsSL -o "$output" "$DOWNLOAD_URL" || {
    rm -f -- "$output"
    echo "Download failed"; exit 1
  }
  verify_checksum "$output"
  chmod +x "$output"
  echo "Saved to: ${output}"
  BINARY_PATH="$output"
}

# Run
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
