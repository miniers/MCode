package mcodeui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

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

// aboutDataDirTarget 在选取目录后计算实际迁移目标：目录已有内容时追加 MCode 子目录。
func aboutDataDirTarget(path string) string {
	path = strings.TrimSpace(path)
	entries, err := os.ReadDir(path)
	if err == nil && len(entries) > 0 {
		return filepath.Join(path, "MCode")
	}
	return path
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
