package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestChatMarkdownThemeSwitch(t *testing.T) {
	resetRuntimeTheme(t)
	setAppReduceMotion(t, true)
	s := &chatState{}
	var expected ui.Color
	tester := ui.NewTester(func(c *ui.Context) {
		ensureChatMarkdown(c, s)
		expected = ThemePrimaryText(c)
	}, 100, 100)

	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	tester.SetDark(true)
	tester.Frame()
	dark := s.mdOpts.Theme
	if dark == nil || dark.Text != expected || dark.Text != ColorTextPrimaryDark {
		t.Fatal("深色 Markdown 正文颜色不正确")
	}
	if s.mdStaticOpts.Theme != dark {
		t.Fatal("静态 Markdown 应使用相同的主题")
	}
	tester.Frame()
	if s.mdOpts.Theme != dark {
		t.Fatal("深色模式不变时不应重建主题")
	}

	tester.SetDark(false)
	tester.Frame()
	light := s.mdOpts.Theme
	if light == nil || light == dark || light.Text != expected || light.Text != ColorTextPrimaryLight {
		t.Fatal("切换浅色模式时应重建 Markdown 主题和正文颜色")
	}
	if s.mdStaticOpts.Theme != light || light.CodeColors == dark.CodeColors {
		t.Fatal("切换浅色模式时应更新静态主题和代码配色")
	}
	tester.Frame()
	if s.mdOpts.Theme != light {
		t.Fatal("浅色模式不变时不应重建主题")
	}

	tester.SetDark(true)
	tester.Frame()
	if s.mdOpts.Theme == light || s.mdOpts.Theme.Text != ColorTextPrimaryDark {
		t.Fatal("切回深色模式时应重建 Markdown 主题")
	}
}
