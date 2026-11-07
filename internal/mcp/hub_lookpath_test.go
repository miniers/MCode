package mcp

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeFakeExec 在 dir 下写一个名为 name 的假可执行文件，perm 用于区分可执行与否。
func writeFakeExec(t *testing.T, dir, name string, perm os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), perm); err != nil {
		t.Fatalf("写入 %s 失败: %v", p, err)
	}
	return p
}

// TestResolveCommandInEnv 验证按 env 里的 PATH 解析 stdio 命令路径。
func TestResolveCommandInEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上不校验权限位，跳过")
	}
	dir := t.TempDir()
	want := writeFakeExec(t, dir, "fakenode", 0o755)

	t.Run("在 PATH 中找到返回完整路径", func(t *testing.T) {
		got := resolveCommandInEnv("fakenode", []string{"PATH=/nonexistent:" + dir})
		if got != want {
			t.Fatalf("期望 %q，实际 %q", want, got)
		}
	})

	t.Run("PATH 里找不到返回原名", func(t *testing.T) {
		got := resolveCommandInEnv("fakenode", []string{"PATH=" + t.TempDir()})
		if got != "fakenode" {
			t.Fatalf("期望原样返回 %q，实际 %q", "fakenode", got)
		}
	})

	t.Run("没有 PATH 时返回原名", func(t *testing.T) {
		if got := resolveCommandInEnv("fakenode", []string{"HOME=/tmp"}); got != "fakenode" {
			t.Fatalf("期望原样返回 %q，实际 %q", "fakenode", got)
		}
	})

	t.Run("含路径分隔符的命令原样返回", func(t *testing.T) {
		if got := resolveCommandInEnv(want, []string{"PATH=" + dir}); got != want {
			t.Fatalf("期望原样返回 %q，实际 %q", want, got)
		}
	})

	t.Run("多个 PATH 以最后一个为准", func(t *testing.T) {
		// 最后一个 PATH 里没有可执行文件，命令应解析失败并返回原名。
		env := []string{"PATH=" + dir, "PATH=" + t.TempDir()}
		if got := resolveCommandInEnv("fakenode", env); got != "fakenode" {
			t.Fatalf("期望原样返回 %q，实际 %q", "fakenode", got)
		}
		// 反过来，最后一个 PATH 命中时才返回完整路径。
		env = []string{"PATH=" + t.TempDir(), "PATH=" + dir}
		if got := resolveCommandInEnv("fakenode", env); got != want {
			t.Fatalf("期望 %q，实际 %q", want, got)
		}
	})

	t.Run("非可执行文件不命中", func(t *testing.T) {
		dir2 := t.TempDir()
		writeFakeExec(t, dir2, "notnode", 0o644)
		if got := resolveCommandInEnv("notnode", []string{"PATH=" + dir2}); got != "notnode" {
			t.Fatalf("期望原样返回 %q，实际 %q", "notnode", got)
		}
	})

	t.Run("空目录项不当作当前目录", func(t *testing.T) {
		if got := resolveCommandInEnv("fakenode", []string{"PATH=:" + t.TempDir()}); got != "fakenode" {
			t.Fatalf("期望原样返回 %q，实际 %q", "fakenode", got)
		}
	})
}
