package mcodeui

import "github.com/egoist/mygo/ui"

// settingStatus 渲染一行次要色小字，只用于运行时值，不写说明句。
func settingStatus(c *ui.Context, key string, value string) {
	ui.Row(c.Key(key)).Children(func() {
		ui.Text(c, value).
			FontSize(TypeFootnote).
			TextColor(ThemeSecondaryText(c)).
			SingleLine()
	})
}

// settingBadge 渲染短徽标，例如锁定行的「锁定」。
func settingBadge(c *ui.Context, key string, label string) {
	Badge(c, key, label, ToneNeutral)
}
