package mcodeui

import (
	"context"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

// messageMediaReader 声明按需读取消息媒体（thumb 缩略图或 full 原图）的可选客户端接口。
type messageMediaReader interface {
	ReadMessageMedia(ctx context.Context, threadID, messageID, mediaID, variant string) (string, []byte, error)
}

// messageMediaRef 指向一条消息媒体，供大图浮层在缩略图之后换取原图。
type messageMediaRef struct {
	threadID  string
	messageID string
	mediaID   string
}

// messageMediaLoadState 是一条消息媒体的加载阶段，供调用方区分加载中与失败。
type messageMediaLoadState int

const (
	// messageMediaLoading 表示请求已发出，尚不知道成败（缓存条目刚创建时的零值）。
	messageMediaLoading messageMediaLoadState = iota
	// messageMediaReady 表示已解码出可绘制的图片源。
	messageMediaReady
	// messageMediaFailed 表示读取或解码失败，errText 记录了原因。
	messageMediaFailed
)

// messageMediaEntry 是一条消息媒体的加载状态，异步加载完成后由重绘读取。
// state 为 messageMediaFailed 时 src 保持 nil，errText 给出简短原因，不再自动重试；
// 只有调用 retryMessageMedia 清掉缓存条目后才会重新加载。
type messageMediaEntry struct {
	mu      sync.Mutex
	state   messageMediaLoadState
	src     ui.ImageSource
	errText string
}

// messageMediaCache 按「会话/消息/媒体/变体」缓存加载状态，避免每帧重新请求与解码。
var messageMediaCache sync.Map // key → *messageMediaEntry

// messageMediaKey 拼接缓存键，用 NUL 分隔避免字段互相粘连。
func messageMediaKey(threadID, messageID, mediaID, variant string) string {
	return threadID + "\x00" + messageID + "\x00" + mediaID + "\x00" + variant
}

// selectedThreadIDSnapshot 按现有加锁约定读取当前会话 ID。
func selectedThreadIDSnapshot(s *chatState) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectedThreadID
}

// cachedMessageMedia 返回已加载的消息媒体图片，首次调用时在独立 goroutine 中开始加载，
// 加载完成后触发重绘（与 cachedPreviewImage 的写法一致）。客户端不支持该接口、
// 会话/消息/媒体 ID 缺失时返回 nil。
func cachedMessageMedia(c *ui.Context, s *chatState, threadID, messageID, mediaID, variant string) ui.ImageSource {
	if c == nil || s == nil || threadID == "" || messageID == "" || mediaID == "" {
		return nil
	}
	// 先确保已开始加载，再读当前状态：缓存命中时 prefetchMessageMedia 立即返回。
	prefetchMessageMedia(c.Services(), s, threadID, messageID, mediaID, variant)
	v, ok := messageMediaCache.Load(messageMediaKey(threadID, messageID, mediaID, variant))
	if !ok {
		return nil
	}
	e := v.(*messageMediaEntry)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.src
}

// messageMediaStatus 返回一条消息媒体当前的加载阶段与失败原因，只读缓存、
// 不会发起新的加载；还没有缓存条目时按「加载中」返回。
func messageMediaStatus(threadID, messageID, mediaID, variant string) (messageMediaLoadState, string) {
	v, ok := messageMediaCache.Load(messageMediaKey(threadID, messageID, mediaID, variant))
	if !ok {
		return messageMediaLoading, ""
	}
	e := v.(*messageMediaEntry)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state, e.errText
}

// retryMessageMedia 丢弃一条处于失败状态的缓存条目并触发重绘，
// 让下一帧的 cachedMessageMedia 重新发起加载；未失败时什么都不做。
func retryMessageMedia(services ui.Services, threadID, messageID, mediaID, variant string) {
	if threadID == "" || messageID == "" || mediaID == "" {
		return
	}
	key := messageMediaKey(threadID, messageID, mediaID, variant)
	if v, ok := messageMediaCache.Load(key); ok {
		e := v.(*messageMediaEntry)
		e.mu.Lock()
		failed := e.state == messageMediaFailed
		e.mu.Unlock()
		if !failed {
			return
		}
		messageMediaCache.Delete(key)
	}
	services.Invalidate()
}

// prefetchMessageMedia 在后台读取并解码一条消息媒体，完成后触发重绘。
// 已有缓存条目时不重复请求；失败时在缓存里记下失败原因，避免反复重试。
func prefetchMessageMedia(services ui.Services, s *chatState, threadID, messageID, mediaID, variant string) {
	if s == nil || threadID == "" || messageID == "" || mediaID == "" {
		return
	}
	reader, ok := s.client.(messageMediaReader)
	if !ok {
		return
	}
	key := messageMediaKey(threadID, messageID, mediaID, variant)
	v, loaded := messageMediaCache.LoadOrStore(key, &messageMediaEntry{})
	if loaded {
		return
	}
	e := v.(*messageMediaEntry)
	go func() {
		mimeType, data, err := reader.ReadMessageMedia(context.Background(), threadID, messageID, mediaID, variant)
		var src ui.ImageSource
		errText := ""
		switch {
		case err != nil:
			errText = shortMediaError(err)
		default:
			src = decodeMessageMedia(mimeType, data, variant)
			if src == nil {
				errText = "无法解码图片"
			}
		}
		e.mu.Lock()
		e.src = src
		if errText == "" {
			e.state = messageMediaReady
			e.errText = ""
		} else {
			e.state = messageMediaFailed
			e.errText = errText
		}
		e.mu.Unlock()
		services.Invalidate()
	}()
}

// shortMediaError 把加载错误压缩成适合放进占位卡提示的一行文本。
func shortMediaError(err error) string {
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return "加载失败"
	}
	if len([]rune(text)) > 60 {
		runes := []rune(text)
		text = string(runes[:60]) + "…"
	}
	return text
}

// decodeMessageMedia 把服务端返回的字节解码成图片源：thumb 走附件缩略图的解码路径，
// full 用不缩放的位图解码。mime 缺失或不受支持时按字节嗅探，仍不认识就按 JPEG 处理
// （底层的 image.Decode 会自行识别格式）。
func decodeMessageMedia(mimeType string, data []byte, variant string) ui.ImageSource {
	if len(data) == 0 {
		return nil
	}
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if !isAllowedImageMime(mimeType) {
		if detected := detectImageMime("", data); detected != "" {
			mimeType = detected
		} else {
			mimeType = "image/jpeg"
		}
	}
	if variant == "full" {
		// 原图不受附件缩略图的解码上限约束，直接解码。
		bmp, err := ui.DecodeBitmap(data)
		if err != nil || bmp == nil {
			return nil
		}
		return bmp
	}
	// 注意不能直接返回 *ui.Bitmap 的 nil，否则接口值非 nil，调用方会误以为已加载。
	thumb := attachmentThumbnail(mimeType, data)
	if thumb == nil {
		return nil
	}
	return thumb
}
