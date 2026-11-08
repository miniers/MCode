package mcodeui

import (
	"sync"
	"time"

	"mcode/internal/config"
)

var (
	composerAttachmentLimitMu       sync.Mutex
	composerAttachmentLimitCache    int
	composerAttachmentLimitCachedAt time.Time
)

// attachmentLimit 优先读取当前设置，否则缓存持久化配置两秒。
func (s *chatState) attachmentLimit() int {
	if s == nil {
		return config.DefaultComposerAttachmentLimit
	}
	if st := getChatSettings(s); st != nil {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.cfg.Session.AttachmentLimit()
	}
	composerAttachmentLimitMu.Lock()
	if !composerAttachmentLimitCachedAt.IsZero() && time.Since(composerAttachmentLimitCachedAt) < 2*time.Second {
		limit := composerAttachmentLimitCache
		composerAttachmentLimitMu.Unlock()
		return limit
	}
	composerAttachmentLimitMu.Unlock()

	limit := config.DefaultComposerAttachmentLimit
	if dataDir := resolveDataDirFallback(); dataDir != "" {
		if cfg, err := config.Load(dataDir); err == nil {
			limit = cfg.Session.AttachmentLimit()
		}
	}
	composerAttachmentLimitMu.Lock()
	composerAttachmentLimitCache = limit
	composerAttachmentLimitCachedAt = time.Now()
	composerAttachmentLimitMu.Unlock()
	return limit
}
