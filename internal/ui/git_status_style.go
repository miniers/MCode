package mcodeui

import (
	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

// gitStatusPresentation 是文件树、改动与 Git 面板共用的状态色来源。
func gitStatusPresentation(c *ui.Context, code byte) (letter string, color ui.Color) {
	switch code {
	case 'M':
		return "M", ThemeWarning(c)
	case 'A':
		return "A", ThemeSuccess(c)
	case 'D':
		return "D", ThemeDanger(c)
	case 'R', 'C':
		return string(code), ThemeAccent(c)
	case '?':
		return "?", ThemeSuccess(c).Alpha(0.7)
	case 'U':
		return "!", ThemeDanger(c)
	default:
		return "", ThemeSecondaryText(c)
	}
}

func gitEntryConflicted(e git.StatusEntry) bool {
	return e.Index == 'U' || e.Worktree == 'U' ||
		(e.Index == 'A' && e.Worktree == 'A') || (e.Index == 'D' && e.Worktree == 'D')
}

func gitLaneColors(c *ui.Context) [6]ui.Color {
	return [6]ui.Color{ThemeAccent(c), ThemeSuccess(c), ThemeWarning(c), ThemePlan(c), ThemeCaution(c), ThemeSecondaryText(c)}
}
