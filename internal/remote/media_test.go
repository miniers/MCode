package remote

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

// makeTestPNG 生成指定宽高的单色 PNG 图片字节数据。
func makeTestPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

type fakeMediaHarness struct {
	mu       sync.Mutex
	lastTurn harness.Turn
	called   bool
}

func (f *fakeMediaHarness) Kind() harness.Kind {
	return "fake-media"
}

func (f *fakeMediaHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	f.mu.Lock()
	f.lastTurn = turn
	f.called = true
	f.mu.Unlock()

	emit(harness.Event{
		Type: harness.EventText,
		Text: "reply done",
	})
	emit(harness.Event{
		Type: harness.EventDone,
	})
	return nil
}

func (f *fakeMediaHarness) getLastTurn() harness.Turn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastTurn
}

// 1. 测试 upload 成功
func TestMedia_UploadSuccess(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()

	b := NewMemoryBackend(hub)
	defer b.Close()

	dataDir := t.TempDir()
	b.SetDataDir(dataDir)

	pngBytes := makeTestPNG(50, 50)
	b64Data := base64.StdEncoding.EncodeToString(pngBytes)

	res, err := b.AttachmentsUpload(context.Background(), wire.AttachmentsUploadParams{
		Name: "avatar.png",
		Data: b64Data,
	})
	if err != nil {
		t.Fatalf("AttachmentsUpload failed: %v", err)
	}

	if res.AttachmentID == "" {
		t.Fatalf("expected non-empty attachmentID")
	}
	if res.Name != "avatar.png" {
		t.Errorf("expected name 'avatar.png', got %q", res.Name)
	}
	if res.MimeType != "image/png" {
		t.Errorf("expected mimeType 'image/png', got %q", res.MimeType)
	}
	if res.ByteCount != len(pngBytes) {
		t.Errorf("expected byteCount %d, got %d", len(pngBytes), res.ByteCount)
	}
	if res.ExpiresAt == "" {
		t.Errorf("expected non-empty expiresAt")
	}

	// 检查磁盘文件存在
	savedPath := filepath.Join(dataDir, "attachments", res.AttachmentID+".png")
	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("failed to read saved attachment file: %v", err)
	}
	if !bytes.Equal(data, pngBytes) {
		t.Fatalf("saved file content mismatch")
	}
}

// 2. 单张超过 2.8MB 被拒
func TestMedia_UploadSingleTooLarge(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()

	b := NewMemoryBackend(hub)
	defer b.Close()
	b.SetDataDir(t.TempDir())

	// 构造超过 2.8MB 的数据 (2.8 * 1024 * 1024 + 10)
	size := maxAttachmentBytes + 100
	hugeData := make([]byte, size)
	// 填入 PNG 头部签名防止直接被 mime 阶段拒绝
	copy(hugeData, []byte("\x89PNG\r\n\x1a\n"))

	_, err := b.AttachmentsUpload(context.Background(), wire.AttachmentsUploadParams{
		Name: "huge.png",
		Data: base64.StdEncoding.EncodeToString(hugeData),
	})
	if err == nil {
		t.Fatalf("expected error for file exceeding 2.8MB, got nil")
	}
	rpcErr, ok := err.(*wire.RPCError)
	if !ok || rpcErr.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams (-32602), got %v", err)
	}
	if !strings.Contains(rpcErr.Message, "2.8 MiB") {
		t.Errorf("expected message mentioning 2.8 MiB, got %q", rpcErr.Message)
	}

	// 非图片文件沿用同一个远程上限，超限同样被拒
	oversizedBin := bytes.Repeat([]byte("PK\x03\x04"), (maxAttachmentBytes+100)/4+1)
	_, err = b.AttachmentsUpload(context.Background(), wire.AttachmentsUploadParams{
		Name: "archive.zip",
		Data: base64.StdEncoding.EncodeToString(oversizedBin),
	})
	if err == nil {
		t.Fatalf("expected error for non-image file exceeding 2.8 MiB, got nil")
	}
	rpcErr, ok = err.(*wire.RPCError)
	if !ok || rpcErr.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams (-32602) for non-image oversized file, got %v", err)
	}
	if !strings.Contains(rpcErr.Message, "2.8 MiB") {
		t.Errorf("expected message mentioning 2.8 MiB for non-image file, got %q", rpcErr.Message)
	}
}

// 3. 未引用附件累计超过 8MB 被拒
func TestMedia_UploadTotalTooLarge(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()

	b := NewMemoryBackend(hub)
	defer b.Close()
	b.SetDataDir(t.TempDir())

	// 单张 2.2MB 的合法 PNG
	// 构造一个高分辨率的图像让其 PNG 编码后超过 2.2MB
	img := image.NewRGBA(image.Rect(0, 0, 900, 900))
	for y := 0; y < 900; y++ {
		for x := 0; x < 900; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: uint8((x + y) % 255), A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	oneImgBytes := buf.Bytes()
	if len(oneImgBytes) < 1.5*1024*1024 || len(oneImgBytes) > maxAttachmentBytes {
		// 如果不够大，手工调整到约 2.2MB
		targetSize := 2306867 // ≈ 2.2 MiB
		if len(oneImgBytes) < targetSize {
			padding := make([]byte, targetSize-len(oneImgBytes))
			oneImgBytes = append(oneImgBytes, padding...)
		}
	}

	b64 := base64.StdEncoding.EncodeToString(oneImgBytes)

	// 上传第 1、2、3 张，直到累计超过 8MB
	totalUploaded := 0
	var lastErr error
	for i := 0; i < 5; i++ {
		_, err := b.AttachmentsUpload(context.Background(), wire.AttachmentsUploadParams{
			Name: fmt.Sprintf("chunk_%d.png", i),
			Data: b64,
		})
		if err != nil {
			lastErr = err
			break
		}
		totalUploaded += len(oneImgBytes)
	}

	if lastErr == nil {
		t.Fatalf("expected error when total exceeds 8MB, but all succeeded")
	}
	rpcErr, ok := lastErr.(*wire.RPCError)
	if !ok || rpcErr.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams error code, got %v", lastErr)
	}
	if !strings.Contains(rpcErr.Message, "8 MiB") {
		t.Errorf("expected error message to mention 8 MiB, got %q", rpcErr.Message)
	}
}

// 4. 把时钟调快 1 小时以上后附件被清理（文件被删、send 时引用它报 ResourceNotFound）
func TestMedia_AttachmentExpiredAndCleanup(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()

	b := NewMemoryBackend(hub)
	defer b.Close()
	dataDir := t.TempDir()
	b.SetDataDir(dataDir)

	mockTime := time.Now()
	b.SetClock(func() time.Time {
		return mockTime
	})

	pngBytes := makeTestPNG(20, 20)
	res, err := b.AttachmentsUpload(context.Background(), wire.AttachmentsUploadParams{
		Name: "photo.png",
		Data: base64.StdEncoding.EncodeToString(pngBytes),
	})
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}

	targetFile := filepath.Join(dataDir, "attachments", res.AttachmentID+".png")
	if _, err := os.Stat(targetFile); err != nil {
		t.Fatalf("file should exist before expiration: %v", err)
	}

	// 创建一个测试用的会话
	projID := newUUID()
	b.AddProject(wire.ProjectSummary{ID: projID, Name: "test-proj", Path: t.TempDir()})
	thRes, err := b.CreateThread(context.Background(), wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("create thread failed: %v", err)
	}

	// 时钟调快 61 分钟
	mockTime = mockTime.Add(61 * time.Minute)

	// SendMessage 引用已过期的附件
	_, err = b.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID:    thRes.ThreadID,
		Text:        "send with expired attachment",
		Attachments: []string{res.AttachmentID},
	})
	if err == nil {
		t.Fatalf("expected ResourceNotFound for expired attachment, got nil")
	}
	rpcErr, ok := err.(*wire.RPCError)
	if !ok || rpcErr.Code != wire.ErrCodeResourceNotFound {
		t.Fatalf("expected ErrCodeResourceNotFound (-32002), got %v", err)
	}

	// 验证文件已经被删除
	if _, err := os.Stat(targetFile); !os.IsNotExist(err) {
		t.Errorf("expected expired file to be deleted, but stat returned %v", err)
	}
}

// 5. 非法 base64 被拒；非图片附件被接受；图片声明类型与内容不符被拒
func TestMedia_InvalidBase64AndMime(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()

	b := NewMemoryBackend(hub)
	defer b.Close()
	b.SetDataDir(t.TempDir())

	// 非法 base64
	_, err := b.AttachmentsUpload(context.Background(), wire.AttachmentsUploadParams{
		Name: "bad.png",
		Data: "invalid-base64-payload???",
	})
	if err == nil {
		t.Fatalf("expected error for invalid base64")
	}
	if rpcErr, ok := err.(*wire.RPCError); !ok || rpcErr.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for bad base64, got %v", err)
	}

	// 非图片数据（文本文件）如今与桌面端一致地被接受
	textBody := []byte("hello plain text world")
	textB64 := base64.StdEncoding.EncodeToString(textBody)
	res, err := b.AttachmentsUpload(context.Background(), wire.AttachmentsUploadParams{
		Name: "test.txt",
		Data: textB64,
	})
	if err != nil {
		t.Fatalf("expected non-image upload to succeed, got %v", err)
	}
	if res.MimeType != "text/plain" {
		t.Errorf("expected text/plain for .txt upload, got %q", res.MimeType)
	}
	if res.Name != "test.txt" {
		t.Errorf("expected original file name preserved, got %q", res.Name)
	}

	// 无扩展名的二进制数据也能落盘（兜底 octet-stream）
	binBody := []byte{0x00, 0x01, 0x02, 0x03, 0xff}
	res, err = b.AttachmentsUpload(context.Background(), wire.AttachmentsUploadParams{
		Name: "blob",
		Data: base64.StdEncoding.EncodeToString(binBody),
	})
	if err != nil {
		t.Fatalf("expected extension-less binary upload to succeed, got %v", err)
	}
	if res.MimeType != "application/octet-stream" {
		t.Errorf("expected application/octet-stream fallback, got %q", res.MimeType)
	}

	// 声明类型与内容不匹配
	pngBytes := makeTestPNG(10, 10)
	declMime := "image/jpeg"
	_, err = b.AttachmentsUpload(context.Background(), wire.AttachmentsUploadParams{
		Name:     "fake.jpg",
		MimeType: &declMime,
		Data:     base64.StdEncoding.EncodeToString(pngBytes),
	})
	if err == nil {
		t.Fatalf("expected error for mime mismatch")
	}
	if rpcErr, ok := err.(*wire.RPCError); !ok || rpcErr.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for mime mismatch, got %v", err)
	}
}

// 6. 带附件的 sendMessage 用 fake harness 捕获 Turn.Images 与 Prompt 中的路径，并检查用户消息带上了 MediaRef
func TestMedia_SendMessageWithAttachments(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()

	b := NewMemoryBackend(hub)
	defer b.Close()
	dataDir := t.TempDir()
	b.SetDataDir(dataDir)

	fakeH := &fakeMediaHarness{}
	b.RegisterHarness(fakeH)

	projID := newUUID()
	b.AddProject(wire.ProjectSummary{ID: projID, Name: "media-proj", Path: t.TempDir()})
	thRes, err := b.CreateThread(context.Background(), wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	// 上传 1 张 100x80 的 PNG 图片
	pngBytes := makeTestPNG(100, 80)
	upRes, err := b.AttachmentsUpload(context.Background(), wire.AttachmentsUploadParams{
		Name: "diagram.png",
		Data: base64.StdEncoding.EncodeToString(pngBytes),
	})
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}

	// 发送带附件的消息
	sendRes, err := b.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID:    thRes.ThreadID,
		Text:        "查看这张架构图",
		ActionID:    newUUID(),
		Attachments: []string{upRes.AttachmentID},
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	if sendRes.MessageID == nil {
		t.Fatalf("expected non-nil MessageID")
	}

	// 等待 harness 被调用
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		turn := fakeH.getLastTurn()
		if len(turn.Images) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	turn := fakeH.getLastTurn()
	if len(turn.Images) != 1 {
		t.Fatalf("expected 1 image in turn.Images, got %d", len(turn.Images))
	}

	expectedPath := filepath.Join(dataDir, "media", thRes.ThreadID, upRes.AttachmentID+".png")
	if turn.Images[0] != expectedPath {
		t.Errorf("expected image path %q, got %q", expectedPath, turn.Images[0])
	}

	if !strings.Contains(turn.Prompt, expectedPath) {
		t.Errorf("expected prompt to contain image path, got: %s", turn.Prompt)
	}
	if !strings.Contains(turn.Prompt, "用户附带了以下本地文件附件") {
		t.Errorf("expected prompt to contain chinese notice, got: %s", turn.Prompt)
	}
	// 有原始文件名时按「- <文件名>：<路径>」列出。
	if !strings.Contains(turn.Prompt, "- diagram.png："+expectedPath) {
		t.Errorf("expected prompt to list attachment name with path, got: %s", turn.Prompt)
	}

	// 检查消息历史记录中的 MediaRef
	hist, err := b.ThreadHistory(context.Background(), wire.HistoryParams{
		ThreadID: thRes.ThreadID,
	})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	var userMsg *wire.ThreadMessage
	for i := range hist.Messages {
		if hist.Messages[i].ID == *sendRes.MessageID {
			userMsg = &hist.Messages[i]
			break
		}
	}
	if userMsg == nil {
		t.Fatalf("user message not found in history")
	}
	if len(userMsg.Media) != 1 {
		t.Fatalf("expected 1 MediaRef on user message, got %d", len(userMsg.Media))
	}
	ref := userMsg.Media[0]
	if ref.ID != upRes.AttachmentID {
		t.Errorf("expected MediaRef ID %q, got %q", upRes.AttachmentID, ref.ID)
	}
	if ref.Source != "attachment" {
		t.Errorf("expected MediaRef Source 'attachment', got %q", ref.Source)
	}
	if ref.Name != "diagram.png" {
		t.Errorf("expected MediaRef Name 'diagram.png', got %q", ref.Name)
	}
	if ref.MimeType != "image/png" {
		t.Errorf("expected MediaRef MimeType 'image/png', got %q", ref.MimeType)
	}
	if ref.Width == nil || *ref.Width != 100 {
		t.Errorf("expected width 100, got %v", ref.Width)
	}
	if ref.Height == nil || *ref.Height != 80 {
		t.Errorf("expected height 80, got %v", ref.Height)
	}
}

// 6b. 多个非图片附件（PDF + PNG）的 prompt 提示使用通用文件文案并带原始文件名
func TestMedia_SendMessageWithMixedAttachmentsListsNames(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()

	b := NewMemoryBackend(hub)
	defer b.Close()
	dataDir := t.TempDir()
	b.SetDataDir(dataDir)

	fakeH := &fakeMediaHarness{}
	b.RegisterHarness(fakeH)

	projID := newUUID()
	b.AddProject(wire.ProjectSummary{ID: projID, Name: "media-proj", Path: t.TempDir()})
	thRes, err := b.CreateThread(context.Background(), wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	// 本地进程上传允许非图片内容，因此可以传 PDF。
	pdfMime := "application/pdf"
	pdfRes, err := b.AttachmentsUpload(WithLocalUpload(context.Background()), wire.AttachmentsUploadParams{
		Name:     "report.pdf",
		Data:     base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\n%test\n")),
		MimeType: &pdfMime,
	})
	if err != nil {
		t.Fatalf("upload pdf failed: %v", err)
	}
	pngRes, err := b.AttachmentsUpload(context.Background(), wire.AttachmentsUploadParams{
		Name: "a.png",
		Data: base64.StdEncoding.EncodeToString(makeTestPNG(20, 10)),
	})
	if err != nil {
		t.Fatalf("upload png failed: %v", err)
	}

	if _, err := b.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID:    thRes.ThreadID,
		Text:        "看这两个文件",
		ActionID:    newUUID(),
		Attachments: []string{pdfRes.AttachmentID, pngRes.AttachmentID},
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(fakeH.getLastTurn().Images) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	turn := fakeH.getLastTurn()
	if len(turn.Images) != 2 {
		t.Fatalf("expected 2 attachments in turn.Images, got %d: %v", len(turn.Images), turn.Images)
	}
	if !strings.Contains(turn.Prompt, "用户附带了以下本地文件附件，可使用文件读取工具查看：") {
		t.Errorf("expected generic file notice, got: %s", turn.Prompt)
	}
	if strings.Contains(turn.Prompt, "本地图片附件") {
		t.Errorf("prompt must not keep old image-only notice, got: %s", turn.Prompt)
	}
	pdfPath := filepath.Join(dataDir, "media", thRes.ThreadID, pdfRes.AttachmentID)
	pngPath := filepath.Join(dataDir, "media", thRes.ThreadID, pngRes.AttachmentID)
	if !strings.Contains(turn.Prompt, "\n- report.pdf："+pdfPath) {
		t.Errorf("expected prompt to list report.pdf with path, got: %s", turn.Prompt)
	}
	if !strings.Contains(turn.Prompt, "\n- a.png："+pngPath) {
		t.Errorf("expected prompt to list a.png with path, got: %s", turn.Prompt)
	}
}

// 7. threads.media.read 的 full 分块（offset/nextOffset/truncated）
func TestMedia_ReadFullChunking(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()

	b := NewMemoryBackend(hub)
	defer b.Close()
	dataDir := t.TempDir()
	b.SetDataDir(dataDir)

	projID := newUUID()
	b.AddProject(wire.ProjectSummary{ID: projID, Name: "chunk-proj", Path: t.TempDir()})
	thRes, _ := b.CreateThread(context.Background(), wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  newUUID(),
	})

	// 制作一个超过 256KB 的 PNG 图片（例如 600KB）
	imgData := makeTestPNG(600, 600)
	if len(imgData) < 300*1024 {
		padding := make([]byte, 350*1024-len(imgData))
		imgData = append(imgData, padding...)
	}
	totalLen := int64(len(imgData))

	upRes, err := b.AttachmentsUpload(context.Background(), wire.AttachmentsUploadParams{
		Name: "large.png",
		Data: base64.StdEncoding.EncodeToString(imgData),
	})
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}

	sendRes, err := b.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID:    thRes.ThreadID,
		Text:        "large image message",
		Attachments: []string{upRes.AttachmentID},
	})
	if err != nil {
		t.Fatalf("send message failed: %v", err)
	}

	// 第 1 块读取（offset = 0）
	offset0 := int64(0)
	res1, err := b.ThreadsMediaRead(context.Background(), wire.ThreadsMediaReadParams{
		ThreadID:  thRes.ThreadID,
		MessageID: *sendRes.MessageID,
		MediaID:   upRes.AttachmentID,
		Variant:   wire.MediaVariantFull,
		Offset:    &offset0,
	})
	if err != nil {
		t.Fatalf("read chunk 1 failed: %v", err)
	}
	if !res1.Truncated {
		t.Errorf("expected chunk 1 to be truncated")
	}
	if res1.NextOffset == nil || *res1.NextOffset != maxChunkBytes {
		t.Errorf("expected chunk 1 nextOffset %d, got %v", maxChunkBytes, res1.NextOffset)
	}
	if res1.TotalBytes != totalLen {
		t.Errorf("expected totalBytes %d, got %d", totalLen, res1.TotalBytes)
	}

	chunk1Bytes, _ := base64.StdEncoding.DecodeString(res1.Base64)
	if len(chunk1Bytes) != maxChunkBytes {
		t.Errorf("expected chunk 1 length %d, got %d", maxChunkBytes, len(chunk1Bytes))
	}

	// 第 2 块读取
	res2, err := b.ThreadsMediaRead(context.Background(), wire.ThreadsMediaReadParams{
		ThreadID:  thRes.ThreadID,
		MessageID: *sendRes.MessageID,
		MediaID:   upRes.AttachmentID,
		Variant:   wire.MediaVariantFull,
		Offset:    res1.NextOffset,
	})
	if err != nil {
		t.Fatalf("read chunk 2 failed: %v", err)
	}
	if res2.Offset != maxChunkBytes {
		t.Errorf("expected chunk 2 offset %d, got %d", maxChunkBytes, res2.Offset)
	}
	if res2.Truncated {
		t.Errorf("expected chunk 2 not truncated for ~350KB total")
	}
	if res2.NextOffset != nil {
		t.Errorf("expected chunk 2 nextOffset nil, got %v", res2.NextOffset)
	}

	chunk2Bytes, _ := base64.StdEncoding.DecodeString(res2.Base64)
	combined := append(chunk1Bytes, chunk2Bytes...)
	if !bytes.Equal(combined, imgData) {
		t.Errorf("combined chunks data mismatch with original")
	}
}

// 8. thumb 输出是 JPEG 且长边不超过 480（用 1200x600 的 png 生成测试）
func TestMedia_ThumbScaling(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()

	b := NewMemoryBackend(hub)
	defer b.Close()
	dataDir := t.TempDir()
	b.SetDataDir(dataDir)

	projID := newUUID()
	b.AddProject(wire.ProjectSummary{ID: projID, Name: "thumb-proj", Path: t.TempDir()})
	thRes, _ := b.CreateThread(context.Background(), wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  newUUID(),
	})

	// 1200 x 600 的 PNG 原图
	origPNG := makeTestPNG(1200, 600)
	upRes, err := b.AttachmentsUpload(context.Background(), wire.AttachmentsUploadParams{
		Name: "wide.png",
		Data: base64.StdEncoding.EncodeToString(origPNG),
	})
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}

	sendRes, err := b.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID:    thRes.ThreadID,
		Text:        "wide image",
		Attachments: []string{upRes.AttachmentID},
	})
	if err != nil {
		t.Fatalf("send message failed: %v", err)
	}

	readRes, err := b.ThreadsMediaRead(context.Background(), wire.ThreadsMediaReadParams{
		ThreadID:  thRes.ThreadID,
		MessageID: *sendRes.MessageID,
		MediaID:   upRes.AttachmentID,
		Variant:   wire.MediaVariantThumb,
	})
	if err != nil {
		t.Fatalf("ThreadsMediaRead thumb failed: %v", err)
	}

	if readRes.MimeType != "image/jpeg" {
		t.Errorf("expected thumb mimeType 'image/jpeg', got %q", readRes.MimeType)
	}

	thumbBytes, err := base64.StdEncoding.DecodeString(readRes.Base64)
	if err != nil {
		t.Fatalf("base64 decode thumb failed: %v", err)
	}

	cfg, err := jpeg.DecodeConfig(bytes.NewReader(thumbBytes))
	if err != nil {
		t.Fatalf("jpeg decode config failed: %v", err)
	}

	if cfg.Width != 480 {
		t.Errorf("expected thumb width 480, got %d", cfg.Width)
	}
	if cfg.Height != 240 {
		t.Errorf("expected thumb height 240, got %d", cfg.Height)
	}
}

// 9. mediaID 或 messageID 不存在时返回 ResourceNotFound
func TestMedia_NotFound(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()

	b := NewMemoryBackend(hub)
	defer b.Close()
	b.SetDataDir(t.TempDir())

	projID := newUUID()
	b.AddProject(wire.ProjectSummary{ID: projID, Name: "nf-proj", Path: t.TempDir()})
	thRes, _ := b.CreateThread(context.Background(), wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  newUUID(),
	})

	validUUID1 := newUUID()
	validUUID2 := newUUID()

	// 查不存在的 messageID
	_, err := b.ThreadsMediaRead(context.Background(), wire.ThreadsMediaReadParams{
		ThreadID:  thRes.ThreadID,
		MessageID: validUUID1,
		MediaID:   validUUID2,
		Variant:   wire.MediaVariantFull,
	})
	if err == nil {
		t.Fatalf("expected error for non-existent messageID")
	}
	if rpcErr, ok := err.(*wire.RPCError); !ok || rpcErr.Code != wire.ErrCodeResourceNotFound {
		t.Errorf("expected ErrCodeResourceNotFound (-32002), got %v", err)
	}

	// 发送一条没有 media 的消息
	sendRes, _ := b.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID: thRes.ThreadID,
		Text:     "no media here",
	})

	// 查不存在的 mediaID
	_, err = b.ThreadsMediaRead(context.Background(), wire.ThreadsMediaReadParams{
		ThreadID:  thRes.ThreadID,
		MessageID: *sendRes.MessageID,
		MediaID:   newUUID(),
		Variant:   wire.MediaVariantFull,
	})
	if err == nil {
		t.Fatalf("expected error for non-existent mediaID")
	}
	if rpcErr, ok := err.(*wire.RPCError); !ok || rpcErr.Code != wire.ErrCodeResourceNotFound {
		t.Errorf("expected ErrCodeResourceNotFound (-32002), got %v", err)
	}
}

// 11. 宿主重启后遗留的孤儿暂存附件：超过 1 小时的被清理，新文件保留
func TestMedia_OrphanAttachmentSweep(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()

	b := NewMemoryBackend(hub)
	defer b.Close()
	dataDir := t.TempDir()
	b.SetDataDir(dataDir)

	attachmentsDir := filepath.Join(dataDir, "attachments")
	if err := os.MkdirAll(attachmentsDir, 0700); err != nil {
		t.Fatal(err)
	}

	stalePath := filepath.Join(attachmentsDir, newUUID())
	if err := os.WriteFile(stalePath, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-90 * time.Minute)
	if err := os.Chtimes(stalePath, old, old); err != nil {
		t.Fatal(err)
	}

	freshPath := filepath.Join(attachmentsDir, newUUID())
	if err := os.WriteFile(freshPath, []byte("fresh"), 0600); err != nil {
		t.Fatal(err)
	}

	// 新上传会顺带清理过期的孤儿文件
	pngBytes := makeTestPNG(10, 10)
	if _, err := b.AttachmentsUpload(context.Background(), wire.AttachmentsUploadParams{
		Name: "new.png",
		Data: base64.StdEncoding.EncodeToString(pngBytes),
	}); err != nil {
		t.Fatalf("upload failed: %v", err)
	}

	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Errorf("expected stale orphan file to be removed, stat returned %v", err)
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Errorf("expected fresh orphan file to be kept, stat returned %v", err)
	}
}

// 12. webp 附件也能生成 thumb（x/image/webp 纯 Go 解码），长边不超过 480
func TestMedia_ThumbWebP(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()

	b := NewMemoryBackend(hub)
	defer b.Close()
	dataDir := t.TempDir()
	b.SetDataDir(dataDir)

	projID := newUUID()
	b.AddProject(wire.ProjectSummary{ID: projID, Name: "webp-proj", Path: t.TempDir()})
	thRes, err := b.CreateThread(context.Background(), wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	upRes, err := b.AttachmentsUpload(context.Background(), wire.AttachmentsUploadParams{
		Name: "photo.webp",
		Data: testWebPBase64,
	})
	if err != nil {
		t.Fatalf("webp upload failed: %v", err)
	}
	if upRes.MimeType != "image/webp" {
		t.Errorf("expected mimeType 'image/webp', got %q", upRes.MimeType)
	}

	sendRes, err := b.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID:    thRes.ThreadID,
		Text:        "webp image",
		Attachments: []string{upRes.AttachmentID},
	})
	if err != nil {
		t.Fatalf("send message failed: %v", err)
	}

	readRes, err := b.ThreadsMediaRead(context.Background(), wire.ThreadsMediaReadParams{
		ThreadID:  thRes.ThreadID,
		MessageID: *sendRes.MessageID,
		MediaID:   upRes.AttachmentID,
		Variant:   wire.MediaVariantThumb,
	})
	if err != nil {
		t.Fatalf("ThreadsMediaRead thumb for webp failed: %v", err)
	}
	if readRes.MimeType != "image/jpeg" {
		t.Errorf("expected thumb mimeType 'image/jpeg', got %q", readRes.MimeType)
	}

	thumbBytes, err := base64.StdEncoding.DecodeString(readRes.Base64)
	if err != nil {
		t.Fatalf("base64 decode thumb failed: %v", err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(thumbBytes))
	if err != nil {
		t.Fatalf("jpeg decode config failed: %v", err)
	}
	if cfg.Width != 120 || cfg.Height != 60 {
		t.Errorf("expected thumb 120x60, got %dx%d", cfg.Width, cfg.Height)
	}
}

// testWebPBase64 是一张 120x60 的合法 webp 图片（由 cwebp 生成），用于验证 webp 缩略图链路。
const testWebPBase64 = "UklGRsQAAABXRUJQVlA4ILgAAACwBgCdASp4ADwAPm02mUmkIqKhIQgAgA2JZwaGZK3IPj8AIPjklO37wv///////uH///////lJ+D////+5AAD++n55oYs/7Xd5u4EeZdDBc6Q9+VEcQG451Mna8jJIGttb50aP3tztbxWBztD1QnI/ewgoW2AwEb3h7ngRJG1fVYm8BIzFO5hfIVtvXLgdffS/uLbXs1bBQFoTWIjxDq44QpLXInushB+5+l+3myU5w4QuUypxwAAA"

// 13. 测试 Dispatcher 层的 handleAttachmentsUpload 和 handleThreadsMediaRead 参数校验与路由
func TestMedia_DispatcherHandlers(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()
	backend.SetDataDir(t.TempDir())

	d := NewDispatcher(&fakeAuth{}, backend)

	replySuccess := func(res any) *wire.Response {
		return &wire.Response{
			JSONRPC: "2.0",
			Result:  res,
		}
	}
	replyErr := func(code int, msg string) *wire.Response {
		return &wire.Response{
			JSONRPC: "2.0",
			Error: &wire.RPCError{
				Code:    code,
				Message: msg,
			},
		}
	}
	replyWithBackendErr := func(err error) *wire.Response {
		if rpcErr, ok := err.(*wire.RPCError); ok {
			return &wire.Response{
				JSONRPC: "2.0",
				Error:   rpcErr,
			}
		}
		return replyErr(wire.InternalError, err.Error())
	}

	ctx := context.Background()

	// 1. handleAttachmentsUpload: 格式错误
	badParamsJSON := json.RawMessage(`{"unknownField": "test"}`)
	resp := d.handleAttachmentsUpload(ctx, badParamsJSON, replySuccess, replyErr, replyWithBackendErr)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for unknownField, got %+v", resp.Error)
	}

	// 2. handleAttachmentsUpload: 成功
	pngBytes := makeTestPNG(10, 10)
	goodParamsJSON, _ := json.Marshal(wire.AttachmentsUploadParams{
		Name: "test.png",
		Data: base64.StdEncoding.EncodeToString(pngBytes),
	})
	resp = d.handleAttachmentsUpload(ctx, goodParamsJSON, replySuccess, replyErr, replyWithBackendErr)
	if resp.Error != nil {
		t.Fatalf("expected upload success, got %+v", resp.Error)
	}
	upResult, ok := resp.Result.(wire.AttachmentsUploadResult)
	if !ok || upResult.AttachmentID == "" {
		t.Fatalf("unexpected upload result: %+v", resp.Result)
	}

	// 3. handleThreadsMediaRead: 非法 UUID
	badUUIDParamsJSON, _ := json.Marshal(wire.ThreadsMediaReadParams{
		ThreadID:  "not-a-uuid",
		MessageID: newUUID(),
		MediaID:   upResult.AttachmentID,
		Variant:   wire.MediaVariantFull,
	})
	resp = d.handleThreadsMediaRead(ctx, badUUIDParamsJSON, replySuccess, replyErr, replyWithBackendErr)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for invalid threadID UUID, got %+v", resp.Error)
	}
}

// 测试本地桌面进程内上传：允许超过 2.8MB、支持 PDF/代码类型、单文件超 20MB 拒绝
func TestMedia_UploadLocalUploadLimitAndMime(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()

	b := NewMemoryBackend(hub)
	defer b.Close()
	b.SetDataDir(t.TempDir())

	localCtx := WithLocalUpload(context.Background())

	// 1. 超过 2.8MB 但小于 20MB 的 PDF 文件在本地上传应成功
	pdfBytes := append([]byte("%PDF-1.4\n"), make([]byte, 3*1024*1024)...)
	pdfMime := "application/pdf"
	res, err := b.AttachmentsUpload(localCtx, wire.AttachmentsUploadParams{
		Name:     "document.pdf",
		MimeType: &pdfMime,
		Data:     base64.StdEncoding.EncodeToString(pdfBytes),
	})
	if err != nil {
		t.Fatalf("expected local upload of 3MB PDF to succeed, got %v", err)
	}
	if res.MimeType != "application/pdf" {
		t.Errorf("expected mimeType application/pdf, got %s", res.MimeType)
	}

	// 2. 本地单文件超过 20MB 应被拒绝
	over20MB := make([]byte, maxLocalAttachmentBytes+10)
	_, err = b.AttachmentsUpload(localCtx, wire.AttachmentsUploadParams{
		Name: "large.pdf",
		Data: base64.StdEncoding.EncodeToString(over20MB),
	})
	if err == nil || !strings.Contains(err.Error(), "20 MiB") {
		t.Fatalf("expected error mentioning 20 MiB, got %v", err)
	}

	// 3. 非本地 context 超过 2.8MB 仍必须被拒绝以保持远程行为
	remoteCtx := context.Background()
	_, err = b.AttachmentsUpload(remoteCtx, wire.AttachmentsUploadParams{
		Name: "avatar.png",
		Data: base64.StdEncoding.EncodeToString(pdfBytes),
	})
	if err == nil || !strings.Contains(err.Error(), "2.8 MiB") {
		t.Fatalf("expected remote upload over 2.8 MiB to fail, got %v", err)
	}
}

// attachmentFileExt 只保留安全的原始扩展名，缺扩展名时按 MIME 类型兜底，取不到时返回空串
func TestMedia_AttachmentFileExt(t *testing.T) {
	cases := []struct {
		name     string
		mimeType string
		want     string
	}{
		{"report.pdf", "application/octet-stream", ".pdf"},
		{"REPORT.PDF", "", ".pdf"},
		{"a../../b", "application/pdf", ".pdf"},
		{"weird.p!f", "application/pdf", ".pdf"},
		{"diagram.png", "image/png", ".png"},
		{"photo", "image/jpeg", ".jpg"},
		{"noext", "", ""},
		{"name.", "", ""},
		{"name.verylongextension", "", ""},
	}
	for _, tc := range cases {
		if got := attachmentFileExt(tc.name, tc.mimeType); got != tc.want {
			t.Errorf("attachmentFileExt(%q, %q) = %q, want %q", tc.name, tc.mimeType, got, tc.want)
		}
	}
}

// 附件落盘保留扩展名：暂存文件与发送后复制到 media/<thread>/ 的文件都以 .pdf 结尾，
// 且 threads.media.read 能读回相同字节
func TestMedia_AttachmentKeepsFileExtension(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()

	b := NewMemoryBackend(hub)
	defer b.Close()
	dataDir := t.TempDir()
	b.SetDataDir(dataDir)
	b.RegisterHarness(&fakeMediaHarness{})

	pdfBytes := []byte("%PDF-1.4\n% attachment extension test\n")
	pdfMime := "application/pdf"
	upRes, err := b.AttachmentsUpload(WithLocalUpload(context.Background()), wire.AttachmentsUploadParams{
		Name:     "report.pdf",
		MimeType: &pdfMime,
		Data:     base64.StdEncoding.EncodeToString(pdfBytes),
	})
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}

	stagedPath := filepath.Join(dataDir, "attachments", upRes.AttachmentID+".pdf")
	if _, err := os.Stat(stagedPath); err != nil {
		t.Fatalf("expected staged attachment %q: %v", stagedPath, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "attachments", upRes.AttachmentID)); !os.IsNotExist(err) {
		t.Errorf("expected no extension-less staged file, stat returned %v", err)
	}

	projID := newUUID()
	b.AddProject(wire.ProjectSummary{ID: projID, Name: "attach-ext", Path: t.TempDir()})
	thRes, err := b.CreateThread(context.Background(), wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	sendRes, err := b.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID:    thRes.ThreadID,
		Text:        "看看这个 PDF",
		ActionID:    newUUID(),
		Attachments: []string{upRes.AttachmentID},
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	if sendRes.MessageID == nil {
		t.Fatalf("expected non-nil MessageID")
	}

	mediaPath := filepath.Join(dataDir, "media", thRes.ThreadID, upRes.AttachmentID+".pdf")
	if _, err := os.Stat(mediaPath); err != nil {
		t.Fatalf("expected media copy %q: %v", mediaPath, err)
	}

	readRes, err := b.ThreadsMediaRead(context.Background(), wire.ThreadsMediaReadParams{
		ThreadID:  thRes.ThreadID,
		MessageID: *sendRes.MessageID,
		MediaID:   upRes.AttachmentID,
		Variant:   wire.MediaVariantFull,
	})
	if err != nil {
		t.Fatalf("ThreadsMediaRead failed: %v", err)
	}
	got, err := base64.StdEncoding.DecodeString(readRes.Base64)
	if err != nil {
		t.Fatalf("decode media bytes failed: %v", err)
	}
	if !bytes.Equal(got, pdfBytes) {
		t.Fatalf("media bytes mismatch: got %d bytes, want %d", len(got), len(pdfBytes))
	}
}

// 带路径穿越成分或奇怪扩展名的文件名不会导致越出暂存目录，扩展名被丢弃或净化
func TestMedia_AttachmentFileNameSanitized(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()

	b := NewMemoryBackend(hub)
	defer b.Close()
	dataDir := t.TempDir()
	b.SetDataDir(dataDir)

	attachmentsDir := filepath.Join(dataDir, "attachments")
	cases := []struct {
		name     string
		mimeType string
		wantExt  string
	}{
		{"a../../b", "application/pdf", ".pdf"},
		{"../../etc/passwd", "image/png", ".png"},
		{"weird.p!f", "application/pdf", ".pdf"},
	}
	for _, tc := range cases {
		localCtx := WithLocalUpload(context.Background())
		upRes, err := b.AttachmentsUpload(localCtx, wire.AttachmentsUploadParams{
			Name:     tc.name,
			MimeType: &tc.mimeType,
			Data:     base64.StdEncoding.EncodeToString(makeTestPNG(8, 8)),
		})
		if err != nil {
			t.Fatalf("upload %q failed: %v", tc.name, err)
		}
		rec, ok := b.attachments.get(upRes.AttachmentID)
		if !ok {
			t.Fatalf("attachment record for %q missing", tc.name)
		}
		if dir := filepath.Dir(rec.path); dir != attachmentsDir {
			t.Fatalf("attachment %q landed in %q, want %q", tc.name, dir, attachmentsDir)
		}
		wantName := upRes.AttachmentID + tc.wantExt
		if filepath.Base(rec.path) != wantName {
			t.Fatalf("attachment %q stored as %q, want %q", tc.name, filepath.Base(rec.path), wantName)
		}
		if _, err := os.Stat(rec.path); err != nil {
			t.Fatalf("expected attachment file %q: %v", rec.path, err)
		}
	}
}

// 历史数据中无扩展名的媒体文件仍能被 threads.media.read 读取
func TestMedia_LegacyMediaFileWithoutExt(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()

	b := NewMemoryBackend(hub)
	defer b.Close()
	dataDir := t.TempDir()
	b.SetDataDir(dataDir)
	b.RegisterHarness(&fakeMediaHarness{})

	pngBytes := makeTestPNG(32, 24)
	upRes, err := b.AttachmentsUpload(context.Background(), wire.AttachmentsUploadParams{
		Name: "legacy.png",
		Data: base64.StdEncoding.EncodeToString(pngBytes),
	})
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}

	projID := newUUID()
	b.AddProject(wire.ProjectSummary{ID: projID, Name: "legacy-media", Path: t.TempDir()})
	thRes, err := b.CreateThread(context.Background(), wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	sendRes, err := b.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID:    thRes.ThreadID,
		Text:        "旧格式媒体",
		ActionID:    newUUID(),
		Attachments: []string{upRes.AttachmentID},
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	if sendRes.MessageID == nil {
		t.Fatalf("expected non-nil MessageID")
	}

	// 模拟历史数据：媒体目录里只有裸 ID 文件
	legacyPath := filepath.Join(dataDir, "media", thRes.ThreadID, upRes.AttachmentID)
	if err := os.Rename(legacyPath+".png", legacyPath); err != nil {
		t.Fatalf("rename to legacy path failed: %v", err)
	}

	readRes, err := b.ThreadsMediaRead(context.Background(), wire.ThreadsMediaReadParams{
		ThreadID:  thRes.ThreadID,
		MessageID: *sendRes.MessageID,
		MediaID:   upRes.AttachmentID,
		Variant:   wire.MediaVariantFull,
	})
	if err != nil {
		t.Fatalf("ThreadsMediaRead on legacy path failed: %v", err)
	}
	got, err := base64.StdEncoding.DecodeString(readRes.Base64)
	if err != nil {
		t.Fatalf("decode media bytes failed: %v", err)
	}
	if !bytes.Equal(got, pngBytes) {
		t.Fatalf("legacy media bytes mismatch")
	}

	// 缩略图也应能从无扩展名文件生成
	if _, err := b.ThreadsMediaRead(context.Background(), wire.ThreadsMediaReadParams{
		ThreadID:  thRes.ThreadID,
		MessageID: *sendRes.MessageID,
		MediaID:   upRes.AttachmentID,
		Variant:   wire.MediaVariantThumb,
	}); err != nil {
		t.Fatalf("ThreadsMediaRead thumb on legacy path failed: %v", err)
	}
}
