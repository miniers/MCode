package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
)

// readFixtureLines 读取 testdata 下录制的 RPC 事件流。
func readFixtureLines(t *testing.T, name string) [][]byte {
	t.Helper()
	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 fixture %s 失败: %v", path, err)
	}
	var lines [][]byte
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, []byte(line))
		}
	}
	return lines
}

// translateFixture 把 fixture 的每一行喂给 translate，返回事件序列与结束标记。
func translateFixture(t *testing.T, name string) ([]harness.Event, bool) {
	t.Helper()
	state := newStreamState()
	var events []harness.Event
	emit := func(event harness.Event) { events = append(events, event) }
	done := false
	for _, line := range readFixtureLines(t, name) {
		event, err := parseEvent(line)
		if err != nil {
			t.Fatalf("解析 fixture 行失败: %v", err)
		}
		if event == nil {
			continue
		}
		if state.translate(event, emit) {
			done = true
		}
	}
	return events, done
}

func TestParseEvent(t *testing.T) {
	if event, err := parseEvent([]byte("   \n")); event != nil || err != nil {
		t.Fatalf("空行应返回 nil, nil，得到 %+v, %v", event, err)
	}
	if event, err := parseEvent([]byte(`{"foo":1}`)); event != nil || err != nil {
		t.Fatalf("缺少 type 的行应返回 nil, nil，得到 %+v, %v", event, err)
	}
	if _, err := parseEvent([]byte("not json")); err == nil {
		t.Fatal("非法 JSON 应返回错误")
	}
	event, err := parseEvent([]byte(`{"type":"message_start","message":{"role":"assistant"}}`))
	if err != nil || event == nil {
		t.Fatalf("合法事件解析失败: %+v, %v", event, err)
	}
	if event.Type != "message_start" || messageRole(event.Message) != "assistant" {
		t.Errorf("事件字段解析错误: %+v", event)
	}
}

func TestTranslateTextToolUsageFixture(t *testing.T) {
	events, done := translateFixture(t, "rpc_text_tool_usage.jsonl")
	if !done {
		t.Fatal("fixture 应以结束事件收尾")
	}

	wantTypes := []harness.EventType{
		harness.EventText,
		harness.EventUsage,
		harness.EventToolStart,
		harness.EventToolEnd,
		harness.EventReasoning,
		harness.EventText,
		harness.EventUsage,
		harness.EventDone,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("事件数量应为 %d，得到 %d: %+v", len(wantTypes), len(events), events)
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Errorf("第 %d 个事件应为 %v，得到 %v", i, want, events[i].Type)
		}
	}

	if events[0].Text != "让我先看文件。" {
		t.Errorf("首个文本增量错误: %q", events[0].Text)
	}
	if usage := events[1].Usage; usage == nil || usage.InputTokens != 160 || usage.OutputTokens != 8 || usage.CachedTokens != 40 {
		t.Errorf("message_update 用量错误: %+v", events[1].Usage)
	}
	start := events[2]
	if start.ToolID != "tc1" || start.ToolName != "bash" || inputString(start.Input, "command") != "ls -la" {
		t.Errorf("工具开始事件错误: %+v", start)
	}
	end := events[3]
	if end.ToolID != "tc1" || end.Output != "main.go\n" || end.IsError {
		t.Errorf("工具结束事件错误: %+v", end)
	}
	if events[4].Text != "该动手了" {
		t.Errorf("思考增量错误: %q", events[4].Text)
	}
	if usage := events[6].Usage; usage == nil || usage.InputTokens != 200 || usage.OutputTokens != 20 || usage.CachedTokens != 50 {
		t.Errorf("message_end 用量（含缓存读写合并）错误: %+v", events[6].Usage)
	}
	if events[7].StopReason != "completed" {
		t.Errorf("结束原因应为 completed，得到 %q", events[7].StopReason)
	}
}

func TestTranslateErrorFixture(t *testing.T) {
	events, done := translateFixture(t, "rpc_error.jsonl")
	if !done {
		t.Fatal("fixture 应以结束事件收尾")
	}
	if len(events) != 4 {
		t.Fatalf("事件数量应为 4，得到 %d: %+v", len(events), events)
	}
	if events[1].Type != harness.EventUsage || events[1].Usage == nil || events[1].Usage.InputTokens != 30 {
		t.Errorf("用量事件错误: %+v", events[1])
	}
	if events[2].Type != harness.EventError || events[2].Err != "rate limit exceeded" {
		t.Errorf("错误事件错误: %+v", events[2])
	}
	if events[3].Type != harness.EventDone || events[3].StopReason != "error" {
		t.Errorf("结束事件应带 error 原因: %+v", events[3])
	}
}

func TestTranslateSessionFixture(t *testing.T) {
	events, done := translateFixture(t, "rpc_session.jsonl")
	if !done {
		t.Fatal("fixture 应以结束事件收尾")
	}
	if len(events) != 3 {
		t.Fatalf("事件数量应为 3，得到 %d: %+v", len(events), events)
	}
	if events[0].Type != harness.EventText || events[0].Text != "接着上次的进度继续。" {
		t.Errorf("文本事件错误: %+v", events[0])
	}
	// message_end 的 content 与已流式的文本相同，不应重复补发。
	if usage := events[1].Usage; usage == nil || usage.InputTokens != 64 || usage.OutputTokens != 0 {
		t.Errorf("只带 totalTokens 的用量应记到输入侧: %+v", events[1].Usage)
	}
	if events[2].StopReason != "completed" {
		t.Errorf("结束原因应为 completed，得到 %q", events[2].StopReason)
	}
}

func TestTranslatePromptFailureResponse(t *testing.T) {
	state := newStreamState()
	var events []harness.Event
	emit := func(event harness.Event) { events = append(events, event) }
	line := []byte(`{"type":"response","id":"p1","command":"prompt","success":false,"error":"模型不可用"}`)
	event, err := parseEvent(line)
	if err != nil {
		t.Fatal(err)
	}
	if !state.translate(event, emit) {
		t.Fatal("prompt 失败应答应立即结束当轮")
	}
	if len(events) != 2 || events[0].Type != harness.EventError || events[0].Err != "模型不可用" {
		t.Fatalf("应用事件序列错误: %+v", events)
	}
	if events[1].Type != harness.EventDone || events[1].StopReason != "error" {
		t.Fatalf("结束事件错误: %+v", events[1])
	}
}

func TestTranslateRetryEndKeepsTurnOpen(t *testing.T) {
	state := newStreamState()
	var events []harness.Event
	emit := func(event harness.Event) { events = append(events, event) }
	event, err := parseEvent([]byte(`{"type":"agent_end","willRetry":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if state.translate(event, emit) {
		t.Fatal("willRetry 的 agent_end 不应结束当轮")
	}
	settled, err := parseEvent([]byte(`{"type":"agent_settled"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !state.translate(settled, emit) {
		t.Fatal("agent_settled 应结束当轮")
	}
	if len(events) != 1 || events[0].Type != harness.EventDone {
		t.Fatalf("应只发出一个 done 事件: %+v", events)
	}
}

// TestTranslateRetrySucceeds 断言可重试的模型错误被 pi 自行重试成功时，当轮不算失败。
func TestTranslateRetrySucceeds(t *testing.T) {
	state := newStreamState()
	var events []harness.Event
	emit := func(event harness.Event) { events = append(events, event) }
	for _, line := range []string{
		`{"type":"message_start","message":{"role":"assistant","id":"m1"}}`,
		`{"type":"message_end","message":{"role":"assistant","usage":{"input":100,"output":5},"stopReason":"error","errorMessage":"rate limit exceeded"}}`,
		`{"type":"agent_end","willRetry":true}`,
		`{"type":"auto_retry_start","attempt":2,"maxAttempts":3,"delayMs":1000,"errorMessage":"rate limit exceeded"}`,
		`{"type":"message_start","message":{"role":"assistant","id":"m2"}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"好了。"}],"usage":{"input":110,"output":8},"stopReason":"end_turn"}}`,
		`{"type":"agent_settled"}`,
	} {
		event, err := parseEvent([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		state.translate(event, emit)
	}

	errors := 0
	reasoning := ""
	dones := 0
	for _, event := range events {
		switch event.Type {
		case harness.EventError:
			errors++
		case harness.EventReasoning:
			reasoning = event.Text
		case harness.EventDone:
			dones++
		}
	}
	if errors != 0 {
		t.Fatalf("重试成功的一轮不应报错: %+v", events)
	}
	if want := "[pi 请求失败，正在重试 2/3：rate limit exceeded]\n"; reasoning != want {
		t.Fatalf("重试提示应为 %q，得到 %q", want, reasoning)
	}
	if dones != 1 {
		t.Fatalf("应只发出一个 done 事件: %+v", events)
	}
	if done := events[len(events)-1]; done.Type != harness.EventDone || done.StopReason != "completed" {
		t.Fatalf("重试成功应以 completed 收尾: %+v", done)
	}
}

// TestTranslateRetryExhausted 断言重试用尽时恰好报一次错，并带 error 结束原因。
func TestTranslateRetryExhausted(t *testing.T) {
	state := newStreamState()
	var events []harness.Event
	emit := func(event harness.Event) { events = append(events, event) }
	for _, line := range []string{
		`{"type":"message_start","message":{"role":"assistant","id":"m1"}}`,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"rate limit exceeded"}}`,
		`{"type":"agent_end","willRetry":true}`,
		`{"type":"auto_retry_start","attempt":3,"maxAttempts":3,"delayMs":8000,"errorMessage":"rate limit exceeded"}`,
		`{"type":"auto_retry_end","success":false,"attempt":3,"finalError":"重试用尽：rate limit exceeded"}`,
		`{"type":"agent_settled"}`,
	} {
		event, err := parseEvent([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		state.translate(event, emit)
	}

	errors := []harness.Event{}
	for _, event := range events {
		if event.Type == harness.EventError {
			errors = append(errors, event)
		}
	}
	if len(errors) != 1 || errors[0].Err != "重试用尽：rate limit exceeded" {
		t.Fatalf("重试用尽应恰好报一次错: %+v", events)
	}
	if done := events[len(events)-1]; done.Type != harness.EventDone || done.StopReason != "error" {
		t.Fatalf("重试用尽应以 error 收尾: %+v", done)
	}
}

// TestTranslateExtensionErrorIsNotFailure 断言扩展钩子抛异常只当提示：pi 会继续这一轮，
// 所以当轮不应报错，结束原因仍是 completed。
func TestTranslateExtensionErrorIsNotFailure(t *testing.T) {
	state := newStreamState()
	var events []harness.Event
	emit := func(event harness.Event) { events = append(events, event) }
	for _, line := range []string{
		`{"type":"extension_error","extensionPath":"/Users/me/code/demo/extensions/guard.ts","event":"tool_call","error":"Guard 钩子读取配置失败"}`,
		`{"type":"message_start","message":{"role":"assistant","id":"m1"}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"继续处理。"}],"stopReason":"end_turn"}}`,
		`{"type":"agent_settled"}`,
	} {
		event, err := parseEvent([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		state.translate(event, emit)
	}

	reasoning := []string{}
	for _, event := range events {
		switch event.Type {
		case harness.EventError:
			t.Fatalf("扩展钩子报错不应判当轮失败: %+v", events)
		case harness.EventReasoning:
			reasoning = append(reasoning, event.Text)
		}
	}
	if len(reasoning) != 1 {
		t.Fatalf("应只发一条扩展出错提示，得到 %d 条: %+v", len(reasoning), events)
	}
	if !strings.Contains(reasoning[0], "guard.ts") ||
		!strings.Contains(reasoning[0], "tool_call") ||
		!strings.Contains(reasoning[0], "Guard 钩子读取配置失败") {
		t.Fatalf("扩展出错提示缺少扩展名、事件名或错误摘要: %q", reasoning[0])
	}
	if strings.Contains(reasoning[0], "/Users/me/code/demo") {
		t.Fatalf("扩展出错提示不应暴露完整目录: %q", reasoning[0])
	}
	if done := events[len(events)-1]; done.Type != harness.EventDone || done.StopReason != "completed" {
		t.Fatalf("扩展报错后当轮应以 completed 收尾: %+v", done)
	}
	// 字段缺失时不可留下空括号。
	if got, want := extensionErrorText(&rpcEvent{Error: "boom"}), "[pi 扩展出错：boom]\n"; got != want {
		t.Fatalf("字段缺失时应省略括号: %q", got)
	}
}

// TestTranslateAccumulatesUsage 断言同一轮多次请求的用量是累计值，
// 而 ContextTokens 只表示最后一次请求带入的上下文大小。
func TestTranslateAccumulatesUsage(t *testing.T) {
	state := newStreamState()
	state.contextWindow = 200000
	var events []harness.Event
	emit := func(event harness.Event) { events = append(events, event) }
	for _, line := range []string{
		`{"type":"message_start","message":{"role":"assistant","id":"m1"}}`,
		`{"type":"message_end","message":{"role":"assistant","usage":{"input":100,"output":10,"cacheRead":20,"cost":{"total":0.002}},"stopReason":"end_turn"}}`,
		`{"type":"message_start","message":{"role":"assistant","id":"m2"}}`,
		`{"type":"message_end","message":{"role":"assistant","usage":{"input":200,"output":30,"cacheRead":50,"cost":{"total":0.003}},"stopReason":"end_turn"}}`,
		`{"type":"agent_settled"}`,
	} {
		event, err := parseEvent([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		state.translate(event, emit)
	}

	var last *harness.Usage
	for _, event := range events {
		if event.Type == harness.EventUsage {
			last = event.Usage
		}
	}
	if last == nil {
		t.Fatalf("应发出用量事件: %+v", events)
	}
	if last.InputTokens != 370 || last.OutputTokens != 40 || last.CachedTokens != 70 {
		t.Fatalf("最后一次用量应取两次请求之和: %+v", last)
	}
	if last.ContextTokens != 280 {
		t.Fatalf("ContextTokens 应取第二次请求的输入加缓存加输出: %+v", last)
	}
	if want := 0.002 + 0.003; last.CostUSD != want {
		t.Fatalf("CostUSD 应为 %.4f，得到 %.4f", want, last.CostUSD)
	}
	if last.ContextWindow != 200000 {
		t.Fatalf("ContextWindow 应有值时上报: %+v", last)
	}
}

// TestParseUsageCost 断言费用取自 usage.cost.total，缺失时为 0。
func TestParseUsageCost(t *testing.T) {
	if got := parseUsageCost(json.RawMessage(`{"input":10,"cost":{"input":0.001,"total":0.002}}`)); got != 0.002 {
		t.Fatalf("费用应取自 cost.total，得到 %v", got)
	}
	if got := parseUsageCost(json.RawMessage(`{"input":10}`)); got != 0 {
		t.Fatalf("没有费用字段应为 0，得到 %v", got)
	}
	if got := parseUsageCost(nil); got != 0 {
		t.Fatalf("空载荷费用应为 0，得到 %v", got)
	}
}

// TestTranslateCompactionEvents 断言压缩开始/成功/被中止/失败都发一条中文 reasoning 提示。
func TestTranslateCompactionEvents(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
		// wantCompacted 非 nil 时表示还会额外发一个上下文压缩事件，值就是压缩前的 token 数。
		wantCompacted *int64
	}{
		{
			name: "start",
			line: `{"type":"compaction_start","reason":"threshold"}`,
			want: "[pi 正在压缩上下文…]\n",
		},
		{
			name:          "end_success",
			line:          `{"type":"compaction_end","reason":"threshold","result":{"summary":"s","firstKeptEntryId":"abc123","tokensBefore":150000,"estimatedTokensAfter":32000},"aborted":false,"willRetry":false}`,
			want:          "[pi 已压缩上下文，压缩前约 150000 tokens]\n",
			wantCompacted: int64Ptr(150000),
		},
		{
			name: "end_failed",
			line: `{"type":"compaction_end","reason":"threshold","aborted":false,"willRetry":false,"errorMessage":"terminated"}`,
			want: "[pi 压缩上下文失败：terminated]\n",
		},
		{
			name: "end_aborted",
			line: `{"type":"compaction_end","reason":"manual","aborted":true,"willRetry":false}`,
			want: "[pi 压缩上下文失败]\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := newStreamState()
			var events []harness.Event
			emit := func(event harness.Event) { events = append(events, event) }
			event, err := parseEvent([]byte(tt.line))
			if err != nil {
				t.Fatal(err)
			}
			if state.translate(event, emit) {
				t.Fatal("压缩事件不应结束当轮")
			}

			var reasoning, compacted []harness.Event
			for _, e := range events {
				switch e.Type {
				case harness.EventReasoning:
					reasoning = append(reasoning, e)
				case harness.EventContextCompacted:
					compacted = append(compacted, e)
				default:
					t.Fatalf("不应发出其他事件: %+v", e)
				}
			}
			if len(reasoning) != 1 || reasoning[0].Text != tt.want {
				t.Fatalf("提示文本错误: %+v", reasoning)
			}
			if tt.wantCompacted == nil {
				if len(compacted) != 0 {
					t.Fatalf("失败或中止时不应发上下文压缩事件，实际 %+v", compacted)
				}
				return
			}
			if len(compacted) != 1 {
				t.Fatalf("压缩成功应恰好发一个上下文压缩事件，实际 %+v", compacted)
			}
			if got := compacted[0].CompactTokensBefore; got != *tt.wantCompacted {
				t.Fatalf("压缩前 token = %d, want %d", got, *tt.wantCompacted)
			}
		})
	}
}

// int64Ptr 返回 v 的指针，供表驱动用例区分「没发事件」与「发了事件、token 为 0」。
func int64Ptr(v int64) *int64 { return &v }

func TestParseUsage(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want harness.Usage
		ok   bool
	}{
		{name: "总计", raw: `{"totalTokens":64}`, want: harness.Usage{InputTokens: 64}, ok: true},
		{name: "下划线总计", raw: `{"total_tokens":70}`, want: harness.Usage{InputTokens: 70}, ok: true},
		{name: "四段拆分", raw: `{"inputTokens":10,"outputTokens":2,"cacheReadTokens":3,"cacheWriteTokens":4}`, want: harness.Usage{InputTokens: 17, OutputTokens: 2, CachedTokens: 7}, ok: true},
		{name: "短键名", raw: `{"input":5,"output":1,"cache_read_tokens":2}`, want: harness.Usage{InputTokens: 7, OutputTokens: 1, CachedTokens: 2}, ok: true},
		{name: "缓存远大于输入", raw: `{"inputTokens":12,"outputTokens":3,"cacheReadTokens":589000,"cacheWriteTokens":0}`, want: harness.Usage{InputTokens: 589012, OutputTokens: 3, CachedTokens: 589000}, ok: true},
		{name: "零值", raw: `{"totalTokens":0}`, want: harness.Usage{}, ok: false},
		{name: "空对象", raw: `{}`, want: harness.Usage{}, ok: false},
		{name: "未知键", raw: `{"total":12}`, want: harness.Usage{}, ok: false},
		{name: "非法 JSON", raw: `<xml>`, want: harness.Usage{}, ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseUsage(json.RawMessage(test.raw))
			if ok != test.ok {
				t.Fatalf("ok 应为 %v，得到 %v", test.ok, ok)
			}
			if ok && !reflect.DeepEqual(got, test.want) {
				t.Fatalf("用量应为 %+v，得到 %+v", test.want, got)
			}
		})
	}
	if _, ok := parseUsage(nil); ok {
		t.Fatal("空载荷不应产生用量")
	}
}

func TestSessionIDFromState(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "会话文件优先",
			line: `{"type":"response","id":"s1","command":"get_state","success":true,"data":{"sessionFile":"/tmp/pi/session.json","sessionId":"sess-1"}}`,
			want: "/tmp/pi/session.json",
		},
		{
			name: "只有 sessionId",
			line: `{"type":"response","id":"s1","command":"get_state","success":true,"data":{"sessionId":"sess-2"}}`,
			want: "sess-2",
		},
		{
			name: "无 data",
			line: `{"type":"response","id":"s1","command":"get_state","success":true}`,
			want: "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event, err := parseEvent([]byte(test.line))
			if err != nil {
				t.Fatal(err)
			}
			if got := sessionIDFromState(event); got != test.want {
				t.Fatalf("会话 id 应为 %q，得到 %q", test.want, got)
			}
		})
	}
}

func TestParseGatePayload(t *testing.T) {
	title := `mcode-gate {"kind":"command","toolName":"bash","input":{"command":"rm -rf /tmp/x"},"toolCallId":"tc9"}`
	gate, ok := parseGatePayload(title)
	if !ok {
		t.Fatal("应解析出 mcode-gate 载荷")
	}
	if gate.Kind != "command" || gate.ToolName != "bash" || gate.ToolCallID != "tc9" {
		t.Fatalf("载荷解析错误: %+v", gate)
	}

	if _, ok := parseGatePayload("选择文件"); ok {
		t.Fatal("非门禁标题不应解析成功")
	}
	if _, ok := parseGatePayload(gateTitlePrefix + "not json"); ok {
		t.Fatal("门禁标题里的非法 JSON 不应解析成功")
	}

	// kind 缺省时按工具处理
	plain, ok := parseGatePayload(`mcode-gate {"toolName":"write","input":{"path":"/tmp/a.go"}}`)
	if !ok || plain.Kind != "tool" {
		t.Fatalf("缺省 kind 应为 tool: %+v", plain)
	}
}

func TestApprovalFromGate(t *testing.T) {
	commandGate, _ := parseGatePayload(`mcode-gate {"kind":"command","toolName":"bash","input":{"command":"ls"},"toolCallId":"tc1"}`)
	req := approvalFromGate(commandGate)
	if req.Kind != harness.RequestKindCommand || req.ToolID != "tc1" || req.ToolName != "bash" || req.Summary != "ls" {
		t.Fatalf("命令类审批请求错误: %+v", req)
	}

	fileGate, _ := parseGatePayload(`mcode-gate {"kind":"fileChange","toolName":"write","input":{"path":"/tmp/a.go"}}`)
	req = approvalFromGate(fileGate)
	if req.Kind != harness.RequestKindFileChange || req.Summary != "/tmp/a.go" {
		t.Fatalf("文件变更类审批请求错误: %+v", req)
	}

	toolGate, _ := parseGatePayload(`mcode-gate {"kind":"tool","toolName":"mcp__x__y","input":{"a":1}}`)
	req = approvalFromGate(toolGate)
	if req.Kind != harness.RequestKindTool || req.ToolName != "mcp__x__y" {
		t.Fatalf("工具类审批请求错误: %+v", req)
	}
}

func TestGateValue(t *testing.T) {
	if got := gateValue(harness.ApprovalResponse{Decision: harness.DecisionAllow}); got != "Allow" {
		t.Errorf("允许应映射为 Allow，得到 %q", got)
	}
	if got := gateValue(harness.ApprovalResponse{Decision: harness.DecisionDeny}); got != "Deny" {
		t.Errorf("拒绝应映射为 Deny，得到 %q", got)
	}
	if got := gateValue(harness.ApprovalResponse{Decision: harness.DecisionAsk}); got != "Deny" {
		t.Errorf("需要二次确认时按拒绝处理，得到 %q", got)
	}
	resp := harness.ApprovalResponse{
		Decision: harness.DecisionAllow,
		Answers:  map[string][]string{"gate": {"Allow for session"}},
	}
	if got := gateValue(resp); got != "Allow for session" {
		t.Errorf("会话级允许应映射为 Allow for session，得到 %q", got)
	}
}

func TestSlashPrefixEscaping(t *testing.T) {
	// message 以 / 开头时前面补空格，避免被 pi 当作命令或模板执行；普通文本保持原样。
	req := promptRequest("1", "/foo", nil)
	if req.Message != " /foo" {
		t.Errorf("promptRequest: 期望 ' /foo'，实际得到 %q", req.Message)
	}

	reqNormal := promptRequest("2", "normal text", nil)
	if reqNormal.Message != "normal text" {
		t.Errorf("promptRequest: 普通文本应保持不变，实际得到 %q", reqNormal.Message)
	}

	steerReq := steerRequest("3", "/bar")
	if steerReq.Message != " /bar" {
		t.Errorf("steerRequest: 期望 ' /bar'，实际得到 %q", steerReq.Message)
	}

	steerNormal := steerRequest("4", "normal steer")
	if steerNormal.Message != "normal steer" {
		t.Errorf("steerRequest: 普通文本应保持不变，实际得到 %q", steerNormal.Message)
	}
}

// TestTranslateRecordsRequestSpeeds 断言每个 assistant 请求的速度样本从首个内容帧算到用量帧，
// 并随用量事件完整上报；非 assistant 消息不产生样本。
func TestTranslateRecordsRequestSpeeds(t *testing.T) {
	state := newStreamState()
	now := time.Unix(1700000000, 0)
	state.now = func() time.Time { return now }
	var events []harness.Event
	emit := func(event harness.Event) { events = append(events, event) }
	feed := func(line string) {
		t.Helper()
		event, err := parseEvent([]byte(line))
		if err != nil {
			t.Fatalf("解析事件失败: %v", err)
		}
		state.translate(event, emit)
	}

	// 第一次请求：消息开始到首个增量之间的 200ms 不计入，样本时长为 500ms。
	feed(`{"type":"message_start","message":{"role":"assistant","id":"m1"}}`)
	now = now.Add(200 * time.Millisecond)
	feed(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"你"}}`)
	now = now.Add(500 * time.Millisecond)
	feed(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"你好"}],"usage":{"input":10,"output":40},"stopReason":"end_turn"}}`)

	// 工具结果消息不是模型请求，不产生样本。
	feed(`{"type":"message_start","message":{"role":"toolResult"}}`)
	now = now.Add(2000 * time.Millisecond)
	feed(`{"type":"message_end","message":{"role":"toolResult","usage":{"input":50,"output":10}}}`)

	// 第二次请求：思考增量同样触发计时，样本时长为 1000ms。
	feed(`{"type":"message_start","message":{"role":"assistant","id":"m2"}}`)
	now = now.Add(300 * time.Millisecond)
	feed(`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"想"}}`)
	now = now.Add(1000 * time.Millisecond)
	feed(`{"type":"message_end","message":{"role":"assistant","usage":{"input":20,"output":70},"stopReason":"end_turn"}}`)

	var usages []*harness.Usage
	for _, event := range events {
		if event.Type == harness.EventUsage {
			usages = append(usages, event.Usage)
		}
	}
	if len(usages) != 2 {
		t.Fatalf("两次请求应各发一次用量事件，得到 %d: %+v", len(usages), events)
	}
	if got := usages[0].Requests; len(got) != 1 || got[0] != (harness.RequestSpeed{OutputTokens: 40, DurationMs: 500}) {
		t.Fatalf("第一次请求的样本错误: %+v", got)
	}
	want := []harness.RequestSpeed{{OutputTokens: 40, DurationMs: 500}, {OutputTokens: 70, DurationMs: 1000}}
	got := usages[1].Requests
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("最后一次用量应带本轮完整样本 %+v，得到 %+v", want, got)
	}
}
