package remote

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"mcode/internal/remote/wire"
)

// 本文件把桌面端设置页的远程设备管理（列出全部已授权设备、撤销任意一台）开放给 Web 端。
// 桌面端对应 loadRemoteDevices / revokeRemoteDevice（internal/ui/settings_remote.go），
// 数据源是 DeviceStore（internal/remote/device_store.go），断连与清理推送由
// PairingAuth.OnRevoke 完成（internal/remote/pairing.go 的 RevokeDevice）。
//
// 与内置方法 session.revokeDevice / session.renameDevice 的分工：
// 那两条只作用于发起请求的连接自己，deviceID 取自连接身份、不接受客户端传入；
// 这里的 session.devices.revoke 面向「撤销别的设备」，因此撤销自己会被拒绝并提示改用内置方法。

const (
	// sessionDevicesListMethod 列出全部已授权设备。
	sessionDevicesListMethod = "session.devices.list"
	// sessionDevicesRevokeMethod 撤销指定设备。
	sessionDevicesRevokeMethod = "session.devices.revoke"
	// sessionDevicesChangedMethod 是设备列表变化后的广播方法名，载荷为 wire.SessionDevicesChanged。
	sessionDevicesChangedMethod = "session.devices.changed"
)

// deviceStoreProvider 是 PairingAuth 暴露设备存储的可选能力。
// Authenticator 接口本身不带设备存储，这里按可选能力断言：
// 实现不了时 session.devices.list 返回 MethodNotFound，与其它扩展方法的降级方式一致。
type deviceStoreProvider interface {
	Store() *DeviceStore
}

func init() {
	RegisterMethod(sessionDevicesListMethod, handleSessionDevicesList)
	RegisterMethod(sessionDevicesRevokeMethod, handleSessionDevicesRevoke)
}

// handleSessionDevicesList 列出全部已授权设备，并标出发起请求的这台与当前在线的设备。
func handleSessionDevicesList(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	if !checkEmptyParams(params) {
		return nil, ExtInvalidParams("Invalid params: 'session.devices.list' takes no parameters")
	}
	provider, ok := d.auth.(deviceStoreProvider)
	if !ok || provider.Store() == nil {
		return nil, ExtNotImplemented()
	}

	currentID := c.Info().DeviceID
	online := d.onlineDeviceIDs()
	records := provider.Store().AllRecords()
	devices := make([]wire.SessionDeviceEntry, 0, len(records))
	for _, rec := range records {
		devices = append(devices, wire.SessionDeviceEntry{
			DeviceID:    rec.DeviceID,
			Label:       rec.Label,
			PairedAt:    deviceExtTime(rec.PairedAt),
			LastSeenAt:  deviceExtTime(rec.LastSeenAt),
			Entry:       rec.LastEntry,
			Platform:    devicePlatformName(rec.UserAgent),
			UserAgent:   rec.UserAgent,
			LastAddress: rec.LastAddress,
			Online:      deviceOnline(online, rec.DeviceID),
			Current:     currentID != "" && rec.DeviceID == currentID,
		})
	}
	return wire.SessionDevicesListResult{Devices: devices}, nil
}

// handleSessionDevicesRevoke 撤销指定设备：撤销自己会被拒绝，未知设备返回资源不存在。
// 真正的断连由 auth.RevokeDevice 触发的 OnRevoke 完成，与桌面端撤销走同一条路径。
func handleSessionDevicesRevoke(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.SessionDevicesRevokeParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	deviceID := strings.TrimSpace(p.DeviceID)
	if !isValidUUID(deviceID) {
		return nil, ExtInvalidParams("Invalid params: 'deviceID' must be a UUID")
	}
	if deviceID == c.Info().DeviceID {
		return nil, ExtInvalidParams("Invalid params: cannot revoke the current device, use session.revokeDevice")
	}

	revoked, err := d.auth.RevokeDevice(deviceID)
	if err != nil {
		return nil, &ExtError{Code: wire.ErrCodeOperationRejected, Message: err.Error()}
	}
	if !revoked {
		return nil, &ExtError{Code: wire.ErrCodeResourceNotFound, Message: "device not found: " + deviceID}
	}

	// 列表变化后广播一次，其它设备据此刷新；被撤销设备自己的连接已在 OnRevoke 里关闭。
	d.NotifyAuthed(sessionDevicesChangedMethod, wire.SessionDevicesChanged{DeviceID: deviceID, Revoked: true})
	return wire.SessionDevicesRevokeResult{DeviceID: deviceID, Revoked: true}, nil
}

// onlineDeviceIDs 按已认证连接聚合在线设备 ID。
// Dispatcher 不持有 Server，这里以「有已认证连接挂着」作为在线判定。
func (d *Dispatcher) onlineDeviceIDs() map[string]struct{} {
	online := make(map[string]struct{})
	d.authedMu.RLock()
	for c := range d.authedConns {
		info := c.Info()
		if info.Authenticated && info.DeviceID != "" {
			online[info.DeviceID] = struct{}{}
		}
	}
	d.authedMu.RUnlock()
	return online
}

func deviceOnline(online map[string]struct{}, deviceID string) bool {
	_, ok := online[deviceID]
	return ok
}

// deviceExtTime 把时间转成协议里的 RFC3339（UTC）字符串；零值返回空串，便于前端区分「从未活动」。
func deviceExtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// devicePlatformName 从 UA 粗略归纳平台，仅用于设备列表展示；识别不出时返回空串。
// 顺序有讲究：先判 iPad/iPhone/Android，再判 Linux，避免 Android 被当成 Linux。
func devicePlatformName(ua string) string {
	switch {
	case ua == "":
		return ""
	case strings.Contains(ua, "iPad"):
		return "iPad"
	case strings.Contains(ua, "iPhone"):
		return "iPhone"
	case strings.Contains(ua, "Android"):
		return "Android"
	case strings.Contains(ua, "Windows"):
		return "Windows"
	case strings.Contains(ua, "Macintosh") || strings.Contains(ua, "Mac OS X"):
		return "Mac"
	case strings.Contains(ua, "Linux"):
		return "Linux"
	}
	return ""
}
