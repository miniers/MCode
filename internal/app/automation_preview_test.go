package app

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"mcode/internal/remote/wire"
	mcodeui "mcode/internal/ui"
)

func TestPreviewAutomationSchedule(t *testing.T) {
	ctx := context.Background()
	after := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	sch := mcodeui.AutomationSchedule{Type: "daily", TimeOfDay: "09:00", Timezone: "Asia/Shanghai"}
	client := &Client{}
	got, err := client.PreviewAutomationSchedule(ctx, sch, after, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Time{time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC), time.Date(2026, 11, 11, 1, 0, 0, 0, time.UTC), time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)}
	if !reflect.DeepEqual(got.Times, want) || got.Exhausted {
		t.Fatalf("preview = %+v, want %v", got, want)
	}
	for _, count := range []int{0, 6} {
		if _, err := client.PreviewAutomationSchedule(ctx, sch, after, count); err == nil {
			t.Errorf("count %d accepted", count)
		}
	}
	remote, err := (remoteAutomationService{client: client}).Preview(ctx, automationExtSchedule(sch), after, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(remote.Times) != len(want) || remote.Exhausted != got.Exhausted {
		t.Fatalf("remote preview = %+v, local = %+v", remote, got)
	}
	for i, timestamp := range remote.Times {
		if timestamp != want[i].Format(time.RFC3339) {
			t.Errorf("remote time %d = %s, want %s", i, timestamp, want[i])
		}
	}
	_, err = (remoteAutomationService{client: client}).Preview(ctx, wire.AutomationExtSchedule{Type: "cron", Expression: "99 * * * *"}, after, 3)
	if err == nil {
		t.Fatal("invalid cron accepted")
	}
	if _, err := client.PreviewAutomationSchedule(ctx, mcodeui.AutomationSchedule{Type: "cron", Expression: strings.Repeat(" ", 4097)}, after, 3); err == nil {
		t.Fatal("oversized cron accepted")
	}
}
