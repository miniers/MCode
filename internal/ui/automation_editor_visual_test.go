package mcodeui

import (
	"github.com/egoist/mygo/ui"
	"testing"
)

func TestAutomationEditorVisualFooterAtMinimumWindow(t *testing.T) {
	s := &chatState{client: NewFakeClient()}
	as := &automationState{isNew: true, name: "名称", prompt: "任务", scheduleType: "daily", timeOfDay: "09:00"}
	defer as.stopSchedulePreview()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("host")).Fill().Children(func() {
			renderAutomationEditor(c, s, as, true, "", "", false, false, nil, nil, 0, false, false, "", nil, nil, nil, "")
		})
	}, 840, 520)
	tt.Frame()
	for _, label := range []string{"新建自动化", "取消编辑", "automation-save"} {
		r, ok := tt.Find(label)
		if !ok || r.Y < 0 || r.Y+r.H > 520 {
			t.Errorf("%s 不在窗口内: %+v", label, r)
		}
	}
}

func TestAutomationEditorVisualTabOrder(t *testing.T) {
	s := &chatState{client: NewFakeClient()}
	as := &automationState{isNew: true, scheduleType: "daily", timeOfDay: "09:00"}
	defer as.stopSchedulePreview()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("host")).Fill().Children(func() {
			renderAutomationEditor(c, s, as, true, "", "", false, false, nil, nil, 0, false, false, "", nil, nil, nil, "")
		})
	}, 840, 520)
	tt.Frame()
	if !tt.Focused("automation-name-input") {
		t.Fatal("名称未自动聚焦")
	}
	tt.Key(0, ui.KeyTab)
	if !tt.Focused("automation-prompt-input") {
		t.Fatal("Tab 未从名称移动到任务")
	}
	found := false
	for i := 0; i < 20; i++ {
		tt.Key(0, ui.KeyTab)
		if tt.Focused("automation-max-runs-input") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Tab 未到达滚动区下方的最大运行次数")
	}
}
