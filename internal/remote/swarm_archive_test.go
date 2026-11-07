package remote

import (
	"context"
	"errors"
	"testing"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// ArchiveBeeSession 要求：非 codex 线程不归档；codex 且只有一条用户消息时归档；
// 有用户追问、运行中或线程不存在时不归档。
func TestArchiveBeeSession_Guards(t *testing.T) {
	ctx := context.Background()

	newBackend := func(t *testing.T) *MemoryBackend {
		t.Helper()
		hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
		t.Cleanup(hub.Close)
		b := NewMemoryBackend(hub)
		t.Cleanup(b.Close)
		return b
	}

	t.Run("非 codex 线程返回 false", func(t *testing.T) {
		b := newBackend(t)
		b.AddThread(wire.ThreadSummary{ID: "bee-claude", ProjectID: "p1", Provider: "claude"}, []wire.ThreadMessage{
			{ID: "m1", Role: "user", Text: "派发"},
		})
		ok, err := b.ArchiveBeeSession(ctx, "bee-claude")
		if err != nil || ok {
			t.Fatalf("非 codex 线程不应归档: ok=%v err=%v", ok, err)
		}
		th, _ := b.GetThread(ctx, "bee-claude")
		if th.IsArchived {
			t.Fatalf("非 codex 线程不应被归档")
		}
	})

	t.Run("codex 单条用户消息归档并可重复调用", func(t *testing.T) {
		b := newBackend(t)
		b.AddThread(wire.ThreadSummary{ID: "bee-codex", ProjectID: "p1", Provider: "codex"}, []wire.ThreadMessage{
			{ID: "m1", Role: "user", Text: "派发"},
			{ID: "m2", Role: "assistant", Text: "完成"},
		})
		ok, err := b.ArchiveBeeSession(ctx, "bee-codex")
		if err != nil || !ok {
			t.Fatalf("codex bee 应归档: ok=%v err=%v", ok, err)
		}
		th, err := b.GetThread(ctx, "bee-codex")
		if err != nil || !th.IsArchived {
			t.Fatalf("线程应处于已归档状态: %+v, %v", th, err)
		}
		// 已归档时重复调用仍返回 true
		ok, err = b.ArchiveBeeSession(ctx, "bee-codex")
		if err != nil || !ok {
			t.Fatalf("已归档线程应返回 true: ok=%v err=%v", ok, err)
		}
	})

	t.Run("有用户追问时返回 false", func(t *testing.T) {
		b := newBackend(t)
		b.AddThread(wire.ThreadSummary{ID: "bee-followup", ProjectID: "p1", Provider: "codex"}, []wire.ThreadMessage{
			{ID: "m1", Role: "user", Text: "派发"},
			{ID: "m2", Role: "assistant", Text: "完成"},
			{ID: "m3", Role: "user", Text: "追问"},
		})
		ok, err := b.ArchiveBeeSession(ctx, "bee-followup")
		if err != nil || ok {
			t.Fatalf("有追问的 bee 不应归档: ok=%v err=%v", ok, err)
		}
		th, _ := b.GetThread(ctx, "bee-followup")
		if th.IsArchived {
			t.Fatalf("有追问的 bee 不应被归档")
		}
	})

	t.Run("运行中的 codex 线程返回 false", func(t *testing.T) {
		b := newBackend(t)
		b.AddThread(wire.ThreadSummary{ID: "bee-running", ProjectID: "p1", Provider: "codex"}, []wire.ThreadMessage{
			{ID: "m1", Role: "user", Text: "派发"},
		})
		b.mu.Lock()
		b.threads["bee-running"].runTurnID = "turn-1"
		b.mu.Unlock()
		ok, err := b.ArchiveBeeSession(ctx, "bee-running")
		if err != nil || ok {
			t.Fatalf("运行中的 bee 不应归档: ok=%v err=%v", ok, err)
		}
	})

	t.Run("线程不存在时返回 store.ErrNotFound", func(t *testing.T) {
		b := newBackend(t)
		ok, err := b.ArchiveBeeSession(ctx, "missing-thread")
		if ok || !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("期望 store.ErrNotFound，实际 ok=%v err=%v", ok, err)
		}
	})
}

// 归档必须写穿 store，重载后仍为已归档状态。
func TestArchiveBeeSession_PersistsArchived(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	if err := b.AttachStore(ctx, s); err != nil {
		t.Fatal(err)
	}
	// 线程在 store 中有外键依赖，必须先建好项目，线程才能真实落盘。
	projID := "proj-archive-persist"
	b.AddProject(wire.ProjectSummary{ID: projID, Name: "Proj Archive", Path: "/path/archive"})
	b.AddThread(wire.ThreadSummary{ID: "bee-persist", ProjectID: projID, Provider: "codex"}, []wire.ThreadMessage{
		{ID: "m1", Role: "user", Text: "派发"},
	})

	if ok, err := b.ArchiveBeeSession(ctx, "bee-persist"); err != nil || !ok {
		t.Fatalf("归档失败: ok=%v err=%v", ok, err)
	}

	// store 中应已写穿归档状态。
	stored, err := s.GetThread(ctx, "bee-persist")
	if err != nil || !stored.Archived {
		t.Fatalf("store 中归档状态未写穿: %+v, %v", stored, err)
	}

	reloaded := NewMemoryBackend(hub)
	defer reloaded.Close()
	if err := reloaded.AttachStore(ctx, s); err != nil {
		t.Fatal(err)
	}
	th, err := reloaded.GetThread(ctx, "bee-persist")
	if err != nil || !th.IsArchived {
		t.Fatalf("归档状态未持久化: %+v, %v", th, err)
	}
}
