package app

import (
	"context"

	"mcode/internal/git"
)

// GitPRList 列出会话所在仓库开放的 PR/MR，limit<=0 时由 git 层取默认条数。
// 与贮藏列表一样，仓库根目录由 gitStashTarget 定位（gh/glab 需要在仓库内执行）。
func (c *Client) GitPRList(ctx context.Context, threadID string, limit int) ([]git.PRSummary, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return runner.PRList(ctx, root, limit)
}

// GitPRDetail 读取单条 PR/MR 的详情。
func (c *Client) GitPRDetail(ctx context.Context, threadID string, number int) (git.PRDetail, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return git.PRDetail{}, err
	}
	return runner.PRDetail(ctx, root, number)
}
