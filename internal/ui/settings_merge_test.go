package mcodeui

import (
	"reflect"
	"testing"

	"mcode/internal/config"
)

// mergeForTest 按 save 的调用方式做三方合并（latest 取 Elem 才可写）。
func mergeForTest(latest *config.Config, base, edited config.Config) {
	mergeConfigChanges(reflect.ValueOf(latest).Elem(), reflect.ValueOf(base), reflect.ValueOf(edited))
}

// 设置页没动供应商字段时，别处（供应商页、预设供应商页）刚写入的值不能被回退。
func TestMergeConfigChangesKeepsLatestProviderWrites(t *testing.T) {
	base := config.Defaults()
	edited := cloneConfig(base)
	latest := cloneConfig(base)
	latest.Providers.Presets = map[string]config.PresetProviderConfig{
		"deepseek": {Enabled: true, BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat"},
	}
	latest.Providers.Custom = []config.CustomEndpointConfig{
		{ID: "local", Name: "本地", BaseURL: "http://127.0.0.1:8080/v1", Model: "qwen", Enabled: true},
	}

	mergeForTest(&latest, base, edited)

	if got := latest.Providers.Presets["deepseek"]; got.Model != "deepseek-chat" || !got.Enabled {
		t.Fatalf("预设供应商被回退: %+v", got)
	}
	if len(latest.Providers.Custom) != 1 || latest.Providers.Custom[0].ID != "local" {
		t.Fatalf("自定义端点被回退: %+v", latest.Providers.Custom)
	}
}

// 设置页改过的字段取设置页的值，其它字段保留磁盘上的最新值。
func TestMergeConfigChangesAppliesEditedField(t *testing.T) {
	base := config.Defaults()
	edited := cloneConfig(base)
	latest := cloneConfig(base)
	_ = edited.Appearance.SetThemeFont(config.ThemeLight, "interface", 130)
	latest.Appearance.Theme = config.ThemeDark
	latest.Models.PinLimit = 12

	mergeForTest(&latest, base, edited)

	if latest.Appearance.ThemeFonts(config.ThemeLight)["interface"] != 130 {
		t.Fatalf("文字大小应取设置页的值，实际 %d", latest.Appearance.ThemeFonts(config.ThemeLight)["interface"])
	}
	if latest.Appearance.Theme != config.ThemeDark {
		t.Fatalf("主题应保留磁盘上的最新值，实际 %q", latest.Appearance.Theme)
	}
	if latest.Models.PinLimit != 12 {
		t.Fatalf("模型列表上限应保留磁盘上的最新值，实际 %d", latest.Models.PinLimit)
	}
}

// slice 与 map 字段被设置页改动时整体替换，并且不与设置页快照共享底层存储。
func TestMergeConfigChangesReplacesSliceAndMapWithoutAliasing(t *testing.T) {
	base := config.Defaults()
	edited := cloneConfig(base)
	latest := cloneConfig(base)
	edited.Models.Pins = append(cloneConfig(base).Models.Pins, config.ModelPin{Provider: "claude", ModelID: "claude-opus"})
	edited.Shortcuts.ShortcutOverrides = map[string]string{"toggle-sidebar": "Cmd+B"}

	mergeForTest(&latest, base, edited)

	if !reflect.DeepEqual(latest.Models.Pins, edited.Models.Pins) {
		t.Fatalf("模型列表应整体取设置页的值，实际 %+v", latest.Models.Pins)
	}
	if got := latest.Shortcuts.ShortcutOverrides["toggle-sidebar"]; got != "Cmd+B" {
		t.Fatalf("快捷键覆盖应整体取设置页的值，实际 %+v", latest.Shortcuts.ShortcutOverrides)
	}

	// 改设置页那一份不应影响合并结果，说明写进 latest 的是深拷贝。
	edited.Models.Pins[0].ModelID = "changed"
	edited.Shortcuts.ShortcutOverrides["toggle-sidebar"] = "changed"
	if got := latest.Models.Pins[0].ModelID; got == "changed" {
		t.Fatalf("模型列表与设置页快照共享底层存储")
	}
	if got := latest.Shortcuts.ShortcutOverrides["toggle-sidebar"]; got == "changed" {
		t.Fatalf("快捷键覆盖与设置页快照共享底层 map")
	}
}
