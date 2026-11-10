package claude

import (
	"strings"
	"sync"
	"testing"
)

func TestTailBufferConcurrentReadWrite(t *testing.T) {
	b := &tailBuffer{limit: 64}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				_, _ = b.Write([]byte("x"))
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				if got := b.String(); len(got) > 64 || strings.Trim(got, "x") != "" {
					t.Errorf("缓冲区内容异常: %q", got)
					return
				}
			}
		}()
	}
	wg.Wait()
	if got := b.String(); got != strings.Repeat("x", 64) {
		t.Errorf("缓冲区尾部异常: %q", got)
	}
}
