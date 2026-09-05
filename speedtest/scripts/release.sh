#!/bin/bash
# mmwx-speedtester 一键发布：更新版本、变更日志、提交并推送 speedtest-vX.Y.Z 标签。
# GitHub Action 会在该标签上创建 Release、上传多平台二进制与构建容器镜像。
# 用法:bash scripts/release.sh [patch|minor|major]   (默认 patch)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLUGIN_DIR="$(dirname "$SCRIPT_DIR")"   # .../mmwX-plugins/speedtest
REPO_ROOT="$(dirname "$PLUGIN_DIR")"    # .../mmwX-plugins
cd "$REPO_ROOT"

BUMP="${1:-patch}"
# 镜像引用在 README 与 Dockerfile 两处替换里都要用。
IMAGE_REF="ghcr.io/zzulpc/mmwx-speedtester"
# changelog 插入点有两种写法:grep 用字面量，sed 地址里的 / 必须转义，不能共用一个变量。
CHANGELOG_MARKER='<summary>更新日志</summary>'
CHANGELOG_MARKER_RE='<summary>更新日志<\/summary>'
BUILD_TMP=""
RELEASE_TMP_DIR=""
CHANGELOG_TMP=""
README_TMP=""
DOCKERFILE_TMP=""
ORIGINAL_HEAD=""
TAG=""
CHANGES_STARTED=0
COMMIT_CREATED=0
TAG_CREATED=0
RELEASE_DONE=0

# cleanup_release 在失败时只回滚本脚本生成的提交、标签和版本文件，不触碰其它路径。
cleanup_release() {
  local status=$?
  set +e
  if [ -n "$BUILD_TMP" ]; then
    rm -rf -- "$BUILD_TMP"
  fi
  if [ -n "$RELEASE_TMP_DIR" ]; then
    # 目录由本次 mktemp 创建；固定的 README.md.tmp 曾可能误删用户同名文件。
    rm -rf -- "$RELEASE_TMP_DIR"
  fi
  if [ "$status" -ne 0 ] && [ "$CHANGES_STARTED" -eq 1 ] && [ "$RELEASE_DONE" -eq 0 ]; then
    if [ "$TAG_CREATED" -eq 1 ]; then
      git tag -d "$TAG" >/dev/null 2>&1
    fi
    if [ "$COMMIT_CREATED" -eq 1 ]; then
      git reset --soft "$ORIGINAL_HEAD" >/dev/null 2>&1
    fi
    git restore --source="$ORIGINAL_HEAD" --staged --worktree -- speedtest/VERSION speedtest/README.md speedtest/Dockerfile >/dev/null 2>&1
    echo "[ROLLBACK] 发布失败，已回滚本地提交、标签和版本文件。" >&2
  fi
  exit "$status"
}
trap cleanup_release EXIT

# count_literal_in_file 统计字面量出现次数，不依赖 GNU grep 的扩展选项。
count_literal_in_file() {
  local needle="$1"
  local file="$2"
  awk -v needle="$needle" '
    {
      line = $0
      while ((position = index(line, needle)) > 0) {
        count++
        line = substr(line, position + length(needle))
      }
    }
    END { print count + 0 }
  ' "$file"
}

# README 的更新日志必须保留历史版本，因此发布版本替换和计数都只检查 marker 之前的正文。
count_literal_before_changelog() {
  local needle="$1"
  local file="$2"
  awk -v needle="$needle" -v marker="$CHANGELOG_MARKER" '
    index($0, marker) { stopped = 1 }
    !stopped {
      line = $0
      while ((position = index(line, needle)) > 0) {
        count++
        line = substr(line, position + length(needle))
      }
    }
    END { print count + 0 }
  ' "$file"
}

# replace_release_version_refs 是发布流程与单元测试共用的纯文件变换：调用方传入自己的临时文件，
# 它只更新 README 当前说明和 Dockerfile 示例，并以旧值清零、新值数量守恒作为硬闸门。
replace_release_version_refs() {
  local current_version="$1"
  local new_version="$2"
  local readme_file="$3"
  local dockerfile_file="$4"
  local readme_tmp="$5"
  local dockerfile_tmp="$6"
  local current_version_re
  local current_tag_ref
  local new_tag_ref
  local current_image_ref
  local new_image_ref
  local readme_tag_replacements
  local readme_image_replacements
  local dockerfile_image_replacements

  if [[ ! "$current_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || \
     [[ ! "$new_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "[ERROR] 版本变换只接受 X.Y.Z 三段数字格式。" >&2
    return 1
  fi

  current_tag_ref="\`speedtest-v${current_version}\`"
  new_tag_ref="\`speedtest-v${new_version}\`"
  current_image_ref="${IMAGE_REF}:${current_version}"
  new_image_ref="${IMAGE_REF}:${new_version}"
  readme_tag_replacements="$(count_literal_before_changelog "$current_tag_ref" "$readme_file")"
  readme_image_replacements="$(count_literal_before_changelog "$current_image_ref" "$readme_file")"
  dockerfile_image_replacements="$(count_literal_in_file "$current_image_ref" "$dockerfile_file")"
  if [ "$readme_tag_replacements" -eq 0 ] || [ "$readme_image_replacements" -eq 0 ] || \
     [ "$dockerfile_image_replacements" -eq 0 ]; then
    echo "[ERROR] 发布版本引用缺失：README tag=${readme_tag_replacements}，README image=${readme_image_replacements}，Dockerfile image=${dockerfile_image_replacements}。" >&2
    return 1
  fi

  current_version_re="${current_version//./\\.}"
  sed -e "1,/${CHANGELOG_MARKER_RE}/s|\`speedtest-v${current_version_re}\`|${new_tag_ref}|g" \
      -e "1,/${CHANGELOG_MARKER_RE}/s|${IMAGE_REF}:${current_version_re}|${new_image_ref}|g" \
      "$readme_file" > "$readme_tmp"
  mv "$readme_tmp" "$readme_file"
  sed -e "s|${IMAGE_REF}:${current_version_re}|${new_image_ref}|g" \
      "$dockerfile_file" > "$dockerfile_tmp"
  mv "$dockerfile_tmp" "$dockerfile_file"

  if [ "$(count_literal_before_changelog "$current_tag_ref" "$readme_file")" -ne 0 ] || \
     [ "$(count_literal_before_changelog "$current_image_ref" "$readme_file")" -ne 0 ] || \
     [ "$(count_literal_in_file "$current_image_ref" "$dockerfile_file")" -ne 0 ]; then
    echo "[ERROR] 版本替换后仍残留正文中的旧版本 ${current_version}。" >&2
    return 1
  fi
  if [ "$(count_literal_before_changelog "$new_tag_ref" "$readme_file")" -ne "$readme_tag_replacements" ] || \
     [ "$(count_literal_before_changelog "$new_image_ref" "$readme_file")" -ne "$readme_image_replacements" ] || \
     [ "$(count_literal_in_file "$new_image_ref" "$dockerfile_file")" -ne "$dockerfile_image_replacements" ]; then
    echo "[ERROR] 版本替换数量不守恒，拒绝继续发布。" >&2
    return 1
  fi
}

# 内部测试入口只变换调用方提供的临时样本，不检查分支、不提交、不打标签也不推送。
if [ "$BUMP" = "--verify-version-transform" ]; then
  if [ "$#" -ne 5 ]; then
    echo "用法: $0 --verify-version-transform CURRENT NEW README_FIXTURE DOCKERFILE_FIXTURE" >&2
    exit 2
  fi
  RELEASE_TMP_DIR="$(mktemp -d)"
  replace_release_version_refs "$2" "$3" "$4" "$5" \
    "$RELEASE_TMP_DIR/README.md" "$RELEASE_TMP_DIR/Dockerfile"
  exit 0
fi

case "$BUMP" in
  major|minor|patch) ;;
  *) echo "[ERROR] 未知 bump 类型: $BUMP (patch|minor|major)"; exit 1 ;;
esac

CURRENT_BRANCH="$(git branch --show-current)"
if [ "$CURRENT_BRANCH" != "main" ]; then
  echo "[ERROR] 发布只能在 main 分支执行，当前分支为 ${CURRENT_BRANCH:-分离 HEAD}。"
  exit 1
fi
if [ -n "$(git status --porcelain)" ]; then
  echo "[ERROR] 发布前工作区必须干净。"
  exit 1
fi

CUR="$(< "$PLUGIN_DIR/VERSION")"
if [[ ! "$CUR" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "[ERROR] speedtest/VERSION 必须严格使用 X.Y.Z 三段数字格式，当前为: $CUR"
  exit 1
fi

MARKER_COUNT="$(count_literal_in_file "$CHANGELOG_MARKER" "$PLUGIN_DIR/README.md")"
if [ "$MARKER_COUNT" -ne 1 ]; then
  echo "[ERROR] README 的更新日志插入点必须正好出现一次，当前为 ${MARKER_COUNT} 次。"
  exit 1
fi
INSERT_LINE=$(grep -n -m1 "$CHANGELOG_MARKER" "$PLUGIN_DIR/README.md" | cut -d: -f1)

# 所有工作文件都放在本次运行的专属目录里；必须在 clean-check 之后才创建。
RELEASE_TMP_DIR="$(mktemp -d "$PLUGIN_DIR/.release.XXXXXX")"
README_TMP="$RELEASE_TMP_DIR/README.md"
DOCKERFILE_TMP="$RELEASE_TMP_DIR/Dockerfile"
CHANGELOG_TMP="$RELEASE_TMP_DIR/changelog.md"

echo "[1/6] 构建、静态检查与测试..."
BUILD_TMP="$(mktemp -d)"
(
  cd "$PLUGIN_DIR"
  go build -mod=readonly -o "$BUILD_TMP/mmwx-speedtester" ./...
  go vet -mod=readonly ./...
  go test -mod=readonly ./... -count=1
)
rm -rf -- "$BUILD_TMP"
BUILD_TMP=""

IFS='.' read -r MAJ MIN PAT <<< "$CUR"
case "$BUMP" in
  major) MAJ=$((10#$MAJ+1)); MIN=0; PAT=0 ;;
  minor) MIN=$((10#$MIN+1)); PAT=0 ;;
  patch) PAT=$((10#$PAT+1)) ;;
esac
NEW_VERSION="${MAJ}.${MIN}.${PAT}"
TAG="speedtest-v${NEW_VERSION}"
if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
  echo "[ERROR] 标签 $TAG 已存在。"
  exit 1
fi

ORIGINAL_HEAD="$(git rev-parse HEAD)"
CHANGES_STARTED=1
echo "[2/6] 版本 ${CUR} -> ${NEW_VERSION}(含 README 与 Dockerfile 的镜像引用)"
echo "$NEW_VERSION" > "$PLUGIN_DIR/VERSION"

# README 与 Dockerfile 里的镜像用法示例是手写的。以前这里只 bump VERSION，
# 于是照 README 一键拉取的用户拿到的永远是上一版镜像。
# README 只改 changelog 之前的正文 —— changelog 记的是历史版本，不能跟着改。
# 一致性由用例 TestDocs引用当前发布版本 守着；共用 helper 另外检查 sed 命中与数量守恒。
replace_release_version_refs "$CUR" "$NEW_VERSION" \
  "$PLUGIN_DIR/README.md" "$PLUGIN_DIR/Dockerfile" "$README_TMP" "$DOCKERFILE_TMP"

# 2. changelog:取自上个 speedtest tag 以来、改动了 speedtest/ 的 commit
PREV_TAG=$(git describe --tags --match 'speedtest-v*' --abbrev=0 2>/dev/null || echo "")
if [ -n "$PREV_TAG" ]; then
  RANGE="${PREV_TAG}..HEAD"
else
  RANGE="HEAD"
fi
COMMITS=$(git log "$RANGE" --pretty=format:"- %s" --no-merges -- speedtest/ | grep -v "^- speedtest-v[0-9]" | sort -u || true)
[ -z "$COMMITS" ] && COMMITS="- 维护性发布"
echo "=== 变更 ==="; echo "$COMMITS"; echo ""

# 3. 更新 README changelog(插入到 <summary>更新日志</summary> 之后)
echo "[3/6] 更新 README changelog..."
TODAY=$(date +%Y-%m-%d)
{ echo ""; echo "### v${NEW_VERSION} (${TODAY})"; echo "$COMMITS"; } > "$CHANGELOG_TMP"
{ head -n "$INSERT_LINE" "$PLUGIN_DIR/README.md"; cat "$CHANGELOG_TMP"; tail -n +"$((INSERT_LINE+1))" "$PLUGIN_DIR/README.md"; } > "$README_TMP"
mv "$README_TMP" "$PLUGIN_DIR/README.md"
rm -f "$CHANGELOG_TMP"
CHANGELOG_TMP=""

# 4. 文件变换完成后再次执行测试，确保验证的是将要提交的版本，而不是修改前的快照。
echo "[4/6] 对发布后的文件执行最终测试..."
(
  cd "$PLUGIN_DIR"
  go test -mod=readonly ./... -count=1
)

# 5. commit + tag + push
echo "[5/6] commit + tag ${TAG}..."
git add speedtest/VERSION speedtest/README.md speedtest/Dockerfile
git commit -m "发布测速端 ${TAG}"
COMMIT_CREATED=1
git tag "$TAG"
TAG_CREATED=1
echo "[6/6] 原子推送分支与标签..."
git push --atomic origin main "$TAG"
RELEASE_DONE=1

echo ""
echo "=== 标签已推送：${TAG} ==="
echo "  GitHub Action 将创建 Release，并发布多平台二进制与容器镜像。"
