// Package codex 实现了基于 OpenAI Codex CLI `codex app-server` 的 harness.Harness 适配器。
//
// 协议契约与版本说明：
// 本适配器基于本机 codex-cli 0.165.0 导出的 app-server JSON-RPC 2.0 规范实现。
//
// 性能优化与架构设计：
//  1. 常驻子进程池：每个 Harness 实例按 (workDir, sandbox, 路由指纹) 维护一组常驻的 `codex app-server` 子进程，
//     跨会话与跨轮次复用，消除每轮 fork/exec 进程冷启动与 initialize/initialized 握手开销；
//     不同项目的会话各持独立进程（切换项目不再杀进程重起），同一项目的并发会话也互不阻塞。
//  2. 会话级线程缓存：按 threadId 缓存活跃线程，后续轮次直接发起 turn/start，免去重复 session 加载。
//  3. 增量恢复轻量化：对于未缓存线程调用 thread/resume 时注入 `excludeTurns: true`，
//     避免反序列化与传输庞大的全量历史 turns，将 resume 耗时由秒级降至毫秒级。
//  4. 精确安全中断：ctx 取消时优先调用 `turn/interrupt` 优雅中断当轮；若子进程超时未响应才强制
//     终止进程组并自愈重建，兼顾响应速度与资源回收。
//  5. 审批与流式事件管道：保持全量强类型事件校验、增量去重及服务端审批请求应答通道，语义完全兼容。
//  6. 原生能力直通：图片以 UserInput.localImage 项随 turn/start 下发；plan 模式带 collaborationMode；
//     `/compact`、`/review` 改发 thread/compact/start 与 review/start，交由 app-server 原生处理。
//  7. 启动阶段有限重试：thread/*、turn/start 等请求失败或进程在产出事件前退出时，丢弃进程重建重试。
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"mcode/internal/harness"
	"mcode/internal/magpieinject"
	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

var _ harness.Harness = (*Harness)(nil)
var _ harness.Steerer = (*Harness)(nil)
var _ harness.ThreadEvictor = (*Harness)(nil)

// Options 包含 Codex 适配器的配置选项。
type Options struct {
	Binary      string
	ExtraArgs   []string
	BuiltinMCP  harness.BuiltinMCP
	ExternalMCP func(workDir, threadID string) []harness.MCPEndpoint
	// BlockedMCP 返回需在线程配置里强制关闭的 MCP 服务名（仓库内未信任的 codex 服务）。
	BlockedMCP func(workDir string) []string
	// CodexHome 非空时以 CODEX_HOME 启动 app-server，用于多账号切换。
	CodexHome string
	// MagpieRoutes 返回临时注入的网关路由；nil 表示未启用。
	MagpieRoutes func() []MagpieRoute
}

// maxIdleProcsPerKey 同一 (workDir, sandbox, 路由指纹) 组合下允许保留的空闲常驻进程上限。
const maxIdleProcsPerKey = 2

// codexRunMaxAttempts 是一轮对话允许的最大尝试次数：首次尝试加最多 2 次重试。
const codexRunMaxAttempts = 3

// defaultRetryBackoffs 是第 1、2 次重试前的退避时长；首次尝试不退避（相当于 0）。
// 与 codexRunMaxAttempts 对应：启动阶段最多 3 次尝试，退避序列为 0、1s、2s。
var defaultRetryBackoffs = []time.Duration{time.Second, 2 * time.Second}

// Harness 实现了基于 Codex app-server 的 harness.Harness 接口。
type Harness struct {
	opts Options

	// mu 保护 procs；residentProcess.busy 同样由该锁保护。
	mu    sync.Mutex
	procs []*residentProcess

	// roots 按会话记录本轮使用的 app-server 常驻进程 PID，供 ProcessRootPIDs 使用。
	roots harness.ProcessRoots

	// versionMu 保护 serverVersion 与 versionLogged：记录 initialize 握手返回的版本信息，只打一次性日志。
	versionMu     sync.Mutex
	serverVersion string
	versionLogged bool

	// retryBackoffs 与 retrySleep 只用于注入重试退避序列与等待实现（测试跳过真实等待），为空时取默认值。
	retryBackoffs []time.Duration
	retrySleep    func(ctx context.Context, d time.Duration) error
}

// New 创建一个新的 Codex Harness 实例。
func New(opts Options) *Harness {
	return &Harness{opts: opts}
}

// Kind 返回后端的种类标识。
func (h *Harness) Kind() harness.Kind {
	return harness.Kind("codex")
}

// CatalogBinary 返回当前运行时配置的 CLI。
func (h *Harness) CatalogBinary() string { return h.opts.Binary }

// ConfirmsApprovalDelivery 返回 true，表示该 Provider 对每次 Approve/Interact 的答复都会调用 DeliveryAcker.AckDelivery。
func (h *Harness) ConfirmsApprovalDelivery() bool {
	return true
}

// ProcessRootPIDs 返回指定会话本轮使用的 app-server 进程 PID，未在运行时返回 nil。
func (h *Harness) ProcessRootPIDs(threadID string) []int {
	return h.roots.PIDs(threadID)
}

// Close 释放并终止池中所有常驻的 Codex app-server 子进程，可重复调用。
func (h *Harness) Close() error {
	h.mu.Lock()
	procs := h.procs
	h.procs = nil
	h.mu.Unlock()

	var firstErr error
	for _, proc := range procs {
		if proc == nil {
			continue
		}
		if err := proc.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ServerVersion 返回 initialize 握手时 app-server 上报的版本标识（userAgent），未握手时为空。
// 仅作记录与观测，不参与任何能力门控。
func (h *Harness) ServerVersion() string {
	h.versionMu.Lock()
	defer h.versionMu.Unlock()
	return h.serverVersion
}

// recordServerVersion 记录握手返回的版本信息，并在首次（或发生变化时）输出一次日志。
func (h *Harness) recordServerVersion(userAgent, platformOS, platformFamily, codexHome string) {
	if userAgent == "" {
		return
	}
	h.versionMu.Lock()
	changed := h.serverVersion != userAgent
	if changed {
		h.serverVersion = userAgent
	}
	logOnce := changed || !h.versionLogged
	h.versionLogged = true
	h.versionMu.Unlock()

	if logOnce {
		log.Printf("codex: app-server %s (os=%s family=%s codexHome=%s)", userAgent, platformOS, platformFamily, codexHome)
	}
}

// EvictThread 关闭并移出持有该会话的空闲常驻进程，供会话或项目删除时立即回收内存。
// threadID 既可以是 codex 的 threadId，也可以是上层 mcode 会话 ID（harness.Turn.ThreadID）。
// 正在执行的轮次不受影响：busy 进程即使也缓存了该会话也不动它。
func (h *Harness) EvictThread(threadID string) {
	if threadID == "" {
		return
	}

	var victims []*residentProcess
	h.mu.Lock()
	kept := make([]*residentProcess, 0, len(h.procs))
	for _, p := range h.procs {
		if p == nil {
			continue
		}
		if !p.busy && (p.hasCachedThread(threadID) || p.servesMcodeThread(threadID)) {
			victims = append(victims, p)
			continue
		}
		kept = append(kept, p)
	}
	h.procs = kept
	h.mu.Unlock()

	for _, p := range victims {
		_ = p.close()
	}
}

// Steer 向指定会话正在运行的轮次注入用户指引。
// 若会话当前未在运行或找不到对应的 activeTurn，返回 harness.ErrNotRunning。
func (h *Harness) Steer(ctx context.Context, sessionID string, text string) error {
	if sessionID == "" {
		return harness.ErrNotRunning
	}

	var targetProc *residentProcess
	var targetTurnID string

	h.mu.Lock()
	for _, p := range h.procs {
		if p == nil || p.isDead() || !p.busy {
			continue
		}
		at := p.getActiveTurn()
		if at != nil {
			thID, tuID := at.ids()
			if thID == sessionID && tuID != "" {
				targetProc = p
				targetTurnID = tuID
				break
			}
		}
	}
	h.mu.Unlock()

	if targetProc == nil {
		return harness.ErrNotRunning
	}

	params := TurnSteerParams{
		ThreadID:       sessionID,
		ExpectedTurnID: targetTurnID,
		Input: []UserInputItem{
			{
				Type: "text",
				Text: text,
			},
		},
	}

	_, err := targetProc.client.call(ctx, "turn/steer", params)
	if err != nil {
		return fmt.Errorf("codex: turn/steer failed: %w", err)
	}
	return nil
}

func (h *Harness) ForceStopTurn(turnID string) {
	h.mu.Lock()
	var targets []*residentProcess
	for _, p := range h.procs {
		if at := p.getActiveTurn(); at != nil && at.turn.ID == turnID {
			targets = append(targets, p)
		}
	}
	h.mu.Unlock()
	for _, p := range targets {
		p.deadOnce.Do(func() {
			_ = p.stdin.Close()
			_ = killProcessGroup(p.cmd, syscall.SIGKILL)
			close(p.deadCh)
		})
	}
}

// buildEnv 构造子进程环境变量列表：
// 1. 从 base 中剔除所有以 "PWD=" 开头的项，避免原样继承父进程的环境变量 PWD；
// 2. 当 workDir 非空时追加 "PWD=" + workDir，保证子进程 PWD 与工作目录一致；
// 3. 最后追加 extra（即 turn.Env）。如果 turn.Env 中自带了 PWD，由于追加在末尾，其优先级高于 workDir 注入的 PWD。
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

func (b *tailBuffer) String() string {
	return string(b.buf)
}

// appClient 管理与 codex app-server 子进程的 JSON-RPC 2.0 双向通信。
type appClient struct {
	stdin        io.WriteCloser
	wmu          sync.Mutex
	mu           sync.Mutex
	nextID       int
	pending      map[string]chan *JSONRPCMessage
	readerDoneCh chan struct{}
}

func newAppClient(stdin io.WriteCloser) *appClient {
	return &appClient{
		stdin:        stdin,
		pending:      make(map[string]chan *JSONRPCMessage),
		readerDoneCh: make(chan struct{}),
	}
}

func (c *appClient) send(msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.stdin.Write(data)
	return err
}

func (c *appClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	idStr := strconv.Itoa(id)
	idRaw := json.RawMessage(idStr)
	ch := make(chan *JSONRPCMessage, 1)
	c.pending[idStr] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, idStr)
		c.mu.Unlock()
	}()

	var paramsRaw json.RawMessage
	if params != nil {
		pData, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("codex: marshal params: %w", err)
		}
		paramsRaw = pData
	}

	req := JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      idRaw,
		Method:  method,
		Params:  paramsRaw,
	}

	if err := c.send(req); err != nil {
		return nil, fmt.Errorf("codex: send %s: %w", method, err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res, ok := <-ch:
		if !ok || res == nil {
			return nil, fmt.Errorf("codex: connection closed before %s answered", method)
		}
		if res.Error != nil {
			return nil, fmt.Errorf("codex: %s error (code %d): %s", method, res.Error.Code, res.Error.Message)
		}
		return res.Result, nil
	}
}

func (c *appClient) notify(method string, params any) error {
	var paramsRaw json.RawMessage
	if params != nil {
		pData, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("codex: marshal notify params: %w", err)
		}
		paramsRaw = pData
	}
	notif := JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  paramsRaw,
	}
	return c.send(notif)
}

// residentProcess 代表一个常驻的 Codex app-server 子进程。
type residentProcess struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	client    *appClient
	stderrBuf *tailBuffer
	workDir   string
	sandbox   harness.Sandbox
	routeKey  string
	cleanup   func()

	deadCh   chan struct{}
	deadOnce sync.Once

	cachedMu      sync.Mutex
	cachedThreads map[string]harness.PermissionMode

	// mcodeThreads 记录该进程服务过的上层会话（harness.Turn.ThreadID）：常驻进程按 codex 的
	// threadId 复用，与上层会话 ID 并不相同，EvictThread 两个键都要认。
	mcodeMu      sync.Mutex
	mcodeThreads map[string]struct{}

	// busy 标记该进程是否正在执行一轮，由 Harness.mu 保护。
	busy bool

	turnMu     sync.Mutex
	atMu       sync.RWMutex
	activeTurn *activeTurnContext
}

// hasCachedThread 判断该进程是否已缓存指定会话线程；空 ID 不匹配任何进程。
func (p *residentProcess) hasCachedThread(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	p.cachedMu.Lock()
	defer p.cachedMu.Unlock()
	_, ok := p.cachedThreads[sessionID]
	return ok
}

// maxMcodeThreadsPerProc 限制单个常驻进程记录的上层会话数量：常驻进程可能服务很多会话，
// 超过上限就整体重置（丢弃最旧的记录），不影响正确性，只降低回收的命中率。
const maxMcodeThreadsPerProc = 64

// noteMcodeThread 记录该进程服务过的上层会话 ID，供 EvictThread 按上层会话回收进程。
func (p *residentProcess) noteMcodeThread(threadID string) {
	if threadID == "" {
		return
	}
	p.mcodeMu.Lock()
	if p.mcodeThreads == nil || len(p.mcodeThreads) >= maxMcodeThreadsPerProc {
		p.mcodeThreads = make(map[string]struct{}, 1)
	}
	p.mcodeThreads[threadID] = struct{}{}
	p.mcodeMu.Unlock()
}

// servesMcodeThread 报告该进程是否服务过指定的上层会话。
func (p *residentProcess) servesMcodeThread(threadID string) bool {
	p.mcodeMu.Lock()
	defer p.mcodeMu.Unlock()
	_, ok := p.mcodeThreads[threadID]
	return ok
}

// sameKey 判断该进程是否服务于同一 (workDir, sandbox, 路由指纹) 组合。
// CODEX_HOME（即所切换的账号）属于 Harness 级别，同一 Harness 内不会变化，
// 因此无需纳入分组 key；切换账号需新建 Harness。
func (p *residentProcess) sameKey(turn harness.Turn, routeKey string) bool {
	return p.workDir == turn.WorkDir && p.sandbox == turn.Sandbox && p.routeKey == routeKey
}

func (p *residentProcess) setActiveTurn(at *activeTurnContext) {
	p.atMu.Lock()
	p.activeTurn = at
	p.atMu.Unlock()
}

func (p *residentProcess) getActiveTurn() *activeTurnContext {
	p.atMu.RLock()
	defer p.atMu.RUnlock()
	return p.activeTurn
}

func (p *residentProcess) isDead() bool {
	select {
	case <-p.deadCh:
		return true
	default:
		return false
	}
}

func (p *residentProcess) kill() {
	p.deadOnce.Do(func() {
		_ = p.stdin.Close()
		_ = killProcessGroup(p.cmd, syscall.SIGKILL)
		close(p.deadCh)
	})
	if p.cmd != nil {
		_ = p.cmd.Wait()
	}
	if p.cleanup != nil {
		p.cleanup()
		p.cleanup = nil
	}
}

func (p *residentProcess) close() error {
	p.deadOnce.Do(func() {
		_ = p.stdin.Close()
		close(p.deadCh)
	})

	waitDone := make(chan struct{})
	go func() {
		if p.cmd != nil {
			_ = p.cmd.Wait()
		}
		close(waitDone)
	}()

	select {
	case <-waitDone:
	case <-time.After(500 * time.Millisecond):
		_ = killProcessGroup(p.cmd, syscall.SIGKILL)
		<-waitDone
	}

	if p.cleanup != nil {
		p.cleanup()
		p.cleanup = nil
	}
	return nil
}

type activeTurnContext struct {
	ctx        context.Context
	turn       harness.Turn
	handler    *ProtocolHandler
	emit       func(harness.Event)
	accept     func()       // 上报本轮已被 CLI 收下；为 nil 时不报（测试可直接构造上下文）
	idMu       sync.RWMutex // 保护 threadID 和 turnID
	threadID   string
	turnID     string
	turnDoneCh chan struct{}
	turnOnce   sync.Once
}

func (at *activeTurnContext) setThreadID(id string) {
	at.idMu.Lock()
	defer at.idMu.Unlock()
	at.threadID = id
}

func (at *activeTurnContext) setTurnID(id string) {
	at.idMu.Lock()
	defer at.idMu.Unlock()
	at.turnID = id
}

func (at *activeTurnContext) ids() (threadID, turnID string) {
	at.idMu.RLock()
	defer at.idMu.RUnlock()
	return at.threadID, at.turnID
}

// adoptStartedTurn 从 turn/started 通知里登记 threadId 与 turnId。
// thread/compact/start 的响应是空对象，turnId 只能从该通知得到。
func (at *activeTurnContext) adoptStartedTurn(params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	if p.ThreadID != "" {
		at.setThreadID(p.ThreadID)
	}
	if p.Turn.ID != "" {
		at.setTurnID(p.Turn.ID)
	}
}

func (at *activeTurnContext) settle() {
	at.turnOnce.Do(func() {
		close(at.turnDoneCh)
	})
}

// acceptStarted 上报本轮用户输入已被 CLI 收下。读取协程在 turn/started 通知到达时调用，
// 这样同一流上随后的正文事件一定排在它后面；主协程在 turn/start 返回后再调一次作为兼底，
// 去重由 turnEmitter 负责。
func (at *activeTurnContext) acceptStarted() {
	if at.accept == nil {
		return
	}
	at.accept()
}

// mapPermissionMode 将 harness.PermissionMode 映射为 Codex app-server schema 的 sandbox 与 approvalPolicy。
func mapPermissionMode(mode harness.PermissionMode) (sandbox string, approvalPolicy string) {
	switch mode {
	case harness.ModeReadOnly:
		return "read-only", "on-request"
	case harness.ModeSupervised:
		return "read-only", "untrusted"
	case harness.ModeAutoAcceptEdits, harness.ModeAuto, "":
		return "workspace-write", "on-request"
	case harness.ModeFullAccess:
		return "danger-full-access", "never"
	default:
		return "read-only", "on-request"
	}
}

func hardLockedPermission(mode harness.PermissionMode, hardLocked bool, override string) (sandbox, approval string) {
	sandbox, approval = mapPermissionMode(mode)
	if hardLocked && strings.TrimSpace(override) == "" && sandbox == "danger-full-access" {
		return "workspace-write", "on-request"
	}
	return sandbox, approval
}

// acquireProcess 从进程池里取一个可用的常驻 app-server 进程并标记为 busy；池中没有可用进程时新起一个。
// 取用规则：在 (workDir, sandbox, 路由指纹) 相同、存活且空闲的进程中，优先选已缓存 turn.SessionID 的那个，
// 其次选任意一个；取用时顺手把已死的进程移出池并回收。
func (h *Harness) acquireProcess(ctx context.Context, turn harness.Turn, route *MagpieRoute) (*residentProcess, error) {
	h.mu.Lock()
	proc, dead := h.pickLocked(turn, magpieRouteKey(route))
	if proc == nil {
		p, err := h.startProcess(ctx, turn, route)
		if err != nil {
			h.mu.Unlock()
			reapProcesses(dead)
			return nil, err
		}
		proc = p
	}
	proc.busy = true
	h.mu.Unlock()

	reapProcesses(dead)
	return proc, nil
}

// pickLocked 在 h.mu 保护下挑选可复用的空闲进程，并返回顺带清出的已死进程；调用方负责标记 busy。
// 已死且空闲的进程会被清出池并回收；已死但 busy 的进程仍留在池里（不可被选中），由持有它的轮次在放回时清理。
func (h *Harness) pickLocked(turn harness.Turn, routeKey string) (*residentProcess, []*residentProcess) {
	live := make([]*residentProcess, 0, len(h.procs))
	var dead []*residentProcess
	for _, p := range h.procs {
		if p == nil {
			continue
		}
		if p.isDead() {
			if p.busy {
				live = append(live, p)
			} else {
				dead = append(dead, p)
			}
			continue
		}
		live = append(live, p)
	}
	h.procs = live

	var fallback *residentProcess
	for _, p := range h.procs {
		if p.busy || !p.sameKey(turn, routeKey) {
			continue
		}
		if p.hasCachedThread(turn.SessionID) {
			return p, dead
		}
		if fallback == nil {
			fallback = p
		}
	}
	return fallback, dead
}

// reapProcesses 回收已死进程（在 h.mu 之外调用，避免持锁等待子进程退出）。
func reapProcesses(procs []*residentProcess) {
	for _, p := range procs {
		if p != nil {
			p.kill()
		}
	}
}

// releaseProcess 把一轮结束后的进程放回池里；同一 (workDir, sandbox, 路由指纹) 下空闲进程超过上限时直接关闭多余的（优先关最旧的）。
// 已死的进程不再放回，并顺手回收。
func (h *Harness) releaseProcess(proc *residentProcess) {
	if proc == nil {
		return
	}

	h.mu.Lock()
	for i, p := range h.procs {
		if p == proc {
			h.procs = append(h.procs[:i], h.procs[i+1:]...)
			break
		}
	}

	if proc.isDead() {
		h.mu.Unlock()
		proc.kill()
		return
	}

	proc.busy = false
	h.procs = append(h.procs, proc)

	// 同一 key 下空闲进程数超过上限时，关闭最旧的（即池中靠前的）空闲进程
	var evicted []*residentProcess
	for h.idleCountLocked(proc.workDir, proc.sandbox, proc.routeKey) > maxIdleProcsPerKey {
		idx := -1
		for i, p := range h.procs {
			if p != proc && !p.busy && p.workDir == proc.workDir && p.sandbox == proc.sandbox && p.routeKey == proc.routeKey {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		evicted = append(evicted, h.procs[idx])
		h.procs = append(h.procs[:idx], h.procs[idx+1:]...)
	}
	h.mu.Unlock()

	for _, p := range evicted {
		_ = p.close()
	}
}

// idleCountLocked 统计指定 (workDir, sandbox, 路由指纹) 下空闲进程的数量（保持调用方持有的 h.mu）。
func (h *Harness) idleCountLocked(workDir string, sandbox harness.Sandbox, routeKey string) int {
	n := 0
	for _, p := range h.procs {
		if p != nil && !p.busy && p.workDir == workDir && p.sandbox == sandbox && p.routeKey == routeKey {
			n++
		}
	}
	return n
}

// dropProcess 把指定进程从池里移除并回收（进程已死或已不可用时的清理路径）。
func (h *Harness) dropProcess(proc *residentProcess) {
	if proc == nil {
		return
	}
	h.mu.Lock()
	for i, p := range h.procs {
		if p == proc {
			h.procs = append(h.procs[:i], h.procs[i+1:]...)
			break
		}
	}
	h.mu.Unlock()

	proc.kill()
}

// retireProcess 把进程从池里移除并优雅关闭，用于一次性轮次（turn.Transient）结束后的收尾。
func (h *Harness) retireProcess(proc *residentProcess) {
	if proc == nil {
		return
	}

	h.mu.Lock()
	for i, p := range h.procs {
		if p == proc {
			h.procs = append(h.procs[:i], h.procs[i+1:]...)
			break
		}
	}
	h.mu.Unlock()

	_ = proc.close()
}

// startProcess 拉起一个新的常驻 app-server 进程并放入池中（保持调用方持有的 h.mu）。
func (h *Harness) startProcess(ctx context.Context, turn harness.Turn, route *MagpieRoute) (*residentProcess, error) {
	bin := h.opts.Binary
	if bin == "" {
		bin = "codex"
	}

	args := []string{"app-server", "--stdio"}
	if len(h.opts.ExtraArgs) > 0 {
		args = append(args, h.opts.ExtraArgs...)
	}

	if route != nil {
		args = append(args, magpieArgs(*route)...)
	}

	var cmd *exec.Cmd
	var cleanup func()
	var err error

	if turn.Sandbox != nil {
		cmd, cleanup, err = turn.Sandbox.Command(context.Background(), turn.WorkDir, turn.Mode, bin, args...)
		if err != nil {
			return nil, fmt.Errorf("codex: sandbox command: %w", err)
		}
	} else {
		cmd = procattr.HideWindow(exec.CommandContext(context.Background(), bin, args...))
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
	// CodexHome 位于 turn.Env 之前，因此 turn.Env 中的同名变量仍然优先。
	extraEnv := turn.Env
	if h.opts.CodexHome != "" {
		_ = EnsureSharedLayout(h.opts.CodexHome)
		extraEnv = append([]string{"CODEX_HOME=" + h.opts.CodexHome}, turn.Env...)
	}
	cmd.Env = buildEnv(baseEnv, turn.WorkDir, extraEnv)
	if route != nil {
		cmd.Env = append(cmd.Env, magpieEnv(*route)[0])
		cmd.Env = magpieinject.WithLoopbackNoProxy(cmd.Env)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, fmt.Errorf("codex: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		if cleanup != nil {
			cleanup()
		}
		return nil, fmt.Errorf("codex: stdout pipe: %w", err)
	}

	stderrBuf := &tailBuffer{limit: 8192}
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		if cleanup != nil {
			cleanup()
		}
		return nil, fmt.Errorf("codex: start: %w", err)
	}

	client := newAppClient(stdin)
	proc := &residentProcess{
		cmd:           cmd,
		stdin:         stdin,
		client:        client,
		stderrBuf:     stderrBuf,
		workDir:       turn.WorkDir,
		sandbox:       turn.Sandbox,
		routeKey:      magpieRouteKey(route),
		cleanup:       cleanup,
		deadCh:        make(chan struct{}),
		cachedThreads: make(map[string]harness.PermissionMode),
	}

	// 启动常驻读循环
	go func() {
		defer close(client.readerDoneCh)
		reader := bufio.NewReader(stdout)

		for {
			line, readErr := reader.ReadBytes('\n')
			if len(line) > 0 {
				line = bytes.TrimSpace(line)
				if len(line) > 0 {
					var rawMsg JSONRPCMessage
					if err := json.Unmarshal(line, &rawMsg); err == nil {
						switch {
						case rawMsg.IsResponse():
							idStr := rawMsg.RawID()
							client.mu.Lock()
							ch, ok := client.pending[idStr]
							client.mu.Unlock()
							if ok && ch != nil {
								ch <- &rawMsg
							}

						case rawMsg.IsRequest():
							// 服务端审批请求：交给当前活跃的 turn 处理
							at := proc.getActiveTurn()
							if at != nil {
								go func(act *activeTurnContext, reqMsg JSONRPCMessage) {
									reply, err := act.handler.HandleServerRequest(act.ctx, &reqMsg)
									if err == nil && reply != nil {
										sendErr := client.send(reply)
										if reply.onAck != nil {
											reply.onAck(sendErr)
										}
									}
								}(at, rawMsg)
							} else {
								// 无活跃轮次时的保护应答，防止服务端悬挂
								go func(reqMsg JSONRPCMessage) {
									reply := &JSONRPCMessage{
										JSONRPC: "2.0",
										ID:      reqMsg.ID,
										Result:  json.RawMessage(`{"decision":"decline"}`),
									}
									_ = client.send(reply)
								}(rawMsg)
							}

						case rawMsg.IsNotification():
							at := proc.getActiveTurn()
							if at != nil {
								// turn/started 是 compact 等响应不携带 turnId 的请求唯一的 turnId 来源，先登记再交给协议层。
								if rawMsg.Method == "turn/started" {
									at.adoptStartedTurn(rawMsg.Params)
									// 在读取协程内上报：同一流上随后的 item 事件一定排在 accepted 之后。
									at.acceptStarted()
								}
								at.handler.HandleNotification(&rawMsg, at.emit)
								if rawMsg.Method == "turn/completed" || rawMsg.Method == "turn/failed" {
									at.settle()
								}
							}
						}
					}
				}
			}
			if readErr != nil {
				break
			}
		}

		proc.deadOnce.Do(func() {
			close(proc.deadCh)
		})

		if at := proc.getActiveTurn(); at != nil {
			at.settle()
		}

		// 释放所有悬空的 pending 请求
		client.mu.Lock()
		for idStr, ch := range client.pending {
			close(ch)
			delete(client.pending, idStr)
		}
		client.mu.Unlock()
	}()

	// 初始化握手（仅在常驻进程启动时执行一次）
	initParams := map[string]any{
		"clientInfo": map[string]any{
			"name":    "mcode",
			"version": "0.1.0",
		},
		"capabilities": map[string]any{
			"experimentalApi": true,
		},
	}
	initRes, err := client.call(ctx, "initialize", initParams)
	if err != nil {
		proc.kill()
		return nil, fmt.Errorf("codex: initialize failed: %w", err)
	}

	// 握手响应携带 app-server 自身版本信息（userAgent 等）：只记录并输出一次日志，不做能力门控。
	var serverInfo struct {
		UserAgent      string `json:"userAgent"`
		PlatformOS     string `json:"platformOs"`
		PlatformFamily string `json:"platformFamily"`
		CodexHome      string `json:"codexHome"`
	}
	_ = json.Unmarshal(initRes, &serverInfo)
	h.recordServerVersion(serverInfo.UserAgent, serverInfo.PlatformOS, serverInfo.PlatformFamily, serverInfo.CodexHome)

	_ = client.notify("initialized", nil)

	h.procs = append(h.procs, proc)
	return proc, nil
}

// buildThreadParams 构造 thread/start 或 thread/resume 所需的参数字典和方法名。
// 当内置 MCP 端点可用或存在外部 endpoint 时注入 config.mcp_servers 配置覆盖表；
// blocked 里的服务名即使 app-server 能从工作目录配置里读到，也必须在线程配置里关闭。
func buildThreadParams(turn harness.Turn, sandbox, approvalPolicy string, threadID string, builtin harness.BuiltinMCP, external []harness.MCPEndpoint, blocked []string) (map[string]any, string) {
	threadParams := map[string]any{
		"cwd":            turn.WorkDir,
		"sandbox":        sandbox,
		"approvalPolicy": approvalPolicy,
	}
	if turn.Model != "" {
		threadParams["model"] = turn.Model
	}

	mcpServers := make(map[string]any)
	if url, token, workdirHeader := builtin.Endpoint(turn); url != "" {
		headers := map[string]any{
			"Authorization": "Bearer " + token,
		}
		if workdirHeader {
			headers["X-Mcode-Workdir"] = turn.WorkDir
		}
		mcpServers["mcode"] = map[string]any{
			"url":          url,
			"http_headers": headers,
		}
	}
	for _, ep := range external {
		if ep.Name == "mcode" || ep.Name == "mcode_approval" {
			continue
		}
		headers := map[string]any{}
		if ep.Token != "" {
			headers["Authorization"] = "Bearer " + ep.Token
		}
		name := ep.Name
		if ep.CodexName != "" && ep.CodexName != ep.Name {
			// Codex 深合并配置，不能把带 command 的 stdio 表原地覆盖成 HTTP。
			mcpServers[name] = map[string]any{"enabled": false}
			name = ep.CodexName
		}
		mcpServers[name] = map[string]any{
			"url":          ep.URL,
			"http_headers": headers,
		}
	}
	for _, name := range blocked {
		if name == "mcode" || name == "mcode_approval" {
			continue
		}
		// 仓库内未信任的服务一旦被 app-server 从工作目录配置拉起就绕过了门控，这里只关不覆盖已有配置。
		if _, exists := mcpServers[name]; exists {
			continue
		}
		mcpServers[name] = map[string]any{"enabled": false}
	}
	if len(mcpServers) > 0 {
		threadParams["config"] = map[string]any{
			"mcp_servers": mcpServers,
		}
	}

	// workspace-write 只放开工作目录；总管会话要碰的已注册项目目录必须显式写进 writable_roots。
	if sandbox == "workspace-write" {
		if roots := writableRoots(turn); len(roots) > 0 {
			cfg, _ := threadParams["config"].(map[string]any)
			if cfg == nil {
				cfg = map[string]any{}
			}
			cfg["sandbox_workspace_write"] = map[string]any{"writable_roots": roots}
			threadParams["config"] = cfg
		}
	}

	threadMethod := "thread/start"
	if threadID != "" {
		threadMethod = "thread/resume"
		threadParams["threadId"] = threadID
		// 关键优化：跳过全量历史 turns 的反序列化与传输
		threadParams["excludeTurns"] = true
	}

	return threadParams, threadMethod
}

// writableRoots 返回工作目录之外的附加目录（总管会话的已注册项目），空串与工作目录自身跳过。
func writableRoots(turn harness.Turn) []string {
	var roots []string
	seen := make(map[string]bool, len(turn.AdditionalDirs))
	for _, dir := range turn.AdditionalDirs {
		if dir == "" || dir == turn.WorkDir || seen[dir] {
			continue
		}
		seen[dir] = true
		roots = append(roots, dir)
	}
	return roots
}

// turnEmitter 包装向上层的事件回调，并记录本轮是否已经发出过内容事件。
// 重试只在「启动阶段、且尚未向上发出任何内容事件」时进行：一旦有内容注入上层，重试会导致重复输出。
// 会话事件（EventSession）只是线程标识，不算内容事件，否则 thread/start 成功后 turn/start 失败就永远无法重试。
type turnEmitter struct {
	fn          func(harness.Event)
	mu          sync.Mutex
	emittedFlag bool
	accepted    bool // 本轮是否已上报 EventTurnAccepted
}

// emit 向上层投递一个事件，并记录是否已发出内容事件。
func (e *turnEmitter) emit(ev harness.Event) {
	e.mu.Lock()
	if ev.Type != harness.EventSession {
		e.emittedFlag = true
	}
	e.mu.Unlock()
	_ = harness.Emit(e.fn, ev)
}

// emitted 报告本轮是否已经向上层发出过内容事件。
func (e *turnEmitter) emitted() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.emittedFlag
}

// emitAccepted 上报本轮用户输入已被 CLI 收下，同一轮最多一次。它不算内容事件：
// accepted 只说明请求被收下、还没有任何输出，重试口径保持不变。
func (e *turnEmitter) emitAccepted() {
	e.mu.Lock()
	if e.accepted {
		e.mu.Unlock()
		return
	}
	e.accepted = true
	e.mu.Unlock()
	_ = harness.Emit(e.fn, harness.Event{Type: harness.EventTurnAccepted})
}

// Run 执行一轮 Codex 交互。
// 启动阶段（thread/start、thread/resume、turn/start 以及原生 compact/review）失败，或常驻进程在本轮
// 还没向上发出任何内容事件就已退出时，丢弃该进程重建并有限重试：最多 2 次，退避依次为 0、1s、2s。
func (h *Harness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	var route *MagpieRoute
	if h.opts.MagpieRoutes != nil {
		var err error
		turn, route, err = magpieTurn(turn, h.opts.MagpieRoutes())
		if err != nil {
			return err
		}
	}
	emitter := &turnEmitter{fn: emit}
	var lastErr error
	for attempt := 0; attempt < codexRunMaxAttempts; attempt++ {
		if attempt > 0 {
			if err := h.waitRetry(ctx, attempt); err != nil {
				// ctx 已取消：沿用上一次的错误，没有历史错误时返回取消原因。
				if lastErr != nil {
					return lastErr
				}
				return err
			}
		}

		retryable, err := h.runTurnAttempt(ctx, turn, route, emitter)
		if err == nil {
			return nil
		}
		lastErr = err
		// 本轮已经向上发出过内容事件或 ctx 已取消时不再重试，沿用现有报错。
		if !retryable || emitter.emitted() || ctx.Err() != nil {
			return err
		}
	}
	return lastErr
}

// runTurnAttempt 执行一次完整尝试：取常驻进程、准备线程、发起 turn/start（或原生 compact / review），
// 再等待回合结束或 ctx 取消。retryable 表示这次失败发生在向上发出任何内容事件之前的启动阶段。
func (h *Harness) runTurnAttempt(ctx context.Context, turn harness.Turn, route *MagpieRoute, emitter *turnEmitter) (retryable bool, err error) {
	proc, err := h.acquireProcess(ctx, turn, route)
	if err != nil {
		return false, err
	}
	defer func() {
		// 辅助会话（如 Swarm bee）不会再有下一轮，收尾时直接关闭进程，不占空闲池。
		if turn.Transient {
			h.retireProcess(proc)
			return
		}
		// 一轮结束后把进程放回池里（已死的进程不会放回）
		h.releaseProcess(proc)
	}()

	// 若取用后进程已死（例如刚被中断路径杀掉），换一个进程并从池里清理旧进程
	if proc.isDead() {
		dead := proc
		next, acquireErr := h.acquireProcess(ctx, turn, route)
		if acquireErr != nil {
			h.dropProcess(dead)
			return false, acquireErr
		}
		proc = next
		h.dropProcess(dead)
	}

	// app-server 是常驻进程，本轮期间它就是该会话的 agent 进程；本轮结束后清理记录。
	if proc.cmd != nil && proc.cmd.Process != nil {
		agentPID := proc.cmd.Process.Pid
		h.roots.Set(turn.ThreadID, agentPID)
		defer h.roots.Clear(turn.ThreadID, agentPID)
	}

	// turnMu 作为防御保留：进程池已保证同一进程同一时间只跑一轮
	proc.turnMu.Lock()
	defer proc.turnMu.Unlock()

	handler := NewProtocolHandler(turn)
	activeTurn := &activeTurnContext{
		ctx:        ctx,
		turn:       turn,
		handler:    handler,
		emit:       emitter.emit,
		accept:     emitter.emitAccepted,
		turnDoneCh: make(chan struct{}),
	}
	proc.setActiveTurn(activeTurn)
	// 记录上层会话 ID：EvictThread 按 harness.Turn.ThreadID 调用，与本进程缓存的 codex threadId 并不相同。
	proc.noteMcodeThread(turn.ThreadID)
	defer func() {
		proc.setActiveTurn(nil)
	}()

	client := proc.client

	// 准备会话线程：已缓存线程可直接进入 turn/start
	sandbox, approvalPolicy := hardLockedPermission(turn.Mode, turn.HardLocked, turn.HardLockOverride)
	cacheMode := turn.Mode
	if turn.Mode == harness.ModeFullAccess && sandbox == "workspace-write" {
		cacheMode = harness.ModeAuto
	}
	threadID := turn.SessionID

	proc.cachedMu.Lock()
	cachedMode, alreadyCached := proc.cachedThreads[threadID]
	alreadyCached = alreadyCached && cachedMode == cacheMode
	proc.cachedMu.Unlock()

	if threadID == "" || !alreadyCached {
		var external []harness.MCPEndpoint
		if h.opts.ExternalMCP != nil {
			external = h.opts.ExternalMCP(turn.WorkDir, turn.ThreadID)
		}
		var blocked []string
		if h.opts.BlockedMCP != nil {
			blocked = h.opts.BlockedMCP(turn.WorkDir)
		}
		threadParams, threadMethod := buildThreadParams(turn, sandbox, approvalPolicy, threadID, h.opts.BuiltinMCP, external, blocked)

		threadRes, err := client.call(ctx, threadMethod, threadParams)
		if err != nil {
			return h.failAttempt(proc, threadMethod, err)
		}

		var started struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		_ = json.Unmarshal(threadRes, &started)
		if started.Thread.ID != "" {
			threadID = started.Thread.ID
		}
		if threadID != "" {
			proc.cachedMu.Lock()
			proc.cachedThreads[threadID] = cacheMode
			proc.cachedMu.Unlock()
		}
	}

	activeTurn.setThreadID(threadID)
	if threadID != "" {
		emitter.emit(harness.Event{
			Type:      harness.EventSession,
			SessionID: threadID,
		})
	}

	// 原生斜杠请求：/compact 与 /review 不发 turn/start，改发对应的 app-server 请求；
	// 其后的通知流（turn/started、item、turn/completed）仍由同一条常驻读取循环投递。
	slashKind, reviewInstructions := planSlashRequest(turn.Prompt, threadID != "" && (turn.SessionID != "" || alreadyCached))
	switch slashKind {
	case slashRequestCompact:
		if _, err := client.call(ctx, "thread/compact/start", buildCompactParams(threadID)); err != nil {
			return h.failAttempt(proc, "thread/compact/start", err)
		}
	case slashRequestReview:
		reviewRes, err := client.call(ctx, "review/start", buildReviewParams(threadID, reviewInstructions))
		if err != nil {
			return h.failAttempt(proc, "review/start", err)
		}
		// inline 审阅的响应里带 turn，可直接拿到 turnId；拿不到时再等 turn/started 通知补齐。
		var reviewStarted struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(reviewRes, &reviewStarted)
		activeTurn.setTurnID(reviewStarted.Turn.ID)
	default:
		turnRes, err := client.call(ctx, "turn/start", buildTurnStartParams(turn, threadID))
		if err != nil {
			return h.failAttempt(proc, "turn/start", err)
		}

		var turnStarted struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(turnRes, &turnStarted)
		activeTurn.setTurnID(turnStarted.Turn.ID)
		// turn/start 返回成功即 CLI 已收下本轮输入；若 turn/started 通知已经先到，这里会去重。
		activeTurn.acceptStarted()
	}

	// 等待回合结束或 ctx 取消
	select {
	case <-activeTurn.turnDoneCh:
		if handler.IsTurnDone() {
			return false, nil
		}
		if proc.isDead() && !emitter.emitted() {
			// 进程在本轮还没向上发出任何事件前就退出：丢弃进程，交给调用方重建重试。
			proc.kill()
			h.dropProcess(proc)
			return true, fmt.Errorf("codex: app-server exited before the turn produced any event")
		}
		tail := strings.TrimSpace(proc.stderrBuf.String())
		if route != nil && route.APIKey != "" {
			tail = strings.ReplaceAll(tail, route.APIKey, "[redacted]")
		}
		errMsg := tail
		if errMsg == "" {
			errMsg = "codex process closed connection unexpectedly"
		}
		emitter.emit(harness.Event{
			Type: harness.EventError,
			Err:  errMsg,
		})
		proc.kill()
		h.dropProcess(proc)
		return false, fmt.Errorf("codex turn incomplete (stderr: %s)", tail)

	case <-ctx.Done():
		_, turnID := activeTurn.ids()
		if turnID != "" && threadID != "" {
			interruptCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, _ = client.call(interruptCtx, "turn/interrupt", map[string]any{
				"threadId": threadID,
				"turnId":   turnID,
			})
			cancel()

			select {
			case <-activeTurn.turnDoneCh:
				return false, ctx.Err()
			case <-time.After(1500 * time.Millisecond):
				// 中断超时无响应，彻底杀死进程组并自愈重建
				proc.kill()
				h.dropProcess(proc)
				return false, ctx.Err()
			}
		}

		// 拿不到 turnId（例如 compact 之类响应不携带 turnId 的请求还没收到 turn/started）时无法优雅中断，
		// 退化为进程级强停，避免卡在无响应的等待上。
		proc.kill()
		h.dropProcess(proc)
		return false, ctx.Err()
	}
}

// failAttempt 处理启动阶段请求失败：丢弃该常驻进程并统一包装错误。
// 失败后的进程可能停在不一致的线程或半死状态上，不再复用，由调用方重建重试。
// 这类失败发生在本轮向上发出任何内容事件之前，调用方可以安全重试。
func (h *Harness) failAttempt(proc *residentProcess, method string, cause error) (retryable bool, err error) {
	h.dropProcess(proc)
	return true, fmt.Errorf("codex: %s failed: %w", method, cause)
}

// waitRetry 在重试前按退避序列等待，attempt 为即将开始的尝试序号（1 表示第 1 次重试）。
// 退避等待可被 ctx 取消；tests 可注入 retryBackoffs / retrySleep 跳过真实等待。
func (h *Harness) waitRetry(ctx context.Context, attempt int) error {
	backoffs := h.retryBackoffs
	if len(backoffs) == 0 {
		backoffs = defaultRetryBackoffs
	}
	var d time.Duration
	if idx := attempt - 1; idx >= 0 && idx < len(backoffs) {
		d = backoffs[idx]
	}
	if h.retrySleep != nil {
		return h.retrySleep(ctx, d)
	}
	if d <= 0 {
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// buildTurnInput 构造 turn/start 的 input 数组：文本项在前，其后追加本地图片项。
// 图片项对应 schema 的 UserInput -> LocalImageUserInput（type=localImage，必填 path）。
func buildTurnInput(turn harness.Turn) []map[string]any {
	input := make([]map[string]any, 0, 1+len(turn.Images))
	input = append(input, map[string]any{
		"type": "text",
		"text": turn.Prompt,
	})
	for _, path := range turn.Images {
		// 图片路径可能带换行等多余空白，去掉后再下发本地图片项。
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		// 非图片附件（PDF、docx 等）不当本地图片下发，其路径已由上层写进 prompt。
		if !isImagePath(path) {
			log.Printf("codex: skip image %s: unsupported image type %q", path, filepath.Ext(path))
			continue
		}
		input = append(input, map[string]any{
			"type": "localImage",
			"path": path,
		})
	}
	return input
}

// isImagePath 判断路径所指附件是否可以作为图片下发：扩展名优先，扩展名不可靠时按内容嗅探
// （会话媒体目录里的历史文件没有扩展名）。
func isImagePath(path string) bool {
	_, ok := harness.ImageMediaType(path)
	return ok
}

// buildTurnStartParams 构造 turn/start 参数。除文本外可带本地图片、模型、推理强度、快速档位与 plan 模式。
func buildTurnStartParams(turn harness.Turn, threadID string) map[string]any {
	params := map[string]any{
		"threadId": threadID,
		"input":    buildTurnInput(turn),
	}
	if turn.Model != "" {
		params["model"] = turn.Model
	}
	if turn.Effort != "" {
		params["effort"] = turn.Effort
	}
	// 快速模式：以模型目录里的快速档位下发 serviceTierForTurn；模型无快速档位或目录未知时不传。
	if turn.FastMode {
		tier := turn.SpeedTier
		if tier == "" {
			tier = fastServiceTierForModel(turn.Model)
		}
		if tier != "" {
			params["serviceTierForTurn"] = tier
		}
	}
	if mode := buildCollaborationMode(turn); mode != nil {
		params["collaborationMode"] = mode
	}
	return params
}

// buildCollaborationMode 构造 plan 模式的 collaborationMode 参数（schema：TurnStartParams.collaborationMode
// -> CollaborationMode{mode: ModeKind, settings: Settings}，Settings.model 必填）。
// schema 不允许缺省 settings.model，因此 turn.Model 为空时返回 nil，退化为普通 build 回合，不下发该参数。
// settings.developer_instructions 留空（schema 语义为 null：使用该模式的内置指令）——plan 模式的约束文本
// 已由上层拼进 Prompt，这里不再重复下发。
func buildCollaborationMode(turn harness.Turn) map[string]any {
	if strings.TrimSpace(turn.InteractionMode) != "plan" {
		return nil
	}
	if turn.Model == "" {
		return nil
	}
	settings := map[string]any{"model": turn.Model}
	if turn.Effort != "" {
		settings["reasoning_effort"] = turn.Effort
	}
	return map[string]any{
		"mode":     "plan",
		"settings": settings,
	}
}

// 斜杠请求种类：本轮 Prompt 命中内置命令时改发对应的 app-server 请求。
const (
	slashRequestNone    = ""
	slashRequestCompact = "compact"
	slashRequestReview  = "review"
)

// planSlashRequest 判断本轮 Prompt 是否要改发原生 compact / review 请求，并返回 review 的附加说明文字。
// 只拦截真正会到达 Harness.Run 的形态：`/compact` 由 backend 原样透传（trimmed 后等于该文本），
// `/review`（可带说明文字）被当作普通文本发给后端，两者都以纯文本出现在 turn.Prompt 里。
// resumable 为 false（会话尚无可恢复的 thread）时不拦截：compact 没有可压缩的历史，review 也无从附着。
func planSlashRequest(prompt string, resumable bool) (kind string, instructions string) {
	if !resumable {
		return slashRequestNone, ""
	}
	text := strings.TrimSpace(prompt)
	if text == "/compact" {
		return slashRequestCompact, ""
	}
	rest, ok := strings.CutPrefix(text, "/review")
	if !ok {
		return slashRequestNone, ""
	}
	if rest == "" {
		return slashRequestReview, ""
	}
	// `/review` 后必须跟空白，避免把 `/reviewx` 这类普通文本当成命令。
	if rest[0] != ' ' && rest[0] != '\t' && rest[0] != '\n' && rest[0] != '\r' {
		return slashRequestNone, ""
	}
	return slashRequestReview, strings.TrimSpace(rest)
}

// buildCompactParams 构造 thread/compact/start 参数（schema：ThreadCompactStartParams，只含 threadId）。
func buildCompactParams(threadID string) map[string]any {
	return map[string]any{"threadId": threadID}
}

// buildReviewParams 构造 review/start 参数（schema：ReviewStartParams{threadId, target, delivery}）。
// 无附加文字时审阅工作区未提交改动（ReviewTarget -> UncommittedChangesReviewTarget），
// 有附加文字时作为说明下发（ReviewTarget -> CustomReviewTarget），delivery 固定 inline：
// detached 已废弃，审阅就应当跑在当前会话上。
func buildReviewParams(threadID, instructions string) map[string]any {
	var target map[string]any
	if instructions == "" {
		target = map[string]any{"type": "uncommittedChanges"}
	} else {
		target = map[string]any{
			"type":         "custom",
			"instructions": instructions,
		}
	}
	return map[string]any{
		"threadId": threadID,
		"target":   target,
		"delivery": "inline",
	}
}

// fastServiceTierForModel 返回模型目录里该模型的快速档位 ID，无目录数据或无快速档位时为空。
// 模型没有快速档位时不下发 serviceTierForTurn。
func fastServiceTierForModel(modelID string) string {
	if modelID == "" {
		return ""
	}
	for _, m := range CachedModels() {
		if m.ID == modelID {
			return m.FastTierID
		}
	}
	return ""
}
