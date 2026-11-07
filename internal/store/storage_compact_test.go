package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	compactTestProjectID = "p-compact"
	compactTestThreadID  = "t-compact"
	compactTestTime      = "2026-01-01T00:00:00Z"
)

func openCompactTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "compact.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func compactTestPragma(t *testing.T, s *Store, name string) int {
	t.Helper()
	v, err := s.pragmaInt(context.Background(), name)
	if err != nil {
		t.Fatalf("读取 PRAGMA %s 失败: %v", name, err)
	}
	return v
}

// seedCompactThread 建一个项目和会话，作为消息的父记录。
func seedCompactThread(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO projects (id, name, path, created_at, updated_at)
		VALUES (?, '压缩测试项目', ?, ?, ?)
	`, compactTestProjectID, t.TempDir(), compactTestTime, compactTestTime); err != nil {
		t.Fatalf("插入项目失败: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO threads (id, project_id, title, provider, created_at, updated_at)
		VALUES (?, ?, '压缩测试会话', 'claude', ?, ?)
	`, compactTestThreadID, compactTestProjectID, compactTestTime, compactTestTime); err != nil {
		t.Fatalf("插入会话失败: %v", err)
	}
}

// insertCompactMessages 插入 count 条正文约 bodyBytes 字节的消息，一个事务写完。
func insertCompactMessages(t *testing.T, s *Store, count, bodyBytes int) {
	t.Helper()
	ctx := context.Background()
	body := strings.Repeat("字", bodyBytes/3)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("开启事务失败: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO messages (id, thread_id, seq, role, kind, text, turn_index, created_at)
		VALUES (?, ?, ?, 'assistant', 'text', ?, ?, ?)
	`)
	if err != nil {
		t.Fatalf("准备插入语句失败: %v", err)
	}
	defer func() { _ = stmt.Close() }()
	for i := 0; i < count; i++ {
		if _, err := stmt.ExecContext(ctx, fmt.Sprintf("m-%d", i), compactTestThreadID, i, body, i, compactTestTime); err != nil {
			t.Fatalf("插入第 %d 条消息失败: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("提交消息失败: %v", err)
	}
}

func deleteCompactMessages(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(), "DELETE FROM messages"); err != nil {
		t.Fatalf("删除消息失败: %v", err)
	}
}

// compactTestMessageCount 是两个预整理用例共用的消息条数。
const compactTestMessageCount = 300

// createCompactTestDB 建一个 auto_vacuum=0 的库，写入 300 条约 8KB 的消息后关闭，
// 返回库文件路径，交给 CompactForIncrementalVacuum 在「未打开」状态下预整理。
func createCompactTestDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "compact.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if mode := compactTestPragma(t, s, "auto_vacuum"); mode != 0 {
		_ = s.Close()
		t.Fatalf("新库 auto_vacuum 期望 0，实际 %d", mode)
	}
	seedCompactThread(t, s)
	insertCompactMessages(t, s, compactTestMessageCount, 8<<10)
	if err := s.Close(); err != nil {
		t.Fatalf("关闭测试库失败: %v", err)
	}
	return path
}

func TestEnsureIncrementalAutoVacuum(t *testing.T) {
	ctx := context.Background()
	s := openCompactTestStore(t)

	// 新库由早期迁移建出重复索引，先确认它真的在，后面的删除才有意义。
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_messages_thread_seq'`).Scan(&name)
	if err != nil {
		t.Fatalf("新库应存在待删除的重复索引: %v", err)
	}

	converted, err := s.EnsureIncrementalAutoVacuum(ctx)
	if err != nil {
		t.Fatalf("切换增量 auto_vacuum 失败: %v", err)
	}
	if !converted {
		t.Fatalf("首次调用应完成一次性切换")
	}
	if mode := compactTestPragma(t, s, "auto_vacuum"); mode != autoVacuumIncremental {
		t.Fatalf("auto_vacuum 期望 %d，实际 %d", autoVacuumIncremental, mode)
	}

	err = s.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_messages_thread_seq'`).Scan(&name)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("重复索引应已删除，实际 err=%v name=%q", err, name)
	}

	converted, err = s.EnsureIncrementalAutoVacuum(ctx)
	if err != nil {
		t.Fatalf("第二次调用失败: %v", err)
	}
	if converted {
		t.Fatalf("已切换的库第二次调用不应再做 VACUUM")
	}
}

func TestReclaimFreePagesAfterDelete(t *testing.T) {
	ctx := context.Background()
	s := openCompactTestStore(t)

	if _, err := s.EnsureIncrementalAutoVacuum(ctx); err != nil {
		t.Fatalf("切换增量 auto_vacuum 失败: %v", err)
	}
	seedCompactThread(t, s)
	insertCompactMessages(t, s, 300, 8<<10)
	beforePages := compactTestPragma(t, s, "page_count")

	deleteCompactMessages(t, s)
	freeBefore := compactTestPragma(t, s, "freelist_count")
	if freeBefore == 0 {
		t.Fatalf("删除大正文后应产生空闲页")
	}

	// 故意用小批次，覆盖多批循环与批间等待。
	reclaimed, err := s.ReclaimFreePages(ctx, 64, time.Millisecond)
	if err != nil {
		t.Fatalf("回收空闲页失败: %v", err)
	}
	if reclaimed <= 0 || reclaimed > int64(freeBefore) {
		t.Fatalf("回收页数应在 (0, %d] 之间，实际 %d", freeBefore, reclaimed)
	}
	if free := compactTestPragma(t, s, "freelist_count"); free != 0 {
		t.Fatalf("回收后不应还有空闲页，实际 %d", free)
	}
	afterPages := compactTestPragma(t, s, "page_count")
	if afterPages >= beforePages {
		t.Fatalf("page_count 应明显减少，删除前 %d，回收后 %d", beforePages, afterPages)
	}
}

func TestReclaimFreePagesSkipsNonIncremental(t *testing.T) {
	ctx := context.Background()
	s := openCompactTestStore(t)

	if mode := compactTestPragma(t, s, "auto_vacuum"); mode == autoVacuumIncremental {
		t.Fatalf("新库不应已是增量 auto_vacuum")
	}
	seedCompactThread(t, s)
	insertCompactMessages(t, s, 100, 8<<10)
	deleteCompactMessages(t, s)
	freeBefore := compactTestPragma(t, s, "freelist_count")
	if freeBefore == 0 {
		t.Fatalf("删除大正文后应产生空闲页")
	}

	reclaimed, err := s.ReclaimFreePages(ctx, 0, 0)
	if err != nil {
		t.Fatalf("非增量模式下调用应直接返回: %v", err)
	}
	if reclaimed != 0 {
		t.Fatalf("非增量模式不应回收页，实际 %d", reclaimed)
	}
	if free := compactTestPragma(t, s, "freelist_count"); free != freeBefore {
		t.Fatalf("非增量模式空闲页不应变化，期望 %d，实际 %d", freeBefore, free)
	}
}

func TestCompactForIncrementalVacuum(t *testing.T) {
	ctx := context.Background()
	path := createCompactTestDB(t)

	var mu sync.Mutex
	var samples [][2]int64
	converted, err := CompactForIncrementalVacuum(ctx, path, func(done, total int64) {
		mu.Lock()
		samples = append(samples, [2]int64{done, total})
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("预整理数据库失败: %v", err)
	}
	if !converted {
		t.Fatalf("首次调用应完成预整理")
	}
	if len(samples) == 0 {
		t.Fatalf("progress 至少应被调用一次")
	}

	total := samples[0][1]
	if total <= 0 {
		t.Fatalf("total 应为正数，实际 %d", total)
	}
	for i, sample := range samples {
		if sample[1] != total {
			t.Fatalf("第 %d 次进度的 total 应恒为 %d，实际 %d", i, total, sample[1])
		}
		if i > 0 && sample[0] < samples[i-1][0] {
			t.Fatalf("进度不应回退：第 %d 次 %d 小于上一次 %d", i, sample[0], samples[i-1][0])
		}
		if i == len(samples)-1 {
			if sample[0] != total {
				t.Fatalf("最后一次进度应为 done == total == %d，实际 %d", total, sample[0])
			}
			continue
		}
		if sample[0] < 0 || sample[0] > total-1 {
			t.Fatalf("中间进度应在 [0, %d] 内，实际 %d", total-1, sample[0])
		}
	}

	// 替换完成后先检查残留文件，再重新打开（Open 会重新进入 WAL 模式生成 -wal）。
	for _, stale := range []string{path + ".compact-tmp", path + ".pre-compact", path + "-wal", path + "-shm"} {
		if _, statErr := os.Stat(stale); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("预整理后不应残留 %s，err=%v", stale, statErr)
		}
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("重新打开预整理后的库失败: %v", err)
	}
	defer func() { _ = s.Close() }()

	if mode := compactTestPragma(t, s, "auto_vacuum"); mode != autoVacuumIncremental {
		t.Fatalf("预整理后 auto_vacuum 期望 %d，实际 %d", autoVacuumIncremental, mode)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages").Scan(&count); err != nil {
		t.Fatalf("统计消息失败: %v", err)
	}
	if count != compactTestMessageCount {
		t.Fatalf("消息条数期望 %d，实际 %d", compactTestMessageCount, count)
	}
	var text string
	if err := s.db.QueryRowContext(ctx, "SELECT text FROM messages WHERE id = 'm-0'").Scan(&text); err != nil {
		t.Fatalf("读取消息正文失败: %v", err)
	}
	if want := strings.Repeat("字", (8<<10)/3); text != want {
		t.Fatalf("消息正文不一致：期望 %d 字节，实际 %d 字节", len(want), len(text))
	}
}

func TestCompactForIncrementalVacuumSkipsConvertedDB(t *testing.T) {
	ctx := context.Background()
	path := createCompactTestDB(t)

	if converted, err := CompactForIncrementalVacuum(ctx, path, nil); err != nil || !converted {
		t.Fatalf("首次预整理失败: converted=%v err=%v", converted, err)
	}

	called := false
	converted, err := CompactForIncrementalVacuum(ctx, path, func(done, total int64) { called = true })
	if err != nil {
		t.Fatalf("第二次调用失败: %v", err)
	}
	if converted {
		t.Fatalf("已转换的库不应再次预整理")
	}
	if called {
		t.Fatalf("已转换的库不应上报进度")
	}
}

func TestCompactForIncrementalVacuumMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	converted, err := CompactForIncrementalVacuum(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("库文件不存在时不应返回错误: %v", err)
	}
	if converted {
		t.Fatalf("库文件不存在时不应做预整理")
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("库文件不存在时不应创建文件，err=%v", statErr)
	}
}

func TestCompactForIncrementalVacuumKeepsOriginalOnFailure(t *testing.T) {
	ctx := context.Background()
	path := createCompactTestDB(t)

	// 用非空目录占住临时文件路径，删除和 VACUUM INTO 都无法进行。
	tmpPath := path + compactTempSuffix
	if err := os.MkdirAll(tmpPath, 0o700); err != nil {
		t.Fatalf("创建占位目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpPath, "occupied"), []byte("keep"), 0o600); err != nil {
		t.Fatalf("写入占位文件失败: %v", err)
	}

	converted, err := CompactForIncrementalVacuum(ctx, path, nil)
	if err == nil {
		t.Fatalf("临时路径不可用时预整理应返回错误")
	}
	if converted {
		t.Fatalf("失败时不应报告已替换原库")
	}
	if _, statErr := os.Stat(filepath.Join(tmpPath, "occupied")); statErr != nil {
		t.Fatalf("失败时不应清理他人在占位目录里的文件: %v", statErr)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("失败后原库应仍可打开: %v", err)
	}
	defer func() { _ = s.Close() }()
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages").Scan(&count); err != nil {
		t.Fatalf("统计原库消息失败: %v", err)
	}
	if count != compactTestMessageCount {
		t.Fatalf("失败后原库消息条数期望 %d，实际 %d", compactTestMessageCount, count)
	}
}
