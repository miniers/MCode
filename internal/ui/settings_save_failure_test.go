package mcodeui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/config"
	"mcode/internal/testenv"
)

func TestOpenInAddKeepsDraftOnSaveFailure(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfgMu.Lock()
	st.cfg.Workbench.OpenInApps = []config.OpenInAppConfig{
		{Name: "Cursor", AppPath: "/Applications/Cursor.app", Enabled: true},
	}
	st.cfgMu.Unlock()
	st.saveConfigFn = func(string, func(*config.Config) error) error {
		return errors.New("disk full")
	}

	wantPath := "/Applications/Zed" + openInAppPathSuffix()
	ps := getOpenInPageState(st)
	ps.adding = true
	ps.draftName = "Zed"
	ps.draftPath = wantPath

	tt := ui.NewTester(func(c *ui.Context) { renderPageOpenIn(c, st) }, 800, 600)
	tt.Frame()
	if err := tt.Click("添加"); err != nil {
		t.Fatalf("点击添加失败: %v", err)
	}
	settingsSaveWriters.Wait()

	st.cfgMu.Lock()
	apps := append([]config.OpenInAppConfig(nil), st.cfg.Workbench.OpenInApps...)
	st.cfgMu.Unlock()
	if len(apps) != 1 || apps[0].Name != "Cursor" {
		t.Fatalf("保存失败后外部应用列表应回滚，实际 %+v", apps)
	}
	if !ps.adding {
		t.Fatal("保存失败后添加表单应保持打开")
	}
	if ps.draftName != "Zed" || ps.draftPath != wantPath {
		t.Fatalf("保存失败后草稿不应清空，实际 name=%q path=%q", ps.draftName, ps.draftPath)
	}
	if ps.err == "" {
		t.Fatal("保存失败后应保留错误提示")
	}
}

func TestSwarmPairSaveRollsBackOnFailure(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)

	existing := swarmPairSave(st, config.SwarmPair{
		Name:           "Existing Pair",
		LeadProvider:   config.ProviderClaude,
		LeadModel:      "claude-3-7-sonnet",
		WorkerProvider: config.ProviderCodex,
		WorkerModel:    "gpt-4o",
	}, true)
	if existing.ID == "" {
		t.Fatal("前置保存配对失败")
	}

	st.saveConfigFn = func(string, func(*config.Config) error) error {
		return errors.New("disk full")
	}

	uiSt := getSwarmUIState(st)
	uiSt.openPopoverKey = "add"
	uiSt.isNew = true
	uiSt.draft = config.SwarmPair{
		Name:           "New Pair",
		LeadProvider:   config.ProviderClaude,
		LeadModel:      "claude-3-7-sonnet",
		WorkerProvider: config.ProviderCodex,
		WorkerModel:    "gpt-4o-mini",
	}

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(1100).Children(func() {
			renderSwarmPairsBody(c, st)
		})
	}, 1200, 1200)
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatalf("点击保存失败: %v", err)
	}
	settingsSaveWriters.Wait()

	if uiSt.openPopoverKey != "add" {
		t.Fatalf("保存失败后不应关闭配对浮层，实际 openPopoverKey=%q", uiSt.openPopoverKey)
	}
	st.cfgMu.Lock()
	pairs := append([]config.SwarmPair(nil), st.cfg.Orchestration.Pairs...)
	st.cfgMu.Unlock()
	if len(pairs) != 1 || pairs[0].ID != existing.ID || pairs[0].Name != "Existing Pair" {
		t.Fatalf("保存失败后 st.cfg.Orchestration.Pairs 应回滚，实际 %+v", pairs)
	}

	if swarmPairDelete(st, existing.ID) {
		t.Fatal("删除配对保存失败时应返回 false")
	}
	st.cfgMu.Lock()
	pairsAfterDelete := append([]config.SwarmPair(nil), st.cfg.Orchestration.Pairs...)
	st.cfgMu.Unlock()
	if len(pairsAfterDelete) != 1 || pairsAfterDelete[0].ID != existing.ID {
		t.Fatalf("删除配对保存失败后应回滚，实际 %+v", pairsAfterDelete)
	}
}

func TestDeleteTierRollsBackWithSameError(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)

	const sameErr = "same write failure"
	st.saveConfigFn = func(string, func(*config.Config) error) error {
		return errors.New(sameErr)
	}
	st.cfgMu.Lock()
	st.errorMessage = sameErr
	wantCount := len(st.cfg.Orchestration.Tiers)
	st.cfgMu.Unlock()

	if deleteTier(st, "deep") {
		t.Fatal("保存失败时 deleteTier 应返回 false")
	}

	st.cfgMu.Lock()
	tiers := append([]config.SwarmTier(nil), st.cfg.Orchestration.Tiers...)
	st.cfgMu.Unlock()
	if len(tiers) != wantCount {
		t.Fatalf("错误文本相同时仍应回滚档位数量，期望 %d，实际 %d", wantCount, len(tiers))
	}
	found := false
	for _, tier := range tiers {
		if tier.ID == "deep" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("回滚后 deep 档位应仍在列表中")
	}
}

func TestMCPToolFlagRollsBackOnFailure(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	dataDir := settingsTestDir(t)
	// 用同名目录占住策略文件路径与全局 MCP 配置文件，使写盘失败。
	if err := os.Mkdir(agentconf.MCPToolPolicyPath(dataDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "mcp.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	st := NewSettingsState(dataDir, nil, nil)
	uiSt := getMCPUIState(st)
	uiSt.autoApproveTools["srv"] = map[string]bool{"existing": true}

	setMCPToolFlag(uiSt, uiSt.disabledTools, "srv", "tool-a", true)
	if uiSt.disabledTools["srv"]["tool-a"] {
		t.Fatal("单条开关保存失败后应回滚内存状态")
	}
	if _, exists := uiSt.disabledTools["srv"]; exists {
		t.Fatalf("原本不存在的服务映射保存失败后应恢复移除，实际 %+v", uiSt.disabledTools["srv"])
	}
	if uiSt.policyError == "" {
		t.Fatal("策略保存失败后应保留错误提示")
	}

	setMCPToolFlags(uiSt, uiSt.autoApproveTools, "srv", []string{"existing", "tool-b"}, false)
	if !uiSt.autoApproveTools["srv"]["existing"] || uiSt.autoApproveTools["srv"]["tool-b"] {
		t.Fatalf("批量开关保存失败后应恢复原值，实际 %+v", uiSt.autoApproveTools["srv"])
	}

	setMCPFoldEnabled(st, false)
	if uiSt.foldThreshold != 0 {
		t.Fatalf("折叠开关保存失败后 foldThreshold 应恢复为 0，实际 %d", uiSt.foldThreshold)
	}

	srv := agentconf.MCPServer{Name: "ctx7", Agent: "claude", Scope: "global"}
	key := "claude:global:ctx7"
	uiSt.mu.Lock()
	uiSt.newThreadDisabled = nil
	uiSt.mu.Unlock()
	tt := ui.NewTester(func(c *ui.Context) {
		renderMCPNewThreadDefaultRow(c, st, uiSt, srv, key)
	}, 800, 200)
	tt.Frame()
	if err := tt.Click("切换" + srv.Name); err != nil {
		t.Fatalf("点击新会话默认启用开关失败: %v", err)
	}
	uiSt.mu.Lock()
	disabledAfter := uiSt.newThreadDisabled[key]
	ntErr := uiSt.newThreadDisabledErr
	uiSt.mu.Unlock()
	if disabledAfter {
		t.Fatal("新会话默认启用保存失败后应回滚内存状态")
	}
	if ntErr == "" {
		t.Fatal("新会话默认启用保存失败后应保留错误提示")
	}
}
