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
			if _, err := svc.AnswerDecision(ctx, d.ID, "停下", "user:desktop"); !errors.Is(err, store.ErrDecisionClosed) {
				t.Fatalf("重复答卡: %v", err)
			}
			again, err := db.GetTask(ctx, task.ID)
			if err != nil || !reflect.DeepEqual(got, again) {
				t.Fatalf("重复答卡修改了任务: %+v, %v", again, err)
			}
		})
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
