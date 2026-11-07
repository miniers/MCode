package remote

import (
	"errors"
	"time"
)

type PairResult struct {
	SessionToken string
	DeviceID     string
	Label        string
}

type PairingInfo struct {
	PairingURL          string
	LANPairingURL       string
	TLSPairingURL       string // 独立的 TLS 入口链接，主链接被 Tailscale 占用时设置页仍可展示
	TailscalePairingURL string
	ShortCode           string // 6 位数字授权码，与 PairingURL 中的 token 同时有效、同时过期、一次性
	ExpiresAt           time.Time
}

var ErrInvalidToken = errors.New("remote: invalid token")

type Authenticator interface {
	Pair(token, deviceName string, info ClientInfo) (PairResult, error)
	Authenticate(token string, info ClientInfo) (deviceID, label string, err error)
	NewPairingCode() (PairingInfo, error)
	RevokeDevice(deviceID string) (revoked bool, err error)
	// RenameDevice 更新指定设备的显示名称。
	RenameDevice(deviceID, name string) error
}
