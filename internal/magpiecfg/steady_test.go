// 本文件的测试改写自 magpie 的 internal/steady/steady_test.go（MIT License），
// 见 docs/THIRD_PARTY_NOTICES.md。

package magpiecfg

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeWindows 把包级重试开关和 rename 换成测试用的假实现，返回调用计数。
func fakeWindows(t *testing.T, failTimes int) *int {
	t.Helper()
	windowsWas, renameWas := onWindows, rename
	t.Cleanup(func() { onWindows, rename = windowsWas, renameWas })
	onWindows = true
	calls := new(int)
	rename = func(oldpath, newpath string) error {
		*calls++
		if *calls <= failTimes {
			return errors.New("Access is denied.")
		}
		return os.Rename(oldpath, newpath)
	}
	return calls
}

func TestWriteAtomicRetriesOnWindowsAccessDenied(t *testing.T) {
	calls := fakeWindows(t, 2)
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := WriteAtomic(p, []byte("{\"a\":1}\n")); err != nil {
		t.Fatalf("重试后仍然失败: %v", err)
	}
	if *calls != 3 {
		t.Fatalf("rename 调用了 %d 次，想要 3 次", *calls)
	}
	if got := readTestFile(t, p); got != "{\"a\":1}\n" {
		t.Fatalf("重试后内容不对: %q", got)
	}
}

func TestWriteAtomicGivesUpAfterTries(t *testing.T) {
	calls := fakeWindows(t, tries+10)
	p := filepath.Join(t.TempDir(), "settings.json")
	err := WriteAtomic(p, []byte("{\"a\":1}\n"))
	if err == nil {
		t.Fatal("一直失败时应该返回错误")
	}
	// tries 是重试次数：第一次尝试之外再等 tries 次。
	if *calls != tries+1 {
		t.Fatalf("rename 调用了 %d 次，想要 %d 次", *calls, tries+1)
	}
}

func TestWriteAtomicNoRetryOutsideWindows(t *testing.T) {
	windowsWas, renameWas := onWindows, rename
	t.Cleanup(func() { onWindows, rename = windowsWas, renameWas })
	onWindows = false
	calls := 0
	denied := errors.New("Access is denied.")
	rename = func(oldpath, newpath string) error {
		calls++
		return denied
	}
	p := filepath.Join(t.TempDir(), "settings.json")
	err := WriteAtomic(p, []byte("{\"a\":1}\n"))
	if !errors.Is(err, denied) {
		t.Fatalf("错误没有原样返回: %v", err)
	}
	if calls != 1 {
		t.Fatalf("非 Windows 下 rename 调用了 %d 次，想要 1 次", calls)
	}
}

func TestRenameDoesNotRetryMissingPath(t *testing.T) {
	calls := fakeWindows(t, 0)
	dir := t.TempDir()
	err := Rename(filepath.Join(dir, "missing"), filepath.Join(dir, "gone"))
	if err == nil {
		t.Fatal("重命名不存在的文件应该报错")
	}
	if *calls != 1 {
		t.Fatalf("路径不存在时也重试了: %d 次", *calls)
	}
}
