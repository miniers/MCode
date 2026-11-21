package mcodeui

import (
	"fmt"
	"sort"
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestNarrowLayoutP1SidebarFooter240(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	setChatSettings(s, NewSettingsState(settingsTestDir(t), nil, s))
	s.mu.Lock()
	s.sidebarWidth = SidebarMinWidth
	s.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) { renderSidebarBottomBar(c, s) }, int(SidebarMinWidth), 100)
	tt.Frame()

	labels := []string{"添加项目", "任务", "自动化", "设置"}
	rects := make([]ui.Rect, 0, len(labels))
	for _, label := range labels {
		info, ok := tt.AccessByRole(ui.RoleButton, label)
		if !ok {
			t.Fatalf("240px 侧栏底栏缺少按钮 %q，texts=%q", label, tt.Texts())
		}
		r := info.Bounds
		if r.W != ControlSM || r.H != ControlSM {
			t.Fatalf("240px 紧凑底栏按钮 %q 应为 ControlSM(%v) 图标按钮: %+v", label, ControlSM, r)
		}
		if r.X < -0.5 || r.X+r.W > SidebarMinWidth+0.5 {
			t.Fatalf("按钮 %q 越出 240px 边界: %+v", label, r)
		}
		rects = append(rects, r)
	}
	sort.Slice(rects, func(i, j int) bool { return rects[i].X < rects[j].X })
	for i := 1; i < len(rects); i++ {
		if rects[i-1].X+rects[i-1].W > rects[i].X+0.5 {
			t.Fatalf("底栏按钮重叠: prev=%+v cur=%+v", rects[i-1], rects[i])
		}
	}
}

func TestNarrowLayoutP1TasksSidebarGoalsScrollAndButtons(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	ks := getKanbanState(s)

	goals := make([]KanbanGoal, 0, 12)
	for i := 0; i < 12; i++ {
		status := "running"
		if i == 0 {
			status = "plan_review"
		}
		goals = append(goals, KanbanGoal{
			ID:     fmt.Sprintf("g-%d", i+1),
			Title:  fmt.Sprintf("目标 %d 用于验证窄高滚动与按钮拆分", i+1),
			Status: status,
		})
	}
	props := kanbanModalBodyProps{
		destination: "tasks",
		board:       KanbanBoard{Goals: goals},
	}

	tt := ui.NewTester(func(c *ui.Context) {
		renderTasksWindowSidebar(c, s, ks, props)
	}, int(TasksSidebarWidth), 400)
	tt.Frame()

	scrollRect, ok := tt.Find("tasks-sidebar-goals-scroll")
	if !ok {
		t.Fatal("缺少 tasks-sidebar-goals-scroll 滚动容器")
	}
	if scrollRect.H <= 0 || scrollRect.Y+scrollRect.H > 400+0.5 {
		t.Fatalf("tasks-sidebar-goals-scroll 布局异常: %+v", scrollRect)
	}

	confirmLabel := "确认目标计划 " + goals[0].Title
	confirmInfo, ok := tt.AccessByRole(ui.RoleButton, confirmLabel)
	if !ok {
		t.Fatalf("未找到独立 RoleButton %q", confirmLabel)
	}
	if confirmInfo.Bounds.W < ControlSM || confirmInfo.Bounds.H < ControlSM {
		t.Fatalf("确认目标计划按钮尺寸小于 ControlSM(%v): %+v", ControlSM, confirmInfo.Bounds)
	}
	rowInfo, ok := tt.AccessByRole(ui.RoleButton, goals[0].Title)
	if !ok {
		t.Fatalf("未找到目标行按钮 %q", goals[0].Title)
	}
	if rowInfo.Bounds.X+rowInfo.Bounds.W > confirmInfo.Bounds.X+0.5 {
		t.Fatalf("目标行按钮与确认按钮重叠或嵌套: row=%+v confirm=%+v", rowInfo.Bounds, confirmInfo.Bounds)
	}

	if err := tt.Click(confirmLabel); err != nil {
		t.Fatalf("点击 %q 失败: %v", confirmLabel, err)
	}
	tt.Frame()
	kanbanMu.Lock()
	gotGoalID := ks.goalID
	gotDetailOpen := ks.goalDetailOpen
	kanbanMu.Unlock()
	if gotGoalID != goals[0].ID || !gotDetailOpen {
		t.Fatalf("点击确认目标计划未打开目标详情: goalID=%q detailOpen=%v", gotGoalID, gotDetailOpen)
	}
}

func TestNarrowLayoutP1ChatHeaderNarrow(t *testing.T) {
	testenv.TempHome(t)
	stubTerminals(t)

	if got := headerCollapseLevel(760); got != 0 {
		t.Fatalf("headerCollapseLevel(760) = %d, want 0", got)
	}
	if got := headerCollapseLevel(759); got != 1 {
		t.Fatalf("headerCollapseLevel(759) = %d, want 1", got)
	}
	if got := headerCollapseLevel(560); got != 1 {
		t.Fatalf("headerCollapseLevel(560) = %d, want 1", got)
	}
	if got := headerCollapseLevel(559); got != 2 {
		t.Fatalf("headerCollapseLevel(559) = %d, want 2", got)
	}

	fake := newGitFakeClient()
	s := newChatState(fake)
	t.Cleanup(func() {
		closeTerminals(s)
		s.close()
	})
	setChatSettings(s, NewSettingsState(settingsTestDir(t), nil, s))

	const threadTitle = "窄窗聊天标题验证测试"
	s.mu.Lock()
	s.sidebarCollapsed = true
	s.selectedThreadID = "t1"
	s.threads = []Thread{{
		ID:          "t1",
		ProjectID:   "p1",
		Title:       threadTitle,
		RuntimeMode: "auto",
	}}
	s.mu.Unlock()

	const narrowW = 460
	tt := ui.NewTester(func(c *ui.Context) {
		renderChatHeader(c, s, "t1", ThreadUsage{}, 0)
	}, narrowW, 320)
	waitFor(t, tt, func() bool {
		_, hasMore := tt.Find("更多头部操作")
		_, hasCommit := tt.Find("提交与推送")
		return hasMore && hasCommit && tt.HasText(threadTitle)
	})

	titleRect, ok := tt.Find("会话标题拖动区")
	if !ok {
		t.Fatal("窄窗下缺少会话标题拖动区")
	}
	if titleRect.W < 40 || titleRect.X < -0.5 || titleRect.X+titleRect.W > narrowW+0.5 {
		t.Fatalf("窄窗下标题区宽度或边界异常: %+v", titleRect)
	}
	moreRect, ok := tt.Find("更多头部操作")
	if !ok {
		t.Fatal("窄窗下缺少「更多头部操作」按钮")
	}
	if moreRect.W <= 0 || moreRect.X < -0.5 || moreRect.X+moreRect.W > narrowW+0.5 {
		t.Fatalf("窄窗下「更多头部操作」按钮越界: %+v", moreRect)
	}
	if _, ok := tt.Find(headerPermissionCapsuleLabel); ok {
		t.Fatal("窄窗折叠级别下权限胶囊不应直接留在顶栏")
	}
	if _, ok := tt.Find("Git 分支"); ok {
		t.Fatal("窄窗折叠级别下分支胶囊不应直接留在顶栏")
	}

	if err := tt.Click("更多头部操作"); err != nil {
		t.Fatalf("打开「更多头部操作」失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find(headerPermissionCapsuleLabel)
		return ok
	})
	for _, label := range []string{headerPermissionCapsuleLabel, "Git 分支", "终端", "右栏", "更多工具"} {
		if _, ok := tt.AccessByRole(ui.RoleButton, label); !ok {
			t.Fatalf("打开「更多头部操作」后缺少折叠项 %q", label)
		}
	}
}
