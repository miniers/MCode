package mcodeui

import (
	"strings"
	"testing"
	"time"
)

// TestIsLongPastedText 覆盖行数与字数两条阈值的边界值。
func TestIsLongPastedText(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"空文本", "", false},
		{"19 行", strings.Repeat("a\n", 18) + "a", false},
		{"20 行", strings.Repeat("a\n", 19) + "a", true},
		{"21 行", strings.Repeat("a\n", 20) + "a", true},
		{"1499 字", strings.Repeat("字", 1499), false},
		{"1500 字", strings.Repeat("字", 1500), true},
		{"短文本", "hello world", false},
	}
	for _, tc := range tests {
		if got := isLongPastedText(tc.text); got != tc.want {
			t.Fatalf("%s: isLongPastedText = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// longPaste 返回一段达到行数阈值的长文本，首行是 first。
func longPaste(first string) string {
	return first + "\n" + strings.Repeat("line\n", pastedTextMinLines)
}

// TestPasteLongTextBecomesSnippet 覆盖长文本收成卡片、不进输入框，短文本交给输入框。
func TestPasteLongTextBecomesSnippet(t *testing.T) {
	s := &chatState{selectedThreadID: "t1"}
	s.draft = "已输入"
	if s.pasteLongText("短文本") {
		t.Fatal("短文本不应被吞掉")
	}
	text := longPaste("第一行")
	if !s.pasteLongText(text) {
		t.Fatal("长文本应被收成卡片")
	}
	if s.draft != "已输入" {
		t.Fatalf("长文本不应写进输入框，draft = %q", s.draft)
	}
	if len(s.snippets) != 1 || s.snippets[0].Text != text || s.snippets[0].ID != 1 {
		t.Fatalf("snippets = %+v", s.snippets)
	}
	if got := s.drafts["t1"].Snippets; len(got) != 1 {
		t.Fatalf("卡片应同步进会话草稿，got %+v", got)
	}
}

// TestSnippetTitle 覆盖标题取第一行非空文本、压缩空白与截断。
func TestSnippetTitle(t *testing.T) {
	if got := snippetTitle("\n  \n  func   main() {\nbody"); got != "func main() {" {
		t.Fatalf("snippetTitle = %q", got)
	}
	if got := snippetTitle(" \n\t"); got != "" {
		t.Fatalf("全空白标题应为空，got %q", got)
	}
	long := strings.Repeat("长", snippetTitleMaxRunes+5)
	if got := snippetChipTitle(long); []rune(got)[snippetTitleMaxRunes-1] != '…' || len([]rune(got)) != snippetTitleMaxRunes {
		t.Fatalf("snippetChipTitle = %q", got)
	}
}

// TestComposeWithSnippets 覆盖正文在前、卡片按顺序空一行接在后面，以及没有卡片时原样返回。
func TestComposeWithSnippets(t *testing.T) {
	snippets := []composerSnippet{{ID: 1, Text: "AAA"}, {ID: 2, Text: "BBB"}}
	if got := composeWithSnippets("  看看这个 \n", snippets); got != "看看这个\n\nAAA\n\nBBB" {
		t.Fatalf("compose = %q", got)
	}
	if got := composeWithSnippets("", snippets); got != "AAA\n\nBBB" {
		t.Fatalf("空正文 compose = %q", got)
	}
	if got := composeWithSnippets(" 原样 ", nil); got != " 原样 " {
		t.Fatalf("没有卡片应原样返回，got %q", got)
	}
}

// TestRemoveAndExpandSnippet 覆盖移除卡片、把卡片原文放回输入框末尾并请求焦点与光标到文末。
func TestRemoveAndExpandSnippet(t *testing.T) {
	s := &chatState{selectedThreadID: "t1"}
	s.pasteLongText(longPaste("一"))
	s.pasteLongText(longPaste("二"))
	first, second := s.snippets[0], s.snippets[1]

	s.snippetPreviewID = first.ID
	s.removeSnippet(first.ID)
	if len(s.snippets) != 1 || s.snippets[0].ID != second.ID || s.snippetPreviewID != 0 {
		t.Fatalf("移除后 snippets = %+v，预览 %d", s.snippets, s.snippetPreviewID)
	}

	s.draft = "前言\n"
	s.expandSnippet(second.ID)
	if len(s.snippets) != 0 {
		t.Fatalf("放回输入框后卡片应移除，got %+v", s.snippets)
	}
	if s.pendingDraftFill == nil || *s.pendingDraftFill != "前言\n\n"+second.Text {
		t.Fatalf("pendingDraftFill = %v", s.pendingDraftFill)
	}
	if !s.completionCaretToEnd || !s.composerFocusRequest {
		t.Fatal("放回输入框后应把光标放到文末并要回焦点")
	}
	if _, ok := s.drafts["t1"]; ok {
		t.Fatal("卡片清空且草稿为空时应删除草稿条目")
	}
}

// TestSnippetDraftLifecycle 覆盖切换会话时卡片随草稿暂存、取回，以及落盘时卡片并入文本。
func TestSnippetDraftLifecycle(t *testing.T) {
	s := &chatState{}
	snippets := []composerSnippet{{ID: 7, Text: "粘贴原文"}}
	s.stashDraftLocked("t1", "", nil, snippets)
	snippets[0].Text = "被改写"
	d := s.takeDraftLocked("t1")
	if len(d.Snippets) != 1 || d.Snippets[0].Text != "粘贴原文" {
		t.Fatalf("只有卡片的草稿也应保留并深拷贝，got %+v", d.Snippets)
	}
	s.stashDraftLocked("t2", "正文", nil, []composerSnippet{{ID: 8, Text: "附带"}})
	texts := s.snapshotDraftTextsLocked()
	if texts["t1"] != "粘贴原文" || texts["t2"] != "正文\n\n附带" {
		t.Fatalf("落盘文本 = %+v", texts)
	}
}

// TestSendMessageIncludesSnippets 覆盖发送时卡片接在正文后面发出，成功后卡片清空。
func TestSendMessageIncludesSnippets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)

	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1"}}
	s.mu.Unlock()
	text := longPaste("日志")
	s.pasteLongText(text)

	// 只有卡片、正文为空也能发送。
	s.sendMessage("")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		done := fake.SendCalls() > 0 && !s.sending
		left := len(s.snippets)
		s.mu.Unlock()
		if done {
			if got := fake.LastSentText(); got != text {
				t.Fatalf("sent = %q, want %q", got, text)
			}
			if left != 0 {
				t.Fatalf("发送成功后卡片应清空，剩 %d 张", left)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for send")
}
