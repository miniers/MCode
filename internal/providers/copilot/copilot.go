// Package copilot 实现了基于 GitHub Copilot CLI（`copilot --headless --stdio`）的 harness.Harness 适配器。
//
// 协议契约与版本说明：
// 按 Copilot SDK（github/copilot-sdk，协议版本 3）的 JSON-RPC 2.0 规范实现。
// 传输层采用 `Content-Length: N\r\n\r\n<body>` 分帧，单帧上限 16MB。
//
// 架构：
//   - 每个会话一个常驻 `copilot --headless --stdio` 子进程，按 mcode 的会话 id 创建或恢复；
//   - 一轮对话先应用权限/交互模式与模型选择，再以 `session.send` 下发提示；
//   - 轮次事件通过 `session.event` 通知流式到达，映射为 harness.Event；
//   - 权限提示走 `permission.requested` 事件与 `session.permissions.handlePendingPermissionRequest`，
//     `userInput.request` 与 `exitPlanMode.request` 是服务端请求，用 Approver/Interactor 原地答复；
//   - ctx 取消时调用 `session.abort` 优雅中断，超时后杀死进程组。
package copilot

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"mcode/internal/harness"
	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

var _ harness.Harness = (*Harness)(nil)

// Kind 是 Copilot 后端的种类标识。
const Kind harness.Kind = "copilot"

// Options 包含 Copilot 适配器的配置选项。
type Options struct {
	Binary    string
	ExtraArgs []string
	Env       []string
	// SystemMessage 以 append 模式追加到每个会话的系统消息（为空时仍下发空 append，
	// 保证被恢复的会话不会继续沿用上一次的追加指导）。
	SystemMessage string
	// BuiltinMCP/ExternalMCP 决定本轮注入的 MCP 服务。每个会话一个子进程，
	// 因此内置服务可以按线程路由（BuiltinMCP.Endpoint(turn)）。
	BuiltinMCP  harness.BuiltinMCP
	ExternalMCP func(workDir, threadID string) []harness.MCPEndpoint
}

// Harness 实现了基于 Copilot CLI headless server 的 harness.Harness 接口。
type Harness struct {
	opts Options

	// mu 保护 sessions。
	mu       sync.Mutex
	sessions map[string]*sessionProc

	// roots 按会话记录本轮使用的常驻 CLI 进程 PID，供 ProcessRootPIDs 使用。
	roots harness.ProcessRoots
}

// New 创建一个新的 Copilot Harness 实例。
func New(opts Options) *Harness {
	if opts.Binary == "" {
		opts.Binary = DefaultBinary()
	}
	return &Harness{opts: opts, sessions: make(map[string]*sessionProc)}
}

// DefaultBinary 返回 Copilot CLI 的默认可执行文件名。
func DefaultBinary() string { return "copilot" }

// Kind 返回后端的种类标识。
func (h *Harness) Kind() harness.Kind { return Kind }

// CatalogBinary 返回当前运行时配置的 CLI。
func (h *Harness) CatalogBinary() string { return h.opts.Binary }

// ConfirmsApprovalDelivery 返回 true，表示该 Provider 对每次 Approve/Interact 的答复都会调用 DeliveryAcker.AckDelivery。
func (h *Harness) ConfirmsApprovalDelivery() bool { return true }

// ProcessRootPIDs 返回指定会话本轮使用的常驻 CLI 进程 PID，未在运行时返回 nil。
func (h *Harness) ProcessRootPIDs(threadID string) []int { return h.roots.PIDs(threadID) }

// 说明：Copilot CLI 没有轮次中注入（steer）能力，因此本适配器不实现 harness.Steerer 接口。

// sessionProc 代表一个常驻的 `copilot --headless --stdio` 子进程，服务于一个会话。
type sessionProc struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	conn      *Conn
	stderrBuf *tailBuffer
	workDir   string
	cleanup   func()

	deadCh   chan struct{}
	deadOnce sync.Once
	killOnce sync.Once

	// cleanupOnce/waitOnce 收敛 close 与 kill 两条路径：沙箱回收只能做一次，
	// 同一个 exec.Cmd 也只能 Wait 一次。waitDone 在 Wait 结束后关闭。
	cleanupOnce sync.Once
	waitOnce    sync.Once
	doneOnce    sync.Once
	waitDone    chan struct{}

	stateMu         sync.Mutex
	sessionID       string
	permissionMode  string
	interactionMode string
	model           string
	effort          string

	atMu   sync.RWMutex
	active *eventHandler
}

func (p *sessionProc) setActiveTurn(at *eventHandler) {
	p.atMu.Lock()
	p.active = at
	p.atMu.Unlock()
}

func (p *sessionProc) getActiveTurn() *eventHandler {
	p.atMu.RLock()
	defer p.atMu.RUnlock()
	return p.active
}

func (p *sessionProc) currentSessionID() string {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	return p.sessionID
}

func (p *sessionProc) setSessionID(id string) {
	p.stateMu.Lock()
	p.sessionID = id
	p.stateMu.Unlock()
}

func (p *sessionProc) isDead() bool {
	select {
	case <-p.deadCh:
		return true
	default:
		return false
	}
}

// call 在常驻连接上发起一次 JSON-RPC 请求。
func (p *sessionProc) call(ctx context.Context, method string, params any, result any) error {
	if p.conn == nil {
		return harness.ErrNotRunning
	}
	return p.conn.Call(ctx, method, params, result)
}

func (p *sessionProc) markDead() {
	p.deadOnce.Do(func() { close(p.deadCh) })
}

// wait 回收子进程，保证同一个 exec.Cmd 只 Wait 一次，并在结束时关闭 waitDone。
func (p *sessionProc) wait() {
	p.waitOnce.Do(func() {
		if p.cmd != nil {
			_ = p.cmd.Wait()
		}
		close(p.doneCh())
	})
}

// doneCh 返回 Wait 结束信号通道；懒创建，直接用字面量构造的 sessionProc 也能安全使用。
func (p *sessionProc) doneCh() chan struct{} {
	p.doneOnce.Do(func() {
		if p.waitDone == nil {
			p.waitDone = make(chan struct{})
		}
	})
	return p.waitDone
}

// releaseCleanup 只执行一次沙箱回收，避免 close 与 kill 重复调用。
func (p *sessionProc) releaseCleanup() {
	p.cleanupOnce.Do(func() {
		if p.cleanup != nil {
			p.cleanup()
		}
	})
}

func (p *sessionProc) kill() {
	p.killOnce.Do(func() {
		// 连接先断开时 markDead 已关闭 deadCh，但进程可能仍存活（如帧超限），
		// 因此无论 deadCh 是否已关，都必须发 SIGKILL，否则下面的 Wait 会一直阻塞。
		_ = p.stdin.Close()
		_ = killProcessGroup(p.cmd, syscall.SIGKILL)
		p.markDead()
		if p.conn != nil {
			_ = p.conn.Close()
		}
		p.wait()
		p.releaseCleanup()
	})
}

// close 先请服务端 runtime.shutdown，让 CLI 落盘会话事件日志，再关闭连接与进程。
func (p *sessionProc) close() error {
	p.markDead()

	if p.conn != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = p.conn.Call(shutdownCtx, "runtime.shutdown", nil, nil)
		cancel()
		_ = p.conn.Close()
	}
	_ = p.stdin.Close()

	waitDone := p.doneCh()
	go p.wait()

	select {
	case <-waitDone:
	case <-time.After(500 * time.Millisecond):
		_ = killProcessGroup(p.cmd, syscall.SIGKILL)
		<-waitDone
	}

	p.releaseCleanup()
	return nil
}

// handleNotification 是常驻连接的会话事件入口；无活跃轮次时忽略。
func (p *sessionProc) handleNotification(method string, params json.RawMessage) {
	if method != "session.event" {
		return
	}
	at := p.getActiveTurn()
	if at == nil {
		return
	}
	at.handleEvent(params)
}

// handleServerRequest 是常驻连接的服务端请求入口，交由当前活跃轮次的处理器原地答复。
func (p *sessionProc) handleServerRequest(method string, params json.RawMessage) (any, error) {
	at := p.getActiveTurn()
	if at == nil {
		return nil, &RPCError{Code: -32601, Message: fmt.Sprintf("no active turn to answer %s", method)}
	}
	return at.handleServerRequest(method, params)
}

// handleConnClosed 在常驻连接断开或子进程退出时收敛当前轮次并标记进程已死。
func (p *sessionProc) handleConnClosed(err error) {
	p.markDead()
	if at := p.getActiveTurn(); at != nil {
		at.abortByClose(err)
	}
}

// tailBuffer 是一个保留末尾至多指定字节大小的缓冲区。
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

func (b *tailBuffer) String() string { return string(b.buf) }

// buildEnv 构造子进程环境变量列表：剔除继承的 PWD，注入 workDir，最后追加 extra（turn.Env 优先级最高）。
func buildEnv(base []string, workDir string, extra []string) []string {
	env := make([]string, 0, len(base)+1+len(extra))
	for _, e := range base {
		if !strings.HasPrefix(e, "PWD=") {
			env = append(env, e)
		}
	}
	if workDir != "" {
		env = append(env, "PWD="+workDir)
	}
	env = append(env, extra...)
	return env
}

// newSessionID 生成小写 UUID（v4）。
func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("mcode-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// permissionModeName 将 harness.PermissionMode 映射为 CLI 自己的权限模式名（manual/assisted/allow-all）。
// 对照 Copilot CLI 自身的规则：supervised 使用 manual，auto 使用 assisted，
// 需要审批的请求交回应用自身的规则裁决。
func permissionModeName(mode harness.PermissionMode) string {
	switch mode {
	case harness.ModeAutoAcceptEdits, harness.ModeAuto:
		return "assisted"
	case harness.ModeFullAccess:
		return "allow-all"
	default:
		return "manual"
	}
}

// copilotArguments 返回启动 headless server 的固定参数。
func copilotArguments(extra []string) []string {
	args := []string{"--headless", "--stdio", "--no-auto-update", "--log-level", "error"}
	return append(args, extra...)
}

// mcpServerConfig 是 `--additional-mcp-config` 中单个服务的条目。
type mcpServerConfig struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Tools   []string          `json:"tools"`
}

// mcpConfig 是 `--additional-mcp-config` 的顶层结构。
type mcpConfig struct {
	MCPServers map[string]mcpServerConfig `json:"mcpServers"`
}

// mcpArgs 返回本轮注入 MCP 服务所需的启动参数；无服务时返回 nil。
func (h *Harness) mcpArgs(turn harness.Turn) []string {
	var external []harness.MCPEndpoint
	if h.opts.ExternalMCP != nil {
		external = h.opts.ExternalMCP(turn.WorkDir, turn.ThreadID)
	}
	return buildMCPArgs(turn, h.opts.BuiltinMCP, external)
}

// buildMCPArgs 构造 `--additional-mcp-config <json>`：内置服务名为 mcode，外部端点一并写入，
// 每个条目都是 http 加 Authorization 头，并放开全部工具（tools:["*"]）。
// 这里返回的是内联 JSON，启动子进程前由 externalizeMCPConfig 换成 0600 临时文件的路径（避免令牌进 argv）。
func buildMCPArgs(turn harness.Turn, builtin harness.BuiltinMCP, external []harness.MCPEndpoint) []string {
	servers := make(map[string]mcpServerConfig)

	if url, token, workdirHeader := builtin.Endpoint(turn); url != "" {
		headers := make(map[string]string)
		if token != "" {
			headers["Authorization"] = "Bearer " + token
		}
		if workdirHeader {
			headers["X-Mcode-Workdir"] = turn.WorkDir
		}
		servers["mcode"] = mcpServerConfig{
			Type:    "http",
			URL:     url,
			Headers: headers,
			Tools:   []string{"*"},
		}
	}

	for _, ep := range external {
		if ep.Name == "mcode" || ep.Name == "mcode_approval" {
			continue
		}
		headers := make(map[string]string)
		if ep.Token != "" {
			headers["Authorization"] = "Bearer " + ep.Token
		}
		servers[ep.Name] = mcpServerConfig{
			Type:    "http",
			URL:     ep.URL,
			Headers: headers,
			Tools:   []string{"*"},
		}
	}

	if len(servers) == 0 {
		return nil
	}
	data, err := json.Marshal(mcpConfig{MCPServers: servers})
	if err != nil {
		return nil
	}
	return []string{"--additional-mcp-config", string(data)}
}

// sessionParameters 构造 session.create / session.resume 的公共参数。
func (h *Harness) sessionParameters(turn harness.Turn, workDir string) map[string]any {
	params := map[string]any{
		"clientName":                     "mcode",
		"workingDirectory":               workDir,
		"streaming":                      true,
		"requestPermission":              true,
		"requestUserInput":               true,
		"requestExitPlanMode":            true,
		"includeSubAgentStreamingEvents": false,
		"isExperimentalMode":             turn.Mode == harness.ModeAuto,
		"systemMessage": map[string]any{
			"mode":    "append",
			"content": h.opts.SystemMessage,
		},
	}
	// "default" 是模型目录的占位行，交给 copilot 自己的默认模型，不传 model。
	if turn.Model != "" && turn.Model != "default" {
		params["model"] = turn.Model
	}
	if turn.Effort != "" {
		params["reasoningEffort"] = turn.Effort
	}
	return params
}

// acquire 取用指定会话的常驻进程；不存在或已死时新起一个。第二个返回值表示该进程尚未建立会话。
func (h *Harness) acquire(ctx context.Context, turn harness.Turn) (*sessionProc, bool, error) {
	if turn.SessionID != "" {
		if p := h.lookup(turn.SessionID); p != nil {
			return p, false, nil
		}
	}
	proc, err := h.startProcess(ctx, turn)
	if err != nil {
		return nil, false, err
	}
	return proc, true, nil
}

// lookup 返回指定会话当前可用的常驻进程。
func (h *Harness) lookup(sessionID string) *sessionProc {
	if sessionID == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.sessions[sessionID]
	if p == nil || p.isDead() {
		return nil
	}
	return p
}

// register 把常驻进程登记到会话 id 下。
func (h *Harness) register(sessionID string, proc *sessionProc) {
	if sessionID == "" || proc == nil {
		return
	}
	h.mu.Lock()
	h.sessions[sessionID] = proc
	h.mu.Unlock()
}

// drop 把常驻进程从会话表中移除并回收。
func (h *Harness) drop(proc *sessionProc) {
	if proc == nil {
		return
	}
	h.mu.Lock()
	for k, p := range h.sessions {
		if p == proc {
			delete(h.sessions, k)
		}
	}
	h.mu.Unlock()
	proc.kill()
}

// Close 释放并终止所有常驻子进程，可重复调用。
func (h *Harness) Close() error {
	h.mu.Lock()
	procs := make([]*sessionProc, 0, len(h.sessions))
	for _, p := range h.sessions {
		procs = append(procs, p)
	}
	h.sessions = make(map[string]*sessionProc)
	h.mu.Unlock()

	for _, p := range procs {
		if p != nil {
			_ = p.close()
		}
	}
	return nil
}

// startProcess 拉起一个常驻 `copilot --headless --stdio` 进程并完成 ping 握手。
func (h *Harness) startProcess(ctx context.Context, turn harness.Turn) (*sessionProc, error) {
	bin := h.opts.Binary
	if bin == "" {
		bin = DefaultBinary()
	}
	args := copilotArguments(append(h.mcpArgs(turn), h.opts.ExtraArgs...))

	// --additional-mcp-config 的内联 JSON 里有 MCP 的 Bearer 令牌，放在 argv 里同机其他用户用 ps
	// 就能看到；改成写 0600 临时文件、argv 只传 "@<路径>"，本轮子进程退出后删除。
	args, cleanupMCPConfig, err := externalizeMCPConfig(args)
	if err != nil {
		return nil, err
	}

	var cmd *exec.Cmd
	var sandboxCleanup func()
	if turn.Sandbox != nil {
		cmd, sandboxCleanup, err = turn.Sandbox.Command(context.Background(), turn.WorkDir, turn.Mode, bin, args...)
		if err != nil {
			cleanupMCPConfig()
			return nil, fmt.Errorf("copilot: sandbox command: %w", err)
		}
	} else {
		cmd = procattr.HideWindow(exec.CommandContext(context.Background(), bin, args...))
	}

	// cleanup 把沙箱回收与临时文件删除合并成一处：sessionProc 在子进程 Wait 结束后只调用一次。
	cleanup := cleanupMCPConfig
	if sandboxCleanup != nil {
		cleanup = func() {
			sandboxCleanup()
			cleanupMCPConfig()
		}
	}

	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd, syscall.SIGTERM) }
	cmd.WaitDelay = 3 * time.Second

	baseEnv := loginenv.Environ()
	if cmd.Env != nil {
		baseEnv = cmd.Env
	}
	cmd.Dir = turn.WorkDir
	cmd.Env = buildEnv(baseEnv, turn.WorkDir, h.opts.envFor(turn))

	stdin, err := cmd.StdinPipe()
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, fmt.Errorf("copilot: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		if cleanup != nil {
			cleanup()
		}
		return nil, fmt.Errorf("copilot: stdout pipe: %w", err)
	}

	stderrBuf := &tailBuffer{limit: 8192}
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		if cleanup != nil {
			cleanup()
		}
		return nil, fmt.Errorf("copilot: start: %w", err)
	}

	conn := NewConn(stdout, stdin)
	proc := &sessionProc{
		cmd:             cmd,
		stdin:           stdin,
		conn:            conn,
		stderrBuf:       stderrBuf,
		workDir:         turn.WorkDir,
		cleanup:         cleanup,
		deadCh:          make(chan struct{}),
		permissionMode:  "manual",
		interactionMode: "interactive",
	}

	conn.SetNotificationHandler(proc.handleNotification)
	conn.SetRequestHandler(proc.handleServerRequest)
	conn.SetCloseHandler(proc.handleConnClosed)

	// ping 握手：同时校验对端确实说 SDK 协议。
	var pong struct {
		ProtocolVersion *int `json:"protocolVersion"`
	}
	if err := conn.Call(ctx, "ping", map[string]any{"message": "mcode"}, &pong); err != nil {
		proc.kill()
		return nil, fmt.Errorf("copilot: ping failed: %w", err)
	}
	if pong.ProtocolVersion == nil {
		proc.kill()
		return nil, fmt.Errorf("copilot: this `copilot` does not speak the SDK protocol; update it with `copilot update`")
	}

	return proc, nil
}

// envFor 组合选项级 Env 与轮次级 Env（轮次在后，优先级更高）。
func (o Options) envFor(turn harness.Turn) []string {
	env := make([]string, 0, len(o.Env)+len(turn.Env))
	env = append(env, o.Env...)
	env = append(env, turn.Env...)
	return env
}

// ensureSession 按 mcode 的会话 id 创建或恢复会话。
func (h *Harness) ensureSession(ctx context.Context, proc *sessionProc, turn harness.Turn) error {
	id := turn.SessionID
	if id == "" {
		id = newSessionID()
	}

	params := h.sessionParameters(turn, proc.workDir)
	params["sessionId"] = id

	if turn.SessionID != "" {
		if err := proc.call(ctx, "session.resume", params, nil); err == nil {
			proc.setSessionID(id)
			return nil
		}
		// 恢复失败（CLI 已不再持有该 id）时按同一 id 新建，保持线程可继续。
		if err := proc.call(ctx, "session.create", params, nil); err != nil {
			return fmt.Errorf("copilot: session.create failed: %w", err)
		}
		proc.setSessionID(id)
		return nil
	}

	if err := proc.call(ctx, "session.create", params, nil); err != nil {
		return fmt.Errorf("copilot: session.create failed: %w", err)
	}
	proc.setSessionID(id)
	return nil
}

// applyModes 下发运行时权限模式与交互模式。
func (p *sessionProc) applyModes(ctx context.Context, turn harness.Turn) {
	sessionID := p.currentSessionID()
	if sessionID == "" {
		return
	}

	p.setPermissionMode(ctx, permissionModeName(turn.Mode))

	interaction := "interactive"
	if turn.InteractionMode == "plan" {
		interaction = "plan"
	}
	p.stateMu.Lock()
	currentInteraction := p.interactionMode
	p.stateMu.Unlock()
	if interaction != currentInteraction {
		if err := p.call(ctx, "session.mode.set", map[string]any{
			"sessionId": sessionID,
			"mode":      interaction,
		}, nil); err == nil {
			p.stateMu.Lock()
			p.interactionMode = interaction
			p.stateMu.Unlock()
		}
	}
}

// setPermissionMode 下发运行时权限模式，与当前记录相同时跳过。
func (p *sessionProc) setPermissionMode(ctx context.Context, wanted string) {
	sessionID := p.currentSessionID()
	if sessionID == "" {
		return
	}
	p.stateMu.Lock()
	current := p.permissionMode
	p.stateMu.Unlock()
	if wanted == current {
		return
	}
	var res struct {
		Mode string `json:"mode"`
	}
	if err := p.call(ctx, "session.permissions.setMode", map[string]any{
		"sessionId": sessionID,
		"mode":      wanted,
		"source":    "rpc",
	}, &res); err == nil {
		got := res.Mode
		if got == "" {
			got = wanted
		}
		p.stateMu.Lock()
		p.permissionMode = got
		p.stateMu.Unlock()
	}
}

// applySelection 下发模型与推理强度。
func (p *sessionProc) applySelection(ctx context.Context, turn harness.Turn) {
	sessionID := p.currentSessionID()
	if sessionID == "" {
		return
	}

	p.stateMu.Lock()
	currentModel, currentEffort := p.model, p.effort
	p.stateMu.Unlock()

	if turn.Model != "" && turn.Model != "default" && turn.Model != currentModel {
		params := map[string]any{"sessionId": sessionID, "modelId": turn.Model}
		if turn.Effort != "" {
			params["reasoningEffort"] = turn.Effort
		}
		if err := p.call(ctx, "session.model.switchTo", params, nil); err == nil {
			p.stateMu.Lock()
			p.model = turn.Model
			if turn.Effort != "" {
				p.effort = turn.Effort
			}
			currentEffort = p.effort
			p.stateMu.Unlock()
		}
	}

	if turn.Effort != "" && turn.Effort != currentEffort {
		if err := p.call(ctx, "session.model.setReasoningEffort", map[string]any{
			"sessionId":       sessionID,
			"reasoningEffort": turn.Effort,
		}, nil); err == nil {
			p.stateMu.Lock()
			p.effort = turn.Effort
			p.stateMu.Unlock()
		}
	}
}

// sendPrompt 以 `session.send` 下发一轮提示。
func (p *sessionProc) sendPrompt(ctx context.Context, turn harness.Turn) error {
	sessionID := p.currentSessionID()
	if sessionID == "" {
		return harness.ErrNotRunning
	}

	params := map[string]any{
		"sessionId": sessionID,
		"prompt":    turn.Prompt,
	}
	attachments := make([]any, 0, len(turn.Images))
	for _, img := range turn.Images {
		// 非图片附件（PDF、docx 等）不当图片下发，其路径已由上层写进 prompt。
		// 图片类型由 harness.ImageMediaType 判定：扩展名优先，扩展名不可靠时按文件内容嗅探。
		mediaType, ok := harness.ImageMediaType(img)
		if !ok {
			log.Printf("copilot: skip image %s: unsupported image type %q", img, filepath.Ext(img))
			continue
		}
		attachments = append(attachments, map[string]any{
			"type":        "file",
			"path":        img,
			"displayName": filepath.Base(img),
			"mimeType":    mediaType,
		})
	}
	if len(attachments) > 0 {
		params["attachments"] = attachments
	}

	if err := p.call(ctx, "session.send", params, nil); err != nil {
		return fmt.Errorf("copilot: session.send failed: %w", err)
	}
	return nil
}

// abort 请求 CLI 中断当前轮次。
func (p *sessionProc) abort(ctx context.Context, reason string) {
	sessionID := p.currentSessionID()
	if sessionID == "" {
		return
	}
	_ = p.call(ctx, "session.abort", map[string]any{
		"sessionId": sessionID,
		"reason":    reason,
	}, nil)
}

// Run 执行一轮 Copilot 交互。
func (h *Harness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	proc, fresh, err := h.acquire(ctx, turn)
	if err != nil {
		return err
	}
	// 常驻 CLI 进程在本轮期间就是该会话的 agent 进程；本轮结束后清理记录。
	if proc.cmd != nil && proc.cmd.Process != nil {
		agentPID := proc.cmd.Process.Pid
		h.roots.Set(turn.ThreadID, agentPID)
		defer h.roots.Clear(turn.ThreadID, agentPID)
	}

	handler := newEventHandler(proc, turn, emit, ctx)
	proc.setActiveTurn(handler)
	defer proc.setActiveTurn(nil)

	if fresh {
		if err := h.ensureSession(ctx, proc, turn); err != nil {
			// 建会话失败时进程仍可能活着，必须无条件回收，否则每失败一次就漏一个常驻 CLI。
			h.drop(proc)
			return err
		}
	}
	h.register(proc.currentSessionID(), proc)

	proc.applyModes(ctx, turn)
	proc.applySelection(ctx, turn)

	if sessionID := proc.currentSessionID(); sessionID != "" {
		_ = harness.Emit(emit, harness.Event{
			Type:      harness.EventSession,
			SessionID: sessionID,
		})
	}

	handler.beginTurn()
	if err := proc.sendPrompt(ctx, turn); err != nil {
		// 同上：发送失败时进程可能仍存活，无条件回收避免泄漏常驻进程。
		h.drop(proc)
		return err
	}

	select {
	case <-handler.turnDoneCh():
		if !handler.turnFinishedOK() {
			tail := strings.TrimSpace(proc.stderrBuf.String())
			errMsg := tail
			if errMsg == "" {
				errMsg = "copilot process closed connection unexpectedly"
			}
			_ = harness.Emit(emit, harness.Event{Type: harness.EventError, Err: errMsg})
			h.drop(proc)
			return fmt.Errorf("copilot turn incomplete (stderr: %s)", tail)
		}
		return nil

	case <-ctx.Done():
		if handler.isTurnActive() {
			abortCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			proc.abort(abortCtx, "user_initiated")
			cancel()
		}
		select {
		case <-handler.turnDoneCh():
			return ctx.Err()
		case <-time.After(1500 * time.Millisecond):
			h.drop(proc)
			return ctx.Err()
		}
	}
}

// ForceStopTurn 仅强停指定轮次对应的常驻进程，不影响其他会话。
func (h *Harness) ForceStopTurn(turnID string) {
	if turnID == "" {
		return
	}
	h.mu.Lock()
	var targets []*sessionProc
	for _, p := range h.sessions {
		if at := p.getActiveTurn(); at != nil && at.turnID() == turnID {
			targets = append(targets, p)
		}
	}
	h.mu.Unlock()

	for _, p := range targets {
		p.kill()
	}
}

// Rollback 按 rewind 点做 Copilot 原生回滚，丢弃最近 turns 个轮次。
// 文件不回滚：由 mcode 自己的检查点负责恢复。
// 会话不在运行时返回 harness.ErrNotRunning。
func (h *Harness) Rollback(ctx context.Context, sessionID string, turns int) error {
	proc := h.lookup(sessionID)
	if proc == nil {
		return harness.ErrNotRunning
	}
	if turns <= 0 {
		return nil
	}

	var listed struct {
		Points []struct {
			EventID string `json:"eventId"`
		} `json:"points"`
	}
	if err := proc.call(ctx, "session.history.listRewindPoints", map[string]any{
		"sessionId": sessionID,
	}, &listed); err != nil {
		return fmt.Errorf("copilot: session.history.listRewindPoints failed: %w", err)
	}

	points := listed.Points
	if len(points) < turns {
		return fmt.Errorf("copilot: no rewind point for that turn")
	}
	eventID := points[len(points)-turns].EventID
	if eventID == "" {
		return fmt.Errorf("copilot: no rewind point for that turn")
	}

	var result struct {
		Outcome string `json:"outcome"`
		Error   string `json:"error"`
	}
	if err := proc.call(ctx, "session.history.rewind", map[string]any{
		"sessionId": sessionID,
		"eventId":   eventID,
		"mode":      "conversation",
	}, &result); err != nil {
		return fmt.Errorf("copilot: session.history.rewind failed: %w", err)
	}
	if result.Outcome != "success" {
		msg := result.Error
		if msg == "" {
			msg = "Copilot could not rewind the conversation."
		}
		return fmt.Errorf("copilot: %s", msg)
	}
	return nil
}

// Compact 触发一次手动上下文压缩。
func (h *Harness) Compact(ctx context.Context, sessionID string) error {
	proc := h.lookup(sessionID)
	if proc == nil {
		return harness.ErrNotRunning
	}
	if err := proc.call(ctx, "session.history.compact", map[string]any{
		"sessionId": sessionID,
		"trigger":   "manual",
	}, nil); err != nil {
		return fmt.Errorf("copilot: session.history.compact failed: %w", err)
	}
	return nil
}

// ModelOption 描述一个可选模型。
type ModelOption struct {
	ID            string
	Name          string
	Detail        string
	Efforts       []string
	DefaultEffort string
}

// AutoModelID 是 Copilot 的自动选模型条目 id。
const AutoModelID = "auto"

// ListModels 启动一次临时连接并调用 `models.list`，返回账号可用的模型目录。
func ListModels(ctx context.Context, binary string, env []string) ([]ModelOption, error) {
	conn, cmd, err := connect(ctx, binary, env)
	if err != nil {
		return nil, err
	}
	defer closeConnection(conn, cmd)

	var result struct {
		Models []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Policy struct {
				State string `json:"state"`
			} `json:"policy"`
			Billing struct {
				Multiplier *float64 `json:"multiplier"`
			} `json:"billing"`
			SupportedReasoningEfforts []string `json:"supportedReasoningEfforts"`
			DefaultReasoningEffort    string   `json:"defaultReasoningEffort"`
		} `json:"models"`
	}
	if err := conn.Call(ctx, "models.list", map[string]any{}, &result); err != nil {
		return nil, fmt.Errorf("copilot: models.list failed: %w", err)
	}

	models := []ModelOption{{
		ID:     AutoModelID,
		Name:   "Auto",
		Detail: "Copilot 为每次请求自动选择模型",
	}}
	for _, m := range result.Models {
		if m.ID == "" || m.Policy.State == "disabled" {
			continue
		}
		var details []string
		if m.Billing.Multiplier != nil {
			v := *m.Billing.Multiplier
			if v == float64(int64(v)) {
				details = append(details, fmt.Sprintf("%d× premium requests", int64(v)))
			} else {
				details = append(details, fmt.Sprintf("%g× premium requests", v))
			}
		}
		if m.Policy.State == "unconfigured" {
			details = append(details, "Enable it in your GitHub Copilot settings first")
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		models = append(models, ModelOption{
			ID:            m.ID,
			Name:          name,
			Detail:        strings.Join(details, " · "),
			Efforts:       m.SupportedReasoningEfforts,
			DefaultEffort: m.DefaultReasoningEffort,
		})
	}
	return models, nil
}

// connect 在临时目录启动一次短生命周期的 copilot 连接并完成 ping 握手。
// 调用方负责 closeConnection。
func connect(ctx context.Context, binary string, env []string) (*Conn, *exec.Cmd, error) {
	if binary == "" {
		binary = DefaultBinary()
	}
	binPath, err := loginenv.LookPath(binary)
	if err != nil {
		binPath, err = exec.LookPath(binary)
		if err != nil {
			return nil, nil, fmt.Errorf("copilot: binary %q not found: %w", binary, err)
		}
	}

	dir := os.TempDir()
	cmd := procattr.HideWindow(exec.CommandContext(ctx, binPath, copilotArguments(nil)...))
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd, syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = dir
	cmd.Env = buildEnv(loginenv.Environ(), dir, env)
	cmd.Stderr = io.Discard

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("copilot: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, nil, fmt.Errorf("copilot: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, nil, fmt.Errorf("copilot: start %q: %w", binPath, err)
	}

	conn := NewConn(stdout, stdin)
	var pong struct {
		ProtocolVersion *int `json:"protocolVersion"`
	}
	if err := conn.Call(ctx, "ping", map[string]any{"message": "mcode"}, &pong); err != nil {
		closeConnection(conn, cmd)
		return nil, nil, fmt.Errorf("copilot: ping failed: %w", err)
	}
	if pong.ProtocolVersion == nil {
		closeConnection(conn, cmd)
		return nil, nil, fmt.Errorf("copilot: this `copilot` does not speak the SDK protocol; update it with `copilot update`")
	}
	return conn, cmd, nil
}

// closeConnection 关闭临时连接并回收子进程。
func closeConnection(conn *Conn, cmd *exec.Cmd) {
	if conn != nil {
		_ = conn.Close()
	}
	if cmd != nil {
		_ = killProcessGroup(cmd, syscall.SIGKILL)
		_ = cmd.Wait()
	}
}
