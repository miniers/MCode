package modelcache

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefreshKeepsLatestAndPreservesCacheOnFailure(t *testing.T) {
	var cache Cache[string]
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	oldFetch := func(context.Context) ([]string, error) {
		close(started)
		<-release
		return []string{"old"}, nil
	}
	cache.fetching = true
	go func() {
		_ = cache.fetch(context.Background(), 0, oldFetch)
		close(done)
	}()
	<-started
	if err := cache.Refresh(context.Background(), func(context.Context) ([]string, error) {
		return []string{"new"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	if got := cache.Get(oldFetch); !reflect.DeepEqual(got, []string{"new"}) {
		t.Fatalf("旧请求覆盖了新目录: %v", got)
	}
	if err := cache.Refresh(context.Background(), func(context.Context) ([]string, error) {
		return nil, errors.New("offline")
	}); err == nil {
		t.Fatal("刷新失败应返回错误")
	}
	if got := cache.Get(oldFetch); !reflect.DeepEqual(got, []string{"new"}) {
		t.Fatalf("刷新失败不应清空旧目录: %v", got)
	}
}

// Peek 要如实报告探测状态：首次在探测中，成功后清空 Err，之后失败保留旧目录，
// 且失败后的节流窗口内不再触发新的 fetch。
func TestPeekReportsProbeState(t *testing.T) {
	var cache Cache[string]
	started := make(chan struct{})
	release := make(chan struct{})
	fetch := func(context.Context) ([]string, error) {
		close(started)
		<-release
		return []string{"old"}, nil
	}
	var unexpectedCalls atomic.Int32
	noFetch := func(context.Context) ([]string, error) {
		unexpectedCalls.Add(1)
		return nil, errors.New("节流窗口内不应再次探测")
	}

	snap := cache.Peek(fetch)
	if !snap.Probing || snap.Models != nil || snap.Err != nil {
		t.Fatalf("首次 Peek 应在探测中且无目录: %+v", snap)
	}
	<-started
	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for {
		snap = cache.Peek(noFetch)
		if len(snap.Models) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("探测成功后目录未写入缓存: %+v", snap)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if snap.Probing || snap.Err != nil || !reflect.DeepEqual(snap.Models, []string{"old"}) {
		t.Fatalf("成功快照错误: %+v", snap)
	}

	if err := cache.Refresh(context.Background(), func(context.Context) ([]string, error) {
		return nil, errors.New("offline")
	}); err == nil {
		t.Fatal("刷新失败应返回错误")
	}
	snap = cache.Peek(noFetch)
	if snap.Probing || snap.Err == nil || !reflect.DeepEqual(snap.Models, []string{"old"}) {
		t.Fatalf("失败后应保留旧目录并报告原因: %+v", snap)
	}
	time.Sleep(20 * time.Millisecond)
	if got := unexpectedCalls.Load(); got != 0 {
		t.Fatalf("节流窗口内触发了 %d 次探测", got)
	}
}
