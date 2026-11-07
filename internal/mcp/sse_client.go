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
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ==========================================
// sseClient 实现（经典 HTTP+SSE 传输）
// ==========================================
//
// 握手流程：GET 目标 URL 建立事件流，上游先发 `event: endpoint` 告诉我们消息
// POST 到哪个地址；之后的请求体一律 POST 到该地址（200/202 且忽略响应体），
// 响应则以 `event: message` 的形式回到 GET 流上，按 id 匹配本地 pending。
// 不少用户把 streamable HTTP 服务误配成 sse，这类服务对 GET 会回 4xx 或非
// text/event-stream，此时内部改为委托 httpClient 完成全部调用。

// sseEvent 为解析出的单个 SSE 事件。
type sseEvent struct {
	name string
	data string
}

type sseClient struct {
	spec   UpstreamSpec
	tokens TokenSource

	startMu sync.Mutex
	once    sync.Once
	closed  int32
	deleted chan struct{}

	mu           sync.Mutex
	writeMu      sync.Mutex
	client       *http.Client
	stream       io.ReadCloser
	streamCancel context.CancelFunc
	postURL      string
	initDone     bool // 本次连接已发过 initialize
	pendings     map[int64]chan rpcResponseMsg
	nextID       int64
	sName        string
	sVer         string
	fallback     *httpClient // 上游其实是 streamable HTTP 时的委托对象
}

func newSSEClient(spec UpstreamSpec, tokens TokenSource) *sseClient {
	// 事件流是长连，请求不能用带超时的上下文，交握阶段只用 ResponseHeaderTimeout 兜底。
	var rt http.RoundTripper = http.DefaultTransport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		cloned := base.Clone()
		cloned.ResponseHeaderTimeout = 30 * time.Second
		rt = cloned
	}
	return &sseClient{
		spec:     spec,
		tokens:   tokens,
		client:   &http.Client{Transport: rt},
		deleted:  make(chan struct{}),
		pendings: make(map[int64]chan rpcResponseMsg),
	}
}

func (c *sseClient) serverInfo() (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fallback != nil {
		return c.fallback.serverInfo()
	}
	return c.sName, c.sVer
}

func (c *sseClient) initialize(ctx context.Context) error {
	return c.ensureInitialized(ctx)
}

func (c *sseClient) ensureInitialized(ctx context.Context) error {
	c.startMu.Lock()
	defer c.startMu.Unlock()

	c.mu.Lock()
	fb := c.fallback
	ready := c.initDone && c.stream != nil && c.postURL != ""
	c.mu.Unlock()

	if fb != nil {
		return fb.ensureInitialized(ctx)
	}
	if ready {
		return nil
	}

	target, err := c.connect(ctx)
	if err != nil {
		return err
	}

	c.mu.Lock()
	fb = c.fallback
	c.mu.Unlock()
	if fb != nil {
		// 已回退成 streamable HTTP，初始化由委托对象负责。
		return nil
	}

	if err := c.doInitialize(ctx, target); err != nil {
		return err
	}

	c.mu.Lock()
	c.initDone = true
	c.mu.Unlock()
	return nil
}

// connect 建立事件流并等待 endpoint 事件，返回 POST 地址。
// 若判定上游其实是 streamable HTTP，则安装委托对象并返回空地址。
func (c *sseClient) connect(ctx context.Context) (string, error) {
	// reqCtx 只用作交握的时间预算；GET 请求本身跑在独立的 streamCtx 上，
	// 否则握手一返回，defer cancel 就会把刚建立的事件流断掉。
	reqCtx, cancel := rpcCtx(ctx)
	defer cancel()

	streamCtx, streamCancel := context.WithCancel(context.Background())
	httpReq, err := http.NewRequestWithContext(streamCtx, http.MethodGet, c.spec.URL, nil)
	if err != nil {
		streamCancel()
		return "", err
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	for k, v := range c.spec.Headers {
		httpReq.Header.Set(k, v)
	}
	if err := c.applyAuth(reqCtx, httpReq); err != nil {
		streamCancel()
		return "", err
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		streamCancel()
		return "", err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		streamCancel()
		return "", ErrUpstreamUnauthorized
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	// 404/405/400 或不是事件流：说明这个服务其实是 streamable HTTP。
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed ||
		resp.StatusCode == http.StatusBadRequest ||
		!strings.Contains(contentType, "text/event-stream") {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		streamCancel()
		return "", c.useFallback(reqCtx)
	}

	// ready 是本次建流专用的唤醒信号：endpoint 就位或流已断开时关闭。
	// 每次重连都新建，避免复用到上一次（可能已关闭）的通道。
	ready := make(chan struct{})
	c.mu.Lock()
	old := c.stream
	c.stream = resp.Body
	c.streamCancel = streamCancel
	c.postURL = ""
	c.initDone = false
	c.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}

	go c.readStream(streamCtx, streamCancel, resp.Body, ready)

	select {
	case <-ready:
		c.mu.Lock()
		target := c.postURL
		alive := c.stream == resp.Body
		c.mu.Unlock()
		if target != "" && alive {
			return target, nil
		}
		streamCancel()
		if target == "" {
			// 拿不到 endpoint 说明它根本不发事件流（或发完就断），当 streamable HTTP 处理。
			return "", c.useFallback(reqCtx)
		}
		return "", errors.New("sse stream closed before endpoint event")
	case <-reqCtx.Done():
		streamCancel()
		return "", reqCtx.Err()
	case <-c.deleted:
		streamCancel()
		return "", errors.New("client closed")
	}
}

// useFallback 切换到 streamable HTTP 委托并完成其初始化。
func (c *sseClient) useFallback(ctx context.Context) error {
	fb := newHTTPClient(c.spec, c.tokens)
	c.mu.Lock()
	old := c.stream
	oldCancel := c.streamCancel
	c.fallback = fb
	c.stream = nil
	c.streamCancel = nil
	c.postURL = ""
	c.initDone = false
	c.mu.Unlock()
	if oldCancel != nil {
		oldCancel()
	}
	if old != nil {
		_ = old.Close()
	}
	return fb.ensureInitialized(ctx)
}

// applyAuth 按与 httpClient 相同的方式取 Bearer token 并写入请求头。
func (c *sseClient) applyAuth(ctx context.Context, req *http.Request) error {
	if !c.spec.OAuth || c.tokens == nil {
		return nil
	}
	tokenID := c.spec.OAuthID
	if tokenID == "" {
		tokenID = c.spec.ID
	}
	tok, err := c.tokens.AccessToken(ctx, tokenID)
	if err != nil && !errors.Is(err, ErrOAuthNotSignedIn) {
		return fmt.Errorf("failed to get access token: %w", err)
	}
	if err == nil && tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return nil
}

// readStream 解析 GET 事件流，把 message 事件按 id 投递给等待方。
// 流断开后置为未连接、给所有 pending 返回错误，下次调用自动重连。
func (c *sseClient) readStream(ctx context.Context, cancel context.CancelFunc, body io.ReadCloser, ready chan struct{}) {
	defer cancel()
	defer body.Close()

	// onEndpoint 与下面的流结束唤醒都在本 goroutine 内串行执行，
	// 用 select-default 关闭即可保证只会关一次。
	streamErr := c.consumeEvents(ctx, body, func(target string) {
		c.mu.Lock()
		c.postURL = target
		c.mu.Unlock()
		select {
		case <-ready:
		default:
			close(ready)
		}
	})
	if streamErr == nil {
		streamErr = errors.New("sse stream closed")
	}

	c.mu.Lock()
	var pending map[int64]chan rpcResponseMsg
	// 只有当前流才算数：新连接已经建立时，旧流退出不能碰新连接的 postURL 与 pending。
	if c.stream == body {
		c.stream = nil
		c.streamCancel = nil
		c.postURL = ""
		c.initDone = false
		pending = c.pendings
		c.pendings = make(map[int64]chan rpcResponseMsg)
	}
	c.mu.Unlock()

	for _, ch := range pending {
		ch <- rpcResponseMsg{err: streamErr}
	}

	// 未拿到 endpoint 就断流：唤醒 connect 的等待方，让它返回错误而不是挂住。
	select {
	case <-ready:
	default:
		close(ready)
	}
}

// consumeEvents 逐行解析 SSE 文本流，返回读流失败原因（正常结束时为 nil）。
// 收到 endpoint 事件时通过 onEndpoint 把 POST 地址告知调用方。
func (c *sseClient) consumeEvents(ctx context.Context, body io.ReadCloser, onEndpoint func(string)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)

	var event sseEvent
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			if event.name != "" || event.data != "" {
				if target := c.dispatchEvent(event); target != "" {
					onEndpoint(target)
				}
				event = sseEvent{}
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event.name = value
		case "data":
			if event.data != "" {
				event.data += "\n"
			}
			event.data += value
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("sse stream read error: %v", err)
	}
	return nil
}

// dispatchEvent 处理单个事件：message 是 JSON-RPC 响应，endpoint 返回 POST 地址。
func (c *sseClient) dispatchEvent(event sseEvent) string {
	switch event.name {
	case "endpoint":
		return c.resolveEndpoint(event.data)
	case "message":
		c.deliver(event.data)
	}
	return ""
}

// resolveEndpoint 把 endpoint 事件的 data（可能是相对路径）解析成绝对地址。
func (c *sseClient) resolveEndpoint(data string) string {
	data = strings.TrimSpace(data)
	if data == "" {
		return ""
	}
	base, err := url.Parse(c.spec.URL)
	if err != nil {
		return data
	}
	ref, err := url.Parse(data)
	if err != nil {
		return data
	}
	return base.ResolveReference(ref).String()
}

// deliver 把一条 JSON-RPC 响应消息投递给对应的等待方。
func (c *sseClient) deliver(payload string) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return
	}
	var raw rawIncomingRPC
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return
	}
	// 带 method 的是上游主动发来的请求/通知，不能当作响应。
	if raw.Method != "" {
		return
	}
	id, ok := parseRPCID(raw.ID)
	if !ok {
		return
	}
	c.mu.Lock()
	ch, found := c.pendings[id]
	if found {
		delete(c.pendings, id)
	}
	c.mu.Unlock()
	if found {
		ch <- rpcResponseMsg{result: raw.Result, rpcErr: raw.Error}
	}
}

func (c *sseClient) doInitialize(ctx context.Context, target string) error {
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

	res, rpcErr, err := c.sendRPC(ctx, target, "initialize", rawParams)
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
	}
	_ = json.Unmarshal(res, &initRes)
	c.mu.Lock()
	c.sName = initRes.ServerInfo.Name
	c.sVer = initRes.ServerInfo.Version
	c.mu.Unlock()

	// 发送 notifications/initialized 通知
	return c.sendNotification(ctx, target, "notifications/initialized", nil)
}

// sendNotification 通过 POST 地址发送 JSON-RPC 通知（无 id，不等待响应）。
func (c *sseClient) sendNotification(ctx context.Context, target, method string, params json.RawMessage) error {
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
	return c.post(ctx, target, data)
}

// post 把请求体 POST 到 endpoint 地址，接受 200/202 且忽略响应体。
func (c *sseClient) post(ctx context.Context, target string, body []byte) error {
	if target == "" {
		return errors.New("sse endpoint not established")
	}

	reqCtx, cancel := rpcCtx(ctx)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range c.spec.Headers {
		httpReq.Header.Set(k, v)
	}
	if err := c.applyAuth(reqCtx, httpReq); err != nil {
		return err
	}

	// 写请求串行化，避免并发 POST 时上游错序。
	c.writeMu.Lock()
	resp, err := c.client.Do(httpReq)
	c.writeMu.Unlock()
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUpstreamUnauthorized
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *sseClient) sendRPC(ctx context.Context, target, method string, params json.RawMessage) (json.RawMessage, *jsonRPCError, error) {
	if atomic.LoadInt32(&c.closed) != 0 {
		return nil, nil, errors.New("client closed")
	}

	id := atomic.AddInt64(&c.nextID, 1)
	ch := make(chan rpcResponseMsg, 1)

	c.mu.Lock()
	c.pendings[id] = ch
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
		c.dropPending(id)
		return nil, nil, err
	}
	if err := c.post(ctx, target, data); err != nil {
		c.dropPending(id)
		return nil, nil, err
	}

	reqCtx, cancel := rpcCtx(ctx)
	defer cancel()

	select {
	case <-reqCtx.Done():
		c.dropPending(id)
		return nil, nil, reqCtx.Err()
	case resp := <-ch:
		if resp.err != nil {
			return nil, nil, resp.err
		}
		return resp.result, resp.rpcErr, nil
	}
}

func (c *sseClient) dropPending(id int64) {
	c.mu.Lock()
	delete(c.pendings, id)
	c.mu.Unlock()
}

func (c *sseClient) forward(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *jsonRPCError, error) {
	if err := c.ensureInitialized(ctx); err != nil {
		return nil, nil, err
	}

	c.mu.Lock()
	fb := c.fallback
	target := c.postURL
	c.mu.Unlock()

	if fb != nil {
		return fb.forward(ctx, method, params)
	}
	if target == "" {
		// 建流成功但 endpoint 丢了（流刚断），重来一次即可。
		if err := c.ensureInitialized(ctx); err != nil {
			return nil, nil, err
		}
		c.mu.Lock()
		fb = c.fallback
		target = c.postURL
		c.mu.Unlock()
		if fb != nil {
			return fb.forward(ctx, method, params)
		}
	}
	return c.sendRPC(ctx, target, method, params)
}

func (c *sseClient) listTools(ctx context.Context) ([]Tool, error) {
	return genericListTools(ctx, c)
}

func (c *sseClient) callTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	return genericCallTool(ctx, c, name, args)
}

func (c *sseClient) close() error {
	c.once.Do(func() {
		atomic.StoreInt32(&c.closed, 1)
		close(c.deleted)
	})

	c.mu.Lock()
	stream := c.stream
	streamCancel := c.streamCancel
	fb := c.fallback
	c.stream = nil
	c.streamCancel = nil
	c.postURL = ""
	c.initDone = false
	pending := c.pendings
	c.pendings = make(map[int64]chan rpcResponseMsg)
	c.mu.Unlock()

	// 取消在建的 GET 请求，并关闭已建立的事件流。
	if streamCancel != nil {
		streamCancel()
	}
	if stream != nil {
		_ = stream.Close()
	}
	for _, ch := range pending {
		ch <- rpcResponseMsg{err: errors.New("client closed")}
	}
	if fb != nil {
		return fb.close()
	}
	c.client.CloseIdleConnections()
	return nil
}
