package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/providers/agy/quota"
)

func testGeminiWeeklyQuotaResponse(w http.ResponseWriter, resetTime string) {
	if resetTime == "" {
		_, _ = w.Write([]byte(`{}`))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"groups": []any{
			map[string]any{
				"displayName": quota.GroupGemini,
				"buckets": []any{
					map[string]any{
						"window":            "7d",
						"remainingFraction": 1.0,
						"resetTime":         resetTime,
					},
				},
			},
		},
	})
}

func TestAutoWarmupSwitch(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Now()

	mgr, err := NewManager(Options{
		DataDir:  dataDir,
		HostHome: t.TempDir(),
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	// 1. 默认状态：缺失字段时默认开启
	if !mgr.AutoWarmup() {
		t.Fatal("expected AutoWarmup to default to true")
	}

	// 2. 显式关闭
	if err := mgr.SetAutoWarmup(false); err != nil {
		t.Fatalf("SetAutoWarmup(false) failed: %v", err)
	}
	if mgr.AutoWarmup() {
		t.Fatal("expected AutoWarmup to be false")
	}

	// 3. 重新加载 Manager，验证持久化
	mgr2, err := NewManager(Options{
		DataDir:  dataDir,
		HostHome: t.TempDir(),
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("reload Manager failed: %v", err)
	}
	if mgr2.AutoWarmup() {
		t.Fatal("expected reloaded AutoWarmup to be false")
	}

	// 4. 再次开启
	if err := mgr2.SetAutoWarmup(true); err != nil {
		t.Fatalf("SetAutoWarmup(true) failed: %v", err)
	}
	if !mgr2.AutoWarmup() {
		t.Fatal("expected AutoWarmup to be true")
	}
}

func TestWarmup_SkipConditions(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Now()

	mgr, err := NewManager(Options{
		DataDir:  dataDir,
		HostHome: t.TempDir(),
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	// 创建已关闭账号
	offAcc := Account{
		ID:     "acc-off",
		Name:   "Off",
		Status: StatusActive,
		On:     false,
	}
	// 创建被禁用账号
	disAcc := Account{
		ID:     "acc-dis",
		Name:   "Dis",
		Status: StatusDisabled,
		On:     true,
	}
	// 创建被封禁账号
	forbidAcc := Account{
		ID:           "acc-forbid",
		Name:         "Forbid",
		Status:       StatusForbidden,
		StatusReason: "terms violated",
		On:           true,
	}
	// 创建账号并在路由中置为冷却
	coolAcc := Account{
		ID:     "acc-cool",
		Name:   "Cool",
		Status: StatusActive,
		On:     true,
	}

	mgr.mu.Lock()
	mgr.index.Accounts = append(mgr.index.Accounts, offAcc, disAcc, forbidAcc, coolAcc)
	mgr.router.MarkLimited("acc-cool", now.Add(10*time.Minute), now)
	_ = mgr.saveIndexLocked()
	mgr.mu.Unlock()

	ctx := context.Background()

	// 单账号应分别返回明确的错误原因
	if err := mgr.Warmup(ctx, "acc-off"); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("expected turned off error, got: %v", err)
	}
	if err := mgr.Warmup(ctx, "acc-dis"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled error, got: %v", err)
	}
	if err := mgr.Warmup(ctx, "acc-forbid"); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("expected forbidden error, got: %v", err)
	}
	if err := mgr.Warmup(ctx, "acc-cool"); err == nil || !strings.Contains(err.Error(), "cooling") {
		t.Fatalf("expected cooling error, got: %v", err)
	}
}

func TestWarmup_ExecutionAndConfirmation(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	oldReset := now.Add(-1 * time.Hour).Format(time.RFC3339)
	newReset := now.Add(7 * 24 * time.Hour).Format(time.RFC3339)

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"proj-1"}`))
		case "/v1internal:fetchAvailableModels":
			rTime := oldReset
			if calls > 0 {
				rTime = newReset
			}
			data := map[string]any{
				"models": map[string]any{
					"gemini-2.5-flash": map[string]any{
						"displayName": "Gemini 2.5 Flash",
						"quotaInfo": map[string]any{
							"remainingFraction": 1.0,
							"resetTime":         rTime,
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(data)
		case "/v1internal:retrieveUserQuotaSummary":
			rTime := oldReset
			if calls > 0 {
				rTime = newReset
			}
			testGeminiWeeklyQuotaResponse(w, rTime)
		case "/v1internal:generateContent":
			calls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"candidates":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	mgr, err := NewManager(Options{
		DataDir:    dataDir,
		HostHome:   t.TempDir(),
		Now:        func() time.Time { return now },
		QuotaHosts: []string{srv.URL},
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	acc := Account{
		ID:     "acc-warmup-test",
		Name:   "Warmup Test",
		Status: StatusActive,
		On:     true,
		Quota: map[string]QuotaGroup{
			quota.GroupGemini + " 7d": {
				UsedPercent: 100,
				ResetAt:     oldReset,
			},
		},
	}

	mgr.mu.Lock()
	mgr.index.Accounts = append(mgr.index.Accounts, acc)
	_ = mgr.saveIndexLocked()
	mgr.mu.Unlock()

	tokenFile := OAuthTokenFile{
		Token: OAuthTokenData{
			AccessToken: "test-token",
			Expiry:      now.Add(1 * time.Hour),
		},
	}
	if err := mgr.WriteToken("acc-warmup-test", tokenFile); err != nil {
		t.Fatalf("WriteToken failed: %v", err)
	}

	// 执行手动预热
	if err := mgr.Warmup(context.Background(), "acc-warmup-test"); err != nil {
		t.Fatalf("Warmup failed: %v", err)
	}

	mgr.mu.Lock()
	updatedAcc, _ := mgr.findAccountLocked("acc-warmup-test")
	mgr.mu.Unlock()

	if updatedAcc.LastWarmup == nil {
		t.Fatal("expected LastWarmup to be set")
	}
	if updatedAcc.LastWarmup.Status != "success" {
		t.Fatalf("expected status success, got %s (msg: %s)", updatedAcc.LastWarmup.Status, updatedAcc.LastWarmup.Message)
	}
	if updatedAcc.LastWarmup.Model != "gemini-2.5-flash" {
		t.Fatalf("expected model gemini-2.5-flash, got %s", updatedAcc.LastWarmup.Model)
	}
	if updatedAcc.LastWarmup.ConfirmedResetAt != newReset {
		t.Fatalf("expected confirmed reset %s, got %s", newReset, updatedAcc.LastWarmup.ConfirmedResetAt)
	}
}

// a 用阻塞RoundTripper/服务器同步并发 RefreshQuota、Warmup 和批量，证明同账号只有一次生成
func TestWarmup_ConcurrentGateSingleExecution(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	oldReset := now.Add(-1 * time.Hour).Format(time.RFC3339)
	newReset := now.Add(7 * 24 * time.Hour).Format(time.RFC3339)

	var genCalls atomic.Int32
	blockGen := make(chan struct{})
	genStarted := make(chan struct{})
	var onceClose sync.Once
	var releaseBlockOnce sync.Once
	releaseBlock := func() {
		releaseBlockOnce.Do(func() {
			close(blockGen)
		})
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"proj-concurrent"}`))
		case "/v1internal:fetchAvailableModels":
			rTime := oldReset
			if genCalls.Load() > 0 {
				rTime = newReset
			}
			data := map[string]any{
				"models": map[string]any{
					"gemini-2.5-flash": map[string]any{
						"displayName": "Gemini 2.5 Flash",
						"quotaInfo": map[string]any{
							"remainingFraction": 1.0,
							"resetTime":         rTime,
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(data)
		case "/v1internal:retrieveUserQuotaSummary":
			rTime := oldReset
			if genCalls.Load() > 0 {
				rTime = newReset
			}
			testGeminiWeeklyQuotaResponse(w, rTime)
		case "/v1internal:generateContent":
			genCalls.Add(1)
			onceClose.Do(func() { close(genStarted) })
			select {
			case <-blockGen:
			case <-r.Context().Done():
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"candidates":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer func() {
		releaseBlock()
		srv.Close()
	}()

	mgr, err := NewManager(Options{
		DataDir:    dataDir,
		HostHome:   t.TempDir(),
		Now:        func() time.Time { return now },
		QuotaHosts: []string{srv.URL},
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	acc := Account{
		ID:     "acc-concurrent",
		Name:   "Concurrent Test",
		Status: StatusActive,
		On:     true,
		Quota: map[string]QuotaGroup{
			quota.GroupGemini + " 7d": {
				UsedPercent: 100,
				ResetAt:     oldReset,
			},
		},
	}
	mgr.mu.Lock()
	for i := range mgr.index.Accounts {
		if mgr.index.Accounts[i].ID == SystemAccountID {
			mgr.index.Accounts[i].On = false
		}
	}
	mgr.index.Accounts = append(mgr.index.Accounts, acc)
	_ = mgr.saveIndexLocked()
	mgr.mu.Unlock()

	_ = mgr.WriteToken("acc-concurrent", OAuthTokenFile{
		Token: OAuthTokenData{
			AccessToken: "token-concurrent",
			Expiry:      now.Add(1 * time.Hour),
		},
	})

	var wg sync.WaitGroup
	// 启动 RefreshQuota（触发自动预热）
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = mgr.RefreshQuota(context.Background(), "acc-concurrent")
	}()

	// 等待生成请求进入阻塞状态（有界等待，防止未进入路径时无限阻塞）
	select {
	case <-genStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for generateContent to start")
	}

	// 并发启动单账号手动预热与批量预热
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = mgr.Warmup(context.Background(), "acc-concurrent")
	}()
	go func() {
		defer wg.Done()
		_ = mgr.Warmup(context.Background(), "")
	}()

	// 放行生成请求
	releaseBlock()
	wg.Wait()

	if genCalls.Load() != 1 {
		t.Fatalf("expected exactly 1 generationContent call across concurrent paths, got %d", genCalls.Load())
	}
}

// b 200但reset不变，后续刷新/重复手动/重建Manager都不再次生成，后续真实reset推进会确认
func TestWarmup_ResetUnchangedDedupAndConfirm(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	oldReset := now.Add(-1 * time.Hour).Format(time.RFC3339)
	newReset := now.Add(7 * 24 * time.Hour).Format(time.RFC3339)

	var genCalls atomic.Int32
	var serverResetAdvanced atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"proj-reset-test"}`))
		case "/v1internal:fetchAvailableModels":
			rTime := oldReset
			if serverResetAdvanced.Load() {
				rTime = newReset
			}
			data := map[string]any{
				"models": map[string]any{
					"gemini-2.5-flash": map[string]any{
						"displayName": "Gemini 2.5 Flash",
						"quotaInfo": map[string]any{
							"remainingFraction": 1.0,
							"resetTime":         rTime,
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(data)
		case "/v1internal:retrieveUserQuotaSummary":
			rTime := oldReset
			if serverResetAdvanced.Load() {
				rTime = newReset
			}
			testGeminiWeeklyQuotaResponse(w, rTime)
		case "/v1internal:generateContent":
			genCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"candidates":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	mgr, err := NewManager(Options{
		DataDir:    dataDir,
		HostHome:   t.TempDir(),
		Now:        func() time.Time { return now },
		QuotaHosts: []string{srv.URL},
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	acc := Account{
		ID:     "acc-reset-unchanged",
		Name:   "Reset Test",
		Status: StatusActive,
		On:     true,
		Quota: map[string]QuotaGroup{
			quota.GroupGemini + " 7d": {
				UsedPercent: 100,
				ResetAt:     oldReset,
			},
		},
	}
	mgr.mu.Lock()
	mgr.index.Accounts = append(mgr.index.Accounts, acc)
	_ = mgr.saveIndexLocked()
	mgr.mu.Unlock()

	_ = mgr.WriteToken("acc-reset-unchanged", OAuthTokenFile{
		Token: OAuthTokenData{
			AccessToken: "token-reset",
			Expiry:      now.Add(1 * time.Hour),
		},
	})

	// 1. 首次手动预热，生成 200 但服务端 reset 未变
	if err := mgr.Warmup(context.Background(), "acc-reset-unchanged"); err != nil {
		t.Fatalf("first warmup failed: %v", err)
	}
	if genCalls.Load() != 1 {
		t.Fatalf("expected 1 generation call, got %d", genCalls.Load())
	}
	cur, _ := mgr.Get("acc-reset-unchanged")
	if cur.LastWarmup == nil || cur.LastWarmup.Status != "pending_confirmation" {
		t.Fatalf("expected status pending_confirmation, got %+v", cur.LastWarmup)
	}

	// 2. 后续刷新、重复手动都不再次生成
	now = now.Add(time.Minute)
	_ = mgr.RefreshQuota(context.Background(), "acc-reset-unchanged")
	_ = mgr.Warmup(context.Background(), "acc-reset-unchanged")
	if genCalls.Load() != 1 {
		t.Fatalf("pending_confirmation should not re-generate, got %d calls", genCalls.Load())
	}

	// 3. 重建 Manager 后依然防重
	mgrReloaded, err := NewManager(Options{
		DataDir:    dataDir,
		HostHome:   t.TempDir(),
		Now:        func() time.Time { return now },
		QuotaHosts: []string{srv.URL},
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = mgrReloaded.Warmup(context.Background(), "acc-reset-unchanged")
	if genCalls.Load() != 1 {
		t.Fatalf("reloaded manager should not re-generate, got %d calls", genCalls.Load())
	}

	// 4. 服务端真实推进周期后，普通刷新确认成功
	serverResetAdvanced.Store(true)
	now = now.Add(time.Minute)
	if err := mgrReloaded.RefreshQuota(context.Background(), "acc-reset-unchanged"); err != nil {
		t.Fatal(err)
	}
	if genCalls.Load() != 1 {
		t.Fatalf("confirming reset should not generate, got %d calls", genCalls.Load())
	}
	confirmed, _ := mgrReloaded.Get("acc-reset-unchanged")
	if confirmed.LastWarmup == nil || confirmed.LastWarmup.Status != "success" {
		t.Fatalf("expected status success, got %+v", confirmed.LastWarmup)
	}
}

// c timeout/连接错误/5xx返回后即使推进注入时钟超过10分钟、重启或再次手动仍不盲目生成
func TestWarmup_NetworkUnknownNoBlindRegeneration(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	oldReset := now.Add(-1 * time.Hour).Format(time.RFC3339)

	var genCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"proj-net-unknown"}`))
		case "/v1internal:fetchAvailableModels":
			data := map[string]any{
				"models": map[string]any{
					"gemini-2.5-flash": map[string]any{
						"displayName": "Gemini 2.5 Flash",
						"quotaInfo": map[string]any{
							"remainingFraction": 1.0,
							"resetTime":         oldReset,
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(data)
		case "/v1internal:retrieveUserQuotaSummary":
			testGeminiWeeklyQuotaResponse(w, oldReset)
		case "/v1internal:generateContent":
			genCalls.Add(1)
			http.Error(w, "internal server error", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	mgr, err := NewManager(Options{
		DataDir:    dataDir,
		HostHome:   t.TempDir(),
		Now:        func() time.Time { return now },
		QuotaHosts: []string{srv.URL},
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}

	acc := Account{
		ID:     "acc-net-unknown",
		Name:   "Net Unknown",
		Status: StatusActive,
		On:     true,
		Quota: map[string]QuotaGroup{
			quota.GroupGemini + " 7d": {
				UsedPercent: 100,
				ResetAt:     oldReset,
			},
		},
	}
	mgr.mu.Lock()
	mgr.index.Accounts = append(mgr.index.Accounts, acc)
	_ = mgr.saveIndexLocked()
	mgr.mu.Unlock()

	_ = mgr.WriteToken("acc-net-unknown", OAuthTokenFile{
		Token: OAuthTokenData{
			AccessToken: "token-net",
			Expiry:      now.Add(1 * time.Hour),
		},
	})

	// 首次预热返回 500（结果未决）
	_ = mgr.Warmup(context.Background(), "acc-net-unknown")
	if genCalls.Load() != 1 {
		t.Fatalf("expected 1 generation call, got %d", genCalls.Load())
	}
	cur, _ := mgr.Get("acc-net-unknown")
	if cur.LastWarmup == nil || cur.LastWarmup.Status != "network_unknown" {
		t.Fatalf("expected status network_unknown, got %+v", cur.LastWarmup)
	}

	// 推进时钟超过 10 分钟，再次手动依然防重
	now = now.Add(15 * time.Minute)
	_ = mgr.Warmup(context.Background(), "acc-net-unknown")
	if genCalls.Load() != 1 {
		t.Fatalf("network_unknown must not blindly re-generate after 15m, got %d calls", genCalls.Load())
	}

	// 重启后再次手动依然不盲目生成
	mgrReloaded, _ := NewManager(Options{
		DataDir:    dataDir,
		HostHome:   t.TempDir(),
		Now:        func() time.Time { return now },
		QuotaHosts: []string{srv.URL},
		HTTPClient: srv.Client(),
	})
	_ = mgrReloaded.Warmup(context.Background(), "acc-net-unknown")
	if genCalls.Load() != 1 {
		t.Fatalf("network_unknown must not re-generate after reload, got %d calls", genCalls.Load())
	}
}

// d 首次无旧reset的手动去重
func TestWarmup_FirstTimeNoResetDedup(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)

	var genCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"proj-no-reset"}`))
		case "/v1internal:fetchAvailableModels":
			data := map[string]any{
				"models": map[string]any{
					"gemini-2.5-flash": map[string]any{
						"displayName": "Gemini 2.5 Flash",
					},
				},
			}
			_ = json.NewEncoder(w).Encode(data)
		case "/v1internal:retrieveUserQuotaSummary":
			_, _ = w.Write([]byte(`{}`))
		case "/v1internal:generateContent":
			genCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"candidates":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	mgr, err := NewManager(Options{
		DataDir:    dataDir,
		HostHome:   t.TempDir(),
		Now:        func() time.Time { return now },
		QuotaHosts: []string{srv.URL},
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// 账号 Quota 中无任何 reset 信息
	acc := Account{
		ID:     "acc-no-reset",
		Name:   "No Reset",
		Status: StatusActive,
		On:     true,
	}
	mgr.mu.Lock()
	mgr.index.Accounts = append(mgr.index.Accounts, acc)
	_ = mgr.saveIndexLocked()
	mgr.mu.Unlock()

	_ = mgr.WriteToken("acc-no-reset", OAuthTokenFile{
		Token: OAuthTokenData{
			AccessToken: "token-no-reset",
			Expiry:      now.Add(1 * time.Hour),
		},
	})

	// 第一次手动预热
	if err := mgr.Warmup(context.Background(), "acc-no-reset"); err != nil {
		t.Fatalf("first warmup failed: %v", err)
	}
	if genCalls.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", genCalls.Load())
	}
	cur, _ := mgr.Get("acc-no-reset")
	if cur.LastWarmup == nil || cur.LastWarmup.TargetResetAt != "initial" {
		t.Fatalf("expected targetResetAt initial, got %+v", cur.LastWarmup)
	}

	// 连续点击或重启均不得重复生成
	_ = mgr.Warmup(context.Background(), "acc-no-reset")
	if genCalls.Load() != 1 {
		t.Fatalf("rapid second click re-generated, got %d calls", genCalls.Load())
	}

	mgrReloaded, _ := NewManager(Options{
		DataDir:    dataDir,
		HostHome:   t.TempDir(),
		Now:        func() time.Time { return now },
		QuotaHosts: []string{srv.URL},
		HTTPClient: srv.Client(),
	})
	_ = mgrReloaded.Warmup(context.Background(), "acc-no-reset")
	if genCalls.Load() != 1 {
		t.Fatalf("reloaded manager re-generated for initial cycle, got %d calls", genCalls.Load())
	}
}

// e 当前空候选或Fetch失败绝不用旧Candidates请求
func TestWarmup_EmptyCandidatesOrFetchFailureNoStaleFallback(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Now()

	var genCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"proj-empty"}`))
		case "/v1internal:fetchAvailableModels":
			// 返回空候选
			data := map[string]any{"models": map[string]any{}}
			_ = json.NewEncoder(w).Encode(data)
		case "/v1internal:retrieveUserQuotaSummary":
			_, _ = w.Write([]byte(`{}`))
		case "/v1internal:generateContent":
			genCalls.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	mgr, _ := NewManager(Options{
		DataDir:    dataDir,
		HostHome:   t.TempDir(),
		Now:        func() time.Time { return now },
		QuotaHosts: []string{srv.URL},
		HTTPClient: srv.Client(),
	})

	acc := Account{
		ID:     "acc-empty-cand",
		Name:   "Empty Cand",
		Status: StatusActive,
		On:     true,
	}
	mgr.mu.Lock()
	mgr.index.Accounts = append(mgr.index.Accounts, acc)
	_ = mgr.saveIndexLocked()
	mgr.mu.Unlock()

	_ = mgr.WriteToken("acc-empty-cand", OAuthTokenFile{
		Token: OAuthTokenData{
			AccessToken: "token-empty",
			Expiry:      now.Add(1 * time.Hour),
		},
	})

	err := mgr.Warmup(context.Background(), "acc-empty-cand")
	if err == nil || !strings.Contains(err.Error(), "no available Gemini models") {
		t.Fatalf("expected no available Gemini models error, got %v", err)
	}
	if genCalls.Load() != 0 {
		t.Fatalf("must not send generateContent request on empty candidates, got %d calls", genCalls.Load())
	}
}

// f router MarkLimited和路由On覆盖停用都跳过自动/手动
func TestWarmup_RouterMarkLimitedAndRoutingOverrideSkipped(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	oldReset := now.Add(-1 * time.Hour).Format(time.RFC3339)

	mgr, _ := NewManager(Options{
		DataDir:  dataDir,
		HostHome: t.TempDir(),
		Now:      func() time.Time { return now },
	})

	acc1 := Account{
		ID:     "acc-cooling",
		Name:   "Cooling",
		Status: StatusActive,
		On:     true,
		Quota: map[string]QuotaGroup{
			quota.GroupGemini + " 7d": {UsedPercent: 100, ResetAt: oldReset},
		},
	}
	acc2 := Account{
		ID:     "acc-override-off",
		Name:   "Override Off",
		Status: StatusActive,
		On:     true,
		Quota: map[string]QuotaGroup{
			quota.GroupGemini + " 7d": {UsedPercent: 100, ResetAt: oldReset},
		},
	}

	mgr.mu.Lock()
	mgr.index.Accounts = append(mgr.index.Accounts, acc1, acc2)
	mgr.router.MarkLimited("acc-cooling", now.Add(15*time.Minute), now)
	falseVal := false
	if mgr.routing.Accounts == nil {
		mgr.routing.Accounts = make(map[string]AccountRoutingOverride)
	}
	mgr.routing.Accounts["acc-override-off"] = AccountRoutingOverride{On: &falseVal}
	_ = mgr.saveIndexLocked()
	_ = mgr.saveRoutingLocked()
	mgr.mu.Unlock()

	ctx := context.Background()
	// 手动执行跳过
	if err := mgr.Warmup(ctx, "acc-cooling"); err == nil || !strings.Contains(err.Error(), "cooling") {
		t.Fatalf("expected cooling error, got %v", err)
	}
	if err := mgr.Warmup(ctx, "acc-override-off"); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("expected turned off error, got %v", err)
	}
}

// j 自动开关、仅ClaudeGPT到期不请求、系统token不回写与删除不回写
func TestWarmup_AutoSwitchAndNonGeminiExpiry(t *testing.T) {
	dataDir := t.TempDir()
	hostHome := t.TempDir()
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	oldReset := now.Add(-1 * time.Hour).Format(time.RFC3339)
	futureReset := now.Add(7 * 24 * time.Hour).Format(time.RFC3339)

	var genCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"proj-auto-test"}`))
		case "/v1internal:fetchAvailableModels":
			data := map[string]any{
				"models": map[string]any{
					"gemini-2.5-flash": map[string]any{
						"displayName": "Gemini 2.5 Flash",
						"quotaInfo": map[string]any{
							"remainingFraction": 1.0,
							"resetTime":         futureReset,
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(data)
		case "/v1internal:retrieveUserQuotaSummary":
			_ = json.NewEncoder(w).Encode(map[string]any{"groups": []any{
				map[string]any{
					"displayName": quota.GroupClaudeGPT,
					"buckets": []any{map[string]any{
						"window":            "7d",
						"remainingFraction": 1.0,
						"resetTime":         oldReset, // Claude 到期
					}},
				},
				map[string]any{
					"displayName": quota.GroupGemini,
					"buckets": []any{map[string]any{
						"window":            "7d",
						"remainingFraction": 1.0,
						"resetTime":         futureReset, // Gemini 未到期
					}},
				},
			}})
		case "/v1internal:generateContent":
			genCalls.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	mgr, _ := NewManager(Options{
		DataDir:    dataDir,
		HostHome:   hostHome,
		Now:        func() time.Time { return now },
		QuotaHosts: []string{srv.URL},
		HTTPClient: srv.Client(),
		SystemTokenReader: func(context.Context) (OAuthTokenFile, error) {
			return OAuthTokenFile{
				Token: OAuthTokenData{
					AccessToken: "sys-token",
					Expiry:      now.Add(time.Hour),
				},
			}, nil
		},
	})

	// 1. 仅 Claude 到期不触发自动预热
	acc := Account{
		ID:     "acc-claude-exp",
		Name:   "Claude Exp",
		Status: StatusActive,
		On:     true,
		Quota: map[string]QuotaGroup{
			quota.GroupClaudeGPT + " 7d": {UsedPercent: 100, ResetAt: oldReset},
			quota.GroupGemini + " 7d":    {UsedPercent: 10, ResetAt: futureReset},
		},
	}
	mgr.mu.Lock()
	mgr.index.Accounts = append(mgr.index.Accounts, acc)
	_ = mgr.saveIndexLocked()
	mgr.mu.Unlock()

	_ = mgr.WriteToken("acc-claude-exp", OAuthTokenFile{
		Token: OAuthTokenData{
			AccessToken: "token-claude",
			Expiry:      now.Add(time.Hour),
		},
	})

	_ = mgr.RefreshQuota(context.Background(), "acc-claude-exp")
	if genCalls.Load() != 0 {
		t.Fatalf("Claude-only expiry must not trigger warmup, got %d calls", genCalls.Load())
	}

	// 2. 验证系统账号不写回 hostHome
	_ = mgr.RefreshQuota(context.Background(), SystemAccountID)
	if _, err := os.Stat(filepath.Join(hostHome, ".gemini")); !os.IsNotExist(err) {
		t.Fatalf("system account refresh wrote to hostHome: %v", err)
	}

	// 3. 验证删除账号防幽灵写回
	delAcc := Account{
		ID:     "acc-del",
		Name:   "Del",
		Status: StatusActive,
		On:     true,
	}
	mgr.mu.Lock()
	mgr.index.Accounts = append(mgr.index.Accounts, delAcc)
	_ = mgr.saveIndexLocked()
	mgr.mu.Unlock()
	_ = mgr.Remove("acc-del")
	if err := mgr.RefreshQuota(context.Background(), "acc-del"); !errors.Is(err, ErrAccountGone) {
		t.Fatalf("expected ErrAccountGone for deleted account, got %v", err)
	}
}

func TestWarmup_CycleIdentity_AdvanceAndRepeat(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	currNow := now

	var genCalls atomic.Int32
	var phase atomic.Int32 // 0: oldReset; 1: newReset; 2: newerReset

	oldReset := now.Add(-1 * time.Hour).Format(time.RFC3339)
	newReset := now.Add(7 * 24 * time.Hour).Format(time.RFC3339)
	newerReset := now.Add(14 * 24 * time.Hour).Format(time.RFC3339)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"proj-cycle-test"}`))
		case "/v1internal:fetchAvailableModels":
			rTime := oldReset
			p := phase.Load()
			if p == 1 {
				rTime = newReset
			} else if p == 2 {
				rTime = newerReset
			}
			data := map[string]any{
				"models": map[string]any{
					"gemini-2.5-flash": map[string]any{
						"displayName": "Gemini 2.5 Flash",
						"quotaInfo": map[string]any{
							"remainingFraction": 1.0,
							"resetTime":         rTime,
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(data)
		case "/v1internal:retrieveUserQuotaSummary":
			rTime := oldReset
			p := phase.Load()
			if p == 1 {
				rTime = newReset
			} else if p == 2 {
				rTime = newerReset
			}
			testGeminiWeeklyQuotaResponse(w, rTime)
		case "/v1internal:generateContent":
			calls := genCalls.Add(1)
			if calls == 1 {
				phase.Store(1) // 第一次生成后推进到 newReset
			} else if calls == 2 {
				phase.Store(2) // 第二次生成后推进到 newerReset
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"candidates":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	mgr, err := NewManager(Options{
		DataDir:    dataDir,
		HostHome:   t.TempDir(),
		Now:        func() time.Time { return currNow },
		QuotaHosts: []string{srv.URL},
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}

	acc := Account{
		ID:     "acc-cycle",
		Name:   "Cycle Test",
		Status: StatusActive,
		On:     true,
		Quota: map[string]QuotaGroup{
			quota.GroupGemini + " 7d": {UsedPercent: 100, ResetAt: oldReset},
		},
	}
	mgr.mu.Lock()
	for i := range mgr.index.Accounts {
		if mgr.index.Accounts[i].ID == SystemAccountID {
			mgr.index.Accounts[i].On = false
		}
	}
	mgr.index.Accounts = append(mgr.index.Accounts, acc)
	_ = mgr.saveIndexLocked()
	mgr.mu.Unlock()

	_ = mgr.WriteToken("acc-cycle", OAuthTokenFile{
		Token: OAuthTokenData{AccessToken: "token-cycle", Expiry: now.Add(30 * 24 * time.Hour)},
	})

	// 1. 第一次预热确认成功
	if err := mgr.Warmup(context.Background(), "acc-cycle"); err != nil {
		t.Fatalf("first warmup failed: %v", err)
	}
	if genCalls.Load() != 1 {
		t.Fatalf("expected 1 generation call, got %d", genCalls.Load())
	}
	acc1, _ := mgr.Get("acc-cycle")
	if acc1.LastWarmup == nil || acc1.LastWarmup.ConfirmedResetAt != newReset || acc1.LastWarmup.Status != "success" {
		t.Fatalf("expected success with ConfirmedResetAt=%s, got %+v", newReset, acc1.LastWarmup)
	}

	// 2. 立即重复单账号手动
	if err := mgr.Warmup(context.Background(), "acc-cycle"); err != nil {
		t.Fatalf("repeat warmup failed: %v", err)
	}
	if genCalls.Load() != 1 {
		t.Fatalf("repeat warmup should not generate, got %d", genCalls.Load())
	}

	// 3. 立即批量预热
	if err := mgr.Warmup(context.Background(), ""); err != nil {
		t.Fatalf("batch warmup failed: %v", err)
	}
	if genCalls.Load() != 1 {
		t.Fatalf("batch warmup should not generate, got %d", genCalls.Load())
	}

	// 4. 重建 Manager
	mgrReloaded, err := NewManager(Options{
		DataDir:    dataDir,
		HostHome:   t.TempDir(),
		Now:        func() time.Time { return currNow },
		QuotaHosts: []string{srv.URL},
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgrReloaded.Warmup(context.Background(), "acc-cycle"); err != nil {
		t.Fatalf("reloaded warmup failed: %v", err)
	}
	if genCalls.Load() != 1 {
		t.Fatalf("reloaded warmup should not generate, got %d", genCalls.Load())
	}

	// 5. 推进到下一 reset（newReset）之后，且服务端尚未推进（phase 仍为 1，即返回 newReset）
	currNow = now.Add(7*24*time.Hour + time.Minute)
	if err := mgrReloaded.Warmup(context.Background(), "acc-cycle"); err != nil {
		t.Fatalf("next cycle warmup failed: %v", err)
	}
	if genCalls.Load() != 2 {
		t.Fatalf("expected 2nd generation call for new cycle, got %d", genCalls.Load())
	}
	acc2, _ := mgrReloaded.Get("acc-cycle")
	if acc2.LastWarmup == nil || acc2.LastWarmup.ConfirmedResetAt != newerReset || acc2.LastWarmup.Status != "success" {
		t.Fatalf("expected success with ConfirmedResetAt=%s, got %+v", newerReset, acc2.LastWarmup)
	}

	// 6. 随后重复手动与批量仍恰好 2 次
	if err := mgrReloaded.Warmup(context.Background(), "acc-cycle"); err != nil {
		t.Fatalf("subsequent warmup failed: %v", err)
	}
	if err := mgrReloaded.Warmup(context.Background(), ""); err != nil {
		t.Fatalf("subsequent batch warmup failed: %v", err)
	}
	if genCalls.Load() != 2 {
		t.Fatalf("subsequent repeat must stay at exactly 2, got %d", genCalls.Load())
	}
}

func TestWarmup_CycleDedup_BeforeExpiryAndInitialNoReset(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	currNow := now

	var genCalls atomic.Int32
	var mode atomic.Int32 // 0: future reset; 1: expired reset unchanged; 2: no reset initial -> reset1

	futureReset := now.Add(3 * 24 * time.Hour).Format(time.RFC3339)
	initialReset1 := now.Add(7 * 24 * time.Hour).Format(time.RFC3339)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"proj-before-exp"}`))
		case "/v1internal:fetchAvailableModels":
			m := mode.Load()
			rTime := ""
			if m == 0 || m == 1 {
				rTime = futureReset
			} else if m == 2 {
				// initial: 第一次调 fetch 时返回空，生成后返回 initialReset1
				if genCalls.Load() > 0 {
					rTime = initialReset1
				}
			}
			modelMap := map[string]any{
				"displayName": "Gemini 2.5 Flash",
			}
			if rTime != "" {
				modelMap["quotaInfo"] = map[string]any{
					"remainingFraction": 1.0,
					"resetTime":         rTime,
				}
			}
			data := map[string]any{
				"models": map[string]any{
					"gemini-2.5-flash": modelMap,
				},
			}
			_ = json.NewEncoder(w).Encode(data)
		case "/v1internal:retrieveUserQuotaSummary":
			m := mode.Load()
			rTime := ""
			if m == 0 || m == 1 {
				rTime = futureReset
			} else if m == 2 {
				// initial: 第一次调 fetch 时返回空，生成后返回 initialReset1
				if genCalls.Load() > 0 {
					rTime = initialReset1
				}
			}
			testGeminiWeeklyQuotaResponse(w, rTime)
		case "/v1internal:generateContent":
			genCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"candidates":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	mgr, err := NewManager(Options{
		DataDir:    dataDir,
		HostHome:   t.TempDir(),
		Now:        func() time.Time { return currNow },
		QuotaHosts: []string{srv.URL},
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}

	acc := Account{
		ID:     "acc-before",
		Name:   "Before Expiry",
		Status: StatusActive,
		On:     true,
	}
	mgr.mu.Lock()
	mgr.index.Accounts = append(mgr.index.Accounts, acc)
	_ = mgr.saveIndexLocked()
	mgr.mu.Unlock()

	_ = mgr.WriteToken("acc-before", OAuthTokenFile{
		Token: OAuthTokenData{AccessToken: "token-before", Expiry: now.Add(30 * 24 * time.Hour)},
	})

	// 1. 到期前手动发起：reset 在未来，不执行生成，不记录 pending_confirmation 阻塞
	if err := mgr.Warmup(context.Background(), "acc-before"); err != nil {
		t.Fatalf("warmup before expiry should succeed: %v", err)
	}
	if genCalls.Load() != 0 {
		t.Fatalf("expected 0 generation calls before expiry, got %d", genCalls.Load())
	}

	// 2. 推进时钟至到期后（4 天后，此时 futureReset 已经到期）
	currNow = now.Add(4 * 24 * time.Hour)
	mode.Store(1) // 服务端尚未推进，仍返回此到期 reset
	if err := mgr.Warmup(context.Background(), "acc-before"); err != nil {
		t.Fatalf("warmup after expiry failed: %v", err)
	}
	if genCalls.Load() != 1 {
		t.Fatalf("expected 1 generation call after expiry, got %d", genCalls.Load())
	}
	// 重复调用：同一到期周期去重，不重复生成
	if err := mgr.Warmup(context.Background(), "acc-before"); err != nil {
		t.Fatal(err)
	}
	if genCalls.Load() != 1 {
		t.Fatalf("repeat in same expired cycle should not generate, got %d", genCalls.Load())
	}

	// 3. 无 reset 首次请求（initial）
	accInit := Account{
		ID:     "acc-initial",
		Name:   "Initial No Reset",
		Status: StatusActive,
		On:     true,
	}
	mgr.mu.Lock()
	mgr.index.Accounts = append(mgr.index.Accounts, accInit)
	_ = mgr.saveIndexLocked()
	mgr.mu.Unlock()
	_ = mgr.WriteToken("acc-initial", OAuthTokenFile{
		Token: OAuthTokenData{AccessToken: "token-initial", Expiry: now.Add(30 * 24 * time.Hour)},
	})

	mode.Store(2)
	genCalls.Store(0)
	if err := mgr.Warmup(context.Background(), "acc-initial"); err != nil {
		t.Fatalf("initial warmup failed: %v", err)
	}
	if genCalls.Load() != 1 {
		t.Fatalf("expected 1 call for initial no reset, got %d", genCalls.Load())
	}
	curInit, _ := mgr.Get("acc-initial")
	if curInit.LastWarmup == nil || curInit.LastWarmup.ConfirmedResetAt != initialReset1 {
		t.Fatalf("expected ConfirmedResetAt=%s, got %+v", initialReset1, curInit.LastWarmup)
	}
	// 随后再次请求：服务端返回 reset1（未来时间），不得转换键后又生成
	if err := mgr.Warmup(context.Background(), "acc-initial"); err != nil {
		t.Fatal(err)
	}
	if genCalls.Load() != 1 {
		t.Fatalf("must not regenerate after key transition, got %d", genCalls.Load())
	}
}

func TestAutoWarmup_FirstTimeWithoutSnapshotAndGeminiOnly(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	oldReset := now.Add(-1 * time.Hour).Format(time.RFC3339)

	var genCalls atomic.Int32
	var scenario atomic.Int32 // 0: Gemini 7d expired; 1: Claude only expired; 2: no Gemini 7d reset

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"proj-auto-first"}`))
		case "/v1internal:fetchAvailableModels":
			data := map[string]any{
				"models": map[string]any{
					"gemini-2.5-flash": map[string]any{
						"displayName": "Gemini 2.5 Flash",
					},
				},
			}
			_ = json.NewEncoder(w).Encode(data)
		case "/v1internal:retrieveUserQuotaSummary":
			s := scenario.Load()
			var groups []any
			if s == 0 {
				// 本次服务端明确返回已过期的 Gemini 7d 周重置
				groups = append(groups, map[string]any{
					"displayName": quota.GroupGemini,
					"buckets": []any{map[string]any{
						"window":            "7d",
						"remainingFraction": 1.0,
						"resetTime":         oldReset,
					}},
				})
			} else if s == 1 {
				// 仅有 Claude 7d 到期
				groups = append(groups, map[string]any{
					"displayName": quota.GroupClaudeGPT,
					"buckets": []any{map[string]any{
						"window":            "7d",
						"remainingFraction": 1.0,
						"resetTime":         oldReset,
					}},
				})
			} else if s == 2 {
				// 无 Gemini 7d 周 reset
				groups = append(groups, map[string]any{
					"displayName": quota.GroupGemini,
					"buckets": []any{map[string]any{
						"window":            "1d",
						"remainingFraction": 1.0,
						"resetTime":         oldReset,
					}},
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"groups": groups})
		case "/v1internal:generateContent":
			genCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"candidates":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	mgr, err := NewManager(Options{
		DataDir:    dataDir,
		HostHome:   t.TempDir(),
		Now:        func() time.Time { return now },
		QuotaHosts: []string{srv.URL},
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. 本地 Quota 为空（无旧快照），本次返回已过期 Gemini 周桶 -> 自动预热触发
	acc1 := Account{
		ID:     "acc-first-gemini",
		Name:   "First Gemini",
		Status: StatusActive,
		On:     true,
		Quota:  nil, // 空快照
	}
	mgr.mu.Lock()
	mgr.index.Accounts = append(mgr.index.Accounts, acc1)
	_ = mgr.saveIndexLocked()
	mgr.mu.Unlock()
	_ = mgr.WriteToken("acc-first-gemini", OAuthTokenFile{
		Token: OAuthTokenData{AccessToken: "token-g", Expiry: now.Add(time.Hour)},
	})

	scenario.Store(0)
	if err := mgr.RefreshQuota(context.Background(), "acc-first-gemini"); err != nil {
		t.Fatal(err)
	}
	if genCalls.Load() != 1 {
		t.Fatalf("expected 1 generation call for empty quota with expired Gemini 7d, got %d", genCalls.Load())
	}

	// 2. 本次仅有 Claude 到期 -> 不触发
	acc2 := Account{
		ID:     "acc-only-claude",
		Name:   "Only Claude",
		Status: StatusActive,
		On:     true,
	}
	mgr.mu.Lock()
	mgr.index.Accounts = append(mgr.index.Accounts, acc2)
	_ = mgr.saveIndexLocked()
	mgr.mu.Unlock()
	_ = mgr.WriteToken("acc-only-claude", OAuthTokenFile{
		Token: OAuthTokenData{AccessToken: "token-c", Expiry: now.Add(time.Hour)},
	})

	scenario.Store(1)
	if err := mgr.RefreshQuota(context.Background(), "acc-only-claude"); err != nil {
		t.Fatal(err)
	}
	if genCalls.Load() != 1 {
		t.Fatalf("Claude-only expiry must not generate, got %d calls", genCalls.Load())
	}

	// 3. 没有 Gemini 周 reset -> 不猜测周期，不触发
	acc3 := Account{
		ID:     "acc-no-7d",
		Name:   "No 7d",
		Status: StatusActive,
		On:     true,
	}
	mgr.mu.Lock()
	mgr.index.Accounts = append(mgr.index.Accounts, acc3)
	_ = mgr.saveIndexLocked()
	mgr.mu.Unlock()
	_ = mgr.WriteToken("acc-no-7d", OAuthTokenFile{
		Token: OAuthTokenData{AccessToken: "token-no7d", Expiry: now.Add(time.Hour)},
	})

	scenario.Store(2)
	if err := mgr.RefreshQuota(context.Background(), "acc-no-7d"); err != nil {
		t.Fatal(err)
	}
	if genCalls.Load() != 1 {
		t.Fatalf("missing Gemini 7d reset must not generate, got %d calls", genCalls.Load())
	}
}

type testBlockingRoundTripper struct {
	started chan struct{}
}

func (b *testBlockingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-req.Context().Done()
	return nil, req.Context().Err()
}

func TestWarmup_CanceledContextReleasesGate(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Now()

	mgr, err := NewManager(Options{
		DataDir:  dataDir,
		HostHome: t.TempDir(),
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	acc := Account{
		ID:     "acc-gate-cancel",
		Name:   "Gate Cancel",
		Status: StatusActive,
		On:     true,
	}
	mgr.mu.Lock()
	mgr.index.Accounts = append(mgr.index.Accounts, acc)
	_ = mgr.saveIndexLocked()
	mgr.mu.Unlock()

	// 1. acquireAccountGate 在已取消 ctx 时不应授予新操作继续工作
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	release, err := mgr.acquireAccountGate(canceledCtx, "acc-gate-cancel")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if release != nil {
		t.Fatal("release func must be nil on canceled ctx")
	}

	// 验证 gate 确实未被占用，正常 ctx 可以立即获取
	relNormal, err := mgr.acquireAccountGate(context.Background(), "acc-gate-cancel")
	if err != nil {
		t.Fatalf("normal ctx failed to acquire gate: %v", err)
	}
	relNormal()

	// 2. 使用可取消 RoundTripper 验证完整预热在请求阻塞中被取消能迅速返回并释放 gate
	rt := &testBlockingRoundTripper{started: make(chan struct{}, 1)}
	client := &http.Client{Transport: rt}
	mgr.opts.HTTPClient = client
	_ = mgr.WriteToken("acc-gate-cancel", OAuthTokenFile{
		Token: OAuthTokenData{AccessToken: "token-gate", Expiry: now.Add(time.Hour)},
	})

	runCtx, runCancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- mgr.Warmup(runCtx, "acc-gate-cancel")
	}()

	// 等待请求进入 RoundTripper（有界等待）
	select {
	case <-rt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for request to enter RoundTripper")
	}
	// 取消 context
	runCancel()

	select {
	case warmupErr := <-errCh:
		if !errors.Is(warmupErr, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", warmupErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Warmup did not return promptly on cancellation")
	}

	// 验证 gate 已被释放，后续操作可立即获取 gate
	relAfter, err := mgr.acquireAccountGate(context.Background(), "acc-gate-cancel")
	if err != nil {
		t.Fatalf("gate was not released after canceled warmup: %v", err)
	}
	relAfter()
}

func TestWarmup_MaxAttemptsLimitAndNewCycleRecovery(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	currNow := now

	var genCalls atomic.Int32
	var currentCycle atomic.Int32 // 0: cycle1; 1: cycle2

	cycle1Reset := now.Add(-1 * time.Hour).Format(time.RFC3339)
	cycle2Reset := now.Add(7 * 24 * time.Hour).Format(time.RFC3339)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"proj-max-attempts"}`))
		case "/v1internal:fetchAvailableModels":
			rTime := cycle1Reset
			if currentCycle.Load() == 1 {
				rTime = cycle2Reset
			}
			data := map[string]any{
				"models": map[string]any{
					"gemini-2.5-flash": map[string]any{
						"displayName": "Gemini 2.5 Flash",
						"quotaInfo": map[string]any{
							"remainingFraction": 1.0,
							"resetTime":         rTime,
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(data)
		case "/v1internal:retrieveUserQuotaSummary":
			rTime := cycle1Reset
			if currentCycle.Load() == 1 {
				rTime = cycle2Reset
			}
			testGeminiWeeklyQuotaResponse(w, rTime)
		case "/v1internal:generateContent":
			genCalls.Add(1)
			// 返回 400 明确模型拒绝
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unknown model gemini-2.5-flash"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	mgr, err := NewManager(Options{
		DataDir:    dataDir,
		HostHome:   t.TempDir(),
		Now:        func() time.Time { return currNow },
		QuotaHosts: []string{srv.URL},
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}

	acc := Account{
		ID:     "acc-limit",
		Name:   "Limit Test",
		Status: StatusActive,
		On:     true,
		Quota: map[string]QuotaGroup{
			quota.GroupGemini + " 7d": {UsedPercent: 100, ResetAt: cycle1Reset},
		},
	}
	mgr.mu.Lock()
	mgr.index.Accounts = append(mgr.index.Accounts, acc)
	_ = mgr.saveIndexLocked()
	mgr.mu.Unlock()
	_ = mgr.WriteToken("acc-limit", OAuthTokenFile{
		Token: OAuthTokenData{AccessToken: "token-limit", Expiry: now.Add(30 * 24 * time.Hour)},
	})

	// 尝试 1：明确失败
	_ = mgr.Warmup(context.Background(), "acc-limit")
	if genCalls.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", genCalls.Load())
	}
	acc1, _ := mgr.Get("acc-limit")
	if acc1.LastWarmup == nil || acc1.LastWarmup.Attempts != 1 || acc1.LastWarmup.Status != "failed" {
		t.Fatalf("expected Attempts=1, status=failed, got %+v", acc1.LastWarmup)
	}

	// 5 分钟内重试：退避阻止
	currNow = currNow.Add(2 * time.Minute)
	_ = mgr.Warmup(context.Background(), "acc-limit")
	if genCalls.Load() != 1 {
		t.Fatalf("debounce must prevent generation within 5m, got %d", genCalls.Load())
	}

	// 推进 6 分钟：尝试 2
	currNow = currNow.Add(6 * time.Minute)
	_ = mgr.Warmup(context.Background(), "acc-limit")
	if genCalls.Load() != 2 {
		t.Fatalf("expected 2 calls, got %d", genCalls.Load())
	}
	acc2, _ := mgr.Get("acc-limit")
	if acc2.LastWarmup == nil || acc2.LastWarmup.Attempts != 2 {
		t.Fatalf("expected Attempts=2, got %+v", acc2.LastWarmup)
	}

	// 推进 6 分钟：尝试 3
	currNow = currNow.Add(6 * time.Minute)
	_ = mgr.Warmup(context.Background(), "acc-limit")
	if genCalls.Load() != 3 {
		t.Fatalf("expected 3 calls, got %d", genCalls.Load())
	}
	acc3, _ := mgr.Get("acc-limit")
	if acc3.LastWarmup == nil || acc3.LastWarmup.Attempts != 3 {
		t.Fatalf("expected Attempts=3, got %+v", acc3.LastWarmup)
	}

	// 模拟多轮轮询推进时钟：均因达到上限不再生成
	for i := 0; i < 5; i++ {
		currNow = currNow.Add(10 * time.Minute)
		_ = mgr.Warmup(context.Background(), "acc-limit")
	}
	if genCalls.Load() != 3 {
		t.Fatalf("must not exceed max attempts limit across polls, got %d", genCalls.Load())
	}

	// 进入真实新周期：推进到 cycle2 到期
	currentCycle.Store(1)
	currNow = now.Add(8 * 24 * time.Hour) // 到达 cycle2Reset 之后
	_ = mgr.Warmup(context.Background(), "acc-limit")
	if genCalls.Load() != 4 {
		t.Fatalf("entering real new cycle must recover and allow attempt, got %d", genCalls.Load())
	}
}

func TestSetAutoWarmup_SaveFailureRollback(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Now()

	mgr, err := NewManager(Options{
		DataDir:  dataDir,
		HostHome: t.TempDir(),
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	// 初始状态默认为 true
	if !mgr.AutoWarmup() {
		t.Fatal("expected AutoWarmup to default to true")
	}

	// 写入合法的现有配置
	routingPath := filepath.Join(dataDir, "agy-routing.json")
	_ = os.WriteFile(routingPath, []byte(`{"autoWarmup":true}`), 0600)

	// 将目录设为不可写使其保存失败
	if err := os.Chmod(dataDir, 0500); err != nil {
		t.Skip("cannot chmod directory, skipping permission test")
	}
	defer func() {
		_ = os.Chmod(dataDir, 0700)
	}()

	// 尝试修改为 false，落盘失败必须回滚内存状态
	err = mgr.SetAutoWarmup(false)
	if err == nil {
		t.Fatal("expected error on read-only directory")
	}

	// 验证内存值未改变
	if !mgr.AutoWarmup() {
		t.Fatal("expected AutoWarmup to roll back to true on save failure")
	}

	// 恢复写权限后验证原文件未损坏
	_ = os.Chmod(dataDir, 0700)
	data, readErr := os.ReadFile(routingPath)
	if readErr != nil {
		t.Fatalf("routing file was corrupted or missing: %v", readErr)
	}
	if !strings.Contains(string(data), `"autoWarmup":true`) {
		t.Fatalf("expected uncorrupted routing content, got: %s", string(data))
	}
}
