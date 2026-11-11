package mcodeui

import (
	"context"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	// fileTreeWatchInterval 是可见文件面板检查工作区代次的最小间隔。
	fileTreeWatchInterval = time.Second
	// fileTreeWatchTimeout 限制一次代次与 Git 改动查询，避免慢盘拖住在途检查。
	fileTreeWatchTimeout = 2 * time.Second
)

// workspaceGenerationClient 是可选能力：支持工作区代次查询的 client 才启用监听刷新。
type workspaceGenerationClient interface {
	WorkspaceGeneration(ctx context.Context, threadID string) (uint64, error)
}

// fileTreeWatchState 保存单个文件面板的监听刷新状态，按 fileManagerState 挂在包级 map 里。
// 计时器与在途请求在会话释放、切换会话或切换工作区时统一取消。
type fileTreeWatchState struct {
	mu         sync.Mutex
	threadID   string
	sessionGen int
	visible    bool
	disposed   bool

	lastCheck time.Time
	lastGen   uint64
	hasGen    bool

	inFlight bool
	seq      int
	cancel   context.CancelFunc
	timer    *time.Timer
	timerAt  time.Time
}

var (
	fileTreeWatchMu sync.Mutex
	fileTreeWatches = make(map[*fileManagerState]*fileTreeWatchState)
)

// fileTreeWatchFor 返回文件面板的监听状态，没有时创建。
func fileTreeWatchFor(st *fileManagerState) *fileTreeWatchState {
	if st == nil {
		return nil
	}
	fileTreeWatchMu.Lock()
	defer fileTreeWatchMu.Unlock()
	if w := fileTreeWatches[st]; w != nil {
		return w
	}
	w := &fileTreeWatchState{}
	fileTreeWatches[st] = w
	return w
}

// fileTreeWatchLookup 只查找已有的监听状态，不创建。
func fileTreeWatchLookup(st *fileManagerState) *fileTreeWatchState {
	if st == nil {
		return nil
	}
	fileTreeWatchMu.Lock()
	defer fileTreeWatchMu.Unlock()
	return fileTreeWatches[st]
}

// fileTreeWatchSetVisible 在右栏打开或关闭、切换标签时调用。
// 重新可见时清空上次检查时间，下一帧立即补检一次。
func fileTreeWatchSetVisible(st *fileManagerState, visible bool) {
	w := fileTreeWatchLookup(st)
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.disposed || w.visible == visible {
		return
	}
	w.visible = visible
	if visible {
		w.lastCheck = time.Time{}
		return
	}
	w.cancelLocked()
	w.stopTimerLocked()
}

// fileTreeWatchDispose 取消在途请求与计时器，并移除该面板的监听状态。
func fileTreeWatchDispose(st *fileManagerState) {
	if st == nil {
		return
	}
	fileTreeWatchMu.Lock()
	w := fileTreeWatches[st]
	delete(fileTreeWatches, st)
	fileTreeWatchMu.Unlock()
	if w == nil {
		return
	}
	w.mu.Lock()
	w.disposed = true
	w.visible = false
	w.cancelLocked()
	w.stopTimerLocked()
	w.mu.Unlock()
}

// fileTreeWatchTick 由文件面板每帧调用：只在可见时按间隔检查工作区代次。
func fileTreeWatchTick(s *chatState, st *fileManagerState, threadID string) {
	w := fileTreeWatchFor(st)
	fileTreeWatchTickState(s, st, threadID, w)
}

// fileTreeWatchTickExisting 只推进已经存在的监听状态；计时器回调使用，
// 避免会话释放后又为旧指针重建计时器。
func fileTreeWatchTickExisting(s *chatState, st *fileManagerState, threadID string) {
	w := fileTreeWatchLookup(st)
	if w == nil {
		return
	}
	fileTreeWatchTickState(s, st, threadID, w)
}

func fileTreeWatchTickState(s *chatState, st *fileManagerState, threadID string, w *fileTreeWatchState) {
	if s == nil || st == nil || threadID == "" || w == nil {
		return
	}
	client, ok := s.client.(workspaceGenerationClient)
	if !ok {
		return
	}

	fileManagerMu.Lock()
	sessionGen := st.gen
	if st.threadID != threadID {
		fileManagerMu.Unlock()
		return
	}
	fileManagerMu.Unlock()

	now := time.Now()
	w.mu.Lock()
	if w.disposed {
		w.mu.Unlock()
		return
	}
	if w.threadID != threadID || w.sessionGen != sessionGen {
		w.cancelLocked()
		w.stopTimerLocked()
		w.threadID = threadID
		w.sessionGen = sessionGen
		w.lastCheck = time.Time{}
		w.hasGen = false
		w.lastGen = 0
	}
	// 渲染只发生在可见的文件面板里；这里同时作为重新可见的兜底。
	if !w.visible {
		w.visible = true
		w.lastCheck = time.Time{}
	}
	if w.inFlight {
		w.scheduleTimerLocked(s, st, threadID, fileTreeWatchInterval)
		w.mu.Unlock()
		return
	}
	if !w.lastCheck.IsZero() {
		if wait := fileTreeWatchInterval - now.Sub(w.lastCheck); wait > 0 {
			w.scheduleTimerLocked(s, st, threadID, wait)
			w.mu.Unlock()
			return
		}
	}
	w.seq++
	seq := w.seq
	w.inFlight = true
	w.lastCheck = now
	ctx, cancel := context.WithTimeout(context.Background(), fileTreeWatchTimeout)
	w.cancel = cancel
	w.scheduleTimerLocked(s, st, threadID, fileTreeWatchInterval)
	w.mu.Unlock()

	go fileTreeWatchCheck(s, st, client, threadID, sessionGen, seq, ctx, cancel)
}

// fileTreeWatchCheck 在后台取一次代次，变化时刷新目录、内容与 Git 标记。
func fileTreeWatchCheck(s *chatState, st *fileManagerState, client workspaceGenerationClient, threadID string, sessionGen, seq int, ctx context.Context, cancel context.CancelFunc) {
	defer cancel()

	generation, err := client.WorkspaceGeneration(ctx, threadID)
	if err != nil {
		fileTreeWatchFinish(st, seq)
		return
	}

	fileManagerMu.Lock()
	if st.gen != sessionGen || st.threadID != threadID {
		fileManagerMu.Unlock()
		fileTreeWatchFinish(st, seq)
		return
	}
	w := fileTreeWatchLookup(st)
	if w == nil {
		fileManagerMu.Unlock()
		fileTreeWatchFinish(st, seq)
		return
	}
	tree := ensureFileTreeLocked(st)
	w.mu.Lock()
	valid := !w.disposed && w.seq == seq
	first := !w.hasGen
	changed := !first && w.lastGen != generation
	needGit := !tree.gitLoaded && (!tree.gitTried || tree.gitTriedGen != generation)
	w.mu.Unlock()
	if !valid {
		fileManagerMu.Unlock()
		return
	}
	fileManagerMu.Unlock()

	var files []GitChangedFile
	var gitErr error
	gitSeq := 0
	if needGit {
		fileManagerMu.Lock()
		gitSeq = fileTreeGitBeginLocked(tree)
		fileManagerMu.Unlock()
		files, gitErr = s.client.GitChangedFiles(ctx, threadID)
	}

	fileManagerMu.Lock()
	w = fileTreeWatchLookup(st)
	if w == nil {
		fileManagerMu.Unlock()
		fileTreeWatchFinish(st, seq)
		return
	}
	w.mu.Lock()
	valid = !w.disposed && w.seq == seq
	w.mu.Unlock()
	if !valid || st.gen != sessionGen || st.threadID != threadID {
		fileManagerMu.Unlock()
		return
	}
	if needGit && tree.gitSeq == gitSeq {
		if gitErr == nil {
			fileTreeApplyGitLocked(tree, files)
			tree.gitLoaded = true
		}
		tree.gitTried = true
		tree.gitTriedGen = generation
	}
	w.mu.Lock()
	if w.hasGen && w.lastGen != generation {
		changed = true
	}
	w.lastGen = generation
	w.hasGen = true
	w.mu.Unlock()

	var refreshes []fileManagerTabRefresh
	if changed {
		for _, tab := range st.tabs {
			tab.gen++
			tab.fileLoading = true
			tab.fileErr = ""
			refreshes = append(refreshes, fileManagerTabRefresh{tab: tab, path: tab.path, gen: tab.gen})
		}
	}
	fileManagerMu.Unlock()

	if changed {
		fileTreeInvalidate(s, threadID, nil)
	}
	for _, refresh := range refreshes {
		s.readFileManagerTab(st, refresh.tab, threadID, refresh.path, refresh.gen)
	}
	fileTreeWatchFinish(st, seq)
	if changed || needGit {
		s.invalidate()
	}
}

// fileTreeWatchFinish 结束一次在途检查；被取消或替换的请求不碰新状态。
func fileTreeWatchFinish(st *fileManagerState, seq int) {
	w := fileTreeWatchLookup(st)
	if w == nil {
		return
	}
	w.mu.Lock()
	if w.seq == seq {
		w.inFlight = false
		w.cancel = nil
	}
	w.mu.Unlock()
}

// fileManagerTabRefresh 记录一次内容标签刷新所需的代数。
type fileManagerTabRefresh struct {
	tab  *fileManagerTab
	path string
	gen  int
}

// cancelLocked 取消在途请求；seq 递增让旧回包失效。
func (w *fileTreeWatchState) cancelLocked() {
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	if w.inFlight {
		w.inFlight = false
		w.seq++
	}
}

// stopTimerLocked 停止定时重绘；调用方持 w.mu。
func (w *fileTreeWatchState) stopTimerLocked() {
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.timerAt = time.Time{}
}

// scheduleTimerLocked 在 wait 后触发一次检查与重绘；已有更早的计时器时不重复安排。
func (w *fileTreeWatchState) scheduleTimerLocked(s *chatState, st *fileManagerState, threadID string, wait time.Duration) {
	if w.disposed || !w.visible {
		return
	}
	if wait <= 0 {
		wait = time.Millisecond
	}
	if w.timer != nil && time.Until(w.timerAt) <= wait {
		return
	}
	w.stopTimerLocked()
	var timer *time.Timer
	timer = time.AfterFunc(wait, func() {
		w.mu.Lock()
		if w.timer != timer || w.disposed || !w.visible {
			w.mu.Unlock()
			return
		}
		w.timer = nil
		w.timerAt = time.Time{}
		w.mu.Unlock()
		fileTreeWatchTickExisting(s, st, threadID)
		s.invalidate()
	})
	w.timer = timer
	w.timerAt = time.Now().Add(wait)
}

// fileTreeRefreshGit 在处理完 Git 动作后主动刷新改动列表，不等下一次监听检查。
func fileTreeRefreshGit(s *chatState, threadID string) {
	if s == nil || threadID == "" {
		return
	}
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	if st.threadID != threadID {
		fileManagerMu.Unlock()
		return
	}
	sessionGen := st.gen
	tree := ensureFileTreeLocked(st)
	seq := fileTreeGitBeginLocked(tree)
	fileManagerMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), fileTreeWatchTimeout)
		defer cancel()
		files, err := s.client.GitChangedFiles(ctx, threadID)
		if err != nil {
			return
		}
		fileManagerMu.Lock()
		if st.gen != sessionGen || st.threadID != threadID || tree.gitSeq != seq {
			fileManagerMu.Unlock()
			return
		}
		fileTreeApplyGitLocked(tree, files)
		tree.gitLoaded = true
		tree.gitTried = true
		fileManagerMu.Unlock()
		s.invalidate()
	}()
}

// fileTreeGitBeginLocked 让更早的 Git 状态回包失效。
func fileTreeGitBeginLocked(tree *fileTreeState) int {
	tree.gitSeq++
	return tree.gitSeq
}

// fileTreeApplyGitLocked 用新结果替换 Git 标记；返回是否有可见变化。
func fileTreeApplyGitLocked(tree *fileTreeState, files []GitChangedFile) bool {
	next := make(map[string]GitChangedFile, len(files))
	for _, file := range files {
		file.Path = strings.Trim(strings.TrimSpace(file.Path), "/")
		if file.Path == "" {
			continue
		}
		file.OrigPath = strings.Trim(strings.TrimSpace(file.OrigPath), "/")
		next[file.Path] = file
	}
	changed := len(next) != len(tree.git)
	if !changed {
		for path, file := range next {
			old, ok := tree.git[path]
			if !ok || old.Status != file.Status || old.OrigPath != file.OrigPath {
				changed = true
				break
			}
		}
	}
	tree.git = next
	if changed {
		// 只看改动视图按新列表重建，并重新展开新增的父目录。
		tree.changedSig = ""
	}
	return changed
}

// fileTreeToggleOnlyChanged 切换「只看改动」并保留完整树的展开、选中与滚动状态。
func (s *chatState) fileTreeToggleOnlyChanged(threadID string) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	tree := ensureFileTreeLocked(st)
	tree.onlyChanged = !tree.onlyChanged
	on := tree.onlyChanged
	if on {
		tree.changedSig = ""
	}
	fileManagerMu.Unlock()
	if on {
		fileTreeRefreshGit(s, threadID)
	}
	s.invalidate()
}

// fileTreeActiveOutlineLocked 返回当前视图使用的 Outline 状态。
func fileTreeActiveOutlineLocked(tree *fileTreeState) *ui.OutlineState[string] {
	if tree.onlyChanged && tree.changedOutline != nil {
		return tree.changedOutline
	}
	return &tree.outline
}

// fileTreeChangedOutlineLocked 返回「只看改动」专用 Outline，与完整树的状态互不影响。
func fileTreeChangedOutlineLocked(tree *fileTreeState) *ui.OutlineState[string] {
	if tree.changedOutline != nil {
		return tree.changedOutline
	}
	outline := &ui.OutlineState[string]{}
	outline.List.Selected = &tree.changedSelected
	outline.List.Selection = &tree.changedSelection
	outline.Indent = Space3
	tree.changedOutline = outline
	return outline
}

// fileTreeChangedRowsLocked 从 Git 改动列表生成只包含改动文件与必要父目录的树。
// 数据变化时默认展开全部目录；用户手动收起的目录在数据不变时保持收起。
func fileTreeChangedRowsLocked(tree *fileTreeState) (roots []string, children map[string][]string, entries map[string]FileEntry) {
	children = make(map[string][]string)
	entries = make(map[string]FileEntry)
	dirs := map[string]bool{"": true}
	paths := make([]string, 0, len(tree.git))
	for path := range tree.git {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	sig := make([]string, 0, len(paths))
	for _, filePath := range paths {
		file := tree.git[filePath]
		sig = append(sig, filePath, file.Status, file.OrigPath)
		for _, dir := range fileTreeAncestors(fileManagerParentPath(filePath)) {
			dirs[dir] = true
		}
	}

	perDir := make(map[string][]FileEntry)
	for _, filePath := range paths {
		entries[filePath] = FileEntry{Name: path.Base(filePath), Path: filePath}
		parent := fileManagerParentPath(filePath)
		perDir[parent] = append(perDir[parent], entries[filePath])
	}
	for dir := range dirs {
		if dir == "" {
			continue
		}
		entries[dir] = FileEntry{Name: path.Base(dir), Path: dir, IsDir: true}
		parent := fileManagerParentPath(dir)
		perDir[parent] = append(perDir[parent], entries[dir])
	}
	for dir, list := range perDir {
		fileTreeSortEntries(list)
		kids := make([]string, 0, len(list))
		for _, entry := range list {
			kids = append(kids, entry.Path)
		}
		children[dir] = kids
	}

	if signature := strings.Join(sig, "\n"); signature != tree.changedSig {
		tree.changedSig = signature
		outline := fileTreeChangedOutlineLocked(tree)
		outline.Open.Clear()
		for dir := range dirs {
			if dir != "" {
				outline.Open.Add(dir)
			}
		}
	}
	return children[""], children, entries
}

// fileTreeChangedRootCountLocked 统计「只看改动」的根条目数，供空状态判断。
func fileTreeChangedRootCountLocked(tree *fileTreeState) int {
	roots, _, _ := fileTreeChangedRowsLocked(tree)
	return len(roots)
}

// fileTreeStatusPresentation 把 Git 状态映射成单字符标记、完整名称与主题色。
func fileTreeStatusPresentation(c *ui.Context, status string) (letter, label string, color ui.Color) {
	switch status {
	case "M":
		return "M", "已修改", ThemeWarning(c)
	case "A":
		return "A", "新增", ThemeSuccess(c)
	case "D":
		return "D", "已删除", ThemeDanger(c)
	case "R":
		return "R", "已重命名", ThemeAccent(c)
	case "C":
		return "C", "已复制", ThemeAccent(c)
	default:
		return "?", "未跟踪", ThemeSecondaryText(c)
	}
}

// fileTreeStatusLabel 返回状态字符的完整名称。
func fileTreeStatusLabel(status string) string {
	switch status {
	case "M":
		return "已修改"
	case "A":
		return "新增"
	case "D":
		return "已删除"
	case "R":
		return "已重命名"
	case "C":
		return "已复制"
	default:
		return "未跟踪"
	}
}

// fileTreeAccessibilityLabel 返回标记的无障碍名称；重命名同时带出来源路径。
func fileTreeAccessibilityLabel(file GitChangedFile) string {
	label := fileTreeStatusLabel(file.Status)
	if file.OrigPath != "" && (file.Status == "R" || file.Status == "C") {
		label += "，原路径 " + file.OrigPath
	}
	return label
}

// renderFileTreeGitTag 渲染单字符状态标记，完整含义放在无障碍名称里。
func renderFileTreeGitTag(c *ui.Context, key string, file GitChangedFile) {
	letter, _, color := fileTreeStatusPresentation(c, file.Status)
	ui.Box(c.Key(key + "-status")).
		Shrink(0).
		Label(fileTreeAccessibilityLabel(file)).
		Transition(motionFeedback(c)).
		Children(func() {
			renderFileManagerTag(c, key, letter, color)
		})
}

// inspectorFileWatchTick 在右栏可见且当前标签是文件时推进监听检查；隐藏时暂停。
func inspectorFileWatchTick(s *chatState, st *inspectorSessionState) {
	if st == nil || s == nil {
		return
	}
	if _, ok := s.client.(workspaceGenerationClient); !ok {
		return
	}
	visible := st.Open && st.Active == sidePaneFileID
	fileTreeWatchSetVisible(st.Files, visible)
	if !visible {
		return
	}
	fileManagerMu.Lock()
	threadID := st.Files.threadID
	fileManagerMu.Unlock()
	fileTreeWatchTick(s, st.Files, threadID)
}

// fileTreeOnlyChangedActive 返回文件面板是否处于「只看改动」视图。
func fileTreeOnlyChangedActive(st *fileManagerState) bool {
	if st == nil {
		return false
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	return st.tree != nil && st.tree.onlyChanged
}

// fileTreeStatusLocked 返回路径的 Git 状态字符，非文件或没有状态时为空。
func fileTreeStatusLocked(tree *fileTreeState, filePath string, isDir bool) (GitChangedFile, bool) {
	if tree == nil || isDir {
		return GitChangedFile{}, false
	}
	file, ok := tree.git[filePath]
	if !ok || file.Status == "" {
		return GitChangedFile{}, false
	}
	return file, true
}
