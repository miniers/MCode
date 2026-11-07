package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/kanban"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

type recordConn struct {
	fakeConn
	mu            sync.Mutex
	notifications []string
	boards        []wire.KanbanBoard
}

func (c *recordConn) Notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notifications = append(c.notifications, method)
	if b, ok := params.(wire.KanbanBoard); ok {
		c.boards = append(c.boards, b)
	}
	return nil
}

type mockKanbanService struct {
	mu       sync.Mutex
	tasks    map[string]store.Task
	goals    map[string]store.Goal
	calls    map[string]int
	boardRev int
	diffMap  map[string]string
}

func newMockKanbanService() *mockKanbanService {
	return &mockKanbanService{
		tasks:    make(map[string]store.Task),
		goals:    make(map[string]store.Goal),
		calls:    make(map[string]int),
		boardRev: 1,
		diffMap:  make(map[string]string),
	}
}

func (m *mockKanbanService) Board(ctx context.Context) (kanban.Board, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["Board"]++

	taskViews := make([]kanban.TaskView, 0, len(m.tasks))
	for _, t := range m.tasks {
		taskViews = append(taskViews, kanban.TaskView{
			Task: t,
			Derived: kanban.Derived{
				DisplayColumn:  kanban.DisplayColumnTodo,
				Blocked:        false,
				CanStart:       true,
				CanMove:        true,
				CanReview:      true,
				CanApprove:     true,
				AttentionFlags: []string{},
			},
		})
	}
	goals := make([]store.Goal, 0, len(m.goals))
	for _, g := range m.goals {
		goals = append(goals, g)
	}

	return kanban.Board{
		Revision: m.boardRev,
		Tasks:    taskViews,
		Goals:    goals,
	}, nil
}

func (m *mockKanbanService) CreateTask(ctx context.Context, input kanban.CreateTaskInput) (store.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["CreateTask"]++

	id := "11111111-1111-1111-1111-111111111111"
	t := store.Task{
		ID:        id,
		ProjectID: &input.ProjectID,
		Title:     input.Title,
		Notes:     "",
		Status:    string(kanban.TaskStatusTodo),
		Revision:  1,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if input.Notes != nil {
		t.Notes = *input.Notes
	}
	t.ThreadID = input.ThreadID
	m.tasks[id] = t
	m.boardRev++
	return t, nil
}

func (m *mockKanbanService) MoveTask(ctx context.Context, taskID string, status kanban.TaskStatus, expectedRevision int) (store.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["MoveTask"]++

	t, ok := m.tasks[taskID]
	if !ok {
		return store.Task{}, store.ErrNotFound
	}
	if t.Revision != expectedRevision {
		return store.Task{}, store.ErrRevisionConflict
	}
	t.Status = string(status)
	t.Revision++
	t.UpdatedAt = time.Now().UTC()
	m.tasks[taskID] = t
	m.boardRev++
	return t, nil
}

func (m *mockKanbanService) StartTask(ctx context.Context, taskID string, expectedRevision int) (store.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["StartTask"]++

	t, ok := m.tasks[taskID]
	if !ok {
		return store.Task{}, store.ErrNotFound
	}
	if t.Revision != expectedRevision {
		return store.Task{}, store.ErrRevisionConflict
	}
	t.Status = string(kanban.TaskStatusInProgress)
	t.Revision++
	t.UpdatedAt = time.Now().UTC()
	m.tasks[taskID] = t
	m.boardRev++
	return t, nil
}

func (m *mockKanbanService) ReviewTask(ctx context.Context, taskID string, decision kanban.ReviewDecision, feedback string, expectedRevision int) (store.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["ReviewTask"]++

	t, ok := m.tasks[taskID]
	if !ok {
		return store.Task{}, store.ErrNotFound
	}
	if t.Revision != expectedRevision {
		return store.Task{}, store.ErrRevisionConflict
	}
	t.ReviewFeedback = &feedback
	t.Revision++
	t.UpdatedAt = time.Now().UTC()
	m.tasks[taskID] = t
	m.boardRev++
	return t, nil
}

func (m *mockKanbanService) TaskDiff(ctx context.Context, taskID string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["TaskDiff"]++

	if _, ok := m.tasks[taskID]; !ok {
		return "", false, store.ErrNotFound
	}
	diff := m.diffMap[taskID]
	return diff, false, nil
}

func (m *mockKanbanService) CreateGoal(ctx context.Context, projectID, prompt string) (store.Goal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["CreateGoal"]++

	id := "22222222-2222-2222-2222-222222222222"
	g := store.Goal{
		ID:        id,
		ProjectID: projectID,
		Title:     prompt,
		Prompt:    prompt,
		Status:    string(kanban.GoalStatusPlanning),
		Revision:  1,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	m.goals[id] = g
	m.boardRev++
	return g, nil
}

func (m *mockKanbanService) StartGoal(ctx context.Context, goalID string, expectedRevision int) (store.Goal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["StartGoal"]++

	g, ok := m.goals[goalID]
	if !ok {
		return store.Goal{}, store.ErrNotFound
	}
	if g.Revision != expectedRevision {
		return store.Goal{}, store.ErrRevisionConflict
	}
	g.Status = string(kanban.GoalStatusRunning)
	g.Revision++
	g.UpdatedAt = time.Now().UTC()
	m.goals[goalID] = g
	m.boardRev++
	return g, nil
}

func (m *mockKanbanService) PauseGoal(ctx context.Context, goalID string, expectedRevision int) (store.Goal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["PauseGoal"]++

	g, ok := m.goals[goalID]
	if !ok {
		return store.Goal{}, store.ErrNotFound
	}
	if g.Revision != expectedRevision {
		return store.Goal{}, store.ErrRevisionConflict
	}
	g.Status = string(kanban.GoalStatusPaused)
	g.Revision++
	g.UpdatedAt = time.Now().UTC()
	m.goals[goalID] = g
	m.boardRev++
	return g, nil
}

func (m *mockKanbanService) CancelGoal(ctx context.Context, goalID string, expectedRevision int) (store.Goal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["CancelGoal"]++

	g, ok := m.goals[goalID]
	if !ok {
		return store.Goal{}, store.ErrNotFound
	}
	if g.Revision != expectedRevision {
		return store.Goal{}, store.ErrRevisionConflict
	}
	g.Status = string(kanban.GoalStatusCancelled)
	g.Revision++
	g.UpdatedAt = time.Now().UTC()
	m.goals[goalID] = g
	m.boardRev++
	return g, nil
}

func (m *mockKanbanService) ReplanGoal(ctx context.Context, goalID, feedback string, expectedRevision int) (store.Goal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["ReplanGoal"]++

	g, ok := m.goals[goalID]
	if !ok {
		return store.Goal{}, store.ErrNotFound
	}
	if g.Revision != expectedRevision {
		return store.Goal{}, store.ErrRevisionConflict
	}
	g.Status = string(kanban.GoalStatusPlanning)
	g.Revision++
	g.UpdatedAt = time.Now().UTC()
	m.goals[goalID] = g
	m.boardRev++
	return g, nil
}

func (m *mockKanbanService) RetryGoalTask(ctx context.Context, taskID string, expectedRevision int) (store.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["RetryGoalTask"]++

	t, ok := m.tasks[taskID]
	if !ok {
		return store.Task{}, store.ErrNotFound
	}
	if t.Revision != expectedRevision {
		return store.Task{}, store.ErrRevisionConflict
	}
	t.Status = string(kanban.TaskStatusTodo)
	t.Revision++
	t.UpdatedAt = time.Now().UTC()
	m.tasks[taskID] = t
	m.boardRev++
	return t, nil
}

func (m *mockKanbanService) SkipGoalTask(ctx context.Context, taskID string, expectedRevision int) (store.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["SkipGoalTask"]++

	t, ok := m.tasks[taskID]
	if !ok {
		return store.Task{}, store.ErrNotFound
	}
	if t.Revision != expectedRevision {
		return store.Task{}, store.ErrRevisionConflict
	}
	t.Status = string(kanban.TaskStatusDone)
	t.Revision++
	t.UpdatedAt = time.Now().UTC()
	m.tasks[taskID] = t
	m.boardRev++
	return t, nil
}

func executeRPC(t *testing.T, d *Dispatcher, conn Conn, reqJSON string) wire.Response {
	t.Helper()
	resRaw := d.Handle(context.Background(), conn, []byte(reqJSON))
	var resp wire.Response
	if err := json.Unmarshal(resRaw, &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v, raw: %s", err, string(resRaw))
	}
	return resp
}

func TestKanbanMethods_NormalAndConflictAndIdempotency(t *testing.T) {
	mockSvc := newMockKanbanService()
	auth := &fakeAuth{}
	backend := &fakeBackend{
		listProjectsFunc: func(ctx context.Context) (wire.ProjectList, error) {
			return wire.ProjectList{
				Projects: []wire.ProjectSummary{
					{ID: "33333333-3333-3333-3333-333333333333", Name: "TestProject"},
				},
				Revision: 1,
			}, nil
		},
	}
	d := NewDispatcher(auth, backend)
	d.SetKanban(mockSvc)

	conn := &recordConn{
		fakeConn: fakeConn{
			info: ClientInfo{
				Authenticated: true,
				DeviceID:      "device-test-1",
				Label:         "TestDevice",
			},
		},
	}

	// 1. kanban.getBoard 正常路径与参数非法路径
	{
		// 正常路径
		req := `{"jsonrpc":"2.0","method":"kanban.getBoard","params":{},"id":1}`
		resp := executeRPC(t, d, conn, req)
		if resp.Error != nil {
			t.Fatalf("kanban.getBoard failed: %v", resp.Error)
		}

		// 参数非法路径（不能带多余参数）
		badReq := `{"jsonrpc":"2.0","method":"kanban.getBoard","params":{"invalid":true},"id":2}`
		badResp := executeRPC(t, d, conn, badReq)
		if badResp.Error == nil || badResp.Error.Code != wire.InvalidParams {
			t.Fatalf("expected InvalidParams (-32602), got: %+v", badResp.Error)
		}
	}

	projectID := "33333333-3333-3333-3333-333333333333"

	// 2. kanban.createTask 正常路径、参数校验与幂等性
	var taskID string
	{
		actionID := "44444444-4444-4444-4444-444444444444"
		req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"kanban.createTask","params":{"projectID":"%s","title":"Task 1","actionID":"%s"},"id":3}`, projectID, actionID)
		resp := executeRPC(t, d, conn, req)
		if resp.Error != nil {
			t.Fatalf("kanban.createTask failed: %v", resp.Error)
		}

		var res wire.KanbanTaskMutationResult
		b, _ := json.Marshal(resp.Result)
		_ = json.Unmarshal(b, &res)
		if res.TaskID == nil || *res.Revision != 1 {
			t.Fatalf("unexpected create task result: %+v", res)
		}
		taskID = *res.TaskID

		// 幂等性：重复发送同一个 actionID
		resp2 := executeRPC(t, d, conn, req)
		if resp2.Error != nil {
			t.Fatalf("kanban.createTask idempotent failed: %v", resp2.Error)
		}
		var res2 wire.KanbanTaskMutationResult
		b2, _ := json.Marshal(resp2.Result)
		_ = json.Unmarshal(b2, &res2)
		if res2.TaskID == nil || *res2.TaskID != taskID || *res2.Revision != 1 {
			t.Fatalf("idempotent result mismatch: %+v vs %+v", res, res2)
		}
		if mockSvc.calls["CreateTask"] != 1 {
			t.Fatalf("expected CreateTask to be called exactly once, got %d", mockSvc.calls["CreateTask"])
		}

		// 参数非法：空 title
		badReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"kanban.createTask","params":{"projectID":"%s","title":"","actionID":"55555555-5555-5555-5555-555555555555"},"id":4}`, projectID)
		badResp := executeRPC(t, d, conn, badReq)
		if badResp.Error == nil || badResp.Error.Code != wire.InvalidParams {
			t.Fatalf("expected InvalidParams (-32602), got: %+v", badResp.Error)
		}
	}

	// 3. kanban.moveTask 正常路径与 CAS 冲突 (-32009)
	{
		// 冲突路径：期望版本 999 不匹配当前版本 1
		conflictReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"kanban.moveTask","params":{"taskID":"%s","status":"in_progress","expectedRevision":999,"actionID":"66666666-6666-6666-6666-666666666666"},"id":5}`, taskID)
		conflictResp := executeRPC(t, d, conn, conflictReq)
		if conflictResp.Error == nil || conflictResp.Error.Code != wire.ErrCodeVersionConflict {
			t.Fatalf("expected version conflict (-32009), got: %+v", conflictResp.Error)
		}

		// 正常路径：期望版本 1
		req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"kanban.moveTask","params":{"taskID":"%s","status":"in_progress","expectedRevision":1,"actionID":"66666666-6666-6666-6666-666666666667"},"id":6}`, taskID)
		resp := executeRPC(t, d, conn, req)
		if resp.Error != nil {
			t.Fatalf("kanban.moveTask failed: %v", resp.Error)
		}
	}

	// 4. kanban.startTask 正常路径与 CAS 冲突
	{
		// 冲突路径：当前版本应为 2
		conflictReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"kanban.startTask","params":{"taskID":"%s","expectedRevision":1,"actionID":"77777777-7777-7777-7777-777777777771"},"id":7}`, taskID)
		conflictResp := executeRPC(t, d, conn, conflictReq)
		if conflictResp.Error == nil || conflictResp.Error.Code != wire.ErrCodeVersionConflict {
			t.Fatalf("expected version conflict (-32009), got: %+v", conflictResp.Error)
		}

		// 正常路径：版本 2
		req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"kanban.startTask","params":{"taskID":"%s","expectedRevision":2,"actionID":"77777777-7777-7777-7777-777777777772"},"id":8}`, taskID)
		resp := executeRPC(t, d, conn, req)
		if resp.Error != nil {
			t.Fatalf("kanban.startTask failed: %v", resp.Error)
		}
	}

	// 5. kanban.reviewTask 正常路径与 CAS 冲突
	{
		// 冲突路径：当前版本为 3
		conflictReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"kanban.reviewTask","params":{"taskID":"%s","decision":"accept","feedback":"lgtm","expectedRevision":999,"actionID":"88888888-8888-8888-8888-888888888881"},"id":9}`, taskID)
		conflictResp := executeRPC(t, d, conn, conflictReq)
		if conflictResp.Error == nil || conflictResp.Error.Code != wire.ErrCodeVersionConflict {
			t.Fatalf("expected version conflict (-32009), got: %+v", conflictResp.Error)
		}

		// 正常路径：版本 3；feedback 按 schema 可省略
		req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"kanban.reviewTask","params":{"taskID":"%s","decision":"accept","expectedRevision":3,"actionID":"88888888-8888-8888-8888-888888888882"},"id":10}`, taskID)
		resp := executeRPC(t, d, conn, req)
		if resp.Error != nil {
			t.Fatalf("kanban.reviewTask failed: %v", resp.Error)
		}
	}

	// 6. kanban.getDiff 正常路径与参数非法
	{
		mockSvc.diffMap[taskID] = "diff content"
		// 正常路径
		req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"kanban.getDiff","params":{"taskID":"%s"},"id":11}`, taskID)
		resp := executeRPC(t, d, conn, req)
		if resp.Error != nil {
			t.Fatalf("kanban.getDiff failed: %v", resp.Error)
		}

		// 参数非法路径
		badReq := `{"jsonrpc":"2.0","method":"kanban.getDiff","params":{"taskID":"invalid-uuid"},"id":12}`
		badResp := executeRPC(t, d, conn, badReq)
		if badResp.Error == nil || badResp.Error.Code != wire.InvalidParams {
			t.Fatalf("expected InvalidParams (-32602), got: %+v", badResp.Error)
		}
	}

	// 7. goals.create 正常路径与参数非法
	var goalID string
	{
		actionID := "99999999-9999-9999-9999-999999999991"
		req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"goals.create","params":{"projectID":"%s","prompt":"Build feature","actionID":"%s"},"id":13}`, projectID, actionID)
		resp := executeRPC(t, d, conn, req)
		if resp.Error != nil {
			t.Fatalf("goals.create failed: %v", resp.Error)
		}
		var res wire.KanbanTaskMutationResult
		b, _ := json.Marshal(resp.Result)
		_ = json.Unmarshal(b, &res)
		if res.GoalID == nil || res.TaskID != nil || *res.Revision != 1 {
			t.Fatalf("unexpected goal create result: %+v", res)
		}
		goalID = *res.GoalID

		// 参数非法路径：空 prompt
		badReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"goals.create","params":{"projectID":"%s","prompt":"","actionID":"99999999-9999-9999-9999-999999999992"},"id":14}`, projectID)
		badResp := executeRPC(t, d, conn, badReq)
		if badResp.Error == nil || badResp.Error.Code != wire.InvalidParams {
			t.Fatalf("expected InvalidParams (-32602), got: %+v", badResp.Error)
		}
	}

	// 8. goals.start 正常路径与 CAS 冲突
	{
		conflictReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"goals.start","params":{"goalID":"%s","expectedRevision":999,"actionID":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"},"id":15}`, goalID)
		conflictResp := executeRPC(t, d, conn, conflictReq)
		if conflictResp.Error == nil || conflictResp.Error.Code != wire.ErrCodeVersionConflict {
			t.Fatalf("expected version conflict (-32009), got: %+v", conflictResp.Error)
		}

		req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"goals.start","params":{"goalID":"%s","expectedRevision":1,"actionID":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaab"},"id":16}`, goalID)
		resp := executeRPC(t, d, conn, req)
		if resp.Error != nil {
			t.Fatalf("goals.start failed: %v", resp.Error)
		}
	}

	// 9. goals.pause 正常路径与 CAS 冲突
	{
		conflictReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"goals.pause","params":{"goalID":"%s","expectedRevision":999,"actionID":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbba"},"id":17}`, goalID)
		conflictResp := executeRPC(t, d, conn, conflictReq)
		if conflictResp.Error == nil || conflictResp.Error.Code != wire.ErrCodeVersionConflict {
			t.Fatalf("expected version conflict (-32009), got: %+v", conflictResp.Error)
		}

		req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"goals.pause","params":{"goalID":"%s","expectedRevision":2,"actionID":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},"id":18}`, goalID)
		resp := executeRPC(t, d, conn, req)
		if resp.Error != nil {
			t.Fatalf("goals.pause failed: %v", resp.Error)
		}
	}

	// 10. goals.cancel 正常路径与 CAS 冲突
	{
		conflictReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"goals.cancel","params":{"goalID":"%s","expectedRevision":999,"actionID":"cccccccc-cccc-cccc-cccc-ccccccccccca"},"id":19}`, goalID)
		conflictResp := executeRPC(t, d, conn, conflictReq)
		if conflictResp.Error == nil || conflictResp.Error.Code != wire.ErrCodeVersionConflict {
			t.Fatalf("expected version conflict (-32009), got: %+v", conflictResp.Error)
		}

		req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"goals.cancel","params":{"goalID":"%s","expectedRevision":3,"actionID":"cccccccc-cccc-cccc-cccc-cccccccccccc"},"id":20}`, goalID)
		resp := executeRPC(t, d, conn, req)
		if resp.Error != nil {
			t.Fatalf("goals.cancel failed: %v", resp.Error)
		}
	}

	// 11. goals.replan 正常路径与 CAS 冲突
	{
		conflictReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"goals.replan","params":{"goalID":"%s","feedback":"need replan","expectedRevision":999,"actionID":"dddddddd-dddd-dddd-dddd-ddddddddddda"},"id":21}`, goalID)
		conflictResp := executeRPC(t, d, conn, conflictReq)
		if conflictResp.Error == nil || conflictResp.Error.Code != wire.ErrCodeVersionConflict {
			t.Fatalf("expected version conflict (-32009), got: %+v", conflictResp.Error)
		}

		req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"goals.replan","params":{"goalID":"%s","feedback":"need replan","expectedRevision":4,"actionID":"dddddddd-dddd-dddd-dddd-dddddddddddd"},"id":22}`, goalID)
		resp := executeRPC(t, d, conn, req)
		if resp.Error != nil {
			t.Fatalf("goals.replan failed: %v", resp.Error)
		}
	}

	// 12. goals.retryTask 正常路径与 CAS 冲突
	{
		// 任务当前版本为 4（经 create 1 -> move 2 -> start 3 -> review 4）
		conflictReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"goals.retryTask","params":{"taskID":"%s","expectedRevision":999,"actionID":"eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeea"},"id":23}`, taskID)
		conflictResp := executeRPC(t, d, conn, conflictReq)
		if conflictResp.Error == nil || conflictResp.Error.Code != wire.ErrCodeVersionConflict {
			t.Fatalf("expected version conflict (-32009), got: %+v", conflictResp.Error)
		}

		req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"goals.retryTask","params":{"taskID":"%s","expectedRevision":4,"actionID":"eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"},"id":24}`, taskID)
		resp := executeRPC(t, d, conn, req)
		if resp.Error != nil {
			t.Fatalf("goals.retryTask failed: %v", resp.Error)
		}
	}

	// 13. goals.skipTask 正常路径与 CAS 冲突
	{
		// 任务当前版本为 5
		conflictReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"goals.skipTask","params":{"taskID":"%s","expectedRevision":999,"actionID":"ffffffff-ffff-ffff-ffff-fffffffffffa"},"id":25}`, taskID)
		conflictResp := executeRPC(t, d, conn, conflictReq)
		if conflictResp.Error == nil || conflictResp.Error.Code != wire.ErrCodeVersionConflict {
			t.Fatalf("expected version conflict (-32009), got: %+v", conflictResp.Error)
		}

		req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"goals.skipTask","params":{"taskID":"%s","expectedRevision":5,"actionID":"ffffffff-ffff-ffff-ffff-ffffffffffff"},"id":26}`, taskID)
		resp := executeRPC(t, d, conn, req)
		if resp.Error != nil {
			t.Fatalf("goals.skipTask failed: %v", resp.Error)
		}
	}

	// 断言变更后已认证的假连接收到 kanban.taskUpdated 通知
	conn.mu.Lock()
	receivedCount := len(conn.notifications)
	conn.mu.Unlock()
	if receivedCount == 0 {
		t.Fatalf("expected conn to receive kanban.taskUpdated notifications, got 0")
	}

	// 断言 capabilities 里含看板相关 features 和新方法
	methods := ImplementedMethods()
	methodMap := make(map[string]bool)
	for _, m := range methods {
		methodMap[m] = true
	}
	expectedMethods := []string{
		"kanban.createTask", "kanban.moveTask", "kanban.startTask", "kanban.reviewTask",
		"kanban.getDiff", "goals.create", "goals.start", "goals.pause",
		"goals.cancel", "goals.replan", "goals.retryTask", "goals.skipTask",
	}
	for _, em := range expectedMethods {
		if !methodMap[em] {
			t.Errorf("expected ImplementedMethods to contain %q", em)
		}
	}

	features := make(map[string]bool, len(DefaultFeatures()))
	for _, f := range DefaultFeatures() {
		features[f] = true
	}
	for _, want := range []string{"kanban.goals", "kanban.linking", "kanban.createTaskFromThread"} {
		if !features[want] {
			t.Errorf("expected DefaultFeatures to contain %q", want)
		}
	}
}

type driverTestMockHarness struct{}

func (h *driverTestMockHarness) Kind() harness.Kind {
	return "mock-driver-harness"
}

func (h *driverTestMockHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	emit(harness.Event{
		Type: harness.EventText,
		Text: "Hello from assistant",
	})
	emit(harness.Event{
		Type:       harness.EventDone,
		StopReason: "completed",
	})
	return nil
}

func TestKanbanDriver_StartSessionAndState(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	mockHarness := &driverTestMockHarness{}
	backend.RegisterHarness(mockHarness)
	backend.SetHarness(mockHarness, "/tmp", harness.ModeFullAccess)

	projectID := "12345678-1234-1234-1234-123456789012"
	backend.AddProject(wire.ProjectSummary{
		ID:   projectID,
		Name: "TestProject",
		Path: "/tmp/project",
	})

	ctx := context.Background()

	// 1. 验证 ProjectRoot
	root, err := backend.ProjectRoot(ctx, projectID)
	if err != nil {
		t.Fatalf("ProjectRoot failed: %v", err)
	}
	if root != "/tmp/project" {
		t.Fatalf("expected /tmp/project, got %q", root)
	}

	_, err = backend.ProjectRoot(ctx, "non-existent-id")
	if err == nil || err != store.ErrNotFound {
		t.Fatalf("expected store.ErrNotFound, got %v", err)
	}

	// 2. 验证 StartSession
	threadID, err := backend.StartSession(ctx, kanban.SessionRequest{
		ProjectID: projectID,
		WorkDir:   "/tmp/worktree-1",
		Title:     "任务目标",
		Prompt:    "启动执行",
	})
	if err != nil {
		t.Fatalf("StartSession failed: %v", err)
	}
	if threadID == "" {
		t.Fatalf("expected non-empty threadID")
	}

	// 3. 等待完成并验证 SessionState
	deadline := time.Now().Add(3 * time.Second)
	var finalState kanban.SessionState
	for time.Now().Before(deadline) {
		st, err := backend.SessionState(ctx, threadID)
		if err != nil {
			t.Fatalf("SessionState failed: %v", err)
		}
		if !st.Running {
			finalState = st
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if finalState.Running {
		t.Fatalf("expected session to finish running")
	}
	if finalState.LastAssistantText != "Hello from assistant" {
		t.Fatalf("expected 'Hello from assistant', got %q", finalState.LastAssistantText)
	}

	// 4. 验证不存在线程返回 store.ErrNotFound
	_, err = backend.SessionState(ctx, "non-existent-thread")
	if err == nil || err != store.ErrNotFound {
		t.Fatalf("expected store.ErrNotFound, got %v", err)
	}
}

// TestSessionStateDropsStaleRunningFlag 验证当会话没有真实运行中的轮次（runTurnID 为空）时，
// SessionState 不会将残留的 summary.IsRunning 视作运行中，并就地纠正内存中的粘滞标记。
func TestSessionStateDropsStaleRunningFlag(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	backend.AddThread(wire.ThreadSummary{ID: "t-stale", ProjectID: "p1", Title: "粘滞运行"}, nil)

	// 模拟异常收尾：IsRunning 残留为 true，但没有正在执行的轮次（runTurnID == "" 且 runCancel == nil）
	backend.mu.Lock()
	backend.threads["t-stale"].summary.IsRunning = true
	backend.mu.Unlock()

	st, err := backend.SessionState(context.Background(), "t-stale")
	if err != nil {
		t.Fatalf("SessionState failed: %v", err)
	}
	if st.Running {
		t.Fatalf("expected SessionState.Running to be false when runTurnID is empty, got true")
	}

	// 验证内部 summary.IsRunning 是否被纠正
	backend.mu.Lock()
	stillRunning := backend.threads["t-stale"].summary.IsRunning
	backend.mu.Unlock()
	if stillRunning {
		t.Fatalf("expected backend.threads[id].summary.IsRunning to be corrected to false")
	}

	// 验证 ListThreads 也不再报告 running
	list, err := backend.ListThreads(context.Background(), wire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads failed: %v", err)
	}
	for _, item := range list.Threads {
		if item.ID == "t-stale" && item.IsRunning {
			t.Fatalf("expected ListThreads to return IsRunning: false, got true")
		}
	}
}
