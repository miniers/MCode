package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAutomationCRUDAndJSON(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "automation_crud_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer s.Close()

	projID := "proj-auto-1"
	now := time.Now().UTC().Truncate(time.Millisecond)
	nextRun := now.Add(10 * time.Minute)
	lastRun := now.Add(-10 * time.Minute)

	auto := Automation{
		ID:        "auto-1",
		ProjectID: &projID,
		Name:      "每日代码检查",
		Prompt:    "执行静态分析并报告",
		Schedule:  `{"type":"daily","timeOfDay":"09:00","timezone":"Asia/Shanghai"}`,
		Enabled:   true,
		Status:    "active",
		NextRunAt: &nextRun,
		LastRunAt: &lastRun,
	}

	if err := s.CreateAutomation(ctx, auto); err != nil {
		t.Fatalf("创建 Automation 失败: %v", err)
	}

	got, err := s.GetAutomation(ctx, auto.ID)
	if err != nil {
		t.Fatalf("获取 Automation 失败: %v", err)
	}
	if got.ID != auto.ID || got.Name != auto.Name || got.Prompt != auto.Prompt || !got.Enabled || got.Status != "active" {
		t.Fatalf("Automation 字段不匹配: %+v", got)
	}
	if got.Revision != 1 {
		t.Fatalf("期望初始 revision 为 1，实际为 %d", got.Revision)
	}
	if got.NextRunAt == nil || !got.NextRunAt.Equal(nextRun) {
		t.Fatalf("NextRunAt 不匹配: got=%v, want=%v", got.NextRunAt, nextRun)
	}
	if got.LastRunAt == nil || !got.LastRunAt.Equal(lastRun) {
		t.Fatalf("LastRunAt 不匹配: got=%v, want=%v", got.LastRunAt, lastRun)
	}

	// List
	list, err := s.ListAutomations(ctx, projID)
	if err != nil {
		t.Fatalf("ListAutomations 失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("期望 1 个 automation，实际 %d", len(list))
	}

	// Delete
	if err := s.DeleteAutomation(ctx, auto.ID); err != nil {
		t.Fatalf("删除 Automation 失败: %v", err)
	}
	_, err = s.GetAutomation(ctx, auto.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后获取应返回 ErrNotFound，实际为: %v", err)
	}
}

func TestAutomationCAS(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "automation_cas_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer s.Close()

	auto := Automation{
		ID:       "auto-cas-1",
		Name:     "定时同步",
		Prompt:   "同步远端日志",
		Schedule: `{"type":"interval","seconds":300}`,
		Enabled:  true,
	}
	if err := s.CreateAutomation(ctx, auto); err != nil {
		t.Fatalf("创建 Automation 失败: %v", err)
	}

	// 正常 CAS 更新
	auto.Name = "定时同步 - 已改名"
	updated, err := s.UpdateAutomation(ctx, auto, 1)
	if err != nil {
		t.Fatalf("CAS 更新 Automation 失败: %v", err)
	}
	if updated.Revision != 2 {
		t.Fatalf("期望 revision 为 2，实际为 %d", updated.Revision)
	}

	// 用过期版本更新
	_, err = s.UpdateAutomation(ctx, auto, 1)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("期望 ErrRevisionConflict，实际为: %v", err)
	}

	// 不存在的 ID
	fake := auto
	fake.ID = "fake-auto"
	_, err = s.UpdateAutomation(ctx, fake, 1)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际为: %v", err)
	}
}

func TestListDueAutomations(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "automation_due_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	past := now.Add(-10 * time.Minute)
	future := now.Add(10 * time.Minute)

	// 应到期
	a1 := Automation{
		ID:        "due-1",
		Name:      "已到期且启用",
		Prompt:    "run now",
		Schedule:  `{"type":"interval","seconds":60}`,
		Enabled:   true,
		NextRunAt: &past,
	}
	// 未到期
	a2 := Automation{
		ID:        "due-2",
		Name:      "未来时间未到期",
		Prompt:    "run later",
		Schedule:  `{"type":"interval","seconds":60}`,
		Enabled:   true,
		NextRunAt: &future,
	}
	// 虽到期但未启用
	a3 := Automation{
		ID:        "due-3",
		Name:      "到期但已停用",
		Prompt:    "disabled",
		Schedule:  `{"type":"interval","seconds":60}`,
		Enabled:   false,
		NextRunAt: &past,
	}

	if err := s.CreateAutomation(ctx, a1); err != nil {
		t.Fatalf("创建 a1 失败: %v", err)
	}
	if err := s.CreateAutomation(ctx, a2); err != nil {
		t.Fatalf("创建 a2 失败: %v", err)
	}
	if err := s.CreateAutomation(ctx, a3); err != nil {
		t.Fatalf("创建 a3 失败: %v", err)
	}

	dues, err := s.ListDueAutomations(ctx, now)
	if err != nil {
		t.Fatalf("ListDueAutomations 失败: %v", err)
	}
	if len(dues) != 1 {
		t.Fatalf("期望过滤出 1 个到期配置，实际为 %d", len(dues))
	}
	if dues[0].ID != "due-1" {
		t.Fatalf("期望到期配置 ID 为 due-1，实际为 %s", dues[0].ID)
	}
}

func TestAutomationRunsCRUDAndCascade(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "automation_runs_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer s.Close()

	auto := Automation{
		ID:       "auto-parent",
		Name:     "自动化父实体",
		Prompt:   "测试 runs 级联",
		Schedule: `{"type":"interval","seconds":3600}`,
		Enabled:  true,
	}
	if err := s.CreateAutomation(ctx, auto); err != nil {
		t.Fatalf("创建父 Automation 失败: %v", err)
	}

	threadID := "thread-run-1"
	r1 := AutomationRun{
		ID:           "run-1",
		AutomationID: auto.ID,
		ThreadID:     &threadID,
		Status:       "running",
		StartedAt:    time.Now().UTC().Add(-5 * time.Minute),
	}
	r2 := AutomationRun{
		ID:           "run-2",
		AutomationID: auto.ID,
		Status:       "running",
		StartedAt:    time.Now().UTC(),
	}

	if err := s.CreateAutomationRun(ctx, r1); err != nil {
		t.Fatalf("创建 run-1 失败: %v", err)
	}
	if err := s.CreateAutomationRun(ctx, r2); err != nil {
		t.Fatalf("创建 run-2 失败: %v", err)
	}

	// 获取 Run 并更新 CAS
	gotRun, err := s.GetAutomationRun(ctx, r1.ID)
	if err != nil {
		t.Fatalf("获取 run-1 失败: %v", err)
	}
	if gotRun.Revision != 1 {
		t.Fatalf("期望 run 初始 revision 为 1，实际为 %d", gotRun.Revision)
	}

	summary := "run finished successfully"
	finAt := time.Now().UTC()
	gotRun.Status = "succeeded"
	gotRun.ResultSummary = &summary
	gotRun.FinishedAt = &finAt

	updatedRun, err := s.UpdateAutomationRun(ctx, gotRun, 1)
	if err != nil {
		t.Fatalf("更新 run-1 CAS 失败: %v", err)
	}
	if updatedRun.Revision != 2 {
		t.Fatalf("期望 run revision 自增为 2，实际为 %d", updatedRun.Revision)
	}

	// 冲突测试
	_, err = s.UpdateAutomationRun(ctx, gotRun, 1)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("期望 run 更新 ErrRevisionConflict，实际为: %v", err)
	}

	// List
	runs, err := s.ListAutomationRuns(ctx, auto.ID, 10)
	if err != nil {
		t.Fatalf("ListAutomationRuns 失败: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("期望 2 个 run 记录，实际为 %d", len(runs))
	}
	// 按 started_at 降序：最新的是 run-2
	if runs[0].ID != "run-2" {
		t.Fatalf("期望最新的运行记录排在最前，实际为: %s", runs[0].ID)
	}

	// 测试 limit
	limitedRuns, err := s.ListAutomationRuns(ctx, auto.ID, 1)
	if err != nil {
		t.Fatalf("ListAutomationRuns 带 limit 失败: %v", err)
	}
	if len(limitedRuns) != 1 {
		t.Fatalf("期望返回 1 个运行记录，实际为 %d", len(limitedRuns))
	}

	// 删除父 Automation，验证级联删除 runs
	if err := s.DeleteAutomation(ctx, auto.ID); err != nil {
		t.Fatalf("删除父 Automation 失败: %v", err)
	}
	runsAfterDelete, err := s.ListAutomationRuns(ctx, auto.ID, 10)
	if err != nil {
		t.Fatalf("删除后列出 runs 失败: %v", err)
	}
	if len(runsAfterDelete) != 0 {
		t.Fatalf("期望级联删除后 runs 记录为空，实际剩余 %d 个", len(runsAfterDelete))
	}
}

func TestAutomationRunChangedFiles(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "changed_files.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer s.Close()

	auto := Automation{ID: "auto-cf", Name: "cf", Prompt: "p", Schedule: `{"type":"interval","seconds":60}`, Enabled: true}
	if err := s.CreateAutomation(ctx, auto); err != nil {
		t.Fatalf("创建 Automation 失败: %v", err)
	}
	run := AutomationRun{ID: "run-cf", AutomationID: auto.ID, Status: "running", StartedAt: time.Now().UTC()}
	if err := s.CreateAutomationRun(ctx, run); err != nil {
		t.Fatalf("创建 run 失败: %v", err)
	}

	got, err := s.GetAutomationRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("获取 run 失败: %v", err)
	}
	if got.ChangedFiles != nil {
		t.Fatalf("期望初始 ChangedFiles 为 nil，实际为 %d", *got.ChangedFiles)
	}

	n := 3
	got.Status = "succeeded"
	got.ChangedFiles = &n
	if _, err := s.UpdateAutomationRun(ctx, got, got.Revision); err != nil {
		t.Fatalf("更新 run 失败: %v", err)
	}

	runs, err := s.ListAutomationRuns(ctx, auto.ID, 1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListAutomationRuns 失败: %v, %d", err, len(runs))
	}
	if runs[0].ChangedFiles == nil || *runs[0].ChangedFiles != 3 {
		t.Fatalf("期望 ChangedFiles=3，实际为 %v", runs[0].ChangedFiles)
	}
}

func TestAutomationRunReadAndArchiveState(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "run_read_archive.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer s.Close()

	projID := "proj-read"
	auto := Automation{ID: "auto-read", ProjectID: &projID, Name: "已读归档", Prompt: "p", Schedule: `{"type":"interval","seconds":60}`, Enabled: true}
	if err := s.CreateAutomation(ctx, auto); err != nil {
		t.Fatalf("创建 Automation 失败: %v", err)
	}
	base := time.Now().UTC().Add(-10 * time.Minute)
	for i, id := range []string{"run-a", "run-b", "run-c"} {
		run := AutomationRun{ID: id, AutomationID: auto.ID, Status: "succeeded", StartedAt: base.Add(time.Duration(i) * time.Minute)}
		if err := s.CreateAutomationRun(ctx, run); err != nil {
			t.Fatalf("创建运行记录 %s 失败: %v", id, err)
		}
	}

	// 新记录默认未读、未归档
	got, err := s.GetAutomationRun(ctx, "run-a")
	if err != nil {
		t.Fatalf("获取运行记录失败: %v", err)
	}
	if got.ReadAt != nil || got.Archived {
		t.Fatalf("新记录应为未读且未归档，实际 read_at=%v archived=%v", got.ReadAt, got.Archived)
	}
	unread, err := s.CountUnreadAutomationRuns(ctx, auto.ID)
	if err != nil || unread != 3 {
		t.Fatalf("期望 3 条未读，实际 %d（err=%v）", unread, err)
	}

	// 单条标记已读，并保留首次已读时间
	readAt := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.MarkAutomationRunRead(ctx, "run-a", readAt); err != nil {
		t.Fatalf("标记已读失败: %v", err)
	}
	got, err = s.GetAutomationRun(ctx, "run-a")
	if err != nil {
		t.Fatalf("获取运行记录失败: %v", err)
	}
	if got.ReadAt == nil || !got.ReadAt.Equal(readAt) {
		t.Fatalf("期望 read_at=%v，实际 %v", readAt, got.ReadAt)
	}
	later := readAt.Add(time.Hour)
	if err := s.MarkAutomationRunRead(ctx, "run-a", later); err != nil {
		t.Fatalf("重复标记已读应幂等，实际错误: %v", err)
	}
	got, _ = s.GetAutomationRun(ctx, "run-a")
	if got.ReadAt == nil || !got.ReadAt.Equal(readAt) {
		t.Fatalf("重复标记不应改写首次已读时间，实际 %v", got.ReadAt)
	}
	if err := s.MarkAutomationRunRead(ctx, "run-missing", readAt); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际: %v", err)
	}

	// 归档：默认列表不含归档记录
	if err := s.SetAutomationRunArchived(ctx, "run-b", true); err != nil {
		t.Fatalf("归档失败: %v", err)
	}
	runs, err := s.ListAutomationRuns(ctx, auto.ID, 10)
	if err != nil {
		t.Fatalf("ListAutomationRuns 失败: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("默认列表应排除归档记录，期望 2 条，实际 %d", len(runs))
	}
	all, err := s.ListAutomationRunsFiltered(ctx, auto.ID, 10, true)
	if err != nil {
		t.Fatalf("ListAutomationRunsFiltered 失败: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("包含归档时应为 3 条，实际 %d", len(all))
	}
	// 归档记录不计入未读数
	unread, err = s.CountUnreadAutomationRuns(ctx, auto.ID)
	if err != nil || unread != 1 {
		t.Fatalf("期望 1 条未读（归档的不计），实际 %d（err=%v）", unread, err)
	}
	if err := s.SetAutomationRunArchived(ctx, "run-missing", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际: %v", err)
	}

	// 取消归档后回到默认列表
	if err := s.SetAutomationRunArchived(ctx, "run-b", false); err != nil {
		t.Fatalf("取消归档失败: %v", err)
	}
	runs, _ = s.ListAutomationRuns(ctx, auto.ID, 10)
	if len(runs) != 3 {
		t.Fatalf("取消归档后应为 3 条，实际 %d", len(runs))
	}

	// 全部标为已读
	affected, err := s.MarkAutomationRunsRead(ctx, auto.ID, time.Now().UTC())
	if err != nil {
		t.Fatalf("全部标记已读失败: %v", err)
	}
	if affected != 2 {
		t.Fatalf("期望影响 2 条未读记录，实际 %d", affected)
	}
	unread, _ = s.CountUnreadAutomationRuns(ctx, auto.ID)
	if unread != 0 {
		t.Fatalf("全部标记已读后期望 0 条未读，实际 %d", unread)
	}

	// 按项目统计未读数
	otherProj := "proj-other"
	other := Automation{ID: "auto-other", ProjectID: &otherProj, Name: "另一个", Prompt: "p", Schedule: `{"type":"interval","seconds":60}`, Enabled: true}
	if err := s.CreateAutomation(ctx, other); err != nil {
		t.Fatalf("创建 Automation 失败: %v", err)
	}
	if err := s.CreateAutomationRun(ctx, AutomationRun{ID: "run-other", AutomationID: other.ID, Status: "failed", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("创建运行记录失败: %v", err)
	}
	counts, err := s.CountUnreadAutomationRunsByProject(ctx, otherProj)
	if err != nil {
		t.Fatalf("按项目统计未读失败: %v", err)
	}
	if len(counts) != 1 || counts[other.ID] != 1 {
		t.Fatalf("期望仅 %s 有 1 条未读，实际 %v", other.ID, counts)
	}
	if counts, _ = s.CountUnreadAutomationRunsByProject(ctx, projID); len(counts) != 0 {
		t.Fatalf("期望原项目无未读，实际 %v", counts)
	}
}

func TestAutomationRunReadBackfillForExistingRows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "run_read_backfill.db")

	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("打开 rawDB 失败: %v", err)
	}

	// 只迁移到 read_at 列加入之前的版本，模拟旧库
	oldVersion := -1
	for i, m := range migrations {
		if strings.Contains(m, "ADD COLUMN read_at") {
			oldVersion = i
			break
		}
	}
	if oldVersion < 0 {
		t.Fatal("未找到包含 ADD COLUMN read_at 的迁移")
	}
	oldStore := &Store{db: rawDB}
	for i := 0; i < oldVersion; i++ {
		if err := oldStore.applyMigration(ctx, migrations[i], i+1); err != nil {
			t.Fatalf("执行旧迁移 v%d 失败: %v", i+1, err)
		}
	}

	now := time.Now().UTC().Truncate(time.Second)
	if _, err := rawDB.ExecContext(ctx, `
		INSERT INTO automations (id, project_id, name, prompt, schedule, enabled, status, revision, created_at, updated_at)
		VALUES ('old-auto', 'old-proj', '旧自动化', 'p', '{"type":"interval","seconds":60}', 1, 'active', 1, ?, ?)
	`, formatTime(now), formatTime(now)); err != nil {
		t.Fatalf("插入旧 automation 失败: %v", err)
	}
	if _, err := rawDB.ExecContext(ctx, `
		INSERT INTO automation_runs (id, automation_id, status, started_at, revision)
		VALUES ('old-run', 'old-auto', 'succeeded', ?, 1)
	`, formatTime(now)); err != nil {
		t.Fatalf("插入旧运行记录失败: %v", err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("关闭 rawDB 失败: %v", err)
	}

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("重新 Open 失败: %v", err)
	}
	defer s.Close()

	// 历史记录在首次升级时被视为已读且未归档
	got, err := s.GetAutomationRun(ctx, "old-run")
	if err != nil {
		t.Fatalf("获取历史运行记录失败: %v", err)
	}
	if got.ReadAt == nil {
		t.Fatal("历史运行记录升级后应为已读")
	}
	if got.Archived {
		t.Fatal("历史运行记录升级后不应为已归档")
	}
	if unread, _ := s.CountUnreadAutomationRuns(ctx, "old-auto"); unread != 0 {
		t.Fatalf("历史记录升级后不应计入未读，实际 %d", unread)
	}

	// 升级后新建的记录仍然是未读
	if err := s.CreateAutomationRun(ctx, AutomationRun{ID: "new-run", AutomationID: "old-auto", Status: "running", StartedAt: now.Add(time.Minute)}); err != nil {
		t.Fatalf("升级后创建运行记录失败: %v", err)
	}
	fresh, err := s.GetAutomationRun(ctx, "new-run")
	if err != nil {
		t.Fatalf("获取新运行记录失败: %v", err)
	}
	if fresh.ReadAt != nil {
		t.Fatalf("升级后新建记录应为未读，实际 read_at=%v", fresh.ReadAt)
	}
	if unread, _ := s.CountUnreadAutomationRuns(ctx, "old-auto"); unread != 1 {
		t.Fatalf("期望 1 条未读，实际 %d", unread)
	}
}

func TestAutomationRunTriggerColumns(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "automation_run_trigger_test.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer s.Close()

	auto := Automation{
		ID:       "auto-trigger",
		Name:     "运行来源测试",
		Prompt:   "run",
		Schedule: `{"type":"interval","seconds":60}`,
		Enabled:  true,
	}
	if err := s.CreateAutomation(ctx, auto); err != nil {
		t.Fatalf("创建 Automation 失败: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	catchUp := AutomationRun{
		ID:           "run-catchup",
		AutomationID: auto.ID,
		Status:       "running",
		Trigger:      "catch_up",
		MissedCount:  2,
		StartedAt:    now,
	}
	if err := s.CreateAutomationRun(ctx, catchUp); err != nil {
		t.Fatalf("创建 catch_up 运行失败: %v", err)
	}
	got, err := s.GetAutomationRun(ctx, catchUp.ID)
	if err != nil {
		t.Fatalf("读取 catch_up 运行失败: %v", err)
	}
	if got.Trigger != "catch_up" || got.MissedCount != 2 {
		t.Fatalf("期望 trigger=catch_up、missed_count=2，实际为 %q、%d", got.Trigger, got.MissedCount)
	}

	// 模拟升级前的旧行：不写新列，读取时应回落到默认值。
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO automation_runs (id, automation_id, status, started_at) VALUES (?, ?, 'running', ?)`,
		"run-legacy", auto.ID, formatTime(now)); err != nil {
		t.Fatalf("插入旧格式运行失败: %v", err)
	}
	legacy, err := s.GetAutomationRun(ctx, "run-legacy")
	if err != nil {
		t.Fatalf("读取旧格式运行失败: %v", err)
	}
	if legacy.Trigger != "scheduled" || legacy.MissedCount != 0 {
		t.Fatalf("期望旧行默认 trigger=scheduled、missed_count=0，实际为 %q、%d", legacy.Trigger, legacy.MissedCount)
	}

	// 结算更新不应丢失运行来源与补跑计数。
	got.Status = "succeeded"
	finished := now.Add(time.Minute)
	got.FinishedAt = &finished
	updated, err := s.UpdateAutomationRun(ctx, got, got.Revision)
	if err != nil {
		t.Fatalf("结算运行失败: %v", err)
	}
	if updated.Trigger != "catch_up" || updated.MissedCount != 2 {
		t.Fatalf("结算后期望 trigger=catch_up、missed_count=2，实际为 %q、%d", updated.Trigger, updated.MissedCount)
	}

	// 空 trigger 写入时按 scheduled 归一。
	plain := AutomationRun{
		ID:           "run-plain",
		AutomationID: auto.ID,
		Status:       "running",
		StartedAt:    now,
	}
	if err := s.CreateAutomationRun(ctx, plain); err != nil {
		t.Fatalf("创建默认 trigger 运行失败: %v", err)
	}
	storedPlain, err := s.GetAutomationRun(ctx, plain.ID)
	if err != nil {
		t.Fatalf("读取默认 trigger 运行失败: %v", err)
	}
	if storedPlain.Trigger != "scheduled" || storedPlain.MissedCount != 0 {
		t.Fatalf("期望默认 trigger=scheduled、missed_count=0，实际为 %q、%d", storedPlain.Trigger, storedPlain.MissedCount)
	}
}

func TestAutomationRunTriggerColumnsMigration(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "automation_run_trigger_migration_test.db")

	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("第一次 Open 失败: %v", err)
	}
	rows, err := s1.db.QueryContext(ctx, "PRAGMA table_info(automation_runs);")
	if err != nil {
		t.Fatalf("查询表结构失败: %v", err)
	}
	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("扫描表结构失败: %v", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历表结构失败: %v", err)
	}
	_ = rows.Close()
	for _, name := range []string{"trigger", "missed_count"} {
		if !columns[name] {
			t.Fatalf("automation_runs 缺少列 %s", name)
		}
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	// 重复打开同一数据库不应重复执行迁移。
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("第二次 Open 失败: %v", err)
	}
	defer s2.Close()
	var version int
	if err := s2.db.QueryRowContext(ctx, "PRAGMA user_version;").Scan(&version); err != nil {
		t.Fatalf("查询 user_version 失败: %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("期望 user_version 为 %d，实际为 %d", len(migrations), version)
	}
}
