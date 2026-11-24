package mcodeui

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	readmeOrbitProjectID  = "orbit-api"
	readmeLumenProjectID  = "lumen-web"
	readmePocketProjectID = "pocket-notes"

	readmePaginationThreadID = "th-orbit-pagination"
	readmeSwarmLeadThreadID  = "th-orbit-swarm"
	readmeBeeAriaThreadID    = "th-bee-aria"
	readmeBeeBramThreadID    = "th-bee-bram"
	readmeBeeCoraThreadID    = "th-bee-cora"
)

// readmeShowcaseClient 在 FakeClient 基础上实现跨会话消息正文搜索接口，
// 供 README 截图与命令面板检索演示复用。
type readmeShowcaseClient struct {
	*FakeClient
}

// SearchMessages 在全部未归档会话消息中按子串匹配查询词，返回命中片段。
func (c *readmeShowcaseClient) SearchMessages(_ context.Context, query string, limit int) ([]MessageSearchHit, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	threadMeta := make(map[string]Thread)
	for _, list := range c.threads {
		for _, th := range list {
			threadMeta[th.ID] = th
		}
	}

	var hits []MessageSearchHit
	for threadID, msgs := range c.messages {
		th := threadMeta[threadID]
		if th.ID == "" || th.IsHelper {
			continue
		}
		for i, m := range msgs {
			if m.Role == "tool" || isSwarmNoticeText(m.Text) {
				continue
			}
			if !strings.Contains(strings.ToLower(m.Text), q) {
				continue
			}
			snippet := strings.ReplaceAll(strings.TrimSpace(m.Text), "\n", " ")
			runes := []rune(snippet)
			if len(runes) > 72 {
				snippet = string(runes[:72]) + "…"
			}
			hits = append(hits, MessageSearchHit{
				ThreadID:    threadID,
				ThreadTitle: th.Title,
				ProjectID:   th.ProjectID,
				MessageID:   m.ID,
				Role:        m.Role,
				Snippet:     snippet,
				Seq:         int64(i + 1),
				CreatedAt:   th.UpdatedAt,
			})
			if limit > 0 && len(hits) >= limit {
				return hits, nil
			}
		}
	}
	return hits, nil
}

// writeReadmeShot 用 1320×860 DIP、scale 2（输出 2640×1720）、ReduceMotion 渲染指定视图并写入 docs/images/<name>.png。
func writeReadmeShot(t *testing.T, name string, view func(*ui.Context), dark bool) {
	t.Helper()
	setAppReduceMotion(t, true)
	tt := ui.NewTesterWith(view, WindowDefaultWidth, WindowDefaultHeight, ui.TesterOptions{
		Preferences: ui.Preferences{ReduceMotion: true},
		Dark:        dark,
	})
	tt.SetScale(2)

	// 推进时钟并让异步数据加载、200ms 搜索防抖与动画收敛。
	for i := 0; i < 80; i++ {
		time.Sleep(12 * time.Millisecond)
		tt.Advance(50 * time.Millisecond)
		tt.Frame()
		if i >= 28 && !tt.FramePending() {
			break
		}
	}
	// 取图前移走窗口键盘焦点，避免输入框占位文字或输入内容前出现闪烁光标。
	tt.SetFocused(false)
	if active, _ := tt.TextInputActive(); active {
		t.Fatalf("screenshot %s still has active text input focus", name)
	}

	img := tt.Image()
	wantW, wantH := WindowDefaultWidth*2, WindowDefaultHeight*2
	if gotW, gotH := img.Bounds().Dx(), img.Bounds().Dy(); gotW != wantW || gotH != wantH {
		t.Fatalf("screenshot %s dimensions = %dx%d, want %dx%d", name, gotW, gotH, wantW, wantH)
	}

	fileName := strings.TrimSuffix(name, ".png") + ".png"
	outPath := filepath.Join("..", "..", "docs", "images", fileName)
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		t.Fatalf("create docs/images dir: %v", err)
	}
	f, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create %s: %v", outPath, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode %s: %v", outPath, err)
	}
}

// newReadmeShowcaseClient 构造包含 3 个项目、多轮对话、工具调用、Todo 清单、编排团队与搜索命中的展示客户端。
func newReadmeShowcaseClient() *readmeShowcaseClient {
	fake := NewFakeClient()
	now := time.Now().UTC().Truncate(time.Minute)
	at := func(base time.Time, offset time.Duration) string {
		return base.Add(offset).Format(time.RFC3339)
	}

	fake.SetProviders([]ProviderInfo{
		{
			ID:        "claude",
			Name:      "Claude Code",
			Available: true,
			Models: []ModelInfo{
				{ID: "claude-opus-5-5", Name: "Claude Opus 5.5", ContextLimit: 200_000},
				{ID: "claude-sonnet-5-5", Name: "Claude Sonnet 5.5", ContextLimit: 200_000},
			},
		},
		{
			ID:        "codex",
			Name:      "Codex",
			Available: true,
			Models: []ModelInfo{
				{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex", ContextLimit: 400_000},
			},
		},
	})

	fake.mu.Lock()
	fake.gitState = GitState{
		WorkDir:      "/Users/alex/code/orbit-api",
		Branch:       "feat/cursor-pagination",
		Head:         "a3f91c2",
		Branches:     []string{"main", "feat/cursor-pagination", "fix/payment-idempotency"},
		Changes:      3,
		ChangedFiles: 3,
		Ahead:        2,
		HasUpstream:  true,
	}
	fake.mu.Unlock()

	projects := []Project{
		{ID: readmeOrbitProjectID, Name: "orbit-api · Go 订单服务", Path: "/Users/alex/code/orbit-api"},
		{ID: readmeLumenProjectID, Name: "lumen-web · React 前台", Path: "/Users/alex/code/lumen-web"},
		{ID: readmePocketProjectID, Name: "pocket-notes · 笔记应用", Path: "/Users/alex/code/pocket-notes"},
	}

	threads := map[string][]Thread{
		readmeOrbitProjectID: {
			{
				ID:                readmePaginationThreadID,
				ProjectID:         readmeOrbitProjectID,
				Title:             "给订单列表接口加游标分页",
				Provider:          "claude",
				Model:             "claude-opus-5-5",
				Pinned:            true,
				UpdatedAt:         now,
				LastUserMessageAt: now.Add(-2 * time.Minute),
				LastStatus:        "completed",
				ContextUsedTokens: 18_400,
				ContextWindow:     200_000,
			},
			{
				ID:                readmeSwarmLeadThreadID,
				ProjectID:         readmeOrbitProjectID,
				Title:             "重构支付回调并补测试",
				Provider:          "claude",
				Model:             "claude-opus-5-5",
				SwarmBusy:         true,
				UpdatedAt:         now.Add(-3 * time.Minute),
				LastUserMessageAt: now.Add(-12 * time.Minute),
				ContextUsedTokens: 24_600,
				ContextWindow:     200_000,
			},
			{
				ID:                readmeBeeCoraThreadID,
				ProjectID:         readmeOrbitProjectID,
				ParentThreadID:    readmeSwarmLeadThreadID,
				IsHelper:          true,
				IsRunning:         true,
				Title:             "Cora: 补充并发回调与回滚测试",
				Provider:          "claude",
				Model:             "claude-opus-5-5",
				UpdatedAt:         now.Add(-1 * time.Minute),
				ContextUsedTokens: 6_400,
				ContextWindow:     200_000,
			},
			{
				ID:                readmeBeeBramThreadID,
				ProjectID:         readmeOrbitProjectID,
				ParentThreadID:    readmeSwarmLeadThreadID,
				IsHelper:          true,
				LastStatus:        "completed",
				Title:             "Bram: 实现基于事件 ID 的幂等去重",
				Provider:          "codex",
				Model:             "gpt-5.3-codex",
				UpdatedAt:         now.Add(-4 * time.Minute),
				ContextUsedTokens: 8_700,
				ContextWindow:     400_000,
			},
			{
				ID:                readmeBeeAriaThreadID,
				ProjectID:         readmeOrbitProjectID,
				ParentThreadID:    readmeSwarmLeadThreadID,
				IsHelper:          true,
				LastStatus:        "completed",
				Title:             "Aria: 梳理支付回调与验签链路",
				Provider:          "claude",
				Model:             "claude-sonnet-5-5",
				UpdatedAt:         now.Add(-6 * time.Minute),
				ContextUsedTokens: 5_200,
				ContextWindow:     200_000,
			},
			{
				ID:                "th-orbit-index",
				ProjectID:         readmeOrbitProjectID,
				Title:             "优化订单查询组合索引",
				Provider:          "codex",
				Model:             "gpt-5.3-codex",
				UpdatedAt:         now.Add(-18 * time.Minute),
				LastUserMessageAt: now.Add(-20 * time.Minute),
				LastStatus:        "completed",
			},
			{
				ID:                "th-orbit-refund",
				ProjectID:         readmeOrbitProjectID,
				Title:             "整理退款状态机流转",
				Provider:          "claude",
				Model:             "claude-sonnet-5-5",
				UpdatedAt:         now.Add(-45 * time.Minute),
				LastUserMessageAt: now.Add(-50 * time.Minute),
				LastStatus:        "completed",
			},
		},
		readmeLumenProjectID: {
			{
				ID:                "th-lumen-pagination",
				ProjectID:         readmeLumenProjectID,
				Title:             "订单表格接入游标分页",
				Provider:          "claude",
				Model:             "claude-sonnet-5-5",
				UpdatedAt:         now.Add(-25 * time.Minute),
				LastUserMessageAt: now.Add(-28 * time.Minute),
				LastStatus:        "completed",
			},
			{
				ID:                "th-lumen-checkout",
				ProjectID:         readmeLumenProjectID,
				Title:             "修复结算页优惠金额四舍五入",
				Provider:          "codex",
				Model:             "gpt-5.3-codex",
				UpdatedAt:         now.Add(-65 * time.Minute),
				LastUserMessageAt: now.Add(-70 * time.Minute),
				LastStatus:        "completed",
			},
			{
				ID:                "th-lumen-dark",
				ProjectID:         readmeLumenProjectID,
				Title:             "登录页适配深色模式配色",
				Provider:          "claude",
				Model:             "claude-sonnet-5-5",
				UpdatedAt:         now.Add(-2 * time.Hour),
				LastUserMessageAt: now.Add(-2 * time.Hour),
				LastStatus:        "completed",
			},
		},
		readmePocketProjectID: {
			{
				ID:                "th-pocket-search",
				ProjectID:         readmePocketProjectID,
				Title:             "改善笔记检索与分页加载",
				Provider:          "claude",
				Model:             "claude-sonnet-5-5",
				UpdatedAt:         now.Add(-3 * time.Hour),
				LastUserMessageAt: now.Add(-3 * time.Hour),
				LastStatus:        "completed",
			},
			{
				ID:                "th-pocket-sync",
				ProjectID:         readmePocketProjectID,
				Title:             "处理离线笔记版本冲突同步",
				Provider:          "codex",
				Model:             "gpt-5.3-codex",
				UpdatedAt:         now.Add(-5 * time.Hour),
				LastUserMessageAt: now.Add(-5 * time.Hour),
				LastStatus:        "completed",
			},
			{
				ID:                "th-pocket-tags",
				ProjectID:         readmePocketProjectID,
				Title:             "支持多标签筛选与键盘导航",
				Provider:          "claude",
				Model:             "claude-sonnet-5-5",
				UpdatedAt:         now.Add(-8 * time.Hour),
				LastUserMessageAt: now.Add(-8 * time.Hour),
				LastStatus:        "completed",
			},
		},
	}
	fake.SetProjectsAndThreads(projects, threads)

	// 1. 主演示会话：多轮对话 + TodoWrite 清单 + 工具调用 + Go 代码块
	pagBase := now.Add(-15 * time.Minute)
	pagTodoInput := `{"todos":[{"content":"改用 (created_at, id) 复合游标并添加索引","status":"completed"},{"content":"运行单元测试验证同时间戳与末页边界","status":"completed"}]}`
	pagMsgs := []Message{
		{
			ID:        "pag-u1",
			Role:      "user",
			Kind:      "text",
			Text:      "订单列表数据越来越多，请改成游标分页，并保持结果稳定。",
			TurnIndex: 1,
			IsFinal:   true,
			CreatedAt: at(pagBase, 0),
		},
		{
			ID:         "pag-todo1",
			Role:       "tool",
			Kind:       "tool",
			ToolName:   "TodoWrite",
			ToolInput:  pagTodoInput,
			Text:       "TodoWrite " + pagTodoInput,
			TurnIndex:  1,
			ToolStatus: "completed",
			CreatedAt:  at(pagBase, 2*time.Second),
		},
		{
			ID:         "pag-read1",
			Role:       "tool",
			Kind:       "tool",
			ToolName:   "Read",
			ToolInput:  `{"file_path":"internal/order/repository.go","offset":42,"limit":60}`,
			Text:       "已读取 internal/order/repository.go 的 ListOrders 查询实现。",
			TurnIndex:  1,
			ToolStatus: "completed",
			CreatedAt:  at(pagBase, 4*time.Second),
		},
		{
			ID:         "pag-edit1",
			Role:       "tool",
			Kind:       "tool",
			ToolName:   "Edit",
			ToolInput:  `{"file_path":"internal/order/repository.go","old_string":"query.Offset(offset).Limit(limit)","new_string":"query.Where(\"(created_at, id) < (?, ?)\", cur.CreatedAt, cur.ID).Limit(limit + 1)"}`,
			Text:       "已将 ListOrders 改为复合游标分页并多取一条判定末页。",
			TurnIndex:  1,
			ToolStatus: "completed",
			CreatedAt:  at(pagBase, 9*time.Second),
		},
		{
			ID:         "pag-bash1",
			Role:       "tool",
			Kind:       "tool",
			ToolName:   "Bash",
			ToolInput:  `{"command":"go test ./internal/order/..."}`,
			Text:       "ok  \torbit-api/internal/order\t0.412s",
			TurnIndex:  1,
			ToolStatus: "completed",
			CreatedAt:  at(pagBase, 14*time.Second),
		},
		{
			ID:        "pag-a1",
			Role:      "assistant",
			Kind:      "text",
			TurnIndex: 1,
			IsFinal:   true,
			CreatedAt: at(pagBase, 16*time.Second),
			Text: "已完成游标分页改造，按 `(created_at DESC, id DESC)` 稳定排序：\n\n" +
				"```go\nfunc (r *OrderRepo) ListOrders(cur Cursor, limit int) ([]Order, string) {\n    return sliceCursorPage(r.db.QueryByCursor(cur.CreatedAt, cur.ID, limit+1), limit)\n}\n```",
		},
		{
			ID:        "pag-u2",
			Role:      "user",
			Kind:      "text",
			Text:      "如果两笔订单创建时间相同，翻页会重复吗？",
			TurnIndex: 2,
			IsFinal:   true,
			CreatedAt: at(pagBase, 10*time.Minute),
		},
		{
			ID:        "pag-a2",
			Role:      "assistant",
			Kind:      "text",
			Text:      "不会。游标包含 `created_at` 与主键 `id`，按 `(created_at, id) < (?, ?)` 严格衔接。",
			TurnIndex: 2,
			IsFinal:   true,
			CreatedAt: at(pagBase, 10*time.Minute+6*time.Second),
		},
	}
	fake.SetMessages(readmePaginationThreadID, pagMsgs)
	fake.SetThreadUsage(readmePaginationThreadID, ThreadUsage{
		Turns: map[int]TokenUsage{
			1: {Input: 9_800, Output: 2_600, Cached: 4_200, DurationMs: 16_000, CostUSD: 0.042},
			2: {Input: 4_600, Output: 1_400, Cached: 2_100, DurationMs: 6_000, CostUSD: 0.018},
		},
		Total:     TokenUsage{Input: 14_400, Output: 4_000, Cached: 6_300, DurationMs: 22_000, CostUSD: 0.060},
		Requests:  3,
		ToolCalls: 3,
	})

	// 2. 编排主会话 + 3 个子代理（Aria 已完成、Bram 已完成、Cora 运行中）
	swBase := now.Add(-12 * time.Minute)
	ariaStarted := swBase.Add(15 * time.Second)
	ariaFinished := swBase.Add(57 * time.Second)
	bramStarted := swBase.Add(20 * time.Second)
	bramFinished := swBase.Add(2*time.Minute + 4*time.Second)
	coraStarted := swBase.Add(25 * time.Second)

	ariaSummary := "已完成回调链路梳理：目前微信与支付宝回调分别在两处 handler 更新订单状态，建议收口到 `PaymentService.HandleCallback`，并在事务开始前校验请求签名。"
	bramSummary := "已为 `payment_events` 表添加 `(channel, event_id)` 唯一索引，并在事务内通过 `InsertEventIfNew` 去重后再流转订单状态，重复回调直接幂等返回。"

	reportText := strings.Join([]string{
		swarmReportPrefix + "Aria、Bram 已完成子任务",
		"",
		"## Aria：梳理支付回调与验签链路 (已完成)",
		"- 档位：只读 · 模型：Claude Sonnet 5.5",
		"- 改动文件：[]",
		`- 统计：{"durationMs":42000,"toolCalls":8,"linesAdded":0,"linesDeleted":0}`,
		"- 汇报总结：" + ariaSummary,
		"",
		"## Bram：实现基于事件 ID 的幂等去重 (已完成)",
		"- 档位：编辑 · 模型：GPT-5.3 Codex",
		`- 改动文件：["internal/payment/callback.go","internal/payment/event_repo.go"]`,
		`- 统计：{"durationMs":104000,"toolCalls":14,"linesAdded":68,"linesDeleted":19}`,
		"- 汇报总结：" + bramSummary,
	}, "\n")

	swMsgs := []Message{
		{
			ID:        "sw-u1",
			Role:      "user",
			Kind:      "text",
			Text:      "请重构支付回调，保证重复通知安全，并补齐并发与异常测试。",
			TurnIndex: 1,
			IsFinal:   true,
			CreatedAt: at(swBase, 0),
		},
		{
			ID:        "sw-a1",
			Role:      "assistant",
			Kind:      "text",
			Text:      "我将这次重构拆成三条并行子任务：由 Aria 梳理现有回调与验签入口，Bram 实现事件表幂等去重，Cora 补充并发重复通知与事务回滚测试。",
			TurnIndex: 1,
			CreatedAt: at(swBase, 10*time.Second),
		},
		{
			ID:        "sw-rep1",
			Role:      "user",
			Kind:      "text",
			Text:      reportText,
			TurnIndex: 2,
			IsFinal:   true,
			CreatedAt: at(swBase, 2*time.Minute+10*time.Second),
		},
		{
			ID:        "sw-a2",
			Role:      "assistant",
			Kind:      "text",
			Text:      "Aria 与 Bram 已完成各自子任务并提交汇报：\n\n- **回调入口收口**：统一路由至 `PaymentService.HandleCallback`，前置校验渠道签名\n- **事务幂等去重**：`payment_events` 建立 `(channel, event_id)` 唯一索引，重复通知安全返回\n\n目前 Cora 正在运行并发竞态与事务回滚测试，全部通过后即可合入主分支。",
			TurnIndex: 2,
			IsFinal:   true,
			CreatedAt: at(swBase, 2*time.Minute+22*time.Second),
		},
	}
	fake.SetMessages(readmeSwarmLeadThreadID, swMsgs)
	fake.SetThreadUsage(readmeSwarmLeadThreadID, ThreadUsage{
		Turns: map[int]TokenUsage{
			1: {Input: 10_200, Output: 2_800, DurationMs: 10_000},
			2: {Input: 8_800, Output: 2_800, DurationMs: 12_000},
		},
		Total:    TokenUsage{Input: 19_000, Output: 5_600, DurationMs: 22_000},
		Requests: 2,
	})

	swarmState := SwarmState{
		Available:    true,
		Enabled:      true,
		LeadThreadID: readmeSwarmLeadThreadID,
		Bees: []SwarmBee{
			{
				ThreadID:   readmeBeeAriaThreadID,
				Index:      1,
				Name:       "Aria",
				Task:       "梳理支付回调与验签链路",
				Status:     "completed",
				Summary:    ariaSummary,
				Provider:   "claude",
				Model:      "claude-sonnet-5-5",
				Tier:       "read",
				ReadOnly:   true,
				ToolCalls:  8,
				Tokens:     5_200,
				StartedAt:  ariaStarted,
				FinishedAt: &ariaFinished,
			},
			{
				ThreadID:     readmeBeeBramThreadID,
				Index:        2,
				Name:         "Bram",
				Task:         "实现基于事件 ID 的幂等去重",
				Status:       "completed",
				Summary:      bramSummary,
				Provider:     "codex",
				Model:        "gpt-5.3-codex",
				Tier:         "edit",
				ToolCalls:    14,
				Tokens:       8_700,
				ChangedFiles: []string{"internal/payment/callback.go", "internal/payment/event_repo.go"},
				StartedAt:    bramStarted,
				FinishedAt:   &bramFinished,
			},
			{
				ThreadID:  readmeBeeCoraThreadID,
				Index:     3,
				Name:      "Cora",
				Task:      "补充并发回调与回滚测试",
				Status:    "running",
				Activity:  "正在运行 go test -race ./internal/payment/... 验证并发幂等",
				Provider:  "claude",
				Model:     "claude-opus-5-5",
				Tier:      "edit",
				ToolCalls: 9,
				Tokens:    6_400,
				StartedAt: coraStarted,
			},
		},
	}
	fake.SetSwarmState(readmeSwarmLeadThreadID, swarmState)
	fake.SetSwarmState(readmeBeeAriaThreadID, SwarmState{Available: true, Enabled: true, LeadThreadID: readmeSwarmLeadThreadID, IsBee: true, Bees: swarmState.Bees})
	fake.SetSwarmState(readmeBeeBramThreadID, SwarmState{Available: true, Enabled: true, LeadThreadID: readmeSwarmLeadThreadID, IsBee: true, Bees: swarmState.Bees})
	fake.SetSwarmState(readmeBeeCoraThreadID, SwarmState{Available: true, Enabled: true, LeadThreadID: readmeSwarmLeadThreadID, IsBee: true, Bees: swarmState.Bees})

	// 3. 各子代理会话内容
	fake.SetMessages(readmeBeeAriaThreadID, []Message{
		{ID: "aria-u1", Role: "user", Kind: "text", Text: "梳理支付回调入口与验签逻辑，输出改造建议。", TurnIndex: 1, IsFinal: true, CreatedAt: at(ariaStarted, 0)},
		{ID: "aria-t1", Role: "tool", Kind: "tool", ToolName: "Read", ToolInput: `{"file_path":"internal/payment/callback.go"}`, Text: "已读取 internal/payment/callback.go。", TurnIndex: 1, ToolStatus: "completed", CreatedAt: at(ariaStarted, 8*time.Second)},
		{ID: "aria-a1", Role: "assistant", Kind: "text", Text: ariaSummary, TurnIndex: 1, IsFinal: true, CreatedAt: at(ariaFinished, 0)},
	})

	fake.SetMessages(readmeBeeBramThreadID, []Message{
		{ID: "bram-u1", Role: "user", Kind: "text", Text: "在支付回调事务内实现基于事件 ID 的幂等去重，确保重复通知不会重复扣款或二次发货。", TurnIndex: 1, IsFinal: true, CreatedAt: at(bramStarted, 0)},
		{ID: "bram-r1", Role: "assistant", Kind: "reasoning", Text: "先查看 payment_events 仓储与回调事务边界，再用唯一约束拦截并发重复事件。", TurnIndex: 1, CreatedAt: at(bramStarted, 6*time.Second)},
		{ID: "bram-t1", Role: "tool", Kind: "tool", ToolName: "Read", ToolInput: `{"file_path":"internal/payment/callback.go"}`, Text: "已定位 HandleCallback 的订单更新事务。", TurnIndex: 1, ToolStatus: "completed", CreatedAt: at(bramStarted, 14*time.Second)},
		{ID: "bram-t2", Role: "tool", Kind: "tool", ToolName: "Edit", ToolInput: `{"file_path":"internal/payment/event_repo.go","old_string":"return r.markOrderPaid(ctx, tx, evt)","new_string":"inserted, err := r.insertEventIfNew(ctx, tx, evt)\nif err != nil || !inserted {\n    return err\n}\nreturn r.markOrderPaid(ctx, tx, evt)"}`, Text: "已写入事务内事件幂等检查。", TurnIndex: 1, ToolStatus: "completed", CreatedAt: at(bramStarted, 36*time.Second)},
		{ID: "bram-t3", Role: "tool", Kind: "tool", ToolName: "Bash", ToolInput: `{"command":"go test ./internal/payment/..."}`, Text: "ok  \torbit-api/internal/payment\t0.538s", TurnIndex: 1, ToolStatus: "completed", CreatedAt: at(bramStarted, 58*time.Second)},
		{
			ID:        "bram-a1",
			Role:      "assistant",
			Kind:      "text",
			TurnIndex: 1,
			IsFinal:   true,
			CreatedAt: at(bramFinished, 0),
			Text: "已在事务内接入事件唯一键幂等去重：\n\n" +
				"```go\nfresh, err := s.events.InsertIfNew(ctx, tx, evt.ID)\nif err != nil || !fresh {\n    return err\n}\nreturn s.orders.MarkPaid(ctx, tx, evt.OrderID)\n```\n\n重复通知会在事务内直接安全拦截，`go test ./internal/payment/...` 全部通过。",
		},
	})
	fake.SetThreadUsage(readmeBeeBramThreadID, ThreadUsage{
		Turns: map[int]TokenUsage{
			1: {Input: 6_400, Output: 2_300, DurationMs: 104_000, CostUSD: 0.031},
		},
		Total:     TokenUsage{Input: 6_400, Output: 2_300, DurationMs: 104_000, CostUSD: 0.031},
		Requests:  2,
		ToolCalls: 3,
	})

	fake.SetMessages(readmeBeeCoraThreadID, []Message{
		{ID: "cora-u1", Role: "user", Kind: "text", Text: "补充并发重复通知、签名校验失败与事务回滚单元测试。", TurnIndex: 1, IsFinal: true, CreatedAt: at(coraStarted, 0)},
		{ID: "cora-t1", Role: "tool", Kind: "tool", ToolName: "Edit", ToolInput: `{"file_path":"internal/payment/callback_test.go"}`, Text: "已新增 TestHandleCallbackConcurrentIdempotency 与 TestHandleCallbackRollback。", TurnIndex: 1, ToolStatus: "completed", CreatedAt: at(coraStarted, 22*time.Second)},
		{ID: "cora-t2", Role: "tool", Kind: "tool", ToolName: "Bash", ToolInput: `{"command":"go test -race -count=3 ./internal/payment/..."}`, Text: "正在运行竞态并发测试…", TurnIndex: 1, ToolStatus: "running", CreatedAt: at(coraStarted, 34*time.Second)},
	})

	// 4. 其它会话消息（用于跨会话「分页」搜索命中）
	fake.SetMessages("th-lumen-pagination", []Message{
		{ID: "lumen-pag-u1", Role: "user", Kind: "text", Text: "订单表格接入游标分页，切换筛选条件时自动重置游标。", TurnIndex: 1, IsFinal: true, CreatedAt: at(now, -28*time.Minute)},
		{ID: "lumen-pag-a1", Role: "assistant", Kind: "text", Text: "已为订单表格接入游标分页，筛选条件变化时重置分页游标并保留骨架加载态。", TurnIndex: 1, IsFinal: true, CreatedAt: at(now, -25*time.Minute)},
	})
	fake.SetMessages("th-orbit-index", []Message{
		{ID: "idx-u1", Role: "user", Kind: "text", Text: "检查订单查询的组合索引，确保分页列表避免全表扫描。", TurnIndex: 1, IsFinal: true, CreatedAt: at(now, -20*time.Minute)},
		{ID: "idx-a1", Role: "assistant", Kind: "text", Text: "已添加 (status, created_at DESC, id DESC) 覆盖索引，游标分页查询执行计划稳定命中索引。", TurnIndex: 1, IsFinal: true, CreatedAt: at(now, -18*time.Minute)},
	})
	fake.SetMessages("th-pocket-search", []Message{
		{ID: "notes-u1", Role: "user", Kind: "text", Text: "笔记搜索结果较多时需要支持分页加载和关键词高亮。", TurnIndex: 1, IsFinal: true, CreatedAt: at(now, -3*time.Hour)},
		{ID: "notes-a1", Role: "assistant", Kind: "text", Text: "已为笔记全文搜索补上增量分页加载与关键词高亮。", TurnIndex: 1, IsFinal: true, CreatedAt: at(now, -3*time.Hour+2*time.Minute)},
	})

	return &readmeShowcaseClient{FakeClient: fake}
}

func resetReadmeShotSwarmGlobals() {
	resetSwarmAutoHideFor(readmeSwarmLeadThreadID)
	ResetBeeFolded()
	ResetBeeShowAll()
	resetSidebarHoverCardForTest()
	swarmFloatMu.Lock()
	delete(swarmAutoPopsOverride, readmeSwarmLeadThreadID)
	delete(swarmSelectedBeeID, readmeSwarmLeadThreadID)
	swarmFloatMu.Unlock()
}

func buildReadmeRoot(t *testing.T, client Client, initialThread string) (*chatState, func(*ui.Context)) {
	t.Helper()
	t.Cleanup(waitUIStateWrites)
	t.Cleanup(resetReadmeShotSwarmGlobals)
	resetReadmeShotSwarmGlobals()

	state, view := buildRoot(
		client,
		WithSettings(settingsTestDir(t), nil),
		WithInitialThread(initialThread),
	)
	t.Cleanup(state.close)
	setAppReduceMotion(t, true)
	return state, view
}

func assertReadmeHeroVisible(t *testing.T, view func(*ui.Context), dark bool) {
	t.Helper()
	setAppReduceMotion(t, true)
	tt := ui.NewTesterWith(view, WindowDefaultWidth, WindowDefaultHeight, ui.TesterOptions{
		Preferences: ui.Preferences{ReduceMotion: true},
		Dark:        dark,
	})
	for i := 0; i < 80; i++ {
		time.Sleep(12 * time.Millisecond)
		tt.Advance(50 * time.Millisecond)
		tt.Frame()
		if i >= 28 && !tt.FramePending() {
			break
		}
	}
	tt.SetFocused(false)
	if active, _ := tt.TextInputActive(); active {
		t.Fatalf("hero shot still has active text input focus")
	}
	r, ok := tt.Find("订单列表数据越来越多，请改成游标分页，并保持结果稳定。")
	if !ok {
		t.Fatalf("hero shot missing first user message")
	}
	if r.Y < 44 || r.H <= 0 {
		t.Fatalf("hero shot first user message clipped at top: rect=%+v", r)
	}
}

// TestReadmeShots 在设置 MCODE_README_SHOTS=1 时用 headless 光栅器生成 6 张 README 桌面端截图。
func TestReadmeShots(t *testing.T) {
	if os.Getenv("MCODE_README_SHOTS") != "1" {
		t.Skip("set MCODE_README_SHOTS=1 to regenerate README screenshots")
	}

	t.Run("hero-light", func(t *testing.T) {
		client := newReadmeShowcaseClient()
		state, view := buildReadmeRoot(t, client, readmePaginationThreadID)
		state.toggleTodoCard("pag-todo1", true)
		state.setToolGroupOpen(turnWorkFoldID("pag-u1"), true)
		writeReadmeShot(t, "hero-light", view, false)
		assertReadmeHeroVisible(t, view, false)
	})

	t.Run("hero-dark", func(t *testing.T) {
		client := newReadmeShowcaseClient()
		state, view := buildReadmeRoot(t, client, readmePaginationThreadID)
		state.toggleTodoCard("pag-todo1", true)
		state.setToolGroupOpen(turnWorkFoldID("pag-u1"), true)
		writeReadmeShot(t, "hero-dark", view, true)
		assertReadmeHeroVisible(t, view, true)
	})

	t.Run("swarm", func(t *testing.T) {
		client := newReadmeShowcaseClient()
		state, view := buildReadmeRoot(t, client, readmeSwarmLeadThreadID)
		SetBeeShowAll(readmeSwarmLeadThreadID, true)
		bees := client.swarmStates[readmeSwarmLeadThreadID].Bees
		dismissSwarmFloatPanel(readmeSwarmLeadThreadID, bees)
		hp := getSwarmPanelState(state)
		swarmPanelMu.Lock()
		if hp.reportOpen == nil {
			hp.reportOpen = make(map[string]bool)
		}
		hp.reportOpen["sw-rep1#1"] = true
		swarmPanelMu.Unlock()
		writeReadmeShot(t, "swarm", view, true)
	})

	t.Run("swarm-bee", func(t *testing.T) {
		client := newReadmeShowcaseClient()
		state, view := buildReadmeRoot(t, client, readmeBeeBramThreadID)
		SetBeeShowAll(readmeSwarmLeadThreadID, true)
		state.setToolGroupOpen(turnWorkFoldID("bram-u1"), true)
		writeReadmeShot(t, "swarm-bee", view, true)
	})

	t.Run("search", func(t *testing.T) {
		client := newReadmeShowcaseClient()
		state, view := buildReadmeRoot(t, client, readmePaginationThreadID)
		openPalette(state)
		p := getPaletteState(state)
		p.query = "分页"
		writeReadmeShot(t, "search", view, true)
		if p.msgQuery != "分页" || len(p.msgHits) < 2 {
			t.Fatalf("expected >= 2 message hits for %q, got msgQuery=%q hits=%d", "分页", p.msgQuery, len(p.msgHits))
		}
		_ = fmt.Sprintf("hits=%d", len(p.msgHits))
	})
}
