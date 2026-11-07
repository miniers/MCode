package remote

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/agentconf"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

func TestSetThreadMCPDisabled_RoundTrip(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	b.AddThread(wire.ThreadSummary{ID: "t1", Provider: "claude"}, nil)

	summary, err := b.SetThreadMCPDisabled(context.Background(), "t1", []string{
		"claude:global:alpha",
		" ",
		"codex:project:beta",
		"claude:global:alpha",
	})
	if err != nil {
		t.Fatalf("SetThreadMCPDisabled: %v", err)
	}
	want := []string{"claude:global:alpha", "codex:project:beta"}
	if len(summary.MCPDisabled) != len(want) {
		t.Fatalf("summary.MCPDisabled = %v, want %v", summary.MCPDisabled, want)
	}
	for i := range want {
		if summary.MCPDisabled[i] != want[i] {
			t.Fatalf("summary.MCPDisabled = %v, want %v", summary.MCPDisabled, want)
		}
	}

	got := b.ThreadMCPDisabled("t1")
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ThreadMCPDisabled = %v, want %v", got, want)
	}

	// 空名单表示恢复为沿用全局设置。
	cleared, err := b.SetThreadMCPDisabled(context.Background(), "t1", nil)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if len(cleared.MCPDisabled) != 0 {
		t.Fatalf("cleared MCPDisabled = %v, want empty", cleared.MCPDisabled)
	}
	if len(b.ThreadMCPDisabled("t1")) != 0 {
		t.Fatalf("ThreadMCPDisabled after clear = %v, want empty", b.ThreadMCPDisabled("t1"))
	}
	if b.ThreadMCPDisabled("missing") != nil {
		t.Fatal("unknown thread must return nil")
	}
}

// assertMCPDisabledEqual 按顺序比较禁用名单；nil 与空切片都视为空名单。
func assertMCPDisabledEqual(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
	}
}

// 名单写穿到 store：同一个 store 新建 backend（模拟重启）后仍能从 store 还原。
func TestSetThreadMCPDisabled_PersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "mcp_disabled.db"))
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	defer s.Close()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	b := NewMemoryBackend(hub)
	defer b.Close()
	b.SetDataDir(t.TempDir())
	if err := b.AttachStore(ctx, s); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}
	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "P1", Path: t.TempDir()})
	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	want := []string{"claude:global:alpha", "codex:project:beta"}
	if _, err := b.SetThreadMCPDisabled(ctx, res.ThreadID, want); err != nil {
		t.Fatalf("SetThreadMCPDisabled: %v", err)
	}

	// 模拟进程重启：新 backend 只从 store 恢复，内存里没有旧名单。
	reloaded := NewMemoryBackend(hub)
	defer reloaded.Close()
	if err := reloaded.AttachStore(ctx, s); err != nil {
		t.Fatalf("重启后 AttachStore: %v", err)
	}
	assertMCPDisabledEqual(t, "重启后 ThreadMCPDisabled", reloaded.ThreadMCPDisabled(res.ThreadID), want)
}

// 落盘失败必须返回错误且不改内存；用关闭 store 制造写入失败。
func TestSetThreadMCPDisabled_StoreFailureKeepsMemory(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "mcp_disabled.db"))
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	b.SetDataDir(t.TempDir())
	if err := b.AttachStore(ctx, s); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}
	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "P1", Path: t.TempDir()})
	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	want := []string{"claude:global:alpha"}
	if _, err := b.SetThreadMCPDisabled(ctx, res.ThreadID, want); err != nil {
		t.Fatalf("SetThreadMCPDisabled: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("关闭 store: %v", err)
	}

	if _, err := b.SetThreadMCPDisabled(ctx, res.ThreadID, []string{"codex:global:beta"}); err == nil {
		t.Fatal("落盘失败必须返回错误")
	}
	assertMCPDisabledEqual(t, "失败后 ThreadMCPDisabled", b.ThreadMCPDisabled(res.ThreadID), want)
}

// 新建会话：没传 mcpDisabled 时套用全局「新会话默认停用」名单；显式传入时以传入值为准。
func TestCreateThread_AppliesNewThreadMCPDefaults(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()

	dataDir := t.TempDir()
	b.SetDataDir(dataDir)
	def := []string{"claude:global:ctx7", "codex:project:db"}
	if err := agentconf.SetNewThreadDisabledServers(agentconf.Options{DataDir: dataDir}, def); err != nil {
		t.Fatalf("写入默认停用名单: %v", err)
	}
	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "P1", Path: t.TempDir()})

	// 旧客户端不传该字段（nil）→ 套用默认名单。
	legacy, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("CreateThread(默认): %v", err)
	}
	th, err := b.GetThread(ctx, legacy.ThreadID)
	if err != nil {
		t.Fatalf("GetThread(默认): %v", err)
	}
	assertMCPDisabledEqual(t, "默认名单", th.MCPDisabled, def)

	// 显式传入的名单优先于默认值。
	explicit := []string{"codex:global:explicit"}
	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1", MCPDisabled: explicit})
	if err != nil {
		t.Fatalf("CreateThread(显式): %v", err)
	}
	th, err = b.GetThread(ctx, res.ThreadID)
	if err != nil {
		t.Fatalf("GetThread(显式): %v", err)
	}
	assertMCPDisabledEqual(t, "显式名单", th.MCPDisabled, explicit)

	// 空数组表示明确要求全部启用，不套默认。
	allOn, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1", MCPDisabled: []string{}})
	if err != nil {
		t.Fatalf("CreateThread(全开): %v", err)
	}
	th, err = b.GetThread(ctx, allOn.ThreadID)
	if err != nil {
		t.Fatalf("GetThread(全开): %v", err)
	}
	if len(th.MCPDisabled) != 0 {
		t.Fatalf("显式空名单不应套默认，got %v", th.MCPDisabled)
	}
}
