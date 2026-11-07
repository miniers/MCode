package app

// 本文件只测自动化远程写操作的纯转换与错误映射，
// 不依赖 Client，因此不需要构造 App 或后端。

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"mcode/internal/automation"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	mcodeui "mcode/internal/ui"
)

func TestAutomationExtInputToUI(t *testing.T) {
	failures := 5
	iterations := 10
	in := wire.AutomationExtInput{
		Name:                   "夜间整理",
		Prompt:                 "整理昨天的对话",
		StopWhen:               "没有新内容时停止",
		Schedule:               wire.AutomationExtSchedule{Type: "once", Date: "2026-01-02 09:30", Timezone: "Asia/Shanghai"},
		Enabled:                true,
		MaxConsecutiveFailures: &failures,
		MaxIterations:          &iterations,
	}

	out := automationExtInputToUI("proj-1", in)
	if out.ProjectID != "proj-1" || out.Name != "夜间整理" || out.Prompt != "整理昨天的对话" || out.StopWhen != "没有新内容时停止" {
		t.Fatalf("unexpected fields: %+v", out)
	}
	if !out.Enabled || out.MaxConsecutiveFailures != 5 || out.MaxIterations != 10 {
		t.Fatalf("unexpected limits: %+v", out)
	}
	if out.Schedule.Type != "once" || out.Schedule.Timezone != "Asia/Shanghai" {
		t.Fatalf("unexpected schedule: %+v", out.Schedule)
	}
	// 本地时间格式按桌面端表单的解析规则落到 time.Local。
	want := time.Date(2026, 1, 2, 9, 30, 0, 0, time.Local)
	if !out.Schedule.Date.Equal(want) {
		t.Fatalf("unexpected once date: got %v want %v", out.Schedule.Date, want)
	}

	// RFC3339 与 nil 限额同样要能吃下。
	in.Schedule.Date = "2026-03-04T05:06:07Z"
	in.MaxConsecutiveFailures = nil
	in.MaxIterations = nil
	out = automationExtInputToUI("proj-2", in)
	if out.Schedule.Date.UTC().Format(time.RFC3339) != "2026-03-04T05:06:07Z" {
		t.Fatalf("unexpected RFC3339 date: %v", out.Schedule.Date)
	}
	if out.MaxConsecutiveFailures != 0 || out.MaxIterations != 0 {
		t.Fatalf("nil limits should map to zero: %+v", out)
	}
}

func TestAutomationExtInputFromUI(t *testing.T) {
	auto := mcodeui.Automation{
		ID:                     "auto-1",
		ProjectID:              "proj-1",
		Name:                   "夜间整理",
		Prompt:                 "整理昨天的对话",
		StopWhen:               "没有新内容时停止",
		Schedule:               mcodeui.AutomationSchedule{Type: "daily", TimeOfDay: "09:00"},
		Enabled:                false,
		MaxConsecutiveFailures: 3,
		MaxIterations:          7,
		ConsecutiveFailures:    2,
		Revision:               4,
	}

	out := automationExtInputFromUI(auto, true)
	if out.ProjectID != auto.ProjectID || out.Name != auto.Name || out.Prompt != auto.Prompt || out.StopWhen != auto.StopWhen {
		t.Fatalf("fields not preserved: %+v", out)
	}
	if out.Schedule != auto.Schedule || out.MaxConsecutiveFailures != 3 || out.MaxIterations != 7 {
		t.Fatalf("limits or schedule dropped: %+v", out)
	}
	// 只有 Enabled 由调用方决定，ConsecutiveFailures/Revision 不进更新入参。
	if !out.Enabled {
		t.Fatalf("enabled should be overridden to true: %+v", out)
	}
}

func TestAutomationExtAutomationCarriesEditFields(t *testing.T) {
	auto := mcodeui.Automation{
		ID:                     "auto-1",
		ProjectID:              "proj-1",
		Name:                   "夜间整理",
		Prompt:                 "整理昨天的对话",
		StopWhen:               "没有新内容时停止",
		Schedule:               mcodeui.AutomationSchedule{Type: "daily", TimeOfDay: "09:00"},
		Status:                 "active",
		Enabled:                true,
		ConsecutiveFailures:    2,
		MaxConsecutiveFailures: 3,
		MaxIterations:          7,
		Revision:               4,
	}

	out := automationExtAutomation(auto)
	if out.Prompt != auto.Prompt || out.StopWhen != auto.StopWhen {
		t.Fatalf("prompt/stopWhen missing: %+v", out)
	}
	if out.ConsecutiveFailures != 2 || out.MaxConsecutiveFailures != 3 || out.MaxIterations != 7 {
		t.Fatalf("failure counters missing: %+v", out)
	}
	if out.Revision != 4 {
		t.Fatalf("unexpected revision: %d", out.Revision)
	}
	if out.ScheduleText != "每天 09:00" {
		t.Fatalf("unexpected schedule text: %q", out.ScheduleText)
	}
}

func TestRemoteAutomationExtError(t *testing.T) {
	if err := remoteAutomationExtError(nil); err != nil {
		t.Fatalf("nil should stay nil, got %v", err)
	}

	conflict := remoteAutomationExtError(fmt.Errorf("更新失败: %w", mcodeui.ErrRevisionConflict))
	if !errors.Is(conflict, remote.ErrAutomationExtConflict) {
		t.Fatalf("expected ErrAutomationExtConflict, got %v", conflict)
	}

	other := remoteAutomationExtError(errors.New("自动化不存在或已被删除：auto-1"))
	if errors.Is(other, remote.ErrAutomationExtConflict) {
		t.Fatalf("other errors should not be conflicts: %v", other)
	}
	if other.Error() != "自动化不存在或已被删除：auto-1" {
		t.Fatalf("message should be kept, got %q", other.Error())
	}
}

func TestAutomationExtInputMaxRunsAndCatchUp(t *testing.T) {
	maxRuns := 12
	catchUp := false
	in := wire.AutomationExtInput{MaxRuns: &maxRuns, CatchUp: &catchUp}
	out := automationExtInputToUI("proj-1", in)
	if out.MaxRuns != 12 || out.CatchUp == nil || *out.CatchUp {
		t.Fatalf("maxRuns/catchUp not mapped: %+v", out)
	}
	spec := AutomationInputToSpec(out, automation.Spec{})
	if spec.MaxRuns != 12 || spec.MaxIterations != 12 || spec.CatchUpEnabled() {
		t.Fatalf("spec should carry maxRuns and catchUp: %+v", spec)
	}

	// 旧客户端只传 maxIterations 时回填 maxRuns，避免上限被清成 0。
	legacy := 7
	out = automationExtInputToUI("proj-1", wire.AutomationExtInput{MaxIterations: &legacy})
	if out.MaxRuns != 7 || out.CatchUp != nil {
		t.Fatalf("legacy maxIterations should backfill maxRuns: %+v", out)
	}

	// CatchUp 缺省时沿用 base 的已有值（remote normalize 会补 true，这里覆盖纯转换语义）。
	keep := true
	spec = AutomationInputToSpec(mcodeui.AutomationInput{}, automation.Spec{CatchUp: &keep})
	if !spec.CatchUpEnabled() {
		t.Fatalf("nil catchUp should keep base value: %+v", spec)
	}
}

func TestAutomationExtAutomationCarriesRunCap(t *testing.T) {
	auto := mcodeui.Automation{ID: "auto-1", MaxRuns: 9, MaxIterations: 9, CatchUp: false}
	out := automationExtAutomation(auto)
	if out.MaxRuns != 9 || out.CatchUp {
		t.Fatalf("maxRuns/catchUp missing: %+v", out)
	}
}

func TestAutomationExtRunCarriesTrigger(t *testing.T) {
	out := automationExtRun(mcodeui.AutomationRun{ID: "run-1", Trigger: "catch_up", MissedCount: 3})
	if out.Trigger != "catch_up" || out.MissedCount != 3 {
		t.Fatalf("trigger/missedCount missing: %+v", out)
	}
}
