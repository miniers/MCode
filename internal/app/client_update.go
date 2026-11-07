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
	return mcodeui.AppUpdateStatus{Current: AppVersion, Latest: info.Latest, URL: info.URL, Available: info.Available}
}

// CheckForUpdate 复用 updater.go 的 UpdateChecker 检查是否有新版本。
// UpdateFeedURL 为空时返回错误「未配置更新源」，且不发起任何网络请求。
func (c *Client) CheckForUpdate(ctx context.Context) (mcodeui.AppUpdateStatus, error) {
	status := mcodeui.AppUpdateStatus{Current: AppVersion}
	if UpdateFeedURL == "" {
		return status, errors.New("未配置更新源")
	}

	checker := &UpdateChecker{
		FeedURL:        UpdateFeedURL,
		CurrentVersion: AppVersion,
	}
	if c != nil && c.httpClient != nil {
		checker.Client = c.httpClient
	}

	info, err := checker.Check(ctx)
	if err != nil {
		return status, err
	}

	status.Latest = info.Latest
	status.URL = info.URL
	status.Available = info.Available
	return status, nil
}
