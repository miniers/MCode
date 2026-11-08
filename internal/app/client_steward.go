package app

import (
	"context"
	"errors"
	"strings"

	mcodeui "mcode/internal/ui"
)

var _ mcodeui.StewardClient = (*Client)(nil)

// StewardOpenDecisionCount 返回当前未答总管决策卡数量。
// 客户端尚未挂载存储时按空列表处理，避免侧栏因为启动顺序报错。
func (c *Client) StewardOpenDecisionCount(ctx context.Context) (int, error) {
	if c == nil || c.backend == nil || c.backend.Store() == nil {
		return 0, nil
	}
	decisions, err := c.backend.Store().ListStewardDecisions(ctx, "open", 200)
	if err != nil {
		return 0, err
	}
	return len(decisions), nil
}

// StewardThreadID 返回已保存的前台会话 ID。
func (c *Client) StewardThreadID() string {
	if c.backend == nil || c.backend.Store() == nil {
		return ""
	}
	id, _, err := c.backend.Store().GetSetting(context.Background(), "steward.feed_thread_id")
	if err != nil {
		return ""
	}
	return id
}

func (c *Client) StewardEnabled() bool {
	cfg, ok := c.loadConfig()
	return ok && cfg.Orchestration.Steward.Enabled
}

func (c *Client) StewardIdentity() (name, avatar string) {
	cfg, ok := c.loadConfig()
	if !ok {
		return "", ""
	}
	return strings.TrimSpace(cfg.Orchestration.Steward.Name), strings.TrimSpace(cfg.Orchestration.Steward.Avatar)
}

func (c *Client) OpenStewardThread(ctx context.Context) (string, error) {
	cfg, ok := c.loadConfig()
	if !ok || !cfg.Orchestration.Steward.Enabled {
		return "", errors.New("总管未启用，请先在设置 → 总管中启用")
	}
	projectID, err := ensureFrontdeskProject(ctx, c.backend, cfg.Orchestration.Steward.ResolvedHomeDir(c.effectiveDataDir()))
	if err != nil {
		return "", err
	}
	return c.backend.EnsureStewardFeedThread(ctx, projectID)
}
