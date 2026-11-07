package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// AttentionEvent 表示一条待处理事件：某个对象（会话、任务、自动化运行、Bee 等）
// 进入了需要用户关注的状态，并在用户处理或对象恢复正常后消解。
type AttentionEvent struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	ProjectID   string `json:"project_id"`
	// ThreadID 用于前端点击事件后跳转到对应会话，可为空。
	ThreadID string `json:"thread_id"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	// EnteredAt 是对象进入该状态的时刻，由调用方给出，与写入时刻可能不同。
	EnteredAt time.Time `json:"entered_at"`
	// DedupeKey 由调用方构造，保证同一对象同一进入时刻只落一条。
	DedupeKey  string     `json:"dedupe_key"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
	ResolvedBy string     `json:"resolved_by"`
}

// AttentionEventFilter 是待处理事件的列表过滤条件。
type AttentionEventFilter struct {
	// UnresolvedOnly 为 true 时只返回 resolved_at 为空的行。
	UnresolvedOnly bool
	// ProjectID 非空时只返回该项目下的事件。
	ProjectID string
	// Kinds 非空时只返回这些类型的事件；为空表示不按类型过滤。
	Kinds []string
	// Limit 为返回条数上限，小于等于 0 时按 defaultAttentionEventLimit 处理。
	Limit int
}

// defaultAttentionEventLimit 是列表未指定条数上限时的默认返回条数。
const defaultAttentionEventLimit = 200

// attentionEventColumns 为 attention_events 表读取时使用的列顺序，读写两侧共用。
const attentionEventColumns = `id, kind, subject_type, subject_id, project_id, thread_id,
	title, detail, entered_at, dedupe_key, created_at, updated_at, resolved_at, resolved_by`

// InsertAttentionEvent 写入一条待处理事件：ID 为空时生成 UUID，
// 创建时间与更新时间取当前时间，消解时间沿用入参（为空表示未消解）。
func (s *Store) InsertAttentionEvent(ctx context.Context, e AttentionEvent) (AttentionEvent, error) {
	if strings.TrimSpace(e.Kind) == "" {
		return AttentionEvent{}, errors.New("insert attention event: kind is empty")
	}
	if strings.TrimSpace(e.SubjectType) == "" {
		return AttentionEvent{}, errors.New("insert attention event: subject type is empty")
	}
	if strings.TrimSpace(e.SubjectID) == "" {
		return AttentionEvent{}, errors.New("insert attention event: subject id is empty")
	}
	if strings.TrimSpace(e.DedupeKey) == "" {
		return AttentionEvent{}, errors.New("insert attention event: dedupe key is empty")
	}
	now := time.Now().UTC()
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.EnteredAt.IsZero() {
		e.EnteredAt = now
	}
	e.CreatedAt = now
	e.UpdatedAt = now
	var resolvedAt any
	if e.ResolvedAt != nil {
		resolvedAt = formatTime(*e.ResolvedAt)
	}

	_, err := s.db.ExecContext(ctx, `
	INSERT INTO attention_events (`+attentionEventColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`, e.ID, e.Kind, e.SubjectType, e.SubjectID, e.ProjectID, e.ThreadID,
		e.Title, e.Detail, formatTime(e.EnteredAt), e.DedupeKey,
		formatTime(e.CreatedAt), formatTime(e.UpdatedAt), resolvedAt, e.ResolvedBy)
	if err != nil {
		return AttentionEvent{}, fmt.Errorf("insert attention event %s: %w", e.ID, err)
	}
	return e, nil
}

// GetAttentionEventByDedupeKey 按去重键读取待处理事件；不存在时返回 ErrNotFound。
func (s *Store) GetAttentionEventByDedupeKey(ctx context.Context, key string) (AttentionEvent, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+attentionEventColumns+" FROM attention_events WHERE dedupe_key = ?", key)
	return scanAttentionEvent(row)
}

// GetAttentionEvent 按 ID 读取待处理事件；不存在时返回 ErrNotFound。
func (s *Store) GetAttentionEvent(ctx context.Context, id string) (AttentionEvent, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+attentionEventColumns+" FROM attention_events WHERE id = ?", id)
	return scanAttentionEvent(row)
}

// CountAttentionEventsSince 统计同一类型、同一对象自 since 之后（按创建时间）写入的事件条数，
// 用于滚动窗口内的写入截停。
func (s *Store) CountAttentionEventsSince(ctx context.Context, kind, subjectType, subjectID string, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
	SELECT COUNT(*) FROM attention_events
	WHERE kind = ? AND subject_type = ? AND subject_id = ? AND created_at >= ?
	`, kind, subjectType, subjectID, formatTime(since)).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count attention events since: %w", err)
	}
	return count, nil
}

// ResolveAttentionEvent 消解单条未消解的事件；返回是否有行被更新，
// 事件不存在或已消解都返回 false 且不报错。
func (s *Store) ResolveAttentionEvent(ctx context.Context, id string, at time.Time, by string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
	UPDATE attention_events SET resolved_at = ?, resolved_by = ?, updated_at = ?
	WHERE id = ? AND resolved_at IS NULL
	`, formatTime(at), by, formatTime(at), id)
	if err != nil {
		return false, fmt.Errorf("resolve attention event %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// ResolveAttentionEventsBySubject 消解某个对象下指定类型的未消解事件，并返回被消解的行；
// kinds 为空表示该对象的全部类型。
func (s *Store) ResolveAttentionEventsBySubject(ctx context.Context, subjectType, subjectID string, kinds []string, at time.Time, by string) ([]AttentionEvent, error) {
	query := "UPDATE attention_events SET resolved_at = ?, resolved_by = ?, updated_at = ?" +
		" WHERE subject_type = ? AND subject_id = ? AND resolved_at IS NULL"
	args := []any{formatTime(at), by, formatTime(at), subjectType, subjectID}
	if len(kinds) > 0 {
		placeholders := make([]string, 0, len(kinds))
		for _, kind := range kinds {
			placeholders = append(placeholders, "?")
			args = append(args, kind)
		}
		query += " AND kind IN (" + strings.Join(placeholders, ", ") + ")"
	}
	query += " RETURNING " + attentionEventColumns

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("resolve attention events by subject %s/%s: %w", subjectType, subjectID, err)
	}
	defer rows.Close()

	events := make([]AttentionEvent, 0)
	for rows.Next() {
		e, err := scanAttentionEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate resolved attention events: %w", err)
	}
	return events, nil
}

// ListAttentionEvents 列出待处理事件，按创建时间倒序；
// Limit 小于等于 0 时默认返回 defaultAttentionEventLimit 条。
func (s *Store) ListAttentionEvents(ctx context.Context, filter AttentionEventFilter) ([]AttentionEvent, error) {
	query := "SELECT " + attentionEventColumns + " FROM attention_events"
	var (
		conds []string
		args  []any
	)
	if filter.UnresolvedOnly {
		conds = append(conds, "resolved_at IS NULL")
	}
	if projectID := strings.TrimSpace(filter.ProjectID); projectID != "" {
		conds = append(conds, "project_id = ?")
		args = append(args, projectID)
	}
	if len(filter.Kinds) > 0 {
		placeholders := make([]string, 0, len(filter.Kinds))
		for _, kind := range filter.Kinds {
			placeholders = append(placeholders, "?")
			args = append(args, kind)
		}
		conds = append(conds, "kind IN ("+strings.Join(placeholders, ", ")+")")
	}
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += " ORDER BY created_at DESC, id DESC LIMIT ?"
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultAttentionEventLimit
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list attention events: %w", err)
	}
	defer rows.Close()

	events := make([]AttentionEvent, 0)
	for rows.Next() {
		e, err := scanAttentionEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate attention events: %w", err)
	}
	return events, nil
}

// CountUnresolvedAttentionEvents 统计全部未消解事件的条数。
func (s *Store) CountUnresolvedAttentionEvents(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM attention_events WHERE resolved_at IS NULL").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count unresolved attention events: %w", err)
	}
	return count, nil
}

// CountUnresolvedAttentionEventsByKinds 统计指定类型的未消解事件条数，口径与
// CountUnresolvedAttentionEvents 相同；kinds 为空表示不按类型过滤，等同全量统计。
func (s *Store) CountUnresolvedAttentionEventsByKinds(ctx context.Context, kinds []string) (int, error) {
	query := "SELECT COUNT(*) FROM attention_events WHERE resolved_at IS NULL"
	args := make([]any, 0, len(kinds))
	if len(kinds) > 0 {
		placeholders := make([]string, 0, len(kinds))
		for _, kind := range kinds {
			placeholders = append(placeholders, "?")
			args = append(args, kind)
		}
		query += " AND kind IN (" + strings.Join(placeholders, ", ") + ")"
	}
	var count int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count unresolved attention events by kinds: %w", err)
	}
	return count, nil
}

// scanAttentionEvent 按 attentionEventColumns 顺序读取一行待处理事件。
func scanAttentionEvent(row rowScanner) (AttentionEvent, error) {
	var (
		e          AttentionEvent
		enteredAt  string
		createdAt  string
		updatedAt  string
		resolvedAt sql.NullString
	)
	err := row.Scan(
		&e.ID, &e.Kind, &e.SubjectType, &e.SubjectID, &e.ProjectID, &e.ThreadID,
		&e.Title, &e.Detail, &enteredAt, &e.DedupeKey, &createdAt, &updatedAt,
		&resolvedAt, &e.ResolvedBy,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AttentionEvent{}, ErrNotFound
	}
	if err != nil {
		return AttentionEvent{}, fmt.Errorf("scan attention event: %w", err)
	}
	if e.EnteredAt, err = parseTime(enteredAt); err != nil {
		return AttentionEvent{}, fmt.Errorf("parse attention event entered_at: %w", err)
	}
	if e.CreatedAt, err = parseTime(createdAt); err != nil {
		return AttentionEvent{}, fmt.Errorf("parse attention event created_at: %w", err)
	}
	if e.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return AttentionEvent{}, fmt.Errorf("parse attention event updated_at: %w", err)
	}
	if resolvedAt.Valid && strings.TrimSpace(resolvedAt.String) != "" {
		at, err := parseTime(resolvedAt.String)
		if err != nil {
			return AttentionEvent{}, fmt.Errorf("parse attention event resolved_at: %w", err)
		}
		e.ResolvedAt = &at
	}
	return e, nil
}
