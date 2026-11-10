package procutil

import "sync"

// TailBuffer 保存最近写入的指定数量的字节。
type TailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func NewTailBuffer(limit int) *TailBuffer {
	return &TailBuffer{limit: limit}
}

func (b *TailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.limit {
		b.buf = b.buf[len(b.buf)-b.limit:]
	}
	return n, nil
}

func (b *TailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
