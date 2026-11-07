package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// waitForCondition 在超时前轮询条件，避免固定长 sleep。
func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// TestMessageRetentionSchedulerRunsRepeatedly 覆盖启动后执行第一次、之后周期性再次执行。
func TestMessageRetentionSchedulerRunsRepeatedly(t *testing.T) {
	var calls atomic.Int32
	s := startMessageRetentionScheduler(
		func(context.Context) error {
			calls.Add(1)
			return nil
		},
		func() bool { return true },
		10*time.Millisecond,
		20*time.Millisecond,
	)
	if s == nil {
		t.Fatal("调度器未启动")
	}
	defer s.Stop()

	if !waitForCondition(t, time.Second, func() bool { return calls.Load() >= 1 }) {
		t.Fatalf("首次执行未发生，calls=%d", calls.Load())
	}
	if !waitForCondition(t, time.Second, func() bool { return calls.Load() >= 3 }) {
		t.Fatalf("未周期性执行，calls=%d", calls.Load())
	}
}

// TestMessageRetentionSchedulerEnabledToggle 覆盖开关关闭时跳过、重新打开后恢复。
func TestMessageRetentionSchedulerEnabledToggle(t *testing.T) {
	var enabled atomic.Bool
	var calls atomic.Int32
	s := startMessageRetentionScheduler(
		func(context.Context) error {
			calls.Add(1)
			return nil
		},
		func() bool { return enabled.Load() },
		5*time.Millisecond,
		20*time.Millisecond,
	)
	if s == nil {
		t.Fatal("调度器未启动")
	}
	defer s.Stop()

	// 关闭状态下跑过至少两轮仍不执行。
	quiet := time.Now().Add(70 * time.Millisecond)
	for time.Now().Before(quiet) {
		if n := calls.Load(); n != 0 {
			t.Fatalf("开关关闭时仍在执行，calls=%d", n)
		}
		time.Sleep(2 * time.Millisecond)
	}

	enabled.Store(true)
	if !waitForCondition(t, time.Second, func() bool { return calls.Load() >= 2 }) {
		t.Fatalf("开关重新打开后未恢复执行，calls=%d", calls.Load())
	}
}

// TestMessageRetentionSchedulerStopWaitsRunningRun 覆盖 Stop 后不再执行，且 Stop 等正在跑的 run 返回。
func TestMessageRetentionSchedulerStopWaitsRunningRun(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	s := startMessageRetentionScheduler(
		func(ctx context.Context) error {
			if calls.Add(1) == 1 {
				close(started)
			}
			// 阻塞到 ctx 取消，模拟正在写库的任务。
			<-ctx.Done()
			return ctx.Err()
		},
		func() bool { return true },
		5*time.Millisecond,
		20*time.Millisecond,
	)
	if s == nil {
		t.Fatal("调度器未启动")
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("首次执行未发生")
	}

	stopped := make(chan struct{})
	go func() {
		s.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop 未等待正在执行的 run 返回")
	}
	callsAfterStop := calls.Load()
	quiet := time.Now().Add(60 * time.Millisecond)
	for time.Now().Before(quiet) {
		if n := calls.Load(); n != callsAfterStop {
			t.Fatalf("Stop 后仍在执行，before=%d after=%d", callsAfterStop, n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestMessageRetentionSchedulerStopIdempotent 覆盖 nil 调用与重复调用都安全。
func TestMessageRetentionSchedulerStopIdempotent(t *testing.T) {
	var nilScheduler *messageRetentionScheduler
	nilScheduler.Stop()

	s := startMessageRetentionScheduler(
		func(context.Context) error { return nil },
		func() bool { return false },
		5*time.Millisecond,
		20*time.Millisecond,
	)
	if s == nil {
		t.Fatal("调度器未启动")
	}
	s.Stop()
	s.Stop()
}

// TestMessageRetentionSchedulerKeepsGoingAfterError 覆盖 run 返回错误后循环继续。
func TestMessageRetentionSchedulerKeepsGoingAfterError(t *testing.T) {
	var calls atomic.Int32
	s := startMessageRetentionScheduler(
		func(context.Context) error {
			calls.Add(1)
			return errors.New("模拟失败")
		},
		func() bool { return true },
		5*time.Millisecond,
		20*time.Millisecond,
	)
	if s == nil {
		t.Fatal("调度器未启动")
	}
	defer s.Stop()

	if !waitForCondition(t, time.Second, func() bool { return calls.Load() >= 3 }) {
		t.Fatalf("出错后未继续执行，calls=%d", calls.Load())
	}
}

// TestMessageRetentionSchedulerNilRun 覆盖 run 为 nil 时不启动。
func TestMessageRetentionSchedulerNilRun(t *testing.T) {
	if s := startMessageRetentionScheduler(nil, nil, time.Millisecond, time.Millisecond); s != nil {
		s.Stop()
		t.Fatal("run 为 nil 时不应启动调度器")
	}
}
