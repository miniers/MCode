package procutil

import (
	"io"
	"sync"
)

// LineWriter 串行化对子进程标准输入的写入。
type LineWriter struct {
	mu     sync.Mutex
	w      io.WriteCloser
	closed bool
}

func NewLineWriter(w io.WriteCloser) *LineWriter {
	return &LineWriter{w: w}
}

func (l *LineWriter) WriteLine(line []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return io.ErrClosedPipe
	}
	_, err := l.w.Write(line)
	return err
}

func (l *LineWriter) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	return l.w.Close()
}
