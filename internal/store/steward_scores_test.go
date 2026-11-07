package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// newStewardScoreTestStore 建一个临时库，供总管建议计分测试使用。
func newStewardScoreTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s, err := Open(filepath.Join(t.TempDir(), "steward_scores.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// getStewardScore 按 request_id 读一行，读不到直接终止测试。
func getStewardScore(t *testing.T, s *Store, requestID string) StewardApprovalScore {
	t.Helper()
	score, err := scanStewardApprovalScore(s.db.QueryRowContext(context.Background(),
		"SELECT "+stewardScoreSelectCols+" FROM steward_approval_scores WHERE request_id = ?", requestID))
	if err != nil {
		t.Fatalf("读取 steward_approval_score %s 失败: %v", requestID, err)
	}
	return score
}

// insertStewardScoresBulk 绕过 UpsertStewardApprovalScore 直接批量写入，供保留策略测试构造数据，
// 避免 upsert 自带的每小时清理干扰用例。
func insertStewardScoresBulk(t *testing.T, s *Store, scores []StewardApprovalScore) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx 失败: %v", err)
	}
	defer tx.Rollback()
	for _, score := range scores {
		outcome := score.Outcome
		if outcome == "" {
			outcome = StewardScorePending
		}
		if _, err := tx.ExecContext(ctx, `
		INSERT INTO steward_approval_scores (request_id, kind, outcome, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		`, score.RequestID, score.Kind, outcome, formatTime(score.CreatedAt), formatTime(score.UpdatedAt)); err != nil {
			t.Fatalf("写入 %s 失败: %v", score.RequestID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit 失败: %v", err)
	}
}

// TestStewardScoreMigration 迁移后表与两个索引都存在，关键列与默认值符合契约。
func TestStewardScoreMigration(t *testing.T) {
	s := newStewardScoreTestStore(t)
	ctx := context.Background()

	var objects int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master WHERE name IN
			('steward_approval_scores', 'idx_steward_scores_outcome', 'idx_steward_scores_created')
	`).Scan(&objects); err != nil {
		t.Fatalf("检查建表结果失败: %v", err)
	}
	if objects != 3 {
		t.Fatalf("表/索引对象数 = %d，期望 3", objects)
	}

	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info(steward_approval_scores);")
	if err != nil {
		t.Fatalf("读取表结构失败: %v", err)
	}
	defer rows.Close()
	columns := map[string]string{}
	for rows.Next() {
		var (
			cid        int
			name, typ  string
			notNull    int
			dfltValue  sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dfltValue, &primaryKey); err != nil {
			t.Fatalf("扫描表结构失败: %v", err)
		}
		columns[name] = dfltValue.String
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历表结构失败: %v", err)
	}
	for _, name := range []string{
		"request_id", "thread_id", "turn_id", "kind", "tool_name",
		"suggested_option", "suggested_role", "chosen_option", "chosen_role",
		"outcome", "answer_source", "unanswered_reason", "created_at", "updated_at", "answered_at",
	} {
		if _, ok := columns[name]; !ok {
			t.Fatalf("缺少列 %s", name)
		}
	}
	if got := columns["outcome"]; got != "'pending'" {
		t.Fatalf("outcome 默认值 = %q，期望 'pending'", got)
	}
}

// TestStewardScoreUpsert 登记建行、重复登记覆盖建议，已判定行不会被翻回 pending。
func TestStewardScoreUpsert(t *testing.T) {
	s := newStewardScoreTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := s.UpsertStewardApprovalScore(ctx, StewardApprovalScore{
		RequestID: "r1", ThreadID: "t1", TurnID: "turn-1", Kind: "command",
		SuggestedOption: "allow_once", SuggestedRole: "allow", CreatedAt: now,
	}); err != nil {
		t.Fatalf("首次 Upsert 失败: %v", err)
	}
	got := getStewardScore(t, s, "r1")
	if got.ThreadID != "t1" || got.TurnID != "turn-1" || got.Kind != "command" ||
		got.SuggestedOption != "allow_once" || got.SuggestedRole != "allow" || got.Outcome != StewardScorePending {
		t.Fatalf("首次登记结果不对: %+v", got)
	}

	if err := s.UpsertStewardApprovalScore(ctx, StewardApprovalScore{
		RequestID: "r1", ThreadID: "t1", TurnID: "turn-1", Kind: "command",
		SuggestedOption: "allow_always", SuggestedRole: "allow", CreatedAt: now,
	}); err != nil {
		t.Fatalf("重复 Upsert 失败: %v", err)
	}
	got = getStewardScore(t, s, "r1")
	if got.SuggestedOption != "allow_always" || got.Outcome != StewardScorePending {
		t.Fatalf("重复登记未覆盖建议: %+v", got)
	}

	if err := s.RecordStewardAnswer(ctx, "r1", "command", "Bash", "allow_always", "allow", "direct"); err != nil {
		t.Fatalf("RecordStewardAnswer 失败: %v", err)
	}
	if err := s.UpsertStewardApprovalScore(ctx, StewardApprovalScore{
		RequestID: "r1", ThreadID: "t1", Kind: "command",
		SuggestedOption: "deny", SuggestedRole: "deny", CreatedAt: now,
	}); err != nil {
		t.Fatalf("已判定行 Upsert 不应报错: %v", err)
	}
	got = getStewardScore(t, s, "r1")
	if got.Outcome != StewardScoreAgree || got.SuggestedOption != "allow_always" || got.ChosenOption != "allow_always" {
		t.Fatalf("已判定行被 Upsert 改写: %+v", got)
	}

	if err := s.UpsertStewardApprovalScore(ctx, StewardApprovalScore{}); err == nil {
		t.Fatal("空 request_id 应被拒绝")
	}
}

// TestStewardScoreRecordAnswer 同角色记 agree、不同角色记 disagree、缺行无错、先到者胜。
func TestStewardScoreRecordAnswer(t *testing.T) {
	s := newStewardScoreTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, requestID := range []string{"agree", "disagree"} {
		if err := s.UpsertStewardApprovalScore(ctx, StewardApprovalScore{
			RequestID: requestID, Kind: "command", SuggestedOption: "allow_once",
			SuggestedRole: "allow", CreatedAt: now,
		}); err != nil {
			t.Fatalf("登记 %s 失败: %v", requestID, err)
		}
	}
	if err := s.RecordStewardAnswer(ctx, "agree", "command", "Bash", "allow_always", "allow", "confirm"); err != nil {
		t.Fatalf("同角色答复失败: %v", err)
	}
	if got := getStewardScore(t, s, "agree"); got.Outcome != StewardScoreAgree || got.AnswerSource != "confirm" || got.AnsweredAt == nil {
		t.Fatalf("同角色未记 agree: %+v", got)
	}
	if err := s.RecordStewardAnswer(ctx, "disagree", "command", "Bash", "deny", "deny", "remote"); err != nil {
		t.Fatalf("不同角色答复失败: %v", err)
	}
	if got := getStewardScore(t, s, "disagree"); got.Outcome != StewardScoreDisagree || got.ChosenOption != "deny" {
		t.Fatalf("不同角色未记 disagree: %+v", got)
	}

	if err := s.RecordStewardAnswer(ctx, "missing", "command", "Bash", "deny", "deny", "direct"); err != nil {
		t.Fatalf("缺建议行不应报错: %v", err)
	}
	if n := countRows(t, s, "steward_approval_scores"); n != 2 {
		t.Fatalf("缺建议行答复不应建行，行数 = %d", n)
	}

	if err := s.RecordStewardAnswer(ctx, "agree", "command", "Bash", "deny", "deny", "direct"); err != nil {
		t.Fatalf("第二次答复失败: %v", err)
	}
	got := getStewardScore(t, s, "agree")
	if got.Outcome != StewardScoreAgree || got.ChosenOption != "allow_always" || got.AnswerSource != "confirm" {
		t.Fatalf("已判定行被二次答复改写: %+v", got)
	}
}

// TestStewardScoreMarkUnanswered 只把 pending 改为 unanswered，已判定行保持不变。
func TestStewardScoreMarkUnanswered(t *testing.T) {
	s := newStewardScoreTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, requestID := range []string{"pending", "agreed"} {
		if err := s.UpsertStewardApprovalScore(ctx, StewardApprovalScore{
			RequestID: requestID, Kind: "command", SuggestedOption: "allow_once",
			SuggestedRole: "allow", CreatedAt: now,
		}); err != nil {
			t.Fatalf("登记 %s 失败: %v", requestID, err)
		}
	}
	if err := s.RecordStewardAnswer(ctx, "agreed", "command", "Bash", "allow_once", "allow", "direct"); err != nil {
		t.Fatalf("答复失败: %v", err)
	}

	if err := s.MarkStewardScoreUnanswered(ctx, "pending", "withdrawn"); err != nil {
		t.Fatalf("标记未答复失败: %v", err)
	}
	got := getStewardScore(t, s, "pending")
	if got.Outcome != StewardScoreUnanswered || got.UnansweredReason != "withdrawn" || got.AnsweredAt != nil {
		t.Fatalf("pending 行未标记为 unanswered: %+v", got)
	}

	if err := s.MarkStewardScoreUnanswered(ctx, "agreed", "canceled"); err != nil {
		t.Fatalf("已判定行标记不应报错: %v", err)
	}
	if got := getStewardScore(t, s, "agreed"); got.Outcome != StewardScoreAgree || got.UnansweredReason != "" {
		t.Fatalf("已判定行被未答复覆盖: %+v", got)
	}

	if err := s.MarkStewardScoreUnanswered(ctx, "", "archive"); err != nil {
		t.Fatalf("空 request_id 应直接返回: %v", err)
	}
}

// TestStewardScoreList 按登记时间倒序返回，limit 生效。
func TestStewardScoreList(t *testing.T) {
	s := newStewardScoreTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)

	for i, requestID := range []string{"r1", "r2", "r3"} {
		if err := s.UpsertStewardApprovalScore(ctx, StewardApprovalScore{
			RequestID: requestID, Kind: "command", SuggestedOption: "allow_once",
			SuggestedRole: "allow", CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("登记 %s 失败: %v", requestID, err)
		}
	}

	all, err := s.ListStewardApprovalScores(ctx, 0)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(all) != 3 || all[0].RequestID != "r3" || all[1].RequestID != "r2" || all[2].RequestID != "r1" {
		t.Fatalf("倒序结果不对: %+v", all)
	}

	limited, err := s.ListStewardApprovalScores(ctx, 2)
	if err != nil {
		t.Fatalf("限量 List 失败: %v", err)
	}
	if len(limited) != 2 || limited[0].RequestID != "r3" || limited[1].RequestID != "r2" {
		t.Fatalf("limit 结果不对: %+v", limited)
	}
}

// TestStewardScorePrune 保留策略的两个条件可分别删除：90 天与最多 5000 行。
func TestStewardScorePrune(t *testing.T) {
	s := newStewardScoreTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	old := now.Add(-100 * 24 * time.Hour)
	insertStewardScoresBulk(t, s, []StewardApprovalScore{
		{RequestID: "old1", CreatedAt: old, UpdatedAt: old},
		{RequestID: "old2", CreatedAt: old.Add(time.Minute), UpdatedAt: old},
		{RequestID: "new1", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now},
		{RequestID: "new2", CreatedAt: now.Add(-time.Hour), UpdatedAt: now},
		{RequestID: "new3", CreatedAt: now, UpdatedAt: now},
	})
	if err := s.PruneStewardApprovalScores(ctx, 90*24*time.Hour, 0); err != nil {
		t.Fatalf("按时间清理失败: %v", err)
	}
	if n := countRows(t, s, "steward_approval_scores"); n != 3 {
		t.Fatalf("按时间清理后行数 = %d，期望 3", n)
	}
	var oldLeft int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM steward_approval_scores WHERE request_id LIKE 'old%'").Scan(&oldLeft); err != nil {
		t.Fatalf("统计旧行失败: %v", err)
	}
	if oldLeft != 0 {
		t.Fatalf("90 天前的行未被删除: %d", oldLeft)
	}

	bulk := make([]StewardApprovalScore, 0, 5010)
	if _, err := s.db.ExecContext(ctx, "DELETE FROM steward_approval_scores"); err != nil {
		t.Fatalf("清空行数用例数据失败: %v", err)
	}
	for i := 0; i < 5010; i++ {
		createdAt := now.Add(-time.Duration(5010-i) * time.Minute)
		bulk = append(bulk, StewardApprovalScore{
			RequestID: fmt.Sprintf("b%04d", i), CreatedAt: createdAt, UpdatedAt: createdAt,
		})
	}
	insertStewardScoresBulk(t, s, bulk)
	if err := s.PruneStewardApprovalScores(ctx, 0, 5000); err != nil {
		t.Fatalf("按行数清理失败: %v", err)
	}
	if n := countRows(t, s, "steward_approval_scores"); n != 5000 {
		t.Fatalf("按行数清理后行数 = %d，期望 5000", n)
	}
	var earlyLeft int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM steward_approval_scores WHERE request_id IN (?, ?, ?)
	`, "b0000", "b0001", "b0002").Scan(&earlyLeft); err != nil {
		t.Fatalf("统计最旧行失败: %v", err)
	}
	if earlyLeft != 0 {
		t.Fatalf("超出 5000 行的最旧记录未被删除: %d", earlyLeft)
	}
}
