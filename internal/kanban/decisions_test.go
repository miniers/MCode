package kanban

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"mcode/internal/store"
)

func TestAnswerDecision(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   string
	}{
		{"needs_approval", "todo"},
		{"waiting", "todo"},
		{"done", "todo"},
		{"failed", "todo"},
		{"cancelled", "todo"},
		{"in_progress", "in_progress"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
			svc, db, _, _ := setupReceiptTestService(t, newPokeDriver(), true, false, func() time.Time { return now })
			threadID := "thread-1"
			task := store.Task{ID: "task-1", Title: "任务", Notes: "原描述", ThreadID: &threadID, Status: tc.status,
				Meta: EncodeTaskMeta(TaskMeta{DecisionID: "D-0001", WaitKind: "machine", WakeAt: "later", WaitCheck: "check", WaitBase: "base", Wakes: 2})}
			if err := db.CreateTask(ctx, task); err != nil {
				t.Fatal(err)
			}
			d, err := db.CreateStewardDecision(ctx, store.StewardDecision{TaskID: task.ID, Question: "继续吗"})
			if err != nil {
				t.Fatal(err)
			}
			answered, err := svc.AnswerDecision(ctx, d.ID, "继续", "user:desktop")
			if err != nil || answered.Status != store.DecisionStatusAnswered || answered.AnsweredAt == nil || !answered.AnsweredAt.Equal(now) {
				t.Fatalf("答卡结果: %+v, %v", answered, err)
			}
			got, err := db.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			meta, err := ParseTaskMeta(got.Meta)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want || got.ThreadID == nil || *got.ThreadID != threadID || !strings.Contains(got.Notes, "决策 D-0001：继续吗 → 继续") {
				t.Fatalf("任务: %+v", got)
			}
			if tc.status == "needs_approval" && (meta.GateOverride != "D-0001" || meta.DecisionID != "") {
				t.Fatalf("解锁元数据: %+v", meta)
			}
			if tc.status == "waiting" && (meta.WaitKind != "" || meta.WakeAt != "" || meta.WaitCheck != "" || meta.WaitBase != "" || meta.Wakes != 0) {
				t.Fatalf("等待元数据: %+v", meta)
			}
			reanswered, err := svc.AnswerDecision(ctx, d.ID, "停下", "user:desktop")
			if err != nil || reanswered.Answer != "停下" {
				t.Fatalf("窗口内改答: %+v, %v", reanswered, err)
			}
			again, err := db.GetTask(ctx, task.ID)
			if err != nil || !strings.Contains(again.Notes, "决策 D-0001：改答为 停下") || reflect.DeepEqual(got, again) {
				t.Fatalf("改答未记录到任务: %+v, %v", again, err)
			}
		})
	}
}

func TestAnswerDecisionReanswerWindow(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	current := now
	svc, db, _, _ := setupReceiptTestService(t, newPokeDriver(), true, false, func() time.Time { return current })
	if err := db.CreateTask(ctx, store.Task{ID: "task-reanswer", Title: "任务", Status: string(TaskStatusInProgress), Meta: EncodeTaskMeta(TaskMeta{})}); err != nil {
		t.Fatal(err)
	}
	d, err := db.CreateStewardDecision(ctx, store.StewardDecision{TaskID: "task-reanswer", Question: "继续吗"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AnswerDecision(ctx, d.ID, "A", "user"); err != nil {
		t.Fatal(err)
	}
	current = now.Add(time.Minute)
	answered, err := svc.AnswerDecision(ctx, d.ID, "B", "user")
	if err != nil || answered.Answer != "B" || answered.AnsweredAt == nil || !answered.AnsweredAt.Equal(current) {
		t.Fatalf("五分钟内改答: %+v, %v", answered, err)
	}
	task, err := db.GetTask(ctx, "task-reanswer")
	if err != nil || task.Status != string(TaskStatusInProgress) || !strings.Contains(task.Notes, "决策 "+d.ID+"：改答为 B") {
		t.Fatalf("改答任务记录: %+v, %v", task, err)
	}
	current = now.Add(6 * time.Minute)
	if _, err := svc.AnswerDecision(ctx, d.ID, "C", "user"); !errors.Is(err, store.ErrDecisionClosed) {
		t.Fatalf("窗口外改答: %v", err)
	}

	ignored, err := db.CreateStewardDecision(ctx, store.StewardDecision{Status: store.DecisionStatusIgnored})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AnswerDecision(ctx, ignored.ID, "B", "user"); !errors.Is(err, store.ErrDecisionClosed) {
		t.Fatalf("ignored 卡改答: %v", err)
	}
}

// TestAnswerDecisionTaskUpdateFailureKeepsCardOpen 验证任务更新失败时卡片仍是 open，修好后可以再次回答。
func TestAnswerDecisionTaskUpdateFailureKeepsCardOpen(t *testing.T) {
	ctx := context.Background()
	svc, db, _, _ := setupReceiptTestService(t, newPokeDriver(), true, false, nil)
	d, err := db.CreateStewardDecision(ctx, store.StewardDecision{TaskID: "task-1", Question: "继续吗"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AnswerDecision(ctx, d.ID, "继续", "user:desktop"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("任务不存在时应更新失败: %v", err)
	}
	open, err := db.GetStewardDecision(ctx, d.ID)
	if err != nil || open.Status != store.DecisionStatusOpen {
		t.Fatalf("任务更新失败后卡片应保持 open: %+v, %v", open, err)
	}
	if err := db.CreateTask(ctx, store.Task{ID: "task-1", Title: "任务", Status: "in_progress", Meta: EncodeTaskMeta(TaskMeta{})}); err != nil {
		t.Fatal(err)
	}
	answered, err := svc.AnswerDecision(ctx, d.ID, "继续", "user:desktop")
	if err != nil || answered.Status != store.DecisionStatusAnswered {
		t.Fatalf("再次回答: %+v, %v", answered, err)
	}
	got, err := db.GetTask(ctx, "task-1")
	if err != nil || !strings.Contains(got.Notes, "决策 "+d.ID+"：继续吗 → 继续") {
		t.Fatalf("任务: %+v, %v", got, err)
	}
}

func TestExecutorResultGateOverride(t *testing.T) {
	for _, tc := range []struct {
		status ResultStatus
		want   string
	}{
		{ResultDone, ""},
		{ResultFailed, ""},
		{ResultWaiting, "D-0001"},
		{ResultDelegated, ""},
		{ResultNeedsDecision, ""},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			ctx := context.Background()
			svc, db, _, _ := setupReceiptTestService(t, newPokeDriver(), true, false, nil)
			task := store.Task{ID: "task-1", Title: "任务", Status: "in_progress", Meta: EncodeTaskMeta(TaskMeta{GateOverride: "D-0001"})}
			if err := db.CreateTask(ctx, task); err != nil {
				t.Fatal(err)
			}
			task, err := db.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			res := ExecutorResult{Status: tc.status, Summary: "结果", Question: "继续吗"}
			if err := svc.applyExecutorResult(ctx, &task, res, time.Now()); err != nil {
				t.Fatal(err)
			}
			task, err = db.UpdateTask(ctx, task, task.Revision)
			if err != nil {
				t.Fatal(err)
			}
			meta, err := ParseTaskMeta(task.Meta)
			if err != nil || meta.GateOverride != tc.want {
				t.Fatalf("解锁元数据: %+v, %v", meta, err)
			}
		})
	}
}
