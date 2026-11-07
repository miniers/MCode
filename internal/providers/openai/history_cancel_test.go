package openai

import (
	"reflect"
	"testing"
)

// TestCloseDanglingToolCallsFillsMissingResult 验证取消后缺失的工具结果会被按原顺序补齐。
func TestCloseDanglingToolCallsFillsMissingResult(t *testing.T) {
	history := []chatMessage{
		{Role: "user", Content: "读两个文件"},
		{
			Role:    "assistant",
			Content: "",
			ToolCalls: []chatOutgoingCall{
				{ID: "call_1", Type: "function", Function: chatOutgoingFunc{Name: "read_file", Arguments: "{}"}},
				{ID: "call_2", Type: "function", Function: chatOutgoingFunc{Name: "read_file", Arguments: "{}"}},
			},
		},
		{Role: "tool", ToolCallID: "call_1", Content: "a.txt 内容"},
	}

	got := closeDanglingToolCalls(history)
	if len(got) != 4 {
		t.Fatalf("期望补齐后 4 条消息，实际 %d 条：%#v", len(got), got)
	}
	last := got[3]
	if last.Role != "tool" {
		t.Fatalf("补出的消息角色应为 tool，实际 %q", last.Role)
	}
	if last.ToolCallID != "call_2" {
		t.Fatalf("补出的消息应带 call_2，实际 %q", last.ToolCallID)
	}
	if last.Content != cancelledToolCallContent {
		t.Fatalf("补出的消息内容应为取消说明，实际 %v", last.Content)
	}
}

// TestCloseDanglingToolCallsNoop 验证没有悬空调用时历史原样返回。
func TestCloseDanglingToolCallsNoop(t *testing.T) {
	history := []chatMessage{
		{Role: "user", Content: "你好"},
		{
			Role:      "assistant",
			ToolCalls: []chatOutgoingCall{{ID: "call_1", Type: "function"}},
		},
		{Role: "tool", ToolCallID: "call_1", Content: "结果"},
		{Role: "assistant", Content: "完成"},
	}

	want := append([]chatMessage(nil), history...)
	got := closeDanglingToolCalls(history)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("无悬空调用时历史应保持不变：\n got=%#v\nwant=%#v", got, want)
	}
}
