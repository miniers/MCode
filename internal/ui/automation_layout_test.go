package mcodeui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestAutomationLayoutHistoryIsDefaultAndReadOnly(t *testing.T) {
	fake := NewFakeClient()
	fake.SetAutomations([]Automation{testAutomation("auto-1", "p1")})
	tt, _ := newWorkspaceTester(t, fake, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("auto-1") })
	if err := tt.Click("auto-1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("暂无运行记录") })
	if _, ok := tt.Find("automation-name-input"); ok {
		t.Fatal("查看记录时出现编辑器")
	}
	if len(fake.CreateAutomationCalls()) != 0 || len(fake.UpdateAutomationCalls()) != 0 {
		t.Fatal("查看记录触发配置写入")
	}
}

func TestAutomationLayoutFilterAndUnreadTotal(t *testing.T) {
	a := testAutomation("a", "p1")
	a.Name, a.LastRunStatus = "构建检查", "failed"
	b := testAutomation("b", "p1")
	b.Name, b.Enabled, b.Status = "构建发布", false, "paused"
	c := testAutomation("c", "p1")
	c.Name, c.Enabled, c.Status = "完成检查", false, "completed"
	autos := []Automation{a, b, c}
	unread := map[string]int{"a": 2, "b": 1, "c": 4}
	got := filterAutomationStatus(autos, "构建", 1, true, unread)
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("失败、搜索和未读组合结果错误: %+v", got)
	}
	paused := filterAutomationStatus(autos, "", 2, false, unread)
	if len(paused) != 1 || paused[0].ID != "b" {
		t.Fatalf("暂停筛选包含已完成: %+v", paused)
	}
	if total := automationUnreadTotal(unread, autos); total != 7 {
		t.Fatalf("全量未读数 = %d，期望 7", total)
	}
}

func TestAutomationLayoutEditorDismiss(t *testing.T) {
	fake := NewFakeClient()
	tt, s := newWorkspaceTester(t, fake, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("还没有自动化") })
	if err := tt.Click("automation-empty-create"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.Focused("automation-name-input") })
	if err := tt.Click("取消编辑"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return !getAutomationState(s).editorOpen })
	if err := tt.Click("automation-empty-create"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.Focused("automation-name-input") })
	tt.Type("草稿")
	if err := tt.Click("取消编辑"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("放弃未保存的修改？") })
	if !getAutomationState(s).editorOpen {
		t.Fatal("未确认便关闭了有修改的编辑器")
	}
	if err := tt.Click("放弃"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return !getAutomationState(s).editorOpen })
}

func TestAutomationLayoutCreatePaused(t *testing.T) {
	fake := NewFakeClient()
	tt, _ := newWorkspaceTester(t, fake, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("还没有自动化") })
	if err := tt.Click("automation-empty-create"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.Focused("automation-name-input") })
	tt.Type("暂停任务")
	if err := tt.Click("automation-prompt-input"); err != nil {
		t.Fatal(err)
	}
	tt.Type("执行检查")
	if err := tt.Click("创建状态"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("创建为暂停"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("automation-save"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return len(fake.CreateAutomationCalls()) == 1 })
	if fake.CreateAutomationCalls()[0].Enabled {
		t.Fatal("创建为暂停时发送了启用状态")
	}
}

func TestAutomationLayoutNarrowReturnKeepsSelection(t *testing.T) {
	fake := NewFakeClient()
	fake.SetAutomations([]Automation{testAutomation("auto-1", "p1")})
	var s *chatState
	tt := ui.NewTester(Root(fake, func(state *chatState) { s = state }), 840, 520)
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.projects) > 0
	})
	s.openWorkspace("automations")
	waitFor(t, tt, func() bool { return tt.HasText("auto-1") })
	if err := tt.Click("auto-1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("← 自动化") })
	if err := tt.Click("← 自动化"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("自动化项 auto-1") })
	if as := getAutomationState(s); as.selectedID != "auto-1" || as.narrowDetail {
		t.Fatal("返回列表时丢失选中项")
	}
}

type failedAutomationListClient struct{ *FakeClient }

func (c *failedAutomationListClient) Automations(context.Context, string) ([]Automation, error) {
	return nil, errors.New("加载失败")
}

func TestAutomationLayoutLoadError(t *testing.T) {
	tt, _ := newWorkspaceTester(t, &failedAutomationListClient{NewFakeClient()}, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("加载失败") && tt.HasText("重试") })
	if tt.HasText("还没有自动化") || tt.HasText("未找到") {
		t.Fatal("加载失败被误认为空列表或筛选无结果")
	}
}

type delayedAutomationPreviewClient struct {
	*FakeClient
	started  chan int
	release  chan struct{}
	finished chan struct{}
}

func (c *delayedAutomationPreviewClient) PreviewAutomationSchedule(_ context.Context, sch AutomationSchedule, _ time.Time, _ int) (AutomationSchedulePreview, error) {
	c.started <- sch.Seconds
	if sch.Seconds == 60 {
		<-c.release
		defer close(c.finished)
	}
	return AutomationSchedulePreview{Times: []time.Time{time.Unix(int64(sch.Seconds), 0)}}, nil
}

func TestAutomationLayoutPreviewDiscardsLateResult(t *testing.T) {
	client := &delayedAutomationPreviewClient{FakeClient: NewFakeClient(), started: make(chan int, 2), release: make(chan struct{}), finished: make(chan struct{})}
	tt, s := newWorkspaceTester(t, client, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("还没有自动化") })
	as := getAutomationState(s)
	as.resetToNewAutomation(s)
	as.intervalInput = "1"
	as.updateSchedulePreview(s)
	select {
	case <-client.started:
	case <-time.After(5 * time.Second):
		t.Fatal("首次预览未开始")
	}
	as.intervalInput = "2"
	as.updateSchedulePreview(s)
	select {
	case <-client.started:
	case <-time.After(5 * time.Second):
		t.Fatal("第二次预览未开始")
	}
	waitFor(t, tt, func() bool { return as.previewVisible && as.preview.Times[0].Unix() == 120 })
	close(client.release)
	select {
	case <-client.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("迟到的预览未完成")
	}
	ws := getWorkspaceState(s)
	deadline := time.After(5 * time.Second)
	for {
		workspaceMu.Lock()
		queued := len(ws.updates) > 0
		workspaceMu.Unlock()
		if queued {
			break
		}
		select {
		case <-deadline:
			t.Fatal("迟到的预览未提交到界面")
		case <-time.After(time.Millisecond):
		}
	}
	s.applyWorkspaceUpdates()
	if !as.previewVisible || as.preview.Times[0].Unix() != 120 {
		t.Fatal("迟到的预览覆盖了最新结果")
	}
	as.intervalInput = "无效"
	as.updateSchedulePreview(s)
	if as.previewVisible {
		t.Fatal("无效输入仍显示旧预览")
	}
}
