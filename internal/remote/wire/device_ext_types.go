package wire

// 设备管理（session.devices.*）的远程扩展方法参数与结果类型。
// 这些方法由 internal/remote/device_ext_methods.go 注册：
//   - session.devices.list 列出本机全部已授权设备；
//   - session.devices.revoke 撤销指定设备，成功后推送 session.devices.changed。
//
// 与桌面端设置页的远程设备列表同源（internal/remote/device_store.go 的 DeviceRecord），
// 时间统一转成 RFC3339（UTC）字符串；userAgent 与最近地址只用于展示。
// 任何凭据材料（tokenHash / sessionToken / 推送密钥）都不出现在协议里。

// SessionDevicesListParams 对应 session.devices.list 请求参数（必须为空对象）。
type SessionDevicesListParams struct{}

// SessionDeviceEntry 是一台已授权设备的展示信息。
// PairedAt 必定有值；LastSeenAt 为空串表示该设备还没有活动记录。
type SessionDeviceEntry struct {
	DeviceID string `json:"deviceID"`
	Label    string `json:"label"`
	// PairedAt 是配对时间（RFC3339 UTC）。
	PairedAt string `json:"pairedAt"`
	// LastSeenAt 是最近一次通过鉴权的活动时间（RFC3339 UTC），未知时为空串。
	LastSeenAt string `json:"lastSeenAt"`
	// Entry 是该设备最近使用的入口：lan、tls、tailscale；未知时省略。
	Entry string `json:"entry,omitempty"`
	// Platform 是从 UA 归纳出的平台（iPhone、Android、Mac 等），识别不出时省略。
	Platform string `json:"platform,omitempty"`
	// UserAgent 是最近一次上报的浏览器标识，只用于展示。
	UserAgent string `json:"userAgent,omitempty"`
	// LastAddress 是最近一次连接的对端地址，只用于展示。
	LastAddress string `json:"lastAddress,omitempty"`
	// Online 表示当前是否有已认证连接挂着这台设备。
	Online bool `json:"online"`
	// Current 表示这台设备就是发起本次请求的设备。
	Current bool `json:"current"`
}

// SessionDevicesListResult 对应 session.devices.list 成功返回，按配对时间升序。
type SessionDevicesListResult struct {
	Devices []SessionDeviceEntry `json:"devices"`
}

// SessionDevicesRevokeParams 对应 session.devices.revoke 请求参数。
type SessionDevicesRevokeParams struct {
	DeviceID string `json:"deviceID"`
}

// SessionDevicesRevokeResult 对应 session.devices.revoke 成功返回。
type SessionDevicesRevokeResult struct {
	DeviceID string `json:"deviceID"`
	Revoked  bool   `json:"revoked"`
}

// SessionDevicesChanged 是 session.devices.changed 通知载荷，收到后重新拉一次列表。
type SessionDevicesChanged struct {
	DeviceID string `json:"deviceID"`
	Revoked  bool   `json:"revoked"`
}
