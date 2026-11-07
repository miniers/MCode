package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// deliverySelectCols 是 thread_deliveries 行的读取列清单，新增列只改这里。
const deliverySelectCols = `
	message_id, thread_id, follow_up_id, turn_id, state, attempt, failure, created_at, settled_at
`

// InsertDelivery 写入一条投递状态行，初始状态通常是 dispatching。
// 主键冲突时返回错误而不覆盖：message_id 由出队生成的用户消息唯一确定，覆盖会把已终态的行
// 重置为 dispatching，启动对账就会把已经交付的消息误报成“可能没有送达”。
func (s *Store) InsertDelivery(ctx context.Context, d Delivery) error {
	if d.MessageID == "" {
		return fmt.Errorf("insert delivery: message_id is empty")
	}
	state := d.State
	if state == "" {
		state = DeliveryDispatching
	}
	attempt := d.Attempt
	if attempt <= 0 {
		attempt = 1
	}
	createdAt := d.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	var settledAt any
	if d.SettledAt != nil {
		settledAt = formatTime(*d.SettledAt)
	}
	if _, err := s.db.ExecContext(ctx, `
	INSERT INTO thread_deliveries (message_id, thread_id, follow_up_id, turn_id, state, attempt, failure, created_at, settled_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, d.MessageID, d.ThreadID, d.FollowUpID, d.TurnID, state, attempt, d.Failure, formatTime(createdAt), settledAt); err != nil {
		return fmt.Errorf("insert delivery %s: %w", d.MessageID, err)
	}
	return nil
}

// MarkDeliveryAccepted 把仍为 dispatching 的投递行迁到 accepted，并补记 provider 回执号。
// 只有 dispatching 能迁移：已是终态时返回 nil，重复的 accepted 信号（显式事件与首个上游事件
// 两条路径）因此幂等。turnID 为空表示该 provider 没有回执，保留原值。
func (s *Store) MarkDeliveryAccepted(ctx context.Context, messageID, turnID string) error {
	if _, err := s.db.ExecContext(ctx, `
	UPDATE thread_deliveries
	SET state = ?, turn_id = CASE WHEN ? <> '' THEN ? ELSE turn_id END, settled_at = ?
	WHERE message_id = ? AND state = ?
	`, DeliveryAccepted, turnID, turnID, formatTime(time.Now().UTC()), messageID, DeliveryDispatching); err != nil {
		return fmt.Errorf("mark delivery %s accepted: %w", messageID, err)
	}
	return nil
}

// MarkDeliveryFailed 把仍为 dispatching 的投递行迁到 failed，并记录失败原因。
// 与 accepted 一样只从 dispatching 迁移，行不存在或已终态时返回 nil（幂等）。
func (s *Store) MarkDeliveryFailed(ctx context.Context, messageID, failure string) error {
	if _, err := s.db.ExecContext(ctx, `
	UPDATE thread_deliveries SET state = ?, failure = ?, settled_at = ?
	WHERE message_id = ? AND state = ?
	`, DeliveryFailed, failure, formatTime(time.Now().UTC()), messageID, DeliveryDispatching); err != nil {
		return fmt.Errorf("mark delivery %s failed: %w", messageID, err)
	}
	return nil
}

// SettleInterruptedDeliveries 在同一事务内把全部 dispatching 行一次性转为 unknown 并返回这些行
// （返回值的 State/SettledAt 已写成新值）。第二次调用没有 dispatching 行，返回空切片；
// unknown 是终态、永不重放，因此启动对账不会重复提示，也不会累积。
func (s *Store) SettleInterruptedDeliveries(ctx context.Context) ([]Delivery, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin settle interrupted deliveries: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		"SELECT "+deliverySelectCols+" FROM thread_deliveries WHERE state = ? ORDER BY created_at ASC, rowid ASC",
		DeliveryDispatching)
	if err != nil {
		return nil, fmt.Errorf("list dispatching deliveries: %w", err)
	}
	items := make([]Delivery, 0)
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate dispatching deliveries: %w", err)
	}
	rows.Close()

	if len(items) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit settle interrupted deliveries: %w", err)
		}
		return items, nil
	}

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
	UPDATE thread_deliveries SET state = ?, settled_at = ?
	WHERE state = ?
	`, DeliveryUnknown, formatTime(now), DeliveryDispatching); err != nil {
		return nil, fmt.Errorf("settle interrupted deliveries: %w", err)
	}
	for i := range items {
		items[i].State = DeliveryUnknown
		settledAt := now
		items[i].SettledAt = &settledAt
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit settle interrupted deliveries: %w", err)
	}
	return items, nil
}

// scanDelivery 读取一行 thread_deliveries，时间列按本包的定宽时间格式解析。
func scanDelivery(s rowScanner) (Delivery, error) {
	var d Delivery
	var createdAtStr string
	var settledAtStr sql.NullString
	if err := s.Scan(
		&d.MessageID, &d.ThreadID, &d.FollowUpID, &d.TurnID,
		&d.State, &d.Attempt, &d.Failure, &createdAtStr, &settledAtStr,
	); err != nil {
		return Delivery{}, fmt.Errorf("scan delivery: %w", err)
	}
	createdAt, err := parseTime(createdAtStr)
	if err != nil {
		return Delivery{}, fmt.Errorf("parse delivery %s created_at: %w", d.MessageID, err)
	}
	d.CreatedAt = createdAt
	if settledAtStr.Valid && settledAtStr.String != "" {
		settledAt, err := parseTime(settledAtStr.String)
		if err != nil {
			return Delivery{}, fmt.Errorf("parse delivery %s settled_at: %w", d.MessageID, err)
		}
		d.SettledAt = &settledAt
	}
	return d, nil
}
