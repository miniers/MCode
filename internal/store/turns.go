package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const turnSelectCols = `
	id, thread_id, turn_index, status, started_at, completed_at,
	COALESCE(base_checkpoint, ''), COALESCE(finish_checkpoint, ''),
	user_prompt, COALESCE(system_injected_context, ''), COALESCE(raw_output, ''),
	COALESCE(touched_files, ''), COALESCE(input_tokens, 0), COALESCE(output_tokens, 0),
	COALESCE(cost_micros, 0), COALESCE(error_message, '')
`

func scanTurn(s rowScanner) (Turn, error) {
	var t Turn
	var startedAtStr string
	var completedAtStr sql.NullString
	var touchedFilesJSON string

	err := s.Scan(
		&t.ID,
		&t.ThreadID,
		&t.TurnIndex,
		&t.Status,
		&startedAtStr,
		&completedAtStr,
		&t.BaseCheckpoint,
		&t.FinishCheckpoint,
		&t.UserPrompt,
		&t.SystemInjectedContext,
		&t.RawOutput,
		&touchedFilesJSON,
		&t.InputTokens,
		&t.OutputTokens,
		&t.CostMicros,
		&t.ErrorMessage,
	)
	if err != nil {
		return Turn{}, err
	}

	if t.StartedAt, err = parseTime(startedAtStr); err != nil {
		return Turn{}, fmt.Errorf("parse turn started_at: %w", err)
	}
	if completedAtStr.Valid && completedAtStr.String != "" {
		ct, err := parseTime(completedAtStr.String)
		if err != nil {
			return Turn{}, fmt.Errorf("parse turn completed_at: %w", err)
		}
		t.CompletedAt = &ct
	}
	if touchedFilesJSON != "" {
		files, err := decodeJSONList[string](touchedFilesJSON)
		if err != nil {
			return Turn{}, fmt.Errorf("decode turn touched_files: %w", err)
		}
		t.TouchedFiles = files
	}
	return t, nil
}

// CreateTurn 插入一条新的轮次记录。
func (s *Store) CreateTurn(ctx context.Context, turn Turn) error {
	if turn.StartedAt.IsZero() {
		turn.StartedAt = time.Now().UTC()
	}

	var completedAt any
	if turn.CompletedAt != nil {
		completedAt = formatTime(*turn.CompletedAt)
	}

	touchedFilesJSON, err := encodeJSONList(turn.TouchedFiles)
	if err != nil {
		return fmt.Errorf("encode touched_files: %w", err)
	}

	query := `
	INSERT INTO turns (
		id, thread_id, turn_index, status, started_at, completed_at,
		base_checkpoint, finish_checkpoint, user_prompt,
		system_injected_context, raw_output, touched_files,
		input_tokens, output_tokens, cost_micros, error_message
	) VALUES (
		?, ?, ?, ?, ?, ?,
		NULLIF(?, ''), NULLIF(?, ''), ?,
		NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''),
		?, ?, ?, NULLIF(?, '')
	)
	`
	_, err = s.db.ExecContext(ctx, query,
		turn.ID,
		turn.ThreadID,
		turn.TurnIndex,
		turn.Status,
		formatTime(turn.StartedAt),
		completedAt,
		turn.BaseCheckpoint,
		turn.FinishCheckpoint,
		turn.UserPrompt,
		turn.SystemInjectedContext,
		turn.RawOutput,
		touchedFilesJSON,
		turn.InputTokens,
		turn.OutputTokens,
		turn.CostMicros,
		turn.ErrorMessage,
	)
	if err != nil {
		return fmt.Errorf("create turn %s: %w", turn.ID, err)
	}
	return nil
}

// UpdateTurn 按 id 更新除 id/thread_id 之外的列；若不存在返回 ErrNotFound。
func (s *Store) UpdateTurn(ctx context.Context, turn Turn) error {
	var completedAt any
	if turn.CompletedAt != nil {
		completedAt = formatTime(*turn.CompletedAt)
	}

	touchedFilesJSON, err := encodeJSONList(turn.TouchedFiles)
	if err != nil {
		return fmt.Errorf("encode touched_files for turn %s: %w", turn.ID, err)
	}

	query := `
	UPDATE turns SET
		turn_index = ?,
		status = ?,
		started_at = ?,
		completed_at = ?,
		base_checkpoint = NULLIF(?, ''),
		finish_checkpoint = NULLIF(?, ''),
		user_prompt = ?,
		system_injected_context = NULLIF(?, ''),
		raw_output = NULLIF(?, ''),
		touched_files = NULLIF(?, ''),
		input_tokens = ?,
		output_tokens = ?,
		cost_micros = ?,
		error_message = NULLIF(?, '')
	WHERE id = ?
	`
	res, err := s.db.ExecContext(ctx, query,
		turn.TurnIndex,
		turn.Status,
		formatTime(turn.StartedAt),
		completedAt,
		turn.BaseCheckpoint,
		turn.FinishCheckpoint,
		turn.UserPrompt,
		turn.SystemInjectedContext,
		turn.RawOutput,
		touchedFilesJSON,
		turn.InputTokens,
		turn.OutputTokens,
		turn.CostMicros,
		turn.ErrorMessage,
		turn.ID,
	)
	if err != nil {
		return fmt.Errorf("update turn %s: %w", turn.ID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// GetTurn 根据 ID 获取单轮记录，不存在时返回 ErrNotFound。
func (s *Store) GetTurn(ctx context.Context, id string) (Turn, error) {
	query := "SELECT " + turnSelectCols + " FROM turns WHERE id = ?"
	t, err := scanTurn(s.db.QueryRowContext(ctx, query, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Turn{}, ErrNotFound
		}
		return Turn{}, fmt.Errorf("get turn %s: %w", id, err)
	}
	return t, nil
}

// ListTurns 查询指定线程的所有轮次，按 turn_index 升序排列。
func (s *Store) ListTurns(ctx context.Context, threadID string) ([]Turn, error) {
	query := "SELECT " + turnSelectCols + " FROM turns WHERE thread_id = ? ORDER BY turn_index ASC"
	rows, err := s.db.QueryContext(ctx, query, threadID)
	if err != nil {
		return nil, fmt.Errorf("list turns: %w", err)
	}
	defer rows.Close()

	turns := make([]Turn, 0)
	for rows.Next() {
		t, err := scanTurn(rows)
		if err != nil {
			return nil, fmt.Errorf("scan turn: %w", err)
		}
		turns = append(turns, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate turns: %w", err)
	}
	return turns, nil
}

// ListRecentTurns 取指定线程最近 limit 轮记录，结果仍按 turn_index 升序排列。
func (s *Store) ListRecentTurns(ctx context.Context, threadID string, limit int) ([]Turn, error) {
	if limit <= 0 {
		return []Turn{}, nil
	}

	query := `
	SELECT ` + turnSelectCols + `
	FROM (
		SELECT *
		FROM turns
		WHERE thread_id = ?
		ORDER BY turn_index DESC
		LIMIT ?
	)
	ORDER BY turn_index ASC
	`
	rows, err := s.db.QueryContext(ctx, query, threadID, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent turns: %w", err)
	}
	defer rows.Close()

	turns := make([]Turn, 0)
	for rows.Next() {
		t, err := scanTurn(rows)
		if err != nil {
			return nil, fmt.Errorf("scan turn: %w", err)
		}
		turns = append(turns, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent turns: %w", err)
	}
	return turns, nil
}

// ListUnfinishedTurns 列出所有未收尾的轮次（status 为 running 或 starting），按会话与轮次排序。
// 供启动加载时把异常退出（崩溃、被杀）遗留下来的“进行中”轮次收尾为 interrupted。
func (s *Store) ListUnfinishedTurns(ctx context.Context) ([]Turn, error) {
	query := `
	SELECT ` + turnSelectCols + `
	FROM turns
	WHERE status IN ('running', 'starting')
	ORDER BY thread_id, turn_index
	`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list unfinished turns: %w", err)
	}
	defer rows.Close()

	turns := make([]Turn, 0)
	for rows.Next() {
		t, err := scanTurn(rows)
		if err != nil {
			return nil, fmt.Errorf("scan unfinished turn: %w", err)
		}
		turns = append(turns, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unfinished turns: %w", err)
	}
	return turns, nil
}

// RecallTurn 原子撤销轮次消息与用量，保留撤回审计记录。
func (s *Store) RecallTurn(ctx context.Context, threadID, turnID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 冷归档会话先整体回填，轮次消息才删得掉。
	if err := s.hydrateThreadTx(ctx, tx, threadID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE turns SET status = 'taken_back' WHERE id = ? AND thread_id = ?`, turnID, threadID)
	if err != nil {
		return fmt.Errorf("take back turn %s: %w", turnID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM messages WHERE thread_id = ? AND turn_id = ?`, threadID, turnID)
	if err != nil {
		return fmt.Errorf("delete messages for turn %s: %w", turnID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM turn_usage WHERE thread_id = ? AND turn_index = (SELECT turn_index FROM turns WHERE id = ?)`, threadID, turnID); err != nil {
		return err
	}
	return tx.Commit()
}
