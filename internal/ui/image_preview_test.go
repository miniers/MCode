package mcodeui

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/fsutil"

	"github.com/egoist/mygo/ui"
)

// TestLoadLocalImage_RejectsHTTP 确认 http/https 地址不加载，Markdown 会照旧显示替代文字。
func TestLoadLocalImage_RejectsHTTP(t *testing.T) {
	for _, url := range []string{
		"http://example.com/a.png",
		"https://example.com/a.png",
		"data:image/png;base64,AAAA",
	} {
		if src, err := loadLocalImage(url); err == nil || src != nil {
			t.Fatalf("loadLocalImage(%q) = %v, %v；希望返回错误且无图片", url, src, err)
		}
	}
}

// TestLoadLocalImage_RejectsRelativePath 确认相对路径不加载。
func TestLoadLocalImage_RejectsRelativePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.png")
	writeTestPNG(t, path)
	// 相对路径即便在进程工作目录下也不接受。
	if src, err := loadLocalImage("a.png"); err == nil || src != nil {
		t.Fatalf("loadLocalImage(\"a.png\") = %v, %v；希望拒绝相对路径", src, err)
	}
}

// TestLoadLocalImage_RejectsUnsupportedExt 确认扩展名不在白名单内时不加载。
func TestLoadLocalImage_RejectsUnsupportedExt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("not an image"), 0o644); err != nil {
		t.Fatalf("写入临时文件失败：%v", err)
	}
	if src, err := loadLocalImage(path); err == nil || src != nil {
		t.Fatalf("loadLocalImage(%q) = %v, %v；希望拒绝不支持的扩展名", path, src, err)
	}
}

// TestLoadLocalImage_LoadsLocalPNG 确认绝对路径与 file:// 地址都能加载临时目录里的 PNG。
func TestLoadLocalImage_LoadsLocalPNG(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.png")
	writeTestPNG(t, path)

	for _, url := range []string{path, fsutil.FileURL(path).String()} {
		src, err := loadLocalImage(url)
		if err != nil {
			t.Fatalf("loadLocalImage(%q) 返回错误：%v", url, err)
		}
		if src == nil {
			t.Fatalf("loadLocalImage(%q) 未返回图片源", url)
		}
	}
}

// TestPreviewClampZoom 确认缩放限制在 1x 到 8x，零值与无效值按 1x 处理。
func TestPreviewClampZoom(t *testing.T) {
	cases := []struct {
		in, want float32
	}{
		{0, 1},
		{-3, 1},
		{0.5, 1},
		{1, 1},
		{2.5, 2.5},
		{7.9, 7.9},
		{8, 8},
		{100, 8},
	}
	for _, tc := range cases {
		if got := previewClampZoom(tc.in); got != tc.want {
			t.Fatalf("previewClampZoom(%v) = %v，希望 %v", tc.in, got, tc.want)
		}
	}
}

// TestPreviewClampPanOffset 确认平移量限制在图片刚好盖住视口的范围内，1x 时只能为 0。
func TestPreviewClampPanOffset(t *testing.T) {
	cases := []struct {
		name                   string
		offset, viewport, zoom float32
		want                   float32
	}{
		{name: "1x 不动", offset: 40, viewport: 100, zoom: 1, want: 0},
		{name: "零缩放不动", offset: -40, viewport: 100, zoom: 0, want: 0},
		{name: "视口为零", offset: 40, viewport: 0, zoom: 4, want: 0},
		{name: "范围内保留", offset: 20, viewport: 100, zoom: 2, want: 20},
		{name: "向右超界", offset: 80, viewport: 100, zoom: 2, want: 50},
		{name: "向左超界", offset: -80, viewport: 100, zoom: 2, want: -50},
		{name: "8x 上限", offset: 1000, viewport: 100, zoom: 8, want: 350},
	}
	for _, tc := range cases {
		if got := previewClampPanOffset(tc.offset, tc.viewport, tc.zoom); got != tc.want {
			t.Fatalf("%s：previewClampPanOffset(%v, %v, %v) = %v，希望 %v",
				tc.name, tc.offset, tc.viewport, tc.zoom, got, tc.want)
		}
	}
}

// TestPreviewScrollZoomFactor 确认向上滚动放大、向下缩小，没位移时不缩放。
func TestPreviewScrollZoomFactor(t *testing.T) {
	if got := previewScrollZoomFactor(0, false); got != 1 {
		t.Fatalf("零位移的缩放倍数 = %v，希望 1", got)
	}
	if got := previewScrollZoomFactor(-10, false); !(got > 1) {
		t.Fatalf("向上滚动的缩放倍数 = %v，希望大于 1", got)
	}
	if got := previewScrollZoomFactor(10, false); !(got > 0 && got < 1) {
		t.Fatalf("向下滚动的缩放倍数 = %v，希望介于 0 与 1 之间", got)
	}
	// 触控板的像素位移应比鼠标滚轮的一格更细腻。
	precise := previewScrollZoomFactor(-10, true)
	wheel := previewScrollZoomFactor(-10, false)
	if !(precise > 1 && precise < wheel) {
		t.Fatalf("触控板倍数 = %v，滚轮倍数 = %v；希望触控板更细腻", precise, wheel)
	}
}

// TestPreviewFitSize 确认图片等比缩放进可视区域，更小的图片不放大。
func TestPreviewFitSize(t *testing.T) {
	src := ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 200, 100)))

	if w, h := previewFitSize(1000, 1000, src); w != 200 || h != 100 {
		t.Fatalf("小图的显示尺寸 = %vx%v，希望 200x100", w, h)
	}
	if w, h := previewFitSize(100, 50, src); w != 100 || h != 50 {
		t.Fatalf("等比例缩小的显示尺寸 = %vx%v，希望 100x50", w, h)
	}
	if w, h := previewFitSize(100, 400, src); w != 100 || h != 50 {
		t.Fatalf("按宽度缩放的显示尺寸 = %vx%v，希望 100x50", w, h)
	}
	// 尺寸未知的图片直接占满最大可视区域。
	if w, h := previewFitSize(320, 240, nil); w != 320 || h != 240 {
		t.Fatalf("未知尺寸的显示尺寸 = %vx%v，希望 320x240", w, h)
	}
}

// writeTestPNG 在 path 写一张 2x2 的测试 PNG。
func writeTestPNG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for x := 0; x < 2; x++ {
		for y := 0; y < 2; y++ {
			img.Set(x, y, color.RGBA{R: 0x2f, G: 0xbf, B: 0xa1, A: 0xff})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建测试图片失败：%v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("编码测试图片失败：%v", err)
	}
}
