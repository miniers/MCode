package git

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSnapshotRestore(t *testing.T) {
	t.Parallel()
	r, root := setupTestRepo(t)
	ctx := context.Background()
	writeTestFile(t, root, "bin/tool", "原始\x00内容\r\n")
	if err := os.Chmod(filepath.Join(root, "bin/tool"), 0751); err != nil {
		t.Fatal(err)
	}
	s, err := r.SnapshotPaths(ctx, root, []string{"bin/tool", "absent.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Files) != 2 || !s.Files[0].Existed || s.Files[1].Existed {
		t.Fatalf("快照错误: %+v", s)
	}
	writeTestFile(t, root, "bin/tool", "改变\n")
	if err := os.Chmod(filepath.Join(root, "bin/tool"), 0600); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "absent.txt", "新增\n")
	if err := r.RestoreSnapshot(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, root, "bin/tool"); got != "原始\x00内容\r\n" {
		t.Fatalf("内容不符: %q", got)
	}
	info, err := os.Stat(filepath.Join(root, "bin/tool"))
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0751) {
		t.Fatalf("权限未恢复: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(root, "absent.txt")); !os.IsNotExist(err) {
		t.Fatalf("新增文件未删除: %v", err)
	}
}

func TestSnapshotSymlinksAndBoundary(t *testing.T) {
	t.Parallel()
	r, root := setupTestRepo(t)
	ctx := context.Background()
	outside := t.TempDir()
	writeTestFile(t, outside, "target", "外部内容\n")
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".git"), filepath.Join(root, "metadata")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside", filepath.Join(outside, "target"), "escape/target", "escape/missing/nested", ".", ".git/config", "metadata/config"} {
		if _, err := r.SnapshotPaths(ctx, root, []string{path}); err == nil {
			t.Fatalf("应拒绝路径 %q", path)
		}
		if err := r.RestoreSnapshot(ctx, &Snapshot{Root: root, Files: []SnapshotFile{{Path: path}}}); err == nil {
			t.Fatalf("恢复应拒绝路径 %q", path)
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(outside, "target"), link); err != nil {
		t.Fatal(err)
	}
	s, err := r.SnapshotPaths(ctx, root, []string{"link"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "link", "替换\n")
	if err := r.RestoreSnapshot(ctx, s); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(link); err != nil || target != filepath.Join(outside, "target") {
		t.Fatalf("链接未恢复: %q %v", target, err)
	}
	if got := readTestFile(t, outside, "target"); got != "外部内容\n" {
		t.Fatal("外部内容被修改")
	}
}

func TestSnapshotRestoreReplacesSymlink(t *testing.T) {
	t.Parallel()
	r, root := setupTestRepo(t)
	ctx := context.Background()
	writeTestFile(t, root, "file", "原始\n")
	s, err := r.SnapshotPaths(ctx, root, []string{"file"})
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writeTestFile(t, outside, "target", "外部\n")
	if err := os.Remove(filepath.Join(root, "file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "target"), filepath.Join(root, "file")); err != nil {
		t.Fatal(err)
	}
	if err := r.RestoreSnapshot(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, root, "file"); got != "原始\n" {
		t.Fatal("普通文件未恢复")
	}
	if got := readTestFile(t, outside, "target"); got != "外部\n" {
		t.Fatal("符号链接目标被修改")
	}
}
