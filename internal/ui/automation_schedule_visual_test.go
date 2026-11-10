package mcodeui

import (
	"github.com/egoist/mygo/ui"
	"testing"
	"time"
)

func TestAutomationScheduleVisualAllTypes(t *testing.T) {
	for _, width := range []int{360, 660} {
		for _, tc := range []struct{ kind, field string }{{"daily", "automation-time-input"}, {"weekdays", "automation-time-input"}, {"weekly", "星期几"}, {"interval", "运行间隔"}, {"once", "automation-once-input"}, {"cron", "automation-cron-input"}} {
			t.Run(tc.kind, func(t *testing.T) {
				s := &chatState{client: NewFakeClient()}
				as := &automationState{scheduleType: tc.kind, intervalInput: "1", intervalUnit: "分钟", timeOfDay: "09:00", onceDateStr: "2030-01-01 09:00", cronExpr: "0 9 * * *"}
				defer as.stopSchedulePreview()
				tt := ui.NewTester(func(c *ui.Context) {
					ui.Column(c.Key("host")).Fill().Children(func() { renderAutomationScheduleFields(c, s, as) })
				}, width, 600)
				tt.Frame()
				for _, label := range []string{"调度类型", tc.field, "时区"} {
					r, ok := tt.Find(label)
					if !ok || r.X < 0 || r.X+r.W > float32(width) {
						t.Errorf("%s 在宽度 %v 下不可见或裁剪: %+v", label, width, r)
					}
				}
			})
		}
	}
}

func TestAutomationScheduleVisualPreviewPlacement(t *testing.T) {
	s := &chatState{client: &delayedAutomationPreviewClient{FakeClient: NewFakeClient()}}
	as := &automationState{scheduleType: "daily", timeOfDay: "09:00"}
	defer as.stopSchedulePreview()
	sch, _ := automationScheduleFromDraft(as)
	as.previewValid = true
	as.previewSchedule = sch
	as.previewVisible = true
	now := time.Now()
	as.preview.Times = []time.Time{now.Add(24 * time.Hour), now.Add(48 * time.Hour), now.Add(72 * time.Hour), now.Add(96 * time.Hour)}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("host")).Fill().Children(func() { renderAutomationScheduleFields(c, s, as) })
	}, 660, 600)
	tt.Frame()
	if !tt.HasText("预计运行") || tt.HasText(automationShortTime(as.preview.Times[3], now)) {
		t.Fatal("预览标题或数量不正确")
	}
	as.scheduleType = "once"
	as.onceDateStr = "2030-01-01 09:00"
	as.previewSchedule, _ = automationScheduleFromDraft(as)
	tt.Frame()
	if !tt.HasText("预计运行") || tt.HasText(automationShortTime(as.preview.Times[1], now)) {
		t.Fatal("单次调度预览超过一条")
	}
	as.timeOfDay = "无效"
	as.onceDateStr = "无效"
	tt.Frame()
	if tt.HasText("预计运行") {
		t.Fatal("无效输入残留预览")
	}
}
