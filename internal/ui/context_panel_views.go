package mcodeui

import (
	"fmt"
	"path/filepath"

	"mcode/internal/config"
	"mcode/internal/projctx"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"
)

const (
	contextModalPadding float32 = Space5
	// contextNavWidth 取自设计稿（216px，参照 settingsNavWidth = 220）。
	contextNavWidth float32 = settingsNavWidth
	// contextDocRowHeight 是文档行在详情区顶部的高度。
	contextDocRowHeight float32 = 40
)

// renderContextModal 在根层挂载项目文档弹窗（760×600，居中卡片）。
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
		contextPanelMu.Lock()
		conflict := cp.conflict
		errMsg := cp.err
		creating := cp.creating
		contextPanelMu.Unlock()
		renderContextConflictBanner(c, s, conflict)
		if errMsg != "" && !conflict && !creating {
			renderContextErrorBanner(c, s, errMsg)
		}
		ui.Row(c.Key("context-body")).
			FillWidth().
			Grow(1).
			MinHeight(0).
			Background(ThemeSurfaceMain(c)).
			Children(func() {
				renderContextNav(c, s)
				renderContextDocView(c, s)
			})
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
	{
		scale := motionPressScale(c, b, "button-press-scale", b.Pressed())
		b.Scale(scale, scale)
	}
	if label != "" {
		b.Label(label)
	}
	return b
}

// contextPanelSnapshot 复制渲染需要的状态，避免渲染过程中长时间持锁。
// layerSwitch 表示是否渲染「项目 / 全局」层切换：全局层只有本地记忆后端使用。
type contextPanelSnapshot struct {
	path           string
	global         bool
	layerSwitch    bool
	section        string
	selected       string
	manifest       projctx.Manifest
	editing        bool
	draft          string
	dirty          bool
	conflict       bool
	conflictMsg    string
	err            string
	showSaved      bool
	saving         bool
	leaveOpen      bool
	leaveClos      bool
	creating       bool
	newName        string
	loading        bool
	headings       []contextDocHeading
	jumpHeadingIdx int
	jumpSeq        uint64
	draftRefs      map[string]bool
}

func snapshotContextPanel(s *chatState) contextPanelSnapshot {
	cp := getContextPanelState(s)
	snap := contextPanelSnapshot{jumpHeadingIdx: -1}
	if cp == nil {
		return snap
	}
	contextPanelMu.Lock()
	defer contextPanelMu.Unlock()
	cp.ensureHeadingsLocked()
	activePath := cp.activePath()
	var draftRefs map[string]bool
	if len(cp.drafts) > 0 && activePath != "" {
		for _, doc := range cp.manifest.Docs {
			ref := contextDocRef(doc)
			if _, ok := cp.drafts[filepath.Join(activePath, ref)]; ok {
				if draftRefs == nil {
					draftRefs = make(map[string]bool)
				}
				draftRefs[ref] = true
			}
		}
	}
	snap = contextPanelSnapshot{
		path:           activePath,
		global:         cp.global,
		section:        cp.section,
		selected:       cp.selected,
		manifest:       cp.manifest,
		editing:        cp.editing,
		draft:          cp.draft,
		dirty:          cp.dirty || cp.draft != cp.savedContent,
		conflict:       cp.conflict,
		conflictMsg:    cp.conflictMsg,
		err:            cp.err,
		showSaved:      cp.showSaved,
		saving:         cp.saving,
		leaveOpen:      cp.leaveOpen,
		leaveClos:      cp.leaveClos,
		creating:       cp.creating,
		newName:        cp.newName,
		loading:        cp.loading,
		headings:       cp.headings,
		jumpHeadingIdx: cp.jumpHeadingIdx,
		jumpSeq:        cp.jumpSeq,
		draftRefs:      draftRefs,
		layerSwitch:    s.client != nil && s.client.MemoryBackend() == config.MemoryBackendLocal,
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
				Justify(ui.Center).
				Children(func() {
					title := "项目文档"
					if snap.global {
						title = "全局记忆"
					}
					ui.Text(c, title).
						FontSize(uiFont(TypeProse)).
						FontWeight(600).
						TextColor(ThemePrimaryText(c))
				})

			ui.Spacer(c)
			// 全局层只有本地记忆后端会读，nmem 后端不提供层切换，面板固定在项目层。
			if snap.layerSwitch {
				index := 0
				if snap.global {
					index = 1
				}
				Segmented(c, "context-layer-switch", &index, []Segment{
					{Label: "项目"},
					{Label: "全局"},
				}, func(idx int) {
					s.switchContextLayer(idx == 1)
				}).Label("文档层级").Shrink(0)
			}

			Button(c.Key("context-open-finder-btn"), ButtonOpts{
				Icon:     IconFolderOpen(),
				Text:     "在" + openInFileManagerName() + "中打开",
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

// renderContextNav 渲染左侧竖排文档目录：四篇标准文档一组，自定义文档一组，新建入口固定在列表底部。
func renderContextNav(c *ui.Context, s *chatState) {
	snap := snapshotContextPanel(s)
	standard := snap.manifest.Standard()
	customs := snap.manifest.Custom()

	ui.Column(c.Key("context-nav")).
		Label("context-nav").
		Width(contextNavWidth).
		Shrink(0).
		FillHeight().
		Background(ThemeSidebarBg(c)).
		BorderWidth(0, Hairline, 0, 0).
		BorderColor(ThemeHairline(c)).
		Children(func() {
			ui.Scroll(c.Key("context-nav-scroll")).
				Label("context-nav-scroll").
				Grow(1).
				MinHeight(0).
				FillWidth().
				Padding(Space3, Space2).
				Children(func() {
					ui.Column(c.Key("context-nav-groups")).
						FillWidth().
						Gap(Space4).
						Children(func() {
							renderContextNavGroup(c, s, snap, "standard", "标准文档", standard, "当前未关联项目")
							emptyCustom := "暂无自定义文档"
							if snap.path == "" {
								emptyCustom = "—"
							}
							renderContextNavGroup(c, s, snap, "custom", "自定义文档", customs, emptyCustom)
						})
				})

			ui.Column(c.Key("context-nav-footer")).
				FillWidth().
				Shrink(0).
				Padding(Space2).
				BorderWidth(Hairline, 0, 0, 0).
				BorderColor(ThemeHairline(c)).
				Children(func() {
					newBtn := contextBaseButton(c, "context-nav-new-doc-btn", "新建自定义文档").
						FillWidth().
						Height(ControlLG).
						Padding(0, Space2).
						Tooltip("新建自定义文档")
					hoverTarget := float32(0)
					if snap.path != "" {
						newBtn = newBtn.Cursor(ui.CursorPointer)
						if newBtn.Hovered() {
							hoverTarget = 1
						}
					}
					var hover float32
					if reduceMotion(c) {
						hover = newBtn.Jump("context-nav-new-hover", hoverTarget)
					} else {
						hover = newBtn.AnimateTo("context-nav-new-hover", hoverTarget, ui.Animation{Duration: DurFast, Ease: EaseSnap})
					}
					if hover > 0 {
						bg := ThemeOverlayHover(c)
						bg.A = uint8(float32(bg.A)*hover + 0.5)
						newBtn.Background(bg)
					}
					if snap.path == "" {
						newBtn = newBtn.Disabled(true)
					} else {
						newBtn = newBtn.OnClick(func() { s.requestCreateContextDoc() })
					}
					newBtn.Children(func() {
						ui.Icon(c, IconPlus()).Size(IconSM, IconSM).Shrink(0).TextColor(ThemeAccent(c))
						ui.Text(c, "新建自定义文档").
							FontSize(uiFont(TypeBody)).
							FontWeight(weightFor(c, 500)).
							SingleLine().
							TextColor(ThemeAccent(c))
					})
				})
		})
}

func renderContextNavGroup(c *ui.Context, s *chatState, snap contextPanelSnapshot, key, title string, docs []projctx.DocInfo, emptyText string) {
	ui.Column(c.Key("context-nav-group-" + key)).
		FillWidth().
		Gap(Space0).
		Children(func() {
			ui.Row(c.Key("context-nav-head-"+key)).
				FillWidth().
				Padding(0, Space2, Space1, Space2).
				AlignItems(ui.Center).
				Children(func() {
					ui.Text(c, title).
						FontSize(uiFont(TypeCaption)).
						FontWeight(weightFor(c, 600)).
						TextColor(ThemeTertiaryText(c))
				})
			if len(docs) == 0 {
				ui.Text(c, emptyText).
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemeTertiaryText(c)).
					Padding(Space2)
				return
			}
			for _, doc := range docs {
				renderContextNavRow(c, s, snap, doc)
			}
		})
}

func renderContextNavRow(c *ui.Context, s *chatState, snap contextPanelSnapshot, doc projctx.DocInfo) {
	ref := contextDocRef(doc)
	selected := snap.section == ref
	hasDraft := (selected && snap.dirty) || snap.draftRefs[ref]
	hasConflict := selected && snap.conflict

	tooltip := doc.Subtitle
	if !doc.Standard {
		tooltip = doc.RelativePath()
	}

	row := ui.Row(c.Key("context-nav-doc-"+doc.File)).
		FillWidth().
		MinHeight(ControlLG).
		Padding(Space1, Space2).
		Radius(RadiusSM).
		Gap(Space2).
		AlignItems(ui.Center).
		Focusable().
		KeepFocus().
		Cursor(ui.CursorPointer).
		Tooltip(tooltip).
		Label(doc.Title)

	hoverTarget, selTarget := float32(0), float32(0)
	if selected {
		selTarget = 1
	} else if row.Hovered() {
		hoverTarget = 1
	}
	var hover, sel float32
	if reduceMotion(c) {
		hover = row.Jump("context-nav-hover", hoverTarget)
		sel = row.Jump("context-nav-sel", selTarget)
	} else {
		hover = row.AnimateTo("context-nav-hover", hoverTarget, ui.Animation{Duration: DurFast, Ease: EaseSnap})
		sel = row.AnimateTo("context-nav-sel", selTarget, ui.Animation{Duration: DurFast, Ease: EaseSnap})
	}
	if selected {
		hover = 0
	}
	if hover > 0 || sel > 0 {
		bg := ThemeOverlayHover(c)
		bg.A = uint8(float32(bg.A)*hover + 0.5)
		bg = bg.Mix(ThemeOverlaySelected(c), sel)
		row.Background(bg)
	}
	focusRing(c, row)

	if row.Clicked() {
		s.switchContextSection(ref)
	}

	row.Children(func() {
		iconColor := ThemeSecondaryText(c)
		if selected {
			iconColor = ThemeAccent(c)
		}
		ui.Icon(c, mapContextIcon(doc.Icon)).Size(IconSM, IconSM).Shrink(0).TextColor(iconColor)

		ui.Column(c.Key("context-nav-main-" + doc.File)).
			Grow(1).
			MinWidth(0).
			Children(func() {
				titleColor := ThemePrimaryText(c)
				if !doc.Exists && !selected {
					titleColor = ThemeSecondaryText(c)
				}
				title := ui.Text(c, doc.Title).
					FontSize(uiFont(TypeBody)).
					SingleLine().
					TextColor(titleColor)
				if selected {
					title.FontWeight(weightFor(c, 600))
				}
				ui.Text(c, doc.File).
					FontSize(uiFont(TypeMicro)).
					SingleLine().
					TextColor(ThemeTertiaryText(c))
			})

		switch {
		case hasConflict:
			ui.Box(c.Key("context-nav-conflict-"+doc.File)).
				Size(Space2, Space2).
				Radius(RadiusPill).
				Background(ThemeDanger(c)).
				Shrink(0)
		case hasDraft:
			ui.Box(c.Key("context-nav-dirty-"+doc.File)).
				Size(Space2, Space2).
				Radius(RadiusPill).
				Background(ThemeWarning(c)).
				Shrink(0)
		case !doc.Exists:
			ui.Row(c.Key("context-nav-tpl-"+doc.File)).
				Height(Space5).
				Padding(0, Space1).
				Radius(RadiusPill).
				Background(ThemeOverlayHover(c)).
				Center().
				Shrink(0).
				Children(func() {
					ui.Text(c, "模板").
						FontSize(uiFont(TypeMicro)).
						TextColor(ThemeTertiaryText(c))
				})
		}
	})
}

// renderContextModeSwitch 渲染「编辑 / 预览」分段控件。
func renderContextModeSwitch(c *ui.Context, s *chatState, editing bool) {
	index := 1
	if editing {
		index = 0
	}
	Segmented(c, "context-mode-switch", &index, []Segment{
		{Label: "编辑"},
		{Label: "预览"},
	}, func(idx int) {
		cp := getContextPanelState(s)
		if cp != nil {
			contextPanelMu.Lock()
			cp.editing = idx == 0
			contextPanelMu.Unlock()
		}
		s.invalidate()
	}).Label("编辑或预览").Shrink(0)
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
			ui.Icon(c, IconOctagonAlert()).Size(IconSM, IconSM).Shrink(0).TextColor(ThemeDanger(c))
			ui.Text(c, msg).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c)).MinWidth(0).Shrink(1)
			ui.Spacer(c)
			Button(c.Key("context-reload-btn"), ButtonOpts{
				Text:    "从磁盘重新加载",
				OnClick: func() { s.reloadContextDoc() },
				Variant: ButtonSecondary,
			}).Shrink(0)
			Button(c.Key("context-overwrite-btn"), ButtonOpts{
				Text:    "覆盖磁盘",
				OnClick: func() { s.overwriteContextDoc() },
				Variant: ButtonDanger,
			}).Shrink(0)
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

// renderContextDocView 渲染文档详情行、章节跳转条与内容区（编辑 TextArea / 预览 markdown）。
func renderContextDocView(c *ui.Context, s *chatState) {
	snap := snapshotContextPanel(s)
	if snap.selected == "" {
		ui.Column(c.Key("context-doc-empty")).Grow(1).MinWidth(0).FillHeight().Center().Children(func() {
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

	ui.Column(c.Key("context-doc-view")).Grow(1).MinWidth(0).FillHeight().Children(func() {
		renderContextDocDetails(c, s, snap, doc, filePath)
		if len(snap.headings) > 0 {
			renderContextHeadingBar(c, s, snap)
		}

		draft := snap.draft
		ui.Column(c.Key("context-doc-body")).Grow(1).MinHeight(0).FillWidth().Padding(contextModalPadding).Children(func() {
			if snap.editing {
				ta := ui.TextArea(c.Key("context-editor-textarea-"+doc.File), &draft).
					Label("项目文档内容").
					Grow(1).
					FillWidth().
					FontSize(uiFont(TypeFootnote)).
					Font("monospace").
					LineHeight(1.4).
					TextColor(ThemePrimaryText(c)).
					Background(ui.Transparent).
					BorderWidth(0).
					Padding(0).
					OnChange(func() { s.editContextDraft(draft) }).
					AutoFocus()
				if offset, ok := s.consumeContextEditorJump(draft); ok {
					ta.Focus().SetTextSelection(offset, offset)
				}
			} else {
				mdOpts, scrollState := contextPreviewMarkdownOpts(c, s, filePath, snap)
				scroll := ui.Scroll(c.Key("context-preview-scroll")).Grow(1).MinHeight(0).FillWidth()
				if scrollState != nil {
					scroll = scroll.TrackScroll(scrollState)
				}
				scroll.Children(func() {
					markdown.View(c.Key("context-markdown-"+doc.File), draft, mdOpts)
				})
			}
		})

		if snap.creating {
			renderContextCreateDialog(c, s, snap.newName)
		}
	})
}

func renderContextHeadingBar(c *ui.Context, s *chatState, snap contextPanelSnapshot) {
	ui.Row(c.Key("context-heading-bar")).
		Label("context-heading-bar").
		FillWidth().
		Shrink(0).
		Background(ThemeSidebarBg(c)).
		BorderWidth(0, 0, Hairline, 0).
		BorderColor(ThemeHairline(c)).
		Children(func() {
			ui.ScrollHorizontal(c.Key("context-heading-scroll")).
				FillWidth().
				Padding(Space2, contextModalPadding).
				Children(func() {
					ui.Row(c.Key("context-heading-row")).
						Gap(Space1).
						AlignItems(ui.Center).
						Children(func() {
							for i, h := range snap.headings {
								idx := i
								active := snap.jumpHeadingIdx == i
								btn := contextBaseButton(c, fmt.Sprintf("context-heading-btn-%d", i), h.title).
									Height(ControlXS).
									Padding(0, Space2).
									Radius(RadiusPill).
									Shrink(0).
									Cursor(ui.CursorPointer).
									OnClick(func() { s.jumpToContextHeading(idx) })
								hoverTarget, activeTarget := float32(0), float32(0)
								if active {
									activeTarget = 1
								} else if btn.Hovered() {
									hoverTarget = 1
								}
								var hover, act float32
								if reduceMotion(c) {
									hover = btn.Jump("context-heading-hover", hoverTarget)
									act = btn.Jump("context-heading-active", activeTarget)
								} else {
									hover = btn.AnimateTo("context-heading-hover", hoverTarget, ui.Animation{Duration: DurFast, Ease: EaseSnap})
									act = btn.AnimateTo("context-heading-active", activeTarget, ui.Animation{Duration: DurFast, Ease: EaseSnap})
								}
								if active {
									hover = 0
								}
								if hover > 0 || act > 0 {
									bg := ThemeOverlayHover(c)
									bg.A = uint8(float32(bg.A)*hover + 0.5)
									bg = bg.Mix(ThemeAccentSubtle(c), act)
									btn.Background(bg)
								}
								textColor := ThemeSecondaryText(c)
								if active {
									textColor = ThemeAccent(c)
								} else if btn.Hovered() {
									textColor = ThemePrimaryText(c)
								}
								btn.Children(func() {
									t := ui.Text(c, h.title).
										FontSize(uiFont(TypeCaption)).
										SingleLine().
										TextColor(textColor)
									if active {
										t.FontWeight(weightFor(c, 600))
									}
								})
							}
						})
				})
		})
}

func contextPreviewMarkdownOpts(c *ui.Context, s *chatState, filePath string, snap contextPanelSnapshot) (*markdown.Options, *ui.ScrollState) {
	cp := getContextPanelState(s)
	if cp == nil {
		return &s.mdStaticOpts, nil
	}
	baseDir := ""
	if snap.path != "" && filepath.IsAbs(filePath) {
		baseDir = filepath.Dir(filePath)
	}
	contextPanelMu.Lock()
	if cp.previewMDOpts == nil {
		cp.previewMDOpts = &markdown.Options{}
	}
	opts := cp.previewMDOpts
	scrollState := &cp.previewScroll
	contextPanelMu.Unlock()

	applyDocMarkdownTheme(c, s, opts, TypeBody)
	if s != nil {
		installMarkdownHooksWithBaseDir(opts, s, baseDir)
	}
	if snap.jumpSeq > 0 && snap.jumpHeadingIdx >= 0 {
		targetIdx := snap.jumpHeadingIdx
		opts.Anchor = fmt.Sprintf("h2-%d", targetIdx)
		opts.AnchorVersion = snap.jumpSeq
		opts.AnchorBlock = func(d *parser.Document, _ string) int {
			seen := 0
			for b := 1; b < d.Len(); b++ {
				blk := d.Block(b)
				if blk.Kind == parser.Heading && blk.Level == 2 {
					if seen == targetIdx {
						return b
					}
					seen++
				}
			}
			return -1
		}
	} else {
		opts.Anchor = ""
		opts.AnchorVersion = 0
		opts.AnchorBlock = nil
	}
	return opts, scrollState
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
						FontSize(uiFont(TypeBody)).
						FontWeight(700).
						Font("monospace").
						SingleLine().
						Shrink(0).
						TextColor(ThemePrimaryText(c))
					ui.Text(c, "•").FontSize(uiFont(TypeFootnote)).Shrink(0).TextColor(ThemeSecondaryText(c))
					ui.Text(c, doc.Subtitle).
						FontSize(uiFont(TypeFootnote)).
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
						openContextDocInEditor(s, filePath)
					}
				},
				Variant: ButtonGhost,
			})

			if snap.saving {
				LoadingIndicator(c, nil, "", IconSM)
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
				ui.Text(c, "保存").FontSize(uiFont(TypeFootnote)).TextColor(saveText)
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
	errMsg := snapshotContextPanel(s).err
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
					ui.Text(c, "新建自定义文档").FontSize(uiFont(TypeTitle)).FontWeight(600).TextColor(ThemePrimaryText(c))
				}))
				TextField(c, TextFieldProps{Key: "context-new-name", Value: &name, Label: "文档名称", Placeholder: "api-spec.md", FillWidth: true, AutoFocus: true, OnChange: func(value string) {
					cp := getContextPanelState(s)
					if cp == nil {
						return
					}
					contextPanelMu.Lock()
					cp.newName = value
					contextPanelMu.Unlock()
				}})
				if errMsg != "" {
					ui.Text(c, errMsg).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c)).FillWidth()
				}
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
