package mcodeui

import (
	"context"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// readmeTasksShowcaseClient 在 readmeShowcaseClient 基础上补充看板与自动化展示数据，
// 供 README 任务看板与自动化截图复用。
type readmeTasksShowcaseClient struct {
	*readmeShowcaseClient
}

func (c *readmeTasksShowcaseClient) PreviewAutomationSchedule(_ context.Context, sch AutomationSchedule, after time.Time, count int) (AutomationSchedulePreview, error) {
	if count <= 0 {
		count = 3
	}
	base := after.Local()
	times := make([]time.Time, 0, count)
	step := 24 * time.Hour
	if sch.Type == "weekly" {
		step = 7 * 24 * time.Hour
	} else if sch.Type == "interval" && sch.Seconds > 0 {
		step = time.Duration(sch.Seconds) * time.Second
	}
	for i := 1; i <= count; i++ {
		times = append(times, base.Add(time.Duration(i)*step))
	}
	return AutomationSchedulePreview{Times: times}, nil
}

func newReadmeTasksShowcaseClient() *readmeTasksShowcaseClient {
	base := newReadmeShowcaseClient()
	now := time.Now().Local()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	daysUntilFriday := (5 - int(today.Weekday()) + 7) % 7
	if daysUntilFriday == 0 {
		daysUntilFriday = 7
	}

	base.SetKanbanBoard(KanbanBoard{
		Revision: 12,
		Goals: []KanbanGoal{
			{
				ID:        "goal-orbit-v2",
				ProjectID: readmeOrbitProjectID,
				Title:     "订单分页与支付幂等",
				Status:    "running",
				Done:      2,
				Total:     5,
				Revision:  3,
				UpdatedAt: today.Add(9 * time.Hour),
			},
			{
				ID:        "goal-lumen-ux",
				ProjectID: readmeLumenProjectID,
				Title:     "结算页体验与深色适配",
				Status:    "running",
				Done:      1,
				Total:     3,
				Revision:  2,
				UpdatedAt: today.Add(8 * time.Hour),
			},
		},
		Tasks: []KanbanTask{
			{
				ID:        "task-orbit-export",
				ProjectID: readmeOrbitProjectID,
				GoalID:    "goal-orbit-v2",
				Title:     "导出订单 CSV 接口支持游标分批拉取",
				Priority:  "high",
				Status:    "todo",
				Column:    "todo",
				CanStart:  true,
				CanMove:   true,
				Revision:  1,
				UpdatedAt: today.Add(9*time.Hour + 20*time.Minute),
			},
			{
				ID:        "task-lumen-skeleton",
				ProjectID: readmeLumenProjectID,
				GoalID:    "goal-lumen-ux",
				Title:     "订单表格切换筛选条件时保留骨架屏",
				Priority:  "medium",
				Status:    "todo",
				Column:    "todo",
				ThreadID:  "th-lumen-pagination",
				CanStart:  true,
				CanMove:   true,
				Revision:  1,
				UpdatedAt: today.Add(8*time.Hour + 45*time.Minute),
			},
			{
				ID:        "task-pocket-shortcut",
				ProjectID: readmePocketProjectID,
				Title:     "支持多标签筛选与键盘上下导航",
				Priority:  "low",
				Status:    "todo",
				Column:    "todo",
				ThreadID:  "th-pocket-tags",
				CanStart:  true,
				CanMove:   true,
				Revision:  1,
				UpdatedAt: today.AddDate(0, 0, -1).Add(19 * time.Hour),
			},
			{
				ID:        "task-orbit-callback",
				ProjectID: readmeOrbitProjectID,
				GoalID:    "goal-orbit-v2",
				Origin:    "owner",
				Title:     "重构支付回调幂等去重",
				Priority:  "urgent",
				Status:    "in_progress",
				Column:    "in_progress",
				ThreadID:  readmeSwarmLeadThreadID,
				CanMove:   true,
				Revision:  4,
				UpdatedAt: today.Add(10*time.Hour + 15*time.Minute),
			},
			{
				ID:        "task-lumen-filter-state",
				ProjectID: readmeLumenProjectID,
				GoalID:    "goal-lumen-ux",
				Title:     "筛选条件变更时自动重置分页游标",
				Priority:  "high",
				Status:    "in_progress",
				Column:    "in_progress",
				ThreadID:  "th-lumen-pagination",
				CanMove:   true,
				Revision:  2,
				UpdatedAt: today.Add(9*time.Hour + 55*time.Minute),
			},
			{
				ID:        "task-pocket-sync",
				ProjectID: readmePocketProjectID,
				Title:     "离线笔记版本冲突合并保留双端修订",
				Priority:  "medium",
				Status:    "in_progress",
				Column:    "in_progress",
				ThreadID:  "th-pocket-sync",
				CanMove:   true,
				Revision:  2,
				UpdatedAt: today.Add(9*time.Hour + 40*time.Minute),
			},
			{
				ID:             "task-orbit-migration",
				ProjectID:      readmeOrbitProjectID,
				Origin:         "owner",
				Title:          "订单复合索引迁移审批",
				Priority:       "urgent",
				Status:         "needs_approval",
				Column:         "needs_you",
				ThreadID:       "th-orbit-index",
				AttentionFlags: []string{"awaiting_approval"},
				CanApprove:     true,
				Revision:       3,
				UpdatedAt:      today.Add(10*time.Hour + 5*time.Minute),
			},
			{
				ID:             "task-lumen-rounding",
				ProjectID:      readmeLumenProjectID,
				GoalID:         "goal-lumen-ux",
				Title:          "确认结算页优惠券分摊尾差归属规则",
				Priority:       "high",
				Status:         "waiting",
				Column:         "needs_you",
				WaitKind:       "human",
				ResultSummary:  "待确认尾差方案",
				ThreadID:       "th-lumen-checkout",
				AttentionFlags: []string{"awaiting_input"},
				Revision:       2,
				UpdatedAt:      today.Add(9*time.Hour + 50*time.Minute),
			},
			{
				ID:            "task-orbit-pagination",
				ProjectID:     readmeOrbitProjectID,
				GoalID:        "goal-orbit-v2",
				Title:         "订单列表接口接入复合游标分页",
				Priority:      "high",
				Status:        "review",
				Column:        "review",
				ThreadID:      readmePaginationThreadID,
				CanReview:     true,
				CanApprove:    true,
				ReviewSummary: "已通过 14 个单测与边界校验",
				Revision:      5,
				UpdatedAt:     today.Add(10*time.Hour + 12*time.Minute),
			},
			{
				ID:         "task-lumen-dark",
				ProjectID:  readmeLumenProjectID,
				GoalID:     "goal-lumen-ux",
				Title:      "登录页与表单控件深色模式对比度适配",
				Priority:   "medium",
				Status:     "review",
				Column:     "review",
				ThreadID:   "th-lumen-dark",
				CanReview:  true,
				CanApprove: true,
				Revision:   2,
				UpdatedAt:  today.Add(8*time.Hour + 10*time.Minute),
			},
			{
				ID:            "task-pocket-search",
				ProjectID:     readmePocketProjectID,
				Title:         "笔记全文检索支持增量分页与关键词高亮",
				Priority:      "medium",
				Status:        "done",
				Column:        "done",
				ThreadID:      "th-pocket-search",
				ResultSummary: "已合入主干",
				Revision:      3,
				UpdatedAt:     today.AddDate(0, 0, -1).Add(17 * time.Hour),
			},
			{
				ID:            "task-orbit-refund",
				ProjectID:     readmeOrbitProjectID,
				GoalID:        "goal-orbit-v2",
				Title:         "梳理退款状态机流转并补充守卫单测",
				Priority:      "low",
				Status:        "done",
				Column:        "done",
				ThreadID:      "th-orbit-refund",
				ResultSummary: "已补充 9 组迁移用例",
				Revision:      2,
				UpdatedAt:     today.AddDate(0, 0, -1).Add(15 * time.Hour),
			},
		},
	})

	autos := []Automation{
		{
			ID:                     "auto-deps-morning",
			ProjectID:              readmeOrbitProjectID,
			Name:                   "每日依赖安全与版本巡检",
			Prompt:                 "检查 go.mod 与关键间接依赖的安全公告及可升级版本，输出影响评估与建议升级清单。",
			Status:                 "active",
			LastRunStatus:          "succeeded",
			Schedule:               AutomationSchedule{Type: "daily", TimeOfDay: "08:30", Timezone: "Asia/Shanghai"},
			Enabled:                true,
			NextRunAt:              today.AddDate(0, 0, 1).Add(8*time.Hour + 30*time.Minute),
			LastRunAt:              today.Add(8*time.Hour + 30*time.Minute),
			TotalRunCount:          24,
			MaxConsecutiveFailures: 3,
			CatchUp:                true,
			Provider:               "claude",
			Model:                  "claude-sonnet-4-5",
			Effort:                 "medium",
			RuntimeMode:            "supervised",
			RunMode:                "standalone",
			Revision:               4,
		},
		{
			ID:                     "auto-nightly-regression",
			ProjectID:              readmeLumenProjectID,
			Origin:                 "steward",
			Name:                   "每晚核心回归测试",
			Prompt:                 "运行结算计算、游标分页表格单测与生产构建检查，失败时定位首次引入回归的提交。",
			Status:                 "active",
			LastRunStatus:          "failed",
			Schedule:               AutomationSchedule{Type: "daily", TimeOfDay: "23:00", Timezone: "Asia/Shanghai"},
			Enabled:                true,
			NextRunAt:              today.Add(23 * time.Hour),
			LastRunAt:              today.AddDate(0, 0, -1).Add(23 * time.Hour),
			TotalRunCount:          19,
			ConsecutiveFailures:    1,
			MaxConsecutiveFailures: 3,
			CatchUp:                true,
			Provider:               "codex",
			Model:                  "gpt-5.3-codex",
			RuntimeMode:            "supervised",
			RunMode:                "standalone",
			Revision:               3,
		},
		{
			ID:            "auto-weekly-pr",
			ProjectID:     readmeOrbitProjectID,
			Name:          "每周 PR 变更与发布摘要",
			Prompt:        "汇总本周已合入 PR、SQL 迁移脚本与待发布变更，生成面向团队的发布说明草稿。",
			Status:        "active",
			LastRunStatus: "succeeded",
			Schedule:      AutomationSchedule{Type: "weekly", DayOfWeek: 5, TimeOfDay: "17:30", Timezone: "Asia/Shanghai"},
			Enabled:       true,
			NextRunAt:     today.AddDate(0, 0, daysUntilFriday).Add(17*time.Hour + 30*time.Minute),
			LastRunAt:     today.AddDate(0, 0, -4).Add(17*time.Hour + 30*time.Minute),
			TotalRunCount: 8,
			CatchUp:       true,
			Provider:      "claude",
			Model:         "claude-opus-4-5",
			RuntimeMode:   "readonly",
			RunMode:       "standalone",
			Revision:      2,
		},
		{
			ID:            "auto-notes-index",
			ProjectID:     readmePocketProjectID,
			Name:          "索引碎片整理与快照核验",
			Prompt:        "校验本地 SQLite FTS 全文索引完整性，并比对最近一次冷备快照校验和。",
			Status:        "paused",
			LastRunStatus: "succeeded",
			Schedule:      AutomationSchedule{Type: "weekdays", TimeOfDay: "12:30", Timezone: "Asia/Shanghai"},
			Enabled:       false,
			LastRunAt:     today.AddDate(0, 0, -2).Add(12*time.Hour + 30*time.Minute),
			TotalRunCount: 14,
			Provider:      "claude",
			Model:         "claude-sonnet-4-5",
			RuntimeMode:   "supervised",
			RunMode:       "standalone",
			Revision:      2,
		},
	}
	base.SetAutomations(autos)

	base.SetAutomationRuns("auto-deps-morning", []AutomationRun{
		{
			ID:           "run-deps-today",
			AutomationID: "auto-deps-morning",
			ThreadID:     "th-orbit-index",
			Status:       "succeeded",
			Summary:      "扫描 48 个 Go 模块完成，发现 2 个次版本安全补丁可升级。",
			Trigger:      "scheduled",
			StartedAt:    today.Add(8*time.Hour + 30*time.Minute),
			FinishedAt:   today.Add(8*time.Hour + 31*time.Minute + 42*time.Second),
		},
		{
			ID:           "run-deps-yesterday",
			AutomationID: "auto-deps-morning",
			Status:       "failed",
			Error:        "拉取 proxy.golang.org 索引超时，等待下个周期自动重试。",
			Trigger:      "scheduled",
			StartedAt:    today.AddDate(0, 0, -1).Add(8*time.Hour + 30*time.Minute),
			FinishedAt:   today.AddDate(0, 0, -1).Add(8*time.Hour + 30*time.Minute + 38*time.Second),
		},
		{
			ID:           "run-deps-2d",
			AutomationID: "auto-deps-morning",
			ThreadID:     readmePaginationThreadID,
			Status:       "succeeded",
			Summary:      "全部直接与间接依赖均无高危漏洞，go.sum 校验一致。",
			Trigger:      "scheduled",
			StartedAt:    today.AddDate(0, 0, -2).Add(8*time.Hour + 30*time.Minute),
			FinishedAt:   today.AddDate(0, 0, -2).Add(8*time.Hour + 31*time.Minute + 18*time.Second),
		},
		{
			ID:           "run-deps-3d",
			AutomationID: "auto-deps-morning",
			Status:       "succeeded",
			Summary:      "核对 pgx v5.7.2 与 grpc-go 更新日志，无破坏性变更。",
			Trigger:      "catch_up",
			MissedCount:  1,
			StartedAt:    today.AddDate(0, 0, -3).Add(9*time.Hour + 5*time.Minute),
			FinishedAt:   today.AddDate(0, 0, -3).Add(9*time.Hour + 6*time.Minute + 24*time.Second),
		},
		{
			ID:           "run-deps-4d",
			AutomationID: "auto-deps-morning",
			Status:       "failed",
			Error:        "工作区存在未提交的 go.work 临时改动，已跳过自动比对。",
			Trigger:      "manual",
			StartedAt:    today.AddDate(0, 0, -4).Add(14*time.Hour + 12*time.Minute),
			FinishedAt:   today.AddDate(0, 0, -4).Add(14*time.Hour + 12*time.Minute + 51*time.Second),
		},
	})

	return &readmeTasksShowcaseClient{readmeShowcaseClient: base}
}

// writeReadmeShotWithSize 用指定宽高（DIP）、scale 2、ReduceMotion 渲染指定视图并写入 docs/images/<name>.png。
func writeReadmeShotWithSize(t *testing.T, name string, view func(*ui.Context), dark bool, width, height int) {
	t.Helper()
	setAppReduceMotion(t, true)
	tt := ui.NewTesterWith(view, width, height, ui.TesterOptions{
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
	wantW, wantH := width*2, height*2
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

// TestReadmeShotsTasks 在设置 MCODE_README_SHOTS=1 时生成看板与自动化桌面端截图。
func TestReadmeShotsTasks(t *testing.T) {
	if os.Getenv("MCODE_README_SHOTS") != "1" {
		t.Skip("set MCODE_README_SHOTS=1 to regenerate task & automation README screenshots")
	}

	t.Run("kanban", func(t *testing.T) {
		t.Cleanup(waitUIStateWrites)
		client := newReadmeTasksShowcaseClient()
		state, view := buildTasksRoot(
			client,
			WithTasksWindowMode("tasks"),
			WithSettings(settingsTestDir(t), nil),
		)
		t.Cleanup(state.close)
		writeReadmeShotWithSize(t, "kanban", view, false, 1760, 500)

		ks := getKanbanState(state)
		kanbanMu.Lock()
		taskCount := len(ks.board.Tasks)
		kanbanMu.Unlock()
		if taskCount < 8 {
			t.Fatalf("expected >= 8 kanban tasks loaded, got %d", taskCount)
		}
	})

	t.Run("automation", func(t *testing.T) {
		t.Cleanup(waitUIStateWrites)
		client := newReadmeTasksShowcaseClient()
		state, view := buildTasksRoot(
			client,
			WithTasksWindowTarget(taskCenterTarget{
				Kind:  "automation",
				ID:    "auto-deps-morning",
				Title: "每日依赖安全与版本巡检",
			}),
			WithSettings(settingsTestDir(t), nil),
		)
		t.Cleanup(state.close)
		writeReadmeShot(t, "automation", view, false)

		as := getAutomationState(state)
		automationMu.Lock()
		selectedID := as.selectedID
		runsCount := len(as.runs)
		automationMu.Unlock()
		if selectedID != "auto-deps-morning" || runsCount < 2 {
			t.Fatalf("expected selected automation with runs, got selectedID=%q runs=%d", selectedID, runsCount)
		}
	})
}
