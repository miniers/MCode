package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	mcodeui "mcode/internal/ui"
)

var _ mcodeui.StewardClient = (*Client)(nil)

// AnswerStewardDecision 回答总管决策卡。
func (c *Client) AnswerStewardDecision(ctx context.Context, id, answer string) error {
	if c == nil || c.backend == nil {
		return errors.New("前台不可用")
	}
	return c.backend.AnswerStewardDecision(ctx, id, answer, "owner")
}

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

// StewardOpenDecisions 返回当前未答总管决策卡列表。
// OptionsJSON 支持字符串数组（如 ["甲","乙"]），解析失败时容错为空切片。
func (c *Client) StewardOpenDecisions(ctx context.Context) ([]mcodeui.StewardDecisionItem, error) {
	if c == nil || c.backend == nil || c.backend.Store() == nil {
		return nil, nil
	}
	decisions, err := c.backend.Store().ListStewardDecisions(ctx, "open", 200)
	if err != nil {
		return nil, err
	}
	items := make([]mcodeui.StewardDecisionItem, 0, len(decisions))
	for _, d := range decisions {
		items = append(items, mcodeui.StewardDecisionItem{
			ID:          d.ID,
			TaskID:      d.TaskID,
			ThreadID:    d.ThreadID,
			Question:    d.Question,
			Context:     d.Context,
			Options:     parseStewardDecisionOptions(d.OptionsJSON),
			Recommended: d.Recommended,
			Status:      d.Status,
			CreatedAt:   d.CreatedAt,
		})
	}
	return items, nil
}

func parseStewardDecisionOptions(optionsJSON string) []string {
	trimmed := strings.TrimSpace(optionsJSON)
	if trimmed == "" {
		return []string{}
	}
	var strs []string
	if err := json.Unmarshal([]byte(trimmed), &strs); err == nil {
		if strs == nil {
			return []string{}
		}
		return strs
	}
	var objs []struct {
		Key  string `json:"key"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(trimmed), &objs); err == nil && len(objs) > 0 {
		out := make([]string, 0, len(objs))
		for _, o := range objs {
			if o.Text != "" {
				out = append(out, o.Text)
			} else if o.Key != "" {
				out = append(out, o.Key)
			}
		}
		return out
	}
	return []string{}
}

// StewardThreadID 返回已保存的前台会话 ID。
func (c *Client) StewardThreadID() string {
	if c.backend == nil || c.backend.Store() == nil {
		return ""
	}
	return c.backend.StewardFeedThreadID()
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
	projectID, err := ensureBuiltinProject(ctx, c.backend, cfg.Orchestration.Steward.ResolvedHomeDir(c.effectiveDataDir()), "总管")
	if err != nil {
		return "", err
	}
	return c.backend.EnsureStewardFeedThread(ctx, projectID)
}

// ResetStewardThread 归档当前总管会话并创建新的主会话。
func (c *Client) ResetStewardThread(ctx context.Context) (string, error) {
	cfg, ok := c.loadConfig()
	if !ok || !cfg.Orchestration.Steward.Enabled {
		return "", errors.New("总管未启用，请先在设置 → 总管中启用")
	}
	projectID, err := ensureBuiltinProject(ctx, c.backend, cfg.Orchestration.Steward.ResolvedHomeDir(c.effectiveDataDir()), "总管")
	if err != nil {
		return "", err
	}
	return c.backend.ResetStewardFeedThread(ctx, projectID)
}
