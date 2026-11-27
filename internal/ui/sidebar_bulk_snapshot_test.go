package mcodeui

import "testing"

// TestBulkThreadsSnapshotIsIndependent 验证 bulkThreadsSnapshotLocked 返回独立切片副本，
// 解锁后后台原地修改原切片元素不会影响快照。
func TestBulkThreadsSnapshotIsIndependent(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.mu.Lock()
	s.threadsByProj["p"] = []Thread{
		{ID: "t1", ProjectID: "p", Title: "原始标题一", Dormant: false},
		{ID: "t2", ProjectID: "p", Title: "原始标题二", Dormant: false},
	}
	snap := bulkThreadsSnapshotLocked(s, "p")
	s.mu.Unlock()

	s.mu.Lock()
	s.threadsByProj["p"][0].Dormant = true
	s.threadsByProj["p"][0].Title = "已修改标题"
	s.mu.Unlock()

	if len(snap) != 2 {
		t.Fatalf("snap len = %d, want 2", len(snap))
	}
	if snap[0].Dormant {
		t.Fatal("snap[0].Dormant = true, want false")
	}
	if snap[0].Title != "原始标题一" {
		t.Fatalf("snap[0].Title = %q, want %q", snap[0].Title, "原始标题一")
	}
	if snap[1].Title != "原始标题二" {
		t.Fatalf("snap[1].Title = %q, want %q", snap[1].Title, "原始标题二")
	}
}
