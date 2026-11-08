#!/bin/bash
# mcode 个人分发打包脚本
#
#   scripts/package_personal.sh [--clean] [--dmg] [--windows] [--bump] [--relaunch|--no-relaunch]
#
# 个人分发：有证书时用 Developer ID 签名，否则用临时签名；不做公证，安装到 /Applications。
# --windows（或 WINDOWS=1）在同一次构建里额外打包 windows/amd64，产物不签名，不安装。
# 每次运行都会先构建 web 前端（npm run build）；默认沿用 mygo.json 的 version。
# --bump（或 BUMP=1）时将 patch 加 1（0.1.0 -> 0.1.1）。
# MyGo 没有独立的 build 号字段（CFBundleVersion 直接取 version）。
# --clean（或 CLEAN=1）构建前清空输出目录；--dmg（或 DMG=1）额外交付安装镜像；
# --relaunch（默认，等同 RELAUNCH=1）安装后退出并重新启动应用；--no-relaunch（或 RELAUNCH=0）不重启。
# 重启由 scripts/relaunch_app.sh 在脱离会话的后台进程里完成，所以在 MCode 内置终端或“运行脚本”菜单里运行本脚本时也能完成重启。
# 需要 macOS；可用环境变量 MCODE_SIGN_IDENTITY 指定 Developer ID Application 签名身份。
# 也可以写在项目根目录的 .env 中，环境变量优先。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

load_dotenv() {
    local line key value
    [[ -f "$ROOT/.env" ]] || return 0
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
    done < "$ROOT/.env"
}
load_dotenv

IDENTITY="${MCODE_SIGN_IDENTITY:-}"
APP_NAME="MCode"
BUILD="$ROOT/build.noindex/personal"
TARGET="/Applications/$APP_NAME.app"
PLATFORM="darwin/arm64"
CLEAN="${CLEAN:-0}"
BUMP="${BUMP:-0}"
DMG="${DMG:-0}"
RELAUNCH="${RELAUNCH:-1}"
WINDOWS="${WINDOWS:-0}"
WINDOWS_PLATFORM="windows/amd64"

step() { printf '\n==> %s\n' "$1"; }

usage() {
    cat <<EOF
用法: $(basename "$0") [选项]

选项:
  --clean       构建前清空输出目录（等同 CLEAN=1）
  --bump        打包前把 mygo.json 的 version patch 加 1（等同 BUMP=1，默认不递增）
  --dmg         额外交付已签名、已校验的 DMG（等同 DMG=1）
  --windows     额外打包 Windows（windows/amd64）可执行文件与安装程序（等同 WINDOWS=1，默认不打包）
  --relaunch    安装后退出并重新启动应用（默认，等同 RELAUNCH=1）
  --no-relaunch 安装后不重新启动应用（等同 RELAUNCH=0）
  -h, --help    显示此帮助信息

说明: 每次运行都会先构建 web 前端（npm run build）再打包，默认沿用 mygo.json 的 version；
      仅在 --bump 或 BUMP=1 时递增 version（patch +1）。有证书时用 Developer ID 签名，
      否则用临时签名；不公证，安装到 ${TARGET}。
      安装后默认会重启应用，重启由一个脱离会话的后台进程完成，因此在 MCode 内置
      终端或“运行脚本”菜单里运行时也能重启成功；用 --no-relaunch 可以关掉。
      Windows 产物不签名、不安装；未安装 makensis 时只产出 exe，不生成安装程序
      （可用 brew install makensis 安装）。
环境变量:
  MCODE_SIGN_IDENTITY（可选，Developer ID Application 签名身份，可写在项目根目录 .env 中；未设置或钥匙串中找不到时用临时签名）
  MCODE_REFRESH_SNAPSHOT=1 时联网刷新 models.dev 能力快照（默认使用已提交的快照）；
  刷新后请检查 internal/modelsdev/snapshot.json 的 diff 再提交。
使用 --bump 或 BUMP=1 时，安装完成前若失败，会把 mygo.json 的版本号还原到运行前的值。
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --bump)
            BUMP=1
            shift
            ;;
        --clean)
            CLEAN=1
            shift
            ;;
        --dmg)
            DMG=1
            shift
            ;;
        --windows)
            WINDOWS=1
            shift
            ;;
        --relaunch)
            RELAUNCH=1
            shift
            ;;
        --no-relaunch)
            RELAUNCH=0
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "错误: 未知参数 $1" >&2
            usage
            exit 1
            ;;
    esac
done

[[ "$(uname -s)" == Darwin ]] || { echo "错误: 个人分发打包需要 macOS" >&2; exit 1; }
CODESIGN_IDENTITIES=""
if [[ -n "$IDENTITY" ]]; then
    CODESIGN_IDENTITIES="$(security find-identity -v -p codesigning 2>/dev/null || true)"
fi
if [[ -z "$IDENTITY" ]]; then
    SIGN_MODE=adhoc
    echo '[WARN] 未设置 MCODE_SIGN_IDENTITY，改用临时签名（ad hoc）打包' >&2
elif [[ "$CODESIGN_IDENTITIES" == *"$IDENTITY"* ]]; then
    SIGN_MODE=developer-id
else
    SIGN_MODE=adhoc
    echo "[WARN] 钥匙串中找不到签名身份 \"$IDENTITY\"，改用临时签名（ad hoc）打包" >&2
fi
if [[ "$SIGN_MODE" == adhoc ]]; then
    IDENTITY="-"
fi

cd "$ROOT"

if [[ $CLEAN -eq 1 ]]; then
    step "清理构建目录（--clean）"
    rm -rf "$BUILD"
fi
mkdir -p "$BUILD"

# 快照由 go:embed 打进二进制：默认使用仓库里已提交的版本，只有显式要求时才联网覆盖。
SNAPSHOT_SCRIPT="$ROOT/scripts/fetch_models_dev_snapshot.sh"
if [[ "${MCODE_REFRESH_SNAPSHOT:-0}" == 1 ]]; then
    step "刷新 models.dev 能力快照"
    [[ -x "$SNAPSHOT_SCRIPT" ]] || { echo "错误: 缺少可执行的 $SNAPSHOT_SCRIPT" >&2; exit 1; }
    "$SNAPSHOT_SCRIPT"
    echo "[WARN] 刷新后请检查 snapshot.json 的 diff 再提交" >&2
else
    step "使用已提交的 models.dev 能力快照（MCODE_REFRESH_SNAPSHOT=1 可刷新）"
fi

# internal/webassets/dist 是被 git 忽略的构建产物，go:embed 只嵌入已存在的文件，
# 所以打包前必须先构建前端，否则装出来的应用内嵌的还是旧的 web。
step "构建 Web 前端（web/ -> internal/webassets/dist）"
command -v npm >/dev/null || { echo "错误: 需要 npm 才能构建 web 前端" >&2; exit 1; }
if [[ ! -d "$ROOT/web/node_modules" ]]; then
    (cd "$ROOT/web" && npm ci)
fi
(cd "$ROOT/web" && npm run build)

VERSION="$(sed -nE 's/^[[:space:]]*"version"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' mygo.json | head -1)"
[[ -n "$VERSION" ]] || { echo "错误: 无法从 mygo.json 读取 version" >&2; exit 1; }

# 仅递增版本时备份，避免构建失败留下脏改动。
MYGO_JSON_BACKUP=""
if [[ "$BUMP" == 1 ]]; then
    MYGO_JSON_BACKUP="$(mktemp "${TMPDIR:-/tmp}/mcode-mygo-XXXXXX.json")"
    cp mygo.json "$MYGO_JSON_BACKUP"
fi

restore_mygo_json() {
    if [[ -f "$MYGO_JSON_BACKUP" ]]; then
        cp "$MYGO_JSON_BACKUP" mygo.json
        echo "错误: 打包未成功，已还原 mygo.json 的版本号" >&2
    fi
}

# 与安装阶段的清理共用一个 EXIT trap，避免后注册的 trap 覆盖前面的。
on_exit() {
    local status=$?
    if [[ "$status" -ne 0 ]]; then
        restore_mygo_json
    fi
    rm -f "$MYGO_JSON_BACKUP" 2>/dev/null || true
    # 尚未进入安装阶段时 cleanup_install 定义不存在，跳过即可。
    if declare -F cleanup_install >/dev/null; then
        cleanup_install "$status"
    fi
}

trap on_exit EXIT

bump_version() {
    local next
    if [[ "$VERSION" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
        next="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.$(( BASH_REMATCH[3] + 1 ))"
    else
        next="$VERSION.1"
    fi
    sed -i '' -E \
        "s/^([[:space:]]*\"version\"[[:space:]]*:[[:space:]]*)\"[^\"]+\"/\1\"$next\"/" \
        mygo.json
    VERSION="$next"
}

if [[ "$BUMP" == 1 ]]; then
    bump_version
fi

BUILD_PLATFORMS="$PLATFORM"
if [[ $WINDOWS -eq 1 ]]; then
    BUILD_PLATFORMS="$PLATFORM,$WINDOWS_PLATFORM"
fi

if [[ "$SIGN_MODE" == developer-id ]]; then
    SIGN_LABEL="Developer ID 签名"
else
    SIGN_LABEL="临时签名"
fi
step "构建 $APP_NAME ${VERSION}（${BUILD_PLATFORMS}，${SIGN_LABEL}，不公证）"
BUILD_ARGS=(
    "build"
    "-platform" "$BUILD_PLATFORMS"
    "-o" "$BUILD"
    "-sign" "$IDENTITY"
    "-skip-notarize"
)
# Windows 安装程序由 NSIS 生成；本机没有 makensis 时只交付 exe。
if [[ $WINDOWS -eq 1 ]] && ! command -v makensis >/dev/null; then
    BUILD_ARGS+=("-skip-installer")
    echo "[WARN] 未找到 makensis，Windows 只产出可执行文件，不生成安装程序" >&2
fi
# 只要 .app 时不生成 DMG，省掉一次镜像打包
if [[ $DMG -eq 0 ]]; then
    BUILD_ARGS+=("-skip-dmg")
fi

if ! go tool mygo "${BUILD_ARGS[@]}" > "$BUILD/build.log" 2>&1; then
    ERRORS="$(grep -E 'error' "$BUILD/build.log" | head -40 || true)"
    if [[ -n "$ERRORS" ]]; then
        printf '%s\n' "$ERRORS"
    else
        tail -40 "$BUILD/build.log"
    fi
    exit 1
fi

APP="$BUILD/darwin-arm64/$APP_NAME.app"
[[ -d "$APP" ]] || { echo "错误: 未生成 $APP" >&2; exit 1; }

step "验证签名与架构"
codesign --verify --deep --strict --verbose=2 "$APP"
EXECUTABLE="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$APP/Contents/Info.plist" 2>/dev/null || true)"
[[ -n "$EXECUTABLE" ]] || { echo "错误: Info.plist 缺少 CFBundleExecutable" >&2; exit 1; }
ARCHS="$(lipo -archs "$APP/Contents/MacOS/$EXECUTABLE" | tr -d '[:space:]')"
if [[ "$ARCHS" != "arm64" ]]; then
    echo "错误: 期望 Apple 芯片二进制，实际为: $ARCHS" >&2
    exit 1
fi

WINDOWS_EXES=()
if [[ $WINDOWS -eq 1 ]]; then
    step "验证 Windows 产物"
    WINDOWS_DIR="$BUILD/windows-amd64"
    shopt -s nullglob
    WINDOWS_EXES=("$WINDOWS_DIR"/*.exe)
    if [[ ${#WINDOWS_EXES[@]} -eq 0 ]]; then
        echo "错误: 未在 $WINDOWS_DIR 生成 Windows 可执行文件" >&2
        exit 1
    fi
fi

step "安装到 $TARGET"
TARGET_OLD="$TARGET.old"
INSTALL_TMP="$TARGET.tmp.$$"
BACKED_UP=0

restore_old() {
    if [[ "$BACKED_UP" -eq 1 ]] && [[ -e "$TARGET_OLD" ]]; then
        rm -rf "$TARGET" 2>/dev/null || true
        mv "$TARGET_OLD" "$TARGET" 2>/dev/null || true
        BACKED_UP=0
    fi
}

cleanup_install() {
    local status=$1
    if [[ "$status" -ne 0 ]]; then
        restore_old
    fi
    rm -rf "$INSTALL_TMP" 2>/dev/null || true
}

rm -rf "$INSTALL_TMP"
if ! ditto "$APP" "$INSTALL_TMP"; then
    exit 1
fi

rm -rf "$TARGET_OLD"
if [[ -e "$TARGET" ]]; then
    mv "$TARGET" "$TARGET_OLD"
    BACKED_UP=1
fi
if ! mv "$INSTALL_TMP" "$TARGET"; then
    exit 1
fi
xattr -dr com.apple.quarantine "$TARGET" 2>/dev/null || true

rm -rf "$TARGET_OLD"
BACKED_UP=0
rm -rf "$INSTALL_TMP" 2>/dev/null || true
# 安装已经完成，版本号的改动要保留：删掉备份并撤掉回滚。
rm -f "$MYGO_JSON_BACKUP" 2>/dev/null || true
trap - EXIT

step "刷新 LaunchServices 注册"
LSREGISTER=/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister
if [[ -x "$LSREGISTER" ]]; then
    "$LSREGISTER" -f "$TARGET" 2>/dev/null || true
fi

printf '\n安装成功: %s（版本 %s）\n' "$TARGET" "$VERSION"

if [[ $DMG -eq 1 ]]; then
    step "交付安装镜像"
    shopt -s nullglob
    DMGS=("$BUILD/darwin-arm64"/mcode-*.dmg)
    if [[ ${#DMGS[@]} -ne 1 ]]; then
        echo "错误: $BUILD/darwin-arm64 中应有且仅有一个 $APP_NAME DMG，实际 ${#DMGS[@]} 个" >&2
        exit 1
    fi
    DMG_PATH="$BUILD/$APP_NAME-$VERSION.dmg"
    mv "${DMGS[0]}" "$DMG_PATH"
    hdiutil verify "$DMG_PATH"
    if [[ "$SIGN_MODE" == developer-id ]]; then
        codesign --verify --strict "$DMG_PATH"
    fi
    printf 'DMG: %s\n' "$DMG_PATH"
fi

if [[ $WINDOWS -eq 1 ]]; then
    step "Windows 产物"
    for f in "${WINDOWS_EXES[@]}"; do
        printf 'Windows: %s\n' "$f"
    done
fi

# 重启必须是最后一步：应用退出会向子进程发 SIGHUP、关闭 pty，本脚本可能被直接杀掉。
# 所以重启交给辅助脚本，它把自己 fork 成新会话后在后台完成；必须传 $$ 让它等本脚本退出。
if [[ $RELAUNCH -eq 1 ]]; then
    step "重新启动 $APP_NAME"
    bash "$ROOT/scripts/relaunch_app.sh" "$APP_NAME" "$TARGET" "$$"
fi
