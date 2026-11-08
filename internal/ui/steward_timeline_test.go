package mcodeui

import (
	"strings"
	"testing"
)

// TestReasoningHeadline 验证单行思考摘要：首行、末行、Markdown 符号与超长截断。
func TestReasoningHeadline(t *testing.T) {
	text := "先看看板任务\n再动手改代码"
	if got, want := reasoningHeadline(text, false), "先看看板任务"; got != want {
		t.Fatalf("首行摘要 = %q, want %q", got, want)
	}
	if got, want := reasoningHeadline(text, true), "再动手改代码"; got != want {
		t.Fatalf("末行摘要 = %q, want %q", got, want)
	}

	if got, want := reasoningHeadline("> - **要点一**", false), "要点一"; got != want {
		t.Fatalf("多层 Markdown 标记应被剥掉，got %q want %q", got, want)
	}
	md := "# 标题\n\n- **要点一**\n- 要点二"
	if got, want := reasoningHeadline(md, false), "标题"; got != want {
		t.Fatalf("Markdown 符号应被剥掉，got %q, want %q", got, want)
	}
	if got, want := reasoningHeadline(md, true), "要点二"; got != want {
		t.Fatalf("末行摘要应剥掉 Markdown 符号，got %q, want %q", got, want)
	}

	trailing := "先想一想\n\n   \n"
	if got, want := reasoningHeadline(trailing, true), "先想一想"; got != want {
		t.Fatalf("末行跳过空行后 = %q, want %q", got, want)
	}

	long := strings.Repeat("长", 80)
	got := reasoningHeadline(long, false)
	if len([]rune(got)) != 61 || !strings.HasSuffix(got, "…") {
		t.Fatalf("超长摘要应截断到 60 个 rune 并补省略号，实际 %d 个字符: %q", len([]rune(got)), got)
	}

	for _, empty := range []string{"", "  \n\n", "###\n**"} {
		if got := reasoningHeadline(empty, false); got != "" {
			t.Fatalf("空文本 %q 应返回空串，实际 %q", empty, got)
		}
	}
}

// TestReasoningHoldOpen 验证「刚结束流式输出」时是否暂时保持展开的全部分支。
func TestReasoningHoldOpen(t *testing.T) {
	cases := []struct {
		name         string
		wasLive      bool
		autoCollapse bool
		atEnd        bool
		wantHold     bool
		wantForget   bool
	}{
		{"从未流式", false, true, true, false, false},
		{"从未流式且关闭自动折叠", false, false, false, false, false},
		{"自动折叠且用户不在底部", true, true, false, true, false},
		{"自动折叠且用户在底部", true, true, true, false, true},
		{"关闭自动折叠", true, false, true, false, true},
	}
	for _, tc := range cases {
		hold, forget := reasoningHoldOpen(tc.wasLive, tc.autoCollapse, tc.atEnd)
		if hold != tc.wantHold || forget != tc.wantForget {
			t.Fatalf("[%s] reasoningHoldOpen(%v, %v, %v) = (%v, %v), want (%v, %v)",
				tc.name, tc.wasLive, tc.autoCollapse, tc.atEnd, hold, forget, tc.wantHold, tc.wantForget)
		}
	}
}
