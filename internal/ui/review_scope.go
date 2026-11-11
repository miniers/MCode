package mcodeui

import (
	"context"
	"time"

	"github.com/egoist/mygo/ui"
)

func reviewScope(scope string) string {
	switch scope {
	case "session", "uncommitted":
		return scope
	default:
		return "turn"
	}
}

func reviewScopeTitle(scope string) string {
	switch reviewScope(scope) {
	case "session":
		return "整个会话"
	case "uncommitted":
		return "未提交"
	default:
		return "本轮"
	}
}

func (s *chatState) loadReview(dp *diffPanelState, client reviewClient, threadID, turnID string) {
	loadReviewPreference(s, dp, threadID)
	diffPanelMu.Lock()
	scope := reviewScope(dp.scope)
	if scope != "turn" {
		turnID = ""
	}
	dp.scope = scope
	dp.requestGen++
	gen := dp.requestGen
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
	}
	diffPanelMu.Unlock()
	s.invalidate()
	if threadID == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		snapshot, err := client.ReviewGet(ctx, threadID, scope, turnID, "")
		diffPanelMu.Lock()
		if dp.requestGen != gen || dp.threadID != threadID || reviewScope(dp.scope) != scope || dp.turnID != turnID {
			diffPanelMu.Unlock()
			return
		}
		dp.loading = false
		if err != nil {
			dp.err = err.Error()
		} else {
			snapshot.Scope = scope
			if scope == "turn" && snapshot.TurnID == "" {
				snapshot.TurnID = turnID
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
	dp.scope, dp.scopeMenuOpen = reviewScope(scope), false
	threadID := dp.threadID
	diffPanelMu.Unlock()
	inspectorFollowPause(s, threadID)
	s.loadDiff(threadID, "")
}

func renderReviewScope(c *ui.Context, s *chatState, dp *diffPanelState) {
	diffPanelMu.Lock()
	scope, open := reviewScope(dp.scope), dp.scopeMenuOpen
	diffPanelMu.Unlock()
	trigger := ui.ButtonBase(c.Key("review-scope-trigger")).Label(reviewScopeTitle(scope)).Padding(Space1, Space3).Radius(RadiusSM)
	if trigger.Clicked() {
		open = !open
		diffPanelMu.Lock()
		dp.scopeMenuOpen = open
		diffPanelMu.Unlock()
	}
	trigger.Children(func() { ui.Text(c, reviewScopeTitle(scope)).FontSize(TypeFootnote).TextColor(ThemePrimaryText(c)) })
	bg := ThemeSurfacePopover(c)
	native := flyoutHostWindow(c.Services()) != nil
	host := c.Services()
	appFlyoutPopover(c, "review-scope-popover", trigger, &open, func() {
		diffPanelMu.Lock()
		dp.scopeMenuOpen = false
		diffPanelMu.Unlock()
	}, appFlyoutProps{Width: 150, Background: &bg}, func(c *ui.Context) {
		card := ui.Column(c.Key("review-scope-menu"))
		if !native {
			card = popoverCard(c, card)
		}
		card.Transition(motionPopover(c, Space2)).Padding(Space1).Children(func() {
			for _, value := range []string{"turn", "session", "uncommitted"} {
				value := value
				btn := ui.ButtonBase(c.Key("review-scope-"+value)).Label(reviewScopeTitle(value)).FillWidth().Padding(Space2, Space3).Radius(RadiusXS)
				if value == scope {
					btn.Background(ThemeSelected(c))
				}
				if btn.Clicked() {
					closeAppFlyoutPopover(host, "review-scope-popover")
					s.selectReviewScope(dp, value)
					host.Invalidate()
				}
				btn.Children(func() { ui.Text(c, reviewScopeTitle(value)).FontSize(TypeFootnote).TextColor(ThemePrimaryText(c)) })
			}
		})
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
