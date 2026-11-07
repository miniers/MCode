package projctx

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatcher_DebounceAndClose(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	var count atomic.Int32
	w, err := s.Watch(func() {
		count.Add(1)
	})
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}
	defer func() {
		_ = w.Close()
	}()

	w.mu.Lock()
	w.debounce = 300 * time.Millisecond
	w.mu.Unlock()

	// 在 100ms 内连续写 docs 文件 5 次
	for i := 0; i < 5; i++ {
		_, err := s.Write("test.md", fmt.Sprintf("content %d", i), "")
		if err != nil {
			t.Fatalf("write %d failed: %v", i, err)
		}
		time.Sleep(15 * time.Millisecond)
	}

	// 等待约 1 秒后断言 onChange 恰好被调用 1 次
	deadline := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := count.Load(); got != 1 {
		t.Fatalf("expected onChange count 1 after debouncing 5 writes, got %d", got)
	}

	// 再写一次，断言计数变为 2
	_, err = s.Write("test.md", "content single", "")
	if err != nil {
		t.Fatalf("single write failed: %v", err)
	}

	deadline = time.Now().Add(1200 * time.Millisecond)
	fired := false
	for time.Now().Before(deadline) {
		if count.Load() == 2 {
			fired = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !fired {
		t.Fatalf("expected onChange count 2 after single write, got %d", count.Load())
	}

	time.Sleep(400 * time.Millisecond)
	if got := count.Load(); got != 2 {
		t.Fatalf("expected count to remain 2, got %d", got)
	}

	// Close 之后再写，计数不再增加
	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	_, err = s.Write("test.md", "content after close", "")
	if err != nil {
		t.Fatalf("write after close failed: %v", err)
	}

	time.Sleep(500 * time.Millisecond)
	if got := count.Load(); got != 2 {
		t.Fatalf("expected count to stay 2 after Close(), got %d", got)
	}
}

func TestWatcher_MultipleClose(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Watch(func() {})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("first close failed: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second close failed: %v", err)
	}
}

// onChange 在锁外调用：回调里关闭监听器不能自死锁。
func TestWatcher_OnChangeCanClose(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	closed := make(chan struct{})
	var w *Watcher
	w, err = s.Watch(func() {
		_ = w.Close()
		close(closed)
	})
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}

	if _, err := s.Write("test.md", "content", ""); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("在 onChange 里调用 Close 死锁")
	}
}
