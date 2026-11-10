package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type delayedRunClient struct {
	*FakeClient
	started chan struct{}
	release chan struct{}
}

func (c *delayedRunClient) RunAutomation(ctx context.Context, id string) (AutomationRun, error) {
	close(c.started)
	<-c.release
	return c.FakeClient.RunAutomation(ctx, id)
}

func TestAutomationRunNowOpensRunAndPreservesPause(t *testing.T) {
	fake := NewFakeClient()
	a := testAutomation("auto-1", "p1")
	a.Enabled = false
	fake.SetAutomations([]Automation{a})
	tt, s := newWorkspaceTester(t, fake, "automations")
	waitFor(t, tt, func() bool { return tt.HasText(a.Name) })
	if err := tt.Click(a.Name); err != nil {
		t.Fatal(err)
	}
	s.runAutomationNow(a.ID)
	waitFor(t, tt, func() bool { return tt.HasText("返回运行记录") && tt.HasText("手动") })
	if getAutomationState(s).openRunID != "run-1" || len(fake.UpdateAutomationCalls()) != 0 {
		t.Fatal("运行详情或暂停状态不正确")
	}
}

func TestAutomationRunNowDoubleClickAndLateProject(t *testing.T) {
	fake := NewFakeClient()
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "项目一"}, {ID: "p2", Name: "项目二"}}, nil)
	fake.SetAutomations([]Automation{testAutomation("a", "p1"), testAutomation("b", "p2")})
	client := &delayedRunClient{FakeClient: fake, started: make(chan struct{}), release: make(chan struct{})}
	tt, s := newWorkspaceTester(t, client, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("a") })
	if err := tt.Click("a"); err != nil {
		t.Fatal(err)
	}
	s.runAutomationNow("a")
	select {
	case <-client.started:
	case <-time.After(5 * time.Second):
		t.Fatal("运行未开始")
	}
	s.runAutomationNow("a")
	if err := tt.Click("workspace-project-p2"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("b") })
	if err := tt.Click("b"); err != nil {
		t.Fatal(err)
	}
	close(client.release)
	waitFor(t, tt, func() bool { return len(fake.RunAutomationCalls()) == 1 })
	tt.Frame()
	as := getAutomationState(s)
	if as.projectID != "p2" || as.selectedID != "b" || as.openRunID != "" {
		t.Fatal("迟到的运行结果改变了新项目")
	}
}

type failedRunClient struct{ *FakeClient }

func (c *failedRunClient) RunAutomation(context.Context, string) (AutomationRun, error) {
	return AutomationRun{ID: "failed-run", Status: "failed", Error: "立即失败", StartedAt: time.Now()}, nil
}

func TestAutomationRunNowImmediateFailure(t *testing.T) {
	client := &failedRunClient{NewFakeClient()}
	client.SetAutomations([]Automation{testAutomation("auto-1", "p1")})
	tt, s := newWorkspaceTester(t, client, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("auto-1") })
	if err := tt.Click("auto-1"); err != nil {
		t.Fatal(err)
	}
	s.runAutomationNow("auto-1")
	waitFor(t, tt, func() bool { return tt.HasText("立即失败") })
	if getAutomationState(s).openRunID != "failed-run" {
		t.Fatal("未打开失败详情")
	}
}

func TestAutomationConfigSummary(t *testing.T) {
	a := testAutomation("auto-1", "p1")
	a.Schedule = AutomationSchedule{Type: "daily", TimeOfDay: "09:00", Timezone: "Asia/Shanghai"}
	a.Model, a.RuntimeMode, a.MaxRuns, a.CatchUp = "model-x", "readonly", 5, true
	lines := strings.Join(automationConfigLines(a), "\n")
	for _, part := range []string{"每天 09:00", "Asia/Shanghai", "model-x", "只读", "最多 5 次", "错过后补跑：开启"} {
		if !strings.Contains(lines, part) {
			t.Fatalf("配置缺少 %s", part)
		}
	}
}

func TestAutomationEditorCommandSaveValidation(t *testing.T) {
	fake := NewFakeClient()
	tt, s := newWorkspaceTester(t, fake, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("还没有自动化") })
	getAutomationState(s).resetToNewAutomation(s)
	waitFor(t, tt, func() bool { return tt.Focused("automation-name-input") })
	tt.Key(ui.Cmd, ui.KeyS)
	waitFor(t, tt, func() bool { return tt.HasText("自动化名称不能为空") })
	if !getAutomationState(s).editorOpen {
		t.Fatal("校验失败后关闭编辑器")
	}
}

func TestAutomationRunNowBackendError(t *testing.T) {
	fake := NewFakeClient()
	fake.SetAutomationRunError(errors.New("运行被拒绝"))
	fake.SetAutomations([]Automation{testAutomation("auto-1", "p1")})
	tt, s := newWorkspaceTester(t, fake, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("auto-1") })
	if err := tt.Click("auto-1"); err != nil {
		t.Fatal(err)
	}
	s.runAutomationNow("auto-1")
	waitFor(t, tt, func() bool { return tt.HasText("运行自动化失败: 运行被拒绝") })
}

func TestAutomationRunNowDirtyChoices(t *testing.T) {
	for _, choice := range []string{"automation-save-run", "automation-saved-run", "automation-cancel-run"} {
		t.Run(choice, func(t *testing.T) {
			fake := NewFakeClient()
			fake.SetAutomations([]Automation{testAutomation("auto-1", "p1")})
			tt, s := newWorkspaceTester(t, fake, "automations")
			waitFor(t, tt, func() bool { return tt.HasText("auto-1") })
			if err := tt.Click("auto-1"); err != nil {
				t.Fatal(err)
			}
			openAutomationEditor(t, tt)
			as := getAutomationState(s)
			as.prompt = "修改后的任务"
			s.requestAutomationRun("auto-1")
			waitFor(t, tt, func() bool { _, ok := tt.Find(choice); return ok })
			if err := tt.Click(choice); err != nil {
				t.Fatal(err)
			}
			if choice == "automation-cancel-run" {
				if len(fake.RunAutomationCalls()) != 0 || !as.editorOpen || !as.dirty() {
					t.Fatal("取消后草稿或运行状态错误")
				}
				return
			}
			waitFor(t, tt, func() bool { return len(fake.RunAutomationCalls()) == 1 })
			if choice == "automation-save-run" && (len(fake.UpdateAutomationCalls()) != 1 || fake.UpdateAutomationCalls()[0].Prompt != "修改后的任务") {
				t.Fatal("运行前未保存草稿")
			}
			if choice == "automation-saved-run" && (len(fake.UpdateAutomationCalls()) != 0 || !as.dirty()) {
				t.Fatal("运行已保存配置时丢失草稿")
			}
		})
	}
}

func TestAutomationListScrollSurvivesDestinationChange(t *testing.T) {
	fake := NewFakeClient()
	var autos []Automation
	for i := 0; i < 30; i++ {
		autos = append(autos, testAutomation(fmt.Sprintf("任务%02d", i), "p1"))
	}
	fake.SetAutomations(autos)
	tt, s := newWorkspaceTester(t, fake, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("任务00") })
	as := getAutomationState(s)
	item, ok := tt.Find("自动化项 任务00")
	if !ok {
		t.Fatal("缺少列表项")
	}
	for i := 0; i < 5; i++ {
		tt.Scroll(item.X+item.W/2, item.Y+item.H/2, 0, 100)
	}
	before := as.listScroll.Y
	if before <= 0 {
		t.Fatal("列表未滚动")
	}
	s.openWorkspace("tasks")
	tt.Frame()
	s.openWorkspace("automations")
	waitFor(t, tt, func() bool { return tt.HasText("自动化项 任务00") })
	if math.Abs(float64(as.listScroll.Y-before)) > 1 {
		t.Fatalf("滚动位置 %v，期望 %v", as.listScroll.Y, before)
	}
}

func TestAutomationEditorArrowKeysDoNotSelectList(t *testing.T) {
	fake := NewFakeClient()
	fake.SetAutomations([]Automation{testAutomation("auto-1", "p1"), testAutomation("auto-2", "p1")})
	tt, s := newWorkspaceTester(t, fake, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("auto-1") })
	if err := tt.Click("auto-1"); err != nil {
		t.Fatal(err)
	}
	openAutomationEditor(t, tt)
	if err := tt.Click("automation-prompt-input"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyUp)
	if getAutomationState(s).selectedID != "auto-1" {
		t.Fatal("文本输入方向键切换了列表选中")
	}
}

func TestAutomationListKeyboardNavigation(t *testing.T) {
	fake := NewFakeClient()
	fake.SetAutomations([]Automation{testAutomation("auto-1", "p1"), testAutomation("auto-2", "p1")})
	tt, s := newWorkspaceTester(t, fake, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("auto-1") })
	if err := tt.Click("自动化项 auto-1"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyDown)
	waitFor(t, tt, func() bool { return getAutomationState(s).selectedID == "auto-2" })
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool { return getAutomationState(s).editorOpen })
}
