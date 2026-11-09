package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	DecisionStatusOpen       = "open"
	DecisionStatusAnswered   = "answered"
	DecisionStatusIgnored    = "ignored"
	DecisionStatusSuperseded = "superseded"
)

var ErrDecisionClosed = errors.New("decision closed")

type StewardDecision struct {
	ID          string
	TaskID      string
	ThreadID    string
	Question    string
	Context     string
	OptionsJSON string
	Recommended string
	Status      string
	Answer      string
	AnsweredBy  string
	AnsweredAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const decisionColumns = `id, task_id, thread_id, question, context, options_json, recommended,
	status, answer, answered_by, answered_at, created_at, updated_at`

func scanStewardDecision(row rowScanner) (StewardDecision, error) {
	var d StewardDecision
	var answered sql.NullString
	var created, updated string
	if err := row.Scan(&d.ID, &d.TaskID, &d.ThreadID, &d.Question, &d.Context, &d.OptionsJSON,
		&d.Recommended, &d.Status, &d.Answer, &d.AnsweredBy, &answered, &created, &updated); err != nil {
		return StewardDecision{}, err
	}
	var err error
	if d.CreatedAt, err = parseTime(created); err != nil {
		return StewardDecision{}, err
	}
	if d.UpdatedAt, err = parseTime(updated); err != nil {
		return StewardDecision{}, err
	}
	if answered.Valid {
		at, err := parseTime(answered.String)
		if err != nil {
			return StewardDecision{}, err
		}
		d.AnsweredAt = &at
	}
	return d, nil
}

func (s *Store) CreateStewardDecision(ctx context.Context, d StewardDecision) (StewardDecision, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StewardDecision{}, err
	}
	defer tx.Rollback()
	// 先取得写锁，再读取最大编号，避免多个数据库连接同时读取相同的编号。
	if _, err = tx.ExecContext(ctx, `UPDATE steward_decisions SET id = id WHERE 0`); err != nil {
		return StewardDecision{}, err
	}
	var maxID int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(CAST(SUBSTR(id, 3) AS INTEGER)), 0) FROM steward_decisions`).Scan(&maxID); err != nil {
		return StewardDecision{}, err
	}
	d.ID = fmt.Sprintf("D-%04d", maxID+1)
	now := time.Now().UTC()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	if d.UpdatedAt.IsZero() {
		d.UpdatedAt = d.CreatedAt
	}
	if d.OptionsJSON == "" {
		d.OptionsJSON = "[]"
	}
	if d.Status == "" {
		d.Status = DecisionStatusOpen
	}
	if d.TaskID != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE steward_decisions SET status = ?, updated_at = ? WHERE task_id = ? AND status = ?`,
			DecisionStatusSuperseded, formatTime(now), d.TaskID, DecisionStatusOpen); err != nil {
			return StewardDecision{}, err
		}
	}
	var answeredAt any
	if d.AnsweredAt != nil {
		answeredAt = formatTime(*d.AnsweredAt)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO steward_decisions (`+decisionColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.TaskID, d.ThreadID, d.Question, d.Context, d.OptionsJSON, d.Recommended,
		d.Status, d.Answer, d.AnsweredBy, answeredAt, formatTime(d.CreatedAt), formatTime(d.UpdatedAt))
	if err != nil {
		return StewardDecision{}, err
	}
	if err = tx.Commit(); err != nil {
		return StewardDecision{}, err
	}
	return d, nil
}

func (s *Store) GetStewardDecision(ctx context.Context, id string) (StewardDecision, error) {
	d, err := scanStewardDecision(s.db.QueryRowContext(ctx, `SELECT `+decisionColumns+` FROM steward_decisions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return StewardDecision{}, ErrNotFound
	}
	return d, err
}

func (s *Store) ListStewardDecisions(ctx context.Context, status string, limit int) ([]StewardDecision, error) {
	query := `SELECT ` + decisionColumns + ` FROM steward_decisions`
	var args []any
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	decisions := make([]StewardDecision, 0)
	for rows.Next() {
		d, err := scanStewardDecision(rows)
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, d)
	}
	return decisions, rows.Err()
}

func (s *Store) AnswerStewardDecision(ctx context.Context, id, answer, by string, at time.Time) (StewardDecision, error) {
	d, err := scanStewardDecision(s.db.QueryRowContext(ctx, `UPDATE steward_decisions
		SET status = ?, answer = ?, answered_by = ?, answered_at = ?, updated_at = ?
		WHERE id = ? AND status = ? RETURNING `+decisionColumns,
		DecisionStatusAnswered, answer, by, formatTime(at), formatTime(at), id, DecisionStatusOpen))
	if errors.Is(err, sql.ErrNoRows) {
		_, err = s.GetStewardDecision(ctx, id)
		if err != nil {
			return StewardDecision{}, err
		}
		return StewardDecision{}, ErrDecisionClosed
	}
	return d, err
}

// ReanswerStewardDecision 在五分钟改答窗口内更新已答决策卡的答案。
func (s *Store) ReanswerStewardDecision(ctx context.Context, id, answer, by string, at time.Time) (StewardDecision, error) {
	d, err := scanStewardDecision(s.db.QueryRowContext(ctx, `UPDATE steward_decisions
		SET answer = ?, answered_by = ?, answered_at = ?, updated_at = ?
		WHERE id = ? AND status = ? RETURNING `+decisionColumns,
		answer, by, formatTime(at), formatTime(at), id, DecisionStatusAnswered))
	if errors.Is(err, sql.ErrNoRows) {
		_, err = s.GetStewardDecision(ctx, id)
		if err != nil {
			return StewardDecision{}, err
		}
		return StewardDecision{}, ErrDecisionClosed
	}
	return d, err
}

func (s *Store) SupersedeOpenDecisionsForTask(ctx context.Context, taskID string) error {
	return supersedeOpenDecisionsForTask(ctx, s.db.ExecContext, taskID)
}

func supersedeOpenDecisionsForTask(ctx context.Context, exec func(context.Context, string, ...any) (sql.Result, error), taskID string) error {
	if taskID == "" {
		return nil
	}
	_, err := exec(ctx, `UPDATE steward_decisions SET status = ?, updated_at = ? WHERE task_id = ? AND status = ?`,
		DecisionStatusSuperseded, formatTime(time.Now().UTC()), taskID, DecisionStatusOpen)
	return err
}
