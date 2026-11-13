//go:build darwin

package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/testenv"
)

// TestFSEventsWatcherGeneration 在真实仓库上验证 FSEvents 监听：写文件后 5 秒内 generation 应增大。
func TestFSEventsWatcherGeneration(t *testing.T) {
	testenv.TempHome(t)
	_, dir := initGitRepo(t)

	w, err := newPlatformWatcher(dir)
	if err != nil {
		t.Skipf("FSEvents 平台监听不可用，跳过: %v", err)
	}
	defer func() {
		if cerr := w.Close(); cerr != nil {
			t.Fatalf("Close 失败: %v", cerr)
		}
	}()

	// TempDir 的 /var 是 /private/var 的软链，监听器内部按真实路径比对
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks 失败: %v", err)
	}
	fw, ok := w.(*fseventsWatcher)
	if !ok {
		t.Skip("平台监听实现不是 fseventsWatcher，跳过")
	}
	if fw.root != real {
		t.Fatalf("监听根目录未解析为真实路径: %s != %s", fw.root, real)
	}

	ctx := context.Background()
	before, err := w.Generation(ctx)
	if err != nil {
		t.Skipf("FSEvents 未就绪，跳过: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "changed.txt"), []byte("changed\n"), 0644); err != nil {
		t.Fatalf("写入 changed.txt 失败: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		gen, gerr := w.Generation(ctx)
		if gerr == nil && gen > before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("5 秒内 generation 未增大: before=%d last=%d err=%v", before, gen, gerr)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
