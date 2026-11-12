package app

import "context"

// GitWriteResolved 将冲突解决后的正文写回工作区。
func (c *Client) GitWriteResolved(ctx context.Context, threadID, path string, content []byte) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.WriteWorktreeFile(ctx, root, path, content)
}
