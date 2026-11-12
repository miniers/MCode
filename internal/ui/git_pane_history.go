package mcodeui

import "github.com/egoist/mygo/ui"

func renderGitPaneHistory(c *ui.Context, s *chatState, threadID string, ps *gitPaneState) {
	renderGitPaneEmpty(c, "暂无提交", IconGitBranch())
}
