package mcodeui

import (
	"math"
	"time"

	"github.com/egoist/mygo"
)

const (
	inspectorMinWidth     float32 = 320
	inspectorDefaultWidth float32 = 480
	inspectorMaxWidth     float32 = 800
	inspectorChatMinWidth float32 = 360
)

// inspectorState 的槽表由 chatState.mu 保护；布局、页面及编辑器由 UI 线程拥有。
type inspectorState struct {
	Sessions    map[string]*inspectorSessionState
	Temporary   *inspectorSessionState
	Preferences map[string]InspectorPreference
	// 空会话不触发回调；nil 状态表示显式删除偏好。
	Persist func(string, *inspectorSessionState)
}

type inspectorSessionState struct {
	Open           bool
	Active         string
	generation     uint64
	Width          float32
	Files          *fileManagerState
	Review         *diffPanelState
	Git            *gitPaneState
	Browser        *previewState
	follow         *inspectorFollowState
	workspace      string
	workspaceKnown bool
}

func inspectorSession(s *chatState) *inspectorSessionState {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	return inspectorSessionLocked(s, s.selectedThreadID)
}

func inspectorSessionLocked(s *chatState, threadID string) *inspectorSessionState {
	var slot **inspectorSessionState
	if threadID == "" {
		slot = &s.inspector.Temporary
	} else {
		if s.inspector.Sessions == nil {
			s.inspector.Sessions = make(map[string]*inspectorSessionState)
		}
		if st := s.inspector.Sessions[threadID]; st != nil {
			return st
		}
	}
	st := &inspectorSessionState{
		Active: sidePaneFileID, generation: 1, Width: inspectorInitialWidth(s),
		Files:   &fileManagerState{},
		Review:  &diffPanelState{owner: s, collapsed: make(map[string]bool)},
		Browser: &previewState{lastUsed: time.Now()},
	}
	if pref, ok := s.inspector.Preferences[threadID]; threadID != "" && ok {
		pref = normalizeInspectorPreference(pref)
		st.Open, st.Active, st.Width = pref.Open, pref.Active, pref.Width
		fileTreeSeedPreference(st.Files, pref)
	}
	if slot != nil {
		if *slot != nil {
			return *slot
		}
		*slot = st
	} else {
		s.inspector.Sessions[threadID] = st
	}
	return st
}

func inspectorInitialWidth(s *chatState) float32 {
	sidePanePrefsMu.Lock()
	defer sidePanePrefsMu.Unlock()
	for _, id := range []string{sidePanePreviewID, sidePaneFileID, sidePaneDiffID, sidePaneGitID} {
		if w := sidePanePrefs[s][id]; w > 0 && !math.IsNaN(float64(w)) && !math.IsInf(float64(w), 0) {
			return min(max(w, inspectorMinWidth), inspectorMaxWidth)
		}
	}
	return inspectorDefaultWidth
}

func inspectorPaneOpen(s *chatState, id string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := inspectorExistingSessionLocked(s, s.selectedThreadID)
	return st != nil && st.Open && st.Active == id
}

func inspectorExistingSessionLocked(s *chatState, threadID string) *inspectorSessionState {
	if threadID == "" {
		return s.inspector.Temporary
	}
	return s.inspector.Sessions[threadID]
}

// inspectorSelect 的 toggle 只用于旧入口；顶部标签始终只选择。
func inspectorSelect(s *chatState, id string, toggle bool) bool {
	st := inspectorSession(s)
	if st == nil {
		return false
	}
	s.mu.Lock()
	wasBrowser := st.Open && st.Active == sidePanePreviewID
	wasOpen, wasActive := st.Open, st.Active
	st.Open = !(toggle && st.Open && st.Active == id)
	st.Active = id
	if st.Open != wasOpen || st.Active != wasActive {
		st.generation++
	}
	open := st.Open
	s.mu.Unlock()
	if wasBrowser && (!open || id != sidePanePreviewID) {
		hidePreviewState(s, st.Browser)
		browserServerDiscoveryStop(s)
	}
	inspectorSyncVisibility(st)
	inspectorRemember(s)
	s.invalidate()
	return open
}

func inspectorClose(s *chatState) {
	st := inspectorSession(s)
	if st == nil {
		return
	}
	s.mu.Lock()
	if st.Open {
		st.generation++
	}
	st.Open = false
	s.mu.Unlock()
	hidePreviewState(s, st.Browser)
	browserServerDiscoveryStop(s)
	inspectorSyncVisibility(st)
	inspectorRemember(s)
	s.invalidate()
}

// 旧聊天头部仍读取 visible；它只作容器可见性的兼容镜像。
func inspectorSyncVisibility(st *inspectorSessionState) {
	previewMu.Lock()
	st.Browser.visible = st.Open && st.Active == sidePanePreviewID
	browserActiveTabLocked(st.Browser).visible = st.Browser.visible
	previewMu.Unlock()
	fileTreeWatchSetVisible(st.Files, st.Open && st.Active == sidePaneFileID)
	if st.Git != nil {
		st.Git.setVisible(st.Open && st.Active == sidePaneGitID)
	}
}

func inspectorRemember(s *chatState) {
	s.mu.Lock()
	tid, save := s.selectedThreadID, s.inspector.Persist
	st := inspectorExistingSessionLocked(s, tid)
	s.mu.Unlock()
	if tid != "" && save != nil && st != nil {
		save(tid, st)
	}
}

func inspectorPersistSession(s *chatState, tid string, st *inspectorSessionState) {
	if tid == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	var fileExpanded []string
	var fileActive, fileAnchor string
	if st != nil {
		fileManagerMu.Lock()
		fileExpanded, fileActive, fileAnchor = fileTreePreferenceLocked(st.Files)
		fileManagerMu.Unlock()
	}
	change := pendingInspectorPreference{Delete: st == nil}
	if st == nil {
		delete(s.inspector.Preferences, tid)
	} else {
		// 删除后到达的旧槽回调不得重新创建偏好。
		if s.inspector.Sessions[tid] != st {
			return
		}
		change.Preference = normalizeInspectorPreference(InspectorPreference{
			Open: st.Open, Active: st.Active, Width: st.Width,
			FileExpanded: fileExpanded, FileActive: fileActive, FileAnchor: fileAnchor,
		})
		if s.inspector.Preferences == nil {
			s.inspector.Preferences = make(map[string]InspectorPreference)
		}
		s.inspector.Preferences[tid] = change.Preference
	}
	if uiStatePersistenceEnabled(s) {
		persistInspectorPreference(tid, change)
	}
}

// 仅在完整会话列表加载成功后清理，部分加载失败时不能判定会话已删除。
func inspectorPrunePreferences(s *chatState) {
	s.mu.Lock()
	existing := make(map[string]bool, len(s.threads))
	for _, th := range s.threads {
		existing[th.ID] = true
	}
	var stale []string
	for tid := range s.inspector.Preferences {
		if !existing[tid] {
			stale = append(stale, tid)
		}
	}
	s.mu.Unlock()
	for _, tid := range stale {
		inspectorDeleteSession(s, tid)
	}
}

// hidePreviewState 保留页面与已选标注；换代后旧轮询不能回写。
func hidePreviewState(s *chatState, st *previewState) {
	if st == nil {
		return
	}
	previewMu.Lock()
	st.visible = false
	tab := browserActiveTabLocked(st)
	previewMu.Unlock()
	hideBrowserTab(s, tab)
}

func hideBrowserTab(s *chatState, st *browserTab) {
	if st == nil {
		return
	}
	previewMu.Lock()
	page, off, picking := st.page, st.pageOff, st.annotating
	annotations, annotOpen := st.annotations, st.annotOpen
	errors, consoleOpen := st.consoleErrors, st.consoleOpen
	previewAdvanceGenerationLocked(st)
	st.annotations, st.annotOpen = annotations, annotOpen
	st.consoleErrors, st.consoleOpen = errors, consoleOpen
	st.visible = false
	st.pageOff = nil
	st.consoleLastPoll = time.Time{}
	token := previewPageTokenLocked(st)
	previewMu.Unlock()
	if off != nil {
		off()
	}
	if page != nil && picking {
		_, _ = page.Eval(previewAnnotateStopScript)
	}
	if events, ok := page.(previewPageEvents); ok {
		previewBindPageCallbacks(s, token, events)
	}
}

func inspectorSwitchThread(s *chatState, oldID string) {
	s.mu.Lock()
	old := inspectorExistingSessionLocked(s, oldID)
	current := inspectorExistingSessionLocked(s, s.selectedThreadID)
	if old != nil {
		old.generation++
		inspectorFollowStopLocked(old)
	}
	if current != nil && current != old {
		current.generation++
	}
	s.mu.Unlock()
	if old != nil {
		browserServerDiscoveryStop(s)
		if old.Git != nil {
			old.Git.setVisible(false)
		}
		fileTreeWatchSetVisible(old.Files, false)
		fileManagerMu.Lock()
		old.Files.opsSeq++
		old.Files.ops = fileManagerOpsDialog{}
		fileManagerMu.Unlock()
		if s.window != nil {
			mygo.RunOnMain(func() { hidePreviewState(s, old.Browser) })
		} else {
			hidePreviewState(s, old.Browser)
		}
	}
	if current != nil {
		inspectorSyncVisibility(current)
		previewMu.Lock()
		current.Browser.lastUsed = time.Now()
		previewMu.Unlock()
	}
	if s.window != nil {
		mygo.RunOnMain(func() { browserEnforceSessionLimit(s) })
	} else {
		browserEnforceSessionLimit(s)
	}
	inspectorValidateWorkspace(s)
}

// 同一个会话换 root/main 后作废缓存；AI worktree 变化由改动面板自行处理。
func inspectorValidateWorkspace(s *chatState) {
	st := inspectorSession(s)
	if st == nil {
		return
	}
	_, root, main := getSelectedThreadDirs(s)
	identity := root + "\x00" + main
	s.mu.Lock()
	changed := st.workspaceKnown && st.workspace != identity
	s.mu.Unlock()
	if changed {
		if st.Git != nil {
			st.Git.dispose()
			st.Git = nil
		}
		inspectorFollowReset(s, st)
		fileManagerMu.Lock()
		st.Files.gen++
		st.Files.searchGen++
		for _, tab := range st.Files.tabs {
			tab.gen++
		}
		oldFiles := st.Files
		st.Files = &fileManagerState{}
		fileManagerMu.Unlock()
		fileTreeWatchDispose(oldFiles)
		diffPanelMu.Lock()
		st.Review.requestGen++
		st.Review = &diffPanelState{owner: s, collapsed: make(map[string]bool)}
		diffPanelMu.Unlock()
	}
	s.mu.Lock()
	if !st.workspaceKnown || st.workspace != identity {
		st.generation++
	}
	st.workspace, st.workspaceKnown = identity, true
	s.mu.Unlock()
	inspectorFileWatchTick(s, st)
}

func inspectorDisposeSession(s *chatState, st *inspectorSessionState) {
	if st == nil {
		return
	}
	if st.Git != nil {
		st.Git.dispose()
	}
	s.mu.Lock()
	st.generation++
	s.mu.Unlock()
	inspectorFollowReset(s, st)
	browserServerDiscoveryDispose(s)
	fileTreeWatchDispose(st.Files)
	fileManagerMu.Lock()
	st.Files.gen++
	st.Files.searchGen++
	for _, tab := range st.Files.tabs {
		tab.gen++
	}
	fileManagerMu.Unlock()
	diffPanelMu.Lock()
	st.Review.requestGen++
	diffPanelMu.Unlock()
	disposePreviewState(s, st.Browser)
}

func inspectorDeleteSession(s *chatState, tid string) {
	s.mu.Lock()
	st := s.inspector.Sessions[tid]
	delete(s.inspector.Sessions, tid)
	save := s.inspector.Persist
	s.mu.Unlock()
	if tid != "" && save != nil {
		save(tid, nil)
	}
	if s.window != nil {
		mygo.RunOnMain(func() { inspectorDisposeSession(s, st) })
	} else {
		inspectorDisposeSession(s, st)
	}
}

func inspectorSuspend(s *chatState) {
	s.mu.Lock()
	st := inspectorExistingSessionLocked(s, s.selectedThreadID)
	s.mu.Unlock()
	if st == nil {
		return
	}
	if st.Git != nil {
		st.Git.setVisible(false)
	}
	previewMu.Lock()
	visible := st.Browser.visible
	previewMu.Unlock()
	if visible {
		hidePreviewState(s, st.Browser)
	}
}

func inspectorDisposeAllSessions(s *chatState) {
	s.mu.Lock()
	states := s.inspector.Sessions
	temporary := s.inspector.Temporary
	s.inspector = inspectorState{}
	s.mu.Unlock()
	for _, st := range states {
		inspectorDisposeSession(s, st)
	}
	inspectorDisposeSession(s, temporary)
}

func inspectorLayout(available, preferred float32) (overlay bool, width float32) {
	width = min(max(preferred, inspectorMinWidth), inspectorMaxWidth)
	if available < inspectorChatMinWidth+sidePaneHandleWidth+inspectorMinWidth {
		return true, max(0, min(width, available))
	}
	return false, min(width, available-inspectorChatMinWidth-sidePaneHandleWidth)
}
