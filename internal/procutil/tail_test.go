package procutil

import (
	"strings"
	"sync"
	"testing"
)

func TestTailBuffer(t *testing.T) {
	b := NewTailBuffer(4)
	_, _ = b.Write([]byte("abc"))
	_, _ = b.Write([]byte("def"))
	if got := b.String(); got != "cdef" {
		t.Fatalf("尾部 = %q", got)
	}
	_, _ = b.Write([]byte("123456"))
	if got := b.String(); got != "3456" {
		t.Fatalf("尾部 = %q", got)
	}
}

func TestTailBufferConcurrentReadWrite(t *testing.T) {
	b := NewTailBuffer(64)
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
