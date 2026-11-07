package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ThreadSearchMatch struct {
	Seq int64
	// Offset 是该消息在线程正文消息（threadBodyFilter 过滤后）中的 0 基下标，
	// 可直接作为 ThreadMessagesPage 的 offset 使用。
	Offset        int
	Role, Snippet string
}

type ThreadSearchHit struct {
	ThreadID, Title string
	MessageCount    int
	LastActivity    time.Time
	Matches         []ThreadSearchMatch
}

const threadBodyFilter = `role IN ('user', 'assistant') AND kind NOT IN ('reasoning', 'thinking', 'tool', 'error', 'notice', 'info')`

// placeholders 返回 n 个逗号分隔的 SQL 占位符，例如 n=3 时为 "?, ?, ?"。
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?, ", n-1) + "?"
}

// SearchThreadMessages searches unarchived project threads and groups matches by thread.
// projectPaths 中的任意一个路径归属的项目都会被检索；为空时返回 nil。
func (s *Store) SearchThreadMessages(ctx context.Context, projectPaths []string, query string, limit int) ([]ThreadSearchHit, error) {
	q := strings.TrimSpace(query)
	if q == "" || len(projectPaths) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	querySQL := `WITH bodies AS (
		SELECT m.thread_id, m.seq, m.role, m.text, m.created_at
		FROM messages m JOIN threads t ON t.id = m.thread_id
		JOIN projects p ON p.id = t.project_id
		WHERE p.path IN (` + placeholders(len(projectPaths)) + `) AND t.archived = 0 AND ` + threadBodyFilter + `
	), positioned AS (
		SELECT b.thread_id, b.seq, b.role, b.text, b.created_at,
		ROW_NUMBER() OVER (PARTITION BY b.thread_id ORDER BY b.seq) - 1 AS pos
		FROM bodies b
	), selected AS (
		SELECT t.id, t.title, COUNT(*) AS total, MAX(b.created_at) AS activity
		FROM positioned b JOIN threads t ON t.id = b.thread_id
		GROUP BY t.id HAVING SUM(CASE WHEN b.text LIKE ? ESCAPE '\' THEN 1 ELSE 0 END) > 0
		ORDER BY activity DESC, t.id LIMIT ?
	), matches AS (
		SELECT b.thread_id, b.seq, b.pos, b.role, b.text,
		ROW_NUMBER() OVER (PARTITION BY b.thread_id ORDER BY b.seq DESC) AS rn
		FROM positioned b WHERE b.text LIKE ? ESCAPE '\'
	)
	SELECT s.id, s.title, s.total, s.activity, m.seq, m.pos, m.role, m.text
	FROM selected s JOIN matches m ON m.thread_id = s.id AND m.rn <= 3
	ORDER BY s.activity DESC, s.id, m.rn`
	pattern := "%" + likeEscape(q) + "%"
	args := make([]any, 0, len(projectPaths)+3)
	for _, p := range projectPaths {
		args = append(args, p)
	}
	args = append(args, pattern, limit, pattern)
	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("search thread messages: %w", err)
	}
	defer rows.Close()
	var hits []ThreadSearchHit
	for rows.Next() {
		var id, title, activity, role, body string
		var count, offset int
		var seq int64
		if err := rows.Scan(&id, &title, &count, &activity, &seq, &offset, &role, &body); err != nil {
			return nil, err
		}
		if len(hits) == 0 || hits[len(hits)-1].ThreadID != id {
			at, err := parseTime(activity)
			if err != nil {
				return nil, err
			}
			hits = append(hits, ThreadSearchHit{ThreadID: id, Title: title, MessageCount: count, LastActivity: at})
		}
		hit := &hits[len(hits)-1]
		hit.Matches = append(hit.Matches, ThreadSearchMatch{Seq: seq, Offset: offset, Role: role, Snippet: messageSearchSnippet(body, q)})
	}
	return hits, rows.Err()
}

// ThreadMessagesPage returns a page of body messages guarded by project paths.
// projectPaths 为空且线程不存在时返回 ErrNotFound。
func (s *Store) ThreadMessagesPage(ctx context.Context, projectPaths []string, threadID string, offset, limit int) (string, int, []Message, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if len(projectPaths) == 0 {
		return "", 0, nil, ErrNotFound
	}
	var title string
	args := make([]any, 0, len(projectPaths)+1)
	args = append(args, threadID)
	for _, p := range projectPaths {
		args = append(args, p)
	}
	err := s.db.QueryRowContext(ctx, `SELECT t.title FROM threads t JOIN projects p ON p.id = t.project_id WHERE t.id = ? AND p.path IN (`+placeholders(len(projectPaths))+`)`, args...).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil, ErrNotFound
	}
	if err != nil {
		return "", 0, nil, err
	}
	// 冷归档会话的正文消息在归档 blob 里，解码后在内存里套用同样的过滤与分页。
	if archived, found, err := loadThreadArchive(ctx, s.db, threadID); err != nil {
		return "", 0, nil, fmt.Errorf("load thread archive %s: %w", threadID, err)
	} else if found {
		total, msgs := archivedThreadBodyPage(threadID, archived, offset, limit)
		return title, total, msgs, nil
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE thread_id = ? AND `+threadBodyFilter, threadID).Scan(&total); err != nil {
		return "", 0, nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, seq, role, kind, text, turn_index, turn_id, is_final, created_at FROM messages WHERE thread_id = ? AND `+threadBodyFilter+` ORDER BY seq LIMIT ? OFFSET ?`, threadID, limit, offset)
	if err != nil {
		return "", 0, nil, err
	}
	defer rows.Close()
	var msgs []Message
	for rows.Next() {
		m := Message{ThreadID: threadID}
		var created string
		if err := rows.Scan(&m.ID, &m.Seq, &m.Role, &m.Kind, &m.Text, &m.TurnIndex, &m.TurnID, &m.IsFinal, &created); err != nil {
			return "", 0, nil, err
		}
		m.CreatedAt, err = parseTime(created)
		if err != nil {
			return "", 0, nil, err
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return "", 0, nil, err
	}
	return title, total, msgs, nil
}
