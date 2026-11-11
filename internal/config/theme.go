package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// ThemeDef 仅保存主题覆盖项，颜色默认值由界面层提供。
type ThemeDef struct {
	ID     string            `json:"id"`
	Name   string            `json:"name,omitempty"`
	Base   string            `json:"base"`
	Colors map[string]string `json:"colors,omitempty"`
	Fonts  map[string]int    `json:"fonts,omitempty"`
}

// ThemeColorKeys 按界面顺序列出颜色覆盖项。
var ThemeColorKeys = []string{
	"canvas", "main", "card", "raised", "popover", "text", "text_secondary", "text_tertiary",
	"accent", "danger", "warning", "success", "caution", "plan", "border", "divider",
}

const ThemeAccentSystem = "system"

// ThemeFontKeys 按界面顺序列出字号覆盖项。
var ThemeFontKeys = []string{"sidebar", "conversation", "interface", "code", "terminal", "editor"}

func IsThemeScaleFont(key string) bool {
	return key == "sidebar" || key == "conversation" || key == "interface"
}

func ThemeFontRange(key string) (min, max, def int, ok bool) {
	if IsThemeScaleFont(key) {
		return TextScaleLevels[0], TextScaleLevels[len(TextScaleLevels)-1], DefaultTextScale, true
	}
	switch key {
	case "code":
		return MinCodeFontSize, MaxCodeFontSize, DefaultCodeFontSize, true
	case "terminal":
		return MinTerminalFontSize, MaxTerminalFontSize, DefaultTerminalFontSize, true
	case "editor":
		return MinEditorFontSize, MaxEditorFontSize, DefaultEditorFontSize, true
	default:
		return 0, 0, 0, false
	}
}

func NormalizeThemeFont(key string, v int) (int, bool) {
	lower, upper, _, ok := ThemeFontRange(key)
	if !ok {
		return 0, false
	}
	v = min(max(v, lower), upper)
	if IsThemeScaleFont(key) {
		nearest := TextScaleLevels[0]
		for _, level := range TextScaleLevels {
			if absThemeDistance(v, level) < absThemeDistance(v, nearest) {
				nearest = level
			}
		}
		v = nearest
	}
	return v, true
}

func absThemeDistance(a, b int) int {
	if a < b {
		return b - a
	}
	return a - b
}

var themeColorPattern = regexp.MustCompile(`^#([0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)

func ValidThemeColor(key, value string) bool {
	return slices.Contains(ThemeColorKeys, key) &&
		((key == "accent" && value == ThemeAccentSystem) || themeColorPattern.MatchString(value))
}

func IsBuiltinTheme(id string) bool { return id == ThemeLight || id == ThemeDark }

func (a AppearanceConfig) ThemeByID(id string) (ThemeDef, bool) {
	for _, theme := range a.Themes {
		if theme.ID == id {
			return theme, true
		}
	}
	if IsBuiltinTheme(id) {
		return ThemeDef{ID: id, Base: id}, true
	}
	return ThemeDef{}, false
}

func (a AppearanceConfig) ActiveThemeID(systemDark bool) string {
	if a.Theme != ThemeSystem {
		if _, ok := a.ThemeByID(a.Theme); ok {
			return a.Theme
		}
	}
	if systemDark {
		return ThemeDark
	}
	return ThemeLight
}

func (a AppearanceConfig) ThemeFonts(id string) map[string]int {
	theme, _ := a.ThemeByID(id)
	fonts := make(map[string]int, len(ThemeFontKeys))
	for _, key := range ThemeFontKeys {
		_, _, fonts[key], _ = ThemeFontRange(key)
		if value, ok := theme.Fonts[key]; ok {
			fonts[key] = value
		}
	}
	return fonts
}

func (a AppearanceConfig) ThemeDisplayName(id string) string {
	switch id {
	case ThemeLight:
		return "浅色"
	case ThemeDark:
		return "深色"
	default:
		theme, _ := a.ThemeByID(id)
		return theme.Name
	}
}

func (a AppearanceConfig) uniqueThemeName(name string) string {
	used := make(map[string]bool, len(a.Themes))
	for _, theme := range a.Themes {
		if !IsBuiltinTheme(theme.ID) {
			used[theme.Name] = true
		}
	}
	candidate := name
	for n := 2; used[candidate]; n++ {
		candidate = fmt.Sprintf("%s %d", name, n)
	}
	return candidate
}

func (a *AppearanceConfig) AddTheme(sourceID string) (ThemeDef, bool) {
	source, ok := a.ThemeByID(sourceID)
	if !ok {
		return ThemeDef{}, false
	}
	theme := ThemeDef{
		ID: uuid.NewString(), Name: a.uniqueThemeName(a.ThemeDisplayName(sourceID) + " 副本"),
		Base: source.Base, Colors: maps.Clone(source.Colors), Fonts: maps.Clone(source.Fonts),
	}
	a.Themes = append(a.Themes, theme)
	return theme, true
}

func (a *AppearanceConfig) RenameTheme(id, name string) bool {
	name = strings.TrimSpace(name)
	if IsBuiltinTheme(id) || name == "" {
		return false
	}
	for i := range a.Themes {
		if a.Themes[i].ID == id {
			a.Themes[i].Name = name
			return true
		}
	}
	return false
}

func (a *AppearanceConfig) RemoveTheme(id string) bool {
	if IsBuiltinTheme(id) {
		return false
	}
	for i := range a.Themes {
		if a.Themes[i].ID == id {
			a.Themes = slices.Delete(a.Themes, i, i+1)
			if a.Theme == id {
				a.Theme = ThemeSystem
			}
			return true
		}
	}
	return false
}

// storeTheme 清理空覆盖项，内置主题没有覆盖时不落盘。
func (a *AppearanceConfig) storeTheme(theme ThemeDef) {
	if len(theme.Colors) == 0 {
		theme.Colors = nil
	}
	if len(theme.Fonts) == 0 {
		theme.Fonts = nil
	}
	for i := range a.Themes {
		if a.Themes[i].ID == theme.ID {
			if IsBuiltinTheme(theme.ID) && theme.Colors == nil && theme.Fonts == nil {
				a.Themes = slices.Delete(a.Themes, i, i+1)
			} else {
				a.Themes[i] = theme
			}
			return
		}
	}
	if !IsBuiltinTheme(theme.ID) || theme.Colors != nil || theme.Fonts != nil {
		a.Themes = append(a.Themes, theme)
	}
}

func (a *AppearanceConfig) SetThemeColor(id, key, value string) error {
	if !ValidThemeColor(key, value) {
		return fmt.Errorf("主题颜色 %q 的值 %q 非法", key, value)
	}
	theme, ok := a.ThemeByID(id)
	if !ok {
		return fmt.Errorf("主题 %q 不存在", id)
	}
	if theme.Colors == nil {
		theme.Colors = make(map[string]string)
	}
	theme.Colors[key] = value
	a.storeTheme(theme)
	return nil
}

func (a *AppearanceConfig) ResetThemeColor(id, key string) {
	if theme, ok := a.ThemeByID(id); ok {
		delete(theme.Colors, key)
		a.storeTheme(theme)
	}
}

func (a *AppearanceConfig) SetThemeFont(id, key string, v int) error {
	v, ok := NormalizeThemeFont(key, v)
	if !ok {
		return fmt.Errorf("主题字号 %q 非法", key)
	}
	theme, ok := a.ThemeByID(id)
	if !ok {
		return fmt.Errorf("主题 %q 不存在", id)
	}
	_, _, def, _ := ThemeFontRange(key)
	if v == def {
		delete(theme.Fonts, key)
	} else {
		if theme.Fonts == nil {
			theme.Fonts = make(map[string]int)
		}
		theme.Fonts[key] = v
	}
	a.storeTheme(theme)
	return nil
}

func (a *AppearanceConfig) ResetThemeFont(id, key string) {
	if theme, ok := a.ThemeByID(id); ok {
		delete(theme.Fonts, key)
		a.storeTheme(theme)
	}
}

func (a *AppearanceConfig) ResetTheme(id string) {
	if theme, ok := a.ThemeByID(id); ok {
		theme.Colors, theme.Fonts = nil, nil
		a.storeTheme(theme)
	}
}

type themeTransfer struct {
	Name   string            `json:"name"`
	Base   string            `json:"base"`
	Colors map[string]string `json:"colors,omitempty"`
	Fonts  map[string]int    `json:"fonts,omitempty"`
}

func (a AppearanceConfig) ExportTheme(id string) ([]byte, error) {
	theme, ok := a.ThemeByID(id)
	if !ok {
		return nil, fmt.Errorf("主题 %q 不存在", id)
	}
	return json.MarshalIndent(themeTransfer{a.ThemeDisplayName(id), theme.Base, theme.Colors, theme.Fonts}, "", "  ")
}

func (a *AppearanceConfig) ImportTheme(data []byte) (ThemeDef, error) {
	var imported themeTransfer
	if err := json.Unmarshal(data, &imported); err != nil {
		return ThemeDef{}, fmt.Errorf("解析主题失败：%w", err)
	}
	if !IsBuiltinTheme(imported.Base) {
		return ThemeDef{}, fmt.Errorf("主题 base %q 非法", imported.Base)
	}
	for key, value := range imported.Colors {
		if !slices.Contains(ThemeColorKeys, key) {
			delete(imported.Colors, key)
		} else if !ValidThemeColor(key, value) {
			return ThemeDef{}, fmt.Errorf("主题颜色 %q 的值 %q 非法", key, value)
		}
	}
	for key, value := range imported.Fonts {
		value, ok := NormalizeThemeFont(key, value)
		_, _, def, _ := ThemeFontRange(key)
		if !ok || value == def {
			delete(imported.Fonts, key)
		} else {
			imported.Fonts[key] = value
		}
	}
	name := strings.TrimSpace(imported.Name)
	if name == "" {
		name = "导入的主题"
	}
	theme := ThemeDef{uuid.NewString(), a.uniqueThemeName(name), imported.Base, imported.Colors, imported.Fonts}
	if len(theme.Colors) == 0 {
		theme.Colors = nil
	}
	if len(theme.Fonts) == 0 {
		theme.Fonts = nil
	}
	a.Themes = append(a.Themes, theme)
	return theme, nil
}

func (a AppearanceConfig) validateThemes() error {
	seen := make(map[string]bool, len(a.Themes))
	for _, theme := range a.Themes {
		if theme.ID == "" || theme.ID == ThemeSystem || seen[theme.ID] {
			return fmt.Errorf("appearance.themes 含非法或重复的 ID %q", theme.ID)
		}
		seen[theme.ID] = true
		if !IsBuiltinTheme(theme.Base) || (IsBuiltinTheme(theme.ID) && theme.Base != theme.ID) {
			return fmt.Errorf("appearance.themes[%q].base %q 非法", theme.ID, theme.Base)
		}
		if !IsBuiltinTheme(theme.ID) && strings.TrimSpace(theme.Name) == "" {
			return fmt.Errorf("appearance.themes[%q].name 不能为空", theme.ID)
		}
		for key, value := range theme.Colors {
			if !ValidThemeColor(key, value) {
				return fmt.Errorf("appearance.themes[%q].colors[%q] %q 非法", theme.ID, key, value)
			}
		}
		for key, value := range theme.Fonts {
			if normalized, ok := NormalizeThemeFont(key, value); !ok || normalized != value {
				return fmt.Errorf("appearance.themes[%q].fonts[%q] %d 非法", theme.ID, key, value)
			}
		}
	}
	if a.Theme != ThemeSystem {
		if _, ok := a.ThemeByID(a.Theme); !ok {
			return fmt.Errorf("invalid appearance.theme %q: 主题不存在", a.Theme)
		}
	}
	return nil
}

func (a *AppearanceConfig) repairThemes() []string {
	var warnings []string
	seen := make(map[string]bool, len(a.Themes))
	var themes []ThemeDef
	for _, theme := range a.Themes {
		if theme.ID == "" || theme.ID == ThemeSystem || seen[theme.ID] {
			warnings = append(warnings, fmt.Sprintf("appearance.themes 的 ID %q 为空、保留或重复，已丢弃", theme.ID))
			continue
		}
		seen[theme.ID] = true
		base := theme.Base
		if IsBuiltinTheme(theme.ID) {
			theme.Base = theme.ID
		} else if !IsBuiltinTheme(theme.Base) {
			theme.Base = ThemeDark
		}
		if base != theme.Base {
			warnings = append(warnings, fmt.Sprintf("appearance.themes[%q].base %q 非法，已重置为 %q", theme.ID, base, theme.Base))
		}
		if !IsBuiltinTheme(theme.ID) && strings.TrimSpace(theme.Name) == "" {
			theme.Name = "未命名主题"
			warnings = append(warnings, fmt.Sprintf("appearance.themes[%q].name 为空，已设为未命名主题", theme.ID))
		}
		for key, value := range theme.Colors {
			if !ValidThemeColor(key, value) {
				delete(theme.Colors, key)
				warnings = append(warnings, fmt.Sprintf("appearance.themes[%q].colors[%q] %q 非法，已丢弃", theme.ID, key, value))
			}
		}
		for key, value := range theme.Fonts {
			normalized, ok := NormalizeThemeFont(key, value)
			if !ok {
				delete(theme.Fonts, key)
				warnings = append(warnings, fmt.Sprintf("appearance.themes[%q].fonts[%q] 未知，已丢弃", theme.ID, key))
				continue
			}
			if normalized != value {
				warnings = append(warnings, fmt.Sprintf("appearance.themes[%q].fonts[%q] %d 非法，已修复为 %d", theme.ID, key, value, normalized))
			}
			_, _, def, _ := ThemeFontRange(key)
			if normalized == def {
				delete(theme.Fonts, key)
			} else {
				theme.Fonts[key] = normalized
			}
		}
		if len(theme.Colors) == 0 {
			theme.Colors = nil
		}
		if len(theme.Fonts) == 0 {
			theme.Fonts = nil
		}
		if !IsBuiltinTheme(theme.ID) || theme.Colors != nil || theme.Fonts != nil {
			themes = append(themes, theme)
		}
	}
	a.Themes = themes
	return warnings
}
