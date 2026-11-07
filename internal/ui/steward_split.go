package mcodeui

import (
	"context"
	"strings"

	"github.com/egoist/mygo/ui"
)

// splitSessionNotice 是分割线的文案，也是后端写入的消息正文。
const splitSessionNotice = "已分割 · 以上历史不再进入上下文"

// splitSessionBtnKey 是输入框右侧分割按钮的固定 Key。
const splitSessionBtnKey = "composer-split-btn"

// splitSessionTooltip 是分割按钮可用时的提示。
const splitSessionTooltip = "分割会话：下一条消息不再带上面的历史"

// splitSessionBusyTooltip 是运行中时分割按钮的提示。
const splitSessionBusyTooltip = "等这一轮结束再分割"

// splitSessionView 汇总分割按钮这一帧的显示与禁用状态。
type splitSessionView struct {
	Show     bool // 是否渲染按钮：必须是总管会话，且已经有消息
	Disabled bool // 这一轮还没结束（正在发送或线程运行中）：按钮禁用
}

// splitSessionViewFor 计算分割按钮的显示与禁用状态。
// 只有总管会话能分割：th.ID 必须等于 s.stewardThreadID()，且当前会话已有消息；
// 运行中的判定与 composer 里一致，IsRunning 与 SwarmBusy 都算这一轮还没结束。
func splitSessionViewFor(s *chatState, th *Thread) splitSessionView {
	if s == nil || th == nil {
		return splitSessionView{}
	}
	if tid := s.stewardThreadID(); tid == "" || th.ID != tid {
		return splitSessionView{}
	}
	s.mu.Lock()
	hasMessages := len(s.messages) > 0
	sending := s.sending
	s.mu.Unlock()
	if !hasMessages {
		// 还没有内容就没有可分割的历史，按钮不出现。
		return splitSessionView{}
	}
	return splitSessionView{Show: true, Disabled: sending || th.IsRunning || th.SwarmBusy}
}

// canSplitThread 判断当前能不能真的发起分割：总管会话、已有消息、且这一轮已经结束。
func canSplitThread(s *chatState, th *Thread) bool {
	view := splitSessionViewFor(s, th)
	return view.Show && !view.Disabled
}

// renderSplitSessionButton 渲染总管会话的分割按钮：一个图标按钮，放在输入框右侧控制组里，
// 与其他按钮同一套悬停反馈。运行中保留按钮但禁用，提示改成「等这一轮结束再分割」；
// 普通会话与没有消息的总管会话整块不渲染。
func renderSplitSessionButton(c *ui.Context, s *chatState, th *Thread) {
	view := splitSessionViewFor(s, th)
	if !view.Show {
		return
	}
	tooltip := splitSessionTooltip
	if view.Disabled {
		tooltip = splitSessionBusyTooltip
	}

	btn := ui.ButtonBase(c.Key(splitSessionBtnKey)).
		Label("分割会话").
		Tooltip(tooltip).
		Size(15, 15).
		Margin(0, Space2, 0, 0).
		Radius(RadiusMD).
		Disabled(view.Disabled).
		OnClick(func() {
			if view.Disabled {
				return
			}
			splitStewardSession(s, th.ID)
		})
	composerHoverBg(c, btn, btn.Hovered() && !view.Disabled)

	btn.Children(func() {
		fg := ThemeSecondaryText(c)
		if view.Disabled {
			fg = ThemeSecondaryText(c).Alpha(0.4)
		}
		ui.Icon(c, IconGitBranch()).Size(15, 15).TextColor(fg)
	})
}

// splitStewardSession 在后台请求后端追加分割线。成功后提示并用一次历史重拉把分割线立刻画出来；
// 失败时 toast 后端给的中文错误原文（例如「这一轮还在进行，等它结束再分割」）。
func splitStewardSession(s *chatState, threadID string) {
	if s == nil || s.client == nil || threadID == "" {
		return
	}
	go func(tid string) {
		if err := s.client.SplitSession(context.Background(), tid); err != nil {
			s.setToast(err.Error())
			return
		}
		s.setToast("已分割，下一条消息不再带上面的历史")
		// 分割线是后端新增的一条消息，watch 不一定推过来，主动重拉一次历史让它立刻出现。
		s.reloadMessagesAfterResync(tid)
	}(threadID)
}

// splitDividerText 把分割线正文拆成标题与说明：正文为空时退回常量文案；
// 说明取第一个换行之后的剩余内容，并压成单行，避免长文案把这条线撑高。
func splitDividerText(text string) (title, detail string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return splitSessionNotice, ""
	}
	title, rest, found := strings.Cut(text, "\n")
	if !found {
		return strings.TrimSpace(title), ""
	}
	return strings.TrimSpace(title), strings.Join(strings.Fields(rest), " ")
}

// splitDividerRow 是左右两条淡色横线夹一段小字的那一行；单行与带说明两种排版共用。
func splitDividerRow(c *ui.Context, key, id, text string, marginBottom float32) ui.Element {
	return ui.Row(c.Key(key)).
		FillWidth().
		AlignItems(ui.Center).
		Gap(Space3).
		Margin(0, 0, marginBottom, 0).
		Children(func() {
			ui.Box(c.Key("split-divider-left-" + id)).
				Height(Hairline).
				Grow(1).
				Background(ThemeDivider(c))
			ui.Text(c, text).
				FontSize(scaledFont(TypeCaption)).
				TextColor(ThemeSecondaryText(c)).
				SingleLine()
			ui.Box(c.Key("split-divider-right-" + id)).
				Height(Hairline).
				Grow(1).
				Background(ThemeDivider(c))
		})
}

// renderSplitDivider 渲染时间线里的分割线：左右两条淡色横线夹一段小字。
// 分割线只是界面上的界线，上面的历史照常显示、可以滚动回看。
// 带计数时正文有两行：第一行仍是横线夹小字，下面另起一行居中写「已带走」的条数，
// 同样只占一行（窄窗口下截断，与同族的换新/压缩线一致）。
func renderSplitDivider(c *ui.Context, m Message) ui.Element {
	title, detail := splitDividerText(m.Text)
	if detail == "" {
		return splitDividerRow(c, "split-divider-"+m.ID, m.ID, title, 10)
	}
	return ui.Column(c.Key("split-divider-"+m.ID)).
		FillWidth().
		AlignItems(ui.Center).
		Margin(0, 0, Space3, 0).
		Children(func() {
			splitDividerRow(c, "split-divider-row-"+m.ID, m.ID, title, 0)
			ui.Text(c, detail).
				FontSize(scaledFont(TypeCaption)).
				TextColor(ThemeSecondaryText(c)).
				SingleLine()
		})
}
