package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mcode/internal/store"
	"mcode/internal/swarm"
)

const showcaseClaude = "claude-opus-5-5"
const showcaseCodex = "gpt-5.5"

const (
	showcaseOrbitProjectID  = "7b0c2e1a-4f3d-4c8e-9a51-000000000001"
	showcaseLumenProjectID  = "7b0c2e1a-4f3d-4c8e-9a51-000000000002"
	showcasePocketProjectID = "7b0c2e1a-4f3d-4c8e-9a51-000000000003"

	showcasePaginationID     = "7b0c2e1a-4f3d-4c8e-9a51-000000000011"
	showcaseSwarmID          = "7b0c2e1a-4f3d-4c8e-9a51-000000000012"
	showcaseOrderIndexID     = "7b0c2e1a-4f3d-4c8e-9a51-000000000013"
	showcaseRefundID         = "7b0c2e1a-4f3d-4c8e-9a51-000000000014"
	showcaseCheckoutID       = "7b0c2e1a-4f3d-4c8e-9a51-000000000021"
	showcaseWebPaginationID  = "7b0c2e1a-4f3d-4c8e-9a51-000000000022"
	showcaseDarkID           = "7b0c2e1a-4f3d-4c8e-9a51-000000000023"
	showcaseNotesSearchID    = "7b0c2e1a-4f3d-4c8e-9a51-000000000031"
	showcaseNotesSyncID      = "7b0c2e1a-4f3d-4c8e-9a51-000000000032"
	showcaseNotesTagsID      = "7b0c2e1a-4f3d-4c8e-9a51-000000000033"
	showcaseBeeFlowID        = "7b0c2e1a-4f3d-4c8e-9a51-000000000041"
	showcaseBeeIdempotencyID = "7b0c2e1a-4f3d-4c8e-9a51-000000000042"
	showcaseBeeTestsID       = "7b0c2e1a-4f3d-4c8e-9a51-000000000043"
)

func seedShowcase(ctx context.Context, db *store.Store, path string) error {
	if err := db.SetSetting(ctx, "demo_seeded", "1"); err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Minute)
	projects := []struct {
		id      string
		slug    string
		threads []struct{ id, title, question, answer string }
	}{
		{showcaseOrbitProjectID, "orbit-api", []struct{ id, title, question, answer string }{
			{showcasePaginationID, "给订单列表接口加游标分页", "订单列表数据越来越多，请改成游标分页，并保持结果稳定。", ""},
			{showcaseSwarmID, "重构支付回调并补测试", "请重构支付回调，保证重复通知安全，并补齐测试。", ""},
			{showcaseOrderIndexID, "优化订单查询索引", "检查订单查询的索引，分页时避免全表扫描。", "已为状态和创建时间添加组合索引，并核对了查询计划。"},
			{showcaseRefundID, "整理退款状态流转", "梳理退款状态转换，避免重复处理。", "已收紧状态转换条件，并覆盖重复请求。"},
		}},
		{showcaseLumenProjectID, "lumen-web", []struct{ id, title, question, answer string }{
			{showcaseCheckoutID, "修复结算页金额四舍五入", "结算页的优惠金额偶尔差一分钱，请修复。", "金额计算改用整数分，展示时再格式化。"},
			{showcaseWebPaginationID, "订单表格接入分页", "订单表格接入游标分页，切换筛选条件时重置游标。", "已接入下一页游标，并处理空列表和加载状态。"},
			{showcaseDarkID, "登录页适配深色模式", "让登录页跟随系统深色模式。", "已统一表单、边框和焦点状态的深色配色。"},
		}},
		{showcasePocketProjectID, "pocket-notes", []struct{ id, title, question, answer string }{
			{showcaseNotesSearchID, "改善笔记搜索体验", "搜索结果需要高亮关键词，并支持键盘选择。", "已增加关键词高亮和上下键导航。"},
			{showcaseNotesSyncID, "处理离线笔记同步", "离线编辑后恢复网络时，请安全同步笔记。", "已按版本合并变更，冲突时保留本地副本。"},
			{showcaseNotesTagsID, "支持笔记标签筛选", "给笔记列表添加标签筛选。", "已支持多标签筛选，并保留当前选择。"},
		}},
	}
	for pi, project := range projects {
		workdir := filepath.Join(path, "work", project.slug)
		if err := os.MkdirAll(workdir, 0755); err != nil {
			return err
		}
		projectName := map[string]string{"orbit-api": "orbit-api · Go 订单服务", "lumen-web": "lumen-web · React 前台", "pocket-notes": "pocket-notes · 笔记应用"}[project.slug]
		if err := db.UpsertProject(ctx, store.Project{ID: project.id, Name: projectName, Path: workdir, CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now.Add(-time.Duration(pi) * time.Hour)}); err != nil {
			return err
		}
		for ti, item := range project.threads {
			when := now.Add(-time.Duration(pi*20+ti*3) * time.Minute)
			if item.id == showcasePaginationID {
				when = now
			}
			provider, model := "claude", showcaseClaude
			if ti == 2 {
				provider, model = "codex", showcaseCodex
			}
			thread := store.Thread{ID: item.id, ProjectID: project.id, Title: item.title, Provider: provider, Model: model, Pinned: item.id == showcasePaginationID, CreatedAt: when.Add(-time.Hour), UpdatedAt: when}
			messages := []store.Message{{Role: "user", Text: item.question, TurnIndex: 1}, {Role: "assistant", Text: item.answer, TurnIndex: 1, IsFinal: true}}
			if item.id == showcasePaginationID {
				messages = []store.Message{
					{Role: "user", Text: item.question, TurnIndex: 1},
					{Role: "assistant", Text: "我会先检查现有查询，再用创建时间和订单 ID 组成稳定游标。", TurnIndex: 1},
					showcaseTool("Read", `{"file_path":"internal/order/repository.go"}`, "已读取订单列表查询。"),
					showcaseTool("Edit", `{"file_path":"internal/order/repository.go","old_string":"query = query.Limit(limit)","new_string":"query = query.Where(\"(created_at, id) < (?, ?)\", cursor.CreatedAt, cursor.ID).Limit(limit + 1)"}`, "已更新查询条件。"),
					showcaseTool("Bash", `{"command":"go test ./internal/order/..."}`, "ok  orbit-api/internal/order  0.412s"),
					{Role: "assistant", Text: "已完成游标分页，并覆盖相同时间戳和末页场景。\n\n```go\nif len(orders) > limit {\n    nextCursor = encodeCursor(orders[limit-1])\n    orders = orders[:limit]\n}\n```\n\n查询按创建时间和 ID 稳定排序，测试已通过。", TurnIndex: 1, IsFinal: true},
					{Role: "user", Text: "如果两笔订单的创建时间相同呢？", TurnIndex: 2},
					{Role: "assistant", Text: "会用订单 ID 作为第二排序键，避免重复或遗漏。", TurnIndex: 2, IsFinal: true},
				}
			} else if item.id == showcaseSwarmID {
				messages = []store.Message{
					{Role: "user", Text: item.question, TurnIndex: 1},
					{Role: "assistant", Text: "我会并行检查回调流程、幂等处理和测试覆盖，再汇总结果。", TurnIndex: 1},
					{Role: "assistant", Text: "派 Aspen 梳理回调流程、Birch 实现幂等处理、Cedar 补测试，完成后汇总。", TurnIndex: 1},
					{Role: "assistant", Text: "三个子代理都已汇报。回调改为在事务内校验事件并更新订单，重复通知不会再次扣款，新增重复通知、签名错误和事务回滚测试。", TurnIndex: 1, IsFinal: true},
				}
			}
			if err := showcaseThread(ctx, db, thread, messages); err != nil {
				return err
			}
		}
	}
	finished := now.Add(-4 * time.Minute)
	bees := []swarm.Bee{
		{ThreadID: showcaseBeeFlowID, Index: 1, Name: "Aspen", Task: "检查支付回调流程", Summary: "梳理了通知解析和订单更新路径，建议将状态校验集中处理。", Provider: "claude", Model: showcaseClaude, Tier: "read", ToolCalls: 8, Tokens: 5200},
		{ThreadID: showcaseBeeIdempotencyID, Index: 2, Name: "Birch", Task: "实现回调幂等处理", Summary: "增加事件去重和事务内状态更新，重复通知不会再次扣款。", Provider: "codex", Model: showcaseCodex, Tier: "edit", ToolCalls: 14, Tokens: 8700},
		{ThreadID: showcaseBeeTestsID, Index: 3, Name: "Cedar", Task: "补充支付回调测试", Summary: "覆盖重复通知、签名错误和事务回滚场景。", Provider: "claude", Model: showcaseClaude, Tier: "edit", ToolCalls: 11, Tokens: 6900},
	}
	for i := range bees {
		bee := &bees[i]
		bee.ProjectID = showcaseOrbitProjectID
		bee.Status = swarm.StatusCompleted
		bee.Reported = true
		bee.StartedAt = now.Add(-20*time.Minute + time.Duration(i)*time.Minute)
		bee.FinishedAt = &finished
		thread := store.Thread{ID: bee.ThreadID, ProjectID: bee.ProjectID, ParentThreadID: showcaseSwarmID, Title: bee.Task, Provider: bee.Provider, Model: bee.Model, IsHelper: true, CreatedAt: bee.StartedAt, UpdatedAt: finished}
		messages := []store.Message{{Role: "user", Text: bee.Task, TurnIndex: 1}, {Role: "assistant", Text: "已检查相关实现并完成处理。", TurnIndex: 1}, {Role: "assistant", Text: bee.Summary, TurnIndex: 1, IsFinal: true}}
		switch bee.ThreadID {
		case showcaseBeeFlowID:
			messages = []store.Message{
				{Role: "user", Text: bee.Task, TurnIndex: 1},
				{Role: "assistant", Text: "已检查相关实现并完成处理。", TurnIndex: 1},
				showcaseTool("Read", `{"file_path":"internal/payment/callback.go"}`, "已读取支付回调实现。"),
				{Role: "assistant", Text: bee.Summary, TurnIndex: 1, IsFinal: true},
			}
		case showcaseBeeIdempotencyID:
			messages = []store.Message{
				{Role: "user", Text: bee.Task, TurnIndex: 1},
				{Role: "assistant", Text: "先确认回调入口，再在事务里按事件 ID 去重。", TurnIndex: 1},
				showcaseTool("Read", `{"file_path":"internal/payment/callback.go"}`, "已读取支付回调实现。"),
				showcaseTool("Edit", `{"file_path":"internal/payment/callback.go","old_string":"if err := s.orders.MarkPaid(ctx, evt.OrderID); err != nil {","new_string":"if seen, err := s.events.Seen(ctx, tx, evt.ID); err != nil || seen {\n    return err"}`, "已更新回调幂等处理。"),
				showcaseTool("Bash", `{"command":"go test ./internal/payment/..."}`, "ok  orbit-api/internal/payment  0.538s"),
				{Role: "assistant", Text: bee.Summary, TurnIndex: 1, IsFinal: true},
			}
		case showcaseBeeTestsID:
			messages = []store.Message{
				{Role: "user", Text: bee.Task, TurnIndex: 1},
				{Role: "assistant", Text: "已检查相关实现并完成处理。", TurnIndex: 1},
				showcaseTool("Bash", `{"command":"go test ./internal/payment/..."}`, "ok  orbit-api/internal/payment  0.538s"),
				{Role: "assistant", Text: bee.Summary, TurnIndex: 1, IsFinal: true},
			}
		}
		if err := showcaseThread(ctx, db, thread, messages); err != nil {
			return err
		}
		if err := db.SaveSwarmReport(ctx, store.SwarmReport{BeeID: bee.ThreadID, LeadThreadID: showcaseSwarmID, Name: bee.Name, Task: bee.Task, Summary: bee.Summary}); err != nil {
			return err
		}
	}
	payload, err := json.Marshal(bees)
	if err != nil {
		return err
	}
	// 会话用量面板与子代理浮窗按 turn_usage 累加 token；展示数据补齐用量，
	// 让每个子代理的累计 tokens 与其 Tokens 字段一致。
	usageTotals := map[string]int64{showcasePaginationID: 14600, showcaseSwarmID: 18800}
	for _, bee := range bees {
		usageTotals[bee.ThreadID] = int64(bee.Tokens)
	}
	for threadID, total := range usageTotals {
		if err := db.UpsertTurnUsage(ctx, store.TurnUsage{ThreadID: threadID, TurnIndex: 1, InputTokens: total * 3 / 4, OutputTokens: total - total*3/4}); err != nil {
			return err
		}
	}
	return db.SaveSwarmLead(ctx, store.SwarmLeadRecord{LeadThreadID: showcaseSwarmID, Enabled: true, ProjectID: showcaseOrbitProjectID, LeadWorkDir: filepath.Join(path, "work", "orbit-api"), NextIndex: len(bees) + 1, BeesJSON: string(payload)})
}

func showcaseTool(name, input, output string) store.Message {
	status := "completed"
	return store.Message{Role: "tool", Kind: "tool", Text: output, TurnIndex: 1, ToolName: &name, ToolInput: &input, ToolStatus: &status}
}

func showcaseThread(ctx context.Context, db *store.Store, thread store.Thread, messages []store.Message) error {
	if _, err := db.GetThread(ctx, thread.ID); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err := db.CreateThread(ctx, thread); err != nil {
		return err
	}
	for i, msg := range messages {
		msg.ID = fmt.Sprintf("%s-message-%d", thread.ID, i+1)
		msg.ThreadID = thread.ID
		msg.Seq = int64(i + 1)
		msg.CreatedAt = thread.UpdatedAt.Add(time.Duration(i-len(messages)) * time.Second)
		if msg.Kind == "" {
			msg.Kind = "text"
		}
		if _, err := db.AppendMessage(ctx, msg); err != nil {
			return err
		}
	}
	return nil
}
