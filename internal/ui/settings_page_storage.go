package mcodeui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

const settingsStorageHeadlineFont float32 = 24

type storageCategoryDef struct {
	cat   StorageCategory
	title string
}

var storageCategories = []storageCategoryDef{
	{cat: StorageCategoryDeletedSessions, title: "已删会话残留"},
	{cat: StorageCategoryOrphanPatches, title: "无主补丁"},
	{cat: StorageCategoryOldBackups, title: "旧备份"},
	{cat: StorageCategoryOldLogs, title: "旧日志"},
}

type storagePageState struct {
	gen         uint64
	loaded      bool
	scanning    bool
	attempted   bool
	scanErr     string
	report      StorageReport
	selected    map[StorageCategory]bool
	expanded    map[StorageCategory]bool
	cleaning    bool
	cleanErr    string
	confirmOpen bool
}

type storageGroupView struct {
	def   storageCategoryDef
	items []StorageItem
	bytes int64
}

type storagePageSnapshot struct {
	loaded      bool
	scanning    bool
	scanErr     string
	report      StorageReport
	selected    map[StorageCategory]bool
	expanded    map[StorageCategory]bool
	cleaning    bool
	cleanErr    string
	confirmOpen bool
}

var pageStorage = settingsPage{
	ID:    "storage",
	Title: "存储",
	Icon:  IconArchive(),
	Keywords: []string{
		"存储", "清理", "空间", "storage", "cleanup",
		"trash", "废纸篓", "日志", "补丁", "会话残留",
	},
	Sections: []settingsSection{
		{
			ID: "storage.cleanup",
			Rows: []setting{{
				ID:      "row-storage-cleanup",
				Control: customFullWidth(renderStorageCleanupSection),
			}},
		},
		{
			ID: "storage.worktree",
			Rows: []setting{{
				ID:      "row-storage-worktree",
				Title:   "Worktree",
				Aliases: []string{"worktree", "工作树", "清理"},
				Control: custom(renderStorageWorktreeGoControl),
			}},
		},
	},
}

func init() { settingsTreePageDefs = append(settingsTreePageDefs, pageStorage) }

func storageStateLocked(st *SettingsState) *storagePageState {
	if st.storagePage == nil {
		st.storagePage = &storagePageState{}
	}
	return st.storagePage
}

func resetStoragePageForEntryLocked(st *SettingsState) {
	ps := storageStateLocked(st)
	ps.gen++
	ps.loaded = false
	ps.scanning = false
	ps.attempted = false
	ps.scanErr = ""
	ps.cleaning = false
	ps.cleanErr = ""
	ps.confirmOpen = false
}

func snapshotStoragePage(st *SettingsState) storagePageSnapshot {
	st.mu.Lock()
	defer st.mu.Unlock()
	ps := storageStateLocked(st)
	selected := make(map[StorageCategory]bool, len(ps.selected))
	for k, v := range ps.selected {
		selected[k] = v
	}
	expanded := make(map[StorageCategory]bool, len(ps.expanded))
	for k, v := range ps.expanded {
		expanded[k] = v
	}
	items := append([]StorageItem(nil), ps.report.Items...)
	return storagePageSnapshot{
		loaded:      ps.loaded,
		scanning:    ps.scanning,
		scanErr:     ps.scanErr,
		report:      StorageReport{TotalBytes: ps.report.TotalBytes, Items: items},
		selected:    selected,
		expanded:    expanded,
		cleaning:    ps.cleaning,
		cleanErr:    ps.cleanErr,
		confirmOpen: ps.confirmOpen,
	}
}

func setStorageCategorySelected(st *SettingsState, cat StorageCategory, checked bool) {
	st.mu.Lock()
	ps := storageStateLocked(st)
	if ps.selected == nil {
		ps.selected = make(map[StorageCategory]bool)
	}
	ps.selected[cat] = checked
	st.mu.Unlock()
	st.invalidate()
}

func toggleStorageCategoryExpanded(st *SettingsState, cat StorageCategory) {
	st.mu.Lock()
	ps := storageStateLocked(st)
	if ps.expanded == nil {
		ps.expanded = make(map[StorageCategory]bool)
	}
	ps.expanded[cat] = !ps.expanded[cat]
	st.mu.Unlock()
	st.invalidate()
}

func openStorageConfirm(st *SettingsState) {
	st.mu.Lock()
	ps := storageStateLocked(st)
	ps.confirmOpen = true
	st.mu.Unlock()
	st.invalidate()
}

func closeStorageConfirm(st *SettingsState) {
	st.mu.Lock()
	if st.storagePage != nil {
		st.storagePage.confirmOpen = false
	}
	st.mu.Unlock()
	st.invalidate()
}

func groupStorageItems(items []StorageItem) []storageGroupView {
	byCat := make(map[StorageCategory][]StorageItem, len(storageCategories))
	bytesByCat := make(map[StorageCategory]int64, len(storageCategories))
	for _, it := range items {
		byCat[it.Category] = append(byCat[it.Category], it)
		bytesByCat[it.Category] += it.Bytes
	}
	var groups []storageGroupView
	for _, def := range storageCategories {
		catItems := byCat[def.cat]
		if len(catItems) == 0 {
			continue
		}
		groups = append(groups, storageGroupView{
			def:   def,
			items: catItems,
			bytes: bytesByCat[def.cat],
		})
	}
	return groups
}

func renderStorageCleanupSection(c *ui.Context, st *SettingsState) {
	ensureStorageScan(st)
	client := archiveClient(st)
	snap := snapshotStoragePage(st)

	groups := groupStorageItems(snap.report.Items)
	var selectedBytes int64
	var selectedIDs []string
	for _, g := range groups {
		if snap.selected[g.def.cat] {
			selectedBytes += g.bytes
			for _, it := range g.items {
				selectedIDs = append(selectedIDs, it.ID)
			}
		}
	}

	ui.Column(c.Key("storage-cleanup-root")).FillWidth().Children(func() {
		switch {
		case client == nil:
			ui.Text(c, "客户端不可用").
				FontSize(uiFont(TypeBody)).
				TextColor(ThemeSecondaryText(c)).
				Padding(settingsRowPadY, settingsRowPadX)
		case snap.scanning && !snap.loaded:
			ui.Row(c.Key("storage-scanning-row")).
				Padding(settingsRowPadY, settingsRowPadX).
				AlignItems(ui.Center).
				Children(func() {
					LoadingIndicator(c, "storage-scanning-indicator", "正在扫描…", IconSM)
				})
		case snap.scanErr != "" && !snap.loaded:
			ui.Row(c.Key("storage-scan-error-row")).
				Padding(settingsRowPadY, settingsRowPadX).
				Gap(Space3).
				AlignItems(ui.Center).
				Children(func() {
					ui.Text(c, snap.scanErr).
						FontSize(uiFont(TypeBody)).
						TextColor(ThemeDanger(c)).
						Grow(1)
					Button(c, ButtonOpts{
						Key:     "btn-storage-retry",
						Text:    "重试",
						Variant: ButtonSecondary,
						Size:    ControlMD,
						OnClick: func() { startStorageScan(st) },
					})
				})
		case snap.loaded && len(snap.report.Items) == 0:
			EmptyState(c, EmptyStateProps{
				Key:   "storage-empty-state",
				Icon:  IconArchive(),
				Title: "没有可清理的内容",
			}).Padding(Space7, Space5)
		default:
			ui.Row(c.Key("storage-summary-row")).
				Padding(settingsRowPadY, settingsRowPadX).
				AlignItems(ui.Center).
				Gap(Space3).
				Children(func() {
					ui.Row(c.Key("storage-summary-numbers")).
						AlignItems(ui.Baseline).
						Gap(Space3).
						Grow(1).
						Children(func() {
							ui.Text(c, formatFileSize(selectedBytes)).
								FontSize(uiFont(settingsStorageHeadlineFont)).
								FontWeight(600).
								TextColor(ThemePrimaryText(c)).
								SingleLine()
							ui.Text(c, "总占用 "+formatFileSize(snap.report.TotalBytes)).
								FontSize(uiFont(TypeFootnote)).
								TextColor(ThemeSecondaryText(c)).
								SingleLine()
						})
					if snap.scanning {
						LoadingIndicator(c, "storage-rescanning-indicator", "正在扫描…", IconSM)
					} else if snap.cleaning {
						LoadingIndicator(c, "storage-cleaning-indicator", "正在移到废纸篓…", IconSM)
					}
				})

			for _, g := range groups {
				renderSettingsDivider(c, "storage-div-"+string(g.def.cat))
				renderStorageGroup(c, st, g, snap.selected[g.def.cat], snap.expanded[g.def.cat], snap.cleaning)
			}

			if snap.cleanErr != "" {
				renderSettingsDivider(c, "storage-div-clean-err")
				ui.Row(c.Key("storage-clean-error-row")).
					Padding(settingsRowPadY, settingsRowPadX).
					Children(func() {
						ui.Text(c, snap.cleanErr).
							FontSize(uiFont(TypeFootnote)).
							TextColor(ThemeDanger(c))
					})
			}

			renderSettingsDivider(c, "storage-div-actions")
			ui.Row(c.Key("storage-actions-row")).
				Padding(settingsRowPadY, settingsRowPadX).
				Justify(ui.End).
				AlignItems(ui.Center).
				Children(func() {
					btnText := "移到废纸篓"
					if len(selectedIDs) > 0 {
						btnText = fmt.Sprintf("移到废纸篓（%s）", formatFileSize(selectedBytes))
					}
					Button(c, ButtonOpts{
						Key:      "btn-storage-trash",
						Icon:     IconTrash2(),
						Text:     btnText,
						Variant:  ButtonPrimary,
						Size:     ControlMD,
						Loading:  snap.cleaning,
						Disabled: snap.scanning || snap.cleaning || len(selectedIDs) == 0,
						OnClick: func() {
							openStorageConfirm(st)
						},
					})
				})
		}
	})

	RenderConfirmDialog(c, ConfirmDialog{
		Key:          modalDragStorageClean,
		Open:         snap.confirmOpen && len(selectedIDs) > 0,
		Tone:         ConfirmDanger,
		Title:        "确认清理",
		Body:         fmt.Sprintf("将把所选 %d 项共 %s 移到废纸篓。", len(selectedIDs), formatFileSize(selectedBytes)),
		ConfirmLabel: "移到废纸篓",
		ConfirmA11y:  "确认移到废纸篓",
		CancelLabel:  "取消",
		OnConfirm: func() {
			startStorageClean(st, selectedIDs)
		},
		OnCancel: func() {
			closeStorageConfirm(st)
		},
	})
}

func renderStorageGroup(c *ui.Context, st *SettingsState, g storageGroupView, checked, expanded, disabled bool) {
	cat := g.def.cat
	ui.Column(c.Key("storage-group-"+string(cat))).FillWidth().Children(func() {
		ui.Row(c.Key("storage-group-row-"+string(cat))).
			Padding(settingsRowPadY, settingsRowPadX).
			Gap(Space3).
			AlignItems(ui.Center).
			Children(func() {
				val := checked
				cb := ui.Checkbox(c.Key("storage-check-"+string(cat)), &val, "").
					Label("选择 " + g.def.title)
				if disabled {
					cb.Disabled(true)
				}
				if cb.Changed() && !disabled {
					setStorageCategorySelected(st, cat, val)
				}

				btn := ui.ButtonBase(c.Key("storage-expand-"+string(cat))).
					Grow(1).
					Expanded(expanded).
					Label("展开 " + g.def.title).
					Padding(Space1, Space2).
					Radius(RadiusSM)
				if btn.Hovered() {
					btn.Background(ThemeHover(c))
				}
				if btn.Clicked() {
					toggleStorageCategoryExpanded(st, cat)
				}
				btn.Children(func() {
					ui.Row(c.Key("storage-expand-inner-"+string(cat))).
						FillWidth().
						Gap(Space3).
						AlignItems(ui.Center).
						Children(func() {
							ui.Text(c, g.def.title).
								FontSize(uiFont(TypeBody)).
								FontWeight(500).
								TextColor(ThemePrimaryText(c)).
								Grow(1).
								SingleLine()
							ui.Text(c, fmt.Sprintf("%d 项 · %s", len(g.items), formatFileSize(g.bytes))).
								FontSize(uiFont(TypeFootnote)).
								TextColor(ThemeSecondaryText(c)).
								SingleLine()
							chevron := IconChevronRight()
							if expanded {
								chevron = IconChevronDown()
							}
							ui.Icon(c, chevron).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
						})
				})
			})

		if expanded {
			ui.Column(c.Key("storage-paths-"+string(cat))).
				FillWidth().
				Padding(Space0, settingsRowPadX, Space3, Space8).
				Gap(Space1).
				Transition(motionCollapse(c)).
				Children(func() {
					for _, it := range g.items {
						ui.Row(c.Key("storage-item-"+it.ID)).
							FillWidth().
							Gap(Space3).
							AlignItems(ui.Center).
							Children(func() {
								ui.Text(c, it.ID).
									FontSize(uiFont(TypeFootnote)).
									TextColor(ThemeSecondaryText(c)).
									Grow(1).
									SingleLine()
								ui.Text(c, formatFileSize(it.Bytes)).
									FontSize(uiFont(TypeCaption)).
									TextColor(ThemeTertiaryText(c)).
									SingleLine()
							})
					}
				})
		}
	})
}

func renderStorageWorktreeGoControl(c *ui.Context, st *SettingsState) {
	Button(c, ButtonOpts{
		Key:     "btn-storage-worktree-go",
		Text:    "前往",
		Variant: ButtonSecondary,
		Size:    ControlMD,
		OnClick: func() {
			if st == nil {
				return
			}
			st.selectSettingsPage("sourceControl")
			openSettingsSheet(st, "worktree", "")
		},
	})
}
