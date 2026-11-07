package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// retentionTestNow 是保留任务测试的固定时钟，30 天边界都由它推算，不依赖真实时间。
var retentionTestNow = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

// retentionTestBee 是写进 heads_json 的 bee 状态快照，字段名必须与 internal/swarm.Bee 一致。
type retentionTestBee struct {
	ThreadID      string     `json:"threadId"`
	Status        string     `json:"status"`
	Interrupted   bool       `json:"interrupted,omitempty"`
	FinishedAt    *time.Time `json:"finishedAt"`
	WorktreePath  string     `json:"worktreePath,omitempty"`
	PendingMerge  bool       `json:"pendingMerge,omitempty"`
	MergeError    string     `json:"mergeError,omitempty"`
	ConflictFiles []string   `json:"conflictFiles,omitempty"`
}

// retentionFailure 是测试准备阶段失败时的辅助函数。
func retentionFailure(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Fatalf(format, args...)
}

// newRetentionTestStore 打开一个临时库，建好一个项目。
func newRetentionTestStore(t *testing.T, dbName string) (*Store, string) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), dbName))
	if err != nil {
		retentionFailure(t, "Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.UpsertProject(context.Background(), Project{
		ID: "p1", Name: "P1", Path: "/p1", CreatedAt: retentionTestNow, UpdatedAt: retentionTestNow,
	}); err != nil {
		retentionFailure(t, "UpsertProject 失败: %v", err)
	}
	return s, "p1"
}

// createRetentionThread 建一个会话；isHelper 为真时标记成 bee（子代理）会话。
func createRetentionThread(t *testing.T, s *Store, projectID, id string, isHelper bool) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateThread(ctx, Thread{
		ID: id, ProjectID: projectID, Title: "会话 " + id, Provider: "openai",
		CreatedAt: retentionTestNow, UpdatedAt: retentionTestNow,
	}); err != nil {
		retentionFailure(t, "CreateThread %s 失败: %v", id, err)
	}
	if isHelper {
		if err := s.UpdateThreadRelation(ctx, id, "lead", true); err != nil {
			retentionFailure(t, "UpdateThreadRelation %s 失败: %v", id, err)
		}
	}
}

// appendRetentionMessage 追加一条消息；createdAt 决定 threads.updated_at，因此直接用它控制「结束时间」。
func appendRetentionMessage(t *testing.T, s *Store, id, threadID, role, kind, text string, toolInput *string, createdAt time.Time) {
	t.Helper()
	m := Message{
		ID: id, ThreadID: threadID, Role: role, Kind: kind, Text: text,
		TurnIndex: 1, CreatedAt: createdAt,
	}
	if role == "tool" {
		name := "read_file"
		status := "completed"
		doneAt := formatTime(createdAt)
		m.ToolName = &name
		m.ToolStatus = &status
		m.ToolCompletedAt = &doneAt
		m.ToolInput = toolInput
	}
	if _, err := s.AppendMessage(context.Background(), m); err != nil {
		retentionFailure(t, "AppendMessage %s 失败: %v", id, err)
	}
}

// saveRetentionBees 把 bee 状态写进 hydra_leads.heads_json，供状态过滤读取。
func saveRetentionBees(t *testing.T, s *Store, leadThreadID string, bees ...retentionTestBee) {
	t.Helper()
	payload, err := json.Marshal(bees)
	if err != nil {
		retentionFailure(t, "序列化 heads_json 失败: %v", err)
	}
	if err := s.SaveSwarmLead(context.Background(), SwarmLeadRecord{
		LeadThreadID: leadThreadID,
		Enabled:      true,
		ProjectID:    "p1",
		NextIndex:    len(bees),
		BeesJSON:     string(payload),
		UpdatedAt:    retentionTestNow,
	}); err != nil {
		retentionFailure(t, "SaveSwarmLead 失败: %v", err)
	}
}

// testFinishedAt 返回相对固定时钟偏移的结束时间指针。
func testFinishedAt(d time.Duration) *time.Time {
	at := retentionTestNow.Add(d)
	return &at
}

// retentionCfg 返回一份注入固定时钟的默认配置。
func retentionCfg() BeeRetentionConfig {
	return BeeRetentionConfig{Now: retentionTestNow}
}

// rawMessageColumns 是一条消息在库里的原始列值。
type rawMessageColumns struct {
	Text          string
	ToolInput     string
	Encoding      string
	TextZ         []byte
	ToolInputZ    []byte
	Kind          string
	ToolName      string
	ToolStatus    string
	ToolCompleted string
}

// readRawMessage 直接读列，绕过 decodeMessageContent，用来确认压缩确实落库。
func readRawMessage(t *testing.T, s *Store, id string) rawMessageColumns {
	t.Helper()
	var rc rawMessageColumns
	err := s.db.QueryRowContext(context.Background(), `
	SELECT text, tool_input, text_encoding, text_z, tool_input_z, kind, tool_name, tool_status, tool_completed_at
	FROM messages WHERE id = ?`, id).
		Scan(&rc.Text, &rc.ToolInput, &rc.Encoding, &rc.TextZ, &rc.ToolInputZ, &rc.Kind, &rc.ToolName, &rc.ToolStatus, &rc.ToolCompleted)
	if err != nil {
		retentionFailure(t, "读取消息 %s 原始列失败: %v", id, err)
	}
	return rc
}

// countMessages 统计会话的消息条数（可按 kind 过滤，kind 为空表示全部）。
func countMessages(t *testing.T, s *Store, threadID, kind string) int {
	t.Helper()
	query := "SELECT COUNT(*) FROM messages WHERE thread_id = ?"
	args := []any{threadID}
	if kind != "" {
		query += " AND kind = ?"
		args = append(args, kind)
	}
	var n int
	if err := s.db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		retentionFailure(t, "统计 %s 消息失败: %v", threadID, err)
	}
	return n
}

// strPtr 返回字符串指针。
func strPtr(s string) *string { return &s }

// 主会话（is_helper = 0）的 tool 与 thinking 同样做无损压缩，assistant/user 正文一条都不能动。
func TestRunBeeMessageRetentionCompressesMainThreads(t *testing.T) {
	s, projectID := newRetentionTestStore(t, "retention_main.db")
	old := retentionTestNow.Add(-31 * 24 * time.Hour)
	createRetentionThread(t, s, projectID, "main1", false)
	toolText := strings.Repeat("主会话工具输出\n", 40)
	thinkingText := strings.Repeat("主会话思考\n", 40)
	assistantText := strings.Repeat("主会话正文\n", 40)
	appendRetentionMessage(t, s, "m1", "main1", "tool", "tool", toolText, strPtr(`{"path":"a.go"}`), old)
	appendRetentionMessage(t, s, "m2", "main1", "assistant", "thinking", thinkingText, nil, old)
	appendRetentionMessage(t, s, "m3", "main1", "assistant", "text", assistantText, nil, old)
	appendRetentionMessage(t, s, "m4", "main1", "user", "text", assistantText, nil, old)

	report, err := s.RunBeeMessageRetention(context.Background(), retentionCfg())
	if err != nil {
		t.Fatalf("RunBeeMessageRetention 失败: %v", err)
	}
	if report.ThreadsCompressed != 1 || report.MessagesCompressed != 2 || report.MessagesDeleted != 0 {
		t.Fatalf("期望压缩 1 个会话 2 条消息、不删除，实际 %+v", report)
	}
	if got := countMessages(t, s, "main1", ""); got != 4 {
		t.Fatalf("主会话消息条数应保持 4，实际 %d", got)
	}

	toolRaw := readRawMessage(t, s, "m1")
	if toolRaw.Text != "" || toolRaw.Encoding != messageEncodingZstd || len(toolRaw.TextZ) == 0 {
		t.Fatalf("主会话工具消息应被 zstd 压缩，实际 %+v", toolRaw)
	}
	thinkingRaw := readRawMessage(t, s, "m2")
	if thinkingRaw.Text != "" || thinkingRaw.Encoding != messageEncodingZstd || len(thinkingRaw.TextZ) == 0 {
		t.Fatalf("主会话思考消息应被 zstd 压缩，实际 %+v", thinkingRaw)
	}

	msgs, _, err := s.ListMessages(context.Background(), "main1", 0, 10)
	if err != nil {
		t.Fatalf("ListMessages 失败: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("读回条数应为 4，实际 %d", len(msgs))
	}
	if msgs[0].Text != toolText || derefString(msgs[0].ToolInput) != `{"path":"a.go"}` {
		t.Fatal("工具消息压缩后读回与原文不一致")
	}
	if msgs[1].Text != thinkingText {
		t.Fatal("思考消息压缩后读回与原文不一致")
	}
	for _, m := range msgs[2:] {
		if m.Text != assistantText {
			t.Fatalf("%s/%s 消息不应被压缩，实际 %q", m.Role, m.Kind, m.Text)
		}
		if raw := readRawMessage(t, s, m.ID); raw.Encoding != "" || raw.TextZ != nil {
			t.Fatalf("%s/%s 消息不应有压缩列：%+v", m.Role, m.Kind, raw)
		}
	}
}

// 思考消息的 30 天边界：未满保留期只做无损压缩，满期才删除。
// 删除保留的是会话最后一条消息（任一类型），因为 append 会复用被删掉的 seq；
// 所以这里把 thinking 放在最后追加，才等价于“保留最后一条思考”。
func TestRunBeeMessageRetentionThinkingBoundary(t *testing.T) {
	s, projectID := newRetentionTestStore(t, "retention_boundary.db")
	old := retentionTestNow.Add(-31 * 24 * time.Hour)
	createRetentionThread(t, s, projectID, "t_fresh", true)
	appendRetentionMessage(t, s, "f_tool", "t_fresh", "tool", "tool", strings.Repeat("刚满 29 天的工具输出\n", 40), nil, old)
	appendRetentionMessage(t, s, "f_k1", "t_fresh", "assistant", "thinking", "刚满 29 天的思考一", nil, old)
	appendRetentionMessage(t, s, "f_k2", "t_fresh", "assistant", "thinking", "刚满 29 天的思考二", nil, old)
	createRetentionThread(t, s, projectID, "t_old", true)
	appendRetentionMessage(t, s, "o_tool", "t_old", "tool", "tool", strings.Repeat("满 30 天的工具输出\n", 40), nil, old)
	appendRetentionMessage(t, s, "o_k1", "t_old", "assistant", "thinking", "满 30 天的思考一", nil, old)
	appendRetentionMessage(t, s, "o_k2", "t_old", "assistant", "thinking", "满 30 天的思考二", nil, old)
	saveRetentionBees(t, s, "lead1",
		retentionTestBee{ThreadID: "t_fresh", Status: "completed", FinishedAt: testFinishedAt(-29 * 24 * time.Hour)},
		retentionTestBee{ThreadID: "t_old", Status: "completed", FinishedAt: testFinishedAt(-31 * 24 * time.Hour)},
	)

	report, err := s.RunBeeMessageRetention(context.Background(), retentionCfg())
	if err != nil {
		t.Fatalf("RunBeeMessageRetention 失败: %v", err)
	}
	if report.ThreadsScanned != 2 || report.ThreadsEligible != 1 {
		t.Fatalf("期望扫描 2 个、处理 1 个，实际 %+v", report)
	}
	if report.Skipped[beeSkipNotFinished] != 1 {
		t.Fatalf("未满 30 天的会话应计入 %s，实际 %+v", beeSkipNotFinished, report.Skipped)
	}
	if report.MessagesDeleted != 1 {
		t.Fatalf("只有满 30 天的会话该删思考，实际删除 %d 条", report.MessagesDeleted)
	}
	if got := readRawMessage(t, s, "f_tool"); got.Encoding != messageEncodingZstd {
		t.Fatalf("未满 30 天的工具消息也应做无损压缩，实际 %+v", got)
	}
	if got := readRawMessage(t, s, "o_tool"); got.Encoding != messageEncodingZstd {
		t.Fatalf("满 30 天的工具消息应做无损压缩，实际 %+v", got)
	}
	if got := countMessages(t, s, "t_fresh", "thinking"); got != 2 {
		t.Fatalf("未满 30 天的思考消息不应删除，实际剩 %d 条", got)
	}
	if got := countMessages(t, s, "t_old", "thinking"); got != 1 {
		t.Fatalf("满 30 天的思考消息应只留最后一条，实际剩 %d 条", got)
	}

	// 再过两天，原本差 1 天的会话也满期，下一轮只保留它最后一条思考。
	later := retentionCfg()
	later.Now = retentionTestNow.Add(2 * 24 * time.Hour)
	second, err := s.RunBeeMessageRetention(context.Background(), later)
	if err != nil {
		t.Fatalf("第二轮 RunBeeMessageRetention 失败: %v", err)
	}
	if second.MessagesDeleted != 1 || second.MessagesCompressed != 0 {
		t.Fatalf("第二轮应只删 1 条思考、不再压缩，实际 %+v", second)
	}
	if got := countMessages(t, s, "t_fresh", "thinking"); got != 1 {
		t.Fatalf("满期后应只保留最后一条思考，实际剩 %d 条", got)
	}
}

// 状态不确定一律跳过，只有 completed + 副本已回收 + 满 30 天才处理。
func TestRunBeeMessageRetentionSkipsUncertainStates(t *testing.T) {
	s, projectID := newRetentionTestStore(t, "retention_states.db")
	old := retentionTestNow.Add(-31 * 24 * time.Hour)
	finished := testFinishedAt(-31 * 24 * time.Hour)
	// 副本目录用真实存在的临时目录：worktree_present 只看目录是否真的还在。
	retentionExistingWorktree := t.TempDir()

	type threadCase struct {
		id         string
		lastStatus string
		bee        *retentionTestBee // nil 表示不写进 heads_json
		skipReason string            // 空串表示应当处理
	}
	cases := []threadCase{
		{id: "t_running", bee: &retentionTestBee{ThreadID: "t_running", Status: "running", FinishedAt: finished}, skipReason: beeSkipRunning},
		{id: "t_blocked", bee: &retentionTestBee{ThreadID: "t_blocked", Status: "blocked", FinishedAt: finished}, skipReason: beeSkipStatusUnknown},
		{id: "t_interrupted", bee: &retentionTestBee{ThreadID: "t_interrupted", Status: "completed", Interrupted: true, FinishedAt: finished}, skipReason: beeSkipInterrupted},
		{id: "t_pending_merge", bee: &retentionTestBee{ThreadID: "t_pending_merge", Status: "completed", PendingMerge: true, FinishedAt: finished}, skipReason: beeSkipPendingMerge},
		{id: "t_merge_error", bee: &retentionTestBee{ThreadID: "t_merge_error", Status: "completed", MergeError: "合并失败", FinishedAt: finished}, skipReason: beeSkipMergeConflict},
		{id: "t_conflict", bee: &retentionTestBee{ThreadID: "t_conflict", Status: "completed", ConflictFiles: []string{"a.go"}, FinishedAt: finished}, skipReason: beeSkipMergeConflict},
		{id: "t_worktree", bee: &retentionTestBee{ThreadID: "t_worktree", Status: "completed", WorktreePath: retentionExistingWorktree, FinishedAt: finished}, skipReason: beeSkipWorktreePresent},
		{id: "t_unfinished", bee: &retentionTestBee{ThreadID: "t_unfinished", Status: "completed", FinishedAt: nil}, skipReason: beeSkipNotFinished},
		{id: "t_no_record", skipReason: beeSkipNoBeeRecord},
		{id: "t_thread_interrupted", lastStatus: "interrupted", skipReason: beeSkipThreadInterrupted},
		{id: "t_eligible", bee: &retentionTestBee{ThreadID: "t_eligible", Status: "completed", FinishedAt: finished}},
	}

	var bees []retentionTestBee
	for _, c := range cases {
		createRetentionThread(t, s, projectID, c.id, true)
		appendRetentionMessage(t, s, c.id+"_tool", c.id, "tool", "tool", "工具输出 "+c.id+"\n"+strings.Repeat("日志行中文", 60), nil, old)
		if c.lastStatus != "" {
			if err := s.UpdateThreadLastStatus(context.Background(), c.id, c.lastStatus); err != nil {
				retentionFailure(t, "UpdateThreadLastStatus %s 失败: %v", c.id, err)
			}
		}
		if c.bee != nil {
			bees = append(bees, *c.bee)
		}
	}
	saveRetentionBees(t, s, "lead1", bees...)

	report, err := s.RunBeeMessageRetention(context.Background(), retentionCfg())
	if err != nil {
		t.Fatalf("RunBeeMessageRetention 失败: %v", err)
	}
	if report.ThreadsScanned != len(cases) || report.ThreadsEligible != 1 {
		t.Fatalf("期望扫描 %d 个、处理 1 个，实际 %+v", len(cases), report)
	}
	for _, c := range cases {
		// 无损压缩与 bee 状态无关：这些会话的 tool 消息都该被压成 zstd。
		if got := readRawMessage(t, s, c.id+"_tool"); got.Encoding != messageEncodingZstd {
			t.Fatalf("会话 %s 的工具消息应被无损压缩，实际 %+v", c.id, got)
		}
		if c.skipReason == "" {
			continue
		}
		if report.Skipped[c.skipReason] < 1 {
			t.Fatalf("会话 %s 应计入跳过原因 %s，实际 %+v", c.id, c.skipReason, report.Skipped)
		}
	}
	if report.MessagesCompressed != len(cases) {
		t.Fatalf("期望压缩 %d 条，实际 %d", len(cases), report.MessagesCompressed)
	}
	if report.MessagesDeleted != 0 {
		t.Fatalf("没有思考消息可删，实际删除 %d 条", report.MessagesDeleted)
	}
}

// 压缩后经 ListMessages 读出的正文与原文逐字节一致；原本为空的 ToolInput 读回仍是 nil。
func TestRunBeeMessageRetentionReadPathTransparent(t *testing.T) {
	s, projectID := newRetentionTestStore(t, "retention_read.db")
	old := retentionTestNow.Add(-31 * 24 * time.Hour)
	createRetentionThread(t, s, projectID, "bee1", true)
	text := strings.Repeat("工具输出：中文 ✅ emoji 🚀\n第二行\x00带 NUL\n", 20)
	toolInput := `{"path":"中文路径/文件.go","nested":{"emoji":"🥳"},"padding":"` + strings.Repeat("日志行中文", 60) + `"}`
	appendRetentionMessage(t, s, "m_tool", "bee1", "tool", "tool", text, strPtr(toolInput), old)
	appendRetentionMessage(t, s, "m_tool_nil", "bee1", "tool", "tool", strings.Repeat("只有正文没有输入\n", 40), nil, old)
	appendRetentionMessage(t, s, "m_last", "bee1", "assistant", "text", "最终汇报", nil, old)
	saveRetentionBees(t, s, "lead1", retentionTestBee{ThreadID: "bee1", Status: "completed", FinishedAt: testFinishedAt(-31 * 24 * time.Hour)})

	before, _, err := s.ListMessages(context.Background(), "bee1", 0, 100)
	if err != nil {
		t.Fatalf("压缩前 ListMessages 失败: %v", err)
	}

	report, err := s.RunBeeMessageRetention(context.Background(), retentionCfg())
	if err != nil {
		t.Fatalf("RunBeeMessageRetention 失败: %v", err)
	}
	if report.MessagesCompressed != 2 {
		t.Fatalf("期望压缩 2 条工具消息，实际 %d", report.MessagesCompressed)
	}

	after, _, err := s.ListMessages(context.Background(), "bee1", 0, 100)
	if err != nil {
		t.Fatalf("压缩后 ListMessages 失败: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("消息条数变化：%d -> %d", len(before), len(after))
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].Text != before[i].Text || after[i].Seq != before[i].Seq {
			t.Fatalf("消息 %s 读回不一致：%+v -> %+v", before[i].ID, before[i], after[i])
		}
		if derefString(after[i].ToolInput) != derefString(before[i].ToolInput) {
			t.Fatalf("消息 %s 的 tool_input 读回不一致", before[i].ID)
		}
	}
	if after[1].ToolInput != nil {
		t.Fatalf("原本为空的 ToolInput 读回应为 nil，实际 %q", derefString(after[1].ToolInput))
	}

	// 原始列确实被压缩：正文清空、编码标记就位，避免「看起来透明其实没压」。
	raw := readRawMessage(t, s, "m_tool")
	if raw.Text != "" || raw.ToolInput != "" || raw.Encoding != messageEncodingZstd || len(raw.TextZ) == 0 || len(raw.ToolInputZ) == 0 {
		t.Fatalf("压缩落库形态不对：%+v", raw)
	}
	if raw.ToolName != "read_file" || raw.ToolStatus != "completed" || raw.ToolCompleted == "" {
		t.Fatalf("工具元信息不应被改动：%+v", raw)
	}
}

// 超长可压缩工具输出必须真的变小（短消息的 zstd 包装开销可能大于原文，不去断言）。
func TestRunBeeMessageRetentionShrinksLargeToolOutput(t *testing.T) {
	s, projectID := newRetentionTestStore(t, "retention_shrink.db")
	old := retentionTestNow.Add(-31 * 24 * time.Hour)
	createRetentionThread(t, s, projectID, "bee1", true)
	text := strings.Repeat("abc中文日志行 12345\n", 50000)
	appendRetentionMessage(t, s, "m1", "bee1", "tool", "tool", text, nil, old)
	saveRetentionBees(t, s, "lead1", retentionTestBee{ThreadID: "bee1", Status: "completed", FinishedAt: testFinishedAt(-31 * 24 * time.Hour)})

	report, err := s.RunBeeMessageRetention(context.Background(), retentionCfg())
	if err != nil {
		t.Fatalf("RunBeeMessageRetention 失败: %v", err)
	}
	if report.MessagesCompressed != 1 {
		t.Fatalf("期望压缩 1 条，实际 %d", report.MessagesCompressed)
	}
	if report.BytesBefore <= report.BytesAfter {
		t.Fatalf("压缩后字节数应变小：%d -> %d", report.BytesBefore, report.BytesAfter)
	}
	msgs, _, err := s.ListMessages(context.Background(), "bee1", 0, 10)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("读取压缩消息失败: %v，条数 %d", err, len(msgs))
	}
	if msgs[0].Text != text {
		t.Fatalf("长正文读回不一致：期望 %d 字节，实际 %d 字节", len(text), len(msgs[0].Text))
	}
}

// 思考消息被删除，但保留会话最后一条（避免 seq 被复用）。
func TestRunBeeMessageRetentionDeletesThinkingKeepsLast(t *testing.T) {
	s, projectID := newRetentionTestStore(t, "retention_thinking.db")
	old := retentionTestNow.Add(-31 * 24 * time.Hour)
	createRetentionThread(t, s, projectID, "bee1", true)
	appendRetentionMessage(t, s, "k1", "bee1", "assistant", "thinking", "思考一", nil, old)
	appendRetentionMessage(t, s, "k2", "bee1", "assistant", "thinking", "思考二", nil, old)
	appendRetentionMessage(t, s, "k3", "bee1", "tool", "tool", "工具输出", nil, old)
	appendRetentionMessage(t, s, "k4", "bee1", "assistant", "thinking", "最后一条思考", nil, old)
	saveRetentionBees(t, s, "lead1", retentionTestBee{ThreadID: "bee1", Status: "completed", FinishedAt: testFinishedAt(-31 * 24 * time.Hour)})

	report, err := s.RunBeeMessageRetention(context.Background(), retentionCfg())
	if err != nil {
		t.Fatalf("RunBeeMessageRetention 失败: %v", err)
	}
	if report.MessagesDeleted != 2 {
		t.Fatalf("期望删除 2 条思考，实际 %d", report.MessagesDeleted)
	}
	if got := countMessages(t, s, "bee1", "thinking"); got != 1 {
		t.Fatalf("应只保留最后一条思考，实际剩 %d 条", got)
	}
	if got := readRawMessage(t, s, "k4"); got.Text == "" {
		t.Fatal("最后一条思考应原样保留")
	}

	// 再跑一遍必须幂等。
	second, err := s.RunBeeMessageRetention(context.Background(), retentionCfg())
	if err != nil {
		t.Fatalf("第二次 RunBeeMessageRetention 失败: %v", err)
	}
	if second.MessagesCompressed != 0 || second.MessagesDeleted != 0 || second.ThreadsCompressed != 0 {
		t.Fatalf("第二次运行应无改动，实际 %+v", second)
	}

	// 删掉中间几条后 seq 仍严格递增，分页不会重复或漏。
	page1, hasMore, err := s.ListMessages(context.Background(), "bee1", 0, 1)
	if err != nil {
		t.Fatalf("分页第一页失败: %v", err)
	}
	if !hasMore || len(page1) != 1 {
		t.Fatalf("剩 2 条时 limit=1 应有下一页，实际 hasMore=%v 条数 %d", hasMore, len(page1))
	}
	page2, hasMore2, err := s.ListMessages(context.Background(), "bee1", page1[0].Seq, 100)
	if err != nil {
		t.Fatalf("分页第二页失败: %v", err)
	}
	if hasMore2 || len(page2) != 1 {
		t.Fatalf("第二页应恰有 1 条且无更多，实际 hasMore=%v 条数 %d", hasMore2, len(page2))
	}
	all, _, err := s.ListMessages(context.Background(), "bee1", 0, 100)
	if err != nil {
		t.Fatalf("读全量失败: %v", err)
	}
	if len(page1)+len(page2) != len(all) {
		t.Fatalf("两页合计 %d 条不等于全量 %d 条", len(page1)+len(page2), len(all))
	}
	lastSeq := int64(-1)
	for _, m := range all {
		if m.Seq <= lastSeq {
			t.Fatalf("seq 未严格递增：%d 在 %d 之后", m.Seq, lastSeq)
		}
		lastSeq = m.Seq
	}
}

// ctx 中途取消后重跑，最终库状态与一次跑完完全相同。
func TestRunBeeMessageRetentionInterruptThenResume(t *testing.T) {
	want := retentionFixtureDump(t, "retention_resume_full.db", false)
	got := retentionFixtureDump(t, "retention_resume_cut.db", true)
	if got != want {
		t.Fatalf("中断重跑后的库状态与一次跑完不同：\nwant:\n%s\ngot:\n%s", want, got)
	}
}

// retentionFixtureDump 准备一份相同数据，可选先被 ctx 中断一次，再跑完，最后导出全表状态。
func retentionFixtureDump(t *testing.T, dbName string, interrupt bool) string {
	t.Helper()
	s, projectID := newRetentionTestStore(t, dbName)
	old := retentionTestNow.Add(-31 * 24 * time.Hour)
	createRetentionThread(t, s, projectID, "bee1", true)
	for i := 1; i <= 4; i++ {
		appendRetentionMessage(t, s, fmt.Sprintf("t%d", i), "bee1", "tool", "tool",
			fmt.Sprintf("工具输出第 %d 条 %.100s", i, strings.Repeat("日志行中文", 40)),
			strPtr(fmt.Sprintf(`{"i":%d}`, i)), old)
	}
	appendRetentionMessage(t, s, "k1", "bee1", "assistant", "thinking", "思考一", nil, old)
	appendRetentionMessage(t, s, "k2", "bee1", "assistant", "thinking", "思考二", nil, old)
	appendRetentionMessage(t, s, "last", "bee1", "assistant", "text", "最终汇报", nil, old)
	saveRetentionBees(t, s, "lead1", retentionTestBee{ThreadID: "bee1", Status: "completed", FinishedAt: testFinishedAt(-31 * 24 * time.Hour)})

	cfg := retentionCfg()
	cfg.BatchRows = 1
	cfg.BatchPause = time.Nanosecond

	if interrupt {
		// 让任务在压完两批之后被取消：Err() 前两次返回 nil，第三次起返回 context.Canceled。
		ctx := &cancelAfterChecksContext{Context: context.Background(), remaining: 2}
		if _, err := s.RunBeeMessageRetention(ctx, cfg); err == nil {
			t.Fatalf("期望被 ctx 中断，实际一轮跑完了")
		}
		if got := countMessages(t, s, "bee1", "thinking"); got != 2 {
			t.Fatalf("中断点之后不应再删除思考消息，实际剩 %d 条", got)
		}
	}
	if _, err := s.RunBeeMessageRetention(context.Background(), cfg); err != nil {
		t.Fatalf("续跑失败: %v", err)
	}
	return dumpMessagesTable(t, s)
}

// cancelAfterChecksContext 在第 remaining 次 Err() 调用之后开始返回 context.Canceled。
// 只影响 store 代码自己检查的退出点，够用来精确模拟「跑了一半被杀」。
type cancelAfterChecksContext struct {
	context.Context
	remaining int
	mu        sync.Mutex
}

func (c *cancelAfterChecksContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.remaining <= 0 {
		return context.Canceled
	}
	c.remaining--
	return nil
}

// dumpMessagesTable 导出 messages 全表的关键列，用于比较两次运行的最终状态。
func dumpMessagesTable(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), `
	SELECT id, seq, role, kind, text, tool_input, text_encoding,
	       COALESCE(hex(text_z), ''), COALESCE(hex(tool_input_z), '')
	FROM messages ORDER BY id`)
	if err != nil {
		retentionFailure(t, "导出 messages 失败: %v", err)
	}
	defer rows.Close()

	var b strings.Builder
	for rows.Next() {
		var id, role, kind, text, toolInput, encoding, textZHex, toolInputZHex string
		var seq int64
		if err := rows.Scan(&id, &seq, &role, &kind, &text, &toolInput, &encoding, &textZHex, &toolInputZHex); err != nil {
			retentionFailure(t, "扫描导出行失败: %v", err)
		}
		fmt.Fprintf(&b, "%s|%d|%s|%s|%s|%s|%s|%s|%s\n", id, seq, role, kind, text, toolInput, encoding, textZHex, toolInputZHex)
	}
	if err := rows.Err(); err != nil {
		retentionFailure(t, "遍历导出行失败: %v", err)
	}
	return b.String()
}

// 对已压缩行调用 UpdateMessage 不能复活明文，且新内容读出来是新内容。
func TestUpdateMessageKeepsCompressedRowsCompressed(t *testing.T) {
	s, projectID := newRetentionTestStore(t, "retention_update.db")
	old := retentionTestNow.Add(-31 * 24 * time.Hour)
	createRetentionThread(t, s, projectID, "bee1", true)
	originalText := strings.Repeat("原始工具输出\n", 40)
	appendRetentionMessage(t, s, "m1", "bee1", "tool", "tool", originalText, strPtr(`{"path":"a.go"}`), old)
	saveRetentionBees(t, s, "lead1", retentionTestBee{ThreadID: "bee1", Status: "completed", FinishedAt: testFinishedAt(-31 * 24 * time.Hour)})
	if _, err := s.RunBeeMessageRetention(context.Background(), retentionCfg()); err != nil {
		t.Fatalf("RunBeeMessageRetention 失败: %v", err)
	}

	msgs, _, err := s.ListMessages(context.Background(), "bee1", 0, 10)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("读取压缩后消息失败: %v, 条数 %d", err, len(msgs))
	}
	msg := msgs[0]

	// 只改 tool_status：正文与编码必须原样。
	msg.ToolStatus = strPtr("failed")
	if err := s.UpdateMessage(context.Background(), msg); err != nil {
		t.Fatalf("UpdateMessage 失败: %v", err)
	}
	raw := readRawMessage(t, s, "m1")
	if raw.Text != "" || raw.ToolInput != "" || raw.Encoding != messageEncodingZstd {
		t.Fatalf("已压缩行被写回明文：%+v", raw)
	}
	readBack, _, err := s.ListMessages(context.Background(), "bee1", 0, 10)
	if err != nil || len(readBack) != 1 {
		t.Fatalf("再次读取失败: %v", err)
	}
	if readBack[0].Text != originalText || derefString(readBack[0].ToolInput) != `{"path":"a.go"}` {
		t.Fatalf("正文读回不一致：%+v", readBack[0])
	}
	if derefString(readBack[0].ToolStatus) != "failed" {
		t.Fatalf("tool_status 未更新：%q", derefString(readBack[0].ToolStatus))
	}

	// 改正文：读出来必须是新内容，而不是空串。
	readBack[0].Text = "补写的新正文"
	if err := s.UpdateMessage(context.Background(), readBack[0]); err != nil {
		t.Fatalf("UpdateMessage 改正文失败: %v", err)
	}
	if raw := readRawMessage(t, s, "m1"); raw.Text != "" || raw.Encoding != messageEncodingZstd {
		t.Fatalf("改正文后仍应按压缩形式落库：%+v", raw)
	}
	final, _, err := s.ListMessages(context.Background(), "bee1", 0, 10)
	if err != nil || len(final) != 1 {
		t.Fatalf("读取改后消息失败: %v", err)
	}
	if final[0].Text != "补写的新正文" {
		t.Fatalf("读回正文应为新内容，实际 %q", final[0].Text)
	}
}

// 老 gzip 行经 UpdateMessage 重新写入时升级为 zstd，读出的仍是新内容。
func TestUpdateMessageUpgradesLegacyGzipToZstd(t *testing.T) {
	s, projectID := newRetentionTestStore(t, "retention_gzip_upgrade.db")
	ctx := context.Background()
	old := retentionTestNow.Add(-31 * 24 * time.Hour)
	createRetentionThread(t, s, projectID, "main1", false)
	text := strings.Repeat("老 gzip 工具输出\n", 40)
	appendRetentionMessage(t, s, "g1", "main1", "tool", "tool", text, nil, old)

	textZ, err := gzipMessageContent(text)
	if err != nil {
		t.Fatalf("gzipMessageContent 失败: %v", err)
	}
	if _, err := s.db.ExecContext(ctx,
		"UPDATE messages SET text = '', text_z = ?, text_encoding = ? WHERE id = ?",
		textZ, messageEncodingGzip, "g1"); err != nil {
		t.Fatalf("构造 gzip 老数据失败: %v", err)
	}

	msgs, _, err := s.ListMessages(ctx, "main1", 0, 10)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("读 gzip 老数据失败: %v，条数 %d", err, len(msgs))
	}
	if msgs[0].Text != text {
		t.Fatal("gzip 老数据读回与原文不一致")
	}
	msgs[0].ToolStatus = strPtr("failed")
	if err := s.UpdateMessage(ctx, msgs[0]); err != nil {
		t.Fatalf("UpdateMessage 失败: %v", err)
	}

	raw := readRawMessage(t, s, "g1")
	if raw.Text != "" || raw.Encoding != messageEncodingZstd || len(raw.TextZ) == 0 {
		t.Fatalf("更新后应升级为 zstd，实际 %+v", raw)
	}
	final, _, err := s.ListMessages(ctx, "main1", 0, 10)
	if err != nil || len(final) != 1 {
		t.Fatalf("升级后读取失败: %v", err)
	}
	if final[0].Text != text || derefString(final[0].ToolStatus) != "failed" {
		t.Fatalf("升级后读回内容不对：%+v", final[0])
	}
}

// 搜索命中与压缩前一致（工具输出与思考本来就不参与搜索）。
func TestSearchMessagesUnaffectedByRetention(t *testing.T) {
	s, projectID := newRetentionTestStore(t, "retention_search.db")
	old := retentionTestNow.Add(-31 * 24 * time.Hour)
	createRetentionThread(t, s, projectID, "bee1", true)
	appendRetentionMessage(t, s, "u1", "bee1", "user", "text", "请检查颜色混合的问题", nil, old)
	appendRetentionMessage(t, s, "a1", "bee1", "assistant", "text", "颜色混合的原因是通道顺序", nil, old)
	appendRetentionMessage(t, s, "t1", "bee1", "tool", "tool", strings.Repeat("颜色混合工具的原始输出\n", 40), nil, old)
	appendRetentionMessage(t, s, "k1", "bee1", "assistant", "thinking", strings.Repeat("思考颜色混合\n", 40), nil, old)
	appendRetentionMessage(t, s, "k2", "bee1", "assistant", "thinking", strings.Repeat("再想一次颜色混合\n", 40), nil, old)
	saveRetentionBees(t, s, "lead1", retentionTestBee{ThreadID: "bee1", Status: "completed", FinishedAt: testFinishedAt(-31 * 24 * time.Hour)})

	before, err := s.SearchMessages(context.Background(), "颜色混合", 0)
	if err != nil {
		t.Fatalf("压缩前搜索失败: %v", err)
	}
	if len(before) != 2 {
		t.Fatalf("压缩前应命中 2 条（用户与助手），实际 %d", len(before))
	}
	if _, err := s.RunBeeMessageRetention(context.Background(), retentionCfg()); err != nil {
		t.Fatalf("RunBeeMessageRetention 失败: %v", err)
	}
	after, err := s.SearchMessages(context.Background(), "颜色混合", 0)
	if err != nil {
		t.Fatalf("压缩后搜索失败: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("命中条数变化：%d -> %d", len(before), len(after))
	}
	// 同一时间戳的命中顺序可能变，按消息 ID 比对集合。
	beforeHits := make(map[string]string, len(before))
	for _, hit := range before {
		beforeHits[hit.MessageID] = hit.Snippet
	}
	for _, hit := range after {
		snippet, ok := beforeHits[hit.MessageID]
		if !ok {
			t.Fatalf("压缩后多出命中 %s", hit.MessageID)
		}
		if snippet != hit.Snippet {
			t.Fatalf("命中 %s 的片段变化：%q -> %q", hit.MessageID, snippet, hit.Snippet)
		}
	}
}

// 旧库（没有三个新列）升级后能直接打开，未压缩数据读出与改动前一致。
func TestMessageRetentionMigrationOnOldDatabase(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "retention_old.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if err := s.UpsertProject(ctx, Project{ID: "p1", Name: "P1", Path: "/p1", CreatedAt: retentionTestNow, UpdatedAt: retentionTestNow}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}
	createRetentionThread(t, s, "p1", "t1", false)
	appendRetentionMessage(t, s, "m1", "t1", "tool", "tool", "旧库的工具输出", strPtr(`{"path":"旧.go"}`), retentionTestNow)
	appendRetentionMessage(t, s, "m2", "t1", "assistant", "text", "旧库正文", nil, retentionTestNow)

	before, _, err := s.ListMessages(ctx, "t1", 0, 10)
	if err != nil {
		t.Fatalf("升级前 ListMessages 失败: %v", err)
	}

	// 模拟升级前的旧库：删掉三个新列，并把 user_version 退回到补 text_z 的那条迁移之前。
	for _, column := range []string{"text_z", "tool_input_z", "text_encoding"} {
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE messages DROP COLUMN %s;", column)); err != nil {
			t.Fatalf("模拟旧库删除 %s 列失败: %v", column, err)
		}
	}
	textZMigrationIndex := -1
	for i, migration := range migrations {
		if strings.Contains(migration, "ALTER TABLE messages ADD COLUMN text_z") {
			textZMigrationIndex = i
			break
		}
	}
	if textZMigrationIndex < 0 {
		t.Fatal("migrations 中找不到补 text_z 列的迁移")
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d;", textZMigrationIndex)); err != nil {
		t.Fatalf("回退 user_version 失败: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("关闭旧库失败: %v", err)
	}

	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("升级旧库失败: %v", err)
	}
	defer reopened.Close()

	for _, column := range []string{"text_z", "tool_input_z", "text_encoding"} {
		var n int
		if err := reopened.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = ?", column).Scan(&n); err != nil {
			t.Fatalf("查询 %s 列失败: %v", column, err)
		}
		if n != 1 {
			t.Fatalf("升级后缺少列 %s", column)
		}
	}

	after, _, err := reopened.ListMessages(ctx, "t1", 0, 10)
	if err != nil {
		t.Fatalf("升级后 ListMessages 失败: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("升级后消息条数变化：%d -> %d", len(before), len(after))
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].Text != before[i].Text || after[i].Seq != before[i].Seq {
			t.Fatalf("升级后消息 %s 读回不一致：%+v -> %+v", before[i].ID, before[i], after[i])
		}
		if derefString(after[i].ToolInput) != derefString(before[i].ToolInput) {
			t.Fatalf("升级后消息 %s 的 tool_input 读回不一致", before[i].ID)
		}
	}
	if raw := readRawMessage(t, reopened, "m1"); raw.Encoding != "" || raw.TextZ != nil {
		t.Fatalf("旧数据不应被标记为已压缩：%+v", raw)
	}
}

// heads_json 是脏 JSON 时不能崩：解析失败的行当作没有 bee 记录，阶段二全部跳过，阶段一照常压缩。
func TestRunBeeMessageRetentionToleratesDirtyHeadsJSON(t *testing.T) {
	s, projectID := newRetentionTestStore(t, "retention_dirty.db")
	old := retentionTestNow.Add(-31 * 24 * time.Hour)
	createRetentionThread(t, s, projectID, "bee1", true)
	appendRetentionMessage(t, s, "m1", "bee1", "tool", "tool", strings.Repeat("工具输出\n", 60), nil, old)
	if err := s.SaveSwarmLead(context.Background(), SwarmLeadRecord{
		LeadThreadID: "lead1", Enabled: true, ProjectID: "p1", BeesJSON: "{不是 JSON", UpdatedAt: retentionTestNow,
	}); err != nil {
		t.Fatalf("SaveSwarmLead 失败: %v", err)
	}

	report, err := s.RunBeeMessageRetention(context.Background(), retentionCfg())
	if err != nil {
		t.Fatalf("脏 heads_json 不应让任务报错: %v", err)
	}
	if report.ThreadsScanned != 1 || report.ThreadsEligible != 0 || report.MessagesDeleted != 0 {
		t.Fatalf("脏 heads_json 应全部跳过阶段二，实际 %+v", report)
	}
	if report.Skipped[beeSkipNoBeeRecord] != 1 {
		t.Fatalf("应计入 %s，实际 %+v", beeSkipNoBeeRecord, report.Skipped)
	}
	if got := readRawMessage(t, s, "m1"); got.Encoding != messageEncodingZstd {
		t.Fatalf("阶段一应与 bee 状态无关，实际 %+v", got)
	}
}

// 一轮结束写入观测键，便于排障与「跑过没跑过」查询。
func TestRunBeeMessageRetentionRecordsLastRun(t *testing.T) {
	s, _ := newRetentionTestStore(t, "retention_setting.db")
	if _, err := s.RunBeeMessageRetention(context.Background(), retentionCfg()); err != nil {
		t.Fatalf("RunBeeMessageRetention 失败: %v", err)
	}
	value, ok, err := s.GetSetting(context.Background(), beeRetentionSettingKey)
	if err != nil || !ok {
		t.Fatalf("未写入 %s: ok=%v err=%v", beeRetentionSettingKey, ok, err)
	}
	if value != formatTime(retentionTestNow) {
		t.Fatalf("记录的时间应为注入时钟，实际 %q", value)
	}
	compressValue, ok, err := s.GetSetting(context.Background(), messageCompressSettingKey)
	if err != nil || !ok {
		t.Fatalf("未写入 %s: ok=%v err=%v", messageCompressSettingKey, ok, err)
	}
	if compressValue != formatTime(retentionTestNow) {
		t.Fatalf("压缩窗口记录的时间应为注入时钟，实际 %q", compressValue)
	}
}

// bee 记录里的 WorktreePath 可能指向早已删除的副本目录：目录不在了就不能一直按 worktree_present 跳过。
func TestRunBeeMessageRetentionProcessesBeeWithDeletedWorktree(t *testing.T) {
	s, projectID := newRetentionTestStore(t, "retention_worktree_missing.db")
	old := retentionTestNow.Add(-31 * 24 * time.Hour)
	createRetentionThread(t, s, projectID, "bee_gone", true)
	appendRetentionMessage(t, s, "m_gone", "bee_gone", "tool", "tool", strings.Repeat("工具输出\n", 60), nil, old)
	saveRetentionBees(t, s, "lead1", retentionTestBee{
		ThreadID:     "bee_gone",
		Status:       "completed",
		WorktreePath: filepath.Join(t.TempDir(), "已删除的副本"),
		FinishedAt:   testFinishedAt(-31 * 24 * time.Hour),
	})

	report, err := s.RunBeeMessageRetention(context.Background(), retentionCfg())
	if err != nil {
		t.Fatalf("RunBeeMessageRetention 失败: %v", err)
	}
	if report.Skipped[beeSkipWorktreePresent] != 0 {
		t.Fatalf("副本目录已不存在时不应计入 %s，实际 %+v", beeSkipWorktreePresent, report.Skipped)
	}
	if report.ThreadsEligible != 1 {
		t.Fatalf("副本目录已不存在的会话应被处理，实际 %+v", report)
	}
	if got := readRawMessage(t, s, "m_gone"); got.Encoding != messageEncodingZstd {
		t.Fatalf("副本目录已不存在时应压缩工具消息，实际 %+v", got)
	}
}

// 副本目录仍存在时阶段二跳过，但阶段一的无损压缩照常进行。
func TestRunBeeMessageRetentionSkipsBeeWithExistingWorktree(t *testing.T) {
	s, projectID := newRetentionTestStore(t, "retention_worktree_present.db")
	old := retentionTestNow.Add(-31 * 24 * time.Hour)
	createRetentionThread(t, s, projectID, "bee_alive", true)
	appendRetentionMessage(t, s, "m_alive", "bee_alive", "tool", "tool", strings.Repeat("工具输出\n", 60), nil, old)
	saveRetentionBees(t, s, "lead1", retentionTestBee{
		ThreadID:     "bee_alive",
		Status:       "completed",
		WorktreePath: "/不存在的假路径/但注入判定为存在",
		FinishedAt:   testFinishedAt(-31 * 24 * time.Hour),
	})

	cfg := retentionCfg()
	cfg.PathExists = func(string) bool { return true }
	report, err := s.RunBeeMessageRetention(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunBeeMessageRetention 失败: %v", err)
	}
	if report.Skipped[beeSkipWorktreePresent] != 1 {
		t.Fatalf("副本仍存在时应计入 %s，实际 %+v", beeSkipWorktreePresent, report.Skipped)
	}
	if report.ThreadsEligible != 0 {
		t.Fatalf("副本仍存在时不应处理任何会话，实际 %+v", report)
	}
	if got := readRawMessage(t, s, "m_alive"); got.Encoding != messageEncodingZstd {
		t.Fatalf("阶段一不受副本状态影响，应压缩工具消息，实际 %+v", got)
	}
}

// 有进行中轮次的会话不压缩：轮次可能还在写消息，等它收尾后再压。
func TestRunBeeMessageRetentionSkipsThreadWithRunningTurn(t *testing.T) {
	s, projectID := newRetentionTestStore(t, "retention_running.db")
	old := retentionTestNow.Add(-31 * 24 * time.Hour)
	createRetentionThread(t, s, projectID, "running1", false)
	appendRetentionMessage(t, s, "r1", "running1", "tool", "tool", strings.Repeat("运行中会话的工具输出\n", 40), nil, old)
	if err := s.CreateTurn(context.Background(), Turn{
		ID: "turn1", ThreadID: "running1", TurnIndex: 1, Status: "running", StartedAt: old,
	}); err != nil {
		t.Fatalf("CreateTurn 失败: %v", err)
	}

	report, err := s.RunBeeMessageRetention(context.Background(), retentionCfg())
	if err != nil {
		t.Fatalf("RunBeeMessageRetention 失败: %v", err)
	}
	if report.MessagesCompressed != 0 || report.ThreadsCompressed != 0 {
		t.Fatalf("有进行中轮次的会话不应压缩，实际 %+v", report)
	}
	if got := readRawMessage(t, s, "r1"); got.Encoding != "" {
		t.Fatalf("进行中会话的消息应保持明文，实际 %+v", got)
	}
}

// 10 分钟静默期内的新行不压：行还可能被 UpdateMessage / 崩溃收尾改写。
func TestRunBeeMessageRetentionSkipsFreshMessages(t *testing.T) {
	s, projectID := newRetentionTestStore(t, "retention_fresh_rows.db")
	old := retentionTestNow.Add(-31 * 24 * time.Hour)
	createRetentionThread(t, s, projectID, "main1", false)
	appendRetentionMessage(t, s, "m_old", "main1", "tool", "tool", strings.Repeat("旧工具输出\n", 40), nil, old)
	appendRetentionMessage(t, s, "m_fresh", "main1", "tool", "tool", strings.Repeat("刚写下的工具输出\n", 40), nil, retentionTestNow.Add(-time.Minute))
	// AppendMessage 会把 threads.updated_at 推到最新一条，这里手动压回旧时间，只留行级静默期这一道门槛。
	if _, err := s.db.ExecContext(context.Background(), "UPDATE threads SET updated_at = ? WHERE id = ?", formatTime(retentionTestNow.Add(-24*time.Hour)), "main1"); err != nil {
		t.Fatalf("回拨 threads.updated_at 失败: %v", err)
	}

	report, err := s.RunBeeMessageRetention(context.Background(), retentionCfg())
	if err != nil {
		t.Fatalf("RunBeeMessageRetention 失败: %v", err)
	}
	if report.MessagesCompressed != 1 {
		t.Fatalf("只应压缩 10 分钟前的那一条，实际 %d", report.MessagesCompressed)
	}
	if got := readRawMessage(t, s, "m_old"); got.Encoding != messageEncodingZstd {
		t.Fatalf("旧行应被压缩，实际 %+v", got)
	}
	if got := readRawMessage(t, s, "m_fresh"); got.Encoding != "" || got.Text == "" {
		t.Fatalf("10 分钟内的新行应保持明文，实际 %+v", got)
	}
}

// 正文加 tool_input 不足 256 字节的行保持明文，压缩只会更大。
func TestRunBeeMessageRetentionLeavesShortRowsPlain(t *testing.T) {
	s, projectID := newRetentionTestStore(t, "retention_short.db")
	old := retentionTestNow.Add(-31 * 24 * time.Hour)
	createRetentionThread(t, s, projectID, "main1", false)
	appendRetentionMessage(t, s, "s1", "main1", "tool", "tool", "短工具输出", strPtr(`{"a":1}`), old)
	appendRetentionMessage(t, s, "s2", "main1", "assistant", "thinking", "短思考", nil, old)

	report, err := s.RunBeeMessageRetention(context.Background(), retentionCfg())
	if err != nil {
		t.Fatalf("RunBeeMessageRetention 失败: %v", err)
	}
	if report.MessagesCompressed != 0 || report.ThreadsCompressed != 0 {
		t.Fatalf("短行不应压缩，实际 %+v", report)
	}
	for _, id := range []string{"s1", "s2"} {
		if got := readRawMessage(t, s, id); got.Encoding != "" || got.Text == "" {
			t.Fatalf("短行 %s 应保持明文，实际 %+v", id, got)
		}
	}
}

// 默认实现只对「确定不存在」返回 false；其余不确定的错误一律视为存在。
func TestDefaultBeeWorktreeExists(t *testing.T) {
	dir := t.TempDir()
	if !defaultBeeWorktreeExists(dir) {
		t.Fatalf("存在的目录 %s 应判定为存在", dir)
	}
	missing := filepath.Join(dir, "not-there")
	if defaultBeeWorktreeExists(missing) {
		t.Fatalf("不存在的路径 %s 应判定为不存在", missing)
	}
	if !defaultBeeWorktreeExists("非法路径\x00") {
		t.Fatal("无法判定的错误应视为存在，继续跳过")
	}
}

// 跳过判定：只有副本目录确实存在才按 worktree_present 跳过，其余跳过规则的顺序不受影响。
func TestBeeRetentionSkipReasonWorktree(t *testing.T) {
	cutoff := retentionTestNow.Add(-beeRetentionRetention)
	finished := testFinishedAt(-31 * 24 * time.Hour)
	existing := t.TempDir()
	missing := filepath.Join(existing, "gone")
	alwaysTrue := func(string) bool { return true }
	alwaysFalse := func(string) bool { return false }

	cases := []struct {
		name       string
		st         beeRetentionState
		pathExists func(string) bool
		want       string
	}{
		{
			name: "目录存在时跳过",
			st:   beeRetentionState{Status: "completed", WorktreePath: existing, FinishedAt: finished},
			want: beeSkipWorktreePresent,
		},
		{
			name: "目录已删除时不跳过",
			st:   beeRetentionState{Status: "completed", WorktreePath: missing, FinishedAt: finished},
			want: "",
		},
		{
			name:       "注入判定存在时跳过",
			st:         beeRetentionState{Status: "completed", WorktreePath: missing, FinishedAt: finished},
			pathExists: alwaysTrue,
			want:       beeSkipWorktreePresent,
		},
		{
			name:       "注入判定不存在时不跳过",
			st:         beeRetentionState{Status: "completed", WorktreePath: existing, FinishedAt: finished},
			pathExists: alwaysFalse,
			want:       "",
		},
		{
			name: "目录已删除时继续走后续规则",
			st:   beeRetentionState{Status: "completed", WorktreePath: missing},
			want: beeSkipNotFinished,
		},
		{
			name: "无副本路径时不影响其他规则",
			st:   beeRetentionState{Status: "running", WorktreePath: existing, FinishedAt: finished},
			want: beeSkipRunning,
		},
	}
	for _, c := range cases {
		if got := beeRetentionSkipReason(c.st, true, "completed", cutoff, c.pathExists); got != c.want {
			t.Fatalf("%s：期望 %q，实际 %q", c.name, c.want, got)
		}
	}
}
