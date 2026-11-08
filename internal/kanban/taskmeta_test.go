package kanban

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"mcode/internal/store"
)

func TestTaskMetaEncoding(t *testing.T) {
	m := TaskMeta{Origin: "owner", ParentID: "parent", Children: []string{"child"}, Provider: "provider", Model: "model", Wakes: 2}
	got, err := ParseTaskMeta(EncodeTaskMeta(m))
	if err != nil || !reflect.DeepEqual(got, m) {
		t.Fatalf("元数据往返: %+v, %v", got, err)
	}
	for _, raw := range []string{"", "{}"} {
		got, err := ParseTaskMeta(raw)
		if err != nil || !reflect.DeepEqual(got, TaskMeta{}) {
			t.Fatalf("空元数据: %+v, %v", got, err)
		}
	}
	if got := EncodeTaskMeta(TaskMeta{}); got != "{}" {
		t.Fatalf("空元数据编码: %q", got)
	}
}

func TestPatchTaskMeta(t *testing.T) {
	ctx := context.Background()
	svc, st, _ := setupTestService(t)
	task, err := svc.CreateTask(ctx, CreateTaskInput{Title: "task", Origin: "owner", Provider: "provider", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := svc.PatchTaskMeta(ctx, task.ID, task.Revision, func(m *TaskMeta) { m.Wakes++ })
	if err != nil {
		t.Fatal(err)
	}
	stored, err := st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := ParseTaskMeta(stored.Meta)
	if err != nil || meta.Wakes != 1 || meta.Origin != "owner" || meta.Provider != "provider" || meta.Model != "model" || updated.Revision != task.Revision+1 {
		t.Fatalf("更新元数据: %+v, %v", meta, err)
	}
	_, err = svc.PatchTaskMeta(ctx, task.ID, task.Revision, func(m *TaskMeta) { m.Wakes++ })
	if !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("过期版本: %v", err)
	}
}
