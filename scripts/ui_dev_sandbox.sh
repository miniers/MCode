#!/bin/bash
set -euo pipefail

seed_only=false
showcase=false
theme=""
usage() { printf '用法：%s [--showcase] [--theme light|dark] [--seed-only]\n' "$0" >&2; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --seed-only)
      if $seed_only; then usage; exit 1; fi
      seed_only=true
      shift
      ;;
    --showcase)
      if $showcase; then usage; exit 1; fi
      showcase=true
      shift
      ;;
    --theme)
      if [[ -n "$theme" || $# -lt 2 ]]; then usage; exit 1; fi
      theme="$2"
      shift 2
      ;;
    *)
      usage
      exit 1
      ;;
  esac
done

root="$(cd "$(dirname "$0")/.." && pwd -P)"
if [[ -n "${MCODE_UI_SANDBOX_DIR:-}" ]]; then
  data="$MCODE_UI_SANDBOX_DIR"
else
  data="$(mktemp -d "${TMPDIR:-/tmp}/mcode-ui-sandbox.XXXXXX")"
fi

cd "$root"
# 目录归属校验与创建都由 Go 完成，这里使用它输出（已解析符号链接）的绝对路径。
seed_args=(-data "$data")
if $showcase; then seed_args+=(-showcase); fi
if [[ -n "$theme" ]]; then seed_args+=(-theme "$theme"); fi
data="$(go run ./scripts/ui_dev_seed "${seed_args[@]}")"
if $seed_only; then
  printf '%s\n' "$data"
  exit 0
fi

# 前端产物 dist 被 git 忽略，新 worktree 里没有它，主 checkout 构建过后复制过来即可。
if [[ ! -f internal/webassets/dist/remote-index.html ]]; then
  main_worktree="$(git worktree list --porcelain | awk '/^worktree /{print substr($0, 10); exit}')"
  if [[ -n "$main_worktree" && -d "$main_worktree/internal/webassets/dist" ]]; then
    mkdir -p internal/webassets/dist
    cp -R "$main_worktree/internal/webassets/dist/." internal/webassets/dist/
    printf '已从主 checkout 复制 internal/webassets/dist：%s\n' "$main_worktree" >&2
  else
    printf '缺少 internal/webassets/dist，请先在主 checkout 构建 web\n' >&2
    exit 1
  fi
fi

# 隔离主目录与工作目录，避免读取用户的 agent 配置。
module_cache="$(go env GOMODCACHE)"
build_cache="$(go env GOCACHE)"
mkdir -p "$data/home" "$data/work"
printf '警告：系统应用标识、窗口、更新检查、本机临时端口及仓库的 .mygo/dev 仍为共享资源；并存情况需要实测。\n' >&2

# go tool mygo dev 会再启动编译出的 MCode Dev，退出时要结束整棵子进程树。
terminate_tree() {
  local pid="$1" child
  for child in $(pgrep -P "$pid" 2>/dev/null || true); do
    terminate_tree "$child"
  done
  kill -TERM "$pid" 2>/dev/null || true
}

cleanup() {
  if [[ -n "${dev_pid:-}" ]]; then
    kill -TERM -- "-$dev_pid" 2>/dev/null || true
    terminate_tree "$dev_pid"
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      kill -0 -- "-$dev_pid" 2>/dev/null || break
      sleep 0.2
    done
    kill -KILL -- "-$dev_pid" 2>/dev/null || true
    kill -KILL "$dev_pid" 2>/dev/null || true
    wait "$dev_pid" 2>/dev/null || true
  fi
  printf '数据目录：%s\n' "$data" >&2
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# 后台运行并进入独立进程组，退出时由 cleanup 整组结束。
set -m
env -i PATH="$PATH" HOME="$data/home" TMPDIR="${TMPDIR:-/tmp}" \
  GOMODCACHE="$module_cache" GOCACHE="$build_cache" MCODE_DATA="$data" \
  MCODE_WORKDIR="$data/work" go tool mygo dev &
dev_pid=$!
set +m
wait "$dev_pid"
