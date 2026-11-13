package claude

import (
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/testenv"
)

// writeClaudeConfig 在 dir 下写入一个 .claude.json fixture，测试不读取真实登录。
func writeClaudeConfig(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(content), 0o600); err != nil {
		t.Fatalf("写 .claude.json: %v", err)
	}
}

func TestReadAccount(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	writeClaudeConfig(t, home, `{"oauthAccount":{"emailAddress":"a@b.c","organizationType":"claude_max"},"mcpServers":{}}`)

	email, plan, err := ReadAccount("")
	if err != nil {
		t.Fatalf("ReadAccount: %v", err)
	}
	if email != "a@b.c" || plan != "Max" {
		t.Fatalf("期望 a@b.c / Max，得到 %q / %q", email, plan)
	}
}

// home 参数优先于当前用户主目录：多账号 home 场景直接读该目录下的 .claude.json。
func TestReadAccountExplicitHome(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	other := t.TempDir()
	writeClaudeConfig(t, other, `{"oauthAccount":{"emailAddress":"other@example.com","organizationType":"claude_pro"}}`)

	email, plan, err := ReadAccount(other)
	if err != nil {
		t.Fatalf("ReadAccount: %v", err)
	}
	if email != "other@example.com" || plan != "Pro" {
		t.Fatalf("期望 other@example.com / Pro，得到 %q / %q", email, plan)
	}
}

// 配置目录被搬迁时（CLAUDE_CONFIG_DIR），.claude.json 位于该目录下而不是用户主目录。
func TestReadAccountRelocatedConfigDir(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	writeClaudeConfig(t, home, `{"oauthAccount":{"emailAddress":"default@example.com"}}`)
	relocated := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", relocated)
	writeClaudeConfig(t, relocated, `{"oauthAccount":{"emailAddress":"relocated@example.com","organizationType":"claude_team"}}`)

	email, plan, err := ReadAccount("")
	if err != nil {
		t.Fatalf("ReadAccount: %v", err)
	}
	if email != "relocated@example.com" || plan != "Team" {
		t.Fatalf("期望 relocated@example.com / Team，得到 %q / %q", email, plan)
	}
}

func TestReadAccountErrors(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	if _, _, err := ReadAccount(""); err == nil {
		t.Fatal("文件缺失应当报错")
	}

	writeClaudeConfig(t, home, `{`)
	if _, _, err := ReadAccount(""); err == nil {
		t.Fatal("内容非法应当报错")
	}

	// 文件存在但没有 oauthAccount（未登录）时同样报错。
	writeClaudeConfig(t, home, `{"mcpServers":{}}`)
	if _, _, err := ReadAccount(""); err == nil {
		t.Fatal("没有 oauthAccount 应当报错")
	}

	writeClaudeConfig(t, home, `{"oauthAccount":{"organizationType":"claude_max"}}`)
	if _, _, err := ReadAccount(""); err == nil {
		t.Fatal("没有邮箱应当报错")
	}
}
