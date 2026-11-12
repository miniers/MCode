package mcodeui

import (
	"path"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

func renderGitPaneChanges(c *ui.Context, s *chatState, threadID string, ps *gitPaneState) {
	ps.mu.Lock()
	entries := append([]git.StatusEntry(nil), ps.entries...)
	ps.mu.Unlock()
	if len(entries) == 0 {
		renderGitPaneEmpty(c, "工作区干净", IconCheck())
		return
	}
	ui.Scroll(c.Key("git-pane-changes")).Fill().Children(func() {
		ui.Column(c).FillWidth().Children(func() {
			for _, entry := range entries {
				code := entry.Worktree
				if code == ' ' || code == 0 {
					code = entry.Index
				}
				if gitEntryConflicted(entry) {
					code = 'U'
				}
				letter, color := gitStatusPresentation(c, code)
				ui.Row(c.Key("git-pane-file-"+entry.Path)).FillWidth().Height(RowHeight).Padding(0, Space3).Gap(Space3).AlignItems(ui.Center).Children(func() {
					ui.Text(c, letter).Width(IconMD).FontSize(uiFont(TypeFootnote)).TextColor(color)
					ui.Text(c, path.Base(entry.Path)).FontSize(uiFont(TypeBody)).SingleLine().Shrink(1).MinWidth(0)
					if dir := path.Dir(entry.Path); dir != "." {
						ui.Text(c, dir).FontSize(uiFont(TypeFootnote)).TextColor(ThemeTertiaryText(c)).SingleLine().Shrink(1).MinWidth(0)
					}
				})
			}
		})
	})
}
