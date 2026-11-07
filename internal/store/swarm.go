package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SwarmLeadRecord 记录 Swarm 主会话的持久化状态。
type SwarmLeadRecord struct {
	LeadThreadID string
	Enabled      bool
	PlansOnly    *bool
	ProjectID    string
	LeadWorkDir  string
	NextIndex    int
	// DelegationRounds 已完成的委派轮次计数；RequestStartedAt 为当前请求开始时间（零值表示未开始）。
	DelegationRounds int
	RequestStartedAt time.Time
	BeesJSON         string
	// QueueJSON 保存重启前仍在排队的条目；空串写 '[]'。
	QueueJSON string
	UpdatedAt time.Time
}

// SaveSwarmLead 插入或覆盖更新 Swarm 主会话记录；BeesJSON 与 QueueJSON 为空时写入 '[]'。
func (s *Store) SaveSwarmLead(ctx context.Context, rec SwarmLeadRecord) error {
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = time.Now().UTC()
	}
	if rec.BeesJSON == "" {
		rec.BeesJSON = "[]"
	}
	if rec.QueueJSON == "" {
		rec.QueueJSON = "[]"
	}
	enabledInt := 0
	if rec.Enabled {
		enabledInt = 1
	}
	var plansOnlyVal any
	if rec.PlansOnly != nil {
		if *rec.PlansOnly {
			plansOnlyVal = 1
		} else {
			plansOnlyVal = 0
		}
	}
	var requestStartedAtVal any
	if !rec.RequestStartedAt.IsZero() {
		requestStartedAtVal = formatTime(rec.RequestStartedAt)
	}

	query := `
	INSERT INTO hydra_leads (lead_thread_id, enabled, plans_only, project_id, lead_work_dir, next_index, delegation_rounds, request_started_at, heads_json, queue_json, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(lead_thread_id) DO UPDATE SET
		enabled = excluded.enabled,
		plans_only = excluded.plans_only,
		project_id = excluded.project_id,
		lead_work_dir = excluded.lead_work_dir,
		next_index = excluded.next_index,
		delegation_rounds = excluded.delegation_rounds,
		request_started_at = excluded.request_started_at,
		heads_json = excluded.heads_json,
		queue_json = excluded.queue_json,
		updated_at = excluded.updated_at;
	`
	_, err := s.db.ExecContext(ctx, query,
		rec.LeadThreadID,
		enabledInt,
		plansOnlyVal,
		rec.ProjectID,
		rec.LeadWorkDir,
		rec.NextIndex,
		rec.DelegationRounds,
		requestStartedAtVal,
		rec.BeesJSON,
		rec.QueueJSON,
		formatTime(rec.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("save swarm lead %s: %w", rec.LeadThreadID, err)
	}
	return nil
}

// ListSwarmLeads 查询所有 Swarm 主会话记录，按 updated_at 倒序排列。
func (s *Store) ListSwarmLeads(ctx context.Context) ([]SwarmLeadRecord, error) {
	query := `
	SELECT lead_thread_id, enabled, plans_only, project_id, lead_work_dir, next_index, delegation_rounds, request_started_at, heads_json, queue_json, updated_at
	FROM hydra_leads
	ORDER BY updated_at DESC
	`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list swarm leads: %w", err)
	}
	defer rows.Close()

	records := make([]SwarmLeadRecord, 0)
	for rows.Next() {
		var rec SwarmLeadRecord
		var enabledInt int
		var plansOnlyVal sql.NullInt64
		var requestStartedAtStr sql.NullString
		var updatedAtStr string
		if err := rows.Scan(
			&rec.LeadThreadID,
			&enabledInt,
			&plansOnlyVal,
			&rec.ProjectID,
			&rec.LeadWorkDir,
			&rec.NextIndex,
			&rec.DelegationRounds,
			&requestStartedAtStr,
			&rec.BeesJSON,
			&rec.QueueJSON,
			&updatedAtStr,
		); err != nil {
			return nil, fmt.Errorf("scan swarm lead: %w", err)
		}
		if rec.UpdatedAt, err = parseTime(updatedAtStr); err != nil {
			return nil, fmt.Errorf("parse swarm lead updated_at: %w", err)
		}
		rec.Enabled = enabledInt != 0
		if plansOnlyVal.Valid {
			b := plansOnlyVal.Int64 != 0
			rec.PlansOnly = &b
		}
		if requestStartedAtStr.Valid && requestStartedAtStr.String != "" {
			if rec.RequestStartedAt, err = parseTime(requestStartedAtStr.String); err != nil {
				return nil, fmt.Errorf("parse swarm lead request_started_at: %w", err)
			}
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate swarm leads: %w", err)
	}
	return records, nil
}

// DeleteSwarmLead 删除指定主会话的 Swarm 记录。
func (s *Store) DeleteSwarmLead(ctx context.Context, leadThreadID string) error {
	query := `DELETE FROM hydra_leads WHERE lead_thread_id = ?`
	if _, err := s.db.ExecContext(ctx, query, leadThreadID); err != nil {
		return fmt.Errorf("delete swarm lead %s: %w", leadThreadID, err)
	}
	return nil
}
