package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// TaskByThreadID 查询执行线程对应的看板任务，线程没有任务时返回 ErrNotFound。
// 复用 GetTask 的查询列与扫描逻辑，保证两个入口返回一致的任务结构。
func (s *Store) TaskByThreadID(ctx context.Context, threadID string) (Task, error) {
	if threadID == "" {
		return Task{}, ErrNotFound
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM tasks WHERE thread_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`, threadID).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, ErrNotFound
		}
		return Task{}, fmt.Errorf("find task by thread %s: %w", threadID, err)
	}
	return s.GetTask(ctx, id)
}
