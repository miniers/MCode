package remote

import "context"

// RecallDraft 为桌面输入区复用运行态撤回流程，但拒绝删除已有输出的轮次。
func (b *MemoryBackend) RecallDraft(ctx context.Context, threadID string) (string, error) {
	result, err := b.recallTurn(ctx, threadID, true)
	return result.Prompt, err
}
