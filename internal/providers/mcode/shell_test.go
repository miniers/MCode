package mcode

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"runtime"
	"testing"
	"time"

	"mcode/internal/agent/llm"
	"mcode/internal/agent/session"
	"mcode/internal/agent/shell"
	"mcode/internal/harness"
)

func TestRunBackgroundCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("需要 Unix shell")
	}
	for _, tc := range []struct {
		name                                     string
		transient, cancel, panicRun, wantRunning bool
	}{
		{name: "普通轮次", wantRunning: true},
		{name: "一次性轮次", transient: true},
		{name: "取消", cancel: true},
		{name: "panic", panicRun: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := harnessFor(&fakeClient{responses: []*llm.Response{answer()}}, Options{})
			if tc.panicRun {
				h.opts.ResolveClient = func(string, string) (llm.Client, string, error) { panic("模拟错误") }
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			id, _, err := shell.Start(h.withProcessTracker(ctx, tc.name), shell.Options{Command: "sleep 30"})
			if err != nil {
				t.Fatal(err)
			}
			defer shell.Kill(id)
			if tc.cancel {
				cancel()
			}
			_ = h.Run(ctx, harness.Turn{ThreadID: tc.name, Transient: tc.transient}, nil)
			_, running, _, err := shell.OutputOwner(tc.name, id)
			if err != nil || running != tc.wantRunning {
				t.Fatalf("running=%v err=%v", running, err)
			}
			if !tc.wantRunning && len(h.ProcessRootPIDs(tc.name)) != 0 {
				t.Fatal("进程未注销")
			}
		})
	}
}

// TestNewDefaults 验证空 Options 下外壳的默认行为与改造前一致。
func TestNewDefaults(t *testing.T) {
	h := New(Options{})
	if got := h.Kind(); got != harness.Kind("mcode") {
		t.Fatalf("Kind() = %q, want mcode", got)
	}
	if got := h.DisplayName(); got != "mcode" {
		t.Fatalf("DisplayName() = %q, want mcode", got)
	}
	if got := h.ModelIDs(); got != nil {
		t.Fatalf("ModelIDs() = %v, want nil", got)
	}
	if err := h.RefreshModels(context.Background()); err != nil {
		t.Fatalf("RefreshModels() = %v, want nil", err)
	}
	if available, reason := h.Available(); !available || reason != "" {
		t.Fatalf("Available() = %v, %q, want true and empty reason", available, reason)
	}
	if !h.ConfirmsApprovalDelivery() {
		t.Fatal("ConfirmsApprovalDelivery() = false, want true")
	}
}

// TestOptionsOverrides 验证配置项原样生效。
func TestOptionsOverrides(t *testing.T) {
	models := []string{"a", "b"}
	var refreshed int
	confirms := false
	h := New(Options{
		Kind:        harness.Kind("mygo"),
		DisplayName: "MyGo",
		ModelIDs:    func() []string { return models },
		RefreshModels: func(context.Context) error {
			refreshed++
			return nil
		},
		Available:                func() (bool, string) { return false, "未登录" },
		ConfirmsApprovalDelivery: &confirms,
	})
	if got := h.Kind(); got != harness.Kind("mygo") {
		t.Fatalf("Kind() = %q, want mygo", got)
	}
	if got := h.DisplayName(); got != "MyGo" {
		t.Fatalf("DisplayName() = %q, want MyGo", got)
	}
	if got := h.ModelIDs(); !reflect.DeepEqual(got, models) {
		t.Fatalf("ModelIDs() = %v, want %v", got, models)
	}
	if err := h.RefreshModels(context.Background()); err != nil || refreshed != 1 {
		t.Fatalf("RefreshModels() = %v, refreshed %d", err, refreshed)
	}
	if available, reason := h.Available(); available || reason != "未登录" {
		t.Fatalf("Available() = %v, %q", available, reason)
	}
	if h.ConfirmsApprovalDelivery() {
		t.Fatal("ConfirmsApprovalDelivery() = true, want false")
	}
}

func TestPreserveSessionsFrom(t *testing.T) {
	old := New(Options{})
	session := old.gateFor("thread")
	run := &activeRun{}
	old.registerRun("session", run)
	old.trackProcess("thread", 123)
	h := New(Options{})
	h.PreserveSessionsFrom(old)
	if h.mu != old.mu || h.gates["thread"] != session || h.runs["session"] != run {
		t.Fatal("新实例未共享旧实例的锁、会话门禁和运行状态")
	}
	h.untrackProcess("thread", 123)
	if len(old.ProcessRootPIDs("thread")) != 0 {
		t.Fatal("新旧实例未共享进程记录")
	}
	h.gateFor("new-thread")
	if old.gates["new-thread"] != h.gates["new-thread"] {
		t.Fatal("新旧实例未共享会话门禁表")
	}
	h.unregisterRun("session")
	if old.runs["session"] != nil {
		t.Fatal("新旧实例未共享运行状态表")
	}
}

type otherHarness struct{ harness.Harness }

func TestPreserveSessionsFromIgnoresInvalidPrevious(t *testing.T) {
	h := New(Options{})
	session := h.gateFor("thread")
	run := &activeRun{}
	h.registerRun("session", run)
	h.trackProcess("thread", 123)
	mu := h.mu
	var nilHarness *Harness
	for _, previous := range []harness.Harness{nil, nilHarness, otherHarness{}, h} {
		h.PreserveSessionsFrom(previous)
		if h.mu != mu || h.gates["thread"] != session || h.runs["session"] != run || !h.pids["thread"][123] {
			t.Fatal("无效的旧实例改变了状态")
		}
	}
}

// TestRefreshModelsPropagatesError 验证刷新回调的错误会原样返回。
func TestRefreshModelsPropagatesError(t *testing.T) {
	want := errors.New("刷新失败")
	h := New(Options{RefreshModels: func(context.Context) error { return want }})
	if err := h.RefreshModels(context.Background()); !errors.Is(err, want) {
		t.Fatalf("RefreshModels() = %v, want %v", err, want)
	}
}

// TestSessionExists 验证会话文件缺失与存在时的判断。
func TestSessionExists(t *testing.T) {
	dir := t.TempDir()
	h := New(Options{StoreDir: dir})
	if h.SessionExists("missing") {
		t.Fatal("SessionExists(missing) = true, want false")
	}
	store := session.NewStore(dir)
	if err := store.Append("session-1", map[string]string{"type": "message"}); err != nil {
		t.Fatal(err)
	}
	if !h.SessionExists("session-1") {
		t.Fatal("SessionExists(session-1) = false, want true")
	}
}

// TestSessionExistsWithoutStore 验证未配置会话目录时不触发交接。
func TestSessionExistsWithoutStore(t *testing.T) {
	h := New(Options{})
	if !h.SessionExists("missing") {
		t.Fatal("SessionExists(missing) = false, want true when no store is configured")
	}
}

// TestProcessRootPIDsTrackRunningCommand 验证引擎会话能定位正在运行的 bash 子进程：
// 命令运行期间 ProcessRootPIDs 非空，对该进程组发 SIGKILL 后本轮正常结束且登记被注销。
func TestProcessRootPIDsTrackRunningCommand(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("需要 Unix 进程组")
	}
	client := &fakeClient{responses: []*llm.Response{
		toolCall("bash", "call-1", `{"command":"sleep 30"}`), answer(),
	}}
	h := harnessFor(client, Options{})
	turn := harness.Turn{ThreadID: "tracked-thread", Prompt: "go", Mode: harness.ModeFullAccess, WorkDir: t.TempDir()}
	done := make(chan error, 1)
	go func() { done <- h.Run(context.Background(), turn, nil) }()

	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pids := h.ProcessRootPIDs("tracked-thread"); len(pids) > 0 {
			pid = pids[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("命令运行期间没有登记 PID")
	}
	killGroup := func() error {
		return exec.Command("/bin/sh", "-c", fmt.Sprintf("kill -KILL -- -%d", pid)).Run()
	}
	defer func() { _ = killGroup() }()
	if err := killGroup(); err != nil {
		t.Fatalf("终止进程组 %d 失败：%v", pid, err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("命令被终止后本轮应正常结束，Run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("命令被终止后本轮没有结束")
	}
	if pids := h.ProcessRootPIDs("tracked-thread"); len(pids) != 0 {
		t.Fatalf("本轮结束后仍登记 PID：%v", pids)
	}
}

// TestEvictThreadReleasesGateAndPIDs 验证回收后进程登记被清空，「本会话允许」的记忆也会被清除。
func TestEvictThreadReleasesGateAndPIDs(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{
		toolCall("bash", "one", `{"command":"touch created"}`), answer(),
		toolCall("bash", "two", `{"command":"touch created"}`), answer(),
	}}
	approver := &allowingInteractor{}
	h := harnessFor(client, Options{})
	turn := harness.Turn{ThreadID: "evict-thread", WorkDir: t.TempDir(), Mode: harness.ModeSupervised, Approver: approver}
	if err := h.Run(context.Background(), turn, nil); err != nil {
		t.Fatal(err)
	}
	if approver.calls != 1 {
		t.Fatalf("approval requests = %d, want 1", approver.calls)
	}
	h.trackProcess("evict-thread", 424242)
	if pids := h.ProcessRootPIDs("evict-thread"); len(pids) != 1 || pids[0] != 424242 {
		t.Fatalf("登记的 PID = %v", pids)
	}
	h.EvictThread("evict-thread")
	if pids := h.ProcessRootPIDs("evict-thread"); len(pids) != 0 {
		t.Fatalf("回收后仍登记 PID：%v", pids)
	}
	if err := h.Run(context.Background(), turn, nil); err != nil {
		t.Fatal(err)
	}
	if approver.calls != 2 {
		t.Fatalf("回收后 approval requests = %d, want 2", approver.calls)
	}
}
