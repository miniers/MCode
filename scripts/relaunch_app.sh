#!/bin/bash
# 在调用方退出后重新启动应用
#
#   scripts/relaunch_app.sh <应用名> <应用路径> [等待退出的 PID]
#
# 调用方可能是 MCode 应用自己（内置终端或“运行脚本”菜单）：应用退出时会向子进程发
# SIGHUP、关闭 pty，甚至杀掉整个进程组。所以本脚本先把自己 fork 成新会话（setsid）
# 脱离当前会话，再在后台依次完成：等调用方退出 -> 让应用退出 -> 等应用进程消失 -> 打开新应用。
# 日志写到 ${MCODE_RELAUNCH_LOG:-${TMPDIR:-/tmp}/mcode-relaunch.log}。
set -euo pipefail

LOG="${MCODE_RELAUNCH_LOG:-${TMPDIR:-/tmp}/mcode-relaunch.log}"
QUIT_TIMEOUT="${MCODE_RELAUNCH_QUIT_TIMEOUT:-300}"
CALLER_TIMEOUT="${MCODE_RELAUNCH_CALLER_TIMEOUT:-2}"

log() { printf '%s %s\n' "$(date '+%F %T')" "$1" >>"$LOG"; }

if [[ $# -lt 2 ]]; then
    echo "错误: 用法: $(basename "$0") <应用名> <应用路径> [等待退出的 PID]" >&2
    exit 1
fi

APP_NAME="$1"
APP_PATH="$2"
WAIT_PID="${3:-}"

if [[ ! -d "$APP_PATH" ]]; then
    echo "错误: 应用路径不是目录: $APP_PATH" >&2
    exit 1
fi

if [[ "${MCODE_RELAUNCH_DETACHED:-0}" != 1 ]]; then
    # 前台阶段只负责把工作丢到新会话：perl 自己 fork，父进程立刻退出，子进程 setsid 后
    # exec 回来继续跑后台阶段。这样当前会话（pty 或进程组）被销毁也不影响重启。
    if command -v perl >/dev/null 2>&1; then
        MCODE_RELAUNCH_DETACHED=1 perl -MPOSIX -e 'exit 0 if fork; POSIX::setsid(); exec @ARGV or die' \
            bash "$0" "$@" </dev/null >>"$LOG" 2>&1
    else
        echo "[WARN] 找不到 perl，改用 nohup 安排重启（在应用内置终端里运行可能不可靠）" >&2
        MCODE_RELAUNCH_DETACHED=1 nohup bash "$0" "$@" </dev/null >>"$LOG" 2>&1 &
        disown 2>/dev/null || true
    fi
    printf '已在后台安排重启 %s，日志: %s\n' "$APP_NAME" "$LOG"
    exit 0
fi

# 后台阶段：即使调用方终端关闭（SIGHUP）也要把重启做完。
trap '' HUP

log "开始重启 ${APP_NAME}（${APP_PATH}），后台 PID $$"

# 轮询给定 PID，最多 $1 秒；打印仍未退出的 PID（全部退出时打印空串）。
wait_gone() {
    local timeout=$1
    shift
    local i pid left
    for (( i = 0; i < timeout; i++ )); do
        left=""
        for pid in "$@"; do
            if kill -0 "$pid" 2>/dev/null; then
                left="$left $pid"
            fi
        done
        [[ -n "$left" ]] || { printf ''; return 0; }
        sleep 1
    done
    printf '%s' "${left# }"
}

# 用两个独立来源找运行中的实例。pgrep 必须带 -a：macOS 的 pgrep 默认排除自身祖先进程，
# 而调用方可能就运行在应用里；^ 锚定可执行文件路径，避免匹配到命令行里提到该路径的 shell。
# lsappinfo 查的是 LaunchServices，open 正是据此决定激活旧实例还是启动新实例。
pids_from_pgrep() { pgrep -a -f "^${APP_PATH}/Contents/MacOS/" 2>/dev/null || true; }
pids_from_lsappinfo() {
    { lsappinfo info -only pid "$APP_NAME" 2>/dev/null || true; } \
        | { grep -Eo 'pid"? ?= ?[0-9]+' || true; } \
        | { grep -Eo '[0-9]+' || true; }
}
find_app_pids() { { pids_from_pgrep; pids_from_lsappinfo; } | sort -un; }

if [[ -n "$WAIT_PID" ]]; then
    log "等待调用方进程 $WAIT_PID 退出"
    # 调用方此时只剩退出这一步，没有必要久等；这里用 bash 内置的 SECONDS 按实际经过的时间计时。
    SECONDS=0
    while kill -0 "$WAIT_PID" 2>/dev/null; do
        sleep 0.2
        if (( SECONDS >= CALLER_TIMEOUT )); then
            caller_state="$(ps -o stat=,etime=,command= -p "$WAIT_PID" 2>/dev/null || true)"
            log "调用方超过 ${CALLER_TIMEOUT} 秒仍未退出（状态: ${caller_state}），继续后续步骤"
            caller_children="$(ps -axo pid=,ppid=,stat=,command= | awk -v p="$WAIT_PID" '$2 == p' | cut -c1-200 || true)"
            log "调用方子进程: ${caller_children:-无}"
            break
        fi
    done
fi

# 找出正在运行的应用进程（排除本脚本自己）。
log "查找 ${APP_NAME}: pgrep=[$(pids_from_pgrep | tr '\n' ' ')] lsappinfo=[$(pids_from_lsappinfo | tr '\n' ' ')] 自身 PPID=$(ps -o ppid= -p $$ | tr -d ' ')"
PIDS=()
while IFS= read -r pid; do
    [[ -n "$pid" ]] || continue
    [[ "$pid" == "$$" ]] && continue
    PIDS+=("$pid")
done < <(find_app_pids)

if [[ ${#PIDS[@]} -gt 0 ]]; then
    log "请求正在运行的 $APP_NAME 退出（PID ${PIDS[*]}）"
    osascript -e 'with timeout of 300 seconds' \
        -e "tell application \"$APP_NAME\" to quit" \
        -e 'end timeout' >>"$LOG" 2>&1 || true
    LEFT="$(wait_gone "$QUIT_TIMEOUT" "${PIDS[@]}")"
    if [[ -n "$LEFT" ]]; then
        log "等待 ${QUIT_TIMEOUT} 秒后仍未退出（PID ${LEFT}），发送 SIGTERM"
        # shellcheck disable=SC2086
        kill -TERM $LEFT 2>/dev/null || true
        LEFT="$(wait_gone 10 $LEFT)"
    fi
    if [[ -n "$LEFT" ]]; then
        log "错误: 进程无法退出（PID ${LEFT}），放弃重启，否则只会激活旧实例"
        exit 1
    fi
fi

if ! open "$APP_PATH" >>"$LOG" 2>&1; then
    log "错误: 打开 $APP_PATH 失败"
    exit 1
fi
# open 在旧实例还活着时只会激活它，不会报错。新实例运行不到一分钟，etime 形如 00:03；
# 更长说明看到的是旧实例，这时不能报告成功。
while IFS= read -r pid; do
    [[ -n "$pid" ]] || continue
    etime="$(ps -o etime= -p "$pid" 2>/dev/null | tr -d ' ' || true)"
    if [[ -n "$etime" && "$etime" != 00:* ]]; then
        log "错误: ${APP_NAME} 没有重启，open 只激活了旧实例（PID ${pid}，已运行 ${etime}）"
        exit 1
    fi
done < <(find_app_pids)
log "已重新启动 $APP_NAME"
