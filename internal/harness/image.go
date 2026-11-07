package harness

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ImageMediaType 返回路径所指文件作为图片下发时的 MIME 类型；不是受支持的图片时返回空串与 false。
//
// 先按扩展名判断：扩展名命中 png/jpeg/gif/webp 时直接返回，不读文件内容。扩展名为空
// 或不在支持列表时改为嗅探文件内容（前 512 字节）——会话媒体目录里按 UUID 命名的历史
// 文件没有扩展名，只能靠魔数识别；而扩展名明确是 .pdf、.docx 之类的文件也以内容为准，
// 只有内容确实是图片才放行。支持的类型与 Anthropic 图片块一致：png、jpeg、gif、webp。
func ImageMediaType(path string) (string, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png", true
	case ".jpg", ".jpeg":
		return "image/jpeg", true
	case ".gif":
		return "image/gif", true
	case ".webp":
		return "image/webp", true
	}

	header, err := readHeader(path, 512)
	if err != nil {
		return "", false
	}
	return sniffImageMediaType(header)
}

// readHeader 读取文件的前 limit 个字节；文件比 limit 短时返回实际读到的内容。
func readHeader(path string, limit int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	buf := make([]byte, limit)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return buf[:n], nil
}

// sniffImageMediaType 按内容头识别图片 MIME 类型，识别不了的返回空串与 false。
// webp 不是 net/http 的默认嗅探项，需先单独判 RIFF....WEBP 头。
func sniffImageMediaType(header []byte) (string, bool) {
	if len(header) >= 12 && string(header[:4]) == "RIFF" && string(header[8:12]) == "WEBP" {
		return "image/webp", true
	}
	detected := http.DetectContentType(header)
	switch {
	case strings.HasPrefix(detected, "image/png"):
		return "image/png", true
	case strings.HasPrefix(detected, "image/jpeg"):
		return "image/jpeg", true
	case strings.HasPrefix(detected, "image/gif"):
		return "image/gif", true
	default:
		return "", false
	}
}
