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

func TestWorkspacePanelClose(t *testing.T) {
	closed := 0
	tt := ui.NewTester(func(c *ui.Context) {
		WorkspacePanel(c, PanelProps{Title: "面板", OnClose: func() { closed++ }}, func() {
			ui.Text(c, "面板内容")
		})
	}, 400, 200)
	if err := tt.Click("关闭"); err != nil {
		t.Fatal(err)
	}
	if closed != 1 {
		t.Fatalf("关闭回调次数=%d", closed)
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

func TestSkeletonReducedMotion(t *testing.T) {
	for _, progress := range []float32{0, 0.5, 1} {
		if got := skeletonFill(progress, true); got != 0.35 {
			t.Errorf("进度 %v 的静态填充=%v", progress, got)
		}
	}
	if skeletonFill(0, false) == skeletonFill(1, false) {
		t.Error("启用动效时填充未随进度变化")
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
