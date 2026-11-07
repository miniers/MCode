package agentconf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/config"
)

// catalogEntryByID 取出预设条目，供测试断言。
func catalogEntryByID(t *testing.T, id string) MCPCatalogEntry {
	t.Helper()
	for _, entry := range MCPCatalog() {
		if entry.ID == id {
			return entry
		}
	}
	t.Fatalf("catalog entry %q not found", id)
	return MCPCatalogEntry{}
}

func TestMCPCatalogResolvePlaceholders(t *testing.T) {
	slack := catalogEntryByID(t, "slack")
	srv, secretEnvKeys, err := slack.Resolve(map[string]string{"token": "xoxb-test"})
	if err != nil {
		t.Fatalf("Resolve(slack) failed: %v", err)
	}
	if srv.Name != "slack" || srv.Transport != "stdio" || srv.Command != "npx" {
		t.Fatalf("unexpected server: %+v", srv)
	}
	if srv.Env["SLACK_MCP_XOXB_TOKEN"] != "xoxb-test" {
		t.Fatalf("expected token placeholder replaced, got %q", srv.Env["SLACK_MCP_XOXB_TOKEN"])
	}
	if srv.Env["SLACK_MCP_ADD_MESSAGE_TOOL"] != "true" {
		t.Fatalf("expected static env kept, got %+v", srv.Env)
	}
	if len(secretEnvKeys) != 1 || secretEnvKeys[0] != "SLACK_MCP_XOXB_TOKEN" {
		t.Fatalf("expected token env marked secret, got %v", secretEnvKeys)
	}

	// 路径占位符落进参数里
	filesystem := catalogEntryByID(t, "filesystem")
	fsSrv, _, err := filesystem.Resolve(map[string]string{"root": "/tmp/project"})
	if err != nil {
		t.Fatalf("Resolve(filesystem) failed: %v", err)
	}
	lastArg := fsSrv.Args[len(fsSrv.Args)-1]
	if lastArg != "/tmp/project" {
		t.Fatalf("expected root arg replaced, got %q", lastArg)
	}

	// oauth 条目转成 http 传输，{instance} 去掉协议和结尾斜杠
	gitlab := catalogEntryByID(t, "gitlab")
	glSrv, _, err := gitlab.Resolve(map[string]string{"instance": "https://gitlab.example.com/"})
	if err != nil {
		t.Fatalf("Resolve(gitlab) failed: %v", err)
	}
	if glSrv.Transport != "http" || glSrv.URL != "https://gitlab.example.com/api/v4/mcp" {
		t.Fatalf("unexpected gitlab server: transport=%q url=%q", glSrv.Transport, glSrv.URL)
	}

	// 留空时回落到默认值
	glDefault, _, err := gitlab.Resolve(nil)
	if err != nil {
		t.Fatalf("Resolve(gitlab, defaults) failed: %v", err)
	}
	if glDefault.URL != "https://gitlab.com/api/v4/mcp" {
		t.Fatalf("expected default instance, got %q", glDefault.URL)
	}
}

func TestMCPCatalogResolveOptionalHeaderDropped(t *testing.T) {
	context7 := catalogEntryByID(t, "context7")

	// 未填可选密钥时不发送落地成 "Bearer " 的半截请求头
	emptySrv, _, err := context7.Resolve(nil)
	if err != nil {
		t.Fatalf("Resolve(context7, empty) failed: %v", err)
	}
	if len(emptySrv.Headers) != 0 {
		t.Fatalf("expected no headers when optional key empty, got %v", emptySrv.Headers)
	}

	filledSrv, _, err := context7.Resolve(map[string]string{"key": "ctx7sk-test"})
	if err != nil {
		t.Fatalf("Resolve(context7, key) failed: %v", err)
	}
	if filledSrv.Headers["Authorization"] != "Bearer ctx7sk-test" {
		t.Fatalf("expected bearer header, got %v", filledSrv.Headers)
	}
}

func TestMCPCatalogResolveRequiredFieldMissing(t *testing.T) {
	github := catalogEntryByID(t, "github")
	if _, _, err := github.Resolve(nil); err == nil {
		t.Fatalf("expected error when required token missing")
	}
}

func TestMCPCatalogResolvePostgresSecretEnv(t *testing.T) {
	postgres := catalogEntryByID(t, "postgres")
	srv, secretEnvKeys, err := postgres.Resolve(map[string]string{"url": "postgresql://user:pass@localhost:5432/app"})
	if err != nil {
		t.Fatalf("Resolve(postgres) failed: %v", err)
	}
	if srv.Env["DSN"] != "postgresql://user:pass@localhost:5432/app" {
		t.Fatalf("expected DSN env, got %+v", srv.Env)
	}
	// 连接串不得出现在参数里
	for _, arg := range srv.Args {
		if arg == "postgresql://user:pass@localhost:5432/app" {
			t.Fatalf("expected no DSN in args, got %v", srv.Args)
		}
	}
	if len(secretEnvKeys) != 1 || secretEnvKeys[0] != "DSN" {
		t.Fatalf("expected DSN marked secret, got %v", secretEnvKeys)
	}
	if len(srv.SecretEnvKeys) != 1 || srv.SecretEnvKeys[0] != "DSN" {
		t.Fatalf("expected SecretEnvKeys populated, got %v", srv.SecretEnvKeys)
	}
}

func TestMCPCatalogCount(t *testing.T) {
	entries := MCPCatalog()
	if len(entries) != 22 {
		t.Fatalf("expected 22 catalog entries, got %d", len(entries))
	}
	seen := make(map[string]bool, len(entries))
	categories := make(map[string]bool, len(MCPCatalogCategories()))
	for _, c := range MCPCatalogCategories() {
		categories[c] = true
	}
	for _, entry := range entries {
		if seen[entry.ID] {
			t.Fatalf("duplicate catalog id %q", entry.ID)
		}
		seen[entry.ID] = true
		if !categories[entry.Category] {
			t.Fatalf("entry %q has unknown category %q", entry.ID, entry.Category)
		}
		switch entry.Transport {
		case "stdio":
			if entry.Command == "" {
				t.Fatalf("stdio entry %q has no command", entry.ID)
			}
		case "http", "oauth":
			if entry.URL == "" {
				t.Fatalf("remote entry %q has no url", entry.ID)
			}
		default:
			t.Fatalf("entry %q has unknown transport %q", entry.ID, entry.Transport)
		}
	}
}

func TestMCPHeaderSecretRoundTrip(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	dataDir := t.TempDir()

	memSecrets := config.NewMemorySecretStore()
	SetSecretStore(memSecrets)
	defer func() {
		SetSecretStore(config.NewSecretStore("mcode.mcp"))
	}()

	opts := Options{Home: home, ProjectDir: proj, DataDir: dataDir}

	srv := MCPServer{
		Name:      "header-srv",
		Scope:     "global",
		Transport: "http",
		URL:       "https://mcp.example.com/mcp",
		Headers: map[string]string{
			"Authorization": "Bearer {{SECRET_ermzhk3v}}",
			"X-Trace":       "plain",
		},
	}
	if err := SaveMCPServer(opts, srv); err != nil {
		t.Fatalf("SaveMCPServer failed: %v", err)
	}

	// Authorization 的取值进凭据库，配置里只留键名
	val, err := memSecrets.Get("mcp.header-srv.header.Authorization")
	if err != nil || val != "Bearer {{SECRET_ermzhk3v}}" {
		t.Fatalf("expected header secret stored, got val=%q err=%v", val, err)
	}

	raw, err := os.ReadFile(filepath.Join(dataDir, "mcp.json"))
	if err != nil {
		t.Fatalf("read config failed: %v", err)
	}
	if strings.Contains(string(raw), "{{SECRET_ermzhk3v}}") {
		t.Fatalf("expected no plaintext secret in config, got %s", raw)
	}
	if !strings.Contains(string(raw), "secretHeaderKeys") || !strings.Contains(string(raw), "X-Trace") {
		t.Fatalf("expected plain header and secret key list in config, got %s", raw)
	}

	// 无凭据扫描不暴露请求头
	servers, err := ScanMCP(opts)
	if err != nil {
		t.Fatalf("ScanMCP failed: %v", err)
	}
	for _, s := range servers {
		if s.Name == "header-srv" && s.Headers != nil {
			t.Fatalf("expected nil Headers without credentials, got %v", s.Headers)
		}
	}

	// 带凭据扫描还原请求头
	credOpts := opts
	credOpts.WithCredentials = true
	credServers, err := ScanMCP(credOpts)
	if err != nil {
		t.Fatalf("ScanMCP with credentials failed: %v", err)
	}
	var found *MCPServer
	for i := range credServers {
		if credServers[i].Name == "header-srv" {
			found = &credServers[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected header-srv in ScanMCP result")
	}
	if found.Headers["Authorization"] != "Bearer {{SECRET_ermzhk3v}}" || found.Headers["X-Trace"] != "plain" {
		t.Fatalf("expected restored headers, got %v", found.Headers)
	}

	// 删除服务器时一并清理 header 凭据
	if err := DeleteMCPServer(opts, "header-srv"); err != nil {
		t.Fatalf("DeleteMCPServer failed: %v", err)
	}
	if _, err := memSecrets.Get("mcp.header-srv.header.Authorization"); err == nil {
		t.Fatalf("expected header secret deleted")
	}
}
