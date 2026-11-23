package mcodeui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// 子线程提问提示条：Swarm 子线程用 ask_user 提问时，在主线程输入框上方内联第一位待答 bee 的提问卡，
// 其余 bee 显示一行摘要，用户可直接作答、打开浮窗自己回答，或转交主线程代答。

const (
	// swarmQuestionBannerMaxRows 是提示条最多展示的子线程行数，其余折叠成一行汇总。
	swarmQuestionBannerMaxRows = 3
	// swarmQuestionBannerMaxRunes 是每行提问摘要的字数上限（单行截断）。
	swarmQuestionBannerMaxRunes = 90
)

// 转交过程中的临时状态按「主会话/子线程」记录，只用于禁用按钮与显示失败原因。
var (
	swarmQuestionBannerMu        sync.Mutex
	swarmQuestionForwarding      = make(map[string]bool)
	swarmQuestionForwardErrs     = make(map[string]string)
	swarmQuestionAnswerRefreshed = make(map[string]bool)
)

// swarmQuestionBannerKey 返回某个子线程在提示条里的记录键。
func swarmQuestionBannerKey(leadID, beeID string) string {
	return leadID + "/" + beeID
}

// renderSwarmQuestionBanner 渲染主线程输入框上方的子线程提问提示条；
// 没有待回答提问（或当前本身就是嵌入的子线程输入框）时不占布局。
func renderSwarmQuestionBanner(c *ui.Context, s *chatState) {
	if s == nil || s.embeddedBee {
		return
	}

	hp := getSwarmPanelState(s)
	if hp == nil {
		return
	}
	// 有效子线程列表读派生视图（状态变化时只算一次），不再每帧重建一份 bee 切片。
	view := swarmStateViewFor(s)
	if view == nil {
		return
	}

	leadID := strings.TrimSpace(view.leadID)
	if leadID == "" {
		return
	}
	bees := swarmBeesWithPendingQuestion(view.active)
	if len(bees) == 0 {
		return
	}

	pending := bees[:0]
	var answeredKeys []string
	for _, h := range bees {
		if beeQuestionAnsweredInChild(s, h) {
			if h.PendingQuestion != nil {
				answeredKeys = append(answeredKeys, swarmQuestionBannerKey(leadID, h.ThreadID)+"/"+h.PendingQuestion.RequestID)
			}
			continue
		}
		pending = append(pending, h)
	}
	if len(answeredKeys) > 0 {
		swarmPanelMu.Lock()
		loaded := hp.loaded
		swarmPanelMu.Unlock()
		if loaded {
			maybeRefreshSwarmForAnsweredBees(s, answeredKeys)
		}
	}
	if len(pending) == 0 {
		return
	}

	shown := pending
	hidden := 0
	if len(shown) > swarmQuestionBannerMaxRows {
		hidden = len(shown) - swarmQuestionBannerMaxRows
		shown = shown[:swarmQuestionBannerMaxRows]
	}

	accent := ThemeWarning(c)
	ui.Row(c.Key("swarm-question-banner-container")).
		FillWidth().
		MinWidth(0).
		Justify(ui.Center).
		Padding(0, Space6, Space2, Space6).
		PassThrough().
		Transition(motionCollapse(c)).
		Children(func() {
			ui.Column(c.Key("swarm-question-banner-stack")).Grow(1).MinWidth(0).MaxWidth(composerDockMaxWidth).Gap(Space2).PassThrough().Children(func() {
				for i, h := range shown {
					if i == 0 {
						renderSwarmQuestionBannerInline(c, s, leadID, h, accent)
					} else {
						renderSwarmQuestionBannerRow(c, s, leadID, h, accent)
					}
				}
				if hidden > 0 {
					ui.Text(c, fmt.Sprintf("另有 %d 个子线程在提问", hidden)).
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemeSecondaryText(c)).
						Selectable()
				}
			})
		})
}

// beeQuestionAnsweredInChild 检查已存在的子会话状态是否已提交/跳过当前待答提问，
// 避免主会话 SwarmState 尚未刷新时仍滞留在提示条里。不会主动创建子会话状态。
func beeQuestionAnsweredInChild(parent *chatState, h SwarmBee) bool {
	if parent == nil || h.PendingQuestion == nil || strings.TrimSpace(h.PendingQuestion.RequestID) == "" {
		return false
	}
	embeddedMu.Lock()
	entry := embeddedBees[embeddedBeeKey{parent, h.ThreadID}]
	var child *chatState
	if entry != nil {
		child = entry.state
	}
	embeddedMu.Unlock()
	if child == nil {
		return false
	}
	reqID := h.PendingQuestion.RequestID
	child.mu.Lock()
	defer child.mu.Unlock()
	for _, aq := range child.answeredQuestions[h.ThreadID] {
		if aq.ID == reqID {
			return true
		}
	}
	return false
}

// maybeRefreshSwarmForAnsweredBees 在子线程提问刚刚经内联卡答复后，对每个请求最多主动刷新一次主会话 SwarmState。
func maybeRefreshSwarmForAnsweredBees(s *chatState, keys []string) {
	if s == nil || len(keys) == 0 {
		return
	}
	needRefresh := false
	swarmQuestionBannerMu.Lock()
	for _, k := range keys {
		if !swarmQuestionAnswerRefreshed[k] {
			swarmQuestionAnswerRefreshed[k] = true
			needRefresh = true
		}
	}
	if len(swarmQuestionAnswerRefreshed) > 256 {
		swarmQuestionAnswerRefreshed = make(map[string]bool)
	}
	swarmQuestionBannerMu.Unlock()
	if needRefresh {
		s.refreshSwarm()
	}
}

// syncBeeQuestionApproval 确保子会话状态上存在与 h.PendingQuestion 对应的提问审批，
// 并返回渲染所需的审批对象；若该请求已答复则返回 false。
func syncBeeQuestionApproval(child *chatState, h SwarmBee) (Approval, bool) {
	if child == nil || h.PendingQuestion == nil || strings.TrimSpace(h.PendingQuestion.RequestID) == "" {
		return Approval{}, false
	}
	reqID := h.PendingQuestion.RequestID
	child.mu.Lock()
	defer child.mu.Unlock()
	for _, aq := range child.answeredQuestions[h.ThreadID] {
		if aq.ID == reqID {
			return Approval{}, false
		}
	}
	for _, a := range child.approvals {
		if a.RequestID == reqID {
			return a, true
		}
	}
	// 子会话刚创建时 selectThread 还在异步拉取审批，或测试仅注入了 SwarmState.PendingQuestion：
	// 先补齐一条提问审批，保证首帧即可内联作答且单选自动推进能命中同一请求。
	appr := Approval{
		ThreadID:  h.ThreadID,
		RequestID: reqID,
		Kind:      ApprovalKindQuestion,
		Questions: h.PendingQuestion.Questions,
	}
	child.approvals = append(child.approvals, appr)
	return appr, true
}

// renderSwarmQuestionBannerInline 为排在第一位的待答 bee 渲染标题栏与内联提问卡。
func renderSwarmQuestionBannerInline(c *ui.Context, s *chatState, leadID string, h SwarmBee, accent ui.Color) {
	key := "swarm-question-banner-" + h.ThreadID
	rec := swarmQuestionBannerKey(leadID, h.ThreadID)

	swarmQuestionBannerMu.Lock()
	forwarding := swarmQuestionForwarding[rec]
	errText := swarmQuestionForwardErrs[rec]
	swarmQuestionBannerMu.Unlock()

	forwarded := h.PendingQuestion != nil && h.PendingQuestion.Forwarded
	name := strings.TrimSpace(h.Name)
	if name == "" {
		name = "子线程"
	}

	child := beeChatState(s, h)
	appr, hasAppr := syncBeeQuestionApproval(child, h)
	responding := false
	if child != nil {
		child.mu.Lock()
		responding = child.respondingApproval > 0
		child.mu.Unlock()
		if !responding {
			setActiveApprovalKey(child, "")
		}
	}

	ui.Column(c.Key(key)).FillWidth().MinWidth(0).Gap(Space2).Transition(motionListItem(c)).Children(func() {
		renderSwarmQuestionBannerHeaderRow(c, s, leadID, h, accent, key, name+" 在提问", swarmQuestionBannerFullSummary(h), forwarding, forwarded)
		if hasAppr {
			ui.Column(c.Key(key + "-inline-card")).FillWidth().MinWidth(0).Transition(motionCollapse(c)).Children(func() {
				renderQuestionCard(c, child, appr, responding)
			})
		}
		if errText != "" {
			ui.Text(c, "转交失败："+errText).
				FontSize(uiFont(TypeCaption)).
				TextColor(ThemeDanger(c)).
				MaxLines(2)
		}
	})
}

// renderSwarmQuestionBannerRow 渲染其余待答子线程的提问摘要行。
func renderSwarmQuestionBannerRow(c *ui.Context, s *chatState, leadID string, h SwarmBee, accent ui.Color) {
	key := "swarm-question-banner-" + h.ThreadID
	rec := swarmQuestionBannerKey(leadID, h.ThreadID)

	swarmQuestionBannerMu.Lock()
	forwarding := swarmQuestionForwarding[rec]
	errText := swarmQuestionForwardErrs[rec]
	swarmQuestionBannerMu.Unlock()

	forwarded := h.PendingQuestion != nil && h.PendingQuestion.Forwarded
	fullSummary := swarmQuestionBannerFullSummary(h)
	summary := swarmQuestionSingleLine(fullSummary, swarmQuestionBannerMaxRunes)

	ui.Column(c.Key(key)).FillWidth().MinWidth(0).Gap(Space1).Transition(motionListItem(c)).Children(func() {
		renderSwarmQuestionBannerHeaderRow(c, s, leadID, h, accent, key, summary, fullSummary, forwarding, forwarded)
		if errText != "" {
			ui.Text(c, "转交失败："+errText).
				FontSize(uiFont(TypeCaption)).
				TextColor(ThemeDanger(c)).
				MaxLines(2)
		}
	})
}

// renderSwarmQuestionBannerHeaderRow 渲染单个子线程提问提示的头部行（头像、标题/摘要与操作按钮）。
func renderSwarmQuestionBannerHeaderRow(c *ui.Context, s *chatState, leadID string, h SwarmBee, accent ui.Color, key, summary, tooltip string, forwarding, forwarded bool) {
	ui.Row(c.Key(key+"-row")).
		FillWidth().
		MinWidth(0).
		Wrap().
		Radius(RadiusXL).
		Background(accent.Alpha(0.10).Over(ThemeSurface(c))).
		Border(1, accent.Alpha(0.40)).
		Padding(Space3, Space4, Space3, Space3).
		Gap(Space3).
		AlignItems(ui.Center).
		Children(func() {
			ui.Row(c.Key(key + "-summary")).
				Grow(1).
				Basis(240).
				MinWidth(0).
				Shrink(1).
				Gap(Space3).
				AlignItems(ui.Center).
				Children(func() {
					ui.Box(c.Key(key + "-avatar-wrap")).Label(h.Name + " 头像").Shrink(0).Children(func() {
						renderSwarmGlyph(c, key+"-avatar", h, 22)
					})
					ui.Text(c, summary).
						Grow(1).
						MinWidth(0).
						Shrink(1).
						Wrap().
						FontSize(uiFont(TypeBody)).
						TextColor(ThemePrimaryText(c)).
						MaxLines(2).
						Ellipsis("…").
						Tooltip(tooltip)
				})
			ui.Row(c.Key(key + "-actions")).
				Wrap().
				MinWidth(0).
				Gap(Space2).
				AlignItems(ui.Center).
				Children(func() {
					if forwarded {
						ui.Text(c, "已交给主线程，等待回答…").
							MinWidth(0).
							Shrink(1).
							FontSize(uiFont(TypeFootnote)).
							TextColor(ThemeSecondaryText(c)).
							Selectable()
					} else {
						now := c.Now()
						if hint := swarmQuestionAutoForwardHint(h.PendingQuestion, now); hint != "" {
							if d, ok := swarmQuestionAutoForwardNextChange(h.PendingQuestion, now); ok {
								c.After(d)
							}
							ui.Text(c, hint).
								FontSize(uiFont(TypeFootnote)).
								FontFeatures("tnum").
								TextColor(ThemeSecondaryText(c)).
								MaxLines(1)
						}
						Button(c, ButtonOpts{
							Key:     key + "-forward",
							Text:    "交给主线程回答",
							Variant: ButtonSecondary,
							Size:    ControlSM,
							Loading: forwarding,
							Tooltip: "把这个提问交给主线程代答",
							OnClick: func() {
								forwardSwarmQuestion(s, leadID, h)
							},
						})
					}
					Button(c, ButtonOpts{
						Key:     key + "-open",
						Text:    "去回答",
						Variant: ButtonPrimary,
						Size:    ControlSM,
						Tooltip: "打开该子线程浮窗自己回答",
						OnClick: func() {
							openSwarmFloatForBee(leadID, h.ThreadID)
							markSwarmFailedBeeSeen(s, leadID, h)
							s.invalidate()
						},
					})
				})
		})
}

// swarmBeesWithPendingQuestion 过滤出正在等用户回答的 bee。
func swarmBeesWithPendingQuestion(bees []SwarmBee) []SwarmBee {
	var res []SwarmBee
	for _, h := range bees {
		if h.PendingQuestion != nil {
			res = append(res, h)
		}
	}
	return res
}

// swarmQuestionBannerFullSummary 生成完整的单行摘要，供截断后的 Tooltip 展示全文。
func swarmQuestionBannerFullSummary(h SwarmBee) string {
	name := strings.TrimSpace(h.Name)
	if name == "" {
		name = "子线程"
	}
	text := name + " 在提问"
	if q := h.PendingQuestion; q != nil {
		if prompt := swarmQuestionPrompt(q); prompt != "" {
			text += "：" + prompt
		}
		if len(q.Questions) > 1 {
			text += fmt.Sprintf("（共 %d 题）", len(q.Questions))
		}
	}
	return strings.Join(strings.Fields(text), " ")
}

// swarmQuestionBannerSummary 生成一行摘要：「<名字> 在提问：<题干>」，多题时补题数。
func swarmQuestionBannerSummary(h SwarmBee) string {
	return swarmQuestionSingleLine(swarmQuestionBannerFullSummary(h), swarmQuestionBannerMaxRunes)
}

// swarmQuestionAutoForwardHint 返回自动转交倒计时文案；未开启自动转交或已转交时返回空。
func swarmQuestionAutoForwardHint(q *SwarmBeeQuestion, now time.Time) string {
	if q == nil || q.Forwarded || q.AutoForwardAt.IsZero() {
		return ""
	}
	left := q.AutoForwardAt.Sub(now)
	switch {
	case left <= 0:
		return "即将自动交给主线程"
	case left >= time.Minute:
		return fmt.Sprintf("%d 分钟后自动交给主线程", int((left+time.Minute-1)/time.Minute))
	default:
		return fmt.Sprintf("%d 秒后自动交给主线程", int((left+time.Second-1)/time.Second))
	}
}

// swarmQuestionAutoForwardNextChange 返回倒计时文案下一次变化前的等待时长；
// 已到期、已转交或未开启自动转交时不再变化，返回 false。
func swarmQuestionAutoForwardNextChange(q *SwarmBeeQuestion, now time.Time) (time.Duration, bool) {
	if q == nil || q.Forwarded || q.AutoForwardAt.IsZero() {
		return 0, false
	}
	left := q.AutoForwardAt.Sub(now)
	if left <= 0 {
		return 0, false
	}
	if left > time.Minute {
		if rem := left % time.Minute; rem > 0 {
			return rem, true
		}
		return time.Minute, true
	}
	if left == time.Minute {
		return time.Second, true
	}
	if rem := left % time.Second; rem > 0 {
		return rem, true
	}
	return time.Second, true
}

// swarmQuestionPrompt 取第一题的题干：Prompt 为空时退回 Header。
func swarmQuestionPrompt(q *SwarmBeeQuestion) string {
	if q == nil || len(q.Questions) == 0 {
		return ""
	}
	first := q.Questions[0]
	prompt := strings.TrimSpace(first.Prompt)
	if prompt == "" {
		prompt = strings.TrimSpace(first.Header)
	}
	return prompt
}

// swarmQuestionSingleLine 把文本压成一行并在超长时截断。
func swarmQuestionSingleLine(text string, maxRunes int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "…"
}

// forwardSwarmQuestion 把子线程的提问转交主线程回答：请求期间禁用按钮，成功后刷新 Swarm 状态。
func forwardSwarmQuestion(s *chatState, leadID string, h SwarmBee) {
	if s == nil || s.client == nil || h.PendingQuestion == nil {
		return
	}
	rec := swarmQuestionBannerKey(leadID, h.ThreadID)
	requestID := h.PendingQuestion.RequestID

	swarmQuestionBannerMu.Lock()
	if swarmQuestionForwarding[rec] {
		swarmQuestionBannerMu.Unlock()
		return
	}
	swarmQuestionForwarding[rec] = true
	delete(swarmQuestionForwardErrs, rec)
	swarmQuestionBannerMu.Unlock()
	s.invalidate()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := s.client.ForwardBeeQuestion(ctx, leadID, h.ThreadID, requestID)
		swarmQuestionBannerMu.Lock()
		delete(swarmQuestionForwarding, rec)
		if err != nil {
			swarmQuestionForwardErrs[rec] = err.Error()
		}
		swarmQuestionBannerMu.Unlock()
		if err != nil {
			s.invalidate()
			return
		}
		s.refreshSwarm()
	}()
}
