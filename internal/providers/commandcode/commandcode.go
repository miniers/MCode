// Package commandcode 实现了 Command Code CLI（可执行文件 `cmd`）的 harness.Harness 适配器。
//
// 协议契约：
//   - CLI 没有常驻服务协议：每个 turn 启动一个 `cmd -p` 进程，prompt 写到 stdin 后关闭，
//     stdout 是按行分隔的 JSON 帧（顶帧 type 为 event 或 result），一轮以 result 帧和
//     进程退出结束。
//   - 第一个 turn 的 run_start（或 result）给出 CLI 会话 id，之后的 turn 用
//     `--resume <id>` 恢复；Harness 按 turn.SessionID 记住 CLI 报出的新 id 供下一轮使用，
//     同时以 EventSession 把 id 交给调用方。
//   - headless 运行不能提问，CLI 以 --yolo 放行，可变更工具的调用由内嵌的审批 mod
//     （approval.go，经 --mod 加载）落成审批目录里的请求文件，gate 轮询目录、按权限模式
//     自动放行，或经 harness.Approver/Interactor 询问后把回复文件写回去。
//   - 退出码 130 表示运行被中止（被 SIGTERM 结束），按用户中止处理，不当作错误。
//   - MCP：内置与外部服务经 MCODE_MCP_SERVERS 交给内嵌的桥接 mod（mcp.go/
//     mcp_bridge.ts，额外的 --mod），由它连服务并注册 mcp__<server>__<tool> 工具。
//
// 尚未实现的行为（清单见 README.md）：--list-models 与 cmd status、
// session_titled / compaction / notice / planCompleted 等展示类事件、final report 的
// --max-turns 1、转录存在性检查、内置 Kanban。
package commandcode

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"mcode/internal/harness"
	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

var (
	_ harness.Harness           = (*Harness)(nil)
	_ harness.TurnStopper       = (*Harness)(nil)
	_ harness.DeliveryConfirmer = (*Harness)(nil)
)

// DefaultBinary 是 Command Code CLI 的默认可执行文件名。
const DefaultBinary = "cmd"

// maxTurns 是一轮 headless 运行最多往返多少回；CLI 自己的默认值是 100，
// 对长任务偏短。
const maxTurns = 400

// Options 包含 Command Code 适配器的配置选项。
type Options struct {
	Binary    string
	ExtraArgs []string
	// Env 追加到子进程环境，排在最前，turn.Env 里的同名变量优先级更高。
	Env []string
	// Sandbox 是默认沙箱；turn.Sandbox 为空时使用。
	Sandbox harness.Sandbox
	// BuiltinMCP 是 mcode 自己的 MCP 服务，ExternalMCP 按工作目录返回用户外部接入的服务。
	// CLI 没有原生的 MCP 配置，两者都经 MCODE_MCP_SERVERS 交给内嵌的桥接 mod
	// （mcp_bridge.ts，以额外的 --mod 加载）。
	BuiltinMCP  harness.BuiltinMCP
	ExternalMCP func(workDir, threadID string) []harness.MCPEndpoint
}

// Harness 实现了基于 Command Code headless print 模式的 harness.Harness 接口。
type Harness struct {
	opts Options

	// mu 保护 runs、sessions 与 grants。
	mu   sync.Mutex
	runs []*activeRun
	// roots 按会话记录正在运行的 agent 进程 PID，供 ProcessRootPIDs 使用。
	roots harness.ProcessRoots
	// sessions 按调用方给的 turn.SessionID 记录 CLI 报出的最新会话 id，
	// 让下一轮能 resume 到 CLI 实际的会话。
	sessions map[string]string
	// grants 是会话级审批放行（"Approve for session"），键为会话 id，值为放行的请求种类。
	grants map[string]map[string]bool
}

// activeRun 是一轮正在执行的 cmd 进程，供 ForceStopTurn 定位。
type activeRun struct {
	turnID string
	cmd    *exec.Cmd
	// done 在 Run 收尾完毕时关闭，供 ForceStopTurn 的兜底强杀判断进程是否仍在。
	done chan struct{}
}

// New 创建一个新的 Command Code Harness 实例。
func New(opts Options) *Harness {
	return &Harness{opts: opts}
}

// Kind 返回后端的种类标识。
func (h *Harness) Kind() harness.Kind {
	return harness.Kind("commandcode")
}

// CatalogBinary 返回当前运行时配置的 CLI。
func (h *Harness) CatalogBinary() string { return h.opts.Binary }

// ProcessRootPIDs 返回指定会话当前运行的 agent 进程 PID，未在运行时返回 nil。
func (h *Harness) ProcessRootPIDs(threadID string) []int {
	return h.roots.PIDs(threadID)
}

// ConfirmsApprovalDelivery 返回 true，表示该 Provider 对每次 Approve/Interact 的答复都会调用
// DeliveryAcker.AckDelivery 回报写入结果。
func (h *Harness) ConfirmsApprovalDelivery() bool {
	return true
}

// buildEnv 构造子进程环境变量列表：
// 1. 从 base 中剔除所有以 "PWD=" 开头的项，避免原样继承父进程的环境变量 PWD；
// 2. 当 workDir 非空时追加 "PWD=" + workDir，保证子进程 PWD 与工作目录一致；
// 3. 最后追加 extra。extra 中自带的同名变量因排在末尾而优先。
func buildEnv(base []string, workDir string, extra []string) []string {
	env := make([]string, 0, len(base)+1+len(extra))
	for _, entry := range base {
		if !strings.HasPrefix(entry, "PWD=") {
			env = append(env, entry)
		}
	}
	if workDir != "" {
		env = append(env, "PWD="+workDir)
	}
	env = append(env, extra...)
	return env
}

// tailBuffer 是一个保留末尾至多指定字节大小的缓冲区，用于错误输出。
type tailBuffer struct {
	limit int
	buf   []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.limit {
		b.buf = b.buf[len(b.buf)-b.limit:]
	}
	return n, nil
}

func (b *tailBuffer) String() string {
	return string(b.buf)
}

// commandcodeEfforts 是 CLI 自己（cmd 1.53.1）的模型 reasoning effort 表
// （逐项固定）：表里没有的模型不接受可调 effort，命令行上带 --effort
// 会让 CLI 直接拒绝整次运行（"LongCat 2.0 has no adjustable reasoning effort"，exit 1）。
func commandcodeEfforts(model string) []string {
	full := []string{"low", "medium", "high", "xhigh", "max"}
	toXHigh := []string{"low", "medium", "high", "xhigh"}
	toHigh := []string{"low", "medium", "high"}
	highMax := []string{"high", "max"}
	lowHighMax := []string{"low", "high", "max"}
	switch model {
	case "claude-sonnet-5", "claude-sonnet-4-6", "claude-fable-5-1", "claude-fable-5",
		"claude-opus-5-5", "claude-opus-5", "claude-opus-4-8", "claude-opus-4-7",
		"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
		"meta/muse-spark-1.3":
		return full
	case "gpt-5.5", "gpt-5.4", "gpt-5.3-codex",
		"xai/grok-4.6",
		"meta/muse-spark-1.1", "meta/muse-spark-1.2", "meta/muse-spark-1.2-contributor",
		"meta/muse-spark-1.3-contributor":
		return toXHigh
	case "gpt-5.4-mini",
		"google/gemini-3.8-flash", "google/gemini-3.7-flash", "google/gemini-3.6-flash",
		"google/gemini-3.5-flash", "google/gemini-3.5-flash-lite", "google/gemini-3.1-flash-lite",
		"tencent/hy4-preview", "xai/grok-4.5",
		"minimaxai/minimax-m3", "minimax/minimax-m3-free":
		return toHigh
	case "deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-flash",
		"deepseek/deepseek-v4-flash-vision-exp", "zai-org/glm-5.2":
		return highMax
	case "deepseek/deepseek-v4-flash-fast", "deepseek/deepseek-v4.1-flash",
		"moonshotai/kimi-k3", "zai-org/glm-5.3", "z-ai/glm-5.3-flash":
		return lowHighMax
	case "sakana/fugu-ultra":
		return []string{"high", "xhigh"}
	case "qwen/qwen3.8-max-0902", "qwen/qwen3.8-max", "qwen/qwen3.8-27b", "qwen/qwen3.8-flash":
		return []string{"low", "medium", "xhigh"}
	default:
		return nil
	}
}

// takesEffort 判断模型是否接受该 effort；模型为空或不在表里时一律为 false。
func takesEffort(effort, model string) bool {
	if model == "" {
		return false
	}
	for _, candidate := range commandcodeEfforts(strings.ToLower(model)) {
		if candidate == effort {
			return true
		}
	}
	return false
}

// buildArgs 组装一次 `cmd -p` 运行的命令行参数。
//
// sessionID 是解析后的 CLI 会话 id（空表示新会话），modPath 是审批 mod 路径
// （空表示没有门禁），mcpModPath 是 MCP 桥 mod 路径（空表示没有 MCP）；
// 三者都不含默认值逻辑，纯函数便于测试。
func buildArgs(opts Options, turn harness.Turn, sessionID, modPath, mcpModPath string) []string {
	args := []string{
		"-p",
		"--output-format", "json",
		"--skip-onboarding",
		"--no-auto-update",
		"--trust",
		"--tools-enable", "todo_write",
		"--max-turns", strconv.Itoa(maxTurns),
	}
	// "default" 是模型目录的占位行，交给 CLI 自己的默认模型，不传 --model。
	if turn.Model != "" && turn.Model != "default" {
		args = append(args, "--model", turn.Model)
	}
	if turn.Effort != "" && takesEffort(turn.Effort, turn.Model) {
		args = append(args, "--effort", turn.Effort)
	}
	if turn.InteractionMode == "plan" {
		// 计划模式用 CLI 自己的只读模式，最终文本即计划。
		args = append(args, "--permission-mode", "plan")
	} else {
		// print 模式默认压制写改与命令，门禁（mod）才是询问用户的地方。
		args = append(args, "--yolo")
	}
	// 完全访问不加载 gate；计划模式与其余模式都要 mod。
	if modPath != "" && turn.Mode != harness.ModeFullAccess {
		args = append(args, "--mod", modPath)
	}
	// MCP 桥与权限模式无关，任何模式下都加载（--mod 可重复）。
	if mcpModPath != "" {
		args = append(args, "--mod", mcpModPath)
	}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	// 工作目录之外的附加目录（总管会话的已注册项目）逐个放开。
	seenDirs := make(map[string]bool, len(turn.AdditionalDirs))
	for _, dir := range turn.AdditionalDirs {
		if dir == "" || dir == turn.WorkDir || seenDirs[dir] {
			continue
		}
		seenDirs[dir] = true
		args = append(args, "--add-dir", dir)
	}
	return append(args, opts.ExtraArgs...)
}

// promptText 是写到 stdin 的 prompt：用户的正文，外加存在的附件路径列表，
// 供模型用 read_file 读取。
func promptText(turn harness.Turn) string {
	text := turn.Prompt
	var files []string
	for _, path := range turn.Images {
		if _, err := os.Stat(path); err == nil {
			files = append(files, path)
		}
	}
	if len(files) > 0 {
		text += "\n\nAttached files (read them with read_file):\n"
		for _, path := range files {
			text += "- " + path + "\n"
		}
		text = strings.TrimSuffix(text, "\n")
	}
	return text
}

// Run 执行一轮 Command Code 交互：启动 `cmd -p`，把 prompt 写到 stdin 后关闭，
// 把 stdout 上的 JSON 帧翻译成 harness 事件；进程退出后按 result 帧或退出码收尾。
// ctx 取消时按进程组发送 SIGTERM（CLI 以 130 退出，不算错误），并返回 ctx 的错误。
func (h *Harness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	if strings.TrimSpace(turn.Prompt) == "" && len(turn.Images) == 0 {
		return fmt.Errorf("commandcode: prompt is required")
	}

	binary := h.opts.Binary
	if binary == "" {
		binary = DefaultBinary
	}

	sessionID := h.resolveSession(turn.SessionID)

	// 审批门禁：完全访问不加载 mod；其余模式把 mod 与本次运行的审批目录写进临时目录，
	// 运行结束后整体清理。
	var gateState *gate
	modPath := ""
	if turn.Mode != harness.ModeFullAccess {
		dir, err := os.MkdirTemp("", "mcode-commandcode-")
		if err != nil {
			return fmt.Errorf("commandcode: 创建审批临时目录: %w", err)
		}
		defer os.RemoveAll(dir)
		modPath = filepath.Join(dir, approvalModFile)
		if err := os.WriteFile(modPath, []byte(approvalMod), 0o644); err != nil {
			return fmt.Errorf("commandcode: 写入审批 mod: %w", err)
		}
		gateState = newGate(filepath.Join(dir, "approvals"), turn, h.grantsFor(turn.SessionID))
		gateState.grantsLock = &h.mu
		if err := os.MkdirAll(gateState.dir, 0o700); err != nil {
			return fmt.Errorf("commandcode: 创建审批目录: %w", err)
		}
	}

	// MCP：与门禁无关，有服务时把清单写进环境变量并额外加载桥接 mod，
	// 没有服务时两者都不做。
	mcpEnv, mcpEnabled := mcpServersEnvValue(turn, h.opts.BuiltinMCP, h.opts.ExternalMCP)
	mcpModPath := ""
	if mcpEnabled {
		dir, err := os.MkdirTemp("", "mcode-commandcode-mcp-")
		if err != nil {
			return fmt.Errorf("commandcode: 创建 MCP 桥临时目录: %w", err)
		}
		defer os.RemoveAll(dir)
		if mcpModPath, err = installMCPBridge(dir); err != nil {
			return fmt.Errorf("commandcode: 写入 MCP 桥: %w", err)
		}
	}

	args := buildArgs(h.opts, turn, sessionID, modPath, mcpModPath)

	sandbox := turn.Sandbox
	if sandbox == nil {
		sandbox = h.opts.Sandbox
	}

	var cmd *exec.Cmd
	var cleanup func()
	var err error
	if sandbox != nil {
		cmd, cleanup, err = sandbox.Command(ctx, turn.WorkDir, turn.Mode, binary, args...)
		if err != nil {
			return fmt.Errorf("commandcode: sandbox command: %w", err)
		}
	} else {
		cmd = procattr.HideWindow(exec.CommandContext(ctx, binary, args...))
	}
	if cleanup != nil {
		defer cleanup()
	}

	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		return killProcessGroup(cmd, syscall.SIGTERM)
	}
	cmd.WaitDelay = 3 * time.Second

	baseEnv := loginenv.Environ()
	if cmd.Env != nil {
		baseEnv = cmd.Env
	}
	cmd.Dir = turn.WorkDir
	extra := append(append([]string{}, h.opts.Env...), turn.Env...)
	// 完全访问时给空值，避免从父进程继承到别的运行的审批目录（mod 见到空值即不注册）。
	approvalsDir := ""
	if gateState != nil {
		approvalsDir = gateState.dir
	}
	extra = append(extra, approvalsEnv+"="+approvalsDir)
	if mcpEnabled {
		extra = append(extra, mcpServersEnv+"="+mcpEnv)
	}
	cmd.Env = buildEnv(baseEnv, turn.WorkDir, extra)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("commandcode: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("commandcode: stdout pipe: %w", err)
	}
	stderrTail := &tailBuffer{limit: 8192}
	cmd.Stderr = stderrTail

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("commandcode: start: %w", err)
	}
	// 按会话记录 agent 进程 PID，供「终止运行中的命令」定位命令子进程树。
	agentPID := cmd.Process.Pid
	h.roots.Set(turn.ThreadID, agentPID)
	defer h.roots.Clear(turn.ThreadID, agentPID)

	run := &activeRun{turnID: turn.ID, cmd: cmd, done: make(chan struct{})}
	h.registerRun(run)
	defer h.unregisterRun(run)
	defer close(run.done)

	// 正常路径与提前返回都走同一套回收：关 stdin、杀掉进程组、回收进程。
	defer func() {
		_ = stdin.Close()
		_ = killProcessGroup(cmd, syscall.SIGKILL)
		// 正常路径已经 Wait 过，重复调用只会返回错误，忽略即可。
		_ = cmd.Wait()
	}()

	if _, err := io.WriteString(stdin, promptText(turn)); err != nil {
		return fmt.Errorf("commandcode: 写入 prompt: %w", err)
	}
	if err := stdin.Close(); err != nil {
		return fmt.Errorf("commandcode: 关闭 stdin: %w", err)
	}

	gateCtx, cancelGate := context.WithCancel(ctx)
	defer cancelGate()
	if gateState != nil {
		go gateState.poll(gateCtx)
	}

	state := newStreamState(sessionID)
	reader := bufio.NewReader(stdout)
	for {
		rawLine, readErr := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(rawLine); len(trimmed) > 0 {
			if frame, parseErr := parseFrame(trimmed); parseErr == nil && frame != nil {
				state.handle(frame, emit)
				if id := state.sessionID; id != "" && id != sessionID {
					// CLI 报出了新的会话 id：记住映射，让下一轮按旧 id 也能 resume 到它，
					// 并让本会话已放行的审批跟着新 id 继续生效。
					h.rememberSession(turn.SessionID, id)
					if turn.SessionID == "" && gateState != nil {
						h.adoptGrants(id, gateState.grants)
					} else {
						h.grantsFollow(turn.SessionID, id)
					}
					sessionID = id
				}
			}
		}
		if readErr != nil {
			break
		}
	}

	if ctx.Err() != nil {
		_ = killProcessGroup(cmd, syscall.SIGTERM)
		_ = waitWithKill(cmd, 3*time.Second)
		return ctx.Err()
	}

	waitErr := cmd.Wait()
	state.finishRun(exitStatus(waitErr), killedByStopSignal(waitErr), stderrTail.String(), emit)
	return nil
}

func waitWithKill(cmd *exec.Cmd, grace time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		_ = killProcessGroup(cmd, syscall.SIGKILL)
		return <-done
	}
}

// ForceStopTurn 强停指定轮次的 cmd 进程组，不影响同一 Harness 的其他会话。
// 先发 SIGTERM（CLI 会以 130 退出，本轮按中止收尾），3 秒后进程仍在再 SIGKILL 兜底。
func (h *Harness) ForceStopTurn(turnID string) {
	run := h.findRun(func(r *activeRun) bool { return r.turnID == turnID })
	if run == nil {
		return
	}
	_ = killProcessGroup(run.cmd, syscall.SIGTERM)
	go func() {
		select {
		case <-run.done:
		case <-time.After(3 * time.Second):
			_ = killProcessGroup(run.cmd, syscall.SIGKILL)
		}
	}()
}

// resolveSession 把调用方给的 turn.SessionID 解析成本轮真正要 resume 的 CLI 会话 id：
// 之前某轮 CLI 报出过新 id 时按旧 id 记过映射，这里取映射值。
func (h *Harness) resolveSession(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if mapped := h.sessions[sessionID]; mapped != "" {
		return mapped
	}
	return sessionID
}

// rememberSession 记住 turn.SessionID 对应的最新 CLI 会话 id，供下一轮 resume。
// 首个 turn 的 SessionID 为空，调用方通过 EventSession 拿到新 id 后自行带回来。
func (h *Harness) rememberSession(turnSessionID, cliSessionID string) {
	if turnSessionID == "" || cliSessionID == "" || turnSessionID == cliSessionID {
		return
	}
	h.mu.Lock()
	if h.sessions == nil {
		h.sessions = make(map[string]string)
	}
	h.sessions[turnSessionID] = cliSessionID
	h.mu.Unlock()
}

// grantsFor 返回某个会话的审批放行集合（"Approve for session"），跨 turn 复用。
// 首个 turn 还没有会话 id：返回一份不入表的新集合，拿到 CLI 的会话 id 后再由
// adoptGrants 登记，避免所有新会话共用同一个空键。
func (h *Harness) grantsFor(sessionKey string) map[string]bool {
	if sessionKey == "" {
		return make(map[string]bool)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.grants == nil {
		h.grants = make(map[string]map[string]bool)
	}
	grants := h.grants[sessionKey]
	if grants == nil {
		grants = make(map[string]bool)
		h.grants[sessionKey] = grants
	}
	return grants
}

// grantsFollow 把会话级放行集合从旧会话 id 迁移到 CLI 新报出的 id，让
// "Approve for session" 在后续 turn（以新 id 恢复）里仍然有效。
func (h *Harness) grantsFollow(from, to string) {
	if from == to {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.adoptGrantsLocked(to, h.grants[from])
}

// adoptGrants 把一份放行集合登记到会话 id 名下（首个 turn 拿到 CLI 会话 id 时使用）。
func (h *Harness) adoptGrants(sessionKey string, grants map[string]bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.adoptGrantsLocked(sessionKey, grants)
}

// adoptGrantsLocked 要求调用方已持有 h.mu：目标会话已有集合时合并，否则直接挂上。
func (h *Harness) adoptGrantsLocked(sessionKey string, grants map[string]bool) {
	if sessionKey == "" || grants == nil {
		return
	}
	if h.grants == nil {
		h.grants = make(map[string]map[string]bool)
	}
	if existing := h.grants[sessionKey]; existing != nil {
		for kind, allowed := range grants {
			existing[kind] = allowed
		}
		return
	}
	h.grants[sessionKey] = grants
}

// registerRun 记录当轮的活动进程。
func (h *Harness) registerRun(run *activeRun) {
	h.mu.Lock()
	h.runs = append(h.runs, run)
	h.mu.Unlock()
}

// unregisterRun 移除当轮的活动进程。
func (h *Harness) unregisterRun(run *activeRun) {
	h.mu.Lock()
	for i, candidate := range h.runs {
		if candidate == run {
			h.runs = append(h.runs[:i], h.runs[i+1:]...)
			break
		}
	}
	h.mu.Unlock()
}

// findRun 返回第一个匹配的活动进程。
func (h *Harness) findRun(match func(*activeRun) bool) *activeRun {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, run := range h.runs {
		if match(run) {
			return run
		}
	}
	return nil
}

// exitStatus 取出进程退出码；被信号终止时为 -1（与 os/exec 一致）。
func exitStatus(waitErr error) int {
	if waitErr == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// killedByStopSignal 判断进程是否被 SIGTERM / SIGKILL 结束：这类结束是用户或上层
// 中止（ForceStopTurn、ctx 取消），不是运行失败。
func killedByStopSignal(waitErr error) bool {
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return false
	}
	signal := status.Signal()
	return signal == syscall.SIGTERM || signal == syscall.SIGKILL
}
