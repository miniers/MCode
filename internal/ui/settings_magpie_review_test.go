package mcodeui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestMagpiePageShowsLoadErrorWithRetry(t *testing.T) {
	fake := NewFakeClient()
	fake.SetPiMagpieEndpoints([]PiMagpieEndpoint{{ID: "gw"}})
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(&routeTestClient{FakeClient: fake}))
	st.mu.Lock()
	st.providerRows.loaded = true
	st.providerRows.available = map[string]bool{"claude": false, "codex": false, "pi": false}
	st.agentMagpieFor("mcode").loaded = true
	st.endpoints.loaded = true
	st.endpoints.endpoints = []CustomEndpoint{{
		ID:        "gw",
		Name:      "测试网关",
		BaseURL:   "https://example.com/v1",
		Enabled:   true,
		Magpie:    true,
		HasAPIKey: true,
	}}
	st.piMagpie.err = "pi 端点读取失败"
	st.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) { renderPageMagpie(c, st) }, 1000, 4000)
	tt.Frame()

	if !tt.HasText("加载失败：pi 端点读取失败") {
		t.Fatalf("应显示加载失败文本，实际: %v", tt.Texts())
	}
	if _, ok := tt.Find("重试"); !ok {
		t.Fatalf("应显示重试按钮，实际: %v", tt.Texts())
	}
	if tt.HasText("正在加载…") {
		t.Fatalf("加载失败时不应显示正在加载，实际: %v", tt.Texts())
	}

	if err := tt.Click("重试"); err != nil {
		t.Fatalf("点击重试失败: %v", err)
	}
	waitMagpie(t, tt, func() bool {
		connected, known := magpieConnected(st)
		return known && connected
	})
}

func TestAgentMagpieLoadErrorRenderedOnce(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.mu.Lock()
	st.agentMagpieFor("claude").err = "读取失败"
	st.mu.Unlock()

	row := magpieAgentRouteRow("claude")
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSetting(c, st, row) }, 1000, 1000)
	tt.Frame()

	count := 0
	for _, text := range tt.Texts() {
		if strings.Contains(text, "加载 Magpie 设置失败") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("加载 Magpie 设置失败应只出现一次，实际 %d 次: %v", count, tt.Texts())
	}
}

func TestProvidersCardShowsEndpointsError(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.mu.Lock()
	st.providerRows.loaded = true
	st.presets.loaded = true
	st.endpoints.loaded = true
	st.endpoints.err = "自定义端点加载失败"
	st.endpoints.failedAt = time.Now()
	st.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) { renderProvidersCard(c, st, nil) }, 1000, 4000)
	tt.Frame()

	if !tt.HasText("自定义端点加载失败") {
		t.Fatalf("供应商卡片应显示自定义端点加载失败，实际: %v", tt.Texts())
	}
	if _, ok := tt.Find("重试"); !ok {
		t.Fatalf("供应商卡片应显示重试按钮，实际: %v", tt.Texts())
	}
}
