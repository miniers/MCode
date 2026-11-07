package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTurnsAndThreads_NewDB 验证全新数据库中 14 个新列、turns 表及其索引全部存在，user_version 正确。
func TestTurnsAndThreads_NewDB(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "new_db.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	var userVersion int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version;").Scan(&userVersion); err != nil {
		t.Fatalf("查询 user_version 失败: %v", err)
	}
	if userVersion != len(migrations) {
		t.Fatalf("期望 user_version 为 %d，实际为 %d", len(migrations), userVersion)
	}

	// 验证 threads 表中 14 个新列
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info(threads);")
	if err != nil {
		t.Fatalf("PRAGMA table_info(threads) 失败: %v", err)
	}
	defer rows.Close()

	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		cols[name] = true
	}

	expectedCols := []string{
		"is_settled", "settled_at", "is_provisional", "worktree_path", "branch",
		"worktree_requested", "hydra_pair_id", "hydra_mode", "parent_thread_id",
		"is_helper", "has_unread", "last_status", "provider_resume_at", "provider_anchor",
	}
	for _, col := range expectedCols {
		if !cols[col] {
			t.Errorf("threads 表缺少字段: %s", col)
		}
	}

	// 验证 turns 表存在
	var turnsTableCount int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='turns';").Scan(&turnsTableCount); err != nil || turnsTableCount != 1 {
		t.Fatalf("turns 表不存在: %v, count=%d", err, turnsTableCount)
	}

	// 验证 3 个新索引存在
	for _, idx := range []string{"idx_threads_parent", "idx_threads_settled", "idx_turns_thread"} {
		var idxCount int
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?;", idx).Scan(&idxCount); err != nil || idxCount != 1 {
			t.Fatalf("索引 %s 不存在: %v, count=%d", idx, err, idxCount)
		}
	}
}

// TestMigrationIdempotency_OldDB 模拟部分迁移的旧库，验证增量迁移幂等性。
func TestMigrationIdempotency_OldDB(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "partial_old.db")

	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("打开 rawDB 失败: %v", err)
	}

	// 只跑到 is_settled 迁移之前的版本：找到第一条 ADD COLUMN is_settled 的下标
	oldVersion := -1
	for i, m := range migrations {
		if strings.Contains(m, "ADD COLUMN is_settled") {
			oldVersion = i
			break
		}
	}
	if oldVersion < 0 {
		t.Fatal("未找到包含 ADD COLUMN is_settled 的迁移")
	}
	oldStore := &Store{db: rawDB}
	for i := 0; i < oldVersion; i++ {
		if err := oldStore.applyMigration(ctx, migrations[i], i+1); err != nil {
			t.Fatalf("执行旧迁移 v%d 失败: %v", i+1, err)
		}
	}

	// 预先手工 ADD 其中两列，模拟部分迁移的旧库
	if _, err := rawDB.ExecContext(ctx, "ALTER TABLE threads ADD COLUMN is_settled INTEGER NOT NULL DEFAULT 0;"); err != nil {
		t.Fatalf("手工 ADD is_settled 失败: %v", err)
	}
	if _, err := rawDB.ExecContext(ctx, "ALTER TABLE threads ADD COLUMN settled_at TEXT;"); err != nil {
		t.Fatalf("手工 ADD settled_at 失败: %v", err)
	}
	now := time.Now().UTC()
	if err := oldStore.UpsertProject(ctx, Project{ID: "old-project", Name: "旧项目", Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.ExecContext(ctx, `
		INSERT INTO threads (id, project_id, title, provider, created_at, updated_at, is_settled, settled_at)
		VALUES ('old-thread', 'old-project', '旧会话', 'codex', ?, ?, 1, ?)
	`, formatTime(now), formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}

	if err := rawDB.Close(); err != nil {
		t.Fatalf("关闭 rawDB 失败: %v", err)
	}

	// 重新 Open，迁移不报错且所有列都在
	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("重新 Open 失败: %v", err)
	}

	var v1 int
	if err := s1.db.QueryRowContext(ctx, "PRAGMA user_version;").Scan(&v1); err != nil {
		t.Fatalf("查询 user_version 失败: %v", err)
	}
	if v1 != len(migrations) {
		t.Fatalf("期望 user_version 为 %d，实际为 %d", len(migrations), v1)
	}

	if err := s1.Close(); err != nil {
		t.Fatalf("关闭 s1 失败: %v", err)
	}

	// 重复 Open 一次仍然正常
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("重复 Open 失败: %v", err)
	}
	defer s2.Close()

	var v2 int
	if err := s2.db.QueryRowContext(ctx, "PRAGMA user_version;").Scan(&v2); err != nil {
		t.Fatalf("二次查询 user_version 失败: %v", err)
	}
	if v2 != len(migrations) {
		t.Fatalf("期望二次 user_version 为 %d，实际为 %d", len(migrations), v2)
	}
	// 读取全部新列，确认跳过已有列后其余迁移仍执行，且旧数据未丢失。
	thread, err := s2.GetThread(ctx, "old-thread")
	if err != nil {
		t.Fatal(err)
	}
	if thread.Title != "旧会话" || !thread.IsDormant || thread.DormantAt == nil || !thread.DormantAt.Equal(now) || thread.SwarmMode != "inherited" {
		t.Fatalf("旧会话数据或新增默认值错误: %+v", thread)
	}
	if err := s2.CreateTurn(ctx, Turn{ID: "migrated-turn", ThreadID: thread.ID, TurnIndex: 1, Status: "completed"}); err != nil {
		t.Fatalf("迁移后 turns 不可写: %v", err)
	}
	for _, index := range []string{"idx_threads_parent", "idx_threads_settled", "idx_turns_thread"} {
		var count int
		if err := s2.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?", index).Scan(&count); err != nil || count != 1 {
			t.Fatalf("迁移后索引 %s 缺失: count=%d, err=%v", index, count, err)
		}
	}
}

// TestThreadNewFieldsRoundTrip 验证 Thread 新字段在 CreateThread 后通过 GetThread/ListThreads 往返一致，SwarmMode 默认为 'inherited'。
func TestThreadNewFieldsRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "thread_roundtrip.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	if err := s.UpsertProject(ctx, Project{ID: "p1", Name: "Project 1", Path: "/path/p1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}

	// 1. 默认值验证：SwarmMode 为空时应写入 'inherited'
	thDefault := Thread{
		ID:        "t-default",
		ProjectID: "p1",
		Title:     "Default Thread",
		Provider:  "anthropic",
		Model:     "claude-3-5-sonnet",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateThread(ctx, thDefault); err != nil {
		t.Fatalf("CreateThread default 失败: %v", err)
	}

	gotDefault, err := s.GetThread(ctx, "t-default")
	if err != nil {
		t.Fatalf("GetThread default 失败: %v", err)
	}
	if gotDefault.SwarmMode != "inherited" {
		t.Errorf("期望 SwarmMode 默认为 'inherited'，实际为 %q", gotDefault.SwarmMode)
	}
	if gotDefault.IsDormant || gotDefault.DormantAt != nil || gotDefault.IsProvisional || gotDefault.WorktreeRequested || gotDefault.IsHelper || gotDefault.HasUnread {
		t.Errorf("默认布尔及指针字段期望为零值，实际为: %+v", gotDefault)
	}

	// 2. 完整新字段验证
	dormantAt := now.Truncate(time.Millisecond)
	thFull := Thread{
		ID:                "t-full",
		ProjectID:         "p1",
		Title:             "Full Thread",
		Provider:          "anthropic",
		Model:             "claude-3-5-sonnet",
		CreatedAt:         now,
		UpdatedAt:         now,
		IsDormant:         true,
		DormantAt:         &dormantAt,
		IsProvisional:     true,
		WorktreePath:      "/tmp/worktree",
		Branch:            "feature/branch",
		WorktreeRequested: true,
		SwarmPairID:       "pair-123",
		SwarmMode:         "lead",
		ParentThreadID:    "t-default",
		IsHelper:          true,
		HasUnread:         true,
		LastStatus:        "completed",
		ProviderResumeAt:  "resume-tok",
		ProviderAnchor:    "anchor-hash",
	}
	if err := s.CreateThread(ctx, thFull); err != nil {
		t.Fatalf("CreateThread full 失败: %v", err)
	}

	gotFull, err := s.GetThread(ctx, "t-full")
	if err != nil {
		t.Fatalf("GetThread full 失败: %v", err)
	}
	if !gotFull.IsDormant || gotFull.DormantAt == nil || !gotFull.DormantAt.Equal(dormantAt) {
		t.Errorf("IsDormant/DormantAt 往返不匹配: %v, %v", gotFull.IsDormant, gotFull.DormantAt)
	}
	if !gotFull.IsProvisional || gotFull.WorktreePath != "/tmp/worktree" || gotFull.Branch != "feature/branch" || !gotFull.WorktreeRequested {
		t.Errorf("Worktree 字段往返不匹配: %+v", gotFull)
	}
	if gotFull.SwarmPairID != "pair-123" || gotFull.SwarmMode != "lead" || gotFull.ParentThreadID != "t-default" || !gotFull.IsHelper || !gotFull.HasUnread {
		t.Errorf("Swarm 字段往返不匹配: %+v", gotFull)
	}
	if gotFull.LastStatus != "completed" || gotFull.ProviderResumeAt != "resume-tok" || gotFull.ProviderAnchor != "anchor-hash" {
		t.Errorf("Provider 字段往返不匹配: %+v", gotFull)
	}

	// 3. 通过 ListThreads 读出验证
	list, err := s.ListThreads(ctx, "p1", false)
	if err != nil {
		t.Fatalf("ListThreads 失败: %v", err)
	}
	var foundFull bool
	for _, th := range list {
		if th.ID == "t-full" {
			foundFull = true
			if !th.IsDormant || th.DormantAt == nil || !th.DormantAt.Equal(dormantAt) || th.SwarmMode != "lead" {
				t.Errorf("ListThreads 读出的字段不匹配: %+v", th)
			}
		}
	}
	if !foundFull {
		t.Fatalf("ListThreads 未能找到 t-full")
	}
}

// TestTurnsCRUDAndRecent 验证 turns 的 CRUD、排序与 ListRecentTurns 懒加载。
func TestTurnsCRUDAndRecent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "turns_crud.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	if err := s.UpsertProject(ctx, Project{ID: "p1", Name: "Project 1", Path: "/path/p1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}
	if err := s.CreateThread(ctx, Thread{ID: "t1", ProjectID: "p1", Title: "Thread 1", Provider: "anthropic", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}

	// 1. CreateTurn 与 GetTurn
	t1Started := now.Truncate(time.Millisecond)
	turn1 := Turn{
		ID:                    "turn-1",
		ThreadID:              "t1",
		TurnIndex:             1,
		Status:                "running",
		StartedAt:             t1Started,
		UserPrompt:            "Build feature X",
		SystemInjectedContext: "context injected",
		RawOutput:             "starting run",
		TouchedFiles:          []string{"main.go", "config.go"},
		InputTokens:           1200,
		OutputTokens:          400,
		CostMicros:            1500,
	}
	if err := s.CreateTurn(ctx, turn1); err != nil {
		t.Fatalf("CreateTurn 失败: %v", err)
	}

	got1, err := s.GetTurn(ctx, "turn-1")
	if err != nil {
		t.Fatalf("GetTurn 失败: %v", err)
	}
	if got1.UserPrompt != "Build feature X" || len(got1.TouchedFiles) != 2 || got1.TouchedFiles[0] != "main.go" {
		t.Errorf("GetTurn 初始字段不匹配: %+v", got1)
	}
	if got1.CompletedAt != nil {
		t.Errorf("初始 CompletedAt 期望为 nil")
	}

	// 2. UpdateTurn（Status/CompletedAt/TouchedFiles）
	t1Completed := t1Started.Add(10 * time.Second)
	got1.Status = "completed"
	got1.CompletedAt = &t1Completed
	got1.TouchedFiles = []string{"main.go", "config.go", "utils.go"}
	got1.FinishCheckpoint = "commit-sha-xyz"
	if err := s.UpdateTurn(ctx, got1); err != nil {
		t.Fatalf("UpdateTurn 失败: %v", err)
	}

	updated1, err := s.GetTurn(ctx, "turn-1")
	if err != nil {
		t.Fatalf("GetTurn 读回更新结果失败: %v", err)
	}
	if updated1.Status != "completed" || updated1.CompletedAt == nil || !updated1.CompletedAt.Equal(t1Completed) {
		t.Errorf("UpdateTurn Status/CompletedAt 未正确更新: %+v", updated1)
	}
	if len(updated1.TouchedFiles) != 3 || updated1.TouchedFiles[2] != "utils.go" {
		t.Errorf("UpdateTurn TouchedFiles 未正确更新: %+v", updated1.TouchedFiles)
	}

	// 更新不存在的轮次应返回 ErrNotFound
	if err := s.UpdateTurn(ctx, Turn{ID: "non-existent-turn"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("更新不存在的轮次期望 ErrNotFound，实际为: %v", err)
	}

	// 3. 追加更多轮次，测试 ListTurns 升序与 ListRecentTurns limit
	for idx := 2; idx <= 5; idx++ {
		tIdx := Turn{
			ID:         fmt.Sprintf("turn-%d", idx),
			ThreadID:   "t1",
			TurnIndex:  idx,
			Status:     "completed",
			StartedAt:  t1Started.Add(time.Duration(idx) * time.Minute),
			UserPrompt: fmt.Sprintf("Prompt %d", idx),
		}
		if err := s.CreateTurn(ctx, tIdx); err != nil {
			t.Fatalf("CreateTurn turn-%d 失败: %v", idx, err)
		}
	}

	// ListTurns：应按 turn_index 升序返回 1..5
	allTurns, err := s.ListTurns(ctx, "t1")
	if err != nil {
		t.Fatalf("ListTurns 失败: %v", err)
	}
	if len(allTurns) != 5 {
		t.Fatalf("期望 5 轮，实际 %d", len(allTurns))
	}
	for i, trn := range allTurns {
		if trn.TurnIndex != i+1 {
			t.Errorf("期望第 %d 项 turn_index 为 %d，实际为 %d", i, i+1, trn.TurnIndex)
		}
	}

	// ListRecentTurns：取最近 3 轮，结果仍按 turn_index 升序（3, 4, 5）
	recentTurns, err := s.ListRecentTurns(ctx, "t1", 3)
	if err != nil {
		t.Fatalf("ListRecentTurns 失败: %v", err)
	}
	if len(recentTurns) != 3 {
		t.Fatalf("期望 3 轮，实际 %d", len(recentTurns))
	}
	if recentTurns[0].TurnIndex != 3 || recentTurns[1].TurnIndex != 4 || recentTurns[2].TurnIndex != 5 {
		t.Errorf("ListRecentTurns 排序或结果不符合预期: %+v", recentTurns)
	}

	// limit 超出总数时应返回全部 5 轮
	recentAll, err := s.ListRecentTurns(ctx, "t1", 10)
	if err != nil || len(recentAll) != 5 {
		t.Errorf("ListRecentTurns 大 limit 期望返回 5 轮，实际 %d", len(recentAll))
	}

	// limit <= 0 应返回空切片
	recentZero, err := s.ListRecentTurns(ctx, "t1", 0)
	if err != nil || len(recentZero) != 0 {
		t.Errorf("ListRecentTurns limit<=0 期望返回空切片，实际 %v", recentZero)
	}
}

// TestTurnsCascadeDelete 验证级联删除：删除线程后 turns 记录自动清空。
func TestTurnsCascadeDelete(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "turns_cascade.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	if err := s.UpsertProject(ctx, Project{ID: "p1", Name: "Project 1", Path: "/path/p1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}
	if err := s.CreateThread(ctx, Thread{ID: "t-cascade", ProjectID: "p1", Title: "Cascade Thread", Provider: "anthropic", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}

	for idx := 1; idx <= 3; idx++ {
		turn := Turn{
			ID:         fmt.Sprintf("turn-cas-%d", idx),
			ThreadID:   "t-cascade",
			TurnIndex:  idx,
			Status:     "completed",
			StartedAt:  now,
			UserPrompt: fmt.Sprintf("Prompt %d", idx),
		}
		if err := s.CreateTurn(ctx, turn); err != nil {
			t.Fatalf("CreateTurn 失败: %v", err)
		}
	}

	// 确认 turns 记录存在
	turnsBefore, err := s.ListTurns(ctx, "t-cascade")
	if err != nil || len(turnsBefore) != 3 {
		t.Fatalf("期望存在 3 条 turns 记录，实际为 %d", len(turnsBefore))
	}

	// 调用现有 DeleteThread 删除线程
	if err := s.DeleteThread(ctx, "t-cascade"); err != nil {
		t.Fatalf("DeleteThread 失败: %v", err)
	}

	// 验证外键级联删除：ListTurns 返回空切片
	turnsAfter, err := s.ListTurns(ctx, "t-cascade")
	if err != nil {
		t.Fatalf("ListTurns 查询失败: %v", err)
	}
	if len(turnsAfter) != 0 {
		t.Fatalf("期望级联删除后 turns 为空，实际仍有 %d 条", len(turnsAfter))
	}

	// GetTurn 查单条应返回 ErrNotFound
	_, err = s.GetTurn(ctx, "turn-cas-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 GetTurn 返回 ErrNotFound，实际为: %v", err)
	}
}

// TestTurnUsageWindowCostRoundTrip 验证上下文窗口上限与本轮费用能往返读写：
// 写入后按原值读回，同一轮覆盖写入 0 时读回 0（provider 未上报，界面回退到模型目录且不显示费用）。
func TestTurnUsageWindowCostRoundTrip(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "usage_window_cost.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	if err := s.UpsertProject(ctx, Project{ID: "p1", Name: "P1", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}
	if err := s.CreateThread(ctx, Thread{ID: "t1", ProjectID: "p1", Title: "T1", Provider: "claude", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}

	if err := s.UpsertTurnUsage(ctx, TurnUsage{
		ThreadID:      "t1",
		TurnIndex:     0,
		InputTokens:   1200,
		OutputTokens:  80,
		ContextTokens: 1200,
		ContextWindow: 200000,
		CostUSD:       0.0123,
		RequestsJSON:  `[{"output_tokens":40,"duration_ms":2000},{"output_tokens":40,"duration_ms":3000}]`,
	}); err != nil {
		t.Fatalf("UpsertTurnUsage 失败: %v", err)
	}

	list, err := s.ListTurnUsage(ctx, "t1")
	if err != nil {
		t.Fatalf("ListTurnUsage 失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("期望 1 条用量记录，实际 %d 条", len(list))
	}
	if list[0].ContextWindow != 200000 {
		t.Fatalf("ContextWindow 期望 200000，实际 %d", list[0].ContextWindow)
	}
	if diff := list[0].CostUSD - 0.0123; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("CostUSD 期望 0.0123，实际 %v", list[0].CostUSD)
	}
	if list[0].RequestsJSON != `[{"output_tokens":40,"duration_ms":2000},{"output_tokens":40,"duration_ms":3000}]` {
		t.Fatalf("RequestsJSON 读回不一致: %s", list[0].RequestsJSON)
	}

	// provider 下一轮不再上报时覆盖写入 0，读回也应是 0。
	if err := s.UpsertTurnUsage(ctx, TurnUsage{
		ThreadID:     "t1",
		TurnIndex:    0,
		InputTokens:  1200,
		OutputTokens: 80,
	}); err != nil {
		t.Fatalf("UpsertTurnUsage 覆盖失败: %v", err)
	}
	list, err = s.ListTurnUsage(ctx, "t1")
	if err != nil {
		t.Fatalf("ListTurnUsage 覆盖后失败: %v", err)
	}
	if list[0].ContextWindow != 0 || list[0].CostUSD != 0 {
		t.Fatalf("覆盖后期望读回 0，实际 window=%d cost=%v", list[0].ContextWindow, list[0].CostUSD)
	}
	if list[0].RequestsJSON != "" {
		t.Fatalf("覆盖后期望 RequestsJSON 读回空串，实际 %s", list[0].RequestsJSON)
	}
}

// TestTurnUsageWindowCostMigrationFromOldDB 模拟缺列的旧库：打开时迁移补上两列，
// 历史行读回 0（与零值路径一致），补列后新写入的窗口上限与费用能正常读回。
func TestTurnUsageWindowCostMigrationFromOldDB(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "usage_legacy.db")

	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("打开 rawDB 失败: %v", err)
	}

	// 只跑到 context_window 迁移之前的版本，模拟还没上报过窗口与费用的旧库。
	oldVersion := -1
	for i, m := range migrations {
		if strings.Contains(m, "ADD COLUMN context_window") {
			oldVersion = i
			break
		}
	}
	if oldVersion < 0 {
		t.Fatal("未找到包含 ADD COLUMN context_window 的迁移")
	}
	oldStore := &Store{db: rawDB}
	for i := 0; i < oldVersion; i++ {
		if err := oldStore.applyMigration(ctx, migrations[i], i+1); err != nil {
			t.Fatalf("执行旧迁移 v%d 失败: %v", i+1, err)
		}
	}

	now := time.Now().UTC()
	if _, err := rawDB.ExecContext(ctx, `
		INSERT INTO projects (id, name, path, created_at, updated_at)
		VALUES ('old-project', '旧项目', ?, ?, ?)
	`, t.TempDir(), formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.ExecContext(ctx, `
		INSERT INTO threads (id, project_id, title, provider, created_at, updated_at)
		VALUES ('old-thread', 'old-project', '旧会话', 'claude', ?, ?)
	`, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.ExecContext(ctx, `
		INSERT INTO turn_usage (thread_id, turn_index, input_tokens, output_tokens, cached_tokens, updated_at)
		VALUES ('old-thread', 0, 100, 20, 0, ?)
	`, formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("关闭 rawDB 失败: %v", err)
	}

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("重新 Open 失败: %v", err)
	}
	defer s.Close()

	// 两列都在 migration 里补齐了。
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info(turn_usage);")
	if err != nil {
		t.Fatalf("PRAGMA table_info(turn_usage) 失败: %v", err)
	}
	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		cols[name] = true
	}
	rows.Close()
	for _, col := range []string{"context_window", "cost_usd"} {
		if !cols[col] {
			t.Errorf("turn_usage 表缺少字段: %s", col)
		}
	}

	// 历史行读回 0，界面据此回退到模型目录里的静态窗口并且不显示费用。
	list, err := s.ListTurnUsage(ctx, "old-thread")
	if err != nil {
		t.Fatalf("ListTurnUsage 失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("期望 1 条历史用量，实际 %d 条", len(list))
	}
	if list[0].ContextWindow != 0 || list[0].CostUSD != 0 {
		t.Fatalf("旧库历史行期望读回 0，实际 window=%d cost=%v", list[0].ContextWindow, list[0].CostUSD)
	}
	if err := s.UpsertTurnUsage(ctx, TurnUsage{
		ThreadID:      "old-thread",
		TurnIndex:     0,
		InputTokens:   100,
		OutputTokens:  20,
		ContextWindow: 200000,
		CostUSD:       0.5,
	}); err != nil {
		t.Fatalf("补列后 UpsertTurnUsage 失败: %v", err)
	}
	list, err = s.ListTurnUsage(ctx, "old-thread")
	if err != nil {
		t.Fatalf("补列后 ListTurnUsage 失败: %v", err)
	}
	if list[0].ContextWindow != 200000 || list[0].CostUSD != 0.5 {
		t.Fatalf("补列后写入未生效: %+v", list[0])
	}
}

// ---- 崩溃收尾：FinalizeInterruptedTurn / ListOrphanRunningToolMessages ----

// newCrashRecoveryStore 建一个带项目和线程的临时库，供崩溃收尾测试共用。
func newCrashRecoveryStore(t *testing.T) (*Store, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "crash_recovery.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	now := time.Now().UTC()
	if err := s.UpsertProject(ctx, Project{ID: "p1", Name: "P1", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}
	if err := s.CreateThread(ctx, Thread{
		ID: "t1", ProjectID: "p1", Title: "收尾", Provider: "anthropic",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	return s, "t1"
}

func createRecoveryTurn(t *testing.T, s *Store, threadID, turnID, status string, index int) {
	t.Helper()
	if err := s.CreateTurn(context.Background(), Turn{
		ID: turnID, ThreadID: threadID, TurnIndex: index, Status: status,
		StartedAt: time.Now().UTC(), UserPrompt: "提示",
	}); err != nil {
		t.Fatalf("CreateTurn %s 失败: %v", turnID, err)
	}
}

func appendRunningToolMessage(t *testing.T, s *Store, threadID, id, turnID, text string, index int) {
	t.Helper()
	status := "running"
	if _, err := s.AppendMessage(context.Background(), Message{
		ID: id, ThreadID: threadID, Role: "tool", Kind: "tool", Text: text,
		TurnIndex: index, TurnID: turnID, ToolName: strPtr("bash"), ToolStatus: &status,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AppendMessage %s 失败: %v", id, err)
	}
}

func toolsByID(t *testing.T, s *Store, threadID string) map[string]Message {
	t.Helper()
	msgs, _, err := s.ListMessages(context.Background(), threadID, 0, 100)
	if err != nil {
		t.Fatalf("ListMessages 失败: %v", err)
	}
	out := make(map[string]Message)
	for _, m := range msgs {
		if m.Role == "tool" {
			out[m.ID] = m
		}
	}
	return out
}

func TestListOrphanRunningToolMessagesIgnoresTurnStatus(t *testing.T) {
	s, threadID := newCrashRecoveryStore(t)
	ctx := context.Background()

	createRecoveryTurn(t, s, threadID, "turn-run", "running", 0)
	createRecoveryTurn(t, s, threadID, "turn-int", "interrupted", 1)
	createRecoveryTurn(t, s, threadID, "turn-done", "completed", 2)
	appendRunningToolMessage(t, s, threadID, "tool-run", "turn-run", "还在跑", 0)
	appendRunningToolMessage(t, s, threadID, "tool-int", "turn-int", "遗留 running", 1)

	completed := "completed"
	if _, err := s.AppendMessage(ctx, Message{
		ID: "tool-done", ThreadID: threadID, Role: "tool", Kind: "tool", Text: "已完成",
		TurnIndex: 2, TurnID: "turn-done", ToolName: strPtr("bash"), ToolStatus: &completed,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AppendMessage tool-done 失败: %v", err)
	}

	orphans, err := s.ListOrphanRunningToolMessages(ctx, nil)
	if err != nil {
		t.Fatalf("ListOrphanRunningToolMessages 失败: %v", err)
	}
	gotIDs := map[string]bool{}
	for _, m := range orphans {
		gotIDs[m.ID] = true
		if m.ToolStatus == nil || *m.ToolStatus != "running" {
			t.Fatalf("孤儿工具消息状态应保持 running: %+v", m)
		}
		if m.Text == "" {
			t.Fatalf("孤儿工具消息正文未解压: %+v", m)
		}
	}
	if len(orphans) != 2 || !gotIDs["tool-run"] || !gotIDs["tool-int"] {
		t.Fatalf("孤儿工具消息 = %+v，期望 tool-run 与 tool-int（不看 turn 状态）", orphans)
	}

	// 当前正在执行的轮次由 activeTurnIDs 排除。
	filtered, err := s.ListOrphanRunningToolMessages(ctx, map[string]struct{}{"turn-run": {}})
	if err != nil {
		t.Fatalf("ListOrphanRunningToolMessages(active) 失败: %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != "tool-int" {
		t.Fatalf("排除 active 后孤儿工具消息 = %+v，期望只剩 tool-int", filtered)
	}
}

func TestFinalizeInterruptedTurnRollsBackMessagesOnFailure(t *testing.T) {
	s, threadID := newCrashRecoveryStore(t)
	ctx := context.Background()

	createRecoveryTurn(t, s, threadID, "turn-1", "running", 0)
	appendRunningToolMessage(t, s, threadID, "msg1", "turn-1", "第一个工具", 0)
	appendRunningToolMessage(t, s, threadID, "msg2", "turn-1", "第二个工具", 0)

	if _, err := s.db.ExecContext(ctx, `
		CREATE TRIGGER fail_second_tool BEFORE UPDATE ON messages
		WHEN NEW.id = 'msg2'
		BEGIN
			SELECT RAISE(ABORT, 'injected failure');
		END;
	`); err != nil {
		t.Fatalf("创建触发器失败: %v", err)
	}

	notice := "工具执行被中断：宿主重启，未收到结果。"
	err := s.FinalizeInterruptedTurn(ctx, "turn-1", time.Now().UTC(), time.Now().UTC(), "宿主重启", notice)
	if err == nil {
		t.Fatal("期望事务失败，实际成功")
	}

	turn, err := s.GetTurn(ctx, "turn-1")
	if err != nil {
		t.Fatalf("GetTurn 失败: %v", err)
	}
	if turn.Status != "running" {
		t.Fatalf("失败后 turn 状态 = %q，期望保持 running", turn.Status)
	}
	tools := toolsByID(t, s, threadID)
	for _, id := range []string{"msg1", "msg2"} {
		m := tools[id]
		if m.ToolStatus == nil || *m.ToolStatus != "running" {
			t.Fatalf("失败后 %s 状态应回滚为 running: %+v", id, m)
		}
		if strings.Contains(m.Text, notice) {
			t.Fatalf("失败后 %s 不应保留中断说明: %q", id, m.Text)
		}
	}

	if _, err := s.db.ExecContext(ctx, `DROP TRIGGER fail_second_tool;`); err != nil {
		t.Fatalf("删除触发器失败: %v", err)
	}
	if err := s.FinalizeInterruptedTurn(ctx, "turn-1", time.Now().UTC(), time.Now().UTC(), "宿主重启", notice); err != nil {
		t.Fatalf("重试失败: %v", err)
	}

	turn, err = s.GetTurn(ctx, "turn-1")
	if err != nil {
		t.Fatalf("重试后 GetTurn 失败: %v", err)
	}
	if turn.Status != "interrupted" || turn.CompletedAt == nil {
		t.Fatalf("重试后 turn 未收尾: %+v", turn)
	}
	tools = toolsByID(t, s, threadID)
	for _, id := range []string{"msg1", "msg2"} {
		m := tools[id]
		if m.ToolStatus == nil || *m.ToolStatus != "failed" || m.ToolCompletedAt == nil {
			t.Fatalf("重试后 %s 未置为 failed: %+v", id, m)
		}
		if !strings.Contains(m.Text, notice) {
			t.Fatalf("重试后 %s 缺少中断说明: %q", id, m.Text)
		}
	}
}

func TestFinalizeInterruptedTurnIsIdempotent(t *testing.T) {
	s, threadID := newCrashRecoveryStore(t)
	ctx := context.Background()

	createRecoveryTurn(t, s, threadID, "turn-1", "running", 0)
	appendRunningToolMessage(t, s, threadID, "msg1", "turn-1", "工具正文", 0)

	notice := "工具执行被中断：宿主重启，未收到结果。"
	first := time.Now().UTC().Add(-time.Minute)
	if err := s.FinalizeInterruptedTurn(ctx, "turn-1", first, first, "第一次", notice); err != nil {
		t.Fatalf("首次收尾失败: %v", err)
	}
	afterFirst := toolsByID(t, s, threadID)["msg1"]

	if err := s.FinalizeInterruptedTurn(ctx, "turn-1", time.Now().UTC(), time.Now().UTC(), "第二次", notice); err != nil {
		t.Fatalf("重复收尾返回错误: %v", err)
	}
	afterSecond := toolsByID(t, s, threadID)["msg1"]

	if afterSecond.ToolStatus == nil || *afterSecond.ToolStatus != "failed" {
		t.Fatalf("重复收尾后状态 = %+v，期望 failed", afterSecond.ToolStatus)
	}
	if afterFirst.ToolCompletedAt == nil || afterSecond.ToolCompletedAt == nil || *afterFirst.ToolCompletedAt != *afterSecond.ToolCompletedAt {
		t.Fatalf("重复收尾改动了完成时间: %v -> %v", afterFirst.ToolCompletedAt, afterSecond.ToolCompletedAt)
	}
	if got := strings.Count(afterSecond.Text, notice); got != 1 {
		t.Fatalf("重复收尾后中断说明出现 %d 次，期望 1: %q", got, afterSecond.Text)
	}
	turn, err := s.GetTurn(ctx, "turn-1")
	if err != nil {
		t.Fatalf("GetTurn 失败: %v", err)
	}
	if turn.Status != "interrupted" {
		t.Fatalf("turn 状态 = %q，期望 interrupted", turn.Status)
	}
}

// TestFinalizeBackgroundToolMessagesConvergesBackgroundOnly 验证后台任务单独收敛：
// background 属于已结束的回合（FinalizeInterruptedTurn 只看 running，不会碰它），
// 启动时要把它们改成 failed 并追加说明，已完成的工具消息保持不变。
func TestFinalizeBackgroundToolMessagesConvergesBackgroundOnly(t *testing.T) {
	s, threadID := newCrashRecoveryStore(t)
	ctx := context.Background()

	createRecoveryTurn(t, s, threadID, "turn-done", "completed", 0)
	background := "background"
	if _, err := s.AppendMessage(ctx, Message{
		ID: "tool-bg", ThreadID: threadID, Role: "tool", Kind: "tool", Text: "后台命令",
		TurnIndex: 0, TurnID: "turn-done", ToolName: strPtr("bash"), ToolStatus: &background,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AppendMessage tool-bg 失败: %v", err)
	}
	completed := "completed"
	if _, err := s.AppendMessage(ctx, Message{
		ID: "tool-done", ThreadID: threadID, Role: "tool", Kind: "tool", Text: "已完成的工具",
		TurnIndex: 0, TurnID: "turn-done", ToolName: strPtr("bash"), ToolStatus: &completed,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AppendMessage tool-done 失败: %v", err)
	}

	notice := "宿主重启，后台任务已中断，输出未收回。"
	n, err := s.FinalizeBackgroundToolMessages(ctx, time.Now().UTC(), notice)
	if err != nil {
		t.Fatalf("FinalizeBackgroundToolMessages 失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("处理条数 = %d，期望 1", n)
	}

	tools := toolsByID(t, s, threadID)
	bg := tools["tool-bg"]
	if bg.ToolStatus == nil || *bg.ToolStatus != "failed" || bg.ToolCompletedAt == nil {
		t.Fatalf("后台任务未收敛为 failed: %+v", bg)
	}
	if !strings.Contains(bg.Text, notice) {
		t.Fatalf("后台任务缺少中断说明: %q", bg.Text)
	}
	done := tools["tool-done"]
	if done.ToolStatus == nil || *done.ToolStatus != "completed" || done.ToolCompletedAt != nil {
		t.Fatalf("已完成的工具消息被改动: %+v", done)
	}
	if strings.Contains(done.Text, notice) {
		t.Fatalf("已完成的工具消息不应被追加说明: %q", done.Text)
	}

	// 幂等：没有 background 行后再次调用返回 0，且不改动已有终态。
	n, err = s.FinalizeBackgroundToolMessages(ctx, time.Now().UTC(), notice)
	if err != nil {
		t.Fatalf("重复调用失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("重复调用处理条数 = %d，期望 0", n)
	}
}
