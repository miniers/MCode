package mcodeui

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

const (
	memoryModalPadding float32 = Space5
	// panelHeaderHeight 是记忆与上下文面板标题行共用的高度。
	panelHeaderHeight float32 = 56
	// memoryEmptyIconSize 是空态插图的图标边长。
	memoryEmptyIconSize float32 = 28
	// memoryEmptyHintMaxWidth 是空态说明文字的最大宽度，超出换行。
	memoryEmptyHintMaxWidth float32 = 420
	// memoryEditTextAreaHeight 是编辑记忆正文输入框的高度。
	memoryEditTextAreaHeight float32 = 240
	// memoryNewTextAreaHeight 是新建记忆正文输入框的高度。
	memoryNewTextAreaHeight float32 = 140
	memoryTabHeight         float32 = ControlMD
)

// memoryPanelSnapshot 复制渲染需要的状态，避免渲染过程长时间持锁。
type memoryPanelSnapshot struct {
	backend     string
	open        bool
	tab         int
	projectPath string
	loading     bool
	distilling  bool
	hasThread   bool
	err         string
	busyID      string
	expanded    string

	proposals []MemoryProposal

	editTitle      string
	editContent    string
	editUnitType   string
	editImportance string

	similarFor     string
	similarItems   []MemoryItem
	similarLoading bool
	similarErr     string

	condensing  bool
	condenseErr string

	creating    bool
	newTitle    string
	newContent  string
	newUnitType string

	searchQuery   string
	searchResults []MemoryItem
	searchLoading bool
	searchErr     string
	searched      bool

	// 忘掉：forgetConfirmID 是已进入确认态的记忆 ID，forgettingID 是正在请求的那一条。
	forgetConfirmID string
	forgettingID    string
	// searchNotice 是搜索页的操作结果提示（已删除 / 已标记废弃 / 已导出到某路径）。
	searchNotice string
	exporting    bool

	wmText    string
	wmLoading bool
	wmErr     string
	wmLoaded  bool

	pendingCount int
}

func snapshotMemoryPanel(s *chatState) memoryPanelSnapshot {
	st := getMemoryPanelState(s)
	if st == nil {
		return memoryPanelSnapshot{}
	}
	hasThread := selectedThreadIDSnapshot(s) != ""
	backend := "nmem"
	if s.client != nil {
		backend = s.client.MemoryBackend()
	}
	memoryPanelMu.Lock()
	defer memoryPanelMu.Unlock()
	if backend == "local" && st.tab == memoryTabWorking {
		st.tab = memoryTabPending
	}
	return memoryPanelSnapshot{
		backend:         backend,
		open:            st.open,
		tab:             st.tab,
		projectPath:     st.projectPath,
		loading:         st.loading,
		distilling:      st.distilling,
		hasThread:       hasThread,
		err:             st.err,
		busyID:          st.busyID,
		expanded:        st.expanded,
		proposals:       append([]MemoryProposal(nil), st.proposals...),
		editTitle:       st.editTitle,
		editContent:     st.editContent,
		editUnitType:    st.editUnitType,
		editImportance:  st.editImportance,
		similarFor:      st.similarFor,
		similarItems:    append([]MemoryItem(nil), st.similarItems...),
		similarLoading:  st.similarLoading,
		similarErr:      st.similarErr,
		condensing:      st.condensing,
		condenseErr:     st.condenseErr,
		creating:        st.creating,
		newTitle:        st.newTitle,
		newContent:      st.newContent,
		newUnitType:     st.newUnitType,
		searchQuery:     st.searchQuery,
		searchResults:   append([]MemoryItem(nil), st.searchResults...),
		searchLoading:   st.searchLoading,
		searchErr:       st.searchErr,
		searched:        st.searched,
		forgetConfirmID: st.forgetConfirmID,
		forgettingID:    st.forgettingID,
		searchNotice:    st.searchNotice,
		exporting:       st.exporting,
		wmText:          st.wmText,
		wmLoading:       st.wmLoading,
		wmErr:           st.wmErr,
		wmLoaded:        st.wmLoaded,
		pendingCount:    st.pendingCount,
	}
}

// renderMemoryModal 在根层挂载记忆面板（680×600，居中卡片）。外层用 DialogBase，避免卡片外多一圈黑边。
func renderMemoryModal(c *ui.Context, s *chatState) {
	st := getMemoryPanelState(s)
	if st == nil {
		return
	}
	memoryPanelMu.Lock()
	open := st.open
	loadedPath := st.projectPath
	memoryPanelMu.Unlock()
	if !open {
		modalDragReset(modalDragMemoryPanel)
		return
	}
	// 面板可能在项目列表异步加载完成前打开：路径就绪后补一次加载，避免一直停在空态。
	if loadedPath == "" {
		if path := s.getSelectedProjectPath(); path != "" {
			s.reloadMemoryProposals()
		}
	}

	windowWidth, windowHeight := c.Size()
	width, height := modalResizeSize(modalDragMemoryPanel, min(MemoryPanelWidth, windowWidth-80), min(MemoryPanelHeight, windowHeight-80), min(360, max(0, windowWidth-modalDragPanelInset)), min(240, max(0, windowHeight-modalDragPanelInset)), windowWidth, windowHeight)

	Sheet(c, SheetProps{Key: modalDragMemoryPanel, CardKey: "memory-modal-card", Label: "memory-modal-card", Open: &open, Width: width, Height: height}, func() {
		renderMemoryHeader(c, s)
		renderMemoryTabs(c, s)
		renderMemoryBody(c, s)
		modalResizeGrips(c, modalDragMemoryPanel, width, height)
	}).Clip()
	if !open {
		modalDragReset(modalDragMemoryPanel)
		s.closeMemoryPanel()
	}
}

// renderMemoryHeader 渲染头部（56 高）：图标块、标题与副标题、「完成」。
func renderMemoryHeader(c *ui.Context, s *chatState) {
	snap := snapshotMemoryPanel(s)
	modalDragHandle(c, modalDragMemoryPanel, ui.Row(c.Key("memory-modal-header")).
		Height(panelHeaderHeight).
		FillWidth().
		Padding(0, memoryModalPadding).
		Gap(Space4).
		AlignItems(ui.Center).
		BorderWidth(0, 0, Hairline, 0).
		BorderColor(ThemeBorder(c)).
		Children(func() {
			ui.Row(c.Key("memory-header-icon")).
				Size(ControlLG, ControlLG).
				Radius(RadiusMD).
				Center().
				Background(ThemeAccent(c).Alpha(0.18)).
				Children(func() {
					ui.Icon(c, IconBrain()).Size(IconMD, IconMD).TextColor(ThemeAccent(c))
				})

			ui.Column(c.Key("memory-header-titles")).
				Gap(Space0).
				Justify(ui.Center).
				Children(func() {
					ui.Text(c, "记忆").
						FontSize(TypeProse).
						FontWeight(600).
						TextColor(ThemePrimaryText(c))
					subtitle := "Nowledge Mem • 写入前必须由你确认"
					if snap.backend == "local" {
						subtitle = "本地文档 • 写入前必须由你确认"
					}
					ui.Text(c, subtitle).
						FontSize(TypeCaption).
						Font("monospace").
						TextColor(ThemeSecondaryText(c))
				})

			ui.Spacer(c)

			Button(c.Key("memory-refresh-btn"), ButtonOpts{
				Icon: IconRefreshCw(),
				Text: "刷新",
				OnClick: func() {
					s.reloadMemoryProposals()
					s.refreshPendingMemoryCount(true)
					if snap.tab == memoryTabWorking {
						s.reloadWorkingMemory()
					}
				},
				Variant: ButtonSecondary,
			})

			Button(c.Key("memory-done-btn"), ButtonOpts{
				Text:    "完成",
				Variant: ButtonPrimary,
				OnClick: func() { s.closeMemoryPanel() },
			})
		}))
}

// renderMemoryTabs 渲染三个标签页：待审、搜索、Working Memory。
func renderMemoryTabs(c *ui.Context, s *chatState) {
	snap := snapshotMemoryPanel(s)
	ui.Row(c.Key("memory-modal-tabs")).
		FillWidth().
		Padding(Space3, memoryModalPadding).
		Gap(Space2).
		AlignItems(ui.Center).
		BorderWidth(0, 0, Hairline, 0).
		BorderColor(ThemeBorder(c)).
		Children(func() {
			renderMemoryTabButton(c, s, memoryTabPending, "待审", snap.tab == memoryTabPending, snap.pendingCount)
			renderMemoryTabButton(c, s, memoryTabSearch, "搜索", snap.tab == memoryTabSearch, 0)
			if snap.backend != "local" {
				renderMemoryTabButton(c, s, memoryTabWorking, "Working Memory", snap.tab == memoryTabWorking, 0)
			}
			ui.Spacer(c)
			ui.Text(c, snap.projectPath).
				FontSize(TypeCaption).
				Font("monospace").
				SingleLine().
				Shrink(1).
				MinWidth(0).
				TextColor(ThemeSecondaryText(c))
		})
}

// renderMemoryTabButton 渲染单个标签页按钮，count>0 时显示数量徽标。
func renderMemoryTabButton(c *ui.Context, s *chatState, tab int, label string, active bool, count int) {
	TabButton(c, TabButtonProps{Key: "memory-tab-" + strconv.Itoa(tab), Label: label, Active: active}, func() {
		ui.Text(c, label).FontSize(TypeFootnote).SingleLine().TextColor(ThemePrimaryText(c))
		if count > 0 {
			tone := ToneNeutral
			if active {
				tone = ToneAccent
			}
			Badge(c, "memory-pending-count", strconv.Itoa(count), tone)
		}
	}).Height(memoryTabHeight).Gap(Space2).AlignItems(ui.Center).OnClick(func() { s.switchMemoryTab(tab) })
}

// renderMemoryBody 渲染当前标签页内容。
func renderMemoryBody(c *ui.Context, s *chatState) {
	snap := snapshotMemoryPanel(s)
	switch snap.tab {
	case memoryTabSearch:
		renderMemorySearchTab(c, s, snap)
	case memoryTabWorking:
		renderMemoryWorkingTab(c, s, snap)
	default:
		renderMemoryPendingTab(c, s, snap)
	}
}

// renderMemoryErrorBanner 渲染红色错误条，带「忽略」。
func renderMemoryErrorBanner(c *ui.Context, s *chatState, key, msg string) {
	Banner(c, BannerProps{
		Key:      "memory-error-banner-" + key,
		Tone:     ToneDanger,
		Text:     msg,
		Action:   "忽略",
		OnAction: func() { s.dismissMemoryPanelError() },
	})
}

// renderMemoryPendingTab 渲染待审页：提议列表与底部「新建记忆」。
func renderMemoryPendingTab(c *ui.Context, s *chatState, snap memoryPanelSnapshot) {
	ui.Column(c.Key("memory-pending-view")).Grow(1).FillWidth().Children(func() {
		ui.Row(c.Key("memory-pending-hint")).
			FillWidth().
			Padding(Space3, memoryModalPadding).
			Gap(Space3).
			AlignItems(ui.Center).
			Children(func() {
				ui.Icon(c, IconSparkles()).Size(IconSM, IconSM).TextColor(ThemeAccent(c))
				hint := "Agent 提议的记忆，只有你接受后才会写入 Nowledge Mem；你在对话里明确要求记住的会直接写入；也可以点「提炼本会话」从当前会话提炼候选记忆"
				if snap.backend == "local" {
					hint = "Agent 提议的记忆，只有你接受后才会写入本地文档；你在对话里明确要求记住的会直接写入"
				}
				ui.Text(c, hint).
					FontSize(TypeCaption).
					TextColor(ThemeSecondaryText(c))
			})

		if snap.err != "" {
			renderMemoryErrorBanner(c, s, "panel", snap.err)
		}

		if len(snap.proposals) == 0 {
			ui.Column(c.Key("memory-pending-empty")).
				Grow(1).
				FillWidth().
				Justify(ui.Center).
				AlignItems(ui.Center).
				Gap(Space3).
				Children(func() {
					if snap.loading {
						LoadingIndicator(c, "memory-pending-loading", "", IconMD)
						return
					}
					EmptyState(c, EmptyStateProps{Icon: IconBrain(), Title: "暂无待审记忆", Description: "Agent 通过 memory_propose 提交提议后，会在这里等待你接受或拒绝。"})
				})
		} else {
			ui.Scroll(c.Key("memory-pending-scroll")).
				Grow(1).
				FillWidth().
				Padding(memoryModalPadding).
				Children(func() {
					ui.Column(c.Key("memory-pending-list")).FillWidth().Gap(Space3).Children(func() {
						for _, p := range snap.proposals {
							renderMemoryProposalCard(c, s, snap, p)
						}
					})
				})
		}

		ui.Row(c.Key("memory-pending-footer")).
			FillWidth().
			Padding(Space3, memoryModalPadding).
			Gap(Space3).
			AlignItems(ui.Center).
			BorderWidth(Hairline, 0, 0, 0).
			BorderColor(ThemeBorder(c)).
			Children(func() {
				ui.Text(c, "待审 "+strconv.Itoa(len(snap.proposals))+" 条").
					FontSize(TypeCaption).
					TextColor(ThemeSecondaryText(c))
				ui.Spacer(c)
				if snap.backend != "local" {
					distillText := "提炼本会话"
					if snap.distilling {
						distillText = "提炼中…"
					}
					Button(c.Key("memory-distill-btn"), ButtonOpts{
						Icon:     IconSparkles(),
						Text:     distillText,
						Disabled: !snap.hasThread || snap.distilling,
						OnClick:  func() { s.distillCurrentThread() },
						Variant:  ButtonSecondary,
					})
				}
				Button(c.Key("memory-new-btn"), ButtonOpts{
					Icon:    IconPlus(),
					Text:    "新建记忆",
					Variant: ButtonPrimary,
					OnClick: func() { s.requestCreateMemoryProposal() },
				})
			})
	})

	if snap.creating {
		renderMemoryCreateDialog(c, s, snap)
	}
}

// memoryUnitTypeOptions 是编辑区可选的固定记忆类型，顺序即展示顺序。
var memoryUnitTypeOptions = []string{"fact", "preference", "decision", "plan", "procedure", "learning", "context", "event"}

// renderMemoryProposalCard 渲染一张提议卡片：标题、类型、重要度、来源、线程、
// 内容摘要（展开后编辑标题/内容/类型/重要度）、错误条，以及写入空间与「接受并写入」「拒绝」。
func renderMemoryProposalCard(c *ui.Context, s *chatState, snap memoryPanelSnapshot, p MemoryProposal) {
	expanded := snap.expanded == p.ID
	busy := snap.busyID == p.ID
	ui.Column(c.Key("memory-proposal-card-" + p.ID)).
		FillWidth().
		Padding(Space4).
		Gap(Space3).
		Radius(RadiusMD).
		Background(ThemeOverlay(c, 0.22)).
		Children(func() {
			// 标题独占一行，点击展开编辑；长标题最多两行，不与元信息、按钮抢宽度。
			ui.Row(c.Key("memory-proposal-header-" + p.ID)).
				FillWidth().
				Gap(Space3).
				AlignItems(ui.Start).
				Children(func() {
					contextBaseButton(c, "memory-proposal-toggle-"+p.ID, p.Title).
						FillWidth().
						Grow(1).
						Shrink(1).
						MinWidth(0).
						OnClick(func() { s.toggleMemoryProposalExpand(p.ID) }).
						Children(func() {
							ui.Icon(c, IconBrain()).Size(IconSM, IconSM).Shrink(0).TextColor(ThemeAccent(c))
							ui.Text(c, p.Title).
								FontSize(TypeFootnote).
								FontWeight(600).
								MaxLines(2).
								Grow(1).
								Shrink(1).
								MinWidth(0).
								TextColor(ThemePrimaryText(c))
						})
				})

			// 元信息允许换行，窄面板下收缩为多行而不是溢出卡片。
			ui.Row(c.Key("memory-proposal-meta-" + p.ID)).
				FillWidth().
				Wrap().
				Gap(Space2).
				GapY(Space1).
				AlignItems(ui.Center).
				Children(func() {
					memoryChip(c, memoryUnitTypeLabel(p.UnitType))
					memoryChip(c, "重要度 "+formatImportance(p.Importance))
					memoryChip(c, memorySourceLabel(p.Source))
					threadText, threadLabel := memoryThreadChip(p.ThreadID)
					memoryChipLabel(c, threadText, threadLabel)
					if p.TargetMemoryID != "" {
						memoryChip(c, "改写已有记忆")
					}
				})

			if p.Error != "" {
				Banner(c, BannerProps{
					Key:  "memory-proposal-error-" + p.ID,
					Tone: ToneDanger,
					Text: p.Error,
				})
			}

			if expanded {
				title := snap.editTitle
				content := snap.editContent
				unit := strings.TrimSpace(snap.editUnitType)
				importance := snap.editImportance
				contentRunes := utf8.RuneCountInString(content)
				ui.Column(c.Key("memory-proposal-editor-" + p.ID)).FillWidth().Gap(Space3).Children(func() {
					// 已选合并目标时提醒：接受会用当前草稿覆盖已有记忆。
					if p.TargetMemoryID != "" {
						Banner(c, BannerProps{
							Key:  "memory-edit-target-hint-" + p.ID,
							Tone: ToneWarning,
							Text: "接受后会用当前标题和内容覆盖已有记忆，建议先点「AI 合并精简」",
						})
					}
					ui.TextInput(c.Key("memory-edit-title-"+p.ID), &title).
						Label("标题").
						FillWidth().
						OnChange(func() { s.setMemoryEdit("title", title) })
					ui.TextArea(c.Key("memory-edit-content-"+p.ID), &content).
						Label("内容").
						Height(memoryEditTextAreaHeight).
						FillWidth().
						FontSize(TypeFootnote).
						Font("monospace").
						LineHeight(1.4).
						TextColor(ThemePrimaryText(c)).
						OnChange(func() { s.setMemoryEdit("content", content) })
					ui.Row(c.Key("memory-edit-count-" + p.ID)).
						FillWidth().
						Wrap().
						Gap(Space3).
						GapY(Space1).
						AlignItems(ui.Center).
						Children(func() {
							countColor := ThemeSecondaryText(c)
							if contentRunes > 600 {
								countColor = ThemeWarning(c)
							}
							ui.Text(c, strconv.Itoa(contentRunes)+" 字").
								FontSize(TypeCaption).
								TextColor(countColor)
							if contentRunes > 600 {
								ui.Text(c, "建议精简到 600 字以内：检索摘要只取前 400 字").
									FontSize(TypeCaption).
									TextColor(ThemeWarning(c))
							}
							ui.Spacer(c).Grow(1).Shrink(1).MinWidth(0)
							if snap.condensing {
								LoadingIndicator(c, nil, "", IconSM)
							}
							condenseText := "AI 精简"
							if p.TargetMemoryID != "" {
								condenseText = "AI 合并精简"
							}
							Button(c.Key("memory-edit-condense-"+p.ID), ButtonOpts{
								Text:     condenseText,
								Size:     ControlXS,
								Disabled: snap.condensing || busy,
								OnClick:  func() { s.condenseMemoryDraft() },
								Variant:  ButtonSecondary,
							}).Shrink(0)
							if snap.condenseErr != "" {
								ui.Text(c, snap.condenseErr).FontSize(TypeCaption).TextColor(ThemeDanger(c))
							}
						})
					ui.Column(c.Key("memory-edit-unit-" + p.ID)).FillWidth().Gap(Space2).Children(func() {
						ui.Text(c, "类型").FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
						ui.Row(c.Key("memory-edit-unit-options-" + p.ID)).
							FillWidth().
							Wrap().
							Gap(Space2).
							GapY(Space2).
							AlignItems(ui.Center).
							Children(func() {
								for _, option := range memoryUnitTypeOptions {
									unitOption := option
									variant := ButtonSecondary
									if unitOption == unit {
										variant = ButtonPrimary
									}
									Button(c.Key("memory-edit-unit-"+p.ID+"-"+unitOption), ButtonOpts{
										Text:     memoryUnitTypeLabel(unitOption),
										Variant:  variant,
										Size:     ControlXS,
										Disabled: busy,
										OnClick: func() {
											s.setMemoryEdit("unit", unitOption)
											s.invalidate()
										},
									}).Shrink(0)
								}
							})
					})
					if len(p.Labels) > 0 {
						ui.Row(c.Key("memory-edit-labels-" + p.ID)).
							FillWidth().
							Wrap().
							Gap(Space2).
							GapY(Space1).
							AlignItems(ui.Center).
							Children(func() {
								ui.Text(c, "标签").FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
								for _, label := range p.Labels {
									memoryChip(c, label)
								}
							})
					}
					ui.Row(c.Key("memory-edit-actions-" + p.ID)).
						FillWidth().
						Wrap().
						Gap(Space3).
						GapY(Space2).
						AlignItems(ui.Center).
						Children(func() {
							ui.TextInput(c.Key("memory-edit-importance-"+p.ID), &importance).
								Label("重要度 (0~1)").
								Placeholder("0.5").
								Width(140).
								Shrink(0).
								OnChange(func() { s.setMemoryEdit("importance", importance) })
							ui.Spacer(c).Grow(1).Shrink(1).MinWidth(0)
							Button(c.Key("memory-edit-save-"+p.ID), ButtonOpts{
								Text:     "保存修改",
								Variant:  ButtonPrimary,
								Disabled: busy,
								OnClick:  func() { s.saveMemoryProposalEdits() },
							}).Shrink(0)
							Button(c.Key("memory-edit-collapse-"+p.ID), ButtonOpts{
								Text:    "收起",
								OnClick: func() { s.toggleMemoryProposalExpand(p.ID) },
								Variant: ButtonSecondary,
							}).Shrink(0)
						})
				})
				// 相似记忆区块放在编辑区下方，供一键设为合并目标。
				renderMemorySimilarBlock(c, s, snap, p, busy)
			} else {
				contextBaseButton(c, "memory-proposal-summary-"+p.ID, "展开编辑："+p.Title).
					FillWidth().
					AlignItems(ui.Start).
					OnClick(func() { s.toggleMemoryProposalExpand(p.ID) }).
					Children(func() {
						ui.Text(c, p.Content).
							FontSize(TypeCaption).
							Font("monospace").
							MaxLines(3).
							FillWidth().
							TextColor(ThemeSecondaryText(c))
					})
			}

			// 写入空间与操作按钮单独一行：左侧两个选项始终可见，右侧按钮不收缩。
			ui.Row(c.Key("memory-proposal-actions-" + p.ID)).
				FillWidth().
				Wrap().
				Gap(Space2).
				GapY(Space2).
				AlignItems(ui.Center).
				Children(func() {
					if p.TargetMemoryID == "" {
						ui.Text(c, "写入到").FontSize(TypeCaption).Shrink(0).TextColor(ThemeSecondaryText(c))
						renderMemoryScopeOption(c, s, p, memoryScopeProject, "当前项目", busy)
						renderMemoryScopeOption(c, s, p, memoryScopeGlobal, "全局", busy)
					}
					ui.Spacer(c).Grow(1).Shrink(1).MinWidth(0)

					editText := "查看 / 编辑"
					if expanded {
						editText = "收起"
					}
					Button(c.Key("memory-proposal-edit-"+p.ID), ButtonOpts{
						Text:    editText,
						OnClick: func() { s.toggleMemoryProposalExpand(p.ID) },
						Variant: ButtonSecondary,
					}).Shrink(0)
					if busy {
						LoadingIndicator(c, nil, "", IconSM)
					}
					Button(c.Key("memory-proposal-reject-"+p.ID), ButtonOpts{
						Icon:     IconX(),
						Text:     "拒绝",
						Variant:  ButtonDanger,
						Disabled: busy,
						OnClick:  func() { s.rejectMemoryProposal(p.ID) },
					}).Shrink(0)
					Button(c.Key("memory-proposal-accept-"+p.ID), ButtonOpts{
						Icon:     IconCheck(),
						Text:     "接受并写入",
						Variant:  ButtonPrimary,
						Disabled: busy,
						OnClick:  func() { s.acceptMemoryProposal(p.ID) },
					}).Shrink(0)
				})
		})
}

// renderMemorySimilarBlock 渲染「相似的已有记忆」：加载中显示 spinner，无结果显示空态，
// 否则逐条列出标题、内容前两行与类型，并在右侧提供设为/取消合并目标的按钮。
func renderMemorySimilarBlock(c *ui.Context, s *chatState, snap memoryPanelSnapshot, p MemoryProposal, busy bool) {
	if snap.similarFor != p.ID {
		return
	}
	ui.Column(c.Key("memory-similar-" + p.ID)).FillWidth().Gap(Space2).Children(func() {
		ui.Text(c, "相似的已有记忆").FontSize(TypeCaption).FontWeight(600).TextColor(ThemeSecondaryText(c))
		switch {
		case snap.similarLoading:
			ui.Row(c.Key("memory-similar-loading-" + p.ID)).
				FillWidth().
				Gap(Space2).
				AlignItems(ui.Center).
				Children(func() {
					LoadingIndicator(c, nil, "正在检索相似记忆…", IconSM).Grow(1).Shrink(1).MinWidth(0)
				})
		case snap.similarErr != "":
			ui.Text(c, snap.similarErr).FontSize(TypeCaption).FillWidth().TextColor(ThemeDanger(c))
		case len(snap.similarItems) == 0:
			ui.Text(c, "没有找到相似记忆").FontSize(TypeCaption).FillWidth().TextColor(ThemeSecondaryText(c))
		default:
			for _, item := range snap.similarItems {
				renderMemorySimilarItem(c, s, p, item, busy)
			}
		}
	})
}

// renderMemorySimilarItem 渲染一条相似记忆；已选为合并目标时改为 accent 文字加「取消合并」。
func renderMemorySimilarItem(c *ui.Context, s *chatState, p MemoryProposal, item MemoryItem, busy bool) {
	selected := p.TargetMemoryID != "" && p.TargetMemoryID == item.ID
	ui.Row(c.Key("memory-similar-item-"+p.ID+"-"+item.ID)).
		FillWidth().
		Padding(Space2, Space3).
		Gap(Space3).
		AlignItems(ui.Start).
		Radius(RadiusSM).
		Background(ThemeOverlay(c, 0.16)).
		Children(func() {
			ui.Column(c.Key("memory-similar-body-" + p.ID + "-" + item.ID)).
				Grow(1).
				Shrink(1).
				MinWidth(0).
				Gap(Space1).
				Children(func() {
					ui.Text(c, item.Title).
						FontSize(TypeCaption).
						FontWeight(600).
						SingleLine().
						FillWidth().
						Shrink(1).
						MinWidth(0).
						TextColor(ThemePrimaryText(c))
					ui.Text(c, item.Content).
						FontSize(TypeCaption).
						Font("monospace").
						MaxLines(2).
						FillWidth().
						Shrink(1).
						MinWidth(0).
						TextColor(ThemeSecondaryText(c))
					ui.Row(c.Key("memory-similar-meta-" + p.ID + "-" + item.ID)).
						FillWidth().
						Wrap().
						Gap(Space2).
						GapY(Space1).
						AlignItems(ui.Center).
						Children(func() {
							memoryChip(c, memoryUnitTypeLabel(item.UnitType))
						})
				})
			ui.Column(c.Key("memory-similar-action-" + p.ID + "-" + item.ID)).
				Shrink(0).
				Gap(Space1).
				AlignItems(ui.End).
				Children(func() {
					if selected {
						ui.Text(c, "已选为合并目标").
							FontSize(TypeCaption).
							SingleLine().
							Shrink(0).
							TextColor(ThemeAccent(c))
						Button(c.Key("memory-similar-clear-"+p.ID+"-"+item.ID), ButtonOpts{
							Text:     "取消合并",
							Size:     ControlSM,
							Disabled: busy,
							OnClick:  func() { s.setMemoryProposalTarget(p.ID, "") },
							Variant:  ButtonSecondary,
						}).Shrink(0)
						return
					}
					Button(c.Key("memory-similar-target-"+p.ID+"-"+item.ID), ButtonOpts{
						Text:     "合并到这条",
						Size:     ControlSM,
						Disabled: busy,
						OnClick:  func() { s.setMemoryProposalTarget(p.ID, item.ID) },
						Variant:  ButtonSecondary,
					}).Shrink(0)
				})
		})
}

// renderMemoryScopeOption 渲染写入空间的单个选项：选中项用主色高亮，点击未选中项才切换。
func renderMemoryScopeOption(c *ui.Context, s *chatState, p MemoryProposal, scope, text string, busy bool) {
	selected := (p.Scope == memoryScopeGlobal) == (scope == memoryScopeGlobal)
	variant := ButtonSecondary
	if selected {
		variant = ButtonPrimary
	}
	props := ButtonOpts{
		Text:     text,
		Label:    text,
		Variant:  variant,
		Size:     ControlSM,
		Disabled: busy,
	}
	if !selected {
		props.OnClick = func() { s.toggleMemoryProposalScope(p.ID) }
	}
	Button(c.Key("memory-proposal-scope-"+scope+"-"+p.ID), props).Shrink(0)
}

// memoryChip 渲染一枚只读元信息徽标。
func memoryChip(c *ui.Context, text string) {
	memoryChipLabel(c, text, text)
}

// memoryChipLabel 渲染徽标，并单独指定悬停/无障碍名称（用于完整线程 ID 等）。
func memoryChipLabel(c *ui.Context, text, label string) {
	chip := Badge(c, nil, text, ToneNeutral)
	if label != "" {
		chip.Label(label)
	}
}

// memoryUnitTypeLabel 把记忆类型转成中文标签（未识别的类型原样显示）。
func memoryUnitTypeLabel(unit string) string {
	switch strings.TrimSpace(unit) {
	case "fact":
		return "事实"
	case "insight":
		return "洞见"
	case "preference":
		return "偏好"
	case "decision":
		return "决策"
	case "plan":
		return "计划"
	case "procedure":
		return "流程"
	case "learning":
		return "经验"
	case "context":
		return "背景"
	case "event":
		return "事件"
	case "digest":
		return "汇总"
	case "":
		return "未分类"
	default:
		return unit
	}
}

// memorySourceLabel 把提议来源转成中文标签。
func memorySourceLabel(source string) string {
	switch strings.TrimSpace(source) {
	case "agent":
		return "来自 Agent"
	case "user":
		return "我手动创建"
	case "distill":
		return "会话提炼"
	case "inbox-migration":
		return "旧收件箱迁移"
	case "":
		return "未知来源"
	default:
		return source
	}
}

// memoryThreadChip 返回线程徽标的短文案与完整名称：列表只显示 ID 前 8 位，完整 ID 放进 Label。
func memoryThreadChip(threadID string) (text, label string) {
	id := strings.TrimSpace(threadID)
	if id == "" {
		return "无线程", "无线程"
	}
	runes := []rune(id)
	if len(runes) <= 8 {
		return "线程 " + id, "线程 " + id
	}
	return "线程 " + string(runes[:8]), "线程 " + id
}

// renderMemoryCreateDialog 渲染「新建记忆」表单（来源记为用户，仍需接受后才写入）。
func renderMemoryCreateDialog(c *ui.Context, s *chatState, snap memoryPanelSnapshot) {
	title := snap.newTitle
	content := snap.newContent
	unit := snap.newUnitType
	winW, winH := c.Size()
	width, height := modalResizeSize(modalDragMemoryCreate, 480, 450, min(360, max(0, winW-modalDragPanelInset)), min(240, max(0, winH-modalDragPanelInset)), winW, winH)
	open := true
	Sheet(c, SheetProps{Key: modalDragMemoryCreate, CardKey: "memory-create-card", Label: "memory-create-card", Open: &open, Width: width, Height: height}, func() {
		ui.Column(c).
			FillWidth().Grow(1).
			Padding(Space5).
			Gap(Space4).
			Children(func() {
				modalDragHandle(c, modalDragMemoryCreate, ui.Row(c.Key("memory-create-title-row")).FillWidth().Children(func() {
					ui.Text(c, "新建记忆").FontSize(TypeTitle).FontWeight(600).TextColor(ThemePrimaryText(c))
				}))
				ui.Text(c, "手动记录一条记忆，来源记为「我」，加入待审列表后再确认写入。").
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c))
				TextField(c, TextFieldProps{Key: "memory-new-title", Value: &title, Label: "标题", Placeholder: "部署只用 make deploy", FillWidth: true, AutoFocus: true, OnChange: func(value string) { s.setMemoryNewField("title", value) }})
				ui.TextArea(c.Key("memory-new-content"), &content).
					Label("内容").
					Height(max(40, height-300)).
					FillWidth().
					FontSize(TypeFootnote).
					Font("monospace").
					LineHeight(1.4).
					TextColor(ThemePrimaryText(c)).
					OnChange(func() { s.setMemoryNewField("content", content) })
				TextField(c, TextFieldProps{Key: "memory-new-unit", Value: &unit, Label: "类型", Placeholder: "fact / insight / preference", FillWidth: true, OnChange: func(value string) { s.setMemoryNewField("unit", value) }})
				FormActions(c, "memory-create-actions", func() {
					Button(c.Key("memory-create-cancel-btn"), ButtonOpts{
						Text: "取消",
						OnClick: func() {
							modalDragReset(modalDragMemoryCreate)
							s.cancelCreateMemoryProposal()
						},
						Variant: ButtonSecondary,
					})
					Button(c.Key("memory-create-confirm-btn"), ButtonOpts{
						Text:    "加入待审",
						Variant: ButtonPrimary,
						OnClick: func() {
							modalDragReset(modalDragMemoryCreate)
							s.createMemoryProposal()
						},
					})
				})
				modalResizeGrips(c, modalDragMemoryCreate, width, height)
			})
	})
	if !open {
		modalDragReset(modalDragMemoryCreate)
		s.cancelCreateMemoryProposal()
	}
}

// renderMemorySearchTab 渲染搜索页：输入框、搜索按钮与结果列表。
func renderMemorySearchTab(c *ui.Context, s *chatState, snap memoryPanelSnapshot) {
	query := snap.searchQuery
	ui.Column(c.Key("memory-search-view")).Grow(1).FillWidth().Children(func() {
		ui.Row(c.Key("memory-search-bar")).
			FillWidth().
			Padding(Space3, memoryModalPadding).
			Gap(Space3).
			AlignItems(ui.Center).
			Children(func() {
				SearchField(c, TextFieldProps{Key: "memory-search-input", Value: &query, Label: "搜索记忆", Placeholder: "例如：部署流程、测试约定", Grow: true, OnChange: func(value string) { s.editMemorySearchQuery(value) }})
				Button(c.Key("memory-search-btn"), ButtonOpts{
					Icon:    IconSearch(),
					Text:    "搜索",
					Label:   "执行搜索",
					Variant: ButtonPrimary,
					OnClick: func() { s.searchMemories() },
				})
				exportText := "导出"
				if snap.exporting {
					exportText = "导出中…"
				}
				Button(c.Key("memory-search-export-btn"), ButtonOpts{
					Icon:     IconFile(),
					Text:     exportText,
					Label:    "导出记忆",
					Disabled: snap.exporting,
					OnClick:  func() { s.exportMemories() },
					Variant:  ButtonSecondary,
				}).Shrink(0)
			})

		if snap.searchErr != "" {
			renderMemoryErrorBanner(c, s, "search", snap.searchErr)
		}

		if snap.searchNotice != "" {
			ui.Row(c.Key("memory-search-notice")).
				FillWidth().
				Padding(Space2, memoryModalPadding).
				Gap(Space3).
				AlignItems(ui.Center).
				Children(func() {
					ui.Icon(c, IconCheck()).Size(IconSM, IconSM).Shrink(0).TextColor(ThemeAccent(c))
					ui.Text(c, snap.searchNotice).
						FontSize(TypeCaption).
						Grow(1).
						Shrink(1).
						MinWidth(0).
						TextColor(ThemeSecondaryText(c))
				})
		}

		if snap.searchLoading {
			ui.Row(c.Key("memory-search-loading")).FillWidth().Padding(memoryModalPadding).Gap(Space3).AlignItems(ui.Center).Children(func() {
				LoadingIndicator(c, nil, "正在检索…", IconSM)
			})
			return
		}

		if len(snap.searchResults) == 0 {
			ui.Column(c.Key("memory-search-empty")).
				Grow(1).
				FillWidth().
				Justify(ui.Center).
				AlignItems(ui.Center).
				Gap(Space3).
				Children(func() {
					hint := "搜索当前项目对应空间中的记忆，结果只读。"
					if snap.backend == "local" {
						hint = "搜索当前项目的记忆，结果只读。"
					}
					EmptyState(c, EmptyStateProps{Icon: IconSearch(), Title: memorySearchEmptyTitle(snap), Description: hint})
				})
			return
		}

		ui.Scroll(c.Key("memory-search-scroll")).
			Grow(1).
			FillWidth().
			Padding(memoryModalPadding).
			Children(func() {
				ui.Column(c.Key("memory-search-list")).FillWidth().Gap(Space3).Children(func() {
					for _, item := range snap.searchResults {
						renderMemoryItemCard(c, s, snap, item)
					}
				})
			})
	})
}

// memorySearchEmptyTitle 按是否已搜索给出空态标题。
func memorySearchEmptyTitle(snap memoryPanelSnapshot) string {
	if snap.searched {
		return "没有找到匹配的记忆"
	}
	return "输入关键字后按「搜索」"
}

// renderMemoryItemCard 渲染一条搜索结果；行内可以直接两步确认「忘掉」这条记忆。
func renderMemoryItemCard(c *ui.Context, s *chatState, snap memoryPanelSnapshot, item MemoryItem) {
	confirming := snap.forgetConfirmID == item.ID
	forgetting := snap.forgettingID == item.ID
	forgetText := "忘掉"
	if confirming {
		forgetText = "确认忘掉"
	}
	ui.Column(c.Key("memory-item-card-" + item.ID)).
		FillWidth().
		Padding(Space4).
		Gap(Space2).
		Radius(RadiusMD).
		Background(ThemeOverlay(c, 0.22)).
		Children(func() {
			ui.Row(c.Key("memory-item-header-" + item.ID)).
				FillWidth().
				Gap(Space3).
				AlignItems(ui.Center).
				Children(func() {
					ui.Icon(c, IconNote()).Size(IconSM, IconSM).TextColor(ThemeAccent(c))
					ui.Text(c, item.Title).
						FontSize(TypeFootnote).
						FontWeight(600).
						SingleLine().
						Grow(1).
						Shrink(1).
						MinWidth(0).
						TextColor(ThemePrimaryText(c))
					memoryChip(c, memoryUnitTypeLabel(item.UnitType))
					memoryChip(c, "重要度 "+formatImportance(item.Importance))
					if !item.CreatedAt.IsZero() {
						ui.Text(c, item.CreatedAt.Format("2006-01-02 15:04")).
							FontSize(TypeCaption).
							Shrink(0).
							TextColor(ThemeSecondaryText(c))
					}
					if forgetting {
						LoadingIndicator(c, nil, "", IconSM)
					}
					if confirming {
						Button(c.Key("memory-item-forget-cancel-"+item.ID), ButtonOpts{
							Text:     "取消",
							Label:    "取消忘掉记忆：" + item.Title,
							Size:     ControlSM,
							Disabled: forgetting || snap.forgettingID != "",
							OnClick:  func() { s.cancelForgetMemory() },
							Variant:  ButtonSecondary,
						}).Shrink(0)
					}
					Button(c.Key("memory-item-forget-"+item.ID), ButtonOpts{
						Icon:     IconTrash2(),
						Text:     forgetText,
						Label:    "忘掉记忆：" + item.Title,
						Variant:  ButtonDanger,
						Size:     ControlSM,
						Disabled: forgetting || snap.forgettingID != "",
						OnClick:  func() { s.requestForgetMemory(item.ID) },
					}).Shrink(0)
				})
			ui.Text(c, item.Content).
				FontSize(TypeCaption).
				Font("monospace").
				MaxLines(4).
				FillWidth().
				TextColor(ThemeSecondaryText(c))
		})
}

// renderMemoryWorkingTab 渲染 Working Memory 只读页。
func renderMemoryWorkingTab(c *ui.Context, s *chatState, snap memoryPanelSnapshot) {
	ui.Column(c.Key("memory-working-view")).Grow(1).FillWidth().Children(func() {
		ui.Row(c.Key("memory-working-hint")).
			FillWidth().
			Padding(Space3, memoryModalPadding).
			Gap(Space3).
			AlignItems(ui.Center).
			Children(func() {
				ui.Icon(c, IconSparkles()).Size(IconSM, IconSM).TextColor(ThemeAccent(c))
				ui.Text(c, "今日 Working Memory（只读）").
					FontSize(TypeCaption).
					TextColor(ThemeSecondaryText(c))
				ui.Spacer(c)
				Button(c.Key("memory-working-refresh-btn"), ButtonOpts{
					Icon:    IconRefreshCw(),
					Text:    "刷新",
					OnClick: func() { s.reloadWorkingMemory() },
					Variant: ButtonSecondary,
				})
			})

		if snap.wmErr != "" {
			renderMemoryErrorBanner(c, s, "working", snap.wmErr)
		}

		if snap.wmLoading {
			ui.Column(c.Key("memory-working-loading")).
				Grow(1).
				FillWidth().
				Justify(ui.Center).
				AlignItems(ui.Center).
				Children(func() {
					LoadingIndicator(c, nil, "", IconMD)
				})
			return
		}

		if strings.TrimSpace(snap.wmText) == "" {
			ui.Column(c.Key("memory-working-empty")).
				Grow(1).
				FillWidth().
				Justify(ui.Center).
				AlignItems(ui.Center).
				Gap(Space3).
				Children(func() {
					ui.Icon(c, IconNotebook()).Size(memoryEmptyIconSize, memoryEmptyIconSize).TextColor(ThemeSecondaryText(c).Alpha(0.6))
					ui.Text(c, "今天还没有 Working Memory").
						FontSize(TypeProse).
						FontWeight(600).
						TextColor(ThemePrimaryText(c))
					ui.Text(c, "Nowledge Mem 会在整理当天记忆后生成 Working Memory。").
						FontSize(TypeFootnote).
						MaxWidth(memoryEmptyHintMaxWidth).
						TextAlign(ui.Center).
						TextColor(ThemeSecondaryText(c))
				})
			return
		}

		ui.Scroll(c.Key("memory-working-scroll")).Grow(1).FillWidth().Padding(memoryModalPadding).Children(func() {
			markdown.View(c.Key("memory-working-markdown"), snap.wmText, &s.mdStaticOpts)
		})
	})
}
