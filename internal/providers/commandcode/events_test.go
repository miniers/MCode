package commandcode

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
)

// replayFixture 把 NDJSON fixture 逐行喂给 streamState，返回回放后的状态与事件。
// 回放阶段不产生 done：done 在进程退出后由 finishRun 发出。
func replayFixture(t *testing.T, name, sessionID string) (*streamState, []harness.Event) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读取 fixture 失败: %v", err)
	}
	state := newStreamState(sessionID)
	var events []harness.Event
	emit := func(event harness.Event) { events = append(events, event) }
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		frame, err := parseFrame([]byte(line))
		if err != nil {
			t.Fatalf("fixture 行不是合法 JSON: %q", line)
		}
		if frame == nil {
			continue
		}
		state.handle(frame, emit)
	}
	return state, events
}

func TestReplaySuccessFixture(t *testing.T) {
	state, events := replayFixture(t, "events_success.ndjson", "old-sess")
	state.finishRun(0, false, "", func(event harness.Event) { events = append(events, event) })

	wantTypes := []harness.EventType{
		harness.EventSession,
		harness.EventReasoning,
		harness.EventText,
		harness.EventText,
		harness.EventToolStart,
		harness.EventToolEnd,
		harness.EventUsage,
		harness.EventDone,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("事件数量应为 %d，得到 %d: %+v", len(wantTypes), len(events), events)
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Fatalf("第 %d 个事件应为 %s，得到 %s", i, want, events[i].Type)
		}
	}
	if events[0].SessionID != "cli-sess-1" {
		t.Fatalf("run_start 应给出会话 id，得到 %+v", events[0])
	}
	if events[1].Text != "先看一下" {
		t.Fatalf("思考增量不符: %+v", events[1])
	}
	if events[2].Text != "你好" || events[3].Text != "，世界" {
		t.Fatalf("文本增量不符: %+v %+v", events[2], events[3])
	}
	if events[4].ToolID != "tc-1" || events[4].ToolName != "shell_command" {
		t.Fatalf("工具开始事件不符: %+v", events[4])
	}
	if events[5].ToolID != "tc-1" || events[5].Output != "main.go" || events[5].IsError {
		t.Fatalf("工具结束事件不符: %+v", events[5])
	}
	if events[6].Usage == nil || events[6].Usage.InputTokens != 1200 || events[6].Usage.OutputTokens != 340 || events[6].Usage.CachedTokens != 800 {
		t.Fatalf("用量事件不符: %+v", events[6])
	}
	if events[7].StopReason != "completed" {
		t.Fatalf("结束原因应为 completed，得到 %q", events[7].StopReason)
	}
	if state.sessionID != "cli-sess-1" {
		t.Fatalf("会话 id 应被捕获，得到 %q", state.sessionID)
	}
}

func TestReplayWholeTextFixture(t *testing.T) {
	state, events := replayFixture(t, "events_whole_text.ndjson", "")
	state.finishRun(0, false, "", func(event harness.Event) { events = append(events, event) })

	wantTypes := []harness.EventType{
		harness.EventSession,
		harness.EventReasoning,
		harness.EventText,
		harness.EventToolStart,
		harness.EventFileChange,
		harness.EventToolEnd,
		harness.EventToolStart,
		harness.EventToolEnd,
		harness.EventDone,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("事件数量应为 %d，得到 %d: %+v", len(wantTypes), len(events), events)
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Fatalf("第 %d 个事件应为 %s，得到 %s", i, want, events[i].Type)
		}
	}
	// 整段到达的思考与回复各补一次事件，不重复。
	if events[1].Text != "整段思考" || events[2].Text != "整段回复" {
		t.Fatalf("整段补发不符: %+v %+v", events[1], events[2])
	}
	if events[4].Path != "src/main.go" {
		t.Fatalf("写文件应带上路径，得到 %+v", events[4])
	}
	if !events[5].IsError || events[5].Output != "磁盘已满" {
		t.Fatalf("工具报错事件不符: %+v", events[5])
	}
	if !events[7].IsError || events[7].Output != "用户拒绝了这次调用" {
		t.Fatalf("受阻工具事件不符: %+v", events[7])
	}
}

func TestReplayErrorFixture(t *testing.T) {
	state, events := replayFixture(t, "events_error.ndjson", "cli-sess-3")
	state.finishRun(1, false, "", func(event harness.Event) { events = append(events, event) })

	wantTypes := []harness.EventType{harness.EventText, harness.EventError, harness.EventDone}
	if len(events) != len(wantTypes) {
		t.Fatalf("事件数量应为 %d，得到 %d: %+v", len(wantTypes), len(events), events)
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Fatalf("第 %d 个事件应为 %s，得到 %s", i, want, events[i].Type)
		}
	}
	// 会话 id 与启动时相同，不应重复发 session 事件。
	if state.sessionID != "cli-sess-3" {
		t.Fatalf("会话 id 不符: %q", state.sessionID)
	}
	if state.result == nil || state.result.Subtype != "error" || state.result.Error != "rate limit exceeded" {
		t.Fatalf("result 帧应被记录: %+v", state.result)
	}
	if state.runError != "rate limit exceeded" {
		t.Fatalf("run_error 帧应被记录，得到 %q", state.runError)
	}
	if events[1].Err != "rate limit exceeded" {
		t.Fatalf("错误事件文案不符: %+v", events[1])
	}
	if events[2].StopReason != "error" {
		t.Fatalf("结束原因应为 error，得到 %q", events[2].StopReason)
	}
}

func TestParseUsage(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want harness.Usage
		ok   bool
	}{
		{
			name: "snake_case 键名",
			raw:  `{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":3,"cache_write_input_tokens":2}`,
			want: harness.Usage{InputTokens: 10, OutputTokens: 5, CachedTokens: 5},
			ok:   true,
		},
		{
			name: "camelCase 键名",
			raw:  `{"inputTokens":10,"outputTokens":5,"cachedInputTokens":4}`,
			want: harness.Usage{InputTokens: 10, OutputTokens: 5, CachedTokens: 4},
			ok:   true,
		},
		{
			name: "只有总量时记到输入侧",
			raw:  `{"totalTokens":42}`,
			want: harness.Usage{InputTokens: 42},
			ok:   true,
		},
		{
			name: "零用量不发事件",
			raw:  `{"inputTokens":0,"outputTokens":0}`,
			ok:   false,
		},
		{
			name: "非法载荷",
			raw:  `"nope"`,
			ok:   false,
		},
		{
			name: "空载荷",
			raw:  ``,
			ok:   false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseUsage([]byte(test.raw))
			if ok != test.ok {
				t.Fatalf("ok 应为 %v，得到 %v (%+v)", test.ok, ok, got)
			}
			if ok && !reflect.DeepEqual(got, test.want) {
				t.Fatalf("用量不符:\n得到 %+v\n期望 %+v", got, test.want)
			}
		})
	}
}

func TestFinishRunExitCodes(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		stopped   bool
		stderr    string
		wantDone  string
		wantError string
	}{
		{name: "正常退出", status: 0, wantDone: "completed"},
		{name: "未登录", status: 3, wantDone: "error", wantError: "cmd login"},
		{name: "被拒绝授权", status: 4, stderr: "denied by hook", wantDone: "error", wantError: "denied by hook"},
		{name: "被限流", status: 5, wantDone: "error", wantError: "限流"},
		{name: "连不上服务", status: 6, wantDone: "error", wantError: "网络"},
		{name: "服务端错误", status: 7, wantDone: "error", wantError: "服务端"},
		{name: "达到往返上限", status: 8, wantDone: "max_turns"},
		{name: "模型没有回复", status: 9, wantDone: "error", wantError: "没有产出"},
		{name: "额度用尽", status: 10, wantDone: "error", wantError: "额度已用尽"},
		{name: "退出码 130 是中止", status: 130, wantDone: "interrupted"},
		{name: "被停止信号结束是中止", status: -1, stopped: true, wantDone: "interrupted"},
		{name: "未知退出码", status: 2, stderr: "boom", wantDone: "error", wantError: "boom"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := newStreamState("")
			var events []harness.Event
			state.finishRun(test.status, test.stopped, test.stderr, func(event harness.Event) { events = append(events, event) })

			var done *harness.Event
			var errText string
			for i := range events {
				switch events[i].Type {
				case harness.EventDone:
					done = &events[i]
				case harness.EventError:
					errText = events[i].Err
				}
			}
			if done == nil || done.StopReason != test.wantDone {
				t.Fatalf("结束原因应为 %q，得到 %+v", test.wantDone, events)
			}
			if test.wantError == "" {
				if errText != "" {
					t.Fatalf("不应发错误事件，得到 %q", errText)
				}
				return
			}
			if !strings.Contains(errText, test.wantError) {
				t.Fatalf("错误文案应包含 %q，得到 %q", test.wantError, errText)
			}
		})
	}
}

func TestFinishRunMaxTurnsResult(t *testing.T) {
	state := newStreamState("")
	state.result = &runResult{Subtype: "success", StopReason: "max_turns"}
	var events []harness.Event
	state.finishRun(0, false, "", func(event harness.Event) { events = append(events, event) })

	if len(events) != 1 || events[0].Type != harness.EventDone || events[0].StopReason != stopReasonMaxTurns {
		t.Fatalf("往返上限应以 %s 收尾: %+v", stopReasonMaxTurns, events)
	}
}

func TestFinishRunPrefersResultFrameOverExitCode(t *testing.T) {
	state := newStreamState("")
	state.result = &runResult{Subtype: "error", Error: "insufficient credits"}
	var events []harness.Event
	state.finishRun(1, false, "", func(event harness.Event) { events = append(events, event) })

	if len(events) != 2 || events[0].Type != harness.EventError || events[1].Type != harness.EventDone {
		t.Fatalf("应发一条错误与一条结束事件: %+v", events)
	}
	if !strings.Contains(events[0].Err, "额度已用尽") {
		t.Fatalf("账号类错误应换成可操作的提示，得到 %q", events[0].Err)
	}
	if events[1].StopReason != "error" {
		t.Fatalf("结束原因应为 error，得到 %q", events[1].StopReason)
	}
}

// 请求速度样本：首个流式增量帧到 model_request_end 计时，没有增量帧的请求不产生样本
// （只有 message_end 整段补发的文本也不算）；每条 usage 都带本轮至今的完整样本列表。
func TestRequestSpeedSamples(t *testing.T) {
	state := newStreamState("")
	// 假时钟每次调用前进 200 毫秒：不必真的等待，只要开始与结算之间取到足够时长。
	var ticks int
	state.now = func() time.Time {
		ticks++
		return time.Unix(0, 0).Add(time.Duration(ticks) * 200 * time.Millisecond)
	}
	var events []harness.Event
	emit := func(event harness.Event) { events = append(events, event) }

	frames := []string{
		`{"type":"event","event":{"type":"message_start"}}`,
		`{"type":"event","event":{"type":"text_delta","delta":"hi"}}`,
		`{"type":"event","event":{"type":"model_request_end","usage":{"outputTokens":20}}}`,
		`{"type":"event","event":{"type":"message_start"}}`,
		`{"type":"event","event":{"type":"thinking_delta","delta":"hmm"}}`,
		`{"type":"event","event":{"type":"model_request_end","usage":{"outputTokens":30}}}`,
		// 没有内容帧的请求：只有 usage，不产生样本。
		`{"type":"event","event":{"type":"model_request_end","usage":{"outputTokens":40}}}`,
		// 只有 message_end 整段补发的文本（没有 text_delta）：不产生样本。
		`{"type":"event","event":{"type":"message_end","content":[{"type":"text","text":"done"}]}}`,
		`{"type":"event","event":{"type":"model_request_end","usage":{"outputTokens":50}}}`,
	}
	for _, raw := range frames {
		frame, err := parseFrame([]byte(raw))
		if err != nil {
			t.Fatalf("解析帧失败: %v", err)
		}
		state.handle(frame, emit)
	}

	var usages []harness.Usage
	for _, event := range events {
		if event.Type == harness.EventUsage && event.Usage != nil {
			usages = append(usages, *event.Usage)
		}
	}
	if len(usages) != 4 {
		t.Fatalf("want 4 usage events, got %d: %+v", len(usages), usages)
	}

	first := usages[0].Requests
	if len(first) != 1 || first[0].OutputTokens != 20 || first[0].DurationMs < 100 {
		t.Fatalf("第一次请求的速度样本 = %+v, want 1 条 20 tokens 且时长 >= 100ms", first)
	}
	second := usages[1].Requests
	if len(second) != 2 || second[0].OutputTokens != 20 || second[1].OutputTokens != 30 || second[1].DurationMs < 100 {
		t.Fatalf("第二次请求的速度样本 = %+v, want [20 30]", second)
	}
	if len(usages[2].Requests) != 2 {
		t.Fatalf("无内容帧的请求不应产生样本，得到 %+v", usages[2].Requests)
	}
	if len(usages[3].Requests) != 2 {
		t.Fatalf("只有 message_end 整段文本的请求不应产生样本，得到 %+v", usages[3].Requests)
	}
}

// 整轮累计用量：后端每轮只保留最后一条 usage 事件，所以每次 model_request_end 都要带
// 整轮累计值；新一轮由新的 streamState 从 0 重新累计。
func TestUsageAccumulatesAcrossRequests(t *testing.T) {
	state := newStreamState("")
	usages := replayUsages(t, state, []string{
		`{"type":"event","event":{"type":"model_request_end","usage":{"inputTokens":10,"outputTokens":100,"cacheReadInputTokens":5}}}`,
		`{"type":"event","event":{"type":"model_request_end","usage":{"inputTokens":20,"outputTokens":250,"cacheCreationInputTokens":7}}}`,
	})
	if len(usages) != 2 {
		t.Fatalf("want 2 usage events, got %d: %+v", len(usages), usages)
	}
	if usages[0].InputTokens != 10 || usages[0].OutputTokens != 100 || usages[0].CachedTokens != 5 {
		t.Fatalf("第一次请求的用量 = %+v, want 10 / 100 / 5", usages[0])
	}
	if usages[1].InputTokens != 30 || usages[1].OutputTokens != 350 || usages[1].CachedTokens != 12 {
		t.Fatalf("第二次请求应带上整轮累计用量 30 / 350 / 12，得到 %+v", usages[1])
	}

	next := replayUsages(t, newStreamState(""), []string{
		`{"type":"event","event":{"type":"model_request_end","usage":{"inputTokens":4,"outputTokens":40}}}`,
	})
	if len(next) != 1 || next[0].InputTokens != 4 || next[0].OutputTokens != 40 {
		t.Fatalf("新一轮应从 0 重新累计，得到 %+v", next)
	}
}

// replayUsages 把帧喂给 state，返回收到的用量事件（按顺序）。
func replayUsages(t *testing.T, state *streamState, frames []string) []harness.Usage {
	t.Helper()
	var usages []harness.Usage
	emit := func(event harness.Event) {
		if event.Type == harness.EventUsage && event.Usage != nil {
			usages = append(usages, *event.Usage)
		}
	}
	for _, raw := range frames {
		frame, err := parseFrame([]byte(raw))
		if err != nil {
			t.Fatalf("解析帧失败: %v", err)
		}
		state.handle(frame, emit)
	}
	return usages
}
