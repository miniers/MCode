package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/testenv"
)

func TestReadOnlyDirs(t *testing.T) {
	workDir := t.TempDir()
	skillDir := t.TempDir()
	outsideDir := t.TempDir()
	skillFile := filepath.Join(skillDir, "skill.txt")
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	put(t, skillFile, "skill content\n")
	put(t, outsideFile, "outside content\n")
	env := Env{WorkDir: workDir, ReadOnlyDirs: []string{skillDir}}

	t.Run("read and search", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			args map[string]any
			want string
		}{
			{"read", map[string]any{"path": skillFile}, "skill content"},
			{"grep", map[string]any{"path": skillDir, "pattern": "skill content"}, "skill content"},
			{"find", map[string]any{"path": skillDir, "pattern": "*.txt"}, "skill.txt"},
			{"ls", map[string]any{"path": skillDir}, "skill.txt"},
		} {
			result := runTool(t, env, tc.name, tc.args)
			if result.IsError || !strings.Contains(result.Text, tc.want) {
				t.Fatalf("%s: %+v", tc.name, result)
			}
		}
	})

	t.Run("write denied", func(t *testing.T) {
		result := runTool(t, env, "edit", map[string]any{
			"path": skillFile, "edits": []textEdit{{"skill content", "changed"}},
		})
		if !result.IsError || !strings.Contains(result.Text, "outside allowed directories") {
			t.Fatalf("result = %+v", result)
		}
		data, err := os.ReadFile(skillFile)
		if err != nil || string(data) != "skill content\n" {
			t.Fatalf("content = %q, err = %v", data, err)
		}
	})

	t.Run("outside read allowed", func(t *testing.T) {
		result := runTool(t, env, "read", map[string]any{"path": outsideFile})
		if result.IsError || !strings.Contains(result.Text, "outside content") {
			t.Fatalf("result = %+v", result)
		}
	})
}

func TestResolveExpandsTilde(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	workDir := t.TempDir()
	put(t, filepath.Join(home, "file.txt"), "home content\n")
	env := Env{WorkDir: workDir, ReadOnlyDirs: []string{home}}
	args := map[string]any{"path": "~/file.txt"}

	result := runTool(t, env, "read", args)
	if result.IsError || !strings.Contains(result.Text, "home content") {
		t.Fatalf("home read: %+v", result)
	}

	withoutReadOnly := env
	withoutReadOnly.ReadOnlyDirs = nil
	result = runTool(t, withoutReadOnly, "read", args)
	if result.IsError || !strings.Contains(result.Text, "home content") {
		t.Fatalf("outside read: %+v", result)
	}

	tildeDir := filepath.Join(workDir, "~")
	if err := os.Mkdir(tildeDir, 0755); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(tildeDir, "file.txt"), "work content\n")
	result = runTool(t, env, "read", args)
	if result.IsError || !strings.Contains(result.Text, "home content") || strings.Contains(result.Text, "work content") {
		t.Fatalf("home read with work directory collision: %+v", result)
	}
}

func TestCredentialPaths(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	workDir := t.TempDir()
	secret := filepath.Join(home, ".ssh", "id_test")
	media := filepath.Join(home, ".mcode", "media", "a.txt")
	for _, dir := range []string{filepath.Dir(secret), filepath.Dir(media)} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	put(t, secret, "needle secret\n")
	put(t, media, "needle attachment\n")
	env := Env{WorkDir: workDir}
	link := filepath.Join(workDir, "ssh-link")
	if err := os.Symlink(filepath.Dir(secret), link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{secret, filepath.Join(link, "id_test")} {
		result := runTool(t, env, "read", map[string]any{"path": path})
		if !result.IsError || !strings.Contains(result.Text, "protected credential path:") {
			t.Fatalf("read %s: %+v", path, result)
		}
	}
	result := runTool(t, env, "read", map[string]any{"path": media})
	if result.IsError || !strings.Contains(result.Text, "needle attachment") {
		t.Fatalf("media read: %+v", result)
	}
	result = runTool(t, env, "grep", map[string]any{"path": home, "pattern": "needle"})
	if result.IsError || !strings.Contains(result.Text, "needle attachment") || strings.Contains(result.Text, "secret") || strings.Contains(result.Text, ".ssh") {
		t.Fatalf("home grep: %+v", result)
	}
	result = runTool(t, env, "find", map[string]any{"path": home, "pattern": "*"})
	if result.IsError || !strings.Contains(result.Text, "a.txt") || strings.Contains(result.Text, "id_test") {
		t.Fatalf("home find: %+v", result)
	}
}

func TestEditOutsideWorkDirDenied(t *testing.T) {
	workDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	put(t, outside, "original\n")
	result := runTool(t, Env{WorkDir: workDir}, "edit", map[string]any{
		"path": outside, "edits": []textEdit{{"original", "changed"}},
	})
	if !result.IsError || !strings.Contains(result.Text, "outside allowed directories") {
		t.Fatalf("edit: %+v", result)
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "original\n" {
		t.Fatalf("content = %q, err = %v", data, err)
	}
}

func TestSecretPathResolvesProtectedSymlink(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	actual := t.TempDir()
	if err := os.Symlink(actual, filepath.Join(home, ".ssh")); err != nil {
		t.Fatal(err)
	}
	if !SecretPath(filepath.Join(actual, "id_test")) {
		t.Fatal("凭据目录指向的路径未被保护")
	}
	if SecretPath(filepath.Join(home, ".ssh-other", "file")) {
		t.Fatal("名称前缀相同的普通目录被拒绝")
	}
}

func TestSecretPathCacheChangesWithHome(t *testing.T) {
	var previous string
	for i := 0; i < 2; i++ {
		home := t.TempDir()
		testenv.SetHome(t, home)
		key := filepath.Join(home, ".ssh", "key")
		if err := os.MkdirAll(filepath.Dir(key), 0755); err != nil {
			t.Fatal(err)
		}
		put(t, key, "key")
		if !SecretPath(key) {
			t.Fatalf("当前用户目录中的凭据未被保护: %s", key)
		}
		if previous != "" && SecretPath(previous) {
			t.Fatalf("上一个用户目录中的路径仍被保护: %s", previous)
		}
		previous = key
	}
}

func BenchmarkSecretPath(b *testing.B) {
	workDir, err := os.Getwd()
	if err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(workDir, "path.go")
	SecretPath(path)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SecretPath(path)
	}
}
