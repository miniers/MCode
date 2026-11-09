package app

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/providers/magpie"
	"mcode/internal/providers/openai"
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
	var requests []engineTestRequest
	server := newEngineTestServer(t, &requests)

	cfg := config.Defaults()
	cfg.Providers.Presets = map[string]config.PresetProviderConfig{
		"deepseek": {Enabled: true, BaseURL: server.URL, Model: "deepseek-flash"},
	}
	secrets := mcodeTestSecrets(map[string]string{"deepseek.apiKey": "sk-deepseek"})

	preset, ok := openai.PresetByID("deepseek")
	if !ok {
		t.Fatal("deepseek preset not found")
	}
	legacyOpts := openai.PresetOptions(preset, secrets, server.URL, false)
	legacyOpts.Model = "deepseek-flash"
	legacy := openai.New(legacyOpts)

	h := newSourceEngineHarness(legacy, cfg, secrets, "", providerMCP{})
	if got := string(h.Kind()); got != "deepseek" {
		t.Fatalf("Kind = %q, want deepseek", got)
	}
	if got, want := h.(interface{ DisplayName() string }).DisplayName(), legacy.DisplayName(); got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
	gotModels := h.(interface{ ModelIDs() []string }).ModelIDs()
	wantModels := legacy.ModelIDs()
	if len(gotModels) != len(wantModels) || len(gotModels) == 0 || gotModels[0] != wantModels[0] {
		t.Fatalf("ModelIDs = %v, want %v", gotModels, wantModels)
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

func TestSourceEngineMagpieAffinity(t *testing.T) {
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

// TestSourceEngineMagpieUsesMcodeSelection 校验 Magpie 来源的模型目录与默认模型
// 都跟着 Magpie 页为 mcode 勾选的模型走。
func TestSourceEngineMagpieUsesMcodeSelection(t *testing.T) {
	var requests []engineTestRequest
	server := newEngineTestServer(t, &requests)

	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("magpie"): "gateway-key"})
	ep := config.CustomEndpointConfig{
		ID: "magpie", Name: "Magpie", BaseURL: server.URL, Magpie: true, Enabled: true,
	}
	dataDir := t.TempDir()
	writeEngineTestConfig(t, dataDir, ep)
	if err := config.Update(dataDir, func(latest *config.Config) error {
		config.SetMagpieAgentModels(latest, config.ProviderMcode, "magpie", []string{"plain-model", "other-model"})
		config.SetMagpieAgentDefault(latest, config.ProviderMcode, "magpie", "other-model")
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	h := newCustomEndpointHarness(ep, secrets, dataDir, providerMCP{})
	if got, want := strings.Join(h.(interface{ ModelIDs() []string }).ModelIDs(), ","), "other-model,plain-model"; got != want {
		t.Fatalf("ModelIDs = %q, want %q", got, want)
	}
	// 空模型跟随 mcode 勾选：默认模型排在最前。
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

// anthropicMessagesSSE 是 Anthropic Messages 接口的最小事件流。
const anthropicMessagesSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestSourceEngineMagpieClaudeUsesMessages(t *testing.T) {
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
	if req.APIKey != "gateway-key" || req.Auth != "" {
		t.Fatalf("鉴权 = x-api-key %q，Authorization %q", req.APIKey, req.Auth)
	}
	if req.Affinity != "thread-9" || req.ClientID != "thread-9" || req.Session != "thread-9" {
		t.Fatalf("会话亲和头 = %q %q %q", req.Affinity, req.ClientID, req.Session)
	}
}

func TestSourceEngineMagpieCostUSD(t *testing.T) {
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

func TestSourceEngineUsesRotatedSecret(t *testing.T) {
	var requests []engineTestRequest
	server := newEngineTestServer(t, &requests)

	cfg := config.Defaults()
	cfg.Providers.Presets = map[string]config.PresetProviderConfig{
		"deepseek": {Enabled: true, BaseURL: server.URL},
	}
	secrets := mcodeTestSecrets(map[string]string{"deepseek.apiKey": "sk-old"})
	preset, _ := openai.PresetByID("deepseek")
	h := newSourceEngineHarness(openai.New(openai.PresetOptions(preset, secrets, server.URL, false)), cfg, secrets, "", providerMCP{})

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
	preset, _ := openai.PresetByID("deepseek")
	legacy := openai.New(openai.PresetOptions(preset, secrets, "", false))

	// 与 runtime 读盘结果一致：同一个 map 里改成停用后立即生效。
	cfg.Providers.Presets["deepseek"] = config.PresetProviderConfig{Enabled: false}
	_, _, err := resolveSourceClient(harness.Kind("deepseek"), legacy, cfg, secrets, "deepseek-chat", "")
	if err == nil || !strings.Contains(err.Error(), "未启用或已删除") {
		t.Fatalf("err = %v, want 来源未启用或已删除", err)
	}
}

func TestSourceEngineDefaultModel(t *testing.T) {
	cfg := config.Defaults()
	if got := sourceEngineDefaultModel(apisource.Source{DefaultModel: " deepseek-flash "}, harness.Kind("deepseek"), cfg, nil); got != "deepseek-flash" {
		t.Fatalf("来源默认模型 = %q", got)
	}
	if got := sourceEngineDefaultModel(apisource.Source{BaseURL: "https://gateway.example/v1"}, harness.Kind("openai"), cfg, nil); got != "gpt-4o" {
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
