package mcodeui

import (
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
)

// insertTurnTimeRows 在每个回合第一条承载消息的条目（单条消息、工具组、工具节拍、回合折叠项、汇报行）
// 之前插入一条日期时间分隔行，每个回合只插一次，其它条目原样保留；只用于总管会话。
func insertTurnTimeRows(items []timelineItem) []timelineItem {
	out := make([]timelineItem, 0, len(items))
	seen := make(map[int]bool)
	for _, item := range items {
		switch item.kind {
		case timelineItemSingle, timelineItemToolGroup, timelineItemToolBeat, timelineItemTurnWork, timelineItemSwarmReport:
			if item.turnIndex >= 0 && !seen[item.turnIndex] {
				seen[item.turnIndex] = true
				out = append(out, timelineItem{kind: timelineItemTurnTime, id: "turn-time-" + strconv.Itoa(item.turnIndex), turnIndex: item.turnIndex})
			}
		}
		out = append(out, item)
	}
	return out
}

// turnTimeLabel 按本地时区把回合起始时间格式化成「今天 15:04」「昨天 15:04」
// 「1月2日 15:04」或「2006年1月2日 15:04」，零值返回空串。
func turnTimeLabel(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	at, now = at.In(time.Local), now.In(time.Local)
	date := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.Local)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	switch {
	case date.Equal(today):
		return "今天 " + at.Format("15:04")
	case date.Equal(today.AddDate(0, 0, -1)):
		return "昨天 " + at.Format("15:04")
	case at.Year() == now.Year():
		return at.Format("1月2日 15:04")
	default:
		return at.Format("2006年1月2日 15:04")
	}
}

// renderTurnTimeRow 渲染居中的日期时间小字，时间取本回合起始时间，拿不到时不画。
func renderTurnTimeRow(c *ui.Context, s *chatState, item timelineItem) {
	label := turnTimeLabel(turnStartTimeFor(s, item.turnIndex), time.Now())
	if label == "" {
		return
	}
	ui.Row(c.Key(item.id + "-label")).FillWidth().Justify(ui.Center).Children(func() {
		ui.Text(c, label).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
	})
}
