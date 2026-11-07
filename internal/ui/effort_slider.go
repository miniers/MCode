package mcodeui

import (
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
	OnEffort          func(string)
	OnToggleFast      func()
	OnReset           func()
	OnOpenModelList   func() // 为 nil 时不显示 chevron
	OnToggleSwarm     func() // 切换 Swarm 状态；为 nil 时卡片右侧不放 Swarm 开关
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

// swarmSideTabLabel 生成分段标签展示文案：
// “<角色> · <档位英文名>”，例如“主导 · Medium”“执行 · Max”，档位名与 modelEffortTitle 一致。
// role 取 "lead" 或 "bees"；调用方应传入已回退到模型默认档位的 effort，
// effort 仍为空时只显示角色名；bees 在 off 时返回“执行 · 关闭”。
func swarmSideTabLabel(role string, effort string, off bool) string {
	name := "子代理"
	if role == "lead" {
		name = "主代理"
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

// renderEffortResetButton 渲染标题右侧的圆形重置按钮：
// 30x30、静止透明，悬停时白 10% 圆底，图标为次要色。
func renderEffortResetButton(c *ui.Context, p EffortSliderCardProps) {
	btn := ui.ButtonBase(c.Key(p.KeyPrefix+"effort-reset-btn")).
		Label("重置强度与快速模式").
		Tooltip(effortLockedTip(p, "重置强度与快速模式")).
		Disabled(p.Disabled).
		Size(effortCardButtonSize, effortCardButtonSize).
		Radius(RadiusPill).
		Center()

	if btn.Hovered() && !p.Disabled {
		btn.Background(effortOnDark.Alpha(0.10))
	}
	if p.OnReset != nil {
		btn.OnClick(p.OnReset)
	}

	btn.Children(func() {
		ui.Icon(c, iconRotateCcw).
			Size(14, 14).
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
		tip = "已保存为配对：" + fav.Name + "（点击取消）"
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
	if btn.Hovered() {
		btn.Background(effortOnDark.Alpha(0.10))
	}
	btn.OnClick(fav.OnToggle)
	btn.Children(func() {
		ui.Icon(c, icon).Size(12, 12).TextColor(color)
	})
}

// renderEffortBrandIcon 渲染 12 号 Provider/Brand 图标：
// 若 brandID 非空且在 colorProviderIcons 中存在则优先使用，否则在 providerID 非空时回退 providerID。
func renderEffortBrandIcon(c *ui.Context, brandID, providerID string) {
	if brand := strings.TrimSpace(brandID); colorProviderIcons[brand] != nil {
		renderProviderIcon(c, brand, 12)
	} else if providerID != "" {
		renderProviderIcon(c, providerID, 12)
	}
}

// renderEffortSubtitleRow 渲染标题下方的模型说明行：配对时左侧是收藏按钮，接着无论是否配对均渲染
// 主代理 12 号图标与模型名；若配对且存在子代理模型名，再紧随“+”、子代理 12 号图标与子模型名；
// 末尾可再附 SubtitleAccessory 自定义小部件（如并发 Bee 上限胶囊）。
func renderEffortSubtitleRow(c *ui.Context, p EffortSliderCardProps) {
	ui.Row(c.Key(p.KeyPrefix + "effort-subtitle-row")).
		AlignItems(ui.Center).
		Gap(Space1).
		Shrink(1).
		MinWidth(0).
		Children(func() {
			if effortCardPaired(p) {
				renderPairFavButton(c, p)
			}
			renderEffortBrandIcon(c, p.BrandID, p.ProviderID)
			ui.Text(c, p.Subtitle).
				FontSize(TypeBody).
				TextColor(effortTextSoft).
				SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)

			if effortCardPaired(p) && strings.TrimSpace(p.SubtitlePair) != "" {
				ui.Text(c, "+").
					FontSize(TypeBody).
					TextColor(effortTextSoft).
					Shrink(0)
				renderEffortBrandIcon(c, p.PairBrandID, p.PairProviderID)
				ui.Text(c, p.SubtitlePair).
					FontSize(TypeBody).
					TextColor(effortTextSoft).
					SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
			}

			if p.SubtitleAccessory != nil {
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
	tip := "快速模式"
	if p.FastMode {
		state = "开启"
		tip = "快速模式已开启"
	}
	btn := ui.ButtonBase(c.Key(p.KeyPrefix+"fast-mode-btn")).
		Label("快速模式").
		Value(state).
		Tooltip(effortLockedTip(p, tip)).
		Disabled(p.Disabled).
		Checked(p.FastMode).
		Size(effortCardButtonSize, effortCardButtonSize).
		Radius(effortCardButtonSize / 2).
		Center().
		// Colors 只插值背景与描边色：开/关切换时底色在系统黄与白色微光之间渐变，悬停的加深也一并变柔和。
		Transition(ui.ElementTransition{Colors: true, Duration: effortFastModeDuration})

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

// swarmRosterColors 是 Swarm 名册前六位的代表色（Aspen、Birch、Cove、Dune、Ember、Flint），
// 开关的彩虹环与同 Provider 配对的融合色都取自这里；色值统一由 theme.go 的 identityPalette 提供。
var swarmRosterColors = []ui.Color{
	ThemeIdentity("aspen"), ThemeIdentity("birch"), ThemeIdentity("cove"),
	ThemeIdentity("dune"), ThemeIdentity("ember"), ThemeIdentity("flint"),
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

// effortSliderTickLabels 报告卡片是否显示各档位名：档位名只出现在无障碍值与
// Swarm 分段栏里，轨道下方不再铺一排档位文字；只有卡片本身承载点击时才绘制。
func effortSliderTickLabels(p EffortSliderCardProps) bool {
	return false
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
	effortThumbD   = float32(42) // 玻璃滑块直径，比轨道高，滑轨整体高度取它
	effortDotD     = float32(5)  // 档位刻度圆点直径
	effortTitleSz  = float32(17) // 档位名字号（macOS .title2）
	effortTitleWgt = 500         // 档位名字重（.medium）
)

// renderEffortSlider 渲染推理强度滑轨：42 高的区域里居中一条 34 高的
// 胶囊轨道，已选部分按 TrackLook 填色并一直填到滑块外缘，每档一个 5px 圆点，42 直径的玻璃圆滑块。
// 首尾档位的圆点内缩半个滑块直径，使滑块在两端也完整可见。
func renderEffortSlider(c *ui.Context, p EffortSliderCardProps) {
	n := len(p.Efforts)
	if n == 0 {
		return
	}
	curIdx := effortIndex(p.Efforts, p.Current, p.DefaultEffort)
	look := effortTrackLookFor(p)

	value := float64(curIdx)
	slider := ui.SliderBase(c.Key(p.KeyPrefix+"effort-slider"), &value, 0, float64(n-1)).
		Step(1).
		Label("推理强度").
		Value(effortLabel(p.Efforts[curIdx])).
		Disabled(p.Disabled).
		Height(effortThumbD).
		PaddingX(effortThumbD / 2).
		AlignSelf(ui.Stretch)

	if slider.Changed() && p.OnEffort != nil {
		idx := effortSliderSnapIndex(float32(value), float32(n-1), n)
		p.OnEffort(p.Efforts[idx])
	}

	pressed := slider.Pressed()
	slider.Draw(func(painter *ui.Painter, r ui.Rect) {
		drawEffortTrack(painter, r, n, curIdx, look, pressed)
	})
}

// drawEffortTrack 绘制滑轨：白 10% 轨道、TrackLook 填充、刻度点（已越过的用 TrackLook 刻度色，
// 其余白 32%），最后是玻璃滑块——下垫一层模糊光晕，上面是白色半透明圆，浅色填充时加深色描边。
func drawEffortTrack(painter *ui.Painter, r ui.Rect, n, curIdx int, look effortTrackLook, pressed bool) {
	const inset = effortThumbD / 2

	slideW := max(float32(0), r.W-inset*2)
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

	passed := ColorTextSecondaryDark
	if look.light {
		passed = effortTickOnLight
	}
	for _, stop := range effortTickPositions(n, slideW) {
		stopX := r.X + inset + stop
		dot := effortOnDark.Alpha(0.32)
		if stopX <= curX+0.5 {
			dot = passed
		}
		painter.Fill(ui.Rect{X: stopX - effortDotD/2, Y: midY - effortDotD/2, W: effortDotD, H: effortDotD}, dot, effortDotD/2)
	}

	d := effortThumbD
	if pressed {
		d *= 1.06
	}
	knob := ui.Rect{X: curX - d/2, Y: midY - d/2, W: d, H: d}
	// MyGo 没有 Liquid Glass：用上亮下暗的白色半透明渐变加一圈高光边近似玻璃透镜。
	tint := float32(0.32)
	if look.light {
		tint = 0.55
	}
	painter.FillGradient(knob, ui.LinearGradient{
		Angle: 180,
		Stops: []ui.GradientStop{
			{Color: effortOnDark.Alpha(tint + 0.14), At: 0},
			{Color: effortOnDark.Alpha(tint - 0.06), At: 1},
		},
	}, d/2)
	edge := ColorTextTertiaryDark
	if look.light {
		edge = effortKnobOnLight
	}
	painter.Stroke(knob, edge, d/2, 1)
}

// effortCardTitle 返回卡片大标题：当前档位的英文名（与 modelEffortTitle 一致，如 Medium、Extra High），
// 无可选档位时为“标准”。
func effortCardTitle(p EffortSliderCardProps) string {
	if len(p.Efforts) == 0 {
		return "标准"
	}
	return modelEffortTitle(p.Efforts[effortIndex(p.Efforts, p.Current, p.DefaultEffort)])
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

// renderEffortTitle 渲染档位名（17 号、medium），颜色取 effortTitleGradient：单色时直接着色，
// 多色时逐字取渐变色。
func renderEffortTitle(c *ui.Context, p EffortSliderCardProps) {
	title := effortCardTitle(p)
	colors := effortTitleGradient(p)
	text := ui.Text(c, title).FontSize(effortTitleSz).FontWeight(effortTitleWgt).SingleLine()
	if len(colors) == 1 {
		text.TextColor(colors[0])
		return
	}
	text.TextColor(ui.Transparent).Draw(func(painter *ui.Painter, r ui.Rect) {
		// MyGo 无原生渐变文本 API：逐字符着色成富文本再整段绘制。
		runes := []rune(title)
		spans := make([]ui.Span, len(runes))
		for i, ch := range runes {
			t := float32(0)
			if len(runes) > 1 {
				t = float32(i) / float32(len(runes)-1)
			}
			spans[i] = ui.Span{Text: string(ch), Color: effortSampleColors(colors, t)}
		}
		layout := ui.ShapeRichText(spans, ui.Font{Size: effortTitleSz, Weight: effortTitleWgt}, 0)
		painter.TextLayout(layout, r.X, r.Y, colors[0])
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

// renderEffortTitleRow 渲染档位标题行：档位名 + 次要色“›”组成的标题按钮
// （横向内边距 8、圆角 7，悬停白 8% 底，点击进入模型列表），重置按钮贴在其右侧 6 处。
// 重置按钮不占位；这里在左侧垫一块等宽占位，让标题按钮仍居中。
func renderEffortTitleRow(c *ui.Context, p EffortSliderCardProps) {
	const resetGap = float32(6)
	ui.Row(c.Key(p.KeyPrefix + "title-row")).
		AlignItems(ui.Center).
		Gap(resetGap).
		Children(func() {
			ui.Box(c.Key(p.KeyPrefix+"title-balance")).Size(effortCardButtonSize, effortCardButtonSize)

			canOpen := p.OnOpenModelList != nil
			titleBtn := ui.ButtonBase(c.Key(p.KeyPrefix+"title-btn")).
				Label("选择其他模型").
				Tooltip(effortTitleCaption(p)).
				Disabled(p.Disabled || !canOpen).
				Padding(Space0, Space3).
				Radius(RadiusSM)
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
					Children(func() {
						renderEffortTitle(c, p)
						if canOpen {
							ui.Icon(c, IconChevronRight()).
								Size(12, 12).
								TextColor(ColorTextSecondaryDark)
						}
					})
			})

			renderEffortResetButton(c, p)
		})
}

// renderEffortSliderCard 渲染推理强度卡片：内边距上 14、左右 16、
// 下 16，两行间距 16。首行左右是 30 的快速模式按钮与 Swarm 开关，中间一栏距卡片边 44、水平居中，
// 依次是档位标题行与模型说明行；第二行是占满宽度的滑轨。
func renderEffortSliderCard(c *ui.Context, p EffortSliderCardProps) {
	n := len(p.Efforts)
	ui.Column(c.Key(p.KeyPrefix+"effort-slider-card")).
		Gap(Space5).
		Padding(Space4, Space5, Space5, Space5).
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

			if n == 0 {
				ui.Row(c.Key(p.KeyPrefix + "single-level-row")).
					Center().
					Height(effortThumbD).
					Children(func() {
						ui.Text(c, "该模型未提供可调的推理强度").
							FontSize(TypeFootnote).
							TextColor(ColorTextSecondaryDark)
					})
			} else {
				renderEffortSlider(c, p)
			}
		})
}
