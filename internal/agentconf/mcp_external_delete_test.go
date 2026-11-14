package agentconf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func externalMCPTestOptions(t *testing.T) Options {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return Options{Home: home, ProjectDir: filepath.Join(home, "project"), DataDir: filepath.Join(home, "data")}
}

func writeExternalMCPTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0640); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteExternalMCPServerJSON(t *testing.T) {
	for _, kind := range []string{"global", "project", "mcpjson", "project-realpath"} {
		t.Run(kind, func(t *testing.T) {
			opts := externalMCPTestOptions(t)
			path := filepath.Join(opts.Home, ".claude.json")
			scope := "global"
			before := `{"z": 1, "mcpServers": {"drop": {"command":"echo"}, "keep": {"command":"keep"}}, "a": [1, 2]}` + "\n"
			after := `{"z": 1, "mcpServers": { "keep": {"command":"keep"}}, "a": [1, 2]}` + "\n"
			if strings.HasPrefix(kind, "project") {
				scope = "project"
				key := opts.ProjectDir
				if kind == "project-realpath" {
					real := filepath.Join(opts.Home, "real")
					if err := os.MkdirAll(real, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(real, opts.ProjectDir); err != nil {
						t.Fatal(err)
					}
					key, _ = filepath.EvalSymlinks(opts.ProjectDir)
				}
				quotedKey, err := json.Marshal(key)
				if err != nil {
					t.Fatal(err)
				}
				before = `{"z":1,"projects":{` + string(quotedKey) + `:{"mcpServers":{"keep":{"command":"keep"},"drop":{"command":"echo"}},"other":true}},"mcpServers":{"global":{"command":"echo"}},"a":2}`
				after = strings.Replace(before, `,"drop":{"command":"echo"}`, "", 1)
			}
			if kind == "mcpjson" {
				path = filepath.Join(opts.ProjectDir, ".mcp.json")
				scope = "project"
			}
			writeExternalMCPTestFile(t, path, before)
			origInfo, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			origPerm := origInfo.Mode().Perm()
			srv := MCPServer{Agent: "claude", Scope: scope, Name: "drop", Source: path}
			if !CanDeleteExternalMCPServer(opts, srv) {
				t.Fatal("合法来源应可删除")
			}
			if err := DeleteExternalMCPServer(opts, srv); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != after {
				t.Fatalf("其它内容改变：\ngot %s\nwant %s", got, after)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != origPerm {
				t.Fatalf("权限改变：%v，原权限：%v", info.Mode(), origPerm)
			}
			if err := DeleteExternalMCPServer(opts, srv); err == nil {
				t.Fatal("重复删除应报不存在")
			}
		})
	}
}

func TestDeleteExternalMCPServerTOML(t *testing.T) {
	for _, scope := range []string{"global", "project"} {
		t.Run(scope, func(t *testing.T) {
			opts := externalMCPTestOptions(t)
			root := opts.Home
			if scope == "project" {
				root = opts.ProjectDir
			}
			path := filepath.Join(root, ".codex", "config.toml")
			before := "# 顶部注释\nmodel = 'gpt'\n\n[mcp_servers.'drop.name'] # 目标\ncommand = 'echo'\nargs = [\n 'one',\n 'two',\n]\n# 保留注释\n[mcp_servers.\"drop.name\".env]\nTOKEN = 'value'\n\n# 下一个服务\n[mcp_servers.keep]\ncommand = 'keep'\n[mcp_servers.keep.env]\nZ = 'last'\n"
			after := "# 顶部注释\nmodel = 'gpt'\n\n# 保留注释\n\n# 下一个服务\n[mcp_servers.keep]\ncommand = 'keep'\n[mcp_servers.keep.env]\nZ = 'last'\n"
			writeExternalMCPTestFile(t, path, before)
			origInfo, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			origPerm := origInfo.Mode().Perm()
			srv := MCPServer{Agent: "codex", Scope: scope, Name: "drop.name", Source: path}
			if err := DeleteExternalMCPServer(opts, srv); err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(path)
			if string(got) != after {
				t.Fatalf("其它内容改变：\ngot %s\nwant %s", got, after)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != origPerm {
				t.Fatalf("权限改变：%v，原权限：%v", info.Mode(), origPerm)
			}
		})
	}
}

func TestDeleteExternalMCPServerRejects(t *testing.T) {
	opts := externalMCPTestOptions(t)
	for _, srv := range []MCPServer{
		{Agent: "claude", Scope: "global", Name: "drop", Source: filepath.Join(opts.Home, ".claude", "plugins", ".mcp.json")},
		{Agent: "claude", Scope: "global", Name: "drop", Source: filepath.Join(opts.Home, ".claude.json"), ReadOnly: true},
		{Agent: "pi", Scope: "global", Name: "drop", Source: filepath.Join(opts.Home, ".claude.json")},
		{Agent: "claude", Scope: "external", Name: "drop", Source: filepath.Join(opts.Home, ".claude.json")},
	} {
		const before = `{"mcpServers":{"drop":{"command":"echo"}}}`
		writeExternalMCPTestFile(t, srv.Source, before)
		if CanDeleteExternalMCPServer(opts, srv) {
			t.Fatalf("应拒绝来源：%+v", srv)
		}
		if err := DeleteExternalMCPServer(opts, srv); err == nil {
			t.Fatal("应拒绝删除")
		}
		got, _ := os.ReadFile(srv.Source)
		if string(got) != before {
			t.Fatal("拒绝后文件改变")
		}
	}
}

func TestDeleteExternalMCPServerTOMLRejectsWithoutWriting(t *testing.T) {
	opts := externalMCPTestOptions(t)
	path := filepath.Join(opts.Home, ".codex", "config.toml")
	for _, before := range []string{
		"[mcp_servers.drop]\ncommand='echo'\n[other]\nbroken = [\n",
		"mcp_servers = { drop = {command='echo'}, keep = {command='keep'} }\n",
	} {
		writeExternalMCPTestFile(t, path, before)
		srv := MCPServer{Agent: "codex", Scope: "global", Name: "drop", Source: path}
		// 原文不合法或内联表无法按表删除时，必须保留原文件。
		if err := DeleteExternalMCPServer(opts, srv); err == nil {
			t.Fatalf("应拒绝：%s", before)
		}
		got, _ := os.ReadFile(path)
		if string(got) != before {
			t.Fatal("校验失败后文件改变")
		}
	}
}

func TestDeleteExternalMCPServerCleansState(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "unique", true: "shared"}[shared], func(t *testing.T) {
			opts := externalMCPTestOptions(t)
			path := filepath.Join(opts.Home, ".claude.json")
			writeExternalMCPTestFile(t, path, `{"mcpServers":{"drop":{"command":"echo"}}}`)
			cfg := customMCPConfigFile{DisabledServers: []string{"drop", "keep"}, NewThreadDisabled: []string{"claude:global:drop", "claude:global:keep"}}
			if err := saveCustomMCPConfig(getMCPConfigPath(opts, "global"), cfg); err != nil {
				t.Fatal(err)
			}
			policyPath := MCPToolPolicyPath(opts.DataDir)
			if err := SaveMCPToolPolicy(policyPath, MCPToolPolicy{Disabled: map[string]map[string]bool{"drop": {"tool": true}, "keep": {"tool": true}}, AutoApprove: map[string]map[string]bool{"drop": {"tool": true}}}); err != nil {
				t.Fatal(err)
			}
			if shared {
				writeExternalMCPTestFile(t, filepath.Join(opts.Home, ".codex", "config.toml"), "[mcp_servers.drop]\ncommand='echo'\n")
			}
			if err := DeleteExternalMCPServer(opts, MCPServer{Agent: "claude", Scope: "global", Name: "drop", Source: path}); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadCustomMCPConfig(getMCPConfigPath(opts, "global"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.Join(cfg.NewThreadDisabled, ","), "claude:global:drop") {
				t.Fatal("服务键未清理")
			}
			if strings.Contains(strings.Join(cfg.DisabledServers, ","), "drop") != shared {
				t.Fatal("同名停用状态错误")
			}
			policy, err := LoadMCPToolPolicy(policyPath)
			if err != nil {
				t.Fatal(err)
			}
			if policy.IsDisabled("drop", "tool") != shared || policy.IsAutoApproved("drop", "tool") != shared || !policy.IsDisabled("keep", "tool") {
				t.Fatal("工具策略清理错误")
			}
		})
	}
}

func TestDeleteExternalMCPJSONEscapedName(t *testing.T) {
	before := []byte(`{"mcpServers":{"a.\"b\\c":{"command":"echo"}},"keep":1}`)
	got, err := deleteExternalMCPJSON(before, []string{"mcpServers", "a.\"b\\c"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"mcpServers":{},"keep":1}` {
		t.Fatalf("删除结果错误：%s", got)
	}
}

func TestDeleteExternalMCPTOMLMultilineString(t *testing.T) {
	before := "[mcp_servers.keep]\ncommand = '''\n[mcp_servers.drop]\n'''\n[mcp_servers.drop]\ncommand='echo'\n[mcp_servers.keep2]\ncommand='keep'"
	want := "[mcp_servers.keep]\ncommand = '''\n[mcp_servers.drop]\n'''\n[mcp_servers.keep2]\ncommand='keep'"
	got, err := deleteExternalMCPTOML([]byte(before), "drop")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("误删字符串内的表头：%s", got)
	}
}
