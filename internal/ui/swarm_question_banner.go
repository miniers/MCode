package mcodeui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// 子线程提问提示条：Swarm 子线程用 ask_user 提问时，在主线程输入框上方给出醒目提示，
// 用户既可以直接跳到浮窗里自己回答，也可以把提问转交主线程由主代理代答。

const (
	// swarmQuestionBannerMaxRows 是提示条最多展示的子线程行数，其余折叠成一行汇总。
	swarmQuestionBannerMaxRows = 3
	// swarmQuestionBannerMaxRunes 是每行提问摘要的字数上限（单行截断）。
	swarmQuestionBannerMaxRunes = 90
)

// 转交过程中的临时状态按「主会话/子线程」记录，只用于禁用按钮与显示失败原因。
var (
	swarmQuestionBannerMu    sync.Mutex
	swarmQuestionForwarding  = make(map[string]bool)
	swarmQuestionForwardErrs = make(map[string]string)
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

	shown := bees
	hidden := 0
	if len(shown) > swarmQuestionBannerMaxRows {
		hidden = len(shown) - swarmQuestionBannerMaxRows
		shown = shown[:swarmQuestionBannerMaxRows]
	}

	accent := ThemeWarning(c)
	ui.Row(c.Key("swarm-question-banner-container")).Justify(ui.Center).Padding(0, Space6, Space2, Space6).Children(func() {
		ui.Column(c.Key("swarm-question-banner-stack")).Grow(1).MaxWidth(composerDockMaxWidth).Gap(Space2).Children(func() {
			for _, h := range shown {
				renderSwarmQuestionBannerRow(c, s, leadID, h, accent)
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

// renderSwarmQuestionBannerRow 渲染单个子线程的提问提示行。
func renderSwarmQuestionBannerRow(c *ui.Context, s *chatState, leadID string, h SwarmBee, accent ui.Color) {
	key := "swarm-question-banner-" + h.ThreadID
	rec := swarmQuestionBannerKey(leadID, h.ThreadID)

	swarmQuestionBannerMu.Lock()
	forwarding := swarmQuestionForwarding[rec]
	errText := swarmQuestionForwardErrs[rec]
	swarmQuestionBannerMu.Unlock()

	forwarded := h.PendingQuestion != nil && h.PendingQuestion.Forwarded

	ui.Column(c.Key(key)).FillWidth().Gap(Space1).Children(func() {
		ui.Row(c.Key(key+"-row")).
			FillWidth().
			Radius(RadiusXL).
			Background(accent.Alpha(0.10).Over(ThemeSurface(c))).
			Border(1, accent.Alpha(0.40)).
			Padding(Space3, Space4, Space3, Space3).
			Gap(Space3).
			AlignItems(ui.Center).
			Children(func() {
				renderSwarmGlyph(c, key+"-avatar", h, 22)
				ui.Text(c, swarmQuestionBannerSummary(h)).
					Grow(1).
					MinWidth(0).
					FontSize(uiFont(TypeBody)).
					TextColor(ThemePrimaryText(c)).
					MaxLines(1)
				ui.Row(c.Key(key + "-actions")).Gap(Space3).AlignItems(ui.Center).Children(func() {
					if forwarded {
						ui.Text(c, "已交给主线程，等待回答…").
							FontSize(uiFont(TypeFootnote)).
							TextColor(ThemeSecondaryText(c)).
							Selectable()
					} else {
						if hint := swarmQuestionAutoForwardHint(h.PendingQuestion, time.Now()); hint != "" {
							ui.Text(c, hint).
								FontSize(uiFont(TypeFootnote)).
								TextColor(ThemeSecondaryText(c)).
								MaxLines(1)
						}
						renderApprovalButton(c, key+"-forward", "交给主线程回答", "把这个提问交给主线程代答", false, forwarding, func() {
							forwardSwarmQuestion(s, leadID, h)
						})
					}
					renderApprovalButton(c, key+"-open", "去回答", "打开该子线程浮窗自己回答", true, false, func() {
						openSwarmFloatForBee(leadID, h.ThreadID)
						s.invalidate()
					})
				})
			})
		if errText != "" {
			ui.Text(c, "转交失败："+errText).
				FontSize(uiFont(TypeCaption)).
				TextColor(ThemeDanger(c)).
				MaxLines(2)
		}
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

// swarmQuestionBannerSummary 生成一行摘要：「<名字> 在提问：<题干>」，多题时补题数。
func swarmQuestionBannerSummary(h SwarmBee) string {
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
	return swarmQuestionSingleLine(text, swarmQuestionBannerMaxRunes)
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
		err := s.client.ForwardBeeQuestion(context.Background(), leadID, h.ThreadID, requestID)
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
