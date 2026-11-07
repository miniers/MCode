package remote

import (
	"context"
	"encoding/json"
	"testing"

	"mcode/internal/remote/wire"
)

// fakeSettingsExtService 是注入用的假设置服务，记录调用参数方便断言。
type fakeSettingsExtService struct {
	snapshot wire.SettingsSnapshot
	err      error

	getCalls  int
	setCalls  int
	setValues map[string]json.RawMessage
}

func (f *fakeSettingsExtService) Get(ctx context.Context) (wire.SettingsSnapshot, error) {
	f.getCalls++
	if f.err != nil {
		return wire.SettingsSnapshot{}, f.err
	}
	return f.snapshot, nil
}

func (f *fakeSettingsExtService) Set(ctx context.Context, values map[string]json.RawMessage) (wire.SettingsSnapshot, error) {
	f.setCalls++
	f.setValues = values
	if f.err != nil {
		return wire.SettingsSnapshot{}, f.err
	}
	return f.snapshot, nil
}

// settingsResultFromResponse 把 handler 的 any 结果折回协议类型，方便逐字段断言。
func settingsResultFromResponse(t *testing.T, resp wire.Response) wire.SettingsSnapshot {
	t.Helper()
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal settings result: %v", err)
	}
	var snapshot wire.SettingsSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatalf("unmarshal settings result: %v", err)
	}
	return snapshot
}

// writableKeys 一律用本地白名单，服务给的顺序或内容都不能改变可写集合；
// 服务多带的键（例如刚加的敏感字段）也不许透给客户端。
func TestSettingsGetUsesLocalWritableKeys(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeSettingsExtService{snapshot: wire.SettingsSnapshot{
		Values: map[string]json.RawMessage{
			SettingsKeyPermissionsMode: json.RawMessage(`"supervised"`),
			"providers.claude.api_key": json.RawMessage(`"sk-secret"`),
		},
		WritableKeys: []string{"providers.claude.api_key"},
	}}
	backend.SetService(settingsExtServiceName, svc)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"settings.get","params":{},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	snapshot := settingsResultFromResponse(t, resp)
	if len(snapshot.WritableKeys) != len(SettingsWritableKeys) {
		t.Fatalf("writableKeys 数 = %d, 期望 %d", len(snapshot.WritableKeys), len(SettingsWritableKeys))
	}
	for i, key := range SettingsWritableKeys {
		if snapshot.WritableKeys[i] != key {
			t.Fatalf("writableKeys[%d] = %q, 期望 %q", i, snapshot.WritableKeys[i], key)
		}
		if !IsSettingsWritableKey(key) {
			t.Fatalf("白名单里的 %q 没被 IsSettingsWritableKey 认出", key)
		}
	}
	if _, ok := snapshot.Values["providers.claude.api_key"]; ok {
		t.Fatalf("白名单外的值不该透给客户端: %v", snapshot.Values)
	}
	if string(snapshot.Values[SettingsKeyPermissionsMode]) != `"supervised"` {
		t.Fatalf("权限档位值不对：%s", snapshot.Values[SettingsKeyPermissionsMode])
	}
	if snapshot.QuickReplies == nil {
		t.Fatalf("快捷回复应为空数组而不是 null")
	}
	if svc.getCalls != 1 {
		t.Fatalf("get 调用次数 = %d, 期望 1", svc.getCalls)
	}
}

// 非白名单键整体拒绝，服务连一次都调不到。
func TestSettingsSetRejectsNonWritableKey(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeSettingsExtService{}
	backend.SetService(settingsExtServiceName, svc)
	d, rec := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"settings.set","params":{"values":{"permissions.mode":"supervised","providers.claude.api_key":"sk-secret"}},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("非白名单键应返回 InvalidParams，得到 %+v", resp.Error)
	}
	if svc.setCalls != 0 {
		t.Fatalf("非白名单键不该调用服务，setCalls = %d", svc.setCalls)
	}
	if len(rec.notifications) != 0 {
		t.Fatalf("拒绝时不该广播，得到 %+v", rec.notifications)
	}
}

// 参数里出现未知字段要拒绝，避免客户端把拼错的键当成已生效。
func TestSettingsSetRejectsUnknownField(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeSettingsExtService{}
	backend.SetService(settingsExtServiceName, svc)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"settings.set","params":{"values":{"permissions.mode":"supervised"},"dryRun":true},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("未知字段应返回 InvalidParams，得到 %+v", resp.Error)
	}
	if svc.setCalls != 0 {
		t.Fatalf("未知字段不该调用服务，setCalls = %d", svc.setCalls)
	}
}

func TestSettingsSetRejectsEmptyValues(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeSettingsExtService{}
	backend.SetService(settingsExtServiceName, svc)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"settings.set","params":{"values":{}},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("空 values 应返回 InvalidParams，得到 %+v", resp.Error)
	}
	if svc.setCalls != 0 {
		t.Fatalf("空 values 不该调用服务，setCalls = %d", svc.setCalls)
	}
}

// 服务未注入时两个方法都返回 NotImplemented。
func TestSettingsMethodsNotImplemented(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"settings.get","params":{},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("未注入服务时 settings.get 应返回 NotImplemented，得到 %+v", resp.Error)
	}

	resp = callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"settings.set","params":{"values":{"permissions.mode":"supervised"}},"id":2}`)
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("未注入服务时 settings.set 应返回 NotImplemented，得到 %+v", resp.Error)
	}
}

// 写入成功后按统一形状广播 settings.changed，载荷就是回写后的完整值。
func TestSettingsSetBroadcastsChanged(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeSettingsExtService{snapshot: wire.SettingsSnapshot{
		Values: map[string]json.RawMessage{SettingsKeyOrchestrationMaxBees: json.RawMessage(`5`)},
	}}
	backend.SetService(settingsExtServiceName, svc)
	d, rec := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"settings.set","params":{"values":{"orchestration.max_bees":5}},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if svc.setCalls != 1 || string(svc.setValues[SettingsKeyOrchestrationMaxBees]) != `5` {
		t.Fatalf("服务收到的值与预期不符：calls=%d values=%v", svc.setCalls, svc.setValues)
	}
	if len(rec.notifications) != 1 {
		t.Fatalf("应广播一条通知，得到 %+v", rec.notifications)
	}
	n := rec.notifications[0]
	if n.method != settingsExtChangedMethod {
		t.Fatalf("广播方法名 = %q, 期望 %q", n.method, settingsExtChangedMethod)
	}
	payload, ok := n.params.(wire.SettingsChangedNotification)
	if !ok {
		t.Fatalf("广播载荷类型 = %T, 期望 wire.SettingsChangedNotification", n.params)
	}
	if string(payload.Values[SettingsKeyOrchestrationMaxBees]) != `5` {
		t.Fatalf("广播载荷不是快照值：%v", payload.Values)
	}
}
