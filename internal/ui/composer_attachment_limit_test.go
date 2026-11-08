package mcodeui

import (
	"testing"

	"mcode/internal/config"
)

// TestAttachmentLimitReadsConfigOnce 验证没有设置面板时只读一次持久化配置。
func TestAttachmentLimitReadsConfigOnce(t *testing.T) {
	t.Setenv("MCODE_DATA", settingsTestDir(t))

	oldLoad := composerAttachmentLimitLoad
	t.Cleanup(func() {
		composerAttachmentLimitMu.Lock()
		composerAttachmentLimitCached = false
		composerAttachmentLimitMu.Unlock()
		composerAttachmentLimitLoad = oldLoad
	})

	composerAttachmentLimitMu.Lock()
	composerAttachmentLimitCached = false
	composerAttachmentLimitMu.Unlock()

	calls := 0
	composerAttachmentLimitLoad = func(string) (config.Config, error) {
		calls++
		cfg := config.Defaults()
		cfg.Session.ComposerAttachmentLimit = 7
		return cfg, nil
	}

	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	if got := s.attachmentLimit(); got != 7 {
		t.Fatalf("首次读取附件上限 = %d, want 7", got)
	}
	if got := s.attachmentLimit(); got != 7 {
		t.Fatalf("第二次读取附件上限 = %d, want 7", got)
	}
	if calls != 1 {
		t.Fatalf("配置应只读一次，实际 %d 次", calls)
	}
}
