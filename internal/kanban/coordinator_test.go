package kanban

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"mcode/internal/store"
)

// coordinatorDriver 记录规划器的启动请求、会话状态与交互模式切换，供协调员相关测试使用。
type coordinatorDriver struct {
	mu           sync.Mutex
	starts       []SessionRequest
	states       map[string]SessionState
	defaultState SessionState
	modes        []string
	modeThreads  []string
	promoted     []string
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

// PromoteReadOnlySession 记录被升档的会话，不修改其他状态。
func (d *coordinatorDriver) PromoteReadOnlySession(ctx context.Context, threadID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.promoted = append(d.promoted, threadID)
	return nil
}

// promotedThreads 在锁内复制被升档的会话列表。
func (d *coordinatorDriver) promotedThreads() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.promoted...)
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
	t.Setenv("HOME", t.TempDir())

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
	t.Setenv("HOME", t.TempDir())
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
	t.Setenv("HOME", t.TempDir())
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
	t.Setenv("HOME", t.TempDir())
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
	t.Setenv("HOME", t.TempDir())
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

// setupPromoteService 构造带驱动的看板服务，用于验证话题会话升档。
func setupPromoteService(t *testing.T, driver SessionDriver) (*Service, *store.Store) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	s, err := store.Open(filepath.Join(t.TempDir(), "promote.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	svc := NewService(Options{
		Store:    s,
		Projects: &mockProjects{dir: t.TempDir()},
		Driver:   driver,
	})
	t.Cleanup(func() { svc.Close() })
	return svc, s
}

// createPromoteGoal 直接在存储里造一个 plan_review 状态的目标，避免触发后台规划。
func createPromoteGoal(t *testing.T, s *store.Store, owner string, planThreadID *string) store.Goal {
	t.Helper()
	goal := store.Goal{
		ID:           "goal-" + uuid.NewString(),
		ProjectID:    "proj-1",
		Title:        "总管话题目标标题",
		Prompt:       "总管话题目标提示词",
		ContentHash:  "hash-" + uuid.NewString(),
		Status:       string(GoalStatusPlanReview),
		Owner:        owner,
		Policy:       map[string]any{},
		Progress:     GoalProgress(nil),
		PlanThreadID: planThreadID,
	}
	if err := s.CreateGoal(context.Background(), goal); err != nil {
		t.Fatalf("创建目标失败: %v", err)
	}
	// 重新读取，让 Revision 与存储层写入的一致，避免 StartGoal 报乐观锁冲突。
	stored, err := s.GetGoal(context.Background(), goal.ID)
	if err != nil {
		t.Fatalf("读取目标失败: %v", err)
	}
	return stored
}

// 只有总管话题目标在开工时把规划会话从只读档升到 auto，普通目标不动。
func TestStartGoalPromotesStewardTopicSession(t *testing.T) {
	cases := []struct {
		name         string
		owner        string
		planThreadID *string
		wantPromote  bool
	}{
		{name: "总管话题目标升档", owner: goalOwnerSteward, planThreadID: strPtr("plan-thread-" + uuid.NewString()), wantPromote: true},
		{name: "普通目标不升档", owner: "", planThreadID: strPtr("plan-thread-" + uuid.NewString()), wantPromote: false},
		{name: "无规划会话不升档", owner: goalOwnerSteward, planThreadID: nil, wantPromote: false},
		{name: "空白规划会话不升档", owner: goalOwnerSteward, planThreadID: strPtr("   "), wantPromote: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			driver := newCoordinatorDriver()
			svc, s := setupPromoteService(t, driver)

			goal := createPromoteGoal(t, s, tc.owner, tc.planThreadID)
			started, err := svc.StartGoal(ctx, goal.ID, goal.Revision)
			if err != nil {
				t.Fatalf("StartGoal 失败: %v", err)
			}
			if started.Status != string(GoalStatusRunning) {
				t.Fatalf("开工后状态应为 running，实际: %q", started.Status)
			}

			promoted := driver.promotedThreads()
			if tc.wantPromote {
				if len(promoted) != 1 || promoted[0] != strings.TrimSpace(*tc.planThreadID) {
					t.Fatalf("期望升档规划会话，实际: %v", promoted)
				}
				return
			}
			if len(promoted) != 0 {
				t.Fatalf("不应升档，实际: %v", promoted)
			}
		})
	}
}

// 升档失败只记日志，不能让开工失败；暂停后重复开工再次升档是安全的。
func TestStartGoalPromotionFailureKeepsGoalRunning(t *testing.T) {
	ctx := context.Background()
	driver := &failingPromoterDriver{coordinatorDriver: newCoordinatorDriver()}
	svc, s := setupPromoteService(t, driver)

	threadID := "plan-thread-" + uuid.NewString()
	goal := createPromoteGoal(t, s, goalOwnerSteward, &threadID)

	started, err := svc.StartGoal(ctx, goal.ID, goal.Revision)
	if err != nil {
		t.Fatalf("升档失败不应让开工失败: %v", err)
	}
	if started.Status != string(GoalStatusRunning) {
		t.Fatalf("开工后状态应为 running，实际: %q", started.Status)
	}

	paused, err := svc.PauseGoal(ctx, started.ID, started.Revision)
	if err != nil {
		t.Fatalf("PauseGoal 失败: %v", err)
	}
	if _, err := svc.StartGoal(ctx, paused.ID, paused.Revision); err != nil {
		t.Fatalf("恢复开工失败: %v", err)
	}
	if calls := driver.calls(); calls != 2 {
		t.Fatalf("每次开工都应尝试升档一次，实际: %d", calls)
	}
}

// failingPromoterDriver 让升档始终失败，用于验证开工不受影响。
type failingPromoterDriver struct {
	*coordinatorDriver
}

func (d *failingPromoterDriver) PromoteReadOnlySession(ctx context.Context, threadID string) error {
	// 先记录调用再返回错误，calls() 依赖 d.promoted 统计尝试次数。
	d.mu.Lock()
	d.promoted = append(d.promoted, threadID)
	d.mu.Unlock()
	return errTestPromoteFailed
}

func (d *failingPromoterDriver) calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.promoted)
}

var errTestPromoteFailed = errors.New("升级权限档失败")
