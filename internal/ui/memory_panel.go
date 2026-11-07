package mcodeui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

const (
	// MemoryPanelWidth/Height 为记忆面板弹窗的默认尺寸。
	MemoryPanelWidth  float32 = 680
	MemoryPanelHeight float32 = 600
	// memoryTabPending/Search/Working 是三个标签页的序号。
	memoryTabPending = 0
	memoryTabSearch  = 1
	memoryTabWorking = 2
	// memoryPendingStatus 是「待审」页过滤的状态。
	memoryPendingStatus = "pending"
	// memoryPendingBadgeInterval 是标题栏角标刷新待审数量的最小间隔（低频刷新，不每帧请求）。
	memoryPendingBadgeInterval = 30 * time.Second
	// memoryDefaultUnitType 是手动新建记忆的默认类型。
	memoryDefaultUnitType = "fact"
	// memorySimilarLimit 是详情区展示的相似记忆条数上限。
	memorySimilarLimit = 3
	// modalDragMemoryPanel/ModalDragMemoryCreate 是记忆面板与新建表单的拖拽 key。
	modalDragMemoryPanel  = "modal-drag-memory-panel"
	modalDragMemoryCreate = "modal-drag-memory-create"
)

var (
	memoryPanelMu     sync.Mutex
	memoryPanelStates = make(map[*chatState]*memoryPanelState)
)

// memoryPanelState 维护单个聊天窗口下的记忆面板状态。
// 提议列表来自本地表，搜索与 Working Memory 直接读 Nowledge Mem。
type memoryPanelState struct {
	open        bool
	tab         int
	projectPath string
	generation  uint64

	proposals  []MemoryProposal
	loading    bool
	distilling bool
	err        string
	busyID     string // 正在接受 / 拒绝的提议 ID
	expanded   string // 正在编辑的提议 ID

	editTitle      string
	editContent    string
	editUnitType   string
	editImportance string

	// 相似记忆：展开提议时按标题检索一次，结果最多 memorySimilarLimit 条，供选为合并目标。
	similarFor     string // 相似结果所属的提议 ID；与当前展开项不一致时结果作废
	similarItems   []MemoryItem
	similarLoading bool
	similarErr     string

	// AI 精简：把编辑草稿（可带上合并目标的正文）压缩得更短，只回填草稿，不自动保存。
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

	// 忘掉：第一次点击只进入确认态（forgetConfirmID），再点同一行才真正请求（forgettingID）。
	forgetConfirmID string
	forgettingID    string
	// searchNotice 是搜索页的操作结果提示（已删除 / 已标记废弃 / 已导出到某路径）。
	searchNotice string
	// exporting 表示正在导出记忆。
	exporting bool

	wmText    string
	wmLoading bool
	wmErr     string
	wmLoaded  bool

	pendingCount    int
	pendingCountAt  time.Time
	pendingFetching bool
}

// getMemoryPanelState 获取或初始化当前 chatState 的记忆面板状态；窗口已关闭时顺手清理。
func getMemoryPanelState(s *chatState) *memoryPanelState {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	memoryPanelMu.Lock()
	defer memoryPanelMu.Unlock()
	if closed {
		delete(memoryPanelStates, s)
		return nil
	}
	st, ok := memoryPanelStates[s]
	if !ok {
		st = &memoryPanelState{newUnitType: memoryDefaultUnitType}
		memoryPanelStates[s] = st
	}
	return st
}

// IsOpen 返回记忆面板当前是否显示。
func (st *memoryPanelState) IsOpen() bool {
	if st == nil {
		return false
	}
	memoryPanelMu.Lock()
	defer memoryPanelMu.Unlock()
	return st.open
}

// toggleMemoryPanel 切换记忆面板；打开时刷新待审列表并触发一次角标计数。
func (s *chatState) toggleMemoryPanel() {
	if s == nil {
		return
	}
	st := getMemoryPanelState(s)
	if st == nil {
		return
	}
	memoryPanelMu.Lock()
	if st.open {
		st.open = false
		memoryPanelMu.Unlock()
		s.invalidate()
		return
	}
	st.open = true
	st.err = ""
	memoryPanelMu.Unlock()
	s.invalidate()
	s.reloadMemoryProposals()
	s.refreshPendingMemoryCount(false)
}

// closeMemoryPanel 关闭面板（状态保留，下次打开仍在原标签页）。
func (s *chatState) closeMemoryPanel() {
	st := getMemoryPanelState(s)
	if st == nil {
		return
	}
	memoryPanelMu.Lock()
	st.open = false
	st.creating = false
	memoryPanelMu.Unlock()
	s.invalidate()
}

// switchMemoryTab 切换标签页；Working Memory 首次进入时才拉取正文。
func (s *chatState) switchMemoryTab(tab int) {
	st := getMemoryPanelState(s)
	if st == nil {
		return
	}
	memoryPanelMu.Lock()
	st.tab = tab
	// 离开搜索页时丢掉「忘掉」的确认态与操作提示。
	if tab != memoryTabSearch {
		st.forgetConfirmID = ""
		st.searchNotice = ""
	}
	needWM := tab == memoryTabWorking && !st.wmLoaded && !st.wmLoading
	memoryPanelMu.Unlock()
	if needWM {
		s.reloadWorkingMemory()
		return
	}
	s.invalidate()
}

// reloadMemoryProposals 异步刷新当前项目的待审提议列表。
func (s *chatState) reloadMemoryProposals() {
	st := getMemoryPanelState(s)
	if st == nil || s.client == nil {
		return
	}
	path := s.getSelectedProjectPath()
	memoryPanelMu.Lock()
	st.projectPath = path
	st.generation++
	gen := st.generation
	st.loading = path != ""
	st.err = ""
	if path == "" {
		st.proposals = nil
	}
	memoryPanelMu.Unlock()
	s.invalidate()
	if path == "" {
		return
	}
	go func() {
		items, err := s.client.MemoryProposals(path, memoryPendingStatus)
		memoryPanelMu.Lock()
		if st.generation == gen {
			st.loading = false
			if err != nil {
				st.err = err.Error()
			} else {
				st.proposals = items
				st.pendingCount = len(items)
				st.pendingCountAt = time.Now()
			}
		}
		memoryPanelMu.Unlock()
		s.invalidate()
	}()
}

// refreshPendingMemoryCount 低频刷新标题栏角标数量；force 为真时忽略间隔。
func (s *chatState) refreshPendingMemoryCount(force bool) {
	if s == nil || s.client == nil {
		return
	}
	st := getMemoryPanelState(s)
	if st == nil {
		return
	}
	memoryPanelMu.Lock()
	if st.pendingFetching || (!force && time.Since(st.pendingCountAt) < memoryPendingBadgeInterval) {
		memoryPanelMu.Unlock()
		return
	}
	st.pendingFetching = true
	memoryPanelMu.Unlock()

	path := s.getSelectedProjectPath()
	go func(p string) {
		n, err := s.client.CountPendingMemoryProposals(p)
		memoryPanelMu.Lock()
		st.pendingFetching = false
		st.pendingCountAt = time.Now()
		if err == nil {
			st.pendingCount = n
		}
		memoryPanelMu.Unlock()
		if err == nil {
			s.invalidate()
		}
	}(path)
}

// memoryPendingBadgeCount 读取缓存的待审数量（渲染用，不发起请求）。
func memoryPendingBadgeCount(s *chatState) int {
	st := getMemoryPanelState(s)
	if st == nil {
		return 0
	}
	memoryPanelMu.Lock()
	defer memoryPanelMu.Unlock()
	return st.pendingCount
}

// toggleMemoryProposalExpand 展开或收起某条提议的编辑区，并把当前值填进草稿。
func (s *chatState) toggleMemoryProposalExpand(id string) {
	st := getMemoryPanelState(s)
	if st == nil {
		return
	}
	memoryPanelMu.Lock()
	if st.expanded == id {
		st.expanded = ""
		st.similarFor = ""
		st.similarItems = nil
		st.similarLoading = false
		st.similarErr = ""
		st.condensing = false
		st.condenseErr = ""
		memoryPanelMu.Unlock()
		s.invalidate()
		return
	}
	st.expanded = id
	title := ""
	for _, p := range st.proposals {
		if p.ID == id {
			st.editTitle = p.Title
			st.editContent = p.Content
			st.editUnitType = p.UnitType
			st.editImportance = formatImportance(p.Importance)
			title = strings.TrimSpace(p.Title)
			break
		}
	}
	// 换一条提议展开时丢弃上一条的相似结果与精简状态，避免串用。
	st.similarFor = id
	st.similarItems = nil
	st.similarErr = ""
	st.similarLoading = s.client != nil && title != ""
	st.condensing = false
	st.condenseErr = ""
	path := st.projectPath
	memoryPanelMu.Unlock()
	s.invalidate()
	if path == "" {
		path = s.getSelectedProjectPath()
	}
	s.loadSimilarMemories(id, path, title)
}

// loadSimilarMemories 异步按标题检索相似的已有记忆，结果只保留前 memorySimilarLimit 条。
// 结果回来时若提议已收起或换成别的提议（st.similarFor 变化）就直接丢弃。
func (s *chatState) loadSimilarMemories(id, path, title string) {
	st := getMemoryPanelState(s)
	if st == nil || s.client == nil || id == "" || path == "" || title == "" {
		return
	}
	go func() {
		items, err := s.client.SearchMemories(path, title)
		memoryPanelMu.Lock()
		if st.similarFor == id {
			st.similarLoading = false
			if err != nil {
				st.similarErr = err.Error()
				st.similarItems = nil
			} else {
				if len(items) > memorySimilarLimit {
					items = items[:memorySimilarLimit]
				}
				st.similarItems = items
			}
		}
		memoryPanelMu.Unlock()
		s.invalidate()
	}()
}

// setMemoryProposalTarget 把提议设为改写某条已有记忆；targetID 为空表示取消合并目标。
// 成功后重新拉取列表，让卡片的 TargetMemoryID 与后端保持一致。
func (s *chatState) setMemoryProposalTarget(id, targetID string) {
	st := getMemoryPanelState(s)
	if st == nil || s.client == nil || id == "" {
		return
	}
	memoryPanelMu.Lock()
	if st.busyID != "" {
		memoryPanelMu.Unlock()
		return
	}
	st.busyID = id
	memoryPanelMu.Unlock()
	s.invalidate()

	go func() {
		err := s.client.SetMemoryProposalTarget(id, targetID)
		memoryPanelMu.Lock()
		if st.busyID == id {
			st.busyID = ""
		}
		if err != nil {
			st.err = err.Error()
		}
		memoryPanelMu.Unlock()
		s.invalidate()
		if err == nil {
			s.reloadMemoryProposals()
		}
	}()
}

// condenseMemoryDraft 把当前编辑草稿交给 AI 精简；已选合并目标时把该条记忆正文一并送去合并。
// 结果只写回草稿，不自动保存：用户仍需点「保存修改」。
func (s *chatState) condenseMemoryDraft() {
	st := getMemoryPanelState(s)
	if st == nil || s.client == nil {
		return
	}
	memoryPanelMu.Lock()
	id := st.expanded
	if id == "" || st.condensing {
		memoryPanelMu.Unlock()
		return
	}
	path := st.projectPath
	title := strings.TrimSpace(st.editTitle)
	content := st.editContent
	mergeWith := ""
	for _, p := range st.proposals {
		if p.ID != id || p.TargetMemoryID == "" {
			continue
		}
		for _, item := range st.similarItems {
			if item.ID == p.TargetMemoryID {
				mergeWith = strings.TrimSpace(item.Title) + "\n\n" + strings.TrimSpace(item.Content)
				break
			}
		}
		break
	}
	if title == "" && strings.TrimSpace(content) == "" {
		st.condenseErr = "标题和内容都是空的，没有可精简的内容"
		memoryPanelMu.Unlock()
		s.invalidate()
		return
	}
	st.condensing = true
	st.condenseErr = ""
	memoryPanelMu.Unlock()
	if path == "" {
		path = s.getSelectedProjectPath()
	}
	s.invalidate()

	go func(p, t, body, merge string) {
		newTitle, newContent, err := s.client.CondenseMemory(p, t, body, merge)
		memoryPanelMu.Lock()
		// 提议已切走时结果作废，不往别的草稿里写。
		if st.expanded == id {
			st.condensing = false
			if err != nil {
				st.condenseErr = err.Error()
			} else {
				// 等价于 setMemoryEdit("title"/"content")：只回填草稿，不自动保存。
				st.editTitle = newTitle
				st.editContent = newContent
			}
		}
		memoryPanelMu.Unlock()
		s.invalidate()
	}(path, title, content, mergeWith)
}

// formatImportance 把 0~1 的重要度格式化为一位小数，供输入框回填。
func formatImportance(v float64) string {
	if v <= 0 {
		return "0.5"
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// saveMemoryProposalEdits 保存展开区里的标题、内容、类型与重要度。
// 重要度解析失败时只更新其余字段，并在面板错误条里提示。
func (s *chatState) saveMemoryProposalEdits() {
	st := getMemoryPanelState(s)
	if st == nil || s.client == nil {
		return
	}
	memoryPanelMu.Lock()
	id := st.expanded
	p := MemoryProposal{ID: id}
	for _, item := range st.proposals {
		if item.ID == id {
			p = item
			break
		}
	}
	p.Title = strings.TrimSpace(st.editTitle)
	p.Content = st.editContent
	if unit := strings.TrimSpace(st.editUnitType); unit != "" {
		p.UnitType = unit
	}
	importance := parseImportance(st.editImportance, p.Importance)
	memoryPanelMu.Unlock()
	if id == "" {
		return
	}
	p.Importance = importance

	memoryPanelMu.Lock()
	st.busyID = id
	memoryPanelMu.Unlock()
	s.invalidate()

	go func(target MemoryProposal) {
		err := s.client.UpdateMemoryProposal(target)
		memoryPanelMu.Lock()
		if st.busyID == id {
			st.busyID = ""
		}
		if err != nil {
			st.err = err.Error()
		} else {
			for i := range st.proposals {
				if st.proposals[i].ID == id {
					st.proposals[i].Title = target.Title
					st.proposals[i].Content = target.Content
					st.proposals[i].UnitType = target.UnitType
					st.proposals[i].Importance = target.Importance
					break
				}
			}
		}
		memoryPanelMu.Unlock()
		s.invalidate()
	}(p)
}

// parseImportance 解析 0~1 的重要度输入，非法时保留原值。
func parseImportance(text string, fallback float64) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || v < 0 || v > 1 {
		return fallback
	}
	return v
}

// acceptMemoryProposal 异步接受一条提议并写入 Nowledge Mem。
// 成功时从待审列表移除；失败时把错误挂回该卡片（红色错误条）并保留在列表里。
func (s *chatState) acceptMemoryProposal(id string) {
	st := getMemoryPanelState(s)
	if st == nil || s.client == nil || id == "" {
		return
	}
	memoryPanelMu.Lock()
	if st.busyID != "" {
		memoryPanelMu.Unlock()
		return
	}
	st.busyID = id
	memoryPanelMu.Unlock()
	s.invalidate()

	go func() {
		p, err := s.client.AcceptMemoryProposal(id)
		memoryPanelMu.Lock()
		if st.busyID == id {
			st.busyID = ""
		}
		if err != nil {
			st.err = ""
			if p.ID != "" {
				replaced := false
				for i := range st.proposals {
					if st.proposals[i].ID == id {
						st.proposals[i] = p
						replaced = true
						break
					}
				}
				if !replaced {
					st.err = err.Error()
				}
			} else {
				st.err = err.Error()
			}
		} else {
			items := st.proposals[:0]
			for _, item := range st.proposals {
				if item.ID != id {
					items = append(items, item)
				}
			}
			st.proposals = items
			if st.pendingCount > 0 {
				st.pendingCount--
			}
			st.pendingCountAt = time.Now()
		}
		memoryPanelMu.Unlock()
		s.invalidate()
	}()
}

// 提议的写入范围取值，与 store.MemoryProposalScope* 一致。
const (
	memoryScopeProject = "project"
	memoryScopeGlobal  = "global"
)

// toggleMemoryProposalScope 在项目空间与全局空间之间切换一条提议的写入范围。
func (s *chatState) toggleMemoryProposalScope(id string) {
	st := getMemoryPanelState(s)
	if st == nil || s.client == nil || id == "" {
		return
	}
	memoryPanelMu.Lock()
	if st.busyID != "" {
		memoryPanelMu.Unlock()
		return
	}
	var (
		p     MemoryProposal
		found bool
	)
	for _, item := range st.proposals {
		if item.ID == id {
			p, found = item, true
			break
		}
	}
	if !found {
		memoryPanelMu.Unlock()
		return
	}
	st.busyID = id
	memoryPanelMu.Unlock()
	if p.Scope == memoryScopeGlobal {
		p.Scope = memoryScopeProject
	} else {
		p.Scope = memoryScopeGlobal
	}
	s.invalidate()

	go func() {
		err := s.client.UpdateMemoryProposal(p)
		memoryPanelMu.Lock()
		if st.busyID == id {
			st.busyID = ""
		}
		if err != nil {
			st.err = err.Error()
		} else {
			for i := range st.proposals {
				if st.proposals[i].ID == id {
					st.proposals[i].Scope = p.Scope
					break
				}
			}
		}
		memoryPanelMu.Unlock()
		s.invalidate()
	}()
}

// rejectMemoryProposal 异步驳回一条提议：只改本地状态，不写入远端。
func (s *chatState) rejectMemoryProposal(id string) {
	st := getMemoryPanelState(s)
	if st == nil || s.client == nil || id == "" {
		return
	}
	memoryPanelMu.Lock()
	if st.busyID != "" {
		memoryPanelMu.Unlock()
		return
	}
	st.busyID = id
	memoryPanelMu.Unlock()
	s.invalidate()

	go func() {
		err := s.client.RejectMemoryProposal(id)
		memoryPanelMu.Lock()
		if st.busyID == id {
			st.busyID = ""
		}
		if err != nil {
			st.err = err.Error()
		} else {
			items := st.proposals[:0]
			for _, item := range st.proposals {
				if item.ID != id {
					items = append(items, item)
				}
			}
			st.proposals = items
			if st.pendingCount > 0 {
				st.pendingCount--
			}
			st.pendingCountAt = time.Now()
		}
		memoryPanelMu.Unlock()
		s.invalidate()
	}()
}

// requestCreateMemoryProposal 打开「新建记忆」表单。
func (s *chatState) requestCreateMemoryProposal() {
	st := getMemoryPanelState(s)
	if st == nil {
		return
	}
	memoryPanelMu.Lock()
	st.creating = true
	st.newTitle, st.newContent, st.newUnitType = "", "", memoryDefaultUnitType
	memoryPanelMu.Unlock()
	s.invalidate()
}

// cancelCreateMemoryProposal 关闭「新建记忆」表单。
func (s *chatState) cancelCreateMemoryProposal() {
	st := getMemoryPanelState(s)
	if st == nil {
		return
	}
	memoryPanelMu.Lock()
	st.creating = false
	memoryPanelMu.Unlock()
	s.invalidate()
}

// createMemoryProposal 手动新建一条记忆提议（来源记为 user，仍需本人接受后才写入）。
func (s *chatState) createMemoryProposal() {
	st := getMemoryPanelState(s)
	if st == nil || s.client == nil {
		return
	}
	memoryPanelMu.Lock()
	path := st.projectPath
	title, content, unitType := st.newTitle, st.newContent, st.newUnitType
	memoryPanelMu.Unlock()
	if path == "" {
		path = s.getSelectedProjectPath()
	}
	if strings.TrimSpace(title) == "" {
		memoryPanelMu.Lock()
		st.err = "记忆标题不能为空"
		memoryPanelMu.Unlock()
		s.invalidate()
		return
	}
	s.mu.Lock()
	threadID := s.selectedThreadID
	s.mu.Unlock()

	go func() {
		p, err := s.client.CreateMemoryProposal(path, threadID, title, content)
		// 类型在设置里手填时补一次更新，成功与否都继续把提议放进列表。
		if err == nil {
			if unit := strings.TrimSpace(unitType); unit != "" && unit != p.UnitType {
				p.UnitType = unit
				if uerr := s.client.UpdateMemoryProposal(p); uerr != nil {
					err = uerr
				}
			}
		}
		memoryPanelMu.Lock()
		if err != nil {
			st.err = err.Error()
			if p.ID == "" {
				memoryPanelMu.Unlock()
				s.invalidate()
				return
			}
		}
		if p.ID != "" {
			st.proposals = append([]MemoryProposal{p}, st.proposals...)
			st.pendingCount++
			st.pendingCountAt = time.Now()
			st.creating = false
			st.expanded = p.ID
			st.editTitle, st.editContent = p.Title, p.Content
			st.editUnitType = p.UnitType
			st.editImportance = formatImportance(p.Importance)
			// 新建后立即展开，顺带检索相似记忆（与手动展开保持一致）。
			st.similarFor = p.ID
			st.similarItems = nil
			st.similarErr = ""
			st.similarLoading = strings.TrimSpace(p.Title) != ""
			st.condensing = false
			st.condenseErr = ""
		}
		newID, newTitle := p.ID, p.Title
		memoryPanelMu.Unlock()
		if newID != "" {
			s.loadSimilarMemories(newID, path, strings.TrimSpace(newTitle))
		}
		s.invalidate()
	}()
}

// editMemorySearchQuery 记录搜索关键字。
func (s *chatState) editMemorySearchQuery(query string) {
	st := getMemoryPanelState(s)
	if st == nil {
		return
	}
	memoryPanelMu.Lock()
	st.searchQuery = query
	memoryPanelMu.Unlock()
}

// searchMemories 异步检索记忆；结果写入面板状态后回到 UI 线程重绘。
func (s *chatState) searchMemories() {
	st := getMemoryPanelState(s)
	if st == nil || s.client == nil {
		return
	}
	memoryPanelMu.Lock()
	// 重新搜索时丢掉上一轮的确认态与操作提示，避免落到新结果上。
	st.forgetConfirmID = ""
	st.searchNotice = ""
	query := strings.TrimSpace(st.searchQuery)
	path := st.projectPath
	if query == "" {
		st.searchErr = "请输入搜索内容"
		st.searchResults = nil
		memoryPanelMu.Unlock()
		s.invalidate()
		return
	}
	st.searchErr = ""
	st.searchLoading = true
	st.searched = true
	memoryPanelMu.Unlock()
	if path == "" {
		path = s.getSelectedProjectPath()
		memoryPanelMu.Lock()
		st.projectPath = path
		memoryPanelMu.Unlock()
	}
	s.invalidate()

	go func(p, q string) {
		items, err := s.client.SearchMemories(p, q)
		memoryPanelMu.Lock()
		st.searchLoading = false
		if err != nil {
			st.searchErr = err.Error()
			st.searchResults = nil
		} else {
			st.searchResults = items
		}
		memoryPanelMu.Unlock()
		s.invalidate()
	}(path, query)
}

// requestForgetMemory 处理搜索结果行里「忘掉」的点击：第一次只进入确认态，
// 再点同一行才真正发起请求；点别的行则把确认态切到那一行。
func (s *chatState) requestForgetMemory(id string) {
	st := getMemoryPanelState(s)
	if st == nil || s.client == nil || id == "" {
		return
	}
	memoryPanelMu.Lock()
	if st.forgettingID != "" {
		memoryPanelMu.Unlock()
		return
	}
	if st.forgetConfirmID == id {
		memoryPanelMu.Unlock()
		s.confirmForgetMemory(id)
		return
	}
	st.forgetConfirmID = id
	st.searchNotice = ""
	st.searchErr = ""
	memoryPanelMu.Unlock()
	s.invalidate()
}

// cancelForgetMemory 退出「忘掉」的确认态，不发起任何请求。
func (s *chatState) cancelForgetMemory() {
	st := getMemoryPanelState(s)
	if st == nil {
		return
	}
	memoryPanelMu.Lock()
	st.forgetConfirmID = ""
	memoryPanelMu.Unlock()
	s.invalidate()
}

// confirmForgetMemory 在后台忘掉一条记忆：真删除或标记废弃都从当前结果里移除该行，
// 并按服务端能力给出提示；出错时写到搜索页错误条。
func (s *chatState) confirmForgetMemory(id string) {
	st := getMemoryPanelState(s)
	if st == nil || s.client == nil || id == "" {
		return
	}
	memoryPanelMu.Lock()
	if st.forgettingID != "" {
		memoryPanelMu.Unlock()
		return
	}
	st.forgettingID = id
	st.forgetConfirmID = ""
	st.searchErr = ""
	st.searchNotice = ""
	path := st.projectPath
	memoryPanelMu.Unlock()
	if path == "" {
		path = s.getSelectedProjectPath()
		memoryPanelMu.Lock()
		st.projectPath = path
		memoryPanelMu.Unlock()
	}
	s.invalidate()

	go func(p, target string) {
		deleted, err := s.client.ForgetMemory(p, target)
		memoryPanelMu.Lock()
		st.forgettingID = ""
		if err != nil {
			st.searchErr = err.Error()
		} else {
			items := st.searchResults[:0]
			for _, item := range st.searchResults {
				if item.ID != target {
					items = append(items, item)
				}
			}
			st.searchResults = items
			if deleted {
				st.searchNotice = "已删除这条记忆"
			} else {
				st.searchNotice = "服务端不支持删除，已标记为已废弃，之后不会再被检索到"
			}
		}
		memoryPanelMu.Unlock()
		s.invalidate()
	}(path, id)
}

// saveMemoryExportDialog 是可注入的记忆导出保存对话框，测试时替换以避免弹出真实窗口。
var saveMemoryExportDialog func(defaultPath string) (string, error) = defaultSaveMemoryExportDialog

// defaultSaveMemoryExportDialog 唤起原生保存对话框，只允许 Markdown；取消时路径为空。
func defaultSaveMemoryExportDialog(defaultPath string) (string, error) {
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Title:       "导出记忆",
		DefaultPath: defaultPath,
		ButtonLabel: "导出",
		Filters: []mygo.FileFilter{
			{Name: "Markdown", Extensions: []string{"md"}},
		},
		CreateDirectories: true,
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

// memoryExportFileName 给出记忆导出文件的默认名：mcode-memories-YYYYMMDD-HHMMSS.md。
func memoryExportFileName(now time.Time) string {
	return "mcode-memories-" + now.Format("20060102-150405") + ".md"
}

// exportMemories 在后台把项目空间记忆导出成 Markdown：
// 先取正文，再走记忆专用的保存对话框选路径，最后写盘并提示保存位置。
func (s *chatState) exportMemories() {
	st := getMemoryPanelState(s)
	if st == nil || s.client == nil {
		return
	}
	memoryPanelMu.Lock()
	if st.exporting {
		memoryPanelMu.Unlock()
		return
	}
	st.exporting = true
	st.searchErr = ""
	st.searchNotice = ""
	path := st.projectPath
	memoryPanelMu.Unlock()
	if path == "" {
		path = s.getSelectedProjectPath()
		memoryPanelMu.Lock()
		st.projectPath = path
		memoryPanelMu.Unlock()
	}
	s.invalidate()

	go func(p string) {
		// 无论成功、取消还是失败，结束时都复位按钮并刷新界面。
		defer s.invalidate()
		setErr := func(msg string) {
			memoryPanelMu.Lock()
			st.exporting = false
			st.searchErr = msg
			memoryPanelMu.Unlock()
		}
		setNotice := func(msg string) {
			memoryPanelMu.Lock()
			st.exporting = false
			st.searchNotice = msg
			memoryPanelMu.Unlock()
		}

		body, err := s.client.ExportMemories(p)
		if err != nil {
			setErr("导出失败: " + err.Error())
			return
		}
		saver := saveMemoryExportDialog
		if saver == nil {
			saver = defaultSaveMemoryExportDialog
		}
		target, err := saver(filepath.Join(downloadsDir(), memoryExportFileName(time.Now())))
		if err != nil {
			setErr("导出失败: " + err.Error())
			return
		}
		if target == "" {
			// 用户取消，静默返回，只复位按钮。
			setNotice("")
			return
		}
		if dir := filepath.Dir(target); dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				setErr("导出失败: " + err.Error())
				return
			}
		}
		if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
			setErr("导出失败: " + err.Error())
			return
		}
		setNotice("已导出到 " + target)
	}(path)
}

// reloadWorkingMemory 异步拉取 Working Memory 正文。
func (s *chatState) reloadWorkingMemory() {
	st := getMemoryPanelState(s)
	if st == nil || s.client == nil {
		return
	}
	path := s.getSelectedProjectPath()
	memoryPanelMu.Lock()
	st.projectPath = path
	st.wmLoading = true
	st.wmErr = ""
	memoryPanelMu.Unlock()
	s.invalidate()

	go func(p string) {
		text, err := s.client.WorkingMemory(p)
		memoryPanelMu.Lock()
		st.wmLoading = false
		st.wmLoaded = true
		if err != nil {
			st.wmErr = err.Error()
		} else {
			st.wmText = text
		}
		memoryPanelMu.Unlock()
		s.invalidate()
	}(path)
}

// dismissMemoryPanelError 关闭面板顶部错误条。
func (s *chatState) dismissMemoryPanelError() {
	st := getMemoryPanelState(s)
	if st == nil {
		return
	}
	memoryPanelMu.Lock()
	st.err = ""
	memoryPanelMu.Unlock()
	s.invalidate()
}

// setMemoryEdit 写入展开编辑区的字段（field 取 title / content / unit / importance）。
func (s *chatState) setMemoryEdit(field, value string) {
	st := getMemoryPanelState(s)
	if st == nil {
		return
	}
	memoryPanelMu.Lock()
	switch field {
	case "title":
		st.editTitle = value
	case "content":
		st.editContent = value
	case "unit":
		st.editUnitType = value
	case "importance":
		st.editImportance = value
	}
	memoryPanelMu.Unlock()
}

// setMemoryNewField 写入「新建记忆」表单的字段（field 取 title / content / unit）。
func (s *chatState) setMemoryNewField(field, value string) {
	st := getMemoryPanelState(s)
	if st == nil {
		return
	}
	memoryPanelMu.Lock()
	switch field {
	case "title":
		st.newTitle = value
	case "content":
		st.newContent = value
	case "unit":
		st.newUnitType = value
	}
	memoryPanelMu.Unlock()
}

// distillCurrentThread 从当前会话提炼候选记忆并放进待审收件箱。
func (s *chatState) distillCurrentThread() {
	if s == nil || s.client == nil || s.client.MemoryBackend() == "local" {
		return
	}
	st := getMemoryPanelState(s)
	if st == nil {
		return
	}
	path := s.getSelectedProjectPath()
	s.mu.Lock()
	threadID := s.selectedThreadID
	s.mu.Unlock()
	if path == "" || threadID == "" {
		return
	}

	memoryPanelMu.Lock()
	if st.distilling {
		memoryPanelMu.Unlock()
		return
	}
	st.distilling = true
	st.err = ""
	memoryPanelMu.Unlock()
	s.invalidate()

	go func() {
		added, err := s.client.DistillThreadMemories(path, threadID)
		memoryPanelMu.Lock()
		st.distilling = false
		if err != nil {
			st.err = err.Error()
		} else if added == 0 {
			st.err = "这个会话没有提炼出新的记忆"
		}
		memoryPanelMu.Unlock()
		if err == nil && added > 0 {
			s.reloadMemoryProposals()
			s.refreshPendingMemoryCount(true)
		}
		s.invalidate()
	}()
}
