package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

// TestComposerTabSlot_FollowUpQueue 测试输入框上方 TabSlot 与排队面板交互：
// 1. 存在 2 条排队消息时折叠行显示「排队 2」与首条摘要，看不到第二条内容；
// 2. 点击展开后按顺序展示两行排队消息；
// 3. 点击删除按钮调用 RemoveQueued，传入对应的 followUpID。
func TestComposerTabSlot_FollowUpQueue(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	fake.setQueue("t1", []QueuedFollowUp{
		{ID: "q1", Text: "第一条排队跟进"},
		{ID: "q2", Text: "第二条排队跟进", AttachmentCount: 2},
	})

	// 未展开前只显示折叠行「排队 2」和首条摘要，看不到第二条内容
	waitFor(t, tt, func() bool {
		return tt.HasText("排队 2") && tt.HasText("第一条排队跟进") && !tt.HasText("第二条排队跟进")
	})

	// 点击折叠行展开排队面板
	if err := tt.Click("排队 2"); err != nil {
		t.Fatalf("click 排队 2 tab failed: %v", err)
	}

	// 展开后两行按顺序可见，并显示操作按钮
	waitFor(t, tt, func() bool {
		return tt.HasText("第一条排队跟进") &&
			tt.HasText("第二条排队跟进") &&
			tt.HasText("清空全部") &&
			tt.HasText("编辑") &&
			tt.HasText("删除")
	})

	// 点击删除按钮（点击第一个可见的“删除”，对应 q1）
	if err := tt.Click("删除"); err != nil {
		t.Fatalf("click 删除 failed: %v", err)
	}

	// 验证 RemoveQueued 收到正确调用
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.removeQueuedCalls) == 1 &&
			fake.removeQueuedCalls[0].ThreadID == "t1" &&
			fake.removeQueuedCalls[0].FollowUpID == "q1"
	})
}
