package app

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/attention"
	"mcode/internal/automation"
	"mcode/internal/kanban"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"mcode/internal/swarm"
)

// attentionTestRig 是测试用的一套真实存储、待处理事件服务与接线。
type attentionTestRig struct {
	wiring *attentionWiring
	svc    *attention.Service
	hub    *remote.StreamHub
}

// newAttentionTestRig 用临时库构造待处理事件服务与接线，启动订阅并在测试结束时停止。
func newAttentionTestRig(t *testing.T, boards ...*kanban.Service) *attentionTestRig {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "attention.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	svc := attention.New(st, attention.Options{})
	hub := remote.NewStreamHub(remote.StreamHubOptions{Interval: 10 * time.Millisecond})
	wiring := newAttentionWiring(attentionWiringOptions{
		Service: svc,
		Hub:     hub,
		Now:     time.Now,
	})
	if len(boards) > 0 {
		wiring.kanban = boards[0]
	}
	wiring.Start()
	t.Cleanup(func() {
		wiring.Stop()
		hub.Close()
		if err := st.Close(); err != nil {
			t.Logf("close store: %v", err)
		}
	})
	return &attentionTestRig{wiring: wiring, svc: svc, hub: hub}
}

// listUnresolved 列出全部未消解事件。
func listUnresolved(t *testing.T, svc *attention.Service) []store.AttentionEvent {
	t.Helper()
	events, err := svc.List(context.Background(), store.AttentionEventFilter{UnresolvedOnly: true, Limit: 100})
	if err != nil {
		t.Fatalf("list attention events: %v", err)
	}
	return events
}

// waitAttentionEvent 等待出现指定类型与对象的未消解事件（写库在 worker goroutine 内异步发生）。
func waitAttentionEvent(t *testing.T, svc *attention.Service, kind attention.Kind, subjectID string) store.AttentionEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		for _, ev := range listUnresolved(t, svc) {
			if ev.Kind == string(kind) && ev.SubjectID == subjectID {
				return ev
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 %s/%s 的未消解事件超时", kind, subjectID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitAttentionResolved 等待指定类型与对象的未消解事件全部消解。
func waitAttentionResolved(t *testing.T, svc *attention.Service, kind attention.Kind, subjectID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		found := false
		for _, ev := range listUnresolved(t, svc) {
			if ev.Kind == string(kind) && ev.SubjectID == subjectID {
				found = true
				break
			}
		}
		if !found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 %s/%s 消解超时", kind, subjectID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// countAttentionEvents 统计指定类型与对象的事件总条数（含已消解）。
func countAttentionEvents(t *testing.T, svc *attention.Service, kind attention.Kind, subjectID string) int {
	t.Helper()
	events, err := svc.List(context.Background(), store.AttentionEventFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list attention events: %v", err)
	}
	count := 0
	for _, ev := range events {
		if ev.Kind == string(kind) && ev.SubjectID == subjectID {
			count++
		}
	}
	return count
}

// 会话事件源：出错记录、重新运行消解、审批记录与消解。
func TestAttentionWiringThreadEvents(t *testing.T) {
	rig := newAttentionTestRig(t)
	const threadID = "thread-attention-1"
	summary := func(running, pending bool) wire.ThreadSummary {
		return wire.ThreadSummary{
			ID:                threadID,
			ProjectID:         "project-1",
			Title:             "重构取数逻辑",
			IsRunning:         running,
			HasPendingRequest: pending,
		}
	}

	// 先进入运行中，探测器才有忙态基线。
	rig.hub.Notify(threadID, "thread.summary", summary(true, false))
	failed := summary(false, false)
	failed.Attention = "failed"
	rig.hub.Notify(threadID, "thread.summary", failed)

	event := waitAttentionEvent(t, rig.svc, attention.KindThreadFailed, threadID)
	if event.SubjectType != string(attention.SubjectThread) {
		t.Fatalf("SubjectType = %q, 期望 thread", event.SubjectType)
	}
	if event.ThreadID != threadID {
		t.Fatalf("ThreadID = %q, 期望 %q", event.ThreadID, threadID)
	}
	if event.Title != "重构取数逻辑" {
		t.Fatalf("Title = %q, 期望会话标题", event.Title)
	}
	if event.ProjectID != "project-1" {
		t.Fatalf("ProjectID = %q, 期望 project-1", event.ProjectID)
	}
	if event.Detail == "" {
		t.Fatalf("Detail 为空，期望带上提醒正文")
	}

	// 重新运行：失败事件自动消解。
	rig.hub.Notify(threadID, "thread.summary", summary(true, false))
	waitAttentionResolved(t, rig.svc, attention.KindThreadFailed, threadID)

	// 审批：新请求出现时记录一条审批事件。
	requestTitle := "允许执行 git push"
	rig.hub.Notify(threadID, "thread.summary", summary(true, true))
	rig.hub.Notify(threadID, "requests.changed", wire.RequestsChangedParams{
		ThreadID: threadID,
		Requests: []wire.RemoteRequest{{ThreadID: threadID, RequestID: "req-1", Title: &requestTitle}},
	})
	approval := waitAttentionEvent(t, rig.svc, attention.KindThreadApproval, threadID)
	if approval.Detail != requestTitle {
		t.Fatalf("Detail = %q, 期望审批正文 %q", approval.Detail, requestTitle)
	}

	// 待审批结束：审批事件自动消解。
	rig.hub.Notify(threadID, "thread.summary", summary(true, false))
	waitAttentionResolved(t, rig.svc, attention.KindThreadApproval, threadID)
}

// 被忽略的会话不产生任何事件。
func TestAttentionWiringIgnoresThread(t *testing.T) {
	rig := newAttentionTestRig(t)
	const threadID = "thread-attention-follow-up"
	rig.wiring.SetIgnoreThread(func(id string) bool { return id == threadID })

	rig.hub.Notify(threadID, "thread.summary", wire.ThreadSummary{ID: threadID, Title: "总管", IsRunning: true})
	rig.hub.Notify(threadID, "thread.summary", wire.ThreadSummary{ID: threadID, Title: "总管", Attention: "failed"})
	// 观察者只入队，给 worker 留出处理时间后确认没有任何事件。
	time.Sleep(200 * time.Millisecond)

	count, err := rig.svc.UnresolvedCount(context.Background())
	if err != nil {
		t.Fatalf("count unresolved: %v", err)
	}
	if count != 0 {
		t.Fatalf("被忽略的会话产生了 %d 条事件，期望 0 条", count)
	}
}

// 同一状态多次观察时 EnteredAt 保持不变，记录层只落一条。
func TestAttentionWiringThreadAlertEnteredAtStable(t *testing.T) {
	rig := newAttentionTestRig(t)
	const threadID = "thread-attention-2"
	alert := remote.SessionAlert{
		Kind:     remote.SessionAlertFailed,
		ThreadID: threadID,
		Title:    "会话出错",
		Body:     "构建失败",
	}
	for i := 0; i < 3; i++ {
		rig.wiring.mu.Lock()
		jobs := rig.wiring.alertJobsLocked(alert)
		rig.wiring.mu.Unlock()
		for _, job := range jobs {
			job()
		}
	}
	if got := countAttentionEvents(t, rig.svc, attention.KindThreadFailed, threadID); got != 1 {
		t.Fatalf("同一状态重复观察产生了 %d 条事件，期望 1 条", got)
	}
}

// hasKanbanAction 判断动作列表里是否有指定任务与类型的动作。
func hasKanbanAction(actions []kanbanAttentionAction, subjectID string, kind attention.Kind, resolve bool) bool {
	for _, action := range actions {
		if action.subjectID == subjectID && action.kind == kind && action.resolve == resolve {
			return true
		}
	}
	return false
}

// kanbanActionIndex 返回指定动作在列表中的下标，不存在时返回 -1。
func kanbanActionIndex(actions []kanbanAttentionAction, subjectID string, kind attention.Kind, resolve bool) int {
	for i, action := range actions {
		if action.subjectID == subjectID && action.kind == kind && action.resolve == resolve {
			return i
		}
	}
	return -1
}

// 看板快照差异：进入状态记事件、状态不变无动作、离开状态消解、失败原因变化先消解再记。
func TestDiffKanbanAttention(t *testing.T) {
	base := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	task := func(status, failure string) kanbanTaskSnapshot {
		return kanbanTaskSnapshot{
			Status:        status,
			Title:         "任务",
			ProjectID:     "project-1",
			ThreadID:      "thread-1",
			FailureReason: failure,
			UpdatedAt:     base,
		}
	}
	initial := map[string]kanbanTaskSnapshot{
		"task-1": task(string(kanban.TaskStatusNeedsApproval), ""),
		"task-2": task(string(kanban.TaskStatusInProgress), "编译失败"),
	}

	actions := diffKanbanAttention(nil, initial)
	if !hasKanbanAction(actions, "task-1", attention.KindKanbanNeedsApproval, false) {
		t.Fatalf("首次扫描缺少 task-1 的待审批事件: %+v", actions)
	}
	if !hasKanbanAction(actions, "task-2", attention.KindKanbanFailed, false) {
		t.Fatalf("首次扫描缺少 task-2 的失败事件: %+v", actions)
	}

	// 状态没有变化：不产生任何动作。
	if got := diffKanbanAttention(initial, initial); len(got) != 0 {
		t.Fatalf("状态未变时产生了 %d 个动作，期望 0 个", len(got))
	}

	// 离开待审批：只消解，不重复记录。
	left := map[string]kanbanTaskSnapshot{
		"task-1": task(string(kanban.TaskStatusInProgress), ""),
		"task-2": task(string(kanban.TaskStatusInProgress), "编译失败"),
	}
	actions = diffKanbanAttention(initial, left)
	if !hasKanbanAction(actions, "task-1", attention.KindKanbanNeedsApproval, true) {
		t.Fatalf("离开待审批时缺少消解动作: %+v", actions)
	}
	if hasKanbanAction(actions, "task-1", attention.KindKanbanNeedsApproval, false) {
		t.Fatalf("离开待审批时不应重复记录: %+v", actions)
	}

	// 失败原因变化：先消解旧事件，再记录新事件，避免新事件被一起消解。
	reasonChanged := map[string]kanbanTaskSnapshot{
		"task-1": task(string(kanban.TaskStatusNeedsApproval), ""),
		"task-2": task(string(kanban.TaskStatusInProgress), "测试失败"),
	}
	actions = diffKanbanAttention(initial, reasonChanged)
	resolveAt := kanbanActionIndex(actions, "task-2", attention.KindKanbanFailed, true)
	recordAt := kanbanActionIndex(actions, "task-2", attention.KindKanbanFailed, false)
	if resolveAt < 0 || recordAt < 0 {
		t.Fatalf("失败原因变化时缺少消解或记录动作: %+v", actions)
	}
	if resolveAt > recordAt {
		t.Fatalf("失败原因变化时应先消解旧事件再记录新事件，实际消解在下标 %d、记录在下标 %d", resolveAt, recordAt)
	}

	// 任务消失：消解它留下的全部事件。
	actions = diffKanbanAttention(initial, map[string]kanbanTaskSnapshot{})
	for _, kind := range kanbanAttentionKinds {
		if !hasKanbanAction(actions, "task-1", kind, true) {
			t.Fatalf("任务消失时缺少 %s 的消解动作: %+v", kind, actions)
		}
	}
}

func TestPlanningGoalOwnerRetryAttention(t *testing.T) {
	ctx := context.Background()
	db, _, _ := newFrontdeskAdapterFixture(t)
	planThread, projectID, goalID, reason := "plan", "project", "planning", "执行失败"
	if err := db.CreateGoal(ctx, store.Goal{ID: goalID, ProjectID: projectID, Title: "总管", Owner: "steward", Status: "running", PlanThreadID: &planThread}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(ctx, store.Task{ID: "task", ProjectID: &projectID, GoalID: &goalID, Title: "任务", Status: "todo", LastFailureReason: &reason, Meta: kanban.EncodeTaskMeta(kanban.TaskMeta{Origin: "owner"})}); err != nil {
		t.Fatal(err)
	}
	rig := newAttentionTestRig(t, kanban.NewService(kanban.Options{Store: db}))
	rig.wiring.scanKanban()
	waitAttentionEvent(t, rig.svc, attention.KindKanbanFailed, "task")
}

func TestStewardRetryAttention(t *testing.T) {
	reason := "执行失败"
	task := store.Task{ID: "task", Status: "todo", LastFailureReason: &reason, Meta: kanban.EncodeTaskMeta(kanban.TaskMeta{Origin: "owner"})}
	retry := kanbanSnapshotForGoal(task, true)
	if actions := diffKanbanAttention(nil, map[string]kanbanTaskSnapshot{task.ID: retry}); len(actions) != 0 {
		t.Fatalf("重试不应产生失败提醒: %+v", actions)
	}
	task.Status = "failed"
	failed := kanbanSnapshotForGoal(task, true)
	if actions := diffKanbanAttention(map[string]kanbanTaskSnapshot{task.ID: retry}, map[string]kanbanTaskSnapshot{task.ID: failed}); !hasKanbanAction(actions, task.ID, attention.KindKanbanFailed, false) {
		t.Fatalf("最终失败应产生提醒: %+v", actions)
	}
	task.Status = "needs_approval"
	if actions := diffKanbanAttention(nil, map[string]kanbanTaskSnapshot{task.ID: kanbanSnapshotForGoal(task, true)}); !hasKanbanAction(actions, task.ID, attention.KindKanbanNeedsApproval, false) {
		t.Fatalf("真实审批应保留: %+v", actions)
	}
	task.Status, task.LastFailureReason = "done", nil
	if actions := diffKanbanAttention(nil, map[string]kanbanTaskSnapshot{task.ID: kanbanSnapshotForGoal(task, true)}); len(actions) != 0 {
		t.Fatalf("完成不应产生验收提醒: %+v", actions)
	}
	task.Status, task.LastFailureReason = "todo", &reason
	if actions := diffKanbanAttention(nil, map[string]kanbanTaskSnapshot{task.ID: kanbanSnapshotForGoal(task, false)}); !hasKanbanAction(actions, task.ID, attention.KindKanbanFailed, false) {
		t.Fatalf("普通目标的失败提醒应保留: %+v", actions)
	}
}

// hasKanbanGoalAction 判断目标动作列表里是否有指定目标与类型的动作。
func hasKanbanGoalAction(actions []kanbanGoalAttentionAction, subjectID string, kind attention.Kind, resolve bool) bool {
	return kanbanGoalActionIndex(actions, subjectID, kind, resolve) >= 0
}

// kanbanGoalActionIndex 返回指定目标动作在列表中的下标，不存在时返回 -1。
func kanbanGoalActionIndex(actions []kanbanGoalAttentionAction, subjectID string, kind attention.Kind, resolve bool) int {
	for i, action := range actions {
		if action.subjectID == subjectID && action.kind == kind && action.resolve == resolve {
			return i
		}
	}
	return -1
}

// 目标快照差异：进入规划失败记事件、状态不变无动作、原因变化先消解再记、离开状态消解。
func TestDiffKanbanGoalAttention(t *testing.T) {
	base := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	goal := func(status, reason string) kanbanGoalSnapshot {
		return kanbanGoalSnapshot{
			Status:    status,
			Title:     "目标",
			ProjectID: "project-1",
			Reason:    reason,
			UpdatedAt: base,
		}
	}
	initial := map[string]kanbanGoalSnapshot{
		"goal-1": goal(string(kanban.GoalStatusPlanFailed), "规划超时"),
		"goal-2": goal(string(kanban.GoalStatusRunning), ""),
	}

	actions := diffKanbanGoalAttention(nil, initial)
	if !hasKanbanGoalAction(actions, "goal-1", attention.KindKanbanPlanFailed, false) {
		t.Fatalf("首次扫描缺少 goal-1 的规划失败事件: %+v", actions)
	}
	if hasKanbanGoalAction(actions, "goal-2", attention.KindKanbanPlanFailed, false) {
		t.Fatalf("运行中的目标不应产生事件: %+v", actions)
	}

	// 状态没有变化：不产生任何动作。
	if got := diffKanbanGoalAttention(initial, initial); len(got) != 0 {
		t.Fatalf("状态未变时产生了 %d 个动作，期望 0 个", len(got))
	}

	// 重新规划：离开 plan_failed 只消解，不重复记录。
	replanned := map[string]kanbanGoalSnapshot{
		"goal-1": goal(string(kanban.GoalStatusPlanning), ""),
		"goal-2": goal(string(kanban.GoalStatusRunning), ""),
	}
	actions = diffKanbanGoalAttention(initial, replanned)
	if !hasKanbanGoalAction(actions, "goal-1", attention.KindKanbanPlanFailed, true) {
		t.Fatalf("离开规划失败时缺少消解动作: %+v", actions)
	}
	if hasKanbanGoalAction(actions, "goal-1", attention.KindKanbanPlanFailed, false) {
		t.Fatalf("离开规划失败时不应重复记录: %+v", actions)
	}

	// 失败原因变化：先消解旧事件，再记录新事件，避免新事件被一起消解。
	reasonChanged := map[string]kanbanGoalSnapshot{
		"goal-1": goal(string(kanban.GoalStatusPlanFailed), "规划器报错"),
		"goal-2": goal(string(kanban.GoalStatusRunning), ""),
	}
	actions = diffKanbanGoalAttention(initial, reasonChanged)
	resolveAt := kanbanGoalActionIndex(actions, "goal-1", attention.KindKanbanPlanFailed, true)
	recordAt := kanbanGoalActionIndex(actions, "goal-1", attention.KindKanbanPlanFailed, false)
	if resolveAt < 0 || recordAt < 0 {
		t.Fatalf("失败原因变化时缺少消解或记录动作: %+v", actions)
	}
	if resolveAt > recordAt {
		t.Fatalf("失败原因变化时应先消解旧事件再记录新事件，实际消解在下标 %d、记录在下标 %d", resolveAt, recordAt)
	}

	// 目标消失：消解它留下的全部事件。
	actions = diffKanbanGoalAttention(initial, map[string]kanbanGoalSnapshot{})
	for _, kind := range kanbanGoalAttentionKinds {
		if !hasKanbanGoalAction(actions, "goal-1", kind, true) {
			t.Fatalf("目标消失时缺少 %s 的消解动作: %+v", kind, actions)
		}
	}
}

// 目标规划失败接入待处理事件：正文取 StatusReason，重复应用同一快照不重复记录，重新规划后消解。
func TestAttentionWiringKanbanPlanFailed(t *testing.T) {
	rig := newAttentionTestRig(t)
	const goalID = "goal-plan-failed"
	const reason = "规划会话执行失败"
	stamp := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	failed := map[string]kanbanGoalSnapshot{
		goalID: {
			Status:    string(kanban.GoalStatusPlanFailed),
			Title:     "重构收件箱",
			ProjectID: "project-1",
			Reason:    reason,
			UpdatedAt: stamp,
		},
	}

	rig.wiring.applyKanbanGoalActions(diffKanbanGoalAttention(nil, failed))
	event := waitAttentionEvent(t, rig.svc, attention.KindKanbanPlanFailed, goalID)
	if event.Detail != reason {
		t.Fatalf("Detail = %q, 期望目标 StatusReason %q", event.Detail, reason)
	}
	if event.SubjectType != string(attention.SubjectKanbanGoal) {
		t.Fatalf("SubjectType = %q, 期望 %q", event.SubjectType, attention.SubjectKanbanGoal)
	}
	if event.Title != "重构收件箱" || event.ProjectID != "project-1" {
		t.Fatalf("Title/ProjectID = %q/%q, 与目标快照不一致", event.Title, event.ProjectID)
	}

	// 重复应用同一快照：去重键相同，不会产生第二条。
	rig.wiring.applyKanbanGoalActions(diffKanbanGoalAttention(failed, failed))
	rig.wiring.applyKanbanGoalActions(diffKanbanGoalAttention(nil, failed))
	if got := countAttentionEvents(t, rig.svc, attention.KindKanbanPlanFailed, goalID); got != 1 {
		t.Fatalf("重复扫描产生了 %d 条事件，期望 1 条", got)
	}

	// 重新规划：离开 plan_failed 后自动消解。
	planning := map[string]kanbanGoalSnapshot{
		goalID: {
			Status:    string(kanban.GoalStatusPlanning),
			Title:     "重构收件箱",
			ProjectID: "project-1",
			UpdatedAt: stamp.Add(time.Second),
		},
	}
	rig.wiring.applyKanbanGoalActions(diffKanbanGoalAttention(failed, planning))
	waitAttentionResolved(t, rig.svc, attention.KindKanbanPlanFailed, goalID)
}

// automationAttentionInput 映射结果：失败仍产出失败事件，成功不产出事件，运行中不记录。
func TestAutomationAttentionInput(t *testing.T) {
	started := time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC)
	finished := started.Add(2 * time.Minute)
	threadID := "thread-automation-1"
	projectID := "project-1"
	errText := "退出码 1"
	def := automation.Definition{Automation: store.Automation{ID: "auto-1", ProjectID: &projectID, Name: "夜间巡检"}}

	failedRun := store.AutomationRun{
		ID:         "run-1",
		ThreadID:   &threadID,
		Status:     automation.RunFailed,
		StartedAt:  started,
		FinishedAt: &finished,
		Error:      &errText,
	}
	input, ok := automationAttentionInput(def, failedRun)
	if !ok {
		t.Fatalf("失败的运行应映射成事件")
	}
	if input.Kind != attention.KindAutomationFailed || input.SubjectType != attention.SubjectAutomationRun {
		t.Fatalf("Kind/SubjectType = %s/%s, 期望 automation_failed/automation_run", input.Kind, input.SubjectType)
	}
	if input.SubjectID != "run-1" || input.ThreadID != threadID || input.ProjectID != projectID {
		t.Fatalf("SubjectID/ThreadID/ProjectID = %s/%s/%s, 与运行记录不一致", input.SubjectID, input.ThreadID, input.ProjectID)
	}
	if input.Title != "夜间巡检" || input.Detail != errText {
		t.Fatalf("Title/Detail = %q/%q, 期望自动化名称与错误摘要", input.Title, input.Detail)
	}
	if !input.EnteredAt.Equal(finished) {
		t.Fatalf("EnteredAt = %v, 期望取结束时间 %v", input.EnteredAt, finished)
	}

	summaryText := "发现 3 个问题"
	successRun := failedRun
	successRun.ID = "run-2"
	successRun.Status = automation.RunSucceeded
	successRun.ResultSummary = &summaryText
	if successInput, ok := automationAttentionInput(def, successRun); ok {
		t.Fatalf("成功的运行不需要人处理，不应产生待处理事件，实际得到 %+v", successInput)
	}

	runningRun := failedRun
	runningRun.ID = "run-3"
	runningRun.Status = automation.RunRunning
	if _, ok := automationAttentionInput(def, runningRun); ok {
		t.Fatalf("运行中的记录不应产生待处理事件")
	}
}

// bee 快照差异：首次观察只建基线、新进入失败记一条、已失败不重复。
func TestDiffBeeFailures(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	failedAt := now.Add(-time.Minute)
	bees := []swarm.Bee{{
		ThreadID:   "bee-1",
		Status:     swarm.StatusFailed,
		Task:       "重构登录",
		Summary:    "编译失败",
		ProjectID:  "project-1",
		FinishedAt: &failedAt,
	}}

	if got := diffBeeFailures(nil, bees, "lead-1", now); len(got) != 0 {
		t.Fatalf("首次观察应只建立基线，实际产出 %d 条失败", len(got))
	}

	failures := diffBeeFailures(map[string]string{"bee-1": string(swarm.StatusRunning)}, bees, "lead-1", now)
	if len(failures) != 1 {
		t.Fatalf("运行中转为失败应产出 1 条失败，实际 %d 条", len(failures))
	}
	failure := failures[0]
	if failure.SubjectID != "bee-1" || failure.ThreadID != "lead-1" || failure.ProjectID != "project-1" {
		t.Fatalf("SubjectID/ThreadID/ProjectID = %s/%s/%s, 期望 bee 会话与 lead 会话", failure.SubjectID, failure.ThreadID, failure.ProjectID)
	}
	if failure.Title != "重构登录" || failure.Detail != "编译失败" {
		t.Fatalf("Title/Detail = %q/%q, 期望 bee 任务标题与失败摘要", failure.Title, failure.Detail)
	}
	if !failure.EnteredAt.Equal(failedAt) {
		t.Fatalf("EnteredAt = %v, 期望取 bee 结束时间 %v", failure.EnteredAt, failedAt)
	}

	if got := diffBeeFailures(map[string]string{"bee-1": string(swarm.StatusFailed)}, bees, "lead-1", now); len(got) != 0 {
		t.Fatalf("已失败的 bee 不应重复产出事件，实际 %d 条", len(got))
	}
}

// bee 快照差异：首次观察补发待审查提醒、新进入待审查才提醒、同一 bee 不重复、AI 审查中不提醒。
func TestDiffMergeReviewPending(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	finishedAt := now.Add(-2 * time.Minute)
	bees := []swarm.Bee{{
		ThreadID:     "bee-1",
		Name:         "Bee1",
		Task:         "重构登录",
		ProjectID:    "project-1",
		PendingMerge: true,
		ReviewState:  swarm.ReviewNeedsHuman,
		ReviewReason: "需要人工确认",
		FinishedAt:   &finishedAt,
	}}

	// 首次观察（例如 MCode 重启后）也要为已在等待人工审查的 bee 补发提醒，
	// EnteredAt 取 bee 结束时刻，保证重启前后算出的去重键一致。
	pending, cleared := diffMergeReviewPending(nil, bees, "lead-1", now)
	if len(cleared) != 0 {
		t.Fatalf("首次观察不应产生消解，实际 %d 条", len(cleared))
	}
	if len(pending) != 1 {
		t.Fatalf("首次观察应补发 1 条待审查提醒，实际 %d 条", len(pending))
	}
	if !pending[0].EnteredAt.Equal(finishedAt) {
		t.Fatalf("EnteredAt = %v, 期望取 bee 结束时间 %v", pending[0].EnteredAt, finishedAt)
	}

	running := []swarm.Bee{bees[0]}
	running[0].ReviewState = swarm.ReviewRunning
	if pending, cleared := diffMergeReviewPending(nil, running, "lead-1", now); len(pending) != 0 || len(cleared) != 0 {
		t.Fatalf("首次观察时 AI 审查中的 bee 不应提醒，实际产出 %d 条提醒、%d 条消解", len(pending), len(cleared))
	}

	pending, cleared = diffMergeReviewPending(map[string]bool{"bee-1": false}, bees, "lead-1", now)
	if len(cleared) != 0 {
		t.Fatalf("进入待审查不应产生消解，实际 %d 条", len(cleared))
	}
	if len(pending) != 1 {
		t.Fatalf("新进入待审查应产出 1 条提醒，实际 %d 条", len(pending))
	}
	review := pending[0]
	if review.SubjectID != "bee-1" || review.ThreadID != "lead-1" || review.ProjectID != "project-1" {
		t.Fatalf("SubjectID/ThreadID/ProjectID = %s/%s/%s, 期望 bee 会话与 lead 会话", review.SubjectID, review.ThreadID, review.ProjectID)
	}
	if review.Title != "有改动待审查" {
		t.Fatalf("Title = %q, 期望 %q", review.Title, "有改动待审查")
	}
	if review.Detail != "Bee1：重构登录；审查意见：需要人工确认" {
		t.Fatalf("Detail = %q, 期望包含 bee 名、任务与审查意见", review.Detail)
	}
	if !review.EnteredAt.Equal(finishedAt) {
		t.Fatalf("EnteredAt = %v, 期望取 bee 结束时间 %v", review.EnteredAt, finishedAt)
	}

	if pending, _ := diffMergeReviewPending(map[string]bool{"bee-1": true}, bees, "lead-1", now); len(pending) != 0 {
		t.Fatalf("已处于待审查的 bee 不应重复产出提醒，实际 %d 条", len(pending))
	}

	if pending, _ := diffMergeReviewPending(map[string]bool{"bee-1": false}, running, "lead-1", now); len(pending) != 0 {
		t.Fatalf("AI 审查中的 bee 不应产生提醒，实际 %d 条", len(pending))
	}
}

// 重启后补发：两个新的接线实例各观察同一个待审查 bee 一次，去重键相同，最终只落一条记录。
func TestAttentionWiringMergeReviewRestartDedup(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "attention.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Logf("close store: %v", err)
		}
	})
	observedAt := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	finishedAt := observedAt.Add(-5 * time.Minute)
	svc := attention.New(st, attention.Options{})
	leadID := "lead-restart"
	record := swarm.LeadRecord{
		LeadThreadID: leadID,
		Enabled:      true,
		Bees: []swarm.Bee{{
			ThreadID:     "bee-restart",
			Name:         "Bee1",
			Task:         "重构登录",
			ProjectID:    "project-1",
			Status:       swarm.StatusCompleted,
			PendingMerge: true,
			ReviewState:  swarm.ReviewNeedsHuman,
			FinishedAt:   &finishedAt,
		}},
	}
	// 每次循环都新建 Manager 与接线，模拟 MCode 重启后重新观察同一份持久化状态。
	for i := 0; i < 2; i++ {
		mgr := swarm.NewManager(swarm.Options{Persister: staticLeadPersister{records: []swarm.LeadRecord{record}}})
		if err := mgr.Restore(ctx); err != nil {
			t.Fatalf("restore swarm: %v", err)
		}
		wiring := newAttentionWiring(attentionWiringOptions{
			Service: svc,
			Swarm:   mgr,
			Now:     func() time.Time { return observedAt },
		})
		wiring.handleBeeLead(leadID)
	}
	events, err := svc.List(ctx, store.AttentionEventFilter{UnresolvedOnly: true, Limit: 100})
	if err != nil {
		t.Fatalf("list attention events: %v", err)
	}
	count := 0
	for _, event := range events {
		if event.Kind == string(attention.KindMergeReviewPending) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("重启两次观察同一 bee 应只有 1 条待审查提醒，实际 %d 条", count)
	}
}

// automationSuccessNotice 组装通知文本：成功有摘要用摘要、无摘要退回「运行完成」、
// 失败与其他状态不通知、过长摘要按字数截断。
func TestAutomationSuccessNotice(t *testing.T) {
	longSummary := strings.Repeat("巡", automationNoticeSummaryLimit+50)
	emptySummary := "   "
	summaryText := "全部检查通过"
	failedText := "退出码 1"
	def := automation.Definition{Automation: store.Automation{ID: "auto-notice", Name: "夜间巡检"}}

	for _, tc := range []struct {
		name       string
		run        store.AutomationRun
		wantTitle  string
		wantBody   string
		wantOK     bool
		wantRunes  int
		bodySuffix string
	}{
		{
			name:      "成功有摘要",
			run:       store.AutomationRun{ID: "run-1", Status: automation.RunSucceeded, ResultSummary: &summaryText},
			wantTitle: "自动化完成：夜间巡检",
			wantBody:  summaryText,
			wantOK:    true,
		},
		{
			name:      "成功无摘要",
			run:       store.AutomationRun{ID: "run-2", Status: automation.RunSucceeded},
			wantTitle: "自动化完成：夜间巡检",
			wantBody:  "运行完成",
			wantOK:    true,
		},
		{
			name:      "成功摘要全是空白",
			run:       store.AutomationRun{ID: "run-3", Status: automation.RunSucceeded, ResultSummary: &emptySummary},
			wantTitle: "自动化完成：夜间巡检",
			wantBody:  "运行完成",
			wantOK:    true,
		},
		{
			name:   "失败不通知",
			run:    store.AutomationRun{ID: "run-4", Status: automation.RunFailed, Error: &failedText},
			wantOK: false,
		},
		{
			name:       "摘要过长被截断",
			run:        store.AutomationRun{ID: "run-5", Status: automation.RunSucceeded, ResultSummary: &longSummary},
			wantTitle:  "自动化完成：夜间巡检",
			wantOK:     true,
			wantRunes:  automationNoticeSummaryLimit + 1,
			bodySuffix: "…",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			title, body, ok := automationSuccessNotice(def, tc.run)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, 期望 %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if title != tc.wantTitle {
				t.Fatalf("title = %q, 期望 %q", title, tc.wantTitle)
			}
			if tc.wantRunes > 0 {
				if runes := []rune(body); len(runes) != tc.wantRunes {
					t.Fatalf("body 长度 = %d 字, 期望 %d 字", len(runes), tc.wantRunes)
				}
				if !strings.HasSuffix(body, tc.bodySuffix) {
					t.Fatalf("body = %q, 期望以 %q 结尾", body, tc.bodySuffix)
				}
				return
			}
			if body != tc.wantBody {
				t.Fatalf("body = %q, 期望 %q", body, tc.wantBody)
			}
		})
	}
}

// HandleAutomationRun 成功时发一条通知且不建事件；失败时不发通知、照旧记录失败事件。
func TestHandleAutomationRunNotify(t *testing.T) {
	rig := newAttentionTestRig(t)
	threadID := "thread-automation-notify"
	summary := "全部检查通过"
	errText := "退出码 1"
	def := automation.Definition{Automation: store.Automation{ID: "auto-notify", Name: "夜间巡检"}}

	var mu sync.Mutex
	var notices []string
	rig.wiring.setAutomationNotify(func(title, body, threadID string) {
		mu.Lock()
		defer mu.Unlock()
		notices = append(notices, title+"|"+body+"|"+threadID)
	})

	successRun := store.AutomationRun{
		ID:            "run-ok",
		ThreadID:      &threadID,
		Status:        automation.RunSucceeded,
		StartedAt:     time.Now(),
		ResultSummary: &summary,
	}
	rig.wiring.HandleAutomationRun(def, successRun)

	mu.Lock()
	got := append([]string(nil), notices...)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("成功运行应发 1 条通知，实际 %d 条：%v", len(got), got)
	}
	want := "自动化完成：夜间巡检|全部检查通过|" + threadID
	if got[0] != want {
		t.Fatalf("通知 = %q, 期望 %q", got[0], want)
	}
	if events := listUnresolved(t, rig.svc); len(events) != 0 {
		t.Fatalf("成功运行不应产生待处理事件，实际 %d 条", len(events))
	}

	failedRun := successRun
	failedRun.ID = "run-bad"
	failedRun.Status = automation.RunFailed
	failedRun.ResultSummary = nil
	failedRun.Error = &errText
	rig.wiring.HandleAutomationRun(def, failedRun)

	waitAttentionEvent(t, rig.svc, attention.KindAutomationFailed, "run-bad")
	mu.Lock()
	after := len(notices)
	mu.Unlock()
	if after != 1 {
		t.Fatalf("失败运行不应发完成通知，实际通知总数为 %d 条", after)
	}
}

// 宿主自拼的通知标题按 hostNoticeTitleMaxRunes 截断：很长的自动化名字不会撑爆标题，
// 短名字保持原样。
func TestAutomationSuccessNoticeTruncatesTitle(t *testing.T) {
	longName := strings.Repeat("巡", hostNoticeTitleMaxRunes+20)
	summary := "全部检查通过"
	run := store.AutomationRun{ID: "run-long-name", Status: automation.RunSucceeded, ResultSummary: &summary}

	def := automation.Definition{Automation: store.Automation{ID: "auto-long", Name: longName}}
	title, body, ok := automationSuccessNotice(def, run)
	if !ok {
		t.Fatal("成功运行应产出通知")
	}
	if runes := []rune(title); len(runes) > hostNoticeTitleMaxRunes {
		t.Fatalf("标题长度 = %d 字, 期望不超过 %d 字", len(runes), hostNoticeTitleMaxRunes)
	}
	if !strings.HasPrefix(title, "自动化完成：") || !strings.HasSuffix(title, "…") {
		t.Fatalf("标题 = %q, 期望以「自动化完成：」开头并以省略号结尾", title)
	}
	if body != summary {
		t.Fatalf("正文 = %q, 期望短摘要不被改动", body)
	}

	shortDef := automation.Definition{Automation: store.Automation{ID: "auto-short", Name: "夜间巡检"}}
	if title, _, _ := automationSuccessNotice(shortDef, run); title != "自动化完成：夜间巡检" {
		t.Fatalf("短标题 = %q, 期望不被改动", title)
	}
}

// automationStoppedNotice 判定自动化是否已停用并组装通知：连续失败、次数上限、停止条件
// 与一次性任务结束都要通知；手动暂停、启用中的任务不通知；正文优先停止原因，其次本轮错误。
func TestAutomationStoppedNotice(t *testing.T) {
	errText := "退出码 1"
	def := func(enabled bool, status, stopReason string) automation.Definition {
		return automation.Definition{
			Automation: store.Automation{ID: "auto-stopped", Name: "夜间巡检", Enabled: enabled, Status: status},
			Spec:       automation.Spec{State: automation.RunState{StopReason: stopReason}},
		}
	}

	for _, tc := range []struct {
		name      string
		def       automation.Definition
		run       store.AutomationRun
		wantTitle string
		wantBody  string
		wantOK    bool
	}{
		{
			name:      "连续失败停用",
			def:       def(false, automation.StatusFailed, "Paused: Reached maximum consecutive failure limit (3)."),
			run:       store.AutomationRun{ID: "run-fail", Status: automation.RunFailed},
			wantTitle: "自动化已停止：夜间巡检",
			wantBody:  "Paused: Reached maximum consecutive failure limit (3).",
			wantOK:    true,
		},
		{
			name:      "maxRuns 停用",
			def:       def(false, automation.StatusCompleted, "Completed: Reached maximum run count (5)."),
			run:       store.AutomationRun{ID: "run-cap", Status: automation.RunSucceeded},
			wantTitle: "自动化已停止：夜间巡检",
			wantBody:  "Completed: Reached maximum run count (5).",
			wantOK:    true,
		},
		{
			name:      "stopWhen 满足",
			def:       def(false, automation.StatusCompleted, "Completed: Stop condition satisfied (all tests pass)."),
			run:       store.AutomationRun{ID: "run-stop", Status: automation.RunSucceeded},
			wantTitle: "自动化已停止：夜间巡检",
			wantBody:  "Completed: Stop condition satisfied (all tests pass).",
			wantOK:    true,
		},
		{
			name:      "没有停止原因退回本轮错误",
			def:       def(false, automation.StatusFailed, ""),
			run:       store.AutomationRun{ID: "run-err", Status: automation.RunFailed, Error: &errText},
			wantTitle: "自动化已停止：夜间巡检",
			wantBody:  errText,
			wantOK:    true,
		},
		{
			name:      "没有停止原因也没有错误",
			def:       def(false, automation.StatusCompleted, ""),
			run:       store.AutomationRun{ID: "run-empty", Status: automation.RunSucceeded},
			wantTitle: "自动化已停止：夜间巡检",
			wantBody:  "已达到停止条件",
			wantOK:    true,
		},
		{
			name:   "启用中的任务不通知",
			def:    def(true, automation.StatusActive, ""),
			run:    store.AutomationRun{ID: "run-active", Status: automation.RunSucceeded},
			wantOK: false,
		},
		{
			name:   "手动暂停不通知",
			def:    def(false, automation.StatusPaused, ""),
			run:    store.AutomationRun{ID: "run-paused", Status: automation.RunStopped},
			wantOK: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			title, body, ok := automationStoppedNotice(tc.def, tc.run)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, 期望 %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if title != tc.wantTitle {
				t.Fatalf("title = %q, 期望 %q", title, tc.wantTitle)
			}
			if body != tc.wantBody {
				t.Fatalf("body = %q, 期望 %q", body, tc.wantBody)
			}
		})
	}
}

// HandleAutomationRun 对同一个自动化只发一次停用通知；重新启用后再次停用会重新通知。
// 成功且同时停用时只发停用通知；失败且停用时仍照旧记录失败事件。
func TestHandleAutomationRunStoppedNotifyOnce(t *testing.T) {
	rig := newAttentionTestRig(t)
	threadID := "thread-automation-stopped"
	summary := "全部检查通过"
	errText := "连续失败三次"
	def := automation.Definition{
		Automation: store.Automation{ID: "auto-stopped", Name: "夜间巡检", Enabled: false, Status: automation.StatusFailed},
		Spec:       automation.Spec{State: automation.RunState{StopReason: "Paused: Reached maximum consecutive failure limit (3)."}},
	}

	var mu sync.Mutex
	var notices []string
	rig.wiring.setAutomationNotify(func(title, body, threadID string) {
		mu.Lock()
		defer mu.Unlock()
		notices = append(notices, title+"|"+body)
	})

	run := store.AutomationRun{
		ID:            "run-stopped",
		ThreadID:      &threadID,
		Status:        automation.RunFailed,
		StartedAt:     time.Now(),
		Error:         &errText,
		ResultSummary: &summary,
	}
	rig.wiring.HandleAutomationRun(def, run)
	rig.wiring.HandleAutomationRun(def, run)

	mu.Lock()
	got := append([]string(nil), notices...)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("同一自动化只应发 1 条停用通知，实际 %d 条：%v", len(got), got)
	}
	want := "自动化已停止：夜间巡检|Paused: Reached maximum consecutive failure limit (3)."
	if got[0] != want {
		t.Fatalf("通知 = %q, 期望 %q", got[0], want)
	}
	// 失败事件仍然照旧记录。
	waitAttentionEvent(t, rig.svc, attention.KindAutomationFailed, "run-stopped")

	// 重新启用后清除记录，下次停用重新通知一次。
	enabled := def
	enabled.Enabled = true
	enabled.Status = automation.StatusActive
	enabled.Spec.State.StopReason = ""
	rig.wiring.HandleAutomationRun(enabled, store.AutomationRun{ID: "run-ok", ThreadID: &threadID, Status: automation.RunStopped})
	rig.wiring.HandleAutomationRun(def, run)

	mu.Lock()
	after := append([]string(nil), notices...)
	mu.Unlock()
	if len(after) != 2 {
		t.Fatalf("重新启用后再停用应再次通知，实际 %d 条：%v", len(after), after)
	}
	if after[1] != want {
		t.Fatalf("第二次停用通知 = %q, 期望 %q", after[1], want)
	}
}
