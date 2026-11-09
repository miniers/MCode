package mcodeui

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// stewardTimeGap 是和上一个时间标签相隔多长才再显示一次的阈值。
const stewardTimeGap = 5 * time.Minute

// insertStewardTimeRows 按消息时间间隔和本地日期插入时间分隔行，只用于总管会话。
func insertStewardTimeRows(items []timelineItem) []timelineItem {
	out := make([]timelineItem, 0, len(items))
	var last time.Time
	inserted := false
	for _, item := range items {
		switch item.kind {
		case timelineItemSingle, timelineItemToolGroup, timelineItemToolBeat, timelineItemTurnWork, timelineItemSwarmReport:
			var at time.Time
			var found bool
			for _, messages := range [][]Message{{item.msg}, item.tools, item.beatTools, item.reportMsgs} {
				for _, msg := range messages {
					if at, found = messageTime(msg); found {
						break
					}
				}
				if found {
					break
				}
			}
			localAt, localLast := at.In(time.Local), last.In(time.Local)
			if found && (!inserted || at.Sub(last) >= stewardTimeGap || localAt.Year() != localLast.Year() || localAt.YearDay() != localLast.YearDay()) {
				out = append(out, timelineItem{kind: timelineItemTurnTime, id: "steward-time-" + item.id, turnIndex: item.turnIndex, stampAt: at})
				last = at
				inserted = true
			}
		}
		out = append(out, item)
	}
	return out
}

// turnTimeLabel 按本地时区把时间标签格式化成「今天 15:04」「昨天 15:04」
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

// turnTimeTooltip 按本地时区格式化完整日期、星期和时间，零值返回空串。
func turnTimeTooltip(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	at = at.In(time.Local)
	weekdays := [...]string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}
	return at.Format("2006年1月2日") + " " + weekdays[at.Weekday()] + " " + at.Format("15:04:05")
}

// renderTurnTimeRow 渲染居中的日期时间小字，时间取分隔行记录的时刻，拿不到时不画。
func renderTurnTimeRow(c *ui.Context, s *chatState, item timelineItem) {
	label := turnTimeLabel(item.stampAt, time.Now())
	if label == "" {
		return
	}
	ui.Row(c.Key(item.id + "-label")).FillWidth().Justify(ui.Center).Children(func() {
		ui.Text(c, label).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c)).Tooltip(turnTimeTooltip(item.stampAt))
	})
}
