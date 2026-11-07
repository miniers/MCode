package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

// quotaReply 是 composer.quota 的响应外壳，供各用例复用。
type quotaReply struct {
	Result wire.RemoteComposerQuotaResult `json:"result"`
	Error  *wire.RPCError                 `json:"error"`
}

// requestComposerQuota 通过 threadID 请求一次额度并解析响应。
func requestComposerQuota(t *testing.T, d *Dispatcher, threadID string, id int) quotaReply {
	t.Helper()
	ctx := context.Background()
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}
	req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"composer.quota","params":{"threadID":"%s"},"id":%d}`, threadID, id)
	raw := d.Handle(ctx, conn, []byte(req))
	var reply quotaReply
	if err := json.Unmarshal(raw, &reply); err != nil || reply.Error != nil {
		t.Fatalf("quota request failed: %v, %+v", err, reply.Error)
	}
	return reply
}

func TestComposerQuota_PiProvider(t *testing.T) {
	resetTime := time.Date(2026, 11, 2, 8, 30, 0, 0, time.UTC)
	balance := 3.5
	var gotProviderID string
	var calls int
	SetPiQuotaReader(func(ctx context.Context, providerID string) (PiQuotaSource, bool) {
		calls++
		gotProviderID = providerID
		return PiQuotaSource{Name: "magpie_jp", Windows: []PiQuotaWindow{
			{
				// Period 由数据源给出（magpie 自己推断好的周期标记）。
				Account: "claude", ID: "claude/5h", Label: "5 hours", Period: "5h",
				UsedPercent: 42.4, ResetsAt: resetTime,
			},
			{
				// Period 为空，靠窗口名推断；同一 provider 的第二张卡片带「 · 标识」。
				Account: "claude · 家里的号", ID: "claude-home/week", Label: "week",
				UsedPercent: 71.6, Active: true,
				Plan: "Max", Display: "$12.30 / $40", Text: true, Unlimited: true,
				Balance: &balance, Unit: "credits", Disabled: true,
				Source: "magpie", ReadAt: resetTime,
			},
		}}, true
	})
	t.Cleanup(func() { SetPiQuotaReader(nil) })

	b, d, _, _, threadID := setupWorkspaceTestEnv(t)
	model := "magpie-jp/auto/deepseek-v4.1-flash"
	b.mu.Lock()
	b.threads[threadID].summary.Provider = "pi"
	b.threads[threadID].summary.Model = &model
	b.mu.Unlock()

	reply := requestComposerQuota(t, d, threadID, 300)
	if calls != 1 {
		t.Fatalf("expected 1 quota read, got %d", calls)
	}
	if gotProviderID != "magpie-jp" {
		t.Fatalf("expected provider id 'magpie-jp', got %q", gotProviderID)
	}
	if len(reply.Result.Budgets) != 2 {
		t.Fatalf("expected 2 budgets (one per account), got %d: %+v", len(reply.Result.Budgets), reply.Result.Budgets)
	}

	first := reply.Result.Budgets[0]
	if first.Provider != "pi" || first.ProviderName != "magpie_jp" || first.Role != "lead" || first.State != "used" {
		t.Errorf("unexpected budget metadata: %+v", first)
	}
	if first.Account == nil || *first.Account != "claude" {
		t.Errorf("expected account 'claude', got %v", first.Account)
	}
	if first.Active != nil && *first.Active {
		t.Errorf("first account should not be marked active")
	}
	if len(first.Windows) != 1 {
		t.Fatalf("expected 1 window, got %d", len(first.Windows))
	}
	w0 := first.Windows[0]
	if w0.Period == nil || *w0.Period != "5h" || w0.Title != "5 hours" || w0.UsedPercent != 42.4 {
		t.Errorf("w0 mismatch: %+v", w0)
	}
	if w0.Label != "5 hours" || w0.Account != "claude" || w0.Active || w0.Plan != "" || w0.Balance != nil {
		t.Errorf("w0 optional fields mismatch: %+v", w0)
	}
	if w0.ResetsAt == nil || *w0.ResetsAt != "2026-11-02T08:30:00Z" {
		t.Errorf("w0 ResetsAt mismatch: %v", w0.ResetsAt)
	}

	second := reply.Result.Budgets[1]
	if second.Account == nil || *second.Account != "claude · 家里的号" {
		t.Errorf("expected second account 'claude · 家里的号', got %v", second.Account)
	}
	if second.Active == nil || !*second.Active {
		t.Errorf("expected second account marked active")
	}
	if len(second.Windows) != 1 {
		t.Fatalf("expected 1 window, got %d", len(second.Windows))
	}
	w1 := second.Windows[0]
	if w1.Period == nil || *w1.Period != "7d" || w1.Title != "week" || w1.UsedPercent != 71.6 {
		t.Errorf("w1 mismatch: %+v", w1)
	}
	if !w1.Active || !w1.Disabled || !w1.Text || !w1.Unlimited {
		t.Errorf("w1 flags mismatch: %+v", w1)
	}
	if w1.Plan != "Max" || w1.Display != "$12.30 / $40" || w1.Unit != "credits" || w1.Source != "magpie" {
		t.Errorf("w1 plan/display/unit/source mismatch: %+v", w1)
	}
	if w1.Balance == nil || *w1.Balance != 3.5 {
		t.Errorf("w1 balance mismatch: %v", w1.Balance)
	}
	if w1.ReadAt == nil || *w1.ReadAt != "2026-11-02T08:30:00Z" {
		t.Errorf("w1 ReadAt mismatch: %v", w1.ReadAt)
	}
	if w1.ResetsAt != nil {
		t.Errorf("w1 expected nil ResetsAt, got %v", *w1.ResetsAt)
	}
}

func TestComposerQuota_PiProviderWithoutMappedEndpoint(t *testing.T) {
	SetPiQuotaReader(func(ctx context.Context, providerID string) (PiQuotaSource, bool) {
		return PiQuotaSource{}, false
	})
	t.Cleanup(func() { SetPiQuotaReader(nil) })

	b, d, _, _, threadID := setupWorkspaceTestEnv(t)
	model := "anthropic/claude-sonnet-4-5"
	b.mu.Lock()
	b.threads[threadID].summary.Provider = "pi"
	b.threads[threadID].summary.Model = &model
	b.mu.Unlock()

	reply := requestComposerQuota(t, d, threadID, 301)
	if len(reply.Result.Budgets) != 0 {
		t.Fatalf("expected no budgets, got %+v", reply.Result.Budgets)
	}
}

func TestComposerQuota_PiProviderNeedsModelPrefix(t *testing.T) {
	var calls int
	SetPiQuotaReader(func(ctx context.Context, providerID string) (PiQuotaSource, bool) {
		calls++
		return PiQuotaSource{Name: "magpie_jp", Windows: []PiQuotaWindow{{Label: "5 hours", UsedPercent: 10}}}, true
	})
	t.Cleanup(func() { SetPiQuotaReader(nil) })

	b, d, _, _, threadID := setupWorkspaceTestEnv(t)
	// 没有 provider 段的模型 ID（pi 自带 provider 的写法各不相同），认不出端点就不请求。
	model := "unknown-model"
	b.mu.Lock()
	b.threads[threadID].summary.Provider = "pi"
	b.threads[threadID].summary.Model = &model
	b.mu.Unlock()

	reply := requestComposerQuota(t, d, threadID, 302)
	if calls != 0 {
		t.Fatalf("expected no quota read, got %d calls", calls)
	}
	if len(reply.Result.Budgets) != 0 {
		t.Fatalf("expected no budgets, got %+v", reply.Result.Budgets)
	}
}

func TestComposerQuota_PiProviderWithoutReader(t *testing.T) {
	SetPiQuotaReader(nil)

	b, d, _, _, threadID := setupWorkspaceTestEnv(t)
	model := "magpie-jp/auto/deepseek-v4.1-flash"
	b.mu.Lock()
	b.threads[threadID].summary.Provider = "pi"
	b.threads[threadID].summary.Model = &model
	b.mu.Unlock()

	reply := requestComposerQuota(t, d, threadID, 303)
	if len(reply.Result.Budgets) != 0 {
		t.Fatalf("expected no budgets without reader, got %+v", reply.Result.Budgets)
	}
}

func TestPiThreadProviderID(t *testing.T) {
	cases := map[string]string{
		"magpie-jp/auto/deepseek-v4.1-flash": "magpie-jp",
		"magpie/jp/auto/glm-5.3-flash":       "magpie",
		"single":                             "",
		"":                                   "",
		"  magpie-jp/auto/x  ":               "magpie-jp",
		"/leading":                           "",
	}
	for model, want := range cases {
		if got := piThreadProviderID(model); got != want {
			t.Errorf("piThreadProviderID(%q) = %q, want %q", model, got, want)
		}
	}
}
