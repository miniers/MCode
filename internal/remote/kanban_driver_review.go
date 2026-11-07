package remote

import (
	"context"

	"mcode/internal/swarm"
)

var _ swarm.LeadRuntimeSource = (*MemoryBackend)(nil)

// LeadRuntime 返回主会话当前使用的 provider、model 与推理强度，供合并审查会话
// 按主会话模型起会话。读不到线程或 provider 为空时返回 ok=false。
func (b *MemoryBackend) LeadRuntime(ctx context.Context, leadThreadID string) (provider, model, effort string, ok bool) {
	lead, err := b.GetThread(ctx, leadThreadID)
	if err != nil {
		return "", "", "", false
	}
	if lead.Provider == "" {
		return "", "", "", false
	}
	return lead.Provider, derefString(lead.Model), derefString(lead.Effort), true
}
