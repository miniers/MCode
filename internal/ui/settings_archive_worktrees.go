package mcodeui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/git"
)

// archiveWorktreeItem 描述归档页「Worktree 清理」区块中的一条扫描结果。
type archiveWorktreeItem struct {
	Path      string
	Branch    string
	RepoRoot  string
	SizeBytes int64
	Merged    bool
	Clean     bool
	Orphan    bool
	InUse     bool
}

// archiveWorktreeState 维护「Worktree 清理」区块的运行时会话状态。
type archiveWorktreeState struct {
	mu       sync.Mutex
	loaded   bool
	scanning bool
	scanErr  string
	items    []archiveWorktreeItem
	selected map[string]bool // worktree 路径 -> 勾选状态

	purging       bool
	resultText    string
	resultRemoved int
	resultFailed  []string

	confirmOpen  bool
	confirmDirty bool // 「我确认丢弃未提交改动」
}

var globalArchiveWorktreeState archiveWorktreeState

// resetArchiveWorktreeStateForTest 重置清理区块状态，供单元测试隔离使用。
func resetArchiveWorktreeStateForTest() {
	globalArchiveWorktreeState.mu.Lock()
	defer globalArchiveWorktreeState.mu.Unlock()
	globalArchiveWorktreeState.loaded = false
	globalArchiveWorktreeState.scanning = false
	globalArchiveWorktreeState.scanErr = ""
	globalArchiveWorktreeState.items = nil
	globalArchiveWorktreeState.selected = nil
	globalArchiveWorktreeState.purging = false
	globalArchiveWorktreeState.resultText = ""
	globalArchiveWorktreeState.resultRemoved = 0
	globalArchiveWorktreeState.resultFailed = nil
	globalArchiveWorktreeState.confirmOpen = false
	globalArchiveWorktreeState.confirmDirty = false
}

// archiveWtItemDirty 判断该项是否存在未提交改动；孤立项（目录已不存在）按无改动处理。
func archiveWtItemDirty(it archiveWorktreeItem) bool {
	return !it.Orphan && !it.Clean
}

// archiveWtSelectable 判断该项是否可被勾选；正在被会话使用的 worktree 不可选。
func archiveWtSelectable(it archiveWorktreeItem) bool {
	return !it.InUse
}

// archiveWtSelectedItems 返回当前选中且仍可选的项，保持扫描顺序。
func archiveWtSelectedItems(items []archiveWorktreeItem, selected map[string]bool) []archiveWorktreeItem {
	var out []archiveWorktreeItem
	for _, it := range items {
		if selected[it.Path] && archiveWtSelectable(it) {
			out = append(out, it)
		}
	}
	return out
}

// archiveWtTotalSize 汇总待清理项占用的磁盘空间。
func archiveWtTotalSize(items []archiveWorktreeItem) int64 {
	var total int64
	for _, it := range items {
		total += it.SizeBytes
	}
	return total
}

// archiveWtDirtyItems 过滤出存在未提交改动的项。
func archiveWtDirtyItems(items []archiveWorktreeItem) []archiveWorktreeItem {
	var out []archiveWorktreeItem
	for _, it := range items {
		if archiveWtItemDirty(it) {
			out = append(out, it)
		}
	}
	return out
}

// archiveWtNeedsDirtyConfirm 判断清理这批项是否需要额外的脏改动确认。
func archiveWtNeedsDirtyConfirm(items []archiveWorktreeItem) bool {
	return len(archiveWtDirtyItems(items)) > 0
}

// archiveWtCanConfirm 判断确认按钮是否可用：含未提交改动时必须先勾选确认丢弃。
func archiveWtCanConfirm(items []archiveWorktreeItem, dirtyConfirmed bool) bool {
	if len(items) == 0 {
		return false
	}
	return !archiveWtNeedsDirtyConfirm(items) || dirtyConfirmed
}

// archiveWorktreeScanTimeout 是归档页 Worktree 扫描的总时长上限。
// git 卡在仓库锁或慢盘上时必须让「扫描中」结束，否则 scanning 永久为 true，
// 扫描与清理按钮会一直禁用（S-116）。
const archiveWorktreeScanTimeout = 90 * time.Second

// archiveSettingsDataDir 读取设置面板的数据目录（构造后不再变化）。
func archiveSettingsDataDir(st *SettingsState) string {
	if st == nil {
		return ""
	}
	return st.dataDir
}

// archiveWorktreeGitRunner 创建清理区块使用的 git 执行器，
// 并把受管 worktree 基目录指向真实数据目录，供孤立目录的兜底校验使用。
func archiveWorktreeGitRunner(dataDir string) *git.Runner {
	if dir := strings.TrimSpace(dataDir); dir != "" {
		git.SetWorktreePurgeRoots(
			filepath.Join(dir, "worktrees"),
			filepath.Join(dir, "swarm-worktrees"),
			filepath.Join(dir, "hydra-worktrees"),
		)
	}
	return git.NewRunner("", 4)
}

// loadArchiveWorktrees 异步扫描各项目遗留的 worktree。
func loadArchiveWorktrees(st *SettingsState) {
	client := archiveClient(st)
	if client == nil {
		return
	}

	globalArchiveWorktreeState.mu.Lock()
	if globalArchiveWorktreeState.scanning {
		globalArchiveWorktreeState.mu.Unlock()
		return
	}
	globalArchiveWorktreeState.scanning = true
	globalArchiveWorktreeState.scanErr = ""
	globalArchiveWorktreeState.mu.Unlock()

	go fetchArchiveWorktreesWorker(st, client)
}

func fetchArchiveWorktreesWorker(st *SettingsState, client Client) {
	items, err := scanProjectWorktrees(client, archiveSettingsDataDir(st))

	globalArchiveWorktreeState.mu.Lock()
	globalArchiveWorktreeState.scanning = false
	globalArchiveWorktreeState.loaded = true
	if err != nil {
		globalArchiveWorktreeState.scanErr = err.Error()
		globalArchiveWorktreeState.items = nil
	} else {
		globalArchiveWorktreeState.scanErr = ""
		globalArchiveWorktreeState.items = items
		// 保留仍然存在且可选的勾选状态。
		next := make(map[string]bool)
		for _, it := range items {
			if globalArchiveWorktreeState.selected[it.Path] && archiveWtSelectable(it) {
				next[it.Path] = true
			}
		}
		globalArchiveWorktreeState.selected = next
	}
	globalArchiveWorktreeState.mu.Unlock()

	if st != nil {
		st.invalidate()
	}
}

// scanProjectWorktrees 扫描所有项目仓库下除主 worktree 以外的全部 worktree。
// 目录状态（未提交改动、孤立、大小）与使用中标记都来自 Client.ScanWorktrees 的一次全量扫描
// （后端对每个仓库只做一遍 git 扫描），这里不再逐项目重复扫描。
func scanProjectWorktrees(client Client, dataDir string) ([]archiveWorktreeItem, error) {
	if client == nil {
		return nil, fmt.Errorf("客户端不可用")
	}
	// 扫描本身不再需要 git 执行器，但清理逻辑依赖这里设置的受管 worktree 基目录。
	_ = archiveWorktreeGitRunner(dataDir)

	ctx, cancel := context.WithTimeout(context.Background(), archiveWorktreeScanTimeout)
	defer cancel()
	infos, err := client.ScanWorktrees(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("扫描超时（超过 %d 秒），请稍后重试", int(archiveWorktreeScanTimeout.Seconds()))
		}
		return nil, fmt.Errorf("扫描 Worktree 失败: %w", err)
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("扫描超时（超过 %d 秒），请稍后重试", int(archiveWorktreeScanTimeout.Seconds()))
	}
	items := make([]archiveWorktreeItem, 0, len(infos))
	for _, it := range infos {
		items = append(items, archiveWorktreeItem{
			Path:      it.Path,
			Branch:    it.Branch,
			RepoRoot:  it.ProjectPath,
			SizeBytes: it.SizeBytes,
			Merged:    it.Merged,
			Clean:     it.Clean,
			Orphan:    it.Orphan,
			InUse:     it.Active,
		})
	}
	return items, nil
}

// archiveWorktreePathInUse 判断路径是否命中使用中集合（含符号链接解析）。
func archiveWorktreePathInUse(inUse map[string]bool, path string) bool {
	if len(inUse) == 0 || strings.TrimSpace(path) == "" {
		return false
	}
	clean := filepath.Clean(path)
	if inUse[clean] {
		return true
	}
	if real, err := filepath.EvalSymlinks(clean); err == nil && real != clean && inUse[real] {
		return true
	}
	return false
}

// purgeArchiveWorktrees 按勾选清理选中的 worktree；完成后重新扫描刷新列表。
func purgeArchiveWorktrees(st *SettingsState, targets []archiveWorktreeItem, allowDirty bool) {
	globalArchiveWorktreeState.mu.Lock()
	if globalArchiveWorktreeState.purging || len(targets) == 0 {
		globalArchiveWorktreeState.mu.Unlock()
		return
	}
	globalArchiveWorktreeState.purging = true
	globalArchiveWorktreeState.resultText = ""
	globalArchiveWorktreeState.resultRemoved = 0
	globalArchiveWorktreeState.resultFailed = nil
	globalArchiveWorktreeState.mu.Unlock()
	if st != nil {
		st.invalidate()
	}

	go func() {
		runner := archiveWorktreeGitRunner(archiveSettingsDataDir(st))
		ctx := context.Background()

		var removed int
		var freed int64
		var failed []string
		for _, it := range targets {
			err := runner.PurgeWorktree(ctx, it.RepoRoot, git.WorktreeScanItem{
				Path:      it.Path,
				Branch:    it.Branch,
				RepoRoot:  it.RepoRoot,
				SizeBytes: it.SizeBytes,
				Merged:    it.Merged,
				Clean:     it.Clean,
				Orphan:    it.Orphan,
			}, allowDirty && archiveWtItemDirty(it))
			if err != nil {
				failed = append(failed, fmt.Sprintf("%s：%s", it.Path, err.Error()))
				continue
			}
			removed++
			freed += it.SizeBytes
		}

		globalArchiveWorktreeState.mu.Lock()
		globalArchiveWorktreeState.purging = false
		globalArchiveWorktreeState.resultRemoved = removed
		globalArchiveWorktreeState.resultFailed = failed
		switch {
		case len(failed) == 0:
			globalArchiveWorktreeState.resultText = fmt.Sprintf("已清理 %d 个 Worktree，回收约 %s 磁盘空间", removed, formatFileSize(freed))
		case removed == 0:
			globalArchiveWorktreeState.resultText = fmt.Sprintf("清理失败：%d 个 Worktree 未被删除", len(failed))
		default:
			globalArchiveWorktreeState.resultText = fmt.Sprintf("已清理 %d 个 Worktree（回收约 %s），%d 个失败", removed, formatFileSize(freed), len(failed))
		}
		globalArchiveWorktreeState.mu.Unlock()
		if st != nil {
			st.invalidate()
		}

		loadArchiveWorktrees(st)
	}()
}

// closeArchiveWorktreeConfirm 关闭清理确认弹窗并复位脏改动确认勾选。
func closeArchiveWorktreeConfirm(st *SettingsState) {
	globalArchiveWorktreeState.mu.Lock()
	globalArchiveWorktreeState.confirmOpen = false
	globalArchiveWorktreeState.confirmDirty = false
	globalArchiveWorktreeState.mu.Unlock()
	if st != nil {
		st.invalidate()
	}
}

// renderArchiveWorktreeSection 渲染归档页「Worktree 清理」区块：
// 列出各项目遗留的 worktree（含孤立目录、已合入、有未提交改动），勾选后确认清理。
func renderArchiveWorktreeSection(c *ui.Context, st *SettingsState) {
	globalArchiveWorktreeState.mu.Lock()
	loaded := globalArchiveWorktreeState.loaded
	scanning := globalArchiveWorktreeState.scanning
	scanErr := globalArchiveWorktreeState.scanErr
	items := make([]archiveWorktreeItem, len(globalArchiveWorktreeState.items))
	copy(items, globalArchiveWorktreeState.items)
	selected := make(map[string]bool, len(globalArchiveWorktreeState.selected))
	for k, v := range globalArchiveWorktreeState.selected {
		selected[k] = v
	}
	purging := globalArchiveWorktreeState.purging
	resultText := globalArchiveWorktreeState.resultText
	resultRemoved := globalArchiveWorktreeState.resultRemoved
	resultFailed := append([]string(nil), globalArchiveWorktreeState.resultFailed...)
	confirmOpen := globalArchiveWorktreeState.confirmOpen
	globalArchiveWorktreeState.mu.Unlock()

	client := archiveClient(st)
	selectedItems := archiveWtSelectedItems(items, selected)

	// 首次进入自动扫描
	if !loaded && !scanning && client != nil {
		loadArchiveWorktrees(st)
	}

	renderSettingsSection(c, "sec-archive-worktrees", "Worktree 清理", func() {
		ui.Row(c.Key("archive-wt-header")).
			Padding(settingsCardPad, Space5).
			Gap(Space4).
			AlignItems(ui.Center).
			Children(func() {
				ui.Text(c, "扫描各项目遗留的 Worktree（孤立目录、已合入、有未提交改动），勾选后可彻底删除并回收磁盘空间；正在被会话使用的 Worktree 不可选择。").
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c)).
					Grow(1)

				Button(c.Key("btn-archive-wt-scan"), ButtonOpts{
					Icon:     IconRefreshCw(),
					Text:     "扫描",
					Variant:  ButtonSecondary,
					Size:     ControlMD,
					Disabled: scanning || purging,
					OnClick:  func() { loadArchiveWorktrees(st) },
				})
				Button(c.Key("btn-archive-wt-purge"), ButtonOpts{
					Icon:     IconTrash2(),
					Text:     "清理所选",
					Variant:  ButtonDanger,
					Size:     ControlMD,
					Disabled: scanning || purging || len(selectedItems) == 0,
					OnClick: func() {
						globalArchiveWorktreeState.mu.Lock()
						globalArchiveWorktreeState.confirmDirty = false
						globalArchiveWorktreeState.confirmOpen = true
						globalArchiveWorktreeState.mu.Unlock()
						if st != nil {
							st.invalidate()
						}
					},
				})
			})

		renderSettingsDivider(c, "div-archive-wt")

		switch {
		case client == nil:
			ui.Text(c, "客户端不可用，无法扫描 Worktree。").
				FontSize(TypeBody).
				TextColor(ThemeSecondaryText(c)).
				Padding(settingsCompactPadX, Space5)
		case scanning:
			ui.Row(c.Key("archive-wt-scanning")).
				Padding(settingsCompactPadX, Space5).
				Gap(Space2).
				AlignItems(ui.Center).
				Children(func() {
					ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))
					ui.Text(c, "正在扫描 Worktree…").
						FontSize(TypeBody).
						TextColor(ThemeSecondaryText(c))
				})
		case scanErr != "":
			ui.Row(c.Key("archive-wt-scan-error")).
				Padding(settingsCompactPadX, Space5).
				Gap(Space3).
				AlignItems(ui.Center).
				Children(func() {
					ui.Text(c, "扫描失败："+scanErr).
						FontSize(TypeBody).
						TextColor(ThemeDanger(c)).
						Grow(1)
					Button(c.Key("btn-archive-wt-retry"), ButtonOpts{
						Text:    "重试",
						Variant: ButtonSecondary,
						Size:    ControlMD,
						OnClick: func() { loadArchiveWorktrees(st) },
					})
				})
		case len(items) == 0:
			ui.Text(c, "没有发现遗留的 Worktree。").
				FontSize(TypeBody).
				TextColor(ThemeSecondaryText(c)).
				Padding(settingsCompactPadX, Space5)
		default:
			ui.Column(c.Key("archive-wt-list")).Gap(Space0).Padding(0, 0, Space2, 0).Children(func() {
				for i, it := range items {
					if i > 0 {
						renderSettingsDivider(c, "div-archive-wt-"+it.Path)
					}
					renderArchiveWorktreeRow(c, st, it, selected[it.Path])
				}
			})
		}

		if purging {
			ui.Row(c.Key("archive-wt-purging")).
				Padding(0, Space5, Space4, Space5).
				Gap(Space2).
				AlignItems(ui.Center).
				Children(func() {
					ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))
					ui.Text(c, "正在清理所选 Worktree…").
						FontSize(TypeFootnote).
						TextColor(ThemeSecondaryText(c))
				})
		}

		if resultText != "" {
			renderSettingsDivider(c, "div-archive-wt-result")
			ui.Column(c.Key("archive-wt-result")).
				Padding(0, Space5, settingsCardPad, Space5).
				Gap(Space1).
				Children(func() {
					color := ThemeSuccess(c)
					switch {
					case resultRemoved == 0 && len(resultFailed) > 0:
						color = ThemeDanger(c)
					case len(resultFailed) > 0:
						color = ThemeWarning(c)
					}
					ui.Text(c, resultText).
						FontSize(TypeFootnote).
						FontWeight(500).
						TextColor(color)
					for _, line := range resultFailed {
						ui.Text(c, line).
							FontSize(TypeCaption).
							TextColor(ThemeDanger(c))
					}
				})
		}
	})

	if confirmOpen && len(selectedItems) > 0 {
		renderArchiveWorktreeConfirmModal(c, st, selectedItems, &globalArchiveWorktreeState.confirmDirty, &globalArchiveWorktreeState.confirmOpen)
	}
}

// renderArchiveWorktreeRow 渲染列表中的一行：勾选、路径、分支、大小与状态标签。
func renderArchiveWorktreeRow(c *ui.Context, st *SettingsState, it archiveWorktreeItem, checked bool) {
	key := "archive-wt-row-" + it.Path
	ui.Row(c.Key(key)).
		Padding(settingsCompactPadX, Space5).
		Gap(settingsCompactPadX).
		AlignItems(ui.Center).
		Children(func() {
			selectable := archiveWtSelectable(it)
			cb := ui.Checkbox(c.Key("archive-wt-check-"+it.Path), &checked, "").
				Label(filepath.Base(it.Path))
			if !selectable {
				cb.Disabled(true)
			}
			if cb.Changed() && selectable {
				globalArchiveWorktreeState.mu.Lock()
				if globalArchiveWorktreeState.selected == nil {
					globalArchiveWorktreeState.selected = make(map[string]bool)
				}
				globalArchiveWorktreeState.selected[it.Path] = checked
				globalArchiveWorktreeState.mu.Unlock()
				if st != nil {
					st.invalidate()
				}
			}

			ui.Column(c.Key(key + "-info")).Grow(1).Gap(Space0).Children(func() {
				ui.Text(c, it.Path).
					FontSize(TypeBody).
					TextColor(ThemePrimaryText(c)).
					SingleLine()
				branch := it.Branch
				if branch == "" {
					branch = "（未关联分支）"
				}
				ui.Text(c, branch).
					FontSize(TypeCaption).
					TextColor(ThemeSecondaryText(c)).
					SingleLine()
			})

			ui.Text(c, formatFileSize(it.SizeBytes)).
				FontSize(TypeCaption).
				TextColor(ThemeSecondaryText(c))

			if it.InUse {
				renderWorktreeTag(c, key+"-tag-inuse", "使用中", ThemeDanger(c))
			}
			if archiveWtItemDirty(it) {
				renderWorktreeTag(c, key+"-tag-dirty", "有未提交改动", ThemeWarning(c))
			}
			if it.Orphan {
				renderWorktreeTag(c, key+"-tag-orphan", "孤立", ThemeSecondaryText(c))
			}
			if it.Merged {
				renderWorktreeTag(c, key+"-tag-merged", "已合入", ThemeAccent(c))
			}
		})
}

// renderArchiveWorktreeConfirmModal 渲染清理确认弹窗：
// 逐条列出将删除的路径与大小；含未提交改动时必须勾选「我确认丢弃这些未提交改动」才能执行。
func renderArchiveWorktreeConfirmModal(c *ui.Context, st *SettingsState, targets []archiveWorktreeItem, dirtyConfirmed *bool, open *bool) {
	if !*open {
		modalDragReset(modalDragArchiveWorktree)
		return
	}
	dirtyItems := archiveWtDirtyItems(targets)
	detail := make([]string, 0, len(targets))
	for _, it := range targets {
		branch := it.Branch
		if branch == "" {
			branch = "（未关联分支）"
		}
		line := fmt.Sprintf("%s · %s · %s", it.Path, branch, formatFileSize(it.SizeBytes))
		if archiveWtItemDirty(it) {
			line += "（有未提交改动）"
		}
		detail = append(detail, line)
	}

	RenderConfirmDialog(c, ConfirmDialog{
		Key:             modalDragArchiveWorktree,
		Open:            true,
		Tone:            ConfirmDanger,
		Title:           "清理 Worktree",
		Body:            fmt.Sprintf("将彻底删除以下 %d 个 Worktree，共约 %s，目录删除后无法恢复：", len(targets), formatFileSize(archiveWtTotalSize(targets))),
		Detail:          detail,
		ConfirmLabel:    "确认删除",
		CancelLabel:     "取消",
		ConfirmDisabled: !archiveWtCanConfirm(targets, *dirtyConfirmed),
		// 含未提交改动时必须先勾选确认，否则确认按钮保持禁用。
		Footer: func() {
			if len(dirtyItems) == 0 {
				return
			}
			ui.Column(c.Key("modal-archive-wt-dirty")).
				FillWidth().
				Padding(settingsCompactPadX, Space4).
				Radius(RadiusMD).
				Gap(Space2).
				Background(ThemeDanger(c).Alpha(0.12)).
				Children(func() {
					ui.Text(c, fmt.Sprintf("其中 %d 个存在未提交改动，删除后这些改动将无法恢复：", len(dirtyItems))).
						FontSize(TypeFootnote).
						FontWeight(600).
						TextColor(ThemeDanger(c))
					for _, it := range dirtyItems {
						ui.Text(c, it.Path).
							FontSize(TypeCaption).
							TextColor(ThemeDanger(c)).
							SingleLine()
					}
					ui.Row(c.Key("modal-archive-wt-dirty-confirm-row")).
						Gap(Space2).
						AlignItems(ui.Center).
						Children(func() {
							cb := ui.Checkbox(c.Key("modal-archive-wt-dirty-confirm"), dirtyConfirmed, "我确认丢弃这些未提交改动")
							if cb.Changed() && st != nil {
								st.invalidate()
							}
						})
				})
		},
		OnConfirm: func() {
			allowDirty := *dirtyConfirmed
			targetsCopy := append([]archiveWorktreeItem(nil), targets...)
			closeArchiveWorktreeConfirm(st)
			purgeArchiveWorktrees(st, targetsCopy, allowDirty)
		},
		OnCancel: func() { closeArchiveWorktreeConfirm(st) },
	})
}
