package mcodeui

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mcode/internal/fsutil"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// imagePreviewMaxBytes 是本地图片允许的最大字节数，超过后不加载。
const imagePreviewMaxBytes = 20 << 20

// imagePreviewExts 是允许预览的图片扩展名。
var imagePreviewExts = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".webp": true,
	".svg":  true,
}

// errImageUnsupported 表示地址不是可加载的本地图片。
var errImageUnsupported = errors.New("unsupported image address")

// 图片预览浮层的固定尺寸：翻页按钮边长与箭头字号。
const (
	imagePreviewNavSize      float32 = 32
	imagePreviewNavGlyphSize float32 = 20
)

// loadLocalImage 加载本地图片文件：只接受 file:// URL 或绝对路径，
// http、https、相对路径与其他地址一律返回错误，Markdown 会照旧显示替代文字。
func loadLocalImage(url string) (ui.ImageSource, error) {
	path, err := localImagePath(url)
	if err != nil {
		return nil, err
	}
	if !imagePreviewExts[strings.ToLower(filepath.Ext(path))] {
		return nil, errImageUnsupported
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() || info.Size() > imagePreviewMaxBytes {
		return nil, errImageUnsupported
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// SVG 与位图分开解码，与 markdown 包 FileImages 的做法一致。
	if strings.EqualFold(filepath.Ext(path), ".svg") {
		return ui.ParseSVG(data)
	}
	return ui.DecodeBitmap(data)
}

// localImagePath 把地址解析成本地绝对路径：file:// URL、绝对路径与 ~/ 开头的路径。
// 其他地址（含 http、https、相对路径与 data:）返回错误。
func localImagePath(url string) (string, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return "", errImageUnsupported
	}
	if strings.HasPrefix(url, "~"+string(filepath.Separator)) || strings.HasPrefix(url, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, filepath.FromSlash(url[2:])), nil
	}
	if strings.HasPrefix(strings.ToLower(url), "file://") {
		path, ok := fsutil.PathFromFileURL(url)
		if !ok || !filepath.IsAbs(path) {
			return "", errImageUnsupported
		}
		return path, nil
	}
	// 带其他 scheme 的地址（http、https、data 等）都不加载。
	if strings.Contains(url, "://") || strings.HasPrefix(strings.ToLower(url), "data:") {
		return "", errImageUnsupported
	}
	if !filepath.IsAbs(url) {
		return "", errImageUnsupported
	}
	return filepath.FromSlash(url), nil
}

// resolveMarkdownImage 把文档里的相对图片路径按基准目录解析成绝对路径：
// baseDir 非空、地址不含 scheme（://）、不以 data:/~/file: 开头且不是绝对路径时，
// 去掉 ?query 与 #fragment 后拼接到 baseDir；其余地址原样返回。
func resolveMarkdownImage(baseDir, rawURL string) string {
	baseDir = strings.TrimSpace(baseDir)
	url := strings.TrimSpace(rawURL)
	if baseDir == "" || url == "" {
		return rawURL
	}
	lower := strings.ToLower(url)
	if strings.Contains(url, "://") ||
		strings.HasPrefix(lower, "data:") ||
		strings.HasPrefix(lower, "file:") ||
		strings.HasPrefix(url, "~") ||
		strings.HasPrefix(url, "/") ||
		filepath.IsAbs(filepath.FromSlash(url)) {
		return rawURL
	}
	clean := url
	if i := strings.IndexAny(clean, "?#"); i >= 0 {
		clean = clean[:i]
	}
	if clean == "" {
		return rawURL
	}
	return filepath.Join(baseDir, filepath.FromSlash(clean))
}

// previewImageEntry 是一张图片的加载状态，异步加载完成后由重绘读取。
type previewImageEntry struct {
	mu  sync.Mutex
	src ui.ImageSource
	err error
}

// markdownImageItem 是图片段落或 HTML 图片块中要绘制的单张图片。
type markdownImageItem struct {
	url     string
	alt     string
	title   string
	pending bool
}

// imagePreviewCache 按 url 缓存图片加载状态，避免每帧重新解码。
var imagePreviewCache sync.Map // url → *previewImageEntry

// imagePreviewHooksInstalled 记录已装过钩子的 Markdown 选项，避免重复包装 Block。
var imagePreviewHooksInstalled sync.Map // *markdown.Options → struct{}

// cachedPreviewImage 返回已加载的图片，首次调用时在独立 goroutine 中开始加载，
// 加载完成后触发重绘（与 markdown 包 viewState.image 的写法一致）。
func cachedPreviewImage(c *ui.Context, url string) ui.ImageSource {
	if url == "" {
		return nil
	}
	v, loaded := imagePreviewCache.LoadOrStore(url, &previewImageEntry{})
	e := v.(*previewImageEntry)
	if !loaded {
		services := c.Services()
		go func() {
			src, err := loadLocalImage(url)
			e.mu.Lock()
			e.src = src
			e.err = err
			e.mu.Unlock()
			services.Invalidate()
		}()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.src
}

// installImagePreviewHooks 给 Markdown 选项装上本地图片加载与点击放大的块钩子。
// 选项原有的 Block 钩子会被包一层并优先调用，保持链式；每个选项只安装一次。
func installImagePreviewHooks(o *markdown.Options, s *chatState) {
	installImagePreviewHooksWithBaseDir(o, s, "")
}

// installImagePreviewHooksWithBaseDir 在 installImagePreviewHooks 基础上为相对图片路径绑定基准目录。
func installImagePreviewHooksWithBaseDir(o *markdown.Options, s *chatState, baseDir string) {
	if o == nil || s == nil {
		return
	}
	if _, done := imagePreviewHooksInstalled.LoadOrStore(o, struct{}{}); done {
		return
	}
	if o.LoadImage == nil {
		if baseDir == "" {
			o.LoadImage = loadLocalImage
		} else {
			o.LoadImage = func(url string) (ui.ImageSource, error) {
				return loadLocalImage(resolveMarkdownImage(baseDir, url))
			}
		}
	}
	o.Block = imagePreviewBlockHookWithBaseDir(s, baseDir, o.Block)
}

// imagePreviewBlockHook 把原 Block 钩子包一层：只有「整段仅含图片」且其中有可加载的本地图片
// （或含行内 <img> HTML）时才交给自己渲染，其余块先交给原钩子，仍不接管时返回 false 交给默认渲染。
func imagePreviewBlockHook(s *chatState, prev func(c *ui.Context, d *parser.Document, i int) bool) func(c *ui.Context, d *parser.Document, i int) bool {
	return imagePreviewBlockHookWithBaseDir(s, "", prev)
}

func imagePreviewBlockHookWithBaseDir(s *chatState, baseDir string, prev func(c *ui.Context, d *parser.Document, i int) bool) func(c *ui.Context, d *parser.Document, i int) bool {
	return func(c *ui.Context, d *parser.Document, i int) bool {
		if prev != nil && prev(c, d, i) {
			return true
		}
		if s == nil {
			return false
		}
		items, hasInlineHTML, ok := collectParagraphImages(d, i, baseDir, isDark(c))
		if !ok || len(items) == 0 {
			return false
		}
		// 预加载并检查是否至少有一张本地图片可用；若全是 Markdown 图片且暂无可用本地图片，交给默认渲染。
		if !hasLoadedImageItems(c, items) && !hasInlineHTML {
			return false
		}
		renderMarkdownImages(c, s, items)
		return true
	}
}

// hasLoadedImageItems 报告图片列表是否至少有一张已加载好，同时触发异步加载。
func hasLoadedImageItems(c *ui.Context, items []markdownImageItem) bool {
	loaded := false
	for _, it := range items {
		if it.pending {
			continue
		}
		if cachedPreviewImage(c, it.url) != nil {
			loaded = true
		}
	}
	return loaded
}

// collectParagraphImages 判断第 i 块是否整段只有图片（parser.Image、空白文本、换行与只含图片的
// 行内 HTML，含单行 <picture> 与 <img>），并按出现顺序提取解析后的图片列表与是否包含行内 HTML 图片。
func collectParagraphImages(d *parser.Document, i int, baseDir string, dark bool) (items []markdownImageItem, hasInlineHTML bool, ok bool) {
	if d == nil || i < 0 || i >= d.Len() || d.Block(i).Kind != parser.Paragraph {
		return nil, false, false
	}
	in := d.Inlines(i)
	if len(in) == 0 {
		return nil, false, false
	}
	var htmlBuf strings.Builder
	flushHTML := func() bool {
		if htmlBuf.Len() == 0 {
			return true
		}
		extracted, valid := extractInlineHTMLImageItems(htmlBuf.String(), dark)
		htmlBuf.Reset()
		if !valid {
			return false
		}
		if len(extracted) > 0 {
			hasInlineHTML = true
			for _, it := range extracted {
				it.url = resolveMarkdownImage(baseDir, it.url)
				items = append(items, it)
			}
		}
		return true
	}
	for k := 0; k < len(in); k = in[k].Next {
		n := &in[k]
		switch n.Kind {
		case parser.Image:
			if !flushHTML() {
				return nil, false, false
			}
			alt := parser.PlainText(in[k+1 : n.Next])
			items = append(items, markdownImageItem{
				url:     resolveMarkdownImage(baseDir, n.Dest),
				alt:     alt,
				title:   n.Title,
				pending: n.Pending,
			})
		case parser.SoftBreak, parser.LineBreak:
			if htmlBuf.Len() > 0 {
				htmlBuf.WriteByte('\n')
			}
		case parser.Text:
			if strings.TrimSpace(n.Text) != "" {
				return nil, false, false
			}
			if htmlBuf.Len() > 0 {
				htmlBuf.WriteString(n.Text)
			}
		case parser.HTML:
			if n.Pending {
				return nil, false, false
			}
			htmlBuf.WriteString(n.Text)
		default:
			return nil, false, false
		}
	}
	if !flushHTML() {
		return nil, false, false
	}
	return items, hasInlineHTML, len(items) > 0
}

// renderImageParagraph 渲染只有图片的段落：提取段落内图片后复用 renderMarkdownImages 绘制。
func renderImageParagraph(c *ui.Context, s *chatState, d *parser.Document, i int) {
	items, _, ok := collectParagraphImages(d, i, "", isDark(c))
	if !ok {
		return
	}
	renderMarkdownImages(c, s, items)
}

// renderMarkdownImages 渲染一组图片：与 markdown 包 images 一致（Wrap 的 Row、
// 最大宽度 100%、保持宽高比、未加载时显示替代文字），并给已加载的图片加上点击放大。
func renderMarkdownImages(c *ui.Context, s *chatState, items []markdownImageItem) {
	if len(items) == 0 {
		return
	}
	ui.Row(c).Wrap().Gap(Space2).AlignSelf(ui.Stretch).Children(func() {
		for _, item := range items {
			src := cachedPreviewImage(c, item.url)
			if src == nil || item.pending {
				label := item.alt
				if label == "" {
					label = "图片"
				}
				ph := ui.Row(c).Padding(Space1, Space2).Radius(RadiusXS).Background(ThemeOverlay(c, 0.06))
				ph.Role(ui.RoleImage).Label(item.alt)
				ph.Children(func() {
					ui.Text(c, label).TextColor(ThemeSecondaryText(c)).FontSize(uiFont(TypeBody))
				})
				continue
			}
			img := ui.Image(c, src).Radius(RadiusXS).MaxWidthPercent(100).Label(item.alt)
			var w, h float32
			switch v := src.(type) {
			case *ui.Bitmap:
				iw, ih := v.Size()
				w, h = float32(iw), float32(ih)
			case *ui.SVG:
				w, h = v.Size()
			}
			if w > 0 && h > 0 {
				img.AspectRatio(w / h)
			}
			if item.title != "" {
				img.Tooltip(item.title)
			}
			if s == nil {
				continue
			}
			// 点击看大图：记录图片来源、解析后地址与替代文字，再由重绘画出浮层。
			previewSrc, previewAlt, previewURL := src, item.alt, item.url
			img.Cursor(ui.CursorPointer).OnClick(func() {
				s.mu.Lock()
				s.imagePreview = &imagePreviewState{src: previewSrc, alt: previewAlt, url: previewURL}
				s.mu.Unlock()
				s.invalidate()
			})
		}
	})
}

// imagePreviewState 是正在放大的图片：图片源、替代文字与缩放平移状态。
// full 不为 nil 时表示这张图来自用户消息附件：原图加载好之后替换缩略图。
// url 是正文图片的原始地址（附件为空），画廊按它定位当前图片。
type imagePreviewState struct {
	src  ui.ImageSource
	alt  string
	url  string
	full *messageMediaRef

	// zoom 是 1x–8x 的缩放比例，零值按 1x 处理；offsetX/offsetY 是屏幕坐标系里的平移量。
	zoom    float32
	offsetX float32
	offsetY float32
}

// previewImageSource 返回浮层应当显示的图片源：原图已加载好时用原图，否则用当前图片。
func previewImageSource(c *ui.Context, s *chatState, pv *imagePreviewState) ui.ImageSource {
	if pv == nil {
		return nil
	}
	if pv.full != nil {
		// 首次调用会启动原图加载，加载完成后重绘再换成原图。
		if full := cachedMessageMedia(c, s, pv.full.threadID, pv.full.messageID, pv.full.mediaID, "full"); full != nil {
			return full
		}
	}
	if pv.src == nil && pv.url != "" {
		return cachedPreviewImage(c, pv.url)
	}
	return pv.src
}

func previewImageError(url string) error {
	v, ok := imagePreviewCache.Load(url)
	if !ok {
		return nil
	}
	e := v.(*previewImageEntry)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

// renderImagePreviewOverlay 渲染大图浮层：全窗口半透明遮罩、可缩放平移的图片、
// 翻页按钮与底部动作条。点击遮罩或按 Esc 关闭，方向键左右翻页。
func renderImagePreviewOverlay(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	s.mu.Lock()
	pv := s.imagePreview
	if pv == nil {
		s.mu.Unlock()
		return
	}
	// 缩放与平移会被输入回调在锁内改写，这里只读一份快照。
	snap := *pv
	snap.zoom = previewClampZoom(snap.zoom)
	s.mu.Unlock()

	src := previewImageSource(c, s, &snap)

	winW, winH := c.Size()
	maxW, maxH := winW*0.9, winH*0.9
	dispW, dispH := previewFitSize(maxW, maxH, src)

	// 画廊列表：从消息快照现算，输入框附件定位不到就不显示翻页。
	items := previewGalleryFor(s)
	index := previewGalleryIndex(items, &snap)
	hasGallery := len(items) > 1 && index >= 0

	goTo := func(i int) {
		if i < 0 || i >= len(items) {
			return
		}
		next := previewGalleryState(c, s, items[i])
		if next == nil {
			return
		}
		s.mu.Lock()
		s.imagePreview = next
		s.mu.Unlock()
		s.invalidate()
	}

	open := true
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		// 半透明遮罩铺满窗口；点击遮罩与 Esc 由 DialogBase 写回 open。
		backdrop.
			Absolute().
			Left(0).
			Top(0).
			Right(0).
			Bottom(0).
			Center().
			Background(imagePreviewScrimColor)

		// 面板不加外观，图片与说明文字直接居中显示。
		panel.Key("image-preview-panel").Padding(0).Gap(Space3)
		// 方向键翻页：用浮层快捷键，不管焦点在哪个元素上都能收到。
		if hasGallery {
			if panel.OverlayShortcut(0, ui.KeyLeft) {
				goTo(index - 1)
			}
			if panel.OverlayShortcut(0, ui.KeyRight) {
				goTo(index + 1)
			}
		}
		ui.Column(c.Key("image-preview-content")).
			Label("image-preview-content").
			AlignItems(ui.Center).
			Justify(ui.Center).
			Gap(Space3).
			Children(func() {
				if hasGallery {
					ui.Text(c.Key("image-preview-counter"), fmt.Sprintf("%d / %d", index+1, len(items))).
						FontSize(uiFont(TypeFootnote)).
						TextColor(imagePreviewMutedTextColor)
				}
				renderImagePreviewStage(c, s, &snap, src, dispW, dispH, index, len(items), goTo)
				if snap.alt != "" {
					ui.Text(c.Key("image-preview-caption"), snap.alt).
						MaxWidth(maxW).
						MaxLines(2).
						FontSize(uiFont(TypeBody)).
						TextColor(imagePreviewTextColor)
				}
				renderImagePreviewActions(c, s, &snap)
			})
	})
	// DialogBase 已经把遮罩点击与 Esc 写回 open，这里据此关闭。
	if !open {
		closeImagePreview(s)
	}
}

// renderImagePreviewStage 渲染图片区：滚轮与捏合缩放、双击切换 1x/2x、放大后拖动平移；
// 画廊有多张时在两侧叠加上一张与下一张按钮。
func renderImagePreviewStage(c *ui.Context, s *chatState, pv *imagePreviewState, src ui.ImageSource, dispW, dispH float32, index, total int, goTo func(int)) {
	stage := ui.Box(c.Key("image-preview-stage")).
		Width(dispW).
		Height(dispH).
		Clip().
		Center()
	stage.Children(func() {
		if src == nil {
			if pv.url != "" && previewImageError(pv.url) != nil {
				ui.Column(c.Key("image-preview-error")).AlignItems(ui.Center).Gap(Space3).Children(func() {
					ui.Text(c.Key("image-preview-error-text"), "图片加载失败").FontSize(uiFont(TypeBody)).TextColor(ThemeDanger(c))
					previewActionButton(c, "image-preview-close", "关闭", "关闭", func() { closeImagePreview(s) })
				})
				return
			}
			// 原图或缩略图还没加载好，先给一行提示，加载完成后由重绘换成图片。
			ui.Text(c.Key("image-preview-loading"), "图片加载中…").
				FontSize(uiFont(TypeBody)).
				TextColor(imagePreviewWeakTextColor)
			return
		}
		zoom := previewClampZoom(pv.zoom)
		img := ui.Image(c.Key("image-preview-image"), src).
			Width(dispW).
			Height(dispH).
			Label(pv.alt).
			TransformOrigin(0.5, 0.5).
			Scale(zoom, zoom).
			Translate(pv.offsetX, pv.offsetY).
			Cursor(ui.CursorPointer).
			// 空点击处理器让图片本身成为点击目标，点图片不会落到遮罩上关掉浮层。
			OnClick(func() {}).
			HandleInput(func(ev ui.InputEvent) bool {
				switch ev.Kind {
				case ui.InputZoom:
					if !(ev.Scale > 0) {
						return false
					}
					previewZoomStep(s, dispW, dispH, ev.Scale)
					return true
				case ui.InputScroll:
					// 只有按住 Cmd（或 Ctrl）时滚轮才是缩放，其余的滚动留给外层。
					if ev.Mods&(ui.Cmd|ui.Ctrl) == 0 {
						return false
					}
					factor := previewScrollZoomFactor(ev.DY, ev.Precise)
					if factor == 1 {
						return false
					}
					previewZoomStep(s, dispW, dispH, factor)
					return true
				}
				return false
			})
		if img.DoubleClicked() {
			// 双击在 1x 与 2x 之间切换。
			target := float32(2)
			if zoom > previewMinZoom {
				target = previewMinZoom
			}
			previewZoomStep(s, dispW, dispH, target/zoom)
		}
		if dx, dy, ok := img.Dragged(); ok && zoom > previewMinZoom {
			panPreviewImage(s, dispW, dispH, dx, dy)
		}
		if index > 0 {
			renderImagePreviewNavButton(c, "image-preview-prev", "‹", "上一张", true, dispH, func() {
				goTo(index - 1)
			})
		}
		if index >= 0 && index+1 < total {
			renderImagePreviewNavButton(c, "image-preview-next", "›", "下一张", false, dispH, func() {
				goTo(index + 1)
			})
		}
	})
}

// renderImagePreviewNavButton 在图片两侧渲染半透明的上一张/下一张按钮。
func renderImagePreviewNavButton(c *ui.Context, key, glyph, tip string, left bool, dispH float32, onClick func()) {
	top := dispH/2 - imagePreviewNavSize/2
	if top < 0 {
		top = 0
	}
	btn := ui.ButtonBase(c.Key(key)).
		Absolute().
		Top(top).
		Width(imagePreviewNavSize).
		Height(imagePreviewNavSize).
		Radius(RadiusPill).
		Center().
		Background(imagePreviewNavBgColor).
		Label(tip).
		Tooltip(tip).
		Cursor(ui.CursorPointer)
	if left {
		btn.Left(Space3)
	} else {
		btn.Right(Space3)
	}
	{
		scale := motionPressScale(c, btn, "button-press-scale", btn.Pressed())
		btn.Scale(scale, scale)
	}
	if btn.Hovered() {
		btn.Background(imagePreviewNavHoverBg)
	}
	btn.Children(func() {
		ui.Text(c, glyph).FontSize(imagePreviewNavGlyphSize).TextColor(imagePreviewTextColor)
	})
	btn.OnClick(onClick)
}

// renderImagePreviewActions 渲染底部动作条：在访达中显示、另存为、发送到对话。
// 输入框附件既没有本地路径也没有消息引用，拿不到原文件时整条不显示。
func renderImagePreviewActions(c *ui.Context, s *chatState, pv *imagePreviewState) {
	if pv == nil {
		return
	}
	localPath := previewLocalPath(pv)
	if localPath == "" && pv.full == nil {
		return
	}
	ui.Row(c.Key("image-preview-actions")).Gap(Space3).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
		if localPath != "" {
			previewActionButton(c, "image-preview-reveal", revealInFileManagerLabel(), revealInFileManagerLabel(), func() {
				openItemInFinder(localPath)
			})
		}
		previewActionButton(c, "image-preview-save", "另存为", "另存为…", func() {
			savePreviewImage(s, pv)
		})
		previewActionButton(c, "image-preview-send", "发送到对话", "", func() {
			sendPreviewToComposer(s, pv)
		})
	})
}

// previewActionButton 渲染动作条上的一枚小按钮。
func previewActionButton(c *ui.Context, key, label, tip string, onClick func()) {
	btn := ui.ButtonBase(c.Key(key)).
		Height(RowHeightCompact).
		Padding(0, Space3).
		Radius(RadiusSM).
		Center().
		Background(imagePreviewBtnBgColor).
		Label(label).
		Tooltip(tip).
		Cursor(ui.CursorPointer)
	{
		scale := motionPressScale(c, btn, "button-press-scale", btn.Pressed())
		btn.Scale(scale, scale)
	}
	if btn.Hovered() {
		btn.Background(imagePreviewBtnHoverBg)
	}
	btn.Children(func() {
		ui.Text(c, label).FontSize(uiFont(TypeFootnote)).TextColor(imagePreviewTextColor)
	})
	btn.OnClick(onClick)
}

// imageSourceSize 返回图片的原始宽高，未知时返回 0。
func imageSourceSize(src ui.ImageSource) (float32, float32) {
	switch v := src.(type) {
	case *ui.Bitmap:
		w, h := v.Size()
		return float32(w), float32(h)
	case *ui.SVG:
		return v.Size()
	}
	return 0, 0
}

// previewMinZoom 与 previewMaxZoom 是大图浮层的缩放范围。
const (
	previewMinZoom float32 = 1
	previewMaxZoom float32 = 8
)

// previewClampZoom 把缩放比例限制在 1x 到 8x；零值与无效值按 1x 处理。
func previewClampZoom(zoom float32) float32 {
	if !(zoom > previewMinZoom) {
		return previewMinZoom
	}
	if zoom > previewMaxZoom {
		return previewMaxZoom
	}
	return zoom
}

// previewFitSize 把图片等比缩放进最大可视区域，图片更小时保持原尺寸；
// 尺寸未知的图片直接占满最大可视区域。
func previewFitSize(maxW, maxH float32, src ui.ImageSource) (float32, float32) {
	natW, natH := imageSourceSize(src)
	if natW <= 0 || natH <= 0 || maxW <= 0 || maxH <= 0 {
		return maxW, maxH
	}
	scale := maxW / natW
	if s := maxH / natH; s < scale {
		scale = s
	}
	if scale > 1 {
		scale = 1
	}
	return natW * scale, natH * scale
}

// previewPanLimit 返回单轴允许的最大平移量：缩放后多出来的部分最多各分一半。
func previewPanLimit(viewport, zoom float32) float32 {
	zoom = previewClampZoom(zoom)
	if viewport <= 0 || zoom <= previewMinZoom {
		return 0
	}
	return viewport * (zoom - 1) / 2
}

// previewClampPanOffset 把单轴平移量夹到“图片始终盖住视口”的范围内，
// 缩放为 1x 时只能是 0。
func previewClampPanOffset(offset, viewport, zoom float32) float32 {
	limit := previewPanLimit(viewport, zoom)
	if offset > limit {
		return limit
	}
	if offset < -limit {
		return -limit
	}
	return offset
}

// previewScrollZoomFactor 把一次带修饰键的滚轮位移换算成缩放倍数：
// 触控板按像素微调，鼠标滚轮按刻度放大，向上滚动放大。
func previewScrollZoomFactor(dy float32, precise bool) float32 {
	if dy == 0 {
		return 1
	}
	base, unit := 1.15, float32(1)
	if precise {
		base, unit = 1.01, 10
	}
	return float32(math.Pow(base, float64(-dy/unit)))
}

// previewZoomStep 按倍数缩放浮层图片，平移量等比例调整后重新夹取；
// 缩到 1x 时平移归零。
func previewZoomStep(s *chatState, viewportW, viewportH, factor float32) {
	if s == nil || !(factor > 0) || factor == 1 {
		return
	}
	s.mu.Lock()
	pv := s.imagePreview
	if pv == nil {
		s.mu.Unlock()
		return
	}
	zoom := previewClampZoom(pv.zoom)
	target := previewClampZoom(zoom * factor)
	if target <= previewMinZoom {
		pv.zoom, pv.offsetX, pv.offsetY = previewMinZoom, 0, 0
	} else {
		pv.zoom = target
		pv.offsetX = previewClampPanOffset(pv.offsetX*target/zoom, viewportW, target)
		pv.offsetY = previewClampPanOffset(pv.offsetY*target/zoom, viewportH, target)
	}
	s.mu.Unlock()
	s.invalidate()
}

// panPreviewImage 按本地位移平移当前图片：缩放为 1x 时不动，偏移始终夹在视口内。
// 拖动位移是图片本地坐标，换算成屏幕位移要乘以缩放比例。
func panPreviewImage(s *chatState, viewportW, viewportH, dx, dy float32) {
	if s == nil || (dx == 0 && dy == 0) {
		return
	}
	s.mu.Lock()
	pv := s.imagePreview
	if pv == nil {
		s.mu.Unlock()
		return
	}
	zoom := previewClampZoom(pv.zoom)
	if zoom <= previewMinZoom {
		pv.zoom, pv.offsetX, pv.offsetY = previewMinZoom, 0, 0
		s.mu.Unlock()
		return
	}
	pv.offsetX = previewClampPanOffset(pv.offsetX+dx*zoom, viewportW, zoom)
	pv.offsetY = previewClampPanOffset(pv.offsetY+dy*zoom, viewportH, zoom)
	s.mu.Unlock()
	s.invalidate()
}

// closeImagePreview 关闭大图浮层并请求重绘。
func closeImagePreview(s *chatState) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.imagePreview = nil
	s.mu.Unlock()
	s.invalidate()
}
