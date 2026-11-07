package mcodeui

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// 会话行左图标的「这一轮跑完了」角标：一轮结束（运行中 → 空闲）且用户当时没在看的会话，
// 在侧栏图标右上角亮一个小圆点、标题同色，打开该会话即清除。
// 颜色按结束状态区分：正常跑完用强调色，只有真正报错的回合才用红色，
// 避免把普通完成误读成出错。
// 标记只存内存：应用重启后不再提示历史轮次，符合一次性的完成提醒语义。

const (
	// turnDoneKindDone 表示这一轮正常结束（完成、被用户停止等都不算错误）。
	turnDoneKindDone = "done"
	// turnDoneKindFailed 表示这一轮以错误结束。
	turnDoneKindFailed = "failed"
)

// turnDoneKindFromStatus 把后端给的回合终态映射成角标种类。
func turnDoneKindFromStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "error":
		return turnDoneKindFailed
	}
	return turnDoneKindDone
}

// markTurnFinishedLocked 记录会话「已跑完、未查看」及其结束状态，调用方需持有 s.mu。
// 返回标记是否发生变化，供调用方决定是否重绘。
func (s *chatState) markTurnFinishedLocked(threadID, status string) bool {
	if s == nil || threadID == "" {
		return false
	}
	if s.turnFinished == nil {
		s.turnFinished = make(map[string]string)
	}
	kind := turnDoneKindFromStatus(status)
	if s.turnFinished[threadID] == kind {
		return false
	}
	s.turnFinished[threadID] = kind
	return true
}

// clearTurnFinishedLocked 清除会话的完成标记，调用方需持有 s.mu。
// 返回标记是否发生变化，供调用方决定是否重绘。
func (s *chatState) clearTurnFinishedLocked(threadID string) bool {
	if s == nil || threadID == "" {
		return false
	}
	if _, ok := s.turnFinished[threadID]; !ok {
		return false
	}
	delete(s.turnFinished, threadID)
	return true
}

// threadTurnFinished 返回会话的完成角标种类；空串表示没有标记。
func (s *chatState) threadTurnFinished(threadID string) string {
	if s == nil || threadID == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnFinished[threadID]
}

// turnDoneColor 返回完成角标与标题的颜色：正常完成用强调色，报错的回合才用红色。
func turnDoneColor(c *ui.Context, kind string) ui.Color {
	if kind == turnDoneKindFailed {
		return ThemeDanger(c)
	}
	return ThemeAccent(c)
}

// renderProviderIconDone 渲染会话行的供应商图标，并在右上角叠一个完成小圆点（颜色按结束状态取）。
// 角标贴图标框内侧，不依赖 overflow，任何布局下都不会被裁掉；先铺一圈侧栏底色再造圆点，
// 否则图标本身的鲜艳颜色会和圆点糊在一起。
func renderProviderIconDone(c *ui.Context, key, provider string, size float32, kind string) {
	ringColor := ThemeWindowBg(c)
	dotColor := turnDoneColor(c, kind)
	ui.Row(c.Key(key)).
		Size(size, size).
		Shrink(0).
		AlignItems(ui.Center).
		Children(func() {
			renderProviderIcon(c, provider, size)
			ui.Box(c.Key(key+"-dot")).
				Size(7, 7).
				Attach(ui.AnchorTopRight, ui.AnchorTopRight).
				Draw(func(p *ui.Painter, r ui.Rect) {
					p.Fill(r, ringColor, 3.5)
					p.Fill(ui.Rect{X: r.X + 1, Y: r.Y + 1, W: 5, H: 5}, dotColor, 2.5)
				})
		})
}
