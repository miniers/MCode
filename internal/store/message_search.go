package store

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// 消息正文搜索：跨所有未归档会话按关键字查用户与助手的正文，
// 供命令面板（Cmd+K）列出「消息」类结果并跳回原会话。

// MessageSearchHit 是一条消息正文搜索命中。
type MessageSearchHit struct {
	ThreadID    string
	ThreadTitle string
	ProjectID   string
	MessageID   string
	Role        string
	Snippet     string // 命中位置附近的片段，换行已换成空格，被截断的一端带省略号
	Seq         int64
	CreatedAt   time.Time
}

const (
	// messageSearchDefaultLimit 是没传 limit（或 limit <= 0）时的返回条数。
	messageSearchDefaultLimit = 30
	// messageSearchMaxLimit 是单次搜索返回条数的上限，避免命令面板一次拉回太多正文。
	messageSearchMaxLimit = 100
)

// likeEscape 把查询串里的 LIKE 通配符（\ % _）转义，配合 ESCAPE '\' 使用，
// 保证用户输入的 % 与 _ 按字面匹配。
func likeEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\\' || r == '%' || r == '_' {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// SearchMessages 跨所有会话搜索消息正文。
// query 先去首尾空白，为空时返回 nil；limit 缺省 30，夹取到 1..100。
// 只看未归档会话里用户与助手的正文，思考块、工具输出、错误与提示类消息不参与。
func (s *Store) SearchMessages(ctx context.Context, query string, limit int) ([]MessageSearchHit, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = messageSearchDefaultLimit
	}
	if limit > messageSearchMaxLimit {
		limit = messageSearchMaxLimit
	}

	const sqlQuery = `
	SELECT m.id, m.thread_id, t.title, t.project_id, m.role, m.seq, m.text, m.created_at
	FROM messages m
	JOIN threads t ON t.id = m.thread_id
	WHERE t.archived = 0
	  AND m.role IN ('user', 'assistant')
	  AND m.kind NOT IN ('reasoning', 'thinking', 'tool', 'error', 'notice', 'info')
	  AND m.text LIKE ? ESCAPE '\'
	ORDER BY m.created_at DESC
	LIMIT ?
	`

	rows, err := s.db.QueryContext(ctx, sqlQuery, "%"+likeEscape(q)+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("query message search: %w", err)
	}
	defer rows.Close()

	var hits []MessageSearchHit
	for rows.Next() {
		var hit MessageSearchHit
		var text, createdAtStr string
		if err := rows.Scan(
			&hit.MessageID,
			&hit.ThreadID,
			&hit.ThreadTitle,
			&hit.ProjectID,
			&hit.Role,
			&hit.Seq,
			&text,
			&createdAtStr,
		); err != nil {
			return nil, fmt.Errorf("scan message search hit: %w", err)
		}
		if hit.CreatedAt, err = parseTime(createdAtStr); err != nil {
			return nil, fmt.Errorf("parse message search created_at: %w", err)
		}
		hit.Snippet = messageSearchSnippet(text, q)
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate message search: %w", err)
	}

	return hits, nil
}

// messageSearchSnippetRange 计算命中片段在当前正文里的起止 rune 下标。
// 找不到命中（理论上不该发生，SQL 已经匹配过）时退化为正文开头。
func messageSearchSnippetRange(text, query string) (start, end int) {
	runes := []rune(text)
	if len(runes) == 0 {
		return 0, 0
	}

	needle := make([]rune, 0, len(query))
	for _, r := range strings.TrimSpace(query) {
		// 逐字符 unicode.ToLower 保持 rune 一一对应，避免 strings.ToLower 改变长度后下标错位。
		needle = append(needle, unicode.ToLower(r))
	}

	idx := -1
	if len(needle) > 0 {
		folded := make([]rune, len(runes))
		for i, r := range runes {
			folded[i] = unicode.ToLower(r)
		}
		idx = indexRunes(folded, needle)
	}

	if idx < 0 {
		end = len(runes)
		if end > 80 {
			end = 80
		}
		return 0, end
	}

	start = idx - 20
	if start < 0 {
		start = 0
	}
	end = idx + len(needle) + 60
	if end > len(runes) {
		end = len(runes)
	}
	return start, end
}

// indexRunes 在 haystack 里找首次出现的 needle（均为已折叠大小写的 rune 序列），
// 找不到返回 -1。
func indexRunes(haystack, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return -1
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// messageSearchSnippet 截出命中位置附近的片段：命中前 20 个 rune、命中后 60 个 rune。
// 换行与制表符换成空格并把连续空白压成一个，被截断的一端补「…」。
func messageSearchSnippet(text, query string) string {
	runes := []rune(text)
	start, end := messageSearchSnippetRange(text, query)

	parts := make([]string, 0, 3)
	if start > 0 {
		parts = append(parts, "…")
	}
	parts = append(parts, collapseSearchWhitespace(string(runes[start:end])))
	if end < len(runes) {
		parts = append(parts, "…")
	}
	return strings.Join(parts, "")
}

// collapseSearchWhitespace 把换行、制表符等空白换成单个空格并去掉首尾空白。
func collapseSearchWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
