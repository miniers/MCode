package remote

import (
	"context"
	"sync"
	"testing"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/testenv"
)

// 1. 临时线程发消息或调用 RetainThread 后转为正式线程，定时器不会删除它
func TestLifecycle_ProvisionalThread_Retained(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "Proj1", Path: t.TempDir()})

	// 场景 A: RetainThread 转正
	provisionalTrue := true
	res1, err := b.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID:   "p1",
		ActionID:    "act-1",
		Provisional: &provisionalTrue,
	})
	if err != nil {
		t.Fatalf("CreateThread 1 failed: %v", err)
	}

	th1, err := b.GetThread(ctx, res1.ThreadID)
	if err != nil {
		t.Fatalf("GetThread 1 failed: %v", err)
	}
	if !th1.IsProvisional {
		t.Fatalf("expected thread 1 to be provisional, got false")
	}

	b.SetProvisionalCleanupDelay(20 * time.Millisecond)
	b.ScheduleProvisionalCleanup(res1.ThreadID)

	// 转正
	if err := b.RetainThread(ctx, res1.ThreadID); err != nil {
		t.Fatalf("RetainThread failed: %v", err)
	}

	th1After, err := b.GetThread(ctx, res1.ThreadID)
	if err != nil {
		t.Fatalf("GetThread 1 after retain failed: %v", err)
	}
	if th1After.IsProvisional {
		t.Fatalf("expected thread 1 to be retained (provisional=false), got true")
	}

	// 等待定时器时间过后，验证未被删除
	time.Sleep(50 * time.Millisecond)
	if _, err := b.GetThread(ctx, res1.ThreadID); err != nil {
		t.Fatalf("thread 1 should not be deleted after being retained: %v", err)
	}

	// 场景 B: SendMessage 转正
	res2, err := b.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID:   "p1",
		ActionID:    "act-2",
		Provisional: &provisionalTrue,
	})
	if err != nil {
		t.Fatalf("CreateThread 2 failed: %v", err)
	}

	b.ScheduleProvisionalCleanup(res2.ThreadID)
	_, _ = b.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: res2.ThreadID,
		Text:     "hello world",
	})

	th2After, err := b.GetThread(ctx, res2.ThreadID)
	if err != nil {
		t.Fatalf("GetThread 2 after message failed: %v", err)
	}
	if th2After.IsProvisional {
		t.Fatalf("expected thread 2 to be retained after SendMessage, got true")
	}

	time.Sleep(50 * time.Millisecond)
	if _, err := b.GetThread(ctx, res2.ThreadID); err != nil {
		t.Fatalf("thread 2 should not be deleted after message: %v", err)
	}
}

// 2. 空的临时线程在 ScheduleProvisionalCleanup 后被删除（把延迟设为 10ms，轮询等待）
func TestLifecycle_ProvisionalThread_CleanupEmpty(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "Proj1", Path: t.TempDir()})

	provisionalTrue := true
	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID:   "p1",
		ActionID:    "act-cleanup",
		Provisional: &provisionalTrue,
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	if !b.CanDiscardEmptyProvisional(res.ThreadID) {
		t.Fatalf("expected CanDiscardEmptyProvisional to be true for newly created empty provisional thread")
	}

	b.SetProvisionalCleanupDelay(10 * time.Millisecond)
	b.ScheduleProvisionalCleanup(res.ThreadID)

	// 轮询等待其被删除
	deadline := time.Now().Add(500 * time.Millisecond)
	deleted := false
	for time.Now().Before(deadline) {
		if _, err := b.GetThread(ctx, res.ThreadID); err != nil {
			deleted = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !deleted {
		t.Fatalf("expected empty provisional thread to be deleted automatically by cleanup timer")
	}
}

// 3. 已休眠线程收到 SendMessage 后取消休眠
func TestLifecycle_DormantThread_UndormantOnSendMessage(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "Proj1", Path: t.TempDir()})

	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: "p1",
		ActionID:  "act-settle",
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	dormantTrue := true
	_, err = b.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID: res.ThreadID,
		Dormant:  &dormantTrue,
	})
	if err != nil {
		t.Fatalf("UpdateThread dormant failed: %v", err)
	}

	th, err := b.GetThread(ctx, res.ThreadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if !th.IsDormant {
		t.Fatalf("expected thread to be dormant, got false")
	}
	if _, ok := b.ThreadDormantAt(res.ThreadID); !ok {
		t.Fatalf("expected ThreadDormantAt to return ok=true")
	}

	// 收到 SendMessage 后自动取消休眠
	_, _ = b.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: res.ThreadID,
		Text:     "resuming discussion",
	})

	thAfter, err := b.GetThread(ctx, res.ThreadID)
	if err != nil {
		t.Fatalf("GetThread after SendMessage failed: %v", err)
	}
	if thAfter.IsDormant {
		t.Fatalf("expected thread to be undormant after SendMessage, got true")
	}
	if _, ok := b.ThreadDormantAt(res.ThreadID); ok {
		t.Fatalf("expected ThreadDormantAt to return ok=false after unsettling")
	}
}

// 4. 归档父线程时子线程级联归档，并清除置顶和休眠
func TestLifecycle_ArchiveParent_CascadesFamily(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "Proj1", Path: t.TempDir()})

	// 创建父线程
	resParent, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1", ActionID: "a-parent"})
	if err != nil {
		t.Fatalf("CreateThread parent failed: %v", err)
	}
	// 创建子线程
	resChild, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1", ActionID: "a-child"})
	if err != nil {
		t.Fatalf("CreateThread child failed: %v", err)
	}
	// 创建孙线程
	resGrandChild, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1", ActionID: "a-grandchild"})
	if err != nil {
		t.Fatalf("CreateThread grandchild failed: %v", err)
	}
	// 创建无关线程
	resOther, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1", ActionID: "a-other"})
	if err != nil {
		t.Fatalf("CreateThread other failed: %v", err)
	}

	b.SetThreadRelation(resChild.ThreadID, resParent.ThreadID, false)
	b.SetThreadRelation(resGrandChild.ThreadID, resChild.ThreadID, true) // helper 子线程

	// 设置置顶和休眠
	pinnedTrue := true
	dormantTrue := true
	for _, tid := range []string{resParent.ThreadID, resChild.ThreadID, resGrandChild.ThreadID} {
		_, _ = b.UpdateThread(ctx, wire.ThreadUpdateParams{
			ThreadID: tid,
			Pinned:   &pinnedTrue,
			Dormant:  &dormantTrue,
		})
	}

	// 归档父线程
	archivedTrue := true
	_, err = b.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID: resParent.ThreadID,
		Archived: &archivedTrue,
	})
	if err != nil {
		t.Fatalf("UpdateThread archive parent failed: %v", err)
	}

	// 验证整个家族均被归档，且置顶与休眠被清除
	for _, tid := range []string{resParent.ThreadID, resChild.ThreadID, resGrandChild.ThreadID} {
		th, err := b.GetThread(ctx, tid)
		if err != nil {
			t.Fatalf("GetThread %s failed: %v", tid, err)
		}
		if !th.IsArchived {
			t.Errorf("expected thread %s to be archived", tid)
		}
		if th.IsPinned {
			t.Errorf("expected thread %s pinned to be cleared, got true", tid)
		}
		if th.IsDormant {
			t.Errorf("expected thread %s dormant to be cleared, got true", tid)
		}
	}

	// 验证无关线程保持原样
	thOther, err := b.GetThread(ctx, resOther.ThreadID)
	if err != nil {
		t.Fatalf("GetThread other failed: %v", err)
	}
	if thOther.IsArchived {
		t.Errorf("expected unrelated thread %s not to be archived", resOther.ThreadID)
	}
}

// 5. 删除父线程时级联删除子线程，hook 对每个线程都被调用一次
func TestLifecycle_DeleteParent_CascadesFamilyAndHooks(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "Proj1", Path: t.TempDir()})

	// 创建父、子、孙三个线程
	resParent, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1", ActionID: "del-parent"})
	if err != nil {
		t.Fatalf("CreateThread parent failed: %v", err)
	}
	resChild, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1", ActionID: "del-child"})
	if err != nil {
		t.Fatalf("CreateThread child failed: %v", err)
	}
	resGrandChild, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1", ActionID: "del-grandchild"})
	if err != nil {
		t.Fatalf("CreateThread grandchild failed: %v", err)
	}

	b.SetThreadRelation(resChild.ThreadID, resParent.ThreadID, false)
	b.SetThreadRelation(resGrandChild.ThreadID, resChild.ThreadID, false)

	var mu sync.Mutex
	var deletedHooks []string
	b.SetThreadDeletedHook(func(ctx context.Context, threadID string) {
		mu.Lock()
		deletedHooks = append(deletedHooks, threadID)
		mu.Unlock()
	})

	var changedPayload wire.ThreadsChanged
	var changedCalled bool
	b.SetThreadsChangedHook(func(tc wire.ThreadsChanged) {
		changedCalled = true
		changedPayload = tc
	})

	// 删除父线程
	if err := b.DeleteThread(ctx, resParent.ThreadID); err != nil {
		t.Fatalf("DeleteThread failed: %v", err)
	}

	// 验证每个线程都被调用了 hook，且顺序从叶子到根
	mu.Lock()
	hooksCount := len(deletedHooks)
	order := make([]string, len(deletedHooks))
	copy(order, deletedHooks)
	mu.Unlock()

	if hooksCount != 3 {
		t.Fatalf("expected hook to be called 3 times, got %d: %v", hooksCount, order)
	}

	expectedOrder := []string{resGrandChild.ThreadID, resChild.ThreadID, resParent.ThreadID}
	for i, id := range expectedOrder {
		if order[i] != id {
			t.Errorf("hook call %d expected %s, got %s", i, id, order[i])
		}
	}

	// 验证内存中三个线程全部已被删除
	for _, id := range expectedOrder {
		if _, err := b.GetThread(ctx, id); err == nil {
			t.Errorf("expected thread %s to be deleted, but still exists", id)
		}
	}

	// 验证广播 RemovedThreadIDs 包含所有被删的 id
	if !changedCalled {
		t.Fatalf("expected ThreadsChanged hook to be called")
	}
	if len(changedPayload.RemovedThreadIDs) != 3 {
		t.Fatalf("expected RemovedThreadIDs to have 3 items, got %d: %+v", len(changedPayload.RemovedThreadIDs), changedPayload.RemovedThreadIDs)
	}
}
