package agentconf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func readCustomMCPConfigForTest(t *testing.T, dataDir string) customMCPConfigFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dataDir, "mcp.json"))
	if err != nil {
		t.Fatalf("读取 mcp.json 失败：%v", err)
	}
	var cfg customMCPConfigFile
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("解析 mcp.json 失败：%v", err)
	}
	return cfg
}

func TestSaveMCPServerKeepsSecretKeyOnEmptyValue(t *testing.T) {
	store := useTestSecretStore(t)
	opts := Options{Home: t.TempDir(), ProjectDir: t.TempDir(), DataDir: t.TempDir()}

	initial := MCPServer{
		Name:      "secret-keep-srv",
		Scope:     "global",
		Transport: "stdio",
		Command:   "node",
		Env: map[string]string{
			"API_TOKEN": "abc",
		},
	}
	if err := SaveMCPServer(opts, initial); err != nil {
		t.Fatalf("首次 SaveMCPServer 失败：%v", err)
	}

	edited := MCPServer{
		Name:      "secret-keep-srv",
		Scope:     "global",
		Transport: "stdio",
		Command:   "node",
		Env: map[string]string{
			"API_TOKEN": "",
		},
	}
	if err := SaveMCPServer(opts, edited); err != nil {
		t.Fatalf("空值保存 SaveMCPServer 失败：%v", err)
	}

	cfg := readCustomMCPConfigForTest(t, opts.DataDir)
	rec, ok := cfg.MCPServers["secret-keep-srv"]
	if !ok {
		t.Fatalf("mcp.json 缺少服务记录")
	}
	if !slices.Contains(rec.SecretEnvKeys, "API_TOKEN") {
		t.Fatalf("SecretEnvKeys 应保留 API_TOKEN，实际为 %v", rec.SecretEnvKeys)
	}
	if _, inPlain := rec.Env["API_TOKEN"]; inPlain {
		t.Fatalf("API_TOKEN 不应写入明文 Env：%v", rec.Env)
	}

	if val, err := store.Get("mcp.secret-keep-srv.API_TOKEN"); err != nil || val != "abc" {
		t.Fatalf("凭据库中 API_TOKEN 应仍为 abc，实际 val=%q err=%v", val, err)
	}

	credOpts := opts
	credOpts.WithCredentials = true
	srv := mcpServerByName(t, mustScanMCP(t, credOpts), "secret-keep-srv")
	if srv.Env["API_TOKEN"] != "abc" {
		t.Fatalf("读取配置应解析出 API_TOKEN=abc，实际为 %q", srv.Env["API_TOKEN"])
	}

	t.Run("old_secret_env_keys_preserved_without_resurrecting_deleted", func(t *testing.T) {
		first := MCPServer{
			Name:          "explicit-secret-srv",
			Scope:         "global",
			Transport:     "stdio",
			Command:       "node",
			SecretEnvKeys: []string{"DSN", "REMOVED_SECRET"},
			Env: map[string]string{
				"DSN":            "postgres://abc",
				"REMOVED_SECRET": "old-secret",
			},
		}
		if err := SaveMCPServer(opts, first); err != nil {
			t.Fatalf("首次 SaveMCPServer 失败：%v", err)
		}

		// 模拟编辑表单提交：不回传 SecretEnvKeys，DSN 留空沿用旧值，REMOVED_SECRET 被删除。
		second := MCPServer{
			Name:      "explicit-secret-srv",
			Scope:     "global",
			Transport: "stdio",
			Command:   "node",
			Env: map[string]string{
				"DSN": "",
			},
		}
		if err := SaveMCPServer(opts, second); err != nil {
			t.Fatalf("编辑 SaveMCPServer 失败：%v", err)
		}

		cfg2 := readCustomMCPConfigForTest(t, opts.DataDir)
		rec2 := cfg2.MCPServers["explicit-secret-srv"]
		if !slices.Equal(rec2.SecretEnvKeys, []string{"DSN"}) {
			t.Fatalf("SecretEnvKeys 期望 [DSN]，实际为 %v", rec2.SecretEnvKeys)
		}
		if _, inPlain := rec2.Env["DSN"]; inPlain {
			t.Fatalf("DSN 不应写入明文 Env：%v", rec2.Env)
		}
		if val, err := store.Get("mcp.explicit-secret-srv.DSN"); err != nil || val != "postgres://abc" {
			t.Fatalf("凭据库中 DSN 应仍为 postgres://abc，实际 val=%q err=%v", val, err)
		}

		srv2 := mcpServerByName(t, mustScanMCP(t, credOpts), "explicit-secret-srv")
		if srv2.Env["DSN"] != "postgres://abc" {
			t.Fatalf("读取配置应解析出 DSN=postgres://abc，实际为 %v", srv2.Env)
		}
		if _, exists := srv2.Env["REMOVED_SECRET"]; exists {
			t.Fatalf("已删除的变量不应复活：%v", srv2.Env)
		}
	})
}
