package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestKanbanTaskSignals(t *testing.T) {
	cases := []struct {
		task KanbanTask
		want string
	}{
		{KanbanTask{Status: "todo", Column: "todo"}, ""},
		{KanbanTask{Status: "in_progress", Column: "in_progress"}, ""},
		{KanbanTask{Status: "waiting", Column: "in_progress", WaitKind: "children"}, "等待子任务"},
		{KanbanTask{Status: "needs_approval", Column: "needs_you"}, "需要审批"},
		{KanbanTask{Status: "review", Column: "review", AttentionFlags: []string{"needs_review"}}, ""},
		{KanbanTask{Status: "done", Column: "done"}, ""},
		{KanbanTask{Status: "failed", Column: "todo"}, "失败"},
		{KanbanTask{Status: "rejected", Column: "needs_you"}, "已驳回"},
		{KanbanTask{Status: "cancelled", Column: "done"}, "已取消"},
		{KanbanTask{Blocked: true, DependsOn: []string{"a", "b"}}, "依赖 2"},
		{KanbanTask{AttentionFlags: []string{"awaiting_input"}}, "需要回复"},
		{KanbanTask{AttentionFlags: []string{"awaiting_approval"}}, "需要审批"},
		{KanbanTask{AttentionFlags: []string{"failed"}}, "失败"},
		{KanbanTask{AttentionFlags: []string{"stuck"}}, "执行停滞"},
		{KanbanTask{AttentionFlags: []string{"needs_review"}}, "需要审阅"},
		{KanbanTask{AttentionFlags: []string{"unknown_flag"}}, ""},
		{KanbanTask{Status: "waiting", ResultSummary: "等待文件", WakeAt: "2026-11-10T10:00:00Z"}, "等待文件 · " + stewardWakeLocalTime("2026-11-10T10:00:00Z")},
		{KanbanTask{Status: "waiting", WaitKind: "human"}, "等待你的决定"},
		{KanbanTask{Status: "waiting", WaitKind: "machine"}, "等待条件满足"},
		{KanbanTask{Status: "done", ResultSummary: "结果摘要"}, "结果摘要"},
	}
	for _, tc := range cases {
		got, _ := kanbanTaskSignal(tc.task)
		if got != tc.want {
			t.Errorf("%+v: 信号 = %q，期望 %q", tc.task, got, tc.want)
		}
	}
	_, tone := kanbanTaskSignal(KanbanTask{Status: "cancelled", Column: "done"})
	if tone != "neutral" || kanbanSignalColor(nil, tone) == ThemeSuccess(nil) {
		t.Fatal("取消任务必须使用中性色")
	}
}

func TestKanbanCardProjectSignalAndPriority(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.projects = []Project{{ID: "p1", Name: "项目一"}}
	task := KanbanTask{ID: "t1", ProjectID: "p1", Title: "任务标题", Status: "todo", Column: "todo", Notes: "不显示的备注"}
	for _, mixed := range []bool{false, true} {
		ks := &kanbanState{scopeID: "p1"}
		if mixed {
			ks.scopeID = ""
		}
		tt := ui.NewTester(func(c *ui.Context) { renderTaskCard(c, s, ks, task, kanbanTaskCardState{}) }, 400, 200)
		_, hasSignal := tt.Find("kanban-task-signal-t1")
		if hasSignal != mixed || tt.HasText("项目一") != mixed || tt.HasText(task.Notes) {
			t.Fatalf("mixed=%v，文本：%v", mixed, tt.Texts())
		}
	}
	for priority, label := range map[string]string{"": "", "none": "", "urgent": "紧急", "high": "高", "medium": "中", "low": "低"} {
		task.Priority = priority
		ks := &kanbanState{scopeID: "p1"}
		tt := ui.NewTester(func(c *ui.Context) { renderTaskCard(c, s, ks, task, kanbanTaskCardState{}) }, 400, 200)
		if label != "" {
			if _, ok := tt.Find("优先级：" + label); !ok {
				t.Errorf("优先级 %s 缺少可访问名称", priority)
			}
		} else if strings.Contains(strings.Join(tt.Texts(), " "), "优先级") {
			t.Errorf("优先级 %s 不应显示", priority)
		}
	}
}

func TestKanbanWaitingCardDoesNotAnimate(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	ks := &kanbanState{projectID: "p1"}
	task := KanbanTask{ID: "waiting", Title: "等待任务", Status: "waiting", Column: "in_progress", WaitKind: "children"}
	tt := ui.NewTester(func(c *ui.Context) { renderTaskCard(c, s, ks, task, kanbanTaskCardState{}) }, 400, 200)
	if !tt.HasText("等待子任务") || tt.FramePending() {
		t.Fatalf("等待任务应显示静态信号：%v", tt.Texts())
	}
}

func TestKanbanCardKeyboardActions(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	ks := &kanbanState{projectID: "p1"}
	task := KanbanTask{ID: "key", Title: "键盘任务", Status: "todo", Column: "todo", CanStart: true}
	tt := ui.NewTester(func(c *ui.Context) { renderTaskCard(c, s, ks, task, kanbanTaskCardState{}) }, 400, 200)
	tt.Key(0, ui.KeyTab)
	if !tt.Focused("kanban-task-card-key") {
		t.Fatal("卡片应可用键盘聚焦")
	}
	if _, ok := tt.Find("kanban-task-key-start"); !ok {
		t.Fatal("聚焦卡片后应显示开始按钮")
	}
	tt.Key(0, ui.KeyTab)
	if !tt.Focused("kanban-task-key-start") {
		t.Fatal("开始按钮应可用键盘聚焦")
	}
}

func TestKanbanListRowProjectAndSignal(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.projects = []Project{{ID: "p1", Name: "项目一"}}
	ks := getKanbanState(s)
	task := KanbanTask{ID: "list", ProjectID: "p1", Title: "列表任务", Status: "waiting", Column: "in_progress", WaitKind: "children"}
	for _, mixed := range []bool{false, true} {
		ks.scopeID = "p1"
		if mixed {
			ks.scopeID = ""
		}
		tt := ui.NewTester(func(c *ui.Context) { renderKanbanListRow(c, s, task) }, 600, 100)
		if !tt.HasText("等待子任务") || tt.HasText("项目一") != mixed || tt.HasText("更新于") {
			t.Fatalf("mixed=%v，文本：%v", mixed, tt.Texts())
		}
	}
}
