package automation

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/kanban"
	"mcode/internal/store"
)

// missingThreadDriver 在 fakeDriver 之上实现 SessionMessenger：投递到 missing 线程时返回预设错误，
// 其余线程照常排队。用于模拟用户把 dedicated 专属线程删掉后的投递结果。
type missingThreadDriver struct {
	*fakeDriver
	missing string
	err     error
	mu      sync.Mutex
	sent    []string
}

var _ kanban.SessionMessenger = (*missingThreadDriver)(nil)

func (d *missingThreadDriver) SendFollowUp(ctx context.Context, threadID, text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sent = append(d.sent, threadID)
	if threadID == d.missing {
		return d.err
	}
	return nil
}

func (d *missingThreadDriver) sentTargets() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.sent...)
}

// setupMessengerService 与 setupTestService 相同，但注入支持追问的驱动。
func setupMessengerService(t *testing.T, clock *fakeClock, driver *missingThreadDriver) (*Service, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	svc := New(Options{
		Store:    s,
		Driver:   driver,
		Projects: &fakeProjects{root: t.TempDir()},
		Now:      clock.Now,
		Interval: 10 * time.Millisecond,
		DataDir:  t.TempDir(),
		AcquireLock: func(string) (LockHandle, error) {
			return &fakeLock{}, nil
		},
	})
	return svc, s
}

// TestDedicatedRunRebuildsDeletedThread 验证专属线程被删除后本轮改为新建会话：
// 新线程写回 Spec、运行不记失败、旧线程的进度基线被清除。
func TestDedicatedRunRebuildsDeletedThread(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		err  error
	}{
		{"store sentinel", fmt.Errorf("send host event: %w", store.ErrNotFound)},
		{"remote not found text", fmt.Errorf("thread %s not found", "thread-gone")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
			driver := &missingThreadDriver{fakeDriver: newFakeDriver(), missing: "thread-gone", err: tc.err}
			svc, s := setupMessengerService(t, clock, driver)
			defer s.Close()

			def, err := svc.Create(ctx, CreateInput{
				ProjectID: "proj-1", Name: "rebuild", Prompt: "run",
				Spec: Spec{
					Schedule:          Schedule{Type: "interval", Seconds: 60},
					RunMode:           RunModeDedicated,
					DedicatedThreadID: "thread-gone",
				},
				Enabled: true,
			})
			if err != nil {
				t.Fatalf("create: %v", err)
			}

			run, err := svc.RunNow(ctx, def.ID)
			if err != nil {
				t.Fatalf("run now: %v", err)
			}
			if driver.startCount() != 1 {
				t.Fatalf("专属线程失效后应新建 1 个会话，实际 %d", driver.startCount())
			}
			if run.Status != RunRunning {
				t.Fatalf("本轮应保持 running，实际 %s（error=%v）", run.Status, run.Error)
			}
			if run.ThreadID == nil || *run.ThreadID == "thread-gone" {
				t.Fatalf("运行应绑定新建的线程，实际 %v", run.ThreadID)
			}
			if targets := driver.sentTargets(); len(targets) != 1 || targets[0] != "thread-gone" {
				t.Errorf("应只尝试投递旧线程一次，实际 %v", targets)
			}

			updated, err := svc.Get(ctx, def.ID)
			if err != nil {
				t.Fatalf("get automation: %v", err)
			}
			if updated.Spec.DedicatedThreadID != *run.ThreadID {
				t.Errorf("专属线程应更新为新线程 %s，实际 %q", *run.ThreadID, updated.Spec.DedicatedThreadID)
			}
			if updated.Spec.State.ConsecutiveFailures != 0 {
				t.Errorf("重建不应记失败，实际连续失败 %d", updated.Spec.State.ConsecutiveFailures)
			}
			if _, ok := svc.runBaselineOf(run.ID); ok {
				t.Error("旧线程的进度基线应被清除")
			}
		})
	}
}

// TestDedicatedDeliveryErrorStillFails 验证除「线程不存在」以外的投递错误仍按原样返回并记失败，
// 不会误触发重建（包括只含 not found 文案、与线程无关的错误）。
func TestDedicatedDeliveryErrorStillFails(t *testing.T) {
	ctx := context.Background()
	for _, msg := range []string{"deliver failed: queue full", "agent binary not found in PATH"} {
		t.Run(msg, func(t *testing.T) {
			clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
			driver := &missingThreadDriver{
				fakeDriver: newFakeDriver(), missing: "thread-gone", err: errors.New(msg),
			}
			svc, s := setupMessengerService(t, clock, driver)
			defer s.Close()

			def, err := svc.Create(ctx, CreateInput{
				ProjectID: "proj-1", Name: "deliver-fails", Prompt: "run",
				Spec: Spec{
					Schedule:          Schedule{Type: "interval", Seconds: 60},
					RunMode:           RunModeDedicated,
					DedicatedThreadID: "thread-gone",
				},
				Enabled: true,
			})
			if err != nil {
				t.Fatalf("create: %v", err)
			}

			run, err := svc.RunNow(ctx, def.ID)
			if err != nil {
				t.Fatalf("run now: %v", err)
			}
			if driver.startCount() != 0 {
				t.Errorf("投递出错时不应新建会话，实际 %d", driver.startCount())
			}
			if run.Status != RunFailed {
				t.Fatalf("运行应记失败，实际 %s", run.Status)
			}
			if run.Error == nil || !strings.Contains(*run.Error, msg) {
				t.Errorf("失败原因应包含投递错误 %q，实际 %v", msg, run.Error)
			}
			updated, err := svc.Get(ctx, def.ID)
			if err != nil {
				t.Fatalf("get automation: %v", err)
			}
			if updated.Spec.DedicatedThreadID != "thread-gone" {
				t.Errorf("投递出错不应改动专属线程，实际 %q", updated.Spec.DedicatedThreadID)
			}
			if updated.Spec.State.ConsecutiveFailures != 1 {
				t.Errorf("应记 1 次连续失败，实际 %d", updated.Spec.State.ConsecutiveFailures)
			}
		})
	}
}
