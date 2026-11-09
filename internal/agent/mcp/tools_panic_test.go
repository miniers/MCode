package mcp

import (
	"context"
	"strings"
	"testing"
	"time"
)

type panicDiscoveryContext struct{ context.Context }

func (panicDiscoveryContext) Deadline() (time.Time, bool) { panic("发现失败") }

func TestDiscoveryPanic(t *testing.T) {
	ctx := panicDiscoveryContext{context.Background()}
	server := Server{Name: "panic", URL: "http://example.invalid"}
	loaded := discoverSafely(ctx, server)
	if loaded.err == nil || !strings.Contains(loaded.err.Error(), "发现失败") {
		t.Fatalf("发现错误=%v", loaded.err)
	}
	done := make(chan struct{})
	go func() { Tools(ctx, []Server{server}); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("发现工作线程未退出")
	}
}
