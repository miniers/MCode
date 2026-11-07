package agy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"mcode/internal/harness"
)

const hookPythonScriptTemplate = `#!/usr/bin/env python3
import sys
import json
import urllib.request
import urllib.error

TOKEN = "%s"
PORT = %d

def main():
    try:
        raw_input = sys.stdin.read()
        if not raw_input.strip():
            sys.exit(0)

        try:
            data = json.loads(raw_input)
        except Exception:
            sys.exit(0)

        if not isinstance(data, dict):
            sys.exit(0)

        tool_call = data.get("toolCall") or data.get("tool_call") or {}
        if not isinstance(tool_call, dict):
            tool_call = {}

        tool_name = (
            tool_call.get("name")
            or data.get("toolName")
            or data.get("tool_name")
            or data.get("name")
        )

        if tool_name != "ask_question":
            print(json.dumps({"decision": "allow"}))
            sys.exit(0)

        req = urllib.request.Request(
            f"http://127.0.0.1:{PORT}/question",
            data=raw_input.encode("utf-8"),
            headers={
                "Authorization": f"Bearer {TOKEN}",
                "Content-Type": "application/json"
            },
            method="POST"
        )
        with urllib.request.urlopen(req, timeout=610) as resp:
            resp_body = resp.read().decode("utf-8")
            print(resp_body)
            sys.exit(0)
    except Exception:
        print(json.dumps({"decision": "deny", "reason": "Question bridge unavailable."}))
        sys.exit(0)

if __name__ == "__main__":
    main()
`

// QuestionBridge 拦截 agy 的 ask_question 工具调用并通过 Interactor 桥接给前端。
type QuestionBridge struct {
	dir      string
	token    string
	server   *http.Server
	listener net.Listener
	approver harness.Approver
	ctx      context.Context
	mu       sync.Mutex
	closed   bool
}

// StartQuestionBridge 创建临时目录、生成 PreToolUse Hook 配置并启动回环 HTTP 服务。
func StartQuestionBridge(ctx context.Context, approver harness.Approver) (*QuestionBridge, error) {
	token := uuid.NewString()
	dir, err := os.MkdirTemp("", "mcode-agy-question-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir for question bridge: %w", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("listen loopback for question bridge: %w", err)
	}

	port := ln.Addr().(*net.TCPAddr).Port

	bridge := &QuestionBridge{
		dir:      dir,
		token:    token,
		listener: ln,
		approver: approver,
		ctx:      ctx,
	}

	if err := bridge.setupFiles(port); err != nil {
		_ = bridge.Close()
		return nil, fmt.Errorf("setup question bridge files: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/question", bridge.handleQuestion)

	srv := &http.Server{
		Handler: mux,
	}
	bridge.server = srv

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("agy: question bridge serve error: %v", err)
		}
	}()

	return bridge, nil
}

func (b *QuestionBridge) Dir() string {
	return b.dir
}

func (b *QuestionBridge) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()

	var err error
	if b.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = b.server.Shutdown(ctx)
	}
	if b.listener != nil {
		_ = b.listener.Close()
	}
	if b.dir != "" {
		_ = os.RemoveAll(b.dir)
	}
	return err
}

func (b *QuestionBridge) setupFiles(port int) error {
	agentsDir := filepath.Join(b.dir, ".agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return err
	}

	hookPath := filepath.Join(b.dir, "hook.py")
	pyScript := fmt.Sprintf(hookPythonScriptTemplate, b.token, port)
	if err := os.WriteFile(hookPath, []byte(pyScript), 0755); err != nil {
		return err
	}

	pyBin := findPython3()
	commandString := fmt.Sprintf("%s %s", pyBin, hookPath)

	hooksConfig := map[string]any{
		"mcode-question-bridge": map[string]any{
			"PreToolUse": []map[string]any{
				{
					"matcher": "^ask_question$",
					"hooks": []map[string]any{
						{
							"type":    "command",
							"command": commandString,
							"timeout": 615,
						},
					},
				},
			},
		},
	}

	hooksData, err := json.MarshalIndent(hooksConfig, "", "  ")
	if err != nil {
		return err
	}
	hooksPath := filepath.Join(agentsDir, "hooks.json")
	return os.WriteFile(hooksPath, hooksData, 0644)
}

func findPython3() string {
	if _, err := os.Stat("/usr/bin/python3"); err == nil {
		return "/usr/bin/python3"
	}
	if p, err := exec.LookPath("python3"); err == nil {
		return p
	}
	return "python3"
}

func (b *QuestionBridge) handleQuestion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBridgeJSON(w, http.StatusNotFound, map[string]any{
			"decision": "deny",
			"reason":   "Not found",
		})
		return
	}

	auth := r.Header.Get("Authorization")
	if auth != "Bearer "+b.token {
		writeBridgeJSON(w, http.StatusUnauthorized, map[string]any{
			"decision": "deny",
			"reason":   "Unauthorized",
		})
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 10*1024*1024))
	if err != nil {
		writeBridgeJSON(w, http.StatusBadRequest, map[string]any{
			"decision": "deny",
			"reason":   "Invalid request body",
		})
		return
	}

	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		writeBridgeJSON(w, http.StatusBadRequest, map[string]any{
			"decision": "deny",
			"reason":   "Invalid JSON",
		})
		return
	}

	rawQuestions := extractRawQuestions(raw)
	if len(rawQuestions) == 0 {
		writeBridgeJSON(w, http.StatusOK, map[string]any{
			"decision": "deny",
			"reason":   "No questions specified.",
		})
		return
	}

	questions := parseQuestions(rawQuestions)
	if len(questions) == 0 {
		writeBridgeJSON(w, http.StatusOK, map[string]any{
			"decision": "deny",
			"reason":   "No valid questions.",
		})
		return
	}

	interactor, ok := b.approver.(harness.Interactor)
	if !ok || b.approver == nil {
		writeBridgeJSON(w, http.StatusOK, map[string]any{
			"decision": "allow",
		})
		return
	}

	reqCtx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		select {
		case <-b.ctx.Done():
			cancel()
		case <-reqCtx.Done():
		}
	}()

	approvalReq := harness.ApprovalRequest{
		ToolID:    fmt.Sprintf("agy-q-%s", uuid.NewString()[:8]),
		ToolName:  "ask_question",
		Input:     body,
		Kind:      harness.RequestKindQuestion,
		Questions: questions,
	}

	resp, err := interactor.Interact(reqCtx, approvalReq)
	if err != nil {
		harness.AckDelivery(b.approver, approvalReq, err)
		_ = writeBridgeJSON(w, http.StatusOK, map[string]any{
			"decision": "deny",
			"reason":   fmt.Sprintf("Question interaction failed: %v", err),
		})
		return
	}

	if len(resp.Answers) > 0 {
		formattedAnswers := make(map[string]any, len(resp.Answers))
		for k, v := range resp.Answers {
			if len(v) == 1 {
				formattedAnswers[k] = v[0]
			} else {
				formattedAnswers[k] = v
			}
		}

		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(map[string]any{"answers": formattedAnswers})
		jsonString := strings.TrimSpace(buf.String())

		prefix := "mcode handled this question through the Desktop UI. Native ask_question is blocked only to prevent automatic skipping. The following JSON contains the actual user response, not a tool permission decision: "
		reason := prefix + jsonString

		writeErr := writeBridgeJSON(w, http.StatusOK, map[string]any{
			"decision": "deny",
			"reason":   reason,
		})
		harness.AckDelivery(b.approver, approvalReq, writeErr)
		return
	}

	if resp.Skipped {
		writeErr := writeBridgeJSON(w, http.StatusOK, map[string]any{
			"decision": "deny",
			"reason":   "User skipped question.",
		})
		harness.AckDelivery(b.approver, approvalReq, writeErr)
		return
	}

	if resp.Decision == harness.DecisionDeny && resp.Feedback != "" {
		writeErr := writeBridgeJSON(w, http.StatusOK, map[string]any{
			"decision": "deny",
			"reason":   resp.Feedback,
		})
		harness.AckDelivery(b.approver, approvalReq, writeErr)
		return
	}

	prefix := "mcode handled this question through the Desktop UI. Native ask_question is blocked only to prevent automatic skipping. The following JSON contains the actual user response, not a tool permission decision: "
	writeErr := writeBridgeJSON(w, http.StatusOK, map[string]any{
		"decision": "deny",
		"reason":   prefix + `{"answers":{}}`,
	})
	harness.AckDelivery(b.approver, approvalReq, writeErr)
}

func extractRawQuestions(root any) []map[string]any {
	m, ok := root.(map[string]any)
	if !ok {
		return nil
	}
	if qs := toMapSlice(m["questions"]); len(qs) > 0 {
		return qs
	}

	toolCall := m
	if tc, ok := m["toolCall"].(map[string]any); ok {
		toolCall = tc
	} else if tc, ok := m["tool_call"].(map[string]any); ok {
		toolCall = tc
	}

	for _, key := range []string{"args", "parameters", "input", "arguments"} {
		if val, ok := toolCall[key]; ok {
			if container, ok := val.(map[string]any); ok {
				if qs := toMapSlice(container["questions"]); len(qs) > 0 {
					return qs
				}
			} else if str, ok := val.(string); ok {
				var inner map[string]any
				if err := json.Unmarshal([]byte(str), &inner); err == nil {
					if qs := toMapSlice(inner["questions"]); len(qs) > 0 {
						return qs
					}
				}
			}
		}
	}

	if qs := toMapSlice(toolCall["questions"]); len(qs) > 0 {
		return qs
	}
	return nil
}

func toMapSlice(v any) []map[string]any {
	if v == nil {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	res := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			res = append(res, m)
		}
	}
	return res
}

func parseQuestions(rawQuestions []map[string]any) []harness.Question {
	questions := make([]harness.Question, 0, len(rawQuestions))
	usedIDs := make(map[string]bool)

	for i, item := range rawQuestions {
		prompt := ""
		for _, key := range []string{"question", "prompt", "text", "title"} {
			if str, ok := item[key].(string); ok && str != "" {
				prompt = str
				break
			}
		}
		if prompt == "" {
			prompt = fmt.Sprintf("Question %d", i+1)
		}

		qID := prompt
		if usedIDs[qID] {
			qID = fmt.Sprintf("%s-%d", prompt, i+1)
		}
		usedIDs[qID] = true

		var choices []harness.QuestionChoice
		if opts, ok := item["options"].([]any); ok {
			for _, opt := range opts {
				if str, ok := opt.(string); ok && str != "" {
					choices = append(choices, harness.QuestionChoice{Label: str})
				} else if obj, ok := opt.(map[string]any); ok {
					label := ""
					for _, k := range []string{"label", "text", "value"} {
						if s, ok := obj[k].(string); ok && s != "" {
							label = s
							break
						}
					}
					detail := ""
					for _, k := range []string{"detail", "description"} {
						if s, ok := obj[k].(string); ok && s != "" {
							detail = s
							break
						}
					}
					if label != "" {
						choices = append(choices, harness.QuestionChoice{
							Label:  label,
							Detail: detail,
						})
					}
				}
			}
		}

		allowsMultiple := false
		for _, key := range []string{"is_multi_select", "isMultiSelect", "allowsMultipleSelections", "allows_multiple_selections", "allowsMultiple"} {
			if b, ok := item[key].(bool); ok {
				allowsMultiple = b
				break
			}
		}

		header := ""
		if h, ok := item["header"].(string); ok && h != "" {
			header = h
		} else if len(rawQuestions) > 1 {
			header = fmt.Sprintf("Question %d", i+1)
		}

		questions = append(questions, harness.Question{
			ID:             qID,
			Header:         header,
			Prompt:         prompt,
			Choices:        choices,
			AllowsMultiple: allowsMultiple,
			AllowsOther:    true,
			IsSecret:       false,
		})
	}
	return questions
}

func writeBridgeJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
