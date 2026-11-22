package mcodeui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
)

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
	s := chatStateForPreview(threadID)
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
	ok, isCurrent := setPendingPreview(s, threadID, target)
	if !ok {
		return errors.New("桌面窗口未打开")
	}
	if isCurrent && s.window != nil {
		mygo.RunOnMain(func() {
			consumePendingPreview(s, threadID)
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

// chatStateForPreview 优先返回当前正在查看 threadID 的窗口状态，否则退回活动窗口。
func chatStateForPreview(threadID string) *chatState {
	type windowEntry struct {
		win   *mygo.Window
		state *chatState
	}
	chatWindowsMu.RLock()
	entries := make([]windowEntry, 0, len(chatWindows))
	for win, state := range chatWindows {
		if state != nil {
			entries = append(entries, windowEntry{win: win, state: state})
		}
	}
	chatWindowsMu.RUnlock()

	var focused *mygo.Window
	if len(entries) > 1 {
		focused = mygo.FocusedWindow()
	}
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
	if matched != nil {
		return matched
	}
	return activeChatState()
}

func setPendingPreview(s *chatState, threadID, target string) (ok bool, isCurrent bool) {
	if s == nil || threadID == "" || target == "" {
		return false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, false
	}
	st := inspectorSessionLocked(s, threadID)
	st.pendingPreview = target
	return true, s.selectedThreadID == threadID
}

func takePendingPreview(s *chatState, threadID string) (string, bool) {
	if s == nil || threadID == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.selectedThreadID != threadID {
		return "", false
	}
	st := inspectorExistingSessionLocked(s, threadID)
	if st == nil || st.pendingPreview == "" {
		return "", false
	}
	target := st.pendingPreview
	st.pendingPreview = ""
	return target, true
}

func consumePendingPreview(s *chatState, threadID string) {
	target, ok := takePendingPreview(s, threadID)
	if !ok {
		return
	}
	if isPreviewHTTPURL(target) {
		openPreviewURL(s, target)
		return
	}
	if !s.openInspectorPreview(threadID, target) {
		s.setToast("无法打开预览")
	}
}

func isPreviewHTTPURL(target string) bool {
	lower := strings.ToLower(target)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}
