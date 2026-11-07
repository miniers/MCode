package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// FollowUpEvent 是排队消息的生命周期事件；append-only，不随队列全量替换删除。
type FollowUpEvent struct {
	ID         string    `json:"id"`
	EventKey   string    `json:"event_key"`
	FollowUpID string    `json:"follow_up_id"`
	ThreadID   string    `json:"thread_id"`
	Kind       string    `json:"kind"`
	Reason     string    `json:"reason"`
	SourceID   string    `json:"source_id"`
	Text       string    `json:"text"`
	CreatedAt  time.Time `json:"created_at"`
}

const (
	FollowUpEventQueued    = "queued"
	FollowUpEventDelivered = "delivered"
	FollowUpEventSteered   = "steered"
	FollowUpEventDropped   = "dropped"
	FollowUpEventEdited    = "edited"
	// 投递状态事件：dispatching 在出队时写入，accepted/failed/unknown 是终态。
	// delivered 的语义收窄为“已送达（accepted）”，旧行保持原意；新流程改用下面这几个事件。
	FollowUpEventDispatching = "dispatching"
	FollowUpEventAccepted    = "accepted"
	FollowUpEventUnknown     = "unknown"
	FollowUpEventFailed      = "failed"
)

const (
	FollowUpDropUserRemove  = "user_remove"
	FollowUpDropEmptyEdit   = "empty_edit"
	FollowUpDropSendAborted = "send_aborted"
	FollowUpDropVanished    = "vanished"
)

// followUpEventKey 生成确定性幂等键：同一逻辑事件重复提交得到同一键，由 UNIQUE 约束吸收。
func followUpEventKey(kind, reason, threadID, followUpID, sourceID string) string {
	return kind + ":" + reason + ":" + threadID + ":" + followUpID + ":" + sourceID
}

// followUpContentSignature 是队列项内容（文本 + 附件 ID + 附件元数据）的 SHA-256 前 16 位十六进制。
// 只用于检测同一 ID 的内容是否变化，不参与状态机。
func followUpContentSignature(f FollowUp) (string, error) {
	attIDs := f.Attachments
	if attIDs == nil {
		attIDs = []string{}
	}
	meta := f.AttachmentMeta
	if meta == nil {
		meta = []FollowUpAttachment{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("marshal attachment_meta for signature of follow up %s: %w", f.ID, err)
	}
	sum := sha256.Sum256([]byte(f.Text + "\x00" + strings.Join(attIDs, "\x00") + "\x00" + string(metaJSON)))
	return hex.EncodeToString(sum[:8]), nil
}

// textSignature 供显式 edited 事件在调用方只给文本时补 source_id。
func textSignature(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

// ReplaceFollowUpsWithAudit 在同一事务里替换队列快照并写入生命周期事件。
// 显式事件的 EventKey 由本方法按 3.1 的规则重算，调用方传入值会被覆盖。
func (s *Store) ReplaceFollowUpsWithAudit(ctx context.Context, threadID string, revision int64, items []FollowUp, explicit []FollowUpEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// 先取旧集合，差值算法要靠它判断哪些项消失或内容变化。
	oldItems, err := listFollowUpsTx(ctx, tx, threadID)
	if err != nil {
		return err
	}

	// 验证线程存在并更新 follow_up_revision
	res, err := tx.ExecContext(ctx, "UPDATE threads SET follow_up_revision = ? WHERE id = ?", revision, threadID)
	if err != nil {
		return fmt.Errorf("update thread follow_up_revision %s: %w", threadID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}

	// 删除该会话原有的全部 follow-up
	if _, err := tx.ExecContext(ctx, "DELETE FROM thread_follow_ups WHERE thread_id = ?", threadID); err != nil {
		return fmt.Errorf("delete old follow ups for thread %s: %w", threadID, err)
	}

	// 逐条插入新 follow-up，按切片下标写入 position
	insertQuery := `
	INSERT INTO thread_follow_ups (id, thread_id, position, text, kind, intent, attachments, created_at, updated_at, attachment_meta)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	for idx, item := range items {
		now := time.Now().UTC()
		createdAt := item.CreatedAt
		if createdAt.IsZero() {
			createdAt = now
		}
		updatedAt := item.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = createdAt
		}

		attachmentsJSON := "[]"
		if item.Attachments != nil {
			data, err := json.Marshal(item.Attachments)
			if err != nil {
				return fmt.Errorf("marshal attachments for follow up %s: %w", item.ID, err)
			}
			attachmentsJSON = string(data)
		}

		attachmentMetaJSON, err := encodeJSONList(item.AttachmentMeta)
		if err != nil {
			return fmt.Errorf("marshal attachment_meta for follow up %s: %w", item.ID, err)
		}

		if _, err := tx.ExecContext(ctx, insertQuery,
			item.ID,
			threadID,
			idx,
			item.Text,
			item.Kind,
			item.Intent,
			attachmentsJSON,
			formatTime(createdAt),
			formatTime(updatedAt),
			attachmentMetaJSON,
		); err != nil {
			return fmt.Errorf("insert follow up %s: %w", item.ID, err)
		}
	}

	oldByID := make(map[string]FollowUp, len(oldItems))
	for _, f := range oldItems {
		oldByID[f.ID] = f
	}
	newByID := make(map[string]FollowUp, len(items))
	for idx, f := range items {
		f.Position = idx
		newByID[f.ID] = f
	}

	// covered 记录本次已由显式事件覆盖的 follow-up ID，避免同一事务内再补差值事件。
	covered := make(map[string]bool, len(explicit))
	now := time.Now().UTC()
	for _, ev := range explicit {
		if err := writeExplicitFollowUpEvent(ctx, tx, threadID, now, ev, oldByID, newByID); err != nil {
			return err
		}
		covered[ev.FollowUpID] = true
	}

	// 新有新无：首次进入队列。
	for _, f := range items {
		if _, ok := oldByID[f.ID]; ok || covered[f.ID] {
			continue
		}
		if err := insertFollowUpEventTx(ctx, tx, FollowUpEvent{
			ID:         uuid.NewString(),
			FollowUpID: f.ID,
			ThreadID:   threadID,
			Kind:       FollowUpEventQueued,
			SourceID:   "enqueue",
			Text:       f.Text,
			CreatedAt:  now,
		}); err != nil {
			return err
		}
	}

	// 同 ID 内容变化：文本或附件（含元数据）签名变化即记一条 edited。
	for _, f := range items {
		old, ok := oldByID[f.ID]
		if !ok || covered[f.ID] {
			continue
		}
		oldSig, err := followUpContentSignature(old)
		if err != nil {
			return err
		}
		newSig, err := followUpContentSignature(f)
		if err != nil {
			return err
		}
		if oldSig == newSig {
			continue
		}
		if err := insertFollowUpEventTx(ctx, tx, FollowUpEvent{
			ID:         uuid.NewString(),
			FollowUpID: f.ID,
			ThreadID:   threadID,
			Kind:       FollowUpEventEdited,
			SourceID:   newSig,
			Text:       f.Text,
			CreatedAt:  now,
		}); err != nil {
			return err
		}
	}

	// 旧有新无：显式事件没有覆盖时按 vanished 记录，作为漏埋点的安全网。
	for _, f := range oldItems {
		if _, ok := newByID[f.ID]; ok || covered[f.ID] {
			continue
		}
		if err := insertFollowUpEventTx(ctx, tx, FollowUpEvent{
			ID:         uuid.NewString(),
			FollowUpID: f.ID,
			ThreadID:   threadID,
			Kind:       FollowUpEventDropped,
			Reason:     FollowUpDropVanished,
			SourceID:   "diff",
			Text:       f.Text,
			CreatedAt:  now,
		}); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// listFollowUpsTx 在事务内按 position 升序读取队列快照，字段解析方式与 ListFollowUps 一致。
func listFollowUpsTx(ctx context.Context, tx *sql.Tx, threadID string) ([]FollowUp, error) {
	query := `
	SELECT id, text, intent, attachments, attachment_meta, position
	FROM thread_follow_ups
	WHERE thread_id = ?
	ORDER BY position ASC
	`
	rows, err := tx.QueryContext(ctx, query, threadID)
	if err != nil {
		return nil, fmt.Errorf("list old follow ups for thread %s: %w", threadID, err)
	}
	defer rows.Close()

	items := make([]FollowUp, 0)
	for rows.Next() {
		var f FollowUp
		var attachmentsStr, attachmentMetaStr string
		if err := rows.Scan(&f.ID, &f.Text, &f.Intent, &attachmentsStr, &attachmentMetaStr, &f.Position); err != nil {
			return nil, fmt.Errorf("scan old follow up: %w", err)
		}
		f.ThreadID = threadID
		if attachmentsStr == "" || attachmentsStr == "[]" {
			f.Attachments = []string{}
		} else if err := json.Unmarshal([]byte(attachmentsStr), &f.Attachments); err != nil {
			return nil, fmt.Errorf("unmarshal attachments for follow up %s: %w", f.ID, err)
		}
		if f.AttachmentMeta, err = decodeJSONList[FollowUpAttachment](attachmentMetaStr); err != nil {
			return nil, fmt.Errorf("decode attachment_meta for follow up %s: %w", f.ID, err)
		}
		items = append(items, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate old follow ups: %w", err)
	}
	return items, nil
}

// writeExplicitFollowUpEvent 校验并补齐调用方传入的事件，再重算 event_key 写入。
func writeExplicitFollowUpEvent(ctx context.Context, tx *sql.Tx, threadID string, now time.Time, ev FollowUpEvent, oldByID, newByID map[string]FollowUp) error {
	switch ev.Kind {
	case FollowUpEventQueued, FollowUpEventDelivered, FollowUpEventSteered, FollowUpEventDropped, FollowUpEventEdited,
		FollowUpEventDispatching, FollowUpEventAccepted, FollowUpEventUnknown, FollowUpEventFailed:
	default:
		return fmt.Errorf("unknown follow-up event kind %q", ev.Kind)
	}
	if ev.FollowUpID == "" {
		return fmt.Errorf("follow-up event %s is missing follow_up_id", ev.Kind)
	}
	ev.ThreadID = threadID
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = now
	}
	if ev.ID == "" {
		ev.ID = uuid.NewString()
	}
	if ev.Text == "" {
		if f, ok := newByID[ev.FollowUpID]; ok {
			ev.Text = f.Text
		} else if f, ok := oldByID[ev.FollowUpID]; ok {
			ev.Text = f.Text
		}
	}
	if ev.SourceID == "" {
		switch {
		case ev.Kind == FollowUpEventQueued:
			ev.SourceID = "enqueue"
		case ev.Kind == FollowUpEventDropped && ev.Reason == FollowUpDropVanished:
			ev.SourceID = "diff"
		case ev.Kind == FollowUpEventEdited:
			ev.SourceID = textSignature(ev.Text)
		case ev.Kind == FollowUpEventDelivered || ev.Kind == FollowUpEventSteered:
			return fmt.Errorf("follow-up event %s for %s is missing source_id", ev.Kind, ev.FollowUpID)
		// 投递状态事件按“投递实例”（turn_id/message_id）区分，source_id 缺失会让 event_key 退化，
		// 同一 follow-up 的多次投递会被 UNIQUE 静默吞掉；因此与 delivered/steered 一样强制要求。
		case ev.Kind == FollowUpEventDispatching || ev.Kind == FollowUpEventAccepted ||
			ev.Kind == FollowUpEventUnknown || ev.Kind == FollowUpEventFailed:
			return fmt.Errorf("follow-up event %s for %s is missing source_id", ev.Kind, ev.FollowUpID)
		}
	}
	return insertFollowUpEventTx(ctx, tx, ev)
}

// insertFollowUpEventTx 用 INSERT OR IGNORE 写入事件；重复的 event_key 被静默吸收。
func insertFollowUpEventTx(ctx context.Context, tx *sql.Tx, ev FollowUpEvent) error {
	ev.EventKey = followUpEventKey(ev.Kind, ev.Reason, ev.ThreadID, ev.FollowUpID, ev.SourceID)
	if _, err := tx.ExecContext(ctx, `
	INSERT OR IGNORE INTO thread_follow_up_events
		(id, event_key, follow_up_id, thread_id, kind, reason, source_id, text, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, ev.ID, ev.EventKey, ev.FollowUpID, ev.ThreadID, ev.Kind, ev.Reason, ev.SourceID, ev.Text, formatTime(ev.CreatedAt)); err != nil {
		return fmt.Errorf("insert follow-up event %s: %w", ev.ID, err)
	}
	return nil
}

// ListFollowUpEvents 按 created_at、rowid 升序返回线程事件；limit<=0 表示不限制。
func (s *Store) ListFollowUpEvents(ctx context.Context, threadID string, limit int) ([]FollowUpEvent, error) {
	query := `
	SELECT id, event_key, follow_up_id, thread_id, kind, reason, source_id, text, created_at
	FROM thread_follow_up_events
	WHERE thread_id = ?
	ORDER BY created_at ASC, rowid ASC
	`
	args := []any{threadID}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list follow-up events for thread %s: %w", threadID, err)
	}
	defer rows.Close()

	events := make([]FollowUpEvent, 0)
	for rows.Next() {
		var ev FollowUpEvent
		var createdAtStr string
		if err := rows.Scan(&ev.ID, &ev.EventKey, &ev.FollowUpID, &ev.ThreadID, &ev.Kind, &ev.Reason, &ev.SourceID, &ev.Text, &createdAtStr); err != nil {
			return nil, fmt.Errorf("scan follow-up event: %w", err)
		}
		if ev.CreatedAt, err = parseTime(createdAtStr); err != nil {
			return nil, fmt.Errorf("parse follow-up event created_at: %w", err)
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate follow-up events: %w", err)
	}
	return events, nil
}

// PruneFollowUpEvents 删除 created_at 早于 before 的事件。
func (s *Store) PruneFollowUpEvents(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM thread_follow_up_events WHERE created_at < ?", formatTime(before))
	if err != nil {
		return 0, fmt.Errorf("prune follow-up events: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}
