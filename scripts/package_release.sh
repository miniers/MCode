#!/usr/bin/env bash
# mcode 本地打包脚本
# 支持本地生产构建（.app 与 DMG）及代码签名检查。
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="$ROOT_DIR/dist"
PLATFORM="darwin/arm64"
SKIP_NOTARIZE=0

usage() {
    cat <<EOF
用法: $(basename "$0") [选项]

选项:
  --platform <target> darwin/arm64、darwin/amd64 或 darwin/universal（默认: darwin/arm64）
  --out <dir>         指定输出目录（默认: dist）
  --skip-notarize     跳过 macOS 公证步骤
  -h, --help          显示此帮助信息

环境变量:
  MCODE_SIGN_IDENTITY macOS 代码签名身份（如 "Developer ID Application: Company (TEAMID)"）。
                      若未指定，则默认使用 ad hoc 签名 ("-")。
  MYGO_UPDATER_PRIVATE_KEY
                      MyGo updater 的 ed25519 签名私钥（字符串内容）。
                      若未设置但 ~/.config/mcode/mygo-update.key 存在，将自动读取该文件。
  MCODE_REFRESH_SNAPSHOT
                      设为 1 时联网刷新 models.dev 能力快照（默认使用已提交的快照）；
                      刷新后请检查 internal/modelsdev/snapshot.json 的 diff 再提交。

注意: 当前 mygo.json 未配置 updates，产物不含自动更新包；要启用更新，需在 mygo.json 补上 updates（publicKey 加上 github 或 https url），私钥放在 ~/.config/mcode/mygo-update.key。
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --platform)
            [[ $# -ge 2 && -n "$2" ]] || { echo "错误: --platform 缺少值" >&2; exit 1; }
            PLATFORM="${2:-}"
            shift 2
            ;;
        --out)
            [[ $# -ge 2 && -n "$2" ]] || { echo "错误: --out 缺少值" >&2; exit 1; }
            OUT_DIR="${2:-}"
            shift 2
            ;;
        --skip-notarize)
            SKIP_NOTARIZE=1
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

case "$PLATFORM" in
    darwin/arm64|darwin/amd64|darwin/universal) ;;
    *) echo "错误: 不支持的平台 $PLATFORM" >&2; exit 1 ;;
esac
[[ "$(uname -s)" == Darwin ]] || { echo "错误: 打包与签名需要 macOS" >&2; exit 1; }

cd "$ROOT_DIR"

echo "==> 检查签名与凭据配置..."
SIGNING_ID="${MCODE_SIGN_IDENTITY:-}"
SIGN_ARGS=()

if [[ -n "$SIGNING_ID" && "$SIGNING_ID" != "-" ]]; then
    echo "[INFO] 使用代码签名身份: $SIGNING_ID"
    SIGN_ARGS=("-sign" "$SIGNING_ID")
else
    echo "[INFO] 未设置代码签名身份环境变量，将使用 ad hoc 签名 (-sign -)"
    SIGN_ARGS=("-sign" "-")
    # ad hoc 签名无法公证，自动跳过公证
    SKIP_NOTARIZE=1
fi

# 检查 updater 私钥
DEFAULT_KEY_FILE="$HOME/.config/mcode/mygo-update.key"
if [[ -z "${MYGO_UPDATER_PRIVATE_KEY:-}" ]] && [[ -f "$DEFAULT_KEY_FILE" ]]; then
    echo "[INFO] 从 $DEFAULT_KEY_FILE 加载 MYGO_UPDATER_PRIVATE_KEY"
    MYGO_UPDATER_PRIVATE_KEY="$(cat "$DEFAULT_KEY_FILE")"
    export MYGO_UPDATER_PRIVATE_KEY
fi

BUILD_ARGS=(
    "build"
    "-platform" "$PLATFORM"
    "-o" "$OUT_DIR"
    "${SIGN_ARGS[@]}"
)

if [[ $SKIP_NOTARIZE -eq 1 ]]; then
    BUILD_ARGS+=("-skip-notarize")
fi

# 快照由 go:embed 打进二进制：默认使用仓库里已提交的版本，只有显式要求时才联网覆盖。
if [[ "${MCODE_REFRESH_SNAPSHOT:-0}" == 1 ]]; then
    echo "==> 刷新 models.dev 能力快照..."
    "$ROOT_DIR/scripts/fetch_models_dev_snapshot.sh"
    echo "[WARN] 刷新后请检查 snapshot.json 的 diff 再提交" >&2
else
    echo "==> 使用已提交的 models.dev 能力快照（MCODE_REFRESH_SNAPSHOT=1 可刷新）"
fi

echo "==> 开始执行 go tool mygo ${BUILD_ARGS[*]}..."
go tool mygo "${BUILD_ARGS[@]}"

echo "==> 构建完成，产物输出至: $OUT_DIR"

# 只验证本次目标，避免误选其他架构遗留的产物。
TARGET_DIR="$OUT_DIR/${PLATFORM/\//-}"
APP_BUNDLE="$TARGET_DIR/MCode.app"
[[ -d "$APP_BUNDLE" ]] || { echo "错误: 未生成 $APP_BUNDLE" >&2; exit 1; }
shopt -s nullglob
DMGS=("$TARGET_DIR"/mcode-*.dmg)
[[ ${#DMGS[@]} -eq 1 ]] || { echo "错误: $TARGET_DIR 中应有且仅有一个 MCode DMG" >&2; exit 1; }
DMG="${DMGS[0]}"

echo "==> 验证 .app 代码签名: $APP_BUNDLE"
codesign --verify --deep --strict "$APP_BUNDLE"
echo "==> 验证 DMG 完整性: $DMG"
hdiutil verify "$DMG"
if [[ "$SIGNING_ID" != "-" && -n "$SIGNING_ID" ]]; then
    codesign --verify --strict "$DMG"
fi

if [[ $SKIP_NOTARIZE -eq 0 ]]; then
    xcrun stapler validate "$APP_BUNDLE"
    xcrun stapler validate "$DMG"
    spctl --assess --type execute --verbose=2 "$APP_BUNDLE"
    spctl --assess --type open --context context:primary-signature --verbose=2 "$DMG"
else
    echo "[WARN] 未公证构建仅适用于本地测试，未执行 Gatekeeper 发布验收。" >&2
fi

echo "==> 刷新 macOS bundle 元数据与 LaunchServices 注册..."
touch "$APP_BUNDLE"
if [[ -x /System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister ]]; then
    /System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$APP_BUNDLE" 2>/dev/null || true
fi

echo "==> 打包完成，产物所在目录: $OUT_DIR"

