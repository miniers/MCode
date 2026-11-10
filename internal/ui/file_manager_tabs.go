package mcodeui

import (
	"context"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// fileManagerTab 是文件管理面板里的一个预览标签页。
// path 是相对工作目录的路径；temporary 为 true 表示它是单击文件列表打开的
// 临时预览，之后单击别的文件会被原地替换，双击或从聊天卡片打开则固定。
// fromList 为 true 表示这个 tab 是从文件侧栏的列表或搜索结果打开的，
// 为 false 表示来自外部入口（聊天文件卡片、终端路径等）。
// 关闭最后一个 tab 时据此决定回到目录列表还是收起整个侧栏。
// gen 是这个 tab 自己的请求代数，异步结果回来时代数不符（或 tab 已被关闭）即丢弃。
type fileManagerTab struct {
	path         string
	temporary    bool
	fromList     bool
	gen          int
	kind         string
	fileLoading  bool
	fileErr      string
	content      string
	totalSize    int64
	nextOffset   int64
	hasMore      bool
	limitReached bool
	imageData    []byte
	imageBmp     *ui.Bitmap
	mdPreview    bool
	hlFirst      int
	hlLast       int
	reveal       bool
	revealSeq    int
}

// findTabLocked 按路径查找 tab；调用方持 fileManagerMu。
func (st *fileManagerState) findTabLocked(path string) *fileManagerTab {
	for _, tab := range st.tabs {
		if tab.path == path {
			return tab
		}
	}
	return nil
}

// temporaryTabLocked 返回一个可以原地复用的临时 tab：最近的、且不是拆分固定栏的那个。
// 调用方持 fileManagerMu。
func (st *fileManagerState) temporaryTabLocked() *fileManagerTab {
	for i := len(st.tabs) - 1; i >= 0; i-- {
		tab := st.tabs[i]
		if tab.temporary && tab.path != st.splitAnchor {
			return tab
		}
	}
	return nil
}

// firstTabExceptLocked 返回第一个路径不等于 except 的 tab。调用方持 fileManagerMu。
func (st *fileManagerState) firstTabExceptLocked(except string) *fileManagerTab {
	for _, tab := range st.tabs {
		if tab.path != except {
			return tab
		}
	}
	return nil
}

// tabLiveLocked 判断异步结果是否仍属于面板里的这个 tab：tab 未被关闭且代数未变。
// 调用方持 fileManagerMu。
func (st *fileManagerState) tabLiveLocked(tab *fileManagerTab, gen int) bool {
	if tab == nil || tab.gen != gen {
		return false
	}
	for _, t := range st.tabs {
		if t == tab {
			return true
		}
	}
	return false
}

// resetFileManagerTab 把一个临时 tab 原地换成新文件：清空上一个文件的内容，gen 由调用方递增。
func resetFileManagerTab(tab *fileManagerTab, path string) {
	tab.path = path
	tab.temporary = true
	tab.kind = ""
	tab.fileErr = ""
	tab.content = ""
	tab.totalSize = 0
	tab.nextOffset = 0
	tab.hasMore = false
	tab.limitReached = false
	tab.imageData = nil
	tab.imageBmp = nil
	tab.mdPreview = isMarkdownPath(path)
	tab.hlFirst, tab.hlLast = 0, 0
	tab.reveal = false
}

// openFileManagerFile 从文件侧栏打开某个文件的预览：已有同路径 tab 时直接激活，
// pin 为 false 时优先原地替换临时 tab，否则追加新 tab。
// 只有新建或替换时才重新读取内容。
func (s *chatState) openFileManagerFile(threadID, filePath string, pin bool) {
	s.openFileManagerFileFrom(threadID, filePath, pin, true)
}

// openFileManagerFileExternal 从侧栏以外的入口（聊天文件卡片、终端路径）打开文件：
// 效果等同于 pin 为 true 打开，但新 tab 记作外部来源，关掉最后一个 tab 时收起整个侧栏。
func (s *chatState) openFileManagerFileExternal(threadID, filePath string) {
	s.openFileManagerFileExternalAt(threadID, filePath, 0, 0)
}

func (s *chatState) openFileManagerFileExternalAt(threadID, filePath string, first, last int) {
	inspectorSelect(s, sidePaneFileID, false)
	s.openFileManagerFileFromAt(threadID, filePath, true, false, first, last)
}

// openFileManagerFileFrom 是前两个入口共用的实现；fromList 表示打开来源是否为文件侧栏。
// 打开已存在的同路径 tab 时不改动它原来的来源标记。
func (s *chatState) openFileManagerFileFrom(threadID, filePath string, pin, fromList bool) {
	s.openFileManagerFileFromAt(threadID, filePath, pin, fromList, 0, 0)
}

func (s *chatState) openFileManagerFileFromAt(threadID, filePath string, pin, fromList bool, first, last int) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}

	fileManagerMu.Lock()
	tab, fresh := st.openFileManagerTabLocked(filePath, pin, fromList)
	if first > 0 && last > 0 {
		if first > last {
			first, last = last, first
		}
		tab.hlFirst, tab.hlLast = first, last
		tab.reveal = true
		tab.revealSeq++
		tab.mdPreview = false
	}
	// 激活这个文件；拆分状态下它若不在固定栏，就顶替第二栏。
	st.active = filePath
	st.showList = false
	if st.splitAnchor != "" && filePath != st.splitAnchor {
		st.splitSide = filePath
	}
	if !fresh {
		fileManagerMu.Unlock()
		s.invalidate()
		return
	}
	tab.gen++
	gen := tab.gen
	tab.fileLoading = threadID != ""
	fileManagerMu.Unlock()
	s.invalidate()

	if threadID == "" {
		return
	}
	s.readFileManagerTab(st, tab, threadID, filePath, gen)
}

// openFileManagerTabLocked 选出显示 filePath 的 tab；返回的 fresh 表示需要重新读取内容。
// 新建或原地替换的 tab 使用传入的 fromList；已有的 tab 保留原来的来源标记。
// 调用方持 fileManagerMu。
func (st *fileManagerState) openFileManagerTabLocked(filePath string, pin, fromList bool) (*fileManagerTab, bool) {
	if existing := st.findTabLocked(filePath); existing != nil {
		if pin {
			existing.temporary = false
		}
		return existing, false
	}
	if !pin {
		if temp := st.temporaryTabLocked(); temp != nil {
			// 临时 tab 原地换文件：同一个下标，自己换一代 gen 丢弃旧请求的结果。
			resetFileManagerTab(temp, filePath)
			temp.fromList = fromList
			return temp, true
		}
	}
	tab := &fileManagerTab{path: filePath, temporary: !pin, fromList: fromList, mdPreview: isMarkdownPath(filePath)}
	st.tabs = append(st.tabs, tab)
	return tab, true
}

// readFileManagerTab 异步读取某个 tab 的首块内容；图片再按更大分块拼接后解码。
func (s *chatState) readFileManagerTab(st *fileManagerState, tab *fileManagerTab, threadID, filePath string, gen int) {
	go func() {
		content, err := s.client.FilesRead(context.Background(), threadID, filePath, 0, fileManagerTextChunkSize)

		fileManagerMu.Lock()
		if !st.tabLiveLocked(tab, gen) {
			fileManagerMu.Unlock()
			return
		}
		tab.fileLoading = false
		if err != nil {
			tab.fileErr = err.Error()
			fileManagerMu.Unlock()
			s.invalidate()
			return
		}
		if content.Kind != "image" {
			tab.kind = content.Kind
			tab.content = content.Text
			tab.totalSize = content.TotalSize
			tab.nextOffset = content.NextOffset
			tab.hasMore = content.HasMore
			fileManagerMu.Unlock()
			s.invalidate()
			return
		}
		// 图片要按更大的分块重新读取：先清掉这次文本块的结果，再从偏移 0 开始拼接。
		tab.kind = "image"
		tab.totalSize = content.TotalSize
		tab.imageData = nil
		tab.fileLoading = true
		fileManagerMu.Unlock()
		s.invalidate()

		var data []byte
		var offset int64
		for {
			chunk, readErr := s.client.FilesRead(context.Background(), threadID, filePath, offset, fileManagerImageChunkSize)
			if readErr != nil {
				fileManagerMu.Lock()
				if st.tabLiveLocked(tab, gen) {
					tab.fileLoading = false
					tab.fileErr = readErr.Error()
					tab.imageData = nil
				}
				fileManagerMu.Unlock()
				s.invalidate()
				return
			}
			if chunk.Kind != "" && chunk.Kind != "image" {
				// 读取过程中类型变了（文件被改写）：按二进制处理，不再拼接。
				fileManagerMu.Lock()
				if st.tabLiveLocked(tab, gen) {
					tab.fileLoading = false
					tab.kind = chunk.Kind
					tab.imageData = nil
					tab.imageBmp = nil
					tab.totalSize = chunk.TotalSize
					tab.fileErr = ""
					tab.content = ""
				}
				fileManagerMu.Unlock()
				s.invalidate()
				return
			}
			data = append(data, chunk.Data...)
			if chunk.TotalSize > 0 {
				fileManagerMu.Lock()
				if st.tabLiveLocked(tab, gen) {
					tab.totalSize = chunk.TotalSize
				}
				fileManagerMu.Unlock()
			}
			reachedLimit := len(data) >= fileManagerImageMaxBytes
			if reachedLimit {
				data = data[:fileManagerImageMaxBytes]
			}
			if !chunk.HasMore || reachedLimit || chunk.NextOffset <= offset {
				break
			}
			offset = chunk.NextOffset
		}

		// 解码放后台：仓库内既有的附件原图解码也在 goroutine 里做（见 decodeMessageMedia）。
		bmp, decodeErr := ui.DecodeBitmap(data)

		fileManagerMu.Lock()
		if !st.tabLiveLocked(tab, gen) {
			fileManagerMu.Unlock()
			return
		}
		tab.fileLoading = false
		tab.imageData = data
		if decodeErr != nil || bmp == nil {
			tab.fileErr = "图片解码失败：" + fmt.Sprint(decodeErr)
			tab.imageBmp = nil
		} else {
			tab.imageBmp = bmp
		}
		fileManagerMu.Unlock()
		s.invalidate()
	}()
}

// loadMoreFileManagerTab 追加读取某个 tab 的下一块文本内容；达到预览上限时停止并标记。
func (s *chatState) loadMoreFileManagerTab(path string) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}

	fileManagerMu.Lock()
	tab := st.findTabLocked(path)
	if tab == nil || tab.fileLoading || !tab.hasMore || tab.limitReached {
		fileManagerMu.Unlock()
		return
	}
	threadID := st.threadID
	offset := tab.nextOffset
	tab.gen++
	gen := tab.gen
	tab.fileLoading = true
	fileManagerMu.Unlock()
	s.invalidate()

	go func() {
		content, err := s.client.FilesRead(context.Background(), threadID, path, offset, fileManagerTextChunkSize)

		fileManagerMu.Lock()
		if !st.tabLiveLocked(tab, gen) {
			fileManagerMu.Unlock()
			return
		}
		tab.fileLoading = false
		if err != nil {
			fallback := tab.reveal && tab.content != ""
			if fallback {
				// 定位时保留已读取的内容，以便回退到最后一行。
				tab.hasMore = false
			} else {
				tab.fileErr = err.Error()
			}
			fileManagerMu.Unlock()
			if fallback {
				s.setToast("加载更多失败：" + err.Error())
			}
			s.invalidate()
			return
		}
		tab.content += content.Text
		tab.nextOffset = content.NextOffset
		tab.hasMore = content.HasMore
		if content.TotalSize > 0 {
			tab.totalSize = content.TotalSize
		}
		if len(tab.content) >= fileManagerTextMaxBytes {
			// 累计内容已到预览上限：停止继续加载，并提示已达上限。
			tab.hasMore = false
			tab.limitReached = true
		}
		fileManagerMu.Unlock()
		s.invalidate()
	}()
}

// closeFileManagerTab 关闭某个 tab：关掉当前 tab 时切到相邻 tab，
// 关掉拆分固定栏时取消拆分。tab 全部关完时，关掉的若是外部入口打开的 tab
// （来自聊天文件卡片、终端路径），收起整个侧栏；否则回到目录列表。
func (s *chatState) closeFileManagerTab(path string) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}

	fileManagerMu.Lock()
	idx := -1
	for i, tab := range st.tabs {
		if tab.path == path {
			idx = i
			break
		}
	}
	if idx < 0 {
		fileManagerMu.Unlock()
		return
	}

	fromList := st.tabs[idx].fromList
	wasActive := st.active == path
	wasAnchor := st.splitAnchor == path
	wasSide := st.splitSide == path
	oldSide := st.splitSide
	st.tabs = append(st.tabs[:idx], st.tabs[idx+1:]...)

	// 相邻 tab：优先原下标，越界取末尾。
	var fallback *fileManagerTab
	if len(st.tabs) > 0 {
		next := idx
		if next >= len(st.tabs) {
			next = len(st.tabs) - 1
		}
		fallback = st.tabs[next]
	}

	if wasActive {
		if fallback != nil {
			st.active = fallback.path
		} else {
			st.active = ""
		}
	}

	switch {
	case wasAnchor:
		// 固定栏被关：取消拆分，原来第二栏的文件接替当前视图。
		st.splitAnchor = ""
		st.splitSide = ""
		if oldSide != "" && oldSide != path {
			st.active = oldSide
			st.showList = false
		}
	case wasSide:
		st.splitSide = ""
		if fallback != nil && fallback.path != st.splitAnchor {
			st.splitSide = fallback.path
		} else if other := st.firstTabExceptLocked(st.splitAnchor); other != nil {
			st.splitSide = other.path
		}
		if st.splitSide == "" {
			st.splitAnchor = ""
		}
	}

	empty := len(st.tabs) == 0
	if empty {
		st.active = ""
		st.splitAnchor = ""
		st.splitSide = ""
		st.showList = true
	}
	fileManagerMu.Unlock()

	// 最后一个 tab 来自外部入口：收起侧栏（closeFileManager 自己会加锁，不能持锁调用）。
	if empty && !fromList {
		closeFileManager(s)
		return
	}
	s.invalidate()
}

// pinFileManagerTab 把临时预览 tab 固定下来（双击 tab 或从聊天卡片打开）。
func (s *chatState) pinFileManagerTab(path string) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	if tab := st.findTabLocked(path); tab != nil && tab.temporary {
		tab.temporary = false
	}
	fileManagerMu.Unlock()
	s.invalidate()
}

// activateFileManagerTab 激活某个 tab；拆分状态下它不在固定栏时同时顶替第二栏。
func (s *chatState) activateFileManagerTab(path string) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	if st.findTabLocked(path) == nil {
		fileManagerMu.Unlock()
		return
	}
	st.active = path
	st.showList = false
	if st.splitAnchor != "" && path != st.splitAnchor {
		st.splitSide = path
	}
	fileManagerMu.Unlock()
	s.invalidate()
}

// splitFileManager 把当前 tab 固定到第一栏，第二栏取列表里相邻的另一个文件；
// 只有一个 tab 时两栏显示同一个文件。
func (s *chatState) splitFileManager() {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	if len(st.tabs) == 0 {
		fileManagerMu.Unlock()
		return
	}
	anchor := st.active
	idx := -1
	for i, tab := range st.tabs {
		if tab.path == anchor {
			idx = i
			break
		}
	}
	if idx < 0 {
		anchor = st.tabs[0].path
		idx = 0
	}
	// 先看 active 前一个，没有就看后一个；只有一个 tab 时两栏相同。
	side := anchor
	if idx > 0 {
		side = st.tabs[idx-1].path
	} else if len(st.tabs) > 1 {
		side = st.tabs[1].path
	}
	st.splitAnchor = anchor
	st.splitSide = side
	st.showList = false
	fileManagerMu.Unlock()
	s.invalidate()
}

// unsplitFileManager 取消拆分，active 不变；方向偏好保留，下次拆分沿用。
func (s *chatState) unsplitFileManager() {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	st.splitAnchor = ""
	st.splitSide = ""
	fileManagerMu.Unlock()
	s.invalidate()
}

// toggleFileManagerSplitDirection 在左右与上下分栏之间切换。
func (s *chatState) toggleFileManagerSplitDirection() {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	st.splitVertical = !st.splitVertical
	fileManagerMu.Unlock()
	s.invalidate()
}

// showFileManagerList 回到目录列表视图，保留全部 tab，也不重新读取目录。
func (s *chatState) showFileManagerList() {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	st.showList = true
	fileManagerMu.Unlock()
	s.invalidate()
}

// setFileManagerTabMarkdownPreview 切换某个 tab 的 Markdown 预览/源码显示。
func (s *chatState) setFileManagerTabMarkdownPreview(path string, preview bool) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	if tab := st.findTabLocked(path); tab != nil {
		tab.mdPreview = preview
	}
	fileManagerMu.Unlock()
	s.invalidate()
}

// fileManagerTabName 返回 tab 显示的文件名（路径的最后一段）。
func fileManagerTabName(path string) string {
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[idx+1:]
	}
	return path
}

// fileManagerPathUnder 判断 path 是否等于 base 或位于 base 目录下。
func fileManagerPathUnder(path, base string) bool {
	return path == base || (base != "" && strings.HasPrefix(path, base+"/"))
}

// closeOtherFileManagerTabs 关闭除 path 以外的所有 tab，并取消拆分；path 成为当前 tab。
func (s *chatState) closeOtherFileManagerTabs(path string) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	var keep *fileManagerTab
	for _, tab := range st.tabs {
		if tab.path == path {
			keep = tab
			break
		}
	}
	if keep == nil {
		fileManagerMu.Unlock()
		return
	}
	st.tabs = []*fileManagerTab{keep}
	st.active = path
	st.splitAnchor = ""
	st.splitSide = ""
	fileManagerMu.Unlock()
	s.invalidate()
}

// closeFileManagerTabsRightOf 关闭 path 右侧的所有 tab。
// 逐个走 closeFileManagerTab，沿用它的相邻 tab 回退与拆分处理规则。
func (s *chatState) closeFileManagerTabsRightOf(path string) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	idx := -1
	for i, tab := range st.tabs {
		if tab.path == path {
			idx = i
			break
		}
	}
	victims := make([]string, 0, len(st.tabs))
	if idx >= 0 {
		for _, tab := range st.tabs[idx+1:] {
			victims = append(victims, tab.path)
		}
	}
	fileManagerMu.Unlock()

	for _, p := range victims {
		s.closeFileManagerTab(p)
	}
}

// closeAllFileManagerTabs 清空 tabs 并取消拆分，回到目录列表；
// 面板保持打开，不走「最后一个外部 tab 关完收起侧栏」的规则。
func (s *chatState) closeAllFileManagerTabs() {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	st.tabs = nil
	st.active = ""
	st.splitAnchor = ""
	st.splitSide = ""
	st.showList = true
	fileManagerMu.Unlock()
	s.invalidate()
}

// renameFileManagerTabPaths 把等于 oldPath 或位于 oldPath 目录下的 tab 路径整体改到
// 新前缀下，当前 tab、拆分两栏与面板目录一并同步（重命名成功后调用）。
func (s *chatState) renameFileManagerTabPaths(oldPath, newPath string) {
	if oldPath == "" || oldPath == newPath {
		return
	}
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	renamed := func(p string) (string, bool) {
		if p == oldPath {
			return newPath, true
		}
		if p != "" && strings.HasPrefix(p, oldPath+"/") {
			return newPath + p[len(oldPath):], true
		}
		return p, false
	}

	fileManagerMu.Lock()
	for _, tab := range st.tabs {
		if next, ok := renamed(tab.path); ok {
			tab.path = next
		}
	}
	if next, ok := renamed(st.active); ok {
		st.active = next
	}
	if next, ok := renamed(st.splitAnchor); ok {
		st.splitAnchor = next
	}
	if next, ok := renamed(st.splitSide); ok {
		st.splitSide = next
	}
	if next, ok := renamed(st.dir); ok {
		st.dir = next
	}
	fileManagerMu.Unlock()
	s.invalidate()
}

// closeFileManagerTabsUnder 关闭等于 path 或位于 path 目录下的所有 tab
// （文件移入废纸篓后调用）。
func (s *chatState) closeFileManagerTabsUnder(path string) {
	if path == "" {
		return
	}
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	victims := make([]string, 0, len(st.tabs))
	for _, tab := range st.tabs {
		if fileManagerPathUnder(tab.path, path) {
			victims = append(victims, tab.path)
		}
	}
	fileManagerMu.Unlock()

	for _, p := range victims {
		s.closeFileManagerTab(p)
	}
}
