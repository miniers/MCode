package acp

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
	"sync/atomic"
)

// ErrConnClosed 表示连接已被关闭。
var ErrConnClosed = errors.New("acp: connection closed")

// ErrFrameTooLarge 表示单帧超过 16MB 上限：
// 超过上限的消息会以该错误终止连接，避免异常 Agent 撑爆内存。
var ErrFrameTooLarge = errors.New("acp: agent sent a message over 16 MB")

// maxFrameSize 是单条 JSON-RPC 消息（含 Content-Length 头部）的大小上限。
const maxFrameSize = 16 * 1024 * 1024

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
	return fmt.Sprintf("rpc error: code %d, message: %s", e.Code, e.Message)
}

// RequestHandler 处理对端发来的 RPC 请求，返回响应结果或错误。
type RequestHandler func(method string, params json.RawMessage) (result any, err error)

// responseWithAck 可由 RequestHandler 的返回值可选实现，在响应真正写入底层连接后接收写入结果。
type responseWithAck interface {
	ResponsePayload() any
	Ack(err error)
}

// NotificationHandler 处理对端发来的 RPC 通知。
type NotificationHandler func(method string, params json.RawMessage)

type pendingCall struct {
	ch   chan *rpcResponse
	done chan struct{}
}

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

/*
分帧规则说明：
写端在 .lines 模式下发送格式为 body + "\n"。
因此本实现每条 JSON-RPC 消息末尾写入 '\n'。
读端必须同时支持 Content-Length: N\r\n\r\n<body> 头部分帧与换行分隔 JSON，
通过跳过前置空白字符后检查首字节自适应区分（'C'/'c' 判定为 Content-Length 头，否则作为换行分隔 JSON 逐行解码）。
*/

// Conn 是基于 io.Reader 与 io.Writer 的双向 JSON-RPC 2.0 连接。
type Conn struct {
	r io.Reader
	w io.Writer

	wMu sync.Mutex

	callsMu      sync.Mutex
	pendingCalls map[string]*pendingCall
	nextID       atomic.Int64

	onReq    RequestHandler
	onReqMu  sync.RWMutex
	onNotif  NotificationHandler
	onNotfMu sync.RWMutex

	closed     chan struct{}
	closeOnce  sync.Once
	localClose atomic.Bool
	closeErr   error
	closeErrMu sync.RWMutex
}

// NewConn 创建并启动一个 JSON-RPC 2.0 连接。
func NewConn(r io.Reader, w io.Writer) *Conn {
	c := &Conn{
		r:            r,
		w:            w,
		pendingCalls: make(map[string]*pendingCall),
		closed:       make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// SetRequestHandler 注册对端请求的回调处理函数。
func (c *Conn) SetRequestHandler(h RequestHandler) {
	c.onReqMu.Lock()
	defer c.onReqMu.Unlock()
	c.onReq = h
}

// SetNotificationHandler 注册对端通知的回调处理函数。
func (c *Conn) SetNotificationHandler(h NotificationHandler) {
	c.onNotfMu.Lock()
	defer c.onNotfMu.Unlock()
	c.onNotif = h
}

// Call 发送带有唯一递增 ID 的 JSON-RPC 请求，并在 ctx 结束前等待对端响应。
func (c *Conn) Call(ctx context.Context, method string, params any, result any) error {
	select {
	case <-c.closed:
		return c.closedError()
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	id := c.nextID.Add(1)
	idKey := strconv.FormatInt(id, 10)

	call := &pendingCall{
		ch:   make(chan *rpcResponse, 1),
		done: make(chan struct{}),
	}

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

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}

	if err := c.writeMessage(req); err != nil {
		return err
	}

	select {
	case resp := <-call.ch:
		return c.finishCall(resp, result)
	case <-c.closed:
		// 连接关闭与响应到达可能同时发生（例如 Agent 回复后立即退出）。
		// 已排入缓冲的响应不会被丢弃：优先取回它，仅在确实没有响应时返回关闭原因。
		select {
		case resp := <-call.ch:
			return c.finishCall(resp, result)
		default:
			return c.closedError()
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// finishCall 将一次响应用于调用方的 result 指针；nil 响应表示连接已断开。
func (c *Conn) finishCall(resp *rpcResponse, result any) error {
	if resp == nil {
		return c.closedError()
	}
	if resp.rpcErr != nil {
		return resp.rpcErr
	}
	if result != nil && len(resp.result) > 0 && string(resp.result) != "null" {
		if err := json.Unmarshal(resp.result, result); err != nil {
			return fmt.Errorf("acp: failed to unmarshal result: %w", err)
		}
	}
	return nil
}

// closedError 返回连接关闭的原因：异常关闭（如帧超限、对端退出）返回底层错误，
// 正常关闭返回 ErrConnClosed。
func (c *Conn) closedError() error {
	c.closeErrMu.RLock()
	defer c.closeErrMu.RUnlock()
	if c.closeErr != nil {
		return c.closeErr
	}
	return ErrConnClosed
}

// Notify 发送单向通知消息（不包含 id）。
func (c *Conn) Notify(method string, params any) error {
	select {
	case <-c.closed:
		return c.closedError()
	default:
	}

	notif := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		notif["params"] = params
	}

	return c.writeMessage(notif)
}

// Close 关闭连接并使所有等待中的 Call 立即返回 ErrConnClosed。
func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		// 先标记本地主动关闭：由本次关闭引发的读错误（ErrClosedPipe 等）不应被记为异常原因。
		c.localClose.Store(true)
		close(c.closed)
		if closer, ok := c.r.(io.Closer); ok {
			_ = closer.Close()
		}
		if closer, ok := c.w.(io.Closer); ok {
			err = closer.Close()
		}

		c.callsMu.Lock()
		for _, call := range c.pendingCalls {
			close(call.done)
			// 非阻塞投递：若响应已排在缓冲里则不覆盖、不阻塞；
			// 等待方会优先读取已到达的响应，否则读取 closed 分支。
			select {
			case call.ch <- nil:
			default:
			}
		}
		c.pendingCalls = make(map[string]*pendingCall)
		c.callsMu.Unlock()
	})
	return err
}

func (c *Conn) writeMessage(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("acp: marshal error: %w", err)
	}

	// 以换行符分隔帧
	data = append(data, '\n')

	c.wMu.Lock()
	defer c.wMu.Unlock()

	select {
	case <-c.closed:
		return ErrConnClosed
	default:
	}

	_, err = c.w.Write(data)
	return err
}

func (c *Conn) writeResponse(id json.RawMessage, result any, rpcErr *RPCError) error {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
	}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else if result != nil {
		resp["result"] = result
	} else {
		resp["result"] = map[string]any{}
	}
	return c.writeMessage(resp)
}

func (c *Conn) handleClose(err error) {
	// 对端正常结束流等价于普通连接关闭；仅在异常或本地未主动关闭时才记录底层原因。
	if errors.Is(err, io.EOF) {
		err = ErrConnClosed
	}
	if !c.localClose.Load() {
		c.closeErrMu.Lock()
		if c.closeErr == nil {
			c.closeErr = err
		}
		c.closeErrMu.Unlock()
	}
	_ = c.Close()
}

// readLoop 运行在单一 goroutine 中，负责从底层 Reader 解码帧并派发消息。
func (c *Conn) readLoop() {
	reader := bufio.NewReader(c.r)
	for {
		firstByte, err := peekNonWhitespace(reader)
		if err != nil {
			c.handleClose(err)
			return
		}

		if firstByte == 'C' || firstByte == 'c' {
			// Content-Length 头部分帧
			body, err := readContentLengthPayload(reader)
			if err != nil {
				c.handleClose(err)
				return
			}
			c.dispatch(body)
		} else {
			// 换行分隔 JSON
			line, err := readLineFrame(reader)
			if err != nil && len(line) == 0 {
				c.handleClose(err)
				return
			}
			line = bytes.TrimSpace(line)
			if len(line) > 0 {
				c.dispatch(line)
			}
		}
	}
}

// readLineFrame 读取一条换行分隔帧。读取按 bufio 缓冲区逐段累积，
// 超过 maxFrameSize 立即报错，避免超长行无限占用内存。
func readLineFrame(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > maxFrameSize {
			return nil, ErrFrameTooLarge
		}
		if err == nil {
			return buf, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return buf, err
	}
}

func peekNonWhitespace(r *bufio.Reader) (byte, error) {
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			if err := r.UnreadByte(); err != nil {
				return 0, err
			}
			return b, nil
		}
	}
}

func readContentLengthPayload(r *bufio.Reader) ([]byte, error) {
	contentLength := -1
	headerBytes := 0
	for {
		line, err := readLineFrame(r)
		if err != nil {
			return nil, err
		}
		headerBytes += len(line)
		if headerBytes > maxFrameSize {
			return nil, ErrFrameTooLarge
		}
		trimmed := strings.TrimRight(string(line), "\r\n")
		if trimmed == "" {
			// 头部结束
			break
		}
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) == 2 && strings.EqualFold(strings.TrimSpace(parts[0]), "content-length") {
			length, parseErr := strconv.Atoi(strings.TrimSpace(parts[1]))
			if parseErr == nil && length >= 0 {
				contentLength = length
			}
		}
	}

	if contentLength < 0 {
		return nil, errors.New("acp: Content-Length header missing or invalid")
	}
	if contentLength > maxFrameSize {
		return nil, ErrFrameTooLarge
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

func (c *Conn) dispatch(msgBytes []byte) {
	var in inboundMessage
	if err := json.Unmarshal(msgBytes, &in); err != nil {
		return
	}

	// 1. 判断是否带有 id
	hasID := len(in.ID) > 0 && string(in.ID) != "null"

	// 2. 若带有 Method 且带有 ID，则是对端发送过来的请求
	if in.Method != "" && hasID {
		c.onReqMu.RLock()
		handler := c.onReq
		c.onReqMu.RUnlock()

		go func(id json.RawMessage, method string, params json.RawMessage) {
			if handler == nil {
				_ = c.writeResponse(id, nil, &RPCError{Code: -32601, Message: "Method not found"})
				return
			}
			res, err := handler(method, params)
			if err != nil {
				_ = c.writeResponse(id, nil, &RPCError{Code: -32603, Message: err.Error()})
				return
			}
			if rwa, ok := res.(responseWithAck); ok {
				writeErr := c.writeResponse(id, rwa.ResponsePayload(), nil)
				rwa.Ack(writeErr)
				return
			}
			_ = c.writeResponse(id, res, nil)
		}(in.ID, in.Method, in.Params)
		return
	}

	// 3. 若带有 Method 但无 ID，则是对端发送过来的通知
	if in.Method != "" && !hasID {
		c.onNotfMu.RLock()
		handler := c.onNotif
		c.onNotfMu.RUnlock()

		if handler != nil {
			// 必须按到达顺序同步分发，回调不得在内部调用 Conn.Call（会阻塞读循环导致死锁）。
			handler(in.Method, in.Params)
		}
		return
	}

	// 4. 若无 Method 但有 ID，则是本端 Call 的响应
	if hasID {
		idKey := normalizeID(in.ID)
		c.callsMu.Lock()
		call, ok := c.pendingCalls[idKey]
		c.callsMu.Unlock()

		if ok && call != nil {
			call.ch <- &rpcResponse{
				result: in.Result,
				rpcErr: in.Error,
			}
		}
	}
}

func normalizeID(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	s = strings.Trim(s, "\"")
	return s
}
