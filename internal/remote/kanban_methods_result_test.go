package remote

import (
	"encoding/json"
	"reflect"
	"testing"

	"mcode/internal/kanban"
	"mcode/internal/store"
)

func TestTaskViewToWireResultMeta(t *testing.T) {
	task := taskViewToWire(kanban.TaskView{Task: store.Task{
		Meta: `{"origin":"owner","parent":"parent-1","source_message":"message-1","amend_messages":["message-2"],"summary":"完成摘要","wait_kind":"machine","wake_at":"2026-11-09T10:00:00Z"}`,
	}})
	if task.ResultSummary != "完成摘要" || task.WaitKind != "machine" || task.WakeAt != "2026-11-09T10:00:00Z" {
		t.Fatalf("unexpected task metadata: %+v", task)
	}
	if task.Origin != "owner" || task.ParentID != "parent-1" || task.SourceMessageID != "message-1" || !reflect.DeepEqual(task.AmendMessageIDs, []string{"message-2"}) {
		t.Fatalf("任务来源错误: %+v", task)
	}
	data, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["resultSummary"] != "完成摘要" || fields["waitKind"] != "machine" || fields["wakeAt"] != "2026-11-09T10:00:00Z" {
		t.Fatalf("unexpected JSON fields: %s", data)
	}
	if fields["origin"] != "owner" || fields["parentID"] != "parent-1" || fields["sourceMessageID"] != "message-1" || !reflect.DeepEqual(fields["amendMessageIDs"], []any{"message-2"}) {
		t.Fatalf("任务来源 JSON 错误: %s", data)
	}

	invalid := taskViewToWire(kanban.TaskView{Task: store.Task{Meta: `{"summary":"partial",`}})
	if invalid.ResultSummary != "" || invalid.WaitKind != "" || invalid.WakeAt != "" {
		t.Fatalf("invalid metadata must be empty: %+v", invalid)
	}
}
