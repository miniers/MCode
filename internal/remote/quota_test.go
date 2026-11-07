package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"mcode/internal/config"
	"mcode/internal/providers/agy/accounts"
	"mcode/internal/providers/agy/quota"
	"mcode/internal/providers/claude"
	"mcode/internal/providers/codex"
	"mcode/internal/remote/wire"
)

func TestComposerQuota_AgyBoundAccountAndWindows(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Truncate(time.Second)
	index := accounts.Index{Version: 1, DefaultAccountID: accounts.SystemAccountID, Accounts: []accounts.Account{
		{ID: accounts.SystemAccountID, Status: accounts.StatusActive},
		{
			ID: "isolated", Email: "bound@example.com", Status: accounts.StatusActive,
			QuotaReadAt: now.Add(-time.Hour).Format(time.RFC3339), QuotaStale: true,
			Quota: map[string]accounts.QuotaGroup{
				quota.GroupClaudeGPT:         {UsedPercent: 80, ResetAt: now.Add(-time.Minute).Format(time.RFC3339)},
				quota.GroupClaudeGPT + " 7d": {UsedPercent: 45, ResetAt: now.Add(24 * time.Hour).Format(time.RFC3339)},
				quota.GroupGemini:            {UsedPercent: 90, ResetAt: now.Add(-time.Minute).Format(time.RFC3339)},
				quota.GroupGemini + " 7d":    {UsedPercent: 30, ResetAt: now.Add(24 * time.Hour).Format(time.RFC3339)},
			},
		},
	}}
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agy-accounts.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	mgr, err := accounts.NewManager(accounts.Options{DataDir: dir, HostHome: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	mgr.Bind("conversation", "isolated")
	b, d, _, _, threadID := setupWorkspaceTestEnv(t)
	b.SetAgyAccounts(mgr)
	b.mu.Lock()
	b.threads[threadID].summary.Provider = "agy"
	model := "claude-sonnet-4-5"
	b.threads[threadID].summary.Model = &model
	b.threads[threadID].sessionID = "conversation"
	b.mu.Unlock()
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}
	raw := d.Handle(context.Background(), conn, []byte(fmt.Sprintf(`{"jsonrpc":"2.0","method":"composer.quota","params":{"threadID":"%s"},"id":1}`, threadID)))
	var reply struct {
		Result wire.RemoteComposerQuotaResult `json:"result"`
		Error  *wire.RPCError                 `json:"error"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil || reply.Error != nil {
		t.Fatalf("quota request failed: %v, %+v", err, reply.Error)
	}
	if len(reply.Result.Budgets) != 1 {
		t.Fatalf("missing agy budget: %s", raw)
	}
	budget := reply.Result.Budgets[0]
	if budget.Profile == nil || *budget.Profile != "bound@example.com" || budget.State != "stale" {
		t.Fatalf("wrong bound account: %+v", budget)
	}
	if len(budget.Windows) != 2 || budget.Windows[0].UsedPercent != 0 || budget.Windows[1].UsedPercent != 30 {
		t.Fatalf("incorrect windows: %+v", budget.Windows)
	}
	if budget.Windows[0].Title != "Gemini Models 5h" || budget.Windows[1].Title != "Gemini Models 7d" {
		t.Fatalf("incorrect window titles: %+v", budget.Windows)
	}
	if budget.Windows[0].ResetsAt != nil || budget.Amount != nil || budget.ReadAt == nil {
		t.Fatalf("invalid nullable contract: %s", raw)
	}
}

// TestComposerQuota_ValidationAndEmptyBudgets 测试 composer.quota 参数互斥与空 budgets 返回
func TestComposerQuota_ValidationAndEmptyBudgets(t *testing.T) {
	ctx := context.Background()
	_, d, _, projectID, threadID := setupWorkspaceTestEnv(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	// 1. 通过 threadID 成功查询配额，返回空数组 budgets: []
	threadReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"composer.quota","params":{"threadID":"%s"},"id":30}`, threadID)
	respRaw := d.Handle(ctx, conn, []byte(threadReq))
	var threadResp struct {
		Result wire.RemoteComposerQuotaResult `json:"result"`
		Error  *wire.RPCError                 `json:"error"`
	}
	if err := json.Unmarshal(respRaw, &threadResp); err != nil {
		t.Fatalf("unmarshal error: %v, raw: %s", err, string(respRaw))
	}
	if threadResp.Error != nil {
		t.Fatalf("unexpected error for thread quota: %+v", threadResp.Error)
	}
	if threadResp.Result.Budgets == nil || len(threadResp.Result.Budgets) != 0 {
		t.Errorf("expected empty non-null budgets, got %+v", threadResp.Result.Budgets)
	}

	// 验证 JSON 字符串中确实是 "[]" 而非 "null"
	var rawMap map[string]any
	_ = json.Unmarshal(respRaw, &rawMap)
	resMap, ok := rawMap["result"].(map[string]any)
	if !ok {
		t.Fatalf("result field not object: %+v", rawMap)
	}
	budgetsVal, exists := resMap["budgets"]
	if !exists {
		t.Fatalf("budgets key missing in result")
	}
	bSlice, isSlice := budgetsVal.([]any)
	if !isSlice || len(bSlice) != 0 {
		t.Errorf("expected budgets to be empty JSON array [], got: %v", budgetsVal)
	}

	// 2. 通过 projectID 成功查询配额
	projReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"composer.quota","params":{"projectID":"%s"},"id":31}`, projectID)
	projRespRaw := d.Handle(ctx, conn, []byte(projReq))
	var projResp struct {
		Result wire.RemoteComposerQuotaResult `json:"result"`
		Error  *wire.RPCError                 `json:"error"`
	}
	if err := json.Unmarshal(projRespRaw, &projResp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if projResp.Error != nil {
		t.Fatalf("unexpected error for project quota: %+v", projResp.Error)
	}
	if projResp.Result.Budgets == nil || len(projResp.Result.Budgets) != 0 {
		t.Errorf("expected empty non-null budgets, got %+v", projResp.Result.Budgets)
	}

	// 3. 同时提供 threadID 与 projectID 报错 InvalidParams
	bothReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"composer.quota","params":{"threadID":"%s","projectID":"%s"},"id":32}`, threadID, projectID)
	bothRespRaw := d.Handle(ctx, conn, []byte(bothReq))
	var bothResp struct {
		Error *wire.RPCError `json:"error"`
	}
	_ = json.Unmarshal(bothRespRaw, &bothResp)
	if bothResp.Error == nil || bothResp.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams error when both IDs provided, got: %+v", bothResp.Error)
	}

	// 4. 两个参数均未提供报错 InvalidParams
	noneReq := `{"jsonrpc":"2.0","method":"composer.quota","params":{},"id":33}`
	noneRespRaw := d.Handle(ctx, conn, []byte(noneReq))
	var noneResp struct {
		Error *wire.RPCError `json:"error"`
	}
	_ = json.Unmarshal(noneRespRaw, &noneResp)
	if noneResp.Error == nil || noneResp.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams error when neither ID provided, got: %+v", noneResp.Error)
	}

	// 5. 不存在的 threadID 报错 ResourceNotFound
	missingThreadID := uuid.New().String()
	missingThreadReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"composer.quota","params":{"threadID":"%s"},"id":34}`, missingThreadID)
	missingThreadRaw := d.Handle(ctx, conn, []byte(missingThreadReq))
	var missingThreadResp struct {
		Error *wire.RPCError `json:"error"`
	}
	_ = json.Unmarshal(missingThreadRaw, &missingThreadResp)
	if missingThreadResp.Error == nil || missingThreadResp.Error.Code != wire.ErrCodeResourceNotFound {
		t.Errorf("expected ResourceNotFound for missing thread, got: %+v", missingThreadResp.Error)
	}

	// 6. 不存在的 projectID 报错 ResourceNotFound
	missingProjID := uuid.New().String()
	missingProjReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"composer.quota","params":{"projectID":"%s"},"id":35}`, missingProjID)
	missingProjRaw := d.Handle(ctx, conn, []byte(missingProjReq))
	var missingProjResp struct {
		Error *wire.RPCError `json:"error"`
	}
	_ = json.Unmarshal(missingProjRaw, &missingProjResp)
	if missingProjResp.Error == nil || missingProjResp.Error.Code != wire.ErrCodeResourceNotFound {
		t.Errorf("expected ResourceNotFound for missing project, got: %+v", missingProjResp.Error)
	}

	// 7. 格式无效的 UUID 报错 InvalidParams
	invalidUUIDReq := `{"jsonrpc":"2.0","method":"composer.quota","params":{"threadID":"not-a-valid-uuid"},"id":36}`
	invalidUUIDRaw := d.Handle(ctx, conn, []byte(invalidUUIDReq))
	var invalidUUIDResp struct {
		Error *wire.RPCError `json:"error"`
	}
	_ = json.Unmarshal(invalidUUIDRaw, &invalidUUIDResp)
	if invalidUUIDResp.Error == nil || invalidUUIDResp.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for malformed UUID, got: %+v", invalidUUIDResp.Error)
	}
}

func TestComposerQuota_CodexProvider(t *testing.T) {
	t.Cleanup(func() { SetCodexQuotaReader(nil) })

	resetTime := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	readAtTime := time.Date(2026, 10, 18, 11, 58, 0, 0, time.UTC)

	// 1. 桩读取器返回成功的 Codex 配额
	SetCodexQuotaReader(func(ctx context.Context, threadID string) (codex.RateLimits, error) {
		return codex.RateLimits{
			Source:   codex.QuotaSourceOpenAI,
			PlanType: "Pro 20x",
			ReadAt:   readAtTime,
			Windows: []codex.RateLimitWindow{
				{
					ID:                 "codex-primary",
					Title:              "5-hour limit",
					Period:             "5h",
					UsedPercent:        25.4,
					WindowDurationMins: 300,
					ResetsAt:           &resetTime,
				},
				{
					ID:                 "codex-secondary",
					Title:              "Weekly",
					Period:             "7d",
					UsedPercent:        60.0,
					WindowDurationMins: 10080,
					ResetsAt:           nil,
				},
			},
		}, nil
	})

	b, d, _, projectID, threadID := setupWorkspaceTestEnv(t)
	b.mu.Lock()
	b.threads[threadID].summary.Provider = "codex"
	b.mu.Unlock()

	ctx := context.Background()
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	// 通过 threadID 请求配额
	req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"composer.quota","params":{"threadID":"%s"},"id":100}`, threadID)
	raw := d.Handle(ctx, conn, []byte(req))

	var reply struct {
		Result wire.RemoteComposerQuotaResult `json:"result"`
		Error  *wire.RPCError                 `json:"error"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil || reply.Error != nil {
		t.Fatalf("quota request failed: %v, %+v", err, reply.Error)
	}

	if len(reply.Result.Budgets) != 1 {
		t.Fatalf("expected 1 budget, got %d: %s", len(reply.Result.Budgets), raw)
	}
	budget := reply.Result.Budgets[0]
	if budget.Provider != "codex" || budget.ProviderName != "Codex" || budget.Role != "lead" || budget.State != "used" {
		t.Errorf("unexpected budget metadata: %+v", budget)
	}
	if budget.Profile == nil || *budget.Profile != "Pro 20x" {
		t.Errorf("expected Profile 'Pro 20x', got %v", budget.Profile)
	}
	if budget.ReadAt == nil || *budget.ReadAt != "2026-10-18T11:58:00Z" {
		t.Errorf("expected ReadAt from the reading, got %v", budget.ReadAt)
	}
	if len(budget.Windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(budget.Windows))
	}

	w0 := budget.Windows[0]
	if w0.Period == nil || *w0.Period != "5h" || w0.Title != "5-hour limit" || w0.UsedPercent != 25.4 {
		t.Errorf("w0 mismatch: %+v", w0)
	}
	if w0.Plan != "Pro 20x" || w0.Source != codex.QuotaSourceOpenAI || w0.ReadAt == nil || *w0.ReadAt != "2026-10-18T11:58:00Z" {
		t.Errorf("w0 plan/source/readAt mismatch: %+v", w0)
	}
	if w0.ResetsAt == nil || *w0.ResetsAt != "2026-10-18T12:00:00Z" {
		t.Errorf("w0 ResetsAt mismatch: %v", w0.ResetsAt)
	}

	w1 := budget.Windows[1]
	if w1.Period == nil || *w1.Period != "7d" || w1.Title != "Weekly" || w1.UsedPercent != 60 {
		t.Errorf("w1 mismatch: %+v", w1)
	}
	if w1.ResetsAt != nil {
		t.Errorf("w1 expected nil ResetsAt, got %v", w1.ResetsAt)
	}

	// 通过 projectID 查询且 defaultProvider 为 codex
	b.mu.Lock()
	b.defaultProvider = "codex"
	b.mu.Unlock()
	projReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"composer.quota","params":{"projectID":"%s"},"id":101}`, projectID)
	projRaw := d.Handle(ctx, conn, []byte(projReq))
	var projReply struct {
		Result wire.RemoteComposerQuotaResult `json:"result"`
		Error  *wire.RPCError                 `json:"error"`
	}
	if err := json.Unmarshal(projRaw, &projReply); err != nil || projReply.Error != nil {
		t.Fatalf("project quota request failed: %v, %+v", err, projReply.Error)
	}
	if len(projReply.Result.Budgets) != 1 || projReply.Result.Budgets[0].Provider != "codex" {
		t.Fatalf("unexpected project budgets: %+v", projReply.Result.Budgets)
	}

	// 2. 桩读取器报错时（例如配置读不到 / 未安装 codex / 超时），返回一条 unknown budget，
	// 让界面显示不可用，而不是回退本机额度。
	SetCodexQuotaReader(func(ctx context.Context, threadID string) (codex.RateLimits, error) {
		return codex.RateLimits{}, errors.New("codex app-server binary not found")
	})
	errRaw := d.Handle(ctx, conn, []byte(req))
	var errReply struct {
		Result wire.RemoteComposerQuotaResult `json:"result"`
		Error  *wire.RPCError                 `json:"error"`
	}
	if err := json.Unmarshal(errRaw, &errReply); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if errReply.Error != nil {
		t.Fatalf("expected nil error on codex failure, got: %+v", errReply.Error)
	}
	if len(errReply.Result.Budgets) != 1 || errReply.Result.Budgets[0].State != "unknown" || len(errReply.Result.Budgets[0].Windows) != 0 {
		t.Errorf("expected one unknown budget on codex error, got: %+v", errReply.Result.Budgets)
	}

	// 3. 桩读取器返回空 windows 时，同样返回 unknown budget
	SetCodexQuotaReader(func(ctx context.Context, threadID string) (codex.RateLimits, error) {
		return codex.RateLimits{Source: codex.QuotaSourceOpenAI, PlanType: "Free", Windows: nil}, nil
	})
	emptyRaw := d.Handle(ctx, conn, []byte(req))
	var emptyReply struct {
		Result wire.RemoteComposerQuotaResult `json:"result"`
		Error  *wire.RPCError                 `json:"error"`
	}
	if err := json.Unmarshal(emptyRaw, &emptyReply); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if emptyReply.Error != nil {
		t.Fatalf("expected nil error on empty windows, got: %+v", emptyReply.Error)
	}
	if len(emptyReply.Result.Budgets) != 1 || emptyReply.Result.Budgets[0].State != "unknown" {
		t.Errorf("expected unknown budget on empty windows, got: %+v", emptyReply.Result.Budgets)
	}
}

// Magpie 的 Codex 卡片按账号拆成多条 budget：账号、套餐与读取时间各自保留，
// 且不能推断某个账号在用。
func TestComposerQuota_CodexMagpieAccounts(t *testing.T) {
	t.Cleanup(func() { SetCodexQuotaReader(nil) })

	readAt := time.Date(2026, 10, 28, 2, 58, 16, 0, time.UTC)
	resetTime := time.Date(2026, 10, 28, 7, 0, 0, 0, time.UTC)
	SetCodexQuotaReader(func(ctx context.Context, threadID string) (codex.RateLimits, error) {
		return codex.RateLimits{
			Source: codex.QuotaSourceMagpie,
			Accounts: []codex.AccountQuota{
				{
					Account:  "a@b.com",
					PlanType: "Plus",
					ReadAt:   readAt,
					Windows: []codex.RateLimitWindow{
						{ID: "a@b.com/5 hours", Title: "5 hours", Period: "5h", UsedPercent: 85, ResetsAt: &resetTime},
						{ID: "a@b.com/7 days", Title: "7 days", Period: "7d", UsedPercent: 40},
					},
				},
				{
					// 读取时间未知的卡片不能伪造读取时间。
					Account:  "{{EMAIL_d949a3a8}}",
					PlanType: "Free",
					Windows: []codex.RateLimitWindow{
						{ID: "{{EMAIL_d949a3a8}}/30 days", Title: "30 days", Period: "30d", UsedPercent: 0},
					},
				},
			},
		}, nil
	})

	b, d, _, _, threadID := setupWorkspaceTestEnv(t)
	b.mu.Lock()
	b.threads[threadID].summary.Provider = "codex"
	b.mu.Unlock()

	req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"composer.quota","params":{"threadID":"%s"},"id":110}`, threadID)
	raw := d.Handle(context.Background(), &fakeConn{info: ClientInfo{Authenticated: true}}, []byte(req))

	var reply struct {
		Result wire.RemoteComposerQuotaResult `json:"result"`
		Error  *wire.RPCError                 `json:"error"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil || reply.Error != nil {
		t.Fatalf("quota request failed: %v, %+v", err, reply.Error)
	}
	if len(reply.Result.Budgets) != 2 {
		t.Fatalf("expected 2 account budgets, got %d: %s", len(reply.Result.Budgets), raw)
	}
	first, second := reply.Result.Budgets[0], reply.Result.Budgets[1]
	if first.Account == nil || *first.Account != "a@b.com" {
		t.Errorf("unexpected first account: %+v", first.Account)
	}
	if first.Profile == nil || *first.Profile != "Plus" {
		t.Errorf("expected first profile Plus, got %v", first.Profile)
	}
	if first.ReadAt == nil || *first.ReadAt != "2026-10-28T02:58:16Z" {
		t.Errorf("expected first ReadAt from the card, got %v", first.ReadAt)
	}
	if len(first.Windows) != 2 || first.Windows[0].Period == nil || *first.Windows[0].Period != "5h" || first.Windows[1].Period == nil || *first.Windows[1].Period != "7d" {
		t.Errorf("unexpected first windows: %+v", first.Windows)
	}
	if second.Account == nil || *second.Account != "{{EMAIL_d949a3a8}}" || second.Profile == nil || *second.Profile != "Free" {
		t.Errorf("unexpected second budget: %+v", second)
	}
	if second.ReadAt != nil {
		t.Errorf("unknown card read time must stay unset, got %v", second.ReadAt)
	}
	for _, budget := range reply.Result.Budgets {
		if budget.Active != nil && *budget.Active {
			t.Errorf("magpie cards must not be marked active: %+v", budget.Account)
		}
	}
}

// 注入的 Codex 读取器（app 层装配）拿到会话 ID，远程端与桌面端才会用同一份来源判定。
func TestComposerQuota_CodexUsesInjectedReader(t *testing.T) {
	var gotThreadID string
	SetCodexQuotaReader(func(ctx context.Context, threadID string) (codex.RateLimits, error) {
		gotThreadID = threadID
		return codex.RateLimits{
			Source:   codex.QuotaSourceOpenAI,
			PlanType: "Plus",
			Windows:  []codex.RateLimitWindow{{ID: "codex-primary", Title: "5 hours", Period: "5h", UsedPercent: 12}},
		}, nil
	})
	t.Cleanup(func() { SetCodexQuotaReader(nil) })

	b, d, _, _, threadID := setupWorkspaceTestEnv(t)
	b.mu.Lock()
	b.threads[threadID].summary.Provider = "codex"
	b.mu.Unlock()

	req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"composer.quota","params":{"threadID":"%s"},"id":120}`, threadID)
	raw := d.Handle(context.Background(), &fakeConn{info: ClientInfo{Authenticated: true}}, []byte(req))
	var reply struct {
		Result wire.RemoteComposerQuotaResult `json:"result"`
		Error  *wire.RPCError                 `json:"error"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil || reply.Error != nil {
		t.Fatalf("quota request failed: %v, %+v", err, reply.Error)
	}
	if gotThreadID != threadID {
		t.Errorf("injected reader got thread %q, want %q", gotThreadID, threadID)
	}
	if len(reply.Result.Budgets) != 1 || reply.Result.Budgets[0].Profile == nil || *reply.Result.Budgets[0].Profile != "Plus" {
		t.Fatalf("unexpected budgets: %+v", reply.Result.Budgets)
	}
}

func TestComposerQuota_ClaudeProvider(t *testing.T) {
	orig := readClaudePlanLimits
	t.Cleanup(func() {
		readClaudePlanLimits = orig
	})

	resetTime := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

	// 1. 桩函数返回成功的 Claude 配额
	readClaudePlanLimits = func(ctx context.Context) (claude.PlanLimits, error) {
		return claude.PlanLimits{
			PlanType: "Max (20x)",
			Windows: []claude.PlanLimitWindow{
				{
					ID:          "session",
					Label:       "5-hour limit",
					UsedPercent: 12.5,
					ResetsAt:    &resetTime,
				},
				{
					ID:          "weekly_all",
					Label:       "Weekly · all models",
					UsedPercent: 40,
					ResetsAt:    nil,
				},
			},
		}, nil
	}

	b, d, _, projectID, threadID := setupWorkspaceTestEnv(t)
	b.mu.Lock()
	b.threads[threadID].summary.Provider = "claude"
	b.mu.Unlock()

	ctx := context.Background()
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	// 通过 threadID 请求配额
	req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"composer.quota","params":{"threadID":"%s"},"id":200}`, threadID)
	raw := d.Handle(ctx, conn, []byte(req))

	var reply struct {
		Result wire.RemoteComposerQuotaResult `json:"result"`
		Error  *wire.RPCError                 `json:"error"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil || reply.Error != nil {
		t.Fatalf("quota request failed: %v, %+v", err, reply.Error)
	}

	if len(reply.Result.Budgets) != 1 {
		t.Fatalf("expected 1 budget, got %d: %s", len(reply.Result.Budgets), raw)
	}
	budget := reply.Result.Budgets[0]
	if budget.Provider != "claude" || budget.ProviderName != "Claude" || budget.Role != "lead" || budget.State != "used" {
		t.Errorf("unexpected budget metadata: %+v", budget)
	}
	if budget.Profile == nil || *budget.Profile != "Max (20x)" {
		t.Errorf("expected Profile 'Max (20x)', got %v", budget.Profile)
	}
	if budget.ReadAt == nil || *budget.ReadAt == "" {
		t.Errorf("expected non-empty ReadAt")
	}
	if len(budget.Windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(budget.Windows))
	}

	w0 := budget.Windows[0]
	if w0.Period == nil || *w0.Period != "5h" || w0.Title != "5-hour limit" || w0.UsedPercent != 12.5 {
		t.Errorf("w0 mismatch: %+v", w0)
	}
	if w0.ID == nil || *w0.ID != "session" {
		t.Errorf("w0 expected ID 'session', got %v", w0.ID)
	}
	if w0.ResetsAt == nil || *w0.ResetsAt != "2026-10-18T12:00:00Z" {
		t.Errorf("w0 ResetsAt mismatch: %v", w0.ResetsAt)
	}

	w1 := budget.Windows[1]
	if w1.Period == nil || *w1.Period != "7d" || w1.Title != "Weekly · all models" || w1.UsedPercent != 40 {
		t.Errorf("w1 mismatch: %+v", w1)
	}
	if w1.ID == nil || *w1.ID != "weekly_all" {
		t.Errorf("w1 expected ID 'weekly_all', got %v", w1.ID)
	}
	if w1.ResetsAt != nil {
		t.Errorf("w1 expected nil ResetsAt, got %v", w1.ResetsAt)
	}
	// 单账号时不分拆：Account/Active 都留空，由前端兜底选中这条 budget。
	if budget.Account != nil || budget.Active != nil {
		t.Errorf("expected nil Account/Active for single account, got %v/%v", budget.Account, budget.Active)
	}

	// 通过 projectID 查询且 defaultProvider 为 claude
	b.mu.Lock()
	b.defaultProvider = "claude"
	b.mu.Unlock()
	projReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"composer.quota","params":{"projectID":"%s"},"id":201}`, projectID)
	projRaw := d.Handle(ctx, conn, []byte(projReq))
	var projReply struct {
		Result wire.RemoteComposerQuotaResult `json:"result"`
		Error  *wire.RPCError                 `json:"error"`
	}
	if err := json.Unmarshal(projRaw, &projReply); err != nil || projReply.Error != nil {
		t.Fatalf("project quota request failed: %v, %+v", err, projReply.Error)
	}
	if len(projReply.Result.Budgets) != 1 || projReply.Result.Budgets[0].Provider != "claude" {
		t.Fatalf("unexpected project budgets: %+v", projReply.Result.Budgets)
	}

	// 2. 桩函数报错时（例如未登录 Claude），返回空 budgets 且不报错
	readClaudePlanLimits = func(ctx context.Context) (claude.PlanLimits, error) {
		return claude.PlanLimits{}, errors.New("claude: 未找到登录凭据")
	}
	errRaw := d.Handle(ctx, conn, []byte(req))
	var errReply struct {
		Result wire.RemoteComposerQuotaResult `json:"result"`
		Error  *wire.RPCError                 `json:"error"`
	}
	if err := json.Unmarshal(errRaw, &errReply); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if errReply.Error != nil {
		t.Fatalf("expected nil error on claude failure, got: %+v", errReply.Error)
	}
	if len(errReply.Result.Budgets) != 0 {
		t.Errorf("expected empty budgets on claude error, got: %+v", errReply.Result.Budgets)
	}

	// 3. 桩函数返回空 windows 时，返回空 budgets
	readClaudePlanLimits = func(ctx context.Context) (claude.PlanLimits, error) {
		return claude.PlanLimits{PlanType: "Pro", Windows: nil}, nil
	}
	emptyRaw := d.Handle(ctx, conn, []byte(req))
	var emptyReply struct {
		Result wire.RemoteComposerQuotaResult `json:"result"`
		Error  *wire.RPCError                 `json:"error"`
	}
	if err := json.Unmarshal(emptyRaw, &emptyReply); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if emptyReply.Error != nil {
		t.Fatalf("expected nil error on empty windows, got: %+v", emptyReply.Error)
	}
	if len(emptyReply.Result.Budgets) != 0 {
		t.Errorf("expected empty budgets on empty windows, got: %+v", emptyReply.Result.Budgets)
	}
}

func TestQuotaPeriodFromWindow(t *testing.T) {
	cases := []struct {
		name  string
		id    string
		label string
		want  string // 空串表示期望 nil
	}{
		{name: "id session", id: "session", label: "5-hour limit", want: "5h"},
		{name: "id five_hour", id: "five_hour", label: "任意标题", want: "5h"},
		{name: "id weekly_all", id: "weekly_all", label: "Weekly · all models", want: "7d"},
		{name: "id seven_day", id: "seven_day", label: "任意标题", want: "7d"},
		{name: "id weekly 前缀", id: "weekly_opus", label: "任意标题", want: "7d"},
		{name: "id seven_day 前缀", id: "seven_day_opus", label: "任意标题", want: "7d"},
		{name: "label 5-hour", id: "", label: "5-hour limit", want: "5h"},
		{name: "label 5h", id: "", label: "Daily 5h", want: "5h"},
		{name: "label 5 小时", id: "", label: "5 小时限额", want: "5h"},
		{name: "label weekly", id: "", label: "Weekly", want: "7d"},
		{name: "label week", id: "", label: "Per week", want: "7d"},
		{name: "label 7-day", id: "", label: "7-day limit", want: "7d"},
		{name: "label 周", id: "", label: "每周", want: "7d"},
		{name: "label daily", id: "", label: "Daily", want: "1d"},
		{name: "label 1d", id: "", label: "1d limit", want: "1d"},
		{name: "label 日", id: "", label: "每日额度", want: "1d"},
		{name: "label monthly", id: "", label: "Monthly", want: "30d"},
		{name: "label 30d", id: "", label: "30d limit", want: "30d"},
		{name: "label 月", id: "", label: "每月额度", want: "30d"},
		{name: "认不出", id: "extra-usage", label: "Extra usage", want: ""},
		{name: "空", id: "", label: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := quotaPeriodFromWindow(tc.id, tc.label)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("expected nil period, got %q", *got)
				}
				return
			}
			if got == nil || *got != tc.want {
				t.Fatalf("expected period %q, got %v", tc.want, got)
			}
		})
	}
}

func TestSplitQuotaAccount(t *testing.T) {
	cases := []struct {
		name        string
		account     string
		label       string
		wantAccount string
		wantTitle   string
	}{
		{name: "账号字段优先", account: "alice@example.com", label: "alice@example.com · 5-hour limit", wantAccount: "alice@example.com", wantTitle: "5-hour limit"},
		{name: "账号字段与标题不一致", account: "alice@example.com", label: "Weekly", wantAccount: "alice@example.com", wantTitle: "Weekly"},
		{name: "从标题拆出邮箱", label: "bob@example.com · Weekly · all models", wantAccount: "bob@example.com", wantTitle: "Weekly · all models"},
		{name: "首段无邮箱不拆", label: "Weekly · all models", wantTitle: "Weekly · all models"},
		{name: "无分隔符", label: "5-hour limit", wantTitle: "5-hour limit"},
		{name: "空标题", label: "", wantTitle: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account, title := splitQuotaAccount(tc.account, tc.label)
			if account != tc.wantAccount || title != tc.wantTitle {
				t.Fatalf("expected (%q, %q), got (%q, %q)", tc.wantAccount, tc.wantTitle, account, title)
			}
		})
	}
}

// Claude 网关双账号：按账号拆成两条 budget，标题去掉账号前缀并补齐 period 与 id。
func TestComposerQuota_ClaudeSplitsAccounts(t *testing.T) {
	orig := readClaudePlanLimits
	t.Cleanup(func() {
		readClaudePlanLimits = orig
	})

	readClaudePlanLimits = func(ctx context.Context) (claude.PlanLimits, error) {
		return claude.PlanLimits{
			PlanType: "Max (20x)",
			Windows: []claude.PlanLimitWindow{
				{ID: "five_hour", Account: "alice@example.com", Label: "alice@example.com · 5-hour limit", UsedPercent: 10},
				{ID: "five_hour", Account: "bob@example.com", Label: "bob@example.com · 5-hour limit", UsedPercent: 20},
				{ID: "weekly_all", Account: "alice@example.com", Label: "alice@example.com · Weekly · all models", UsedPercent: 30},
			},
		}, nil
	}

	b, d, _, _, threadID := setupWorkspaceTestEnv(t)
	b.mu.Lock()
	b.threads[threadID].summary.Provider = "claude"
	b.mu.Unlock()

	result := quotaRequest(t, d, threadID, 210)
	if len(result.Budgets) != 2 {
		t.Fatalf("expected 2 budgets, got %d: %+v", len(result.Budgets), result.Budgets)
	}

	first := result.Budgets[0]
	if first.Account == nil || *first.Account != "alice@example.com" {
		t.Errorf("first budget expected Account 'alice@example.com', got %v", first.Account)
	}
	if len(first.Windows) != 2 || first.Windows[0].Title != "5-hour limit" || first.Windows[1].Title != "Weekly · all models" {
		t.Errorf("first budget windows mismatch: %+v", first.Windows)
	}
	if first.Windows[0].Period == nil || *first.Windows[0].Period != "5h" {
		t.Errorf("first budget w0 expected period 5h, got %v", first.Windows[0].Period)
	}
	if first.Windows[1].Period == nil || *first.Windows[1].Period != "7d" {
		t.Errorf("first budget w1 expected period 7d, got %v", first.Windows[1].Period)
	}
	if first.Active == nil || !*first.Active {
		t.Errorf("expected first budget Active=true (首次读取取列表第一个), got %v", first.Active)
	}

	second := result.Budgets[1]
	if second.Account == nil || *second.Account != "bob@example.com" {
		t.Errorf("second budget expected Account 'bob@example.com', got %v", second.Account)
	}
	if second.Active != nil {
		t.Errorf("expected nil Active on non-active account, got %v", *second.Active)
	}
	if len(second.Windows) != 1 || second.Windows[0].Title != "5-hour limit" || second.Windows[0].UsedPercent != 20 {
		t.Errorf("second budget windows mismatch: %+v", second.Windows)
	}
	if second.Profile == nil || *second.Profile != "Max (20x)" {
		t.Errorf("second budget expected Profile 'Max (20x)', got %v", second.Profile)
	}
}

// activeQuotaAccounts 收集结果里带 Account 且已标记 Active 的账号。
func activeQuotaAccounts(result wire.RemoteComposerQuotaResult) []string {
	names := make([]string, 0, 1)
	for _, budget := range result.Budgets {
		if budget.Account != nil && budget.Active != nil && *budget.Active {
			names = append(names, *budget.Account)
		}
	}
	return names
}

// TestComposerQuota_InfersActiveAccount 多账号时后端推断在用账号：首次读取取列表第一个；
// 之后某账号的已用百分比变了就转到它；都不变时保持上一个；只有一个账号时不给标记。
func TestComposerQuota_InfersActiveAccount(t *testing.T) {
	orig := readClaudePlanLimits
	t.Cleanup(func() {
		readClaudePlanLimits = orig
	})

	windows := []claude.PlanLimitWindow{
		{ID: "five_hour", Account: "alice@example.com", Label: "alice@example.com · 5-hour limit", UsedPercent: 10},
		{ID: "five_hour", Account: "bob@example.com", Label: "bob@example.com · 5-hour limit", UsedPercent: 20},
	}
	readClaudePlanLimits = func(ctx context.Context) (claude.PlanLimits, error) {
		return claude.PlanLimits{PlanType: "Max (20x)", Windows: windows}, nil
	}

	b, d, _, _, threadID := setupWorkspaceTestEnv(t)
	b.mu.Lock()
	b.threads[threadID].summary.Provider = "claude"
	b.mu.Unlock()

	clock := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	b.quotaTracker().now = func() time.Time { return clock }

	// 1. 首次读取：两个账号都是第一次看到，变动时间相同 → 取列表第一个。
	result := quotaRequest(t, d, threadID, 220)
	if got := activeQuotaAccounts(result); len(got) != 1 || got[0] != "alice@example.com" {
		t.Fatalf("首次读取 expected active [alice@example.com], got %v: %+v", got, result.Budgets)
	}

	// 2. bob 的已用百分比变了，再读一次就选 bob。
	clock = clock.Add(time.Minute)
	windows[1].UsedPercent = 55
	result = quotaRequest(t, d, threadID, 221)
	if got := activeQuotaAccounts(result); len(got) != 1 || got[0] != "bob@example.com" {
		t.Fatalf("bob 变化后 expected active [bob@example.com], got %v: %+v", got, result.Budgets)
	}

	// 3. 再读一次但没人变化，仍保持 bob。
	clock = clock.Add(time.Minute)
	result = quotaRequest(t, d, threadID, 222)
	if got := activeQuotaAccounts(result); len(got) != 1 || got[0] != "bob@example.com" {
		t.Fatalf("无变化时 expected active [bob@example.com], got %v: %+v", got, result.Budgets)
	}

	// 4. 只剩一个账号时不设置 Active。
	windows = windows[:1]
	result = quotaRequest(t, d, threadID, 223)
	if len(result.Budgets) != 1 || result.Budgets[0].Account == nil || *result.Budgets[0].Account != "alice@example.com" {
		t.Fatalf("single account budgets mismatch: %+v", result.Budgets)
	}
	if result.Budgets[0].Active != nil {
		t.Errorf("单账号 expected nil Active, got %v", *result.Budgets[0].Active)
	}
}

// TestMarkActiveQuotaAccounts_KeepsBackendActive 后端已给的 Active 标记不被推断结果覆盖。
func TestMarkActiveQuotaAccounts_KeepsBackendActive(t *testing.T) {
	b, _, _, _, _ := setupWorkspaceTestEnv(t)
	clock := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	b.quotaTracker().now = func() time.Time { return clock }

	active := true
	budgets := []wire.RemoteQuotaBudget{
		{Provider: "claude", Account: stringPtr("alice@example.com"), Active: &active,
			Windows: []wire.RemoteQuotaWindow{{Title: "5-hour limit", UsedPercent: 10}}},
		{Provider: "claude", Account: stringPtr("bob@example.com"),
			Windows: []wire.RemoteQuotaWindow{{Title: "5-hour limit", UsedPercent: 20}}},
	}
	b.markActiveQuotaAccounts(budgets)
	if budgets[0].Active == nil || !*budgets[0].Active {
		t.Errorf("expected alice keeps Active=true, got %v", budgets[0].Active)
	}
	if budgets[1].Active != nil {
		t.Errorf("expected bob Active nil, got %v", *budgets[1].Active)
	}

	// 后端标记取消后，按下一次读取的变动时间重新推断（bob 变了 → bob）。
	clock = clock.Add(time.Minute)
	budgets[0].Active = nil
	budgets[1].Windows[0].UsedPercent = 60
	b.markActiveQuotaAccounts(budgets)
	if budgets[0].Active != nil {
		t.Errorf("expected alice Active nil after re-inference, got %v", *budgets[0].Active)
	}
	if budgets[1].Active == nil || !*budgets[1].Active {
		t.Errorf("expected bob Active=true after change, got %v", budgets[1].Active)
	}
}

// TestComposerQuota_WindowFieldsPassthrough 检查远程窗口把桌面端的额度字段完整透传。
func TestComposerQuota_WindowFieldsPassthrough(t *testing.T) {
	readAt := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	balance := 12.5
	SetPiQuotaReader(func(ctx context.Context, providerID string) (PiQuotaSource, bool) {
		return PiQuotaSource{Name: "magpie_jp", Windows: []PiQuotaWindow{{
			Account: "claude", ID: "claude/credits", Label: "credits", Period: "30d",
			UsedPercent: 12.5, Active: true, Plan: "Max", Display: "$12.30 / $40",
			Text: true, Unlimited: true, Balance: &balance, Unit: "credits",
			Disabled: true, Source: "magpie", ReadAt: readAt,
		}}}, true
	})
	t.Cleanup(func() { SetPiQuotaReader(nil) })

	b, d, _, _, threadID := setupWorkspaceTestEnv(t)
	model := "magpie-jp/auto/deepseek-v4.1-flash"
	b.mu.Lock()
	b.threads[threadID].summary.Provider = "pi"
	b.threads[threadID].summary.Model = &model
	b.mu.Unlock()

	result := quotaRequest(t, d, threadID, 400)
	if len(result.Budgets) != 1 || len(result.Budgets[0].Windows) != 1 {
		t.Fatalf("expected 1 budget with 1 window, got %+v", result.Budgets)
	}
	w := result.Budgets[0].Windows[0]
	if w.Title != "credits" || w.Label != "credits" || w.Account != "claude" {
		t.Errorf("title/label/account mismatch: %+v", w)
	}
	if w.UsedPercent != 12.5 || !w.Active || !w.Disabled || !w.Text || !w.Unlimited {
		t.Errorf("flags or percent mismatch: %+v", w)
	}
	if w.Plan != "Max" || w.Display != "$12.30 / $40" || w.Unit != "credits" || w.Source != "magpie" {
		t.Errorf("plan/display/unit/source mismatch: %+v", w)
	}
	if w.Balance == nil || *w.Balance != 12.5 {
		t.Errorf("expected balance 12.5, got %v", w.Balance)
	}
	if w.ReadAt == nil || *w.ReadAt != "2026-11-02T08:00:00Z" {
		t.Errorf("expected readAt from the source, got %v", w.ReadAt)
	}

	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"label", "account", "active", "disabled", "source", "plan", "display", "text", "unlimited", "balance", "unit", "readAt"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("expected JSON key %q in %s", key, raw)
		}
	}
}

// TestRemoteQuotaWindowLegacyJSON 检查旧版字段仍按原样输出、旧客户端只读五个字段也能解析。
func TestRemoteQuotaWindowLegacyJSON(t *testing.T) {
	window := wire.RemoteQuotaWindow{
		Period: stringPtr("5h"), Title: "5-hour limit", UsedPercent: 42,
		ResetsAt: stringPtr("2026-11-02T08:30:00Z"),
	}
	raw, err := json.Marshal(window)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"period":"5h","title":"5-hour limit","usedPercent":42,"resetsAt":"2026-11-02T08:30:00Z"}`
	if string(raw) != want {
		t.Errorf("legacy JSON mismatch:\n got %s\nwant %s", raw, want)
	}
	// 旧客户端只认这五个字段：整数百分比仍能解出来。
	var legacy struct {
		ID          *string `json:"id"`
		Period      *string `json:"period"`
		Title       string  `json:"title"`
		UsedPercent int     `json:"usedPercent"`
		ResetsAt    *string `json:"resetsAt"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatalf("legacy client unmarshal failed: %v", err)
	}
	if legacy.UsedPercent != 42 || legacy.Title != "5-hour limit" || legacy.Period == nil || *legacy.Period != "5h" {
		t.Errorf("legacy client parsed mismatch: %+v", legacy)
	}
	// nil 的 balance 不出现，0 的 balance 仍出现。
	zero := 0.0
	window.Balance = &zero
	raw, err = json.Marshal(window)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"balance":0`) {
		t.Errorf("expected zero balance in JSON, got %s", raw)
	}
}

// TestComposerQuota_IgnoresTrayHiddenList 检查托盘隐藏名单只作用于菜单栏，不影响远程输入框额度：
// 名单含 claude 时，claude 线程的 composer.quota 仍然返回 claude 额度。
func TestComposerQuota_IgnoresTrayHiddenList(t *testing.T) {
	orig := readClaudePlanLimits
	t.Cleanup(func() { readClaudePlanLimits = orig })
	readClaudePlanLimits = func(ctx context.Context) (claude.PlanLimits, error) {
		return claude.PlanLimits{PlanType: "Max (20x)", Windows: []claude.PlanLimitWindow{
			{ID: "session", Label: "5-hour limit", UsedPercent: 10},
		}}, nil
	}

	b, d, _, _, threadID := setupWorkspaceTestEnv(t)
	b.mu.Lock()
	b.threads[threadID].summary.Provider = "claude"
	b.mu.Unlock()

	b.SetGeneralSettingsFunc(func() config.Config {
		return config.Config{Startup: config.StartupConfig{TrayQuotaHidden: []string{"droid", "cline", "claude", "zai"}}}
	})
	t.Cleanup(func() { b.SetGeneralSettingsFunc(nil) })

	result := quotaRequest(t, d, threadID, 401)
	if len(result.Budgets) == 0 {
		t.Fatal("expected claude budget despite tray hidden list, got none")
	}
	found := false
	for _, budget := range result.Budgets {
		if budget.Provider == "claude" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a claude budget, got %+v", result.Budgets)
	}

	// Magpie 卡片账号（「claude · 家里的号」）同样不受托盘名单影响。
	SetPiQuotaReader(func(ctx context.Context, providerID string) (PiQuotaSource, bool) {
		return PiQuotaSource{Name: "magpie_jp", Windows: []PiQuotaWindow{
			{Account: "claude · 家里的号", Label: "5 hours", UsedPercent: 10},
			{Account: "codex", Label: "Weekly", UsedPercent: 20},
		}}, true
	})
	t.Cleanup(func() { SetPiQuotaReader(nil) })
	model := "magpie-jp/auto/deepseek-v4.1-flash"
	b.mu.Lock()
	b.threads[threadID].summary.Provider = "pi"
	b.threads[threadID].summary.Model = &model
	b.mu.Unlock()

	result = quotaRequest(t, d, threadID, 402)
	if len(result.Budgets) != 2 {
		t.Fatalf("expected both magpie cards, got %+v", result.Budgets)
	}
	found = false
	for _, budget := range result.Budgets {
		if budget.Account != nil && *budget.Account == "claude · 家里的号" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected claude account card to stay visible, got %+v", result.Budgets)
	}
}
