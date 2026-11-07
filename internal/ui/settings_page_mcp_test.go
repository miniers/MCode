package mcodeui

import (
	"context"
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
)

// TestSettings_MCPAddAndDelete 测试自定义 MCP 服务的表单新增与删除 UI 交互流程
func TestSettings_MCPAddAndDelete(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)

	memSecrets := config.NewMemorySecretStore()
	agentconf.SetSecretStore(memSecrets)
	defer func() {
		agentconf.SetSecretStore(config.NewSecretStore("mcode.mcp"))
	}()

	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 6000)

	waitFor(t, tt, func() bool {
		return tt.HasText("设置")
	})

	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("扩展")
	})

	if err := tt.Click("扩展"); err != nil {
		t.Fatalf("click '扩展' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("+ 添加服务")
	})

	// 1. 打开添加面板并选择手动填写。
	openMCPAddSheet(t, tt)
	waitFor(t, tt, func() bool { return tt.HasText("手动填写") })
	if err := tt.Click("手动填写"); err != nil {
		t.Fatalf("click '手动填写' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("添加 MCP 服务") && tt.HasText("保存")
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
		return tt.HasText("删除") && (tt.HasText("连接后可见") || tt.HasText("收起工具") || tt.HasText("探测失败"))
	})

	// 6. 点击删除：删除会连凭据一起清掉，必须先出现二次确认框
	if err := tt.Click("删除"); err != nil {
		t.Fatalf("click '删除' failed: %v", err)
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
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)

	memSecrets := config.NewMemorySecretStore()
	agentconf.SetSecretStore(memSecrets)
	defer func() {
		agentconf.SetSecretStore(config.NewSecretStore("mcode.mcp"))
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
	waitFor(t, tt, func() bool { return tt.HasText("扩展") })
	if err := tt.Click("扩展"); err != nil {
		t.Fatalf("click '扩展' failed: %v", err)
	}
	openMCPDetail(t, tt, "confirm-mcp")
	waitFor(t, tt, func() bool { return tt.HasText("删除") })

	if err := tt.Click("删除"); err != nil {
		t.Fatalf("click '删除' failed: %v", err)
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
	if err := tt.Click("删除"); err != nil {
		t.Fatalf("click '删除' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("确认删除 MCP 服务") })
	if err := tt.Click("确认删除 MCP 服务"); err != nil {
		t.Fatalf("click '确认删除 MCP 服务' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("confirm-mcp") })
}

func TestMCPServerDeleteModalRenderedOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dataDir := settingsTestDir(t)
	home, _ := os.UserHomeDir()
	if err := agentconf.SaveMCPServer(agentconf.Options{Home: home, DataDir: dataDir}, agentconf.MCPServer{
		Name: "single-modal-mcp", Agent: "custom", Scope: "global", Transport: "stdio", Command: "echo",
	}); err != nil {
		t.Fatalf("保存自定义 MCP 服务失败: %v", err)
	}

	tt := newThreadDefaultsTestTester(t, dataDir)
	openMCPDetail(t, tt, "single-modal-mcp")
	waitFor(t, tt, func() bool { return tt.HasText("删除") })
	if err := tt.Click("删除"); err != nil {
		t.Fatalf("click '删除' failed: %v", err)
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
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)

	memSecrets := config.NewMemorySecretStore()
	agentconf.SetSecretStore(memSecrets)
	defer func() {
		agentconf.SetSecretStore(config.NewSecretStore("mcode.mcp"))
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
	waitFor(t, tt, func() bool { return tt.HasText("扩展") })
	if err := tt.Click("扩展"); err != nil {
		t.Fatalf("click '扩展' failed: %v", err)
	}
	openMCPDetail(t, tt, "broken-mcp")
	waitFor(t, tt, func() bool {
		return tt.HasText("探测失败：401 unauthorized") && tt.HasText("重新检测")
	})
}

// TestMCPServerRowReadOnly 覆盖：外部 Agent 配置里的只读服务只做展示，
// 必须显示「只读 · 外部配置」徽章，且没有启停开关、编辑、删除按钮。
func TestMCPServerRowReadOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
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
	waitFor(t, tt, func() bool { return tt.HasText("扩展") })
	if err := tt.Click("扩展"); err != nil {
		t.Fatalf("click '扩展' failed: %v", err)
	}

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
	if _, ok := tt.Find("删除"); ok {
		t.Fatal("只读服务不应渲染删除按钮")
	}
}

// newThreadDefaultsTestTester 打开设置页的 MCP 页，供「新会话默认」相关用例复用。
func newThreadDefaultsTestTester(t *testing.T, dataDir string) *ui.Tester {
	t.Helper()
	tt := ui.NewTester(Root(NewFakeClient(), WithSettings(dataDir, nil)), 1000, 6000)
	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("扩展") })
	if err := tt.Click("扩展"); err != nil {
		t.Fatalf("click '扩展' failed: %v", err)
	}
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
	t.Setenv("HOME", home)
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
	t.Setenv("HOME", t.TempDir())
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
	waitFor(t, tt, func() bool {
		return tt.HasText("这个服务在 Claude Code 里被关掉了（~/.claude.json 的 disabledMcpServers）。MCode 是否注入仍由这里的开关决定。")
	})

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
	t.Setenv("HOME", t.TempDir())
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
	waitFor(t, tt, func() bool { return tt.HasText("支持的工具 (2):") && tt.HasText("切换全部禁用") })

	policyPath := agentconf.MCPToolPolicyPath(dataDir)
	// 单个工具：只禁用 tool-a。
	if err := tt.Click("切换禁用 tool-a"); err != nil {
		t.Fatalf("click '切换禁用 tool-a' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		policy, err := agentconf.LoadMCPToolPolicy(policyPath)
		return err == nil && policy.IsDisabled("batch-mcp", "tool-a") && !policy.IsDisabled("batch-mcp", "tool-b")
	})

	// 批量禁用：两个工具都写盘。
	if err := tt.Click("切换全部禁用"); err != nil {
		t.Fatalf("click '切换全部禁用' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		policy, err := agentconf.LoadMCPToolPolicy(policyPath)
		return err == nil && policy.IsDisabled("batch-mcp", "tool-a") && policy.IsDisabled("batch-mcp", "tool-b")
	})

	// 再点一次批量开关：禁用记录全部清除。
	if err := tt.Click("切换全部禁用"); err != nil {
		t.Fatalf("click '切换全部禁用' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		policy, err := agentconf.LoadMCPToolPolicy(policyPath)
		return err == nil && !policy.IsDisabled("batch-mcp", "tool-a") && !policy.IsDisabled("batch-mcp", "tool-b")
	})
}
