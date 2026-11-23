package mcodeui

import (
	"context"
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
)

func reviewScope(scope string) string {
	switch scope {
	case "all", "session", "pr":
		return scope
	case "uncommitted":
		return "all"
	default:
		return "turn"
	}
}

func reviewScopeTitle(scope string) string {
	switch reviewScope(scope) {
	case "pr":
		return "PR"
	case "session":
		return "会话"
	case "all":
		return "全部"
	default:
		return "本轮"
	}
}

func (s *chatState) loadReview(dp *diffPanelState, client reviewClient, threadID, turnID string) {
	loadReviewPreference(s, dp, threadID)
	diffPanelMu.Lock()
	scope, number := reviewScope(dp.scope), dp.prNumber
	if scope != "turn" {
		turnID = ""
	}
	dp.scope = scope
	dp.requestGen++
	gen := dp.requestGen
	if dp.threadID != threadID {
		dp.sessionFiles = nil
	}
	dp.threadID, dp.turnID = threadID, turnID
	dp.loading, dp.err = threadID != "", ""
	// 旧正文请求不能阻止新批次重新取数。
	for key, entry := range dp.contents {
		if entry.loading || entry.err != "" {
			delete(dp.contents, key)
		}
	}
	if threadID == "" {
		dp.snapshot = ReviewSnapshot{}
		dp.selectedPath = ""
		dp.sessionFiles = nil
	}
	diffPanelMu.Unlock()
	s.invalidate()
	if threadID == "" {
		return
	}
	if scope == "all" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			sessSnap, sessErr := getReview(ctx, client, threadID, "session", "", 0, "")
			var files map[string]bool
			if sessErr == nil && sessSnap.Unavailable == "" {
				files = make(map[string]bool, len(sessSnap.Files))
				for _, f := range sessSnap.Files {
					if f.Path != "" {
						files[f.Path] = true
					}
				}
			}
			diffPanelMu.Lock()
			valid := dp.requestGen == gen && dp.threadID == threadID && reviewScope(dp.scope) == "all"
			changed := valid && (len(files) != len(dp.sessionFiles) || len(files) > 0)
			if valid {
				dp.sessionFiles = files
			}
			diffPanelMu.Unlock()
			if changed {
				s.invalidate()
			}
		}()
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		snapshot, err := getReview(ctx, client, threadID, scope, turnID, number, "")
		diffPanelMu.Lock()
		if dp.requestGen != gen || dp.threadID != threadID || reviewScope(dp.scope) != scope || dp.turnID != turnID || dp.prNumber != number {
			diffPanelMu.Unlock()
			return
		}
		dp.loading = false
		if err != nil {
			dp.err = err.Error()
		} else {
			snapshot.Scope = scope
			if scope == "pr" {
				snapshot.Number = number
			}
			if scope == "turn" && snapshot.TurnID == "" {
				snapshot.TurnID = turnID
			}
			if scope == "session" && snapshot.Unavailable == "" {
				files := make(map[string]bool, len(snapshot.Files))
				for _, f := range snapshot.Files {
					if f.Path != "" {
						files[f.Path] = true
					}
				}
				dp.sessionFiles = files
			}
			dp.snapshot = snapshot
			selected := dp.pendingPath
			if selected == "" {
				selected = dp.selectedPath
			}
			dp.selectedPath = ""
			for _, file := range snapshot.Files {
				if toolDiffPathMatch(file.Path, selected) {
					dp.selectedPath = file.Path
					break
				}
			}
			dp.pendingPath = ""
		}
		diffPanelMu.Unlock()
		if err == nil {
			rememberReviewPreference(s, dp, threadID)
		}
		s.invalidate()
	}()
}

func (s *chatState) selectReviewScope(dp *diffPanelState, scope string) {
	diffPanelMu.Lock()
	dp.scope = reviewScope(scope)
	threadID := dp.threadID
	diffPanelMu.Unlock()
	// 只有「未提交」需要 Git 状态；离开后停掉文件监听。
	s.mu.Lock()
	st := inspectorExistingSessionLocked(s, s.selectedThreadID)
	s.mu.Unlock()
	if st != nil {
		inspectorSyncVisibility(st)
	}
	inspectorFollowPause(s, threadID)
	s.loadDiff(threadID, "")
}

// renderReviewScope 用分段控件切换范围：全部、本轮、会话，有 PR 时追加 PR。
func renderReviewScope(c *ui.Context, s *chatState, dp *diffPanelState) {
	diffPanelMu.Lock()
	scope, number := reviewScope(dp.scope), dp.prNumber
	diffPanelMu.Unlock()
	values := []string{"all", "turn", "session"}
	if _, ok := s.client.(reviewPRClient); ok && number > 0 {
		values = append(values, "pr")
	}
	segments := make([]Segment, len(values))
	selected := 0
	for i, value := range values {
		segments[i].Label = reviewScopeTitle(value)
		if value == "pr" {
			segments[i].Label = fmt.Sprintf("PR #%d", number)
		}
		if value == scope {
			selected = i
		}
	}
	Segmented(c, "review-scope", &selected, segments, func(i int) {
		s.selectReviewScope(dp, values[i])
	})
}

// 轮次来自消息记录，不为获取菜单而额外读取整批 patch。
func reviewTurns(s *chatState) ThreadDiff {
	s.mu.Lock()
	defer s.mu.Unlock()
	data := ThreadDiff{}
	seen := map[string]bool{}
	for _, m := range s.messages {
		if m.TurnID != "" && !seen[m.TurnID] {
			seen[m.TurnID] = true
			data.Turns = append(data.Turns, DiffTurn{TurnID: m.TurnID, Index: m.TurnIndex, HasChanges: true})
		}
	}
	return data
}
