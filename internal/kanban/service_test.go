package kanban

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"mcode/internal/store"
)

type mockDriver struct {
	mu           sync.Mutex
	starts       []SessionRequest
	states       map[string]SessionState
	defaultState SessionState
}

func newMockDriver() *mockDriver {
	return &mockDriver{
		states: make(map[string]SessionState),
		defaultState: SessionState{
			Running:           false,
			LastAssistantText: "ok",
		},
	}
}

func (m *mockDriver) StartSession(ctx context.Context, req SessionRequest) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.starts = append(m.starts, req)
	threadID := uuid.NewString()
	if _, ok := m.states[threadID]; !ok {
		m.states[threadID] = m.defaultState
	}
	return threadID, nil
}

func (m *mockDriver) SessionState(ctx context.Context, threadID string) (SessionState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.states[threadID]; ok {
		return st, nil
	}
	return m.defaultState, nil
}

func (m *mockDriver) setThreadState(threadID string, st SessionState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.states[threadID] = st
}

type mockProjects struct {
	dir string
}

func (m *mockProjects) ProjectRoot(ctx context.Context, projectID string) (string, error) {
	return m.dir, nil
}

func setupTestService(t *testing.T) (*Service, *store.Store, *mockDriver) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "t.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	driver := newMockDriver()
	projects := &mockProjects{dir: t.TempDir()}

	svc := NewService(Options{
		Store:    s,
		Git:      nil,
		Driver:   driver,
		Projects: projects,
	})
	t.Cleanup(func() { svc.Close() })

	return svc, s, driver
}

func TestBuildStartPromptTaskContext(t *testing.T) {
	goal := "目标正文"
	task := store.Task{ID: "e58a7f8a-1d03-4f10-8643-15b487f293e9", Title: "任务标题", Notes: "任务正文", GoalPrompt: &goal}
	for _, workDir := range []string{"/Users/test/项目", ""} {
		t.Run(workDir, func(t *testing.T) {
			prompt := buildStartPrompt(task, nil, "branch", workDir, false, false)
			if !strings.Contains(prompt, "任务 ID：`"+task.ID+"`，调用看板工具时使用此 ID。") {
				t.Fatalf("开工提示缺少完整任务 ID: %s", prompt)
			}
			if strings.Index(prompt, "任务正文") >= strings.Index(prompt, "## 任务") || strings.Index(prompt, "## 任务") >= strings.Index(prompt, "## Goal") {
				t.Fatalf("任务信息应位于正文之后、目标之前: %s", prompt)
			}
			if workDir == "" {
				if strings.Contains(prompt, "工作区：") {
					t.Fatalf("空工作区不应输出工作区行: %s", prompt)
				}
			} else if !strings.Contains(prompt, "工作区：`"+workDir+"`，这是本机路径，所有工具都在这个目录执行，直接在这里读写代码。") {
				t.Fatalf("开工提示缺少本机工作区说明: %s", prompt)
			}
		})
	}
}

func TestStartTaskMetaSessionRequest(t *testing.T) {
	for _, tc := range []struct {
		name, meta, provider, model string
		attachments                 []string
	}{
		{"模型与附件", `{"provider":"codex","model":"gpt-5.4","source_message":"source"}`, "codex", "gpt-5.4", []string{"image-1", "image-2"}},
		{"仅提供商", `{"provider":"codex"}`, "codex", "", nil},
		{"仅模型", `{"model":"gpt-5.4"}`, "", "gpt-5.4", nil},
		{"无元数据", "", "", "", nil},
		{"无效元数据", `{"provider":"codex","model":123}`, "", "", nil},
		{"源消息不存在", `{"provider":"codex","source_message":"missing"}`, "codex", "", nil},
		{"源消息无附件", `{"source_message":"empty"}`, "", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, db, driver := setupTestService(t)
			now := time.Now()
			if err := db.UpsertProject(ctx, store.Project{ID: "proj-1", Name: "项目", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			if err := db.CreateThread(ctx, store.Thread{ID: "source-thread", ProjectID: "proj-1", Provider: "codex", CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			for _, msg := range []store.Message{
				{ID: "source", ThreadID: "source-thread", Role: "user", CreatedAt: now, Media: []store.MediaRef{{ID: "image-1"}, {ID: "image-2"}}},
				{ID: "empty", ThreadID: "source-thread", Role: "user", CreatedAt: now},
			} {
				if _, err := db.AppendMessage(ctx, msg); err != nil {
					t.Fatal(err)
				}
			}
			task, err := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "开工"})
			if err != nil {
				t.Fatal(err)
			}
			task.Meta = tc.meta
			task, err = db.UpdateTask(ctx, task, task.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.StartTask(ctx, task.ID, task.Revision); err != nil {
				t.Fatal(err)
			}
			if len(driver.starts) != 1 {
				t.Fatalf("应只开工一次: %+v", driver.starts)
			}
			req := driver.starts[0]
			if req.Provider != tc.provider || req.Model != tc.model || !reflect.DeepEqual(req.Attachments, tc.attachments) {
				t.Fatalf("开工偏好与附件不符: %+v", req)
			}
			if !strings.Contains(req.Prompt, "任务 ID：`"+task.ID+"`") || !strings.Contains(req.Prompt, "工作区：`"+req.WorkDir+"`") {
				t.Fatalf("开工提示缺少任务与实际工作区: %s", req.Prompt)
			}
			if len(tc.attachments) > 0 && req.SourceMessageID != "source" {
				t.Fatalf("附件恢复应携带源消息 ID: %+v", req)
			}
		})
	}
}

func TestCreateTask(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t)

	// 标题空白测试
	_, err := svc.CreateTask(ctx, CreateTaskInput{
		ProjectID: "proj-1",
		Title:     "   ",
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("期望 ErrInvalidTransition，实际: %v", err)
	}

	// 正常创建
	notes := "任务备注"
	task, err := svc.CreateTask(ctx, CreateTaskInput{
		ProjectID: "proj-1",
		Title:     "测试任务",
		Notes:     &notes,
	})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if task.Status != string(TaskStatusTodo) {
		t.Errorf("期望 status todo，实际: %s", task.Status)
	}
	if task.Priority != string(PriorityNone) {
		t.Errorf("期望 priority none，实际: %s", task.Priority)
	}
	if task.Notes != notes {
		t.Errorf("期望 notes %s，实际: %s", notes, task.Notes)
	}
	if task.Revision != 1 {
		t.Errorf("期望 revision 1，实际: %d", task.Revision)
	}

	// 带前置依赖创建：依赖写入且派生为阻塞
	dep, err := svc.CreateTask(ctx, CreateTaskInput{
		ProjectID: "proj-1",
		Title:     "前置任务",
	})
	if err != nil {
		t.Fatalf("创建前置任务失败: %v", err)
	}
	dependent, err := svc.CreateTask(ctx, CreateTaskInput{
		ProjectID: "proj-1",
		Title:     "依赖任务",
		DependsOn: []string{dep.ID},
	})
	if err != nil {
		t.Fatalf("创建依赖任务失败: %v", err)
	}
	if len(dependent.DependsOn) != 1 || dependent.DependsOn[0] != dep.ID {
		t.Fatalf("期望依赖持久化为 [%s]，实际: %+v", dep.ID, dependent.DependsOn)
	}
	board, err := svc.Board(ctx)
	if err != nil {
		t.Fatalf("获取看板失败: %v", err)
	}
	for _, tv := range board.Tasks {
		if tv.Task.ID != dependent.ID {
			continue
		}
		if !tv.Derived.Blocked {
			t.Errorf("前置未完成时依赖任务应处于阻塞状态")
		}
	}
}

func TestMoveTask(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t)

	task, err := svc.CreateTask(ctx, CreateTaskInput{
		ProjectID: "proj-1",
		Title:     "测试移动",
	})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 非法状态测试
	_, err = svc.MoveTask(ctx, task.ID, TaskStatus("invalid_status"), task.Revision)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("期望 ErrInvalidTransition，实际: %v", err)
	}

	// revision 冲突测试
	_, err = svc.MoveTask(ctx, task.ID, TaskStatusInProgress, task.Revision+99)
	if !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("期望 store.ErrRevisionConflict，实际: %v", err)
	}

	// 正常移动
	moved, err := svc.MoveTask(ctx, task.ID, TaskStatusInProgress, task.Revision)
	if err != nil {
		t.Fatalf("移动任务失败: %v", err)
	}
	if moved.Status != string(TaskStatusInProgress) {
		t.Errorf("期望 in_progress，实际: %s", moved.Status)
	}
	if moved.Revision != task.Revision+1 {
		t.Errorf("期望 revision 递增，实际: %d", moved.Revision)
	}
}

func TestStartTask(t *testing.T) {
	ctx := context.Background()
	svc, _, driver := setupTestService(t)

	task, err := svc.CreateTask(ctx, CreateTaskInput{
		ProjectID: "proj-1",
		Title:     "启动测试",
	})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// revision 冲突测试
	_, err = svc.StartTask(ctx, task.ID, task.Revision+99)
	if !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("期望 store.ErrRevisionConflict，实际: %v", err)
	}

	// 正常启动
	started, err := svc.StartTask(ctx, task.ID, task.Revision)
	if err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	if started.Status != string(TaskStatusInProgress) {
		t.Errorf("期望 in_progress，实际: %s", started.Status)
	}
	if started.ThreadID == nil || *started.ThreadID == "" {
		t.Errorf("期望 ThreadID 非空")
	}
	if started.AttemptCount != 1 {
		t.Errorf("期望 AttemptCount 1，实际: %d", started.AttemptCount)
	}
	if len(driver.starts) != 1 {
		t.Errorf("期望 driver 收到 1 次启动，实际: %d", len(driver.starts))
	}

	// 非 todo 状态再次启动应报错
	_, err = svc.StartTask(ctx, started.ID, started.Revision)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("非 todo 启动期望 ErrInvalidTransition，实际: %v", err)
	}
}

func TestReviewTask(t *testing.T) {
	ctx := context.Background()
	svc, _, driver := setupTestService(t)

	task, err := svc.CreateTask(ctx, CreateTaskInput{
		ProjectID: "proj-1",
		Title:     "评审测试",
	})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 非 review 状态调用 ReviewTask 应报错
	_, err = svc.ReviewTask(ctx, task.ID, ReviewAccept, "", task.Revision)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("期望 ErrInvalidTransition，实际: %v", err)
	}

	// 移至 review
	inReview, err := svc.MoveTask(ctx, task.ID, TaskStatusReview, task.Revision)
	if err != nil {
		t.Fatalf("移至 review 失败: %v", err)
	}

	// revision 冲突测试
	_, err = svc.ReviewTask(ctx, inReview.ID, ReviewAccept, "", inReview.Revision+99)
	if !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("期望 store.ErrRevisionConflict，实际: %v", err)
	}

	// 正常 accept
	feedback := "通过"
	accepted, err := svc.ReviewTask(ctx, inReview.ID, ReviewAccept, feedback, inReview.Revision)
	if err != nil {
		t.Fatalf("评审通过失败: %v", err)
	}
	if accepted.Status != string(TaskStatusDone) {
		t.Errorf("期望 status done，实际: %s", accepted.Status)
	}
	if accepted.ReviewStatus == nil || *accepted.ReviewStatus != "passed" {
		t.Errorf("期望 ReviewStatus passed")
	}
	if accepted.ReviewFeedback == nil || *accepted.ReviewFeedback != feedback {
		t.Errorf("期望 ReviewFeedback %s", feedback)
	}

	// 测试 reject：附带反馈退回 todo，重启时把反馈带进提示词
	task2, _ := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "驳回测试"})
	task2Rev, _ := svc.MoveTask(ctx, task2.ID, TaskStatusReview, task2.Revision)
	rejected, err := svc.ReviewTask(ctx, task2Rev.ID, ReviewReject, "不合格", task2Rev.Revision)
	if err != nil {
		t.Fatalf("驳回失败: %v", err)
	}
	if rejected.Status != string(TaskStatusTodo) {
		t.Errorf("期望 status todo，实际: %s", rejected.Status)
	}
	if rejected.ReviewStatus == nil || *rejected.ReviewStatus != "revisions_requested" {
		t.Errorf("期望 ReviewStatus revisions_requested，实际: %v", rejected.ReviewStatus)
	}
	if rejected.ReviewFeedback == nil || *rejected.ReviewFeedback != "不合格" {
		t.Errorf("期望 ReviewFeedback 保留驳回反馈")
	}

	// 重启被驳回的任务，提示词应包含评审反馈
	if _, err := svc.StartTask(ctx, rejected.ID, rejected.Revision); err != nil {
		t.Fatalf("重启被驳回任务失败: %v", err)
	}
	driver.mu.Lock()
	lastPrompt := driver.starts[len(driver.starts)-1].Prompt
	driver.mu.Unlock()
	if !strings.Contains(lastPrompt, "不合格") || !strings.Contains(lastPrompt, "Review Feedback To Address") {
		t.Errorf("期望提示词包含评审反馈，实际: %s", lastPrompt)
	}

	// 测试 requestReview
	task3, _ := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "二次评审"})
	task3Rev, _ := svc.MoveTask(ctx, task3.ID, TaskStatusReview, task3.Revision)
	reqReview, err := svc.ReviewTask(ctx, task3Rev.ID, ReviewRequest, "请重新确认", task3Rev.Revision)
	if err != nil {
		t.Fatalf("requestReview 失败: %v", err)
	}
	if reqReview.Status != string(TaskStatusReview) {
		t.Errorf("期望 status review，实际: %s", reqReview.Status)
	}
	if reqReview.ReviewStatus == nil || *reqReview.ReviewStatus != "requested" {
		t.Errorf("期望 ReviewStatus requested")
	}
	if reqReview.ReviewThreadID == nil || *reqReview.ReviewThreadID == "" {
		t.Errorf("期望 ReviewThreadID 非空")
	}
}

func TestSkipGoalTask(t *testing.T) {
	ctx := context.Background()
	svc, s, _ := setupTestService(t)

	projectID := "proj-1"
	goal := store.Goal{
		ID:        "goal-skip-1",
		ProjectID: projectID,
		Title:     "跳过任务测试",
		Prompt:    "跳过任务测试",
		Status:    string(GoalStatusPlanReview),
		Policy:    map[string]any{"maxConcurrency": 1, "previewPlan": true, "autoAccept": false, "maxAttempts": 2},
		Progress:  map[string]any{},
	}
	if err := s.CreateGoal(ctx, goal); err != nil {
		t.Fatalf("创建目标失败: %v", err)
	}
	createdGoal, err := s.GetGoal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("获取目标失败: %v", err)
	}

	task := store.Task{
		ID:        "task-skip-1",
		ProjectID: &projectID,
		GoalID:    &goal.ID,
		Title:     "可跳过任务",
		Status:    string(TaskStatusTodo),
		Priority:  string(PriorityNone),
		DependsOn: []string{},
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := s.SetGoalTasks(ctx, goal.ID, []string{task.ID}, createdGoal.Revision); err != nil {
		t.Fatalf("关联目标任务失败: %v", err)
	}
	storedTask, _ := s.GetTask(ctx, task.ID)

	skipped, err := svc.SkipGoalTask(ctx, storedTask.ID, storedTask.Revision)
	if err != nil {
		t.Fatalf("跳过任务失败: %v", err)
	}
	if skipped.Status != string(TaskStatusDone) {
		t.Errorf("期望跳过任务状态为 done，实际: %s", skipped.Status)
	}
	if skipped.ReviewStatus == nil || *skipped.ReviewStatus != "skipped" {
		t.Errorf("期望 ReviewStatus skipped，实际: %v", skipped.ReviewStatus)
	}

	updatedGoal, err := s.GetGoal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("获取目标失败: %v", err)
	}
	if done := progressInt(updatedGoal.Progress["done"]); done != 1 {
		t.Errorf("期望 progress done 为 1，实际: %d", done)
	}
	if failed := progressInt(updatedGoal.Progress["failed"]); failed != 0 {
		t.Errorf("期望 progress failed 为 0，实际: %d", failed)
	}
}

func progressInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return -1
}

func TestRetryGoalTaskResetsAttempts(t *testing.T) {
	ctx := context.Background()
	svc, s, _ := setupTestService(t)

	projectID := "proj-1"
	goal := store.Goal{
		ID:        "goal-retry-1",
		ProjectID: projectID,
		Title:     "重试任务测试",
		Prompt:    "重试任务测试",
		Status:    string(GoalStatusPlanReview),
		Policy:    map[string]any{"maxConcurrency": 1, "previewPlan": true, "autoAccept": false, "maxAttempts": 2},
		Progress:  map[string]any{},
	}
	if err := s.CreateGoal(ctx, goal); err != nil {
		t.Fatalf("创建目标失败: %v", err)
	}
	createdGoal, err := s.GetGoal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("获取目标失败: %v", err)
	}

	task := store.Task{
		ID:           "task-retry-1",
		ProjectID:    &projectID,
		GoalID:       &goal.ID,
		Title:        "失败后重试的任务",
		Status:       string(TaskStatusRejected),
		Priority:     string(PriorityNone),
		AttemptCount: 2,
		DependsOn:    []string{},
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := s.SetGoalTasks(ctx, goal.ID, []string{task.ID}, createdGoal.Revision); err != nil {
		t.Fatalf("关联目标任务失败: %v", err)
	}
	storedTask, _ := s.GetTask(ctx, task.ID)

	retried, err := svc.RetryGoalTask(ctx, storedTask.ID, storedTask.Revision)
	if err != nil {
		t.Fatalf("重试任务失败: %v", err)
	}
	if retried.Status != string(TaskStatusTodo) {
		t.Errorf("期望重试后状态为 todo，实际: %s", retried.Status)
	}
	if retried.AttemptCount != 0 {
		t.Errorf("期望重试后 AttemptCount 重置为 0，实际: %d", retried.AttemptCount)
	}
}

func TestAgentPlannerParsing(t *testing.T) {
	ctx := context.Background()
	driver := newMockDriver()
	projects := &mockProjects{dir: t.TempDir()}

	planner := NewAgentPlanner(driver, projects)
	planner.PollInterval = 1 * time.Millisecond

	driver.defaultState = SessionState{
		Running: false,
		LastAssistantText: `下面是规划方案：
{
  "summary": "实现认证功能",
  "tasks": [
    {"title": "数据库模型", "notes": "创建用户表", "dependsOn": []},
    {"title": "登录接口", "notes": "实现 JWT 签发", "dependsOn": [0]}
  ]
}
完成。`,
	}

	res, err := planner.Plan(ctx, store.Goal{
		ID:        "goal-1",
		ProjectID: "proj-1",
		Title:     "实现认证",
		Prompt:    "需要用户系统",
	}, "")
	if err != nil {
		t.Fatalf("Planner.Plan 失败: %v", err)
	}

	if res.Summary != "实现认证功能" {
		t.Errorf("期望 summary '实现认证功能'，实际: %s", res.Summary)
	}
	if len(res.Tasks) != 2 {
		t.Fatalf("期望 2 个任务，实际: %d", len(res.Tasks))
	}
	if res.Tasks[0].Title != "数据库模型" {
		t.Errorf("期望任务 0 为数据库模型，实际: %s", res.Tasks[0].Title)
	}
	if len(res.Tasks[1].DependsOn) != 1 || res.Tasks[1].DependsOn[0] != 0 {
		t.Errorf("期望任务 1 依赖任务 0")
	}
}

func TestAutoCleanupMergedWorktreesOption(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	defer s.Close()

	cleanupCalls := 0
	svc := NewService(Options{
		Store: s,
		AutoCleanupMergedWorktrees: func() bool {
			cleanupCalls++
			return false
		},
	})
	defer svc.Close()

	if svc.opts.AutoCleanupMergedWorktrees == nil {
		t.Fatal("expected AutoCleanupMergedWorktrees option to be set")
	}
	if svc.opts.AutoCleanupMergedWorktrees() {
		t.Fatal("expected AutoCleanupMergedWorktrees to return false")
	}
	if cleanupCalls != 1 {
		t.Fatalf("expected 1 call, got %d", cleanupCalls)
	}
}
