package mcodeui

import (
	"testing"
)

func TestComposerHistoryEntries(t *testing.T) {
	messages := []Message{
		{ID: "u1", Role: "user", Text: "第一条"},
		{ID: "a1", Role: "assistant", Text: "助手回复"},
		{ID: "u2", Role: "user", Text: "   "}, // 空文本过滤
		{ID: "u3", Role: "user", Text: "第二条"},
		{ID: "u4", Role: "user", Text: "第二条"}, // 相邻重复去重
		{ID: "t1", Role: "tool", Text: "工具输出"},
		{ID: "u5", Role: "user", Text: " 第三条 "}, // 首尾空白裁剪
		{ID: "u6", Role: "user", Text: "第三条"},   // 裁剪后与上一条重复
		{ID: "u7", Role: "user", Text: "第一条"},   // 非相邻重复保留
	}

	got := composerHistoryEntries(messages)
	want := []string{"第一条", "第二条", "第三条", "第一条"}
	if len(got) != len(want) {
		t.Fatalf("composerHistoryEntries = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("composerHistoryEntries[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// 子代理汇报以 user 消息形式落入会话，但不是用户输入，不应进入方向键历史。
func TestComposerHistoryEntriesSkipsSwarmReports(t *testing.T) {
	messages := []Message{
		{ID: "u1", Role: "user", Text: "正常输入"},
		{ID: "h1", Role: "user", Text: "[编排] 子代理汇报：已完成 A 任务"},
		{ID: "h2", Role: "user", Text: "  [Hydra] 协作者汇报：已完成 B 任务"}, // 旧前缀 + 首尾空白：裁剪后仍应跳过
		{ID: "h3", Role: "user", Text: "[编排] 子代理汇报：已完成 C 任务"},
		{ID: "h4", Role: "user", Text: "[编排] 已派出 Ives"},
		{ID: "u2", Role: "user", Text: "另一条正常输入"},
	}

	got := composerHistoryEntries(messages)
	want := []string{"正常输入", "另一条正常输入"}
	if len(got) != len(want) {
		t.Fatalf("composerHistoryEntries = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("composerHistoryEntries[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	for _, entry := range got {
		if isSwarmNoticeText(entry) {
			t.Fatalf("编排通知不应出现在历史中：%q", entry)
		}
	}
}

func TestComposerHistoryEntriesEmpty(t *testing.T) {
	if got := composerHistoryEntries(nil); len(got) != 0 {
		t.Fatalf("composerHistoryEntries(nil) = %q, want empty", got)
	}
	if got := composerHistoryEntries([]Message{{Role: "assistant", Text: "hi"}}); len(got) != 0 {
		t.Fatalf("composerHistoryEntries(assistant) = %q, want empty", got)
	}
}

func TestCaretLineHelpers(t *testing.T) {
	text := "第一行\n第二行"
	// 首行：caret 在 '\n' 之前
	if !caretOnFirstLine(text, 0) || !caretOnFirstLine(text, 3) {
		t.Fatal("caretOnFirstLine should be true before the newline")
	}
	// caret 在 '\n' 之后不再位于首行
	if caretOnFirstLine(text, 4) || caretOnFirstLine(text, 7) {
		t.Fatal("caretOnFirstLine should be false after the newline")
	}
	// 末行：caret 之后没有 '\n'
	if !caretOnLastLine(text, 4) || !caretOnLastLine(text, 7) {
		t.Fatal("caretOnLastLine should be true after the newline")
	}
	if caretOnLastLine(text, 3) || caretOnLastLine(text, 0) {
		t.Fatal("caretOnLastLine should be false before the newline")
	}

	// 单行文本：既是首行也是末行
	if !caretOnFirstLine("单行", 0) || !caretOnFirstLine("单行", 2) {
		t.Fatal("single line text should be the first line")
	}
	if !caretOnLastLine("单行", 0) || !caretOnLastLine("单行", 2) {
		t.Fatal("single line text should be the last line")
	}

	// 空文本
	if !caretOnFirstLine("", 0) || !caretOnLastLine("", 0) {
		t.Fatal("empty text should be both first and last line")
	}
}

func TestComposerHistoryStateMachine(t *testing.T) {
	entries := []string{"first", "second", "third"}
	var h composerHistoryState

	// 未在导航时 next/restore 不生效
	if _, ok := h.next(entries); ok {
		t.Fatal("next should not hit before navigating")
	}
	if _, ok := h.restore(); ok {
		t.Fatal("restore should not hit before navigating")
	}

	// 首次 prev 暂存当前草稿并回到最新一条
	text, ok := h.prev(entries, "draft in progress")
	if !ok || text != "third" {
		t.Fatalf("first prev = (%q, %v), want (third, true)", text, ok)
	}
	if h.stash != "draft in progress" || h.recalled != "third" || h.index != 1 {
		t.Fatalf("state after first prev = %+v", h)
	}

	// 继续往旧翻，stash 不被覆盖
	if text, ok := h.prev(entries, "ignored"); !ok || text != "second" {
		t.Fatalf("second prev = (%q, %v), want (second, true)", text, ok)
	}
	if text, ok := h.prev(entries, "ignored"); !ok || text != "first" {
		t.Fatalf("third prev = (%q, %v), want (first, true)", text, ok)
	}
	if h.stash != "draft in progress" {
		t.Fatalf("stash = %q, want draft in progress", h.stash)
	}

	// 已到最旧一条，继续 prev 不命中且状态保持
	if text, ok := h.prev(entries, "ignored"); ok || text != "" {
		t.Fatalf("prev at oldest = (%q, %v), want (\"\", false)", text, ok)
	}
	if h.index != len(entries) {
		t.Fatalf("index at oldest = %d, want %d", h.index, len(entries))
	}

	// next 往回翻
	if text, ok := h.next(entries); !ok || text != "second" {
		t.Fatalf("first next = (%q, %v), want (second, true)", text, ok)
	}
	if text, ok := h.next(entries); !ok || text != "third" {
		t.Fatalf("second next = (%q, %v), want (third, true)", text, ok)
	}
	// 翻过最新一条恢复进入导航前的草稿并退出导航
	if text, ok := h.next(entries); !ok || text != "draft in progress" {
		t.Fatalf("next past newest = (%q, %v), want (draft in progress, true)", text, ok)
	}
	if h.index != 0 || h.stash != "" || h.recalled != "" {
		t.Fatalf("state after leaving navigation = %+v", h)
	}
	if _, ok := h.next(entries); ok {
		t.Fatal("next should not hit after leaving navigation")
	}

	// restore 从导航中途退出并恢复草稿
	if _, ok := h.prev(entries, "another draft"); !ok {
		t.Fatal("prev should hit")
	}
	if _, ok := h.prev(entries, "ignored"); !ok {
		t.Fatal("prev should hit")
	}
	if text, ok := h.restore(); !ok || text != "another draft" {
		t.Fatalf("restore = (%q, %v), want (another draft, true)", text, ok)
	}
	if h.index != 0 || h.stash != "" || h.recalled != "" {
		t.Fatalf("state after restore = %+v", h)
	}
	if _, ok := h.restore(); ok {
		t.Fatal("restore should not hit after leaving navigation")
	}

	// reset 丢弃状态，不返回文本
	h.prev(entries, "draft")
	h.reset()
	if h.index != 0 || h.stash != "" || h.recalled != "" {
		t.Fatalf("state after reset = %+v", h)
	}

	// 空历史不进入导航
	if _, ok := h.prev(nil, "draft"); ok {
		t.Fatal("prev without entries should not hit")
	}
	if h.index != 0 || h.stash != "" {
		t.Fatalf("state after empty prev = %+v", h)
	}
}
