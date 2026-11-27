package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAutomationTxStartRunAtomic(t *testing.T) {
	ctx := context.Background()
	s := OpenForTest(t)

	now := time.Now().UTC().Truncate(time.Millisecond)
	nextRun := now.Add(5 * time.Minute)
	auto := Automation{
		ID:        "auto-tx-start",
		Name:      "事务触发测试",
		Prompt:    "检查状态",
		Schedule:  `{"type":"interval","seconds":300}`,
		Enabled:   true,
		Status:    "active",
		NextRunAt: &nextRun,
	}
	if err := s.CreateAutomation(ctx, auto); err != nil {
		t.Fatalf("CreateAutomation failed: %v", err)
	}
	got, err := s.GetAutomation(ctx, auto.ID)
	if err != nil {
		t.Fatalf("GetAutomation failed: %v", err)
	}

	// 1. 过期 expectedRevision 时返回 ErrRevisionConflict，且不插入运行记录。
	mod := got
	mod.Name = "不应写入"
	mod.LastRunAt = &now
	staleRun := AutomationRun{
		ID:           "run-tx-stale",
		AutomationID: auto.ID,
		Status:       "running",
		Trigger:      "scheduled",
		StartedAt:    now,
	}
	if _, err := s.StartAutomationRun(ctx, mod, got.Revision-1, staleRun); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict, got: %v", err)
	}
	if _, err := s.GetAutomationRun(ctx, staleRun.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected run not inserted on revision conflict, got: %v", err)
	}

	// 2. 重复运行 ID 时返回错误，且任务的 revision 和字段均不变。
	existingRun := AutomationRun{
		ID:           "run-tx-dup",
		AutomationID: auto.ID,
		Status:       "running",
		Trigger:      "scheduled",
		StartedAt:    now,
	}
	if err := s.CreateAutomationRun(ctx, existingRun); err != nil {
		t.Fatalf("CreateAutomationRun failed: %v", err)
	}
	if _, err := s.StartAutomationRun(ctx, mod, got.Revision, existingRun); err == nil {
		t.Fatalf("expected error on duplicate run ID, got nil")
	}
	afterFail, err := s.GetAutomation(ctx, auto.ID)
	if err != nil {
		t.Fatalf("GetAutomation after rollback failed: %v", err)
	}
	if afterFail.Revision != got.Revision || afterFail.Name != got.Name || afterFail.LastRunAt != nil {
		t.Fatalf("expected automation unchanged after rollback, got=%+v want=%+v", afterFail, got)
	}

	// 3. 正常情况两者都写入。
	okRun := AutomationRun{
		ID:           "run-tx-ok",
		AutomationID: auto.ID,
		Status:       "running",
		Trigger:      "scheduled",
		StartedAt:    now,
	}
	updatedAuto, err := s.StartAutomationRun(ctx, mod, got.Revision, okRun)
	if err != nil {
		t.Fatalf("StartAutomationRun failed: %v", err)
	}
	if updatedAuto.Revision != got.Revision+1 || updatedAuto.Name != mod.Name {
		t.Fatalf("unexpected updated automation: %+v", updatedAuto)
	}
	persistedAuto, err := s.GetAutomation(ctx, auto.ID)
	if err != nil {
		t.Fatalf("GetAutomation failed: %v", err)
	}
	if persistedAuto.Revision != got.Revision+1 || persistedAuto.Name != mod.Name || persistedAuto.LastRunAt == nil || !persistedAuto.LastRunAt.Equal(now) {
		t.Fatalf("persisted automation mismatch: %+v", persistedAuto)
	}
	persistedRun, err := s.GetAutomationRun(ctx, okRun.ID)
	if err != nil {
		t.Fatalf("GetAutomationRun failed: %v", err)
	}
	if persistedRun.Status != "running" || persistedRun.Revision != 1 {
		t.Fatalf("persisted run mismatch: %+v", persistedRun)
	}
}

func TestAutomationTxSettleRunAtomic(t *testing.T) {
	ctx := context.Background()
	s := OpenForTest(t)

	now := time.Now().UTC().Truncate(time.Millisecond)
	nextRun := now.Add(5 * time.Minute)
	auto := Automation{
		ID:        "auto-tx-settle",
		Name:      "事务结算测试",
		Prompt:    "执行结算",
		Schedule:  `{"type":"once","runAt":"2026-11-27T12:00:00Z"}`,
		Enabled:   true,
		Status:    "active",
		NextRunAt: &nextRun,
	}
	if err := s.CreateAutomation(ctx, auto); err != nil {
		t.Fatalf("CreateAutomation failed: %v", err)
	}
	run := AutomationRun{
		ID:           "run-tx-settle",
		AutomationID: auto.ID,
		Status:       "running",
		Trigger:      "scheduled",
		StartedAt:    now,
	}
	if err := s.CreateAutomationRun(ctx, run); err != nil {
		t.Fatalf("CreateAutomationRun failed: %v", err)
	}

	gotAuto, err := s.GetAutomation(ctx, auto.ID)
	if err != nil {
		t.Fatalf("GetAutomation failed: %v", err)
	}
	gotRun, err := s.GetAutomationRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetAutomationRun failed: %v", err)
	}

	finishedAt := now.Add(time.Minute)
	summary := "完成"
	settledRun := gotRun
	settledRun.Status = "succeeded"
	settledRun.FinishedAt = &finishedAt
	settledRun.ResultSummary = &summary

	settledAuto := gotAuto
	settledAuto.Enabled = false
	settledAuto.NextRunAt = nil
	settledAuto.Status = "completed"

	// 1. 任务 revision 过期时返回冲突，运行记录仍是原状态和原 revision。
	if _, _, err := s.SettleAutomationRun(ctx, settledRun, gotRun.Revision, settledAuto, gotAuto.Revision-1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict on stale auto revision, got: %v", err)
	}
	checkRun, err := s.GetAutomationRun(ctx, gotRun.ID)
	if err != nil {
		t.Fatalf("GetAutomationRun failed: %v", err)
	}
	if checkRun.Status != "running" || checkRun.Revision != gotRun.Revision || checkRun.FinishedAt != nil {
		t.Fatalf("expected run unchanged on auto conflict, got: %+v", checkRun)
	}

	// 2. 运行 revision 过期时返回冲突，任务不变。
	if _, _, err := s.SettleAutomationRun(ctx, settledRun, gotRun.Revision-1, settledAuto, gotAuto.Revision); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict on stale run revision, got: %v", err)
	}
	checkAuto, err := s.GetAutomation(ctx, gotAuto.ID)
	if err != nil {
		t.Fatalf("GetAutomation failed: %v", err)
	}
	if !checkAuto.Enabled || checkAuto.Status != "active" || checkAuto.Revision != gotAuto.Revision {
		t.Fatalf("expected automation unchanged on run conflict, got: %+v", checkAuto)
	}

	// 3. 正常情况下两者都更新。
	resRun, resAuto, err := s.SettleAutomationRun(ctx, settledRun, gotRun.Revision, settledAuto, gotAuto.Revision)
	if err != nil {
		t.Fatalf("SettleAutomationRun failed: %v", err)
	}
	if resRun.Status != "succeeded" || resRun.Revision != gotRun.Revision+1 {
		t.Fatalf("unexpected returned run: %+v", resRun)
	}
	if resAuto.Enabled || resAuto.Status != "completed" || resAuto.Revision != gotAuto.Revision+1 {
		t.Fatalf("unexpected returned auto: %+v", resAuto)
	}
	persistedRun, err := s.GetAutomationRun(ctx, gotRun.ID)
	if err != nil {
		t.Fatalf("GetAutomationRun failed: %v", err)
	}
	if persistedRun.Status != "succeeded" || persistedRun.Revision != gotRun.Revision+1 || persistedRun.FinishedAt == nil || !persistedRun.FinishedAt.Equal(finishedAt) {
		t.Fatalf("persisted run mismatch: %+v", persistedRun)
	}
	persistedAuto, err := s.GetAutomation(ctx, gotAuto.ID)
	if err != nil {
		t.Fatalf("GetAutomation failed: %v", err)
	}
	if persistedAuto.Enabled || persistedAuto.Status != "completed" || persistedAuto.NextRunAt != nil || persistedAuto.Revision != gotAuto.Revision+1 {
		t.Fatalf("persisted auto mismatch: %+v", persistedAuto)
	}
}
