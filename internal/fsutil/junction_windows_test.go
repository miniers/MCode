//go:build windows

package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLinkOrCopyWindowsDirJunction 验证 Windows 软链接失败时目录退化为目录联接：
// 经联接写入的文件落到原目录；删除账号目录只删联接，不删原目录内容。
func TestLinkOrCopyWindowsDirJunction(t *testing.T) {
	failSymlink(t, "windows")
	dir := t.TempDir()
	target := filepath.Join(dir, "host skills")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep.md"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	account := filepath.Join(dir, "account")
	if err := os.MkdirAll(account, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(account, "skills")
	if err := LinkOrCopy(target, link); err != nil {
		t.Fatalf("LinkOrCopy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(link, "new.md"), []byte("new"), 0o600); err != nil {
		t.Fatalf("经联接写入失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "new.md")); err != nil {
		t.Fatalf("经联接写入的文件没有落到原目录: %v", err)
	}

	if err := os.RemoveAll(account); err != nil {
		t.Fatalf("删除账号目录: %v", err)
	}
	for _, name := range []string{"keep.md", "new.md"} {
		if _, err := os.Stat(filepath.Join(target, name)); err != nil {
			t.Fatalf("删除账号目录后原目录的 %s 丢失: %v", name, err)
		}
	}
}
