package magpie

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// useTraceServer 把 trace 的客户端指向 srv，测试结束后还原。
func useTraceServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	restore := traceHTTPClient
	traceHTTPClient = srv.Client()
	t.Cleanup(func() { traceHTTPClient = restore })
}

const traceRespJSON = `{
	"object":"trace",
	"now":"2026-11-04T09:00:00Z",
	"seq":12,
	"routes":[{
		"seq":12,
		"id":42,
		"time":"2026-11-04T08:59:00Z",
		"agent":"claude",
		"session":"sess-1",
		"model":"claude-sonnet-5-5",
		"provider":"claude",
		"order":[{"id":"claude","provider":"claude","name":"Claude","who":"me@example.com","kind":"account","model":"claude-sonnet-5-5"}],
		"left":[{"id":"claude-2","provider":"claude","name":"Claude","who":"other@example.com","kind":"account"}],
		"tries":[{"id":"claude","model":"claude-sonnet-5-5","start":"2026-11-04T08:59:00Z","done":true,"status":200}],
		"affinity":{"mode":"session","turn":2,"last":"claude"},
		"done":true,
		"status":200
	}]
}`

func TestReadTrace(t *testing.T) {
	var gotPath, gotAfter, gotWait, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAfter = r.URL.Query().Get("after")
		gotWait = r.URL.Query().Get("wait")
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, traceRespJSON)
	}))
	defer srv.Close()
	useTraceServer(t, srv)

	snapshot, err := ReadTrace(context.Background(), srv.URL+"/v1", "gw-key", 11, true)
	if err != nil {
		t.Fatalf("ReadTrace: %v", err)
	}
	if gotPath != "/v1/magpie/trace" {
		t.Errorf("path = %q, want '/v1/magpie/trace'", gotPath)
	}
	if gotAfter != "11" {
		t.Errorf("after = %q, want '11'", gotAfter)
	}
	if gotWait != "1" {
		t.Errorf("wait = %q, want '1'", gotWait)
	}
	if gotAuth != "Bearer gw-key" {
		t.Errorf("Authorization = %q, want 'Bearer gw-key'", gotAuth)
	}
	if snapshot.Seq != 12 || len(snapshot.Routes) != 1 {
		t.Fatalf("snapshot = %+v, want seq 12 与 1 条路由", snapshot)
	}
	r := snapshot.Routes[0]
	if r.ID != 42 || r.Session != "sess-1" || r.Agent != "claude" || r.Model != "claude-sonnet-5-5" || !r.Done {
		t.Errorf("路由 = %+v", r)
	}
	if len(r.Order) != 1 || r.Order[0].Who != "me@example.com" || r.Order[0].Kind != "account" {
		t.Errorf("order = %+v", r.Order)
	}
	if len(r.Left) != 1 || r.Left[0].ID != "claude-2" || r.Left[0].Who != "other@example.com" {
		t.Errorf("left = %+v", r.Left)
	}
	if len(r.Tries) != 1 || r.Tries[0].ID != "claude" || !r.Tries[0].Done || r.Tries[0].Status != 200 {
		t.Errorf("tries = %+v", r.Tries)
	}
	if r.Affinity == nil || r.Affinity.Mode != "session" || r.Affinity.Last != "claude" {
		t.Errorf("affinity = %+v", r.Affinity)
	}
}

func TestReadTraceNoWait(t *testing.T) {
	var rawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		io.WriteString(w, `{"seq":0,"routes":[]}`)
	}))
	defer srv.Close()
	useTraceServer(t, srv)

	snapshot, err := ReadTrace(context.Background(), srv.URL, "", 0, false)
	if err != nil {
		t.Fatalf("ReadTrace: %v", err)
	}
	if rawQuery != "after=0" {
		t.Errorf("query = %q, want 'after=0'", rawQuery)
	}
	if snapshot.Seq != 0 || len(snapshot.Routes) != 0 {
		t.Errorf("snapshot = %+v, want 空", snapshot)
	}
}

func TestReadTraceForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":"magpie's routing is told to this computer alone"}`)
	}))
	defer srv.Close()
	useTraceServer(t, srv)

	_, err := ReadTrace(context.Background(), srv.URL, "gw-key", 0, true)
	if err == nil {
		t.Fatal("403 时应当报错")
	}
	if !errors.Is(err, ErrTraceForbidden) {
		t.Errorf("错误 = %v，应当可用 errors.Is 认出 ErrTraceForbidden", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("错误 = %v，想要含状态码 403", err)
	}
}

func TestReadTraceOtherStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, "bad gateway")
	}))
	defer srv.Close()
	useTraceServer(t, srv)

	_, err := ReadTrace(context.Background(), srv.URL, "", 0, false)
	if err == nil {
		t.Fatal("非 2xx 时应当报错")
	}
	if errors.Is(err, ErrTraceForbidden) {
		t.Errorf("错误 = %v，不应被当成 403", err)
	}
	if !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "bad gateway") {
		t.Errorf("错误 = %v，想要含 502 与响应片段", err)
	}
}

func TestServedAccount(t *testing.T) {
	order := []TraceWeighed{
		{ID: "claude", Provider: "claude", Who: "first@example.com", Kind: "account"},
		{ID: "claude-2", Provider: "claude", Who: "second@example.com", Kind: "account"},
	}
	left := []TraceWeighed{
		{ID: "claude-left", Provider: "claude", Who: "left@example.com", Kind: "account"},
		{ID: "key-1", Provider: "claude", Who: "my-key", Kind: "key"},
	}
	for _, tc := range []struct {
		name     string
		route    TraceRoute
		provider string
		who      string
		kind     string
		ok       bool
	}{
		{
			name: "成功 try 命中 order",
			route: TraceRoute{
				Order: order,
				Left:  left,
				Tries: []TraceTry{{ID: "claude", Done: true, Status: 200}},
			},
			provider: "claude", who: "first@example.com", kind: "account", ok: true,
		},
		{
			name: "成功 try 命中 left",
			route: TraceRoute{
				Order: order,
				Left:  left,
				Tries: []TraceTry{{ID: "claude-left", Done: true, Status: 200}},
			},
			provider: "claude", who: "left@example.com", kind: "account", ok: true,
		},
		{
			name: "取最后一条成功的 try",
			route: TraceRoute{
				Order: order,
				Tries: []TraceTry{
					{ID: "claude", Done: true, Status: 200},
					{ID: "claude-2", Done: true, Status: 500},
					{ID: "claude-2", Done: true, Status: 200},
				},
			},
			provider: "claude", who: "second@example.com", kind: "account", ok: true,
		},
		{
			name: "进行中的 2xx try 也能命中",
			route: TraceRoute{
				Order: order,
				Tries: []TraceTry{{ID: "claude", Status: 200}},
			},
			provider: "claude", who: "first@example.com", kind: "account", ok: true,
		},
		{
			name: "只有 affinity.last 命中 order",
			route: TraceRoute{
				Order:    order,
				Affinity: &TraceAffinity{Mode: "session", Last: "claude-2"},
			},
			provider: "claude", who: "second@example.com", kind: "account", ok: true,
		},
		{
			name: "只有 affinity.last 命中 left",
			route: TraceRoute{
				Order:    order,
				Left:     left,
				Affinity: &TraceAffinity{Mode: "session", Last: "claude-left"},
			},
			provider: "claude", who: "left@example.com", kind: "account", ok: true,
		},
		{
			name: "kind 为 key 时 who 是 key 名",
			route: TraceRoute{
				Left:  left,
				Tries: []TraceTry{{ID: "key-1", Done: true, Status: 200}},
			},
			provider: "claude", who: "my-key", kind: "key", ok: true,
		},
		{
			name: "进行中且没有 try 时不猜",
			route: TraceRoute{
				Order: order,
				Left:  left,
				Tries: []TraceTry{{ID: "claude"}},
			},
		},
		{
			name: "只有失败的 try 时不猜",
			route: TraceRoute{
				Order: order,
				Tries: []TraceTry{{ID: "claude", Done: true, Status: 500}},
			},
		},
		{
			name: "affinity.last 不在 order/left 时不猜",
			route: TraceRoute{
				Order:    order,
				Affinity: &TraceAffinity{Mode: "session", Last: "gone"},
			},
		},
		{
			name: "条目没有 provider 时不猜",
			route: TraceRoute{
				Order: []TraceWeighed{{ID: "bare", Who: "someone@example.com", Kind: "account"}},
				Tries: []TraceTry{{ID: "bare", Done: true, Status: 200}},
			},
		},
		{
			name:  "空路由",
			route: TraceRoute{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, who, kind, ok := ServedAccount(tc.route)
			if ok != tc.ok || provider != tc.provider || who != tc.who || kind != tc.kind {
				t.Errorf("ServedAccount = (%q, %q, %q, %v), want (%q, %q, %q, %v)",
					provider, who, kind, ok, tc.provider, tc.who, tc.kind, tc.ok)
			}
		})
	}
}
