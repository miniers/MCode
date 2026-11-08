package app

import (
	"context"
	"errors"

	mcodeui "mcode/internal/ui"
)

// AppVersion 返回当前应用版本（可在构建时通过 -ldflags -X 覆盖包变量 AppVersion）。
func (c *Client) AppVersion() string {
	return AppVersion
}

// LatestUpdate 返回后台最近一次检查结果；尚未检查时返回零值。
func (c *Client) LatestUpdate() mcodeui.AppUpdateStatus {
	if c == nil || c.updateChecker == nil {
		return mcodeui.AppUpdateStatus{}
	}
	info := c.updateChecker.Latest()
	if info == (UpdateInfo{}) {
		return mcodeui.AppUpdateStatus{}
	}
	return mcodeui.AppUpdateStatus{Current: AppVersion, Latest: info.Latest, URL: info.URL, Available: info.Available, Installable: info.Installable}
}

// UpdateNotes 返回最近一次检查结果中的发布说明。
func (c *Client) UpdateNotes() string {
	if c == nil || c.updateChecker == nil {
		return ""
	}
	return c.updateChecker.Latest().Notes
}

// CheckForUpdate 复用 updater.go 的 UpdateChecker 检查是否有新版本。
// 未配置更新源且原生更新不可用时返回错误「未配置更新源」。
func (c *Client) CheckForUpdate(ctx context.Context) (mcodeui.AppUpdateStatus, error) {
	status := mcodeui.AppUpdateStatus{Current: AppVersion}
	var checker *UpdateChecker
	if c != nil {
		checker = c.updateChecker
	}
	if checker == nil {
		checker = &UpdateChecker{FeedURL: UpdateFeedURL, CurrentVersion: AppVersion}
		if c != nil && c.httpClient != nil {
			checker.Client = c.httpClient
		}
	}
	if checker.FeedURL == "" && !nativeUpdatesEnabled() {
		return status, errors.New("未配置更新源")
	}

	info, err := checker.Check(ctx)
	if err != nil {
		return status, err
	}

	status.Latest = info.Latest
	status.URL = info.URL
	status.Available = info.Available
	status.Installable = info.Installable
	return status, nil
}

// InstallUpdate 安装待处理的更新。
func (c *Client) InstallUpdate(ctx context.Context, progress func(float64)) error {
	if c == nil || c.updateChecker == nil {
		return errors.New("没有可安装的更新")
	}
	return c.updateChecker.Install(ctx, progress)
}
