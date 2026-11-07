package remote

import (
	"context"

	"mcode/internal/harness"
	"mcode/internal/kanban"
	"mcode/internal/remote/wire"
)

var _ kanban.InteractionModeSetter = (*MemoryBackend)(nil)
var _ kanban.ReadOnlySessionPromoter = (*MemoryBackend)(nil)

// SetInteractionMode 只更新指定会话的交互模式，其余 composer 偏好原样保留（D-KB-018）。
func (b *MemoryBackend) SetInteractionMode(ctx context.Context, threadID, mode string) error {
	thread, err := b.GetThread(ctx, threadID)
	if err != nil {
		return err
	}
	_, err = b.SetThreadComposerPrefs(ctx, threadID, ThreadComposerPrefs{
		InteractionMode: mode,
		Effort:          derefString(thread.Effort),
		BeesProvider:    derefString(thread.SwarmBeesProvider),
		BeesModel:       derefString(thread.SwarmBeesModel),
		BeesEffort:      derefString(thread.SwarmBeesEffort),
		Persona:         thread.Persona,
	})
	return err
}

// PromoteReadOnlySession 把仍处只读档的会话升到 auto；其他档位是用户选过的，保持不变。
// 重复调用是安全的：已经不是只读档时直接返回。
func (b *MemoryBackend) PromoteReadOnlySession(ctx context.Context, threadID string) error {
	thread, err := b.GetThread(ctx, threadID)
	if err != nil {
		return err
	}
	if thread.RuntimeMode != string(harness.ModeReadOnly) {
		return nil
	}
	mode := string(harness.ModeAuto)
	_, err = b.UpdateThread(ctx, wire.ThreadUpdateParams{ThreadID: threadID, RuntimeMode: &mode})
	return err
}
