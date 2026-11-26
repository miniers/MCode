// Package openartifact 实现 open-artifact 的 stdio harness 桥接客户端。
package openartifact

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mcode/internal/procattr"
)

const (
	// CLIVersion 固定 @open-artifact/cli 的版本，避免 npx 拉取未验证的升级。
	CLIVersion = "0.16.2"
	// HarnessName 是 mcode 向 open-artifact 声明的 harness 名称。
	HarnessName = "mcode"

	// maxLineBytes 与桥接进程单行 64 MiB 上限对齐。
	maxLineBytes = 64 << 20
	// defaultReplyTimeout 小于 daemon 的 30 秒调用超时，确保宿主按时回复。
	defaultReplyTimeout = 25 * time.Second
	// defaultCloseGrace 是关闭 stdin 后等待桥接进程自行退出的宽限时间。
	defaultCloseGrace = 3 * time.Second
	// defaultKillWait 是触发取消或强杀后等待进程退出的超时时间。
	defaultKillWait = 2 * time.Second
	// inboxReturnBatchLimit 对齐 protocol 的 INBOX_RETURN_MAX。
	inboxReturnBatchLimit = 500
)

// ErrNotReady 表示桥接进程未启动、已关闭或已退出。
var ErrNotReady = errors.New("openartifact: 桥接进程未就绪")

// Host 定义 open-artifact 桥接回调给宿主的能力。
type Host interface {
	DeliverWake(threadID, text string) error
	OpenURL(threadID, url string) bool
}

// Options 配置 Bridge 的运行环境与宿主回调。
type Options struct {
	// NodeBinDir 非空时放到 PATH 最前面，并优先从中解析 npx。
	NodeBinDir string
	// Env 为子进程环境变量；为空时继承当前进程环境。
	Env []string
	// Host 接收 wake.deliver 与 host.open 回调。
	Host Host
	// Logf 记录 stderr 与诊断日志；为空时丢弃日志。
	Logf func(string, ...any)
}

// ToolDefinition 描述 tools.list 返回的单个工具定义。
type ToolDefinition struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	InputSchema   json.RawMessage   `json:"inputSchema"`
	Annotations   json.RawMessage   `json:"annotations,omitempty"`
	Actions       map[string]string `json:"actions,omitempty"`
	RemoteActions []string          `json:"remoteActions,omitempty"`
}

// RPCError 表示桥接或 daemon 返回的 JSON-RPC 错误。
type RPCError struct {
	Code     int
	Message  string
	DataCode string
	Fix      string
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	msg := e.Message
	if msg == "" {
		msg = "rpc error"
	}
	if e.DataCode != "" {
		msg = fmt.Sprintf("%s (%s)", msg, e.DataCode)
	}
	if e.Fix != "" {
		msg = fmt.Sprintf("%s: %s", msg, e.Fix)
	}
	return fmt.Sprintf("openartifact: %s", msg)
}

// Bridge 管理 open-artifact stdio 桥接子进程及其 JSON-RPC 2.0 会话。
type Bridge struct {
	opts Options

	cmdFactory   func(ctx context.Context) (*exec.Cmd, error)
	replyTimeout time.Duration
	closeGrace   time.Duration
	killWait     time.Duration

	nextID atomic.Int64

	lifecycleMu sync.Mutex
	writeMu     sync.Mutex

	mu                sync.Mutex
	ready             bool
	cmd               *exec.Cmd
	stdin             io.WriteCloser
	procCancel        context.CancelFunc
	doneCh            chan struct{}
	waitDone          chan struct{}
	waitErr           error
	readDone          chan struct{}
	stderrDone        chan struct{}
	pending           map[string]chan rpcResponse
	features          map[string]bool
	attached          map[string]bool
	attaching         map[string]chan struct{}
	sessionWorkspaces map[string]string
}

type rpcResponse struct {
	result json.RawMessage
	err    error
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcWireError   `json:"error,omitempty"`
}

type rpcWireError struct {
	Code    int               `json:"code"`
	Message string            `json:"message"`
	Data    *rpcWireErrorData `json:"data,omitempty"`
}

type rpcWireErrorData struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Fix     string `json:"fix,omitempty"`
}

// New 创建一个未启动的 Bridge 实例。
func New(opts Options) *Bridge {
	b := &Bridge{
		opts:              opts,
		replyTimeout:      defaultReplyTimeout,
		closeGrace:        defaultCloseGrace,
		killWait:          defaultKillWait,
		pending:           make(map[string]chan rpcResponse),
		features:          make(map[string]bool),
		attached:          make(map[string]bool),
		attaching:         make(map[string]chan struct{}),
		sessionWorkspaces: make(map[string]string),
	}
	b.cmdFactory = b.defaultCommand
	return b
}

func (b *Bridge) defaultCommand(ctx context.Context) (*exec.Cmd, error) {
	env := buildEnv(b.opts.Env, b.opts.NodeBinDir)
	bin := resolveCommandInEnv("npx", env)
	args := []string{"-y", "@open-artifact/cli@" + CLIVersion, "harness", "--stdio", "--harness", HarnessName}
	cmd := procattr.TerminateOnCancel(procattr.HideWindow(exec.CommandContext(ctx, bin, args...)))
	if env != nil {
		cmd.Env = env
	}
	return cmd, nil
}

// Start 启动桥接子进程并完成 hello 握手。
func (b *Bridge) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	b.lifecycleMu.Lock()
	defer b.lifecycleMu.Unlock()

	if b.Ready() {
		return nil
	}

	procCtx, procCancel := context.WithCancel(context.Background())
	cmd, err := b.cmdFactory(procCtx)
	if err != nil {
		procCancel()
		return fmt.Errorf("openartifact: 创建桥接命令失败: %w", err)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		procCancel()
		return fmt.Errorf("openartifact: 创建 stdin 管道失败: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		procCancel()
		return fmt.Errorf("openartifact: 创建 stdout 管道失败: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		procCancel()
		return fmt.Errorf("openartifact: 创建 stderr 管道失败: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		procCancel()
		return fmt.Errorf("openartifact: 启动桥接进程失败: %w", err)
	}

	doneCh := make(chan struct{})
	waitDone := make(chan struct{})
	readDone := make(chan struct{})
	stderrDone := make(chan struct{})

	b.mu.Lock()
	b.ready = false
	b.cmd = cmd
	b.stdin = stdin
	b.procCancel = procCancel
	b.doneCh = doneCh
	b.waitDone = waitDone
	b.waitErr = nil
	b.readDone = readDone
	b.stderrDone = stderrDone
	b.pending = make(map[string]chan rpcResponse)
	b.features = make(map[string]bool)
	b.attached = make(map[string]bool)
	b.attaching = make(map[string]chan struct{})
	b.sessionWorkspaces = make(map[string]string)
	b.mu.Unlock()

	go b.stderrLoop(stderr, stderrDone)
	go b.readLoop(stdout, doneCh, readDone)
	go b.waitLoop(cmd, waitDone)

	helloParams := map[string]any{
		"harness":   HarnessName,
		"protocols": map[string]int{"min": 1, "max": 1},
		"caps": map[string]any{
			"agentName": HarnessName,
			"tools":     "native",
			"wake":      map[string]any{},
			// 依据 docs/spec/embed.md:14：声明 embed:"view" 后宿主必须处理 shell.event 与 shell.command，故此处用 true。
			"open": true,
		},
	}

	rawHello, err := b.call(ctx, false, "hello", helloParams)
	if err != nil {
		_ = b.closeLocked()
		return err
	}

	var helloRes struct {
		Protocol   int      `json:"protocol"`
		Daemon     string   `json:"daemon"`
		Connection string   `json:"connection"`
		Features   []string `json:"features"`
	}
	if len(rawHello) > 0 {
		if err := json.Unmarshal(rawHello, &helloRes); err != nil {
			_ = b.closeLocked()
			return fmt.Errorf("openartifact: 解析 hello 响应失败: %w", err)
		}
	}

	features := make(map[string]bool, len(helloRes.Features))
	for _, f := range helloRes.Features {
		features[f] = true
	}

	b.mu.Lock()
	// 仅在进程尚未退出时标记就绪。
	select {
	case <-doneCh:
		b.mu.Unlock()
		_ = b.closeLocked()
		return ErrNotReady
	default:
		b.features = features
		b.ready = true
		b.mu.Unlock()
	}

	return nil
}

// Close 关闭桥接 stdin 并等待进程退出；超时后触发取消与强杀。
func (b *Bridge) Close() error {
	b.lifecycleMu.Lock()
	defer b.lifecycleMu.Unlock()
	return b.closeLocked()
}

func (b *Bridge) closeLocked() error {
	b.mu.Lock()
	cmd := b.cmd
	stdin := b.stdin
	procCancel := b.procCancel
	doneCh := b.doneCh
	waitDone := b.waitDone
	readDone := b.readDone
	stderrDone := b.stderrDone
	b.ready = false
	b.cmd = nil
	b.stdin = nil
	b.procCancel = nil
	b.attached = make(map[string]bool)
	b.sessionWorkspaces = make(map[string]string)
	b.mu.Unlock()

	if cmd == nil && stdin == nil {
		return nil
	}

	if stdin != nil {
		b.writeMu.Lock()
		_ = stdin.Close()
		b.writeMu.Unlock()
	}

	if waitDone != nil {
		grace := b.closeGrace
		if grace <= 0 {
			grace = defaultCloseGrace
		}
		timer := time.NewTimer(grace)
		select {
		case <-waitDone:
			timer.Stop()
		case <-timer.C:
			if procCancel != nil {
				procCancel()
			}
			killWait := b.killWait
			if killWait <= 0 {
				killWait = defaultKillWait
			}
			killTimer := time.NewTimer(killWait)
			select {
			case <-waitDone:
				killTimer.Stop()
			case <-killTimer.C:
				if cmd != nil && cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				<-waitDone
			}
		}
	}

	if procCancel != nil {
		procCancel()
	}

	waitShort(readDone, 500*time.Millisecond)
	waitShort(stderrDone, 500*time.Millisecond)
	waitShort(doneCh, 500*time.Millisecond)

	b.failAllPending(ErrNotReady)
	return nil
}

func waitShort(ch <-chan struct{}, d time.Duration) {
	if ch == nil {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C:
	}
}

// Ready 返回桥接进程当前是否已握手且仍在运行。
func (b *Bridge) Ready() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ready
}

// ListTools 调用 tools.list 返回可用工具定义。
func (b *Bridge) ListTools(ctx context.Context) ([]ToolDefinition, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := b.call(ctx, true, "tools.list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var res struct {
		Tools []ToolDefinition `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("openartifact: 解析 tools.list 响应失败: %w", err)
	}
	return res.Tools, nil
}

// CallTool 调用指定工具；若该会话尚未在本连接附着，则先发送 session.attach。
func (b *Bridge) CallTool(ctx context.Context, threadID, cwd, name string, args json.RawMessage) (json.RawMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !b.Ready() {
		return nil, ErrNotReady
	}
	if threadID != "" || cwd != "" {
		if err := b.ensureAttached(ctx, threadID, cwd); err != nil {
			return nil, err
		}
	}

	trimmedArgs := bytes.TrimSpace(args)
	if len(trimmedArgs) == 0 {
		trimmedArgs = json.RawMessage(`{}`)
	}

	params := map[string]any{
		"name":      name,
		"arguments": json.RawMessage(trimmedArgs),
	}
	if caller := buildSessionParams(threadID, cwd); len(caller) > 0 {
		params["caller"] = caller
	}
	return b.call(ctx, true, "tools.call", params)
}

// SessionStatus 调用 session.status 更新会话状态（如 idle、running）。
func (b *Bridge) SessionStatus(ctx context.Context, threadID, status string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	params := buildSessionParams(threadID, b.workspaceFor(threadID))
	params["status"] = status
	_, err := b.call(ctx, true, "session.status", params)
	return err
}

// Detach 调用 session.detach 分离会话并清除本地附着记录。
func (b *Bridge) Detach(ctx context.Context, threadID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cwd := b.workspaceFor(threadID)
	params := buildSessionParams(threadID, cwd)
	if _, err := b.call(ctx, true, "session.detach", params); err != nil {
		return err
	}
	b.mu.Lock()
	if key := sessionKey(threadID, cwd); key != "" {
		delete(b.attached, key)
	}
	if threadID != "" {
		delete(b.attached, "s:"+threadID)
		delete(b.sessionWorkspaces, threadID)
	}
	b.mu.Unlock()
	return nil
}

// Skill 调用 skill.get 获取面向 mcode 原生工具的入口 skill 正文。
func (b *Bridge) Skill(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	params := map[string]any{
		"form":      "entry",
		"tools":     "native",
		"opens":     "host",
		"agentName": HarnessName,
	}
	raw, err := b.call(ctx, true, "skill.get", params)
	if err != nil {
		return "", err
	}
	var res struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("openartifact: 解析 skill.get 响应失败: %w", err)
	}
	return res.Body, nil
}

// HomeURL 调用 embed.origins 返回当前连接的 daemon 主页地址。
func (b *Bridge) HomeURL(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := b.call(ctx, true, "embed.origins", map[string]any{})
	if err != nil {
		return "", err
	}
	var res struct {
		Entries []struct {
			Origin string `json:"origin"`
			Kind   string `json:"kind"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("openartifact: 解析 embed.origins 响应失败: %w", err)
	}
	for _, entry := range res.Entries {
		if entry.Kind == "daemon" {
			if origin := strings.TrimSpace(entry.Origin); origin != "" {
				return strings.TrimRight(origin, "/"), nil
			}
		}
	}
	return "", errors.New("openartifact: daemon 未返回主页地址")
}

func (b *Bridge) ensureAttached(ctx context.Context, threadID, cwd string) error {
	key := sessionKey(threadID, cwd)
	if key == "" {
		return nil
	}

	for {
		b.mu.Lock()
		if !b.ready {
			b.mu.Unlock()
			return ErrNotReady
		}
		if b.attached[key] {
			b.mu.Unlock()
			return nil
		}
		if waitCh, ok := b.attaching[key]; ok {
			b.mu.Unlock()
			select {
			case <-waitCh:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		waitCh := make(chan struct{})
		b.attaching[key] = waitCh
		b.mu.Unlock()

		params := buildSessionParams(threadID, cwd)
		raw, err := b.call(ctx, true, "session.attach", params)
		if err != nil {
			b.mu.Lock()
			delete(b.attaching, key)
			close(waitCh)
			b.mu.Unlock()
			return err
		}

		var attachRes struct {
			Key   string            `json:"key"`
			Inbox []json.RawMessage `json:"inbox"`
		}
		if len(raw) > 0 {
			if unmarshalErr := json.Unmarshal(raw, &attachRes); unmarshalErr != nil {
				b.logf("openartifact: 解析 session.attach 响应失败: %v", unmarshalErr)
			}
		}

		b.mu.Lock()
		b.attached[key] = true
		if threadID != "" && cwd != "" {
			b.sessionWorkspaces[threadID] = cwd
		}
		delete(b.attaching, key)
		close(waitCh)
		b.mu.Unlock()

		if len(attachRes.Inbox) > 0 {
			b.deliverInbox(threadID, cwd, attachRes.Inbox)
		}
		return nil
	}
}

func (b *Bridge) workspaceFor(threadID string) string {
	if threadID == "" {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessionWorkspaces[threadID]
}

func (b *Bridge) call(ctx context.Context, requireReady bool, method string, params any) (json.RawMessage, error) {
	b.mu.Lock()
	if requireReady && !b.ready {
		b.mu.Unlock()
		return nil, ErrNotReady
	}
	if b.stdin == nil {
		b.mu.Unlock()
		return nil, ErrNotReady
	}
	doneCh := b.doneCh
	id := b.nextID.Add(1)
	idKey := strconv.FormatInt(id, 10)
	respCh := make(chan rpcResponse, 1)
	b.pending[idKey] = respCh
	b.mu.Unlock()

	payload := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int64  `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	if err := b.writeJSON(payload); err != nil {
		b.removePending(idKey)
		return nil, err
	}

	select {
	case resp := <-respCh:
		return resp.result, resp.err
	case <-ctx.Done():
		b.removePending(idKey)
		return nil, ctx.Err()
	case <-doneCh:
		select {
		case resp := <-respCh:
			return resp.result, resp.err
		default:
			b.removePending(idKey)
			return nil, ErrNotReady
		}
	}
}

func (b *Bridge) removePending(idKey string) {
	b.mu.Lock()
	delete(b.pending, idKey)
	b.mu.Unlock()
}

func (b *Bridge) failAllPending(err error) {
	b.mu.Lock()
	pending := b.pending
	b.pending = make(map[string]chan rpcResponse)
	b.mu.Unlock()

	for _, ch := range pending {
		select {
		case ch <- rpcResponse{err: err}:
		default:
		}
	}
}

func (b *Bridge) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("openartifact: 序列化 JSON-RPC 消息失败: %w", err)
	}
	data = append(data, '\n')

	b.writeMu.Lock()
	defer b.writeMu.Unlock()

	b.mu.Lock()
	stdin := b.stdin
	b.mu.Unlock()
	if stdin == nil {
		return ErrNotReady
	}

	if _, err := stdin.Write(data); err != nil {
		return fmt.Errorf("openartifact: 写入桥接进程失败: %w", err)
	}
	return nil
}

func (b *Bridge) waitLoop(cmd *exec.Cmd, waitDone chan struct{}) {
	defer close(waitDone)
	err := cmd.Wait()
	b.mu.Lock()
	b.waitErr = err
	b.mu.Unlock()
	if err != nil {
		b.logf("openartifact: 桥接进程退出: %v", err)
	}
}

func (b *Bridge) stderrLoop(r io.Reader, stderrDone chan struct{}) {
	defer close(stderrDone)
	br := bufio.NewReaderSize(r, 16*1024)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimRight(line, "\r\n")
			if trimmed != "" {
				b.logf("openartifact stderr: %s", trimmed)
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				b.logf("openartifact: 读取 stderr 失败: %v", err)
			}
			return
		}
	}
}

func (b *Bridge) readLoop(r io.Reader, doneCh, readDone chan struct{}) {
	defer func() {
		b.mu.Lock()
		b.ready = false
		b.stdin = nil
		b.mu.Unlock()
		close(doneCh)
		b.failAllPending(ErrNotReady)
		close(readDone)
	}()

	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > maxLineBytes {
			b.logf("openartifact: 收到超过 64 MiB 的响应行，结束读取")
			return
		}
		if len(line) > 0 {
			b.handleLine(line)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				b.logf("openartifact: 读取 stdout 失败: %v", err)
			}
			return
		}
	}
}

func (b *Bridge) handleLine(raw []byte) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return
	}
	var msg rpcMessage
	if err := json.Unmarshal(trimmed, &msg); err != nil {
		b.logf("openartifact: 忽略非 JSON 行: %v", err)
		return
	}

	if msg.Method != "" {
		if hasRPCID(msg.ID) {
			go b.handleRequest(msg)
			return
		}
		b.handleNotification(msg.Method, msg.Params)
		return
	}

	if !hasRPCID(msg.ID) {
		return
	}
	idKey := normalizeRPCID(msg.ID)

	b.mu.Lock()
	ch, ok := b.pending[idKey]
	if ok {
		delete(b.pending, idKey)
	}
	b.mu.Unlock()

	if !ok {
		return
	}

	if msg.Error != nil {
		rpcErr := &RPCError{
			Code:    msg.Error.Code,
			Message: msg.Error.Message,
		}
		if msg.Error.Data != nil {
			rpcErr.DataCode = msg.Error.Data.Code
			if msg.Error.Data.Message != "" {
				rpcErr.Message = msg.Error.Data.Message
			}
			rpcErr.Fix = msg.Error.Data.Fix
		}
		ch <- rpcResponse{err: rpcErr}
		return
	}
	ch <- rpcResponse{result: msg.Result}
}

func (b *Bridge) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "bridge.state":
		var p struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(params, &p); err == nil {
			b.logf("openartifact: 桥接状态变更: %s", p.State)
		}
	case "bridge.reattached":
		go b.handleReattached(params)
	default:
		// 忽略未知通知（如未订阅的 event 扩展字段）。
	}
}

func (b *Bridge) handleRequest(msg rpcMessage) {
	switch msg.Method {
	case "wake.deliver":
		var p struct {
			Session string `json:"session"`
			Text    string `json:"text"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			b.sendError(msg.ID, -32602, "invalid_argument", "无效的 wake.deliver 参数")
			return
		}
		status := "failed"
		if err := b.deliverWakeWithTimeout(p.Session, p.Text); err == nil {
			status = "queued"
		}
		b.sendResult(msg.ID, map[string]string{"status": status})

	case "host.open":
		var p struct {
			Session string `json:"session"`
			URL     string `json:"url"`
			ViewURL string `json:"view_url"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			b.sendError(msg.ID, -32602, "invalid_argument", "无效的 host.open 参数")
			return
		}
		target := p.ViewURL
		if target == "" {
			target = p.URL
		}
		opened := b.openURLWithTimeout(p.Session, target)
		b.sendResult(msg.ID, map[string]bool{"opened": opened})

	default:
		b.sendError(msg.ID, -32601, "method_not_found", fmt.Sprintf("未知方法: %s", msg.Method))
	}
}

func (b *Bridge) deliverWakeWithTimeout(threadID, text string) error {
	if b.opts.Host == nil || threadID == "" {
		return errors.New("openartifact: 缺少宿主或会话标识")
	}
	timeout := b.replyTimeout
	if timeout <= 0 {
		timeout = defaultReplyTimeout
	}
	done := make(chan error, 1)
	go func() {
		done <- b.opts.Host.DeliverWake(threadID, text)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		b.logf("openartifact: DeliverWake 超时 (%s)", threadID)
		return errors.New("openartifact: DeliverWake 超时")
	}
}

func (b *Bridge) openURLWithTimeout(threadID, targetURL string) bool {
	if b.opts.Host == nil || targetURL == "" {
		return false
	}
	timeout := b.replyTimeout
	if timeout <= 0 {
		timeout = defaultReplyTimeout
	}
	done := make(chan bool, 1)
	go func() {
		done <- b.opts.Host.OpenURL(threadID, targetURL)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case ok := <-done:
		return ok
	case <-timer.C:
		b.logf("openartifact: OpenURL 超时 (%s)", threadID)
		return false
	}
}

func (b *Bridge) handleReattached(params json.RawMessage) {
	var p struct {
		Params struct {
			Session   string `json:"session"`
			Workspace string `json:"workspace"`
		} `json:"params"`
		Inbox []json.RawMessage `json:"inbox"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		b.logf("openartifact: 解析 bridge.reattached 失败: %v", err)
		return
	}
	if len(p.Inbox) == 0 {
		return
	}
	b.deliverInbox(p.Params.Session, p.Params.Workspace, p.Inbox)
}

func (b *Bridge) deliverInbox(threadID, cwd string, inbox []json.RawMessage) {
	var undelivered []json.RawMessage
	for _, rawMsg := range inbox {
		var item struct {
			Session string `json:"session"`
			Text    string `json:"text"`
		}
		if err := json.Unmarshal(rawMsg, &item); err != nil {
			undelivered = append(undelivered, rawMsg)
			continue
		}
		targetSession := item.Session
		if targetSession == "" {
			targetSession = threadID
		}
		if err := b.deliverWakeWithTimeout(targetSession, item.Text); err != nil {
			undelivered = append(undelivered, rawMsg)
		}
	}

	if len(undelivered) == 0 {
		return
	}
	if !b.supportsInboxReturn() {
		b.logf("openartifact: 存在 %d 条未投递消息但 daemon 未声明 inbox.return", len(undelivered))
		return
	}

	for start := 0; start < len(undelivered); start += inboxReturnBatchLimit {
		end := start + inboxReturnBatchLimit
		if end > len(undelivered) {
			end = len(undelivered)
		}
		reqParams := buildSessionParams(threadID, cwd)
		reqParams["messages"] = undelivered[start:end]
		ctx, cancel := context.WithTimeout(context.Background(), b.replyTimeout)
		_, err := b.call(ctx, true, "inbox.return", reqParams)
		cancel()
		if err != nil {
			b.logf("openartifact: inbox.return 失败: %v", err)
			return
		}
	}
}

func (b *Bridge) supportsInboxReturn() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	// 若 hello 返回了非空特性清单，按特性清单判断；未返回特性清单时允许尝试。
	if len(b.features) == 0 {
		return true
	}
	return b.features["inbox.return"]
}

func (b *Bridge) sendResult(id json.RawMessage, result any) {
	resp := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	if err := b.writeJSON(resp); err != nil {
		b.logf("openartifact: 发送响应失败: %v", err)
	}
}

func (b *Bridge) sendError(id json.RawMessage, rpcCode int, dataCode, message string) {
	resp := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   rpcWireError    `json:"error"`
	}{
		JSONRPC: "2.0",
		ID:      id,
		Error: rpcWireError{
			Code:    rpcCode,
			Message: message,
			Data: &rpcWireErrorData{
				Code:    dataCode,
				Message: message,
			},
		},
	}
	if err := b.writeJSON(resp); err != nil {
		b.logf("openartifact: 发送错误响应失败: %v", err)
	}
}

func (b *Bridge) logf(format string, args ...any) {
	if b.opts.Logf != nil {
		b.opts.Logf(format, args...)
	}
}

func hasRPCID(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func normalizeRPCID(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) >= 2 && trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return "str:" + s
		}
	}
	return string(trimmed)
}

func sessionKey(threadID, cwd string) string {
	if threadID != "" {
		return "s:" + threadID
	}
	if cwd != "" {
		return "w:" + cwd
	}
	return ""
}

func buildSessionParams(threadID, cwd string) map[string]any {
	params := make(map[string]any, 2)
	if threadID != "" {
		params["session"] = threadID
	}
	if cwd != "" {
		params["workspace"] = cwd
	}
	return params
}

func buildEnv(base []string, nodeBinDir string) []string {
	if base == nil && nodeBinDir == "" {
		return nil
	}
	var env []string
	if base != nil {
		env = append([]string(nil), base...)
	} else {
		env = os.Environ()
	}
	if nodeBinDir == "" {
		return env
	}

	sep := string(os.PathListSeparator)
	for i, entry := range env {
		k, v, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(k, "PATH") {
			if v == "" {
				env[i] = k + "=" + nodeBinDir
			} else {
				env[i] = k + "=" + nodeBinDir + sep + v
			}
			return env
		}
	}
	return append(env, "PATH="+nodeBinDir)
}

func resolveCommandInEnv(command string, env []string) string {
	if command == "" || strings.ContainsAny(command, `/\`) {
		return command
	}
	var pathVal string
	for _, entry := range env {
		if k, v, ok := strings.Cut(entry, "="); ok && strings.EqualFold(k, "PATH") {
			pathVal = v
			break
		}
	}
	if pathVal == "" {
		return command
	}

	names := []string{command}
	if runtime.GOOS == "windows" {
		names = []string{command + ".cmd", command + ".exe", command + ".bat", command}
	}
	for _, dir := range filepath.SplitList(pathVal) {
		if dir == "" {
			continue
		}
		for _, name := range names {
			candidate := filepath.Join(dir, name)
			info, err := os.Stat(candidate)
			if err != nil || info.IsDir() {
				continue
			}
			if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
				continue
			}
			return candidate
		}
	}
	return command
}
