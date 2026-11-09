package app

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"mcode/internal/apisource"
)

func TestAPISourceModelCacheExpiryAndFailure(t *testing.T) {
	c := newAPISourceModelCache()
	now := time.Unix(100, 0)
	c.now = func() time.Time { return now }
	calls := 0
	c.fetch = func(context.Context, *http.Client, apisource.Source, string) ([]string, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("failed")
		}
		return []string{"model"}, nil
	}
	source := apisource.Source{Kind: "custom-test", BaseURL: "https://example.com"}
	if err := c.refresh(context.Background(), source, "key"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	if err := c.refresh(context.Background(), source, "key"); err == nil {
		t.Fatal("刷新应失败")
	}
	if got := c.models(source, "key"); !reflect.DeepEqual(got, []string{"model"}) {
		t.Fatalf("旧目录 = %v", got)
	}
	now = now.Add(31 * time.Second)
	if err := c.refresh(context.Background(), source, "key"); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("请求数 = %d", calls)
	}
}

func TestAPISourceModelCacheConcurrentAndIdentity(t *testing.T) {
	c := newAPISourceModelCache()
	started, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	calls := 0
	oldCalls := 0
	c.fetch = func(ctx context.Context, _ *http.Client, source apisource.Source, key string) ([]string, error) {
		mu.Lock()
		calls++
		if source.BaseURL == "old" && key == "old-key" {
			oldCalls++
		}
		n := calls
		mu.Unlock()
		if n == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []string{source.BaseURL + key}, nil
	}
	source := apisource.Source{Kind: "custom-test", BaseURL: "old"}
	c.models(source, "old-key")
	<-started
	var readers sync.WaitGroup
	for i := 0; i < 20; i++ {
		readers.Add(1)
		go func() { defer readers.Done(); c.models(source, "old-key") }()
	}
	readers.Wait()
	// 在旧请求尚未结束时，新地址和新密钥均不得读到旧目录。
	changed := source
	changed.BaseURL = "new"
	if got := c.models(changed, "old-key"); len(got) != 0 {
		t.Fatalf("新地址读到旧目录：%v", got)
	}
	if got := c.models(source, "new-key"); len(got) != 0 {
		t.Fatalf("新密钥读到旧目录：%v", got)
	}
	c.mu.Lock()
	done := c.entries[apiModelKey(source, "old-key")].flight.done
	c.mu.Unlock()
	close(release)
	<-done
	if err := c.refresh(context.Background(), changed, "old-key"); err != nil {
		t.Fatal(err)
	}
	if err := c.refresh(context.Background(), source, "new-key"); err != nil {
		t.Fatal(err)
	}
	if got := c.models(changed, "old-key"); !reflect.DeepEqual(got, []string{"newold-key"}) {
		t.Fatalf("新地址目录 = %v", got)
	}
	if got := c.models(source, "new-key"); !reflect.DeepEqual(got, []string{"oldnew-key"}) {
		t.Fatalf("新密钥目录 = %v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if oldCalls != 1 {
		t.Fatalf("同一身份并发请求未合并：%d", oldCalls)
	}
}
