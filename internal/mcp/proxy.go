package mcp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"mcode/internal/agentconf"
)

var (
	toolPolicyMu   sync.RWMutex
	toolPolicyPath string
)

// SetToolPolicyPath 显式指定 MCP 工具策略文件路径；
// 进程入口应在代理开始服务前调用，未指定时按 MCODE_DATA 环境变量或 ~/.mcode 解析。
func SetToolPolicyPath(path string) {
	toolPolicyMu.Lock()
	toolPolicyPath = path
	toolPolicyMu.Unlock()
}

// resolveToolPolicyPath 返回当前生效的 MCP 工具策略文件路径。
func resolveToolPolicyPath() string {
	toolPolicyMu.RLock()
	path := toolPolicyPath
	toolPolicyMu.RUnlock()
	if path != "" {
		return path
	}
	return agentconf.ResolveMCPToolPolicyPath()
}

// loadToolPolicy 读取工具策略。文件缺失或损坏时按空策略处理，不影响代理转发。
func loadToolPolicy() agentconf.MCPToolPolicy {
	path := resolveToolPolicyPath()
	if path == "" {
		return agentconf.MCPToolPolicy{}
	}
	policy, err := agentconf.LoadMCPToolPolicy(path)
	if err != nil {
		return agentconf.MCPToolPolicy{}
	}
	return policy
}

// filterDisabledTools 从 tools/list 结果中移除策略禁用的工具，保留其余字段与顺序。
// 解析失败时原样返回，避免代理层破坏上游响应。
func filterDisabledTools(result json.RawMessage, serverName string, policy agentconf.MCPToolPolicy) json.RawMessage {
	if serverName == "" || len(policy.Disabled[serverName]) == 0 {
		return result
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(result, &envelope); err != nil {
		return result
	}
	rawTools, ok := envelope["tools"]
	if !ok {
		return result
	}
	var tools []json.RawMessage
	if err := json.Unmarshal(rawTools, &tools); err != nil {
		return result
	}
	kept := make([]json.RawMessage, 0, len(tools))
	for _, raw := range tools {
		var meta struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil || !policy.IsDisabled(serverName, meta.Name) {
			kept = append(kept, raw)
		}
	}
	if len(kept) == len(tools) {
		return result
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return result
	}
	envelope["tools"] = encoded
	out, err := json.Marshal(envelope)
	if err != nil {
		return result
	}
	return out
}

// foldEntryToolName 是折叠后向模型暴露的唯一入口工具名。
const foldEntryToolName = "call_tool"

// foldEntryDescriptionLimit 是入口工具描述的长度上限（字符数），超出后直接截断。
const foldEntryDescriptionLimit = 2000

// foldState 记录某个上游服务的折叠状态与折叠前的完整工具清单。
// list 是按策略过滤后、折叠前的 tools/list 结果；names 是其中的工具名，
// 供调用时校验内部工具名与提示可用工具。
type foldState struct {
	folded bool
	list   json.RawMessage
	names  []string
}

// parseToolsList 取出 tools/list 结果里的工具数组与工具名；结构不符时 ok 为 false。
func parseToolsList(result json.RawMessage) (tools []json.RawMessage, names []string, ok bool) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(result, &envelope); err != nil {
		return nil, nil, false
	}
	rawTools, found := envelope["tools"]
	if !found {
		return nil, nil, false
	}
	if err := json.Unmarshal(rawTools, &tools); err != nil {
		return nil, nil, false
	}
	names = make([]string, 0, len(tools))
	for _, raw := range tools {
		var meta struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			return nil, nil, false
		}
		names = append(names, meta.Name)
	}
	return tools, names, true
}

// foldEntryDescription 生成入口工具的描述：说明参数用法，并在末尾附上全部工具名。
// 超过长度上限时按字符截断，避免单个工具描述过大。
func foldEntryDescription(count int, names []string) string {
	head := fmt.Sprintf("本服务共有 %d 个工具。参数 tool 填内部工具名，arguments 填该工具的参数对象；"+
		"省略 tool 时返回全部工具的名字、描述和 inputSchema。可用工具：", count)
	desc := head + strings.Join(names, ", ")
	if runes := []rune(desc); len(runes) > foldEntryDescriptionLimit {
		desc = string(runes[:foldEntryDescriptionLimit])
	}
	return desc
}

// foldToolsList 在工具数超过阈值时把 tools 数组折叠成只含一个 call_tool 工具。
// threshold 为 -1 表示关闭折叠；上游本身就有名为 call_tool 的工具时不折叠，避免把它藏起来。
// 返回值 bool 表示是否发生了折叠；解析失败或无需折叠时原样返回 result。
func foldToolsList(result json.RawMessage, threshold int) (json.RawMessage, bool) {
	if threshold < 0 {
		return result, false
	}
	tools, names, ok := parseToolsList(result)
	if !ok || len(tools) <= threshold {
		return result, false
	}
	for _, name := range names {
		if name == foldEntryToolName {
			return result, false
		}
	}
	entry, err := json.Marshal(map[string]any{
		"name":        foldEntryToolName,
		"description": foldEntryDescription(len(tools), names),
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tool":      map[string]any{"type": "string", "description": "内部工具名"},
				"arguments": map[string]any{"type": "object", "description": "该工具的参数对象，可省略"},
			},
		},
	})
	if err != nil {
		return result, false
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(result, &envelope); err != nil {
		return result, false
	}
	envelope["tools"] = json.RawMessage("[" + string(entry) + "]")
	out, err := json.Marshal(envelope)
	if err != nil {
		return result, false
	}
	return out, true
}

// setFoldState 记录某个服务的折叠状态。
func (p *Proxy) setFoldState(serverID string, state foldState) {
	p.foldMu.Lock()
	if p.fold == nil {
		p.fold = make(map[string]foldState)
	}
	p.fold[serverID] = state
	p.foldMu.Unlock()
}

// foldStateOf 读取某个服务已记录的折叠状态。
func (p *Proxy) foldStateOf(serverID string) (foldState, bool) {
	p.foldMu.RLock()
	state, ok := p.fold[serverID]
	p.foldMu.RUnlock()
	return state, ok
}

// applyFold 在已按策略过滤的 tools/list 结果上应用折叠，并把状态（含折叠前清单）记入 map。
// 返回最终要回给客户端的结果与记录下来的状态。
func (p *Proxy) applyFold(serverID, serverName string, filtered json.RawMessage, policy agentconf.MCPToolPolicy) (json.RawMessage, foldState) {
	_, names, _ := parseToolsList(filtered)
	state := foldState{list: filtered, names: names}
	threshold := policy.EffectiveFoldThreshold()
	// 免审批开关由 mcode 按 mcp__<server>__<tool> 工具名匹配（见 internal/remote/approvals.go），
	// 折叠后工具名统一变成 call_tool，免审批必然失效，因此配置了任意免审批工具的服务不折叠。
	if threshold < 0 || len(policy.AutoApprove[serverName]) > 0 {
		p.setFoldState(serverID, state)
		return filtered, state
	}
	folded, ok := foldToolsList(filtered, threshold)
	if !ok {
		p.setFoldState(serverID, state)
		return filtered, state
	}
	state.folded = true
	p.setFoldState(serverID, state)
	return folded, state
}

// refreshFoldState 通过 hub 重新拉一次 tools/list 并判定折叠状态，
// 供 tools/call 在 map 里还没有记录时使用（模型可能先调工具再列清单）。
func (p *Proxy) refreshFoldState(ctx context.Context, serverID, serverName string) foldState {
	result, rpcErr, err := p.hub.Forward(ctx, serverID, "tools/list", json.RawMessage("{}"))
	if err != nil || rpcErr != nil || len(result) == 0 {
		return foldState{}
	}
	policy := loadToolPolicy()
	filtered := filterDisabledTools(result, serverName, policy)
	_, state := p.applyFold(serverID, serverName, filtered, policy)
	return state
}

// foldCallPlan 描述一次 call_tool 入口调用该怎么处理。
type foldCallPlan struct {
	// params 与 tool 是改写后的 tools/call 参数与内部工具名，继续转发时使用。
	params json.RawMessage
	tool   string
	// result 非空表示直接返回该结果，不再转发上游。
	result map[string]any
}

// foldTextResult 构造 tools/call 结果：content 里放一段文本，可标记为错误。
func foldTextResult(text string, isError bool) map[string]any {
	res := map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	}
	if isError {
		res["isError"] = true
	}
	return res
}

// dispatchFoldCall 处理折叠入口 call_tool：省略 tool 时直接返回缓存的完整工具清单，
// 带 tool 时改写成内部工具调用。服务未折叠时返回空 plan，交由原有流程原样转发。
func (p *Proxy) dispatchFoldCall(ctx context.Context, serverID, serverName string, args json.RawMessage) foldCallPlan {
	state, known := p.foldStateOf(serverID)
	if !known {
		state = p.refreshFoldState(ctx, serverID, serverName)
	}
	if !state.folded {
		return foldCallPlan{}
	}
	var call struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &call); err != nil {
			return foldCallPlan{result: foldTextResult("参数必须是合法的 JSON 对象。", true)}
		}
	}
	tool := strings.TrimSpace(call.Tool)
	if tool == "" {
		return foldCallPlan{result: foldTextResult(string(state.list), false)}
	}
	found := false
	for _, name := range state.names {
		if name == tool {
			found = true
			break
		}
	}
	if !found {
		return foldCallPlan{result: foldTextResult(fmt.Sprintf("内部工具 %q 不存在。可用工具：%s", tool, strings.Join(state.names, ", ")), true)}
	}
	inner := call.Arguments
	if len(inner) == 0 || string(inner) == "null" {
		inner = json.RawMessage("{}")
	}
	params, err := json.Marshal(map[string]any{"name": tool, "arguments": inner})
	if err != nil {
		return foldCallPlan{result: foldTextResult("改写工具调用参数失败："+err.Error(), true)}
	}
	return foldCallPlan{params: params, tool: tool}
}

// proxyCapabilities 把上游 capabilities 收敛成代理对外声明的能力。
// tools 始终存在；resources 与 prompts 只按上游是否声明决定是否出现，且一律输出空对象：
// 代理不转发 list_changed 通知，也不转发 resources/subscribe，因此不能照抄上游的子字段。
// 其余能力（logging、completions、experimental 等）代理未透传，这里直接丢弃。
func proxyCapabilities(upstream json.RawMessage) map[string]any {
	caps := map[string]any{"tools": map[string]any{}}
	if len(upstream) == 0 {
		return caps
	}
	var declared map[string]json.RawMessage
	if err := json.Unmarshal(upstream, &declared); err != nil {
		return caps
	}
	for _, key := range []string{"resources", "prompts"} {
		if _, ok := declared[key]; ok {
			caps[key] = map[string]any{}
		}
	}
	return caps
}

// Proxy 将外部或本地 stdio/HTTP MCP 上游服务转换为统一的本地 HTTP 代理端点。
type Proxy struct {
	hub        *Hub
	tokenStr   string
	mu         sync.RWMutex
	port       int
	serverURL  string
	listener   net.Listener
	httpServer *http.Server
	closeOnce  sync.Once
	builtin    *Server

	// foldMu 保护 fold：记录每个上游服务是否已折叠，并缓存折叠前的完整工具清单。
	foldMu sync.RWMutex
	fold   map[string]foldState
}

// builtinPrefix 是按会话路由到内置 MCP 服务的路径前缀：/mcp/mcode/{thread_id}。
const builtinPrefix = "/mcp/mcode/"

// threadIDPattern 与会话 ID（UUID）的实际格式保持一致。
var threadIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// SetBuiltin 并发安全地设置 /mcp/mcode/{thread_id} 所转交的内置 MCP 服务。
func (p *Proxy) SetBuiltin(srv *Server) {
	p.mu.Lock()
	p.builtin = srv
	p.mu.Unlock()
}

// NewProxy 创建一个代理服务实例。
// Token 在构造时使用 crypto/rand 生成 32 字节 hex 字符串。
func NewProxy(hub *Hub) *Proxy {
	tokenBytes := make([]byte, 32)
	_, _ = rand.Read(tokenBytes)
	token := hex.EncodeToString(tokenBytes)

	return &Proxy{
		hub:      hub,
		tokenStr: token,
	}
}

// Start 监听 127.0.0.1:0 随机端口并启动 HTTP 代理服务。
// 重复调用会返回错误，避免覆盖已有监听器造成泄漏。
func (p *Proxy) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return fmt.Errorf("expected *net.TCPAddr, got %T", ln.Addr())
	}

	p.mu.Lock()
	if p.httpServer != nil {
		p.mu.Unlock()
		_ = ln.Close()
		return errors.New("proxy already started")
	}
	p.listener = ln
	p.port = tcpAddr.Port
	p.serverURL = fmt.Sprintf("http://127.0.0.1:%d", tcpAddr.Port)
	p.httpServer = &http.Server{
		Handler: p.Handler(),
	}
	p.mu.Unlock()

	go func() {
		_ = p.httpServer.Serve(ln)
	}()

	if ctx.Done() != nil {
		go func() {
			<-ctx.Done()
			_ = p.Close()
		}()
	}

	return nil
}

// Close 幂等地关闭代理 HTTP 服务与监听器。
func (p *Proxy) Close() error {
	var err error
	p.closeOnce.Do(func() {
		p.mu.Lock()
		server := p.httpServer
		listener := p.listener
		p.mu.Unlock()

		if server != nil {
			err = server.Close()
		} else if listener != nil {
			err = listener.Close()
		}
	})
	return err
}

// Handler 返回 HTTP 路由处理器，供直接测试或内部使用。
func (p *Proxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp/", p.handleMCP)
	return mux
}

// Token 返回用于 Bearer 身份校验的令牌。
func (p *Proxy) Token() string {
	return p.tokenStr
}

// URL 返回指定 serverID 对应的本地代理 HTTP 完整地址。
func (p *Proxy) URL(serverID string) string {
	p.mu.RLock()
	port := p.port
	p.mu.RUnlock()
	return fmt.Sprintf("http://127.0.0.1:%d/mcp/%s", port, url.PathEscape(serverID))
}

// BuiltinBase 返回内置 MCP 按会话路由的地址前缀，调用方再拼接会话 ID 即可。
// 未启动时监听端口为 0，此时返回空串，让调用方退回共享端点。
func (p *Proxy) BuiltinBase() string {
	p.mu.RLock()
	port := p.port
	p.mu.RUnlock()
	if port == 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, builtinPrefix)
}

// handleBuiltin 校验 /mcp/mcode/{thread_id} 并转交内置服务，threadID 作为请求作用域传入。
func (p *Proxy) handleBuiltin(w http.ResponseWriter, r *http.Request) {
	rawID := strings.TrimPrefix(r.URL.EscapedPath(), builtinPrefix)
	threadID, err := url.PathUnescape(rawID)
	if rawID == "" || strings.Contains(rawID, "/") || err != nil || !threadIDPattern.MatchString(threadID) {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	auth := r.Header.Get("Authorization")
	expected := "Bearer " + p.tokenStr
	if subtle.ConstantTimeCompare([]byte(auth), []byte(expected)) != 1 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	p.mu.RLock()
	srv := p.builtin
	p.mu.RUnlock()
	if srv == nil {
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}
	srv.ServeThread(w, r, threadID)
}

func (p *Proxy) handleMCP(w http.ResponseWriter, r *http.Request) {
	prefix := "/mcp/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(r.URL.EscapedPath(), builtinPrefix) {
		p.handleBuiltin(w, r)
		return
	}
	rawID := strings.TrimPrefix(r.URL.Path, prefix)
	if rawID == "" || strings.Contains(rawID, "/") {
		http.NotFound(w, r)
		return
	}
	serverID, err := url.PathUnescape(rawID)
	if err != nil || serverID == "" {
		http.NotFound(w, r)
		return
	}

	// 1. 非 POST 返回 405 Method Not Allowed
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 2. id 不存在返回 404 Not Found
	if _, ok := p.hub.Upstream(serverID); !ok {
		http.NotFound(w, r)
		return
	}

	// 3. Bearer 校验不对返回 401 Unauthorized
	auth := r.Header.Get("Authorization")
	expected := "Bearer " + p.tokenStr
	if subtle.ConstantTimeCompare([]byte(auth), []byte(expected)) != 1 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// 没有 id 的通知返回 202 Accepted
	if req.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		var initParams struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &initParams)
		}
		protoVer := initParams.ProtocolVersion
		if protoVer == "" {
			protoVer = "2025-06-18"
		}
		// 上游能力只做透传补充：取不到（含上游启动失败、不支持上报能力）时退回只含 tools，
		// 不让 initialize 因为上游不可用而失败。
		var upstreamCaps json.RawMessage
		capsCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		upstreamCaps, err = p.hub.UpstreamCapabilities(capsCtx, serverID)
		cancel()
		if err != nil {
			upstreamCaps = nil
		}
		res := map[string]any{
			"protocolVersion": protoVer,
			"capabilities":    proxyCapabilities(upstreamCaps),
			"serverInfo": map[string]any{
				"name":    "mcode-proxy",
				"version": "0.1.0",
			},
		}
		writeRPCResponse(w, req.ID, res, nil)
		return

	case "ping":
		writeRPCResponse(w, req.ID, map[string]any{}, nil)
		return

	default:
		upstreamName := ""
		if spec, ok := p.hub.Upstream(serverID); ok {
			upstreamName = spec.Name
		}
		// 折叠入口 call_tool 也要先改写成内部工具调用，禁用检查才能命中真正的工具名。
		var callParams struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if upstreamName != "" && req.Method == "tools/call" {
			if len(req.Params) > 0 {
				_ = json.Unmarshal(req.Params, &callParams)
			}
			if callParams.Name == foldEntryToolName {
				// 折叠入口的禁用检查必须命中内部工具名：dispatchFoldCall 用的工具清单
				// 已被 filterDisabledTools 过滤，被禁用的名字会先被当成「不存在」，
				// 所以这里先从折叠参数里取出 tool 做一次禁用判断。
				var foldArgs struct {
					Tool string `json:"tool"`
				}
				if len(callParams.Arguments) > 0 {
					_ = json.Unmarshal(callParams.Arguments, &foldArgs)
				}
				if innerTool := strings.TrimSpace(foldArgs.Tool); innerTool != "" && loadToolPolicy().IsDisabled(upstreamName, innerTool) {
					writeRPCError(w, req.ID, -32601, fmt.Sprintf("tool %q is disabled by mcode tool policy", innerTool))
					return
				}
				plan := p.dispatchFoldCall(r.Context(), serverID, upstreamName, callParams.Arguments)
				if plan.result != nil {
					writeRPCResponse(w, req.ID, plan.result, nil)
					return
				}
				if plan.tool != "" {
					callParams.Name = plan.tool
					req.Params = plan.params
				}
			}
			// 禁用的工具直接拒绝调用（列表已在 tools/list 中过滤）。
			if callParams.Name != "" && loadToolPolicy().IsDisabled(upstreamName, callParams.Name) {
				writeRPCError(w, req.ID, -32601, fmt.Sprintf("tool %q is disabled by mcode tool policy", callParams.Name))
				return
			}
		}

		result, rpcErr, err := p.hub.Forward(r.Context(), serverID, req.Method, req.Params)
		if err != nil {
			if errors.Is(err, ErrUpstreamUnauthorized) {
				writeRPCError(w, req.ID, -32001, "upstream unauthorized: re-authentication required")
				return
			}
			writeRPCError(w, req.ID, -32603, err.Error())
			return
		}
		if rpcErr != nil {
			writeRPCResponse(w, req.ID, nil, rpcErr)
			return
		}
		if upstreamName != "" && req.Method == "tools/list" {
			policy := loadToolPolicy()
			result = filterDisabledTools(result, upstreamName, policy)
			result, _ = p.applyFold(serverID, upstreamName, result, policy)
		}
		writeRPCResponse(w, req.ID, result, nil)
		return
	}
}
