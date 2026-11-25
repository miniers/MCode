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
	t.Parallel()
	for _, tc := range []struct {
		status string
		want   string
	}{
		{"needs_approval", "todo"},
		{"waiting", "todo"},
		{"in_progress", "in_progress"},
		{"review", "review"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
			svc, db, _, _ := setupReceiptTestService(t, newPokeDriver(), true, false, func() time.Time { return now })
			threadID := "thread-1"
			taskMeta := TaskMeta{DecisionID: "D-0001"}
			if tc.status == "waiting" {
				taskMeta = TaskMeta{DecisionID: "D-0001", WaitKind: "machine", WakeAt: "later", WaitCheck: "check", WaitBase: "base", Wakes: 2}
			}
			task := store.Task{ID: "task-1", Title: "任务", Notes: "原描述", ThreadID: &threadID, Status: tc.status,
				Meta: EncodeTaskMeta(taskMeta)}
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
			if tc.status == "in_progress" || tc.status == "review" {
				if !errors.Is(err, ErrDecisionLocked) {
					t.Fatalf("执行中改答应被拒绝: %v", err)
				}
				decision, err := db.GetStewardDecision(ctx, d.ID)
				if err != nil || decision.Answer != "继续" {
					t.Fatalf("原答案应保持不变: %+v, %v", decision, err)
				}
				again, err := db.GetTask(ctx, task.ID)
				if err != nil || !reflect.DeepEqual(got, again) {
					t.Fatalf("任务应保持不变: %+v, %v", again, err)
				}
				return
			}
			if err != nil || reanswered.Answer != "停下" {
				t.Fatalf("窗口内改答: %+v, %v", reanswered, err)
			}
			again, err := db.GetTask(ctx, task.ID)
			if err != nil || again.Status != string(TaskStatusCancelled) || !strings.Contains(again.Notes, "决策 D-0001：改答为 停下") || reflect.DeepEqual(got, again) {
				t.Fatalf("改答未记录到任务: %+v, %v", again, err)
			}
		})
	}
}

func TestAnswerDecisionStop(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"waiting", "needs_approval", "todo", "in_progress"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			driver := newPokeDriver()
			svc, db, _, _ := setupReceiptTestService(t, driver, true, false, nil)
			thread := "executor"
			if status == "in_progress" {
				driver.setThreadState(thread, SessionState{Running: true})
			}
			stopped := ""
			svc.opts.StopSession = func(_ context.Context, id string) error { stopped = id; return nil }
			task := store.Task{ID: "task", Status: status, ThreadID: &thread, Meta: EncodeTaskMeta(TaskMeta{DecisionID: "D-0002", WaitKind: "machine", WakeAt: "later", WaitCheck: "check", WaitBase: "base", Wakes: 2})}
			if err := db.CreateTask(ctx, task); err != nil {
				t.Fatal(err)
			}
			options := `[{"key":"A","text":"继续等"},{"key":"B","text":"停下"}]`
			other, err := db.CreateStewardDecision(ctx, store.StewardDecision{TaskID: task.ID, Question: "其他"})
			if err != nil {
				t.Fatal(err)
			}
			d, err := db.CreateStewardDecision(ctx, store.StewardDecision{TaskID: task.ID, Question: "继续吗", OptionsJSON: options})
			if err != nil {
				t.Fatal(err)
			}
			answered, err := svc.AnswerDecision(ctx, d.ID, "B", "user")
			if err != nil || answered.Status != store.DecisionStatusAnswered {
				t.Fatalf("答卡: %+v, %v", answered, err)
			}
			got, err := db.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			meta, err := ParseTaskMeta(got.Meta)
			if err != nil || got.Status != "cancelled" || meta.DecisionID != "" || meta.WaitKind != "" || meta.WakeAt != "" || meta.WaitCheck != "" || meta.WaitBase != "" || meta.Wakes != 0 || meta.GateOverride != "" || !strings.Contains(got.Notes, "B · 停下") {
				t.Fatalf("任务: %+v, 元数据: %+v, 错误: %v", got, meta, err)
			}
			if (status == "in_progress" && stopped != thread) || (status != "in_progress" && stopped != "") {
				t.Fatalf("停止会话: %q", stopped)
			}
			other, err = db.GetStewardDecision(ctx, other.ID)
			if err != nil || other.Status != store.DecisionStatusSuperseded {
				t.Fatalf("其他决策: %+v, %v", other, err)
			}
		})
	}
}

func TestDecisionStopScheduler(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	driver := newPokeDriver()
	svc, db, _, scheduler := setupReceiptTestService(t, driver, true, true, nil)
	seedSchedulerGoal(t, ctx, db, store.Goal{ID: "g", ProjectID: "p", Title: "目标"}, store.Task{ID: "task", Status: "waiting", Meta: EncodeTaskMeta(TaskMeta{DecisionID: "D-0001"})})
	d, err := db.CreateStewardDecision(ctx, store.StewardDecision{TaskID: "task", OptionsJSON: `[{"key":"B","text":"停下"}]`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AnswerDecision(ctx, d.ID, "B", "user"); err != nil {
		t.Fatal(err)
	}
	scheduler.Tick(ctx)
	task, err := db.GetTask(ctx, "task")
	driver.mu.Lock()
	starts := len(driver.states)
	driver.mu.Unlock()
	if err != nil || task.Status != "cancelled" || starts != 0 {
		t.Fatalf("任务: %+v, 启动数: %d, 错误: %v", task, starts, err)
	}
}

func TestDecisionAnswerOptions(t *testing.T) {
	t.Parallel()
	d := store.StewardDecision{OptionsJSON: `[{"key":"A","text":"继续等"},{"key":"B","text":"停下"}]`}
	for _, tc := range []struct{ input, text, display string }{{" a ", "继续等", "A · 继续等"}, {"停下", "停下", "B · 停下"}, {"其他", "其他", "其他"}} {
		text, display := decisionAnswer(d, tc.input)
		if text != tc.text || display != tc.display {
			t.Fatalf("%q: %q, %q", tc.input, text, display)
		}
	}
}

func TestAnswerDecisionContinueOption(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	svc, db, _, _ := setupReceiptTestService(t, newPokeDriver(), true, false, func() time.Time { return now })
	if err := db.CreateTask(ctx, store.Task{ID: "task", Status: "waiting", Meta: EncodeTaskMeta(TaskMeta{DecisionID: "D-0001", WaitKind: "machine", WakeAt: "old", WaitCheck: "check", WaitBase: "base", Wakes: 2})}); err != nil {
		t.Fatal(err)
	}
	d, err := db.CreateStewardDecision(ctx, store.StewardDecision{TaskID: "task", OptionsJSON: `[{"key":"A","text":"继续等"},{"key":"B","text":"停下"}]`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AnswerDecision(ctx, d.ID, "A", "user"); err != nil {
		t.Fatal(err)
	}
	task, err := db.GetTask(ctx, "task")
	meta, metaErr := ParseTaskMeta(task.Meta)
	if err != nil || metaErr != nil || task.Status != "waiting" || meta.WaitKind != "machine" || meta.WaitCheck != "check" || meta.WaitBase != "base" || meta.Wakes != 0 || meta.WakeAt != now.Add(5*time.Minute).Format(time.RFC3339) || !strings.Contains(task.Notes, "A · 继续等") {
		t.Fatalf("任务: %+v, 元数据: %+v, 错误: %v, %v", task, meta, err, metaErr)
	}
}

func TestAnswerDecisionStopPrefix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, db, _, _ := setupReceiptTestService(t, newPokeDriver(), true, false, nil)
	if err := db.CreateTask(ctx, store.Task{ID: "task-stop-prefix", Status: "waiting", Meta: EncodeTaskMeta(TaskMeta{DecisionID: "D-0001", WaitKind: "human", WakeAt: "later"})}); err != nil {
		t.Fatal(err)
	}
	d, err := db.CreateStewardDecision(ctx, store.StewardDecision{TaskID: "task-stop-prefix"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AnswerDecision(ctx, d.ID, "取消发布", "user"); err != nil {
		t.Fatal(err)
	}
	task, err := db.GetTask(ctx, "task-stop-prefix")
	if err != nil || task.Status != string(TaskStatusCancelled) {
		t.Fatalf("取消前缀应停止任务: %+v, %v", task, err)
	}
}

func TestAnswerDecisionReanswerWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	current := now
	svc, db, _, _ := setupReceiptTestService(t, newPokeDriver(), true, false, func() time.Time { return current })
	if err := db.CreateTask(ctx, store.Task{ID: "task-reanswer", Title: "任务", Status: string(TaskStatusTodo), Meta: EncodeTaskMeta(TaskMeta{})}); err != nil {
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
	if err != nil || task.Status != string(TaskStatusTodo) || !strings.Contains(task.Notes, "决策 "+d.ID+"：改答为 B") {
		t.Fatalf("改答任务记录: %+v, %v", task, err)
	}
	// 改答窗口从最后一次回答起算，这里已超过五分钟。
	current = now.Add(time.Minute + 5*time.Minute + time.Second)
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
	t.Parallel()
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
	t.Parallel()
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
			t.Parallel()
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

func TestAnswerWaitDecisionContinueKeepsWaiting(t *testing.T) {
	t.Parallel()
	for _, answer := range []string{"A", "继续等"} {
		t.Run(answer, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
			driver := newMockDriver()
			threadID := "thread-wait"
			driver.setThreadState(threadID, SessionState{Running: false})
			svc, db, _, _ := setupReceiptTestService(t, driver, true, false, func() time.Time { return now })
			initMeta := TaskMeta{
				WaitKind:  "human",
				WakeAt:    now.Add(-time.Minute).Format(time.RFC3339),
				WaitCheck: "echo ok",
				WaitBase:  "base",
				Wakes:     3,
				Summary:   "人工确认",
			}
			if err := db.CreateTask(ctx, store.Task{ID: "task-wait", ThreadID: &threadID, Status: string(TaskStatusWaiting), Meta: EncodeTaskMeta(initMeta)}); err != nil {
				t.Fatal(err)
			}
			if err := svc.CheckWaits(ctx, now); err != nil {
				t.Fatal(err)
			}
			decisions, err := db.ListStewardDecisions(ctx, "open", 0)
			if err != nil || len(decisions) != 1 {
				t.Fatalf("等待到期决策卡: %+v, %v", decisions, err)
			}
			if _, err := svc.AnswerDecision(ctx, decisions[0].ID, answer, "user:desktop"); err != nil {
				t.Fatal(err)
			}
			got, err := db.GetTask(ctx, "task-wait")
			if err != nil || got.Status != string(TaskStatusWaiting) {
				t.Fatalf("继续等应保持 waiting: %+v, %v", got, err)
			}
			meta, err := ParseTaskMeta(got.Meta)
			if err != nil {
				t.Fatal(err)
			}
			wake, wakeErr := time.Parse(time.RFC3339, meta.WakeAt)
			if wakeErr != nil || !wake.After(now) {
				t.Fatalf("WakeAt 应在 now 之后: %q, %v", meta.WakeAt, wakeErr)
			}
			if meta.Wakes != 0 || meta.GateOverride != "" || meta.DecisionID != "" || meta.WaitKind != "human" || meta.WaitCheck != "echo ok" || meta.WaitBase != "base" {
				t.Fatalf("等待元数据异常: %+v", meta)
			}
		})
	}
}

func TestAnswerWaitDecisionOtherAnswerNoGateOverride(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	driver := newMockDriver()
	threadID := "thread-wait"
	driver.setThreadState(threadID, SessionState{Running: false})
	svc, db, _, _ := setupReceiptTestService(t, driver, true, false, func() time.Time { return now })
	initMeta := TaskMeta{
		WaitKind:  "human",
		WakeAt:    now.Add(-time.Minute).Format(time.RFC3339),
		WaitCheck: "echo ok",
		WaitBase:  "base",
		Wakes:     2,
		Summary:   "人工确认",
	}
	if err := db.CreateTask(ctx, store.Task{ID: "task-wait", ThreadID: &threadID, Status: string(TaskStatusWaiting), Meta: EncodeTaskMeta(initMeta)}); err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckWaits(ctx, now); err != nil {
		t.Fatal(err)
	}
	decisions, err := db.ListStewardDecisions(ctx, "open", 0)
	if err != nil || len(decisions) != 1 {
		t.Fatalf("等待到期决策卡: %+v, %v", decisions, err)
	}
	if _, err := svc.AnswerDecision(ctx, decisions[0].ID, "先跳过继续执行", "user:desktop"); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetTask(ctx, "task-wait")
	if err != nil || got.Status != string(TaskStatusTodo) {
		t.Fatalf("其它非停下回答应转为 todo: %+v, %v", got, err)
	}
	meta, err := ParseTaskMeta(got.Meta)
	if err != nil {
		t.Fatal(err)
	}
	if meta.WaitKind != "" || meta.WakeAt != "" || meta.WaitCheck != "" || meta.WaitBase != "" || meta.Wakes != 0 || meta.GateOverride != "" || meta.DecisionID != "" {
		t.Fatalf("等待元数据与授权字段应已清空: %+v", meta)
	}
}

func TestAnswerDecisionStaleOpenCardOnlyNotes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, db, _, _ := setupReceiptTestService(t, newPokeDriver(), true, false, nil)
	if err := db.CreateTask(ctx, store.Task{
		ID:     "task-stale",
		Title:  "任务",
		Notes:  "原描述",
		Status: string(TaskStatusNeedsApproval),
		Meta:   EncodeTaskMeta(TaskMeta{WaitKind: "machine", WakeAt: "later"}),
	}); err != nil {
		t.Fatal(err)
	}
	stale, err := db.CreateStewardDecision(ctx, store.StewardDecision{TaskID: "task-stale", Question: "旧卡"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := db.CreateStewardDecision(ctx, store.StewardDecision{Question: "新卡"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.GetTask(ctx, "task-stale")
	if err != nil {
		t.Fatal(err)
	}
	task.Meta = EncodeTaskMeta(TaskMeta{DecisionID: current.ID, WaitKind: "machine", WakeAt: "later"})
	if _, err := db.UpdateTask(ctx, task, task.Revision); err != nil {
		t.Fatal(err)
	}

	answered, err := svc.AnswerDecision(ctx, stale.ID, "停下", "user:desktop")
	if err != nil || answered.Status != store.DecisionStatusAnswered {
		t.Fatalf("旧卡应标记为 answered: %+v, %v", answered, err)
	}
	got, err := db.GetTask(ctx, "task-stale")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := ParseTaskMeta(got.Meta)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != string(TaskStatusNeedsApproval) || meta.DecisionID != current.ID || meta.WaitKind != "machine" || meta.WakeAt != "later" || meta.GateOverride != "" {
		t.Fatalf("过期卡不应改变任务状态与元数据: %+v, %+v", got, meta)
	}
	if !strings.Contains(got.Notes, "决策 "+stale.ID+"：旧卡 → 停下") {
		t.Fatalf("过期卡回答应记入 Notes: %q", got.Notes)
	}
	gotCurrent, err := db.GetStewardDecision(ctx, current.ID)
	if err != nil || gotCurrent.Status != store.DecisionStatusOpen {
		t.Fatalf("当前卡应保持 open: %+v, %v", gotCurrent, err)
	}
}

func TestAnswerDecisionRejectedTaskClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, db, _, _ := setupReceiptTestService(t, newPokeDriver(), true, false, nil)
	if err := db.CreateTask(ctx, store.Task{
		ID:     "task-rejected",
		Title:  "任务",
		Status: string(TaskStatusRejected),
		Meta:   EncodeTaskMeta(TaskMeta{DecisionID: "D-0001"}),
	}); err != nil {
		t.Fatal(err)
	}
	d, err := db.CreateStewardDecision(ctx, store.StewardDecision{TaskID: "task-rejected", Question: "继续？"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AnswerDecision(ctx, d.ID, "继续", "user:desktop"); !errors.Is(err, store.ErrDecisionClosed) {
		t.Fatalf("rejected 任务应返回 ErrDecisionClosed: %v", err)
	}
	got, err := db.GetTask(ctx, "task-rejected")
	if err != nil || got.Status != string(TaskStatusRejected) || got.Notes != "" {
		t.Fatalf("rejected 任务不应被改动: %+v, %v", got, err)
	}
}

func TestAnswerDecisionStopStopsRunningSessionInNeedsApproval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	driver := newPokeDriver()
	svc, db, _, _ := setupReceiptTestService(t, driver, true, false, nil)
	thread := "executor-running"
	driver.setThreadState(thread, SessionState{Running: true})
	stopped := ""
	svc.opts.StopSession = func(_ context.Context, id string) error {
		stopped = id
		return nil
	}
	if err := db.CreateTask(ctx, store.Task{
		ID:       "task-needs-approval-running",
		Title:    "任务",
		Status:   string(TaskStatusNeedsApproval),
		ThreadID: &thread,
		Meta:     EncodeTaskMeta(TaskMeta{DecisionID: "D-0001"}),
	}); err != nil {
		t.Fatal(err)
	}
	d, err := db.CreateStewardDecision(ctx, store.StewardDecision{TaskID: "task-needs-approval-running", Question: "继续吗"})
	if err != nil {
		t.Fatal(err)
	}
	answered, err := svc.AnswerDecision(ctx, d.ID, "停下", "user:desktop")
	if err != nil || answered.Status != store.DecisionStatusAnswered {
		t.Fatalf("答卡应成功: %+v, %v", answered, err)
	}
	if stopped != thread {
		t.Fatalf("运行中会话应被停止: got %q, want %q", stopped, thread)
	}
	got, err := db.GetTask(ctx, "task-needs-approval-running")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := ParseTaskMeta(got.Meta)
	if err != nil || got.Status != string(TaskStatusCancelled) || meta.DecisionID != "" {
		t.Fatalf("任务应变为 cancelled 且清空 DecisionID: %+v, %+v, %v", got, meta, err)
	}
}

type failingFollowUpDriver struct {
	*pokeDriver
	err error
}

func (d *failingFollowUpDriver) SendFollowUp(ctx context.Context, threadID, text string) error {
	_ = d.pokeDriver.SendFollowUp(ctx, threadID, text)
	return d.err
}

func TestAnswerDecisionResumesOriginalSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	driver := newPokeDriver()
	threadID := "thread-resume"
	driver.setThreadState(threadID, SessionState{Running: false})
	svc, db, _, _ := setupReceiptTestService(t, driver, true, false, func() time.Time { return now })

	if err := db.CreateTask(ctx, store.Task{
		ID:       "task-resume",
		Title:    "继续原会话",
		Status:   string(TaskStatusNeedsApproval),
		ThreadID: &threadID,
		Meta:     EncodeTaskMeta(TaskMeta{DecisionID: "D-0001"}),
	}); err != nil {
		t.Fatal(err)
	}
	// 预设一轮未消费回执，验证送回原会话时会清空上一轮回执与追问状态。
	if err := svc.SubmitExecutorResult(ctx, "task-resume", threadID, ExecutorResult{
		Status:   ResultNeedsDecision,
		Summary:  "等待决定",
		Question: "是否合并",
		Options:  []DecisionOption{{Key: "A", Text: "允许合并"}, {Key: "B", Text: "暂不合并"}},
	}); err != nil {
		t.Fatal(err)
	}
	if action := svc.planReceiptAsk("task-resume", now); action != receiptAskSend {
		t.Fatalf("预期先记录一次追问: %v", action)
	}

	d, err := db.CreateStewardDecision(ctx, store.StewardDecision{
		TaskID:      "task-resume",
		Question:    "是否合并",
		OptionsJSON: `[{"key":"A","text":"允许合并"},{"key":"B","text":"暂不合并"}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	answered, err := svc.AnswerDecision(ctx, d.ID, "A", "user:desktop")
	if err != nil || answered.Status != store.DecisionStatusAnswered {
		t.Fatalf("答卡失败: %+v, %v", answered, err)
	}

	got, err := db.GetTask(ctx, "task-resume")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := ParseTaskMeta(got.Meta)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != string(TaskStatusInProgress) || got.ThreadID == nil || *got.ThreadID != threadID {
		t.Fatalf("应回到 in_progress 且保留原 ThreadID: %+v", got)
	}
	if meta.GateOverride != d.ID || meta.DecisionID != "" {
		t.Fatalf("应写入 GateOverride 且清空 DecisionID: %+v", meta)
	}
	calls := driver.sentFollowUps()
	wantPrompt := decisionResumePrompt(got.ID, d.ID, d.Question, "A · 允许合并")
	if len(calls) != 1 || calls[0].threadID != threadID || calls[0].text != wantPrompt {
		t.Fatalf("应向原会话发送续跑文案: got=%+v, want thread=%s text=%q", calls, threadID, wantPrompt)
	}
	if _, ok := svc.takeReceipt("task-resume", threadID); ok {
		t.Fatal("送回原会话后应清空上一轮回执")
	}
	if action := svc.planReceiptAsk("task-resume", now.Add(time.Second)); action != receiptAskSend {
		t.Fatalf("送回原会话后应重置追问计数: %v", action)
	}
}

func TestAnswerDecisionFallsBackToTodoWhenSessionGone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(*pokeDriver, string) SessionDriver
	}{
		{
			name: "session_not_found",
			setup: func(d *pokeDriver, _ string) SessionDriver {
				return d
			},
		},
		{
			name: "host_tools_unavailable",
			setup: func(d *pokeDriver, threadID string) SessionDriver {
				d.setThreadState(threadID, SessionState{Running: false, HostToolsUnavailable: true})
				return d
			},
		},
		{
			name: "send_follow_up_error",
			setup: func(d *pokeDriver, threadID string) SessionDriver {
				d.setThreadState(threadID, SessionState{Running: false})
				return &failingFollowUpDriver{pokeDriver: d, err: errors.New("send failed")}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
			base := newPokeDriver()
			threadID := "thread-missing"
			driver := tc.setup(base, threadID)
			svc, db, _, _ := setupReceiptTestService(t, driver, true, false, func() time.Time { return now })

			if err := db.CreateTask(ctx, store.Task{
				ID:       "task-fallback",
				Title:    "回退待办",
				Status:   string(TaskStatusNeedsApproval),
				ThreadID: &threadID,
				Meta:     EncodeTaskMeta(TaskMeta{DecisionID: "D-0001"}),
			}); err != nil {
				t.Fatal(err)
			}
			d, err := db.CreateStewardDecision(ctx, store.StewardDecision{TaskID: "task-fallback", Question: "继续吗"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.AnswerDecision(ctx, d.ID, "继续", "user:desktop"); err != nil {
				t.Fatalf("答卡失败: %v", err)
			}
			got, err := db.GetTask(ctx, "task-fallback")
			if err != nil {
				t.Fatal(err)
			}
			meta, err := ParseTaskMeta(got.Meta)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != string(TaskStatusTodo) || meta.GateOverride != d.ID || meta.DecisionID != "" {
				t.Fatalf("无法送回原会话时应回退 todo 且保留 GateOverride: %+v, %+v", got, meta)
			}
			if tc.name != "send_follow_up_error" && len(base.sentFollowUps()) != 0 {
				t.Fatalf("条件不满足时不应发送跟进: %+v", base.sentFollowUps())
			}
		})
	}
}

func TestAnswerDecisionIgnoreDoesNotUnlockGate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	driver := newPokeDriver()
	threadID := "thread-ignore"
	driver.setThreadState(threadID, SessionState{Running: false})
	svc, db, _, _ := setupReceiptTestService(t, driver, true, false, func() time.Time { return now })

	if err := db.CreateTask(ctx, store.Task{
		ID:       "task-ignore",
		Title:    "忽略授权",
		Status:   string(TaskStatusNeedsApproval),
		ThreadID: &threadID,
		Meta:     EncodeTaskMeta(TaskMeta{DecisionID: "D-0001"}),
	}); err != nil {
		t.Fatal(err)
	}
	d, err := db.CreateStewardDecision(ctx, store.StewardDecision{
		TaskID:      "task-ignore",
		Question:    "是否强制推送",
		OptionsJSON: `[{"key":"A","text":"允许"}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AnswerDecision(ctx, d.ID, "忽略", "user:desktop"); err != nil {
		t.Fatalf("答卡失败: %v", err)
	}
	got, err := db.GetTask(ctx, "task-ignore")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := ParseTaskMeta(got.Meta)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != string(TaskStatusInProgress) || got.ThreadID == nil || *got.ThreadID != threadID {
		t.Fatalf("忽略后应送回原会话继续其余工作: %+v", got)
	}
	if meta.GateOverride != "" || meta.DecisionID != "" {
		t.Fatalf("忽略不得解锁 GateOverride: %+v", meta)
	}
	calls := driver.sentFollowUps()
	wantPrompt := decisionIgnorePrompt(got.ID, d.ID, d.Question)
	if len(calls) != 1 || calls[0].threadID != threadID || calls[0].text != wantPrompt {
		t.Fatalf("应发送忽略文案: got=%+v, want thread=%s text=%q", calls, threadID, wantPrompt)
	}
}

func TestAnswerWaitDecisionIgnoreKeepsWaiting(t *testing.T) {
	t.Parallel()
	for _, status := range []TaskStatus{TaskStatusNeedsApproval, TaskStatusWaiting} {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
			driver := newPokeDriver()
			threadID := "thread-wait-ignore"
			driver.setThreadState(threadID, SessionState{Running: false})
			svc, db, _, _ := setupReceiptTestService(t, driver, true, false, func() time.Time { return now })

			initMeta := TaskMeta{
				DecisionID: "D-0001",
				WaitKind:   "human",
				WakeAt:     now.Add(-time.Minute).Format(time.RFC3339),
				WaitCheck:  "echo ok",
				WaitBase:   "base",
				Wakes:      3,
			}
			if err := db.CreateTask(ctx, store.Task{
				ID:       "task-wait-ignore",
				ThreadID: &threadID,
				Status:   string(status),
				Meta:     EncodeTaskMeta(initMeta),
			}); err != nil {
				t.Fatal(err)
			}
			d, err := db.CreateStewardDecision(ctx, store.StewardDecision{TaskID: "task-wait-ignore", Question: "人工确认好了吗"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.AnswerDecision(ctx, d.ID, "忽略", "user:desktop"); err != nil {
				t.Fatalf("答卡失败: %v", err)
			}
			got, err := db.GetTask(ctx, "task-wait-ignore")
			if err != nil {
				t.Fatal(err)
			}
			meta, err := ParseTaskMeta(got.Meta)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != string(TaskStatusWaiting) {
				t.Fatalf("等待卡忽略应保持 waiting: %+v", got)
			}
			wake, wakeErr := time.Parse(time.RFC3339, meta.WakeAt)
			if wakeErr != nil || !wake.After(now) {
				t.Fatalf("WakeAt 应续期到 now 之后: %q, %v", meta.WakeAt, wakeErr)
			}
			if meta.Wakes != 0 || meta.GateOverride != "" || meta.DecisionID != "" || meta.WaitKind != "human" || meta.WaitCheck != "echo ok" || meta.WaitBase != "base" {
				t.Fatalf("等待元数据异常: %+v", meta)
			}
			if len(driver.sentFollowUps()) != 0 {
				t.Fatalf("等待卡忽略不应唤醒原会话: %+v", driver.sentFollowUps())
			}
		})
	}
}

func TestAnswerWaitDecisionResumesOriginalSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	driver := newPokeDriver()
	threadID := "thread-wait-resume"
	driver.setThreadState(threadID, SessionState{Running: false})
	svc, db, _, _ := setupReceiptTestService(t, driver, true, false, func() time.Time { return now })

	initMeta := TaskMeta{
		WaitKind:  "human",
		WakeAt:    now.Add(-time.Minute).Format(time.RFC3339),
		WaitCheck: "echo ok",
		WaitBase:  "base",
		Wakes:     2,
		Summary:   "人工确认",
	}
	if err := db.CreateTask(ctx, store.Task{
		ID:       "task-wait-resume",
		ThreadID: &threadID,
		Status:   string(TaskStatusWaiting),
		Meta:     EncodeTaskMeta(initMeta),
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckWaits(ctx, now); err != nil {
		t.Fatal(err)
	}
	decisions, err := db.ListStewardDecisions(ctx, "open", 0)
	if err != nil || len(decisions) != 1 {
		t.Fatalf("等待到期决策卡: %+v, %v", decisions, err)
	}
	if _, err := svc.AnswerDecision(ctx, decisions[0].ID, "继续执行下一步", "user:desktop"); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetTask(ctx, "task-wait-resume")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := ParseTaskMeta(got.Meta)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != string(TaskStatusInProgress) || got.ThreadID == nil || *got.ThreadID != threadID {
		t.Fatalf("等待卡普通回答应送回原会话: %+v", got)
	}
	if meta.WaitKind != "" || meta.WakeAt != "" || meta.WaitCheck != "" || meta.WaitBase != "" || meta.Wakes != 0 || meta.GateOverride != "" || meta.DecisionID != "" {
		t.Fatalf("等待元数据应已清空且不写 GateOverride: %+v", meta)
	}
	calls := driver.sentFollowUps()
	wantPrompt := decisionResumePrompt(got.ID, decisions[0].ID, decisions[0].Question, "继续执行下一步")
	if len(calls) != 1 || calls[0].threadID != threadID || calls[0].text != wantPrompt {
		t.Fatalf("应发送续跑文案: got=%+v, want thread=%s text=%q", calls, threadID, wantPrompt)
	}
}
