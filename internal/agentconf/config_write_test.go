package agentconf

import (
	"os"
	"path/filepath"
	"testing"
)

// 配置文件损坏时保存必须报错并保持原文件不变，不能当成空配置把其它服务整体覆盖掉。
func TestSaveMCPServerRejectsCorruptConfig(t *testing.T) {
	useTestSecretStore(t)
	opts := Options{Home: t.TempDir(), ProjectDir: t.TempDir(), DataDir: t.TempDir()}
	path := filepath.Join(opts.DataDir, "mcp.json")
	corrupt := []byte(`{"mcpServers": {"keep": {"transport": "stdio", "command": "node"}}`)
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatalf("写入损坏配置失败：%v", err)
	}

	if err := SaveMCPServer(opts, MCPServer{Name: "new-srv", Scope: "global", Transport: "stdio", Command: "node"}); err == nil {
		t.Fatal("配置损坏时 SaveMCPServer 应返回错误")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(corrupt) {
		t.Fatalf("保存失败时不得改写原文件：content=%q err=%v", got, err)
	}

	if err := SetMCPServerEnabled(opts, "keep", false); err == nil {
		t.Fatal("配置损坏时 SetMCPServerEnabled 应返回错误")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(corrupt) {
		t.Fatalf("停用失败时不得改写原文件：content=%q err=%v", got, err)
	}
}

// 保存走「临时文件 + rename」：成功写盘后不留临时文件，权限保持 0600。
func TestSaveMCPServerWritesAtomically(t *testing.T) {
	useTestSecretStore(t)
	opts := Options{Home: t.TempDir(), ProjectDir: t.TempDir(), DataDir: t.TempDir()}
	if err := SaveMCPServer(opts, MCPServer{Name: "atomic-srv", Scope: "global", Transport: "stdio", Command: "node"}); err != nil {
		t.Fatalf("SaveMCPServer 失败：%v", err)
	}

	entries, err := os.ReadDir(opts.DataDir)
	if err != nil {
		t.Fatalf("读取数据目录失败：%v", err)
	}
	for _, entry := range entries {
		if entry.Name() != "mcp.json" {
			t.Fatalf("数据目录里不应留下临时文件：%s", entry.Name())
		}
	}

	info, err := os.Stat(filepath.Join(opts.DataDir, "mcp.json"))
	if err != nil {
		t.Fatalf("读取配置文件属性失败：%v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("配置文件权限应为 0600，实际 %o", perm)
	}
}

// 技能停用列表损坏时同样不能当成「没有禁用项」写回。
func TestSetSkillEnabledRejectsCorruptConfig(t *testing.T) {
	opts := Options{Home: t.TempDir(), ProjectDir: t.TempDir(), DataDir: t.TempDir()}
	path := filepath.Join(opts.DataDir, "skills.json")
	corrupt := []byte(`{"disabled": ["existing-skill"`)
	if err := os.WriteFile(path, corrupt, 0644); err != nil {
		t.Fatalf("写入损坏配置失败：%v", err)
	}

	if err := SetSkillEnabled(opts, "my-skill", false); err == nil {
		t.Fatal("配置损坏时 SetSkillEnabled 应返回错误")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(corrupt) {
		t.Fatalf("保存失败时不得改写原文件：content=%q err=%v", got, err)
	}
}
