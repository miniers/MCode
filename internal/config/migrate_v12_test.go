package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMigrateV11ToV12Fonts(t *testing.T) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(`{"appearance":{"text_scale":130,"code_font_size":14},"workbench":{"terminal_font_size":13,"editor_font_size":16}}`), &raw); err != nil {
		t.Fatal(err)
	}
	if _, err := migrateV11ToV12(raw); err != nil {
		t.Fatal(err)
	}
	appearance := raw["appearance"].(map[string]any)
	themes := appearance["themes"].([]any)
	want := map[string]int{"sidebar": 130, "conversation": 130, "code": 14, "editor": 16}
	for i, id := range []string{ThemeLight, ThemeDark} {
		theme := themes[i].(map[string]any)
		if theme["id"] != id || theme["base"] != id || !reflect.DeepEqual(theme["fonts"], want) {
			t.Fatalf("迁移结果：%v", theme)
		}
	}
	themes[0].(map[string]any)["fonts"].(map[string]int)["code"] = 20
	if themes[1].(map[string]any)["fonts"].(map[string]int)["code"] != 14 {
		t.Fatal("两个主题的字号 map 不独立")
	}
	if appearance["text_scale"] != float64(130) || appearance["code_font_size"] != float64(14) || raw["workbench"].(map[string]any)["editor_font_size"] != float64(16) {
		t.Fatal("旧键被修改")
	}
}

func TestMigrateV11ToV12SkipsDefaultsAndExisting(t *testing.T) {
	for _, input := range []string{
		`{}`, `{"appearance":"invalid"}`, `{"appearance":{}}`,
		`{"appearance":{"text_scale":100,"code_font_size":12},"workbench":{"terminal_font_size":13,"editor_font_size":12}}`,
		`{"appearance":{"text_scale":0,"code_font_size":"big"},"workbench":{"editor_font_size":-1}}`,
		`{"appearance":{"text_scale":130,"themes":[]}}`,
		`{"appearance":{"text_scale":130,"themes":[{"id":"custom","base":"dark","name":"自定义"}]}}`,
	} {
		var raw, before map[string]any
		if err := json.Unmarshal([]byte(input), &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(input), &before); err != nil {
			t.Fatal(err)
		}
		out, err := migrateV11ToV12(raw)
		if err != nil || !reflect.DeepEqual(out, before) {
			t.Errorf("应保持原样：%s, got=%v, err=%v", input, out, err)
		}
	}
}

func TestLoadMigratesV11ThemeFonts(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"version":11,"appearance":{"text_scale":130,"code_font_size":14},"workbench":{"terminal_font_size":13,"editor_font_size":16}}`)
	if err := os.WriteFile(ConfigPath(dir), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != Version || len(cfg.Appearance.Themes) != 2 {
		t.Fatalf("迁移结果：%+v", cfg.Appearance)
	}
	want := map[string]int{"sidebar": 130, "conversation": 130, "code": 14, "editor": 16}
	for _, id := range []string{ThemeLight, ThemeDark} {
		theme, ok := cfg.Appearance.ThemeByID(id)
		if !ok || !reflect.DeepEqual(theme.Fonts, want) {
			t.Fatalf("字号迁移错误：%v", theme.Fonts)
		}
	}
	backup, err := os.ReadFile(filepath.Join(dir, "config.json.bak"))
	if err != nil || string(backup) != string(data) {
		t.Fatalf("迁移备份不一致：%s, %v", backup, err)
	}
	if err := Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil || !reflect.DeepEqual(loaded.Appearance.Themes, cfg.Appearance.Themes) {
		t.Fatalf("保存加载不一致：%v", err)
	}
}

func TestLoadV12WithLegacyFontKeys(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"version":12,"appearance":{"text_scale":130,"code_font_size":14},"workbench":{"terminal_font_size":15,"editor_font_size":16}}`)
	if err := os.WriteFile(ConfigPath(dir), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, warnings, err := LoadWithWarnings(dir)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("旧键配置加载失败：warnings=%v, err=%v", warnings, err)
	}
	if cfg.Version != Version || len(cfg.Appearance.Themes) != 0 {
		t.Fatalf("v12 旧键不应再次迁移：%+v", cfg.Appearance)
	}
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
}
