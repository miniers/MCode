package mcodeui

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// 文件管理面板的一次性尺寸：令牌刻度覆盖不到的档位，集中在这里避免散落成字面量。
const (
	fileManagerHeaderHeight  float32 = 40  // 列表头与文件头的高度
	fileManagerErrorMaxWidth float32 = 300 // 错误文案的最大宽度
	fileManagerIconTiny      float32 = 8   // 清除搜索的叉号
	fileManagerIconAlert     float32 = 24  // 错误占位图标
)

// 文件预览的分块大小与上限：文本首块 64 KiB、累计 512 KiB；图片按 256 KiB 拼接、上限 8 MiB。
// 面板是只读预览，超限后停止加载并提示，避免一次点开超大文件把整段内容灌进内存。
const (
	fileManagerTextChunkSize  int64 = 65536
	fileManagerTextMaxBytes         = 512 * 1024
	fileManagerImageChunkSize int64 = 262144
	fileManagerImageMaxBytes        = 8 * 1024 * 1024
	// fileManagerSearchDebounce 是搜索框输入后的防抖间隔。
	fileManagerSearchDebounce = 200 * time.Millisecond
)

var (
	fileManagerMu     sync.Mutex
	fileManagerStates = make(map[*chatState]*fileManagerState)
)

// fileManagerState 维护文件管理面板的状态。
// 树导航（tree）与文件预览共用一个面板：tabs 里是按打开顺序排列的预览 tab，
// active 是当前 tab 的路径，showList 为 true 时窄栏显示树而不是内容。
// gen 是会话代数，异步结果回来时代数不符即丢弃；tab 的内容请求各有自己的代数。
type fileManagerState struct {
	threadID string
	dir      string // 树的当前目录：新建入口与目录菜单使用
	err      string // 根目录读取错误

	query           string
	searchGen       int
	searching       bool
	searchResults   []FileMatch
	searchTruncated bool
	searchErr       string
	searchSelected  int
	searchReveal    bool

	// 预览 tab 与拆分状态：拆分时第一栏固定为 splitAnchor，第二栏为 splitSide；
	// splitVertical 为 true 表示上下分栏，false（默认）表示左右分栏，取消拆分后保留这个偏好。
	tabs          []*fileManagerTab
	tabsScroll    ui.ScrollState
	active        string
	showList      bool
	splitAnchor   string
	splitSide     string
	splitVertical bool

	// tree 是文件树导航状态；宽栏并排时与内容共用右栏，窄栏与内容二选一。
	tree *fileTreeState
	// treeNavOpen 记录用户是否主动展开宽栏导航；窄栏不改变它。
	treeNavOpen bool

	// codeViews 是本窗口代码预览的编辑器状态缓存，key 为「栏位前缀 + 路径」。
	// 它只在这个窗口的 UI 渲染线程读写（渲染与 prune 都在每帧渲染里执行），不受 fileManagerMu 保护，
	// 也不进入面板快照；按窗口隔离，避免多窗口共用一个状态或被别的窗口的 prune 删掉。
	codeViews map[string]*fileManagerCodePreview

	// 新建 / 移到废纸篓的弹窗；同一时刻只开一个。
	ops fileManagerOpsDialog
	// opsSeq 是弹窗的代数计数器，只增不减：关掉再开时旧请求不会撞上新弹窗。
	opsSeq int

	// rename 是文件树与搜索列表的行内重命名状态。
	rename    fileManagerInlineRename
	renameSeq int

	// discardConfirm 是关闭未保存标签页时的丢弃确认弹窗状态。
	discardConfirm fileManagerDiscardConfirm
	// updates 保存后台文件保存等操作完成后待在 UI 线程执行的回调。
	updates []func()

	width float32 // 内容的实际布局宽度；偏好宽度只存于 Inspector。
	gen   int
}

// getFileManagerState 获取或初始化当前 chatState 的文件管理面板状态。
func getFileManagerState(s *chatState) *fileManagerState {
	if s == nil {
		return nil
	}
	slot := inspectorSession(s)
	if slot == nil {
		return nil
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	fileManagerStates[s] = slot.Files
	return slot.Files
}

// isFileManagerOpen 返回文件管理面板当前是否展开。
func isFileManagerOpen(s *chatState) bool {
	return inspectorPaneOpen(s, sidePaneFileID)
}

// toggleFileManager 切换文件管理面板；打开时关闭改动面板与网页预览，三者互斥。
func toggleFileManager(s *chatState) {
	if s == nil {
		return
	}
	st := getFileManagerState(s)
	if st == nil {
		return
	}

	if inspectorSelect(s, sidePaneFileID, true) {
		threadID, workDir := getSelectedThreadWorkDir(s)
		if workDir == "" || threadID == "" {
			threadID = ""
		}
		if threadID != "" {
			s.prepareFileManagerTree(threadID)
		}
	}
	s.invalidate()
}

// closeFileManager 关闭文件管理面板；已打开的 tab 保留，再次打开面板时仍在。
func closeFileManager(s *chatState) {
	if s == nil {
		return
	}
	if inspectorPaneOpen(s, sidePaneFileID) {
		inspectorClose(s)
	}
}

// loadFileManagerDir 兼容旧的目录入口：切换会话时重置面板，否则展开并刷新该目录。
// 树导航取代了平铺目录，目录打开不再替换内容，只影响树的展开状态。
func (s *chatState) loadFileManagerDir(threadID, dir string) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	dir = strings.Trim(strings.TrimSpace(dir), "/")

	fileManagerMu.Lock()
	if threadID == "" {
		fileManagerResetForThreadLocked(st, "")
		st.loadingResetLocked()
		fileManagerMu.Unlock()
		s.invalidate()
		return
	}
	if st.threadID != threadID {
		fileManagerResetForThreadLocked(st, threadID)
	}
	tree := ensureFileTreeLocked(st)
	addedOpen := false
	for _, path := range fileTreeAncestors(dir) {
		if path != "" && !tree.outline.Open.Has(path) {
			tree.outline.Open.Add(path)
			addedOpen = true
		}
	}
	if addedOpen {
		fileTreeBumpOutlineGenLocked(tree)
	}
	st.dir = dir
	fileManagerMu.Unlock()

	// 兼容旧入口的刷新语义：目标目录标记过期后重新读取，其它已加载目录不受影响。
	fileTreeInvalidate(s, threadID, []string{dir})
	s.invalidate()
}

// prepareFileManagerTree 在右栏显示前对齐会话与树状态，并在根目录未加载时发起读取。
func (s *chatState) prepareFileManagerTree(threadID string) {
	st := getFileManagerState(s)
	if st == nil || threadID == "" {
		return
	}
	fileManagerMu.Lock()
	if st.threadID != threadID {
		fileManagerResetForThreadLocked(st, threadID)
	}
	tree := ensureFileTreeLocked(st)
	needRoot := tree.children[""] == nil && !tree.loading[""]
	fileManagerMu.Unlock()
	if needRoot {
		s.fileTreeLoadDir(threadID, "")
	}
}

// fileManagerResetForThreadLocked 清空上一个会话的 tab 与树缓存，保留当前会话的恢复偏好。
// 调用方持 fileManagerMu。
func fileManagerResetForThreadLocked(st *fileManagerState, threadID string) {
	prev := st.threadID
	if prev == threadID {
		return
	}
	st.gen++
	st.searchGen++
	st.threadID = threadID
	st.dir = ""
	st.showList = true
	st.err = ""
	st.tabs = nil
	st.tabsScroll = ui.ScrollState{}
	st.active = ""
	st.splitAnchor = ""
	st.splitSide = ""
	st.ops = fileManagerOpsDialog{}
	st.rename = fileManagerInlineRename{}
	st.discardConfirm = fileManagerDiscardConfirm{}
	st.updates = nil
	st.query = ""
	st.searchResults = nil
	st.searchTruncated = false
	st.searchErr = ""
	st.searchSelected = 0
	st.searchReveal = false
	st.searching = false
	// 新会话的树从空缓存开始；若刚由会话偏好初始化且未加载数据，保留其恢复字段与排序设置。
	var seeded *fileTreeState
	if prev == "" && threadID != "" {
		seeded = st.tree
	}
	st.tree = nil
	tree := ensureFileTreeLocked(st)
	if seeded != nil {
		tree.restoreExpanded = seeded.restoreExpanded
		tree.restoreActive = seeded.restoreActive
		tree.restoreAnchor = seeded.restoreAnchor
		tree.anchor = seeded.anchor
		tree.sort = seeded.sort
	}
	modalDragReset(modalDragFileManagerOp)
}

// loadingResetLocked 清除根目录加载状态，用于没有工作区的空会话。
func (st *fileManagerState) loadingResetLocked() {
	if st.tree != nil {
		if st.tree.loading[""] {
			delete(st.tree.loading, "")
			fileTreeBumpOutlineGenLocked(st.tree)
		}
	}
	st.err = ""
}

// fileManagerSearch 按防抖间隔在后台搜索文件；过期结果用 searchGen 丢弃。
func (s *chatState) fileManagerSearch(threadID, query string) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}

	fileManagerMu.Lock()
	st.searchGen++
	gen := st.searchGen
	dirGen := st.gen
	st.query = query
	st.searchErr = ""
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		st.searching = false
		st.searchResults = nil
		st.searchTruncated = false
		st.searchSelected = 0
		st.searchReveal = false
		fileManagerMu.Unlock()
		s.invalidate()
		return
	}
	st.searching = true
	st.searchResults = nil
	st.searchTruncated = false
	st.searchSelected = 0
	st.searchReveal = false
	fileManagerMu.Unlock()
	s.invalidate()

	go func(tid, q string) {
		time.Sleep(fileManagerSearchDebounce)
		fileManagerMu.Lock()
		if st.searchGen != gen || st.gen != dirGen || st.threadID != tid || strings.TrimSpace(st.query) != q {
			fileManagerMu.Unlock()
			return
		}
		fileManagerMu.Unlock()

		var result FileSearchResult
		var err error
		if searcher, ok := s.client.(FileEntrySearcher); ok {
			result, err = searcher.SearchFileEntries(context.Background(), tid, q)
		} else {
			result.Entries, err = s.client.SearchFiles(context.Background(), tid, q)
		}

		fileManagerMu.Lock()
		defer fileManagerMu.Unlock()
		if st.searchGen != gen || st.gen != dirGen || st.threadID != tid || strings.TrimSpace(st.query) != q {
			return
		}
		st.searching = false
		st.searchSelected = 0
		st.searchReveal = false
		if err != nil {
			st.searchErr = err.Error()
			st.searchResults = nil
			st.searchTruncated = false
		} else {
			st.searchErr = ""
			st.searchResults = result.Entries
			st.searchTruncated = result.Truncated
		}
		s.invalidate()
	}(threadID, trimmed)
}

// renderFileManagerPanel 渲染右侧只读文件管理面板。
// 宽于 640 DIP 且用户展开导航时，树与内容并排；窄栏两者二选一，由内容头部的返回按钮切换。
func renderFileManagerPanel(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	st := getFileManagerState(s)
	if st == nil {
		return
	}

	s.applyFileManagerUpdates()

	threadID, workDir := getSelectedThreadWorkDir(s)
	if threadID != "" && workDir != "" {
		s.prepareFileManagerTree(threadID)
	}

	fileManagerMu.Lock()
	syncFileManagerDirtyLocked(st)
	width := st.width
	navOpen := st.treeNavOpen
	snap := fileManagerSnapshotLocked(st)
	fileManagerMu.Unlock()

	hasActive := false
	if _, ok := snap.activeTab(); ok {
		hasActive = true
	}
	narrow := width < fileTreeSplitMinWidth
	treeOnly := !hasActive
	if narrow {
		treeOnly = !hasActive || snap.showList
	}

	// 单栏树切换到并排导航（或关闭最后标签切回单栏树）时，树节点会随父容器重挂；
	// 若切屏前树持有焦点，需要把焦点带回重挂后的树节点。
	fileManagerMu.Lock()
	if st.tree != nil {
		if st.tree.lastTreeOnlyInit && st.tree.lastTreeOnly != treeOnly && st.tree.wasFocused {
			st.tree.focusPending = true
		}
		st.tree.lastTreeOnlyInit = true
		st.tree.lastTreeOnly = treeOnly
	}
	fileManagerMu.Unlock()

	inspectorFollowSurface(c, s, threadID, "file-manager-panel").
		Fill().
		Background(ThemeWindowBg(c)).
		BorderWidth(0, 0, 0, Hairline).
		BorderColor(ThemeBorder(c)).
		Children(func() {
			switch {
			case treeOnly:
				renderFileManagerNavigation(c, s, threadID, workDir)
			case narrow:
				renderFileManagerContent(c, s, snap, true, navOpen)
			default:
				ui.Row(c.Key("file-manager-split-nav")).
					FillWidth().
					FillHeight().
					Children(func() {
						if navOpen {
							ui.Column(c.Key("file-manager-nav-column")).
								Label("文件导航").
								Width(fileTreeNavWidth).
								Shrink(0).
								FillHeight().
								Clip().
								BorderWidth(0, 0, 0, Hairline).
								BorderColor(ThemeBorder(c)).
								Transition(motionCollapse(c)).
								Children(func() {
									renderFileManagerNavigation(c, s, threadID, workDir)
								})
						}
						ui.Column(c.Key("file-manager-content-column")).
							Grow(1).
							MinWidth(0).
							FillHeight().
							Transition(motionCollapse(c)).
							Children(func() {
								renderFileManagerContent(c, s, snap, false, navOpen)
							})
					})
			}
		})

	// 写操作弹窗与未保存丢弃确认弹窗挂在面板末尾：DialogBase 自己挂到 overlay root，不受面板裁剪影响。
	renderFileManagerOpsDialog(c, s)
	renderFileManagerDiscardDialog(c, s)
}

// fileManagerThreadID 返回面板当前绑定的会话 ID（面包屑跳转等即时操作使用）。
func fileManagerThreadID(s *chatState) string {
	st := getFileManagerState(s)
	if st == nil {
		return ""
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	return st.threadID
}

// renderFileManagerSearchResults 渲染搜索结果列表：结果与目录列表共用同一套行渲染。
func renderFileManagerSearchResults(c *ui.Context, s *chatState, searching bool, errMsg string, results []FileMatch, truncated bool) {
	if errMsg != "" {
		renderFileManagerError(c, s, "", errMsg, nil)
		return
	}
	if searching && len(results) == 0 {
		LoadingIndicator(c, "file-manager-search-loading", "正在搜索…", IconSM).Margin(ui.Auto)
		return
	}
	if len(results) == 0 {
		if truncated {
			ui.Text(c, "搜索结果已截断").FontSize(uiFont(TypeFootnote)).TextColor(ThemeWarning(c)).Margin(ui.Auto)
			return
		}
		EmptyState(c, EmptyStateProps{Icon: iconFileSearch, Title: "没有匹配的文件", Compact: true}).Margin(ui.Auto)
		return
	}

	st := getFileManagerState(s)
	selectedIdx := 0
	reveal := false
	threadID := ""
	if st != nil {
		fileManagerMu.Lock()
		threadID = st.threadID
		selectedIdx = st.searchSelected
		if selectedIdx < 0 {
			selectedIdx = 0
		} else if selectedIdx >= len(results) {
			selectedIdx = len(results) - 1
		}
		st.searchSelected = selectedIdx
		reveal = st.searchReveal
		st.searchReveal = false
		fileManagerMu.Unlock()
	}

	ui.Column(c.Key("file-manager-search-results")).
		Gap(Hairline).
		FillWidth().
		Children(func() {
			for i, match := range results {
				idx := i
				item := match
				icon := IconFile()
				if item.IsDir {
					icon = IconFolder()
				}
				isSelected := idx == selectedIdx
				renderFileManagerRow(c, s, fileManagerRowProps{
					key:            "file-manager-search-item-" + item.Path,
					icon:           icon,
					name:           item.Path,
					isDir:          item.IsDir,
					selected:       isSelected,
					scrollIntoView: isSelected && reveal,
					threadID:       threadID,
					entry:          FileEntry{Name: item.Name, Path: item.Path, IsDir: item.IsDir},
					onOpen: func(pin bool, _ ui.Modifiers) {
						if st != nil {
							fileManagerMu.Lock()
							st.searchSelected = idx
							fileManagerMu.Unlock()
						}
						if item.IsDir {
							s.fileTreeRevealDir(threadID, item.Path)
							return
						}
						s.openFileManagerFile(threadID, item.Path, pin)
					},
				})
			}
			if truncated {
				ui.Text(c, "搜索结果已截断").
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeWarning(c)).
					Padding(Space2, Space1, 0, Space1)
			}
		})
}

// fileManagerRowProps 是列表行需要的展示与回调字段。
type fileManagerRowProps struct {
	key            string
	icon           *ui.SVG
	name           string
	ignored        bool
	protected      bool
	isDir          bool
	inTree         bool
	deleted        bool
	sortField      fileSortField
	selected       bool
	focused        bool
	scrollIntoView bool
	hasGit         bool
	git            GitChangedFile
	// threadID 与 entry 供右键菜单使用：菜单里的打开、复制路径与写操作都按它们做。
	threadID string
	entry    FileEntry
	// onOpen 打开文件：pin 为 false 表示单击（临时预览），true 表示双击（固定 tab）。
	onOpen func(pin bool, mods ui.Modifiers)
}

// renderFileManagerRow 渲染单行条目：图标、名称（或行内重命名输入框）、可选锁图标、Git 标记与悬停「更多操作」按钮。
// Protected 条目点击无效（次要文字色、不绑定打开回调）。
func renderFileManagerRow(c *ui.Context, s *chatState, props fileManagerRowProps) {
	st := getFileManagerState(s)
	isRenaming := false
	renameErr := ""
	renameBusy := false
	if st != nil {
		fileManagerMu.Lock()
		if st.rename.active && st.rename.path == props.entry.Path && st.threadID == props.threadID {
			isRenaming = true
			renameErr = st.rename.err
			renameBusy = st.rename.busy
		}
		fileManagerMu.Unlock()
	}

	buildRow := func() {
		row := ui.Row(c.Key(props.key)).
			FillWidth().
			Height(RowHeightCompact).
			Padding(0, Space2).
			Radius(RadiusXS).
			Gap(Space2).
			AlignItems(ui.Center)
		rowHovered := row.Hovered()
		if props.selected && !props.inTree {
			row.Selected(true)
		} else if !props.selected && rowHovered && !props.protected && !isRenaming {
			row.Background(ThemeHover(c))
		}
		if props.scrollIntoView {
			row.ScrollIntoView()
		}

		nameColor := ThemePrimaryText(c)
		if props.ignored || props.protected {
			nameColor = ThemeSecondaryText(c)
		}

		morePressed := false
		showMore := !isRenaming && (rowHovered || props.selected || props.focused || row.FocusWithin())

		// 右键菜单与行尾「更多操作」按钮共用同一套菜单项；重命名编辑中不抢右键。
		if !isRenaming {
			row.ContextMenu(fileManagerEntryMenu(c, s, props.threadID, props.entry))
		}

		row.Children(func() {
			iconColor := ThemeSecondaryText(c)
			if props.isDir {
				iconColor = ThemeAccent(c)
			}
			ui.Icon(c, props.icon).Size(IconSM, IconSM).TextColor(iconColor).Shrink(0)

			if isRenaming && st != nil {
				borderColor := ThemeAccent(c)
				if renameErr != "" {
					borderColor = ThemeDanger(c)
				}
				in := ui.TextInput(c.Key(props.key+"-rename-input"), &st.rename.draft).
					Label("重命名").
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemePrimaryText(c)).
					Background(ThemeSurfaceCard(c)).
					BorderWidth(1.5).
					BorderColor(borderColor).
					Radius(RadiusXS).
					Padding(0, Space1).
					Height(20).
					Grow(1).
					MinWidth(0).
					Disabled(renameBusy).
					AutoFocus()
				if in.Changed() {
					fileManagerMu.Lock()
					if st.rename.active && st.rename.path == props.entry.Path {
						st.rename.err = ""
					}
					fileManagerMu.Unlock()
				}
				in.OnTextKey(func(k ui.TextKey) bool {
					if k.Key == ui.KeyEnter && k.Mods == 0 {
						fileManagerMu.Lock()
						if st.rename.active && st.rename.path == props.entry.Path {
							st.rename.draft = k.Text()
						}
						fileManagerMu.Unlock()
						s.submitFileManagerInlineRename(true)
						return true
					}
					if k.Key == ui.KeyEscape {
						s.cancelFileManagerInlineRename(true)
						return true
					}
					return false
				})
				// 失焦提交：获得过焦点后又失去才算失焦，避免 AutoFocus 生效前的首帧误提交。
				focused := in.Focused()
				fileManagerMu.Lock()
				stillRenaming := st.rename.active && st.rename.path == props.entry.Path
				blurred := stillRenaming && !st.rename.busy && st.rename.hadFocus && !focused
				if stillRenaming {
					if focused {
						if !st.rename.hadFocus && st.rename.needSelect {
							in.SetTextSelection(0, fileManagerRenameSelectEnd(st.rename.draft, props.isDir))
							st.rename.needSelect = false
						}
						st.rename.hadFocus = true
					} else {
						st.rename.hadFocus = false
					}
				}
				fileManagerMu.Unlock()
				if blurred {
					s.submitFileManagerInlineRename(false)
				}
				return
			}

			renderName := func(grow bool) {
				nameText := ui.Text(c, props.name).
					FontSize(uiFont(TypeFootnote)).
					TextColor(nameColor).
					SingleLine().
					TruncateMiddle().
					MinWidth(0)
				if grow {
					nameText.Grow(1)
				} else {
					nameText.Shrink(1)
				}
				if props.entry.Path != "" {
					if w := nameText.LayoutBounds().W; w > 0 {
						naturalW, _ := cachedMeasureText(c, 0, ui.Span{Text: props.name, Size: uiFont(TypeFootnote)})
						if naturalW > w+Hairline {
							nameText.Tooltip(props.entry.Path)
						}
					}
				}
			}
			if props.protected {
				ui.Row(c.Key(props.key + "-name-wrap")).
					Grow(1).
					MinWidth(0).
					AlignItems(ui.Center).
					Gap(Space1).
					Children(func() {
						renderName(false)
						ui.Icon(c, IconLock()).
							Size(12, 12).
							TextColor(ThemeSecondaryText(c)).
							Shrink(0)
					})
			} else {
				renderName(true)
			}

			metaText := ""
			if props.inTree && !props.isDir && !props.entry.IsSymlink && !props.deleted {
				if props.sortField == fileSortModTime {
					if !props.entry.ModTime.IsZero() {
						metaText = formatRelativeTime(props.entry.ModTime)
					}
				} else {
					metaText = formatFileSize(props.entry.Size)
				}
			}

			if metaText != "" || props.hasGit {
				trailing := ui.Row(c.Key(props.key + "-trailing")).
					Shrink(0).
					AlignItems(ui.Center).
					Gap(Space1)
				if showMore {
					trailing.Opacity(0)
				}
				trailing.Children(func() {
					if metaText != "" {
						ui.Text(c, metaText).
							FontSize(uiFont(TypeCaption)).
							FontFeatures("tnum").
							TextColor(ThemeSecondaryText(c)).
							SingleLine().
							Shrink(0)
					}
					if props.hasGit {
						renderFileTreeGitTag(c, "file-tree-git-"+props.entry.Path, props.git)
					}
				})
			}

			// 菜单选中项只交给下一帧仍构建的按钮，指针移进菜单后行不再悬停，所以常驻构建并用 Hidden 隐藏。
			// 按钮叠在行尾，避免悬停时行宽与文件名截断位置跳动。
			moreWrap := ui.Row(c.Key(props.key + "-more-wrap")).
				Absolute().
				Right(Space2).
				Top(0).
				Bottom(0).
				AlignItems(ui.Center).
				Hidden(!showMore)
			moreWrap.Children(func() {
				more := ui.ButtonBase(c.Key(props.key+"-more")).
					Label("更多操作").
					Tooltip("更多操作").
					KeepFocus().
					Radius(RadiusXS).
					Padding(Hairline, Space1).
					Cursor(ui.CursorPointer)
				focusRing(c, more)
				moreScale := motionPressScale(c, more, "button-press-scale", more.Pressed())
				more.Scale(moreScale, moreScale)
				if more.Hovered() {
					more.Background(ThemeSelected(c))
				}
				if more.Pressed() {
					morePressed = true
				}
				more.Menu(fileManagerEntryMenu(c, s, props.threadID, props.entry))
				more.Children(func() {
					ui.Icon(c, IconMoreHorizontal()).
						Size(IconSM, IconSM).
						TextColor(ThemeSecondaryText(c))
				})
			})

			if !props.protected && !morePressed && props.onOpen != nil {
				// 双击固定预览，单击只开临时 tab；双击先于 Clicked 拦下。
				if row.DoubleClicked() {
					props.onOpen(true, 0)
				} else if row.Clicked() {
					props.onOpen(false, row.ClickModifiers())
				}
			}
		})
	}

	if !isRenaming {
		buildRow()
		return
	}

	ui.Column(c.Key(props.key + "-rename-box")).
		FillWidth().
		Children(func() {
			buildRow()
			if renameErr != "" {
				ui.Row(c.Key(props.key+"-rename-error")).
					FillWidth().
					Margin(Space0, Space2, Space1, Space7).
					Padding(Space0, Space2).
					Radius(RadiusXS).
					Background(ThemeDanger(c).Alpha(0.10)).
					Border(Hairline, ThemeDanger(c).Alpha(0.28)).
					Children(func() {
						ui.Text(c, renameErr).
							FontSize(uiFont(TypeCaption)).
							TextColor(ThemeDanger(c)).
							Wrap()
					})
			}
		})
}

// renderFileManagerTag 渲染行内单字母状态标签。
func renderFileManagerTag(c *ui.Context, key, label string, color ui.Color) {
	ui.Row(c.Key(key)).
		Padding(Hairline, Space1).
		Radius(RadiusXS).
		Background(color.Alpha(0.12)).
		Shrink(0).
		Children(func() {
			ui.Text(c, label).
				FontSize(uiFont(TypeCaption)).
				FontWeight(500).
				TextColor(color)
		})
}

// fileManagerEntryIcon 返回条目图标：目录用文件夹，文件（含符号链接）用文件图标。
func fileManagerEntryIcon(entry FileEntry) *ui.SVG {
	if entry.IsDir {
		return IconFolder()
	}
	return IconFile()
}

// renderFileManagerError 渲染面板内的错误行（不弹窗），可选重试按钮。
// keyPrefix 用于区分同时渲染的两栏。
func renderFileManagerError(c *ui.Context, s *chatState, keyPrefix, errMsg string, onRetry func()) {
	ui.Column(c.Key(keyPrefix + "file-manager-error")).
		Center().
		Gap(Space3).
		Margin(ui.Auto).
		Children(func() {
			ui.Icon(c, IconOctagonAlert()).
				Size(fileManagerIconAlert, fileManagerIconAlert).
				TextColor(ThemeDanger(c).Alpha(0.6))
			ui.Text(c, "无法读取").
				FontSize(uiFont(TypeBody)).
				FontWeight(500).
				TextColor(ThemePrimaryText(c))
			ui.Text(c, errMsg).
				FontSize(uiFont(TypeFootnote)).
				MaxWidth(fileManagerErrorMaxWidth).
				TextAlign(ui.Center).
				Wrap().
				TextColor(ThemeSecondaryText(c))
			if onRetry != nil {
				Button(c, ButtonOpts{
					Key:     keyPrefix + "file-manager-error-retry",
					Text:    "重试",
					Variant: ButtonSecondary,
					OnClick: onRetry,
				})
			}
		})
}

// fileManagerPreviewFontSize 返回文件预览正文的字号：跟随设置里的编辑器字号，每帧读取以便即时生效。
func fileManagerPreviewFontSize(s *chatState) float32 {
	return editorFontSize()
}

// renderFileManagerPreviewToggle 渲染文件预览区的「预览/源码」分段切换。
func renderFileManagerPreviewToggle(c *ui.Context, key string, preview bool, onToggle func(bool)) {
	selected := 1
	if preview {
		selected = 0
	}
	Segmented(c, key, &selected, []Segment{
		{Label: "预览"},
		{Label: "源码"},
	}, func(idx int) {
		onToggle(idx == 0)
	}).Shrink(0)
}

// renderFileManagerMarkdownToggle 渲染 Markdown 文件的「预览/源码」切换。
func renderFileManagerMarkdownToggle(c *ui.Context, s *chatState, tab fileManagerTabSnapshot, keyPrefix string) {
	renderFileManagerPreviewToggle(c, keyPrefix+"file-manager-md-toggle", tab.mdPreview, func(preview bool) {
		s.setFileManagerTabMarkdownPreview(tab.path, preview)
	})
}

// renderFileManagerFileBody 渲染某一栏的预览主体：文本（含 Markdown 预览）、图片或二进制提示。
func renderFileManagerFileBody(c *ui.Context, s *chatState, tab fileManagerTabSnapshot, keyPrefix string) {
	if tab.fileErr != "" {
		renderFileManagerError(c, s, keyPrefix, tab.fileErr, nil)
		return
	}
	if tab.fileLoading && tab.content == "" && tab.imageBmp == nil {
		ui.Row(c.Key(keyPrefix + "file-manager-file-loading")).
			Gap(Space3).
			AlignItems(ui.Center).
			Margin(ui.Auto).
			Children(func() {
				LoadingIndicator(c, nil, "正在读取文件…", IconSM)
			})
		return
	}

	switch tab.kind {
	case "image":
		if tab.imageBmp == nil {
			LoadingIndicator(c, keyPrefix+"file-manager-image-loading", "", IconSM, ThemeAccent(c)).
				Margin(ui.Auto)
			return
		}
		ui.Column(c.Key(keyPrefix + "file-manager-image-col")).
			AlignItems(ui.Center).
			Gap(Space3).
			FillWidth().
			Children(func() {
				img := ui.Image(c.Key(keyPrefix+"file-manager-image"), tab.imageBmp).
					MaxWidthPercent(100).
					Radius(RadiusXS)
				if w, h := tab.imageBmp.Size(); w > 0 && h > 0 {
					img.AspectRatio(float32(w) / float32(h))
				}
			})
	case "binary":
		abs := fileManagerWorkspacePath(s, fileManagerThreadID(s), tab.path)
		ui.Column(c.Key(keyPrefix + "file-manager-binary")).
			Center().
			Gap(Space3).
			Margin(ui.Auto).
			Children(func() {
				ui.Text(c, "无法预览此文件").
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeSecondaryText(c))
				if tab.binaryLocal {
					Button(c, ButtonOpts{
						Key:     keyPrefix + "file-manager-binary-open",
						Text:    "用默认应用打开",
						Variant: ButtonSecondary,
						Size:    ControlSM,
						OnClick: func() {
							_ = fileManagerOpenDefault(abs)
						},
					})
				}
			})
	default:
		if tab.kind == "" && tab.content == "" {
			return
		}
		if tab.svgPreview && isInspectorSVGPath(tab.path) && tab.svg != nil {
			renderFileManagerSVGPreview(c, tab, keyPrefix)
			return
		}
		mdPreview := tab.mdPreview && isMarkdownPath(tab.path)
		if mdPreview && tab.content == "" {
			ui.Text(c, "空文件").
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemeSecondaryText(c)).
				Margin(ui.Auto)
			return
		}
		if mdPreview {
			// Markdown 交给 markdown.View：表格、列表、代码块高亮都由它渲染，
			// 正文默认可选中复制。配置按被预览文件所在目录绑定相对路径解析，正文比代码字号大一档。
			baseDir := ""
			if abs := fileManagerWorkspacePath(s, fileManagerThreadID(s), tab.path); abs != "" {
				baseDir = filepath.Dir(abs)
			} else if filepath.IsAbs(tab.path) {
				baseDir = filepath.Dir(tab.path)
			}
			markdown.View(c.Key(keyPrefix+"file-manager-md-view"), tab.content, markdownDocOptions(c, s, fileManagerPreviewFontSize(s)+1, baseDir)).
				FillWidth()
		} else {
			// 文本预览用只读代码编辑器：语法高亮、行号、可选中复制，滚动也由它管理。
			inspectorFollowCodePreview(c, s, tab, keyPrefix)
		}
		if tab.limitReached {
			ui.Text(c, "已达预览上限").
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemeWarning(c)).
				Padding(Space3, 0, 0, 0)
			return
		}
		if tab.hasMore {
			Button(c, ButtonOpts{
				Key:     keyPrefix + "file-manager-load-more",
				Text:    "加载更多",
				Loading: tab.fileLoading,
				Variant: ButtonSecondary,
				OnClick: func() { s.loadMoreFileManagerTab(tab.path) },
			})
		}
	}
}

// isMarkdownPath 判断文件是否为 Markdown（决定是否提供源码/预览切换）。
func isMarkdownPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".mdx":
		return true
	}
	return false
}
