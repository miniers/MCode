package mcodeui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

const fileManagerReadTimeout = 30 * time.Second

// fileManagerTab 是文件管理面板里的一个预览标签页。
// path 是相对工作目录的路径；temporary 为 true 表示它是单击文件列表打开的
// 临时预览，之后单击别的文件会被原地替换，双击或从聊天卡片打开则固定。
// gen 是这个 tab 自己的请求代数，异步结果回来时代数不符（或 tab 已被关闭）即丢弃。
type fileManagerTab struct {
	path         string
	temporary    bool
	gen          int
	kind         string
	binaryLocal  bool
	fileLoading  bool
	fileErr      string
	content      string
	totalSize    int64
	nextOffset   int64
	hasMore      bool
	limitReached bool
	dirty        bool
	saving       bool
	saveGen      int
	imageData    []byte
	imageBmp     *ui.Bitmap
	mdPreview    bool
	svg          *ui.SVG
	svgPreview   bool
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

// temporaryTabLocked 返回一个可以原地复用的临时 tab：最近的、未修改且不是拆分固定栏的那个。
// 调用方持 fileManagerMu。
func (st *fileManagerState) temporaryTabLocked() *fileManagerTab {
	for i := len(st.tabs) - 1; i >= 0; i-- {
		tab := st.tabs[i]
		if tab.temporary && !tab.dirty && !tab.saving && tab.path != st.splitAnchor {
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
	tab.binaryLocal = false
	tab.fileErr = ""
	tab.content = ""
	tab.totalSize = 0
	tab.nextOffset = 0
	tab.hasMore = false
	tab.limitReached = false
	tab.dirty = false
	tab.saving = false
	tab.saveGen++
	tab.imageData = nil
	tab.imageBmp = nil
	tab.mdPreview = isMarkdownPath(path)
	tab.svg = nil
	tab.svgPreview = isInspectorSVGPath(path)
	tab.hlFirst, tab.hlLast = 0, 0
	tab.reveal = false
}

// openFileManagerFile 从文件侧栏打开某个文件的预览：已有同路径 tab 时直接激活，
// pin 为 false 时优先原地替换临时 tab，否则追加新 tab。
// 只有新建或替换时才重新读取内容。
func (s *chatState) openFileManagerFile(threadID, filePath string, pin bool) {
	s.openFileManagerFileFromAt(threadID, filePath, pin, 0, 0)
}

// openFileManagerFileExternal 从侧栏以外的入口（聊天文件卡片、终端路径）打开文件：
// 效果等同于 pin 为 true 打开。关闭最后一个 tab 时面板保持打开并回到树。
func (s *chatState) openFileManagerFileExternal(threadID, filePath string) {
	s.openFileManagerFileExternalAt(threadID, filePath, 0, 0)
}

func (s *chatState) openFileManagerFileExternalAt(threadID, filePath string, first, last int) {
	inspectorFollowClear(s, threadID, sidePaneFileID)
	inspectorSelect(s, sidePaneFileID, false)
	s.openFileManagerFileFromAt(threadID, filePath, true, first, last)
}

func (s *chatState) openFileManagerFileFromAt(threadID, filePath string, pin bool, first, last int) {
	inspectorFollowPause(s, threadID)
	s.openFileManagerFileAt(threadID, filePath, pin, first, last, true)
}

func (s *chatState) openFileManagerFileFollowing(threadID, filePath string, first, last int, refresh bool) {
	s.prepareFileManagerTree(threadID)
	if first <= 0 {
		first, last = 1, 1
	}
	s.openFileManagerFileAt(threadID, filePath, false, first, last, false)
	if refresh {
		st := getFileManagerState(s)
		fileManagerMu.Lock()
		tab := st.findTabLocked(filePath)
		if tab == nil || tab.fileLoading {
			fileManagerMu.Unlock()
			return
		}
		tab.gen++
		gen := tab.gen
		tab.fileLoading = true
		fileManagerMu.Unlock()
		s.readFileManagerTab(st, tab, threadID, filePath, gen)
	}
}

func (s *chatState) openFileManagerFileAt(threadID, filePath string, pin bool, first, last int, user bool) {
	// PDF 不在内容标签里读源码，直接交给右栏浏览器；客户端不支持时退回普通二进制提示。
	if user && threadID != "" && isInspectorPDFPath(filePath) && s.openInspectorPreview(threadID, filePath) {
		return
	}
	st := getFileManagerState(s)
	if st == nil {
		return
	}

	fileManagerMu.Lock()
	tab, fresh := st.openFileManagerTabLocked(filePath, pin)
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
		if threadID != "" && !tab.fileLoading && !fileManagerTabUsesCodePreviewLocked(tab) {
			tab.reveal = false
		}
		fileManagerMu.Unlock()
		inspectorRemember(s)
		s.invalidate()
		return
	}
	tab.gen++
	gen := tab.gen
	tab.fileLoading = threadID != ""
	fileManagerMu.Unlock()
	inspectorRemember(s)
	s.invalidate()

	if threadID == "" {
		return
	}
	s.readFileManagerTab(st, tab, threadID, filePath, gen)
}

// openFileManagerTabLocked 选出显示 filePath 的 tab；返回的 fresh 表示需要重新读取内容。
// 调用方持 fileManagerMu。
func (st *fileManagerState) openFileManagerTabLocked(filePath string, pin bool) (*fileManagerTab, bool) {
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
			return temp, true
		}
	}
	tab := &fileManagerTab{path: filePath, temporary: !pin, mdPreview: isMarkdownPath(filePath), svgPreview: isInspectorSVGPath(filePath)}
	st.tabs = append(st.tabs, tab)
	return tab, true
}

func fileManagerTabUsesCodePreviewLocked(tab *fileManagerTab) bool {
	if tab == nil || tab.fileErr != "" || tab.kind == "image" || tab.kind == "binary" || tab.content == "" {
		return false
	}
	if tab.mdPreview && isMarkdownPath(tab.path) {
		return false
	}
	if tab.svgPreview && isInspectorSVGPath(tab.path) && tab.svg != nil {
		return false
	}
	return true
}

func (s *chatState) readFileManagerChunk(threadID, filePath string, offset, limit int64) (FileContent, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fileManagerReadTimeout)
	defer cancel()
	return s.client.FilesRead(ctx, threadID, filePath, offset, limit)
}

// readFileManagerTab 异步读取某个 tab 的首块内容；图片再按更大分块拼接后解码。
func (s *chatState) readFileManagerTab(st *fileManagerState, tab *fileManagerTab, threadID, filePath string, gen int) {
	go func() {
		content, err := s.readFileManagerChunk(threadID, filePath, 0, fileManagerTextChunkSize)
		var binaryLocal bool
		if err == nil && content.Kind == "binary" {
			binaryLocal = fileManagerLocalPath(fileManagerWorkspacePath(s, threadID, filePath))
		}

		fileManagerMu.Lock()
		if !st.tabLiveLocked(tab, gen) {
			fileManagerMu.Unlock()
			return
		}
		tab.fileLoading = false
		if tab.dirty || tab.saving {
			fileManagerMu.Unlock()
			s.invalidate()
			return
		}
		if err != nil {
			tab.fileErr = err.Error()
			tab.reveal = false
			fileManagerMu.Unlock()
			s.invalidate()
			return
		}
		if content.Kind != "image" {
			// SVG 一律尝试解析，但只有完整读入且通过上限/外链检查时才有预览。
			svg := fileManagerSVGForContent(filePath, content.Text, content.TotalSize, content.HasMore)
			tab.kind = content.Kind
			tab.binaryLocal = binaryLocal
			tab.content = content.Text
			tab.totalSize = content.TotalSize
			tab.nextOffset = content.NextOffset
			tab.hasMore = content.HasMore
			tab.svg = svg
			if isInspectorSVGPath(filePath) {
				// 解析失败/超限/外链时没有可预览的内容，默认停在源码。
				tab.svgPreview = svg != nil
			}
			if !fileManagerTabUsesCodePreviewLocked(tab) {
				tab.reveal = false
			}
			fileManagerMu.Unlock()
			s.invalidate()
			return
		}
		// 图片要按更大的分块重新读取：先清掉这次文本块的结果，再从偏移 0 开始拼接。
		tab.kind = "image"
		tab.totalSize = content.TotalSize
		tab.imageData = nil
		tab.fileLoading = true
		tab.reveal = false
		fileManagerMu.Unlock()
		s.invalidate()

		var data []byte
		var offset int64
		for {
			chunk, readErr := s.readFileManagerChunk(threadID, filePath, offset, fileManagerImageChunkSize)
			if readErr != nil {
				fileManagerMu.Lock()
				if st.tabLiveLocked(tab, gen) {
					tab.fileLoading = false
					tab.fileErr = readErr.Error()
					tab.imageData = nil
					tab.reveal = false
				}
				fileManagerMu.Unlock()
				s.invalidate()
				return
			}
			if chunk.Kind != "" && chunk.Kind != "image" {
				// 读取过程中类型变了（文件被改写）：按二进制处理，不再拼接。
				var binaryLocal bool
				if chunk.Kind == "binary" {
					binaryLocal = fileManagerLocalPath(fileManagerWorkspacePath(s, threadID, filePath))
				}
				fileManagerMu.Lock()
				if st.tabLiveLocked(tab, gen) {
					tab.fileLoading = false
					tab.kind = chunk.Kind
					tab.binaryLocal = binaryLocal
					tab.imageData = nil
					tab.imageBmp = nil
					tab.totalSize = chunk.TotalSize
					tab.fileErr = ""
					tab.content = ""
					tab.reveal = false
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
		tab.reveal = false
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
		content, err := s.readFileManagerChunk(threadID, path, offset, fileManagerTextChunkSize)

		fileManagerMu.Lock()
		if !st.tabLiveLocked(tab, gen) {
			fileManagerMu.Unlock()
			return
		}
		tab.fileLoading = false
		if tab.dirty || tab.saving {
			fileManagerMu.Unlock()
			s.invalidate()
			return
		}
		if err != nil {
			fallback := tab.reveal && tab.content != ""
			if fallback {
				// 定位时保留已读取的内容，以便回退到最后一行。
				tab.hasMore = false
			} else {
				tab.fileErr = err.Error()
				tab.reveal = false
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
		// 加载更多后 SVG 可能才完整读入；重新解析一次，结果仍由同一代次保护。
		svgContent, svgTotal := "", int64(0)
		if isInspectorSVGPath(path) && !tab.hasMore && !tab.limitReached {
			svgContent, svgTotal = tab.content, tab.totalSize
		}
		fileManagerMu.Unlock()
		if svgContent != "" {
			svg := fileManagerSVGForContent(path, svgContent, svgTotal, false)
			fileManagerMu.Lock()
			if st.tabLiveLocked(tab, gen) {
				if tab.svg == nil && svg != nil {
					// 首次完整读入后才具备预览条件；此前没有切换按钮，不存在用户选择被覆盖。
					tab.svgPreview = true
					tab.reveal = false
				}
				tab.svg = svg
			}
			fileManagerMu.Unlock()
		}
		s.invalidate()
	}()
}

// closeFileManagerTab 关闭某个 tab：有未保存修改时先弹出丢弃确认，否则立即关闭；
// 关掉当前 tab 时切到相邻 tab，关掉拆分固定栏时取消拆分。tab 全部关完时回到文件树，面板保持打开。
func (s *chatState) closeFileManagerTab(path string) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}

	fileManagerMu.Lock()
	tab := st.findTabLocked(path)
	if tab == nil {
		fileManagerMu.Unlock()
		return
	}
	if tab.dirty {
		st.discardConfirm = fileManagerDiscardConfirm{open: true, mode: "single", path: path}
		fileManagerMu.Unlock()
		s.invalidate()
		return
	}
	empty, wasActive := st.closeFileManagerTabLocked(path)
	fileManagerMu.Unlock()

	if empty || wasActive {
		inspectorRemember(s)
	}
	s.invalidate()
}

// closeFileManagerTabLocked 在持有 fileManagerMu 时强制关闭单个 tab，不检查 dirty。
func (st *fileManagerState) closeFileManagerTabLocked(path string) (empty, wasActive bool) {
	idx := -1
	for i, tab := range st.tabs {
		if tab.path == path {
			idx = i
			break
		}
	}
	if idx < 0 {
		return len(st.tabs) == 0, false
	}

	wasActive = st.active == path
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

	empty = len(st.tabs) == 0
	if empty {
		st.active = ""
		st.splitAnchor = ""
		st.splitSide = ""
		st.showList = true
	}
	return empty, wasActive
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
	inspectorFollowPause(s, "")
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
	inspectorRemember(s)
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
	inspectorFollowPause(s, "")
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

// setFileManagerTabSVGPreview 切换某个 tab 的 SVG 预览/源码显示。
func (s *chatState) setFileManagerTabSVGPreview(path string, preview bool) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	if tab := st.findTabLocked(path); tab != nil {
		tab.svgPreview = preview
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
	hasDirty := false
	for _, tab := range st.tabs {
		if tab.path == path {
			keep = tab
			continue
		}
		if tab.dirty {
			hasDirty = true
		}
	}
	if keep == nil {
		fileManagerMu.Unlock()
		return
	}
	if hasDirty {
		st.discardConfirm = fileManagerDiscardConfirm{open: true, mode: "other", path: path}
		fileManagerMu.Unlock()
		s.invalidate()
		return
	}
	st.closeOtherFileManagerTabsLocked(path)
	fileManagerMu.Unlock()
	inspectorRemember(s)
	s.invalidate()
}

func (st *fileManagerState) closeOtherFileManagerTabsLocked(path string) {
	keep := st.findTabLocked(path)
	if keep == nil {
		return
	}
	st.tabs = []*fileManagerTab{keep}
	st.active = path
	st.splitAnchor = ""
	st.splitSide = ""
}

// closeFileManagerTabsRightOf 关闭 path 右侧的所有 tab。
// 逐个走强制关闭逻辑，沿用相邻 tab 回退与拆分处理规则。
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
	if idx < 0 {
		fileManagerMu.Unlock()
		return
	}
	hasDirty := false
	for _, tab := range st.tabs[idx+1:] {
		if tab.dirty {
			hasDirty = true
			break
		}
	}
	if hasDirty {
		st.discardConfirm = fileManagerDiscardConfirm{open: true, mode: "right", path: path}
		fileManagerMu.Unlock()
		s.invalidate()
		return
	}
	needRemember := st.closeFileManagerTabsRightOfLocked(path)
	fileManagerMu.Unlock()
	if needRemember {
		inspectorRemember(s)
	}
	s.invalidate()
}

func (st *fileManagerState) closeFileManagerTabsRightOfLocked(path string) bool {
	idx := -1
	for i, tab := range st.tabs {
		if tab.path == path {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}
	victims := make([]string, 0, len(st.tabs)-idx-1)
	for _, tab := range st.tabs[idx+1:] {
		victims = append(victims, tab.path)
	}
	needRemember := false
	for _, p := range victims {
		empty, wasActive := st.closeFileManagerTabLocked(p)
		if empty || wasActive {
			needRemember = true
		}
	}
	return needRemember
}

// closeAllFileManagerTabs 清空 tabs 并取消拆分，回到目录列表；
// 面板保持打开，不走「最后一个外部 tab 关完收起侧栏」的规则。
func (s *chatState) closeAllFileManagerTabs() {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	for _, tab := range st.tabs {
		if tab.dirty {
			st.discardConfirm = fileManagerDiscardConfirm{open: true, mode: "all"}
			fileManagerMu.Unlock()
			s.invalidate()
			return
		}
	}
	st.closeAllFileManagerTabsLocked()
	fileManagerMu.Unlock()
	inspectorRemember(s)
	s.invalidate()
}

func (st *fileManagerState) closeAllFileManagerTabsLocked() {
	st.tabs = nil
	st.active = ""
	st.splitAnchor = ""
	st.splitSide = ""
	st.showList = true
}

const modalDragFileManagerDiscard = "modal-drag-file-manager-discard"

type fileManagerDiscardConfirm struct {
	open bool
	mode string // "single", "other", "right", "all"
	path string
}

func (s *chatState) confirmDiscardFileManagerTabs() {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	dc := st.discardConfirm
	st.discardConfirm = fileManagerDiscardConfirm{}
	needRemember := false
	switch dc.mode {
	case "single":
		empty, wasActive := st.closeFileManagerTabLocked(dc.path)
		needRemember = empty || wasActive
	case "other":
		st.closeOtherFileManagerTabsLocked(dc.path)
		needRemember = true
	case "right":
		needRemember = st.closeFileManagerTabsRightOfLocked(dc.path)
	case "all":
		st.closeAllFileManagerTabsLocked()
		needRemember = true
	}
	fileManagerMu.Unlock()
	modalDragReset(modalDragFileManagerDiscard)
	if needRemember {
		inspectorRemember(s)
	}
	s.invalidate()
}

func (s *chatState) cancelDiscardFileManagerTabs() {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	st.discardConfirm = fileManagerDiscardConfirm{}
	fileManagerMu.Unlock()
	modalDragReset(modalDragFileManagerDiscard)
	s.invalidate()
}

func renderFileManagerDiscardDialog(c *ui.Context, s *chatState) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	dc := st.discardConfirm
	body := ""
	if dc.open {
		if dc.mode == "single" {
			body = fileManagerTabName(dc.path) + " 有未保存的修改，关闭将丢失这些修改。"
		} else {
			body = "有文件存在未保存的修改，关闭将丢失这些修改。"
		}
	}
	fileManagerMu.Unlock()
	if !dc.open {
		RenderConfirmDialog(c, ConfirmDialog{Key: modalDragFileManagerDiscard, Open: false})
		return
	}
	RenderConfirmDialog(c, ConfirmDialog{
		Key:             modalDragFileManagerDiscard,
		Open:            true,
		Tone:            ConfirmDanger,
		Title:           "放弃未保存的修改？",
		Body:            body,
		ConfirmLabel:    "放弃并关闭",
		CancelLabel:     "取消",
		CancelAutoFocus: true,
		OnConfirm:       func() { s.confirmDiscardFileManagerTabs() },
		OnCancel:        func() { s.cancelDiscardFileManagerTabs() },
	})
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
	s.fileTreeRenamePaths(oldPath, newPath)
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
	needRemember := false
	for _, p := range victims {
		empty, wasActive := st.closeFileManagerTabLocked(p)
		if empty || wasActive {
			needRemember = true
		}
	}
	fileManagerMu.Unlock()

	if needRemember {
		inspectorRemember(s)
	}
	if len(victims) > 0 {
		s.invalidate()
	}
}
