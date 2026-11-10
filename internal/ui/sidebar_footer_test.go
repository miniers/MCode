package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestSidebarFooterLabels(t *testing.T) {
	state, view := buildRoot(NewFakeClient(), WithSettings(settingsTestDir(t), nil))
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	for _, label := range []string{"添加项目", "任务", "自动化", "设置", "更多"} {
		if _, ok := tt.Find(label); !ok {
			t.Errorf("找不到底栏按钮 %q", label)
		}
	}
}

func TestSidebarFooter_WorkspaceButtons(t *testing.T) {
	for _, tc := range []struct {
		label string
		empty string
	}{
		{label: "任务", empty: "还没有任务"},
		{label: "自动化", empty: "此项目没有自动化"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			fake := NewFakeClient()
			fake.SetKanbanBoard(KanbanBoard{})
			fake.SetAutomations(nil)
			state, view := buildRoot(fake)
			t.Cleanup(state.close)
			tt := ui.NewTester(view, 1000, 700)
			waitFor(t, tt, func() bool {
				return tt.HasText("测试会话1") && tt.HasText(tc.label)
			})

			if err := tt.Click(tc.label); err != nil {
				t.Fatalf("点击底栏 %q 失败: %v", tc.label, err)
			}
			waitFor(t, tt, func() bool {
				return tt.HasText(tc.empty)
			})
		})
	}
}

// TestSidebarFooter_KanbanReviewCount 验证 refreshKanbanBadge 缓存待审阅任务数：
// 只统计 Column 为 review 或带 CanReview 标记的任务，并在 30 秒内节流。
func TestSidebarFooter_KanbanReviewCount(t *testing.T) {
	fake := NewFakeClient()
	fake.kanbanBoard = KanbanBoard{
		Revision: 1,
		Tasks: []KanbanTask{
			{ID: "k1", ProjectID: "p1", Column: "review"},
			{ID: "k2", ProjectID: "p1", Column: "todo", CanReview: true},
			{ID: "k3", ProjectID: "p1", Column: "todo"},
			{ID: "k4", ProjectID: "p1", Column: "done"},
		},
	}
	s := newChatState(fake)

	if got := sidebarKanbanReviewCount(s); got != 0 {
		t.Fatalf("初始缓存应为 0，got %d", got)
	}

	refreshKanbanBadge(s)

	deadline := time.Now().Add(2 * time.Second)
	for sidebarKanbanReviewCount(s) != 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := sidebarKanbanReviewCount(s); got != 2 {
		t.Fatalf("待审阅任务数 = %d, want 2", got)
	}
	if calls := fake.KanbanBoardCalls(); calls != 1 {
		t.Fatalf("KanbanBoard 调用次数 = %d, want 1", calls)
	}

	// 30 秒节流：立即再次刷新不会发起新请求
	refreshKanbanBadge(s)
	if calls := fake.KanbanBoardCalls(); calls != 1 {
		t.Fatalf("节流期内不应再次请求，KanbanBoard 调用次数 = %d, want 1", calls)
	}
}

// TestSidebarFooter_KanbanBadgeSkipsNilClient 验证 client 为空时刷新静默跳过。
func TestSidebarFooter_KanbanBadgeSkipsNilClient(t *testing.T) {
	s := newChatState(nil)

	refreshKanbanBadge(s)

	if got := sidebarKanbanReviewCount(s); got != 0 {
		t.Fatalf("无 client 时缓存应保持 0，got %d", got)
	}
}
