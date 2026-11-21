package mcodeui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/testenv"
)

type blockingAttachmentUploadClient struct {
	*FakeClient
	uploadOnce    sync.Once
	uploadStarted chan struct{}
	localUpload   chan any
}

func (c *blockingAttachmentUploadClient) UploadAttachment(ctx context.Context, name, mimeType string, data []byte) (Attachment, error) {
	c.uploadOnce.Do(func() {
		c.localUpload <- ctx.Value("local_upload")
		close(c.uploadStarted)
	})
	<-ctx.Done()
	return Attachment{}, ctx.Err()
}

func TestUIReview34AttachmentUploadTimeout(t *testing.T) {
	testenv.TempHome(t)

	orig := attachmentUploadTimeout
	attachmentUploadTimeout = 50 * time.Millisecond
	t.Cleanup(func() { attachmentUploadTimeout = orig })

	client := &blockingAttachmentUploadClient{
		FakeClient:    NewFakeClient(),
		uploadStarted: make(chan struct{}),
		localUpload:   make(chan any, 1),
	}
	s := newChatState(client)
	t.Cleanup(s.close)

	data := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}
	if ok := s.addAttachment("stuck.png", "image/png", data); !ok {
		t.Fatal("expected addAttachment to succeed")
	}

	<-client.uploadStarted
	if got := <-client.localUpload; got != true {
		t.Fatalf("local_upload context value = %v, want true", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		var done bool
		var uploading bool
		var errMsg string
		if len(s.attachments) == 1 {
			uploading = s.attachments[0].Uploading
			errMsg = s.attachments[0].Err
			done = !uploading && errMsg != ""
		}
		s.mu.Unlock()
		if done {
			if toast := recallTestToast(s); !strings.HasPrefix(toast, "附件上传失败: ") {
				t.Fatalf("toast = %q, want prefix 附件上传失败: ", toast)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(s.attachments))
	}
	t.Fatalf("附件上传超时后未结束上传状态: Uploading=%v, Err=%q, toast=%q",
		s.attachments[0].Uploading, s.attachments[0].Err, s.toastMessage)
}
