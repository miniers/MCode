package mcodeui

import (
	"strings"
	"time"
)

const automationEditorMaxWidth float32 = 700

type automationVisualRow struct {
	Name, Schedule, Next, Plan, Result, Kind string
}

func automationVisualTime(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	at, now = at.Local(), now.Local()
	if at.Year() != now.Year() {
		return at.Format("2006-01-02 15:04")
	}
	return automationShortTime(at, now)
}

func automationVisualRowFields(a Automation, projectName, scopeID string, running bool, now time.Time) automationVisualRow {
	row := automationVisualRow{Name: a.Name, Schedule: automationScheduleText(a.Schedule)}
	if scopeID == "" && projectName != "" {
		row.Schedule = projectName + " · " + row.Schedule
	}
	if a.Status == "completed" {
		row.Plan = "已完成"
	} else if !a.Enabled {
		row.Plan, _ = automationCardStatus(a, false)
		// LastRunStatus can be running even when the schedule is paused.
		if row.Plan == "运行中" {
			row.Plan = "已暂停"
			if a.Status == "failed" && a.ConsecutiveFailures > 0 {
				row.Plan = "连续失败后暂停"
			}
		}
	} else {
		row.Next = automationVisualTime(a.NextRunAt, now)
	}
	status, kind := automationRunStatus(a.LastRunStatus)
	if running || a.LastRunStatus == "running" {
		row.Result, row.Kind = "运行中", "running"
	} else if a.LastRunStatus != "" {
		row.Kind = kind
		switch kind {
		case "succeeded":
			row.Result = "上次成功"
		case "failed":
			row.Result = "上次失败"
		default:
			row.Result = status
		}
	}
	return row
}

type automationVisualRun struct{ Status, Kind, Summary, Time, Duration string }

func automationVisualRunFields(run AutomationRun, now time.Time) automationVisualRun {
	status, kind := automationRunStatus(run.Status)
	summary := run.Summary
	if kind == "failed" && strings.TrimSpace(run.Error) != "" {
		summary = run.Error
	}
	duration := automationRunDuration(run)
	if run.Status == "running" && run.FinishedAt.IsZero() {
		duration = "—"
	}
	return automationVisualRun{Status: status, Kind: kind, Summary: summary, Time: automationVisualTime(run.StartedAt, now), Duration: duration}
}
