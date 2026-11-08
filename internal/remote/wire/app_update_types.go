package wire

// AppUpdateStatus 是桌面端最近一次检查的更新状态。
type AppUpdateStatus struct {
	// Current 是当前版本。
	Current string `json:"current"`
	// Latest 是最新版本。
	Latest string `json:"latest,omitempty"`
	// Available 表示有新版本。
	Available bool `json:"available"`
	// Installable 表示可以安装更新。
	Installable bool `json:"installable"`
	// URL 是更新页面地址。
	URL string `json:"url,omitempty"`
	// Notes 是发布说明。
	Notes string `json:"notes,omitempty"`
}

// AppUpdateInstallResult 表示安装请求是否已受理。
type AppUpdateInstallResult struct {
	// Started 表示已开始处理安装。
	Started bool `json:"started"`
}
