package credits

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAmountText(t *testing.T) {
	cases := []struct {
		name     string
		total    float64
		currency string
		want     string
	}{
		{"美元", 12.34, "USD", "$12.34"},
		{"人民币", 5, "CNY", "¥5.00"},
		{"千分位", 1234.5, "USD", "$1,234.50"},
		{"日元无小数", 1234, "JPY", "¥1,234"},
		{"未知币种", 5, "XYZ", "5.00 XYZ"},
		{"空币种", 5, "", "5.00"},
		{"负数", -12.34, "USD", "-$12.34"},
		{"币种大小写", 5, "usd", "$5.00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Credits{Total: tc.total, Currency: tc.currency}.AmountText()
			if got != tc.want {
				t.Errorf("AmountText() = %q，期望 %q", got, tc.want)
			}
		})
	}
}

func TestBreakdownText(t *testing.T) {
	monthly, toppedUp, granted := 10.0, 2.0, 0.5
	full := Credits{Currency: "USD", Monthly: &monthly, ToppedUp: &toppedUp, Granted: &granted}
	if got, want := full.BreakdownText(), "Monthly $10.00 · Topped up $2.00 · Granted $0.50"; got != want {
		t.Errorf("BreakdownText() = %q，期望 %q", got, want)
	}

	zero := 0.0
	if got := (Credits{Currency: "USD", Monthly: &zero, Granted: &zero}).BreakdownText(); got != "" {
		t.Errorf("零值拆分应为空串，实际 %q", got)
	}
	if got := (Credits{Currency: "USD"}).BreakdownText(); got != "" {
		t.Errorf("无拆分应为空串，实际 %q", got)
	}
}

func TestDepleted(t *testing.T) {
	if !(Credits{Total: 0, Usable: true}).Depleted() {
		t.Error("余额为 0 时应为已耗尽")
	}
	if !(Credits{Total: 5, Usable: false}).Depleted() {
		t.Error("不可用时应为已耗尽")
	}
	if (Credits{Total: 5, Usable: true}).Depleted() {
		t.Error("有余额且可用时不应为已耗尽")
	}
}

func TestExposesCredits(t *testing.T) {
	for provider, want := range map[string]bool{
		"deepseek":    true,
		"cline":       true,
		"commandcode": true,
		"zai":         false,
		"droid":       false,
		"cursor":      false,
	} {
		if got := ExposesCredits(provider); got != want {
			t.Errorf("ExposesCredits(%q) = %v，期望 %v", provider, got, want)
		}
	}
}

func TestReadUnsupported(t *testing.T) {
	_, err := Read(context.Background(), "cursor", Options{})
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("Read(cursor) 期望 ErrUnsupported，实际 %v", err)
	}
	_, err = Read(context.Background(), "zai", Options{APIKey: "k"})
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("Read(zai) 期望 ErrUnsupported，实际 %v", err)
	}
}

func TestReadPlanWindowsUnsupported(t *testing.T) {
	for _, provider := range []string{"deepseek", "commandcode", "cursor"} {
		if _, err := ReadPlanWindows(context.Background(), provider, Options{}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("ReadPlanWindows(%q) 期望 ErrUnsupported，实际 %v", provider, err)
		}
	}
}

func TestOptionsDefaults(t *testing.T) {
	var opts Options
	if opts.client() == nil {
		t.Error("默认 HTTP client 不应为 nil")
	}
	if opts.client().Timeout != defaultHTTPTimeout {
		t.Errorf("默认超时 = %v，期望 %v", opts.client().Timeout, defaultHTTPTimeout)
	}
	fixed := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	if got := (Options{Now: func() time.Time { return fixed }}).now(); !got.Equal(fixed) {
		t.Errorf("now() = %v，期望 %v", got, fixed)
	}
	if (Options{}).now().IsZero() {
		t.Error("默认 now() 不应为零值")
	}
}
