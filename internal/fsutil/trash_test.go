package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func fakeTrash(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("仅 macOS 使用可替换的废纸篓目录")
	}
	root := t.TempDir()
	previous := TrashDir
	TrashDir = func() (string, error) { return root, nil }
	t.Cleanup(func() { TrashDir = previous })
	return root
}

func TestMoveToTrash(t *testing.T) {
	root := fakeTrash(t)
	source := filepath.Join(t.TempDir(), "skill.txt")
	for _, content := range []string{"first", "second", "third"} {
		if err := os.WriteFile(source, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := MoveToTrash(source); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(source); !os.IsNotExist(err) {
			t.Fatalf("源文件仍存在：%v", err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 3 {
		t.Fatalf("废纸篓条目：%v, %v", entries, err)
	}
	contents := map[string]bool{}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		contents[string(data)] = true
	}
	if !contents["first"] || !contents["second"] || !contents["third"] {
		t.Fatalf("重名文件被覆盖：%v", contents)
	}
}

func TestMoveToTrashConcurrent(t *testing.T) {
	root := fakeTrash(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		source := filepath.Join(t.TempDir(), "same.txt")
		if err := os.WriteFile(source, []byte(fmt.Sprint(i)), 0o600); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := MoveToTrash(source); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 8 {
		t.Fatalf("并发移入丢失文件：%v, %v", entries, err)
	}
}

func TestMoveToTrashUnsupported(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("当前平台支持废纸篓")
	}
	if err := MoveToTrash(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("不支持的平台应返回错误")
	}
}
