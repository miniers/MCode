package mcodeui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

func worktreeCleanupCount(_ *SettingsState) string {
	globalArchiveWorktreeState.mu.Lock()
	defer globalArchiveWorktreeState.mu.Unlock()
	if !globalArchiveWorktreeState.loaded {
		return ""
	}
	return fmt.Sprintf("%d 个", len(globalArchiveWorktreeState.items))
}

var sheetWorktree = settingsSheet{
	ID:    "worktree",
	Title: "Worktree 清理",
	Body: func(c *ui.Context, st *SettingsState) {
		renderArchiveWorktreeSection(c, st)
	},
}

func init() { settingsTreeSheetDefs = append(settingsTreeSheetDefs, sheetWorktree) }
