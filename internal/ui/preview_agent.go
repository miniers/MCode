package mcodeui

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
)

// OpenPreviewURLInNewTabForThread 在当前正在显示指定会话的桌面窗口右侧浏览器新标签页打开本机回环 URL。
// 会话未在任何桌面窗口显示时直接返回错误，不暂存。
func OpenPreviewURLInNewTabForThread(threadID, rawURL string) error {
	threadID = strings.TrimSpace(threadID)
	rawURL = strings.TrimSpace(rawURL)
	if threadID == "" || rawURL == "" {
		return errors.New("会话或预览 URL 不能为空")
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.User != nil || u.Host == "" {
		return errors.New("无效的预览 URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return errors.New("仅支持 http 或 https 本机回环地址")
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
	default:
		return errors.New("仅允许打开本机回环地址（localhost、127.0.0.1、::1）")
	}
	if !HasChatWindow() {
		return errors.New("桌面窗口未打开")
	}

	chatWindowsMu.RLock()
	entries := make([]previewWindowEntry, 0, len(chatWindows))
	for win, state := range chatWindows {
		if state != nil {
			entries = append(entries, previewWindowEntry{win: win, state: state})
		}
	}
	chatWindowsMu.RUnlock()

	var focused *mygo.Window
	if len(entries) > 1 {
		focused = mygo.FocusedWindow()
	}
	s := previewWindowViewing(entries, focused, threadID)
	if s == nil {
		if leadID, _ := swarmLeadForPreviewBee(entries, focused, threadID); leadID != "" {
			s = previewWindowViewing(entries, focused, leadID)
		}
	}
	if s == nil || s.isClosed() || s.window == nil {
		return errors.New("会话当前未在桌面窗口显示")
	}
	targetURL := u.String()
	mygo.RunOnMain(func() {
		if s.isClosed() {
			return
		}
		openPreviewURLInNewTab(s, targetURL)
	})
	return nil
}

// OpenPreviewForThread 在拥有指定会话的桌面窗口中打开右侧浏览器预览。
// 目标会话正在显示时立即在主线程消费，否则暂存到会话槽中，切回该会话时再打开。
func OpenPreviewForThread(threadID, target string) error {
	threadID = strings.TrimSpace(threadID)
	target = strings.TrimSpace(target)
	if threadID == "" || target == "" {
		return errors.New("会话或预览目标不能为空")
	}
	if !HasChatWindow() {
		return errors.New("桌面窗口未打开")
	}
	s, slotThreadID := resolvePreviewTargetWindow(threadID)
	if s == nil {
		return errors.New("桌面窗口未打开")
	}
	if !isPreviewHTTPURL(target) {
		var err error
		target, err = resolveAgentPreviewTarget(turnFilesWorkDir(s, threadID), target)
		if err != nil {
			return err
		}
	}
	ok, isCurrent := setPendingPreviewFor(s, slotThreadID, threadID, target)
	if !ok {
		return errors.New("桌面窗口未打开")
	}
	if isCurrent && s.window != nil {
		mygo.RunOnMain(func() {
			consumePendingPreview(s, slotThreadID)
		})
	}
	return nil
}

func resolveAgentPreviewTarget(workDir, target string) (string, error) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return "", errors.New("会话没有工作目录")
	}
	path := filepath.FromSlash(strings.TrimSpace(target))
	if path == "" {
		return "", errors.New("只能预览工作区内的 HTML 文件")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}
	rel, ok := chatLinkHTMLPreviewPath(path, workDir, 0)
	if !ok {
		return "", errors.New("只能预览工作区内的 HTML 文件")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("文件不存在")
	}
	return rel, nil
}

func (s *chatState) isClosed() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

type previewWindowEntry struct {
	win   *mygo.Window
	state *chatState
}

func previewWindowViewing(entries []previewWindowEntry, focused *mygo.Window, threadID string) *chatState {
	var matched *chatState
	for _, entry := range entries {
		if entry.state.isClosed() || entry.state.chatSelectedThreadID() != threadID {
			continue
		}
		if focused != nil && entry.win == focused {
			return entry.state
		}
		if matched == nil {
			matched = entry.state
		}
	}
	return matched
}

func swarmLeadForPreviewBee(entries []previewWindowEntry, focused *mygo.Window, beeID string) (leadID string, owner *chatState) {
	if beeID == "" || len(entries) == 0 {
		return "", nil
	}
	ordered := make([]previewWindowEntry, 0, len(entries))
	for _, entry := range entries {
		if focused != nil && entry.win == focused {
			ordered = append(ordered, entry)
		}
	}
	for _, entry := range entries {
		if focused == nil || entry.win != focused {
			ordered = append(ordered, entry)
		}
	}
	for _, entry := range ordered {
		if entry.state.isClosed() {
			continue
		}
		swarmPanelMu.Lock()
		hp := swarmPanelStates[entry.state]
		var foundLead string
		if hp != nil && !hp.state.IsBee {
			lid := strings.TrimSpace(hp.state.LeadThreadID)
			if lid == "" {
				lid = strings.TrimSpace(hp.loadedFor)
			}
			if lid != "" && lid != beeID {
				for _, h := range hp.state.Bees {
					if strings.TrimSpace(h.ThreadID) == beeID {
						foundLead = lid
						break
					}
				}
			}
		}
		swarmPanelMu.Unlock()
		if foundLead != "" {
			return foundLead, entry.state
		}
	}
	for _, entry := range ordered {
		if entry.state.isClosed() {
			continue
		}
		entry.state.mu.Lock()
		var foundLead string
		for _, th := range entry.state.threads {
			if th.ID == beeID && strings.TrimSpace(th.ParentThreadID) != "" && th.ParentThreadID != beeID {
				foundLead = strings.TrimSpace(th.ParentThreadID)
				break
			}
		}
		entry.state.mu.Unlock()
		if foundLead != "" {
			return foundLead, entry.state
		}
	}
	return "", nil
}

// resolvePreviewTargetWindow 优先返回正在查看 threadID 的窗口与会话槽；
// 若没有窗口显示它且它是某主会话的 bee，改为投递到显示其主会话的窗口（与主会话槽）。
func resolvePreviewTargetWindow(threadID string) (s *chatState, slotThreadID string) {
	chatWindowsMu.RLock()
	entries := make([]previewWindowEntry, 0, len(chatWindows))
	for win, state := range chatWindows {
		if state != nil {
			entries = append(entries, previewWindowEntry{win: win, state: state})
		}
	}
	chatWindowsMu.RUnlock()

	var focused *mygo.Window
	if len(entries) > 1 {
		focused = mygo.FocusedWindow()
	}
	if matched := previewWindowViewing(entries, focused, threadID); matched != nil {
		return matched, threadID
	}
	if leadID, fallback := swarmLeadForPreviewBee(entries, focused, threadID); leadID != "" {
		if matched := previewWindowViewing(entries, focused, leadID); matched != nil {
			return matched, leadID
		}
		if active := activeChatState(); active != nil {
			return active, leadID
		}
		return fallback, leadID
	}
	return activeChatState(), threadID
}

func setPendingPreview(s *chatState, threadID, target string) (ok bool, isCurrent bool) {
	return setPendingPreviewFor(s, threadID, threadID, target)
}

func setPendingPreviewFor(s *chatState, slotThreadID, previewThreadID, target string) (ok bool, isCurrent bool) {
	if s == nil || slotThreadID == "" || target == "" {
		return false, false
	}
	if previewThreadID == "" {
		previewThreadID = slotThreadID
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false, false
	}
	st := inspectorSessionLocked(s, slotThreadID)
	st.pendingPreview = target
	st.pendingPreviewThreadID = previewThreadID
	isCurrent = s.selectedThreadID == slotThreadID
	s.mu.Unlock()
	if !isPreviewHTTPURL(target) {
		recordPreviewLocalPathBee(target, previewThreadID, slotThreadID)
	}
	return true, isCurrent
}

func takePendingPreview(s *chatState, threadID string) (target, previewThreadID string, ok bool) {
	if s == nil || threadID == "" {
		return "", "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.selectedThreadID != threadID {
		return "", "", false
	}
	st := inspectorExistingSessionLocked(s, threadID)
	if st == nil || st.pendingPreview == "" {
		return "", "", false
	}
	target = st.pendingPreview
	previewThreadID = st.pendingPreviewThreadID
	st.pendingPreview = ""
	st.pendingPreviewThreadID = ""
	if previewThreadID == "" {
		previewThreadID = threadID
	}
	return target, previewThreadID, true
}

func consumePendingPreview(s *chatState, threadID string) {
	target, previewThreadID, ok := takePendingPreview(s, threadID)
	if !ok {
		return
	}
	if isPreviewHTTPURL(target) {
		openPreviewURLForThread(s, previewThreadID, target)
		return
	}
	recordPreviewLocalPathBee(target, previewThreadID, threadID)
	if !s.openInspectorPreview(previewThreadID, target) {
		s.setToast("无法打开预览")
	}
}

func isPreviewHTTPURL(target string) bool {
	lower := strings.ToLower(target)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}
