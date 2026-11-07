package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// openMCPDisabledStore 打开临时库并建好依赖的测试项目。
func openMCPDisabledStore(t *testing.T, dbName string) (*Store, string) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), dbName)
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if err := s.UpsertProject(ctx, Project{ID: "p1", Name: "Project 1", Path: "/tmp/p1"}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}
	return s, dbPath
}

// assertMCPDisabled 比较名单内容；nil 与空切片视为等价。
func assertMCPDisabled(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("期望 MCPDisabled %v，实际为 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("期望 MCPDisabled %v，实际为 %v", want, got)
		}
	}
}

// TestCreateThreadMCPDisabledRoundTrip 验证 CreateThread 传入的名单能读回。
func TestCreateThreadMCPDisabledRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _ := openMCPDisabledStore(t, "mcp_disabled_create.db")
	defer s.Close()

	want := []string{"github", "linear"}
	thread := Thread{
		ID:          "t-mcp-create",
		ProjectID:   "p1",
		Title:       "带 MCP 禁用名单的会话",
		Provider:    "codex",
		MCPDisabled: want,
	}
	if err := s.CreateThread(ctx, thread); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	loaded, err := s.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	assertMCPDisabled(t, loaded.MCPDisabled, want)
}

// TestSetThreadMCPDisabledRoundTrip 验证写入后读回一致，并且重开数据库后仍能读回。
func TestSetThreadMCPDisabledRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, dbPath := openMCPDisabledStore(t, "mcp_disabled_set.db")

	thread := Thread{ID: "t-mcp-set", ProjectID: "p1", Title: "测试名单写入", Provider: "codex"}
	if err := s.CreateThread(ctx, thread); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	want := []string{"sentry", "playwright"}
	if err := s.SetThreadMCPDisabled(ctx, thread.ID, want); err != nil {
		t.Fatalf("SetThreadMCPDisabled 失败: %v", err)
	}
	loaded, err := s.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	assertMCPDisabled(t, loaded.MCPDisabled, want)

	if err := s.Close(); err != nil {
		t.Fatalf("关闭库失败: %v", err)
	}
	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("重新 Open 失败: %v", err)
	}
	defer reopened.Close()
	loaded, err = reopened.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("重开后 GetThread 失败: %v", err)
	}
	assertMCPDisabled(t, loaded.MCPDisabled, want)
}

// TestSetThreadMCPDisabledClears 验证空名单清空后读回 nil 或长度 0。
func TestSetThreadMCPDisabledClears(t *testing.T) {
	ctx := context.Background()
	s, _ := openMCPDisabledStore(t, "mcp_disabled_clear.db")
	defer s.Close()

	thread := Thread{
		ID:          "t-mcp-clear",
		ProjectID:   "p1",
		Title:       "清空名单",
		Provider:    "codex",
		MCPDisabled: []string{"github"},
	}
	if err := s.CreateThread(ctx, thread); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	if err := s.SetThreadMCPDisabled(ctx, thread.ID, []string{}); err != nil {
		t.Fatalf("SetThreadMCPDisabled 失败: %v", err)
	}
	loaded, err := s.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if len(loaded.MCPDisabled) != 0 {
		t.Fatalf("期望清空后 MCPDisabled 为 nil 或长度 0，实际为 %v", loaded.MCPDisabled)
	}
}

// TestSetThreadMCPDisabledNotFound 验证不存在的会话返回 ErrNotFound。
func TestSetThreadMCPDisabledNotFound(t *testing.T) {
	ctx := context.Background()
	s, _ := openMCPDisabledStore(t, "mcp_disabled_missing.db")
	defer s.Close()

	err := s.SetThreadMCPDisabled(ctx, "missing-thread", []string{"github"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际为 %v", err)
	}
}
