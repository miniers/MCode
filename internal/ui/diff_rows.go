package mcodeui

import (
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

const diffLargeFileLineThreshold = 2000
const toolDiffLineNumberWidth float32 = 40

func diffRowFilePath(file git.FileDiff) string {
	if file.Status == "deleted" || file.NewPath == "" {
		return file.OldPath
	}
	return file.NewPath
}

func diffFileLineCount(file git.FileDiff) int {
	total := 0
	for _, hunk := range file.Hunks {
		total += len(hunk.Lines)
	}
	return total
}

func prefillDiffStats(files []git.FileDiff) {
	for i := range files {
		file := &files[i]
		if file.Binary || file.IsDeferred || file.Additions != 0 || file.Deletions != 0 {
			continue
		}
		stats := toolDiffFileStatsFor(*file)
		file.Additions, file.Deletions = stats.adds, stats.dels
	}
}

// 旧接口仅有 patch，保留有限摘要与显式延迟展开，不伪装成整文件 Buffer。
func renderLegacyDiffFile(c *ui.Context, s *chatState, dp *diffPanelState, file git.FileDiff) {
	path := diffRowFilePath(file)
	diffPanelMu.Lock()
	if expanded, ok := dp.expandedDeferred[path]; ok {
		file = expanded
	}
	collapsed, expanded := dp.collapsed[path], dp.expandedLarge[path]
	diffPanelMu.Unlock()
	ui.Column(c.Key("diff-file-" + path)).FillWidth().Transition(motionListItem(c)).Children(func() {
		btn := ui.ButtonBase(c.Key("diff-file-header-"+path)).Label(path).FillWidth().Padding(Space2, Space3)
		if btn.Clicked() {
			diffPanelMu.Lock()
			if dp.collapsed == nil {
				dp.collapsed = map[string]bool{}
			}
			dp.collapsed[path] = !collapsed
			diffPanelMu.Unlock()
			c.Invalidate()
		}
		btn.Children(func() {
			ui.Text(c, path).SingleLine().FontSize(uiFont(TypeFootnote))
			adds, dels := file.Additions, file.Deletions
			if adds == 0 && dels == 0 {
				stats := toolDiffFileStatsFor(file)
				adds, dels = stats.adds, stats.dels
			}
			ui.Text(c, fmt.Sprintf("+%d", adds)).TextColor(ThemeSuccess(c)).FontSize(uiFont(TypeFootnote))
			ui.Text(c, fmt.Sprintf("-%d", dels)).TextColor(ThemeDanger(c)).FontSize(uiFont(TypeFootnote))
		})
		if collapsed {
			return
		}
		ui.Column(c.Key("diff-file-body-" + path)).FillWidth().Transition(motionCollapse(c)).Children(func() {
			if file.IsDeferred || diffFileLineCount(file) > diffLargeFileLineThreshold && !expanded {
				Button(c, ButtonOpts{Key: "diff-expand-" + path, Text: "展开改动", Variant: ButtonSecondary, OnClick: func() {
					if file.IsDeferred {
						expandDeferredDiffFile(s, dp, path, file)
					} else {
						diffPanelMu.Lock()
						if dp.expandedLarge == nil {
							dp.expandedLarge = map[string]bool{}
						}
						dp.expandedLarge[path] = true
						diffPanelMu.Unlock()
						c.Invalidate()
					}
				}})
				return
			}
			renderLegacyPatch(c, dp, path, file)
		})
	})
}

// 直接索引解析器已有的 hunk 和行，不复制或摊平整批项目代码。
func renderLegacyPatch(c *ui.Context, dp *diffPanelState, path string, file git.FileDiff) {
	if file.Binary {
		renderDiffStatusState(c, "legacy-binary-"+path, IconFile(), "二进制文件", "", nil)
		return
	}
	if dp.patchLists == nil {
		dp.patchLists = map[string]*ui.ListState{}
	}
	list := dp.patchLists[path]
	if list == nil {
		list = &ui.ListState{}
		dp.patchLists[path] = list
		dp.patchOrder = append(dp.patchOrder, path)
		for len(dp.patchOrder) > reviewDiffCacheLimit {
			delete(dp.patchLists, dp.patchOrder[0])
			dp.patchOrder = dp.patchOrder[1:]
		}
	}
	total := diffFileLineCount(file) + len(file.Hunks)
	ui.List(c.Key("legacy-patch-"+path), list, total, func(index int) {
		for _, hunk := range file.Hunks {
			if index == 0 {
				ui.Text(c, hunk.Header).Font("monospace").FontSize(codeFontSize()).SingleLine()
				return
			}
			index--
			if index < len(hunk.Lines) {
				renderPatchLine(c, hunk.Lines[index])
				return
			}
			index -= len(hunk.Lines)
		}
	}).FillWidth().Height(min(float32(total)*ControlMD, 320))
}

func renderPatchLine(c *ui.Context, line git.Line) {
	bg := ui.Transparent
	if line.Kind == '+' {
		bg = ThemeSuccess(c).Alpha(.12)
	}
	if line.Kind == '-' {
		bg = ThemeDanger(c).Alpha(.12)
	}
	ui.Row(c).FillWidth().Background(bg).Children(func() {
		for _, no := range []int{line.OldNo, line.NewNo} {
			text := ""
			if no > 0 {
				text = strconv.Itoa(no)
			}
			ui.Text(c, text).Width(toolDiffLineNumberWidth).Font("monospace").FontSize(codeFontSize()).SingleLine().TextColor(ThemeSecondaryText(c))
		}
		ui.Text(c, string(line.Kind)).Width(Space4).Font("monospace").FontSize(codeFontSize())
		ui.Text(c, line.Text).Font("monospace").FontSize(codeFontSize()).SingleLine().Selectable().TextColor(ThemePrimaryText(c))
	})
}

// 摘要直接使用 patch 自带的真实坐标；不再维护第二套折叠行模型。
func renderPatchSummary(c *ui.Context, key string, file git.FileDiff) {
	if file.Binary {
		renderDiffStatusState(c, key+"-binary", IconFile(), "二进制文件", "", nil)
		return
	}
	if file.IsDeferred {
		renderDiffStatusState(c, key+"-deferred", IconFile(), "改动过多", "", nil)
		return
	}
	shown := 0
	for hi, hunk := range file.Hunks {
		if shown >= toolDiffMaxLines {
			break
		}
		ui.Text(c.Key(fmt.Sprintf("%s-hunk-%d", key, hi)), hunk.Header).Font("monospace").FontSize(codeFontSize()).SingleLine().TextColor(ThemeSecondaryText(c))
		for li, line := range hunk.Lines {
			if shown >= toolDiffMaxLines {
				break
			}
			shown++
			renderPatchLine(c.Key(fmt.Sprintf("%s-line-%d-%d", key, hi, li)), line)
		}
	}
	if remaining := diffFileLineCount(file) - shown; remaining > 0 {
		ui.Text(c, fmt.Sprintf("已截断，还有 %d 行未显示", remaining)).FontSize(uiFont(TypeCaption)).TextColor(ThemeWarning(c))
	}
}
