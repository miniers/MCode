package omp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"mcode/internal/harness"
)

// omp `--mode rpc-ui` 的请求类型常量，与 stdin 上的 JSON 行协议一致。
const (
	rpcTypePrompt          = "prompt"
	rpcTypeSteer           = "steer"
	rpcTypeGetState        = "get_state"
	rpcTypeGetCommands     = "get_available_commands"
	rpcTypeAbort           = "abort"
	rpcTypeExtensionUIResp = "extension_ui_response"
)

// rpcRequest 是写往 omp stdin 的一行 JSON 请求。
type rpcRequest struct {
	Type    string         `json:"type"`
	ID      string         `json:"id,omitempty"`
	Message string         `json:"message,omitempty"`
	Images  []imagePayload `json:"images,omitempty"`
}

// imagePayload 是 prompt 请求中携带的一张图片。
type imagePayload struct {
	Type     string `json:"type"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

// extensionUIResponse 是写回 omp 扩展的交互应答：
// value 用于 select/input/editor，confirmed 用于 confirm，
// cancelled 表示用户放弃。
type extensionUIResponse struct {
	Type      string  `json:"type"`
	ID        string  `json:"id"`
	Value     *string `json:"value,omitempty"`
	Cancelled *bool   `json:"cancelled,omitempty"`
	Confirmed *bool   `json:"confirmed,omitempty"`
}

var requestSeq atomic.Uint64

// nextRequestID 返回进程内唯一的请求 id。
func nextRequestID() string {
	return fmt.Sprintf("mcode-%d", requestSeq.Add(1))
}

// requestLine 把请求序列化为一行 JSON（含结尾换行）。
func requestLine(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// getAvailableCommandsRequest 构造取命令表的请求：门禁扩展加载成功时，命令表里有
// mcode-gate。
func getAvailableCommandsRequest(id string) rpcRequest {
	return rpcRequest{Type: rpcTypeGetCommands, ID: id}
}

// getStateRequest 构造取会话状态的请求，应答里带 omp 的会话文件路径与上下文窗口。
func getStateRequest(id string) rpcRequest {
	return rpcRequest{Type: rpcTypeGetState, ID: id}
}

// promptRequest 构造一轮提问请求。
func promptRequest(id, message string, images []imagePayload) rpcRequest {
	req := rpcRequest{Type: rpcTypePrompt, ID: id, Message: message}
	if len(images) > 0 {
		req.Images = images
	}
	return req
}

// steerRequest 构造轮内追加指引的请求；当轮以同 id 的 prompt_result 收尾，不受影响。
func steerRequest(id, message string) rpcRequest {
	return rpcRequest{Type: rpcTypeSteer, ID: id, Message: message}
}

// abortRequest 构造中断当轮的请求。
func abortRequest() rpcRequest {
	return rpcRequest{Type: rpcTypeAbort}
}

func valueResponse(id, value string) extensionUIResponse {
	return extensionUIResponse{Type: rpcTypeExtensionUIResp, ID: id, Value: &value}
}

func cancelledResponse(id string) extensionUIResponse {
	cancelled := true
	return extensionUIResponse{Type: rpcTypeExtensionUIResp, ID: id, Cancelled: &cancelled}
}

func confirmedResponse(id string, confirmed bool) extensionUIResponse {
	return extensionUIResponse{Type: rpcTypeExtensionUIResp, ID: id, Confirmed: &confirmed}
}

// imagePayloads 读取本地图片并编码为 prompt 请求携带的图片载荷；读不到的图片静默跳过。
// 不是图片的附件（PDF、docx、表格、压缩包等）同样跳过：它们的路径已由上层写进
// Prompt，agent 可以用文件工具读取。图片类型由 harness.ImageMediaType 判定：扩展名优先，
// 扩展名不可靠时按文件内容嗅探（会话媒体目录里的历史文件没有扩展名）。
func imagePayloads(paths []string) []imagePayload {
	var images []imagePayload
	for _, path := range paths {
		mimeType, ok := harness.ImageMediaType(path)
		if !ok {
			log.Printf("omp: skip image %s: unsupported image type %q", path, filepath.Ext(path))
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		images = append(images, imagePayload{
			Type:     "image",
			Data:     base64.StdEncoding.EncodeToString(data),
			MimeType: mimeType,
		})
	}
	return images
}

// rpcEvent 是 omp stdout 上的一行 JSON 帧。
// 帧类型多且字段形态不一，这里按需保留原始 JSON，由 translate 分派。
type rpcEvent struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	RequestID string          `json:"requestId"`
	Success   *bool           `json:"success"`
	Command   string          `json:"command"`
	Message   json.RawMessage `json:"message"`
	Data      json.RawMessage `json:"data"`
	Usage     json.RawMessage `json:"usage"`
	// Error 在 response 里是字符串，在 prompt_result 里是对象，两种形态都由
	// errorMessage 归一。
	Error json.RawMessage `json:"error"`

	// prompt_result：completed / error / aborted
	Status string `json:"status"`

	// message_update 的增量载荷
	AssistantMessageEvent *deltaPayload `json:"assistantMessageEvent"`

	// tool_execution_*
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Args       json.RawMessage `json:"args"`
	Input      json.RawMessage `json:"input"`
	Result     *toolResult     `json:"result"`
	IsError    bool            `json:"isError"`

	// extension_ui_request：扩展交互请求
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`

	// auto_retry_start / auto_retry_end：omp 自行重试失败请求的进度与结果
	Attempt      int    `json:"attempt"`
	MaxAttempts  int    `json:"maxAttempts"`
	ErrorMessage string `json:"errorMessage"`
	FinalError   string `json:"finalError"`
}

// deltaPayload 是 message_update 中的增量载荷（assistantMessageEvent）。
type deltaPayload struct {
	Type  string          `json:"type"`
	Delta string          `json:"delta"`
	Usage json.RawMessage `json:"usage"`
}

// messagePayload 是 message_start / message_end 中的 message 字段。
type messagePayload struct {
	Role         string          `json:"role"`
	Content      []contentBlock  `json:"content"`
	Usage        json.RawMessage `json:"usage"`
	StopReason   string          `json:"stopReason"`
	ErrorMessage string          `json:"errorMessage"`
}

// contentBlock 是消息与工具结果中的单个文本块。
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// toolResult 是 tool_execution_end 的结果载荷。
// 结果详情（exitCode、oldText/newText 等）没有对应的 harness 字段，这里不解析。
type toolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError"`
}

// parseEvent 解析 omp stdout 上的一行 JSON。空行与缺少 type 的行返回 nil, nil；
// 非法 JSON 返回错误，调用方可以忽略（与 pi/claude 包同样容忍未知行）。
func parseEvent(line []byte) (*rpcEvent, error) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return nil, nil
	}
	var ev rpcEvent
	if err := json.Unmarshal([]byte(trimmed), &ev); err != nil {
		return nil, fmt.Errorf("omp: parse event: %w", err)
	}
	if ev.Type == "" {
		return nil, nil
	}
	return &ev, nil
}

// streamState 保存一轮对话的事件翻译状态。
type streamState struct {
	// promptID 是当轮 prompt 请求的 id：prompt_result 与失败的 prompt 应答靠它识别。
	promptID string
	// streamedText 表示本轮 assistant 消息是否已有文本增量，避免 message_end 重复补发。
	streamedText bool
	// messageError 是最近一条以 error 收尾的 assistant 消息的说明，作为当轮错误的兜底文案。
	messageError string
	interrupted  bool
	// turnErr 是当轮的失败原因，finish 时补发一次错误事件；已发过的错误用 errSent 去重。
	turnErr string
	// errSent 表示当轮已经发过错误事件，保证同一轮最多报一次。
	errSent bool
	sawDone bool
	// usage 是本轮所有模型请求的累计用量。remote 对同一轮用量是覆盖写，而 omp 每次模型请求
	// 都在 message_end 上报一次「这一次请求」的用量，所以必须在这里逐次累加。
	usage harness.Usage
	// clock 为当前这次模型请求计时：首个流式内容帧到达时 Begin，用量帧到达时 Finish。
	clock harness.RequestClock
	// requests 是本轮已完成的每次模型请求的速度样本，随每次用量事件完整上报。
	requests []harness.RequestSpeed
	// messageStart 是当前助手消息开始的时刻，只在拿不到增量帧时作为计时起点兜底。
	messageStart time.Time
	// now 返回当前时间，测试可注入固定时钟；为 nil 时用 time.Now。
	now func() time.Time
}

// newStreamState 创建一轮的事件翻译状态。
func newStreamState() *streamState {
	return &streamState{}
}

// translate 把一条 omp 帧翻译为 harness 事件，返回当轮是否已经结束。
// extension_ui_request 由 Harness.handleExtensionUI 应答；命令表与 get_state 应答由 Run 消费。
func (s *streamState) translate(ev *rpcEvent, emit func(harness.Event)) bool {
	switch ev.Type {
	case "response":
		if s.promptID == "" || ev.ID != s.promptID {
			return false
		}
		if ev.Success != nil && !*ev.Success {
			s.fail(firstNonEmpty(ev.errorMessage(), "omp reported an error."), emit)
			return s.finish(emit)
		}
		if !agentInvoked(ev) {
			// 斜杠命令由 omp 自行执行：没有后续轮次。
			return s.finish(emit)
		}
		return false
	case "prompt_result":
		if s.promptID == "" || ev.ID != s.promptID {
			return false
		}
		switch ev.Status {
		case "error":
			s.fail(firstNonEmpty(ev.errorMessage(), s.messageError, "omp reported an error."), emit)
		case "aborted":
			s.interrupted = true
		}
		return s.finish(emit)
	case "message_start":
		if messageRole(ev.Message) == "assistant" {
			s.streamedText = false
			// 新的请求重新计时；这里只记起点，真正 Begin 留到首个内容帧，
			// 否则会把首 token 延迟算进生成时长。
			s.clock.Reset()
			s.messageStart = s.requestNow()
		}
		return false
	case "message_update":
		s.handleUpdate(ev, emit)
		return false
	case "message_end":
		s.handleMessageEnd(ev, emit)
		return false
	case "tool_execution_start":
		if ev.ToolCallID == "" || ev.ToolName == "" {
			return false
		}
		input := ev.Args
		if len(input) == 0 {
			input = ev.Input
		}
		_ = harness.Emit(emit, harness.Event{
			Type:     harness.EventToolStart,
			ToolID:   ev.ToolCallID,
			ToolName: ev.ToolName,
			Input:    input,
		})
		// 文件写改类工具额外上报路径，供消费方统计改动文件。
		if path := editedFilePath(ev.ToolName, input); path != "" {
			_ = harness.Emit(emit, harness.Event{Type: harness.EventFileChange, Path: path})
		}
		return false
	case "tool_execution_end":
		if ev.ToolCallID == "" {
			return false
		}
		output := ""
		isError := ev.IsError
		if ev.Result != nil {
			output = contentText(ev.Result.Content)
			isError = isError || ev.Result.IsError
		}
		_ = harness.Emit(emit, harness.Event{
			Type:    harness.EventToolEnd,
			ToolID:  ev.ToolCallID,
			Output:  output,
			IsError: isError,
		})
		return false
	case "auto_retry_start":
		// 可重试的模型错误会被 omp 自行重试：只提示进度，错误仍由 prompt_result 或 finish 报。
		_ = harness.Emit(emit, harness.Event{Type: harness.EventReasoning, Text: retryStartText(ev)})
		return false
	case "auto_retry_end":
		// 重试用尽：记下最终失败原因，供 prompt_result 缺失时在 finish 补报。
		if ev.Success != nil && !*ev.Success && ev.FinalError != "" {
			s.turnErr = ev.FinalError
		}
		return false
	default:
		// agent_end / agent_settled（omp 在重试前也会发，当轮终点是 prompt_result）、
		// tool_execution_update（部分输出没有对应事件）、subagent_*（子代理 Bee 尚未实现）、
		// command_output / session_info_update / available_commands_update、auto_compaction_*、
		// notice、extension_error 等没有对应的 harness 事件，忽略。
		return false
	}
}

// agentInvoked 判断 prompt 应答是否真的起了模型轮次：data.agentInvoked 显式为 false 时
// 表示 omp 自行处理了这条斜杠命令，没有后续轮次；缺省按 true 处理。
func agentInvoked(ev *rpcEvent) bool {
	data := ev.Data
	if len(data) == 0 {
		data = ev.Message
	}
	value, ok := jsonBool(jsonObject(data), "agentInvoked")
	if !ok {
		return true
	}
	return value
}

// handleUpdate 处理 message_update 的文本/思考增量与用量。
func (s *streamState) handleUpdate(ev *rpcEvent, emit func(harness.Event)) {
	delta := ev.AssistantMessageEvent
	if delta != nil {
		// 文本、思考、工具调用等任何带内容的增量帧都标志生成开始；只带用量的帧不计。
		if delta.Delta != "" {
			s.clock.Begin(s.requestNow())
		}
		switch delta.Type {
		case "text_delta":
			if delta.Delta != "" {
				s.streamedText = true
				_ = harness.Emit(emit, harness.Event{Type: harness.EventText, Text: delta.Delta})
			}
		case "thinking_delta":
			if delta.Delta != "" {
				_ = harness.Emit(emit, harness.Event{Type: harness.EventReasoning, Text: delta.Delta})
			}
		}
	}
	usage := messageUsage(ev.Message)
	if len(usage) == 0 {
		// 兼容 pi 家族把 usage 挂在帧顶层的写法。
		usage = ev.Usage
	}
	if u, ok := parseUsage(usage); ok {
		s.emitPartialUsage(u, parseUsageCost(usage), emit)
	}
}

// handleMessageEnd 处理 assistant 消息结束：结算用量、补发未流式的正文、记录错误文案。
// 消息以 error 收尾时当轮未必失败（omp 可能自行重试），当轮判决在 prompt_result，
// 所以这里不发错误事件，只留下说明供 prompt_result 兜底。
func (s *streamState) handleMessageEnd(ev *rpcEvent, emit func(harness.Event)) {
	message, ok := decodeMessage(ev.Message)
	if !ok || message.Role != "assistant" {
		return
	}
	if u, ok := parseUsage(message.Usage); ok {
		s.addUsage(u, parseUsageCost(message.Usage), emit)
	}
	// 没有任何增量文本时（整段回复一次到达），用最终内容补一次文本。
	if !s.streamedText {
		if text := contentText(message.Content); text != "" {
			_ = harness.Emit(emit, harness.Event{Type: harness.EventText, Text: text})
		}
	}
	if message.StopReason == "error" && message.ErrorMessage != "" {
		s.messageError = message.ErrorMessage
	}
}

// addUsage 把一次模型请求的用量并入本轮累计，并发出累计用量事件。
// 失败后被重试的请求同样计费，所以一律计入。
func (s *streamState) addUsage(u harness.Usage, costUSD float64, emit func(harness.Event)) {
	// 没有收到任何增量帧时退回消息开始时刻计时（会含首 token 延迟，仅作兜底）。
	s.clock.Begin(s.messageStart)
	if speed, ok := s.clock.Finish(s.requestNow(), u.OutputTokens); ok {
		s.requests = append(s.requests, speed)
	}
	s.usage.InputTokens += u.InputTokens
	s.usage.OutputTokens += u.OutputTokens
	s.usage.CachedTokens += u.CachedTokens
	s.usage.CostUSD += costUSD
	total := s.usage
	total.ContextTokens = u.InputTokens + u.OutputTokens
	total.Requests = s.requestSamples()
	_ = harness.Emit(emit, harness.Event{Type: harness.EventUsage, Usage: &total})
}

// requestNow 返回当前时间，测试可注入固定时钟。
func (s *streamState) requestNow() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// requestSamples 返回本轮请求样本的拷贝，避免消费方持有内部切片。
func (s *streamState) requestSamples() []harness.RequestSpeed {
	if len(s.requests) == 0 {
		return nil
	}
	return append([]harness.RequestSpeed(nil), s.requests...)
}

// emitPartialUsage 发出「本轮累计 + 当前这次请求的部分值」，但不并入累计：
// message_update 上报的是这次请求的实时用量，正式金额等 message_end 再累加。
func (s *streamState) emitPartialUsage(u harness.Usage, costUSD float64, emit func(harness.Event)) {
	total := s.usage
	total.InputTokens += u.InputTokens
	total.OutputTokens += u.OutputTokens
	total.CachedTokens += u.CachedTokens
	total.CostUSD += costUSD
	total.ContextTokens = u.InputTokens + u.OutputTokens
	total.Requests = s.requestSamples()
	_ = harness.Emit(emit, harness.Event{Type: harness.EventUsage, Usage: &total})
}

// fail 记录并立即发出一次终止性错误事件。
// 可能被 omp 重试掉的错误不要走这里，改为写 turnErr 由 finish 补发。
func (s *streamState) fail(message string, emit func(harness.Event)) {
	s.turnErr = message
	s.emitError(message, emit)
}

// emitError 发出错误事件，同一轮最多发一次。
func (s *streamState) emitError(message string, emit func(harness.Event)) {
	if s.errSent || message == "" {
		return
	}
	s.errSent = true
	_ = harness.Emit(emit, harness.Event{Type: harness.EventError, Err: message})
}

// finish 发出 done 事件并标记当轮结束，只生效一次。
func (s *streamState) finish(emit func(harness.Event)) bool {
	if s.sawDone {
		return true
	}
	s.sawDone = true
	// 重试用尽或被终止时错误还没报过，这里补发一次。
	s.emitError(s.turnErr, emit)
	stopReason := "completed"
	switch {
	case s.turnErr != "":
		stopReason = "error"
	case s.interrupted:
		stopReason = "interrupted"
	}
	_ = harness.Emit(emit, harness.Event{Type: harness.EventDone, StopReason: stopReason})
	return true
}

// editedFilePath 取写改类工具的目标文件路径：
// write 取 path 参数，edit / ast_edit 取 path 参数或 hashline 输入头部 `[path#tag]` 里的路径。
// 写到扩展设备（xd://<tool>）的不是文件改动。
func editedFilePath(toolName string, input json.RawMessage) string {
	switch toolName {
	case "write", "edit", "ast_edit":
	default:
		return ""
	}
	object := jsonObject(input)
	if object == nil {
		return ""
	}
	if path := jsonText(object, "path"); path != "" {
		if strings.HasPrefix(path, "xd://") {
			return ""
		}
		return path
	}
	if text := jsonText(object, "input"); text != "" {
		if match := ompHashlinePathRegexp.FindStringSubmatch(text); len(match) == 2 {
			return match[1]
		}
	}
	return ""
}

// ompHashlinePathRegexp 匹配 edit 的 hashline 输入头部 `[path#tag]`。
var ompHashlinePathRegexp = regexp.MustCompile(`\[([^\]#]+)#[0-9A-Za-z]+\]`)

// errorMessage 取出 error 字段的文本：字符串直接用（prompt 失败应答），
// 对象取它的 message（prompt_result 的错误载荷）。
func (ev *rpcEvent) errorMessage() string {
	if len(ev.Error) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(ev.Error, &text); err == nil {
		return text
	}
	return jsonText(jsonObject(ev.Error), "message")
}

// parseUsage 解析 omp 的用量载荷，兼容多种键名。
// omp 上报的 input 不含缓存读写，这里把 cacheRead + cacheWrite 并入 InputTokens
// （与 codex 口径一致：InputTokens 已包含缓存命中部分），
// CachedTokens 单独保留缓存命中量供上层展示，不再重复计入上下文估算之外。
// 只带 totalTokens 的载荷把总量记到输入侧，避免丢失用量。
func parseUsage(raw json.RawMessage) (harness.Usage, bool) {
	if len(raw) == 0 {
		return harness.Usage{}, false
	}
	fields := jsonObject(raw)
	if fields == nil {
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

	input, _ := pick("input", "inputTokens", "input_tokens")
	output, _ := pick("output", "outputTokens", "output_tokens")
	cacheRead, _ := pick("cacheRead", "cacheReadTokens", "cache_read_tokens")
	cacheWrite, _ := pick("cacheWrite", "cacheWriteTokens", "cache_write_tokens")
	total, hasTotal := pick("totalTokens", "total_tokens")
	if !hasTotal {
		total = input + output + cacheRead + cacheWrite
	}
	if total <= 0 {
		return harness.Usage{}, false
	}
	if input == 0 && output == 0 && cacheRead == 0 && cacheWrite == 0 {
		input = total
	}
	return harness.Usage{
		InputTokens:  input + cacheRead + cacheWrite,
		OutputTokens: output,
		CachedTokens: cacheRead + cacheWrite,
	}, true
}

// retryStartText 把 auto_retry_start 翻译成一行中文提示，以换行结尾以便单独成段。
// 错误摘要截断到 200 个字符以内，避免一屏提示被长报文占满。
func retryStartText(ev *rpcEvent) string {
	message := truncateText(firstNonEmpty(ev.ErrorMessage, "未知错误"), 200)
	return fmt.Sprintf("[omp 请求失败，正在重试 %d/%d：%s]\n", ev.Attempt, ev.MaxAttempts, message)
}

// truncateText 把文本截断到 limit 个字符（按 rune 计），被截断时末尾加省略号。
func truncateText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}

// parseUsageCost 取用量载荷里的费用（美元）：omp 的 usage.cost.total。
// 没有费用字段的载荷返回 0，累加时不影响其它请求。
func parseUsageCost(raw json.RawMessage) float64 {
	if len(raw) == 0 {
		return 0
	}
	var fields struct {
		Cost struct {
			Total float64 `json:"total"`
		} `json:"cost"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return 0
	}
	return fields.Cost.Total
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

// sessionIDFromState 从 get_state 应答中取出 omp 的会话文件路径，作为后续轮次的 --resume id。
func sessionIDFromState(ev *rpcEvent) string {
	data := ev.Data
	if len(data) == 0 {
		data = ev.Message
	}
	state := jsonObject(data)
	if state == nil {
		return ""
	}
	if file := jsonText(state, "sessionFile"); file != "" {
		return file
	}
	return jsonText(state, "sessionId")
}

// hasCommand 判断命令表应答里是否列出了指定命令。
func hasCommand(ev *rpcEvent, name string) bool {
	data := ev.Data
	if len(data) == 0 {
		data = ev.Message
	}
	for _, entry := range jsonArray(jsonObject(data)["commands"]) {
		if jsonText(jsonObject(entry), "name") == name {
			return true
		}
	}
	return false
}

// decodeMessage 解析 message 字段；缺失或非法时返回 false。
func decodeMessage(raw json.RawMessage) (messagePayload, bool) {
	var message messagePayload
	if len(raw) == 0 || json.Unmarshal(raw, &message) != nil {
		return messagePayload{}, false
	}
	return message, true
}

// messageRole 取出 message 字段中的角色，用于过滤 assistant 事件。
func messageRole(raw json.RawMessage) string {
	message, ok := decodeMessage(raw)
	if !ok {
		return ""
	}
	return message.Role
}

// messageUsage 取 message 对象里的 usage 载荷（omp 把用量挂在消息上）。
func messageUsage(raw json.RawMessage) json.RawMessage {
	return jsonObject(raw)["usage"]
}

// contentText 拼接文本块内容。
func contentText(blocks []contentBlock) string {
	var builder strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			builder.WriteString(block.Text)
		}
	}
	return builder.String()
}

// jsonObject 把 JSON 载荷解析成对象；缺失或不是对象时返回 nil。
func jsonObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil
	}
	return object
}

// jsonArray 把 JSON 载荷解析成数组；缺失或不是数组时返回 nil。
func jsonArray(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var array []json.RawMessage
	if err := json.Unmarshal(raw, &array); err != nil {
		return nil
	}
	return array
}

// jsonText 取对象里某个键的字符串值；键缺失或不是字符串时返回空串。
func jsonText(object map[string]json.RawMessage, key string) string {
	if object == nil {
		return ""
	}
	var text string
	if err := json.Unmarshal(object[key], &text); err != nil {
		return ""
	}
	return text
}

// jsonFloat 把 JSON 数字解析为 float64；非数字时返回 nil。
func jsonFloat(raw json.RawMessage) *float64 {
	if len(raw) == 0 {
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return nil
	}
	value, err := number.Float64()
	if err != nil {
		return nil
	}
	return &value
}

// jsonBool 取对象里某个键的布尔值；键缺失或不是布尔时返回 (false, false)。
func jsonBool(object map[string]json.RawMessage, key string) (bool, bool) {
	if object == nil {
		return false, false
	}
	var value bool
	if err := json.Unmarshal(object[key], &value); err != nil {
		return false, false
	}
	return value, true
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

// 审批与提问：omp 的门禁与交互请求。

const (
	// gateTitlePrefix 是门禁扩展在 select 标题里标记工具门禁的前缀。
	gateTitlePrefix = "mcode-gate "
	// approvalPrefix 是 omp 自带审批提示的标题前缀："Allow tool: bash\nCommand: rm -rf build"，
	// 第一行是工具名，其余是这次询问的内容。
	approvalPrefix = "Allow tool: "
	// confirmMethod 是 omp 的确认类提问，应答走 confirmed 字段而不是 value。
	confirmMethod = "confirm"
	// questionHeader 是提问类请求在审批卡片上的标题。
	questionHeader = "omp"
)

// gatePayload 是门禁扩展在 select 标题里携带的工具调用信息。
type gatePayload struct {
	Kind       string          `json:"kind"`
	ToolName   string          `json:"toolName"`
	Input      json.RawMessage `json:"input"`
	ToolCallID string          `json:"toolCallId"`
}

// readOnlyGateTools 是门禁扩展直接放行的工具（只看不写、只回报结果的，与 mcode-gate.ts
// 的 readOnly 表一致）。
var readOnlyGateTools = map[string]bool{
	"read": true, "grep": true, "glob": true, "find": true, "ast_grep": true,
	"web_search": true, "todo": true, "wait": true, "think": true, "ask": true, "yield": true,
}

// planBlocksTool 判断计划模式下这次调用是否直接拒绝：计划模式只读，只放行只读工具。
// 只读 shell 命令由门禁扩展直接放行，不会进到这里。
func planBlocksTool(turn harness.Turn, toolName string) bool {
	return turn.InteractionMode == "plan" && !readOnlyGateTools[toolName]
}

// parseGatePayload 从 select 标题解析门禁载荷；非门禁标题返回 ok=false。
func parseGatePayload(title string) (*gatePayload, bool) {
	if !strings.HasPrefix(title, gateTitlePrefix) {
		return nil, false
	}
	var gate gatePayload
	if err := json.Unmarshal([]byte(strings.TrimPrefix(title, gateTitlePrefix)), &gate); err != nil {
		return nil, false
	}
	if gate.Kind == "" {
		gate.Kind = "tool"
	}
	return &gate, true
}

// approvalFromGate 把门禁载荷翻译为审批请求。
func approvalFromGate(gate *gatePayload) harness.ApprovalRequest {
	kind := harness.RequestKindTool
	switch gate.Kind {
	case "command":
		kind = harness.RequestKindCommand
	case "fileChange":
		kind = harness.RequestKindFileChange
	}
	name := gate.ToolName
	if name == "" {
		name = "tool"
	}
	req := harness.ApprovalRequest{
		ToolID:   gate.ToolCallID,
		ToolName: name,
		Input:    gate.Input,
		Kind:     kind,
	}
	// 摘要取询问内容：命令取 command，文件变更取 path。
	switch kind {
	case harness.RequestKindCommand:
		req.Summary = jsonText(jsonObject(gate.Input), "command")
	case harness.RequestKindFileChange:
		req.Summary = jsonText(jsonObject(gate.Input), "path")
	}
	return req
}

// parseApprovalPrompt 拆开 omp 自带的审批提示标题（"Allow tool: " 分支）：
// 第一行是工具名，其余是这次询问的内容。
func parseApprovalPrompt(title string) (toolName, detail string) {
	lines := strings.SplitN(strings.TrimPrefix(title, approvalPrefix), "\n", 2)
	toolName = strings.TrimSpace(lines[0])
	if toolName == "" {
		toolName = "tool"
	}
	if len(lines) > 1 {
		detail = strings.TrimSpace(lines[1])
	}
	return toolName, detail
}

// approvalKind 按工具名把 omp 自带审批提示翻译成请求种类。
func approvalKind(toolName string) harness.RequestKind {
	switch toolName {
	case "bash", "eval":
		return harness.RequestKindCommand
	case "write", "edit", "ast_edit", "notebook":
		return harness.RequestKindFileChange
	default:
		return harness.RequestKindTool
	}
}

// toolGateValue 把审批答复映射为门禁扩展期望的选项文本（Approve / Deny）。
// 会话级允许（approveAlways）同样回
// "Approve"，会话级的记账由审批方负责；DecisionAsk 在这里等同于拒绝——适配器没有向用户
// 二次确认的通道。
func toolGateValue(resp harness.ApprovalResponse) string {
	for _, answers := range resp.Answers {
		for _, answer := range answers {
			if strings.EqualFold(strings.TrimSpace(answer), "approve for session") {
				return "Approve"
			}
		}
	}
	if resp.Decision == harness.DecisionAllow {
		return "Approve"
	}
	return "Deny"
}

// handleExtensionUI 应答 omp 的交互请求：门禁、omp 自带审批、选择题、确认、输入。
// cancel 与 notify、setStatus 等绘制 omp 自己界面的方法没有对应 harness 事件，见 README。
func (h *Harness) handleExtensionUI(ctx context.Context, turn harness.Turn, writer *lineWriter, ev *rpcEvent) {
	id := ev.ID
	if id == "" {
		id = ev.RequestID
	}
	if id == "" || ev.Method == "" {
		return
	}
	params := ev.Params
	if len(params) == 0 {
		params = ev.Message
	}
	var payload struct {
		Title   string   `json:"title"`
		Message string   `json:"message"`
		Options []string `json:"options"`
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &payload)
	}

	switch ev.Method {
	case "select":
		if gate, ok := parseGatePayload(payload.Title); ok {
			h.answerApproval(ctx, turn, writer, id, approvalFromGate(gate))
			return
		}
		if strings.HasPrefix(payload.Title, approvalPrefix) {
			toolName, detail := parseApprovalPrompt(payload.Title)
			h.answerApproval(ctx, turn, writer, id, harness.ApprovalRequest{
				ToolName: toolName,
				Kind:     approvalKind(toolName),
				Summary:  detail,
			})
			return
		}
		h.answerQuestion(ctx, turn, writer, id, "", payload.Title, payload.Options)
	case confirmMethod:
		prompt := strings.TrimSpace(payload.Title)
		if message := strings.TrimSpace(payload.Message); message != "" {
			prompt = prompt + "\n" + message
		}
		h.answerQuestion(ctx, turn, writer, id, confirmMethod, prompt, []string{"Yes", "No"})
	case "input", "editor":
		h.answerQuestion(ctx, turn, writer, id, "", strings.TrimSpace(payload.Title), nil)
	case "cancel":
		// omp 撤回了对话框（超时、中断）：没有需要应答的东西。
	default:
		// notify / setStatus / setWidget / setTitle 等绘制 omp 自己的界面，没有应答通道。
	}
}

// answerApproval 应答一次工具门禁或 omp 自带审批：计划模式只读，能改东西的调用直接拒绝
// 并回写 Deny（门禁扩展据此给出“plan mode only allows reading”的说明），其余交 turn.Approver 裁决。
// 没有审批方时按拒绝处理：扩展在等这条应答，悬着会卡死当轮（对照 pi 的门禁处理）。
func (h *Harness) answerApproval(ctx context.Context, turn harness.Turn, writer *lineWriter, id string, req harness.ApprovalRequest) {
	resp := harness.ApprovalResponse{Decision: harness.DecisionDeny}
	if !planBlocksTool(turn, req.ToolName) {
		if interactor, ok := turn.Approver.(harness.Interactor); ok && interactor != nil {
			if answer, err := interactor.Interact(ctx, req); err == nil {
				resp = answer
			}
		} else if turn.Approver != nil {
			if decision, err := turn.Approver.Approve(ctx, req); err == nil {
				resp.Decision = decision
			}
		}
	}
	line, err := requestLine(valueResponse(id, toolGateValue(resp)))
	if err == nil {
		err = writer.write(line)
	}
	harness.AckDelivery(turn.Approver, req, err)
}

// questionRequest 把 omp 的交互提问包装为 harness 审批请求（Kind 为 question）。
func questionRequest(id, prompt string, options []string) harness.ApprovalRequest {
	question := harness.Question{
		ID:          id,
		Header:      questionHeader,
		Prompt:      prompt,
		AllowsOther: len(options) == 0,
	}
	for _, option := range options {
		question.Choices = append(question.Choices, harness.QuestionChoice{Label: option})
	}
	return harness.ApprovalRequest{
		Kind:      harness.RequestKindQuestion,
		ToolName:  "question",
		Summary:   prompt,
		Questions: []harness.Question{question},
	}
}

// answerQuestion 应答 omp 的选择/确认/输入请求。
// 没有可用的交互方时按取消（cancelled）应答，等价于用户放弃这次提问。
func (h *Harness) answerQuestion(ctx context.Context, turn harness.Turn, writer *lineWriter, id, method, prompt string, options []string) {
	req := questionRequest(id, prompt, options)
	var resp harness.ApprovalResponse
	if interactor, ok := turn.Approver.(harness.Interactor); ok && interactor != nil {
		if answer, err := interactor.Interact(ctx, req); err == nil {
			resp = answer
		}
	} else if turn.Approver != nil {
		if decision, err := turn.Approver.Approve(ctx, req); err == nil && decision == harness.DecisionAllow {
			resp = harness.ApprovalResponse{Decision: decision, Answers: map[string][]string{id: {"Yes"}}}
		}
	}

	answers := answersFor(resp, id)
	var line []byte
	var err error
	switch {
	case len(answers) == 0:
		line, err = requestLine(cancelledResponse(id))
	case method == confirmMethod:
		line, err = requestLine(confirmedResponse(id, strings.EqualFold(answers[0], "yes")))
	default:
		line, err = requestLine(valueResponse(id, answers[0]))
	}
	if err == nil {
		err = writer.write(line)
	}
	harness.AckDelivery(turn.Approver, req, err)
}

// answersFor 取某道题的答案；答案缺失时按题目 ID 排序展平全部答案兜底。
func answersFor(resp harness.ApprovalResponse, id string) []string {
	if answers := resp.Answers[id]; len(answers) > 0 {
		return answers
	}
	keys := make([]string, 0, len(resp.Answers))
	for key := range resp.Answers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var flat []string
	for _, key := range keys {
		flat = append(flat, resp.Answers[key]...)
	}
	return flat
}
