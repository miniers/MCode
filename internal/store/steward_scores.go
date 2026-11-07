package store

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"
)

// 总管建议计分的四种结局。
const (
	StewardScorePending    = "pending"
	StewardScoreAgree      = "agree"
	StewardScoreDisagree   = "disagree"
	StewardScoreUnanswered = "unanswered"
)

// 保留策略：只留最近 90 天，且最多 5000 行。
const (
	stewardScoreRetention     = 90 * 24 * time.Hour
	stewardScoreMaxRows       = 5000
	stewardScorePruneInterval = time.Hour
)

// 清理节流放在包级变量里，避免为了统计给 Store 结构加字段。
var (
	stewardScorePruneMu  sync.Mutex
	stewardScorePrunedAt time.Time
)

// StewardApprovalScore 是一条“总管提过建议的审批请求”的计分记录。
// 不存请求标题与命令正文：统计用不到，也避免把内容复制进长期表。
type StewardApprovalScore struct {
	RequestID, ThreadID, TurnID, Kind, ToolName string
	SuggestedOption, SuggestedRole              string
	ChosenOption, ChosenRole                    string
	Outcome, AnswerSource, UnansweredReason     string
	CreatedAt, UpdatedAt                        time.Time
	AnsweredAt                                  *time.Time
}

// stewardScoreSelectCols 是 steward_approval_scores 行的读取列清单，新增列只改这里。
const stewardScoreSelectCols = `
	request_id, thread_id, turn_id, kind, tool_name,
	suggested_option, suggested_role, chosen_option, chosen_role,
	outcome, answer_source, unanswered_reason, created_at, updated_at, answered_at
`

// UpsertStewardApprovalScore 登记一条总管建议；主键冲突时只覆盖建议字段。
// 已判定的行（outcome 非 pending）不会被翻回待定：建议晚于答复到达时不污染已有结局。
func (s *Store) UpsertStewardApprovalScore(ctx context.Context, score StewardApprovalScore) error {
	if score.RequestID == "" {
		return fmt.Errorf("upsert steward approval score: request_id is empty")
	}
	now := time.Now().UTC()
	createdAt := score.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	updatedAt := score.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = now
	}
	if _, err := s.db.ExecContext(ctx, `
	INSERT INTO steward_approval_scores
		(request_id, thread_id, turn_id, kind, suggested_option, suggested_role, outcome, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(request_id) DO UPDATE SET
		suggested_option = excluded.suggested_option,
		suggested_role   = excluded.suggested_role,
		updated_at       = excluded.updated_at
	WHERE steward_approval_scores.outcome = ?
	`, score.RequestID, score.ThreadID, score.TurnID, score.Kind, score.SuggestedOption,
		score.SuggestedRole, StewardScorePending, formatTime(createdAt), formatTime(updatedAt),
		StewardScorePending); err != nil {
		return fmt.Errorf("upsert steward approval score %s: %w", score.RequestID, err)
	}
	s.pruneStewardScoresHourly(ctx)
	return nil
}

// RecordStewardAnswer 记录答复：建议角色与所选角色一致记 agree，否则记 disagree。
// 只更新 pending 行，先到者胜；没有建议行或已结算时影响 0 行，属正常情况，不报错。
func (s *Store) RecordStewardAnswer(ctx context.Context, requestID, kind, toolName, chosenOption, chosenRole, source string) error {
	now := formatTime(time.Now().UTC())
	if _, err := s.db.ExecContext(ctx, `
	UPDATE steward_approval_scores SET
		kind          = ?,
		tool_name     = ?,
		chosen_option = ?,
		chosen_role   = ?,
		outcome       = CASE WHEN suggested_role <> '' AND suggested_role = ? THEN ? ELSE ? END,
		answer_source = ?,
		answered_at   = ?,
		updated_at    = ?
	WHERE request_id = ? AND outcome = ?
	`, kind, toolName, chosenOption, chosenRole, chosenRole,
		StewardScoreAgree, StewardScoreDisagree, source, now, now,
		requestID, StewardScorePending); err != nil {
		return fmt.Errorf("record steward answer %s: %w", requestID, err)
	}
	return nil
}

// MarkStewardScoreUnanswered 把仍未答复的行标记为 unanswered 并记下原因。
// 只改 pending 行；requestID 为空时直接返回，省掉调用方判断。
func (s *Store) MarkStewardScoreUnanswered(ctx context.Context, requestID, reason string) error {
	if requestID == "" {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `
	UPDATE steward_approval_scores SET outcome = ?, unanswered_reason = ?, updated_at = ?
	WHERE request_id = ? AND outcome = ?
	`, StewardScoreUnanswered, reason, formatTime(time.Now().UTC()), requestID, StewardScorePending); err != nil {
		return fmt.Errorf("mark steward score %s unanswered: %w", requestID, err)
	}
	return nil
}

// ListStewardApprovalScores 按登记时间倒序返回计分记录；limit <= 0 表示不限。
func (s *Store) ListStewardApprovalScores(ctx context.Context, limit int) ([]StewardApprovalScore, error) {
	query := "SELECT " + stewardScoreSelectCols + " FROM steward_approval_scores ORDER BY created_at DESC, request_id DESC"
	args := []any{}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list steward approval scores: %w", err)
	}
	defer rows.Close()

	scores := make([]StewardApprovalScore, 0)
	for rows.Next() {
		score, err := scanStewardApprovalScore(rows)
		if err != nil {
			return nil, err
		}
		scores = append(scores, score)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate steward approval scores: %w", err)
	}
	return scores, nil
}

// PruneStewardApprovalScores 按保留策略清理：maxAge <= 0 跳过按时间清理，
// maxRows <= 0 跳过按行数清理。两个条件可单独使用，便于调用方与测试分别验证。
func (s *Store) PruneStewardApprovalScores(ctx context.Context, maxAge time.Duration, maxRows int) error {
	if maxAge > 0 {
		cutoff := formatTime(time.Now().UTC().Add(-maxAge))
		if _, err := s.db.ExecContext(ctx, `
		DELETE FROM steward_approval_scores WHERE created_at < ?
		`, cutoff); err != nil {
			return fmt.Errorf("prune steward approval scores by age: %w", err)
		}
	}
	if maxRows > 0 {
		if _, err := s.db.ExecContext(ctx, `
		DELETE FROM steward_approval_scores WHERE request_id IN (
			SELECT request_id FROM steward_approval_scores
			ORDER BY created_at DESC, request_id DESC LIMIT -1 OFFSET ?
		)
		`, maxRows); err != nil {
			return fmt.Errorf("prune steward approval scores by rows: %w", err)
		}
	}
	return nil
}

// pruneStewardScoresHourly 在登记建议后顺带清理，用包级时间戳节流到每小时最多一次。
// 清理失败只记日志：统计数据的保留不该影响审批主链路。
func (s *Store) pruneStewardScoresHourly(ctx context.Context) {
	stewardScorePruneMu.Lock()
	if time.Since(stewardScorePrunedAt) < stewardScorePruneInterval {
		stewardScorePruneMu.Unlock()
		return
	}
	stewardScorePrunedAt = time.Now()
	stewardScorePruneMu.Unlock()

	if err := s.PruneStewardApprovalScores(ctx, stewardScoreRetention, stewardScoreMaxRows); err != nil {
		log.Printf("store: 清理总管建议计分失败: %v", err)
	}
}

// scanStewardApprovalScore 读取一行 steward_approval_scores，时间列按本包的定宽时间格式解析。
func scanStewardApprovalScore(s rowScanner) (StewardApprovalScore, error) {
	var score StewardApprovalScore
	var createdAtStr, updatedAtStr string
	var answeredAtStr sql.NullString
	if err := s.Scan(
		&score.RequestID, &score.ThreadID, &score.TurnID, &score.Kind, &score.ToolName,
		&score.SuggestedOption, &score.SuggestedRole, &score.ChosenOption, &score.ChosenRole,
		&score.Outcome, &score.AnswerSource, &score.UnansweredReason,
		&createdAtStr, &updatedAtStr, &answeredAtStr,
	); err != nil {
		return StewardApprovalScore{}, fmt.Errorf("scan steward approval score: %w", err)
	}
	createdAt, err := parseTime(createdAtStr)
	if err != nil {
		return StewardApprovalScore{}, fmt.Errorf("parse steward score %s created_at: %w", score.RequestID, err)
	}
	score.CreatedAt = createdAt
	updatedAt, err := parseTime(updatedAtStr)
	if err != nil {
		return StewardApprovalScore{}, fmt.Errorf("parse steward score %s updated_at: %w", score.RequestID, err)
	}
	score.UpdatedAt = updatedAt
	if answeredAtStr.Valid && answeredAtStr.String != "" {
		answeredAt, err := parseTime(answeredAtStr.String)
		if err != nil {
			return StewardApprovalScore{}, fmt.Errorf("parse steward score %s answered_at: %w", score.RequestID, err)
		}
		score.AnsweredAt = &answeredAt
	}
	return score, nil
}
