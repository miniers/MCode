package store

import "context"

// RecallMessage 原子移除未回复的提示词并清除供应商会话，避免下次恢复已撤回的上下文。
func (s *Store) RecallMessage(ctx context.Context, threadID, messageID, continuation string, turnIndex int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 冷归档会话先整体回填，否则撤回会以为消息不存在。
	if err := s.hydrateThreadTx(ctx, tx, threadID); err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE id = ? AND thread_id = ? AND role = 'user'
		AND seq = (SELECT MAX(seq) FROM messages WHERE thread_id = ?)`, messageID, threadID, threadID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM turn_usage WHERE thread_id = ? AND turn_index = ?", threadID, turnIndex); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE threads SET session_id = '', continuation = ? WHERE id = ?", continuation, threadID); err != nil {
		return err
	}
	return tx.Commit()
}
