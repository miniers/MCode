package mcodeui

import (
	"strings"
	"testing"
	"time"
)

func TestAutomationVisualRowFields(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.Local)
	a := Automation{Name: "检查", Enabled: true, Schedule: AutomationSchedule{Type: "daily"}, NextRunAt: now, LastRunStatus: "failed"}
	all := automationVisualRowFields(a, "项目", "", false, now)
	one := automationVisualRowFields(a, "项目", "p", false, now)
	if all.Name != "检查" || !strings.HasPrefix(all.Schedule, "项目 · ") || strings.Contains(one.Schedule, "项目") || all.Next != "今天 12:00" || all.Result != "上次失败" || all.Plan != "" {
		t.Fatalf("字段错误: %+v %+v", all, one)
	}
	run := automationVisualRunFields(AutomationRun{Status: "failed", Summary: "摘要", Error: "错误"}, now)
	if run.Status != "失败" || run.Summary != "错误" || run.Time != "" {
		t.Fatalf("运行字段错误: %+v", run)
	}
}

func TestAutomationVisualStatusIndependence(t *testing.T) {
	now := time.Now()
	a := Automation{Enabled: false, LastRunStatus: "running", NextRunAt: now}
	row := automationVisualRowFields(a, "", "", true, now)
	if row.Plan != "已暂停" || row.Result != "运行中" || row.Next != "" {
		t.Fatal(row)
	}
	a.Status = "completed"
	a.LastRunStatus = "succeeded"
	row = automationVisualRowFields(a, "", "", false, now)
	if row.Plan != "已完成" || row.Result != "上次成功" {
		t.Fatal(row)
	}
	a.Enabled = true
	a.Status = ""
	a.LastRunStatus = "mystery"
	row = automationVisualRowFields(a, "", "", false, now)
	if row.Result != "mystery" || row.Plan != "" {
		t.Fatal(row)
	}
	if automationVisualRunFields(AutomationRun{}, now).Status != "未知" {
		t.Fatal("未知状态")
	}
}

func TestAutomationVisualTimeAcrossYear(t *testing.T) {
	now := time.Date(2026, 12, 31, 12, 0, 0, 0, time.Local)
	if got := automationVisualTime(now.AddDate(1, 0, 0), now); !strings.Contains(got, "2027") {
		t.Fatal(got)
	}
	if got := automationVisualTime(time.Time{}, now); got != "" {
		t.Fatal(got)
	}
}
