package mcodeui

import (
	"testing"

	"mcode/internal/testenv"
)

// 上传中的图片卡片在底部进度条上显示百分比：Progress 0.4 时出现「40%」。
func TestAttachmentUploadProgressShowsPercent(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	s.attachments = []composerAttachment{{
		LocalID:   "progress",
		Name:      "progress.png",
		MimeType:  "image/png",
		Uploading: true,
		Progress:  0.4,
	}}

	tt := composerTester(s)
	waitFor(t, tt, func() bool { return tt.HasText("40%") })
}

// 上传失败时卡片上有「重试上传 文件名」按钮，点击后复用现有上传路径重新上传。
func TestAttachmentRetryButtonReusesUpload(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)
	s.attachments = []composerAttachment{{
		LocalID:   "retry",
		Name:      "retry.png",
		MimeType:  "image/png",
		Err:       "网络中断",
		RetryData: []byte{0x89, 'P', 'N', 'G'},
	}}

	tt := composerTester(s)
	waitFor(t, tt, func() bool { return tt.HasText("重试上传 retry.png") })

	if err := tt.Click("重试上传 retry.png"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		calls := len(fake.uploadCalls)
		fake.mu.Unlock()
		if calls != 1 {
			return false
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.attachments) == 1 &&
			!s.attachments[0].Uploading &&
			s.attachments[0].Err == "" &&
			s.attachments[0].ID != ""
	})
}

// 删除按钮平时透明，但仍能按 a11y Label 找到并点击（非 hover 状态）。
func TestAttachmentRemoveButtonClickableWhileHidden(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	s.attachments = []composerAttachment{{
		LocalID:  "one",
		ID:       "att-1",
		Name:     "one.png",
		MimeType: "image/png",
	}}

	tt := composerTester(s)
	waitFor(t, tt, func() bool { return tt.HasText("删除附件 one.png") })

	if err := tt.Click("删除附件 one.png"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.attachments) != 0 {
		t.Fatalf("expected hidden remove button to delete the attachment, got %d left", len(s.attachments))
	}
}
