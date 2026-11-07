package remote

import (
	"testing"

	"mcode/internal/remote/wire"
)

func replyBoolPtr(v bool) *bool { return &v }

func replyStringPtr(v string) *string { return &v }

// TestLastAssistantReplyText 验证子代理汇报优先取最终回复，跳过推理消息。
func TestLastAssistantReplyText(t *testing.T) {
	cases := []struct {
		name string
		msgs []wire.ThreadMessage
		want string
	}{
		{
			name: "最终回复后跟推理",
			msgs: []wire.ThreadMessage{
				{Role: "assistant", Text: "已完成迁移", IsFinalResponse: replyBoolPtr(true)},
				{Role: "assistant", Kind: replyStringPtr("reasoning"), Text: "Analyzing screenshot scaling discrepancy"},
			},
			want: "已完成迁移",
		},
		{
			name: "只有推理消息",
			msgs: []wire.ThreadMessage{
				{Role: "assistant", Kind: replyStringPtr("reasoning"), Text: "先看看仓库状态"},
				{Role: "assistant", Kind: replyStringPtr("thinking"), Text: "再核对测试入口"},
			},
			want: "",
		},
		{
			name: "多轮取最后一轮最终回复",
			msgs: []wire.ThreadMessage{
				{Role: "assistant", Text: "第一轮完成", IsFinalResponse: replyBoolPtr(true)},
				{Role: "assistant", Kind: replyStringPtr("reasoning"), Text: "第二轮先思考"},
				{Role: "assistant", Text: "第二轮完成", IsFinalResponse: replyBoolPtr(true)},
			},
			want: "第二轮完成",
		},
		{
			name: "没有 IsFinal 标记时退回最后一条正文",
			msgs: []wire.ThreadMessage{
				{Role: "assistant", Text: "旧的正文"},
				{Role: "assistant", Text: "新的正文"},
				{Role: "assistant", Kind: replyStringPtr("reasoning"), Text: "推理"},
			},
			want: "新的正文",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lastAssistantReplyText(tc.msgs); got != tc.want {
				t.Fatalf("lastAssistantReplyText() = %q, want %q", got, tc.want)
			}
		})
	}
}
