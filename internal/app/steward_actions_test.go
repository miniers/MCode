package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/automation"
	"mcode/internal/kanban"
	"mcode/internal/remote/wire"
	"mcode/internal/steward"
	"mcode/internal/store"
)

// fakeStewardSettings 是内存版设置存储，供真实护栏在测试里读写动作日志。
type fakeStewardSettings struct {
	mu sync.Mutex
	kv map[string]string
}

func newFakeStewardSettings() *fakeStewardSettings {
	return &fakeStewardSettings{kv: map[string]string{}}
}

func (f *fakeStewardSettings) GetSetting(_ context.Context, key string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, ok := f.kv[key]
	return value, ok, nil
}

func (f *fakeStewardSettings) SetSetting(_ context.Context, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kv[key] = value
	return nil
}

// fakeStewardRuntimeGate 取代 *steward.Runtime，只提供权限档与总管的会话 ID。
type fakeStewardRuntimeGate struct {
	canAct   bool
	threadID string
}

func (f *fakeStewardRuntimeGate) CanAct() bool     { return f.canAct }
func (f *fakeStewardRuntimeGate) ThreadID() string { return f.threadID }

// fakeStewardActionBackend 记录动作对会话后端的影响，并可按用例注入错误。
type fakeStewardActionBackend struct {
	threads map[string]wire.ThreadSummary

	getErr       error
	sendErr      error
	interruptErr error

	sent        []string
	interrupted []string
	notices     []string
}

func (f *fakeStewardActionBackend) GetThread(_ context.Context, threadID string) (wire.ThreadSummary, error) {
	if f.getErr != nil {
		return wire.ThreadSummary{}, f.getErr
	}
	summary, ok := f.threads[threadID]
	if !ok {
		return wire.ThreadSummary{}, fmt.Errorf("thread %s not found", threadID)
	}
	return summary, nil
}

func (f *fakeStewardActionBackend) SendFollowUp(_ context.Context, threadID, text string) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, threadID+"|"+text)
	return nil
}

func (f *fakeStewardActionBackend) InterruptCurrent(_ context.Context, threadID string) (wire.InterruptResult, error) {
	if f.interruptErr != nil {
		return wire.InterruptResult{}, f.interruptErr
	}
	f.interrupted = append(f.interrupted, threadID)
	return wire.InterruptResult{}, nil
}

func (f *fakeStewardActionBackend) AppendNotice(threadID, text string) error {
	f.notices = append(f.notices, threadID+"|"+text)
	return nil
}

// fakeStewardActionKanban 记录看板移动请求，并可按用例注入错误。
type fakeStewardActionKanban struct {
	moves []string
	err   error
}

func (f *fakeStewardActionKanban) MoveTask(_ context.Context, taskID string, status kanban.TaskStatus, expectedRevision int) (store.Task, error) {
	f.moves = append(f.moves, taskID+"->"+string(status)+"@"+strconv.Itoa(expectedRevision))
	if f.err != nil {
		return store.Task{}, f.err
	}
	return store.Task{ID: taskID, Status: string(status), Revision: expectedRevision + 1}, nil
}

// fakeStewardTaskReader 返回预置的看板任务。
type fakeStewardTaskReader struct {
	tasks map[string]store.Task
	err   error
}

func (f *fakeStewardTaskReader) GetTask(_ context.Context, id string) (store.Task, error) {
	if f.err != nil {
		return store.Task{}, f.err
	}
	task, ok := f.tasks[id]
	if !ok {
		return store.Task{}, store.ErrNotFound
	}
	return task, nil
}

// fakeStewardActionAutomation 记录 RunNow 调用次数。
type fakeStewardActionAutomation struct {
	calls int
	err   error
}

func (f *fakeStewardActionAutomation) RunNow(_ context.Context, id string) (store.AutomationRun, error) {
	f.calls++
	if f.err != nil {
		return store.AutomationRun{}, f.err
	}
	return store.AutomationRun{ID: "run-" + id, AutomationID: id}, nil
}

// fakeStewardForeground 模拟界面上报的前台会话。
type fakeStewardForeground struct {
	threadID string
}

func (f *fakeStewardForeground) ForegroundThread() string { return f.threadID }

// stewardActionsRig 是一套最小依赖，覆盖四个动作与真实动作护栏。
type stewardActionsRig struct {
	actions    *stewardActions
	backend    *fakeStewardActionBackend
	kanban     *fakeStewardActionKanban
	tasks      *fakeStewardTaskReader
	automation *fakeStewardActionAutomation
	client     *fakeStewardForeground
	runtime    *fakeStewardRuntimeGate
	guard      *steward.ActionGuard
	now        time.Time
}

// newStewardActionsRig 构造一套可动手的接线：总管的会话为 steward-1，界面前台为空，
// 时钟固定在 2024-05-01T10:00:00Z，便于构造「10 分钟内发过消息」的会话摘要。
func newStewardActionsRig() *stewardActionsRig {
	now := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	settings := newFakeStewardSettings()
	runtime := &fakeStewardRuntimeGate{canAct: true, threadID: "steward-1"}
	backend := &fakeStewardActionBackend{threads: map[string]wire.ThreadSummary{
		"idle":      {ID: "idle", ProjectID: "p1", Title: "空闲"},
		"helper":    {ID: "helper", IsHelper: true},
		"bee":       {ID: "bee", ParentThreadID: stewardStrPtr("lead-1")},
		"steward-1": {ID: "steward-1", Title: "总管"},
		"watching":  {ID: "watching"},
		"recent":    {ID: "recent", LastUserMessageAt: stewardStrPtr(now.Add(-2 * time.Minute).Format(time.RFC3339Nano))},
	}}
	kanbanSvc := &fakeStewardActionKanban{}
	tasks := &fakeStewardTaskReader{tasks: map[string]store.Task{
		"t1": {ID: "t1", Status: string(kanban.TaskStatusTodo), ThreadID: stewardStrPtr("idle"), Revision: 7},
		"t2": {ID: "t2", Status: string(kanban.TaskStatusTodo), Revision: 3},
	}}
	automationSvc := &fakeStewardActionAutomation{}
	client := &fakeStewardForeground{}
	guard := steward.NewActionGuard(settings, func() time.Time { return now }, 0)
	return &stewardActionsRig{
		actions: &stewardActions{
			backend:    backend,
			kanban:     kanbanSvc,
			tasks:      tasks,
			automation: automationSvc,
			client:     client,
			runtime:    runtime,
			guard:      guard,
			now:        func() time.Time { return now },
		},
		backend:    backend,
		kanban:     kanbanSvc,
		tasks:      tasks,
		automation: automationSvc,
		client:     client,
		runtime:    runtime,
		guard:      guard,
		now:        now,
	}
}

// 目标会话检查：总管自己、helper、bee、正在看、刚发过消息都要拒绝；
// 最后用户消息时间缺失或格式错误时按通过处理。
func TestCheckStewardThreadTarget(t *testing.T) {
	now := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	const stewardID = "steward-1"
	const watchingID = "watching"

	cases := []struct {
		name       string
		summary    wire.ThreadSummary
		foreground string
		wantErr    string
	}{
		{
			name:    "总管自身",
			summary: wire.ThreadSummary{ID: stewardID},
			wantErr: "不能对自己动手",
		},
		{
			name:    "helper 会话",
			summary: wire.ThreadSummary{ID: "helper", IsHelper: true},
			wantErr: "子代理",
		},
		{
			name:    "bee 会话",
			summary: wire.ThreadSummary{ID: "bee", ParentThreadID: stewardStrPtr("lead-1")},
			wantErr: "子代理",
		},
		{
			name:       "用户正在看的会话",
			summary:    wire.ThreadSummary{ID: watchingID},
			foreground: watchingID,
			wantErr:    "正在看着这个会话",
		},
		{
			name:    "10 分钟内发过消息",
			summary: wire.ThreadSummary{ID: "recent", LastUserMessageAt: stewardStrPtr(now.Add(-9 * time.Minute).Format(time.RFC3339Nano))},
			wantErr: "发过消息",
		},
		{
			name:    "全部通过",
			summary: wire.ThreadSummary{ID: "idle", LastUserMessageAt: stewardStrPtr(now.Add(-11 * time.Minute).Format(time.RFC3339Nano))},
		},
		{
			name:    "最后用户消息时间为空",
			summary: wire.ThreadSummary{ID: "idle"},
		},
		{
			name:    "最后用户消息时间格式错误",
			summary: wire.ThreadSummary{ID: "idle", LastUserMessageAt: stewardStrPtr("not-a-time")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkStewardThreadTarget(tc.summary, stewardID, tc.foreground, now)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("应放行，得到错误 %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("应拒绝，但没有返回错误")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误 %q 应包含 %q", err.Error(), tc.wantErr)
			}
			if !strings.Contains(err.Error(), "notify_owner") {
				t.Fatalf("拒绝说明应提示改用 notify_owner，得到 %q", err.Error())
			}
		})
	}
}

// 权限档不允许动手时四个动作都拒绝，且不碰任何后端。
func TestStewardActionsRejectWhenCannotAct(t *testing.T) {
	rig := newStewardActionsRig()
	rig.runtime.canAct = false
	ctx := context.Background()

	if err := rig.actions.SendMessage(ctx, "idle", "继续", "卡住了"); !errors.Is(err, errStewardActionNotAllowed) {
		t.Fatalf("SendMessage 错误 = %v, 期望 errStewardActionNotAllowed", err)
	}
	if err := rig.actions.InterruptThread(ctx, "idle", "卡住了"); !errors.Is(err, errStewardActionNotAllowed) {
		t.Fatalf("InterruptThread 错误 = %v, 期望 errStewardActionNotAllowed", err)
	}
	if _, err := rig.actions.RunAutomation(ctx, "auto-1", "重跑"); !errors.Is(err, errStewardActionNotAllowed) {
		t.Fatalf("RunAutomation 错误 = %v, 期望 errStewardActionNotAllowed", err)
	}
	if err := rig.actions.MoveKanbanTask(ctx, "t1", "review", "重开"); !errors.Is(err, errStewardActionNotAllowed) {
		t.Fatalf("MoveKanbanTask 错误 = %v, 期望 errStewardActionNotAllowed", err)
	}

	if len(rig.backend.sent) != 0 || len(rig.backend.interrupted) != 0 || len(rig.backend.notices) != 0 {
		t.Fatalf("不应调用会话后端，得到 sent=%v interrupted=%v notices=%v",
			rig.backend.sent, rig.backend.interrupted, rig.backend.notices)
	}
	if rig.automation.calls != 0 {
		t.Fatalf("不应调用 RunNow，得到 %d 次", rig.automation.calls)
	}
	if len(rig.kanban.moves) != 0 {
		t.Fatalf("不应移动看板任务，得到 %v", rig.kanban.moves)
	}
}

// reason 为空时四个动作都拒绝，且不碰任何后端。
func TestStewardActionsRejectEmptyReason(t *testing.T) {
	rig := newStewardActionsRig()
	ctx := context.Background()

	if err := rig.actions.SendMessage(ctx, "idle", "继续", "  "); !errors.Is(err, errStewardActionReasonRequired) {
		t.Fatalf("SendMessage 错误 = %v, 期望 errStewardActionReasonRequired", err)
	}
	if err := rig.actions.InterruptThread(ctx, "idle", ""); !errors.Is(err, errStewardActionReasonRequired) {
		t.Fatalf("InterruptThread 错误 = %v, 期望 errStewardActionReasonRequired", err)
	}
	if _, err := rig.actions.RunAutomation(ctx, "auto-1", ""); !errors.Is(err, errStewardActionReasonRequired) {
		t.Fatalf("RunAutomation 错误 = %v, 期望 errStewardActionReasonRequired", err)
	}
	if err := rig.actions.MoveKanbanTask(ctx, "t1", "review", "\t"); !errors.Is(err, errStewardActionReasonRequired) {
		t.Fatalf("MoveKanbanTask 错误 = %v, 期望 errStewardActionReasonRequired", err)
	}

	if len(rig.backend.sent) != 0 || len(rig.backend.interrupted) != 0 || len(rig.backend.notices) != 0 {
		t.Fatalf("不应调用会话后端，得到 sent=%v interrupted=%v notices=%v",
			rig.backend.sent, rig.backend.interrupted, rig.backend.notices)
	}
	if rig.automation.calls != 0 || len(rig.kanban.moves) != 0 {
		t.Fatalf("不应触发自动化或移动任务，得到 calls=%d moves=%v", rig.automation.calls, rig.kanban.moves)
	}
}

// 成功路径会调用后端、记次并在目标会话时间线留一条提示。
func TestStewardActionsSuccessPaths(t *testing.T) {
	rig := newStewardActionsRig()
	ctx := context.Background()

	if err := rig.actions.SendMessage(ctx, "idle", "请继续", "任务卡住"); err != nil {
		t.Fatalf("SendMessage 失败: %v", err)
	}
	if len(rig.backend.sent) != 1 || rig.backend.sent[0] != "idle|请继续" {
		t.Fatalf("投递记录 = %v, 期望 [idle|请继续]", rig.backend.sent)
	}
	if !stewardHasNotice(rig.backend.notices, "idle", "总管：已发送跟进消息（任务卡住）") {
		t.Fatalf("缺少发送提示，得到 %v", rig.backend.notices)
	}

	if err := rig.actions.InterruptThread(ctx, "idle", "跑偏了"); err != nil {
		t.Fatalf("InterruptThread 失败: %v", err)
	}
	if len(rig.backend.interrupted) != 1 || rig.backend.interrupted[0] != "idle" {
		t.Fatalf("中断记录 = %v, 期望 [idle]", rig.backend.interrupted)
	}
	if !stewardHasNotice(rig.backend.notices, "idle", "总管：已中断本轮（跑偏了）") {
		t.Fatalf("缺少中断提示，得到 %v", rig.backend.notices)
	}

	runID, err := rig.actions.RunAutomation(ctx, "auto-1", "重跑一次")
	if err != nil {
		t.Fatalf("RunAutomation 失败: %v", err)
	}
	if runID != "run-auto-1" || rig.automation.calls != 1 {
		t.Fatalf("RunAutomation 返回 %q、调用 %d 次，期望 run-auto-1 与 1 次", runID, rig.automation.calls)
	}

	if err := rig.actions.MoveKanbanTask(ctx, "t1", "review", "重开复核"); err != nil {
		t.Fatalf("MoveKanbanTask 失败: %v", err)
	}
	if len(rig.kanban.moves) != 1 || rig.kanban.moves[0] != "t1->review@7" {
		t.Fatalf("移动记录 = %v, 期望 [t1->review@7]", rig.kanban.moves)
	}
	if !stewardHasNotice(rig.backend.notices, "idle", "总管：已把看板任务移到 review（重开复核）") {
		t.Fatalf("缺少移动提示，得到 %v", rig.backend.notices)
	}
}

// 同一对象的第 4 次动作因护栏上限被拒，前三次仍然生效。
func TestStewardActionsCapReached(t *testing.T) {
	rig := newStewardActionsRig()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := rig.actions.SendMessage(ctx, "idle", "继续", "任务卡住"); err != nil {
			t.Fatalf("第 %d 次 SendMessage 应成功，得到 %v", i+1, err)
		}
	}
	err := rig.actions.SendMessage(ctx, "idle", "继续", "任务卡住")
	if !errors.Is(err, steward.ErrActionCapReached) {
		t.Fatalf("第 4 次 SendMessage 错误 = %v, 期望 ErrActionCapReached", err)
	}
	if len(rig.backend.sent) != 3 {
		t.Fatalf("投递次数 = %d, 期望 3（第 4 次应被护栏拦下）", len(rig.backend.sent))
	}
}

// 自动化正在运行时只提示无需重跑，且不占用护栏次数。
func TestStewardRunAutomationAlreadyRunningNotCounted(t *testing.T) {
	rig := newStewardActionsRig()
	rig.automation.err = automation.ErrAlreadyRunning
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		_, err := rig.actions.RunAutomation(ctx, "auto-1", "重跑一次")
		if !errors.Is(err, errStewardAutomationAlreadyRunning) {
			t.Fatalf("第 %d 次 RunAutomation 错误 = %v, 期望 errStewardAutomationAlreadyRunning", i+1, err)
		}
	}

	rig.automation.err = nil
	runID, err := rig.actions.RunAutomation(ctx, "auto-1", "重跑一次")
	if err != nil {
		t.Fatalf("恢复后 RunAutomation 应成功，得到 %v", err)
	}
	if runID != "run-auto-1" {
		t.Fatalf("运行 ID = %q, 期望 run-auto-1", runID)
	}
}

// 任务关联的会话正被用户看着时，移动任务同样被拒。
func TestStewardMoveKanbanTaskRejectsWatchedThread(t *testing.T) {
	rig := newStewardActionsRig()
	rig.tasks.tasks["t1"] = store.Task{ID: "t1", Status: string(kanban.TaskStatusTodo), ThreadID: stewardStrPtr("watching"), Revision: 2}
	rig.client.threadID = "watching"

	err := rig.actions.MoveKanbanTask(context.Background(), "t1", "review", "重开")
	if err == nil || !strings.Contains(err.Error(), "正在看着这个会话") {
		t.Fatalf("错误 = %v, 期望拒绝并说明用户正在看", err)
	}
	if len(rig.kanban.moves) != 0 {
		t.Fatalf("不应移动任务，得到 %v", rig.kanban.moves)
	}
}

// review 到 done 的直接移动被拒：验收和合并只能走评审接受；其他流转不受影响。
func TestStewardMoveKanbanTaskRejectsReviewToDone(t *testing.T) {
	rig := newStewardActionsRig()
	rig.tasks.tasks["t-review"] = store.Task{ID: "t-review", Status: string(kanban.TaskStatusReview), Revision: 4}
	ctx := context.Background()

	err := rig.actions.MoveKanbanTask(ctx, "t-review", "done", "看起来做完了")
	if err == nil || !strings.Contains(err.Error(), "待验收") {
		t.Fatalf("错误 = %v, 期望拒绝 review 直接移到 done", err)
	}
	if !strings.Contains(err.Error(), "notify_owner") {
		t.Fatalf("拒绝说明应提示改用 notify_owner，得到 %q", err.Error())
	}
	if len(rig.kanban.moves) != 0 {
		t.Fatalf("不应移动任务，得到 %v", rig.kanban.moves)
	}

	// 其他流转照常：review 到 todo 仍然可以移动
	if err := rig.actions.MoveKanbanTask(ctx, "t-review", "todo", "打回重做"); err != nil {
		t.Fatalf("review 到 todo 应放行，得到 %v", err)
	}
	if len(rig.kanban.moves) != 1 || rig.kanban.moves[0] != "t-review->todo@4" {
		t.Fatalf("移动记录 = %v, 期望 [t-review->todo@4]", rig.kanban.moves)
	}
}

// stewardHasNotice 报告提示列表里是否有指向 threadID、包含 text 的一条。
func stewardHasNotice(notices []string, threadID, text string) bool {
	for _, notice := range notices {
		if notice == threadID+"|"+text {
			return true
		}
	}
	return false
}
