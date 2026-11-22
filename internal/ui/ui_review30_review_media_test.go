package mcodeui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

func resetLocalFilePreviewCaches(t *testing.T) {
	t.Helper()
	clear := func() {
		localFilePreviewMu.Lock()
		localFilePreviewCache = localFilePreviewCacheEntry{}
		localFilePreviewMu.Unlock()
		localTextPreviewMu.Lock()
		localTextPreviewCache = map[string]localTextPreviewEntry{}
		localTextPreviewMu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

func TestUIReview30LocalFilePreviewCache(t *testing.T) {
	resetLocalFilePreviewCaches(t)

	t.Run("text", func(t *testing.T) {
		dir := t.TempDir()
		file1 := filepath.Join(dir, "first.go")
		file2 := filepath.Join(dir, "second.go")
		if err := os.WriteFile(file1, []byte("package main\nfunc FirstCachedLine() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file2, []byte("package main\nfunc SecondReloadedLine() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		dest := file1
		tt := ui.NewTester(func(c *ui.Context) {
			if fn := localFileLinkPreview(c, dest); fn != nil {
				fn(ui.Column(c))
			}
		}, 800, 600)
		tt.Frame()
		if !tt.HasText("func FirstCachedLine() {}") {
			t.Fatalf("首帧未渲染文本预览，texts=%v", tt.Texts())
		}

		if err := os.Remove(file1); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		tt.Frame()
		if !tt.HasText("func FirstCachedLine() {}") {
			t.Fatalf("删掉文件后第二帧起仍应返回缓存内容，texts=%v", tt.Texts())
		}

		dest = file2
		tt.Frame()
		if !tt.HasText("func SecondReloadedLine() {}") {
			t.Fatalf("切换路径后应重新加载新文件，texts=%v", tt.Texts())
		}
	})

	t.Run("image", func(t *testing.T) {
		dir := t.TempDir()
		imgPath := filepath.Join(dir, "thumb.png")
		missingImg := filepath.Join(dir, "missing.png")
		writeTestPNG(t, imgPath)

		dest := imgPath
		tt := ui.NewTester(func(c *ui.Context) {
			if fn := localFileLinkPreview(c, dest); fn != nil {
				fn(ui.Column(c))
			}
		}, 800, 600)
		tt.Frame()
		firstPix := slices.Clone(tt.Image().Pix)

		localFilePreviewMu.Lock()
		cachedSrc := localFilePreviewCache.src
		cachedErr := localFilePreviewCache.err
		localFilePreviewMu.Unlock()
		if cachedSrc == nil || cachedErr != nil {
			t.Fatalf("首帧图片未进入缓存: src=%v err=%v", cachedSrc, cachedErr)
		}

		if err := os.Remove(imgPath); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if !slices.Equal(tt.Image().Pix, firstPix) {
			t.Fatal("删掉图片文件后第二帧渲染结果发生变化，说明未命中图片缓存")
		}

		dest = missingImg
		tt.Frame()
		localFilePreviewMu.Lock()
		nextSrc := localFilePreviewCache.src
		nextErr := localFilePreviewCache.err
		localFilePreviewMu.Unlock()
		if nextSrc != nil || nextErr == nil {
			t.Fatalf("切换到不存在图片后应重新加载并缓存错误: src=%v err=%v", nextSrc, nextErr)
		}
	})
}

type blockingMediaReaderClient struct {
	*FakeClient
	started  chan struct{}
	timedOut atomic.Bool
}

func (c *blockingMediaReaderClient) ReadMessageMedia(ctx context.Context, _, _, _, _ string) (string, []byte, error) {
	select {
	case <-c.started:
	default:
		close(c.started)
	}
	<-ctx.Done()
	c.timedOut.Store(true)
	return "", nil, ctx.Err()
}

func TestUIReview30MediaReadTimeoutAndNonBlocking(t *testing.T) {
	origTimeout := previewMediaReadTimeout
	previewMediaReadTimeout = 60 * time.Millisecond
	t.Cleanup(func() { previewMediaReadTimeout = origTimeout })

	origSaveDialog := previewSaveDialog
	previewSaveDialog = func(string) (string, error) {
		t.Error("读取原图超时失败时不应弹出保存对话框")
		return "", nil
	}
	t.Cleanup(func() { previewSaveDialog = origSaveDialog })

	pv := &imagePreviewState{full: &messageMediaRef{threadID: "t1", messageID: "m1", mediaID: "att1"}}

	t.Run("savePreviewImage", func(t *testing.T) {
		client := &blockingMediaReaderClient{FakeClient: NewFakeClient(), started: make(chan struct{})}
		s := newChatState(client)
		t.Cleanup(s.close)

		savePreviewImage(s, pv)
		if client.timedOut.Load() {
			t.Fatal("savePreviewImage 同步阻塞了调用线程")
		}
		select {
		case <-client.started:
		case <-time.After(time.Second):
			t.Fatal("savePreviewImage 未在后台启动读取请求")
		}
		waitReviewComments(t, func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return strings.Contains(s.toastMessage, "读取原图失败")
		})
		if !client.timedOut.Load() {
			t.Fatal("读取超时后 context 未取消")
		}
	})

	t.Run("sendPreviewToComposer", func(t *testing.T) {
		client := &blockingMediaReaderClient{FakeClient: NewFakeClient(), started: make(chan struct{})}
		s := newChatState(client)
		t.Cleanup(s.close)

		sendPreviewToComposer(s, pv)
		if client.timedOut.Load() {
			t.Fatal("sendPreviewToComposer 同步阻塞了调用线程")
		}
		select {
		case <-client.started:
		case <-time.After(time.Second):
			t.Fatal("sendPreviewToComposer 未在后台启动读取请求")
		}
		waitReviewComments(t, func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return strings.Contains(s.toastMessage, "读取原图失败")
		})
		if !client.timedOut.Load() {
			t.Fatal("读取超时后 context 未取消")
		}
	})
}

func TestUIReview30ReviewCommentAttachRetryEnabledAfterFailure(t *testing.T) {
	s, client, dp, key, content := reviewCommentsSetup(t)
	addReviewTestComment(t, dp, key, content, editor.DiffLineRange{Side: editor.DiffNew, Start: 1, End: 1}, "重试评论")

	tt := ui.NewTesterWith(func(c *ui.Context) {
		renderReviewCommentsAction(c, s, dp, "t1")
	}, 400, 100, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	tt.Frame()

	btn, ok := tt.Access("添加到对话 1")
	if !ok || btn.Disabled {
		t.Fatalf("初始状态下按钮应可点击: ok=%v disabled=%v", ok, btn.Disabled)
	}

	if err := tt.Click("添加到对话 1"); err != nil {
		t.Fatalf("点击添加到对话失败: %v", err)
	}
	req := receiveReviewCommentUpload(t, client)
	req.finish <- errors.New("上传失败")

	waitReviewComments(t, func() bool {
		s.mu.Lock()
		failed := len(s.attachments) == 1 && s.attachments[0].Err != ""
		s.mu.Unlock()
		diffPanelMu.Lock()
		batchDone := !dp.commentBatch
		diffPanelMu.Unlock()
		return failed && batchDone
	})

	tt.Frame()
	btn, ok = tt.Access("添加到对话 1")
	if !ok || btn.Disabled {
		t.Fatalf("上传失败后「添加到对话」按钮应恢复可点: ok=%v disabled=%v", ok, btn.Disabled)
	}
	diffPanelMu.Lock()
	attachID := dp.comments[0].attachmentID
	diffPanelMu.Unlock()
	if attachID != "" {
		t.Fatalf("上传失败后 attachmentID 应清空，实际为 %q", attachID)
	}

	if err := tt.Click("添加到对话 1"); err != nil {
		t.Fatalf("重试点击添加到对话失败: %v", err)
	}
	retryReq := receiveReviewCommentUpload(t, client)
	retryReq.finish <- nil
	waitReviewCommentsAdded(t, s, dp, "t1", 1)
}

func TestUIReview30ReviewPRActionsWrapInNarrowPanel(t *testing.T) {
	s, _, dp := reviewPRSetup(t)
	list, full := reviewFixture()
	list.Scope, list.Number = "pr", 7
	key := reviewKey(s, "t1", list, "a.go")
	addReviewTestComment(t, dp, key, *full.File, editor.DiffLineRange{Side: editor.DiffNew, Start: 1, End: 1}, "PR 行内评论")
	diffPanelMu.Lock()
	dp.prAIErr = "AI 审查服务暂时不可用"
	dp.prAIErrNumber = 7
	dp.prReviewErr = "提交失败"
	dp.prReviewNumber = 7
	diffPanelMu.Unlock()

	const panelW, panelH = 320, 200
	tt := ui.NewTesterWith(func(c *ui.Context) {
		renderReviewPRAction(c, s, dp, "t1")
	}, panelW, panelH, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	tt.Frame()

	assertPanel := func(width float32, requireButtonWrap bool) {
		t.Helper()
		panelBounds := ui.Rect{X: 0, Y: 0, W: width, H: panelH}
		var minBtnY, maxBtnY, maxAllY float32
		for i, label := range []string{"评论", "批准", "要求修改", "重试"} {
			info, ok := tt.Access(label)
			if !ok {
				t.Fatalf("未找到 PR 动作按钮 %q", label)
			}
			assertInsideBounds(t, label, info.Bounds, panelBounds)
			if i == 0 || info.Bounds.Y < minBtnY {
				minBtnY = info.Bounds.Y
			}
			if info.Bounds.Y > maxBtnY {
				maxBtnY = info.Bounds.Y
			}
			if info.Bounds.Y > maxAllY {
				maxAllY = info.Bounds.Y
			}
		}
		for _, errText := range []string{"AI 审查服务暂时不可用", "提交失败"} {
			r, ok := tt.Find(errText)
			if !ok {
				t.Fatalf("未找到 PR 错误文本 %q", errText)
			}
			assertInsideBounds(t, errText, r, panelBounds)
			if r.Y > maxAllY {
				maxAllY = r.Y
			}
		}
		if maxAllY <= minBtnY {
			t.Fatalf("%v 宽下 PR 动作行应发生换行: minBtnY=%v maxAllY=%v", width, minBtnY, maxAllY)
		}
		if requireButtonWrap && maxBtnY <= minBtnY {
			t.Fatalf("%v 宽下 4 个 PR 动作按钮应换行: minBtnY=%v maxBtnY=%v", width, minBtnY, maxBtnY)
		}
	}

	assertPanel(panelW, false)
	tt.SetSize(220, panelH)
	tt.Frame()
	assertPanel(220, true)
}
