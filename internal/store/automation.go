package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Automation 表示自动化任务配置。
type Automation struct {
	ID        string     `json:"id"`
	ProjectID *string    `json:"project_id"`
	Name      string     `json:"name"`
	Prompt    string     `json:"prompt"`
	Schedule  string     `json:"schedule"`
	Enabled   bool       `json:"enabled"`
	Status    string     `json:"status"`
	NextRunAt *time.Time `json:"next_run_at"`
	LastRunAt *time.Time `json:"last_run_at"`
	Revision  int        `json:"revision"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// AutomationRun 表示一次自动化运行记录。
type AutomationRun struct {
	ID           string  `json:"id"`
	AutomationID string  `json:"automation_id"`
	ThreadID     *string `json:"thread_id"`
	Status       string  `json:"status"`
	// Trigger 为运行来源：scheduled / catch_up / manual，空值按 scheduled 处理。
	Trigger string `json:"trigger"`
	// MissedCount 是本次补跑折叠的错过触发点数量，非补跑为 0。
	MissedCount   int        `json:"missed_count"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	ResultSummary *string    `json:"result_summary"`
	Error         *string    `json:"error"`
	ChangedFiles  *int       `json:"changed_files"`
	// ReadAt 为用户首次查看该运行记录的时间；nil 表示未读，新建记录默认未读。
	ReadAt *time.Time `json:"read_at"`
	// Archived 表示该运行记录已归档，默认列表不返回归档记录。
	Archived bool `json:"archived"`
	Revision int  `json:"revision"`
}

// automationRunColumns 是 automation_runs 的查询列，供各查询共用，保证扫描顺序一致。
const automationRunColumns = `
		id, automation_id, thread_id, status, "trigger", missed_count,
		started_at, finished_at, result_summary,
		error, changed_files, read_at, archived, revision
	`

// scanAutomationRun 按 automationRunColumns 的顺序扫描一条运行记录。
func scanAutomationRun(row rowScanner) (AutomationRun, error) {
	var r AutomationRun
	var startedAtStr string
	var finishedAtStr, readAtStr *string
	var archivedInt int
	if err := row.Scan(
		&r.ID,
		&r.AutomationID,
		&r.ThreadID,
		&r.Status,
		&r.Trigger,
		&r.MissedCount,
		&startedAtStr,
		&finishedAtStr,
		&r.ResultSummary,
		&r.Error,
		&r.ChangedFiles,
		&readAtStr,
		&archivedInt,
		&r.Revision,
	); err != nil {
		return AutomationRun{}, err
	}
	t, err := parseTime(startedAtStr)
	if err != nil {
		return AutomationRun{}, fmt.Errorf("parse automation run started_at: %w", err)
	}
	r.StartedAt = t
	if finishedAtStr != nil {
		ft, err := parseTime(*finishedAtStr)
		if err != nil {
			return AutomationRun{}, fmt.Errorf("parse automation run finished_at: %w", err)
		}
		r.FinishedAt = &ft
	}
	if readAtStr != nil {
		rt, err := parseTime(*readAtStr)
		if err != nil {
			return AutomationRun{}, fmt.Errorf("parse automation run read_at: %w", err)
		}
		r.ReadAt = &rt
	}
	r.Archived = archivedInt != 0
	// 旧数据没有 trigger 列值时按正常调度处理。
	if r.Trigger == "" {
		r.Trigger = "scheduled"
	}
	return r, nil
}

// CreateAutomation 创建新自动化配置，初始版本号置为 1。
func (s *Store) CreateAutomation(ctx context.Context, a Automation) error {
	now := time.Now().UTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	if a.UpdatedAt.IsZero() {
		a.UpdatedAt = a.CreatedAt
	}
	if a.Status == "" {
		a.Status = "active"
	}
	a.Revision = 1

	enabledInt := 0
	if a.Enabled {
		enabledInt = 1
	}

	var nextRunAtStr *string
	if a.NextRunAt != nil {
		s := formatTime(*a.NextRunAt)
		nextRunAtStr = &s
	}
	var lastRunAtStr *string
	if a.LastRunAt != nil {
		s := formatTime(*a.LastRunAt)
		lastRunAtStr = &s
	}

	query := `
	INSERT INTO automations (
		id, project_id, name, prompt, schedule,
		enabled, status, next_run_at, last_run_at,
		revision, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := s.db.ExecContext(ctx, query,
		a.ID,
		a.ProjectID,
		a.Name,
		a.Prompt,
		a.Schedule,
		enabledInt,
		a.Status,
		nextRunAtStr,
		lastRunAtStr,
		a.Revision,
		formatTime(a.CreatedAt),
		formatTime(a.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("create automation %s: %w", a.ID, err)
	}
	return nil
}

// GetAutomation 根据 ID 查询自动化配置，不存在时返回 ErrNotFound。
func (s *Store) GetAutomation(ctx context.Context, id string) (Automation, error) {
	query := `
	SELECT
		id, project_id, name, prompt, schedule,
		enabled, status, next_run_at, last_run_at,
		revision, created_at, updated_at
	FROM automations
	WHERE id = ?
	`
	var a Automation
	var enabledInt int
	var nextRunAtStr, lastRunAtStr, createdAtStr, updatedAtStr *string
	err := s.db.QueryRowContext(ctx, query, id).Scan(
		&a.ID,
		&a.ProjectID,
		&a.Name,
		&a.Prompt,
		&a.Schedule,
		&enabledInt,
		&a.Status,
		&nextRunAtStr,
		&lastRunAtStr,
		&a.Revision,
		&createdAtStr,
		&updatedAtStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Automation{}, ErrNotFound
		}
		return Automation{}, fmt.Errorf("get automation %s: %w", id, err)
	}

	a.Enabled = enabledInt != 0
	if nextRunAtStr != nil {
		t, err := parseTime(*nextRunAtStr)
		if err != nil {
			return Automation{}, fmt.Errorf("parse automation next_run_at: %w", err)
		}
		a.NextRunAt = &t
	}
	if lastRunAtStr != nil {
		t, err := parseTime(*lastRunAtStr)
		if err != nil {
			return Automation{}, fmt.Errorf("parse automation last_run_at: %w", err)
		}
		a.LastRunAt = &t
	}
	if createdAtStr != nil {
		t, err := parseTime(*createdAtStr)
		if err != nil {
			return Automation{}, fmt.Errorf("parse automation created_at: %w", err)
		}
		a.CreatedAt = t
	}
	if updatedAtStr != nil {
		t, err := parseTime(*updatedAtStr)
		if err != nil {
			return Automation{}, fmt.Errorf("parse automation updated_at: %w", err)
		}
		a.UpdatedAt = t
	}
	return a, nil
}

// ListAutomations 列出指定项目的自动化配置，按 updated_at 降序排列。projectID 为空时列出全部。
func (s *Store) ListAutomations(ctx context.Context, projectID string) ([]Automation, error) {
	query := `
	SELECT
		id, project_id, name, prompt, schedule,
		enabled, status, next_run_at, last_run_at,
		revision, created_at, updated_at
	FROM automations
	`
	var args []any
	if projectID != "" {
		query += " WHERE project_id = ?"
		args = append(args, projectID)
	}
	query += " ORDER BY rtrim(updated_at, 'Z') DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list automations: %w", err)
	}
	defer rows.Close()

	automations := make([]Automation, 0)
	for rows.Next() {
		var a Automation
		var enabledInt int
		var nextRunAtStr, lastRunAtStr, createdAtStr, updatedAtStr *string
		if err := rows.Scan(
			&a.ID,
			&a.ProjectID,
			&a.Name,
			&a.Prompt,
			&a.Schedule,
			&enabledInt,
			&a.Status,
			&nextRunAtStr,
			&lastRunAtStr,
			&a.Revision,
			&createdAtStr,
			&updatedAtStr,
		); err != nil {
			return nil, fmt.Errorf("scan automation: %w", err)
		}
		a.Enabled = enabledInt != 0
		if nextRunAtStr != nil {
			t, err := parseTime(*nextRunAtStr)
			if err != nil {
				return nil, fmt.Errorf("parse automation next_run_at: %w", err)
			}
			a.NextRunAt = &t
		}
		if lastRunAtStr != nil {
			t, err := parseTime(*lastRunAtStr)
			if err != nil {
				return nil, fmt.Errorf("parse automation last_run_at: %w", err)
			}
			a.LastRunAt = &t
		}
		if createdAtStr != nil {
			t, err := parseTime(*createdAtStr)
			if err != nil {
				return nil, fmt.Errorf("parse automation created_at: %w", err)
			}
			a.CreatedAt = t
		}
		if updatedAtStr != nil {
			t, err := parseTime(*updatedAtStr)
			if err != nil {
				return nil, fmt.Errorf("parse automation updated_at: %w", err)
			}
			a.UpdatedAt = t
		}
		automations = append(automations, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate automations: %w", err)
	}
	return automations, nil
}

// UpdateAutomation 执行 CAS 乐观并发更新。
// 更新成功时版本号自增 1 并返回新记录。受影响行数为 0 时检查是否存在：不存在返回 ErrNotFound，存在则返回 ErrRevisionConflict。
func (s *Store) UpdateAutomation(ctx context.Context, a Automation, expectedRevision int) (Automation, error) {
	now := time.Now().UTC()
	a.UpdatedAt = now

	enabledInt := 0
	if a.Enabled {
		enabledInt = 1
	}

	var nextRunAtStr *string
	if a.NextRunAt != nil {
		s := formatTime(*a.NextRunAt)
		nextRunAtStr = &s
	}
	var lastRunAtStr *string
	if a.LastRunAt != nil {
		s := formatTime(*a.LastRunAt)
		lastRunAtStr = &s
	}

	query := `
	UPDATE automations SET
		project_id = ?,
		name = ?,
		prompt = ?,
		schedule = ?,
		enabled = ?,
		status = ?,
		next_run_at = ?,
		last_run_at = ?,
		revision = revision + 1,
		updated_at = ?
	WHERE id = ? AND revision = ?
	RETURNING created_at
	`
	var createdAt string
	err := s.db.QueryRowContext(ctx, query,
		a.ProjectID,
		a.Name,
		a.Prompt,
		a.Schedule,
		enabledInt,
		a.Status,
		nextRunAtStr,
		lastRunAtStr,
		formatTime(a.UpdatedAt),
		a.ID,
		expectedRevision,
	).Scan(&createdAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Automation{}, fmt.Errorf("update automation %s: %w", a.ID, err)
	}

	if errors.Is(err, sql.ErrNoRows) {
		var exists int
		err := s.db.QueryRowContext(ctx, "SELECT 1 FROM automations WHERE id = ?", a.ID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return Automation{}, ErrNotFound
		}
		if err != nil {
			return Automation{}, fmt.Errorf("check automation existence %s: %w", a.ID, err)
		}
		return Automation{}, ErrRevisionConflict
	}

	if a.CreatedAt, err = parseTime(createdAt); err != nil {
		return Automation{}, fmt.Errorf("parse automation created_at: %w", err)
	}
	a.Revision = expectedRevision + 1
	return a, nil
}

// DeleteAutomation 根据 ID 删除自动化配置。若配置不存在返回 ErrNotFound。
func (s *Store) DeleteAutomation(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM automations WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete automation %s: %w", id, err)
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

// ListDueAutomations 查询已启用且 next_run_at <= now 的所有自动化配置，按 next_run_at 升序排列。
func (s *Store) ListDueAutomations(ctx context.Context, now time.Time) ([]Automation, error) {
	nowStr := formatTime(now)
	// 时间统一存为 UTC；去掉 Z，避免它干扰可变长度小数秒的字典序。
	query := `
	SELECT
		id, project_id, name, prompt, schedule,
		enabled, status, next_run_at, last_run_at,
		revision, created_at, updated_at
	FROM automations
	WHERE enabled = 1 AND next_run_at IS NOT NULL
		AND rtrim(next_run_at, 'Z') <= rtrim(?, 'Z')
	ORDER BY rtrim(next_run_at, 'Z') ASC
	`
	rows, err := s.db.QueryContext(ctx, query, nowStr)
	if err != nil {
		return nil, fmt.Errorf("list due automations: %w", err)
	}
	defer rows.Close()

	automations := make([]Automation, 0)
	for rows.Next() {
		var a Automation
		var enabledInt int
		var nextRunAtStr, lastRunAtStr, createdAtStr, updatedAtStr *string
		if err := rows.Scan(
			&a.ID,
			&a.ProjectID,
			&a.Name,
			&a.Prompt,
			&a.Schedule,
			&enabledInt,
			&a.Status,
			&nextRunAtStr,
			&lastRunAtStr,
			&a.Revision,
			&createdAtStr,
			&updatedAtStr,
		); err != nil {
			return nil, fmt.Errorf("scan due automation: %w", err)
		}
		a.Enabled = enabledInt != 0
		if nextRunAtStr != nil {
			t, err := parseTime(*nextRunAtStr)
			if err != nil {
				return nil, fmt.Errorf("parse due automation next_run_at: %w", err)
			}
			a.NextRunAt = &t
		}
		if lastRunAtStr != nil {
			t, err := parseTime(*lastRunAtStr)
			if err != nil {
				return nil, fmt.Errorf("parse due automation last_run_at: %w", err)
			}
			a.LastRunAt = &t
		}
		if createdAtStr != nil {
			t, err := parseTime(*createdAtStr)
			if err != nil {
				return nil, fmt.Errorf("parse due automation created_at: %w", err)
			}
			a.CreatedAt = t
		}
		if updatedAtStr != nil {
			t, err := parseTime(*updatedAtStr)
			if err != nil {
				return nil, fmt.Errorf("parse due automation updated_at: %w", err)
			}
			a.UpdatedAt = t
		}
		automations = append(automations, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due automations: %w", err)
	}
	return automations, nil
}

// CreateAutomationRun 创建自动化运行记录，初始版本号置为 1。
func (s *Store) CreateAutomationRun(ctx context.Context, r AutomationRun) error {
	now := time.Now().UTC()
	if r.StartedAt.IsZero() {
		r.StartedAt = now
	}
	if r.Status == "" {
		r.Status = "running"
	}
	if r.Trigger == "" {
		r.Trigger = "scheduled"
	}
	r.Revision = 1

	var finishedAtStr *string
	if r.FinishedAt != nil {
		s := formatTime(*r.FinishedAt)
		finishedAtStr = &s
	}
	// 零值即未读、未归档：新建运行记录默认未读，失败运行同样保持未读直到用户查看。
	var readAtStr *string
	if r.ReadAt != nil {
		s := formatTime(*r.ReadAt)
		readAtStr = &s
	}
	archivedInt := 0
	if r.Archived {
		archivedInt = 1
	}

	query := `
	INSERT INTO automation_runs (
		id, automation_id, thread_id, status, "trigger", missed_count,
		started_at, finished_at, result_summary,
		error, changed_files, read_at, archived, revision
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := s.db.ExecContext(ctx, query,
		r.ID,
		r.AutomationID,
		r.ThreadID,
		r.Status,
		r.Trigger,
		r.MissedCount,
		formatTime(r.StartedAt),
		finishedAtStr,
		r.ResultSummary,
		r.Error,
		r.ChangedFiles,
		readAtStr,
		archivedInt,
		r.Revision,
	)
	if err != nil {
		return fmt.Errorf("create automation run %s: %w", r.ID, err)
	}
	return nil
}

// GetAutomationRun 根据 ID 查询自动化运行记录，不存在时返回 ErrNotFound。
func (s *Store) GetAutomationRun(ctx context.Context, id string) (AutomationRun, error) {
	query := "SELECT" + automationRunColumns + "FROM automation_runs WHERE id = ?"
	r, err := scanAutomationRun(s.db.QueryRowContext(ctx, query, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AutomationRun{}, ErrNotFound
		}
		return AutomationRun{}, fmt.Errorf("get automation run %s: %w", id, err)
	}
	return r, nil
}

// UpdateAutomationRun 执行 CAS 乐观并发更新。
// 更新成功时版本号自增 1 并返回新记录。受影响行数为 0 时检查是否存在：不存在返回 ErrNotFound，存在则返回 ErrRevisionConflict。
func (s *Store) UpdateAutomationRun(ctx context.Context, r AutomationRun, expectedRevision int) (AutomationRun, error) {
	var finishedAtStr *string
	if r.FinishedAt != nil {
		s := formatTime(*r.FinishedAt)
		finishedAtStr = &s
	}

	// 结算更新不写 read_at / archived：已读与归档状态由专门的方法维护，互不覆盖。
	if r.Trigger == "" {
		r.Trigger = "scheduled"
	}
	query := `
	UPDATE automation_runs SET
		thread_id = ?,
		status = ?,
		"trigger" = ?,
		missed_count = ?,
		finished_at = ?,
		result_summary = ?,
		error = ?,
		changed_files = ?,
		revision = revision + 1
	WHERE id = ? AND revision = ?
	RETURNING automation_id, started_at, read_at, archived
	`
	var startedAt string
	var readAtStr *string
	var archivedInt int
	err := s.db.QueryRowContext(ctx, query,
		r.ThreadID,
		r.Status,
		r.Trigger,
		r.MissedCount,
		finishedAtStr,
		r.ResultSummary,
		r.Error,
		r.ChangedFiles,
		r.ID,
		expectedRevision,
	).Scan(&r.AutomationID, &startedAt, &readAtStr, &archivedInt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AutomationRun{}, fmt.Errorf("update automation run %s: %w", r.ID, err)
	}

	if errors.Is(err, sql.ErrNoRows) {
		var exists int
		err := s.db.QueryRowContext(ctx, "SELECT 1 FROM automation_runs WHERE id = ?", r.ID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return AutomationRun{}, ErrNotFound
		}
		if err != nil {
			return AutomationRun{}, fmt.Errorf("check automation run existence %s: %w", r.ID, err)
		}
		return AutomationRun{}, ErrRevisionConflict
	}

	if r.StartedAt, err = parseTime(startedAt); err != nil {
		return AutomationRun{}, fmt.Errorf("parse automation run started_at: %w", err)
	}
	r.ReadAt = nil
	if readAtStr != nil {
		t, perr := parseTime(*readAtStr)
		if perr != nil {
			return AutomationRun{}, fmt.Errorf("parse automation run read_at: %w", perr)
		}
		r.ReadAt = &t
	}
	r.Archived = archivedInt != 0
	r.Revision = expectedRevision + 1
	return r, nil
}

// DeleteAutomationRun 删除单次运行记录，不存在时返回 ErrNotFound。
func (s *Store) DeleteAutomationRun(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM automation_runs WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete automation run %s: %w", id, err)
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

// ListAutomationRuns 列出指定自动化的运行记录，默认不含已归档记录，按 started_at 降序排列。limit <= 0 表示不限制数量。
func (s *Store) ListAutomationRuns(ctx context.Context, automationID string, limit int) ([]AutomationRun, error) {
	return s.ListAutomationRunsFiltered(ctx, automationID, limit, false)
}

// ListAutomationRunsFiltered 列出指定自动化的运行记录；includeArchived 为 true 时一并返回已归档记录。
// 按 started_at 降序排列，limit <= 0 表示不限制数量。
func (s *Store) ListAutomationRunsFiltered(ctx context.Context, automationID string, limit int, includeArchived bool) ([]AutomationRun, error) {
	query := "SELECT" + automationRunColumns + "FROM automation_runs WHERE automation_id = ?"
	var args []any
	args = append(args, automationID)
	if !includeArchived {
		query += " AND archived = 0"
	}
	query += " ORDER BY rtrim(started_at, 'Z') DESC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list automation runs: %w", err)
	}
	defer rows.Close()

	runs := make([]AutomationRun, 0)
	for rows.Next() {
		r, err := scanAutomationRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan automation run: %w", err)
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate automation runs: %w", err)
	}
	return runs, nil
}

// ListRunningAutomationRuns 列出所有仍处于 running 状态的运行记录，不限归档与自动化启用状态。
// 服务启动时用它对账重启前丢失了内存追踪的在途运行。
func (s *Store) ListRunningAutomationRuns(ctx context.Context) ([]AutomationRun, error) {
	query := "SELECT" + automationRunColumns + "FROM automation_runs WHERE status = 'running' ORDER BY rtrim(started_at, 'Z') ASC"
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list running automation runs: %w", err)
	}
	defer rows.Close()

	runs := make([]AutomationRun, 0)
	for rows.Next() {
		r, err := scanAutomationRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan automation run: %w", err)
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate automation runs: %w", err)
	}
	return runs, nil
}

// MarkAutomationRunRead 把单条运行记录标记为已读。已读记录保留首次已读时间；记录不存在返回 ErrNotFound。
func (s *Store) MarkAutomationRunRead(ctx context.Context, id string, at time.Time) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE automation_runs SET read_at = ? WHERE id = ? AND read_at IS NULL",
		formatTime(at.UTC()), id)
	if err != nil {
		return fmt.Errorf("mark automation run read %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	// 未更新到行：要么已经读过（幂等成功），要么记录不存在。
	var exists int
	err = s.db.QueryRowContext(ctx, "SELECT 1 FROM automation_runs WHERE id = ?", id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("check automation run existence %s: %w", id, err)
	}
	return nil
}

// MarkAutomationRunsRead 把指定自动化下所有未读运行记录标记为已读，返回受影响条数。
func (s *Store) MarkAutomationRunsRead(ctx context.Context, automationID string, at time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		"UPDATE automation_runs SET read_at = ? WHERE automation_id = ? AND read_at IS NULL",
		formatTime(at.UTC()), automationID)
	if err != nil {
		return 0, fmt.Errorf("mark automation runs read %s: %w", automationID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return affected, nil
}

// SetAutomationRunArchived 归档或取消归档单条运行记录。记录不存在返回 ErrNotFound。
func (s *Store) SetAutomationRunArchived(ctx context.Context, id string, archived bool) error {
	archivedInt := 0
	if archived {
		archivedInt = 1
	}
	res, err := s.db.ExecContext(ctx,
		"UPDATE automation_runs SET archived = ? WHERE id = ?", archivedInt, id)
	if err != nil {
		return fmt.Errorf("set automation run archived %s: %w", id, err)
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

// CountUnreadAutomationRuns 统计指定自动化下未读且未归档的运行记录条数。
func (s *Store) CountUnreadAutomationRuns(ctx context.Context, automationID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM automation_runs WHERE automation_id = ? AND read_at IS NULL AND archived = 0",
		automationID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count unread automation runs %s: %w", automationID, err)
	}
	return n, nil
}

// CountUnreadAutomationRunsByProject 统计项目下每个自动化的未读运行记录数（automationID -> 未读数）。
// projectID 为空时统计全部项目。
func (s *Store) CountUnreadAutomationRunsByProject(ctx context.Context, projectID string) (map[string]int, error) {
	query := `
	SELECT r.automation_id, COUNT(*)
	FROM automation_runs r
	JOIN automations a ON a.id = r.automation_id
	WHERE r.read_at IS NULL AND r.archived = 0`
	var args []any
	if projectID != "" {
		query += " AND a.project_id = ?"
		args = append(args, projectID)
	}
	query += " GROUP BY r.automation_id"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("count unread automation runs by project: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("scan unread automation run count: %w", err)
		}
		counts[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unread automation run counts: %w", err)
	}
	return counts, nil
}
