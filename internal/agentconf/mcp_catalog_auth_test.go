package agentconf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestMCPAuthModeConstants 锁定登录方式常量的字符串取值，需与 internal/mcp 包保持一致。
func TestMCPAuthModeConstants(t *testing.T) {
	cases := map[string]string{
		MCPAuthModeAuto:     "auto",
		MCPAuthModeLoopback: "loopback",
		MCPAuthModeDevice:   "device",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("auth mode constant = %q, want %q", got, want)
		}
	}
}

// TestCatalogResolveAuth 验证目录条目的 Auth 会写入 MCPServer，且 Scopes 是深拷贝。
func TestCatalogResolveAuth(t *testing.T) {
	entry := MCPCatalogEntry{
		ID:        "synthetic-device",
		Name:      "Synthetic Device",
		Transport: "http",
		URL:       "https://mcp.example.com/mcp",
		Auth: &MCPCatalogAuth{
			Mode:     MCPAuthModeDevice,
			ClientID: "client-synthetic",
			Scopes:   []string{"read", "write"},
		},
	}

	srv, _, err := entry.Resolve(nil)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if srv.Auth == nil {
		t.Fatalf("expected srv.Auth, got nil")
	}
	if srv.Auth.Mode != "device" {
		t.Fatalf("expected auth mode device, got %q", srv.Auth.Mode)
	}
	if srv.Auth.ClientID != "client-synthetic" {
		t.Fatalf("expected client id copied, got %q", srv.Auth.ClientID)
	}
	if len(srv.Auth.Scopes) != 2 || srv.Auth.Scopes[0] != "read" || srv.Auth.Scopes[1] != "write" {
		t.Fatalf("expected scopes copied, got %v", srv.Auth.Scopes)
	}
	if srv.Auth == entry.Auth {
		t.Fatalf("expected auth deep copy, got shared pointer")
	}

	// 改目录条目或其 Scopes 都不能影响已解析出的服务。
	entry.Auth.Mode = MCPAuthModeLoopback
	entry.Auth.Scopes[0] = "changed"
	if srv.Auth.Mode != "device" || srv.Auth.Scopes[0] != "read" {
		t.Fatalf("expected resolved auth isolated from entry, got mode=%q scopes=%v", srv.Auth.Mode, srv.Auth.Scopes)
	}
}

// TestCatalogResolveRejectsUnknownAuthMode 非法登录方式在 Resolve 处即报错。
func TestCatalogResolveRejectsUnknownAuthMode(t *testing.T) {
	entry := MCPCatalogEntry{
		ID:        "synthetic-pkce",
		Transport: "http",
		URL:       "https://mcp.example.com/mcp",
		Auth:      &MCPCatalogAuth{Mode: "pkce"},
	}
	if _, _, err := entry.Resolve(nil); err == nil {
		t.Fatalf("expected error for unknown auth mode")
	}
}

// TestSaveMCPServerPersistsAuth 验证 Auth 写入 mcp.json 并能重扫读回。
func TestSaveMCPServerPersistsAuth(t *testing.T) {
	opts := Options{Home: t.TempDir(), ProjectDir: t.TempDir(), DataDir: t.TempDir()}

	srv := MCPServer{
		Name:      "auth-srv",
		Scope:     "global",
		Transport: "http",
		URL:       "https://mcp.example.com/mcp",
		Auth: &MCPCatalogAuth{
			Mode:     MCPAuthModeDevice,
			ClientID: "client-saved",
			Scopes:   []string{"read", "write"},
		},
	}
	if err := SaveMCPServer(opts, srv); err != nil {
		t.Fatalf("SaveMCPServer failed: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(opts.DataDir, "mcp.json"))
	if err != nil {
		t.Fatalf("read config failed: %v", err)
	}
	// 断言配置结构而不是序列化格式：mcp.json 是缩进输出，字符串匹配依赖空格。
	var parsed struct {
		MCPServers map[string]struct {
			Auth *struct {
				Mode     string   `json:"mode"`
				ClientID string   `json:"clientId"`
				Scopes   []string `json:"scopes"`
			} `json:"auth"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal config failed: %v, got %s", err, raw)
	}
	persisted, ok := parsed.MCPServers["auth-srv"]
	if !ok {
		t.Fatalf("expected auth-srv in config, got %s", raw)
	}
	if persisted.Auth == nil {
		t.Fatalf("expected auth persisted, got %s", raw)
	}
	if persisted.Auth.Mode != MCPAuthModeDevice || persisted.Auth.ClientID != "client-saved" {
		t.Fatalf("unexpected persisted auth: %+v", persisted.Auth)
	}
	if len(persisted.Auth.Scopes) != 2 || persisted.Auth.Scopes[0] != "read" || persisted.Auth.Scopes[1] != "write" {
		t.Fatalf("expected scopes persisted, got %v", persisted.Auth.Scopes)
	}

	servers, err := ScanMCP(opts)
	if err != nil {
		t.Fatalf("ScanMCP failed: %v", err)
	}
	var found *MCPServer
	for i := range servers {
		if servers[i].Name == "auth-srv" {
			found = &servers[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected auth-srv in ScanMCP result")
	}
	if found.Auth == nil {
		t.Fatalf("expected auth backfilled, got nil")
	}
	if found.Auth.Mode != MCPAuthModeDevice || found.Auth.ClientID != "client-saved" {
		t.Fatalf("unexpected auth after rescan: %+v", found.Auth)
	}
	if len(found.Auth.Scopes) != 2 || found.Auth.Scopes[1] != "write" {
		t.Fatalf("expected scopes round-tripped, got %v", found.Auth.Scopes)
	}
}

// TestSaveMCPServerRejectsUnknownAuthMode 非法登录方式不落盘。
func TestSaveMCPServerRejectsUnknownAuthMode(t *testing.T) {
	opts := Options{Home: t.TempDir(), ProjectDir: t.TempDir(), DataDir: t.TempDir()}

	err := SaveMCPServer(opts, MCPServer{
		Name:      "bad-auth-srv",
		Scope:     "global",
		Transport: "http",
		URL:       "https://mcp.example.com/mcp",
		Auth:      &MCPCatalogAuth{Mode: "pkce"},
	})
	if err == nil {
		t.Fatalf("expected error for unknown auth mode")
	}

	cfg, loadErr := loadCustomMCPConfig(filepath.Join(opts.DataDir, "mcp.json"))
	if loadErr != nil {
		t.Fatalf("load config failed: %v", loadErr)
	}
	if _, ok := cfg.MCPServers["bad-auth-srv"]; ok {
		t.Fatalf("expected rejected server not persisted")
	}
}

// TestScanMCPLegacyRecordWithoutAuth 旧格式 mcp.json 没有 auth 字段时正常读取。
func TestScanMCPLegacyRecordWithoutAuth(t *testing.T) {
	opts := Options{Home: t.TempDir(), ProjectDir: t.TempDir(), DataDir: t.TempDir()}

	legacy := `{"mcpServers":{"legacy-srv":{"transport":"http","url":"https://legacy.example.com/mcp"}}}`
	path := filepath.Join(opts.DataDir, "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy config failed: %v", err)
	}

	servers, err := ScanMCP(opts)
	if err != nil {
		t.Fatalf("ScanMCP failed: %v", err)
	}
	var found *MCPServer
	for i := range servers {
		if servers[i].Name == "legacy-srv" {
			found = &servers[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected legacy-srv in ScanMCP result")
	}
	if found.Auth != nil {
		t.Fatalf("expected nil auth for legacy record, got %+v", found.Auth)
	}
}

// TestCatalogOAuthEntriesDeclareAutoAuth 首批 oauth 条目只声明 auto，不写死 client id。
func TestCatalogOAuthEntriesDeclareAutoAuth(t *testing.T) {
	for _, id := range []string{"gitlab", "tavily", "exa"} {
		entry := catalogEntryByID(t, id)
		if entry.Auth == nil {
			t.Fatalf("entry %q has no auth", id)
		}
		if entry.Auth.Mode != MCPAuthModeAuto {
			t.Fatalf("entry %q has auth mode %q, want auto", id, entry.Auth.Mode)
		}
		if entry.Auth.ClientID != "" {
			t.Fatalf("entry %q should not pin client id, got %q", id, entry.Auth.ClientID)
		}
		if len(entry.Auth.Scopes) != 0 {
			t.Fatalf("entry %q should not pin scopes, got %v", id, entry.Auth.Scopes)
		}
	}
}
