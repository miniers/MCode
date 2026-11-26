package app

import (
	"context"
	"errors"
	"fmt"

	mcodeui "mcode/internal/ui"
)

var (
	gitPRCheckoutWorktreeFunc = (*Client).GitPRCheckoutWorktree
	gitPRSendFunc             = (*Client).Send
)

// GitPRStartReview 检出指定 PR 到独立 worktree 并新建非临时审查会话，切入 worktree 后发送 AI 审查指令。
// 改名或切 worktree 失败时删除新建会话并返回错误；首条消息发送失败时保留会话，同时返回新会话 ID 与错误。
func (c *Client) GitPRStartReview(ctx context.Context, threadID string, number int) (string, error) {
	if number <= 0 {
		return "", fmt.Errorf("无效的 PR 编号: %d", number)
	}
	if c == nil || c.backend == nil {
		return "", errors.New("backend 不可用")
	}

	detail, err := gitPRDetailFunc(c, ctx, threadID, number)
	if err != nil {
		return "", err
	}
	path, err := gitPRCheckoutWorktreeFunc(c, ctx, threadID, number, detail.HeadBranch, detail.CrossRepo)
	if err != nil {
		return "", err
	}

	th, err := c.backend.GetThread(ctx, threadID)
	if err != nil {
		return "", err
	}
	if th.ProjectID == "" {
		return "", errors.New("找不到 PR 所属项目")
	}

	id, err := c.CreateThreadWithOptions(ctx, th.ProjectID, mcodeui.NewThreadOptions{Provisional: false})
	if err != nil {
		return "", err
	}
	title := fmt.Sprintf("审查 PR #%d", number)
	if err := c.RenameThread(id, title); err != nil {
		_ = c.DeleteThread(id)
		return "", err
	}
	if err := c.GitSwitchWorktree(ctx, id, path); err != nil {
		_ = c.DeleteThread(id)
		return "", err
	}

	prompt := fmt.Sprintf(mcodeui.GitPRAIReviewPrompt, number, detail.Title, detail.BaseBranch, detail.BaseBranch, detail.BaseBranch)
	if _, err := gitPRSendFunc(c, ctx, id, prompt); err != nil {
		return id, fmt.Errorf("会话已创建，但首条消息发送失败：%w", err)
	}
	return id, nil
}
