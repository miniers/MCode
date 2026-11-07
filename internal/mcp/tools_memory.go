package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"mcode/internal/nmem"
	"mcode/internal/store"
)

// memoryUnitTypes 为 memory_propose 允许的记忆类型，与远端的单元类型保持一致。
var memoryUnitTypes = []string{
	"fact", "preference", "decision", "plan", "procedure", "learning", "context", "event",
}

const (
	memoryUnitTypeDefault    = "fact"
	memoryImportanceDefault  = 0.5
	memoryImportanceMin      = 0.1
	memoryImportanceMax      = 1.0
	memorySearchDefaultLimit = 8
	memorySearchMinLimit     = 1
	memorySearchMaxLimit     = 20
	// memorySearchContentMaxRunes 限制单条记忆摘要的长度，避免检索结果淹没上下文。
	memorySearchContentMaxRunes = 400
	// memorySearchTimeout 为单次远端检索的额外超时；调用方已有更早的截止时间时沿用调用方的。
	memorySearchTimeout = 10 * time.Second

	// memoryGetContentMaxRunes 限制 memory_get 返回的正文长度。
	memoryGetContentMaxRunes = 12000
	// memoryProposeContentMaxRunes 限制 memory_propose 正文的长度，超长记忆会拖累后续检索。
	memoryProposeContentMaxRunes = 1200
	// memoryProposeTitleMaxRunes 限制 memory_propose 标题的长度，标题应是一行短语。
	memoryProposeTitleMaxRunes = 80
	threadSearchDefaultLimit   = 5
	threadSearchMaxLimit       = 20
	threadFetchDefaultLimit    = 20
	threadFetchMaxLimit        = 50
	// threadMessageMaxRunes 限制 thread_fetch_messages 里单条消息的长度。
	threadMessageMaxRunes = 4000
	// memoryURIPrefix 是简报与记忆里引用记忆的链接前缀，memory_get 接受它代替裸 ID。
	memoryURIPrefix = "nowledgemem://memory/"

	memoryProposeDescription = "Submit valuable long-term knowledge to the user's review inbox for approval. Proactive proposals are encouraged, typically at task completion when noting decisions and their reasons, pitfalls that cost time, user preferences, or reusable procedures. Keep each memory concise: state the conclusion in the first sentence, then the reason and when it applies, ideally under 500 characters; do not paste logs, long quotes, code dumps or step-by-step narratives, because search results only show the first 400 characters. By default the proposal is only stored locally and is written to long-term memory after the user confirms it. Do not propose current-turn progress, restated code, or anything already obvious in the repository. Call memory_search before proposing to avoid duplicate records; to correct an outdated memory, pass its ID as target_memory_id. To update the project’s 当前重点, set target_memory_id to project:overview#当前重点 (local backend only). When the user explicitly asks you to remember something, use this tool with user_requested set to true, not a memory file of your own."
	memorySearchDescription  = "Search the user's long-term memories before starting unfamiliar work and before proposing a new memory. The search covers the long-term memory space of the current project together with the shared global space, and returns past decisions, preferences, pitfalls and procedures. Treat the result as reference only: the current code is the source of truth."
	memoryGetDescription     = "Read one memory in full by its ID or by a nowledgemem://memory/<id> link, such as the links in the Working Memory briefing or the IDs that memory_search returns. Only memories in the current project's space or the global space can be read."
	memoryForgetDescription  = "Forget a memory. Use this only when the user explicitly asks to forget or delete a specific memory, passing its ID or a nowledgemem://memory/<id> link; it is the counterpart of memory_propose ('remember'). mcode first tries to delete the memory in long-term memory. When the server does not support deletion, the memory is marked as deprecated instead: its title gets a [已废弃] prefix, its content and importance stay unchanged, and it no longer appears in memory_search or the memory panel. Tell the user which of the two happened."
	threadSearchDescription  = "Search past agent conversations of the current project that were synced to long-term memory, for example how a bug was fixed or why a design was chosen. Returns matching threads with message snippets; read a thread with thread_fetch_messages. Treat the result as reference only: the current code is the source of truth."
	threadFetchDescription   = "Read the messages of one synced conversation of the current project, by the thread_id that thread_search returns. Messages are returned in order from offset; use offset to page through long threads."
)

func (r *Registry) registerMemoryTools(s *Server) {
	r.Register(ToolDefinition{
		Name:        "memory_propose",
		Description: memoryProposeDescription,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{
					"type":        "string",
					"description": "One short line naming the memory, for example 'Use worktree branches for risky changes'. At most 80 characters.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The memory itself in Markdown: lead with the conclusion, then the reason behind it and when it applies. Ideally under 500 characters, at most 1200.",
				},
				"unit_type": map[string]any{
					"type":        "string",
					"enum":        memoryUnitTypes,
					"default":     memoryUnitTypeDefault,
					"description": "Kind of memory: fact, preference, decision, plan, procedure, learning, context or event. Defaults to fact.",
				},
				"importance": map[string]any{
					"type":        "number",
					"minimum":     memoryImportanceMin,
					"maximum":     memoryImportanceMax,
					"default":     memoryImportanceDefault,
					"description": "How much the memory matters later, from 0.1 (minor) to 1.0 (critical). Defaults to 0.5.",
				},
				"labels": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional short labels for grouping, such as the module or topic the memory belongs to.",
				},
				"scope": map[string]any{
					"type":        "string",
					"enum":        []string{store.MemoryProposalScopeProject, store.MemoryProposalScopeGlobal},
					"default":     store.MemoryProposalScopeProject,
					"description": "Where the memory belongs: project (default) for knowledge about this codebase, global for knowledge that holds across all of the user's projects, such as general preferences or tooling habits.",
				},
				"user_requested": map[string]any{
					"type":        "boolean",
					"default":     false,
					"description": "Set to true only when the user explicitly asked you in this conversation to remember this. The memory is then written to long-term memory at once without review; tell the user which space it was saved to. Never set it on your own initiative.",
				},
				"target_memory_id": map[string]any{
					"type":        "string",
					"description": "ID of an existing memory to correct instead of adding a new one. Read it with memory_get first: title and content replace the old ones.",
				},
			},
			"required": []string{"title", "content"},
		},
		IsWrite:    true,
		Capability: CapMemory,
		Handler:    handleMemoryPropose,
	})

	r.Register(ToolDefinition{
		Name:        "memory_search",
		Description: memorySearchDescription,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "What to look for, in natural language, such as 'how do we run integration tests'.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     memorySearchMinLimit,
					"maximum":     memorySearchMaxLimit,
					"default":     memorySearchDefaultLimit,
					"description": "Maximum number of memories to return. Defaults to 8, clamped to 1-20.",
				},
				"unit_type": map[string]any{
					"type":        "string",
					"enum":        memoryUnitTypes,
					"description": "Only return memories of this kind, for example decision or procedure.",
				},
				"labels": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Only return memories carrying these labels.",
				},
				"mode": map[string]any{
					"type":        "string",
					"enum":        []string{"deep", "fast"},
					"description": "deep (default) also reasons over related memories; fast only does keyword and vector matching.",
				},
			},
			"required": []string{"query"},
		},
		IsWrite:    false,
		Capability: CapMemory,
		Handler:    handleMemorySearch,
	})

	r.Register(ToolDefinition{
		Name:        "memory_get",
		Description: memoryGetDescription,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The memory ID, or a nowledgemem://memory/<id> link.",
				},
			},
			"required": []string{"id"},
		},
		IsWrite:    false,
		Capability: CapMemory,
		Handler:    handleMemoryGet,
	})

	r.Register(ToolDefinition{
		Name:        "memory_forget",
		Description: memoryForgetDescription,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The memory ID, or a nowledgemem://memory/<id> link.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Optional short reason for forgetting the memory. It is only echoed in the reply, not written to long-term memory.",
				},
			},
			"required": []string{"id"},
		},
		IsWrite:    true,
		Capability: CapMemory,
		Handler:    handleMemoryForget,
	})

	r.Register(ToolDefinition{
		Name:        "thread_search",
		Description: threadSearchDescription,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Words to look for in past conversations, such as 'worktree merge conflict'.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     threadSearchMaxLimit,
					"default":     threadSearchDefaultLimit,
					"description": "Maximum number of threads to return. Defaults to 5, clamped to 1-20.",
				},
			},
			"required": []string{"query"},
		},
		IsWrite:    false,
		Capability: CapMemory,
		Handler:    handleThreadSearch,
	})

	r.Register(ToolDefinition{
		Name:        "thread_fetch_messages",
		Description: threadFetchDescription,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"thread_id": map[string]any{
					"type":        "string",
					"description": "The thread_id returned by thread_search.",
				},
				"offset": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"default":     0,
					"description": "Index of the first message to return, starting at 0. Use it with a match index from thread_search to jump to the relevant part.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     threadFetchMaxLimit,
					"default":     threadFetchDefaultLimit,
					"description": "Maximum number of messages to return. Defaults to 20, clamped to 1-50.",
				},
			},
			"required": []string{"thread_id"},
		},
		IsWrite:    false,
		Capability: CapMemory,
		Handler:    handleThreadFetch,
	})

	// 入口工具放在最后注册：此时全部记忆与会话检索工具已在列表中，enum 可直接从它们收集。
	memoryFamily.register(r)
}

// handleMemorySearch 检索当前项目记忆空间里的长期记忆，只读，不写入任何内容。
func handleMemorySearch(tc ToolContext, args map[string]any) ToolResult {
	if tc.Server == nil || tc.Server.opts.MemorySearch == nil {
		return toolError(CodeInternal, "Long-term memory is not configured in mcode.", false)
	}

	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'query'.", false)
	}

	q := nmem.SearchQuery{
		Query: query,
		Limit: clampIntArg(args["limit"], memorySearchDefaultLimit, memorySearchMinLimit, memorySearchMaxLimit),
	}
	if raw, ok := args["unit_type"].(string); ok && strings.TrimSpace(raw) != "" {
		q.UnitType = strings.TrimSpace(raw)
		if !validMemoryUnitType(q.UnitType) {
			return toolError(CodeInvalidArguments,
				fmt.Sprintf("Argument 'unit_type' must be one of: %s.", strings.Join(memoryUnitTypes, ", ")), false)
		}
	}
	labels, err := memoryProposalLabels(args["labels"])
	if err != nil {
		return toolError(CodeInvalidArguments, err.Error(), false)
	}
	q.Labels = labels
	if raw, ok := args["mode"].(string); ok && strings.TrimSpace(raw) != "" {
		q.Mode = strings.TrimSpace(raw)
		if q.Mode != "deep" && q.Mode != "fast" {
			return toolError(CodeInvalidArguments, "Argument 'mode' must be deep or fast.", false)
		}
	}

	ctx, cancel := memoryCallContext(tc)
	defer cancel()

	memories, err := tc.Server.opts.MemorySearch(ctx, tc.Project.Path, q)
	if err != nil {
		return toolError(CodeInternal, "Memory search failed: "+err.Error(), true)
	}
	memories = dropDeprecatedMemories(memories)
	if len(memories) == 0 {
		return toolSuccess("No matching memories.")
	}

	return toolSuccess(formatMemorySearchResults(memories))
}

// handleMemoryGet 读取一条记忆的完整内容，只读。
func handleMemoryGet(tc ToolContext, args map[string]any) ToolResult {
	if tc.Server == nil || tc.Server.opts.MemoryGet == nil {
		return toolError(CodeInternal, "Long-term memory is not configured in mcode.", false)
	}
	raw, _ := args["id"].(string)
	id := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), memoryURIPrefix))
	if id == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'id'.", false)
	}

	ctx, cancel := memoryCallContext(tc)
	defer cancel()
	m, err := tc.Server.opts.MemoryGet(ctx, tc.Project.Path, id)
	if isNmemNotFound(err) {
		return toolError(CodeNotFound, fmt.Sprintf("Memory %s was not found in this project's space or the global space.", id), false)
	}
	if err != nil {
		return toolError(CodeInternal, "Reading the memory failed: "+err.Error(), true)
	}
	return toolSuccess(formatMemoryDetail(m))
}

// handleMemoryForget 忘掉一条记忆：服务端能删就删，不支持删除时改为标记废弃。写操作，
// 同时把本地待审里指向这条记忆的提议一并清掉。
func handleMemoryForget(tc ToolContext, args map[string]any) ToolResult {
	if tc.Server == nil || tc.Server.opts.MemoryForget == nil {
		return toolError(CodeInternal, "Long-term memory is not configured in mcode.", false)
	}
	raw, _ := args["id"].(string)
	id := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), memoryURIPrefix))
	if id == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'id'.", false)
	}
	reason, _ := args["reason"].(string)
	reason = strings.TrimSpace(reason)

	ctx, cancel := memoryCallContext(tc)
	deleted, err := tc.Server.opts.MemoryForget(ctx, tc.Project.Path, id)
	cancel()
	if isNmemNotFound(err) {
		return toolError(CodeNotFound, fmt.Sprintf("Memory %s was not found in this project's space or the global space.", id), false)
	}
	if err != nil {
		return toolError(CodeInternal, "Forgetting the memory failed: "+err.Error(), true)
	}

	removed := removePendingProposalsForMemory(tc, id)

	var message string
	if deleted {
		message = fmt.Sprintf("Memory %s was deleted from long-term memory. Tell the user in one line that it was deleted.", id)
	} else {
		message = fmt.Sprintf("The server does not support deleting memories, so memory %s was marked as deprecated instead: its title now starts with %q and it no longer appears in searches. Tell the user in one line that it was marked as deprecated, not deleted.", id, nmem.DeprecatedTitlePrefix)
	}
	if reason != "" {
		message += " Reason: " + reason
	}
	if removed > 0 {
		message += fmt.Sprintf(" %d pending memory proposal(s) pointing at it were removed from the review inbox.", removed)
	}
	return toolSuccess(message)
}

// dropDeprecatedMemories 过滤掉已标记废弃的记忆，它们不应再出现在检索结果里。
func dropDeprecatedMemories(memories []nmem.Memory) []nmem.Memory {
	out := make([]nmem.Memory, 0, len(memories))
	for _, m := range memories {
		if nmem.IsDeprecated(m) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// removePendingProposalsForMemory 删除本地待审提议里指向该记忆的条目，返回删除条数。
// 失败不影响忘掉记忆本身，因此错误被忽略。
func removePendingProposalsForMemory(tc ToolContext, memoryID string) int {
	if tc.Server == nil || tc.Server.opts.Store == nil {
		return 0
	}
	ctx := tc.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), memorySearchTimeout)
	defer cancel()
	proposals, err := tc.Server.opts.Store.ListMemoryProposals(ctx, tc.Project.Path, store.MemoryProposalStatusPending, 0)
	if err != nil {
		return 0
	}
	removed := 0
	for _, p := range proposals {
		if p.TargetMemoryID != memoryID && p.RemoteMemoryID != memoryID {
			continue
		}
		if err := tc.Server.opts.Store.DeleteMemoryProposal(ctx, p.ID); err == nil {
			removed++
		}
	}
	return removed
}

// formatMemoryDetail 渲染单条记忆：标题行带类型、重要度、日期与标签，其后是正文（超长截断）。
func formatMemoryDetail(m nmem.Memory) string {
	var b strings.Builder
	title := strings.TrimSpace(m.Title)
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	unitType := strings.TrimSpace(m.UnitType)
	if unitType == "" {
		unitType = "unknown"
	}
	fmt.Fprintf(&b, "- ID: %s\n- Type: %s · importance %.1f", strings.TrimSpace(m.ID), unitType, m.Importance)
	if !m.CreatedAt.IsZero() {
		fmt.Fprintf(&b, " · %s", m.CreatedAt.UTC().Format("2006-01-02"))
	}
	b.WriteString("\n")
	if len(m.Labels) > 0 {
		fmt.Fprintf(&b, "- Labels: %s\n", strings.Join(m.Labels, ", "))
	}
	b.WriteString("\n")
	b.WriteString(truncateRunes(strings.TrimSpace(m.Content), memoryGetContentMaxRunes))
	return b.String()
}

// handleThreadSearch 全文检索当前项目已同步的对话，只读。
func handleThreadSearch(tc ToolContext, args map[string]any) ToolResult {
	if tc.Server == nil || tc.Server.opts.ThreadSearch == nil {
		return toolError(CodeInternal, "Long-term memory is not configured in mcode.", false)
	}
	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'query'.", false)
	}
	limit := clampIntArg(args["limit"], threadSearchDefaultLimit, 1, threadSearchMaxLimit)

	ctx, cancel := memoryCallContext(tc)
	defer cancel()
	hits, err := tc.Server.opts.ThreadSearch(ctx, tc.Project.Path, query, limit)
	if err != nil {
		return toolError(CodeInternal, "Thread search failed: "+err.Error(), true)
	}
	if len(hits) == 0 {
		return toolSuccess("No matching threads.")
	}
	var b strings.Builder
	for _, h := range hits {
		title := strings.TrimSpace(h.Title)
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(&b, "- **%s** · %s · %d messages", title, h.Source, h.MessageCount)
		if !h.LastActivity.IsZero() {
			fmt.Fprintf(&b, " · %s", h.LastActivity.UTC().Format("2006-01-02"))
		}
		fmt.Fprintf(&b, "\n  thread_id: %s\n", h.ThreadID)
		for _, m := range h.Matches {
			fmt.Fprintf(&b, "  [#%d %s] %s\n", m.Index, m.Role, memoryContentSummary(m.Snippet, memorySearchContentMaxRunes))
		}
	}
	return toolSuccess(strings.TrimRight(b.String(), "\n"))
}

// handleThreadFetch 分页读取当前项目一条已同步对话的消息，只读。
func handleThreadFetch(tc ToolContext, args map[string]any) ToolResult {
	if tc.Server == nil || tc.Server.opts.ThreadFetch == nil {
		return toolError(CodeInternal, "Long-term memory is not configured in mcode.", false)
	}
	threadID, _ := args["thread_id"].(string)
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'thread_id'.", false)
	}
	offset := clampIntArg(args["offset"], 0, 0, 1<<30)
	limit := clampIntArg(args["limit"], threadFetchDefaultLimit, 1, threadFetchMaxLimit)

	ctx, cancel := memoryCallContext(tc)
	defer cancel()
	page, err := tc.Server.opts.ThreadFetch(ctx, tc.Project.Path, threadID, offset, limit)
	if isNmemNotFound(err) {
		return toolError(CodeNotFound, fmt.Sprintf("Thread %s was not found in this project's space.", threadID), false)
	}
	if err != nil {
		return toolError(CodeInternal, "Reading the thread failed: "+err.Error(), true)
	}

	var b strings.Builder
	title := strings.TrimSpace(page.Title)
	if title == "" {
		title = "(untitled)"
	}
	end := page.Offset + len(page.Messages)
	fmt.Fprintf(&b, "# %s\n\nthread_id: %s · messages %d-%d of %d\n", title, page.ThreadID, page.Offset, end-1, page.Total)
	if len(page.Messages) == 0 {
		b.WriteString("\nNo messages at this offset.")
		return toolSuccess(b.String())
	}
	for i, m := range page.Messages {
		fmt.Fprintf(&b, "\n## #%d %s", page.Offset+i, m.Role)
		if !m.Timestamp.IsZero() {
			fmt.Fprintf(&b, " · %s", m.Timestamp.UTC().Format("2006-01-02 15:04"))
		}
		fmt.Fprintf(&b, "\n%s\n", truncateRunes(strings.TrimSpace(m.Content), threadMessageMaxRunes))
	}
	if end < page.Total {
		fmt.Fprintf(&b, "\nMore messages follow: call again with offset %d.", end)
	}
	return toolSuccess(strings.TrimRight(b.String(), "\n"))
}

// memoryCallContext 为一次远端调用加上超时；调用方已有更早的截止时间时沿用调用方的。
func memoryCallContext(tc ToolContext) (context.Context, context.CancelFunc) {
	ctx := tc.Context
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, memorySearchTimeout)
}

// clampIntArg 读取整数参数并夹到 [min, max]；缺省或类型不对时返回 def。
func clampIntArg(raw any, def, min, max int) int {
	f, ok := raw.(float64)
	if !ok {
		return def
	}
	v := int(f)
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// isNmemNotFound 判断远端是否返回 404（记忆或对话不存在，或不在守卫的空间里）。
func isNmemNotFound(err error) bool {
	var apiErr *nmem.APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// truncateRunes 按 rune 截断到 limit，超出时在末尾标注已截断。
func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "\n\n(truncated)"
}

// formatMemorySearchResults 把检索结果渲染成紧凑的 Markdown 列表：每条一行写标题、
// 类型、重要度与日期，下一行是截断后的内容摘要，末尾附记忆 ID。
func formatMemorySearchResults(memories []nmem.Memory) string {
	var b strings.Builder
	for _, m := range memories {
		title := strings.TrimSpace(m.Title)
		if title == "" {
			title = "(untitled)"
		}
		unitType := strings.TrimSpace(m.UnitType)
		if unitType == "" {
			unitType = "unknown"
		}
		fmt.Fprintf(&b, "- **%s** · %s · importance %.1f", title, unitType, m.Importance)
		if !m.CreatedAt.IsZero() {
			fmt.Fprintf(&b, " · %s", m.CreatedAt.UTC().Format("2006-01-02"))
		}
		b.WriteString("\n")
		if summary := memoryContentSummary(m.Content, memorySearchContentMaxRunes); summary != "" {
			fmt.Fprintf(&b, "  %s\n", summary)
		}
		fmt.Fprintf(&b, "  ID: %s\n", strings.TrimSpace(m.ID))
	}
	return strings.TrimRight(b.String(), "\n")
}

// memoryContentSummary 把多行内容压成单行，并按 rune 数量截断到 limit（超出时以省略号结尾）。
func memoryContentSummary(content string, limit int) string {
	joined := strings.Join(strings.Fields(content), " ")
	runes := []rune(joined)
	if len(runes) <= limit {
		return joined
	}
	return string(runes[:limit]) + "…"
}

// handleMemoryPropose 把 agent 的提议写入本地待审收件箱，不直接写入远端记忆。
func handleMemoryPropose(tc ToolContext, args map[string]any) ToolResult {
	if tc.Server == nil || tc.Server.opts.Store == nil {
		return toolError(CodeInternal, "Memory inbox is unavailable: the local store is not configured.", false)
	}

	title, _ := args["title"].(string)
	title = strings.TrimSpace(title)
	if title == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'title'.", false)
	}
	content, _ := args["content"].(string)
	if strings.TrimSpace(content) == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'content' (the memory text in Markdown).", false)
	}
	// 长度按 rune 计数，超长提议会挤占检索结果的 400 字摘要，因此直接要求 agent 精简后重试。
	if n := utf8.RuneCountInString(title); n > memoryProposeTitleMaxRunes {
		return toolError(CodeInvalidArguments, fmt.Sprintf(
			"Argument 'title' is %d characters long, but at most %d are allowed. Shorten it to one line naming the memory, then retry.",
			n, memoryProposeTitleMaxRunes), false)
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(content)); n > memoryProposeContentMaxRunes {
		return toolError(CodeInvalidArguments, fmt.Sprintf(
			"Argument 'content' is %d characters long, but at most %d are allowed. Condense it to the conclusion, the reason and when it applies, then retry.",
			n, memoryProposeContentMaxRunes), false)
	}

	unitType := memoryUnitTypeDefault
	if raw, ok := args["unit_type"].(string); ok && strings.TrimSpace(raw) != "" {
		unitType = strings.TrimSpace(raw)
		if !validMemoryUnitType(unitType) {
			return toolError(CodeInvalidArguments,
				fmt.Sprintf("Argument 'unit_type' must be one of: %s.", strings.Join(memoryUnitTypes, ", ")), false)
		}
	}

	importance := memoryImportanceDefault
	if raw, ok := args["importance"].(float64); ok {
		if raw < memoryImportanceMin || raw > memoryImportanceMax {
			return toolError(CodeInvalidArguments,
				fmt.Sprintf("Argument 'importance' must be between %.1f and %.1f.", memoryImportanceMin, memoryImportanceMax), false)
		}
		importance = raw
	}

	labels, err := memoryProposalLabels(args["labels"])
	if err != nil {
		return toolError(CodeInvalidArguments, err.Error(), false)
	}

	scope := store.MemoryProposalScopeProject
	if raw, ok := args["scope"].(string); ok && strings.TrimSpace(raw) != "" {
		scope = strings.TrimSpace(raw)
		if scope != store.MemoryProposalScopeProject && scope != store.MemoryProposalScopeGlobal {
			return toolError(CodeInvalidArguments, "Argument 'scope' must be project or global.", false)
		}
	}
	userRequested, _ := args["user_requested"].(bool)

	ctx := tc.Context
	if ctx == nil {
		ctx = context.Background()
	}

	// 改写提议先确认目标记忆在本项目或全局空间里，避免 agent 改到别的项目的记忆。
	targetID, _ := args["target_memory_id"].(string)
	targetID = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(targetID), memoryURIPrefix))
	if targetID != "" {
		if tc.Server.opts.MemoryGet == nil {
			return toolError(CodeInternal, "Long-term memory is not configured in mcode, so existing memories cannot be corrected.", false)
		}
		getCtx, cancel := memoryCallContext(tc)
		_, err := tc.Server.opts.MemoryGet(getCtx, tc.Project.Path, targetID)
		cancel()
		if isNmemNotFound(err) {
			return toolError(CodeInvalidArguments, fmt.Sprintf("Memory %s was not found in this project's space or the global space.", targetID), false)
		}
		if err != nil {
			return toolError(CodeInternal, "Reading the memory to correct failed: "+err.Error(), true)
		}
	}

	threadID := ""
	if tc.Context != nil {
		threadID = scopeThreadID(tc.Context)
	}
	source := store.MemoryProposalSourceAgent
	if userRequested {
		source = store.MemoryProposalSourceUser
	}

	proposal, err := tc.Server.opts.Store.CreateMemoryProposal(ctx, store.MemoryProposal{
		ProjectPath:    tc.Project.Path,
		ThreadID:       threadID,
		Title:          title,
		Content:        content,
		UnitType:       unitType,
		Importance:     importance,
		Labels:         labels,
		Source:         source,
		Scope:          scope,
		TargetMemoryID: targetID,
	})
	if err != nil {
		return toolError(CodeInternal, "Failed to save memory proposal: "+err.Error(), true)
	}

	if userRequested && tc.Server.opts.MemoryCommit != nil {
		committed, space, err := tc.Server.opts.MemoryCommit(ctx, proposal.ID)
		if err != nil {
			return toolSuccess(map[string]any{
				"success": false,
				"id":      proposal.ID,
				"status":  proposal.Status,
				"message": fmt.Sprintf("Writing to long-term memory failed (%v). The memory was kept as proposal %s in the review inbox; tell the user it still needs their confirmation there.", err, proposal.ID),
			})
		}
		verb := "Saved"
		if targetID != "" {
			verb = "Updated"
		}
		return toolSuccess(map[string]any{
			"success":   true,
			"id":        committed.ID,
			"status":    committed.Status,
			"memory_id": committed.RemoteMemoryID,
			"space":     space,
			"message":   fmt.Sprintf("%s in long-term memory, space %s (memory %s). Tell the user in one line that it was saved to the %s space.", verb, space, committed.RemoteMemoryID, space),
		})
	}

	return toolSuccess(map[string]any{
		"success": true,
		"id":      proposal.ID,
		"status":  proposal.Status,
		"message": fmt.Sprintf("Memory proposal %s was added to the review inbox. Nothing is saved to long-term memory until the user confirms it there.", proposal.ID),
	})
}

// validMemoryUnitType 判断记忆类型是否在允许的枚举内。
func validMemoryUnitType(unitType string) bool {
	for _, allowed := range memoryUnitTypes {
		if unitType == allowed {
			return true
		}
	}
	return false
}

// memoryProposalLabels 把参数里的标签数组转为去空白后的字符串列表；非字符串元素视为参数错误。
func memoryProposalLabels(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New("Argument 'labels' must be an array of strings.")
	}
	labels := make([]string, 0, len(items))
	for _, item := range items {
		label, ok := item.(string)
		if !ok {
			return nil, errors.New("Argument 'labels' must be an array of strings.")
		}
		if label = strings.TrimSpace(label); label != "" {
			labels = append(labels, label)
		}
	}
	return labels, nil
}

// memoryFamily 是长期记忆入口族：普通会话只看到 `memory`，按 action 调用记忆与会话检索操作。
// thread_search / thread_fetch_messages 名字不带 memory_ 前缀，用 aliases 显式收进本族。
var memoryFamily = &toolFamily{
	entry:       "memory",
	prefix:      "memory_",
	capability:  CapMemory,
	description: "长期记忆入口：检索记忆与历史会话、读取单条记忆、提交新记忆等待用户确认、或按用户要求忘记某条记忆。先调用 action=help 查看可用操作及其参数，再用 action 加上 args 调用目标操作。",
	overview:    "记忆操作入口：先用 action=help 查看操作，再用 action 加 args 调用。",
	aliases: map[string]string{
		"thread_search":         "thread_search",
		"thread_fetch_messages": "thread_fetch_messages",
	},
}
