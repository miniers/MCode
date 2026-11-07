package claude

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"mcode/internal/harness"
)

var (
	backgroundTaskPattern   = regexp.MustCompile(`moved to the background as task ([A-Za-z0-9]+)(\s|\.|$)`)
	taskNotificationPattern = regexp.MustCompile(`(?s)<task-notification>.*?</task-notification>`)
	taskIDPattern           = regexp.MustCompile(`<task-id>\s*([^<\s]+)\s*</task-id>`)
	toolUseIDPattern        = regexp.MustCompile(`<tool-use-id>\s*([^<\s]+)\s*</tool-use-id>`)
	taskStatusPattern       = regexp.MustCompile(`<status>\s*([^<\s]+)\s*</status>`)
)

// parser 负责解析 Claude Code CLI 的 stream-json 行并维护流式状态。
type parser struct {
	textStreamed      map[int]*strings.Builder
	reasoningStreamed map[int]*strings.Builder
	backgroundTasks   map[string]string
	curTextIndex      int // 当前正文块的 stream index，-1 表示尚无
	curReasoningIndex int // 当前思考块的 stream index，-1 表示尚无
	// textPending 记录本次请求里已经以增量流出、还没被 assistant 完整块确认的正文 index。
	// 下一次请求开始时仍未确认，说明这段正文随流中断丢了，CLI 不会把它交还给模型，需要撤回。
	textPending map[int]bool
	sawResult   bool
	// errCode 是最近一条合成 assistant 消息上的结构化失败码（authentication_failed、rate_limit 等）。
	// 接口失败时 CLI 会先插一条 model 为 <synthetic> 的 assistant 消息，随后的 result 里 subtype 仍
	// 可能是 success，所以失败类别靠这个字段判断；由 result 消费后清空。
	errCode string
	// ctxSnapshot 是本轮最后一次顶层（非子代理）模型请求带入上下文的用量快照。
	// result.usage 是整轮多次请求的累计，不能当作上下文大小，所以单独记最近一次请求。
	ctxSnapshot contextSnapshot
	// clock 为当前模型请求计时；requests 收集本轮每次顶层请求的输出速度样本。
	clock    harness.RequestClock
	requests []harness.RequestSpeed
	// now 返回当前时间，测试可注入；为 nil 时用 time.Now。
	now func() time.Time
	// reqStart 是当前请求的计时起点：首个内容帧，或没有 partial 流时该请求首条助手消息的出现时刻。
	reqStart time.Time
	// reqMsgID 是当前请求的 assistant message id，同一请求的多条助手消息只结算一次。
	reqMsgID string
	// reqLocked 表示当前请求的 tokens 已定稿（来自 message_delta），后续助手消息只做去重。
	reqLocked bool
	// reqSampleIdx 是当前请求样本在 requests 里的下标，-1 表示还没有样本。
	reqSampleIdx int
}

// contextSnapshot 是一次模型请求实际带入上下文的四项用量：未命中缓存的输入、命中缓存的输入、
// 新写入缓存的输入，以及这次请求生成的输出；四项之和就是该次请求之后的上下文占用。
// 压缩（compact_boundary）之后 CLI 只给压缩后的总大小、没有分类明细，记在 input 上。
type contextSnapshot struct {
	input         int64
	cacheRead     int64
	cacheCreation int64
	output        int64
}

// total 返回这次请求带入上下文的总 token 数。
func (s contextSnapshot) total() int64 {
	return s.input + s.cacheRead + s.cacheCreation + s.output
}

// usagePayload 是 CLI 消息里可能出现的 usage 片段。字段可能缺失（message_delta 常常只带
// output_tokens），因此用指针区分「没有这个字段」和「值为 0」。
type usagePayload struct {
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
}

func int64OrZero(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// snapshot 用一份完整 usage 重建快照，缺失的字段按 0 计。
func (u *usagePayload) snapshot() contextSnapshot {
	if u == nil {
		return contextSnapshot{}
	}
	return contextSnapshot{
		input:         int64OrZero(u.InputTokens),
		cacheRead:     int64OrZero(u.CacheReadInputTokens),
		cacheCreation: int64OrZero(u.CacheCreationInputTokens),
		output:        int64OrZero(u.OutputTokens),
	}
}

// updateOutput 用 message_delta 的 usage 刷新输出数；该字段缺失时沿用上一份快照。
func (u *usagePayload) updateOutput(s *contextSnapshot) {
	if u == nil || u.OutputTokens == nil {
		return
	}
	s.output = *u.OutputTokens
}

func newParser() *parser {
	return &parser{
		textStreamed:      make(map[int]*strings.Builder),
		reasoningStreamed: make(map[int]*strings.Builder),
		curTextIndex:      -1,
		curReasoningIndex: -1,
		reqSampleIdx:      -1,
	}
}

// nowTime 返回当前时间，测试可替换 p.now 注入时钟。
func (p *parser) nowTime() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// startRequest 记录当前请求的计时起点，重复调用只认第一次。
func (p *parser) startRequest(now time.Time) {
	if p.reqLocked {
		// 上一条请求已经用 message_delta 定稿：新的内容帧属于下一条请求
		// （例如缺少 message_start 的流），不能沿用它的起点。
		p.beginNewRequest("")
	}
	if !p.reqStart.IsZero() {
		return
	}
	p.reqStart = now
	p.clock.Begin(now)
}

// beginNewRequest 在一次新请求开始时（message_start）重置计时与去重状态。
func (p *parser) beginNewRequest(id string) {
	p.reqStart = time.Time{}
	p.clock.Reset()
	p.reqMsgID = id
	p.reqLocked = false
	p.reqSampleIdx = -1
}

// takeSample 用当前请求的输出 tokens 尝试结算一次，成功后把样本记入本轮。
// 请求太短（时长不足时钟下限）或 tokens 为 0 时不算误差：把起点还给时钟，等同一请求的
// 下一条消息再试。
func (p *parser) takeSample(outputTokens int64) bool {
	if p.reqStart.IsZero() || outputTokens <= 0 {
		return false
	}
	if speed, ok := p.clock.Finish(p.nowTime(), outputTokens); ok {
		p.requests = append(p.requests, speed)
		p.reqSampleIdx = len(p.requests) - 1
		return true
	}
	p.clock.Begin(p.reqStart)
	return false
}

// settleRequest 在当前请求的输出 tokens 确定时结算一次。
// locked 表示 tokens 来自 message_delta usage，是该请求的最终值；未定稿时同一请求（按 message id
// 去重）的后续助手消息只能用更大的 tokens 补齐，因为 CLI 助手消息的 usage 会随生成逐步增长。
func (p *parser) settleRequest(outputTokens int64, locked bool) {
	if p.reqLocked {
		return
	}
	if locked {
		p.reqLocked = true
		if p.reqSampleIdx >= 0 {
			// message_delta 的 tokens 是最终值：覆盖先前用助手消息结算的样本，时长以此刻为准。
			p.requests[p.reqSampleIdx].OutputTokens = outputTokens
			if !p.reqStart.IsZero() {
				if ms := p.nowTime().Sub(p.reqStart).Milliseconds(); ms > p.requests[p.reqSampleIdx].DurationMs {
					p.requests[p.reqSampleIdx].DurationMs = ms
				}
			}
			return
		}
		p.takeSample(outputTokens)
		return
	}
	if p.reqSampleIdx >= 0 {
		if outputTokens > p.requests[p.reqSampleIdx].OutputTokens {
			p.requests[p.reqSampleIdx].OutputTokens = outputTokens
		}
		return
	}
	p.takeSample(outputTokens)
}

// settleFromAssistant 用顶层助手消息的 usage 结算当前请求：没有 partial 流时以该请求首条助手消息
// 的出现时刻作为起点；同一 message id 的多条消息只结算一次。
func (p *parser) settleFromAssistant(id string, usage *usagePayload) {
	if usage == nil || p.reqLocked {
		return
	}
	switch {
	case id == "":
		// 没有 message id 时无法去重，只结算一次。
	case p.reqMsgID == "":
		p.reqMsgID = id
	case id != p.reqMsgID:
		// 没有 message_start 的流：换到新的助手消息说明上一条请求已经结束。
		p.beginNewRequest(id)
	}
	p.startRequest(p.nowTime())
	p.settleRequest(int64OrZero(usage.OutputTokens), false)
}

func (p *parser) resetStreamed() {
	p.textStreamed = make(map[int]*strings.Builder)
	p.textPending = nil
	p.reasoningStreamed = make(map[int]*strings.Builder)
	p.curTextIndex = -1
	p.curReasoningIndex = -1
}

func (p *parser) appendTextStreamed(index int, text string) {
	if p.textStreamed == nil {
		p.textStreamed = make(map[int]*strings.Builder)
	}
	b, ok := p.textStreamed[index]
	if !ok {
		b = &strings.Builder{}
		p.textStreamed[index] = b
	}
	b.WriteString(text)
}

func (p *parser) appendReasoningStreamed(index int, text string) {
	if p.reasoningStreamed == nil {
		p.reasoningStreamed = make(map[int]*strings.Builder)
	}
	b, ok := p.reasoningStreamed[index]
	if !ok {
		b = &strings.Builder{}
		p.reasoningStreamed[index] = b
	}
	b.WriteString(text)
}

func (p *parser) textStreamedFor(index int) string {
	if p.textStreamed == nil {
		return ""
	}
	if b, ok := p.textStreamed[index]; ok {
		return b.String()
	}
	return ""
}

func (p *parser) reasoningStreamedFor(index int) string {
	if p.reasoningStreamed == nil {
		return ""
	}
	if b, ok := p.reasoningStreamed[index]; ok {
		return b.String()
	}
	return ""
}

// textIndexFor 为 assistant 中位于 pos 的完整正文块选择流式 index。assistant 事件通常每次只带一个块，
// 其位置恒为 0，不能直接当作 stream index；因此优先用当前流式块，但要求它已流出的内容是完整块的前缀。
func (p *parser) textIndexFor(pos int, full string) int {
	if p.curTextIndex >= 0 && strings.HasPrefix(full, p.textStreamedFor(p.curTextIndex)) {
		return p.curTextIndex
	}
	return pos
}

// reasoningIndexFor 与 textIndexFor 相同，作用于思考块。
func (p *parser) reasoningIndexFor(pos int, full string) int {
	if p.curReasoningIndex >= 0 && strings.HasPrefix(full, p.reasoningStreamedFor(p.curReasoningIndex)) {
		return p.curReasoningIndex
	}
	return pos
}

func (p *parser) setTextStreamed(index int, s string) {
	if p.textStreamed == nil {
		p.textStreamed = make(map[int]*strings.Builder)
	}
	b, ok := p.textStreamed[index]
	if !ok {
		b = &strings.Builder{}
		p.textStreamed[index] = b
	}
	b.Reset()
	b.WriteString(s)
}

func (p *parser) setReasoningStreamed(index int, s string) {
	if p.reasoningStreamed == nil {
		p.reasoningStreamed = make(map[int]*strings.Builder)
	}
	b, ok := p.reasoningStreamed[index]
	if !ok {
		b = &strings.Builder{}
		p.reasoningStreamed[index] = b
	}
	b.Reset()
	b.WriteString(s)
}

// retractPendingText 在新一次请求开始前撤回上一次请求流出但没被确认的正文：上游流中断后 CLI
// 只保留已确认的块并让模型续写，模型看不到这段残文会从头重写，不撤回就会和新回复拼成两遍。
func (p *parser) retractPendingText(emit func(harness.Event)) {
	if len(p.textPending) == 0 {
		return
	}
	indexes := make([]int, 0, len(p.textPending))
	for idx := range p.textPending {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)
	var b strings.Builder
	for _, idx := range indexes {
		b.WriteString(p.textStreamedFor(idx))
	}
	if b.Len() == 0 {
		return
	}
	log.Printf("claude: retract %d unconfirmed streamed text chars after interrupted request", b.Len())
	_ = harness.Emit(emit, harness.Event{
		Type: harness.EventTextRetract,
		Text: b.String(),
	})
}

// handleLine 解析单行 stream-json 数据并按规则派发事件。
func (p *parser) handleLine(line []byte, emit func(harness.Event)) error {
	var base struct {
		Type            string  `json:"type"`
		ParentToolUseID *string `json:"parent_tool_use_id"`
	}
	if err := json.Unmarshal(line, &base); err != nil {
		// 无法解析的 JSON 行忽略，不报错
		return nil
	}

	parentID := ""
	if base.ParentToolUseID != nil {
		parentID = *base.ParentToolUseID
	}

	switch base.Type {
	case "system":
		p.handleSystem(line, parentID != "", emit)
	case "stream_event":
		if parentID != "" {
			return nil
		}
		p.handleStreamEvent(line, emit)
	case "assistant":
		p.handleAssistant(line, parentID != "", emit)
	case "user":
		p.handleUser(line, parentID != "", emit)
	case "result":
		p.handleResult(line, emit)
	default:
		// 未知 type 忽略，不报错
	}

	return nil
}

func (p *parser) handleSystem(line []byte, isSubagent bool, emit func(harness.Event)) {
	var sys struct {
		Subtype         string `json:"subtype"`
		SessionID       string `json:"session_id"`
		TaskID          string `json:"task_id"`
		ToolUseID       string `json:"tool_use_id"`
		Status          string `json:"status"`
		Description     string `json:"description"`
		IsBackgrounded  *bool  `json:"is_backgrounded"`
		CompactMetadata *struct {
			PreTokens  int64 `json:"pre_tokens"`
			PostTokens int64 `json:"post_tokens"`
		} `json:"compact_metadata"`
	}
	if err := json.Unmarshal(line, &sys); err != nil {
		return
	}
	if sys.Subtype == "init" && sys.SessionID != "" {
		_ = harness.Emit(emit, harness.Event{
			Type:      harness.EventSession,
			SessionID: sys.SessionID,
		})
	}
	// 压缩边界：compact_metadata.post_tokens 是压缩后已用的上下文大小，作为「下一次请求之前」的快照；
	// 之后到来的 message_start 会把它覆盖成新请求的实际用量。子代理的压缩不影响本轮上下文。
	// 主会话的压缩边界同时发一个上下文压缩事件，时间线据此落一条独立边界行（不进回合折叠）。
	if sys.Subtype == "compact_boundary" && !isSubagent {
		var before, after int64
		if sys.CompactMetadata != nil {
			before = sys.CompactMetadata.PreTokens
			after = sys.CompactMetadata.PostTokens
		}
		if after > 0 {
			p.ctxSnapshot = contextSnapshot{input: after}
		}
		_ = harness.Emit(emit, harness.Event{
			Type:                harness.EventContextCompacted,
			CompactTokensBefore: before,
			CompactTokensAfter:  after,
		})
	}

	// provider 托管的后台任务：Bash 的 run_in_background 先返回 tool_result，命令仍在后台跑。
	// 开始帧把发起它的工具消息标成 background，结束帧（常出现在后续 attach 续跑轮次）按
	// tool_use_id 回写最终状态。is_backgrounded 显式为 false 表示这条任务没有在后台运行；
	// 子代理的任务不影响主会话时间线。
	if !isSubagent {
		switch sys.Subtype {
		case "task_started":
			if sys.ToolUseID != "" && (sys.IsBackgrounded == nil || *sys.IsBackgrounded) {
				if sys.TaskID != "" {
					if p.backgroundTasks == nil {
						p.backgroundTasks = make(map[string]string)
					}
					p.backgroundTasks[sys.TaskID] = sys.ToolUseID
				}
				_ = harness.Emit(emit, harness.Event{
					Type:   harness.EventBackgroundTaskStart,
					ToolID: sys.ToolUseID,
					TaskID: sys.TaskID,
					Text:   sys.Description,
				})
			}
		case "task_notification":
			toolID := sys.ToolUseID
			if toolID == "" {
				toolID = p.backgroundTasks[sys.TaskID]
			}
			if toolID != "" || sys.TaskID != "" {
				_ = harness.Emit(emit, harness.Event{
					Type:       harness.EventBackgroundTaskEnd,
					ToolID:     toolID,
					TaskID:     sys.TaskID,
					TaskStatus: sys.Status,
				})
				delete(p.backgroundTasks, sys.TaskID)
			}
		}
	}
}

func (p *parser) handleStreamEvent(line []byte, emit func(harness.Event)) {
	var se struct {
		ParentToolUseID *string `json:"parent_tool_use_id"`
		Event           struct {
			Type            string  `json:"type"`
			Index           int     `json:"index"`
			ParentToolUseID *string `json:"parent_tool_use_id"`
			ContentBlock    struct {
				Type string `json:"type"`
			} `json:"content_block"`
			Delta struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
			} `json:"delta"`
			Message *struct {
				ID    string        `json:"id"`
				Usage *usagePayload `json:"usage"`
			} `json:"message"`
			Usage *usagePayload `json:"usage"`
		} `json:"event"`
	}
	if err := json.Unmarshal(line, &se); err != nil {
		return
	}
	if (se.ParentToolUseID != nil && *se.ParentToolUseID != "") ||
		(se.Event.ParentToolUseID != nil && *se.Event.ParentToolUseID != "") {
		return
	}

	switch se.Event.Type {
	case "message_start":
		p.retractPendingText(emit)
		p.resetStreamed()
		// 一次新请求开始：重置计时与去重状态，并用它的 usage 建立快照（input + 缓存读写 + 已知输出）。
		requestID := ""
		if se.Event.Message != nil {
			requestID = se.Event.Message.ID
			if se.Event.Message.Usage != nil {
				p.ctxSnapshot = se.Event.Message.Usage.snapshot()
			}
		}
		p.beginNewRequest(requestID)
	case "message_delta":
		// 同一请求的输出增量；usage 里没有 output_tokens 时沿用上一份快照。
		if se.Event.Usage != nil {
			se.Event.Usage.updateOutput(&p.ctxSnapshot)
			// message_delta 的 output_tokens 是该请求的最终值，优先用它结算。
			if se.Event.Usage.OutputTokens != nil {
				p.settleRequest(*se.Event.Usage.OutputTokens, true)
			}
		}
	case "content_block_start":
		switch se.Event.ContentBlock.Type {
		case "text":
			p.curTextIndex = se.Event.Index
		case "thinking":
			p.curReasoningIndex = se.Event.Index
		}
		if p.textStreamed != nil {
			if b, ok := p.textStreamed[se.Event.Index]; ok {
				b.Reset()
			}
		}
		if p.reasoningStreamed != nil {
			if b, ok := p.reasoningStreamed[se.Event.Index]; ok {
				b.Reset()
			}
		}
	case "content_block_delta":
		// 该请求的第一个内容帧（文本、思考、工具调用参数）到达：开始计时。
		if se.Event.Delta.Text != "" || se.Event.Delta.Thinking != "" || se.Event.Delta.Type == "input_json_delta" {
			p.startRequest(p.nowTime())
		}
		switch se.Event.Delta.Type {
		case "text_delta":
			if se.Event.Delta.Text != "" {
				p.curTextIndex = se.Event.Index
				_ = harness.Emit(emit, harness.Event{
					Type: harness.EventText,
					Text: se.Event.Delta.Text,
				})
				p.appendTextStreamed(se.Event.Index, se.Event.Delta.Text)
				if p.textPending == nil {
					p.textPending = make(map[int]bool)
				}
				p.textPending[se.Event.Index] = true
			}
		case "thinking_delta":
			if se.Event.Delta.Thinking != "" {
				p.curReasoningIndex = se.Event.Index
				_ = harness.Emit(emit, harness.Event{
					Type: harness.EventReasoning,
					Text: se.Event.Delta.Thinking,
				})
				p.appendReasoningStreamed(se.Event.Index, se.Event.Delta.Thinking)
			}
		}
	}
}

func (p *parser) handleAssistant(line []byte, isSubagent bool, emit func(harness.Event)) {
	var asst struct {
		ParentToolUseID *string `json:"parent_tool_use_id"`
		// Error 是接口失败时 CLI 填的结构化失败码（authentication_failed、rate_limit 等）。
		Error   string `json:"error"`
		Message struct {
			ID              string  `json:"id"`
			ParentToolUseID *string `json:"parent_tool_use_id"`
			Content         []struct {
				Type     string          `json:"type"`
				Text     string          `json:"text"`
				Thinking string          `json:"thinking"`
				ID       string          `json:"id"`
				Name     string          `json:"name"`
				Input    json.RawMessage `json:"input"`
			} `json:"content"`
			Usage *usagePayload `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &asst); err != nil {
		return
	}
	if (asst.ParentToolUseID != nil && *asst.ParentToolUseID != "") ||
		(asst.Message.ParentToolUseID != nil && *asst.Message.ParentToolUseID != "") {
		isSubagent = true
	}

	// 子代理自己的接口失败不属于这一轮的错误，只有顶层消息才记。
	if !isSubagent && asst.Error != "" {
		p.errCode = asst.Error
	}

	// 顶层 assistant 完整消息带 message.usage 时以它为准覆盖快照：一条完整消息就是一次模型请求的
	// 最终用量；子代理消息的 usage 不属于本轮的上下文。
	if !isSubagent && asst.Message.Usage != nil {
		p.ctxSnapshot = asst.Message.Usage.snapshot()
		// 没有 partial 流（没有 message_delta）时，用助手消息的 output_tokens 结算该请求。
		p.settleFromAssistant(asst.Message.ID, asst.Message.Usage)
	}

	for i, blk := range asst.Message.Content {
		switch blk.Type {
		case "text":
			if isSubagent {
				continue
			}
			idx := p.textIndexFor(i, blk.Text)
			s := p.textStreamedFor(idx)
			if s == "" {
				if blk.Text != "" {
					_ = harness.Emit(emit, harness.Event{
						Type: harness.EventText,
						Text: blk.Text,
					})
				}
			} else if suffix, ok := strings.CutPrefix(blk.Text, s); ok {
				if suffix != "" {
					_ = harness.Emit(emit, harness.Event{
						Type: harness.EventText,
						Text: suffix,
					})
				}
			} else if blk.Text != s {
				log.Printf("claude: stream text mismatch with assistant block %d: streamed %q vs block %q", i, s, blk.Text)
			}
			p.setTextStreamed(idx, blk.Text)
			delete(p.textPending, idx)

		case "thinking":
			if isSubagent {
				continue
			}
			idx := p.reasoningIndexFor(i, blk.Thinking)
			s := p.reasoningStreamedFor(idx)
			if s == "" {
				if blk.Thinking != "" {
					_ = harness.Emit(emit, harness.Event{
						Type: harness.EventReasoning,
						Text: blk.Thinking,
					})
				}
			} else if suffix, ok := strings.CutPrefix(blk.Thinking, s); ok {
				if suffix != "" {
					_ = harness.Emit(emit, harness.Event{
						Type: harness.EventReasoning,
						Text: suffix,
					})
				}
			} else if blk.Thinking != s {
				log.Printf("claude: stream reasoning mismatch with assistant block %d: streamed %q vs block %q", i, s, blk.Thinking)
			}
			p.setReasoningStreamed(idx, blk.Thinking)

		case "tool_use":
			p.resetStreamed()
			if blk.ID != "" && blk.Name != "" {
				_ = harness.Emit(emit, harness.Event{
					Type:     harness.EventToolStart,
					ToolID:   blk.ID,
					ToolName: blk.Name,
					Input:    blk.Input,
				})

				if blk.Name == "Edit" || blk.Name == "Write" || blk.Name == "MultiEdit" {
					var in struct {
						FilePath string `json:"file_path"`
					}
					if err := json.Unmarshal(blk.Input, &in); err == nil && in.FilePath != "" {
						_ = harness.Emit(emit, harness.Event{
							Type: harness.EventFileChange,
							Path: in.FilePath,
						})
					}
				}
			}
		}
	}
}

func (p *parser) handleUser(line []byte, isSubagent bool, emit func(harness.Event)) {
	var usr struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		ToolResultMeta []toolResultMeta `json:"tool_result_meta"`
		// ToolUseResult 是工具的结构化输出（CLI 2.1.x）：编辑类工具在这里给出完整 patch。
		ToolUseResult json.RawMessage `json:"tool_use_result"`
	}
	if err := json.Unmarshal(line, &usr); err != nil {
		return
	}

	var blocks []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
		IsError   bool            `json:"is_error"`
	}
	if json.Unmarshal(usr.Message.Content, &blocks) != nil {
		var text string
		if json.Unmarshal(usr.Message.Content, &text) == nil && !isSubagent {
			p.handleTaskNotifications(text, emit)
		}
	}
	for _, blk := range blocks {
		if blk.Type == "text" && !isSubagent {
			p.handleTaskNotifications(blk.Text, emit)
		}
		if blk.Type == "tool_result" && blk.ToolUseID != "" {
			output := parseToolResultContent(blk.Content)
			_ = harness.Emit(emit, harness.Event{
				Type:    harness.EventToolEnd,
				ToolID:  blk.ToolUseID,
				Output:  output,
				IsError: blk.IsError,
				Blocked: isHookBlockedResult(usr.ToolResultMeta, blk.ToolUseID, output, blk.IsError),
			})
			if match := backgroundTaskPattern.FindStringSubmatch(output); !isSubagent && len(match) > 1 {
				if p.backgroundTasks == nil {
					p.backgroundTasks = make(map[string]string)
				}
				p.backgroundTasks[match[1]] = blk.ToolUseID
				_ = harness.Emit(emit, harness.Event{
					Type: harness.EventBackgroundTaskStart, ToolID: blk.ToolUseID, TaskID: match[1],
				})
			}
		}
	}

	// tool_use 阶段只能拿到路径，完整 diff 要等这里：补发一条带 Patch 的文件变更事件。
	// 消费方按路径去重，同一路径重复上报无害；解析不出时不补发、不报错。
	if path, patch := filePatchFromToolResult(usr.ToolUseResult); path != "" {
		_ = harness.Emit(emit, harness.Event{
			Type:  harness.EventFileChange,
			Path:  path,
			Patch: patch,
		})
	}
}

func (p *parser) handleTaskNotifications(text string, emit func(harness.Event)) {
	for _, notification := range taskNotificationPattern.FindAllString(text, -1) {
		id := taskIDPattern.FindStringSubmatch(notification)
		status := taskStatusPattern.FindStringSubmatch(notification)
		if len(id) < 2 || len(status) < 2 {
			continue
		}
		toolID := p.backgroundTasks[id[1]]
		if match := toolUseIDPattern.FindStringSubmatch(notification); len(match) > 1 {
			toolID = match[1]
		}
		_ = harness.Emit(emit, harness.Event{
			Type: harness.EventBackgroundTaskEnd, ToolID: toolID, TaskID: id[1], TaskStatus: status[1],
		})
		delete(p.backgroundTasks, id[1])
	}
}

// toolUseResult 是 user 消息顶层 tool_use_result 里与文件变更相关的字段：Edit/MultiEdit 与覆盖写的
// Write 带 structuredPatch，新建文件的 Write 只带 type=create 与 content。
type toolUseResult struct {
	Type            string          `json:"type"`
	FilePath        string          `json:"filePath"`
	Content         string          `json:"content"`
	StructuredPatch []toolPatchHunk `json:"structuredPatch"`
}

// toolPatchHunk 是 structuredPatch 中的一个 hunk；Lines 已带 +/-/空格 前缀。
type toolPatchHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// filePatchFromToolResult 从 tool_use_result 还原文件变更的路径与 unified diff（与 codex 的
// EventFileChange.Patch 同一格式）。拿不到 structuredPatch（老版本 CLI 或非编辑类工具）时返回空串，
// 调用方据此不补发事件。
func filePatchFromToolResult(raw json.RawMessage) (string, string) {
	if len(raw) == 0 {
		return "", ""
	}
	var res toolUseResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", ""
	}
	path := strings.TrimSpace(res.FilePath)
	if path == "" {
		return "", ""
	}
	hdr := diffHeaderPath(path)
	var b strings.Builder
	if len(res.StructuredPatch) > 0 {
		b.WriteString("--- a/" + hdr + "\n+++ b/" + hdr + "\n")
		for _, h := range res.StructuredPatch {
			fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
			for _, l := range h.Lines {
				// 行已带前缀，原样保留；"\\ No newline at end of file" 也走这条路径。
				b.WriteString(l)
				b.WriteString("\n")
			}
		}
		return path, b.String()
	}
	if res.Type != "create" || res.Content == "" {
		return "", ""
	}
	lines := strings.Split(strings.TrimSuffix(res.Content, "\n"), "\n")
	fmt.Fprintf(&b, "--- /dev/null\n+++ b/%s\n@@ -0,0 +1,%d @@\n", hdr, len(lines))
	for _, l := range lines {
		b.WriteString("+")
		b.WriteString(l)
		b.WriteString("\n")
	}
	return path, b.String()
}

// diffHeaderPath 给 diff 头部准备路径：只去掉前导的 "./" 与 "/"。parser 拿不到工作目录，
// 无法还原成相对仓库的路径，因此保留其余部分不做改写。
func diffHeaderPath(path string) string {
	p := strings.TrimPrefix(path, "./")
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return path
	}
	return p
}

// toolResultMeta 是 tool_result 行外层的结构化标记（CLI 2.1.x 起提供），
// 用于把「被 PreToolUse hook 拦截」和「工具执行失败」区分开。
type toolResultMeta struct {
	ID                 string `json:"id"`
	PermissionDecision struct {
		Decision string `json:"decision"`
		Source   string `json:"source"`
	} `json:"permission_decision"`
}

// isHookBlockedResult 判断一条 tool_result 是否来自 hook 拦截（而非工具故障）。
// 优先用结构化标记 tool_result_meta；没有该字段的老版本 CLI 回退到文本前缀兜底。
func isHookBlockedResult(meta []toolResultMeta, toolUseID, output string, isError bool) bool {
	if !isError {
		return false
	}
	for _, m := range meta {
		if m.ID != "" && m.ID != toolUseID {
			continue
		}
		if m.PermissionDecision.Decision == "reject" && m.PermissionDecision.Source == "hook" {
			return true
		}
	}
	// 老版本兜底：拦截时 CLI 会把 tool_result 文本写成 `PreToolUse:<工具名> hook error: ...`。
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "PreToolUse:") && strings.Contains(line, " hook error:") {
			return true
		}
	}
	return false
}

func parseToolResultContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var sb strings.Builder
		for _, b := range blocks {
			if b.Type == "text" {
				sb.WriteString(b.Text)
			}
		}
		return sb.String()
	}
	return string(raw)
}

// modelUsageInfo 是 result.modelUsage 中单个模型的用量信息，这里只取上下文窗口上限。
type modelUsageInfo struct {
	ContextWindow int64 `json:"contextWindow"`
}

// maxContextWindow 取 modelUsage 中最大的上下文窗口：一轮可能同时用到主模型与小模型，
// 界面关心的是主模型那一档，取最大值即可；没有该字段时返回 0。
func maxContextWindow(modelUsage map[string]modelUsageInfo) int64 {
	var max int64
	for _, m := range modelUsage {
		if m.ContextWindow > max {
			max = m.ContextWindow
		}
	}
	return max
}

func (p *parser) handleResult(line []byte, emit func(harness.Event)) {
	p.sawResult = true

	var res struct {
		Subtype    string `json:"subtype"`
		IsError    bool   `json:"is_error"`
		Result     string `json:"result"`
		StopReason string `json:"stop_reason"`
		// TerminalReason 是本轮结束的结构化原因（completed、api_error、aborted_streaming 等），
		// 与 subtype 相互独立：失败时 subtype 仍可能是 success。
		TerminalReason string `json:"terminal_reason"`
		// APIErrorStatus 是失败时的 HTTP 状态码，成功时为 null。
		APIErrorStatus int     `json:"api_error_status"`
		TotalCostUSD   float64 `json:"total_cost_usd"`
		Usage          *struct {
			InputTokens          int64 `json:"input_tokens"`
			OutputTokens         int64 `json:"output_tokens"`
			CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
		ModelUsage map[string]modelUsageInfo `json:"modelUsage"`
	}
	if err := json.Unmarshal(line, &res); err != nil {
		return
	}

	if res.Usage != nil {
		// 计费用量仍是整轮累计（多次请求之和）；上下文占用只认本轮最后一次顶层请求的快照，
		// 拿不到快照时留 0，让界面沿用旧口径。Requests 是本轮每次请求的速度样本。
		var samples []harness.RequestSpeed
		if len(p.requests) > 0 {
			samples = make([]harness.RequestSpeed, len(p.requests))
			copy(samples, p.requests)
		}
		_ = harness.Emit(emit, harness.Event{
			Type: harness.EventUsage,
			Usage: &harness.Usage{
				InputTokens:   res.Usage.InputTokens,
				OutputTokens:  res.Usage.OutputTokens,
				CachedTokens:  res.Usage.CacheReadInputTokens,
				ContextTokens: p.ctxSnapshot.total(),
				ContextWindow: maxContextWindow(res.ModelUsage),
				CostUSD:       res.TotalCostUSD,
				Requests:      samples,
			},
		})
	}
	// 本轮结束：样本已随 usage 上报，清空并停表，避免残留到下一次运行。
	p.requests = nil
	p.beginNewRequest("")

	if res.IsError {
		errStr := res.Result
		if errStr == "" {
			errStr = res.Subtype
		}
		if errStr == "" {
			errStr = "error"
		}
		_ = harness.Emit(emit, harness.Event{
			Type:    harness.EventError,
			Err:     errStr,
			ErrKind: classifyResultError(p.errCode, res.Subtype, res.TerminalReason, res.APIErrorStatus, errStr),
		})
	}
	p.errCode = ""

	stopReason := res.StopReason
	if stopReason == "" {
		stopReason = res.Subtype
	}
	_ = harness.Emit(emit, harness.Event{
		Type:       harness.EventDone,
		StopReason: stopReason,
	})
}

// classifyResultError 判断一条失败 result 的错误类别，可靠度从高到低：结束原因 terminal_reason >
// 合成 assistant 消息上的 error 失败码 > result.subtype > HTTP 状态码 > 错误文本关键词。
// 判不出来时归为 harness.ErrKindAPIError，交给消费方按文本展示。
func classifyResultError(errCode, subtype, terminalReason string, apiStatus int, text string) string {
	switch terminalReason {
	case "aborted_streaming", "aborted_tools":
		// 用户或客户端中止了本轮（CLI 自己把 aborted_* 当作非失败），不归到接口错误。
		return harness.ErrKindCancelled
	case "prompt_too_long", "blocking_limit", "rapid_refill_breaker":
		return harness.ErrKindContextExhausted
	case "max_turns":
		return harness.ErrKindMaxTurns
	}
	if kind := errKindFromAPICode(errCode, text); kind != "" {
		return kind
	}
	switch subtype {
	case "error_max_turns":
		return harness.ErrKindMaxTurns
	case "error_max_budget_usd":
		// 预算耗尽与上下文、限流都不是一回事，但也没有更合适的类别，归为接口错误。
		return harness.ErrKindAPIError
	case "error_max_structured_output_retries":
		return harness.ErrKindAPIError
	}
	if kind := errKindFromStatus(apiStatus); kind != "" {
		return kind
	}
	return errKindFromText(text)
}

// errKindFromAPICode 把合成 assistant 消息上的 error 取值映射为错误类别。取值集合与 CLI 自己的
// 处置说明一致：authentication_failed / oauth_org_not_allowed / account_on_hold → 登录或账号问题，
// rate_limit / overloaded → 稍后重试，invalid_request → 看文本（提示词过长时归到上下文超限）。
// 其余取值（billing_error、model_not_found、cloud_credential_error 等）需要用户在控制台处理，
// 没有对应的类别，归为接口错误。
func errKindFromAPICode(errCode, text string) string {
	switch errCode {
	case "authentication_failed", "oauth_org_not_allowed", "account_on_hold":
		return harness.ErrKindAuthRequired
	case "rate_limit", "overloaded":
		return harness.ErrKindRateLimited
	case "invalid_request":
		if isContextOverflowText(text) {
			return harness.ErrKindContextExhausted
		}
	}
	return ""
}

// errKindFromStatus 按 HTTP 状态码判断类别：401/403 认证失败、429 限流、413 请求过大。
func errKindFromStatus(status int) string {
	switch status {
	case 401, 403:
		return harness.ErrKindAuthRequired
	case 429:
		return harness.ErrKindRateLimited
	case 413:
		return harness.ErrKindContextExhausted
	}
	return ""
}

// errKindFromText 是最后的兜底：按错误文本关键词判断类别，命中不了归为接口错误。
func errKindFromText(text string) string {
	lower := strings.ToLower(text)
	for _, kw := range []string{
		"not logged in", "/login", "login required", "please log in", "not signed in",
		"authentication_failed", "authentication failed", "authentication_error",
		"invalid api key", "invalid_api_key", "unauthorized",
	} {
		if strings.Contains(lower, kw) {
			return harness.ErrKindAuthRequired
		}
	}
	for _, kw := range []string{"rate limit", "rate_limit", "too many requests", "overloaded", "throttl"} {
		if strings.Contains(lower, kw) {
			return harness.ErrKindRateLimited
		}
	}
	if isContextOverflowText(text) {
		return harness.ErrKindContextExhausted
	}
	// 裸状态码另判：避免把长数字串里的片段当成 429。
	switch {
	case hasStatusCode(lower, "401"), hasStatusCode(lower, "403"):
		return harness.ErrKindAuthRequired
	case hasStatusCode(lower, "429"):
		return harness.ErrKindRateLimited
	case hasStatusCode(lower, "413"):
		return harness.ErrKindContextExhausted
	}
	return harness.ErrKindAPIError
}

// isContextOverflowText 判断文本是否在说提示词/上下文过大；关键词与 CLI 自己的同类判断保持一致。
func isContextOverflowText(text string) bool {
	lower := strings.ToLower(text)
	for _, kw := range []string{
		"prompt is too long", "too long", "too large", "token limit",
		"context length", "context window", "too many tokens",
	} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// hasStatusCode 判断文本里是否出现裸状态码（左右不是数字），避免误判长数字串。
func hasStatusCode(text, code string) bool {
	for off := 0; ; {
		i := strings.Index(text[off:], code)
		if i < 0 {
			return false
		}
		i += off
		before, after := byte(' '), byte(' ')
		if i > 0 {
			before = text[i-1]
		}
		if i+len(code) < len(text) {
			after = text[i+len(code)]
		}
		if !isASCIIDigit(before) && !isASCIIDigit(after) {
			return true
		}
		off = i + len(code)
	}
}

// isASCIIDigit 判断是否为 ASCII 数字。
func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }
