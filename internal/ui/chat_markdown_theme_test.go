package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"
	"mcode/internal/config"
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

// 代码块语法色必须来自 mcode 的主题，而不是 mygo 固定的 GitHub 配色。
func TestMarkdownCodeColorsFollowTheme(t *testing.T) {
	resetRuntimeTheme(t)
	setAppReduceMotion(t, true)
	custom := func(base string, colors map[string]string) config.AppearanceConfig {
		return config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: base, Colors: colors}}}
	}
	cases := []struct {
		name   string
		cfg    config.AppearanceConfig
		dark   bool
		accent ui.Color
	}{
		{"light", config.AppearanceConfig{}, false, ui.Hex("#0062CC")},
		{"dark", config.AppearanceConfig{}, true, ui.Hex("#0A84FF")},
		{"light-accent", custom(config.ThemeLight, map[string]string{"accent": "#123456"}), false, ui.Hex("#123456")},
		{"dark-accent", custom(config.ThemeDark, map[string]string{"accent": "#123456"}), true, ui.Hex("#123456")},
	}
	for _, tc := range cases {
		setActiveTheme(tc.cfg)
		appReduceMotion.Store(true)
		var mdTheme *markdown.Theme
		tt := ui.NewTester(func(c *ui.Context) { mdTheme = buildMarkdownTheme(c, 13, "", 12) }, 100, 100)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		tt.SetDark(tc.dark)
		tt.Frame()
		cc := mdTheme.CodeColors
		if cc == nil {
			t.Fatalf("%s: 代码块缺少语法配色", tc.name)
		}
		if got := cc.TokenColor(editor.TokenKeyword); got != tc.accent {
			t.Fatalf("%s: keyword 色 = %v，期望强调色 %v", tc.name, got, tc.accent)
		}
		for _, k := range []editor.TokenKind{editor.TokenString, editor.TokenType, editor.TokenComment, editor.TokenOperator, editor.TokenNumber, editor.TokenVariable, editor.TokenPunctuation, editor.TokenFunction} {
			if cc.Tokens[k].Color != (ui.Color{}) {
				continue
			}
			t.Fatalf("%s: token %d 未设置颜色", tc.name, k)
		}
		if cc.TokenColor(editor.TokenKeyword) == cc.TokenColor(editor.TokenString) || cc.TokenColor(editor.TokenString) == cc.TokenColor(editor.TokenType) {
			t.Fatalf("%s: 关键字/字符串/类型应使用不同语义色", tc.name)
		}
	}
	setActiveTheme(custom(config.ThemeLight, map[string]string{"success": "#112233", "plan": "#445566"}))
	appReduceMotion.Store(true)
	var overwritten *markdown.Theme
	tt := ui.NewTester(func(c *ui.Context) { overwritten = buildMarkdownTheme(c, 13, "", 12) }, 100, 100)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	if got := overwritten.CodeColors.TokenColor(editor.TokenString); got != ui.Hex("#112233") {
		t.Fatalf("字符串色应跟随 success: %v", got)
	}
	if got := overwritten.CodeColors.TokenColor(editor.TokenType); got != ui.Hex("#445566") {
		t.Fatalf("类型色应跟随 plan: %v", got)
	}
}

// 主题未变时 markdownThemeChanged 不得误判，否则每帧重建 Markdown 主题。
func TestMarkdownThemeStableWhenUnchanged(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.AppearanceConfig
		dark bool
	}{
		{"light", config.AppearanceConfig{}, false},
		{"dark", config.AppearanceConfig{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetRuntimeTheme(t)
			setAppReduceMotion(t, true)
			setActiveTheme(tc.cfg)
			s := &chatState{}
			var changed bool
			tt := ui.NewTester(func(c *ui.Context) {
				ensureChatMarkdown(c, s)
				changed = markdownThemeChanged(c, s.mdOpts.Theme)
			}, 100, 100)
			tt.SetPreferences(ui.Preferences{ReduceMotion: true})
			tt.SetDark(tc.dark)
			for i := 0; i < 3; i++ {
				tt.Frame()
				if changed {
					t.Fatalf("主题未变时第 %d 帧不应重建", i)
				}
			}
			before := s.mdOpts.Theme
			tt.Frame()
			if s.mdOpts.Theme != before {
				t.Fatal("主题未变时不应替换 Markdown 主题实例")
			}
		})
	}
}
