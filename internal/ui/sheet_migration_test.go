package mcodeui

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestKanbanNewTaskCancelDoesNotSubmit(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	t.Cleanup(func() { modalDragReset(modalDragKanbanNewTask) })
	ks := getKanbanState(s)
	ks.showNewTask = true
	ks.projectID = "project"
	ks.newTaskTitle = "未保存的任务"
	tt := ui.NewTester(func(c *ui.Context) { renderKanbanNewTaskSheet(c, s) }, 1000, 800)
	tt.Frame()
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if ks.showNewTask || len(fake.CreateTaskCalls()) != 0 {
		t.Fatalf("取消后窗口打开=%v，提交次数=%d", ks.showNewTask, len(fake.CreateTaskCalls()))
	}
}

func TestScriptsSheetInvalidDoesNotSave(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	t.Cleanup(func() { modalDragReset(modalDragScriptsSheet) })
	s.selectedThreadID = "thread"
	st := getScriptsSheetState(s)
	st.open = true
	st.threadID = "thread"
	st.rows = []*scriptsSheetRow{{name: "无效脚本"}}
	tt := ui.NewTester(func(c *ui.Context) { renderScriptsSheet(c, s) }, 1000, 800)
	tt.Frame()
	if err := tt.Click("保存脚本"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if st.err != "" {
		t.Fatalf("禁用按钮触发了保存：%q", st.err)
	}
	saveScriptsSheet(s)
	scripts, err := fake.ProjectScripts(context.Background(), "thread")
	if err != nil {
		t.Fatal(err)
	}
	if !st.open || st.saving || st.err == "" || len(scripts) != 0 {
		t.Fatalf("无效内容被保存或缺少校验：open=%v，saving=%v，err=%q，脚本数=%d", st.open, st.saving, st.err, len(scripts))
	}
}
