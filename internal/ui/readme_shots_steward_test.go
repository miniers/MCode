package mcodeui

import (
	"context"
	"os"
	"testing"
	"time"
)

const (
	readmeStewardProjectID = "steward-home"
	readmeStewardThreadID  = "th-steward-moco"
)

// readmeStewardClient 在 readmeShowcaseClient 基础上补充总管接口、身份与未答决策数，
// 用于渲染 README 的「MoCo · 总管」宣传截图。
type readmeStewardClient struct {
	*readmeShowcaseClient
	stewardThreadID string
	nickname        string
	avatar          string
	decisionCount   int
}

var (
	_ StewardClient              = (*readmeStewardClient)(nil)
	_ stewardEnabledClient       = (*readmeStewardClient)(nil)
	_ stewardIdentityClient      = (*readmeStewardClient)(nil)
	_ stewardDecisionCountClient = (*readmeStewardClient)(nil)
)

func (c *readmeStewardClient) StewardThreadID() string {
	return c.stewardThreadID
}

func (c *readmeStewardClient) OpenStewardThread(_ context.Context) (string, error) {
	return c.stewardThreadID, nil
}

func (c *readmeStewardClient) ResetStewardThread(_ context.Context) (string, error) {
	return c.stewardThreadID, nil
}

func (c *readmeStewardClient) StewardEnabled() bool {
	return true
}

func (c *readmeStewardClient) StewardIdentity() (string, string) {
	return c.nickname, c.avatar
}

func (c *readmeStewardClient) StewardOpenDecisionCount(_ context.Context) (int, error) {
	return c.decisionCount, nil
}

// GitState 在总管主会话中不绑定单个业务仓库分支，避免头部出现错位的分支胶囊。
func (c *readmeStewardClient) GitState(ctx context.Context, threadID string) (GitState, error) {
	if threadID == c.stewardThreadID {
		return GitState{}, nil
	}
	return c.readmeShowcaseClient.GitState(ctx, threadID)
}

func newReadmeStewardClient() *readmeStewardClient {
	base := newReadmeShowcaseClient()
	now := time.Now().UTC().Truncate(time.Minute)
	at := func(offset time.Duration) string {
		return now.Add(offset).Format(time.RFC3339)
	}

	base.mu.Lock()
	base.projects = append(base.projects, Project{
		ID:   readmeStewardProjectID,
		Name: "MoCo · 总管",
		Path: "/Users/alex/.mcode/steward",
	})
	base.threads[readmeStewardProjectID] = []Thread{
		{
			ID:                readmeStewardThreadID,
			ProjectID:         readmeStewardProjectID,
			Title:             "MoCo · 总管",
			Provider:          "claude",
			Model:             "claude-opus-4-5",
			UpdatedAt:         now,
			LastUserMessageAt: now.Add(-8 * time.Minute),
			LastStatus:        "completed",
			ContextUsedTokens: 12_800,
			ContextWindow:     200_000,
		},
	}
	base.mu.Unlock()

	const decisionJSON = `{"id":"D-042","task":"lumen-web · 订单分页与异步导出","task_id":"task-lumen-export","thread":"th-lumen-pagination","question":"全量导出超过 5 万行时同步接口会超时，lumen-web 改用哪种方案？","options":[{"key":"A","text":"后台异步生成 CSV 并轮询下载进度"},{"key":"B","text":"限制单次导出 1 万条并提示按时间分段"}],"recommended":"A"}`

	base.SetMessages(readmeStewardThreadID, []Message{
		{
			ID:        "steward-u1",
			Role:      "user",
			Kind:      "text",
			Text:      "把三个项目的订单游标分页与检索体验统一拉齐：orbit-api 补齐导出接口，lumen-web 接入分页和导出，pocket-notes 跟进检索高亮。",
			TurnIndex: 1,
			IsFinal:   true,
			CreatedAt: at(-8 * time.Minute),
		},
		{
			ID:        "steward-r1",
			Role:      "assistant",
			Kind:      stewardReplyKind,
			TurnIndex: 1,
			IsFinal:   true,
			CreatedAt: at(-7 * time.Minute),
			Text: "已按三个项目拆成 4 项跨项目任务并分派执行：\n" +
				"1. orbit-api：补齐订单游标导出接口与状态筛选（已完成）\n" +
				"2. lumen-web：订单表格接入游标分页并加异步导出（待你确认导出策略）\n" +
				"3. lumen-web：校验导出空态与权限拦截（待办）\n" +
				"4. pocket-notes：全文检索补齐关键词高亮与增量翻页（进行中）",
		},
		{
			ID:        "steward-d1",
			Role:      "assistant",
			Kind:      stewardDecisionKind,
			Text:      decisionJSON,
			TurnIndex: 1,
			IsFinal:   true,
			CreatedAt: at(-3 * time.Minute),
		},
	})
	base.SetThreadUsage(readmeStewardThreadID, ThreadUsage{
		Turns: map[int]TokenUsage{
			1: {Input: 9_600, Output: 3_200, Cached: 3_800, DurationMs: 8_400, CostUSD: 0.036},
		},
		Total:     TokenUsage{Input: 9_600, Output: 3_200, Cached: 3_800, DurationMs: 8_400, CostUSD: 0.036},
		Requests:  2,
		ToolCalls: 0,
	})

	base.SetKanbanBoard(KanbanBoard{
		Revision: 4,
		Tasks: []KanbanTask{
			{
				ID:              "task-lumen-export",
				Origin:          "owner",
				SourceMessageID: "steward-u1",
				ProjectID:       readmeLumenProjectID,
				ThreadID:        "th-lumen-pagination",
				Title:           "lumen-web · 订单分页与异步导出",
				Status:          "waiting",
				WaitKind:        "human",
				Column:          "needs_you",
				Priority:        "high",
				UpdatedAt:       now.Add(-3 * time.Minute),
			},
			{
				ID:              "task-pocket-highlight",
				Origin:          "owner",
				SourceMessageID: "steward-u1",
				ProjectID:       readmePocketProjectID,
				ThreadID:        "th-pocket-search",
				Title:           "pocket-notes · 检索结果高亮与翻页",
				Status:          "in_progress",
				Column:          "in_progress",
				LastActivity:    "正在补充关键词高亮边界单测",
				Priority:        "medium",
				UpdatedAt:       now.Add(-2 * time.Minute),
			},
			{
				ID:              "task-lumen-empty",
				Origin:          "owner",
				SourceMessageID: "steward-u1",
				ProjectID:       readmeLumenProjectID,
				ThreadID:        "th-lumen-checkout",
				Title:           "lumen-web · 导出空态与权限校验",
				Status:          "todo",
				Column:          "todo",
				LastActivity:    "待导出策略确认后启动",
				Priority:        "medium",
				UpdatedAt:       now.Add(-5 * time.Minute),
			},
			{
				ID:              "task-orbit-export",
				Origin:          "owner",
				SourceMessageID: "steward-u1",
				ProjectID:       readmeOrbitProjectID,
				ThreadID:        readmePaginationThreadID,
				Title:           "orbit-api · 补齐游标导出接口与索引",
				Status:          "done",
				Column:          "done",
				ResultSummary:   "已通过并发分页与边界单元测试",
				Priority:        "high",
				UpdatedAt:       now.Add(-4 * time.Minute),
			},
		},
	})

	return &readmeStewardClient{
		readmeShowcaseClient: base,
		stewardThreadID:      readmeStewardThreadID,
		nickname:             "MoCo",
		avatar:               "M",
		decisionCount:        1,
	}
}

// TestReadmeShotsSteward 在设置 MCODE_README_SHOTS=1 时生成浅色主题的 MoCo · 总管 README 截图。
func TestReadmeShotsSteward(t *testing.T) {
	if os.Getenv("MCODE_README_SHOTS") != "1" {
		t.Skip("set MCODE_README_SHOTS=1 to regenerate README steward screenshot")
	}

	client := newReadmeStewardClient()
	state, view := buildReadmeRoot(t, client, readmeStewardThreadID)
	state.mu.Lock()
	state.stewardBoard.doneOpen = true
	state.mu.Unlock()
	writeReadmeShot(t, "steward", view, false)
}
