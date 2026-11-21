package mcodeui

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestQuotaWeekTimelineLayoutPointsAndRounding(t *testing.T) {
	// 2026-11-21 为周六，本地今天 0 点起跨 7 天，now 在当天正午（12/168）。
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.Local)
	start := time.Date(2026, 11, 21, 0, 0, 0, 0, time.Local)
	groups := []quotaAccountGroup{
		{
			account: "a@example.com",
			windows: []ProviderQuotaWindow{
				{Label: "5h", UsedPercent: 30, ResetsAt: start.Add(24 * time.Hour)},
				{Label: "7d", UsedPercent: 45, ResetsAt: start.Add(84 * time.Hour)},
			},
		},
	}

	layout := buildQuotaWeekTimelineLayout(now, 100, groups)
	if !layout.hasMarks {
		t.Fatal("落在 7 天内的重置窗口应标记 hasMarks=true")
	}
	if wantNow := float32(math.Round((12.0 / 168.0) * 100)); layout.nowX != wantNow {
		t.Fatalf("nowX = %v, want %v", layout.nowX, wantNow)
	}
	if len(layout.lanes) != 1 || layout.lanes[0].account != "a@example.com" {
		t.Fatalf("lanes = %+v, 期望 1 条泳道", layout.lanes)
	}
	wantPoints := []float32{
		float32(math.Round((24.0 / 168.0) * 100)),
		float32(math.Round((84.0 / 168.0) * 100)),
	}
	gotPoints := layout.lanes[0].points
	if len(gotPoints) != len(wantPoints) {
		t.Fatalf("points len = %d, want %d (%v)", len(gotPoints), len(wantPoints), gotPoints)
	}
	for i := range wantPoints {
		if gotPoints[i] != wantPoints[i] {
			t.Fatalf("point[%d] = %v, want %v", i, gotPoints[i], wantPoints[i])
		}
	}
	wantDays := [7]string{"周六", "周日", "周一", "周二", "周三", "周四", "周五"}
	if layout.weekdays != wantDays {
		t.Fatalf("weekdays = %v, want %v", layout.weekdays, wantDays)
	}
}

func TestQuotaWeekTimelineDepletedRange(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.Local)
	start := time.Date(2026, 11, 21, 0, 0, 0, 0, time.Local)
	groups := []quotaAccountGroup{
		{
			account: "full@example.com",
			windows: []ProviderQuotaWindow{
				{Label: "5h", UsedPercent: 100, ResetsAt: now.Add(24 * time.Hour)},
				{Label: "7d", UsedPercent: 99.5, ResetsAt: now.Add(48 * time.Hour)},
				{Label: "past", UsedPercent: 100, ResetsAt: start.Add(2 * time.Hour)},
			},
		},
	}

	layout := buildQuotaWeekTimelineLayout(now, 280, groups)
	if len(layout.lanes) != 1 {
		t.Fatalf("lanes len = %d, want 1", len(layout.lanes))
	}
	lane := layout.lanes[0]
	if len(lane.points) != 3 {
		t.Fatalf("points len = %d, want 3", len(lane.points))
	}
	if len(lane.depleted) != 1 {
		t.Fatalf("depleted len = %d, want 1 (%+v)", len(lane.depleted), lane.depleted)
	}
	wantX0 := layout.nowX
	wantX1 := float32(math.Round((now.Add(24*time.Hour).Sub(start).Hours() / 168.0) * 280))
	if lane.depleted[0].x0 != wantX0 || lane.depleted[0].x1 != wantX1 {
		t.Fatalf("depleted[0] = %+v, want [%v, %v]", lane.depleted[0], wantX0, wantX1)
	}
}

func TestQuotaWeekTimelineIgnoresOutOfAxisWindows(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.Local)
	start := time.Date(2026, 11, 21, 0, 0, 0, 0, time.Local)
	end := start.Add(7 * 24 * time.Hour)
	groups := []quotaAccountGroup{
		{
			account: "a@example.com",
			windows: []ProviderQuotaWindow{
				{Label: "zero", UsedPercent: 100},
				{Label: "before", UsedPercent: 100, ResetsAt: start.Add(-time.Minute)},
				{Label: "after", UsedPercent: 100, ResetsAt: end.Add(time.Minute)},
			},
		},
	}

	layout := buildQuotaWeekTimelineLayout(now, 280, groups)
	if layout.hasMarks {
		t.Fatal("轴外与零值重置窗口不应产生可画内容")
	}
	if len(layout.lanes) != 1 || len(layout.lanes[0].points) != 0 || len(layout.lanes[0].depleted) != 0 {
		t.Fatalf("轴外窗口应被忽略: %+v", layout.lanes)
	}
}

func TestQuotaWeekTimelineHiddenWhenNoResetInWeek(t *testing.T) {
	testenv.TempHome(t)
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.Local)
	oldNow := usagePanelNow
	usagePanelNow = func() time.Time { return now }
	t.Cleanup(func() { usagePanelNow = oldNow })

	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	th := &Thread{ID: "t1", Provider: "claude"}
	SetChatQuotaWindowsForProvider(s, th.Provider, []ProviderQuotaWindow{
		{Label: "5 hours", UsedPercent: 20},
		{Label: "30 days", UsedPercent: 40, ResetsAt: now.Add(15 * 24 * time.Hour)},
	})
	t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, th.Provider, nil) })

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Width(quotaPopoverWidth).Children(func() {
			renderComposerQuotaPopover(c, s, th, th.Provider, 0)
		})
	}, 480, 480)
	tt.Frame()
	if tt.HasText("本周重置") {
		t.Fatalf("7 天内无重置窗口时不应渲染「本周重置」: %q", tt.Texts())
	}

	// 单账号有 7 天内重置时只画一条泳道，不写账号名。
	SetChatQuotaWindowsForProvider(s, th.Provider, []ProviderQuotaWindow{
		{Label: "5 hours", Account: "solo@example.com", UsedPercent: 20, ResetsAt: now.Add(3 * time.Hour)},
	})
	ttSolo := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Width(quotaPopoverWidth).Children(func() {
			renderComposerQuotaPopover(c, s, th, th.Provider, 0)
		})
	}, 480, 480)
	ttSolo.Frame()
	if !ttSolo.HasText("本周重置") {
		t.Fatalf("7 天内有重置窗口时应渲染「本周重置」: %q", ttSolo.Texts())
	}
	if ttSolo.HasText("solo@example.com") {
		t.Fatalf("单账号泳道不应显示账号名: %q", ttSolo.Texts())
	}
	if _, ok := ttSolo.Find("本周重置泳道"); !ok {
		t.Fatal("单账号时应渲染一条泳道")
	}
}

func TestQuotaWeekTimelineTwoAccountsAndHoverHighlight(t *testing.T) {
	testenv.TempHome(t)
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.Local)
	oldNow := usagePanelNow
	usagePanelNow = func() time.Time { return now }
	t.Cleanup(func() { usagePanelNow = oldNow })

	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	th := &Thread{ID: "t1", Provider: "claude"}
	SetChatQuotaWindowsForProvider(s, th.Provider, []ProviderQuotaWindow{
		{Label: "5 hours", Account: "claude · alice@example.com", UsedPercent: 20, Active: true, ResetsAt: now.Add(3 * time.Hour)},
		{Label: "5 hours", Account: "claude · bob@example.com", UsedPercent: 100, ResetsAt: now.Add(48 * time.Hour)},
	})
	t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, th.Provider, nil) })

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Width(quotaPopoverWidth).Children(func() {
			renderComposerQuotaPopover(c, s, th, th.Provider, 0)
		})
	}, 480, 520)
	tt.Frame()

	laneAlice, okAlice := tt.Find("本周重置泳道 alice@example.com")
	laneBob, okBob := tt.Find("本周重置泳道 bob@example.com")
	if !okAlice || !okBob {
		t.Fatalf("两个账号应产生两条带账号名的泳道，alice=%v bob=%v texts=%q", okAlice, okBob, tt.Texts())
	}
	if laneAlice.Y >= laneBob.Y {
		t.Fatalf("两条泳道应上下排列，alice=%+v bob=%+v", laneAlice, laneBob)
	}
	for _, wd := range []string{"周六", "周日", "周一", "周二", "周三", "周四", "周五"} {
		if !tt.HasText(wd) {
			t.Fatalf("缺少周几刻度 %q: %q", wd, tt.Texts())
		}
	}

	tt.Move(laneBob.X+laneBob.W/2, laneBob.Y+laneBob.H/2)
	tt.Frame()
	if s.composerView.quotaHoverAccount != "claude · bob@example.com" {
		t.Fatalf("悬停泳道后 quotaHoverAccount = %q, want %q", s.composerView.quotaHoverAccount, "claude · bob@example.com")
	}

	tt.Advance(DurSnap + time.Second)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("悬停高亮过渡结束后不应继续请求下一帧")
	}

	tt.Move(0, 0)
	tt.Frame()
	if s.composerView.quotaHoverAccount != "" {
		t.Fatalf("移出泳道后 quotaHoverAccount 应清空，got %q", s.composerView.quotaHoverAccount)
	}
}

func TestQuotaWeekTimelineSettlesNoPendingFrames(t *testing.T) {
	testenv.TempHome(t)
	baseNow := time.Date(2026, 11, 21, 10, 0, 0, 0, time.Local)

	for _, reduced := range []bool{false, true} {
		t.Run(fmt.Sprintf("reduced=%v", reduced), func(t *testing.T) {
			setAppReduceMotion(t, reduced)
			currentNow := baseNow
			oldNow := usagePanelNow
			usagePanelNow = func() time.Time { return currentNow }
			t.Cleanup(func() { usagePanelNow = oldNow })

			s := newChatState(NewFakeClient())
			t.Cleanup(s.close)
			th := &Thread{ID: "t1", Provider: "claude"}
			SetChatQuotaWindowsForProvider(s, th.Provider, []ProviderQuotaWindow{
				{Label: "5 hours", Account: "a@example.com", UsedPercent: 30, ResetsAt: baseNow.Add(4 * time.Hour)},
				{Label: "7 days", Account: "b@example.com", UsedPercent: 100, ResetsAt: baseNow.Add(72 * time.Hour)},
			})
			t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, th.Provider, nil) })

			tt := ui.NewTesterWith(func(c *ui.Context) {
				ui.Box(c).Width(quotaPopoverWidth).Children(func() {
					renderComposerQuotaPopover(c, s, th, th.Provider, 0)
				})
			}, 480, 520, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduced}})
			tt.Frame()

			if !tt.HasText("本周重置") {
				t.Fatalf("应渲染「本周重置」，texts=%q", tt.Texts())
			}
			tt.Advance(DurBase + time.Second)
			tt.Frame()
			if tt.FramePending() {
				t.Fatal("静止状态下浮层不应继续请求下一帧")
			}

			for i := 0; i < 60; i++ {
				currentNow = currentNow.Add(16 * time.Millisecond)
				tt.Advance(16 * time.Millisecond)
				tt.Frame()
			}
			if tt.FramePending() {
				t.Fatal("时钟推进时本周重置时间线不应触发连续重绘")
			}
		})
	}
}
