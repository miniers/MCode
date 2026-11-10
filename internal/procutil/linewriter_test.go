package procutil

import (
	"bytes"
	"io"
	"testing"
)

type testWriteCloser struct{ bytes.Buffer }

func (w *testWriteCloser) Close() error { return nil }

func TestLineWriter(t *testing.T) {
	sink := &testWriteCloser{}
	w := NewLineWriter(sink)
	for _, line := range []string{"first\n", "second\n"} {
		if err := w.WriteLine([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if got := sink.String(); got != "first\nsecond\n" {
		t.Fatalf("输出 = %q", got)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteLine([]byte("third\n")); err != io.ErrClosedPipe {
		t.Fatalf("关闭后写入: %v", err)
	}
}
