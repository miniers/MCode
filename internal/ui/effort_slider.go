package mcodeui

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

// effort 卡片固定画在模型选择浮层的深色面板上（见 visual_style_guard_test.go 的 fixedDarkSurfaceFiles），
// 所以这里只用固定深色常量：Theme* 会在浅色主题下取到浅色文字与叠加色，压在深色玻璃上对比度不足。
var (
	// effortOnDark 是白色叠加的基色：ThemeOnAccent 恒为 #FFFFFF，与主题无关。
	effortOnDark = ThemeOnAccent(nil)
	// effortTextSoft 是说明行文字（白 70%），比 ColorTextSecondaryDark（60%）略亮。
	effortTextSoft = effortOnDark.Alpha(0.70)
	// effortKnobOnLight 与 effortTickOnLight 用于滑轨填充本身是浅色的品牌：滑块描边与已越过刻度改深色。
	effortKnobOnLight = ColorBorderLight
	effortTickOnLight = ColorTextTertiaryLight
	// effortAccentBright 是普通档的 accent 提亮色（提亮 22%）。
	effortAccentBright = ColorAccentDark.Mix(effortOnDark, 0.22)
)

// EffortRosterSlot 定义双槽配对卡片中单侧（主代理或子代理）的行配置。
type EffortRosterSlot struct {
	Key         string
	Role        string // 如 "主代理" / "子代理"，单代理可为空
	BrandID     string
	ProviderID  string
	ModelTitle  string
	Placeholder string // 如 "跟随主代理"
	EffortLabel string // 英文短档位名如 "High" / "Med"，无档位为空
	Active      bool
	Disabled    bool
	LockedTip   string
	Accessory   func() // 可选右侧配件（如子代理并发上限胶囊）
	OnSelect    func() // 点击未激活行切换激活侧
	OnOpenList  func() // 点击激活行或行尾箭头打开该侧模型列表
}

// EffortSliderCardProps 定义推理强度调节卡片的入参属性。
type EffortSliderCardProps struct {
	KeyPrefix         string
	ProviderID        string
	BrandID           string // 模型品牌键，用于配色；为空时回退 ProviderID
	PairProviderID    string
	PairBrandID       string // 子代理模型品牌键，用于配色；为空时回退 PairProviderID
	Paired            bool
	IsBees            bool          // 是否为执行侧 (Bees)
	SwarmOn           bool          // Swarm 是否开启
	ModelTitle        string        // 当前侧模型名称；为空时回退 Subtitle
	Subtitle          string        // 说明行文案
	SubtitlePair      string        // 配对时主导侧说明行“+”后面的子代理模型名，图标取 PairBrandID / PairProviderID
	SubtitleAccessory func()        // 说明行右侧自定义小部件（如并发 Bee 上限胶囊）
	PairFav           *pairFavProps // 说明行左侧的配对收藏按钮；nil 时配对卡片退回 Swarm 标记
	TrailingAccessory func()        // 卡片右侧自定义小部件（默认渲染 Swarm 开关）
	LockedReason      string        // 卡片被禁用时各禁用控件 Tooltip 的说明原因；为空时用通用文案
	Efforts           []string
	Current           string // 当前档位，空表示用 DefaultEffort
	DefaultEffort     string
	SupportsFast      bool
	FastMode          bool
	Disabled          bool
	Roster            []EffortRosterSlot // 非空时按双槽同屏配对卡渲染
	OnEffort          func(string)
	OnToggleFast      func()
	OnReset           func()
	OnOpenModelList   func() // 为 nil 时不显示 chevron
	OnToggleSwarm     func() // 切换 Swarm 状态；为 nil 时卡片右侧不放 Swarm 开关
	Personas          []MagpiePersona
	Persona           string
	OnPersona         func(id string)
	PersonaOpen       *bool  // 可选：由外层持有子菜单开合状态；nil 时使用控件本地状态
	Invalidate        func() // 可选：人设子菜单开关或选中后触发宿主重绘
}

// 逆时针旋转箭头图标（Lucide rotate-ccw）。
var iconRotateCcw = parseLucideSVG(`<path d="M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8"/><path d="M3 3v5h5"/>`)

// effortSliderSnapIndex 将指定 x 坐标吸附到最近的刻度索引；n<=1 时返回 0，结果限制在 [0, n-1]。
func effortSliderSnapIndex(x, width float32, n int) int {
	if n <= 1 || width <= 0 || x <= 0 {
		return 0
	}
	if x >= width {
		return n - 1
	}
	step := width / float32(n-1)
	idx := int(float32(x)/step + 0.5)
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return idx
}

// effortTickPositions 返回 n 个刻度点在宽度 width 内的等距分布横坐标，首尾在两端。
func effortTickPositions(n int, width float32) []float32 {
	if n <= 0 {
		return nil
	}
	if n == 1 {
		return []float32{0}
	}
	pos := make([]float32, n)
	step := width / float32(n-1)
	for i := 0; i < n; i++ {
		pos[i] = float32(i) * step
	}
	pos[n-1] = width
	return pos
}

// effortIndex 依次取：当前值、默认值、中档、首档。
func effortIndex(efforts []string, current, def string) int {
	if len(efforts) == 0 {
		return 0
	}
	if current != "" {
		for i, e := range efforts {
			if e == current {
				return i
			}
		}
	}
	if def != "" {
		for i, e := range efforts {
			if e == def {
				return i
			}
		}
	}
	for i, e := range efforts {
		if e == "medium" {
			return i
		}
	}
	return 0
}

// effortLabel 将推理强度内部英文名转换为中文标签；minimal→最低，low→低，medium→中，high→高，xhigh→超高，max→最大，其他原样返回。
func effortLabel(e string) string {
	switch strings.ToLower(strings.TrimSpace(e)) {
	case "minimal":
		return "最低"
	case "none":
		return "无"
	case "low":
		return "低"
	case "medium":
		return "中"
	case "high":
		return "高"
	case "xhigh", "extra-high", "extra_high", "extra high":
		return "超高"
	case "max", "ultra":
		return "最大"
	default:
		return e
	}
}

// effortThumbLabel 将推理强度档位转换为滑块胶囊上展示的英文短标签：
// minimal→Min，none→Off，low→Low，medium→Med，high→High，xhigh→XHigh，max/ultra→Max；
// 其他非空值回退 modelEffortTitle，空串返回 ""。
func effortThumbLabel(e string) string {
	trimmed := strings.TrimSpace(e)
	if trimmed == "" {
		return ""
	}
	switch strings.ToLower(trimmed) {
	case "minimal":
		return "Min"
	case "none":
		return "Off"
	case "low":
		return "Low"
	case "medium":
		return "Med"
	case "high":
		return "High"
	case "xhigh", "extra-high", "extra_high", "extra high":
		return "XHigh"
	case "max", "ultra":
		return "Max"
	default:
		return modelEffortTitle(trimmed)
	}
}

// swarmSideTabLabel 生成分段标签展示文案：
// “<角色> · <档位英文名>”，例如“主导 · Medium”“执行 · Max”，档位名与 modelEffortTitle 一致。
// role 取 "lead" 或 "bees"；调用方应传入已回退到模型默认档位的 effort，
// effort 仍为空时只显示角色名；bees 在 off 时返回“执行 · 关闭”。
func swarmSideTabLabel(role string, effort string, off bool) string {
	name := "子"
	if role == "lead" {
		name = "主"
	}
	effText := ""
	if strings.TrimSpace(effort) != "" {
		effText = modelEffortTitle(effort)
	}
	if role == "lead" {
		if off || effText == "" {
			return name
		}
		return name + " · " + effText
	}
	if off {
		return name + " · 关闭"
	}
	if effText == "" {
		return name
	}
	return name + " · " + effText
}

// effortLockedTip 返回禁用控件的 Tooltip 文案：优先使用卡片给出的禁用原因。
func effortLockedTip(p EffortSliderCardProps, fallback string) string {
	if tip := strings.TrimSpace(p.LockedReason); tip != "" {
		return tip
	}
	return fallback
}

// effortResetButtonSize 是标题右侧紧凑重置按钮的直径。
const effortResetButtonSize = float32(24)

// renderEffortResetButton 渲染标题右侧的圆形重置按钮：
// 24x24、静止透明，悬停时白 10% 圆底，图标为次要色。
func renderEffortResetButton(c *ui.Context, p EffortSliderCardProps) {
	btn := ui.ButtonBase(c.Key(p.KeyPrefix+"effort-reset-btn")).
		Label("重置强度与快速模式").
		Tooltip(effortLockedTip(p, "重置")).
		Disabled(p.Disabled).
		Size(effortResetButtonSize, effortResetButtonSize).
		Radius(RadiusPill).
		Center()

	scale := motionPressScale(c, btn, "button-press-scale", !p.Disabled && btn.Pressed())
	btn.Scale(scale, scale)
	if btn.Hovered() && !p.Disabled {
		btn.Background(effortOnDark.Alpha(0.10))
	}
	if p.OnReset != nil {
		btn.OnClick(p.OnReset)
	}

	btn.Children(func() {
		ui.Icon(c, iconRotateCcw).
			Size(13, 13).
			TextColor(ColorTextSecondaryDark)
	})
}

// pairFavProps 描述说明行左侧配对收藏按钮的状态与行为。
type pairFavProps struct {
	Matched  bool   // 当前主/子模型组合与某个已存配对完全一致
	Name     string // 已匹配配对的显示名，仅 Matched 时用于 Tooltip
	OnToggle func() // 点击切换：未匹配则按默认名保存，已匹配则删除该配对
}

// renderPairFavButton 渲染说明行左侧的配对收藏按钮：已存为配对时是主题色实心星，
// 未存时是次要色半透明线框星；点击在保存与取消之间切换。没有收藏状态时退回 Swarm 标记，
// 保证说明行左侧始终有图标。
func renderPairFavButton(c *ui.Context, p EffortSliderCardProps) {
	fav := p.PairFav
	if fav == nil || fav.OnToggle == nil {
		ui.Icon(c, IconSwarm()).Size(12, 12).TextColor(effortOnDark.Alpha(0.75))
		return
	}
	icon := IconStar()
	label := "存为配对"
	tip := "存为配对"
	color := ColorTextSecondaryDark.Alpha(0.65)
	if fav.Matched {
		icon = IconStarFilled()
		label = "已保存为配对"
		tip = "取消配对"
		color = ColorAccentDark
	}

	btn := ui.ButtonBase(c.Key(p.KeyPrefix+"effort-pair-fav-btn")).
		Label(label).
		Value(map[bool]string{true: "已收藏", false: "未收藏"}[fav.Matched]).
		Tooltip(tip).
		Checked(fav.Matched).
		Size(effortPairFavSize, effortPairFavSize).
		Radius(RadiusPill).
		Center()
	scale := motionPressScale(c, btn, "button-press-scale", true && btn.Pressed())
	btn.Scale(scale, scale)
	if btn.Hovered() {
		btn.Background(effortOnDark.Alpha(0.10))
	}
	btn.OnClick(fav.OnToggle)
	btn.Children(func() {
		ui.Icon(c, icon).Size(12, 12).TextColor(color)
	})
}

// renderEffortBrandIconSize 渲染指定尺寸的 Provider/Brand 图标：
// 若 brandID 非空且在 colorProviderIcons 中存在则优先使用，否则在 providerID 非空时回退 providerID。
func renderEffortBrandIconSize(c *ui.Context, brandID, providerID string, size float32) {
	brand := strings.TrimSpace(brandID)
	prov := strings.TrimSpace(providerID)
	if brand != "" && colorProviderIcons[brand] != nil {
		renderProviderIcon(c, brand, size)
	} else if prov != "" {
		renderProviderIcon(c, prov, size)
	} else if brand != "" {
		renderProviderIcon(c, brand, size)
	}
}

// renderEffortBrandIcon 渲染 12 号 Provider/Brand 图标。
func renderEffortBrandIcon(c *ui.Context, brandID, providerID string) {
	renderEffortBrandIconSize(c, brandID, providerID, 12)
}

// renderEffortSubtitleRow 渲染标题下方的副标题行：
// 配对模式只渲染配对收藏按钮、“+”、子代理 12 号图标与子代理模型名，不再重复主模型名；
// 非配对时仅在存在独立说明（如“跟随主代理”）或 SubtitleAccessory（如并发上限胶囊）时才渲染。
func renderEffortSubtitleRow(c *ui.Context, p EffortSliderCardProps) {
	hasPair := effortCardPaired(p) && strings.TrimSpace(p.SubtitlePair) != ""
	sub := strings.TrimSpace(p.Subtitle)
	hasNote := !hasPair && sub != "" && sub != effortCardTitle(p)
	hasAcc := p.SubtitleAccessory != nil
	if !hasPair && !hasNote && !hasAcc {
		return
	}

	ui.Row(c.Key(p.KeyPrefix + "effort-subtitle-row")).
		AlignItems(ui.Center).
		Justify(ui.Center).
		Gap(Space1).
		Shrink(1).
		MinWidth(0).
		Children(func() {
			if hasPair {
				renderPairFavButton(c, p)
				ui.Text(c, "+").
					FontSize(uiFont(TypeBody)).
					TextColor(effortTextSoft).
					Shrink(0)
				renderEffortBrandIcon(c, p.PairBrandID, p.PairProviderID)
				ui.Text(c, p.SubtitlePair).
					FontSize(uiFont(TypeBody)).
					TextColor(effortTextSoft).
					SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
				if hasAcc {
					p.SubtitleAccessory()
				}
				return
			}
			if hasNote {
				ui.Text(c, sub).
					FontSize(uiFont(TypeBody)).
					TextColor(effortTextSoft).
					SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
			}
			if hasAcc {
				p.SubtitleAccessory()
			}
		})
}

// iconFastBoltFilled 实心闪电（对齐 SF Symbols 的 bolt.fill）：沿用 Lucide zap 的轮廓路径，仅改为填充绘制。
var iconFastBoltFilled = parseLucideSVG(`<polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2" fill="currentColor" stroke="none"/>`)

// effortCardButtonSize 是卡片左右两枚圆形按钮（快速模式、Swarm 开关）的直径。
const effortCardButtonSize = float32(30)

// effortPairFavSize 是说明行配对收藏按钮的直径，比左右两枚圆形按钮小一号。
const effortPairFavSize = float32(15)

// effortFastModeDuration 是快速模式开关开/关切换的过渡时长，取全局展开 token，与模型选择弹层的短促过渡保持一致。
const effortFastModeDuration = motionUnfold

// effortFastModeIconKey 是闪电图标开关进度的动画通道名（同元素上的其他动画需另用键名）。
const effortFastModeIconKey = "fast-mode-icon"

// renderFastModeButton 渲染快速模式开关：30x30 圆形，
// 关闭时白 6%（悬停 10%）底、次要色闪电轮廓；开启时系统黄 18% 底、实心黄色闪电。
func renderFastModeButton(c *ui.Context, p EffortSliderCardProps) {
	if !p.SupportsFast {
		return
	}
	state := "关闭"
	if p.FastMode {
		state = "开启"
	}
	btn := ui.ButtonBase(c.Key(p.KeyPrefix+"fast-mode-btn")).
		Label("快速模式").
		Value(state).
		Tooltip(effortLockedTip(p, "快速模式")).
		Disabled(p.Disabled).
		Checked(p.FastMode).
		Size(effortCardButtonSize, effortCardButtonSize).
		Radius(effortCardButtonSize / 2).
		Center().
		// Colors 只插值背景与描边色：开/关切换时底色在系统黄与白色微光之间渐变，悬停的加深也一并变柔和。
		Transition(motionFeedback(c))

	scale := motionPressScale(c, btn, "button-press-scale", !p.Disabled && btn.Pressed())
	btn.Scale(scale, scale)
	switch {
	case p.FastMode:
		btn.Background(effortBoltYellow.Alpha(0.18))
	case btn.Hovered() && !p.Disabled:
		btn.Background(effortOnDark.Alpha(0.10))
	default:
		btn.Background(effortOnDark.Alpha(0.06))
	}
	if p.OnToggleFast != nil {
		btn.OnClick(p.OnToggleFast)
	}

	btn.Children(func() {
		// 两态的闪电画法不同（Lucide 轮廓 vs 实心多边形），换图像本身无法插值，
		// 因此只把图标颜色按开关进度从次要色插到系统黄，避免黄色在切换帧直接跳出。
		iconOn := float32(0)
		icon := IconByName("zap")
		if p.FastMode {
			iconOn = 1
			icon = iconFastBoltFilled
		}
		on := btn.AnimateTo(effortFastModeIconKey, iconOn, ui.Animation{Duration: animateDuration(c, effortFastModeDuration), Ease: ui.EaseOut})
		ui.Icon(c, icon).Size(14, 14).TextColor(ColorTextSecondaryDark.Mix(effortBoltYellow, on))
	})
}

// swarmRosterColors 是 Swarm 名册前六位的代表色（Aria、Finn、Cora、Eva、Bram、Dean），
// 按色环顺滑排列使编排开关角向渐变环自然闭合；色值统一由 theme.go 的 identityPalette 提供。
var swarmRosterColors = []ui.Color{
	ThemeIdentity("aria"), ThemeIdentity("finn"), ThemeIdentity("cora"),
	ThemeIdentity("eva"), ThemeIdentity("bram"), ThemeIdentity("dean"),
}

// renderProviderRainbowAvatar 渲染卡片右侧的 Swarm 开关：
// 30x30，中间是 16 号 Swarm 标记；开启时外圈 2px 名册色角向渐变环、内衬 accent 微光，
// 标记为主文字色；关闭时只是白 6%（悬停 10%）圆底，标记 60% 不透明。
func renderProviderRainbowAvatar(c *ui.Context, p EffortSliderCardProps) {
	interactive := p.OnToggleSwarm != nil && !p.Disabled
	btn := ui.ButtonBase(c.Key(p.KeyPrefix+"avatar-btn")).
		Label("编排开关").
		Value(map[bool]string{true: "开启", false: "关闭"}[p.SwarmOn]).
		Tooltip(effortLockedTip(p, "编排")).
		Disabled(p.Disabled).
		Checked(p.SwarmOn).
		Size(effortCardButtonSize, effortCardButtonSize).
		Radius(effortCardButtonSize / 2).
		Center()
	scale := motionPressScale(c, btn, "button-press-scale", interactive && btn.Pressed())
	btn.Scale(scale, scale)
	if interactive {
		btn.OnClick(p.OnToggleSwarm)
	}
	hovered := btn.Hovered() && interactive

	btn.Draw(func(painter *ui.Painter, r ui.Rect) {
		if !p.SwarmOn {
			bg := effortOnDark.Alpha(0.06)
			if hovered {
				bg = effortOnDark.Alpha(0.10)
			}
			painter.Fill(r, bg, r.W/2)
			return
		}
		stops := make([]ui.GradientStop, 0, len(swarmRosterColors)+1)
		for i, col := range swarmRosterColors {
			stops = append(stops, ui.GradientStop{Color: col, At: float32(i) / float32(len(swarmRosterColors))})
		}
		stops = append(stops, ui.GradientStop{Color: swarmRosterColors[0], At: 1})
		painter.FillConicGradient(r, ui.ConicGradient{Stops: stops}, r.W/2)
		// 镂空环内侧：先用面板底色盖住，再叠一层 accent 微光。
		inner := ui.Rect{X: r.X + 2, Y: r.Y + 2, W: r.W - 4, H: r.H - 4}
		painter.Fill(inner, modelPickerPanelBg, inner.W/2)
		painter.Fill(inner, ColorAccentDark.Alpha(0.16), inner.W/2)
	})

	btn.Children(func() {
		alpha := float32(0.6)
		if p.SwarmOn || hovered {
			alpha = 1
		}
		ui.Icon(c, IconSwarm()).Size(16, 16).TextColor(effortOnDark.Alpha(alpha))
	})
}

// effortTrackEffect 归类轨道填充效果：
// 配对 "pair" 优先；否则最高档+快速 "fusion"、快速 "fast"、最高档 "supercharged"、普通 "plain"。
func effortTrackEffect(isMax, isFast, isPair bool) string {
	switch {
	case isPair:
		return "pair"
	case isMax && isFast:
		return "fusion"
	case isMax:
		return "supercharged"
	case isFast:
		return "fast"
	default:
		return "plain"
	}
}

// effortIsMax 判断当前档位是否停在最高档（最后一档，且需多于一个档位）。
func effortIsMax(p EffortSliderCardProps) bool {
	return len(p.Efforts) > 1 && effortIndex(p.Efforts, p.Current, p.DefaultEffort) == len(p.Efforts)-1
}

// effortCardBrand 返回卡片取色用的品牌键：调用方给了 BrandID 就用它，否则回退 ProviderID。
func effortCardBrand(p EffortSliderCardProps) string {
	if brand := strings.TrimSpace(p.BrandID); brand != "" {
		return brand
	}
	return p.ProviderID
}

// effortCardPairBrand 返回执行侧取色用的品牌键：为空时回退 PairProviderID。
func effortCardPairBrand(p EffortSliderCardProps) string {
	if brand := strings.TrimSpace(p.PairBrandID); brand != "" {
		return brand
	}
	return p.PairProviderID
}

// effortPairBeesProvider 返回配对时执行侧（Bees）的品牌；
// 未配置（Bees 跟随主导）时回退主导品牌，使融合渐变退化为品牌色实色。
func effortPairBeesProvider(p EffortSliderCardProps) string {
	if bees := strings.TrimSpace(effortCardPairBrand(p)); bees != "" {
		return bees
	}
	return effortCardBrand(p)
}

// effortTitleColor 返回档位标题的主色：
// 普通档为 accent 提亮 22%，最高档为品牌色，快速为金色（最高档 + 快速时品牌色渐入金色由调用方处理），
// 配对的近端为 Lead 品牌色。滑块暖光取该色。
func effortTitleColor(p EffortSliderCardProps) ui.Color {
	switch effortTrackEffect(effortIsMax(p), p.SupportsFast && p.FastMode, p.Paired) {
	case "pair":
		return effortBrandColor(effortCardBrand(p))
	case "fusion", "supercharged":
		return effortBrandColor(effortCardBrand(p))
	case "fast":
		return effortFastColor()
	default:
		return effortAccentBright
	}
}

// effortCardPaired 报告卡片是否按配对呈现：只有主导侧卡片承载配对
// （Swarm 标记说明行、Lead→Bees 融合的标题与滑轨），执行侧始终是单模型卡片。
func effortCardPaired(p EffortSliderCardProps) bool {
	return p.Paired && !p.IsBees
}

// effortBrandKind 是最高档滑轨与标题使用的 Provider 品牌配色。
type effortBrandKind int

const (
	effortBrandPurple effortBrandKind = iota // 无 Provider 时的默认紫
	effortBrandClaude
	effortBrandDeepseek
	effortBrandMeta
	effortBrandAntigravity
	effortBrandSilver // 黑白标识的品牌：白色填充、深色刻度
)

// effortBrandOf 把 provider 归入品牌配色；未单列的 Provider 都是黑白标识，归为 silver。
func effortBrandOf(provider string) effortBrandKind {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "":
		return effortBrandPurple
	case "claude":
		return effortBrandClaude
	case "deepseek":
		return effortBrandDeepseek
	case "meta":
		return effortBrandMeta
	case "antigravity", "agy", "gemini":
		return effortBrandAntigravity
	default:
		return effortBrandSilver
	}
}

// titleColor 是最高档时档位名的颜色，也是快速融合的近端色。
func (b effortBrandKind) titleColor() ui.Color {
	switch b {
	case effortBrandClaude:
		return effortClaudeTitle
	case effortBrandDeepseek:
		return effortDeepseekTitle
	case effortBrandMeta:
		return effortMetaTitle
	case effortBrandAntigravity:
		return effortGeminiColors[1]
	case effortBrandSilver:
		return effortSilverTitle
	default:
		return effortPurpleTitle
	}
}

// fillColors 是最高档滑轨填充（自左向右）；单色时只有一项。
func (b effortBrandKind) fillColors() []ui.Color {
	switch b {
	case effortBrandClaude:
		return []ui.Color{effortClaudeFill}
	case effortBrandDeepseek:
		return []ui.Color{effortDeepseekFill}
	case effortBrandMeta:
		return []ui.Color{effortMetaFill, effortMetaFillLight}
	case effortBrandAntigravity:
		return effortGeminiColors
	case effortBrandSilver:
		return []ui.Color{effortOnDark, effortSilverFill}
	default:
		return []ui.Color{effortPurpleFill}
	}
}

// fillColor 是填充里最深的一色：快速融合的近端与滑块光晕取它。
func (b effortBrandKind) fillColor() ui.Color {
	switch b {
	case effortBrandMeta:
		return effortMetaFillDeep
	case effortBrandAntigravity:
		return effortGeminiFill
	case effortBrandSilver:
		// silver 的填充是白→灰，取标题白作为最深色。
		return effortSilverTitle
	default:
		return b.fillColors()[0]
	}
}

// effortPairColors 返回配对配色：Bees 在别的 Provider 上时取其品牌色
// （Gemini 扫色、silver 的浅灰、其余为填充色→标题色），否则取名册前四色；light 表示远端为浅色。
func effortPairColors(p EffortSliderCardProps) (colors []ui.Color, light bool) {
	bees := strings.TrimSpace(effortCardPairBrand(p))
	if bees == "" || strings.EqualFold(bees, effortCardBrand(p)) {
		return swarmRosterColors[:4], false
	}
	switch b := effortBrandOf(bees); b {
	case effortBrandAntigravity:
		return effortGeminiColors, false
	case effortBrandSilver:
		return []ui.Color{effortSilverPair, effortSilverPairHi}, true
	default:
		return []ui.Color{b.fillColor(), b.titleColor()}, false
	}
}

// effortTitleGradient 返回档位名自左向右的颜色：
// 配对为 Lead 品牌标题色接 Bees 色（提亮 18%），快速时尾端加金；否则最高档 + 快速为品牌→金，
// 最高档为品牌标题色（Antigravity 为 Gemini 扫色），快速为金，普通档为 accent 提亮 22%。
func effortTitleGradient(p EffortSliderCardProps) []ui.Color {
	brand := effortBrandOf(effortCardBrand(p))
	isFast := p.SupportsFast && p.FastMode
	if effortCardPaired(p) {
		bees, _ := effortPairColors(p)
		colors := []ui.Color{brand.titleColor()}
		for _, col := range bees {
			colors = append(colors, col.Mix(effortOnDark, 0.18))
		}
		if isFast {
			colors = append(colors, effortFastTitle)
		}
		return colors
	}
	switch isMax := effortIsMax(p); {
	case isMax && isFast:
		return []ui.Color{brand.titleColor(), effortFastTitle}
	case isMax && brand == effortBrandAntigravity:
		return effortGeminiColors
	case isMax:
		return []ui.Color{brand.titleColor()}
	case isFast:
		return []ui.Color{effortFastTitle}
	default:
		return []ui.Color{effortAccentBright}
	}
}

// effortTrackLook 返回滑轨已选部分的填充与刻度色。
type effortTrackLook struct {
	fill  []ui.Color
	light bool // 滑块下的填充为浅色：刻度与滑块描边改用深色
}

func effortTrackLookFor(p EffortSliderCardProps) effortTrackLook {
	brand := effortBrandOf(effortCardBrand(p))
	isFast := p.SupportsFast && p.FastMode
	if effortCardPaired(p) {
		bees, light := effortPairColors(p)
		return effortTrackLook{fill: append([]ui.Color{brand.fillColor()}, bees...), light: light}
	}
	switch effortTrackEffect(effortIsMax(p), isFast, false) {
	case "supercharged":
		return effortTrackLook{fill: brand.fillColors(), light: brand == effortBrandSilver}
	case "fusion":
		return effortTrackLook{fill: []ui.Color{brand.fillColor(), brand.fillColor(), effortFastFill}}
	case "fast":
		return effortTrackLook{fill: []ui.Color{effortFastFill}}
	default:
		return effortTrackLook{fill: []ui.Color{ColorAccentDark}}
	}
}

// effortGradientStops 把等距颜色列表转成线性渐变色标。
func effortGradientStops(colors []ui.Color) []ui.GradientStop {
	if len(colors) == 1 {
		return []ui.GradientStop{{Color: colors[0], At: 0}, {Color: colors[0], At: 1}}
	}
	stops := make([]ui.GradientStop, len(colors))
	for i, col := range colors {
		stops[i] = ui.GradientStop{Color: col, At: float32(i) / float32(len(colors)-1)}
	}
	return stops
}

// effortSampleColors 在等距颜色列表上按 t∈[0,1] 取插值色。
func effortSampleColors(colors []ui.Color, t float32) ui.Color {
	if len(colors) == 1 {
		return colors[0]
	}
	pos := min(max(t, 0), 1) * float32(len(colors)-1)
	i := min(int(pos), len(colors)-2)
	return colors[i].Mix(colors[i+1], pos-float32(i))
}

const (
	effortTrackH   = float32(34) // 轨道高度，全圆角
	effortThumbD   = float32(42) // 滑轨整体高度，留足点击热区与按下微缩放空间
	effortThumbW   = float32(64) // 固定毛玻璃胶囊手柄宽度，避免切换档位时刻度点左右抖动
	effortThumbH   = float32(36) // 毛玻璃胶囊手柄高度
	effortDotD     = float32(5)  // 档位刻度圆点直径
	effortTitleSz  = float32(16) // 标题模型名字号
	effortTitleWgt = 500         // 标题模型名字重（.medium）
)

// renderEffortSlider 渲染推理强度滑轨：42 高的区域里居中一条 34 高的
// 胶囊轨道，已选部分按 TrackLook 填色并一直填到胶囊右缘，每档一个 5px 圆点，上覆 64x36 的毛玻璃胶囊手柄。
// 首尾档位的圆点内缩半个胶囊宽度（32px），使胶囊在两端也严丝合缝落在轨道内。
func renderEffortSlider(c *ui.Context, p EffortSliderCardProps) {
	n := len(p.Efforts)
	if n == 0 {
		return
	}
	curIdx := effortIndex(p.Efforts, p.Current, p.DefaultEffort)
	look := effortTrackLookFor(p)
	thumbText := effortThumbLabel(p.Efforts[curIdx])
	const inset = effortThumbW / 2

	value := float64(curIdx)
	slider := ui.SliderBase(c.Key(p.KeyPrefix+"effort-slider"), &value, 0, float64(n-1)).
		Step(1).
		Label("推理强度").
		Value(thumbText).
		Disabled(p.Disabled).
		Height(effortThumbD).
		PaddingX(inset).
		AlignSelf(ui.Stretch)

	if slider.Changed() && p.OnEffort != nil {
		idx := effortSliderSnapIndex(float32(value), float32(n-1), n)
		p.OnEffort(p.Efforts[idx])
	}

	var tickLabels []string
	if len(p.Roster) == 0 {
		tickLabels = make([]string, n)
		for i, eff := range p.Efforts {
			tickLabels[i] = effortThumbLabel(eff)
		}
	}
	pressed := slider.Pressed()
	reduced := reduceMotion(c)
	slider.Draw(func(painter *ui.Painter, r ui.Rect) {
		drawEffortTrack(painter, r, n, curIdx, look, pressed, reduced, thumbText, tickLabels)
	})
}

// drawEffortTrack 绘制滑轨：白 10% 轨道、TrackLook 填充、刻度点或短档位名（已越过的用 TrackLook 刻度色，
// 其余白 32% 或至少 60% 不透明度的短名），最后绘制带当前英文短档位名的毛玻璃胶囊手柄，保证在任意轨道填充色上文字对比度清晰。
func drawEffortTrack(painter *ui.Painter, r ui.Rect, n, curIdx int, look effortTrackLook, pressed, reduced bool, thumbText string, tickLabels []string) {
	const inset = effortThumbW / 2

	slideW := max(float32(0), r.W-effortThumbW)
	frac := float32(0)
	if n > 1 {
		frac = float32(curIdx) / float32(n-1)
	}
	curX := r.X + inset + slideW*frac
	midY := r.Y + r.H/2
	trackRect := ui.Rect{X: r.X, Y: midY - effortTrackH/2, W: r.W, H: effortTrackH}

	painter.Fill(trackRect, effortOnDark.Alpha(0.10), effortTrackH/2)

	fillRect := ui.Rect{X: r.X, Y: trackRect.Y, W: curX - r.X + inset, H: effortTrackH}
	painter.FillGradient(fillRect, ui.LinearGradient{Angle: 90, Stops: effortGradientStops(look.fill)}, effortTrackH/2)

	showLabels := len(tickLabels) == n && n > 1 && slideW/float32(n-1) >= 36
	passed := ColorTextSecondaryDark
	if look.light {
		passed = effortTickOnLight
	}
	for i, stop := range effortTickPositions(n, slideW) {
		stopX := r.X + inset + stop
		if showLabels {
			if i == curIdx {
				continue
			}
			if label := strings.TrimSpace(tickLabels[i]); label != "" {
				col := effortOnDark.Alpha(0.60)
				if stopX <= curX+0.5 {
					col = passed
				}
				layout := ui.ShapeRichText(
					[]ui.Span{{Text: label, Color: col}},
					ui.Font{Size: 11, Weight: 500},
					0,
				)
				tw, th := layout.Size()
				painter.TextLayout(layout, stopX-tw/2, midY-th/2, col)
				continue
			}
		}
		dot := effortOnDark.Alpha(0.32)
		if stopX <= curX+0.5 {
			dot = passed
		}
		painter.Fill(ui.Rect{X: stopX - effortDotD/2, Y: midY - effortDotD/2, W: effortDotD, H: effortDotD}, dot, effortDotD/2)
	}

	w, h := effortThumbW, effortThumbH
	if pressed && !reduced {
		w *= 1.04
		h *= 1.04
	}
	knob := ui.Rect{X: curX - w/2, Y: midY - h/2, W: w, H: h}
	radius := h / 2

	if look.light {
		painter.Fill(knob, effortOnDark.Alpha(0.86), radius)
		painter.FillGradient(knob, ui.LinearGradient{
			Angle: 180,
			Stops: []ui.GradientStop{
				{Color: effortOnDark.Alpha(0.96), At: 0},
				{Color: effortOnDark.Alpha(0.74), At: 1},
			},
		}, radius)
		painter.Stroke(knob, effortKnobOnLight, radius, 1)
	} else {
		// 深色/彩色填充轨道用半透明深雾毛玻璃底叠柔白高光，确保纯白短档位文字对比度充足。
		painter.Fill(knob, ColorSurfaceMainDark.Alpha(0.78), radius)
		painter.FillGradient(knob, ui.LinearGradient{
			Angle: 180,
			Stops: []ui.GradientStop{
				{Color: effortOnDark.Alpha(0.22), At: 0},
				{Color: effortOnDark.Alpha(0.06), At: 1},
			},
		}, radius)
		painter.Stroke(knob, ColorTextTertiaryDark, radius, 1)
	}

	if thumbText != "" {
		textColor := effortOnDark
		if look.light {
			textColor = ColorTextPrimaryLight
		}
		layout := ui.ShapeRichText(
			[]ui.Span{{Text: thumbText, Color: textColor}},
			ui.Font{Size: 12, Weight: 600},
			0,
		)
		tw, th := layout.Size()
		painter.TextLayout(layout, knob.X+(knob.W-tw)/2, knob.Y+(knob.H-th)/2, textColor)
	}
}

// effortCardTitle 返回卡片大标题：优先返回 ModelTitle，为空时回退 Subtitle，两者均为空时回退“标准”。
func effortCardTitle(p EffortSliderCardProps) string {
	if title := strings.TrimSpace(p.ModelTitle); title != "" {
		return title
	}
	if sub := strings.TrimSpace(p.Subtitle); sub != "" {
		return sub
	}
	return "标准"
}

// effortTitleColors 返回标题文字水平渐变起始与终止颜色：
// 普通档为 accent 提亮色（即 effortTitleColor）；快速模式金色；最高档 Provider 品牌色；
// 最高档 + 快速为品牌色 → 金色；配对时为 Lead 品牌色 → Bees 品牌色融合，
// 配对 + 快速时尾端收在金色（两色返回无法再容纳 Bees 色，快速时以金色收尾）。
func effortTitleColors(c *ui.Context, p EffortSliderCardProps) (ui.Color, ui.Color) {
	isFast := p.SupportsFast && p.FastMode
	switch effortTrackEffect(effortIsMax(p), isFast, p.Paired) {
	case "pair":
		from := effortBrandColor(effortCardBrand(p))
		to := effortBrandColor(effortPairBeesProvider(p))
		if isFast {
			to = effortFastColor()
		}
		return from, to
	case "fusion":
		return effortBrandColor(effortCardBrand(p)), effortFastColor()
	case "fast":
		gold := effortFastColor()
		return gold, gold
	case "supercharged":
		brand := effortBrandColor(effortCardBrand(p))
		return brand, brand
	default:
		return effortTitleColor(p), effortTitleColor(p)
	}
}

// fitEffortGradientText 将多色渐变标题裁剪到 maxW 以内（超出时末尾加省略号），
// 保持与 SingleLine().Ellipsis("…") 一致的截断行为。
func fitEffortGradientText(s string, font ui.Font, maxW float32) string {
	if maxW <= 0 {
		return s
	}
	w, _ := ui.ShapeText(s, font, 0).Size()
	if w <= maxW+0.5 {
		return s
	}
	runes := []rune(s)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		cand := string(runes[:mid]) + "…"
		cw, _ := ui.ShapeText(cand, font, 0).Size()
		if cw <= maxW+0.5 {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo <= 0 {
		return "…"
	}
	return string(runes[:lo]) + "…"
}

// renderEffortTitle 渲染模型名标题（16 号、medium）：普通态用高对比主文字色，
// 配对/快速/最高档时用柔和提亮的品牌或金色渐变，保证在深色面板上对比度 ≥ 4.5:1。
func renderEffortTitle(c *ui.Context, p EffortSliderCardProps) {
	renderEffortTitleWithStyle(c, p, effortCardTitle(p), effortTitleSz, effortTitleWgt)
}

// renderEffortTitleWithStyle 按指定字号与字重渲染渐变或高对比模型名。
func renderEffortTitleWithStyle(c *ui.Context, p EffortSliderCardProps, title string, fontSize float32, fontWeight int) {
	if strings.TrimSpace(title) == "" {
		title = "标准"
	}
	colors := effortTitleGradient(p)
	if effortTrackEffect(effortIsMax(p), p.SupportsFast && p.FastMode, effortCardPaired(p)) == "plain" {
		colors = []ui.Color{ColorTextPrimaryDark}
	}
	text := ui.Text(c, title).
		FontSize(fontSize).
		FontWeight(fontWeight).
		SingleLine().
		Ellipsis("…").
		Shrink(1).
		MinWidth(0)
	if len(colors) == 1 {
		col := colors[0]
		if col != ColorTextPrimaryDark {
			col = col.Mix(effortOnDark, 0.20)
		}
		text.TextColor(col)
		return
	}
	soft := make([]ui.Color, len(colors))
	for i, col := range colors {
		soft[i] = col.Mix(effortOnDark, 0.20)
	}
	text.TextColor(ui.Transparent).Draw(func(painter *ui.Painter, r ui.Rect) {
		font := ui.Font{Size: fontSize, Weight: fontWeight}
		display := fitEffortGradientText(title, font, r.W)
		// MyGo 无原生渐变文本 API：逐字符着色成富文本再整段绘制。
		runes := []rune(display)
		spans := make([]ui.Span, len(runes))
		for i, ch := range runes {
			t := float32(0)
			if len(runes) > 1 {
				t = float32(i) / float32(len(runes)-1)
			}
			spans[i] = ui.Span{Text: string(ch), Color: effortSampleColors(soft, t)}
		}
		layout := ui.ShapeRichText(spans, font, 0)
		_, th := layout.Size()
		painter.TextLayout(layout, r.X, r.Y+(r.H-th)/2, soft[0])
	})
}

// effortTitleCaption 返回标题按钮的悬停提示：不可进入模型列表时为空串。
func effortTitleCaption(p EffortSliderCardProps) string {
	if p.OnOpenModelList == nil {
		return ""
	}
	if p.Disabled {
		return effortLockedTip(p, "选择模型")
	}
	return "选择模型"
}

// renderEffortTitleRow 渲染模型名标题行：14px 品牌图标 + 模型名 + 次要色“›”组成的标题按钮
// （点击进入模型列表），OnReset 非空时右侧紧跟 24x24 重置按钮。
func renderEffortTitleRow(c *ui.Context, p EffortSliderCardProps) {
	ui.Row(c.Key(p.KeyPrefix + "title-row")).
		AlignItems(ui.Center).
		Justify(ui.Center).
		Gap(Space1).
		Shrink(1).
		MinWidth(0).
		Children(func() {
			canOpen := p.OnOpenModelList != nil
			titleBtn := ui.ButtonBase(c.Key(p.KeyPrefix+"title-btn")).
				Label("选择其他模型").
				Tooltip(effortTitleCaption(p)).
				Disabled(p.Disabled || !canOpen).
				Padding(Space0, Space2).
				Radius(RadiusSM).
				Shrink(1).
				MinWidth(0)
			scale := motionPressScale(c, titleBtn, "button-press-scale", !p.Disabled && canOpen && titleBtn.Pressed())
			titleBtn.Scale(scale, scale)
			if titleBtn.Hovered() && canOpen && !p.Disabled {
				titleBtn.Background(ColorHoverDark)
			}
			if canOpen && !p.Disabled {
				titleBtn.OnClick(p.OnOpenModelList)
			}

			titleBtn.Children(func() {
				ui.Row(c.Key(p.KeyPrefix + "title-content")).
					AlignItems(ui.Center).
					Gap(Space1).
					Shrink(1).
					MinWidth(0).
					Children(func() {
						if strings.TrimSpace(p.BrandID) != "" || strings.TrimSpace(p.ProviderID) != "" {
							renderEffortBrandIconSize(c, p.BrandID, p.ProviderID, 14)
						}
						renderEffortTitle(c, p)
						if canOpen {
							ui.Icon(c, IconChevronRight()).
								Size(12, 12).
								TextColor(ColorTextSecondaryDark)
						}
					})
			})

			if p.OnReset != nil {
				renderEffortResetButton(c, p)
			}
		})
}

// renderEffortSliderCard 渲染推理强度卡片：
// 当 Roster 非空时按方案 B 渲染（顶部工具栏 + 双槽同屏配对卡 + 底部滑轨）；
// 否则保留单卡布局以兼容设置页 Inline 模式与独立单测。
func renderEffortSliderCard(c *ui.Context, p EffortSliderCardProps) {
	if len(p.Roster) > 0 {
		renderEffortRosterCard(c, p)
		return
	}
	ui.Column(c.Key(p.KeyPrefix+"effort-slider-card")).
		Gap(Space3).
		Padding(Space3, Space4, Space4, Space4).
		Children(func() {
			ui.Row(c.Key(p.KeyPrefix + "card-head-row")).
				AlignItems(ui.Center).
				Gap(Space4).
				AlignSelf(ui.Stretch).
				Children(func() {
					if p.SupportsFast {
						renderFastModeButton(c, p)
					} else {
						ui.Box(c.Key(p.KeyPrefix+"fast-spacer")).Size(effortCardButtonSize, effortCardButtonSize)
					}

					ui.Column(c.Key(p.KeyPrefix + "card-head-center")).
						AlignItems(ui.Center).
						Grow(1).
						MinWidth(0).
						Gap(Space0).
						Children(func() {
							renderEffortTitleRow(c, p)
							renderEffortSubtitleRow(c, p)
						})

					switch {
					case p.TrailingAccessory != nil:
						p.TrailingAccessory()
					case p.OnToggleSwarm != nil:
						renderProviderRainbowAvatar(c, p)
					default:
						ui.Box(c.Key(p.KeyPrefix+"swarm-spacer")).Size(effortCardButtonSize, effortCardButtonSize)
					}
				})

			renderEffortSliderSection(c, p)
		})
}

const (
	effortToolbarBtnSize  = float32(26)
	effortToolbarIconSize = float32(13)
	effortSwarmBadgeSize  = float32(20)
	effortSwarmRingWidth  = float32(1.5)
	effortSwarmIconSize   = float32(11)
	effortRosterRowMinH   = float32(40)
	effortRosterBarW      = float32(3)
	effortRosterBarInsetY = float32(8)
	effortRosterBarRadius = float32(1.5)
)

func renderEffortSliderSection(c *ui.Context, p EffortSliderCardProps) {
	if len(p.Efforts) <= 1 {
		return
	}
	renderEffortSlider(c, p)
}

func renderEffortRosterCard(c *ui.Context, p EffortSliderCardProps) {
	ui.Column(c.Key(p.KeyPrefix+"effort-slider-card")).
		Padding(Space3, Space4, Space4, Space4).
		Gap(Space3).
		Children(func() {
			hasPersona := p.OnPersona != nil && len(p.Personas) > 0
			if p.OnToggleSwarm != nil || p.PairFav != nil || p.SupportsFast || p.OnReset != nil || p.TrailingAccessory != nil || hasPersona {
				renderEffortRosterToolbar(c, p)
			}
			renderEffortRosterSlots(c, p)
			renderEffortSliderSection(c, p)
		})
}

func renderEffortRosterToolbar(c *ui.Context, p EffortSliderCardProps) {
	ui.Row(c.Key(p.KeyPrefix + "card-toolbar")).
		MinHeight(ControlMD).
		AlignItems(ui.Center).
		Justify(ui.SpaceBetween).
		AlignSelf(ui.Stretch).
		Children(func() {
			switch {
			case p.OnToggleSwarm != nil:
				renderEffortSwarmCapsule(c, p)
			case p.TrailingAccessory != nil:
				p.TrailingAccessory()
			default:
				ui.Box(c.Key(p.KeyPrefix + "toolbar-spacer"))
			}

			ui.Row(c.Key(p.KeyPrefix + "toolbar-actions")).
				AlignItems(ui.Center).
				Gap(Space2).
				Children(func() {
					if p.OnPersona != nil && len(p.Personas) > 0 {
						renderToolbarPersonaButton(c, p)
					}
					if p.PairFav != nil {
						renderToolbarPairFavButton(c, p)
					}
					if p.SupportsFast {
						renderToolbarFastModeButton(c, p)
					}
					if p.OnReset != nil {
						renderToolbarResetButton(c, p)
					}
				})
		})
}

func personaDisplayTitle(current string, personas []MagpiePersona) string {
	if current == "" || current == personaNoneValue {
		return "无人设"
	}
	for _, item := range personas {
		if item.ID == current {
			if item.Name != "" {
				return item.Name
			}
			return item.ID
		}
	}
	return "无人设"
}

func renderToolbarPersonaButton(c *ui.Context, p EffortSliderCardProps) {
	personaActive := strings.TrimSpace(p.Persona) != "" && p.Persona != personaNoneValue && p.Persona != "无人设"
	displayTitle := personaDisplayTitle(p.Persona, p.Personas)
	btn := ui.ButtonBase(c.Key(p.KeyPrefix+"persona-btn")).
		Label("人设").
		Value(displayTitle).
		Tooltip(effortLockedTip(p, "人设："+displayTitle)).
		Disabled(p.Disabled).
		Checked(personaActive).
		Size(effortToolbarBtnSize, effortToolbarBtnSize).
		Radius(RadiusMD).
		Center()

	personaOpen := p.PersonaOpen
	if personaOpen == nil {
		personaOpen = ui.Local(btn, "persona-menu-open", func() bool { return false })
	}

	scale := motionPressScale(c, btn, "button-press-scale", !p.Disabled && btn.Pressed())
	btn.Scale(scale, scale)
	hovered := btn.Hovered() && !p.Disabled
	iconColor := ColorTextSecondaryDark
	if personaActive {
		bg := ColorAccentDark.Alpha(0.18)
		if hovered || *personaOpen {
			bg = ColorAccentDark.Alpha(0.26)
		}
		btn.Background(bg).Border(1, ColorAccentDark.Alpha(0.36))
		iconColor = effortAccentBright
	} else {
		bg := effortOnDark.Alpha(0.05)
		if hovered || *personaOpen {
			bg = effortOnDark.Alpha(0.10)
		}
		btn.Background(bg).Border(1, effortOnDark.Alpha(0.06))
	}
	if !p.Disabled {
		btn.OnClick(func() {
			togglePopoverOpen(personaOpen)
			if p.Invalidate != nil {
				p.Invalidate()
			}
		})
	}
	btn.Children(func() {
		ui.Icon(c, IconUserRoundCheck()).Size(effortToolbarIconSize, effortToolbarIconSize).TextColor(iconColor)
	})

	appFlyoutPopover(c, p.KeyPrefix+"persona-popover", btn, personaOpen, func() {
		*personaOpen = false
	}, appFlyoutProps{Width: 156, Background: &ColorSurfacePopoverDark}, func(fc *ui.Context) {
		c := fc
		optionsCol := ui.Column(c.Key(p.KeyPrefix + "persona-options")).
			Padding(Space1).
			Gap(Space0).
			FocusGroup(ui.Vertical)
		if flyoutHostWindow(c.Services()) == nil {
			optionsCol = applyElevation(c, optionsCol.Width(156).Radius(RadiusMD).Background(ColorSurfacePopoverDark).Border(1, ColorBorderDark), ElevPopover)
		}
		optionsCol.Children(func() {
			renderEffortPersonaOption(c, p, personaOpen, personaNoneValue, "无人设")
			for _, item := range p.Personas {
				if item.ID == "" || item.ID == personaNoneValue {
					continue
				}
				name := item.Name
				if name == "" {
					name = item.ID
				}
				renderEffortPersonaOption(c, p, personaOpen, item.ID, name)
			}
		})
	})
}

func renderEffortPersonaOption(c *ui.Context, p EffortSliderCardProps, personaOpen *bool, id, title string) {
	checked := personaMenuChecked(p.Persona, id, p.Personas)
	btn := ui.ButtonBase(c.Key(p.KeyPrefix+"persona-opt-"+id)).
		Label(title).
		Checked(checked).
		Height(RowHeightCompact).
		Padding(0, Space2).
		Radius(RadiusXS).
		FillWidth().
		Justify(ui.Start)

	switch {
	case btn.Hovered():
		btn.Background(ColorHoverDark)
	case checked:
		btn.Background(effortOnDark.Alpha(0.05))
	default:
		btn.Background(ui.Transparent)
	}
	btn.OnClick(func() {
		*personaOpen = false
		if p.OnPersona != nil {
			p.OnPersona(id)
		}
		if p.Invalidate != nil {
			p.Invalidate()
		}
	})
	btn.Children(func() {
		ui.Row(c.Key(p.KeyPrefix + "persona-opt-row-" + id)).
			AlignItems(ui.Center).
			Gap(Space2).
			Grow(1).
			Children(func() {
				ui.Text(c, title).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ColorTextPrimaryDark).
					Grow(1).
					Shrink(1).
					MinWidth(0).
					SingleLine().
					Ellipsis("…")
				if checked {
					ui.Icon(c, IconCheck()).Size(12, 12).TextColor(effortAccentBright)
				}
			})
	})
}

func renderEffortSwarmCapsule(c *ui.Context, p EffortSliderCardProps) {
	interactive := p.OnToggleSwarm != nil && !p.Disabled
	tip := "编排"
	btn := ui.ButtonBase(c.Key(p.KeyPrefix+"avatar-btn")).
		Label("编排开关").
		Value(map[bool]string{true: "开启", false: "关闭"}[p.SwarmOn]).
		Tooltip(effortLockedTip(p, tip)).
		Disabled(p.Disabled).
		Checked(p.SwarmOn).
		Height(effortToolbarBtnSize).
		Radius(RadiusXL).
		Padding(0, Space3, 0, Space1).
		Center()

	scale := motionPressScale(c, btn, "button-press-scale", interactive && btn.Pressed())
	btn.Scale(scale, scale)
	hovered := btn.Hovered() && interactive
	if p.SwarmOn {
		bg := effortOnDark.Alpha(0.06)
		if hovered {
			bg = effortOnDark.Alpha(0.11)
		}
		btn.Background(bg).Border(1, effortOnDark.Alpha(0.10))
	} else {
		bg := effortOnDark.Alpha(0.04)
		if hovered {
			bg = effortOnDark.Alpha(0.09)
		}
		btn.Background(bg).Border(1, effortOnDark.Alpha(0.07))
	}
	if interactive {
		btn.OnClick(p.OnToggleSwarm)
	}

	btn.Children(func() {
		ui.Row(c.Key(p.KeyPrefix + "swarm-capsule-row")).
			AlignItems(ui.Center).
			Gap(Space2).
			Children(func() {
				badge := ui.Box(c.Key(p.KeyPrefix+"swarm-badge")).
					Size(effortSwarmBadgeSize, effortSwarmBadgeSize).
					Radius(effortSwarmBadgeSize / 2).
					Center()
				if p.SwarmOn {
					badge.Draw(func(painter *ui.Painter, r ui.Rect) {
						stops := make([]ui.GradientStop, 0, len(swarmRosterColors)+1)
						for i, col := range swarmRosterColors {
							stops = append(stops, ui.GradientStop{Color: col, At: float32(i) / float32(len(swarmRosterColors))})
						}
						stops = append(stops, ui.GradientStop{Color: swarmRosterColors[0], At: 1})
						painter.FillConicGradient(r, ui.ConicGradient{Stops: stops}, r.W/2)
						inner := ui.Rect{
							X: r.X + effortSwarmRingWidth,
							Y: r.Y + effortSwarmRingWidth,
							W: r.W - effortSwarmRingWidth*2,
							H: r.H - effortSwarmRingWidth*2,
						}
						painter.Fill(inner, modelPickerPanelBg, inner.W/2)
					})
					badge.Children(func() {
						ui.Icon(c, IconSwarm()).Size(effortSwarmIconSize, effortSwarmIconSize).TextColor(effortOnDark)
					})
					ui.Text(c, "编排").
						FontSize(uiFont(TypeFootnote)).
						FontWeight(500).
						TextColor(ColorTextPrimaryDark)
				} else {
					badge.Background(effortOnDark.Alpha(0.08))
					badge.Children(func() {
						ui.Icon(c, IconSwarm()).Size(effortSwarmIconSize, effortSwarmIconSize).TextColor(effortOnDark.Alpha(0.55))
					})
					ui.Text(c, "编排").
						FontSize(uiFont(TypeFootnote)).
						FontWeight(500).
						TextColor(ColorTextSecondaryDark)
				}
			})
	})
}

func renderToolbarPairFavButton(c *ui.Context, p EffortSliderCardProps) {
	fav := p.PairFav
	if fav == nil {
		return
	}
	canToggle := !p.Disabled && fav.OnToggle != nil
	icon := IconStar()
	label := "存为配对"
	tip := "存为配对"
	iconColor := ColorTextSecondaryDark
	if fav.Matched {
		icon = IconStarFilled()
		label = "已保存为配对"
		tip = "取消配对"
		iconColor = effortBoltYellow
	}

	btn := ui.ButtonBase(c.Key(p.KeyPrefix+"effort-pair-fav-btn")).
		Label(label).
		Value(map[bool]string{true: "已收藏", false: "未收藏"}[fav.Matched]).
		Tooltip(effortLockedTip(p, tip)).
		Disabled(p.Disabled || fav.OnToggle == nil).
		Checked(fav.Matched).
		Size(effortToolbarBtnSize, effortToolbarBtnSize).
		Radius(RadiusMD).
		Center()

	scale := motionPressScale(c, btn, "button-press-scale", canToggle && btn.Pressed())
	btn.Scale(scale, scale)
	hovered := btn.Hovered() && canToggle
	if fav.Matched {
		bg := effortBoltYellow.Alpha(0.14)
		if hovered {
			bg = effortBoltYellow.Alpha(0.22)
		}
		btn.Background(bg).Border(1, effortBoltYellow.Alpha(0.28))
	} else {
		bg := effortOnDark.Alpha(0.05)
		if hovered {
			bg = effortOnDark.Alpha(0.10)
		}
		btn.Background(bg).Border(1, effortOnDark.Alpha(0.06))
	}
	if canToggle {
		btn.OnClick(fav.OnToggle)
	}
	btn.Children(func() {
		ui.Icon(c, icon).Size(effortToolbarIconSize, effortToolbarIconSize).TextColor(iconColor)
	})
}

func renderToolbarFastModeButton(c *ui.Context, p EffortSliderCardProps) {
	state := "关闭"
	if p.FastMode {
		state = "开启"
	}
	btn := ui.ButtonBase(c.Key(p.KeyPrefix+"fast-mode-btn")).
		Label("快速模式").
		Value(state).
		Tooltip(effortLockedTip(p, "快速模式")).
		Disabled(p.Disabled).
		Checked(p.FastMode).
		Size(effortToolbarBtnSize, effortToolbarBtnSize).
		Radius(RadiusMD).
		Center().
		Transition(motionFeedback(c))

	scale := motionPressScale(c, btn, "button-press-scale", !p.Disabled && btn.Pressed())
	btn.Scale(scale, scale)
	switch {
	case p.FastMode:
		btn.Background(effortBoltYellow.Alpha(0.18)).Border(1, effortBoltYellow.Alpha(0.32))
	case btn.Hovered() && !p.Disabled:
		btn.Background(effortOnDark.Alpha(0.10)).Border(1, effortOnDark.Alpha(0.06))
	default:
		btn.Background(effortOnDark.Alpha(0.05)).Border(1, effortOnDark.Alpha(0.06))
	}
	if p.OnToggleFast != nil && !p.Disabled {
		btn.OnClick(p.OnToggleFast)
	}
	btn.Children(func() {
		iconOn := float32(0)
		icon := IconByName("zap")
		if p.FastMode {
			iconOn = 1
			icon = iconFastBoltFilled
		}
		on := btn.AnimateTo(effortFastModeIconKey, iconOn, ui.Animation{Duration: animateDuration(c, effortFastModeDuration), Ease: ui.EaseOut})
		ui.Icon(c, icon).Size(effortToolbarIconSize, effortToolbarIconSize).TextColor(ColorTextSecondaryDark.Mix(effortBoltYellow, on))
	})
}

func renderToolbarResetButton(c *ui.Context, p EffortSliderCardProps) {
	btn := ui.ButtonBase(c.Key(p.KeyPrefix+"effort-reset-btn")).
		Label("重置强度与快速模式").
		Tooltip(effortLockedTip(p, "重置")).
		Disabled(p.Disabled).
		Size(effortToolbarBtnSize, effortToolbarBtnSize).
		Radius(RadiusMD).
		Center()

	scale := motionPressScale(c, btn, "button-press-scale", !p.Disabled && btn.Pressed())
	btn.Scale(scale, scale)
	bg := effortOnDark.Alpha(0.05)
	if btn.Hovered() && !p.Disabled {
		bg = effortOnDark.Alpha(0.10)
	}
	btn.Background(bg).Border(1, effortOnDark.Alpha(0.06))
	if p.OnReset != nil && !p.Disabled {
		btn.OnClick(p.OnReset)
	}
	btn.Children(func() {
		ui.Icon(c, iconRotateCcw).
			Size(effortToolbarIconSize, effortToolbarIconSize).
			TextColor(effortTextSoft)
	})
}

func renderEffortRosterSlots(c *ui.Context, p EffortSliderCardProps) {
	ui.Column(c.Key(p.KeyPrefix+"roster-card")).
		Radius(RadiusLG).
		Background(effortOnDark.Alpha(0.035)).
		Border(1, effortOnDark.Alpha(0.08)).
		Clip().
		AlignSelf(ui.Stretch).
		Children(func() {
			for i, slot := range p.Roster {
				if i > 0 {
					ui.Box(c.Key(p.KeyPrefix + "roster-div-" + strconv.Itoa(i))).
						Height(Hairline).
						Background(effortOnDark.Alpha(0.065)).
						FillWidth()
				}
				renderEffortRosterSlot(c, p, i, slot)
			}
		})
}

func renderEffortRosterSlot(c *ui.Context, p EffortSliderCardProps, index int, slot EffortRosterSlot) {
	rowKey := strings.TrimSpace(slot.Key)
	if rowKey == "" {
		rowKey = strconv.Itoa(index)
	}

	row := ui.Row(c.Key(p.KeyPrefix+"roster-row-"+rowKey)).
		MinHeight(effortRosterRowMinH).
		Padding(Space2, Space2, Space2, Space3).
		AlignItems(ui.Center).
		Gap(Space2).
		AlignSelf(ui.Stretch)

	if slot.Active {
		row.Background(ColorAccentDark.Alpha(0.11)).
			Draw(func(painter *ui.Painter, r ui.Rect) {
				if r.H <= effortRosterBarInsetY*2 {
					return
				}
				painter.Fill(ui.Rect{
					X: r.X,
					Y: r.Y + effortRosterBarInsetY,
					W: effortRosterBarW,
					H: r.H - effortRosterBarInsetY*2,
				}, ColorAccentDark, effortRosterBarRadius)
			})
	}

	row.Children(func() {
		// 主体按钮与并发上限胶囊、行尾档位箭头按钮保持兄弟关系，禁止嵌套 ButtonBase。
		mainLabel := slot.ModelTitle
		var onMainClick func()
		var mainTip string
		if slot.Active {
			if slot.OnOpenList != nil {
				mainLabel = "选择其他模型"
				if slot.Disabled {
					mainTip = strings.TrimSpace(slot.LockedTip)
					if mainTip == "" {
						mainTip = effortLockedTip(p, "选择模型")
					}
				} else {
					onMainClick = slot.OnOpenList
					mainTip = "选择模型"
				}
			}
		} else {
			if strings.TrimSpace(slot.Role) != "" {
				mainLabel = slot.Role
			}
			if slot.OnSelect != nil {
				onMainClick = slot.OnSelect
			}
		}
		canClickMain := onMainClick != nil
		mainBtn := ui.ButtonBase(c.Key(p.KeyPrefix+"roster-main-"+rowKey)).
			Label(mainLabel).
			Tooltip(mainTip).
			Disabled(!canClickMain).
			Padding(Space0, Space1).
			Radius(RadiusSM).
			Grow(1).
			MinWidth(0).
			Justify(ui.Start)

		mainScale := motionPressScale(c, mainBtn, "button-press-scale", canClickMain && mainBtn.Pressed())
		mainBtn.Scale(mainScale, mainScale)
		if mainBtn.Hovered() && canClickMain {
			mainBtn.Background(effortOnDark.Alpha(0.05))
		}
		if canClickMain {
			mainBtn.OnClick(onMainClick)
		}

		mainBtn.Children(func() {
			ui.Row(c.Key(p.KeyPrefix + "roster-main-row-" + rowKey)).
				AlignItems(ui.Center).
				Gap(Space2).
				MinWidth(0).
				Children(func() {
					if strings.TrimSpace(slot.Role) != "" {
						badgeBg := effortOnDark.Alpha(0.06)
						badgeFg := ColorTextSecondaryDark
						badgeWeight := 500
						if slot.Active {
							badgeBg = ColorAccentDark.Alpha(0.22)
							badgeFg = effortAccentBright
							badgeWeight = 600
						}
						ui.Box(c.Key(p.KeyPrefix+"roster-role-"+rowKey)).
							Padding(Space0, Space2).
							Radius(RadiusXS).
							Background(badgeBg).
							Shrink(0).
							Children(func() {
								ui.Text(c, slot.Role).
									FontSize(uiFont(TypeCaption)).
									FontWeight(badgeWeight).
									SingleLine().
									TextColor(badgeFg)
							})
					}

					hasBrand := strings.TrimSpace(slot.BrandID) != "" || strings.TrimSpace(slot.ProviderID) != ""
					if strings.TrimSpace(slot.Placeholder) == "" && hasBrand {
						iconBox := ui.Box(c.Key(p.KeyPrefix + "roster-icon-" + rowKey)).Shrink(0)
						if !slot.Active {
							iconBox.Opacity(0.80)
						}
						iconBox.Children(func() {
							renderEffortBrandIconSize(c, slot.BrandID, slot.ProviderID, 14)
						})
					}

					switch {
					case strings.TrimSpace(slot.Placeholder) != "":
						ui.Text(c, slot.Placeholder).
							FontSize(uiFont(TypeProse)).
							FontWeight(500).
							SingleLine().
							Ellipsis("…").
							Shrink(1).
							MinWidth(0).
							TextColor(ColorTextSecondaryDark)
					case slot.Active:
						renderEffortTitleWithStyle(c, p, slot.ModelTitle, uiFont(TypeProse), 600)
					default:
						title := strings.TrimSpace(slot.ModelTitle)
						if title == "" {
							title = "标准"
						}
						ui.Text(c, title).
							FontSize(uiFont(TypeProse)).
							FontWeight(500).
							SingleLine().
							Ellipsis("…").
							Shrink(1).
							MinWidth(0).
							TextColor(ColorTextPrimaryDark.Alpha(0.76))
					}
				})
		})

		if slot.Accessory != nil {
			slot.Accessory()
		}

		if strings.TrimSpace(slot.EffortLabel) != "" || slot.OnOpenList != nil {
			canOpen := slot.OnOpenList != nil && !slot.Disabled
			trailLabel := "打开模型列表"
			if strings.TrimSpace(slot.Role) != "" {
				trailLabel = "选择" + slot.Role + "模型"
			} else if strings.TrimSpace(slot.EffortLabel) != "" {
				trailLabel = slot.EffortLabel
			}
			var trailTip string
			if slot.OnOpenList != nil {
				if slot.Disabled {
					trailTip = strings.TrimSpace(slot.LockedTip)
					if trailTip == "" {
						trailTip = effortLockedTip(p, "选择模型")
					}
				} else {
					trailTip = "选择模型"
				}
			}
			trailBtn := ui.ButtonBase(c.Key(p.KeyPrefix+"roster-trail-"+rowKey)).
				Label(trailLabel).
				Tooltip(trailTip).
				Disabled(!canOpen).
				Padding(Space0, Space1).
				Radius(RadiusSM).
				Shrink(0).
				Center()

			trailScale := motionPressScale(c, trailBtn, "button-press-scale", canOpen && trailBtn.Pressed())
			trailBtn.Scale(trailScale, trailScale)
			if trailBtn.Hovered() && canOpen {
				trailBtn.Background(effortOnDark.Alpha(0.06))
			}
			if canOpen {
				trailBtn.OnClick(slot.OnOpenList)
			}
			trailBtn.Children(func() {
				ui.Row(c.Key(p.KeyPrefix + "roster-trail-row-" + rowKey)).
					AlignItems(ui.Center).
					Gap(Space1).
					Children(func() {
						if strings.TrimSpace(slot.EffortLabel) != "" {
							effColor := ColorTextSecondaryDark
							effWeight := 500
							if slot.Active {
								effColor = ColorTextPrimaryDark.Alpha(0.90)
								effWeight = 600
							}
							ui.Text(c, slot.EffortLabel).
								FontSize(uiFont(TypeCaption)).
								FontWeight(effWeight).
								SingleLine().
								TextColor(effColor)
						}
						if slot.OnOpenList != nil {
							ui.Icon(c, IconChevronRight()).
								Size(12, 12).
								TextColor(ColorTextSecondaryDark)
						}
					})
			})
		}
	})
}
