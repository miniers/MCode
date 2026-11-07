package mcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// swarmDelegateToolName 是编排派发工具名；只对编排开启的主会话可见。
const swarmDelegateToolName = "swarm_delegate"

// SwarmHooks 把编排派发器接给 swarm_delegate 工具。编排在 MCP 服务之后才接线，
// 所以由 app 用 Server.SetSwarm 注入；没有注入时工具既不列出也不能调用。
type SwarmHooks struct {
	// Available 报告该会话能否使用工具（编排开启的主会话）。
	Available func(threadID string) bool
	// Delegate 派发一批条目，返回给模型看的结果文本。条目的键与 ```swarm 块里的一致。
	Delegate func(ctx context.Context, threadID string, entries []map[string]any) (string, error)
}

// SetSwarm 并发安全地设置编排钩子。
func (s *Server) SetSwarm(hooks *SwarmHooks) {
	s.swarmMu.Lock()
	defer s.swarmMu.Unlock()
	s.swarm = hooks
}

func (s *Server) swarmHooks() *SwarmHooks {
	s.swarmMu.RLock()
	defer s.swarmMu.RUnlock()
	return s.swarm
}

// swarmAvailable 判断请求所在的会话能否使用 swarm_delegate；没有会话作用域的共享端点一律不能。
func (s *Server) swarmAvailable(ctx context.Context) bool {
	return swarmUsable(s.swarmHooks(), ctx)
}

// swarmUsable 用调用方已取到的钩子做判定，tools/list 与 tools/call 因此共用同一条规则，
// 也让 tools/call 之后复用同一个钩子指针，避免两次取钩子之间被 SetSwarm(nil) 置空。
func swarmUsable(hooks *SwarmHooks, ctx context.Context) bool {
	threadID := scopeThreadID(ctx)
	return threadID != "" && hooks != nil && hooks.Available != nil && hooks.Delegate != nil && hooks.Available(threadID)
}

// filterSwarmTool 在 tools/list 里隐藏该会话用不了的 swarm_delegate。
func (s *Server) filterSwarmTool(r *http.Request, tools []ToolDefinition) []ToolDefinition {
	if s.swarmAvailable(r.Context()) {
		return tools
	}
	out := make([]ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		if tool.Name != swarmDelegateToolName {
			out = append(out, tool)
		}
	}
	return out
}

func (r *Registry) registerSwarmTools(s *Server) {
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	r.Register(ToolDefinition{
		Name: swarmDelegateToolName,
		Description: "Hand work to helper agents (bees). Each entry starts a bee right away, so call it as soon as a brief is ready and order entries by start time. " +
			"Entries have the same fields as an entry of the ```swarm block, and unlike the block a prompt here may contain code fences. " +
			"The result says how many bees were started and why any were not. " +
			"Use this for work that changes files: every bee gets its own worktree, review and merge. For read-only investigation use the subagent tool instead.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tasks": map[string]any{
					"type":        "array",
					"description": "Bees to start. Each is an object: prompt (required, full standalone instructions naming the files and acceptance criteria), task (short title), and optionally bee, tier, skills (array of names), project, provider, model, profile, pair, replaces (name of a running bee this entry supersedes; mcode stops that bee before starting this one).",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"prompt":   str("Full standalone instructions for the bee."),
							"task":     str("Short title."),
							"bee":      str("Name from the roster."),
							"replaces": str("Name of a running bee this entry supersedes; mcode stops that bee before starting this one. Use it only when a running bee's brief must change."),
							"tier":     str("Tier id or name."),
							"project":  str("Another project to work in."),
							"provider": str("Provider override."),
							"model":    str("Model override."),
							"profile":  str("Profile override."),
							"pair":     str("Lead and bee model pair."),
							"skills":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						},
						"required": []string{"prompt"},
					},
				},
			},
			"required": []string{"tasks"},
		},
		IsWrite: true,
		Handler: handleSwarmDelegate,
	})
}

func handleSwarmDelegate(tc ToolContext, args map[string]any) ToolResult {
	// 钩子只取一次：判定与派发用同一个指针，中途 SetSwarm(nil) 也不会让后面拿到 nil。
	// 与 tools/list 同一判定：用不了的工具按未知工具拒绝，不暴露它存在。
	hooks := tc.Server.swarmHooks()
	if !swarmUsable(hooks, tc.Context) {
		return toolError(CodeNotFound, "Unknown tool: "+swarmDelegateToolName, false)
	}
	raw, _ := args["tasks"].([]any)
	entries := make([]map[string]any, 0, len(raw))
	ignored := 0
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			ignored++
			continue
		}
		// 与下游 swarm.ParseEntry 同一判定：prompt 是 TrimSpace 后非空的字符串，否则这条派不出去。
		if prompt, ok := entry["prompt"].(string); !ok || strings.TrimSpace(prompt) == "" {
			ignored++
			continue
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return toolError(CodeInvalidArguments, "tasks must be a non-empty array of objects, each with a prompt", false)
	}
	text, err := hooks.Delegate(tc.Context, scopeThreadID(tc.Context), entries)
	if err != nil {
		return toolError(CodeInternal, err.Error(), false)
	}
	// 措辞与 internal/app/swarm_wiring.go 里的忽略提示保持一致，让模型知道哪些条目被丢掉。
	if ignored > 0 {
		text += fmt.Sprintf(" %d entry(ies) ignored: no prompt.", ignored)
	}
	return toolSuccess(text)
}
