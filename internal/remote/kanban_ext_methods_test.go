package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"mcode/internal/kanban"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// extKanbanService 在 mockKanbanService 之上补出远程扩展需要的删除与救援能力，
// 并记录调用参数，方便断言参数透传与幂等重放。
type extKanbanService struct {
	*mockKanbanService

	mu          sync.Mutex
	deleteErr   error
	rescueErr   error
	deletedIDs  []string
	deleteRevs  []int
	rescueCalls []extRescueCall
	rescued     store.Task
}

type extRescueCall struct {
	taskID string
	action kanban.RescueAction
	rev    int
}

func newExtKanbanService() *extKanbanService {
	return &extKanbanService{mockKanbanService: newMockKanbanService()}
}

func (s *extKanbanService) DeleteTask(ctx context.Context, taskID string, expectedRevision int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletedIDs = append(s.deletedIDs, taskID)
	s.deleteRevs = append(s.deleteRevs, expectedRevision)
	return s.deleteErr
}

func (s *extKanbanService) RescueTask(ctx context.Context, taskID string, action kanban.RescueAction, expectedRevision int) (store.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rescueCalls = append(s.rescueCalls, extRescueCall{taskID: taskID, action: action, rev: expectedRevision})
	if s.rescueErr != nil {
		return store.Task{}, s.rescueErr
	}
	if s.rescued.ID != "" {
		return s.rescued, nil
	}
	return store.Task{ID: taskID, Status: string(kanban.TaskStatusTodo), Revision: 2}, nil
}

func (s *extKanbanService) deleteCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.deletedIDs)
}

func (s *extKanbanService) rescueCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rescueCalls)
}

// newKanbanExtTestDispatcher 构造带看板服务与已认证录制连接的 Dispatcher。
func newKanbanExtTestDispatcher(t *testing.T, svc KanbanService) (*Dispatcher, *recordConn) {
	t.Helper()
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
	d := NewDispatcher(&fakeAuth{}, backend)
	if svc != nil {
		d.SetKanban(svc)
	}
	conn := &recordConn{fakeConn: fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}}
	d.addAuthedConn(conn)
	t.Cleanup(func() { d.RemoveConn(conn) })
	return d, conn
}

func kanbanExtDecodeMutation(t *testing.T, resp wire.Response) wire.KanbanTaskMutationResult {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var res wire.KanbanTaskMutationResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return res
}

func TestKanbanDeleteTask_SuccessAndIdempotent(t *testing.T) {
	svc := newExtKanbanService()
	d, conn := newKanbanExtTestDispatcher(t, svc)
	taskID := "55555555-5555-5555-5555-555555555555"
	actionID := "66666666-6666-6666-6666-666666666666"

	req := fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"kanban.deleteTask","params":{"taskID":%q,"expectedRevision":3,"actionID":%q},"id":1}`, taskID, actionID)
	res := kanbanExtDecodeMutation(t, callExtension(t, d, conn, req))
	if res.TaskID == nil || *res.TaskID != taskID {
		t.Fatalf("删除结果应带上 taskID，得到 %+v", res)
	}
	// 删除后任务已不存在，不返回修订号与目标。
	if res.Revision != nil || res.GoalID != nil {
		t.Fatalf("删除结果不该带 revision/goalID，得到 %+v", res)
	}
	if svc.deleteCallCount() != 1 || svc.deleteRevs[0] != 3 {
		t.Fatalf("删除参数未透传：%+v", svc.deleteRevs)
	}
	// 成功后按现有机制广播看板变化。
	if len(conn.notifications) != 1 || conn.notifications[0] != "kanban.taskUpdated" {
		t.Fatalf("删除后应广播 kanban.taskUpdated，得到 %+v", conn.notifications)
	}

	// 幂等重放：同一个 actionID 不再调用服务，直接返回首次结果。
	res = kanbanExtDecodeMutation(t, callExtension(t, d, conn, req))
	if res.TaskID == nil || *res.TaskID != taskID {
		t.Fatalf("重放结果应与首次一致，得到 %+v", res)
	}
	if svc.deleteCallCount() != 1 {
		t.Fatalf("重放不该再次删除，调用次数 %d", svc.deleteCallCount())
	}
	if len(conn.notifications) != 1 {
		t.Fatalf("重放不该再次广播，得到 %+v", conn.notifications)
	}
}

func TestKanbanDeleteTask_Errors(t *testing.T) {
	svc := newExtKanbanService()
	d, conn := newKanbanExtTestDispatcher(t, svc)
	taskID := "55555555-5555-5555-5555-555555555555"

	cases := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"修订号冲突", store.ErrRevisionConflict, wire.ErrCodeVersionConflict},
		{"任务不存在", store.ErrNotFound, wire.ErrCodeResourceNotFound},
		{"运行中任务被拒绝", kanban.ErrInvalidTransition, wire.ErrCodeOperationRejected},
	}
	for i, tc := range cases {
		svc.mu.Lock()
		svc.deleteErr = tc.err
		svc.mu.Unlock()
		req := fmt.Sprintf(
			`{"jsonrpc":"2.0","method":"kanban.deleteTask","params":{"taskID":%q,"expectedRevision":0,"actionID":%q},"id":%d}`,
			taskID, fmt.Sprintf("77777777-7777-7777-7777-77777777777%d", i), i)
		resp := callExtension(t, d, conn, req)
		if resp.Error == nil || resp.Error.Code != tc.wantCode {
			t.Fatalf("%s 应返回错误码 %d，得到 %+v", tc.name, tc.wantCode, resp.Error)
		}
	}
	// 失败时不广播。
	if len(conn.notifications) != 0 {
		t.Fatalf("失败时不该广播，得到 %+v", conn.notifications)
	}
}

func TestKanbanDeleteTask_InvalidParams(t *testing.T) {
	svc := newExtKanbanService()
	d, conn := newKanbanExtTestDispatcher(t, svc)
	actionID := "66666666-6666-6666-6666-666666666666"
	taskID := "55555555-5555-5555-5555-555555555555"

	requests := []string{
		`{"jsonrpc":"2.0","method":"kanban.deleteTask","params":{"taskID":"not-a-uuid","actionID":"` + actionID + `"},"id":1}`,
		`{"jsonrpc":"2.0","method":"kanban.deleteTask","params":{"taskID":"` + taskID + `","expectedRevision":-1,"actionID":"` + actionID + `"},"id":2}`,
		`{"jsonrpc":"2.0","method":"kanban.deleteTask","params":{"taskID":"` + taskID + `","actionID":"not-a-uuid"},"id":3}`,
		`{"jsonrpc":"2.0","method":"kanban.deleteTask","params":{"taskID":"` + taskID + `","actionID":"` + actionID + `","unknown":true},"id":4}`,
	}
	for _, request := range requests {
		resp := callExtension(t, d, conn, request)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
			t.Fatalf("非法参数应返回 InvalidParams，得到 %+v（%s）", resp.Error, request)
		}
	}
	if svc.deleteCallCount() != 0 {
		t.Fatalf("非法参数不该调用服务，调用次数 %d", svc.deleteCallCount())
	}
}

func TestKanbanRescueTask_SuccessAndIdempotent(t *testing.T) {
	svc := newExtKanbanService()
	d, conn := newKanbanExtTestDispatcher(t, svc)
	taskID := "55555555-5555-5555-5555-555555555555"
	actionID := "66666666-6666-6666-6666-666666666666"

	req := fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"kanban.rescueTask","params":{"taskID":%q,"action":"interrupt","expectedRevision":4,"actionID":%q},"id":1}`,
		taskID, actionID)
	res := kanbanExtDecodeMutation(t, callExtension(t, d, conn, req))
	if res.TaskID == nil || *res.TaskID != taskID {
		t.Fatalf("救援结果应带上 taskID，得到 %+v", res)
	}
	if res.Revision == nil || *res.Revision != 2 {
		t.Fatalf("救援结果应带上更新后的修订号，得到 %+v", res)
	}
	if svc.rescueCallCount() != 1 || svc.rescueCalls[0].action != kanban.RescueActionInterrupt || svc.rescueCalls[0].rev != 4 {
		t.Fatalf("救援参数未透传：%+v", svc.rescueCalls)
	}
	if len(conn.notifications) != 1 || conn.notifications[0] != "kanban.taskUpdated" {
		t.Fatalf("救援后应广播 kanban.taskUpdated，得到 %+v", conn.notifications)
	}

	// 幂等重放：同一个 actionID 不再调用服务。
	callExtension(t, d, conn, req)
	if svc.rescueCallCount() != 1 {
		t.Fatalf("重放不该再次救援，调用次数 %d", svc.rescueCallCount())
	}

	// 另一种动作：提醒 Agent。
	nudgeReq := fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"kanban.rescueTask","params":{"taskID":%q,"action":"nudge","expectedRevision":0,"actionID":"88888888-8888-8888-8888-888888888888"},"id":2}`,
		taskID)
	callExtension(t, d, conn, nudgeReq)
	if svc.rescueCallCount() != 2 || svc.rescueCalls[1].action != kanban.RescueActionNudge {
		t.Fatalf("nudge 未透传：%+v", svc.rescueCalls)
	}
}

func TestKanbanRescueTask_Errors(t *testing.T) {
	svc := newExtKanbanService()
	d, conn := newKanbanExtTestDispatcher(t, svc)
	taskID := "55555555-5555-5555-5555-555555555555"

	// 非法动作：不在枚举内。
	resp := callExtension(t, d, conn, fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"kanban.rescueTask","params":{"taskID":%q,"action":"restart","actionID":"66666666-6666-6666-6666-666666666666"},"id":1}`, taskID))
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("非法动作应返回 InvalidParams，得到 %+v", resp.Error)
	}
	if svc.rescueCallCount() != 0 {
		t.Fatalf("非法动作不该调用服务，调用次数 %d", svc.rescueCallCount())
	}

	cases := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"修订号冲突", store.ErrRevisionConflict, wire.ErrCodeVersionConflict},
		{"任务不存在", store.ErrNotFound, wire.ErrCodeResourceNotFound},
		{"驱动不支持中断", kanban.ErrInterruptUnsupported, wire.ErrCodeOperationRejected},
		{"驱动不支持催促", kanban.ErrNudgeUnsupported, wire.ErrCodeOperationRejected},
		{"任务状态不允许", kanban.ErrInvalidTransition, wire.ErrCodeOperationRejected},
	}
	for i, tc := range cases {
		svc.mu.Lock()
		svc.rescueErr = tc.err
		svc.mu.Unlock()
		req := fmt.Sprintf(
			`{"jsonrpc":"2.0","method":"kanban.rescueTask","params":{"taskID":%q,"action":"interrupt","actionID":"99999999-9999-9999-9999-99999999999%d"},"id":%d}`,
			taskID, i, i+2)
		resp := callExtension(t, d, conn, req)
		if resp.Error == nil || resp.Error.Code != tc.wantCode {
			t.Fatalf("%s 应返回错误码 %d，得到 %+v", tc.name, tc.wantCode, resp.Error)
		}
	}
	if len(conn.notifications) != 0 {
		t.Fatalf("失败时不该广播，得到 %+v", conn.notifications)
	}
}

func TestKanbanExtMethods_ServiceUnavailable(t *testing.T) {
	// 没有注入看板服务：与内置看板方法一致，报 OperationRejected。
	d, conn := newKanbanExtTestDispatcher(t, nil)
	taskID := "55555555-5555-5555-5555-555555555555"
	actionID := "66666666-6666-6666-6666-666666666666"
	resp := callExtension(t, d, conn, fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"kanban.deleteTask","params":{"taskID":%q,"actionID":%q},"id":1}`, taskID, actionID))
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeOperationRejected {
		t.Fatalf("未注入看板服务时应返回 OperationRejected，得到 %+v", resp.Error)
	}

	// 注入了看板服务但不支持救援：按未实现处理，前端据此隐藏入口。
	plain := newMockKanbanService()
	d2, conn2 := newKanbanExtTestDispatcher(t, plain)
	resp = callExtension(t, d2, conn2, fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"kanban.rescueTask","params":{"taskID":%q,"action":"interrupt","actionID":%q},"id":1}`, taskID, actionID))
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("服务不支持救援时应返回 MethodNotFound，得到 %+v", resp.Error)
	}
}

func TestKanbanExtMethods_Implemented(t *testing.T) {
	methods := make(map[string]bool)
	for _, name := range ImplementedMethods() {
		methods[name] = true
	}
	for _, name := range []string{"kanban.deleteTask", "kanban.rescueTask"} {
		if !methods[name] {
			t.Fatalf("%s 应出现在 capabilities 的方法清单里", name)
		}
	}
}
