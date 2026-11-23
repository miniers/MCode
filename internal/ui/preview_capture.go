package mcodeui

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"math"
	"net/url"
	"strings"
	"time"
)

const (
	previewCaptureMaxPixels = 16_000_000
	previewCaptureMaxBytes  = 20 << 20
)

func boundPreviewPNG(data []byte) ([]byte, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("截图不是有效 PNG: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, fmt.Errorf("截图尺寸无效")
	}
	pixels := int64(cfg.Width) * int64(cfg.Height)
	if pixels <= previewCaptureMaxPixels && len(data) <= previewCaptureMaxBytes {
		return data, nil
	}
	scale := 1.0
	if pixels > previewCaptureMaxPixels {
		scale = min(scale, float64(previewCaptureMaxPixels)/float64(pixels))
	}
	if len(data) > previewCaptureMaxBytes {
		scale = min(scale, float64(previewCaptureMaxBytes)/float64(len(data)))
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("无法缩小截图: %w", err)
	}
	w, h := max(1, int(float64(cfg.Width)*scale)), max(1, int(float64(cfg.Height)*scale))
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out.Set(x, y, img.At(x*cfg.Width/w, y*cfg.Height/h))
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, out); err != nil {
		return nil, err
	}
	if int64(w)*int64(h) > previewCaptureMaxPixels || b.Len() > previewCaptureMaxBytes {
		return nil, fmt.Errorf("截图超过大小上限")
	}
	return b.Bytes(), nil
}

// previewCropPNG crops a viewport PNG by CSS coordinates and optionally scales its longest edge to maxEdge.
func previewCropPNG(data []byte, viewportW, x, y, w, h float64, maxEdge int) ([]byte, error) {
	if !(viewportW > 0) || math.IsInf(viewportW, 0) {
		return nil, fmt.Errorf("视口宽度无效")
	}
	if !(w > 0 && h > 0) || math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) || math.IsInf(w, 0) || math.IsInf(h, 0) {
		return nil, fmt.Errorf("裁切区域无效")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("截图不是有效 PNG: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, fmt.Errorf("截图尺寸无效")
	}
	if int64(cfg.Width)*int64(cfg.Height) > previewCaptureMaxPixels {
		return nil, fmt.Errorf("截图超过像素上限")
	}

	scale := float64(cfg.Width) / viewportW
	fx0 := math.Max(0, math.Round(x*scale))
	fy0 := math.Max(0, math.Round(y*scale))
	fx1 := math.Min(float64(cfg.Width), math.Round((x+w)*scale))
	fy1 := math.Min(float64(cfg.Height), math.Round((y+h)*scale))
	if fx1 <= fx0 || fy1 <= fy0 {
		return nil, fmt.Errorf("裁切区域超出图片范围")
	}
	cropRect := image.Rect(int(fx0), int(fy0), int(fx1), int(fy1))

	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("无法解码截图: %w", err)
	}

	cropW, cropH := cropRect.Dx(), cropRect.Dy()
	dstW, dstH := cropW, cropH
	if maxEdge > 0 && max(cropW, cropH) > maxEdge {
		if cropW >= cropH {
			dstW = maxEdge
			dstH = max(1, int(math.Round(float64(cropH)*float64(maxEdge)/float64(cropW))))
		} else {
			dstH = maxEdge
			dstW = max(1, int(math.Round(float64(cropW)*float64(maxEdge)/float64(cropH))))
		}
	}

	srcMin := img.Bounds().Min.Add(cropRect.Min)
	out := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	for dy := 0; dy < dstH; dy++ {
		sy := srcMin.Y + dy*cropH/dstH
		for dx := 0; dx < dstW; dx++ {
			sx := srcMin.X + dx*cropW/dstW
			out.Set(dx, dy, img.At(sx, sy))
		}
	}

	var b bytes.Buffer
	if err := png.Encode(&b, out); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func previewCaptureName(rawURL string, now time.Time) string {
	host := "页面"
	if u, err := url.Parse(rawURL); err == nil && u.Hostname() != "" {
		host = strings.NewReplacer(":", "-", "/", "-").Replace(u.Hostname())
	}
	return fmt.Sprintf("%s-%s.png", host, now.Format("20060102-150405"))
}

func previewCaptureValid(s *chatState, token previewPageToken, threadID string, page previewPage) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	selected := s.selectedThreadID
	s.mu.Unlock()
	previewMu.Lock()
	defer previewMu.Unlock()
	st := previewStateForTokenLocked(s, token)
	return selected == threadID && st != nil && st.tabID == token.state.tabID && st.visible && st.page == page
}

func previewCaptureScreenshot(s *chatState, st *browserTab) {
	if s == nil || st == nil {
		return
	}
	previewMu.Lock()
	page, token, threadID, rawURL := st.page, previewPageTokenLocked(st), st.threadID, st.url
	active := st.visible && st.tabID != 0 && st.owner != nil && st.owner.activeTabID == st.tabID
	previewMu.Unlock()
	capture, ok := page.(previewCapturePage)
	if !active || !ok {
		s.setToast("截图失败：页面未就绪")
		return
	}
	go func() {
		data, err := capture.CapturePage()
		if err == nil {
			data, err = boundPreviewPNG(data)
		}
		if err != nil || !previewCaptureValid(s, token, threadID, page) {
			s.setToast("截图失败：页面已变化或截图无效")
			return
		}
		if !s.addAttachmentToThread(threadID, previewCaptureName(rawURL, time.Now()), "image/png", data, nil) {
			s.setToast("截图失败：无法添加附件")
			return
		}
		s.setToast("截图已添加")
	}()
}
