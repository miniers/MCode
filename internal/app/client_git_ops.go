package app

import (
	"context"
	"fmt"
	"mcode/internal/git"
)

func (c *Client) GitPullWith(ctx context.Context, threadID string, strategy string) error {
	switch strategy {
	case "", "ff-only", "rebase", "merge":
	default:
		return fmt.Errorf("无效的拉取策略：%s", strategy)
	}
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.Pull(ctx, root, strategy)
}

func (c *Client) GitPushForceWithLease(ctx context.Context, threadID string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.PushForceWithLease(ctx, root)
}

func (c *Client) GitOpState(ctx context.Context, threadID string) (git.OpState, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return git.OpState{}, err
	}
	return runner.OpState(ctx, root)
}

func (c *Client) GitRebase(ctx context.Context, threadID string, onto string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.Rebase(ctx, root, onto)
}

func (c *Client) GitCherryPick(ctx context.Context, threadID string, sha string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.CherryPick(ctx, root, sha)
}

func (c *Client) GitRevertCommit(ctx context.Context, threadID string, sha string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.RevertCommit(ctx, root, sha)
}

func (c *Client) GitReset(ctx context.Context, threadID string, rev, mode string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.Reset(ctx, root, rev, mode)
}

func (c *Client) GitContinueOp(ctx context.Context, threadID string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.ContinueOp(ctx, root)
}

func (c *Client) GitSkipOp(ctx context.Context, threadID string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.SkipOp(ctx, root)
}

func (c *Client) GitAbortOp(ctx context.Context, threadID string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.AbortOp(ctx, root)
}

func (c *Client) GitConflictFiles(ctx context.Context, threadID string) ([]git.ConflictFile, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return runner.ConflictFiles(ctx, root)
}

func (c *Client) GitConflictSides(ctx context.Context, threadID string, path string) (ours, base, theirs []byte, err error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return nil, nil, nil, err
	}
	return runner.ConflictSides(ctx, root, path)
}

func (c *Client) GitResolveFileSide(ctx context.Context, threadID string, path string, ours bool) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.ResolveFileSide(ctx, root, path, ours)
}

func (c *Client) GitMarkResolved(ctx context.Context, threadID string, paths []string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.MarkResolved(ctx, root, paths)
}
