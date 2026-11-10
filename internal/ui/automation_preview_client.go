package mcodeui

import (
	"context"
	"time"
)

type AutomationSchedulePreview struct {
	Times     []time.Time
	Exhausted bool
}

type automationPreviewClient interface {
	PreviewAutomationSchedule(ctx context.Context, sch AutomationSchedule, after time.Time, count int) (AutomationSchedulePreview, error)
}
