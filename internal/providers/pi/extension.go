package pi

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"mcode/internal/harness"
)

// gateTitlePrefix 是 pi 扩展在 select 标题里标记工具门禁的前缀。
const gateTitlePrefix = "mcode-gate "

// confirmMethod 是 pi 扩展的确认类提问，应答走 confirmed 字段而不是 value。
const confirmMethod = "confirm"

// gatePayload 是 mcode-gate 门禁标题中携带的工具调用信息。
type gatePayload struct {
	Kind       string          `json:"kind"`
	ToolName   string          `json:"toolName"`
	Input      json.RawMessage `json:"input"`
	ToolCallID string          `json:"toolCallId"`
	Sensitive  string          `json:"sensitive,omitempty"`
}

// readOnlyGateTools 是门禁扩展直接放行的工具（只看不写，与 mcode-gate.ts 的 readOnly 表一致）。
var readOnlyGateTools = map[string]bool{"read": true, "grep": true, "find": true, "ls": true, "mcode_mcp_list": true, "ask_user_question": true, "vision": true}

// builtinMCPToolPrefix 是 mcode 自带 MCP 工具的名字前缀（见 mcp_bridge.ts）；它们的权限由 MCP
// 服务端自己校验，计划模式下不算写操作（与 mcode-gate.ts 的 BUILTIN_MCP_TOOL_PREFIX 一致）。
const builtinMCPToolPrefix = "mcp__mcode__"

// planBlocksTool 判断计划模式下这次调用是否直接拒绝：计划模式只读，只放行只读工具与
// mcode 自带 MCP 工具。只读 shell 命令由门禁扩展直接放行，不会进到这里。
func planBlocksTool(turn harness.Turn, toolName string) bool {
	if turn.InteractionMode != "plan" {
		return false
	}
	return !readOnlyGateTools[toolName] && !strings.HasPrefix(toolName, builtinMCPToolPrefix)
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
		ToolID:    gate.ToolCallID,
		ToolName:  name,
		Input:     gate.Input,
		Kind:      kind,
		Sensitive: gate.Sensitive,
	}
	// 摘要取行内标题：命令取 command，文件变更取 path，只读工具优先取路径或模式。
	switch kind {
	case harness.RequestKindCommand:
		req.Summary = inputString(gate.Input, "command")
	case harness.RequestKindFileChange:
		req.Summary = inputString(gate.Input, "path")
	default:
		for _, key := range []string{"path", "pattern", "glob"} {
			if s := inputString(gate.Input, key); s != "" {
				req.Summary = s
				break
			}
		}
	}
	return req
}

// gateValue 把审批答复映射为 pi 扩展期望的选项文本（Allow / Allow for session / Deny）。
// DecisionAsk 在这里等同于拒绝：适配器没有向用户二次确认的通道。
func gateValue(resp harness.ApprovalResponse) string {
	for _, answers := range resp.Answers {
		for _, answer := range answers {
			if strings.EqualFold(answer, "allow for session") {
				return "Allow for session"
			}
		}
	}
	if resp.Decision == harness.DecisionAllow {
		return "Allow"
	}
	return "Deny"
}

// questionRequest 把 pi 扩展的提问包装为 harness 审批请求（Kind 为 question）。
func questionRequest(id, prompt string, options []string) harness.ApprovalRequest {
	question := harness.Question{
		ID:          id,
		Header:      "Pi",
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

// handleExtensionUI 应答 pi 扩展发来的交互请求（工具门禁、选择、确认、输入）。
// notify / setWidget 等没有应答通道，直接忽略；setStatus 里的 Magpie 账号回传另行消费。
func (h *Harness) handleExtensionUI(ctx context.Context, turn harness.Turn, writer *lineWriter, ev *rpcEvent) {
	// setStatus 是无需应答的通知：Magpie 桥用它回传实际路由到的上游账号，
	// 处理完直接返回，不进审批流程。
	if ev.Method == "setStatus" {
		h.handleSetStatus(ev)
		return
	}
	id := ev.ID
	if id == "" {
		id = ev.RequestID
	}
	if id == "" {
		return
	}
	params := ev.Params
	var payload struct {
		Title   string   `json:"title"`
		Message string   `json:"message"`
		Options []string `json:"options"`
	}
	// 有的扩展把 title/options 放在根对象，有的放在 params，两处都读，params 最后覆盖。
	payload.Title = ev.Title
	payload.Options = ev.Options
	if len(ev.Message) > 0 {
		var message string
		if err := json.Unmarshal(ev.Message, &message); err == nil {
			payload.Message = message
		} else if payload.Title == "" && len(payload.Options) == 0 {
			// 旧格式把整个载荷塞在 message 对象里。
			_ = json.Unmarshal(ev.Message, &payload)
		}
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &payload)
	}

	switch ev.Method {
	case "select":
		title := strings.TrimSpace(payload.Title)
		if title == "" && len(payload.Options) == 0 {
			// 没有标题也没有选项：问也问不出东西，直接取消，别弹空白提问。
			if line, err := requestLine(cancelledResponse(id)); err == nil {
				_ = writer.write(line)
			}
			return
		}
		if gate, ok := parseGatePayload(title); ok {
			h.answerGate(ctx, turn, writer, id, gate)
			return
		}
		h.answerQuestion(ctx, turn, writer, id, "", title, payload.Options)
	case confirmMethod:
		prompt := strings.TrimSpace(payload.Title)
		if message := strings.TrimSpace(payload.Message); message != "" {
			prompt = prompt + "\n" + message
		}
		h.answerQuestion(ctx, turn, writer, id, confirmMethod, prompt, []string{"Yes", "No"})
	case "input", "editor":
		h.answerQuestion(ctx, turn, writer, id, "", strings.TrimSpace(payload.Title), nil)
	default:
		// 其余方法不需要应答。
	}
}

// handleSetStatus 消费扩展的 setStatus 通知：只关心 Magpie 桥回传的账号，
// 解析后交给 Options.OnMagpieMember，其余键一律忽略。
func (h *Harness) handleSetStatus(ev *rpcEvent) {
	if ev.StatusKey != magpieMemberStatusKey || h.opts.OnMagpieMember == nil {
		return
	}
	var member magpieMember
	if err := json.Unmarshal([]byte(ev.StatusText), &member); err != nil {
		return
	}
	if member.Endpoint == "" {
		return
	}
	h.opts.OnMagpieMember(member.Endpoint, member.Provider, member.Model)
}

// answerGate 应答一次工具门禁：计划模式只读，能改东西的调用直接拒绝（扩展据此给出
// “plan mode only allows reading”的说明），其余交 turn.Approver 裁决。
// 没有审批方时按拒绝处理：扩展在等这条应答，悬着会卡死当轮。
func (h *Harness) answerGate(ctx context.Context, turn harness.Turn, writer *lineWriter, id string, gate *gatePayload) {
	req := approvalFromGate(gate)
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
	val := gateValue(resp)
	if gate.Sensitive != "" && val == "Allow for session" {
		val = "Allow"
	}
	line, err := requestLine(valueResponse(id, val))
	if err == nil {
		err = writer.write(line)
	}
	harness.AckDelivery(turn.Approver, req, err)
}

// answerQuestion 应答 pi 扩展的选择/确认/输入请求。
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
