package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"mcode/internal/remote"
	"mcode/internal/store"
	"mcode/internal/swarm"
	mcodeui "mcode/internal/ui"
)

func writeTestBytes(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", path, err)
	}
	data := make([]byte, size)
	for i := range data {
		data[i] = 'a'
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

func setupStorageTestEnv(t *testing.T) (dir string, db *store.Store, backend *remote.MemoryBackend, mgr *swarm.Manager, client *Client) {
	t.Helper()
	ctx := context.Background()
	dir = t.TempDir()

	dbPath := filepath.Join(dir, "mcode.db")
	var err error
	db, err = store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC()
	old := now.Add(-30 * 24 * time.Hour)
	if err := db.UpsertProject(ctx, store.Project{
		ID: "p1", Name: "P1", Path: "/tmp/p1", CreatedAt: old, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	// 1. 存活线程 + session_id
	if err := db.CreateThread(ctx, store.Thread{
		ID: "th-live", ProjectID: "p1", Title: "live", Provider: "mcode", SessionID: "sess-live",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateThread live: %v", err)
	}

	// 2. 冷归档线程 + session_id
	if err := db.CreateThread(ctx, store.Thread{
		ID: "th-cold", ProjectID: "p1", Title: "cold", Provider: "mcode", SessionID: "sess-cold",
		CreatedAt: old, UpdatedAt: old,
	}); err != nil {
		t.Fatalf("CreateThread cold: %v", err)
	}
	if _, err := db.AppendMessage(ctx, store.Message{
		ID: "msg-cold-1", ThreadID: "th-cold", Role: "user", Kind: "text", Text: "hello",
		TurnIndex: 1, CreatedAt: old,
	}); err != nil {
		t.Fatalf("AppendMessage cold: %v", err)
	}
	if err := db.SetThreadArchived(ctx, "th-cold", true); err != nil {
		t.Fatalf("SetThreadArchived cold: %v", err)
	}
	rep, err := db.RunColdArchive(ctx, store.ColdArchiveConfig{MinIdle: time.Hour, BatchPause: time.Millisecond})
	if err != nil || rep.ThreadsArchived != 1 {
		t.Fatalf("RunColdArchive: rep=%+v err=%v", rep, err)
	}

	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	t.Cleanup(hub.Close)
	backend = remote.NewMemoryBackend(hub)
	t.Cleanup(func() { backend.Close() })
	if err := backend.AttachStore(ctx, db); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}

	// 3. Swarm Manager 及其在用补丁
	usedLanded := filepath.Join(dir, "swarm-landed", "used.patch")
	usedArch := filepath.Join(dir, "swarm-patches", "used.patch")
	usedRev := filepath.Join(dir, "swarm-reviews", "used.patch")
	writeTestBytes(t, usedLanded, 10)
	writeTestBytes(t, usedArch, 11)
	writeTestBytes(t, usedRev, 12)

	adapter := swarmStoreAdapter{s: db}
	if err := adapter.SaveLead(ctx, swarm.LeadRecord{
		LeadThreadID: "th-live",
		Enabled:      true,
		Bees: []swarm.Bee{{
			ThreadID:          "bee-1",
			Status:            swarm.StatusCompleted,
			LandedPatchPath:   usedLanded,
			ArchivedPatchPath: usedArch,
			ReviewPatchPath:   usedRev,
		}},
	}); err != nil {
		t.Fatalf("SaveLead: %v", err)
	}
	mgr = swarm.NewManager(swarm.Options{Persister: adapter, DataDir: dir, PollInterval: time.Minute})
	if err := mgr.Restore(ctx); err != nil {
		t.Fatalf("mgr.Restore: %v", err)
	}

	// 4. 存活与冷归档线程文件（不应出现在扫描结果）
	writeTestBytes(t, filepath.Join(dir, "media", "th-live", "a.png"), 100)
	writeTestBytes(t, filepath.Join(dir, "tool-outputs", "th-live", "m1.txt"), 101)
	writeTestBytes(t, filepath.Join(dir, "mcode", "sessions", "sess-live.jsonl"), 102)
	writeTestBytes(t, filepath.Join(dir, "media", "th-cold", "b.png"), 103)
	writeTestBytes(t, filepath.Join(dir, "tool-outputs", "th-cold", "m2.txt"), 104)
	writeTestBytes(t, filepath.Join(dir, "mcode", "sessions", "sess-cold.jsonl"), 105)

	// 5. 已删线程残留（应出现）
	writeTestBytes(t, filepath.Join(dir, "media", "th-deleted", "old.png"), 200)
	writeTestBytes(t, filepath.Join(dir, "tool-outputs", "th-deleted", "old.txt"), 201)
	writeTestBytes(t, filepath.Join(dir, "mcode", "sessions", "sess-deleted.jsonl"), 202)

	// 6. 无主补丁（应出现）
	writeTestBytes(t, filepath.Join(dir, "swarm-landed", "orphan.patch"), 300)
	writeTestBytes(t, filepath.Join(dir, "swarm-patches", "orphan.patch"), 301)
	writeTestBytes(t, filepath.Join(dir, "swarm-reviews", "orphan.patch"), 302)

	// 7. 旧备份（应出现）
	writeTestBytes(t, filepath.Join(dir, "swarm-worktree-backup-20260101", "wt.txt"), 400)

	// 8. 日志：当前日志不出现，轮转日志出现
	writeTestBytes(t, filepath.Join(dir, "logs", "mcode.log"), 500)
	writeTestBytes(t, filepath.Join(dir, "logs", "mcode.log.1"), 501)

	// 9. 受保护路径（绝不能出现）
	writeTestBytes(t, filepath.Join(dir, "mcode.db-wal"), 50)
	writeTestBytes(t, filepath.Join(dir, "agy-accounts", "acc.json"), 51)
	writeTestBytes(t, filepath.Join(dir, "agy-bindings.json"), 52)
	writeTestBytes(t, filepath.Join(dir, "agentcfg", "cfg.json"), 53)
	writeTestBytes(t, filepath.Join(dir, "tailnet", "state.json"), 54)
	writeTestBytes(t, filepath.Join(dir, "attachments", "att.png"), 55)
	writeTestBytes(t, filepath.Join(dir, "worktrees", "wt1", "f.txt"), 56)
	writeTestBytes(t, filepath.Join(dir, "swarm-worktrees", "swt1", "f.txt"), 57)
	writeTestBytes(t, filepath.Join(dir, "shadow-git", "HEAD"), 58)

	// 10. 符号链接不跟随、不列入
	outsideDir := t.TempDir()
	writeTestBytes(t, filepath.Join(outsideDir, "secret.txt"), 9999)
	_ = os.Symlink(outsideDir, filepath.Join(dir, "media", "th-symlink"))
	_ = os.Symlink(outsideDir, filepath.Join(dir, "swarm-worktree-backup-sym"))

	client = &Client{
		dataDir: dir,
		backend: backend,
		swarm:   mgr,
	}
	return dir, db, backend, mgr, client
}

func TestStorageScan_SafeCategoriesAndExclusions(t *testing.T) {
	_, _, _, _, client := setupStorageTestEnv(t)

	report, err := client.StorageScan(context.Background())
	if err != nil {
		t.Fatalf("StorageScan: %v", err)
	}
	if report.TotalBytes <= 0 {
		t.Fatalf("TotalBytes 应大于 0，实际 %d", report.TotalBytes)
	}

	got := make(map[string]mcodeui.StorageItem, len(report.Items))
	for _, it := range report.Items {
		got[it.ID] = it
	}

	want := map[string]mcodeui.StorageItem{
		"media/th-deleted":                 {ID: "media/th-deleted", Category: mcodeui.StorageCategoryDeletedSessions, Bytes: 200},
		"tool-outputs/th-deleted":          {ID: "tool-outputs/th-deleted", Category: mcodeui.StorageCategoryDeletedSessions, Bytes: 201},
		"mcode/sessions/sess-deleted.jsonl": {ID: "mcode/sessions/sess-deleted.jsonl", Category: mcodeui.StorageCategoryDeletedSessions, Bytes: 202},
		"swarm-landed/orphan.patch":        {ID: "swarm-landed/orphan.patch", Category: mcodeui.StorageCategoryOrphanPatches, Bytes: 300},
		"swarm-patches/orphan.patch":       {ID: "swarm-patches/orphan.patch", Category: mcodeui.StorageCategoryOrphanPatches, Bytes: 301},
		"swarm-reviews/orphan.patch":       {ID: "swarm-reviews/orphan.patch", Category: mcodeui.StorageCategoryOrphanPatches, Bytes: 302},
		"swarm-worktree-backup-20260101":   {ID: "swarm-worktree-backup-20260101", Category: mcodeui.StorageCategoryOldBackups, Bytes: 400},
		"logs/mcode.log.1":                 {ID: "logs/mcode.log.1", Category: mcodeui.StorageCategoryOldLogs, Bytes: 501},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("StorageScan 候选项不匹配:\ngot:  %+v\nwant: %+v", got, want)
	}

	// Manager 不可用时，无主补丁一类整体不列。
	clientNoSwarm := &Client{
		dataDir: client.dataDir,
		backend: client.backend,
	}
	reportNoSwarm, err := clientNoSwarm.StorageScan(context.Background())
	if err != nil {
		t.Fatalf("StorageScan without swarm: %v", err)
	}
	for _, it := range reportNoSwarm.Items {
		if it.Category == mcodeui.StorageCategoryOrphanPatches {
			t.Fatalf("Manager 不可用时不应列出无主补丁: %+v", it)
		}
	}
}

func TestStorageClean_TrustBoundaryAndPartialFailure(t *testing.T) {
	dir, _, _, _, client := setupStorageTestEnv(t)

	var trashed []string
	oldTrash := moveToTrash
	moveToTrash = func(path string) error {
		trashed = append(trashed, path)
		if filepath.Base(path) == "mcode.log.1" {
			return errors.New("mock trash error")
		}
		return os.RemoveAll(path)
	}
	t.Cleanup(func() { moveToTrash = oldTrash })

	// 传入合法项 + 非法/受保护 ID（../、mcode.db、存活会话、当前日志）。
	freed, err := client.StorageClean(context.Background(), []string{
		"../outside",
		"mcode.db",
		"agy-bindings.json",
		"media/th-live",
		"logs/mcode.log",
		"media/th-deleted",
		"logs/mcode.log.1",
		"swarm-worktree-backup-20260101",
		"media/th-deleted", // 重复 ID 不应重复计入
	})
	if err == nil {
		t.Fatal("部分失败时应返回首个错误")
	}
	// media/th-deleted (200) + swarm-worktree-backup-20260101 (400) 成功；logs/mcode.log.1 (501) 失败。
	if freed != 600 {
		t.Fatalf("freed = %d, want 600", freed)
	}

	sort.Strings(trashed)
	wantTrashed := []string{
		filepath.Join(dir, "logs", "mcode.log.1"),
		filepath.Join(dir, "media", "th-deleted"),
		filepath.Join(dir, "swarm-worktree-backup-20260101"),
	}
	if !reflect.DeepEqual(trashed, wantTrashed) {
		t.Fatalf("trashed = %v, want %v", trashed, wantTrashed)
	}

	// 验证 mcode.db、存活会话与当前日志仍完整保留。
	for _, keep := range []string{
		filepath.Join(dir, "mcode.db"),
		filepath.Join(dir, "media", "th-live", "a.png"),
		filepath.Join(dir, "media", "th-cold", "b.png"),
		filepath.Join(dir, "logs", "mcode.log"),
		filepath.Join(dir, "logs", "mcode.log.1"),
	} {
		if _, statErr := os.Stat(keep); statErr != nil {
			t.Fatalf("应保留的文件 %s 丢失: %v", keep, statErr)
		}
	}
}
