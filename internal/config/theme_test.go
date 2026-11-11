package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestThemeAddRenameRemove(t *testing.T) {
	a := AppearanceConfig{Theme: ThemeSystem}
	if _, ok := a.AddTheme("missing"); ok {
		t.Fatal("不存在的源主题不应复制")
	}
	if err := a.SetThemeColor(ThemeLight, "text", "#123456"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetThemeFont(ThemeLight, "code", 18); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"浅色 副本", "浅色 副本 2", "浅色 副本 3"} {
		theme, ok := a.AddTheme(ThemeLight)
		if !ok || theme.Name != name || theme.Base != ThemeLight {
			t.Fatalf("复制结果：%+v, %v", theme, ok)
		}
		if _, err := uuid.Parse(theme.ID); err != nil {
			t.Fatal(err)
		}
		if err := a.SetThemeColor(theme.ID, "text", "#abcdef"); err != nil {
			t.Fatal(err)
		}
		if err := a.SetThemeFont(theme.ID, "code", 20); err != nil {
			t.Fatal(err)
		}
		source, _ := a.ThemeByID(ThemeLight)
		if source.Colors["text"] != "#123456" || source.Fonts["code"] != 18 {
			t.Fatal("复制未深拷贝覆盖项")
		}
	}
	id := a.Themes[1].ID
	if a.RenameTheme(ThemeLight, "x") || a.RenameTheme(id, "  ") || a.RenameTheme("missing", "x") {
		t.Fatal("非法重命名被接受")
	}
	if !a.RenameTheme(id, "  自定义  ") || a.ThemeDisplayName(id) != "自定义" {
		t.Fatal("用户主题重命名失败")
	}
	if a.RemoveTheme(ThemeLight) || a.RemoveTheme(ThemeDark) || a.RemoveTheme("missing") {
		t.Fatal("不应删除内置或不存在的主题")
	}
	a.Theme = id
	if !a.RemoveTheme(id) || a.Theme != ThemeSystem {
		t.Fatal("删除当前主题未回退 system")
	}
	if _, ok := a.ThemeByID(id); ok {
		t.Fatal("主题未删除")
	}
}

func TestThemeSetResetSparseOverrides(t *testing.T) {
	var a AppearanceConfig
	if err := a.SetThemeColor(ThemeDark, "accent", ThemeAccentSystem); err != nil {
		t.Fatal(err)
	}
	if err := a.SetThemeFont(ThemeDark, "sidebar", 128); err != nil {
		t.Fatal(err)
	}
	if len(a.Themes) != 1 || a.Themes[0].Base != ThemeDark || a.Themes[0].Fonts["sidebar"] != 130 {
		t.Fatalf("内置覆盖项：%+v", a.Themes)
	}
	a.ResetThemeColor(ThemeDark, "accent")
	if a.Themes[0].Colors != nil {
		t.Fatal("空颜色 map 未置 nil")
	}
	a.ResetThemeFont(ThemeDark, "sidebar")
	if len(a.Themes) != 0 {
		t.Fatal("空内置主题未清除")
	}
	if err := a.SetThemeFont(ThemeDark, "code", DefaultCodeFontSize); err != nil || len(a.Themes) != 0 {
		t.Fatal("默认字号不应落盘")
	}
	if err := a.SetThemeFont(ThemeDark, "code", 18); err != nil {
		t.Fatal(err)
	}
	a.ResetTheme(ThemeDark)
	if len(a.Themes) != 0 {
		t.Fatal("重置未清除内置主题")
	}
	custom, _ := a.AddTheme(ThemeDark)
	if err := a.SetThemeColor(custom.ID, "text", "#123456"); err != nil {
		t.Fatal(err)
	}
	a.ResetTheme(custom.ID)
	if theme, ok := a.ThemeByID(custom.ID); !ok || theme.Colors != nil || theme.Fonts != nil {
		t.Fatal("重置应保留用户主题且清除覆盖项")
	}
	for _, err := range []error{
		a.SetThemeColor(ThemeDark, "text", "red"), a.SetThemeColor("missing", "text", "#123456"),
		a.SetThemeFont(ThemeDark, "missing", 1), a.SetThemeFont("missing", "code", 12),
	} {
		if err == nil {
			t.Fatal("非法设置应报错")
		}
	}
	a.ResetTheme("missing")
	a.ResetThemeColor("missing", "text")
	a.ResetThemeFont("missing", "code")
}

func TestThemeFontsAndActiveID(t *testing.T) {
	a := AppearanceConfig{Theme: ThemeSystem}
	want := map[string]int{"sidebar": 100, "conversation": 100, "interface": 100, "code": 12, "terminal": 13, "editor": 12}
	if got := a.ThemeFonts(ThemeLight); !reflect.DeepEqual(got, want) {
		t.Fatalf("默认字号 = %v", got)
	}
	if err := a.SetThemeFont(ThemeLight, "editor", 16); err != nil {
		t.Fatal(err)
	}
	fonts := a.ThemeFonts(ThemeLight)
	want["editor"] = 16
	if !reflect.DeepEqual(fonts, want) {
		t.Fatalf("字号 = %v", fonts)
	}
	fonts["editor"] = 17
	if a.ThemeFonts(ThemeLight)["editor"] != 16 {
		t.Fatal("字号结果应独立")
	}
	custom, _ := a.AddTheme(ThemeDark)
	for _, tt := range []struct{ selected, light, dark string }{
		{ThemeSystem, ThemeLight, ThemeDark}, {ThemeLight, ThemeLight, ThemeLight},
		{ThemeDark, ThemeDark, ThemeDark}, {custom.ID, custom.ID, custom.ID}, {"missing", ThemeLight, ThemeDark},
	} {
		a.Theme = tt.selected
		if a.ActiveThemeID(false) != tt.light || a.ActiveThemeID(true) != tt.dark {
			t.Errorf("ActiveThemeID(%q) 结果错误", tt.selected)
		}
	}
	if a.ThemeDisplayName(ThemeLight) != "浅色" || a.ThemeDisplayName(ThemeDark) != "深色" {
		t.Fatal("内置显示名错误")
	}
}

func TestNormalizeThemeFont(t *testing.T) {
	for _, tt := range []struct {
		key         string
		value, want int
	}{
		{"sidebar", 127, 130}, {"conversation", 110, 115}, {"interface", 140, 130},
		{"sidebar", -100, 85}, {"sidebar", 1000, 150}, {"code", 0, 10},
		{"code", 100, 24}, {"terminal", -10, 9}, {"terminal", 100, 28},
		{"editor", 0, 10}, {"editor", 100, 18},
	} {
		if got, ok := NormalizeThemeFont(tt.key, tt.value); !ok || got != tt.want {
			t.Errorf("NormalizeThemeFont(%q, %d) = %d, %v", tt.key, tt.value, got, ok)
		}
	}
	if _, ok := NormalizeThemeFont("unknown", 12); ok {
		t.Fatal("未知字号应拒绝")
	}
	if _, _, _, ok := ThemeFontRange("unknown"); ok {
		t.Fatal("未知字号不应有范围")
	}
	for _, key := range ThemeFontKeys {
		min, max, def, ok := ThemeFontRange(key)
		if !ok || def < min || def > max {
			t.Errorf("%s 范围错误", key)
		}
	}
}

func TestValidThemeColor(t *testing.T) {
	for _, key := range ThemeColorKeys {
		if !ValidThemeColor(key, "#Ab12Ef") || !ValidThemeColor(key, "#Ab12Ef80") {
			t.Errorf("%s 应接受十六进制颜色", key)
		}
	}
	for _, tt := range []struct{ key, value string }{
		{"unknown", "#123456"}, {"text", "system"}, {"text", "#123"},
		{"text", "#1234567"}, {"text", "#12345g"}, {"text", "123456"}, {"text", " #123456"},
	} {
		if ValidThemeColor(tt.key, tt.value) {
			t.Errorf("不应接受 %+v", tt)
		}
	}
	if !ValidThemeColor("accent", ThemeAccentSystem) {
		t.Fatal("accent 应接受 system")
	}
}

func TestThemeExportImport(t *testing.T) {
	a := AppearanceConfig{}
	theme, _ := a.AddTheme(ThemeDark)
	if err := a.SetThemeColor(theme.ID, "text", "#abcdef80"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetThemeFont(theme.ID, "code", 18); err != nil {
		t.Fatal(err)
	}
	theme, _ = a.ThemeByID(theme.ID)
	data, err := a.ExportTheme(theme.ID)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, exists := raw["id"]; exists || !strings.Contains(string(data), "\n  \"name\"") {
		t.Fatal("导出应缩进且不带 ID")
	}
	imported, err := a.ImportTheme(data)
	if err != nil {
		t.Fatal(err)
	}
	if imported.ID == theme.ID || imported.Name != theme.Name+" 2" || imported.Base != theme.Base ||
		!reflect.DeepEqual(imported.Colors, theme.Colors) || !reflect.DeepEqual(imported.Fonts, theme.Fonts) {
		t.Fatalf("往返结果不一致：%+v", imported)
	}
	data, err = a.ExportTheme(ThemeLight)
	if err != nil || !strings.Contains(string(data), `"name": "浅色"`) {
		t.Fatalf("内置导出错误：%s, %v", data, err)
	}
	if _, err := a.ExportTheme("missing"); err == nil {
		t.Fatal("不存在的主题导出应失败")
	}
	imported, err = a.ImportTheme([]byte(`{"base":"light","name":" ","colors":{"future":"invalid","accent":"system"},"fonts":{"future":1,"sidebar":127,"code":999,"editor":12}}`))
	if err != nil || imported.Name != "导入的主题" || !reflect.DeepEqual(imported.Colors, map[string]string{"accent": "system"}) ||
		!reflect.DeepEqual(imported.Fonts, map[string]int{"sidebar": 130, "code": 24}) {
		t.Fatalf("导入归一化错误：%+v, %v", imported, err)
	}
}

func TestThemeImportInvalid(t *testing.T) {
	var a AppearanceConfig
	for _, data := range []string{
		`{`, `null`, `[]`, `{}`, `{"base":"system"}`, `{"base":"light","colors":{"text":"red"}}`,
		`{"base":"dark","fonts":{"code":"big"}}`, `{"base":"dark"} {}`,
	} {
		if _, err := a.ImportTheme([]byte(data)); err == nil || len(a.Themes) != 0 {
			t.Errorf("非法导入 %s, err=%v, themes=%v", data, err, a.Themes)
		}
	}
}

func TestValidateThemes(t *testing.T) {
	cfg := Defaults()
	theme, _ := cfg.Appearance.AddTheme(ThemeDark)
	cfg.Appearance.Theme = theme.ID
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.Appearance.Theme = "missing" },
		func(c *Config) { c.Appearance.Themes = append(c.Appearance.Themes, c.Appearance.Themes[0]) },
		func(c *Config) { c.Appearance.Themes[0].ID = "" },
		func(c *Config) { c.Appearance.Themes[0].Base = ThemeSystem },
		func(c *Config) { c.Appearance.Themes[0].Name = " " },
		func(c *Config) { c.Appearance.Themes[0].Colors = map[string]string{"text": "red"} },
		func(c *Config) { c.Appearance.Themes[0].Colors = map[string]string{"future": "#123456"} },
		func(c *Config) { c.Appearance.Themes[0].Fonts = map[string]int{"future": 12} },
		func(c *Config) { c.Appearance.Themes[0].Fonts = map[string]int{"code": 1} },
		func(c *Config) { c.Appearance.Themes[0].Fonts = map[string]int{"sidebar": 120} },
	} {
		bad := cfg
		bad.Appearance.Themes = append([]ThemeDef(nil), cfg.Appearance.Themes...)
		change(&bad)
		if err := Validate(bad); err == nil {
			t.Error("非法主题未被拒绝")
		}
	}
}

func TestRepairThemes(t *testing.T) {
	cfg := Defaults()
	cfg.Appearance.Theme = "missing"
	cfg.Appearance.Themes = []ThemeDef{
		{ID: "", Base: ThemeDark, Name: "空 ID"},
		{ID: "custom", Base: "bad", Name: " ", Colors: map[string]string{"text": "red", "future": "#123456", "accent": "system"}, Fonts: map[string]int{"sidebar": 127, "code": 100, "editor": 12, "future": 1}},
		{ID: "custom", Base: ThemeLight, Name: "重复"},
		{ID: ThemeLight, Base: ThemeDark, Colors: map[string]string{"text": "red"}},
		{ID: ThemeLight, Base: ThemeLight, Colors: map[string]string{"text": "#123456"}},
		{ID: ThemeDark, Base: "bad", Fonts: map[string]int{"editor": 16}},
	}
	warnings := repairInvalidFields(&cfg)
	if len(warnings) < 10 || cfg.Appearance.Theme != ThemeSystem || len(cfg.Appearance.Themes) != 2 {
		t.Fatalf("修复结果：%+v, warnings=%v", cfg.Appearance, warnings)
	}
	custom, _ := cfg.Appearance.ThemeByID("custom")
	if custom.Base != ThemeDark || custom.Name != "未命名主题" || !reflect.DeepEqual(custom.Colors, map[string]string{"accent": "system"}) ||
		!reflect.DeepEqual(custom.Fonts, map[string]int{"sidebar": 130, "code": 24}) {
		t.Fatalf("用户主题未修复：%+v", custom)
	}
	if dark, _ := cfg.Appearance.ThemeByID(ThemeDark); dark.Base != ThemeDark {
		t.Fatal("内置 base 未修复")
	}
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Appearance.Theme = "custom"
	if warnings := repairInvalidFields(&cfg); len(warnings) != 0 || cfg.Appearance.Theme != "custom" {
		t.Fatalf("有效用户主题被修复：%v", warnings)
	}
}
