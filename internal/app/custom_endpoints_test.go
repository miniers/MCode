package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	mcodeui "mcode/internal/ui"
)

func TestCustomEndpoints_SaveDeleteLifecycle(t *testing.T) {
	dataDir := t.TempDir()
	secrets := config.NewMemorySecretStore()

	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	backend := remote.NewMemoryBackend(hub)
	defer backend.Close()

	client := NewClient(backend)
	client.SetProviderStore(dataDir, secrets)

	ctx := context.Background()

	// 1. 保存自定义端点
	endpoint := mcodeui.CustomEndpoint{
		Name:    "Local Ollama",
		BaseURL: "http://localhost:11434/v1",
		Model:   "llama3:latest",
		Enabled: true,
	}
	saved, err := client.SaveCustomEndpoint(ctx, endpoint, "sk-secret-key-123", "")
	if err != nil {
		t.Fatalf("SaveCustomEndpoint failed: %v", err)
	}
	if saved.ID == "" {
		t.Fatalf("expected non-empty endpoint ID")
	}
	if !saved.HasAPIKey {
		t.Fatalf("expected HasAPIKey=true for saved endpoint")
	}
	if saved.Name != "Local Ollama" {
		t.Fatalf("expected Name 'Local Ollama', got %q", saved.Name)
	}

	// 2. 检查配置文件含该端点且不含 key 明文
	rawConfig, err := os.ReadFile(config.ConfigPath(dataDir))
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	configStr := string(rawConfig)
	if !strings.Contains(configStr, "Local Ollama") || !strings.Contains(configStr, "llama3:latest") {
		t.Fatalf("config should contain endpoint metadata, got: %s", configStr)
	}
	if strings.Contains(strings.ToLower(configStr), "sk-secret-key-123") {
		t.Fatalf("config file must not contain raw api key")
	}

	// 3. secrets 有 key
	secretVal, err := secrets.Get(config.CustomEndpointSecretAccount(saved.ID))
	if err != nil || secretVal != "sk-secret-key-123" {
		t.Fatalf("secrets Get mismatch: val=%q, err=%v", secretVal, err)
	}

	// 4. CustomEndpoints 查询包含该端点
	list, err := client.CustomEndpoints(ctx)
	if err != nil {
		t.Fatalf("CustomEndpoints failed: %v", err)
	}
	if len(list) != 1 || list[0].ID != saved.ID || !list[0].HasAPIKey {
		t.Fatalf("unexpected CustomEndpoints list: %+v", list)
	}

	// 5. Client.Providers 列表包含 ID 为 "custom-<id>" 且 Name 为端点名、Models 含默认模型
	providers, err := client.Providers(ctx)
	if err != nil {
		t.Fatalf("client.Providers failed: %v", err)
	}
	expectedKind := "custom-" + saved.ID
	var foundProv *mcodeui.ProviderInfo
	for i := range providers {
		if providers[i].ID == expectedKind {
			foundProv = &providers[i]
			break
		}
	}
	if foundProv == nil {
		t.Fatalf("provider %s not found in Providers: %+v", expectedKind, providers)
	}
	if foundProv.Name != "Local Ollama" {
		t.Errorf("expected provider Name 'Local Ollama', got %q", foundProv.Name)
	}
	if len(foundProv.Models) != 1 || foundProv.Models[0].ID != "llama3:latest" {
		t.Errorf("expected provider models to contain llama3:latest, got: %+v", foundProv.Models)
	}

	// 6. apiKey 为空时保存保留原 key
	updated, err := client.SaveCustomEndpoint(ctx, mcodeui.CustomEndpoint{
		ID:      saved.ID,
		Name:    "Local Ollama Renamed",
		BaseURL: "http://localhost:11434/v1",
		Model:   "llama3:latest",
		Enabled: true,
	}, "", "")
	if err != nil {
		t.Fatalf("SaveCustomEndpoint with empty apiKey failed: %v", err)
	}
	if !updated.HasAPIKey {
		t.Fatalf("expected HasAPIKey=true after saving empty key")
	}
	secretValAfter, err := secrets.Get(config.CustomEndpointSecretAccount(saved.ID))
	if err != nil || secretValAfter != "sk-secret-key-123" {
		t.Fatalf("api key should be preserved in secrets, got val=%q, err=%v", secretValAfter, err)
	}

	// 7. Enabled=false 时 Providers 不再包含
	_, err = client.SaveCustomEndpoint(ctx, mcodeui.CustomEndpoint{
		ID:      saved.ID,
		Name:    "Local Ollama Renamed",
		BaseURL: "http://localhost:11434/v1",
		Model:   "llama3:latest",
		Enabled: false,
	}, "", "")
	if err != nil {
		t.Fatalf("SaveCustomEndpoint with enabled=false failed: %v", err)
	}
	provsDisabled, err := client.Providers(ctx)
	if err != nil {
		t.Fatalf("client.Providers failed: %v", err)
	}
	for _, p := range provsDisabled {
		if p.ID == expectedKind {
			t.Fatalf("disabled provider %s should not appear in Providers list", expectedKind)
		}
	}

	// 8. 删除后配置、secret、provider 均移除
	if err := client.DeleteCustomEndpoint(ctx, saved.ID); err != nil {
		t.Fatalf("DeleteCustomEndpoint failed: %v", err)
	}
	cfgAfterDelete, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("load config after delete: %v", err)
	}
	if len(cfgAfterDelete.Providers.Custom) != 0 {
		t.Fatalf("expected empty custom endpoints after delete, got %d", len(cfgAfterDelete.Providers.Custom))
	}
	_, err = secrets.Get(config.CustomEndpointSecretAccount(saved.ID))
	if !errors.Is(err, config.ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound after delete, got %v", err)
	}
	provsAfterDelete, err := client.Providers(ctx)
	if err != nil {
		t.Fatalf("client.Providers failed: %v", err)
	}
	for _, p := range provsAfterDelete {
		if p.ID == expectedKind {
			t.Fatalf("deleted provider %s should not appear in Providers list", expectedKind)
		}
	}

	// 删除不存在的 ID 报错
	if err := client.DeleteCustomEndpoint(ctx, "non-existent"); err == nil {
		t.Fatalf("expected error deleting non-existent endpoint, got nil")
	}
}

func TestCustomEndpoints_TestCustomEndpoint(t *testing.T) {
	client := NewClient(nil)
	ctx := context.Background()

	// 1. 非法 BaseURL 返回 error
	invalidEP := mcodeui.CustomEndpoint{BaseURL: "ftp://invalid-scheme.com"}
	if _, err := client.TestCustomEndpoint(ctx, invalidEP, ""); err == nil {
		t.Fatalf("expected error on invalid scheme ftp")
	}
	noHostEP := mcodeui.CustomEndpoint{BaseURL: "http://"}
	if _, err := client.TestCustomEndpoint(ctx, noHostEP, ""); err == nil {
		t.Fatalf("expected error on missing host")
	}

	// 2. 成功（httptest 返回 data 列表，断言 Authorization 头）
	var receivedAuthHeader string
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		receivedAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "gpt-4o"},
				{"id": "gpt-4o-mini"},
			},
		})
	}))
	defer mockServer.Close()

	successEP := mcodeui.CustomEndpoint{BaseURL: mockServer.URL}
	res, err := client.TestCustomEndpoint(ctx, successEP, "sk-test-token")
	if err != nil {
		t.Fatalf("TestCustomEndpoint should not return error on HTTP check, got: %v", err)
	}
	if !res.OK {
		t.Fatalf("expected OK=true, got false, message: %s", res.Message)
	}
	if receivedAuthHeader != "Bearer sk-test-token" {
		t.Errorf("expected Authorization 'Bearer sk-test-token', got %q", receivedAuthHeader)
	}
	if len(res.Models) != 2 || res.Models[0] != "gpt-4o" || res.Models[1] != "gpt-4o-mini" {
		t.Errorf("unexpected Models returned: %v", res.Models)
	}
	if !strings.Contains(res.Message, "可用模型 2 个") {
		t.Errorf("expected success message with count, got: %q", res.Message)
	}
	if res.Latency <= 0 {
		t.Errorf("expected positive latency, got %v", res.Latency)
	}

	// 3. 401 失败消息含 401
	unauthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Invalid API key provided", http.StatusUnauthorized)
	}))
	defer unauthServer.Close()

	res401, err := client.TestCustomEndpoint(ctx, mcodeui.CustomEndpoint{BaseURL: unauthServer.URL}, "wrong-key")
	if err != nil {
		t.Fatalf("network failure should not return error: %v", err)
	}
	if res401.OK {
		t.Fatalf("expected OK=false on 401")
	}
	if res401.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected StatusCode 401, got %d", res401.StatusCode)
	}
	if !strings.Contains(res401.Message, "401") {
		t.Errorf("expected Message to contain '401', got: %q", res401.Message)
	}

	// 4. 超时
	origTimeout := customEndpointTestTimeout
	customEndpointTestTimeout = 40 * time.Millisecond
	defer func() { customEndpointTestTimeout = origTimeout }()

	sleepServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer sleepServer.Close()

	resTimeout, err := client.TestCustomEndpoint(ctx, mcodeui.CustomEndpoint{BaseURL: sleepServer.URL}, "")
	if err != nil {
		t.Fatalf("timeout should not return error, got: %v", err)
	}
	if resTimeout.OK {
		t.Fatalf("expected OK=false on timeout")
	}
	if !strings.Contains(resTimeout.Message, "超时") {
		t.Errorf("expected timeout message, got: %q", resTimeout.Message)
	}
}

func TestCustomEndpoints_ChatCompletionsDialogue(t *testing.T) {
	// 模拟 /chat/completions SSE 服务，驱动通过 newCustomEndpointHarness 构造的实例完成一轮会话
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[]}`)
			return
		}
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		chunk1 := `{"id":"c1","choices":[{"index":0,"delta":{"content":"Hello from custom endpoint!"},"finish_reason":null}]}`
		chunk2 := `{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
		fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", chunk1, chunk2)
		flusher.Flush()
	}))
	defer server.Close()

	dataDir := t.TempDir()
	secrets := config.NewMemorySecretStore()
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	backend := remote.NewMemoryBackend(hub)
	defer backend.Close()

	client := NewClient(backend)
	client.SetProviderStore(dataDir, secrets)

	ctx := context.Background()
	ep, err := client.SaveCustomEndpoint(ctx, mcodeui.CustomEndpoint{
		Name:    "Mock Custom",
		BaseURL: server.URL,
		Model:   "custom-mock",
		Enabled: true,
	}, "sk-dummy-key", "")
	if err != nil {
		t.Fatalf("SaveCustomEndpoint failed: %v", err)
	}

	h := newCustomEndpointHarness(config.CustomEndpointConfig{
		ID:      ep.ID,
		Name:    ep.Name,
		BaseURL: server.URL,
		Model:   ep.Model,
		Enabled: true,
	}, secrets, dataDir, providerMCP{})

	var textParts []string
	var done bool
	err = h.Run(ctx, harness.Turn{
		Prompt:  "hi",
		WorkDir: t.TempDir(),
	}, func(ev harness.Event) {
		switch ev.Type {
		case harness.EventText:
			textParts = append(textParts, ev.Text)
		case harness.EventDone:
			done = true
		}
	})

	if err != nil {
		t.Fatalf("Harness Run failed: %v", err)
	}
	if !done {
		t.Fatalf("expected harness EventDone")
	}
	fullReply := strings.Join(textParts, "")
	if fullReply != "Hello from custom endpoint!" {
		t.Fatalf("expected reply 'Hello from custom endpoint!', got %q", fullReply)
	}
}

type failingEndpointSecrets struct {
	config.SecretStore
	setErr, deleteErr error
}

func (s *failingEndpointSecrets) Set(account, value string) error {
	if s.setErr != nil {
		return s.setErr
	}
	return s.SecretStore.Set(account, value)
}

func (s *failingEndpointSecrets) Delete(account string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.SecretStore.Delete(account)
}

func TestCustomEndpoints_SecretFailuresPreserveConfig(t *testing.T) {
	dir := t.TempDir()
	store := &failingEndpointSecrets{SecretStore: config.NewMemorySecretStore()}
	client := NewClient(nil)
	client.SetProviderStore(dir, store)
	ctx := context.Background()
	ep := mcodeui.CustomEndpoint{Name: "原端点", BaseURL: "http://localhost/v1", Enabled: true}
	store.setErr = errors.New("keyring 写入失败")
	if _, err := client.SaveCustomEndpoint(ctx, ep, "secret", ""); err == nil {
		t.Fatal("凭据写入失败应返回错误")
	}
	cfg, err := config.Load(dir)
	if err != nil || len(cfg.Providers.Custom) != 0 {
		t.Fatalf("凭据写入失败后不应留下配置：%+v, %v", cfg.Providers.Custom, err)
	}
	store.setErr = nil
	saved, err := client.SaveCustomEndpoint(ctx, ep, "secret", "")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(config.ConfigPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	store.setErr = errors.New("keyring 写入失败")
	saved.Name = "不应保存的新名称"
	if _, err := client.SaveCustomEndpoint(ctx, saved, "new-secret", ""); err == nil {
		t.Fatal("更新密钥失败应返回错误")
	}
	store.setErr = nil
	store.deleteErr = errors.New("keyring 删除失败")
	if err := client.DeleteCustomEndpoint(ctx, saved.ID); err == nil {
		t.Fatal("删除密钥失败应返回错误")
	}
	after, err := os.ReadFile(config.ConfigPath(dir))
	if err != nil || string(before) != string(after) {
		t.Fatalf("失败操作不应修改配置：%v", err)
	}
	client.SetProviderStore(dir, nil)
	if _, err := client.SaveCustomEndpoint(ctx, ep, "secret", ""); err == nil {
		t.Fatal("未配置凭据存储时不得假装保存成功")
	}
}

func TestCustomEndpoints_TestResponseValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"HTML", "<html>登录页面</html>"},
		{"错误结构", `{"error":"not an API"}`},
		{"响应过大", strings.Repeat("x", (1<<20)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			res, err := NewClient(nil).TestCustomEndpoint(context.Background(), mcodeui.CustomEndpoint{BaseURL: server.URL}, "")
			if err != nil || res.OK {
				t.Fatalf("无效响应不得判为成功：%+v, %v", res, err)
			}
		})
	}
}

func TestCustomEndpoints_TestBodyTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	res, err := NewClient(nil).TestCustomEndpoint(ctx, mcodeui.CustomEndpoint{BaseURL: server.URL}, "")
	if err != nil || res.OK || !strings.Contains(res.Message, "超时") {
		t.Fatalf("响应头之后超时也必须失败：%+v, %v", res, err)
	}
}

func TestCustomEndpoints_TestKeyPriority(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "environment-key")
	store := config.NewMemorySecretStore()
	account := config.CustomEndpointSecretAccount("saved")
	if err := store.Set(account, "saved-key"); err != nil {
		t.Fatal(err)
	}
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/models" {
			t.Errorf("错误路径：%s", r.URL.Path)
		}
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer server.Close()
	client := NewClient(nil)
	client.SetProviderStore(t.TempDir(), store)
	for _, tc := range []struct{ id, key, want string }{
		{"saved", "explicit-key", "Bearer explicit-key"},
		{"saved", "", "Bearer saved-key"},
		// 自定义端点不回退 OPENAI_API_KEY：即使环境变量已设置，也不能把官方密钥发给第三方端点。
		{"missing", "", ""},
	} {
		res, err := client.TestCustomEndpoint(context.Background(), mcodeui.CustomEndpoint{
			ID: tc.id, BaseURL: server.URL + "/v1/",
		}, tc.key)
		if err != nil || !res.OK || received != tc.want {
			t.Fatalf("密钥优先级错误：%+v, %v, Authorization=%q", res, err, received)
		}
	}
}

func TestCustomEndpoints_RejectURLCredentialsAndQuery(t *testing.T) {
	client := NewClient(nil)
	client.SetProviderStore(t.TempDir(), config.NewMemorySecretStore())
	for _, raw := range []string{
		"https://user:secret@example.com/v1",
		"https://example.com/v1?api_key=secret",
		"https://example.com/v1#fragment",
		"http://:8080/v1",
	} {
		ep := mcodeui.CustomEndpoint{BaseURL: raw}
		if _, err := client.SaveCustomEndpoint(context.Background(), ep, "", ""); err == nil {
			t.Fatal("不得持久化含凭据或不支持结构的 URL")
		}
		if _, err := client.TestCustomEndpoint(context.Background(), ep, ""); err == nil {
			t.Fatal("不得测试含凭据或不支持结构的 URL")
		}
	}
}

func TestCustomEndpoints_RegisteredConversationKeepsHistory(t *testing.T) {
	requests := make(chan int, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[]}`)
			return
		}
		var payload struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer saved-key" {
			t.Error("注册后的端点未使用指定地址或凭据")
		}
		requests <- len(payload.Messages)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	backend := remote.NewMemoryBackend(hub)
	defer backend.Close()
	client := NewClient(backend)
	client.SetProviderStore(t.TempDir(), config.NewMemorySecretStore())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ep, err := client.SaveCustomEndpoint(ctx, mcodeui.CustomEndpoint{
		Name: "网关", BaseURL: server.URL + "/v1", Model: "test-model", Enabled: true,
	}, "saved-key", "")
	if err != nil {
		t.Fatal(err)
	}
	backend.AddProject(wire.ProjectSummary{ID: "project", Path: t.TempDir()})
	kind := config.CustomEndpointKind(ep.ID)
	thread, err := backend.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: "project", Options: &wire.ComposerOptions{Provider: &kind},
	})
	if err != nil {
		t.Fatal(err)
	}
	for turn, wantCount := range []int{2, 4} {
		if turn == 1 {
			ep.Name = "重命名的网关"
			if _, err := client.SaveCustomEndpoint(ctx, ep, "", ""); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: thread.ThreadID, Text: "hello"}); err != nil {
			t.Fatal(err)
		}
		select {
		case count := <-requests:
			if count != wantCount {
				t.Fatalf("第 %d 轮消息数量为 %d，期望 %d", turn, count, wantCount)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		for {
			summary, err := backend.GetThread(ctx, thread.ThreadID)
			if err != nil {
				t.Fatal(err)
			}
			if !summary.IsRunning {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Millisecond):
			}
		}
	}
}

func TestCustomEndpoints_MagpieUsesLegacyKind(t *testing.T) {
	client, dataDir, secrets := newProviderSettingsClient(t)
	ctx := context.Background()

	saved, err := client.SaveCustomEndpoint(ctx, mcodeui.CustomEndpoint{
		ID:      "magpie",
		Enabled: true,
		Magpie:  true,
	}, "magpie-key", "")
	if err != nil {
		t.Fatalf("SaveCustomEndpoint failed: %v", err)
	}
	if !saved.Magpie || saved.Name != "Magpie" || saved.BaseURL != config.DefaultMagpieBaseURL {
		t.Fatalf("Magpie 默认值不匹配: %+v", saved)
	}

	providers, err := client.Providers(ctx)
	if err != nil {
		t.Fatalf("client.Providers failed: %v", err)
	}
	found := false
	for _, p := range providers {
		if p.ID == "magpie" {
			found = true
		}
		if p.ID == config.CustomEndpointKind("magpie") {
			t.Fatalf("Magpie 端点不应注册为 %s", p.ID)
		}
	}
	if !found {
		t.Fatalf("Magpie 端点应以原 ID 注册: %+v", providers)
	}

	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Providers.Custom) != 1 || !cfg.Providers.Custom[0].Magpie {
		t.Fatalf("配置未持久化 Magpie 标记: %+v", cfg.Providers.Custom)
	}

	if err := client.DeleteCustomEndpoint(ctx, "magpie"); err != nil {
		t.Fatalf("DeleteCustomEndpoint failed: %v", err)
	}
	if _, err := secrets.Get(config.CustomEndpointSecretAccount("magpie")); !errors.Is(err, config.ErrSecretNotFound) {
		t.Fatalf("删除后密钥应被清除: %v", err)
	}
}

func TestMigrateLegacyMagpieEndpoints(t *testing.T) {
	dataDir := t.TempDir()
	secrets := config.NewMemorySecretStore()
	cfg := config.Defaults()
	cfg.Providers.Presets = map[string]config.PresetProviderConfig{
		"magpie":  {Enabled: true, BaseURL: "http://127.0.0.1:3999/v1", Model: "gpt-5"},
		"magpie2": {},
		"magpie4": {Enabled: true},
	}
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := secrets.Set("magpie1.apiKey", "legacy-1"); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Set("magpie4.apiKey", "legacy-4"); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Set(config.CustomEndpointSecretAccount("magpie"), "existing-key"); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacyMagpieEndpoints(dataDir, &cfg, secrets); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	if len(cfg.Providers.Custom) != 2 {
		t.Fatalf("应迁移 2 个有配置的槽位，实际: %+v", cfg.Providers.Custom)
	}
	byID := make(map[string]config.CustomEndpointConfig, len(cfg.Providers.Custom))
	for _, ep := range cfg.Providers.Custom {
		byID[ep.ID] = ep
	}
	if ep := byID["magpie"]; !ep.Magpie || !ep.Enabled || ep.BaseURL != "http://127.0.0.1:3999/v1" || ep.Model != "gpt-5" || ep.Name != "Magpie" {
		t.Fatalf("magpie 迁移结果不匹配: %+v", ep)
	}
	if ep := byID["magpie4"]; !ep.Magpie || !ep.Enabled || ep.BaseURL != config.DefaultMagpieBaseURL || ep.Model != "" {
		t.Fatalf("magpie4 迁移结果不匹配: %+v", ep)
	}
	if _, ok := cfg.Providers.Presets["magpie"]; ok {
		t.Fatalf("迁移后不应保留 magpie preset")
	}
	if _, ok := cfg.Providers.Presets["magpie2"]; ok {
		t.Fatalf("未配置的 magpie2 preset 也应清理")
	}

	if key, err := secrets.Get(config.CustomEndpointSecretAccount("magpie")); err != nil || key != "existing-key" {
		t.Fatalf("目标已有密钥时应保留目标值: %q, %v", key, err)
	}
	if key, err := secrets.Get(config.CustomEndpointSecretAccount("magpie4")); err != nil || key != "legacy-4" {
		t.Fatalf("magpie4 密钥未迁移: %q, %v", key, err)
	}
	for _, legacy := range []string{"magpie1.apiKey", "magpie4.apiKey"} {
		if _, err := secrets.Get(legacy); !errors.Is(err, config.ErrSecretNotFound) {
			t.Fatalf("旧密钥 %s 应删除: %v", legacy, err)
		}
	}

	persisted, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
	if len(persisted.Providers.Custom) != 2 {
		t.Fatalf("配置文件未保存迁移结果: %+v", persisted.Providers.Custom)
	}

	// 重复执行保持幂等：不新增条目，也不改动已有设置。
	if err := migrateLegacyMagpieEndpoints(dataDir, &persisted, secrets); err != nil {
		t.Fatalf("second migrate failed: %v", err)
	}
	if len(persisted.Providers.Custom) != 2 {
		t.Fatalf("重复迁移不应新增条目: %+v", persisted.Providers.Custom)
	}
	again, err := config.Load(dataDir)
	if err != nil || len(again.Providers.Custom) != 2 {
		t.Fatalf("重复迁移改动了配置: %+v, %v", again.Providers.Custom, err)
	}

	if got := config.CustomEndpointProviderKind(byID["magpie"]); got != "magpie" {
		t.Fatalf("Magpie 端点应沿用旧 Kind，got %q", got)
	}
	if got := config.CustomEndpointProviderKind(config.CustomEndpointConfig{ID: "magpie3", Magpie: true}); got != "magpie3" {
		t.Fatalf("Magpie 端点应沿用旧 Kind，got %q", got)
	}
	if got := config.CustomEndpointProviderKind(config.CustomEndpointConfig{ID: "gateway", Magpie: true}); got != "custom-gateway" {
		t.Fatalf("非旧槽位应使用 custom- 前缀，got %q", got)
	}
}

func TestCustomEndpoints_SavePreservesPiModels(t *testing.T) {
	dataDir := t.TempDir()
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	backend := remote.NewMemoryBackend(hub)
	defer backend.Close()

	client := NewClient(backend)
	client.SetProviderStore(dataDir, config.NewMemorySecretStore())
	ctx := context.Background()

	saved, err := client.SaveCustomEndpoint(ctx, mcodeui.CustomEndpoint{
		Name:    "Magpie",
		BaseURL: config.DefaultMagpieBaseURL,
		Enabled: true,
		Magpie:  true,
	}, "sk-magpie", "")
	if err != nil {
		t.Fatalf("SaveCustomEndpoint failed: %v", err)
	}
	// pi 注入路径单独写入的模型勾选不受端点编辑影响。
	if err := client.SetPiMagpieModels(saved.ID, []string{"gpt-image-1"}); err != nil {
		t.Fatalf("SetPiMagpieModels failed: %v", err)
	}
	if _, err := client.SaveCustomEndpoint(ctx, mcodeui.CustomEndpoint{
		ID:      saved.ID,
		Name:    "Magpie Renamed",
		BaseURL: config.DefaultMagpieBaseURL,
		Enabled: true,
		Magpie:  true,
	}, "", ""); err != nil {
		t.Fatalf("SaveCustomEndpoint rename failed: %v", err)
	}

	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Providers.Custom) != 1 {
		t.Fatalf("expected 1 endpoint, got %+v", cfg.Providers.Custom)
	}
	if got := cfg.Providers.Custom[0].PiModels; len(got) != 1 || got[0] != "gpt-image-1" {
		t.Fatalf("编辑端点不应清空 PiModels，得到 %+v", got)
	}
}

// TestCustomEndpoints_PersonaSources 覆盖人设来源字段的保存：web 密钥的保存、保留、
// 覆盖、清空与删除端点时一并删除，以及 CustomEndpoints 能否读回三个字段。
func TestCustomEndpoints_PersonaSources(t *testing.T) {
	client, dataDir, secrets := newProviderSettingsClient(t)
	ctx := context.Background()
	webAccount := config.CustomEndpointWebKeySecretAccount("magpie")

	base := mcodeui.CustomEndpoint{
		ID:            "magpie",
		Name:          "Magpie",
		BaseURL:       config.DefaultMagpieBaseURL,
		Enabled:       true,
		Magpie:        true,
		PersonaWebURL: "https://magpie.example.com:3430/",
		PersonasJSON:  `{"personas":[{"id":"coder","name":"编码"}]}`,
		PersonaRules:  true,
	}

	// 1. 保存 web 地址与 web 密钥：地址去末尾斜杠，三个来源字段与 HasWebKey 都回来。
	saved, err := client.SaveCustomEndpoint(ctx, base, "sk-magpie", "web-secret-1")
	if err != nil {
		t.Fatalf("SaveCustomEndpoint failed: %v", err)
	}
	if saved.PersonaWebURL != "https://magpie.example.com:3430" {
		t.Fatalf("web 管理地址应去末尾斜杠，得到 %q", saved.PersonaWebURL)
	}
	if !saved.HasWebKey || !saved.PersonaRules || saved.PersonasJSON == "" {
		t.Fatalf("人设来源字段保存后不完整: %+v", saved)
	}
	if got, err := secrets.Get(webAccount); err != nil || got != "web-secret-1" {
		t.Fatalf("web 密钥应写入密钥库: val=%q, err=%v", got, err)
	}
	rawConfig, err := os.ReadFile(config.ConfigPath(dataDir))
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	if strings.Contains(string(rawConfig), "web-secret-1") {
		t.Fatal("配置文件不得包含 web 管理密钥明文")
	}

	// 2. webKey 为空表示保留已保存的密钥。
	saved, err = client.SaveCustomEndpoint(ctx, base, "", "")
	if err != nil {
		t.Fatalf("保留 web 密钥失败: %v", err)
	}
	if !saved.HasWebKey {
		t.Fatal("webKey 为空且地址未变时应保留已保存的密钥")
	}
	if got, err := secrets.Get(webAccount); err != nil || got != "web-secret-1" {
		t.Fatalf("web 密钥被误改: val=%q, err=%v", got, err)
	}

	// 3. CustomEndpoints 读回三个来源字段。
	list, err := client.CustomEndpoints(ctx)
	if err != nil {
		t.Fatalf("CustomEndpoints failed: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("端点数 = %d，想要 1", len(list))
	}
	got := list[0]
	if got.PersonaWebURL != "https://magpie.example.com:3430" || got.PersonasJSON != base.PersonasJSON ||
		!got.PersonaRules || !got.HasWebKey {
		t.Fatalf("CustomEndpoints 没读回人设来源: %+v", got)
	}

	// 4. 非空 webKey 覆盖已保存的密钥。
	if _, err := client.SaveCustomEndpoint(ctx, base, "", "web-secret-2"); err != nil {
		t.Fatalf("覆盖 web 密钥失败: %v", err)
	}
	if got, err := secrets.Get(webAccount); err != nil || got != "web-secret-2" {
		t.Fatalf("web 密钥应被覆盖: val=%q, err=%v", got, err)
	}

	// 5. 管理地址清空时删掉已保存的 web 密钥。
	cleared := base
	cleared.PersonaWebURL = ""
	saved, err = client.SaveCustomEndpoint(ctx, cleared, "", "")
	if err != nil {
		t.Fatalf("清空 web 地址失败: %v", err)
	}
	if saved.HasWebKey {
		t.Fatal("web 地址清空后不该还有 web 密钥")
	}
	if _, err := secrets.Get(webAccount); err == nil {
		t.Fatal("web 地址清空后应删除密钥库里的 web 密钥")
	}

	// 6. 删除端点同样删 web 密钥。
	saved, err = client.SaveCustomEndpoint(ctx, base, "", "web-secret-3")
	if err != nil {
		t.Fatalf("重新保存 web 来源失败: %v", err)
	}
	if err := client.DeleteCustomEndpoint(ctx, saved.ID); err != nil {
		t.Fatalf("DeleteCustomEndpoint failed: %v", err)
	}
	if _, err := secrets.Get(webAccount); err == nil {
		t.Fatal("删除端点时应一并删除 web 密钥")
	}
}

// TestCustomEndpoints_RejectsBadPersonaSources 覆盖人设来源的校验：非法 web 管理地址与
// 非法人设列表都不得落盘，非 Magpie 端点的人设字段顺手清掉。
func TestCustomEndpoints_RejectsBadPersonaSources(t *testing.T) {
	client, _, _ := newProviderSettingsClient(t)
	ctx := context.Background()

	for _, raw := range []string{
		"ftp://magpie.example.com",
		"https://user:secret@magpie.example.com:3430",
		"https://magpie.example.com:3430?x=1",
		"magpie.example.com:3430",
	} {
		ep := mcodeui.CustomEndpoint{
			ID: "magpie", Name: "Magpie", BaseURL: config.DefaultMagpieBaseURL,
			Enabled: true, Magpie: true, PersonaWebURL: raw,
		}
		if _, err := client.SaveCustomEndpoint(ctx, ep, "", "web-secret"); err == nil {
			t.Fatalf("非法 web 管理地址 %q 应被拒绝", raw)
		}
	}
	if _, err := client.SaveCustomEndpoint(ctx, mcodeui.CustomEndpoint{
		ID: "magpie", Name: "Magpie", BaseURL: config.DefaultMagpieBaseURL,
		Enabled: true, Magpie: true, PersonasJSON: `{"personas":`,
	}, "", ""); err == nil {
		t.Fatal("非法人设列表应被拒绝")
	}

	// 非 Magpie 端点用不上人设来源，三个字段与 web 密钥都不该留下。
	saved, err := client.SaveCustomEndpoint(ctx, mcodeui.CustomEndpoint{
		Name: "Plain", BaseURL: "http://localhost:11434/v1", Enabled: true,
		PersonaWebURL: "https://magpie.example.com:3430",
		PersonasJSON:  `[{"id":"coder"}]`,
		PersonaRules:  true,
	}, "", "web-secret")
	if err != nil {
		t.Fatalf("SaveCustomEndpoint failed: %v", err)
	}
	if saved.PersonaWebURL != "" || saved.PersonasJSON != "" || saved.PersonaRules || saved.HasWebKey {
		t.Fatalf("非 Magpie 端点的人设来源应被清空: %+v", saved)
	}
}
