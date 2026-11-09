package app

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/agent/llm"
	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/providers/magpie"
	"mcode/internal/remote"
)

// engineTestRequest 记录一次 /chat/completions 或 /v1/messages 请求的关键字段。
type engineTestRequest struct {
	Path     string
	Auth     string
	APIKey   string
	Model    string
	Affinity string
	ClientID string
	Session  string
}

// newEngineTestServer 启动假 OpenAI 兼容服务：/models 返回空目录，
// /chat/completions 记录请求并返回一条普通（非流式）回复。
func newEngineTestServer(t *testing.T, requests *[]engineTestRequest) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			io.WriteString(w, `{"data":[]}`)
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		*requests = append(*requests, engineTestRequest{
			Path:     r.URL.Path,
			Auth:     r.Header.Get("Authorization"),
			APIKey:   r.Header.Get("x-api-key"),
			Model:    body.Model,
			Affinity: r.Header.Get("x-session-affinity"),
			ClientID: r.Header.Get("x-client-request-id"),
			Session:  r.Header.Get("session_id"),
		})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(server.Close)
	return server
}

// runEngineTurn 跑完一轮内置引擎会话，并返回拼接后的文本。
func runEngineTurn(t *testing.T, h harness.Harness, model, threadID string) string {
	t.Helper()
	var text strings.Builder
	var done bool
	err := h.Run(context.Background(), harness.Turn{
		Prompt:   "hi",
		WorkDir:  t.TempDir(),
		ThreadID: threadID,
		Model:    model,
	}, func(ev harness.Event) {
		switch ev.Type {
		case harness.EventText:
			text.WriteString(ev.Text)
		case harness.EventDone:
			done = true
		}
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if !done {
		t.Fatalf("expected EventDone")
	}
	return text.String()
}

func TestSourceEnginePreset(t *testing.T) {
	t.Cleanup(func() { remote.SetAPICatalog("deepseek", nil); setMcodeAgentRuntime("", nil) })
	var requests []engineTestRequest
	server := newEngineTestServer(t, &requests)

	cfg := config.Defaults()
	cfg.Providers.Presets = map[string]config.PresetProviderConfig{
		"deepseek": {Enabled: true, BaseURL: server.URL, Model: "deepseek-flash"},
	}
	secrets := mcodeTestSecrets(map[string]string{"deepseek.apiKey": "sk-deepseek"})

	presetSource, _ := findAPISource(apisource.Resolve(cfg, secrets), "deepseek")
	h := newSourceEngineHarness("deepseek", cfg, secrets, "", providerMCP{})
	if got := string(h.Kind()); got != "deepseek" {
		t.Fatalf("Kind = %q, want deepseek", got)
	}
	if got, want := h.(interface{ DisplayName() string }).DisplayName(), presetSource.Label; got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
	gotModels := h.(interface{ ModelIDs() []string }).ModelIDs()
	if len(gotModels) == 0 || gotModels[0] != "deepseek-flash" {
		t.Fatalf("ModelIDs = %v", gotModels)
	}
	for _, id := range presetSource.Models {
		if !strings.Contains(","+strings.Join(gotModels, ",")+",", ","+id+",") {
			t.Fatalf("ModelIDs 缺少预设模型 %q：%v", id, gotModels)
		}
	}

	// 空模型与占位模型 default 都落到来源配置的默认模型。
	for _, model := range []string{"", "default", " DEFAULT "} {
		requests = nil
		if reply := runEngineTurn(t, h, model, "thread-1"); reply != "ok" {
			t.Fatalf("reply = %q, want ok", reply)
		}
		if len(requests) != 1 {
			t.Fatalf("模型 %q：请求 %d 次，want 1", model, len(requests))
		}
		req := requests[0]
		if req.Path != "/chat/completions" || req.Auth != "Bearer sk-deepseek" || req.Model != "deepseek-flash" {
			t.Fatalf("模型 %q：请求 = %+v", model, req)
		}
	}
}

// writeEngineTestConfig 把自定义端点写进数据目录，模拟运行时的配置来源。
func writeEngineTestConfig(t *testing.T, dataDir string, ep config.CustomEndpointConfig) {
	t.Helper()
	if err := config.Update(dataDir, func(latest *config.Config) error {
		latest.Providers.Custom = []config.CustomEndpointConfig{ep}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceEngineCustomEndpoint(t *testing.T) {
	t.Cleanup(func() { remote.SetAPICatalog("custom-mock", nil); setMcodeAgentRuntime("", nil) })
	var requests []engineTestRequest
	server := newEngineTestServer(t, &requests)

	dataDir := t.TempDir()
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("mock"): "sk-custom"})
	ep := config.CustomEndpointConfig{
		ID: "mock", Name: "Mock", BaseURL: server.URL, Model: "mock-model", Enabled: true,
	}
	writeEngineTestConfig(t, dataDir, ep)
	h := newCustomEndpointHarness(ep, secrets, dataDir, providerMCP{})
	if got := string(h.Kind()); got != "custom-mock" {
		t.Fatalf("Kind = %q, want custom-mock", got)
	}
	if reply := runEngineTurn(t, h, "", ""); reply != "ok" {
		t.Fatalf("reply = %q, want ok", reply)
	}
	if len(requests) != 1 {
		t.Fatalf("请求 %d 次，want 1", len(requests))
	}
	req := requests[0]
	if req.Path != "/chat/completions" || req.Auth != "Bearer sk-custom" || req.Model != "mock-model" {
		t.Fatalf("请求 = %+v", req)
	}
}

func TestSourceEngineCustomEndpointColdDefault(t *testing.T) {
	setMcodeAgentRuntime("", nil)
	t.Cleanup(func() { setMcodeAgentRuntime("", nil) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"first-model"}]}`)
	}))
	defer server.Close()
	cfg := config.Config{}
	cfg.Providers.Custom = []config.CustomEndpointConfig{{ID: "cold-default", Enabled: true, BaseURL: server.URL}}
	_, model, err := resolveSourceClient("custom-cold-default", cfg, config.NewMemorySecretStore(), "", "")
	if err != nil || model != "first-model" {
		t.Fatalf("默认模型 = %q，错误 = %v", model, err)
	}
}

func TestSourceEngineMagpieAffinity(t *testing.T) {
	t.Cleanup(func() { remote.SetAPICatalog("magpie", nil); setMcodeAgentRuntime("", nil) })
	var requests []engineTestRequest
	server := newEngineTestServer(t, &requests)

	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("magpie"): "gateway-key"})
	ep := config.CustomEndpointConfig{
		ID: "magpie", Name: "Magpie", BaseURL: server.URL, Magpie: true, Enabled: true,
	}
	dataDir := t.TempDir()
	writeEngineTestConfig(t, dataDir, ep)
	h := newCustomEndpointHarness(ep, secrets, dataDir, providerMCP{})
	if got := string(h.Kind()); got != "magpie" {
		t.Fatalf("Kind = %q, want magpie", got)
	}
	if reply := runEngineTurn(t, h, "plain-model", "thread-42"); reply != "ok" {
		t.Fatalf("reply = %q, want ok", reply)
	}
	if len(requests) != 1 {
		t.Fatalf("请求 %d 次，want 1", len(requests))
	}
	req := requests[0]
	if req.Path != "/chat/completions" {
		t.Fatalf("非 Claude 模型请求路径 = %q", req.Path)
	}
	if req.Auth != "Bearer gateway-key" {
		t.Fatalf("Authorization = %q", req.Auth)
	}
	if req.Affinity != "thread-42" || req.ClientID != "thread-42" || req.Session != "thread-42" {
		t.Fatalf("会话亲和头 = %q %q %q", req.Affinity, req.ClientID, req.Session)
	}
}

// Magpie 未勾选模型时仍使用端点的完整目录和显式默认模型。
func TestSourceEngineMagpieFullCatalog(t *testing.T) {
	t.Cleanup(func() { remote.SetAPICatalog("magpie", nil); setMcodeAgentRuntime("", nil) })
	var requests []engineTestRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			io.WriteString(w, `{"data":[{"id":"plain-model"},{"id":"other-model"}]}`)
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests = append(requests, engineTestRequest{Model: body.Model})
		io.WriteString(w, `{"id":"c1","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(server.Close)

	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("magpie"): "gateway-key"})
	ep := config.CustomEndpointConfig{
		ID: "magpie", Name: "Magpie", BaseURL: server.URL, Model: "other-model", Magpie: true, Enabled: true,
	}
	dataDir := t.TempDir()
	writeEngineTestConfig(t, dataDir, ep)
	h := newCustomEndpointHarness(ep, secrets, dataDir, providerMCP{})
	if err := h.(interface{ RefreshModels(context.Context) error }).RefreshModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(h.(interface{ ModelIDs() []string }).ModelIDs(), ","), "other-model,plain-model"; got != want {
		t.Fatalf("ModelIDs = %q, want %q", got, want)
	}
	// 空模型使用端点的显式默认模型。
	if reply := runEngineTurn(t, h, "", "thread-1"); reply != "ok" {
		t.Fatalf("reply = %q, want ok", reply)
	}
	if len(requests) != 1 || requests[0].Model != "other-model" {
		t.Fatalf("默认模型请求 = %+v", requests)
	}
	// 没勾选的模型照样能发请求：老会话不受模型目录影响。
	requests = nil
	if reply := runEngineTurn(t, h, "plain-model", "thread-2"); reply != "ok" {
		t.Fatalf("reply = %q, want ok", reply)
	}
	if len(requests) != 1 || requests[0].Model != "plain-model" {
		t.Fatalf("显式模型请求 = %+v", requests)
	}
}

func TestSourceEngineCatalogUsesLatestConfig(t *testing.T) {
	dir := t.TempDir()
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("latest"): "key"})
	ep := config.CustomEndpointConfig{ID: "latest", Name: "Latest", BaseURL: "https://example.com", Model: "first", Enabled: true}
	writeEngineTestConfig(t, dir, ep)
	newCustomEndpointHarness(ep, secrets, dir, providerMCP{})
	t.Cleanup(func() { remote.SetAPICatalog("custom-latest", nil); setMcodeAgentRuntime("", nil) })
	if rows := remote.ProviderModels("custom-latest"); len(rows) == 0 || rows[0].ID != "first" {
		t.Fatalf("初始目录 = %v", rows)
	}
	ep.Model = "second"
	writeEngineTestConfig(t, dir, ep)
	if rows := remote.ProviderModels("custom-latest"); len(rows) == 0 || rows[0].ID != "second" {
		t.Fatalf("更新后目录 = %v", rows)
	}
	ep.Enabled = false
	writeEngineTestConfig(t, dir, ep)
	if rows := remote.ProviderModels("custom-latest"); len(rows) != 0 {
		t.Fatalf("停用后的目录 = %v", rows)
	}
}

func TestSourceEngineMagpieColdDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"first"},{"id":"preferred"}]}`)
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	ep := config.CustomEndpointConfig{ID: "magpie", Name: "Magpie", BaseURL: server.URL, Model: "preferred", Magpie: true, Enabled: true}
	writeEngineTestConfig(t, dir, ep)
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("magpie"): "key"})
	newCustomEndpointHarness(ep, secrets, dir, providerMCP{})
	t.Cleanup(func() { remote.SetAPICatalog("magpie", nil); setMcodeAgentRuntime("", nil) })
	_, model, err := resolveSourceClient("magpie", config.Defaults(), secrets, "default", "")
	if err != nil || model != "preferred" {
		t.Fatalf("冷缓存默认模型 = %q，错误 = %v", model, err)
	}
}

func TestAnthropicEngineRemoteCatalogWithoutKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	setMcodeAgentRuntime("", nil)
	t.Cleanup(func() { remote.SetAPICatalog("anthropic", nil); setMcodeAgentRuntime("", nil) })
	newAnthropicEngineHarness(config.Config{}, config.NewMemorySecretStore(), "", providerMCP{})
	rows := remote.ProviderModels("anthropic")
	want := []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}
	if len(rows) != len(want) {
		t.Fatalf("Anthropic 目录 = %#v", rows)
	}
	for i, id := range want {
		if rows[i].ID != id {
			t.Fatalf("Anthropic 目录 = %#v", rows)
		}
	}
}

func TestSourceEngineMagpieColdDefaultFetch(t *testing.T) {
	setMcodeAgentRuntime("", nil)
	t.Cleanup(func() { setMcodeAgentRuntime("", nil) })
	originalTimeout := magpieDefaultModelTimeout
	magpieDefaultModelTimeout = 100 * time.Millisecond
	t.Cleanup(func() { magpieDefaultModelTimeout = originalTimeout })
	for _, tc := range []struct {
		name      string
		handler   http.HandlerFunc
		wantModel string
		wantError string
	}{
		{"成功", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"data":[{"id":"fetched-model"}]}`) }, "fetched-model", ""},
		{"服务错误", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}, "", "获取模型目录失败"},
		{"超时", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, "", "获取模型目录失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			cfg := config.Config{}
			cfg.Providers.Custom = []config.CustomEndpointConfig{{ID: "cold", Enabled: true, Magpie: true, BaseURL: server.URL}}
			secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("cold"): "key"})
			start := time.Now()
			_, model, err := resolveSourceClient("custom-cold", cfg, secrets, "default", "")
			elapsed := time.Since(start)
			if tc.wantError == "" {
				if err != nil || model != tc.wantModel {
					t.Fatalf("默认模型 = %q，错误 = %v", model, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("预期错误 %q，得到 %v", tc.wantError, err)
			}
			if elapsed > magpieDefaultModelTimeout+time.Second {
				t.Fatalf("获取目录耗时过长：%v", elapsed)
			}
		})
	}
}

// anthropicMessagesSSE 是 Anthropic Messages 接口的最小事件流。
const anthropicMessagesSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestSourceEngineMagpieClaudeUsesMessages(t *testing.T) {
	t.Cleanup(func() { remote.SetAPICatalog("magpie", nil); setMcodeAgentRuntime("", nil) })
	var mu sync.Mutex
	var requests []engineTestRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			io.WriteString(w, `{"data":[]}`)
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		requests = append(requests, engineTestRequest{
			Path:     r.URL.Path,
			Auth:     r.Header.Get("Authorization"),
			APIKey:   r.Header.Get("x-api-key"),
			Model:    body.Model,
			Affinity: r.Header.Get("x-session-affinity"),
			ClientID: r.Header.Get("x-client-request-id"),
			Session:  r.Header.Get("session_id"),
		})
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, anthropicMessagesSSE)
	}))
	t.Cleanup(server.Close)

	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("magpie"): "gateway-key"})
	ep := config.CustomEndpointConfig{
		ID: "magpie", Name: "Magpie", BaseURL: server.URL + "/v1", Magpie: true, Enabled: true,
	}
	dataDir := t.TempDir()
	writeEngineTestConfig(t, dataDir, ep)
	h := newCustomEndpointHarness(ep, secrets, dataDir, providerMCP{})
	if reply := runEngineTurn(t, h, "claude-sonnet-5-5", "thread-9"); reply != "ok" {
		t.Fatalf("reply = %q, want ok", reply)
	}
	if len(requests) != 1 {
		t.Fatalf("请求 %d 次，want 1", len(requests))
	}
	req := requests[0]
	if req.Path != "/v1/messages" || req.Model != "claude-sonnet-5-5" {
		t.Fatalf("Claude 请求 = %+v", req)
	}
	if req.APIKey != "" || req.Auth != "Bearer gateway-key" {
		t.Fatalf("鉴权 = x-api-key %q，Authorization %q", req.APIKey, req.Auth)
	}
	if req.Affinity != "thread-9" || req.ClientID != "thread-9" || req.Session != "thread-9" {
		t.Fatalf("会话亲和头 = %q %q %q", req.Affinity, req.ClientID, req.Session)
	}
}

func TestSourceEngineMagpieCostUSD(t *testing.T) {
	t.Cleanup(func() { remote.SetAPICatalog("magpie", nil); setMcodeAgentRuntime("", nil) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[{"id":"plain-model","magpie_price":{"input":3,"output":15,"cache_read":0.3,"cache_write":3.75}}]}`)
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"c1","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("magpie"): "gateway-key"})
	ep := config.CustomEndpointConfig{
		ID: "magpie", Name: "Magpie", BaseURL: server.URL + "/v1", Magpie: true, Enabled: true,
	}
	dataDir := t.TempDir()
	writeEngineTestConfig(t, dataDir, ep)
	if _, err := magpie.FetchModels(context.Background(), server.URL+"/v1", "gateway-key"); err != nil {
		t.Fatalf("填充模型缓存失败: %v", err)
	}
	h := newCustomEndpointHarness(ep, secrets, dataDir, providerMCP{})
	var cost float64
	var seen bool
	err := h.Run(context.Background(), harness.Turn{Prompt: "hi", WorkDir: t.TempDir(), Model: "plain-model"}, func(ev harness.Event) {
		if ev.Type == harness.EventUsage && ev.Usage != nil {
			seen = true
			cost = ev.Usage.CostUSD
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !seen || math.Abs(cost-0.00045) > 1e-9 {
		t.Fatalf("usage = %v, cost = %v", seen, cost)
	}
}

func TestSourceEngineMagpieModelCapabilities(t *testing.T) {
	setMcodeAgentRuntime("", nil)
	t.Cleanup(func() {
		setMcodeAgentRuntime("", nil)
		remote.SetAPICatalog("custom-capabilities", nil)
		remote.SetAPICatalog("custom-ordinary", nil)
	})
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			io.WriteString(w, `{"data":[{"id":"model-a","max_output_tokens":8192,"modalities":{"input":["text"]}},{"id":"model-b","modalities":{"input":["text","image"]}}]}`)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		requests = append(requests, body)
		io.WriteString(w, `{"id":"c1","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("capabilities"): "key", config.CustomEndpointSecretAccount("ordinary"): "key"})
	cfg := config.Defaults()
	cfg.Providers.Custom = []config.CustomEndpointConfig{
		{ID: "capabilities", Enabled: true, Magpie: true, BaseURL: server.URL},
		{ID: "ordinary", Enabled: true, BaseURL: server.URL},
	}
	if _, err := magpie.FetchModels(context.Background(), server.URL, "key"); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(image, []byte("image data"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind      harness.Kind
		model     string
		wantImage bool
		wantMax   float64
	}{
		{"custom-capabilities", "model-a", false, 8192},
		{"custom-capabilities", "model-b", true, 32000},
		{"custom-capabilities", "missing", true, 0},
		{"custom-ordinary", "model-a", true, 0},
	} {
		t.Run(string(tc.kind)+"/"+tc.model, func(t *testing.T) {
			var ep config.CustomEndpointConfig
			for _, candidate := range cfg.Providers.Custom {
				if "custom-"+candidate.ID == string(tc.kind) {
					ep = candidate
				}
			}
			h := newCustomEndpointHarness(ep, secrets, "", providerMCP{})
			requests = nil
			if err := h.Run(context.Background(), harness.Turn{Model: tc.model, Prompt: "hi", Images: []string{image}, WorkDir: t.TempDir()}, nil); err != nil {
				t.Fatal(err)
			}
			if len(requests) != 1 {
				t.Fatalf("请求数 = %d", len(requests))
			}
			body := requests[0]
			encoded, err := json.Marshal(body["messages"])
			if err != nil {
				t.Fatal(err)
			}
			hasImage := strings.Contains(string(encoded), "image_url")
			max, _ := body["max_tokens"].(float64)
			if hasImage != tc.wantImage || max != tc.wantMax {
				t.Fatalf("图片 = %v，输出上限 = %v", hasImage, max)
			}
		})
	}
	for _, model := range []string{"", " DEFAULT "} {
		if _, ok := magpieCachedModelFor("custom-capabilities", cfg, secrets, model); ok {
			t.Fatalf("占位模型 %q 不应命中缓存", model)
		}
	}
}

func TestSourceEngineUsesRotatedSecret(t *testing.T) {
	t.Cleanup(func() { remote.SetAPICatalog("deepseek", nil); setMcodeAgentRuntime("", nil) })
	var requests []engineTestRequest
	server := newEngineTestServer(t, &requests)

	cfg := config.Defaults()
	cfg.Providers.Presets = map[string]config.PresetProviderConfig{
		"deepseek": {Enabled: true, BaseURL: server.URL},
	}
	secrets := mcodeTestSecrets(map[string]string{"deepseek.apiKey": "sk-old"})
	h := newSourceEngineHarness("deepseek", cfg, secrets, "", providerMCP{})

	runEngineTurn(t, h, "deepseek-chat", "")
	if len(requests) != 1 || requests[0].Auth != "Bearer sk-old" {
		t.Fatalf("第一轮请求 = %+v", requests)
	}
	if err := secrets.Set("deepseek.apiKey", "sk-new"); err != nil {
		t.Fatal(err)
	}
	runEngineTurn(t, h, "deepseek-chat", "")
	if len(requests) != 2 || requests[1].Auth != "Bearer sk-new" {
		t.Fatalf("第二轮请求 = %+v", requests)
	}
}

func TestSourceEngineDisabledSource(t *testing.T) {
	cfg := config.Defaults()
	cfg.Providers.Presets = map[string]config.PresetProviderConfig{
		"deepseek": {Enabled: true, BaseURL: "https://api.deepseek.com"},
	}
	secrets := mcodeTestSecrets(map[string]string{"deepseek.apiKey": "sk"})
	// 与 runtime 读盘结果一致：同一个 map 里改成停用后立即生效。
	cfg.Providers.Presets["deepseek"] = config.PresetProviderConfig{Enabled: false}
	_, _, err := resolveSourceClient(harness.Kind("deepseek"), cfg, secrets, "deepseek-chat", "")
	if err == nil || !strings.Contains(err.Error(), "未启用或已删除") {
		t.Fatalf("err = %v, want 来源未启用或已删除", err)
	}
}

func TestSourceEngineDefaultPlaceholder(t *testing.T) {
	source := apisource.Source{DefaultModel: " DEFAULT ", Models: []string{"first"}}
	if got := sourceEngineDefaultModel(source, "openai", config.NewMemorySecretStore()); got != "gpt-4o" {
		t.Fatalf("默认模型 = %q", got)
	}
}

func TestSourceEngineAnthropicProtocol(t *testing.T) {
	setMcodeAgentRuntime("", nil)
	t.Cleanup(func() { setMcodeAgentRuntime("", nil) })
	var path, key string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, key = r.URL.Path, r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, anthropicMessagesSSE)
	}))
	defer server.Close()
	t.Setenv("ANTHROPIC_BASE_URL", server.URL)
	secrets := mcodeTestSecrets(map[string]string{"anthropic.apiKey": "anthropic-key"})
	client, model, err := resolveSourceClient("anthropic", config.Defaults(), secrets, "claude-test", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Stream(context.Background(), llm.Request{Model: model}, nil); err != nil {
		t.Fatal(err)
	}
	if path != "/v1/messages" || key != "anthropic-key" {
		t.Fatalf("路径 = %q，密钥 = %q", path, key)
	}
}

func TestSourceEngineDefaultModel(t *testing.T) {
	if got := sourceEngineDefaultModel(apisource.Source{DefaultModel: " deepseek-flash "}, harness.Kind("deepseek"), nil); got != "deepseek-flash" {
		t.Fatalf("来源默认模型 = %q", got)
	}
	if got := sourceEngineDefaultModel(apisource.Source{BaseURL: "https://gateway.example/v1"}, harness.Kind("openai"), nil); got != "gpt-4o" {
		t.Fatalf("官方 OpenAI 默认模型 = %q, want gpt-4o", got)
	}
}

func TestEngineReasoningEffort(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		model   string
		effort  string
		want    string
		send    bool
	}{
		{"非官方端点原样透传", "https://gateway.example/v1", "gpt-4o", "xhigh", "xhigh", true},
		{"非官方端点透传 medium", "https://gateway.example/v1", "some-model", "medium", "medium", true},
		{"官方端点非推理模型不发", openAIOfficialBaseURL, "gpt-4o", "high", "", false},
		{"官方端点 o3 透传", openAIOfficialBaseURL, "o3-mini", "medium", "medium", true},
		{"官方端点 o1 的 max 归一 high", openAIOfficialBaseURL, "o1-preview", "max", "high", true},
		{"官方端点不支持取值不发", openAIOfficialBaseURL, "o3-mini", "minimal", "", false},
		{"官方端点尾斜杠等价", openAIOfficialBaseURL + "/", "o3-mini", "low", "low", true},
		{"deepseek-reasoner 不发送", "https://gateway.example/v1", "DeepSeek-Reasoner", "high", "", false},
		{"effort 为空不发送", openAIOfficialBaseURL, "o3-mini", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, send := engineReasoningEffort(tc.baseURL)(tc.model, tc.effort)
			if got != tc.want || send != tc.send {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, send, tc.want, tc.send)
			}
		})
	}
}
