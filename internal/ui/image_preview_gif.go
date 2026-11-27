package mcodeui

import (
	"bytes"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
)

// isPreviewGIF 判断本地图片是否为 GIF：支持扩展名与 GIF87a/GIF89a 文件头嗅探。
func isPreviewGIF(ext string, data []byte) bool {
	if strings.EqualFold(ext, ".gif") {
		return true
	}
	return bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a"))
}

// decodePreviewLocalImage 读取并解码本地图片供图片预览缓存使用：
// 普通图片走静态解码；GIF 额外检查帧数，仅多帧 GIF 保留原始字节以便按需创建 VideoPlayer。
// 若静态解码失败但 NewGIFDecoder 可容错解码首帧，则用首帧位图兜底。
func decodePreviewLocalImage(url string) (src ui.ImageSource, gifData []byte, err error) {
	path, data, err := loadLocalImageData(url)
	if err != nil {
		return nil, nil, err
	}
	ext := filepath.Ext(path)
	if strings.EqualFold(ext, ".svg") {
		svg, parseErr := ui.ParseSVG(data)
		if parseErr != nil || svg == nil {
			return nil, nil, parseErr
		}
		return svg, nil, nil
	}
	bmp, decErr := ui.DecodeBitmap(data)
	if decErr == nil && bmp != nil {
		src = bmp
	} else {
		err = decErr
	}
	if isPreviewGIF(ext, data) {
		first, animated := inspectPreviewGIF(data, src == nil)
		if src == nil && first != nil {
			src = first
			err = nil
		}
		if animated {
			gifData = data
		}
	}
	return src, gifData, err
}

// inspectPreviewGIF 用 NewGIFDecoder 检查 GIF 是否包含多帧；单帧或解码失败时返回 animated=false。
// needFirst 为 true 时同时提取首帧位图，供静态解码失败的截断 GIF 兜底。
func inspectPreviewGIF(data []byte, needFirst bool) (first *ui.Bitmap, animated bool) {
	dec, err := ui.NewGIFDecoder(data)
	if err != nil {
		return nil, false
	}
	f1, err := dec.Next()
	if err != nil || f1.Image == nil {
		return nil, false
	}
	if needFirst {
		first = ui.NewBitmap(f1.Image)
	}
	if _, err := dec.Next(); err != nil {
		return first, false
	}
	return first, true
}

// newPreviewGIFPlayer 为多帧 GIF 创建循环播放的 VideoPlayer。
func newPreviewGIFPlayer(data []byte) (*ui.VideoPlayer, error) {
	dec, err := ui.NewGIFDecoder(data)
	if err != nil {
		return nil, err
	}
	player, err := ui.NewVideoPlayer(dec)
	if err != nil {
		return nil, err
	}
	player.SetLoop(true)
	return player, nil
}

// closeGIFPlayer 关闭并清空预览状态持有的 GIF 播放器。
func (pv *imagePreviewState) closeGIFPlayer() {
	if pv == nil || pv.gifPlayer == nil {
		return
	}
	p := pv.gifPlayer
	pv.gifPlayer = nil
	p.Close()
}

// setImagePreview 切换当前大图预览状态，并关闭上一个预览持有的 GIF 播放器。
func setImagePreview(s *chatState, next *imagePreviewState) {
	if s == nil {
		if next != nil {
			next.closeGIFPlayer()
		}
		return
	}
	s.mu.Lock()
	prev := s.imagePreview
	s.imagePreview = next
	s.mu.Unlock()
	if prev != nil && prev != next {
		prev.closeGIFPlayer()
	}
	s.invalidate()
}

// clearPreviewGIFPlayer 在不再显示动图（如开启减少动效）时关闭当前预览的播放器，
// 同时重置 gifAttempted 以便关闭减少动效后重新创建。
func clearPreviewGIFPlayer(s *chatState, pv *imagePreviewState) {
	if s == nil || pv == nil {
		return
	}
	s.mu.Lock()
	var p *ui.VideoPlayer
	if s.imagePreview == pv && pv.gifPlayer != nil {
		p = pv.gifPlayer
		pv.gifPlayer = nil
		pv.gifAttempted = false
	}
	s.mu.Unlock()
	if p != nil {
		p.Close()
	}
}

// cachedPreviewGIFEntry 确保 url 已发起异步加载并返回当前缓存的静态图源、多帧 GIF 字节与完成标志。
func cachedPreviewGIFEntry(c *ui.Context, url string) (src ui.ImageSource, gifData []byte, done bool) {
	e := ensurePreviewImageEntry(c, url)
	if e == nil {
		return nil, nil, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	done = e.done || e.src != nil || e.err != nil
	return e.src, e.gifData, done
}

// previewImageMedia 返回大图浮层应显示的静态图源与（非减少动效且为多帧 GIF 时的）播放器。
// 播放器在图片加载完成后创建一次并缓存在 imagePreviewState 中，不在渲染函数里做文件 IO。
func previewImageMedia(c *ui.Context, s *chatState, pv, snap *imagePreviewState) (ui.ImageSource, *ui.VideoPlayer) {
	src := previewImageSource(c, s, snap)
	if s == nil || pv == nil || snap == nil {
		return src, nil
	}
	if snap.full != nil || snap.url == "" || reduceMotion(c) {
		if snap.gifPlayer != nil {
			clearPreviewGIFPlayer(s, pv)
		}
		return src, nil
	}
	if snap.gifPlayer != nil {
		return src, snap.gifPlayer
	}
	if snap.gifAttempted {
		return src, nil
	}
	cachedSrc, gifData, done := cachedPreviewGIFEntry(c, snap.url)
	if src == nil {
		src = cachedSrc
	}
	if !done {
		return src, nil
	}
	if len(gifData) == 0 {
		s.mu.Lock()
		if s.imagePreview == pv {
			pv.gifAttempted = true
		}
		s.mu.Unlock()
		return src, nil
	}
	player, err := newPreviewGIFPlayer(gifData)
	if err != nil {
		s.mu.Lock()
		if s.imagePreview == pv {
			pv.gifAttempted = true
		}
		s.mu.Unlock()
		return src, nil
	}
	s.mu.Lock()
	if !s.closed && s.imagePreview == pv && pv.gifPlayer == nil {
		pv.gifAttempted = true
		pv.gifPlayer = player
		s.mu.Unlock()
		return src, player
	}
	var existing *ui.VideoPlayer
	if s.imagePreview == pv {
		existing = pv.gifPlayer
	}
	s.mu.Unlock()
	player.Close()
	return src, existing
}

// previewDisplaySize 计算大图浮层的显示尺寸：有播放器时取播放器帧尺寸，否则取静态图片尺寸。
func previewDisplaySize(maxW, maxH float32, src ui.ImageSource, player *ui.VideoPlayer) (float32, float32) {
	if player != nil {
		w, h := player.Size()
		return previewFitDimensions(maxW, maxH, float32(w), float32(h))
	}
	return previewFitSize(maxW, maxH, src)
}

// previewStageElement 根据是否存在播放器构造对应的显示元素（Video 或静态 Image）。
func previewStageElement(c *ui.Context, src ui.ImageSource, player *ui.VideoPlayer) ui.Element {
	if player != nil {
		return ui.Video(c.Key("image-preview-video"), player)
	}
	return ui.Image(c.Key("image-preview-image"), src)
}
