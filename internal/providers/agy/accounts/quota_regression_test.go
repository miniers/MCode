package accounts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/providers/agy/quota"
)

func TestSystemQuotaCredentialsReadOnly(t *testing.T) {
	srv, _, tokenCalls := setupQuotaMockServer(t)
	defer srv.Close()
	source := OAuthTokenFile{Token: OAuthTokenData{
		AccessToken: "expired-token", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour),
	}}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	encoded := []byte("go-keyring-base64:" + base64.StdEncoding.EncodeToString(raw))
	home := t.TempDir()
	m, err := NewManager(Options{
		DataDir: t.TempDir(), HostHome: home, HTTPClient: srv.Client(),
		QuotaHosts: []string{srv.URL}, Endpoints: OAuthEndpoints{TokenURL: srv.URL + "/token"},
		SystemTokenReader: func(context.Context) (OAuthTokenFile, error) { return decodeSystemToken(encoded) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RefreshQuota(context.Background(), SystemAccountID); err != nil {
		t.Fatal(err)
	}
	acc, _ := m.Get(SystemAccountID)
	if acc.ProjectID != "mock-pid" || atomic.LoadInt32(tokenCalls) != 1 {
		t.Fatal("system credentials were not used for quota refresh")
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini")); !os.IsNotExist(err) {
		t.Fatalf("system token refresh wrote to host home: %v", err)
	}
	if source.Token.AccessToken != "expired-token" {
		t.Fatal("credential source was mutated")
	}
}

func TestQuotaActivatesExpiredWeeklyWindowOnce(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	serverNow := now
	var activationCalls atomic.Int32
	var serverResetAdvanced atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"mock-pid"}`))
		case "/v1internal:fetchAvailableModels":
			data := map[string]any{
				"models": map[string]any{
					"gemini-2.5-flash": map[string]any{
						"displayName": "Gemini 2.5 Flash",
						"quotaInfo": map[string]any{
							"remainingFraction": 1.0,
							"resetTime":         serverNow.Add(time.Hour).Format(time.RFC3339),
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(data)
		case "/v1internal:generateContent":
			activationCalls.Add(1)
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
		case "/v1internal:retrieveUserQuotaSummary":
			reset := serverNow.Add(-time.Minute)
			if serverResetAdvanced.Load() {
				reset = serverNow.Add(7 * 24 * time.Hour)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"groups": []any{map[string]any{
				"displayName": quota.GroupGemini, "buckets": []any{map[string]any{
					"window": "7d", "remainingFraction": 1, "resetTime": reset.Format(time.RFC3339),
				}},
			}}})
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	m, err := NewManager(Options{
		DataDir: t.TempDir(), HostHome: t.TempDir(), HTTPClient: srv.Client(),
		QuotaHosts: []string{srv.URL}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	id := createTestAccountWithToken(t, m, "weekly@example.com", "valid", "refresh", now.Add(time.Hour))
	m.mu.Lock()
	for i := range m.index.Accounts {
		if m.index.Accounts[i].ID == id {
			m.index.Accounts[i].QuotaReadAt = now.Add(-time.Hour).Format(time.RFC3339)
			m.index.Accounts[i].Quota = map[string]QuotaGroup{
				quota.GroupGemini + " 7d": {UsedPercent: 100, ResetAt: now.Add(-time.Minute).Format(time.RFC3339)},
			}
		}
	}
	m.mu.Unlock()

	// 第一次刷新：预热失败不丢弃额度拉取结果，额度不应为 stale，预热状态保留错误
	if err := m.RefreshQuota(context.Background(), id); err != nil {
		t.Fatalf("quota fetch should succeed even if warmup fails: %v", err)
	}
	accAfterFail, _ := m.Get(id)
	if accAfterFail.QuotaStale {
		t.Fatal("quota should not be stale when quota fetch succeeded")
	}
	if accAfterFail.LastWarmup == nil || accAfterFail.LastWarmup.Status != "network_unknown" {
		t.Fatalf("expected LastWarmup to preserve network_unknown error, got %+v", accAfterFail.LastWarmup)
	}

	// 推进时钟后再次刷新：网络未决状态绝不盲目重新调用生成，只补查额度
	now = now.Add(time.Minute)
	if err := m.RefreshQuota(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if activationCalls.Load() != 1 {
		t.Fatalf("network_unknown should not blindly re-generate, got %d calls", activationCalls.Load())
	}

	// 服务端在后续真实推进周期，普通刷新应将其推进为确认成功
	serverResetAdvanced.Store(true)
	now = now.Add(time.Minute)
	if err := m.RefreshQuota(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if activationCalls.Load() != 1 {
		t.Fatalf("successful cycle advance should not re-generate, got %d calls", activationCalls.Load())
	}
	acc, _ := m.Get(id)
	if acc.LastWarmup == nil || acc.LastWarmup.Status != "success" {
		t.Fatalf("expected LastWarmup to advance to success, got %+v", acc.LastWarmup)
	}
	reset, _ := time.Parse(time.RFC3339, acc.Quota[quota.GroupGemini+" 7d"].ResetAt)
	if !reset.After(now) {
		t.Fatal("activated window was not reloaded")
	}
}

func TestQuotaRefreshDoesNotRecreateDeletedCredentials(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"access_token":"fresh","expires_in":3600}`))
	}))
	defer server.Close()
	m, err := NewManager(Options{
		DataDir: t.TempDir(), HostHome: t.TempDir(),
		HTTPClient: server.Client(), Endpoints: OAuthEndpoints{TokenURL: server.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := createTestAccountWithToken(t, m, "deleted@example.com", "expired", "refresh", time.Now().Add(-time.Hour))
	result := make(chan error, 1)
	go func() { result <- m.RefreshQuota(context.Background(), id) }()
	<-started
	removeErr := m.Remove(id)
	close(release)
	if removeErr != nil {
		t.Fatal(removeErr)
	}
	if err := <-result; !errors.Is(err, ErrAccountGone) {
		t.Fatalf("expected ErrAccountGone, got %v", err)
	}
	if _, err := os.Stat(m.accountDir(id)); !os.IsNotExist(err) {
		t.Fatalf("deleted credentials directory was recreated: %v", err)
	}
	if err := m.RefreshQuota(context.Background(), id); !errors.Is(err, ErrAccountGone) {
		t.Fatalf("debounce hid deletion: %v", err)
	}
}

func TestQuotaRefreshDoesNotOverwriteNewLogin(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			close(started)
			<-release
			_, _ = w.Write([]byte(`{"access_token":"old-refresh-result","expires_in":3600}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer new-login" {
			t.Errorf("quota request used superseded credentials")
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	m, err := NewManager(Options{
		DataDir: t.TempDir(), HostHome: t.TempDir(), HTTPClient: server.Client(),
		QuotaHosts: []string{server.URL}, Endpoints: OAuthEndpoints{TokenURL: server.URL + "/token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := createTestAccountWithToken(t, m, "login@example.com", "expired", "refresh", time.Now().Add(-time.Hour))
	result := make(chan error, 1)
	go func() { result <- m.RefreshQuota(context.Background(), id) }()
	<-started
	_, loginErr := m.mergeAndSaveAccount(OAuthTokenFile{Token: OAuthTokenData{
		AccessToken: "new-login", RefreshToken: "new-refresh", Expiry: time.Now().Add(time.Hour),
	}}, "login@example.com", "New login")
	close(release)
	if loginErr != nil {
		t.Fatal(loginErr)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	var token OAuthTokenFile
	if err := m.withTokenLock(id, func(p string) error {
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, &token)
	}); err != nil {
		t.Fatal(err)
	}
	if token.Token.AccessToken != "new-login" {
		t.Fatal("new login was overwritten by an old refresh")
	}
}

func TestQuotaBatchAndDebouncePreserveErrors(t *testing.T) {
	m, err := NewManager(Options{DataDir: t.TempDir(), HostHome: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RefreshQuota(context.Background(), ""); err == nil {
		t.Fatal("batch suppressed the missing token error")
	}
	if err := m.RefreshQuota(context.Background(), SystemAccountID); err == nil {
		t.Fatal("debounce changed failure into success")
	}
	acc, _ := m.Get(SystemAccountID)
	if !acc.QuotaStale || acc.Status != StatusActive {
		t.Fatalf("missing credentials should mark quota stale without disabling session: %+v", acc)
	}
}

func TestQuotaSessionLeavesSpentAccountAndSmartUsesWeeklyReset(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	m, err := NewManager(Options{DataDir: t.TempDir(), HostHome: t.TempDir(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	a := createTestAccountWithToken(t, m, "a@example.com", "a", "r", now.Add(time.Hour))
	b := createTestAccountWithToken(t, m, "b@example.com", "b", "r", now.Add(time.Hour))
	m.mu.Lock()
	for i := range m.index.Accounts {
		acc := &m.index.Accounts[i]
		if acc.ID == SystemAccountID {
			acc.On = false
			continue
		}
		short, weekly := now.Add(time.Hour), now.Add(6*24*time.Hour)
		if acc.ID == b {
			short, weekly = now.Add(2*time.Hour), now.Add(24*time.Hour)
		}
		acc.QuotaReadAt = now.Format(time.RFC3339)
		acc.Quota = map[string]QuotaGroup{
			quota.GroupGemini:         {UsedPercent: 20, ResetAt: short.Format(time.RFC3339)},
			quota.GroupGemini + " 7d": {UsedPercent: 10, ResetAt: weekly.Format(time.RFC3339)},
		}
	}
	m.mu.Unlock()
	picked, _, _, err := m.Route("new", "gemini-3-flash", nil)
	if err != nil || picked != b {
		t.Fatalf("smart should prefer earlier weekly reset: %s, %v", picked, err)
	}
	m.mu.Lock()
	for i := range m.index.Accounts {
		if m.index.Accounts[i].ID == b {
			m.index.Accounts[i].Quota[quota.GroupGemini] = QuotaGroup{UsedPercent: 100}
		}
	}
	m.mu.Unlock()
	picked, _, _, err = m.Route("new", "gemini-3-flash", nil)
	if err != nil || picked != a {
		t.Fatalf("session stayed on exhausted account: %s, %v", picked, err)
	}
}

func TestQuotaActiveAndTokenChangeRefresh(t *testing.T) {
	srv, calls, _ := setupQuotaMockServer(t)
	defer srv.Close()
	now := time.Now()
	m, err := NewManager(Options{
		DataDir: t.TempDir(), HostHome: t.TempDir(), HTTPClient: srv.Client(),
		QuotaHosts: []string{srv.URL}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	id := createTestAccountWithToken(t, m, "active@example.com", "valid", "r", now.Add(time.Hour))
	if err := m.RefreshQuota(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	end := m.QuotaUse(id)
	now = now.Add(time.Minute)
	m.pollActiveQuota(context.Background())
	if atomic.LoadInt32(calls) != 2 {
		t.Fatalf("active account not refreshed after one minute: %d", *calls)
	}
	end()
	now = now.Add(10 * time.Second)
	path := filepath.Join(m.accountHomeDir(id), ".gemini", "antigravity-cli", "antigravity-oauth-token")
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	m.pollActiveQuota(context.Background())
	if atomic.LoadInt32(calls) != 2 {
		t.Fatal("token-change refresh ignored 30-second debounce")
	}
	now = now.Add(20 * time.Second)
	m.pollActiveQuota(context.Background())
	if atomic.LoadInt32(calls) != 3 {
		t.Fatal("new token did not trigger refresh")
	}
	now = now.Add(time.Minute)
	m.pollActiveQuota(context.Background())
	if atomic.LoadInt32(calls) != 3 {
		t.Fatal("inactive account refreshed without a token change")
	}
}

func TestEffectiveQuotaResetBoundary(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	q := QuotaGroup{UsedPercent: 100, ResetAt: now.Format(time.RFC3339)}
	if got := EffectiveQuota(q, now.Add(-time.Minute).Format(time.RFC3339), now); got.UsedPercent != 0 {
		t.Fatal("quota not reset at reset boundary")
	}
	if got := EffectiveQuota(q, "", now); got.UsedPercent != 100 {
		t.Fatal("unknown read time must not imply a reset")
	}
}
