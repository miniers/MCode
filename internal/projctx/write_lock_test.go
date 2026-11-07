package projctx

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

// 跨实例并发 CAS：多个 goroutine 各自 projctx.New 同一目录，用相同 expectedHash
// 写同一文档，必须恰好一个成功，其余返回 ErrConflict（实例锁管不到跨实例写）。
func TestWriteRefCrossInstanceCAS(t *testing.T) {
	root := t.TempDir()
	base, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	created, err := base.WriteRef("overview", "seed", "")
	if err != nil {
		t.Fatalf("seed write failed: %v", err)
	}

	const n = 8
	results := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			svc, err := New(root)
			if err != nil {
				results[i] = err
				return
			}
			<-start
			_, err = svc.WriteRef("overview", fmt.Sprintf("value %d", i), created.Hash)
			results[i] = err
		}(i)
	}
	close(start)
	wg.Wait()

	succeeded := 0
	for _, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrConflict):
		default:
			t.Fatalf("unexpected write error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("exactly one write should succeed, got %d: %v", succeeded, results)
	}
	if doc, err := base.ReadRef("overview"); err != nil || doc.Hash != HashContent(doc.Content) {
		t.Fatalf("unexpected doc after concurrent writes: %+v %v", doc, err)
	}
}
