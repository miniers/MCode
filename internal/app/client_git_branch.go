package app

import (
	"context"

	"mcode/internal/git"
)

func (c *Client) GitBranches(ctx context.Context, threadID string) ([]git.BranchInfo, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return runner.Branches(ctx, root)
}

func (c *Client) GitRecentBranches(ctx context.Context, threadID string, limit int) ([]string, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return runner.RecentBranches(ctx, root, limit)
}

func (c *Client) GitBranchRename(ctx context.Context, threadID string, oldName, newName string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.BranchRename(ctx, root, oldName, newName)
}

func (c *Client) GitBranchDelete(ctx context.Context, threadID string, name string, force bool) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.BranchDelete(ctx, root, name, force)
}

func (c *Client) GitSetUpstream(ctx context.Context, threadID string, branch, upstream string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.SetUpstream(ctx, root, branch, upstream)
}

func (c *Client) GitUnsetUpstream(ctx context.Context, threadID string, branch string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.UnsetUpstream(ctx, root, branch)
}

func (c *Client) GitCreateBranchAt(ctx context.Context, threadID string, name, startPoint string, checkout bool) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.CreateBranchAt(ctx, root, name, startPoint, checkout)
}

func (c *Client) GitCheckoutRemoteBranch(ctx context.Context, threadID string, remoteRef string) (string, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return "", err
	}
	return runner.CheckoutRemoteBranch(ctx, root, remoteRef)
}

func (c *Client) GitDeleteRemoteBranch(ctx context.Context, threadID string, remote, branch string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.DeleteRemoteBranch(ctx, root, remote, branch)
}

func (c *Client) GitTags(ctx context.Context, threadID string) ([]git.TagInfo, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return runner.Tags(ctx, root)
}

func (c *Client) GitTagCreate(ctx context.Context, threadID string, name, rev, message string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.TagCreate(ctx, root, name, rev, message)
}

func (c *Client) GitTagDelete(ctx context.Context, threadID string, name string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.TagDelete(ctx, root, name)
}

func (c *Client) GitPushTag(ctx context.Context, threadID string, remote, name string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.PushTag(ctx, root, remote, name)
}

func (c *Client) GitDeleteRemoteTag(ctx context.Context, threadID string, remote, name string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.DeleteRemoteTag(ctx, root, remote, name)
}

func (c *Client) GitRemotes(ctx context.Context, threadID string) ([]git.RemoteInfo, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return runner.Remotes(ctx, root)
}

func (c *Client) GitRemoteAdd(ctx context.Context, threadID string, name, url string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.RemoteAdd(ctx, root, name, url)
}

func (c *Client) GitRemoteRemove(ctx context.Context, threadID string, name string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.RemoteRemove(ctx, root, name)
}

func (c *Client) GitRemoteRename(ctx context.Context, threadID string, oldName, newName string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.RemoteRename(ctx, root, oldName, newName)
}

func (c *Client) GitRemoteSetURL(ctx context.Context, threadID string, name, url string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.RemoteSetURL(ctx, root, name, url)
}
