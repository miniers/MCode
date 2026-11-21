package mcodeui

import (
	"bytes"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func TestSettingsSearchHighlightVisibleWithReduceMotion(t *testing.T) {
	setAppReduceMotion(t, true)
	old := settingsRowFocus
	t.Cleanup(func() { settingsRowFocus = old })

	settingsRowFocus.jumpID = ""
	settingsRowFocus.highlightID = "target-row"
	settingsRowFocus.dynamicJump = false
	settingsRowFocus.onJump = nil

	paint := settingsRowHighlightPaintFor("target-row", settingsRowFocus.highlightID, 1)
	if paint.BorderAlpha <= 0 {
		t.Fatalf("减少动效时静态高亮描边透明度应大于 0，实际 %v", paint.BorderAlpha)
	}

	opts := ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}}
	actual := ui.NewTesterWith(func(c *ui.Context) {
		ui.Scroll(c).Fill().Children(func() {
			row := ui.Row(c.Key("target-row")).FillWidth().Height(ControlMD)
			applySettingsRowFocus(c, row, "target-row").Children(func() { ui.Text(c, "命中行") })
		})
	}, 300, 100, opts)
	expected := ui.NewTesterWith(func(c *ui.Context) {
		ui.Scroll(c).Fill().Children(func() {
			ui.Row(c.Key("target-row")).FillWidth().Height(ControlMD).
				Border(settingsSearchPulseBorder, ThemeAccent(c).Alpha(settingsSearchPulseMaxAlpha)).
				Radius(settingsSearchPulseRadius).
				Children(func() { ui.Text(c, "命中行") })
		})
	}, 300, 100, opts)
	noHighlight := ui.NewTesterWith(func(c *ui.Context) {
		ui.Scroll(c).Fill().Children(func() {
			ui.Row(c.Key("target-row")).FillWidth().Height(ControlMD).
				Children(func() { ui.Text(c, "命中行") })
		})
	}, 300, 100, opts)

	actual.Frame()
	expected.Frame()
	noHighlight.Frame()

	if !bytes.Equal(actual.Image().Pix, expected.Image().Pix) {
		t.Fatal("减少动效时搜索命中行未渲染静态可见描边")
	}
	if bytes.Equal(actual.Image().Pix, noHighlight.Image().Pix) {
		t.Fatal("减少动效时搜索命中行描边不可见")
	}
	if actual.FramePending() {
		t.Fatal("减少动效时搜索高亮不应请求下一帧动画")
	}
}

func TestSettingsNavSearchClearable(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.currentPage = "general"

	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsWindow(c, st)
	}, 960, 680)
	tt.Frame()

	if _, ok := tt.Find("清除搜索"); ok {
		t.Fatal("空搜索不应显示清除按钮")
	}

	if err := tt.Click("搜索设置"); err != nil {
		t.Fatalf("点击导航搜索框失败: %v", err)
	}
	tt.Type("主题")
	tt.Frame()

	if _, ok := tt.Find("清除搜索"); !ok {
		t.Fatalf("输入后导航搜索框应显示清除按钮: %v", tt.Texts())
	}

	if err := tt.Click("清除搜索"); err != nil {
		t.Fatalf("点击清除搜索失败: %v", err)
	}
	tt.Frame()

	if st.navSearch != "" {
		t.Fatalf("点击清除后搜索词应为空，实际 %q", st.navSearch)
	}
	if _, ok := tt.Find("清除搜索"); ok {
		t.Fatal("清空后不应再显示清除按钮")
	}
}

func TestSettingsReset_CancelAutoFocus(t *testing.T) {
	tt, st := newSettingsAdvancedTester(t)
	openSettingsAdvancedPage(t, tt, st, "conversation")
	waitFor(t, tt, func() bool { return tt.HasText("恢复本页默认") })

	def := config.Defaults()
	st.cfg.Session.QueueWhileRunning = !def.Session.QueueWhileRunning
	tt.Frame()

	openSettingsResetDialog(t, tt, st, "conversation")
	if !tt.Focused("取消") {
		t.Fatal("恢复本页默认确认框应默认聚焦「取消」")
	}
	if tt.Focused("确认恢复本页默认") {
		t.Fatal("恢复本页默认确认框不应默认聚焦确认按钮")
	}

	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool { return st.resetPendingPage == "" })
	if st.cfg.Session.QueueWhileRunning == def.Session.QueueWhileRunning {
		t.Fatal("默认聚焦取消时按 Enter 不应重置配置")
	}
}
