package mcodeui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/config"
	"mcode/internal/testenv"
)

// TestSettings_MCPAddAndDelete 测试自定义 MCP 服务的表单新增与删除 UI 交互流程
func TestSettings_MCPAddAndDelete(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)

	memSecrets := config.NewMemorySecretStore()
	agentconf.SetSecretStore(memSecrets)
	defer func() {
		agentconf.SetSecretStore(config.NewMemorySecretStore())
	}()

	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 6000)

	waitFor(t, tt, func() bool {
		return tt.HasText("设置")
	})

	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}

	clickExtensionsMCPTab(t, tt)

	// 1. 打开添加面板并选择手动填写。
	openMCPAddSheet(t, tt)
	waitFor(t, tt, func() bool { return tt.HasText("手动填写") })
	if err := tt.Click("手动填写"); err != nil {
		t.Fatalf("click '手动填写' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("服务名称") && tt.HasText("保存")
	})

	// 2. 填写表单
	if err := tt.Click("编辑服务名称"); err != nil {
		t.Fatalf("click '编辑服务名称' failed: %v", err)
	}
	tt.Type("my-test-mcp")

	if err := tt.Click("编辑执行命令"); err != nil {
		t.Fatalf("click '编辑执行命令' failed: %v", err)
	}
	tt.Type("echo")

	// 3. 点击保存
	if err := tt.Click("保存"); err != nil {
		t.Fatalf("click '保存' failed: %v", err)
	}

	// 4. 服务出现在列表后打开详情，工具默认展开。
	openMCPDetail(t, tt, "my-test-mcp")
	waitFor(t, tt, func() bool {
		return tt.HasText("删除服务") && (tt.HasText("连接后可见") || tt.HasText("收起工具") || tt.HasText("探测失败"))
	})

	// 6. 点击删除：删除会连凭据一起清掉，必须先出现二次确认框
	if err := tt.Click("删除服务"); err != nil {
		t.Fatalf("click '删除服务' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("将删除 my-test-mcp（全局）的配置与已保存的凭据")
	})

	// 7. 确认后才真正删除
	if err := tt.Click("确认删除 MCP 服务"); err != nil {
		t.Fatalf("click '确认删除 MCP 服务' failed: %v", err)
	}

	// 8. 验证服务已从列表中移除
	waitFor(t, tt, func() bool {
		return !tt.HasText("my-test-mcp")
	})
}

// TestMCPServerDeleteRequiresConfirm 覆盖：删除要二次确认（S-076），取消后服务仍在，确认后才消失。
func TestMCPServerDeleteRequiresConfirm(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)

	memSecrets := config.NewMemorySecretStore()
	agentconf.SetSecretStore(memSecrets)
	defer func() {
		agentconf.SetSecretStore(config.NewMemorySecretStore())
	}()

	home, _ := os.UserHomeDir()
	if err := agentconf.SaveMCPServer(agentconf.Options{Home: home, DataDir: dataDir}, agentconf.MCPServer{
		Name:      "confirm-mcp",
		Agent:     "custom",
		Scope:     "global",
		Transport: "stdio",
		Command:   "echo",
	}); err != nil {
		t.Fatalf("保存自定义 MCP 服务失败: %v", err)
	}

	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 6000)
	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}
	clickExtensionsMCPTab(t, tt)
	openMCPDetail(t, tt, "confirm-mcp")
	waitFor(t, tt, func() bool { return tt.HasText("删除服务") })

	if err := tt.Click("删除服务"); err != nil {
		t.Fatalf("click '删除服务' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("将删除 confirm-mcp（全局）的配置与已保存的凭据")
	})

	// 取消后什么都没删。
	if err := tt.Click("取消删除 MCP 服务"); err != nil {
		t.Fatalf("click '取消删除 MCP 服务' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return !tt.HasText("将删除") && tt.HasText("confirm-mcp")
	})

	// 再点一次删除并确认，服务消失。
	if err := tt.Click("删除服务"); err != nil {
		t.Fatalf("click '删除服务' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("确认删除 MCP 服务") })
	if err := tt.Click("确认删除 MCP 服务"); err != nil {
		t.Fatalf("click '确认删除 MCP 服务' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("confirm-mcp") })
}

func TestMCPServerDeleteModalRenderedOnce(t *testing.T) {
	testenv.TempHome(t)
	dataDir := settingsTestDir(t)
	home, _ := os.UserHomeDir()
	if err := agentconf.SaveMCPServer(agentconf.Options{Home: home, DataDir: dataDir}, agentconf.MCPServer{
		Name: "single-modal-mcp", Agent: "custom", Scope: "global", Transport: "stdio", Command: "echo",
	}); err != nil {
		t.Fatalf("保存自定义 MCP 服务失败: %v", err)
	}

	tt := newThreadDefaultsTestTester(t, dataDir)
	openMCPDetail(t, tt, "single-modal-mcp")
	waitFor(t, tt, func() bool { return tt.HasText("删除服务") })
	if err := tt.Click("删除服务"); err != nil {
		t.Fatalf("click '删除服务' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("取消删除 MCP 服务") })
	count := 0
	for _, text := range tt.Texts() {
		if text == "取消删除 MCP 服务" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("删除确认框应只有一个，取消按钮数量: %d", count)
	}
}

func TestSetMCPToolFlag(t *testing.T) {
	uiSt := getMCPUIState(&SettingsState{})
	setMCPToolFlag(uiSt, uiSt.disabledTools, "srv", "tool-a", true)
	setMCPToolFlag(uiSt, uiSt.autoApproveTools, "srv", "tool-a", true)
	if !uiSt.disabledTools["srv"]["tool-a"] || !uiSt.autoApproveTools["srv"]["tool-a"] {
		t.Fatal("开关应被置位")
	}
	setMCPToolFlag(uiSt, uiSt.disabledTools, "srv", "tool-a", false)
	if uiSt.disabledTools["srv"]["tool-a"] {
		t.Fatal("禁用开关应被清除")
	}
	if !uiSt.autoApproveTools["srv"]["tool-a"] {
		t.Fatal("免审批开关不应受影响")
	}
}

// TestMCPToolFlagPersistsAndReloads 覆盖：切换开关写入策略文件，重建页面状态后开关恢复，关闭开关后文件同步清除。
func TestMCPToolFlagPersistsAndReloads(t *testing.T) {
	dataDir := settingsTestDir(t)
	policyPath := agentconf.MCPToolPolicyPath(dataDir)

	uiSt := getMCPUIState(&SettingsState{dataDir: dataDir})
	if uiSt.policyPath != policyPath {
		t.Fatalf("策略文件路径不符: %s", uiSt.policyPath)
	}
	setMCPToolFlag(uiSt, uiSt.disabledTools, "srv", "tool-a", true)
	setMCPToolFlag(uiSt, uiSt.autoApproveTools, "srv", "tool-b", true)

	policy, err := agentconf.LoadMCPToolPolicy(policyPath)
	if err != nil {
		t.Fatalf("读取策略文件失败: %v", err)
	}
	if !policy.IsDisabled("srv", "tool-a") {
		t.Fatal("禁用开关应写入策略文件")
	}
	if !policy.IsAutoApproved("srv", "tool-b") {
		t.Fatal("免审批开关应写入策略文件")
	}

	// 重建页面状态（新的 SettingsState）后应从文件恢复开关
	reloaded := getMCPUIState(&SettingsState{dataDir: dataDir})
	if !reloaded.disabledTools["srv"]["tool-a"] {
		t.Fatal("重载后禁用状态应恢复")
	}
	if !reloaded.autoApproveTools["srv"]["tool-b"] {
		t.Fatal("重载后免审批状态应恢复")
	}
	if reloaded.policyError != "" {
		t.Fatalf("重载不应产生错误: %s", reloaded.policyError)
	}

	// 关闭开关后文件中的记录应被清除
	setMCPToolFlag(reloaded, reloaded.disabledTools, "srv", "tool-a", false)
	policy, err = agentconf.LoadMCPToolPolicy(policyPath)
	if err != nil {
		t.Fatalf("读取策略文件失败: %v", err)
	}
	if policy.IsDisabled("srv", "tool-a") {
		t.Fatal("关闭开关后文件中不应保留禁用记录")
	}
	if !policy.IsAutoApproved("srv", "tool-b") {
		t.Fatal("关闭禁用开关不应影响免审批记录")
	}
}

// TestMCPToolPolicyLoadErrorSurfaces 覆盖：策略文件损坏时页面状态记录错误。
func TestMCPToolPolicyLoadErrorSurfaces(t *testing.T) {
	dataDir := settingsTestDir(t)
	if err := os.WriteFile(agentconf.MCPToolPolicyPath(dataDir), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	uiSt := getMCPUIState(&SettingsState{dataDir: dataDir})
	if uiSt.policyError == "" {
		t.Fatal("损坏的策略文件应在页面状态中记录错误")
	}
	if !strings.Contains(uiSt.policyError, "读取") {
		t.Fatalf("错误提示应说明读取失败: %s", uiSt.policyError)
	}
}

// TestMCPToolPolicySaveErrorSurfaces 覆盖：策略路径不可写时保存失败记录错误，且不影响内存开关。
func TestMCPToolPolicySaveErrorSurfaces(t *testing.T) {
	dataDir := settingsTestDir(t)
	// 以同名目录占位，使原子写入的 rename 失败
	if err := os.Mkdir(agentconf.MCPToolPolicyPath(dataDir), 0o700); err != nil {
		t.Fatal(err)
	}
	uiSt := getMCPUIState(&SettingsState{dataDir: dataDir})
	setMCPToolFlag(uiSt, uiSt.disabledTools, "srv", "tool-a", true)
	if !uiSt.disabledTools["srv"]["tool-a"] {
		t.Fatal("保存失败不应回滚内存开关")
	}
	if !strings.Contains(uiSt.policyError, "保存") {
		t.Fatalf("错误提示应说明保存失败: %s", uiSt.policyError)
	}
}

// TestMCPServerRowShowsProbeError 覆盖：探测失败要在行内显示错误与「重新检测」，而不是一律写「连接后可见」。
func TestMCPServerRowShowsProbeError(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)

	memSecrets := config.NewMemorySecretStore()
	agentconf.SetSecretStore(memSecrets)
	defer func() {
		agentconf.SetSecretStore(config.NewMemorySecretStore())
	}()

	home, _ := os.UserHomeDir()
	if err := agentconf.SaveMCPServer(agentconf.Options{Home: home, DataDir: dataDir}, agentconf.MCPServer{
		Name:      "broken-mcp",
		Agent:     "custom",
		Scope:     "global",
		Transport: "stdio",
		Command:   "echo",
	}); err != nil {
		t.Fatalf("保存自定义 MCP 服务失败: %v", err)
	}

	oldLister := agentconf.DefaultToolLister
	agentconf.DefaultToolLister = func(ctx context.Context, srv agentconf.MCPServer) ([]agentconf.MCPToolInfo, error) {
		return nil, errors.New("401 unauthorized")
	}
	t.Cleanup(func() { agentconf.DefaultToolLister = oldLister })

	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 6000)
	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}
	clickExtensionsMCPTab(t, tt)
	openMCPDetail(t, tt, "broken-mcp")
	waitFor(t, tt, func() bool {
		return tt.HasText("探测失败：401 unauthorized") && tt.HasText("重新检测")
	})
}

// TestMCPServerRowReadOnly 覆盖：外部 Agent 配置里的只读服务只做展示，
// 必须显示「只读 · 外部配置」徽章，且没有启停开关、编辑、删除按钮。
func TestMCPServerRowReadOnly(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)

	// 写一份 pi 的外部配置，让设置页扫描到只读条目。
	if err := os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".pi", "agent", "mcp.json"), []byte(`{
  "mcpServers": {
    "pi-readonly-mcp": {"command": "pi-mcp", "args": ["--stdio"]}
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 6000)
	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}
	clickExtensionsMCPTab(t, tt)

	openMCPDetail(t, tt, "pi-readonly-mcp")
	waitFor(t, tt, func() bool { return tt.HasText("只读 · 外部配置") })
	// 只读条目不挂启停开关（开关靠 Label 可查），也没有编辑与删除按钮。
	if tt.HasText("切换启用 pi-readonly-mcp") {
		t.Fatal("只读服务不应渲染启停开关")
	}
	// 这里必须用精确匹配：HasText 是子串匹配，会误中页面上其他控件的 Label（如目录搜索框的「编辑…」）。
	if _, ok := tt.Find("编辑"); ok {
		t.Fatal("只读服务不应渲染编辑按钮")
	}
	if _, ok := tt.Find("删除服务"); ok {
		t.Fatal("只读服务不应渲染删除按钮")
	}
}

// clickExtensionsMCPTab 点击设置导航中的「扩展」并切换到「MCP 服务」分段。
func clickExtensionsMCPTab(t *testing.T, tt *ui.Tester) {
	t.Helper()
	waitFor(t, tt, func() bool { return tt.HasText("扩展") })
	if err := tt.Click("扩展"); err != nil {
		t.Fatalf("click '扩展' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("MCP 服务") })
	if err := tt.Click("MCP 服务"); err != nil {
		t.Fatalf("click 'MCP 服务' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("+ 添加服务") })
}

// newThreadDefaultsTestTester 打开设置页的 MCP 页，供「新会话默认」相关用例复用。
func newThreadDefaultsTestTester(t *testing.T, dataDir string) *ui.Tester {
	t.Helper()
	tt := ui.NewTester(Root(NewFakeClient(), WithSettings(dataDir, nil)), 1000, 6000)
	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}
	clickExtensionsMCPTab(t, tt)
	return tt
}

// clickNewThreadToggle 把 MCP 页滚到底再点「新会话默认」里的开关。
// 小节排在服务列表之后，小窗口里常在首屏之外，直接 Click 会报坐标越界。
func clickNewThreadToggle(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	const width, height float32 = 1000, 6000
	tt.Scroll(width/2, height/2, 0, -1e6)
	tt.Frame()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if r, ok := tt.Find(label); ok && r.W > 0 && r.H > 0 &&
			r.X >= 0 && r.Y >= 0 && r.X+r.W <= width && r.Y+r.H <= height {
			tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("元素 %q 始终不可见: %v", label, tt.Texts())
		}
		tt.Scroll(width/2, height/2, 0, 120)
		tt.Frame()
	}
}

// TestMCPNewThreadDefaults 覆盖 A3.2：新会话默认小节列出可注入服务（跳过只读），
// 切换开关写入全局配置，再切一次从名单移除。
func TestMCPNewThreadDefaults(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	dataDir := settingsTestDir(t)

	origScanMCP := scanMCP
	t.Cleanup(func() { scanMCP = origScanMCP })
	scanMCP = func(agentconf.Options) ([]agentconf.MCPServer, error) {
		return []agentconf.MCPServer{
			{Name: "ctx7", Agent: "claude", Scope: "global", Transport: "stdio", Command: "ctx7"},
			{Name: "ctx9", Agent: "claude", Scope: "global", Transport: "stdio", Command: "ctx9"},
			{Name: "pi-readonly", Agent: "pi", Scope: "global", Transport: "stdio", Command: "pi-mcp", ReadOnly: true},
		}, nil
	}

	tt := newThreadDefaultsTestTester(t, dataDir)
	openMCPDetail(t, tt, "ctx7")
	waitFor(t, tt, func() bool { return tt.HasText("新会话与批量策略") })
	if err := tt.Click("新会话与批量策略"); err != nil {
		t.Fatalf("展开高级策略失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("新会话默认启用") })

	// 只读服务不会注入会话，小节里不应出现它的默认开关。
	if tt.HasText("切换pi-readonly") {
		t.Fatal("只读服务不应出现在新会话默认小节")
	}

	opts := agentconf.Options{Home: home, DataDir: dataDir}
	if got := agentconf.NewThreadDisabledServers(opts); len(got) != 0 {
		t.Fatalf("初始名单应为空: %v", got)
	}

	// 关掉 ctx7 的默认启用：名单里出现 claude:global:ctx7。
	clickNewThreadToggle(t, tt, "切换ctx7")
	if got := agentconf.NewThreadDisabledServers(opts); !slices.Contains(got, "claude:global:ctx7") {
		t.Fatalf("关闭后名单应含 claude:global:ctx7: %v", got)
	}
	// 另一个服务不受影响。
	if got := agentconf.NewThreadDisabledServers(opts); slices.Contains(got, "claude:global:ctx9") {
		t.Fatalf("未操作的服务不应进入名单: %v", got)
	}

	// 再点一次打开：名单移除该键。
	clickNewThreadToggle(t, tt, "切换ctx7")
	if got := agentconf.NewThreadDisabledServers(opts); slices.Contains(got, "claude:global:ctx7") {
		t.Fatalf("重新打开后名单不应含 claude:global:ctx7: %v", got)
	}
}

// TestMCPServerRowShowsEngineDisabledBadge 覆盖 A11：引擎侧（Claude Code）关闭的服务
// 要显示徽标，展开区说明原因，且不影响 mcode 自己的开关。
func TestMCPServerRowShowsEngineDisabledBadge(t *testing.T) {
	testenv.TempHome(t)
	dataDir := settingsTestDir(t)

	origScanMCP := scanMCP
	t.Cleanup(func() { scanMCP = origScanMCP })
	scanMCP = func(agentconf.Options) ([]agentconf.MCPServer, error) {
		return []agentconf.MCPServer{{
			Name:             "ctx7",
			Agent:            "claude",
			Scope:            "global",
			Transport:        "stdio",
			Command:          "ctx7",
			DisabledByEngine: true,
		}}, nil
	}

	tt := newThreadDefaultsTestTester(t, dataDir)
	openMCPDetail(t, tt, "ctx7")
	waitFor(t, tt, func() bool { return tt.HasText("已在 Claude Code 关闭") })
	if tt.HasText("这个服务在 Claude Code 里被关掉了（~/.claude.json 的 disabledMcpServers）。MCode 是否注入仍由这里的开关决定。") {
		t.Fatal("重复的 Claude Code 关闭说明不应显示在展开面板")
	}

	// 引擎侧关闭只做展示：mcode 自己的启停开关仍要在。
	if !tt.HasText("切换启用 ctx7") {
		t.Fatal("引擎侧关闭不应隐藏 mcode 的启停开关")
	}
}

// TestMCPPageStateOwnedBySettingsState 覆盖：页面状态挂在 SettingsState 上，
// 同一实例重复取到同一个指针，Close 后字段清空。
func TestMCPPageStateOwnedBySettingsState(t *testing.T) {
	st := &SettingsState{dataDir: settingsTestDir(t)}
	page := getMCPUIState(st)
	if again := getMCPUIState(st); again != page {
		t.Fatal("同一 SettingsState 应复用同一个 MCP 页面状态")
	}
	catalog := getMCPCatalogUIState(st)
	if again := getMCPCatalogUIState(st); again != catalog {
		t.Fatal("同一 SettingsState 应复用同一个预设页面状态")
	}

	st.Close()

	st.mu.Lock()
	defer st.mu.Unlock()
	if st.mcpPage != nil || st.mcpCatalogPage != nil {
		t.Fatal("Close 应清空页面状态")
	}
}

// TestMCPToolBatchSwitchWritesPolicy 覆盖：展开工具后的单工具与批量开关都改内存并写盘。
func TestMCPToolBatchSwitchWritesPolicy(t *testing.T) {
	testenv.TempHome(t)
	dataDir := settingsTestDir(t)
	useMCPToolLister(t, func(ctx context.Context, srv agentconf.MCPServer) ([]agentconf.MCPToolInfo, error) {
		return []agentconf.MCPToolInfo{{Name: "tool-a"}, {Name: "tool-b"}}, nil
	})

	home, _ := os.UserHomeDir()
	if err := agentconf.SaveMCPServer(agentconf.Options{Home: home, DataDir: dataDir}, agentconf.MCPServer{
		Name: "batch-mcp", Agent: "custom", Scope: "global", Transport: "stdio", Command: "echo",
	}); err != nil {
		t.Fatalf("保存自定义 MCP 服务失败: %v", err)
	}

	tt := ui.NewTester(Root(NewFakeClient(), WithSettings(dataDir, nil)), 1000, 4000)
	openMCPPage(t, tt)
	openMCPDetail(t, tt, "batch-mcp")
	waitFor(t, tt, func() bool { return tt.HasText("支持的工具 (2):") && tt.HasText("新会话与批量策略") })
	if err := tt.Click("新会话与批量策略"); err != nil {
		t.Fatalf("展开高级策略失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("切换全部工具可用") })

	policyPath := agentconf.MCPToolPolicyPath(dataDir)
	// 单个工具：只禁用 tool-a。
	if err := tt.Click("切换禁用 tool-a"); err != nil {
		t.Fatalf("click '切换禁用 tool-a' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		policy, err := agentconf.LoadMCPToolPolicy(policyPath)
		return err == nil && policy.IsDisabled("batch-mcp", "tool-a") && !policy.IsDisabled("batch-mcp", "tool-b")
	})

	// 批量启用（全部工具可用）：两个工具都清除禁用记录。
	if err := tt.Click("切换全部工具可用"); err != nil {
		t.Fatalf("click '切换全部工具可用' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		policy, err := agentconf.LoadMCPToolPolicy(policyPath)
		return err == nil && !policy.IsDisabled("batch-mcp", "tool-a") && !policy.IsDisabled("batch-mcp", "tool-b")
	})

	// 再点一次批量开关：全部工具都禁用。
	if err := tt.Click("切换全部工具可用"); err != nil {
		t.Fatalf("click '切换全部工具可用' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		policy, err := agentconf.LoadMCPToolPolicy(policyPath)
		return err == nil && policy.IsDisabled("batch-mcp", "tool-a") && policy.IsDisabled("batch-mcp", "tool-b")
	})
}

func TestMCPExternalServerDeleteUI(t *testing.T) {
	home := testenv.TempHome(t)
	dataDir := settingsTestDir(t)
	path := filepath.Join(home, ".claude.json")
	before := `{"mcpServers":{"claude-delete":{"command":"echo"},"claude-keep":{"command":"echo"}},"other":true}`
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	tt := newThreadDefaultsTestTester(t, dataDir)
	openMCPDetail(t, tt, "claude-delete")
	waitFor(t, tt, func() bool { _, ok := tt.Find("删除服务"); return ok })
	if _, ok := tt.Find("编辑"); ok {
		t.Fatal("Claude 来源不应显示编辑")
	}
	if err := tt.Click("删除服务"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("将从 ~" + string(filepath.Separator) + ".claude.json 删除 claude-delete，删除后无法恢复。")
	})
	if err := tt.Click("确认删除 MCP 服务"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("claude-delete") })
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"mcpServers":{"claude-keep":{"command":"echo"}},"other":true}` {
		t.Fatalf("删除后内容不符：%s", got)
	}
}

func TestMCPPluginServerCannotDeleteUI(t *testing.T) {
	home := testenv.TempHome(t)
	dataDir := settingsTestDir(t)
	root := filepath.Join(home, ".claude", "plugins", "cache", "p")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	rootJSON, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(home, ".claude", "plugins", "installed_plugins.json"): `{"plugins":{"p@m":[{"installPath":` + string(rootJSON) + `}]},"enabledPlugins":{"p@m":true}}`,
		filepath.Join(root, ".mcp.json"):                                    `{"remote":{"command":"echo"}}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tt := newThreadDefaultsTestTester(t, dataDir)
	openMCPDetail(t, tt, "plugin_p_remote")
	waitFor(t, tt, func() bool { return tt.HasText("plugin_p_remote") && tt.HasText("收起工具") })
	if _, ok := tt.Find("删除服务"); ok {
		t.Fatal("插件来源不应显示删除按钮")
	}
}

func TestMCPCustomSourceBadge(t *testing.T) {
	home := testenv.TempHome(t)
	dataDir := settingsTestDir(t)
	if err := agentconf.SaveMCPServer(agentconf.Options{Home: home, DataDir: dataDir}, agentconf.MCPServer{Agent: "custom", Scope: "global", Name: "badge-server", Command: "echo"}); err != nil {
		t.Fatal(err)
	}
	tt := newThreadDefaultsTestTester(t, dataDir)
	waitFor(t, tt, func() bool { return tt.HasText("badge-server") })
	found := false
	for _, text := range tt.Texts() {
		if text == "custom" {
			t.Fatal("徽章不应显示 custom")
		}
		found = found || text == "mcode"
	}
	if !found {
		t.Fatal("未显示 mcode 徽章")
	}
	for _, agent := range []string{"custom", "CUSTOM", "mcode", "MCode", "claude", "codex"} {
		want := agent
		if strings.EqualFold(agent, "custom") || strings.EqualFold(agent, "mcode") {
			want = "mcode"
		}
		if got := mcpSourceLabel(agent); got != want {
			t.Fatalf("%s 标签为 %s", agent, got)
		}
	}
}

func TestMCPExternalDeleteRefreshesPolicyCache(t *testing.T) {
	home := testenv.TempHome(t)
	dataDir := settingsTestDir(t)
	path := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"drop":{"command":"echo"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := agentconf.SaveMCPToolPolicy(agentconf.MCPToolPolicyPath(dataDir), agentconf.MCPToolPolicy{Disabled: map[string]map[string]bool{"drop": {"tool": true}}, AutoApprove: map[string]map[string]bool{"drop": {"tool": true}}}); err != nil {
		t.Fatal(err)
	}
	st := &SettingsState{dataDir: dataDir}
	uiSt := getMCPUIState(st)
	if !uiSt.disabledTools["drop"]["tool"] {
		t.Fatal("未加载旧策略")
	}
	confirmMCPServerDelete(st, agentconf.Options{Home: home, DataDir: dataDir}, agentconf.MCPServer{Agent: "claude", Scope: "global", Name: "drop", Source: path})
	waitAgentConfLoaded(t, st)
	uiSt.mu.Lock()
	defer uiSt.mu.Unlock()
	if len(uiSt.disabledTools["drop"]) != 0 || len(uiSt.autoApproveTools["drop"]) != 0 {
		t.Fatal("删除后旧策略仍在缓存")
	}
}

// findScannedMCPServer 按名称查找扫描到的 MCP 服务。
func findScannedMCPServer(t *testing.T, opts agentconf.Options, name string) (agentconf.MCPServer, bool) {
	t.Helper()
	servers, err := agentconf.ScanMCP(opts)
	if err != nil {
		t.Fatalf("扫描 MCP 服务失败: %v", err)
	}
	for _, srv := range servers {
		if srv.Name == name {
			return srv, true
		}
	}
	return agentconf.MCPServer{}, false
}

// TestMCPQuickToggle 覆盖列表快捷开关：切换启用且不打开详情、只读来源无开关、未信任仓库服务不被静默启用。
func TestMCPQuickToggle(t *testing.T) {
	t.Run("开关切换启用且不打开详情", func(t *testing.T) {
		home := testenv.TempHome(t)
		dataDir := settingsTestDir(t)
		memSecrets := config.NewMemorySecretStore()
		agentconf.SetSecretStore(memSecrets)
		defer func() {
			agentconf.SetSecretStore(config.NewMemorySecretStore())
		}()
		useMCPToolLister(t, func(context.Context, agentconf.MCPServer) ([]agentconf.MCPToolInfo, error) {
			return nil, nil
		})

		opts := agentconf.Options{Home: home, DataDir: dataDir}
		if err := agentconf.SaveMCPServer(opts, agentconf.MCPServer{
			Name: "quick-mcp", Agent: "custom", Scope: "global", Transport: "stdio", Command: "echo",
		}); err != nil {
			t.Fatalf("保存测试服务失败: %v", err)
		}

		tt := ui.NewTester(Root(NewFakeClient(), WithSettings(dataDir, nil)), 1000, 6000)
		openMCPPage(t, tt)
		waitFor(t, tt, func() bool { return tt.HasText("quick-mcp") && tt.HasText("已启用") })

		// 第一次点快捷开关：停用服务，且不打开详情弹窗。
		if err := tt.Click("启用 quick-mcp"); err != nil {
			t.Fatalf("点击快捷开关失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			srv, ok := findScannedMCPServer(t, opts, "quick-mcp")
			return ok && srv.Disabled && tt.HasText("已停用")
		})
		if tt.HasText("删除服务") || tt.HasText("新会话与批量策略") {
			t.Fatalf("点击列表快捷开关不应打开详情弹窗: %v", tt.Texts())
		}

		// 第二次点快捷开关：重新启用，仍不打开详情弹窗。
		if err := tt.Click("启用 quick-mcp"); err != nil {
			t.Fatalf("再次点击快捷开关失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			srv, ok := findScannedMCPServer(t, opts, "quick-mcp")
			return ok && !srv.Disabled && tt.HasText("已启用")
		})
		if tt.HasText("删除服务") || tt.HasText("新会话与批量策略") {
			t.Fatalf("点击列表快捷开关不应打开详情弹窗: %v", tt.Texts())
		}

		// 点击整行其余区域仍打开详情弹窗。
		openMCPDetail(t, tt, "quick-mcp")
		waitFor(t, tt, func() bool { return tt.HasText("删除服务") })
	})

	t.Run("只读来源无开关", func(t *testing.T) {
		home := testenv.TempHome(t)
		dataDir := settingsTestDir(t)
		useMCPToolLister(t, func(context.Context, agentconf.MCPServer) ([]agentconf.MCPToolInfo, error) {
			return nil, nil
		})
		if err := os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".pi", "agent", "mcp.json"), []byte(`{
  "mcpServers": {
    "pi-readonly-quick": {"command": "pi-mcp", "args": ["--stdio"]}
  }
}`), 0o600); err != nil {
			t.Fatal(err)
		}

		tt := ui.NewTester(Root(NewFakeClient(), WithSettings(dataDir, nil)), 1000, 6000)
		openMCPPage(t, tt)
		waitFor(t, tt, func() bool { return tt.HasText("pi-readonly-quick") && tt.HasText("只读") })
		if _, ok := tt.Find("启用 pi-readonly-quick"); ok {
			t.Fatal("只读服务列表行不应显示快捷开关")
		}
		if tt.HasText("切换启用 pi-readonly-quick") {
			t.Fatal("只读服务不应显示启停开关")
		}
	})

	t.Run("未信任服务不被静默启用", func(t *testing.T) {
		home := testenv.TempHome(t)
		dataDir := settingsTestDir(t)
		projDir := t.TempDir()
		memSecrets := config.NewMemorySecretStore()
		agentconf.SetSecretStore(memSecrets)
		defer func() {
			agentconf.SetSecretStore(config.NewMemorySecretStore())
		}()
		useMCPToolLister(t, func(context.Context, agentconf.MCPServer) ([]agentconf.MCPToolInfo, error) {
			return nil, nil
		})

		if err := os.WriteFile(filepath.Join(projDir, ".mcp.json"), []byte(`{
  "mcpServers": {
    "repo-untrusted": {"command": "echo", "args": ["hi"]}
  }
}`), 0o600); err != nil {
			t.Fatal(err)
		}
		opts := agentconf.Options{Home: home, ProjectDir: projDir, DataDir: dataDir}
		before, ok := findScannedMCPServer(t, opts, "repo-untrusted")
		if !ok || !before.Untrusted || !before.Disabled {
			t.Fatalf("前置条件失败：仓库服务应未信任且禁用，实际 %+v (ok=%v)", before, ok)
		}

		fake := NewFakeClient()
		fake.projects[0].Path = projDir
		tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil), func(s *chatState) {
			s.mu.Lock()
			s.projects = []Project{{ID: "p1", Name: "mcode-test", Path: projDir}}
			s.threads = []Thread{{ID: "t1", ProjectID: "p1"}}
			s.selectedThreadID = "t1"
			s.mu.Unlock()
		}), 1000, 6000)
		openMCPPage(t, tt)
		waitFor(t, tt, func() bool { return tt.HasText("repo-untrusted") && tt.HasText("已停用") })

		// 在列表点快捷开关：不得静默信任/启用，改为进入现有详情信任流程。
		if err := tt.Click("启用 repo-untrusted"); err != nil {
			t.Fatalf("点击未信任服务快捷开关失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			return tt.HasText("来自仓库配置，未信任：启用即表示信任该命令")
		})
		afterQuick, ok := findScannedMCPServer(t, opts, "repo-untrusted")
		if !ok || !afterQuick.Untrusted || !afterQuick.Disabled {
			t.Fatalf("未信任服务不能被快捷开关静默启用，实际 %+v (ok=%v)", afterQuick, ok)
		}

		// 在详情里确认启用后才正式信任并启用。
		if err := tt.Click("切换启用 repo-untrusted"); err != nil {
			t.Fatalf("在详情启用未信任服务失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			srv, found := findScannedMCPServer(t, opts, "repo-untrusted")
			return found && !srv.Untrusted && !srv.Disabled
		})
	})
}

// TestMCPDetailCollapsed 覆盖详情折叠行：默认只见「启用」、按服务记忆展开、
// 展开收起动效尊重 ReduceMotion 且动画结束后不再请求下一帧。
func TestMCPDetailCollapsed(t *testing.T) {
	for _, reduced := range []bool{false, true} {
		name := map[bool]string{false: "normal", true: "reduced"}[reduced]
		t.Run(name, func(t *testing.T) {
			setAppReduceMotion(t, reduced)
			home := testenv.TempHome(t)
			dataDir := settingsTestDir(t)
			memSecrets := config.NewMemorySecretStore()
			agentconf.SetSecretStore(memSecrets)
			defer func() {
				agentconf.SetSecretStore(config.NewMemorySecretStore())
			}()
			useMCPToolLister(t, func(context.Context, agentconf.MCPServer) ([]agentconf.MCPToolInfo, error) {
				return []agentconf.MCPToolInfo{{Name: "tool-1"}}, nil
			})

			opts := agentconf.Options{Home: home, DataDir: dataDir}
			for _, srvName := range []string{"collapse-mcp", "other-mcp"} {
				if err := agentconf.SaveMCPServer(opts, agentconf.MCPServer{
					Name: srvName, Agent: "custom", Scope: "global", Transport: "stdio", Command: "echo",
				}); err != nil {
					t.Fatalf("保存服务 %s 失败: %v", srvName, err)
				}
			}

			tt := ui.NewTesterWith(Root(NewFakeClient(), WithSettings(dataDir, nil)), 1000, 6000, ui.TesterOptions{
				Preferences: ui.Preferences{ReduceMotion: reduced},
			})
			openMCPPage(t, tt)
			openMCPDetail(t, tt, "collapse-mcp")
			waitFor(t, tt, func() bool {
				return tt.HasText("支持的工具 (1):") && tt.HasText("切换启用 collapse-mcp") && tt.HasText("新会话与批量策略")
			})
			settleFrames(t, tt)
			if tt.FramePending() {
				t.Fatal("详情弹出落位后不应继续请求帧")
			}

			// 默认折叠：只见「启用」与折叠行标题，不见三项细则。
			for _, hidden := range []string{"新会话默认启用", "全部工具可用", "全部免审批"} {
				if tt.HasText(hidden) {
					t.Fatalf("默认折叠状态下不应显示 %q", hidden)
				}
			}

			// 点击展开：显示三项；正常模式开启动效，推进时钟到动画结束后不再请求下一帧。
			if err := tt.Click("新会话与批量策略"); err != nil {
				t.Fatalf("点击展开行失败: %v", err)
			}
			if !reduced && !tt.FramePending() {
				t.Fatal("正常模式下展开折叠行应请求动效帧")
			}
			if reduced && tt.FramePending() {
				t.Fatal("ReduceMotion 开启时展开不应请求后续帧")
			}
			tt.Advance(DurBase + 50*time.Millisecond)
			tt.Frame()
			if tt.FramePending() {
				t.Fatal("展开动画结束后不应继续请求下一帧")
			}
			for _, shown := range []string{"新会话默认启用", "全部工具可用", "全部免审批"} {
				if !tt.HasText(shown) {
					t.Fatalf("展开后应显示 %q: %v", shown, tt.Texts())
				}
			}

			// 静止推进时钟不再重启动效。
			tt.Advance(DurBase)
			if tt.FramePending() {
				t.Fatal("展开静止状态下不应继续请求下一帧")
			}

			// 按服务记忆展开状态：关闭再重开仍展开；切换到另一服务默认折叠。
			if err := tt.Click("关闭MCP 服务"); err != nil {
				t.Fatalf("关闭详情失败: %v", err)
			}
			settleFrames(t, tt)
			openMCPDetail(t, tt, "other-mcp")
			waitFor(t, tt, func() bool { return tt.HasText("切换启用 other-mcp") })
			settleFrames(t, tt)
			if tt.HasText("新会话默认启用") {
				t.Fatal("未展开过的服务应保持默认折叠")
			}
			if err := tt.Click("关闭MCP 服务"); err != nil {
				t.Fatalf("关闭详情失败: %v", err)
			}
			settleFrames(t, tt)
			openMCPDetail(t, tt, "collapse-mcp")
			waitFor(t, tt, func() bool { return tt.HasText("切换启用 collapse-mcp") })
			settleFrames(t, tt)
			if !tt.HasText("新会话默认启用") {
				t.Fatal("同一服务应记住上次展开状态")
			}

			// 再次点击收起：动画结束后三项消失且不再请求下一帧。
			if err := tt.Click("新会话与批量策略"); err != nil {
				t.Fatalf("点击收起行失败: %v", err)
			}
			if !reduced && !tt.FramePending() {
				t.Fatal("正常模式下收起折叠行应请求动效帧")
			}
			if reduced && tt.FramePending() {
				t.Fatal("ReduceMotion 开启时收起不应请求后续帧")
			}
			tt.Advance(DurBase + 50*time.Millisecond)
			tt.Frame()
			if tt.FramePending() {
				t.Fatal("收起动画结束后不应继续请求下一帧")
			}
			for _, hidden := range []string{"新会话默认启用", "全部工具可用", "全部免审批"} {
				if tt.HasText(hidden) {
					t.Fatalf("收起后不应继续显示 %q", hidden)
				}
			}
		})
	}
}
