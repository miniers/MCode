package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/harness"
)

func TestMCPSettingsEntries(t *testing.T) {
	workDir := t.TempDir()

	// 共享端点：URL/Token 加 X-Mcode-Workdir 头
	servers := mcpSettingsEntries(harness.Turn{WorkDir: workDir}, harness.BuiltinMCP{URL: "http://127.0.0.1:1/mcp", Token: "shared"}, nil)
	builtin, ok := servers[mcpBuiltinServerName]
	if !ok {
		t.Fatal("missing builtin mcode server")
	}
	if builtin.URL != "http://127.0.0.1:1/mcp" {
		t.Errorf("unexpected builtin url: %s", builtin.URL)
	}
	if got := builtin.Headers["Authorization"]; got != "Bearer shared" {
		t.Errorf("unexpected builtin authorization: %s", got)
	}
	if got := builtin.Headers["X-Mcode-Workdir"]; got != workDir {
		t.Errorf("unexpected workdir header: %s", got)
	}

	// 线程端点：地址按会话路由，不带 X-Mcode-Workdir 头
	threadServers := mcpSettingsEntries(harness.Turn{WorkDir: workDir, ThreadID: "t-1"}, harness.BuiltinMCP{
		ThreadBase:  "http://127.0.0.1:1/threads/",
		ThreadToken: "thread",
		URL:         "http://127.0.0.1:1/mcp",
		Token:       "shared",
	}, nil)
	thread := threadServers[mcpBuiltinServerName]
	if thread.URL != "http://127.0.0.1:1/threads/t-1" {
		t.Errorf("unexpected thread url: %s", thread.URL)
	}
	if got := thread.Headers["Authorization"]; got != "Bearer thread" {
		t.Errorf("unexpected thread authorization: %s", got)
	}
	if _, exists := thread.Headers["X-Mcode-Workdir"]; exists {
		t.Error("thread endpoint must not send X-Mcode-Workdir")
	}

	// 外部端点：跳过内置与审批保留名，带令牌时写 Authorization
	external := mcpSettingsEntries(harness.Turn{WorkDir: workDir}, harness.BuiltinMCP{}, []harness.MCPEndpoint{
		{Name: "mcode", URL: "http://bad/mcp"},
		{Name: "mcode_approval", URL: "http://bad/mcp"},
		{Name: "docs", URL: "http://127.0.0.1:2/mcp", Token: "ext"},
		{Name: "plain", URL: "http://127.0.0.1:3/mcp"},
		{Name: "", URL: "http://127.0.0.1:4/mcp"},
		{Name: "empty-url", URL: ""},
	})
	if len(external) != 2 {
		t.Fatalf("expected 2 external servers, got %d: %+v", len(external), external)
	}
	if got := external["docs"].Headers["Authorization"]; got != "Bearer ext" {
		t.Errorf("unexpected external authorization: %s", got)
	}
	if len(external["plain"].Headers) != 0 {
		t.Errorf("server without token must not send headers: %+v", external["plain"].Headers)
	}
	if _, exists := external[mcpBuiltinServerName]; exists {
		t.Error("external servers must not override the builtin name")
	}
}

func TestPrepareMCPGeminiDir(t *testing.T) {
	sourceDir := t.TempDir()
	// 源目录里放上登录态、目录条目与用户自己的 MCP 配置
	if err := os.MkdirAll(filepath.Join(sourceDir, "antigravity", "conversations"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "antigravity", "conversations", "c1.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "oauth_creds.json"), []byte(`{"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(sourceDir, "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"userSettings":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	userMCP := `{"mcpServers":{"user-only":{"url":"http://127.0.0.1:9/mcp"}}}`
	if err := os.WriteFile(filepath.Join(configDir, mcpConfigFileName), []byte(userMCP), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(configDir, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}

	turn := harness.Turn{
		WorkDir: t.TempDir(),
		Mode:    harness.ModeAutoAcceptEdits,
	}
	args, cleanup, err := prepareMCPGeminiDir(turn,
		[]string{"--gemini_dir=" + sourceDir},
		nil,
		harness.BuiltinMCP{URL: "http://127.0.0.1:1/mcp", Token: "thread-token"},
		func(workDir, threadID string) []harness.MCPEndpoint {
			return []harness.MCPEndpoint{{Name: "docs", URL: "http://127.0.0.1:2/mcp", Token: "ext"}}
		},
	)
	if err != nil {
		t.Fatalf("prepareMCPGeminiDir failed: %v", err)
	}
	if len(args) != 1 || !strings.HasPrefix(args[0], "--gemini_dir=") {
		t.Fatalf("unexpected args: %v", args)
	}
	sessionDir := strings.TrimPrefix(args[0], "--gemini_dir=")
	if sessionDir == sourceDir {
		t.Fatal("session gemini dir must not be the user's own directory")
	}
	defer cleanup()

	data, err := os.ReadFile(filepath.Join(sessionDir, "config", mcpConfigFileName))
	if err != nil {
		t.Fatalf("read generated mcp_config.json: %v", err)
	}
	var settings struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal generated mcp_config.json: %v", err)
	}
	if len(settings.MCPServers) != 2 {
		t.Fatalf("expected builtin plus external servers, got %+v", settings.MCPServers)
	}
	if got := settings.MCPServers[mcpBuiltinServerName].URL; got != "http://127.0.0.1:1/mcp" {
		t.Errorf("unexpected builtin url: %s", got)
	}
	if got := settings.MCPServers["docs"].Headers["Authorization"]; got != "Bearer ext" {
		t.Errorf("unexpected external headers: %+v", settings.MCPServers["docs"].Headers)
	}

	// 生成的 mcp_config.json 必须是实体文件，其余条目是软链接且可正常读取
	assertRegularFile(t, filepath.Join(sessionDir, "config", mcpConfigFileName))
	assertSymlink(t, filepath.Join(sessionDir, "config", "config.json"))
	assertSymlink(t, filepath.Join(sessionDir, "oauth_creds.json"))
	assertSymlink(t, filepath.Join(sessionDir, "antigravity"))

	if got, err := os.ReadFile(filepath.Join(sessionDir, "config", "config.json")); err != nil || string(got) != `{"userSettings":{}}` {
		t.Fatalf("symlinked config must stay readable: %s (%v)", string(got), err)
	}

	// 用户的全局配置不能被改写
	if got, err := os.ReadFile(filepath.Join(configDir, mcpConfigFileName)); err != nil || string(got) != userMCP {
		t.Fatalf("user's own mcp_config.json was modified: %s (%v)", string(got), err)
	}

	cleanup()
	if _, statErr := os.Stat(sessionDir); !os.IsNotExist(statErr) {
		t.Fatalf("cleanup did not remove the session dir: %v", statErr)
	}
}

func TestPrepareMCPGeminiDirSkipsWithoutEndpoints(t *testing.T) {
	turn := harness.Turn{WorkDir: t.TempDir()}
	args, cleanup, err := prepareMCPGeminiDir(turn, nil, nil, harness.BuiltinMCP{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(args) != 0 || cleanup != nil {
		t.Fatalf("expected no injection without endpoints, got args=%v", args)
	}

	args, cleanup, err = prepareMCPGeminiDir(turn, nil, nil, harness.BuiltinMCP{}, func(string, string) []harness.MCPEndpoint {
		return []harness.MCPEndpoint{{Name: "mcode"}}
	})
	if err != nil || len(args) != 0 || cleanup != nil {
		t.Fatalf("expected no injection for skipped endpoints, got args=%v err=%v", args, err)
	}
}

// 源目录不存在或为空时，这一轮没有登录态可继承，不应注入 --gemini_dir。
func TestPrepareMCPGeminiDirSkipsWithoutSourceEntries(t *testing.T) {
	tmpRoot := t.TempDir()
	t.Setenv("TMPDIR", tmpRoot)

	cases := []struct {
		name      string
		sourceDir func(t *testing.T) string
	}{
		{
			name: "source dir missing",
			sourceDir: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "missing-gemini")
			},
		},
		{
			name:      "source dir empty",
			sourceDir: func(t *testing.T) string { return t.TempDir() },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sourceDir := tc.sourceDir(t)
			turn := harness.Turn{WorkDir: t.TempDir()}
			args, cleanup, err := prepareMCPGeminiDir(turn,
				[]string{"--gemini_dir=" + sourceDir},
				nil,
				harness.BuiltinMCP{URL: "http://127.0.0.1:1/mcp", Token: "thread-token"},
				func(string, string) []harness.MCPEndpoint {
					return []harness.MCPEndpoint{{Name: "docs", URL: "http://127.0.0.1:2/mcp", Token: "ext"}}
				},
			)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(args) != 0 {
				t.Fatalf("expected no --gemini_dir without source entries, got %v", args)
			}
			// 与“没有服务器时不注入”分支一致，cleanup 为 nil；能调用时也必须安全。
			if cleanup != nil {
				cleanup()
			}

			// t.TempDir 也依赖 TMPDIR，源目录和 WorkDir 都会落在这个根下，
			// 因此只断言没有 prepareMCPGeminiDir 用 os.MkdirTemp("", "mcode-agy-mcp-") 建出的会话目录。
			entries, readErr := os.ReadDir(tmpRoot)
			if readErr != nil {
				t.Fatalf("read temp root: %v", readErr)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "mcode-agy-mcp-") {
					t.Fatalf("expected no session gemini dir, found %v", entries)
				}
			}
		})
	}
}

func TestResolveSourceGeminiDir(t *testing.T) {
	if got := resolveSourceGeminiDir([]string{"--model", "x", "--gemini_dir=/custom/gemini"}, nil, nil); got != "/custom/gemini" {
		t.Fatalf("route args must win, got %s", got)
	}

	home := t.TempDir()
	if got := resolveSourceGeminiDir(nil, []string{"HOME=/ignored"}, []string{"HOME=" + home}); got != filepath.Join(home, ".gemini") {
		t.Fatalf("expected route env home, got %s", got)
	}
	if got := resolveSourceGeminiDir(nil, []string{"HOME=" + home}, nil); got != filepath.Join(home, ".gemini") {
		t.Fatalf("expected turn env home, got %s", got)
	}
}

func assertSymlink(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s should be a symlink", path)
	}
}

func assertRegularFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s should be a regular file", path)
	}
}
