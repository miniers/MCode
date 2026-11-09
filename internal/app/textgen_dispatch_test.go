package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"mcode/internal/agent/llm"
	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/providers/claude"
	"mcode/internal/providers/codex"
	"mcode/internal/providers/pi"
	"mcode/internal/textgen"
)

// textgenFakeSecrets 是仅用于文本生成测试的凭据存储，只对一个账号返回密钥。
type textgenFakeSecrets struct {
	account string
	key     string
}

func TestCLITextgenOptionsMagpie(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			cfg := magpieTestConfig(config.CustomEndpointConfig{
				ID: "gateway", Name: "Magpie Gateway", Enabled: true, Magpie: true, BaseURL: "http://localhost:1234/v1",
			})
			cfg.Providers.Claude.Bin, cfg.Providers.Codex.Bin = "claude-test", "codex-test"
			secrets := magpieTestSecrets(t, map[string]string{"gateway": "secret-key"})
			setting := config.MagpieAgentConfig{Selections: []config.MagpieSelection{{EndpointID: "gateway", Models: []string{"vendor/model"}}}}
			for _, mode := range []config.MagpieMode{config.MagpieModeOff, config.MagpieModeInject, config.MagpieModeGlobal} {
				t.Run(string(mode), func(t *testing.T) {
					setting.Mode = mode
					cfg.Providers.MagpieAgents = map[string]config.MagpieAgentConfig{provider: setting}
					ref := "magpie-gateway/vendor/model"
					opts, err := cliTextgenOptions(cfg, secrets, provider, ref, "high")
					if err != nil {
						t.Fatal(err)
					}
					wantModel := ref
					if mode == config.MagpieModeInject {
						wantModel = "vendor/model"
					}
					if opts.Model != wantModel || opts.Provider != provider || opts.Effort != "high" {
						t.Fatalf("选项 = %+v", opts)
					}
					var wantEnv, wantArgs []string
					if mode == config.MagpieModeInject {
						if provider == "claude" {
							wantEnv = claude.MagpieEnv("http://localhost:1234/v1", "secret-key")
						} else {
							route := codex.MagpieRoute{ProviderID: "magpie-gateway", BaseURL: "http://localhost:1234/v1", APIKey: "secret-key"}
							wantEnv, wantArgs = codex.MagpieEnv(route), codex.MagpieArgs(route)
						}
					}
					if !reflect.DeepEqual(opts.ExtraEnv, wantEnv) || !reflect.DeepEqual(opts.ExtraArgs, wantArgs) || strings.Contains(strings.Join(opts.ExtraArgs, " "), "secret-key") {
						t.Fatalf("注入参数或环境不正确: %+v", opts)
					}
					if mode == config.MagpieModeGlobal {
						got, err := cliTextgenOptions(cfg, secrets, provider, "vendor/model", "")
						if err != nil || got.Model != "vendor/model" || len(got.ExtraEnv) != 0 || len(got.ExtraArgs) != 0 {
							t.Errorf("全局模式选项 = %+v，错误 = %v", got, err)
						}
					}
					for _, input := range []string{"", "default"} {
						got, err := cliTextgenOptions(cfg, secrets, provider, input, "")
						if err != nil {
							t.Fatal(err)
						}
						want := input
						if mode == config.MagpieModeInject || (mode == config.MagpieModeGlobal && input == "") {
							want = "vendor/model"
						}
						if got.Model != want {
							t.Errorf("模型 %q = %q，期望 %q", input, got.Model, want)
						}
					}
				})
			}
			setting.Mode = config.MagpieModeInject
			cfg.Providers.MagpieAgents[provider] = setting
			for _, ref := range []string{"magpie-gateway/unselected", "magpie-gateway/", "magpie-deleted/model"} {
				if _, err := cliTextgenOptions(cfg, secrets, provider, ref, ""); err == nil {
					t.Errorf("%q 应返回错误", ref)
				}
			}
			if _, err := cliTextgenOptions(cfg, nil, provider, "magpie-gateway/vendor/model", ""); err == nil {
				t.Error("缺少密钥时应返回错误")
			}
			cfg.Providers.MagpieAgents[provider] = config.MagpieAgentConfig{Mode: config.MagpieModeInject}
			if _, err := cliTextgenOptions(cfg, secrets, provider, "magpie-gateway/vendor/model", ""); err == nil {
				t.Error("未选择端点时应返回错误")
			}
			cfg.Providers.MagpieAgents[provider] = setting
			cfg.Providers.Custom[0].Enabled = false
			if _, err := cliTextgenOptions(cfg, secrets, provider, "magpie-gateway/vendor/model", ""); err == nil {
				t.Error("端点关闭时应返回错误")
			}
			if got, err := cliTextgenOptions(cfg, secrets, provider, "native/model", ""); err != nil || got.Model != "native/model" || len(got.ExtraEnv) != 0 {
				t.Errorf("原生模型选项 = %+v，错误 = %v", got, err)
			}
		})
	}
}

func (f textgenFakeSecrets) Get(account string) (string, error) {
	if account == f.account {
		return f.key, nil
	}
	return "", errors.New("secret not found")
}

func (f textgenFakeSecrets) Set(string, string) error { return nil }

func (f textgenFakeSecrets) Delete(string) error { return nil }

// TestGenerateTextCustomEndpoint 验证自定义端点走 OpenAI 兼容接口并按注册 Kind 解析密钥。
func TestGenerateTextCustomEndpoint(t *testing.T) {
	var gotPath, gotAuth, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("解析请求体失败: %v", err)
		}
		gotModel = body.Model
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\" hi \"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Providers.Custom = []config.CustomEndpointConfig{{
		ID:      "x",
		BaseURL: srv.URL,
		Model:   "m1",
		Enabled: true,
	}}
	secrets := textgenFakeSecrets{
		account: config.CustomEndpointSecretAccount("x"),
		key:     "k1",
	}

	out, err := generateText(context.Background(), cfg, secrets, config.CustomEndpointKind("x"), "", "", "p")
	if err != nil {
		t.Fatalf("generateText 返回错误: %v", err)
	}
	if out != "hi" {
		t.Fatalf("输出 = %q，期望 %q", out, "hi")
	}
	if !strings.HasSuffix(gotPath, "/chat/completions") {
		t.Errorf("请求路径 = %q，期望以 /chat/completions 结尾", gotPath)
	}
	if gotAuth != "Bearer k1" {
		t.Errorf("Authorization = %q，期望 %q", gotAuth, "Bearer k1")
	}
	if gotModel != "m1" {
		t.Errorf("请求体 model = %q，期望 %q", gotModel, "m1")
	}
}

// TestGenerateTextFallsBackToClaude 验证未知 provider 回退到 claude CLI。
func TestGenerateTextFallsBackToClaude(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows 不支持 shell 脚本测试")
	}

	bin := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\ncat >/dev/null\necho fallback\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("写入测试脚本失败: %v", err)
	}

	cfg := config.Defaults()
	cfg.Providers.Claude.Bin = bin
	out, err := generateText(context.Background(), cfg, nil, "some-unknown-provider", "", "", "p")
	if err != nil {
		t.Fatalf("generateText 返回错误: %v", err)
	}
	if out != "fallback" {
		t.Fatalf("输出 = %q，期望 %q", out, "fallback")
	}
}

// TestGenerateTextMemory 验证 memory provider 直接返回本地模拟结果。
func TestGenerateTextMemory(t *testing.T) {
	out, err := generateText(context.Background(), config.Defaults(), nil, "memory", "", "", "你好")
	if err != nil {
		t.Fatalf("generateText 返回错误: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("memory provider 返回了空结果")
	}
}

// TestTextgenModelSelection 验证提交信息与会话标题共用同一套模型选择：
// 设置页指定模型优先；未指定时不读角色模型，直接回退通用 provider（model 留空）。
func TestTextgenModelSelection(t *testing.T) {
	cfg := config.Defaults()
	cfg.Models.Provider = "claude"
	cfg.Models.Roles.Utility = config.ModelRoleConfig{Provider: "codex", Model: "gpt-5.4-mini"}

	// 未指定提交信息模型：即使配置了 utility 角色，也不再读它。
	provider, model, effort := textgenModelSelection(cfg)
	if provider != "claude" || model != "" || effort != "" {
		t.Fatalf("未指定时的结果 = (%q, %q, %q)，期望 (claude, \"\", \"\")", provider, model, effort)
	}

	// 指定提交信息模型：包括 effort 在内全部覆盖。
	cfg.SourceControl.CommitMessageModel = config.ModelSelectionConfig{
		Provider: " pi ",
		Model:    " claude-sonnet ",
		Effort:   " high ",
	}
	provider, model, effort = textgenModelSelection(cfg)
	if provider != "pi" || model != "claude-sonnet" || effort != "high" {
		t.Fatalf("显式模型结果 = (%q, %q, %q)，期望 (pi, claude-sonnet, high)", provider, model, effort)
	}

	// 角色与提交信息模型都未配置：回退通用 provider，model 留空表示用默认模型。
	cfg = config.Defaults()
	cfg.Models.Provider = "claude"
	cfg.Models.Roles.Utility = config.ModelRoleConfig{}
	provider, model, effort = textgenModelSelection(cfg)
	if provider != "claude" || model != "" || effort != "" {
		t.Fatalf("通用回退结果 = (%q, %q, %q)，期望 (claude, \"\", \"\")", provider, model, effort)
	}
}

// TestResolveTextgenSourceDisabled 验证未启用的自定义端点返回错误。
func TestResolveTextgenSourceDisabled(t *testing.T) {
	cfg := config.Defaults()
	cfg.Providers.Custom = []config.CustomEndpointConfig{{
		ID:      "x",
		BaseURL: "http://127.0.0.1:1/v1",
		Model:   "m1",
		Enabled: false,
	}}

	if _, ok, err := resolveTextgenSource(cfg, nil, config.CustomEndpointKind("x")); !ok || err == nil {
		t.Fatal("未启用的自定义端点应返回错误")
	}
}

// TestResolveTextgenSourceMagpie 验证 Magpie 自定义端点解析。
func TestResolveTextgenSourceMagpie(t *testing.T) {
	cfg := config.Defaults()
	cfg.Providers.Custom = []config.CustomEndpointConfig{{
		ID:      "magpie",
		BaseURL: "http://127.0.0.1:9/v1",
		Model:   "mm",
		Enabled: true,
		Magpie:  true,
	}}

	target, ok, err := resolveTextgenSource(cfg, nil, "magpie")
	if !ok || err != nil {
		t.Fatal("Magpie 自定义端点应返回 ok")
	}
	if target.BaseURL != "http://127.0.0.1:9/v1" {
		t.Errorf("baseURL = %q，期望 %q", target.BaseURL, "http://127.0.0.1:9/v1")
	}
	if target.DefaultModel != "mm" {
		t.Errorf("model = %q，期望 %q", target.DefaultModel, "mm")
	}
}

func TestGenerateTextAPISources(t *testing.T) {
	var path, auth, apiKey, extra string
	var body map[string]any
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		path, auth, apiKey, extra = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("x-api-key"), r.Header.Get("X-Test-Preset")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"limited"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.HasSuffix(path, "/messages") {
			_, _ = w.Write([]byte("data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" hi \"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"))
		} else {
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\" hi \"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
		}
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.Providers.Custom = []config.CustomEndpointConfig{{ID: "x", Enabled: true, BaseURL: server.URL + "/v1/chat/completions", Model: "custom-model"}, {ID: "magpie", Enabled: true, Magpie: true, BaseURL: server.URL + "/v1"}}
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("magpie"): "mag-key", "anthropic.apiKey": "anth-key", "deepseek.apiKey": "deep-key"})
	for _, model := range []string{"", "default"} {
		out, err := generateText(context.Background(), cfg, nil, strings.ToUpper(config.CustomEndpointKind("x")), model, "", "p")
		if err != nil || out != "hi" || path != "/v1/chat/completions" || body["model"] != "custom-model" || auth != "" {
			t.Fatalf("自定义端点: %q %v %q %v %q", out, err, path, body, auth)
		}
	}
	t.Setenv("ANTHROPIC_BASE_URL", server.URL)
	out, err := generateText(context.Background(), cfg, secrets, "anthropic", "claude-test", "", "p")
	if err != nil || out != "hi" || path != "/v1/messages" || apiKey != "anth-key" {
		t.Fatalf("Anthropic: %q %v %q %q", out, err, path, apiKey)
	}
	out, err = generateText(context.Background(), cfg, secrets, "magpie", "claude-test", "", "p")
	if err != nil || out != "hi" || path != "/v1/messages" || auth != "Bearer mag-key" {
		t.Fatalf("Magpie: %q %v %q %q", out, err, path, auth)
	}
	oldHeaders := apisource.DeepSeek.ExtraHeaders
	apisource.DeepSeek.ExtraHeaders = map[string]string{"X-Test-Preset": "preset"}
	t.Cleanup(func() { apisource.DeepSeek.ExtraHeaders = oldHeaders })
	if cfg.Providers.Presets == nil {
		cfg.Providers.Presets = make(map[string]config.PresetProviderConfig)
	}
	pc := cfg.Providers.Presets["deepseek"]
	pc.Enabled, pc.BaseURL = true, server.URL
	cfg.Providers.Presets["deepseek"] = pc
	out, err = generateText(context.Background(), cfg, secrets, "deepseek", "deepseek-reasoner", "high", "p")
	if err != nil || out != "hi" || extra != "preset" {
		t.Fatalf("预设: %q %v %q", out, err, extra)
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Fatalf("DeepSeek reasoner 不应发送档位: %v", body)
	}
	t.Setenv("DEEPSEEK_API_KEY", "")
	if _, err := generateText(context.Background(), cfg, nil, "deepseek", "model", "", "p"); err == nil {
		t.Fatal("缺少密钥应报错")
	}
	cfg.Providers.Custom[0].Enabled = false
	if _, err := generateText(context.Background(), cfg, nil, config.CustomEndpointKind("x"), "model", "", "p"); err == nil {
		t.Fatal("停用来源应报错")
	}
	status = http.StatusTooManyRequests
	_, err = generateText(context.Background(), cfg, secrets, "deepseek", "model", "", "p")
	var apiErr *llm.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 429 {
		t.Fatalf("429 错误 = %v", err)
	}
}

// TestCLITextgenAvailableConfiguredBin 验证配置的 Bin 不存在时不再视为可用。
func TestCLITextgenAvailableConfiguredBin(t *testing.T) {
	restore := textgen.SetLookPathForTest(func(string) (string, error) { return "", os.ErrNotExist })
	defer restore()
	dir := t.TempDir()
	cfg := config.Config{}
	cfg.Providers.Claude.Bin = filepath.Join(dir, "missing-claude")
	if cliTextgenAvailable(cfg, "claude") {
		t.Fatal("不存在的 Bin 不应视为可用")
	}
	existing := filepath.Join(dir, "claude")
	if err := os.WriteFile(existing, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.Providers.Claude.Bin = existing
	if !cliTextgenAvailable(cfg, "claude") {
		t.Fatal("存在的 Bin 应视为可用")
	}

	restorePath := textgen.SetLookPathForTest(func(string) (string, error) { return "/usr/bin/claude", nil })
	defer restorePath()
	cfg.Providers.Claude.Bin = ""
	if !cliTextgenAvailable(cfg, "claude") {
		t.Fatal("Bin 为空时应在 PATH 中找到")
	}
}

// TestPiTextgenOptionsMagpieInjection 验证 pi 一次性文本生成只在拿到桥路径与端点清单时注入。
func TestPiTextgenOptionsMagpieInjection(t *testing.T) {
	cases := []struct {
		name       string
		bridge     string
		env        string
		wantExt    []string
		wantEnvLen int
	}{
		{"桥与清单都有", "/tmp/bridge.ts", `[{"name":"m1"}]`, []string{"/tmp/bridge.ts"}, 2},
		{"只有桥", "/tmp/bridge.ts", "", nil, 0},
		{"只有清单", "", `[{"name":"m1"}]`, nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := textgenPiMagpieInjection
			textgenPiMagpieInjection = func() (string, string) { return tc.bridge, tc.env }
			t.Cleanup(func() { textgenPiMagpieInjection = original })

			opts := piTextgenOptions(config.Defaults(), "magpie-jp/auto/x", "high")
			if opts.Provider != "pi" {
				t.Errorf("Provider = %q，期望 %q", opts.Provider, "pi")
			}
			if opts.Model != "magpie-jp/auto/x" {
				t.Errorf("Model = %q，期望 %q", opts.Model, "magpie-jp/auto/x")
			}
			if opts.Effort != "high" {
				t.Errorf("Effort = %q，期望 %q", opts.Effort, "high")
			}
			if len(opts.Extensions) != len(tc.wantExt) {
				t.Fatalf("Extensions = %q，期望 %q", opts.Extensions, tc.wantExt)
			}
			for i, want := range tc.wantExt {
				if opts.Extensions[i] != want {
					t.Errorf("Extensions[%d] = %q，期望 %q", i, opts.Extensions[i], want)
				}
			}
			if len(opts.ExtraEnv) != tc.wantEnvLen {
				t.Fatalf("ExtraEnv = %q，期望 %d 项", opts.ExtraEnv, tc.wantEnvLen)
			}
			if tc.wantEnvLen == 0 {
				return
			}
			if want := pi.MagpieEnvVar + "=" + tc.env; opts.ExtraEnv[0] != want {
				t.Errorf("ExtraEnv[0] = %q，期望 %q", opts.ExtraEnv[0], want)
			}
			if want := pi.MagpieBridgeEnvVar + "=" + tc.bridge; opts.ExtraEnv[1] != want {
				t.Errorf("ExtraEnv[1] = %q，期望 %q", opts.ExtraEnv[1], want)
			}
		})
	}
}
