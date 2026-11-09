package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"mcode/internal/agent/llm"
)

const protocolVersion = "2025-06-18"

// Client 是单个 Streamable HTTP MCP 端点的客户端。
type Client struct {
	URL        string
	Token      string
	Headers    map[string]string
	HTTPClient *http.Client

	mu        sync.Mutex
	recoveryMu sync.Mutex
	sessionID string
	nextID    int64
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// ToolDescriptor 是 MCP tools/list 返回的工具描述。
type ToolDescriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations struct {
		ReadOnlyHint bool `json:"readOnlyHint"`
	} `json:"annotations"`
}

// Initialize 与 MCP 服务完成 initialize 握手。
func (c *Client) Initialize(ctx context.Context) error {
	params := map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "mcode", "version": "1"}}
	if _, err := c.request(ctx, "initialize", params); err != nil {
		return err
	}
	return c.notify(ctx, "notifications/initialized")
}

// ListTools 返回所有工具，自动按 nextCursor 翻页。
func (c *Client) ListTools(ctx context.Context) ([]ToolDescriptor, error) {
	var all []ToolDescriptor
	var cursor string
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.request(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools      []ToolDescriptor `json:"tools"`
			NextCursor string           `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("parse tools/list result: %w", err)
		}
		all = append(all, page.Tools...)
		if page.NextCursor == "" {
			return all, nil
		}
		cursor = page.NextCursor
	}
}

// CallTool 调用远端工具并返回原始 MCP 结果。
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	if args == nil {
		args = map[string]any{}
	}
	return c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
}

func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	items, err := readRPCResponses(resp)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID == nil || *item.ID != id {
			continue
		}
		if item.Error != nil {
			return nil, fmt.Errorf("%s failed: code %d, message %s", method, item.Error.Code, item.Error.Message)
		}
		if len(item.Result) == 0 {
			return nil, fmt.Errorf("%s: missing result", method)
		}
		return item.Result, nil
	}
	return nil, fmt.Errorf("%s: empty response", method)
}

func (c *Client) notify(ctx context.Context, method string) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method})
	if err != nil {
		return err
	}
	resp, err := c.post(ctx, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

func (c *Client) post(ctx context.Context, body []byte) (*http.Response, error) {
	return c.postAttempt(ctx, body, true)
}

type recoveringKey struct{}

func (c *Client) postAttempt(ctx context.Context, body []byte, retry bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", llm.UserAgent)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	for key, value := range c.Headers {
		req.Header.Set(key, value)
	}
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound && sid != "" && retry && ctx.Value(recoveringKey{}) == nil {
		resp.Body.Close()
		c.recoveryMu.Lock()
		c.mu.Lock()
		current := c.sessionID
		if current == sid {
			c.sessionID = ""
		}
		c.mu.Unlock()
		if current == sid {
			err = c.Initialize(context.WithValue(ctx, recoveringKey{}, true))
		}
		c.recoveryMu.Unlock()
		if err != nil {
			return nil, err
		}
		return c.postAttempt(ctx, body, false)
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		c.mu.Lock()
		c.sessionID = sid
		c.mu.Unlock()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if len(data) == 0 {
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return resp, nil
}

func readRPCResponses(resp *http.Response) ([]*rpcResponse, error) {
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return readSSE(resp.Body)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, nil
	}
	if body[0] == '[' {
		var out []*rpcResponse
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, err
		}
		return out, nil
	}
	var out rpcResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return []*rpcResponse{&out}, nil
}

func readSSE(body io.Reader) ([]*rpcResponse, error) {
	s := bufio.NewScanner(body)
	s.Buffer(make([]byte, 4096), 8<<20)
	var data strings.Builder
	var out []*rpcResponse
	consume := func() {
		if data.Len() == 0 {
			return
		}
		var item rpcResponse
		if json.Unmarshal([]byte(strings.TrimSpace(data.String())), &item) == nil {
			out = append(out, &item)
		}
		data.Reset()
	}
	for s.Scan() {
		line := strings.TrimSuffix(s.Text(), "\r")
		if line == "" {
			consume()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	consume()
	if err := s.Err(); err != nil {
		return out, err
	}
	return out, nil
}

const discoverTimeout = 10 * time.Second
const callTimeout = 2 * time.Minute
