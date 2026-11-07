package remote

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"mcode/internal/remote/wire"
)

// 消息全文搜索的远程扩展方法（messages.search）。
// 数据落在宿主本地 SQLite，通过 MemoryBackend.SearchMessages 检索。

const (
	messageSearchDefaultLimit = 30
	messageSearchMaxLimit     = 100
)

func init() {
	RegisterMethod("messages.search", messageSearchHandleSearch)
}

func messageSearchBackendOf(d *Dispatcher) (*MemoryBackend, error) {
	backend, ok := d.backend.(*MemoryBackend)
	if !ok || backend == nil {
		return nil, ExtNotImplemented()
	}
	return backend, nil
}

// messageSearchHandleSearch 跨未归档会话搜索用户与助手消息正文。
func messageSearchHandleSearch(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.MessageSearchParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	query := strings.TrimSpace(p.Query)
	if query == "" {
		return nil, ExtInvalidParams("query 不能为空")
	}
	limit := messageSearchDefaultLimit
	if p.Limit != nil {
		limit = *p.Limit
	}
	if limit <= 0 {
		limit = messageSearchDefaultLimit
	}
	if limit > messageSearchMaxLimit {
		limit = messageSearchMaxLimit
	}
	backend, err := messageSearchBackendOf(d)
	if err != nil {
		return nil, err
	}
	hits, err := backend.SearchMessages(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	result := wire.MessageSearchResult{Hits: make([]wire.MessageSearchHit, 0, len(hits))}
	for _, hit := range hits {
		result.Hits = append(result.Hits, wire.MessageSearchHit{
			ThreadID:    hit.ThreadID,
			ThreadTitle: hit.ThreadTitle,
			MessageID:   hit.MessageID,
			Role:        hit.Role,
			Snippet:     hit.Snippet,
			CreatedAt:   hit.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return result, nil
}
