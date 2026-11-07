#!/usr/bin/env bash
# 本地创建并推送发布标签。
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"

usage() {
    echo '用法: scripts/release.sh [patch|minor|major|版本号] [--dry-run] [-h|--help]'
}

VERSION_ARG=""
DRY_RUN=0
while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run) DRY_RUN=1 ;;
        -h|--help) usage; exit 0 ;;
        -*) echo "错误: 未知参数 $1" >&2; exit 1 ;;
        *)
            [[ -z "$VERSION_ARG" ]] || { echo '错误: 只能指定一个版本' >&2; exit 1; }
            VERSION_ARG="$1"
            ;;
    esac
    shift
done
VERSION_ARG="${VERSION_ARG:-patch}"
cd "$ROOT_DIR"
[[ "$(git rev-parse --show-toplevel 2>/dev/null)" == "$ROOT_DIR" ]] || { echo '错误: 脚本必须位于仓库根目录下的 scripts 中' >&2; exit 1; }
[[ "$(git branch --show-current)" == main ]] || { echo '错误: 必须在 main 分支发布' >&2; exit 1; }
[[ -z "$(git status --porcelain)" ]] || { echo '错误: 工作区不干净，请先处理所有改动' >&2; exit 1; }

CURRENT="$(sed -nE 's/^[[:space:]]*"version"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' mygo.json | head -1)"
[[ "$CURRENT" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)(-[0-9A-Za-z.-]+)?$ ]] || { echo '错误: mygo.json 中的版本号无效' >&2; exit 1; }
major="${BASH_REMATCH[1]}"; minor="${BASH_REMATCH[2]}"; patch="${BASH_REMATCH[3]}"
case "$VERSION_ARG" in
    patch) NEXT="$major.$minor.$((10#$patch + 1))" ;;
    minor) NEXT="$major.$((10#$minor + 1)).0" ;;
    major) NEXT="$((10#$major + 1)).0.0" ;;
    *) NEXT="$VERSION_ARG" ;;
esac
[[ "$NEXT" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo '错误: 版本号格式无效' >&2; exit 1; }
[[ "$NEXT" != "$CURRENT" ]] || { echo '错误: 新版本与当前版本相同' >&2; exit 1; }
TAG="v$NEXT"
if git show-ref --verify --quiet "refs/tags/$TAG"; then
    echo "错误: 本地标签 $TAG 已存在" >&2; exit 1
fi
REMOTE_TAG="$(git ls-remote --tags origin "refs/tags/$TAG")" || { echo '错误: 无法查询远程标签' >&2; exit 1; }
[[ -z "$REMOTE_TAG" ]] || { echo "错误: 远程标签 $TAG 已存在" >&2; exit 1; }

if [[ $DRY_RUN -eq 1 ]]; then
    git fetch --dry-run --no-write-fetch-head origin main --tags
    REMOTE_MAIN="$(git ls-remote origin refs/heads/main)" || { echo '错误: 无法查询远程 main' >&2; exit 1; }
    REMOTE_MAIN="${REMOTE_MAIN%%[[:space:]]*}"
    [[ -n "$REMOTE_MAIN" ]] && git merge-base --is-ancestor "$REMOTE_MAIN" HEAD 2>/dev/null || { echo '错误: 本地 main 落后于远程 main，或尚未获取远程提交' >&2; exit 1; }
else
    git fetch origin main --tags
    git merge-base --is-ancestor origin/main HEAD || { echo '错误: 本地 main 落后于 origin/main' >&2; exit 1; }
fi

printf '版本: %s -> %s\n' "$CURRENT" "$NEXT"
if [[ $DRY_RUN -eq 1 ]]; then
    printf 'git add mygo.json\ngit commit -m "chore(release): %s"\ngit tag -a %s -m %s\ngit push --atomic origin main %s\n' "$TAG" "$TAG" "$TAG" "$TAG"
    exit 0
fi

# 仅替换第一个版本行，保留其余内容和原有格式。
TEMP_FILE="$(mktemp "${TMPDIR:-/tmp}/mcode-release.XXXXXX")"
trap 'rm -f "$TEMP_FILE"' EXIT
awk -v version="$NEXT" ' !done && /^[[:space:]]*"version"[[:space:]]*:[[:space:]]*"[^"]+"/ { match($0, /^[[:space:]]*"version"[[:space:]]*:[[:space:]]*"/); prefix=substr($0, 1, RLENGTH); rest=substr($0, RLENGTH+1); sub(/^[^"]+/, version, rest); $0=prefix rest; done=1 } { print }' mygo.json > "$TEMP_FILE"
cp "$TEMP_FILE" mygo.json
git add mygo.json
git commit -m "chore(release): $TAG"
git tag -a "$TAG" -m "$TAG"
git push --atomic origin main "$TAG"
printf '发布标签: %s\nhttps://github.com/miniers/MCode/actions\n' "$TAG"
