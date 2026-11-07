package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// pendingAskSelectCols 是 pending_asks 行的读取列清单，新增列只改这里。
const pendingAskSelectCols = `
	request_id, thread_id, turn_id, kind, title, state, resolution, created_at, settled_at
`

// InsertPendingAsk 写入一条待结算的提问/审批留痕行。
// 主键冲突时直接返回错误而不覆盖：request_id 由卡片创建方唯一生成，覆盖会把已结算的行
// 重置为 pending，下一次启动对账就会把已经处理过的卡片误报成“重启失效”。
func (s *Store) InsertPendingAsk(ctx context.Context, ask PendingAsk) error {
	if ask.RequestID == "" {
		return fmt.Errorf("insert pending ask: request_id is empty")
	}
	state := ask.State
	if state == "" {
		state = PendingAskPending
	}
	createdAt := ask.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	var settledAt any
	if ask.SettledAt != nil {
		settledAt = formatTime(*ask.SettledAt)
	}
	if _, err := s.db.ExecContext(ctx, `
	INSERT INTO pending_asks (request_id, thread_id, turn_id, kind, title, state, resolution, created_at, settled_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, ask.RequestID, ask.ThreadID, ask.TurnID, ask.Kind, ask.Title, state, ask.Resolution, formatTime(createdAt), settledAt); err != nil {
		return fmt.Errorf("insert pending ask %s: %w", ask.RequestID, err)
	}
	return nil
}

// SettlePendingAsk 把仍为 pending 的行结算为 state、写 resolution 与 settled_at。
// 只更新 pending 行（先到者决定终态）：行不存在或已终态时返回 nil，重复调用幂等。
func (s *Store) SettlePendingAsk(ctx context.Context, requestID, state, resolution string) error {
	_, err := s.SettlePendingAskChanged(ctx, requestID, state, resolution)
	return err
}

// SettlePendingAskChanged 返回是否将一行从 pending 转为终态。
func (s *Store) SettlePendingAskChanged(ctx context.Context, requestID, state, resolution string) (bool, error) {
	switch state {
	case PendingAskSettled, PendingAskWithdrawn, PendingAskExpired:
	default:
		return false, fmt.Errorf("settle pending ask %s: invalid terminal state %q", requestID, state)
	}
	result, err := s.db.ExecContext(ctx, `
	UPDATE pending_asks SET state = ?, resolution = ?, settled_at = ?
	WHERE request_id = ? AND state = ?
	`, state, resolution, formatTime(time.Now().UTC()), requestID, PendingAskPending)
	if err != nil {
		return false, fmt.Errorf("settle pending ask %s: %w", requestID, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("settle pending ask %s rows affected: %w", requestID, err)
	}
	return count > 0, nil
}

// ExpirePendingAsks 在同一事务内把全部 pending 行一次性转为 expired，并返回被转换的行
// （返回值的 State/Resolution/SettledAt 已写成新值）。第二次调用没有 pending 行，返回空切片；
// 已终态的行不会被再次返回，因此启动对账不会重复提示。
func (s *Store) ExpirePendingAsks(ctx context.Context, resolution string) ([]PendingAsk, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin expire pending asks: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		"SELECT "+pendingAskSelectCols+" FROM pending_asks WHERE state = ? ORDER BY created_at ASC, rowid ASC",
		PendingAskPending)
	if err != nil {
		return nil, fmt.Errorf("list pending asks: %w", err)
	}
	asks := make([]PendingAsk, 0)
	for rows.Next() {
		ask, err := scanPendingAsk(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		asks = append(asks, ask)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate pending asks: %w", err)
	}
	rows.Close()

	if len(asks) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit expire pending asks: %w", err)
		}
		return asks, nil
	}

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
	UPDATE pending_asks SET state = ?, resolution = ?, settled_at = ?
	WHERE state = ?
	`, PendingAskExpired, resolution, formatTime(now), PendingAskPending); err != nil {
		return nil, fmt.Errorf("expire pending asks: %w", err)
	}
	for i := range asks {
		asks[i].State = PendingAskExpired
		asks[i].Resolution = resolution
		settledAt := now
		asks[i].SettledAt = &settledAt
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit expire pending asks: %w", err)
	}
	return asks, nil
}

// scanPendingAsk 读取一行 pending_asks，时间列按本包的定宽时间格式解析。
func scanPendingAsk(s rowScanner) (PendingAsk, error) {
	var ask PendingAsk
	var createdAtStr string
	var settledAtStr sql.NullString
	if err := s.Scan(
		&ask.RequestID, &ask.ThreadID, &ask.TurnID, &ask.Kind, &ask.Title,
		&ask.State, &ask.Resolution, &createdAtStr, &settledAtStr,
	); err != nil {
		return PendingAsk{}, fmt.Errorf("scan pending ask: %w", err)
	}
	createdAt, err := parseTime(createdAtStr)
	if err != nil {
		return PendingAsk{}, fmt.Errorf("parse pending ask %s created_at: %w", ask.RequestID, err)
	}
	ask.CreatedAt = createdAt
	if settledAtStr.Valid && settledAtStr.String != "" {
		settledAt, err := parseTime(settledAtStr.String)
		if err != nil {
			return PendingAsk{}, fmt.Errorf("parse pending ask %s settled_at: %w", ask.RequestID, err)
		}
		ask.SettledAt = &settledAt
	}
	return ask, nil
}
