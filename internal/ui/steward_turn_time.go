package mcodeui

import (
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
)

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

func renderTurnTimeRow(c *ui.Context, s *chatState, item timelineItem) {
	label := turnTimeLabel(turnStartTimeFor(s, item.turnIndex), time.Now())
	if label == "" {
		return
	}
	ui.Row(c.Key(item.id + "-label")).FillWidth().Justify(ui.Center).Children(func() {
		ui.Text(c, label).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
	})
}
