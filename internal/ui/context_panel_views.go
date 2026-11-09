package mcodeui

import (
	"path/filepath"

	"mcode/internal/config"
	"mcode/internal/projctx"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

const (
	contextModalPadding float32 = Space5
	contextChipGap      float32 = Space2
	contextChipHeight   float32 = ControlMD
	// contextDocRowHeight 是文档行在详情区顶部的高度。
	contextDocRowHeight float32 = 40
)

// renderContextModal 在根层挂载项目文档弹窗（680×600，居中卡片）。
func renderContextModal(c *ui.Context, s *chatState) {
	cp := getContextPanelState(s)
	if cp == nil {
		return
	}
	contextPanelMu.Lock()
	open := cp.open
	contextPanelMu.Unlock()
	if !open {
		modalDragReset(modalDragContextPanel)
		return
	}

	windowWidth, windowHeight := c.Size()
	width, height := modalResizeSize(modalDragContextPanel, min(ContextPanelWidth, windowWidth-80), min(ContextPanelHeight, windowHeight-80), min(360, max(0, windowWidth-modalDragPanelInset)), min(240, max(0, windowHeight-modalDragPanelInset)), windowWidth, windowHeight)

	Sheet(c, SheetProps{Key: modalDragContextPanel, CardKey: "context-modal-card", Label: "context-modal-card", Open: &open, Width: width, Height: height}, func() {
		// 与命令面板一致，弹窗打开时也在此处理 ⇧⌘K 关闭。
		if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyK) {
			s.toggleContextPanel()
		}
		renderContextHeader(c, s)
		// 头部与 chip 行之间的竖向间距。
		ui.Column(c.Key("context-chips-wrap")).FillWidth().Padding(Space3, 0, 0, 0).Children(func() {
			renderContextChips(c, s)
		})
		contextPanelMu.Lock()
		conflict := cp.conflict
		errMsg := cp.err
		contextPanelMu.Unlock()
		renderContextConflictBanner(c, s, conflict)
		if errMsg != "" && !conflict {
			renderContextErrorBanner(c, s, errMsg)
		}
		renderContextDocView(c, s)
		modalResizeGrips(c, modalDragContextPanel, width, height)
	}).Clip()
	// 点击遮罩或按 Esc 关闭时同样要经过未保存修改确认。
	if !open {
		modalDragReset(modalDragContextPanel)
		cp.requestClose(s)
	}

	// 未保存修改的离开确认挂在根层、且在面板弹窗之后渲染：嵌在面板卡片里会被裁剪，
	// 遮罩也只盖住卡片本身；后渲染的弹窗同时能优先拿到 Esc。
	contextPanelMu.Lock()
	leaveOpen := cp.leaveOpen
	contextPanelMu.Unlock()
	if leaveOpen {
		renderContextLeaveConfirm(c, s)
	}
}

// requestClose 请求关闭弹窗：有未保存修改时先确认「保存 / 放弃 / 取消」。
func (cp *contextPanelState) requestClose(s *chatState) {
	contextPanelMu.Lock()
	if cp.closed || !cp.open {
		contextPanelMu.Unlock()
		s.invalidate()
		return
	}
	if cp.dirty {
		cp.leaveOpen, cp.leaveDoc, cp.leaveTo, cp.leaveClos = true, cp.selected, "", true
		contextPanelMu.Unlock()
		s.invalidate()
		return
	}
	contextPanelMu.Unlock()
	s.closeContextPanel()
}

// completeContextPanel 对应「完成」按钮。
func (s *chatState) completeContextPanel() {
	cp := getContextPanelState(s)
	if cp == nil {
		return
	}
	cp.requestClose(s)
}

// contextBaseButton 是弹窗内不带外观的按钮基座。
func contextBaseButton(c *ui.Context, key any, label string) ui.Element {
	b := ui.ButtonBase(c.Key(key)).Radius(RadiusSM).Gap(Space2).AlignItems(ui.Center)
	if label != "" {
		b.Label(label)
	}
	return b
}

// contextPanelSnapshot 复制渲染需要的状态，避免渲染过程中长时间持锁。
// layerSwitch 表示是否渲染「项目 / 全局」层切换：全局层只有本地记忆后端使用。
type contextPanelSnapshot struct {
	path        string
	global      bool
	layerSwitch bool
	section     string
	selected    string
	manifest    projctx.Manifest
	editing     bool
	draft       string
	dirty       bool
	conflict    bool
	conflictMsg string
	err         string
	showSaved   bool
	saving      bool
	leaveOpen   bool
	leaveClos   bool
	creating    bool
	newName     string
	loading     bool
}

func snapshotContextPanel(s *chatState) contextPanelSnapshot {
	cp := getContextPanelState(s)
	snap := contextPanelSnapshot{}
	if cp == nil {
		return snap
	}
	contextPanelMu.Lock()
	defer contextPanelMu.Unlock()
	snap = contextPanelSnapshot{
		path:        cp.activePath(),
		global:      cp.global,
		section:     cp.section,
		selected:    cp.selected,
		manifest:    cp.manifest,
		editing:     cp.editing,
		draft:       cp.draft,
		dirty:       cp.dirty || cp.draft != cp.savedContent,
		conflict:    cp.conflict,
		conflictMsg: cp.conflictMsg,
		err:         cp.err,
		showSaved:   cp.showSaved,
		saving:      cp.saving,
		leaveOpen:   cp.leaveOpen,
		leaveClos:   cp.leaveClos,
		creating:    cp.creating,
		newName:     cp.newName,
		loading:     cp.loading,
		layerSwitch: s.client != nil && s.client.MemoryBackend() == config.MemoryBackendLocal,
	}
	return snap
}

// renderContextHeader 渲染头部（56 高）：图标块、标题与副标题、Finder 与「完成」。
func renderContextHeader(c *ui.Context, s *chatState) {
	snap := snapshotContextPanel(s)
	// 标题行同时是弹窗的拖拽把手。
	modalDragHandle(c, modalDragContextPanel, ui.Row(c.Key("context-modal-header")).
		Height(panelHeaderHeight).
		FillWidth().
		Padding(0, contextModalPadding).
		Gap(Space4).
		AlignItems(ui.Center).
		BorderWidth(0, 0, Hairline, 0).
		BorderColor(ThemeBorder(c)).
		Children(func() {
			ui.Row(c.Key("context-header-icon")).
				Size(ControlLG, ControlLG).
				Radius(RadiusMD).
				Center().
				Background(ThemeAccent(c).Alpha(0.18)).
				Children(func() {
					ui.Icon(c, IconBookOpen()).Size(IconMD, IconMD).TextColor(ThemeAccent(c))
				})

			ui.Column(c.Key("context-header-titles")).
				Gap(Space0).
				Justify(ui.Center).
				Children(func() {
					title := "项目文档"
					if snap.global {
						title = "全局记忆"
					}
					ui.Text(c, title).
						FontSize(TypeProse).
						FontWeight(600).
						TextColor(ThemePrimaryText(c))
					subtitle := ".mcode/context/ • CAS 并发保护"
					if snap.global {
						subtitle = "全局记忆与规则，位于 ~/.mcode/context"
					}
					ui.Text(c, subtitle).
						FontSize(TypeCaption).
						Font("monospace").
						TextColor(ThemeSecondaryText(c))
				})

			ui.Spacer(c)
			// 全局层只有本地记忆后端会读，nmem 后端不提供层切换，面板固定在项目层。
			if snap.layerSwitch {
				index := 0
				if snap.global {
					index = 1
				}
				seg := ui.Segmented(c.Key("context-layer-switch"), &index, "项目", "全局")
				seg.Label("文档层级").Width(100).Shrink(0)
				if seg.Changed() {
					s.switchContextLayer(index == 1)
				}
			}

			Button(c.Key("context-open-finder-btn"), ButtonOpts{
				Icon:     IconFolderOpen(),
				Text:     "在 Finder 中打开",
				Disabled: snap.path == "",
				OnClick: func() {
					if snap.path != "" {
						revealContextItem(filepath.Join(snap.path, filepath.FromSlash(projctx.DirName)))
					}
				},
				Variant: ButtonSecondary,
			})

			Button(c.Key("context-done-btn"), ButtonOpts{
				Text:    "完成",
				OnClick: func() { s.completeContextPanel() },
				Variant: ButtonPrimary,
			})
		}))
}

// renderContextChips 渲染 chip 行：四篇标准文档、自定义文档与新建入口。
// 这一行只放 chip，横向滚动、不换行，「编辑 / 预览」分段控件挪到文档详情行。
// 背景与头部一致（不加更亮的底色），下方留一条细分隔线。
func renderContextChips(c *ui.Context, s *chatState) {
	snap := snapshotContextPanel(s)
	standard := snap.manifest.Standard()
	customs := snap.manifest.Custom()

	ui.Column(c.Key("context-modal-chips")).
		FillWidth().
		Padding(Space3, contextModalPadding).
		BorderWidth(0, 0, Hairline, 0).
		BorderColor(ThemeBorder(c)).
		Children(func() {
			// 横向滚动，chip 放不下时整行左右滑动，而不是被压扁换行。
			ui.ScrollHorizontal(c.Key("context-chips-scroll")).FillWidth().Children(func() {
				ui.Row(c.Key("context-chips-row")).
					Height(contextChipHeight).
					Gap(contextChipGap).
					AlignItems(ui.Center).
					Children(func() {
						for _, doc := range standard {
							renderContextDocChip(c, s, doc, snap.section == contextDocRef(doc))
						}
						contextChipDivider(c, "custom")
						// 标签不收缩、不换行，否则会被挤成竖排。
						ui.Text(c, "自定义").
							FontSize(TypeCaption).
							FontWeight(500).
							Shrink(0).
							MaxLines(1).
							TextColor(ThemeSecondaryText(c))
						for _, doc := range customs {
							renderContextDocChip(c, s, doc, snap.section == contextDocRef(doc))
						}
						contextBaseButton(c, "context-new-doc-btn", "新建自定义文档").
							Size(contextChipHeight, contextChipHeight).
							Shrink(0).
							Center().
							Background(ThemeHover(c)).
							Tooltip("新建自定义文档").
							OnClick(func() { s.requestCreateContextDoc() }).
							Children(func() {
								ui.Icon(c, IconPlus()).Size(IconSM, IconSM).TextColor(ThemeAccent(c))
							})
					})
			})
		})
}

func contextChipDivider(c *ui.Context, key string) {
	ui.Row(c.Key("context-chip-divider-"+key)).
		Size(Hairline, Space5).
		Shrink(0).
		Background(ThemeSecondaryText(c).Alpha(0.25))
}

func renderContextDocChip(c *ui.Context, s *chatState, doc projctx.DocInfo, selected bool) {
	tooltip := doc.Subtitle
	if !doc.Standard {
		tooltip = doc.RelativePath()
	}
	Capsule(c, CapsuleProps{
		Key:         "context-chip-doc-" + doc.File,
		Text:        doc.Title,
		Icon:        mapContextIcon(doc.Icon),
		Tone:        ToneNeutral,
		Size:        contextChipHeight,
		Interactive: true,
		Selected:    selected,
		Tooltip:     tooltip,
		OnClick:     func() { s.switchContextSection(contextDocRef(doc)) },
	})
}

// renderContextModeSwitch 渲染 120 宽的「编辑 / 预览」分段控件。
func renderContextModeSwitch(c *ui.Context, s *chatState, editing bool) {
	index := 1
	if editing {
		index = 0
	}
	seg := ui.Segmented(c.Key("context-mode-switch"), &index, "编辑", "预览")
	seg.Label("编辑或预览").Width(120).Shrink(0)
	if seg.Changed() {
		cp := getContextPanelState(s)
		if cp != nil {
			contextPanelMu.Lock()
			cp.editing = index == 0
			contextPanelMu.Unlock()
		}
		s.invalidate()
	}
}

// renderContextConflictBanner 渲染并发冲突横幅（danger 12% 背景）。
func renderContextConflictBanner(c *ui.Context, s *chatState, conflict bool) {
	if !conflict {
		return
	}
	msg := snapshotContextPanel(s).conflictMsg
	if msg == "" {
		msg = "文件已被外部修改，存在并发冲突"
	}
	ui.Row(c.Key("context-conflict-banner")).
		FillWidth().
		Padding(Space3, contextModalPadding).
		Gap(Space4).
		AlignItems(ui.Center).
		Background(ThemeDanger(c).Alpha(0.12)).
		Children(func() {
			ui.Icon(c, IconOctagonAlert()).Size(IconSM, IconSM).TextColor(ThemeDanger(c))
			ui.Text(c, msg).FontSize(TypeFootnote).TextColor(ThemeDanger(c))
			ui.Spacer(c)
			Button(c.Key("context-reload-btn"), ButtonOpts{
				Text:    "从磁盘重新加载",
				OnClick: func() { s.reloadContextDoc() },
				Variant: ButtonSecondary,
			})
			Button(c.Key("context-overwrite-btn"), ButtonOpts{
				Text:    "覆盖磁盘",
				OnClick: func() { s.overwriteContextDoc() },
				Variant: ButtonDanger,
			})
		})
}

// renderContextErrorBanner 渲染保存失败横幅，带「忽略」。
func renderContextErrorBanner(c *ui.Context, s *chatState, errMsg string) {
	Banner(c, BannerProps{
		Key:      "context-error-banner",
		Tone:     ToneDanger,
		Text:     errMsg,
		Action:   "忽略",
		OnAction: func() { s.dismissContextError() },
	})
}

// renderContextDocView 渲染文档详情行与内容区（编辑 TextArea / 预览 markdown）。
func renderContextDocView(c *ui.Context, s *chatState) {
	snap := snapshotContextPanel(s)
	if snap.selected == "" {
		ui.Column(c.Key("context-doc-empty")).Grow(1).FillWidth().Center().Children(func() {
			ui.Text(c, "选择一篇文档").TextColor(ThemeSecondaryText(c))
		})
		if snap.creating {
			renderContextCreateDialog(c, s, snap.newName)
		}
		return
	}
	doc, ok := snap.manifest.Find(snap.selected)
	if !ok {
		doc = projctx.DocInfo{
			Name:     projctx.TitleOf(snap.selected),
			File:     snap.selected,
			Title:    projctx.TitleOf(snap.selected),
			Subtitle: "自定义文档 · docs/",
			Icon:     projctx.IconFile,
		}
	}
	filePath := filepath.Join(snap.path, filepath.FromSlash(doc.RelativePath()))

	ui.Column(c.Key("context-doc-view")).Grow(1).FillWidth().Children(func() {
		// chip 行与详情行之间留出竖向间距。
		ui.Column(c.Key("context-doc-details-wrap")).FillWidth().Padding(Space3, 0, 0, 0).Children(func() {
			renderContextDocDetails(c, s, snap, doc, filePath)
		})

		draft := snap.draft
		ui.Column(c.Key("context-doc-body")).Grow(1).FillWidth().Padding(contextModalPadding).Children(func() {
			if snap.editing {
				ui.TextArea(c.Key("context-editor-textarea-"+doc.File), &draft).
					Label("项目文档内容").
					Grow(1).
					FillWidth().
					FontSize(TypeFootnote).
					Font("monospace").
					LineHeight(1.4).
					TextColor(ThemePrimaryText(c)).
					Background(ui.Transparent).
					BorderWidth(0).
					Padding(0).
					OnChange(func() { s.editContextDraft(draft) }).
					AutoFocus()
			} else {
				ui.Scroll(c.Key("context-preview-scroll")).Grow(1).FillWidth().Children(func() {
					markdown.View(c.Key("context-markdown-"+doc.File), draft, &s.mdStaticOpts)
				})
			}
		})

		if snap.creating {
			renderContextCreateDialog(c, s, snap.newName)
		}
	})
}

// renderContextDocDetails 渲染 40 高的文档详情行：文件名 • 副标题、已保存徽标、外部编辑、
// 「编辑 / 预览」分段控件、Spinner、保存。
func renderContextDocDetails(c *ui.Context, s *chatState, snap contextPanelSnapshot, doc projctx.DocInfo, filePath string) {
	saveBg := ThemeHover(c)
	saveText := ThemeSecondaryText(c)
	if snap.dirty || snap.conflict {
		saveBg = ThemeAccent(c)
		saveText = ThemeOnAccent(c)
	}

	ui.Row(c.Key("context-doc-details")).
		Height(contextDocRowHeight).
		FillWidth().
		Padding(0, contextModalPadding).
		Gap(Space3).
		AlignItems(ui.Center).
		BorderWidth(0, 0, Hairline, 0).
		BorderColor(ThemeBorder(c)).
		Children(func() {
			ui.Row(c.Key("context-doc-details-title")).
				AlignItems(ui.Center).
				Gap(Space2).
				Grow(1).
				Shrink(1).
				MinWidth(0).
				Children(func() {
					ui.Text(c, doc.File).
						FontSize(TypeBody).
						FontWeight(700).
						Font("monospace").
						SingleLine().
						Shrink(0).
						TextColor(ThemePrimaryText(c))
					ui.Text(c, "•").FontSize(TypeFootnote).Shrink(0).TextColor(ThemeSecondaryText(c))
					ui.Text(c, doc.Subtitle).
						FontSize(TypeFootnote).
						SingleLine().
						Shrink(1).
						MinWidth(0).
						TextColor(ThemeSecondaryText(c))
				})

			if snap.showSaved {
				Capsule(c, CapsuleProps{
					Key:  "context-saved-badge",
					Text: "已保存",
					Icon: IconCheck(),
					Tone: ToneSuccess,
					Size: ControlXS,
				})
			}

			IconButton(c.Key("context-open-editor-btn"), IconButtonProps{
				Icon:  IconSquarePen(),
				Label: "在编辑器中打开",
				OnClick: func() {
					if filePath != "" {
						openContextDocInEditor(filePath)
					}
				},
				Variant: ButtonGhost,
			})

			if snap.saving {
				ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))
			}

			renderContextModeSwitch(c, s, snap.editing)

			b := contextBaseButton(c, "context-save-btn", "保存").
				Height(ControlSM).
				Padding(0, Space4).
				Shrink(0).
				Background(saveBg)
			if snap.saving || !snap.dirty || snap.conflict {
				b = b.Disabled(true)
			} else {
				b = b.OnClick(func() { s.saveContextDoc() })
			}
			b.Children(func() {
				ui.Icon(c, IconCheck()).Size(IconSM, IconSM).TextColor(saveText)
				ui.Text(c, "保存").FontSize(TypeFootnote).TextColor(saveText)
			})
		})
}

// renderContextLeaveConfirm 在有未保存修改时，切换文档或关闭弹窗前弹出「保存 / 放弃 / 取消」。
// 用统一的三态确认弹窗（安全态）：「保存并离开」是主按钮，Enter 即保存；Esc 取消。
func renderContextLeaveConfirm(c *ui.Context, s *chatState) {
	RenderConfirmDialog(c, ConfirmDialog{
		Key:          modalDragContextLeave,
		Open:         true,
		Tone:         ConfirmSafe,
		Title:        "保存这次编辑？",
		Body:         "有未保存的修改，要先保存吗？",
		ConfirmLabel: "保存并离开",
		ExtraLabel:   "放弃",
		CancelLabel:  "取消",
		OnConfirm:    func() { s.resolveContextLeave(true) },
		OnExtra:      func() { s.resolveContextLeave(false) },
		OnCancel:     func() { s.cancelContextLeave() },
	})
}

const contextCreateDialogHeight float32 = 240

// renderContextCreateDialog 渲染「新建自定义文档」的输入对话框。
func renderContextCreateDialog(c *ui.Context, s *chatState, initial string) {
	name := initial
	open := true
	winW, winH := c.Size()
	width, height := modalResizeSize(modalDragContextCreate, 360, contextCreateDialogHeight, 360, 160, winW, winH)
	Sheet(c, SheetProps{Key: modalDragContextCreate, CardKey: "context-create-card", Label: "context-create-card", Open: &open, Width: width, Height: height}, func() {
		ui.Column(c).
			FillWidth().Grow(1).
			Padding(Space5).
			Gap(Space4).
			Children(func() {
				// 标题行同时是弹窗的拖拽把手。
				modalDragHandle(c, modalDragContextCreate, ui.Row(c.Key("context-create-title-row")).FillWidth().Children(func() {
					ui.Text(c, "新建自定义文档").FontSize(TypeTitle).FontWeight(600).TextColor(ThemePrimaryText(c))
				}))
				ui.Text(c, "保存在 .mcode/context/docs/ 下，并作为独立标签显示。").
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c))
				TextField(c, TextFieldProps{Key: "context-new-name", Value: &name, Label: "文档名称", Placeholder: "api-spec.md", FillWidth: true, AutoFocus: true, OnChange: func(value string) {
					cp := getContextPanelState(s)
					if cp == nil {
						return
					}
					contextPanelMu.Lock()
					cp.newName = value
					contextPanelMu.Unlock()
				}})
				FormActions(c, "context-create-actions", func() {
					Button(c.Key("context-create-cancel-btn"), ButtonOpts{
						Text: "取消",
						OnClick: func() {
							modalDragReset(modalDragContextCreate)
							s.cancelCreateContextDoc()
						},
						Variant: ButtonSecondary,
					})
					Button(c.Key("context-create-confirm-btn"), ButtonOpts{
						Text: "创建",
						OnClick: func() {
							modalDragReset(modalDragContextCreate)
							s.createContextDoc()
						},
						Variant: ButtonPrimary,
					})
				})
				modalResizeGripsMin(c, modalDragContextCreate, width, height, 360, 160)
			})
	})
	if !open {
		modalDragReset(modalDragContextCreate)
		s.cancelCreateContextDoc()
	}
}
