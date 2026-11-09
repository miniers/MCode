package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSectionCardContent(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		SectionCard(c, "section", "设置分组", func() { ui.Text(c, "分组内容") })
	}, 400, 200)
	if !tt.HasText("设置分组") || !tt.HasText("分组内容") {
		t.Fatal("分组标题或内容未显示")
	}
}

func TestBannerTonesAndAction(t *testing.T) {
	clicked := 0
	var colors []ui.Color
	tt := ui.NewTester(func(c *ui.Context) {
		palette := bannerPalette{ThemeAccent(c), ThemeWarning(c), ThemeDanger(c), ThemeSuccess(c)}
		colors = nil
		ui.Column(c).Children(func() {
			for _, tone := range []Tone{ToneAccent, ToneWarning, ToneDanger, ToneSuccess} {
				colors = append(colors, bannerToneColor(tone, palette))
				Banner(c, BannerProps{Tone: tone, Text: "提示", Action: "执行操作", OnAction: func() { clicked++ }})
			}
		})
	}, 400, 400)
	for i, color := range colors {
		for j := 0; j < i; j++ {
			if color == colors[j] {
				t.Errorf("语气 %d 与 %d 颜色相同", i, j)
			}
		}
	}
	if err := tt.Click("执行操作"); err != nil {
		t.Fatal(err)
	}
	if clicked != 1 {
		t.Fatalf("操作回调次数=%d", clicked)
	}
}

func TestTooltipText(t *testing.T) {
	for _, tc := range []struct{ text, shortcut, want string }{
		{"保存", "⌘S", "保存 ⌘S"},
		{"保存", "", "保存"},
		{"", "⌘S", "⌘S"},
		{"", "", ""},
	} {
		if got := tooltipText(tc.text, tc.shortcut); got != tc.want {
			t.Errorf("tooltipText(%q, %q) = %q，期望 %q", tc.text, tc.shortcut, got, tc.want)
		}
	}
}
