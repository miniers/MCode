// Package omp 实现了基于 omp CLI（oh-my-pi，可执行文件 `omp`，Pi 的分支）RPC 模式的
// harness.Harness 适配器。
//
// 协议契约：
//   - 一轮一个 `omp --mode rpc-ui` 子进程，stdin 写 JSON 行请求（get_available_commands、
//     get_state、prompt、abort、steer），stdout 读 JSON 行帧（message_*、tool_execution_*、
//     prompt_result、extension_ui_request 等）。prompt 的应答即时返回，当轮以携带同一 id 的
//     prompt_result 收尾；agent_end 不是终点，omp 在一次失败的尝试与重试之间也会发它。
//   - 会话恢复：get_state 应答里的 sessionFile 作为会话 id 经 EventSession 发出，
//     下一轮以 `--resume <sessionFile>` 启动。
//   - 运行模式注入：omp 自身不是门禁（用户配置的 tools.approval 能把工具
//     直接放行，yolo 也不再追问），所以任何权限模式都以 `--approval-mode yolo` 启动；
//     真正的裁决在门禁扩展（-e）里，它按启动前写入的运行模式文件（MCODE_OMP_GATE）
//     决定放行、询问还是拦截。询问以标题 "mcode-gate {…}" 的 select 回到适配器，按
//     turn.Approver 应答。计划模式把只读规则追加进系统提示词（--append-system-prompt）。
//   - 底层 provider 凭证：omp 自己持有登录（~/.omp/agent/agent.db，外部不读那个库），
//     适配器只经环境变量与启动配置把 omp 需要的上下文传下去（buildEnv）。
//
// 尚未实现的行为：rollback（会话切片回退）、requestTitle（/rename 取名）、
// compact、stopAgent（取消单个子代理）、子代理 Bee 与 Channel（omp 的 task 子代理映射）、
// 常驻进程复用（这里按轮启停）、斜杠命令列表、用量限额识别
// （usageLimit）、notice 类事件与上下文窗口上报。逐条见 README.md。
//
// 已在 internal/app 注册并接入设置页，实机联调尚未进行。
package omp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	_ harness.Steerer           = (*Harness)(nil)
	_ harness.TurnStopper       = (*Harness)(nil)
	_ harness.DeliveryConfirmer = (*Harness)(nil)
	_ harness.HostToolsReporter = (*Harness)(nil)
)

// DefaultBinary 是 omp CLI 的默认可执行文件名。
const DefaultBinary = "omp"

// GateEnvVar 是门禁扩展读取运行模式注入文件的路径环境变量。
const GateEnvVar = "MCODE_OMP_GATE"

// GateCommand 是门禁扩展注册的斜杠命令名：它出现在 get_available_commands 的命令表里
// 就说明门禁已加载。
const GateCommand = "mcode-gate"

// ompApprovalMode 是 omp 自身审批模式的启动取值。omp 在每个权限
// 模式下都以 yolo 启动——omp 自身的审批不是门禁（用户配置能把工具直接放行，yolo 不再
// 追问），工具的放行与否由门禁扩展在 app 侧按运行模式文件裁决。
const ompApprovalMode = "yolo"

// ompPlanPrompt 是计划模式下追加给 omp 的系统提示词。
const ompPlanPrompt = "Plan mode is on, so this turn is read-only: no file edits and no commands that change state. " +
	"You can still investigate: read files, use grep / glob / find, and run read-only shell commands " +
	"(ls, cat, rg, git status, ...). " +
	"Look into the task, then finish with the plan written in markdown."

// ompLaunchSettings 是 `--config` 启动配置叠加的内容：
// 关掉自动恢复（否则新会话会接着该目录上一个会话继续），禁掉 bash/async 的自动后台化，
// 限制 task 的递归深度——结果一律等待，不出现没人看过开场的续跑轮次。
const ompLaunchSettings = "autoResume: false\n" +
	"bash:\n  autoBackground:\n    enabled: false\n" +
	"async:\n  enabled: false\n" +
	"task:\n  maxRecursionDepth: 1\n"

// ompThinkingLevels 是 omp `--thinking` 接受的思考档位。
var ompThinkingLevels = map[string]bool{
	"off": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

// Options 包含 omp 适配器的配置选项。
type Options struct {
	Binary    string
	ExtraArgs []string
	// Extension 是 `-e` 加载的门禁扩展路径（指向写出的
	// mcode-gate.ts）。只有加载了扩展，工具调用才会以 mcode-gate 门禁回到适配器
	// 走 turn.Approver 审批。
	Extension string
	// ConfigPath 是 `--config` 指向的启动配置文件路径。
	// 为空时 Run 启动前会写一份默认配置。
	ConfigPath string
	// GatePath 是运行模式注入文件路径，经 GateEnvVar 传给门禁扩展。
	// 为空时 Run 启动前会写一份。
	GatePath string
	// SystemPrompt 是追加到 omp 系统提示词的策略文本（Swarm 调度、Anti-Slop 之类，
	// 由上层注入）；计划模式的只读规则由 turn.InteractionMode 附加。
	SystemPrompt string
	// Env 追加到子进程环境，排在最前，turn.Env 里的同名变量优先级更高。
	Env []string
	// Sandbox 是默认沙箱；turn.Sandbox 为空时使用。
	Sandbox harness.Sandbox
}

// Harness 实现了基于 omp RPC 模式的 harness.Harness 接口。
type Harness struct {
	opts Options

	// mu 保护 runs；activeRun.sessionID 同样由该锁保护。
	mu   sync.Mutex
	runs []*activeRun
	// roots 按会话记录正在运行的 agent 进程 PID，供 ProcessRootPIDs 使用。
	roots harness.ProcessRoots
}

// activeRun 是一轮正在执行的 omp 进程，供 Steer 与 ForceStopTurn 定位。
type activeRun struct {
	turnID    string
	sessionID string
	cmd       *exec.Cmd
	writer    *lineWriter
}

// New 创建一个新的 omp Harness 实例。
func New(opts Options) *Harness {
	return &Harness{opts: opts}
}

// Kind 返回后端的种类标识。
func (h *Harness) Kind() harness.Kind {
	return harness.Kind("omp")
}

// CatalogBinary 返回当前运行时配置的 CLI。
func (h *Harness) CatalogBinary() string { return h.opts.Binary }

// ProcessRootPIDs 返回指定会话当前运行的 agent 进程 PID，未在运行时返回 nil。
func (h *Harness) ProcessRootPIDs(threadID string) []int {
	return h.roots.PIDs(threadID)
}

// ConfirmsApprovalDelivery 返回 true，表示该 Provider 对每次 Approve/Interact 的答复都会调用
// DeliveryAcker.AckDelivery。
func (h *Harness) ConfirmsApprovalDelivery() bool {
	return true
}

// HostToolsAvailable 返回 false：omp 不接 MCP，会话里没有 automation_report_result、
// kanban_submit_result 等回执工具，调度器不应追问回执。
func (h *Harness) HostToolsAvailable() bool {
	return false
}

// buildEnv 构造子进程环境变量列表：
// 1. 从 base 中剔除所有以 "PWD=" 开头的项，避免原样继承父进程的环境变量 PWD；
// 2. 当 workDir 非空时追加 "PWD=" + workDir，保证子进程 PWD 与工作目录一致；
// 3. 最后追加 extra。extra 中自带的 PWD 因排在末尾而优先。
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

// lineWriter 串行化对 omp stdin 的写入：主循环、Steer 与强停可能并发操作同一管道。
type lineWriter struct {
	mu     sync.Mutex
	w      io.WriteCloser
	closed bool
}

func (l *lineWriter) write(line []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return io.ErrClosedPipe
	}
	_, err := l.w.Write(line)
	return err
}

func (l *lineWriter) close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	return l.w.Close()
}

// ompModelArg 返回要透传给 omp 的模型选择。只有非空、不是
// "default" 且带 provider 前缀（"provider/model"）的选择才传给 `--model`，
// 裸模型名交给 omp 自己的默认解析。
func ompModelArg(model string) string {
	if model == "" || model == "default" || !strings.Contains(model, "/") {
		return ""
	}
	return model
}

// ompSystemPrompt 组装 `--append-system-prompt` 的内容：上层给的策略文本在前，
// 计划模式的只读规则在后，空段丢弃。
func ompSystemPrompt(opts Options, turn harness.Turn) string {
	var parts []string
	if text := strings.TrimSpace(opts.SystemPrompt); text != "" {
		parts = append(parts, text)
	}
	if turn.InteractionMode == "plan" {
		parts = append(parts, ompPlanPrompt)
	}
	return strings.Join(parts, "\n\n")
}

// buildArgs 组装调用 omp RPC 模式所需的命令行参数。
//
// 运行模式的启动注入就落在这里——omp 对任何权限模式都用同一档
// `--approval-mode yolo`（裁决在门禁扩展里），计划模式则把只读规则追加进系统提示词；
// 运行模式本体随启动前写好的门禁文件（GateEnvVar）注入，见 prepareLaunchFiles。
func buildArgs(opts Options, turn harness.Turn) []string {
	args := []string{"--mode", "rpc-ui", "--approval-mode", ompApprovalMode}
	if opts.ConfigPath != "" {
		args = append(args, "--config", opts.ConfigPath)
	}
	if opts.Extension != "" {
		args = append(args, "-e", opts.Extension)
	}
	if model := ompModelArg(turn.Model); model != "" {
		args = append(args, "--model", model)
	}
	if turn.Effort != "" && ompThinkingLevels[turn.Effort] {
		args = append(args, "--thinking", turn.Effort)
	}
	if prompt := ompSystemPrompt(opts, turn); prompt != "" {
		args = append(args, "--append-system-prompt", prompt)
	}
	if turn.SessionID != "" {
		args = append(args, "--resume", turn.SessionID)
	}
	return append(args, opts.ExtraArgs...)
}

// gateState 是门禁扩展读取的运行模式注入内容。
type gateState struct {
	RuntimeMode     string `json:"runtimeMode"`
	InteractionMode string `json:"interactionMode"`
	FinalReport     bool   `json:"finalReport"`
	AllowTask       bool   `json:"allowTask"`
}

// runtimeModeName 把 harness 权限模式映射为门禁扩展认识的 RuntimeMode 取值
// （RuntimeMode 取值：supervised / autoAcceptEdits / auto / fullAccess）。
// 只读模式在 omp 侧没有对应档位，按最严的 supervised 处理（每次改动与命令都要问）。
func runtimeModeName(mode harness.PermissionMode) string {
	switch mode {
	case harness.ModeAutoAcceptEdits:
		return "autoAcceptEdits"
	case harness.ModeAuto:
		return "auto"
	case harness.ModeFullAccess:
		return "fullAccess"
	default:
		return "supervised"
	}
}

// gateContent 生成门禁文件内容：本轮的运行模式与交互模式，finalReport 与 allowTask
// 固定为 false（最终报告与 Swarm 子代理的开关由上层决定）。
func gateContent(turn harness.Turn) ([]byte, error) {
	interaction := turn.InteractionMode
	if interaction == "" {
		interaction = "build"
	}
	data, err := json.Marshal(gateState{
		RuntimeMode:     runtimeModeName(turn.Mode),
		InteractionMode: interaction,
		FinalReport:     false,
		AllowTask:       false,
	})
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// prepareLaunchFiles 保证本轮启动有一份 `--config` 启动配置与一份门禁运行模式文件，
// 并把运行模式写进门禁文件（在拉起进程前写好）。
// 返回填好路径的 Options 与清理函数：本轮为临时文件时删除它们，调用方传入的路径不动。
func (h *Harness) prepareLaunchFiles(turn harness.Turn) (Options, func(), error) {
	opts := h.opts
	var written []string
	cleanup := func() {
		for _, file := range written {
			_ = os.Remove(file)
		}
	}
	fail := func(err error) (Options, func(), error) {
		cleanup()
		return Options{}, func() {}, err
	}

	if opts.GatePath == "" {
		file, err := os.CreateTemp("", "mcode-omp-gate-*.json")
		if err != nil {
			return fail(fmt.Errorf("omp: 创建门禁文件: %w", err))
		}
		opts.GatePath = file.Name()
		written = append(written, opts.GatePath)
		_ = file.Close()
	}
	content, err := gateContent(turn)
	if err != nil {
		return fail(fmt.Errorf("omp: 序列化门禁: %w", err))
	}
	if err := os.WriteFile(opts.GatePath, content, 0o600); err != nil {
		return fail(fmt.Errorf("omp: 写入门禁文件: %w", err))
	}

	if opts.ConfigPath == "" {
		file, err := os.CreateTemp("", "mcode-omp-launch-*.yml")
		if err != nil {
			return fail(fmt.Errorf("omp: 创建启动配置: %w", err))
		}
		opts.ConfigPath = file.Name()
		written = append(written, opts.ConfigPath)
		_ = file.Close()
		if err := os.WriteFile(opts.ConfigPath, []byte(ompLaunchSettings), 0o600); err != nil {
			return fail(fmt.Errorf("omp: 写入启动配置: %w", err))
		}
	}
	return opts, cleanup, nil
}

// Run 执行一轮 omp 交互：写入门禁运行模式文件与启动配置，启动 `omp --mode rpc-ui`，
// （配置了门禁扩展时先确认门禁已加载）取状态拿会话 id，再写入 prompt，把 stdout 上的帧
// 翻译成 harness 事件。ctx 取消时先发 abort 请求，再按进程组强杀。
// 门禁扩展缺失时只有「完全访问」模式允许启动，其余模式直接报错。
func (h *Harness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	if turn.Prompt == "" && len(turn.Images) == 0 {
		return fmt.Errorf("omp: prompt is required")
	}
	// omp 对任何权限模式都用 --approval-mode yolo 启动，工具放行与否全靠门禁扩展裁决。
	// 门禁没装好就不启动：裸跑等于全放行，而界面仍显示只读/受监督。
	if h.opts.Extension == "" && turn.Mode != harness.ModeFullAccess {
		return fmt.Errorf("omp: 门禁扩展未加载，无法以 %s 权限模式启动（omp 在缺门禁时会把所有工具直接放行）", turn.Mode)
	}

	binary := h.opts.Binary
	if binary == "" {
		binary = DefaultBinary
	}
	// GUI 进程的 PATH 里通常没有 node 全局 bin，omp 又是需要 node 的脚本：
	// 裸名字一律在登录 shell 的 PATH 里解析成绝对路径；解析失败保留原值，让启动错误照常暴露。
	if !strings.Contains(binary, "/") {
		if resolved, err := loginenv.LookPath(binary); err == nil {
			binary = resolved
		}
	}

	sandbox := turn.Sandbox
	if sandbox == nil {
		sandbox = h.opts.Sandbox
	}

	opts, cleanupFiles, err := h.prepareLaunchFiles(turn)
	if err != nil {
		return err
	}
	defer cleanupFiles()

	args := buildArgs(opts, turn)

	var cmd *exec.Cmd
	var cleanup func()
	if sandbox != nil {
		cmd, cleanup, err = sandbox.Command(ctx, turn.WorkDir, turn.Mode, binary, args...)
		if err != nil {
			return fmt.Errorf("omp: sandbox command: %w", err)
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
	extraEnv := append(append([]string{}, opts.Env...), turn.Env...)
	extraEnv = append(extraEnv, GateEnvVar+"="+opts.GatePath)
	cmd.Env = buildEnv(baseEnv, turn.WorkDir, extraEnv)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("omp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("omp: stdout pipe: %w", err)
	}
	stderrTail := &tailBuffer{limit: 8192}
	cmd.Stderr = stderrTail

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("omp: start: %w", err)
	}
	// 按会话记录 agent 进程 PID，供「终止运行中的命令」定位命令子进程树。
	agentPID := cmd.Process.Pid
	h.roots.Set(turn.ThreadID, agentPID)
	defer h.roots.Clear(turn.ThreadID, agentPID)

	writer := &lineWriter{w: stdin}
	run := &activeRun{turnID: turn.ID, sessionID: turn.SessionID, cmd: cmd, writer: writer}
	h.registerRun(run)
	defer h.unregisterRun(run)

	// 正常路径与提前返回都走同一套回收：关 stdin、杀掉进程组、回收进程。
	defer func() {
		_ = writer.close()
		_ = killProcessGroup(cmd, syscall.SIGKILL)
		// 正常路径已经 Wait 过，重复调用只会返回错误，忽略即可。
		_ = cmd.Wait()
	}()

	state := newStreamState()
	commandsRequestID := nextRequestID()
	stateRequestID := nextRequestID()
	state.promptID = nextRequestID()

	sendState := func() error {
		line, err := requestLine(getStateRequest(stateRequestID))
		if err != nil {
			return err
		}
		return writer.write(line)
	}

	// 配置了门禁扩展时，先问命令表确认门禁加载，
	// 没有扩展时直接取状态。
	if opts.Extension != "" {
		line, err := requestLine(getAvailableCommandsRequest(commandsRequestID))
		if err != nil {
			return err
		}
		if err := writer.write(line); err != nil {
			return fmt.Errorf("omp: write get_available_commands: %w", err)
		}
	} else if err := sendState(); err != nil {
		return fmt.Errorf("omp: write get_state: %w", err)
	}

	promptSent := false
	sendPrompt := func() error {
		if promptSent {
			return nil
		}
		promptSent = true
		line, err := requestLine(promptRequest(state.promptID, turn.Prompt, imagePayloads(turn.Images)))
		if err != nil {
			return err
		}
		return writer.write(line)
	}

	reader := bufio.NewReader(stdout)
	done := false
	for !done {
		rawLine, readErr := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(rawLine); len(trimmed) > 0 {
			event, parseErr := parseEvent(trimmed)
			if parseErr == nil && event != nil {
				switch {
				case event.Type == "response" && event.ID == commandsRequestID:
					if !hasCommand(event, GateCommand) {
						message := "omp did not load mcode's tool gate, so it was not started."
						_ = harness.Emit(emit, harness.Event{Type: harness.EventError, Err: message})
						return fmt.Errorf("omp: %s", message)
					}
					if err := sendState(); err != nil {
						return fmt.Errorf("omp: write get_state: %w", err)
					}
				case event.Type == "response" && event.ID == stateRequestID:
					if sessionID := sessionIDFromState(event); sessionID != "" {
						h.setRunSession(run, sessionID)
						_ = harness.Emit(emit, harness.Event{Type: harness.EventSession, SessionID: sessionID})
					}
					if err := sendPrompt(); err != nil {
						return fmt.Errorf("omp: write prompt: %w", err)
					}
				case event.Type == "extension_ui_request":
					// 门禁与提问的应答可能阻塞在用户审批上，这里同步等待，与 pi/codex 的审批管道一致。
					h.handleExtensionUI(ctx, turn, writer, event)
				default:
					if state.translate(event, emit) {
						done = true
					}
				}
			}
		}
		if readErr != nil {
			break
		}
	}

	if ctx.Err() != nil {
		// ctx 取消：先发 abort 让 omp 收尾，再按进程组强杀兜底。
		if line, err := requestLine(abortRequest()); err == nil {
			_ = writer.write(line)
		}
		_ = writer.close()
		_ = killProcessGroup(cmd, syscall.SIGTERM)
		_ = cmd.Wait()
		return ctx.Err()
	}

	// 当轮结束后 omp 进程仍会驻留，主动终止整个进程组再回收。
	_ = writer.close()
	_ = killProcessGroup(cmd, syscall.SIGTERM)
	waitErr := cmd.Wait()

	if !state.sawDone {
		stderrText := strings.TrimSpace(stderrTail.String())
		errMessage := firstNonEmpty(state.turnErr, stderrText)
		if errMessage == "" && waitErr != nil {
			errMessage = waitErr.Error()
		}
		if errMessage == "" {
			errMessage = "omp exited before the turn settled"
		}
		_ = harness.Emit(emit, harness.Event{Type: harness.EventError, Err: errMessage})
		return fmt.Errorf("omp: process exited before the turn settled: %s", errMessage)
	}

	return nil
}

// Steer 向指定会话正在执行的轮次注入指引：以 steer 请求写入其 stdin。
// 找不到对应会话的活动进程时返回 harness.ErrNotRunning。
//
// 说明：按 omp 继承自 Pi 的 RPC 协议实现 steer
// （Omp 的协议与 Pi 的 RPC 很接近），实机行为待联调校准。
func (h *Harness) Steer(ctx context.Context, sessionID string, text string) error {
	if sessionID == "" {
		return harness.ErrNotRunning
	}
	run := h.findRun(func(r *activeRun) bool { return r.sessionID == sessionID })
	if run == nil {
		return harness.ErrNotRunning
	}
	line, err := requestLine(steerRequest(nextRequestID(), text))
	if err != nil {
		return err
	}
	if err := run.writer.write(line); err != nil {
		return fmt.Errorf("omp: steer: %w", err)
	}
	return nil
}

// ForceStopTurn 强停指定轮次的 omp 进程组，不影响同一 Harness 的其他会话。
func (h *Harness) ForceStopTurn(turnID string) {
	run := h.findRun(func(r *activeRun) bool { return r.turnID == turnID })
	if run == nil {
		return
	}
	_ = run.writer.close()
	_ = killProcessGroup(run.cmd, syscall.SIGKILL)
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

// setRunSession 在 omp 回报会话 id 之后更新活动进程的会话标识。
func (h *Harness) setRunSession(run *activeRun, sessionID string) {
	h.mu.Lock()
	run.sessionID = sessionID
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
