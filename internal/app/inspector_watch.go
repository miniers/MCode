package app

import (
	"context"
	"errors"
)

// WorkspaceGeneration 返回会话工作区代次，供文件树监听刷新使用。
func (c *Client) WorkspaceGeneration(ctx context.Context, threadID string) (uint64, error) {
	if c.backend == nil {
		return 0, errors.New("backend 不可用")
	}
	return c.backend.WorkspaceGeneration(ctx, threadID)
}
