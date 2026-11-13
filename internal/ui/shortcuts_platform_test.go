package mcodeui

import (
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
)

// withShortcutGOOS 临时替换快捷键展示使用的平台标识。
func withShortcutGOOS(t *testing.T, goos string) {
	t.Helper()
	old := shortcutGOOS
	t.Cleanup(func() { shortcutGOOS = old })
	shortcutGOOS = goos
}

// TestShortcutDisplay 验证非 macOS 把符号键位写成 Ctrl+Shift+P，macOS 原样保留。
func TestShortcutDisplay(t *testing.T) {
	cases := map[string]string{
		"⇧⌘P":  "Ctrl+Shift+P",
		"⌃⌘A":  "Ctrl+A",
		"⌥⌘↑":  "Ctrl+Alt+↑",
		"⌘↩":   "Ctrl+Enter",
		"↩":    "Enter",
		"⌘1…9": "Ctrl+1…9",
		"Esc":  "Esc",
	}
	withShortcutGOOS(t, "windows")
	for in, want := range cases {
		if got := shortcutDisplay(in); got != want {
			t.Errorf("windows: shortcutDisplay(%q) = %q，期望 %q", in, got, want)
		}
	}
	shortcutGOOS = "darwin"
	for in := range cases {
		if got := shortcutDisplay(in); got != in {
			t.Errorf("darwin: shortcutDisplay(%q) = %q，期望原样", in, got)
		}
	}
}

// TestKbdKeysNonDarwin 验证非 macOS 的键帽按 Ctrl / Shift / 主键拆分。
func TestKbdKeysNonDarwin(t *testing.T) {
	withShortcutGOOS(t, "windows")
	cases := map[string][]string{
		"Cmd+Shift+P": {"Ctrl", "Shift", "P"},
		"⌘K":          {"Ctrl", "K"},
		"⌘1…9":        {"Ctrl", "1…9"},
		"Esc":         {"Esc"},
	}
	for in, want := range cases {
		if got := kbdKeys(in); !reflect.DeepEqual(got, want) {
			t.Errorf("kbdKeys(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestArchiveThreadKeepsSelectAll 验证归档会话的默认键位不占用 ⌘A / Ctrl+A（输入框全选）。
// 非 macOS 上 Cmd 与 Ctrl 是同一修饰位，⌃⌘A 曾退化成 Ctrl+A，在输入框里按全选会归档会话。
func TestArchiveThreadKeepsSelectAll(t *testing.T) {
	setShortcutOverrides(nil)
	for _, mods := range []ui.Modifiers{ui.Cmd, ui.Ctrl} {
		if id, _, ok := matchShortcut(mods, ui.KeyA); ok {
			t.Fatalf("修饰位 %v + A 不应命中快捷键，却命中了 %q", mods, id)
		}
	}
}
