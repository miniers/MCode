package app

import (
	"context"
	"errors"
	"fmt"

	"mcode/internal/store"
)

// SwarmReportSummary 从存档读取某个 bee 的完整汇报总结，供界面展开被截断的汇报卡片时显示全文。
// 只允许读调用方所在主会话自己的 bee：主会话 ID 为空，或与存档里记录的 lead 不一致时都按读不到处理。
func (c *Client) SwarmReportSummary(ctx context.Context, leadThreadID, beeID string) (string, error) {
	if c == nil || c.backend == nil {
		return "", errors.New("编排汇报存档不可用")
	}
	st := c.backend.Store()
	if st == nil {
		return "", errors.New("编排汇报存档不可用")
	}
	if leadThreadID == "" {
		return "", errors.New("缺少主会话 ID，无法读取汇报存档")
	}
	rec, err := st.GetSwarmReport(ctx, beeID)
	if errors.Is(err, store.ErrNotFound) {
		return "", fmt.Errorf("没有找到子代理 %s 的汇报存档", beeID)
	}
	if err != nil {
		return "", err
	}
	if rec.LeadThreadID != leadThreadID {
		return "", fmt.Errorf("子代理 %s 不属于当前主会话", beeID)
	}
	return rec.Summary, nil
}
