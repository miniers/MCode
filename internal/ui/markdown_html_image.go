package mcodeui

import (
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// markdownHTMLHooksInstalled 记录已装过 HTML 图片钩子的 Markdown 选项，避免重复安装。
var markdownHTMLHooksInstalled sync.Map // *markdown.Options → struct{}

// installMarkdownHTMLImageHook 为 Markdown 选项安装 HTML 块渲染钩子（每个选项只安装一次）：
// 能提取到图片时复用图片段落外观绘制，纯注释直接跳过，其余 HTML 按默认等宽次要文本显示。
func installMarkdownHTMLImageHook(o *markdown.Options, s *chatState, baseDir string) {
	if o == nil || s == nil {
		return
	}
	if _, done := markdownHTMLHooksInstalled.LoadOrStore(o, struct{}{}); done {
		return
	}
	if o.HTML == nil {
		o.HTML = func(c *ui.Context, raw string) {
			renderHTMLImageBlock(c, s, o, baseDir, raw)
		}
	}
}

// renderHTMLImageBlock 渲染 HTMLBlock：注释块直接跳过；提取到图片则按图片段落外观渲染，
// 提取不到图片时按 mygo 默认样式绘制原文。
func renderHTMLImageBlock(c *ui.Context, s *chatState, o *markdown.Options, baseDir, raw string) {
	if isHTMLCommentOnly(raw) {
		return
	}
	items := htmlImageItems(raw, isDark(c))
	if len(items) > 0 {
		for idx := range items {
			items[idx].url = resolveMarkdownImage(baseDir, items[idx].url)
		}
		renderMarkdownImages(c, s, items)
		return
	}
	renderRawHTMLFallback(c, o, raw)
}

// renderRawHTMLFallback 按 mygo 默认样式（等宽字体、Muted 色）渲染未接管的 HTML 原文。
func renderRawHTMLFallback(c *ui.Context, o *markdown.Options, raw string) {
	def := markdown.DefaultTheme(c.Theme())
	codeFont := def.CodeFont
	fontSize := def.FontSize
	codeSize := def.CodeSize
	muted := def.Muted
	if o != nil && o.Theme != nil {
		if o.Theme.CodeFont != "" {
			codeFont = o.Theme.CodeFont
		}
		if o.Theme.FontSize > 0 {
			fontSize = o.Theme.FontSize
		}
		if o.Theme.CodeSize > 0 {
			codeSize = o.Theme.CodeSize
		}
		if o.Theme.Muted.A != 0 {
			muted = o.Theme.Muted
		}
	}
	ui.Text(c, raw).Font(codeFont).FontSize(fontSize * codeSize).TextColor(muted)
}

// htmlImageRefs 从 HTML 片段里按出现顺序提取图片引用（支持 <img> 与 <picture> 深浅色切换）。
func htmlImageRefs(raw string, dark bool) []previewMarkdownImage {
	items := htmlImageItems(raw, dark)
	if len(items) == 0 {
		return nil
	}
	refs := make([]previewMarkdownImage, len(items))
	for i, it := range items {
		refs[i] = previewMarkdownImage{URL: it.url, Alt: it.alt}
	}
	return refs
}

// htmlImageItems 从 HTML 片段提取用于渲染的图片条目列表。
func htmlImageItems(raw string, dark bool) []markdownImageItem {
	nodes := parseHTMLFragmentNodes(raw)
	if len(nodes) == 0 {
		return nil
	}
	var items []markdownImageItem
	for _, n := range nodes {
		collectHTMLImageItems(n, dark, &items)
	}
	return items
}

// parseHTMLFragmentNodes 把 HTML 片段按 body 上下文解析成节点列表。
func parseHTMLFragmentNodes(raw string) []*html.Node {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	bodyCtx := &html.Node{
		Type:     html.ElementNode,
		DataAtom: atom.Body,
		Data:     "body",
	}
	nodes, err := html.ParseFragment(strings.NewReader(raw), bodyCtx)
	if err != nil {
		return nil
	}
	return nodes
}

// collectHTMLImageItems 递归收集节点内的图片；遇到 <picture> 时按当前深浅色从 <source> 选择地址。
func collectHTMLImageItems(n *html.Node, dark bool, out *[]markdownImageItem) {
	if n == nil {
		return
	}
	if n.Type == html.ElementNode {
		switch strings.ToLower(n.Data) {
		case "picture":
			collectPictureImages(n, dark, out)
			return
		case "img":
			if item, ok := imgNodeToItem(n, ""); ok {
				*out = append(*out, item)
			}
			return
		}
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		collectHTMLImageItems(ch, dark, out)
	}
}

// collectPictureImages 处理 <picture> 节点：按 prefers-color-scheme 选择匹配的 <source srcset>，
// 再替换同 <picture> 内 <img> 的地址；未匹配到时退回 <img> 自带的 src。
func collectPictureImages(pic *html.Node, dark bool, out *[]markdownImageItem) {
	preferredURL := picturePreferredSourceURL(pic, dark)
	var walk func(cur *html.Node)
	walk = func(cur *html.Node) {
		if cur == nil {
			return
		}
		if cur.Type == html.ElementNode && strings.EqualFold(cur.Data, "img") {
			if item, ok := imgNodeToItem(cur, preferredURL); ok {
				*out = append(*out, item)
			}
			return
		}
		for ch := cur.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	for ch := pic.FirstChild; ch != nil; ch = ch.NextSibling {
		walk(ch)
	}
}

// picturePreferredSourceURL 在 <picture> 内查找匹配当前深浅色偏好的 <source> 并取其 srcset 首地址。
func picturePreferredSourceURL(pic *html.Node, dark bool) string {
	var found string
	var walk func(cur *html.Node)
	walk = func(cur *html.Node) {
		if cur == nil || found != "" {
			return
		}
		if cur.Type == html.ElementNode && strings.EqualFold(cur.Data, "source") {
			media, _ := htmlAttr(cur, "media")
			if matchesColorSchemeMedia(media, dark) {
				srcset, _ := htmlAttr(cur, "srcset")
				if u := firstSrcsetURL(srcset); u != "" {
					found = u
					return
				}
			}
		}
		for ch := cur.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	for ch := pic.FirstChild; ch != nil; ch = ch.NextSibling {
		walk(ch)
	}
	return found
}

// matchesColorSchemeMedia 判断 media 属性是否包含对应深浅色的 prefers-color-scheme 条件。
func matchesColorSchemeMedia(media string, dark bool) bool {
	if media == "" {
		return false
	}
	norm := strings.Join(strings.Fields(strings.ToLower(media)), "")
	if dark {
		return strings.Contains(norm, "prefers-color-scheme:dark")
	}
	return strings.Contains(norm, "prefers-color-scheme:light")
}

// firstSrcsetURL 从 HTML srcset 属性值里取出第一个候选 URL（去掉 1x/2x/800w 等描述符）。
func firstSrcsetURL(srcset string) string {
	s := strings.TrimSpace(srcset)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToLower(s), "data:") {
		if comma := strings.IndexByte(s, ','); comma >= 0 {
			s = s[:comma]
		}
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimSpace(fields[0])
}

// imgNodeToItem 把 <img> 元素转成图片渲染项；overrideURL 非空时优先使用。
func imgNodeToItem(img *html.Node, overrideURL string) (markdownImageItem, bool) {
	url := strings.TrimSpace(overrideURL)
	if url == "" {
		src, _ := htmlAttr(img, "src")
		url = strings.TrimSpace(src)
	}
	if url == "" {
		return markdownImageItem{}, false
	}
	alt, _ := htmlAttr(img, "alt")
	title, _ := htmlAttr(img, "title")
	return markdownImageItem{
		url:   url,
		alt:   strings.TrimSpace(alt),
		title: strings.TrimSpace(title),
	}, true
}

// htmlAttr 读取 HTML 节点指定属性的值（属性名大小写不敏感）。
func htmlAttr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val, true
		}
	}
	return "", false
}

// isHTMLCommentOnly 判断 HTML 片段是否只由注释和空白构成。
func isHTMLCommentOnly(raw string) bool {
	nodes := parseHTMLFragmentNodes(raw)
	if len(nodes) == 0 {
		return false
	}
	hasComment := false
	for _, n := range nodes {
		switch n.Type {
		case html.CommentNode:
			hasComment = true
		case html.TextNode:
			if strings.TrimSpace(n.Data) != "" {
				return false
			}
		default:
			return false
		}
	}
	return hasComment
}

// extractInlineHTMLImageItems 从段落里合并出的连续行内 HTML 片段中提取图片（覆盖单行 <picture>、
// <p align="center"><img></p> 与多个 <img> 拼接）。若片段含非空白正文或非注释/换行的无图标签则返回 false。
func extractInlineHTMLImageItems(raw string, dark bool) ([]markdownImageItem, bool) {
	nodes := parseHTMLFragmentNodes(raw)
	if len(nodes) == 0 {
		return nil, false
	}
	if !hasNoNonWhitespaceTextNodes(nodes) {
		return nil, false
	}
	var items []markdownImageItem
	for _, n := range nodes {
		collectHTMLImageItems(n, dark, &items)
	}
	if len(items) > 0 {
		return items, true
	}
	return nil, isHTMLCommentOrBreakOnly(nodes)
}

func hasNoNonWhitespaceTextNodes(nodes []*html.Node) bool {
	var check func(n *html.Node) bool
	check = func(n *html.Node) bool {
		if n == nil {
			return true
		}
		if n.Type == html.TextNode && strings.TrimSpace(n.Data) != "" {
			return false
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if !check(ch) {
				return false
			}
		}
		return true
	}
	for _, n := range nodes {
		if !check(n) {
			return false
		}
	}
	return true
}

func isHTMLCommentOrBreakOnly(nodes []*html.Node) bool {
	for _, n := range nodes {
		switch n.Type {
		case html.CommentNode:
		case html.TextNode:
			if strings.TrimSpace(n.Data) != "" {
				return false
			}
		case html.ElementNode:
			if !strings.EqualFold(n.Data, "br") {
				return false
			}
		default:
			return false
		}
	}
	return true
}
