package remote

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/remote/wire"
)

// deviceExtRecordingConn 记录收到的通知，用来断言设备列表变化会广播到其它已认证连接。
type deviceExtRecordingConn struct {
	info          ClientInfo
	notifications []string
}

func (c *deviceExtRecordingConn) Info() ClientInfo { return c.info }

func (c *deviceExtRecordingConn) MarkAuthenticated(deviceID, label string) {
	c.info.Authenticated = true
	c.info.DeviceID = deviceID
	c.info.Label = label
}

func (c *deviceExtRecordingConn) Notify(method string, params any) error {
	c.notifications = append(c.notifications, method)
	return nil
}

func (c *deviceExtRecordingConn) Close() error { return nil }

func (c *deviceExtRecordingConn) got(method string) bool {
	for _, m := range c.notifications {
		if m == method {
			return true
		}
	}
	return false
}

// newDeviceExtTestEnv 构造带真实 PairingAuth 的 Dispatcher，并配对两台设备（A 为发起请求的当前设备）。
func newDeviceExtTestEnv(t *testing.T) (*Dispatcher, *PairingAuth, PairResult, PairResult, *[]string) {
	t.Helper()

	revoked := make([]string, 0, 1)
	auth, err := NewPairingAuth(PairingAuthOptions{
		DevicesPath: filepath.Join(t.TempDir(), "remote_devices.json"),
		LANBaseURL:  func() string { return "http://192.168.1.10:43117" },
		OnRevoke:    func(deviceID string) { revoked = append(revoked, deviceID) },
	})
	if err != nil {
		t.Fatalf("NewPairingAuth failed: %v", err)
	}

	pair := func(name, ua string) PairResult {
		info, err := auth.NewPairingCode()
		if err != nil {
			t.Fatalf("NewPairingCode failed: %v", err)
		}
		parts := strings.Split(info.PairingURL, "#pair=")
		if len(parts) != 2 {
			t.Fatalf("invalid pairing URL: %s", info.PairingURL)
		}
		res, err := auth.Pair(parts[1], name, ClientInfo{
			UserAgent: ua,
			Address:   "192.168.1.50",
			Entry:     EntryLAN,
		})
		if err != nil {
			t.Fatalf("Pair(%s) failed: %v", name, err)
		}
		return res
	}

	deviceA := pair("iPhone", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)")
	deviceB := pair("MacBook", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)")

	d := NewDispatcher(auth, &fakeBackend{})
	return d, auth, deviceA, deviceB, &revoked
}

func decodeDeviceList(t *testing.T, resp wire.Response) wire.SessionDevicesListResult {
	t.Helper()
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var out wire.SessionDevicesListResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return out
}

// 列表：包含两台设备、current 只标当前这台、字段齐全且不含任何凭据材料。
func TestSessionDevicesList_NoSecretsAndCurrentFlag(t *testing.T) {
	d, auth, deviceA, deviceB, _ := newDeviceExtTestEnv(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: deviceA.DeviceID}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"session.devices.list","params":{},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	list := decodeDeviceList(t, resp)
	if len(list.Devices) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(list.Devices))
	}

	byID := make(map[string]wire.SessionDeviceEntry, len(list.Devices))
	for _, entry := range list.Devices {
		byID[entry.DeviceID] = entry
	}
	if entry, ok := byID[deviceA.DeviceID]; !ok || !entry.Current {
		t.Fatalf("device A must be marked current: %+v", entry)
	}
	if entry, ok := byID[deviceB.DeviceID]; !ok || entry.Current {
		t.Fatalf("device B must not be marked current: %+v", entry)
	}
	for _, entry := range list.Devices {
		if entry.Label == "" || entry.PairedAt == "" {
			t.Fatalf("label/pairedAt must be present: %+v", entry)
		}
		if entry.Entry != string(EntryLAN) {
			t.Fatalf("expected entry %q, got %q", string(EntryLAN), entry.Entry)
		}
		if entry.Platform == "" {
			t.Fatalf("platform should be derived from user agent: %+v", entry)
		}
	}

	// 不得返回 token 或任何密钥材料（含 tokenHash）。
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	payload := strings.ToLower(string(raw))
	if strings.Contains(payload, "token") || strings.Contains(payload, "hash") {
		t.Fatalf("device list must not expose credentials: %s", raw)
	}
	if strings.Contains(string(raw), deviceA.SessionToken) || strings.Contains(string(raw), deviceB.SessionToken) {
		t.Fatalf("device list leaked session token")
	}
	for _, id := range []string{deviceA.DeviceID, deviceB.DeviceID} {
		if rec, ok := auth.Store().FindByDeviceID(id); ok && rec.TokenHash != "" {
			if strings.Contains(string(raw), rec.TokenHash) {
				t.Fatalf("device list leaked token hash")
			}
		}
	}
}

// 撤销他人：成功、从存储移除、触发 OnRevoke（与桌面端同一条断连路径）、token 失效、广播变化。
func TestSessionDevicesRevoke_OtherDevice(t *testing.T) {
	d, auth, deviceA, deviceB, revoked := newDeviceExtTestEnv(t)

	// 设备 B 的在线连接，用来验证列表变化会广播过去。
	connB := &deviceExtRecordingConn{info: ClientInfo{Authenticated: true, DeviceID: deviceB.DeviceID}}
	d.addAuthedConn(connB)

	connA := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: deviceA.DeviceID}}
	raw := `{"jsonrpc":"2.0","method":"session.devices.revoke","params":{"deviceID":"` + deviceB.DeviceID + `"},"id":2}`
	resp := callExtension(t, d, connA, raw)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	resultRaw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result wire.SessionDevicesRevokeResult
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !result.Revoked || result.DeviceID != deviceB.DeviceID {
		t.Fatalf("unexpected revoke result: %+v", result)
	}

	if _, ok := auth.Store().FindByDeviceID(deviceB.DeviceID); ok {
		t.Fatalf("device B must be removed from the store")
	}
	if len(*revoked) != 1 || (*revoked)[0] != deviceB.DeviceID {
		t.Fatalf("OnRevoke must fire for device B, got %v", *revoked)
	}
	// OnRevoke 就是桌面端撤销走的断连入口；此处再确认凭据本身已失效。
	if _, _, err := auth.Authenticate(deviceB.SessionToken, ClientInfo{Address: "192.168.1.50", Entry: EntryLAN}); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken for revoked device, got %v", err)
	}
	if !connB.got(sessionDevicesChangedMethod) {
		t.Fatalf("expected %s broadcast, got %v", sessionDevicesChangedMethod, connB.notifications)
	}
	// 当前设备不受影响。
	if _, ok := auth.Store().FindByDeviceID(deviceA.DeviceID); !ok {
		t.Fatalf("device A must survive revoking device B")
	}
}

// 撤销自己：拒绝并提示改用 session.revokeDevice。
func TestSessionDevicesRevoke_CurrentDeviceRejected(t *testing.T) {
	d, auth, deviceA, _, revoked := newDeviceExtTestEnv(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: deviceA.DeviceID}}

	raw := `{"jsonrpc":"2.0","method":"session.devices.revoke","params":{"deviceID":"` + deviceA.DeviceID + `"},"id":3}`
	resp := callExtension(t, d, conn, raw)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams, got: %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "session.revokeDevice") {
		t.Fatalf("error should point at session.revokeDevice, got %q", resp.Error.Message)
	}
	if _, ok := auth.Store().FindByDeviceID(deviceA.DeviceID); !ok {
		t.Fatalf("current device must not be removed")
	}
	if len(*revoked) != 0 {
		t.Fatalf("OnRevoke must not fire, got %v", *revoked)
	}
}

// 未知设备：明确的资源不存在错误。
func TestSessionDevicesRevoke_UnknownDevice(t *testing.T) {
	d, _, deviceA, _, _ := newDeviceExtTestEnv(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: deviceA.DeviceID}}

	raw := `{"jsonrpc":"2.0","method":"session.devices.revoke","params":{"deviceID":"00000000-0000-4000-8000-000000000000"},"id":4}`
	resp := callExtension(t, d, conn, raw)
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeResourceNotFound {
		t.Fatalf("expected resource-not-found, got: %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "device not found") {
		t.Fatalf("unexpected error message: %q", resp.Error.Message)
	}
}

// 非法参数：非 UUID、缺字段、未知字段。
func TestSessionDevicesRevoke_InvalidParams(t *testing.T) {
	d, _, deviceA, _, _ := newDeviceExtTestEnv(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: deviceA.DeviceID}}

	cases := []string{
		`{"jsonrpc":"2.0","method":"session.devices.revoke","params":{"deviceID":"not-a-uuid"},"id":5}`,
		`{"jsonrpc":"2.0","method":"session.devices.revoke","params":{},"id":6}`,
		`{"jsonrpc":"2.0","method":"session.devices.revoke","params":{"deviceID":"` + deviceA.DeviceID + `","extra":1},"id":7}`,
	}
	for _, raw := range cases {
		resp := callExtension(t, d, conn, raw)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
			t.Fatalf("expected InvalidParams for %s, got: %+v", raw, resp.Error)
		}
	}
}

// 列表参数非法：无参方法不接受任何字段。
func TestSessionDevicesList_InvalidParams(t *testing.T) {
	d, _, deviceA, _, _ := newDeviceExtTestEnv(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: deviceA.DeviceID}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"session.devices.list","params":{"foo":1},"id":8}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams, got: %+v", resp.Error)
	}
}

// 认证实现不提供设备存储时降级为 MethodNotFound（与其它扩展方法一致）。
func TestSessionDevicesList_UnavailableWithoutStore(t *testing.T) {
	d := NewDispatcher(&fakeAuth{}, &fakeBackend{})
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"session.devices.list","params":{},"id":9}`)
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("expected MethodNotFound, got: %+v", resp.Error)
	}
}

// 两个新方法都要出现在 capabilities 的 methods 里。
func TestDeviceAndFileWriteMethodsRegistered(t *testing.T) {
	d := NewDispatcher(&fakeAuth{}, &fakeBackend{
		capabilitiesFunc: func(ctx context.Context) (wire.Capabilities, error) {
			return wire.Capabilities{
				SchemaVersion: 1,
				Methods:       ImplementedMethods(),
				Features:      DefaultFeatures(),
			}, nil
		},
	})
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"capabilities.get","params":{},"id":10}`)
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
	want := []string{sessionDevicesListMethod, sessionDevicesRevokeMethod, filesWriteMethod}
	for _, name := range want {
		found := false
		for _, m := range caps.Methods {
			if m == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("method %s missing from capabilities: %v", name, caps.Methods)
		}
	}
}
