package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestNormalizeSwarmStatus 覆盖 5 态收敛：未知取值一律归一到 idle。
func TestNormalizeSwarmStatus(t *testing.T) {
	cases := map[string]string{
		"":                swarmStatusIdle,
		"idle":            swarmStatusIdle,
		"unknown":         swarmStatusIdle,
		"stopped":         swarmStatusIdle,
		"cancelled":       swarmStatusIdle,
		"running":         swarmStatusRunning,
		" RUNNING ":       swarmStatusRunning,
		"waiting_review":  swarmStatusWaitingReview,
		"needs_human":     swarmStatusWaitingReview,
		"completed":       swarmStatusCompleted,
		"succeeded":       swarmStatusCompleted,
		"failed":          swarmStatusErrored,
		"errored":         swarmStatusErrored,
		"error":           swarmStatusErrored,
		"custom_status":   swarmStatusIdle,
		"waiting_for_ok?": swarmStatusIdle,
	}
	for in, want := range cases {
		if got := normalizeSwarmStatus(in); got != want {
			t.Errorf("normalizeSwarmStatus(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// TestMapSwarmBeeStatus 验证 PendingMerge 与 ReviewState 派生 waiting_review。
func TestMapSwarmBeeStatus(t *testing.T) {
	cases := []struct {
		name string
		bee  SwarmBee
		want string
	}{
		{"pending merge", SwarmBee{Status: "completed", PendingMerge: true}, swarmStatusWaitingReview},
		{"needs human", SwarmBee{Status: "completed", ReviewState: "needs_human"}, swarmStatusWaitingReview},
		{"review approved", SwarmBee{Status: "completed", ReviewState: "approved"}, swarmStatusCompleted},
		{"running", SwarmBee{Status: "running"}, swarmStatusRunning},
		{"failed", SwarmBee{Status: "failed"}, swarmStatusErrored},
		{"stopped", SwarmBee{Status: "stopped"}, swarmStatusIdle},
		{"empty", SwarmBee{}, swarmStatusIdle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MapSwarmBeeStatus(tc.bee); got != tc.want {
				t.Errorf("MapSwarmBeeStatus(%+v) = %q, 期望 %q", tc.bee, got, tc.want)
			}
		})
	}
}

// TestSwarmStatusLabelAndTone 验证标签文案与 StatusDot 状态一一对应。
func TestSwarmStatusLabelAndTone(t *testing.T) {
	labels := map[string]string{
		swarmStatusIdle:          "空闲",
		swarmStatusRunning:       "运行中",
		swarmStatusWaitingReview: "待审查",
		swarmStatusCompleted:     "已完成",
		swarmStatusErrored:       "失败",
		"unknown":                "空闲",
	}
	for status, want := range labels {
		if got := swarmStatusLabel(status); got != want {
			t.Errorf("swarmStatusLabel(%q) = %q, 期望 %q", status, got, want)
		}
	}

	wantTone := map[string]Status{
		swarmStatusIdle:          StatusIdle,
		swarmStatusRunning:       StatusRunning,
		swarmStatusWaitingReview: StatusAttention,
		swarmStatusCompleted:     StatusIdle,
		swarmStatusErrored:       StatusError,
		"unknown":                StatusIdle,
	}
	tt := ui.NewTester(func(c *ui.Context) {
		for status, want := range wantTone {
			if got, _ := swarmStatusTone(c, status); got != want {
				t.Errorf("swarmStatusTone(%q) = %v, 期望 %v", status, got, want)
			}
		}
	}, 200, 200)
	tt.Frame()
}

// TestSwarmReportStatusText 验证汇报条目匹配仍使用历史状态文案。
func TestSwarmReportStatusText(t *testing.T) {
	cases := map[string]string{
		"running":   "运行中",
		"completed": "已完成",
		"failed":    "失败",
		"stopped":   "已停止",
	}
	for in, want := range cases {
		if got := swarmReportStatusText(in); got != want {
			t.Errorf("swarmReportStatusText(%q) = %q, 期望 %q", in, got, want)
		}
	}
}
