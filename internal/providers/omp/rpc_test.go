package omp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/procutil"
)

// readFixtureLines 读取 testdata 下录制的 RPC 帧流。
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
// fixture 里的当轮 prompt id 是 __PROMPT_ID__ 占位，这里直接把它当作 translate 的 promptID。
func translateFixture(t *testing.T, name string) ([]harness.Event, bool) {
	t.Helper()
	state := newStreamState()
	state.promptID = "__PROMPT_ID__"
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

// bufferWriteCloser 让测试用 bytes.Buffer 充当可关闭的 stdin 管道。
type bufferWriteCloser struct {
	bytes.Buffer
}

func (b *bufferWriteCloser) Close() error { return nil }

// decodeSingleRequest 解析替身管道上唯一的一条请求。
func decodeSingleRequest(t *testing.T, raw string) map[string]any {
	t.Helper()
	var request map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &request); err != nil {
		t.Fatalf("请求不是合法 JSON: %q", raw)
	}
	return request
}

// inputString 从工具入参里取字符串字段（测试断言用）。
func inputString(raw json.RawMessage, key string) string {
	return jsonText(jsonObject(raw), key)
}

// recordingApprover 记录审批请求并返回固定判决。
type recordingApprover struct {
	decision harness.Decision
	requests []harness.ApprovalRequest
}

func (a *recordingApprover) Approve(_ context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	a.requests = append(a.requests, req)
	return a.decision, nil
}

// recordingInteractor 在 Approver 之上实现 Interactor。
type recordingInteractor struct {
	recordingApprover
	response harness.ApprovalResponse
}

func (a *recordingInteractor) Interact(_ context.Context, req harness.ApprovalRequest) (harness.ApprovalResponse, error) {
	a.requests = append(a.requests, req)
	return a.response, nil
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
		harness.EventToolStart,
		harness.EventFileChange,
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

	if events[0].Text != "先看下仓库。" {
		t.Errorf("首个文本增量错误: %q", events[0].Text)
	}
	if usage := events[1].Usage; usage == nil || usage.InputTokens != 160 || usage.OutputTokens != 8 || usage.CachedTokens != 40 {
		t.Errorf("message_update 用量错误: %+v", events[1].Usage)
	}
	if start := events[2]; start.ToolID != "tc1" || start.ToolName != "bash" || inputString(start.Input, "command") != "ls -la" {
		t.Errorf("工具开始事件错误: %+v", start)
	}
	if end := events[3]; end.ToolID != "tc1" || end.Output != "main.go\n" || end.IsError {
		t.Errorf("工具结束事件错误: %+v", end)
	}
	if change := events[5]; change.Path != "internal/app/app.go" {
		t.Errorf("文件改动事件错误: %+v", change)
	}
	if events[7].Text != "该动手了" {
		t.Errorf("思考增量错误: %q", events[7].Text)
	}
	if usage := events[9].Usage; usage == nil || usage.InputTokens != 200 || usage.OutputTokens != 20 || usage.CachedTokens != 50 {
		t.Errorf("message_end 用量（含缓存读写合并）错误: %+v", events[9].Usage)
	}
	if events[10].StopReason != "completed" {
		t.Errorf("结束原因应为 completed，得到 %q", events[10].StopReason)
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
	if events[0].Type != harness.EventUsage || events[0].Usage == nil || events[0].Usage.InputTokens != 30 {
		t.Errorf("用量事件错误: %+v", events[0])
	}
	// 消息以 error 收尾但没有增量文本时，用最终内容补一次文本
	if events[1].Type != harness.EventText || events[1].Text != "正在尝试。" {
		t.Errorf("文本事件错误: %+v", events[1])
	}
	if events[2].Type != harness.EventError || events[2].Err != "API error: rate limit exceeded" {
		t.Errorf("错误事件错误: %+v", events[2])
	}
	if events[3].Type != harness.EventDone || events[3].StopReason != "error" {
		t.Errorf("结束事件应带 error 原因: %+v", events[3])
	}
}

func TestTranslatePromptFailureResponse(t *testing.T) {
	state := newStreamState()
	state.promptID = "p1"
	var events []harness.Event
	emit := func(event harness.Event) { events = append(events, event) }
	event, err := parseEvent([]byte(`{"type":"response","id":"p1","success":false,"error":"模型不可用"}`))
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
		t.Fatalf("结束事件错误: %+v", events)
	}
}

func TestTranslateSlashCommandWithoutTurn(t *testing.T) {
	state := newStreamState()
	state.promptID = "p1"
	var events []harness.Event
	emit := func(event harness.Event) { events = append(events, event) }

	// agentInvoked 显式为 true（或缺省）时当轮继续
	event, err := parseEvent([]byte(`{"type":"response","id":"p1","success":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if state.translate(event, emit) {
		t.Fatal("agentInvoked 缺省时不应结束当轮")
	}

	// 斜杠命令由 omp 自行执行：没有后续轮次
	event, err = parseEvent([]byte(`{"type":"response","id":"p1","success":true,"data":{"agentInvoked":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !state.translate(event, emit) {
		t.Fatal("agentInvoked 为 false 应结束当轮")
	}
	if len(events) != 1 || events[0].Type != harness.EventDone || events[0].StopReason != "completed" {
		t.Fatalf("应只发出一个 completed 结束事件: %+v", events)
	}
}

func TestTranslateAbortedPrompt(t *testing.T) {
	state := newStreamState()
	state.promptID = "p1"
	var events []harness.Event
	emit := func(event harness.Event) { events = append(events, event) }
	event, err := parseEvent([]byte(`{"type":"prompt_result","id":"p1","status":"aborted"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !state.translate(event, emit) {
		t.Fatal("prompt_result 应结束当轮")
	}
	if len(events) != 1 || events[0].Type != harness.EventDone || events[0].StopReason != "interrupted" {
		t.Fatalf("中断的当轮应带 interrupted 结束原因: %+v", events)
	}
}

func TestTranslateIgnoresAgentEnd(t *testing.T) {
	state := newStreamState()
	state.promptID = "p1"
	var events []harness.Event
	emit := func(event harness.Event) { events = append(events, event) }

	// omp 在一次失败的尝试与重试之间也会发 agent_end，它不是当轮终点
	event, err := parseEvent([]byte(`{"type":"agent_end","willRetry":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if state.translate(event, emit) {
		t.Fatal("agent_end 不应结束当轮")
	}
	if len(events) != 0 {
		t.Fatalf("agent_end 不应产生事件: %+v", events)
	}

	settled, err := parseEvent([]byte(`{"type":"prompt_result","id":"p1","status":"completed"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !state.translate(settled, emit) {
		t.Fatal("prompt_result 应结束当轮")
	}
	if len(events) != 1 || events[0].Type != harness.EventDone {
		t.Fatalf("应只发出一个 done 事件: %+v", events)
	}
}

// TestTranslateRetrySucceeds 断言可重试的模型错误被 omp 自行重试成功时，当轮不算失败。
func TestTranslateRetrySucceeds(t *testing.T) {
	state := newStreamState()
	state.promptID = "p1"
	var events []harness.Event
	emit := func(event harness.Event) { events = append(events, event) }
	for _, line := range []string{
		`{"type":"message_start","message":{"role":"assistant","id":"m1"}}`,
		`{"type":"message_end","message":{"role":"assistant","usage":{"input":100,"output":5},"stopReason":"error","errorMessage":"rate limit exceeded"}}`,
		`{"type":"agent_end","willRetry":true}`,
		`{"type":"auto_retry_start","attempt":2,"maxAttempts":3,"delayMs":1000,"errorMessage":"rate limit exceeded"}`,
		`{"type":"message_start","message":{"role":"assistant","id":"m2"}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"好了。"}],"usage":{"input":110,"output":8},"stopReason":"end_turn"}}`,
		`{"type":"prompt_result","id":"p1","status":"completed"}`,
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
	if want := "[omp 请求失败，正在重试 2/3：rate limit exceeded]\n"; reasoning != want {
		t.Fatalf("重试提示应为 %q，得到 %q", want, reasoning)
	}
	if dones != 1 {
		t.Fatalf("应只发出一个 done 事件: %+v", events)
	}
	if done := events[len(events)-1]; done.Type != harness.EventDone || done.StopReason != "completed" {
		t.Fatalf("重试成功应以 completed 收尾: %+v", done)
	}
}

// TestTranslateAccumulatesUsage 断言同一轮多次请求的用量是累计值，
// 而 ContextTokens 只表示最后一次请求带入的上下文大小。
func TestTranslateAccumulatesUsage(t *testing.T) {
	state := newStreamState()
	state.promptID = "p1"
	var events []harness.Event
	emit := func(event harness.Event) { events = append(events, event) }
	for _, line := range []string{
		`{"type":"message_start","message":{"role":"assistant","id":"m1"}}`,
		`{"type":"message_end","message":{"role":"assistant","usage":{"input":100,"output":10,"cacheRead":20,"totalTokens":130,"cost":{"total":0.002}},"stopReason":"end_turn"}}`,
		`{"type":"message_start","message":{"role":"assistant","id":"m2"}}`,
		`{"type":"message_end","message":{"role":"assistant","usage":{"input":200,"output":30,"cacheRead":50,"totalTokens":280,"cost":{"total":0.003}},"stopReason":"end_turn"}}`,
		`{"type":"prompt_result","id":"p1","status":"completed"}`,
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
}

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
		// omp 的 input 不含缓存命中量，缓存很大、input 很小时也要并入 InputTokens
		{name: "缓存并入输入", raw: `{"input":12,"output":3,"cacheRead":90000,"cacheWrite":1200}`, want: harness.Usage{InputTokens: 91212, OutputTokens: 3, CachedTokens: 91200}, ok: true},
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
			line: `{"type":"response","id":"s1","command":"get_state","success":true,"data":{"sessionFile":"/tmp/omp/session.json","sessionId":"sess-1"}}`,
			want: "/tmp/omp/session.json",
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

func TestHasCommand(t *testing.T) {
	event, err := parseEvent([]byte(`{"type":"response","id":"c1","success":true,"data":{"commands":[{"name":"help"},{"name":"mcode-gate"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !hasCommand(event, GateCommand) {
		t.Fatal("命令表里应有 mcode-gate")
	}
	if hasCommand(event, "missing") {
		t.Fatal("命令表里没有的命令不应算命中")
	}

	empty, err := parseEvent([]byte(`{"type":"response","id":"c2","success":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if hasCommand(empty, GateCommand) {
		t.Fatal("没有命令表时不应算命中")
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

func TestToolGateValue(t *testing.T) {
	if got := toolGateValue(harness.ApprovalResponse{Decision: harness.DecisionAllow}); got != "Approve" {
		t.Errorf("允许应映射为 Approve，得到 %q", got)
	}
	if got := toolGateValue(harness.ApprovalResponse{Decision: harness.DecisionDeny}); got != "Deny" {
		t.Errorf("拒绝应映射为 Deny，得到 %q", got)
	}
	if got := toolGateValue(harness.ApprovalResponse{Decision: harness.DecisionAsk}); got != "Deny" {
		t.Errorf("需要二次确认时按拒绝处理，得到 %q", got)
	}
	resp := harness.ApprovalResponse{
		Decision: harness.DecisionAllow,
		Answers:  map[string][]string{"gate": {"Approve for session"}},
	}
	if got := toolGateValue(resp); got != "Approve" {
		t.Errorf("会话级允许应映射为 Approve，得到 %q", got)
	}
}

func TestParseApprovalPrompt(t *testing.T) {
	toolName, detail := parseApprovalPrompt("Allow tool: bash\nCommand: rm -rf build")
	if toolName != "bash" || detail != "Command: rm -rf build" {
		t.Fatalf("自带审批提示解析错误: %q / %q", toolName, detail)
	}
	toolName, detail = parseApprovalPrompt("Allow tool: \n")
	if toolName != "tool" || detail != "" {
		t.Fatalf("缺工具名时按 tool 处理: %q / %q", toolName, detail)
	}

	if got := approvalKind("bash"); got != harness.RequestKindCommand {
		t.Errorf("bash 应为命令类，得到 %v", got)
	}
	if got := approvalKind("write"); got != harness.RequestKindFileChange {
		t.Errorf("write 应为文件变更类，得到 %v", got)
	}
	if got := approvalKind("mcp__x__y"); got != harness.RequestKindTool {
		t.Errorf("其它工具应为工具类，得到 %v", got)
	}
}

// TestApprovalRoundTrip 用 fixture 的门禁帧验证审批往返：假的 Interactor 收到审批请求，
// 适配器把 Approve 写回 stdin 管道。
func TestApprovalRoundTrip(t *testing.T) {
	lines := readFixtureLines(t, "rpc_approval_gate.jsonl")
	if len(lines) != 1 {
		t.Fatalf("fixture 应只有一帧: %d", len(lines))
	}
	event, err := parseEvent(lines[0])
	if err != nil {
		t.Fatal(err)
	}

	pr, pw := io.Pipe()
	approver := &recordingInteractor{response: harness.ApprovalResponse{Decision: harness.DecisionAllow}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		New(Options{}).handleExtensionUI(context.Background(), harness.Turn{Approver: approver}, procutil.NewLineWriter(pw), event)
	}()

	raw, err := bufio.NewReader(pr).ReadBytes('\n')
	if err != nil {
		t.Fatalf("读取应答失败: %v", err)
	}
	<-done
	_ = pr.Close()

	response := decodeSingleRequest(t, string(raw))
	if response["type"] != rpcTypeExtensionUIResp || response["id"] != "req-1" || response["value"] != "Approve" {
		t.Fatalf("门禁应答错误: %+v", response)
	}
	if len(approver.requests) != 1 {
		t.Fatalf("应只发出一个审批请求: %+v", approver.requests)
	}
	req := approver.requests[0]
	if req.Kind != harness.RequestKindCommand || req.ToolID != "tc1" || req.ToolName != "bash" || req.Summary != "ls -la" {
		t.Fatalf("审批请求错误: %+v", req)
	}
	if inputString(req.Input, "command") != "ls -la" {
		t.Fatalf("审批请求应带工具入参: %+v", req)
	}
}

func TestAnswerToolGateWithoutApprover(t *testing.T) {
	lines := readFixtureLines(t, "rpc_approval_gate.jsonl")
	event, err := parseEvent(lines[0])
	if err != nil {
		t.Fatal(err)
	}
	sink := &bufferWriteCloser{}
	New(Options{}).handleExtensionUI(context.Background(), harness.Turn{}, procutil.NewLineWriter(sink), event)
	response := decodeSingleRequest(t, sink.String())
	if response["value"] != "Deny" {
		t.Fatalf("没有审批方时应拒绝: %+v", response)
	}
}

func TestAnswerQuestion(t *testing.T) {
	t.Run("确认类提问", func(t *testing.T) {
		confirmLine := []byte(`{"type":"extension_ui_request","id":"q-1","method":"confirm","params":{"title":"是否继续？","message":"会覆盖文件"}}`)
		event, err := parseEvent(confirmLine)
		if err != nil {
			t.Fatal(err)
		}
		h := New(Options{})
		sink := &bufferWriteCloser{}
		interactor := &recordingInteractor{
			response: harness.ApprovalResponse{
				Decision: harness.DecisionAllow,
				Answers:  map[string][]string{"q-1": {"Yes"}},
			},
		}
		h.handleExtensionUI(context.Background(), harness.Turn{Approver: interactor}, procutil.NewLineWriter(sink), event)

		response := decodeSingleRequest(t, sink.String())
		if response["confirmed"] != true {
			t.Fatalf("确认应答错误: %+v", response)
		}
		questions := interactor.requests[0].Questions
		if len(questions) != 1 || !strings.Contains(questions[0].Prompt, "是否继续？") || !strings.Contains(questions[0].Prompt, "会覆盖文件") {
			t.Fatalf("提问内容错误: %+v", questions)
		}
	})

	t.Run("选择题", func(t *testing.T) {
		selectLine := []byte(`{"type":"extension_ui_request","id":"q-2","method":"select","params":{"title":"选一个","options":["a","b"]}}`)
		event, err := parseEvent(selectLine)
		if err != nil {
			t.Fatal(err)
		}
		h := New(Options{})
		sink := &bufferWriteCloser{}
		interactor := &recordingInteractor{
			response: harness.ApprovalResponse{
				Decision: harness.DecisionAllow,
				Answers:  map[string][]string{"q-2": {"b"}},
			},
		}
		h.handleExtensionUI(context.Background(), harness.Turn{Approver: interactor}, procutil.NewLineWriter(sink), event)

		response := decodeSingleRequest(t, sink.String())
		if response["value"] != "b" {
			t.Fatalf("选择应答错误: %+v", response)
		}
		if questions := interactor.requests[0].Questions; len(questions) != 1 || len(questions[0].Choices) != 2 {
			t.Fatalf("选择题候选项错误: %+v", questions)
		}
	})

	t.Run("没有答案时取消", func(t *testing.T) {
		inputLine := []byte(`{"type":"extension_ui_request","id":"q-3","method":"input","params":{"title":"补充说明"}}`)
		event, err := parseEvent(inputLine)
		if err != nil {
			t.Fatal(err)
		}
		h := New(Options{})
		sink := &bufferWriteCloser{}
		h.handleExtensionUI(context.Background(), harness.Turn{}, procutil.NewLineWriter(sink), event)
		response := decodeSingleRequest(t, sink.String())
		if response["cancelled"] != true {
			t.Fatalf("无答案时应取消: %+v", response)
		}
	})

	t.Run("omp 自带审批提示", func(t *testing.T) {
		promptLine := []byte(`{"type":"extension_ui_request","id":"q-4","method":"select","params":{"title":"Allow tool: bash\nCommand: rm -rf build","options":["Approve","Deny"]}}`)
		event, err := parseEvent(promptLine)
		if err != nil {
			t.Fatal(err)
		}
		h := New(Options{})
		sink := &bufferWriteCloser{}
		approver := &recordingApprover{decision: harness.DecisionAllow}
		h.handleExtensionUI(context.Background(), harness.Turn{Approver: approver}, procutil.NewLineWriter(sink), event)

		response := decodeSingleRequest(t, sink.String())
		if response["value"] != "Approve" {
			t.Fatalf("自带审批提示应答错误: %+v", response)
		}
		req := approver.requests[0]
		if req.Kind != harness.RequestKindCommand || req.ToolName != "bash" || req.Summary != "Command: rm -rf build" {
			t.Fatalf("自带审批提示请求错误: %+v", req)
		}
	})
}

func TestEditedFilePath(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		input string
		want  string
	}{
		{name: "write 取 path", tool: "write", input: `{"path":"internal/app/app.go","content":"x"}`, want: "internal/app/app.go"},
		{name: "write 到扩展设备不算文件改动", tool: "write", input: `{"path":"xd://todo","content":"x"}`, want: ""},
		{name: "edit 取 path", tool: "edit", input: `{"path":"a.go","input":"[b.go#tag]\n1|+x"}`, want: "a.go"},
		{name: "edit 从 hashline 头部取路径", tool: "edit", input: `{"input":"[internal/app/app.go#9f3]\n1|+x"}`, want: "internal/app/app.go"},
		{name: "ast_edit 同样取路径", tool: "ast_edit", input: `{"path":"a.go"}`, want: "a.go"},
		{name: "bash 不上报路径", tool: "bash", input: `{"command":"ls"}`, want: ""},
		{name: "空入参", tool: "write", input: ``, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := editedFilePath(test.tool, json.RawMessage(test.input)); got != test.want {
				t.Fatalf("路径应为 %q，得到 %q", test.want, got)
			}
		})
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
