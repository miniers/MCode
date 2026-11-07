package accounts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/providers/multiaccount"
)

// setupMockOAuthServer 创建模拟的 Google OAuth Token 与 UserInfo 端点服务。
func setupMockOAuthServer(t *testing.T, emailSeq *int32) (*httptest.Server, OAuthEndpoints) {
	mux := http.NewServeMux()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		code := r.Form.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}

		resp := tokenExchangeResponse{
			AccessToken:  "mock-access-token-" + code,
			TokenType:    "Bearer",
			RefreshToken: "mock-refresh-token-" + code,
			ExpiresIn:    3600,
			IDToken:      "mock-id-token",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		token := strings.TrimPrefix(auth, "Bearer ")
		var email string
		if strings.HasPrefix(token, "mock-access-token-code-") {
			// 从 code 中提取或使用动态序列
			suffix := strings.TrimPrefix(token, "mock-access-token-code-")
			email = fmt.Sprintf("user-%s@example.com", suffix)
		} else {
			seq := atomic.AddInt32(emailSeq, 1)
			email = fmt.Sprintf("user-%d@example.com", seq)
		}

		resp := userInfoResponse{
			Email: email,
			Name:  "Test User " + email,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	srv := httptest.NewServer(mux)
	endpoints := OAuthEndpoints{
		AuthURL:     srv.URL + "/auth",
		TokenURL:    srv.URL + "/token",
		UserInfoURL: srv.URL + "/userinfo",
	}
	return srv, endpoints
}

// 1. 索引原子写入与 0600 权限、首次加载自动补系统账号
func TestIndexAtomicAndSystemAccount(t *testing.T) {
	dataDir := t.TempDir()
	hostHome := t.TempDir()

	mgr, err := NewManager(Options{
		DataDir:  dataDir,
		HostHome: hostHome,
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	indexPath := filepath.Join(dataDir, accountsFileName)
	fi, err := os.Stat(indexPath)
	if err != nil {
		t.Fatalf("stat index file: %v", err)
	}

	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("expected index file permission 0600, got %o", perm)
	}

	accounts := mgr.List()
	if len(accounts) != 1 {
		t.Fatalf("expected exactly 1 account initially, got %d", len(accounts))
	}
	if accounts[0].ID != SystemAccountID {
		t.Errorf("expected system account ID 'system', got %q", accounts[0].ID)
	}
	if accounts[0].Status != StatusActive || !accounts[0].On || accounts[0].Weight != 100 {
		t.Errorf("system account default properties invalid: %+v", accounts[0])
	}
}

// 2. 新增账号（通过 Submit 提交裸 code 以及完整回调 URL 两条路径）
// 校验目录布局、软链接目标、token 文件内容与 0600 权限、installation_id、DeviceProfile 格式
func TestLoginSuccess_RawCodeAndCallbackURL(t *testing.T) {
	dataDir := t.TempDir()
	hostHome := t.TempDir()
	var emailSeq int32
	srv, endpoints := setupMockOAuthServer(t, &emailSeq)
	defer srv.Close()

	mgr, err := NewManager(Options{
		DataDir:   dataDir,
		HostHome:  hostHome,
		Endpoints: endpoints,
		Opener:    func(u string) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	ctx := context.Background()

	// 路径 A: 提交裸 code
	sessA, err := mgr.LoginStart(ctx)
	if err != nil {
		t.Fatalf("LoginStart A failed: %v", err)
	}
	if err := sessA.Submit("code-raw-1"); err != nil {
		t.Fatalf("Submit raw code failed: %v", err)
	}
	accA, err := sessA.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait A failed: %v", err)
	}

	if accA.Email != "user-raw-1@example.com" {
		t.Errorf("unexpected email for accA: %q", accA.Email)
	}

	// 验证 accA 目录布局与软链接
	accHomeA := mgr.accountHomeDir(accA.ID)
	tokenPathA := filepath.Join(accHomeA, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	tokenFI, err := os.Stat(tokenPathA)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if tokenFI.Mode().Perm() != 0600 {
		t.Errorf("token file perm must be 0600, got %o", tokenFI.Mode().Perm())
	}

	// 验证 token 内容
	var tf OAuthTokenFile
	tokenData, _ := os.ReadFile(tokenPathA)
	if err := json.Unmarshal(tokenData, &tf); err != nil {
		t.Fatalf("unmarshal token file: %v", err)
	}
	if tf.AuthMethod != "consumer" || !strings.Contains(tf.Token.AccessToken, "code-raw-1") {
		t.Errorf("token file content mismatch: %+v", tf)
	}

	// 验证 installation_id 存在且为 0600
	instIDPath := filepath.Join(accHomeA, ".gemini", "installation_id")
	instFI, err := os.Stat(instIDPath)
	if err != nil {
		t.Fatalf("stat installation_id: %v", err)
	}
	if instFI.Mode().Perm() != 0600 {
		t.Errorf("installation_id perm must be 0600, got %o", instFI.Mode().Perm())
	}

	// 验证软链接目标
	gitconfigLink := filepath.Join(accHomeA, ".gitconfig")
	linkTarget, err := os.Readlink(gitconfigLink)
	if err != nil {
		t.Fatalf("readlink .gitconfig: %v", err)
	}
	expectedTarget := filepath.Join(hostHome, ".gitconfig")
	if linkTarget != expectedTarget {
		t.Errorf("expected symlink target %s, got %s", expectedTarget, linkTarget)
	}

	// 验证 DeviceProfile 格式
	dp := accA.DeviceProfile
	if !strings.HasPrefix(dp.MachineId, "auth0|user_") || len(dp.MachineId) != len("auth0|user_")+32 {
		t.Errorf("invalid MachineId format: %s", dp.MachineId)
	}
	if !strings.HasPrefix(dp.SqmId, "{") || !strings.HasSuffix(dp.SqmId, "}") {
		t.Errorf("invalid SqmId format: %s", dp.SqmId)
	}
	if dp.ServiceMachineId != dp.DevDeviceId {
		t.Errorf("serviceMachineId != devDeviceId")
	}

	// 路径 B: 提交完整回调 URL
	sessB, err := mgr.LoginStart(ctx)
	if err != nil {
		t.Fatalf("LoginStart B failed: %v", err)
	}
	fullURL := fmt.Sprintf("%s?code=code-url-2&state=%s", sessB.redirectURI, sessB.state)
	if err := sessB.Submit(fullURL); err != nil {
		t.Fatalf("Submit full url failed: %v", err)
	}
	accB, err := sessB.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait B failed: %v", err)
	}
	if accB.Email != "user-url-2@example.com" {
		t.Errorf("unexpected email for accB: %q", accB.Email)
	}
}

// 3. 同一 email 重复登录不新增账号
func TestDuplicateEmailDoesNotDuplicate(t *testing.T) {
	dataDir := t.TempDir()
	hostHome := t.TempDir()
	var emailSeq int32
	srv, endpoints := setupMockOAuthServer(t, &emailSeq)
	defer srv.Close()

	mgr, _ := NewManager(Options{
		DataDir:   dataDir,
		HostHome:  hostHome,
		Endpoints: endpoints,
		Opener:    func(u string) error { return nil },
	})

	ctx := context.Background()

	sess1, _ := mgr.LoginStart(ctx)
	_ = sess1.Submit("code-dup")
	acc1, err := sess1.Wait(ctx)
	if err != nil {
		t.Fatalf("first login failed: %v", err)
	}

	// 再次使用同一 code（会产生相同的 email）登录
	sess2, _ := mgr.LoginStart(ctx)
	_ = sess2.Submit("code-dup")
	acc2, err := sess2.Wait(ctx)
	if err != nil {
		t.Fatalf("second login failed: %v", err)
	}

	if acc1.ID != acc2.ID {
		t.Errorf("expected same account ID, got %s and %s", acc1.ID, acc2.ID)
	}

	// 验证总账号数量仍为 2 (system + 1 个隔离账号)
	if count := len(mgr.List()); count != 2 {
		t.Errorf("expected 2 accounts, got %d", count)
	}
}

// 4. state 不匹配时拒绝
func TestStateMismatchRejected(t *testing.T) {
	dataDir := t.TempDir()
	hostHome := t.TempDir()
	var emailSeq int32
	srv, endpoints := setupMockOAuthServer(t, &emailSeq)
	defer srv.Close()

	mgr, _ := NewManager(Options{
		DataDir:   dataDir,
		HostHome:  hostHome,
		Endpoints: endpoints,
		Opener:    func(u string) error { return nil },
	})

	ctx := context.Background()
	sess, _ := mgr.LoginStart(ctx)

	err := sess.Submit(fmt.Sprintf("%s?code=123&state=wrong-state", sess.redirectURI))
	if err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Errorf("expected state mismatch error, got %v", err)
	}
	sess.Cancel()
}

// 5. Remove system 返回 ErrSystemAccount
func TestRemoveSystemAccount(t *testing.T) {
	dataDir := t.TempDir()
	hostHome := t.TempDir()

	mgr, _ := NewManager(Options{
		DataDir:  dataDir,
		HostHome: hostHome,
	})

	err := mgr.Remove(SystemAccountID)
	if err != ErrSystemAccount {
		t.Errorf("expected ErrSystemAccount, got %v", err)
	}
}

// 6. Remove 默认账号后默认回到 system，目录被删、绑定被清除
func TestRemoveDefaultAccountAndBindings(t *testing.T) {
	dataDir := t.TempDir()
	hostHome := t.TempDir()
	var emailSeq int32
	srv, endpoints := setupMockOAuthServer(t, &emailSeq)
	defer srv.Close()

	mgr, _ := NewManager(Options{
		DataDir:   dataDir,
		HostHome:  hostHome,
		Endpoints: endpoints,
		Opener:    func(u string) error { return nil },
	})

	ctx := context.Background()
	sess, _ := mgr.LoginStart(ctx)
	_ = sess.Submit("code-rem")
	acc, err := sess.Wait(ctx)
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}

	// 设为默认账号并绑定会话
	if err := mgr.SetDefault(acc.ID); err != nil {
		t.Fatalf("SetDefault failed: %v", err)
	}
	mgr.Bind("conv-test-1", acc.ID)

	bound := mgr.BoundConversations(acc.ID)
	if len(bound) != 1 || bound[0] != "conv-test-1" {
		t.Errorf("unexpected bound conversations: %v", bound)
	}

	// 执行删除
	if err := mgr.Remove(acc.ID); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	// 验证默认账号回退到 system
	if mgr.index.DefaultAccountID != SystemAccountID {
		t.Errorf("expected default account to be system, got %s", mgr.index.DefaultAccountID)
	}

	// 验证物理目录已被删除
	accDir := mgr.accountDir(acc.ID)
	if _, err := os.Stat(accDir); !os.IsNotExist(err) {
		t.Errorf("account dir should be deleted, stat error: %v", err)
	}

	// 验证绑定已清除
	boundAfter := mgr.BoundConversations(acc.ID)
	if len(boundAfter) != 0 {
		t.Errorf("bound conversations should be empty, got %v", boundAfter)
	}
}

// 7. 删除后迟到的 token 回写被丢弃（幽灵写入）
func TestGhostWriteProtection(t *testing.T) {
	dataDir := t.TempDir()
	hostHome := t.TempDir()

	mgr, err := NewManager(Options{
		DataDir:  dataDir,
		HostHome: hostHome,
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	// 1. 新建一个隔离账号并记下其 id 与账号目录
	initialTok := OAuthTokenFile{
		AuthMethod: "consumer",
		Token: OAuthTokenData{
			AccessToken:  "initial-token-1",
			TokenType:    "Bearer",
			RefreshToken: "initial-refresh-1",
			Expiry:       time.Now().Add(time.Hour),
		},
	}
	acc, err := mgr.mergeAndSaveAccount(initialTok, "ghost-test@example.com", "Ghost Tester")
	if err != nil {
		t.Fatalf("mergeAndSaveAccount failed: %v", err)
	}
	id := acc.ID
	accDir := filepath.Join(dataDir, "agy-accounts", id)

	if _, err := os.Stat(accDir); err != nil {
		t.Fatalf("account dir %s should exist before remove: %v", accDir, err)
	}

	// 2. 调用 Remove(id)
	if err := mgr.Remove(id); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	// 3. 调用 WriteToken(id, tokenFile)
	tokenFile := OAuthTokenFile{
		AuthMethod: "consumer",
		Token: OAuthTokenData{
			AccessToken: "ghost-token",
		},
	}
	err = mgr.WriteToken(id, tokenFile)

	// 断言：返回 errors.Is(err, ErrAccountGone)
	if !errors.Is(err, ErrAccountGone) {
		t.Errorf("expected ErrAccountGone, got %v", err)
	}

	// 断言：账号目录不存在
	if _, statErr := os.Stat(accDir); !os.IsNotExist(statErr) {
		t.Errorf("account dir %s should not exist after remove, stat err: %v", accDir, statErr)
	}

	// 断言：List() 中没有这个 id
	for _, a := range mgr.List() {
		if a.ID == id {
			t.Errorf("account %s still found in List()", id)
		}
	}

	// 断言：重新用 NewManager 加载同一 dataDir 后，索引里也没有它
	reloadedMgr, err := NewManager(Options{
		DataDir:  dataDir,
		HostHome: hostHome,
	})
	if err != nil {
		t.Fatalf("reload NewManager failed: %v", err)
	}
	for _, a := range reloadedMgr.List() {
		if a.ID == id {
			t.Errorf("account %s still found in reloaded index", id)
		}
	}

	// 补测：对存在的账号调用 WriteToken 后，token 文件内容更新且权限为 0600
	activeTok := OAuthTokenFile{
		AuthMethod: "consumer",
		Token: OAuthTokenData{
			AccessToken:  "active-initial-token",
			TokenType:    "Bearer",
			RefreshToken: "active-refresh",
			Expiry:       time.Now().Add(time.Hour),
		},
	}
	activeAcc, err := mgr.mergeAndSaveAccount(activeTok, "active-test@example.com", "Active Test")
	if err != nil {
		t.Fatalf("create active account failed: %v", err)
	}

	updatedTok := OAuthTokenFile{
		AuthMethod: "consumer",
		Token: OAuthTokenData{
			AccessToken:  "updated-active-token",
			TokenType:    "Bearer",
			RefreshToken: "updated-refresh",
			Expiry:       time.Now().Add(2 * time.Hour),
		},
	}
	if err := mgr.WriteToken(activeAcc.ID, updatedTok); err != nil {
		t.Fatalf("WriteToken on existing account failed: %v", err)
	}

	tokPath := filepath.Join(mgr.accountHomeDir(activeAcc.ID), ".gemini", "antigravity-cli", "antigravity-oauth-token")
	fi, err := os.Stat(tokPath)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("expected token file permission 0600, got %o", perm)
	}

	tokData, err := os.ReadFile(tokPath)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	var readTok OAuthTokenFile
	if err := json.Unmarshal(tokData, &readTok); err != nil {
		t.Fatalf("unmarshal token file: %v", err)
	}
	if readTok.Token.AccessToken != "updated-active-token" {
		t.Errorf("expected access token updated-active-token, got %s", readTok.Token.AccessToken)
	}

	// 补测：对 "system" 调用 WriteToken 返回错误
	if err := mgr.WriteToken("system", tokenFile); err == nil {
		t.Error("expected error when writing token for system account, got nil")
	}
}

// 8. Update/SetRouting 能持久化
func TestUpdateAndSetRoutingPersistence(t *testing.T) {
	dataDir := t.TempDir()
	hostHome := t.TempDir()

	mgr, _ := NewManager(Options{
		DataDir:  dataDir,
		HostHome: hostHome,
	})

	// 1. Update 账号
	newLabel := "主账号"
	newOrder := 5
	newWeight := 80
	onVal := true
	if err := mgr.Update(SystemAccountID, UpdatePatch{
		Label:  &newLabel,
		Order:  &newOrder,
		Weight: &newWeight,
		On:     &onVal,
	}); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// 2. SetRouting
	rCfg := multiaccount.Config{
		Strategy: "weight",
		Sink:     true,
		Affinity: multiaccount.AffinityStrict,
	}
	if err := mgr.SetRouting(rCfg); err != nil {
		t.Fatalf("SetRouting failed: %v", err)
	}

	// 3. 重新加载新 Manager 验证持久化
	mgr2, err := NewManager(Options{
		DataDir:  dataDir,
		HostHome: hostHome,
	})
	if err != nil {
		t.Fatalf("reload Manager failed: %v", err)
	}

	sysAcc, _ := mgr2.Get(SystemAccountID)
	if sysAcc.Label != newLabel || sysAcc.Order != newOrder || sysAcc.Weight != newWeight {
		t.Errorf("account updates not persisted properly: %+v", sysAcc)
	}

	loadedRouting := mgr2.Routing()
	if loadedRouting.Strategy != "weight" || !loadedRouting.Sink || loadedRouting.Affinity != multiaccount.AffinityStrict {
		t.Errorf("routing config not persisted properly: %+v", loadedRouting)
	}
}

// 9. Route 分别在 session/turn/strict 三种亲和下的行为、exclude 生效、disabled/on=false 被排除、system 和隔离账号返回的 env/args 正确
func TestRoutingBehavior(t *testing.T) {
	dataDir := t.TempDir()
	hostHome := t.TempDir()
	var emailSeq int32
	srv, endpoints := setupMockOAuthServer(t, &emailSeq)
	defer srv.Close()

	mgr, _ := NewManager(Options{
		DataDir:   dataDir,
		HostHome:  hostHome,
		Endpoints: endpoints,
		Opener:    func(u string) error { return nil },
	})

	ctx := context.Background()
	sess, _ := mgr.LoginStart(ctx)
	_ = sess.Submit("code-route-1")
	isoAcc, err := sess.Wait(ctx)
	if err != nil {
		t.Fatalf("login iso account failed: %v", err)
	}

	// 9.1 验证 system 与隔离账号的 env / args 返回值
	// 针对 system
	accID, env, args, err := mgr.buildRuntimeEnv(SystemAccountID)
	if err != nil || accID != SystemAccountID {
		t.Fatalf("buildRuntimeEnv for system failed: %v", err)
	}
	if len(env) != 1 || env[0] != "AGY_CLI_DISABLE_AUTO_UPDATE=true" || args != nil {
		t.Errorf("system runtime env/args mismatch: env=%v, args=%v", env, args)
	}

	// 针对隔离账号
	accID, env, args, err = mgr.buildRuntimeEnv(isoAcc.ID)
	if err != nil || accID != isoAcc.ID {
		t.Fatalf("buildRuntimeEnv for iso failed: %v", err)
	}
	hasHome := false
	hasForceFile := false
	for _, e := range env {
		if strings.HasPrefix(e, "HOME=") {
			hasHome = true
		}
		if e == "GEMINI_FORCE_FILE_STORAGE=true" {
			hasForceFile = true
		}
	}
	if !hasHome || !hasForceFile || len(args) != 1 || !strings.HasPrefix(args[0], "--gemini_dir=") {
		t.Errorf("iso runtime env/args mismatch: env=%v, args=%v", env, args)
	}

	// 9.2 测试 affinity: session
	_ = mgr.SetRouting(multiaccount.Config{
		Strategy: "order",
		Affinity: multiaccount.AffinitySession,
	})
	// 选号
	picked1, _, _, err := mgr.Route("conv-session", "gemini-2.5-pro", nil)
	if err != nil {
		t.Fatalf("Route 1 failed: %v", err)
	}
	// 再次请求同一 conversation，应继续沿用
	picked2, _, _, err := mgr.Route("conv-session", "gemini-2.5-pro", nil)
	if err != nil {
		t.Fatalf("Route 2 failed: %v", err)
	}
	if picked1 != picked2 {
		t.Errorf("session affinity failed to keep account: %s vs %s", picked1, picked2)
	}

	// 9.3 exclude 生效：exclude 掉 picked1，必须换号
	picked3, _, _, err := mgr.Route("conv-session", "gemini-2.5-pro", []string{picked1})
	if err != nil {
		t.Fatalf("Route with exclude failed: %v", err)
	}
	if picked3 == picked1 {
		t.Errorf("exclude failed to avoid excluded account %s", picked1)
	}

	// 9.4 affinity: strict 模式下即使在冷却中也不换
	_ = mgr.SetRouting(multiaccount.Config{
		Strategy: "order",
		Affinity: multiaccount.AffinityStrict,
	})
	mgr.Bind("conv-strict", picked1)
	// 标为限流冷却中
	limited := mgr.MarkLimited("conv-strict", picked1, time.Now().Add(10*time.Minute))
	if limited {
		t.Errorf("MarkLimited should return false under strict mode")
	}
	pickedStrict, _, _, err := mgr.Route("conv-strict", "gemini-2.5-pro", nil)
	if err != nil {
		t.Fatalf("Route strict failed: %v", err)
	}
	if pickedStrict != picked1 {
		t.Errorf("strict affinity failed to keep account: %s vs %s", pickedStrict, picked1)
	}

	// 9.5 affinity: turn 模式不会被已有绑定钉住，每轮重新选号
	// （此时 system 仍处于冷却中，可用账号只有 isoAcc，重新选号必然绕过绑定的 system）
	_ = mgr.SetRouting(multiaccount.Config{
		Strategy: "order",
		Affinity: multiaccount.AffinityTurn,
	})
	mgr.Bind("conv-turn", SystemAccountID)
	pickedTurn, _, _, err := mgr.Route("conv-turn", "gemini-2.5-pro", nil)
	if err != nil {
		t.Fatalf("Route turn failed: %v", err)
	}
	if pickedTurn == SystemAccountID {
		t.Errorf("turn affinity must re-route instead of reusing the bound account %s", SystemAccountID)
	}

	// 9.6 disabled 或 on=false 被排除
	mgr, err = NewManager(Options{
		DataDir:   dataDir,
		HostHome:  hostHome,
		Endpoints: endpoints,
		Opener:    func(u string) error { return nil },
	})
	if err != nil {
		t.Fatalf("re-create manager failed: %v", err)
	}
	_ = mgr.SetRouting(multiaccount.Config{
		Strategy: "order",
		Affinity: multiaccount.AffinitySession,
	})
	onFalse := false
	_ = mgr.Update(isoAcc.ID, UpdatePatch{On: &onFalse})
	pickedOnlySys, _, _, err := mgr.Route("conv-new", "gemini-2.5-pro", nil)
	if err != nil {
		t.Fatalf("Route after on=false failed: %v", err)
	}
	if pickedOnlySys != SystemAccountID {
		t.Errorf("expected SystemAccountID when isoAcc on=false, got %s", pickedOnlySys)
	}

	// 唯一可用账号在冷却中
	mgr.MarkLimited("", SystemAccountID, time.Now().Add(10*time.Minute))
	_, _, _, err = mgr.Route("conv-new2", "gemini-2.5-pro", nil)
	if !errors.Is(err, multiaccount.ErrNoAccount) {
		t.Errorf("expected ErrNoAccount when only available account is cooling down, got: %v", err)
	}

	// session 模式下 Route("", ...) 不写入空会话绑定
	onTrue := true
	_ = mgr.Update(isoAcc.ID, UpdatePatch{On: &onTrue})
	pickedEmpty, _, _, err := mgr.Route("", "gemini-2.5-pro", nil)
	if err != nil {
		t.Fatalf("Route with empty conversation failed: %v", err)
	}
	for _, id := range []string{pickedEmpty, SystemAccountID, isoAcc.ID} {
		for _, conv := range mgr.BoundConversations(id) {
			if conv == "" {
				t.Errorf("BoundConversations(%s) contains empty conversation ID", id)
			}
		}
	}

	// 重新 NewManager 加载出的绑定里也没有空键
	mgrReloaded, err := NewManager(Options{
		DataDir:   dataDir,
		HostHome:  hostHome,
		Endpoints: endpoints,
		Opener:    func(u string) error { return nil },
	})
	if err != nil {
		t.Fatalf("re-create manager failed: %v", err)
	}
	for _, id := range []string{pickedEmpty, SystemAccountID, isoAcc.ID} {
		for _, conv := range mgrReloaded.BoundConversations(id) {
			if conv == "" {
				t.Errorf("reloaded BoundConversations(%s) contains empty conversation ID", id)
			}
		}
	}

	// 验证 Bind 空字符串直接忽略
	mgr.Bind("", isoAcc.ID)
	mgr.Bind("conv-invalid", "")
	for _, id := range []string{SystemAccountID, isoAcc.ID} {
		for _, conv := range mgr.BoundConversations(id) {
			if conv == "" {
				t.Errorf("BoundConversations(%s) contains empty conversation ID after Bind", id)
			}
		}
	}
}

// 10. withTokenLock 用多个 goroutine 并发递增文件中的计数，结果准确
func TestWithTokenLockConcurrentIncrement(t *testing.T) {
	dataDir := t.TempDir()
	hostHome := t.TempDir()

	mgr, _ := NewManager(Options{
		DataDir:  dataDir,
		HostHome: hostHome,
	})

	testID := "test-concurrent-account"
	if err := mgr.setupAccountLayout(testID); err != nil {
		t.Fatalf("setup layout failed: %v", err)
	}

	// 初始化 token 计数文件
	initErr := mgr.withTokenLock(testID, func(tokenPath string) error {
		return os.WriteFile(tokenPath, []byte("0"), 0600)
	})
	if initErr != nil {
		t.Fatalf("init count file failed: %v", initErr)
	}

	const concurrency = 20
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			err := mgr.withTokenLock(testID, func(tokenPath string) error {
				data, err := os.ReadFile(tokenPath)
				if err != nil {
					return err
				}
				val, err := strconv.Atoi(string(data))
				if err != nil {
					return err
				}
				val++
				return os.WriteFile(tokenPath, []byte(strconv.Itoa(val)), 0600)
			})
			if err != nil {
				t.Errorf("concurrent increment error: %v", err)
			}
		}()
	}

	wg.Wait()

	// 验证最终值等于并发次数
	var finalVal int
	_ = mgr.withTokenLock(testID, func(tokenPath string) error {
		data, err := os.ReadFile(tokenPath)
		if err != nil {
			return err
		}
		val, err := strconv.Atoi(string(data))
		finalVal = val
		return err
	})

	if finalVal != concurrency {
		t.Errorf("expected final count %d, got %d", concurrency, finalVal)
	}
}

// 11. 备用终端登录：TerminalLogin 返回隔离环境；AdoptTerminalLogin 采纳凭证后可重复调用，
// 不得删除已生效账号自己的隔离目录；误传 system 等已占用的 id 必须被拒绝。
func TestTerminalLoginAdopt(t *testing.T) {
	dataDir := t.TempDir()
	hostHome := t.TempDir()
	var emailSeq int32
	srv, endpoints := setupMockOAuthServer(t, &emailSeq)
	defer srv.Close()

	mgr, err := NewManager(Options{
		DataDir:   dataDir,
		HostHome:  hostHome,
		Endpoints: endpoints,
		Opener:    func(u string) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	ctx := context.Background()

	// 11.1 TerminalLogin 返回隔离环境与 --gemini_dir
	id, env, args, err := mgr.TerminalLogin()
	if err != nil {
		t.Fatalf("TerminalLogin failed: %v", err)
	}
	homeDir := mgr.accountHomeDir(id)
	hasHome, hasForce, hasAutoUpdate := false, false, false
	for _, e := range env {
		switch {
		case e == "HOME="+homeDir:
			hasHome = true
		case e == "GEMINI_FORCE_FILE_STORAGE=true":
			hasForce = true
		case e == "AGY_CLI_DISABLE_AUTO_UPDATE=true":
			hasAutoUpdate = true
		}
	}
	if !hasHome || !hasForce || !hasAutoUpdate {
		t.Errorf("terminal login env mismatch: %v", env)
	}
	if len(args) != 1 || args[0] != "--gemini_dir="+filepath.Join(homeDir, ".gemini") {
		t.Errorf("terminal login args mismatch: %v", args)
	}

	// 11.2 模拟 agy 在隔离目录写入终端登录凭证后采纳
	tokenPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	tokenJSON := `{"auth_method":"consumer","token":{"access_token":"mock-access-token-code-term-1","token_type":"Bearer","refresh_token":"term-refresh","expiry":"2026-10-17T18:00:00Z"}}`
	if err := os.WriteFile(tokenPath, []byte(tokenJSON), 0600); err != nil {
		t.Fatalf("write terminal token: %v", err)
	}

	acc, err := mgr.AdoptTerminalLogin(ctx, id)
	if err != nil {
		t.Fatalf("AdoptTerminalLogin failed: %v", err)
	}
	if acc.ID != id || acc.Email != "user-term-1@example.com" {
		t.Fatalf("unexpected adopted account: %+v", acc)
	}
	if _, err := os.Stat(homeDir); err != nil {
		t.Fatalf("account dir should exist after adopt: %v", err)
	}

	// 11.3 重复采纳同一临时 id：命中 email 分支，但不得删除该账号自己的目录
	accAgain, err := mgr.AdoptTerminalLogin(ctx, id)
	if err != nil {
		t.Fatalf("second AdoptTerminalLogin failed: %v", err)
	}
	if accAgain.ID != acc.ID {
		t.Errorf("expected same account on repeat adopt, got %s vs %s", accAgain.ID, acc.ID)
	}
	if _, err := os.Stat(tokenPath); err != nil {
		t.Errorf("token file must survive repeat adopt: %v", err)
	}
	if _, err := os.Stat(homeDir); err != nil {
		t.Errorf("account dir must survive repeat adopt: %v", err)
	}
	count := 0
	for _, a := range mgr.List() {
		if a.ID == id {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 entry for adopted account, got %d", count)
	}

	// 11.4 误传已占用的 id（system）必须被拒绝，不得向索引追加重复 id
	sysTokenPath := filepath.Join(hostHome, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	if err := os.MkdirAll(filepath.Dir(sysTokenPath), 0700); err != nil {
		t.Fatalf("mkdir system token dir: %v", err)
	}
	sysToken := `{"auth_method":"consumer","token":{"access_token":"mock-access-token-code-sys-1","token_type":"Bearer","refresh_token":"sys-refresh","expiry":"2026-10-17T18:00:00Z"}}`
	if err := os.WriteFile(sysTokenPath, []byte(sysToken), 0600); err != nil {
		t.Fatalf("write system token: %v", err)
	}
	if _, err := mgr.AdoptTerminalLogin(ctx, SystemAccountID); err == nil {
		t.Errorf("expected error when adopting with occupied id %q", SystemAccountID)
	}
	countSystem := 0
	for _, a := range mgr.List() {
		if a.ID == SystemAccountID {
			countSystem++
		}
	}
	if countSystem != 1 {
		t.Errorf("expected exactly 1 system account, got %d", countSystem)
	}
}

// TestRoutePrefersDefaultAccount 验证新会话优先用默认账号：DefaultAccountID 之前只影响展示，
// 新会话仍按调度策略选号，界面上的「设为默认」不生效。
func TestRoutePrefersDefaultAccount(t *testing.T) {
	mgr, err := NewManager(Options{DataDir: t.TempDir(), HostHome: t.TempDir()})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	// 追加一个 Order 更大的隔离账号并设为默认：order 策略否则一定先选 Order=0 的系统账号。
	mgr.mu.Lock()
	mgr.index.Accounts = append(mgr.index.Accounts, Account{
		ID:     "acc-1",
		Email:  "acc1@example.com",
		Status: StatusActive,
		On:     true,
		Order:  5,
		Weight: 100,
		Quota:  make(map[string]QuotaGroup),
	})
	mgr.mu.Unlock()
	if err := mgr.SetDefault("acc-1"); err != nil {
		t.Fatalf("SetDefault failed: %v", err)
	}
	_ = mgr.SetRouting(multiaccount.Config{Strategy: multiaccount.StrategyOrder, Affinity: multiaccount.AffinitySession})

	picked, _, _, err := mgr.Route("conv-default", "gemini-2.5-pro", nil)
	if err != nil {
		t.Fatalf("Route failed: %v", err)
	}
	if picked != "acc-1" {
		t.Errorf("unbound session should use default account acc-1, got %s", picked)
	}
	// 选中的账号应写进会话绑定，界面「已绑定会话」才与实际一致。
	if bound, ok := mgr.router.Bound("conv-default"); !ok || bound != "acc-1" {
		t.Errorf("conversation should be bound to acc-1, got %q (ok=%v)", bound, ok)
	}

	// 已绑定到其他账号的会话不受默认账号影响。
	mgr.Bind("conv-bound", SystemAccountID)
	picked, _, _, err = mgr.Route("conv-bound", "gemini-2.5-pro", nil)
	if err != nil {
		t.Fatalf("Route bound conversation failed: %v", err)
	}
	if picked != SystemAccountID {
		t.Errorf("bound conversation should keep %s, got %s", SystemAccountID, picked)
	}

	// 默认账号被排除（本轮限流换号）时回退到调度策略。
	picked, _, _, err = mgr.Route("conv-excluded", "gemini-2.5-pro", []string{"acc-1"})
	if err != nil {
		t.Fatalf("Route with exclude failed: %v", err)
	}
	if picked != SystemAccountID {
		t.Errorf("excluded default account should fall back to strategy, got %s", picked)
	}
}

// TestBoundAccountIDOnlyReportsBinding 验证 BoundAccountID 只查绑定、不回退默认账号：
// 额度弹窗靠它把「在用」标在当前会话真正用的账号上。
func TestBoundAccountIDOnlyReportsBinding(t *testing.T) {
	mgr, err := NewManager(Options{DataDir: t.TempDir(), HostHome: t.TempDir()})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	mgr.Bind("conv-bound", "acc-1")

	if id, ok := mgr.BoundAccountID("conv-bound"); !ok || id != "acc-1" {
		t.Errorf("bound conversation should report acc-1, got %q (ok=%v)", id, ok)
	}
	// 新会话尚未绑定账号：不能拿默认账号冒充，要由调用方按路由目标兜底。
	if id, ok := mgr.BoundAccountID("conv-new"); ok || id != "" {
		t.Errorf("unbound conversation should report nothing, got %q (ok=%v)", id, ok)
	}
	if id, ok := mgr.BoundAccountID(""); ok || id != "" {
		t.Errorf("empty conversation id should report nothing, got %q (ok=%v)", id, ok)
	}
}

// TestQuotaViewAppliesRoutingOverride 验证额度视图报告的是应用路由覆盖后的有效启用状态，
// 并把新会话目标账号切到默认账号停用后的路由回退结果。
func TestQuotaViewAppliesRoutingOverride(t *testing.T) {
	mgr, err := NewManager(Options{DataDir: t.TempDir(), HostHome: t.TempDir()})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	mgr.mu.Lock()
	mgr.index.Accounts = append(mgr.index.Accounts, Account{
		ID:     "acc-1",
		Email:  "acc1@example.com",
		Status: StatusActive,
		On:     true,
		Order:  5,
		Weight: 100,
		Quota:  make(map[string]QuotaGroup),
	})
	mgr.mu.Unlock()
	_ = mgr.SetRouting(multiaccount.Config{Strategy: multiaccount.StrategyOrder, Affinity: multiaccount.AffinitySession})

	view := mgr.QuotaView()
	if !view.On[SystemAccountID] || !view.On["acc-1"] {
		t.Fatalf("两个账号都应报告为启用: %+v", view.On)
	}
	if view.Target != SystemAccountID {
		t.Fatalf("默认账号应为新会话目标，得到 %q", view.Target)
	}

	// 只改路由覆盖关掉系统账号（索引里的 On 不动）。
	off := false
	mgr.mu.Lock()
	mgr.routing.Accounts[SystemAccountID] = AccountRoutingOverride{On: &off}
	mgr.mu.Unlock()

	view = mgr.QuotaView()
	if view.On[SystemAccountID] {
		t.Errorf("路由覆盖停用后系统账号不应报告为启用: %+v", view.On)
	}
	if acc, ok := mgr.Get(SystemAccountID); !ok || !acc.On {
		t.Errorf("路由覆盖不应改动索引里的 On: %+v", acc)
	}
	if view.Target != "acc-1" {
		t.Errorf("默认账号被停用后应回退到 acc-1，得到 %q", view.Target)
	}
	if !view.On["acc-1"] {
		t.Errorf("未覆盖的账号应保持启用: %+v", view.On)
	}
}

// TestQuotaViewDoesNotAdvanceRotate 验证额度弹窗只是预览选号：反复打开不推进 rotate 轮询，
// 弹窗里的目标账号与随后真实请求选到的账号一致，选号序列也不被打乱。
func TestQuotaViewDoesNotAdvanceRotate(t *testing.T) {
	mgr, err := NewManager(Options{DataDir: t.TempDir(), HostHome: t.TempDir()})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	mgr.mu.Lock()
	for _, id := range []string{"acc-1", "acc-2"} {
		mgr.index.Accounts = append(mgr.index.Accounts, Account{
			ID:     id,
			Status: StatusActive,
			On:     true,
			Order:  5,
			Weight: 100,
			Quota:  make(map[string]QuotaGroup),
		})
	}
	// 关掉默认账号优先，逼所有选号都走 rotate 轮询。
	mgr.index.DefaultAccountID = ""
	mgr.mu.Unlock()
	if err := mgr.SetRouting(multiaccount.Config{Strategy: multiaccount.StrategyRotate, Affinity: multiaccount.AffinityTurn}); err != nil {
		t.Fatalf("SetRouting failed: %v", err)
	}

	// rotate 按候选顺序轮转：系统账号 → acc-1 → acc-2。
	want := []string{SystemAccountID, "acc-1", "acc-2"}
	for i, id := range want {
		// 每次真实选号前多开几遍弹窗，弹窗目标应是下一个待选账号且不改变这个预期。
		for j := 0; j < 3; j++ {
			if target := mgr.QuotaView().Target; target != id {
				t.Fatalf("第 %d 次选号前弹窗目标应为 %q，得到 %q", i, id, target)
			}
		}
		picked, _, _, err := mgr.Route(fmt.Sprintf("conv-%d", i), "gemini-2.5-pro", nil)
		if err != nil {
			t.Fatalf("Route failed: %v", err)
		}
		if picked != id {
			t.Fatalf("第 %d 次真实选号应为 %q，得到 %q", i, id, picked)
		}
	}
}

// TestIDTokenEmail 验证从 id_token 的 payload 解析邮箱，缺失或损坏时返回空串。
func TestIDTokenEmail(t *testing.T) {
	encode := func(claims string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(claims))
	}
	token := OAuthTokenFile{IDToken: "header." + encode(`{"email":"Host@Example.com"}`) + ".sig"}
	if got := idTokenEmail(token); got != "Host@Example.com" {
		t.Errorf("idTokenEmail = %q，想要 Host@Example.com", got)
	}
	for _, tok := range []OAuthTokenFile{
		{},
		{IDToken: "not-a-jwt"},
		{IDToken: "header." + encode(`{"sub":"no-email"}`) + ".sig"},
		{IDToken: "header.!!!.sig"},
	} {
		if got := idTokenEmail(tok); got != "" {
			t.Errorf("idTokenEmail(%q) = %q，想要空串", tok.IDToken, got)
		}
	}
}

// TestIsolatedAgyEnvArgs 覆盖隔离账号的 env/args 组装：Windows 追加 USERPROFILE，darwin 不追加；
// 两种平台都必须包含 HOME、ANTIGRAVITY_GEMINI_DIR 与 --gemini_dir。
func TestIsolatedAgyEnvArgs(t *testing.T) {
	oldGoos := goos
	t.Cleanup(func() { goos = oldGoos })

	homeDir := filepath.Join(string(filepath.Separator), "tmp", "agy-isolated", "home")
	geminiDir := filepath.Join(homeDir, ".gemini")

	cases := []struct {
		name        string
		goos        string
		wantProfile bool
	}{
		{name: "windows", goos: "windows", wantProfile: true},
		{name: "darwin", goos: "darwin", wantProfile: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			goos = tc.goos
			env, args := isolatedAgyEnvArgs(homeDir)

			got := make(map[string]bool, len(env))
			for _, e := range env {
				got[e] = true
			}
			for _, want := range []string{
				"HOME=" + homeDir,
				"GEMINI_FORCE_FILE_STORAGE=true",
				"AGY_CLI_DISABLE_AUTO_UPDATE=true",
				"ANTIGRAVITY_GEMINI_DIR=" + geminiDir,
			} {
				if !got[want] {
					t.Fatalf("%s: env 缺少 %q，实际 %v", tc.name, want, env)
				}
			}

			hasProfile := false
			for _, e := range env {
				if strings.HasPrefix(e, "USERPROFILE=") {
					hasProfile = true
				}
			}
			if hasProfile != tc.wantProfile {
				t.Fatalf("%s: 是否包含 USERPROFILE = %v，期望 %v，env=%v", tc.name, hasProfile, tc.wantProfile, env)
			}

			if len(args) != 1 || args[0] != "--gemini_dir="+geminiDir {
				t.Fatalf("%s: args = %v，期望 [--gemini_dir=%s]", tc.name, args, geminiDir)
			}
		})
	}
}

// TestSetupAccountLayoutSkipsMacOnlyLinksOnWindows 验证 goos 为 windows 时不共享 .zshrc 与 Library。
func TestSetupAccountLayoutSkipsMacOnlyLinksOnWindows(t *testing.T) {
	oldGoos := goos
	t.Cleanup(func() { goos = oldGoos })
	goos = "windows"

	mgr, err := NewManager(Options{DataDir: t.TempDir(), HostHome: t.TempDir()})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	const id = "layout-windows"
	if err := mgr.setupAccountLayout(id); err != nil {
		t.Fatalf("setupAccountLayout failed: %v", err)
	}

	home := mgr.accountHomeDir(id)
	for _, rel := range []string{".zshrc", "Library"} {
		if _, err := os.Lstat(filepath.Join(home, rel)); err == nil {
			t.Errorf("windows 上不应创建 %s", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini", "installation_id")); err != nil {
		t.Errorf("installation_id 应已创建: %v", err)
	}
}
