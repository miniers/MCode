package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// TestThreadBeeTiersRoundTrip 验证 BeeTiers 在 CreateThread、GetThread、ListThreads 与 SetThreadBeeTiers 下正确持久化与回读。
func TestThreadBeeTiersRoundTrip(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "tiers_test.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	if err := s.UpsertProject(ctx, Project{ID: "p1", Name: "Project 1", Path: "/tmp/p1"}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}

	const initialTiersJSON = `{"edit":{"model":"gpt-4o","provider":"codex"}}`

	t1 := Thread{
		ID:        "t-tiers-1",
		ProjectID: "p1",
		Title:     "测试档位会话",
		Provider:  "claude",
		Model:     "claude-3-5-sonnet",
		BeeTiers:  initialTiersJSON,
	}
	if err := s.CreateThread(ctx, t1); err != nil {
		t.Fatalf("CreateThread t1 失败: %v", err)
	}

	t2 := Thread{
		ID:        "t-tiers-2",
		ProjectID: "p1",
		Title:     "空档位覆盖会话",
		Provider:  "claude",
		Model:     "claude-3-5-sonnet",
		BeeTiers:  "",
	}
	if err := s.CreateThread(ctx, t2); err != nil {
		t.Fatalf("CreateThread t2 失败: %v", err)
	}

	// 1. GetThread 读取验证
	loaded1, err := s.GetThread(ctx, "t-tiers-1")
	if err != nil {
		t.Fatalf("GetThread t-tiers-1 失败: %v", err)
	}
	if loaded1.BeeTiers != initialTiersJSON {
		t.Fatalf("期望 BeeTiers 为 %q，实际为 %q", initialTiersJSON, loaded1.BeeTiers)
	}

	loaded2, err := s.GetThread(ctx, "t-tiers-2")
	if err != nil {
		t.Fatalf("GetThread t-tiers-2 失败: %v", err)
	}
	if loaded2.BeeTiers != "" {
		t.Fatalf("期望空 BeeTiers，实际为 %q", loaded2.BeeTiers)
	}

	// 2. ListThreads 批量查询验证
	list, err := s.ListThreads(ctx, "p1", false)
	if err != nil {
		t.Fatalf("ListThreads 失败: %v", err)
	}
	foundT1 := false
	for _, item := range list {
		if item.ID == "t-tiers-1" {
			foundT1 = true
			if item.BeeTiers != initialTiersJSON {
				t.Fatalf("ListThreads 期望 BeeTiers %q，实际为 %q", initialTiersJSON, item.BeeTiers)
			}
		}
	}
	if !foundT1 {
		t.Fatal("ListThreads 未找到 t-tiers-1")
	}

	// 3. SetThreadBeeTiers 更新并重新回读
	const updatedTiersJSON = `{"deep":{"effort":"high"}}`
	if err := s.SetThreadBeeTiers(ctx, "t-tiers-1", updatedTiersJSON); err != nil {
		t.Fatalf("SetThreadBeeTiers 更新失败: %v", err)
	}

	loadedAfterUpdate, err := s.GetThread(ctx, "t-tiers-1")
	if err != nil {
		t.Fatalf("GetThread 更新后读取失败: %v", err)
	}
	if loadedAfterUpdate.BeeTiers != updatedTiersJSON {
		t.Fatalf("更新后期望 BeeTiers 为 %q，实际为 %q", updatedTiersJSON, loadedAfterUpdate.BeeTiers)
	}

	// 4. SetThreadBeeTiers 清空为 ""
	if err := s.SetThreadBeeTiers(ctx, "t-tiers-1", ""); err != nil {
		t.Fatalf("SetThreadBeeTiers 清空失败: %v", err)
	}
	loadedAfterClear, err := s.GetThread(ctx, "t-tiers-1")
	if err != nil {
		t.Fatalf("GetThread 清空后读取失败: %v", err)
	}
	if loadedAfterClear.BeeTiers != "" {
		t.Fatalf("清空后期望 BeeTiers 为空串，实际为 %q", loadedAfterClear.BeeTiers)
	}

	// 5. 不存在的线程 ID 应返回 ErrNotFound
	if err := s.SetThreadBeeTiers(ctx, "not-exist-thread", updatedTiersJSON); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望返回 ErrNotFound，实际为 %v", err)
	}
}

// TestThreadSwarmPairIDRoundTrip 验证 SetThreadSwarmPairID 写穿 threads.hydra_pair_id，空串回读为空串，不存在时返回 ErrNotFound。
func TestThreadSwarmPairIDRoundTrip(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "pair_id_test.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	if err := s.UpsertProject(ctx, Project{ID: "p1", Name: "Project 1", Path: "/tmp/p1"}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}

	t1 := Thread{
		ID:        "t-pair-1",
		ProjectID: "p1",
		Title:     "测试配对会话",
		Provider:  "claude",
		Model:     "claude-3-5-sonnet",
	}
	if err := s.CreateThread(ctx, t1); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}

	// 1. 写入显式选中的配对 ID，重新读取会话后值一致
	if err := s.SetThreadSwarmPairID(ctx, "t-pair-1", "pair-store-1"); err != nil {
		t.Fatalf("SetThreadSwarmPairID 更新失败: %v", err)
	}
	loaded, err := s.GetThread(ctx, "t-pair-1")
	if err != nil {
		t.Fatalf("GetThread 更新后读取失败: %v", err)
	}
	if loaded.SwarmPairID != "pair-store-1" {
		t.Fatalf("更新后期望 SwarmPairID 为 %q，实际为 %q", "pair-store-1", loaded.SwarmPairID)
	}

	// 2. 写空串后回读为空串（空串存 NULL，读取处 COALESCE 为 ""）
	if err := s.SetThreadSwarmPairID(ctx, "t-pair-1", ""); err != nil {
		t.Fatalf("SetThreadSwarmPairID 清空失败: %v", err)
	}
	loadedAfterClear, err := s.GetThread(ctx, "t-pair-1")
	if err != nil {
		t.Fatalf("GetThread 清空后读取失败: %v", err)
	}
	if loadedAfterClear.SwarmPairID != "" {
		t.Fatalf("清空后期望 SwarmPairID 为空串，实际为 %q", loadedAfterClear.SwarmPairID)
	}

	// 3. 不存在的线程 ID 应返回 ErrNotFound
	if err := s.SetThreadSwarmPairID(ctx, "not-exist-thread", "pair-store-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望返回 ErrNotFound，实际为 %v", err)
	}
}
