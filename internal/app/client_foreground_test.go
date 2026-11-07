package app

import (
	"strings"
	"sync"
	"testing"
)

// TestClientForegroundThreadRoundTrip 覆盖前台会话的读写与空串语义。
func TestClientForegroundThreadRoundTrip(t *testing.T) {
	client := &Client{}
	if got := client.ForegroundThread(); got != "" {
		t.Fatalf("初始状态应为空串，实际 %q", got)
	}
	client.SetForegroundThread("t1")
	if got := client.ForegroundThread(); got != "t1" {
		t.Fatalf("期望 t1，实际 %q", got)
	}
	// 界面只在值变化时上报，同一 ID 反复写也要保持幂等。
	client.SetForegroundThread("t1")
	if got := client.ForegroundThread(); got != "t1" {
		t.Fatalf("重复上报后仍应为 t1，实际 %q", got)
	}
	// 空串表示用户没有在看任何会话（窗口失焦或隐藏）。
	client.SetForegroundThread("")
	if got := client.ForegroundThread(); got != "" {
		t.Fatalf("失焦后应为空串，实际 %q", got)
	}
}

// TestClientForegroundThreadConcurrent 覆盖并发读写：界面可能在任意 goroutine 上报，
// 总管同时在读，-race 下不应报数据竞争。
func TestClientForegroundThreadConcurrent(t *testing.T) {
	client := &Client{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := "t" + strings.Repeat("x", n%4)
			for j := 0; j < 100; j++ {
				client.SetForegroundThread(id)
				_ = client.ForegroundThread()
			}
		}(i)
	}
	wg.Wait()

	// 所有写者用同一组取值，最终值只能是其中之一，不能是撕裂的结果。
	switch got := client.ForegroundThread(); got {
	case "", "t", "tx", "txx", "txxx":
	default:
		t.Fatalf("并发写后读到了非法值 %q", got)
	}
}
