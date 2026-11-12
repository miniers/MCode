package mcodeui

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

const inspectorFollowInterval = 300 * time.Millisecond

type inspectorFollowTarget struct {
	path, messageID, workspace string
	first, last                int
	write                      bool
}

// 跟随状态由 chatState.mu 保护；计时器只请求重绘，定位只在 UI 帧中执行。
type inspectorFollowState struct {
	paused       bool
	badges       map[string]int
	latest       map[string]inspectorFollowTarget
	pending      map[string]bool
	seen         map[string]bool
	seenOrder    []string
	timer        *time.Timer
	timerSeq     uint64
	due          time.Time
	ignoreScroll bool
	scroll       map[string]float64
}

func inspectorFollowLocked(st *inspectorSessionState) *inspectorFollowState {
	if st.follow == nil {
		st.follow = &inspectorFollowState{
			badges: make(map[string]int), latest: make(map[string]inspectorFollowTarget),
			pending: make(map[string]bool), seen: make(map[string]bool),
		}
	}
	return st.follow
}

func inspectorFollowStopLocked(st *inspectorSessionState) {
	if st.follow != nil {
		st.follow.timerSeq++
		if st.follow.timer != nil {
			st.follow.timer.Stop()
			st.follow.timer = nil
		}
	}
}

func inspectorFollowReset(s *chatState, st *inspectorSessionState) {
	s.mu.Lock()
	inspectorFollowStopLocked(st)
	st.follow = nil
	s.mu.Unlock()
}

func inspectorFollowPause(s *chatState, threadID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if threadID == "" {
		threadID = s.selectedThreadID
	}
	if st := inspectorExistingSessionLocked(s, threadID); st != nil {
		inspectorFollowLocked(st).paused = true
	}
	s.mu.Unlock()
}

func inspectorFollowClear(s *chatState, tid, purpose string) {
	s.mu.Lock()
	if st := inspectorExistingSessionLocked(s, tid); st != nil && st.follow != nil {
		delete(st.follow.badges, purpose)
		delete(st.follow.pending, purpose)
	}
	s.mu.Unlock()
}

// 不信任文本回退字段；只给现有提取函数传入完整的结构化输入。
func inspectorFollowTargets(m Message, root string) []inspectorFollowTarget {
	name := toolNameKey(m.ToolName)
	write := fileEditToolNames[name]
	if m.Role != "tool" || m.ID == "" || (!write && !readToolNames[name]) {
		return nil
	}
	structured := m
	structured.Text = ""
	var paths []string
	valid := json.Valid([]byte(m.ToolInput))
	if write {
		if valid || (name == "applypatch" && strings.HasSuffix(strings.TrimSpace(m.ToolInput), "*** End Patch")) {
			paths = toolEditedPaths(structured)
		}
	} else if valid {
		paths = []string{toolCallPath(structured)}
	}
	if len(paths) == 0 {
		paths = []string{""}
	}
	fields := toolInputFields(structured)
	first, hasFirst := toolFieldInt(fields, "start_line", "startline", "start")
	last, hasLast := toolFieldInt(fields, "end_line", "endline", "end")
	if !hasFirst {
		first, hasFirst = toolFieldInt(fields, "offset")
	}
	if hasFirst && !hasLast {
		last = first
		if limit, ok := toolFieldInt(fields, "limit"); ok && limit > 0 && limit < 1000000 {
			last += limit - 1
		}
	}
	if first <= 0 || last <= 0 || first > 1000000 || last > 1000000 {
		first, last = 0, 0
	}
	var targets []inspectorFollowTarget
	for _, path := range paths {
		path = inspectorFollowPath(root, path)
		if m.ToolStatus == "failed" || m.ToolStatus == "blocked" || (write && m.ToolStatus != "completed") {
			path = ""
		}
		targets = append(targets, inspectorFollowTarget{path: path, messageID: m.ID, first: first, last: last, write: write})
	}
	return targets
}

func inspectorFollowPath(root, path string) string {
	if root == "" || path == "" || path != strings.TrimSpace(path) || strings.ContainsAny(path, "\x00\r\n\\") || strings.Contains(path, "…") || strings.Contains(path, "...") {
		return ""
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return ""
		}
	}
	if filepath.IsAbs(path) {
		var err error
		path, err = filepath.Rel(root, path)
		if err != nil {
			return ""
		}
	}
	path = filepath.ToSlash(filepath.Clean(path))
	if path == "." || path == ".." || strings.HasPrefix(path, "../") || strings.Contains(path, "://") {
		return ""
	}
	return path
}

func inspectorFollowWorkspace(s *chatState, tid string) string {
	s.mu.Lock()
	main := ""
	for _, th := range s.threads {
		if th.ID == tid {
			for _, project := range s.projects {
				if project.ID == th.ProjectID {
					main = project.Path
					break
				}
			}
			break
		}
	}
	s.mu.Unlock()
	root := main
	if s.client != nil {
		if dir, ok := s.client.ThreadWorkDir(tid); ok && dir != "" {
			root = dir
		}
	}
	return root + "\x00" + main
}

func inspectorFollowObserve(s *chatState, update ThreadUpdate) {
	if s == nil || update.ThreadID == "" || len(update.Messages) == 0 {
		return
	}
	workspace := inspectorFollowWorkspace(s, update.ThreadID)
	root, _, _ := strings.Cut(workspace, "\x00")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	known := false
	for _, th := range s.threads {
		known = known || th.ID == update.ThreadID
	}
	if !known {
		return
	}
	var st *inspectorSessionState
	var f *inspectorFollowState
	for _, m := range update.Messages {
		for _, target := range inspectorFollowTargets(m, root) {
			if target.path == "" {
				continue
			}
			if st == nil {
				st = inspectorSessionLocked(s, update.ThreadID)
				f = inspectorFollowLocked(st)
			}
			key := m.ID + "\x00" + target.path
			if f.seen[key] {
				continue
			}
			f.seen[key] = true
			f.seenOrder = append(f.seenOrder, key)
			if len(f.seenOrder) > 4096 {
				delete(f.seen, f.seenOrder[0])
				f.seenOrder = f.seenOrder[1:]
			}
			target.workspace = workspace
			purposes := []string{sidePaneFileID}
			if target.write {
				purposes = append(purposes, sidePaneDiffID)
			}
			for _, id := range purposes {
				f.badges[id]++
				f.latest[id] = target
				f.pending[id] = true
			}
			if f.timer == nil {
				f.timerSeq++
				seq := f.timerSeq
				f.due = time.Now().Add(inspectorFollowInterval)
				f.timer = time.AfterFunc(inspectorFollowInterval, func() {
					s.mu.Lock()
					live := !s.closed && inspectorExistingSessionLocked(s, update.ThreadID) == st && st.follow == f && f.timerSeq == seq
					if live {
						f.timer = nil
					}
					s.mu.Unlock()
					if live {
						s.invalidate()
					}
				})
			}
		}
	}
}

// tick 和 resume 只由 UI 帧调用，后台增量不得接触编辑器或输入焦点。
func inspectorFollowTick(s *chatState, st *inspectorSessionState) {
	positions := inspectorFollowScrollPositions(st)
	s.mu.Lock()
	tid := s.selectedThreadID
	f := st.follow
	if f != nil && !f.ignoreScroll {
		for key, position := range positions {
			if old, ok := f.scroll[key]; ok && old != position {
				f.paused = true
			}
		}
	}
	if f == nil || f.paused || !st.Open || !f.pending[st.Active] || time.Now().Before(f.due) {
		s.mu.Unlock()
		return
	}
	id, target := st.Active, f.latest[st.Active]
	delete(f.pending, id)
	inspectorFollowStopLocked(st)
	s.mu.Unlock()
	if inspectorFollowReveal(s, tid, id, target) {
		s.mu.Lock()
		if st.follow == f && f.latest[id] == target {
			delete(f.badges, id)
			delete(f.pending, id)
		}
		s.mu.Unlock()
	} else {
		s.mu.Lock()
		if st.follow == f && f.latest[id] == target && !f.paused {
			delete(f.badges, id)
		}
		s.mu.Unlock()
	}
}

func inspectorFollowScrollPositions(st *inspectorSessionState) map[string]float64 {
	positions := make(map[string]float64)
	fileManagerMu.Lock()
	for key, view := range st.Files.codeViews {
		positions["file:"+key] = view.state.ScrollY()
	}
	fileManagerMu.Unlock()
	diffPanelMu.Lock()
	for key, view := range st.Review.views {
		positions[fmt.Sprint("review:", key)] = view.state.ScrollY()
	}
	diffPanelMu.Unlock()
	return positions
}

func inspectorFollowResume(s *chatState, st *inspectorSessionState, id string) {
	s.mu.Lock()
	tid := s.selectedThreadID
	f := inspectorFollowLocked(st)
	f.paused = false
	target := f.latest[id]
	delete(f.badges, id)
	delete(f.pending, id)
	s.mu.Unlock()
	inspectorSelect(s, id, false)
	inspectorFollowReveal(s, tid, id, target)
}

func inspectorFollowReveal(s *chatState, tid, id string, target inspectorFollowTarget) bool {
	if target.path == "" || target.workspace != inspectorFollowWorkspace(s, tid) {
		return false
	}
	s.mu.Lock()
	st := inspectorExistingSessionLocked(s, tid)
	allowed := !s.closed && s.selectedThreadID == tid && st != nil && st.Open && st.Active == id && !inspectorFollowLocked(st).paused
	if allowed {
		st.follow.ignoreScroll = true
	}
	s.mu.Unlock()
	if !allowed {
		return false
	}
	switch id {
	case sidePaneFileID:
		s.openFileManagerFileFollowing(tid, target.path, target.first, target.last, target.write)
	case sidePaneDiffID:
		if !target.write {
			return false
		}
		selectReviewFileFollowing(s, st.Review, tid, target.path)
	default:
		return false
	}
	return true
}

func inspectorFollowCodePreview(c *ui.Context, s *chatState, tab fileManagerTabSnapshot, keyPrefix string) ui.Element {
	manager := getFileManagerState(s)
	s.mu.Lock()
	slot := inspectorExistingSessionLocked(s, s.selectedThreadID)
	following := slot != nil && slot.follow != nil && !slot.follow.paused
	s.mu.Unlock()
	view := manager.codeViews[keyPrefix+tab.path]
	reveal := following && tab.reveal && view != nil && view.state.ViewportHeight() > 0 && (tab.hlFirst <= view.state.Buffer.LineCount() || !tab.hasMore || tab.limitReached)
	if reveal {
		tab.reveal = false
	}
	e := renderFileManagerCodePreview(c, s, tab, keyPrefix)
	type scrollMotion struct {
		active       bool
		from, to, at float64
	}
	motion := ui.Local(e, "inspector-follow-scroll", func() scrollMotion { return scrollMotion{} })
	if !following {
		motion.active = false
		return e
	}
	view = manager.codeViews[keyPrefix+tab.path]
	if view == nil {
		return e
	}
	state := view.state
	if motion.active && !reveal && state.ScrollY() != motion.at {
		inspectorFollowPause(s, "")
		motion.active = false
		return e
	}
	if reveal {
		state.ScrollTo(min(tab.hlFirst, state.Buffer.LineCount()) - 1)
		motion.to = max(0, state.LineTop(min(tab.hlFirst, state.Buffer.LineCount())-1)-state.ViewportHeight()/2)
		// 远距离跳转最多移动两个视口，不让用户等待整份文件滚完。
		span := 2 * state.ViewportHeight()
		motion.from = min(max(state.ScrollY(), motion.to-span), motion.to+span)
		motion.active = true
		e.Jump("follow-scroll", 0)
		e.Jump("line-highlight", 0)
		fileManagerMu.Lock()
		if live := manager.findTabLocked(tab.path); live != nil && live.revealSeq == tab.revealSeq {
			live.reveal = false
		}
		fileManagerMu.Unlock()
	}
	if motion.active {
		progress := float32(1)
		if reduceMotion(c) {
			e.Jump("follow-scroll", 1)
		} else {
			progress = e.AnimateTo("follow-scroll", 1, ui.Animation{Duration: DurBase, Ease: EaseSnap})
		}
		state.SetScrollY(motion.from + (motion.to-motion.from)*float64(progress))
		motion.at = state.ScrollY()
		motion.active = progress < 1
		s.mu.Lock()
		slot.follow.ignoreScroll = true
		s.mu.Unlock()
	}
	return e
}

// 父级输入观察覆盖键盘和拖动；Draw 比较编辑器滚动，避免子编辑器吞掉滚轮后漏掉接管。
func inspectorFollowSurface(c *ui.Context, s *chatState, tid, key string) ui.Element {
	st := inspectorSession(s)
	if st == nil {
		return ui.Column(c.Key(key)).Fill()
	}
	return ui.Column(c.Key(key)).Fill().ObserveInput(func(ev ui.InputEvent) {
		if ev.Kind == ui.InputPointerDown || ev.Kind == ui.InputKeyDown {
			inspectorFollowPause(s, tid)
		}
	}).HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputScroll {
			inspectorFollowPause(s, tid)
		}
		return false
	}).Draw(func(_ *ui.Painter, _ ui.Rect) {
		positions := inspectorFollowScrollPositions(st)
		fileManagerMu.Lock()
		revealing := false
		for _, tab := range st.Files.tabs {
			revealing = revealing || tab.reveal
		}
		fileManagerMu.Unlock()
		s.mu.Lock()
		f := inspectorFollowLocked(st)
		if !f.ignoreScroll {
			for key, position := range positions {
				if old, ok := f.scroll[key]; ok && old != position {
					f.paused = true
				}
			}
		}
		f.scroll, f.ignoreScroll = positions, f.ignoreScroll && revealing
		s.mu.Unlock()
	})
}
