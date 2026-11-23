package mcodeui

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

func TestQuotaWarningThreshold(t *testing.T) {
	for _, tc := range []struct {
		used  float64
		reset time.Time
		pct   string
	}{
		{94.9, time.Time{}, "95%"},
		{95, time.Time{}, "95%"},
		{100, time.Date(2026, 1, 1, 13, 45, 0, 0, time.Local), "100%"},
	} {
		groups := []trayQuotaGroupModel{{Title: "测试", Accounts: []trayQuotaAccountModel{{Windows: []trayQuotaWindowModel{{ShortLabel: "5h", UsedPercent: tc.used, resetsAt: tc.reset}}}}}}
		tt := ui.NewTester(func(c *ui.Context) { renderQuotaGroupList(c, "test-", groups, quotaGroupListOptions{}) }, 500, 300)
		tt.Frame()
		if !tt.HasText("5h") || !tt.HasText(tc.pct) {
			t.Fatalf("%.1f 缺少窗口或百分比 %q: %v", tc.used, tc.pct, tt.Texts())
		}
		if tt.HasText("即将用尽") || tt.HasText("已用尽") {
			t.Fatalf("%.1f 不应额外显示重复用尽提示: %v", tc.used, tt.Texts())
		}
		if tt.HasText("重置于") {
			t.Fatalf("%.1f 不应重复拼接重置时刻: %v", tc.used, tt.Texts())
		}
	}
}

func TestQuotaBarMotion(t *testing.T) {
	for _, reduced := range []bool{false, true} {
		t.Run(fmt.Sprintf("reduced=%v", reduced), func(t *testing.T) {
			setAppReduceMotion(t, reduced)
			used := 0.2
			tt := ui.NewTesterWith(func(c *ui.Context) {
				ui.Row(c.Key("bar")).Width(200).Height(4).Children(func() {
					renderQuotaBar(c, "motion-quota", used, 0.5, false, false, ThemeAccent(c), "marker")
				})
			}, 220, 50, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduced}})
			tt.Frame()
			used = 0.4
			tt.Frame()
			if tt.FramePending() == reduced {
				t.Fatalf("减少动效=%v 时帧排队状态不正确", reduced)
			}
			tt.Advance(DurBase + time.Second)
			tt.Frame()
			if tt.FramePending() {
				t.Fatal("动效应结束")
			}
		})
	}
}

func TestQuotaBarSlotsStableAcrossHover(t *testing.T) {
	cases := []struct {
		name    string
		used    float64
		elapsed float64
	}{
		{name: "under-pace", used: 0.2, elapsed: 0.5},
		{name: "ghost-crosses-marker", used: 0.3, elapsed: 0.5},
		{name: "over-pace", used: 0.6, elapsed: 0.4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			segsOff := quotaBarSegments(tc.used, tc.elapsed, true, false)
			segsOn := quotaBarSegments(tc.used, tc.elapsed, true, true)
			slotsOff := quotaBarSlots("hover-test", segsOff)
			slotsOn := quotaBarSlots("hover-test", segsOn)

			if len(slotsOff) != 11 || len(slotsOn) != 11 {
				t.Fatalf("槽位总数应为 11（10 槽 + 1 竖线），got off=%d on=%d", len(slotsOff), len(slotsOn))
			}
			for i := range slotsOff {
				if slotsOff[i].key != slotsOn[i].key || slotsOff[i].kind != slotsOn[i].kind {
					t.Fatalf("槽位 %d 不一致：off=(%s,%v) on=(%s,%v)", i, slotsOff[i].key, slotsOff[i].kind, slotsOn[i].key, slotsOn[i].kind)
				}
			}

			for _, check := range []struct {
				label string
				segs  []quotaBarSegment
				slots []quotaBarSlot
			}{
				{"projected=false", segsOff, slotsOff},
				{"projected=true", segsOn, slotsOn},
			} {
				var sum float64
				var nonZero []quotaBarSlot
				for _, s := range check.slots {
					if s.kind == quotaBarSegmentMarker {
						continue
					}
					sum += float64(s.grow)
					if s.grow > 0 {
						nonZero = append(nonZero, s)
					}
				}
				if math.Abs(sum-1) > 1e-6 {
					t.Fatalf("%s grow 之和应为 1，got %v", check.label, sum)
				}

				var want []quotaBarSegment
				for _, s := range check.segs {
					if s.kind != quotaBarSegmentMarker {
						want = append(want, s)
					}
				}
				if len(nonZero) != len(want) {
					t.Fatalf("%s 非零槽数量 %d 与段数 %d 不一致", check.label, len(nonZero), len(want))
				}
				for i := range want {
					if nonZero[i].kind != want[i].kind || math.Abs(float64(nonZero[i].grow-want[i].grow)) > 1e-6 {
						t.Fatalf("%s 非零槽[%d]=(%v,%v) 与段=(%v,%v) 不一致", check.label, i, nonZero[i].kind, nonZero[i].grow, want[i].kind, want[i].grow)
					}
				}
			}
		})
	}
}

func TestQuotaBarSettlesWhileClockAdvances(t *testing.T) {
	for _, wide := range []bool{false, true} {
		t.Run(fmt.Sprintf("wide=%v", wide), func(t *testing.T) {
			elapsed := 0.4
			used := 20.0
			tt := ui.NewTester(func(c *ui.Context) {
				win := trayQuotaWindowModel{ShortLabel: "5h", UsedPercent: used, Wide: wide, elapsedRatio: elapsed, hasElapsed: true, paceKnown: true}
				groups := []trayQuotaGroupModel{{Accounts: []trayQuotaAccountModel{{Windows: []trayQuotaWindowModel{win}}}}}
				renderQuotaGroupList(c, "test-", groups, quotaGroupListOptions{})
			}, 500, 300)
			tt.Frame()
			used = 30
			tt.Frame()
			if !tt.FramePending() {
				t.Fatal("用量变化后应启动动画")
			}
			for i := 0; i < 100; i++ {
				elapsed += 16.0 / (300 * 60 * 1000)
				tt.Advance(16 * time.Millisecond)
				tt.Frame()
			}
			if tt.FramePending() {
				t.Fatal("时间在同一像素内推进时动画应结束")
			}
		})
	}
}

// TestUIReview26_QuotaGroupErrorDangerColorAndPercentWidth 验证分组报错使用危险色、
// 完整报错保留在模型里，且 100% 百分比列随字号缩放并能完整放下。
func TestUIReview26_QuotaGroupErrorDangerColorAndPercentWidth(t *testing.T) {
	t.Cleanup(func() { setActiveTheme(config.AppearanceConfig{}) })

	longErr := "dial tcp 127.0.0.1:8080: connect: connection refused by upstream gateway"
	model := buildTrayQuotaModel([]TrayQuotaGroup{
		{
			Provider: "claude",
			Title:    "Claude With A Very Long Provider Title",
			Err:      longErr,
		},
	}, time.Now(), time.Now(), false)
	if len(model.Groups) != 1 || !model.Groups[0].HasErr {
		t.Fatalf("应生成报错分组: %+v", model.Groups)
	}
	if model.Groups[0].ErrFull != longErr {
		t.Fatalf("ErrFull = %q, want %q", model.Groups[0].ErrFull, longErr)
	}

	var wantDanger ui.Color
	tt := ui.NewTester(func(c *ui.Context) {
		applyAppTheme(c)
		wantDanger = ThemeDanger(c)
		renderQuotaGroupList(c, "u26-", model.Groups, quotaGroupListOptions{})
	}, 360, 200)
	tt.Frame()

	errRect, ok := tt.Find(model.Groups[0].ErrText)
	if !ok {
		t.Fatalf("未找到报错文字 %q: %v", model.Groups[0].ErrText, tt.Texts())
	}
	img := tt.Image()
	foundDanger := false
	for y := int(errRect.Y); y < int(errRect.Y+errRect.H) && y < img.Bounds().Max.Y; y++ {
		for x := int(errRect.X); x < int(errRect.X+errRect.W) && x < img.Bounds().Max.X; x++ {
			if y < 0 || x < 0 {
				continue
			}
			px := img.RGBAAt(x, y)
			if px.R == wantDanger.R && px.G == wantDanger.G && px.B == wantDanger.B && px.A > 0 {
				foundDanger = true
				break
			}
		}
		if foundDanger {
			break
		}
	}
	if !foundDanger {
		t.Fatalf("报错文字未渲染为危险色 %+v", wantDanger)
	}

	// 放大界面字号后，半格 100% 与 20% 仍能完整放下且右对齐一致。
	setActiveTheme(config.AppearanceConfig{
		Theme: "custom",
		Themes: []config.ThemeDef{{
			ID:    "custom",
			Base:  config.ThemeLight,
			Fonts: map[string]int{"interface": 150},
		}},
	})
	var scaledWidth float32
	pctGroups := []trayQuotaGroupModel{{
		Title: "Claude",
		Accounts: []trayQuotaAccountModel{{
			Windows: []trayQuotaWindowModel{
				{ShortLabel: "5h", UsedPercent: 20},
				{ShortLabel: "周", UsedPercent: 100},
			},
		}},
	}}
	ttPct := ui.NewTester(func(c *ui.Context) {
		applyAppTheme(c)
		scaledWidth = quotaPercentWidth(c)
		renderQuotaGroupList(c, "u26-pct-", pctGroups, quotaGroupListOptions{})
	}, 440, 200)
	ttPct.Frame()
	if scaledWidth <= 40 {
		t.Fatalf("字号放大后百分比列宽应随之放大，实际 %v", scaledWidth)
	}
	r100, ok := ttPct.Find("100%")
	if !ok {
		t.Fatalf("放大字号后应完整显示 100%%: %v", ttPct.Texts())
	}
	if r100.W < scaledWidth-1 {
		t.Fatalf("100%% 列宽应至少达到测量宽度 %v，实际 %v", scaledWidth, r100.W)
	}
}

func TestQuotaBarGhostSingleFront(t *testing.T) {
	slotGrow := func(slots []quotaBarSlot, key string) float64 {
		for _, s := range slots {
			if s.key == key {
				return float64(s.grow)
			}
		}
		t.Fatalf("未找到槽位 %q", key)
		return 0
	}
	assertGrowSum := func(slots []quotaBarSlot) {
		var sum float64
		for _, s := range slots {
			if s.kind == quotaBarSegmentMarker {
				continue
			}
			sum += float64(s.grow)
		}
		if math.Abs(sum-1) > 1e-6 {
			t.Fatalf("所有槽 grow 之和应为 1，got %v", sum)
		}
	}

	const used, elapsed = 0.2, 0.4
	for _, g := range []float64{0.3, 0.4, 0.7} {
		slots := quotaBarSlots("t", quotaBarSegmentsTo(used, elapsed, g, true))
		assertGrowSum(slots)
		beforeGhost := slotGrow(slots, "t-before-ghost")
		afterGhost := slotGrow(slots, "t-after-ghost")
		if g <= elapsed {
			if math.Abs(afterGhost) > 1e-6 {
				t.Fatalf("g=%v <= elapsed=%v 时竖线后 ghost 应为 0，got %v", g, elapsed, afterGhost)
			}
			if math.Abs(beforeGhost-(g-used)) > 1e-6 {
				t.Fatalf("g=%v <= elapsed=%v 时竖线前 ghost 应为 %v，got %v", g, elapsed, g-used, beforeGhost)
			}
		} else {
			if math.Abs(beforeGhost-(elapsed-used)) > 1e-6 {
				t.Fatalf("g=%v > elapsed=%v 时竖线前 ghost 应为 %v，got %v", g, elapsed, elapsed-used, beforeGhost)
			}
			if math.Abs(afterGhost-(g-elapsed)) > 1e-6 {
				t.Fatalf("g=%v > elapsed=%v 时竖线后 ghost 应为 %v，got %v", g, elapsed, g-elapsed, afterGhost)
			}
		}
	}

	const overUsed, overElapsed = 0.6, 0.4
	for _, g := range []float64{0.7, 1.0} {
		slots := quotaBarSlots("t", quotaBarSegmentsTo(overUsed, overElapsed, g, true))
		assertGrowSum(slots)
		if math.Abs(slotGrow(slots, "t-before-ghost")) > 1e-6 || math.Abs(slotGrow(slots, "t-before-ghost-over")) > 1e-6 {
			t.Fatalf("超速 g=%v 时竖线前不应有虚影", g)
		}
		if math.Abs(slotGrow(slots, "t-after-ghost")) > 1e-6 {
			t.Fatalf("超速 g=%v 时不应使用普通 ghost 槽", g)
		}
		if got, want := slotGrow(slots, "t-after-ghost-over"), g-overUsed; math.Abs(got-want) > 1e-6 {
			t.Fatalf("超速 g=%v 时竖线后 ghost-over 应为 %v，got %v", g, want, got)
		}
		if got, want := slotGrow(slots, "t-after-over"), overUsed-overElapsed; math.Abs(got-want) > 1e-6 {
			t.Fatalf("超速 g=%v 时虚影应从 used 开始（over=%v），got %v", g, want, got)
		}
	}
}

func TestQuotaBarHoverSettles(t *testing.T) {
	for _, reduced := range []bool{false, true} {
		t.Run(fmt.Sprintf("reduced=%v", reduced), func(t *testing.T) {
			setAppReduceMotion(t, reduced)
			projected := false
			tt := ui.NewTesterWith(func(c *ui.Context) {
				ui.Row(c.Key("bar")).Width(200).Height(4).Children(func() {
					renderQuotaBar(c, "hover", 0.2, 0.4, true, projected, ThemeAccent(c), "marker")
				})
			}, 220, 50, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduced}})
			tt.Frame()
			projected = true
			tt.Frame()
			if tt.FramePending() == reduced {
				t.Fatalf("减少动效=%v 时帧排队状态不正确", reduced)
			}
			tt.Advance(DurBase + time.Second)
			tt.Frame()
			if tt.FramePending() {
				t.Fatal("悬停虚影动效应结束")
			}
		})
	}
}

func TestQuotaRedesign_MultiAccountWindowLabelsOnce(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.Local)
	model := buildTrayQuotaModel([]TrayQuotaGroup{{
		Provider: "claude",
		Title:    "Claude",
		Windows: []ProviderQuotaWindow{
			{Account: "a@example.com", Label: "5h", Period: "5h", UsedPercent: 20, ResetsAt: now.Add(2 * time.Hour), Active: true},
			{Account: "a@example.com", Label: "Weekly", Period: "7d", UsedPercent: 80, ResetsAt: now.Add(4 * 24 * time.Hour)},
			{Account: "b@example.com", Label: "5h", Period: "5h", UsedPercent: 0, ResetsAt: now.Add(2 * time.Hour)},
			{Account: "b@example.com", Label: "Weekly", Period: "7d", UsedPercent: 5, ResetsAt: now.Add(5 * 24 * time.Hour)},
		},
	}}, now, now, false)

	tt := ui.NewTester(func(c *ui.Context) {
		renderQuotaGroupList(c, "redesign-once-", model.Groups, quotaGroupListOptions{})
	}, 456, 300)
	tt.Frame()

	countOf := func(target string) int {
		n := 0
		for _, txt := range tt.Texts() {
			if txt == target {
				n++
			}
		}
		return n
	}
	if got := countOf("5h"); got != 1 {
		t.Fatalf("5h 列头应只渲染一次，实际 %d 次: %v", got, tt.Texts())
	}
	if got := countOf("周"); got != 1 {
		t.Fatalf("周 列头应只渲染一次，实际 %d 次: %v", got, tt.Texts())
	}

	// 混合窗口（账号 1 只有月，账号 2 有 5h 和周）：表头为 5h / 周，账号 1 在 5h 列内联标「月」并在周列占位「—」。
	codexModel := buildTrayQuotaModel([]TrayQuotaGroup{{
		Provider: "codex",
		Title:    "Codex",
		Windows: []ProviderQuotaWindow{
			{Account: "a@example.com", Label: "Monthly", Period: "30d", UsedPercent: 10, ResetsAt: now.Add(20 * 24 * time.Hour), Plan: "free"},
			{Account: "b@example.com", Label: "5h", Period: "5h", UsedPercent: 0, ResetsAt: now.Add(3 * time.Hour), Plan: "plus"},
			{Account: "b@example.com", Label: "Weekly", Period: "7d", UsedPercent: 0, ResetsAt: now.Add(5 * 24 * time.Hour), Plan: "plus"},
		},
	}}, now, now, false)

	ttMix := ui.NewTester(func(c *ui.Context) {
		renderQuotaGroupList(c, "redesign-mix-", codexModel.Groups, quotaGroupListOptions{})
	}, 456, 300)
	ttMix.Frame()
	if !ttMix.HasText("月") || !ttMix.HasText("—") {
		t.Fatalf("混合窗口缺月或占位符: %v", ttMix.Texts())
	}
}

func TestQuotaRedesign_NoReadingDisclosure(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.Local)
	model := buildTrayQuotaModel([]TrayQuotaGroup{{
		Provider: "codex",
		Title:    "Codex",
		Windows: []ProviderQuotaWindow{
			{Account: "a@example.com", Label: "5h", Period: "5h", UsedPercent: 10, ResetsAt: now.Add(3 * time.Hour)},
			{Account: "a@example.com", Label: "Image generation", Text: true, Display: "Unknown"},
			{Account: "b@example.com", Label: "5h", Period: "5h", UsedPercent: 20, ResetsAt: now.Add(3 * time.Hour)},
			{Account: "b@example.com", Label: "Image generation", Text: true, Display: "Unknown"},
		},
	}}, now, now, false)

	if len(model.Groups) != 1 || len(model.Groups[0].NoReadings) != 2 {
		t.Fatalf("期望 2 项无读数，实际 %+v", model.Groups)
	}

	tt := ui.NewTester(func(c *ui.Context) {
		renderQuotaGroupList(c, "redesign-nr-", model.Groups, quotaGroupListOptions{})
	}, 456, 360)
	tt.Frame()

	if !tt.HasText("2 项无读数") {
		t.Fatalf("应显示折叠摘要 '2 项无读数': %v", tt.Texts())
	}
	if tt.HasText("Image generation") || tt.HasText("Unknown") {
		t.Fatalf("折叠状态下不应直接展示无读数明细: %v", tt.Texts())
	}

	if err := tt.Click("2 项无读数"); err != nil {
		t.Fatalf("点击展开无读数失败: %v", err)
	}
	tt.Advance(DurBase + time.Second)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("展开无读数动画完成后不应继续请求帧")
	}
	if !tt.HasText("Image generation") || !tt.HasText("Unknown") {
		t.Fatalf("展开后应展示无读数明细: %v", tt.Texts())
	}

	if err := tt.Click("2 项无读数"); err != nil {
		t.Fatalf("点击收起无读数失败: %v", err)
	}
	tt.Advance(DurBase + time.Second)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("收起无读数动画完成后不应继续请求帧")
	}
	if tt.HasText("Image generation") {
		t.Fatalf("收起后不应继续展示无读数明细: %v", tt.Texts())
	}
}

func TestQuotaRedesign_SelectableAccountsAndActiveBadge(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.Local)
	model := buildTrayQuotaModel([]TrayQuotaGroup{{
		Provider: "agy",
		Title:    "Antigravity",
		Windows: []ProviderQuotaWindow{
			{Account: "系统账号", Label: "Gemini 3.1 Pro", Period: "5h", UsedPercent: 100, ResetsAt: now.Add(time.Hour), Active: true},
			{Account: "系统账号", Label: "Gemini 3.1 Pro 7d", Period: "7d", UsedPercent: 60, ResetsAt: now.Add(4 * 24 * time.Hour), Active: true},
			{Account: "work@example.com", Label: "Gemini 3.1 Pro", Period: "5h", UsedPercent: 14, ResetsAt: now.Add(4 * time.Hour)},
			{Account: "work@example.com", Label: "Gemini 3.1 Pro 7d", Period: "7d", UsedPercent: 21, ResetsAt: now.Add(5 * 24 * time.Hour)},
		},
	}}, now, now, false)

	selected := ""
	var picked []string
	opts := quotaGroupListOptions{
		HideSingleGroupTitle: true,
		SelectedAccount:      selected,
		OnSelectAccount: func(account string) {
			picked = append(picked, account)
			selected = account
		},
		AccountDisabled: func(account string) bool {
			return account == "系统账号"
		},
	}

	tt := ui.NewTester(func(c *ui.Context) {
		opts.SelectedAccount = selected
		renderQuotaGroupList(c, "redesign-sel-", model.Groups, opts)
	}, 456, 300)
	tt.Frame()

	// 自动选号（SelectedAccount == ""）：单选圈都不选中，IsActive 账号显示「在用」微标，禁用行不可点。
	if !tt.HasText("在用") {
		t.Fatalf("自动选号模式下 IsActive 账号应显示「在用」微标: %v", tt.Texts())
	}
	sysBtn, ok := tt.AccessByRole(ui.RoleButton, "系统账号")
	if !ok || !sysBtn.Disabled || sysBtn.Checked {
		t.Fatalf("系统账号应禁用且未选中: ok=%v node=%+v", ok, sysBtn)
	}
	workBtn, ok := tt.AccessByRole(ui.RoleButton, "work@example.com")
	if !ok || workBtn.Disabled || workBtn.Checked {
		t.Fatalf("自动选号模式下 work@example.com 应可用且未选中: ok=%v node=%+v", ok, workBtn)
	}

	if err := tt.Click("系统账号"); err != nil {
		t.Fatalf("点击禁用行失败: %v", err)
	}
	if len(picked) != 0 {
		t.Fatalf("禁用行不应触发选择回调，picked=%v", picked)
	}

	// 指定账号后：SelectedAccount 对应的单选圈选中，不再显示「在用」微标。
	if err := tt.Click("work@example.com"); err != nil {
		t.Fatalf("点击可用账号失败: %v", err)
	}
	if len(picked) != 1 || picked[0] != "work@example.com" {
		t.Fatalf("应触发 OnSelectAccount('work@example.com')，实际 %v", picked)
	}
	tt.Advance(DurFast + time.Second)
	tt.Frame()
	if tt.HasText("在用") {
		t.Fatalf("指定账号后不应再显示「在用」微标: %v", tt.Texts())
	}
	workBtn, ok = tt.AccessByRole(ui.RoleButton, "work@example.com")
	if !ok || !workBtn.Checked {
		t.Fatalf("指定账号后 work@example.com 应为 Checked: ok=%v node=%+v", ok, workBtn)
	}
	sysBtn, ok = tt.AccessByRole(ui.RoleButton, "系统账号")
	if !ok || sysBtn.Checked {
		t.Fatalf("未指定账号不应为 Checked: ok=%v node=%+v", ok, sysBtn)
	}
}

func TestQuotaRedesign_HoverAndExpandMotionSettles(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.Local)
	model := buildTrayQuotaModel([]TrayQuotaGroup{{
		Provider: "claude",
		Title:    "Claude",
		Windows: []ProviderQuotaWindow{
			{Account: "a@example.com", Label: "5h", Period: "5h", UsedPercent: 20, ResetsAt: now.Add(2 * time.Hour), Active: true},
			{Account: "a@example.com", Label: "Weekly", Period: "7d", UsedPercent: 60, ResetsAt: now.Add(4 * 24 * time.Hour)},
			{Account: "b@example.com", Label: "5h", Period: "5h", UsedPercent: 5, ResetsAt: now.Add(2 * time.Hour)},
			{Account: "b@example.com", Label: "Weekly", Period: "7d", UsedPercent: 10, ResetsAt: now.Add(5 * 24 * time.Hour)},
			{Account: "b@example.com", Label: "Image generation", Text: true, Display: "Unknown"},
		},
	}}, now, now, false)

	for _, reduced := range []bool{false, true} {
		t.Run(fmt.Sprintf("reduced=%v", reduced), func(t *testing.T) {
			setAppReduceMotion(t, reduced)
			tt := ui.NewTesterWith(func(c *ui.Context) {
				ui.Scroll(c.Key("scroll")).FillWidth().MaxHeight(260).Children(func() {
					renderQuotaGroupList(c, "redesign-motion-", model.Groups, quotaGroupListOptions{})
				})
			}, 456, 320, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduced}})
			tt.Frame()
			if tt.FramePending() {
				t.Fatal("初始静止时不应请求下一帧")
			}

			rowRect, ok := tt.Find("a@example.com")
			if !ok {
				t.Fatalf("找不到账号行: %v", tt.Texts())
			}
			tt.Move(rowRect.X+rowRect.W/2, rowRect.Y+rowRect.H/2)
			tt.Frame()

			detail := model.Groups[0].Accounts[0].hoverLeft
			if detail == "" {
				detail = model.Groups[0].Accounts[0].HoverDetail
			}
			if !tt.HasText(detail) {
				t.Fatalf("悬停后应浮出完整详情 %q: %v", detail, tt.Texts())
			}

			tt.Advance(DurBase + time.Second)
			tt.Frame()
			if tt.FramePending() {
				t.Fatal("悬停详情与虚影动效完成后不应继续请求下一帧")
			}

			// 移开指针后浮条退出并停止请求帧。
			tt.Move(0, 0)
			tt.Advance(DurBase + time.Second)
			tt.Frame()
			if tt.FramePending() {
				t.Fatal("离开悬停动效完成后不应继续请求下一帧")
			}
			if tt.HasText(detail) {
				t.Fatalf("离开悬停后浮条应消失: %v", tt.Texts())
			}

			// 展开无读数折叠项后动画结束不再请求下一帧。
			if err := tt.Click("1 项无读数"); err != nil {
				t.Fatalf("点击展开失败: %v", err)
			}
			tt.Advance(DurBase + time.Second)
			tt.Frame()
			if tt.FramePending() {
				t.Fatal("展开动画完成后不应继续请求下一帧")
			}
		})
	}
}
