package mcodeui

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// Kbd 渲染单个键帽。
func Kbd(c *ui.Context, key any, text string) ui.Element {
	ctx := c
	if key != nil {
		ctx = c.Key(key)
	}
	return ui.Row(ctx).
		Height(uiSize(ControlKbd)).
		MinWidth(ControlKbd).
		Padding(Space1, Space2).
		Radius(RadiusXS).
		Border(Space1/4, ThemeBorder(c)).
		Background(ThemeHover(c).Alpha(0.5)).
		Justify(ui.Center).
		AlignItems(ui.Center).
		Children(func() {
			ui.Text(c, text).FontSize(uiFont(TypeCaption)).Font("monospace").TextColor(ThemeSecondaryText(c))
		})
}

// KbdRow 将快捷键按键依次渲染为键帽。
func KbdRow(c *ui.Context, key any, keys ...string) ui.Element {
	ctx := c
	if key != nil {
		ctx = c.Key(key)
	}
	return ui.Row(ctx).Gap(Space1).AlignItems(ui.Center).Children(func() {
		index := 0
		for _, shortcut := range keys {
			for _, label := range kbdKeys(shortcut) {
				Kbd(ctx, index, label)
				index++
			}
		}
	})
}

// kbdKeys 按现有快捷键显示顺序拆分修饰键和主键。
func kbdKeys(shortcut string) []string {
	shortcut = strings.TrimSpace(shortcut)
	if shortcut == "" {
		return nil
	}
	if mods, key, err := parseShortcutSpec(shortcut); err == nil {
		shortcut = formatShortcutKeys(mods, key)
	}
	var keys []string
	for len(shortcut) > 0 {
		matched := false
		for _, modifier := range shortcutModifierSymbols {
			if strings.HasPrefix(shortcut, modifier.symbol) {
				keys = append(keys, modifier.symbol)
				shortcut = strings.TrimPrefix(shortcut, modifier.symbol)
				matched = true
				break
			}
		}
		if !matched {
			keys = append(keys, shortcut)
			break
		}
	}
	return keys
}
