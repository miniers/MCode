package claude

import (
	"strings"
	"testing"

	"mcode/internal/harness"
)

// streamedText 按事件顺序还原消费方看到的正文：EventText 追加，EventTextRetract 从末尾裁掉。
func streamedText(t *testing.T, events []harness.Event) string {
	t.Helper()
	var text string
	for _, e := range events {
		switch e.Type {
		case harness.EventText:
			text += e.Text
		case harness.EventTextRetract:
			rest, ok := strings.CutSuffix(text, e.Text)
			if !ok {
				t.Fatalf("retract %q is not the tail of %q", e.Text, text)
			}
			text = rest
		}
	}
	return text
}

// TestParser_RetractUnconfirmedTextAfterCutStream 复现流中断：第一次请求流出半截正文后，CLI 只拿到
// 思考块（没有对应的 assistant 正文块），随后用续写提示重新请求，模型从头重写。半截正文必须撤回。
func TestParser_RetractUnconfirmedTextAfterCutStream(t *testing.T) {
	events := feedParserLines(t,
		`{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":2}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"想一想"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"想一想"}]}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"text"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"# 第一版"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"，写到一半「"}}}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":2}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"# 重写版"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"# 重写版"}]}}`,
		`{"type":"result","subtype":"success"}`,
	)

	var retracts []string
	for _, e := range events {
		if e.Type == harness.EventTextRetract {
			retracts = append(retracts, e.Text)
		}
	}
	if len(retracts) != 1 || retracts[0] != "# 第一版，写到一半「" {
		t.Fatalf("retracts = %q, want exactly the unconfirmed first draft", retracts)
	}
	if got := streamedText(t, events); got != "# 重写版" {
		t.Errorf("final text = %q, want %q", got, "# 重写版")
	}
}

// TestParser_NoRetractForConfirmedText 校验正常流程：正文被 assistant 完整块确认后，工具调用后的
// 下一次请求不撤回任何正文。
func TestParser_NoRetractForConfirmedText(t *testing.T) {
	events := feedParserLines(t,
		`{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":2}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"先看"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"一下"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"先看一下"}]}}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":2}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"。结论"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"。结论"}]}}`,
		`{"type":"result","subtype":"success"}`,
	)
	for _, e := range events {
		if e.Type == harness.EventTextRetract {
			t.Fatalf("unexpected retract %q", e.Text)
		}
	}
	if got := streamedText(t, events); got != "先看一下。结论" {
		t.Errorf("final text = %q", got)
	}
}
