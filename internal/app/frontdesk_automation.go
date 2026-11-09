package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"mcode/internal/automation"
	"mcode/internal/frontdesk"
)

type frontdeskAutomation struct {
	service interface {
		Create(context.Context, automation.CreateInput) (automation.Definition, error)
	}
	store interface {
		GetSetting(context.Context, string) (string, bool, error)
	}
}

func (a frontdeskAutomation) CreateAutomation(ctx context.Context, in frontdesk.AutomationInput) (frontdesk.AutomationResult, error) {
	schedule, err := frontdeskAutomationSchedule(in.Schedule)
	if err != nil {
		return frontdesk.AutomationResult{}, err
	}
	threadID, ok, err := a.store.GetSetting(ctx, "steward.feed_thread_id")
	if err != nil {
		return frontdesk.AutomationResult{}, err
	}
	if !ok || strings.TrimSpace(threadID) == "" {
		return frontdesk.AutomationResult{}, errors.New("总管会话不可用")
	}
	created, err := a.service.Create(ctx, automation.CreateInput{
		ProjectID: in.ProjectID, Name: strings.TrimSpace(in.Name), Prompt: strings.TrimSpace(in.Prompt),
		Spec:    automation.Spec{Schedule: schedule, RunMode: automation.RunModeStandalone},
		Enabled: true, CreatorThreadID: threadID,
	})
	if err != nil {
		return frontdesk.AutomationResult{}, err
	}
	result := frontdesk.AutomationResult{ID: created.ID, Name: created.Name}
	if created.NextRunAt != nil {
		result.NextRunAt = created.NextRunAt.Format(time.RFC3339)
	}
	return result, nil
}

// 调度类型与取值对应 MCP 的 schedule_type 和 schedule_value。
func frontdeskAutomationSchedule(raw string) (automation.Schedule, error) {
	typ, value, ok := strings.Cut(strings.TrimSpace(raw), ":")
	if !ok || strings.TrimSpace(value) == "" {
		return automation.Schedule{}, errors.New("schedule 格式应为类型:值")
	}
	typ, value = strings.TrimSpace(typ), strings.TrimSpace(value)
	s := automation.Schedule{Type: typ}
	switch typ {
	case "interval":
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds <= 0 {
			return automation.Schedule{}, errors.New("interval 必须是正整数秒数")
		}
		s.Seconds = seconds
	case "daily", "weekdays":
		s.TimeOfDay = value
	case "weekly":
		parts := strings.Split(value, ",")
		if len(parts) != 2 {
			return automation.Schedule{}, errors.New("weekly 格式应为星期数字,HH:mm")
		}
		day, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil || day < 0 || day > 6 {
			return automation.Schedule{}, errors.New("星期数字必须为 0 至 6")
		}
		s.DayOfWeek, s.TimeOfDay = day, strings.TrimSpace(parts[1])
	case "cron":
		s.Expression = value
	default:
		return automation.Schedule{}, fmt.Errorf("未知 schedule 类型: %s", typ)
	}
	if err := s.Validate(); err != nil {
		return automation.Schedule{}, fmt.Errorf("无效 schedule: %w", err)
	}
	return s, nil
}
