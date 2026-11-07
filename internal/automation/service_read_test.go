package automation

import (
	"context"
	"testing"
	"time"

	"mcode/internal/kanban"
)

// TestRunReadStateLifecycle 覆盖运行记录的未读/已读/归档：
// 运行结束后新记录未读，成功后仍是未读，失败记录同样保持未读直到用户查看。
func TestRunReadStateLifecycle(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	driver := newFakeDriver()
	svc, db := setupTestService(t, clock, driver)
	defer db.Close()

	var changeCount int
	svc.SetOnChange(func() { changeCount++ })

	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-read",
		Name:      "已读归档",
		Prompt:    "run",
		Spec:      Spec{Schedule: Schedule{Type: "interval", Seconds: 60}},
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("创建自动化失败: %v", err)
	}

	// 第一次运行：成功后仍为未读
	clock.Advance(60 * time.Second)
	svc.Tick(ctx)
	runs, err := svc.Runs(ctx, def.ID, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("期望 1 条运行记录，实际 %d（err=%v）", len(runs), err)
	}
	successRun := runs[0]
	driver.setThreadState(*successRun.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})
	svc.Tick(ctx)

	settled, err := svc.Runs(ctx, def.ID, 10)
	if err != nil || len(settled) != 1 {
		t.Fatalf("读取运行记录失败: %v, %d", err, len(settled))
	}
	if settled[0].Status != RunSucceeded {
		t.Fatalf("期望运行成功，实际 %s", settled[0].Status)
	}
	if settled[0].ReadAt != nil {
		t.Fatalf("运行结束后记录应为未读，实际 read_at=%v", settled[0].ReadAt)
	}

	// 第二次运行：结算为失败，同样保持未读
	clock.Advance(60 * time.Second)
	svc.Tick(ctx)
	runs, _ = svc.Runs(ctx, def.ID, 10)
	if len(runs) != 2 {
		t.Fatalf("期望 2 条运行记录，实际 %d", len(runs))
	}
	failedRun := runs[0]
	if err := svc.ReportRun(ctx, def.ID, failedRun.ID, *failedRun.ThreadID, RunFailed, "boom", "", false); err != nil {
		t.Fatalf("结算失败运行出错: %v", err)
	}
	failedRun, err = db.GetAutomationRun(ctx, failedRun.ID)
	if err != nil {
		t.Fatalf("读取失败运行出错: %v", err)
	}
	if failedRun.Status != RunFailed || failedRun.ReadAt != nil {
		t.Fatalf("失败运行为未读，实际 status=%s read_at=%v", failedRun.Status, failedRun.ReadAt)
	}

	counts, err := svc.UnreadCounts(ctx, "proj-read")
	if err != nil {
		t.Fatalf("统计未读数失败: %v", err)
	}
	if counts[def.ID] != 2 {
		t.Fatalf("期望 2 条未读，实际 %d", counts[def.ID])
	}

	// 单条标为已读
	before := changeCount
	if err := svc.MarkRunRead(ctx, failedRun.ID); err != nil {
		t.Fatalf("标记已读失败: %v", err)
	}
	if changeCount <= before {
		t.Fatal("标记已读应触发变更通知")
	}
	read, err := db.GetAutomationRun(ctx, failedRun.ID)
	if err != nil {
		t.Fatalf("读取运行记录失败: %v", err)
	}
	if read.ReadAt == nil {
		t.Fatal("期望运行记录已被标记为已读")
	}
	if err := svc.MarkRunRead(ctx, "run-missing"); err == nil {
		t.Fatal("不存在的运行记录应返回错误")
	}

	// 全部标为已读
	n, err := svc.MarkAllRunsRead(ctx, def.ID)
	if err != nil {
		t.Fatalf("全部标记已读失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("期望影响 1 条未读记录，实际 %d", n)
	}
	counts, _ = svc.UnreadCounts(ctx, "proj-read")
	if counts[def.ID] != 0 {
		t.Fatalf("全部标记已读后期望 0 条未读，实际 %d", counts[def.ID])
	}

	// 归档后默认列表不再返回，包含归档时仍可见
	if err := svc.ArchiveRun(ctx, failedRun.ID, true); err != nil {
		t.Fatalf("归档失败: %v", err)
	}
	runs, err = svc.Runs(ctx, def.ID, 10)
	if err != nil {
		t.Fatalf("读取运行记录失败: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("默认列表应只剩 1 条，实际 %d", len(runs))
	}
	filtered, err := svc.RunsFiltered(ctx, def.ID, 10, true)
	if err != nil {
		t.Fatalf("读取含归档的运行记录失败: %v", err)
	}
	if len(filtered) != 2 {
		t.Fatalf("包含归档时应为 2 条，实际 %d", len(filtered))
	}

	// 取消归档后回到默认列表
	if err := svc.ArchiveRun(ctx, failedRun.ID, false); err != nil {
		t.Fatalf("取消归档失败: %v", err)
	}
	runs, _ = svc.Runs(ctx, def.ID, 10)
	if len(runs) != 2 {
		t.Fatalf("取消归档后应为 2 条，实际 %d", len(runs))
	}

	// 归档记录不计入未读：将成功运行也归档后，未读数仍为 0
	if err := svc.ArchiveRun(ctx, successRun.ID, true); err != nil {
		t.Fatalf("归档失败: %v", err)
	}
	counts, err = svc.UnreadCounts(ctx, "proj-read")
	if err != nil {
		t.Fatalf("统计未读数失败: %v", err)
	}
	if counts[def.ID] != 0 {
		t.Fatalf("归档记录不应计入未读，实际 %d", counts[def.ID])
	}
}
