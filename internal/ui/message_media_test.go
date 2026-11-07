package mcodeui

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// fakeMediaReader 只实现 messageMediaReader；嵌入的 Client 为 nil，仅用来满足赋给
// chatState.client 的接口约束，其余方法在测试里不会被调用。
type fakeMediaReader struct {
	Client
	err   error
	mime  string
	data  []byte
	count atomic.Int32
}

// ReadMessageMedia 记录调用次数并返回预置的结果。
func (f *fakeMediaReader) ReadMessageMedia(ctx context.Context, threadID, messageID, mediaID, variant string) (string, []byte, error) {
	f.count.Add(1)
	return f.mime, f.data, f.err
}

// waitForMediaState 轮询等待某条消息媒体进入指定加载阶段。
func waitForMediaState(t *testing.T, threadID, messageID, mediaID string, want messageMediaLoadState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if state, _ := messageMediaStatus(threadID, messageID, mediaID, "thumb"); state == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	got, errText := messageMediaStatus(threadID, messageID, mediaID, "thumb")
	t.Fatalf("消息媒体状态未变成 %v，当前 %v（%s）", want, got, errText)
}

// TestMessageMediaStatusLifecycle 验证读取失败后状态从加载中变为失败，
// 重试后清掉失败缓存回到加载中，且失败期间不会反复请求。
func TestMessageMediaStatusLifecycle(t *testing.T) {
	const (
		threadID  = "t-media-state"
		messageID = "m-media-state"
		mediaID   = "media-state"
	)
	key := messageMediaKey(threadID, messageID, mediaID, "thumb")
	messageMediaCache.Delete(key)
	t.Cleanup(func() { messageMediaCache.Delete(key) })

	if state, _ := messageMediaStatus(threadID, messageID, mediaID, "thumb"); state != messageMediaLoading {
		t.Fatalf("没有缓存条目时应为加载中，实际 %v", state)
	}

	reader := &fakeMediaReader{err: errors.New("网络中断")}
	s := &chatState{client: reader}
	prefetchMessageMedia(ui.Services{}, s, threadID, messageID, mediaID, "thumb")
	waitForMediaState(t, threadID, messageID, mediaID, messageMediaFailed)

	if state, errText := messageMediaStatus(threadID, messageID, mediaID, "thumb"); state != messageMediaFailed || !strings.Contains(errText, "网络中断") {
		t.Fatalf("失败态应带上错误文本，实际 state=%v err=%q", state, errText)
	}

	// 失败后保持缓存，重复 prefetch 不应再发起读取。
	prefetchMessageMedia(ui.Services{}, s, threadID, messageID, mediaID, "thumb")
	if got := reader.count.Load(); got != 1 {
		t.Fatalf("失败后不应重试，读取次数 = %d", got)
	}

	retryMessageMedia(ui.Services{}, threadID, messageID, mediaID, "thumb")
	if state, _ := messageMediaStatus(threadID, messageID, mediaID, "thumb"); state != messageMediaLoading {
		t.Fatalf("重试后应回到加载中，实际 %v", state)
	}
}

// TestMessageMediaDecodeFailure 验证读到了字节但解不出图片时也记为失败，而不是一直停在加载中。
func TestMessageMediaDecodeFailure(t *testing.T) {
	const (
		threadID  = "t-media-decode"
		messageID = "m-media-decode"
		mediaID   = "media-decode"
	)
	key := messageMediaKey(threadID, messageID, mediaID, "thumb")
	messageMediaCache.Delete(key)
	t.Cleanup(func() { messageMediaCache.Delete(key) })

	reader := &fakeMediaReader{mime: "image/png", data: []byte("not an image")}
	s := &chatState{client: reader}
	prefetchMessageMedia(ui.Services{}, s, threadID, messageID, mediaID, "thumb")
	waitForMediaState(t, threadID, messageID, mediaID, messageMediaFailed)

	if errText := mustMediaErrorText(t, threadID, messageID, mediaID); errText == "" {
		t.Fatalf("解码失败应记录错误文本")
	}
}

// mustMediaErrorText 读取失败态的简短错误文本。
func mustMediaErrorText(t *testing.T, threadID, messageID, mediaID string) string {
	t.Helper()
	state, errText := messageMediaStatus(threadID, messageID, mediaID, "thumb")
	if state != messageMediaFailed {
		t.Fatalf("期望失败态，实际 %v", state)
	}
	return errText
}
