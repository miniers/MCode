package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"mcode/internal/providers/credits"
	mcodeui "mcode/internal/ui"
)

// stubProviderCredits 替换 readProviderCredits，记录入参并返回预设结果。
func stubProviderCredits(t *testing.T, fn func(ctx context.Context, provider string, opts credits.Options) (credits.Credits, error)) {
	t.Helper()
	prev := readProviderCredits
	readProviderCredits = fn
	t.Cleanup(func() { readProviderCredits = prev })
}

// stubProviderPlanWindows 替换 readProviderPlanWindows，记录入参并返回预设结果。
func stubProviderPlanWindows(t *testing.T, fn func(ctx context.Context, provider string, opts credits.Options) ([]credits.Window, error)) {
	t.Helper()
	prev := readProviderPlanWindows
	readProviderPlanWindows = fn
	t.Cleanup(func() { readProviderPlanWindows = prev })
}

func TestProviderCreditsMapsDeepSeekBalance(t *testing.T) {
	readAt := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	monthly := 10.0
	toppedUp := 2.34
	stubProviderCredits(t, func(_ context.Context, provider string, _ credits.Options) (credits.Credits, error) {
		if provider != "deepseek" {
			t.Errorf("provider = %q，期望 deepseek", provider)
		}
		return credits.Credits{
			Provider: provider, Total: 12.34, Currency: "USD",
			Monthly: &monthly, ToppedUp: &toppedUp,
			Usable: true, ReadAt: readAt,
		}, nil
	})

	got, err := (&Client{}).ProviderCredits(context.Background(), "deepseek")
	if err != nil {
		t.Fatalf("ProviderCredits 返回错误: %v", err)
	}
	if got.Provider != "deepseek" {
		t.Errorf("Provider = %q，期望 deepseek", got.Provider)
	}
	if got.Total != 12.34 || got.Currency != "USD" || !got.Usable {
		t.Errorf("余额字段映射有误: %+v", got)
	}
	if got.AmountText == "" {
		t.Error("AmountText 为空，期望非空")
	}
	if got.BreakdownText == "" {
		t.Error("BreakdownText 为空，期望非空")
	}
	if !got.ReadAt.Equal(readAt) {
		t.Errorf("ReadAt = %v，期望 %v", got.ReadAt, readAt)
	}
}

func TestProviderCreditsUnsupportedProvider(t *testing.T) {
	stubProviderCredits(t, func(context.Context, string, credits.Options) (credits.Credits, error) {
		t.Error("不支持的 provider 不应调用 readProviderCredits")
		return credits.Credits{}, nil
	})

	_, err := (&Client{}).ProviderCredits(context.Background(), "openai")
	if !errors.Is(err, mcodeui.ErrNotImplemented) {
		t.Fatalf("err = %v，期望满足 errors.Is(err, mcodeui.ErrNotImplemented)", err)
	}
}

func TestProviderQuotaZaiMapsWindows(t *testing.T) {
	resetsAt := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	stubProviderPlanWindows(t, func(_ context.Context, provider string, _ credits.Options) ([]credits.Window, error) {
		if provider != "zai" {
			t.Errorf("provider = %q，期望 zai", provider)
		}
		return []credits.Window{
			{Label: "5-hour limit", UsedPercent: 32.5, ResetsAt: resetsAt},
			{Label: "Weekly", UsedPercent: 10},
		}, nil
	})

	got, err := (&Client{}).ProviderQuota(context.Background(), "zai")
	if err != nil {
		t.Fatalf("ProviderQuota 返回错误: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("窗口数 = %d，期望 2", len(got))
	}
	if got[0].Label != "5-hour limit" || got[0].UsedPercent != 32.5 || !got[0].ResetsAt.Equal(resetsAt) {
		t.Errorf("首个窗口映射有误: %+v", got[0])
	}
	if got[1].Label != "Weekly" || got[1].UsedPercent != 10 {
		t.Errorf("第二个窗口映射有误: %+v", got[1])
	}
}

func TestProviderQuotaZaiUnsupportedBecomesNotImplemented(t *testing.T) {
	stubProviderPlanWindows(t, func(context.Context, string, credits.Options) ([]credits.Window, error) {
		return nil, credits.ErrUnsupported
	})

	_, err := (&Client{}).ProviderQuota(context.Background(), "zai")
	if !errors.Is(err, mcodeui.ErrNotImplemented) {
		t.Fatalf("err = %v，期望满足 errors.Is(err, mcodeui.ErrNotImplemented)", err)
	}
}
