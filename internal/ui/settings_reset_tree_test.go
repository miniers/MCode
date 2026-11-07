package mcodeui

import (
	"testing"

	"github.com/egoist/mygo"

	"mcode/internal/config"
)

// TestSettingsResetTree_ThemeApplied 覆盖外观页重置：
// 主题、文字大小与终端字号回到默认值，并立刻把默认主题应用到运行时。
func TestSettingsResetTree_ThemeApplied(t *testing.T) {
	tt, st := newSettingsAdvancedTester(t)
	openSettingsAdvancedPage(t, tt, st, "appearance")
	def := config.Defaults()

	st.cfg.Appearance.Theme = config.ThemeDark
	st.cfg.Appearance.TextScale = 130
	st.cfg.Workbench.TerminalFontSize = def.Workbench.TerminalFontSize + 1
	tt.Frame()

	prevApply := applyThemeSource
	prevApplied := appearanceThemeApplied
	applied := make([]mygo.ThemeSource, 0, 1)
	applyThemeSource = func(src mygo.ThemeSource) { applied = append(applied, src) }
	appearanceThemeApplied = config.ThemeDark
	t.Cleanup(func() {
		applyThemeSource = prevApply
		appearanceThemeApplied = prevApplied
	})

	applySettingsPageReset(st, "appearance")

	if st.cfg.Appearance.Theme != def.Appearance.Theme {
		t.Fatalf("主题应恢复默认 %q，实际 %q", def.Appearance.Theme, st.cfg.Appearance.Theme)
	}
	if st.cfg.Appearance.TextScale != def.Appearance.TextScale {
		t.Fatalf("文字大小应恢复默认 %d，实际 %d", def.Appearance.TextScale, st.cfg.Appearance.TextScale)
	}
	if st.cfg.Workbench.TerminalFontSize != def.Workbench.TerminalFontSize {
		t.Fatalf("终端字号应恢复默认 %d，实际 %d", def.Workbench.TerminalFontSize, st.cfg.Workbench.TerminalFontSize)
	}
	if len(applied) != 1 || applied[0] != mygo.ThemeSource(def.Appearance.Theme) {
		t.Fatalf("重置后应立即应用默认主题 %q，实际应用 %v", def.Appearance.Theme, applied)
	}
	if appearanceThemeApplied != def.Appearance.Theme {
		t.Fatalf("主题缓存应更新为默认值 %q，实际 %q", def.Appearance.Theme, appearanceThemeApplied)
	}
}

// TestSettingsResetTree_KeepsAccountsAndKeys 覆盖不可重置的数据：
// 模型页没有可重置项，重置不会清掉收藏、账号鉴权与 Magpie 网关；
// 远程页可重置，但钥匙串里的令牌不属于配置段，不会被动到。
func TestSettingsResetTree_KeepsAccountsAndKeys(t *testing.T) {
	_, st := newSettingsAdvancedTester(t)

	pins := []config.ModelPin{{Provider: "claude", ModelID: "sonnet"}}
	gateways := []config.CustomEndpointConfig{{
		ID:      "magpie",
		Name:    "Magpie",
		BaseURL: config.DefaultMagpieBaseURL,
		Magpie:  true,
		Enabled: true,
	}}
	st.cfg.Models.Pins = pins
	st.cfg.Providers.Claude.Auth = "apiKey"
	st.cfg.Providers.Custom = gateways

	if _, ok := settingsResetTargetFor("models", st); ok {
		t.Fatal("模型页只有收藏、账号与网关，不应被视为可重置页面")
	}
	applySettingsPageReset(st, "models")

	if len(st.cfg.Models.Pins) != 1 || st.cfg.Models.Pins[0] != pins[0] {
		t.Fatalf("重置不应改动收藏模型，实际 %#v", st.cfg.Models.Pins)
	}
	if st.cfg.Providers.Claude.Auth != "apiKey" {
		t.Fatalf("重置不应改动账号鉴权方式，实际 %q", st.cfg.Providers.Claude.Auth)
	}
	if len(st.cfg.Providers.Custom) != 1 || st.cfg.Providers.Custom[0].ID != "magpie" {
		t.Fatalf("重置不应改动 Magpie 网关，实际 %#v", st.cfg.Providers.Custom)
	}

	st.remote.tlsHasToken = true
	st.cfg.Remote.Enabled = true
	applySettingsPageReset(st, "remote")

	if st.cfg.Remote.Enabled {
		t.Fatal("远程页重置应关闭远程服务")
	}
	if !st.remote.tlsHasToken {
		t.Fatal("远程页重置不应清掉钥匙串里的 Cloudflare 令牌")
	}
	if len(st.cfg.Providers.Custom) != 1 || st.cfg.Providers.Custom[0].ID != "magpie" {
		t.Fatalf("远程页重置不应改动 Magpie 网关，实际 %#v", st.cfg.Providers.Custom)
	}
}

// TestSettingsResetTree_PageWithoutResettableItems 没有可重置行的页面返回 false。
func TestSettingsResetTree_PageWithoutResettableItems(t *testing.T) {
	_, st := newSettingsAdvancedTester(t)
	for _, page := range []string{"models", "magpie", "missing"} {
		if target, ok := settingsResetTargetFor(page, st); ok {
			t.Fatalf("%s 页不应有重置目标，实际得到 %+v", page, target)
		}
	}
}
