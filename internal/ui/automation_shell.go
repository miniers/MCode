package mcodeui

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

type automationWorkspaceProps struct {
	ScopeID     string
	SearchText  string
	ClearSearch func()
}

func renderAutomationSidebarSection(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	as := getAutomationState(s)
	ui.Column(c.Key("automation-sidebar-section")).FillWidth().Children(func() {
		for i, label := range []string{"全部", "失败", "已暂停"} {
			row := ui.Row(c.Key("automation-sidebar-"+label)).FillWidth().Height(RowHeight).Radius(RowCornerRadius).Padding(0, Space3).AlignItems(ui.Center).Focusable().Label(label)
			if as.filter == i {
				row.Background(ThemeSelected(c))
			} else if row.Hovered() {
				row.Background(ThemeHover(c))
			}
			if row.Clicked() || (row.Focused() && row.Shortcut(0, ui.KeyEnter)) {
				as.filter = i
				s.invalidate()
			}
			row.Children(func() { ui.Text(c, label).FontSize(TypeBody).TextColor(ThemePrimaryText(c)) })
		}
		value := as.unreadOnly
		if ui.Switch(c.Key("automation-unread-filter"), &value).Label("未读").Changed() {
			as.unreadOnly = value
			s.invalidate()
		}
	})
}

func renderAutomationWorkspaceInScope(c *ui.Context, s *chatState, p automationWorkspaceProps) {
	renderAutomationScopedBody(c, s, p)
}

func automationCanCreate(s *chatState) bool { return s != nil && len(s.projects) > 0 }

func requestNewAutomation(s *chatState, scopeID string, chooseProject func(onChosen func(projectID string))) {
	if !automationCanCreate(s) {
		return
	}
	chosen := false
	open := func(id string) {
		if chosen || id == "" {
			return
		}
		valid := false
		for _, p := range s.projects {
			if p.ID == id {
				valid = true
				break
			}
		}
		if !valid {
			return
		}
		chosen = true
		as := getAutomationState(s)
		as.resetToNewAutomation(s)
		as.editorProjectID = id
		s.invalidate()
	}
	if scopeID != "" {
		open(scopeID)
	} else if chooseProject != nil {
		chooseProject(open)
	}
}

func prepareAutomationLeave(s *chatState, next func()) bool {
	if s == nil {
		return true
	}
	as := getAutomationState(s)
	if !as.editorOpen || !as.dirty() {
		return true
	}
	as.pendingLeave = next
	as.confirmDiscard = true
	s.invalidate()
	return false
}

func handleAutomationEscape(s *chatState) bool {
	if s == nil {
		return false
	}
	as := getAutomationState(s)
	switch {
	case as.chooseProjectOpen:
		as.chooseProjectOpen = false
		as.pendingProjectChoice = nil
	case as.confirmDiscard:
		as.confirmDiscard = false
		as.pendingLeave = nil
	case as.confirmRun:
		as.confirmRun = false
	case as.editorOpen:
		as.closeEditor()
	case as.moreOpen:
		as.moreOpen = false
	case as.confirmDelete:
		as.confirmDelete = false
	case as.openRunID != "":
		as.openRunID = ""
	case as.narrowDetail:
		as.narrowDetail = false
	default:
		return false
	}
	s.invalidate()
	return true
}

func automationScopeUnread(s *chatState) (count int, known bool) {
	if s == nil {
		return 0, false
	}
	automationMu.Lock()
	defer automationMu.Unlock()
	as := automationStates[s]
	if as == nil || !as.initialized || !as.unreadKnown {
		return 0, false
	}
	for _, n := range as.unread {
		count += n
	}
	return count, true
}

func renderAutomationScopedBody(c *ui.Context, s *chatState, p automationWorkspaceProps) {
	if s == nil {
		return
	}
	as := getAutomationState(s)
	if as.projectID != p.ScopeID || !as.initialized {
		if as.projectID != p.ScopeID {
			as.stopSchedulePreview()
			*as = automationState{loadID: as.loadID, runsLoadID: as.runsLoadID, saveID: as.saveID, previewEpoch: as.previewEpoch, runRequest: as.runRequest + 1, selectionVersion: as.selectionVersion + 1, editSession: as.editSession + 1, watchStop: as.watchStop, runningIDs: make(map[string]bool), busyIDs: make(map[string]bool), projectID: p.ScopeID, filter: as.filter, unreadOnly: as.unreadOnly}
		}
		if s.client != nil {
			s.startAutomationWatch(p.ScopeID)
			s.loadAutomations(p.ScopeID)
		}
	}
	visible := filterAutomationStatus(as.autos, p.SearchText, as.filter, as.unreadOnly, as.unread)
	var selected Automation
	for _, a := range visible {
		if a.ID == as.selectedID {
			selected = a
			break
		}
	}
	width, height := c.Size()
	narrow := width < automationNarrowWidth
	if !as.editorOpen && c.Shortcut(0, ui.KeyEscape) {
		handleAutomationEscape(s)
	}
	ui.Column(c.Key("automation-workspace-root")).Fill().Background(ThemeSurface(c)).Children(func() {
		if as.loading && len(as.autos) == 0 {
			ui.Column(c.Key("automation-loading-box")).Fill().Center().Children(func() { LoadingIndicator(c, nil, "正在加载自动化…", IconMD) })
			return
		}
		if as.err != "" && len(as.autos) == 0 {
			ui.Column(c.Key("automation-error-box")).Fill().Center().Gap(Space2).Children(func() {
				ui.Text(c, as.err).TextColor(ThemeDanger(c))
				Button(c.Key("automation-retry-btn"), ButtonOpts{Text: "重试", OnClick: func() { s.loadAutomations(p.ScopeID) }, Variant: ButtonSecondary})
			})
			return
		}
		if as.err != "" {
			ui.Text(c, "刷新失败: "+as.err).TextColor(ThemeDanger(c))
		}
		if len(visible) == 0 {
			filtered := strings.TrimSpace(p.SearchText) != "" || as.filter != 0 || as.unreadOnly
			ui.Column(c.Key("automation-empty-full")).Fill().Center().Gap(Space5).Children(func() {
				if filtered {
					ui.Icon(c, IconSearch()).Size(IconLG, IconLG).TextColor(ThemeSecondaryText(c))
					ui.Text(c, "无匹配结果").TextColor(ThemePrimaryText(c))
					Button(c.Key("automation-clear-filter"), ButtonOpts{Text: "清除筛选", Variant: ButtonSecondary, OnClick: func() {
						as.filter = 0
						as.unreadOnly = false
						if p.ClearSearch != nil {
							p.ClearSearch()
						}
						s.invalidate()
					}})
				} else {
					ui.Icon(c, IconCalendarClock()).Size(automationEmptyIconSize, automationEmptyIconSize).TextColor(ThemeAccent(c))
					label := "还没有自动化"
					if p.ScopeID != "" {
						label = "此项目没有自动化"
					}
					ui.Text(c, label).TextColor(ThemePrimaryText(c))
					button := Button(c.Key("automation-empty-create"), ButtonOpts{Text: "新建自动化", Disabled: !automationCanCreate(s), Variant: ButtonPrimary, OnClick: func() {
						requestNewAutomation(s, p.ScopeID, func(onChosen func(string)) {
							as.pendingProjectChoice = onChosen
							as.chooseProjectOpen = true
						})
					}})
					appPopover(c, button, &as.chooseProjectOpen, nil, func() {
						popoverCard(c, ui.Column(c.Key("automation-project-menu"))).Padding(Space2, Space2).Children(func() {
							for _, project := range s.projects {
								Button(c.Key("automation-project-"+project.ID), ButtonOpts{Text: project.Name, Variant: ButtonGhost, OnClick: func() {
									choose := as.pendingProjectChoice
									as.pendingProjectChoice = nil
									as.chooseProjectOpen = false
									if choose != nil {
										choose(project.ID)
									}
								}})
							}
						})
					})
				}
			})
			return
		}
		ui.Row(c.Key("automation-main-row")).Fill().Children(func() {
			if !narrow || !as.narrowDetail || selected.ID == "" {
				list := ui.Column(c.Key("automation-list-col")).FillHeight()
				if narrow {
					list.Grow(1)
				} else {
					list.Width(automationListWidth)
				}
				list.Children(func() {
					names := map[string]string{}
					for _, project := range s.projects {
						names[project.ID] = project.Name
					}
					renderAutomationList(c, automationListProps{Visible: visible, SelectedID: as.selectedID, Unread: as.unread, RunningIDs: as.runningIDs, ScopeID: p.ScopeID, ProjectNames: names, Scroll: &as.listScroll, KeyboardFocusID: &as.keyboardFocusID, EditorOpen: as.editorOpen, Now: time.Now(), OnSelect: func(a Automation) { as.selectAutomation(a, s) }, OnEdit: func(a Automation) { as.fillAutomation(a); as.editorOpen = true; s.invalidate() }})
				})
			}
			if !narrow || (as.narrowDetail && selected.ID != "") {
				ui.Column(c.Key("automation-detail-col")).Grow(1).FillHeight().BorderWidth(Hairline).BorderColor(ThemeDivider(c)).Padding(Space6, Space6).Gap(Space4).Children(func() {
					if narrow {
						Button(c.Key("automation-back"), ButtonOpts{Text: "← 自动化", Variant: ButtonGhost, OnClick: func() { as.narrowDetail = false; s.invalidate() }})
					}
					if selected.ID == "" {
						ui.Column(c.Key("automation-select-prompt")).Grow(1).Center().Children(func() { ui.Text(c, "未选择自动化").TextColor(ThemeSecondaryText(c)) })
						return
					}
					ui.Row(c.Key("automation-selected-header")).FillWidth().AlignItems(ui.Center).Gap(Space2).Children(func() {
						ui.Text(c, selected.Name).FontSize(TypeTitle).TextColor(ThemePrimaryText(c)).Grow(1)
						enabled := selected.Enabled
						if ui.Switch(c.Key("automation-detail-toggle"), &enabled).Label("启用自动化").Disabled(as.busyIDs[selected.ID]).Changed() {
							s.setAutomationEnabled(selected, enabled)
						}
						Button(c.Key("automation-detail-run"), ButtonOpts{Text: "立即运行", Label: "auto-run-now-" + selected.ID, Disabled: as.runningIDs[selected.ID] || as.busyIDs[selected.ID], Variant: ButtonSecondary, OnClick: func() { s.requestAutomationRun(selected.ID) }})
						more := IconButton(c.Key("automation-detail-more"), IconButtonProps{Icon: IconMoreHorizontal(), Label: "自动化更多操作", Variant: ButtonGhost, OnClick: func() { as.moreOpen = true }})
						appPopover(c, more, &as.moreOpen, nil, func() {
							popoverCard(c, ui.Column(c.Key("automation-more-menu"))).Padding(Space2, Space2).Children(func() {
								Button(c.Key("automation-copy"), ButtonOpts{Text: "复制", Label: "auto-duplicate-" + selected.ID, Variant: ButtonGhost, OnClick: func() { as.moreOpen = false; s.duplicateAutomation(selected) }})
								Button(c.Key("automation-delete"), ButtonOpts{Text: "删除", Variant: ButtonDanger, OnClick: func() { as.moreOpen = false; s.deleteAutomation(selected.ID) }})
							})
						})
					})
					ui.Text(c, automationScheduleText(selected.Schedule)).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
					Segmented(c, "automation-detail-tabs", &as.detailTab, []Segment{{Label: "运行记录"}, {Label: "配置"}}, func(i int) { as.detailTab = i; s.invalidate() })
					if as.detailTab == 0 {
						unread := 0
						for _, r := range as.runs {
							if automationRunUnread(as.runRead, r.ID) {
								unread++
							}
						}
						scroll := ui.Scroll(c.Key("automation-history-scroll")).Grow(1).FillWidth()
						if as.openRunID == "" {
							scroll.TrackScroll(&as.historyScroll)
						}
						scroll.Children(func() {
							renderAutomationRunHistory(c, s, as, false, selected.ID, unread, as.includeArchived, as.runsLoading, as.runsErr, as.runs, as.runRead, as.runArchived, as.openRunID)
						})
					} else {
						Button(c.Key("automation-edit"), ButtonOpts{Text: "编辑", Variant: ButtonSecondary, OnClick: func() {
							if !as.dirty() {
								as.fillAutomation(selected)
							}
							as.editorOpen = true
							s.invalidate()
						}})
						renderAutomationConfig(c, selected)
					}
				})
			}
		})
	})
	if as.editorOpen {
		open := true
		modalDragModal(c, "automation-editor-modal", &open, func() {
			ui.Column(c.Key("automation-editor-card")).MaxWidth(min(automationEditorMaxWidth, max(0, width-modalDragPanelInset))).MaxHeight(max(0, height-modalDragPanelInset)).Background(ThemeSurface(c)).Radius(RadiusLG).Padding(Space5, Space5).Gap(Space4).Children(func() {
				renderAutomationEditor(c, s, as, as.isNew, as.selectedID, as.formErr, as.saving, as.confirmDelete, as.runningIDs, as.busyIDs, 0, as.includeArchived, as.runsLoading, as.runsErr, as.runs, as.runRead, as.runArchived, as.openRunID)
			})
		})
		if !open {
			as.closeEditor()
			s.invalidate()
		}
	}
}
