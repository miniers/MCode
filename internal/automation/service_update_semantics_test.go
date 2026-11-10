package automation

import (
	"context"
	"errors"
	"testing"
	"time"

	"mcode/internal/store"
)

func TestUpdateScheduleAndEnableSemantics(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := newFakeClock(start)
	svc, db := setupTestService(t, clock, newFakeDriver())
	defer db.Close()
	def, err := svc.Create(ctx, CreateInput{ProjectID: "p", Name: "old", Prompt: "prompt", Enabled: true, Spec: Spec{Schedule: Schedule{Type: "interval", Seconds: 3600}}})
	if err != nil {
		t.Fatal(err)
	}
	def.Spec.State.ConsecutiveFailures = 2
	def.Automation.Schedule, err = EncodeSpec(def.Spec)
	if err != nil {
		t.Fatal(err)
	}
	def.Automation, err = db.UpdateAutomation(ctx, def.Automation, def.Revision)
	if err != nil {
		t.Fatal(err)
	}
	originalNext := *def.NextRunAt
	clock.Advance(10 * time.Minute)
	name := "new"
	def, err = svc.Update(ctx, def.ID, UpdateInput{Name: &name}, def.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !def.NextRunAt.Equal(originalNext) || def.Spec.State.ConsecutiveFailures != 2 {
		t.Fatalf("name update changed state: %+v", def)
	}
	copySpec := def.Spec
	def, err = svc.Update(ctx, def.ID, UpdateInput{Spec: &copySpec}, def.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !def.NextRunAt.Equal(originalNext) || def.Spec.State.ConsecutiveFailures != 2 {
		t.Fatalf("same schedule changed state: %+v", def)
	}
	copySpec.Schedule.Seconds = 7200
	def, err = svc.Update(ctx, def.ID, UpdateInput{Spec: &copySpec}, def.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !def.NextRunAt.Equal(clock.Now().Add(2*time.Hour)) || def.Spec.State.ConsecutiveFailures != 2 {
		t.Fatalf("changed schedule: %+v", def)
	}
	if _, err = svc.Update(ctx, def.ID, UpdateInput{Name: &name}, def.Revision-1); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("expected revision conflict, got %v", err)
	}
	enabled := true
	def, err = svc.Update(ctx, def.ID, UpdateInput{Enabled: &enabled}, def.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if def.Spec.State.ConsecutiveFailures != 0 || !def.NextRunAt.Equal(clock.Now().Add(2*time.Hour)) {
		t.Fatalf("reset failed: %+v", def)
	}
	enabled = false
	def, err = svc.Update(ctx, def.ID, UpdateInput{Enabled: &enabled}, def.Revision)
	if err != nil {
		t.Fatal(err)
	}
	def, err = svc.Update(ctx, def.ID, UpdateInput{Name: &name, Spec: &copySpec}, def.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if def.Enabled || def.Status != StatusPaused || def.NextRunAt != nil {
		t.Fatalf("edit resumed paused automation: %+v", def)
	}
}

func TestUpdateScheduleDateComparesTimePoints(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	svc, db := setupTestService(t, clock, newFakeDriver())
	defer db.Close()
	date := clock.Now().Add(24 * time.Hour)
	def, err := svc.Create(ctx, CreateInput{ProjectID: "p", Name: "n", Prompt: "p", Enabled: true, Spec: Spec{Schedule: Schedule{Type: "once", Date: &date}}})
	if err != nil {
		t.Fatal(err)
	}
	// 用人为设置的下一次运行时间检测是否发生重算。
	custom := date.Add(-time.Hour)
	def.Automation.NextRunAt = &custom
	def.Automation, err = db.UpdateAutomation(ctx, def.Automation, def.Revision)
	if err != nil {
		t.Fatal(err)
	}
	sameDate := date.In(time.FixedZone("other", 3600))
	spec := def.Spec
	spec.Schedule.Date = &sameDate
	def, err = svc.Update(ctx, def.ID, UpdateInput{Spec: &spec}, def.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !def.NextRunAt.Equal(custom) {
		t.Fatalf("equal dates rescheduled: %v", def.NextRunAt)
	}
}
