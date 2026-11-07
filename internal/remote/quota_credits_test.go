package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"mcode/internal/providers/credits"
	"mcode/internal/remote/wire"
)

// stubProviderCredentials 替换余额/方案额度读取与密钥查询，避免测试触碰 keyring 与网络。
func stubProviderCredentials(t *testing.T, readCredits func(context.Context, string, credits.Options) (credits.Credits, error), readWindows func(context.Context, string, credits.Options) ([]credits.Window, error)) {
	t.Helper()
	origCredits := readProviderCredits
	origWindows := readProviderPlanWindows
	origKey := lookupProviderAPIKey
	t.Cleanup(func() {
		readProviderCredits = origCredits
		readProviderPlanWindows = origWindows
		lookupProviderAPIKey = origKey
	})
	lookupProviderAPIKey = func(provider string) string { return "" }
	if readCredits != nil {
		readProviderCredits = readCredits
	}
	if readWindows != nil {
		readProviderPlanWindows = readWindows
	}
}

func quotaRequest(t *testing.T, d *Dispatcher, threadID string, id int) wire.RemoteComposerQuotaResult {
	t.Helper()
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}
	raw := d.Handle(context.Background(), conn, []byte(fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"composer.quota","params":{"threadID":"%s"},"id":%d}`, threadID, id)))
	var reply struct {
		Result wire.RemoteComposerQuotaResult `json:"result"`
		Error  *wire.RPCError                 `json:"error"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatalf("unmarshal error: %v, raw: %s", err, raw)
	}
	if reply.Error != nil {
		t.Fatalf("unexpected quota error: %+v", reply.Error)
	}
	return reply.Result
}

// 1. deepseek 线程：余额非空、State 为 used 的独立 budget
func TestComposerQuota_DeepSeekCreditsAmount(t *testing.T) {
	stubProviderCredentials(t, func(ctx context.Context, provider string, opts credits.Options) (credits.Credits, error) {
		return credits.Credits{Provider: provider, Total: 12.34, Currency: "USD", Usable: true}, nil
	}, nil)

	b, d, _, _, threadID := setupWorkspaceTestEnv(t)
	b.mu.Lock()
	b.threads[threadID].summary.Provider = "deepseek"
	b.mu.Unlock()

	result := quotaRequest(t, d, threadID, 300)
	if len(result.Budgets) != 1 {
		t.Fatalf("expected 1 budget, got %d: %+v", len(result.Budgets), result.Budgets)
	}
	budget := result.Budgets[0]
	if budget.Provider != "deepseek" || budget.Role != "lead" || budget.State != "used" {
		t.Errorf("unexpected budget metadata: %+v", budget)
	}
	if budget.Amount == nil || *budget.Amount != "$12.34" {
		t.Errorf("expected Amount '$12.34', got %v", budget.Amount)
	}
	if budget.Windows == nil || len(budget.Windows) != 0 {
		t.Errorf("expected empty non-nil windows, got %+v", budget.Windows)
	}
	if budget.ReadAt == nil || *budget.ReadAt == "" {
		t.Errorf("expected non-empty ReadAt")
	}
}

// 2. 余额为 0 时 State 为 attention
func TestComposerQuota_CreditsDepletedAttention(t *testing.T) {
	stubProviderCredentials(t, func(ctx context.Context, provider string, opts credits.Options) (credits.Credits, error) {
		return credits.Credits{Provider: provider, Total: 0, Currency: "USD", Usable: true}, nil
	}, nil)

	b, d, _, _, threadID := setupWorkspaceTestEnv(t)
	b.mu.Lock()
	b.threads[threadID].summary.Provider = "deepseek"
	b.mu.Unlock()

	result := quotaRequest(t, d, threadID, 301)
	if len(result.Budgets) != 1 {
		t.Fatalf("expected 1 budget, got %d", len(result.Budgets))
	}
	budget := result.Budgets[0]
	if budget.State != "attention" {
		t.Errorf("expected State 'attention' for depleted credits, got %q", budget.State)
	}
	if budget.Amount == nil || *budget.Amount != "$0.00" {
		t.Errorf("expected Amount '$0.00', got %v", budget.Amount)
	}
}

// 3. cline：方案窗口与余额合并在同一条 budget 中
func TestComposerQuota_ClineMergedWindowsAndCredits(t *testing.T) {
	resetTime := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	stubProviderCredentials(t, func(ctx context.Context, provider string, opts credits.Options) (credits.Credits, error) {
		return credits.Credits{Provider: provider, Total: 5, Currency: "USD", Usable: true}, nil
	}, func(ctx context.Context, provider string, opts credits.Options) ([]credits.Window, error) {
		return []credits.Window{
			{Label: "5-hour limit", UsedPercent: 12.5, ResetsAt: resetTime},
			{Label: "Weekly", UsedPercent: 40.4},
		}, nil
	})

	b, d, _, _, threadID := setupWorkspaceTestEnv(t)
	b.mu.Lock()
	b.threads[threadID].summary.Provider = "cline"
	b.mu.Unlock()

	result := quotaRequest(t, d, threadID, 302)
	if len(result.Budgets) != 1 {
		t.Fatalf("expected 1 merged budget, got %d: %+v", len(result.Budgets), result.Budgets)
	}
	budget := result.Budgets[0]
	if budget.Provider != "cline" || budget.State != "used" {
		t.Errorf("unexpected budget metadata: %+v", budget)
	}
	if budget.Amount == nil || *budget.Amount != "$5.00" {
		t.Errorf("expected merged Amount '$5.00', got %v", budget.Amount)
	}
	if len(budget.Windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(budget.Windows))
	}
	if budget.Windows[0].Title != "5-hour limit" || budget.Windows[0].UsedPercent != 12.5 {
		t.Errorf("w0 mismatch: %+v", budget.Windows[0])
	}
	if budget.Windows[0].ResetsAt == nil || *budget.Windows[0].ResetsAt != "2026-10-18T12:00:00Z" {
		t.Errorf("w0 ResetsAt mismatch: %v", budget.Windows[0].ResetsAt)
	}
	if budget.Windows[1].Title != "Weekly" || budget.Windows[1].UsedPercent != 40.4 || budget.Windows[1].ResetsAt != nil {
		t.Errorf("w1 mismatch: %+v", budget.Windows[1])
	}
}

// 4. 上游出错时：方案窗口标 unknown，余额读取失败不追加 budget，均不返回错误
func TestComposerQuota_ProviderCreditsUpstreamError(t *testing.T) {
	stubProviderCredentials(t, func(ctx context.Context, provider string, opts credits.Options) (credits.Credits, error) {
		return credits.Credits{}, errors.New("credentials unavailable")
	}, func(ctx context.Context, provider string, opts credits.Options) ([]credits.Window, error) {
		return nil, errors.New("plan windows unavailable")
	})

	b, d, _, _, threadID := setupWorkspaceTestEnv(t)
	b.mu.Lock()
	b.threads[threadID].summary.Provider = "cline"
	b.mu.Unlock()

	result := quotaRequest(t, d, threadID, 303)
	if len(result.Budgets) != 1 {
		t.Fatalf("expected 1 unknown budget, got %d: %+v", len(result.Budgets), result.Budgets)
	}
	budget := result.Budgets[0]
	if budget.State != "unknown" {
		t.Errorf("expected State 'unknown' on plan window failure, got %q", budget.State)
	}
	if budget.Windows == nil || len(budget.Windows) != 0 {
		t.Errorf("expected empty non-nil windows, got %+v", budget.Windows)
	}
	if budget.Amount != nil {
		t.Errorf("expected nil Amount on credits failure, got %v", budget.Amount)
	}

	// deepseek 只有余额读取且失败：不追加任何 budget
	b.mu.Lock()
	b.threads[threadID].summary.Provider = "deepseek"
	b.mu.Unlock()
	result = quotaRequest(t, d, threadID, 304)
	if len(result.Budgets) != 0 {
		t.Errorf("expected no budgets on deepseek credits failure, got %+v", result.Budgets)
	}
}
