package app

import (
	"context"
	"errors"
)

// RevokeInspectorPreviewURL 撤销单个本地预览 URL。
func (c *Client) RevokeInspectorPreviewURL(previewURL string) {
	if c.backend != nil {
		c.backend.RevokeInspectorPreviewURL(previewURL)
	}
}

// InspectorPreviewURL 为会话工作区里的文件签发本地内容服务的 URL。
func (c *Client) InspectorPreviewURL(ctx context.Context, threadID, relPath string) (string, error) {
	if c.backend == nil {
		return "", errors.New("backend 不可用")
	}
	return c.backend.InspectorPreviewURL(ctx, threadID, relPath)
}

// RevokeInspectorPreviews 撤销某个会话的全部本地预览 Token。
func (c *Client) RevokeInspectorPreviews(threadID string) {
	if c.backend == nil || threadID == "" {
		return
	}
	c.backend.RevokeInspectorPreviews(threadID)
}
