package openai

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"mcode/internal/harness"
)

const (
	// mcpProtocolVersion 是请求头声明的 MCP 协议版本。
	mcpProtocolVersion = "2025-06-18"
	// mcpDiscoverTimeout 是单个端点 initialize 加 tools/list 的整体预算，超时仅跳过该端点。
	mcpDiscoverTimeout = 10 * time.Second
	// mcpCallTimeout 是单次 tools/call 的预算。
	mcpCallTimeout = 2 * time.Minute
	// mcpMaxToolNameLen 是 function tool 名称的最大长度。
	mcpMaxToolNameLen = 64
	// mcpBuiltinServerName 是内置 MCP 服务在工具名前缀里的名字。
	mcpBuiltinServerName = "mcode"
	// mcpReservedServerName 是仅用于审批的保留服务名，不作为工具来源。
	mcpReservedServerName = "mcode_approval"
)

// mcpUnsafeNameChars 匹配工具名中不允许出现的字符。
var mcpUnsafeNameChars = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// mcpToolEntry 记录暴露给模型的工具名与上游服务、原始工具名之间的映射。
type mcpToolEntry struct {
	CallName    string
	Server      string
	Tool        string
	Description string
	Parameters  map[string]any
	Endpoint    *mcpEndpoint
}

// mcpSession 保存本轮的 MCP 端点与工具列表；缓存范围严格限制在一轮内。
type mcpSession struct {
	tools  []mcpToolEntry
	byName map[string]*mcpToolEntry
}

// mcpEndpoint 是最小可用的 Streamable HTTP MCP 客户端，只实现本轮需要的三个方法。
type mcpEndpoint struct {
	name    string
	url     string
	token   string
	headers map[string]string
	client  *http.Client

	mu        sync.Mutex
	sessionID string
	nextID    int64
}

// mcpJSONRPCRequest 是 JSON-RPC 2.0 请求体；id 为 nil 表示通知。
type mcpJSONRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// mcpJSONRPCError 是 JSON-RPC 2.0 错误体。
type mcpJSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// mcpJSONRPCResponse 是 JSON-RPC 2.0 响应体。
type mcpJSONRPCResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *int64           `json:"id"`
	Result  json.RawMessage  `json:"result"`
	Error   *mcpJSONRPCError `json:"error"`
}

// mcpToolDescriptor 是 tools/list 返回的单个工具描述。
type mcpToolDescriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// mcpToolResult 是 tools/call 返回结果中本轮需要的字段。
type mcpToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// mcpSession 在本轮开始时发现所有 MCP 端点与其工具。
// 任何端点初始化或列举失败都只跳过该端点并记日志，不影响本轮对话。
func (h *Harness) mcpToolsForTurn(ctx context.Context, turn harness.Turn) *mcpSession {
	session := &mcpSession{byName: make(map[string]*mcpToolEntry)}
	if !h.opts.BuiltinMCP.Enabled() && h.opts.ExternalMCP == nil {
		return session
	}

	if url, token, workdirHeader := h.opts.BuiltinMCP.Endpoint(turn); url != "" {
		headers := make(map[string]string)
		if workdirHeader && turn.WorkDir != "" {
			headers["X-Mcode-Workdir"] = turn.WorkDir
		}
		session.addEndpoint(ctx, &mcpEndpoint{
			name:    mcpBuiltinServerName,
			url:     url,
			token:   token,
			headers: headers,
			client:  h.opts.HTTPClient,
		})
	}

	if h.opts.ExternalMCP != nil {
		for _, ep := range h.opts.ExternalMCP(turn.WorkDir, turn.ThreadID) {
			name := strings.TrimSpace(ep.Name)
			if name == "" || strings.TrimSpace(ep.URL) == "" {
				continue
			}
			// 内置服务名保留给内置端点，避免外部配置覆盖审批与内置工具。
			if name == mcpBuiltinServerName || name == mcpReservedServerName {
				continue
			}
			session.addEndpoint(ctx, &mcpEndpoint{
				name:    name,
				url:     strings.TrimSpace(ep.URL),
				token:   ep.Token,
				headers: make(map[string]string),
				client:  h.opts.HTTPClient,
			})
		}
	}

	return session
}

// addEndpoint 完成单个端点的 initialize 与 tools/list，并把工具登记进本轮工具表。
func (s *mcpSession) addEndpoint(ctx context.Context, ep *mcpEndpoint) {
	discoverCtx, cancel := context.WithTimeout(ctx, mcpDiscoverTimeout)
	defer cancel()

	if err := ep.initialize(discoverCtx); err != nil {
		log.Printf("openai: mcp %s: initialize failed: %v", ep.name, err)
		return
	}
	tools, err := ep.listTools(discoverCtx)
	if err != nil {
		log.Printf("openai: mcp %s: tools/list failed: %v", ep.name, err)
		return
	}

	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		entry := mcpToolEntry{
			Server:      ep.name,
			Tool:        name,
			Description: tool.Description,
			Parameters:  mcpParameters(tool.InputSchema),
			Endpoint:    ep,
		}
		entry.CallName = s.uniqueCallName(ep.name, name)
		s.tools = append(s.tools, entry)
		s.byName[entry.CallName] = &s.tools[len(s.tools)-1]
	}
}

// definitions 把本轮 MCP 工具转换成 OpenAI function tool 定义。
func (s *mcpSession) definitions() []openAIToolDefinition {
	if s == nil || len(s.tools) == 0 {
		return nil
	}
	defs := make([]openAIToolDefinition, 0, len(s.tools))
	for _, tool := range s.tools {
		defs = append(defs, openAIToolDefinition{
			Type: "function",
			Function: openAIFunctionDef{
				Name:        tool.CallName,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			},
		})
	}
	return defs
}

// toolsForRequest 组合内置文件工具与本轮发现的 MCP 工具。
func toolsForRequest(mcpTools *mcpSession) []openAIToolDefinition {
	tools := make([]openAIToolDefinition, 0, len(defaultTools)+8)
	tools = append(tools, defaultTools...)
	tools = append(tools, mcpTools.definitions()...)
	return tools
}

// callTool 按暴露名转发到上游端点，返回拼接后的文本输出与 isError。
func (s *mcpSession) callTool(ctx context.Context, callName string, args map[string]any) (string, bool, error) {
	if s == nil {
		return "", true, fmt.Errorf("unknown tool %s", callName)
	}
	entry, ok := s.byName[callName]
	if !ok {
		return "", true, fmt.Errorf("unknown tool %s", callName)
	}
	callCtx, cancel := context.WithTimeout(ctx, mcpCallTimeout)
	defer cancel()
	return entry.Endpoint.callTool(callCtx, entry.Tool, args)
}

// uniqueCallName 生成一个不与本轮已有工具冲突的暴露名。
func (s *mcpSession) uniqueCallName(server, tool string) string {
	base := mcpCallName(server, tool)
	name := base
	for i := 2; i < 100; i++ {
		if _, exists := s.byName[name]; !exists {
			return name
		}
		suffix := fmt.Sprintf("_%d", i)
		name = truncateToolName(base, mcpMaxToolNameLen-len(suffix)) + suffix
	}
	sum := sha1.Sum([]byte(server + "__" + tool))
	return truncateToolName(base, mcpMaxToolNameLen-9) + "_" + hex.EncodeToString(sum[:4])
}

// mcpCallName 拼出 mcp__<server>__<tool> 形式的工具名，并保证长度与字符集合法。
func mcpCallName(server, tool string) string {
	raw := "mcp__" + server + "__" + tool
	safe := mcpUnsafeNameChars.ReplaceAllString(raw, "_")
	if len(safe) <= mcpMaxToolNameLen {
		return safe
	}
	sum := sha1.Sum([]byte(raw))
	suffix := hex.EncodeToString(sum[:4])
	return truncateToolName(safe, mcpMaxToolNameLen-len(suffix)-1) + "_" + suffix
}

// truncateToolName 把工具名截断到 limit 以内，且不留下尾部下划线。
func truncateToolName(name string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(name) <= limit {
		return name
	}
	return strings.TrimRight(name[:limit], "_")
}

// mcpParameters 把工具 inputSchema 转成请求体里的 parameters 对象。
func mcpParameters(schema json.RawMessage) map[string]any {
	params := make(map[string]any)
	if len(schema) > 0 {
		if err := json.Unmarshal(schema, &params); err == nil && params != nil {
			return params
		}
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

// initialize 完成 MCP 握手：initialize 请求加 notifications/initialized 通知。
func (e *mcpEndpoint) initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "mcode",
			"version": "1.0",
		},
	}
	if _, err := e.postRPC(ctx, "initialize", params); err != nil {
		return err
	}
	// 通知失败不影响后续调用，仅记录日志。
	if err := e.postNotification(ctx, "notifications/initialized", nil); err != nil {
		log.Printf("openai: mcp %s: notifications/initialized failed: %v", e.name, err)
	}
	return nil
}

// listTools 返回端点声明的工具列表。
func (e *mcpEndpoint) listTools(ctx context.Context) ([]mcpToolDescriptor, error) {
	res, err := e.postRPC(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []mcpToolDescriptor `json:"tools"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("parse tools/list result: %w", err)
	}
	return out.Tools, nil
}

// callTool 调用上游工具，把 content 里的 text 拼成输出。
func (e *mcpEndpoint) callTool(ctx context.Context, name string, args map[string]any) (string, bool, error) {
	if args == nil {
		args = map[string]any{}
	}
	raw, err := e.postRPC(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return "", true, err
	}

	var result mcpToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		// 结构不符时直接回传原始结果，避免丢掉服务端的实际答复。
		return string(raw), false, nil
	}

	var texts []string
	for _, item := range result.Content {
		if item.Text != "" {
			texts = append(texts, item.Text)
		}
	}
	if len(texts) == 0 {
		return string(raw), result.IsError, nil
	}
	return strings.Join(texts, "\n"), result.IsError, nil
}

// postRPC 发送带 id 的 JSON-RPC 请求并等待同 id 的响应。
func (e *mcpEndpoint) postRPC(ctx context.Context, method string, params any) (json.RawMessage, error) {
	e.mu.Lock()
	e.nextID++
	id := e.nextID
	sessionID := e.sessionID
	e.mu.Unlock()

	body, err := json.Marshal(mcpJSONRPCRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("marshal %s request: %w", method, err)
	}

	resp, err := e.do(ctx, body, sessionID)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	responses, err := e.readResponses(resp)
	if err != nil {
		return nil, err
	}

	var first *mcpJSONRPCResponse
	for _, r := range responses {
		if first == nil {
			first = r
		}
		if r.ID != nil && *r.ID == id {
			first = r
			break
		}
	}
	if first == nil {
		return nil, fmt.Errorf("%s: empty response", method)
	}
	if first.Error != nil {
		return nil, fmt.Errorf("%s failed: code %d, message %s", method, first.Error.Code, first.Error.Message)
	}
	if len(first.Result) == 0 {
		return nil, fmt.Errorf("%s: missing result", method)
	}
	return first.Result, nil
}

// postNotification 发送不带 id 的 JSON-RPC 通知，不等待业务响应。
func (e *mcpEndpoint) postNotification(ctx context.Context, method string, params any) error {
	e.mu.Lock()
	sessionID := e.sessionID
	e.mu.Unlock()

	body, err := json.Marshal(mcpJSONRPCRequest{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("marshal %s notification: %w", method, err)
	}

	resp, err := e.do(ctx, body, sessionID)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

// do 发出 Streamable HTTP POST 请求，并记录服务端下发的 Mcp-Session-Id。
func (e *mcpEndpoint) do(ctx context.Context, body []byte, sessionID string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	for k, v := range e.headers {
		req.Header.Set(k, v)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	client := e.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		e.mu.Lock()
		e.sessionID = sid
		e.mu.Unlock()
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		msg := strings.TrimSpace(string(errBody))
		if msg == "" {
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	return resp, nil
}

// readResponses 解析响应体，兼容 application/json 与 text/event-stream 两种返回形式。
func (e *mcpEndpoint) readResponses(resp *http.Response) ([]*mcpJSONRPCResponse, error) {
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/event-stream") {
		return e.readSSEResponses(resp.Body)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, nil
	}

	if trimmed[0] == '[' {
		var list []*mcpJSONRPCResponse
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return nil, fmt.Errorf("parse response: %w", err)
		}
		return list, nil
	}

	var single mcpJSONRPCResponse
	if err := json.Unmarshal(trimmed, &single); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return []*mcpJSONRPCResponse{&single}, nil
}

// readSSEResponses 从 SSE 流中挑出 JSON-RPC 响应事件。
func (e *mcpEndpoint) readSSEResponses(body io.Reader) ([]*mcpJSONRPCResponse, error) {
	reader := newSSEReader(body)
	var out []*mcpJSONRPCResponse
	for {
		event, err := reader.readEvent()
		if err != nil {
			if err == io.EOF {
				return out, nil
			}
			return out, err
		}
		data := strings.TrimSpace(event.Data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var item mcpJSONRPCResponse
		if err := json.Unmarshal([]byte(data), &item); err != nil {
			continue
		}
		out = append(out, &item)
	}
}
