package localmem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/projctx"
	"mcode/internal/store"
)

func TestThreadsMapping(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertProject(ctx, store.Project{ID: "p", Name: "P", Path: "/p"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateThread(ctx, store.Thread{ID: "t", ProjectID: "p", Title: "Title", Provider: "test"}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	// 首条是 tool 消息，被正文过滤器排除：seq 与正文下标不一致，Index 必须是正文下标。
	if _, err := s.AppendMessage(ctx, store.Message{ID: "m0", ThreadID: "t", Role: "tool", Kind: "tool", Text: "tool noise", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(ctx, store.Message{ID: "m", ThreadID: "t", Role: "assistant", Kind: "text", Text: "hello world", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	backend := Threads{Store: s}
	hits, err := backend.Search(ctx, "/p", "hello", 0)
	if err != nil || len(hits) != 1 {
		t.Fatalf("search: %+v %v", hits, err)
	}
	h := hits[0]
	if h.ThreadID != "t" || h.Title != "Title" || h.Source != "mcode" || h.MessageCount != 1 || !h.LastActivity.Equal(at) || len(h.Matches) != 1 || h.Matches[0].Index != 0 || h.Matches[0].Role != "assistant" || h.Matches[0].Snippet != "hello world" {
		t.Fatalf("hit: %+v", h)
	}
	// Index 可直接作为 Fetch 的 offset，翻到的第一条就是命中消息。
	hit, err := backend.Fetch(ctx, "/p", "t", h.Matches[0].Index, 1)
	if err != nil || len(hit.Messages) != 1 || hit.Messages[0].Content != "hello world" {
		t.Fatalf("fetch by index: %+v %v", hit, err)
	}
	page, err := backend.Fetch(ctx, "/p", "t", 0, 20)
	if err != nil || page.ThreadID != "t" || page.Title != "Title" || page.Total != 1 || page.Offset != 0 || len(page.Messages) != 1 {
		t.Fatalf("page: %+v %v", page, err)
	}
	m := page.Messages[0]
	if m.Role != "assistant" || m.Content != "hello world" || !m.Timestamp.Equal(at) {
		t.Fatalf("message: %+v", m)
	}
	if _, err := backend.Fetch(ctx, "/p", "missing", 0, 20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing thread should be ErrNotFound: %v", err)
	}
}

// 主 checkout 与其 Git worktree 分别是两个项目路径，两边的线程必须互相可搜、可翻页。
func TestThreadsWorktreeSharing(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	main := t.TempDir()
	gitDir := filepath.Join(main, ".git", "worktrees", "wt")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := projctx.MainCheckout(wt); got != main {
		t.Fatalf("worktree should resolve to main checkout: got %q want %q", got, main)
	}

	if err := s.UpsertProject(ctx, store.Project{ID: "pm", Name: "M", Path: main}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertProject(ctx, store.Project{ID: "pw", Name: "W", Path: wt}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateThread(ctx, store.Thread{ID: "tm", ProjectID: "pm", Title: "Main", Provider: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateThread(ctx, store.Thread{ID: "tw", ProjectID: "pw", Title: "Worktree", Provider: "test"}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	if _, err := s.AppendMessage(ctx, store.Message{ID: "mm", ThreadID: "tm", Role: "user", Kind: "text", Text: "shared keyword on main", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(ctx, store.Message{ID: "mw", ThreadID: "tw", Role: "user", Kind: "text", Text: "shared keyword in worktree", CreatedAt: at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}

	backend := Threads{Store: s}
	for _, from := range []string{main, wt} {
		hits, err := backend.Search(ctx, from, "shared keyword", 0)
		if err != nil || len(hits) != 2 {
			t.Fatalf("search from %q: %+v %v", from, hits, err)
		}
		for _, threadID := range []string{"tm", "tw"} {
			page, err := backend.Fetch(ctx, from, threadID, 0, 10)
			if err != nil || page.ThreadID != threadID || len(page.Messages) != 1 {
				t.Fatalf("fetch %q from %q: %+v %v", threadID, from, page, err)
			}
			if _, err := backend.Fetch(ctx, from, "nope", 0, 10); !errors.Is(err, ErrNotFound) {
				t.Fatalf("missing thread from %q should be ErrNotFound: %v", from, err)
			}
		}
	}
}
