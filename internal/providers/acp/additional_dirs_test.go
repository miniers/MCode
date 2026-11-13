package acp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/testenv"
)

// TestResolveWorkDirPathWithAdditionalDirs 覆盖附加目录白名单：附加目录内的绝对路径放行且 rel
// 相对命中的那个目录；相对路径仍只相对 workDir 解析；白名单之外与 ".." 越界一律拒绝。
func TestResolveWorkDirPathWithAdditionalDirs(t *testing.T) {
	workDir := t.TempDir()
	extraDir := t.TempDir()
	outside := t.TempDir()

	t.Run("附加目录内的绝对路径放行", func(t *testing.T) {
		nested := filepath.Join(extraDir, "pkg")
		if err := os.MkdirAll(nested, 0755); err != nil {
			t.Fatalf("mkdir nested: %v", err)
		}
		target, rel, err := resolveWorkDirPath(workDir, filepath.Join(extraDir, "pkg", "file.txt"), extraDir)
		if err != nil {
			t.Fatalf("附加目录内路径被拒绝: %v", err)
		}
		if want := filepath.Join("pkg", "file.txt"); rel != want {
			t.Fatalf("rel = %q, want %q", rel, want)
		}
		if !strings.HasSuffix(target, filepath.Join("pkg", "file.txt")) {
			t.Fatalf("abs = %q, want suffix %q", target, filepath.Join("pkg", "file.txt"))
		}
	})

	t.Run("相对路径仍按 workDir 解析", func(t *testing.T) {
		_, rel, err := resolveWorkDirPath(workDir, "sub/file.txt", extraDir)
		if err != nil {
			t.Fatalf("workDir 内相对路径被拒绝: %v", err)
		}
		if want := filepath.Join("sub", "file.txt"); rel != want {
			t.Fatalf("rel = %q, want %q", rel, want)
		}
	})

	t.Run("附加目录也接收重复项", func(t *testing.T) {
		if _, _, err := resolveWorkDirPath(workDir, filepath.Join(extraDir, "a.txt"), extraDir, extraDir); err != nil {
			t.Fatalf("重复附加目录不应报错: %v", err)
		}
	})

	t.Run("白名单之外的目录仍被拒绝", func(t *testing.T) {
		_, _, err := resolveWorkDirPath(workDir, filepath.Join(outside, "secret.txt"), extraDir)
		if err == nil {
			t.Fatal("附加目录之外的路径应被拒绝")
		}
		if !strings.Contains(err.Error(), "access denied: path outside workdir") {
			t.Fatalf("error = %v, want access denied", err)
		}
	})

	t.Run("相对路径..越界仍被拒绝", func(t *testing.T) {
		_, _, err := resolveWorkDirPath(workDir, "../secret.txt", extraDir)
		if err == nil {
			t.Fatal("../ 越界应被拒绝")
		}
		if !strings.Contains(err.Error(), "access denied: path outside workdir") {
			t.Fatalf("error = %v, want access denied", err)
		}
	})

	t.Run("符号链接导出附加目录被拒绝", func(t *testing.T) {
		if err := os.Symlink(outside, filepath.Join(extraDir, "escape")); err != nil {
			t.Fatalf("create symlink: %v", err)
		}
		if _, _, err := resolveWorkDirPath(workDir, filepath.Join(extraDir, "escape", "secret.txt"), extraDir); err == nil {
			t.Fatal("经符号链接导出附加目录的路径应被拒绝")
		}
	})
}

func TestReadOutsideAndProtectCredentials(t *testing.T) {
	workDir := t.TempDir()
	home := t.TempDir()
	testenv.SetHome(t, home)
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	target, err := resolveReadPath(workDir, file)
	if err != nil {
		t.Fatalf("工作目录外读取被拒绝: %v", err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "content" {
		t.Fatalf("读取内容 = %q, error = %v", data, err)
	}
	if _, _, err := resolveWorkDirPath(workDir, file); err == nil {
		t.Fatal("工作目录外写入应被拒绝")
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "content" {
		t.Fatalf("文件内容 = %q, error = %v", data, err)
	}
	secret := filepath.Join(home, ".ssh", "id_test")
	if err := os.MkdirAll(filepath.Dir(secret), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workDir, "ssh-link")
	if err := os.Symlink(filepath.Dir(secret), link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{secret, filepath.Join(link, "id_test")} {
		if _, err := resolveReadPath(workDir, path); err == nil || !strings.Contains(err.Error(), "protected credential path:") {
			t.Fatalf("读取凭据 %q: %v", path, err)
		}
	}
}
