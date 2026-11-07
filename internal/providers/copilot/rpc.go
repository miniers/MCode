package copilot

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// maxFrameSize 是单条 JSON-RPC 消息正文的大小上限（16MB）：
// 超过即终止连接，避免异常 CLI 撑爆内存。
const maxFrameSize = 16 * 1024 * 1024

var (
	// ErrConnClosed 表示连接已正常关闭。
	ErrConnClosed = errors.New("copilot: connection closed")
	// ErrFrameTooLarge 表示对端发来的单帧超过 16MB 上限。
	ErrFrameTooLarge = errors.New("copilot: message over 16 MB")
)

// RPCError 表示符合 JSON-RPC 2.0 规范的错误对象。
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("copilot rpc error: code %d, message: %s", e.Code, e.Message)
}

// RequestHandler 处理对端发来的请求，返回响应结果或错误。
// 返回 *RPCError 时按该错误原样回写，否则以 -32603 回写。
type RequestHandler func(method string, params json.RawMessage) (any, error)

// NotificationHandler 处理对端发来的通知。
type NotificationHandler func(method string, params json.RawMessage)

// CloseHandler 在连接关闭时回调底层原因（正常关闭为 ErrConnClosed）。
type CloseHandler func(err error)

type rpcResponse struct {
	result json.RawMessage
	rpcErr *RPCError
}

type inboundMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Conn 是基于 `Content-Length: N\r\n\r\n<body>` 分帧的双向 JSON-RPC 2.0 连接。
type Conn struct {
	r io.Reader
	w io.Writer

	wmu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[string]chan *rpcResponse

	reqMu sync.RWMutex
	onReq RequestHandler

	notifMu sync.RWMutex
	onNotif NotificationHandler

	closeMu   sync.RWMutex
	onClose   CloseHandler
	closeErr  error
	localQuit bool

	closed    chan struct{}
	closeOnce sync.Once
}

// NewConn 创建并启动一个 JSON-RPC 2.0 连接。
func NewConn(r io.Reader, w io.Writer) *Conn {
	c := &Conn{
		r:       r,
		w:       w,
		pending: make(map[string]chan *rpcResponse),
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// SetRequestHandler 注册对端请求的回调处理函数。
func (c *Conn) SetRequestHandler(h RequestHandler) {
	c.reqMu.Lock()
	c.onReq = h
	c.reqMu.Unlock()
}

// SetNotificationHandler 注册对端通知的回调处理函数。
func (c *Conn) SetNotificationHandler(h NotificationHandler) {
	c.notifMu.Lock()
	c.onNotif = h
	c.notifMu.Unlock()
}

// SetCloseHandler 注册连接关闭的回调处理函数。
func (c *Conn) SetCloseHandler(h CloseHandler) {
	c.closeMu.Lock()
	c.onClose = h
	c.closeMu.Unlock()
}

// Call 发送带唯一递增 id 的请求，并在 ctx 结束前等待对端响应。
func (c *Conn) Call(ctx context.Context, method string, params any, result any) error {
	select {
	case <-c.closed:
		return c.closedError()
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan *rpcResponse, 1)
	idKey := strconv.FormatInt(id, 10)
	c.pending[idKey] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, idKey)
		c.mu.Unlock()
	}()

	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := c.write(req); err != nil {
		return err
	}

	select {
	case resp := <-ch:
		return c.finish(resp, result)
	case <-c.closed:
		// 连接关闭与响应到达可能同时发生：优先取回已排入缓冲的响应。
		select {
		case resp := <-ch:
			return c.finish(resp, result)
		default:
			return c.closedError()
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// finish 将一次响应用于调用方的 result 指针。
func (c *Conn) finish(resp *rpcResponse, result any) error {
	if resp == nil {
		return c.closedError()
	}
	if resp.rpcErr != nil {
		return resp.rpcErr
	}
	if result != nil && len(resp.result) > 0 && string(resp.result) != "null" {
		if err := json.Unmarshal(resp.result, result); err != nil {
			return fmt.Errorf("copilot: failed to unmarshal result: %w", err)
		}
	}
	return nil
}

// Notify 发送单向通知消息（不包含 id）。
func (c *Conn) Notify(method string, params any) error {
	notif := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		notif["params"] = params
	}
	return c.write(notif)
}

// Respond 以原始 id 回写一次成功响应。
func (c *Conn) Respond(id json.RawMessage, result any) error {
	return c.writeResponse(id, result, nil)
}

// RespondError 以原始 id 回写一次错误响应。
func (c *Conn) RespondError(id json.RawMessage, code int, message string) error {
	return c.writeResponse(id, nil, &RPCError{Code: code, Message: message})
}

// Close 关闭连接并使所有等待中的 Call 立即返回。
func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.closeMu.Lock()
		c.localQuit = true
		c.closeMu.Unlock()

		close(c.closed)
		if closer, ok := c.r.(io.Closer); ok {
			_ = closer.Close()
		}
		if closer, ok := c.w.(io.Closer); ok {
			err = closer.Close()
		}

		c.mu.Lock()
		for _, ch := range c.pending {
			select {
			case ch <- nil:
			default:
			}
		}
		c.pending = make(map[string]chan *rpcResponse)
		c.mu.Unlock()
	})
	return err
}

// closedError 返回连接关闭的原因：异常关闭返回底层错误，正常关闭返回 ErrConnClosed。
func (c *Conn) closedError() error {
	c.closeMu.RLock()
	defer c.closeMu.RUnlock()
	if c.closeErr != nil {
		return c.closeErr
	}
	return ErrConnClosed
}

// write 以 Content-Length 分帧写出一条消息。
func (c *Conn) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("copilot: marshal error: %w", err)
	}

	c.wmu.Lock()
	defer c.wmu.Unlock()

	select {
	case <-c.closed:
		return ErrConnClosed
	default:
	}

	if _, err := io.WriteString(c.w, "Content-Length: "+strconv.Itoa(len(data))+"\r\n\r\n"); err != nil {
		return err
	}
	_, err = c.w.Write(data)
	return err
}

func (c *Conn) writeResponse(id json.RawMessage, result any, rpcErr *RPCError) error {
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	switch {
	case rpcErr != nil:
		resp["error"] = rpcErr
	case result != nil:
		resp["result"] = result
	default:
		resp["result"] = map[string]any{}
	}
	return c.write(resp)
}

func (c *Conn) handleClose(err error) {
	if errors.Is(err, io.EOF) {
		err = ErrConnClosed
	}
	c.closeMu.Lock()
	if !c.localQuit && c.closeErr == nil {
		c.closeErr = err
	}
	c.closeMu.Unlock()

	_ = c.Close()

	c.closeMu.RLock()
	h := c.onClose
	c.closeMu.RUnlock()
	if h != nil {
		h(err)
	}
}

// readLoop 在单一 goroutine 中按 Content-Length 分帧并派发消息。
func (c *Conn) readLoop() {
	reader := bufio.NewReader(c.r)
	for {
		body, err := readFrame(reader)
		if err != nil {
			c.handleClose(err)
			return
		}
		c.dispatch(body)
	}
}

// readFrame 读取一条 Content-Length 分帧的消息正文。
func readFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	headerBytes := 0
	for {
		line, err := r.ReadString('\n')
		headerBytes += len(line)
		if headerBytes > maxFrameSize {
			return nil, ErrFrameTooLarge
		}

		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			if err != nil && len(line) == 0 {
				return nil, err
			}
			break
		}
		if idx := strings.IndexByte(trimmed, ':'); idx > 0 {
			if strings.EqualFold(strings.TrimSpace(trimmed[:idx]), "content-length") {
				if n, parseErr := strconv.Atoi(strings.TrimSpace(trimmed[idx+1:])); parseErr == nil && n >= 0 {
					length = n
				}
			}
		}
		if err != nil {
			return nil, err
		}
	}

	if length < 0 {
		return nil, errors.New("copilot: Content-Length header missing or invalid")
	}
	if length > maxFrameSize {
		return nil, ErrFrameTooLarge
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// dispatch 解析一条消息并按请求 / 通知 / 响应分流。
func (c *Conn) dispatch(body []byte) {
	var in inboundMessage
	if err := json.Unmarshal(body, &in); err != nil {
		return
	}

	hasID := len(in.ID) > 0 && string(bytes.TrimSpace(in.ID)) != "null"

	// 1. 服务端发来的请求：带 method 与 id，响应必须回写原 id。
	if in.Method != "" && hasID {
		c.reqMu.RLock()
		handler := c.onReq
		c.reqMu.RUnlock()

		go func(id json.RawMessage, method string, params json.RawMessage) {
			if handler == nil {
				_ = c.writeResponse(id, nil, &RPCError{Code: -32601, Message: "Method not found"})
				return
			}
			res, err := handler(method, params)
			if err != nil {
				var rpcErr *RPCError
				if errors.As(err, &rpcErr) {
					_ = c.writeResponse(id, nil, rpcErr)
					return
				}
				_ = c.writeResponse(id, nil, &RPCError{Code: -32603, Message: err.Error()})
				return
			}
			_ = c.writeResponse(id, res, nil)
		}(in.ID, in.Method, in.Params)
		return
	}

	// 2. 服务端发来的通知：带 method 但无 id。同步按到达顺序分发；
	// 处理器内部不得直接调用 Conn.Call（会占住读循环），需要请求时另起 goroutine。
	if in.Method != "" && !hasID {
		c.notifMu.RLock()
		handler := c.onNotif
		c.notifMu.RUnlock()
		if handler != nil {
			handler(in.Method, in.Params)
		}
		return
	}

	// 3. 本端 Call 的响应：无 method 但有 id。
	if hasID {
		idKey := normalizeID(in.ID)
		c.mu.Lock()
		ch, ok := c.pending[idKey]
		c.mu.Unlock()
		if ok && ch != nil {
			select {
			case ch <- &rpcResponse{result: in.Result, rpcErr: in.Error}:
			default:
			}
		}
	}
}

func normalizeID(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	return strings.Trim(s, "\"")
}
