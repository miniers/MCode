package app

import (
	"context"
	"errors"
	"path/filepath"

	"mcode/internal/git"
)

// GitStatusEntries 调用会话所在仓库的 Git 操作。
func (c *Client) GitStatusEntries(ctx context.Context, threadID string) ([]git.StatusEntry, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return runner.Status(ctx, root)
}

// GitStageFiles 调用会话所在仓库的 Git 操作。
func (c *Client) GitStageFiles(ctx context.Context, threadID string, paths []string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.StageFiles(ctx, root, paths)
}

// GitUnstageFiles 调用会话所在仓库的 Git 操作。
func (c *Client) GitUnstageFiles(ctx context.Context, threadID string, paths []string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.UnstageFiles(ctx, root, paths)
}

// GitDiscardFiles 调用会话所在仓库的 Git 操作。
func (c *Client) GitDiscardFiles(ctx context.Context, threadID string, paths []string) (*git.Snapshot, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return runner.DiscardFiles(ctx, root, paths)
}

// GitStageLines 调用会话所在仓库的 Git 操作。
func (c *Client) GitStageLines(ctx context.Context, threadID, path string, sels []git.LineSel) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.StageLines(ctx, root, path, sels)
}

// GitUnstageLines 调用会话所在仓库的 Git 操作。
func (c *Client) GitUnstageLines(ctx context.Context, threadID, path string, sels []git.LineSel) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.UnstageLines(ctx, root, path, sels)
}

// GitDiscardLines 调用会话所在仓库的 Git 操作。
func (c *Client) GitDiscardLines(ctx context.Context, threadID, path string, sels []git.LineSel) (*git.Snapshot, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return runner.DiscardLines(ctx, root, path, sels)
}

// GitCommitIndex 调用会话所在仓库的 Git 操作。
func (c *Client) GitCommitIndex(ctx context.Context, threadID, message string, amend bool) (string, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return "", err
	}
	return runner.CommitIndex(ctx, root, message, amend)
}

// GitLastCommitMessage 调用会话所在仓库的 Git 操作。
func (c *Client) GitLastCommitMessage(ctx context.Context, threadID string) (string, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return "", err
	}
	return runner.LastCommitMessage(ctx, root)
}

// GitHeadPushed 调用会话所在仓库的 Git 操作。
func (c *Client) GitHeadPushed(ctx context.Context, threadID string) (bool, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return false, err
	}
	return runner.HeadPushed(ctx, root)
}

// GitIndexDiff 调用会话所在仓库的 Git 操作。
func (c *Client) GitIndexDiff(ctx context.Context, threadID, path string, staged bool) ([]git.FileDiff, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return runner.IndexDiff(ctx, root, path, staged)
}

// GitRestoreSnapshot 恢复当前会话仓库的快照。
func (c *Client) GitRestoreSnapshot(ctx context.Context, threadID string, snap *git.Snapshot) error {
	if snap == nil {
		return errors.New("快照不能为空")
	}
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	if filepath.Clean(snap.Root) != filepath.Clean(root) {
		return errors.New("快照不属于当前仓库")
	}
	return runner.RestoreSnapshot(ctx, snap)
}
