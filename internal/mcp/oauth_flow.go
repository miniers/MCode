package mcp

const (
	// AuthModeAuto 表示按服务器元数据自动选择登录方式（等价于未声明）。
	AuthModeAuto = "auto"
	// AuthModeLoopback 表示 OAuth 2.1 授权码 + PKCE，回调走本机 loopback 端口。
	AuthModeLoopback = "loopback"
	// AuthModeDevice 表示 RFC 8628 设备授权流程，适合无浏览器或无法打开本地端口的场景。
	AuthModeDevice = "device"
)

// AuthHint 是调用方（目录条目、UI）给登录流程的提示，不参与持久化。
type AuthHint struct {
	// Mode 为目录声明的登录方式：auto / loopback / device；空或未知按 auto 处理。
	Mode string
	// ClientID 非空且等于 CIMD 文档地址时才允许 CIMD，否则直接走 DCR。
	ClientID string
	// Scopes 非空时作为本次请求的 scope，覆盖服务器元数据。
	Scopes []string
	// StepUp 预留给第二阶段：强制交互式授权并合并待补 scope。
	StepUp bool
}

// chooseAuthFlow 按方案 §2.1 的优先级选择登录方式：
// 目录显式声明 > 服务器只支持设备流 > 默认 loopback。
// 两个端点都没有时返回空串，由调用方报 ErrOAuthDiscoveryFailed。
//
// 设计取舍：元数据同时发布两个端点时保持 loopback，避免既有用户的手感变化与
// 测试语义回归；设备流只在目录显式声明、或服务器只支持设备流时启用。
func chooseAuthFlow(meta *authServerMetadata, hint AuthHint) string {
	switch hint.Mode {
	case AuthModeDevice:
		return AuthModeDevice
	case AuthModeLoopback:
		return AuthModeLoopback
	}
	if meta == nil {
		return ""
	}
	if meta.DeviceAuthorizationEndpoint != "" && meta.AuthorizationEndpoint == "" {
		return AuthModeDevice
	}
	if meta.AuthorizationEndpoint != "" {
		return AuthModeLoopback
	}
	return ""
}
