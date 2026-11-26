package projctx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWorkspaceSnapshot_StackDetection(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"go.mod", "package.json", "pnpm-lock.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := WorkspaceSnapshot(context.Background(), dir)
	for _, want := range []string{"<workspace_snapshot>", "</workspace_snapshot>", "root: " + dir, "Go", "Node", "pnpm"} {
		if !strings.Contains(got, want) {
			t.Fatalf("输出缺少 %q：\n%s", want, got)
		}
	}
}

func TestWorkspaceSnapshot_GitStatus(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("本机未安装 git")
	}

	dir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Tester",
			"GIT_AUTHOR_EMAIL=tester@example.com",
			"GIT_COMMITTER_NAME=Tester",
			"GIT_COMMITTER_EMAIL=tester@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runGit("init")
	target := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(target, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "tracked.txt")
	runGit("commit", "-m", "init")
	if err := os.WriteFile(target, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := WorkspaceSnapshot(context.Background(), dir)
	if !strings.Contains(got, "git: branch ") {
		t.Fatalf("输出缺少 git branch 行：\n%s", got)
	}
	if !strings.Contains(got, "1 changed") {
		t.Fatalf("输出缺少 1 changed：\n%s", got)
	}
	if !strings.Contains(got, "tracked.txt") {
		t.Fatalf("输出缺少改动文件 tracked.txt：\n%s", got)
	}
}

func TestWorkspaceSnapshot_TopLevelLimit(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i <= 60; i++ {
		name := fmt.Sprintf("item-%02d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := WorkspaceSnapshot(context.Background(), dir)
	if !strings.Contains(got, "+20") {
		t.Fatalf("top-level 应带 +20 提示：\n%s", got)
	}
	var topLine string
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "top-level: ") {
			topLine = line
			break
		}
	}
	if topLine == "" {
		t.Fatalf("缺少 top-level 行：\n%s", got)
	}
	// 40 项加末尾 ", … (+20)" 共 41 个逗号分隔段。
	parts := strings.Split(strings.TrimPrefix(topLine, "top-level: "), ", ")
	if len(parts) != 41 {
		t.Fatalf("top-level 项数 = %d（含省略后缀），want 41：\n%s", len(parts), topLine)
	}
}

func TestWorkspaceSnapshot_MaxBytesAndUTF8Boundary(t *testing.T) {
	dir := t.TempDir()
	// 写入足够多的长文件名（含多字节 UTF-8 字符），使未截断时超过 1500 字节。
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("%02d-%s.txt", i, strings.Repeat("长名称", 25))
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := WorkspaceSnapshot(context.Background(), dir)
	if len(got) > workspaceSnapshotMaxBytes {
		t.Fatalf("输出字节数 = %d，超过上限 %d", len(got), workspaceSnapshotMaxBytes)
	}
	if !strings.HasSuffix(got, "</workspace_snapshot>") {
		t.Fatalf("输出必须以 </workspace_snapshot> 结尾：\n%s", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("截断后必须保持合法 UTF-8")
	}

	// 单行本身超过上限时也必须按 UTF-8 rune 边界截断并保留闭合标签。
	longLine := "root: /" + strings.Repeat("中文", 800)
	truncated := formatWorkspaceSnapshot([]string{longLine}, workspaceSnapshotMaxBytes)
	if len(truncated) > workspaceSnapshotMaxBytes {
		t.Fatalf("单行截断后字节数 = %d，超过上限 %d", len(truncated), workspaceSnapshotMaxBytes)
	}
	if !strings.HasSuffix(truncated, "</workspace_snapshot>") {
		t.Fatalf("单行截断后必须以 </workspace_snapshot> 结尾：\n%s", truncated)
	}
	if !utf8.ValidString(truncated) {
		t.Fatalf("单行截断后必须保持合法 UTF-8")
	}
}

func TestWorkspaceSnapshot_MissingDir(t *testing.T) {
	if got := WorkspaceSnapshot(context.Background(), ""); got != "" {
		t.Fatalf("空目录路径应返回空串，got %q", got)
	}
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if got := WorkspaceSnapshot(context.Background(), missing); got != "" {
		t.Fatalf("不存在的目录应返回空串，got %q", got)
	}
}

func TestWorkspaceSnapshot_CanceledContext(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := WorkspaceSnapshot(ctx, dir)
	if !strings.Contains(got, "root: "+dir) {
		t.Fatalf("已取消的 ctx 仍应返回 root 行，got %q", got)
	}
	if !strings.HasSuffix(got, "</workspace_snapshot>") {
		t.Fatalf("已取消的 ctx 输出应包含闭合标签，got %q", got)
	}
}
