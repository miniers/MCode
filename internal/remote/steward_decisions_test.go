package remote

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

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
	if len(open) != 2 || len(open[1].Options) != 1 || open[1].Options[0].Key != "A" || open[1].Options[0].Text != "选项" {
		t.Fatalf("unexpected open decisions: %+v", open)
	}
	if open[0].Options == nil || len(open[0].Options) != 0 || open[0].AnsweredAt != nil {
		t.Fatalf("unexpected malformed options or answeredAt: %+v", open[0])
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
