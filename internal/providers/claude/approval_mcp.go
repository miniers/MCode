package claude

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"mcode/internal/harness"
)

type approvalServer struct {
	listener  net.Listener
	server    *http.Server
	serverURL string
	tokenStr  string
	closeOnce sync.Once

	// mu 保护 approver：常驻进程的审批服务与进程同生共死，每一轮开始/结束各自替换审批方，
	// 而 HTTP 处理协程可能并发读取它。
	mu       sync.RWMutex
	approver harness.Approver
}

// setApprover 替换本轮使用的审批方；传 nil 表示这一轮没有审批方（审批请求一律拒绝）。
func (s *approvalServer) setApprover(approver harness.Approver) {
	s.mu.Lock()
	s.approver = approver
	s.mu.Unlock()
}

// currentApprover 返回当前这一轮的审批方。
func (s *approvalServer) currentApprover() harness.Approver {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.approver
}

func startApprovalServer(ctx context.Context, approver harness.Approver) (*approvalServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		_ = ln.Close()
		return nil, err
	}
	token := hex.EncodeToString(tokenBytes)

	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return nil, fmt.Errorf("expected *net.TCPAddr, got %T", ln.Addr())
	}
	serverURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", tcpAddr.Port)

	s := &approvalServer{
		listener:  ln,
		serverURL: serverURL,
		tokenStr:  token,
		approver:  approver,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", s.handleMCP)
	s.server = &http.Server{
		Handler: mux,
	}

	go func() {
		_ = s.server.Serve(ln)
	}()

	go func() {
		<-ctx.Done()
		_ = s.close()
	}()

	return s, nil
}

func (s *approvalServer) url() string {
	return s.serverURL
}

func (s *approvalServer) token() string {
	return s.tokenStr
}

func (s *approvalServer) close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.server.Close()
	})
	return err
}

func (s *approvalServer) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mcp" {
		http.NotFound(w, r)
		return
	}

	if r.Method == http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	auth := r.Header.Get("Authorization")
	if auth != "Bearer "+s.tokenStr {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	type rpcRequest struct {
		JSONRPC string           `json:"jsonrpc"`
		ID      *json.RawMessage `json:"id"`
		Method  string           `json:"method"`
		Params  json.RawMessage  `json:"params"`
	}

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// 没有 id 的通知（如 notifications/initialized）：返回 HTTP 202，响应体为空
	if req.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		var initParams struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &initParams)
		}
		protoVer := initParams.ProtocolVersion
		if protoVer == "" {
			protoVer = "2025-06-18"
		}
		res := map[string]any{
			"protocolVersion": protoVer,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "mcode",
				"version": "0.1.0",
			},
		}
		s.writeRPCResponse(w, req.ID, res, nil)

	case "tools/list":
		res := map[string]any{
			"tools": []any{
				map[string]any{
					"name":        "approve",
					"description": "Ask the mcode user to approve a tool call",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"tool_name": map[string]any{
								"type": "string",
							},
							"input": map[string]any{
								"type": "object",
							},
							"tool_use_id": map[string]any{
								"type": "string",
							},
						},
						"required": []string{"tool_name", "input"},
					},
				},
			},
		}
		s.writeRPCResponse(w, req.ID, res, nil)

	case "tools/call":
		var callParams struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &callParams); err != nil || callParams.Name != "approve" {
			s.writeRPCError(w, req.ID, -32601, "Method not found")
			return
		}
		s.handleApproveCall(w, r, req.ID, callParams.Arguments)

	default:
		s.writeRPCError(w, req.ID, -32601, "Method not found")
	}
}

func (s *approvalServer) handleApproveCall(w http.ResponseWriter, r *http.Request, reqID *json.RawMessage, rawArgs json.RawMessage) {
	var args struct {
		ToolName  string          `json:"tool_name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
	}
	if len(rawArgs) > 0 {
		_ = json.Unmarshal(rawArgs, &args)
	}

	inputStr := string(args.Input)
	if runes := []rune(inputStr); len(runes) > 200 {
		inputStr = string(runes[:200])
	}
	summary := args.ToolName
	if inputStr != "" {
		summary += " " + inputStr
	}

	approvalReq := harness.ApprovalRequest{
		ToolID:   args.ToolUseID,
		ToolName: args.ToolName,
		Input:    args.Input,
		Summary:  summary,
	}

	workDir := ""
	// 一次请求内固定用同一个审批方：等到这里才取快照，避免与下一轮的 setApprover 交错。
	approver := s.currentApprover()
	if wp, ok := approver.(harness.WorkDirProvider); ok {
		workDir = wp.WorkDir()
	}
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	resolvePath := func(p string) string {
		if p == "" {
			return ""
		}
		if filepath.IsAbs(p) {
			return p
		}
		if workDir != "" {
			return filepath.Join(workDir, p)
		}
		return p
	}

	switch args.ToolName {
	case "AskUserQuestion":
		approvalReq.Kind = harness.RequestKindQuestion
		approvalReq.Questions = parseClaudeQuestions(args.Input)
	case "ExitPlanMode":
		approvalReq.Kind = harness.RequestKindPlan
		var planInput struct {
			Plan string `json:"plan"`
		}
		_ = json.Unmarshal(args.Input, &planInput)
		approvalReq.Plan = planInput.Plan
	case "Edit":
		var in struct {
			FilePath   string `json:"file_path"`
			OldString  string `json:"old_string"`
			NewString  string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		}
		if err := json.Unmarshal(args.Input, &in); err == nil && in.FilePath != "" {
			approvalReq.Diff = harness.PreviewEditDiff(resolvePath(in.FilePath), in.OldString, in.NewString, in.ReplaceAll)
		}
	case "MultiEdit":
		var in struct {
			FilePath string `json:"file_path"`
			Edits    []struct {
				OldString  string `json:"old_string"`
				NewString  string `json:"new_string"`
				ReplaceAll bool   `json:"replace_all"`
			} `json:"edits"`
		}
		if err := json.Unmarshal(args.Input, &in); err == nil && in.FilePath != "" && len(in.Edits) > 0 {
			edits := make([]harness.EditOperation, len(in.Edits))
			for i, e := range in.Edits {
				edits[i] = harness.EditOperation{
					OldString:  e.OldString,
					NewString:  e.NewString,
					ReplaceAll: e.ReplaceAll,
				}
			}
			approvalReq.Diff = harness.PreviewMultiEditDiff(resolvePath(in.FilePath), edits)
		}
	case "Write":
		var in struct {
			FilePath string `json:"file_path"`
			Content  string `json:"content"`
		}
		if err := json.Unmarshal(args.Input, &in); err == nil && in.FilePath != "" {
			approvalReq.Diff = harness.PreviewWriteDiff(resolvePath(in.FilePath), in.Content)
		}
	}

	var resp harness.ApprovalResponse
	var approveErr error
	if ia, ok := approver.(harness.Interactor); ok {
		resp, approveErr = ia.Interact(r.Context(), approvalReq)
	} else if approver != nil {
		resp.Decision, approveErr = approver.Approve(r.Context(), approvalReq)
	} else {
		resp.Decision = harness.DecisionDeny
	}

	denyPayload := func(msg string) []byte {
		b, _ := json.Marshal(map[string]string{
			"behavior": "deny",
			"message":  msg,
		})
		return b
	}

	if approveErr != nil {
		harness.AckDelivery(approver, approvalReq, approveErr)
		callResult := map[string]any{
			"content": []map[string]any{
				{
					"type": "text",
					"text": string(denyPayload(approveErr.Error())),
				},
			},
		}
		_ = s.writeRPCResponse(w, reqID, callResult, nil)
		return
	}

	inputMsg := args.Input
	if len(inputMsg) == 0 {
		inputMsg = json.RawMessage("{}")
	}
	allowPayload := func(updated json.RawMessage) []byte {
		b, _ := json.Marshal(struct {
			Behavior     string          `json:"behavior"`
			UpdatedInput json.RawMessage `json:"updatedInput"`
		}{
			Behavior:     "allow",
			UpdatedInput: updated,
		})
		return b
	}

	var payloadJSON []byte
	switch {
	case approvalReq.Kind == harness.RequestKindQuestion:
		if resp.Skipped || resp.Decision != harness.DecisionAllow {
			payloadJSON = denyPayload("用户跳过了提问，请自行判断")
		} else {
			payloadJSON = allowPayload(withClaudeAnswers(inputMsg, approvalReq.Questions, resp.Answers))
		}
	case approvalReq.Kind == harness.RequestKindPlan:
		if resp.Decision == harness.DecisionAllow {
			payloadJSON = allowPayload(inputMsg)
		} else {
			msg := "用户驳回了计划"
			if resp.Feedback != "" {
				msg += "：" + resp.Feedback
			}
			payloadJSON = denyPayload(msg)
		}
	case resp.Decision == harness.DecisionAllow:
		payloadJSON = allowPayload(inputMsg)
	default:
		payloadJSON = denyPayload("用户拒绝了该工具调用")
	}

	callResult := map[string]any{
		"content": []map[string]any{
			{
				"type": "text",
				"text": string(payloadJSON),
			},
		},
	}
	writeErr := s.writeRPCResponse(w, reqID, callResult, nil)
	harness.AckDelivery(approver, approvalReq, writeErr)
}

// parseClaudeQuestions 解析 AskUserQuestion 输入中的 questions 数组，题目 ID 取题目文本，为空时取下标。
func parseClaudeQuestions(input json.RawMessage) []harness.Question {
	var in struct {
		Questions []struct {
			Question string `json:"question"`
			Header   string `json:"header"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
			MultiSelect bool `json:"multiSelect"`
		} `json:"questions"`
	}
	if len(input) > 0 {
		_ = json.Unmarshal(input, &in)
	}
	out := make([]harness.Question, 0, len(in.Questions))
	for i, q := range in.Questions {
		id := q.Question
		if id == "" {
			id = strconv.Itoa(i)
		}
		choices := make([]harness.QuestionChoice, 0, len(q.Options))
		for _, o := range q.Options {
			choices = append(choices, harness.QuestionChoice{Label: o.Label, Detail: o.Description})
		}
		out = append(out, harness.Question{
			ID:             id,
			Header:         q.Header,
			Prompt:         q.Question,
			Choices:        choices,
			AllowsMultiple: q.MultiSelect,
			// Claude 的 AskUserQuestion 始终允许用户填写“其他”
			AllowsOther: true,
		})
	}
	return out
}

// withClaudeAnswers 在原输入上追加 answers 对象：键为题目文本，多个值以 ", " 连接。
func withClaudeAnswers(input json.RawMessage, questions []harness.Question, answers map[string][]string) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(input, &obj); err != nil || obj == nil {
		obj = make(map[string]json.RawMessage)
	}
	out := make(map[string]string, len(answers))
	for _, q := range questions {
		vals, ok := answers[q.ID]
		if !ok {
			continue
		}
		key := q.Prompt
		if key == "" {
			key = q.ID
		}
		out[key] = strings.Join(vals, ", ")
	}
	encoded, _ := json.Marshal(out)
	obj["answers"] = encoded
	merged, err := json.Marshal(obj)
	if err != nil {
		return input
	}
	return merged
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *approvalServer) writeRPCResponse(w http.ResponseWriter, id *json.RawMessage, result any, rpcErr *jsonRPCError) error {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
	}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(resp)
}

func (s *approvalServer) writeRPCError(w http.ResponseWriter, id *json.RawMessage, code int, message string) {
	_ = s.writeRPCResponse(w, id, nil, &jsonRPCError{
		Code:    code,
		Message: message,
	})
}
