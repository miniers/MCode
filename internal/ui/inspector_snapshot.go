package mcodeui

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const inspectorSnapshotStringMaxBytes = 2048

// InspectorSnapshot 只含定位元数据；Badges 属于调用方，不引用内部可变状态。
type InspectorSnapshot struct {
	Timestamp          time.Time
	Generation         uint64 // 容器生命周期代次，不是各面板内容的事务版本。
	ThreadID           string
	WindowID           string
	Open               bool
	Active             string
	Workspace          string
	FilePath           string
	FirstLine          int
	LastLine           int
	ReviewScope        string
	ReviewTurnID       string
	ReviewBaseRevision string
	ReviewHeadRevision string
	ReviewPath         string
	Following          bool
	Paused             bool
	Badges             map[string]int
	BrowserOrigin      string
}

// inspectorSnapshotFor 不初始化面板；各锁独立取值，末尾复核会话与容器生命周期。
func inspectorSnapshotFor(s *chatState) (InspectorSnapshot, bool) {
	if s == nil {
		return InspectorSnapshot{}, false
	}
	s.mu.Lock()
	tid := s.selectedThreadID
	st := inspectorExistingSessionLocked(s, tid)
	if s.closed || st == nil {
		s.mu.Unlock()
		return InspectorSnapshot{}, false
	}
	snap := InspectorSnapshot{
		Timestamp: time.Now(), Generation: st.generation,
		ThreadID: inspectorSnapshotString(tid),
		// 每个窗口拥有独立 chatState；不调用 window 回调或猜测前台窗口。
		WindowID: fmt.Sprintf("chat:%p", s), Open: st.Open,
		Active: inspectorSnapshotString(st.Active), Following: true,
		Workspace: inspectorSnapshotString(st.workspace), Badges: make(map[string]int),
	}
	if f := st.follow; f != nil {
		snap.Paused, snap.Following = f.paused, !f.paused
		// 目的集合固定，避免任意键或无限徽标进入快照。
		for _, id := range []string{sidePaneFileID, sidePaneDiffID, sidePaneGitID, sidePanePreviewID} {
			if count, ok := f.badges[id]; ok {
				snap.Badges[id] = count
			}
		}
	}
	root, _, _ := strings.Cut(st.workspace, "\x00")
	s.mu.Unlock()

	// 工作区切换会在各自面板锁下替换 Files / Review，不能在 s.mu 下取它们。
	fileManagerMu.Lock()
	if files := st.Files; files != nil {
		snap.FilePath = inspectorSnapshotPath(root, files.active)
		if tab := files.findTabLocked(files.active); tab != nil && snap.FilePath != "" {
			snap.FirstLine, snap.LastLine = tab.hlFirst, tab.hlLast
		}
	}
	fileManagerMu.Unlock()

	diffPanelMu.Lock()
	if review := st.Review; review != nil {
		snap.ReviewScope = reviewScope(review.scope)
		snap.ReviewTurnID = inspectorSnapshotString(review.turnID)
		snap.ReviewBaseRevision = inspectorSnapshotString(review.snapshot.BaseRevision)
		snap.ReviewHeadRevision = inspectorSnapshotString(review.snapshot.HeadRevision)
		if review.snapshot.TurnID != "" {
			snap.ReviewTurnID = inspectorSnapshotString(review.snapshot.TurnID)
		}
		snap.ReviewPath = inspectorSnapshotPath(root, review.selectedPath)
	}
	diffPanelMu.Unlock()

	previewMu.Lock()
	if browser := st.Browser; browser != nil {
		// browserActiveTabLocked 会创建 empty 标签，快照只能查找已有标签。
		tab := browser.empty
		for _, candidate := range browser.tabs {
			if candidate != nil && candidate.tabID == browser.activeTabID {
				tab = candidate
				break
			}
		}
		if tab != nil {
			snap.BrowserOrigin = inspectorSnapshotOrigin(tab.url, tab.localPreview || tab.localPreviewURL != "")
		}
	}
	previewMu.Unlock()

	s.mu.Lock()
	live := !s.closed && s.selectedThreadID == tid && inspectorExistingSessionLocked(s, tid) == st && st.generation == snap.Generation
	s.mu.Unlock()
	if !live {
		return InspectorSnapshot{}, false
	}
	return snap, true
}

func inspectorSnapshotString(value string) string {
	if len(value) > inspectorSnapshotStringMaxBytes {
		end := inspectorSnapshotStringMaxBytes
		for end > 0 && !utf8.RuneStart(value[end]) {
			end--
		}
		value = value[:end]
	}
	// 克隆也避免短的结果继续持有来源大字符串的底层内存。
	return strings.Clone(value)
}

func inspectorSnapshotPath(root, value string) string {
	if value == "" || strings.ContainsAny(value, "\x00\r\n") || strings.Contains(value, "://") {
		return ""
	}
	if filepath.IsAbs(value) {
		if root == "" {
			return ""
		}
		var err error
		value, err = filepath.Rel(root, value)
		if err != nil {
			return ""
		}
	}
	value = filepath.ToSlash(filepath.Clean(value))
	if value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return ""
	}
	return inspectorSnapshotString(value)
}

func inspectorSnapshotOrigin(raw string, local bool) string {
	if local {
		return "local"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	// 用户也可能把本地令牌地址粘贴到普通标签中，不能只依赖标签标记。
	host := u.Hostname()
	if ip := net.ParseIP(host); strings.HasPrefix(u.Path, "/p/") && (strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()) {
		return "local"
	}
	return inspectorSnapshotString(u.Scheme + "://" + u.Host)
}
