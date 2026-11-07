package mcodeui

import (
	"fmt"
	"strings"
	"testing"
)

// toolOutputTestText 生成 n 行、每行形如 L01 的测试文本。
func toolOutputTestText(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("L%02d", i+1)
	}
	return strings.Join(lines, "\n")
}

// TestToolOutputPreview_Head 验证头部模式：不足或刚好 limit 行不截断，超限只保留前 limit 行。
func TestToolOutputPreview_Head(t *testing.T) {
	cases := []struct {
		lines     int
		truncated bool
		preview   string
	}{
		{lines: 0, truncated: false, preview: ""},
		{lines: 1, truncated: false, preview: "L01"},
		{lines: 29, truncated: false, preview: toolOutputTestText(29)},
		{lines: 30, truncated: false, preview: toolOutputTestText(30)},
		{lines: 31, truncated: true, preview: toolOutputTestText(30)},
		{lines: 100, truncated: true, preview: toolOutputTestText(30)},
	}
	for _, tc := range cases {
		text := toolOutputTestText(tc.lines)
		preview, omitted, truncated := toolOutputPreview(text, toolPreviewHead, 30)
		if preview != tc.preview {
			t.Errorf("%d 行头部预览 = %q, want %q", tc.lines, preview, tc.preview)
		}
		if omitted != 0 {
			t.Errorf("%d 行头部预览 omitted = %d, want 0", tc.lines, omitted)
		}
		if truncated != tc.truncated {
			t.Errorf("%d 行头部预览 truncated = %v, want %v", tc.lines, truncated, tc.truncated)
		}
	}
}

// TestToolOutputPreview_Tail 验证尾部模式：超限保留后 limit 行，并给出前面省略的行数。
func TestToolOutputPreview_Tail(t *testing.T) {
	cases := []struct {
		lines   int
		omitted int
		first   string
		last    string
	}{
		{lines: 0, omitted: 0, first: "", last: ""},
		{lines: 1, omitted: 0, first: "L01", last: "L01"},
		{lines: 29, omitted: 0, first: "L01", last: "L29"},
		{lines: 30, omitted: 0, first: "L01", last: "L30"},
		{lines: 31, omitted: 1, first: "L02", last: "L31"},
		{lines: 100, omitted: 70, first: "L71", last: "L100"},
	}
	for _, tc := range cases {
		text := toolOutputTestText(tc.lines)
		preview, omitted, truncated := toolOutputPreview(text, toolPreviewTail, 30)
		if omitted != tc.omitted {
			t.Errorf("%d 行尾部预览 omitted = %d, want %d", tc.lines, omitted, tc.omitted)
		}
		wantTruncated := tc.omitted > 0
		if truncated != wantTruncated {
			t.Errorf("%d 行尾部预览 truncated = %v, want %v", tc.lines, truncated, wantTruncated)
		}
		if tc.lines == 0 {
			if preview != "" {
				t.Errorf("0 行尾部预览 = %q, want 空串", preview)
			}
			continue
		}
		gotLines := strings.Split(preview, "\n")
		if gotLines[0] != tc.first {
			t.Errorf("%d 行尾部预览首行 = %q, want %q", tc.lines, gotLines[0], tc.first)
		}
		if gotLines[len(gotLines)-1] != tc.last {
			t.Errorf("%d 行尾部预览末行 = %q, want %q", tc.lines, gotLines[len(gotLines)-1], tc.last)
		}
	}
}

// TestToolOutputPreview_LimitNonPositive 验证 limit 非正数时不截断，原样返回。
func TestToolOutputPreview_LimitNonPositive(t *testing.T) {
	text := toolOutputTestText(100)
	for _, limit := range []int{0, -1} {
		for _, mode := range []toolOutputPreviewMode{toolPreviewHead, toolPreviewTail} {
			preview, omitted, truncated := toolOutputPreview(text, mode, limit)
			if preview != text {
				t.Errorf("limit=%d mode=%d preview 应原样返回", limit, mode)
			}
			if omitted != 0 || truncated {
				t.Errorf("limit=%d mode=%d omitted=%d truncated=%v, want 0 false", limit, mode, omitted, truncated)
			}
		}
	}
}

// TestToolOutputPreview_TrailingNewline 验证末尾换行按 strings.Split 计行，与现状口径一致。
func TestToolOutputPreview_TrailingNewline(t *testing.T) {
	const text = "a\nb\n"

	head, headOmitted, headTruncated := toolOutputPreview(text, toolPreviewHead, 2)
	if head != "a\nb" || headOmitted != 0 || !headTruncated {
		t.Errorf("头部预览 = (%q, %d, %v), want (\"a\\nb\", 0, true)", head, headOmitted, headTruncated)
	}

	tail, tailOmitted, tailTruncated := toolOutputPreview(text, toolPreviewTail, 2)
	if tail != "b\n" || tailOmitted != 1 || !tailTruncated {
		t.Errorf("尾部预览 = (%q, %d, %v), want (\"b\\n\", 1, true)", tail, tailOmitted, tailTruncated)
	}
}

// TestToolOutputPrefersTail 验证各类工具默认看尾部还是头部。
func TestToolOutputPrefersTail(t *testing.T) {
	cases := []struct {
		m    Message
		tail bool
	}{
		{m: Message{ID: "c1", Role: "tool", Kind: "command", Text: "ls"}, tail: true},
		{m: Message{ID: "c2", Role: "tool", ToolName: "run_command", Text: "ls"}, tail: true},
		{m: Message{ID: "c3", Role: "tool", ToolName: "bash", Text: "ls"}, tail: true},
		{m: Message{ID: "r1", Role: "tool", ToolName: "read", Text: "a.go"}, tail: false},
		{m: Message{ID: "r2", Role: "tool", ToolName: "read_file", Text: "a.go"}, tail: false},
		{m: Message{ID: "s1", Role: "tool", ToolName: "grep", Text: "hit"}, tail: false},
		{m: Message{ID: "s2", Role: "tool", ToolName: "glob", Text: "a.go"}, tail: false},
		{m: Message{ID: "e1", Role: "tool", ToolName: "edit", Text: "ok"}, tail: true},
		{m: Message{ID: "e2", Role: "tool", ToolName: "write", Text: "ok"}, tail: true},
		{m: Message{ID: "m1", Role: "tool", ToolName: "mcp__foo__bar", Text: "ok"}, tail: true},
	}
	for _, tc := range cases {
		if got := toolOutputPrefersTail(tc.m); got != tc.tail {
			t.Errorf("%s prefersTail = %v, want %v", tc.m.ToolName, got, tc.tail)
		}
		wantMode := toolPreviewHead
		if tc.tail {
			wantMode = toolPreviewTail
		}
		if got := toolOutputPreviewModeFor(tc.m); got != wantMode {
			t.Errorf("%s previewMode = %v, want %v", tc.m.ToolName, got, wantMode)
		}
	}
}
