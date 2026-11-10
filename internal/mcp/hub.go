package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mcode/internal/agentconf"
	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

// ErrUpstreamUnauthorized 表示上游 MCP 服务返回 401 未授权，供上层提示重新授权。
var ErrUpstreamUnauthorized = errors.New("upstream unauthorized")

// errSessionExpired 表示上游已不认识当前会话号（典型场景是上游重启），需要丢弃会话状态后重新初始化。
var errSessionExpired = errors.New("upstream session expired")

const (
	// defaultRPCTimeout 为单次上游 RPC 的默认超时。
	defaultRPCTimeout = 60 * time.Second
	// toolCallTimeout 为 tools/call 的默认超时。浏览器自动化、批量数据处理等工具
	// 单次执行可能远超通用 RPC 时限，用 60 秒会误判为失败。
	toolCallTimeout = 300 * time.Second
)

// capabilityReporter 为可选接口：能报告 initialize 结果中 capabilities 的客户端实现它。
// 不放进 upstreamClient，避免 sse 等暂未支持的客户端被迫改动。
type capabilityReporter interface {
	capabilities() json.RawMessage
}

// 上游空闲回收参数。声明为变量而非常量，便于测试缩短阈值直接验证回收行为。
var (
	// upstreamIdleTimeout 为 stdio 上游可闲置多久后被回收。
	// 不回收会一直占着子进程与内存，长会话里上游数量只增不减。
	upstreamIdleTimeout = 15 * time.Minute
	// idleReapInterval 为后台回收 goroutine 的检查间隔。
	idleReapInterval = time.Minute
)

// defaultTokensMu 保护 defaultTokens。
var (
	defaultTokensMu sync.RWMutex
	defaultTokens   TokenSource
)

// SetDefaultTokenSource 设置包级默认 TokenSource。
// 设置页等不持有 Hub 的调用方靠它复用与运行期一致的授权凭据。
func SetDefaultTokenSource(tokens TokenSource) {
	defaultTokensMu.Lock()
	defaultTokens = tokens
	defaultTokensMu.Unlock()
}

// DefaultTokenSource 返回包级默认 TokenSource，未设置时为 nil。
func DefaultTokenSource() TokenSource {
	defaultTokensMu.RLock()
	defer defaultTokensMu.RUnlock()
	return defaultTokens
}

// rpcCtx 在调用方未设置截止时间时施加默认 RPC 超时；调用方已给出截止时间
// （如 Probe 的整体预算）时沿用调用方的截止时间，避免默认值提前截断。
func rpcCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, defaultRPCTimeout)
}

// withMethodTimeout 按 JSON-RPC 方法放宽超时。代理转发用的是 HTTP 请求上下文
// （r.Context()），本身没有截止时间，不在这里放宽的话 tools/call 会落回通用 RPC
// 超时，把浏览器自动化这类长耗时工具误判为失败。
func withMethodTimeout(ctx context.Context, method string) (context.Context, context.CancelFunc) {
	if method == "tools/call" {
		if _, ok := ctx.Deadline(); !ok {
			return context.WithTimeout(ctx, toolCallTimeout)
		}
	}
	return context.WithCancel(ctx)
}

// TokenSource 提供用于访问上游服务的访问令牌。
type TokenSource interface {
	AccessToken(ctx context.Context, serverID string) (string, error)
}

// UpstreamSpec 描述一个上游 MCP 服务器的配置。
// Transport 取 "stdio"、"http"、"sse"。
type UpstreamSpec struct {
	ID string
	// Name 为上游服务名（来自 agentconf 配置），代理层据此匹配工具策略（禁用/免审批）。
	Name      string
	Transport string
	Command   string
	Args      []string
	Env       map[string]string
	Dir       string
	URL       string
	Headers   map[string]string
	OAuth     bool
	OAuthID   string
}

// SpecFromAgentconf 从扫描到的 agentconf.MCPServer 构造 UpstreamSpec。
// 运行时扫描需开启 WithCredentials，避免丢失配置文件内的环境变量和请求头。
func SpecFromAgentconf(id string, s agentconf.MCPServer) UpstreamSpec {
	if id == "" {
		id = s.Name
	}
	env := make(map[string]string, len(s.EnvKeys))
	for _, key := range s.EnvKeys {
		if val, ok := os.LookupEnv(key); ok {
			env[key] = val
		}
	}
	for key, value := range s.Env {
		env[key] = value
	}
	return UpstreamSpec{
		ID:        id,
		Name:      s.Name,
		Transport: s.Transport,
		Command:   s.Command,
		Args:      s.Args,
		Env:       env,
		URL:       s.URL,
		Headers:   s.Headers,
		OAuth:     s.URL != "",
		OAuthID:   s.AuthID(),
	}
}

// Tool 表示 MCP 服务器导出的工具定义。
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// upstreamClient 为上游客户端内部接口。
type upstreamClient interface {
	initialize(ctx context.Context) error
	serverInfo() (name, version string)
	listTools(ctx context.Context) ([]Tool, error)
	callTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
	forward(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *jsonRPCError, error)
	close() error
}

// Hub 管理外部上游 MCP 服务的生命周期与路由分发。
type Hub struct {
	tokens    TokenSource
	mu        sync.RWMutex
	upstreams map[string]UpstreamSpec
	clients   map[string]upstreamClient
	// lastUsed 记录每个上游最近一次被使用的时间，供空闲回收判定。
	lastUsed map[string]time.Time
	// inflight 记录每个上游正在进行的请求数，非零时不允许回收。
	inflight map[string]int
	// done 用于通知后台回收 goroutine 退出。
	done chan struct{}
	// closeOnce 保证 done 只关闭一次，Close 保持幂等。
	closeOnce sync.Once
	closed    bool
}

// NewHub 创建一个 Hub 实例，tokens 可以为 nil。
func NewHub(tokens TokenSource) *Hub {
	if tokens != nil {
		SetDefaultTokenSource(tokens)
	}
	h := &Hub{
		tokens:    tokens,
		upstreams: make(map[string]UpstreamSpec),
		clients:   make(map[string]upstreamClient),
		lastUsed:  make(map[string]time.Time),
		inflight:  make(map[string]int),
		done:      make(chan struct{}),
	}
	go h.reapLoop()
	return h
}

// reapLoop 周期性回收空闲的 stdio 上游。
func (h *Hub) reapLoop() {
	ticker := time.NewTicker(idleReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-h.done:
			return
		case now := <-ticker.C:
			h.reapIdle(now)
		}
	}
}

// release 结束一次请求计数；lastUsed 从请求结束时刻起算，长调用不会刚结束就被判定空闲。
func (h *Hub) release(id string) {
	h.mu.Lock()
	if h.inflight == nil {
		h.inflight = make(map[string]int)
	}
	if h.lastUsed == nil {
		h.lastUsed = make(map[string]time.Time)
	}
	if h.inflight[id] > 0 {
		h.inflight[id]--
		// 只在仍有计数时刷新时间：若条目已被 reapIdle 回收，写回会留下无对应客户端的孤立记录。
		h.lastUsed[id] = time.Now()
	}
	if h.inflight[id] == 0 {
		delete(h.inflight, id)
	}
	h.mu.Unlock()
}

// reapIdle 回收 inflight 为 0 且空闲超时的 stdio 上游，之后的请求会懒创建新客户端。
// 只处理 stdio：http/sse 客户端不持有子进程，回收收益低而重建成本高。
func (h *Hub) reapIdle(now time.Time) {
	var idle []upstreamClient
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	for id, client := range h.clients {
		if _, ok := client.(*stdioClient); !ok {
			continue
		}
		if h.inflight[id] != 0 {
			continue
		}
		last, ok := h.lastUsed[id]
		if !ok {
			// 无使用时间记录时无法判定空闲时长，按保守策略保留。
			continue
		}
		if now.Sub(last) <= upstreamIdleTimeout {
			continue
		}
		idle = append(idle, client)
		delete(h.clients, id)
		delete(h.lastUsed, id)
		delete(h.inflight, id)
	}
	h.mu.Unlock()

	for _, client := range idle {
		_ = client.close()
	}
}

// UpstreamCapabilities 返回上游 initialize 结果中的 capabilities 原始 JSON。
// 客户端不支持上报能力（如 sse）时返回 nil, nil，由调用方按无能力处理。
func (h *Hub) UpstreamCapabilities(ctx context.Context, id string) (json.RawMessage, error) {
	client, release, err := h.acquireClient(id)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := client.initialize(ctx); err != nil {
		return nil, err
	}
	reporter, ok := client.(capabilityReporter)
	if !ok {
		return nil, nil
	}
	return reporter.capabilities(), nil
}

// SetUpstreams 替换全部上游配置。被移除或配置变更的已有客户端将被关闭。
func (h *Hub) SetUpstreams(specs []UpstreamSpec) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}

	newSpecs := make(map[string]UpstreamSpec, len(specs))
	for _, s := range specs {
		newSpecs[s.ID] = s
	}

	var toClose []upstreamClient
	for id, client := range h.clients {
		newSpec, exists := newSpecs[id]
		oldSpec := h.upstreams[id]
		if !exists || !specsEqual(oldSpec, newSpec) {
			toClose = append(toClose, client)
			delete(h.clients, id)
			// 客户端已销毁，使用统计一并清理，避免这些条目长期残留。
			delete(h.lastUsed, id)
			delete(h.inflight, id)
		}
	}
	h.upstreams = newSpecs
	h.mu.Unlock()

	for _, client := range toClose {
		_ = client.close()
	}
}

// Upstream 查询指定 ID 的上游配置。
func (h *Hub) Upstream(id string) (UpstreamSpec, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	spec, ok := h.upstreams[id]
	return spec, ok
}

// acquireClient 获取或创建客户端，并在同一锁内标记请求进行中。
func (h *Hub) acquireClient(id string) (upstreamClient, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, nil, errors.New("hub is closed")
	}
	client, ok := h.clients[id]
	if !ok {
		spec, ok := h.upstreams[id]
		if !ok {
			return nil, nil, fmt.Errorf("upstream %q not found", id)
		}
		var err error
		client, err = createClient(spec, h.tokens)
		if err != nil {
			return nil, nil, err
		}
		h.clients[id] = client
	}
	if h.inflight == nil {
		h.inflight = make(map[string]int)
	}
	if h.lastUsed == nil {
		h.lastUsed = make(map[string]time.Time)
	}
	h.inflight[id]++
	h.lastUsed[id] = time.Now()
	return client, func() { h.release(id) }, nil
}

// ListTools 获取指定上游服务的可用工具列表。
func (h *Hub) ListTools(ctx context.Context, id string) ([]Tool, error) {
	client, release, err := h.acquireClient(id)
	if err != nil {
		return nil, err
	}
	defer release()
	return client.listTools(ctx)
}

// CallTool 调用指定上游服务的具体工具，原样返回上游的 result。
func (h *Hub) CallTool(ctx context.Context, id, name string, args json.RawMessage) (json.RawMessage, error) {
	client, release, err := h.acquireClient(id)
	if err != nil {
		return nil, err
	}
	defer release()
	return client.callTool(ctx, name, args)
}

// Forward 将 JSON-RPC 请求直接转发给指定上游服务。
func (h *Hub) Forward(ctx context.Context, id, method string, params json.RawMessage) (json.RawMessage, *jsonRPCError, error) {
	client, release, err := h.acquireClient(id)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	ctx, cancel := withMethodTimeout(ctx, method)
	defer cancel()
	return client.forward(ctx, method, params)
}

// Close 关闭 Hub 并释放所有上游客户端资源。
func (h *Hub) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	clients := make([]upstreamClient, 0, len(h.clients))
	for _, c := range h.clients {
		clients = append(clients, c)
	}
	h.clients = make(map[string]upstreamClient)
	h.lastUsed = make(map[string]time.Time)
	h.inflight = make(map[string]int)
	h.mu.Unlock()

	// 通知后台回收 goroutine 退出；closeOnce 保证重复 Close 不会重复关闭通道。
	h.closeOnce.Do(func() {
		if h.done != nil {
			close(h.done)
		}
	})

	var firstErr error
	for _, c := range clients {
		if err := c.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// specsEqual 比较两个 UpstreamSpec 是否完全一致。
func specsEqual(a, b UpstreamSpec) bool {
	if a.ID != b.ID || a.Name != b.Name || a.Transport != b.Transport || a.Command != b.Command ||
		a.Dir != b.Dir || a.URL != b.URL || a.OAuth != b.OAuth || a.OAuthID != b.OAuthID {
		return false
	}
	if len(a.Args) != len(b.Args) {
		return false
	}
	for i := range a.Args {
		if a.Args[i] != b.Args[i] {
			return false
		}
	}
	if len(a.Env) != len(b.Env) {
		return false
	}
	for k, v := range a.Env {
		if b.Env[k] != v {
			return false
		}
	}
	if len(a.Headers) != len(b.Headers) {
		return false
	}
	for k, v := range a.Headers {
		if b.Headers[k] != v {
			return false
		}
	}
	return true
}

func createClient(spec UpstreamSpec, tokens TokenSource) (upstreamClient, error) {
	switch strings.ToLower(spec.Transport) {
	case "stdio":
		return newStdioClient(spec), nil
	case "http":
		return newHTTPClient(spec, tokens), nil
	case "sse":
		return newSSEClient(spec, tokens), nil
	default:
		return nil, fmt.Errorf("unsupported transport: %q", spec.Transport)
	}
}

// rpcResponseMsg 用于内部 goroutine 之间传递 JSON-RPC 响应。
type rpcResponseMsg struct {
	result json.RawMessage
	rpcErr *jsonRPCError
	err    error
}

// rawIncomingRPC 用于初次反序列化收到的 JSON-RPC 消息。
type rawIncomingRPC struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"`
	// Method/Params 用于识别上游主动发来的请求与通知，它们绝不能当成响应。
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *jsonRPCError   `json:"error"`
}

func parseRPCID(raw *json.RawMessage) (int64, bool) {
	if raw == nil {
		return 0, false
	}
	var num int64
	if err := json.Unmarshal(*raw, &num); err == nil {
		return num, true
	}
	var str string
	if err := json.Unmarshal(*raw, &str); err == nil {
		if n, err := strconv.ParseInt(str, 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

// tailBuffer 维护最多 limit 字节的最近输出，并发安全。
type tailBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func newTailBuffer(limit int) *tailBuffer {
	return &tailBuffer{limit: limit}
}

func (b *tailBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = b.data[len(b.data)-b.limit:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

func firstNonEmptyLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		if t != "" {
			return t
		}
	}
	return ""
}

// ==========================================
// stdioClient 实现
// ==========================================

type stdioClient struct {
	spec      UpstreamSpec
	startMu   sync.Mutex
	mu        sync.Mutex
	writeMu   sync.Mutex
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stderrBuf *tailBuffer
	nextID    int64
	pending   map[int64]chan rpcResponseMsg
	started   bool
	closed    bool
	sName     string
	sVer      string
	// sCaps 保存 initialize 结果中的 capabilities 原始 JSON，供代理层透传能力。
	sCaps json.RawMessage
}

func newStdioClient(spec UpstreamSpec) *stdioClient {
	return &stdioClient{
		spec:      spec,
		pending:   make(map[int64]chan rpcResponseMsg),
		stderrBuf: newTailBuffer(4096),
	}
}

func (c *stdioClient) serverInfo() (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sName, c.sVer
}

// capabilities 返回上游 initialize 结果里的 capabilities 原始 JSON。
func (c *stdioClient) capabilities() json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sCaps
}

func (c *stdioClient) initialize(ctx context.Context) error {
	return c.ensureInitialized(ctx)
}

func (c *stdioClient) ensureInitialized(ctx context.Context) error {
	c.startMu.Lock()
	defer c.startMu.Unlock()

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("client closed")
	}
	alive := c.started && c.cmd != nil && c.cmd.Process != nil
	c.mu.Unlock()

	if alive {
		return nil
	}

	if err := c.startProcessLocked(); err != nil {
		return err
	}

	if err := c.doInitialize(ctx); err != nil {
		_ = c.closeProcessLocked()
		return err
	}
	return nil
}

// envValue 取出 env 里最后一个 key 对应的值；Windows 下 key 不区分大小写。
func envValue(env []string, key string) string {
	value := ""
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		if kv[:i] == key || (runtime.GOOS == "windows" && strings.EqualFold(kv[:i], key)) {
			value = kv[i+1:]
		}
	}
	return value
}

// resolveCommandInEnv 按 env 里的 PATH 把命令解析成绝对路径。
// exec.Command 内部用当前进程的 PATH 做 LookPath，不受之后设置的 cmd.Env 影响；
// 而 stdio 上游要改用登录 Shell 的 PATH，所以必须先自己解析出完整路径。
// 解析不到时返回原 command，让 exec 给出原有错误。
func resolveCommandInEnv(command string, env []string) string {
	if command == "" {
		return command
	}
	// 含路径分隔符的命令不做查找，原样交给 exec。
	if strings.ContainsRune(command, '/') || (runtime.GOOS == "windows" && strings.ContainsRune(command, '\\')) {
		return command
	}
	pathValue := envValue(env, "PATH")
	if pathValue == "" {
		return command
	}
	// Windows 除了原样尝试，还要补 PATHEXT 里的扩展名。
	exts := []string{""}
	if runtime.GOOS == "windows" {
		for _, ext := range strings.Split(envValue(env, "PATHEXT"), ";") {
			if ext = strings.TrimSpace(ext); ext != "" {
				exts = append(exts, ext)
			}
		}
	}
	for _, dir := range filepath.SplitList(pathValue) {
		// 空目录项会退化成当前目录，不参与查找。
		if dir == "" {
			continue
		}
		for _, ext := range exts {
			candidate := filepath.Join(dir, command+ext)
			info, err := os.Stat(candidate)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			// Windows 的权限位不表达可执行性，只在类 Unix 上校验。
			if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
				continue
			}
			return candidate
		}
	}
	return command
}

func (c *stdioClient) startProcessLocked() error {
	// 优先使用登录 Shell 环境：GUI 启动时进程自带的 PATH 往往不含 npx、uvx 等工具。
	env := loginenv.Environ()
	if len(env) == 0 {
		env = os.Environ()
	}
	for k, v := range c.spec.Env {
		env = append(env, k+"="+v)
	}
	// 先按上面的 env 解析出命令路径，否则 exec.Command 会用进程自身的 PATH 查找而失败。
	cmd := procattr.HideWindow(exec.Command(resolveCommandInEnv(c.spec.Command, env), c.spec.Args...))
	if c.spec.Dir != "" {
		cmd.Dir = c.spec.Dir
	}
	setProcessGroup(cmd)
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	stderrBuf := newTailBuffer(4096)
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return err
	}

	c.mu.Lock()
	c.cmd = cmd
	c.stdin = stdin
	c.stderrBuf = stderrBuf
	c.started = true
	c.mu.Unlock()

	go c.readLoop(stdout, cmd)
	return nil
}

func (c *stdioClient) closeProcessLocked() error {
	c.mu.Lock()
	cmd := c.cmd
	stdin := c.stdin
	c.started = false
	c.cmd = nil
	c.stdin = nil
	c.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	_ = killProcessGroup(cmd, os.Kill)
	return nil
}

func (c *stdioClient) doInitialize(ctx context.Context) error {
	initParams := map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "mcode",
			"version": "1.0",
		},
	}
	rawParams, err := json.Marshal(initParams)
	if err != nil {
		return err
	}

	res, rpcErr, err := c.sendRPC(ctx, "initialize", rawParams)
	if err != nil {
		return err
	}
	if rpcErr != nil {
		return fmt.Errorf("initialize failed: code %d, message %s", rpcErr.Code, rpcErr.Message)
	}

	var initRes struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Capabilities json.RawMessage `json:"capabilities"`
	}
	_ = json.Unmarshal(res, &initRes)
	c.mu.Lock()
	c.sName = initRes.ServerInfo.Name
	c.sVer = initRes.ServerInfo.Version
	c.sCaps = initRes.Capabilities
	c.mu.Unlock()

	// 发送 notifications/initialized 通知
	return c.sendNotification("notifications/initialized", nil)
}

func (c *stdioClient) sendNotification(method string, params json.RawMessage) error {
	c.mu.Lock()
	if c.closed || !c.started || c.stdin == nil {
		c.mu.Unlock()
		return errors.New("client not running")
	}
	stdin := c.stdin
	c.mu.Unlock()

	reqMap := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if len(params) > 0 {
		reqMap["params"] = params
	}
	data, err := json.Marshal(reqMap)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = stdin.Write(data)
	return err
}

func (c *stdioClient) sendRPC(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *jsonRPCError, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, nil, errors.New("client closed")
	}
	if !c.started || c.stdin == nil {
		c.mu.Unlock()
		return nil, nil, errors.New("client not started")
	}
	id := atomic.AddInt64(&c.nextID, 1)
	ch := make(chan rpcResponseMsg, 1)
	c.pending[id] = ch
	stdin := c.stdin
	c.mu.Unlock()

	reqMap := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if len(params) > 0 {
		reqMap["params"] = params
	}
	data, err := json.Marshal(reqMap)
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, nil, err
	}
	data = append(data, '\n')

	c.writeMu.Lock()
	_, writeErr := stdin.Write(data)
	c.writeMu.Unlock()
	if writeErr != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, nil, writeErr
	}

	reqCtx, cancel := rpcCtx(ctx)
	defer cancel()

	select {
	case <-reqCtx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, nil, reqCtx.Err()
	case resp := <-ch:
		if resp.err != nil {
			return nil, nil, resp.err
		}
		return resp.result, resp.rpcErr, nil
	}
}

func (c *stdioClient) readLoop(stdout io.ReadCloser, cmd *exec.Cmd) {
	scanner := bufio.NewScanner(stdout)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 16*1024*1024)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var raw rawIncomingRPC
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}
		// 带 method 的是上游主动发来的消息，不能拿去匹配 pending，否则会把自己的
		// 请求 id 顶掉（服务端请求 id 与客户端 id 撞车时尤其致命）。
		if raw.Method != "" {
			c.replyToServer(raw)
			continue
		}
		id, ok := parseRPCID(raw.ID)
		if !ok {
			continue
		}
		c.mu.Lock()
		ch, found := c.pending[id]
		if found {
			delete(c.pending, id)
		}
		c.mu.Unlock()
		if found {
			ch <- rpcResponseMsg{
				result: raw.Result,
				rpcErr: raw.Error,
			}
		}
	}

	// 读 stdout 失败（典型是单行超过 16MB 上限）时子进程往往还活着，必须先终结
	// 进程组再 Wait，否则这里会永久阻塞，子进程也没人回收。
	readErr := scanner.Err()
	if readErr != nil {
		_ = killProcessGroup(cmd, os.Kill)
		readErr = fmt.Errorf("stdout read error: %v", readErr)
	}

	_ = cmd.Wait()

	c.mu.Lock()
	c.started = false
	c.cmd = nil
	c.stdin = nil
	pending := c.pending
	c.pending = make(map[int64]chan rpcResponseMsg)
	tail := c.stderrBuf.String()
	c.mu.Unlock()

	firstLine := firstNonEmptyLine(tail)
	var exitErr error
	switch {
	case readErr != nil:
		exitErr = readErr
	case firstLine != "":
		exitErr = fmt.Errorf("process exited: %s", firstLine)
	default:
		exitErr = errors.New("process exited")
	}

	for _, ch := range pending {
		ch <- rpcResponseMsg{err: exitErr}
	}
}

// replyToServer 回应上游主动发来的请求：ping 回空 result，其余方法回 -32601。
// 没有 id 的通知无需回应；id 原样回显，上游可能用字符串 id。
func (c *stdioClient) replyToServer(raw rawIncomingRPC) {
	if raw.ID == nil {
		return
	}
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      *raw.ID,
	}
	if raw.Method == "ping" {
		resp["result"] = map[string]any{}
	} else {
		resp["error"] = &jsonRPCError{Code: -32601, Message: "method not found"}
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	data = append(data, '\n')

	c.mu.Lock()
	stdin := c.stdin
	c.mu.Unlock()
	if stdin == nil {
		return
	}

	c.writeMu.Lock()
	_, _ = stdin.Write(data)
	c.writeMu.Unlock()
}

func (c *stdioClient) forward(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *jsonRPCError, error) {
	if err := c.ensureInitialized(ctx); err != nil {
		return nil, nil, err
	}
	return c.sendRPC(ctx, method, params)
}

func (c *stdioClient) listTools(ctx context.Context) ([]Tool, error) {
	return genericListTools(ctx, c)
}

func (c *stdioClient) callTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	return genericCallTool(ctx, c, name, args)
}

func (c *stdioClient) close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	return c.closeProcessLocked()
}

// ==========================================
// httpClient 实现
// ==========================================

type httpClient struct {
	spec      UpstreamSpec
	tokens    TokenSource
	client    *http.Client
	sessionID string
	mu        sync.RWMutex
	startMu   sync.Mutex
	initDone  bool
	nextID    int64
	sName     string
	sVer      string
	// sCaps 保存 initialize 结果中的 capabilities 原始 JSON，供代理层透传能力。
	sCaps json.RawMessage
}

func newHTTPClient(spec UpstreamSpec, tokens TokenSource) *httpClient {
	return &httpClient{
		spec:   spec,
		tokens: tokens,
		client: &http.Client{},
	}
}

func (c *httpClient) serverInfo() (string, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sName, c.sVer
}

// capabilities 返回上游 initialize 结果里的 capabilities 原始 JSON。
func (c *httpClient) capabilities() json.RawMessage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sCaps
}

func (c *httpClient) initialize(ctx context.Context) error {
	return c.ensureInitialized(ctx)
}

func (c *httpClient) ensureInitialized(ctx context.Context) error {
	c.startMu.Lock()
	defer c.startMu.Unlock()

	if c.initDone {
		return nil
	}

	if err := c.doInitialize(ctx); err != nil {
		return err
	}
	c.initDone = true
	return nil
}

func (c *httpClient) doInitialize(ctx context.Context) error {
	initParams := map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "mcode",
			"version": "1.0",
		},
	}
	rawParams, err := json.Marshal(initParams)
	if err != nil {
		return err
	}

	id := atomic.AddInt64(&c.nextID, 1)
	res, rpcErr, err := c.postRPC(ctx, &id, "initialize", rawParams)
	if err != nil {
		return err
	}
	if rpcErr != nil {
		return fmt.Errorf("initialize failed: code %d, message %s", rpcErr.Code, rpcErr.Message)
	}

	var initRes struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Capabilities json.RawMessage `json:"capabilities"`
	}
	_ = json.Unmarshal(res, &initRes)
	c.mu.Lock()
	c.sName = initRes.ServerInfo.Name
	c.sVer = initRes.ServerInfo.Version
	c.sCaps = initRes.Capabilities
	c.mu.Unlock()

	// 发送 notifications/initialized 通知
	_, _, err = c.postRPC(ctx, nil, "notifications/initialized", nil)
	return err
}

func (c *httpClient) postRPC(ctx context.Context, id *int64, method string, params json.RawMessage) (json.RawMessage, *jsonRPCError, error) {
	reqCtx, cancel := rpcCtx(ctx)
	defer cancel()

	reqMap := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if id != nil {
		reqMap["id"] = *id
	}
	if len(params) > 0 {
		reqMap["params"] = params
	}
	bodyBytes, err := json.Marshal(reqMap)
	if err != nil {
		return nil, nil, err
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.spec.URL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	httpReq.Header.Set("MCP-Protocol-Version", "2025-06-18")

	for k, v := range c.spec.Headers {
		httpReq.Header.Set(k, v)
	}

	if c.spec.OAuth && c.tokens != nil {
		tokenID := c.spec.OAuthID
		if tokenID == "" {
			tokenID = c.spec.ID
		}
		tok, err := c.tokens.AccessToken(reqCtx, tokenID)
		if err != nil && !errors.Is(err, ErrOAuthNotSignedIn) {
			return nil, nil, fmt.Errorf("failed to get access token: %w", err)
		}
		if err == nil && tok != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tok)
		}
	}

	c.mu.RLock()
	sid := c.sessionID
	c.mu.RUnlock()
	if sid != "" {
		httpReq.Header.Set("Mcp-Session-Id", sid)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if newSid := resp.Header.Get("Mcp-Session-Id"); newSid != "" {
		c.mu.Lock()
		c.sessionID = newSid
		c.mu.Unlock()
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, nil, ErrUpstreamUnauthorized
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		err := fmt.Errorf("upstream returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
		// 带着会话号却被拒（404 或 400）说明上游已丢掉这个会话，标记出来让调用方重新初始化。
		if sid != "" && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest) {
			err = fmt.Errorf("%w: %v", errSessionExpired, err)
		}
		return nil, nil, err
	}

	// 通知请求：202 或 200 或空 body 视为通知已接受
	if id == nil {
		return nil, nil, nil
	}

	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/event-stream") {
		watchDone := make(chan struct{})
		defer close(watchDone)
		go func() {
			select {
			case <-ctx.Done():
				_ = resp.Body.Close()
			case <-watchDone:
			}
		}()

		scanner := bufio.NewScanner(resp.Body)
		buf := make([]byte, 64*1024)
		scanner.Buffer(buf, 16*1024*1024)

		targetID := *id
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" {
				continue
			}
			var raw rawIncomingRPC
			if err := json.Unmarshal([]byte(payload), &raw); err != nil {
				continue
			}
			// 只接受响应；带 method 的是服务端请求，不能当成本次调用的结果。
			if raw.Method != "" {
				continue
			}
			if msgID, ok := parseRPCID(raw.ID); ok && msgID == targetID {
				return raw.Result, raw.Error, nil
			}
		}
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if err := scanner.Err(); err != nil {
			return nil, nil, err
		}
		return nil, nil, errors.New("no matching response found in event stream")
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if len(bytes.TrimSpace(respBytes)) == 0 {
		return nil, nil, nil
	}

	var raw rawIncomingRPC
	if err := json.Unmarshal(respBytes, &raw); err != nil {
		return nil, nil, err
	}
	return raw.Result, raw.Error, nil
}

// dropSession 丢弃当前上游会话，使下一次调用重新 initialize。
// 上游重启后旧会话号会一直 404，不丢弃就只能重启本应用才能恢复。
func (c *httpClient) dropSession() {
	c.startMu.Lock()
	c.initDone = false
	c.startMu.Unlock()

	c.mu.Lock()
	c.sessionID = ""
	c.sName = ""
	c.sVer = ""
	c.sCaps = nil
	c.mu.Unlock()
}

func (c *httpClient) forward(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *jsonRPCError, error) {
	if err := c.ensureInitialized(ctx); err != nil {
		return nil, nil, err
	}
	id := atomic.AddInt64(&c.nextID, 1)
	res, rpcErr, err := c.postRPC(ctx, &id, method, params)
	if err == nil || !errors.Is(err, errSessionExpired) {
		return res, rpcErr, err
	}

	// 会话已过期：重新初始化后重试一次，上游重启不应让本进程内该上游永久不可用。
	c.dropSession()
	if initErr := c.ensureInitialized(ctx); initErr != nil {
		return nil, nil, initErr
	}
	id = atomic.AddInt64(&c.nextID, 1)
	return c.postRPC(ctx, &id, method, params)
}

func (c *httpClient) listTools(ctx context.Context) ([]Tool, error) {
	return genericListTools(ctx, c)
}

func (c *httpClient) callTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	return genericCallTool(ctx, c, name, args)
}

func (c *httpClient) close() error {
	c.client.CloseIdleConnections()
	return nil
}

// ==========================================
// 通用客户端帮助函数
// ==========================================

type forwarder interface {
	forward(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *jsonRPCError, error)
}

func genericListTools(ctx context.Context, f forwarder) ([]Tool, error) {
	var tools []Tool
	var cursor string
	for page := 0; page < 10; page++ {
		var paramsMap map[string]any
		if cursor != "" {
			paramsMap = map[string]any{"cursor": cursor}
		}
		var rawParams json.RawMessage
		if len(paramsMap) > 0 {
			rawParams, _ = json.Marshal(paramsMap)
		}

		res, rpcErr, err := f.forward(ctx, "tools/list", rawParams)
		if err != nil {
			return nil, err
		}
		if rpcErr != nil {
			return nil, fmt.Errorf("rpc error (%d): %s", rpcErr.Code, rpcErr.Message)
		}

		var listResult struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &listResult); err != nil {
			return nil, fmt.Errorf("unmarshal tools/list result failed: %w", err)
		}

		for _, t := range listResult.Tools {
			schema := t.InputSchema
			if len(bytes.TrimSpace(schema)) == 0 || string(bytes.TrimSpace(schema)) == "null" {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tools = append(tools, Tool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: schema,
			})
		}

		if listResult.NextCursor == "" {
			break
		}
		cursor = listResult.NextCursor
	}
	return tools, nil
}

func genericCallTool(ctx context.Context, f forwarder, name string, args json.RawMessage) (json.RawMessage, error) {
	// 调用方未设置截止时间时用放宽的 tools/call 超时，而非通用 RPC 超时。
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, toolCallTimeout)
		defer cancel()
	}
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("{}")
	}
	callParams := map[string]any{
		"name":      name,
		"arguments": args,
	}
	paramBytes, err := json.Marshal(callParams)
	if err != nil {
		return nil, err
	}

	res, rpcErr, err := f.forward(ctx, "tools/call", paramBytes)
	if err != nil {
		return nil, err
	}
	if rpcErr != nil {
		return nil, fmt.Errorf("tool error (%d): %s", rpcErr.Code, rpcErr.Message)
	}
	return res, nil
}
