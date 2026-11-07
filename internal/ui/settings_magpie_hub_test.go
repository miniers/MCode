package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsMagpieHub_EmptyState(t *testing.T) {
	tt, _ := magpiePageWithFake(t, NewFakeClient(), false)
	for _, text := range []string{"接口地址（BaseURL）", "密钥（API Key）", "连接"} {
		if !tt.HasText(text) {
			t.Fatalf("缺少 %q: %v", text, tt.Texts())
		}
	}
	for _, text := range []string{"智能体", "人设", "添加网关"} {
		if tt.HasText(text) {
			t.Fatalf("未连接时不应显示 %q", text)
		}
	}
}

func TestSettingsMagpieHub_GatewayStatus(t *testing.T) {
	fake := NewFakeClient()
	fake.customEndpoints = []CustomEndpoint{
		{ID: "ok", Name: "本机网关", BaseURL: "https://example.com/v1", Enabled: true, Magpie: true, HasAPIKey: true},
		{ID: "off", Name: "停用网关", Magpie: true, HasAPIKey: true},
		{ID: "err", Name: "故障网关", Enabled: true, Magpie: true, HasAPIKey: true},
		{ID: "nokey", Name: "缺密钥网关", Enabled: true, Magpie: true},
	}
	fake.SetPiMagpieEndpoints([]PiMagpieEndpoint{{ID: "ok", Models: []PiMagpieModel{{ID: "alpha"}, {ID: "beta"}}}, {ID: "err", Err: "connection refused"}})
	tt, _ := magpiePageWithFake(t, fake, false)
	for _, text := range []string{"已连接 · 2 个模型", "未启用", "连接失败：connection refused", "未设置 API Key"} {
		if !tt.HasText(text) {
			t.Fatalf("缺少状态 %q: %v", text, tt.Texts())
		}
	}
}

func TestSettingsMagpieHub_PersonaAndQuota(t *testing.T) {
	fake := NewFakeClient()
	fake.customEndpoints = []CustomEndpoint{{ID: "gw", Name: "本机网关", BaseURL: "https://example.com/v1", Enabled: true, Magpie: true, HasAPIKey: true, PersonaWebURL: "https://magpie.example.com:3430", PersonasJSON: `[{"id":"coder"}]`, PersonaRules: true, HasWebKey: true, QuotaProviders: []string{"codex"}}}
	fake.magpieQuotaProviders = []string{"claude", "codex"}
	fake.SetPiMagpieEndpoints([]PiMagpieEndpoint{{ID: "gw"}})
	tt, st := magpiePageWithFake(t, fake, false)
	if _, ok := tt.Find("人设 1"); !ok {
		t.Fatalf("人设入口或数量缺失: %v", tt.Texts())
	}
	if err := tt.Click("人设 1"); err != nil {
		t.Fatal(err)
	}
	if id, _ := settingsTreeSheetState(st); id != "magpie-personas" {
		t.Fatalf("人设面板未打开: %q", id)
	}
	closeSettingsSheet(st)
	tt.Frame()
	if err := tt.Click("打开 本机网关"); err != nil {
		t.Fatal(err)
	}
	if id, _ := settingsTreeSheetState(st); id != "magpie-gateway" {
		t.Fatalf("面板未打开: %q", id)
	}
	waitMagpie(t, tt, func() bool { return tt.HasText("菜单栏额度") && tt.HasText("刷新供应商") })
	waitMagpie(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.endpoints.quotaProviderLoadedFor == "gw" && !st.endpoints.quotaProviderLoading
	})
	st.mu.Lock()
	quotaOK := len(st.endpoints.quotaProviderList) == 2 && st.endpoints.editQuotaProviders["codex"] && !st.endpoints.editQuotaProviders["claude"]
	st.mu.Unlock()
	if !quotaOK {
		t.Fatal("额度供应商或选择状态错误")
	}
	for _, provider := range []string{"claude", "codex"} {
		if _, ok := tt.Find(trayQuotaProviderLabel(provider)); !ok {
			t.Fatalf("额度供应商 %q 不可见: %v", provider, tt.Texts())
		}
	}
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	waitMagpie(t, tt, func() bool { fake.mu.Lock(); defer fake.mu.Unlock(); return len(fake.saveCustomEndpointCalls) == 1 })
	fake.mu.Lock()
	saved, webKey := fake.saveCustomEndpointCalls[0], fake.saveCustomEndpointWebKeys[0]
	fake.mu.Unlock()
	if !saved.Magpie || saved.PersonaWebURL != "https://magpie.example.com:3430" || saved.PersonasJSON != `[{"id":"coder"}]` || !saved.PersonaRules || webKey != "" || len(saved.QuotaProviders) != 1 || saved.QuotaProviders[0] != "codex" {
		t.Fatalf("保存时人设或额度丢失: %+v", saved)
	}
}

func TestSettingsMagpieHub_PersonaCount(t *testing.T) {
	for _, tc := range []struct {
		name, personas, label string
		known                 bool
	}{
		{"零个人设", "[]", "人设 0", true},
		{"未知数量", "", "人设", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := NewFakeClient()
			fake.customEndpoints = []CustomEndpoint{{ID: "gw", Name: "网关", Magpie: true, Enabled: true, HasAPIKey: true, PersonasJSON: tc.personas}}
			fake.SetPiMagpieEndpoints([]PiMagpieEndpoint{{ID: "gw"}})
			tt, st := magpiePageWithFake(t, fake, false)
			if count, known := magpiePersonaCount(st); count != 0 || known != tc.known {
				t.Fatalf("人设数量 = %d, %v", count, known)
			}
			if _, ok := tt.Find(tc.label); !ok {
				t.Fatalf("找不到入口 %q: %v", tc.label, tt.Texts())
			}
			if !tc.known && countExactText(tt.Texts(), "0") != 0 {
				t.Fatalf("未知人设数量不应显示 0: %v", tt.Texts())
			}
		})
	}
}

// TestSettingsMagpieHub_AgentRowsInlineRoute 覆盖：智能体只有 Claude、Codex、pi 三行，
// 每行内嵌直连、仅 mcode、全局三段路由。
func TestSettingsMagpieHub_AgentRowsInlineRoute(t *testing.T) {
	tt, st := magpiePageWithFake(t, NewFakeClient(), true)
	for agent, title := range map[string]string{"claude": "Claude", "codex": "Codex", "pi": "pi"} {
		agent, title := agent, title
		waitMagpie(t, tt, func() bool {
			_, known := magpieAgentRouteValue(st, agent)
			return known && tt.HasText(title)
		})
	}
	if tt.HasText("Cline") {
		t.Fatalf("页面不应再有 Cline 行: %v", tt.Texts())
	}
	for _, option := range []string{"直连", "仅 mcode", "全局"} {
		// 每个分段选项在 Texts() 里出现两次（无障碍标签 + 文本），三个智能体合计 6 次。
		if count := countExactText(tt.Texts(), option); count != 6 {
			t.Fatalf("三个智能体都应有 %s，实际 %d: %v", option, count, tt.Texts())
		}
	}
	for _, title := range []string{"Claude", "Codex", "pi"} {
		if _, ok := tt.Find(magpieAgentRouteLabel(title)); !ok {
			t.Fatalf("缺少 %s 的路由控件: %v", title, tt.Texts())
		}
	}
}

func TestSettingsMagpieHub_CodexRowShowsGlobalMode(t *testing.T) {
	fake := NewFakeClient()
	setting := agentMagpieCodexTestSetting()
	setting.Mode = "global"
	setting.Global.State = "drifted"
	setting.Global.Drifted = []string{"model"}
	fake.SetAgentMagpie(setting)
	tt, st := magpiePageWithFake(t, fake, true)
	waitMagpie(t, tt, func() bool {
		value, known := magpieAgentRouteValue(st, "codex")
		return known && value == "全局" && tt.HasText("Codex")
	})
	// 漂移状态与操作内嵌在 Codex 行的路由区块里。
	waitMagpie(t, tt, func() bool {
		return tt.HasText("配置被改动：model") && tt.HasText("重新写入") && tt.HasText("保留我的修改")
	})
}

func TestSettingsMagpieHub_ClaudeSnapshot(t *testing.T) {
	fake := NewFakeClient()
	fake.SetAgentMagpie(AgentMagpieSetting{Agent: "claude", Mode: "global", Global: AgentMagpieGlobal{Supported: true, State: "missing", Path: "/tmp/claude-settings.json"}})
	tt, st := magpiePageWithFake(t, fake, true)
	waitMagpie(t, tt, func() bool {
		value, known := magpieAgentRouteValue(st, "claude")
		return known && value == "全局" && tt.HasText("Claude")
	})
	waitMagpie(t, tt, func() bool { return tt.HasText("配置文件不存在：/tmp/claude-settings.json") })
}

func TestSettingsMagpieHub_GatewayMoreMenu(t *testing.T) {
	fake := NewFakeClient()
	tt, st := magpiePageWithFake(t, fake, true)
	if err := tt.Click("打开 测试网关"); err != nil {
		t.Fatal(err)
	}
	waitMagpie(t, tt, func() bool { return tt.HasText("启用 测试网关") && tt.HasText("删除") && tt.HasText("刷新") })
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	waitMagpie(t, tt, func() bool { return tt.HasText("确认删除") })
	fake.mu.Lock()
	count := len(fake.deleteCustomEndpointCalls)
	fake.mu.Unlock()
	if count != 0 {
		t.Fatal("确认前不能删除")
	}
	if err := tt.Click("确认删除"); err != nil {
		t.Fatal(err)
	}
	waitMagpie(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.deleteCustomEndpointCalls) == 1 && fake.deleteCustomEndpointCalls[0] == "gw"
	})
	if id, _ := settingsTreeSheetState(st); id != "" {
		t.Fatalf("删除后面板未关闭: %q", id)
	}
}

func TestMagpieModelSource(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.piMagpie.loaded = true
	st.piMagpie.endpoints = []PiMagpieEndpoint{{ID: "gw", ProviderID: "magpie-gw", Selected: []string{"auto/model"}}}
	if id, ok := magpieModelSource(st, "pi", "magpie-gw/auto/model"); !ok || id != "gw" {
		t.Fatalf("匹配结果 = %q, %v", id, ok)
	}
	if _, ok := magpieModelSource(st, "pi", "other/auto/model"); ok {
		t.Fatal("不同供应商不应匹配")
	}
	st.piMagpie.loading = true
	if _, ok := magpieModelSource(st, "pi", "magpie-gw/auto/model"); ok {
		t.Fatal("加载中不应返回缓存匹配")
	}
}

func TestMagpieConnected(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.endpoints.loaded = true
	if connected, known := magpieConnected(st); connected || !known {
		t.Fatalf("空列表 = %v, %v", connected, known)
	}
	st.endpoints.endpoints = []CustomEndpoint{{ID: "gw", Magpie: true, Enabled: true, HasAPIKey: true}}
	st.piMagpie.loading = true
	if connected, known := magpieConnected(st); connected || known {
		t.Fatalf("检测中 = %v, %v", connected, known)
	}
	st.piMagpie.loading = false
	st.piMagpie.loaded = true
	st.piMagpie.endpoints = []PiMagpieEndpoint{{ID: "gw"}}
	if connected, known := magpieConnected(st); !connected || !known {
		t.Fatalf("检测成功 = %v, %v", connected, known)
	}
}

func TestMagpieGatewaySheetBody_SaveKeepsKey(t *testing.T) {
	fake := NewFakeClient()
	gw := CustomEndpoint{ID: "gw", Name: "旧名称", BaseURL: "https://example.com/v1", Magpie: true, Enabled: true, HasAPIKey: true}
	fake.customEndpoints = []CustomEndpoint{gw}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
	st.endpoints.loaded = true
	st.endpoints.endpoints = []CustomEndpoint{gw}
	tt := ui.NewTester(func(c *ui.Context) { renderMagpieGatewaySheetBody(c, st, "gw") }, 1200, 4000)
	if err := tt.Click("网关名称"); err != nil {
		t.Fatal(err)
	}
	tt.Type("新名称")
	if err := tt.Click("编辑网关默认模型"); err != nil {
		t.Fatal(err)
	}
	tt.Type("magpie-model")
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	waitMagpie(t, tt, func() bool { fake.mu.Lock(); defer fake.mu.Unlock(); return len(fake.saveCustomEndpointCalls) > 0 })
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.saveCustomEndpointCalls[0].Name == gw.Name || fake.saveCustomEndpointKeys[0] != "" {
		t.Fatal("名称未更新或留空密钥覆盖原值")
	}
	if fake.saveCustomEndpointCalls[0].Model != "magpie-model" {
		t.Fatalf("默认模型未写入: %+v", fake.saveCustomEndpointCalls[0])
	}
}
