package mcodeui

import (
	"context"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"
)

// 文件树与内容并排所需的最小右栏宽度，以及树导航的固定宽度。
// 窄于阈值时树与内容二选一，由返回按钮切换。
const (
	fileTreeSplitMinWidth float32 = 640
	fileTreeNavWidth      float32 = 200
)

// fileTreeState 是文件面板的树导航状态，只由 UI 线程读写；
// 字段的读写统一用 fileManagerMu 保护，后台加载结果不能在锁外回填。
type fileTreeState struct {
	outline   ui.OutlineState[string]
	selected  int
	selection ui.Selection[string]

	// children 按目录缓存排好序的子路径；nil 表示未加载，空切片表示空目录。
	children map[string][]string
	entries  map[string]FileEntry
	loading  map[string]bool
	stale    map[string]bool
	dirGen   map[string]int
	gen      int

	// 恢复偏好：展开路径在目录真正加载并确认存在后才写入 outline.Open。
	restoreExpanded map[string]bool
	restoreActive   string
	restoreAnchor   string

	// pendingOpen 是恢复时发现的活动文件，等下一帧由 UI 线程打开。
	pendingOpen string
	// anchor 是树当前首行路径，用于会话切换后恢复滚动位置（不保存像素）。
	anchor string

	rootTruncated bool
	// rowBuilds 只用于测试：一帧里 Outline 实际构建的行数。
	rowBuilds int

	// git 是路径到 Git 改动状态的缓存，只显示文件行，不汇总到目录。
	git         map[string]GitChangedFile
	gitLoaded   bool
	gitTried    bool
	gitTriedGen uint64
	gitSeq      int

	// onlyChanged 打开时用 Git 改动列表生成独立树；changedOutline 与完整树状态互不影响。
	onlyChanged      bool
	changedOutline   *ui.OutlineState[string]
	changedSig       string
	changedSelected  int
	changedSelection ui.Selection[string]
}

// ensureFileTreeLocked 初始化树状态；调用方持 fileManagerMu。
func ensureFileTreeLocked(st *fileManagerState) *fileTreeState {
	if st.tree != nil {
		return st.tree
	}
	tree := &fileTreeState{
		children:        make(map[string][]string),
		entries:         make(map[string]FileEntry),
		loading:         make(map[string]bool),
		stale:           make(map[string]bool),
		dirGen:          make(map[string]int),
		restoreExpanded: make(map[string]bool),
	}
	tree.outline.List.Selected = &tree.selected
	tree.outline.List.Selection = &tree.selection
	tree.outline.Indent = Space3
	st.tree = tree
	return tree
}

// fileTreeSeedPreference 把持久化偏好挂到树状态上。目录是否仍存在要等加载后确认，
// 所以这里只记录，不直接写 outline.Open。
func fileTreeSeedPreference(st *fileManagerState, pref InspectorPreference) {
	if st == nil {
		return
	}
	tree := ensureFileTreeLocked(st)
	if len(pref.FileExpanded) > 0 {
		tree.restoreExpanded = make(map[string]bool, len(pref.FileExpanded))
		for _, path := range pref.FileExpanded {
			tree.restoreExpanded[path] = true
		}
	}
	tree.restoreActive = pref.FileActive
	tree.restoreAnchor = pref.FileAnchor
	tree.anchor = pref.FileAnchor
}

// fileTreeAncestors 返回从根到 dir 的所有目录路径，根为空串。
func fileTreeAncestors(dir string) []string {
	dir = strings.Trim(strings.TrimSpace(dir), "/")
	if dir == "" {
		return []string{""}
	}
	parts := strings.Split(dir, "/")
	paths := make([]string, 0, len(parts)+1)
	paths = append(paths, "")
	current := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		paths = append(paths, current)
	}
	return paths
}

// fileTreeSortEntries 目录在前、文件在后，同类按名称排序，排序稳定。
func fileTreeSortEntries(entries []FileEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if an != bn {
			return an < bn
		}
		return a.Name < b.Name
	})
}

// fileTreeLoadDir 异步加载一个目录；同一目录正在加载时不重复请求，
// 回包按会话与目录代次校验，旧结果直接丢弃。
func (s *chatState) fileTreeLoadDir(threadID, dir string) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}

	fileManagerMu.Lock()
	tree := ensureFileTreeLocked(st)
	if threadID == "" || st.threadID != threadID {
		fileManagerMu.Unlock()
		return
	}
	if tree.loading[dir] || (tree.children[dir] != nil && !tree.stale[dir]) {
		fileManagerMu.Unlock()
		return
	}
	delete(tree.stale, dir)
	tree.dirGen[dir]++
	dirGen := tree.dirGen[dir]
	sessionGen := tree.gen
	tree.loading[dir] = true
	fileManagerMu.Unlock()

	go func(tid, sub string, gen int, dirRequest int) {
		listing, err := s.client.FilesList(context.Background(), tid, sub)

		fileManagerMu.Lock()
		if tree.gen != gen || st.threadID != tid || tree.dirGen[sub] != dirRequest {
			fileManagerMu.Unlock()
			return
		}
		delete(tree.loading, sub)
		if err != nil {
			if sub == "" {
				st.err = err.Error()
			} else {
				// 恢复偏好里的目录已不存在：静默丢弃展开状态。
				tree.outline.Open.Remove(sub)
				delete(tree.restoreExpanded, sub)
			}
			fileManagerMu.Unlock()
			s.invalidate()
			return
		}
		opened, openFile := fileTreeApplyListingLocked(st, tree, sub, listing)
		fileManagerMu.Unlock()

		for _, path := range opened {
			s.fileTreeLoadDir(tid, path)
		}
		if openFile != "" {
			s.invalidate()
		}
		s.invalidate()
	}(threadID, dir, sessionGen, dirGen)
}

// fileTreeApplyListingLocked 用新目录结果替换缓存，并返回需要继续加载的新展开目录。
// 旧子项按路径清理，展开、选中与滚动锚点只保留仍然存在的路径。
func fileTreeApplyListingLocked(st *fileManagerState, tree *fileTreeState, dir string, listing FileListing) (opened []string, openFile string) {
	previous := tree.children[dir]
	entries := make([]FileEntry, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		if entry.Path == "" {
			entry.Path = fileManagerJoinPath(dir, entry.Name)
		}
		entries = append(entries, entry)
	}
	fileTreeSortEntries(entries)

	kids := make([]string, 0, len(entries))
	next := make(map[string]bool, len(entries))
	for _, entry := range entries {
		kids = append(kids, entry.Path)
		next[entry.Path] = true
		tree.entries[entry.Path] = entry
		if entry.IsDir {
			if tree.restoreExpanded[entry.Path] {
				tree.outline.Open.Add(entry.Path)
				delete(tree.restoreExpanded, entry.Path)
			}
			if tree.outline.Open.Has(entry.Path) && tree.children[entry.Path] == nil && !tree.loading[entry.Path] {
				opened = append(opened, entry.Path)
			}
		} else if tree.restoreActive == entry.Path {
			tree.restoreActive = ""
			tree.pendingOpen = entry.Path
			openFile = entry.Path
		}
	}

	for _, old := range previous {
		if next[old] {
			continue
		}
		fileTreeForgetLocked(st, tree, old)
	}
	// 恢复偏好里属于本目录、但已经不存在的直接子项直接丢弃。
	for path := range tree.restoreExpanded {
		if fileManagerParentPath(path) == dir && !next[path] {
			delete(tree.restoreExpanded, path)
		}
	}

	tree.children[dir] = kids
	if dir == "" {
		tree.rootTruncated = listing.Truncated
		st.err = ""
	}
	return opened, openFile
}

// fileTreeForgetLocked 从树状态中移除 path 及其子树。调用方持 fileManagerMu。
func fileTreeForgetLocked(st *fileManagerState, tree *fileTreeState, path string) {
	if path == "" {
		return
	}
	prefix := path + "/"
	for key := range tree.entries {
		if key == path || strings.HasPrefix(key, prefix) {
			delete(tree.entries, key)
		}
	}
	for key := range tree.children {
		if key == path || strings.HasPrefix(key, prefix) {
			delete(tree.children, key)
		}
	}
	for key := range tree.loading {
		if key == path || strings.HasPrefix(key, prefix) {
			delete(tree.loading, key)
			tree.dirGen[key]++
		}
	}
	var open []string
	for key := range tree.outline.Open.All() {
		if key == path || strings.HasPrefix(key, prefix) {
			open = append(open, key)
		}
	}
	for _, key := range open {
		tree.outline.Open.Remove(key)
	}
	var chosen []string
	for key := range tree.selection.All() {
		if key == path || strings.HasPrefix(key, prefix) {
			chosen = append(chosen, key)
		}
	}
	for _, key := range chosen {
		tree.selection.Remove(key)
	}
	if tree.anchor == path || strings.HasPrefix(tree.anchor, prefix) {
		tree.anchor = ""
	}
	if tree.restoreActive == path || strings.HasPrefix(tree.restoreActive, prefix) {
		tree.restoreActive = ""
	}
	if tree.pendingOpen == path || strings.HasPrefix(tree.pendingOpen, prefix) {
		tree.pendingOpen = ""
	}
	if tree.restoreAnchor == path || strings.HasPrefix(tree.restoreAnchor, prefix) {
		tree.restoreAnchor = ""
	}
}

// fileTreeRenamePaths 在重命名目录后同步树里的展开、选中与锚点路径。
// 子项缓存直接丢弃，由父目录刷新按新路径重新加载。
func (s *chatState) fileTreeRenamePaths(oldPath, newPath string) {
	if oldPath == "" || oldPath == newPath {
		return
	}
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	rename := func(p string) string {
		if p == oldPath {
			return newPath
		}
		if strings.HasPrefix(p, oldPath+"/") {
			return newPath + p[len(oldPath):]
		}
		return p
	}
	fileManagerMu.Lock()
	tree := ensureFileTreeLocked(st)
	var open []string
	for key := range tree.outline.Open.All() {
		if key == oldPath || strings.HasPrefix(key, oldPath+"/") {
			open = append(open, key)
		}
	}
	for _, key := range open {
		tree.outline.Open.Remove(key)
		tree.outline.Open.Add(rename(key))
	}
	var chosen []string
	for key := range tree.selection.All() {
		if key == oldPath || strings.HasPrefix(key, oldPath+"/") {
			chosen = append(chosen, key)
		}
	}
	for _, key := range chosen {
		tree.selection.Remove(key)
		tree.selection.Add(rename(key))
	}
	anchor := rename(tree.anchor)
	restoreActive := rename(tree.restoreActive)
	pendingOpen := rename(tree.pendingOpen)
	restoreAnchor := rename(tree.restoreAnchor)
	restoreExpanded := make(map[string]bool, len(tree.restoreExpanded))
	for path := range tree.restoreExpanded {
		restoreExpanded[rename(path)] = true
	}
	fileTreeForgetLocked(st, tree, oldPath)
	if tree.anchor == "" {
		tree.anchor = anchor
	}
	tree.restoreActive = restoreActive
	tree.pendingOpen = pendingOpen
	tree.restoreAnchor = restoreAnchor
	if len(restoreExpanded) > 0 {
		tree.restoreExpanded = restoreExpanded
	}
	fileManagerMu.Unlock()
}

// fileTreeTrashPath 移除被移到废纸篓的路径及其缓存与锚点。
func (s *chatState) fileTreeTrashPath(path string) {
	if path == "" {
		return
	}
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	tree := ensureFileTreeLocked(st)
	fileTreeForgetLocked(st, tree, path)
	if st.dir == path || strings.HasPrefix(st.dir, path+"/") {
		st.dir = fileManagerParentPath(path)
	}
	fileManagerMu.Unlock()
}

// fileTreeInvalidate 是 P1.2 监听刷新的接入点：把指定目录标记为过期并重新加载，
// 空切片表示全部已加载目录。旧 children 在新结果到达前继续显示，
// 展开状态、选中项与滚动位置都不重置。
func fileTreeInvalidate(s *chatState, threadID string, dirs []string) {
	if s == nil {
		return
	}
	st := getFileManagerState(s)
	if st == nil {
		return
	}

	fileManagerMu.Lock()
	tree := ensureFileTreeLocked(st)
	if threadID == "" {
		threadID = st.threadID
	}
	if threadID == "" || st.threadID != threadID {
		fileManagerMu.Unlock()
		return
	}
	var targets []string
	if len(dirs) == 0 {
		targets = append(targets, "")
		for dir := range tree.children {
			if dir != "" {
				targets = append(targets, dir)
			}
		}
	} else {
		seen := make(map[string]bool, len(dirs))
		for _, dir := range dirs {
			dir = strings.Trim(strings.TrimSpace(dir), "/")
			if seen[dir] {
				continue
			}
			seen[dir] = true
			targets = append(targets, dir)
		}
	}
	// 让在途的旧请求失效，并允许这次重新请求同一个目录。
	for _, dir := range targets {
		tree.dirGen[dir]++
		delete(tree.loading, dir)
		tree.stale[dir] = true
		if dir == "" {
			st.err = ""
		}
	}
	sort.Strings(targets)
	fileManagerMu.Unlock()

	for _, dir := range targets {
		s.fileTreeLoadDir(threadID, dir)
	}
	s.invalidate()
}

// fileTreeDirsToLoadLocked 返回当前需要懒加载的目录：根目录、已展开但未加载的目录。
// 根目录出错后等用户重试，避免每帧重复请求。
func fileTreeDirsToLoadLocked(st *fileManagerState) []string {
	tree := st.tree
	var dirs []string
	if tree.children[""] == nil && !tree.loading[""] && st.err == "" {
		dirs = append(dirs, "")
	}
	for path := range tree.outline.Open.All() {
		if tree.children[path] == nil && !tree.loading[path] {
			dirs = append(dirs, path)
		}
	}
	sort.Strings(dirs)
	return dirs
}

// fileTreeRowIndexLocked 返回路径在当前可见行里的下标。
func fileTreeRowIndexLocked(tree *fileTreeState, path string) int {
	outline := fileTreeActiveOutlineLocked(tree)
	for i := 0; i < outline.Rows(); i++ {
		if outline.Item(i) == path {
			return i
		}
	}
	return -1
}

// fileTreeSelect 更新树的单选/多选，不改展开状态与滚动位置。
func (s *chatState) fileTreeSelect(path string, mods ui.Modifiers) {
	inspectorFollowPause(s, "")
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	tree := ensureFileTreeLocked(st)
	idx := fileTreeRowIndexLocked(tree, path)
	if idx < 0 {
		fileManagerMu.Unlock()
		return
	}
	selected := &tree.selected
	selection := &tree.selection
	outline := &tree.outline
	if tree.onlyChanged && tree.changedOutline != nil {
		selected = &tree.changedSelected
		selection = &tree.changedSelection
		outline = tree.changedOutline
	}
	switch {
	case mods&ui.Shift != 0 && *selected >= 0:
		lo, hi := min(*selected, idx), max(*selected, idx)
		selection.Clear()
		for i := lo; i <= hi; i++ {
			selection.Add(outline.Item(i))
		}
	case mods&ui.Cmd != 0:
		if selection.Has(path) {
			selection.Remove(path)
		} else {
			selection.Add(path)
		}
	default:
		selection.Clear()
		selection.Add(path)
	}
	*selected = idx
	fileManagerMu.Unlock()
}

// fileTreeToggleDir 展开或收起一个目录；展开时异步加载子项。
func (s *chatState) fileTreeToggleDir(threadID, path string) {
	inspectorFollowPause(s, threadID)
	st := getFileManagerState(s)
	if st == nil || path == "" {
		return
	}
	fileManagerMu.Lock()
	tree := ensureFileTreeLocked(st)
	outline := fileTreeActiveOutlineLocked(tree)
	open := false
	if outline.Open.Has(path) {
		outline.Open.Remove(path)
	} else {
		outline.Open.Add(path)
		open = true
	}
	st.dir = path
	needLoad := !tree.onlyChanged && open && tree.children[path] == nil && !tree.loading[path]
	onlyChanged := tree.onlyChanged
	fileManagerMu.Unlock()

	if needLoad {
		s.fileTreeLoadDir(threadID, path)
	}
	if !onlyChanged {
		inspectorRemember(s)
	}
	s.invalidate()
}

// fileTreeRevealDir 清空搜索并在树里展开目标目录及其祖先。
func (s *chatState) fileTreeRevealDir(threadID, dir string) {
	inspectorFollowPause(s, threadID)
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	dir = strings.Trim(strings.TrimSpace(dir), "/")
	fileManagerMu.Lock()
	tree := ensureFileTreeLocked(st)
	ancestors := fileTreeAncestors(dir)
	for _, path := range ancestors {
		if path != "" {
			tree.outline.Open.Add(path)
		}
	}
	st.dir = dir
	st.showList = true
	fileManagerMu.Unlock()

	s.fileManagerSearch(threadID, "")
	for _, path := range ancestors {
		s.fileTreeLoadDir(threadID, path)
	}
	inspectorRemember(s)
	s.invalidate()
}

// fileTreeClickEntry 是树行单击/双击的统一入口：先更新选择，再展开目录或打开文件。
func (s *chatState) fileTreeClickEntry(threadID string, entry FileEntry, pin bool, mods ui.Modifiers) {
	if entry.Protected {
		return
	}
	s.fileTreeSelect(entry.Path, mods)
	if entry.IsDir {
		// 双击目录时第一击已经切换过展开状态，第二击不再反向切换。
		if !pin {
			s.fileTreeToggleDir(threadID, entry.Path)
		}
		return
	}
	s.openFileManagerFile(threadID, entry.Path, pin)
}

// fileTreeOpenSnapshot 复制当前展开集合，用于检测 Outline 自己处理的箭头与键盘操作。
func fileTreeOpenSnapshot(tree *fileTreeState) map[string]struct{} {
	result := make(map[string]struct{}, tree.outline.Open.Len())
	for path := range tree.outline.Open.All() {
		result[path] = struct{}{}
	}
	return result
}

// fileTreeOpenChanged 报告此次 Outline 构建后展开集合是否变化，并返回新展开的目录。
func fileTreeOpenChanged(before map[string]struct{}, tree *fileTreeState) (bool, []string) {
	changed := len(before) != tree.outline.Open.Len()
	var opened []string
	for path := range tree.outline.Open.All() {
		if _, ok := before[path]; !ok {
			changed = true
			opened = append(opened, path)
		}
	}
	if !changed {
		for path := range before {
			if !tree.outline.Open.Has(path) {
				changed = true
				break
			}
		}
	}
	sort.Strings(opened)
	return changed, opened
}

// fileTreePreferenceLocked 汇总需要落盘的树状态：展开路径、活动文件与滚动锚点。
// 还没加载的恢复展开路径也一并保留，避免首次切换会话时被空集合覆盖。
func fileTreePreferenceLocked(st *fileManagerState) (expanded []string, active, anchor string) {
	if st == nil {
		return nil, "", ""
	}
	active = st.active
	if st.tree == nil {
		return nil, active, ""
	}
	anchor = st.tree.restoreAnchor
	seen := make(map[string]bool, st.tree.outline.Open.Len()+len(st.tree.restoreExpanded))
	for path := range st.tree.outline.Open.All() {
		seen[path] = true
	}
	for path := range st.tree.restoreExpanded {
		seen[path] = true
	}
	if active == "" {
		active = st.tree.restoreActive
	}
	if st.tree.anchor != "" {
		anchor = st.tree.anchor
	}
	if len(seen) > 0 {
		expanded = make([]string, 0, len(seen))
		for path := range seen {
			expanded = append(expanded, path)
		}
	}
	return expanded, active, anchor
}

// renderFileManagerNavigation 渲染树导航：顶部搜索与新建，下面按模式显示树或搜索结果。
func renderFileManagerNavigation(c *ui.Context, s *chatState, threadID, workDir string) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	queryPtr := &st.query
	query := st.query
	searching := st.searching
	searchResults := append([]FileMatch(nil), st.searchResults...)
	searchTruncated := st.searchTruncated
	searchErr := st.searchErr
	rootErr := st.err
	rootLoading := false
	rootTruncated := false
	rootCount := 0
	onlyChanged := false
	gitLoaded := false
	gitTried := false
	if st.tree != nil {
		rootLoading = st.tree.loading[""]
		rootTruncated = st.tree.rootTruncated
		rootCount = len(st.tree.children[""])
		onlyChanged = st.tree.onlyChanged
		gitLoaded = st.tree.gitLoaded
		gitTried = st.tree.gitTried
		if onlyChanged {
			rootCount = fileTreeChangedRootCountLocked(st.tree)
		}
	}
	fileManagerMu.Unlock()

	ui.Column(c.Key("file-manager-nav")).
		FillWidth().
		FillHeight().
		Children(func() {
			renderFileManagerTreeHeader(c, s, queryPtr, threadID)

			if workDir == "" {
				EmptyState(c, EmptyStateProps{Icon: IconFolder(), Title: "未选择工作区"}).Margin(ui.Auto)
				return
			}
			if query != "" {
				ui.Scroll(c.Key("file-manager-search-scroll")).
					Grow(1).
					FillWidth().
					Padding(Space3, Space3).
					Children(func() {
						renderFileManagerSearchResults(c, s, searching, searchErr, searchResults, searchTruncated)
					})
				return
			}

			switch {
			case onlyChanged && rootCount == 0 && !gitLoaded && !gitTried:
				LoadingIndicator(c, "file-manager-only-changed-loading", "正在读取改动…", IconSM).Margin(ui.Auto)
			case onlyChanged && rootCount == 0:
				EmptyState(c, EmptyStateProps{Icon: IconFileDiff(), Title: "没有改动", Compact: true}).Margin(ui.Auto)
			case !onlyChanged && rootErr != "" && rootCount == 0:
				renderFileManagerError(c, s, "", rootErr, func() {
					fileTreeInvalidate(s, threadID, []string{""})
				})
			case !onlyChanged && rootLoading && rootCount == 0:
				LoadingIndicator(c, "file-manager-loading", "正在读取目录…", IconSM).Margin(ui.Auto)
			case rootCount == 0:
				EmptyState(c, EmptyStateProps{Icon: IconFolder(), Title: "空目录", Compact: true}).Margin(ui.Auto)
			default:
				renderFileManagerTree(c, s, threadID)
			}
			if !onlyChanged && rootTruncated && rootErr == "" {
				ui.Text(c, "目录条目已截断（仅显示前 1000 个条目）").
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeWarning(c)).
					Padding(Space2, Space3, Space3, Space3)
			}
		})
}

// renderFileManagerTreeHeader 渲染导航顶部：搜索框与新建入口。
func renderFileManagerTreeHeader(c *ui.Context, s *chatState, query *string, threadID string) {
	ui.Row(c.Key("file-manager-tree-header")).
		FillWidth().
		Height(fileManagerHeaderHeight).
		Padding(Space2, Space3).
		BorderWidth(0, 0, Hairline, 0).
		BorderColor(ThemeBorder(c)).
		AlignItems(ui.Center).
		Gap(Space1).
		Children(func() {
			SearchField(c, TextFieldProps{
				Key: "file-manager-search-input", Value: query, Label: "搜索文件", Placeholder: "搜索文件…",
				Grow: true, Clearable: true, ClearLabel: "清除搜索",
				OnChange: func(value string) { s.fileManagerSearch(threadID, value) },
			})
			if _, ok := fileOpsClient(s); ok {
				newBtn := ui.ButtonBase(c.Key("file-manager-new-btn")).
					Label("新建文件或文件夹").
					Tooltip("新建").
					Size(ControlSM, ControlSM).
					Radius(RadiusSM).
					Center().
					Shrink(0)
				if newBtn.Hovered() {
					newBtn.Background(ThemeHover(c))
				}
				newBtn.Menu(fileManagerDirMenu(c, s))
				newBtn.Children(func() {
					ui.Icon(c, IconPlus()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
				})
			}
			if threadID != "" {
				onlyChanged := fileTreeOnlyChangedActive(getFileManagerState(s))
				onlyBtn := ui.ButtonBase(c.Key("file-manager-only-changed"))
				onlyBtn.Label("只看改动").
					Tooltip("只看改动").
					Size(ControlSM, ControlSM).
					Radius(RadiusSM).
					Center().
					Shrink(0)
				if onlyChanged {
					onlyBtn.Background(ThemeSelected(c))
				} else if onlyBtn.Hovered() {
					onlyBtn.Background(ThemeHover(c))
				}
				if onlyBtn.Clicked() {
					s.fileTreeToggleOnlyChanged(threadID)
				}
				onlyBtn.Children(func() {
					color := ThemeSecondaryText(c)
					if onlyChanged {
						color = ThemeAccent(c)
					}
					ui.Icon(c, IconFileDiff()).Size(IconSM, IconSM).TextColor(color)
				})
			}
		})
}

// renderFileManagerTree 渲染虚拟文件树。children 回调只读缓存，目录加载在构建前触发。
func renderFileManagerTree(c *ui.Context, s *chatState, threadID string) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}

	fileManagerMu.Lock()
	tree := ensureFileTreeLocked(st)
	onlyChanged := tree.onlyChanged
	var outline *ui.OutlineState[string]
	var loads []string
	var roots []string
	var changedChildren map[string][]string
	var changedEntries map[string]FileEntry
	var before map[string]struct{}
	if onlyChanged {
		roots, changedChildren, changedEntries = fileTreeChangedRowsLocked(tree)
		outline = fileTreeChangedOutlineLocked(tree)
	} else {
		loads = fileTreeDirsToLoadLocked(st)
		roots = append([]string(nil), tree.children[""]...)
		before = fileTreeOpenSnapshot(tree)
		outline = &tree.outline
	}
	tree.rowBuilds = 0
	fileManagerMu.Unlock()

	for _, dir := range loads {
		s.fileTreeLoadDir(threadID, dir)
	}

	children := func(path string) []string {
		if onlyChanged {
			return changedChildren[path]
		}
		fileManagerMu.Lock()
		kids := tree.children[path]
		fileManagerMu.Unlock()
		return kids
	}

	// 自定义前缀：箭头用 springSnappy 旋转；树只构建视口内的行。
	outline.Prefix = func(r ui.OutlineRow) {
		if r.Depth > 0 {
			indent := float32(r.Depth) * Space3
			ui.Box(c).Width(indent).Shrink(0)
		}
		arrow := r.Arrow().Size(IconSM, IconSM).Shrink(0).Center()
		if r.Branch {
			angle := float32(0)
			if r.Open {
				angle = 90
			}
			arrow.Children(func() {
				icon := ui.Icon(c, IconChevronRight()).
					Size(IconSM, IconSM).
					TextColor(ThemeSecondaryText(c))
				if reduceMotion(c) {
					icon.Rotate(angle)
				} else {
					icon.Rotate(icon.AnimateTo("file-tree-arrow", angle, springSnappy))
				}
			})
		}
		ui.Box(c).Width(Space1).Shrink(0)
	}

	ui.Outline(c.Key("file-manager-tree"), outline, roots, children, func(path string) {
		fileManagerMu.Lock()
		entry, ok := tree.entries[path]
		if onlyChanged {
			entry, ok = changedEntries[path]
		}
		status, hasStatus := fileTreeStatusLocked(tree, path, entry.IsDir)
		deleted := onlyChanged && hasStatus && status.Status == "D"
		tree.rowBuilds++
		fileManagerMu.Unlock()
		if !ok {
			return
		}
		props := fileManagerRowProps{
			key:       "file-tree-row-" + path,
			icon:      fileManagerEntryIcon(entry),
			name:      truncateMiddle(entry.Name, 40),
			size:      fileManagerEntrySize(entry),
			ignored:   entry.Ignored || deleted,
			protected: entry.Protected,
			symlink:   entry.IsSymlink,
			isDir:     entry.IsDir,
			threadID:  threadID,
			entry:     entry,
			onOpen: func(pin bool, mods ui.Modifiers) {
				s.fileTreeClickEntry(threadID, entry, pin, mods)
			},
			onMention: fileManagerMentionHandler(s, entry.IsDir, entry.Path),
		}
		if onlyChanged {
			props.size = ""
		}
		if deleted {
			props.onOpen = nil
			props.onMention = nil
		}
		row := ui.Row(c.Key("file-tree-content-" + path)).
			Grow(1).
			MinWidth(0).
			AlignItems(ui.Center).
			Gap(Space1).
			Transition(motionListItem(c))
		if payload, ok := fileTreeDragReference(s, threadID, entry); ok && !deleted {
			row.Drag(payload)
		}
		row.Children(func() {
			renderFileManagerRow(c, s, props)
			if hasStatus {
				renderFileTreeGitTag(c, "file-tree-git-"+path, status)
			}
		})
	}).Grow(1).FillWidth().Padding(Space2, Space2).ContextMenu(fileManagerDirMenu(c, s))

	if onlyChanged {
		return
	}

	// 箭头与键盘操作由 Outline 内部处理，构建后比较展开集合即可接上懒加载与落盘。
	fileManagerMu.Lock()
	changed, opened := fileTreeOpenChanged(before, tree)
	if len(opened) > 0 {
		st.dir = opened[len(opened)-1]
	}
	var needLoad []string
	for _, dir := range opened {
		if tree.children[dir] == nil && !tree.loading[dir] {
			needLoad = append(needLoad, dir)
		}
	}
	if outline.Rows() > 0 {
		if first, _ := outline.List.Visible(); first >= 0 && first < outline.Rows() {
			tree.anchor = outline.Item(first)
		}
	}
	if tree.restoreAnchor != "" {
		if idx := fileTreeRowIndexLocked(tree, tree.restoreAnchor); idx >= 0 {
			outline.List.ScrollTo(idx, ui.Start)
			tree.restoreAnchor = ""
		} else if tree.children[""] != nil && len(tree.loading) == 0 {
			tree.restoreAnchor = ""
		}
	}
	pendingOpen := tree.pendingOpen
	tree.pendingOpen = ""
	fileManagerMu.Unlock()

	for _, dir := range needLoad {
		s.fileTreeLoadDir(threadID, dir)
	}
	if changed {
		inspectorFollowPause(s, threadID)
		inspectorRemember(s)
	}
	if pendingOpen != "" {
		s.openFileManagerFile(threadID, pendingOpen, true)
	}
}
