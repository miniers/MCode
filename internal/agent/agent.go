package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"mcode/internal/agent/gate"
	"mcode/internal/agent/llm"
	"mcode/internal/agent/session"
	"mcode/internal/agent/tools"
	"mcode/internal/harness"
)

type Options struct {
	Client    llm.Client
	Model     string
	Effort    string
	System    string
	Tools     []tools.Tool
	Env       tools.Env
	Mode      harness.PermissionMode
	Plan      bool
	Approver  harness.Approver
	Gate      *gate.Session
	Store     *session.Store
	SessionID string
	MaxRounds int
	// ContextWindow 是模型上下文窗口的 token 数，为 0 时使用默认值。
	ContextWindow int
	Emit          func(harness.Event)
}

type messageEntry struct {
	Type    string      `json:"type"`
	Message llm.Message `json:"message"`
}

// Run 执行一次用户回合，并逐条保存消息。
func Run(ctx context.Context, opts Options, prompt string, images []llm.Part) (runErr error) {
	id := opts.SessionID
	if id == "" {
		id = session.NewID()
	}
	if opts.Gate == nil {
		opts.Gate = &gate.Session{}
	}
	if opts.MaxRounds <= 0 {
		opts.MaxRounds = 200
	}
	window := opts.ContextWindow
	if window <= 0 {
		window = defaultContextWindow
	}
	var messages []llm.Message
	// contextTokens/contextMeasured 记录最近一次用量对应的上下文大小和消息数量，之后的消息按字符估算。
	var contextTokens, contextMeasured int
	var pending []llm.ToolCall
	var usage harness.Usage
	var stopReason string
	emit := func(event harness.Event) {
		_ = harness.Emit(opts.Emit, event)
	}
	appendMessage := func(message llm.Message) error {
		if opts.Store != nil {
			if err := opts.Store.Append(id, messageEntry{Type: "message", Message: message}); err != nil {
				return err
			}
		}
		messages = append(messages, message)
		return nil
	}
	emit(harness.Event{Type: harness.EventSession, SessionID: id})
	defer func() {
		if ctx.Err() != nil {
			for _, call := range pending {
				err := appendMessage(toolMessage(call.ID, "Cancelled", true))
				if err != nil {
					emit(harness.Event{Type: harness.EventError, Err: err.Error()})
					runErr = errors.Join(ctx.Err(), err)
					break
				}
			}
			if runErr == nil {
				runErr = ctx.Err()
			}
		}
		emit(harness.Event{Type: harness.EventUsage, Usage: &usage})
		emit(harness.Event{Type: harness.EventDone, StopReason: stopReason})
	}()
	fail := func(err error) error {
		emit(harness.Event{Type: harness.EventError, Err: err.Error()})
		return err
	}
	if opts.Store != nil && opts.SessionID != "" && opts.Store.Exists(id) {
		entries, err := opts.Store.Load(id)
		if err != nil {
			return fail(err)
		}
		messages = historyFromEntries(entries)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	content := []llm.Part{{Type: "text", Text: prompt}}
	content = append(content, images...)
	if err := appendMessage(llm.Message{Role: "user", Content: content}); err != nil {
		return fail(err)
	}
	emit(harness.Event{Type: harness.EventTurnAccepted})
	if opts.Client == nil {
		return fail(errors.New("agent: llm client is required"))
	}
	compactNow := func() (*compactionResult, error) {
		result, err := compactMessages(ctx, opts.Client, opts.Model, messages, emit)
		if err != nil || result == nil {
			return nil, err
		}
		if opts.Store != nil {
			if err := opts.Store.Append(id, result.Entry); err != nil {
				return nil, err
			}
		}
		messages = result.Messages
		contextTokens = result.After
		contextMeasured = len(messages)
		return result, nil
	}
	request := llm.Request{Model: opts.Model, Effort: opts.Effort, System: opts.System}
	if opts.Plan {
		request.System += "\n\n" + gate.PlanModePrompt()
	}
	for _, tool := range opts.Tools {
		request.Tools = append(request.Tools, llm.ToolSpec{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters})
	}
	for round := 0; round < opts.MaxRounds; round++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if estimateContextTokens(messages, contextTokens, contextMeasured) > window-reserveTokens {
			// 压缩是为后续调用腾出空间，失败时保留原上下文继续，让模型调用自己给出结论。
			_, _ = compactNow()
		}
		request.Messages = append([]llm.Message(nil), messages...)
		var response *llm.Response
		var err error
		// 本轮内最多因上下文溢出压缩重试一次，再溢出就返回原始错误。
		overflowRetried := false
		for attempt := 0; ; {
			var streamed strings.Builder
			response, err = opts.Client.Stream(ctx, request, func(delta llm.Delta) {
				if delta.Text == "" {
					return
				}
				switch delta.Type {
				case "text":
					streamed.WriteString(delta.Text)
					emit(harness.Event{Type: harness.EventText, Text: delta.Text})
				case "reasoning":
					emit(harness.Event{Type: harness.EventReasoning, Text: delta.Text})
				}
			})
			if err == nil || ctx.Err() != nil {
				break
			}
			if llm.IsContextOverflow(err) && !overflowRetried {
				overflowRetried = true
				if streamed.Len() > 0 {
					emit(harness.Event{Type: harness.EventTextRetract, Text: streamed.String()})
				}
				result, compactErr := compactNow()
				if compactErr != nil || result == nil {
					break
				}
				request.Messages = append([]llm.Message(nil), messages...)
				continue
			}
			if attempt == 3 || !retryable(err) {
				break
			}
			if streamed.Len() > 0 {
				emit(harness.Event{Type: harness.EventTextRetract, Text: streamed.String()})
			}
			timer := time.NewTimer(time.Second << attempt)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			attempt++
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return fail(err)
		}
		if response == nil {
			return fail(errors.New("agent: empty llm response"))
		}
		stopReason = response.StopReason
		usage.InputTokens += int64(response.Usage.Input + response.Usage.CacheRead + response.Usage.CacheWrite)
		usage.OutputTokens += int64(response.Usage.Output)
		usage.CachedTokens += int64(response.Usage.CacheRead + response.Usage.CacheWrite)
		invalid := make(map[int]bool)
		for i := range response.Message.ToolCalls {
			if !json.Valid(response.Message.ToolCalls[i].Arguments) {
				invalid[i] = true
				response.Message.ToolCalls[i].Arguments = json.RawMessage(`{}`)
			}
		}
		if err := appendMessage(response.Message); err != nil {
			return fail(err)
		}
		contextTokens = response.Usage.Input + response.Usage.CacheRead + response.Usage.CacheWrite + response.Usage.Output
		if estimate := tokensForMessages(messages); estimate > contextTokens {
			contextTokens = estimate
		}
		contextMeasured = len(messages)
		pending = append([]llm.ToolCall(nil), response.Message.ToolCalls...)
		if stopReason != "tool_use" && !(stopReason == "length" && len(invalid) > 0) {
			pending = nil
			return nil
		}
		for i, call := range response.Message.ToolCalls {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			emit(harness.Event{Type: harness.EventToolStart, ToolID: call.ID, ToolName: call.Name, Input: call.Arguments})
			var result tools.Result
			if invalid[i] {
				result = tools.Result{Text: "Invalid or incomplete tool arguments", IsError: true}
			} else {
				result = runTool(ctx, opts, call)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := appendMessage(toolMessage(call.ID, result.Text, result.IsError)); err != nil {
				return fail(err)
			}
			pending = pending[1:]
			emit(harness.Event{Type: harness.EventToolEnd, ToolID: call.ID, ToolName: call.Name, Output: result.Text, IsError: result.IsError})
			if result.Change != nil {
				emit(harness.Event{Type: harness.EventFileChange, Path: result.Change.Path, Patch: result.Change.Patch})
			}
		}
	}
	err := fmt.Errorf("agent: maximum rounds reached (%d)", opts.MaxRounds)
	emit(harness.Event{Type: harness.EventError, Err: err.Error(), ErrKind: harness.ErrKindMaxTurns})
	return err
}

func toolMessage(id, text string, isError bool) llm.Message {
	return llm.Message{Role: "tool", ToolCallID: id, Content: []llm.Part{{Type: "text", Text: text}}, IsError: isError}
}

func retryable(err error) bool {
	var apiErr *llm.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

func runTool(ctx context.Context, opts Options, call llm.ToolCall) tools.Result {
	var tool *tools.Tool
	for i := range opts.Tools {
		if opts.Tools[i].Name == call.Name {
			tool = &opts.Tools[i]
			break
		}
	}
	if tool == nil {
		return tools.Result{Text: "Unknown tool: " + call.Name, IsError: true}
	}
	verdict := gate.Check(gate.Input{Mode: opts.Mode, Plan: opts.Plan, ToolName: call.Name, ToolID: call.ID, Args: call.Arguments, WorkDir: opts.Env.WorkDir, AdditionalDirs: opts.Env.AdditionalDirs})
	if verdict.Action == gate.Deny {
		return tools.Result{Text: verdict.Reason, IsError: true}
	}
	if verdict.Action == gate.Ask && !opts.Gate.Allowed(verdict.SessionKey) {
		response := harness.ApprovalResponse{Decision: harness.DecisionDeny}
		var err error
		if interactor, ok := opts.Approver.(harness.Interactor); ok && interactor != nil {
			response, err = interactor.Interact(ctx, verdict.Request)
		} else if opts.Approver != nil {
			response.Decision, err = opts.Approver.Approve(ctx, verdict.Request)
		}
		harness.AckDelivery(opts.Approver, verdict.Request, err)
		if err != nil {
			return tools.Result{Text: err.Error(), IsError: true}
		}
		for _, answers := range response.Answers {
			for _, answer := range answers {
				if strings.EqualFold(answer, "allow for session") {
					opts.Gate.Remember(verdict.SessionKey)
					response.Decision = harness.DecisionAllow
				}
			}
		}
		if response.Decision != harness.DecisionAllow {
			return tools.Result{Text: "User rejected the tool call", IsError: true}
		}
	}
	if tool.Run == nil {
		return tools.Result{Text: "Tool has no implementation: " + call.Name, IsError: true}
	}
	result, err := tool.Run(ctx, opts.Env, call.Arguments)
	if err != nil {
		return tools.Result{Text: err.Error(), IsError: true}
	}
	return result
}
