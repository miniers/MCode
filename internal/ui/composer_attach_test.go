package mcodeui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
	"mcode/internal/testenv"
)

// 场景 1：正常添加图片与非图片附件并成功异步上传回填 ID。
func TestAttachment_AddSuccess(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)

	data := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}
	ok := s.addAttachment("photo.png", "image/png", data)
	if !ok {
		t.Fatalf("expected addAttachment to succeed")
	}

	s.mu.Lock()
	if len(s.attachments) != 1 {
		s.mu.Unlock()
		t.Fatalf("expected 1 attachment, got %d", len(s.attachments))
	}
	s.mu.Unlock()

	// 等待异步上传完成
	deadline := time.Now().Add(2 * time.Second)
	var uploadedID string
	for time.Now().Before(deadline) {
		s.mu.Lock()
		if len(s.attachments) > 0 && !s.attachments[0].Uploading && s.attachments[0].ID != "" {
			uploadedID = s.attachments[0].ID
			s.mu.Unlock()
			break
		}
		s.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}

	if uploadedID == "" {
		t.Fatalf("attachment was not uploaded successfully")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.uploadCalls) != 1 {
		t.Fatalf("expected 1 upload call, got %d", len(fake.uploadCalls))
	}
	if fake.uploadCalls[0].Name != "photo.png" {
		t.Fatalf("expected uploaded name photo.png, got %s", fake.uploadCalls[0].Name)
	}
}

// 场景 2：detectAttachmentMime 对 png、pdf、md、go 返回允许，未知扩展名的二进制也兜底接受。
func TestDetectAttachmentMime(t *testing.T) {
	testenv.TempHome(t)

	// PNG 图片
	pngData := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	mime, ok := detectAttachmentMime("image.png", pngData)
	if !ok || mime != "image/png" {
		t.Fatalf("expected image.png to be allowed image/png, got mime=%q ok=%v", mime, ok)
	}

	// PDF 文档
	pdfData := []byte("%PDF-1.4 file content here")
	mime, ok = detectAttachmentMime("doc.pdf", pdfData)
	if !ok || mime != "application/pdf" {
		t.Fatalf("expected doc.pdf to be allowed application/pdf, got mime=%q ok=%v", mime, ok)
	}

	// Markdown 文本
	mdData := []byte("# Hello World\n\nThis is markdown.")
	mime, ok = detectAttachmentMime("README.md", mdData)
	if !ok || mime != "text/markdown" {
		t.Fatalf("expected README.md to be allowed text/markdown, got mime=%q ok=%v", mime, ok)
	}

	// Go 代码源文件
	goData := []byte("package main\n\nfunc main() {}\n")
	mime, ok = detectAttachmentMime("main.go", goData)
	if !ok || mime == "" {
		t.Fatalf("expected main.go to be allowed, got mime=%q ok=%v", mime, ok)
	}

	// 随机二进制文件：扩展名未知，按兜底 MIME 接受
	randomBinary := []byte{0x00, 0xff, 0x88, 0xfe, 0x12, 0x34, 0xaa}
	mime, ok = detectAttachmentMime("data.bin", randomBinary)
	if !ok || mime == "" {
		t.Fatalf("expected data.bin to be accepted, got mime=%q ok=%v", mime, ok)
	}

	// 命名为 .go 但内容是二进制：不当文本，也不拒绝，按二进制兜底接受
	invalidUTF8Go := []byte{0xff, 0xfe, 0x80, 0x00}
	mime, ok = detectAttachmentMime("bad.go", invalidUTF8Go)
	if !ok || mime == "" {
		t.Fatalf("expected bad.go with invalid utf8 to be accepted as binary, got mime=%q ok=%v", mime, ok)
	}
	if strings.HasPrefix(mime, "text/") {
		t.Fatalf("expected binary mime for non-utf8 .go content, got %q", mime)
	}
}

// 任意文件类型都被接受：docx、zip 与无扩展名二进制不会再报「不支持的文件类型」。
func TestDetectAttachmentMime_ArbitraryFilesAccepted(t *testing.T) {
	testenv.TempHome(t)

	// docx 是 zip 容器，内容含非 UTF-8 字节；MIME 允许取扩展名表或内容嗅探的兜底值
	docxData := append([]byte("PK\x03\x04"), 0x14, 0x00, 0x06, 0x00, 0xff, 0xfe, 0x00, 0x80)
	mime, ok := detectAttachmentMime("report.docx", docxData)
	if !ok || mime == "" {
		t.Fatalf("expected report.docx to be accepted, got mime=%q ok=%v", mime, ok)
	}
	if strings.HasPrefix(mime, "text/") {
		t.Fatalf("expected binary mime for report.docx, got %q", mime)
	}

	// zip 压缩包（不同系统 MIME 表可能给出 x-zip-compressed，两者都算正确）
	zipData := append([]byte("PK\x03\x04"), 0x14, 0x00, 0x00, 0x00)
	if mime, ok := detectAttachmentMime("archive.zip", zipData); !ok || (mime != "application/zip" && mime != "application/x-zip-compressed") {
		t.Fatalf("expected archive.zip to be accepted as zip mime, got mime=%q ok=%v", mime, ok)
	}

	// 无扩展名的二进制文件按内容嗅探兜底为 octet-stream
	bare := []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0x7f, 0x00}
	if mime, ok := detectAttachmentMime("blob", bare); !ok || mime != "application/octet-stream" {
		t.Fatalf("expected extensionless binary to be accepted as application/octet-stream, got mime=%q ok=%v", mime, ok)
	}
}

// classifyAttachment：非图片/PDF/文本的二进制文件归为 categoryFile，tag 取大写扩展名。
func TestClassifyAttachment_FileCategory(t *testing.T) {
	const docxMime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	if cat, tag := classifyAttachment("report.docx", docxMime); cat != categoryFile || tag != "DOCX" {
		t.Fatalf("expected categoryFile/DOCX for docx, got %v/%q", cat, tag)
	}
	if cat, tag := classifyAttachment("archive.zip", "application/zip"); cat != categoryFile || tag != "ZIP" {
		t.Fatalf("expected categoryFile/ZIP for zip, got %v/%q", cat, tag)
	}
	// 无扩展名的二进制文件用 FILE 作为 tag
	if cat, tag := classifyAttachment("blob", "application/octet-stream"); cat != categoryFile || tag != "FILE" {
		t.Fatalf("expected categoryFile/FILE for extensionless binary, got %v/%q", cat, tag)
	}
	// 代码源文件仍按代码类渲染，不受影响
	if cat, tag := classifyAttachment("main.go", "text/plain"); cat != categoryCode || tag != "GO" {
		t.Fatalf("expected categoryCode/GO for go source, got %v/%q", cat, tag)
	}
}

// estimateAttachmentTokens：docx/zip 等二进制文件由 agent 按路径读取，不计入上下文。
func TestEstimateAttachmentTokens_BinaryFiles(t *testing.T) {
	const docxMime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	if got := estimateAttachmentTokens(docxMime, nil, 8*1024*1024); got != 0 {
		t.Fatalf("expected 0 tokens for docx, got %d", got)
	}
	if got := estimateAttachmentTokens("application/zip", nil, 1024); got != 0 {
		t.Fatalf("expected 0 tokens for zip, got %d", got)
	}
}

// 场景 3：单文件超过 20MB (maxAttachmentBytes) 被拒绝并出现 notice。
func TestAttachment_ExceedMaxSize(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)

	bigData := make([]byte, maxAttachmentBytes+1)
	bigData[0] = 0x89
	bigData[1] = 'P'
	bigData[2] = 'N'
	bigData[3] = 'G'

	ok := s.addAttachment("big.png", "image/png", bigData)
	if ok {
		t.Fatalf("expected addAttachment to be rejected for exceeding max size")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.attachments) != 0 {
		t.Fatalf("expected 0 attachments, got %d", len(s.attachments))
	}
	notice := s.attachmentNotice()
	if !strings.Contains(notice, "20MB") && !strings.Contains(notice, "上限") {
		t.Fatalf("expected attachment notice mentioning limit, got %q", notice)
	}
	if !strings.Contains(notice, "big.png") {
		t.Fatalf("expected attachment notice mentioning filename, got %q", notice)
	}
}

// 场景 4：达到配置的附件数量上限后，继续添加会被拒绝。
func TestAttachment_ExceedMaxCount(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)
	cfg := config.Defaults()
	cfg.Session.ComposerAttachmentLimit = 2
	setChatSettings(s, &SettingsState{cfg: cfg})
	t.Cleanup(func() { setChatSettings(s, nil) })

	data := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	for i := 1; i <= 2; i++ {
		ok := s.addAttachment("p.png", "image/png", data)
		if !ok {
			t.Fatalf("expected attachment %d to succeed", i)
		}
	}

	// 添加第 3 个应被拒绝。
	ok := s.addAttachment("p5.png", "image/png", data)
	if ok {
		t.Fatalf("expected 3rd attachment to be rejected")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.attachments) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(s.attachments))
	}
	notice := s.attachmentNotice()
	if !strings.Contains(notice, "2 个") {
		t.Fatalf("expected attachment notice mentioning max count, got %q", notice)
	}
	if !strings.Contains(notice, "p5.png") {
		t.Fatalf("expected attachment notice mentioning filename p5.png, got %q", notice)
	}
}

// 场景 5：总大小超过 50MB (maxTotalAttachmentBytes) 被拒绝并出现 notice。
func TestAttachment_ExceedTotalSize(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)

	// 模拟已存在 49.9MB 附件
	s.attachments = []composerAttachment{
		{LocalID: "loc-1", Name: "large1.png", Size: maxTotalAttachmentBytes - 100},
	}

	// 再次添加 200 字节超出 50MB 上限
	data := make([]byte, 200)
	data[0] = 0x89
	data[1] = 'P'
	data[2] = 'N'
	data[3] = 'G'

	ok := s.addAttachment("extra.png", "image/png", data)
	if ok {
		t.Fatalf("expected extra attachment over total limit to be rejected")
	}

	notice := s.attachmentNotice()
	if !strings.Contains(notice, "50MB") && !strings.Contains(notice, "上限") {
		t.Fatalf("expected attachment notice mentioning 50MB limit, got %q", notice)
	}
	if !strings.Contains(notice, "extra.png") {
		t.Fatalf("expected attachment notice mentioning filename extra.png, got %q", notice)
	}
}

// waitAttachmentFiles 等待 readAttachmentFiles 的后台读盘完成：附件数达到 want，
// 且（noticePart 非空时）拒绝提示包含该片段。
func waitAttachmentFiles(t *testing.T, s *chatState, want int, noticePart string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		count := len(s.attachments)
		s.mu.Unlock()
		if count == want && (noticePart == "" || strings.Contains(s.attachmentNotice(), noticePart)) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.mu.Lock()
	count := len(s.attachments)
	s.mu.Unlock()
	t.Fatalf("后台读盘未完成：期望 %d 个附件，实际 %d 个，提示 %q", want, count, s.attachmentNotice())
}

// 场景 6：addAttachmentFiles 传入临时目录时递归展开目录、跳过隐藏文件并按名称排序。
func TestAttachment_AddFiles_DirectoryExpansion(t *testing.T) {
	testenv.TempHome(t)
	tmpDir := t.TempDir()

	// 创建测试目录结构：
	// tmpDir/
	//   b.txt
	//   a.txt
	//   .hidden.txt (应跳过)
	//   sub/
	//     c.go
	//     .subhidden/ (应跳过)
	//       d.go
	if err := os.WriteFile(filepath.Join(tmpDir, "b.txt"), []byte("content b"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("content a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".hidden.txt"), []byte("hidden"), 0644); err != nil {
		t.Fatal(err)
	}

	subDir := filepath.Join(tmpDir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "c.go"), []byte("package sub"), 0644); err != nil {
		t.Fatal(err)
	}

	hiddenSub := filepath.Join(subDir, ".subhidden")
	if err := os.MkdirAll(hiddenSub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hiddenSub, "d.go"), []byte("package hidden"), 0644); err != nil {
		t.Fatal(err)
	}

	fake := NewFakeClient()
	s := newChatState(fake)
	s.addAttachmentFiles([]string{tmpDir})
	// 读盘与解码在后台协程完成（见 readAttachmentFiles）。
	waitAttachmentFiles(t, s, 3, "")

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.attachments) != 3 {
		t.Fatalf("expected 3 attachments from expanded directory, got %d", len(s.attachments))
	}

	// 验证按排序展开且未包含隐藏文件
	names := []string{s.attachments[0].Name, s.attachments[1].Name, s.attachments[2].Name}
	expected := []string{"a.txt", "b.txt", "c.go"}
	for i, exp := range expected {
		if names[i] != exp {
			t.Errorf("attachment[%d] expected name %s, got %s", i, exp, names[i])
		}
	}
}

// 场景 7：替换包变量 clipboardReader 后，粘贴文件路径列表能把附件入队。
func TestAttachment_PasteFilePaths(t *testing.T) {
	testenv.TempHome(t)
	tmpFile := filepath.Join(t.TempDir(), "paste_test.go")
	if err := os.WriteFile(tmpFile, []byte("package paste"), 0644); err != nil {
		t.Fatal(err)
	}

	old := clipboardReader
	t.Cleanup(func() { clipboardReader = old })
	clipboardReader = func() (bool, string, string, []byte, []string) {
		return true, "", "", nil, []string{tmpFile}
	}

	fake := NewFakeClient()
	s := newChatState(fake)
	ok := s.tryPasteImage()
	if !ok {
		t.Fatalf("expected tryPasteImage with filePaths to succeed")
	}
	// 读盘在后台协程完成。
	waitAttachmentFiles(t, s, 1, "")

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.attachments) != 1 {
		t.Fatalf("expected 1 attachment pasted, got %d", len(s.attachments))
	}
	if s.attachments[0].Name != "paste_test.go" {
		t.Errorf("expected pasted attachment name paste_test.go, got %s", s.attachments[0].Name)
	}
}

// 场景 7b：粘贴回调只在剪贴板含文件或图片时吞掉粘贴，纯文本交给输入框。
func TestAttachment_HandlePaste(t *testing.T) {
	testenv.TempHome(t)

	newFile := func(t *testing.T) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "pasted.png")
		if err := os.WriteFile(path, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	swapClipboard := func(t *testing.T, fn clipboardReaderFunc) {
		t.Helper()
		old := clipboardReader
		oldFiles := clipboardFilesReader
		t.Cleanup(func() { clipboardReader = old; clipboardFilesReader = oldFiles })
		clipboardReader = fn
		clipboardFilesReader = func() ([]string, error) { _, _, _, _, paths := fn(); return paths, nil }
	}

	// 复制文件：粘贴回调拿到的文本就是文件路径，应作为附件加入并吞掉粘贴
	t.Run("file path becomes attachment", func(t *testing.T) {
		path := newFile(t)
		swapClipboard(t, func() (bool, string, string, []byte, []string) {
			return true, "", "", nil, []string{path}
		})
		s := newChatState(NewFakeClient())
		if took := s.handlePaste(path); !took {
			t.Fatalf("expected pasting a file path to be taken")
		}
		// 读盘在后台协程完成。
		waitAttachmentFiles(t, s, 1, "")
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.attachments) != 1 || s.attachments[0].Name != "pasted.png" {
			t.Fatalf("expected pasted.png attachment, got %+v", s.attachments)
		}
	})

	// 剪贴板同时含文件与文本：文本不是文件路径时按普通文本插入
	t.Run("file with text content inserts text", func(t *testing.T) {
		path := newFile(t)
		swapClipboard(t, func() (bool, string, string, []byte, []string) {
			return true, "", "", nil, []string{path}
		})
		s := newChatState(NewFakeClient())
		if took := s.handlePaste("package main"); took {
			t.Fatalf("expected text content to fall through to the input")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.attachments) != 0 {
			t.Fatalf("expected no attachment for text paste, got %d", len(s.attachments))
		}
	})

	// 纯文本剪贴板：不拦截
	t.Run("plain text is not taken", func(t *testing.T) {
		swapClipboard(t, func() (bool, string, string, []byte, []string) {
			return false, "", "", nil, nil
		})
		s := newChatState(NewFakeClient())
		if took := s.handlePaste("hello"); took {
			t.Fatalf("expected plain text paste not to be taken")
		}
	})

	t.Run("image with text inserts text", func(t *testing.T) {
		swapClipboard(t, func() (bool, string, string, []byte, []string) {
			return true, "pasted-image.png", "image/png", []byte{1}, nil
		})
		s := newChatState(NewFakeClient())
		if s.handlePaste("hello") || len(s.attachments) != 0 {
			t.Fatal("有文本时应插入文本，不添加图片")
		}
	})

	t.Run("jpeg becomes attachment", func(t *testing.T) {
		swapClipboard(t, func() (bool, string, string, []byte, []string) {
			return true, "pasted-image.jpg", "image/jpeg", []byte{0xff, 0xd8, 0xff}, nil
		})
		s := newChatState(NewFakeClient())
		if !s.handlePaste("") {
			t.Fatal("图片应作为附件")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.attachments) != 1 || s.attachments[0].Name != "pasted-image.jpg" || s.attachments[0].MimeType != "image/jpeg" {
			t.Fatalf("JPEG 附件不正确: %+v", s.attachments)
		}
	})

	// 剪贴板只有图片：作为 pasted-image.png 附件加入
	t.Run("image data becomes attachment", func(t *testing.T) {
		swapClipboard(t, func() (bool, string, string, []byte, []string) {
			return true, "pasted-image.png", "image/png", []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, nil
		})
		s := newChatState(NewFakeClient())
		if took := s.handlePaste(""); !took {
			t.Fatalf("expected image paste to be taken")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.attachments) != 1 || s.attachments[0].Name != "pasted-image.png" {
			t.Fatalf("expected pasted-image.png attachment, got %+v", s.attachments)
		}
	})
}

// 场景 9：图片附件生成缩略图位图，非图片附件不生成。
func TestAttachment_Thumbnail(t *testing.T) {
	testenv.TempHome(t)

	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 120, 80))
	for x := 0; x < 120; x++ {
		for y := 0; y < 80; y++ {
			img.Set(x, y, color.RGBA{R: 0x3b, G: 0x82, B: 0xf6, A: 0xff})
		}
	}
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()

	if thumb := attachmentThumbnail("image/png", data); thumb == nil {
		t.Fatalf("expected a thumbnail for png data")
	} else if w, h := thumb.Size(); w != 120 || h != 80 {
		t.Fatalf("expected thumbnail 120x80, got %dx%d", w, h)
	}
	if thumb := attachmentThumbnail("text/plain", []byte("hello")); thumb != nil {
		t.Fatalf("expected no thumbnail for plain text")
	}
	if thumb := attachmentThumbnail("image/png", []byte("not a png")); thumb != nil {
		t.Fatalf("expected no thumbnail for undecodable data")
	}

	s := newChatState(NewFakeClient())
	if ok := s.addAttachment("shot.png", "image/png", data); !ok {
		t.Fatalf("expected addAttachment to accept png")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.attachments) != 1 || s.attachments[0].Thumb == nil {
		t.Fatalf("expected attachment with thumbnail, got %+v", s.attachments)
	}
}

// 场景 8：任意二进制文件（含 ELF）都被接受，不再出现「不支持的文件类型」提示。
func TestAttachment_ArbitraryBinaryAccepted(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)

	randBytes := []byte{0x7f, 'E', 'L', 'F', 0x02, 0x01, 0x01}
	ok := s.addAttachment("program.elf", "application/octet-stream", randBytes)
	if !ok {
		t.Fatalf("expected elf binary attachment to be accepted")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(s.attachments))
	}
	if s.attachments[0].MimeType == "" {
		t.Fatalf("expected non-empty mime for accepted binary attachment")
	}
	if notice := s.attachmentNotice(); strings.Contains(notice, "不支持") {
		t.Fatalf("expected no unsupported-type notice, got %q", notice)
	}
}

// 场景 9：删除附件（removeAttachment）。
func TestAttachment_Remove(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)

	data := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	s.addAttachment("photo1.png", "image/png", data)
	s.addAttachment("photo2.png", "image/png", data)

	s.mu.Lock()
	targetID := s.attachments[0].LocalID
	s.mu.Unlock()

	s.removeAttachment(targetID)

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.attachments) != 1 {
		t.Fatalf("expected 1 attachment left, got %d", len(s.attachments))
	}
	if s.attachments[0].LocalID == targetID {
		t.Fatalf("expected removed attachment %s, but it was still present", targetID)
	}
}

// 场景 10：发送时带上已上传附件 ID，发送后清空附件列表。
func TestAttachment_SendWithAttachmentsAndClear(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)
	s.selectedThreadID = "t1"

	// 模拟已存在两个已成功上传的附件
	s.attachments = []composerAttachment{
		{LocalID: "loc-1", Name: "img1.png", ID: "att-1", Uploading: false},
		{LocalID: "loc-2", Name: "img2.png", ID: "att-2", Uploading: false},
	}

	s.sendMessage("查看这两张图")

	// 等待后台 goroutine 发送完成
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		count := len(fake.sendWithAttachCalls)
		fake.mu.Unlock()
		if count > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	tt := ui.NewTester(func(c *ui.Context) {}, 100, 100)
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.sending && len(s.attachments) == 0
	})

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.sendWithAttachCalls) != 1 {
		t.Fatalf("expected 1 sendWithAttach call, got %d", len(fake.sendWithAttachCalls))
	}
	call := fake.sendWithAttachCalls[0]
	if call.ThreadID != "t1" || call.Text != "查看这两张图" {
		t.Fatalf("unexpected call args: %+v", call)
	}
	if len(call.AttachmentIDs) != 2 || call.AttachmentIDs[0] != "att-1" || call.AttachmentIDs[1] != "att-2" {
		t.Fatalf("unexpected attachment IDs: %v", call.AttachmentIDs)
	}
}

type failingSendClient struct{ *FakeClient }

func (f failingSendClient) SendWithAttachments(context.Context, string, string, []string) (bool, error) {
	return false, errors.New("offline")
}

func TestAttachmentFailedSendKeepsDraft(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(failingSendClient{NewFakeClient()})
	s.draft = "不要丢失"
	s.attachments = []composerAttachment{{LocalID: "local", ID: "att-1", Name: "test.png"}}
	tt := composerTester(s)
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.toastMessage != ""
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.draft != "不要丢失" || len(s.attachments) != 1 || s.attachments[0].ID != "att-1" {
		t.Fatalf("failed send lost draft or attachment: %q, %+v", s.draft, s.attachments)
	}
}

func TestAttachmentSendBlockedUntilAllReady(t *testing.T) {
	testenv.TempHome(t)
	for _, att := range []composerAttachment{
		{LocalID: "uploading", Uploading: true},
		{LocalID: "failed", Err: "offline"},
	} {
		fake := NewFakeClient()
		s := newChatState(fake)
		s.selectedThreadID = "t1"
		s.attachments = []composerAttachment{{ID: "ready"}, att}
		s.sendMessage("图片")
		if s.sending || len(s.attachments) != 2 {
			t.Fatal("send accepted an incomplete attachment set")
		}
	}
}

func TestAttachmentOnlySendAndRemoveUI(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)
	s.attachments = []composerAttachment{
		{LocalID: "one", ID: "att-1", Name: "one.png"},
		{LocalID: "two", ID: "att-2", Name: "two.png"},
	}
	tt := composerTester(s)
	if err := tt.Click("删除附件 one.png"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("发送"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.sendWithAttachCalls) == 1
	})
	fake.mu.Lock()
	defer fake.mu.Unlock()
	call := fake.sendWithAttachCalls[0]
	if call.Text != "" || len(call.AttachmentIDs) != 1 || call.AttachmentIDs[0] != "att-2" {
		t.Fatalf("unexpected attachment-only send: %+v", call)
	}
}

func TestAttachmentTotalLimitAndBoundedFileRead(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	s.attachments = []composerAttachment{{Size: maxTotalAttachmentBytes}}
	if s.addAttachment("extra.png", "image/png", []byte{1}) {
		t.Fatal("accepted attachment over total limit")
	}
	path := filepath.Join(t.TempDir(), "large.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(100 * 1024 * 1024); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	s.attachments = nil
	s.addAttachmentFiles([]string{path})
	// 超限拒绝发生在后台读盘阶段，等它落地后再看提示。
	waitAttachmentFiles(t, s, 0, "20MB")
	notice := s.attachmentNotice()
	if len(s.attachments) != 0 || !strings.Contains(notice, "20MB") {
		t.Fatalf("oversized file not rejected or notice missing 20MB: %q", notice)
	}
}

// 目录枚举封顶：拖入超过 attachmentScanLimit 个文件的目录时只枚举前 attachmentScanLimit 个，
// 并在提示里说明结果被截断，避免遍历整棵树后拼出上千条拒绝项。
func TestAttachment_AddFiles_ScanLimit(t *testing.T) {
	testenv.TempHome(t)
	tmpDir := t.TempDir()
	for i := 0; i < attachmentScanLimit+5; i++ {
		name := filepath.Join(tmpDir, fmt.Sprintf("f%04d.txt", i))
		if err := os.WriteFile(name, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	files, _, truncated := collectFilesFromPath(tmpDir)
	if len(files) != attachmentScanLimit || !truncated {
		t.Fatalf("expected %d files and truncated=true, got %d/%v", attachmentScanLimit, len(files), truncated)
	}

	s := newChatState(NewFakeClient())
	s.addAttachmentFiles([]string{tmpDir})
	waitAttachmentFiles(t, s, config.DefaultComposerAttachmentLimit, "500")
	notice := s.attachmentNotice()
	if !strings.Contains(notice, "只处理前") {
		t.Fatalf("expected truncation notice, got %q", notice)
	}
}

func TestComposerPasteImage(t *testing.T) {
	testenv.TempHome(t)
	old := clipboardReader
	t.Cleanup(func() { clipboardReader = old })
	clipboardReader = func() (bool, string, string, []byte, []string) {
		return true, "pasted.png", "image/png", []byte{1, 2, 3}, nil
	}
	s := newChatState(NewFakeClient())
	tt := composerTester(s)
	tt.Key(ui.Cmd, ui.KeyV)
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.attachments) == 1
	})
	// 图片附件只显示缩略图正方形卡片，不再渲染文件名与大小。
	if _, ok := tt.Find("pasted.png"); ok {
		t.Fatal("image attachment should not render its file name")
	}
	if _, ok := tt.Find("3B"); ok {
		t.Fatal("image attachment should not render its size")
	}
	if err := tt.Click("删除附件 pasted.png"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	s.mu.Lock()
	remaining := len(s.attachments)
	s.mu.Unlock()
	if remaining != 0 {
		t.Fatal("pasted attachment was not removed")
	}
}

// estimateAttachmentTokens：图片按 宽×高/750 估算并向上取整，长边超 1568 先等比缩放，上限 1600。
func TestEstimateAttachmentTokens_Image(t *testing.T) {
	encodePNG := func(w, h int) []byte {
		var buf bytes.Buffer
		img := image.NewGray(image.Rect(0, 0, w, h))
		if err := png.Encode(&buf, img); err != nil {
			t.Fatalf("failed to encode png: %v", err)
		}
		return buf.Bytes()
	}

	cases := []struct {
		name     string
		w, h     int
		expected int
	}{
		{"10x10 向上取整为 1", 10, 10, 1},
		{"750x750 恰为 750", 750, 750, 750},
		{"2000x100 缩放后为 164", 2000, 100, 164},
		{"1600x1600 缩放后仍封顶 1600", 1600, 1600, 1600},
	}
	for _, tc := range cases {
		got := estimateAttachmentTokens("image/png", encodePNG(tc.w, tc.h), 0)
		if got != tc.expected {
			t.Fatalf("%s: expected %d, got %d", tc.name, tc.expected, got)
		}
	}

	// 解码失败按图片上限计
	if got := estimateAttachmentTokens("image/png", []byte("not a png"), 0); got != 1600 {
		t.Fatalf("expected 1600 for undecodable image, got %d", got)
	}
}

// estimateAttachmentTokens：文本/代码按字符估算，CJK 每字 1 token，其余每 4 字 1 token 向上取整。
func TestEstimateAttachmentTokens_Text(t *testing.T) {
	// 4 个 CJK + 8 个 ASCII：4 + ceil(8/4) = 6
	if got := estimateAttachmentTokens("text/plain", []byte("你好世界abcdefgh"), 0); got != 6 {
		t.Fatalf("expected 6 tokens for 4 CJK + 8 ASCII chars, got %d", got)
	}
	// 5 个 ASCII：ceil(5/4) = 2
	if got := estimateAttachmentTokens("text/plain", []byte("hello"), 0); got != 2 {
		t.Fatalf("expected 2 tokens for 5 ASCII chars, got %d", got)
	}
	// JSON 属于文本类，7 个 ASCII：ceil(7/4) = 2
	if got := estimateAttachmentTokens("application/json", []byte(`{"a":1}`), 0); got != 2 {
		t.Fatalf("expected 2 tokens for 7 ASCII chars of json, got %d", got)
	}
}

// estimateAttachmentTokens：PDF 每 3KB 约 1000 token 且至少 1000，其他二进制文件不计 token。
func TestEstimateAttachmentTokens_PDFAndBinary(t *testing.T) {
	if got := estimateAttachmentTokens("application/pdf", []byte("%PDF-1.4"), 8); got != 1000 {
		t.Fatalf("expected 1000 token floor for small pdf, got %d", got)
	}
	if got := estimateAttachmentTokens("application/pdf", nil, 6000); got != 2000 {
		t.Fatalf("expected 2000 tokens for 6KB pdf, got %d", got)
	}
	// 二进制文件不进上下文，不再按字节数估算
	if got := estimateAttachmentTokens("application/octet-stream", nil, 4000); got != 0 {
		t.Fatalf("expected 0 tokens for binary attachment, got %d", got)
	}
}

// attachmentTokenNotice：上限未知不提示，恰好 80% 不提示，超过 80% 提示，剩余最小按 0 计。
func TestAttachmentTokenNotice(t *testing.T) {
	if got := attachmentTokenNotice(5000, 0, 0); got != "" {
		t.Fatalf("expected no notice when limit is unknown, got %q", got)
	}
	if got := attachmentTokenNotice(8000, 10000, 20000); got != "" {
		t.Fatalf("expected no notice exactly at 80%% threshold, got %q", got)
	}
	got := attachmentTokenNotice(8001, 10000, 20000)
	if got == "" {
		t.Fatalf("expected notice above 80%% threshold")
	}
	if !strings.Contains(got, "80%") || !strings.Contains(got, "10.0k") {
		t.Fatalf("unexpected notice text: %q", got)
	}
	// 已用超过上限：剩余按 0 计，有附件 token 就提示
	if got := attachmentTokenNotice(1, 25000, 20000); got == "" {
		t.Fatalf("expected notice when remaining context is zero")
	}
	if got := attachmentTokenNotice(0, 25000, 20000); got != "" {
		t.Fatalf("expected no notice with zero attachment tokens, got %q", got)
	}
}

// 附件 token 估算缓存：addAttachment 写入、removeAttachment 删除。
func TestAttachmentTokenCacheLifecycle(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())

	var buf bytes.Buffer
	img := image.NewGray(image.Rect(0, 0, 10, 10))
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("failed to encode png: %v", err)
	}
	if ok := s.addAttachment("tiny.png", "image/png", buf.Bytes()); !ok {
		t.Fatalf("expected addAttachment to succeed")
	}

	s.mu.Lock()
	localID := s.attachments[0].LocalID
	s.mu.Unlock()

	v, ok := attachmentTokenCache.Load(localID)
	if !ok || v.(int) != 1 {
		t.Fatalf("expected cached estimate 1 for %s, got %v (ok=%v)", localID, v, ok)
	}

	s.removeAttachment(localID)
	if _, ok := attachmentTokenCache.Load(localID); ok {
		t.Fatalf("expected cache entry to be removed with the attachment")
	}
}
