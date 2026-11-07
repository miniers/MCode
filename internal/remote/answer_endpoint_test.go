package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

// answerTestSeq 给每个用例生成互不冲突的会话与动作 ID。
var answerTestSeq atomic.Int64

func answerTestIDs() (threadID, actionID string) {
	n := answerTestSeq.Add(1)
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", n), fmt.Sprintf("10000000-0000-4000-8000-%012d", n)
}

// answerQuestion 是本文件最常用的单题、双选项提问。
func answerQuestion() harness.ApprovalRequest {
	return harness.ApprovalRequest{
		ToolName: "AskUserQuestion",
		Kind:     harness.RequestKindQuestion,
		Questions: []harness.Question{{
			ID:      "q1",
			Header:  "库",
			Prompt:  "用哪个库？",
			Choices: []harness.QuestionChoice{{Label: "A"}, {Label: "B"}},
		}},
	}
}

// newAnswerTestAuth 生成已配对设备，返回鉴权器与长效会话令牌。
func newAnswerTestAuth(t *testing.T) (*PairingAuth, string) {
	t.Helper()
	auth, err := NewPairingAuth(PairingAuthOptions{
		DevicesPath: filepath.Join(t.TempDir(), "remote_devices.json"),
		LANBaseURL:  func() string { return "http://192.168.1.10:43117" },
	})
	if err != nil {
		t.Fatalf("NewPairingAuth failed: %v", err)
	}
	parts := strings.Split(auth.CurrentPairingURLs().PairingURL, "#pair=")
	if len(parts) != 2 {
		t.Fatalf("invalid pairing URL: %s", auth.CurrentPairingURLs().PairingURL)
	}
	result, err := auth.Pair(parts[1], "Phone", ClientInfo{Entry: EntryTLS, Address: "192.168.1.50"})
	if err != nil {
		t.Fatalf("Pair failed: %v", err)
	}
	return auth, result.SessionToken
}

type answerTestEnv struct {
	backend  *MemoryBackend
	auth     *PairingAuth
	token    string
	threadID string
	req      wire.RemoteRequest
	resultCh <-chan interactResult
	handler  http.Handler
}

// newAnswerTestEnv 挂起一条真实提问请求，并组装只读依赖内存后端的作答端点。
func newAnswerTestEnv(t *testing.T, approval harness.ApprovalRequest) *answerTestEnv {
	t.Helper()
	threadID, actionID := answerTestIDs()
	backend, req, resultCh := startInteract(t, threadID, actionID, approval)
	auth, token := newAnswerTestAuth(t)
	return &answerTestEnv{
		backend:  backend,
		auth:     auth,
		token:    token,
		threadID: threadID,
		req:      req,
		resultCh: resultCh,
		handler: NewAnswerEndpoint(AnswerEndpointOptions{
			Auth:    auth,
			Backend: backend,
			Entry:   EntryTLS,
		}),
	}
}

// body 按端点约定拼出合法载荷。
func (e *answerTestEnv) body(choice string) string {
	raw, err := json.Marshal(map[string]string{
		"threadID":  e.threadID,
		"requestID": e.req.RequestID,
		"choice":    choice,
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// post 发出 POST 请求；mutate 可覆盖请求头或请求体。
func (e *answerTestEnv) post(t *testing.T, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "https://mcode.example.ts.net"+answerPath, strings.NewReader(body))
	req.Host = "mcode.example.ts.net"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.token)
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func (e *answerTestEnv) pending(t *testing.T) []wire.RemoteRequest {
	t.Helper()
	list, err := e.backend.ListRequests(context.Background(), e.threadID)
	if err != nil {
		t.Fatalf("ListRequests failed: %v", err)
	}
	return list.Requests
}

func TestAnswerEndpoint_MethodNotAllowed(t *testing.T) {
	env := newAnswerTestEnv(t, answerQuestion())
	req := httptest.NewRequest(http.MethodGet, "https://mcode.example.ts.net"+answerPath, nil)
	req.Host = "mcode.example.ts.net"
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Errorf("expected Allow POST, got %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("expected Cache-Control no-store, got %q", got)
	}
}

func TestAnswerEndpoint_ContentType(t *testing.T) {
	env := newAnswerTestEnv(t, answerQuestion())
	rec := env.post(t, env.body("A"), func(r *http.Request) {
		r.Header.Set("Content-Type", "text/plain")
	})
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", rec.Code)
	}
	if len(env.pending(t)) != 1 {
		t.Fatal("request should still be pending after 415")
	}
}

func TestAnswerEndpoint_BodyTooLarge(t *testing.T) {
	env := newAnswerTestEnv(t, answerQuestion())
	body := `{"threadID":"` + env.threadID + `","requestID":"` + env.req.RequestID +
		`","choice":"` + strings.Repeat("x", maxAnswerBodyBytes) + `"}`
	rec := env.post(t, body, nil)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", rec.Code)
	}
}

func TestAnswerEndpoint_InvalidJSON(t *testing.T) {
	env := newAnswerTestEnv(t, answerQuestion())
	cases := []struct {
		name string
		body string
	}{
		{"not json", `not json`},
		{"unknown field", `{"threadID":"` + env.threadID + `","requestID":"` + env.req.RequestID + `","choice":"A","extra":1}`},
		{"trailing data", env.body("A") + `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rec := env.post(t, tc.body, nil); rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", rec.Code)
			}
		})
	}
	if len(env.pending(t)) != 1 {
		t.Fatal("request should still be pending after bad JSON")
	}
}

func TestAnswerEndpoint_Unauthorized(t *testing.T) {
	env := newAnswerTestEnv(t, answerQuestion())
	cases := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"missing token", func(r *http.Request) { r.Header.Del("Authorization") }},
		{"wrong token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong-token") }},
		{"wrong scheme", func(r *http.Request) { r.Header.Set("Authorization", "Token "+env.token) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rec := env.post(t, env.body("A"), tc.mutate); rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rec.Code)
			}
		})
	}
	if len(env.pending(t)) != 1 {
		t.Fatal("request should still be pending after failed auth")
	}
}

func TestAnswerEndpoint_UnknownThread(t *testing.T) {
	env := newAnswerTestEnv(t, answerQuestion())
	body, _ := json.Marshal(map[string]string{
		"threadID":  "00000000-0000-4000-8000-000000000999",
		"requestID": env.req.RequestID,
		"choice":    "A",
	})
	if rec := env.post(t, string(body), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestAnswerEndpoint_InvalidThreadID(t *testing.T) {
	env := newAnswerTestEnv(t, answerQuestion())
	body, _ := json.Marshal(map[string]string{
		"threadID":  "not-a-uuid",
		"requestID": env.req.RequestID,
		"choice":    "A",
	})
	if rec := env.post(t, string(body), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAnswerEndpoint_RequestNotPending(t *testing.T) {
	env := newAnswerTestEnv(t, answerQuestion())

	unknown, _ := json.Marshal(map[string]string{
		"threadID":  env.threadID,
		"requestID": "00000000-0000-4000-8000-000000000998",
		"choice":    "A",
	})
	if rec := env.post(t, string(unknown), nil); rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for unknown request, got %d", rec.Code)
	}

	if rec := env.post(t, env.body("A"), nil); rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on first answer, got %d", rec.Code)
	}
	if rec := env.post(t, env.body("A"), nil); rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate answer, got %d", rec.Code)
	}
}

func TestAnswerEndpoint_NotAnswerable(t *testing.T) {
	single := func(mutate func(*harness.Question)) harness.ApprovalRequest {
		req := answerQuestion()
		mutate(&req.Questions[0])
		return req
	}
	cases := []struct {
		name     string
		approval harness.ApprovalRequest
	}{
		{"multiple", single(func(q *harness.Question) { q.AllowsMultiple = true })},
		{"secret", single(func(q *harness.Question) { q.IsSecret = true })},
		{"three choices", single(func(q *harness.Question) {
			q.Choices = append(q.Choices, harness.QuestionChoice{Label: "C"})
		})},
		{"two questions", func() harness.ApprovalRequest {
			req := answerQuestion()
			second := req.Questions[0]
			second.ID = "q2"
			req.Questions = append(req.Questions, second)
			return req
		}()},
		{"command approval", harness.ApprovalRequest{
			ToolName: "Bash",
			Kind:     harness.RequestKindCommand,
			Summary:  "rm -rf build",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newAnswerTestEnv(t, tc.approval)
			if rec := env.post(t, env.body("A"), nil); rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d", rec.Code)
			}
			if len(env.pending(t)) != 1 {
				t.Fatal("request should still be pending after 403")
			}
		})
	}
}

func TestAnswerEndpoint_RequestWithdrawn(t *testing.T) {
	env := newAnswerTestEnv(t, answerQuestion())
	if _, err := env.backend.InterruptCurrent(context.Background(), env.threadID); err != nil {
		t.Fatalf("InterruptCurrent failed: %v", err)
	}
	if rec := env.post(t, env.body("A"), nil); rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 after withdraw, got %d", rec.Code)
	}
}

func TestAnswerEndpoint_InvalidChoice(t *testing.T) {
	env := newAnswerTestEnv(t, answerQuestion())
	if rec := env.post(t, env.body("C"), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown choice, got %d", rec.Code)
	}
	if len(env.pending(t)) != 1 {
		t.Fatal("request should still be pending after invalid choice")
	}
}

func TestAnswerEndpoint_Success(t *testing.T) {
	env := newAnswerTestEnv(t, answerQuestion())
	rec := env.post(t, env.body("B"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("expected Cache-Control no-store, got %q", got)
	}
	var body map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body["ok"] {
		t.Errorf("expected ok true, got %v", body)
	}

	// 先等 Interact 收尾：非送达确认路径下，pending 由 Interact 返回前清理。
	result := waitInteract(t, env.resultCh)
	if result.err != nil {
		t.Fatalf("Interact returned error: %v", result.err)
	}
	answers := result.resp.Answers["q1"]
	if len(answers) != 1 || answers[0] != "B" {
		t.Fatalf("expected q1 -> [B], got %v", result.resp.Answers)
	}
	if pending := env.pending(t); len(pending) != 0 {
		t.Fatalf("expected no pending requests after success, got %d", len(pending))
	}
}

// answerSourceRecorder 包装真实后端，记下作答请求携带的答复来源后委托处理。
type answerSourceRecorder struct {
	AnswerBackend
	source string
}

func (r *answerSourceRecorder) RespondRequest(ctx context.Context, params wire.RequestsRespondParams) (wire.RequestsRespondResult, error) {
	r.source = AnswerSourceOf(ctx)
	return r.AnswerBackend.RespondRequest(ctx, params)
}

func TestAnswerEndpoint_MarksRemoteAnswerSource(t *testing.T) {
	env := newAnswerTestEnv(t, answerQuestion())
	recorder := &answerSourceRecorder{AnswerBackend: env.backend}
	env.handler = NewAnswerEndpoint(AnswerEndpointOptions{
		Auth:    env.auth,
		Backend: recorder,
		Entry:   EntryTLS,
	})

	if rec := env.post(t, env.body("A"), nil); rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if recorder.source != AnswerSourceRemote {
		t.Fatalf("expected answer source %q, got %q", AnswerSourceRemote, recorder.source)
	}

	// 等 Interact 收尾，避免后端在用例结束时还挂着请求。
	if result := waitInteract(t, env.resultCh); result.err != nil {
		t.Fatalf("Interact returned error: %v", result.err)
	}
}
