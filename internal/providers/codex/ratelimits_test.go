package codex

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseRateLimitsResponse_RateLimitsByLimitId(t *testing.T) {
	raw := json.RawMessage(`{
		"rateLimitsByLimitId": {
			"limit-main": {
				"limitId": "codex-test",
				"limitName": "Standard",
				"planType": "pro",
				"primary": {
					"usedPercent": 24.5,
					"windowDurationMins": 300,
					"resetsAt": 1729168800
				},
				"secondary": {
					"usedPercent": 50.0,
					"windowDurationMins": 10080,
					"resetsAt": "2026-10-24T12:00:00Z"
				}
			}
		}
	}`)

	limits, err := ParseRateLimitsResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if limits.PlanType != "Pro 20x" {
		t.Errorf("expected plan Pro 20x, got %q", limits.PlanType)
	}

	if len(limits.Windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(limits.Windows))
	}

	// primary 窗口
	w0 := limits.Windows[0]
	if w0.ID != "codex-test-primary" {
		t.Errorf("w0 ID mismatch: got %q", w0.ID)
	}
	if w0.Title != "5-hour limit" {
		t.Errorf("w0 Title mismatch: got %q", w0.Title)
	}
	if w0.Period != "5h" {
		t.Errorf("w0 Period mismatch: got %q", w0.Period)
	}
	if w0.UsedPercent != 24.5 {
		t.Errorf("w0 UsedPercent mismatch: got %f", w0.UsedPercent)
	}
	if w0.ResetsAt == nil || w0.ResetsAt.Unix() != 1729168800 {
		t.Errorf("w0 ResetsAt mismatch: got %v", w0.ResetsAt)
	}

	// secondary 窗口
	w1 := limits.Windows[1]
	if w1.ID != "codex-test-secondary" {
		t.Errorf("w1 ID mismatch: got %q", w1.ID)
	}
	if w1.Title != "Weekly" {
		t.Errorf("w1 Title mismatch: got %q", w1.Title)
	}
	if w1.Period != "7d" {
		t.Errorf("w1 Period mismatch: got %q", w1.Period)
	}
	if w1.UsedPercent != 50.0 {
		t.Errorf("w1 UsedPercent mismatch: got %f", w1.UsedPercent)
	}
	expectedTime, _ := time.Parse(time.RFC3339, "2026-10-24T12:00:00Z")
	if w1.ResetsAt == nil || !w1.ResetsAt.Equal(expectedTime) {
		t.Errorf("w1 ResetsAt mismatch: got %v, expected %v", w1.ResetsAt, expectedTime)
	}
}

func TestParseRateLimitsResponse_SingleRateLimitsSnakeCase(t *testing.T) {
	raw := json.RawMessage(`{
		"rate_limits": {
			"limit_id": "codex",
			"plan_type": "plus",
			"primary": {
				"used_percent": 0.0,
				"window_duration_mins": 1440,
				"resets_at": 1729168800000
			}
		}
	}`)

	limits, err := ParseRateLimitsResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if limits.PlanType != "Plus" {
		t.Errorf("expected plan Plus, got %q", limits.PlanType)
	}

	if len(limits.Windows) != 1 {
		t.Fatalf("expected 1 window, got %d", len(limits.Windows))
	}

	w := limits.Windows[0]
	if w.ID != "codex-primary" {
		t.Errorf("w ID mismatch: got %q", w.ID)
	}
	if w.Title != "Daily" {
		t.Errorf("w Title mismatch: got %q", w.Title)
	}
	if w.Period != "1d" {
		t.Errorf("w Period mismatch: got %q", w.Period)
	}
	if w.UsedPercent != 0.0 {
		t.Errorf("w UsedPercent mismatch: got %f", w.UsedPercent)
	}
	if w.ResetsAt == nil || w.ResetsAt.Unix() != 1729168800 {
		t.Errorf("w ResetsAt mismatch: got %v", w.ResetsAt)
	}
}

func TestParseRateLimitsResponse_MultipleSnapshots(t *testing.T) {
	raw := json.RawMessage(`{
		"rateLimitsByLimitId": {
			"limit-b": {
				"limitId": "limit-b",
				"limitName": "Search",
				"planType": "prolite",
				"primary": {
					"usedPercent": 10.0,
					"windowDurationMins": 300
				}
			},
			"limit-a": {
				"limitId": "limit-a",
				"limitName": "Code",
				"planType": "prolite",
				"primary": {
					"usedPercent": 30.0,
					"windowDurationMins": 300
				}
			}
		}
	}`)

	limits, err := ParseRateLimitsResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if limits.PlanType != "Pro 5x" {
		t.Errorf("expected plan Pro 5x, got %q", limits.PlanType)
	}

	if len(limits.Windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(limits.Windows))
	}

	// 应按 key 升序排序：limit-a 先于 limit-b
	if limits.Windows[0].ID != "limit-a-primary" || limits.Windows[0].Title != "5-hour limit · Code" {
		t.Errorf("w0 mismatch: %+v", limits.Windows[0])
	}
	if limits.Windows[1].ID != "limit-b-primary" || limits.Windows[1].Title != "5-hour limit · Search" {
		t.Errorf("w1 mismatch: %+v", limits.Windows[1])
	}
}

func TestParseRateLimitsResponse_EmptyAndMalformed(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte(""),
		[]byte("null"),
		[]byte("{}"),
		[]byte(`{"rateLimitsByLimitId": {}}`),
		[]byte(`{"rateLimits": {}}`),
		[]byte(`{"rateLimits": {"primary": {}}}`), // 无 usedPercent
	}

	for i, c := range cases {
		limits, err := ParseRateLimitsResponse(c)
		if err != nil {
			t.Errorf("case %d failed with error: %v", i, err)
		}
		if len(limits.Windows) != 0 {
			t.Errorf("case %d expected 0 windows, got %d", i, len(limits.Windows))
		}
	}
}

func TestFormatCodexPlanName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"pro", "Pro 20x"},
		{"PRO", "Pro 20x"},
		{"prolite", "Pro 5x"},
		{"pro lite", "Pro 5x"},
		{"ent26", "Enterprise"},
		{"hc", "Enterprise"},
		{"self_serve_business_prolite", "Business Premium"},
		{"self_serve_business_usage_based", "Business"},
		{"enterprise_cbp_automation", "Enterprise (Automation)"},
		{"enterprise_cbp_usage_based", "Enterprise"},
		{"education", "Edu"},
		{"edu_plus", "Edu Plus"},
		{"edu_pro", "Edu Pro"},
		{"plus", "Plus"},
		{"team", "Team"},
		{"free", "Free"},
		{"", ""},
	}

	for _, tt := range tests {
		got := FormatCodexPlanName(tt.input)
		if got != tt.expected {
			t.Errorf("FormatCodexPlanName(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestFormatWindowTitleAndPeriod(t *testing.T) {
	if title := formatWindowTitle(300, ""); title != "5-hour limit" {
		t.Errorf("expected '5-hour limit', got %q", title)
	}
	if title := formatWindowTitle(1440, ""); title != "Daily" {
		t.Errorf("expected 'Daily', got %q", title)
	}
	if title := formatWindowTitle(10080, ""); title != "Weekly" {
		t.Errorf("expected 'Weekly', got %q", title)
	}
	if title := formatWindowTitle(43200, ""); title != "Monthly" {
		t.Errorf("expected 'Monthly', got %q", title)
	}
	if title := formatWindowTitle(2880, ""); title != "2-day limit" {
		t.Errorf("expected '2-day limit', got %q", title)
	}
	if title := formatWindowTitle(120, ""); title != "2-hour limit" {
		t.Errorf("expected '2-hour limit', got %q", title)
	}
	if title := formatWindowTitle(0, ""); title != "Usage limit" {
		t.Errorf("expected 'Usage limit', got %q", title)
	}

	if p := formatWindowPeriod(300); p != "5h" {
		t.Errorf("expected '5h', got %q", p)
	}
	if p := formatWindowPeriod(1440); p != "1d" {
		t.Errorf("expected '1d', got %q", p)
	}
	if p := formatWindowPeriod(10080); p != "7d" {
		t.Errorf("expected '7d', got %q", p)
	}
	if p := formatWindowPeriod(43200); p != "30d" {
		t.Errorf("expected '30d', got %q", p)
	}
}
