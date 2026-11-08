package remote

import (
	"context"

	"mcode/internal/kanban"
)

var _ kanban.InteractionModeSetter = (*MemoryBackend)(nil)

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
