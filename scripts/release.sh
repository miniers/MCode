#!/usr/bin/env bash
# 本地创建并推送发布标签。
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"

load_dotenv() {
    local line key value
    [[ -f "$ROOT_DIR/.env" ]] || return 0
    while IFS= read -r line || [[ -n $line ]]; do
        line="${line#"${line%%[![:space:]]*}"}"
        [[ -z "$line" || "$line" == \#* ]] && continue
        if [[ "$line" =~ ^export[[:space:]]+ ]]; then
            line="${line#"${BASH_REMATCH[0]}"}"
        fi
        [[ "$line" =~ ^[A-Za-z_][A-Za-z0-9_]*= ]] || continue
        key="${line%%=*}"
        value="${line#*=}"
        if [[ ${#value} -ge 2 ]] && { [[ "$value" == \"*\" ]] || [[ "$value" == \'*\' ]]; }; then
            value="${value:1:${#value}-2}"
        fi
        if [[ ! -n "${!key+x}" ]]; then
            export "$key=$value"
        fi
    done < "$ROOT_DIR/.env"
}
load_dotenv

usage() {
    echo '用法: scripts/release.sh [patch|minor|major|版本号] [--notes 文件] [--dry-run] [-h|--help]'
    echo '在 .env 中使用 MCODE_RELEASE_AGENT 和 MCODE_RELEASE_MODEL 配置本地 agent。'
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
AI_AGENT="${MCODE_RELEASE_AGENT:-claude}"
AI_MODEL="${MCODE_RELEASE_MODEL:-}"
CONFIRM_SECONDS="${MCODE_RELEASE_CONFIRM_SECONDS:-60}"
if [[ -z "$NOTES_FILE" && -z "${MCODE_RELEASE_NOTES_CMD:-}" ]]; then
    case "$AI_AGENT" in
        claude|codex|pi) ;;
        *) echo '错误: 不支持的 MCODE_RELEASE_AGENT（可选 claude、codex、pi）' >&2; exit 1 ;;
    esac
fi
cd "$ROOT_DIR"
top="$(git rev-parse --show-toplevel 2>/dev/null)" && [[ "$(cd "$top" && pwd -P)" == "$ROOT_DIR" ]] || { echo '错误: 脚本必须位于仓库根目录下的 scripts 中' >&2; exit 1; }
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
AI_INPUT_TEMP=""
AI_OUTPUT_TEMP=""
AI_LAST_TEMP=""
trap 'rm -f "$TEMP_FILE" "$NOTES_TEMP" "$CHANGELOG_TEMP" "$AI_INPUT_TEMP" "$AI_OUTPUT_TEMP" "$AI_LAST_TEMP"' EXIT
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
    AI_INPUT_TEMP="$(mktemp "${TMPDIR:-/tmp}/mcode-ai-input.XXXXXX")"
    AI_OUTPUT_TEMP="$(mktemp "${TMPDIR:-/tmp}/mcode-ai-output.XXXXXX")"
    if [[ -n "$PREVIOUS_TAG" ]]; then
        git log "$PREVIOUS_TAG..HEAD" --no-merges --format='- %s%n%b' > "$AI_INPUT_TEMP"
        printf '\n改动文件统计:\n' >> "$AI_INPUT_TEMP"
        git diff --stat "$PREVIOUS_TAG..HEAD" >> "$AI_INPUT_TEMP"
    else
        git log HEAD --no-merges --format='- %s%n%b' > "$AI_INPUT_TEMP"
    fi
    if [[ -n "${MCODE_RELEASE_NOTES_CMD:-}" ]]; then
        read -r -a AI_CMD <<< "$MCODE_RELEASE_NOTES_CMD"
    else
        case "$AI_AGENT" in
            claude) AI_CMD=(claude -p); [[ -z "$AI_MODEL" ]] || AI_CMD+=(--model "$AI_MODEL") ;;
            pi) AI_CMD=(pi -p --no-session --no-tools); [[ -z "$AI_MODEL" ]] || AI_CMD+=(--model "$AI_MODEL") ;;
            codex)
                AI_LAST_TEMP="$(mktemp "${TMPDIR:-/tmp}/mcode-ai-last.XXXXXX")"
                AI_CMD=(codex exec --skip-git-repo-check --output-last-message "$AI_LAST_TEMP")
                [[ -z "$AI_MODEL" ]] || AI_CMD+=(-m "$AI_MODEL")
                ;;
        esac
    fi
    if [[ ${#AI_CMD[@]} -eq 0 ]] || ! command -v "${AI_CMD[0]}" >/dev/null 2>&1; then
        echo '提示: 未找到 AI 命令，使用提交标题草稿' >&2
    else
        echo "正在用 ${AI_CMD[0]}（${AI_MODEL:-默认模型}）生成更新内容…" >&2
        PROMPT="你正在为 MCode（面向 AI 编程代理的 macOS 桌面应用）版本 $NEXT 撰写发布说明。根据以下提交和文件统计，用 Markdown 撰写面向用户的中文亮点。按 ### 新功能、### 改进、### 修复 分组，省略空分组；每条用一句短句，总计最多 8 条；合并相关提交；省略测试、重构、CI、文档、发布杂务和内部细节。不要版本标题、前言或结束语，只输出 Markdown。

$(head -c 60000 "$AI_INPUT_TEMP")"
        if perl -e 'alarm shift; exec @ARGV' 180 "${AI_CMD[@]}" "$PROMPT" </dev/null > "$AI_OUTPUT_TEMP"; then
            AI_NOTES="$(awk '
                BEGIN { fence = sprintf("%c%c%c", 96, 96, 96) "[[:alnum:]_+-]*$" }
                $0 ~ "^" fence || /^## / || /^# / { next }
                /^[[:space:]]*$/ { if (started) blanks = blanks "\n"; next }
                { if (started) printf "%s", blanks; print; started = 1; blanks = "" }
            ' "${AI_LAST_TEMP:-$AI_OUTPUT_TEMP}")"
        else
            AI_NOTES=""
        fi
        if [[ -n "$AI_NOTES" ]]; then
            printf '%s\n' "$AI_NOTES" > "$NOTES_TEMP"
        else
            echo '警告: AI 生成失败，改用提交标题草稿' >&2
        fi
    fi
    if [[ -t 0 && -t 1 ]]; then
        while true; do
            printf '更新内容:\n'
            cat "$NOTES_TEMP"
            answer=""; c=""; rest=""
            for ((s=CONFIRM_SECONDS; s>0; s--)); do
                printf '\r确认使用？[Y/e/n]（%d 秒后自动确认）: \033[K' "$s"
                if IFS= read -r -n 1 -t 1 c; then
                    if [[ "$c" == $'\004' ]]; then
                        echo '已取消'
                        exit 0
                    fi
                    if [[ -n "$c" ]]; then
                        if ! IFS= read -r rest; then
                            echo '已取消'
                            exit 0
                        fi
                        answer="$c$rest"
                    fi
                    break
                fi
            done
            if (( s == 0 )); then
                printf '\n已自动确认\n'
                break
            fi
            case "$answer" in
                ''|y|Y) break ;;
                e|E) "${VISUAL:-${EDITOR:-vi}}" "$NOTES_TEMP" ;;
                n|N) echo '已取消'; exit 0 ;;
            esac
        done
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
