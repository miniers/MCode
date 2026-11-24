package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"

	"mcode/internal/agent/tools"
	"mcode/internal/harness"
	"mcode/internal/procattr"
	"mcode/internal/procutil"
	"mcode/internal/providers/loginenv"
)

var _ harness.Harness = (*Harness)(nil)
var _ harness.HostToolsReporter = (*Harness)(nil)

// Options 包含 ACP 适配器的配置选项。
type Options struct {
	HoldBackgroundTasks bool
	Agent               Agent
	Binary              string
	ExtraArgs           []string
	TurnArgs            func(turn harness.Turn) []string
	Env                 []string
	DroidAuth           DroidAuthMode
	DroidAPIKey         func() string
	// BuiltinMCP/ExternalMCP 决定 session/new 与 session/load 中注入的 MCP 服务，
	// 内置服务按会话路由（BuiltinMCP.Endpoint(turn)）。
	BuiltinMCP  harness.BuiltinMCP
	ExternalMCP func(workDir, threadID string) []harness.MCPEndpoint
	// 以下字段用于测试覆盖内部等待时长，零值保持生产默认值。
	DisableLaunchSpacing   bool
	BackgroundPollInterval time.Duration
	AttachIdleTimeout      time.Duration
	StopKillGrace          time.Duration
}

const (
	defaultBackgroundPollInterval = 2 * time.Second
	defaultAttachIdleTimeout      = 3 * time.Second
	defaultAttachPollInterval     = 200 * time.Millisecond
	defaultStopKillGrace          = 3 * time.Second
)

// launchSpacing 是相邻两次 ACP 进程启动的最小间隔（1.5 秒）：
// 同一瞬间批量启动多个 ACP agent（例如 Swarm 的多个 bee）会在 Agent 自身的本地存储
// （如 OpenCode 的 SQLite）上撞锁，导致某轮对话开局即失败；单个启动不受影响。
var launchSpacing = 1500 * time.Millisecond
var agyCancelWait = 15 * time.Second
var agyStepPollInterval = 30 * time.Second

// 6 分钟长于 agy 内部约 5 分钟的静默重试，为成功重试留出窗口。
var agyStepErrorStall = 6 * time.Minute
var agyAutoDenyMaxContinuations = 3
var beforeAgyPromptSelect func()

type agyAutoDenial struct {
	toolName string
	summary  string
	feedback string
}

func buildAgyAutoDenyContinuationPrompt(denials []agyAutoDenial) string {
	var b strings.Builder
	b.WriteString("以下工具调用已被权限策略自动拦截，请不要重试被拦截的操作，也不要就此中断，请换一种允许的做法继续完成当前任务：\n")
	seen := make(map[string]bool, len(denials))
	for _, d := range denials {
		target := strings.TrimSpace(d.summary)
		if target == "" {
			target = strings.TrimSpace(d.toolName)
		}
		if target == "" {
			target = "未命名工具"
		}
		reason := strings.TrimSpace(d.feedback)
		if reason == "" {
			reason = "当前权限策略不允许该操作"
		}
		key := target + "\x00" + reason
		if seen[key] {
			continue
		}
		seen[key] = true
		b.WriteString("- ")
		b.WriteString(target)
		b.WriteString("：")
		b.WriteString(reason)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

var launchGate struct {
	mu   sync.Mutex
	next time.Time
}

// waitLaunchSlot 等待到下一个允许启动的时刻；ctx 取消时立即返回。
func waitLaunchSlot(ctx context.Context) error {
	launchGate.mu.Lock()
	now := time.Now()
	slot := now
	if launchGate.next.After(now) {
		slot = launchGate.next
	}
	launchGate.next = slot.Add(launchSpacing)
	launchGate.mu.Unlock()

	if wait := time.Until(slot); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func (h *Harness) waitLaunchSlot(ctx context.Context) error {
	if h.opts.DisableLaunchSpacing {
		return nil
	}
	return waitLaunchSlot(ctx)
}

func (h *Harness) backgroundPollInterval() time.Duration {
	if h.opts.BackgroundPollInterval > 0 {
		return h.opts.BackgroundPollInterval
	}
	return defaultBackgroundPollInterval
}

func (h *Harness) attachIdleTimeout() time.Duration {
	if h.opts.AttachIdleTimeout > 0 {
		return h.opts.AttachIdleTimeout
	}
	return defaultAttachIdleTimeout
}

func (h *Harness) attachPollInterval() time.Duration {
	idle := h.attachIdleTimeout()
	interval := idle / 4
	if interval > defaultAttachPollInterval {
		interval = defaultAttachPollInterval
	}
	if interval < 5*time.Millisecond {
		interval = 5 * time.Millisecond
	}
	return interval
}

func (h *Harness) stopKillGrace() time.Duration {
	if h.opts.StopKillGrace > 0 {
		return h.opts.StopKillGrace
	}
	return defaultStopKillGrace
}

// Harness 实现了基于 ACP (Agent Communication Protocol) 的 harness.Harness 接口。
type Harness struct {
	opts Options
	// 常驻表与后台任务状态由 residentMu 保护；持锁时不等待进程、不调用外部回调。
	residentMu          sync.Mutex
	residentStartMu     sync.Mutex
	closed              bool
	residents           map[string]*acpProcess
	finishedTasks       map[string][]harness.Event
	unsolicitedNotified map[string]bool
	unsolicitedHandler  func(string)
	processExitHandler  func(string, time.Time)
	// roots 按会话记录本轮正在运行的 agent 进程 PID，供 ProcessRootPIDs 使用。
	roots harness.ProcessRoots
	// mcpHTTP 记录 initialize 应答里已确认支持 HTTP MCP；确认前按需要 npx 的退化路径判断。
	mcpHTTP atomic.Bool
	// npxOnce/npxOK 缓存 npx 是否在 PATH 中，避免每次会话状态查询都扫一遍 PATH。
	npxOnce sync.Once
	npxOK   bool
}

// acpSessionModes 是 session/new 与 session/load 响应里广告的会话模式（ACP 的 modes 字段）。
// Droid 用它判断计划模式与运行时权限对应的档位。
type acpSessionModes struct {
	CurrentModeID  string `json:"currentModeId"`
	AvailableModes []struct {
		ID string `json:"id"`
	} `json:"availableModes"`
}

type acpPromptUsage struct {
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	ThoughtTokens    int64 `json:"thoughtTokens"`
	CachedReadTokens int64 `json:"cachedReadTokens"`
}

// IDs 返回会话广告的可用模式 ID 列表。
func (m acpSessionModes) IDs() []string {
	ids := make([]string, 0, len(m.AvailableModes))
	for _, mode := range m.AvailableModes {
		ids = append(ids, mode.ID)
	}
	return ids
}

// New 创建一个新的 ACP Harness 实例。若 Binary 为空则自动回退到 Agent 的默认可执行文件名。
func New(opts Options) *Harness {
	if opts.Binary == "" {
		opts.Binary = DefaultBinary(opts.Agent)
	}
	return &Harness{opts: opts}
}

// Kind 返回 Agent 后端的标识。
func (h *Harness) Kind() harness.Kind {
	return harness.Kind(h.opts.Agent)
}

// CatalogBinary 返回当前运行时配置的 CLI。
func (h *Harness) CatalogBinary() string { return h.opts.Binary }

// ProcessRootPIDs 返回指定会话当前运行的 agent 进程 PID，未在运行时返回 nil。
func (h *Harness) ProcessRootPIDs(threadID string) []int {
	return h.roots.PIDs(threadID)
}

// ConfirmsApprovalDelivery 返回 true，表示该 Provider 对每次 Approve/Interact 的答复都会调用 DeliveryAcker.AckDelivery。
func (h *Harness) ConfirmsApprovalDelivery() bool {
	return true
}

// HostToolsAvailable 报告会话能否用到 mcode 的 MCP 回执工具：
// 内置 MCP 未配置时没有工具可注入；Agent 声明支持 HTTP MCP 时直连内置端点，
// 否则退化条目由 npx 启动 mcp-remote，找不到 npx 就没有工具可用。
func (h *Harness) HostToolsAvailable() bool {
	if !h.opts.BuiltinMCP.Enabled() {
		return false
	}
	if h.opts.Agent == AgentAgy || h.mcpHTTP.Load() {
		return true
	}
	return h.npxAvailable()
}

// npxAvailable 报告 PATH 中是否有 npx；结果按 Harness 缓存一次。
func (h *Harness) npxAvailable() bool {
	h.npxOnce.Do(func() {
		_, err := lookupNpx("npx")
		h.npxOK = err == nil
	})
	return h.npxOK
}

// Available 检查 Agent 的可执行文件是否在登录环境的 PATH 中可用。
// 仅校验可执行文件是否存在，不启动进程、不触发登录；登录环境还在后台加载时先按可用处理，
// 避免首次拉供应商列表时为一次登录 Shell 子进程等待。
func (h *Harness) Available() (bool, string) {
	bin := h.opts.Binary
	if bin == "" {
		bin = DefaultBinary(h.opts.Agent)
	}
	if found, decided := loginenv.ProbePath(bin); found || !decided {
		return true, ""
	}
	return false, fmt.Sprintf("%s (%s) was not found in PATH", DisplayName(h.opts.Agent), bin)
}

func buildEnv(base []string, workDir string, extraOpts []string, extraTurn []string) []string {
	env := make([]string, 0, len(base)+1+len(extraOpts)+len(extraTurn))
	for _, e := range base {
		if !strings.HasPrefix(e, "PWD=") {
			env = append(env, e)
		}
	}
	if workDir != "" {
		env = append(env, "PWD="+workDir)
	}
	env = append(env, extraOpts...)
	env = append(env, extraTurn...)
	return env
}

func (h *Harness) authEnv(base []string) []string {
	if h.opts.Agent != AgentDroid {
		return base
	}
	key := ""
	if h.opts.DroidAuth == DroidAuthAPIKey && h.opts.DroidAPIKey != nil {
		key = h.opts.DroidAPIKey()
	}
	return DroidAuthEnv(base, h.opts.DroidAuth, key)
}

func resolveReadPath(workDir, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if tools.SecretPath(target) {
		return "", fmt.Errorf("protected credential path: %s", target)
	}
	return filepath.EvalSymlinks(target)
}

// resolveWorkDirPath 将 Agent 请求的相对或绝对路径解析为工作目录（或附加目录）内的绝对路径。
// 越出白名单（含 ".."、指向目录外的绝对路径，以及经符号链接逃出）一律拒绝。
// 相对路径只相对 workDir 解析；extraDirs 是工作目录之外额外放开的绝对目录。
func resolveWorkDirPath(workDir, path string, extraDirs ...string) (string, string, error) {
	root := filepath.Clean(workDir)
	// rootReal 是工作目录的真实路径；按真实路径比较才能拦住经符号链接的目录（如 macOS 的 /var → /private/var）。
	rootReal := root
	if real, err := filepath.EvalSymlinks(root); err == nil {
		rootReal = real
	}
	// roots/rootsReal 是本次放行的词法白名单与真实路径白名单，第一项恒为 workDir。
	roots := []string{root}
	rootsReal := []string{rootReal}
	seenRoot := map[string]bool{root: true}
	for _, dir := range extraDirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		clean := filepath.Clean(dir)
		if seenRoot[clean] {
			continue
		}
		seenRoot[clean] = true
		real := clean
		if resolved, err := filepath.EvalSymlinks(clean); err == nil {
			real = resolved
		}
		roots = append(roots, clean)
		rootsReal = append(rootsReal, real)
	}

	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	target = filepath.Clean(target)

	// 同时放行词法根与真实根：agy 会话 cwd 使用真实路径，回传的绝对路径也是真实路径形式。
	if !withinAnyRoot(target, roots) && !withinAnyRoot(target, rootsReal) {
		return "", "", fmt.Errorf("access denied: path outside workdir")
	}

	// 词法检查挡不住符号链接，再解析真实路径；目标本身不存在时
	// 逐级上溯到最近的已存在祖先，再把剩余部分拼回去。
	resolved := target
	for probe := target; ; probe = filepath.Dir(probe) {
		real, err := filepath.EvalSymlinks(probe)
		if err == nil {
			suffix, relErr := filepath.Rel(probe, target)
			if relErr != nil {
				return "", "", relErr
			}
			resolved = filepath.Join(real, suffix)
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", "", err
		}
		if parent := filepath.Dir(probe); parent == probe {
			break
		}
	}

	// 真实路径同样按每个白名单目录再比一次，符号链接导出工作目录的路径在这里被拦住。
	for _, candidate := range rootsReal {
		if rel, err := relWithinRoot(candidate, resolved); err == nil {
			return resolved, rel, nil
		}
	}
	return "", "", fmt.Errorf("access denied: path outside workdir")
}

// withinAnyRoot 报告 target 是否落在任意一个 root 内（纯词法判断）。
func withinAnyRoot(target string, roots []string) bool {
	for _, root := range roots {
		if _, err := relWithinRoot(root, target); err == nil {
			return true
		}
	}
	return false
}

// relWithinRoot 返回 target 相对 root 的路径，越界时报错。
func relWithinRoot(root, target string) (string, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("access denied: path outside workdir")
	}
	return rel, nil
}

// mcpRemotePackage 是退化条目要跑的 npx 包，版本固定：不写版本时 npx 每轮都去 npm
// 解析并可能拉到新的 latest，Agent 启动由此受网络与上游发布节奏影响（新版本改参数名或
// 挂掉会让所有不支持 HTTP MCP 的 Agent 一起失效）。升级：npm view mcp-remote version
// 取当前稳定版，改这里的版本号后跑 internal/providers/acp 的测试。
const mcpRemotePackage = "mcp-remote@0.15.2"

// lookupNpx 是 npx 的 PATH 查找函数，测试中可替换。
var lookupNpx = exec.LookPath

// mcpServerEntry 构造 session/new 与 session/load 的单个 `mcpServers` 条目：
// Agent 支持 HTTP MCP 时直连，
// 否则退化为经 `npx mcpRemotePackage` 转发的 stdio 条目。
//
// 退化条目里令牌不进 argv：子进程的 argv 同机其他用户用 ps 就能读到，所以 argv 的
// --header 只写 `${MCODE_MCP_HEADER_<i>}` 这样的变量名，真实值放进条目的 env。
// mcp-remote 启动时会用自身环境变量展开 header 值里的 ${VAR}
// （0.15.2 dist 的 parseCommandLineArgs → substituteEnvVars），取值发生在子进程内。
// 冒号后不留空格是照上游 README 的建议：部分客户端不会转义 args 里的空格，
// 而 env 的值含空格不受影响，整个 "Bearer xxx" 就都塞进变量里。
func mcpServerEntry(name, rawURL string, headers [][2]string, allowsHTTP bool) map[string]any {
	if allowsHTTP {
		list := make([]any, 0, len(headers))
		for _, header := range headers {
			list = append(list, map[string]any{"name": header[0], "value": header[1]})
		}
		return map[string]any{
			"type":    "http",
			"name":    name,
			"url":     rawURL,
			"headers": list,
		}
	}

	args := []any{"-y", mcpRemotePackage, rawURL}
	env := make([]any, 0, len(headers))
	for i, header := range headers {
		// 下标在单个条目内唯一（各条目独立进程，互不影响）。
		key := fmt.Sprintf("MCODE_MCP_HEADER_%d", i)
		args = append(args, "--header", header[0]+":${"+key+"}")
		env = append(env, map[string]any{"name": key, "value": header[1]})
	}
	return map[string]any{
		"name":    name,
		"command": "npx",
		"args":    args,
		"env":     env,
	}
}

// mcpServers 生成本轮注入的 MCP 服务列表：内置服务按线程路由，外部端点一并写入，
// 名为 mcode 或 mcode_approval 的外部端点跳过（与 Claude/Codex 适配器一致）。
func (h *Harness) mcpServers(turn harness.Turn, allowsHTTP bool) []any {
	servers := make([]any, 0, 4)

	if url, token, workdirHeader := h.opts.BuiltinMCP.Endpoint(turn); url != "" {
		var headers [][2]string
		if token != "" {
			headers = append(headers, [2]string{"Authorization", "Bearer " + token})
		}
		if workdirHeader {
			headers = append(headers, [2]string{"X-Mcode-Workdir", turn.WorkDir})
		}
		servers = append(servers, mcpServerEntry("mcode", url, headers, allowsHTTP))
	}

	if h.opts.ExternalMCP != nil {
		for _, ep := range h.opts.ExternalMCP(turn.WorkDir, turn.ThreadID) {
			if ep.Name == "mcode" || ep.Name == "mcode_approval" {
				continue
			}
			var headers [][2]string
			if ep.Token != "" {
				headers = append(headers, [2]string{"Authorization", "Bearer " + ep.Token})
			}
			servers = append(servers, mcpServerEntry(ep.Name, ep.URL, headers, allowsHTTP))
		}
	}

	return servers
}

// startProcess 创建进程与连接；进程级资源一直保留到进程退出。
func (h *Harness) startProcess(ctx context.Context, turn harness.Turn) (*acpProcess, error) {
	bin := h.opts.Binary
	if bin == "" {
		bin = DefaultBinary(h.opts.Agent)
	}

	launchArgs := LaunchArgs(h.opts.Agent, turn.Mode)
	allArgs := append(append([]string{}, launchArgs...), h.opts.ExtraArgs...)
	if h.opts.TurnArgs != nil {
		allArgs = append(allArgs, h.opts.TurnArgs(turn)...)
	}

	processCtx := ctx
	cancelProcess := func() {}
	if h.opts.HoldBackgroundTasks {
		processCtx, cancelProcess = context.WithCancel(context.Background())
	} else if h.opts.Agent == AgentAgy {
		processCtx, cancelProcess = context.WithCancel(context.WithoutCancel(ctx))
	}
	started := false
	defer func() {
		if !started {
			cancelProcess()
		}
	}()
	var cmd *exec.Cmd
	var cleanup func()
	var err error

	if turn.Sandbox != nil {
		cmd, cleanup, err = turn.Sandbox.Command(processCtx, turn.WorkDir, turn.Mode, bin, allArgs...)
		if err != nil {
			return nil, fmt.Errorf("acp: sandbox command: %w", err)
		}
	} else {
		cmd = procattr.HideWindow(exec.CommandContext(processCtx, bin, allArgs...))
	}
	if cleanup != nil {
		defer func() {
			if !started {
				cleanup()
			}
		}()
	}

	setProcessGroup(cmd)
	cmd.WaitDelay = 3 * time.Second
	cmd.Dir = turn.WorkDir

	combinedOptsEnv := append(ExtraEnv(h.opts.Agent), h.opts.Env...)
	cmd.Env = buildEnv(h.authEnv(loginenv.Environ()), turn.WorkDir, combinedOptsEnv, turn.Env)

	stderrBuf := procutil.NewTailBuffer(8192)
	cmd.Stderr = stderrBuf

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("acp: stdin pipe: %w", err)
	}
	var stdoutPipe io.ReadCloser
	if h.opts.HoldBackgroundTasks {
		// 常驻进程由独立协程 Wait；管道由 Conn 关闭，避免 Wait 抢先丢掉末尾响应。
		reader, writer, pipeErr := os.Pipe()
		if pipeErr != nil {
			return nil, fmt.Errorf("acp: stdout pipe: %w", pipeErr)
		}
		stdoutPipe, cmd.Stdout = reader, writer
		defer writer.Close()
		defer func() {
			if !started {
				_ = reader.Close()
			}
		}()
	} else {
		stdoutPipe, err = cmd.StdoutPipe()
		if err != nil {
			return nil, fmt.Errorf("acp: stdout pipe: %w", err)
		}
	}

	if err := h.waitLaunchSlot(ctx); err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		tail := strings.TrimSpace(stderrBuf.String())
		if tail != "" {
			return nil, fmt.Errorf("acp start failed: %w (stderr: %s)", err, tail)
		}
		return nil, fmt.Errorf("acp start failed: %w", err)
	}
	// 按会话记录 agent 进程 PID，供「终止运行中的命令」定位命令子进程树。
	agentPID := cmd.Process.Pid
	h.roots.Set(turn.ThreadID, agentPID)
	p := &acpProcess{
		cmd: cmd, conn: NewConn(stdoutPipe, stdinPipe), stderr: stderrBuf,
		args: allArgs, cancel: cancelProcess, cleanup: cleanup,
		tasks: make(map[string]string), done: make(chan struct{}),
		resident: h.opts.HoldBackgroundTasks,
	}
	if p.resident {
		p.conn.SetNotificationHandler(func(method string, params json.RawMessage) {
			h.handleResidentNotification(p, method, params)
		})
	}
	started = true
	return p, nil
}

// Run 执行一轮 ACP 交互。
func (h *Harness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	if h.opts.HoldBackgroundTasks {
		_, err := h.runHeld(ctx, turn, emit, false)
		return err
	}
	p, err := h.startProcess(ctx, turn)
	if err != nil {
		return err
	}
	defer p.cancel()
	if p.cleanup != nil {
		defer p.cleanup()
	}
	defer h.roots.Clear(turn.ThreadID, p.cmd.Process.Pid)
	defer p.conn.Close()
	defer func() {
		if !p.waited {
			_ = p.conn.Close()
			_ = killProcessGroup(p.cmd, syscall.SIGKILL)
			_ = p.wait()
		}
	}()
	return h.runRound(ctx, turn, emit, p)
}

func (c *Conn) callAgyPrompt(ctx context.Context, params map[string]any, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := c.nextID.Add(1)
	idKey := strconv.FormatInt(id, 10)
	call := &pendingCall{ch: make(chan *rpcResponse, 1), done: make(chan struct{})}
	c.callsMu.Lock()
	select {
	case <-c.closed:
		c.callsMu.Unlock()
		return c.closedError()
	default:
		c.pendingCalls[idKey] = call
		c.callsMu.Unlock()
	}
	defer func() {
		c.callsMu.Lock()
		delete(c.pendingCalls, idKey)
		c.callsMu.Unlock()
	}()

	sendCancel := make(chan struct{})
	stop := make(chan struct{})
	defer close(stop)
	written := make(chan error, 1)
	go func() {
		err := c.writeMessage(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": "session/prompt", "params": params,
		})
		if err == nil {
			select {
			case <-stop:
				return
			case <-sendCancel:
				err = c.Notify("session/cancel", map[string]any{"sessionId": params["sessionId"]})
			}
		}
		written <- err
	}()

	if beforeAgyPromptSelect != nil {
		beforeAgyPromptSelect()
	}

	select {
	case response := <-call.ch:
		return c.finishCall(response, result)
	case err := <-written:
		return err
	case <-c.closed:
		select {
		case response := <-call.ch:
			return c.finishCall(response, result)
		default:
			return c.closedError()
		}
	case <-ctx.Done():
		// 应答与取消同时就绪时，优先返回已到达的应答，不补发 session/cancel。
		select {
		case response := <-call.ch:
			return c.finishCall(response, result)
		default:
		}
	}

	timer := time.NewTimer(agyCancelWait)
	defer timer.Stop()
	close(sendCancel)
	select {
	case err := <-written:
		if err != nil {
			return err
		}
	case <-c.closed:
		return c.closedError()
	case <-timer.C:
		return context.DeadlineExceeded
	}
	select {
	case response := <-call.ch:
		return c.finishCall(response, result)
	case <-c.closed:
		return c.closedError()
	case <-timer.C:
		return context.DeadlineExceeded
	}
}

// runRound 每轮重绑处理器；只有新进程执行 initialize 和会话建立。
func (h *Harness) runRound(ctx context.Context, turn harness.Turn, emit func(harness.Event), p *acpProcess) error {
	cmd, conn, stderrBuf, allArgs := p.cmd, p.conn, p.stderr, p.args
	attach := h.opts.HoldBackgroundTasks && turn.Attach
	var err error

	var sessionID string
	var sessionMu sync.Mutex
	var contextUsed, contextSize atomic.Int64
	// sessionCostBits 是 usage_update 最近上报的会话累计花费（USD，float64 位模式）。
	// 后端对同轮 EventUsage 整条替换，所以之后每条 EventUsage 都要带上它，否则会被覆盖成 0。
	var sessionCostBits atomic.Uint64

	setSessionID := func(id string) {
		sessionMu.Lock()
		defer sessionMu.Unlock()
		sessionID = id
	}
	getSessionID := func() string {
		sessionMu.Lock()
		defer sessionMu.Unlock()
		return sessionID
	}

	var toolsMu sync.Mutex
	type pendingTool struct {
		readPath   string
		background bool
		editPath   string
		editPatch  string
	}
	type startedTool struct {
		id      string
		command string
	}
	pendingTools := make(map[string]pendingTool)
	startedTools := make(map[string]bool)
	pendingOrder := make([]string, 0)
	var toolStartOrder []startedTool
	var planUpdateCount int
	planTurnID := turn.ID
	earlyReads := make(map[string]int)
	servedReads := make(map[string]bool)
	// writtenPaths 记录本轮已通过 fs/write_text_file 发过文件变更的路径，避免编辑工具补发的 diff 重复。
	writtenPaths := make(map[string]bool)
	toolsFinished := false
	var receivedText bool
	var autoDenyMu sync.Mutex
	var autoDenials []agyAutoDenial
	var userDenied bool

	setRequestHandler, setNotificationHandler, detach := h.roundHandlers(p)
	defer detach()

	// 注册对端发来的请求处理器
	setRequestHandler(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "elicitation/create":
			return handleElicitation(ctx, turn.Approver, params)

		case "fs/read_text_file":
			var p struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			target, err := resolveReadPath(turn.WorkDir, p.Path)
			if err != nil {
				return nil, err
			}
			content, err := os.ReadFile(target)
			if err != nil {
				return nil, err
			}
			if h.opts.Agent == AgentAgy {
				toolsMu.Lock()
				defer toolsMu.Unlock()
				if toolsFinished {
					return map[string]any{"content": string(content)}, nil
				}
				matched := false
				for i, id := range pendingOrder {
					pending, exists := pendingTools[id]
					if !exists {
						continue
					}
					if pending.readPath == p.Path {
						matched = true
						servedReads[id] = true
						delete(pendingTools, id)
						pendingOrder = append(pendingOrder[:i], pendingOrder[i+1:]...)
						_ = harness.Emit(emit, harness.Event{Type: harness.EventToolEnd, ToolID: id})
						break
					}
				}
				if !matched {
					earlyReads[p.Path]++
				}
			}
			return map[string]any{"content": string(content)}, nil

		case "fs/write_text_file":
			var p struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			if turn.Mode == harness.ModeReadOnly || turn.InteractionMode == "plan" {
				return nil, fmt.Errorf("read-only mode does not allow writing files: %s", p.Path)
			}
			target, rel, err := resolveWorkDirPath(turn.WorkDir, p.Path, turn.AdditionalDirs...)
			if err != nil {
				return nil, err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(target, []byte(p.Content), 0644); err != nil {
				return nil, err
			}
			if h.opts.Agent == AgentAgy {
				toolsMu.Lock()
				writtenPaths[rel] = true
				toolsMu.Unlock()
			}
			_ = harness.Emit(emit, harness.Event{
				Type: harness.EventFileChange,
				Path: rel,
			})
			return map[string]any{}, nil

		case "session/request_permission":
			var p struct {
				ToolCall struct {
					ToolCallID string          `json:"toolCallId"`
					Name       string          `json:"name"`
					ToolName   string          `json:"toolName"`
					Title      string          `json:"title"`
					Kind       string          `json:"kind"`
					RawInput   json.RawMessage `json:"rawInput"`
					Input      json.RawMessage `json:"input"`
				} `json:"toolCall"`
				Options []struct {
					OptionID string `json:"optionId"`
					Name     string `json:"name"`
					Kind     string `json:"kind"`
				} `json:"options"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			if h.opts.Agent == AgentAgy && strings.HasPrefix(p.ToolCall.ToolCallID, "interaction_") && len(p.Options) > 0 {
				interactor, ok := turn.Approver.(harness.Interactor)
				if !ok {
					return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
				}
				prompt := p.ToolCall.Title
				if prompt == "" {
					prompt = "请选择"
				}
				req := harness.ApprovalRequest{Kind: harness.RequestKindQuestion, ToolName: "ask_question", ToolID: p.ToolCall.ToolCallID, Summary: p.ToolCall.Title,
					Questions: []harness.Question{{ID: p.ToolCall.ToolCallID, Prompt: prompt}}}
				for _, option := range p.Options {
					label := option.Name
					if label == "" {
						label = option.OptionID
					}
					req.Questions[0].Choices = append(req.Questions[0].Choices, harness.QuestionChoice{Label: label})
				}
				response, interactErr := interactor.Interact(ctx, req)
				if interactErr != nil {
					harness.AckDelivery(turn.Approver, req, interactErr)
					return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
				}
				var selected string
				if !response.Skipped && len(response.Answers[p.ToolCall.ToolCallID]) > 0 {
					answer := response.Answers[p.ToolCall.ToolCallID][0]
					for _, option := range p.Options {
						label := option.Name
						if label == "" {
							label = option.OptionID
						}
						if answer == label || answer == option.OptionID {
							selected = option.OptionID
							break
						}
					}
				}
				if selected == "" || ctx.Err() != nil {
					payload := acpCancelledOutcome()
					return acpApprovalResponse{payload: payload, onAck: func(err error) { harness.AckDelivery(turn.Approver, req, err) }}, nil
				}
				payload := map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": selected}}
				return acpApprovalResponse{payload: payload, onAck: func(err error) { harness.AckDelivery(turn.Approver, req, err) }}, nil
			}

			toolID := p.ToolCall.ToolCallID
			toolName := p.ToolCall.Name
			if toolName == "" {
				toolName = p.ToolCall.ToolName
			}
			if toolName == "" {
				toolName = p.ToolCall.Title
			}
			input := p.ToolCall.Input
			if len(input) == 0 {
				input = p.ToolCall.RawInput
			}
			summary := p.ToolCall.Title
			if summary == "" {
				summary = toolName
			}

			// 命令类工具调用：执行类 kind、agy 的命令输入或常见 shell 工具名。
			isCommandCall := p.ToolCall.Kind == "execute" ||
				(h.opts.Agent == AgentAgy && p.ToolCall.Kind == "" && agyCommand(input) != "") ||
				toolName == "bash" || toolName == "shell_command" || toolName == "run_command"

			// Droid 的计划退出（Approve Spec / ExitSpecMode）单独走计划类审批。
			optionIDs := make([]string, 0, len(p.Options))
			optionNames := make([]string, 0, len(p.Options))
			for _, opt := range p.Options {
				optionIDs = append(optionIDs, opt.OptionID)
				optionNames = append(optionNames, opt.Name)
			}
			planExitName := p.ToolCall.Name
			if planExitName == "" {
				planExitName = p.ToolCall.ToolName
			}
			planExit := h.opts.Agent == AgentDroid && DroidIsPlanExit(p.ToolCall.Title, planExitName, optionNames, input)

			var decision harness.Decision
			var approveReq harness.ApprovalRequest
			var approveErr error
			var autoDenied bool
			var feedback string
			lockedCommand := isCommandCall && turn.HardLocked && strings.TrimSpace(turn.HardLockOverride) == ""
			// plan 回合的 Mode 会被改成只读，用户选的档位存放在 PlanExitMode 里，需要一并判断。
			autoApproveAgy := h.opts.Agent == AgentAgy && (turn.Mode == harness.ModeFullAccess || turn.PlanExitMode == harness.ModeFullAccess) && !lockedCommand
			useApprover := turn.Approver != nil && !autoApproveAgy
			if useApprover {
				approveReq = harness.ApprovalRequest{
					ToolID:   toolID,
					ToolName: toolName,
					Input:    input,
					Summary:  summary,
				}
				if isCommandCall {
					approveReq.Kind = harness.RequestKindCommand
				}
				if planExit {
					// 计划正文随计划类请求带给前端（planCompleted 事件）。
					approveReq.Kind = harness.RequestKindPlan
					approveReq.Summary = "执行计划"
					approveReq.Plan = DroidPlanMarkdown(input)
				}
				if !isCommandCall && !planExit {
					switch p.ToolCall.Kind {
					case "read", "search":
						approveReq.ReadOnly = true
					default:
						switch toolName {
						case "read_file", "view_file", "list_dir", "grep_search", "find_by_name":
							approveReq.ReadOnly = true
						}
					}
				}
				var dec harness.Decision
				var err error
				if interactor, ok := turn.Approver.(harness.Interactor); ok && interactor != nil {
					var resp harness.ApprovalResponse
					resp, err = interactor.Interact(ctx, approveReq)
					dec = resp.Decision
					autoDenied = resp.AutoDenied
					feedback = strings.TrimSpace(resp.Feedback)
				} else {
					dec, err = turn.Approver.Approve(ctx, approveReq)
				}
				if err != nil {
					approveErr = err
					harness.AckDelivery(turn.Approver, approveReq, err)
					decision = harness.DecisionDeny
				} else {
					decision = dec
				}
			} else if autoApproveAgy && turn.Approver != nil {
				decision = harness.DecisionAllow
			} else {
				decision = harness.DecisionDeny
				if turn.Mode == harness.ModeFullAccess {
					decision = harness.DecisionAllow
				} else if !planExit && (turn.Mode == harness.ModeAuto || turn.Mode == harness.ModeAutoAcceptEdits) {
					name := toolName
					arg := ""
					var values map[string]any
					_ = json.Unmarshal(input, &values)
					command := ""
					if h.opts.Agent == AgentAgy {
						command = agyCommand(input)
					} else {
						command, _ = values["command"].(string)
					}
					if isCommandCall {
						name, arg = "bash", command
					} else if p.ToolCall.Kind == "edit" {
						name = "edit_file"
					}
					if harness.Decide(turn.Mode, nil, name, arg, p.ToolCall.Kind == "read") == harness.DecisionAllow {
						decision = harness.DecisionAllow
					}
				}
				if decision == harness.DecisionDeny && !planExit {
					autoDenied = true
					feedback = "当前权限模式不允许该操作"
				}
			}
			if h.opts.Agent == AgentAgy && decision == harness.DecisionDeny && approveErr == nil && !planExit {
				autoDenyMu.Lock()
				if autoDenied {
					autoDenials = append(autoDenials, agyAutoDenial{
						toolName: toolName,
						summary:  summary,
						feedback: feedback,
					})
				} else {
					userDenied = true
				}
				autoDenyMu.Unlock()
			}

			var selectedID string
			if h.opts.Agent == AgentAgy {
				wantedKind := "reject_once"
				if decision == harness.DecisionAllow {
					wantedKind = "allow_once"
				}
				for _, opt := range p.Options {
					if opt.Kind == wantedKind {
						selectedID = opt.OptionID
						break
					}
				}
			} else if planExit {
				// 批准时按用户所选档位选 Droid 的高档自动执行选项（Plan 回合的 Mode 是只读），驳回固定 cancel。
				if decision == harness.DecisionAllow {
					runMode := turn.Mode
					if turn.PlanExitMode != "" {
						runMode = turn.PlanExitMode
					}
					selectedID = DroidPlanExitApprovalID(string(runMode), optionIDs)
				} else {
					selectedID = droidCancelOption
				}
			} else if decision == harness.DecisionAllow {
				for _, opt := range p.Options {
					if opt.Kind == "allow_once" {
						selectedID = opt.OptionID
						break
					}
				}
				for _, opt := range p.Options {
					if selectedID != "" {
						break
					}
					if opt.Kind == "allow_once" || opt.Kind == "allow_always" || opt.Kind == "allow_session" ||
						strings.Contains(strings.ToLower(opt.Name), "allow") ||
						strings.Contains(strings.ToLower(opt.Name), "proceed") {
						selectedID = opt.OptionID
						break
					}
				}
				if selectedID == "" && len(p.Options) > 0 {
					selectedID = p.Options[0].OptionID
				}
			} else {
				for _, opt := range p.Options {
					if opt.Kind == "reject_once" {
						selectedID = opt.OptionID
						break
					}
				}
				for _, opt := range p.Options {
					if selectedID != "" {
						break
					}
					if opt.Kind != "reject_always" && (strings.Contains(strings.ToLower(opt.Name), "reject") ||
						strings.Contains(strings.ToLower(opt.Name), "cancel") ||
						strings.Contains(strings.ToLower(opt.Name), "deny")) {
						selectedID = opt.OptionID
						break
					}
				}
				for _, opt := range p.Options {
					if selectedID != "" {
						break
					}
					if opt.Kind == "reject_always" {
						selectedID = opt.OptionID
						break
					}
				}
				if selectedID == "" && len(p.Options) > 0 {
					selectedID = p.Options[len(p.Options)-1].OptionID
				}
			}

			payload := map[string]any{
				"outcome": map[string]any{
					"outcome":  "selected",
					"optionId": selectedID,
				},
			}
			if approvalCancelled(ctx, approveErr) || (h.opts.Agent == AgentAgy && selectedID == "") {
				payload = acpCancelledOutcome()
			}
			if useApprover && approveErr == nil {
				return acpApprovalResponse{
					payload: payload,
					onAck: func(writeErr error) {
						harness.AckDelivery(turn.Approver, approveReq, writeErr)
					},
				}, nil
			}
			return payload, nil

		default:
			return nil, errors.New("method not found")
		}
	})

	// 注册通知分发处理器
	onNotification := func(method string, params json.RawMessage) {
		if method != "session/update" {
			return
		}
		var message struct {
			Update struct {
				SessionUpdate string          `json:"sessionUpdate"`
				Content       json.RawMessage `json:"content"`
				RawOutput     json.RawMessage `json:"rawOutput"`
				ToolCallID    string          `json:"toolCallId"`
				Name          string          `json:"name"`
				ToolName      string          `json:"toolName"`
				Title         string          `json:"title"`
				RawInput      json.RawMessage `json:"rawInput"`
				Input         json.RawMessage `json:"input"`
				Entries       json.RawMessage `json:"entries"`
				Locations     []struct {
					Path string `json:"path"`
				} `json:"locations"`
				Status string `json:"status"`
				Output string `json:"output"`
				Error  any    `json:"error"`
				Diff   string `json:"diff"`
				Path   string `json:"path"`
				Patch  string `json:"patch"`
				Used   int64  `json:"used"`
				Size   int64  `json:"size"`
				Cost   *struct {
					Amount   float64 `json:"amount"`
					Currency string  `json:"currency"`
				} `json:"cost"`
			} `json:"update"`
		}
		if err := json.Unmarshal(params, &message); err != nil {
			return
		}
		u := message.Update
		if u.SessionUpdate == "tool_call_update" {
			toolsMu.Lock()
			served := servedReads[u.ToolCallID]
			toolsMu.Unlock()
			if served {
				return
			}
		}

		switch u.SessionUpdate {
		case "plan":
			if len(u.Entries) == 0 {
				return
			}
			input, err := json.Marshal(map[string]json.RawMessage{"plan": u.Entries})
			if err != nil {
				return
			}
			if planTurnID == "" {
				planTurnID = uuid.NewString()
			}
			planUpdateCount++
			toolID := fmt.Sprintf("plan-%s-%d", planTurnID, planUpdateCount)
			_ = harness.Emit(emit, harness.Event{
				Type: harness.EventToolStart, ToolID: toolID, ToolName: "update_plan", Input: input,
			})
			_ = harness.Emit(emit, harness.Event{
				Type: harness.EventToolEnd, ToolID: toolID, ToolName: "update_plan",
			})

		case "agent_message_chunk":
			var content struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(u.Content, &content)
			if content.Text != "" {
				receivedText = true
				_ = harness.Emit(emit, harness.Event{
					Type: harness.EventText,
					Text: content.Text,
				})
			}

		case "agent_thought_chunk":
			var content struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(u.Content, &content)
			if content.Text != "" {
				_ = harness.Emit(emit, harness.Event{
					Type: harness.EventReasoning,
					Text: content.Text,
				})
			}

		case "tool_call":
			toolName := u.Name
			if toolName == "" {
				toolName = u.ToolName
			}
			if toolName == "" {
				toolName = u.Title
			}
			input := u.Input
			if len(input) == 0 {
				input = u.RawInput
			}
			// 只有命令工具会转后台；读写文件的工具可能回显含后台任务标记的文件内容。
			commandTool := toolName == "bash" || toolName == "shell_command" || toolName == "run_command" || agyCommand(input) != ""
			editPath, editPatch := "", ""
			if h.opts.Agent == AgentAgy {
				rawInput := u.RawInput
				if len(rawInput) == 0 {
					rawInput = u.Input
				}
				locationPath := ""
				if len(u.Locations) > 0 {
					locationPath = u.Locations[0].Path
				}
				// agy 的编辑工具常常只给路径和新旧片段，不给 diff；在 pending 时文件还是旧内容，
				// 这时生成 patch 才有真实行号（对齐 t3code 在 pending 时读 pre-edit 文件的做法）。
				if targetPath, chunks, ok := agyEditInput(rawInput, locationPath); ok {
					if target, rel, err := resolveWorkDirPath(turn.WorkDir, targetPath, turn.AdditionalDirs...); err == nil {
						editPath, editPatch = rel, agyEditPatch(target, chunks)
					}
				}
			}
			toolsMu.Lock()
			if toolsFinished {
				toolsMu.Unlock()
				break
			}
			readPath := ""
			if h.opts.Agent == AgentAgy {
				readPath = agyReadPath(u.Title, u.Locations, input)
			}
			pendingTools[u.ToolCallID] = pendingTool{readPath: readPath, editPath: editPath, editPatch: editPatch}
			if commandTool {
				startedTools[u.ToolCallID] = true
			}
			if h.opts.HoldBackgroundTasks && commandTool {
				var raw struct{ CommandLine string }
				_ = json.Unmarshal(u.RawInput, &raw)
				toolStartOrder = append(toolStartOrder, startedTool{id: u.ToolCallID, command: raw.CommandLine})
			}
			if readPath != "" {
				pendingOrder = append(pendingOrder, u.ToolCallID)
			}
			_ = harness.Emit(emit, harness.Event{
				Type:     harness.EventToolStart,
				ToolID:   u.ToolCallID,
				ToolName: toolName,
				Input:    input,
			})
			if readPath != "" && earlyReads[readPath] > 0 {
				servedReads[u.ToolCallID] = true
				delete(pendingTools, u.ToolCallID)
				for i, id := range pendingOrder {
					if id == u.ToolCallID {
						pendingOrder = append(pendingOrder[:i], pendingOrder[i+1:]...)
						break
					}
				}
				earlyReads[readPath]--
				_ = harness.Emit(emit, harness.Event{Type: harness.EventToolEnd, ToolID: u.ToolCallID})
			}
			toolsMu.Unlock()

		case "tool_call_update":
			if h.opts.HoldBackgroundTasks {
				toolsMu.Lock()
				owned := startedTools[u.ToolCallID] && !toolsFinished
				toolsMu.Unlock()
				if owned && h.backgroundTaskStarts(p, u.ToolCallID, []string{string(u.Content), string(u.RawOutput), u.Output}, emit) && attach {
					toolsMu.Lock()
					if pending, ok := pendingTools[u.ToolCallID]; ok {
						pending.background = true
						pendingTools[u.ToolCallID] = pending
					}
					toolsMu.Unlock()
				}
			}
			if u.Status == "pending" || u.Status == "in_progress" {
				break
			}
			output := ""
			var content []struct {
				Text    string `json:"text"`
				Content struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			if json.Unmarshal(u.Content, &content) == nil {
				for _, item := range content {
					if item.Content.Text != "" {
						output += item.Content.Text
					} else {
						output += item.Text
					}
				}
			}
			if output == "" {
				var raw struct {
					Output string `json:"output"`
				}
				_ = json.Unmarshal(u.RawOutput, &raw)
				output = raw.Output
			}
			if output == "" {
				output = u.Output
			}
			if h.opts.Agent == AgentAgy {
				combined, exitCode, hasExit := agyCommandResult(u.RawOutput)
				if output == "" {
					output = combined
				}
				// t3code 不把非 0 退出码当工具失败（AntigravityProtocol.ts 只把它放进 item.exitCode）；
				// mcode 没有这个字段，就把退出码附在输出末尾显示。
				if hasExit && exitCode != 0 {
					output = agyWithExitCode(output, exitCode)
				}
			}
			toolsMu.Lock()
			if toolsFinished {
				toolsMu.Unlock()
				break
			}
			if servedReads[u.ToolCallID] {
				toolsMu.Unlock()
				return
			}
			pending := pendingTools[u.ToolCallID]
			delete(pendingTools, u.ToolCallID)
			for i, id := range pendingOrder {
				if id == u.ToolCallID {
					pendingOrder = append(pendingOrder[:i], pendingOrder[i+1:]...)
					break
				}
			}
			isErr := u.Status == "failed" || u.Status == "error" || u.Error != nil
			_ = harness.Emit(emit, harness.Event{
				Type:    harness.EventToolEnd,
				ToolID:  u.ToolCallID,
				Output:  output,
				IsError: isErr,
			})
			fileChanged := h.opts.Agent == AgentAgy && u.Status == "completed" && !isErr && pending.editPatch != "" &&
				u.Path == "" && u.Patch == "" && u.Diff == "" && !writtenPaths[pending.editPath]
			toolsMu.Unlock()
			if fileChanged {
				_ = harness.Emit(emit, harness.Event{
					Type:  harness.EventFileChange,
					Path:  pending.editPath,
					Patch: pending.editPatch,
				})
			}

		case "usage_update":
			costOK := u.Cost != nil && strings.EqualFold(u.Cost.Currency, "USD") && u.Cost.Amount > 0
			hasContext := u.Used > 0 || u.Size > 0
			if costOK {
				sessionCostBits.Store(math.Float64bits(u.Cost.Amount))
			}
			// 只带 cost 的事件不覆盖已有上下文数据。
			if hasContext {
				contextUsed.Store(u.Used)
				contextSize.Store(u.Size)
			}
			if hasContext || costOK {
				_ = harness.Emit(emit, harness.Event{
					Type: harness.EventUsage,
					Usage: &harness.Usage{
						InputTokens:   contextUsed.Load(),
						ContextTokens: contextUsed.Load(),
						ContextWindow: contextSize.Load(),
						CostUSD:       math.Float64frombits(sessionCostBits.Load()),
					},
				})
			}
		}

		// 提取可能存在的文件变更 / diff
		diffPath := u.Path
		diffPatch := u.Patch
		if diffPatch == "" && u.Diff != "" {
			diffPatch = u.Diff
		}
		if diffPath != "" {
			_ = harness.Emit(emit, harness.Event{
				Type:  harness.EventFileChange,
				Path:  diffPath,
				Patch: diffPatch,
			})
		}
	}
	if !attach {
		setNotificationHandler(onNotification, false)
	}

	// 监听 ctx 取消，发送 session/cancel 并优雅杀灭进程组
	shutdownCh := make(chan struct{})
	cancelDone := make(chan struct{})
	stopCancelWatcher := sync.OnceFunc(func() {
		close(shutdownCh)
		if h.opts.HoldBackgroundTasks || h.opts.Agent == AgentAgy {
			<-cancelDone
		}
	})
	defer stopCancelWatcher()

	go func() {
		defer close(cancelDone)
		select {
		case <-shutdownCh:
			return
		case <-ctx.Done():
			if h.opts.Agent == AgentAgy {
				select {
				case <-shutdownCh:
					return
				default:
				}
			}
			// 1. 发送 session/cancel 通知
			if currID := getSessionID(); currID != "" {
				_ = conn.Notify("session/cancel", map[string]any{"sessionId": currID})
			}
			// 2. 向进程组发送 SIGTERM
			_ = killProcessGroup(cmd, syscall.SIGTERM)
			// 3. 超时 3 秒后强制 SIGKILL 兜底
			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()
			select {
			case <-shutdownCh:
				return
			case <-timer.C:
				_ = killProcessGroup(cmd, syscall.SIGKILL)
			}
		}
	}()

	// 步骤 1: initialize
	initResp := p.initResp
	initParams := map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs": map[string]any{
				"readTextFile":  true,
				"writeTextFile": true,
			},
			"terminal":    false,
			"elicitation": map[string]any{"form": map[string]any{}},
		},
		"clientInfo": map[string]any{
			"name":    "mcode",
			"title":   "mcode",
			"version": "1.0.0",
		},
	}
	if !p.initialized {
		if err := conn.Call(ctx, "initialize", initParams, &initResp); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			tail := strings.TrimSpace(stderrBuf.String())
			if tail != "" {
				return fmt.Errorf("acp initialize failed: %w (stderr: %s)", err, tail)
			}
			return fmt.Errorf("acp initialize failed: %w", err)
		}
		p.initResp = initResp
		p.initialized = true
		if h.opts.Agent == AgentAgy && slices.ContainsFunc(initResp.AuthMethods, func(method acpAuthMethod) bool {
			return method.ID == "oauth-personal" && method.Type != "terminal"
		}) {
			if err := conn.Call(ctx, "authenticate", map[string]any{"methodId": "oauth-personal"}, nil); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("acp authenticate failed: %w", err)
			}
		}
	}

	// Cline 没有推理强度的 ACP 配置项：开会话之前把强度写进它自己的 providers.json。
	if h.opts.Agent == AgentCline && turn.Effort != "" {
		_ = UpdateClineReasoning(turn.Effort)
	}

	// 步骤 2 & 3: session/load 或 session/new
	canLoad := initResp.AgentCapabilities.LoadSession
	canResume := initResp.AgentCapabilities.SessionCapabilities.Resume != nil
	canClose := initResp.AgentCapabilities.SessionCapabilities.Close != nil
	authMethods := initResp.AuthMethods
	// 会话级的 MCP 服务列表：Agent 声明支持 HTTP MCP 时直连，否则退化为 npx mcp-remote。
	// shortcut: agy 1.5.9 未声明 mcpCapabilities.http 但实测支持直连，agy 开始声明后可删
	allowsHTTP := h.opts.Agent == AgentAgy || initResp.AgentCapabilities.MCPCapabilities.HTTP
	// 记下能力，供 HostToolsAvailable 判断是否真的需要 npx。
	h.mcpHTTP.Store(allowsHTTP)
	servers := h.mcpServers(turn, allowsHTTP)

	// sessionOptions 缓存会话响应里的 configOptions，用于下发 effort 与快速模式。
	var sessionOptions []map[string]any
	// sessionModes 缓存会话响应里广告的会话模式，用于下发计划模式与运行时权限档位。
	var sessionModes acpSessionModes

	createOrResume := func() (string, error) {
		type sessionSetupResult struct {
			SessionID     string           `json:"sessionId"`
			ConfigOptions []map[string]any `json:"configOptions"`
			Modes         acpSessionModes  `json:"modes"`
		}

		cwd := turn.WorkDir
		if h.opts.Agent == AgentAgy {
			// agy 按真实路径判定编辑目标是否在工作区内，传软链路径会导致工作区内编辑触发越界权限请求。
			if real, err := filepath.EvalSymlinks(turn.WorkDir); err == nil {
				cwd = real
			}
		}
		params := map[string]any{"cwd": cwd, "mcpServers": servers}
		if h.opts.Agent == AgentAgy && len(turn.AdditionalDirs) > 0 {
			params["additionalDirectories"] = turn.AdditionalDirs
		}
		if turn.SessionID != "" {
			if !canLoad && !canResume {
				// Agent 不支持 session/load 时明确报错，
				// 而不是静默开一个丢失上下文的新会话。
				return "", fmt.Errorf("this ACP agent (%s) cannot resume the original session", DisplayName(h.opts.Agent))
			}
			var res sessionSetupResult
			method := "session/load"
			if canResume {
				method = "session/resume"
			}
			params["sessionId"] = turn.SessionID
			err := conn.Call(ctx, method, params, &res)
			if err != nil {
				return "", err
			}
			sessionOptions = res.ConfigOptions
			sessionModes = res.Modes
			return turn.SessionID, nil
		}

		var res sessionSetupResult
		err := conn.Call(ctx, "session/new", params, &res)
		if err != nil {
			return "", err
		}
		sessionOptions = res.ConfigOptions
		sessionModes = res.Modes
		return res.SessionID, nil
	}

	sid := p.sessionID
	reusedSession := sid != ""
	sessionOptions, sessionModes = p.sessionOptions, p.sessionModes
	if sid == "" {
		sid, err = createOrResume()
	}
	// 若需要且属于认证错误，且非 Droid，尝试执行 authenticate 后重试。
	// ACP 规定客户端不得传 terminal 方法：没有其他方法时不重试，保留原错误。
	if err != nil && h.opts.Agent != AgentDroid && acpAuthRequired(err) {
		if methodID := pickAuthMethod(authMethods); methodID != "" {
			_ = conn.Call(ctx, "authenticate", map[string]any{"methodId": methodID}, nil)
			sid, err = createOrResume()
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if turn.SessionID != "" && strings.Contains(err.Error(), "Failed to read session") {
			return fmt.Errorf("原会话 %s 的数据已不存在（所属账号可能已删除），请新开会话", turn.SessionID)
		}
		tail := strings.TrimSpace(stderrBuf.String())
		if tail != "" {
			return fmt.Errorf("acp session setup failed: %w (stderr: %s)", err, tail)
		}
		return fmt.Errorf("acp session setup failed: %w", err)
	}

	setSessionID(sid)
	h.residentMu.Lock()
	p.sessionID, p.sessionOptions, p.sessionModes = sid, sessionOptions, sessionModes
	h.residentMu.Unlock()

	// 步骤 4: 发出 harness.EventSession
	_ = harness.Emit(emit, harness.Event{
		Type:      harness.EventSession,
		SessionID: sid,
	})

	// 步骤 5: 设置模型
	// "default" 是模型目录的占位行，交给 Agent 自己的默认模型，不发 set_model。
	if h.opts.Agent == AgentAgy {
		configuredModes, configuredOptions := sessionModes, sessionOptions
		if err := configureAgy(ctx, conn, sid, turn, &configuredModes, &configuredOptions); err != nil {
			return err
		}
		if h.opts.HoldBackgroundTasks {
			sessionModes, sessionOptions = configuredModes, configuredOptions
			p.sessionModes, p.sessionOptions = sessionModes, sessionOptions
		}
	} else if turn.Model != "" && turn.Model != "default" {
		_ = conn.Call(ctx, "session/set_model", map[string]any{
			"sessionId": sid,
			"modelId":   turn.Model,
		}, nil)
	}

	// 步骤 5.5: 计划模式与运行时权限档位
	// Droid 通过会话广告的会话模式切档：
	// 只有目标模式与会话当前模式不同才下发；Agent 未广告目标模式时 set_mode 会失败，忽略即可。
	if h.opts.Agent == AgentDroid {
		wanted := DroidRequestedMode(turn.InteractionMode, string(turn.Mode), sessionModes.IDs())
		if wanted != "" && wanted != sessionModes.CurrentModeID {
			_ = conn.Call(ctx, "session/set_mode", map[string]any{
				"sessionId": sid,
				"modeId":    wanted,
			}, nil)
		}
	}

	// 推理强度与快速模式：
	// 二者都通过会话广告的配置项下发（session/set_config_option）；Agent 未广告对应配置项时不传。
	// effort 与当前值相同则跳过；除 droid 外下发失败忽略。
	effortConfigID, currentEffort, fastConfigID, currentFast := selectACPConfigOptions(sessionOptions)
	if h.opts.Agent != AgentAgy && turn.Effort != "" && effortConfigID != "" && turn.Effort != currentEffort {
		err := conn.Call(ctx, "session/set_config_option", map[string]any{
			"sessionId": sid,
			"configId":  effortConfigID,
			"value":     turn.Effort,
		}, nil)
		if err != nil && h.opts.Agent == AgentDroid {
			return fmt.Errorf("acp set effort: %w", err)
		}
	}
	if fastConfigID != "" {
		wanted := "false"
		if turn.FastMode {
			wanted = "true"
		}
		if wanted != currentFast {
			err := conn.Call(ctx, "session/set_config_option", map[string]any{
				"sessionId": sid,
				"configId":  fastConfigID,
				"value":     wanted,
			}, nil)
			if err != nil && h.opts.Agent == AgentDroid {
				return fmt.Errorf("acp set fast mode: %w", err)
			}
			if err == nil && h.opts.HoldBackgroundTasks {
				for _, option := range sessionOptions {
					if option["id"] == fastConfigID {
						option["currentValue"] = wanted
					}
				}
			}
		}
	}

	var transcriptPath string
	var transcriptOffset int64
	if attach && reusedSession {
		transcriptPath, transcriptOffset = p.transcriptPath, p.transcriptSize
	} else if h.opts.HoldBackgroundTasks {
		transcriptPath, transcriptOffset = agyTranscriptPosition(p, sid)
	}
	scanTranscript := func(path, fallback string) {
		offset := transcriptOffset
		if path != transcriptPath {
			offset = 0
		}
		h.backgroundTaskTranscriptStarts(p, path, offset, func(description string) string {
			if description != "" {
				for index := len(toolStartOrder) - 1; index >= 0; index-- {
					tool := toolStartOrder[index]
					if tool.id != "" && tool.command == description {
						return tool.id
					}
				}
			}
			return fallback
		}, func(event harness.Event) {
			if pending, ok := pendingTools[event.ToolID]; ok {
				pending.background = true
				pendingTools[event.ToolID] = pending
			}
			_ = harness.Emit(emit, event)
		})
	}
	lastPendingTool := func() string {
		for index := len(toolStartOrder) - 1; index >= 0; index-- {
			toolID := toolStartOrder[index].id
			if pending, ok := pendingTools[toolID]; toolID != "" && ok && !pending.background {
				return toolID
			}
		}
		return ""
	}
	var spontaneous bool
	var promptErr error
	if attach {
		setNotificationHandler(onNotification, true)
		attachIdle := h.attachIdleTimeout()
		ticker := time.NewTicker(h.attachPollInterval())
		scannedPath, scannedOffset := transcriptPath, transcriptOffset
		taskPath, taskSize, taskFallback := transcriptPath, transcriptOffset, ""
		seenFinalStep := false
		waitStart := time.Now()
	waitForReply:
		for {
			path, size := agyTranscriptPosition(p, sid)
			if path != "" && !seenFinalStep {
				if path != scannedPath || size < scannedOffset {
					scannedPath, scannedOffset = path, 0
				}
				if size > scannedOffset {
					scannedOffset = readAgyTranscript(path, scannedOffset, func(step agyTranscriptStep) {
						if step.Type == "PLANNER_RESPONSE" && step.Status == "DONE" && len(step.ToolCalls) == 0 {
							seenFinalStep = true
						}
					})
				}
			}
			p.notificationMu.Lock()
			h.residentMu.Lock()
			lastUpdate := p.lastUpdate
			if lastUpdate.Before(waitStart) {
				lastUpdate = waitStart
			}
			idleFor := time.Since(lastUpdate)
			h.residentMu.Unlock()
			ready := false
			if seenFinalStep || idleFor >= attachIdle {
				toolsMu.Lock()
				if toolID := lastPendingTool(); toolID != "" {
					if path != taskPath || size != taskSize || toolID != taskFallback {
						taskPath, taskSize, taskFallback = path, size, toolID
						scanTranscript(path, toolID)
					}
				}
				if lastPendingTool() == "" {
					if receivedText {
						ready = seenFinalStep || idleFor >= 30*time.Second
					} else {
						ready = idleFor >= attachIdle
					}
				}
				toolsMu.Unlock()
				if ready && receivedText {
					spontaneous = true
					p.notificationHandler = nil
				}
			}
			p.notificationMu.Unlock()
			if ready {
				promptErr = ctx.Err()
				break
			}
			select {
			case <-ctx.Done():
				promptErr = ctx.Err()
				break waitForReply
			case <-conn.closed:
				promptErr = conn.closedError()
				break waitForReply
			case <-ticker.C:
			}
		}
		ticker.Stop()
	}

	// 步骤 6: session/prompt
	promptItems := []any{
		map[string]any{
			"type": "text",
			"text": turn.Prompt,
		},
	}
	for _, imgPath := range turn.Images {
		// 非图片附件（PDF、docx 等）不当图片下发，其路径已由上层写进 prompt。
		// 图片类型由 harness.ImageMediaType 判定：扩展名优先，扩展名不可靠时按文件内容嗅探。
		mediaType, ok := harness.ImageMediaType(imgPath)
		if !ok {
			log.Printf("acp: skip image %s: unsupported image type %q", imgPath, filepath.Ext(imgPath))
			continue
		}
		data, readErr := os.ReadFile(imgPath)
		if readErr == nil {
			promptItems = append(promptItems, map[string]any{
				"type":     "image",
				"mimeType": mediaType,
				"data":     base64.StdEncoding.EncodeToString(data),
			})
		}
	}
	var agyDBPath string
	agyStepStart := int64(-1)
	if h.opts.Agent == AgentAgy && promptErr == nil {
		agyDBPath = agySessionDB(agyGeminiDir(allArgs, cmd.Env), sid)
		if spontaneous {
			if reusedSession && p.agyStepStartOK {
				agyStepStart = p.agyStepStart
			} else {
				agyDBPath = ""
			}
		} else if start, ok := agyStepIndex(agyDBPath); ok {
			agyStepStart = start
		} else {
			agyDBPath = ""
		}
	}

	var promptResult struct {
		StopReason string         `json:"stopReason"`
		Usage      acpPromptUsage `json:"usage"`
	}
	agyPrompt := h.opts.Agent == AgentAgy && !spontaneous && promptErr == nil
	if spontaneous {
		promptResult.StopReason = "end_turn"
	} else if promptErr == nil {
		if agyPrompt {
			stopCancelWatcher()
			callAgyPromptOnce := func(items []any) error {
				params := map[string]any{
					"sessionId": sid,
					"prompt":    items,
				}
				if agyDBPath != "" {
					promptCtx, cancelPrompt := context.WithCancel(ctx)
					pollStop := make(chan struct{})
					pollDone := make(chan struct{})
					stallErrCh := make(chan error, 1)
					go func() {
						defer close(pollDone)
						ticker := time.NewTicker(agyStepPollInterval)
						defer ticker.Stop()
						var firstErrAt time.Time
						for {
							select {
							case <-pollStop:
								return
							case <-promptCtx.Done():
								return
							case <-ticker.C:
								if text, ok := agyNewStepError(agyDBPath, agyStepStart); ok {
									now := time.Now()
									if firstErrAt.IsZero() {
										firstErrAt = now
									}
									if now.Sub(firstErrAt) >= agyStepErrorStall {
										stallErrCh <- fmt.Errorf("agy turn failed: %s", text)
										cancelPrompt()
										return
									}
								} else {
									firstErrAt = time.Time{}
								}
							}
						}
					}()
					err := conn.callAgyPrompt(promptCtx, params, &promptResult)
					close(pollStop)
					<-pollDone
					cancelPrompt()
					select {
					case detectedStallErr := <-stallErrCh:
						if ctx.Err() == nil && detectedStallErr != nil {
							err = detectedStallErr
						}
					default:
					}
					return err
				}
				return conn.callAgyPrompt(ctx, params, &promptResult)
			}

			var totalUsage acpPromptUsage
			currentItems := promptItems
			for cont := 0; ; cont++ {
				promptResult = struct {
					StopReason string         `json:"stopReason"`
					Usage      acpPromptUsage `json:"usage"`
				}{}
				promptErr = callAgyPromptOnce(currentItems)
				totalUsage.InputTokens += promptResult.Usage.InputTokens
				totalUsage.OutputTokens += promptResult.Usage.OutputTokens
				totalUsage.ThoughtTokens += promptResult.Usage.ThoughtTokens
				totalUsage.CachedReadTokens += promptResult.Usage.CachedReadTokens

				autoDenyMu.Lock()
				denials := autoDenials
				hadUserDeny := userDenied
				autoDenials = nil
				userDenied = false
				autoDenyMu.Unlock()

				connClosed := false
				select {
				case <-conn.closed:
					connClosed = true
				default:
				}
				stepErr := false
				if agyDBPath != "" {
					_, stepErr = agyNewStepError(agyDBPath, agyStepStart)
				}
				if promptErr != nil ||
					ctx.Err() != nil ||
					promptResult.StopReason == "refusal" ||
					promptResult.StopReason == "cancelled" ||
					len(denials) == 0 ||
					hadUserDeny ||
					cont >= agyAutoDenyMaxContinuations ||
					connClosed ||
					stepErr {
					break
				}

				toolsMu.Lock()
				var settleIDs []string
				for id, pending := range pendingTools {
					if !pending.background {
						settleIDs = append(settleIDs, id)
					}
				}
				sort.Strings(settleIDs)
				if len(settleIDs) > 0 {
					settled := make(map[string]bool, len(settleIDs))
					for _, id := range settleIDs {
						settled[id] = true
						delete(pendingTools, id)
					}
					keptOrder := pendingOrder[:0]
					for _, id := range pendingOrder {
						if !settled[id] {
							keptOrder = append(keptOrder, id)
						}
					}
					pendingOrder = keptOrder
					for _, id := range settleIDs {
						_ = harness.Emit(emit, harness.Event{
							Type:    harness.EventToolEnd,
							ToolID:  id,
							IsError: true,
							Output:  "工具调用已被权限策略拦截，正在换用其他方式继续",
						})
					}
				}
				toolsMu.Unlock()

				currentItems = []any{
					map[string]any{
						"type": "text",
						"text": buildAgyAutoDenyContinuationPrompt(denials),
					},
				}
			}
			promptResult.Usage = totalUsage
		} else {
			params := map[string]any{
				"sessionId": sid,
				"prompt":    promptItems,
			}
			promptErr = conn.Call(ctx, "session/prompt", params, &promptResult)
		}
	}
	detach()

	agyCancelled := h.opts.Agent == AgentAgy && ctx.Err() != nil
	var agyErr error
	if h.opts.Agent == AgentAgy && promptErr == nil && !agyCancelled {
		if spontaneous {
			agyErr = agyTurnFailure(agyDBPath, agyStepStart, "")
			if agyErr == nil {
				promptResult.Usage = agyStepsUsage(agyDBPath, agyStepStart)
			}
		} else {
			agyErr = agyTurnFailure(agyDBPath, agyStepStart, promptResult.StopReason)
		}
	}
	if h.opts.HoldBackgroundTasks && promptErr == nil && agyErr == nil && ctx.Err() == nil && len(toolStartOrder) > 0 {
		toolID := lastPendingTool()
		if toolID == "" && spontaneous {
			toolID = toolStartOrder[len(toolStartOrder)-1].id
		}
		scanTranscript(agyRecordPath(p, sid, "logs/transcript.jsonl"), toolID)
	}

	toolsMu.Lock()
	toolsFinished = true
	ids := make([]string, 0, len(pendingTools))
	for id := range pendingTools {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	interrupted := promptErr != nil || ctx.Err() != nil
	for _, id := range ids {
		event := harness.Event{Type: harness.EventToolEnd, ToolID: id, IsError: interrupted}
		if interrupted {
			event.Output = "本轮中断，工具未完成"
		}
		_ = harness.Emit(emit, event)
	}
	toolsMu.Unlock()

	if h.opts.HoldBackgroundTasks && promptErr == nil && agyErr == nil {
		h.reconcileRoundTasks(p, turn.ThreadID, emit)
	}
	if promptErr == nil && agyErr == nil && (h.opts.Agent != AgentAgy || ctx.Err() == nil) {
		u := promptResult.Usage
		if u.InputTokens != 0 || u.OutputTokens != 0 || u.ThoughtTokens != 0 || u.CachedReadTokens != 0 {
			// agy 的 outputTokens 已含 thoughtTokens，不能重复相加；其他 Agent 仍按 ACP 规范草案累加。
			output := u.OutputTokens + u.ThoughtTokens
			if h.opts.Agent == AgentAgy {
				output = u.OutputTokens
			}
			_ = harness.Emit(emit, harness.Event{Type: harness.EventUsage, Usage: &harness.Usage{
				InputTokens: u.InputTokens, OutputTokens: output, CachedTokens: u.CachedReadTokens,
				ContextTokens: contextUsed.Load(), ContextWindow: contextSize.Load(),
				CostUSD: math.Float64frombits(sessionCostBits.Load()),
			}})
		}
		if h.opts.Agent != AgentAgy || ctx.Err() == nil {
			_ = harness.Emit(emit, harness.Event{
				Type:       harness.EventDone,
				StopReason: promptResult.StopReason,
			})
		}
	}

	cancelledPrompt := agyPrompt && ctx.Err() != nil
	if h.opts.HoldBackgroundTasks && promptErr == nil && agyErr == nil &&
		(cancelledPrompt || ctx.Err() == nil) && h.holdProcess(p, turn.ThreadID, cancelledPrompt) {
		p.transcriptPath, p.transcriptSize = agyTranscriptPosition(p, sid)
		if h.opts.Agent == AgentAgy {
			p.agyStepStart, p.agyStepStartOK = agyStepIndex(agySessionDB(agyGeminiDir(allArgs, cmd.Env), sid))
		}
		if h.opts.Agent == AgentAgy {
			return ctx.Err()
		}
		return nil
	}
	if cancelledPrompt {
		if promptErr != nil {
			h.stopAgyProcess(p)
			return ctx.Err()
		}
		select {
		case <-conn.closed:
			h.stopAgyProcess(p)
			return ctx.Err()
		default:
		}
	}
	if h.opts.HoldBackgroundTasks {
		h.residentMu.Lock()
		p.expectedExit = true
		h.residentMu.Unlock()
	}
	if promptErr == nil && ctx.Err() == nil && canClose && (!h.opts.HoldBackgroundTasks || agyErr == nil) {
		closeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_ = conn.Call(closeCtx, "session/close", map[string]any{"sessionId": sid}, nil)
		cancel()
	}
	if h.opts.HoldBackgroundTasks && (promptErr != nil || agyErr != nil) {
		_ = conn.Notify("session/cancel", map[string]any{"sessionId": sid})
		_ = killProcessGroup(cmd, syscall.SIGKILL)
	}
	_ = conn.Close()

	if ctx.Err() != nil {
		_ = killProcessGroup(cmd, syscall.SIGKILL)
	}

	waitErr := p.wait()

	if ctx.Err() != nil {
		return ctx.Err()
	}
	if promptErr != nil {
		tail := strings.TrimSpace(stderrBuf.String())
		if tail != "" {
			return fmt.Errorf("acp prompt failed: %w (stderr: %s)", promptErr, tail)
		}
		return fmt.Errorf("acp prompt failed: %w", promptErr)
	}
	if agyErr != nil {
		return agyErr
	}
	if waitErr != nil {
		tail := strings.TrimSpace(stderrBuf.String())
		if tail != "" {
			return fmt.Errorf("acp process exited with error: %w (stderr: %s)", waitErr, tail)
		}
		return fmt.Errorf("acp process exited with error: %w", waitErr)
	}

	return nil
}

type acpInitializeResult struct {
	AgentCapabilities struct {
		LoadSession         bool `json:"loadSession"`
		SessionCapabilities struct {
			Resume *struct{} `json:"resume"`
			Close  *struct{} `json:"close"`
		} `json:"sessionCapabilities"`
		// MCPCapabilities 声明 Agent 是否直接支持 HTTP MCP；不支持时退化为 mcp-remote。
		MCPCapabilities struct {
			HTTP bool `json:"http"`
		} `json:"mcpCapabilities"`
	} `json:"agentCapabilities"`
	AuthMethods []acpAuthMethod `json:"authMethods"`
}

// acpAuthMethod 是 Agent 广告的认证方法。
type acpAuthMethod struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// pickAuthMethod 返回可用的认证方法 ID：ACP 客户端不得传 terminal 方法，
// 因此跳过 terminal，取第一个可用的；没有可用方法时返回空。
func pickAuthMethod(methods []acpAuthMethod) string {
	for _, method := range methods {
		if method.ID == "" || method.Type == "terminal" {
			continue
		}
		return method.ID
	}
	return ""
}

// acpAuthRequired 判断会话建立失败是否属于未认证：错误文本带 auth，
// 或 JSON-RPC 错误码为 -32000（agy 未登录）。
func acpAuthRequired(err error) bool {
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) && rpcErr.Code == -32000 {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "auth")
}

// selectACPConfigOptions 从会话响应的 configOptions 里找出推理强度与快速模式配置项。
// 规则：
// effort 选项按 category/id（thought_level/effort/reasoning_effort）识别，取值需构成推理强度刻度；
// Fast 选项是 id 为 "fast"、category 为 model_config 的布尔项。
func selectACPConfigOptions(options []map[string]any) (effortConfigID, currentEffort, fastConfigID, currentFast string) {
	for _, option := range options {
		id, _ := option["id"].(string)
		if id == "" {
			continue
		}
		key, _ := option["category"].(string)
		if key == "" || id == "thought_level" {
			key = id
		}
		current, _ := option["currentValue"].(string)
		switch key {
		case "thought_level", "effort", "reasoning_effort":
			if !hasACPEffortScale(option) {
				continue
			}
			effortConfigID, currentEffort = id, current
		case "model_config":
			if id == "fast" {
				fastConfigID, currentFast = id, current
			}
		}
	}
	return
}

// hasACPEffortScale 判断配置项取值（含一层嵌套 options）里是否含推理强度刻度。
// 只有取值构成刻度才算 effort 选项，布尔 thinking 不算。
func hasACPEffortScale(option map[string]any) bool {
	scale := map[string]bool{
		"none": true, "low": true, "medium": true, "high": true,
		"xhigh": true, "max": true, "extra-high": true,
	}
	values, _ := option["options"].([]any)
	for _, v := range values {
		valueMap, ok := v.(map[string]any)
		if !ok {
			continue
		}
		entries := []any{valueMap}
		if nested, ok := valueMap["options"].([]any); ok {
			entries = nested
		}
		for _, entry := range entries {
			entryMap, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if s, _ := entryMap["value"].(string); scale[strings.ToLower(s)] {
				return true
			}
		}
	}
	return false
}

// approvalCancelled 判断审批是否因本轮取消而结束：
// ACP 规范要求取消时以 cancelled 应答所有挂起的 permission 请求。
func approvalCancelled(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func acpCancelledOutcome() map[string]any {
	return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
}

type acpApprovalResponse struct {
	payload any
	onAck   func(err error)
}

func (a acpApprovalResponse) ResponsePayload() any {
	return a.payload
}

func (a acpApprovalResponse) Ack(err error) {
	if a.onAck != nil {
		a.onAck(err)
	}
}

type elicitationProperty struct {
	Type        string                `json:"type"`
	Title       string                `json:"title"`
	Description string                `json:"description"`
	Enum        []string              `json:"enum"`
	Const       string                `json:"const"`
	OneOf       []elicitationProperty `json:"oneOf"`
	AnyOf       []elicitationProperty `json:"anyOf"`
	Items       *elicitationProperty  `json:"items"`
}

func handleElicitation(ctx context.Context, approver harness.Approver, params json.RawMessage) (any, error) {
	var p struct {
		Mode            string `json:"mode"`
		Message         string `json:"message"`
		RequestedSchema struct {
			Properties map[string]elicitationProperty `json:"properties"`
		} `json:"requestedSchema"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if p.Mode != "form" {
		return nil, fmt.Errorf("unsupported elicitation mode %q", p.Mode)
	}
	cancel := map[string]any{"action": "cancel"}
	interactor, ok := approver.(harness.Interactor)
	if !ok {
		return cancel, nil
	}
	req := harness.ApprovalRequest{Kind: harness.RequestKindQuestion, ToolName: "ask_question", ToolID: "acp-q-" + uuid.NewString()[:8], Summary: p.Message}
	ids := make([]string, 0, len(p.RequestedSchema.Properties))
	for id := range p.RequestedSchema.Properties {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	const otherPrefix = "Optional free-form answer to: "
	hasOptions := func(prop elicitationProperty) bool {
		switch prop.Type {
		case "string":
			return len(prop.Enum) > 0 || len(prop.OneOf) > 0 || len(prop.AnyOf) > 0
		case "array":
			return prop.Items != nil && (len(prop.Items.Enum) > 0 || len(prop.Items.OneOf) > 0 || len(prop.Items.AnyOf) > 0)
		default:
			return false
		}
	}
	propPrompt := func(prop elicitationProperty) string {
		if prop.Description != "" {
			return prop.Description
		}
		return prop.Title
	}
	isFreeformString := func(prop elicitationProperty) bool {
		return prop.Type == "string" && len(prop.Enum) == 0 && len(prop.OneOf) == 0 && len(prop.AnyOf) == 0
	}
	companions := make(map[string]string)
	isCompanion := make(map[string]bool)
	// 优先按 <id>_other 的 key 配对，避免描述文案变化导致配对失效。
	for _, id := range ids {
		if !strings.HasSuffix(id, "_other") {
			continue
		}
		if !isFreeformString(p.RequestedSchema.Properties[id]) {
			continue
		}
		baseID := strings.TrimSuffix(id, "_other")
		if baseProp, exists := p.RequestedSchema.Properties[baseID]; exists && baseID != "" && companions[baseID] == "" && hasOptions(baseProp) {
			companions[baseID] = id
			isCompanion[id] = true
		}
	}
	// 按 key 未配对时，回退到原有的 description 前缀与题干匹配。
	for _, id := range ids {
		if isCompanion[id] {
			continue
		}
		prop := p.RequestedSchema.Properties[id]
		if !isFreeformString(prop) {
			continue
		}
		if !strings.HasPrefix(prop.Description, otherPrefix) {
			continue
		}
		target := strings.TrimSpace(strings.TrimPrefix(prop.Description, otherPrefix))
		if target == "" {
			continue
		}
		for _, baseID := range ids {
			if baseID == id || companions[baseID] != "" {
				continue
			}
			baseProp := p.RequestedSchema.Properties[baseID]
			if hasOptions(baseProp) && propPrompt(baseProp) == target {
				companions[baseID] = id
				isCompanion[id] = true
				break
			}
		}
	}
	values := make(map[string]map[string]string)
	for _, id := range ids {
		if isCompanion[id] {
			continue
		}
		prop := p.RequestedSchema.Properties[id]
		prompt := prop.Description
		if prompt == "" {
			prompt = prop.Title
		}
		if prompt == "" {
			prompt = p.Message
		}
		q := harness.Question{ID: id, Header: prop.Title, Prompt: prompt}
		choices := prop
		switch prop.Type {
		case "array":
			q.AllowsMultiple = true
			if prop.Items != nil {
				choices = *prop.Items
			}
		case "boolean":
			choices.Enum = []string{"true", "false"}
		case "string":
		default:
			return nil, fmt.Errorf("unsupported elicitation property type %q", prop.Type)
		}
		values[id] = map[string]string{}
		add := func(value, label string) {
			if label == "" {
				label = value
			}
			q.Choices = append(q.Choices, harness.QuestionChoice{Label: label})
			values[id][label] = value
		}
		for _, value := range choices.Enum {
			add(value, value)
		}
		for _, choice := range append(choices.OneOf, choices.AnyOf...) {
			value := choice.Const
			if len(choice.Enum) > 0 {
				value = choice.Enum[0]
			}
			add(value, choice.Title)
		}
		_, hasCompanion := companions[id]
		q.AllowsOther = (prop.Type == "string" && len(q.Choices) == 0) || hasCompanion
		req.Questions = append(req.Questions, q)
	}
	response, err := interactor.Interact(ctx, req)
	if err != nil {
		harness.AckDelivery(approver, req, err)
		return cancel, nil
	}
	payload := cancel
	if response.Skipped {
		payload = map[string]any{"action": "decline"}
	} else if len(response.Answers) > 0 {
		content := map[string]any{}
		for _, q := range req.Questions {
			answers, ok := response.Answers[q.ID]
			if !ok {
				continue
			}
			if companionID, hasCompanion := companions[q.ID]; hasCompanion {
				var optionAnswers, otherAnswers []string
				for _, answer := range answers {
					if value, ok := values[q.ID][answer]; ok {
						optionAnswers = append(optionAnswers, value)
					} else {
						otherAnswers = append(otherAnswers, answer)
					}
				}
				if q.AllowsMultiple {
					if len(optionAnswers) > 0 {
						content[q.ID] = optionAnswers
					}
				} else if len(optionAnswers) > 0 {
					content[q.ID] = optionAnswers[0]
				}
				if len(otherAnswers) > 0 {
					content[companionID] = strings.Join(otherAnswers, "\n")
				}
				continue
			}
			mapped := make([]string, 0, len(answers))
			for _, answer := range answers {
				if value, ok := values[q.ID][answer]; ok {
					answer = value
				}
				mapped = append(mapped, answer)
			}
			if q.AllowsMultiple {
				content[q.ID] = mapped
			} else if len(mapped) > 0 {
				if p.RequestedSchema.Properties[q.ID].Type == "boolean" {
					value, err := strconv.ParseBool(mapped[0])
					if err != nil {
						return acpApprovalResponse{payload: cancel, onAck: func(err error) { harness.AckDelivery(approver, req, err) }}, nil
					}
					content[q.ID] = value
				} else {
					content[q.ID] = mapped[0]
				}
			}
		}
		if len(content) > 0 {
			payload = map[string]any{"action": "accept", "content": content}
		}
	}
	return acpApprovalResponse{payload: payload, onAck: func(err error) { harness.AckDelivery(approver, req, err) }}, nil
}

func configureAgy(ctx context.Context, conn *Conn, sid string, turn harness.Turn, modes *acpSessionModes, options *[]map[string]any) error {
	modeIDs := modes.IDs()
	wanted := "default"
	if turn.InteractionMode == "plan" && slices.Contains(modeIDs, "plan") {
		wanted = "plan"
	}
	if wanted != modes.CurrentModeID && slices.Contains(modeIDs, wanted) {
		if err := conn.Call(ctx, "session/set_mode", map[string]any{"sessionId": sid, "modeId": wanted}, nil); err != nil {
			return fmt.Errorf("acp set mode: %w", err)
		}
		modes.CurrentModeID = wanted
	}
	apply := func(id, value, name string) error {
		var result struct {
			ConfigOptions []map[string]any `json:"configOptions"`
		}
		if err := conn.Call(ctx, "session/set_config_option", map[string]any{"sessionId": sid, "configId": id, "value": value}, &result); err != nil {
			return fmt.Errorf("acp set %s: %w", name, err)
		}
		for _, option := range result.ConfigOptions {
			if option["id"] == id && option["currentValue"] == value {
				*options = result.ConfigOptions
				return nil
			}
		}
		return fmt.Errorf("agy did not apply %s %s", name, value)
	}
	if turn.Model != "" && turn.Model != "default" {
		modelID, current := "", ""
		for _, option := range *options {
			if option["id"] == "model" || option["category"] == "model" {
				modelID, _ = option["id"].(string)
				current, _ = option["currentValue"].(string)
				break
			}
		}
		if modelID == "" {
			return fmt.Errorf("agy did not apply model %s", turn.Model)
		}
		if current != turn.Model {
			if err := apply(modelID, turn.Model, "model"); err != nil {
				return err
			}
		}
	}
	if turn.Effort != "" {
		id, current, _, _ := selectACPConfigOptions(*options)
		if id == "" {
			return fmt.Errorf("agy did not apply effort %s", turn.Effort)
		}
		if current != turn.Effort {
			return apply(id, turn.Effort, "effort")
		}
	}
	return nil
}
