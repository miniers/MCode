package mcodeui

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/mcp"
	"mcode/internal/testenv"
)

// deviceAuthClient 给 FakeClient 补上授权查看与设备码能力，并让登录一直挂起：
// 设备码与授权链接在点击「登录」前预置，便于断言等待期间的界面。
type deviceAuthClient struct {
	*FakeClient

	mu        sync.Mutex
	authURL   string
	device    mcp.DeviceCodePrompt
	hasDevice bool
	authSeen  *agentconf.MCPCatalogAuth

	release     chan struct{}
	releaseOnce sync.Once
}

func newDeviceAuthClient() *deviceAuthClient {
	return &deviceAuthClient{
		FakeClient: NewFakeClient(),
		release:    make(chan struct{}),
	}
}

// MCPSignInWithAuth 记录收到的目录 Auth 声明后挂起，直到测试放行或取消。
func (c *deviceAuthClient) MCPSignInWithAuth(ctx context.Context, serverID, serverURL string, auth *agentconf.MCPCatalogAuth) error {
	c.mu.Lock()
	c.authSeen = cloneTestCatalogAuth(auth)
	c.mu.Unlock()
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// MCPAuthURL 返回预置的授权链接，设备流下与真实客户端一样同时写 authURL。
func (c *deviceAuthClient) MCPAuthURL(serverID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.authURL
}

// MCPDeviceCode 返回预置的设备码；没有设备流会话时 ok 为 false。
func (c *deviceAuthClient) MCPDeviceCode(serverID string) (string, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.hasDevice {
		return "", "", false
	}
	return c.device.UserCode, c.device.VerificationURI, true
}

// MCPCancelSignIn 模拟取消设备流：清掉设备码与链接，并放行挂起的登录 goroutine。
func (c *deviceAuthClient) MCPCancelSignIn(serverID string) {
	c.mu.Lock()
	c.hasDevice = false
	c.device = mcp.DeviceCodePrompt{}
	c.authURL = ""
	c.mu.Unlock()
	c.finish()
}

// setAuthURL 预置授权链接。
func (c *deviceAuthClient) setAuthURL(url string) {
	c.mu.Lock()
	c.authURL = url
	c.mu.Unlock()
}

// setDevicePrompt 预置设备流进行中的验证码与验证地址。
func (c *deviceAuthClient) setDevicePrompt(userCode, verificationURI string) {
	c.mu.Lock()
	c.hasDevice = true
	c.device = mcp.DeviceCodePrompt{UserCode: userCode, VerificationURI: verificationURI}
	c.mu.Unlock()
}

// signInAuth 返回最近一次登录收到的 Auth 声明。
func (c *deviceAuthClient) signInAuth() *agentconf.MCPCatalogAuth {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneTestCatalogAuth(c.authSeen)
}

// finish 放行挂起的登录，供测试清理使用。
func (c *deviceAuthClient) finish() {
	c.releaseOnce.Do(func() { close(c.release) })
}

// remoteOAuthServer 返回一个需要登录的远程 MCP 服务。
func remoteOAuthServer() agentconf.MCPServer {
	return agentconf.MCPServer{
		Agent:     "claude",
		Scope:     "global",
		Name:      "device-flow",
		Transport: "http",
		URL:       "https://example.com/mcp",
		Auth:      &agentconf.MCPCatalogAuth{Mode: agentconf.MCPAuthModeDevice},
	}
}

// newDeviceAuthTester 启动设置页并进入 MCP 页，等待目标服务与「登录」按钮出现。
func newDeviceAuthTester(t *testing.T, client *deviceAuthClient, srv agentconf.MCPServer) *ui.Tester {
	t.Helper()
	testenv.TempHome(t)
	stubMCPAuthScans(t,
		func(agentconf.Options) ([]agentconf.MCPServer, error) {
			return []agentconf.MCPServer{srv}, nil
		},
		func(agentconf.Options) map[string]bool { return nil },
	)
	t.Cleanup(client.finish)
	tt := ui.NewTester(Root(client, WithSettings(settingsTestDir(t), nil)), 1200, 6000)
	openMCPPage(t, tt)
	openMCPDetail(t, tt, srv.Name)
	waitFor(t, tt, func() bool { return tt.HasText("登录") })
	return tt
}

// TestMCPDeviceCodeRow 覆盖：设备流等待期间内联显示验证网址与验证码，隐藏授权链接行。
func TestMCPDeviceCodeRow(t *testing.T) {
	client := newDeviceAuthClient()
	client.setAuthURL("https://example.com/device")
	client.setDevicePrompt("ABCD-EFGH", "https://example.com/device")
	tt := newDeviceAuthTester(t, client, remoteOAuthServer())

	if err := tt.Click("登录"); err != nil {
		t.Fatalf("click '登录' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("ABCD-EFGH") &&
			tt.HasText("example.com/device") &&
			tt.HasText("复制验证码") &&
			tt.HasText("等待授权…")
	})

	if got := countExactText(tt.Texts(), "复制链接"); got != 0 {
		t.Fatalf("设备流等待期间不应再显示授权链接行，实际出现 %d 次", got)
	}
}

// TestMCPDeviceCodeCopy 覆盖：复制验证码把 user_code 写进剪贴板，并显示「已复制」回执。
func TestMCPDeviceCodeCopy(t *testing.T) {
	client := newDeviceAuthClient()
	client.setAuthURL("https://example.com/device")
	client.setDevicePrompt("ABCD-EFGH", "https://example.com/device")
	tt := newDeviceAuthTester(t, client, remoteOAuthServer())

	if err := tt.Click("登录"); err != nil {
		t.Fatalf("click '登录' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("复制验证码") })

	if err := tt.Click("复制验证码"); err != nil {
		t.Fatalf("click '复制验证码' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.Clipboard() == "ABCD-EFGH" && tt.HasText("已复制")
	})
}

// TestMCPDeviceCodeHiddenWhenNotPending 覆盖：没有设备码会话时保持原样，只显示授权链接行。
func TestMCPDeviceCodeHiddenWhenNotPending(t *testing.T) {
	client := newDeviceAuthClient()
	client.setAuthURL("https://example.com/oauth/authorize?client_id=abc")
	tt := newDeviceAuthTester(t, client, remoteOAuthServer())

	if err := tt.Click("登录"); err != nil {
		t.Fatalf("click '登录' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("复制链接") && tt.HasText("授权中…")
	})

	if got := countExactText(tt.Texts(), "复制验证码"); got != 0 {
		t.Fatalf("没有设备码时不应显示复制验证码按钮，实际出现 %d 次", got)
	}
}

// TestMCPAuthLinkStillShownForLoopback 覆盖：loopback 登录仍显示授权链接行，
// 复制链接拷贝完整地址，并把目录声明的 Auth 传给客户端。
func TestMCPAuthLinkStillShownForLoopback(t *testing.T) {
	client := newDeviceAuthClient()
	authURL := "https://example.com/oauth/authorize?client_id=abc"
	client.setAuthURL(authURL)
	srv := remoteOAuthServer()
	srv.Auth = &agentconf.MCPCatalogAuth{Mode: agentconf.MCPAuthModeLoopback, ClientID: "client-abc"}
	tt := newDeviceAuthTester(t, client, srv)

	if err := tt.Click("登录"); err != nil {
		t.Fatalf("click '登录' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("复制链接") && tt.HasText("授权中…") })
	if got := countExactText(tt.Texts(), "复制验证码"); got != 0 {
		t.Fatalf("loopback 登录不应显示复制验证码按钮，实际出现 %d 次", got)
	}

	if err := tt.Click("复制链接"); err != nil {
		t.Fatalf("click '复制链接' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.Clipboard() == authURL && tt.HasText("已复制")
	})

	auth := client.signInAuth()
	if auth == nil || auth.Mode != agentconf.MCPAuthModeLoopback || auth.ClientID != "client-abc" {
		t.Fatalf("登录应收到 loopback 声明，实际 %+v", auth)
	}
}

// TestMCPSignInPassesAuth 覆盖：服务行上的「登录」把保存的 Auth 传给客户端。
func TestMCPSignInPassesAuth(t *testing.T) {
	testenv.TempHome(t)
	srv := remoteOAuthServer()
	stubMCPAuthScans(t,
		func(agentconf.Options) ([]agentconf.MCPServer, error) {
			return []agentconf.MCPServer{srv}, nil
		},
		func(agentconf.Options) map[string]bool { return nil },
	)
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake, WithSettings(settingsTestDir(t), nil)), 1200, 6000)
	openMCPPage(t, tt)
	openMCPDetail(t, tt, srv.Name)
	waitFor(t, tt, func() bool { return tt.HasText("登录") })

	if err := tt.Click("登录"); err != nil {
		t.Fatalf("click '登录' failed: %v", err)
	}
	waitFakeSignInCalls(t, fake, 1)

	calls, auth := fakeSignInSnapshot(fake)
	if calls[0] != srv.AuthID() {
		t.Fatalf("登录应使用服务标识 %q，实际 %q", srv.AuthID(), calls[0])
	}
	if auth == nil || auth.Mode != agentconf.MCPAuthModeDevice {
		t.Fatalf("登录应收到 device 声明，实际 %+v", auth)
	}
}

// fakeSignInSnapshot 在锁内取出登录调用与服务标识，避免与登录 goroutine 竞态。
func fakeSignInSnapshot(f *FakeClient) ([]string, *agentconf.MCPCatalogAuth) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.mcpSignInCalls...), cloneTestCatalogAuth(f.mcpSignInAuth)
}

// waitFakeSignInCalls 等待客户端收到至少 want 次登录调用。
func waitFakeSignInCalls(t *testing.T, f *FakeClient, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		calls, _ := fakeSignInSnapshot(f)
		if len(calls) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待 %d 次 MCP 登录调用超时", want)
}
