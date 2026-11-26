package mcodeui

import (
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"
	"mcode/internal/config"
)

// 子线程汇报正文（「工作汇报」）的 Markdown 配置：按基准字号缓存，
// 避免每帧重建（markdown.Options 内部带图片缓存，每帧新建会让图片反复重新加载）。
var (
	swarmReportMDOptsMu sync.Mutex
	swarmReportMDOpts   = make(map[float32]*markdown.Options)
)

// markdownDocKey 区分文档预览的 Markdown 选项缓存：按会话状态、基准字号与文档所在目录隔离。
type markdownDocKey struct {
	state *chatState
	size  float32
	dir   string
}

var (
	markdownDocMDOptsMu sync.Mutex
	markdownDocMDOpts   = make(map[markdownDocKey]*markdown.Options)
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

// cachedMarkdownDocOptions 按 (state, baseSize, baseDir) 取（或建）文档预览的 Markdown 选项容器。
func cachedMarkdownDocOptions(s *chatState, baseSize float32, baseDir string) *markdown.Options {
	key := markdownDocKey{state: s, size: baseSize, dir: baseDir}
	markdownDocMDOptsMu.Lock()
	defer markdownDocMDOptsMu.Unlock()
	opts := markdownDocMDOpts[key]
	if opts == nil {
		opts = &markdown.Options{}
		markdownDocMDOpts[key] = opts
	}
	return opts
}

// installMarkdownHooks 为 Markdown 选项统一安装代码块、扩展语法、公式、图片预览、
// 本地文件链接、表格展开与 HTML 图片等公共钩子。
func installMarkdownHooks(o *markdown.Options, s *chatState) {
	installMarkdownHooksWithBaseDir(o, s, "")
}

// installMarkdownHooksWithBaseDir 为 Markdown 选项安装公共钩子，并把相对图片路径绑定到 baseDir。
func installMarkdownHooksWithBaseDir(o *markdown.Options, s *chatState, baseDir string) {
	if o == nil || s == nil {
		return
	}
	// 代码块复用助手正文的容器（语言标题栏 + 复制按钮）。
	ensureCodeBlockContainer(s)
	o.CodeBlock = s.mdStaticOpts.CodeBlock
	// 与主会话共享同一个 Code map，使 html/mermaid/pr-review 等渲染器的变动自动同步到各处。
	if s.mdStaticOpts.Code == nil {
		s.mdStaticOpts.Code = make(map[string]func(c *ui.Context, b *markdown.CodeBlockView))
	}
	o.Code = s.mdStaticOpts.Code
	if s.mdStaticOpts.Extensions != 0 {
		o.Extensions = s.mdStaticOpts.Extensions
	} else {
		o.Extensions = parser.Default | parser.ExtDollarMath
	}
	installImagePreviewHooksWithBaseDir(o, s, baseDir)
	installLocalFileLinkHook(o, s)
	installTableExpandHook(o, s)
	installMathHooks(o)
	installMarkdownHTMLImageHook(o, s, baseDir)
}

// applyDocMarkdownTheme 为文档或汇报类的静态 Markdown 选项应用主题、字号与即时揭示配置。
func applyDocMarkdownTheme(c *ui.Context, s *chatState, opts *markdown.Options, baseSize float32) {
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
	th.CodeSize = codeFontSize() / baseSize

	if opts.Reveal == nil {
		opts.Reveal = &markdown.Reveal{Unit: markdown.AtOnce, Fade: -1}
	}
}

// swarmReportMarkdownOptions 返回汇报正文用的 Markdown 配置：时间线胶囊浮窗与子线程浮窗里的
// 「工作汇报」共用。汇报是已经写好的文字，所以整段静态呈现（Reveal 用 AtOnce，不做逐字揭示）；
// 正文字号按 baseSize 指定并跟随「文字大小」，颜色沿用原来的正文色与主题色；
// 代码块沿用助手正文的标题栏与复制按钮，图片支持点击看大图。
func swarmReportMarkdownOptions(c *ui.Context, s *chatState, baseSize float32) *markdown.Options {
	opts := cachedSwarmReportOptions(baseSize)
	applyDocMarkdownTheme(c, s, opts, baseSize)
	if s != nil {
		installMarkdownHooks(opts, s)
	}
	return opts
}

// markdownDocOptions 返回绑定了基准目录 baseDir 的文档 Markdown 配置：
// 按 (state, baseSize, baseDir) 缓存，复用汇报正文的主题与全套公共钩子，
// 并把文档中的相对图片路径按 baseDir 解析为本地绝对路径。
func markdownDocOptions(c *ui.Context, s *chatState, baseSize float32, baseDir string) *markdown.Options {
	opts := cachedMarkdownDocOptions(s, baseSize, baseDir)
	applyDocMarkdownTheme(c, s, opts, baseSize)
	if s != nil {
		installMarkdownHooksWithBaseDir(opts, s, baseDir)
	}
	return opts
}

// swarmReportDetailOpen 记录胶囊浮窗与汇报卡片内「详情」折叠块的展开状态，由 swarmFloatMu 保护。
var swarmReportDetailOpen = make(map[string]bool)

// splitSwarmReport 把子代理汇报按独占一行的「## 结论」和「## 详情」拆成两段；
// 找不到「## 结论」时 conclusion 返回空串，detail 返回原文 TrimSpace。
func splitSwarmReport(summary string) (conclusion, detail string) {
	normalized := strings.ReplaceAll(summary, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	conclusionIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "## 结论" {
			conclusionIdx = i
			break
		}
	}
	if conclusionIdx < 0 {
		return "", strings.TrimSpace(summary)
	}
	detailIdx := -1
	for i := conclusionIdx + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "## 详情" {
			detailIdx = i
			break
		}
	}
	if detailIdx < 0 {
		return strings.TrimSpace(strings.Join(lines[conclusionIdx+1:], "\n")), ""
	}
	conclusion = strings.TrimSpace(strings.Join(lines[conclusionIdx+1:detailIdx], "\n"))
	detail = strings.TrimSpace(strings.Join(lines[detailIdx+1:], "\n"))
	return conclusion, detail
}

// trimOrderedListPrefix 去掉行首的数字序号前缀（如 "1. "、"12. "）。
func trimOrderedListPrefix(s string) (string, bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(s) || s[i] != '.' || s[i+1] != ' ' {
		return "", false
	}
	return s[i+2:], true
}

// swarmReportHeadline 提取汇报结论的第一个非空行，去掉常见 Markdown 标记后返回纯文本；
// 没有「## 结论」或结论为空时返回空串。
func swarmReportHeadline(summary string) string {
	conclusion, _ := splitSwarmReport(summary)
	if conclusion == "" {
		return ""
	}
	for _, raw := range strings.Split(conclusion, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		for {
			prev := line
			if strings.HasPrefix(line, "#") {
				line = strings.TrimSpace(strings.TrimLeft(line, "#"))
			}
			if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
				line = strings.TrimSpace(line[2:])
			} else if rest, ok := trimOrderedListPrefix(line); ok {
				line = strings.TrimSpace(rest)
			}
			if line == prev {
				break
			}
		}
		line = strings.ReplaceAll(line, "**", "")
		line = strings.ReplaceAll(line, "`", "")
		return strings.TrimSpace(line)
	}
	return ""
}

// renderSwarmReportSplitMarkdown 渲染拆分后的汇报正文：有结论时先渲染结论，
// 有详情时在下方渲染默认收起的「详情」开关与 motionUnfoldBody 折叠区；
// 没有「## 结论」时保持整段原样渲染。
func renderSwarmReportSplitMarkdown(c *ui.Context, detailKey, text string, renderMD func(string)) {
	conclusion, detail := splitSwarmReport(text)
	if conclusion == "" {
		renderMD(text)
		return
	}
	renderMD(conclusion)
	if detail == "" {
		return
	}

	swarmFloatMu.Lock()
	detailOpen := swarmReportDetailOpen[detailKey]
	swarmFloatMu.Unlock()

	ui.Row(c.Key(detailKey + "-detail-toggle-row")).
		Children(func() {
			btn := ui.ButtonBase(c.Key(detailKey+"-detail-toggle")).
				Label("详情").
				Padding(Space0, Space3, Space0, Space3).
				Radius(RadiusLG).
				Border(Hairline, ThemeBorder(c)).
				AlignItems(ui.Center).
				Gap(Space1).
				Cursor(ui.CursorPointer)
			focusRing(c, btn)
			if btn.Hovered() {
				btn.Background(ThemeHover(c))
			}
			if btn.Clicked() {
				detailOpen = !detailOpen
				swarmFloatMu.Lock()
				swarmReportDetailOpen[detailKey] = detailOpen
				swarmFloatMu.Unlock()
			}
			btn.Children(func() {
				chevron := IconChevronRight()
				if detailOpen {
					chevron = IconChevronDown()
				}
				ui.Icon(c, chevron).Size(10, 10).TextColor(ThemeSecondaryText(c))
				ui.Text(c, "详情").
					FontSize(scaledFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c))
			})
		})

	motionUnfoldBody(c, detailKey+"-detail-unfold", detailOpen, func() {
		renderMD(detail)
	})
}
