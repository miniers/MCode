package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestTaskDetailDrawerSplitAndSheet(t *testing.T) {
	t.Run("宽屏分栏支持拖拽与键盘调宽", func(t *testing.T) {
		width := float32(480)
		committed := float32(0)
		tt := ui.NewTester(func(c *ui.Context) {
			renderTaskDetailDrawerSplit(c, taskDetailDrawerSplitOpts{
				AvailableWidth: 1219,
				PreferredWidth: width,
				SplitKey:       "test-split",
				BoardKey:       "test-board",
				HandleKey:      "test-handle",
				PaneKey:        "test-pane",
				OnWidthChange:  func(w float32) { width = w },
				OnWidthCommit:  func(w float32) { committed = w },
				Board:          func(float32) { ui.Text(c, "board") },
				Detail:         func() { ui.Text(c, "detail") },
			})
		}, 1219, 700)
		tt.Frame()
		if !tt.HasText("board") || !tt.HasText("detail") {
			t.Fatalf("宽屏应同时渲染主区与详情: %v", tt.Texts())
		}
		r, ok := tt.Find("调整详情宽度")
		if !ok {
			t.Fatal("找不到详情分隔手柄")
		}
		x, y := r.X+r.W/2, r.Y+r.H/2
		tt.Press(x, y)
		tt.Move(x-40, y)
		tt.Release(x-40, y)
		if width != 520 || committed != 520 {
			t.Fatalf("拖拽后宽度异常: width=%v committed=%v", width, committed)
		}
		tt.Key(0, ui.KeyLeft)
		if width != 528 || committed != 528 {
			t.Fatalf("键盘左键调整后宽度异常: width=%v committed=%v", width, committed)
		}
	})

	t.Run("窄屏不足时仅渲染主区且Sheet支持背景点击与Esc关闭", func(t *testing.T) {
		closed := 0
		tt := ui.NewTester(func(c *ui.Context) {
			renderTaskDetailDrawerSplit(c, taskDetailDrawerSplitOpts{
				AvailableWidth: 640,
				PreferredWidth: 480,
				Board:          func(float32) { ui.Text(c, "board-only") },
				Detail:         func() { ui.Text(c, "detail-hidden") },
			})
			renderTaskDetailDrawerSheet(c, taskDetailDrawerSheetOpts{
				SheetKey: "test-sheet",
				OnClose:  func() { closed++ },
				Content:  func() { ui.Text(c, "sheet-body") },
			})
		}, 640, 480)
		tt.Frame()
		if !tt.HasText("board-only") || tt.HasText("detail-hidden") || !tt.HasText("sheet-body") {
			t.Fatalf("窄屏渲染不符合预期: %v", tt.Texts())
		}
		tt.Key(0, ui.KeyEscape)
		if closed != 1 {
			t.Fatalf("按 Esc 应触发 OnClose 一次，实际 %d", closed)
		}
		tt.ClickAt(4, 4)
		if closed != 2 {
			t.Fatalf("点击遮罩背景应触发 OnClose，实际 %d", closed)
		}
	})
}

func TestTaskDetailDrawerTaskHeaderAndEmptyState(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.projects = []Project{{ID: "p1", Name: "核心项目"}}
	s.tasksWindowView = "tasks"
	var openedThread string
	s.revealThreadInMainFn = func(id string) {
		openedThread = id
	}

	ks := getKanbanState(s)
	task := KanbanTask{
		ID:        "task-steward-1",
		ProjectID: "p1",
		Title:     "总管派发任务",
		Origin:    "owner",
		Status:    "in_progress",
		Column:    "in_progress",
		ThreadID:  "thread-steward-1",
	}
	ks.board.Tasks = []KanbanTask{task}
	ks.detailTaskID = task.ID

	tt := ui.NewTester(func(c *ui.Context) { renderTaskDetailContent(c, s, ks) }, 900, 700)
	tt.Frame()
	for _, want := range []string{"总管", "进行中", "核心项目", "打开会话", "总管派发任务"} {
		if !tt.HasText(want) {
			t.Fatalf("详情顶栏缺少 %q: %v", want, tt.Texts())
		}
	}
	if err := tt.Click("打开会话"); err != nil {
		t.Fatalf("点击打开会话失败: %v", err)
	}
	if openedThread != task.ThreadID {
		t.Fatalf("打开会话触发的 threadID = %q, want %q", openedThread, task.ThreadID)
	}

	ks.detailTaskID = "missing-task"
	tt.Frame()
	if !tt.HasText("任务已不存在") {
		t.Fatalf("任务不存在时应展示空态文案: %v", tt.Texts())
	}
}

func TestTaskDetailDrawerGoalEmptyAndBadge(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.projects = []Project{{ID: "p1", Name: "项目一"}}
	ks := getKanbanState(s)
	ks.goalID = "missing-goal"
	ks.goalDetailOpen = true

	tt := ui.NewTester(func(c *ui.Context) { renderGoalDetailContent(c, s, ks) }, 840, 520)
	tt.Frame()
	if !tt.HasText("目标已不存在") {
		t.Fatalf("目标不存在时应展示空态文案: %v", tt.Texts())
	}

	ks.board.Goals = []KanbanGoal{{
		ID:        "goal-1",
		ProjectID: "p1",
		Title:     "Phase 3A 目标",
		Status:    "plan_review",
	}}
	ks.goalID = "goal-1"
	tt.Frame()
	if !tt.HasText("方案待审") || !tt.HasText("Phase 3A 目标") {
		t.Fatalf("目标详情未显示状态译名或标题: %v", tt.Texts())
	}
	if _, ok := tt.Find("kanban-goal-detail-header-status"); !ok {
		t.Fatal("目标详情顶部缺少带语义色的状态徽标")
	}
}
