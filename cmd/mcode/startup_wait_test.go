package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// 结果先到且没有整理信号：不显示加载窗口，done 只调用一次。
func TestWaitOpenWithLoadingFastResult(t *testing.T) {
	result := make(chan openResult, 1)
	result <- openResult{}

	var showCalls, doneCalls atomic.Int32
	doneCh := make(chan openResult, 1)
	waitOpenWithLoading(result, make(chan struct{}), func() {
		showCalls.Add(1)
	}, func(res openResult) {
		doneCalls.Add(1)
		doneCh <- res
	})

	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("done 没有被调用")
	}
	if got := showCalls.Load(); got != 0 {
		t.Fatalf("没有整理信号时不应显示加载窗口，show 调用次数 = %d", got)
	}
	if got := doneCalls.Load(); got != 1 {
		t.Fatalf("done 应只调用一次，实际 %d 次", got)
	}
}

// 整理信号先到：先 show 再 done，各一次。
func TestWaitOpenWithLoadingSlowResult(t *testing.T) {
	result := make(chan openResult, 1)
	loading := make(chan struct{})
	events := make(chan string, 4)
	waitOpenWithLoading(result, loading, func() {
		events <- "show"
	}, func(openResult) {
		events <- "done"
	})
	close(loading)

	select {
	case got := <-events:
		if got != "show" {
			t.Fatalf("第一次回调应是 show，实际 %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("整理信号到达后仍未调用 show")
	}

	result <- openResult{}
	select {
	case got := <-events:
		if got != "done" {
			t.Fatalf("show 之后应调用 done，实际 %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("结果到达后仍未调用 done")
	}

	// 两个回调都只发生一次。
	select {
	case got := <-events:
		t.Fatalf("show 与 done 应各只调用一次，多出 %q", got)
	case <-time.After(50 * time.Millisecond):
	}
}

// 普通启动即使较慢，也不显示加载窗口。
func TestWaitOpenWithLoadingSlowWithoutCompact(t *testing.T) {
	result := make(chan openResult, 1)
	loading := make(chan struct{})
	var showCalls, doneCalls atomic.Int32
	doneCh := make(chan struct{}, 1)
	waitOpenWithLoading(result, loading, func() {
		showCalls.Add(1)
	}, func(openResult) {
		doneCalls.Add(1)
		doneCh <- struct{}{}
	})

	time.Sleep(50 * time.Millisecond)
	result <- openResult{}
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("done 没有被调用")
	}
	if got := showCalls.Load(); got != 0 {
		t.Fatalf("没有整理信号时不应显示加载窗口，show 调用次数 = %d", got)
	}
	if got := doneCalls.Load(); got != 1 {
		t.Fatalf("done 应只调用一次，实际 %d 次", got)
	}
}
