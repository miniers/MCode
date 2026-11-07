package app

import (
	"context"

	mcodeui "mcode/internal/ui"
)

// SearchMessages 跨所有未归档会话搜索消息正文，供命令面板展示「消息」类结果。
// 客户端尚未接后端时返回 nil, nil。
func (c *Client) SearchMessages(ctx context.Context, query string, limit int) ([]mcodeui.MessageSearchHit, error) {
	if c == nil || c.backend == nil {
		return nil, nil
	}
	hits, err := c.backend.SearchMessages(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]mcodeui.MessageSearchHit, len(hits))
	for i, h := range hits {
		out[i] = mcodeui.MessageSearchHit{
			ThreadID:    h.ThreadID,
			ThreadTitle: h.ThreadTitle,
			ProjectID:   h.ProjectID,
			MessageID:   h.MessageID,
			Role:        h.Role,
			Snippet:     h.Snippet,
			Seq:         h.Seq,
			CreatedAt:   h.CreatedAt,
		}
	}
	return out, nil
}
