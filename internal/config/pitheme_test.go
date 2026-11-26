package config

import (
	"math"
	"strings"
	"testing"
)

// piLightThemeJSON 取自 @earendil-works/pi-coding-agent 的 light 主题，原样内联。
const piLightThemeJSON = `{
	"$schema": "https://pi.dev/schemas/theme.schema.json",
	"name": "light",
	"appearance": "light",
	"vars": {
		"text": "okhsl(225 5% 27%)",
		"muted": "okhsl(229 8% 47%)",
		"violet": "okhsl(295 60% 46%)",
		"blue": "okhsl(231 68% 47%)",
		"green": "okhsl(159 75% 46%)",
		"red": "okhsl(20 91% 47%)",
		"yellow": "okhsl(83 99% 47%)",
		"blueBg": "okhsl(235 19% 91%)"
	},
	"colors": {
		"accent": "violet",
		"borderAccent": "okhsl(295 59% 55%)",
		"borderMuted": "okhsl(235 7% 66%)",
		"success": "green",
		"error": "red",
		"warning": "yellow",
		"muted": "muted",
		"dim": "okhsl(229 7% 59%)",
		"text": "text",
		"selectedBg": "blueBg",
		"userMessageBg": "blueBg",
		"userMessageText": "text",
		"toolTitle": "text",
		"mdHeading": "yellow",
		"mdLink": "blue",
		"mdLinkUrl": "muted",
		"mdCode": "violet",
		"mdCodeBlock": "green",
		"mdQuote": "muted",
		"mdQuoteBorder": "okhsl(235 7% 66%)",
		"mdHr": "muted",
		"mdListBullet": "violet",
		"syntaxComment": "muted",
		"syntaxKeyword": "blue",
		"syntaxString": "okhsl(52 84% 46%)",
		"syntaxType": "violet"
	},
	"export": {
		"pageBg": "okhsl(17 3% 94%)",
		"cardBg": "okhsl(17 5% 97%)"
	}
}`

// srgb8 把 0..1 的 sRGB 分量量化成 0..255，和转换输出对拍。
func srgb8(rgb [3]float64) [3]int {
	var out [3]int
	for i, v := range rgb {
		out[i] = int(math.Round(clamp01(v) * 255))
	}
	return out
}

func TestImportPiThemeLight(t *testing.T) {
	var a AppearanceConfig

	theme, err := a.ImportPiTheme([]byte(piLightThemeJSON))
	if err != nil {
		t.Fatalf("导入 pi 主题失败：%v", err)
	}
	if theme.Base != ThemeLight {
		t.Fatalf("Base = %q，期望 light", theme.Base)
	}
	if theme.Name != "light" {
		t.Fatalf("Name = %q，期望 light", theme.Name)
	}
	if len(a.Themes) != 1 {
		t.Fatalf("Themes 长度 = %d", len(a.Themes))
	}
	seen := map[string]string{}
	for _, key := range []string{"text", "code_heading", "code_link", "code_muted", "user_message_background", "quote_text", "quote_bar"} {
		value, ok := theme.Colors[key]
		if !ok {
			t.Fatalf("缺少颜色 %q：%v", key, theme.Colors)
		}
		if !ValidThemeColor(key, value) {
			t.Fatalf("%s = %q 不是合法主题颜色", key, value)
		}
		if prev, dup := seen[value]; dup {
			t.Fatalf("%s 与 %s 映射到同一颜色 %q", key, prev, value)
		}
		seen[value] = key
	}
	// code_muted 取 mdHr、mdListBullet，列表圆点优先（violet）。引用文字单独落 quote_text。
	if got := theme.Colors["code_muted"]; got != "#7459b4" {
		t.Fatalf("code_muted = %q，期望列表圆点 violet", got)
	}
	if got := theme.Colors["quote_text"]; got != "#677176" {
		t.Fatalf("quote_text = %q，期望 muted", got)
	}
	if got := theme.Colors["quote_bar"]; got != "#9aa2a7" {
		t.Fatalf("quote_bar = %q，期望 mdQuoteBorder", got)
	}
	if theme.Colors["accent"] != "#7459b4" || theme.Colors["code_text"] != "#7459b4" {
		t.Fatalf("accent/code_text = %q/%q", theme.Colors["accent"], theme.Colors["code_text"])
	}
	// pi 的 mdCodeBlock 是代码文字色（green），落到 code_background 时换成同色相的浅底。
	if bg := theme.Colors["code_background"]; bg != "#caf8dc" {
		t.Fatalf("code_background = %q，期望 green 色相的浅底", bg)
	}
	// 未被映射的 pi 键不会泄漏成主题键。
	for key := range theme.Colors {
		if !ValidThemeColor(key, theme.Colors[key]) && key != "accent" {
			t.Fatalf("映射出非法键 %q", key)
		}
		if strings.HasPrefix(key, "syntax") || strings.HasPrefix(key, "tool") || strings.HasPrefix(key, "md") {
			t.Fatalf("pi 专有键 %q 不应落槽", key)
		}
	}
}

func TestImportPiThemeGhostty(t *testing.T) {
	var a AppearanceConfig
	body := `{
		"name": "ghostty-sync",
		"vars": {
			"bg": "#043844",
			"fg": "#c5f2ff",
			"accent": "#e9c1ff"
		},
		"colors": {
			"text": "",
			"accent": "accent",
			"toolTitle": "",
			"customMessageText": ""
		},
		"export": {
			"pageBg": "#00303c"
		}
	}`
	theme, err := a.ImportPiTheme([]byte(body))
	if err != nil {
		t.Fatalf("导入 ghostty 主题失败：%v", err)
	}
	if theme.Base != ThemeDark {
		t.Fatalf("Base = %q，期望 dark", theme.Base)
	}
	if value := theme.Colors["text"]; value != "" {
		t.Fatalf("text = %q，空串不应落槽", value)
	}
	if theme.Colors["accent"] != "#e9c1ff" {
		t.Fatalf("accent = %q，期望 #e9c1ff", theme.Colors["accent"])
	}
}

func TestPiThemeBaseFromLightVarsBG(t *testing.T) {
	var a AppearanceConfig
	body := `{"name":"light-bg","vars":{"bg":"#f0f0f0","accent":"#3366cc"},"colors":{"text":"","accent":"accent"}}`
	theme, err := a.ImportPiTheme([]byte(body))
	if err != nil {
		t.Fatalf("按 vars.bg 判断明暗失败：%v", err)
	}
	if theme.Base != ThemeLight {
		t.Fatalf("Base = %q，期望 light", theme.Base)
	}
}

func TestPiThemeInvalid(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"循环引用", `{"name":"x","appearance":"light","vars":{"a":"b","b":"a"},"colors":{"text":"a"}}`, "循环引用"},
		{"自引用", `{"name":"x","appearance":"light","vars":{"a":"a"},"colors":{"text":"a"}}`, "循环引用"},
		{"缺失变量", `{"name":"x","appearance":"light","vars":{},"colors":{"text":"missing"}}`, "不存在"},
		{"非法函数", `{"name":"x","appearance":"light","colors":{"text":"rgb(1 2 3)"}}`, "不支持的颜色函数"},
		{"非法十六进制", `{"name":"x","appearance":"light","colors":{"text":"#12345"}}`, "非法"},
		{"参数不足", `{"name":"x","appearance":"light","colors":{"text":"okhsl(1 2%)"}}`, "参数"},
		{"无非空值", `{"name":"x","appearance":"light","colors":{"text":""}}`, "没有可映射的颜色"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var a AppearanceConfig
			a.Themes = []ThemeDef{{ID: "keep", Name: "已有", Base: ThemeDark}}
			before := len(a.Themes)
			_, err := a.ImportPiTheme([]byte(tc.body))
			if err == nil {
				t.Fatal("期望报错")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误 %q 未包含 %q", err, tc.want)
			}
			if len(a.Themes) != before {
				t.Fatalf("失败后 Themes 长度 = %d，期望 %d", len(a.Themes), before)
			}
		})
	}
}

func TestPiThemeEmptyColorDropped(t *testing.T) {
	var a AppearanceConfig
	body := `{"name":"x","appearance":"dark","colors":{"text":"#102030","muted":"","dim":"   ","mdHeading":"#aabbcc"}}`
	theme, err := a.ImportPiTheme([]byte(body))
	if err != nil {
		t.Fatalf("空串颜色不应报错：%v", err)
	}
	if _, ok := theme.Colors["text_secondary"]; ok {
		t.Fatal("空串 dim/muted 不应落槽")
	}
	if _, ok := theme.Colors["text_tertiary"]; ok {
		t.Fatal("空串 dim 不应落槽")
	}
	if theme.Colors["text"] != "#102030" || theme.Colors["code_heading"] != "#aabbcc" {
		t.Fatalf("颜色映射错误：%v", theme.Colors)
	}
}

func TestPiThemeBaseFallback(t *testing.T) {
	var a AppearanceConfig
	theme, err := a.ImportPiTheme([]byte(`{"name":"dark","colors":{"text":"#101418","accent":"#abc"}}`))
	if err != nil {
		t.Fatalf("缺失 appearance 时按亮度判断：%v", err)
	}
	if theme.Base != ThemeDark {
		t.Fatalf("Base = %q，期望 dark", theme.Base)
	}
	if theme.Colors["accent"] != "#aabbcc" {
		t.Fatalf("#abc 未补全：%q", theme.Colors["accent"])
	}
	theme, err = a.ImportPiTheme([]byte(`{"name":"mid","colors":{"text":"#f0f0f0"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if theme.Base != ThemeLight {
		t.Fatalf("Base = %q，期望 light", theme.Base)
	}
	if _, err := a.ImportPiTheme([]byte(`{"name":"none","colors":{"text":""}}`)); err == nil {
		t.Fatal("既无 appearance 也无可判断的 text 时应报错")
	}
}

func TestPiThemeVarsChain(t *testing.T) {
	var a AppearanceConfig
	body := `{"name":"x","appearance":"light","vars":{"base":"#112233","alias":"base"},"colors":{"text":"alias","accent":"#44556677"}}`
	theme, err := a.ImportPiTheme([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if theme.Colors["text"] != "#112233" {
		t.Fatalf("链式变量展开失败：%q", theme.Colors["text"])
	}
	// 8 位 hex 带 alpha 时保留 alpha。
	if theme.Colors["accent"] != "#44556677" {
		t.Fatalf("带 alpha 的颜色被改写：%q", theme.Colors["accent"])
	}
}

func TestPiThemeRejectsNonStringColor(t *testing.T) {
	var a AppearanceConfig
	if _, err := a.ImportPiTheme([]byte(`{"name":"x","appearance":"light","colors":{"text":123}}`)); err == nil {
		t.Fatal("非字符串颜色应报错")
	}
}

func TestPiThemeColorSpace(t *testing.T) {
	near := func(t *testing.T, got [3]int, want [3]int) {
		t.Helper()
		for i := 0; i < 3; i++ {
			if d := got[i] - want[i]; d < -2 || d > 2 {
				t.Fatalf("分量 %d 偏差 %d：得到 %v，期望 %v", i, d, got, want)
			}
		}
	}
	// 中性灰：s=0 时三通道相等，亮度单调。
	mid := okhslToSRGB(0, 0, 0.5)
	g := srgb8(mid)
	if g[0] != g[1] || g[1] != g[2] {
		t.Fatalf("okhsl(0 0%% 50%%) 应为中性灰：%v", mid)
	}
	if mid[0] <= okhslToSRGB(0, 0, 0.2)[0] || mid[0] >= okhslToSRGB(0, 0, 0.8)[0] {
		t.Fatal("无彩度时亮度应随 l 单调")
	}
	if nearMid := srgb8(okhslToSRGB(0, 0, 0.5)); nearMid != g {
		t.Fatalf("同一输入结果不稳定：%v vs %v", nearMid, g)
	}
	// 满饱和基色：色相 0/120/240 分别取红/绿/蓝。
	red := srgb8(okhslToSRGB(0, 1, 0.5))
	if red != [3]int{0xd7, 0x00, 0x71} {
		t.Fatalf("okhsl(0 100%% 50%%) = %v，期望 [215 0 113]", red)
	}
	green := srgb8(okhslToSRGB(120, 1, 0.5))
	if green != [3]int{0x6d, 0x82, 0x00} {
		t.Fatalf("okhsl(120 100%% 50%%) = %v，期望 [109 130 0]", green)
	}
	blue := srgb8(okhslToSRGB(240, 1, 0.5))
	if blue != [3]int{0x00, 0x7f, 0xbb} {
		t.Fatalf("okhsl(240 100%% 50%%) = %v，期望 [0 127 187]", blue)
	}
	// 参考值来自 pi-tui 的 parseColor（pi 自己渲染主题时用的实现）。
	// pi light 的 vars：text / muted / violet / blue / blueBg。
	near(t, srgb8(okhslToSRGB(225, 0.05, 0.27)), [3]int{0x3b, 0x3f, 0x41})
	near(t, srgb8(okhslToSRGB(229, 0.08, 0.47)), [3]int{0x67, 0x71, 0x76})
	near(t, srgb8(okhslToSRGB(295, 0.60, 0.46)), [3]int{0x74, 0x59, 0xb4})
	near(t, srgb8(okhslToSRGB(231, 0.68, 0.47)), [3]int{0x2f, 0x78, 0x99})
	near(t, srgb8(okhslToSRGB(235, 0.19, 0.91)), [3]int{0xdf, 0xe7, 0xec})
	// 无彩度：三通道相等（OKLab 灰经传递函数后非线性，故不是 l*255）。
	near(t, srgb8(okhslToSRGB(0, 0, 0.5)), [3]int{0x77, 0x77, 0x77})
	// 与 sRGB 红等价：OKLCH 中的纯色应回到原值。
	lab := oklabFromLinearRGB(1, 0, 0)
	near(t, srgb8(oklchToSRGB(lab[0], math.Hypot(lab[1], lab[2]), math.Atan2(lab[2], lab[1])*180/math.Pi)), [3]int{255, 0, 0})
	// 超出色域的 chroma 被收回而没有越界。
	out := srgb8(oklchToSRGB(0.7, 0.5, 30))
	for _, v := range out {
		if v < 0 || v > 255 {
			t.Fatalf("越界颜色未 clamp：%v", out)
		}
	}
	if got := rgbHex(okhslToSRGB(225, 0.05, 0.27)); got != "#3b3f41" {
		t.Fatalf("rgbHex 输出 = %q", got)
	}
}
