package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/providers/agy/quota"
	"mcode/internal/providers/multiaccount"
)

func init() {
	quotaBatchWaitMin = 0
	quotaBatchWaitMax = 0
	quotaDebounceDuration = 15 * time.Second
}

func setupQuotaMockServer(t *testing.T) (*httptest.Server, *int32, *int32) {
	var quotaCalls int32
	var tokenCalls int32

	mux := http.NewServeMux()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "refreshed-access-token",
			"expires_in":   3600,
		})
	})

	mux.HandleFunc("/v1internal:loadCodeAssist", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&quotaCalls, 1)
		auth := r.Header.Get("Authorization")
		if auth == "Bearer expired-token" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"cloudaicompanionProject": "mock-pid",
			"paidTier":                map[string]any{"name": "Pro Tier"},
		})
	})

	mux.HandleFunc("/v1internal:fetchAvailableModels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": map[string]any{
				"gemini-1.5-pro": map[string]any{
					"quotaInfo": map[string]any{
						"remainingFraction": 0.7,
						"resetTime":         "2026-10-17T20:00:00Z",
					},
				},
			},
		})
	})

	mux.HandleFunc("/v1internal:retrieveUserQuotaSummary", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"groups": []map[string]any{
				{
					"displayName": "Gemini Models",
					"buckets": []map[string]any{
						{
							"window":            "5h",
							"remainingFraction": 0.8,
							"resetTime":         "2026-10-17T20:00:00Z",
						},
						{
							"window":            "7d",
							"remainingFraction": 0.9,
							"resetTime":         "2026-10-24T00:00:00Z",
						},
					},
				},
			},
		})
	})

	srv := httptest.NewServer(mux)
	return srv, &quotaCalls, &tokenCalls
}

func createTestAccountWithToken(t *testing.T, mgr *Manager, email, token, rtoken string, expiry time.Time) string {
	acc, err := mgr.mergeAndSaveAccount(OAuthTokenFile{
		AuthMethod: "consumer",
		Token: OAuthTokenData{
			AccessToken:  token,
			RefreshToken: rtoken,
			TokenType:    "Bearer",
			Expiry:       expiry,
		},
	}, email, "User "+email)
	if err != nil {
		t.Fatalf("failed to create test account: %v", err)
	}
	return acc.ID
}

// 1. RefreshQuota 写回 ProjectID / Tier / Quota / QuotaReadAt
func TestAccounts_RefreshQuotaSuccess(t *testing.T) {
	srv, _, _ := setupQuotaMockServer(t)
	defer srv.Close()

	dataDir := t.TempDir()
	hostHome := t.TempDir()

	mgr, err := NewManager(Options{
		DataDir:    dataDir,
		HostHome:   hostHome,
		HTTPClient: srv.Client(),
		QuotaHosts: []string{srv.URL},
		Endpoints: OAuthEndpoints{
			TokenURL: srv.URL + "/token",
		},
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	accID := createTestAccountWithToken(t, mgr, "user1@example.com", "valid-token", "valid-refresh", time.Now().Add(1*time.Hour))

	err = mgr.RefreshQuota(context.Background(), accID)
	if err != nil {
		t.Fatalf("RefreshQuota failed: %v", err)
	}

	acc, ok := mgr.Get(accID)
	if !ok {
		t.Fatalf("account %s not found", accID)
	}

	if acc.ProjectID != "mock-pid" {
		t.Errorf("expected ProjectID mock-pid, got %s", acc.ProjectID)
	}
	if acc.Tier != "Pro Tier" {
		t.Errorf("expected Tier Pro Tier, got %s", acc.Tier)
	}
	if acc.QuotaReadAt == "" {
		t.Errorf("expected non-empty QuotaReadAt")
	}
	if acc.QuotaStale {
		t.Errorf("expected QuotaStale false")
	}

	g, ok := acc.Quota[quota.GroupGemini]
	if !ok {
		t.Fatalf("missing QuotaGroup %s", quota.GroupGemini)
	}
	if math.Abs(g.UsedPercent-20.0) > 1e-9 {
		t.Errorf("expected Gemini Models 5h used 20%%, got %f", g.UsedPercent)
	}
	if g.ResetAt != "2026-10-17T20:00:00Z" {
		t.Errorf("expected ResetAt RFC3339 2026-10-17T20:00:00Z, got %s", g.ResetAt)
	}
}

// 2. 全部失败时 QuotaStale=true 且保留旧数据
func TestAccounts_RefreshQuotaPreservesOldDataOnFailure(t *testing.T) {
	// 500 故障服务器
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	hostHome := t.TempDir()

	mgr, err := NewManager(Options{
		DataDir:    dataDir,
		HostHome:   hostHome,
		HTTPClient: srv.Client(),
		QuotaHosts: []string{srv.URL},
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	accID := createTestAccountWithToken(t, mgr, "user2@example.com", "token", "refresh", time.Now().Add(1*time.Hour))

	// 先注入旧的有效 Quota
	mgr.mu.Lock()
	for i := range mgr.index.Accounts {
		if mgr.index.Accounts[i].ID == accID {
			mgr.index.Accounts[i].Quota = map[string]QuotaGroup{
				quota.GroupGemini: {
					UsedPercent: 42.0,
					ResetAt:     "2026-10-17T12:00:00Z",
				},
			}
			mgr.index.Accounts[i].QuotaStale = false
			_ = mgr.saveIndexLocked()
			break
		}
	}
	mgr.mu.Unlock()

	// 执行刷新，预期失败
	err = mgr.RefreshQuota(context.Background(), accID)
	if err == nil {
		t.Fatalf("expected error from failed server")
	}

	acc, _ := mgr.Get(accID)
	if !acc.QuotaStale {
		t.Errorf("expected QuotaStale to be true")
	}
	if g, ok := acc.Quota[quota.GroupGemini]; !ok || math.Abs(g.UsedPercent-42.0) > 1e-9 {
		t.Errorf("expected preserved old Quota with 42%%, got %v", g)
	}
}

// 3. 401 后刷新 token 重试成功，并写回隔离账号文件
func TestAccounts_RefreshQuota401TokenRefreshAndRetry(t *testing.T) {
	srv, _, tokenCalls := setupQuotaMockServer(t)
	defer srv.Close()

	dataDir := t.TempDir()
	hostHome := t.TempDir()

	mgr, err := NewManager(Options{
		DataDir:    dataDir,
		HostHome:   hostHome,
		HTTPClient: srv.Client(),
		QuotaHosts: []string{srv.URL},
		Endpoints: OAuthEndpoints{
			TokenURL: srv.URL + "/token",
		},
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	accID := createTestAccountWithToken(t, mgr, "user3@example.com", "expired-token", "refresh-token-3", time.Now().Add(1*time.Hour))

	err = mgr.RefreshQuota(context.Background(), accID)
	if err != nil {
		t.Fatalf("expected retry after 401 to succeed, got %v", err)
	}

	if atomic.LoadInt32(tokenCalls) == 0 {
		t.Errorf("expected token refresh endpoint to be called")
	}

	// 检查隔离账号的 token 文件已被回写为 refreshed-access-token
	var tok OAuthTokenFile
	_ = mgr.withTokenLock(accID, func(p string) error {
		data, _ := os.ReadFile(p)
		return json.Unmarshal(data, &tok)
	})
	if tok.Token.AccessToken != "refreshed-access-token" {
		t.Errorf("expected file token updated to refreshed-access-token, got %s", tok.Token.AccessToken)
	}
}

// 4. 15 秒防抖（同一账号 15 秒内重复调用直接返回 nil）
func TestAccounts_RefreshQuotaDebounce(t *testing.T) {
	srv, quotaCalls, _ := setupQuotaMockServer(t)
	defer srv.Close()

	dataDir := t.TempDir()
	hostHome := t.TempDir()

	mgr, err := NewManager(Options{
		DataDir:    dataDir,
		HostHome:   hostHome,
		HTTPClient: srv.Client(),
		QuotaHosts: []string{srv.URL},
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	accID := createTestAccountWithToken(t, mgr, "user4@example.com", "token", "refresh", time.Now().Add(1*time.Hour))

	// 第一次调用
	err = mgr.RefreshQuota(context.Background(), accID)
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	initialCalls := atomic.LoadInt32(quotaCalls)

	// 立即再次调用，应被 15s 防抖命中，直接返回 nil 且不发请求
	err = mgr.RefreshQuota(context.Background(), accID)
	if err != nil {
		t.Fatalf("second call within debounce should return nil: %v", err)
	}
	secondCalls := atomic.LoadInt32(quotaCalls)

	if secondCalls != initialCalls {
		t.Errorf("expected no additional network requests within debounce window, calls: %d -> %d", initialCalls, secondCalls)
	}
}

// 5. 账号被删后不回写（防幽灵写入，返回 ErrAccountGone）
func TestAccounts_GhostWriteProtection(t *testing.T) {
	dataDir := t.TempDir()
	hostHome := t.TempDir()

	mgr, err := NewManager(Options{
		DataDir:  dataDir,
		HostHome: hostHome,
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	accID := createTestAccountWithToken(t, mgr, "deleted@example.com", "token", "refresh", time.Now().Add(1*time.Hour))

	// 删除该账号
	if err := mgr.Remove(accID); err != nil {
		t.Fatalf("remove failed: %v", err)
	}

	// 模拟异步任务尝试回写或刷新该已删除账号
	err = mgr.RefreshQuota(context.Background(), accID)
	if !errors.Is(err, ErrAccountGone) {
		t.Errorf("expected ErrAccountGone, got %v", err)
	}
}

// 6. 两个账号额度不同时 Route 选中剩余额度更多的账号，以及乐观重置
func TestAccounts_RouteSelectionAndOptimisticReset(t *testing.T) {
	dataDir := t.TempDir()
	hostHome := t.TempDir()

	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	mgr, err := NewManager(Options{
		DataDir:  dataDir,
		HostHome: hostHome,
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	// 设置 usage 调度策略
	_ = mgr.SetRouting(multiaccount.Config{
		Strategy: multiaccount.StrategyUsage,
		Affinity: multiaccount.AffinityTurn,
	})

	// 账号 A: Used 80%
	accA := createTestAccountWithToken(t, mgr, "a@example.com", "tokenA", "rA", now.Add(1*time.Hour))
	// 账号 B: Used 20%
	accB := createTestAccountWithToken(t, mgr, "b@example.com", "tokenB", "rB", now.Add(1*time.Hour))

	mgr.mu.Lock()
	for i := range mgr.index.Accounts {
		if mgr.index.Accounts[i].ID == accA {
			mgr.index.Accounts[i].Quota = map[string]QuotaGroup{
				quota.GroupGemini: {UsedPercent: 80.0, ResetAt: "2026-10-17T15:00:00Z"},
			}
			mgr.index.Accounts[i].QuotaReadAt = "2026-10-17T11:00:00Z"
		}
		if mgr.index.Accounts[i].ID == accB {
			mgr.index.Accounts[i].Quota = map[string]QuotaGroup{
				quota.GroupGemini: {UsedPercent: 20.0, ResetAt: "2026-10-17T15:00:00Z"},
			}
			mgr.index.Accounts[i].QuotaReadAt = "2026-10-17T11:00:00Z"
		}
	}
	_ = mgr.saveIndexLocked()
	mgr.mu.Unlock()

	// 轮选，应选中已用比例更低（剩余更多）的账号 B
	chosen, _, _, err := mgr.Route("conv-1", "gemini-1.5-flash", []string{SystemAccountID})
	if err != nil {
		t.Fatalf("Route failed: %v", err)
	}
	if chosen != accB {
		t.Errorf("expected accB (20%% used) to be chosen over accA (80%% used), got %s", chosen)
	}

	// 测试乐观重置：将 accA 的 ResetAt 设为 11:30（已早于 now 12:00），且 QuotaReadAt 是 11:00（早于 ResetAt）
	mgr.mu.Lock()
	for i := range mgr.index.Accounts {
		if mgr.index.Accounts[i].ID == accA {
			mgr.index.Accounts[i].Quota = map[string]QuotaGroup{
				quota.GroupGemini: {UsedPercent: 80.0, ResetAt: "2026-10-17T11:30:00Z"},
			}
			mgr.index.Accounts[i].QuotaReadAt = "2026-10-17T11:00:00Z"
		}
	}
	_ = mgr.saveIndexLocked()
	mgr.mu.Unlock()

	// 此时 accA 发生乐观重置，UsedPercent 视为 0%，应被选出
	chosen, _, _, err = mgr.Route("conv-2", "gemini-1.5-flash", []string{SystemAccountID})
	if err != nil {
		t.Fatalf("Route failed: %v", err)
	}
	if chosen != accA {
		t.Errorf("expected accA (optimistically reset to 0%%) to be chosen, got %s", chosen)
	}
}
