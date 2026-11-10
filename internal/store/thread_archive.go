package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"
)

// 会话冷归档：已归档、长期没有更新的会话把全部消息打包成一个 zstd 压缩的 JSON blob，
// 存进 thread_archives 并从 messages 里删除，主库因此不再随历史会话无限增长。
//
// 不变量：一个会话的消息要么全部在 messages，要么全部在 thread_archives，不能两边都有。
//   - 读路径先看归档行，有就整段解码后在内存里按原语义分页（见 store.go 的 ListMessages）。
//   - 写路径（追加、更新、撤回、取消归档）一律在同一事务内先 hydrateThreadTx 回填，再照原逻辑写。
//   - 回填按原 id / seq 与全部字段逐条写回，已压缩的 tool / thinking 行重新压缩，不把明文复活。

const (
	// threadArchiveEncoding 是 payload 的编码标记：zstd 压缩后的 JSON 消息数组。
	threadArchiveEncoding = "zstd-json-v1"
)

// threadArchiveMaxDecodedBytes 是解压后的上限，防脏数据（压缩炸弹）撑爆内存。
var threadArchiveMaxDecodedBytes = 256 << 20

const (
	// threadArchiveDefaultMinIdle 是会话「安静」多久之后才允许归档。
	threadArchiveDefaultMinIdle = 7 * 24 * time.Hour
	// threadArchiveDefaultBatchThreads 是单轮最多归档的会话数。
	threadArchiveDefaultBatchThreads = 200
	// threadArchiveDefaultBatchPause 是会话之间的停顿，把唯一的连接让给界面。
	threadArchiveDefaultBatchPause = 50 * time.Millisecond
)

// ColdArchiveConfig 是冷归档任务的参数；零值取默认。
type ColdArchiveConfig struct {
	// Now 是注入时钟，零值取 time.Now().UTC()。
	Now time.Time
	// MinIdle 是会话最后更新到现在的静默期，默认 7 天。
	MinIdle time.Duration
	// BatchThreads 是单轮最多归档的会话数，默认 200。
	BatchThreads int
	// BatchPause 是会话之间让出连接的等待时长，默认 50ms。
	BatchPause time.Duration
}

// withDefaults 返回补齐默认值的配置副本。
func (c ColdArchiveConfig) withDefaults() ColdArchiveConfig {
	if c.Now.IsZero() {
		c.Now = time.Now().UTC()
	}
	if c.MinIdle <= 0 {
		c.MinIdle = threadArchiveDefaultMinIdle
	}
	if c.BatchThreads <= 0 {
		c.BatchThreads = threadArchiveDefaultBatchThreads
	}
	if c.BatchPause <= 0 {
		c.BatchPause = threadArchiveDefaultBatchPause
	}
	return c
}

// ColdArchiveReport 是一轮冷归档的统计结果。
type ColdArchiveReport struct {
	// ThreadsArchived 是真正归档的会话数。
	ThreadsArchived int
	// MessagesArchived 是搬进归档的消息条数。
	MessagesArchived int
	// RawBytes 是编码前 JSON 的字节数，PayloadBytes 是压缩后落库的字节数。
	RawBytes     int64
	PayloadBytes int64
}

// archivedMessage 是消息的可序列化快照：逐字段保留，时间落成定宽字符串，
// 避免依赖 time.Time 的 JSON 往返细节，回填时能与原行逐字节一致。
type archivedMessage struct {
	ID              string     `json:"id"`
	ThreadID        string     `json:"thread_id"`
	Seq             int64      `json:"seq"`
	Role            string     `json:"role"`
	Kind            string     `json:"kind"`
	Text            string     `json:"text"`
	TurnIndex       int        `json:"turn_index"`
	TurnID          string     `json:"turn_id"`
	IsFinal         bool       `json:"is_final"`
	CreatedAt       string     `json:"created_at"`
	ToolName        *string    `json:"tool_name,omitempty"`
	ToolInput       *string    `json:"tool_input,omitempty"`
	ToolStatus      *string    `json:"tool_status,omitempty"`
	ToolCompletedAt *string    `json:"tool_completed_at,omitempty"`
	ErrKind         *string    `json:"err_kind,omitempty"`
	Media           []MediaRef `json:"media,omitempty"`
}

// encodeThreadArchive 把会话消息按 seq 升序序列化成 JSON 再 zstd 压缩，返回 payload 与编码前字节数。
func encodeThreadArchive(msgs []Message) ([]byte, int64, error) {
	archived := make([]archivedMessage, 0, len(msgs))
	for _, m := range msgs {
		archived = append(archived, archivedMessage{
			ID:              m.ID,
			ThreadID:        m.ThreadID,
			Seq:             m.Seq,
			Role:            m.Role,
			Kind:            m.Kind,
			Text:            m.Text,
			TurnIndex:       m.TurnIndex,
			TurnID:          m.TurnID,
			IsFinal:         m.IsFinal,
			CreatedAt:       formatTime(m.CreatedAt),
			ToolName:        m.ToolName,
			ToolInput:       m.ToolInput,
			ToolStatus:      m.ToolStatus,
			ToolCompletedAt: m.ToolCompletedAt,
			ErrKind:         m.ErrKind,
			Media:           m.Media,
		})
	}
	raw, err := json.Marshal(archived)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal thread archive: %w", err)
	}
	enc, err := zstdMessageEncoder()
	if err != nil {
		return nil, 0, fmt.Errorf("init zstd encoder: %w", err)
	}
	return enc.EncodeAll(raw, nil), int64(len(raw)), nil
}

// decodeThreadArchive 解压并还原归档消息；条数必须与 message_count 一致。
func decodeThreadArchive(threadID string, payload []byte, messageCount int) ([]Message, error) {
	dec, err := zstd.NewReader(bytes.NewReader(payload),
		zstd.WithDecoderMaxMemory(uint64(threadArchiveMaxDecodedBytes)),
		zstd.WithDecoderConcurrency(1),
	)
	if err != nil {
		return nil, fmt.Errorf("init zstd decoder: %w", err)
	}
	defer dec.Close()
	raw, err := io.ReadAll(io.LimitReader(dec, int64(threadArchiveMaxDecodedBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("decode thread archive %s: %w", threadID, err)
	}
	if len(raw) > threadArchiveMaxDecodedBytes {
		return nil, fmt.Errorf("thread archive %s exceeds %d bytes after decompression", threadID, threadArchiveMaxDecodedBytes)
	}
	var archived []archivedMessage
	if err := json.Unmarshal(raw, &archived); err != nil {
		return nil, fmt.Errorf("unmarshal thread archive %s: %w", threadID, err)
	}
	if len(archived) != messageCount {
		return nil, fmt.Errorf("thread archive %s has %d messages, want %d", threadID, len(archived), messageCount)
	}
	msgs := make([]Message, 0, len(archived))
	for _, a := range archived {
		createdAt, err := parseTime(a.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse archived message %s created_at: %w", a.ID, err)
		}
		msgs = append(msgs, Message{
			ID:              a.ID,
			ThreadID:        a.ThreadID,
			Seq:             a.Seq,
			Role:            a.Role,
			Kind:            a.Kind,
			Text:            a.Text,
			TurnIndex:       a.TurnIndex,
			TurnID:          a.TurnID,
			IsFinal:         a.IsFinal,
			CreatedAt:       createdAt,
			ToolName:        a.ToolName,
			ToolInput:       a.ToolInput,
			ToolStatus:      a.ToolStatus,
			ToolCompletedAt: a.ToolCompletedAt,
			ErrKind:         a.ErrKind,
			Media:           a.Media,
		})
	}
	return msgs, nil
}

// archiveQueryer 是 loadThreadArchive 需要的查询能力，*sql.DB 与 *sql.Tx 都实现。
type archiveQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// loadThreadArchive 读出一个会话的归档消息；没有归档行时 found 为 false。
func loadThreadArchive(ctx context.Context, q archiveQueryer, threadID string) ([]Message, bool, error) {
	var messageCount int
	var encoding string
	var payload []byte
	err := q.QueryRowContext(ctx, `
	SELECT message_count, encoding, payload FROM thread_archives WHERE thread_id = ?
	`, threadID).Scan(&messageCount, &encoding, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("query thread archive %s: %w", threadID, err)
	}
	if encoding != threadArchiveEncoding {
		return nil, false, fmt.Errorf("thread archive %s has unknown encoding %q", threadID, encoding)
	}
	msgs, err := decodeThreadArchive(threadID, payload, messageCount)
	if err != nil {
		return nil, false, err
	}
	return msgs, true, nil
}

// archiveThreadTx 在一个事务内把一个会话的全部消息搬进归档并删除原行。
// 返回本次归档的增量统计与是否真的归档（没有消息或已有归档行时跳过）。
func (s *Store) archiveThreadTx(ctx context.Context, tx *sql.Tx, threadID string) (ColdArchiveReport, bool, error) {
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM thread_archives WHERE thread_id = ?", threadID).Scan(&exists); err == nil {
		return ColdArchiveReport{}, false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ColdArchiveReport{}, false, fmt.Errorf("check thread archive %s: %w", threadID, err)
	}

	rows, err := tx.QueryContext(ctx, "SELECT "+messageSelectCols+" FROM messages WHERE thread_id = ? ORDER BY seq", threadID)
	if err != nil {
		return ColdArchiveReport{}, false, fmt.Errorf("query messages for archive %s: %w", threadID, err)
	}
	var msgs []Message
	for rows.Next() {
		m, createdAtStr, mediaStr, err := scanMessageRow(rows)
		if err != nil {
			rows.Close()
			return ColdArchiveReport{}, false, err
		}
		if m.CreatedAt, err = parseTime(createdAtStr); err != nil {
			rows.Close()
			return ColdArchiveReport{}, false, fmt.Errorf("parse message created_at: %w", err)
		}
		if m.Media, err = decodeJSONList[MediaRef](mediaStr); err != nil {
			rows.Close()
			return ColdArchiveReport{}, false, fmt.Errorf("decode media for message %s: %w", m.ID, err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ColdArchiveReport{}, false, fmt.Errorf("iterate messages for archive %s: %w", threadID, err)
	}
	if err := rows.Close(); err != nil {
		return ColdArchiveReport{}, false, fmt.Errorf("close messages for archive %s: %w", threadID, err)
	}
	if len(msgs) == 0 {
		return ColdArchiveReport{}, false, nil
	}

	payload, rawBytes, err := encodeThreadArchive(msgs)
	if err != nil {
		return ColdArchiveReport{}, false, fmt.Errorf("encode thread archive %s: %w", threadID, err)
	}
	if _, err := tx.ExecContext(ctx, `
	INSERT INTO thread_archives (thread_id, archived_at, message_count, first_seq, last_seq, encoding, payload, raw_bytes)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, threadID, formatTime(time.Now().UTC()), len(msgs), msgs[0].Seq, msgs[len(msgs)-1].Seq,
		threadArchiveEncoding, payload, rawBytes); err != nil {
		return ColdArchiveReport{}, false, fmt.Errorf("insert thread archive %s: %w", threadID, err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM messages WHERE thread_id = ?", threadID); err != nil {
		return ColdArchiveReport{}, false, fmt.Errorf("delete archived messages %s: %w", threadID, err)
	}
	return ColdArchiveReport{
		ThreadsArchived:  1,
		MessagesArchived: len(msgs),
		RawBytes:         rawBytes,
		PayloadBytes:     int64(len(payload)),
	}, true, nil
}

// coldArchiveCandidates 返回本轮要归档的会话：已归档、MinIdle 内没有动静、没有进行中的轮次、
// 不是 swarm lead、没有排队项、没有遗留的 running 工具行，且仍有消息可搬。
func (s *Store) coldArchiveCandidates(ctx context.Context, cfg ColdArchiveConfig) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
	SELECT t.id
	FROM threads t
	WHERE t.archived = 1
	  AND t.updated_at < ?
	  AND t.is_provisional = 0
	  AND NOT EXISTS (SELECT 1 FROM turns tn
	                  WHERE tn.thread_id = t.id AND tn.status IN ('running', 'starting'))
	  AND NOT EXISTS (SELECT 1 FROM hydra_leads l WHERE l.lead_thread_id = t.id)
	  AND NOT EXISTS (SELECT 1 FROM thread_follow_ups f WHERE f.thread_id = t.id)
	  AND NOT EXISTS (SELECT 1 FROM thread_archives a WHERE a.thread_id = t.id)
	  AND EXISTS (SELECT 1 FROM messages m WHERE m.thread_id = t.id)
	  AND NOT EXISTS (SELECT 1 FROM messages m2
	                  WHERE m2.thread_id = t.id AND m2.tool_status = 'running')
	ORDER BY t.updated_at ASC
	LIMIT ?
	`, formatTime(cfg.Now.Add(-cfg.MinIdle)), cfg.BatchThreads)
	if err != nil {
		return nil, fmt.Errorf("query cold archive candidates: %w", err)
	}
	defer rows.Close()

	var threadIDs []string
	for rows.Next() {
		var threadID string
		if err := rows.Scan(&threadID); err != nil {
			return nil, fmt.Errorf("scan cold archive candidate: %w", err)
		}
		threadIDs = append(threadIDs, threadID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cold archive candidates: %w", err)
	}
	return threadIDs, nil
}

// RunColdArchive 执行一轮冷归档；一个会话一个事务，可被 ctx 中断，重复执行幂等。
func (s *Store) RunColdArchive(ctx context.Context, cfg ColdArchiveConfig) (ColdArchiveReport, error) {
	cfg = cfg.withDefaults()
	var rep ColdArchiveReport

	threadIDs, err := s.coldArchiveCandidates(ctx, cfg)
	if err != nil {
		return rep, err
	}
	for _, threadID := range threadIDs {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return rep, fmt.Errorf("begin cold archive tx for %s: %w", threadID, err)
		}
		delta, archived, err := s.archiveThreadTx(ctx, tx, threadID)
		if err != nil {
			tx.Rollback()
			return rep, err
		}
		if err := tx.Commit(); err != nil {
			return rep, fmt.Errorf("commit cold archive tx for %s: %w", threadID, err)
		}
		if archived {
			rep.ThreadsArchived += delta.ThreadsArchived
			rep.MessagesArchived += delta.MessagesArchived
			rep.RawBytes += delta.RawBytes
			rep.PayloadBytes += delta.PayloadBytes
		}
		// 会话之间把唯一连接让给界面与正在跑的轮次。
		select {
		case <-ctx.Done():
			return rep, ctx.Err()
		case <-time.After(cfg.BatchPause):
		}
	}

	log.Printf(
		"store: 冷归档一轮完成：扫描 %d 个会话，归档 %d 个 / 消息 %d 条（JSON %d -> payload %d 字节）",
		len(threadIDs), rep.ThreadsArchived, rep.MessagesArchived, rep.RawBytes, rep.PayloadBytes,
	)
	return rep, nil
}

// hydrateThreadTx 在一个事务内把归档消息回填到 messages 并删除归档行；没有归档行时直接返回。
// 回填按原 id / seq 与全部字段写回；tool / thinking 行按 message_retention.go 的同一规则重新压缩。
func (s *Store) hydrateThreadTx(ctx context.Context, tx *sql.Tx, threadID string) error {
	msgs, found, err := loadThreadArchive(ctx, tx, threadID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}

	insertQuery := `
	INSERT INTO messages (id, thread_id, seq, role, kind, text, turn_index, turn_id, is_final, created_at, media,
		tool_name, tool_input, tool_status, tool_completed_at, err_kind, text_encoding, text_z, tool_input_z)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	dictID := atomic.LoadUint32(&s.zstdDictID)
	for _, m := range msgs {
		isFinalInt := 0
		if m.IsFinal {
			isFinalInt = 1
		}
		mediaJSON, err := encodeJSONList(m.Media)
		if err != nil {
			return fmt.Errorf("encode media for message %s: %w", m.ID, err)
		}
		textVal := m.Text
		toolInputVal := derefString(m.ToolInput)
		var textZ, toolInputZ []byte
		encoding := ""
		// 与 compressMessageBatch 一致：只有 tool / thinking 行、正文够长、压完更小才压缩。
		if m.Role == "tool" || m.Kind == "thinking" {
			sourceLen := len(textVal) + len(toolInputVal)
			if sourceLen >= messageCompressMinBytes {
				z1, z2, err := encodeMessageContent(textVal, toolInputVal, dictID)
				if err != nil {
					return fmt.Errorf("encode message %s content: %w", m.ID, err)
				}
				if len(z1)+len(z2) < sourceLen {
					textVal, toolInputVal = "", ""
					textZ, toolInputZ = z1, z2
					encoding = messageEncodingZstd
				}
			}
		}
		if _, err := tx.ExecContext(ctx, insertQuery,
			m.ID,
			m.ThreadID,
			m.Seq,
			m.Role,
			m.Kind,
			textVal,
			m.TurnIndex,
			m.TurnID,
			isFinalInt,
			formatTime(m.CreatedAt),
			mediaJSON,
			derefString(m.ToolName),
			toolInputVal,
			derefString(m.ToolStatus),
			derefString(m.ToolCompletedAt),
			derefString(m.ErrKind),
			encoding,
			textZ,
			toolInputZ,
		); err != nil {
			return fmt.Errorf("insert hydrated message %s: %w", m.ID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM thread_archives WHERE thread_id = ?", threadID); err != nil {
		return fmt.Errorf("delete thread archive %s: %w", threadID, err)
	}
	return nil
}

// HydrateThread 是 hydrateThreadTx 的独立事务包装，幂等：没有归档行时什么也不做。
func (s *Store) HydrateThread(ctx context.Context, threadID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin hydrate tx for %s: %w", threadID, err)
	}
	defer tx.Rollback()
	if err := s.hydrateThreadTx(ctx, tx, threadID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit hydrate tx for %s: %w", threadID, err)
	}
	return nil
}

// pageArchivedMessages 在内存里复刻 ListMessages 的 SQL 语义：
// 取 seq < beforeSeq（beforeSeq <= 0 时不限）的最新 limit 条，按 seq 升序返回，并给出是否还有更早的消息。
func pageArchivedMessages(msgs []Message, beforeSeq int64, limit int) ([]Message, bool) {
	picked := make([]Message, 0, limit+1)
	for i := len(msgs) - 1; i >= 0 && len(picked) <= limit; i-- {
		if beforeSeq > 0 && msgs[i].Seq >= beforeSeq {
			continue
		}
		picked = append(picked, msgs[i])
	}
	hasMore := len(picked) > limit
	if hasMore {
		picked = picked[:limit]
	}
	// 翻转为 seq 升序，与 ListMessages 的返回顺序一致。
	for i, j := 0, len(picked)-1; i < j; i, j = i+1, j-1 {
		picked[i], picked[j] = picked[j], picked[i]
	}
	return picked, hasMore
}

// isThreadBodyMessage 是 threadBodyFilter（thread_search.go）的内存等价实现：
// 只保留 user / assistant 的正文消息。
func isThreadBodyMessage(m Message) bool {
	if m.Role != "user" && m.Role != "assistant" {
		return false
	}
	switch m.Kind {
	case "reasoning", "thinking", "tool", "error", "notice", "info":
		return false
	}
	return true
}

// archivedThreadBodyPage 按 ThreadMessagesPage 的列清单与语义切片：
// 只带 id/seq/role/kind/text/turn_index/turn_id/is_final/created_at，其余字段保持零值。
func archivedThreadBodyPage(threadID string, msgs []Message, offset, limit int) (int, []Message) {
	body := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if !isThreadBodyMessage(m) {
			continue
		}
		body = append(body, Message{
			ID:        m.ID,
			ThreadID:  threadID,
			Seq:       m.Seq,
			Role:      m.Role,
			Kind:      m.Kind,
			Text:      m.Text,
			TurnIndex: m.TurnIndex,
			TurnID:    m.TurnID,
			IsFinal:   m.IsFinal,
			CreatedAt: m.CreatedAt,
		})
	}
	total := len(body)
	start := offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	if start >= end {
		return total, nil
	}
	return total, body[start:end]
}
