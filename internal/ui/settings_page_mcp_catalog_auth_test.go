package mcodeui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/agentconf"
)

// deviceAuthCatalogEntry 返回一条声明设备码登录方式的目录条目。
func deviceAuthCatalogEntry(auth *agentconf.MCPCatalogAuth) agentconf.MCPCatalogEntry {
	return agentconf.MCPCatalogEntry{
		ID:        "device-auth-service",
		Name:      "设备码登录测试服务",
		Vendor:    "测试",
		Category:  "developer",
		Transport: "oauth",
		URL:       "https://example.com/mcp",
		Auth:      auth,
	}
}

// stubCatalogInstallProbe 让工具探测立即返回空列表，避免目录安装测试发起网络请求。
func stubCatalogInstallProbe(t *testing.T) {
	t.Helper()
	useMCPToolLister(t, func(ctx context.Context, srv agentconf.MCPServer) ([]agentconf.MCPToolInfo, error) {
		return nil, nil
	})
}

// newCatalogInstallState 构造一个只扫描到空列表的设置状态。
func newCatalogInstallState(t *testing.T) (*SettingsState, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	stubMCPAuthScans(t,
		func(agentconf.Options) ([]agentconf.MCPServer, error) { return nil, nil },
		func(agentconf.Options) map[string]bool { return nil },
	)
	dir := settingsTestDir(t)
	return NewSettingsState(dir, nil, nil), dir
}

// readSavedMCPNames 读取自定义 MCP 配置里的服务名，用于断言安装是否落盘。
func readSavedMCPNames(t *testing.T, dir string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "mcp.json"))
	if err != nil {
		t.Fatalf("读取 MCP 配置失败: %v", err)
	}
	var cfg struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("解析 MCP 配置失败: %v", err)
	}
	names := make(map[string]bool, len(cfg.MCPServers))
	for name := range cfg.MCPServers {
		names[name] = true
	}
	return names
}

// TestCatalogInstallStartsDeviceAuth 覆盖：安装 device 登录方式的目录条目后自动发起登录并传入 Auth。
func TestCatalogInstallStartsDeviceAuth(t *testing.T) {
	stubCatalogInstallProbe(t)
	st, dir := newCatalogInstallState(t)
	entry := deviceAuthCatalogEntry(&agentconf.MCPCatalogAuth{Mode: agentconf.MCPAuthModeDevice})
	srv, _, err := entry.Resolve(nil)
	if err != nil {
		t.Fatalf("解析目录条目失败: %v", err)
	}

	fake := NewFakeClient()
	addMCPCatalogEntry(st, getMCPCatalogUIState(st), entry, fake)
	waitFakeSignInCalls(t, fake, 1)

	calls, auth := fakeSignInSnapshot(fake)
	if len(calls) != 1 {
		t.Fatalf("安装后应只发起 1 次登录，实际 %d 次", len(calls))
	}
	if calls[0] != srv.AuthID() {
		t.Fatalf("自动登录应使用服务标识 %q，实际 %q", srv.AuthID(), calls[0])
	}
	if auth == nil || auth.Mode != agentconf.MCPAuthModeDevice {
		t.Fatalf("自动登录应收到 device 声明，实际 %+v", auth)
	}
	if !readSavedMCPNames(t, dir)[srv.Name] {
		t.Fatalf("目录条目 %q 未写入 MCP 配置", srv.Name)
	}
}

// TestCatalogInstallSkipsAuthWhenNotDeclared 覆盖：没有 Auth 声明的条目安装后不发起登录。
func TestCatalogInstallSkipsAuthWhenNotDeclared(t *testing.T) {
	stubCatalogInstallProbe(t)
	st, dir := newCatalogInstallState(t)
	entry := deviceAuthCatalogEntry(nil)

	fake := NewFakeClient()
	addMCPCatalogEntry(st, getMCPCatalogUIState(st), entry, fake)

	if calls, _ := fakeSignInSnapshot(fake); len(calls) != 0 {
		t.Fatalf("未声明 Auth 的条目不应自动登录，实际调用 %v", calls)
	}
	if !readSavedMCPNames(t, dir)[entry.ID] {
		t.Fatalf("目录条目 %q 未写入 MCP 配置", entry.ID)
	}
}

// TestCatalogInstallAuthFailureKeepsServer 覆盖：自动登录失败只记录错误，不撤销已保存的条目。
func TestCatalogInstallAuthFailureKeepsServer(t *testing.T) {
	stubCatalogInstallProbe(t)
	st, dir := newCatalogInstallState(t)
	entry := deviceAuthCatalogEntry(&agentconf.MCPCatalogAuth{Mode: agentconf.MCPAuthModeDevice})
	srv, _, err := entry.Resolve(nil)
	if err != nil {
		t.Fatalf("解析目录条目失败: %v", err)
	}

	fake := NewFakeClient()
	fake.mcpSignInErr = errors.New("设备码已过期")
	addMCPCatalogEntry(st, getMCPCatalogUIState(st), entry, fake)
	waitFakeSignInCalls(t, fake, 1)

	deadline := time.Now().Add(2 * time.Second)
	for {
		st.mu.Lock()
		authErr := st.mcpAuthErr[srv.AuthID()]
		st.mu.Unlock()
		if authErr != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("登录失败后应记录错误信息")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !readSavedMCPNames(t, dir)[srv.Name] {
		t.Fatalf("登录失败后条目 %q 应保留在 MCP 配置中", srv.Name)
	}
}
