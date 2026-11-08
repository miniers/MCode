package kanban

import (
	"context"
	"strings"
	"testing"
	"time"

	"mcode/internal/store"
)

func TestDelegationAndWakeParents(t *testing.T) {
	ctx := context.Background()
	svc, db, _, _ := setupReceiptTestService(t, newPokeDriver(), true, false, nil)
	parent := store.Task{ID: "parent", Title: "父任务", Status: "in_progress"}
	if err := db.CreateTask(ctx, parent); err != nil {
		t.Fatal(err)
	}
	parent, _ = db.GetTask(ctx, parent.ID)
	res := ExecutorResult{Status: ResultDelegated, Summary: "拆分", Followups: []Followup{{Title: "一"}, {Title: "二"}}}
	if err := svc.applyExecutorResult(ctx, &parent, res, time.Now()); err != nil {
		t.Fatal(err)
	}
	parent, err := db.UpdateTask(ctx, parent, parent.Revision)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := ParseTaskMeta(parent.Meta)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Status != "waiting" || len(meta.Children) != 2 {
		t.Fatalf("父任务 %#v %#v", parent, meta)
	}
	for i, id := range meta.Children {
		child, err := db.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		cm, _ := ParseTaskMeta(child.Meta)
		cm.Summary = []string{"摘要一", "摘要二"}[i]
		child.Meta, child.Status = EncodeTaskMeta(cm), "done"
		if _, err := db.UpdateTask(ctx, child, child.Revision); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.WakeParents(ctx); err != nil {
		t.Fatal(err)
	}
	parent, err = db.GetTask(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Status != "todo" || !strings.Contains(parent.Notes, "摘要一") || !strings.Contains(parent.Notes, "摘要二") {
		t.Fatalf("父任务 %#v", parent)
	}
}
