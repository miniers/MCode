package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func TestInterfaceScaleHeights(t *testing.T) {
	previousDark := themeDark.Load()
	t.Cleanup(func() { setActiveTheme(config.AppearanceConfig{}); themeDark.Store(previousDark) })
	for _, dark := range []bool{false, true} {
		for _, percent := range []int{100, 150} {
			cfg := config.AppearanceConfig{}
			for _, id := range []string{config.ThemeLight, config.ThemeDark} {
				if err := cfg.SetThemeFont(id, "interface", percent); err != nil {
					t.Fatal(err)
				}
			}
			setActiveTheme(cfg)
			themeDark.Store(dark)
			factor := float32(percent) / 100
			if got := uiScaleFactor(); got != factor {
				t.Fatalf("dark=%v percent=%d: factor=%v", dark, percent, got)
			}
			if got := uiSize(ControlXS); got != ControlXS*factor {
				t.Errorf("dark=%v percent=%d: uiSize(ControlXS)=%v want=%v", dark, percent, got, ControlXS*factor)
			}
			tester := ui.NewTester(func(c *ui.Context) {
				ui.Column(c).Children(func() {
					Capsule(c, CapsuleProps{Text: "chip"})
					CopyButton(c, CopyButtonProps{Label: "copy", Text: "copy", Mode: CopyText})
					Kbd(c, nil, "kbd").Label("kbd")
				})
			}, 300, 200)
			for _, item := range []struct {
				label  string
				height float32
			}{{"chip", ControlSM}, {"copy", ControlXS}, {"kbd", ControlKbd}} {
				bounds, ok := tester.Find(item.label)
				if !ok || bounds.H != item.height*factor {
					t.Errorf("dark=%v percent=%d %s: height=%v found=%v want=%v", dark, percent, item.label, bounds.H, ok, item.height*factor)
				}
			}
		}
	}
}
