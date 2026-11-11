package mcodeui

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"path"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// fileManagerSVGMaxBytes 是 SVG 内联预览的上限：超过就只显示源码。
const fileManagerSVGMaxBytes int64 = 1 << 20

// fileManagerPreviewTimeout 覆盖本地内容服务签发 URL 的本地调用，避免界面无限等待。
const fileManagerPreviewTimeout = 5 * time.Second

// inspectorPreviewClient 是 UI 可选依赖：客户端不支持时，PDF/HTML 继续走普通文件预览。
type inspectorPreviewClient interface {
	InspectorPreviewURL(ctx context.Context, threadID, relPath string) (string, error)
	RevokeInspectorPreviews(threadID string)
}

func isInspectorPDFPath(relPath string) bool {
	return strings.EqualFold(path.Ext(relPath), ".pdf")
}

func isInspectorHTMLPath(relPath string) bool {
	switch strings.ToLower(path.Ext(relPath)) {
	case ".html", ".htm", ".xhtml":
		return true
	default:
		return false
	}
}

func isInspectorSVGPath(relPath string) bool {
	return strings.EqualFold(path.Ext(relPath), ".svg")
}

func inspectorPreviewAvailable(s *chatState) bool {
	if s == nil || s.client == nil {
		return false
	}
	_, ok := s.client.(inspectorPreviewClient)
	return ok
}

// openInspectorPreview 请求后端签发本地内容 URL，并切到浏览器目的。
// 返回 false 表示客户端不支持或签发失败；调用方仍可按原有方式预览文件。
func (s *chatState) openInspectorPreview(threadID, filePath string) bool {
	if s == nil || s.client == nil || threadID == "" {
		return false
	}
	client, ok := s.client.(inspectorPreviewClient)
	if !ok {
		return false
	}
	active := getPreviewState(s)
	if active == nil {
		return false
	}
	previewMu.Lock()
	var existingID uint64
	for _, tab := range active.owner.tabs {
		if tab.localPreview && tab.previewThreadID == threadID && tab.localPath == filePath {
			existingID = tab.tabID
			break
		}
	}
	previewMu.Unlock()
	if existingID != 0 {
		browserActivateTab(s, active.owner, existingID)
		inspectorSelect(s, sidePanePreviewID, false)
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), fileManagerPreviewTimeout)
	defer cancel()
	target, err := client.InspectorPreviewURL(ctx, threadID, filePath)
	if err != nil {
		s.setToast(err.Error())
		return false
	}
	tab := browserNewTab(s)
	openPreviewLocalURL(s, threadID, target)
	previewMu.Lock()
	tab.localPath, tab.title = filePath, path.Base(filePath)
	previewMu.Unlock()
	return true
}

// renderFileManagerBrowserOpenButton 渲染文件头的「在浏览器打开」图标按钮。
func renderFileManagerBrowserOpenButton(c *ui.Context, s *chatState, tab fileManagerTabSnapshot, keyPrefix string) {
	btn := ui.ButtonBase(c.Key(keyPrefix+"file-manager-open-browser")).
		Label("在浏览器打开").
		Tooltip("在浏览器打开").
		Size(ControlSM, ControlSM).
		Radius(RadiusSM).
		Center().
		Shrink(0)
	if btn.Hovered() {
		btn.Background(ThemeHover(c))
	}
	if btn.Clicked() {
		if threadID := fileManagerThreadID(s); threadID != "" {
			s.openInspectorPreview(threadID, tab.path)
		}
	}
	btn.Children(func() {
		ui.Icon(c, IconExternalLink()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
	})
}

// fileManagerSVGForContent 只在 SVG 完整读入且未超上限时解析；解析失败或含外链一律返回 nil。
func fileManagerSVGForContent(filePath, content string, totalSize int64, hasMore bool) *ui.SVG {
	if !isInspectorSVGPath(filePath) || hasMore || content == "" {
		return nil
	}
	if totalSize > fileManagerSVGMaxBytes || int64(len(content)) > fileManagerSVGMaxBytes {
		return nil
	}
	data := []byte(content)
	if fileManagerSVGHasExternalReference(data) {
		return nil
	}
	svg, err := ui.ParseSVG(data)
	if err != nil {
		return nil
	}
	return svg
}

// fileManagerSVGHasExternalReference 拒绝 href / xlink:href 指向 http、https、file 或 // 的文档。
// XML 解析失败也按外链处理：宁可回退源码，也不把无法确认的内容交给渲染器。
func fileManagerSVGHasExternalReference(data []byte) bool {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return false
		}
		if err != nil {
			return true
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		for _, attr := range start.Attr {
			if !strings.EqualFold(attr.Name.Local, "href") {
				continue
			}
			value := strings.TrimSpace(attr.Value)
			lower := strings.ToLower(value)
			if strings.HasPrefix(lower, "http://") ||
				strings.HasPrefix(lower, "https://") ||
				strings.HasPrefix(lower, "file:") ||
				strings.HasPrefix(value, "//") {
				return true
			}
		}
	}
}

// renderFileManagerSVGPreview 用 mygo 的 SVG 绘制能力显示矢量图，按栏宽等比缩放。
func renderFileManagerSVGPreview(c *ui.Context, tab fileManagerTabSnapshot, keyPrefix string) {
	if tab.svg == nil {
		return
	}
	ui.Column(c.Key(keyPrefix + "file-manager-svg-col")).
		Fill().
		Center().
		Padding(Space3).
		Children(func() {
			img := ui.Image(c.Key(keyPrefix+"file-manager-svg"), tab.svg).
				MaxWidthPercent(100).
				MaxHeightPercent(100)
			if w, h := tab.svg.Size(); w > 0 && h > 0 {
				img.AspectRatio(w / h)
			}
		})
}

// renderFileManagerSVGToggle 渲染 SVG 的「预览/源码」切换，与 Markdown 的切换同构。
func renderFileManagerSVGToggle(c *ui.Context, s *chatState, tab fileManagerTabSnapshot, keyPrefix string) {
	ui.Row(c.Key(keyPrefix+"file-manager-svg-toggle")).
		Radius(RadiusXS).
		Background(ThemeHover(c)).
		Padding(Hairline, Hairline).
		Shrink(0).
		Children(func() {
			renderFileManagerSVGTab(c, s, tab, keyPrefix, "预览", true, tab.svgPreview)
			renderFileManagerSVGTab(c, s, tab, keyPrefix, "源码", false, tab.svgPreview)
		})
}

func renderFileManagerSVGTab(c *ui.Context, s *chatState, tab fileManagerTabSnapshot, keyPrefix, label string, previewMode, current bool) {
	btn := ui.ButtonBase(c.Key(keyPrefix+"file-manager-svg-tab-"+label)).
		Label(label).
		Radius(RadiusXS).
		Padding(Space0, Space2)
	if previewMode == current {
		btn.Background(ThemeSurface(c))
	} else if btn.Hovered() {
		btn.Background(ThemeHover(c))
	}
	if btn.Clicked() {
		s.setFileManagerTabSVGPreview(tab.path, previewMode)
	}
	btn.Children(func() {
		color := ThemeSecondaryText(c)
		if previewMode == current {
			color = ThemePrimaryText(c)
		}
		ui.Text(c, label).FontSize(TypeCaption).TextColor(color)
	})
}
