package mcodeui

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
	"mcode/internal/providers/agy/accounts"
)

// TestUIReview16_MagpieMatrixCacheAndSingleLoading 验证：
// (a) 版本号与数据源不变时，连续多帧渲染 Magpie 矩阵只构建一次；
// 数据源或版本号变化后会重新构建；多个未加载 agent 只显示一次「正在加载…」。
func TestUIReview16_MagpieMatrixCacheAndSingleLoading(t *testing.T) {
	fake := NewFakeClient()
	gateway := CustomEndpoint{ID: "gw", Name: "网关", Magpie: true, Enabled: true, HasAPIKey: true}
	endpoints := []PiMagpieEndpoint{{
		ID:       "gw",
		Name:     "网关",
		Models:   []PiMagpieModel{{ID: "claude-opus-4", Name: "Claude Opus 4"}, {ID: "gpt-5", Name: "GPT-5"}},
		Selected: []string{"claude-opus-4"},
	}}
	settings := make(map[string]AgentMagpieSetting, len(magpieMatrixAgents))
	for _, agent := range magpieMatrixAgents {
		settings[agent] = AgentMagpieSetting{Agent: agent, Endpoints: endpoints}
	}
	st := matrixTestState(t, fake, []CustomEndpoint{gateway}, settings)

	before := magpieMatrixBuilds.Load()
	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsTreeSetting(c, st, setting{ID: "row-magpie-model-matrix", Control: customFullWidth(renderMagpieModelMatrix)})
	}, 1000, 1200)
	// NewTester 已跑过第 1 帧，再连续渲染 4 帧不应再次构建。
	for range 4 {
		tt.Frame()
	}
	afterSteady := magpieMatrixBuilds.Load()
	if afterSteady != before+1 {
		t.Fatalf("连续渲染构建次数 = %d，期望只构建 1 次（before=%d）", afterSteady-before, before)
	}

	// 递增版本号后再渲染一帧应触发重建。
	magpieMatrixVersion.Add(1)
	tt.Frame()
	afterBump := magpieMatrixBuilds.Load()
	if afterBump != afterSteady+1 {
		t.Fatalf("递增版本号后构建次数 = %d，期望 +1", afterBump-afterSteady)
	}

	// 多个 agent 均未加载时，「正在加载…」只显示一次。
	stLoading := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
	stLoading.mu.Lock()
	stLoading.endpoints.loaded = true
	stLoading.endpoints.endpoints = []CustomEndpoint{gateway}
	for _, agent := range magpieMatrixAgents {
		state := stLoading.agentMagpieFor(agent)
		state.loading = true
	}
	stLoading.piMagpie.loading = true
	stLoading.mu.Unlock()

	ttLoading := ui.NewTester(func(c *ui.Context) {
		renderMagpieModelMatrix(c, stLoading)
	}, 1000, 1200)
	loadingCount := 0
	for _, txt := range ttLoading.Texts() {
		if txt == "正在加载…" {
			loadingCount++
		}
	}
	if loadingCount != 1 {
		t.Fatalf("「正在加载…」出现次数 = %d，期望 1 次；texts=%v", loadingCount, ttLoading.Texts())
	}
}

// TestUIReview16_RemotePortValidationError 验证：
// (b) HTTP 端口输入 "0"、"80" 或 "abc" 时显示「端口需为 1024–65535」，TLS 端口输入 "0" 或 "abc" 时显示「端口需为 1–65535」，且不修改配置。
func TestUIReview16_RemotePortValidationError(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117, StartedListen: "0.0.0.0:43117"}
	fake.remoteTLSStatus = RemoteTLSStatus{Configured: true, Domain: "mcode.example.com", TLSPort: 8443}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.Listen = "0.0.0.0:43117"
	cfg.Remote.TLSListen = "0.0.0.0:8443"
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, st := openRemoteSettingsPageState(t, fake, dataDir)
	tt.SetSize(1000, 4000)
	waitFor(t, tt, func() bool { return tt.HasText("监听端口") && tt.HasText("HTTPS 端口") })

	for _, bad := range []string{"0", "80", "abc"} {
		clickRemoteSettingsLabel(t, tt, "编辑监听端口")
		tt.Command("selectAll")
		tt.Type(bad)
		tt.Frame()
		if !tt.HasText("端口需为 1024–65535") {
			t.Fatalf("HTTP 端口输入 %q 后未出现错误提示: %v", bad, tt.Texts())
		}
		st.mu.Lock()
		listen := st.cfg.Remote.Listen
		st.mu.Unlock()
		if listen != "0.0.0.0:43117" {
			t.Fatalf("HTTP 端口输入 %q 不应改内存配置，实际 %q", bad, listen)
		}
		loaded, err := config.Load(dataDir)
		if err != nil || loaded.Remote.Listen != "0.0.0.0:43117" {
			t.Fatalf("HTTP 端口输入 %q 不应落盘，实际 %q (err=%v)", bad, loaded.Remote.Listen, err)
		}

		// 恢复合法 HTTP 端口，使错误提示消失，再单独测 HTTPS 端口。
		tt.Command("selectAll")
		tt.Type("43117")
		tt.Frame()
	}

	for _, bad := range []string{"0", "abc"} {
		clickRemoteSettingsLabel(t, tt, "编辑HTTPS 端口")
		tt.Command("selectAll")
		tt.Type(bad)
		tt.Frame()
		if !tt.HasText("端口需为 1–65535") {
			t.Fatalf("TLS 端口输入 %q 后未出现错误提示: %v", bad, tt.Texts())
		}
		st.mu.Lock()
		tlsListen := st.cfg.Remote.TLSListen
		st.mu.Unlock()
		if tlsListen != "0.0.0.0:8443" {
			t.Fatalf("TLS 端口输入 %q 不应改内存配置，实际 %q", bad, tlsListen)
		}
		loaded, err := config.Load(dataDir)
		if err != nil || loaded.Remote.TLSListen != "0.0.0.0:8443" {
			t.Fatalf("TLS 端口输入 %q 不应落盘，实际 %q (err=%v)", bad, loaded.Remote.TLSListen, err)
		}

		tt.Command("selectAll")
		tt.Type("8443")
		tt.Frame()
	}
}

// TestUIReview16_ConfirmDialogs 验证：
// (c) 点「清除」Cloudflare Token 与 Swarm 删除档位时先出现 Danger 确认框，确认后才执行原逻辑。
func TestUIReview16_ConfirmDialogs(t *testing.T) {
	t.Run("清除CloudflareToken确认框", func(t *testing.T) {
		fake := NewFakeClient()
		fake.mu.Lock()
		fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
		fake.remoteTLSStatus = RemoteTLSStatus{
			Configured:         true,
			Domain:             "mcode.example.com",
			TLSPort:            8443,
			HasCloudflareToken: true,
		}
		fake.mu.Unlock()

		dataDir := settingsTestDir(t)
		cfg := config.Defaults()
		cfg.Remote.TLSListen = "0.0.0.0:8443"
		if err := config.Save(dataDir, cfg); err != nil {
			t.Fatal(err)
		}

		tt, _ := openRemoteSettingsPageState(t, fake, dataDir)
		tt.SetSize(1000, 4000)
		waitFor(t, tt, func() bool { return tt.HasText("清除") })

		clickRemoteSettingsLabel(t, tt, "清除 Token")
		waitFor(t, tt, func() bool { return tt.HasText("清除 Cloudflare API Token？") })

		fake.mu.Lock()
		callsBefore := len(fake.saveRemoteTokenCalls)
		fake.mu.Unlock()
		if callsBefore != 0 {
			t.Fatalf("确认前不应清除 Token，saveRemoteTokenCalls=%d", callsBefore)
		}

		if err := tt.Click("确认清除 Cloudflare API Token"); err != nil {
			t.Fatal(err)
		}
		waitRemoteCondition(t, func() bool {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			return len(fake.saveRemoteTokenCalls) == 1 && fake.saveRemoteTokenCalls[0] == ""
		})
	})

	t.Run("删除Swarm档位确认框", func(t *testing.T) {
		st, host, _, _ := setupSwarmTierFlyoutTest(t)
		if err := host.Click("deep"); err != nil {
			t.Fatal(err)
		}
		uiSt := getSwarmTiersUIState(st)
		editor := ui.NewTester(uiSt.editor.view, 360, 1200)
		editor.Frame()

		if err := editor.Click("删除"); err != nil {
			t.Fatal(err)
		}
		if !editor.HasText("删除档位「") {
			t.Fatalf("点删除后应先弹出确认框，实际Texts=%v", editor.Texts())
		}
		st.mu.Lock()
		tierCountBefore := len(st.cfg.Orchestration.Tiers)
		st.mu.Unlock()
		if tierCountBefore < 2 {
			t.Fatalf("确认前档位不应被删除，当前=%d", tierCountBefore)
		}

		if err := editor.Click("确认删除档位"); err != nil {
			t.Fatal(err)
		}
		st.mu.Lock()
		tierCountAfter := len(st.cfg.Orchestration.Tiers)
		st.mu.Unlock()
		if tierCountAfter != tierCountBefore-1 {
			t.Fatalf("确认后应删除档位，before=%d after=%d", tierCountBefore, tierCountAfter)
		}
	})
}

// TestUIReview16_RemoteLANIPCache 验证：
// (d) 连续渲染多帧时取局域网 IP 的函数不会每帧调用，且缓存过期后可在后台刷新。
func TestUIReview16_RemoteLANIPCache(t *testing.T) {
	prev := remoteLANIPsFunc
	t.Cleanup(func() { remoteLANIPsFunc = prev })

	var calls atomic.Int32
	remoteLANIPsFunc = func() []string {
		calls.Add(1)
		return []string{"192.168.50.10"}
	}

	chat := &chatState{}
	st := &SettingsState{chat: chat}
	st.remote.status = RemoteStatus{State: RemoteStateRunning, Port: 43117, StartedListen: "0.0.0.0:43117"}

	tt := ui.NewTester(func(c *ui.Context) {
		remoteEntryControl{}.render(c, st, setting{})
	}, 600, 100)

	waitRemoteCondition(t, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.remote.lanIPFetchedAt.IsZero() && st.remote.lanIP == "192.168.50.10"
	})

	for range 10 {
		tt.Frame()
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("10 秒缓存期内连续渲染多帧应只调用 1 次取 IP 函数，实际 %d 次", got)
	}

	// 模拟缓存超过 10 秒：再次渲染触发一次后台刷新，且值变化时调用 invalidate。
	genBefore := chat.invalidateGen.Load()
	remoteLANIPsFunc = func() []string {
		calls.Add(1)
		return []string{"192.168.50.20"}
	}
	st.mu.Lock()
	st.remote.lanIPFetchedAt = time.Now().Add(-remoteLANIPCacheTTL - time.Second)
	st.mu.Unlock()

	tt.Frame()
	tt.Frame()
	waitRemoteCondition(t, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.remote.lanIP == "192.168.50.20" && !st.remote.lanIPRefreshing
	})
	if got := calls.Load(); got != 2 {
		t.Fatalf("缓存过期后连续渲染应再刷新 1 次，总调用期望 2，实际 %d", got)
	}
	if chat.invalidateGen.Load() <= genBefore {
		t.Fatal("局域网 IP 变化后应调用 invalidate")
	}
}

// TestUIReview16_A11yLabelsAndRemovedCopy 验证：
// 任务 5 的无障碍 Label（Agy 删除账号、远程撤销设备）与任务 6 删除的多余文案。
func TestUIReview16_A11yLabelsAndRemovedCopy(t *testing.T) {
	// 1. Agy 删除按钮 Label 与内联说明删除
	fake := NewFakeClient()
	fake.agyAccounts = []accounts.Account{
		{ID: "acc-named", Label: "工作号", Email: "work@example.com", Status: accounts.StatusActive},
		{ID: "acc-id-only", Status: accounts.StatusActive},
	}
	fake.agyDefaultAccountID = "other"

	ttAgy, _ := openAgySettingsTest(t, fake)
	waitFor(t, ttAgy, func() bool { return ttAgy.HasText("工作号") })
	if ttAgy.HasText("只修改 MCode 自己的账号配置，不改动系统 ~/.gemini") {
		t.Fatal("应已删除 Agy 账号操作行里的内联配置说明")
	}
	if _, ok := ttAgy.Find("删除账号 工作号"); !ok {
		t.Fatalf("缺少无障碍 Label「删除账号 工作号」")
	}
	if _, ok := ttAgy.Find("删除账号 acc-id-only"); !ok {
		t.Fatalf("账号名为空时缺少无障碍 Label「删除账号 acc-id-only」")
	}

	// 2. Swarm 预设面板副标题删除
	stPresets := NewSettingsState(settingsTestDir(t), nil, nil)
	ttPresets := ui.NewTester(func(c *ui.Context) {
		renderSwarmPresetsPanel(c, stPresets, nil)
	}, 1000, 900)
	if err := ttPresets.Click("预设组合"); err != nil {
		t.Fatal(err)
	}
	if ttPresets.HasText("预置的高效协作配对模式，一键添加到当前设置。") {
		t.Fatal("应已删除 Swarm 预设面板副标题")
	}

	// 3. Magpie 人设来源提示语删除
	stHub := NewSettingsState(settingsTestDir(t), nil, nil)
	ttHub := ui.NewTester(func(c *ui.Context) {
		renderMagpieGatewayForm(c, stHub, "", "", "", "", "", false, false, false, false, "", PersonaSourceTestResult{}, false, "", nil, nil, false, false, false, "", "", nil, "hub")
	}, 800, 1200)
	if ttHub.HasText("本机 magpie 会自动读取；远端 magpie 请配置 web 管理接口，或粘贴人设列表") {
		t.Fatal("应已删除 Magpie 人设来源提示语")
	}

	// 4. Swarm 档位写入白名单重复说明删除
	stTier, hostTier, _, _ := setupSwarmTierFlyoutTest(t)
	if err := hostTier.Click("deep"); err != nil {
		t.Fatal(err)
	}
	tierEditor := ui.NewTester(getSwarmTiersUIState(stTier).editor.view, 360, 1200)
	tierEditor.Frame()
	if tierEditor.HasText("每行一个路径通配，留空则不限制") {
		t.Fatal("应已删除 Swarm 档位写入白名单下面重复的说明")
	}
}
