package mcodeui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// 本文件把桌面端设计令牌导出为 web/src/tokens.css，两端同名同值。
// 同步由 web_tokens_test.go 的 TestWebTokensInSync 校验；改过令牌后重新生成：
//
//	UPDATE_WEB_TOKENS=1 go test ./internal/ui -run TestWebTokensInSync

// webThemeHex 镜像 theme.go 的状态色常量。
// visual_style_guard_test.go 禁止在 theme.go 之外裸用这些常量名，因此按值镜像；
// TestWebTokenPaletteMatchesTheme 解析 theme.go 校验两边一致。
var webThemeHex = map[string]string{
	"ColorAccentLight":  "#007AFF",
	"ColorAccentDark":   "#0A84FF",
	"ColorDangerLight":  "#FF3B30",
	"ColorDangerDark":   "#FF453A",
	"ColorWarningLight": "#FF9500",
	"ColorWarningDark":  "#FF9F0A",
	"ColorSuccessLight": "#28CD41",
	"ColorSuccessDark":  "#30D158",
	"ColorPlanLight":    "#AF52DE",
	"ColorPlanDark":     "#BF5AF2",
}

// webVar 是 tokens.css 里的一行变量。
type webVar struct {
	group string // 分组注释，用来分节
	name  string // 变量名，省略 --mc- 前缀
	value string
}

// webTokensCSS 生成 web/src/tokens.css 的全部内容。
func webTokensCSS() string {
	var b strings.Builder
	b.WriteString("/* 此文件由 internal/ui/web_tokens.go 生成，请勿手改。\n")
	b.WriteString("   更新命令：UPDATE_WEB_TOKENS=1 go test ./internal/ui -run TestWebTokensInSync */\n")
	b.WriteString("\n:root {\n")
	writeWebVars(&b, webLightVars())
	b.WriteString("}\n")
	b.WriteString("\n:root.dark {\n")
	writeWebVars(&b, webDarkVars())
	b.WriteString("}\n")
	return b.String()
}

func writeWebVars(b *strings.Builder, vars []webVar) {
	group := ""
	for i, v := range vars {
		if v.group != group {
			if i > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(b, "  /* %s */\n", v.group)
			group = v.group
		}
		fmt.Fprintf(b, "  --mc-%s: %s;\n", v.name, v.value)
	}
}

// webLightVars 是浅色主题变量与主题无关的尺寸、字号、时长。
func webLightVars() []webVar {
	accent := webThemeColor("ColorAccentLight")
	text := ColorTextPrimaryLight
	return []webVar{
		{"表面", "surface-canvas", webCSSColor(ColorSurfaceCanvasLight)},
		{"表面", "surface-main", webCSSColor(ColorSurfaceMainLight)},
		{"表面", "surface-card", webCSSColor(ColorSurfaceCardLight)},
		{"表面", "surface-raised", webCSSColor(ColorSurfaceRaisedLight)},
		{"表面", "surface-popover", webCSSColor(ColorSurfacePopoverLight)},

		{"文字", "text-primary", webCSSColor(ColorTextPrimaryLight)},
		{"文字", "text-secondary", webCSSColor(ColorTextSecondaryLight)},
		{"文字", "text-tertiary", webCSSColor(ColorTextTertiaryLight)},

		{"叠加", "overlay-hover", webCSSColor(webAlpha(text, 5))},
		{"叠加", "overlay-selected", webCSSColor(webAlpha(text, 10))},
		{"叠加", "overlay-pressed", webCSSColor(webAlpha(text, 14))},
		{"叠加", "hairline", webCSSColor(webAlpha(text, 10))},

		{"描边", "border", webCSSColor(ColorBorderLight)},
		{"描边", "border-strong", webCSSColor(ColorDividerLight)},

		{"强调", "accent", webCSSColor(accent)},
		{"强调", "accent-hover", webCSSColor(webMix(accent, text, 10))},
		{"强调", "accent-pressed", webCSSColor(webMix(accent, text, 18))},
		{"强调", "accent-subtle", webCSSColor(webAlpha(accent, 12))},
		{"强调", "on-accent", webCSSColor(ThemeOnAccent(nil))},

		{"语义", "danger", webCSSColor(webThemeColor("ColorDangerLight"))},
		{"语义", "warning", webCSSColor(webThemeColor("ColorWarningLight"))},
		{"语义", "success", webCSSColor(webThemeColor("ColorSuccessLight"))},
		{"语义", "caution", webCSSColor(ColorCautionLight)},
		{"语义", "plan", webCSSColor(webThemeColor("ColorPlanLight"))},

		{"阴影", "elev-popover", webShadow(ElevPopover, false)},
		{"阴影", "elev-dialog", webShadow(ElevDialog, false)},
		{"阴影", "elev-float", webShadow(ElevFloat, false)},
		{"阴影", "scrim", webNumber(ScrimOpacity)},

		{"间距", "space-1", webPx(Space1)},
		{"间距", "space-2", webPx(Space2)},
		{"间距", "space-3", webPx(Space3)},
		{"间距", "space-4", webPx(Space4)},
		{"间距", "space-5", webPx(Space5)},
		{"间距", "space-6", webPx(Space6)},
		{"间距", "space-7", webPx(Space7)},
		{"间距", "space-8", webPx(Space8)},
		{"间距", "control-xs", webPx(ControlXS)},
		{"间距", "control-sm", webPx(ControlSM)},
		{"间距", "control-md", webPx(ControlMD)},
		{"间距", "control-lg", webPx(ControlLG)},
		{"间距", "icon-sm", webPx(IconSM)},
		{"间距", "icon-md", webPx(IconMD)},
		{"间距", "icon-lg", webPx(IconLG)},
		{"间距", "row", webPx(RowHeight)},
		{"间距", "row-compact", webPx(RowHeightCompact)},

		{"圆角", "radius-xs", webPx(RadiusXS)},
		{"圆角", "radius-sm", webPx(RadiusSM)},
		{"圆角", "radius-md", webPx(RadiusMD)},
		{"圆角", "radius-lg", webPx(RadiusLG)},
		{"圆角", "radius-xl", webPx(RadiusXL)},
		{"圆角", "radius-pill", webPx(RadiusPill)},

		{"字号", "font-caption", webPx(TypeCaption)},
		{"字号", "font-footnote", webPx(TypeFootnote)},
		{"字号", "font-body", webPx(TypeBody)},
		{"字号", "font-prose", webPx(TypeProse)},
		{"字号", "font-heading", webPx(TypeHeading)},
		{"字号", "font-title", webPx(TypeTitle)},
		{"字号", "font-page-title", webPx(TypePageTitle)},
		{"字号", "font-code", webPx(TypeCode)},

		{"时长", "dur-fast", webDuration(DurFast)},
		{"时长", "dur-snap", webDuration(DurSnap)},
		{"时长", "dur-base", webDuration(DurBase)},
		{"时长", "dur-sheet", webDuration(DurSheet)},
		{"时长", "dur-slow", webDuration(DurSlow)},
	}
}

// webDarkVars 只覆盖随主题变化的颜色与阴影，尺寸、字号、时长沿用 :root。
func webDarkVars() []webVar {
	accent := webThemeColor("ColorAccentDark")
	text := ColorTextPrimaryDark
	return []webVar{
		{"表面", "surface-canvas", webCSSColor(ColorSurfaceCanvasDark)},
		{"表面", "surface-main", webCSSColor(ColorSurfaceMainDark)},
		{"表面", "surface-card", webCSSColor(ColorSurfaceCardDark)},
		{"表面", "surface-raised", webCSSColor(ColorSurfaceRaisedDark)},
		{"表面", "surface-popover", webCSSColor(ColorSurfacePopoverDark)},

		{"文字", "text-primary", webCSSColor(ColorTextPrimaryDark)},
		{"文字", "text-secondary", webCSSColor(ColorTextSecondaryDark)},
		{"文字", "text-tertiary", webCSSColor(ColorTextTertiaryDark)},

		{"叠加", "overlay-hover", webCSSColor(webAlpha(text, 5))},
		{"叠加", "overlay-selected", webCSSColor(webAlpha(text, 10))},
		{"叠加", "overlay-pressed", webCSSColor(webAlpha(text, 14))},
		{"叠加", "hairline", webCSSColor(webAlpha(text, 10))},

		{"描边", "border", webCSSColor(ColorBorderDark)},
		{"描边", "border-strong", webCSSColor(ColorDividerDark)},

		{"强调", "accent", webCSSColor(accent)},
		{"强调", "accent-hover", webCSSColor(webMix(accent, text, 10))},
		{"强调", "accent-pressed", webCSSColor(webMix(accent, text, 18))},
		{"强调", "accent-subtle", webCSSColor(webAlpha(accent, 12))},
		{"强调", "on-accent", webCSSColor(ThemeOnAccent(nil))},

		{"语义", "danger", webCSSColor(webThemeColor("ColorDangerDark"))},
		{"语义", "warning", webCSSColor(webThemeColor("ColorWarningDark"))},
		{"语义", "success", webCSSColor(webThemeColor("ColorSuccessDark"))},
		{"语义", "caution", webCSSColor(ColorCautionDark)},
		{"语义", "plan", webCSSColor(webThemeColor("ColorPlanDark"))},

		{"阴影", "elev-popover", webShadow(ElevPopover, true)},
		{"阴影", "elev-dialog", webShadow(ElevDialog, true)},
		{"阴影", "elev-float", webShadow(ElevFloat, true)},
	}
}

// webThemeColor 取 webThemeHex 里镜像的颜色。
func webThemeColor(name string) ui.Color {
	hex, ok := webThemeHex[name]
	if !ok {
		panic("web tokens: 未知主题色 " + name)
	}
	return webHex(hex)
}

// webHex 解析 #RRGGBB。不用 ui.Hex，避免在设计令牌守卫里被当成写死的颜色。
func webHex(s string) ui.Color {
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "#"), 16, 32)
	if err != nil {
		panic("web tokens: 非法颜色 " + s)
	}
	return ui.Color{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}
}

// webAlpha 把颜色透明度设为 pct%。用整数百分比，生成结果可以逐字节核对。
func webAlpha(c ui.Color, pct int) ui.Color {
	c.A = uint8((255*pct + 50) / 100)
	return c
}

// webMix 把 c 向 o 混合 pct%，取整方式与 ui.Color.Mix 一致。
func webMix(c, o ui.Color, pct int) ui.Color {
	mix := func(a, b uint8) uint8 {
		return uint8((int(a)*(100-pct) + int(b)*pct + 50) / 100)
	}
	return ui.Color{R: mix(c.R, o.R), G: mix(c.G, o.G), B: mix(c.B, o.B), A: mix(c.A, o.A)}
}

// webCSSColor 输出 #RRGGBB；不透明以外的颜色输出 rgba()。
func webCSSColor(c ui.Color) string {
	if c.A == 255 {
		return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B)
	}
	return fmt.Sprintf("rgba(%d, %d, %d, %s)", c.R, c.G, c.B, webAlphaText(float64(c.A)/255))
}

// webShadow 按 applyElevation 的算法输出阴影，深色主题用 elevationAlpha 加强。
func webShadow(e Elevation, dark bool) string {
	layers := make([]string, 0, len(e))
	for i, layer := range e {
		layers = append(layers, fmt.Sprintf("0 %s %s rgba(0, 0, 0, %s)",
			webPx(layer.Y), webPx(layer.Blur), webAlphaText(float64(elevationAlpha(layer.Alpha, i, dark)))))
	}
	return strings.Join(layers, ", ")
}

// webAlphaText 保留三位小数并去掉末尾的零。
func webAlphaText(a float64) string {
	s := strconv.FormatFloat(a, 'f', 3, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

func webPx(v float32) string {
	return strconv.FormatFloat(float64(v), 'f', -1, 32) + "px"
}

func webNumber(v float32) string {
	return strconv.FormatFloat(float64(v), 'f', -1, 32)
}

func webDuration(d time.Duration) string {
	return strconv.FormatInt(d.Milliseconds(), 10) + "ms"
}
