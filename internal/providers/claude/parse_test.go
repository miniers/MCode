package claude

import (
	"reflect"
	"testing"
	"time"

	"mcode/internal/harness"
)

// feedParserLines 用新 parser 依次喂入给定的 stream-json 行，返回收集到的事件。
func feedParserLines(t *testing.T, lines ...string) []harness.Event {
	t.Helper()
	p := newParser()
	var events []harness.Event
	emit := func(e harness.Event) {
		events = append(events, e)
	}
	for _, l := range lines {
		if err := p.handleLine([]byte(l), emit); err != nil {
			t.Fatalf("handleLine error: %v", err)
		}
	}
	return events
}

// pickUsage 取出唯一一条 usage 事件：没有上报或流式途中多发都算失败（消费方把 usage 当整轮用量）。
func pickUsage(t *testing.T, events []harness.Event) harness.Usage {
	t.Helper()
	var got []harness.Usage
	for _, e := range events {
		if e.Type != harness.EventUsage {
			continue
		}
		if e.Usage == nil {
			t.Fatalf("usage event carries no payload: %+v", e)
		}
		got = append(got, *e.Usage)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 usage event, got %d: %+v", len(got), events)
	}
	return got[0]
}

// TestParser_ContextTokensFromLastRequest 校验上下文占用取本轮最后一次顶层请求的四项之和，
// 而 InputTokens/OutputTokens 仍是 result.usage 的整轮累计。
func TestParser_ContextTokensFromLastRequest(t *testing.T) {
	events := feedParserLines(t,
		`{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":1000,"cache_read_input_tokens":20000,"cache_creation_input_tokens":500,"output_tokens":1}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}}`,
		`{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":320}}}`,
		`{"type":"assistant","message":{"usage":{"input_tokens":1000,"cache_read_input_tokens":20000,"cache_creation_input_tokens":500,"output_tokens":340},"content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"result","subtype":"success","usage":{"input_tokens":2500,"output_tokens":900,"cache_read_input_tokens":40000}}`,
	)

	usage := pickUsage(t, events)
	// assistant 完整消息覆盖了 message_start + message_delta：1000 + 20000 + 500 + 340。
	if want := int64(1000 + 20000 + 500 + 340); usage.ContextTokens != want {
		t.Errorf("ContextTokens = %d, want %d", usage.ContextTokens, want)
	}
	if usage.InputTokens != 2500 || usage.OutputTokens != 900 || usage.CachedTokens != 40000 {
		t.Errorf("累计用量应取自 result.usage, got %+v", usage)
	}
}

// TestParser_ContextTokensMessageDelta 校验 message_delta 刷新输出数，字段缺失时沿用上一份快照。
func TestParser_ContextTokensMessageDelta(t *testing.T) {
	cases := []struct {
		name  string
		delta string
		want  int64
	}{
		{
			name:  "带 output_tokens 时刷新输出",
			delta: `{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":200}}}`,
			want:  800 + 100 + 200,
		},
		{
			name:  "缺 output_tokens 时沿用上一份快照",
			delta: `{"type":"stream_event","event":{"type":"message_delta"}}`,
			want:  800 + 100 + 7,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := feedParserLines(t,
				`{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":800,"cache_read_input_tokens":100,"output_tokens":7}}}}`,
				tc.delta,
				`{"type":"result","subtype":"success","usage":{"input_tokens":800,"output_tokens":7}}`,
			)
			if got := pickUsage(t, events).ContextTokens; got != tc.want {
				t.Errorf("ContextTokens = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestParser_ContextTokensIgnoresSubagent 校验子代理消息的 usage 不参与本轮上下文快照。
func TestParser_ContextTokensIgnoresSubagent(t *testing.T) {
	events := feedParserLines(t,
		`{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":1000,"output_tokens":10}}}}`,
		`{"type":"stream_event","parent_tool_use_id":"toolu_1","event":{"type":"message_start","message":{"usage":{"input_tokens":500000,"output_tokens":1}}}}`,
		`{"type":"stream_event","event":{"type":"message_delta","parent_tool_use_id":"toolu_1","usage":{"output_tokens":99999}}}`,
		`{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"usage":{"input_tokens":500000,"output_tokens":120000},"content":[{"type":"text","text":"子代理正文"}]}}`,
		`{"type":"result","subtype":"success","usage":{"input_tokens":1000,"output_tokens":20}}`,
	)

	if got := pickUsage(t, events).ContextTokens; got != 1000+10 {
		t.Errorf("ContextTokens = %d, want %d（只算顶层请求）", got, 1000+10)
	}
}

// TestParser_ContextTokensAfterCompact 校验压缩边界把 compact_metadata.post_tokens 记为已用上下文，
// 之后到来的新请求会覆盖它。
func TestParser_ContextTokensAfterCompact(t *testing.T) {
	t.Run("压缩后没有新请求", func(t *testing.T) {
		events := feedParserLines(t,
			`{"type":"system","subtype":"compact_boundary","session_id":"s1","compact_metadata":{"trigger":"auto","pre_tokens":180000,"post_tokens":12000}}`,
			`{"type":"result","subtype":"success","usage":{"input_tokens":180000,"output_tokens":900}}`,
		)
		if got := pickUsage(t, events).ContextTokens; got != 12000 {
			t.Errorf("ContextTokens = %d, want 12000", got)
		}
	})

	t.Run("压缩后又有新请求", func(t *testing.T) {
		events := feedParserLines(t,
			`{"type":"system","subtype":"compact_boundary","compact_metadata":{"post_tokens":12000}}`,
			`{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":3000,"output_tokens":1}}}}`,
			`{"type":"result","subtype":"success","usage":{"input_tokens":195000,"output_tokens":900}}`,
		)
		if got := pickUsage(t, events).ContextTokens; got != 3000+1 {
			t.Errorf("ContextTokens = %d, want %d（新请求覆盖压缩快照）", got, 3000+1)
		}
	})
}

// TestParser_ContextCompactedEvent 校验压缩边界发一条上下文压缩事件：主会话带出 pre_tokens 与
// post_tokens，子代理的压缩不进主会话时间线。
func TestParser_ContextCompactedEvent(t *testing.T) {
	compactedEvents := func(events []harness.Event) []harness.Event {
		var got []harness.Event
		for _, e := range events {
			if e.Type == harness.EventContextCompacted {
				got = append(got, e)
			}
		}
		return got
	}

	events := feedParserLines(t,
		`{"type":"system","subtype":"compact_boundary","session_id":"s1","compact_metadata":{"trigger":"auto","pre_tokens":180000,"post_tokens":12000}}`,
	)
	got := compactedEvents(events)
	if len(got) != 1 {
		t.Fatalf("主会话压缩应发一个上下文压缩事件，实际 %+v", got)
	}
	if got[0].CompactTokensBefore != 180000 || got[0].CompactTokensAfter != 12000 {
		t.Fatalf("压缩前后 token = %d → %d, want 180000 → 12000", got[0].CompactTokensBefore, got[0].CompactTokensAfter)
	}

	subEvents := feedParserLines(t,
		`{"type":"system","subtype":"compact_boundary","parent_tool_use_id":"toolu_1","compact_metadata":{"pre_tokens":9000,"post_tokens":3000}}`,
	)
	if got := compactedEvents(subEvents); len(got) != 0 {
		t.Fatalf("子代理的压缩不应发上下文压缩事件，实际 %+v", got)
	}
}

// TestParser_BackgroundTaskEvents 校验后台任务的两种系统帧：task_started 发开始事件（带 task_id、
// tool_use_id、description），task_notification 发结束事件（带原始 status）；is_backgrounded:false
// 与缺少 tool_use_id 的开始帧不发事件。
func TestParser_BackgroundTaskEvents(t *testing.T) {
	pickEvents := func(events []harness.Event, typ harness.EventType) []harness.Event {
		var got []harness.Event
		for _, e := range events {
			if e.Type == typ {
				got = append(got, e)
			}
		}
		return got
	}

	events := feedParserLines(t,
		`{"type":"system","subtype":"task_started","task_id":"task-1","tool_use_id":"toolu_bg_1","description":"sleep 5","is_backgrounded":true}`,
		`{"type":"system","subtype":"task_notification","task_id":"task-1","tool_use_id":"toolu_bg_1","status":"completed"}`,
	)
	starts := pickEvents(events, harness.EventBackgroundTaskStart)
	if len(starts) != 1 {
		t.Fatalf("task_started 应发一个后台任务开始事件，实际 %+v", starts)
	}
	if starts[0].ToolID != "toolu_bg_1" || starts[0].TaskID != "task-1" || starts[0].Text != "sleep 5" {
		t.Fatalf("后台任务开始事件字段不对: %+v", starts[0])
	}
	ends := pickEvents(events, harness.EventBackgroundTaskEnd)
	if len(ends) != 1 {
		t.Fatalf("task_notification 应发一个后台任务结束事件，实际 %+v", ends)
	}
	if ends[0].ToolID != "toolu_bg_1" || ends[0].TaskID != "task-1" || ends[0].TaskStatus != "completed" {
		t.Fatalf("后台任务结束事件字段不对: %+v", ends[0])
	}

	t.Run("is_backgrounded 为 false 不发开始事件", func(t *testing.T) {
		got := pickEvents(feedParserLines(t,
			`{"type":"system","subtype":"task_started","task_id":"task-2","tool_use_id":"toolu_bg_2","is_backgrounded":false}`,
		), harness.EventBackgroundTaskStart)
		if len(got) != 0 {
			t.Fatalf("非后台任务不应发开始事件，实际 %+v", got)
		}
	})

	t.Run("缺少 tool_use_id 仍发结束事件", func(t *testing.T) {
		got := feedParserLines(t,
			`{"type":"system","subtype":"task_started","task_id":"task-3","description":"sleep 5"}`,
			`{"type":"system","subtype":"task_notification","task_id":"task-3","status":"completed"}`,
		)
		if starts := pickEvents(got, harness.EventBackgroundTaskStart); len(starts) != 0 {
			t.Fatalf("缺少 tool_use_id 不应发开始事件，实际 %+v", starts)
		}
		if ends := pickEvents(got, harness.EventBackgroundTaskEnd); len(ends) != 1 || ends[0].ToolID != "" || ends[0].TaskID != "task-3" {
			t.Fatalf("缺少 tool_use_id 应按 task_id 发结束事件，实际 %+v", ends)
		}
	})
}

func TestParser_MCPBackgroundTasks(t *testing.T) {
	results := []string{
		`"MCP tool moved to the background as task kdxhm0uqv and keeps running"`,
		`[{"type":"text","text":"MCP tool moved to the background as task kdxhm0uqv."}]`,
	}
	notifications := []string{
		`"<system-reminder><task-notification>\n<task-id>kdxhm0uqv</task-id><status>completed</status></task-notification></system-reminder>"`,
		`[{"type":"text","text":"<task-notification><task-id>kdxhm0uqv</task-id><status>completed</status></task-notification>"}]`,
	}
	for i := range results {
		events := feedParserLines(t,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_mcp","content":`+results[i]+`}]}}`,
			`{"type":"user","message":{"content":`+notifications[i]+`}}`,
			`{"type":"user","message":{"content":`+notifications[i]+`}}`,
		)
		if len(events) != 4 {
			t.Fatalf("应发出工具结束、后台开始和两次后台结束，实际 %+v", events)
		}
		if events[0].Type != harness.EventToolEnd || events[0].ToolID != "toolu_mcp" {
			t.Fatalf("工具结束事件不对: %+v", events[0])
		}
		if events[1].Type != harness.EventBackgroundTaskStart || events[1].ToolID != "toolu_mcp" || events[1].TaskID != "kdxhm0uqv" {
			t.Fatalf("后台开始事件不对: %+v", events[1])
		}
		if events[2].Type != harness.EventBackgroundTaskEnd || events[2].ToolID != "toolu_mcp" || events[2].TaskID != "kdxhm0uqv" || events[2].TaskStatus != "completed" {
			t.Fatalf("后台结束事件不对: %+v", events[2])
		}
		if events[3].Type != harness.EventBackgroundTaskEnd || events[3].ToolID != "" || events[3].TaskID != "kdxhm0uqv" {
			t.Fatalf("重复通知应按 task_id 发结束事件: %+v", events[3])
		}
	}

	for _, tc := range []struct {
		name   string
		field  string
		toolID string
	}{
		{name: "跨轮", field: "", toolID: ""},
		{name: "通知包含工具 ID", field: "<tool-use-id>toolu_explicit</tool-use-id>", toolID: "toolu_explicit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := feedParserLines(t, `{"type":"user","message":{"content":"<task-notification><task-id>unknown</task-id>`+tc.field+`<status>completed</status></task-notification>"}}`)
			if len(events) != 1 || events[0].Type != harness.EventBackgroundTaskEnd || events[0].ToolID != tc.toolID || events[0].TaskID != "unknown" || events[0].TaskStatus != "completed" {
				t.Fatalf("跨轮通知事件不对: %+v", events)
			}
		})
	}
}

func TestParser_BackgroundTaskNotifications(t *testing.T) {
	events := feedParserLines(t,
		`{"type":"system","subtype":"task_started","task_id":"task-1","tool_use_id":"toolu_bg","is_backgrounded":true}`,
		`{"type":"system","subtype":"task_notification","task_id":"task-1","status":"failed"}`,
		`{"type":"user","message":{"content":"<task-notification><task-id>task-1</task-id><status>failed</status></task-notification><task-notification><task-id>unknown</task-id><status>completed</status></task-notification>"}}`,
	)
	if len(events) != 4 || events[0].Type != harness.EventBackgroundTaskStart || events[1].Type != harness.EventBackgroundTaskEnd || events[1].ToolID != "toolu_bg" || events[1].TaskID != "task-1" || events[1].TaskStatus != "failed" || events[2].ToolID != "" || events[2].TaskID != "task-1" || events[3].Type != harness.EventBackgroundTaskEnd || events[3].ToolID != "" || events[3].TaskID != "unknown" {
		t.Fatalf("后台任务通知事件不对，实际 %+v", events)
	}
}

func TestParser_SubagentMCPBackgroundTaskIgnored(t *testing.T) {
	events := feedParserLines(t,
		`{"type":"user","parent_tool_use_id":"parent","message":{"content":[{"type":"tool_result","tool_use_id":"child","content":"moved to the background as task abc123."}]}}`,
		`{"type":"user","message":{"content":"<task-notification><task-id>abc123</task-id><status>completed</status></task-notification>"}}`,
	)
	if len(events) != 2 || events[0].Type != harness.EventToolEnd || events[1].Type != harness.EventBackgroundTaskEnd || events[1].ToolID != "" || events[1].TaskID != "abc123" {
		t.Fatalf("子代理不应产生后台开始事件，实际 %+v", events)
	}
}

// TestParser_ResultModelUsageAndCost 校验 result.modelUsage 的上下文窗口（取最大值）与
// total_cost_usd 被带到 usage 上。
func TestParser_ResultModelUsageAndCost(t *testing.T) {
	events := feedParserLines(t,
		`{"type":"result","subtype":"success","total_cost_usd":0.0421,"usage":{"input_tokens":10,"output_tokens":5},`+
			`"modelUsage":{"claude-sonnet-4-5-20250929":{"contextWindow":200000,"costUSD":0.04},`+
			`"claude-haiku-4-5-20251001":{"contextWindow":1000000,"costUSD":0.0021}}}`,
	)

	usage := pickUsage(t, events)
	if usage.ContextWindow != 1000000 {
		t.Errorf("ContextWindow = %d, want 1000000（取 modelUsage 最大值）", usage.ContextWindow)
	}
	if usage.CostUSD != 0.0421 {
		t.Errorf("CostUSD = %v, want 0.0421", usage.CostUSD)
	}
}

// TestParser_ResultMissingUsageExtras 校验缺少 modelUsage / total_cost_usd / 请求快照时都留 0 且不 panic。
func TestParser_ResultMissingUsageExtras(t *testing.T) {
	usage := pickUsage(t, feedParserLines(t,
		`{"type":"result","subtype":"success","usage":{"input_tokens":10,"output_tokens":5}}`,
	))
	if usage.ContextTokens != 0 || usage.ContextWindow != 0 || usage.CostUSD != 0 {
		t.Errorf("缺失字段应留 0, got %+v", usage)
	}

	t.Run("result 没有 usage 时不上报用量", func(t *testing.T) {
		for _, e := range feedParserLines(t, `{"type":"result","subtype":"success"}`) {
			if e.Type == harness.EventUsage {
				t.Fatalf("unexpected usage event: %+v", e)
			}
		}
	})
}

// pickError 取出唯一的 error 事件：没有或多条都算失败（一轮结果最多一个失败事件）。
func pickError(t *testing.T, events []harness.Event) harness.Event {
	t.Helper()
	var got []harness.Event
	for _, e := range events {
		if e.Type == harness.EventError {
			got = append(got, e)
		}
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 error event, got %d: %+v", len(got), events)
	}
	return got[0]
}

// pickFileChanges 取出全部文件变更事件，便于断言补发的那条带 Patch。
func pickFileChanges(t *testing.T, events []harness.Event) []harness.Event {
	t.Helper()
	var got []harness.Event
	for _, e := range events {
		if e.Type == harness.EventFileChange {
			got = append(got, e)
		}
	}
	return got
}

// TestParser_AuthFailureSequence 校验认证失败序列：CLI 先插一条 <synthetic> assistant 消息并在顶层
// 带 error=authentication_failed，随后 result 的 subtype 仍是 success，只能靠 is_error 判定失败。
func TestParser_AuthFailureSequence(t *testing.T) {
	events := feedParserLines(t,
		`{"type":"assistant","message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"Not logged in · Please run /login"}]},"error":"authentication_failed","is_api_error_message":true}`,
		`{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":null,`+
			`"result":"Not logged in · Please run /login","num_turns":0,"duration_ms":12,"permission_denials":[]}`,
	)

	errEvent := pickError(t, events)
	if errEvent.ErrKind != harness.ErrKindAuthRequired {
		t.Errorf("ErrKind = %q, want %q", errEvent.ErrKind, harness.ErrKindAuthRequired)
	}
	// 错误文本保持原样，界面仍按供应商原文展示。
	if errEvent.Err != "Not logged in · Please run /login" {
		t.Errorf("Err = %q, want 原文", errEvent.Err)
	}
}

// TestParser_ErrorKindClassification 校验各类失败的结构化判定，顺序上 terminal_reason 与失败码优先，
// 状态码与文本关键词兜底，判不出来归 api_error。
func TestParser_ErrorKindClassification(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{
			name: "429 限流",
			lines: []string{
				`{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":429,"result":"Too Many Requests"}`,
			},
			want: harness.ErrKindRateLimited,
		},
		{
			name: "合成消息的 rate_limit 失败码",
			lines: []string{
				`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"Rate limited"}]},"error":"rate_limit","is_api_error_message":true}`,
				`{"type":"result","subtype":"success","is_error":true,"terminal_reason":"completed","result":"Rate limited"}`,
			},
			want: harness.ErrKindRateLimited,
		},
		{
			name: "提示词过长（状态码 413）",
			lines: []string{
				`{"type":"result","subtype":"error_during_execution","is_error":true,"api_error_status":413,"result":"request entity too large"}`,
			},
			want: harness.ErrKindContextExhausted,
		},
		{
			name: "提示词过长（文本兜底）",
			lines: []string{
				`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"Prompt is too long: 250000 tokens > 200000 maximum"}`,
			},
			want: harness.ErrKindContextExhausted,
		},
		{
			name: "上下文超限（terminal_reason）",
			lines: []string{
				`{"type":"result","subtype":"success","is_error":true,"terminal_reason":"prompt_too_long","result":"prompt is too long"}`,
			},
			want: harness.ErrKindContextExhausted,
		},
		{
			name: "自动压缩连续快速填充（上下文类）",
			lines: []string{
				`{"type":"result","subtype":"success","is_error":true,"terminal_reason":"rapid_refill_breaker","result":"autocompact thrashing"}`,
			},
			want: harness.ErrKindContextExhausted,
		},
		{
			name: "账号封禁的失败码",
			lines: []string{
				`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"account on hold"}]},"error":"account_on_hold","is_api_error_message":true}`,
				`{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","result":"account on hold"}`,
			},
			want: harness.ErrKindAuthRequired,
		},
		{
			name: "达到最大轮数（subtype）",
			lines: []string{
				`{"type":"result","subtype":"error_max_turns","is_error":true,"result":"max turns reached"}`,
			},
			want: harness.ErrKindMaxTurns,
		},
		{
			name: "用户中止不算接口错误",
			lines: []string{
				`{"type":"result","subtype":"error_during_execution","is_error":true,"terminal_reason":"aborted_streaming","result":"[Request interrupted by user]"}`,
			},
			want: harness.ErrKindCancelled,
		},
		{
			name: "老版本 CLI 的限流原文",
			lines: []string{
				`{"type":"result","subtype":"error","is_error":true,"result":"rate limit exceeded"}`,
			},
			want: harness.ErrKindRateLimited,
		},
		{
			name: "判不出来的归接口错误",
			lines: []string{
				`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"连接失败：connection reset by peer"}`,
			},
			want: harness.ErrKindAPIError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickError(t, feedParserLines(t, tc.lines...)).ErrKind; got != tc.want {
				t.Errorf("ErrKind = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParser_NormalResultNoError 校验正常结束的 result 不产生错误事件。
func TestParser_NormalResultNoError(t *testing.T) {
	events := feedParserLines(t,
		`{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed","api_error_status":null,"result":"done"}`,
	)
	for _, e := range events {
		if e.Type == harness.EventError {
			t.Fatalf("unexpected error event: %+v", e)
		}
	}
}

// TestParser_ToolResultStructuredPatch 校验 Edit 的 tool_use_result.structuredPatch 被还原成 unified diff，
// 在 tool_end 之后补发一条带 Patch 的文件变更事件。
func TestParser_ToolResultStructuredPatch(t *testing.T) {
	events := feedParserLines(t,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Edit","input":{"file_path":"/tmp/x/main.go","old_string":"one","new_string":"two"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"edited"}]},`+
			`"tool_use_result":{"filePath":"/tmp/x/main.go","oldString":"one","newString":"two","originalFile":"one\n",`+
			`"structuredPatch":[{"oldStart":1,"oldLines":1,"newStart":1,"newLines":1,"lines":["-one","+two"]}],`+
			`"userModified":false,"replaceAll":false}}`,
	)

	changes := pickFileChanges(t, events)
	if len(changes) != 2 {
		t.Fatalf("expected 2 file_change events, got %d: %+v", len(changes), events)
	}
	// 第一条来自 tool_use（只有路径），补发的这条带 diff。
	patched := changes[len(changes)-1]
	if patched.Path != "/tmp/x/main.go" {
		t.Errorf("Path = %q, want /tmp/x/main.go", patched.Path)
	}
	want := "--- a/tmp/x/main.go\n+++ b/tmp/x/main.go\n@@ -1,1 +1,1 @@\n-one\n+two\n"
	if patched.Patch != want {
		t.Errorf("Patch = %q, want %q", patched.Patch, want)
	}
	// 补发发生在 tool_end 之后。
	lastToolEnd, lastChange := -1, -1
	for i, e := range events {
		if e.Type == harness.EventToolEnd {
			lastToolEnd = i
		}
		if e.Type == harness.EventFileChange {
			lastChange = i
		}
	}
	if lastChange < lastToolEnd {
		t.Errorf("file_change 应补发在 tool_end 之后: %+v", events)
	}
}

// TestParser_ToolResultStructuredPatchNoNewline 校验 hunk 里的 "\ No newline at end of file" 行原样保留。
func TestParser_ToolResultStructuredPatchNoNewline(t *testing.T) {
	events := feedParserLines(t,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Edit","input":{"file_path":"/tmp/x/a.txt"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"edited"}]},`+
			`"tool_use_result":{"filePath":"/tmp/x/a.txt","structuredPatch":[{"oldStart":2,"oldLines":2,"newStart":2,"newLines":2,`+
			`"lines":["-one","\\ No newline at end of file","+two","\\ No newline at end of file"]}]}}`,
	)

	changes := pickFileChanges(t, events)
	if len(changes) != 2 {
		t.Fatalf("expected 2 file_change events, got %d: %+v", len(changes), events)
	}
	want := "--- a/tmp/x/a.txt\n+++ b/tmp/x/a.txt\n@@ -2,2 +2,2 @@\n-one\n\\ No newline at end of file\n+two\n\\ No newline at end of file\n"
	if got := changes[len(changes)-1].Patch; got != want {
		t.Errorf("Patch = %q, want %q", got, want)
	}
}

// TestParser_ToolResultWriteCreate 校验 Write 新建文件（type=create、structuredPatch 为空）时由 content
// 拼出新增 diff。
func TestParser_ToolResultWriteCreate(t *testing.T) {
	events := feedParserLines(t,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_2","name":"Write","input":{"file_path":"/tmp/x/new.go"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"created"}]},`+
			`"tool_use_result":{"type":"create","filePath":"/tmp/x/new.go","content":"package main\n\nfunc main() {}\n",`+
			`"structuredPatch":[],"originalFile":null}}`,
	)

	changes := pickFileChanges(t, events)
	if len(changes) != 2 {
		t.Fatalf("expected 2 file_change events, got %d: %+v", len(changes), events)
	}
	want := "--- /dev/null\n+++ b/tmp/x/new.go\n@@ -0,0 +1,3 @@\n+package main\n+\n+func main() {}\n"
	if got := changes[len(changes)-1].Patch; got != want {
		t.Errorf("Patch = %q, want %q", got, want)
	}
}

// TestParser_ToolResultWithoutPatch 校验没有 tool_use_result（或内容解析不出来）时行为与现状一致：
// 只有 tool_end，不补发文件变更事件。
func TestParser_ToolResultWithoutPatch(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{
			name: "老版本 CLI 没有 tool_use_result",
			line: `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}}`,
		},
		{
			name: "tool_use_result 解析失败",
			line: `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]},"tool_use_result":"oops"}`,
		},
		{
			name: "非编辑类工具的结构化输出",
			line: `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]},` +
				`"tool_use_result":{"type":"text","filePath":"/tmp/x/a.txt"}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := feedParserLines(t, tc.line)
			if len(events) != 1 || events[0].Type != harness.EventToolEnd {
				t.Fatalf("expected only 1 tool_end event, got %+v", events)
			}
		})
	}
}

// speedFeeder 用注入的假时钟依次喂入 stream-json 行：每次喂入前先推进时钟，
// 便于按「首个内容帧到用量帧」的时长断言请求速度样本。
type speedFeeder struct {
	t      *testing.T
	p      *parser
	now    time.Time
	events []harness.Event
}

func newSpeedFeeder(t *testing.T) *speedFeeder {
	t.Helper()
	f := &speedFeeder{t: t, p: newParser()}
	// 被测代码用零值表示尚未开始计时，测试时钟必须从非零时间起步，否则首帧的起点会丢失。
	f.now = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.p.now = func() time.Time { return f.now }
	return f
}

func (f *speedFeeder) feed(advance time.Duration, line string) {
	f.now = f.now.Add(advance)
	emit := func(e harness.Event) { f.events = append(f.events, e) }
	if err := f.p.handleLine([]byte(line), emit); err != nil {
		f.t.Fatalf("handleLine error: %v", err)
	}
}

// usage 取出唯一一条 usage 事件，和 pickUsage 一样把多发或缺失都算失败。
func (f *speedFeeder) usage() harness.Usage {
	f.t.Helper()
	var got []harness.Usage
	for _, e := range f.events {
		if e.Type == harness.EventUsage {
			if e.Usage == nil {
				f.t.Fatalf("usage event carries no payload: %+v", e)
			}
			got = append(got, *e.Usage)
		}
	}
	if len(got) != 1 {
		f.t.Fatalf("expected exactly 1 usage event, got %d: %+v", len(got), f.events)
	}
	return got[0]
}

// TestParser_RequestSpeeds 校验两次顶层请求各产出一条速度样本：起点是首个内容帧，
// tokens 优先取 message_delta，助手消息不重复结算，子代理帧不参与。
func TestParser_RequestSpeeds(t *testing.T) {
	f := newSpeedFeeder(t)

	f.feed(0, `{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":1000,"output_tokens":1}}}}`)
	f.feed(200*time.Millisecond, `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}}`)
	f.feed(50*time.Millisecond, `{"type":"stream_event","parent_tool_use_id":"toolu_1","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"子代理"}}}`)
	f.feed(1000*time.Millisecond, `{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}}`)
	f.feed(900*time.Millisecond, `{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":120}}}`)
	f.feed(10*time.Millisecond, `{"type":"assistant","message":{"id":"msg_1","usage":{"output_tokens":130},"content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]}}`)
	f.feed(5*time.Millisecond, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"ok"}]}]}}`)
	f.feed(300*time.Millisecond, `{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_2","usage":{"input_tokens":1200,"output_tokens":1}}}}`)
	f.feed(150*time.Millisecond, `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"World"}}}`)
	f.feed(10*time.Millisecond, `{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"id":"msg_sub","usage":{"output_tokens":99999},"content":[{"type":"text","text":"子代理正文"}]}}`)
	f.feed(1050*time.Millisecond, `{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":45}}}`)
	f.feed(0, `{"type":"result","subtype":"success","usage":{"input_tokens":2500,"output_tokens":900,"cache_read_input_tokens":40000}}`)

	usage := f.usage()
	want := []harness.RequestSpeed{
		{OutputTokens: 120, DurationMs: 1950},
		{OutputTokens: 45, DurationMs: 1060},
	}
	if !reflect.DeepEqual(usage.Requests, want) {
		t.Fatalf("Requests = %+v, want %+v", usage.Requests, want)
	}
	// result 之后本轮样本清空，避免残留到下一次运行。
	if len(f.p.requests) != 0 || f.p.reqSampleIdx != -1 {
		t.Errorf("result 之后未清空样本: %+v idx=%d", f.p.requests, f.p.reqSampleIdx)
	}
}

// TestParser_RequestSpeedsWithoutPartialStream 校验没有 partial 流时改用顶层助手消息的 usage：
// 首个内容帧缺席时以首条助手消息为起点，同一 message id 只结算一次，tokens 取后续更大的值。
func TestParser_RequestSpeedsWithoutPartialStream(t *testing.T) {
	f := newSpeedFeeder(t)

	f.feed(0, `{"type":"assistant","message":{"id":"msg_1","usage":{"output_tokens":5},"content":[{"type":"text","text":"先想一下"}]}}`)
	f.feed(400*time.Millisecond, `{"type":"assistant","message":{"id":"msg_1","usage":{"output_tokens":30},"content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]}}`)
	f.feed(100*time.Millisecond, `{"type":"assistant","message":{"id":"msg_1","usage":{"output_tokens":34},"content":[{"type":"text","text":"再看一眼"}]}}`)
	f.feed(0, `{"type":"result","subtype":"success","usage":{"input_tokens":50,"output_tokens":34}}`)

	want := []harness.RequestSpeed{{OutputTokens: 34, DurationMs: 400}}
	if got := f.usage().Requests; !reflect.DeepEqual(got, want) {
		t.Fatalf("Requests = %+v, want %+v", got, want)
	}
}

// TestParser_RequestSpeedsSkipTooShortRequest 校验不足时钟下限的请求不产出样本，usage 里保持空。
func TestParser_RequestSpeedsSkipTooShortRequest(t *testing.T) {
	f := newSpeedFeeder(t)

	f.feed(0, `{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_1"}}}`)
	f.feed(0, `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}}`)
	f.feed(10*time.Millisecond, `{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":3}}}`)
	f.feed(0, `{"type":"result","subtype":"success","usage":{"input_tokens":10,"output_tokens":3}}`)

	if got := f.usage().Requests; len(got) != 0 {
		t.Fatalf("过短的请求不应产出样本: %+v", got)
	}
}
