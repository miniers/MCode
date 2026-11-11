package mcodeui

import (
	"bytes"
	"errors"
	"image"
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
