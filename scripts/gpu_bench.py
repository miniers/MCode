#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""MCode 窗口合成模式 GPU 占用自动对照测试脚本（仅 macOS，纯标准库）。

背景：窗口可见时 WindowServer 的 GPU 占用偏高，主聊天窗口新增了调试开关
`MCODE_DEBUG_WINDOW`（见 internal/ui/windows.go 的 debugWindowComposition）：
  - 未设置或其它值：透明 + VibrancySidebar（默认，记为 mode=default）
  - novibrancy：透明但无 vibrancy
  - opaque：不透明且无 vibrancy
本脚本对每种模式各启动一个实例，分阶段（idle / hidden / load）采样 GPU 数据，
最后汇总成 ~/gpu_bench/<YYYYmmdd-HHMMSS>/summary.md。

用法示例：
  python3 -I scripts/gpu_bench.py
  python3 -I scripts/gpu_bench.py --modes default,opaque --rounds 3
  python3 -I scripts/gpu_bench.py --cmd "go tool mygo dev" --proc-name mcode

说明：
  - 只用 Python 3 标准库，不引入任何第三方依赖，不涉及 cgo。
  - 多轮交错（同一轮内按 --modes 顺序跑完所有模式，再进下一轮），抵制冷启动/漂移。
  - 每个样本产生后立即 flush 到 CSV，Ctrl-C 中断也保留已有数据。
"""

import argparse
import csv
import math
import os
import platform
import re
import signal
import statistics
import subprocess
import sys
import time
import traceback
from datetime import datetime

# ---------------------------------------------------------------- 常量

BUNDLE_ID = "com.miniers.mcode"
APP_NAME = "MCode"
MODE_ENV = "MCODE_DEBUG_WINDOW"

BUILD_DIR = "/tmp/mcode-bench"
BENCH_BINARY = os.path.join(BUILD_DIR, "mcode-bench")
OUT_ROOT = os.path.expanduser("~/gpu_bench")

STAGES = ("idle", "hidden", "load")
STAGE_LABELS = {"idle": "idle（窗口前台空闲）", "hidden": "hidden（窗口隐藏）", "load": "load（会话运行 sleep 90）"}

# AppUsage 行形如：{"API"="Metal","lastSubmittedTime"=28615505682166,"accumulatedGPUTime"=3032294029833}
APP_USAGE_RE = re.compile(r'"lastSubmittedTime"\s*=\s*(\d+)\s*,\s*"accumulatedGPUTime"\s*=\s*(\d+)')
# IOUserClientCreator 行形如：|     "IOUserClientCreator" = "pid 422, WindowServer"
CREATOR_RE = re.compile(r'"IOUserClientCreator"\s*=\s*"pid\s+(\d+)\s*,\s*([^"]*)"')

# 负载阶段的手动操作提示（远程控制需要配对码，无法非交互拿到会话，故走交互回退路径）
LOAD_PROMPT = """
╔══════════════════════════════════════════════════════════════════════════╗
║  【需要你手动操作】负载阶段（load）开始前请在新启动的 MCode 实例里：      ║
║    1. 新建一个会话（聊天）；                                              ║
║    2. 发送一条让它执行命令的消息，例如：执行命令 sleep 90                 ║
║    3. 确认 agent 已经开始跑（界面上有正在进行中的工具调用）；             ║
║    4. 回到这个终端，按回车，脚本才开始 load 阶段的采样。                  ║
║                                                                          ║
║  说明：mcode 的远程控制需要配对码 / 设备令牌（无法非交互获得），          ║
║        所以本脚本无法自动建会话，改为手动触发一次真实 agent 负载。        ║
╚══════════════════════════════════════════════════════════════════════════╝
"""


# ---------------------------------------------------------------- 通用小工具


def log(msg):
    """带本地时间前缀打印一行进度。"""
    print("[%s] %s" % (datetime.now().strftime("%H:%M:%S"), msg), flush=True)


def run_cmd(cmd, timeout=30, cwd=None):
    """执行命令并捕获输出，失败不抛异常，返回 CompletedProcess(可能为 None)。"""
    try:
        return subprocess.run(
            cmd,
            cwd=cwd,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=timeout,
        )
    except (OSError, subprocess.SubprocessError):
        return None


def run_text(cmd, timeout=30):
    """执行命令返回 (returncode, stdout 文本)。"""
    proc = run_cmd(cmd, timeout=timeout)
    if proc is None:
        return 1, ""
    return proc.returncode, proc.stdout.decode("utf-8", "replace")


def osascript(script, timeout=20):
    """执行一段 AppleScript，返回 (ok, 输出)。"""
    code, out = run_text(["osascript", "-e", script], timeout=timeout)
    return code == 0, out.strip()


def list_processes():
    """`ps -axo pid=,comm=` -> [(pid, comm)]，comm 可能是完整路径。"""
    _, out = run_text(["ps", "-axo", "pid=,comm="], timeout=20)
    procs = []
    for line in out.splitlines():
        line = line.strip()
        if not line:
            continue
        parts = line.split(None, 1)
        if len(parts) != 2:
            continue
        try:
            pid = int(parts[0])
        except ValueError:
            continue
        procs.append((pid, parts[1].strip()))
    return procs


def comm_basename(comm):
    return os.path.basename(comm.strip()) or comm.strip()


def find_pid_by_comm(name):
    """按 comm 的 basename 精确找进程 pid（例如 WindowServer）。"""
    for pid, comm in list_processes():
        if comm_basename(comm) == name:
            return pid
    return None


def proc_alive(pid):
    if not pid:
        return False
    proc = run_cmd(["ps", "-p", str(pid)], timeout=10)
    return proc is not None and proc.returncode == 0


def pgrep_exact(name):
    code, out = run_text(["pgrep", "-x", name], timeout=10)
    if code != 0:
        return []
    pids = []
    for tok in out.split():
        try:
            pids.append(int(tok))
        except ValueError:
            pass
    return pids


# ---------------------------------------------------------------- GPU 采样


def snapshot_gpu_clients():
    """抓一次 ioreg，返回 {pid: (lastSubmittedTime 最大值, accumulatedGPUTime 之和)}。

    解析规则（已在实机验证）：每个 GPU client 块里 `"AppUsage"` 行出现在
    `"IOUserClientCreator"` 行之前，所以先把 AppUsage 里的
    (lastSubmittedTime, accumulatedGPUTime) 对暂存，遇到 IOUserClientCreator
    行时才归属到该 pid。同一 pid 可能有多个 client：accumulatedGPUTime 求和，
    lastSubmittedTime 取最大。
    """
    proc = run_cmd(["ioreg", "-r", "-c", "IOAccelerator", "-l", "-w0"], timeout=30)
    if proc is None:
        return {}
    text = proc.stdout.decode("utf-8", "replace")

    clients = {}
    pending = []  # 尚未归属的 (last, acc)
    for line in text.splitlines():
        if "IOUserClientCreator" in line:
            match = CREATOR_RE.search(line)
            if not match:
                continue
            try:
                pid = int(match.group(1))
            except ValueError:
                pending = []
                continue
            for last, acc in pending:
                cur = clients.get(pid)
                if cur is None:
                    clients[pid] = [last, acc]
                else:
                    cur[0] = max(cur[0], last)
                    cur[1] += acc
            pending = []
        elif "AppUsage" in line:
            for match in APP_USAGE_RE.finditer(line):
                pending.append((int(match.group(1)), int(match.group(2))))
    return {pid: (val[0], val[1]) for pid, val in clients.items()}


# 两次 ioreg 抓取之间的最小间隔：抓取本身约 20ms，这里限到 50ms（20Hz），
# 既保证 submit_change_pct 的分辨率，也避免脚本自己把 CPU 打满影响测量。
FETCH_MIN_PERIOD = 0.05


def gpu_pct(before, after, pid, duration):
    """区间内 GPU 占比百分数：accumulatedGPUTime 差值(纳秒) / 区间秒数 / 1e9 * 100。"""
    if not pid:
        return None
    first = before.get(pid)
    last = after.get(pid)
    if first is None or last is None:
        return None
    delta = last[1] - first[1]
    if delta < 0:  # client 重建 / 计数器重置
        delta = 0
    return delta / 1e9 / max(1e-6, duration) * 100.0


def take_sample(app_pid, ws_pid, interval):
    """在一个采样区间内高频抓 ioreg，汇总成一条样本。"""
    t0 = time.time()
    first = snapshot_gpu_clients()
    prev_last = first.get(app_pid, (None, 0))[0]
    fetches = 1
    changes = 0
    latest = first
    while True:
        elapsed = time.time() - t0
        if elapsed >= interval:
            break
        fetch_start = time.time()
        latest = snapshot_gpu_clients()
        fetches += 1
        cur_last = latest.get(app_pid, (None, 0))[0]
        if prev_last is not None and cur_last is not None and cur_last != prev_last:
            changes += 1
        prev_last = cur_last
        cost = time.time() - fetch_start
        if cost < FETCH_MIN_PERIOD:
            time.sleep(FETCH_MIN_PERIOD - cost)

    duration = max(1e-6, time.time() - t0)
    return {
        "fetch_count": fetches,
        "ws_gpu_pct": gpu_pct(first, latest, ws_pid, duration),
        "app_gpu_pct": gpu_pct(first, latest, app_pid, duration),
        "submit_change_pct": changes / float(max(1, fetches)) * 100.0,
    }


# ---------------------------------------------------------------- 窗口控制


def set_frontmost(pid):
    """把进程置为前台并确保可见、未最小化（尽力而为）。"""
    script = (
        'tell application "System Events"\n'
        "  try\n"
        "    set proc to first application process whose unix id is %d\n"
        "    set visible of proc to true\n"
        "    set frontmost of proc to true\n"
        "  end try\n"
        "end tell" % pid
    )
    osascript(script)
    osascript(
        'tell application "System Events"\n'
        "  try\n"
        '    set value of attribute "AXMinimized" of window 1 of '
        "(first application process whose unix id is %d) to false\n"
        "  end try\n"
        "end tell" % pid
    )


def set_visible(pid, visible):
    """设置进程可见性（隐藏/显示）。"""
    osascript(
        'tell application "System Events"\n'
        "  try\n"
        "    set visible of (first application process whose unix id is %d) to %s\n"
        "  end try\n"
        "end tell" % (pid, "true" if visible else "false")
    )


def frontmost_app_name():
    ok, out = osascript(
        'tell application "System Events" to get name of first application process whose frontmost is true',
        timeout=10,
    )
    return out if ok else ""


# ---------------------------------------------------------------- 进程生命周期


class Instance:
    """被测实例的句柄。"""

    def __init__(self, popen, logfile, pid, mode, log_path):
        self.popen = popen
        self.logfile = logfile
        self.pid = pid
        self.mode = mode
        self.log_path = log_path

    def terminate(self, timeout=10):
        if self.pid and proc_alive(self.pid):
            try:
                os.kill(self.pid, signal.SIGTERM)
            except OSError:
                pass
            deadline = time.time() + timeout
            while time.time() < deadline and proc_alive(self.pid):
                time.sleep(0.2)
            if proc_alive(self.pid):
                try:
                    os.kill(self.pid, signal.SIGKILL)
                except OSError:
                    pass
                time.sleep(0.3)
        # 通过 --cmd 启动时，兜底收掉整个进程组（shell / go 子进程）
        try:
            if self.popen is not None and self.popen.poll() is None:
                self.popen.terminate()
                try:
                    self.popen.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    try:
                        os.killpg(os.getpgid(self.popen.pid), signal.SIGKILL)
                    except OSError:
                        pass
        except OSError:
            pass
        try:
            if self.logfile is not None:
                self.logfile.close()
        except OSError:
            pass


def any_bench_or_app_running():
    """是否有 MCode 或基准二进制还在跑。"""
    if pgrep_exact(APP_NAME):
        return True
    code, _ = run_text(["pgrep", "-f", BENCH_BINARY], timeout=10)
    return code == 0


def quit_all_mcode():
    """退出所有 MCode 实例（含已安装版与之前的基准实例）。"""
    for target in (
        'tell application id "%s" to quit' % BUNDLE_ID,
        'tell application "%s" to quit' % APP_NAME,
    ):
        osascript(target, timeout=15)
    deadline = time.time() + 10
    while time.time() < deadline:
        if not any_bench_or_app_running():
            return
        time.sleep(0.5)
    log("仍有 MCode 在运行，改用 pkill 强制结束")
    run_text(["pkill", "-x", APP_NAME], timeout=10)
    run_text(["pkill", "-f", BENCH_BINARY], timeout=10)
    time.sleep(0.5)


def launch_instance(mode, repo_root, cmd, cmd_proc_name, log_path):
    """启动一个被测实例，返回 Instance（失败返回 None）。"""
    env = os.environ.copy()
    if mode == "default":
        env.pop(MODE_ENV, None)
    else:
        env[MODE_ENV] = mode

    logfile = open(log_path, "wb")
    if cmd:
        before = {
            pid for pid, comm in list_processes() if cmd_proc_name.lower() in comm_basename(comm).lower()
        }
        popen = subprocess.Popen(
            cmd,
            shell=True,
            cwd=repo_root,
            env=env,
            stdin=subprocess.DEVNULL,
            stdout=logfile,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        pid = wait_for_new_process(cmd_proc_name, before, 30)
        if pid is None:
            logfile.close()
            return None
        return Instance(popen, logfile, pid, mode, log_path)

    popen = subprocess.Popen(
        [BENCH_BINARY],
        cwd=repo_root,
        env=env,
        stdin=subprocess.DEVNULL,
        stdout=logfile,
        stderr=subprocess.STDOUT,
    )
    return Instance(popen, logfile, popen.pid, mode, log_path)


def wait_for_new_process(name_substr, before_pids, timeout):
    """按 comm basename 子串（不区分大小写）找最新启动的进程。"""
    needle = name_substr.lower()
    deadline = time.time() + timeout
    while time.time() < deadline:
        candidates = [
            pid
            for pid, comm in list_processes()
            if needle in comm_basename(comm).lower() and pid not in before_pids
        ]
        if candidates:
            return max(candidates)
        time.sleep(0.5)
    return None


def wait_for_gpu_client(pid, timeout=30):
    """等 pid 出现在 ioreg 的 GPU client 列表里。"""
    deadline = time.time() + timeout
    while time.time() < deadline:
        if pid in snapshot_gpu_clients():
            return True
        time.sleep(0.5)
    return False


# ---------------------------------------------------------------- 统计


def percentile(values, pct):
    """最近秩法分位数。"""
    if not values:
        return None
    ordered = sorted(values)
    idx = max(0, int(math.ceil(pct / 100.0 * len(ordered))) - 1)
    return ordered[min(idx, len(ordered) - 1)]


def describe(values):
    vals = [v for v in values if v is not None]
    if not vals:
        return None
    return {
        "n": len(vals),
        "median": statistics.median(vals),
        "mean": sum(vals) / float(len(vals)),
        "p90": percentile(vals, 90),
    }


def fmt(value, digits=2):
    if value is None:
        return "—"
    return ("%." + str(digits) + "f") % value


# ---------------------------------------------------------------- 主流程


class Bench:
    def __init__(self, args):
        self.args = args
        self.repo_root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
        self.outdir = os.path.join(OUT_ROOT, datetime.now().strftime("%Y%m%d-%H%M%S"))
        self.csv_path = os.path.join(self.outdir, "samples.csv")
        self.summary_path = os.path.join(self.outdir, "summary.md")
        self.samples = []
        self.csv_file = None
        self.csv_writer = None
        self.ws_pid = None
        self.started = None
        self.ended = None
        self.load_note = ""
        self.notes = []
        self.total_units = 0
        self.done_units = 0

    # -- 输出 ------------------------------------------------------

    def open_csv(self):
        os.makedirs(self.outdir, exist_ok=True)
        self.csv_file = open(self.csv_path, "w", newline="", encoding="utf-8")
        self.csv_writer = csv.writer(self.csv_file)
        self.csv_writer.writerow(
            ["timestamp", "round", "mode", "stage", "frontmost", "ws_gpu_pct", "app_gpu_pct", "submit_change_pct"]
        )
        self.csv_file.flush()

    def emit(self, row):
        """记录一条样本：内存 + 立即 flush 到 CSV。"""
        self.samples.append(row)
        if self.csv_writer is not None:
            self.csv_writer.writerow(
                [
                    row["timestamp"],
                    row["round"],
                    row["mode"],
                    row["stage"],
                    row["frontmost"],
                    "" if row["ws_gpu_pct"] is None else "%.4f" % row["ws_gpu_pct"],
                    "" if row["app_gpu_pct"] is None else "%.4f" % row["app_gpu_pct"],
                    "" if row["submit_change_pct"] is None else "%.2f" % row["submit_change_pct"],
                ]
            )
            self.csv_file.flush()

    # -- 参数与前置检查 --------------------------------------------

    def print_banner(self):
        per_unit = 15 + (30 + 10) + self.args.idle + self.args.hidden + self.args.load + 6
        build_cost = 60 if not self.args.cmd else 0
        total = build_cost + per_unit * len(self.args.modes) * self.args.rounds
        print("=" * 78)
        print("MCode GPU 对照测试（MCODE_DEBUG_WINDOW: default / novibrancy / opaque）")
        print("=" * 78)
        print("⚠️  警告：本脚本会退出所有正在运行的 MCode 实例（含已安装版，结束时再 open -a MCode 打开）")
        print("⚠️  期间请不要手动操作 MCode，window/前台会被脚本反复切换")
        print(
            "预计总耗时约 %d 分 %d 秒（%d 模式 × %d 轮；含构建/启动/三个阶段/清理）"
            % (total // 60, total % 60, len(self.args.modes), self.args.rounds)
        )
        print(
            "参数：modes=%s rounds=%d idle=%ds hidden=%ds load=%ds step=%gs warmup=%gs"
            % (
                ",".join(self.args.modes),
                self.args.rounds,
                self.args.idle,
                self.args.hidden,
                self.args.load,
                self.args.step,
                self.args.warmup,
            )
        )
        print("输出目录：%s" % self.outdir)
        if self.args.cmd:
            print("启动方式：--cmd `%s`（不执行 go build，进程名匹配 `%s`）" % (self.args.cmd, self.args.proc_name))
        else:
            print("启动方式：go build -o %s ./cmd/mcode 后直接运行该二进制" % BENCH_BINARY)
        print("=" * 78, flush=True)
        self.total_units = len(self.args.modes) * self.args.rounds

    def build_once(self):
        """默认路径：在仓库根目录构建一次基准二进制。"""
        if self.args.cmd:
            log("使用 --cmd，跳过 go build：%s" % self.args.cmd)
            return True
        os.makedirs(BUILD_DIR, exist_ok=True)
        log("构建基准二进制：go build -o %s ./cmd/mcode（在 %s）" % (BENCH_BINARY, self.repo_root))
        proc = run_cmd(["go", "build", "-o", BENCH_BINARY, "./cmd/mcode"], timeout=600, cwd=self.repo_root)
        if proc is None or proc.returncode != 0:
            err = "" if proc is None else proc.stderr.decode("utf-8", "replace")
            log("构建失败：\n%s" % err.strip())
            return False
        log("构建完成：%s" % BENCH_BINARY)
        return True

    # -- 单次 (轮, 模式) ------------------------------------------

    def run_stage(self, mode, round_idx, stage, duration, app_pid):
        t_stage = time.time()
        t_end = t_stage + duration
        kept = 0
        total = 0
        while time.time() < t_end:
            remaining = t_end - time.time()
            interval = min(self.args.step, max(0.5, remaining))
            t_sample = time.time()
            measured = take_sample(app_pid, self.ws_pid, interval)
            elapsed = t_sample - t_stage
            total += 1
            row = {
                "timestamp": datetime.now().strftime("%Y-%m-%d %H:%M:%S.%f")[:-3],
                "round": round_idx,
                "mode": mode,
                "stage": stage,
                "frontmost": frontmost_app_name(),
                "ws_gpu_pct": measured["ws_gpu_pct"],
                "app_gpu_pct": measured["app_gpu_pct"],
                "submit_change_pct": measured["submit_change_pct"],
            }
            if elapsed >= self.args.warmup:
                kept += 1
                self.emit(row)
            # 补齐到下一个采样点
            spent = time.time() - t_sample
            if spent < interval:
                time.sleep(interval - spent)
        log(
            "  阶段 %s 完成：采集 %d 个区间，保留 %d 条（丢弃前 %gs 预热）"
            % (STAGE_LABELS.get(stage, stage), total, kept, self.args.warmup)
        )

    def prompt_load(self, round_idx, mode):
        print(LOAD_PROMPT, flush=True)
        print(">>> 第 %d 轮 / 模式 %s：请在 MCode 里新建会话并让它执行 sleep 90" % (round_idx, mode))
        try:
            input(">>> 确认 agent 已经开始运行后，按回车开始 load 阶段采样：")
        except EOFError:
            log("标准输入不可用，直接开始 load 阶段")
        print(flush=True)

    def run_mode(self, round_idx, mode):
        """跑完一个 (轮, 模式)：清场 -> 启动 -> 前台 -> 三阶段 -> 结束。"""
        self.done_units += 1
        head = "[%d/%d] 第 %d 轮 · 模式 %s" % (self.done_units, self.total_units, round_idx, mode)
        log("%s：退出所有 MCode 实例" % head)
        quit_all_mcode()

        log("%s：启动被测实例（%s=%s）" % (head, MODE_ENV, mode))
        log_path = os.path.join(self.outdir, "app-r%d-%s.log" % (round_idx, mode))
        inst = launch_instance(mode, self.repo_root, self.args.cmd, self.args.proc_name, log_path)
        if inst is None:
            log("%s：启动失败，跳过该模式" % head)
            self.notes.append("第 %d 轮 %s：启动失败（未找到进程），跳过" % (round_idx, mode))
            return
        app_pid = inst.pid
        try:
            log("%s：等待 pid %d 出现在 GPU client 列表" % (head, app_pid))
            if not wait_for_gpu_client(app_pid, timeout=30):
                log("%s：30 秒内未在 ioreg 里看到 pid %d，跳过该模式" % (head, app_pid))
                self.notes.append("第 %d 轮 %s：pid %d 未出现在 ioreg GPU client 列表，跳过" % (round_idx, mode, app_pid))
                return
            log("%s：已上 GPU 客户端，稳定等待 10 秒" % head)
            time.sleep(10)

            set_frontmost(app_pid)
            time.sleep(1)
            if not frontmost_app_name():
                self.notes.append(
                    "osascript 取不到前台应用名：请在「系统设置 > 隐私与安全性 > 辅助功能」里给运行本脚本的终端授权，"
                    "否则窗口置前台/隐藏可能无效（前台应用名会记为空）"
                )

            self.run_stage(mode, round_idx, "idle", self.args.idle, app_pid)

            log("%s：隐藏窗口（visible=false）" % head)
            set_visible(app_pid, False)
            try:
                self.run_stage(mode, round_idx, "hidden", self.args.hidden, app_pid)
            finally:
                set_visible(app_pid, True)
                set_frontmost(app_pid)
                time.sleep(0.5)

            if self.args.load > 0:
                self.prompt_load(round_idx, mode)
                set_frontmost(app_pid)
                self.run_stage(mode, round_idx, "load", self.args.load, app_pid)
        finally:
            log("%s：结束被测实例（pid %s）" % (head, app_pid))
            inst.terminate()

    # -- 汇总 ------------------------------------------------------

    def group(self, mode, stage, field, round_idx=None):
        return [
            s[field]
            for s in self.samples
            if s["mode"] == mode
            and s["stage"] == stage
            and (round_idx is None or s["round"] == round_idx)
        ]

    def build_summary(self):
        args = self.args
        lines = []
        lines.append("# MCode GPU 对照测试报告")
        lines.append("")
        lines.append("## 运行参数")
        lines.append("")
        lines.append("| 项目 | 值 |")
        lines.append("| --- | --- |")
        lines.append("| 开始时间 | %s |" % (datetime.fromtimestamp(self.started).strftime("%Y-%m-%d %H:%M:%S") if self.started else "—"))
        lines.append("| 结束时间 | %s |" % (datetime.fromtimestamp(self.ended).strftime("%Y-%m-%d %H:%M:%S") if self.ended else "—"))
        lines.append("| 模式 | %s |" % ", ".join(args.modes))
        lines.append("| 轮数 | %d |" % args.rounds)
        lines.append("| 阶段时长 | idle %ds / hidden %ds / load %ds |" % (args.idle, args.hidden, args.load))
        lines.append("| 采样间隔 / 预热丢弃 | %gs / 前 %gs |" % (args.step, args.warmup))
        lines.append("| 启动方式 | %s |" % (("--cmd `%s`（进程名匹配 `%s`）" % (args.cmd, args.proc_name)) if args.cmd else "go build -o %s ./cmd/mcode" % BENCH_BINARY))
        lines.append("| 仓库根目录 | %s |" % self.repo_root)
        lines.append("| WindowServer pid | %s |" % self.ws_pid)
        lines.append("| 样本总数 | %d |" % len(self.samples))
        lines.append("| 实例日志 | `%s`（app-r<轮>-<模式>.log） |" % os.path.join(self.outdir, "app-r1-default.log"))
        lines.append("| load 阶段负载方式 | %s |" % (self.load_note or "（本次未执行 load 阶段）"))
        lines.append("")
        # 数据质量提示：hidden 阶段前台应用名里仍出现被测进程名，说明隐藏可能没生效
        hidden_rows = [s for s in self.samples if s["stage"] == "hidden"]
        hidden_note = None
        if hidden_rows:
            needle = (args.proc_name if args.cmd else "mcode").lower()
            still = sum(1 for s in hidden_rows if needle in (s["frontmost"] or "").lower())
            if still * 2 >= len(hidden_rows):
                hidden_note = (
                    "hidden 阶段有 %d/%d 条样本的前台应用名仍是被测应用，隐藏窗口可能未生效"
                    "（先确认「系统设置 > 隐私与安全性 > 辅助功能」已给终端授权）" % (still, len(hidden_rows))
                )
        if hidden_note:
            notes = list(self.notes) + [hidden_note]
        else:
            notes = list(self.notes)
        if notes:
            lines.append("运行备注：")
            for note in notes:
                lines.append("- %s" % note)
            lines.append("")

        lines.append("## 按 (模式 × 阶段) 汇总（多轮合并）")
        lines.append("")
        lines.append("| 模式 | 阶段 | 样本数 | ws 中位数 | ws 均值 | ws p90 | app 中位数 | submit 变化率中位数 |")
        lines.append("| --- | --- | --- | --- | --- | --- | --- | --- |")
        summary = {}
        for mode in args.modes:
            for stage in STAGES:
                ws = describe(self.group(mode, stage, "ws_gpu_pct"))
                app = describe(self.group(mode, stage, "app_gpu_pct"))
                sub = describe(self.group(mode, stage, "submit_change_pct"))
                summary[(mode, stage)] = ws
                lines.append(
                    "| %s | %s | %s | %s | %s | %s | %s | %s |"
                    % (
                        mode,
                        stage,
                        ws["n"] if ws else 0,
                        fmt(ws["median"]) if ws else "—",
                        fmt(ws["mean"]) if ws else "—",
                        fmt(ws["p90"]) if ws else "—",
                        fmt(app["median"]) if app else "—",
                        fmt(sub["median"]) if sub else "—",
                    )
                )
        lines.append("")

        lines.append("## 每一轮的 ws_gpu_pct 中位数（看漂移）")
        lines.append("")
        header = "| 轮 | 模式 |"
        sep = "| --- | --- |"
        for stage in STAGES:
            header += " %s |" % stage
            sep += " --- |"
        header += " 全部阶段 |"
        sep += " --- |"
        lines.append(header)
        lines.append(sep)
        for round_idx in range(1, args.rounds + 1):
            for mode in args.modes:
                cells = []
                for stage in STAGES:
                    stat = describe(self.group(mode, stage, "ws_gpu_pct", round_idx))
                    cells.append(fmt(stat["median"]) if stat else "—")
                allstat = describe([s["ws_gpu_pct"] for s in self.samples if s["mode"] == mode and s["round"] == round_idx])
                lines.append("| %d | %s | %s | %s |" % (round_idx, mode, " | ".join(cells), fmt(allstat["median"]) if allstat else "—"))
        lines.append("")

        lines.append("## 相对 default 的 ws_gpu_pct 中位数差值")
        lines.append("")
        if "default" not in args.modes:
            lines.append("本次未包含 default 模式，无基准可比。")
        else:
            lines.append("| 阶段 | 模式 | default 中位数 | 差值（绝对） | 差值（相对） |")
            lines.append("| --- | --- | --- | --- | --- |")
            for stage in STAGES:
                base = summary.get(("default", stage))
                base_median = base["median"] if base else None
                for mode in args.modes:
                    if mode == "default":
                        continue  # 基准行不重复列出
                    cur = summary.get((mode, stage))
                    cur_median = cur["median"] if cur else None
                    if base_median is None or cur_median is None:
                        lines.append("| %s | %s | %s | — | — |" % (stage, mode, fmt(base_median)))
                        continue
                    delta = cur_median - base_median
                    rel = (delta / base_median * 100.0) if base_median else None
                    lines.append(
                        "| %s | %s | %s | %+.2f | %s |"
                        % (stage, mode, fmt(base_median), delta, "—" if rel is None else "%+.1f%%" % rel)
                    )
        lines.append("")
        lines.append("指标说明：ws_gpu_pct / app_gpu_pct = 区间内 accumulatedGPUTime 增量 ÷ 区间秒数 × 100（纳秒换算），")
        lines.append("submit_change_pct = 采样区间内被测进程 lastSubmittedTime 发生变化次数 ÷ ioreg 抓取次数 × 100，作为出帧率代理。")
        lines.append("")
        return "\n".join(lines)

    def write_summary(self):
        self.ended = time.time()
        text = self.build_summary()
        try:
            with open(self.summary_path, "w", encoding="utf-8") as f:
                f.write(text)
        except OSError as exc:
            log("写入 summary.md 失败：%s" % exc)
        if self.csv_file is not None:
            try:
                self.csv_file.close()
            except OSError:
                pass
        return text


def parse_args(argv):
    parser = argparse.ArgumentParser(
        description="MCode 窗口合成模式（MCODE_DEBUG_WINDOW）下的 GPU 占用自动对照测试（仅 macOS）",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--modes", default="default,novibrancy,opaque", help="逗号分隔的合成模式列表")
    parser.add_argument("--rounds", type=int, default=2, help="轮数（轮内按 modes 顺序，多轮交错以抵消漂移）")
    parser.add_argument("--idle", type=int, default=30, help="idle 阶段秒数")
    parser.add_argument("--hidden", type=int, default=15, help="hidden 阶段秒数")
    parser.add_argument("--load", type=int, default=60, help="load 阶段秒数")
    parser.add_argument("--step", type=float, default=5.0, help="采样间隔秒数")
    parser.add_argument("--warmup", type=float, default=5.0, help="每个阶段开头丢弃的秒数")
    parser.add_argument("--cmd", default="", help="覆盖启动命令（在仓库根目录用 shell 执行，例如 'go tool mygo dev'）")
    parser.add_argument("--proc-name", default="mcode", help="配合 --cmd 时用于找进程的进程名子串")
    args = parser.parse_args(argv)

    modes = [m.strip() for m in args.modes.split(",") if m.strip()]
    if not modes:
        parser.error("--modes 不能为空")
    # 保持用户给定的顺序（只去重），因为轮内顺序影响漂移交错的方式
    deduped = []
    for mode in modes:
        if mode not in deduped:
            deduped.append(mode)
    args.modes = deduped
    # mcode 里 debugWindowComposition 对不认识的取值会静默回退到 default，这里提前提醒
    known = {"default", "novibrancy", "opaque"}
    unknown = [m for m in deduped if m not in known]
    if unknown:
        print(
            "⚠️  警告：模式 %s 不是 mcode 认识的取值（只认 default/novibrancy/opaque），"
            "会被静默当作 default，结果会与 default 重复。" % ",".join(unknown)
        )
    args.rounds = max(1, args.rounds)
    return args


def main(argv):
    if platform.system() != "Darwin":
        print("本脚本只支持 macOS（当前系统：%s）。" % platform.system(), file=sys.stderr)
        return 1

    args = parse_args(argv)
    bench = Bench(args)
    bench.print_banner()
    bench.started = time.time()

    try:
        bench.open_csv()
        if not bench.build_once():
            log("构建失败，退出。")
            return 1

        bench.ws_pid = find_pid_by_comm("WindowServer")
        if bench.ws_pid is None:
            log("找不到 WindowServer 进程，退出。")
            return 1
        log("WindowServer pid = %d" % bench.ws_pid)

        bench.load_note = (
            "（未执行 load 阶段）"
            if args.load <= 0
            else (
                "交互方式：每个 (轮, 模式) 启动新实例后，由用户在终端按提示新建会话并让它执行 sleep 90，"
                "按回车后才开始 load 阶段采样。原因：mcode 远程控制（internal/remote）默认未开启，"
                "且 session.pair 必须先拿到配对码/设备令牌才能建会话，无法非交互获得，故未走 JSON-RPC 自动建会话。"
            )
        )

        for round_idx in range(1, args.rounds + 1):
            for mode in args.modes:
                bench.run_mode(round_idx, mode)
    except KeyboardInterrupt:
        log("收到 Ctrl-C，保留已采集数据并生成报告")
        bench.notes.append("运行被 Ctrl-C 中断，报告只包含已完成的样本")
    except Exception:  # noqa: BLE001 - 兜底保证清理与报告
        log("运行出现异常：\n%s" % traceback.format_exc())
        bench.notes.append("运行出现异常，详见终端输出")
    finally:
        try:
            quit_all_mcode()
        except Exception:
            pass
        try:
            run_text(["open", "-a", APP_NAME], timeout=30)
            log("已尝试重新打开已安装版 MCode（open -a %s）" % APP_NAME)
        except Exception:
            pass
        text = bench.write_summary()
        print("\n" + "=" * 78)
        print(text)
        print("=" * 78)
        print("样本 CSV：%s" % bench.csv_path)
        print("汇总报告：%s" % bench.summary_path)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
