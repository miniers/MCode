package mcodeui

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/testenv"
)

// TestSettingsRedesignControls_SegmentedHeight 验证分段控件每个选项按钮高度为 24（ControlSM）。
func TestSettingsRedesignControls_SegmentedHeight(t *testing.T) {
	selected := 0
	tt := ui.NewTester(func(c *ui.Context) {
		Segmented(c, "seg-height", &selected, []Segment{{Label: "stdio"}, {Label: "http"}}, nil)
	}, 400, 100)
	for _, label := range []string{"stdio", "http"} {
		rect, ok := tt.Find(label)
		if !ok {
			t.Fatalf("找不到分段 %q", label)
		}
		if rect.H != ControlSM {
			t.Errorf("分段 %q 高度 = %.1f，期望 %.1f", label, rect.H, ControlSM)
		}
	}
}

// TestSettingsRedesignControls_MCPDetailHeaderTitle 验证打开 MCP 详情时标题显示服务名，未选中时回退为「MCP 服务」。
func TestSettingsRedesignControls_MCPDetailHeaderTitle(t *testing.T) {
	testenv.TempHome(t)
	dataDir := settingsTestDir(t)
	srv := agentconf.MCPServer{
		Agent:     "custom",
		Scope:     "global",
		Name:      "context7-mcp",
		Transport: "stdio",
		Command:   "npx",
	}
	stubMCPAuthScans(t, func(agentconf.Options) ([]agentconf.MCPServer, error) {
		return []agentconf.MCPServer{srv}, nil
	}, func(agentconf.Options) map[string]bool { return nil })
	useMCPToolLister(t, func(context.Context, agentconf.MCPServer) ([]agentconf.MCPToolInfo, error) {
		return []agentconf.MCPToolInfo{{Name: "query"}}, nil
	})

	if got := mcpDetailSheetTitle(nil); got != "MCP 服务" {
		t.Fatalf("空状态回退标题 = %q，期望 MCP 服务", got)
	}

	tt := ui.NewTester(Root(NewFakeClient(), WithSettings(dataDir, nil)), 1200, 6000)
	openMCPPage(t, tt)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("打开 MCP 服务 custom:global:context7-mcp")
		return ok
	})
	if err := tt.Click("打开 MCP 服务 custom:global:context7-mcp"); err != nil {
		t.Fatalf("打开 MCP 详情失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("启用") && tt.HasText("全部工具可用") && tt.HasText("全部免审批") && tt.HasText("删除服务")
	})
	if got := countExactText(tt.Texts(), "context7-mcp"); got == 0 {
		t.Fatalf("详情面板标题未显示服务名 context7-mcp: %v", tt.Texts())
	}
}

// TestSettingsRedesignControls_MCPEmptyState 验证 MCP 空列表使用 EmptyState 组件渲染。
func TestSettingsRedesignControls_MCPEmptyState(t *testing.T) {
	testenv.TempHome(t)
	orig := scanMCP
	t.Cleanup(func() { scanMCP = orig })
	scanMCP = func(agentconf.Options) ([]agentconf.MCPServer, error) {
		return nil, nil
	}

	st := NewSettingsState(settingsTestDir(t), nil, nil)
	tt := ui.NewTester(func(c *ui.Context) { renderPageExtensions(c, st) }, 1000, 2000)
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		loaded := st.agentConfLoaded
		st.mu.Unlock()
		return loaded && tt.HasText("暂无自定义服务")
	})

	card, ok := tt.Find("设置卡片 mcp.custom")
	if !ok {
		t.Fatalf("缺少 MCP 卡片: %v", tt.Texts())
	}
	emptyText, ok := tt.Find("暂无自定义服务")
	if !ok {
		t.Fatalf("缺少空态文案: %v", tt.Texts())
	}
	// EmptyState 居中对齐，左边缘应明显内缩于卡片居中位置而非紧贴左侧。
	cardCenter := card.X + card.W/2
	textCenter := emptyText.X + emptyText.W/2
	if diff := cardCenter - textCenter; diff < -2 || diff > 2 {
		t.Errorf("空态组件未居中显示：卡片中心 %.1f，文字中心 %.1f", cardCenter, textCenter)
	}
}

// TestSettingsRedesignControls_ResetButtonOpensConfirm 验证「恢复本页默认」按钮高度为 ControlMD 且点击后弹出确认弹窗。
func TestSettingsRedesignControls_ResetButtonOpensConfirm(t *testing.T) {
	tt, st := newSettingsAdvancedTester(t)
	openSettingsAdvancedPage(t, tt, st, "conversation")
	waitFor(t, tt, func() bool { return tt.HasText("恢复本页默认") })

	btnRect, ok := tt.Find("恢复本页默认")
	if !ok {
		t.Fatal("找不到「恢复本页默认」按钮")
	}
	if btnRect.H != ControlMD {
		t.Errorf("「恢复本页默认」按钮高度 = %.1f，期望 %.1f", btnRect.H, ControlMD)
	}

	if err := settingsClick(t, tt, "恢复本页默认"); err != nil {
		t.Fatalf("点击恢复本页默认失败: %v", err)
	}
	waitFor(t, tt, func() bool { return st.resetPendingPage == "conversation" && tt.HasText("恢复默认") })
}

// TestSettingsRedesignControls_MCPAddFormAndRemoteLoading 验证 mcp-add 手动填写表单与远程加载指示器。
func TestSettingsRedesignControls_MCPAddFormAndRemoteLoading(t *testing.T) {
	testenv.TempHome(t)
	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(NewFakeClient(), WithSettings(dataDir, nil)), 1200, 6000)
	openMCPPage(t, tt)
	openMCPAddSheet(t, tt)
	waitFor(t, tt, func() bool { return tt.HasText("手动填写") })
	if err := tt.Click("手动填写"); err != nil {
		t.Fatalf("点击手动填写失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("传输方式") && tt.HasText("stdio") && tt.HasText("http") })
	if tt.HasText("添加 MCP 服务") {
		t.Fatal("mcp-add 手动填写不应重复显示「添加 MCP 服务」标题")
	}
	if !tt.HasText("面板操作栏 添加服务") {
		t.Fatalf("mcp-add 手动填写的保存与取消按钮应位于面板操作栏: %v", tt.Texts())
	}

	st := NewSettingsState(dataDir, nil, nil)
	remoteTT := ui.NewTester(func(c *ui.Context) { renderRemoteDevicesBlock(c, st) }, 800, 200)
	if !remoteTT.HasText("正在加载设备…") {
		t.Fatalf("加载中应显示「正在加载设备…」: %v", remoteTT.Texts())
	}
	if _, ok := remoteTT.Find("加载中"); !ok {
		t.Fatalf("加载中应使用 LoadingIndicator 渲染进度指示器: %v", remoteTT.Texts())
	}
}
