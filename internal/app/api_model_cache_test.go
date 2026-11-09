package app

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/harness"
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

func TestAPISourceModelCachePanicUnblocksWaiters(t *testing.T) {
	c := newAPISourceModelCache()
	started, release := make(chan struct{}), make(chan struct{})
	c.fetch = func(context.Context, *http.Client, apisource.Source, string) ([]string, error) {
		close(started)
		<-release
		panic("fetch failed")
	}
	source := apisource.Source{Kind: "panic"}
	c.models(source, "key")
	<-started
	c.mu.Lock()
	flight := c.entries[apiModelKey(source, "key")].flight
	c.mu.Unlock()
	result := make(chan error, 1)
	go func() { <-flight.done; result <- flight.err }()
	close(release)
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "panic") {
			t.Fatalf("刷新错误 = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("等待模型目录超时")
	}
}

type panicSecretStore struct{ config.SecretStore }

func (panicSecretStore) Get(string) (string, error) { panic("secret store failed") }

func TestSourceAvailabilityRecoversPanic(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	setMcodeAgentRuntime("", nil)
	t.Cleanup(func() { setMcodeAgentRuntime("", nil) })
	cfg := config.Defaults()
	cfg.Providers.Presets = map[string]config.PresetProviderConfig{"deepseek": {Enabled: true}}
	a := &sourceAvailability{}
	a.get(harness.Kind("deepseek"), cfg, panicSecretStore{})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		checked, running, available, reason := !a.checked.IsZero(), a.running, a.available, a.reason
		a.mu.Unlock()
		if checked {
			if running || available || reason == "" {
				t.Fatalf("检查状态：running=%v available=%v reason=%q", running, available, reason)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("后台检查未结束")
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

func TestSourceAvailabilityFirstReadAndBackgroundCheck(t *testing.T) {
	dir := t.TempDir()
	secrets := config.NewMemorySecretStore()
	setMcodeAgentRuntime(dir, secrets)
	t.Cleanup(func() { setMcodeAgentRuntime("", nil) })

	cfg := config.Defaults()
	cfg.Providers.Presets = map[string]config.PresetProviderConfig{
		"deepseek": {Enabled: true},
	}
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Set("deepseek.apiKey", "sk-test"); err != nil {
		t.Fatal(err)
	}

	waitChecked := func(t *testing.T, a *sourceAvailability) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			a.mu.Lock()
			checked := !a.checked.IsZero()
			a.mu.Unlock()
			if checked {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("后台检查未在 2 秒内完成")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	disabled := &sourceAvailability{}
	disabled.get(harness.Kind("custom-missing"), cfg, secrets)
	waitChecked(t, disabled)
	if ok, reason := disabled.get(harness.Kind("custom-missing"), cfg, secrets); ok || reason == "" {
		t.Fatalf("未启用来源 = %v, %q，期望不可用且有原因", ok, reason)
	}

	enabled := &sourceAvailability{}
	if ok, reason := enabled.get(harness.Kind("deepseek"), cfg, secrets); !ok || reason != "" {
		t.Fatalf("首次读取 = %v, %q，期望按可用处理", ok, reason)
	}
	waitChecked(t, enabled)
	if ok, reason := enabled.get(harness.Kind("deepseek"), cfg, secrets); !ok || reason != "" {
		t.Fatalf("已启用来源 = %v, %q，期望可用", ok, reason)
	}
}
