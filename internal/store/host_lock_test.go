package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mcode/internal/filelock"
)

func TestHostLockPreservesLiveProvisionalThread(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mcode.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	if !first.IsPrimary() {
		t.Fatal("首次打开应为主宿主")
	}
	now := time.Now().UTC()
	if err := first.UpsertProject(ctx, Project{ID: "p", Name: "项目", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := first.CreateThread(ctx, Thread{ID: "draft", ProjectID: "p", Title: "草稿", Provider: "memory", IsProvisional: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if second.IsPrimary() {
		t.Fatal("锁被占用时第二次打开应为非主宿主")
	}
	if _, err := second.GetThread(ctx, "draft"); err != nil {
		t.Fatalf("非主宿主不能清扫活跃宿主的空草稿: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if second.IsPrimary() {
		t.Fatal("非主宿主不能在运行期间自动升级")
	}
	third, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = third.Close() })
	if !third.IsPrimary() {
		t.Fatal("原主宿主关闭后再次打开应取得主宿主锁")
	}
	if _, err := third.GetThread(ctx, "draft"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("新主宿主应清扫空草稿，实际错误: %v", err)
	}
}

func TestHostLockReleasedOnOpenFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcode.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// 库版本超前使迁移失败，验证已经取得的锁也会释放。
	if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", len(migrations)+1)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if failed, err := Open(path); err == nil {
		_ = failed.Close()
		t.Fatal("打开超前版本的库应失败")
	}
	lock, err := filelock.TryAcquire(path + ".host.lock")
	if err != nil {
		t.Fatalf("Open 失败后不应遗留主宿主锁: %v", err)
	}
	_ = lock.Close()
}

func TestHostLockCloseConcurrentAndRepeated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcode.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := s.Close(); err != nil {
				t.Errorf("并发关闭失败: %v", err)
			}
		})
	}
	wg.Wait()
	next, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Close() })
	if !next.IsPrimary() {
		t.Fatal("并发关闭后应释放主宿主锁")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if lock, err := filelock.TryAcquire(path + ".host.lock"); !errors.Is(err, filelock.ErrBusy) {
		_ = lock.Close()
		t.Fatalf("重复关闭不能释放新主宿主的锁: %v", err)
	}
}

func TestHostLockFallback(t *testing.T) {
	t.Run("内存库", func(t *testing.T) {
		s, err := Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if !s.IsPrimary() || s.hostLock != nil {
			t.Fatal("内存库应按主宿主处理且不创建文件锁")
		}
	})
	t.Run("锁文件无法打开", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "mcode.db")
		if err := os.Mkdir(path+".host.lock", 0o700); err != nil {
			t.Fatal(err)
		}
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if !s.IsPrimary() {
			t.Fatal("锁文件打开失败应按主宿主继续启动")
		}
	})
}
