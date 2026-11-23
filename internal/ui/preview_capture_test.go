package mcodeui

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"
)

type screenshotTestPage struct {
	previewTestPage
	data             []byte
	err              error
	started, release chan struct{}
}

func (p *screenshotTestPage) CapturePage() ([]byte, error) {
	if p.started != nil {
		close(p.started)
		<-p.release
	}
	return p.data, p.err
}

func screenshotPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestBrowserScreenshotAddsPNGAttachment(t *testing.T) {
	s, client := inspectorContextSetup(t)
	tab, _ := browserAttachTestPage(s, "https://example.test/page")
	page := &screenshotTestPage{data: screenshotPNG(t)}
	previewMu.Lock()
	tab.page, tab.visible = page, true
	tab.owner.activeTabID = tab.tabID
	previewMu.Unlock()
	previewCaptureScreenshot(s, tab)
	req := inspectorUpload(t, client)
	if req.mime != "image/png" || !bytes.HasPrefix(req.data, []byte("\x89PNG")) {
		t.Fatalf("截图附件错误: %s %q", req.mime, req.name)
	}
	req.finish <- nil
}

func TestBrowserScreenshotRejectsStalePageAndOversize(t *testing.T) {
	s, client := inspectorContextSetup(t)
	tab, _ := browserAttachTestPage(s, "https://example.test/page")
	page := &screenshotTestPage{data: screenshotPNG(t), started: make(chan struct{}), release: make(chan struct{})}
	previewMu.Lock()
	tab.page, tab.visible = page, true
	tab.owner.activeTabID = tab.tabID
	previewMu.Unlock()
	previewCaptureScreenshot(s, tab)
	<-page.started
	previewMu.Lock()
	tab.generation++
	previewMu.Unlock()
	close(page.release)
	time.Sleep(30 * time.Millisecond)
	if len(client.requests) != 0 {
		t.Fatal("旧页面截图进入附件")
	}
	data := append(screenshotPNG(t), bytes.Repeat([]byte("x"), previewCaptureMaxBytes)...)
	if _, err := boundPreviewPNG(data); err != nil {
		t.Fatalf("超字节上限的 PNG 应缩小: %v", err)
	}
	if _, err := boundPreviewPNG([]byte("not png")); err == nil {
		t.Fatal("非法 PNG 未拒绝")
	}
}

func TestBrowserCaptureFailureKeepsSelection(t *testing.T) {
	s, _ := inspectorContextSetup(t)
	tab, _ := browserAttachTestPage(s, "https://example.test")
	page := &screenshotTestPage{err: errors.New("capture failed")}
	previewMu.Lock()
	tab.page, tab.visible = page, true
	tab.owner.activeTabID = tab.tabID
	tab.annotations = []previewAnnotation{{Selector: "#keep"}}
	previewMu.Unlock()
	previewCaptureScreenshot(s, tab)
	time.Sleep(30 * time.Millisecond)
	previewMu.Lock()
	defer previewMu.Unlock()
	if len(tab.annotations) != 1 || tab.annotations[0].Selector != "#keep" {
		t.Fatal("截图失败清除了标注")
	}
}

func TestPreviewCropPNG(t *testing.T) {
	leftColor := color.RGBA{R: 220, G: 40, B: 40, A: 255}
	rightColor := color.RGBA{R: 40, G: 120, B: 220, A: 255}

	src := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			if x < 100 {
				src.SetRGBA(x, y, leftColor)
			} else {
				src.SetRGBA(x, y, rightColor)
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()

	decode := func(t *testing.T, data []byte) image.Image {
		t.Helper()
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("解码裁切结果失败: %v", err)
		}
		return img
	}

	t.Run("crop right half at 2x dpr", func(t *testing.T) {
		out, err := previewCropPNG(raw, 100, 50, 0, 50, 50, 0)
		if err != nil {
			t.Fatalf("previewCropPNG 返回错误: %v", err)
		}
		img := decode(t, out)
		b := img.Bounds()
		if b.Dx() != 100 || b.Dy() != 100 {
			t.Fatalf("期望尺寸 100x100，实际 %dx%d", b.Dx(), b.Dy())
		}
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if c := color.RGBAModel.Convert(img.At(x, y)).(color.RGBA); c != rightColor {
					t.Fatalf("像素 (%d,%d) 颜色 = %+v，期望 %+v", x, y, c, rightColor)
				}
			}
		}
	})

	t.Run("clamp out-of-bounds rect to image edge", func(t *testing.T) {
		out, err := previewCropPNG(raw, 100, 75, 25, 50, 50, 0)
		if err != nil {
			t.Fatalf("越界裁切返回错误: %v", err)
		}
		img := decode(t, out)
		b := img.Bounds()
		if b.Dx() != 50 || b.Dy() != 50 {
			t.Fatalf("期望截断后尺寸 50x50，实际 %dx%d", b.Dx(), b.Dy())
		}
		if c := color.RGBAModel.Convert(img.At(b.Min.X, b.Min.Y)).(color.RGBA); c != rightColor {
			t.Fatalf("越界截断后颜色 = %+v，期望 %+v", c, rightColor)
		}
	})

	t.Run("reject completely outside rect", func(t *testing.T) {
		if _, err := previewCropPNG(raw, 100, 120, 0, 20, 20, 0); err == nil {
			t.Fatal("完全越界矩形未报错")
		}
		if _, err := previewCropPNG(raw, 100, -30, 0, 20, 20, 0); err == nil {
			t.Fatal("负方向完全越界矩形未报错")
		}
	})

	t.Run("reject non-positive viewportW", func(t *testing.T) {
		if _, err := previewCropPNG(raw, 0, 0, 0, 10, 10, 0); err == nil {
			t.Fatal("viewportW=0 未报错")
		}
		if _, err := previewCropPNG(raw, -100, 0, 0, 10, 10, 0); err == nil {
			t.Fatal("viewportW<0 未报错")
		}
	})

	t.Run("downscale long edge to maxEdge", func(t *testing.T) {
		out, err := previewCropPNG(raw, 100, 0, 0, 100, 50, 40)
		if err != nil {
			t.Fatalf("缩放裁切返回错误: %v", err)
		}
		img := decode(t, out)
		b := img.Bounds()
		if max(b.Dx(), b.Dy()) != 40 || b.Dx() != 40 || b.Dy() != 20 {
			t.Fatalf("期望尺寸 40x20，实际 %dx%d", b.Dx(), b.Dy())
		}
	})
}
