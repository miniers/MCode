package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"mcode/internal/config"
	"mcode/internal/providers/magpie"
	mcodeui "mcode/internal/ui"
)

// personaTestConfig 是本文件用的配置：一个启用的 Magpie 端点、一个没启用的 Magpie
// 端点和一个非 Magpie 的自定义端点。
func personaTestConfig() config.Config {
	return config.Config{Providers: config.ProvidersConfig{Custom: []config.CustomEndpointConfig{
		{
			ID:       "magpie",
			Name:     "Magpie",
			BaseURL:  "http://127.0.0.1:3425/v1",
			Enabled:  true,
			Magpie:   true,
			PiModels: []string{"auto"},
		},
		{
			ID:      "magpie2",
			Name:    "Gemini",
			BaseURL: "http://127.0.0.1:3426/v1",
			Magpie:  true,
		},
		{
			ID:      "openrouter",
			Name:    "OpenRouter",
			BaseURL: "https://openrouter.ai/api/v1",
			Enabled: true,
		},
		// BaseURL 留空：按默认本机地址处理。
		{
			ID:      "magpie3",
			Name:    "Backup",
			Enabled: true,
			Magpie:  true,
		},
	}}}
}

// usePersonaConfigDir 把 magpie 的配置目录指向 dir，测试结束自动恢复。
func usePersonaConfigDir(t *testing.T, dir string) {
	t.Helper()
	restore := magpie.SetConfigDirForTest(dir)
	t.Cleanup(restore)
}

// resetPersonaCache 清空 magpie 的 web 人设缓存：httptest 的端口会被复用，不清可能
// 读到上一条用例的结果。
func resetPersonaCache(t *testing.T) {
	t.Helper()
	magpie.InvalidatePersonaCache("")
	t.Cleanup(func() { magpie.InvalidatePersonaCache("") })
}

// writePersonaPlugins 写出 persona-rules 插件的 plugins.json；personas 为 nil 时写出
// 一个没有 persona-rules 条目的插件列表（模拟 magpie 上没装插件）。
func writePersonaPlugins(t *testing.T, dir string, personas []map[string]any) {
	t.Helper()
	plugins := []map[string]any{}
	if personas != nil {
		plugins = append(plugins, map[string]any{
			"spec":    "@magpie-community/middleware-persona-rules@latest",
			"options": map[string]any{"personas": personas},
		})
	}
	raw, err := json.Marshal(map[string]any{"plugins": plugins})
	if err != nil {
		t.Fatalf("生成 plugins.json 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugins.json"), raw, 0o600); err != nil {
		t.Fatalf("写入 plugins.json 失败: %v", err)
	}
}

// writePersonaPluginsOff 写出 persona-rules 被关掉的 plugins.json。
func writePersonaPluginsOff(t *testing.T, dir string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"plugins": []map[string]any{
		{"spec": "@magpie-community/middleware-persona-rules@latest", "off": true},
	}})
	if err != nil {
		t.Fatalf("生成 plugins.json 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugins.json"), raw, 0o600); err != nil {
		t.Fatalf("写入 plugins.json 失败: %v", err)
	}
}

// personaWebServer 起一个模拟 magpie web 管理接口的服务器：与 magpie 一样先 303 到去掉
// k 的同一路径并种 cookie，之后只认 cookie。hits 统计 /api/plugins 被请求的次数。
type personaWebServer struct {
	*httptest.Server
	hits *int32
}

// newPersonaWebServer 起服务；body 是拿到 cookie 后返回的插件列表 JSON。
func newPersonaWebServer(t *testing.T, key, body string) personaWebServer {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/plugins" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&hits, 1)
		if got := r.URL.Query().Get("k"); got != "" {
			if got != key {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "magpie", Value: key, Path: "/"})
			target := *r.URL
			query := target.Query()
			query.Del("k")
			target.RawQuery = query.Encode()
			http.Redirect(w, r, target.String(), http.StatusSeeOther)
			return
		}
		if cookie, err := r.Cookie("magpie"); err != nil || cookie.Value != key {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return personaWebServer{Server: srv, hits: &hits}
}

// personaIDs 取出人设 ID，便于只比较关心的部分。
func personaIDs(personas []magpie.Persona) []string {
	out := make([]string, 0, len(personas))
	for _, p := range personas {
		out = append(out, p.ID)
	}
	return out
}

// personaWebPluginsJSON 拼出 web 接口返回的插件列表（persona-rules 插件在第一位）。
func personaWebPluginsJSON(t *testing.T, personas []map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"plugins": []map[string]any{
		{
			"spec":    "@magpie-community/middleware-persona-rules@latest",
			"options": map[string]any{"personas": personas},
		},
	}})
	if err != nil {
		t.Fatalf("生成插件列表失败: %v", err)
	}
	return string(raw)
}

func TestPersonaSourceFor(t *testing.T) {
	cfg := personaTestConfig()
	targets := piMagpieTargets(cfg, nil)
	if len(targets) != 2 {
		t.Fatalf("启用的 Magpie 端点数 = %d，想要 2", len(targets))
	}
	piProviderID := targets[0].providerID

	cases := []struct {
		name     string
		provider string
		model    string
		want     string
		found    bool
	}{
		{
			name:     "自定义端点 kind",
			provider: "magpie",
			want:     "http://127.0.0.1:3425/v1",
			found:    true,
		},
		{
			name:     "pi 线程按模型首段命中",
			provider: "pi",
			model:    piProviderID + "/auto/deepseek-v4",
			want:     "http://127.0.0.1:3425/v1",
			found:    true,
		},
		{
			// mcode 与 pi 共用模型串格式，同一份解析同样适用。
			name:     "mcode 线程按模型首段命中",
			provider: "mcode",
			model:    piProviderID + "/auto/deepseek-v4",
			want:     "http://127.0.0.1:3425/v1",
			found:    true,
		},
		{
			name:     "pi 线程按 pi:<provider id> 命中",
			provider: "pi:" + piProviderID,
			want:     "http://127.0.0.1:3425/v1",
			found:    true,
		},
		{
			name:     "pi 线程用的是别的 provider",
			provider: "pi",
			model:    "anthropic/claude-sonnet-5",
		},
		{
			name:     "pi 线程模型没有分段",
			provider: "pi",
			model:    "auto",
		},
		{
			name:     "非 Magpie 的自定义端点",
			provider: "custom-openrouter",
		},
		{
			name:     "未启用的 Magpie 端点",
			provider: "magpie2",
		},
		{
			// BaseURL 为空时与 piMagpieTargets 一样用默认地址。
			name:     "端点没配 baseURL",
			provider: "magpie3",
			want:     config.DefaultMagpieBaseURL,
			found:    true,
		},
		{
			name:     "provider 为空",
			provider: "",
		},
		{
			name:     "别的 provider",
			provider: "claude",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := personaSourceFor(cfg, tc.provider, tc.model)
			if found != tc.found {
				t.Fatalf("found = %v，想要 %v", found, tc.found)
			}
			if got.baseURL != tc.want {
				t.Fatalf("baseURL = %q，想要 %q", got.baseURL, tc.want)
			}
		})
	}
}

func TestPersonaSourceForCarriesSources(t *testing.T) {
	cfg := config.Config{Providers: config.ProvidersConfig{Custom: []config.CustomEndpointConfig{
		{
			ID:            "magpie",
			Name:          "Magpie",
			BaseURL:       "http://127.0.0.1:3425/v1",
			Enabled:       true,
			Magpie:        true,
			PersonaWebURL: "https://magpie.example.com:3430",
			PersonasJSON:  `{"personas":[{"id":"coder"}]}`,
			PersonaRules:  true,
		},
	}}}
	got, ok := personaSourceFor(cfg, "magpie", "")
	if !ok {
		t.Fatal("personaSourceFor 应该命中启用的 Magpie 端点")
	}
	if got.endpointID != "magpie" || got.webURL != "https://magpie.example.com:3430" ||
		got.manualJSON != `{"personas":[{"id":"coder"}]}` || !got.personaRules {
		t.Fatalf("人设来源字段没有带上: %+v", got)
	}
}

func TestPersonasForPriority(t *testing.T) {
	const webKey = "web-key-for-test"
	resetPersonaCache(t)

	t.Run("本机来源优先于 web", func(t *testing.T) {
		dir := t.TempDir()
		usePersonaConfigDir(t, dir)
		writePersonaPlugins(t, dir, []map[string]any{
			{"id": "local-coder", "name": "本机", "text": "你是资深工程师"},
		})
		srv := newPersonaWebServer(t, webKey, personaWebPluginsJSON(t, []map[string]any{
			{"id": "web-coder", "name": "远端", "text": "你是资深工程师"},
		}))
		cfg := config.Config{Providers: config.ProvidersConfig{Custom: []config.CustomEndpointConfig{
			{
				ID: "magpie", Name: "Magpie", BaseURL: "http://127.0.0.1:3425/v1",
				Enabled: true, Magpie: true, PersonaWebURL: srv.URL, PersonaRules: true,
			},
		}}}
		secrets := config.NewMemorySecretStore()
		_ = secrets.Set(config.CustomEndpointWebKeySecretAccount("magpie"), webKey)

		got := personasFor(context.Background(), cfg, secrets, "magpie", "")
		if want := []string{"local-coder"}; strings.Join(personaIDs(got), ",") != strings.Join(want, ",") {
			t.Fatalf("personasFor = %v，想要 %v", personaIDs(got), want)
		}
		if atomic.LoadInt32(srv.hits) != 0 {
			t.Fatalf("本机来源命中时不该请求 web 接口，实际请求 %d 次", *srv.hits)
		}
	})

	t.Run("本机来源为空时用 web", func(t *testing.T) {
		usePersonaConfigDir(t, t.TempDir())
		srv := newPersonaWebServer(t, webKey, personaWebPluginsJSON(t, []map[string]any{
			{"id": "web-coder", "name": "远端", "text": "你是资深工程师"},
		}))
		cfg := config.Config{Providers: config.ProvidersConfig{Custom: []config.CustomEndpointConfig{
			{
				ID: "magpie", Name: "Magpie", BaseURL: srv.URL,
				Enabled: true, Magpie: true, PersonaWebURL: srv.URL,
				PersonasJSON: `{"personas":[{"id":"manual-coder"}]}`, PersonaRules: true,
			},
		}}}
		secrets := config.NewMemorySecretStore()
		_ = secrets.Set(config.CustomEndpointWebKeySecretAccount("magpie"), webKey)

		got := personasFor(context.Background(), cfg, secrets, "magpie", "")
		if want := "web-coder"; len(got) != 1 || got[0].ID != want {
			t.Fatalf("personasFor = %v，想要 %s", personaIDs(got), want)
		}
	})

	t.Run("web 成功但空列表不回落手动", func(t *testing.T) {
		usePersonaConfigDir(t, t.TempDir())
		// 插件列表里没有 persona-rules：magpie 上插件已关或不存在，仍算 web 的有效结果。
		srv := newPersonaWebServer(t, webKey, `{"plugins":[]}`)
		cfg := config.Config{Providers: config.ProvidersConfig{Custom: []config.CustomEndpointConfig{
			{
				ID: "magpie", Name: "Magpie", BaseURL: srv.URL,
				Enabled: true, Magpie: true, PersonaWebURL: srv.URL,
				PersonasJSON: `{"personas":[{"id":"manual-coder"}]}`, PersonaRules: true,
			},
		}}}
		secrets := config.NewMemorySecretStore()
		_ = secrets.Set(config.CustomEndpointWebKeySecretAccount("magpie"), webKey)

		if got := personasFor(context.Background(), cfg, secrets, "magpie", ""); len(got) != 0 {
			t.Fatalf("personasFor = %v，想要空（web 的空结果是准的，不该回落手动）", personaIDs(got))
		}
	})

	t.Run("web 出错回落手动", func(t *testing.T) {
		usePersonaConfigDir(t, t.TempDir())
		// 服务器只认别的 key：带错 key 的请求得到 401。
		srv := newPersonaWebServer(t, webKey, `{"plugins":[]}`)
		cfg := config.Config{Providers: config.ProvidersConfig{Custom: []config.CustomEndpointConfig{
			{
				ID: "magpie", Name: "Magpie", BaseURL: srv.URL,
				Enabled: true, Magpie: true, PersonaWebURL: srv.URL,
				PersonasJSON: `{"personas":[{"id":"manual-coder"},{"id":"manual-writer"}]}`,
				PersonaRules: true,
			},
		}}}
		secrets := config.NewMemorySecretStore()
		_ = secrets.Set(config.CustomEndpointWebKeySecretAccount("magpie"), "wrong-key")

		got := personasFor(context.Background(), cfg, secrets, "magpie", "")
		if want := []string{"manual-coder", "manual-writer"}; strings.Join(personaIDs(got), ",") != strings.Join(want, ",") {
			t.Fatalf("personasFor = %v，想要 %v", personaIDs(got), want)
		}
	})

	t.Run("web 没配密钥回落手动", func(t *testing.T) {
		usePersonaConfigDir(t, t.TempDir())
		srv := newPersonaWebServer(t, webKey, `{"plugins":[]}`)
		cfg := config.Config{Providers: config.ProvidersConfig{Custom: []config.CustomEndpointConfig{
			{
				ID: "magpie", Name: "Magpie", BaseURL: srv.URL,
				Enabled: true, Magpie: true, PersonaWebURL: srv.URL,
				PersonasJSON: `{"personas":[{"id":"manual-coder"}]}`, PersonaRules: true,
			},
		}}}
		got := personasFor(context.Background(), cfg, config.NewMemorySecretStore(), "magpie", "")
		if want := "manual-coder"; len(got) != 1 || got[0].ID != want {
			t.Fatalf("personasFor = %v，想要 %s", personaIDs(got), want)
		}
	})

	t.Run("手动列表未勾选 PersonaRules 不生效", func(t *testing.T) {
		usePersonaConfigDir(t, t.TempDir())
		cfg := config.Config{Providers: config.ProvidersConfig{Custom: []config.CustomEndpointConfig{
			{
				ID: "magpie", Name: "Magpie", BaseURL: "http://127.0.0.1:3425/v1",
				Enabled: true, Magpie: true,
				PersonasJSON: `{"personas":[{"id":"manual-coder"}]}`,
			},
		}}}
		if got := personasFor(context.Background(), cfg, config.NewMemorySecretStore(), "magpie", ""); len(got) != 0 {
			t.Fatalf("personasFor = %v，想要空（没声明部署 persona-rules 时手动列表不生效）", personaIDs(got))
		}
	})

	t.Run("非 magpie 返回空", func(t *testing.T) {
		usePersonaConfigDir(t, t.TempDir())
		cfg := personaTestConfig()
		for _, tc := range []struct{ provider, model string }{
			{provider: "custom-openrouter"},
			{provider: "claude"},
			{provider: "pi", model: "anthropic/claude-sonnet-5"},
		} {
			if got := personasFor(context.Background(), cfg, nil, tc.provider, tc.model); len(got) != 0 {
				t.Fatalf("personasFor(%q, %q) = %v，想要空", tc.provider, tc.model, personaIDs(got))
			}
		}
	})
}

func TestPersonaTagFor(t *testing.T) {
	dir := t.TempDir()
	usePersonaConfigDir(t, dir)
	writePersonaPlugins(t, dir, []map[string]any{
		{"id": "coder", "name": "编码", "text": "你是资深工程师"},
		{"id": "writer", "name": "写作", "text": "你是技术作者"},
	})

	cfg := personaTestConfig()
	piProviderID := piMagpieTargets(cfg, nil)[0].providerID

	cases := []struct {
		name     string
		provider string
		model    string
		persona  string
		want     string
	}{
		{name: "未选人设", provider: "magpie", persona: "", want: ""},
		{name: "选了 none", provider: "magpie", persona: "none", want: "[[persona:none]]\n\n"},
		{name: "人设存在", provider: "magpie", persona: "coder", want: "[[persona:coder]]\n\n"},
		{
			name:     "pi 线程的人设存在",
			provider: "pi",
			model:    piProviderID + "/auto/deepseek-v4",
			persona:  "writer",
			want:     "[[persona:writer]]\n\n",
		},
		// 人设被删掉后，历史里残留的旧标签要覆盖成 none，别让旧人设继续生效。
		{name: "人设已不存在", provider: "magpie", persona: "ghost", want: "[[persona:none]]\n\n"},
		{name: "非 Magpie 的 provider", provider: "claude", persona: "coder", want: ""},
		{name: "pi 线程用的是别的 provider", provider: "pi", model: "anthropic/claude-sonnet-5", persona: "coder", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := personaTagFor(cfg, nil, tc.provider, tc.model, tc.persona); got != tc.want {
				t.Fatalf("personaTagFor = %q，想要 %q", got, tc.want)
			}
		})
	}
}

func TestPersonaTagForWithoutPersonas(t *testing.T) {
	// 没有 plugins.json（magpie 没装 persona-rules）时，即便选了人设也不加标签。
	usePersonaConfigDir(t, t.TempDir())
	cfg := personaTestConfig()
	if got := personaTagFor(cfg, nil, "magpie", "", "coder"); got != "" {
		t.Fatalf("personaTagFor = %q，想要空", got)
	}
}

func TestPersonaTagForManualSource(t *testing.T) {
	usePersonaConfigDir(t, t.TempDir())
	cfg := config.Config{Providers: config.ProvidersConfig{Custom: []config.CustomEndpointConfig{
		{
			ID: "magpie", Name: "Magpie", BaseURL: "https://magpie.example.com/v1",
			Enabled: true, Magpie: true,
			PersonasJSON: `{"personas":[{"id":"coder"},{"id":"writer"}]}`, PersonaRules: true,
		},
	}}}
	if got := personaTagFor(cfg, nil, "magpie", "", "writer"); got != "[[persona:writer]]\n\n" {
		t.Fatalf("personaTagFor = %q，想要手动列表里的人设标签", got)
	}
	if got := personaTagFor(cfg, nil, "magpie", "", "ghost"); got != "[[persona:none]]\n\n" {
		t.Fatalf("personaTagFor = %q，想要回落到 none", got)
	}
}

func TestPersonaTagForWebSource(t *testing.T) {
	const webKey = "tag-web-key"
	resetPersonaCache(t)
	usePersonaConfigDir(t, t.TempDir())
	srv := newPersonaWebServer(t, webKey, personaWebPluginsJSON(t, []map[string]any{
		{"id": "web-coder", "name": "远端", "text": "你是资深工程师"},
	}))
	cfg := config.Config{Providers: config.ProvidersConfig{Custom: []config.CustomEndpointConfig{
		{
			ID: "magpie", Name: "Magpie", BaseURL: srv.URL,
			Enabled: true, Magpie: true, PersonaWebURL: srv.URL,
		},
	}}}
	secrets := config.NewMemorySecretStore()
	_ = secrets.Set(config.CustomEndpointWebKeySecretAccount("magpie"), webKey)

	if got := personaTagFor(cfg, secrets, "magpie", "", "web-coder"); got != "[[persona:web-coder]]\n\n" {
		t.Fatalf("personaTagFor = %q，想要 web 人设标签", got)
	}
}

func TestTestMagpiePersonas(t *testing.T) {
	const webKey = "saved-web-key"
	resetPersonaCache(t)

	newClient := func(secrets config.SecretStore) *Client {
		return &Client{secrets: secrets}
	}
	secretsWithKey := func(t *testing.T) config.SecretStore {
		t.Helper()
		secrets := config.NewMemorySecretStore()
		if err := secrets.Set(config.CustomEndpointWebKeySecretAccount("magpie"), webKey); err != nil {
			t.Fatalf("写入 web 密钥失败: %v", err)
		}
		return secrets
	}

	t.Run("web 来源用表单里的密钥", func(t *testing.T) {
		srv := newPersonaWebServer(t, webKey, personaWebPluginsJSON(t, []map[string]any{
			{"id": "coder", "name": "编码", "text": "你是资深工程师"},
			{"id": "writer", "name": "写作", "text": "你是技术作者"},
		}))
		got, err := newClient(config.NewMemorySecretStore()).TestMagpiePersonas(context.Background(),
			mcodeui.CustomEndpoint{PersonaWebURL: srv.URL}, webKey)
		if err != nil {
			t.Fatalf("TestMagpiePersonas 出错: %v", err)
		}
		if !got.OK || got.Source != "web" || got.Count != 2 {
			t.Fatalf("结果 = %+v，想要 web 成功 2 条", got)
		}
		if !strings.Contains(got.Message, "2 个人设") {
			t.Fatalf("Message = %q，想要含条数", got.Message)
		}
	})

	t.Run("web 来源回落已保存的密钥", func(t *testing.T) {
		srv := newPersonaWebServer(t, webKey, personaWebPluginsJSON(t, []map[string]any{
			{"id": "coder", "text": "你是资深工程师"},
		}))
		got, err := newClient(secretsWithKey(t)).TestMagpiePersonas(context.Background(),
			mcodeui.CustomEndpoint{ID: "magpie", PersonaWebURL: srv.URL}, "")
		if err != nil {
			t.Fatalf("TestMagpiePersonas 出错: %v", err)
		}
		if !got.OK || got.Source != "web" || got.Count != 1 {
			t.Fatalf("结果 = %+v，想要用已保存密钥测通", got)
		}
	})

	t.Run("插件未启用", func(t *testing.T) {
		srv := newPersonaWebServer(t, webKey, `{"plugins":[]}`)
		got, err := newClient(config.NewMemorySecretStore()).TestMagpiePersonas(context.Background(),
			mcodeui.CustomEndpoint{PersonaWebURL: srv.URL}, webKey)
		if err != nil {
			t.Fatalf("TestMagpiePersonas 出错: %v", err)
		}
		if !got.OK || got.Source != "web" || got.Count != 0 {
			t.Fatalf("结果 = %+v，想要 web 成功但 0 条", got)
		}
		if !strings.Contains(got.Message, "未启用或不存在") {
			t.Fatalf("Message = %q，想要说明插件未启用", got.Message)
		}
	})

	t.Run("web 鉴权失败", func(t *testing.T) {
		srv := newPersonaWebServer(t, webKey, `{"plugins":[]}`)
		got, err := newClient(config.NewMemorySecretStore()).TestMagpiePersonas(context.Background(),
			mcodeui.CustomEndpoint{PersonaWebURL: srv.URL}, "wrong-key")
		if err != nil {
			t.Fatalf("TestMagpiePersonas 出错: %v", err)
		}
		if got.OK || got.Source != "web" {
			t.Fatalf("结果 = %+v，想要 web 失败", got)
		}
		if strings.Contains(got.Message, "wrong-key") || strings.Contains(got.Message, "k=") {
			t.Fatalf("Message 不能含密钥或带 k= 的地址: %q", got.Message)
		}
	})

	t.Run("手动列表", func(t *testing.T) {
		got, err := newClient(nil).TestMagpiePersonas(context.Background(),
			mcodeui.CustomEndpoint{PersonasJSON: `{"personas":[{"id":"coder"},{"id":"writer"}]}`}, "")
		if err != nil {
			t.Fatalf("TestMagpiePersonas 出错: %v", err)
		}
		if !got.OK || got.Source != "manual" || got.Count != 2 {
			t.Fatalf("结果 = %+v，想要手动列表成功 2 条", got)
		}
		if !strings.Contains(got.Message, "2 个人设") {
			t.Fatalf("Message = %q，想要含条数", got.Message)
		}
	})

	t.Run("手动列表非法", func(t *testing.T) {
		got, err := newClient(nil).TestMagpiePersonas(context.Background(),
			mcodeui.CustomEndpoint{PersonasJSON: `{"personas":`}, "")
		if err != nil {
			t.Fatalf("TestMagpiePersonas 出错: %v", err)
		}
		if got.OK || got.Source != "manual" || got.Message == "" {
			t.Fatalf("结果 = %+v，想要手动列表失败并带原因", got)
		}
	})

	t.Run("本机来源", func(t *testing.T) {
		dir := t.TempDir()
		usePersonaConfigDir(t, dir)
		writePersonaPlugins(t, dir, []map[string]any{
			{"id": "coder", "text": "你是资深工程师"},
		})
		got, err := newClient(nil).TestMagpiePersonas(context.Background(),
			mcodeui.CustomEndpoint{BaseURL: "http://127.0.0.1:3425/v1"}, "")
		if err != nil {
			t.Fatalf("TestMagpiePersonas 出错: %v", err)
		}
		if !got.OK || got.Source != "local" || got.Count != 1 {
			t.Fatalf("结果 = %+v，想要本机来源 1 条", got)
		}
	})

	t.Run("本机插件被关掉", func(t *testing.T) {
		dir := t.TempDir()
		usePersonaConfigDir(t, dir)
		writePersonaPluginsOff(t, dir)
		got, err := newClient(nil).TestMagpiePersonas(context.Background(),
			mcodeui.CustomEndpoint{BaseURL: "http://127.0.0.1:3425/v1"}, "")
		if err != nil {
			t.Fatalf("TestMagpiePersonas 出错: %v", err)
		}
		if !got.OK || got.Source != "local" || got.Count != 0 {
			t.Fatalf("结果 = %+v，想要本机来源成功但 0 条", got)
		}
	})

	t.Run("没配人设来源", func(t *testing.T) {
		got, err := newClient(nil).TestMagpiePersonas(context.Background(),
			mcodeui.CustomEndpoint{BaseURL: "https://magpie.example.com/v1"}, "")
		if err != nil {
			t.Fatalf("TestMagpiePersonas 出错: %v", err)
		}
		if got.OK || got.Source != "" || got.Message != "未配置人设来源" {
			t.Fatalf("结果 = %+v，想要未配置人设来源", got)
		}
	})

	t.Run("web 地址非法", func(t *testing.T) {
		if _, err := newClient(nil).TestMagpiePersonas(context.Background(),
			mcodeui.CustomEndpoint{PersonaWebURL: "ftp://magpie.example.com"}, ""); err == nil {
			t.Fatal("非法 web 管理地址应该返回 error")
		}
	})
}
