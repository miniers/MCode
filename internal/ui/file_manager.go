package mcodeui

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/richtext"
)

// 文件管理面板的宽度范围，与右侧改动面板、网页预览面板同属主区右侧分栏。
const (
	FileManagerPanelMinWidth     float32 = 220
	FileManagerPanelDefaultWidth float32 = 420
	FileManagerPanelMaxWidth     float32 = 720
)

// fileManagerCompactHeaderWidth 是文件预览头部收紧按钮文案的宽度阈值：
// 面板被拖到很窄时，文件名与「引用到输入框」按钮同处一行会互相挤压。
const fileManagerCompactHeaderWidth float32 = 260

// 文件管理面板的一次性尺寸：令牌刻度覆盖不到的档位，集中在这里避免散落成字面量。
const (
	fileManagerHeaderHeight  float32 = 40  // 列表头与文件头的高度
	fileManagerErrorMaxWidth float32 = 300 // 错误文案的最大宽度
	fileManagerIconTiny      float32 = 8   // 清除搜索的叉号
	fileManagerIconChevron   float32 = 10  // 面包屑分隔符
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

// fileBreadcrumb 是目录面包屑的一段：标签与它的相对路径（根目录为空串）。
type fileBreadcrumb struct {
	label string
	path  string
}

var (
	fileManagerMu     sync.Mutex
	fileManagerStates = make(map[*chatState]*fileManagerState)
)

// fileManagerState 维护文件管理面板的状态。
// 列表（目录 / 搜索结果）与文件预览共用一个面板：tabs 里是按打开顺序排列的预览 tab，
// active 是当前 tab 的路径，showList 为 true 时显示目录列表。gen 是目录请求代数，
// 异步结果回来时代数不符即丢弃；tab 的内容请求各有自己的代数（见 fileManagerTab.gen）。
type fileManagerState struct {
	threadID  string
	dir       string
	entries   []FileEntry
	truncated bool
	loading   bool
	err       string // 目录及文件操作错误

	query           string
	searchGen       int
	searching       bool
	searchResults   []FileMatch
	searchTruncated bool
	searchErr       string

	// 预览 tab 与拆分状态：拆分时第一栏固定为 splitAnchor，第二栏为 splitSide；
	// splitVertical 为 true 表示上下分栏，false（默认）表示左右分栏，取消拆分后保留这个偏好。
	tabs          []*fileManagerTab
	active        string
	showList      bool
	splitAnchor   string
	splitSide     string
	splitVertical bool

	// codeViews 是本窗口代码预览的编辑器状态缓存，key 为「栏位前缀 + 路径」。
	// 它只在这个窗口的 UI 渲染线程读写（渲染与 prune 都在每帧渲染里执行），不受 fileManagerMu 保护，
	// 也不进入面板快照；按窗口隔离，避免多窗口共用一个状态或被别的窗口的 prune 删掉。
	codeViews map[string]*fileManagerCodePreview

	// 新建 / 重命名 / 移到废纸篓的弹窗；同一时刻只开一个。
	ops fileManagerOpsDialog
	// opsSeq 是弹窗的代数计数器，只增不减：关掉再开时旧请求不会撞上新弹窗。
	opsSeq int

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
		fileManagerMu.Lock()
		needLoad := st.gen == 0 || st.threadID != threadID
		fileManagerMu.Unlock()
		if needLoad {
			s.loadFileManagerDir(threadID, "")
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

// loadFileManagerDir 在后台异步加载指定会话下某个相对目录的条目。
// 传空 threadID 表示没有可用工作区：清空列表并停止加载。
func (s *chatState) loadFileManagerDir(threadID, dir string) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}

	fileManagerMu.Lock()
	prevThread := st.threadID
	st.gen++
	st.searchGen++
	gen := st.gen
	st.threadID = threadID
	st.dir = dir
	st.entries = nil
	st.truncated = false
	st.err = ""
	st.loading = threadID != ""
	// 回到列表视图。同一会话内切换目录保留已打开的 tab，只有切换会话才清空。
	st.showList = true
	if prevThread != threadID {
		st.tabs = nil
		st.active = ""
		st.splitAnchor = ""
		st.splitSide = ""
		// 弹窗里的目标和 tab 都属于旧会话，一并丢掉。
		st.ops = fileManagerOpsDialog{}
		modalDragReset(modalDragFileManagerOp)
	}
	// 搜索状态与目录绑定：换目录时清空搜索词，避免列表与搜索结果错位。
	st.query = ""
	st.searchResults = nil
	st.searchTruncated = false
	st.searchErr = ""
	st.searching = false
	fileManagerMu.Unlock()
	s.invalidate()

	if threadID == "" {
		return
	}

	go func(tid, sub string) {
		listing, err := s.client.FilesList(context.Background(), tid, sub)
		fileManagerMu.Lock()
		defer fileManagerMu.Unlock()
		if st.gen != gen {
			return
		}
		st.loading = false
		if err != nil {
			st.err = err.Error()
			st.entries = nil
			st.truncated = false
		} else {
			st.entries = listing.Entries
			st.truncated = listing.Truncated
			if listing.Path != "" {
				st.dir = listing.Path
			}
		}
		s.invalidate()
	}(threadID, dir)
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
		fileManagerMu.Unlock()
		s.invalidate()
		return
	}
	st.searching = true
	st.searchResults = nil
	st.searchTruncated = false
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

// fileManagerBreadcrumb 把当前相对目录拆成面包屑：首段「根目录」，其后每段累加一级路径。
func fileManagerBreadcrumb(dir string) []fileBreadcrumb {
	items := []fileBreadcrumb{{label: "根目录", path: ""}}
	trimmed := strings.Trim(strings.TrimSpace(dir), "/")
	if trimmed == "" {
		return items
	}
	current := ""
	for _, part := range strings.Split(trimmed, "/") {
		if part == "" {
			continue
		}
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		items = append(items, fileBreadcrumb{label: part, path: current})
	}
	return items
}

// appendFileReferenceToDraft 把「@相对路径 」追加到当前会话的 composer 草稿末尾，
// 与补全插入的 @ 引用格式一致；草稿非空且不以空白结尾时先补一个空格。
func appendFileReferenceToDraft(s *chatState, relPath string) {
	if s == nil || relPath == "" {
		return
	}
	s.mu.Lock()
	if s.draft != "" && !strings.HasSuffix(s.draft, " ") && !strings.HasSuffix(s.draft, "\n") && !strings.HasSuffix(s.draft, "\t") {
		s.draft += " "
	}
	s.draft += "@" + relPath + " "
	s.draftText = richtext.NewText(s.draft)
	s.completionKind = completionNone
	s.mu.Unlock()
	s.invalidate()
}

// renderFileManagerPanel 渲染右侧只读文件管理面板。
func renderFileManagerPanel(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	st := getFileManagerState(s)
	if st == nil {
		return
	}

	threadID, workDir := getSelectedThreadWorkDir(s)

	fileManagerMu.Lock()
	panelThread := st.threadID
	firstLoad := st.gen == 0
	fileManagerMu.Unlock()

	// 线程切换（或首次拿到工作目录）时重置面板并重新加载根目录。
	if (firstLoad || threadID != panelThread) && threadID != "" && workDir != "" {
		s.loadFileManagerDir(threadID, "")
	}

	fileManagerMu.Lock()
	dir := st.dir
	listLoading := st.loading
	listErr := st.err
	truncated := st.truncated
	entries := make([]FileEntry, len(st.entries))
	copy(entries, st.entries)
	query := st.query
	searching := st.searching
	searchResults := make([]FileMatch, len(st.searchResults))
	copy(searchResults, st.searchResults)
	searchTruncated := st.searchTruncated
	searchErr := st.searchErr
	searchQueryPtr := &st.query
	snap := fileManagerSnapshotLocked(st)
	fileManagerMu.Unlock()

	showList := snap.showList || len(snap.tabs) == 0

	ui.Column(c.Key("file-manager-panel")).
		Fill().
		Background(ThemeWindowBg(c)).
		BorderWidth(0, 0, 0, Hairline).
		BorderColor(ThemeBorder(c)).
		Children(func() {
			if showList {
				renderFileManagerHeader(c, s, dir)
				// 列表视图里也留一行 tab：从预览回到目录后还能直接点回某个文件。
				if len(snap.tabs) > 0 {
					renderFileManagerTabBar(c, s, snap, false)
				}
				renderFileManagerSearch(c, s, searchQueryPtr, threadID)

				ui.Scroll(c.Key("file-manager-scroll")).
					Grow(1).
					FillWidth().
					Padding(Space3, Space3).
					ContextMenu(fileManagerDirMenu(c, s)).
					Children(func() {
						if workDir == "" {
							ui.Text(c, "未选择会话").
								FontSize(TypeBody).
								TextColor(ThemeSecondaryText(c)).
								Margin(ui.Auto)
							return
						}
						if query != "" {
							renderFileManagerSearchResults(c, s, searching, searchErr, searchResults, searchTruncated)
							return
						}
						switch {
						case listErr != "":
							renderFileManagerError(c, s, "", listErr, func() {
								s.loadFileManagerDir(threadID, dir)
							})
						case listLoading && len(entries) == 0:
							LoadingIndicator(c, "file-manager-loading", "正在读取目录…", IconSM).Margin(ui.Auto)
						case len(entries) == 0:
							EmptyState(c, EmptyStateProps{Title: "空目录", Compact: true}).Margin(ui.Auto)
						default:
							renderFileManagerEntries(c, s, threadID, entries)
						}
						if truncated && query == "" && listErr == "" {
							ui.Text(c, "目录条目已截断（仅显示前 1000 个条目）").
								FontSize(TypeFootnote).
								TextColor(ThemeWarning(c)).
								Padding(Space2, Space1, 0, Space1)
						}
					})
				return
			}

			// 预览模式：上面是 tab 栏，下面是内容区（未拆分时一栏，拆分时两栏）。
			renderFileManagerTabBar(c, s, snap, true)
			renderFileManagerPanes(c, s, snap)
		})

	// 写操作弹窗挂在面板末尾：DialogBase 自己挂到 overlay root，不受面板裁剪影响。
	renderFileManagerOpsDialog(c, s)
}

// renderFileManagerHeader 渲染列表视图的头部：标题、目录面包屑与关闭按钮。
func renderFileManagerHeader(c *ui.Context, s *chatState, dir string) {
	ui.Row(c.Key("file-manager-header")).
		FillWidth().
		Height(fileManagerHeaderHeight).
		Padding(Space3, Space4).
		BorderWidth(0, 0, Hairline, 0).
		BorderColor(ThemeBorder(c)).
		AlignItems(ui.Center).
		Gap(Space2).
		Children(func() {
			ui.Text(c, "文件").
				FontSize(TypeProse).
				FontWeight(600).
				TextColor(ThemePrimaryText(c))

			crumbs := fileManagerBreadcrumb(dir)
			ui.Row(c.Key("file-manager-crumbs")).
				Gap(Space0).
				AlignItems(ui.Center).
				Grow(1).
				MinWidth(0).
				ClipX().
				Children(func() {
					for i, crumb := range crumbs {
						item := crumb
						if i > 0 {
							ui.Icon(c, IconChevronRight()).
								Size(fileManagerIconChevron, fileManagerIconChevron).
								TextColor(ThemeSecondaryText(c).Alpha(0.5))
						}
						btn := ui.ButtonBase(c.Key("file-manager-crumb-"+item.path)).
							Label(item.label).
							Tooltip(item.path).
							Radius(RadiusXS).
							Padding(Space0, Space1)
						if btn.Hovered() {
							btn.Background(ThemeHover(c))
						}
						if btn.Clicked() && item.path != dir {
							s.loadFileManagerDir(fileManagerThreadID(s), item.path)
						}
						btn.Children(func() {
							ui.Text(c, item.label).
								FontSize(TypeFootnote).
								TextColor(ThemeSecondaryText(c)).
								SingleLine()
						})
					}
				})

			// 新建：菜单里放「新建文件 / 新建文件夹」，客户端不支持文件操作时不显示。
			if _, ok := fileOpsClient(s); ok {
				newBtn := ui.ButtonBase(c.Key("file-manager-new-btn")).
					Label("新建文件或文件夹").
					Tooltip("新建").
					Radius(RadiusSM).
					Padding(Space1, Space2)
				if newBtn.Hovered() {
					newBtn.Background(ThemeHover(c))
				}
				newBtn.Menu(fileManagerDirMenu(c, s))
				newBtn.Children(func() {
					ui.Icon(c, IconPlus()).
						Size(IconSM, IconSM).
						TextColor(ThemeSecondaryText(c))
				})
			}

			IconButton(c, IconButtonProps{
				Key: "file-manager-close-btn", Icon: IconX(), Label: "关闭文件面板",
				Tooltip: "关闭文件面板 (⇧⌘F)", Size: ControlXS, Variant: ButtonGhost,
				OnClick: func() { toggleFileManager(s) },
			})
		})
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

// renderFileManagerSearch 渲染搜索框：输入后防抖调用 SearchFiles，清空即回到目录列表。
func renderFileManagerSearch(c *ui.Context, s *chatState, query *string, threadID string) {
	SearchField(c, TextFieldProps{
		Key: "file-manager-search-input", Value: query, Label: "搜索文件", Placeholder: "搜索文件…",
		FillWidth: true, Clearable: true, ClearLabel: "清除搜索",
		OnChange: func(value string) { s.fileManagerSearch(threadID, value) },
	}).Margin(0, Space3, Space3, Space3)
}

// renderFileManagerEntries 渲染目录条目列表。
func renderFileManagerEntries(c *ui.Context, s *chatState, threadID string, entries []FileEntry) {
	ui.Column(c.Key("file-manager-entries")).
		Gap(Hairline).
		FillWidth().
		Children(func() {
			for _, entry := range entries {
				item := entry
				renderFileManagerRow(c, s, fileManagerRowProps{
					key:       "file-manager-entry-" + item.Path,
					icon:      fileManagerEntryIcon(item),
					name:      truncateMiddle(item.Name, 40),
					size:      fileManagerEntrySize(item),
					ignored:   item.Ignored,
					protected: item.Protected,
					symlink:   item.IsSymlink,
					isDir:     item.IsDir,
					threadID:  threadID,
					entry:     item,
					onOpen: func(pin bool) {
						if item.IsDir {
							s.loadFileManagerDir(threadID, item.Path)
							return
						}
						s.openFileManagerFile(threadID, item.Path, pin)
					},
					onMention: fileManagerMentionHandler(s, item.IsDir, item.Path),
				})
			}
		})
}

// fileManagerMentionHandler 返回「@引用」回调：只有文件行才提供引用，目录不参与。
func fileManagerMentionHandler(s *chatState, isDir bool, relPath string) func() {
	if isDir {
		return nil
	}
	return func() {
		appendFileReferenceToDraft(s, relPath)
	}
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
			ui.Text(c, "搜索结果已截断").FontSize(TypeFootnote).TextColor(ThemeWarning(c)).Margin(ui.Auto)
			return
		}
		EmptyState(c, EmptyStateProps{Title: "没有匹配的文件", Compact: true}).Margin(ui.Auto)
		return
	}

	threadID := fileManagerThreadID(s)
	ui.Column(c.Key("file-manager-search-results")).
		Gap(Hairline).
		FillWidth().
		Children(func() {
			for _, match := range results {
				item := match
				icon := IconFile()
				if item.IsDir {
					icon = IconFolder()
				}
				renderFileManagerRow(c, s, fileManagerRowProps{
					key:      "file-manager-search-item-" + item.Path,
					icon:     icon,
					name:     truncateMiddle(item.Path, 44),
					isDir:    item.IsDir,
					threadID: threadID,
					entry:    FileEntry{Name: item.Name, Path: item.Path, IsDir: item.IsDir},
					onOpen: func(pin bool) {
						if item.IsDir {
							s.loadFileManagerDir(threadID, item.Path)
							return
						}
						s.openFileManagerFile(threadID, item.Path, pin)
					},
					onMention: fileManagerMentionHandler(s, item.IsDir, item.Path),
				})
			}
			if truncated {
				ui.Text(c, "搜索结果已截断").
					FontSize(TypeFootnote).
					TextColor(ThemeWarning(c)).
					Padding(Space2, Space1, 0, Space1)
			}
		})
}

// fileManagerRowProps 是列表行需要的展示与回调字段。
type fileManagerRowProps struct {
	key       string
	icon      *ui.SVG
	name      string
	size      string
	ignored   bool
	protected bool
	symlink   bool
	isDir     bool
	// threadID 与 entry 供右键菜单使用：菜单里的打开、复制路径与写操作都按它们做。
	threadID string
	entry    FileEntry
	// onOpen 打开文件：pin 为 false 表示单击（临时预览），true 表示双击（固定 tab）。
	onOpen    func(pin bool)
	onMention func()
}

// renderFileManagerRow 渲染单行条目：图标、名称、可选大小、标签与「@引用」按钮。
// Protected 条目点击无效（行变灰、不绑定打开回调）。
func renderFileManagerRow(c *ui.Context, s *chatState, props fileManagerRowProps) {
	row := ui.Row(c.Key(props.key)).
		FillWidth().
		Padding(Space1, Space2).
		Radius(RadiusXS).
		Gap(Space2).
		AlignItems(ui.Center)
	if row.Hovered() && !props.protected {
		row.Background(ThemeHover(c))
	}

	nameColor := ThemePrimaryText(c)
	if props.ignored || props.protected {
		nameColor = ThemeSecondaryText(c)
	}

	// 点「@引用」不应同时打开文件：按钮先于行评估点击，用标记拦下行的打开。
	mentionClicked := false
	morePressed := false

	// 右键菜单与行尾「更多操作」按钮共用同一套菜单项。
	row.ContextMenu(fileManagerEntryMenu(c, s, props.threadID, props.entry))

	row.Children(func() {
		iconColor := ThemeSecondaryText(c)
		if props.isDir {
			iconColor = ThemeAccent(c)
		}
		ui.Icon(c, props.icon).Size(IconSM, IconSM).TextColor(iconColor).Shrink(0)

		ui.Text(c, props.name).
			FontSize(TypeFootnote).
			TextColor(nameColor).
			SingleLine().
			Grow(1).
			MinWidth(0)

		if props.symlink {
			renderFileManagerTag(c, props.key+"-symlink", "链接", ThemeSecondaryText(c))
		}
		if props.protected {
			renderFileManagerTag(c, props.key+"-protected", "受保护", ThemeWarning(c))
		}
		if props.size != "" {
			ui.Text(c, props.size).
				FontSize(TypeCaption).
				TextColor(ThemeSecondaryText(c)).
				Shrink(0)
		}

		if props.onMention != nil && !props.protected {
			mention := ui.ButtonBase(c.Key(props.key+"-mention")).
				Label("@引用 "+props.name).
				Tooltip("引用到输入框").
				Radius(RadiusXS).
				Padding(Hairline, Space1).
				Cursor(ui.CursorPointer)
			if mention.Hovered() {
				mention.Background(ThemeSelected(c))
			}
			if mention.Clicked() {
				mentionClicked = true
				props.onMention()
			}
			mention.Children(func() {
				ui.Text(c, "@引用").
					FontSize(TypeCaption).
					TextColor(ThemeSecondaryText(c))
			})
		}

		// 「更多操作」按钮：悬停时出现，按下即开菜单（与行右键同一个构建函数）。
		if row.Hovered() {
			more := ui.ButtonBase(c.Key(props.key+"-more")).
				Label("更多操作").
				Tooltip("更多操作").
				Radius(RadiusXS).
				Padding(Hairline, Space1).
				Cursor(ui.CursorPointer)
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
		}

		if !props.protected && !mentionClicked && !morePressed && props.onOpen != nil {
			// 双击固定预览，单击只开临时 tab；双击先于 Clicked 拦下。
			if row.DoubleClicked() {
				props.onOpen(true)
			} else if row.Clicked() {
				props.onOpen(false)
			}
		}
	})
}

// renderFileManagerTag 渲染行内小标签（链接 / 受保护）。
func renderFileManagerTag(c *ui.Context, key, label string, color ui.Color) {
	ui.Row(c.Key(key)).
		Padding(Hairline, Space1).
		Radius(RadiusXS).
		Background(color.Alpha(0.12)).
		Shrink(0).
		Children(func() {
			ui.Text(c, label).
				FontSize(TypeCaption).
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

// fileManagerEntrySize 返回条目右侧的大小文本：目录不显示大小。
func fileManagerEntrySize(entry FileEntry) string {
	if entry.IsDir {
		return ""
	}
	return formatFileSize(entry.Size)
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
				FontSize(TypeBody).
				FontWeight(500).
				TextColor(ThemePrimaryText(c))
			ui.Text(c, errMsg).
				FontSize(TypeFootnote).
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

// renderFileManagerFileHeader 渲染某一栏的文件头：文件名、路径与大小、Markdown 切换、「引用到输入框」。
// 返回目录与关闭面板按钮在标签栏里，两栏不再重复。keyPrefix 区分两栏的键。
func renderFileManagerFileHeader(c *ui.Context, s *chatState, tab fileManagerTabSnapshot, keyPrefix string) {
	fileName := fileManagerTabName(tab.path)

	ui.Row(c.Key(keyPrefix+"file-manager-file-header")).
		FillWidth().
		Height(fileManagerHeaderHeight).
		Padding(Space3, Space3).
		BorderWidth(0, 0, Hairline, 0).
		BorderColor(ThemeBorder(c)).
		AlignItems(ui.Center).
		Gap(Space2).
		Children(func() {
			ui.Column(c.Key(keyPrefix + "file-manager-file-title")).
				Grow(1).
				MinWidth(0).
				Gap(Hairline).
				Children(func() {
					ui.Text(c, fileName).
						FontSize(TypeFootnote).
						FontWeight(500).
						TextColor(ThemePrimaryText(c)).
						SingleLine().
						Tooltip(tab.path)
					detail := tab.path
					if tab.totalSize > 0 {
						detail = tab.path + " · " + formatFileSize(tab.totalSize)
					}
					ui.Text(c, detail).
						FontSize(TypeCaption).
						TextColor(ThemeSecondaryText(c)).
						SingleLine()
				})

			if isMarkdownPath(tab.path) {
				renderFileManagerMarkdownToggle(c, s, tab, keyPrefix)
			}

			// 窄面板下把按钮文案收紧为「引用」，保证按钮不被挤出可视区；无障碍名称不变。
			mentionText := "引用到输入框"
			if narrowFileManager(s) {
				mentionText = "引用"
			}

			Button(c, ButtonOpts{
				Key:     keyPrefix + "file-manager-file-mention",
				Text:    mentionText,
				Label:   "引用到输入框",
				Variant: ButtonSecondary,
				OnClick: func() {
					appendFileReferenceToDraft(s, tab.path)
				},
			})
		})
}

// narrowFileManager 判断面板是否处于窄宽度（文件头需要收紧文案）。
func narrowFileManager(s *chatState) bool {
	st := getFileManagerState(s)
	if st == nil {
		return false
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	return st.width < fileManagerCompactHeaderWidth
}

// fileManagerPreviewFontSize 返回文件预览正文的字号：跟随设置里的编辑器字号，每帧读取以便即时生效。
func fileManagerPreviewFontSize(s *chatState) float32 {
	return clampEditorFontSize(float32(currentWorkbenchConfig(s).EditorFontSize))
}

// renderFileManagerMarkdownToggle 渲染 Markdown 文件的「预览/源码」切换。
func renderFileManagerMarkdownToggle(c *ui.Context, s *chatState, tab fileManagerTabSnapshot, keyPrefix string) {
	ui.Row(c.Key(keyPrefix+"file-manager-md-toggle")).
		Radius(RadiusXS).
		Background(ThemeHover(c)).
		Padding(Hairline, Hairline).
		Shrink(0).
		Children(func() {
			renderFileManagerMdTab(c, s, tab, keyPrefix, "预览", true, tab.mdPreview)
			renderFileManagerMdTab(c, s, tab, keyPrefix, "源码", false, tab.mdPreview)
		})
}

// renderFileManagerMdTab 渲染 Markdown 预览/源码切换的单个页签；切换只写到本栏对应的 tab。
func renderFileManagerMdTab(c *ui.Context, s *chatState, tab fileManagerTabSnapshot, keyPrefix, label string, previewMode, current bool) {
	btn := ui.ButtonBase(c.Key(keyPrefix+"file-manager-md-tab-"+label)).
		Label(label).
		Radius(RadiusXS).
		Padding(Space0, Space2)
	if previewMode == current {
		btn.Background(ThemeSurface(c))
	} else if btn.Hovered() {
		btn.Background(ThemeHover(c))
	}
	if btn.Clicked() {
		s.setFileManagerTabMarkdownPreview(tab.path, previewMode)
	}
	btn.Children(func() {
		color := ThemeSecondaryText(c)
		if previewMode == current {
			color = ThemePrimaryText(c)
		}
		ui.Text(c, label).FontSize(TypeCaption).TextColor(color)
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
			ui.Text(c, "图片加载中…").
				FontSize(TypeFootnote).
				TextColor(ThemeSecondaryText(c)).
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
		ui.Text(c, "该文件为二进制格式，暂不支持预览").
			FontSize(TypeFootnote).
			TextColor(ThemeSecondaryText(c)).
			Margin(ui.Auto)
	default:
		mdPreview := tab.mdPreview && isMarkdownPath(tab.path)
		if tab.content == "" {
			ui.Text(c, "空文件").
				FontSize(TypeFootnote).
				TextColor(ThemeSecondaryText(c)).
				Margin(ui.Auto)
			return
		}
		if mdPreview {
			// Markdown 交给 markdown.View：表格、列表、代码块高亮都由它渲染，
			// 正文默认可选中复制。配置与子线程「工作汇报」共用，正文比代码字号大一档。
			markdown.View(c.Key(keyPrefix+"file-manager-md-view"), tab.content, swarmReportMarkdownOptions(c, s, fileManagerPreviewFontSize(s)+1)).
				FillWidth()
		} else {
			// 文本预览用只读代码编辑器：语法高亮、行号、可选中复制，滚动也由它管理。
			renderFileManagerCodePreview(c, s, tab, keyPrefix)
		}
		if tab.limitReached {
			ui.Text(c, "已达预览上限").
				FontSize(TypeFootnote).
				TextColor(ThemeWarning(c)).
				Padding(Space3, 0, 0, 0)
			return
		}
		if tab.hasMore {
			Button(c, ButtonOpts{
				Key:     keyPrefix + "file-manager-load-more",
				Text:    "加载更多",
				Variant: ButtonSecondary,
				OnClick: func() { s.loadMoreFileManagerTab(tab.path) },
			})
		}
	}
}
