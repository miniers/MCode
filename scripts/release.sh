#!/usr/bin/env bash
# 本地创建并推送发布标签。
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"

usage() {
    echo '用法: scripts/release.sh [patch|minor|major|版本号] [--notes 文件] [--dry-run] [-h|--help]'
}

VERSION_ARG=""
NOTES_FILE=""
DRY_RUN=0
while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run) DRY_RUN=1 ;;
        --notes)
            [[ $# -ge 2 && -n "$2" && "$2" != -* ]] || { echo '错误: --notes 缺少文件' >&2; exit 1; }
            [[ -f "$2" && -r "$2" ]] || { echo "错误: --notes 文件不可读: $2" >&2; exit 1; }
            NOTES_FILE="$(cd "$(dirname "$2")" && pwd -P)/$(basename "$2")"
            shift
            ;;
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

TEMP_FILE=""
NOTES_TEMP=""
CHANGELOG_TEMP=""
trap 'rm -f "$TEMP_FILE" "$NOTES_TEMP" "$CHANGELOG_TEMP"' EXIT
NOTES_TEMP="$(mktemp "${TMPDIR:-/tmp}/mcode-notes.XXXXXX")"
if [[ -n "$NOTES_FILE" ]]; then
    cp "$NOTES_FILE" "$NOTES_TEMP"
else
    PREVIOUS_TAG="$(git describe --tags --abbrev=0 --match 'v*' 2>/dev/null || true)"
    if [[ -n "$PREVIOUS_TAG" ]]; then
        git log "$PREVIOUS_TAG..HEAD" --no-merges --format=%s
    else
        git log HEAD --no-merges --format=%s
    fi | awk '
        tolower($0) ~ /^(chore|test|tests|docs|ci|style|build)(\(.*\))?!?: / { next }
        { sub(/^[[:alnum:]_-]+(\([^)]*\))?!?: /, ""); print "- " $0 }
    ' > "$NOTES_TEMP"
    if [[ -t 0 && -t 1 ]]; then
        "${VISUAL:-${EDITOR:-vi}}" "$NOTES_TEMP"
    fi
fi
NOTES="$(awk '
    /^[[:space:]]*$/ { if (started) blanks = blanks "\n"; next }
    { if (started) printf "%s", blanks; print; started = 1; blanks = "" }
' "$NOTES_TEMP")"
[[ -n "$NOTES" ]] || { echo '错误: 更新内容为空，请使用 --notes 指定内容' >&2; exit 1; }
printf '版本: %s -> %s\n' "$CURRENT" "$NEXT"
if [[ $DRY_RUN -eq 1 ]]; then
    printf '更新内容:\n%s\n' "$NOTES"
    printf 'git add mygo.json CHANGELOG.md\ngit commit -m "chore(release): %s"\ngit tag -a %s -m %s\ngit push --atomic origin main %s\n' "$TAG" "$TAG" "$TAG" "$TAG"
    exit 0
fi

TEMP_FILE="$(mktemp "${TMPDIR:-/tmp}/mcode-release.XXXXXX")"
CHANGELOG_TEMP="$(mktemp "${TMPDIR:-/tmp}/mcode-changelog.XXXXXX")"
# 先准备两个文件，避免生成内容时留下部分修改。
awk -v version="$NEXT" ' !done && /^[[:space:]]*"version"[[:space:]]*:[[:space:]]*"[^"]+"/ { match($0, /^[[:space:]]*"version"[[:space:]]*:[[:space:]]*"/); prefix=substr($0, 1, RLENGTH); rest=substr($0, RLENGTH+1); sub(/^[^"]+/, version, rest); $0=prefix rest; done=1 } { print }' mygo.json > "$TEMP_FILE"
printf '%s\n' "$NOTES" > "$NOTES_TEMP"
if [[ -e CHANGELOG.md ]]; then
    awk -v version="$NEXT" -v notes_file="$NOTES_TEMP" '
        function section() {
            print "## " version; print ""
            while ((getline line < notes_file) > 0) print line
            close(notes_file)
        }
        !inserted && /^## / { section(); print ""; inserted = 1 }
        { print }
        END { if (!inserted) { if (NR) print ""; section() } }
    ' CHANGELOG.md > "$CHANGELOG_TEMP"
else
    { printf '# 更新日志\n\n## %s\n\n' "$NEXT"; printf '%s\n' "$NOTES"; } > "$CHANGELOG_TEMP"
fi
cp "$TEMP_FILE" mygo.json
cp "$CHANGELOG_TEMP" CHANGELOG.md
git add mygo.json CHANGELOG.md
git commit -m "chore(release): $TAG"
git tag -a "$TAG" -m "$TAG"
git push --atomic origin main "$TAG"
printf '发布标签: %s\nhttps://github.com/miniers/MCode/actions\n' "$TAG"
