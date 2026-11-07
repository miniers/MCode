package mcodeui

import (
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"mcode/internal/config"
)

// 子线程汇报正文（「工作汇报」）的 Markdown 配置：按基准字号缓存，
// 避免每帧重建（markdown.Options 内部带图片缓存，每帧新建会让图片反复重新加载）。
var (
	swarmReportMDOptsMu sync.Mutex
	swarmReportMDOpts   = make(map[float32]*markdown.Options)
)

// cachedSwarmReportOptions 按基准字号取（或建）汇报正文的 Markdown 选项容器。
func cachedSwarmReportOptions(baseSize float32) *markdown.Options {
	swarmReportMDOptsMu.Lock()
	defer swarmReportMDOptsMu.Unlock()
	opts := swarmReportMDOpts[baseSize]
	if opts == nil {
		opts = &markdown.Options{}
		swarmReportMDOpts[baseSize] = opts
	}
	return opts
}

// swarmReportMarkdownOptions 返回汇报正文用的 Markdown 配置：时间线胶囊浮窗与子线程浮窗里的
// 「工作汇报」共用。汇报是已经写好的文字，所以整段静态呈现（Reveal 用 AtOnce，不做逐字揭示）；
// 正文字号按 baseSize 指定并跟随「文字大小」，颜色沿用原来的正文色与主题色；
// 代码块沿用助手正文的标题栏与复制按钮，图片支持点击看大图。
func swarmReportMarkdownOptions(c *ui.Context, s *chatState, baseSize float32) *markdown.Options {
	opts := cachedSwarmReportOptions(baseSize)
	if opts.Theme == nil {
		opts.Theme = &markdown.Theme{
			LineHeight:      1.45,
			CodeBlockRadius: 8,
			QuoteBarWidth:   3,
			// 卡片比主时间线窄：收窄表格列宽上限，让单元格换行，整表再超宽就横向滚动。
			TableMaxColumn: 14,
			Headings: [6]markdown.HeadingStyle{
				{Size: 1.25, Weight: 600, Top: 0.40, Bottom: 0.24}, // H1
				{Size: 1.12, Weight: 600, Top: 0.34, Bottom: 0.20}, // H2
				{Size: 1.00, Weight: 600, Top: 0.20, Bottom: 0.16}, // H3
				{Size: 0.90, Weight: 600, Top: 0.15, Bottom: 0.12}, // H4
				{Size: 0.90, Weight: 600, Top: 0.15, Bottom: 0.12}, // H5
				{Size: 0.90, Weight: 600, Top: 0.15, Bottom: 0.12}, // H6
			},
		}
	}
	// 颜色与字号每帧同步，跟随主题切换与「文字大小」的即时改动。
	th := opts.Theme
	th.FontSize = scaledFont(baseSize)
	th.Text = ThemePrimaryText(c)
	th.Muted = ThemeSecondaryText(c).Alpha(0.75)
	th.Link = ThemeAccent(c)
	th.QuoteBar = ThemeBorder(c)
	th.QuoteText = ThemeSecondaryText(c)
	th.CodeBlockBackground = ThemeSurface(c)
	th.CodeBlockBorder = ThemeBorder(c)
	// 代码块跟随设置里的等宽字体与字号（CodeSize 是相对基准字号的 em 值）。
	var appCfg config.AppearanceConfig
	if st := getChatSettings(s); st != nil {
		appCfg = st.cfg.Appearance
	}
	th.CodeFont = codeFontFamily(appCfg)
	th.CodeSize = codeFontSize(appCfg) / baseSize

	if opts.Reveal == nil {
		opts.Reveal = &markdown.Reveal{Unit: markdown.AtOnce, Fade: -1}
	}
	if s != nil {
		// 代码块复用助手正文的容器（语言标题栏 + 复制按钮），汇报里的代码也能一键复制。
		ensureCodeBlockContainer(s)
		opts.CodeBlock = s.mdStaticOpts.CodeBlock
		installImagePreviewHooks(opts, s)
		installLocalFileLinkHook(opts, s)
		// 汇报里的表格同样可以展开到浮窗里看完整内容。
		installTableExpandHook(opts, s)
	}
	return opts
}
