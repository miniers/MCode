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
					renderQuotaBarSegments(c, "motion-quota", quotaBarSegments(used, 0.5, false, false), ThemeAccent(c), "marker")
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
