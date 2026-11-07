package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/harness"
)

// waitModelIDs 等待后台目录拉取落地（缓存到期时间被写入）后返回当前目录。
// 首次读取不再同步等端点，所以断言最终目录前要先等后台拉取完成。
func waitModelIDs(t *testing.T, h *Harness) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.modelMu.Lock()
		settled := !h.modelExpiry.IsZero() && !h.modelRefreshing
		ids := append([]string(nil), h.modelIDs...)
		h.modelMu.Unlock()
		if settled {
			return ids
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待后台模型目录超时，当前目录: %v", ids)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitAvailability 等待后台密钥探测落地后返回 Available 的结论。
func waitAvailability(t *testing.T, h *Harness) (bool, string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.availMu.Lock()
		settled := h.availResult != nil && !h.availProbing
		h.availMu.Unlock()
		if settled {
			return h.Available()
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待后台密钥探测超时")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// blockingSecretStore 让读密钥一直阻塞，用来验证 Available 不等钥匙串。
type blockingSecretStore struct {
	release chan struct{}
}

func (b *blockingSecretStore) Get(string) (string, error) {
	<-b.release
	return "", config.ErrSecretNotFound
}

func (b *blockingSecretStore) Set(string, string) error { return nil }

func (b *blockingSecretStore) Delete(string) error { return nil }

func TestOpenAI_Available(t *testing.T) {
	// 1. 无 key
	h1 := New(Options{})
	ok, reason := h1.Available()
	if ok || reason == "" {
		t.Errorf("expected Available=false with reason, got ok=%v, reason=%q", ok, reason)
	}

	// 2. 有 APIKey
	h2 := New(Options{
		APIKey: "sk-test-key",
	})
	ok, reason = h2.Available()
	if !ok || reason != "" {
		t.Errorf("expected Available=true, got ok=%v, reason=%q", ok, reason)
	}

	// 3. Secrets 中存储 key：首次调用不等钥匙串读取，后台探测后确认可用
	sec := config.NewMemorySecretStore()
	_ = sec.Set("openai_account", "sk-secret-key")
	h3 := New(Options{
		APIKeyAccount: "openai_account",
		Secrets:       sec,
	})
	h3.Available()
	ok, reason = waitAvailability(t, h3)
	if !ok || reason != "" {
		t.Errorf("expected Available=true from secrets, got ok=%v, reason=%q", ok, reason)
	}

	// 4. 仅有 FallbackAPIKey
	h4 := New(Options{
		FallbackAPIKey: "sk-fallback-only",
	})
	ok, reason = h4.Available()
	if !ok || reason != "" {
		t.Errorf("expected Available=true from fallback, got ok=%v, reason=%q", ok, reason)
	}
}

func TestOpenAI_FallbackAPIKeyPriority(t *testing.T) {
	sec := config.NewMemorySecretStore()
	_ = sec.Set("test_account", "sk-secret-key")

	// 1. Secrets 有值时优先使用 Secrets，忽略 FallbackAPIKey
	h1 := New(Options{
		APIKeyAccount:  "test_account",
		Secrets:        sec,
		FallbackAPIKey: "sk-fallback-key",
	})
	key, err := h1.getAPIKey()
	if err != nil || key != "sk-secret-key" {
		t.Fatalf("expected secret key sk-secret-key, got key=%q, err=%v", key, err)
	}
	avail, _ := h1.Available()
	if !avail {
		t.Fatalf("expected Available=true when secret exists")
	}

	// 2. Secrets 无值时回退使用 FallbackAPIKey
	h2 := New(Options{
		APIKeyAccount:  "non_existent_account",
		Secrets:        sec,
		FallbackAPIKey: "sk-fallback-key",
	})
	key, err = h2.getAPIKey()
	if err != nil || key != "sk-fallback-key" {
		t.Fatalf("expected fallback key sk-fallback-key, got key=%q, err=%v", key, err)
	}
	avail, _ = h2.Available()
	if !avail {
		t.Fatalf("expected Available=true when fallback exists")
	}

	// 3. 显式 APIKey 仍优先于 Secrets 和 FallbackAPIKey
	h3 := New(Options{
		APIKey:         "sk-explicit-key",
		APIKeyAccount:  "test_account",
		Secrets:        sec,
		FallbackAPIKey: "sk-fallback-key",
	})
	key, err = h3.getAPIKey()
	if err != nil || key != "sk-explicit-key" {
		t.Fatalf("expected explicit key sk-explicit-key, got key=%q, err=%v", key, err)
	}

	// 4. 全部无值时返回错误
	h4 := New(Options{
		APIKeyAccount: "non_existent_account",
		Secrets:       sec,
	})
	_, err = h4.getAPIKey()
	if err == nil {
		t.Fatalf("expected error when no API key available")
	}
}

func TestOpenAI_AvailableProbesSecretInBackground(t *testing.T) {
	// 构造时已启动预热探测；读钥匙串阻塞时 Available 不能等它。
	store := &blockingSecretStore{release: make(chan struct{})}
	h := New(Options{APIKeyAccount: "missing_account", Secrets: store})

	// 首次调用不能等钥匙串：还没有缓存，先按可用处理。
	if ok, reason := h.Available(); !ok || reason != "" {
		t.Fatalf("首次调用应按可用处理: ok=%v reason=%q", ok, reason)
	}
	close(store.release)
	ok, reason := waitAvailability(t, h)
	if ok || !strings.Contains(reason, "not found") {
		t.Fatalf("探测后应报告账号缺失: ok=%v reason=%q", ok, reason)
	}
	// 探测结论在缓存期内直接复用。
	if ok, _ := h.Available(); ok {
		t.Fatal("缓存期内应复用探测结论")
	}
}

// TestOpenAI_AvailableKeepsStaleNegativeResult 覆盖 TTL 过期后不乐观返回可用：
// 旧结论为不可用时先复用旧结论，同时后台重探。
func TestOpenAI_AvailableKeepsStaleNegativeResult(t *testing.T) {
	sec := config.NewMemorySecretStore()
	h := New(Options{APIKeyAccount: "missing_account", Secrets: sec})
	if ok, reason := waitAvailability(t, h); ok || !strings.Contains(reason, "not found") {
		t.Fatalf("预热后应报告账号缺失: ok=%v reason=%q", ok, reason)
	}

	// 把缓存时间推到很久以前，模拟 TTL 过期。
	h.availMu.Lock()
	h.availResult.at = time.Now().Add(-2 * availabilityCacheTTL)
	h.availMu.Unlock()

	ok, reason := h.Available()
	if ok {
		t.Fatal("缓存过期后应复用旧的不可用结论，而不是乐观返回可用")
	}
	if !strings.Contains(reason, "not found") {
		t.Fatalf("过期后应返回旧 reason: %q", reason)
	}

	// 过期会触发后台重探，重探完成后结论仍是不可用。
	if ok, reason := waitAvailability(t, h); ok || reason == "" {
		t.Fatalf("重探后应报告账号缺失: ok=%v reason=%q", ok, reason)
	}
}

func TestOpenAI_DisplayNameAndModelIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"server-model"}]}`)
	}))
	defer server.Close()

	h1 := New(Options{
		DisplayName: "My Custom Provider",
		Model:       "gpt-4o-mini",
		BaseURL:     server.URL,
		HTTPClient:  server.Client(),
	})
	if got := h1.DisplayName(); got != "My Custom Provider" {
		t.Errorf("DisplayName() = %q, want 'My Custom Provider'", got)
	}
	if got := h1.ModelIDs(); len(got) == 0 || got[0] != "gpt-4o-mini" {
		t.Errorf("ModelIDs() = %v, want ['gpt-4o-mini' ...]", got)
	}
	if got := waitModelIDs(t, h1); len(got) != 2 || got[0] != "gpt-4o-mini" || got[1] != "server-model" {
		t.Errorf("ModelIDs() = %v, want ['gpt-4o-mini' 'server-model']", got)
	}

	h2 := New(Options{BaseURL: server.URL, HTTPClient: server.Client()})
	if got := h2.DisplayName(); got != "OpenAI" {
		t.Errorf("default DisplayName() = %q, want 'OpenAI'", got)
	}
	h2.ModelIDs()
	if got := waitModelIDs(t, h2); len(got) != 1 || got[0] != "server-model" {
		t.Errorf("empty model ModelIDs() = %v, want ['server-model']", got)
	}
}

// TestOpenAI_ModelIDsDoesNotWaitForNetwork 验证首次读取目录不等端点响应：
// 请求还在路上时立即返回配置的模型，后台拉取完成后由下一次读取补上。
func TestOpenAI_ModelIDsDoesNotWaitForNetwork(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-release
		fmt.Fprint(w, `{"data":[{"id":"late-model"}]}`)
	}))
	defer server.Close()

	h := New(Options{BaseURL: server.URL, HTTPClient: server.Client(), Model: "configured"})
	if got := h.ModelIDs(); !reflect.DeepEqual(got, []string{"configured"}) {
		t.Fatalf("首次读取应只返回配置的模型: %v", got)
	}
	<-started
	close(release)
	if got := waitModelIDs(t, h); !reflect.DeepEqual(got, []string{"configured", "late-model"}) {
		t.Fatalf("后台拉取未补齐目录: %v", got)
	}
}

func TestOpenAI_RefreshModelsBypassesCacheAndPreservesOnFailure(t *testing.T) {
	var version atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch version.Load() {
		case 0:
			fmt.Fprint(w, `{"data":[{"id":"old"}]}`)
		case 1:
			fmt.Fprint(w, `{"data":[{"id":"new"}]}`)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	h := New(Options{BaseURL: server.URL, HTTPClient: server.Client(), Model: "configured"})
	h.ModelIDs()
	if got := waitModelIDs(t, h); !reflect.DeepEqual(got, []string{"configured", "old"}) {
		t.Fatalf("初始目录错误: %v", got)
	}
	version.Store(1)
	if err := h.RefreshModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := h.ModelIDs(); !reflect.DeepEqual(got, []string{"configured", "new"}) {
		t.Fatalf("刷新未绕过缓存: %v", got)
	}
	version.Store(2)
	if err := h.RefreshModels(context.Background()); err == nil {
		t.Fatal("端点失败应返回错误")
	}
	if got := h.ModelIDs(); !reflect.DeepEqual(got, []string{"configured", "new"}) {
		t.Fatalf("端点失败不应清空旧目录: %v", got)
	}
}

func TestOpenAI_ModelIDsDoesNotStartAnotherFetchDuringRefresh(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		fmt.Fprint(w, `{"data":[{"id":"new"}]}`)
	}))
	defer server.Close()
	h := New(Options{BaseURL: server.URL, HTTPClient: server.Client()})
	done := make(chan error, 1)
	go func() { done <- h.RefreshModels(context.Background()) }()
	<-started
	if got := h.ModelIDs(); len(got) != 0 || calls.Load() != 1 {
		t.Errorf("刷新期间不应重复请求端点: models=%v calls=%d", got, calls.Load())
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := h.ModelIDs(); !reflect.DeepEqual(got, []string{"new"}) {
		t.Fatalf("刷新结果不正确: %v", got)
	}
}

func TestOpenAI_PreserveSessionsFrom(t *testing.T) {
	old := New(Options{Model: "old-model"})
	old.sessions["session"] = []chatMessage{{Role: "user", Content: "历史消息"}}
	updated := New(Options{Model: "new-model"})
	updated.PreserveSessionsFrom(old)
	if updated.mu != old.mu || len(updated.sessions["session"]) != 1 {
		t.Fatal("更新配置必须共享历史及其锁")
	}
	old.saveSessionHistory("session", append(old.sessions["session"], chatMessage{Role: "assistant", Content: "进行中的回复"}))
	if len(updated.sessions["session"]) != 2 || updated.opts.Model != "new-model" {
		t.Fatal("更新后的实例应保留配置，并接收进行中的历史更新")
	}
}

func TestOpenAI_ToolCallAndFollowUp(t *testing.T) {
	workDir := t.TempDir()
	docFile := filepath.Join(workDir, "doc.txt")
	_ = os.WriteFile(docFile, []byte("hello doc content"), 0644)

	var requestCount int32
	var capturedBodies [][]byte
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		capturedBodies = append(capturedBodies, body)
		mu.Unlock()

		count := atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		if count == 1 {
			// 第一轮：模型发起 read_file 工具调用
			chunk1 := `{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_01","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"doc.txt\"}"}}]},"finish_reason":null}]}`
			chunk2 := `{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":15,"completion_tokens":5,"total_tokens":20}}`
			fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", chunk1, chunk2)
			flusher.Flush()
			return
		}

		if count == 2 {
			// 第二轮：模型输出最终文本与 usage 汇总
			chunk1 := `{"id":"c2","choices":[{"index":0,"delta":{"content":"Doc read success"},"finish_reason":null}]}`
			chunk2 := `{"id":"c2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":25,"completion_tokens":10,"total_tokens":35,"prompt_tokens_details":{"cached_tokens":5}}}`
			fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", chunk1, chunk2)
			flusher.Flush()
			return
		}

		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	h := New(Options{
		BaseURL:    server.URL,
		APIKey:     "sk-test",
		Model:      "test-model",
		HTTPClient: server.Client(),
	})

	var (
		events   []harness.Event
		eventsMu sync.Mutex
	)
	emit := func(e harness.Event) {
		eventsMu.Lock()
		events = append(events, e)
		eventsMu.Unlock()
	}

	turn := harness.Turn{
		Prompt:  "Please read doc.txt",
		WorkDir: workDir,
		Mode:    harness.ModeFullAccess,
	}

	err := h.Run(context.Background(), turn, emit)
	if err != nil {
		t.Fatalf("h.Run failed: %v", err)
	}

	if atomic.LoadInt32(&requestCount) != 2 {
		t.Fatalf("expected 2 rounds of requests, got %d", atomic.LoadInt32(&requestCount))
	}

	// 验证第二轮请求体中带上了 tool 结果
	mu.Lock()
	secondReqBody := capturedBodies[1]
	mu.Unlock()

	var payload struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    any    `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(secondReqBody, &payload); err != nil {
		t.Fatalf("unmarshal second request body: %v", err)
	}

	var foundToolResult bool
	for _, m := range payload.Messages {
		if m.Role == "tool" && m.ToolCallID == "call_01" {
			if str, ok := m.Content.(string); ok && strings.Contains(str, "hello doc content") {
				foundToolResult = true
			}
		}
	}
	if !foundToolResult {
		t.Errorf("second request did not contain tool result, payload was: %s", string(secondReqBody))
	}

	// 验证事件序列与 usage 汇总
	eventsMu.Lock()
	defer eventsMu.Unlock()

	var (
		sawSession   bool
		sawToolStart bool
		sawToolEnd   bool
		sawText      bool
		sawUsage     bool
		sawDone      bool
	)
	for _, ev := range events {
		switch ev.Type {
		case harness.EventSession:
			sawSession = true
		case harness.EventToolStart:
			sawToolStart = true
			if ev.ToolID != "call_01" || ev.ToolName != "read_file" {
				t.Errorf("tool_start mismatch: %+v", ev)
			}
		case harness.EventToolEnd:
			sawToolEnd = true
			if ev.ToolID != "call_01" || !strings.Contains(ev.Output, "hello doc content") {
				t.Errorf("tool_end mismatch: %+v", ev)
			}
		case harness.EventText:
			sawText = true
			if ev.Text != "Doc read success" {
				t.Errorf("text mismatch: %q", ev.Text)
			}
		case harness.EventUsage:
			sawUsage = true
			if ev.Usage == nil || ev.Usage.InputTokens != 40 || ev.Usage.OutputTokens != 15 || ev.Usage.CachedTokens != 5 {
				t.Errorf("usage accumulation mismatch: %+v", ev.Usage)
			}
			// ContextTokens 取最后一次请求（prompt 25 + completion 10），不是多轮累加的 InputTokens。
			if ev.Usage != nil && ev.Usage.ContextTokens != 35 {
				t.Errorf("ContextTokens 应取最后一次请求的 25+10，得到 %+v", ev.Usage)
			}
		case harness.EventDone:
			sawDone = true
			if ev.StopReason != "stop" {
				t.Errorf("stop reason mismatch: %q", ev.StopReason)
			}
		}
	}

	if !sawSession || !sawToolStart || !sawToolEnd || !sawText || !sawUsage || !sawDone {
		t.Fatalf("missing expected events in sequence: session=%v, toolStart=%v, toolEnd=%v, text=%v, usage=%v, done=%v",
			sawSession, sawToolStart, sawToolEnd, sawText, sawUsage, sawDone)
	}
}

// stepClock 每次读取前进固定步长，用来在不等待真实时间的前提下模拟请求计时。
type stepClock struct {
	mu   sync.Mutex
	cur  time.Time
	step time.Duration
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.cur
	c.cur = c.cur.Add(c.step)
	return t
}

// TestOpenAI_RequestSpeedSamples 验证每个工具轮（一次模型请求）各自产生一条速度样本：
// 从该请求的第一个内容帧算到它的 usage chunk，随整轮的 EventUsage 一起上报。
func TestOpenAI_RequestSpeedSamples(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		var chunk1, chunk2 string
		switch count {
		case 1:
			// 第一轮：单次请求产出工具调用，usage 报 5 个输出 token。
			chunk1 = `{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_01","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"doc.txt\"}"}}]},"finish_reason":null}]}`
			chunk2 = `{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":15,"completion_tokens":5}}`
		case 2:
			// 第二轮：单次请求产出正文，usage 报 10 个输出 token。
			chunk1 = `{"id":"c2","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":null}]}`
			chunk2 = `{"id":"c2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":25,"completion_tokens":10}}`
		default:
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", chunk1, chunk2)
		flusher.Flush()
	}))
	defer server.Close()

	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "doc.txt"), []byte("hello doc content"), 0644); err != nil {
		t.Fatalf("write doc.txt: %v", err)
	}

	h := New(Options{
		BaseURL:    server.URL,
		APIKey:     "sk-test",
		Model:      "test-model",
		HTTPClient: server.Client(),
	})
	clock := &stepClock{cur: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), step: 800 * time.Millisecond}
	h.now = clock.Now

	var (
		usage   *harness.Usage
		usageMu sync.Mutex
	)
	emit := func(e harness.Event) {
		if e.Type == harness.EventUsage && e.Usage != nil {
			usageMu.Lock()
			usage = e.Usage
			usageMu.Unlock()
		}
	}

	turn := harness.Turn{Prompt: "Please read doc.txt", WorkDir: workDir, Mode: harness.ModeFullAccess}
	if err := h.Run(context.Background(), turn, emit); err != nil {
		t.Fatalf("h.Run failed: %v", err)
	}
	if atomic.LoadInt32(&requestCount) != 2 {
		t.Fatalf("expected 2 rounds of requests, got %d", atomic.LoadInt32(&requestCount))
	}

	usageMu.Lock()
	defer usageMu.Unlock()
	if usage == nil {
		t.Fatal("expected usage event")
	}
	want := []harness.RequestSpeed{
		{OutputTokens: 5, DurationMs: 800},
		{OutputTokens: 10, DurationMs: 800},
	}
	if !reflect.DeepEqual(usage.Requests, want) {
		t.Errorf("request speeds mismatch: got %+v, want %+v", usage.Requests, want)
	}
}

func TestOpenAI_Non2xxError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API key provided"}}`))
	}))
	defer server.Close()

	h := New(Options{
		BaseURL:    server.URL,
		APIKey:     "bad-key",
		Model:      "test-model",
		HTTPClient: server.Client(),
	})

	var errorEvents []harness.Event
	emit := func(e harness.Event) {
		if e.Type == harness.EventError {
			errorEvents = append(errorEvents, e)
		}
	}

	turn := harness.Turn{
		Prompt:  "test",
		WorkDir: t.TempDir(),
	}

	err := h.Run(context.Background(), turn, emit)
	if err == nil {
		t.Fatalf("expected error from Run on 401, got nil")
	}
	if !strings.Contains(err.Error(), "Invalid API key provided") {
		t.Errorf("error string mismatch: %v", err)
	}
	if len(errorEvents) == 0 || !strings.Contains(errorEvents[0].Err, "Invalid API key provided") {
		t.Errorf("expected EventError with error message, got: %+v", errorEvents)
	}
}

func TestOpenAI_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 阻塞等待或者慢慢输出
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	h := New(Options{
		BaseURL:    server.URL,
		APIKey:     "sk-test",
		Model:      "test-model",
		HTTPClient: server.Client(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	turn := harness.Turn{
		Prompt:  "test cancellation",
		WorkDir: t.TempDir(),
	}

	err := h.Run(ctx, turn, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestOpenAI_SessionHistoryPersistence(t *testing.T) {
	var requestCount int32
	var capturedBodies [][]byte
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		capturedBodies = append(capturedBodies, body)
		mu.Unlock()

		atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := `{"choices":[{"index":0,"delta":{"content":"reply"},"finish_reason":"stop"}]}`
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer server.Close()

	h := New(Options{
		BaseURL:    server.URL,
		APIKey:     "sk-test",
		Model:      "test-model",
		HTTPClient: server.Client(),
	})

	sessionID := "persisted-session-123"

	// 第一轮 Turn
	turn1 := harness.Turn{
		SessionID: sessionID,
		Prompt:    "hello turn 1",
		WorkDir:   t.TempDir(),
	}
	if err := h.Run(context.Background(), turn1, nil); err != nil {
		t.Fatalf("turn 1 failed: %v", err)
	}

	// 第二轮 Turn 使用相同的 SessionID
	turn2 := harness.Turn{
		SessionID: sessionID,
		Prompt:    "hello turn 2",
		WorkDir:   turn1.WorkDir,
	}
	if err := h.Run(context.Background(), turn2, nil); err != nil {
		t.Fatalf("turn 2 failed: %v", err)
	}

	mu.Lock()
	secondReq := capturedBodies[1]
	mu.Unlock()

	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(secondReq, &payload); err != nil {
		t.Fatalf("unmarshal second request: %v", err)
	}

	var hasTurn1Prompt, hasTurn1Reply, hasTurn2Prompt bool
	for _, m := range payload.Messages {
		if m.Role == "user" && m.Content == "hello turn 1" {
			hasTurn1Prompt = true
		}
		if m.Role == "assistant" && m.Content == "reply" {
			hasTurn1Reply = true
		}
		if m.Role == "user" && m.Content == "hello turn 2" {
			hasTurn2Prompt = true
		}
	}

	if !hasTurn1Prompt || !hasTurn1Reply || !hasTurn2Prompt {
		t.Errorf("history persistence failed: turn1Prompt=%v, turn1Reply=%v, turn2Prompt=%v. Payload: %s",
			hasTurn1Prompt, hasTurn1Reply, hasTurn2Prompt, string(secondReq))
	}
}

// TestOpenAI_DeepSeekReasoningSSE 回放 DeepSeek 风格 SSE：先若干 reasoning_content
// 增量，再 content，最后 [DONE]，断言 Run 依次发出 EventReasoning 与文本事件。
func TestOpenAI_DeepSeekReasoningSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunks := []string{
			`{"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":"Let me "},"finish_reason":null}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":"think. "},"finish_reason":null}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":"Done."},"finish_reason":null}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{"content":"Hello "},"finish_reason":null}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{"content":"there"},"finish_reason":null}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	h := New(Options{
		BaseURL:    server.URL,
		APIKey:     "sk-test",
		Model:      "test-model",
		HTTPClient: server.Client(),
	})

	type step struct {
		typ  harness.EventType
		text string
	}
	var steps []step
	emit := func(e harness.Event) {
		if e.Type == harness.EventReasoning || e.Type == harness.EventText {
			steps = append(steps, step{typ: e.Type, text: e.Text})
		}
	}

	turn := harness.Turn{
		Prompt:  "hi",
		WorkDir: t.TempDir(),
		Mode:    harness.ModeFullAccess,
	}
	if err := h.Run(context.Background(), turn, emit); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	want := []step{
		{harness.EventReasoning, "Let me "},
		{harness.EventReasoning, "think. "},
		{harness.EventReasoning, "Done."},
		{harness.EventText, "Hello "},
		{harness.EventText, "there"},
	}
	if len(steps) != len(want) {
		t.Fatalf("event steps = %+v, want %+v", steps, want)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Errorf("step %d = %+v, want %+v", i, steps[i], want[i])
		}
	}
}

// TestOpenAI_DeepSeekReasonerEffortExcluded 断言 deepseek-reasoner 不会带上它
// 不接受的 reasoning_effort 参数。
func TestOpenAI_DeepSeekReasonerEffortExcluded(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	h := New(Options{
		BaseURL:    server.URL,
		APIKey:     "sk-test",
		HTTPClient: server.Client(),
	})

	turn := harness.Turn{
		Prompt:  "hi",
		WorkDir: t.TempDir(),
		Model:   "deepseek-reasoner",
		Effort:  "high",
	}
	if err := h.Run(context.Background(), turn, nil); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if _, ok := payload["reasoning_effort"]; ok {
		t.Errorf("reasoning_effort must not be sent for deepseek-reasoner, body: %s", string(capturedBody))
	}

	// 其它模型仍按自定义端点规则原样透传
	h2 := New(Options{
		BaseURL:    server.URL,
		APIKey:     "sk-test",
		HTTPClient: server.Client(),
	})
	turn2 := turn
	turn2.Model = "deepseek-v4-pro"
	if err := h2.Run(context.Background(), turn2, nil); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("unmarshal second request body: %v", err)
	}
	if got, ok := payload["reasoning_effort"]; !ok || got != "high" {
		t.Errorf("reasoning_effort = %v, want high, body: %s", got, string(capturedBody))
	}
}

// TestOpenAI_ModelIDsMergesConfiguredFirstAndCaches 验证 /models 列表与配置模型的
// 合并去重、配置模型排首位、Bearer 鉴权，以及按实例缓存避免重复请求。
func TestOpenAI_ModelIDsMergesConfiguredFirstAndCaches(t *testing.T) {
	var (
		requests int32
		gotAuth  string
		gotPath  string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"second"},{"id":"first"},{"id":"second"},{"id":"  "}]}`)
	}))
	defer server.Close()

	h := New(Options{
		BaseURL:    server.URL,
		Model:      "first",
		APIKey:     "sk-models",
		HTTPClient: server.Client(),
	})

	want := []string{"first", "second"}
	h.ModelIDs()
	if got := waitModelIDs(t, h); !reflect.DeepEqual(got, want) {
		t.Fatalf("ModelIDs() = %v, want %v", got, want)
	}
	if gotPath != "/models" {
		t.Errorf("path = %q, want /models", gotPath)
	}
	if gotAuth != "Bearer sk-models" {
		t.Errorf("authorization = %q, want Bearer sk-models", gotAuth)
	}

	if got := h.ModelIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("cached ModelIDs() = %v, want %v", got, want)
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Errorf("requests = %d, want 1 (second call should hit cache)", got)
	}
}

// TestOpenAI_ModelIDsFallsBackOnError 验证请求失败时返回配置的模型，并缓存失败结果。
func TestOpenAI_ModelIDsFallsBackOnError(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	h := New(Options{
		BaseURL:    server.URL,
		Model:      "configured-model",
		APIKey:     "sk-test",
		HTTPClient: server.Client(),
	})
	h.ModelIDs()
	if got := waitModelIDs(t, h); len(got) != 1 || got[0] != "configured-model" {
		t.Fatalf("ModelIDs() on error = %v, want ['configured-model']", got)
	}
	if got := h.ModelIDs(); len(got) != 1 || got[0] != "configured-model" {
		t.Fatalf("cached ModelIDs() = %v, want ['configured-model']", got)
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Errorf("requests = %d, want 1 (failure should be cached)", got)
	}

	h2 := New(Options{BaseURL: server.URL, APIKey: "sk-test", HTTPClient: server.Client()})
	if got := h2.ModelIDs(); got != nil {
		t.Errorf("ModelIDs() without configured model = %v, want nil", got)
	}
}

// TestOpenAI_RunUsesFirstModelFromEndpoint 验证非官方端点未配置模型时使用 /models 的第一个模型。
func TestOpenAI_RunUsesFirstModelFromEndpoint(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"auto-model"},{"id":"other-model"}]}`)
		case "/chat/completions":
			capturedBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	h := New(Options{
		Kind:       harness.Kind("custom-test"),
		BaseURL:    server.URL,
		APIKey:     "sk-test",
		HTTPClient: server.Client(),
	})

	turn := harness.Turn{Prompt: "hi", WorkDir: t.TempDir()}
	if err := h.Run(context.Background(), turn, nil); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if payload.Model != "auto-model" {
		t.Errorf("model = %q, want auto-model", payload.Model)
	}
}

// TestOpenAI_RunEmptyModelWithoutCatalog 验证非官方端点没有可用模型时返回明确错误。
func TestOpenAI_RunEmptyModelWithoutCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer server.Close()

	h := New(Options{
		Kind:       harness.Kind("custom-test"),
		BaseURL:    server.URL,
		APIKey:     "sk-test",
		HTTPClient: server.Client(),
	})

	err := h.Run(context.Background(), harness.Turn{Prompt: "hi", WorkDir: t.TempDir()}, nil)
	if err == nil || err.Error() != "未配置模型，且端点未返回模型列表" {
		t.Fatalf("Run error = %v, want 未配置模型，且端点未返回模型列表", err)
	}
}

// TestOpenAI_RunOfficialEndpointDefaultsToGPT4o 验证官方端点未配置模型时仍回退到
// gpt-4o，且不会请求 /models。
func TestOpenAI_RunOfficialEndpointDefaultsToGPT4o(t *testing.T) {
	var (
		capturedBody []byte
		modelsCalls  int32
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			atomic.AddInt32(&modelsCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"server-model"}]}`)
		case "/chat/completions":
			capturedBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	h := New(Options{
		Kind:       harness.Kind("openai"),
		BaseURL:    server.URL,
		APIKey:     "sk-test",
		HTTPClient: server.Client(),
	})
	if err := h.Run(context.Background(), harness.Turn{Prompt: "hi", WorkDir: t.TempDir()}, nil); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if got := atomic.LoadInt32(&modelsCalls); got != 0 {
		t.Errorf("/models calls = %d, want 0 for official endpoint", got)
	}

	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if payload.Model != "gpt-4o" {
		t.Errorf("model = %q, want gpt-4o", payload.Model)
	}
}

// TestOpenAI_TruncatedToolCallProtection 验证当模型输出因 token 上限截断（finish_reason 为 length）时，
// 残缺的工具调用不会被执行（目标文件未被写入），并且在 history 中记录截断错误提示后进入下一轮。
func TestOpenAI_TruncatedToolCallProtection(t *testing.T) {
	var (
		reqCount       int32
		capturedBodies [][]byte
		mu             sync.Mutex
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		capturedBodies = append(capturedBodies, body)
		mu.Unlock()

		count := atomic.AddInt32(&reqCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if count == 1 {
			// 第一轮：返回残缺参数的 write_file 工具调用，finish_reason 为 length
			chunk := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_trunc_1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"truncated.txt\",\"content\":\"incomplete..."}}]},"finish_reason":"length"}]}`
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
		} else {
			// 第二轮：返回普通结束，完成交互
			chunk := `{"choices":[{"index":0,"delta":{"content":"ok, stopping."},"finish_reason":"stop"}]}`
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
		}
	}))
	defer server.Close()

	workDir := t.TempDir()
	targetPath := filepath.Join(workDir, "truncated.txt")

	h := New(Options{
		BaseURL:    server.URL,
		APIKey:     "sk-test",
		Model:      "test-model",
		HTTPClient: server.Client(),
	})

	var (
		eventsMu    sync.Mutex
		seenToolEnd bool
		toolEndErr  bool
		toolEndOut  string
	)
	emit := func(e harness.Event) {
		if e.Type == harness.EventToolEnd {
			eventsMu.Lock()
			seenToolEnd = true
			toolEndErr = e.IsError
			toolEndOut = e.Output
			eventsMu.Unlock()
		}
	}

	turn := harness.Turn{
		Prompt:  "please write truncated.txt",
		WorkDir: workDir,
		Mode:    harness.ModeFullAccess,
	}

	if err := h.Run(context.Background(), turn, emit); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// 1. 断言目标文件没有被写入
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Fatalf("expected target file %s not to be written, but it exists", targetPath)
	}

	// 2. 断言发出了工具失败事件
	eventsMu.Lock()
	if !seenToolEnd || !toolEndErr {
		t.Errorf("expected EventToolEnd with IsError=true, got seen=%v, isError=%v", seenToolEnd, toolEndErr)
	}
	const expectedErr = "error: the response hit the output token limit before the tool arguments finished; re-issue the call in full"
	if toolEndOut != expectedErr {
		t.Errorf("toolEnd output = %q, want %q", toolEndOut, expectedErr)
	}
	eventsMu.Unlock()

	// 3. 断言第二轮请求携带了截断错误历史
	mu.Lock()
	defer mu.Unlock()
	if len(capturedBodies) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(capturedBodies))
	}

	var secondReq struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(capturedBodies[1], &secondReq); err != nil {
		t.Fatalf("unmarshal second request: %v", err)
	}

	var foundToolErrorMsg bool
	for _, m := range secondReq.Messages {
		if m.Role == "tool" && m.Content == expectedErr && m.ToolCallID == "call_trunc_1" {
			foundToolErrorMsg = true
			break
		}
	}
	if !foundToolErrorMsg {
		t.Errorf("expected tool message with truncation error in history, payload: %s", string(capturedBodies[1]))
	}
}

// 200 但不是 event-stream 的响应不能被静默当成空回答。
func TestOpenAI_NonStreamJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"直接回答"},` +
			`"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer server.Close()

	h := New(Options{
		BaseURL:    server.URL,
		APIKey:     "sk-test",
		Model:      "test-model",
		HTTPClient: server.Client(),
	})

	var text strings.Builder
	emit := func(e harness.Event) {
		if e.Type == harness.EventText {
			text.WriteString(e.Text)
		}
	}
	if err := h.Run(context.Background(), harness.Turn{Prompt: "hi", WorkDir: t.TempDir()}, emit); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text.String() != "直接回答" {
		t.Fatalf("expected text from JSON response, got %q", text.String())
	}
}

func TestOpenAI_NonStreamGarbageResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	}))
	defer server.Close()

	h := New(Options{
		BaseURL:    server.URL,
		APIKey:     "sk-test",
		Model:      "test-model",
		HTTPClient: server.Client(),
	})

	err := h.Run(context.Background(), harness.Turn{Prompt: "hi", WorkDir: t.TempDir()}, nil)
	if err == nil {
		t.Fatal("expected error for non-JSON 200 response")
	}
	if !strings.Contains(err.Error(), "bad gateway") {
		t.Fatalf("expected body excerpt in error, got %v", err)
	}
}

// 上游完全不回 usage 时不报错，也不上报 EventUsage（ContextTokens 仍为 0）。
func TestOpenAI_UsageAbsentKeepsContextTokensZero(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunk := `{"id":"c1","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}`
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
		flusher.Flush()
	}))
	defer server.Close()

	h := New(Options{
		BaseURL:    server.URL,
		APIKey:     "sk-test",
		Model:      "test-model",
		HTTPClient: server.Client(),
	})

	var usage *harness.Usage
	emit := func(e harness.Event) {
		if e.Type == harness.EventUsage {
			usage = e.Usage
		}
	}
	if err := h.Run(context.Background(), harness.Turn{Prompt: "hi", WorkDir: t.TempDir()}, emit); err != nil {
		t.Fatalf("上游不回 usage 不应报错: %v", err)
	}
	if usage != nil {
		t.Fatalf("无可统计用量时不应上报 EventUsage: %+v", usage)
	}
}

func TestBuildUserMessageSkipsNonImage(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "a.png")
	if err := os.WriteFile(imagePath, []byte("png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	pdfPath := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.7"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 非图片附件不生成 image_url：它的路径已由 prompt 文本给出。
	msg, err := buildUserMessage(harness.Turn{Prompt: "看附件", Images: []string{imagePath, pdfPath}})
	if err != nil {
		t.Fatal(err)
	}
	parts, ok := msg.Content.([]any)
	if !ok {
		t.Fatalf("有图片时应为多模态分片，得到 %#v", msg.Content)
	}
	if len(parts) != 2 {
		t.Fatalf("应只有 text 与 a.png 两项，得到 %+v", parts)
	}
	if imagePart, ok := parts[1].(map[string]any); !ok || imagePart["type"] != "image_url" {
		t.Fatalf("第二项应为 image_url，得到 %+v", parts[1])
	}

	// 附件全不是图片时退化成纯文本 content。
	msg, err = buildUserMessage(harness.Turn{Prompt: "看附件", Images: []string{pdfPath}})
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := msg.Content.(string); !ok || text != "看附件" {
		t.Fatalf("只有 PDF 时应退化为纯文本 content，得到 %#v", msg.Content)
	}
}

// connIDKey 在测试里标记「请求来自第几条底层连接」。
type connIDKey struct{}

// TestOpenAI_RefreshModelsRecoversFromStreamError 校验 RefreshModels 的生产调用
// 确实接入专属读取客户端：首条连接被 HTTP/2 流中断时换池重试一次即成功，注入的
// opts.HTTPClient 本身保持原值，聊天请求仍用它。
func TestOpenAI_RefreshModelsRecoversFromStreamError(t *testing.T) {
	var conns, requests atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if id, _ := r.Context().Value(connIDKey{}).(int64); id == 1 {
			panic(http.ErrAbortHandler)
		}
		fmt.Fprint(w, `{"data":[{"id":"jp/auto/claude-sonnet-5-5"}]}`)
	}))
	server.EnableHTTP2 = true
	server.Config.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		return context.WithValue(ctx, connIDKey{}, conns.Add(1))
	}
	server.StartTLS()
	defer server.Close()

	injected := server.Client()
	h := New(Options{BaseURL: server.URL, Model: "configured", APIKey: "sk-test", HTTPClient: injected})
	if h.opts.HTTPClient != injected {
		t.Fatal("注入的 opts.HTTPClient 被改动了")
	}
	if h.modelHTTPClient == injected {
		t.Fatal("模型读取应使用独立客户端")
	}
	if h.modelHTTPClient.Transport == injected.Transport {
		t.Fatal("模型读取的 Transport 被直接复用，未克隆独立连接池")
	}
	if err := h.RefreshModels(context.Background()); err != nil {
		t.Fatalf("RefreshModels: %v", err)
	}
	if got := h.ModelIDs(); !reflect.DeepEqual(got, []string{"configured", "jp/auto/claude-sonnet-5-5"}) {
		t.Fatalf("ModelIDs() = %v", got)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("请求数 = %d，想要 2（首条中断 + 一次重试）", got)
	}
	if got := conns.Load(); got != 2 {
		t.Fatalf("底层连接数 = %d，想要 2（换池后新连接）", got)
	}
}
