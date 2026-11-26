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
	// 用户消息气泡底色，未设置时沿用气泡当前底色（卡片色）。
	"canvas", "main", "card", "raised", "popover", "code_background", "table_header", "composer_background", "user_message_background", "text", "text_secondary", "text_tertiary", "code_text",
	"accent", "danger", "warning", "success", "caution", "plan", "border", "divider",
	// Markdown 渲染角色：标题、次要标记（列表圆点/序号/引用/代码块语言标签）、链接、粗体。
	"code_heading", "code_muted", "code_link", "code_strong",
	// 引用块专用色：引用文字与左侧竖线。
	"quote_text", "quote_bar",
}

const ThemeAccentSystem = "system"

// ThemeFontKeys 按界面顺序列出字号覆盖项。
var ThemeFontKeys = []string{"sidebar", "conversation", "interface", "code", "terminal", "editor"}

func IsThemeScaleFont(key string) bool {
	return key == "sidebar" || key == "conversation" || key == "interface"
}

func ThemeFontRange(key string) (min, max, def int, ok bool) {
	if IsThemeScaleFont(key) {
		return MinTextScale, MaxTextScale, DefaultTextScale, true
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
	return v, true
}

var themeColorPattern = regexp.MustCompile(`^#([0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)

func ValidThemeColor(key, value string) bool {
	return slices.Contains(ThemeColorKeys, key) &&
		((key == "accent" && value == ThemeAccentSystem) || themeColorPattern.MatchString(value))
}

type builtinTheme struct {
	Base   string
	Name   string
	Colors map[string]string
}

func mergeThemeColors(base, overrides map[string]string) map[string]string {
	colors := maps.Clone(base)
	for key, value := range overrides {
		colors[key] = value
	}
	return colors
}

var builtinThemes = func() map[string]builtinTheme {
	lightColors := map[string]string{
		"canvas": "#F5F5F3", "main": "#FAFAF9", "card": "#FFFFFF", "raised": "#FFFFFF", "popover": "#FFFFFF",
		"text": "#000000D9", "text_secondary": "#00000099", "text_tertiary": "#00000094",
		"accent": "#0062CC", "danger": "#C82333", "warning": "#A85500", "success": "#167936", "caution": "#8A6700", "plan": "#8A2BB8",
		"border": "#00000024", "divider": "#00000038",
	}
	darkColors := map[string]string{
		"canvas": "#141414", "main": "#1C1C1C", "card": "#1F1F1F", "raised": "#242424", "popover": "#2B2B2B",
		"text": "#FFFFFFD9", "text_secondary": "#FFFFFF99", "text_tertiary": "#FFFFFF61",
		"accent": "#0A84FF", "danger": "#FF453A", "warning": "#FF9F0A", "success": "#30D158", "caution": "#E8C15A", "plan": "#BF5AF2",
		"border": "#FFFFFF24", "divider": "#FFFFFF38",
	}
	colorfulLightOverrides := map[string]string{
		"accent": "#0062CC", "code_background": "#CAF8DC00", "code_heading": "#5B8FE6E6", "code_link": "#0062CC", "code_muted": "#EC4899", "code_strong": "#B27551", "code_text": "#D4922B", "composer_background": "#EAF2F1", "danger": "#C8253D", "quote_bar": "#8F6802", "success": "#337E58", "table_header": "#E4EEFF", "text": "#4D5666D8", "text_secondary": "#677176", "text_tertiary": "#879095", "user_message_background": "#DDF0EEDC", "warning": "#8F6802",
	}
	colorfulDarkOverrides := map[string]string{
		"accent": "#e9c1ff", "code_background": "#0a1b00", "code_heading": "#D3B147", "code_link": "#82aaff", "code_muted": "#e9c1ff", "code_strong": "#F4D69F", "code_text": "#e9c1ff", "danger": "#ff8288", "quote_bar": "#F4D69F", "quote_text": "#81b1be", "success": "#b4e88d", "table_header": "#2D4985", "text_secondary": "#81b1be", "text_tertiary": "#5b8c98", "user_message_background": "#043844", "warning": "#f4d69f",
	}
	return map[string]builtinTheme{
		ThemeLight:         {Base: ThemeLight, Name: "浅色", Colors: lightColors},
		ThemeDark:          {Base: ThemeDark, Name: "深色", Colors: darkColors},
		ThemeColorfulLight: {Base: ThemeLight, Name: "多彩浅色", Colors: mergeThemeColors(lightColors, colorfulLightOverrides)},
		ThemeColorfulDark:  {Base: ThemeDark, Name: "多彩深色", Colors: mergeThemeColors(darkColors, colorfulDarkOverrides)},
	}
}()

func IsBuiltinTheme(id string) bool {
	_, ok := builtinThemes[id]
	return ok
}

func (a AppearanceConfig) ThemeByID(id string) (ThemeDef, bool) {
	for _, theme := range a.Themes {
		if theme.ID == id {
			return theme, true
		}
	}
	if theme, ok := builtinThemes[id]; ok {
		return ThemeDef{ID: id, Base: theme.Base}, true
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
	if theme, ok := builtinThemes[id]; ok {
		return theme.Name
	}
	theme, _ := a.ThemeByID(id)
	return theme.Name
}

// BuiltinThemeColors 返回内置主题的默认颜色表副本。
func BuiltinThemeColors(id string) (map[string]string, bool) {
	theme, ok := builtinThemes[id]
	if !ok {
		return nil, false
	}
	return maps.Clone(theme.Colors), true
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

// ParseThemeTransfer 只解析 mcode 自有主题格式。pi 主题走 parsePiThemeTransfer。
func ParseThemeTransfer(data []byte) (ThemeDef, error) {
	return parseMcodeThemeTransfer(data)
}

func parseMcodeThemeTransfer(data []byte) (ThemeDef, error) {
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
	return ThemeDef{Name: imported.Name, Base: imported.Base, Colors: imported.Colors, Fonts: imported.Fonts}, nil
}

func (a *AppearanceConfig) ImportTheme(data []byte) (ThemeDef, error) {
	parsed, err := parseMcodeThemeTransfer(data)
	if err != nil {
		return ThemeDef{}, err
	}
	return a.addTransfer(themeTransfer{Name: parsed.Name, Base: parsed.Base, Colors: parsed.Colors, Fonts: parsed.Fonts}), nil
}

// AddThemeTransfer 把已解析的导入项加入主题列表。
func (a *AppearanceConfig) AddThemeTransfer(imported ThemeDef) ThemeDef {
	return a.addTransfer(themeTransfer{Name: imported.Name, Base: imported.Base, Colors: imported.Colors, Fonts: imported.Fonts})
}

// addTransfer 把已校验的导入项加入主题列表，所有导入入口共用这一条路径。
func (a *AppearanceConfig) addTransfer(imported themeTransfer) ThemeDef {
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
	return theme
}

func (a AppearanceConfig) validateThemes() error {
	seen := make(map[string]bool, len(a.Themes))
	for _, theme := range a.Themes {
		if theme.ID == "" || theme.ID == ThemeSystem || seen[theme.ID] {
			return fmt.Errorf("appearance.themes 含非法或重复的 ID %q", theme.ID)
		}
		seen[theme.ID] = true
		if !IsBuiltinTheme(theme.Base) || (IsBuiltinTheme(theme.ID) && theme.Base != builtinThemes[theme.ID].Base) {
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
			theme.Base = builtinThemes[theme.ID].Base
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
