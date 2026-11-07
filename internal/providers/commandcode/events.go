package commandcode

// 本文件把 `cmd -p` stdout 上的按行 JSON 帧翻译成 harness 事件。

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"mcode/internal/harness"
)

// frame 是 stdout 上的一行 JSON 顶帧：type 为 event 或 result。
// event 帧的载荷在 event 字段里，result 帧带 subtype / stopReason / sessionId / error。
type frame struct {
	Type       string          `json:"type"`
	Event      json.RawMessage `json:"event"`
	Subtype    string          `json:"subtype"`
	StopReason string          `json:"stopReason"`
	SessionID  string          `json:"sessionId"`
	Error      json.RawMessage `json:"error"`
}

// eventFrame 是 event 帧里的载荷；字段按 handleEvent 的分支保留。
type eventFrame struct {
	Type string `json:"type"`

	SessionID string          `json:"sessionId"`
	Delta     string          `json:"delta"`
	Text      string          `json:"text"`
	Usage     json.RawMessage `json:"usage"`
	Content   json.RawMessage `json:"content"`

	ToolCallID  string          `json:"toolCallId"`
	ToolName    string          `json:"toolName"`
	Input       json.RawMessage `json:"input"`
	Result      json.RawMessage `json:"result"`
	Error       json.RawMessage `json:"error"`
	HookOutput  string          `json:"hookOutput"`
	DenyMessage string          `json:"denyMessage"`
}

// parseFrame 解析 stdout 上的一行 JSON。空行与缺少 type 的行返回 nil, nil；
// 非法 JSON 返回错误，调用方可以忽略（与 pi 包同样容忍未知行）。
func parseFrame(line []byte) (*frame, error) {
	var parsed frame
	if err := json.Unmarshal(line, &parsed); err != nil {
		return nil, fmt.Errorf("commandcode: parse frame: %w", err)
	}
	if parsed.Type == "" {
		return nil, nil
	}
	return &parsed, nil
}

// runResult 是 result 帧里决定当轮结局的部分。
type runResult struct {
	Subtype    string
	StopReason string
	Error      string
}

// streamState 保存一轮对话的事件翻译状态。
type streamState struct {
	// sessionID 是本轮 CLI 实际使用的会话 id；run_start / result 报出新 id 时更新。
	sessionID string

	streamedText     bool
	streamedThinking bool

	// clock 为本次模型请求计时；requestSpeeds 是本轮已完成的请求速度样本。
	clock         harness.RequestClock
	requestSpeeds []harness.RequestSpeed
	// now 返回当前时间；nil 时用 time.Now，测试可注入假时钟。
	now func() time.Time

	result      *runResult
	runError    string
	interrupted bool

	doneEmitted bool
}

// newStreamState 创建一轮的事件翻译状态，sessionID 是本轮启动时的会话 id。
func newStreamState(sessionID string) *streamState {
	return &streamState{sessionID: sessionID}
}

// nowTime 返回当前时间；测试可注入假时钟。
func (s *streamState) nowTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// beginRequest 在本次请求的第一个内容帧到达时开始计时。
func (s *streamState) beginRequest() {
	s.clock.Begin(s.nowTime())
}

// appendRequestSpeed 结算本次请求的速度样本：没有内容帧或时长过短时不追加。
func (s *streamState) appendRequestSpeed(outputTokens int64) {
	if speed, ok := s.clock.Finish(s.nowTime(), outputTokens); ok {
		s.requestSpeeds = append(s.requestSpeeds, speed)
	}
}

// requestSpeedSamples 返回本轮样本的拷贝，避免事件里带上内部切片。
func requestSpeedSamples(samples []harness.RequestSpeed) []harness.RequestSpeed {
	if len(samples) == 0 {
		return nil
	}
	return append([]harness.RequestSpeed(nil), samples...)
}

// handle 处理一条 stdout 帧。
func (s *streamState) handle(parsed *frame, emit func(harness.Event)) {
	switch parsed.Type {
	case "event":
		s.handleEvent(parsed.Event, emit)
	case "result":
		s.result = &runResult{
			Subtype:    firstNonEmpty(parsed.Subtype, "success"),
			StopReason: parsed.StopReason,
			Error:      errorText(parsed.Error),
		}
		s.captureSession(parsed.SessionID, emit)
	default:
		// 未知顶帧类型忽略。
	}
}

// handleEvent 处理 event 帧的载荷。
func (s *streamState) handleEvent(raw json.RawMessage, emit func(harness.Event)) {
	if len(raw) == 0 {
		return
	}
	var event eventFrame
	if err := json.Unmarshal(raw, &event); err != nil {
		return
	}
	// 工具没执行成功时统一上报 tool_end：被 hook 拦截的额外标记 blocked，便于区分拦截与故障。
	emitRejected := func(blocked bool) {
		if event.ToolCallID == "" {
			return
		}
		reason := firstNonEmpty(event.HookOutput, event.DenyMessage, "已被拒绝。")
		_ = harness.Emit(emit, harness.Event{
			Type:    harness.EventToolEnd,
			ToolID:  event.ToolCallID,
			Output:  reason,
			IsError: true,
			Blocked: blocked,
		})
	}
	switch event.Type {
	case "run_start":
		s.captureSession(event.SessionID, emit)
	case "message_start":
		// 新一轮助手消息开始，重置流式标记。
		s.streamedText = false
	case "text_delta":
		if event.Delta == "" {
			return
		}
		s.beginRequest()
		s.streamedText = true
		_ = harness.Emit(emit, harness.Event{Type: harness.EventText, Text: event.Delta})
	case "thinking_start":
		s.streamedThinking = false
	case "thinking_delta":
		if event.Delta == "" {
			return
		}
		s.beginRequest()
		s.streamedThinking = true
		_ = harness.Emit(emit, harness.Event{Type: harness.EventReasoning, Text: event.Delta})
	case "thinking_end":
		// 思考整段到达（没有增量）时补一次 reasoning，避免丢内容。
		if !s.streamedThinking && strings.TrimSpace(event.Text) != "" {
			_ = harness.Emit(emit, harness.Event{Type: harness.EventReasoning, Text: event.Text})
		}
		s.streamedThinking = false
	case "message_end":
		// 回复整段到达（没有 text_delta）时补一次文本。
		if !s.streamedText {
			if text := contentText(event.Content); text != "" {
				_ = harness.Emit(emit, harness.Event{Type: harness.EventText, Text: text})
			}
		}
		s.streamedText = false
		s.streamedThinking = false
	case "model_request_end":
		usage, ok := parseUsage(event.Usage)
		if !ok {
			// 没有可用用量：本次请求不算样本，但要清掉计时，避免影响下一次请求。
			s.clock.Reset()
			return
		}
		s.appendRequestSpeed(usage.OutputTokens)
		usage.Requests = requestSpeedSamples(s.requestSpeeds)
		_ = harness.Emit(emit, harness.Event{Type: harness.EventUsage, Usage: &usage})
	case "tool_queued":
		if event.ToolCallID == "" || event.ToolName == "" {
			return
		}
		s.beginRequest()
		_ = harness.Emit(emit, harness.Event{
			Type:     harness.EventToolStart,
			ToolID:   event.ToolCallID,
			ToolName: event.ToolName,
			Input:    event.Input,
		})
		// 写改类工具额外上报路径，供消费方统计改动文件（对照 claude 的 Edit/Write 与 pi 的 write/edit）。
		if event.ToolName == "write_file" || event.ToolName == "edit_file" {
			path := inputString(event.Input, "file_path")
			if path == "" {
				path = inputString(event.Input, "path")
			}
			if path != "" {
				_ = harness.Emit(emit, harness.Event{Type: harness.EventFileChange, Path: path})
			}
		}
	case "tool_completed":
		if event.ToolCallID == "" {
			return
		}
		_ = harness.Emit(emit, harness.Event{
			Type:   harness.EventToolEnd,
			ToolID: event.ToolCallID,
			Output: contentText(event.Result),
		})
	case "tool_errored":
		if event.ToolCallID == "" {
			return
		}
		_ = harness.Emit(emit, harness.Event{
			Type:    harness.EventToolEnd,
			ToolID:  event.ToolCallID,
			Output:  firstNonEmpty(errorText(event.Error), "工具执行失败。"),
			IsError: true,
		})
	case "tool_hook_blocked":
		emitRejected(true)
	case "tool_denied":
		emitRejected(false)
	case "run_error":
		s.runError = errorText(event.Error)
	case "interrupted":
		s.interrupted = true
	default:
		// turn_start / turn_end / message_update / model_request_start / model_trace /
		// tool_running / tool_update（部分输出，harness 无对应事件）/ tool_hooks /
		// tool_input_repaired / subagent_* / session_titled / compaction_* / notice /
		// run_end 等没有可映射的 harness 事件，忽略；尚未实现的清单见 README.md。
	}
}

// captureSession 在 CLI 报出新的会话 id 时更新状态并发一次 session 事件。
func (s *streamState) captureSession(id string, emit func(harness.Event)) {
	if id == "" || id == s.sessionID {
		return
	}
	s.sessionID = id
	_ = harness.Emit(emit, harness.Event{Type: harness.EventSession, SessionID: id})
}

// finishRun 在进程退出后收尾当轮（判定顺序）：
// 先看中止，再看 result 帧，最后按退出码兜底。stderrText 是子进程 stderr 的尾部。
func (s *streamState) finishRun(status int, stopped bool, stderrText string, emit func(harness.Event)) {
	if s.interrupted || status == 130 || stopped {
		s.emitDone("interrupted", emit)
		return
	}
	if s.result != nil {
		switch s.result.Subtype {
		case "success", "max_turns":
			// max_turns 不是失败：这一轮到了往返上限，提示一句后按完成收尾；
			// harness 没有提示类事件，这里把上限写进结束原因。
			s.emitDone(turnCapStopReason(s.result), emit)
		default:
			message := firstNonEmpty(s.result.Error, s.runError, lastMeaningfulLines(stderrText), "Command Code 在完成前停止。")
			s.fail(friendlyError(message), emit)
			s.emitDone("error", emit)
		}
		return
	}
	// 没有 result 帧：CLI 在写出来之前就退出了，退出码说明原因。
	switch status {
	case 0:
		s.emitDone("completed", emit)
	case 3:
		s.fail("Command Code 未登录：请在终端运行 `cmd login`，或在设置中填入 API key。", emit)
		s.emitDone("error", emit)
	case 4:
		s.fail(firstNonEmpty(lastMeaningfulLines(stderrText), "Command Code 被拒绝授权。"), emit)
		s.emitDone("error", emit)
	case 5:
		s.fail(firstNonEmpty(lastMeaningfulLines(stderrText), "Command Code 已被限流：请等待窗口重置，或购买不受限流的按需额度。"), emit)
		s.emitDone("error", emit)
	case 6:
		s.fail("无法连接 Command Code：请检查网络后重试。", emit)
		s.emitDone("error", emit)
	case 7:
		s.fail(firstNonEmpty(lastMeaningfulLines(stderrText), "Command Code 的服务端返回错误：请稍后重试。"), emit)
		s.emitDone("error", emit)
	case 8:
		// 达到往返上限：按完成收尾，结束原因标出上限。
		s.emitDone(stopReasonMaxTurns, emit)
	case 9:
		s.fail("模型没有产出任何回复。", emit)
		s.emitDone("error", emit)
	case 10:
		s.fail("Command Code 额度已用尽：请在 commandcode.ai/billing 充值，或等待月度额度重置。", emit)
		s.emitDone("error", emit)
	default:
		message := firstNonEmpty(s.runError, lastMeaningfulLines(stderrText), fmt.Sprintf("Command Code 以退出码 %d 结束。", status))
		s.fail(friendlyError(message), emit)
		s.emitDone("error", emit)
	}
}

// stopReasonMaxTurns 是撞上往返上限时的结束原因：不是失败，但让消费方知道这轮被上限截断
// （提示撞上往返上限）。
const stopReasonMaxTurns = "max_turns"

// turnCapStopReason 判断当轮是否撞上了往返上限：result 帧的子类型或停止原因任一说
// max_turns 都算。
func turnCapStopReason(result *runResult) string {
	if result.Subtype == "max_turns" || result.StopReason == "max_turns" {
		return stopReasonMaxTurns
	}
	return "completed"
}

// fail 发出一次错误事件。
func (s *streamState) fail(message string, emit func(harness.Event)) {
	if message == "" {
		message = "Command Code 报告了一个错误。"
	}
	_ = harness.Emit(emit, harness.Event{Type: harness.EventError, Err: message})
}

// emitDone 发出当轮结束事件，只生效一次。
func (s *streamState) emitDone(stopReason string, emit func(harness.Event)) {
	if s.doneEmitted {
		return
	}
	s.doneEmitted = true
	_ = harness.Emit(emit, harness.Event{Type: harness.EventDone, StopReason: stopReason})
}

// friendlyError 把 CLI 的账号类错误文案换成可操作的提示。
func friendlyError(text string) string {
	lowered := strings.ToLower(text)
	if strings.Contains(lowered, "not authenticated") || strings.Contains(lowered, "unauthorized") || strings.Contains(lowered, "session expired") {
		return "Command Code 未登录：请在终端运行 `cmd login`，或在设置中填入 API key。"
	}
	if strings.Contains(lowered, "insufficient credits") || strings.Contains(lowered, "out of credits") {
		return "Command Code 额度已用尽：请在 commandcode.ai/billing 充值，或等待月度额度重置。"
	}
	return text
}

// errorText 把 error 字段转成文本：字符串直接用，对象取 message，其余取压缩 JSON。
func errorText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	if len(object) == 0 {
		return ""
	}
	if message := jsonText(object["message"]); message != "" {
		return message
	}
	compact, err := json.Marshal(object)
	if err != nil || string(compact) == "{}" {
		return ""
	}
	return string(compact)
}

// contentText 取 content / result 载荷里的文本：字符串直接用，内容块数组拼接 text 块
// （image 块记作 [image]）。
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Text != "" {
			parts = append(parts, block.Text)
			continue
		}
		if block.Type == "image" {
			parts = append(parts, "[image]")
		}
	}
	return strings.Join(parts, "\n")
}

// parseUsage 解析 model_request_end 的用量载荷，键名兼容 UsageNormalizer 的通用表。
// 全部为 0 时返回 false（不发空用量事件）；只有总量时把总量记到输入侧，避免丢用量。
func parseUsage(raw json.RawMessage) (harness.Usage, bool) {
	if len(raw) == 0 {
		return harness.Usage{}, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return harness.Usage{}, false
	}
	pick := func(keys ...string) (int64, bool) {
		for _, key := range keys {
			if value, ok := fields[key]; ok {
				if n, ok := jsonInt(value); ok {
					return n, true
				}
			}
		}
		return 0, false
	}

	input, _ := pick("input_tokens", "inputTokens", "prompt_tokens", "promptTokens", "input")
	output, _ := pick("output_tokens", "outputTokens", "completion_tokens", "completionTokens", "output")
	cacheRead, _ := pick("cached_input_tokens", "cachedInputTokens", "cache_read_input_tokens", "cacheRead", "cacheReadTokens", "cache_read_tokens", "cache_read", "cacheReadInputTokens")
	cacheWrite, _ := pick("cache_creation_input_tokens", "cacheWriteTokens", "cache_write_tokens", "cacheCreation", "cacheWrite", "cache_write", "cache_write_input_tokens", "cacheWriteInputTokens", "cacheCreationInputTokens")
	total, hasTotal := pick("totalTokens", "total_tokens", "total")
	if input == 0 && output == 0 && cacheRead == 0 && cacheWrite == 0 {
		if !hasTotal || total <= 0 {
			return harness.Usage{}, false
		}
		input = total
	}
	return harness.Usage{
		InputTokens:  input,
		OutputTokens: output,
		CachedTokens: cacheRead + cacheWrite,
	}, true
}

// inputString 从工具入参里取字符串字段。
func inputString(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ""
	}
	return jsonText(fields[key])
}

// jsonInt 把 JSON 数字解析为 int64。
func jsonInt(raw json.RawMessage) (int64, bool) {
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		if n, err := number.Int64(); err == nil {
			return n, true
		}
		if f, err := number.Float64(); err == nil {
			return int64(f), true
		}
	}
	return 0, false
}

// lastMeaningfulLines 取末尾至多 4 行非空文本
// （这里不做 ANSI 与噪声过滤）。
func lastMeaningfulLines(text string) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	return strings.Join(lines, "\n")
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
