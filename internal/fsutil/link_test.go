package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// failSymlink 模拟 Windows 未开开发者模式时软链接创建失败。
func failSymlink(t *testing.T, goos string) {
	t.Helper()
	oldGOOS, oldSymlink := linkGOOS, symlink
	t.Cleanup(func() { linkGOOS, symlink = oldGOOS, oldSymlink })
	linkGOOS = goos
	symlink = func(string, string) error { return errors.New("A required privilege is not held by the client.") }
}

// TestLinkOrCopyNonWindowsReturnsSymlinkError 验证非 Windows 平台软链接失败时不退化。
func TestLinkOrCopyNonWindowsReturnsSymlinkError(t *testing.T) {
	failSymlink(t, "darwin")
	dir := t.TempDir()
	target := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := LinkOrCopy(target, link); err == nil {
		t.Fatal("LinkOrCopy 返回 nil，期望软链接错误")
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("不应创建 %s，Lstat err = %v", link, err)
	}
}

// TestLinkOrCopyWindowsFileFallback 验证 Windows 软链接失败时普通文件仍能共享到 link。
func TestLinkOrCopyWindowsFileFallback(t *testing.T) {
	failSymlink(t, "windows")
	dir := t.TempDir()
	target := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(target, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "account", "settings.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := LinkOrCopy(target, link); err != nil {
		t.Fatalf("LinkOrCopy: %v", err)
	}
	if data, err := os.ReadFile(link); err != nil || string(data) != `{"a":1}` {
		t.Fatalf("link 内容 = %q, %v", data, err)
	}
}
