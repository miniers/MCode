package remote

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

// fakeMCPExtService 记录调用参数并返回预置结果。
type fakeMCPExtService struct {
	result    wire.MCPExtListServersResult
	err       error
	calls     int
	gotThread string
	gotProj   string
}

func (f *fakeMCPExtService) ListServers(ctx context.Context, threadID, projectID string) (wire.MCPExtListServersResult, error) {
	f.calls++
	f.gotThread = threadID
	f.gotProj = projectID
	if f.err != nil {
		return wire.MCPExtListServersResult{}, f.err
	}
	return f.result, nil
}

func newMCPExtTestBackend(t *testing.T) *MemoryBackend {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	backend.SetDataDir(t.TempDir())
	return backend
}

// mcpExtResult 断言响应无错误并解码结果。
func mcpExtResult[T any](t *testing.T, resp wire.Response) T {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return out
}

func TestMCPExtListServers(t *testing.T) {
	backend := newMCPExtTestBackend(t)
	threadID := newUUID()
	svc := &fakeMCPExtService{result: wire.MCPExtListServersResult{
		Servers: []wire.MCPExtServer{
			{Key: "claude:project:fs", Name: "fs", Agent: "claude", Scope: "project", Disabled: true},
			{Key: "pi:home:web", Name: "web", Agent: "pi", Scope: "home", Source: "/tmp/mcp.json", Locked: true, LockReason: "全局已停用"},
		},
	}}
	backend.SetService(MCPExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"mcp.servers.list","params":{"threadID":"`+threadID+`"},"id":1}`)
	result := mcpExtResult[wire.MCPExtListServersResult](t, resp)
	if svc.calls != 1 || svc.gotThread != threadID || svc.gotProj != "" {
		t.Fatalf("unexpected service args: calls=%d thread=%q project=%q", svc.calls, svc.gotThread, svc.gotProj)
	}
	if result.ThreadID != threadID {
		t.Fatalf("threadID not echoed: %+v", result)
	}
	if len(result.Servers) != 2 {
		t.Fatalf("unexpected servers: %+v", result.Servers)
	}
	if !result.Servers[0].Disabled || result.Servers[1].Locked != true || result.Servers[1].LockReason != "全局已停用" {
		t.Fatalf("disabled/locked flags lost: %+v", result.Servers)
	}
	if len(svc.result.Servers) != 2 {
		t.Fatalf("service result mutated: %+v", svc.result.Servers)
	}
}

// 只给 projectID 时也要能列清单（禁用状态全 false，由服务侧决定）。
func TestMCPExtListServersByProject(t *testing.T) {
	backend := newMCPExtTestBackend(t)
	projectID := newUUID()
	svc := &fakeMCPExtService{result: wire.MCPExtListServersResult{
		ProjectID: projectID,
		Servers:   []wire.MCPExtServer{{Key: "pi:home:web", Name: "web", Agent: "pi", Scope: "home"}},
	}}
	backend.SetService(MCPExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"mcp.servers.list","params":{"projectID":"`+projectID+`"},"id":1}`)
	result := mcpExtResult[wire.MCPExtListServersResult](t, resp)
	if svc.gotThread != "" || svc.gotProj != projectID {
		t.Fatalf("unexpected service args: thread=%q project=%q", svc.gotThread, svc.gotProj)
	}
	if result.ProjectID != projectID || len(result.Servers) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

// 空结果要回 []，不能是 null，客户端才能直接遍历。
func TestMCPExtListServersEmptyResult(t *testing.T) {
	backend := newMCPExtTestBackend(t)
	svc := &fakeMCPExtService{}
	backend.SetService(MCPExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"mcp.servers.list","params":{"threadID":"`+newUUID()+`"},"id":1}`)
	result := mcpExtResult[wire.MCPExtListServersResult](t, resp)
	if !serversIsJSONArray(t, result) {
		t.Fatalf("servers should be an empty array: %+v", result)
	}
}

// serversIsJSONArray 判断序列化后的 servers 字段是否为数组（而不是 null 或缺失）。
func serversIsJSONArray(t *testing.T, result wire.MCPExtListServersResult) bool {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var probe struct {
		Servers []wire.MCPExtServer `json:"servers"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return probe.Servers != nil
}

func TestMCPExtListServersRejectsInvalidParams(t *testing.T) {
	backend := newMCPExtTestBackend(t)
	svc := &fakeMCPExtService{}
	backend.SetService(MCPExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	cases := []struct {
		name   string
		params string
	}{
		{"unknownField", `{"threadID":"` + newUUID() + `","projectID":"` + newUUID() + `","extra":1}`},
		{"missingBoth", `{}`},
		{"badThreadID", `{"threadID":"t-1"}`},
		{"badProjectID", `{"projectID":"proj-1"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := callExtension(t, d, conn,
				`{"jsonrpc":"2.0","method":"mcp.servers.list","params":`+tc.params+`,"id":1}`)
			if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
				t.Fatalf("expected InvalidParams, got: %+v", resp.Error)
			}
		})
	}
	if svc.calls != 0 {
		t.Fatalf("service should not be called on invalid params: %d", svc.calls)
	}
}

func TestMCPExtListServersWithoutService(t *testing.T) {
	backend := newMCPExtTestBackend(t)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"mcp.servers.list","params":{"threadID":"`+newUUID()+`"},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("expected MethodNotFound, got: %+v", resp.Error)
	}
}

// 线程不存在时服务返回后端错误，dispatcher 要原样保留 ResourceNotFound 码。
func TestMCPExtListServersThreadNotFound(t *testing.T) {
	backend := newMCPExtTestBackend(t)
	svc := &fakeMCPExtService{err: &wire.RPCError{Code: wire.ErrCodeResourceNotFound, Message: "thread not found"}}
	backend.SetService(MCPExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"mcp.servers.list","params":{"threadID":"`+newUUID()+`"},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeResourceNotFound {
		t.Fatalf("expected ResourceNotFound, got: %+v", resp.Error)
	}
}
