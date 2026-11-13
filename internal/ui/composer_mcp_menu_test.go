package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/mcp"
	"mcode/internal/testenv"
)

// stubMCPMenuScan 用固定列表替换磁盘扫描，并清空菜单缓存，返回复位函数。
func stubMCPMenuScan(t *testing.T, servers []agentconf.MCPServer) {
	t.Helper()
	prev := scanMCPMenuServers
	scanMCPMenuServers = func(agentconf.Options) ([]agentconf.MCPServer, error) {
		return append([]agentconf.MCPServer(nil), servers...), nil
	}
	resetMCPMenuCache()
	t.Cleanup(func() {
		scanMCPMenuServers = prev
		resetMCPMenuCache()
	})
}

// newMCPMenuTestRoot 造一个 provider 可配、带一条会话的根视图。
func newMCPMenuTestRoot(t *testing.T, provider string) (*FakeClient, *chatState, *ui.Tester) {
	t.Helper()
	testenv.TempHome(t)
	fake := NewFakeClient()
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}},
		map[string][]Thread{"p1": {{ID: "t1", ProjectID: "p1", Title: "测试会话1", Provider: provider, UpdatedAt: now}}},
	)
	fake.SetProviders([]ProviderInfo{{ID: provider, Name: provider, Available: true}})
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	return fake, state, tt
}

// TestComposerMCPMenu_AppliesThreadDisabledKeys 验证在 MCP 二级页取消勾选并点「应用」后，
// 后端收到的禁用键正是 mcp.EndpointKey(agent, scope, name)。
func TestComposerMCPMenu_AppliesThreadDisabledKeys(t *testing.T) {
	stubMCPMenuScan(t, []agentconf.MCPServer{
		{Agent: "codex", Scope: "project", Name: "demo"},
		{Agent: "codex", Scope: "project", Name: "other"},
	})
	fake, state, tt := newMCPMenuTestRoot(t, "codex")

	if err := tt.Click("添加附件"); err != nil {
		t.Fatalf("打开附件浮层失败: %v", err)
	}
	if err := tt.Click("MCP 服务器"); err != nil {
		t.Fatalf("进入 MCP 二级页失败: %v", err)
	}

	// 二级页列出扫描到的服务。
	waitFor(t, tt, func() bool {
		return tt.HasText("demo · codex") && tt.HasText("other · codex")
	})

	if err := tt.Click("demo · codex"); err != nil {
		t.Fatalf("取消勾选 demo 失败: %v", err)
	}
	if err := tt.Click("应用"); err != nil {
		t.Fatalf("点应用失败: %v", err)
	}

	want := mcp.EndpointKey("codex", "project", "demo")
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.mcpDisabledCalls) == 1
	})
	fake.mu.Lock()
	call := fake.mcpDisabledCalls[0]
	fake.mu.Unlock()
	if call.ThreadID != "t1" {
		t.Fatalf("应写到当前会话 t1，got %q", call.ThreadID)
	}
	if len(call.Keys) != 1 || call.Keys[0] != want {
		t.Fatalf("禁用键应为 [%s]，got %v", want, call.Keys)
	}

	// 本地会话记录同步，重新打开始终能看到已停用状态。
	state.mu.Lock()
	disabled := append([]string(nil), state.threads[0].MCPDisabled...)
	state.mu.Unlock()
	if len(disabled) != 1 || disabled[0] != want {
		t.Fatalf("会话本地的 MCPDisabled 应同步为 [%s]，got %v", want, disabled)
	}
}

// TestComposerMCPMenu_UnsupportedProviderIsNoted 验证不支持按会话启停 MCP 的 Agent 只给说明，
// 不做看着生效、实际没生效的开关。
func TestComposerMCPMenu_UnsupportedProviderIsNoted(t *testing.T) {
	stubMCPMenuScan(t, []agentconf.MCPServer{{Agent: "omp", Scope: "project", Name: "demo"}})
	fake, _, tt := newMCPMenuTestRoot(t, "omp")

	if err := tt.Click("添加附件"); err != nil {
		t.Fatalf("打开附件浮层失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("不支持") })

	// 不支持时入口置灰，点了也进不去二级页，更不会下发任何写入。
	_ = tt.Click("MCP 服务器")
	fake.mu.Lock()
	calls := len(fake.mcpDisabledCalls)
	fake.mu.Unlock()
	if calls != 0 {
		t.Fatalf("不支持的 Agent 不应下发 MCP 设置，got %d 次", calls)
	}
	if tt.HasText("勾选仅对当前会话生效") {
		t.Fatal("不支持的 Agent 不应打开勾选页")
	}
}

// TestProviderSupportsThreadMCP 校验按会话启停外部 MCP 的能力表：
// mcode 的 ExtraTools 会读 Thread.MCPDisabled，pi 的端点前缀先归一后再查表。
func TestProviderSupportsThreadMCP(t *testing.T) {
	cases := []struct {
		provider string
		want     bool
	}{
		{"claude", true},
		{"mcode", true},
		{"pi", true},
		{"pi:auto", true},
		{"omp", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			if got := providerSupportsThreadMCP(tc.provider); got != tc.want {
				t.Fatalf("providerSupportsThreadMCP(%q) = %v，想要 %v", tc.provider, got, tc.want)
			}
		})
	}
}
