package config

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// 本文件把 pi（@earendil-works/pi-coding-agent）主题 JSON 转成 mcode 主题。
// pi 的颜色值有四种形态：hex 字符串、okhsl(...)/oklch(...) 函数、vars 里的变量名（可链式），
// 以及空串（终端默认色，没有可映射的颜色）。

type piTheme struct {
	Name       string          `json:"name"`
	Appearance string          `json:"appearance"`
	Vars       map[string]any  `json:"vars"`
	Colors     json.RawMessage `json:"colors"`
	Export     map[string]any  `json:"export"`
}

// piColorRole 列出参与映射的 pi 颜色键及其对应的 mcode 颜色槽。
// 未列出的 pi 键（syntaxXxx、toolXxx、thinkingXxx、scrollbar 等）丢弃：
// mcode 的语法色从 accent/success/plan 等语义色派生，pi 的语法色只作为佐证。
// 'mdCodeBlock' 在 pi 中是代码块文字色（light 里取绿），mcode 的 'code_background' 是底色。
// 代码块文字使用该色时，底色取同一色相的低亮度版本（浅色主题 L=0.94、深色主题 L=0.20），
// 这样代码文字在底色上保持可读，又延续 pi 的色相。
var piColorRole = map[string]string{
	"text":          "text",
	"muted":         "text_secondary",
	"dim":           "text_tertiary",
	"accent":        "accent",
	"error":         "danger",
	"warning":       "warning",
	"success":       "success",
	"mdHeading":     "code_heading",
	"mdLink":        "code_link",
	"mdCode":        "code_text",
	"userMessageBg": "user_message_background",
	"mdQuote":       "quote_text",
	"mdQuoteBorder": "quote_bar",
}

func parsePiThemeTransfer(data []byte) (ThemeDef, error) {
	var raw piTheme
	if err := json.Unmarshal(data, &raw); err != nil {
		return ThemeDef{}, fmt.Errorf("解析 pi 主题失败：%w", err)
	}
	var colors map[string]any
	if len(raw.Colors) > 0 {
		if err := json.Unmarshal(raw.Colors, &colors); err != nil {
			return ThemeDef{}, fmt.Errorf("解析 pi 主题 colors 失败：%w", err)
		}
	}
	resolver := piResolver{vars: raw.Vars}
	// 先解析所有用到的 pi 颜色，空串视为无值。
	resolved := make(map[string]string, len(colors))
	for key := range colors {
		hex, ok, err := resolver.resolve(colors, key, nil)
		if err != nil {
			return ThemeDef{}, err
		}
		if ok {
			resolved[key] = hex
		}
	}
	base := piBase(raw.Appearance, resolved["text"], resolver, raw.Export)
	if base == "" {
		return ThemeDef{}, fmt.Errorf("pi 主题缺少 appearance 且无法从 colors.text 判断明暗")
	}
	out := map[string]string{}
	for piKey, mcodeKey := range piColorRole {
		if hex, ok := resolved[piKey]; ok {
			out[mcodeKey] = piLowerHex(hex)
		}
	}
	// 水平线、列表圆点都是 Markdown 的次要标记，落在同一个槽；
	// 冲突时列表圆点优先。引用文字和竖线有独立槽，见 piColorRole。
	for _, piKey := range []string{"mdHr", "mdListBullet"} {
		if hex, ok := resolved[piKey]; ok {
			out["code_muted"] = piLowerHex(hex)
		}
	}
	if hex, ok := resolved["mdCodeBlock"]; ok {
		if codeBg := piCodeBackground(hex, base); codeBg != "" {
			out["code_background"] = codeBg
		}
	}
	if len(out) == 0 {
		return ThemeDef{}, fmt.Errorf("pi 主题没有可映射的颜色")
	}
	return ThemeDef{Name: raw.Name, Base: base, Colors: out}, nil
}

// ImportPiTheme 导入 pi 主题；失败时不改动主题列表。
func (a *AppearanceConfig) ImportPiTheme(data []byte) (ThemeDef, error) {
	parsed, err := parsePiThemeTransfer(data)
	if err != nil {
		return ThemeDef{}, err
	}
	return a.addTransfer(themeTransfer{Name: parsed.Name, Base: parsed.Base, Colors: parsed.Colors}), nil
}

// piCodeBackground 把 pi 的代码块文字色换成同一色相的代码块底色：
// 浅色主题取 OKLCH L=0.94，深色主题取 L=0.20；无彩度或解析失败时返回空串（不落槽）。
func piCodeBackground(textHex, base string) string {
	lab := oklabFromHex(textHex)
	chroma := math.Hypot(lab[1], lab[2])
	if chroma < 1e-3 {
		return ""
	}
	hue := math.Atan2(lab[2], lab[1]) * 180 / math.Pi
	targetL := 0.94
	if base == ThemeDark {
		targetL = 0.20
	}
	c := chroma
	if c > 0.06 {
		c = 0.06
	}
	return piLowerHex(rgbHex(oklchToSRGB(targetL, c, hue)))
}

// piLowerHex 统一小写，避免同一颜色因大小写不同看起来“不同”。
func piLowerHex(hex string) string { return strings.ToLower(hex) }

// piBase 取 pi 的 appearance；缺失时按颜色亮度判断，亮度 < 0.6 视为深色。
func piBase(appearance, text string, resolver piResolver, export map[string]any) string {
	switch appearance {
	case ThemeLight, ThemeDark:
		return appearance
	}
	if text != "" {
		return piBaseFromHex(text, false)
	}
	for _, candidate := range []struct {
		value  any
		invert bool
	}{
		{resolver.vars["bg"], false},
		{export["pageBg"], false},
		{resolver.vars["fg"], true},
	} {
		hex, ok, _ := resolver.resolveValue(candidate.value, "base", nil)
		if ok {
			return piBaseFromHex(hex, candidate.invert)
		}
	}
	return ""
}

func piBaseFromHex(hex string, invert bool) string {
	lab := oklabFromHex(hex)
	if (lab[0] < 0.6) != invert {
		return ThemeDark
	}
	return ThemeLight
}

// piResolver 负责 pi 颜色值的变量展开与色彩转换。
type piResolver struct {
	vars map[string]any
}

// resolve 返回 key 对应的 hex 颜色；空串表示无值。seen 记录当前展开链以检测循环。
func (r piResolver) resolve(colors map[string]any, key string, seen []string) (string, bool, error) {
	value, ok := colors[key]
	if !ok {
		if v, ok := r.vars[key]; ok {
			return r.resolveValue(v, key, seen)
		}
		return "", false, nil
	}
	return r.resolveValue(value, key, seen)
}

func (r piResolver) resolveValue(value any, key string, seen []string) (string, bool, error) {
	s, ok := value.(string)
	if !ok {
		return "", false, fmt.Errorf("pi 主题颜色 %q 不是字符串", key)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false, nil
	}
	if strings.HasPrefix(s, "#") {
		return normalizeHex(s)
	}
	if strings.Contains(s, "(") {
		return parseColorFunction(s)
	}
	// 变量名：允许链式引用，递归展开并检测循环。
	for _, name := range seen {
		if name == s {
			return "", false, fmt.Errorf("pi 主题颜色 %q 存在循环引用：%s", key, strings.Join(append(seen, s), " → "))
		}
	}
	next, ok := r.vars[s]
	if !ok {
		return "", false, fmt.Errorf("pi 主题颜色 %q 引用了不存在的变量 %q", key, s)
	}
	return r.resolveValue(next, key, append(seen, s))
}

// normalizeHex 把 #rgb 补成 #rrggbb，#rrggbbaa 保留 alpha。
func normalizeHex(s string) (string, bool, error) {
	h := s[1:]
	for _, c := range h {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return "", false, fmt.Errorf("pi 主题颜色 %q 非法", s)
		}
	}
	switch len(h) {
	case 3:
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	case 6, 8:
	default:
		return "", false, fmt.Errorf("pi 主题颜色 %q 非法", s)
	}
	return "#" + h, true, nil
}

// parseColorFunction 解析 okhsl()/oklch()/#hex。函数参数用空格分隔，可选带 / alpha。
func parseColorFunction(s string) (string, bool, error) {
	upper := strings.ToLower(s)
	var kind string
	switch {
	case strings.HasPrefix(upper, "okhsl("):
		kind = "okhsl"
	case strings.HasPrefix(upper, "oklch("):
		kind = "oklch"
	default:
		return "", false, fmt.Errorf("pi 主题颜色 %q 使用了不支持的颜色函数", s)
	}
	body := strings.TrimSuffix(s[len(kind)+1:], ")")
	body = strings.ReplaceAll(body, ",", " ")
	parts := strings.Fields(body)
	if len(parts) != 3 {
		return "", false, fmt.Errorf("pi 主题颜色 %q 参数不足", s)
	}
	var vals [3]float64
	for i, part := range parts {
		v, err := parseColorNumber(part)
		if err != nil {
			return "", false, fmt.Errorf("pi 主题颜色 %q 非法：%v", s, err)
		}
		vals[i] = v
	}
	var rgb [3]float64
	if kind == "okhsl" {
		rgb = okhslToSRGB(vals[0], vals[1]/100, vals[2]/100)
	} else {
		rgb = oklchToSRGB(vals[0]/100, vals[1]/100, vals[2])
	}
	return rgbHex(rgb), true, nil
}

func parseColorNumber(part string) (float64, error) {
	switch {
	case strings.HasSuffix(part, "%"):
		v, err := strconv.ParseFloat(strings.TrimSuffix(part, "%"), 64)
		if err != nil {
			return 0, err
		}
		return v, nil
	case strings.HasSuffix(part, "deg"):
		return strconv.ParseFloat(strings.TrimSuffix(part, "deg"), 64)
	default:
		return strconv.ParseFloat(part, 64)
	}
}

// rgbHex 把 0..1 的 sRGB 分量转成 #rrggbb，越界时 clamp。
func rgbHex(rgb [3]float64) string {
	var out [3]int
	for i, v := range rgb {
		v = math.Max(0, math.Min(1, v))
		out[i] = int(math.Round(v * 255))
	}
	return fmt.Sprintf("#%02x%02x%02x", out[0], out[1], out[2])
}

// oklabFromHex 把 #rrggbb 转成 OKLab，用于判断明暗。
func oklabFromHex(hex string) [3]float64 {
	var v [3]float64
	for i := 0; i < 3; i++ {
		n, _ := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		v[i] = srgbToLinear(float64(n) / 255)
	}
	return oklabFromLinearRGB(v[0], v[1], v[2])
}
