package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// 会话消息保留任务，一轮分两个阶段：
//
//	阶段一（无损压缩，所有会话：主会话与 bee 都包括）：已经结束的 tool 消息与 thinking 消息
//	做 zstd 无损压缩，压缩字节进 text_z/tool_input_z，text_encoding 标记算法；
//	读路径（scanMessageRow）自动解压，上层拿到的永远是逐字节一致的明文。
//	阶段二（有损清理，只针对 bee）：子代理（is_helper = 1）会话结束满 30 天后，
//	删掉它的思考消息（保留最后一条）。
//
// 设计要点：
//   - 无损与有损分开：阶段一只做可逆压缩；删除思考这个不可逆操作仍然等满 30 天。
//   - 状态不确定就跳过，宁可少做事（见 beeRetentionSkipReason）。
//   - 每批一个事务：全进程 MaxOpenConns(1)，长事务会卡住整个界面。
//   - 幂等 + 可中断：候选集由状态谓词决定（text_encoding = ''、role/kind），
//     不需要进度表，重启后自然从未处理的部分继续。
//   - 增量窗口：retention.compress_last_run_at 记录上一轮开始时刻，只重扫它前 24 小时
//     以来更新过的会话；键不存在时扫全部会话。

// beeRetentionRetention 是 bee 会话结束后到可以删除思考的保留期（产品决定，不做配置项）。
const beeRetentionRetention = 30 * 24 * time.Hour

const (
	// beeRetentionSettingKey 记录上一轮任务的完成时刻，只作观测与排障用，不是进度表。
	beeRetentionSettingKey = "retention.bee_last_run_at"
	// messageCompressSettingKey 记录上一轮无损压缩的开始时刻，用来收缩下一轮的会话候选集。
	messageCompressSettingKey = "retention.compress_last_run_at"
	// 单批上限：行数与源文本字节数先到者为准；单行超过字节上限时单独成批。
	beeRetentionDefaultBatchRows  = 200
	beeRetentionDefaultBatchBytes = 8 << 20
	beeRetentionDefaultBatchPause = 50 * time.Millisecond
)

const (
	// messageCompressSettle 是消息与会话的静默期：太新的行还可能被收尾流程改写，先不动。
	messageCompressSettle = 10 * time.Minute
	// messageCompressMinBytes 是单行正文加 tool_input 的最小字节数，太短的行压缩后反而更大。
	messageCompressMinBytes = 256
	// messageCompressLookback 是增量窗口：只重扫上一轮开始前 24 小时内更新过的会话。
	messageCompressLookback = 24 * time.Hour
)

// bee 会话被跳过的原因（计入 BeeRetentionReport.Skipped）。
const (
	beeSkipNotHelper         = "not_helper"
	beeSkipRunning           = "bee_running"
	beeSkipStatusUnknown     = "bee_status_unknown"
	beeSkipInterrupted       = "bee_interrupted"
	beeSkipPendingMerge      = "pending_merge"
	beeSkipMergeConflict     = "merge_conflict"
	beeSkipWorktreePresent   = "worktree_present"
	beeSkipNotFinished       = "not_finished"
	beeSkipNoBeeRecord       = "no_bee_record"
	beeSkipThreadInterrupted = "thread_interrupted"
)

// BeeRetentionConfig 是 bee 消息保留任务的参数；零值取默认。
type BeeRetentionConfig struct {
	// Now 是注入时钟，零值取 time.Now().UTC()（测试 10 分钟静默期与 30 天边界用）。
	Now time.Time
	// Retention 是结束后的保留期，默认 beeRetentionRetention。
	Retention time.Duration
	// BatchRows 是单批行数上限，默认 200。
	BatchRows int
	// BatchBytes 是单批源文本字节上限，默认 8MB。
	BatchBytes int64
	// BatchPause 是批间让出连接的等待时长，默认 50ms。
	BatchPause time.Duration
	// PathExists 判断副本目录是否存在，nil 时使用 defaultBeeWorktreeExists；仅供测试注入。
	PathExists func(path string) bool
}

// withDefaults 返回补齐默认值的配置副本。
func (c BeeRetentionConfig) withDefaults() BeeRetentionConfig {
	if c.Now.IsZero() {
		c.Now = time.Now().UTC()
	}
	if c.Retention <= 0 {
		c.Retention = beeRetentionRetention
	}
	if c.BatchRows <= 0 {
		c.BatchRows = beeRetentionDefaultBatchRows
	}
	if c.BatchBytes <= 0 {
		c.BatchBytes = beeRetentionDefaultBatchBytes
	}
	if c.BatchPause <= 0 {
		c.BatchPause = beeRetentionDefaultBatchPause
	}
	if c.PathExists == nil {
		c.PathExists = defaultBeeWorktreeExists
	}
	return c
}

// defaultBeeWorktreeExists 判断副本目录是否仍然存在。
// 只有「确定不存在」才返回 false；权限、IO 等不确定的错误一律视为存在，继续跳过，宁可少做事。
func defaultBeeWorktreeExists(path string) bool {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false
		}
		return true
	}
	return true
}

// BeeRetentionReport 是一轮任务的统计结果。
type BeeRetentionReport struct {
	// ThreadsScanned / ThreadsEligible 统计阶段二（bee 有损清理）扫描与可处理的会话数。
	ThreadsScanned  int
	ThreadsEligible int
	// ThreadsCompressed 是阶段一里真正压到内容的会话数。
	ThreadsCompressed int
	// MessagesCompressed / BytesBefore / BytesAfter 统计阶段一的压缩量与字节数。
	MessagesCompressed int
	MessagesDeleted    int
	BytesBefore        int64
	BytesAfter         int64
	// Skipped 是阶段二跳过原因 -> 次数。
	Skipped map[string]int
}

// beeRetentionState 是 retention 判定需要的 Bee 字段子集，JSON tag 必须与 internal/swarm.Bee 一致。
// store 不能 import internal/swarm（internal/swarm 反过来依赖 store），所以这里只留判定字段。
type beeRetentionState struct {
	ThreadID      string     `json:"threadId"`
	Status        string     `json:"status"`
	Interrupted   bool       `json:"interrupted"`
	FinishedAt    *time.Time `json:"finishedAt"`
	WorktreePath  string     `json:"worktreePath"`
	PendingMerge  bool       `json:"pendingMerge"`
	MergeError    string     `json:"mergeError"`
	ConflictFiles []string   `json:"conflictFiles"`
}

// beeRetentionCandidate 是一个通过候选 SQL 的 bee 会话，外加判定需要的外层字段。
type beeRetentionCandidate struct {
	ThreadID   string
	LastStatus string
	IsHelper   int
}

// RunBeeMessageRetention 执行一轮消息保留（无损压缩 + bee 有损清理）；可被 ctx 中断，重复执行幂等。
func (s *Store) RunBeeMessageRetention(ctx context.Context, cfg BeeRetentionConfig) (BeeRetentionReport, error) {
	cfg = cfg.withDefaults()
	rep := BeeRetentionReport{Skipped: map[string]int{}}

	// 阶段一：所有会话（主会话与 bee）的 tool / thinking 消息，结束后做无损压缩。
	if err := s.compressSettledMessages(ctx, cfg, &rep); err != nil {
		return rep, err
	}

	// 阶段二：bee 会话结束满 30 天后删除思考消息（有损）。
	if err := s.deleteBeeThinking(ctx, cfg, &rep); err != nil {
		return rep, err
	}

	if err := ctx.Err(); err != nil {
		return rep, err
	}
	// 只有整轮完整跑完才推进增量窗口；中途被取消时下一轮重扫，靠 text_encoding = '' 保证幂等。
	if err := s.SetSetting(ctx, messageCompressSettingKey, formatTime(cfg.Now)); err != nil {
		return rep, err
	}
	if err := s.SetSetting(ctx, beeRetentionSettingKey, formatTime(cfg.Now)); err != nil {
		return rep, err
	}
	s.logBeeRetentionReport(ctx, rep)
	return rep, nil
}

// deleteBeeThinking 是阶段二：只处理结束满保留期的 bee 会话，删除其中的思考消息。
func (s *Store) deleteBeeThinking(ctx context.Context, cfg BeeRetentionConfig, rep *BeeRetentionReport) error {
	cutoff := cfg.Now.Add(-cfg.Retention)
	states, err := s.beeRetentionStates(ctx)
	if err != nil {
		return err
	}
	candidates, err := s.beeRetentionCandidates(ctx, cutoff)
	if err != nil {
		return err
	}
	rep.ThreadsScanned = len(candidates)

	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		// 主会话的思考消息一条都不能删：候选 SQL 已带 is_helper = 1，这里再兜一层。
		if c.IsHelper != 1 {
			rep.Skipped[beeSkipNotHelper]++
			continue
		}
		st, found := states[c.ThreadID]
		if reason := beeRetentionSkipReason(st, found, c.LastStatus, cutoff, cfg.PathExists); reason != "" {
			rep.Skipped[reason]++
			continue
		}
		rep.ThreadsEligible++
		deleted, err := s.deleteThreadThinking(ctx, c.ThreadID)
		if err != nil {
			return err
		}
		rep.MessagesDeleted += int(deleted)
	}
	return nil
}

// logBeeRetentionReport 打一行任务统计；同时附 freelist 页数与总页数，便于确认空间是否真的被回收。
func (s *Store) logBeeRetentionReport(ctx context.Context, rep BeeRetentionReport) {
	var freelist, pageCount int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA freelist_count;").Scan(&freelist); err != nil {
		freelist = -1
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA page_count;").Scan(&pageCount); err != nil {
		pageCount = -1
	}
	log.Printf(
		"store: 消息保留一轮完成：无损压缩会话 %d 个、消息 %d 条（%d -> %d 字节），bee 清理扫描 %d 个（处理 %d 个）、删除思考 %d 条，跳过 %v，freelist %d 页 / 共 %d 页",
		rep.ThreadsCompressed, rep.MessagesCompressed, rep.BytesBefore, rep.BytesAfter,
		rep.ThreadsScanned, rep.ThreadsEligible, rep.MessagesDeleted, rep.Skipped, freelist, pageCount,
	)
}

// beeRetentionCandidates 返回满足候选条件的 bee 会话。
// 只做「能证明它停了」的粗筛，状态是否终态、副本是否回收等交给 beeRetentionSkipReason 判定。
func (s *Store) beeRetentionCandidates(ctx context.Context, cutoff time.Time) ([]beeRetentionCandidate, error) {
	cutoffStr := formatTime(cutoff)
	rows, err := s.db.QueryContext(ctx, `
	SELECT t.id, COALESCE(t.last_status, ''), t.is_helper
	FROM threads t
	WHERE t.is_helper = 1
	  AND t.is_provisional = 0
	  AND t.updated_at < ?
	  AND NOT EXISTS (SELECT 1 FROM turns tn
	                  WHERE tn.thread_id = t.id AND tn.status IN ('running', 'starting'))
	  AND NOT EXISTS (SELECT 1 FROM messages m2
	                  WHERE m2.thread_id = t.id AND m2.created_at >= ?)
	  AND NOT EXISTS (SELECT 1 FROM hydra_leads l WHERE l.lead_thread_id = t.id)
	  AND NOT EXISTS (SELECT 1 FROM thread_archives a WHERE a.thread_id = t.id)
	ORDER BY t.updated_at
	`, cutoffStr, cutoffStr)
	if err != nil {
		return nil, fmt.Errorf("query bee retention candidates: %w", err)
	}
	defer rows.Close()

	var candidates []beeRetentionCandidate
	for rows.Next() {
		var c beeRetentionCandidate
		if err := rows.Scan(&c.ThreadID, &c.LastStatus, &c.IsHelper); err != nil {
			return nil, fmt.Errorf("scan bee retention candidate: %w", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate bee retention candidates: %w", err)
	}
	return candidates, nil
}

// beeRetentionStates 读 hydra_leads.heads_json，返回 threadID -> 判定快照。
// 表不存在或某行 JSON 脏时只跳过该行：不确定就不动手。
func (s *Store) beeRetentionStates(ctx context.Context) (map[string]beeRetentionState, error) {
	states := make(map[string]beeRetentionState)

	var tableCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'hydra_leads';").Scan(&tableCount); err != nil {
		return nil, fmt.Errorf("check hydra_leads table: %w", err)
	}
	if tableCount == 0 {
		return states, nil
	}

	rows, err := s.db.QueryContext(ctx, "SELECT lead_thread_id, heads_json FROM hydra_leads;")
	if err != nil {
		return nil, fmt.Errorf("query hydra_leads heads: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var leadID, headsJSON string
		if err := rows.Scan(&leadID, &headsJSON); err != nil {
			return nil, fmt.Errorf("scan hydra_leads heads: %w", err)
		}
		headsJSON = strings.TrimSpace(headsJSON)
		if headsJSON == "" || headsJSON == "[]" {
			continue
		}
		var bees []beeRetentionState
		if err := json.Unmarshal([]byte(headsJSON), &bees); err != nil {
			log.Printf("store: bee 消息保留跳过脏 heads_json（lead %s）: %v", leadID, err)
			continue
		}
		for _, b := range bees {
			if b.ThreadID == "" {
				continue
			}
			states[b.ThreadID] = b
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hydra_leads heads: %w", err)
	}
	return states, nil
}

// beeRetentionSkipReason 返回跳过原因（空串表示可以处理）。
// 判定表见方案第 5.3 节：任何不确定的情况都跳过。
func beeRetentionSkipReason(st beeRetentionState, found bool, lastStatus string, cutoff time.Time, pathExists func(string) bool) string {
	if pathExists == nil {
		pathExists = defaultBeeWorktreeExists
	}
	if !found {
		// 找不到权威状态时只信强证据：会话已结束且长时间没有动静。
		if lastStatus == "interrupted" {
			return beeSkipThreadInterrupted
		}
		if lastStatus != "completed" {
			return beeSkipNoBeeRecord
		}
		return ""
	}

	switch st.Status {
	case "running":
		return beeSkipRunning
	case "completed", "failed", "stopped":
		// 终态，继续判定。
	default:
		return beeSkipStatusUnknown
	}
	if st.Interrupted {
		return beeSkipInterrupted
	}
	if st.PendingMerge {
		return beeSkipPendingMerge
	}
	if st.MergeError != "" || len(st.ConflictFiles) > 0 {
		return beeSkipMergeConflict
	}
	if st.WorktreePath != "" && pathExists(st.WorktreePath) {
		return beeSkipWorktreePresent
	}
	if st.FinishedAt == nil {
		return beeSkipNotFinished
	}
	if st.FinishedAt.After(cutoff) {
		return beeSkipNotFinished
	}
	// 宿主重启遗留的 interrupted 状态：等用户决定续跑还是放弃。
	if lastStatus == "interrupted" {
		return beeSkipThreadInterrupted
	}
	return ""
}

// compressSettledMessages 是阶段一：找出满足条件的会话，逐个会话做无损压缩。
func (s *Store) compressSettledMessages(ctx context.Context, cfg BeeRetentionConfig, rep *BeeRetentionReport) error {
	// 先确保有可用的 zstd 字典；训练失败不影响压缩，本轮继续用无字典编码器。
	if err := s.ensureMessageDict(ctx); err != nil {
		log.Printf("store: 准备 zstd 字典失败，本轮继续无字典压缩: %v", err)
	}
	since, err := s.messageCompressSince(ctx)
	if err != nil {
		return err
	}
	threadIDs, err := s.messageCompressCandidates(ctx, cfg.Now, since)
	if err != nil {
		return err
	}
	for _, threadID := range threadIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		compressed, err := s.compressThreadMessages(ctx, threadID, cfg, rep)
		if err != nil {
			return err
		}
		if compressed {
			rep.ThreadsCompressed++
		}
	}
	return nil
}

// messageCompressSince 读增量窗口起点：上一轮开始前 24 小时的时刻（含边界）。
// 没有键或值不可解析时返回空串，表示本轮扫全部会话（宁多扫不漏压）。
func (s *Store) messageCompressSince(ctx context.Context) (string, error) {
	value, ok, err := s.GetSetting(ctx, messageCompressSettingKey)
	if err != nil || !ok {
		return "", err
	}
	last, err := parseTime(value)
	if err != nil {
		log.Printf("store: 压缩增量窗口无法解析（%q），本轮扫全部会话: %v", value, err)
		return "", nil
	}
	return formatTime(last.Add(-messageCompressLookback)), nil
}

// messageCompressCandidates 返回本轮要扫的会话：非临时会话、最近 10 分钟没有动静、没有进行中的轮次，
// 且仍有无损压缩候选行（text_encoding 为空的 tool 消息或 thinking 消息）。
// since 非空时额外要求 updated_at 不早于该时刻。
func (s *Store) messageCompressCandidates(ctx context.Context, now time.Time, since string) ([]string, error) {
	query := `
	SELECT t.id
	FROM threads t
	WHERE t.is_provisional = 0
	  AND t.updated_at < ?
	  AND NOT EXISTS (SELECT 1 FROM turns tn
	                  WHERE tn.thread_id = t.id AND tn.status IN ('running', 'starting'))
	  AND EXISTS (SELECT 1 FROM messages m
	              WHERE m.thread_id = t.id AND m.text_encoding = ''
	                AND (m.role = 'tool' OR m.kind = 'thinking'))
	`
	args := []any{formatTime(now.Add(-messageCompressSettle))}
	if since != "" {
		query += "  AND t.updated_at >= ?\n"
		args = append(args, since)
	}
	query += "ORDER BY t.updated_at"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query message compress candidates: %w", err)
	}
	defer rows.Close()

	var threadIDs []string
	for rows.Next() {
		var threadID string
		if err := rows.Scan(&threadID); err != nil {
			return nil, fmt.Errorf("scan message compress candidate: %w", err)
		}
		threadIDs = append(threadIDs, threadID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate message compress candidates: %w", err)
	}
	return threadIDs, nil
}

// compressThreadMessages 按 seq 做 keyset 分页压缩一个会话，直到取不到新行为止。
// 返回本会话是否真的压到了行。
func (s *Store) compressThreadMessages(ctx context.Context, threadID string, cfg BeeRetentionConfig, rep *BeeRetentionReport) (bool, error) {
	var afterSeq int64
	compressed := false
	for {
		if err := ctx.Err(); err != nil {
			return compressed, err
		}
		scanned, lastSeq, processed, err := s.compressMessageBatch(ctx, threadID, afterSeq, cfg, rep)
		if err != nil {
			return compressed, err
		}
		if scanned == 0 {
			return compressed, nil
		}
		if processed > 0 {
			compressed = true
		}
		// 即使本批一条都没压（太新、太短、压完不更小），也要推进 seq，避免重复扫同一批。
		afterSeq = lastSeq
		// 批间把唯一的连接让给界面与正在跑的轮次。
		select {
		case <-ctx.Done():
			return compressed, ctx.Err()
		case <-time.After(cfg.BatchPause):
		}
	}
}

// pendingMessage 是一条等待压缩的消息的明文快照。
type pendingMessage struct {
	id        string
	seq       int64
	text      string
	toolInput string
	createdAt string
}

// compressMessageBatch 处理一个会话的一批候选消息（一个事务），
// 返回本批读到的行数、最后一行的 seq 与本批实际压缩的行数。
func (s *Store) compressMessageBatch(ctx context.Context, threadID string, afterSeq int64, cfg BeeRetentionConfig, rep *BeeRetentionReport) (int, int64, int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("begin message compress tx: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
	SELECT id, seq, text, tool_input, created_at FROM messages
	WHERE thread_id = ? AND seq > ? AND text_encoding = ''
	  AND (role = 'tool' OR kind = 'thinking')
	ORDER BY seq
	LIMIT ?
	`, threadID, afterSeq, cfg.BatchRows)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("query compressible messages: %w", err)
	}

	items := make([]pendingMessage, 0, cfg.BatchRows)
	var sourceBytes int64
	for rows.Next() {
		var it pendingMessage
		if err := rows.Scan(&it.id, &it.seq, &it.text, &it.toolInput, &it.createdAt); err != nil {
			rows.Close()
			return 0, 0, 0, fmt.Errorf("scan compressible message: %w", err)
		}
		items = append(items, it)
		sourceBytes += int64(len(it.text)) + int64(len(it.toolInput))
		// 达到单批字节上限就停：已读出的行先处理，剩下的下一批继续。
		if sourceBytes >= cfg.BatchBytes {
			break
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, 0, fmt.Errorf("iterate compressible messages: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, 0, fmt.Errorf("close compressible message rows: %w", err)
	}
	if len(items) == 0 {
		return 0, 0, 0, nil
	}

	settleCutoff := cfg.Now.Add(-messageCompressSettle)
	lastSeq := items[len(items)-1].seq
	processed := 0
	var beforeBytes, afterBytes int64
	for _, it := range items {
		// 刚写下的行可能还会被 UpdateMessage / 崩溃收尾改写，静默期内先不动。
		createdAt, err := parseTime(it.createdAt)
		if err != nil {
			log.Printf("store: 消息 %s 的 created_at 无法解析，跳过压缩: %v", it.id, err)
			continue
		}
		if !createdAt.Before(settleCutoff) {
			continue
		}
		// 太短的行压完反而更大，保持明文。
		sourceLen := len(it.text) + len(it.toolInput)
		if sourceLen < messageCompressMinBytes {
			continue
		}
		textZ, toolInputZ, err := encodeMessageContent(it.text, it.toolInput, atomic.LoadUint32(&s.zstdDictID))
		if err != nil {
			return 0, 0, 0, fmt.Errorf("encode message %s content: %w", it.id, err)
		}
		// 压完不比原文小就不压，免得白花一次写入。
		if len(textZ)+len(toolInputZ) >= sourceLen {
			continue
		}
		// WHERE text_encoding = '' 保证并发或重复执行时不会覆盖别人写的新值。
		res, err := tx.ExecContext(ctx, `
		UPDATE messages SET text = '', tool_input = '', text_z = ?, tool_input_z = ?, text_encoding = ?
		WHERE id = ? AND text_encoding = ''
		`, textZ, toolInputZ, messageEncodingZstd, it.id)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("compress message %s: %w", it.id, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return 0, 0, 0, err
		}
		if affected == 0 {
			continue
		}
		processed++
		beforeBytes += int64(sourceLen)
		afterBytes += int64(len(textZ)) + int64(len(toolInputZ))
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, 0, fmt.Errorf("commit message compress tx: %w", err)
	}
	if rep != nil {
		rep.MessagesCompressed += processed
		rep.BytesBefore += beforeBytes
		rep.BytesAfter += afterBytes
	}
	return len(items), lastSeq, processed, nil
}

// deleteThreadThinking 删除一个会话的思考消息（保留最后一条），返回删除行数。
// 保留最后一条是为了避免 AppendMessage 复用被删掉的 seq（它取 COALESCE(MAX(seq), 0) + 1）。
func (s *Store) deleteThreadThinking(ctx context.Context, threadID string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
	DELETE FROM messages
	WHERE thread_id = ?
	  AND kind = 'thinking'
	  AND seq < (SELECT COALESCE(MAX(seq), 0) FROM messages WHERE thread_id = ?)
	`, threadID, threadID)
	if err != nil {
		return 0, fmt.Errorf("delete bee thinking messages: %w", err)
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return deleted, nil
}
