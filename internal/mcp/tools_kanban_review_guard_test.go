package mcp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/kanban"
	"mcode/internal/store"
)

// newReviewGuardServer 构造只带 Store 的服务：审批护栏只读任务与目标，不需要看板服务。
func newReviewGuardServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "review_guard.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return New(Options{Store: st}), st
}

// TestGoalReviewGuard 覆盖总管对自己目标下任务的审批护栏：
// 第二次打回与接受含 [你验] 的任务必须先问用户，普通会话与请求复核不受影响。
func TestGoalReviewGuard(t *testing.T) {
	ctx := context.Background()
	srv, st := newReviewGuardServer(t)

	projectID := "proj-guard"
	planThread := "thread-plan"
	goalID := "goal-guard"
	if err := st.CreateGoal(ctx, store.Goal{
		ID:           goalID,
		ProjectID:    projectID,
		Title:        "守护测试目标",
		Status:       string(kanban.GoalStatusRunning),
		Owner:        goalOwnerSteward,
		PlanThreadID: &planThread,
	}); err != nil {
		t.Fatalf("创建目标失败: %v", err)
	}
	otherGoalID := "goal-plain"
	if err := st.CreateGoal(ctx, store.Goal{
		ID:        otherGoalID,
		ProjectID: projectID,
		Title:     "普通目标",
		Status:    string(kanban.GoalStatusRunning),
	}); err != nil {
		t.Fatalf("创建普通目标失败: %v", err)
	}

	criteria := "- [命令] `go test ./...`\n- [你验] 手工点一遍界面"
	taskIDs := map[string]string{
		"steward": "task-steward",
		"plain":   "task-plain",
	}
	for name, id := range taskIDs {
		goalRef := goalID
		acceptance := criteria
		if name == "plain" {
			goalRef = otherGoalID
			acceptance = "- [审阅] 读一遍改动"
		}
		if err := st.CreateTask(ctx, store.Task{
			ID:                 id,
			ProjectID:          &projectID,
			GoalID:             &goalRef,
			Title:              "任务 " + name,
			Status:             string(kanban.TaskStatusReview),
			AcceptanceCriteria: acceptance,
			RejectCount:        1,
		}); err != nil {
			t.Fatalf("创建任务 %s 失败: %v", id, err)
		}
	}

	stewardTask, err := st.GetTask(ctx, taskIDs["steward"])
	if err != nil {
		t.Fatalf("读取总管任务失败: %v", err)
	}
	plainTask, err := st.GetTask(ctx, taskIDs["plain"])
	if err != nil {
		t.Fatalf("读取普通任务失败: %v", err)
	}
	firstReject := stewardTask
	firstReject.RejectCount = 0

	planCtx := context.WithValue(ctx, threadScopeKey{}, planThread)
	otherCtx := context.WithValue(ctx, threadScopeKey{}, "thread-other")
	newTC := func(c context.Context) ToolContext {
		return ToolContext{Context: c, Project: store.Project{ID: projectID}, Server: srv}
	}

	cases := []struct {
		name        string
		ctx         context.Context
		task        store.Task
		decision    kanban.ReviewDecision
		args        map[string]any
		wantBlocked bool
	}{
		{name: "总管第二次打回被拦", ctx: planCtx, task: stewardTask, decision: kanban.ReviewReject, wantBlocked: true},
		{name: "总管接受含你验被拦", ctx: planCtx, task: stewardTask, decision: kanban.ReviewAccept, wantBlocked: true},
		{name: "用户确认后放行", ctx: planCtx, task: stewardTask, decision: kanban.ReviewReject, args: map[string]any{"user_confirmed": true}},
		{name: "首次打回不拦", ctx: planCtx, task: firstReject, decision: kanban.ReviewReject},
		{name: "普通会话打回不受影响", ctx: otherCtx, task: stewardTask, decision: kanban.ReviewReject},
		{name: "请求复核不拦", ctx: planCtx, task: stewardTask, decision: kanban.ReviewRequest},
		{name: "非总管目标不拦", ctx: planCtx, task: plainTask, decision: kanban.ReviewAccept},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			guard := goalReviewGuard(newTC(tc.ctx), tc.task, tc.decision, tc.args)
			if gotBlocked := guard != nil; gotBlocked != tc.wantBlocked {
				t.Fatalf("拦截 = %v, 期望 %v（%+v）", gotBlocked, tc.wantBlocked, guard)
			}
			if guard == nil {
				return
			}
			if !guard.IsError {
				t.Fatalf("拦截结果应为错误结果: %+v", guard)
			}
			if !strings.Contains(guard.Text, "向用户说明情况") || !strings.Contains(guard.Text, "等待明确确认") {
				t.Fatalf("拦截说明缺少询问用户的指令: %s", guard.Text)
			}
			if !strings.Contains(guard.Text, "user_confirmed") {
				t.Fatalf("拦截说明缺少 user_confirmed 提示: %s", guard.Text)
			}
		})
	}
}
