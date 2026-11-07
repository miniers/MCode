package remote

import (
	"context"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// ArchiveBeeSession 归档一个已成功完成的 Swarm Codex bee 会话。
// 只在下列条件全部满足时才归档：
//   - 会话存在且 provider 为 codex（非 codex bee 不自动归档）；
//   - 会话不在运行中（判断方式与 SessionState 一致）；
//   - 会话中用户消息不超过一条（首条是派发指令，多于一条说明用户有追问，保持可见）。
//
// 已归档时直接返回 true。归档只隐藏会话，不结束 provider 进程，也不触发任何 git 操作；
// 归档会话保留 ID，但当前发送消息路径不会自动取消归档，需用户手动取消归档。
func (b *MemoryBackend) ArchiveBeeSession(ctx context.Context, threadID string) (bool, error) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return false, store.ErrNotFound
	}
	// C-ST-07：用户消息计数依赖内存消息，先确保历史已回载。
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		b.mu.Unlock()
		return false, err
	}
	b.touchThreadLocked(td)
	provider := td.summary.Provider
	running := td.runTurnID != "" || td.runCancel != nil
	if !running && td.summary.IsRunning {
		td.summary.IsRunning = false
	}
	archived := td.summary.IsArchived
	userMessages := 0
	for _, m := range td.messages {
		if m.Role == "user" {
			userMessages++
		}
	}
	b.mu.Unlock()

	if provider != "codex" {
		return false, nil
	}
	if running {
		return false, nil
	}
	if userMessages > 1 {
		return false, nil
	}
	if archived {
		return true, nil
	}

	// 复用单线程归档路径：UpdateThread 会写穿归档状态到 store，并由 hub.Publish 广播变更事件。
	archivedTrue := true
	if _, err := b.UpdateThread(ctx, wire.ThreadUpdateParams{ThreadID: threadID, Archived: &archivedTrue}); err != nil {
		return false, err
	}
	return true, nil
}
