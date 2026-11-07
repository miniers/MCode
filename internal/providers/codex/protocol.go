package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mcode/internal/harness"
)

// JSONRPCMessage 表示通用的 JSON-RPC 2.0 报文。
type JSONRPCMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`

	onAck func(err error)
}

// JSONRPCError 表示 JSON-RPC 2.0 错误对象。
type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// IsRequest 判断是否为请求（包含方法名和有效 ID）。
func (m *JSONRPCMessage) IsRequest() bool {
	if m.Method == "" {
		return false
	}
	id := bytes.TrimSpace(m.ID)
	return len(id) > 0 && !bytes.Equal(id, []byte("null"))
}

// IsNotification 判断是否为通知（包含方法名，但无有效 ID）。
func (m *JSONRPCMessage) IsNotification() bool {
	if m.Method == "" {
		return false
	}
	id := bytes.TrimSpace(m.ID)
	return len(id) == 0 || bytes.Equal(id, []byte("null"))
}

// IsResponse 判断是否为响应（不含方法名，但有有效 ID）。
func (m *JSONRPCMessage) IsResponse() bool {
	if m.Method != "" {
		return false
	}
	id := bytes.TrimSpace(m.ID)
	return len(id) > 0 && !bytes.Equal(id, []byte("null"))
}

// RawID 返回 ID 字符串。
func (m *JSONRPCMessage) RawID() string {
	return string(bytes.TrimSpace(m.ID))
}

// ProtocolHandler 负责维护单轮对话的流式增量状态、去重、事件映射及审批逻辑。
// 不依赖外部进程，可直接用于单元测试。
type ProtocolHandler struct {
	turn              harness.Turn
	mu                sync.Mutex
	streamedText      map[string]*strings.Builder
	streamedReasoning map[string]*strings.Builder
	streamedPlan      map[string]*strings.Builder
	startedTools      map[string]string
	closedTools       map[string]struct{}
	sentFileChanges   map[string]string
	itemChanges       map[string][]rawFileChange
	accumulatedUsage  harness.Usage
	usageBase         usageCounts
	usageBaseSet      bool
	loggedUnknown     map[string]struct{}
	turnDone          bool

	// 每个 ProtocolHandler 只服务一轮，请求计时和样本列表随本轮创建、随本轮丢弃。
	requestClock  harness.RequestClock
	requestSpeeds []harness.RequestSpeed
	now           func() time.Time
}

// NewProtocolHandler 创建一个 ProtocolHandler 实例。
func NewProtocolHandler(turn harness.Turn) *ProtocolHandler {
	return &ProtocolHandler{
		turn:              turn,
		streamedText:      make(map[string]*strings.Builder),
		streamedReasoning: make(map[string]*strings.Builder),
		streamedPlan:      make(map[string]*strings.Builder),
		startedTools:      make(map[string]string),
		closedTools:       make(map[string]struct{}),
		sentFileChanges:   make(map[string]string),
		itemChanges:       make(map[string][]rawFileChange),
		loggedUnknown:     make(map[string]struct{}),
	}
}

// IsTurnDone 报告当前轮次是否已完成。
func (h *ProtocolHandler) IsTurnDone() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.turnDone
}

// HandleLine 解析单行 JSON-RPC 报文并派发对应事件。
// 若该行为服务端请求（例如工具审批），则调用审批处理并返回应答报文。
func (h *ProtocolHandler) HandleLine(ctx context.Context, line []byte, emit func(harness.Event)) (*JSONRPCMessage, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, nil
	}

	var msg JSONRPCMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return nil, fmt.Errorf("codex: unmarshal rpc frame: %w", err)
	}

	switch {
	case msg.IsRequest():
		return h.HandleServerRequest(ctx, &msg)
	case msg.IsNotification():
		h.HandleNotification(&msg, emit)
		return nil, nil
	default:
		// 响应消息，上层可在 pending map 中匹配
		return nil, nil
	}
}

// HandleNotification 处理服务端通知并映射为 harness.Event。
func (h *ProtocolHandler) HandleNotification(msg *JSONRPCMessage, emit func(harness.Event)) {
	switch msg.Method {
	case "item/agentMessage/delta":
		var p struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		if err := json.Unmarshal(msg.Params, &p); err == nil && p.Delta != "" {
			h.beginRequest()
			h.mu.Lock()
			b, ok := h.streamedText[p.ItemID]
			if !ok {
				b = &strings.Builder{}
				h.streamedText[p.ItemID] = b
			}
			b.WriteString(p.Delta)
			h.mu.Unlock()

			_ = harness.Emit(emit, harness.Event{
				Type: harness.EventText,
				Text: p.Delta,
			})
		}

	case "item/reasoning/textDelta", "item/reasoning/summaryTextDelta":
		var p struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		if err := json.Unmarshal(msg.Params, &p); err == nil && p.Delta != "" {
			h.beginRequest()
			h.mu.Lock()
			b, ok := h.streamedReasoning[p.ItemID]
			if !ok {
				b = &strings.Builder{}
				h.streamedReasoning[p.ItemID] = b
			}
			b.WriteString(p.Delta)
			h.mu.Unlock()

			_ = harness.Emit(emit, harness.Event{
				Type: harness.EventReasoning,
				Text: p.Delta,
			})
		}

	case "item/started":
		h.handleItemStarted(msg.Params, emit)

	case "item/completed":
		h.handleItemCompleted(msg.Params, emit)

	case "item/fileChange/patchUpdated":
		h.handlePatchUpdated(msg.Params, emit)

	case "item/plan/delta":
		var p struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		if err := json.Unmarshal(msg.Params, &p); err == nil && p.Delta != "" {
			h.beginRequest()
			h.mu.Lock()
			b, ok := h.streamedPlan[p.ItemID]
			if !ok {
				b = &strings.Builder{}
				h.streamedPlan[p.ItemID] = b
			}
			b.WriteString(p.Delta)
			h.mu.Unlock()

			_ = harness.Emit(emit, harness.Event{
				Type: harness.EventText,
				Text: p.Delta,
			})
		}

	case "thread/tokenUsage/updated":
		h.handleTokenUsage(msg.Params, emit)

	case "turn/completed":
		h.handleTurnCompleted(msg.Params, emit)

	case "turn/failed":
		h.handleTurnFailed(msg.Params, emit)

	case "error":
		var p struct {
			Message   string `json:"message"`
			WillRetry bool   `json:"willRetry"`
			Error     *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		errMsg := ""
		if err := json.Unmarshal(msg.Params, &p); err == nil {
			if p.Error != nil && p.Error.Message != "" {
				errMsg = p.Error.Message
			} else if p.Message != "" {
				errMsg = p.Message
			}
		}
		if errMsg != "" {
			// 轮次报错后 Codex 不会再为本轮未完成的工具发 item/completed，先补失败收尾；
			// willRetry 为 true 时本轮还会重试，工具可能后续正常完成，留给最终的失败或完成通知收尾。
			if !p.WillRetry {
				h.finishPendingTools(toolEndReasonFailed, emit)
			}
			_ = harness.Emit(emit, harness.Event{
				Type: harness.EventError,
				Err:  errMsg,
			})
		}

	case "thread/compacted":
		// 线程上下文被 Codex 压缩：通知里没有 token 数，只落一条边界（token 留 0 表示未知）。
		_ = harness.Emit(emit, harness.Event{Type: harness.EventContextCompacted})

	case "turn/started", "thread/started", "thread/status/changed", "thread/name/updated",
		"account/rateLimits/updated", "warning", "configWarning",
		"item/commandExecution/outputDelta", "item/fileChange/outputDelta",
		"turn/diff/updated", "turn/plan/updated":
		// 已知但本层有意忽略：不映射事件，也不算未处理通知。
		// outputDelta 类待接入；turn/diff/updated 是整轮聚合 diff，改动已由 item 级事件覆盖，不发事件。

	default:
		h.logUnknownOnce(msg.Method)
	}
}

type rawThreadItem struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Text             string          `json:"text,omitempty"`
	Summary          []string        `json:"summary,omitempty"`
	Content          []string        `json:"content,omitempty"`
	Command          string          `json:"command,omitempty"`
	Status           string          `json:"status,omitempty"`
	ExitCode         *int            `json:"exitCode,omitempty"`
	AggregatedOutput string          `json:"aggregatedOutput,omitempty"`
	Changes          []rawFileChange `json:"changes,omitempty"`
	Server           string          `json:"server,omitempty"`
	Tool             string          `json:"tool,omitempty"`
	Review           string          `json:"review,omitempty"`
	Result           json.RawMessage `json:"result,omitempty"`
	Error            json.RawMessage `json:"error,omitempty"`
	Success          *bool           `json:"success,omitempty"`
}

type rawFileChange struct {
	Path string `json:"path"`
	Diff string `json:"diff"`
	Kind string `json:"kind"`
}

func (h *ProtocolHandler) handleItemStarted(params json.RawMessage, emit func(harness.Event)) {
	var p struct {
		Item json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(params, &p); err != nil || len(p.Item) == 0 {
		return
	}

	var it rawThreadItem
	if err := json.Unmarshal(p.Item, &it); err != nil || it.ID == "" {
		return
	}

	// 同一 item 重新开始（例如重试）时清掉上一轮的收尾标记，后续仍能再发一次 tool_end。
	h.mu.Lock()
	delete(h.closedTools, it.ID)
	h.mu.Unlock()

	if len(it.Changes) > 0 {
		h.mu.Lock()
		h.itemChanges[it.ID] = it.Changes
		h.mu.Unlock()
	}

	switch it.Type {
	case "commandExecution":
		toolName := "Bash"
		if it.Command != "" {
			toolName = it.Command
		}
		h.mu.Lock()
		h.startedTools[it.ID] = toolName
		h.mu.Unlock()

		_ = harness.Emit(emit, harness.Event{
			Type:     harness.EventToolStart,
			ToolID:   it.ID,
			ToolName: toolName,
			Input:    p.Item,
		})

	case "fileChange":
		toolName := "FileChange"
		h.mu.Lock()
		h.startedTools[it.ID] = toolName
		h.mu.Unlock()

		_ = harness.Emit(emit, harness.Event{
			Type:     harness.EventToolStart,
			ToolID:   it.ID,
			ToolName: toolName,
			Input:    p.Item,
		})

	case "mcpToolCall":
		toolName := it.Tool
		if toolName == "" {
			toolName = "mcpToolCall"
		}
		if it.Server != "" {
			toolName = it.Server + "/" + toolName
		}
		h.mu.Lock()
		h.startedTools[it.ID] = toolName
		h.mu.Unlock()

		_ = harness.Emit(emit, harness.Event{
			Type:     harness.EventToolStart,
			ToolID:   it.ID,
			ToolName: toolName,
			Input:    p.Item,
		})

	case "dynamicToolCall":
		toolName := it.Tool
		if toolName == "" {
			toolName = "dynamicToolCall"
		}
		h.mu.Lock()
		h.startedTools[it.ID] = toolName
		h.mu.Unlock()

		_ = harness.Emit(emit, harness.Event{
			Type:     harness.EventToolStart,
			ToolID:   it.ID,
			ToolName: toolName,
			Input:    p.Item,
		})

	case "webSearch":
		toolName := "webSearch"
		h.mu.Lock()
		h.startedTools[it.ID] = toolName
		h.mu.Unlock()

		_ = harness.Emit(emit, harness.Event{
			Type:     harness.EventToolStart,
			ToolID:   it.ID,
			ToolName: toolName,
			Input:    p.Item,
		})

	case "plan", "enteredReviewMode", "exitedReviewMode", "contextCompaction":
		// 这些是展示类 item，不是工具调用，不发 EventToolStart。
	}
}

func (h *ProtocolHandler) handleItemCompleted(params json.RawMessage, emit func(harness.Event)) {
	var p struct {
		Item json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(params, &p); err != nil || len(p.Item) == 0 {
		return
	}

	var it rawThreadItem
	if err := json.Unmarshal(p.Item, &it); err != nil || it.ID == "" {
		return
	}

	if len(it.Changes) > 0 {
		h.mu.Lock()
		h.itemChanges[it.ID] = it.Changes
		h.mu.Unlock()
	}

	switch it.Type {
	case "agentMessage":
		h.mu.Lock()
		var streamed string
		if b, ok := h.streamedText[it.ID]; ok {
			streamed = b.String()
		}
		h.mu.Unlock()

		if streamed == "" {
			if it.Text != "" {
				_ = harness.Emit(emit, harness.Event{
					Type: harness.EventText,
					Text: it.Text,
				})
			}
		} else if suffix, ok := strings.CutPrefix(it.Text, streamed); ok {
			if suffix != "" {
				_ = harness.Emit(emit, harness.Event{
					Type: harness.EventText,
					Text: suffix,
				})
			}
		}

	case "reasoning":
		h.mu.Lock()
		var streamed string
		if b, ok := h.streamedReasoning[it.ID]; ok {
			streamed = b.String()
		}
		h.mu.Unlock()

		if streamed == "" {
			full := it.Text
			if full == "" && len(it.Summary) > 0 {
				full = strings.Join(it.Summary, " ")
			}
			if full == "" && len(it.Content) > 0 {
				full = strings.Join(it.Content, "\n")
			}
			if full != "" {
				_ = harness.Emit(emit, harness.Event{
					Type: harness.EventReasoning,
					Text: full,
				})
			}
		}

	case "plan":
		// 计划正文按普通文本展示；已流过 delta 时只补尾部，避免重复输出。
		h.mu.Lock()
		var streamed string
		if b, ok := h.streamedPlan[it.ID]; ok {
			streamed = b.String()
		}
		h.mu.Unlock()

		if streamed == "" {
			if it.Text != "" {
				_ = harness.Emit(emit, harness.Event{
					Type: harness.EventText,
					Text: it.Text,
				})
			}
		} else if suffix, ok := strings.CutPrefix(it.Text, streamed); ok && suffix != "" {
			_ = harness.Emit(emit, harness.Event{
				Type: harness.EventText,
				Text: suffix,
			})
		}

	case "enteredReviewMode":
		// 进入审阅模式只标记状态，不对外发事件。

	case "exitedReviewMode":
		if it.Review != "" {
			_ = harness.Emit(emit, harness.Event{
				Type: harness.EventText,
				Text: it.Review,
			})
		}

	case "contextCompaction":
		_ = harness.Emit(emit, harness.Event{
			Type: harness.EventText,
			Text: "上下文已压缩",
		})

	case "commandExecution":
		if !h.finishToolIfOpen(it.ID) {
			return
		}

		isErr := false
		if (it.ExitCode != nil && *it.ExitCode != 0) || it.Status == "failed" {
			isErr = true
		}
		_ = harness.Emit(emit, harness.Event{
			Type:    harness.EventToolEnd,
			ToolID:  it.ID,
			Output:  it.AggregatedOutput,
			IsError: isErr,
		})

	case "fileChange":
		h.mu.Lock()
		delete(h.startedTools, it.ID)
		h.mu.Unlock()

		isErr := it.Status == "failed"
		_ = harness.Emit(emit, harness.Event{
			Type:    harness.EventToolEnd,
			ToolID:  it.ID,
			Output:  it.Status,
			IsError: isErr,
		})

		h.emitFileChanges(it.ID, it.Changes, emit)

	case "mcpToolCall":
		if !h.finishToolIfOpen(it.ID) {
			return
		}

		isErr := it.Status == "failed" || len(it.Error) > 0
		output := string(it.Result)
		if len(it.Error) > 0 {
			output = string(it.Error)
		}
		_ = harness.Emit(emit, harness.Event{
			Type:    harness.EventToolEnd,
			ToolID:  it.ID,
			Output:  output,
			IsError: isErr,
		})

	case "dynamicToolCall":
		if !h.finishToolIfOpen(it.ID) {
			return
		}

		isErr := it.Status == "failed"
		if it.Success != nil && !*it.Success {
			isErr = true
		}
		_ = harness.Emit(emit, harness.Event{
			Type:    harness.EventToolEnd,
			ToolID:  it.ID,
			Output:  string(it.Result),
			IsError: isErr,
		})

	default:
		h.mu.Lock()
		_, tracked := h.startedTools[it.ID]
		if tracked {
			delete(h.startedTools, it.ID)
		}
		h.mu.Unlock()

		if tracked {
			_ = harness.Emit(emit, harness.Event{
				Type:    harness.EventToolEnd,
				ToolID:  it.ID,
				Output:  it.Status,
				IsError: it.Status == "failed",
			})
		}
	}
}

// finishToolIfOpen 把已完成的工具从「已开始未结束」集合中移除，并报告是否还需要发 tool_end。
// 工具已被失败/中断收尾补发过 tool_end 时返回 false，避免同一个 ToolID 出现两条结束事件；
// 没有对应 item/started、直接收到 item/completed 的工具不受影响，仍然发 tool_end。
func (h *ProtocolHandler) finishToolIfOpen(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.startedTools, id)
	_, closed := h.closedTools[id]
	return !closed
}

// handlePatchUpdated 处理 item/fileChange/patchUpdated 增量：每个改动映射为一条 EventFileChange。
func (h *ProtocolHandler) handlePatchUpdated(params json.RawMessage, emit func(harness.Event)) {
	var p struct {
		ItemID  string          `json:"itemId"`
		Changes []rawFileChange `json:"changes"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.ItemID == "" {
		return
	}

	h.mu.Lock()
	if len(p.Changes) > 0 {
		h.itemChanges[p.ItemID] = p.Changes
	}
	h.mu.Unlock()

	h.emitFileChanges(p.ItemID, p.Changes, emit)
}

// emitFileChanges 把一个 fileChange item 的改动列表映射为 EventFileChange，
// Patch 取 item 里的 unified diff（与完成路径一致）。
// patchUpdated 增量与 item/completed 经常携带同一份 diff，所以按 (itemId, path) 记住最近一次
// 已发出的 patch，内容相同时不再重复发。
func (h *ProtocolHandler) emitFileChanges(itemID string, changes []rawFileChange, emit func(harness.Event)) {
	for _, ch := range changes {
		if ch.Path == "" {
			continue
		}
		patch := ch.Diff
		key := itemID + "\x00" + ch.Path

		h.mu.Lock()
		sent, ok := h.sentFileChanges[key]
		if ok && sent == patch {
			h.mu.Unlock()
			continue
		}
		h.sentFileChanges[key] = patch
		h.mu.Unlock()

		_ = harness.Emit(emit, harness.Event{
			Type:  harness.EventFileChange,
			Path:  ch.Path,
			Patch: patch,
		})
	}
}

// rawTokenUsageBreakdown 对应 schema 中的 TokenUsageBreakdown。
type rawTokenUsageBreakdown struct {
	InputTokens           int64 `json:"inputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
	ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
	CachedInputTokens     int64 `json:"cachedInputTokens"`
}

// usageCounts 是计费口径的三项 token 计数：input 为输入，output 为输出（含推理输出），cached 为命中缓存的输入。
type usageCounts struct {
	input  int64
	output int64
	cached int64
}

// counts 把一份 tokenUsage breakdown 转成计费口径的三项计数。
func (b rawTokenUsageBreakdown) counts() usageCounts {
	return usageCounts{
		input:  b.InputTokens,
		output: b.OutputTokens + b.ReasoningOutputTokens,
		cached: b.CachedInputTokens,
	}
}

func (c usageCounts) sub(o usageCounts) usageCounts {
	return usageCounts{input: c.input - o.input, output: c.output - o.output, cached: c.cached - o.cached}
}

// nonNegative 把因报文异常导致的负值归零，避免上游展示负用量。
func (c usageCounts) nonNegative() usageCounts {
	if c.input < 0 {
		c.input = 0
	}
	if c.output < 0 {
		c.output = 0
	}
	if c.cached < 0 {
		c.cached = 0
	}
	return c
}

// nowTime 返回当前时间；测试可以通过 now 字段注入时钟。
func (h *ProtocolHandler) nowTime() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// beginRequest 记下当前模型请求第一个流式内容帧的时间；同一请求重复调用只认第一次。
func (h *ProtocolHandler) beginRequest() {
	now := h.nowTime()
	h.mu.Lock()
	h.requestClock.Begin(now)
	h.mu.Unlock()
}

// finishRequest 用该请求的用量帧结算速度样本，有效时追加到本轮样本列表。
func (h *ProtocolHandler) finishRequest(outputTokens int64) {
	now := h.nowTime()
	h.mu.Lock()
	defer h.mu.Unlock()
	if speed, ok := h.requestClock.Finish(now, outputTokens); ok {
		h.requestSpeeds = append(h.requestSpeeds, speed)
	}
}

// requestSpeedSnapshot 返回本轮速度样本的拷贝；没有样本时返回 nil。
func (h *ProtocolHandler) requestSpeedSnapshot() []harness.RequestSpeed {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.requestSpeeds) == 0 {
		return nil
	}
	return append([]harness.RequestSpeed(nil), h.requestSpeeds...)
}

func (h *ProtocolHandler) handleTokenUsage(params json.RawMessage, emit func(harness.Event)) {
	var p struct {
		TokenUsage struct {
			Last               rawTokenUsageBreakdown  `json:"last"`
			Total              *rawTokenUsageBreakdown `json:"total"`
			ModelContextWindow int64                   `json:"modelContextWindow"`
		} `json:"tokenUsage"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	last := p.TokenUsage.Last

	// 口径：InputTokens/OutputTokens/CachedTokens 报「本轮累计」。
	// total 是线程级累计（跳轮不清零），因此用 total 减「轮前基线」得出本轮累计；基线由本轮首次
	// 更新的 total - last 推得，所以重复收到同一份 total 不会重复计数。
	// 不带 total 的旧报文退化为逐次累加 last，行为与历史版本一致。
	h.mu.Lock()
	var cur usageCounts
	if total := p.TokenUsage.Total; total != nil {
		totalCounts := total.counts()
		if !h.usageBaseSet {
			h.usageBase = usageCounts{
				input:  totalCounts.input - last.InputTokens,
				output: totalCounts.output - (last.OutputTokens + last.ReasoningOutputTokens),
				cached: totalCounts.cached - last.CachedInputTokens,
			}
			h.usageBaseSet = true
		}
		cur = totalCounts.sub(h.usageBase).nonNegative()
	} else {
		h.accumulatedUsage.InputTokens += last.InputTokens
		h.accumulatedUsage.OutputTokens += last.OutputTokens + last.ReasoningOutputTokens
		h.accumulatedUsage.CachedTokens += last.CachedInputTokens
		cur = usageCounts{
			input:  h.accumulatedUsage.InputTokens,
			output: h.accumulatedUsage.OutputTokens,
			cached: h.accumulatedUsage.CachedTokens,
		}
	}
	h.mu.Unlock()

	// ContextTokens/ContextWindow 是「单次请求」口径：取 last 和模型窗口上限，与累计计费量无关。
	lastCounts := last.counts()

	// last 是单次请求的用量，用它结算这次请求的速度样本；输出 token 含推理输出，与计数口径一致。
	h.finishRequest(lastCounts.output)

	_ = harness.Emit(emit, harness.Event{
		Type: harness.EventUsage,
		Usage: &harness.Usage{
			InputTokens:   cur.input,
			OutputTokens:  cur.output,
			CachedTokens:  cur.cached,
			ContextTokens: lastCounts.input + lastCounts.output,
			ContextWindow: p.TokenUsage.ModelContextWindow,
			Requests:      h.requestSpeedSnapshot(),
		},
	})
}

func (h *ProtocolHandler) handleTurnCompleted(params json.RawMessage, emit func(harness.Event)) {
	var p struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(params, &p)

	h.mu.Lock()
	h.turnDone = true
	h.mu.Unlock()

	// 失败或中断的轮次不会再有 item/completed，先为未完成的工具补发失败收尾。
	switch p.Turn.Status {
	case "failed":
		h.finishPendingTools(toolEndReasonFailed, emit)
	case "interrupted":
		h.finishPendingTools(toolEndReasonInterrupted, emit)
	}

	if p.Turn.Error != nil && p.Turn.Error.Message != "" {
		_ = harness.Emit(emit, harness.Event{
			Type: harness.EventError,
			Err:  p.Turn.Error.Message,
		})
	}

	stopReason := p.Turn.Status
	if stopReason == "" {
		stopReason = "completed"
	}
	_ = harness.Emit(emit, harness.Event{
		Type:       harness.EventDone,
		StopReason: stopReason,
	})
}

func (h *ProtocolHandler) handleTurnFailed(params json.RawMessage, emit func(harness.Event)) {
	h.mu.Lock()
	h.turnDone = true
	h.mu.Unlock()

	var p struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
		Reason  string `json:"reason"`
	}
	_ = json.Unmarshal(params, &p)

	errMsg := ""
	if p.Error != nil && p.Error.Message != "" {
		errMsg = p.Error.Message
	} else if p.Message != "" {
		errMsg = p.Message
	} else if p.Reason != "" {
		errMsg = p.Reason
	}
	if errMsg == "" {
		errMsg = "turn failed"
	}

	h.finishPendingTools(toolEndReasonFailed, emit)

	_ = harness.Emit(emit, harness.Event{
		Type: harness.EventError,
		Err:  errMsg,
	})
	_ = harness.Emit(emit, harness.Event{
		Type:       harness.EventDone,
		StopReason: "failed",
	})
}

const (
	toolEndReasonFailed      = "失败"
	toolEndReasonInterrupted = "已中断"
)

// finishPendingTools 为已发出 EventToolStart 但还没等到 item/completed 的工具补发一条失败收尾事件。
// 补发后直接把工具从待完成集合移除，因此多次失败/中断通知不会重复补发。
func (h *ProtocolHandler) finishPendingTools(reason string, emit func(harness.Event)) {
	h.mu.Lock()
	pending := make([]string, 0, len(h.startedTools))
	for id := range h.startedTools {
		pending = append(pending, id)
	}
	sort.Strings(pending)
	for _, id := range pending {
		delete(h.startedTools, id)
		h.closedTools[id] = struct{}{}
	}
	h.mu.Unlock()

	output := fmt.Sprintf("本轮%s，工具未完成", reason)
	for _, id := range pending {
		_ = harness.Emit(emit, harness.Event{
			Type:    harness.EventToolEnd,
			ToolID:  id,
			Output:  output,
			IsError: true,
		})
	}
}

// logUnknownOnce 对同一个未处理的通知方法名只记一次日志，避免刷屏。
func (h *ProtocolHandler) logUnknownOnce(method string) {
	h.mu.Lock()
	if _, ok := h.loggedUnknown[method]; ok {
		h.mu.Unlock()
		return
	}
	if h.loggedUnknown == nil {
		h.loggedUnknown = make(map[string]struct{})
	}
	h.loggedUnknown[method] = struct{}{}
	h.mu.Unlock()

	log.Printf("codex: 未处理的通知方法 %q（已忽略，同类只记一次）", method)
}

// HandleServerRequest 处理服务端发来的请求，调用 Approver 或根据模式裁决，返回应答报文。
func (h *ProtocolHandler) HandleServerRequest(ctx context.Context, msg *JSONRPCMessage) (*JSONRPCMessage, error) {
	switch msg.Method {
	case "item/commandExecution/requestApproval":
		var p struct {
			ItemID     string `json:"itemId"`
			ApprovalID string `json:"approvalId"`
			Command    string `json:"command"`
			Reason     string `json:"reason"`
			GrantRoot  string `json:"grantRoot"`
		}
		_ = json.Unmarshal(msg.Params, &p)

		toolID := p.ItemID
		if toolID == "" {
			toolID = p.ApprovalID
		}
		if toolID == "" {
			toolID = fmt.Sprintf("codex-%s", msg.RawID())
		}

		summary := "$ " + p.Command
		if p.Reason != "" {
			summary += " (" + p.Reason + ")"
		}

		req := harness.ApprovalRequest{
			ToolID:   toolID,
			ToolName: "Bash",
			Input:    msg.Params,
			Summary:  summary,
		}
		return h.handleApprovalRequest(ctx, msg, req)

	case "item/fileChange/requestApproval":
		var p struct {
			ItemID    string          `json:"itemId"`
			GrantRoot string          `json:"grantRoot"`
			Reason    string          `json:"reason"`
			Changes   []rawFileChange `json:"changes,omitempty"`
		}
		_ = json.Unmarshal(msg.Params, &p)

		toolID := p.ItemID
		if toolID == "" {
			toolID = fmt.Sprintf("codex-%s", msg.RawID())
		}

		summary := "apply file changes"
		if p.GrantRoot != "" {
			summary += ": " + p.GrantRoot
		}
		if p.Reason != "" {
			summary += " (" + p.Reason + ")"
		}

		var changes []rawFileChange
		if len(p.Changes) > 0 {
			changes = p.Changes
		} else if p.ItemID != "" {
			h.mu.Lock()
			if chs, ok := h.itemChanges[p.ItemID]; ok {
				changes = chs
			}
			h.mu.Unlock()
		}

		var diffBuilder strings.Builder
		for _, ch := range changes {
			d := strings.TrimSpace(ch.Diff)
			if d == "" {
				continue
			}
			cleanPath := filepath.Clean(ch.Path)
			cleanPath = strings.TrimPrefix(cleanPath, "/")
			if cleanPath == "" {
				cleanPath = ch.Path
			}
			hasOldHeader := strings.Contains(d, "--- ")
			hasNewHeader := strings.Contains(d, "+++ ")
			if !hasOldHeader || !hasNewHeader {
				diffBuilder.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n%s\n", cleanPath, cleanPath, cleanPath, cleanPath, d))
			} else {
				if !strings.HasPrefix(d, "diff --git ") {
					diffBuilder.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", cleanPath, cleanPath))
				}
				diffBuilder.WriteString(d)
				diffBuilder.WriteString("\n")
			}
		}

		req := harness.ApprovalRequest{
			ToolID:   toolID,
			ToolName: "FileChange",
			Input:    msg.Params,
			Summary:  summary,
			Diff:     diffBuilder.String(),
		}
		return h.handleApprovalRequest(ctx, msg, req)

	case "applyPatchApproval":
		var p struct {
			CallID    string          `json:"callId"`
			GrantRoot string          `json:"grantRoot"`
			Reason    string          `json:"reason"`
			Changes   []rawFileChange `json:"changes,omitempty"`
		}
		_ = json.Unmarshal(msg.Params, &p)

		toolID := p.CallID
		if toolID == "" {
			toolID = fmt.Sprintf("codex-%s", msg.RawID())
		}
		summary := "apply patch"
		if p.GrantRoot != "" {
			summary += ": " + p.GrantRoot
		}

		var changes []rawFileChange
		if len(p.Changes) > 0 {
			changes = p.Changes
		} else if p.CallID != "" {
			h.mu.Lock()
			if chs, ok := h.itemChanges[p.CallID]; ok {
				changes = chs
			}
			h.mu.Unlock()
		}

		var diffBuilder strings.Builder
		for _, ch := range changes {
			d := strings.TrimSpace(ch.Diff)
			if d == "" {
				continue
			}
			cleanPath := filepath.Clean(ch.Path)
			cleanPath = strings.TrimPrefix(cleanPath, "/")
			if cleanPath == "" {
				cleanPath = ch.Path
			}
			hasOldHeader := strings.Contains(d, "--- ")
			hasNewHeader := strings.Contains(d, "+++ ")
			if !hasOldHeader || !hasNewHeader {
				diffBuilder.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n%s\n", cleanPath, cleanPath, cleanPath, cleanPath, d))
			} else {
				if !strings.HasPrefix(d, "diff --git ") {
					diffBuilder.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", cleanPath, cleanPath))
				}
				diffBuilder.WriteString(d)
				diffBuilder.WriteString("\n")
			}
		}

		req := harness.ApprovalRequest{
			ToolID:   toolID,
			ToolName: "applyPatch",
			Input:    msg.Params,
			Summary:  summary,
			Diff:     diffBuilder.String(),
		}
		return h.handleApprovalRequest(ctx, msg, req)

	case "execCommandApproval":
		var p struct {
			CallID  string   `json:"callId"`
			Command []string `json:"command"`
			Reason  string   `json:"reason"`
		}
		_ = json.Unmarshal(msg.Params, &p)

		toolID := p.CallID
		if toolID == "" {
			toolID = fmt.Sprintf("codex-%s", msg.RawID())
		}
		summary := "$ " + strings.Join(p.Command, " ")

		req := harness.ApprovalRequest{
			ToolID:   toolID,
			ToolName: "Bash",
			Input:    msg.Params,
			Summary:  summary,
		}
		return h.handleApprovalRequest(ctx, msg, req)

	case "item/permissions/requestApproval":
		// 本轮不实现权限提升交互，直接回空权限档拒绝
		return &JSONRPCMessage{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Result:  approvalDeclineResult(msg.Method),
		}, nil

	case "item/tool/requestUserInput":
		return h.handleRequestUserInput(ctx, msg)

	case "mcpServer/elicitation/request":
		// 本轮不接 MCP elicitation 交互：回 decline，让 codex 继续本轮而不是报 request 失败。
		// 响应结构（McpServerElicitationRequestResponse）只有 action 必填，decline 不带 content。
		resBody, _ := json.Marshal(map[string]any{"action": "decline"})
		return &JSONRPCMessage{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Result:  resBody,
		}, nil

	default:
		// 遇到未知请求，为避免 CLI hang，回写 JSON-RPC Method not found 错误
		return &JSONRPCMessage{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Error: &JSONRPCError{
				Code:    -32601,
				Message: fmt.Sprintf("method not found: %s", msg.Method),
			},
		}, nil
	}
}

func (h *ProtocolHandler) handleApprovalRequest(ctx context.Context, msg *JSONRPCMessage, req harness.ApprovalRequest) (*JSONRPCMessage, error) {
	approved, approveErr := h.decideApproval(ctx, req)
	if approveErr != nil {
		harness.AckDelivery(h.turn.Approver, req, approveErr)
	}

	var resBody json.RawMessage
	if approved {
		resBody, _ = json.Marshal(map[string]any{"decision": "accept"})
	} else {
		// 拒绝应答的结构按方法而异（见 approvalDeclineResult）
		resBody = approvalDeclineResult(msg.Method)
	}
	reply := &JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      msg.ID,
		Result:  resBody,
	}
	if approveErr == nil && h.turn.Approver != nil {
		reply.onAck = func(sendErr error) {
			harness.AckDelivery(h.turn.Approver, req, sendErr)
		}
	}
	return reply, nil
}

func (h *ProtocolHandler) decideApproval(ctx context.Context, req harness.ApprovalRequest) (bool, error) {
	if h.turn.Approver != nil {
		dec, err := h.turn.Approver.Approve(ctx, req)
		if err != nil {
			return false, err
		}
		return dec == harness.DecisionAllow, nil
	}

	// Approver 为空时按权限模式自动决定：fullAccess 同意，其他拒绝
	return h.turn.Mode == harness.ModeFullAccess, nil
}

// approvalDeclineResult 按请求方法返回 schema 规定的「拒绝」应答体，
// 供没有审批方（无活跃轮次/无 Approver）时兜底，避免请求因响应结构不合法而在 codex 侧报错。
// 各方法虽然都是审批，但响应结构不同：
//   - item/commandExecution/requestApproval、item/fileChange/requestApproval 的 decision
//     是字符串枚举 accept/acceptForSession/decline/cancel（CommandExecution/FileChangeApprovalDecision）；
//   - applyPatchApproval、execCommandApproval 的 decision 是 ReviewDecision，拒绝只能写成
//     {"denied":{"rejection":...}} 这个 tagged enum 形式；
//   - item/permissions/requestApproval 用空权限档表示拒绝（GrantedPermissionProfile 无必填字段），scope 缺省为 turn。
func approvalDeclineResult(method string) json.RawMessage {
	switch method {
	case "applyPatchApproval", "execCommandApproval":
		body, _ := json.Marshal(map[string]any{
			"decision": map[string]any{
				"denied": map[string]any{"rejection": "用户拒绝了该请求"},
			},
		})
		return body
	case "item/permissions/requestApproval":
		body, _ := json.Marshal(map[string]any{
			"permissions": map[string]any{},
			"scope":       "turn",
		})
		return body
	default:
		body, _ := json.Marshal(map[string]any{"decision": "decline"})
		return body
	}
}

// rawUserInputQuestion 对应 schema 中的 ToolRequestUserInputQuestion。
type rawUserInputQuestion struct {
	ID       string `json:"id"`
	Header   string `json:"header"`
	Question string `json:"question"`
	IsOther  bool   `json:"isOther"`
	IsSecret bool   `json:"isSecret"`
	Options  []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"options"`
}

// userInputAnswer 对应 schema 中的 ToolRequestUserInputAnswer：一道题的答案恒为字符串数组。
type userInputAnswer struct {
	Answers []string `json:"answers"`
}

// handleRequestUserInput 处理 item/tool/requestUserInput：转成提问类审批请求交给 Interactor，
// 再按 ToolRequestUserInputResponse 回写 answers。
// 没有 Interactor、用户跳过或交互出错时回空 answers（schema 要求 answers 必填，空对象即「无答案」）。
func (h *ProtocolHandler) handleRequestUserInput(ctx context.Context, msg *JSONRPCMessage) (*JSONRPCMessage, error) {
	var p struct {
		ItemID     string                 `json:"itemId"`
		IsBlocking bool                   `json:"isBlocking"`
		Questions  []rawUserInputQuestion `json:"questions"`
	}
	_ = json.Unmarshal(msg.Params, &p)

	toolID := p.ItemID
	if toolID == "" {
		toolID = fmt.Sprintf("codex-%s", msg.RawID())
	}

	req := harness.ApprovalRequest{
		ToolID:    toolID,
		ToolName:  "requestUserInput",
		Input:     msg.Params,
		Kind:      harness.RequestKindQuestion,
		Summary:   requestUserInputSummary(p.Questions),
		Questions: userInputQuestions(p.Questions),
	}

	answers := make(map[string]userInputAnswer, len(p.Questions))
	var interactErr error
	interacted := false
	if interactor, ok := h.turn.Approver.(harness.Interactor); ok && interactor != nil {
		interacted = true
		var resp harness.ApprovalResponse
		resp, interactErr = interactor.Interact(ctx, req)
		if interactErr == nil && !resp.Skipped && resp.Decision != harness.DecisionDeny {
			answers = userInputAnswers(p.Questions, resp.Answers)
		}
	}

	resBody, _ := json.Marshal(struct {
		Answers map[string]userInputAnswer `json:"answers"`
	}{Answers: answers})

	reply := &JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      msg.ID,
		Result:  resBody,
	}
	if interactErr != nil {
		harness.AckDelivery(h.turn.Approver, req, interactErr)
	} else if interacted {
		// 只有确实交给用户作答时才回报投递结果；没有 Interactor 时无交互可回报。
		reply.onAck = func(sendErr error) {
			harness.AckDelivery(h.turn.Approver, req, sendErr)
		}
	}
	return reply, nil
}

// userInputQuestions 把 schema 的题目列表转成 harness 题目。
// schema 里没有多选字段，AllowsMultiple 固定为 false；isOther 表示允许用户自己填写答案。
func userInputQuestions(questions []rawUserInputQuestion) []harness.Question {
	out := make([]harness.Question, 0, len(questions))
	for _, q := range questions {
		choices := make([]harness.QuestionChoice, 0, len(q.Options))
		for _, o := range q.Options {
			choices = append(choices, harness.QuestionChoice{Label: o.Label, Detail: o.Description})
		}
		out = append(out, harness.Question{
			ID:          q.ID,
			Header:      q.Header,
			Prompt:      q.Question,
			Choices:     choices,
			AllowsOther: q.IsOther,
			IsSecret:    q.IsSecret,
		})
	}
	return out
}

// userInputAnswers 只收集题目列表里出现过的题目的答案，未知 ID 的答案不写回。
func userInputAnswers(questions []rawUserInputQuestion, answers map[string][]string) map[string]userInputAnswer {
	out := make(map[string]userInputAnswer, len(answers))
	for _, q := range questions {
		values := answers[q.ID]
		if len(values) == 0 {
			continue
		}
		out[q.ID] = userInputAnswer{Answers: values}
	}
	return out
}

// requestUserInputSummary 用第一道题的正文做摘要，多题时补上题数。
func requestUserInputSummary(questions []rawUserInputQuestion) string {
	if len(questions) == 0 {
		return "请求补充信息"
	}
	summary := questions[0].Question
	if summary == "" {
		summary = questions[0].Header
	}
	if len(questions) > 1 {
		summary += fmt.Sprintf("（共 %d 个问题）", len(questions))
	}
	return summary
}

// UserInputItem 表示向 Codex 模型输入的文本或媒体项。
type UserInputItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Path string `json:"path,omitempty"`
}

// TurnSteerParams 对应 Codex app-server turn/steer 请求入参。
type TurnSteerParams struct {
	ThreadID       string          `json:"threadId"`
	ExpectedTurnID string          `json:"expectedTurnId"`
	Input          []UserInputItem `json:"input"`
}
