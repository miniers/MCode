package claude

import (
	"testing"

	"mcode/internal/harness"
)

// TestParser_ToolEndBlocked 校验 tool_result 的拦截标记解析：
// 结构化 tool_result_meta 优先，老版本没有该字段时回退到文本前缀。
func TestParser_ToolEndBlocked(t *testing.T) {
	cases := []struct {
		name        string
		line        string
		wantIsError bool
		wantBlocked bool
	}{
		{
			name: "带 tool_result_meta 的 hook 拦截",
			line: `{"type":"user","parent_tool_use_id":null,"session_id":"s1","uuid":"u1",` +
				`"message":{"content":[{"type":"tool_result","tool_use_id":"tool_call_1","content":"hook rule denied this tool call","is_error":true}]},` +
				`"tool_result_meta":[{"id":"tool_call_1","non_execution_kind":"permission-rule","permission_decision":{"decision":"reject","source":"hook","reason_type":"hook"}}]}`,
			wantIsError: true,
			wantBlocked: true,
		},
		{
			name:        "老版本没有 meta 时的文本前缀兜底",
			line:        `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool_call_2","content":"PreToolUse:Bash hook error: x","is_error":true}]}}`,
			wantIsError: true,
			wantBlocked: true,
		},
		{
			name: "普通工具失败不算拦截",
			line: `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool_call_3","content":"Exit code 1","is_error":true}]},` +
				`"tool_result_meta":[{"id":"tool_call_3","permission_decision":{"decision":"accept","source":"config"}}]}`,
			wantIsError: true,
			wantBlocked: false,
		},
		{
			name:        "正常结果不算拦截",
			line:        `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool_call_4","content":"file1\nfile2","is_error":false}]}}`,
			wantIsError: false,
			wantBlocked: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newParser()
			var events []harness.Event
			emit := func(e harness.Event) {
				events = append(events, e)
			}

			if err := p.handleLine([]byte(tc.line), emit); err != nil {
				t.Fatalf("handleLine error: %v", err)
			}

			if len(events) != 1 {
				t.Fatalf("expected 1 event, got %d: %+v", len(events), events)
			}
			ev := events[0]
			if ev.Type != harness.EventToolEnd {
				t.Fatalf("expected tool_end event, got %v", ev.Type)
			}
			if ev.IsError != tc.wantIsError {
				t.Errorf("IsError = %v, want %v", ev.IsError, tc.wantIsError)
			}
			if ev.Blocked != tc.wantBlocked {
				t.Errorf("Blocked = %v, want %v", ev.Blocked, tc.wantBlocked)
			}
		})
	}
}
