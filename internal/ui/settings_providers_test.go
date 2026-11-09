package mcodeui

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func openProvidersSettings(t *testing.T, tt *ui.Tester) {
	t.Helper()
	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("模型与供应商") })
	if err := tt.Click("模型与供应商"); err != nil {
		t.Fatal(err)
	}
}

func TestAPIProviderListEntries_ExcludesGateways(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.presets.loaded = true
	st.presets.items = []ProviderSetting{{ID: "api", Name: "API", Kind: "api", Enabled: true, HasAPIKey: true}}
	st.endpoints.loaded = true
	st.endpoints.endpoints = []CustomEndpoint{{ID: "plain", Name: "普通端点", Enabled: true}, {ID: "gateway", Name: "网关", Magpie: true}}
	entries := apiProviderListEntries(st)
	if len(entries) != 2 || entries[0].ID != "api" || entries[0].Kind != "preset" || entries[1].ID != "plain" || entries[1].Kind != "custom" {
		t.Fatalf("供应商条目错误: %+v", entries)
	}
}

func TestAPIProviderListEntriesAnthropicEnvironmentKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "environment-key")
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.presets.loaded = true
	st.endpoints.loaded = true
	st.presets.items = []ProviderSetting{{ID: "anthropic", Name: "Anthropic", Kind: "api", Enabled: true}}
	entries := apiProviderListEntries(st)
	if len(entries) != 1 || entries[0].Status != "已配置" {
		t.Fatalf("环境变量密钥状态错误: %+v", entries)
	}
}

type failingEndpointListClient struct {
	*FakeClient
	calls chan struct{}
}

func (c *failingEndpointListClient) CustomEndpoints(context.Context) ([]CustomEndpoint, error) {
	c.calls <- struct{}{}
	return nil, errors.New("无法加载")
}

func TestCustomEndpointsFailedLoadIsThrottled(t *testing.T) {
	client := &failingEndpointListClient{FakeClient: NewFakeClient(), calls: make(chan struct{}, 10)}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(client))
	loadCustomEndpoints(st)
	select {
	case <-client.calls:
	case <-time.After(time.Second):
		t.Fatal("首次加载没有发起")
	}
	deadline := time.Now().Add(time.Second)
	for {
		st.mu.Lock()
		finished := !st.endpoints.loading && !st.endpoints.failedAt.IsZero()
		st.mu.Unlock()
		if finished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("首次加载没有完成")
		}
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < 10; i++ {
		loadCustomEndpoints(st)
	}
	st.mu.Lock()
	loading := st.endpoints.loading
	st.mu.Unlock()
	if loading {
		t.Fatal("失败后立即重复请求")
	}
	select {
	case <-client.calls:
		t.Fatal("失败后立即重复请求")
	default:
	}
}

func TestAPIProviderSheet_NativePresets(t *testing.T) {
	for _, id := range []string{"anthropic", "openai"} {
		t.Run(id, func(t *testing.T) {
			st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
			st.presets.loaded, st.endpoints.loaded = true, true
			st.presets.items = []ProviderSetting{{ID: id, Name: id, Kind: "api", HasAPIKey: true}}
			entries := apiProviderListEntries(st)
			if len(entries) != 1 || entries[0].Kind != "preset" {
				t.Fatalf("供应商条目错误: %+v", entries)
			}
			openProviderSheet(st, entries[0].Kind, entries[0].ID)
			tt := ui.NewTester(func(c *ui.Context) { renderProviderSheetBody(c, st) }, 1000, 3000)
			if !tt.HasText("密钥（API Key）") {
				t.Fatalf("供应商面板缺少 API Key: %v", tt.Texts())
			}
			for _, label := range []string{"接口地址（BaseURL）", "默认模型"} {
				if got, want := tt.HasText(label), id == "openai"; got != want {
					t.Fatalf("供应商面板 %q 显示状态 = %v，期望 %v: %v", label, got, want, tt.Texts())
				}
			}
		})
	}
}

func TestProviderAddSheet_TwoKinds(t *testing.T) {
	fake := NewFakeClient()
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
	st.presets.loaded = true
	st.presets.items = []ProviderSetting{{ID: "api", Name: "示例预置", Kind: "api"}, {ID: "configured", Name: "已配置预置", Kind: "api", HasAPIKey: true}, {ID: "magpie", Name: "Magpie", Kind: "api"}}
	enabled := false
	candidates := []providerListEntry{{ID: "cli", Name: "示例 CLI", CanToggle: true, Toggle: func(v bool) { enabled = v }}}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("test-add")).Children(func() { renderProviderAddSheetBody(c, st, candidates) })
	}, 1000, 3000)
	waitFor(t, tt, func() bool { return tt.HasText("启用 示例 CLI") })
	for _, label := range []string{"智能体", "API Key"} {
		if !tt.HasText(label) {
			t.Fatalf("缺少类型选项 %q", label)
		}
	}
	for _, label := range []string{"CLI 智能体", "API 预置", "自定义"} {
		if tt.HasText(label) {
			t.Fatalf("仍有旧类型选项 %q", label)
		}
	}
	if err := tt.Click("启用 示例 CLI"); err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("CLI 未启用")
	}
	if err := tt.Click("API Key"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("示例预置") })
	if !tt.HasText("选择 自定义端点") || tt.HasText("已配置预置") || tt.HasText("Magpie") {
		t.Fatalf("API Key 来源列表错误: %v", tt.Texts())
	}
	if err := tt.Click("选择 自定义端点"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("端点名称") })
	for _, label := range []string{"名称", "接口地址（BaseURL）", "默认模型", "密钥（API Key）"} {
		if !tt.HasText(label) {
			t.Fatalf("自定义端点表单缺少 %q", label)
		}
	}
	for _, field := range []struct{ label, value string }{{"编辑端点名称", "测试端点"}, {"编辑端点 BaseURL", "https://example.com/v1"}, {"编辑端点默认模型", "example"}, {"编辑端点 API Key", "secret"}} {
		if err := tt.Click(field.label); err != nil {
			t.Fatal(err)
		}
		tt.Type(field.value)
	}
	if tt.HasText("Magpie") {
		t.Fatal("添加面板不应包含 Magpie")
	}
	if err := tt.Click("添加"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.saveCustomEndpointCalls) == 1 && fake.saveCustomEndpointCalls[0].Name == "测试端点" && fake.saveCustomEndpointCalls[0].BaseURL == "https://example.com/v1" && fake.saveCustomEndpointCalls[0].Model == "example" && fake.saveCustomEndpointKeys[0] == "secret" && !fake.saveCustomEndpointCalls[0].Magpie
	})
}

// TestSettingsProviders_EmptyList 覆盖：空列表时显示“尚未添加自定义端点”。
func TestSettingsProviders_EmptyList(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.presets.loaded, st.endpoints.loaded = true, true
	if entries := apiProviderListEntries(st); len(entries) != 0 {
		t.Fatalf("空列表包含条目: %+v", entries)
	}
}

// TestSettingsProviders_ListRender 覆盖：列表渲染（名称、BaseURL 与统一状态摘要出现）。
func TestSettingsProviders_ListRender(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.presets.loaded, st.endpoints.loaded = true, true
	st.endpoints.endpoints = []CustomEndpoint{{ID: "ep-1", Name: "本地网关", BaseURL: "http://localhost:8000/v1", Model: "qwen2.5-coder", Enabled: true}}
	entries := apiProviderListEntries(st)
	if len(entries) != 1 || entries[0].ID != "ep-1" || entries[0].Name != "本地网关" || entries[0].Status != "未配置密钥" || entries[0].Kind != "custom" {
		t.Fatalf("列表错误: %+v", entries)
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("test-sheet")).Children(func() { renderAPIProviderSheetSections(c, st, "custom", "ep-1") })
	}, 1000, 3000)
	if _, ok := tt.Find("编辑端点 BaseURL"); !ok {
		t.Fatal("面板缺少地址输入框")
	}
	if _, ok := tt.Find("编辑端点默认模型"); !ok {
		t.Fatal("面板缺少模型输入框")
	}
	if st.endpoints.editBaseURL != "http://localhost:8000/v1" || st.endpoints.editModel != "qwen2.5-coder" {
		t.Fatal("面板未载入端点地址和模型")
	}
}

// TestSettingsProviders_CreateCustomEndpoint 覆盖：新建保存调用 SaveCustomEndpoint 且 ID 为空并带 key。
func TestSettingsProviders_CreateCustomEndpoint(t *testing.T) {
	fake := NewFakeClient()
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
	st.presets.loaded = true
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("test-add")).Children(func() { renderProviderAddSheetBody(c, st, nil) })
	}, 1000, 3000)
	if err := tt.Click("API Key"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("选择 自定义端点"); err != nil {
		t.Fatal(err)
	}
	for _, field := range []struct{ label, value string }{{"编辑端点名称", "OpenRouter"}, {"编辑端点 BaseURL", "https://openrouter.ai/api/v1"}, {"编辑端点默认模型", "anthropic/claude-3.5-sonnet"}, {"编辑端点 API Key", "sk-or-testkey-123456"}} {
		if err := tt.Click(field.label); err != nil {
			t.Fatal(err)
		}
		tt.Type(field.value)
	}
	if err := tt.Click("添加"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if len(fake.saveCustomEndpointCalls) != 1 {
			return false
		}
		call := fake.saveCustomEndpointCalls[0]
		return call.ID == "" && call.Name == "OpenRouter" && call.BaseURL == "https://openrouter.ai/api/v1" && call.Model == "anthropic/claude-3.5-sonnet" && fake.saveCustomEndpointKeys[0] == "sk-or-testkey-123456"
	})
}

// TestSettingsProviders_EditEndpointKeepKey 覆盖：编辑时 key 留空传空串。
func TestSettingsProviders_EditEndpointKeepKey(t *testing.T) {
	fake := NewFakeClient()
	ep := CustomEndpoint{ID: "ep-exist", Name: "旧端点", BaseURL: "https://old.example.com/v1", Model: "gpt-4o", Enabled: true, HasAPIKey: true}
	fake.customEndpoints = []CustomEndpoint{ep}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
	st.endpoints.loaded = true
	st.endpoints.endpoints = []CustomEndpoint{ep}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("test-sheet")).Children(func() { renderAPIProviderSheetSections(c, st, "custom", ep.ID) })
	}, 1000, 3000)
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.saveCustomEndpointCalls) == 1 && fake.saveCustomEndpointCalls[0].ID == ep.ID && fake.saveCustomEndpointKeys[0] == "" && fake.customEndpoints[0].HasAPIKey
	})
}

// TestSettingsProviders_ToggleEnabled 覆盖：启用开关切换。
func TestSettingsProviders_ToggleEnabled(t *testing.T) {
	fake := NewFakeClient()
	ep := CustomEndpoint{ID: "ep-toggle", Name: "切换开关端点", Enabled: true}
	fake.customEndpoints = []CustomEndpoint{ep}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
	st.presets.loaded = true
	st.endpoints.loaded = true
	st.endpoints.endpoints = []CustomEndpoint{ep}
	entries := apiProviderListEntries(st)
	if len(entries) != 1 || !entries[0].CanToggle {
		t.Fatal("缺少启用开关")
	}
	entries[0].Toggle(false)
	tt := ui.NewTester(func(c *ui.Context) {}, 100, 100)
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.saveCustomEndpointCalls) == 1 && fake.saveCustomEndpointCalls[0].ID == ep.ID && !fake.saveCustomEndpointCalls[0].Enabled && fake.saveCustomEndpointKeys[0] == ""
	})
}

// TestSettingsProviders_DeleteWithConfirmation 覆盖：删除需二次确认后才调用 DeleteCustomEndpoint。
func TestSettingsProviders_DeleteWithConfirmation(t *testing.T) {
	fake := NewFakeClient()
	ep := CustomEndpoint{ID: "ep-del", Name: "待删除端点", BaseURL: "https://delete.example.com/v1", Enabled: true}
	fake.customEndpoints = []CustomEndpoint{ep}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
	st.endpoints.loaded = true
	st.endpoints.endpoints = []CustomEndpoint{ep}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("test-sheet")).Children(func() { renderAPIProviderSheetSections(c, st, "custom", ep.ID) })
	}, 1000, 3000)
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("确认删除") {
		t.Fatal("缺少二次确认")
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	n := len(fake.deleteCustomEndpointCalls)
	fake.mu.Unlock()
	if n != 0 {
		t.Fatal("取消后发生删除")
	}
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("确认删除"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.deleteCustomEndpointCalls) == 1 && fake.deleteCustomEndpointCalls[0] == ep.ID && len(fake.customEndpoints) == 0
	})
}

// TestSettingsProviders_TestConnection 覆盖：测试连接成功与失败文案。
func TestSettingsProviders_TestConnection(t *testing.T) {
	fake := NewFakeClient()
	fake.testCustomEndpointResult = CustomEndpointTestResult{OK: true, StatusCode: 200, Latency: 128 * time.Millisecond, Models: []string{"model-a", "model-b"}}
	ep := CustomEndpoint{ID: "test", Name: "测试端点", BaseURL: "https://example.com", Model: "model"}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
	st.endpoints.loaded = true
	st.endpoints.endpoints = []CustomEndpoint{ep}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("test-sheet")).Children(func() { renderAPIProviderSheetSections(c, st, "custom", ep.ID) })
	}, 1000, 3000)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("128 ms · 2 个模型") })
	fake.mu.Lock()
	fake.testCustomEndpointResult = CustomEndpointTestResult{OK: false, StatusCode: 401, Message: "401 Invalid API Key"}
	fake.mu.Unlock()
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("401 Invalid API Key") })
}

// TestSettingsProviders_ClientError 覆盖：客户端错误显示在区块内。
func TestSettingsProviders_ClientError(t *testing.T) {
	fake := NewFakeClient()
	fake.customEndpointErr = errors.New("服务连接被拒绝")
	ep := CustomEndpoint{ID: "err", Name: "端点", BaseURL: "https://example.com", Model: "model"}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
	st.endpoints.loaded = true
	st.endpoints.endpoints = []CustomEndpoint{ep}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("test-sheet")).Children(func() { renderAPIProviderSheetSections(c, st, "custom", ep.ID) })
	}, 1000, 3000)
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("服务连接被拒绝") })
}

func TestSettingsProviders_RefreshSelector(t *testing.T) {
	fake := NewFakeClient()
	chat := &chatState{client: fake, newThreadProvider: "deleted"}
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	fake.SetProviders([]ProviderInfo{{ID: "custom-new", Name: "新端点", Available: true}})
	refreshCustomEndpointProviders(st, fake)
	chat.mu.Lock()
	defer chat.mu.Unlock()
	if len(chat.providers) != 1 || chat.providers[0].ID != "custom-new" || chat.newThreadProvider != "custom-new" {
		t.Fatal("端点修改后必须刷新会话选择器和失效的选择")
	}
}

type orderedCatalogClient struct {
	*FakeClient
	requests chan chan []ProviderInfo
}

func (c *orderedCatalogClient) Providers(ctx context.Context) ([]ProviderInfo, error) {
	reply := make(chan []ProviderInfo, 1)
	select {
	case c.requests <- reply:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case providers := <-reply:
		return providers, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestRefreshCustomEndpointProvidersIgnoresOlderCatalog(t *testing.T) {
	client := &orderedCatalogClient{FakeClient: NewFakeClient(), requests: make(chan chan []ProviderInfo, 2)}
	chat := &chatState{client: client, newThreadProvider: "deleted"}
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	done := make(chan struct{}, 2)
	go func() { refreshCustomEndpointProviders(st, client); done <- struct{}{} }()
	var older chan []ProviderInfo
	select {
	case older = <-client.requests:
	case <-time.After(3 * time.Second):
		t.Fatal("旧目录请求未发起")
	}
	go func() { refreshCustomEndpointProviders(st, client); done <- struct{}{} }()
	var newer chan []ProviderInfo
	select {
	case newer = <-client.requests:
	case <-time.After(3 * time.Second):
		older <- nil
		t.Fatal("新目录请求未发起")
	}
	newer <- []ProviderInfo{{ID: "new", Available: true, Models: []ModelInfo{{ID: "new-model"}}}}
	<-done
	older <- []ProviderInfo{{ID: "old", Available: true, Models: []ModelInfo{{ID: "old-model"}}}}
	<-done
	chat.mu.Lock()
	defer chat.mu.Unlock()
	if len(chat.providers) != 1 || chat.providers[0].ID != "new" || len(chat.providers[0].Models) != 1 || chat.providers[0].Models[0].ID != "new-model" || chat.newThreadProvider != "new" {
		t.Fatalf("旧目录覆盖了新目录或新线程选择: %+v，%q", chat.providers, chat.newThreadProvider)
	}
}

func TestSettingsProviders_OtherSettingsPreserveEndpoints(t *testing.T) {
	dir := t.TempDir()
	st := NewSettingsState(dir, nil, nil)
	cfg := config.Defaults()
	cfg.Providers.Custom = []config.CustomEndpointConfig{
		{ID: "new", Name: "刚添加的端点", BaseURL: "http://localhost/v1", Enabled: true},
	}
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	st.cfg.Permissions.Mode = config.ModeSupervised
	st.save()
	loaded, err := config.Load(dir)
	if err != nil || st.errorMessage != "" {
		t.Fatalf("保存失败：%v, %s", err, st.errorMessage)
	}
	if len(loaded.Providers.Custom) != 1 || loaded.Permissions.Mode != config.ModeSupervised {
		t.Fatal("其他设置保存不应覆盖后台新添加的端点")
	}
}

// presetsPageTester 渲染仅包含「API 供应商」预设区域的测试页面，返回可断言的 Tester 与 SettingsState。
func presetsPageTester(t *testing.T, client Client) (*ui.Tester, *SettingsState) {
	t.Helper()
	chat := newChatState(client)
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("presets-test-page")).Children(func() {
			renderPresetProviders(c, st)
		})
	}, 1000, 3000)
	return tt, st
}

// TestSettingsProviders_PresetListFiltersAndOrder 覆盖：预设列表仅保留 Kind=="api" 的项且保持后端返回顺序。
func TestSettingsProviders_PresetListFiltersAndOrder(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.providerSettings = []ProviderSetting{
		{ID: "claude", Name: "Claude CLI", Kind: "cli"},
		{ID: "deepseek", Name: "DeepSeek", Kind: "api"},
		{ID: "zai", Name: "ZAI", Kind: "api", HasAPIKey: true},
		{ID: "meta", Name: "Meta AI", Kind: "api"},
	}
	fake.mu.Unlock()

	tt, st := presetsPageTester(t, fake)
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.presets.loaded
	})

	st.mu.Lock()
	got := append([]ProviderSetting(nil), st.presets.items...)
	st.mu.Unlock()
	want := []string{"deepseek", "zai", "meta"}
	if len(got) != len(want) {
		t.Fatalf("预设项数量 = %d, 期望 %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id || got[i].Kind != "api" {
			t.Fatalf("预设[%d] = %s(%s), 期望 %s(api)", i, got[i].ID, got[i].Kind, id)
		}
	}

	tt.Frame()
	if tt.HasText("Claude CLI") {
		t.Fatal("cli 供应商不应出现在 API 预设区域")
	}
	if !tt.HasText("已配置") || !tt.HasText("未配置") {
		t.Fatal("Key 状态应分别显示「已配置」与「未配置」")
	}
	first, okFirst := tt.Find("DeepSeek")
	second, okSecond := tt.Find("ZAI")
	third, okThird := tt.Find("Meta AI")
	if !okFirst || !okSecond || !okThird || !(first.Y < second.Y && second.Y < third.Y) {
		t.Fatal("预设供应商应按后端返回顺序渲染")
	}
}

// TestSettingsProviders_PresetSavePassesKeyAndDrafts 覆盖：保存时 apiKey 透传、BaseURL/Model 草稿写入 ProviderSetting，保存后 HasAPIKey 刷新。
func TestSettingsProviders_PresetSavePassesKeyAndDrafts(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.providerSettings = []ProviderSetting{
		{
			ID: "deepseek", Name: "DeepSeek", Kind: "api",
			Enabled:        true,
			DefaultBaseURL: "https://api.deepseek.com/v1",
			DefaultModel:   "deepseek-chat",
		},
	}
	fake.mu.Unlock()

	tt, _ := presetsPageTester(t, fake)
	waitFor(t, tt, func() bool { return tt.HasText("DeepSeek") })
	if !tt.HasText("未配置 · 填写 API Key") {
		t.Fatal("启用但未设置 Key 的供应商应提示「未配置 · 填写 API Key」")
	}
	if err := tt.Click("编辑 DeepSeek"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("编辑预设 API Key") })

	// 草稿初始为空，占位符为 DefaultBaseURL / DefaultModel
	if err := tt.Click("编辑预设 BaseURL"); err != nil {
		t.Fatal(err)
	}
	tt.Type("https://proxy.example.com/v1")
	if err := tt.Click("编辑预设默认模型"); err != nil {
		t.Fatal(err)
	}
	tt.Type("deepseek-reasoner")
	if err := tt.Click("编辑预设 API Key"); err != nil {
		t.Fatal(err)
	}
	tt.Type("sk-preset-123456")

	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if len(fake.saveProviderSettingCalls) == 0 {
			return false
		}
		call := fake.saveProviderSettingCalls[0]
		key := fake.saveProviderSettingKeys[0]
		return call.ID == "deepseek" &&
			call.Kind == "api" &&
			call.BaseURL == "https://proxy.example.com/v1" &&
			call.Model == "deepseek-reasoner" &&
			key == "sk-preset-123456"
	})

	// 保存成功后表单关闭，列表刷新为「已配置」
	waitFor(t, tt, func() bool {
		return !tt.HasText("编辑预设 API Key") && tt.HasText("已配置")
	})
	if tt.HasText("未配置 · 填写 API Key") {
		t.Fatal("保存 Key 后不应再提示「未配置 · 填写 API Key」")
	}
	fake.mu.Lock()
	hasKey := fake.providerSettings[0].HasAPIKey
	fake.mu.Unlock()
	if !hasKey {
		t.Fatal("保存新 Key 后 FakeClient 中的 HasAPIKey 未刷新")
	}
}

// TestSettingsProviders_PresetEditKeepsKeyWhenBlank 覆盖：编辑时 Key 留空传空串，预填的 BaseURL/Model 原样写回。
func TestSettingsProviders_PresetEditKeepsKeyWhenBlank(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.providerSettings = []ProviderSetting{
		{
			ID: "zai", Name: "ZAI", Kind: "api",
			Enabled:   true,
			BaseURL:   "https://old.example.com/v1",
			Model:     "glm-4",
			HasAPIKey: true,
		},
	}
	fake.mu.Unlock()

	tt, _ := presetsPageTester(t, fake)
	waitFor(t, tt, func() bool { return tt.HasText("已配置") })
	if err := tt.Click("编辑 ZAI"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("编辑预设 API Key") })

	// 不输入 API Key 直接保存
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if len(fake.saveProviderSettingCalls) == 0 {
			return false
		}
		call := fake.saveProviderSettingCalls[0]
		key := fake.saveProviderSettingKeys[0]
		return call.ID == "zai" &&
			call.BaseURL == "https://old.example.com/v1" &&
			call.Model == "glm-4" &&
			key == ""
	})

	// 已保存的 Key 保留
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		hasKey := fake.providerSettings[0].HasAPIKey
		fake.mu.Unlock()
		return hasKey && !tt.HasText("编辑预设 API Key") && tt.HasText("已配置")
	})
}

// TestSettingsProviders_PresetToggleEnabled 覆盖：切换启用开关调用 SaveProviderSetting（apiKey 传空）并更新 FakeClient 中的 Enabled。
func TestSettingsProviders_PresetToggleEnabled(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.providerSettings = []ProviderSetting{
		{ID: "meta", Name: "Meta AI", Kind: "api", Enabled: true, HasAPIKey: true},
	}
	fake.mu.Unlock()

	tt, st := presetsPageTester(t, fake)
	waitFor(t, tt, func() bool { return tt.HasText("Meta AI") })

	// 关闭开关
	if err := tt.Click("切换Meta AI"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.saveProviderSettingCalls) == 1 &&
			!fake.providerSettings[0].Enabled &&
			fake.saveProviderSettingKeys[0] == ""
	})
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return len(st.presets.items) == 1 && !st.presets.items[0].Enabled && !st.presets.saving
	})
	tt.Frame()

	// 重新启用
	if err := tt.Click("切换Meta AI"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.saveProviderSettingCalls) == 2 &&
			fake.providerSettings[0].Enabled &&
			fake.saveProviderSettingKeys[1] == ""
	})
}

// TestSettingsProviders_PresetRowShowsCredits 覆盖：后端支持余额的预设供应商在行下方渲染余额面板（金额 + 明细）。
func TestSettingsProviders_PresetRowShowsCredits(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.presets.loaded = true
	st.presets.items = []ProviderSetting{{ID: "deepseek", Name: "DeepSeek", Kind: "api", Enabled: true, HasAPIKey: true}}
	st.providerRows.credits = map[string]ProviderCredits{"deepseek": {Provider: "deepseek", Total: 3.5, Currency: "USD", AmountText: "$3.50", BreakdownText: "充值 $5.00 · 已用 $1.50", Usable: true}}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("test-sheet")).Children(func() { renderAPIProviderSheetSections(c, st, "preset", "deepseek") })
	}, 1000, 3000)
	for _, text := range []string{"余额", "$3.50", "充值 $5.00 · 已用 $1.50"} {
		if !tt.HasText(text) {
			t.Fatalf("面板缺少 %s: %v", text, tt.Texts())
		}
	}
	if tt.HasText("余额不足") {
		t.Fatal("余额充足时不应标注余额不足")
	}
}

// TestSettingsProviders_PresetSectionInSettingsPage 覆盖：供应商账号和模型直接显示。
func TestSettingsProviders_PresetSectionInSettingsPage(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.providerSettings = []ProviderSetting{
		{ID: "deepseek", Name: "DeepSeek", Kind: "api", DefaultModel: "deepseek-chat", DefaultBaseURL: "https://api.deepseek.com"},
	}
	fake.mu.Unlock()

	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
	st.presets.loaded = true
	st.presets.items = []ProviderSetting{{ID: "deepseek", Name: "DeepSeek", Kind: "api", DefaultModel: "deepseek-chat", DefaultBaseURL: "https://api.deepseek.com"}}
	openProviderSheet(st, "preset", "deepseek")
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("preset-sheet-test")).Children(func() { renderProviderSheetBody(c, st) })
	}, 1000, 3000)
	waitFor(t, tt, func() bool {
		return tt.HasText("账号") && tt.HasText("模型") && tt.HasText("编辑预设默认模型")
	})
}

// TestSettingsProviders_FooterTestResultState 覆盖：底栏成功与失败两种结果文案。
func TestSettingsProviders_FooterTestResultState(t *testing.T) {
	ep := CustomEndpoint{ID: "footer", Name: "测试端点", BaseURL: "https://example.com", Model: "model"}
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.endpoints.loaded = true
	st.endpoints.endpoints = []CustomEndpoint{ep}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Row(c.Key("footer-test")).Children(func() { renderAPIProviderSheetFooter(c, st, "custom", ep.ID) })
	}, 1000, 3000)
	waitFor(t, tt, func() bool { return tt.HasText("保存") })

	st.mu.Lock()
	st.endpoints.testResult = &CustomEndpointTestResult{OK: true, StatusCode: 200, Latency: 128 * time.Millisecond, Models: []string{"model-a", "model-b"}}
	st.mu.Unlock()
	st.invalidate()
	waitFor(t, tt, func() bool { return tt.HasText("128 ms · 2 个模型") })

	st.mu.Lock()
	st.endpoints.testResult = &CustomEndpointTestResult{OK: false, StatusCode: 401, Message: "401 Invalid API Key"}
	st.mu.Unlock()
	st.invalidate()
	waitFor(t, tt, func() bool { return tt.HasText("401 Invalid API Key") })
}

// TestSettingsProviders_FooterButtonsRightAligned 覆盖：底栏状态文字靠左，删除紧跟其后，取消/测试/保存贴在右侧。
func TestSettingsProviders_FooterButtonsRightAligned(t *testing.T) {
	ep := CustomEndpoint{ID: "footer-align", Name: "测试端点", BaseURL: "https://example.com", Model: "model"}
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.endpoints.loaded = true
	st.endpoints.endpoints = []CustomEndpoint{ep}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Row(c.Key("footer-align-test")).FillWidth().Children(func() { renderAPIProviderSheetFooter(c, st, "custom", ep.ID) })
	}, 720, 600)
	waitFor(t, tt, func() bool { return tt.HasText("保存") })

	find := func(label string) ui.Rect {
		t.Helper()
		rect, ok := tt.Find(label)
		if !ok {
			t.Fatalf("未找到 %q：%v", label, tt.Texts())
		}
		return rect
	}
	save := find("保存")
	del := find("删除")
	cancel := find("取消")
	testBtn := find("测试连接")
	if save.X <= 360 {
		t.Errorf("保存应贴在右侧：save.X=%.1f（宽度 720）", save.X)
	}
	if del.X >= cancel.X {
		t.Errorf("删除应位于取消之前：delete.X=%.1f cancel.X=%.1f", del.X, cancel.X)
	}
	if !(cancel.X < testBtn.X && testBtn.X < save.X) {
		t.Errorf("取消/测试/保存应依次右对齐：cancel.X=%.1f test.X=%.1f save.X=%.1f", cancel.X, testBtn.X, save.X)
	}
}

// setModelSettingEnabled 在测试里直接改设置快照里的供应商启用开关，模拟用户在供应商页关闭/启用。
func setModelSettingEnabled(st *SettingsState, provider string, enabled bool) {
	st.mu.Lock()
	switch provider {
	case config.ProviderClaude:
		st.cfg.Providers.Claude.Enabled = enabled
	case config.ProviderCodex:
		st.cfg.Providers.Codex.Enabled = enabled
	}
	st.mu.Unlock()
	st.invalidate()
}

// TestSettingsProviders_DisabledProviderHidesPinnedModels 覆盖：关闭的供应商在「模型」页隐藏，
// 「我的模型」的持久化数据仍保留；重新启用后条目重新出现。
func TestSettingsProviders_DisabledProviderHidesPinnedModels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	pins := []ModelPin{
		{Provider: "claude", ModelID: "sonnet"},
		{Provider: "codex", ModelID: "gpt-5.5"},
	}
	for _, pin := range pins {
		if _, err := fake.AddModelToList(context.Background(), pin); err != nil {
			t.Fatal(err)
		}
	}
	providers := []ProviderInfo{
		{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "sonnet", Name: "Sonnet"}}},
		{ID: "codex", Name: "Codex", Models: []ModelInfo{{ID: "gpt-5.5", Name: "GPT-5.5"}}},
	}
	tt, st := modelsPageTester(t, fake, providers)
	waitFor(t, tt, func() bool {
		return tt.HasText("移除收藏 Claude Sonnet") && tt.HasText("移除收藏 Codex GPT-5.5")
	})

	// 关闭 Claude：固定模型行不再展示（模型库已并入供应商行，不再单独断言区块）。
	setModelSettingEnabled(st, config.ProviderClaude, false)
	waitFor(t, tt, func() bool {
		return !tt.HasText("移除收藏 Claude Sonnet")
	})
	if !tt.HasText("移除收藏 Codex GPT-5.5") {
		t.Fatal("关闭 Claude 不应影响其他供应商的固定模型")
	}
	// 数据没被删除：存储与设置快照都保留两条。
	persisted, _, err := fake.ModelList(context.Background())
	if err != nil || !reflect.DeepEqual(persisted, pins) {
		t.Fatalf("关闭供应商改动了已持久化的我的模型: %+v, %v", persisted, err)
	}
	st.mu.Lock()
	snapshot := append([]ModelPin(nil), st.cfg.Models.Pins...)
	st.mu.Unlock()
	if !reflect.DeepEqual(snapshot, pins) {
		t.Fatalf("设置快照里的我的模型被改动: %+v", snapshot)
	}

	// 重新启用：条目重新出现。
	setModelSettingEnabled(st, config.ProviderClaude, true)
	waitFor(t, tt, func() bool {
		return tt.HasText("移除收藏 Claude Sonnet")
	})
}

// TestProviderEnabledMap_FiltersByProviderSettings 覆盖：CLI harness / API 预设的启用状态
// 来自 ProviderSettings，供应商页已加载的行设置优先于打开页面时的快照。
func TestProviderEnabledMap_FiltersByProviderSettings(t *testing.T) {
	cfg := config.Defaults()
	pins := []ModelPin{
		{Provider: "pi", ModelID: "gpt"},
		{Provider: "deepseek", ModelID: "deepseek-chat"},
		{Provider: "claude", ModelID: "sonnet"},
	}
	settings := []ProviderSetting{{ID: "pi", Enabled: false}, {ID: "deepseek", Enabled: true}}

	enabled := providerEnabledMap(cfg, nil, settings)
	got := filterPinsByEnabledProviders(pins, enabled)
	if want := pins[1:]; !reflect.DeepEqual(got, want) {
		t.Fatalf("关闭的供应商未隐藏: %+v", got)
	}
	if len(pins) != 3 || pins[0].Provider != "pi" {
		t.Fatalf("过滤改动了原始固定模型列表: %+v", pins)
	}

	// 供应商页刚重新启用 Pi：行设置覆盖页面加载时的快照，条目立即恢复。
	enabled = providerEnabledMap(cfg, map[string]ProviderSetting{"pi": {ID: "pi", Enabled: true}}, settings)
	if got := filterPinsByEnabledProviders(pins, enabled); !reflect.DeepEqual(got, pins) {
		t.Fatalf("重新启用后条目没有恢复: %+v", got)
	}

	// 模型库按同样的启用状态过滤供应商区块。
	providers := []ProviderInfo{{ID: "pi", Name: "Pi"}, {ID: "deepseek", Name: "DeepSeek"}, {ID: "claude", Name: "Claude"}}
	if got := filterProvidersByEnabled(providers, providerEnabledMap(cfg, nil, settings)); len(got) != 2 || got[0].ID != "deepseek" {
		t.Fatalf("关闭的供应商仍出现在模型库: %+v", got)
	}

	// 没有启用状态信息的供应商不视为关闭。
	if providerDisabled(providerEnabledMap(cfg, nil, nil), "unknown") {
		t.Fatal("未知供应商被误判为关闭")
	}
}

// TestPersonaWebURLNeedsHTTPSWarning 覆盖：只有明文 http 且主机不是本机回环地址时才提示风险。
func TestPersonaWebURLNeedsHTTPSWarning(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"空地址", "", false},
		{"https 地址", "https://magpie.example.com:3430", false},
		{"本机 localhost", "http://localhost:3430", false},
		{"本机回环 IPv4", "http://127.0.0.1:3430", false},
		{"本机回环 IPv6", "http://[::1]:3430", false},
		{"远端 http 带端口", "http://magpie.example.com:3430", true},
		{"远端 http 带路径", "  http://192.168.1.10/magpie  ", true},
		{"缺少主机", "http://", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := personaWebURLNeedsHTTPSWarning(tc.raw); got != tc.want {
				t.Fatalf("personaWebURLNeedsHTTPSWarning(%q) = %v, 期望 %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestResolveMagpieQuotaSelection 覆盖：resolveMagpieQuotaSelection 处理 nil（默认只选 codex）和空切片。
func TestResolveMagpieQuotaSelection(t *testing.T) {
	selNil := resolveMagpieQuotaSelection(nil)
	if !selNil["codex"] || len(selNil) != 1 {
		t.Fatalf("nil 应默认只包含 codex，got: %+v", selNil)
	}

	selEmpty := resolveMagpieQuotaSelection([]string{})
	if len(selEmpty) != 0 {
		t.Fatalf("空切片应返回空 map，got: %+v", selEmpty)
	}

	selCustom := resolveMagpieQuotaSelection([]string{"claude", "zai"})
	if !selCustom["claude"] || !selCustom["zai"] || len(selCustom) != 2 {
		t.Fatalf("应包含 claude 与 zai，got: %+v", selCustom)
	}
}

// TestSettingsProviders_MagpieEndpointNotListed 覆盖：Magpie 端点只出现在顶部 hub，
// 不再出现在「自定义 Endpoints」列表里（名称只渲染一次）。
func TestSettingsProviders_MagpieEndpointNotListed(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.presets.loaded, st.endpoints.loaded = true, true
	st.endpoints.endpoints = []CustomEndpoint{{ID: "plain", Name: "普通端点"}, {ID: "magpie", Name: "远程 Magpie", Magpie: true}}
	entries := apiProviderListEntries(st)
	if len(entries) != 1 || entries[0].ID != "plain" {
		t.Fatalf("列表包含 Magpie: %+v", entries)
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("test-add")).Children(func() { renderProviderAddSheetBody(c, st, nil) })
	}, 1000, 3000)
	if err := tt.Click("API Key"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("选择 自定义端点"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("远程 Magpie") || tt.HasText("Magpie · ") {
		t.Fatal("添加面板包含 Magpie")
	}
}

// 勾选框，也不会出现人设来源区块。
func TestSettingsProviders_FormHasNoMagpieCheckbox(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.presets.loaded = true
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("test-add")).Children(func() { renderProviderAddSheetBody(c, st, nil) })
	}, 1000, 3000)
	if err := tt.Click("API Key"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("选择 自定义端点"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("这是 Magpie 网关") || tt.HasText("人设来源（可选）") {
		t.Fatal("自定义表单包含 Magpie 控件")
	}
}

// TestSettingsProviders_RowDescriptionsReplacedByPlaceholders 覆盖：行内说明文字已删除，
// 默认值提示改由占位符承接。
func TestSettingsProviders_RowDescriptionsReplacedByPlaceholders(t *testing.T) {
	if got := providerDefaultPlaceholder(""); got != "留空则用供应商默认值" {
		t.Fatalf("空默认值占位符 = %q", got)
	}
	if got := providerDefaultPlaceholder("https://api.deepseek.com/v1"); got != "https://api.deepseek.com/v1" {
		t.Fatalf("默认值占位符 = %q", got)
	}

	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.presets.loaded = true
	st.presets.items = []ProviderSetting{{ID: "deepseek", Name: "DeepSeek", Kind: "api", DefaultBaseURL: "https://api.deepseek.com"}}
	st.endpoints.loaded = true
	ep := CustomEndpoint{ID: "ep", Name: "端点", BaseURL: "https://example.com/v1"}
	st.endpoints.endpoints = []CustomEndpoint{ep}
	st.mu.Lock()
	openCustomEndpointForm(st, &ep, endpointFormScopeCustom)
	st.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("desc-test")).Children(func() {
			renderAPIProviderSheetSections(c, st, "preset", "deepseek")
			renderPageCustomEndpoints(c, st)
		})
	}, 1000, 4000)
	for _, desc := range []string{"这个网关在列表里显示的名字", "例如 https://host/v1", "不填则按请求里的 model 透传", "只保存在本机配置文件", "留空则用供应商默认值"} {
		if tt.HasText(desc) {
			t.Fatalf("说明文字未删除: %q", desc)
		}
	}
	for _, label := range []string{"编辑端点名称", "编辑预设 BaseURL", "编辑预设默认模型"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("缺少输入框 %q: %v", label, tt.Texts())
		}
	}
}

// 验证预设和自定义端点的地址与模型字段。
func TestAPIProviderNeedsEndpointFields(t *testing.T) {
	st := &SettingsState{}
	st.presets.items = []ProviderSetting{
		{ID: "deepseek", Kind: "api", DefaultBaseURL: "https://api.deepseek.com"},
		{ID: "anthropic", Kind: "api"},
	}
	if !apiProviderNeedsEndpointFields(st, "preset", "deepseek") {
		t.Fatal("带默认端点的预设应显示 BaseURL 与默认模型")
	}
	if !apiProviderNeedsEndpointFields(st, "preset", "openai") {
		t.Fatal("OpenAI 应显示 BaseURL 与默认模型")
	}
	if apiProviderNeedsEndpointFields(st, "preset", "anthropic") {
		t.Fatal("Anthropic 只保存 API Key，不应显示 BaseURL 与默认模型")
	}
	st.presets.items = []ProviderSetting{
		{ID: "deepseek", Kind: "api", DefaultBaseURL: "https://api.deepseek.com"},
		{ID: "anthropic", Kind: "api", DefaultBaseURL: "https://api.anthropic.com"},
	}
	if apiProviderNeedsEndpointFields(st, "preset", "anthropic") {
		t.Fatal("Anthropic 即使预设带默认端点也不应显示 BaseURL 与默认模型")
	}
	if !apiProviderNeedsEndpointFields(st, "custom", "ep") {
		t.Fatal("自定义端点始终需要 BaseURL 与默认模型")
	}
}
