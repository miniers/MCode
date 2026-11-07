package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureSharedLayout(t *testing.T) {
	// 1. 正常建立符号链接：主目录存在部分条目与私有文件
	hostDir := t.TempDir()
	configDir := filepath.Join(t.TempDir(), "account_config")

	// 在 hostDir 中创建需要共享的条目
	if err := os.WriteFile(filepath.Join(hostDir, "CLAUDE.md"), []byte("# Instructions"), 0o600); err != nil {
		t.Fatalf("写入 hostDir CLAUDE.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hostDir, "settings.json"), []byte(`{"env":{}}`), 0o600); err != nil {
		t.Fatalf("写入 hostDir settings.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(hostDir, "skills"), 0o700); err != nil {
		t.Fatalf("创建 hostDir skills: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(hostDir, "plugins"), 0o700); err != nil {
		t.Fatalf("创建 hostDir plugins: %v", err)
	}
	// 在 hostDir 中创建私有文件 .claude.json 与 .credentials.json（不应被共享）
	if err := os.WriteFile(filepath.Join(hostDir, ".claude.json"), []byte(`{"oauthAccount":{}}`), 0o600); err != nil {
		t.Fatalf("写入 hostDir .claude.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hostDir, ".credentials.json"), []byte(`{"tokens":{}}`), 0o600); err != nil {
		t.Fatalf("写入 hostDir .credentials.json: %v", err)
	}

	if err := ensureSharedLayout(hostDir, configDir); err != nil {
		t.Fatalf("ensureSharedLayout 失败: %v", err)
	}

	// 断言：存在的共享条目都建成了符号链接并指向 hostDir
	for _, entry := range []string{"CLAUDE.md", "settings.json", "skills", "plugins"} {
		dst := filepath.Join(configDir, entry)
		fi, err := os.Lstat(dst)
		if err != nil {
			t.Fatalf("期望条目 %s 存在，但获取状态失败: %v", entry, err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("期望条目 %s 为符号链接，实际 mode: %v", entry, fi.Mode())
		}
		target, err := os.Readlink(dst)
		if err != nil {
			t.Fatalf("读取符号链接 %s 失败: %v", dst, err)
		}
		expectedTarget := filepath.Join(hostDir, entry)
		if target != expectedTarget {
			t.Fatalf("条目 %s 符号链接目标不匹配: 期望 %s, 得到 %s", entry, expectedTarget, target)
		}
	}

	// 断言：私有凭据文件未被链接
	for _, privateFile := range []string{".claude.json", ".credentials.json"} {
		if _, err := os.Lstat(filepath.Join(configDir, privateFile)); err == nil {
			t.Fatalf("私有文件 %s 不应在配置目录下建立符号链接", privateFile)
		}
	}

	// 断言：主目录中不存在的条目（如 agents、commands、hooks、output-styles、projects）被跳过
	for _, entry := range []string{"agents", "commands", "hooks", "output-styles", "projects"} {
		if _, err := os.Lstat(filepath.Join(configDir, entry)); err == nil {
			t.Fatalf("主目录不存在的条目 %s 不应在配置目录中创建", entry)
		}
	}
}

func TestEnsureSharedLayoutDoNotOverwriteExisting(t *testing.T) {
	// 2. 已有真实文件与断链绝不覆盖
	hostDir := t.TempDir()
	configDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(hostDir, "settings.json"), []byte(`{"host":true}`), 0o600); err != nil {
		t.Fatalf("写入 hostDir settings.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(hostDir, "skills"), 0o700); err != nil {
		t.Fatalf("创建 hostDir skills: %v", err)
	}

	// 在 configDir 中预先写入定制的 settings.json 真实文件
	customContent := []byte(`{"custom":true}`)
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), customContent, 0o600); err != nil {
		t.Fatalf("写入 configDir settings.json: %v", err)
	}

	// 在 configDir 中预先创建一个断链 skills
	brokenTarget := filepath.Join(t.TempDir(), "nonexistent")
	if err := os.Symlink(brokenTarget, filepath.Join(configDir, "skills")); err != nil {
		t.Fatalf("创建断链 skills: %v", err)
	}

	if err := ensureSharedLayout(hostDir, configDir); err != nil {
		t.Fatalf("ensureSharedLayout 失败: %v", err)
	}

	// 断言：已有的 settings.json 真实文件未被覆盖
	readContent, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if err != nil {
		t.Fatalf("读取 configDir settings.json 失败: %v", err)
	}
	if string(readContent) != string(customContent) {
		t.Fatalf("已有的真实文件被覆盖: 期望 %q, 得到 %q", string(customContent), string(readContent))
	}

	// 断言：已有的断链 skills 未被篡改
	target, err := os.Readlink(filepath.Join(configDir, "skills"))
	if err != nil {
		t.Fatalf("读取断链 skills 失败: %v", err)
	}
	if target != brokenTarget {
		t.Fatalf("断链被篡改: 期望指向 %s, 得到 %s", brokenTarget, target)
	}
}

func TestEnsureSharedLayoutSameDirOrEmpty(t *testing.T) {
	// 3. configDir 为空或与 hostDir 相同时什么都不做
	hostDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostDir, "CLAUDE.md"), []byte("doc"), 0o600); err != nil {
		t.Fatalf("写入 CLAUDE.md: %v", err)
	}

	if err := ensureSharedLayout(hostDir, ""); err != nil {
		t.Fatalf("configDir 为空时应返回 nil: %v", err)
	}

	if err := ensureSharedLayout(hostDir, hostDir); err != nil {
		t.Fatalf("configDir 等于 hostDir 时应返回 nil: %v", err)
	}

	fi, err := os.Lstat(filepath.Join(hostDir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("获取 CLAUDE.md 状态失败: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("configDir 等于 hostDir 时不应被转为符号链接")
	}
}

func TestEnsureSharedLayoutFromEnv(t *testing.T) {
	// 4. 从环境变量提取 CLAUDE_CONFIG_DIR
	if err := EnsureSharedLayoutFromEnv([]string{"PATH=/usr/bin"}); err != nil {
		t.Fatalf("未包含 CLAUDE_CONFIG_DIR 时应返回 nil: %v", err)
	}
	if err := EnsureSharedLayoutFromEnv([]string{"CLAUDE_CONFIG_DIR="}); err != nil {
		t.Fatalf("CLAUDE_CONFIG_DIR 为空时应返回 nil: %v", err)
	}
}
