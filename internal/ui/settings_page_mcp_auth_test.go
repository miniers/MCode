package mcodeui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/testenv"
)

// stubMCPAuthScans 打桩技能、MCP 与静态鉴权三个扫描函数，测试结束后恢复。
func stubMCPAuthScans(t *testing.T, mcpFn func(agentconf.Options) ([]agentconf.MCPServer, error), staticFn func(agentconf.Options) map[string]bool) {
	t.Helper()
	origSkills, origMCP, origStatic := scanSkills, scanMCP, scanStaticAuth
	t.Cleanup(func() {
		scanSkills, scanMCP, scanStaticAuth = origSkills, origMCP, origStatic
	})
	scanSkills = func(agentconf.Options) ([]agentconf.Skill, error) { return nil, nil }
	scanMCP = mcpFn
	scanStaticAuth = staticFn
}

// waitAgentConfLoaded 等 loadAgentConf 的后台 goroutine 跑完。
func waitAgentConfLoaded(t *testing.T, st *SettingsState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		loaded := st.agentConfLoaded
		st.mu.Unlock()
		if loaded {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待 loadAgentConf 完成超时")
}

// TestSettingsMCPAuth_StaticAuthHidesSignInButton 覆盖：静态请求头鉴权的远程服务不显示「登录」按钮、
// 徽标改为「请求头认证」；没有鉴权头的远程服务仍保留「登录」按钮。
func TestSettingsMCPAuth_StaticAuthHidesSignInButton(t *testing.T) {
	testenv.TempHome(t)

	staticSrv := agentconf.MCPServer{
		Agent:     "claude",
		Scope:     "global",
		Name:      "nowledge-mem",
		Transport: "http",
		URL:       "https://mem.example.com/mcp",
	}
	plainSrv := agentconf.MCPServer{
		Agent:     "claude",
		Scope:     "global",
		Name:      "plain-remote",
		Transport: "http",
		URL:       "https://plain.example.com/mcp",
	}

	stubMCPAuthScans(t,
		func(agentconf.Options) ([]agentconf.MCPServer, error) {
			return []agentconf.MCPServer{staticSrv, plainSrv}, nil
		},
		func(agentconf.Options) map[string]bool {
			return map[string]bool{"claude:global:nowledge-mem": true}
		},
	)

	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake, WithSettings(settingsTestDir(t), nil)), 1200, 6000)
	openMCPPage(t, tt)

	waitFor(t, tt, func() bool {
		return tt.HasText("nowledge-mem") && tt.HasText("plain-remote")
	})

	openMCPDetail(t, tt, staticSrv.Name)
	waitFor(t, tt, func() bool { return tt.HasText("请求头认证") })
	// 静态鉴权服务不显示登录按钮，普通远程服务仍显示登录按钮。
	if !tt.HasText("请求头认证") {
		t.Fatalf("静态鉴权服务应显示「请求头认证」徽标，实际文本 %v", tt.Texts())
	}
	if got := countExactText(tt.Texts(), "登录"); got != 0 {
		t.Fatalf("静态鉴权服务不应显示登录按钮，实际 %d 个", got)
	}
	if err := tt.Click("关闭MCP 服务"); err != nil {
		t.Fatalf("关闭服务详情失败: %v", err)
	}
	openMCPDetail(t, tt, plainSrv.Name)
	waitFor(t, tt, func() bool { return tt.HasText("登录") })
	if got := countExactText(tt.Texts(), "登录"); got != 1 {
		t.Fatalf("普通远程服务应显示 1 个登录按钮，实际 %d 个", got)
	}
}

// countExactText 统计某个文本在最后一帧里完整出现的次数（不做子串匹配）。
func countExactText(texts []string, want string) int {
	count := 0
	for _, t := range texts {
		if t == want {
			count++
		}
	}
	return count
}

// TestSettingsMCPAuth_StaticAuthMapFilledAfterLoad 覆盖：loadAgentConf 成功后把静态鉴权集合写进状态。
func TestSettingsMCPAuth_StaticAuthMapFilledAfterLoad(t *testing.T) {
	testenv.TempHome(t)

	stubMCPAuthScans(t,
		func(agentconf.Options) ([]agentconf.MCPServer, error) {
			return []agentconf.MCPServer{{
				Agent:     "claude",
				Scope:     "global",
				Name:      "nowledge-mem",
				Transport: "http",
				URL:       "https://mem.example.com/mcp",
			}}, nil
		},
		func(agentconf.Options) map[string]bool {
			return map[string]bool{"claude:global:nowledge-mem": true}
		},
	)

	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.loadAgentConf("")
	waitAgentConfLoaded(t, st)

	st.mu.Lock()
	got := st.mcpStaticAuth["claude:global:nowledge-mem"]
	total := len(st.mcpStaticAuth)
	st.mu.Unlock()
	if !got || total != 1 {
		t.Fatalf("扫描完成后 mcpStaticAuth 应正好包含静态鉴权服务，实际 %d 项", total)
	}
}

// TestSettingsMCPAuth_ScanErrorClearsStaticAuth 覆盖：列表扫描失败时不采信静态鉴权结果。
func TestSettingsMCPAuth_ScanErrorClearsStaticAuth(t *testing.T) {
	testenv.TempHome(t)

	stubMCPAuthScans(t,
		func(agentconf.Options) ([]agentconf.MCPServer, error) {
			return nil, errors.New("扫描 MCP 失败")
		},
		func(agentconf.Options) map[string]bool {
			return map[string]bool{"claude:global:stale": true}
		},
	)

	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.loadAgentConf("")
	waitAgentConfLoaded(t, st)

	st.mu.Lock()
	total := len(st.mcpStaticAuth)
	st.mu.Unlock()
	if total != 0 {
		t.Fatalf("扫描失败时不应写入静态鉴权集合，实际 %d 项", total)
	}
}

// TestMCPAuthLinkDisplay 覆盖：授权链接超过 80 个字符时截断并以省略号收尾。
func TestMCPAuthLinkDisplay(t *testing.T) {
	short := "https://example.com/oauth/authorize?client_id=abc"
	if got := mcpAuthLinkDisplay(short); got != short {
		t.Fatalf("短链接不应被截断，得到 %q", got)
	}

	long := strings.Repeat("a", 120)
	got := mcpAuthLinkDisplay(long)
	runes := []rune(got)
	if len(runes) != mcpAuthLinkMaxLen+1 {
		t.Fatalf("长链接应裁到 %d 个字符加省略号，实际 %d 个", mcpAuthLinkMaxLen, len(runes))
	}
	if runes[len(runes)-1] != '…' {
		t.Fatalf("长链接应以省略号结尾，得到 %q", got)
	}
}
