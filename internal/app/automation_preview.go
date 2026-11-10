package app

import (
	"context"
	"fmt"
	"time"

	"mcode/internal/automation"
	mcodeui "mcode/internal/ui"
)

func (c *Client) PreviewAutomationSchedule(ctx context.Context, sch mcodeui.AutomationSchedule, after time.Time, count int) (mcodeui.AutomationSchedulePreview, error) {
	if sch.Type == "cron" && len(sch.Expression) > 4096 {
		return mcodeui.AutomationSchedulePreview{}, fmt.Errorf("cron expression exceeds 4096 bytes")
	}
	result, err := automation.PreviewSchedule(ctx, AutomationScheduleFromUI(sch), after, count)
	return mcodeui.AutomationSchedulePreview{Times: result.Times, Exhausted: result.Exhausted}, err
}
