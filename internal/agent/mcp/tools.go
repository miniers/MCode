package mcp

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"mcode/internal/agent/tools"
)

// Server 描述一个 MCP 服务端点。
type Server struct {
	Name, URL, Token string
	Headers          map[string]string
	Builtin          bool
}

type loadedServer struct {
	server Server
	client *Client
	tools  []ToolDescriptor
	err    error
}
type callArgs struct {
	Server    string         `json:"server"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}
type listArgs struct {
	Server string `json:"server"`
	Cursor string `json:"cursor"`
}
type toolInfoArgs struct {
	Tool string `json:"tool"`
}

// 发现缓存控制重复握手；测试可调小这两个变量。
var (
	discoveryTTL = 5 * time.Minute
	failureTTL   = 30 * time.Second

	discoveryMu    sync.Mutex
	discoveryCache = map[string]discoveryEntry{}
)

type discoveryEntry struct {
	loaded  *loadedServer
	fetched time.Time
}

// discoveryKey 由 URL、Token 与排序后的 Headers 生成缓存键，保证 map 顺序不影响命中。
func discoveryKey(server Server) string {
	keys := make([]string, 0, len(server.Headers))
	for key := range server.Headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	builder.WriteString(server.URL)
	builder.WriteByte('\n')
	builder.WriteString(server.Token)
	for _, key := range keys {
		builder.WriteByte('\n')
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(server.Headers[key])
	}
	return builder.String()
}

// resetDiscoveryCache 仅供测试清空包级缓存。
func resetDiscoveryCache() {
	discoveryMu.Lock()
	discoveryCache = map[string]discoveryEntry{}
	discoveryMu.Unlock()
}

// discoverServer 读取缓存或执行一次发现；失败结果按更短的 TTL 缓存。
func discoverServer(ctx context.Context, server Server) *loadedServer {
	key := discoveryKey(server)
	now := time.Now()
	discoveryMu.Lock()
	entry, ok := discoveryCache[key]
	discoveryMu.Unlock()
	if ok && entry.loaded != nil {
		ttl := discoveryTTL
		if entry.loaded.err != nil {
			ttl = failureTTL
		}
		if now.Sub(entry.fetched) < ttl {
			// 浅拷贝后替换服务描述，同一端点的不同别名共享发现结果。
			cached := *entry.loaded
			cached.server = server
			return &cached
		}
	}
	loaded := &loadedServer{server: server, client: &Client{URL: server.URL, Token: server.Token, Headers: server.Headers}}
	discoverCtx, cancel := context.WithTimeout(ctx, discoverTimeout)
	defer cancel()
	err := loaded.client.Initialize(discoverCtx)
	if err == nil {
		loaded.tools, err = loaded.client.ListTools(discoverCtx)
	}
	loaded.err = err
	discoveryMu.Lock()
	now = time.Now()
	pruneDiscoveryCacheLocked(now)
	discoveryCache[key] = discoveryEntry{loaded: loaded, fetched: now}
	discoveryMu.Unlock()
	return loaded
}

// pruneDiscoveryCacheLocked 删除已过期的发现缓存条目，避免键长期累积。
// 调用方必须持有 discoveryMu。
func pruneDiscoveryCacheLocked(now time.Time) {
	for key, entry := range discoveryCache {
		ttl := discoveryTTL
		if entry.loaded != nil && entry.loaded.err != nil {
			ttl = failureTTL
		}
		if now.Sub(entry.fetched) >= ttl {
			delete(discoveryCache, key)
		}
	}
}

// invalidateClient 在会话失效时删除对应缓存，下一轮重新发现。
func invalidateClient(client *Client) {
	discoveryMu.Lock()
	defer discoveryMu.Unlock()
	for key, entry := range discoveryCache {
		if entry.loaded != nil && entry.loaded.client == client {
			delete(discoveryCache, key)
		}
	}
}

// sessionExpired 判断错误是否表示 MCP 会话已失效。
func sessionExpired(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "http 404") || strings.Contains(message, "session")
}

func discoverSafely(ctx context.Context, server Server) (loaded *loadedServer) {
	defer func() {
		if v := recover(); v != nil {
			loaded = &loadedServer{server: server, err: fmt.Errorf("MCP 发现 panic: %v", v)}
		}
	}()
	return discoverServer(ctx, server)
}

// Tools 发现并转换 MCP 工具。内置服务工具直接暴露，外部服务只暴露 list/call 网关。
func Tools(ctx context.Context, servers []Server) []tools.Tool {
	loadedServers := make([]*loadedServer, len(servers))
	var wg sync.WaitGroup
	for index, server := range servers {
		if strings.TrimSpace(server.Name) == "" || strings.TrimSpace(server.URL) == "" {
			continue
		}
		wg.Add(1)
		go func(index int, server Server) {
			defer wg.Done()
			loadedServers[index] = discoverSafely(ctx, server)
		}(index, server)
	}
	wg.Wait()
	var builtins []tools.Tool
	var external []*loadedServer
	for _, loaded := range loadedServers {
		if loaded == nil || loaded.err != nil {
			continue
		}
		server := loaded.server
		if server.Builtin {
			for _, descriptor := range loaded.tools {
				name := strings.TrimSpace(descriptor.Name)
				if name == "" {
					continue
				}
				schema, invalid := validatedSchema(descriptor.InputSchema)
				description := descriptor.Description
				if invalid { description += " 原始参数结构无效，已忽略。" }
				builtins = append(builtins, makeTool(
					safeName("mcp__"+server.Name+"__"+name),
					description,
					schema,
					descriptor.Annotations.ReadOnlyHint,
					builtinToolHandler(loaded.client, name),
				))
			}
			continue
		}
		external = append(external, loaded)
	}
	if len(external) == 0 {
		return builtins
	}
	return append(builtins, externalListTool(external), externalCallTool(external))
}

func builtinToolHandler(client *Client, name string) func(context.Context, tools.Env, json.RawMessage) (tools.Result, error) {
	return func(ctx context.Context, _ tools.Env, args json.RawMessage) (tools.Result, error) {
		return invoke(client, ctx, name, args)
	}
}

func externalListTool(external []*loadedServer) tools.Tool {
	return makeTool(
		"mcode_mcp_list",
		"List external MCP servers and tools. Provide server and tool to get a tool's full input schema.",
		json.RawMessage(`{
  "type": "object",
  "properties": {
    "server": {"type": "string", "description": "External MCP server name to filter by."},
    "cursor": {"type": "string", "description": "Pagination cursor returned by the previous page."},
    "tool": {"type": "string", "description": "Tool name; provide with server to get its full input schema."}
  }
}`),
		true,
		externalListHandler(external),
	)
}

// externalListHandler 处理外部服务列表和工具 schema 查询。
func externalListHandler(external []*loadedServer) func(context.Context, tools.Env, json.RawMessage) (tools.Result, error) {
	return func(_ context.Context, _ tools.Env, raw json.RawMessage) (tools.Result, error) {
		var args listArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return tools.Result{Text: err.Error(), IsError: true}, nil
		}
		var extra toolInfoArgs
		_ = json.Unmarshal(raw, &extra)
		if extra.Tool != "" {
			return findExternalToolInfo(external, args.Server, extra.Tool)
		}
		if args.Server == "" {
			names := make([]string, 0, len(external))
			for _, server := range external {
				names = append(names, server.server.Name)
			}
			return tools.Result{Text: "External MCP servers: " + strings.Join(names, ", ")}, nil
		}
		selected := findExternalServer(external, args.Server)
		if selected == nil {
			return tools.Result{Text: fmt.Sprintf("Error: MCP server %q not found.", args.Server), IsError: true}, nil
		}
		return listServerTools(selected, args.Cursor), nil
	}
}

// findExternalToolInfo 按服务名和工具名返回工具的完整 schema。
func findExternalToolInfo(external []*loadedServer, serverName, toolName string) (tools.Result, error) {
	for _, server := range external {
		if serverName != "" && serverName != server.server.Name {
			continue
		}
		for _, descriptor := range server.tools {
			if descriptor.Name == toolName {
				return tools.Result{Text: toolInfo(server.server.Name, descriptor)}, nil
			}
		}
	}
	return tools.Result{Text: fmt.Sprintf("Error: Tool %q not found.", toolName), IsError: true}, nil
}

func findExternalServer(external []*loadedServer, name string) *loadedServer {
	for _, server := range external {
		if server.server.Name == name {
			return server
		}
	}
	return nil
}

// listServerTools 按游标分页生成单个 MCP 服务的工具清单。
func listServerTools(server *loadedServer, cursor string) tools.Result {
	offset := 0
	if cursor != "" {
		if _, err := fmt.Sscanf(cursor, "%d", &offset); err != nil || offset < 0 {
			return tools.Result{Text: "Error: invalid cursor.", IsError: true}
		}
	}
	const pageSize = 20
	end := offset + pageSize
	if end > len(server.tools) {
		end = len(server.tools)
	}
	if offset > len(server.tools) {
		offset = len(server.tools)
	}
	var lines []string
	for _, descriptor := range server.tools[offset:end] {
		description := strings.Join(strings.Fields(descriptor.Description), " ")
		if description == "" {
			description = descriptor.Name
		}
		if len(description) > 160 {
			description = description[:160] + "…"
		}
		lines = append(lines, fmt.Sprintf("%s: %s", descriptor.Name, description))
	}
	if end < len(server.tools) {
		lines = append(lines, "Next cursor: "+fmt.Sprint(end))
	}
	if len(lines) == 0 {
		lines = append(lines, "No MCP tools found.")
	}
	return tools.Result{Text: strings.Join(lines, "\n")}
}

func externalCallTool(external []*loadedServer) tools.Tool {
	return makeTool(
		"mcode_mcp_call",
		"Call a tool on an external MCP server.",
		json.RawMessage(`{
  "type": "object",
  "properties": {
    "server": {"type": "string", "description": "Name of the target MCP server."},
    "tool": {"type": "string", "description": "Name of the tool to call."},
    "arguments": {"type": "object", "description": "Arguments to pass to the target tool."}
  },
  "required": ["server", "tool"]
}`),
		false,
		externalCallHandler(external),
	)
}

// externalCallHandler 校验目标服务和工具后转发调用。
func externalCallHandler(external []*loadedServer) func(context.Context, tools.Env, json.RawMessage) (tools.Result, error) {
	return func(callCtx context.Context, _ tools.Env, raw json.RawMessage) (tools.Result, error) {
		var args callArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return tools.Result{Text: err.Error(), IsError: true}, nil
		}
		if args.Server == "" || args.Tool == "" {
			return tools.Result{Text: "Error: both server and tool are required.", IsError: true}, nil
		}
		server := findExternalServer(external, args.Server)
		if server == nil {
			return tools.Result{Text: fmt.Sprintf("Error: MCP server %q not found or not connected.", args.Server), IsError: true}, nil
		}
		for _, descriptor := range server.tools {
			if descriptor.Name == args.Tool {
				return invoke(server.client, callCtx, args.Tool, mustJSON(args.Arguments))
			}
		}
		return tools.Result{Text: fmt.Sprintf("Error: Tool %q not found in server %q.", args.Tool, args.Server), IsError: true}, nil
	}
}

func makeTool(name, description string, params json.RawMessage, readOnly bool, run func(context.Context, tools.Env, json.RawMessage) (tools.Result, error)) tools.Tool {
	return tools.Tool{Name: name, Description: description, Parameters: params, ReadOnly: readOnly, Run: run}
}

// schemaOrObject 在远端 schema 无效时返回空对象 schema。
func schemaOrObject(schema json.RawMessage) json.RawMessage {
	result, _ := validatedSchema(schema)
	return result
}

func validatedSchema(schema json.RawMessage) (json.RawMessage, bool) {
	var value map[string]any
	if json.Unmarshal(schema, &value) == nil && value != nil {
		typ, hasType := value["type"]
		props, hasProps := value["properties"]
		_, propsOK := props.(map[string]any)
		if (!hasType || typ == "object") && (!hasProps || propsOK) {
			if !hasType {
				value["type"] = "object"
				result, _ := json.Marshal(value)
				return result, false
			}
			return schema, false
		}
	}
	return json.RawMessage(`{"type":"object","properties":{}}`), true
}

// safeName 将远端工具名转换为可注册名称，并为过长名称追加稳定摘要。
func safeName(raw string) string {
	var builder strings.Builder
	for _, r := range raw {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
	}
	name := builder.String()
	if len(name) <= 64 {
		return name
	}
	sum := sha1.Sum([]byte(raw))
	return strings.TrimRight(name[:55], "_") + "_" + hex.EncodeToString(sum[:4])
}

// invoke 调用 MCP 工具并将响应内容转换为 Agent 工具结果。
func invoke(client *Client, ctx context.Context, name string, raw json.RawMessage) (tools.Result, error) {
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var args map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return tools.Result{Text: err.Error(), IsError: true}, nil
		}
	}
	response, err := client.CallTool(callCtx, name, args)
	if err != nil {
		if sessionExpired(err) {
			invalidateClient(client)
		}
		return tools.Result{Text: "Error: " + err.Error(), IsError: true}, nil
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(response, &result); err != nil {
		return tools.Result{Text: string(response)}, nil
	}
	parts := make([]string, 0, len(result.Content))
	for _, part := range result.Content {
		if part.Type == "text" {
			parts = append(parts, part.Text)
		} else {
			typ := part.Type
			if typ == "" {
				typ = "unsupported"
			}
			parts = append(parts, "["+typ+" content]")
		}
	}
	return tools.Result{Text: tools.Truncate(strings.Join(parts, "\n"), "Output truncated."), IsError: result.IsError}, nil
}

// toolInfo 生成模型可读取的 MCP 工具详情 JSON。
func toolInfo(server string, descriptor ToolDescriptor) string {
	description := descriptor.Description
	if description == "" {
		description = descriptor.Name
	}
	schema, invalid := validatedSchema(descriptor.InputSchema)
	if invalid { description += " 原始参数结构无效，已忽略。" }
	data, _ := json.MarshalIndent(map[string]any{
		"server":      server,
		"tool":        descriptor.Name,
		"description": description,
		"inputSchema": schema,
	}, "", "  ")
	return string(data)
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}
