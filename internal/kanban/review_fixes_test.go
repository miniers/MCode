package kanban

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"mcode/internal/store"
)

type twoTaskPlanner struct{}

func (p *twoTaskPlanner) Plan(ctx context.Context, goal store.Goal, feedback string) (PlanResult, error) {
	return PlanResult{
		Summary: "两个任务",
		Tasks: []PlannedTask{
			{Title: "任务一"},
			{Title: "任务二"},
		},
	}, nil
}

// TestPlanLandingCreateTaskFailureMarksPlanFailed 验证规划落地时任一 CreateTask 失败会停止落地、
// 删除已建卡片并把目标置为 plan_failed。
func TestPlanLandingCreateTaskFailureMarksPlanFailed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.OpenWithOptions(dbPath, store.OpenOptions{
		SkipQuickCheck: true,
		FastSync:       true,
	})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rawDB, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", dbPath))
	if err != nil {
		t.Fatalf("打开 SQLite 触发器连接失败: %v", err)
	}
	_, err = rawDB.Exec(`
		CREATE TABLE insert_log (title TEXT);
		CREATE TRIGGER log_task_insert AFTER INSERT ON tasks
		BEGIN
			INSERT INTO insert_log(title) VALUES (NEW.title);
		END;
		CREATE TRIGGER fail_second_task BEFORE INSERT ON tasks
		WHEN NEW.title = '任务二'
		BEGIN
			SELECT RAISE(ABORT, 'injected create task failure');
		END;
	`)
	_ = rawDB.Close()
	if err != nil {
		t.Fatalf("创建测试触发器失败: %v", err)
	}

	svc := NewService(Options{
		Store:    st,
		Driver:   newMockDriver(),
		Projects: &mockProjects{dir: t.TempDir()},
		Planner:  &twoTaskPlanner{},
	})
	t.Cleanup(func() { svc.Close() })

	goal, err := svc.CreateGoal(ctx, "proj-1", "测试建卡失败")
	if err != nil {
		t.Fatalf("创建目标失败: %v", err)
	}
	svc.wg.Wait()

	latest, err := st.GetGoal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("读取目标失败: %v", err)
	}
	if latest.Status != string(GoalStatusPlanFailed) {
		t.Fatalf("期望目标状态为 plan_failed，实际: %s", latest.Status)
	}
	if latest.StatusReason == nil || !strings.Contains(*latest.StatusReason, "injected create task failure") {
		t.Fatalf("期望状态原因包含失败信息，实际: %v", latest.StatusReason)
	}

	tasks, err := st.ListTasks(ctx, "")
	if err != nil {
		t.Fatalf("列出任务失败: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("期望第 1 张已建卡片被回滚删除，实际剩余: %+v", tasks)
	}

	taskIDs, err := st.ListGoalTaskIDs(ctx, goal.ID)
	if err != nil {
		t.Fatalf("读取目标关联任务失败: %v", err)
	}
	if len(taskIDs) != 0 {
		t.Fatalf("期望目标无关联任务，实际: %v", taskIDs)
	}

	checkDB, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", dbPath))
	if err != nil {
		t.Fatalf("打开校验数据库失败: %v", err)
	}
	defer func() { _ = checkDB.Close() }()
	var insertedCount int
	if err := checkDB.QueryRow(`SELECT COUNT(*) FROM insert_log WHERE title = '任务一'`).Scan(&insertedCount); err != nil {
		t.Fatalf("查询插入日志失败: %v", err)
	}
	if insertedCount != 1 {
		t.Fatalf("期望第 1 张卡片先建卡成功后再被删除，实际插入次数: %d", insertedCount)
	}
}

type blockingSetupDriver struct {
	*mockDriver
	startedOnce sync.Once
	started     chan struct{}
	release     chan struct{}
	mu          sync.Mutex
	ctxErr      error
}

func (d *blockingSetupDriver) RunWorktreeSetupScripts(ctx context.Context, projectID, projectRoot, worktreePath string) {
	d.startedOnce.Do(func() {
		close(d.started)
	})
	<-d.release
	d.mu.Lock()
	d.ctxErr = ctx.Err()
	d.mu.Unlock()
}

// TestCloseWaitsWorktreeSetup 验证 Close 会等待运行中的 worktree setup 协程退出。
func TestCloseWaitsWorktreeSetup(t *testing.T) {
	t.Parallel()
	st := store.OpenForTest(t)
	drv := &blockingSetupDriver{
		mockDriver: newMockDriver(),
		started:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	svc := NewService(Options{
		Store:  st,
		Driver: drv,
	})

	svc.runWorktreeSetup(context.Background(), "proj-1", "/repo", "/wt")

	select {
	case <-drv.started:
	case <-time.After(5 * time.Second):
		close(drv.release)
		t.Fatal("超时未进入 RunWorktreeSetupScripts")
	}

	closed := make(chan struct{})
	go func() {
		svc.Close()
		close(closed)
	}()

	select {
	case <-closed:
		close(drv.release)
		t.Fatal("Close 在 RunWorktreeSetupScripts 放行前提前返回")
	case <-time.After(50 * time.Millisecond):
	}

	close(drv.release)

	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close 在 RunWorktreeSetupScripts 放行后未返回")
	}

	drv.mu.Lock()
	ctxErr := drv.ctxErr
	drv.mu.Unlock()
	if ctxErr != nil {
		t.Fatalf("期望 RunWorktreeSetupScripts 保留 WithoutCancel 上下文，实际错误: %v", ctxErr)
	}
}
