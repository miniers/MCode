package app

import (
	"context"

	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// migrateFrontdeskArchive 只归档旧总管会话，保留原设置。
func migrateFrontdeskArchive(ctx context.Context, db *store.Store, backend *remote.MemoryBackend) error {
	oldID, _, err := db.GetSetting(ctx, "steward.thread_id")
	if err != nil || oldID == "" {
		return err
	}
	archived, _, err := db.GetSetting(ctx, "steward.archived_thread_id")
	if err != nil || archived != "" {
		return err
	}
	title := "总管 · 归档"
	if _, err := backend.UpdateThread(ctx, wire.ThreadUpdateParams{ThreadID: oldID, Title: &title}); err != nil {
		return err
	}
	return db.SetSetting(ctx, "steward.archived_thread_id", oldID)
}
