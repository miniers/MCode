package mcodeui

import "context"

type appUpdateInstallClient interface {
	InstallUpdate(ctx context.Context, progress func(float64)) error
}

// runAppUpdateInstall 同步安装更新并刷新进度。
func runAppUpdateInstall(st *SettingsState, client appUpdateInstallClient) {
	err := client.InstallUpdate(context.Background(), func(progress float64) {
		globalAboutState.mu.Lock()
		globalAboutState.progress = progress
		globalAboutState.mu.Unlock()
		if st != nil {
			st.invalidate()
		}
	})
	globalAboutState.mu.Lock()
	globalAboutState.installing = false
	if err != nil {
		globalAboutState.errMsg = err.Error()
	}
	globalAboutState.mu.Unlock()
	if st != nil {
		st.invalidate()
	}
}

// startAppUpdateInstall 在后台安装更新。
func startAppUpdateInstall(st *SettingsState, client appUpdateInstallClient) {
	globalAboutState.mu.Lock()
	if globalAboutState.installing {
		globalAboutState.mu.Unlock()
		return
	}
	globalAboutState.installing = true
	globalAboutState.progress = 0
	globalAboutState.errMsg = ""
	globalAboutState.mu.Unlock()
	if st != nil {
		st.invalidate()
	}
	go runAppUpdateInstall(st, client)
}
