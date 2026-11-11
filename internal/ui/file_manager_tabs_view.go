package mcodeui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// fileManagerTabSnapshot 是渲染用的 tab 只读快照：渲染时不再读面板的活状态。
type fileManagerTabSnapshot struct {
	path         string
	temporary    bool
	kind         string
	fileLoading  bool
	fileErr      string
	content      string
	totalSize    int64
	hasMore      bool
	limitReached bool
	imageBmp     *ui.Bitmap
	mdPreview    bool
	svg          *ui.SVG
	svgPreview   bool
	hlFirst      int
	hlLast       int
	reveal       bool
	revealSeq    int
}

// fileManagerPanelSnapshot 是渲染用的面板快照：tab 列表、当前 tab 与拆分状态。
type fileManagerPanelSnapshot struct {
	tabs          []fileManagerTabSnapshot
	active        string
	showList      bool
	splitAnchor   string
	splitSide     string
	splitVertical bool
}

// fileManagerSnapshotLocked 在 fileManagerMu 下复制面板的 tab 与拆分状态。调用方持锁。
func fileManagerSnapshotLocked(st *fileManagerState) fileManagerPanelSnapshot {
	snap := fileManagerPanelSnapshot{
		active:        st.active,
		showList:      st.showList,
		splitAnchor:   st.splitAnchor,
		splitSide:     st.splitSide,
		splitVertical: st.splitVertical,
	}
	snap.tabs = make([]fileManagerTabSnapshot, 0, len(st.tabs))
	for _, tab := range st.tabs {
		snap.tabs = append(snap.tabs, fileManagerTabSnapshot{
			path:         tab.path,
			temporary:    tab.temporary,
			kind:         tab.kind,
			fileLoading:  tab.fileLoading,
			fileErr:      tab.fileErr,
			content:      tab.content,
			totalSize:    tab.totalSize,
			hasMore:      tab.hasMore,
			limitReached: tab.limitReached,
			imageBmp:     tab.imageBmp,
			mdPreview:    tab.mdPreview,
			svg:          tab.svg,
			svgPreview:   tab.svgPreview,
			hlFirst:      tab.hlFirst,
			hlLast:       tab.hlLast,
			reveal:       tab.reveal,
			revealSeq:    tab.revealSeq,
		})
	}
	return snap
}

// tabByPath 按路径取快照里的 tab。
func (snap fileManagerPanelSnapshot) tabByPath(path string) (fileManagerTabSnapshot, bool) {
	for _, tab := range snap.tabs {
		if tab.path == path {
			return tab, true
		}
	}
	return fileManagerTabSnapshot{}, false
}

// activeTab 返回当前 tab 的快照。
func (snap fileManagerPanelSnapshot) activeTab() (fileManagerTabSnapshot, bool) {
	if snap.active == "" {
		return fileManagerTabSnapshot{}, false
	}
	return snap.tabByPath(snap.active)
}

// fileManagerTabMaxWidth 是单个 tab 名的最大宽度。
const fileManagerTabMaxWidth float32 = 140

// renderFileManagerContent 渲染内容区：tab 栏与预览。窄栏导航由返回按钮切换。
func renderFileManagerContent(c *ui.Context, s *chatState, snap fileManagerPanelSnapshot, narrow, navOpen bool) {
	ui.Column(c.Key("file-manager-content")).
		FillWidth().
		FillHeight().
		MinWidth(0).
		Children(func() {
			renderFileManagerTabBar(c, s, snap, narrow, navOpen)
			renderFileManagerPanes(c, s, snap, !narrow)
		})
}

// renderFileManagerNavToggle 渲染内容头部的导航按钮：窄栏是返回文件树，
// 宽栏是展开或收起并排导航。只有图标，无障碍名称说明动作。
func renderFileManagerNavToggle(c *ui.Context, s *chatState, narrow, navOpen bool) {
	label := "返回文件树"
	icon := IconArrowLeft()
	action := func() { s.showFileManagerList() }
	if !narrow {
		if navOpen {
			label = "收起文件导航"
		} else {
			label = "展开文件导航"
		}
		icon = IconPanelLeft()
		action = func() {
			st := getFileManagerState(s)
			if st == nil {
				return
			}
			fileManagerMu.Lock()
			st.treeNavOpen = !st.treeNavOpen
			fileManagerMu.Unlock()
			s.invalidate()
		}
	}
	btn := ui.ButtonBase(c.Key("file-manager-nav-toggle")).
		Label(label).
		Tooltip(label).
		Size(ControlSM, ControlSM).
		Radius(RadiusSM).
		Center().
		Shrink(0)
	if btn.Hovered() {
		btn.Background(ThemeHover(c))
	}
	if btn.Clicked() {
		action()
	}
	btn.Children(func() {
		ui.Icon(c, icon).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
	})
}

// renderFileManagerTabBar 渲染标签栏：导航切换、tab 列表、拆分与方向。
func renderFileManagerTabBar(c *ui.Context, s *chatState, snap fileManagerPanelSnapshot, narrow, navOpen bool) {
	ui.Row(c.Key("file-manager-tab-bar")).
		FillWidth().
		Height(ControlLG).
		Padding(Space1, Space3).
		BorderWidth(0, 0, Hairline, 0).
		BorderColor(ThemeBorder(c)).
		AlignItems(ui.Center).
		Gap(Space1).
		Children(func() {
			renderFileManagerNavToggle(c, s, narrow, navOpen)

			// tab 多了就整行左右滑动，而不是把每个 tab 压扁。
			ui.ScrollHorizontal(c.Key("file-manager-tabs-scroll")).
				Grow(1).
				Height(RowHeightCompact).
				MinWidth(0).
				ClipX().
				Children(func() {
					ui.Row(c.Key("file-manager-tabs")).
						Gap(Space0).
						AlignItems(ui.Center).
						Children(func() {
							for _, tab := range snap.tabs {
								renderFileManagerTabItem(c, s, snap, tab)
							}
						})
				})

			splitLabel := "拆分"
			if snap.splitAnchor != "" {
				splitLabel = "取消拆分"
			}
			fileManagerBarButton(c, "file-manager-split-btn", splitLabel, func() {
				if snap.splitAnchor != "" {
					s.unsplitFileManager()
					return
				}
				s.splitFileManager()
			})

			if snap.splitAnchor != "" && !narrow {
				directionLabel := "切换为上下分栏"
				if snap.splitVertical {
					directionLabel = "切换为左右分栏"
				}
				fileManagerBarButton(c, "file-manager-split-direction-btn", directionLabel, func() {
					s.toggleFileManagerSplitDirection()
				})
			}

		})
}

// fileManagerBarButton 渲染标签栏里的小文字按钮：Label 与 Tooltip 同名，便于无障碍与测试定位。
func fileManagerBarButton(c *ui.Context, key, label string, onClick func()) {
	btn := ui.ButtonBase(c.Key(key)).
		Label(label).
		Tooltip(label).
		Radius(RadiusSM).
		Padding(Space1, Space2).
		Shrink(0)
	if btn.Hovered() {
		btn.Background(ThemeHover(c))
	}
	if btn.Clicked() {
		onClick()
	}
	btn.Children(func() {
		ui.Text(c, label).
			FontSize(TypeFootnote).
			TextColor(ThemeSecondaryText(c)).
			SingleLine()
	})
}

// renderFileManagerTabItem 渲染单个 tab：单击激活，双击固定，× 关闭。
// 临时 tab 的文字更淡；拆分时另一栏可见的 tab 用悬停底色标出来。
func renderFileManagerTabItem(c *ui.Context, s *chatState, snap fileManagerPanelSnapshot, tab fileManagerTabSnapshot) {
	name := fileManagerTabName(tab.path)
	tooltip := tab.path
	if tab.temporary {
		tooltip += "（预览中，双击固定）"
	}
	active := snap.active == tab.path
	inOtherPane := snap.splitAnchor != "" && !active &&
		(tab.path == snap.splitAnchor || tab.path == snap.splitSide)

	// 关闭 ×：当前 tab 一直显示，其他 tab 只在悬停时显示；
	// 隐藏时仍然占位（Invisible），避免悬停时 tab 宽度跳动。
	wrapper := ui.Row(c.Key("file-manager-tab-wrapper-" + tab.path)).Shrink(0)
	wrapper.Children(func() {
		showClose := active || wrapper.Hovered()
		closeClicked := false
		btn := TabButton(c, TabButtonProps{
			Key: "file-manager-tab-" + tab.path, Label: name, Tooltip: tooltip, Active: active,
		}, func() {
			ui.Row(c.Key("file-manager-tab-inner-" + tab.path)).
				Gap(Space0).
				AlignItems(ui.Center).
				Children(func() {
					color := ThemeSecondaryText(c)
					if active {
						color = ThemePrimaryText(c)
					}
					if tab.temporary {
						color = color.Alpha(0.75)
					}
					text := ui.Text(c, name).
						FontSize(TypeFootnote).
						SingleLine().
						MaxWidth(fileManagerTabMaxWidth).
						TextColor(color)
					if tab.temporary {
						text.Italic()
					}

					closeBtn := ui.ButtonBase(c.Key("file-manager-tab-close-"+tab.path)).
						Size(IconSM, IconSM).
						Radius(RadiusSM).
						Center().
						Label("关闭 " + name).
						Tooltip("关闭 " + name).
						Cursor(ui.CursorPointer)
					if showClose {
						if closeBtn.Hovered() {
							closeBtn.Background(ThemeOverlay(c, 0.20))
						}
						if closeBtn.Clicked() {
							// 关闭按钮先于外层 tab 评估点击，用标记拦下外层的激活。
							closeClicked = true
						}
					} else {
						closeBtn.Invisible()
					}
					closeBtn.Children(func() {
						ui.Icon(c, IconX()).
							Size(fileManagerIconTiny, fileManagerIconTiny).
							TextColor(ThemeSecondaryText(c))
					})
				})
		})
		if inOtherPane {
			btn.Background(ThemeHover(c))
		}
		btn.ContextMenu(fileManagerTabMenu(c, s, snap, tab))
		if closeClicked {
			s.closeFileManagerTab(tab.path)
			return
		}
		if btn.DoubleClicked() {
			s.pinFileManagerTab(tab.path)
			return
		}
		if btn.Clicked() {
			s.activateFileManagerTab(tab.path)
		}
	})
}

// renderFileManagerPanes 渲染预览内容区：未拆分或宽度不足时只有活动文件一栏，
// 拆分可用时两栏各占一半。拆分偏好不因窄栏而清除。
func renderFileManagerPanes(c *ui.Context, s *chatState, snap fileManagerPanelSnapshot, splitVisible bool) {
	// 代码预览的状态按「窗口 + 栏位前缀 + 路径」缓存，保存在各自窗口的 fileManagerState 里：
	// 本帧所有 tab 与两个栏位的组合都算存活，其余（已关闭的 tab）在渲染前清掉，
	// 避免缓存随打开过的文件一直增长。
	live := make(map[string]bool, len(snap.tabs)*2)
	for _, prefix := range []string{"file-manager-pane-0-", "file-manager-pane-1-"} {
		for _, tab := range snap.tabs {
			live[prefix+tab.path] = true
		}
	}
	pruneFileManagerCodeViews(getFileManagerState(s), live)

	anchor, hasAnchor := snap.tabByPath(snap.splitAnchor)
	if !splitVisible || snap.splitAnchor == "" || !hasAnchor {
		tab, ok := snap.activeTab()
		if !ok {
			return
		}
		renderFileManagerPane(c, s, snap, tab, 0)
		return
	}

	side, hasSide := snap.tabByPath(snap.splitSide)
	var container func(*ui.Context) ui.Element = ui.Row
	if snap.splitVertical {
		container = ui.Column
	}
	container(c.Key("file-manager-split")).
		Grow(1).
		FillWidth().
		Children(func() {
			renderFileManagerPane(c, s, snap, anchor, 0)

			// 中间用 1px 边框分隔两栏。
			if snap.splitVertical {
				ui.Row(c.Key("file-manager-split-divider")).
					FillWidth().
					Height(Hairline).
					Shrink(0).
					Background(ThemeBorder(c))
			} else {
				ui.Column(c.Key("file-manager-split-divider")).
					FillHeight().
					Width(Hairline).
					Shrink(0).
					Background(ThemeBorder(c))
			}

			if hasSide {
				renderFileManagerPane(c, s, snap, side, 1)
				return
			}
			renderFileManagerPane(c, s, snap, anchor, 1)
		})
}

// renderFileManagerPane 渲染一栏：上面是文件头，下面是可滚动的预览主体。
// keyPrefix 按栏区分（file-manager-pane-0- / file-manager-pane-1-），两栏同时渲染时键不冲突。
func renderFileManagerPane(c *ui.Context, s *chatState, snap fileManagerPanelSnapshot, tab fileManagerTabSnapshot, index int) {
	prefix := fmt.Sprintf("file-manager-pane-%d-", index)
	pane := ui.Column(c.Key(prefix + "pane")).
		Grow(1).
		MinWidth(0).
		MinHeight(0)
	switch {
	case snap.splitAnchor == "":
		pane.FillWidth()
	case snap.splitVertical:
		pane.FillWidth()
	default:
		pane.FillHeight()
	}
	pane.Children(func() {
		renderFileManagerFileHeader(c, s, tab, prefix)

		// 代码预览自带滚动与选区，直接占满剩余空间；再套 ui.Scroll 会出现两层滚动。
		if fileManagerCodePreviewTab(tab) {
			ui.Column(c.Key(prefix+"code-body")).
				Grow(1).
				FillWidth().
				MinHeight(0).
				Padding(Space2, Space3).
				Children(func() {
					renderFileManagerFileBody(c, s, tab, prefix)
				})
			return
		}

		ui.Scroll(c.Key(prefix+"scroll")).
			Grow(1).
			FillWidth().
			Padding(Space3, Space3).
			Children(func() {
				renderFileManagerFileBody(c, s, tab, prefix)
			})
	})
}
