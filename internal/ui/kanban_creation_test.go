package mcodeui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type createRequest struct {
	project, title, notes string
	reply                 chan error
}
type creationClient struct {
	*FakeClient
	tasks, goals chan createRequest
}

func (c *creationClient) CreateTask(ctx context.Context, project, title, notes string) (KanbanTask, error) {
	r := createRequest{project, title, notes, make(chan error, 1)}
	c.tasks <- r
	err := <-r.reply
	return KanbanTask{}, err
}
func (c *creationClient) CreateGoal(ctx context.Context, project, prompt string) (KanbanGoal, error) {
	r := createRequest{project, prompt, "", make(chan error, 1)}
	c.goals <- r
	err := <-r.reply
	return KanbanGoal{}, err
}
func nextCreate(t *testing.T, ch <-chan createRequest) createRequest {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("未收到创建请求")
		return createRequest{}
	}
}
func creationSetup() (*chatState, *kanbanState, *creationClient) {
	c := &creationClient{FakeClient: NewFakeClient(), tasks: make(chan createRequest, 4), goals: make(chan createRequest, 4)}
	s := newChatState(c)
	ks := getKanbanState(s)
	ks.projectID = "p1"
	ks.scopeID = "p1"
	ks.newTaskProjectID = "p1"
	ks.newGoalProjectID = "p1"
	return s, ks, c
}
func TestKanbanCreation_FailureRetry(t *testing.T) {
	s, ks, c := creationSetup()
	ks.showNewTask = true
	ks.newTaskTitle = "draft"
	submitKanbanNewTask(s)
	r := nextCreate(t, c.tasks)
	r.reply <- errors.New("failed")
	awaitKanban(t, s, func() bool { return !ks.newTaskBusy })
	if !ks.showNewTask || ks.newTaskTitle != "draft" || ks.newTaskErr == "" {
		t.Fatal("失败丢失草稿或错误")
	}
	submitKanbanNewTask(s)
	r = nextCreate(t, c.tasks)
	r.reply <- nil
	awaitKanban(t, s, func() bool { return !ks.newTaskBusy })
	if ks.showNewTask || ks.newTaskTitle != "" {
		t.Fatal("重试成功未清理")
	}
	ks.showNewGoal = true
	ks.newGoalPrompt = "goal"
	submitKanbanNewGoal(s)
	g := nextCreate(t, c.goals)
	g.reply <- errors.New("failed")
	awaitKanban(t, s, func() bool { return !ks.newGoalBusy })
	if !ks.showNewGoal || ks.newGoalPrompt != "goal" || ks.newGoalErr == "" {
		t.Fatal("目标失败丢失草稿")
	}
	submitKanbanNewGoal(s)
	g = nextCreate(t, c.goals)
	g.reply <- nil
	awaitKanban(t, s, func() bool { return !ks.newGoalBusy })
	if ks.showNewGoal {
		t.Fatal("目标重试未关闭")
	}
}
func TestKanbanCreation_DoubleSubmit(t *testing.T) {
	s, ks, c := creationSetup()
	ks.newTaskTitle = "draft"
	submitKanbanNewTask(s)
	r := nextCreate(t, c.tasks)
	submitKanbanNewTask(s)
	select {
	case <-c.tasks:
		t.Fatal("重复创建")
	default:
	}
	r.reply <- nil
	awaitKanban(t, s, func() bool { return !ks.newTaskBusy })
	ks.newGoalPrompt = "goal"
	submitKanbanNewGoal(s)
	g := nextCreate(t, c.goals)
	submitKanbanNewGoal(s)
	select {
	case <-c.goals:
		t.Fatal("重复创建目标")
	default:
	}
	g.reply <- nil
	awaitKanban(t, s, func() bool { return !ks.newGoalBusy })
}
func TestKanbanCreation_ProjectIsolation(t *testing.T) {
	s, ks, c := creationSetup()
	ks.newTaskProjectID = "p2"
	ks.newGoalProjectID = "p3"
	ks.newTaskTitle = "draft"
	ks.newGoalPrompt = "goal"
	submitKanbanNewTask(s)
	submitKanbanNewGoal(s)
	r := nextCreate(t, c.tasks)
	g := nextCreate(t, c.goals)
	if ks.projectID != "p1" || r.project != "p2" || g.project != "p3" {
		t.Fatal("表单项目污染范围")
	}
	r.reply <- nil
	g.reply <- nil
	awaitKanban(t, s, func() bool { return !ks.newTaskBusy && !ks.newGoalBusy })
}
func TestKanbanCreation_OldResponseKeepsNewDraft(t *testing.T) {
	s, ks, c := creationSetup()
	ks.showNewTask = true
	ks.newTaskTitle = "old"
	submitKanbanNewTask(s)
	r := nextCreate(t, c.tasks)
	s.syncKanbanProject("p2")
	defer stopKanbanWatch(s)
	ks.newTaskTitle = "new"
	ks.newTaskProjectID = "p2"
	r.reply <- nil
	awaitKanban(t, s, func() bool { return !ks.newTaskBusy })
	if ks.newTaskTitle != "new" || !ks.showNewTask {
		t.Fatal("旧响应清空新草稿")
	}
}
func TestKanbanCreation_TitleFromNotes(t *testing.T) {
	s, ks, c := creationSetup()
	ks.newTaskNotes = "首行需求\n更多内容"
	submitKanbanNewTask(s)
	r := nextCreate(t, c.tasks)
	if r.title != "首行需求" || r.notes != ks.newTaskNotes {
		t.Fatal("标题未从首行生成")
	}
	r.reply <- nil
	awaitKanban(t, s, func() bool { return !ks.newTaskBusy })
}
func TestKanbanCreation_QuickAddContinuous(t *testing.T) {
	s, ks, c := creationSetup()
	ks.quickAddOpen = true
	ks.quickAddText = "one"
	s.submitQuickAdd()
	r := nextCreate(t, c.tasks)
	s.submitQuickAdd()
	select {
	case <-c.tasks:
		t.Fatal("重复快速新建")
	default:
	}
	r.reply <- nil
	awaitKanban(t, s, func() bool { return ks.quickAddText == "" })
	if !ks.quickAddOpen {
		t.Fatal("输入框被关闭")
	}
	ks.quickAddText = "two"
	s.submitQuickAdd()
	r = nextCreate(t, c.tasks)
	if r.title != "two" {
		t.Fatal("第二次创建失败")
	}
	r.reply <- nil
	awaitKanban(t, s, func() bool { return ks.quickAddText == "" })
}

func TestKanbanQuickAddUsesSelectedScope(t *testing.T) {
	s, ks, client := creationSetup()
	s.setKanbanScope("p2", false)
	defer stopKanbanWatch(s)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() { renderTodoQuickAdd(c, s, ks) })
	}, 840, 520)
	tt.Frame()
	if err := tt.Click("kanban-quick-add-toggle"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Type("项目 B 的任务")
	tt.Key(0, ui.KeyEnter)
	r := nextCreate(t, client.tasks)
	if r.project != "p2" || r.title != "项目 B 的任务" {
		t.Fatalf("创建请求错误: %+v", r)
	}
	r.reply <- nil
	awaitKanban(t, s, func() bool { return ks.quickAddText == "" })
}

func TestKanbanQuickAddAllProjectsRequiresSelection(t *testing.T) {
	s, ks, client := creationSetup()
	s.projects = []Project{{ID: "p1", Name: "项目 A", Path: "/a"}, {ID: "p2", Name: "项目 B", Path: "/b"}}
	s.setKanbanScope("", true)
	defer stopKanbanWatch(s)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() { renderTodoQuickAdd(c, s, ks) })
		renderKanbanNewTaskSheet(c, s)
	}, 840, 520)
	tt.Frame()
	if err := tt.Click("kanban-quick-add-toggle"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !ks.showNewTask || ks.newTaskProjectID != "" || !tt.HasText("选择项目") {
		t.Fatal("全部项目范围应打开未选项目的任务表单")
	}
	ks.newTaskTitle = "任务"
	submitKanbanNewTask(s)
	select {
	case <-client.tasks:
		t.Fatal("未选择项目时创建了任务")
	default:
	}
	if err := tt.Click("选择项目"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("项目 B") })
	if err := tt.Click("项目 B"); err != nil {
		t.Fatal(err)
	}
	submitKanbanNewTask(s)
	r := nextCreate(t, client.tasks)
	if r.project != "p2" {
		t.Fatalf("创建项目 = %q，期望 p2", r.project)
	}
	r.reply <- nil
	awaitKanban(t, s, func() bool { return !ks.newTaskBusy })
}
