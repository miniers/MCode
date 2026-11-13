package agentconf

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mcode/internal/config"
)

// useTestSecretStore 注入内存凭据库，测试结束后恢复真实实现。
func useTestSecretStore(t *testing.T) SecretStore {
	t.Helper()
	store := config.NewMemorySecretStore()
	SetSecretStore(store)
	t.Cleanup(func() {
		SetSecretStore(config.NewSecretStore("mcode.mcp"))
	})
	return store
}

// mustScanMCP 扫描配置，出错时终止测试。
func mustScanMCP(t *testing.T, opts Options) []MCPServer {
	t.Helper()
	servers, err := ScanMCP(opts)
	if err != nil {
		t.Fatalf("ScanMCP 失败：%v", err)
	}
	return servers
}

// mcpServerByName 从扫描结果里取出指定名字的服务。
func mcpServerByName(t *testing.T, servers []MCPServer, name string) MCPServer {
	t.Helper()
	for _, s := range servers {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("扫描结果里没有服务 %s", name)
	return MCPServer{}
}

// 编辑自定义服务时只提交空值或干脆省略敏感键，凭据库里的旧值不能被覆盖。
func TestSaveMCPServerKeepSecretsOnEmptySubmit(t *testing.T) {
	store := useTestSecretStore(t)
	opts := Options{Home: t.TempDir(), ProjectDir: t.TempDir(), DataDir: t.TempDir()}

	original := MCPServer{
		Name:      "keep-secrets",
		Scope:     "global",
		Transport: "http",
		URL:       "https://mcp.example.com/mcp",
		Env: map[string]string{
			"API_TOKEN": "token-1",
			"PLAIN_VAR": "hello",
		},
		Headers: map[string]string{
			"Authorization": "Bearer token-1",
			"X-Trace":       "trace-1",
		},
	}
	if err := SaveMCPServer(opts, original); err != nil {
		t.Fatalf("首次 SaveMCPServer 失败：%v", err)
	}

	// 模拟列表扫描回填后的表单：敏感变量是空串，Authorization 头整组缺失。
	edited := MCPServer{
		Name:      "keep-secrets",
		Scope:     "global",
		Transport: "http",
		URL:       "https://mcp.example.com/mcp",
		Env: map[string]string{
			"API_TOKEN": "",
			"PLAIN_VAR": "hello-2",
		},
		Headers: map[string]string{
			"X-Trace": "trace-2",
		},
	}
	if err := SaveMCPServer(opts, edited); err != nil {
		t.Fatalf("编辑保存失败：%v", err)
	}

	if val, err := store.Get("mcp.keep-secrets.API_TOKEN"); err != nil || val != "token-1" {
		t.Fatalf("敏感环境变量被空值覆盖：val=%q err=%v", val, err)
	}
	if val, err := store.Get("mcp.keep-secrets.header.Authorization"); err != nil || val != "Bearer token-1" {
		t.Fatalf("Authorization 头被空值覆盖：val=%q err=%v", val, err)
	}

	raw, err := os.ReadFile(filepath.Join(opts.DataDir, "mcp.json"))
	if err != nil {
		t.Fatalf("读取配置文件失败：%v", err)
	}
	if !strings.Contains(string(raw), "API_TOKEN") || !strings.Contains(string(raw), "Authorization") {
		t.Fatalf("敏感键名应保留在记录里：%s", raw)
	}
	if strings.Contains(string(raw), "token-1") {
		t.Fatalf("配置文件不应出现凭据明文：%s", raw)
	}

	credOpts := opts
	credOpts.WithCredentials = true
	srv := mcpServerByName(t, mustScanMCP(t, credOpts), "keep-secrets")
	if srv.Env["API_TOKEN"] != "token-1" || srv.Env["PLAIN_VAR"] != "hello-2" {
		t.Fatalf("环境变量沿用旧凭据失败：%v", srv.Env)
	}
	if srv.Headers["Authorization"] != "Bearer token-1" || srv.Headers["X-Trace"] != "trace-2" {
		t.Fatalf("请求头沿用旧凭据失败：%v", srv.Headers)
	}
}

// 仓库内配置的服务默认未信任且禁用，启用后信任，命令一变又要重新信任。
func TestScanMCPUntrustedProjectServer(t *testing.T) {
	useTestSecretStore(t)
	proj := t.TempDir()
	opts := Options{Home: t.TempDir(), ProjectDir: proj, DataDir: t.TempDir()}

	writeRepoMCPConfig := func(command string) {
		content := `{"mcpServers":{"repo-srv":{"command":"` + command + `","args":["--stdio"]}}}`
		if err := os.WriteFile(filepath.Join(proj, ".mcp.json"), []byte(content), 0600); err != nil {
			t.Fatalf("写入 .mcp.json 失败：%v", err)
		}
	}
	writeRepoMCPConfig("node")

	srv := mcpServerByName(t, mustScanMCP(t, opts), "repo-srv")
	if !srv.Untrusted || !srv.Disabled {
		t.Fatalf("仓库内服务应默认未信任并禁用：untrusted=%v disabled=%v", srv.Untrusted, srv.Disabled)
	}

	// 在设置里启用即表示信任其命令。
	if err := SetMCPServerEnabled(opts, "repo-srv", true); err != nil {
		t.Fatalf("SetMCPServerEnabled 失败：%v", err)
	}
	trusted := mcpServerByName(t, mustScanMCP(t, opts), "repo-srv")
	if trusted.Untrusted || trusted.Disabled {
		t.Fatalf("信任后应可执行：untrusted=%v disabled=%v", trusted.Untrusted, trusted.Disabled)
	}

	// 命令改变后指纹不同，必须重新确认信任。
	writeRepoMCPConfig("python")
	changed := mcpServerByName(t, mustScanMCP(t, opts), "repo-srv")
	if !changed.Untrusted || !changed.Disabled {
		t.Fatalf("命令变更后应回到未信任：untrusted=%v disabled=%v", changed.Untrusted, changed.Disabled)
	}
}

// 不同 Agent 的仓库配置里有同名服务时，启用其中一条只信任这一条，另一条仍未信任。
func TestSetMCPServerEnabledForTrustsOnlyThatServer(t *testing.T) {
	useTestSecretStore(t)
	proj := t.TempDir()
	opts := Options{Home: t.TempDir(), ProjectDir: proj, DataDir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(proj, ".mcp.json"), []byte(`{"mcpServers":{"dup":{"command":"node","args":["a.js"]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(proj, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".codex", "config.toml"), []byte("[mcp_servers.dup]\ncommand = \"sh\"\nargs = [\"evil.sh\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	byAgent := func() map[string]MCPServer {
		out := map[string]MCPServer{}
		for _, s := range mustScanMCP(t, opts) {
			if s.Name == "dup" {
				out[s.Agent] = s
			}
		}
		return out
	}
	before := byAgent()
	if len(before) != 2 || !before["claude"].Untrusted || !before["codex"].Untrusted {
		t.Fatalf("两条同名仓库服务都应默认未信任：%+v", before)
	}
	if err := SetMCPServerEnabledFor(opts, before["claude"], true); err != nil {
		t.Fatalf("SetMCPServerEnabledFor 失败：%v", err)
	}
	after := byAgent()
	if after["claude"].Untrusted || after["claude"].Disabled {
		t.Fatalf("被启用的那条应已信任：%+v", after["claude"])
	}
	if !after["codex"].Untrusted || !after["codex"].Disabled {
		t.Fatalf("另一条同名服务不应被连带信任：%+v", after["codex"])
	}
}

// 仓库把已信任服务的 env/headers 改掉，等于换了一份代码去执行，必须重新确认信任。
func TestScanMCPProjectFingerprintCoversEnvAndHeaders(t *testing.T) {
	useTestSecretStore(t)
	proj := t.TempDir()
	opts := Options{Home: t.TempDir(), ProjectDir: proj, DataDir: t.TempDir()}

	writeRepoMCPConfig := func(env, headers string) {
		content := `{"mcpServers":{"repo-env":{"command":"node","args":["--stdio"],"env":` + env + `,"headers":` + headers + `}}}`
		if err := os.WriteFile(filepath.Join(proj, ".mcp.json"), []byte(content), 0600); err != nil {
			t.Fatalf("写入 .mcp.json 失败：%v", err)
		}
	}
	trustedState := func(cred bool) (untrusted, disabled bool) {
		scanOpts := opts
		scanOpts.WithCredentials = cred
		srv := mcpServerByName(t, mustScanMCP(t, scanOpts), "repo-env")
		return srv.Untrusted, srv.Disabled
	}

	writeRepoMCPConfig(`{"NODE_OPTIONS":"--max-old-space-size=512"}`, `{"X-Trace":"trace-1"}`)
	if untrusted, _ := trustedState(false); !untrusted {
		t.Fatal("仓库内服务应默认未信任")
	}

	if err := SetMCPServerEnabled(opts, "repo-env", true); err != nil {
		t.Fatalf("SetMCPServerEnabled 失败：%v", err)
	}
	// 未改动时应保持信任，且带凭据与不带凭据两种扫描的信任状态一致。
	if untrusted, disabled := trustedState(false); untrusted || disabled {
		t.Fatalf("信任后应可执行：untrusted=%v disabled=%v", untrusted, disabled)
	}
	if untrusted, disabled := trustedState(true); untrusted || disabled {
		t.Fatalf("带凭据扫描的信任状态应一致：untrusted=%v disabled=%v", untrusted, disabled)
	}

	// 环境变量取值被改（典型是塞进 --require）必须回到未信任。
	writeRepoMCPConfig(`{"NODE_OPTIONS":"--require ./evil.js"}`, `{"X-Trace":"trace-1"}`)
	if untrusted, disabled := trustedState(false); !untrusted || !disabled {
		t.Fatalf("env 变更后应回到未信任：untrusted=%v disabled=%v", untrusted, disabled)
	}

	// 请求头取值被改同理。
	writeRepoMCPConfig(`{"NODE_OPTIONS":"--max-old-space-size=512"}`, `{"X-Trace":"trace-2"}`)
	if untrusted, disabled := trustedState(false); !untrusted || !disabled {
		t.Fatalf("headers 变更后应回到未信任：untrusted=%v disabled=%v", untrusted, disabled)
	}

	// 内容改回原样时旧信任记录仍然有效，不需要再次确认。
	writeRepoMCPConfig(`{"NODE_OPTIONS":"--max-old-space-size=512"}`, `{"X-Trace":"trace-1"}`)
	if untrusted, disabled := trustedState(false); untrusted || disabled {
		t.Fatalf("改回原内容后应仍为已信任：untrusted=%v disabled=%v", untrusted, disabled)
	}
}

// 界面里创建的项目级服务视为用户亲手信任，扫描后不应被标成未信任。
func TestSaveMCPServerProjectScopeTrusted(t *testing.T) {
	useTestSecretStore(t)
	proj := t.TempDir()
	opts := Options{Home: t.TempDir(), ProjectDir: proj, DataDir: t.TempDir()}

	srv := MCPServer{
		Agent:     "custom",
		Scope:     "project",
		Name:      "ui-srv",
		Transport: "stdio",
		Command:   "node",
		Args:      []string{"server.js"},
	}
	if err := SaveMCPServer(opts, srv); err != nil {
		t.Fatalf("SaveMCPServer 失败：%v", err)
	}

	saved := mcpServerByName(t, mustScanMCP(t, opts), "ui-srv")
	if saved.Untrusted || saved.Disabled {
		t.Fatalf("界面创建的项目级服务应已信任：untrusted=%v disabled=%v", saved.Untrusted, saved.Disabled)
	}
}

// 按作用域删除只删一份：另一个作用域的同名服务与两侧共用的凭据都要保留。
func TestDeleteMCPServerInScope(t *testing.T) {
	store := useTestSecretStore(t)
	proj := t.TempDir()
	opts := Options{Home: t.TempDir(), ProjectDir: proj, DataDir: t.TempDir()}

	for _, scope := range []string{"global", "project"} {
		if err := SaveMCPServer(opts, MCPServer{
			Agent:     "custom",
			Scope:     scope,
			Name:      "dup-srv",
			Transport: "stdio",
			Command:   "node",
			Env:       map[string]string{"API_TOKEN": "token-dup"},
		}); err != nil {
			t.Fatalf("保存 %s 作用域服务失败：%v", scope, err)
		}
	}

	if err := DeleteMCPServerInScope(opts, "project", "dup-srv"); err != nil {
		t.Fatalf("DeleteMCPServerInScope 失败：%v", err)
	}

	projCfg, err := loadCustomMCPConfig(filepath.Join(proj, ".mcode", "mcp.json"))
	if err != nil {
		t.Fatalf("读取项目配置失败：%v", err)
	}
	if _, ok := projCfg.MCPServers["dup-srv"]; ok {
		t.Fatal("项目作用域的服务应被删除")
	}
	globalCfg, err := loadCustomMCPConfig(filepath.Join(opts.DataDir, "mcp.json"))
	if err != nil {
		t.Fatalf("读取全局配置失败：%v", err)
	}
	if _, ok := globalCfg.MCPServers["dup-srv"]; !ok {
		t.Fatal("另一个作用域的同名服务不应被删除")
	}
	// 凭据按服务名存放，另一个作用域还在用，此时不能删。
	if v, err := store.Get("mcp.dup-srv.API_TOKEN"); err != nil || v != "token-dup" {
		t.Fatalf("另一个作用域还在用的凭据不应删除：value=%q err=%v", v, err)
	}

	// 最后一个同名服务删除后，凭据随之清除。
	if err := DeleteMCPServerInScope(opts, "global", "dup-srv"); err != nil {
		t.Fatalf("DeleteMCPServerInScope 失败：%v", err)
	}
	if _, err := store.Get("mcp.dup-srv.API_TOKEN"); err == nil {
		t.Fatal("最后一个同名服务删除后凭据应清除")
	}
}

// 删除项目级服务后，全局停用列表里的同名条目必须清掉，
// 否则以后再建同名服务会默不作声地处于停用状态。
func TestDeleteMCPServerInScopeClearsGlobalDisabled(t *testing.T) {
	useTestSecretStore(t)
	opts := Options{Home: t.TempDir(), ProjectDir: t.TempDir(), DataDir: t.TempDir()}

	if err := SaveMCPServer(opts, MCPServer{
		Agent:     "custom",
		Scope:     "project",
		Name:      "x",
		Transport: "stdio",
		Command:   "node",
	}); err != nil {
		t.Fatalf("保存项目级服务失败：%v", err)
	}
	if err := SetMCPServerEnabled(opts, "x", false); err != nil {
		t.Fatalf("停用服务失败：%v", err)
	}

	if err := DeleteMCPServerInScope(opts, "project", "x"); err != nil {
		t.Fatalf("DeleteMCPServerInScope 失败：%v", err)
	}

	cfg, err := loadCustomMCPConfig(getMCPConfigPath(opts, "global"))
	if err != nil {
		t.Fatalf("读取全局配置失败：%v", err)
	}
	for _, d := range cfg.DisabledServers {
		if d == "x" {
			t.Fatal("已没有任何同名服务，全局停用列表不应残留 x")
		}
	}
}

// 全局还留着同名服务时，删项目级那份不能动全局停用列表，
// 那个条目是全局服务自己的停用状态。
func TestDeleteMCPServerInScopeKeepsGlobalDisabledWhileStillUsed(t *testing.T) {
	useTestSecretStore(t)
	opts := Options{Home: t.TempDir(), ProjectDir: t.TempDir(), DataDir: t.TempDir()}

	for _, scope := range []string{"global", "project"} {
		if err := SaveMCPServer(opts, MCPServer{
			Agent:     "custom",
			Scope:     scope,
			Name:      "y",
			Transport: "stdio",
			Command:   "node",
		}); err != nil {
			t.Fatalf("保存 %s 作用域服务失败：%v", scope, err)
		}
	}
	if err := SetMCPServerEnabled(opts, "y", false); err != nil {
		t.Fatalf("停用服务失败：%v", err)
	}

	if err := DeleteMCPServerInScope(opts, "project", "y"); err != nil {
		t.Fatalf("DeleteMCPServerInScope 失败：%v", err)
	}

	cfg, err := loadCustomMCPConfig(getMCPConfigPath(opts, "global"))
	if err != nil {
		t.Fatalf("读取全局配置失败：%v", err)
	}
	disabled := false
	for _, d := range cfg.DisabledServers {
		if d == "y" {
			disabled = true
		}
	}
	if !disabled {
		t.Fatal("全局仍有同名服务，全局停用列表应保留 y")
	}
}

// 配置文件损坏时删除必须报错并保持原文件不变，不能静默当成功。
func TestDeleteMCPServerRejectsCorruptConfig(t *testing.T) {
	useTestSecretStore(t)
	opts := Options{Home: t.TempDir(), ProjectDir: t.TempDir(), DataDir: t.TempDir()}
	path := filepath.Join(opts.DataDir, "mcp.json")
	corrupt := []byte(`{"mcpServers": {"keep": {"transport": "stdio", "command": "node"}}`)
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatalf("写入损坏配置失败：%v", err)
	}

	if err := DeleteMCPServerInScope(opts, "global", "keep"); err == nil {
		t.Fatal("配置损坏时按作用域删除应返回错误")
	}
	if err := DeleteMCPServer(opts, "keep"); err == nil {
		t.Fatal("配置损坏时 DeleteMCPServer 应返回错误")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(corrupt) {
		t.Fatalf("删除失败时不得改写原文件：content=%q err=%v", got, err)
	}
}

// 编辑改名时敏感字段回填为空，凭据也要迁到新名下，不能随旧记录一起被删掉。
func TestRenameMCPServerKeepsSecrets(t *testing.T) {
	store := useTestSecretStore(t)
	opts := Options{Home: t.TempDir(), ProjectDir: t.TempDir(), DataDir: t.TempDir()}

	original := MCPServer{
		Name:      "old-srv",
		Scope:     "global",
		Transport: "http",
		URL:       "https://mcp.example.com/mcp",
		Env:       map[string]string{"API_TOKEN": "token-1"},
		Headers:   map[string]string{"Authorization": "Bearer token-1"},
	}
	if err := SaveMCPServer(opts, original); err != nil {
		t.Fatalf("SaveMCPServer 失败：%v", err)
	}

	// 改名并提交空凭据：新记录上要么缺键，要么是空值，凭据库里没有新名下的条目。
	renamed := original
	renamed.Name = "new-srv"
	renamed.Env = map[string]string{"API_TOKEN": ""}
	renamed.Headers = map[string]string{"Authorization": ""}
	if err := SaveMCPServer(opts, renamed); err != nil {
		t.Fatalf("保存新名记录失败：%v", err)
	}
	if err := RenameMCPServer(opts, "global", "old-srv", "new-srv"); err != nil {
		t.Fatalf("RenameMCPServer 失败：%v", err)
	}

	cfg, err := loadCustomMCPConfig(filepath.Join(opts.DataDir, "mcp.json"))
	if err != nil {
		t.Fatalf("读取配置失败：%v", err)
	}
	if _, ok := cfg.MCPServers["old-srv"]; ok {
		t.Fatal("旧名记录应被删除")
	}
	rec, ok := cfg.MCPServers["new-srv"]
	if !ok {
		t.Fatal("新名记录应保留")
	}
	if len(rec.SecretEnvKeys) != 1 || rec.SecretEnvKeys[0] != "API_TOKEN" {
		t.Fatalf("新记录应保留环境变量凭据键：%v", rec.SecretEnvKeys)
	}
	if len(rec.SecretHeaderKeys) != 1 || rec.SecretHeaderKeys[0] != "Authorization" {
		t.Fatalf("新记录应保留请求头凭据键：%v", rec.SecretHeaderKeys)
	}
	if v, err := store.Get("mcp.new-srv.API_TOKEN"); err != nil || v != "token-1" {
		t.Fatalf("环境变量凭据应迁到新名：value=%q err=%v", v, err)
	}
	if v, err := store.Get("mcp.new-srv.header.Authorization"); err != nil || v != "Bearer token-1" {
		t.Fatalf("请求头凭据应迁到新名：value=%q err=%v", v, err)
	}
	if _, err := store.Get("mcp.old-srv.API_TOKEN"); err == nil {
		t.Fatal("旧名凭据应清除")
	}

	// 带凭据扫描能取到迁移后的值。
	credOpts := opts
	credOpts.WithCredentials = true
	srv := mcpServerByName(t, mustScanMCP(t, credOpts), "new-srv")
	if srv.Env["API_TOKEN"] != "token-1" || srv.Headers["Authorization"] != "Bearer token-1" {
		t.Fatalf("改名后凭据应可用：env=%v headers=%v", srv.Env, srv.Headers)
	}
}

// 启用/停用写盘失败必须返回错误，不能吞掉。
func TestSetMCPServerEnabledReportsWriteError(t *testing.T) {
	useTestSecretStore(t)
	dataDir := t.TempDir()
	opts := Options{Home: t.TempDir(), ProjectDir: t.TempDir(), DataDir: dataDir}
	if err := SaveMCPServer(opts, MCPServer{Name: "keep", Scope: "global", Transport: "stdio", Command: "node"}); err != nil {
		t.Fatalf("SaveMCPServer 失败：%v", err)
	}
	// 目录设为只读：配置照样能读，原子写入的临时文件却建不出来。
	mcpPath := filepath.Join(dataDir, "mcp.json")
	if runtime.GOOS == "windows" {
		if err := os.Chmod(mcpPath, 0444); err != nil {
			t.Skip("cannot mark MCP config read-only on Windows")
		}
		t.Cleanup(func() { _ = os.Chmod(mcpPath, 0644) })
	} else {
		if err := os.Chmod(dataDir, 0500); err != nil {
			t.Fatalf("修改目录权限失败：%v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(dataDir, 0700) })
	}

	if err := SetMCPServerEnabled(opts, "keep", false); err == nil {
		t.Fatal("写盘失败时 SetMCPServerEnabled 应返回错误")
	}
}
