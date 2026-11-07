package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// swarmReportsMigration 建立子代理汇报存档表：每个已结束 bee 的完整汇报总结单独落一行，
// 不随 hydra_leads 里的 bee 记录被清理或删线程而丢失；bee_id 即 bee 的线程 ID。
const swarmReportsMigration = `
	CREATE TABLE IF NOT EXISTS swarm_reports (
		bee_id TEXT PRIMARY KEY,
		lead_thread_id TEXT NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		task TEXT NOT NULL DEFAULT '',
		summary TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_swarm_reports_lead ON swarm_reports(lead_thread_id);
`

// SwarmReport 是一份存档的子代理完整汇报。
type SwarmReport struct {
	BeeID        string
	LeadThreadID string
	Name         string
	Task         string
	Summary      string
	CreatedAt    time.Time
}

// SaveSwarmReport 写入或覆盖一份汇报存档；同一 bee 重复写入时保留首次的 created_at。
func (s *Store) SaveSwarmReport(ctx context.Context, r SwarmReport) error {
	if r.BeeID == "" {
		return errors.New("save swarm report: bee id is empty")
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	query := `
	INSERT INTO swarm_reports (bee_id, lead_thread_id, name, task, summary, created_at)
	VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT(bee_id) DO UPDATE SET
		lead_thread_id = excluded.lead_thread_id,
		name = excluded.name,
		task = excluded.task,
		summary = excluded.summary;
	`
	if _, err := s.db.ExecContext(ctx, query, r.BeeID, r.LeadThreadID, r.Name, r.Task, r.Summary, formatTime(r.CreatedAt)); err != nil {
		return fmt.Errorf("save swarm report %s: %w", r.BeeID, err)
	}
	return nil
}

// GetSwarmReport 按 bee ID 读取汇报存档；不存在时返回 ErrNotFound。
func (s *Store) GetSwarmReport(ctx context.Context, beeID string) (SwarmReport, error) {
	query := `SELECT bee_id, lead_thread_id, name, task, summary, created_at FROM swarm_reports WHERE bee_id = ?`
	var r SwarmReport
	var createdAt string
	err := s.db.QueryRowContext(ctx, query, beeID).Scan(&r.BeeID, &r.LeadThreadID, &r.Name, &r.Task, &r.Summary, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SwarmReport{}, ErrNotFound
	}
	if err != nil {
		return SwarmReport{}, fmt.Errorf("get swarm report %s: %w", beeID, err)
	}
	if r.CreatedAt, err = parseTime(createdAt); err != nil {
		return SwarmReport{}, fmt.Errorf("parse swarm report created_at: %w", err)
	}
	return r, nil
}
