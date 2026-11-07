package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestAttentionInboxShowsNotificationTitle 收件箱浮层要直接显示通知标题，
// 用户不用展开条目就能判断是哪条通知。
func TestAttentionInboxShowsNotificationTitle(t *testing.T) {
	fake := newAttentionFakeClient(
		attentionItem("n1", "thread_failed", "thread", "t1", "构建失败：超时", time.Minute),
	)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("待处理 1 条") })
	if err := tt.Click("待处理"); err != nil {
		t.Fatalf("点击待处理入口失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("构建失败：超时") })
}
