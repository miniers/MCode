package modelcache

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiskCachePeek(t *testing.T) {
	for _, tc := range []struct {
		name, contents string
		fail           bool
		initial, final []string
	}{
		{"refresh", `["disk"]`, false, []string{"disk"}, []string{"fresh"}},
		{"failure", `["disk"]`, true, []string{"disk"}, []string{"disk"}},
		{"corrupt", `{`, false, nil, []string{"fresh"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "models.json")
			if err := os.WriteFile(path, []byte(tc.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			cache := Cache[string]{File: path}
			started, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			fetch := func(context.Context) ([]string, error) {
				calls.Add(1)
				close(started)
				<-release
				if tc.fail {
					return nil, errors.New("offline")
				}
				return []string{"fresh"}, nil
			}
			snap := cache.Peek(fetch)
			if !reflect.DeepEqual(snap.Models, tc.initial) || !snap.Probing || snap.Err != nil {
				t.Fatalf("首次快照错误: %+v", snap)
			}
			<-started
			close(release)
			deadline := time.Now().Add(2 * time.Second)
			for {
				snap = cache.Peek(fetch)
				if !snap.Probing {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("探测未完成")
				}
				time.Sleep(time.Millisecond)
			}
			if !reflect.DeepEqual(snap.Models, tc.final) || (snap.Err != nil) != tc.fail || calls.Load() != 1 {
				t.Fatalf("完成快照错误: %+v, calls=%d", snap, calls.Load())
			}
			if !tc.fail {
				data, err := os.ReadFile(path)
				var stored []string
				if err != nil || json.Unmarshal(data, &stored) != nil || !reflect.DeepEqual(stored, tc.final) {
					t.Fatalf("磁盘目录错误: %s, %v", data, err)
				}
			}
		})
	}
}

func TestRefreshWritesDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "models.json")
	cache := Cache[string]{File: path}
	if err := cache.Refresh(context.Background(), func(context.Context) ([]string, error) { return []string{"fresh"}, nil }); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != `["fresh"]` {
		t.Fatalf("磁盘目录错误: %s, %v", data, err)
	}
}

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
