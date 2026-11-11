package modelsdev

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

const sample = `{"anthropic":{"models":{
 "claude-haiku-4-5":{"id":"claude-haiku-4-5","family":"claude-haiku","release_date":"2025-10-15","limit":{"context":200000},"reasoning_options":[{"type":"budget_tokens","min":1024}]},
 "claude-haiku-5-5":{"id":"claude-haiku-5-5","family":"claude-haiku","release_date":"2026-10-07","limit":{"context":1000000,"output":1},"cost":{"input":1},"reasoning_options":[{"type":"toggle"},{"type":"effort","values":["max","none","low",null,"high","medium"]}],"experimental":{"modes":{"fast":{"cost":{}}}}},
 "bad":"not an object"
}},"other":{"models":{"x":{"id":"x"}}}}`

func newFrom(t *testing.T, data string) *Catalog {
	t.Helper()
	c := &Catalog{}
	if !c.load([]byte(data)) {
		t.Fatal("load failed")
	}
	return c
}

func TestStartCancelStopsBeforeWrite(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := start(ctx, dir, srv.Client(), srv.URL)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("请求未开始")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("刷新未退出")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("取消后目录不为空: %v, %v", entries, err)
	}
}

func TestStartWritesCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sample))
	}))
	defer srv.Close()
	dir := t.TempDir()
	select {
	case <-start(context.Background(), dir, srv.Client(), srv.URL):
	case <-time.After(5 * time.Second):
		t.Fatal("刷新未退出")
	}
	if !cacheFresh(dir) {
		t.Fatal("缓存未写入")
	}
	select {
	case <-start(context.Background(), dir, srv.Client(), srv.URL):
	default:
		t.Fatal("新鲜缓存不应启动刷新")
	}
}

func TestCostAndSummary(t *testing.T) {
	c := newFrom(t, `{"anthropic":{"models":{
 "claude-sonnet-4-5":{"id":"claude-sonnet-4-5","limit":{"context":200000},"cost":{"input":3,"output":15,"cache_read":0.3,"cache_write":3.75},"reasoning":true,"attachment":true,"knowledge":"2025-03-31","modalities":{"input":["text","image"],"output":["text"]}},
 "claude-opus-4-1":{"id":"claude-opus-4-1","limit":{"context":1500000}},
 "zero-cost":{"id":"zero-cost","cost":{"input":0,"output":0},"modalities":{"input":["text"]}}
}}}`)

	in, ok := c.Lookup("anthropic", "claude-sonnet-4-5")
	if !ok {
		t.Fatal("claude-sonnet-4-5 not found")
	}
	wantCost := Cost{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}
	if in.Cost == nil || *in.Cost != wantCost {
		t.Errorf("cost = %+v, want %+v", in.Cost, wantCost)
	}
	if !in.Reasoning || !in.Attachment || in.Knowledge != "2025-03-31" {
		t.Errorf("capabilities = %+v", in)
	}
	if want := []string{"text", "image"}; !reflect.DeepEqual(in.InputModalities, want) {
		t.Errorf("input modalities = %v, want %v", in.InputModalities, want)
	}
	if want := "200K 上下文 · 支持推理 · 支持图像输入 · 知识截至 2025-03-31"; in.Summary() != want {
		t.Errorf("summary = %q, want %q", in.Summary(), want)
	}

	// 超过 1M 时保留一位小数，整数时去掉 ".0"。
	big, _ := c.Lookup("anthropic", "claude-opus-4-1")
	if big.Cost != nil {
		t.Errorf("missing cost must stay nil, got %+v", big.Cost)
	}
	if want := "1.5M 上下文"; big.Summary() != want {
		t.Errorf("summary = %q, want %q", big.Summary(), want)
	}

	// 输入输出价格都为 0 时按未提供处理。
	free, _ := c.Lookup("anthropic", "zero-cost")
	if free.Cost != nil {
		t.Errorf("zero cost must be dropped, got %+v", free.Cost)
	}
	if free.Summary() != "" {
		t.Errorf("summary should be empty, got %q", free.Summary())
	}
}

func TestParseTolerantAndTrimmed(t *testing.T) {
	c := newFrom(t, sample)
	in, ok := c.Lookup("anthropic", "CLAUDE-haiku-5-5")
	if !ok {
		t.Fatal("haiku-5-5 not found")
	}
	if want := []string{"low", "medium", "high", "max"}; !reflect.DeepEqual(in.Efforts, want) {
		t.Errorf("efforts = %v, want %v", in.Efforts, want)
	}
	if !in.Fast || in.ContextLimit != 1000000 {
		t.Errorf("fast=%v context=%d", in.Fast, in.ContextLimit)
	}
	if _, ok := c.Lookup("other", "x"); ok {
		t.Error("unsupported provider must be dropped")
	}
	old, _ := c.Lookup("anthropic", "claude-haiku-4-5")
	if len(old.Efforts) != 0 {
		t.Errorf("budget_tokens-only model must have no efforts, got %v", old.Efforts)
	}
}

func TestLatestPicksNewestFamilyMember(t *testing.T) {
	c := newFrom(t, sample)
	in, ok := c.Latest("anthropic", "claude-haiku")
	if !ok || in.ID != "claude-haiku-5-5" {
		t.Fatalf("Latest = %+v, %v", in, ok)
	}
}

func TestWrappedProvidersShape(t *testing.T) {
	c := newFrom(t, `{"providers":`+sample+`}`)
	if _, ok := c.Lookup("anthropic", "claude-haiku-5-5"); !ok {
		t.Fatal("wrapped shape not parsed")
	}
}

func TestLoadEmptyKeepsExisting(t *testing.T) {
	c := newFrom(t, sample)
	if c.load([]byte(`{}`)) || c.load([]byte(`garbage`)) {
		t.Fatal("empty/invalid payload must not replace the catalog")
	}
	if _, ok := c.Lookup("anthropic", "claude-haiku-5-5"); !ok {
		t.Fatal("existing data lost")
	}
}

func TestEmbeddedSnapshotUsable(t *testing.T) {
	c := New()
	in, ok := c.Latest("anthropic", "claude-sonnet")
	if !ok || len(in.Efforts) == 0 || in.ContextLimit == 0 {
		t.Fatalf("embedded snapshot lacks sonnet data: %+v %v", in, ok)
	}
}

func TestRefreshWritesCacheAndUsesETag(t *testing.T) {
	var hits, notModified atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(sample))
	}))
	defer srv.Close()

	dir := t.TempDir()
	c := &Catalog{}
	if err := c.Refresh(context.Background(), srv.Client(), srv.URL, dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Lookup("anthropic", "claude-haiku-5-5"); !ok {
		t.Fatal("refresh did not update memory")
	}
	if !cacheFresh(dir) {
		t.Fatal("cache not written")
	}

	// 缓存可被新目录加载，且内容已裁剪（只保留白名单字段）。
	raw, _ := os.ReadFile(filepath.Join(dir, cacheName))
	if len(raw) == 0 || reflect.DeepEqual(raw, []byte(sample)) {
		t.Fatal("cache should be the trimmed form")
	}
	c2 := &Catalog{}
	c2.LoadCache(dir)
	if _, ok := c2.Lookup("anthropic", "claude-haiku-5-5"); !ok {
		t.Fatal("cache not loadable")
	}

	// 第二次刷新带 ETag，服务端 304，数据保持。
	if err := c.Refresh(context.Background(), srv.Client(), srv.URL, dir); err != nil {
		t.Fatal(err)
	}
	if notModified.Load() != 1 {
		t.Fatalf("expected one conditional hit, got %d of %d", notModified.Load(), hits.Load())
	}
	if _, ok := c.Lookup("anthropic", "claude-haiku-5-5"); !ok {
		t.Fatal("data lost after 304")
	}
}

func TestRefreshFailureKeepsData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := newFrom(t, sample)
	if err := c.Refresh(context.Background(), srv.Client(), srv.URL, t.TempDir()); err == nil {
		t.Fatal("expected error")
	}
	if _, ok := c.Lookup("anthropic", "claude-haiku-5-5"); !ok {
		t.Fatal("data lost after failed refresh")
	}
}
