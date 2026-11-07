package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestScrollToMessageIDFlashesTarget 验证跳转成功后把落点记进 flashMessageID，
// 并按跳转序号清除：旧一次跳转的清除回调不会抹掉新一次跳转的高亮。
func TestScrollToMessageIDFlashesTarget(t *testing.T) {
	msgs := []Message{
		{ID: "m1", Role: "user", Text: "第一问", TurnIndex: 1, IsFinal: true},
		{ID: "m2", Role: "assistant", Kind: "text", Text: "第一答", TurnIndex: 1, IsFinal: true},
	}
	items := buildTimelineItems(msgs)
	state := &chatState{}

	if !scrollToMessageID(state, items, "m1") {
		t.Fatal("跳转到已存在的消息应成功")
	}
	state.mu.Lock()
	got, seq1 := state.flashMessageID, state.flashSeq
	state.mu.Unlock()
	if got != "m1" {
		t.Fatalf("跳转后应高亮目标消息，got %q", got)
	}

	// 1.4s 的清除回调执行后，高亮回到空。
	clearJumpFlash(state, seq1)
	state.mu.Lock()
	got = state.flashMessageID
	state.mu.Unlock()
	if got != "" {
		t.Fatalf("清除回调执行后应取消高亮，got %q", got)
	}

	if !scrollToMessageID(state, items, "m2") {
		t.Fatal("第二次跳转应成功")
	}
	state.mu.Lock()
	got, seq2 := state.flashMessageID, state.flashSeq
	state.mu.Unlock()
	if got != "m2" {
		t.Fatalf("第二次跳转后应高亮新目标，got %q", got)
	}
	if seq2 == seq1 {
		t.Fatalf("每次跳转都应推进序号，got %d", seq2)
	}

	// 旧一次跳转的清除回调晚到：序号对不上，不能清掉新一次的高亮。
	clearJumpFlash(state, seq1)
	state.mu.Lock()
	got = state.flashMessageID
	state.mu.Unlock()
	if got != "m2" {
		t.Fatalf("旧回调不应清掉新一次跳转的高亮，got %q", got)
	}

	clearJumpFlash(state, seq2)
	state.mu.Lock()
	got = state.flashMessageID
	state.mu.Unlock()
	if got != "" {
		t.Fatalf("新一次跳转的清除回调应生效，got %q", got)
	}
}

// TestToolOutputDialogShowsFullText 验证「显示完整输出」改为独立弹窗：
// 时间线保持头部预览，全文最后一行只出现在弹窗里，弹窗提供「复制输出」与「关闭」。
func TestToolOutputDialogShowsFullText(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "u1", Role: "user", Text: "看看这个文件", TurnIndex: 1, IsFinal: true},
		{ID: "t1", Role: "tool", ToolName: "read", ToolInput: `{"file_path":"/workspace/main.go"}`, Text: toolDetailTestLines(40), TurnIndex: 1},
	})
	s, root := buildRoot(fake)
	s.mu.Lock()
	s.expandedTool["t1"] = true
	s.mu.Unlock()
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("LINE-01") })
	if tt.HasText("LINE-40") {
		t.Fatalf("预览只保留头部，不应出现最后一行: %v", tt.Texts())
	}

	s.openToolOutputDialog(Message{ID: "t1", Role: "tool", ToolName: "read"}, toolDetailTestLines(40))
	waitFor(t, tt, func() bool { return tt.HasText("复制输出") })

	if got := countText(tt, "LINE-40"); got != 1 {
		t.Fatalf("全文最后一行只应出现在弹窗里，got %d: %v", got, tt.Texts())
	}
	if !tt.HasText("关闭") {
		t.Fatalf("弹窗应提供「关闭」按钮: %v", tt.Texts())
	}
	if !tt.HasText("展开全部") {
		t.Fatalf("时间线应保持折叠与入口: %v", tt.Texts())
	}

	clickText(t, tt, "关闭")
	waitFor(t, tt, func() bool { return !tt.HasText("复制输出") })
}
