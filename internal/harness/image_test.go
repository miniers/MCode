package harness

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// writeImageTestFile 在 dir 下写一个文件并返回其绝对路径。
func writeImageTestFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}
	return path
}

// pngBytes 返回一张编码好的 1x1 png 图。
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 0xff, A: 0xff})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("编码 png 失败: %v", err)
	}
	return buf.Bytes()
}

// TestImageMediaType 验证图片识别：扩展名命中时直接返回；扩展名不可靠时按内容嗅探。
func TestImageMediaType(t *testing.T) {
	dir := t.TempDir()

	t.Run("带扩展名的png", func(t *testing.T) {
		path := writeImageTestFile(t, dir, "shot.png", pngBytes(t))
		mediaType, ok := ImageMediaType(path)
		if !ok || mediaType != "image/png" {
			t.Fatalf("应为 image/png，实际 %q %v", mediaType, ok)
		}
	})

	t.Run("无扩展名但内容是png", func(t *testing.T) {
		// 会话媒体目录里的历史文件名是不带扩展名的 UUID。
		path := writeImageTestFile(t, dir, "7f3c0d1e-2b4a-4c5d-8e9f-0a1b2c3d4e5f", pngBytes(t))
		mediaType, ok := ImageMediaType(path)
		if !ok || mediaType != "image/png" {
			t.Fatalf("无扩展名的 png 应被识别，实际 %q %v", mediaType, ok)
		}
	})

	t.Run("无扩展名的webp头", func(t *testing.T) {
		header := []byte("RIFF\x24\x00\x00\x00WEBPVP8 ")
		path := writeImageTestFile(t, dir, "8a1b2c3d-4e5f-4a6b-9c7d-0e1f2a3b4c5d", header)
		mediaType, ok := ImageMediaType(path)
		if !ok || mediaType != "image/webp" {
			t.Fatalf("无扩展名的 webp 应被识别，实际 %q %v", mediaType, ok)
		}
	})

	t.Run("无扩展名的纯文本", func(t *testing.T) {
		path := writeImageTestFile(t, dir, "0c1d2e3f-4a5b-4c6d-8e7f-9a0b1c2d3e4f", []byte("not an image"))
		if mediaType, ok := ImageMediaType(path); ok {
			t.Fatalf("纯文本不应被识别为图片，得到 %q", mediaType)
		}
	})

	t.Run("文件不存在", func(t *testing.T) {
		if mediaType, ok := ImageMediaType(filepath.Join(dir, "missing")); ok {
			t.Fatalf("不存在的文件不应被识别为图片，得到 %q", mediaType)
		}
	})

	t.Run("非图片扩展名但内容是png", func(t *testing.T) {
		// 扩展名不可信：内容才是依据。
		path := writeImageTestFile(t, dir, "renamed.bin", pngBytes(t))
		mediaType, ok := ImageMediaType(path)
		if !ok || mediaType != "image/png" {
			t.Fatalf("改名的 png 应按内容识别，实际 %q %v", mediaType, ok)
		}
	})
}
