package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Task 表示看板任务实体。
type Task struct {
	ID                 string    `json:"id"`
	ProjectID          *string   `json:"project_id"`
	ThreadID           *string   `json:"thread_id"`
	ReviewThreadID     *string   `json:"review_thread_id"`
	GoalID             *string   `json:"goal_id"`
	DependsOn          []string  `json:"depends_on"`
	Title              string    `json:"title"`
	Notes              string    `json:"notes"`
	AcceptanceCriteria string    `json:"acceptance_criteria"`
	Status             string    `json:"status"`
	Priority           string    `json:"priority"`
	IsDraft            bool      `json:"is_draft"`
	GoalPrompt         *string   `json:"goal_prompt"`
	ReviewSummary      *string   `json:"review_summary"`
	ReviewFeedback     *string   `json:"review_feedback"`
	ReviewStatus       *string   `json:"review_status"`
	AttemptCount       int       `json:"attempt_count"`
	RejectCount        int       `json:"reject_count"`
	LastFailureReason  *string   `json:"last_failure_reason"`
	LastActivityText   *string   `json:"last_activity_text"`
	Revision           int       `json:"revision"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// Goal 表示目标实体。
type Goal struct {
	ID            string         `json:"id"`
	ProjectID     string         `json:"project_id"`
	Title         string         `json:"title"`
	Prompt        string         `json:"prompt"`
	ContentHash   string         `json:"content_hash"`
	Status        string         `json:"status"`
	Owner         string         `json:"owner"`
	Policy        map[string]any `json:"policy"`
	Progress      map[string]any `json:"progress"`
	PlanThreadID  *string        `json:"plan_thread_id"`
	PlanSummary   *string        `json:"plan_summary"`
	StatusReason  *string        `json:"status_reason"`
	ResultSummary *string        `json:"result_summary"`
	Revision      int            `json:"revision"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// CreateTask 创建新任务，初始版本号置为 1。
func (s *Store) CreateTask(ctx context.Context, t Task) error {
	return createTask(ctx, t, s.db.ExecContext)
}

// CreateGoalTask 将任务及其 goal 顺序关联放在同一事务中，避免 CAS 失败留下孤立任务。
func (s *Store) CreateGoalTask(ctx context.Context, t Task, goalRevision int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE goals SET revision = revision + 1, updated_at = ?
		WHERE id = ? AND revision = ? AND project_id = ?
		AND status IN ('plan_review', 'running', 'paused')`,
		formatTime(time.Now().UTC()), t.GoalID, goalRevision, t.ProjectID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrRevisionConflict
	}
	if err := createTask(ctx, t, tx.ExecContext); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO goal_tasks (goal_id, task_id, position)
		SELECT ?, ?, COALESCE(MAX(position), -1) + 1 FROM goal_tasks WHERE goal_id = ?`,
		t.GoalID, t.ID, t.GoalID); err != nil {
		return err
	}
	return tx.Commit()
}

func createTask(ctx context.Context, t Task, exec func(context.Context, string, ...any) (sql.Result, error)) error {
	now := time.Now().UTC()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = t.CreatedAt
	}
	if t.Status == "" {
		t.Status = "todo"
	}
	if t.Priority == "" {
		t.Priority = "none"
	}
	t.Revision = 1

	dependsOnBytes, err := json.Marshal(t.DependsOn)
	if err != nil || string(dependsOnBytes) == "null" {
		dependsOnBytes = []byte("[]")
	}

	isDraftInt := 0
	if t.IsDraft {
		isDraftInt = 1
	}

	query := `
	INSERT INTO tasks (
		id, project_id, thread_id, review_thread_id, goal_id,
		depends_on, title, notes, acceptance_criteria, status, priority, is_draft, goal_prompt,
		review_summary, review_feedback, review_status,
		attempt_count, reject_count, last_failure_reason, last_activity_text,
		revision, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err = exec(ctx, query,
		t.ID,
		t.ProjectID,
		t.ThreadID,
		t.ReviewThreadID,
		t.GoalID,
		string(dependsOnBytes),
		t.Title,
		t.Notes,
		t.AcceptanceCriteria,
		t.Status,
		t.Priority,
		isDraftInt,
		t.GoalPrompt,
		t.ReviewSummary,
		t.ReviewFeedback,
		t.ReviewStatus,
		t.AttemptCount,
		t.RejectCount,
		t.LastFailureReason,
		t.LastActivityText,
		t.Revision,
		formatTime(t.CreatedAt),
		formatTime(t.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("create task %s: %w", t.ID, err)
	}
	return nil
}

// GetTask 根据 ID 查询任务，不存在时返回 ErrNotFound。
func (s *Store) GetTask(ctx context.Context, id string) (Task, error) {
	query := `
	SELECT
		id, project_id, thread_id, review_thread_id, goal_id,
		depends_on, title, notes, acceptance_criteria, status, priority, is_draft, goal_prompt,
		review_summary, review_feedback, review_status,
		attempt_count, reject_count, last_failure_reason, last_activity_text,
		revision, created_at, updated_at
	FROM tasks
	WHERE id = ?
	`
	var t Task
	var dependsOnStr, createdAtStr, updatedAtStr string
	var isDraftInt int
	err := s.db.QueryRowContext(ctx, query, id).Scan(
		&t.ID,
		&t.ProjectID,
		&t.ThreadID,
		&t.ReviewThreadID,
		&t.GoalID,
		&dependsOnStr,
		&t.Title,
		&t.Notes,
		&t.AcceptanceCriteria,
		&t.Status,
		&t.Priority,
		&isDraftInt,
		&t.GoalPrompt,
		&t.ReviewSummary,
		&t.ReviewFeedback,
		&t.ReviewStatus,
		&t.AttemptCount,
		&t.RejectCount,
		&t.LastFailureReason,
		&t.LastActivityText,
		&t.Revision,
		&createdAtStr,
		&updatedAtStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, ErrNotFound
		}
		return Task{}, fmt.Errorf("get task %s: %w", id, err)
	}

	t.IsDraft = (isDraftInt != 0)
	if err := json.Unmarshal([]byte(dependsOnStr), &t.DependsOn); err != nil {
		return Task{}, fmt.Errorf("decode task dependencies: %w", err)
	}
	if t.DependsOn == nil {
		t.DependsOn = []string{}
	}
	if t.CreatedAt, err = parseTime(createdAtStr); err != nil {
		return Task{}, fmt.Errorf("parse task created_at: %w", err)
	}
	if t.UpdatedAt, err = parseTime(updatedAtStr); err != nil {
		return Task{}, fmt.Errorf("parse task updated_at: %w", err)
	}
	return t, nil
}

// ListTasks 列出指定项目的任务，按 updated_at 降序排列。projectID 为空时列出全部。
func (s *Store) ListTasks(ctx context.Context, projectID string) ([]Task, error) {
	query := `
	SELECT
		id, project_id, thread_id, review_thread_id, goal_id,
		depends_on, title, notes, acceptance_criteria, status, priority, is_draft, goal_prompt,
		review_summary, review_feedback, review_status,
		attempt_count, reject_count, last_failure_reason, last_activity_text,
		revision, created_at, updated_at
	FROM tasks
	`
	var args []any
	if projectID != "" {
		query += " WHERE project_id = ?"
		args = append(args, projectID)
	}
	query += " ORDER BY rtrim(updated_at, 'Z') DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	tasks := make([]Task, 0)
	for rows.Next() {
		var t Task
		var dependsOnStr, createdAtStr, updatedAtStr string
		var isDraftInt int
		if err := rows.Scan(
			&t.ID,
			&t.ProjectID,
			&t.ThreadID,
			&t.ReviewThreadID,
			&t.GoalID,
			&dependsOnStr,
			&t.Title,
			&t.Notes,
			&t.AcceptanceCriteria,
			&t.Status,
			&t.Priority,
			&isDraftInt,
			&t.GoalPrompt,
			&t.ReviewSummary,
			&t.ReviewFeedback,
			&t.ReviewStatus,
			&t.AttemptCount,
			&t.RejectCount,
			&t.LastFailureReason,
			&t.LastActivityText,
			&t.Revision,
			&createdAtStr,
			&updatedAtStr,
		); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		t.IsDraft = (isDraftInt != 0)
		if err := json.Unmarshal([]byte(dependsOnStr), &t.DependsOn); err != nil {
			return nil, fmt.Errorf("decode task dependencies: %w", err)
		}
		if t.DependsOn == nil {
			t.DependsOn = []string{}
		}
		if t.CreatedAt, err = parseTime(createdAtStr); err != nil {
			return nil, fmt.Errorf("parse task created_at: %w", err)
		}
		if t.UpdatedAt, err = parseTime(updatedAtStr); err != nil {
			return nil, fmt.Errorf("parse task updated_at: %w", err)
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tasks: %w", err)
	}
	return tasks, nil
}

// UpdateTask 执行 CAS 乐观并发更新。
// 更新成功时版本号自增 1 并返回新记录。受影响行数为 0 时检查是否存在：不存在返回 ErrNotFound，存在则返回 ErrRevisionConflict。
func (s *Store) UpdateTask(ctx context.Context, t Task, expectedRevision int) (Task, error) {
	now := time.Now().UTC()
	t.UpdatedAt = now

	dependsOnBytes, err := json.Marshal(t.DependsOn)
	if err != nil || string(dependsOnBytes) == "null" {
		dependsOnBytes = []byte("[]")
	}

	isDraftInt := 0
	if t.IsDraft {
		isDraftInt = 1
	}

	query := `
	UPDATE tasks SET
		project_id = ?,
		thread_id = ?,
		review_thread_id = ?,
		goal_id = ?,
		depends_on = ?,
		title = ?,
		notes = ?,
		acceptance_criteria = ?,
		status = ?,
		priority = ?,
		is_draft = ?,
		goal_prompt = ?,
		review_summary = ?,
		review_feedback = ?,
		review_status = ?,
		attempt_count = ?,
		reject_count = ?,
		last_failure_reason = ?,
		last_activity_text = ?,
		revision = revision + 1,
		updated_at = ?
	WHERE id = ? AND revision = ?
	RETURNING created_at
	`
	var createdAt string
	err = s.db.QueryRowContext(ctx, query,
		t.ProjectID,
		t.ThreadID,
		t.ReviewThreadID,
		t.GoalID,
		string(dependsOnBytes),
		t.Title,
		t.Notes,
		t.AcceptanceCriteria,
		t.Status,
		t.Priority,
		isDraftInt,
		t.GoalPrompt,
		t.ReviewSummary,
		t.ReviewFeedback,
		t.ReviewStatus,
		t.AttemptCount,
		t.RejectCount,
		t.LastFailureReason,
		t.LastActivityText,
		formatTime(t.UpdatedAt),
		t.ID,
		expectedRevision,
	).Scan(&createdAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("update task %s: %w", t.ID, err)
	}

	if errors.Is(err, sql.ErrNoRows) {
		var exists int
		err := s.db.QueryRowContext(ctx, "SELECT 1 FROM tasks WHERE id = ?", t.ID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, ErrNotFound
		}
		if err != nil {
			return Task{}, fmt.Errorf("check task existence %s: %w", t.ID, err)
		}
		return Task{}, ErrRevisionConflict
	}

	if t.CreatedAt, err = parseTime(createdAt); err != nil {
		return Task{}, fmt.Errorf("parse task created_at: %w", err)
	}
	if t.DependsOn == nil {
		t.DependsOn = []string{}
	}
	t.Revision = expectedRevision + 1
	return t, nil
}

// DeleteTask 根据 ID 删除任务。若任务不存在返回 ErrNotFound。
func (s *Store) DeleteTask(ctx context.Context, id string, expectedRevision ...int) error {
	query := "DELETE FROM tasks WHERE id = ?"
	args := []any{id}
	if len(expectedRevision) > 0 {
		query += " AND revision = ?"
		args = append(args, expectedRevision[0])
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("delete task %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		if _, err := s.GetTask(ctx, id); err == nil {
			return ErrRevisionConflict
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		return ErrNotFound
	}
	return nil
}

// CreateGoal 创建新目标，初始版本号置为 1。
func (s *Store) CreateGoal(ctx context.Context, g Goal) error {
	now := time.Now().UTC()
	if g.CreatedAt.IsZero() {
		g.CreatedAt = now
	}
	if g.UpdatedAt.IsZero() {
		g.UpdatedAt = g.CreatedAt
	}
	if g.Status == "" {
		g.Status = "planning"
	}
	g.Revision = 1

	policyBytes, err := json.Marshal(g.Policy)
	if err != nil {
		return fmt.Errorf("encode goal policy: %w", err)
	}
	if string(policyBytes) == "null" {
		policyBytes = []byte("{}")
	}
	progressBytes, err := json.Marshal(g.Progress)
	if err != nil {
		return fmt.Errorf("encode goal progress: %w", err)
	}
	if string(progressBytes) == "null" {
		progressBytes = []byte("{}")
	}

	query := `
	INSERT INTO goals (
		id, project_id, title, prompt, content_hash, status, owner,
		policy, progress, plan_thread_id, plan_summary,
		status_reason, result_summary, revision, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err = s.db.ExecContext(ctx, query,
		g.ID,
		g.ProjectID,
		g.Title,
		g.Prompt,
		g.ContentHash,
		g.Status,
		g.Owner,
		string(policyBytes),
		string(progressBytes),
		g.PlanThreadID,
		g.PlanSummary,
		g.StatusReason,
		g.ResultSummary,
		g.Revision,
		formatTime(g.CreatedAt),
		formatTime(g.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("create goal %s: %w", g.ID, err)
	}
	return nil
}

// GetGoal 根据 ID 查询目标，不存在时返回 ErrNotFound。
func (s *Store) GetGoal(ctx context.Context, id string) (Goal, error) {
	query := `
	SELECT
		id, project_id, title, prompt, content_hash, status, owner,
		policy, progress, plan_thread_id, plan_summary,
		status_reason, result_summary, revision, created_at, updated_at
	FROM goals
	WHERE id = ?
	`
	var g Goal
	var policyStr, progressStr, createdAtStr, updatedAtStr string
	err := s.db.QueryRowContext(ctx, query, id).Scan(
		&g.ID,
		&g.ProjectID,
		&g.Title,
		&g.Prompt,
		&g.ContentHash,
		&g.Status,
		&g.Owner,
		&policyStr,
		&progressStr,
		&g.PlanThreadID,
		&g.PlanSummary,
		&g.StatusReason,
		&g.ResultSummary,
		&g.Revision,
		&createdAtStr,
		&updatedAtStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Goal{}, ErrNotFound
		}
		return Goal{}, fmt.Errorf("get goal %s: %w", id, err)
	}

	if err := json.Unmarshal([]byte(policyStr), &g.Policy); err != nil {
		return Goal{}, fmt.Errorf("decode goal policy: %w", err)
	}
	if g.Policy == nil {
		g.Policy = make(map[string]any)
	}
	if err := json.Unmarshal([]byte(progressStr), &g.Progress); err != nil {
		return Goal{}, fmt.Errorf("decode goal progress: %w", err)
	}
	if g.Progress == nil {
		g.Progress = make(map[string]any)
	}
	if g.CreatedAt, err = parseTime(createdAtStr); err != nil {
		return Goal{}, fmt.Errorf("parse goal created_at: %w", err)
	}
	if g.UpdatedAt, err = parseTime(updatedAtStr); err != nil {
		return Goal{}, fmt.Errorf("parse goal updated_at: %w", err)
	}
	return g, nil
}

// ListGoals 列出指定项目的目标，按 updated_at 降序排列。projectID 为空时列出全部。
func (s *Store) ListGoals(ctx context.Context, projectID string) ([]Goal, error) {
	query := `
	SELECT
		id, project_id, title, prompt, content_hash, status, owner,
		policy, progress, plan_thread_id, plan_summary,
		status_reason, result_summary, revision, created_at, updated_at
	FROM goals
	`
	var args []any
	if projectID != "" {
		query += " WHERE project_id = ?"
		args = append(args, projectID)
	}
	query += " ORDER BY rtrim(updated_at, 'Z') DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list goals: %w", err)
	}
	defer rows.Close()

	goals := make([]Goal, 0)
	for rows.Next() {
		var g Goal
		var policyStr, progressStr, createdAtStr, updatedAtStr string
		if err := rows.Scan(
			&g.ID,
			&g.ProjectID,
			&g.Title,
			&g.Prompt,
			&g.ContentHash,
			&g.Status,
			&g.Owner,
			&policyStr,
			&progressStr,
			&g.PlanThreadID,
			&g.PlanSummary,
			&g.StatusReason,
			&g.ResultSummary,
			&g.Revision,
			&createdAtStr,
			&updatedAtStr,
		); err != nil {
			return nil, fmt.Errorf("scan goal: %w", err)
		}
		if err := json.Unmarshal([]byte(policyStr), &g.Policy); err != nil {
			return nil, fmt.Errorf("decode goal policy: %w", err)
		}
		if g.Policy == nil {
			g.Policy = make(map[string]any)
		}
		if err := json.Unmarshal([]byte(progressStr), &g.Progress); err != nil {
			return nil, fmt.Errorf("decode goal progress: %w", err)
		}
		if g.Progress == nil {
			g.Progress = make(map[string]any)
		}
		if g.CreatedAt, err = parseTime(createdAtStr); err != nil {
			return nil, fmt.Errorf("parse goal created_at: %w", err)
		}
		if g.UpdatedAt, err = parseTime(updatedAtStr); err != nil {
			return nil, fmt.Errorf("parse goal updated_at: %w", err)
		}
		goals = append(goals, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate goals: %w", err)
	}
	return goals, nil
}

// UpdateGoal 执行 CAS 乐观并发更新。
// 更新成功时版本号自增 1 并返回新记录。受影响行数为 0 时检查是否存在：不存在返回 ErrNotFound，存在则返回 ErrRevisionConflict。
func (s *Store) UpdateGoal(ctx context.Context, g Goal, expectedRevision int) (Goal, error) {
	now := time.Now().UTC()
	g.UpdatedAt = now

	policyBytes, err := json.Marshal(g.Policy)
	if err != nil {
		return Goal{}, fmt.Errorf("encode goal policy: %w", err)
	}
	if string(policyBytes) == "null" {
		policyBytes = []byte("{}")
	}
	progressBytes, err := json.Marshal(g.Progress)
	if err != nil {
		return Goal{}, fmt.Errorf("encode goal progress: %w", err)
	}
	if string(progressBytes) == "null" {
		progressBytes = []byte("{}")
	}

	query := `
	UPDATE goals SET
		project_id = ?,
		title = ?,
		prompt = ?,
		content_hash = ?,
		status = ?,
		owner = ?,
		policy = ?,
		progress = ?,
		plan_thread_id = ?,
		plan_summary = ?,
		status_reason = ?,
		result_summary = ?,
		revision = revision + 1,
		updated_at = ?
	WHERE id = ? AND revision = ?
	RETURNING created_at
	`
	var createdAt string
	err = s.db.QueryRowContext(ctx, query,
		g.ProjectID,
		g.Title,
		g.Prompt,
		g.ContentHash,
		g.Status,
		g.Owner,
		string(policyBytes),
		string(progressBytes),
		g.PlanThreadID,
		g.PlanSummary,
		g.StatusReason,
		g.ResultSummary,
		formatTime(g.UpdatedAt),
		g.ID,
		expectedRevision,
	).Scan(&createdAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Goal{}, fmt.Errorf("update goal %s: %w", g.ID, err)
	}

	if errors.Is(err, sql.ErrNoRows) {
		var exists int
		err := s.db.QueryRowContext(ctx, "SELECT 1 FROM goals WHERE id = ?", g.ID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return Goal{}, ErrNotFound
		}
		if err != nil {
			return Goal{}, fmt.Errorf("check goal existence %s: %w", g.ID, err)
		}
		return Goal{}, ErrRevisionConflict
	}

	if g.CreatedAt, err = parseTime(createdAt); err != nil {
		return Goal{}, fmt.Errorf("parse goal created_at: %w", err)
	}
	if g.Policy == nil {
		g.Policy = make(map[string]any)
	}
	if g.Progress == nil {
		g.Progress = make(map[string]any)
	}
	g.Revision = expectedRevision + 1
	return g, nil
}

// DeleteGoal 根据 ID 删除目标。若目标不存在返回 ErrNotFound。
func (s *Store) DeleteGoal(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM goals WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete goal %s: %w", id, err)
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

// SetGoalTasks 按目标版本执行 CAS，原子替换关联及顺序，并递增目标和关联版本。
func (s *Store) SetGoalTasks(ctx context.Context, goalID string, taskIDs []string, expectedRevision int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `UPDATE goals SET revision = revision + 1, updated_at = ?
		WHERE id = ? AND revision = ?`, formatTime(time.Now().UTC()), goalID, expectedRevision)
	if err != nil {
		return fmt.Errorf("update goal tasks revision: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		var revision int
		err := tx.QueryRowContext(ctx, "SELECT revision FROM goals WHERE id = ?", goalID).Scan(&revision)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("check goal existence: %w", err)
		}
		return ErrRevisionConflict
	}

	// 保留仍在列表内的关联，让其 revision 单调递增。
	if _, err := tx.ExecContext(ctx, "UPDATE goal_tasks SET position = -1 WHERE goal_id = ?", goalID); err != nil {
		return fmt.Errorf("mark existing goal tasks: %w", err)
	}
	insertQuery := `INSERT INTO goal_tasks (goal_id, task_id, position) VALUES (?, ?, ?)
		ON CONFLICT(goal_id, task_id) DO UPDATE SET position = excluded.position, revision = revision + 1`
	seen := make(map[string]bool, len(taskIDs))
	for i, taskID := range taskIDs {
		if seen[taskID] {
			return fmt.Errorf("duplicate goal task %s", taskID)
		}
		seen[taskID] = true
		if _, err := tx.ExecContext(ctx, insertQuery, goalID, taskID, i); err != nil {
			return fmt.Errorf("insert goal task (goal=%s, task=%s, pos=%d): %w", goalID, taskID, i, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM goal_tasks WHERE goal_id = ? AND position = -1", goalID); err != nil {
		return fmt.Errorf("delete removed goal tasks: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit set goal tasks tx: %w", err)
	}
	return nil
}

// ListGoalTaskIDs 查询目标关联的所有任务 ID，按 position 升序排列。
func (s *Store) ListGoalTaskIDs(ctx context.Context, goalID string) ([]string, error) {
	query := `SELECT task_id FROM goal_tasks WHERE goal_id = ? ORDER BY position ASC`
	rows, err := s.db.QueryContext(ctx, query, goalID)
	if err != nil {
		return nil, fmt.Errorf("list goal task ids for goal %s: %w", goalID, err)
	}
	defer rows.Close()

	taskIDs := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan goal task id: %w", err)
		}
		taskIDs = append(taskIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate goal task ids: %w", err)
	}
	return taskIDs, nil
}
