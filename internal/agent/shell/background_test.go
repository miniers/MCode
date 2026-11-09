//go:build unix

package shell

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
)

func waitBackground(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待后台任务状态超时")
}

func TestBackgroundOutputAndKill(t *testing.T) {
	id, pid, err := Start(context.Background(), Options{Command: "echo hi; sleep 30"})
	if err != nil || id == "" || pid <= 0 {
		t.Fatalf("id=%q pid=%d err=%v", id, pid, err)
	}
	var text string
	var running bool
	waitBackground(t, func() bool {
		var outErr error
		text, running, _, outErr = Output(id)
		return outErr == nil && strings.Contains(text, "hi")
	})
	if !running {
		t.Fatal("任务应仍在运行")
	}
	text, _, _, err = Output(id)
	if err != nil || strings.Contains(text, "hi") {
		t.Fatalf("重复输出=%q err=%v", text, err)
	}
	if err := Kill(id); err != nil {
		t.Fatal(err)
	}
	waitBackground(t, func() bool {
		_, running, _, _ := Output(id)
		return !running
	})
}

func TestBackgroundOwnerIsolation(t *testing.T) {
	a := WithOwner(context.Background(), "test-owner-a")
	b := WithOwner(context.Background(), "test-owner-b")
	idA, _, err := Start(a, Options{Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	defer Kill(idA)
	idB, _, err := Start(b, Options{Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	defer Kill(idB)
	if _, _, _, err := OutputOwner("test-owner-a", idB); err == nil {
		t.Fatal("跨线程读取成功")
	}
	if err := KillOwned("test-owner-a", idB); err == nil {
		t.Fatal("跨线程停止成功")
	}
	finished := make(chan struct{})
	go func() { KillOwner("test-owner-a"); KillOwner("test-owner-a"); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("停止超时")
	}
	if _, running, _, _ := Output(idA); running {
		t.Fatal("A 仍在运行")
	}
	if _, running, _, _ := Output(idB); !running {
		t.Fatal("B 被停止")
	}
}

func TestKillOwnerIsolatesThreads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("需要 Unix shell")
	}
	const ownerA, ownerB = "kill-owner-isolation-a", "kill-owner-isolation-b"
	t.Cleanup(func() { KillOwner(ownerA); KillOwner(ownerB) })
	idA, _, err := Start(WithOwner(context.Background(), ownerA), Options{Command: `sh -c 'trap "" TERM; echo ready; sleep 30'`})
	if err != nil {
		t.Fatal(err)
	}
	idB, _, err := Start(WithOwner(context.Background(), ownerB), Options{Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	waitBackground(t, func() bool {
		text, running, _, err := OutputOwner(ownerA, idA)
		return err == nil && running && strings.Contains(text, "ready")
	})
	done := make(chan struct{})
	go func() { KillOwner(ownerA); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("停止 A 超时")
	}
	waitBackground(t, func() bool {
		_, running, _, err := OutputOwner(ownerA, idA)
		return err == nil && !running
	})
	if _, running, _, err := OutputOwner(ownerB, idB); err != nil || !running {
		t.Fatalf("B running=%v err=%v", running, err)
	}
	done = make(chan struct{})
	go func() { KillOwner(ownerA); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("重复停止 A 超时")
	}
}

func TestKillOwnerConcurrent(t *testing.T) {
	const owner = "kill-owner-concurrent"
	t.Cleanup(func() { KillOwner(owner) })
	var ids []string
	for i := 0; i < 3; i++ {
		id, _, err := Start(WithOwner(context.Background(), owner), Options{Command: "sleep 30"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(2)
		go func() { defer wg.Done(); KillOwner(owner) }()
		go func(id string) { defer wg.Done(); _ = KillOwned(owner, id) }(id)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("并发停止超时")
	}
	for _, id := range ids {
		if _, running, _, err := OutputOwner(owner, id); err != nil || running {
			t.Errorf("任务 %s running=%v err=%v", id, running, err)
		}
	}
}

func TestBackgroundExitCallbackPanic(t *testing.T) {
	ctx := WithProcessTracker(context.Background(), nil, func(int) { panic("退出回调失败") })
	id, _, err := Start(ctx, Options{Command: "echo ok", Sandbox: panicCleanupSandbox{}})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	waitBackground(t, func() bool { out, running, _, _ := Output(id); text += out; return !running })
	if !strings.Contains(text, "退出回调失败") || !strings.Contains(text, "清理失败") {
		t.Fatalf("输出=%q", text)
	}
	if err := Kill(id); err != nil {
		t.Fatal(err)
	}
}

type panicCleanupSandbox struct{}

func (panicCleanupSandbox) Command(_ context.Context, _ string, _ harness.PermissionMode, name string, args ...string) (*exec.Cmd, func(), error) {
	return exec.Command(name, args...), func() { panic("清理失败") }, nil
}

func TestBackgroundSurvivesContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	id, _, err := Start(ctx, Options{Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	defer Kill(id)
	cancel()
	time.Sleep(100 * time.Millisecond)
	if _, running, _, err := Output(id); err != nil || !running {
		t.Fatalf("running=%v err=%v", running, err)
	}
}

func TestBackgroundTrackerLifecycle(t *testing.T) {
	started := make(chan int, 1)
	exited := make(chan int, 1)
	ctx := WithProcessTracker(context.Background(),
		func(pid int) { started <- pid },
		func(pid int) { exited <- pid },
	)
	id, pid, err := Start(ctx, Options{Command: "echo tracked"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-started:
		if got != pid {
			t.Fatalf("onStart = %d, pid = %d", got, pid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("onStart 未被调用")
	}
	select {
	case got := <-exited:
		if got != pid {
			t.Fatalf("onExit = %d, pid = %d", got, pid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("onExit 未被调用")
	}
	if text, running, exitCode, err := Output(id); err != nil || running || exitCode != 0 || !strings.Contains(text, "tracked") {
		t.Fatalf("text=%q running=%v exitCode=%d err=%v", text, running, exitCode, err)
	}
}

func backgroundJobExists(id string) bool {
	backgroundMu.Lock()
	defer backgroundMu.Unlock()
	_, ok := backgroundJobs[id]
	return ok
}

func TestBackgroundStartPrunesReadJobs(t *testing.T) {
	oldID, _, err := Start(context.Background(), Options{Command: "echo drained"})
	if err != nil {
		t.Fatal(err)
	}
	waitBackground(t, func() bool {
		backgroundMu.Lock()
		job := backgroundJobs[oldID]
		backgroundMu.Unlock()
		return job != nil && !job.isRunning()
	})
	// 已退出但未被读取的任务保留，避免读取时拿到 unknown job。
	nextID, _, err := Start(context.Background(), Options{Command: "echo next"})
	if err != nil {
		t.Fatal(err)
	}
	defer Kill(nextID)
	if !backgroundJobExists(oldID) {
		t.Fatal("未读取的已退出任务应保留")
	}
	if text, running, exitCode, err := Output(oldID); err != nil || running || exitCode != 0 || !strings.Contains(text, "drained") {
		t.Fatalf("text=%q running=%v exitCode=%d err=%v", text, running, exitCode, err)
	}
	// 输出读完后，下一次 Start 清理旧任务。
	thirdID, _, err := Start(context.Background(), Options{Command: "echo third"})
	if err != nil {
		t.Fatal(err)
	}
	defer Kill(thirdID)
	if backgroundJobExists(oldID) {
		t.Fatal("已读完输出的任务应在 Start 时清理")
	}
}
