// 此文件包含 Client 的远程控制方法。
package app

import (
	"context"
	"os"
	"strings"
	"time"

	"mcode/internal/config"
	mcodeui "mcode/internal/ui"
)

// RemoteStatus 获取远程控制服务的运行状态（桌面专用，不走 RPC）。
func (c *Client) RemoteStatus(ctx context.Context) (mcodeui.RemoteStatus, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return mcodeui.RemoteStatus{State: mcodeui.RemoteStateStopped}, ErrRemoteNotRunning
	}
	return r.Status(), nil
}

// SetRemoteEnabled 开启或关闭远程控制服务（桌面专用，不走 RPC）。
func (c *Client) SetRemoteEnabled(ctx context.Context, enabled bool) (mcodeui.RemoteStatus, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return mcodeui.RemoteStatus{State: mcodeui.RemoteStateStopped}, ErrRemoteNotRunning
	}

	if enabled {
		cfg, err := config.Load(r.dataDir)
		if err != nil {
			return r.Status(), err
		}
		config.ApplyEnv(&cfg, os.LookupEnv)
		if err := r.Start(cfg.Remote.Listen, cfg.Remote.ExtraHosts); err != nil {
			return r.Status(), err
		}
		return r.Status(), nil
	}

	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := r.Stop(stopCtx); err != nil {
		return r.Status(), err
	}
	return r.Status(), nil
}

// RemotePairing 获取或换发远程控制配对信息（桌面专用，不走 RPC）。
func (c *Client) RemotePairing(ctx context.Context, refresh bool) (mcodeui.RemotePairing, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return mcodeui.RemotePairing{}, ErrRemoteNotRunning
	}

	// 先取状态再取配对信息：CurrentPairingURLs 会自动换码，不能用于状态判断。
	state := r.PairingState()
	info, err := r.Pairing(refresh)
	if err != nil {
		return mcodeui.RemotePairing{}, err
	}
	if refresh {
		state = r.PairingState()
	}
	return mcodeui.RemotePairing{
		PairingURL:          info.PairingURL,
		LANPairingURL:       info.LANPairingURL,
		TLSPairingURL:       info.TLSPairingURL,
		TailscalePairingURL: info.TailscalePairingURL,
		ShortCode:           info.ShortCode,
		ExpiresAt:           info.ExpiresAt,
		State:               state,
	}, nil
}

// RemoteDevices 获取所有已授权的远程设备列表（桌面专用，不走 RPC）。
func (c *Client) RemoteDevices(ctx context.Context) ([]mcodeui.RemoteDevice, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return nil, ErrRemoteNotRunning
	}
	return r.Devices(), nil
}

// RevokeRemoteDevice 撤销指定的远程设备（桌面专用，不走 RPC）。
func (c *Client) RevokeRemoteDevice(ctx context.Context, deviceID string) error {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return ErrRemoteNotRunning
	}
	return r.RevokeDevice(deviceID)
}

// RenameRemoteDevice 重命名指定的远程设备（桌面专用，不走 RPC）。
func (c *Client) RenameRemoteDevice(ctx context.Context, deviceID, name string) error {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return ErrRemoteNotRunning
	}
	return r.Rename(deviceID, name)
}

// RevokeAllRemoteDevices 撤销全部远程设备（桌面专用，不走 RPC）。
func (c *Client) RevokeAllRemoteDevices(ctx context.Context) error {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return ErrRemoteNotRunning
	}
	return r.RevokeAll()
}

// RestartRemote 使用当前配置重启远程控制服务（桌面专用，不走 RPC）。
func (c *Client) RestartRemote(ctx context.Context) (mcodeui.RemoteStatus, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return mcodeui.RemoteStatus{State: mcodeui.RemoteStateStopped}, ErrRemoteNotRunning
	}
	return r.Restart(ctx)
}

// SendRemoteTestPush 通过远程控制服务向所有已登记的 Web Push 订阅发一条测试通知（桌面专用，不走 RPC）。
func (c *Client) SendRemoteTestPush(ctx context.Context) (int, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return 0, ErrRemoteNotRunning
	}
	return r.SendTestPush()
}

// RemoteTLSStatus 获取远程控制 TLS 证书与监听状态（桌面专用，不走 RPC）。
func (c *Client) RemoteTLSStatus(ctx context.Context) (mcodeui.RemoteTLSStatus, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return mcodeui.RemoteTLSStatus{}, ErrRemoteNotRunning
	}
	return r.TLSStatus(), nil
}

// SaveRemoteCloudflareToken 保存或清除 Cloudflare API Token（桌面专用，不走 RPC）。
func (c *Client) SaveRemoteCloudflareToken(token string) error {
	store := config.NewSecretStore("mcode")
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return store.Delete("cloudflare_api_token")
	}
	return store.Set("cloudflare_api_token", trimmed)
}

// HasRemoteCloudflareToken 查询是否已保存 Cloudflare API Token（桌面专用，不走 RPC）。
func (c *Client) HasRemoteCloudflareToken() bool {
	return hasRemoteCloudflareToken()
}

// IssueRemoteTLSCertificate 立即签发远程控制 TLS 证书（桌面专用，不走 RPC）。
func (c *Client) IssueRemoteTLSCertificate(ctx context.Context) (mcodeui.RemoteTLSStatus, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return mcodeui.RemoteTLSStatus{}, ErrRemoteNotRunning
	}
	return r.IssueTLSCertificate(ctx)
}

// ImportRemoteTLSCertificate 导入外部 TLS 证书与私钥（桌面专用，不走 RPC）。
func (c *Client) ImportRemoteTLSCertificate(ctx context.Context, chainPath, keyPath string) (mcodeui.RemoteTLSStatus, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return mcodeui.RemoteTLSStatus{}, ErrRemoteNotRunning
	}
	return r.ImportTLSCertificate(ctx, chainPath, keyPath)
}

// RemoveRemoteTLSCertificate 移除已配置的 TLS 证书（桌面专用，不走 RPC）。
func (c *Client) RemoveRemoteTLSCertificate(ctx context.Context) error {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return ErrRemoteNotRunning
	}
	return r.RemoveTLSCertificate(ctx)
}

// RemoteTailscaleStatus 获取内嵌 Tailscale 入口的状态（桌面专用，不走 RPC）。
func (c *Client) RemoteTailscaleStatus(ctx context.Context) (mcodeui.RemoteTailscaleStatus, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return mcodeui.RemoteTailscaleStatus{}, ErrRemoteNotRunning
	}
	return r.TailscaleStatus(), nil
}

// SetRemoteTailscaleEnabled 在当前运行中的远程服务上启用或停用 Tailscale 入口（桌面专用，不走 RPC）。
func (c *Client) SetRemoteTailscaleEnabled(ctx context.Context, enabled bool) (mcodeui.RemoteTailscaleStatus, error) {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return mcodeui.RemoteTailscaleStatus{}, ErrRemoteNotRunning
	}
	return r.SetTailscaleEnabled(ctx, enabled)
}

// ResetRemoteTailscale 停止 Tailscale 入口并删除其状态目录（桌面专用，不走 RPC）。
func (c *Client) ResetRemoteTailscale(ctx context.Context) error {
	c.kanbanMu.Lock()
	r := c.remote
	c.kanbanMu.Unlock()
	if r == nil {
		return ErrRemoteNotRunning
	}
	return r.ResetTailscale(ctx)
}
