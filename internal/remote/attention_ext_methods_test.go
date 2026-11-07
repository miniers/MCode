package remote

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"mcode/internal/remote/wire"
)

// fakeAttentionExtService 是注入用的假待处理收件箱服务，记录调用参数方便断言。
type fakeAttentionExtService struct {
	items      []wire.AttentionExtItem
	count      int
	listParams wire.AttentionExtListParams
	listCalls  int
	resolvedID string
	// 确认建议时收到的三项参数，未调用时保持空字符串。
	confirmedThreadID  string
	confirmedRequestID string
	confirmedOptionID  string
}

func (f *fakeAttentionExtService) List(ctx context.Context, params wire.AttentionExtListParams) (wire.AttentionExtListResult, error) {
	f.listCalls++
	f.listParams = params
	return wire.AttentionExtListResult{Items: f.items}, nil
}

func (f *fakeAttentionExtService) Resolve(ctx context.Context, id string) error {
	f.resolvedID = id
	return nil
}

func (f *fakeAttentionExtService) UnresolvedCount(ctx context.Context) (int, error) {
	return f.count, nil
}

func (f *fakeAttentionExtService) ConfirmSuggestion(ctx context.Context, threadID, requestID, optionID string) error {
	f.confirmedThreadID = threadID
	f.confirmedRequestID = requestID
	f.confirmedOptionID = optionID
	return nil
}

func TestAttentionList_DefaultsAndLimit(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeAttentionExtService{count: 3}
	backend.SetService(attentionExtServiceName, svc)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	cases := []struct {
		params string
		want   int
	}{
		{`{}`, attentionExtDefaultLimit},
		{`{"limit":-5}`, attentionExtDefaultLimit},
		{`{"limit":20}`, 20},
		{`{"limit":9999}`, attentionExtMaxLimit},
	}
	for _, tc := range cases {
		resp := callExtension(t, d, conn,
			`{"jsonrpc":"2.0","method":"attention.list","params":`+tc.params+`,"id":1}`)
		if resp.Error != nil {
			t.Fatalf("%s: unexpected error: %+v", tc.params, resp.Error)
		}
		if svc.listParams.Limit != tc.want {
			t.Fatalf("%s: limit = %d, 期望 %d", tc.params, svc.listParams.Limit, tc.want)
		}
		if svc.listParams.IncludeResolved {
			t.Fatalf("%s: 缺省应只列未处理事件", tc.params)
		}
	}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"attention.list","params":{"projectID":"p-1","includeResolved":true},"id":2}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if svc.listParams.ProjectID != "p-1" || !svc.listParams.IncludeResolved {
		t.Fatalf("参数未透传：%+v", svc.listParams)
	}
}

func TestAttentionList_EmptyAndNullResolvedAt(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeAttentionExtService{count: 0}
	backend.SetService(attentionExtServiceName, svc)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	// 服务返回 nil 时输出空数组，客户端不必处理 null。
	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"attention.list","params":{},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if string(raw) != `{"items":[],"unresolvedCount":0}` {
		t.Fatalf("空列表应序列化为 []，得到 %s", raw)
	}

	// 未处理事件的 resolvedAt 输出 null，而不是省略字段。
	svc.items = []wire.AttentionExtItem{{ID: "a-1", Kind: "thread_failed", EnteredAt: "2026-10-30T00:00:00Z"}}
	svc.count = 1
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"attention.list","params":{},"id":2}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err = json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if !strings.Contains(string(raw), `"resolvedAt":null`) || !strings.Contains(string(raw), `"unresolvedCount":1`) {
		t.Fatalf("resolvedAt 应为 null 且带未处理数，得到 %s", raw)
	}
}

func TestAttentionResolve(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeAttentionExtService{}
	backend.SetService(attentionExtServiceName, svc)
	d, rec := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"attention.resolve","params":{"id":"  "},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("空 id 应返回 InvalidParams，得到 %+v", resp.Error)
	}
	if svc.resolvedID != "" {
		t.Fatalf("空 id 不该调用服务，得到 %q", svc.resolvedID)
	}

	resp = callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"attention.resolve","params":{"id":"a-1"},"id":2}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if string(raw) != `{"ok":true}` || svc.resolvedID != "a-1" {
		t.Fatalf("resolve 结果不对：%s，id=%q", raw, svc.resolvedID)
	}
	// 推送统一由订阅侧发出，handler 不自己广播。
	if len(rec.notifications) != 0 {
		t.Fatalf("resolve 不该直接广播，得到 %+v", rec.notifications)
	}
}

func TestAttentionConfirmSuggestion(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeAttentionExtService{}
	backend.SetService(attentionExtServiceName, svc)
	d, rec := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	// 三项参数缺一不可，缺了就不调服务。
	missing := []string{
		`{"requestID":"r-1","optionID":"allow"}`,
		`{"threadID":"t-1","optionID":"allow"}`,
		`{"threadID":"t-1","requestID":"r-1"}`,
		`{"threadID":"  ","requestID":"r-1","optionID":"allow"}`,
	}
	for _, params := range missing {
		resp := callExtension(t, d, conn,
			`{"jsonrpc":"2.0","method":"attention.confirmSuggestion","params":`+params+`,"id":1}`)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
			t.Fatalf("%s 应返回 InvalidParams，得到 %+v", params, resp.Error)
		}
	}
	if svc.confirmedRequestID != "" {
		t.Fatalf("参数缺失时不该调用服务，得到 %q", svc.confirmedRequestID)
	}

	// 成功：参数去空格后透传，返回 ok，推送由订阅侧发出。
	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"attention.confirmSuggestion",`+
			`"params":{"threadID":" t-1 ","requestID":" r-1 ","optionID":" allow "},"id":2}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if string(raw) != `{"ok":true}` {
		t.Fatalf("confirmSuggestion 结果不对：%s", raw)
	}
	if svc.confirmedThreadID != "t-1" || svc.confirmedRequestID != "r-1" || svc.confirmedOptionID != "allow" {
		t.Fatalf("参数未去空格透传：%q %q %q", svc.confirmedThreadID, svc.confirmedRequestID, svc.confirmedOptionID)
	}
	if len(rec.notifications) != 0 {
		t.Fatalf("confirmSuggestion 不该直接广播，得到 %+v", rec.notifications)
	}
}

// 审批事件的 suggestions 有值时序列化，为空时省略字段。
func TestAttentionList_Suggestions(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeAttentionExtService{count: 2}
	backend.SetService(attentionExtServiceName, svc)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	svc.items = []wire.AttentionExtItem{
		{
			ID: "a-1", Kind: "thread_approval", ThreadID: "t-1", EnteredAt: "2026-10-30T00:00:00Z",
			Suggestions: []wire.AttentionExtSuggestion{{
				RequestID: "r-1", Kind: "command", Title: "执行命令", Detail: "rm -rf build",
				OptionID: "allow", Reason: "只删构建产物",
			}},
		},
		{ID: "a-2", Kind: "thread_failed", ThreadID: "t-2", EnteredAt: "2026-10-30T00:00:00Z"},
	}
	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"attention.list","params":{},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result wire.AttentionExtListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Items) != 2 || len(result.Items[0].Suggestions) != 1 || len(result.Items[1].Suggestions) != 0 {
		t.Fatalf("建议条数不对：%+v", result.Items)
	}
	sug := result.Items[0].Suggestions[0]
	if sug.RequestID != "r-1" || sug.Kind != "command" || sug.Title != "执行命令" ||
		sug.Detail != "rm -rf build" || sug.OptionID != "allow" || sug.Reason != "只删构建产物" {
		t.Fatalf("建议字段映射不对：%+v", sug)
	}

	// 没有建议的条目不输出该字段。
	items, ok := resp.Result.(map[string]any)["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("结果结构不对：%#v", resp.Result)
	}
	if _, present := items[0].(map[string]any)["suggestions"]; !present {
		t.Fatal("有建议时应输出 suggestions 字段")
	}
	if _, present := items[1].(map[string]any)["suggestions"]; present {
		t.Fatalf("没有建议时应省略 suggestions 字段：%#v", items[1])
	}
}

func TestAttention_NoService(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	requests := []string{
		`{"jsonrpc":"2.0","method":"attention.list","params":{},"id":1}`,
		`{"jsonrpc":"2.0","method":"attention.resolve","params":{"id":"a-1"},"id":2}`,
		`{"jsonrpc":"2.0","method":"attention.confirmSuggestion","params":{"threadID":"t-1","requestID":"r-1","optionID":"allow"},"id":3}`,
	}
	for _, request := range requests {
		resp := callExtension(t, d, conn, request)
		if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
			t.Fatalf("未注入服务时应返回 MethodNotFound，得到 %+v（%s）", resp.Error, request)
		}
	}
}

func TestAttention_ImplementedMethods(t *testing.T) {
	methods := make(map[string]bool)
	for _, m := range ImplementedMethods() {
		methods[m] = true
	}
	for _, want := range []string{"attention.list", "attention.resolve", "attention.confirmSuggestion"} {
		if !methods[want] {
			t.Errorf("ImplementedMethods 应包含 %q", want)
		}
	}
}

func TestNotifyAttentionChanged(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	d, rec := newMemoryExtTestDispatcher(t, backend)

	d.NotifyAttentionChanged(7)
	if len(rec.notifications) != 1 {
		t.Fatalf("应广播一条通知，得到 %+v", rec.notifications)
	}
	got := rec.notifications[0]
	if got.method != "attention.changed" {
		t.Fatalf("通知方法名不对：%q", got.method)
	}
	payload, ok := got.params.(wire.AttentionExtChangedNotification)
	if !ok || payload.UnresolvedCount != 7 {
		t.Fatalf("通知载荷不对：%#v", got.params)
	}
}
