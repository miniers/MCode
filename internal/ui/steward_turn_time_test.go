package mcodeui

import (
	"reflect"
	"testing"
	"time"
)

func TestInsertStewardTimeRows(t *testing.T) {
	at := func(day, hour, minute, second int) string {
		return time.Date(2026, 3, day, hour, minute, second, 0, time.Local).Format(time.RFC3339)
	}
	stamp := func(item timelineItem, createdAt string) timelineItem {
		parsed, err := time.Parse(time.RFC3339, createdAt)
		if err != nil {
			t.Fatal(err)
		}
		return timelineItem{kind: timelineItemTurnTime, id: "steward-time-" + item.id, turnIndex: item.turnIndex, stampAt: parsed}
	}
	items := []timelineItem{
		{kind: timelineItemSingle, id: "a", turnIndex: 0, msg: Message{Role: "user", CreatedAt: at(2, 10, 0, 0)}},
		{kind: timelineItemSingle, id: "b", turnIndex: 0, msg: Message{Role: "assistant", CreatedAt: at(2, 10, 0, 20)}},
		{kind: timelineItemSwarmCapsules, id: "capsules", turnIndex: -1},
		{kind: timelineItemSingle, id: "missing", turnIndex: 0},
		{kind: timelineItemSingle, id: "c", turnIndex: 0, msg: Message{Role: "user", CreatedAt: at(2, 10, 2, 0)}},
		{kind: timelineItemSingle, id: "d", turnIndex: 0, msg: Message{Role: "system", Kind: "steward_result", CreatedAt: at(2, 13, 0, 0)}},
		{kind: timelineItemSingle, id: "e", turnIndex: 0, msg: Message{Role: "user", CreatedAt: at(3, 9, 0, 0)}},
	}
	want := []timelineItem{
		stamp(items[0], at(2, 10, 0, 0)), items[0], items[1], items[2], items[3], items[4],
		stamp(items[5], at(2, 13, 0, 0)), items[5],
		stamp(items[6], at(3, 9, 0, 0)), items[6],
	}
	if got := insertStewardTimeRows(items); !reflect.DeepEqual(got, want) {
		t.Errorf("insertStewardTimeRows() = %#v, want %#v", got, want)
	}

	t.Run("跨天和五分钟边界", func(t *testing.T) {
		items := []timelineItem{
			{kind: timelineItemSingle, id: "a", turnIndex: 0, msg: Message{CreatedAt: at(2, 23, 58, 0)}},
			{kind: timelineItemSingle, id: "b", turnIndex: 0, msg: Message{CreatedAt: at(3, 0, 1, 0)}},
			{kind: timelineItemSingle, id: "c", turnIndex: 0, msg: Message{CreatedAt: at(3, 0, 6, 0)}},
		}
		want := []timelineItem{
			stamp(items[0], at(2, 23, 58, 0)), items[0],
			stamp(items[1], at(3, 0, 1, 0)), items[1],
			stamp(items[2], at(3, 0, 6, 0)), items[2],
		}
		if got := insertStewardTimeRows(items); !reflect.DeepEqual(got, want) {
			t.Errorf("insertStewardTimeRows() = %#v, want %#v", got, want)
		}
	})

	t.Run("聚合消息时间", func(t *testing.T) {
		items := []timelineItem{
			{kind: timelineItemToolGroup, id: "a", turnIndex: 0, msg: Message{CreatedAt: "invalid"}, tools: []Message{{}, {CreatedAt: at(2, 10, 0, 0)}}},
			{kind: timelineItemToolBeat, id: "b", turnIndex: 0, beatTools: []Message{{CreatedAt: at(2, 10, 5, 0)}}},
			{kind: timelineItemTurnWork, id: "c", turnIndex: 0, tools: []Message{{CreatedAt: at(2, 10, 10, 0)}}, beatTools: []Message{{CreatedAt: at(2, 10, 20, 0)}}},
			{kind: timelineItemSwarmReport, id: "d", turnIndex: 0, reportMsgs: []Message{{CreatedAt: at(2, 10, 15, 0)}}},
		}
		want := []timelineItem{
			stamp(items[0], at(2, 10, 0, 0)), items[0],
			stamp(items[1], at(2, 10, 5, 0)), items[1],
			stamp(items[2], at(2, 10, 10, 0)), items[2],
			stamp(items[3], at(2, 10, 15, 0)), items[3],
		}
		if got := insertStewardTimeRows(items); !reflect.DeepEqual(got, want) {
			t.Errorf("insertStewardTimeRows() = %#v, want %#v", got, want)
		}
	})
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

func TestTurnTimeTooltip(t *testing.T) {
	tests := []struct {
		name string
		at   time.Time
		want string
	}{
		{"零值", time.Time{}, ""},
		{"完整时间", time.Date(2026, 11, 9, 14, 32, 5, 0, time.Local), "2026年11月9日 星期一 14:32:05"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := turnTimeTooltip(tt.at); got != tt.want {
				t.Errorf("turnTimeTooltip() = %q, want %q", got, tt.want)
			}
		})
	}
}
