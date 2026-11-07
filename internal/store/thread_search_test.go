package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestThreadSearchAndPage(t *testing.T) {
	ctx := context.Background()
	s, _ := newMessageSearchTestStore(t, "threads.db")
	if err := s.UpsertProject(ctx, Project{ID: "p2", Name: "P2", Path: "/p2"}); err != nil {
		t.Fatal(err)
	}
	for _, thread := range []Thread{{ID: "a", ProjectID: "p1", Title: "A"}, {ID: "b", ProjectID: "p2"}, {ID: "c", ProjectID: "p1"}} {
		thread.Provider = "test"
		if err := s.CreateThread(ctx, thread); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetThreadArchived(ctx, "c", true); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 0; i < 4; i++ {
		appendSearchMessage(t, s, fmt.Sprintf("a%d", i), "a", "user", "text", "match 50% a_b", now.Add(time.Duration(i)*time.Second))
	}
	appendSearchMessage(t, s, "thinking", "a", "assistant", "thinking", "hidden", now)
	appendSearchMessage(t, s, "tool", "a", "tool", "tool", "hidden", now)
	appendSearchMessage(t, s, "other", "b", "user", "text", "match 50x axb", now)
	appendSearchMessage(t, s, "archived", "c", "user", "text", "match", now)
	for _, q := range []string{"match", "50%", "a_b"} {
		hits, err := s.SearchThreadMessages(ctx, []string{"/p1"}, q, 0)
		if err != nil || len(hits) != 1 {
			t.Fatalf("%q: %+v %v", q, hits, err)
		}
		if hits[0].ThreadID != "a" || hits[0].Title != "A" || hits[0].MessageCount != 4 || len(hits[0].Matches) != 3 || hits[0].Matches[0].Seq != 4 || hits[0].LastActivity.IsZero() {
			t.Fatalf("unexpected hit: %+v", hits[0])
		}
	}
	for _, path := range []string{"/p1", "/p2", "/missing"} {
		hits, err := s.SearchThreadMessages(ctx, []string{path}, "hidden", 0)
		if err != nil || len(hits) != 0 {
			t.Fatalf("hidden: %+v %v", hits, err)
		}
	}
	hits, err := s.SearchThreadMessages(ctx, []string{"/p2"}, "50%", 0)
	if err != nil || len(hits) != 0 {
		t.Fatalf("wildcard: %+v %v", hits, err)
	}
	title, total, msgs, err := s.ThreadMessagesPage(ctx, []string{"/p1"}, "a", 1, 2)
	if err != nil || title != "A" || total != 4 || len(msgs) != 2 || msgs[0].Seq != 2 || msgs[1].Seq != 3 {
		t.Fatalf("page: %q %d %+v %v", title, total, msgs, err)
	}
	_, _, _, err = s.ThreadMessagesPage(ctx, []string{"/p2"}, "a", 0, 0)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found: %v", err)
	}
}

// 测试命中编号与多路径检索：Offset 是正文消息（过滤后）的 0 基下标，可直接用于翻页；
// 两条路径的线程应一次命中；空切片时检索返回 nil、翻页返回 ErrNotFound。
func TestThreadSearchOffsetMultiPath(t *testing.T) {
	ctx := context.Background()
	s, _ := newMessageSearchTestStore(t, "thread_search_offset.db")
	if err := s.UpsertProject(ctx, Project{ID: "p2", Name: "P2", Path: "/p2"}); err != nil {
		t.Fatal(err)
	}
	for _, thread := range []Thread{{ID: "off", ProjectID: "p1", Title: "Off", Provider: "test"}, {ID: "wt", ProjectID: "p2", Title: "Wt", Provider: "test"}} {
		if err := s.CreateThread(ctx, thread); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	// 前两条 tool 消息被 threadBodyFilter 过滤，命中的正文在正文序列里的下标是 0。
	appendSearchMessage(t, s, "t1", "off", "tool", "tool", "needle tool", now)
	appendSearchMessage(t, s, "t2", "off", "tool", "tool", "needle tool 2", now.Add(time.Second))
	appendSearchMessage(t, s, "t3", "off", "assistant", "text", "needle answer", now.Add(2*time.Second))
	appendSearchMessage(t, s, "w1", "wt", "user", "text", "needle in worktree", now.Add(3*time.Second))

	hits, err := s.SearchThreadMessages(ctx, []string{"/p1", "/p2"}, "needle", 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("两条路径的线程应一次命中，得到 %+v", hits)
	}
	byThread := map[string]ThreadSearchHit{}
	for _, h := range hits {
		byThread[h.ThreadID] = h
	}
	off, ok := byThread["off"]
	if !ok || len(off.Matches) != 1 || off.Matches[0].Offset != 0 || off.Matches[0].Seq != 3 {
		t.Fatalf("unexpected off matches: %+v", off)
	}
	title, total, msgs, err := s.ThreadMessagesPage(ctx, []string{"/p1"}, "off", off.Matches[0].Offset, 1)
	if err != nil || title != "Off" || total != 1 || len(msgs) != 1 || msgs[0].Text != "needle answer" {
		t.Fatalf("page by offset: %q %d %+v %v", title, total, msgs, err)
	}

	empty, err := s.SearchThreadMessages(ctx, nil, "needle", 0)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty paths should return nil: %+v %v", empty, err)
	}
	if _, _, _, err := s.ThreadMessagesPage(ctx, nil, "off", 0, 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty paths should be not found: %v", err)
	}
}
