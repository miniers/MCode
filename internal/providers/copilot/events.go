package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"mcode/internal/harness"
)

// hiddenTools 是永远不产生工具行的工具：提问、计划、待办与意图上报各有自己的呈现面。
var hiddenTools = map[string]bool{
	"ask_user":       true,
	"exit_plan_mode": true,
	"update_todo":    true,
	"task_complete":  true,
	"report_intent":  true,
}

// eventHandler 维护单轮对话的流式增量状态、事件映射与审批逻辑。
type eventHandler struct {
	proc *sessionProc
	turn harness.Turn
	emit func(harness.Event)
	ctx  context.Context

	mu                 sync.Mutex
	turnActive         bool
	turnClosed         bool
	finished           bool
	turnError          string
	interruptRequested bool
	turnDone           chan struct{}

	// streamedText 记录已流式下发的助手文本，用于 assistant.message 只补发后缀。
	streamedText map[string]string

	toolNames   map[string]string
	shownReason map[string]bool

	usage harness.Usage
	// requestClock 为本次模型请求计时；requestSpeeds 是本轮已完成的请求速度样本。
	requestClock  harness.RequestClock
	requestSpeeds []harness.RequestSpeed

	// now 返回当前时间；nil 时用 time.Now，测试可注入假时钟。
	now func() time.Time
}

func newEventHandler(proc *sessionProc, turn harness.Turn, emit func(harness.Event), ctx context.Context) *eventHandler {
	return &eventHandler{
		proc:         proc,
		turn:         turn,
		emit:         emit,
		ctx:          ctx,
		turnDone:     make(chan struct{}),
		streamedText: make(map[string]string),
		toolNames:    make(map[string]string),
		shownReason:  make(map[string]bool),
	}
}

// turnID 返回本轮 harness 轮次标识，供 ForceStopTurn 匹配。
func (h *eventHandler) turnID() string { return h.turn.ID }

// turnDoneCh 返回本轮结束信号通道。
func (h *eventHandler) turnDoneCh() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.turnDone
}

func (h *eventHandler) isTurnActive() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.turnActive
}

// turnFinishedOK 报告本轮是否以 session.idle 正常收敛（而非连接意外断开）。
func (h *eventHandler) turnFinishedOK() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.finished
}

// beginTurn 重置轮次状态并开启新的结束通道。
func (h *eventHandler) beginTurn() {
	h.mu.Lock()
	h.turnActive = true
	h.turnClosed = false
	h.finished = false
	h.turnError = ""
	h.interruptRequested = false
	h.turnDone = make(chan struct{})
	h.streamedText = make(map[string]string)
	h.toolNames = make(map[string]string)
	h.shownReason = make(map[string]bool)
	h.usage = harness.Usage{}
	h.requestClock.Reset()
	h.requestSpeeds = nil
	h.mu.Unlock()
}

// settle 关闭本轮结束通道（幂等）。
func (h *eventHandler) settle() {
	h.mu.Lock()
	ch := h.turnDone
	closed := h.turnClosed
	h.turnClosed = true
	h.mu.Unlock()
	if ch != nil && !closed {
		close(ch)
	}
}

func (h *eventHandler) emitEvent(e harness.Event) {
	_ = harness.Emit(h.emit, e)
}

// nowTime 返回当前时间；测试可注入假时钟。
func (h *eventHandler) nowTime() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// beginRequest 在本次请求的第一个内容帧到达时开始计时。
func (h *eventHandler) beginRequest() {
	h.mu.Lock()
	h.requestClock.Begin(h.nowTime())
	h.mu.Unlock()
}

// requestSpeedSamples 返回本轮样本的拷贝，避免事件里带上内部切片。
func requestSpeedSamples(samples []harness.RequestSpeed) []harness.RequestSpeed {
	if len(samples) == 0 {
		return nil
	}
	return append([]harness.RequestSpeed(nil), samples...)
}

// abortByClose 在常驻连接意外断开时收敛本轮：报错并结束。
func (h *eventHandler) abortByClose(err error) {
	h.mu.Lock()
	if !h.turnActive {
		h.mu.Unlock()
		return
	}
	h.turnActive = false
	h.mu.Unlock()

	if err != nil && err != ErrConnClosed {
		h.emitEvent(harness.Event{Type: harness.EventError, Err: err.Error()})
	}
	h.emitEvent(harness.Event{Type: harness.EventDone, StopReason: "failed"})
	h.settle()
}

// handleEvent 处理一条 `session.event` 通知并映射为 harness.Event。
func (h *eventHandler) handleEvent(params json.RawMessage) {
	var p struct {
		SessionID string `json:"sessionId"`
		Event     struct {
			Type    string          `json:"type"`
			AgentID string          `json:"agentId"`
			Data    json.RawMessage `json:"data"`
		} `json:"event"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	if p.Event.Type == "" {
		return
	}
	if current := h.proc.currentSessionID(); current != "" && p.SessionID != "" && p.SessionID != current {
		return
	}
	// Swarm 的子代理（bee）事件尚未实现：带 agentId 的事件一律忽略。
	if p.Event.AgentID != "" {
		return
	}

	data := p.Event.Data
	switch p.Event.Type {
	case "assistant.turn_start":
		h.mu.Lock()
		h.turnActive = true
		h.mu.Unlock()

	case "assistant.message_delta":
		var d struct {
			MessageID    string `json:"messageId"`
			DeltaContent string `json:"deltaContent"`
		}
		if err := json.Unmarshal(data, &d); err != nil || d.DeltaContent == "" {
			return
		}
		h.beginRequest()
		h.mu.Lock()
		if d.MessageID != "" {
			h.streamedText[d.MessageID] += d.DeltaContent
		}
		h.mu.Unlock()
		h.emitEvent(harness.Event{Type: harness.EventText, Text: d.DeltaContent})

	case "assistant.message":
		h.handleMessage(data)

	case "assistant.reasoning_delta":
		var d struct {
			ReasoningID  string `json:"reasoningId"`
			DeltaContent string `json:"deltaContent"`
		}
		if err := json.Unmarshal(data, &d); err != nil || d.DeltaContent == "" {
			return
		}
		h.beginRequest()
		h.emitEvent(harness.Event{Type: harness.EventReasoning, Text: d.DeltaContent})

	case "assistant.reasoning":
		var d struct {
			ReasoningID string `json:"reasoningId"`
			Content     string `json:"content"`
		}
		if err := json.Unmarshal(data, &d); err != nil || d.Content == "" {
			return
		}
		h.mu.Lock()
		h.shownReason[d.Content] = true
		h.mu.Unlock()
		h.emitEvent(harness.Event{Type: harness.EventReasoning, Text: d.Content})

	case "assistant.usage":
		h.handleUsage(data)

	case "session.usage_info":
		// 这是上下文窗口占用（currentTokens/tokenLimit），不是本轮用量；harness.Usage 没有
		// 对应字段，且消费方按轮次取最后一条 usage，混进来会覆盖 assistant.usage 的累计值。

	case "tool.execution_start":
		h.handleToolStart(data)

	case "tool.execution_complete":
		h.handleToolComplete(data)

	case "permission.requested":
		// 审批可能等待用户，放到独立 goroutine，避免占住读循环。
		go h.handlePermission(data)

	case "permission.completed":
		// 请求已了结，harness 无对应事件。

	case "session.mode_changed", "session.title_changed", "session.compaction_complete", "tool.execution_partial_result", "tool.execution_progress":
		// harness 事件面没有对应类型（模式、标题、压缩提示、工具增量输出）。

	case "session.error":
		msg := errorMessage(data)
		if msg == "" {
			return
		}
		h.mu.Lock()
		active := h.turnActive
		if active {
			h.turnError = msg
		}
		h.mu.Unlock()
		if !active {
			h.emitEvent(harness.Event{Type: harness.EventError, Err: msg})
		}

	case "session.warning":
		if msg := jsonString(data, "message"); msg != "" {
			h.emitEvent(harness.Event{Type: harness.EventError, Err: msg})
		}

	case "session.shutdown":
		if jsonString(data, "shutdownType") == "error" {
			if reason := jsonString(data, "errorReason"); reason != "" {
				h.emitEvent(harness.Event{Type: harness.EventError, Err: reason})
			}
		}

	case "session.idle":
		h.finishTurn(jsonBool(data, "aborted"))
	}
}

// handleMessage 处理 assistant.message：补发未流式下发的文本、推理文本，并登记其中的工具调用。
func (h *eventHandler) handleMessage(data json.RawMessage) {
	var d struct {
		MessageID     string `json:"messageId"`
		Content       string `json:"content"`
		ReasoningText string `json:"reasoningText"`
		ToolRequests  []struct {
			ToolCallID    string          `json:"toolCallId"`
			Name          string          `json:"name"`
			Arguments     json.RawMessage `json:"arguments"`
			MCPServerName string          `json:"mcpServerName"`
			MCPToolName   string          `json:"mcpToolName"`
		} `json:"toolRequests"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return
	}
	if strings.TrimSpace(d.ReasoningText) != "" {
		h.mu.Lock()
		shown := h.shownReason[d.ReasoningText]
		if !shown {
			h.shownReason[d.ReasoningText] = true
		}
		h.mu.Unlock()
		if !shown {
			h.emitEvent(harness.Event{Type: harness.EventReasoning, Text: d.ReasoningText})
		}
	}

	if d.Content != "" {
		h.mu.Lock()
		streamed := h.streamedText[d.MessageID]
		h.mu.Unlock()
		if streamed == "" {
			h.emitEvent(harness.Event{Type: harness.EventText, Text: d.Content})
		} else if suffix, ok := strings.CutPrefix(d.Content, streamed); ok {
			if suffix != "" {
				h.emitEvent(harness.Event{Type: harness.EventText, Text: suffix})
			}
		} else {
			h.emitEvent(harness.Event{Type: harness.EventText, Text: d.Content})
		}
	}

	for _, req := range d.ToolRequests {
		h.toolRequested(req.ToolCallID, req.Name, req.Arguments, req.MCPServerName, req.MCPToolName)
	}
}

// handleToolStart 处理 tool.execution_start。
func (h *eventHandler) handleToolStart(data json.RawMessage) {
	var d struct {
		ToolCallID    string          `json:"toolCallId"`
		ToolName      string          `json:"toolName"`
		Arguments     json.RawMessage `json:"arguments"`
		MCPServerName string          `json:"mcpServerName"`
		MCPToolName   string          `json:"mcpToolName"`
	}
	if err := json.Unmarshal(data, &d); err != nil || d.ToolCallID == "" || d.ToolName == "" {
		return
	}
	h.toolRequested(d.ToolCallID, d.ToolName, d.Arguments, d.MCPServerName, d.MCPToolName)
}

// toolRequested 登记并派发一个工具调用开始事件（同一 call id 只派发一次）。
func (h *eventHandler) toolRequested(id, name string, arguments json.RawMessage, server, mcpTool string) {
	if id == "" || name == "" {
		return
	}
	h.mu.Lock()
	if _, ok := h.toolNames[id]; ok {
		h.mu.Unlock()
		return
	}
	h.toolNames[id] = name
	h.mu.Unlock()

	if hiddenTools[name] {
		return
	}

	h.emitEvent(harness.Event{
		Type:     harness.EventToolStart,
		ToolID:   id,
		ToolName: toolDisplayName(name, server, mcpTool),
		Input:    arguments,
	})
}

// handleToolComplete 处理 tool.execution_complete。
func (h *eventHandler) handleToolComplete(data json.RawMessage) {
	var d struct {
		ToolCallID string          `json:"toolCallId"`
		Success    *bool           `json:"success"`
		Error      json.RawMessage `json:"error"`
		Result     json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &d); err != nil || d.ToolCallID == "" {
		return
	}

	h.mu.Lock()
	name := h.toolNames[d.ToolCallID]
	h.mu.Unlock()
	if hiddenTools[name] {
		return
	}

	success := true
	if d.Success != nil {
		success = *d.Success
	}

	output := ""
	if !success {
		output = jsonString(d.Error, "message")
	} else {
		output = jsonString(d.Result, "detailedContent")
		if output == "" {
			output = jsonString(d.Result, "content")
		}
	}

	h.emitEvent(harness.Event{
		Type:    harness.EventToolEnd,
		ToolID:  d.ToolCallID,
		Output:  output,
		IsError: !success,
	})
}

// handleUsage 累计 assistant.usage 的令牌用量并派发 usage 事件。
// 键名兼容多种候选写法。
func (h *eventHandler) handleUsage(data json.RawMessage) {
	in, _ := usageNumber(data, "inputTokens", "input_tokens", "promptTokens", "prompt_tokens")
	out, _ := usageNumber(data, "outputTokens", "output_tokens", "completionTokens", "completion_tokens")
	cached, _ := usageNumber(data, "cachedTokens", "cached_tokens", "cachedInputTokens", "cacheReadTokens")
	if in == 0 && out == 0 && cached == 0 {
		// 没有可用用量：本次请求不算样本，但要清掉计时，避免影响下一次请求。
		h.mu.Lock()
		h.requestClock.Reset()
		h.mu.Unlock()
		return
	}

	h.mu.Lock()
	h.usage.InputTokens += int64(in)
	h.usage.OutputTokens += int64(out)
	h.usage.CachedTokens += int64(cached)
	if speed, ok := h.requestClock.Finish(h.nowTime(), int64(out)); ok {
		h.requestSpeeds = append(h.requestSpeeds, speed)
	}
	cur := h.usage
	speeds := requestSpeedSamples(h.requestSpeeds)
	h.mu.Unlock()

	h.emitEvent(harness.Event{
		Type: harness.EventUsage,
		Usage: &harness.Usage{
			InputTokens:  cur.InputTokens,
			OutputTokens: cur.OutputTokens,
			CachedTokens: cur.CachedTokens,
			Requests:     speeds,
		},
	})
}

// finishTurn 收敛一轮。
func (h *eventHandler) finishTurn(aborted bool) {
	h.mu.Lock()
	if !h.turnActive {
		h.mu.Unlock()
		return
	}
	h.turnActive = false
	h.finished = true
	errMsg := h.turnError
	interrupted := aborted || h.interruptRequested
	h.turnError = ""
	h.streamedText = make(map[string]string)
	h.toolNames = make(map[string]string)
	h.shownReason = make(map[string]bool)
	h.mu.Unlock()

	stopReason := "completed"
	if interrupted {
		stopReason = "interrupted"
	} else if errMsg != "" {
		stopReason = "failed"
		h.emitEvent(harness.Event{Type: harness.EventError, Err: errMsg})
	}

	h.emitEvent(harness.Event{Type: harness.EventDone, StopReason: stopReason})
	h.settle()
}

// handlePermission 处理 permission.requested：先按模式自动裁决，否则交给 Approver，
// 再通过 session.permissions.handlePendingPermissionRequest 回写决定。
func (h *eventHandler) handlePermission(data json.RawMessage) {
	var p struct {
		RequestID         string          `json:"requestId"`
		ResolvedByHook    bool            `json:"resolvedByHook"`
		PermissionRequest json.RawMessage `json:"permissionRequest"`
		PromptRequest     json.RawMessage `json:"promptRequest"`
	}
	if err := json.Unmarshal(data, &p); err != nil || p.RequestID == "" || len(p.PermissionRequest) == 0 {
		return
	}
	if p.ResolvedByHook {
		return
	}

	if decision := h.automaticDecision(p.PermissionRequest, p.PromptRequest); decision != nil {
		_ = h.respondPermission(p.RequestID, decision)
		return
	}

	req := h.describePermission(p.PermissionRequest)
	req.ToolID = firstNonEmpty(jsonString(p.PermissionRequest, "toolCallId"), p.RequestID)

	allow := false
	if h.turn.Approver != nil {
		dec, err := h.turn.Approver.Approve(h.ctx, req)
		if err != nil {
			harness.AckDelivery(h.turn.Approver, req, err)
		} else {
			allow = dec == harness.DecisionAllow
		}
	}

	var decision map[string]any
	if allow {
		decision = map[string]any{"kind": "approve-once", "approvedInteractively": true}
	} else {
		decision = map[string]any{"kind": "reject", "feedback": "The user declined this action."}
	}
	writeErr := h.respondPermission(p.RequestID, decision)
	if h.turn.Approver != nil {
		harness.AckDelivery(h.turn.Approver, req, writeErr)
	}
}

// automaticDecision 返回线程模式无需询问即可给出的决定，需要询问时返回 nil。
func (h *eventHandler) automaticDecision(request, prompt json.RawMessage) map[string]any {
	kind := jsonString(request, "kind")
	approve := map[string]any{"kind": "approve-once"}

	switch h.turn.Mode {
	case harness.ModeFullAccess:
		return approve
	case harness.ModeAutoAcceptEdits, harness.ModeAuto:
		if kind == "write" || kind == "read" {
			return approve
		}
		recommendation := jsonString(jsonRaw(prompt, "assistedApproval"), "recommendation")
		if recommendation == "" {
			recommendation = jsonString(jsonRaw(request, "assistedApproval"), "recommendation")
		}
		if recommendation == "approve" {
			return approve
		}
	}
	return nil
}

// describePermission 把 CLI 的权限请求映射为 harness.ApprovalRequest。
func (h *eventHandler) describePermission(request json.RawMessage) harness.ApprovalRequest {
	intention := jsonString(request, "intention")
	kind := jsonString(request, "kind")
	toolID := jsonString(request, "toolCallId")

	req := harness.ApprovalRequest{
		ToolID:   toolID,
		ToolName: kind,
		Input:    request,
		Kind:     harness.RequestKindTool,
	}

	switch kind {
	case "shell":
		req.Kind = harness.RequestKindCommand
		req.ToolName = "bash"
		command := jsonString(request, "fullCommandText")
		if command == "" {
			command = "Command"
		}
		var details []string
		if dir := jsonString(request, "resolvedWorkingDirectory"); dir != "" && dir != h.proc.workDir {
			details = append(details, dir)
		}
		if jsonBool(request, "requestSandboxBypass") {
			reason := jsonString(request, "requestSandboxBypassReason")
			if reason == "" {
				reason = "Runs outside the sandbox."
			}
			details = append(details, reason)
		}
		req.Summary = command
		if len(details) > 0 {
			req.Summary += " · " + strings.Join(details, " · ")
		}
	case "write":
		req.Kind = harness.RequestKindFileChange
		req.ToolName = "write"
		path := firstNonEmpty(jsonString(request, "resolvedPath"), jsonString(request, "fileName"), "file")
		req.Summary = path
	case "read":
		req.Kind = harness.RequestKindPermissions
		req.ToolName = "read"
		path := firstNonEmpty(jsonString(request, "resolvedPath"), jsonString(request, "path"), "file")
		req.Summary = "Read " + path
	case "url":
		req.Kind = harness.RequestKindPermissions
		req.ToolName = "url"
		req.Summary = "Fetch " + firstNonEmpty(jsonString(request, "url"), "a URL")
	case "mcp":
		req.Kind = harness.RequestKindTool
		server := firstNonEmpty(jsonString(request, "serverName"), "MCP")
		tool := firstNonEmpty(jsonString(request, "toolTitle"), jsonString(request, "toolName"), "tool")
		req.ToolName = server + " · " + tool
		req.Summary = req.ToolName
	case "memory":
		req.Kind = harness.RequestKindTool
		req.ToolName = "memory"
		req.Summary = "Remember: " + firstNonEmpty(jsonString(request, "fact"), "something about this project")
	case "custom-tool":
		req.Kind = harness.RequestKindTool
		req.ToolName = firstNonEmpty(jsonString(request, "toolName"), "Tool")
		req.Summary = req.ToolName
	default:
		req.Kind = harness.RequestKindTool
		if req.ToolName == "" {
			req.ToolName = "action"
		}
		req.Summary = humanize(req.ToolName)
	}

	if intention != "" {
		if req.Summary == "" {
			req.Summary = intention
		} else {
			req.Summary += " (" + intention + ")"
		}
	}
	return req
}

// respondPermission 通过 session.permissions.handlePendingPermissionRequest 回写审批决定。
func (h *eventHandler) respondPermission(requestID string, decision map[string]any) error {
	sessionID := h.proc.currentSessionID()
	if sessionID == "" {
		return harness.ErrNotRunning
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return h.proc.call(ctx, "session.permissions.handlePendingPermissionRequest", map[string]any{
		"sessionId": sessionID,
		"requestId": requestID,
		"result":    decision,
	}, nil)
}

// handleServerRequest 原地答复服务端请求：提问（ask_user）与计划退出（exit_plan_mode）走 Interactor。
func (h *eventHandler) handleServerRequest(method string, params json.RawMessage) (any, error) {
	switch method {
	case "userInput.request":
		return h.answerUserInput(params), nil
	case "exitPlanMode.request":
		return h.answerExitPlanMode(params), nil
	case "autoModeSwitch.request":
		// 限流曾提议切到 Auto 模型档：保持用户已选的模型。
		return map[string]any{"response": "no"}, nil
	default:
		return nil, &RPCError{Code: -32601, Message: fmt.Sprintf("mcode does not support %s.", method)}
	}
}

// answerUserInput 用 Interactor 回答 ask_user 的提问。
func (h *eventHandler) answerUserInput(params json.RawMessage) map[string]any {
	var p struct {
		Question      string   `json:"question"`
		Choices       []string `json:"choices"`
		AllowFreeform *bool    `json:"allowFreeform"`
	}
	_ = json.Unmarshal(params, &p)

	allowsOther := true
	if p.AllowFreeform != nil {
		allowsOther = *p.AllowFreeform
	}

	choices := make([]harness.QuestionChoice, 0, len(p.Choices))
	for _, c := range p.Choices {
		choices = append(choices, harness.QuestionChoice{Label: c})
	}

	answer := ""
	if interactor, ok := h.turn.Approver.(harness.Interactor); ok && interactor != nil {
		resp, err := interactor.Interact(h.ctx, harness.ApprovalRequest{
			ToolID:   "ask_user",
			ToolName: "ask_user",
			Kind:     harness.RequestKindQuestion,
			Summary:  p.Question,
			Plan:     "",
			Questions: []harness.Question{{
				ID:          "answer",
				Prompt:      p.Question,
				Choices:     choices,
				AllowsOther: allowsOther,
			}},
		})
		if err == nil {
			if vals, ok := resp.Answers["answer"]; ok && len(vals) > 0 {
				answer = strings.Join(vals, ", ")
			}
		}
	}

	wasFreeform := true
	for _, c := range p.Choices {
		if c == answer {
			wasFreeform = false
			break
		}
	}
	return map[string]any{"answer": answer, "wasFreeform": wasFreeform}
}

// answerExitPlanMode 用 Interactor 裁决计划退出。
func (h *eventHandler) answerExitPlanMode(params json.RawMessage) map[string]any {
	var p struct {
		PlanContent string `json:"planContent"`
		Summary     string `json:"summary"`
	}
	_ = json.Unmarshal(params, &p)

	plan := p.PlanContent
	if plan == "" {
		plan = p.Summary
	}

	approved := false
	feedback := "The user is not ready to leave planning yet. Make no changes until they reply."
	if interactor, ok := h.turn.Approver.(harness.Interactor); ok && interactor != nil {
		resp, err := interactor.Interact(h.ctx, harness.ApprovalRequest{
			ToolID:   "exit_plan_mode",
			ToolName: "exit_plan_mode",
			Kind:     harness.RequestKindPlan,
			Summary:  "Implement this plan",
			Plan:     plan,
		})
		if err == nil {
			approved = resp.Decision == harness.DecisionAllow
			if resp.Feedback != "" {
				feedback = resp.Feedback
			}
		}
	}

	if approved {
		// Plan 回合的权限是只读档（manual）：先切回用户所选档位，否则这一轮剩下的操作会逐个审批。
		if h.turn.PlanExitMode != "" && h.proc != nil {
			h.proc.setPermissionMode(h.ctx, permissionModeName(h.turn.PlanExitMode))
		}
		return map[string]any{"approved": true, "selectedAction": "interactive"}
	}
	return map[string]any{"approved": false, "feedback": feedback}
}

// toolDisplayName 拼接 MCP 工具显示名（server · tool）。
func toolDisplayName(name, server, mcpTool string) string {
	if server == "" {
		return name
	}
	tool := mcpTool
	if tool == "" {
		tool = name
	}
	return server + " · " + tool
}

// errorMessage 组装 CLI 的错误文案，并在账号本身是问题时附上解决办法。
func errorMessage(data json.RawMessage) string {
	message := jsonString(data, "message")
	if message == "" {
		message = "Copilot reported an error."
	}

	if idx := strings.Index(message, "GenericFailure, "); idx >= 0 {
		rest := strings.TrimSpace(message[idx+len("GenericFailure, "):])
		var parsed struct {
			Status int    `json:"status"`
			Body   string `json:"body"`
		}
		if err := json.Unmarshal([]byte(rest), &parsed); err == nil && parsed.Body != "" && parsed.Status != 0 {
			message = fmt.Sprintf("HTTP %d: %s", parsed.Status, strings.TrimSpace(parsed.Body))
		}
	}

	if jsonInt(data, "statusCode") == 403 || jsonString(data, "remediation") == "sign_in" ||
		strings.Contains(strings.ToLower(message), "not authorized to use this copilot feature") {
		message += " Make sure this GitHub account has an active Copilot subscription, or run `copilot login` again."
	}
	return message
}

// humanize 把工具名转成可读标题。
func humanize(name string) string {
	if name == "" {
		return ""
	}
	words := strings.ReplaceAll(strings.ReplaceAll(name, "-", " "), "_", " ")
	if words == "" {
		return ""
	}
	return strings.ToUpper(words[:1]) + words[1:]
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// decodeObject 把 JSON 对象解成 key -> 原始值；非法或非对象返回 nil。
func decodeObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// jsonRaw 取对象里的某个字段原始值；不存在返回 nil。
func jsonRaw(raw json.RawMessage, key string) json.RawMessage {
	return decodeObject(raw)[key]
}

func jsonString(raw json.RawMessage, key string) string {
	v, ok := decodeObject(raw)[key]
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(v, &s)
	return s
}

func jsonBool(raw json.RawMessage, key string) bool {
	v, ok := decodeObject(raw)[key]
	if !ok {
		return false
	}
	var b bool
	_ = json.Unmarshal(v, &b)
	return b
}

func jsonInt(raw json.RawMessage, key string) int {
	v, ok := decodeObject(raw)[key]
	if !ok {
		return 0
	}
	var n int
	_ = json.Unmarshal(v, &n)
	return n
}

// usageNumber 按候选键名取数值。
func usageNumber(raw json.RawMessage, keys ...string) (float64, bool) {
	obj := decodeObject(raw)
	for _, key := range keys {
		v, ok := obj[key]
		if !ok {
			continue
		}
		var n float64
		if err := json.Unmarshal(v, &n); err == nil {
			return n, true
		}
	}
	return 0, false
}
