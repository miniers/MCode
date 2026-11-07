package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// 记忆提议的来源取值：agent 表示会话内 agent 提议，distill 表示自动提炼，user 表示用户手写。
const (
	MemoryProposalSourceAgent   = "agent"
	MemoryProposalSourceDistill = "distill"
	MemoryProposalSourceUser    = "user"
)

// 记忆提议的审核状态取值：pending 待用户审核，accepted 已写入远端，rejected 已驳回。
const (
	MemoryProposalStatusPending  = "pending"
	MemoryProposalStatusAccepted = "accepted"
	MemoryProposalStatusRejected = "rejected"
)

// 记忆提议的写入范围：project 写入项目对应的空间，global 写入所有项目共享的全局空间。
const (
	MemoryProposalScopeProject = "project"
	MemoryProposalScopeGlobal  = "global"
)

// defaultMemoryProposalUnitType 为提议未指定类型时的记忆类型。
const defaultMemoryProposalUnitType = "fact"

// MemoryProposal 表示收件箱里的一条记忆提议：本地先落库，用户确认后才写入 Nowledge Mem。
type MemoryProposal struct {
	ID             string   `json:"id"`
	ProjectPath    string   `json:"project_path"`
	ThreadID       string   `json:"thread_id"`
	Title          string   `json:"title"`
	Content        string   `json:"content"`
	UnitType       string   `json:"unit_type"`
	Importance     float64  `json:"importance"`
	Labels         []string `json:"labels"`
	Source         string   `json:"source"`
	Status         string   `json:"status"`
	RemoteMemoryID string   `json:"remote_memory_id"`
	Error          string   `json:"error"`
	// Scope 为写入范围（MemoryProposalScope*），空值按 project 处理。
	Scope string `json:"scope"`
	// TargetMemoryID 非空表示这是对已有远端记忆的改写提议，接受时更新该记忆而不是新增。
	TargetMemoryID string    `json:"target_memory_id"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// memoryProposalColumns 为 memory_proposals 表读取时使用的列顺序，读写两侧共用。
const memoryProposalColumns = `id, project_path, thread_id, title, content, unit_type,
	importance, labels_json, source, status, remote_memory_id, error, created_at, updated_at,
	scope, target_memory_id`

// CreateMemoryProposal 新建一条记忆提议：ID 为空时生成 UUID，状态为空时置为 pending，
// 来源为空时记为 agent，创建时间与更新时间取当前时间。
func (s *Store) CreateMemoryProposal(ctx context.Context, p MemoryProposal) (MemoryProposal, error) {
	if strings.TrimSpace(p.Title) == "" {
		return MemoryProposal{}, errors.New("create memory proposal: title is empty")
	}
	labels, err := encodeMemoryProposalLabels(p.Labels)
	if err != nil {
		return MemoryProposal{}, err
	}
	now := time.Now().UTC()
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if p.UnitType == "" {
		p.UnitType = defaultMemoryProposalUnitType
	}
	if p.Source == "" {
		p.Source = MemoryProposalSourceAgent
	}
	if p.Status == "" {
		p.Status = MemoryProposalStatusPending
	}
	if p.Scope == "" {
		p.Scope = MemoryProposalScopeProject
	}
	p.CreatedAt = now
	p.UpdatedAt = now
	if p.Labels == nil {
		p.Labels = []string{}
	}

	_, err = s.db.ExecContext(ctx, `
	INSERT INTO memory_proposals (`+memoryProposalColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`, p.ID, p.ProjectPath, p.ThreadID, p.Title, p.Content, p.UnitType,
		p.Importance, labels, p.Source, p.Status, p.RemoteMemoryID, p.Error,
		formatTime(p.CreatedAt), formatTime(p.UpdatedAt), p.Scope, p.TargetMemoryID)
	if err != nil {
		return MemoryProposal{}, fmt.Errorf("create memory proposal %s: %w", p.ID, err)
	}
	return p, nil
}

// GetMemoryProposal 按 ID 读取记忆提议；不存在时返回 ErrNotFound。
func (s *Store) GetMemoryProposal(ctx context.Context, id string) (MemoryProposal, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+memoryProposalColumns+" FROM memory_proposals WHERE id = ?", id)
	return scanMemoryProposal(row)
}

// ListMemoryProposals 列出记忆提议，按创建时间倒序；projectPath 与 status 为空表示不过滤，
// limit 大于 0 时限制返回条数。
func (s *Store) ListMemoryProposals(ctx context.Context, projectPath, status string, limit int) ([]MemoryProposal, error) {
	query := "SELECT " + memoryProposalColumns + " FROM memory_proposals"
	var (
		conds []string
		args  []any
	)
	if path := strings.TrimSpace(projectPath); path != "" {
		conds = append(conds, "project_path = ?")
		args = append(args, path)
	}
	if st := strings.TrimSpace(status); st != "" {
		conds = append(conds, "status = ?")
		args = append(args, st)
	}
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += " ORDER BY created_at DESC, id DESC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list memory proposals: %w", err)
	}
	defer rows.Close()

	proposals := make([]MemoryProposal, 0)
	for rows.Next() {
		p, err := scanMemoryProposal(rows)
		if err != nil {
			return nil, err
		}
		proposals = append(proposals, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memory proposals: %w", err)
	}
	return proposals, nil
}

// UpdateMemoryProposal 更新一条记忆提议的可编辑字段与状态字段，并刷新 UpdatedAt；
// 不存在时返回 ErrNotFound。
func (s *Store) UpdateMemoryProposal(ctx context.Context, p MemoryProposal) error {
	if p.ID == "" {
		return errors.New("update memory proposal: id is empty")
	}
	labels, err := encodeMemoryProposalLabels(p.Labels)
	if err != nil {
		return err
	}
	p.UpdatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
	UPDATE memory_proposals SET
		title = ?, content = ?, unit_type = ?, importance = ?, labels_json = ?,
		source = ?, status = ?, remote_memory_id = ?, error = ?, updated_at = ?, scope = ?,
		target_memory_id = ?
	WHERE id = ?
	`, p.Title, p.Content, p.UnitType, p.Importance, labels, p.Source, p.Status,
		p.RemoteMemoryID, p.Error, formatTime(p.UpdatedAt), p.Scope, p.TargetMemoryID, p.ID)
	if err != nil {
		return fmt.Errorf("update memory proposal %s: %w", p.ID, err)
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

// DeleteMemoryProposal 按 ID 删除记忆提议；不存在时返回 ErrNotFound。
func (s *Store) DeleteMemoryProposal(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM memory_proposals WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete memory proposal %s: %w", id, err)
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

// CountPendingMemoryProposals 统计待审核的记忆提议条数；projectPath 为空表示不按项目过滤。
func (s *Store) CountPendingMemoryProposals(ctx context.Context, projectPath string) (int, error) {
	query := "SELECT COUNT(*) FROM memory_proposals WHERE status = ?"
	args := []any{MemoryProposalStatusPending}
	if path := strings.TrimSpace(projectPath); path != "" {
		query += " AND project_path = ?"
		args = append(args, path)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count pending memory proposals: %w", err)
	}
	return count, nil
}

// scanMemoryProposal 按 memoryProposalColumns 顺序读取一行记忆提议。
func scanMemoryProposal(row rowScanner) (MemoryProposal, error) {
	var (
		p         MemoryProposal
		labelsRaw string
		created   string
		updated   string
	)
	err := row.Scan(
		&p.ID, &p.ProjectPath, &p.ThreadID, &p.Title, &p.Content, &p.UnitType,
		&p.Importance, &labelsRaw, &p.Source, &p.Status, &p.RemoteMemoryID, &p.Error,
		&created, &updated, &p.Scope, &p.TargetMemoryID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return MemoryProposal{}, ErrNotFound
	}
	if err != nil {
		return MemoryProposal{}, fmt.Errorf("scan memory proposal: %w", err)
	}
	labels, err := decodeMemoryProposalLabels(labelsRaw)
	if err != nil {
		return MemoryProposal{}, err
	}
	p.Labels = labels
	if p.CreatedAt, err = parseTime(created); err != nil {
		return MemoryProposal{}, fmt.Errorf("parse memory proposal created_at: %w", err)
	}
	if p.UpdatedAt, err = parseTime(updated); err != nil {
		return MemoryProposal{}, fmt.Errorf("parse memory proposal updated_at: %w", err)
	}
	return p, nil
}

// encodeMemoryProposalLabels 把标签编码为 JSON 数组文本；nil 视为空数组。
func encodeMemoryProposalLabels(labels []string) (string, error) {
	if labels == nil {
		labels = []string{}
	}
	b, err := json.Marshal(labels)
	if err != nil {
		return "", fmt.Errorf("encode memory proposal labels: %w", err)
	}
	return string(b), nil
}

// decodeMemoryProposalLabels 解析标签 JSON 文本；空串与 "null" 都视为空数组。
func decodeMemoryProposalLabels(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return []string{}, nil
	}
	var labels []string
	if err := json.Unmarshal([]byte(raw), &labels); err != nil {
		return nil, fmt.Errorf("decode memory proposal labels: %w", err)
	}
	if labels == nil {
		labels = []string{}
	}
	return labels, nil
}
