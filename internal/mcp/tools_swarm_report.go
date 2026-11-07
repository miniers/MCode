package mcp

import (
	"errors"
	"sort"
	"strings"
	"time"

	"mcode/internal/store"
)

// swarmReportGetToolName 是按 bee ID 取回存档汇报全文、或不带 bee ID 列出本会话所有 bee 的工具名。
const swarmReportGetToolName = "swarm_report_get"

// SwarmBeeSummary 是编排子代理（bee）的对外快照，供 swarm_report_get 列表模式返回。
// mcp 包不依赖 internal/swarm，由 app 层把 swarm.Bee 转换进来。
type SwarmBeeSummary struct {
	// ID 是 bee 的线程 ID。
	ID     string
	Name   string
	Task   string
	Tier   string
	Status string
	// StartedAt 是派发时刻，列表按它升序排列。
	StartedAt time.Time
	// FinishedAt 为 nil 表示尚未结束。
	FinishedAt *time.Time
	// LastProgressAt 是最后一次看到进展的时刻，零值表示没有进展记录。
	LastProgressAt time.Time
}

func (r *Registry) registerSwarmReportTools(s *Server) {
	r.Register(ToolDefinition{
		Name: swarmReportGetToolName,
		Description: "Without bee_id, list every bee of the calling lead session with its status (running, completed, failed, stopped), " +
			"so you can tell a bee still at work from one that failed. " +
			"With bee_id, fetch the full archived report of a finished bee; reports longer than 1500 characters are cut short in the report message, " +
			"which then carries a '- ID：' line. Only bees of the calling lead session can be read.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"bee_id": map[string]any{"type": "string", "description": "Bee ID from the '- ID：' line of the report message; omit it to list every bee of the calling lead session with its status."},
			},
			"required": []string{},
		},
		Handler: handleSwarmReportGet,
	})
}

func handleSwarmReportGet(tc ToolContext, args map[string]any) ToolResult {
	beeID, _ := args["bee_id"].(string)
	beeID = strings.TrimSpace(beeID)
	// 只认会话作用域里的线程身份：没有身份的共享端点无从判断归属，一律拒绝（两种模式都要）。
	leadThreadID := scopeThreadID(tc.Context)
	if leadThreadID == "" {
		return toolError(CodeForbidden, "swarm_report_get needs a session-scoped endpoint to identify the lead session", false)
	}
	if beeID == "" {
		return listSwarmBees(tc, leadThreadID)
	}
	if tc.Server == nil || tc.Server.opts.Store == nil {
		return toolError(CodeInternal, "Store is unavailable", false)
	}
	rec, err := tc.Server.opts.Store.GetSwarmReport(tc.Context, beeID)
	if errors.Is(err, store.ErrNotFound) {
		return toolError(CodeNotFound, "No archived report for bee "+beeID, false)
	}
	if err != nil {
		return toolError(CodeInternal, err.Error(), true)
	}
	if rec.LeadThreadID != leadThreadID {
		return toolError(CodeForbidden, "Bee "+beeID+" does not belong to this lead session", false)
	}
	return toolSuccess(map[string]any{
		"bee_id":  rec.BeeID,
		"name":    rec.Name,
		"task":    rec.Task,
		"summary": rec.Summary,
	})
}

// listSwarmBees 返回本主会话所有 bee 的状态快照，按 StartedAt 升序。
func listSwarmBees(tc ToolContext, leadThreadID string) ToolResult {
	if tc.Server == nil || tc.Server.opts.SwarmBees == nil {
		return toolError(CodeInternal, "bee listing is unavailable", false)
	}
	bees := tc.Server.opts.SwarmBees(leadThreadID)
	sort.SliceStable(bees, func(i, j int) bool { return bees[i].StartedAt.Before(bees[j].StartedAt) })
	items := make([]map[string]any, 0, len(bees))
	for _, bee := range bees {
		item := map[string]any{
			"bee_id":     bee.ID,
			"name":       bee.Name,
			"task":       bee.Task,
			"tier":       bee.Tier,
			"status":     bee.Status,
			"started_at": bee.StartedAt.UTC().Format(time.RFC3339),
		}
		if bee.FinishedAt != nil && !bee.FinishedAt.IsZero() {
			item["finished_at"] = bee.FinishedAt.UTC().Format(time.RFC3339)
		}
		if !bee.LastProgressAt.IsZero() {
			item["last_progress_at"] = bee.LastProgressAt.UTC().Format(time.RFC3339)
		}
		items = append(items, item)
	}
	return toolSuccess(map[string]any{"bees": items})
}
