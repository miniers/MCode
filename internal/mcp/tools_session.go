package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"mcode/internal/store"
)

func (r *Registry) registerSessionTools(s *Server) {
	r.Register(ToolDefinition{
		Name:        "session_get",
		Description: "Read session metadata and messages using the mcode session ID copied from the sidebar. Returns the latest page by default; use before_seq to page backwards.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{"type": "string", "description": "mcode session ID copied from the sidebar."},
				"before_seq": map[string]any{"type": "integer", "description": "Return messages before this sequence; use next_before_seq from the previous page."},
				"limit":      map[string]any{"type": "integer", "description": "Number of messages, default 50, clamped to 1–200."},
			},
			"required": []string{"session_id"},
		},
		IsWrite: false,
		Handler: handleSessionGet,
	})
}

func handleSessionGet(tc ToolContext, args map[string]any) ToolResult {
	id, _ := args["session_id"].(string)
	id = strings.TrimSpace(id)
	if id == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'session_id'.", false)
	}
	if tc.Server == nil || tc.Server.opts.Store == nil {
		return toolError(CodeInternal, "Session store is unavailable.", false)
	}
	ctx := tc.Context
	if ctx == nil {
		ctx = context.Background()
	}
	st := tc.Server.opts.Store
	thread, err := st.GetThread(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return toolError(CodeNotFound, fmt.Sprintf("Session %q not found.", id), false)
	}
	if err != nil {
		return toolError(CodeInternal, "Failed to read session: "+err.Error(), false)
	}
	var before int64
	if n, ok := args["before_seq"].(float64); ok && n > 0 {
		if n >= float64(1<<63) {
			before = 1<<63 - 1
		} else {
			before = int64(n)
		}
	}
	messages, hasMore, warnings, err := st.ListMessagesTolerant(ctx, id, before, clampIntArg(args["limit"], 50, 1, 200))
	if err != nil {
		return toolError(CodeInternal, "Failed to read session messages: "+err.Error(), false)
	}
	session := map[string]any{"id": thread.ID, "archived": thread.Archived}
	for key, value := range map[string]string{
		"title": thread.Title, "project_id": thread.ProjectID, "provider": thread.Provider,
		"model": thread.Model, "session_id": thread.SessionID, "runtime_mode": thread.RuntimeMode,
		"interaction_mode": thread.InteractionMode, "effort": thread.Effort,
		"worktree_path": thread.WorktreePath, "branch": thread.Branch,
	} {
		if value != "" {
			session[key] = value
		}
	}
	if !thread.CreatedAt.IsZero() {
		session["created_at"] = thread.CreatedAt.Format(time.RFC3339)
	}
	if !thread.UpdatedAt.IsZero() {
		session["updated_at"] = thread.UpdatedAt.Format(time.RFC3339)
	}
	if projects, err := st.ListProjects(ctx); err == nil {
		for _, project := range projects {
			if project.ID == thread.ProjectID {
				if project.Name != "" {
					session["project_name"] = project.Name
				}
				if project.Path != "" {
					session["project_path"] = project.Path
				}
				break
			}
		}
	}
	items := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		item := map[string]any{
			"seq": message.Seq, "role": message.Role, "kind": message.Kind,
			"turn_index": message.TurnIndex, "created_at": message.CreatedAt.Format(time.RFC3339),
			"text": truncateSessionText(message.Text),
		}
		if message.Role == "tool" {
			if message.ToolName != nil {
				item["tool_name"] = *message.ToolName
			}
			if message.ToolStatus != nil {
				item["tool_status"] = *message.ToolStatus
			}
			if message.ToolInput != nil {
				item["tool_input"] = truncateSessionText(*message.ToolInput)
			}
		}
		if message.ErrKind != nil && *message.ErrKind != "" {
			item["err_kind"] = *message.ErrKind
		}
		items = append(items, item)
	}
	result := map[string]any{"session": session, "messages": items, "has_more": hasMore}
	if hasMore && len(messages) > 0 {
		result["next_before_seq"] = messages[0].Seq
	}
	if len(warnings) > 0 {
		result["warnings"] = warnings
	}
	return toolSuccess(result)
}

func truncateSessionText(s string) string {
	runes := []rune(s)
	if len(runes) > 4000 {
		return string(runes[:4000]) + "…(truncated)"
	}
	return s
}
