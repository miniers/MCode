package codex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureSharedLayout(t *testing.T) {
	// 1. 正常建立符号链接：主目录存在部分条目与私有文件
	hostDir := t.TempDir()
	home := filepath.Join(t.TempDir(), "account_home")

	// 在 hostDir 中创建需要共享的条目
	if err := os.WriteFile(filepath.Join(hostDir, "config.toml"), []byte("model = 'o3'"), 0o600); err != nil {
		t.Fatalf("写入 hostDir config.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hostDir, "AGENTS.md"), []byte("# Agents"), 0o600); err != nil {
		t.Fatalf("写入 hostDir AGENTS.md: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(hostDir, "skills"), 0o700); err != nil {
		t.Fatalf("创建 hostDir skills: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(hostDir, "sessions"), 0o700); err != nil {
		t.Fatalf("创建 hostDir sessions: %v", err)
	}
	// 在 hostDir 中创建私有文件 auth.json（不应被共享）
	if err := os.WriteFile(filepath.Join(hostDir, "auth.json"), []byte(`{"tokens":{}}`), 0o600); err != nil {
		t.Fatalf("写入 hostDir auth.json: %v", err)
	}

	if err := ensureSharedLayout(hostDir, home); err != nil {
		t.Fatalf("ensureSharedLayout 失败: %v", err)
	}

	// 断言：存在的共享条目都建成了符号链接并指向 hostDir
	for _, entry := range []string{"config.toml", "AGENTS.md", "skills", "sessions"} {
		dst := filepath.Join(home, entry)
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

	// 断言：私有文件 auth.json 未被链接
	if _, err := os.Lstat(filepath.Join(home, "auth.json")); err == nil {
		t.Fatal("auth.json 为私有凭据文件，不应在账号目录下建立符号链接")
	}

	// 断言：主目录中不存在的条目（如 rules、prompts、archived_sessions）在 home 中被跳过
	for _, entry := range []string{"rules", "prompts", "archived_sessions"} {
		if _, err := os.Lstat(filepath.Join(home, entry)); err == nil {
			t.Fatalf("主目录不存在的条目 %s 不应在账号目录中创建", entry)
		}
	}
}

func TestEnsureSharedLayoutDoNotOverwriteExisting(t *testing.T) {
	// 2. 已有真实文件与断链绝不覆盖
	hostDir := t.TempDir()
	home := t.TempDir()

	if err := os.WriteFile(filepath.Join(hostDir, "config.toml"), []byte("host config"), 0o600); err != nil {
		t.Fatalf("写入 hostDir config.toml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(hostDir, "skills"), 0o700); err != nil {
		t.Fatalf("创建 hostDir skills: %v", err)
	}

	// 在 home 中预先写入不同的 config.toml 真实文件
	customContent := []byte("account custom config")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), customContent, 0o600); err != nil {
		t.Fatalf("写入 home config.toml: %v", err)
	}

	// 在 home 中预先创建一个断链 skills（指向一个不存在的路径）
	brokenTarget := filepath.Join(t.TempDir(), "nonexistent")
	if err := os.Symlink(brokenTarget, filepath.Join(home, "skills")); err != nil {
		t.Fatalf("创建断链 skills: %v", err)
	}

	if err := ensureSharedLayout(hostDir, home); err != nil {
		t.Fatalf("ensureSharedLayout 失败: %v", err)
	}

	// 断言：已有的 config.toml 真实文件未被覆盖
	readContent, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatalf("读取 home config.toml 失败: %v", err)
	}
	if string(readContent) != string(customContent) {
		t.Fatalf("已有的真实文件被覆盖: 期望 %q, 得到 %q", string(customContent), string(readContent))
	}

	// 断言：已有的断链 skills 未被修改
	target, err := os.Readlink(filepath.Join(home, "skills"))
	if err != nil {
		t.Fatalf("读取断链 skills 失败: %v", err)
	}
	if target != brokenTarget {
		t.Fatalf("断链被篡改: 期望指向 %s, 得到 %s", brokenTarget, target)
	}
}

func TestEnsureSharedLayoutSameDirOrEmpty(t *testing.T) {
	// 3. home 为空或与 hostDir 相同时什么都不做
	hostDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostDir, "config.toml"), []byte("config"), 0o600); err != nil {
		t.Fatalf("写入 config.toml: %v", err)
	}

	// 空字符串
	if err := ensureSharedLayout(hostDir, ""); err != nil {
		t.Fatalf("home 为空时应返回 nil: %v", err)
	}

	// home 与 hostDir 为同一目录
	if err := ensureSharedLayout(hostDir, hostDir); err != nil {
		t.Fatalf("home 等于 hostDir 时应返回 nil: %v", err)
	}

	// 验证 hostDir 中的 config.toml 没有被做成循环软链接
	fi, err := os.Lstat(filepath.Join(hostDir, "config.toml"))
	if err != nil {
		t.Fatalf("获取 config.toml 状态失败: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("home 等于 hostDir 时不应被转为符号链接")
	}
}
