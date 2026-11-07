package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestModelsURL(t *testing.T) {
	cases := []struct {
		base string
		want string
	}{
		{"https://api.meta.ai/v1", "https://api.meta.ai/v1/models"},
		{"https://api.deepseek.com", "https://api.deepseek.com/models"},
		{"https://api.z.ai/api/coding/paas/v4/", "https://api.z.ai/api/coding/paas/v4/models"},
		{"https://example.test/v1/chat/completions", "https://example.test/v1/models"},
	}
	for _, tc := range cases {
		if got := modelsURL(tc.base); got != tc.want {
			t.Errorf("modelsURL(%q) = %q, want %q", tc.base, got, tc.want)
		}
	}
}

func TestListModels(t *testing.T) {
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"b-model"},{"id":"a-model"},{"id":"a-model"},{"id":"  "}]}`)
	}))
	defer server.Close()

	models, err := ListModels(context.Background(), server.Client(), server.URL, "sk-test")
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if gotPath != "/models" {
		t.Errorf("path = %q, want /models", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("authorization = %q", gotAuth)
	}
	if len(models) != 2 || models[0] != "a-model" || models[1] != "b-model" {
		t.Errorf("models = %v, want [a-model b-model]", models)
	}
}

func TestListModelsErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid key"}}`)
	}))
	defer server.Close()

	_, err := ListModels(context.Background(), server.Client(), server.URL, "bad")
	if err == nil {
		t.Fatal("expected error on 401, got nil")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "invalid key") {
		t.Errorf("error = %v", err)
	}
}

func TestModelCacheHitAndExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := newModelCacheWithClock(time.Minute, func() time.Time { return now })

	var calls int32
	fetch := func(context.Context) ([]string, error) {
		atomic.AddInt32(&calls, 1)
		return []string{"m1", "m2"}, nil
	}

	for i := 0; i < 2; i++ {
		models, err := cache.Get(context.Background(), "https://api.test/v1", fetch)
		if err != nil {
			t.Fatalf("Get #%d failed: %v", i+1, err)
		}
		if len(models) != 2 {
			t.Fatalf("Get #%d models = %v", i+1, models)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("fetch calls = %d, want 1 (second should hit cache)", got)
	}

	// 命中缓存时返回副本，改动不影响缓存内容
	models, _ := cache.Get(context.Background(), "https://api.test/v1", fetch)
	models[0] = "mutated"
	again, _ := cache.Get(context.Background(), "https://api.test/v1", fetch)
	if again[0] != "m1" {
		t.Errorf("cache entry mutated: %v", again)
	}

	// 过期后重新拉取
	now = now.Add(2 * time.Minute)
	if _, err := cache.Get(context.Background(), "https://api.test/v1", fetch); err != nil {
		t.Fatalf("Get after expiry failed: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("fetch calls = %d, want 2 after expiry", got)
	}
}

func TestModelCacheFetchErrorNotCached(t *testing.T) {
	cache := NewModelCache(0)
	var calls int32
	fetch := func(context.Context) ([]string, error) {
		atomic.AddInt32(&calls, 1)
		return nil, fmt.Errorf("boom")
	}

	for i := 0; i < 2; i++ {
		if _, err := cache.Get(context.Background(), "k", fetch); err == nil {
			t.Fatal("expected fetch error")
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("fetch calls = %d, want 2 (errors must not be cached)", got)
	}

	if _, err := cache.Get(context.Background(), "k", nil); err == nil {
		t.Error("expected error for nil fetch")
	}
}

func TestModelCacheConcurrent(t *testing.T) {
	cache := NewModelCache(time.Minute)
	fetch := func(context.Context) ([]string, error) { return []string{"m"}, nil }

	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				if _, err := cache.Get(context.Background(), "k", fetch); err != nil {
					t.Errorf("Get failed: %v", err)
					return
				}
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
