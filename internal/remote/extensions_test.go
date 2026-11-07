package remote

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"mcode/internal/remote/wire"
)

// testEchoService 是服务注册表往返测试用的载荷类型。
type testEchoService struct {
	Prefix string
}

// init 里注册测试用扩展方法，用法与真实扩展（xxx_methods.go）一致。
func init() {
	RegisterMethod("test.ext.echo", handleTestEcho)
}

// handleTestEcho 覆盖成功、*ExtError、普通错误、非法参数与取服务这几条路径。
func handleTestEcho(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p struct {
		Text string `json:"text"`
		Mode string `json:"mode"`
	}
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	switch p.Mode {
	case "extErr":
		return nil, &ExtError{Code: wire.ErrCodeOperationRejected, Message: "echo rejected"}
	case "plainErr":
		return nil, errors.New("echo exploded")
	case "notImplemented":
		return nil, ExtNotImplemented()
	}
	prefix := ""
	if svc, ok := backendService[*testEchoService](d.backend, "test.echoService"); ok {
		prefix = svc.Prefix
	}
	return map[string]any{"text": prefix + p.Text}, nil
}

// newExtensionTestDispatcher 构造一个带 fakeBackend 的 Dispatcher，capabilities 用真实方法清单。
func newExtensionTestDispatcher() *Dispatcher {
	return NewDispatcher(&fakeAuth{}, &fakeBackend{
		capabilitiesFunc: func(ctx context.Context) (wire.Capabilities, error) {
			return wire.Capabilities{
				SchemaVersion: 1,
				Methods:       ImplementedMethods(),
				Features:      DefaultFeatures(),
			}, nil
		},
	})
}

// callExtension 发一条请求并解码响应。
func callExtension(t *testing.T, d *Dispatcher, c Conn, raw string) wire.Response {
	t.Helper()
	out := d.Handle(context.Background(), c, []byte(raw))
	var resp wire.Response
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return resp
}

func TestExtensionMethodDispatch(t *testing.T) {
	d := newExtensionTestDispatcher()
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	// 1. 成功返回
	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"test.ext.echo","params":{"text":"hi"},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if !strings.Contains(string(raw), `"hi"`) {
		t.Fatalf("unexpected result: %s", raw)
	}

	// 2. *ExtError 映射成对应错误码
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"test.ext.echo","params":{"mode":"extErr"},"id":2}`)
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeOperationRejected {
		t.Fatalf("expected ExtError code %d, got: %+v", wire.ErrCodeOperationRejected, resp.Error)
	}
	if resp.Error.Message != "echo rejected" {
		t.Fatalf("unexpected ExtError message: %q", resp.Error.Message)
	}

	// 3. ExtNotImplemented 走 MethodNotFound
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"test.ext.echo","params":{"mode":"notImplemented"},"id":3}`)
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("expected MethodNotFound, got: %+v", resp.Error)
	}

	// 4. 普通错误归为内部错误
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"test.ext.echo","params":{"mode":"plainErr"},"id":4}`)
	if resp.Error == nil || resp.Error.Code != wire.InternalError {
		t.Fatalf("expected InternalError, got: %+v", resp.Error)
	}

	// 5. 参数非法（未知字段被 decodeStrict 拒绝）返回 InvalidParams
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"test.ext.echo","params":{"unknown":1},"id":5}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams, got: %+v", resp.Error)
	}
	if !strings.HasPrefix(resp.Error.Message, "Invalid params: ") {
		t.Fatalf("unexpected InvalidParams message: %q", resp.Error.Message)
	}

	// 6. 未注册的扩展方法仍是 MethodNotFound
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"test.ext.missing","params":{},"id":6}`)
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("expected MethodNotFound for unknown method, got: %+v", resp.Error)
	}
}

func TestExtensionUnauthenticated(t *testing.T) {
	d := newExtensionTestDispatcher()
	conn := &fakeConn{info: ClientInfo{Authenticated: false}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"test.ext.echo","params":{"text":"hi"},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeUnauthorized {
		t.Fatalf("expected unauthorized, got: %+v", resp.Error)
	}
}

func TestExtensionMethodInCapabilities(t *testing.T) {
	d := newExtensionTestDispatcher()
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"capabilities.get","params":{},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("capabilities.get failed: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal capabilities: %v", err)
	}
	var caps wire.Capabilities
	if err := json.Unmarshal(raw, &caps); err != nil {
		t.Fatalf("unmarshal capabilities: %v", err)
	}
	count := 0
	for _, m := range caps.Methods {
		if m == "test.ext.echo" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("extension method should appear exactly once in capabilities, got %d: %v", count, caps.Methods)
	}
	if !d.methods["test.ext.echo"] {
		t.Fatal("dispatcher route table should contain extension method")
	}
}

func TestExtensionServiceRegistry(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	if got := backend.Service("test.echoService"); got != nil {
		t.Fatalf("service should be absent, got: %+v", got)
	}
	if _, ok := backendService[*testEchoService](backend, "test.echoService"); ok {
		t.Fatal("backendService should miss unregistered service")
	}

	backend.SetService("test.echoService", &testEchoService{Prefix: "> "})
	if got, ok := backend.Service("test.echoService").(*testEchoService); !ok || got.Prefix != "> " {
		t.Fatalf("Service roundtrip failed: %+v", got)
	}
	svc, ok := backendService[*testEchoService](backend, "test.echoService")
	if !ok || svc.Prefix != "> " {
		t.Fatalf("backendService roundtrip failed: ok=%v svc=%+v", ok, svc)
	}
	// 类型不符时返回 false，而不是 panic。
	if _, ok := backendService[string](backend, "test.echoService"); ok {
		t.Fatal("backendService should reject mismatched type")
	}

	// 扩展方法通过 backendService 取到注入的服务。
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}
	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"test.ext.echo","params":{"text":"hi"},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if !strings.Contains(string(raw), `\u003e hi`) && !strings.Contains(string(raw), `> hi`) {
		t.Fatalf("result should carry service prefix: %s", raw)
	}

	backend.SetService("test.echoService", nil)
	if got := backend.Service("test.echoService"); got != nil {
		t.Fatalf("service should be removed, got: %+v", got)
	}
}

func TestRegisterMethodRejectsBuiltinAndDuplicates(t *testing.T) {
	cases := []struct {
		name string
		fn   func()
	}{
		{"builtin", func() { RegisterMethod("capabilities.get", handleTestEcho) }},
		{"duplicate", func() { RegisterMethod("test.ext.echo", handleTestEcho) }},
		{"empty", func() { RegisterMethod("  ", handleTestEcho) }},
		{"nilHandler", func() { RegisterMethod("test.ext.nil", nil) }},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s: RegisterMethod should panic", tc.name)
				}
			}()
			tc.fn()
		})
	}
}

func TestNotifyAuthed(t *testing.T) {
	d := newExtensionTestDispatcher()
	rec := &recordingConn{info: ClientInfo{Authenticated: true}}
	d.addAuthedConn(rec)
	d.NotifyAuthed("test.ext.changed", map[string]any{"ok": true})
	if len(rec.notifications) != 1 || rec.notifications[0] != "test.ext.changed" {
		t.Fatalf("unexpected notifications: %+v", rec.notifications)
	}
}

// recordingConn 记录 Notify 收到的通知方法名。
type recordingConn struct {
	info          ClientInfo
	notifications []string
}

func (c *recordingConn) Info() ClientInfo { return c.info }

func (c *recordingConn) MarkAuthenticated(deviceID, label string) {
	c.info.Authenticated = true
	c.info.DeviceID = deviceID
	c.info.Label = label
}

func (c *recordingConn) Notify(method string, params any) error {
	c.notifications = append(c.notifications, method)
	return nil
}

func (c *recordingConn) Close() error { return nil }
