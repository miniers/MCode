package kanban

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/store"
)

// cancellingPlanner 在规划阶段取消目标，模拟「规划期间用户点了取消」。
type cancellingPlanner struct {
	svc *Service
}

func (p *cancellingPlanner) Plan(ctx context.Context, goal store.Goal, feedback string) (PlanResult, error) {
	if p.svc != nil {
		if _, err := p.svc.CancelGoal(ctx, goal.ID, goal.Revision); err != nil {
			return PlanResult{}, err
		}
	}
	return PlanResult{Summary: "取消后不应落地", Tasks: []PlannedTask{{Title: "幽灵任务"}}}, nil
}

// TestRunPlanRollsBackTasksWhenGoalCancelled：规划期间目标被取消时，本轮新建的任务要删掉，
// 不能留下没有 goal_tasks 关联、可被手工启动的幽灵卡片。
func TestRunPlanRollsBackTasksWhenGoalCancelled(t *testing.T) {
	ctx := context.Background()
	st := openMergeTestStore(t)
	planner := &cancellingPlanner{}
	svc := NewService(Options{Store: st, Driver: newMockDriver(), Projects: &mockProjects{dir: t.TempDir()}, Planner: planner})
	t.Cleanup(func() { svc.Close() })
	planner.svc = svc

	goal, err := svc.CreateGoal(ctx, "proj-1", "规划中途取消的目标")
	if err != nil {
		t.Fatalf("创建目标失败: %v", err)
	}
	svc.wg.Wait()

	latest, err := st.GetGoal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("读取目标失败: %v", err)
	}
	if latest.Status != string(GoalStatusCancelled) {
		t.Fatalf("期望目标保持 cancelled，实际: %s", latest.Status)
	}

	taskIDs, err := st.ListGoalTaskIDs(ctx, goal.ID)
	if err != nil {
		t.Fatalf("读取目标任务失败: %v", err)
	}
	if len(taskIDs) != 0 {
		t.Fatalf("取消后不应留下目标关联任务，实际: %v", taskIDs)
	}
	tasks, err := st.ListTasks(ctx, "")
	if err != nil {
		t.Fatalf("列出任务失败: %v", err)
	}
	for _, task := range tasks {
		if task.GoalID != nil && *task.GoalID == goal.ID {
			t.Fatalf("取消后不应留下幽灵任务 %s", task.ID)
		}
	}
}

// TestRecoverStalePlanningGoals：启动恢复时超时仍为 planning 的目标置为 plan_failed 并写原因，
// 刚创建的目标不受影响。
func TestRecoverStalePlanningGoals(t *testing.T) {
	ctx := context.Background()
	st := openMergeTestStore(t)
	svc := NewService(Options{Store: st})
	t.Cleanup(func() { svc.Close() })

	stale := store.Goal{
		ID:        "goal-stale-planning",
		ProjectID: "proj-1",
		Title:     "卡住的规划",
		Prompt:    "卡住的规划",
		Status:    string(GoalStatusPlanning),
		Policy:    map[string]any{},
		Progress:  map[string]any{},
		CreatedAt: time.Now().UTC().Add(-2 * planningGoalStaleAfter),
		UpdatedAt: time.Now().UTC().Add(-planningGoalStaleAfter - time.Minute),
	}
	fresh := store.Goal{
		ID:        "goal-fresh-planning",
		ProjectID: "proj-1",
		Title:     "刚创建的规划",
		Prompt:    "刚创建的规划",
		Status:    string(GoalStatusPlanning),
		Policy:    map[string]any{},
		Progress:  map[string]any{},
	}
	for _, g := range []store.Goal{stale, fresh} {
		if err := st.CreateGoal(ctx, g); err != nil {
			t.Fatalf("创建目标 %s 失败: %v", g.ID, err)
		}
	}

	if _, err := svc.RecoverOrphanTasks(ctx); err != nil {
		t.Fatalf("启动恢复失败: %v", err)
	}

	gotStale, err := st.GetGoal(ctx, stale.ID)
	if err != nil {
		t.Fatalf("读取目标失败: %v", err)
	}
	if gotStale.Status != string(GoalStatusPlanFailed) {
		t.Errorf("超时 planning 目标期望置为 plan_failed，实际: %s", gotStale.Status)
	}
	if gotStale.StatusReason == nil || strings.TrimSpace(*gotStale.StatusReason) == "" {
		t.Errorf("期望写入 plan_failed 原因，实际: %v", gotStale.StatusReason)
	}

	gotFresh, err := st.GetGoal(ctx, fresh.ID)
	if err != nil {
		t.Fatalf("读取目标失败: %v", err)
	}
	if gotFresh.Status != string(GoalStatusPlanning) {
		t.Errorf("刚创建的 planning 目标不应被改写，实际: %s", gotFresh.Status)
	}
}

// TestRunPlanFailsWhenMaterializedPromptUnreadable：落盘的目标提示词读不出来时，
// 目标要落到 plan_failed 并写原因，不能用库内摘要继续规划。
func TestRunPlanFailsWhenMaterializedPromptUnreadable(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	svc, st, planner := newGoalPromptTestService(t, dataDir)

	prompt := longGoalPrompt("落盘丢失：")
	goal, err := svc.CreateGoal(ctx, "proj-1", prompt)
	if err != nil {
		t.Fatalf("创建目标失败: %v", err)
	}
	svc.wg.Wait()

	stored, err := st.GetGoal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("读取目标失败: %v", err)
	}
	if !isMaterializedGoalPrompt(stored.Prompt) {
		t.Fatalf("前置条件：库内 Prompt 应为落盘摘要，实际: %q", stored.Prompt)
	}
	if err := os.Remove(filepath.Join(dataDir, "goals", promptContentHash(prompt)+".md")); err != nil {
		t.Fatalf("删除落盘文件失败: %v", err)
	}

	if _, err := svc.ReplanGoal(ctx, goal.ID, "重新规划", stored.Revision); err != nil {
		t.Fatalf("重新规划失败: %v", err)
	}
	svc.wg.Wait()

	latest, err := st.GetGoal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("读取目标失败: %v", err)
	}
	if latest.Status != string(GoalStatusPlanFailed) {
		t.Fatalf("期望目标置为 plan_failed，实际: %s", latest.Status)
	}
	if latest.StatusReason == nil || !strings.Contains(*latest.StatusReason, "读取失败") {
		t.Errorf("期望原因包含「读取失败」，实际: %v", latest.StatusReason)
	}
	if calls := len(planner.prompts); calls != 1 {
		t.Errorf("提示词读不出来时不应进入规划，规划调用次数: %d", calls)
	}
}
