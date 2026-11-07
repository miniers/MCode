// 本文件的测试移植自 magpie 的 internal/edit/bom_test.go、symlink_test.go
// 等（MIT License），见 docs/THIRD_PARTY_NOTICES.md。

package magpiecfg

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWriteAtomicKeepsBOM(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	writeTestFile(t, p, "\xef\xbb\xbf{\n  \"a\": 1\n}\n")
	if err := WriteAtomic(p, []byte("{\n  \"a\": 2\n}\n")); err != nil {
		t.Fatal(err)
	}
	got := readTestFile(t, p)
	if !strings.HasPrefix(got, "\xef\xbb\xbf") {
		t.Fatalf("BOM 丢失: %q", got)
	}
	if strings.Count(got, "\xef\xbb\xbf") != 1 {
		t.Fatalf("BOM 不止一个: %q", got)
	}
}

func TestWriteAtomicDoesNotAddBOM(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	writeTestFile(t, p, "{\n  \"a\": 1\n}\n")
	if err := WriteAtomic(p, []byte("{\n  \"a\": 2\n}\n")); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, p); strings.HasPrefix(got, "\xef\xbb\xbf") {
		t.Fatalf("没有 BOM 的文件被加上了 BOM: %q", got)
	}
}

func TestWriteAtomicFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	link := filepath.Join(dir, "link.json")
	writeTestFile(t, target, "{\"a\":1}\n")
	if err := os.Symlink("target.json", link); err != nil {
		t.Skipf("此环境无法创建符号链接: %v", err)
	}
	if err := WriteAtomic(link, []byte("{\"a\":2}\n")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("链接被替换成了普通文件")
	}
	if got := readTestFile(t, target); !strings.Contains(got, `"a":2`) {
		t.Fatalf("链接目标没有更新: %q", got)
	}
}

func TestAtomicallyRestoresOnFailure(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.toml")
	c := filepath.Join(dir, "c.json")
	writeTestFile(t, a, "{\n  \"x\": 1\n}\n")
	writeTestFile(t, b, "x = 1\n")

	errBoom := errors.New("boom")
	err := Atomically(func() error {
		if err := SetJSON(a, KV{Path: "x", Value: 2}); err != nil {
			return err
		}
		if err := SetJSON(c, KV{Path: "made", Value: true}); err != nil {
			return err
		}
		return errBoom
	}, a, c)
	if !errors.Is(err, errBoom) {
		t.Fatalf("错误没有传出来: %v", err)
	}
	if got := readTestFile(t, a); got != "{\n  \"x\": 1\n}\n" {
		t.Fatalf("a 没有恢复原样: %q", got)
	}
	if _, err := os.Stat(c); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("中途创建的文件没有删掉: %v", err)
	}
	if got := readTestFile(t, b); got != "x = 1\n" {
		t.Fatalf("未涉及的文件被改动: %q", got)
	}
}

func TestAtomicallyKeepsAllOnSuccess(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	err := Atomically(func() error {
		return SetJSON(a, KV{Path: "x", Value: 1})
	}, a)
	if err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, a); !bytes.Contains([]byte(got), []byte(`"x": 1`)) {
		t.Fatalf("成功的写入没有保留: %q", got)
	}
}
