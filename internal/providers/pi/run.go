package pi

// 本文件是 Harness.Run 的实现：按线程取用或新起 pi 进程（acquire / startProc），
// 在进程上跑一轮（runTurn），再交还或回收（release）。进程池只在 Options.IdleTTL > 0 且
// 轮次带 ThreadID 时启用；否则每轮一个进程，当轮结束即退出。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"mcode/internal/harness"
	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
	"mcode/internal/redact"
)

// errStaleProc 表示复用的进程在这一轮真正开始前就已经不可用（空闲期间退出），
// Run 会丢掉它，换一个新进程重来一次。
var errStaleProc = errors.New("pi: resident process is gone")

// launchSpec 是启动 pi 所需、且不用落盘就能算出的信息；key 相同的进程可以跨轮复用。
type launchSpec struct {
	binary       string
	key          string
	sandbox      harness.Sandbox
	mcpEnv       string
	mcpEnabled   bool
	magpieBridge string
	magpieEnv    string
	skills       []string
	// skillsFull 为 true 时走完整清单模式：参数里加 --no-skills，技能只看 skills 列表。
	skillsFull bool
}

// planLaunch 汇总本轮的启动参数并算出复用键。
func (h *Harness) planLaunch(turn harness.Turn) launchSpec {
	binary := h.opts.Binary
	if binary == "" {
		binary = DefaultBinary
	}
	// GUI 进程的 PATH 里通常没有 node 全局 bin，pi 又是需要 node 的脚本：
	// 裸名字一律在登录 shell 的 PATH 里解析成绝对路径；解析失败保留原值，让启动错误照常暴露。
	if !strings.Contains(binary, "/") {
		if resolved, err := loginenv.LookPath(binary); err == nil {
			binary = resolved
		}
	}
	spec := launchSpec{binary: binary, sandbox: turn.Sandbox}
	if spec.sandbox == nil {
		spec.sandbox = h.opts.Sandbox
	}
	// MCP：有服务时把清单写进环境变量并额外加载桥接扩展，没有服务时两者都不做。
	spec.mcpEnv, spec.mcpEnabled = mcpServersEnvValue(turn, h.opts.BuiltinMCP, h.opts.ExternalMCP)
	// Magpie：设置里有端点时才加载账号桥并把端点清单写进环境变量；为空时什么都不做，
	// pi 用它自己的配置。桥只是追加的扩展，不改变其余参数。
	spec.magpieBridge, spec.magpieEnv = h.opts.magpieInjection()
	if h.opts.Skills != nil && turn.WorkDir != "" {
		raw, full := h.opts.Skills(turn.WorkDir)
		spec.skillsFull = full
		if len(raw) > 0 {
			cp := append([]string(nil), raw...)
			sort.Strings(cp)
			var dedup []string
			for _, s := range cp {
				if len(dedup) == 0 || dedup[len(dedup)-1] != s {
					dedup = append(dedup, s)
				}
			}
			spec.skills = dedup
		}
	}
	spec.key = launchKey(h.opts, turn, spec)
	return spec
}

// modelClass 把 turn.Model 归为三类：交给 pi 默认解析的 "default"、可以用 set_model 就地切换的
// "qualified"（provider/model 形式），以及只能靠启动参数指定的裸模型 id。
// 类别进复用键：同类才可能复用，只有 qualified 之间的变化走 set_model。
func modelClass(model string) string {
	if model == "" || model == "default" {
		return "default"
	}
	if _, _, qualified := splitModelRef(model); qualified {
		return "qualified"
	}
	return "model:" + model
}

// launchKey 是进程复用键：启动时固定下来、之后无法再改的东西都在里面（模型与思考档位只记类别）。
// 取哈希是为了不在内存里多留一份拼好的端点令牌与 API key。
func launchKey(opts Options, turn harness.Turn, spec launchSpec) string {
	parts := []string{
		spec.binary,
		turn.WorkDir,
		modelClass(turn.Model),
		strconv.FormatBool(turn.Effort != ""),
		spec.mcpEnv,
		spec.magpieBridge,
		spec.magpieEnv,
		opts.Extension,
		opts.GatePath,
		strings.Join(opts.Env, "\x01"),
		strings.Join(turn.Env, "\x01"),
		strings.Join(opts.ExtraArgs, "\x01"),
		strings.Join(spec.skills, "\x01"),
		strconv.FormatBool(spec.skillsFull),
	}
	if spec.sandbox != nil {
		// 沙箱按权限模式生成命令，模式一变就得换进程。
		parts = append(parts, "sandbox:"+string(turn.Mode))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// Run 执行一轮 Pi 交互：取用（或启动）`pi --mode rpc` 进程，先取状态拿到会话 id，再写入 prompt，
// 把 stdout 上的事件翻译成 harness 事件。ctx 取消时先发 abort 请求；进程能回到空闲就留着，否则回收。
func (h *Harness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	if turn.Prompt == "" && len(turn.Images) == 0 {
		return fmt.Errorf("pi: prompt is required")
	}
	images, err := imagePayloads(turn.Images)
	if err != nil {
		_ = harness.Emit(emit, harness.Event{Type: harness.EventError, Err: err.Error()})
		return err
	}
	spec := h.planLaunch(turn)
	// 直连时没有桥注册 provider，线程里残留的注入模型会让 pi 报 Unknown provider。
	if spec.magpieEnv == "" && h.opts.InjectedModel != nil && h.opts.InjectedModel(turn.Model) {
		turn.Model = "default"
		spec = h.planLaunch(turn)
	}
	for attempt := 0; ; attempt++ {
		p, pooled, reused, err := h.acquire(turn, spec)
		if err != nil {
			return err
		}
		err = h.runTurn(ctx, p, turn, images, emit, pooled, reused)
		if errors.Is(err, errStaleProc) && attempt == 0 {
			continue
		}
		return err
	}
}

// acquire 取出本轮使用的进程：线程上有空闲且键与会话都对得上的驻留进程就复用，
// 否则启动新进程。pooled 表示进程归池管理（轮后可能留下），reused 表示它是复用的。
func (h *Harness) acquire(turn harness.Turn, spec launchSpec) (p *proc, pooled, reused bool, err error) {
	// 辅助会话（Swarm bee）一般跑一轮就结束，常驻只会白占名额，走用完即关的路径。
	pooled = h.opts.IdleTTL > 0 && turn.ThreadID != "" && !turn.Transient
	var stale *proc
	if pooled {
		h.mu.Lock()
		if cur := h.procs[turn.ThreadID]; cur != nil {
			cur.mu.Lock()
			switch {
			case cur.busy:
				// 同一线程上一轮还没收尾：这一轮用独立进程，不动池里的那个。
				pooled = false
			case !cur.dead && cur.key == spec.key && cur.sessionID == turn.SessionID:
				cur.busy = true
				if cur.idle != nil {
					cur.idle.Stop()
					cur.idle = nil
				}
				cur.mu.Unlock()
				h.mu.Unlock()
				return cur, true, true, nil
			default:
				stale = cur
				delete(h.procs, turn.ThreadID)
			}
			cur.mu.Unlock()
		}
		h.mu.Unlock()
	}
	if stale != nil {
		stale.close()
	}

	p, err = h.startProc(turn, spec)
	if err != nil {
		return nil, false, false, err
	}
	p.busy = true
	p.lastUsed = time.Now()
	if pooled {
		h.register(p)
	}
	return p, pooled, false, nil
}

// register 把进程登记为线程的驻留进程，并在超出上限时回收最久没用的空闲进程。
func (h *Harness) register(p *proc) {
	h.mu.Lock()
	h.procs[p.threadID] = p
	h.mu.Unlock()
	h.trim()
}

// trim 把驻留进程数压回上限：回收最久没用的空闲进程，正在跑的不动。
// 全部都在忙时可以暂时超出上限，等它们跑完在 release 里再收。
func (h *Harness) trim() {
	limit := h.opts.MaxResident
	if limit <= 0 {
		limit = defaultMaxResident
	}
	var evicted []*proc
	h.mu.Lock()
	for len(h.procs) > limit {
		var oldest *proc
		for _, candidate := range h.procs {
			candidate.mu.Lock()
			idle := !candidate.busy
			older := oldest == nil || candidate.lastUsed.Before(oldest.lastUsed)
			candidate.mu.Unlock()
			if idle && older {
				oldest = candidate
			}
		}
		if oldest == nil {
			break
		}
		delete(h.procs, oldest.threadID)
		evicted = append(evicted, oldest)
	}
	h.mu.Unlock()
	for _, victim := range evicted {
		victim.close()
	}
}

// reap 回收空闲超时的驻留进程；它在等待期间又被复用时什么都不做。
func (h *Harness) reap(p *proc) {
	h.mu.Lock()
	p.mu.Lock()
	busy := p.busy
	p.mu.Unlock()
	if busy {
		h.mu.Unlock()
		return
	}
	if h.procs[p.threadID] == p {
		delete(h.procs, p.threadID)
	}
	h.mu.Unlock()
	p.close()
}

// procExited 在进程收尾后把它从池里摘掉。
func (h *Harness) procExited(p *proc) {
	h.mu.Lock()
	if h.procs[p.threadID] == p {
		delete(h.procs, p.threadID)
	}
	h.mu.Unlock()
}

// release 在一轮结束后处理进程：keep 为真且归池管理就留下并开始空闲计时，否则回收。
func (h *Harness) release(p *proc, keep bool) {
	if keep {
		h.mu.Lock()
		// harness 已被替换（CloseIdle）：不再回池，免得旧的 Node 进程与新 harness 的一起驻留。
		keep = !h.closed
		h.mu.Unlock()
	}
	if !keep {
		p.closeAndWait()
		return
	}
	p.mu.Lock()
	p.busy = false
	p.lastUsed = time.Now()
	p.idle = time.AfterFunc(h.opts.IdleTTL, func() { h.reap(p) })
	p.mu.Unlock()
	// 并发高峰里 register 时可能全忙、没法回收；这一轮收尾后补收，免得多出来的进程各自等满空闲时限。
	h.trim()
}

// startProc 启动一个 `pi --mode rpc` 进程：准备桥扩展与门禁文件，拉起进程并开始读 stdout。
// 进程退出后这些文件与沙箱资源由读取协程统一清理。
func (h *Harness) startProc(turn harness.Turn, spec launchSpec) (*proc, error) {
	var cleanups []func()
	abandon := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}

	mcpExtension := ""
	if spec.mcpEnabled {
		dir, err := os.MkdirTemp("", "mcode-pi-mcp-")
		if err != nil {
			return nil, fmt.Errorf("pi: 创建 MCP 桥临时目录: %w", err)
		}
		cleanups = append(cleanups, func() { _ = os.RemoveAll(dir) })
		mcpExtension, err = installMCPBridge(dir)
		if err != nil {
			abandon()
			return nil, fmt.Errorf("pi: 写入 MCP 桥: %w", err)
		}
	}

	// 门禁扩展按本轮权限与交互模式裁决，所以进程启动前先把运行模式写进文件。
	opts, cleanupGate, err := h.prepareGateFile(turn)
	if err != nil {
		abandon()
		return nil, fmt.Errorf("pi: 写入门禁文件: %w", err)
	}
	cleanups = append(cleanups, cleanupGate)

	args := buildArgs(opts, turn, mcpExtension, spec.magpieBridge, spec.skills, spec.skillsFull)

	// 进程的生命周期不属于任何一轮：用不会取消的 ctx 构造命令，取消一轮靠 abort 请求。
	var cmd *exec.Cmd
	if spec.sandbox != nil {
		var cleanup func()
		cmd, cleanup, err = spec.sandbox.Command(context.Background(), turn.WorkDir, turn.Mode, spec.binary, args...)
		if err != nil {
			abandon()
			return nil, fmt.Errorf("pi: sandbox command: %w", err)
		}
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
	} else {
		cmd = procattr.HideWindow(exec.Command(spec.binary, args...))
	}
	setProcessGroup(cmd)
	cmd.WaitDelay = 3 * time.Second

	baseEnv := loginenv.Environ()
	if cmd.Env != nil {
		baseEnv = cmd.Env
	}
	cmd.Dir = turn.WorkDir
	// 只关掉版本检查与遥测：PI_OFFLINE 会连带禁止 pi 按需下载 fd/rg（find、grep 因此失败）
	// 并跳过 RPC 模式下的模型目录刷新，粒度太粗。两项都排在最前，可被 opts.Env 与 turn.Env 覆盖。
	extraEnv := []string{"PI_SKIP_VERSION_CHECK=1", "PI_TELEMETRY=0"}
	extraEnv = append(extraEnv, opts.Env...)
	extraEnv = append(extraEnv, turn.Env...)
	if spec.mcpEnabled {
		extraEnv = append(extraEnv, mcpServersEnv+"="+spec.mcpEnv)
	}
	extraEnv = append(extraEnv, GateEnvVar+"="+opts.GatePath)
	// 桥路径一并注入：vision 子进程要用它重新加载同一个桥，Magpie 路由的模型才找得到。
	extraEnv = append(extraEnv, magpieEnvPairs(spec.magpieEnv, spec.magpieBridge)...)
	cmd.Env = withLoopbackNoProxy(withNodeEnvProxy(buildEnv(baseEnv, turn.WorkDir, extraEnv)))

	stdin, err := cmd.StdinPipe()
	if err != nil {
		abandon()
		return nil, fmt.Errorf("pi: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		abandon()
		return nil, fmt.Errorf("pi: stdout pipe: %w", err)
	}
	stderrTail := &tailBuffer{limit: 8192}
	cmd.Stderr = stderrTail

	if err := cmd.Start(); err != nil {
		// Start 失败时 cmd 不会关闭已创建的管道，这里自己关掉，免得多丢两个 fd。
		_ = stdin.Close()
		_ = stdout.Close()
		abandon()
		return nil, fmt.Errorf("pi: start: %w", err)
	}

	p := &proc{
		key:      spec.key,
		threadID: turn.ThreadID,
		cmd:      cmd,
		writer:   &lineWriter{w: stdin},
		stderr:   stderrTail,
		gatePath: opts.GatePath,
		cleanups: cleanups,
		exited:   make(chan struct{}),
		model:    turn.Model,
		effort:   turn.Effort,
	}
	go p.readLoop(stdout, h.procExited)
	return p, nil
}

// refreshReused 让复用的进程跟上这一轮：重写门禁运行模式文件，并按需切换模型与思考档位。
func (h *Harness) refreshReused(ctx context.Context, p *proc, turn harness.Turn) error {
	content, err := gateContent(turn)
	if err != nil {
		return err
	}
	if err := os.WriteFile(p.gatePath, content, 0o600); err != nil {
		return fmt.Errorf("pi: 写入门禁文件: %w", err)
	}

	p.mu.Lock()
	curModel, curEffort := p.model, p.effort
	p.mu.Unlock()

	modelChanged := modelClass(turn.Model) == "qualified" && turn.Model != curModel
	if modelChanged {
		provider, modelID, _ := splitModelRef(turn.Model)
		if _, err := p.call(ctx, setModelRequest(nextRequestID(), provider, modelID)); err != nil {
			return err
		}
		p.mu.Lock()
		p.model = turn.Model
		p.mu.Unlock()
	}
	// 换模型之后 pi 可能把思考档位钳到新模型支持的范围，所以换了模型就重新设一次。
	if turn.Effort != "" && (modelChanged || turn.Effort != curEffort) {
		if _, err := p.call(ctx, setThinkingRequest(nextRequestID(), turn.Effort)); err != nil {
			return err
		}
		p.mu.Lock()
		p.effort = turn.Effort
		p.mu.Unlock()
	} else if modelChanged && turn.Effort == "" {
		settings := Settings(nil, "")
		level := piDefaultThinkingLevel(turn.Model, settings)
		if _, err := p.call(ctx, setThinkingRequest(nextRequestID(), level)); err != nil {
			return err
		}
		p.mu.Lock()
		p.effort = ""
		p.mu.Unlock()
	}
	return nil
}

// contextWindowFromState 从 get_state 应答的 data.model.contextWindow 取出当前模型的上下文窗口上限。
// 模型未选中、字段缺失或非正数时返回 0，表示未知，用量事件里不上报。
// 注意：必须拿切换模型之后的应答，所以调用点放在 refreshReused 之后。
func contextWindowFromState(ev *rpcEvent) int64 {
	data := ev.Data
	if len(data) == 0 {
		data = ev.Message
	}
	var state struct {
		Model struct {
			ContextWindow json.RawMessage `json:"contextWindow"`
		} `json:"model"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return 0
	}
	window, _ := jsonInt(state.Model.ContextWindow)
	return window
}

// runTurn 在进程上跑一轮。pooled 表示进程轮后可以留下；reused 表示它是复用的，
// 复用的进程如果在这一轮开始前就已经退出，返回 errStaleProc 让调用方换新进程重来。
func (h *Harness) runTurn(ctx context.Context, p *proc, turn harness.Turn, images []imagePayload, emit func(harness.Event), pooled, reused bool) error {
	keep := false
	defer func() { h.release(p, pooled && keep) }()

	// 按会话记录 agent 进程 PID，供「终止运行中的命令」定位命令子进程树。
	agentPID := p.cmd.Process.Pid
	h.roots.Set(turn.ThreadID, agentPID)
	defer h.roots.Clear(turn.ThreadID, agentPID)

	run := &activeRun{turnID: turn.ID, sessionID: turn.SessionID, cmd: p.cmd, writer: p.writer}
	h.registerRun(run)
	defer h.unregisterRun(run)

	sink := p.attach()
	defer p.detach(sink)

	state := newStreamState()
	// 驻留的进程留给下一轮，必须等 pi 报告没有后续工作（agent_settled）才算结束。
	state.strictSettle = pooled

	if reused {
		if err := h.refreshReused(ctx, p, turn); err != nil {
			switch {
			case ctx.Err() != nil:
				keep = h.cancelTurn(p, pooled)
				return ctx.Err()
			case errors.Is(err, errProcDead):
				return errStaleProc
			}
			_ = harness.Emit(emit, harness.Event{Type: harness.EventError, Err: err.Error()})
			return err
		}
	}

	// 先取状态：应答里的 sessionFile 是下一轮恢复会话用的 id，
	// model.contextWindow 是本轮实际使用模型的上下文窗口上限。
	resp, err := p.call(ctx, getStateRequest(nextRequestID()))
	switch {
	case err == nil:
		if sessionID := sessionIDFromState(resp); sessionID != "" {
			p.setSession(sessionID)
			h.setRunSession(run, sessionID)
			_ = harness.Emit(emit, harness.Event{Type: harness.EventSession, SessionID: sessionID})
		}
		// 复用的进程可能刚在 refreshReused 里换过模型，所以窗口一律取自这次应答。
		if window := contextWindowFromState(resp); window > 0 {
			state.contextWindow = window
		}
	case ctx.Err() != nil:
		keep = h.cancelTurn(p, pooled)
		return ctx.Err()
	case errors.Is(err, errProcDead):
		if reused {
			return errStaleProc
		}
		return h.exitedError(p, state, emit)
	}
	// get_state 被 pi 拒绝时不挡这一轮：拿不到会话 id 只影响下一轮的恢复。

	line, err := requestLine(promptRequest(nextRequestID(), turn.Prompt, images))
	if err != nil {
		return err
	}
	if err := p.writer.write(line); err != nil {
		if reused {
			return errStaleProc
		}
		return fmt.Errorf("pi: write prompt: %w", err)
	}
	// prompt 帧已经写进进程管道，先上报送达信号：pi 的 RPC 写入成功只说明帧进了管道，
	// 不代表 pi 已经处理（弱保证）。写成功后本轮不会再重试，所以每轮只报这一次。
	_ = harness.Emit(emit, harness.Event{Type: harness.EventTurnAccepted})

	for {
		select {
		case ev, open := <-sink.ch:
			if !open {
				return h.exitedError(p, state, emit)
			}
			if ev.Type == "extension_ui_request" {
				// 门禁应答可能阻塞在用户审批上，这里同步等待，与 codex 的审批管道一致。
				h.handleExtensionUI(ctx, turn, p.writer, ev)
				continue
			}
			if state.translate(ev, emit) {
				keep = true
				return nil
			}
		case <-ctx.Done():
			keep = h.cancelTurn(p, pooled)
			return ctx.Err()
		}
	}
}

// cancelTurn 在 ctx 取消后让 pi 收尾，返回进程之后还能不能留着。
// 驻留进程发带 id 的 abort 并等 pi 回到空闲；一次性进程只发 abort，随后由 release 回收。
func (h *Harness) cancelTurn(p *proc, pooled bool) (keep bool) {
	if !pooled {
		if line, err := requestLine(abortRequest("")); err == nil {
			_ = p.writer.write(line)
		}
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), abortGrace)
	defer cancel()
	_, err := p.call(ctx, abortRequest(nextRequestID()))
	return err == nil
}

// sanitizeStderr 对子进程 stderr 进行凭证脱敏并将 HOME 路径替换为 ~。
func sanitizeStderr(s string) string {
	s = redact.Secrets(s)
	home, err := os.UserHomeDir()
	if err == nil && home != "" && home != "/" {
		s = strings.ReplaceAll(s, home, "~")
	}
	return s
}

// exitedError 处理进程在当轮 settle 之前就退出：等它收尾，报告错误并回收。
func (h *Harness) exitedError(p *proc, state *streamState, emit func(harness.Event)) error {
	<-p.exited
	if state.sawDone {
		return nil
	}
	errMessage := firstNonEmpty(state.turnErr, strings.TrimSpace(sanitizeStderr(p.stderr.String())))
	if errMessage == "" && p.waitErr != nil {
		errMessage = p.waitErr.Error()
	}
	if errMessage == "" {
		errMessage = "pi exited before the turn settled"
	}
	_ = harness.Emit(emit, harness.Event{Type: harness.EventError, Err: errMessage})
	return fmt.Errorf("pi: process exited before the turn settled: %s", errMessage)
}
