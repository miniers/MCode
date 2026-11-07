package mcodeui

import (
	"strings"
	"testing"
)

// TestErrorSummaryLine 验证错误行摘要只取首行、压缩空白，并在超过字数上限时截断加省略号。
func TestErrorSummaryLine(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"空文本", "", ""},
		{"只有空白", "  \n\t\n ", ""},
		{"单行去首尾空白", "  boom  ", "boom"},
		{"多行只取首行", "第一行\n第二行", "第一行"},
		{"行尾换行不算多行", "boom\n", "boom"},
		{"行内空白压成一个空格", "a   b\tc", "a b c"},
		{"正好上限不截断", strings.Repeat("错", errorSummaryMaxRunes), strings.Repeat("错", errorSummaryMaxRunes)},
		{"超过上限截断", strings.Repeat("错", errorSummaryMaxRunes+1), strings.Repeat("错", errorSummaryMaxRunes) + "…"},
	}
	for _, tc := range cases {
		if got := errorSummaryLine(tc.text); got != tc.want {
			t.Fatalf("%s: errorSummaryLine(%q) = %q, want %q", tc.name, tc.text, got, tc.want)
		}
	}
}

// TestErrorNeedsDetailToggle 验证「详情」开关的判定：超过一行或超过字数上限时才显示。
func TestErrorNeedsDetailToggle(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"空文本", "", false},
		{"单行短文本", "连接失败", false},
		{"单行末尾换行", "连接失败\n", false},
		{"多行文本", "连接失败\n第二行细节", true},
		{"首行为空、实际只有一行", "\n第二行细节", false},
		{"正好上限", strings.Repeat("错", errorSummaryMaxRunes), false},
		{"超过上限", strings.Repeat("错", errorSummaryMaxRunes+1), true},
	}
	for _, tc := range cases {
		if got := errorNeedsDetailToggle(tc.text); got != tc.want {
			t.Fatalf("%s: errorNeedsDetailToggle(%q) = %v, want %v", tc.name, tc.text, got, tc.want)
		}
	}
}

// TestErrorIsLastMessage 验证「这条错误是否是会话最后一条消息」的判定。
func TestErrorIsLastMessage(t *testing.T) {
	messages := []Message{
		{ID: "u1", Role: "user", Text: "跑一下"},
		{ID: "e1", Role: "error", Text: "失败了"},
	}
	if !errorIsLastMessage(messages, messages[1]) {
		t.Fatalf("末条错误应判定为最后一条消息")
	}
	if errorIsLastMessage(messages, messages[0]) {
		t.Fatalf("非末条消息不应判定为最后一条")
	}
	if errorIsLastMessage(nil, messages[1]) {
		t.Fatalf("空消息列表不应判定为最后一条")
	}
	if errorIsLastMessage(messages, Message{Role: "error", Text: "无 ID"}) {
		t.Fatalf("没有 ID 的消息不应判定为最后一条")
	}
}

// TestErrorRetryVisible 验证「重试」只在这条错误仍是最后一条消息且线程没有运行时显示。
func TestErrorRetryVisible(t *testing.T) {
	cases := []struct {
		isLast  bool
		running bool
		want    bool
	}{
		{true, false, true},
		{true, true, false},
		{false, false, false},
		{false, true, false},
	}
	for _, tc := range cases {
		if got := errorRetryVisible(tc.isLast, tc.running); got != tc.want {
			t.Fatalf("errorRetryVisible(%v, %v) = %v, want %v", tc.isLast, tc.running, got, tc.want)
		}
	}
}

// TestErrorRetryPrompt 验证重试取的是错误之前最近一条非空用户消息的正文。
func TestErrorRetryPrompt(t *testing.T) {
	messages := []Message{
		{ID: "u1", Role: "user", Text: "第一句"},
		{ID: "a1", Role: "assistant", Text: "好的"},
		{ID: "u2", Role: "user", Text: "  "},
		{ID: "u3", Role: "user", Text: "重新跑这个命令"},
		{ID: "a2", Role: "assistant", Text: "在跑"},
		{ID: "e1", Role: "error", Text: "失败了"},
	}
	if got := errorRetryPrompt(messages, "e1"); got != "重新跑这个命令" {
		t.Fatalf("应取最近一条非空用户消息，got %q", got)
	}
	// 错误不在列表里时从末尾往前找，同样跳过助手消息与空白用户消息
	if got := errorRetryPrompt(messages[:5], "e-missing"); got != "重新跑这个命令" {
		t.Fatalf("错误不在列表时应从末尾找上一条用户消息，got %q", got)
	}
	if got := errorRetryPrompt([]Message{{ID: "a1", Role: "assistant", Text: "只有助手"}}, "e1"); got != "" {
		t.Fatalf("没有用户消息时应返回空，got %q", got)
	}
	if got := errorRetryPrompt(nil, "e1"); got != "" {
		t.Fatalf("空列表应返回空，got %q", got)
	}
	// 只带附件、没有正文的用户消息：正文部分为空，附件由 errorRetryTargetFor 负责取回
	withMedia := []Message{
		{ID: "u1", Role: "user", Text: "第一句"},
		{ID: "u2", Role: "user", Media: []MessageMedia{{ID: "m1", Name: "shot.png", MimeType: "image/png"}}},
		{ID: "e1", Role: "error", Text: "失败了"},
	}
	if got := errorRetryPrompt(withMedia, "e1"); got != "" {
		t.Fatalf("只有附件的用户消息正文为空应返回空，got %q", got)
	}
}
