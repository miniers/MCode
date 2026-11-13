package kanban

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"mcode/internal/store"
	"mcode/internal/testenv"
)

// coordinatorDriver 记录规划器的启动请求、会话状态与交互模式切换，供协调员相关测试使用。
type coordinatorDriver struct {
	mu           sync.Mutex
	starts       []SessionRequest
	states       map[string]SessionState
	defaultState SessionState
	modes        []string
	modeThreads  []string
}

func newCoordinatorDriver() *coordinatorDriver {
	return &coordinatorDriver{states: make(map[string]SessionState)}
}

func (d *coordinatorDriver) StartSession(ctx context.Context, req SessionRequest) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.starts = append(d.starts, req)
	threadID := uuid.NewString()
	if _, ok := d.states[threadID]; !ok {
		d.states[threadID] = d.defaultState
	}
	return threadID, nil
}

func (d *coordinatorDriver) SessionState(ctx context.Context, threadID string) (SessionState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if st, ok := d.states[threadID]; ok {
		return st, nil
	}
	return d.defaultState, nil
}

func (d *coordinatorDriver) SetInteractionMode(ctx context.Context, threadID, mode string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.modes = append(d.modes, mode)
	d.modeThreads = append(d.modeThreads, threadID)
	return nil
}

// snapshot 在锁内复制已记录内容，避免测试读取共享状态。
func (d *coordinatorDriver) snapshot() (starts []SessionRequest, modes []string, modeThreads []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	starts = append([]SessionRequest(nil), d.starts...)
	modes = append([]string(nil), d.modes...)
	modeThreads = append([]string(nil), d.modeThreads...)
	return starts, modes, modeThreads
}

func setupCoordinatorService(t *testing.T, worktreeDir string) (*Service, *store.Store) {
	t.Helper()
	testenv.TempHome(t)

	s, err := store.Open(filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	svc := NewService(Options{
		Store:       s,
		Projects:    &mockProjects{dir: t.TempDir()},
		WorktreeDir: worktreeDir,
	})
	t.Cleanup(func() { svc.Close() })
	return svc, s
}

func createCoordinatorGoal(t *testing.T, s *store.Store, status GoalStatus, planThreadID string) store.Goal {
	t.Helper()
	goal := store.Goal{
		ID:           "goal-" + uuid.NewString(),
		ProjectID:    "proj-1",
		Title:        "协调员目标标题",
		Prompt:       "协调员目标提示词",
		ContentHash:  "hash-" + uuid.NewString(),
		Status:       string(status),
		Policy:       map[string]any{"previewPlan": true},
		Progress:     GoalProgress(nil),
		PlanThreadID: &planThreadID,
	}
	if err := s.CreateGoal(context.Background(), goal); err != nil {
		t.Fatalf("创建目标失败: %v", err)
	}
	return goal
}

func TestCoordinatorPrefaceSkipsUnplannedGoal(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	svc, s := setupCoordinatorService(t, "")

	planning := createCoordinatorGoal(t, s, GoalStatusPlanning, "plan-thread-"+uuid.NewString())
	if got := svc.CoordinatorPreface(ctx, *planning.PlanThreadID); got != "" {
		t.Fatalf("规划中的目标不应注入 preface，实际: %q", got)
	}
	if got := svc.CoordinatorPreface(ctx, "missing-thread"); got != "" {
		t.Fatalf("未知会话不应注入 preface，实际: %q", got)
	}
}

func TestCoordinatorPrefaceIncludesGoalAndTasks(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	svc, s := setupCoordinatorService(t, "")

	goal := createCoordinatorGoal(t, s, GoalStatusPlanReview, "plan-thread-"+uuid.NewString())
	projectID := goal.ProjectID
	task := store.Task{
		ID:        "task-" + uuid.NewString(),
		ProjectID: &projectID,
		GoalID:    &goal.ID,
		Title:     "任务甲",
		Status:    string(TaskStatusTodo),
		Priority:  string(PriorityNone),
		DependsOn: []string{},
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	stored, err := s.GetGoal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("获取目标失败: %v", err)
	}
	if err := s.SetGoalTasks(ctx, goal.ID, []string{task.ID}, stored.Revision); err != nil {
		t.Fatalf("关联目标任务失败: %v", err)
	}

	preface := svc.CoordinatorPreface(ctx, *goal.PlanThreadID)
	if preface == "" {
		t.Fatal("规划完成的目标应返回 preface")
	}
	for _, want := range []string{goal.ID, goal.Title, task.Title} {
		if !strings.Contains(preface, want) {
			t.Fatalf("preface 缺少 %q:\n%s", want, preface)
		}
	}
}

func TestAgentPlannerStartsInPlanModeAndSwitchesToBuild(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()

	driver := newCoordinatorDriver()
	driver.defaultState = SessionState{
		LastAssistantText: `{"summary":"拆分完成","tasks":[{"title":"任务一","notes":"","dependsOn":[]}]}`,
	}
	planner := NewAgentPlanner(driver, &mockProjects{dir: t.TempDir()})
	planner.PollInterval = time.Millisecond

	res, err := planner.Plan(ctx, store.Goal{
		ID:        "goal-plan-mode",
		ProjectID: "proj-1",
		Title:     "规划模式目标",
		Prompt:    "把工作拆成任务",
	}, "")
	if err != nil {
		t.Fatalf("Planner.Plan 失败: %v", err)
	}
	if res.ThreadID == "" {
		t.Fatal("期望 Plan 返回会话 ID")
	}

	starts, modes, modeThreads := driver.snapshot()
	if len(starts) != 1 {
		t.Fatalf("期望 1 次 StartSession，实际: %d", len(starts))
	}
	if starts[0].InteractionMode != "plan" {
		t.Fatalf("期望 StartSession 的 InteractionMode 为 plan，实际: %q", starts[0].InteractionMode)
	}
	if len(modes) != 1 || modes[0] != "build" {
		t.Fatalf("期望规划成功后切换到 build，实际: %v", modes)
	}
	if len(modeThreads) != 1 || modeThreads[0] != res.ThreadID {
		t.Fatalf("期望对规划会话切换模式，实际: %v", modeThreads)
	}
}

func TestIsTaskWorktreeDir(t *testing.T) {
	testenv.TempHome(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	svc, _ := setupCoordinatorService(t, root)

	if !svc.IsTaskWorktreeDir(filepath.Join(root, "task-1")) {
		t.Fatal("任务 worktree 子目录应为 true")
	}
	if svc.IsTaskWorktreeDir(root) {
		t.Fatal("worktree 根目录本身不应视为任务目录")
	}
	if svc.IsTaskWorktreeDir(t.TempDir()) {
		t.Fatal("worktree 之外的目录应为 false")
	}

	noRoot, _ := setupCoordinatorService(t, "")
	if noRoot.IsTaskWorktreeDir(filepath.Join(root, "task-1")) {
		t.Fatal("未配置 WorktreeDir 时应为 false")
	}
}
