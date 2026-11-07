package agy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"mcode/internal/harness"
)

const testBridgeToken = "test-token"

// fakeQuestionApprover 实现 harness.Approver 与 harness.Interactor，记录收到的审批请求并返回预设答复。
type fakeQuestionApprover struct {
	interactReq harness.ApprovalRequest
	interactCnt int
	resp        harness.ApprovalResponse
	err         error

	approveCnt int
	acked      bool
	ackErr     error
}

func (f *fakeQuestionApprover) Approve(_ context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	f.approveCnt++
	f.interactReq = req
	if f.err != nil {
		return harness.DecisionDeny, f.err
	}
	return harness.DecisionAllow, nil
}

func (f *fakeQuestionApprover) Interact(_ context.Context, req harness.ApprovalRequest) (harness.ApprovalResponse, error) {
	f.interactCnt++
	f.interactReq = req
	return f.resp, f.err
}

func (f *fakeQuestionApprover) AckDelivery(_ harness.ApprovalRequest, err error) {
	f.acked = true
	f.ackErr = err
}

func newTestBridge(approver harness.Approver) *QuestionBridge {
	return &QuestionBridge{
		token:    testBridgeToken,
		approver: approver,
		ctx:      context.Background(),
	}
}

func postQuestion(t *testing.T, b *QuestionBridge, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/question", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testBridgeToken)
	rec := httptest.NewRecorder()
	b.handleQuestion(rec, req)

	var payload map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("响应不是合法 JSON: %v (body=%q)", err, rec.Body.String())
		}
	}
	return rec, payload
}

func reasonOf(t *testing.T, payload map[string]any) string {
	t.Helper()
	reason, _ := payload["reason"].(string)
	return reason
}

func singleQuestionBody(questionJSON string) string {
	return `{"toolCall":{"name":"ask_question","args":{"questions":[` + questionJSON + `]}}}`
}

func TestHandleQuestion_SingleSingleSelect(t *testing.T) {
	fake := &fakeQuestionApprover{
		resp: harness.ApprovalResponse{
			Decision: harness.DecisionAllow,
			Answers:  map[string][]string{"Pick one": {"B"}},
		},
	}
	bridge := newTestBridge(fake)

	body := singleQuestionBody(`{"question":"Pick one","options":["A","B"]}`)
	rec, payload := postQuestion(t, bridge, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际 %d", rec.Code)
	}
	if payload["decision"] != "deny" {
		t.Errorf("期望 decision=deny，实际 %v", payload["decision"])
	}
	if got := reasonOf(t, payload); !strings.Contains(got, `{"answers":{"Pick one":"B"}}`) {
		t.Errorf("reason 未包含单选答案，实际 %q", got)
	}

	if fake.interactCnt != 1 {
		t.Fatalf("期望 Interact 调用 1 次，实际 %d", fake.interactCnt)
	}
	if fake.approveCnt != 0 {
		t.Errorf("期望不调用 Approve，实际 %d 次", fake.approveCnt)
	}
	if !fake.acked {
		t.Errorf("期望调用 AckDelivery 回报送达")
	}

	req := fake.interactReq
	if req.Kind != harness.RequestKindQuestion {
		t.Errorf("期望 Kind=question，实际 %q", req.Kind)
	}
	if req.ToolName != "ask_question" {
		t.Errorf("期望 ToolName=ask_question，实际 %q", req.ToolName)
	}
	if req.ToolID == "" {
		t.Errorf("期望 ToolID 非空")
	}
	if string(req.Input) != body {
		t.Errorf("期望 Input 为原始请求体，实际 %q", string(req.Input))
	}
	if len(req.Questions) != 1 {
		t.Fatalf("期望 1 道题，实际 %d", len(req.Questions))
	}

	q := req.Questions[0]
	if q.Prompt != "Pick one" {
		t.Errorf("期望 Prompt=Pick one，实际 %q", q.Prompt)
	}
	if q.ID != "Pick one" {
		t.Errorf("期望 ID=Pick one，实际 %q", q.ID)
	}
	if q.AllowsMultiple {
		t.Errorf("期望 AllowsMultiple=false")
	}
	if !q.AllowsOther {
		t.Errorf("期望 AllowsOther=true")
	}
	wantChoices := []harness.QuestionChoice{{Label: "A"}, {Label: "B"}}
	if !reflect.DeepEqual(q.Choices, wantChoices) {
		t.Errorf("期望 Choices=%+v，实际 %+v", wantChoices, q.Choices)
	}
}

func TestHandleQuestion_MultipleQuestions(t *testing.T) {
	fake := &fakeQuestionApprover{
		resp: harness.ApprovalResponse{
			Decision: harness.DecisionAllow,
			Answers:  map[string][]string{"First?": {"yes"}, "Second?": {"no"}},
		},
	}
	bridge := newTestBridge(fake)

	body := `{"questions":[{"question":"First?","options":["yes","no"]},{"question":"Second?","options":["yes","no"]}]}`
	rec, payload := postQuestion(t, bridge, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际 %d", rec.Code)
	}
	reason := reasonOf(t, payload)
	if !strings.Contains(reason, `"First?":"yes"`) || !strings.Contains(reason, `"Second?":"no"`) {
		t.Errorf("reason 未包含两题答案，实际 %q", reason)
	}

	if len(fake.interactReq.Questions) != 2 {
		t.Fatalf("期望 2 道题，实际 %d", len(fake.interactReq.Questions))
	}
	if got := fake.interactReq.Questions[0].Header; got != "Question 1" {
		t.Errorf("期望第一题 Header=Question 1，实际 %q", got)
	}
	if got := fake.interactReq.Questions[1].Header; got != "Question 2" {
		t.Errorf("期望第二题 Header=Question 2，实际 %q", got)
	}
}

func TestHandleQuestion_MultiSelect(t *testing.T) {
	fake := &fakeQuestionApprover{
		resp: harness.ApprovalResponse{
			Decision: harness.DecisionAllow,
			Answers:  map[string][]string{"Pick many": {"A", "C"}},
		},
	}
	bridge := newTestBridge(fake)

	body := singleQuestionBody(`{"question":"Pick many","options":["A","B","C"],"is_multi_select":true}`)
	rec, payload := postQuestion(t, bridge, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际 %d", rec.Code)
	}
	if !strings.Contains(reasonOf(t, payload), `{"answers":{"Pick many":["A","C"]}}`) {
		t.Errorf("reason 未包含数组形式的多选答案，实际 %q", reasonOf(t, payload))
	}

	q := fake.interactReq.Questions[0]
	if !q.AllowsMultiple {
		t.Errorf("期望 AllowsMultiple=true")
	}
}

func TestHandleQuestion_MultiSelectCamelKey(t *testing.T) {
	fake := &fakeQuestionApprover{resp: harness.ApprovalResponse{Decision: harness.DecisionAllow}}
	bridge := newTestBridge(fake)

	body := singleQuestionBody(`{"question":"Q","allowsMultipleSelections":true}`)
	postQuestion(t, bridge, body)

	if !fake.interactReq.Questions[0].AllowsMultiple {
		t.Errorf("期望 allowsMultipleSelections 被识别为多选")
	}
}

func TestHandleQuestion_ApproverDenyWithFeedback(t *testing.T) {
	fake := &fakeQuestionApprover{
		resp: harness.ApprovalResponse{
			Decision: harness.DecisionDeny,
			Feedback: "user rejected the question",
		},
	}
	bridge := newTestBridge(fake)

	rec, payload := postQuestion(t, bridge, singleQuestionBody(`{"question":"Q","options":["A"]}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际 %d", rec.Code)
	}
	if payload["decision"] != "deny" {
		t.Errorf("期望 decision=deny，实际 %v", payload["decision"])
	}
	if got := reasonOf(t, payload); got != "user rejected the question" {
		t.Errorf("期望 reason 为拒绝反馈原文，实际 %q", got)
	}
	if !fake.acked {
		t.Errorf("期望调用 AckDelivery 回报送达")
	}
}

func TestHandleQuestion_Skipped(t *testing.T) {
	fake := &fakeQuestionApprover{
		resp: harness.ApprovalResponse{
			Decision: harness.DecisionDeny,
			Skipped:  true,
		},
	}
	bridge := newTestBridge(fake)

	rec, payload := postQuestion(t, bridge, singleQuestionBody(`{"question":"Q","options":["A"]}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际 %d", rec.Code)
	}
	if got := reasonOf(t, payload); got != "User skipped question." {
		t.Errorf("期望 reason=User skipped question.，实际 %q", got)
	}
}

func TestHandleQuestion_ApproverError(t *testing.T) {
	fake := &fakeQuestionApprover{err: errors.New("boom")}
	bridge := newTestBridge(fake)

	rec, payload := postQuestion(t, bridge, singleQuestionBody(`{"question":"Q","options":["A"]}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际 %d", rec.Code)
	}
	if payload["decision"] != "deny" {
		t.Errorf("期望 decision=deny，实际 %v", payload["decision"])
	}
	if got := reasonOf(t, payload); !strings.Contains(got, "boom") {
		t.Errorf("期望 reason 包含错误信息，实际 %q", got)
	}
	if !fake.acked || fake.ackErr == nil {
		t.Errorf("期望 AckDelivery 收到非空错误")
	}
}

func TestHandleQuestion_InvalidJSON(t *testing.T) {
	fake := &fakeQuestionApprover{resp: harness.ApprovalResponse{Decision: harness.DecisionAllow}}
	bridge := newTestBridge(fake)

	rec, payload := postQuestion(t, bridge, `{"toolCall": not-json`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("期望状态码 400，实际 %d", rec.Code)
	}
	if payload["decision"] != "deny" {
		t.Errorf("期望 decision=deny，实际 %v", payload["decision"])
	}
	if got := reasonOf(t, payload); got != "Invalid JSON" {
		t.Errorf("期望 reason=Invalid JSON，实际 %q", got)
	}
	if fake.interactCnt != 0 {
		t.Errorf("非法 JSON 不应调用 Interact，实际 %d 次", fake.interactCnt)
	}
}

func TestHandleQuestion_NoQuestions(t *testing.T) {
	fake := &fakeQuestionApprover{resp: harness.ApprovalResponse{Decision: harness.DecisionAllow}}
	bridge := newTestBridge(fake)

	rec, payload := postQuestion(t, bridge, `{"toolCall":{"name":"ask_question","args":{}}}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际 %d", rec.Code)
	}
	if got := reasonOf(t, payload); got != "No questions specified." {
		t.Errorf("期望 reason=No questions specified.，实际 %q", got)
	}
	if fake.interactCnt != 0 {
		t.Errorf("无题目时不应调用 Interact，实际 %d 次", fake.interactCnt)
	}
}

func TestHandleQuestion_NonInteractorApprover(t *testing.T) {
	fake := &approveOnlyApprover{}
	bridge := newTestBridge(fake)

	rec, payload := postQuestion(t, bridge, singleQuestionBody(`{"question":"Q","options":["A"]}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际 %d", rec.Code)
	}
	if payload["decision"] != "allow" {
		t.Errorf("期望 decision=allow，实际 %v", payload["decision"])
	}
}

type approveOnlyApprover struct{}

func (approveOnlyApprover) Approve(context.Context, harness.ApprovalRequest) (harness.Decision, error) {
	return harness.DecisionAllow, nil
}

func TestExtractRawQuestions(t *testing.T) {
	nested := `{"toolCall":{"name":"ask_question","args":{"questions":[{"question":"nested"}]}}}`
	stringified := `{"arguments":"{\"questions\":[{\"question\":\"stringified\"}]}"}`
	topLevel := `{"questions":[{"question":"top"}]}`
	inputKey := `{"toolCall":{"input":{"questions":[{"question":"input"}]}}}`

	cases := []struct {
		name string
		body string
		want []map[string]any
	}{
		{name: "空对象", body: `{}`, want: nil},
		{name: "非法 JSON 结果为 nil", body: ``, want: nil},
		{name: "toolCall.args 嵌套", body: nested, want: []map[string]any{{"question": "nested"}}},
		{name: "顶层 questions", body: topLevel, want: []map[string]any{{"question": "top"}}},
		{name: "字符串形式的 arguments", body: stringified, want: []map[string]any{{"question": "stringified"}}},
		{name: "toolCall.input", body: inputKey, want: []map[string]any{{"question": "input"}}},
		{name: "questions 字段非数组", body: `{"questions":"oops"}`, want: nil},
		{name: "数组元素非对象被跳过", body: `{"questions":[1,{"question":"ok"}]}`, want: []map[string]any{{"question": "ok"}}},
		{name: "questions 为空数组", body: `{"questions":[]}`, want: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var root any
			if tc.body != "" {
				if err := json.Unmarshal([]byte(tc.body), &root); err != nil {
					t.Fatalf("用例 body 非法: %v", err)
				}
			}
			got := extractRawQuestions(root)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("extractRawQuestions 期望 %+v，实际 %+v", tc.want, got)
			}
		})
	}
}

func TestParseQuestions(t *testing.T) {
	cases := []struct {
		name string
		in   []map[string]any
		want []harness.Question
	}{
		{
			name: "字符串选项",
			in:   []map[string]any{{"question": "Q1", "options": []any{"A", "B"}}},
			want: []harness.Question{{
				ID: "Q1", Prompt: "Q1", AllowsOther: true,
				Choices: []harness.QuestionChoice{{Label: "A"}, {Label: "B"}},
			}},
		},
		{
			name: "对象选项带 description 与 header",
			in: []map[string]any{{
				"question": "Q2",
				"header":   "标题",
				"options": []any{
					map[string]any{"label": "L1", "description": "D1"},
					map[string]any{"text": "L2"},
				},
			}},
			want: []harness.Question{{
				ID: "Q2", Header: "标题", Prompt: "Q2", AllowsOther: true,
				Choices: []harness.QuestionChoice{{Label: "L1", Detail: "D1"}, {Label: "L2"}},
			}},
		},
		{
			name: "多选标志 is_multi_select",
			in:   []map[string]any{{"question": "Q3", "options": []any{"A"}, "is_multi_select": true}},
			want: []harness.Question{{
				ID: "Q3", Prompt: "Q3", AllowsMultiple: true, AllowsOther: true,
				Choices: []harness.QuestionChoice{{Label: "A"}},
			}},
		},
		{
			name: "多选标志 allows_multiple_selections",
			in:   []map[string]any{{"question": "Q4", "allows_multiple_selections": true}},
			want: []harness.Question{{
				ID: "Q4", Prompt: "Q4", AllowsMultiple: true, AllowsOther: true,
			}},
		},
		{
			name: "缺少 question 字段使用默认文案",
			in:   []map[string]any{{"options": []any{"A"}}},
			want: []harness.Question{{
				ID: "Question 1", Prompt: "Question 1", AllowsOther: true,
				Choices: []harness.QuestionChoice{{Label: "A"}},
			}},
		},
		{
			name: "重复提示词追加序号去重",
			in: []map[string]any{
				{"question": "same"},
				{"question": "same"},
			},
			want: []harness.Question{
				{ID: "same", Header: "Question 1", Prompt: "same", AllowsOther: true},
				{ID: "same-2", Header: "Question 2", Prompt: "same", AllowsOther: true},
			},
		},
		{
			name: "prompt 优先于 title",
			in:   []map[string]any{{"title": "标题兜底", "prompt": "真实题目"}},
			want: []harness.Question{{
				ID: "真实题目", Prompt: "真实题目", AllowsOther: true,
			}},
		},
		{
			name: "空选项与非字符串选项被忽略",
			in:   []map[string]any{{"question": "Q5", "options": []any{"", 42, map[string]any{"detail": "no label"}}}},
			want: []harness.Question{{ID: "Q5", Prompt: "Q5", AllowsOther: true}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseQuestions(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseQuestions 期望 %+v，实际 %+v", tc.want, got)
			}
		})
	}
}

func TestParseQuestions_EmptyInput(t *testing.T) {
	if got := parseQuestions(nil); len(got) != 0 {
		t.Errorf("期望空结果，实际 %+v", got)
	}
}
