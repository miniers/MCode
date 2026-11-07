package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/remote"
	mcodeui "mcode/internal/ui"
)

// fakeStewardClient 是 stewardExtClient 的假实现，记录收到的参数并可按需返回错误。
type fakeStewardClient struct {
	openThreadID string

	startProjectID string
	startRequest   string
	startNewID     string

	topicThreadID  string
	reviewThreadID string
	reviewTaskID   string
	reviewDecision string
	reviewFeedback string
	reviewRevision int
	reviewErr      error
}

func (f *fakeStewardClient) OpenStewardThread(ctx context.Context) (string, error) {
	return f.openThreadID, nil
}

func (f *fakeStewardClient) StartStewardTopic(ctx context.Context, projectID, request string) (string, error) {
	f.startProjectID = projectID
	f.startRequest = request
	return f.startNewID, nil
}

func (f *fakeStewardClient) ReplanStewardTopic(ctx context.Context, threadID string) error {
	f.topicThreadID = threadID
	return nil
}

func (f *fakeStewardClient) ConfirmStewardTopic(ctx context.Context, threadID string) error {
	f.topicThreadID = threadID
	return nil
}

func (f *fakeStewardClient) CloseStewardTopic(ctx context.Context, threadID string) error {
	f.topicThreadID = threadID
	return nil
}

func (f *fakeStewardClient) DeleteStewardTopic(ctx context.Context, threadID string) error {
	f.topicThreadID = threadID
	return nil
}

func (f *fakeStewardClient) ReviewTask(ctx context.Context, taskID, decision, feedback string, revision int) (mcodeui.KanbanTask, error) {
	f.reviewTaskID = taskID
	f.reviewDecision = decision
	f.reviewFeedback = feedback
	f.reviewRevision = revision
	if f.reviewErr != nil {
		return mcodeui.KanbanTask{}, f.reviewErr
	}
	return mcodeui.KanbanTask{ID: taskID, Revision: revision}, nil
}

// TestStewardServicePassThrough 校验远程服务把参数原样交给 Client，与桌面端按钮走同一条链路。
func TestStewardServicePassThrough(t *testing.T) {
	ctx := context.Background()
	client := &fakeStewardClient{openThreadID: "steward-thread", startNewID: "topic-new"}
	svc := remoteStewardService{client: client}

	threadID, err := svc.Open(ctx, "p1")
	if err != nil || threadID != "steward-thread" {
		t.Fatalf("Open = (%q, %v)", threadID, err)
	}

	// threadID 是提出需求的会话，Client.StartStewardTopic 只按项目与需求开话题。
	newID, err := svc.StartTopic(ctx, "p1", "topic-1", "把登录改成扫码")
	if err != nil || newID != "topic-new" {
		t.Fatalf("StartTopic = (%q, %v)", newID, err)
	}
	if client.startProjectID != "p1" || client.startRequest != "把登录改成扫码" {
		t.Fatalf("StartTopic 参数未透传: %+v", client)
	}

	actions := map[string]func(context.Context, string) error{
		"replan":  svc.ReplanTopic,
		"confirm": svc.ConfirmTopic,
		"close":   svc.CloseTopic,
		"delete":  svc.DeleteTopic,
	}
	for name, action := range actions {
		client.topicThreadID = ""
		if err := action(ctx, "topic-1"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if client.topicThreadID != "topic-1" {
			t.Fatalf("%s 未透传 threadID: %q", name, client.topicThreadID)
		}
	}

	if err := svc.ReviewTask(ctx, "topic-1", "task-9", "accept", "", 4); err != nil {
		t.Fatalf("ReviewTask: %v", err)
	}
	if client.reviewTaskID != "task-9" || client.reviewDecision != "accept" || client.reviewFeedback != "" || client.reviewRevision != 4 {
		t.Fatalf("ReviewTask 参数未透传: %+v", client)
	}
}

// TestStewardServiceMapsRevisionConflict 校验修订号冲突映射成远程层可识别的哨兵错误，其它错误原样返回。
func TestStewardServiceMapsRevisionConflict(t *testing.T) {
	ctx := context.Background()
	conflict := remoteStewardService{client: &fakeStewardClient{
		reviewErr: fmt.Errorf("%w: revision 3 != 5", mcodeui.ErrRevisionConflict),
	}}
	err := conflict.ReviewTask(ctx, "topic-1", "task-9", "accept", "", 3)
	if !errors.Is(err, remote.ErrStewardRevisionConflict) {
		t.Fatalf("应映射成 remote.ErrStewardRevisionConflict，实际 %v", err)
	}

	other := remoteStewardService{client: &fakeStewardClient{reviewErr: errors.New("目标当前状态是 plan_failed")}}
	err = other.ReviewTask(ctx, "topic-1", "task-9", "accept", "", 3)
	if err == nil || !strings.Contains(err.Error(), "plan_failed") {
		t.Fatalf("其它错误应原样返回，实际 %v", err)
	}
	if errors.Is(err, remote.ErrStewardRevisionConflict) {
		t.Fatalf("非冲突错误不该映射成版本冲突: %v", err)
	}

	if err := (remoteStewardService{}).ReviewTask(ctx, "topic-1", "task-9", "accept", "", 3); !errors.Is(err, errStewardServiceUnavailable) {
		t.Fatalf("client 为空时应返回不可用错误，实际 %v", err)
	}
}

// TestStewardChangeDebouncerMergesBursts 校验一串变化只广播一条，静默后再变化才补第二条。
func TestStewardChangeDebouncerMergesBursts(t *testing.T) {
	var count atomic.Int32
	d := newStewardChangeDebouncer(20*time.Millisecond, func() { count.Add(1) })
	defer d.stop()

	for i := 0; i < 5; i++ {
		d.trigger()
		time.Sleep(2 * time.Millisecond)
	}
	// 触发间隔都小于窗口，等窗口过去后只应有一条。
	time.Sleep(100 * time.Millisecond)
	if got := count.Load(); got != 1 {
		t.Fatalf("一串变化应合并成一条通知，实际 %d 条", got)
	}

	d.trigger()
	time.Sleep(100 * time.Millisecond)
	if got := count.Load(); got != 2 {
		t.Fatalf("静默后再次变化应补一条通知，实际 %d 条", got)
	}
}

// TestStewardChangeDebouncerStopCancelsPending 校验 stop 之后不再发出已经排队的通知。
func TestStewardChangeDebouncerStopCancelsPending(t *testing.T) {
	var count atomic.Int32
	d := newStewardChangeDebouncer(20*time.Millisecond, func() { count.Add(1) })
	d.trigger()
	d.stop()
	time.Sleep(80 * time.Millisecond)
	d.trigger()
	time.Sleep(80 * time.Millisecond)
	if got := count.Load(); got != 0 {
		t.Fatalf("stop 之后不该再发通知，实际 %d 条", got)
	}
}
