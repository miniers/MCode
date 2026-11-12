package app

import (
	"context"

	"mcode/internal/git"
)

// GitLog 调用会话所在仓库的 Git 操作。
func (c *Client) GitLog(ctx context.Context, threadID string, opts git.LogOptions) ([]git.LogCommit, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return runner.Log(ctx, root, opts)
}

// GitShowCommit 调用会话所在仓库的 Git 操作。
func (c *Client) GitShowCommit(ctx context.Context, threadID, sha string) (git.CommitDetail, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return git.CommitDetail{}, err
	}
	return runner.ShowCommit(ctx, root, sha)
}

// GitCommitBase 调用会话所在仓库的 Git 操作。
func (c *Client) GitCommitBase(ctx context.Context, threadID, sha string) (string, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return "", err
	}
	return runner.CommitBase(ctx, root, sha)
}

// GitBlame 调用会话所在仓库的 Git 操作。
func (c *Client) GitBlame(ctx context.Context, threadID, path, rev string) ([]git.BlameLine, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return runner.Blame(ctx, root, path, rev)
}
