package mcodeui

import (
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

// stewardDecisionPrefix 是总管回复里「需要用户拍板」那一行的固定句式前缀，
// 与 internal/steward/prompt.go 的提示词保持一致。
const stewardDecisionPrefix = "需要你决定："

var (
	// stewardDecisionDismissedMu 保护 stewardDecisionDismissed：
	// 关闭状态按消息 ID 记录，渲染与点击可能在不同帧、不同协程里接触它。
	stewardDecisionDismissedMu sync.Mutex
	stewardDecisionDismissed   = make(map[string]bool)
)

// stewardDecisionDismissedFor 判断某条决策消息是否已被用户手动关闭。
func stewardDecisionDismissedFor(id string) bool {
	if id == "" {
		return false
	}
	stewardDecisionDismissedMu.Lock()
	dismissed := stewardDecisionDismissed[id]
	stewardDecisionDismissedMu.Unlock()
	return dismissed
}

// dismissStewardDecision 记下用户关闭了这条决策消息：同一条消息不再显示置顶条，
// 总管下一次发出新的决策消息（ID 不同）时置顶条会重新出现。
func dismissStewardDecision(id string) {
	if id == "" {
		return
	}
	stewardDecisionDismissedMu.Lock()
	stewardDecisionDismissed[id] = true
	stewardDecisionDismissedMu.Unlock()
}

// stewardDecisionFirstLine 取消息正文里第一个非空行，去掉行首空白与 * # 标记，
// 让它能认出去掉 markdown 前缀后的「需要你决定：…」。
func stewardDecisionFirstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return strings.TrimSpace(strings.TrimLeft(line, " \t*#"))
	}
	return ""
}

// stewardPendingDecision 找出总管最新一条还没被回应的「需要你决定：…」。
// 从后往前扫：碰到用户自己说的消息就说明之前的事已经有人开口，不算待决定；
// 碰到定稿的助手消息就看它第一行是不是那个句式。命中时返回消息 ID 与一句话标题。
func stewardPendingDecision(messages []Message) (id, title string, ok bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		switch m.Role {
		case "user":
			// 宿主转来的事件、总管唤醒都是系统输入，不算用户开口。
			if isHiddenTriggerKind(m.Kind) {
				continue
			}
			return "", "", false
		case "assistant":
			if !m.IsFinal {
				continue
			}
			rest, found := strings.CutPrefix(stewardDecisionFirstLine(m.Text), stewardDecisionPrefix)
			if !found {
				continue
			}
			name := strings.TrimSpace(strings.ReplaceAll(rest, "**", ""))
			if name == "" {
				continue
			}
			return m.ID, name, true
		}
	}
	return "", "", false
}

// renderStewardDecisionBar 在总管会话的时间线上方渲染一条置顶条，
// 提醒用户总管最新一轮还有事等他拍板，点一下跳到那条消息。
func renderStewardDecisionBar(c *ui.Context, s *chatState, threadID string, messages []Message) {
	if !stewardSelected(s, threadID) {
		return
	}
	id, title, ok := stewardPendingDecision(messages)
	if !ok || stewardDecisionDismissedFor(id) {
		return
	}

	bar := ui.Row(c.Key("steward-decision-bar-"+threadID)).
		FillWidth().
		MaxWidth(timelineContentMaxWidth(s)).
		AlignSelf(ui.Center).
		AlignItems(ui.Center).
		Gap(Space3).
		Background(ThemeSurface(c)).
		Border(1, ThemeBorder(c)).
		Radius(RadiusXL).
		Padding(Space3, Space4).
		Margin(0, 0, Space4, 0).
		Label("需要你决定：" + title)
	if bar.Hovered() {
		bar.Background(ThemeHover(c))
	}
	if bar.Clicked() {
		s.requestMessageJump(threadID, id)
		s.invalidate()
	}
	bar.Children(func() {
		ui.Icon(c, IconAlertTriangle()).
			Size(14, 14).
			TextColor(ThemeWarning(c))
		ui.Text(c, "需要你决定").
			SingleLine().
			FontSize(scaledFont(TypeBody)).
			FontWeight(600).
			TextColor(ThemeWarning(c))
		ui.Text(c, title).
			Grow(1).
			MaxLines(1).
			Ellipsis("…").
			FontSize(scaledFont(TypeBody)).
			TextColor(ThemePrimaryText(c))
		// 关闭按钮自己接管按下的指针（ButtonBase 是链上最内层的可点元素），
		// 不会连带触发整行的跳转。
		closeBtn := ui.ButtonBase(c.Key("steward-decision-bar-close-"+threadID)).
			Label("关闭").
			Tooltip("关闭").
			Radius(RadiusSM).
			Padding(Space0, Space1)
		if closeBtn.Hovered() {
			closeBtn.Background(ThemeHover(c))
		}
		if closeBtn.Clicked() {
			dismissStewardDecision(id)
			s.invalidate()
		}
		closeBtn.Children(func() {
			ui.Icon(c, IconX()).
				Size(12, 12).
				TextColor(ThemeSecondaryText(c))
		})
	})
}
