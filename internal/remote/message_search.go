package remote

import (
	"context"

	"mcode/internal/store"
)

// SearchMessages 跨所有未归档会话搜索用户与助手的消息正文，
// 供命令面板（Cmd+K）列出「消息」类结果；未接存储时返回 nil, nil。
func (b *MemoryBackend) SearchMessages(ctx context.Context, query string, limit int) ([]store.MessageSearchHit, error) {
	if b == nil {
		return nil, nil
	}
	b.mu.Lock()
	s := b.store
	b.mu.Unlock()
	if s == nil {
		return nil, nil
	}
	return s.SearchMessages(ctx, query, limit)
}
