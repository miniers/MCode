package remote

import (
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func newTestGuard(busy *atomic.Bool, launches, releases *atomic.Int32, pids chan<- int) *SleepGuard {
	g := NewSleepGuard(busy.Load, time.Hour)
	g.goos = "darwin"
	g.launch = func(pid int) (func(), error) {
		launches.Add(1)
		if pids != nil {
			pids <- pid
		}
		return func() { releases.Add(1) }, nil
	}
	return g
}

func TestSleepGuard_HoldsWhileBusyAndReleasesWhenIdle(t *testing.T) {
	var busy atomic.Bool
	var launches, releases atomic.Int32
	pids := make(chan int, 4)
	g := newTestGuard(&busy, &launches, &releases, pids)

	g.refresh()
	if launches.Load() != 0 {
		t.Fatal("空闲时不应启动 caffeinate")
	}

	busy.Store(true)
	g.refresh()
	g.refresh() // 已持有时不重复启动
	if launches.Load() != 1 {
		t.Fatalf("期望启动 1 次，实际 %d", launches.Load())
	}
	if pid := <-pids; pid != os.Getpid() {
		t.Errorf("应绑定当前进程 pid，实际 %d", pid)
	}

	busy.Store(false)
	g.refresh()
	if releases.Load() != 1 {
		t.Fatalf("空闲后应释放，实际释放 %d 次", releases.Load())
	}

	busy.Store(true)
	g.refresh()
	if launches.Load() != 2 {
		t.Fatalf("再次忙碌应重新启动，实际 %d", launches.Load())
	}
}

func TestSleepGuard_StopReleases(t *testing.T) {
	var busy atomic.Bool
	busy.Store(true)
	var launches, releases atomic.Int32
	g := newTestGuard(&busy, &launches, &releases, nil)

	g.Start()
	deadline := time.Now().Add(2 * time.Second)
	for launches.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if launches.Load() != 1 {
		t.Fatalf("Start 后应立即启动，实际 %d", launches.Load())
	}
	g.Stop()
	if releases.Load() != 1 {
		t.Fatalf("Stop 应释放断言，实际 %d", releases.Load())
	}
	g.Stop() // 幂等
	if releases.Load() != 1 {
		t.Fatalf("重复 Stop 不应重复释放，实际 %d", releases.Load())
	}
}

func TestSleepGuard_NonDarwinNoop(t *testing.T) {
	var busy atomic.Bool
	busy.Store(true)
	var launches, releases atomic.Int32
	g := newTestGuard(&busy, &launches, &releases, nil)
	g.goos = "linux"
	g.Start()
	g.Stop()
	if launches.Load() != 0 {
		t.Fatalf("非 darwin 不应启动，实际 %d", launches.Load())
	}
}

func TestSleepGuard_WindowsUsesLaunch(t *testing.T) {
	var busy atomic.Bool
	busy.Store(true)
	var launches, releases atomic.Int32
	g := newTestGuard(&busy, &launches, &releases, nil)
	g.goos = "windows"

	g.Start()
	deadline := time.Now().Add(2 * time.Second)
	for launches.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if launches.Load() != 1 {
		t.Fatalf("windows 上 Start 后应立即启动，实际 %d", launches.Load())
	}
	g.Stop()
	if releases.Load() != 1 {
		t.Fatalf("windows 上 Stop 应释放断言，实际 %d", releases.Load())
	}
}

func TestSleepGuard_DefaultLaunchByGOOS(t *testing.T) {
	if defaultLaunch("darwin") == nil || defaultLaunch("windows") == nil {
		t.Fatal("darwin 与 windows 都应有默认 launch 实现")
	}
	if _, err := defaultLaunch("linux")(os.Getpid()); err == nil {
		t.Fatal("不支持的平台应返回错误而不是 nil 实现")
	}
}

func TestSleepGuard_LaunchFailureRetriesNextRefresh(t *testing.T) {
	var busy atomic.Bool
	busy.Store(true)
	var attempts atomic.Int32
	g := NewSleepGuard(busy.Load, time.Hour)
	g.goos = "darwin"
	g.launch = func(int) (func(), error) {
		if attempts.Add(1) == 1 {
			return nil, errors.New("boom")
		}
		return func() {}, nil
	}
	g.refresh()
	g.refresh()
	g.refresh()
	if attempts.Load() != 2 {
		t.Fatalf("失败后应在下次刷新重试且成功后不再启动，实际尝试 %d 次", attempts.Load())
	}
}
