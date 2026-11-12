package app

import (
	"context"
	"errors"
	"fmt"

	"mcode/internal/git"
)

// gitStashTarget 定位会话工作目录所在仓库的根目录，返回执行器与根目录。
// 贮藏命令都是仓库级的，必须在根目录执行。
func (c *Client) gitStashTarget(ctx context.Context, threadID string) (*git.Runner, string, error) {
	if c.backend == nil {
		return nil, "", errors.New("backend 不可用")
	}
	workDir, ok := c.ThreadWorkDir(threadID)
	if !ok || workDir == "" {
		return nil, "", fmt.Errorf("会话 %s 没有工作目录", threadID)
	}
	runner := c.gitRunner()
	root, err := runner.RepoRoot(ctx, workDir)
	if err != nil {
		return nil, "", err
	}
	return runner, root, nil
}

// GitStashList 列出会话所在仓库的贮藏记录，最新一条在最前。
func (c *Client) GitStashList(ctx context.Context, threadID string) ([]git.StashEntry, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return runner.StashList(ctx, root)
}

// GitStashApply 应用指定序号的贮藏，记录保留；冲突时返回 git 的原始错误。
func (c *Client) GitStashApply(ctx context.Context, threadID string, index int) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.StashApply(ctx, root, index)
}

// GitStashDrop 删除指定序号的贮藏记录，不影响工作区。
func (c *Client) GitStashDrop(ctx context.Context, threadID string, index int) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.StashDrop(ctx, root, index)
}

// GitStashPopAt 应用并删除指定序号的贮藏。
func (c *Client) GitStashPopAt(ctx context.Context, threadID string, index int) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.StashPopAt(ctx, root, index)
}

// GitStashPush 创建带说明的贮藏，Runner 始终包含未跟踪文件。
func (c *Client) GitStashPush(ctx context.Context, threadID, message string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.StashPush(ctx, root, message)
}
