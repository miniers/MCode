#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FAILED=0

check_pkg_forbidden() {
    local rel_dir="$1"
    shift
    local target_dir="$ROOT_DIR/$rel_dir"

    # 对尚不存在的目录跳过而不是报错
    if [[ ! -d "$target_dir" ]]; then
        return 0
    fi

    # 若目录下无 .go 文件则跳过
    if ! find "$target_dir" -type f -name "*.go" -print -quit | grep -q .; then
        return 0
    fi

    for forbidden in "$@"; do
        local matches
        matches=$(grep -rnE "(\"|')${forbidden}(/|\"|')" "$target_dir" --include="*.go" || true)
        if [[ -n "$matches" ]]; then
            echo "Error: Package '$rel_dir' violates dependency direction by importing '$forbidden':"
            echo "$matches"
            FAILED=1
        fi
    done
}

# 规则 1: internal/harness, store, git, remote, runtime 都不得 import mcode/internal/ui 和 github.com/egoist/mygo
RULE1_PKGS=(
    "internal/harness"
    "internal/store"
    "internal/git"
    "internal/kanban"
    "internal/remote"
    "internal/runtime"
    "internal/providers"
    "internal/automation"
    "internal/projctx"
    "internal/mcp"
    "internal/swarm"
)

for pkg in "${RULE1_PKGS[@]}"; do
    check_pkg_forbidden "$pkg" "mcode/internal/ui" "github.com/egoist/mygo"
done

# 规则 2: internal/ui 不得 import mcode/internal/store 与 mcode/internal/remote/wire
# 说明：顶层 mcode/internal/remote 是允许的——设置页（internal/ui/settings_remote.go:17）
# 需要 remotepkg.LANIPv4s() 这类展示逻辑，方向为 ui → remote；反方向 remote → ui
# 才由规则 1 拦截。wire 是协议层，UI 应经 Client 接口而非直接读写 JSON-RPC 包体，故继续禁止。
check_pkg_forbidden "internal/ui" "mcode/internal/store" "mcode/internal/remote/wire"

# 规则 3: internal/native 不得 import ui/remote/config
check_pkg_forbidden "internal/native" "mcode/internal/ui" "mcode/internal/remote" "mcode/internal/config"

if [[ $FAILED -ne 0 ]]; then
    echo "Dependency check failed."
    exit 1
fi

echo "Dependency check passed."
exit 0
