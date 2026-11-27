package mcodeui

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func writeTestGIF(t *testing.T, path string, delayCs int, colors ...color.RGBA) {
	t.Helper()
	const size = 20
	g := &gif.GIF{Config: image.Config{Width: size, Height: size}}
	for _, c := range colors {
		f := image.NewPaletted(image.Rect(0, 0, size, size), color.Palette{c})
		g.Image = append(g.Image, f)
		g.Delay = append(g.Delay, delayCs)
		g.Disposal = append(g.Disposal, gif.DisposalNone)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatalf("编码测试 GIF 失败：%v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("写入测试 GIF 失败：%v", err)
	}
}

func waitForPreviewCondition(t *testing.T, tt *ui.Tester, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if tt.FramePending() {
			tt.Frame()
		}
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	if tt.FramePending() {
		tt.Frame()
	}
	if !cond() {
		t.Fatalf("等待超时：%s", desc)
	}
}

func TestImagePreviewGIF_AnimatedPlaysAndCloses(t *testing.T) {
	setAppReduceMotion(t, false)
	path := filepath.Join(t.TempDir(), "anim.gif")
	writeTestGIF(t, path, 10, color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255})
	t.Cleanup(func() { imagePreviewCache.Delete(path) })

	s := newChatState(NewFakeClient())
	defer s.close()
	s.imagePreview = &imagePreviewState{url: path, alt: "anim-gif"}

	builds := 0
	tt := ui.NewTester(func(c *ui.Context) {
		builds++
		renderImagePreviewOverlay(c, s)
	}, 400, 300)

	waitForPreviewCondition(t, tt, "多帧 GIF 创建播放器", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.imagePreview != nil && s.imagePreview.gifPlayer != nil
	})

	s.mu.Lock()
	player := s.imagePreview.gifPlayer
	s.mu.Unlock()
	if player == nil || !player.Playing() {
		t.Fatalf("多帧 GIF 播放器未处于播放状态：%v", player)
	}

	rect, ok := tt.Find("anim-gif")
	if !ok {
		t.Fatal("未找到正在播放的 GIF 元素")
	}
	cx, cy := int(rect.X+rect.W/2), int(rect.Y+rect.H/2)
	firstPixel := tt.Image().RGBAAt(cx, cy)

	// 等待解码协程预解出后续帧后推进时钟，应切换到下一帧颜色。
	deadline := time.Now().Add(2 * time.Second)
	switched := false
	for time.Now().Before(deadline) {
		tt.Advance(110 * time.Millisecond)
		if tt.FramePending() {
			tt.Frame()
		}
		if tt.Image().RGBAAt(cx, cy) != firstPixel {
			switched = true
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !switched {
		t.Fatalf("推进时钟后 GIF 未切帧，中心像素仍为 %v", firstPixel)
	}

	closeImagePreview(s)
	tt.Frame()
	if player.Playing() {
		t.Fatal("关闭预览后播放器未 Close")
	}
	buildsAfterClose := builds
	tt.Advance(time.Second)
	if builds != buildsAfterClose {
		t.Fatalf("关闭预览并推进时钟后不应继续重建视图：%d -> %d", buildsAfterClose, builds)
	}
	if tt.FramePending() {
		t.Fatal("关闭预览后不应继续请求下一帧")
	}
}

func TestImagePreviewGIF_SingleFrameNoPlayer(t *testing.T) {
	setAppReduceMotion(t, false)
	path := filepath.Join(t.TempDir(), "single.gif")
	writeTestGIF(t, path, 10, color.RGBA{G: 255, A: 255})
	t.Cleanup(func() { imagePreviewCache.Delete(path) })

	s := newChatState(NewFakeClient())
	defer s.close()
	s.imagePreview = &imagePreviewState{url: path, alt: "single-gif"}

	builds := 0
	tt := ui.NewTester(func(c *ui.Context) {
		builds++
		renderImagePreviewOverlay(c, s)
	}, 400, 300)

	waitForPreviewCondition(t, tt, "单帧 GIF 加载完成", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.imagePreview != nil && s.imagePreview.gifAttempted
	})

	s.mu.Lock()
	player := s.imagePreview.gifPlayer
	s.mu.Unlock()
	if player != nil {
		t.Fatal("单帧 GIF 不应创建播放器")
	}
	if _, ok := tt.Find("single-gif"); !ok {
		t.Fatal("单帧 GIF 应显示静态图片")
	}
	buildsDone := builds
	tt.Advance(time.Second)
	if builds != buildsDone || tt.FramePending() {
		t.Fatalf("单帧 GIF 推进时钟后不应请求下一帧：builds %d->%d, pending=%v", buildsDone, builds, tt.FramePending())
	}
}

func TestImagePreviewGIF_ReduceMotionNoPlayer(t *testing.T) {
	setAppReduceMotion(t, true)
	path := filepath.Join(t.TempDir(), "reduced.gif")
	writeTestGIF(t, path, 10, color.RGBA{R: 255, A: 255}, color.RGBA{G: 255, A: 255})
	t.Cleanup(func() { imagePreviewCache.Delete(path) })

	s := newChatState(NewFakeClient())
	defer s.close()
	s.imagePreview = &imagePreviewState{url: path, alt: "reduced-gif"}

	builds := 0
	tt := ui.NewTesterWith(func(c *ui.Context) {
		builds++
		renderImagePreviewOverlay(c, s)
	}, 400, 300, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})

	waitForPreviewCondition(t, tt, "减少动效下 GIF 首帧加载完成", func() bool {
		v, ok := imagePreviewCache.Load(path)
		if !ok {
			return false
		}
		e := v.(*previewImageEntry)
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.done
	})

	s.mu.Lock()
	player := s.imagePreview.gifPlayer
	s.mu.Unlock()
	if player != nil {
		t.Fatal("减少动效开启时不应创建播放器")
	}
	if _, ok := tt.Find("reduced-gif"); !ok {
		t.Fatal("减少动效开启时应显示首帧图片")
	}
	buildsDone := builds
	tt.Advance(time.Second)
	if builds != buildsDone || tt.FramePending() {
		t.Fatalf("减少动效开启时推进时钟不应请求下一帧：builds %d->%d, pending=%v", buildsDone, builds, tt.FramePending())
	}

	// 运行时关闭减少动效后创建播放器，再次开启减少动效后立即关闭播放器并停帧。
	setAppReduceMotion(t, false)
	tt.SetPreferences(ui.Preferences{})
	tt.Frame()
	waitForPreviewCondition(t, tt, "取消减少动效后创建播放器", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.imagePreview != nil && s.imagePreview.gifPlayer != nil
	})
	s.mu.Lock()
	activePlayer := s.imagePreview.gifPlayer
	s.mu.Unlock()

	setAppReduceMotion(t, true)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	s.mu.Lock()
	afterToggle := s.imagePreview.gifPlayer
	s.mu.Unlock()
	if afterToggle != nil || activePlayer.Playing() {
		t.Fatalf("开启减少动效后应关闭并清理播放器：afterToggle=%v, playing=%v", afterToggle, activePlayer.Playing())
	}
	buildsStopped := builds
	tt.Advance(time.Second)
	if builds != buildsStopped || tt.FramePending() {
		t.Fatalf("切换减少动效后推进时钟不应再请求帧：builds %d->%d, pending=%v", buildsStopped, builds, tt.FramePending())
	}
}

func TestImagePreviewGIF_SwitchImageClosesPlayer(t *testing.T) {
	setAppReduceMotion(t, false)
	dir := t.TempDir()
	gifPath := filepath.Join(dir, "a.gif")
	pngPath := filepath.Join(dir, "b.png")
	writeTestGIF(t, gifPath, 10, color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255})
	writeTestPNG(t, pngPath)
	t.Cleanup(func() {
		imagePreviewCache.Delete(gifPath)
		imagePreviewCache.Delete(pngPath)
	})

	s := newChatState(NewFakeClient())
	defer s.close()
	s.selectedThreadID = "t-gif-switch"
	s.messages = []Message{
		{
			ID:   "m1",
			Role: "assistant",
			Text: "![gif](" + gifPath + ")\n\n![png](" + pngPath + ")",
		},
	}
	s.messagesRev = 1
	s.imagePreview = &imagePreviewState{url: gifPath, alt: "gif"}

	builds := 0
	tt := ui.NewTester(func(c *ui.Context) {
		builds++
		renderImagePreviewOverlay(c, s)
	}, 400, 300)

	waitForPreviewCondition(t, tt, "切换前 GIF 创建播放器", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.imagePreview != nil && s.imagePreview.gifPlayer != nil
	})
	s.mu.Lock()
	gifPlayer := s.imagePreview.gifPlayer
	s.mu.Unlock()

	tt.Key(0, ui.KeyRight)
	waitForPreviewCondition(t, tt, "切换到静态 PNG 完成", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.imagePreview != nil && s.imagePreview.url == pngPath && cachedPreviewImageReady(pngPath)
	})
	tt.Frame()

	if gifPlayer.Playing() {
		t.Fatal("切换到其他图片后原 GIF 播放器未 Close")
	}
	s.mu.Lock()
	nextPlayer := s.imagePreview.gifPlayer
	s.mu.Unlock()
	if nextPlayer != nil {
		t.Fatal("切换到 PNG 后不应存在 GIF 播放器")
	}

	buildsDone := builds
	tt.Advance(time.Second)
	if builds != buildsDone || tt.FramePending() {
		t.Fatalf("切换到静态图片并推进时钟后不应继续请求下一帧：builds %d->%d, pending=%v", buildsDone, builds, tt.FramePending())
	}
}

func TestImagePreviewGIF_CorruptFallbackToStatic(t *testing.T) {
	setAppReduceMotion(t, false)
	path := filepath.Join(t.TempDir(), "broken.gif")
	if err := os.WriteFile(path, []byte("GIF89a-corrupt-data"), 0o644); err != nil {
		t.Fatalf("写入损坏 GIF 失败：%v", err)
	}
	t.Cleanup(func() { imagePreviewCache.Delete(path) })

	if _, err := newPreviewGIFPlayer([]byte("GIF89a-corrupt-data")); err == nil {
		t.Fatal("损坏的 GIF 数据应创建播放器失败")
	}

	s := newChatState(NewFakeClient())
	defer s.close()
	s.imagePreview = &imagePreviewState{url: path, alt: "broken-gif"}

	builds := 0
	tt := ui.NewTester(func(c *ui.Context) {
		builds++
		renderImagePreviewOverlay(c, s)
	}, 400, 300)

	waitForPreviewCondition(t, tt, "损坏 GIF 回退错误提示", func() bool {
		return tt.HasText("图片加载失败")
	})

	s.mu.Lock()
	player := s.imagePreview.gifPlayer
	s.mu.Unlock()
	if player != nil {
		t.Fatal("解码失败时不应创建播放器")
	}
	buildsDone := builds
	tt.Advance(time.Second)
	if builds != buildsDone || tt.FramePending() {
		t.Fatalf("解码失败后不应请求下一帧：builds %d->%d, pending=%v", buildsDone, builds, tt.FramePending())
	}
}

func cachedPreviewImageReady(url string) bool {
	v, ok := imagePreviewCache.Load(url)
	if !ok {
		return false
	}
	e := v.(*previewImageEntry)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.done && e.src != nil
}
