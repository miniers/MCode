package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestOpenSkipsSyncQuickCheck 验证常规启动不同步执行 quick_check，标记文件存在时才同步检查并在通过后删除标记。
func TestOpenSkipsSyncQuickCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skip_sync.db")

	var syncCalls int
	origRun := runQuickCheck
	t.Cleanup(func() { runQuickCheck = origRun })
	runQuickCheck = func(ctx context.Context, db *sql.DB) error {
		syncCalls++
		return scanQuickCheck(ctx, db)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if syncCalls != 0 {
		t.Fatalf("常规启动不应同步执行 quick_check，实际调用 %d 次", syncCalls)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	// 标记文件存在时，下次打开应同步检查并在通过后删除标记
	if err := os.WriteFile(quickCheckMarkerPath(path), []byte("pending\n"), 0o644); err != nil {
		t.Fatalf("写入标记文件失败: %v", err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("带标记 Open 失败: %v", err)
	}
	if syncCalls != 1 {
		t.Fatalf("标记文件存在时应同步执行一次 quick_check，实际调用 %d 次", syncCalls)
	}
	if quickCheckMarkerExists(path) {
		t.Fatalf("同步检查通过后应删除标记文件")
	}
	if err := s2.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
}

// TestOpenQuarantinesNonDatabaseFile 验证文件头损坏（不是数据库）时仍会隔离旧文件并自愈重建。
func TestOpenQuarantinesNonDatabaseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.db")
	if err := os.WriteFile(path, []byte("this is not a sqlite database at all"), 0o644); err != nil {
		t.Fatalf("写入损坏文件失败: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("损坏文件应隔离并自愈重建，Open 返回错误: %v", err)
	}
	defer s.Close()
	if s.RecoveredBackupPath() == "" {
		t.Fatalf("期望记录自愈备份路径")
	}
	matches, globErr := filepath.Glob(path + ".corrupt-*")
	if globErr != nil {
		t.Fatalf("列举隔离文件失败: %v", globErr)
	}
	if len(matches) != 1 {
		t.Fatalf("期望存在 1 个隔离备份文件，实际: %v", matches)
	}
}

// TestBackgroundQuickCheckWritesMarker 验证后台检查失败时写入标记文件，让下次启动走同步检查与自愈。
func TestBackgroundQuickCheckWritesMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bg_fail.db")

	origBg := backgroundQuickCheck
	t.Cleanup(func() { backgroundQuickCheck = origBg })
	checked := make(chan struct{})
	backgroundQuickCheck = func(ctx context.Context, filePath string) error {
		defer close(checked)
		return errors.New("quick_check: page 3 is malformed (corrupt)")
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	<-checked

	deadline := time.Now().Add(2 * time.Second)
	for !quickCheckMarkerExists(path) {
		if time.Now().After(deadline) {
			_ = s.Close()
			t.Fatalf("后台检查失败后应写入标记文件 %s", quickCheckMarkerPath(path))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
}
