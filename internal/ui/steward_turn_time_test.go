package mcodeui

import (
	"reflect"
	"testing"
	"time"
)

func TestInsertTurnTimeRows(t *testing.T) {
	items := []timelineItem{
		{kind: timelineItemSingle, id: "a", turnIndex: 0},
		{kind: timelineItemToolGroup, id: "b", turnIndex: 0},
		{kind: timelineItemSwarmCapsules, id: "capsules", turnIndex: -1},
		{kind: timelineItemSwarmReport, id: "c", turnIndex: 1},
		{kind: timelineItemToolBeat, id: "d", turnIndex: 1},
		{kind: timelineItemTurnWork, id: "e", turnIndex: 1},
	}
	want := []timelineItem{
		{kind: timelineItemTurnTime, id: "turn-time-0", turnIndex: 0},
		items[0], items[1], items[2],
		{kind: timelineItemTurnTime, id: "turn-time-1", turnIndex: 1},
		items[3], items[4], items[5],
	}
	if got := insertTurnTimeRows(items); !reflect.DeepEqual(got, want) {
		t.Errorf("insertTurnTimeRows() = %#v, want %#v", got, want)
	}
}

func TestInsertTurnTimeRowsWithoutMessages(t *testing.T) {
	items := []timelineItem{
		{kind: timelineItemSwarmCapsules, id: "capsules", turnIndex: -1},
		{kind: timelineItemResolvedPlan, id: "plan", turnIndex: 0},
		{kind: timelineItemRunning, id: "running", turnIndex: 1},
	}
	if got := insertTurnTimeRows(items); !reflect.DeepEqual(got, items) {
		t.Errorf("insertTurnTimeRows() = %#v, want %#v", got, items)
	}
}

func TestTurnTimeLabel(t *testing.T) {
	now := time.Date(2026, 3, 2, 12, 0, 0, 0, time.Local)
	tests := []struct {
		name string
		at   time.Time
		want string
	}{
		{"零值", time.Time{}, ""},
		{"今天", time.Date(2026, 3, 2, 15, 4, 0, 0, time.Local), "今天 15:04"},
		{"昨天", time.Date(2026, 3, 1, 15, 4, 0, 0, time.Local), "昨天 15:04"},
		{"同年", time.Date(2026, 1, 2, 15, 4, 0, 0, time.Local), "1月2日 15:04"},
		{"跨年", time.Date(2025, 1, 2, 15, 4, 0, 0, time.Local), "2025年1月2日 15:04"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := turnTimeLabel(tt.at, now); got != tt.want {
				t.Errorf("turnTimeLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}
