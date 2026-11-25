package kanban

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mcode/internal/store"
)

func TestCheckWaits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, command string
		wakes         int
		want          string
	}{
		{"成功", "true", 0, "todo"}, {"退避", "false", 0, "waiting"},
		{"超时", "false", 6, "needs_approval"}, {"写命令", "rm marker", 0, "needs_approval"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			svc, db, _, _ := setupReceiptTestService(t, newPokeDriver(), true, false, nil)
			dir := svc.opts.Projects.(*mockProjects).dir
			marker := filepath.Join(dir, "marker")
			if err := os.WriteFile(marker, []byte("保留"), 0600); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
			project := "p"
			meta := TaskMeta{WaitKind: "machine", WaitCheck: tc.command, WakeAt: now.Format(time.RFC3339), Wakes: tc.wakes, Summary: "等待数据"}
			if err := db.CreateTask(ctx, store.Task{ID: "t", ProjectID: &project, Title: "等待", Status: "waiting", Meta: EncodeTaskMeta(meta)}); err != nil {
				t.Fatal(err)
			}
			if err := svc.CheckWaits(ctx, now); err != nil {
				t.Fatal(err)
			}
			task, err := db.GetTask(ctx, "t")
			if err != nil {
				t.Fatal(err)
			}
			if task.Status != tc.want {
				t.Fatalf("状态 %s", task.Status)
			}
			m, err := ParseTaskMeta(task.Meta)
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "退避" && (m.Wakes != 1 || m.WakeAt != now.Add(10*time.Minute).Format(time.RFC3339)) {
				t.Fatalf("退避数据 %#v", m)
			}
			if tc.want == "needs_approval" {
				decisions, err := db.ListStewardDecisions(ctx, "open", 0)
				if err != nil || len(decisions) != 1 {
					t.Fatalf("决策卡 %#v %v", decisions, err)
				}
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("文件被删除: %v", err)
			}
		})
	}
}

func TestCheckWaitsBadTaskDoesNotStopRound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, db, _, _ := setupReceiptTestService(t, newPokeDriver(), true, false, nil)
	project := "p"
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	if err := db.CreateTask(ctx, store.Task{ID: "bad", ProjectID: &project, Status: "waiting", Meta: "{"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(ctx, store.Task{ID: "good", ProjectID: &project, Status: "waiting", Meta: EncodeTaskMeta(TaskMeta{WaitKind: "machine", WaitCheck: "true", WakeAt: now.Format(time.RFC3339)})}); err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckWaits(ctx, now); err == nil {
		t.Fatal("损坏任务应汇总返回错误")
	}
	bad, err := db.GetTask(ctx, "bad")
	if err != nil || bad.LastFailureReason == nil {
		t.Fatalf("损坏任务应记录失败原因: %+v, %v", bad, err)
	}
	good, err := db.GetTask(ctx, "good")
	if err != nil || good.Status != string(TaskStatusTodo) {
		t.Fatalf("后续等待任务未唤醒: %+v, %v", good, err)
	}
}

func TestCheckWaitsHumanExpiryCreatesDecision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	svc, db, _, _ := setupReceiptTestService(t, newPokeDriver(), true, false, func() time.Time { return now })
	if err := db.CreateTask(ctx, store.Task{ID: "human", Status: "waiting", Meta: EncodeTaskMeta(TaskMeta{WaitKind: "human", WakeAt: now.Add(-time.Minute).Format(time.RFC3339), Summary: "人工确认"})}); err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckWaits(ctx, now); err != nil {
		t.Fatal(err)
	}
	task, err := db.GetTask(ctx, "human")
	if err != nil || task.Status != string(TaskStatusNeedsApproval) {
		t.Fatalf("人工等待应升级: %+v, %v", task, err)
	}
	decisions, err := db.ListStewardDecisions(ctx, "open", 0)
	if err != nil || len(decisions) != 1 || decisions[0].Question != "等待的人工处理已到期，是否继续等" {
		t.Fatalf("人工等待决策卡: %+v, %v", decisions, err)
	}
}

type conflictProjects struct {
	dir        string
	db         *store.Store
	once       sync.Once
	conflicted string
}

func (p *conflictProjects) ProjectRoot(ctx context.Context, projectID string) (string, error) {
	p.once.Do(func() {
		taskID := "wait-1"
		if projectID == "p2" {
			taskID = "wait-2"
		}
		t, err := p.db.GetTask(ctx, taskID)
		if err == nil {
			t.Title = "concurrently updated"
			if _, uerr := p.db.UpdateTask(ctx, t, t.Revision); uerr == nil {
				p.conflicted = taskID
			}
		}
	})
	return p.dir, nil
}

func TestCheckWaitsContinuesAfterConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, db, _, _ := setupReceiptTestService(t, newPokeDriver(), true, false, nil)
	projects := &conflictProjects{dir: t.TempDir(), db: db}
	svc.opts.Projects = projects

	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	meta := EncodeTaskMeta(TaskMeta{WaitKind: "machine", WaitCheck: "true", WakeAt: now.Format(time.RFC3339)})
	p1, p2 := "p1", "p2"
	if err := db.CreateTask(ctx, store.Task{ID: "wait-1", ProjectID: &p1, Status: string(TaskStatusWaiting), Meta: meta}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(ctx, store.Task{ID: "wait-2", ProjectID: &p2, Status: string(TaskStatusWaiting), Meta: meta}); err != nil {
		t.Fatal(err)
	}

	err := svc.CheckWaits(ctx, now)
	if !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("期望返回 store.ErrRevisionConflict，实际: %v", err)
	}
	if projects.conflicted == "" {
		t.Fatal("未触发预期的并发版本冲突")
	}

	otherID := "wait-2"
	if projects.conflicted == "wait-2" {
		otherID = "wait-1"
	}

	conflictedTask, err := db.GetTask(ctx, projects.conflicted)
	if err != nil || conflictedTask.Status != string(TaskStatusWaiting) {
		t.Fatalf("冲突任务应保留 waiting 状态: %+v, %v", conflictedTask, err)
	}
	otherTask, err := db.GetTask(ctx, otherID)
	if err != nil || otherTask.Status != string(TaskStatusTodo) {
		t.Fatalf("冲突后的剩余 waiting 任务应继续处理并唤醒: %+v, %v", otherTask, err)
	}
}

func TestCheckWaitsExpiredWhileSessionRunningResumes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	driver := newMockDriver()
	threadID := "thread-running"
	driver.setThreadState(threadID, SessionState{Running: true})
	svc, db, _, _ := setupReceiptTestService(t, driver, true, false, func() time.Time { return now })
	meta := TaskMeta{
		WaitKind:  "human",
		WaitCheck: "false",
		WakeAt:    now.Add(-time.Minute).Format(time.RFC3339),
		WaitBase:  "base",
		Wakes:     2,
		Summary:   "人工确认",
	}
	if err := db.CreateTask(ctx, store.Task{ID: "wait-running", ThreadID: &threadID, Status: string(TaskStatusWaiting), Meta: EncodeTaskMeta(meta)}); err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckWaits(ctx, now); err != nil {
		t.Fatal(err)
	}
	task, err := db.GetTask(ctx, "wait-running")
	if err != nil || task.Status != string(TaskStatusInProgress) {
		t.Fatalf("会话运行中时等待到期应回到 in_progress: %+v, %v", task, err)
	}
	gotMeta, err := ParseTaskMeta(task.Meta)
	if err != nil || gotMeta.WaitKind != "" || gotMeta.WaitCheck != "" || gotMeta.WakeAt != "" || gotMeta.WaitBase != "" || gotMeta.Wakes != 0 {
		t.Fatalf("等待元数据应已清空: %+v, %v", gotMeta, err)
	}
	decisions, err := db.ListStewardDecisions(ctx, "open", 0)
	if err != nil || len(decisions) != 0 {
		t.Fatalf("会话运行中不应创建决策卡: %+v, %v", decisions, err)
	}
}

func TestCheckWaitsExpiredWhileSessionStoppedCreatesDecision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	driver := newMockDriver()
	threadID := "thread-stopped"
	driver.setThreadState(threadID, SessionState{Running: false})
	svc, db, _, _ := setupReceiptTestService(t, driver, true, false, func() time.Time { return now })
	meta := TaskMeta{
		WaitKind: "human",
		WakeAt:   now.Add(-time.Minute).Format(time.RFC3339),
		Summary:  "人工确认",
	}
	if err := db.CreateTask(ctx, store.Task{ID: "wait-stopped", ThreadID: &threadID, Status: string(TaskStatusWaiting), Meta: EncodeTaskMeta(meta)}); err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckWaits(ctx, now); err != nil {
		t.Fatal(err)
	}
	task, err := db.GetTask(ctx, "wait-stopped")
	if err != nil || task.Status != string(TaskStatusNeedsApproval) {
		t.Fatalf("会话未运行时等待到期应升级为 needs_approval: %+v, %v", task, err)
	}
	decisions, err := db.ListStewardDecisions(ctx, "open", 0)
	if err != nil || len(decisions) != 1 || decisions[0].Question != "等待的人工处理已到期，是否继续等" {
		t.Fatalf("会话未运行时应创建等待到期决策卡: %+v, %v", decisions, err)
	}
}

func TestCheckWaitsMachineMissingProjectCreatesDecision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	svc, db, _, _ := setupReceiptTestService(t, newPokeDriver(), true, false, func() time.Time { return now })
	meta := TaskMeta{
		WaitKind:  "machine",
		WaitCheck: "true",
		WakeAt:    now.Add(-time.Minute).Format(time.RFC3339),
		Summary:   "等待服务就绪",
	}
	if err := db.CreateTask(ctx, store.Task{ID: "missing-project", Status: string(TaskStatusWaiting), Meta: EncodeTaskMeta(meta)}); err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckWaits(ctx, now); err != nil {
		t.Fatal(err)
	}
	task, err := db.GetTask(ctx, "missing-project")
	if err != nil || task.Status != string(TaskStatusNeedsApproval) {
		t.Fatalf("缺少项目目录应升级为 needs_approval: %+v, %v", task, err)
	}
	gotMeta, err := ParseTaskMeta(task.Meta)
	if err != nil || gotMeta.DecisionID == "" || gotMeta.WaitKind != "machine" {
		t.Fatalf("等待决策元数据异常: %+v, %v", gotMeta, err)
	}
	decisions, err := db.ListStewardDecisions(ctx, "open", 0)
	if err != nil || len(decisions) != 1 || decisions[0].Question != "等待检查无法执行：找不到项目目录，是否继续等" {
		t.Fatalf("缺少项目目录决策卡异常: %+v, %v", decisions, err)
	}
}
