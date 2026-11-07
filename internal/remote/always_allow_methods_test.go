package remote

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"mcode/internal/remote/wire"
)

// alwaysAllowTestProject 是一个合法项目 UUID，用来覆盖参数校验之后的成功路径。
const alwaysAllowTestProject = "33333333-3333-3333-3333-333333333333"

// newAlwaysAllowTestDispatcher 构造带 MemoryBackend 的 Dispatcher，并把数据目录指向临时目录，
// 这样 ListAlwaysAllowed / RevokeAlwaysAllowed 走真实的持久化存储。
func newAlwaysAllowTestDispatcher(t *testing.T) (*Dispatcher, *MemoryBackend, string) {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)

	dir := t.TempDir()
	backend.SetDataDir(dir)
	// 存储实例按数据目录缓存，测试结束丢弃，避免后续用例读到这份数据。
	t.Cleanup(func() { forgetAlwaysAllowStore(dir) })
	return NewDispatcher(&fakeAuth{}, backend), backend, dir
}

func TestRequestsAlwaysAllowedListRejectsBadParams(t *testing.T) {
	d, _, _ := newAlwaysAllowTestDispatcher(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	cases := []struct {
		name string
		raw  string
	}{
		{"missingProjectID", `{"jsonrpc":"2.0","method":"requests.alwaysAllowed.list","params":{},"id":1}`},
		{"notUUID", `{"jsonrpc":"2.0","method":"requests.alwaysAllowed.list","params":{"projectID":"proj-1"},"id":2}`},
		{"unknownField", `{"jsonrpc":"2.0","method":"requests.alwaysAllowed.list","params":{"projectID":"` + alwaysAllowTestProject + `","extra":true},"id":3}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			resp := callExtension(t, d, conn, tc.raw)
			if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
				t.Fatalf("expected InvalidParams, got: %+v", resp.Error)
			}
		})
	}
}

func TestRequestsAlwaysAllowedWithoutBackendSupport(t *testing.T) {
	// fakeBackend 没有 ListAlwaysAllowed / RevokeAlwaysAllowed，等价于后端不支持撤销。
	d := newExtensionTestDispatcher()
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	for _, raw := range []string{
		`{"jsonrpc":"2.0","method":"requests.alwaysAllowed.list","params":{"projectID":"` + alwaysAllowTestProject + `"},"id":1}`,
		`{"jsonrpc":"2.0","method":"requests.alwaysAllowed.revoke","params":{"projectID":"` + alwaysAllowTestProject + `","key":"Bash(git status)"},"id":2}`,
	} {
		resp := callExtension(t, d, conn, raw)
		if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
			t.Fatalf("expected MethodNotFound, got: %+v", resp.Error)
		}
	}
}

func TestRequestsAlwaysAllowedRevokeRequiresKey(t *testing.T) {
	d, _, _ := newAlwaysAllowTestDispatcher(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"requests.alwaysAllowed.revoke","params":{"projectID":"`+alwaysAllowTestProject+`","key":"  "},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for blank key, got: %+v", resp.Error)
	}
}

func TestRequestsAlwaysAllowedListAndRevoke(t *testing.T) {
	d, backend, dir := newAlwaysAllowTestDispatcher(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	projectPath := t.TempDir()
	backend.AddProject(wire.ProjectSummary{
		ID:   alwaysAllowTestProject,
		Name: "演示项目",
		Path: projectPath,
	})
	// 存储层按「项目路径」记账（会话有工作目录时），也允许退回「项目 ID」，两种键都要能列出。
	store := alwaysAllowStoreFor(dir)
	if err := store.Add(projectPath, "Bash(git status)"); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if err := store.Add(alwaysAllowTestProject, "Write(internal/**)"); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	// Add 会把项目键规范化（解析符号链接），撤销与断言都用规范化后的值。
	projectKey := normalizeAlwaysAllowProject(projectPath)
	if projectKey == "" {
		t.Fatal("normalized project key should not be empty")
	}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"requests.alwaysAllowed.list","params":{"projectID":"`+alwaysAllowTestProject+`"},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	listed := string(raw)
	for _, want := range []string{"Bash(git status)", "Write(internal/**)"} {
		if !strings.Contains(listed, want) {
			t.Fatalf("list result %s should contain %s", listed, want)
		}
	}

	// 撤销按项目路径记的那条，返回的 keys 里就不再有它。
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"requests.alwaysAllowed.revoke","params":{"projectID":"`+alwaysAllowTestProject+`","key":"Bash(git status)"},"id":2}`)
	if resp.Error != nil {
		t.Fatalf("unexpected revoke error: %+v", resp.Error)
	}
	raw, err = json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	after := string(raw)
	if strings.Contains(after, "Bash(git status)") {
		t.Fatalf("revoked key should disappear, got: %s", after)
	}
	if !strings.Contains(after, "Write(internal/**)") {
		t.Fatalf("other keys should survive, got: %s", after)
	}
	if got := store.List(projectKey); len(got) != 0 {
		t.Fatalf("store should drop revoked key, got: %v", got)
	}

	// 全撤光后列表为空数组而不是 null，前端可以直接遍历。
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"requests.alwaysAllowed.revoke","params":{"projectID":"`+alwaysAllowTestProject+`","key":"Write(internal/**)"},"id":3}`)
	if resp.Error != nil {
		t.Fatalf("unexpected revoke error: %+v", resp.Error)
	}
	raw, err = json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if strings.Contains(string(raw), "Write(internal/**)") {
		t.Fatalf("revoked key should disappear, got: %s", raw)
	}
	if !strings.Contains(string(raw), `"keys":[]`) {
		t.Fatalf("empty list should serialize as []: %s", raw)
	}
}

// TestAlwaysAllowProjectKeysFallsBackToProjectID 覆盖项目列表取不到路径时仍能按项目 ID 撤销。
func TestAlwaysAllowProjectKeysFallsBackToProjectID(t *testing.T) {
	d := newExtensionTestDispatcher()
	ctx := context.Background()
	keys := alwaysAllowProjectKeys(ctx, d, alwaysAllowTestProject)
	if len(keys) != 1 || keys[0] != alwaysAllowTestProject {
		t.Fatalf("expected project id fallback, got: %v", keys)
	}
}
