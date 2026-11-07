package app

import (
	"context"
	"errors"
)

// ResumeInterruptedSwarm 续跑指定主会话下因 MCode 重启被打断的子代理，
// 并把重启前排队、尚未启动的条目重新入队；返回成功续跑的条目数。
func (c *Client) ResumeInterruptedSwarm(ctx context.Context, leadThreadID string) (int, error) {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return 0, errors.New("编排服务不可用")
	}
	n, err := m.ResumeInterrupted(ctx, leadThreadID)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// DismissInterruptedSwarm 放弃续跑指定主会话下因重启被打断的子代理与排队条目；
// 只清除中断标记，不删除 worktree 等任何文件。返回清除掉的条目数。
func (c *Client) DismissInterruptedSwarm(ctx context.Context, leadThreadID string) (int, error) {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return 0, errors.New("编排服务不可用")
	}
	return m.DismissInterrupted(leadThreadID), nil
}

// RetrySwarmBee 让用户手动沿用某个已失败或已停止子代理的原 worktree 与半成品改动重新派发；
// 只改该 bee 的记录，不影响其它子代理。
func (c *Client) RetrySwarmBee(ctx context.Context, leadThreadID, beeThreadID string) error {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return errors.New("编排服务不可用")
	}
	_, err := m.RetryBee(ctx, leadThreadID, beeThreadID)
	return err
}
