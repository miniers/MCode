package mcodeui

import (
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// 基础颜色令牌 (Hex / RGBA)
var (
	// 主文本色 (85% labelColor)
	ColorTextPrimaryLight = ui.RGBA(0, 0, 0, 0.85)       // 近似 #000000
	ColorTextPrimaryDark  = ui.RGBA(255, 255, 255, 0.85) // 近似 #FFFFFF

	// 次要文本色 (60% secondaryText)
	ColorTextSecondaryLight = ui.RGBA(0, 0, 0, 0.60)       // 近似 #4D4D4D
	ColorTextSecondaryDark  = ui.RGBA(255, 255, 255, 0.60) // 近似 #B3B3B3

	// 三级文本色 (38% tertiaryLabel)：读数、次要辅助信息
	ColorTextTertiaryLight = ui.RGBA(0, 0, 0, 0.38)
	ColorTextTertiaryDark  = ui.RGBA(255, 255, 255, 0.38)

	// 强调色 (accent)
	ColorAccentLight = ui.Hex("#007AFF") // 系统强调色 (浅色默认)
	ColorAccentDark  = ui.Hex("#0A84FF") // 系统强调色 (深色默认)

	// 状态语义色
	ColorDangerLight  = ui.Hex("#FF3B30") // 危险/失败/删除 (浅色)
	ColorDangerDark   = ui.Hex("#FF453A") // 危险/失败/删除 (深色)
	ColorWarningLight = ui.Hex("#FF9500") // 警告/审批等待 (#FF9500)
	ColorWarningDark  = ui.Hex("#FF9F0A") // 警告/审批等待 (#FF9F0A)
	ColorSuccessLight = ui.Hex("#28CD41") // 成功/已完成归档 (浅色)
	ColorSuccessDark  = ui.Hex("#30D158") // 成功/已完成归档 (深色)
	ColorCautionLight = ui.Hex("#C9A227") // 配额黄色档 (配额使用过半, 浅色)
	ColorCautionDark  = ui.Hex("#E8C15A") // 配额黄色档 (配额使用过半, 深色)
	ColorPlanLight    = ui.Hex("#AF52DE") // Plan 模式高亮 (系统紫，浅色)
	ColorPlanDark     = ui.Hex("#BF5AF2") // Plan 模式高亮 (系统紫，深色)

	// 交互叠加态 (8% hover, 12% selected)
	ColorHoverLight    = ui.RGBA(0, 0, 0, 0.08)
	ColorHoverDark     = ui.RGBA(255, 255, 255, 0.08)
	ColorSelectedLight = ui.RGBA(0, 0, 0, 0.12)
	ColorSelectedDark  = ui.RGBA(255, 255, 255, 0.12)

	// 分隔线与边框 (Divider 22%, Border 14%)
	ColorDividerLight = ui.RGBA(0, 0, 0, 0.22)
	ColorDividerDark  = ui.RGBA(255, 255, 255, 0.22)
	ColorBorderLight  = ui.RGBA(0, 0, 0, 0.14)
	ColorBorderDark   = ui.RGBA(255, 255, 255, 0.14)
)

// 字号与字重常量
const (
	FontSizeTitle3       float32 = 15.0 // 大标题 / 面板标题
	FontSizeCallout      float32 = 12.0 // 胶囊图标字体
	FontSizeSubheadline  float32 = 11.0 // 行内小图标字体
	FontSizeCapsuleText  float32 = 12.5 // 胶囊文字按钮
	FontSizeThreadTitle  float32 = 13.0 // 活跃会话行标题
	FontSizeDormantTitle float32 = 12.0 // Dormant 会话行标题
	FontSizeProjectName  float32 = 11.0 // 项目名称 / 分支副标
	FontSizeRelativeTime float32 = 11.0 // 相对时间标签
)

// 尺寸与间距常量
const (
	// 窗口尺寸
	WindowDefaultWidth  = 1320
	WindowDefaultHeight = 860
	// WindowMinWidth 固定窗口最小宽度，避免随侧边栏默认宽度变化。
	WindowMinWidth  = 948
	WindowMinHeight = 560

	// 侧边栏布局
	SidebarDefaultWidth      = 288
	SidebarMinWidth          = 240
	SidebarMaxWidth          = 420
	SidebarCollapseThreshold = 110

	// 间距与尺寸
	ChromeHorizontalPadding  = 14
	ChromeTopPadding         = 12
	CapsuleContentHeight     = 28
	CapsuleVerticalPadding   = 2
	CapsuleHorizontalPadding = 12
	CapsuleInnerPadding      = 3 // 胶囊左右内边距，对齐 Chrome.iconCapsuleInnerPadding
	CapsuleHeight            = 32
	ContentBreathBelowChrome = 28
	RowHorizontalPadding     = 8
	ListInset                = 10
	GroupGap                 = 10
	FolderIndent             = 12
	DormantGap               = 4

	// 行高
	HeightProjectRow        = 32
	HeightProjectRowCompact = 26
	HeightThreadRow         = 28
	HeightThreadRowCompact  = 24
	HeightDormantRow        = 24
	HeightDormantRowCompact = 20
	HeightBottomBar         = 28
	TrafficLightTopOffset   = 30 // 14 + 16 pt 占位高度

	// 圆角
	SheetCornerRadius = 16
	CardCornerRadius  = 16
	RowCornerRadius   = 7  // 行高亮背景圆角
	CapsuleRadius     = 14 // 胶囊标准圆角 (28/2)
	DividerWidth      = 1  // 分隔线宽度
)

// 表面颜色令牌。
var (
	ColorSurfaceCanvasLight  = ui.Hex("#F5F5F3")
	ColorSurfaceCanvasDark   = ui.Hex("#141414")
	ColorSurfaceMainLight    = ui.Hex("#FAFAF9")
	ColorSurfaceMainDark     = ui.Hex("#1C1C1C")
	ColorSurfaceCardLight    = ui.Hex("#FFFFFF")
	ColorSurfaceCardDark     = ui.Hex("#1F1F1F")
	ColorSurfaceRaisedLight  = ui.Hex("#FFFFFF")
	ColorSurfaceRaisedDark   = ui.Hex("#242424")
	ColorSurfacePopoverLight = ui.Hex("#FFFFFF")
	ColorSurfacePopoverDark  = ui.Hex("#2B2B2B")
)

// 力度滑块的模型品牌色：档位标题、滑轨填充与融合色的取色表。
// 这些颜色固定画在模型选择浮层的深色面板上（见 visual_style_guard_test.go 的 fixedDarkSurfaceFiles），
// 是 Provider 品牌色，不随主题变化；数值与原实现逐一对齐。
var (
	effortBoltYellow = ui.Hex("#FFD60A") // 快速模式系统黄
	effortFastTitle  = ui.Hex("#FFD65C") // 快速模式标题金
	effortFastFill   = ui.Hex("#EDA821") // 快速模式填充金

	// Gemini 扫色：蓝 → 紫 → 珊瑚。
	effortGeminiColors = []ui.Color{ui.Hex("#4285F5"), ui.Hex("#9C73CC"), ui.Hex("#D96670")}

	effortClaudeTitle   = ui.Hex("#ED9470")
	effortClaudeFill    = ui.Hex("#CC6B47")
	effortDeepseekTitle = ui.Hex("#7D94FF")
	effortDeepseekFill  = ui.Hex("#3D5CFA")
	effortMetaTitle     = ui.Hex("#4FA3FF")
	effortMetaFill      = ui.Hex("#0063E0")
	effortMetaFillLight = ui.Hex("#0082FA")
	effortMetaFillDeep  = ui.Hex("#0073ED")
	effortGeminiFill    = ui.Hex("#8F73CC")
	effortSilverTitle   = ui.Hex("#EBEBF0")
	effortSilverFill    = ui.Hex("#E0E0E8")
	effortSilverPair    = ui.Hex("#CCCCCC")
	effortSilverPairHi  = ui.Hex("#F2F2F2")
	effortPurpleTitle   = ui.Hex("#BD8CFF")
	effortPurpleFill    = ui.Hex("#8C57F7")
)

// 看图遮罩固定为深色，不随主题变化；下列具名颜色是浮层专用色：
// 全窗口遮罩、遮罩上的白字（主/次/弱）与半透明白色按钮底。
var (
	imagePreviewScrimColor     = ui.RGBA(0, 0, 0, 0.78)
	imagePreviewMutedTextColor = ui.RGBA(255, 255, 255, 0.72)
	imagePreviewWeakTextColor  = ui.RGBA(255, 255, 255, 0.8)
	imagePreviewTextColor      = ui.RGB(255, 255, 255)
	imagePreviewNavBgColor     = ui.RGBA(255, 255, 255, 0.16)
	imagePreviewNavHoverBg     = ui.RGBA(255, 255, 255, 0.28)
	imagePreviewBtnBgColor     = ui.RGBA(255, 255, 255, 0.12)
	imagePreviewBtnHoverBg     = ui.RGBA(255, 255, 255, 0.22)
)

// Swarm 档位色：与主题无关的固定档位强调色。
var (
	// swarmTierTealColor 是 read 档位的蓝绿色 (48, 176, 199)。
	swarmTierTealColor = ui.RGBA(48, 176, 199, 1.0)
	// swarmTierPurpleColor 是 deep 档位的紫色 (175, 82, 222)。
	swarmTierPurpleColor = ui.RGBA(175, 82, 222, 1.0)
)

// identityPalette 是人设色：Swarm 名册（小写名字）与外部代理各一色。
// 名册色由 ThemeIdentity 取用，浮卡外发光与头像、名字因此始终同色。
var identityPalette = map[string]ui.Color{
	"aspen": ui.Hex("#E66A56"), "birch": ui.Hex("#A0E3A4"),
	"cove": ui.Hex("#8D8FE2"), "dune": ui.Hex("#E67A86"),
	"ember": ui.Hex("#7DEE63"), "flint": ui.Hex("#567DE6"),
	"grove": ui.Hex("#E3A0B7"), "heath": ui.Hex("#B1E28D"),
	"isle": ui.Hex("#7AB1E6"), "jade": ui.Hex("#EE63B4"),
	"kestrel": ui.Hex("#B6E656"), "larch": ui.Hex("#A0D2E3"),
	"moss": ui.Hex("#E28DD4"), "nimbus": ui.Hex("#DCE67A"),
	"onyx": ui.Hex("#63ECEE"), "pike": ui.Hex("#DC56E6"),
	"quill": ui.Hex("#E3DAA0"), "reed": ui.Hex("#8DE2CF"),
	"sage": ui.Hex("#C57AE6"), "tide": ui.Hex("#EEB863"),
	"umber": ui.Hex("#56E6A3"), "vale": ui.Hex("#BFA0E3"),
	"wren": ui.Hex("#E2AD8D"), "yarrow": ui.Hex("#7AE69A"),
	"zephyr": ui.Hex("#8163EE"),
	"claude": ui.Hex("#D97757"), "codex": ui.Hex("#10A37F"),
	"gemini": ui.Hex("#4285F4"), "deepseek": ui.Hex("#4D6BFE"),
	"meta": ui.Hex("#0082FB"),
}

func ThemeIdentity(key string) ui.Color {
	if color, ok := identityPalette[strings.ToLower(key)]; ok {
		return color
	}
	return ColorTextSecondaryLight
}

type themePalette struct {
	Canvas, Main, Card, Raised, Popover, Text, TextSecondary, TextTertiary, Accent, Danger, Warning, Success, Caution, Plan, Border, Divider ui.Color
	AccentSystem                                                                                                                             bool
}

var builtinPalettes = map[string]themePalette{
	config.ThemeLight: {
		Canvas:        ColorSurfaceCanvasLight,
		Main:          ColorSurfaceMainLight,
		Card:          ColorSurfaceCardLight,
		Raised:        ColorSurfaceRaisedLight,
		Popover:       ColorSurfacePopoverLight,
		Text:          ColorTextPrimaryLight,
		TextSecondary: ColorTextSecondaryLight,
		TextTertiary:  ColorTextTertiaryLight,
		Accent:        ColorAccentLight,
		Danger:        ColorDangerLight,
		Warning:       ColorWarningLight,
		Success:       ColorSuccessLight,
		Caution:       ColorCautionLight,
		Plan:          ColorPlanLight,
		Border:        ColorBorderLight,
		Divider:       ColorDividerLight,
	},
	config.ThemeDark: {
		Canvas:        ColorSurfaceCanvasDark,
		Main:          ColorSurfaceMainDark,
		Card:          ColorSurfaceCardDark,
		Raised:        ColorSurfaceRaisedDark,
		Popover:       ColorSurfacePopoverDark,
		Text:          ColorTextPrimaryDark,
		TextSecondary: ColorTextSecondaryDark,
		TextTertiary:  ColorTextTertiaryDark,
		Accent:        ColorAccentDark,
		Danger:        ColorDangerDark,
		Warning:       ColorWarningDark,
		Success:       ColorSuccessDark,
		Caution:       ColorCautionDark,
		Plan:          ColorPlanDark,
		Border:        ColorBorderDark,
		Divider:       ColorDividerDark,
	},
}

type resolvedTheme struct {
	light, dark           themePalette
	fixed                 *themePalette
	lightFonts, darkFonts themeFonts
	fixedFonts            *themeFonts
}

// 发布后不再修改调色板或字号，渲染与配置回调可并发读取。
var activeTheme atomic.Pointer[resolvedTheme]
var themeDark atomic.Bool

func init() { setActiveTheme(config.AppearanceConfig{}) }

func parseThemeColor(value string) (ui.Color, bool) {
	if (len(value) != 7 && len(value) != 9) || value[0] != '#' {
		return ui.Color{}, false
	}
	n, err := strconv.ParseUint(value[1:], 16, 32)
	if err != nil {
		return ui.Color{}, false
	}
	if len(value) == 7 {
		n = n<<8 | 255
	}
	return ui.Color{R: uint8(n >> 24), G: uint8(n >> 16), B: uint8(n >> 8), A: uint8(n)}, true
}

func resolvePalette(cfg config.AppearanceConfig, id string) themePalette {
	def, _ := cfg.ThemeByID(id)
	base := def.Base
	if !config.IsBuiltinTheme(base) {
		base = config.ThemeDark
	}
	p := builtinPalettes[base]
	fields := map[string]*ui.Color{
		"canvas":         &p.Canvas,
		"main":           &p.Main,
		"card":           &p.Card,
		"raised":         &p.Raised,
		"popover":        &p.Popover,
		"text":           &p.Text,
		"text_secondary": &p.TextSecondary,
		"text_tertiary":  &p.TextTertiary,
		"accent":         &p.Accent,
		"danger":         &p.Danger,
		"warning":        &p.Warning,
		"success":        &p.Success,
		"caution":        &p.Caution,
		"plan":           &p.Plan,
		"border":         &p.Border,
		"divider":        &p.Divider,
	}
	for key, value := range def.Colors {
		if key == "accent" && value == config.ThemeAccentSystem {
			p.AccentSystem = true
			continue
		}
		if field := fields[key]; field != nil {
			if color, ok := parseThemeColor(value); ok {
				*field = color
			}
		}
	}
	return p
}

func setActiveTheme(cfg config.AppearanceConfig) {
	appReduceMotion.Store(cfg.ReduceMotion)
	next := &resolvedTheme{
		light: resolvePalette(cfg, config.ThemeLight), dark: resolvePalette(cfg, config.ThemeDark),
		lightFonts: resolveThemeFonts(cfg, config.ThemeLight), darkFonts: resolveThemeFonts(cfg, config.ThemeDark),
	}
	if cfg.Theme != config.ThemeSystem && !config.IsBuiltinTheme(cfg.Theme) {
		if _, ok := cfg.ThemeByID(cfg.Theme); ok {
			palette, fonts := resolvePalette(cfg, cfg.Theme), resolveThemeFonts(cfg, cfg.Theme)
			next.fixed, next.fixedFonts = &palette, &fonts
		}
	}
	activeTheme.Store(next)
}

func paletteOf(c *ui.Context) themePalette {
	theme := activeTheme.Load()
	if theme.fixed != nil {
		return *theme.fixed
	}
	if isDark(c) {
		return theme.dark
	}
	return theme.light
}

// 动画状态由窗口根节点持有，窗口关闭时随节点释放，不共享多窗口的过渡进度。
func themeColor(c *ui.Context, key string, target ui.Color) ui.Color {
	if c == nil || reduceMotion(c) {
		return target
	}
	return c.Root().AnimateColor("app-theme/"+key, target, ui.Animation{Duration: motionFast, Ease: motionEaseStandard})
}

func ThemeSurfaceCanvas(c *ui.Context) ui.Color { return themeColor(c, "Canvas", paletteOf(c).Canvas) }
func ThemeSurfaceMain(c *ui.Context) ui.Color   { return themeColor(c, "Main", paletteOf(c).Main) }
func ThemeSurfaceCard(c *ui.Context) ui.Color   { return themeColor(c, "Card", paletteOf(c).Card) }
func ThemeSurfaceRaised(c *ui.Context) ui.Color { return themeColor(c, "Raised", paletteOf(c).Raised) }
func ThemeSurfacePopover(c *ui.Context) ui.Color {
	return themeColor(c, "Popover", paletteOf(c).Popover)
}
func ThemeOverlayHover(c *ui.Context) ui.Color    { return ThemeOverlay(c, 0.05) }
func ThemeOverlaySelected(c *ui.Context) ui.Color { return ThemeOverlay(c, 0.10) }
func ThemeOverlayPressed(c *ui.Context) ui.Color  { return ThemeOverlay(c, 0.14) }
func ThemeAccentHover(c *ui.Context) ui.Color     { return ThemeAccent(c).Mix(ThemePrimaryText(c), 0.10) }
func ThemeAccentPressed(c *ui.Context) ui.Color   { return ThemeAccent(c).Mix(ThemePrimaryText(c), 0.18) }
func ThemeAccentSubtle(c *ui.Context) ui.Color    { return ThemeAccent(c).Alpha(0.12) }
func ThemeOnAccent(c *ui.Context) ui.Color        { return ui.Hex("#FFFFFF") }
func ThemeHairline(c *ui.Context) ui.Color        { return ThemeOverlay(c, 0.10) }

// nativeTheme 保留基础主题的系统偏好，并覆盖应用颜色。
func nativeTheme(base ui.Theme, colors ui.Theme, highContrast bool) ui.Theme {
	accent := colors.Accent
	accent.A = 255
	black, white := ui.Color{A: 255}, ui.Color{R: 255, G: 255, B: 255, A: 255}
	base.Background, base.Surface = colors.Background, colors.Surface
	base.SurfaceHover, base.SurfacePressed = colors.SurfaceHover, colors.SurfacePressed
	base.Border, base.Text, base.TextMuted = colors.Border, colors.Text, colors.TextMuted
	base.Accent = accent
	if base.Dark {
		base.AccentHover, base.AccentPressed = accent.Mix(white, 0.2), accent.Mix(black, 0.15)
	} else {
		base.AccentHover, base.AccentPressed = accent.Mix(black, 0.12), accent.Mix(black, 0.25)
	}
	base.AccentText = colors.AccentText
	base.Danger, base.Warning, base.Success = colors.Danger, colors.Warning, colors.Success
	base.Selection, base.Focus = colors.Selection, colors.Focus
	base.Inverse, base.InverseText = colors.Inverse, colors.InverseText
	base.Scrollbar, base.ScrollbarWidth = colors.Scrollbar, 8
	base.Radius, base.Spacing = RadiusSM, Space1
	if highContrast {
		base.Focus.A = 255
	}
	return base
}

func nativeThemeColors(c *ui.Context) ui.Theme {
	return ui.Theme{
		Background: ThemeSurfaceCanvas(c), Surface: ThemeSurfaceCard(c),
		SurfaceHover: ThemeOverlayHover(c), SurfacePressed: ThemeOverlayPressed(c),
		Border: ThemeBorder(c), Text: ThemePrimaryText(c), TextMuted: ThemeSecondaryText(c),
		Accent: ThemeAccent(c), AccentText: ThemeOnAccent(c),
		Danger: ThemeDanger(c), Warning: ThemeWarning(c), Success: ThemeSuccess(c),
		Selection: ThemeAccentSubtle(c), Focus: ThemeAccent(c),
		Inverse: ThemeSurfacePopover(c), InverseText: ThemePrimaryText(c),
		Scrollbar: ThemeOverlay(c, 0.28),
	}
}

var appThemeCache struct {
	base         ui.Theme
	colors       ui.Theme
	highContrast bool
	theme        *ui.Theme
}

func applyAppTheme(c *ui.Context) {
	base := c.Theme()
	if base == nil {
		return
	}
	themeDark.Store(base.Dark)
	colors := nativeThemeColors(c)
	highContrast := c.Preferences().HighContrast
	if appThemeCache.theme == nil || appThemeCache.base != *base || appThemeCache.colors != colors || appThemeCache.highContrast != highContrast {
		theme := nativeTheme(*base, colors, highContrast)
		appThemeCache.base, appThemeCache.colors, appThemeCache.highContrast, appThemeCache.theme = *base, colors, highContrast, &theme
	}
	c.SetTheme(appThemeCache.theme)
}

// isDark 辅助判断当前上下文是否处于深色模式。
func isDark(c *ui.Context) bool {
	if c == nil || c.Theme() == nil {
		return true // 默认深色
	}
	return c.Theme().Dark
}

// ThemeOverlay 返回指定不透明度的主文本遮罩色 (浅色黑色遮罩，深色白色遮罩)。
func ThemeOverlay(c *ui.Context, opacity float32) ui.Color {
	color := ThemePrimaryText(c)
	return ui.RGBA(color.R, color.G, color.B, opacity)
}

// ThemePrimaryText 返回当前模式的主文本色。
func ThemePrimaryText(c *ui.Context) ui.Color { return themeColor(c, "Text", paletteOf(c).Text) }

// ThemeSecondaryText 返回当前模式的次要文本色。
func ThemeSecondaryText(c *ui.Context) ui.Color {
	return themeColor(c, "TextSecondary", paletteOf(c).TextSecondary)
}

// ThemeAccent 返回当前模式的强调色 (#0A84FF 深色 / #007AFF 浅色)。
func ThemeAccent(c *ui.Context) ui.Color {
	p := paletteOf(c)
	if p.AccentSystem && c != nil {
		if accent := c.Preferences().Accent; accent.A != 0 {
			return themeColor(c, "Accent", accent)
		}
	}
	return themeColor(c, "Accent", p.Accent)
}

// ThemeDanger 返回当前模式的危险色。
func ThemeDanger(c *ui.Context) ui.Color { return themeColor(c, "Danger", paletteOf(c).Danger) }

// ThemeWarning 返回当前模式的警告/审批等待色 (#FF9500 / #FF9F0A)。
func ThemeWarning(c *ui.Context) ui.Color { return themeColor(c, "Warning", paletteOf(c).Warning) }

// ThemeSuccess 返回当前模式的成功/完成色。
func ThemeSuccess(c *ui.Context) ui.Color { return themeColor(c, "Success", paletteOf(c).Success) }

// ThemeCaution 返回当前模式的配额黄色档色 (#C9A227 / #E8C15A)：配额使用过半但尚未进入警告。
func ThemeCaution(c *ui.Context) ui.Color { return themeColor(c, "Caution", paletteOf(c).Caution) }

// ThemeTertiaryText 返回当前模式的三级文本色 (38% 透明度)，用于低优先级的辅助读数。
func ThemeTertiaryText(c *ui.Context) ui.Color {
	return themeColor(c, "TextTertiary", paletteOf(c).TextTertiary)
}

// ThemePlan 返回当前模式的 Plan 模式高亮紫色 (#AF52DE / #BF5AF2)。
func ThemePlan(c *ui.Context) ui.Color { return themeColor(c, "Plan", paletteOf(c).Plan) }

// ThemeWindowBg 返回当前模式的窗口底色。
func ThemeWindowBg(c *ui.Context) ui.Color {
	return ThemeSurfaceCanvas(c)
}

// ThemeSidebarBg 返回当前模式的侧边栏底色（不透明实心，不再依赖毛玻璃）。
func ThemeSidebarBg(c *ui.Context) ui.Color {
	return ThemeSurfaceCanvas(c)
}

// ThemeSurface 返回当前模式的卡片/浮层底色。
func ThemeSurface(c *ui.Context) ui.Color {
	return ThemeSurfaceCard(c)
}

// ThemeHover 返回当前模式的悬停态叠加色 (8%)。
func ThemeHover(c *ui.Context) ui.Color { return ThemeOverlay(c, 0.08) }

// ThemeSelected 返回当前模式的选中态叠加色 (12%)。
func ThemeSelected(c *ui.Context) ui.Color { return ThemeOverlay(c, 0.12) }

// ThemeDivider 返回当前模式的分隔线颜色 (22%)。
func ThemeDivider(c *ui.Context) ui.Color { return themeColor(c, "Divider", paletteOf(c).Divider) }

// ThemeBorder 返回当前模式的边框颜色 (14%)。
func ThemeBorder(c *ui.Context) ui.Color { return themeColor(c, "Border", paletteOf(c).Border) }

// effortBrandColor 返回推理强度最高档使用的 Provider 品牌色（集中原先散落在
// effort_slider.go 的硬编码映射）：
//   - claude → Anthropic 赤陶 #D97757，codex → OpenAI 绿 #10A37F（沿用既有映射）；
//   - gemini/antigravity/agy → Gemini 蓝 #4285F4（Gemini 扫色首色）；
//   - deepseek → DeepSeek 蓝 #4D6BFE，meta → Meta 蓝 #0082FB；
//   - cursor/opencode/copilot/commandcode/pi/omp/zai 等黑白标识品牌统一白色；
//   - 未知 provider 返回主题 accent（composer 卡片固定深色玻璃底，取深色 accent）。
func effortBrandColor(provider string) ui.Color {
	switch strings.ToLower(provider) {
	case "claude":
		return ui.Hex("#D97757")
	case "codex":
		return ui.Hex("#10A37F")
	case "gemini", "antigravity", "agy":
		return ui.Hex("#4285F4")
	case "deepseek":
		return ui.Hex("#4D6BFE")
	case "meta":
		return ui.Hex("#0082FB")
	case "cursor", "opencode", "opencode go", "opencodego", "opencode zen", "zen",
		"copilot", "github", "commandcode", "command code", "cmd",
		"mcode", "pi", "mu", "omp", "zai", "z.ai", "glm", "grok", "devin", "cline", "droid":
		return ui.RGB(255, 255, 255)
	default:
		return ColorAccentDark
	}
}

// effortFastColor 返回快速模式的金色（与快速模式按钮、标题快速态取同一色）。
func effortFastColor() ui.Color {
	return ui.Hex("#FACC15")
}

// codeFontFamily 返回配置的代码等宽字体，未配置时回退到系统等宽字体 "monospace"。
func codeFontFamily(cfg config.AppearanceConfig) string {
	f := strings.TrimSpace(cfg.CodeFont)
	if f == "" || f == "系统默认" {
		return "monospace"
	}
	return f
}
