package mcodeui

import (
	"bytes"
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func TestSettingsRedesignShell_SwarmIconDistinctFromMagpie(t *testing.T) {
	if IconSwarm() == IconNetwork() {
		t.Fatal("IconSwarm 与 IconNetwork 不应指向同一图标")
	}
	swarmTT := ui.NewTester(func(c *ui.Context) {
		ui.Icon(c, IconSwarm()).Size(IconLG, IconLG).TextColor(ThemePrimaryText(c))
	}, 48, 48)
	networkTT := ui.NewTester(func(c *ui.Context) {
		ui.Icon(c, IconNetwork()).Size(IconLG, IconLG).TextColor(ThemePrimaryText(c))
	}, 48, 48)
	swarmTT.Frame()
	networkTT.Frame()
	if bytes.Equal(swarmTT.Image().Pix, networkTT.Image().Pix) {
		t.Fatal("编排图标与 Magpie 图标渲染像素完全一致")
	}
}

func TestSettingsRedesignShell_SidebarWidth220(t *testing.T) {
	for _, winW := range []int{960, 480} {
		st := NewSettingsState(settingsTestDir(t), nil, nil)
		st.open = true
		st.currentPage = "appearance"

		tt := ui.NewTester(func(c *ui.Context) {
			renderSettingsWindow(c, st)
		}, winW, 680)
		tt.Frame()

		navItem, ok := tt.Find("通用")
		if !ok {
			t.Fatalf("winW=%d 找不到侧栏导航项: %v", winW, tt.Texts())
		}
		if navItem.X+navItem.W > 220 {
			t.Fatalf("winW=%d 侧栏导航项越过 220: %+v", winW, navItem)
		}
		detail, ok := tt.Find("设置详情列")
		if !ok {
			t.Fatalf("winW=%d 找不到设置详情列", winW)
		}
		if detail.X < 220 {
			t.Fatalf("winW=%d 详情列起点应不小于 220，实际 %+v", winW, detail)
		}
		if winW == 480 && detail.X != 220+Space7 {
			t.Fatalf("winW=480 窄窗下侧栏应保持 220 且详情区左侧保留 24pt 内边距: detail=%+v", detail)
		}
	}
}

func TestSettingsRedesignShell_RowMinHeight44(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	section := settingsSection{
		ID:    "test-row-height",
		Title: "测试行高",
		Rows: []setting{
			{
				ID:      "row-height-a",
				Title:   "第一行",
				Control: toggle(val(func(c *config.Config) *bool { return &c.Notifications.NotifyWhenFinished }, true)),
			},
			{
				ID:      "row-height-b",
				Title:   "第二行",
				Control: toggle(val(func(c *config.Config) *bool { return &c.Notifications.NotifyOnApproval }, true)),
			},
		},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsTreeSection(c, st, section, false)
	}, 680, 400)
	tt.Frame()

	rowA, okA := tt.Find("第一行")
	rowB, okB := tt.Find("第二行")
	if !okA || !okB {
		t.Fatalf("找不到测试行: %v", tt.Texts())
	}
	if gap := rowB.Y - rowA.Y; gap < 44 {
		t.Fatalf("相邻设置行间距 = %.1f，期望不低于 44", gap)
	}
}

func TestSettingsRedesignShell_ContentMaxWidth680(t *testing.T) {
	for _, winW := range []int{1400, 960} {
		st := NewSettingsState(settingsTestDir(t), nil, nil)
		st.open = true
		st.currentPage = "general"

		tt := ui.NewTester(func(c *ui.Context) {
			renderSettingsWindow(c, st)
		}, winW, 680)
		tt.Frame()

		detail, ok := tt.Find("设置详情列")
		if !ok {
			t.Fatalf("winW=%d 找不到设置详情列", winW)
		}
		if detail.W > 680 {
			t.Fatalf("winW=%d 内容列宽 %.1f 超过 680", winW, detail.W)
		}
		if detail.W != 680 {
			t.Fatalf("winW=%d 内容列宽 = %.1f，期望 680", winW, detail.W)
		}
	}
}

func TestSettingsRedesignShell_SheetWidthDefaults(t *testing.T) {
	if settingsSheetWidth != 600 {
		t.Fatalf("settingsSheetWidth = %v，期望 600", settingsSheetWidth)
	}
	if settingsSheetNarrowWidth != 440 {
		t.Fatalf("settingsSheetNarrowWidth = %v，期望 440", settingsSheetNarrowWidth)
	}
	if settingsSheetWideWidth != 720 {
		t.Fatalf("settingsSheetWideWidth = %v，期望 720", settingsSheetWideWidth)
	}

	for _, tc := range []struct {
		name      string
		widthSpec float32
		wantW     float32
	}{
		{"默认宽度", 0, 600},
		{"窄面板宽度", settingsSheetNarrowWidth, 440},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "shell-width-" + tc.name
			modalDragReset("settings-sheet-" + id)
			t.Cleanup(func() { modalDragReset("settings-sheet-" + id) })

			st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
			sh := settingsSheet{
				ID:    id,
				Title: tc.name,
				Width: tc.widthSpec,
				Body:  func(c *ui.Context, st *SettingsState) { ui.Text(c, "内容") },
			}
			openSettingsSheet(st, id, "")
			t.Cleanup(func() { closeSettingsSheet(st) })

			tt := ui.NewTester(func(c *ui.Context) {
				renderSettingsTreeSheet(c, st, sh)
			}, 1200, 800)
			tt.Frame()

			bounds, ok := tt.Find("设置面板 " + tc.name)
			if !ok {
				t.Fatalf("找不到面板: %v", tt.Texts())
			}
			if bounds.W != tc.wantW {
				t.Fatalf("面板宽度 = %.1f，期望 %.1f", bounds.W, tc.wantW)
			}
		})
	}
}

func TestSettingsRedesignShell_MagpieDisconnectedTitle(t *testing.T) {
	fake := NewFakeClient()
	chat := newChatState(&routeTestClient{FakeClient: fake})
	t.Cleanup(chat.close)
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	st.open = true
	st.currentPage = "general"

	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsWindow(c, st)
	}, 960, 680)
	waitMagpie(t, tt, func() bool {
		_, known := magpieConnected(st)
		return known
	})

	if !tt.HasText("Magpie") {
		t.Fatalf("未连接时 Magpie 导航项应显示「Magpie」: %v", tt.Texts())
	}
	if tt.HasText("连接 Magpie") {
		t.Fatalf("未连接时不应再显示「连接 Magpie」: %v", tt.Texts())
	}
	if _, ok := tt.Find("已连接"); ok {
		t.Fatal("未连接时不应显示已连接圆点")
	}
}

func TestSettingsRedesignShell_DisabledRowTitleHalfOpacity(t *testing.T) {
	setAppReduceMotion(t, true)
	opts := ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}}

	disabledRow := setting{
		ID:      "row-disabled-opacity",
		Title:   "禁用标题",
		Enabled: func(*SettingsState) bool { return false },
		Control: choice(val(func(c *config.Config) *string { return &c.Appearance.ConversationDensity }, config.DensityComfortable), []choiceOption{{Value: config.DensityComfortable, Label: "舒适"}}),
	}
	enabledRow := setting{
		ID:      "row-disabled-opacity",
		Title:   "禁用标题",
		Enabled: func(*SettingsState) bool { return true },
		Control: choice(val(func(c *config.Config) *string { return &c.Appearance.ConversationDensity }, config.DensityComfortable), []choiceOption{{Value: config.DensityComfortable, Label: "舒适"}}),
	}

	st := NewSettingsState(settingsTestDir(t), nil, nil)
	disabledTT := ui.NewTesterWith(func(c *ui.Context) {
		ui.Column(c).FillWidth().Children(func() {
			renderSettingsTreeSetting(c, st, disabledRow)
		})
	}, 500, 120, opts)
	enabledTT := ui.NewTesterWith(func(c *ui.Context) {
		ui.Column(c).FillWidth().Children(func() {
			renderSettingsTreeSetting(c, st, enabledRow)
		})
	}, 500, 120, opts)
	refHalfTT := ui.NewTesterWith(func(c *ui.Context) {
		ui.Column(c).FillWidth().Opacity(0.5).Children(func() {
			renderSettingsTreeSetting(c, st, enabledRow)
		})
	}, 500, 120, opts)

	disabledTT.Frame()
	enabledTT.Frame()
	refHalfTT.Frame()

	titleRect, ok := disabledTT.Find("禁用标题")
	if !ok {
		t.Fatal("找不到禁用行标题")
	}
	refTitleRect, ok := refHalfTT.Find("禁用标题")
	if !ok {
		t.Fatal("找不到参照行标题")
	}
	if titleRect != refTitleRect {
		t.Fatalf("禁用行标题位置与参照行不一致: disabled=%+v ref=%+v", titleRect, refTitleRect)
	}
	crop := func(img *image.RGBA, r ui.Rect) []byte {
		rect := image.Rect(int(r.X), int(r.Y), int(r.X+r.W), int(r.Y+r.H)).Intersect(img.Bounds())
		var out []byte
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			for x := rect.Min.X; x < rect.Max.X; x++ {
				off := img.PixOffset(x, y)
				out = append(out, img.Pix[off:off+4]...)
			}
		}
		return out
	}

	disabledPix := crop(disabledTT.Image(), titleRect)
	enabledPix := crop(enabledTT.Image(), titleRect)
	refHalfPix := crop(refHalfTT.Image(), refTitleRect)
	if bytes.Equal(disabledPix, enabledPix) {
		t.Fatal("禁用行左侧标题未应用半透明样式")
	}
	if !bytes.Equal(disabledPix, refHalfPix) {
		t.Fatal("禁用行左侧标题透明度与 0.5 不一致")
	}
}

func TestSettingsRedesignShell_SheetEnterAnimationSettles(t *testing.T) {
	const id = "shell-anim-settle"
	modalDragReset("settings-sheet-" + id)
	t.Cleanup(func() { modalDragReset("settings-sheet-" + id) })

	sh := settingsSheet{
		ID:    id,
		Title: "动效测试面板",
		Body:  func(c *ui.Context, st *SettingsState) { ui.Text(c, "面板正文") },
	}

	t.Run("开启动效时推进到结束后停止续帧", func(t *testing.T) {
		setAppReduceMotion(t, false)
		st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
		t.Cleanup(func() { closeSettingsSheet(st) })

		tt := ui.NewTester(func(c *ui.Context) {
			renderSettingsTreeSheet(c, st, sh)
		}, 1200, 800)
		tt.Frame()

		openSettingsSheet(st, id, "")
		tt.Frame()
		if !tt.FramePending() {
			t.Fatal("面板刚打开时入场动效应当仍在运行")
		}

		tt.Advance(DurSheet + 100*time.Millisecond)
		tt.Frame()
		if tt.FramePending() {
			t.Fatal("面板入场动画超过 280ms 后仍未停止请求下一帧")
		}
	})

	t.Run("减少动效时直接到终点", func(t *testing.T) {
		setAppReduceMotion(t, true)
		st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
		openSettingsSheet(st, id, "")
		t.Cleanup(func() { closeSettingsSheet(st) })

		tt := ui.NewTesterWith(func(c *ui.Context) {
			renderSettingsTreeSheet(c, st, sh)
		}, 1200, 800, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
		tt.Frame()
		if tt.FramePending() {
			t.Fatal("减少动效时面板打开首帧不应请求下一帧")
		}
	})
}

func TestSettingsRedesignShell_SavedBadgeCenteredInTitlebar(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.currentPage = "general"
	st.cfgMu.Lock()
	st.showSaved = true
	st.cfgMu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsWindow(c, st)
	}, 960, 680)
	tt.Frame()

	badge, ok := tt.Find("已保存")
	if !ok {
		t.Fatalf("找不到「已保存」徽标: %v", tt.Texts())
	}
	center := badge.X + badge.W/2
	if diff := center - 480; diff < -2 || diff > 2 {
		t.Fatalf("「已保存」提示未在 960 顶栏水平居中: center=%.1f badge=%+v", center, badge)
	}
}
