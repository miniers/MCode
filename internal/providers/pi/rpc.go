package pi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"mcode/internal/harness"
)

// pi `--mode rpc` 的请求类型常量，与 stdin 上的 JSON 行协议一致。
const (
	rpcTypePrompt          = "prompt"
	rpcTypeSteer           = "steer"
	rpcTypeGetState        = "get_state"
	rpcTypeAbort           = "abort"
	rpcTypeSetModel        = "set_model"
	rpcTypeSetThinking     = "set_thinking_level"
	rpcTypeExtensionUIResp = "extension_ui_response"
)

// rpcRequest 是写往 pi stdin 的一行 JSON 请求。
type rpcRequest struct {
	Type    string         `json:"type"`
	ID      string         `json:"id,omitempty"`
	Message string         `json:"message,omitempty"`
	Images  []imagePayload `json:"images,omitempty"`
	// Provider / ModelID 是 set_model 的参数，Level 是 set_thinking_level 的参数。
	Provider string `json:"provider,omitempty"`
	ModelID  string `json:"modelId,omitempty"`
	Level    string `json:"level,omitempty"`
}

// imagePayload 是 prompt 请求中携带的一张图片。
type imagePayload struct {
	Type     string `json:"type"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

// extensionUIResponse 是写回 pi 扩展的交互应答。
// value 用于 select/input/editor，confirmed 用于 confirm，cancelled 表示用户放弃。
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

// getStateRequest 构造取会话状态的请求，应答里带 pi 的会话文件路径。
func getStateRequest(id string) rpcRequest {
	return rpcRequest{Type: rpcTypeGetState, ID: id}
}

// promptRequest 构造一轮提问请求。
// message 以 / 开头时在前面补一个空格，避免被 pi RPC 当作扩展命令、技能或模板执行。
func promptRequest(id, message string, images []imagePayload) rpcRequest {
	if strings.HasPrefix(message, "/") {
		message = " " + message
	}
	req := rpcRequest{Type: rpcTypePrompt, ID: id, Message: message}
	if len(images) > 0 {
		req.Images = images
	}
	return req
}

// steerRequest 构造轮内追加指引的请求。轮内的 prompt 不带 streamingBehavior 会被 pi 拒绝，
// 且 translate 会把被拒的 prompt 应答当成当轮失败，所以必须用专门的 steer 命令。
// message 以 / 开头时在前面补一个空格，避免被 pi RPC 当作扩展命令、技能或模板执行。
func steerRequest(id, message string) rpcRequest {
	if strings.HasPrefix(message, "/") {
		message = " " + message
	}
	return rpcRequest{Type: rpcTypeSteer, ID: id, Message: message}
}

// abortRequest 构造中断当轮的请求。带 id 时 pi 在会话回到空闲之后才应答，
// 复用进程的取消路径据此判断进程还能不能留着。
func abortRequest(id string) rpcRequest {
	return rpcRequest{Type: rpcTypeAbort, ID: id}
}

// setModelRequest 构造切换模型的请求。
func setModelRequest(id, provider, modelID string) rpcRequest {
	return rpcRequest{Type: rpcTypeSetModel, ID: id, Provider: provider, ModelID: modelID}
}

// setThinkingRequest 构造切换思考档位的请求。
func setThinkingRequest(id, level string) rpcRequest {
	return rpcRequest{Type: rpcTypeSetThinking, ID: id, Level: level}
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

// rpcEvent 是 pi stdout 上的一行 JSON 事件。
// 事件类型多且字段形态不一，这里按需保留原始 JSON，由 translate 分派。
type rpcEvent struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	RequestID string          `json:"requestId"`
	Command   string          `json:"command"`
	Success   *bool           `json:"success"`
	Error     string          `json:"error"`
	Message   json.RawMessage `json:"message"`
	Data      json.RawMessage `json:"data"`
	Usage     json.RawMessage `json:"usage"`

	// message_update 的增量载荷（新版 pi 放在 assistantMessageEvent，老版放在 event）
	AssistantMessageEvent *deltaPayload `json:"assistantMessageEvent"`
	// event 有两种形态：message_update 的增量载荷是对象，extension_error 的钩子事件名是字符串，
	// 所以先按原始 JSON 保留，由使用方各自解码。
	Event json.RawMessage `json:"event"`

	// 工具执行
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Args       json.RawMessage `json:"args"`
	Input      json.RawMessage `json:"input"`
	Result     *toolResult     `json:"result"`
	IsError    bool            `json:"isError"`

	// agent_end：pi 说明这次结束是否还会自行重试
	WillRetry bool `json:"willRetry"`

	// compaction_end：压缩结果（含压缩前 token 数）、是否被中止与失败原因
	Aborted      bool   `json:"aborted"`
	ErrorMessage string `json:"errorMessage"`

	// auto_retry_start / auto_retry_end：pi 自行重试失败请求的进度与结果
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"maxAttempts"`
	FinalError  string `json:"finalError"`

	// extension_ui_request：扩展交互请求
	// 部分扩展把 title/options 放在根对象而不是 params 里，两者都要读。
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Title   string          `json:"title"`
	Options []string        `json:"options"`
	// setStatus 通知的线上格式：statusKey 是状态键，statusText 是内容。
	StatusKey  string `json:"statusKey"`
	StatusText string `json:"statusText"`

	// extension_error：抛出异常的扩展路径，错误摘要走 Error，事件名走 Event
	ExtensionPath string `json:"extensionPath"`
}

// deltaPayload 是 message_update 中的增量载荷（assistantMessageEvent 或 event）。
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

// toolResult 是 tool_execution_end 与 compaction_end 共用的 result 载荷：
// 工具结果用 content/isError，压缩结果只取压缩前的 token 数。
type toolResult struct {
	Content      []contentBlock `json:"content"`
	IsError      bool           `json:"isError"`
	TokensBefore int64          `json:"tokensBefore"`
}

// parseEvent 解析 pi stdout 上的一行 JSON。空行与缺少 type 的行返回 nil, nil；
// 非法 JSON 返回错误，调用方可以忽略（与 claude 包同样容忍未知行）。
func parseEvent(line []byte) (*rpcEvent, error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var ev rpcEvent
	if err := json.Unmarshal(trimmed, &ev); err != nil {
		return nil, fmt.Errorf("pi: parse event: %w", err)
	}
	if ev.Type == "" {
		return nil, nil
	}
	return &ev, nil
}

// streamState 保存一轮对话的事件翻译状态。
type streamState struct {
	streamedText bool
	// turnErr 是当轮的失败原因。pi 遇到可重试的模型错误会先报错再自行重试，所以这里只记录，
	// 错误事件统一留到 finish 时补发；重试成功时 turnErr 已被 message_start 清空。
	turnErr string
	// errSent 表示当轮已经发过错误事件，保证同一轮最多报一次。
	errSent bool
	sawDone bool
	// usage 是本轮所有模型请求的累计用量。remote 对同一轮用量是覆盖写，而 pi 每次模型请求
	// 都在 message_end 上报一次「这一次请求」的用量，所以必须在这里逐次累加。
	usage harness.Usage
	// contextWindow 是当前模型的上下文窗口上限，零值表示未知、不上报；由 run.go 填写。
	contextWindow int64
	// strictSettle 为真时只有 agent_settled 才算当轮结束：进程会留给下一轮复用，
	// agent_end 之后 pi 可能还在重试、压缩或处理排队消息，这时发新的 prompt 会被拒绝。
	strictSettle bool
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

// translate 把一条 pi 事件翻译为 harness 事件，返回当轮是否已经结束。
// 扩展交互请求（extension_ui_request）不在翻译范围内，由 Harness.handleExtensionUI 应答。
func (s *streamState) translate(ev *rpcEvent, emit func(harness.Event)) bool {
	switch ev.Type {
	case "response":
		// 只关心当轮 prompt 的失败应答；get_state 等应答由 Run 自行消费。
		if ev.Command == rpcTypePrompt && ev.Success != nil && !*ev.Success {
			s.fail(firstNonEmpty(ev.Error, "pi reported an error."), emit)
			return s.finish(emit)
		}
		// 扩展命令或输入处理器直接消化了这条 prompt：没有 run 启动，也就不会有 agent_settled。
		if ev.Command == rpcTypePrompt && dispositionOf(ev.Data) == "handled" {
			return s.finish(emit)
		}
		return false
	case "message_start":
		if messageRole(ev.Message) == "assistant" {
			// 上一条回复报错后 pi 会自行重试，重试开始即清掉错误标记。
			s.turnErr = ""
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
		// 文件写改类工具额外上报路径，供消费方统计改动文件（对照 claude 的 Edit/Write）。
		if ev.ToolName == "write" || ev.ToolName == "edit" {
			if path := inputString(input, "path"); path != "" {
				_ = harness.Emit(emit, harness.Event{Type: harness.EventFileChange, Path: path})
			}
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
	case "agent_end":
		// willRetry 为真的结束只是 pi 的重试前奏，agent_settled 才是当轮真正的终点。
		if !ev.WillRetry && !s.strictSettle {
			return s.finish(emit)
		}
		return false
	case "auto_retry_start":
		// 可重试的模型错误会被 pi 自行重试：只提示进度，错误留到 finish 再补发。
		_ = harness.Emit(emit, harness.Event{Type: harness.EventReasoning, Text: retryStartText(ev)})
		return false
	case "auto_retry_end":
		// 重试用尽：记下最终失败原因，由 finish 统一报错。
		if ev.Success != nil && !*ev.Success && ev.FinalError != "" {
			s.turnErr = ev.FinalError
		}
		return false
	case "compaction_start":
		_ = harness.Emit(emit, harness.Event{Type: harness.EventReasoning, Text: "[pi 正在压缩上下文…]\n"})
		return false
	case "compaction_end":
		_ = harness.Emit(emit, harness.Event{Type: harness.EventReasoning, Text: compactionEndText(ev)})
		// 压缩成功时额外发一个上下文压缩事件，时间线据此落一条独立边界行（不进回合折叠）；
		// 中止或失败时上下文没变，不发。
		if compactionSucceeded(ev) {
			_ = harness.Emit(emit, harness.Event{
				Type:                harness.EventContextCompacted,
				CompactTokensBefore: ev.Result.TokensBefore,
			})
		}
		return false
	case "agent_settled":
		return s.finish(emit)
	case "extension_error":
		// 扩展钩子抛异常只是通知：pi 自己会继续这一轮，所以这里只提示，
		// 不记 turnErr、不报错，避免整轮被判为失败。
		_ = harness.Emit(emit, harness.Event{Type: harness.EventReasoning, Text: extensionErrorText(ev)})
		return false
	default:
		// tool_execution_update（部分输出）等没有对应的 harness 事件，忽略。
		return false
	}
}

// dispositionOf 取出 prompt 应答 data.disposition（started / queued / handled）。
func dispositionOf(raw json.RawMessage) string {
	var data struct {
		Disposition string `json:"disposition"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &data) != nil {
		return ""
	}
	return data.Disposition
}

// retryStartText 把 auto_retry_start 翻译成一行中文提示，以换行结尾以便单独成段。
// 错误摘要截断到 200 个字符以内，避免一屏提示被长报文占满。
func retryStartText(ev *rpcEvent) string {
	message := truncateText(firstNonEmpty(ev.ErrorMessage, "未知错误"), 200)
	return fmt.Sprintf("[pi 请求失败，正在重试 %d/%d：%s]\n", ev.Attempt, ev.MaxAttempts, message)
}

// truncateText 把文本截断到 limit 个字符（按 rune 计），被截断时末尾加省略号。
func truncateText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}

// compactionSucceeded 判断 compaction_end 是否真的把上下文压掉了：被中止、带错误信息或
// 没有 result 都算失败（与 compactionEndText 的判定一致）。
func compactionSucceeded(ev *rpcEvent) bool {
	return ev != nil && !ev.Aborted && ev.ErrorMessage == "" && ev.Result != nil
}

// compactionEndText 把 compaction_end 翻译成一行中文提示，以换行结尾以便单独成段。
func compactionEndText(ev *rpcEvent) string {
	if ev.Aborted || ev.ErrorMessage != "" || ev.Result == nil {
		if ev.ErrorMessage != "" {
			return fmt.Sprintf("[pi 压缩上下文失败：%s]\n", ev.ErrorMessage)
		}
		return "[pi 压缩上下文失败]\n"
	}
	if tokens := ev.Result.TokensBefore; tokens > 0 {
		return fmt.Sprintf("[pi 已压缩上下文，压缩前约 %d tokens]\n", tokens)
	}
	return "[pi 已压缩上下文]\n"
}

// eventPayload 解码 event 字段里的增量载荷；形态不符（例如 extension_error 里的事件名）时返回 nil。
func (ev *rpcEvent) eventPayload() *deltaPayload {
	if len(ev.Event) == 0 {
		return nil
	}
	var payload deltaPayload
	if json.Unmarshal(ev.Event, &payload) != nil {
		return nil
	}
	return &payload
}

// extensionEventName 解码 extension_error 的 event 字段（钩子事件名）。
func (ev *rpcEvent) extensionEventName() string {
	var name string
	if len(ev.Event) == 0 {
		return ""
	}
	_ = json.Unmarshal(ev.Event, &name)
	return name
}

// extensionErrorText 把 extension_error 翻译成一行中文提示，以换行结尾以便单独成段。
// 扩展只暴露文件名，不把用户的完整目录结构摊到对话里；错误摘要截断到 200 个字符以内。
// 缺失的字段直接省略，不留下空括号。
func extensionErrorText(ev *rpcEvent) string {
	var details []string
	if ev.ExtensionPath != "" {
		if name := filepath.Base(ev.ExtensionPath); name != "" && name != "." && name != "/" {
			details = append(details, name)
		}
	}
	if name := ev.extensionEventName(); name != "" {
		details = append(details, name)
	}
	prefix := "[pi 扩展出错"
	if len(details) > 0 {
		prefix += "（" + strings.Join(details, "，") + "）"
	}
	if message := truncateText(strings.TrimSpace(ev.Error), 200); message != "" {
		return fmt.Sprintf("%s：%s]\n", prefix, message)
	}
	return prefix + "]\n"
}

// handleUpdate 处理 message_update 的文本/思考增量与用量。
func (s *streamState) handleUpdate(ev *rpcEvent, emit func(harness.Event)) {
	delta := ev.AssistantMessageEvent
	if delta == nil {
		delta = ev.eventPayload()
	}
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
	usage := ev.Usage
	if len(usage) == 0 && delta != nil {
		usage = delta.Usage
	}
	if u, ok := parseUsage(usage); ok {
		s.emitPartialUsage(u, parseUsageCost(usage), emit)
	}
}

// handleMessageEnd 处理 assistant 消息结束：结算用量、补发未流式的正文、记录错误。
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
	if message.StopReason == "error" {
		// 这条消息可能马上被 pi 自行重试：只记下原因，错误事件留到 finish 再补发。
		s.turnErr = firstNonEmpty(message.ErrorMessage, "pi reported an error.")
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
	total.ContextWindow = s.contextWindow
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
	total.ContextWindow = s.contextWindow
	total.Requests = s.requestSamples()
	_ = harness.Emit(emit, harness.Event{Type: harness.EventUsage, Usage: &total})
}

// fail 记录并立即发出一次终止性错误事件。
// 可能被 pi 重试掉的错误不要走这里，改为写 turnErr 由 finish 补发。
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
	// 重试耗尽或被终止时错误还没报过，这里补发一次；重试成功时 turnErr 已清空，不会误报。
	s.emitError(s.turnErr, emit)
	stopReason := "completed"
	if s.turnErr != "" {
		stopReason = "error"
	}
	_ = harness.Emit(emit, harness.Event{Type: harness.EventDone, StopReason: stopReason})
	return true
}

// parseUsage 解析 pi 的用量载荷，兼容多种键名。
// InputTokens 含缓存读与缓存写（与 codex 口径一致），因此 Input+Output 就是该轮真正
// 进入模型的上下文；命中量另记在 CachedTokens 里单独保留。
// 只带 totalTokens 的载荷把总量记到输入侧，避免丢失用量。
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

// parseUsageCost 取用量载荷里的费用（美元）：pi 的 usage.cost.total。
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

// sessionIDFromState 从 get_state 应答中取出 pi 的会话文件路径，作为后续轮次的 --session 恢复 id。
func sessionIDFromState(ev *rpcEvent) string {
	data := ev.Data
	if len(data) == 0 {
		data = ev.Message
	}
	var state struct {
		SessionFile string `json:"sessionFile"`
		SessionID   string `json:"sessionId"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return ""
	}
	if state.SessionFile != "" {
		return state.SessionFile
	}
	return state.SessionID
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

// contentText 拼接文本块内容。
func contentText(blocks []contentBlock) string {
	var builder bytes.Buffer
	for _, block := range blocks {
		if block.Type == "text" {
			builder.WriteString(block.Text)
		}
	}
	return builder.String()
}

// inputString 从工具入参里取字符串字段。
func inputString(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ""
	}
	text, _ := fields[key].(string)
	return text
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
