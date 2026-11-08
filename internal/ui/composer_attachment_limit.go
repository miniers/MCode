package mcodeui

import (
	"sync"

	"mcode/internal/config"
)

var (
	composerAttachmentLimitMu     sync.Mutex
	composerAttachmentLimitCache  int
	composerAttachmentLimitCached bool
	// composerAttachmentLimitLoad 包成变量，测试可以替换它统计读盘次数。
	composerAttachmentLimitLoad = config.Load
)

// attachmentLimit 优先读取当前设置，否则首次读取持久化配置后一直复用。
// 附件上限的变更会随设置面板写回内存配置；没有设置面板时不需要再读盘。
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
	defer composerAttachmentLimitMu.Unlock()
	if composerAttachmentLimitCached {
		return composerAttachmentLimitCache
	}

	limit := config.DefaultComposerAttachmentLimit
	if dataDir := resolveDataDirFallback(); dataDir != "" {
		if cfg, err := composerAttachmentLimitLoad(dataDir); err == nil {
			limit = cfg.Session.AttachmentLimit()
		}
	}
	composerAttachmentLimitCache = limit
	composerAttachmentLimitCached = true
	return limit
}
