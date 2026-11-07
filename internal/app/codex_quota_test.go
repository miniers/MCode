package app

import (
	"testing"
	"time"

	"mcode/internal/providers/codex"
)

// 直连 Codex 只有一组窗口，套餐与读取时间来自本机登录的实时读取。
func TestCodexQuotaWindowsDirect(t *testing.T) {
	reset := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	readAt := time.Date(2026, 10, 18, 11, 58, 0, 0, time.UTC)
	windows := codexQuotaWindows(codex.RateLimits{
		Source:   codex.QuotaSourceOpenAI,
		PlanType: "Pro 20x",
		ReadAt:   readAt,
		Windows: []codex.RateLimitWindow{
			{ID: "codex-primary", Title: "5-hour limit", Period: "5h", UsedPercent: 25.4, ResetsAt: &reset},
			{ID: "codex-secondary", Title: "Weekly", Period: "7d", UsedPercent: 60},
		},
	})
	if len(windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(windows))
	}
	for _, w := range windows {
		if w.Account != "" {
			t.Errorf("direct codex must not carry an account, got %q", w.Account)
		}
		if w.Source != codex.QuotaSourceOpenAI || w.Plan != "Pro 20x" || !w.ReadAt.Equal(readAt) {
			t.Errorf("unexpected source metadata: %+v", w)
		}
	}
	if !windows[0].ResetsAt.Equal(reset) || windows[0].UsedPercent != 25.4 {
		t.Errorf("unexpected first window: %+v", windows[0])
	}
	if !windows[1].ResetsAt.IsZero() {
		t.Errorf("missing reset time must stay zero, got %v", windows[1].ResetsAt)
	}
}

// Magpie 的每张 Codex 卡片各带账号、套餐和读取时间；未知读取时间保持零值。
func TestCodexQuotaWindowsMagpieAccounts(t *testing.T) {
	readAt := time.Date(2026, 10, 28, 2, 58, 16, 0, time.UTC)
	lastServedAt := time.Date(2026, 10, 28, 2, 40, 0, 0, time.UTC)
	windows := codexQuotaWindows(codex.RateLimits{
		Source: codex.QuotaSourceMagpie,
		Accounts: []codex.AccountQuota{
			{
				Account:      "a@b.com",
				PlanType:     "Plus",
				Provider:     "codex",
				User:         "a@example.com",
				Kind:         "subscription",
				Last:         true,
				LastServedAt: lastServedAt,
				ReadAt:       readAt,
				Windows: []codex.RateLimitWindow{
					{ID: "a@b.com/5 hours", Title: "5 hours", Period: "5h", UsedPercent: 85},
					{ID: "a@b.com/30 days", Title: "30 days", Period: "30d", UsedPercent: 10},
				},
			},
			{
				Account:  "{{EMAIL_d949a3a8}}",
				PlanType: "Free",
				Windows: []codex.RateLimitWindow{
					{ID: "{{EMAIL_d949a3a8}}/30 days", Title: "30 days", Period: "30d", UsedPercent: 0},
				},
			},
		},
	})
	if len(windows) != 3 {
		t.Fatalf("expected 3 windows, got %d: %+v", len(windows), windows)
	}
	first, second, third := windows[0], windows[1], windows[2]
	if first.Account != "a@b.com" || first.Plan != "Plus" || first.Source != codex.QuotaSourceMagpie {
		t.Errorf("unexpected first window metadata: %+v", first)
	}
	if !first.ReadAt.Equal(readAt) || first.Period != "5h" {
		t.Errorf("unexpected first window: %+v", first)
	}
	if second.Period != "30d" || second.Account != "a@b.com" {
		t.Errorf("unexpected second window: %+v", second)
	}
	// 卡片上的路由元数据要落到该卡片的每个窗口上，另一张卡不受影响。
	if first.Provider != "codex" || first.User != "a@example.com" || first.Kind != "subscription" {
		t.Errorf("card route metadata missing on first window: %+v", first)
	}
	if !first.Last || !first.LastServedAt.Equal(lastServedAt) {
		t.Errorf("card last-served metadata missing on first window: %+v", first)
	}
	if second.Provider != "codex" || second.User != "a@example.com" || !second.Last {
		t.Errorf("same card windows must share route metadata: %+v", second)
	}
	if third.Provider != "" || third.User != "" || third.Kind != "" || third.Last {
		t.Errorf("second card must not inherit route metadata: %+v", third)
	}
	if third.Account != "{{EMAIL_d949a3a8}}" || third.Plan != "Free" {
		t.Errorf("second card must stay a separate account: %+v", third)
	}
	if !third.ReadAt.IsZero() {
		t.Errorf("unknown card read time must stay zero, got %v", third.ReadAt)
	}
}
