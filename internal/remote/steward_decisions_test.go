package remote

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

type decisionAnswerDesk struct {
	id, answer, by string
	err            error
}

func TestStewardDecisionAnswerDispatcher(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	desk := &decisionAnswerDesk{}
	b.SetStewardDesk(desk)
	d, _ := newMemoryExtTestDispatcher(t, b)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	request := `{"jsonrpc":"2.0","method":"steward.decisions.answer","params":{"id":"D-0001","answer":"A"},"id":1}`
	resp := callExtension(t, d, conn, request)
	if resp.Error != nil || desk.answer != "A" {
		t.Fatalf("答卡失败: %+v", resp.Error)
	}
	resp = callExtension(t, d, &fakeConn{}, request)
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeUnauthorized {
		t.Fatalf("未认证未拒绝: %+v", resp.Error)
	}
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"capabilities.get","params":{},"id":2}`)
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var caps wire.Capabilities
	if err := json.Unmarshal(raw, &caps); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, method := range caps.Methods {
		if method == "steward.decisions.answer" {
			found = true
		}
	}
	if !found {
		t.Fatal("capabilities 缺少答卡方法")
	}
	params, err := json.Marshal(map[string]string{"id": "D-0001", "answer": strings.Repeat("a", maxStewardAnswerRunes+1)})
	if err != nil {
		t.Fatal(err)
	}
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"steward.decisions.answer","params":`+string(params)+`,"id":3}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("超长回答未拒绝: %+v", resp.Error)
	}
	for _, n := range []int{maxStewardAnswerRunes, maxStewardAnswerRunes + 1} {
		params, err := json.Marshal(map[string]string{"id": "D-0001", "answer": strings.Repeat("汉", n)})
		if err != nil {
			t.Fatal(err)
		}
		resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"steward.decisions.answer","params":`+string(params)+`,"id":4}`)
		if n == maxStewardAnswerRunes && (resp.Error != nil || desk.answer != strings.Repeat("汉", n)) {
			t.Fatalf("有效长度回答未接受: %+v", resp.Error)
		}
		if n > maxStewardAnswerRunes && (resp.Error == nil || resp.Error.Code != wire.InvalidParams) {
			t.Fatalf("超长回答未拒绝: %+v", resp.Error)
		}
	}
}

func (*decisionAnswerDesk) IsFeed(string) bool               { return true }
func (*decisionAnswerDesk) Enqueue(string, string, []string) {}
func (d *decisionAnswerDesk) AnswerStewardDecision(_ context.Context, id, answer, by string) error {
	d.id, d.answer, d.by = id, answer, by
	return d.err
}

func TestStewardDecisionAnswer(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	d, _ := newMemoryExtTestDispatcher(t, b)
	params := json.RawMessage(`{"id":"D-0001","answer":"A"}`)
	_, err := handleStewardDecisionsAnswer(context.Background(), d, nil, params)
	if e, ok := err.(*ExtError); !ok || e.Code != wire.MethodNotFound {
		t.Fatalf("未接前台: %v", err)
	}
	desk := &decisionAnswerDesk{}
	b.SetStewardDesk(desk)
	result, err := handleStewardDecisionsAnswer(context.Background(), d, nil, params)
	if err != nil || result == nil || desk.id != "D-0001" || desk.answer != "A" || desk.by != "remote" {
		t.Fatalf("回答未送达: %+v %v %+v", result, err, desk)
	}
	for _, tc := range []struct {
		err  error
		code int
	}{
		{store.ErrDecisionClosed, wire.ErrCodeOperationRejected},
		{store.ErrNotFound, wire.ErrCodeResourceNotFound},
	} {
		desk.err = tc.err
		_, err = handleStewardDecisionsAnswer(context.Background(), d, nil, params)
		if e, ok := err.(*ExtError); !ok || e.Code != tc.code {
			t.Fatalf("错误映射 %v: %v", tc.err, err)
		}
	}
}

func TestStewardDecisionsListRPC(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "decisions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	answeredAt := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	for _, decision := range []store.StewardDecision{
		{Question: "一", OptionsJSON: `[{"key":"A","text":"选项"}]`, CreatedAt: answeredAt},
		{Question: "二", OptionsJSON: `无效`, CreatedAt: answeredAt.Add(time.Second)},
		{Question: "三", Status: store.DecisionStatusAnswered, Answer: "A", AnsweredAt: &answeredAt, CreatedAt: answeredAt.Add(2 * time.Second)},
		{Question: "四", CreatedAt: answeredAt.Add(-time.Second)},
	} {
		if _, err := st.CreateStewardDecision(ctx, decision); err != nil {
			t.Fatal(err)
		}
	}
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	if err := backend.AttachStore(ctx, st); err != nil {
		t.Fatal(err)
	}
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	call := func(params string) stewardDecisionsListResult {
		t.Helper()
		resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"steward.decisions.list","params":`+params+`,"id":1}`)
		if resp.Error != nil {
			t.Fatalf("unexpected error: %+v", resp.Error)
		}
		raw, err := json.Marshal(resp.Result)
		if err != nil {
			t.Fatal(err)
		}
		var result stewardDecisionsListResult
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	open := call(`{"status":"open"}`).Decisions
	if len(open) != 3 || len(open[1].Options) != 1 || open[1].Options[0].Key != "A" || open[1].Options[0].Text != "选项" {
		t.Fatalf("unexpected open decisions: %+v", open)
	}
	if open[0].Options == nil || len(open[0].Options) != 0 || open[0].AnsweredAt != nil {
		t.Fatalf("unexpected malformed options or answeredAt: %+v", open[0])
	}
	openLimited := call(`{"status":"open","limit":1}`)
	if len(openLimited.Decisions) != 1 || openLimited.Total != 3 {
		t.Fatalf("unexpected open total: %+v", openLimited)
	}
	limited := call(`{"limit":1}`).Decisions
	if len(limited) != 1 || limited[0].AnsweredAt == nil || *limited[0].AnsweredAt != "2026-11-08T10:00:00Z" {
		t.Fatalf("unexpected limited decisions: %+v", limited)
	}
	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"steward.decisions.list","params":{"status":"invalid"},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected invalid params, got %+v", resp.Error)
	}
}
