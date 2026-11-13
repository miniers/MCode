package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"mcode/internal/providers/agy/quota"
)

var (
	quotaBatchWaitMin     = 800 * time.Millisecond
	quotaBatchWaitMax     = 2500 * time.Millisecond
	quotaDebounceDuration = 15 * time.Second
)

// DefaultAccountID 返回当前索引中的默认账号 ID（线程安全）。
func (m *Manager) DefaultAccountID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.index.DefaultAccountID
}

// StartQuotaPoller 标记启用自动额度拉取，并启动后台每 5 分钟轮询所有有效账号额度。
func (m *Manager) StartQuotaPoller(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	if !m.autoQuota.CompareAndSwap(false, true) {
		close(done)
		return done
	}

	go func() {
		defer close(done)
		// 启动后立即执行一次全量刷新
		_ = m.RefreshQuota(ctx, "")

		lastFull := m.opts.Now()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if m.opts.Now().Sub(lastFull) >= 5*time.Minute {
					_ = m.RefreshQuota(ctx, "")
					lastFull = m.opts.Now()
				} else {
					m.pollActiveQuota(ctx)
				}
			}
		}
	}()
	return done
}

// QuotaUse 跟踪实际运行的子进程，不把已结束但仍绑定的历史会话计为活跃。
func (m *Manager) QuotaUse(id string) func() {
	m.lastRefreshMu.Lock()
	m.activeQuota[id]++
	m.lastRefreshMu.Unlock()
	return func() {
		m.lastRefreshMu.Lock()
		m.activeQuota[id]--
		m.lastRefreshMu.Unlock()
	}
}

func (m *Manager) pollActiveQuota(ctx context.Context) {
	for _, acc := range m.List() {
		if ctx.Err() != nil {
			return
		}
		if acc.Status != StatusActive {
			continue
		}
		home := m.accountHomeDir(acc.ID)
		if acc.ID == SystemAccountID {
			home = m.opts.HostHome
		}
		info, statErr := os.Stat(filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token"))
		m.lastRefreshMu.Lock()
		elapsed := m.opts.Now().Sub(m.lastRefresh[acc.ID])
		changed := statErr == nil && !m.tokenModTime[acc.ID].IsZero() && !info.ModTime().Equal(m.tokenModTime[acc.ID])
		if statErr == nil && m.tokenModTime[acc.ID].IsZero() {
			m.tokenModTime[acc.ID] = info.ModTime()
		}
		refresh := (m.activeQuota[acc.ID] > 0 && elapsed >= time.Minute) || (changed && elapsed >= 30*time.Second)
		m.lastRefreshMu.Unlock()
		if refresh {
			_ = m.RefreshQuota(ctx, acc.ID)
		}
	}
}

// RefreshQuota 刷新单个账号或所有有效账号的额度。
// id 为空时遍历所有有效账号（跳过 disabled 和 forbidden），账号间增加 0.8~2.5s 随机等待；
// 单账号刷新与 Warmup 共享操作门闩严格串行化，具备 15s 防抖控制；持有 m.mu 时绝不执行网络调用。
func (m *Manager) RefreshQuota(ctx context.Context, id string) error {
	if id == "" {
		m.mu.Lock()
		var targetIDs []string
		for _, acc := range m.index.Accounts {
			if acc.Status != StatusDisabled && acc.Status != StatusForbidden {
				targetIDs = append(targetIDs, acc.ID)
			}
		}
		m.mu.Unlock()

		var errs []error
		for i, accID := range targetIDs {
			if i > 0 && quotaBatchWaitMax > 0 {
				waitRange := quotaBatchWaitMax - quotaBatchWaitMin
				var wait time.Duration
				if waitRange > 0 {
					wait = quotaBatchWaitMin + time.Duration(rand.Int63n(int64(waitRange)))
				} else {
					wait = quotaBatchWaitMin
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(wait):
				}
			}
			if err := m.RefreshQuota(ctx, accID); err != nil {
				// 宿主没登录 agy（没有 token 文件，系统凭据也读不到）是常态，不必每轮记一条错误。
				if accID != SystemAccountID || !errors.Is(err, fs.ErrNotExist) {
					log.Printf("agy: refresh quota for %s: %v", accID, err)
				}
				errs = append(errs, fmt.Errorf("%s: %w", accID, err))
			}
		}
		return errors.Join(errs...)
	}

	// 删除检查优先于防抖，避免向已删除账号返回成功。
	m.mu.Lock()
	_, exists := m.findAccountLocked(id)
	m.mu.Unlock()
	if !exists {
		return ErrAccountGone
	}

	// 获取操作门闩（串行化同账号的 RefreshQuota 与 Warmup）
	release, err := m.acquireAccountGate(ctx, id)
	if err != nil {
		return err
	}
	defer release()

	// 重新读取账号最新快照
	m.mu.Lock()
	acc, exists := m.findAccountLocked(id)
	m.mu.Unlock()
	if !exists {
		return ErrAccountGone
	}

	// 防抖控制
	m.lastRefreshMu.Lock()
	now := m.opts.Now()
	if last, ok := m.lastRefresh[id]; ok && now.Sub(last) < quotaDebounceDuration {
		lastErr := m.refreshErr[id]
		m.lastRefreshMu.Unlock()
		return lastErr
	}
	m.lastRefresh[id] = now
	m.lastRefreshMu.Unlock()

	err = m.refreshQuota(ctx, acc)
	m.lastRefreshMu.Lock()
	m.refreshErr[id] = err
	m.lastRefreshMu.Unlock()
	return err
}

func (m *Manager) refreshQuota(ctx context.Context, acc Account) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	id := acc.ID
	now := m.opts.Now()

	// 3. 读取 token 文件（走 withTokenLock）
	var tokenFile OAuthTokenFile
	var readErr error
	if id == SystemAccountID && m.opts.SystemTokenReader != nil {
		tokenFile, readErr = m.opts.SystemTokenReader(ctx)
	}
	if tokenFile.Token.AccessToken == "" && tokenFile.Token.RefreshToken == "" {
		m.mu.Lock()
		if _, exists := m.findAccountLocked(id); !exists {
			m.mu.Unlock()
			return ErrAccountGone
		}
		readErr = m.withTokenLock(id, func(p string) error {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if info, err := os.Stat(p); err == nil {
				m.lastRefreshMu.Lock()
				m.tokenModTime[id] = info.ModTime()
				m.lastRefreshMu.Unlock()
			}
			return json.Unmarshal(data, &tokenFile)
		})
		m.mu.Unlock()
	}
	if readErr != nil {
		return m.handleRefreshFailure(id, fmt.Errorf("read token file: %w", readErr), false)
	}

	qc := &quota.Client{
		HTTPClient: m.opts.HTTPClient,
		Hosts:      m.opts.QuotaHosts,
	}

	accessToken := tokenFile.Token.AccessToken
	refreshToken := tokenFile.Token.RefreshToken
	expiry := tokenFile.Token.Expiry
	tokenURL := m.opts.Endpoints.withDefaults().TokenURL

	refreshTokenFn := func() error {
		newAccess, newExp, err := quota.RefreshAccessToken(ctx, m.opts.HTTPClient, tokenURL, OfficialClientID, OfficialClientSecret, refreshToken)
		if err != nil {
			return err
		}
		previous := tokenFile
		accessToken = newAccess
		expiry = newExp
		tokenFile.Token.AccessToken = newAccess
		tokenFile.Token.Expiry = newExp

		// 隔离账号写回私有 token 文件；系统账号绝不写回 ~/.gemini
		if id != SystemAccountID {
			m.mu.Lock()
			defer m.mu.Unlock()
			if _, exists := m.findAccountLocked(id); !exists {
				return ErrAccountGone
			}
			if err := m.withTokenLock(id, func(p string) error {
				var current OAuthTokenFile
				data, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				if err := json.Unmarshal(data, &current); err != nil {
					return err
				}
				// CLI 或重新登录已经更新凭证时，不得用旧网络响应覆盖它。
				if current.Token != previous.Token {
					tokenFile = current
					accessToken = current.Token.AccessToken
					refreshToken = current.Token.RefreshToken
					return nil
				}
				return atomicWriteJSON(p, tokenFile)
			}); err != nil {
				return err
			}
		}
		return nil
	}

	// 若 access token 过期或为空，先刷新
	if accessToken == "" || (!expiry.IsZero() && now.After(expiry)) {
		if err := refreshTokenFn(); err != nil {
			return m.handleRefreshFailure(id, err, true)
		}
	}

	// 4. 执行配额拉取
	res, err := qc.Fetch(ctx, accessToken, acc.ProjectID)
	if err != nil && errors.Is(err, quota.ErrUnauthorized) {
		// 遇到 401 Unauthorized 强制刷新 token 重试一次
		if refErr := refreshTokenFn(); refErr != nil {
			return m.handleRefreshFailure(id, refErr, true)
		}
		res, err = qc.Fetch(ctx, accessToken, acc.ProjectID)
	}

	if err != nil {
		return m.handleRefreshFailure(id, err, false)
	}

	// 5. 成功：在 m.mu 内先确认账号仍在索引中（防幽灵回写），更新并原子落盘
	m.mu.Lock()
	idx := m.findAccountIndexLocked(id)
	if idx == -1 {
		m.mu.Unlock()
		return ErrAccountGone
	}

	m.applyQuotaResultLocked(idx, res)

	// 普通刷新根据服务端真实 reset 推进把 pending_confirmation / network_unknown 记录推进为确认成功
	if m.index.Accounts[idx].LastWarmup != nil {
		lw := m.index.Accounts[idx].LastWarmup
		if lw.Status == "pending_confirmation" || lw.Status == "network_unknown" {
			newReset := res.Groups[quota.GroupGemini+" 7d"].ResetAt
			if !newReset.IsZero() {
				advanced := false
				if lw.TargetResetAt == "initial" {
					advanced = true
				} else if t, err := time.Parse(time.RFC3339, lw.TargetResetAt); err == nil && newReset.After(t) {
					advanced = true
				}
				if advanced {
					lw.Status = "success"
					lw.ConfirmedResetAt = newReset.UTC().Format(time.RFC3339)
					lw.Message = "新周期已生效"
					lw.Attempts = 0
				}
			}
		}
	}

	if err := m.saveIndexLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()

	// 6. 自动预热：仅在开启自动预热且 Gemini 7d 周期已到期但服务端尚未更新时触发。
	// 复用本次 Fetch 已有 accessToken、res.ProjectID、res.Candidates，跳过 disabled、forbidden、On=false、路由覆盖停用及冷却中账号。
	if m.AutoWarmup() {
		group := quota.GroupGemini
		current, hasCurrent := res.Groups[group+" 7d"]

		// 必须服务端明确返回真实可解析的 Gemini 7d 周 reset；缺失时不猜自动周期
		if hasCurrent && !current.ResetAt.IsZero() && !current.ResetAt.After(now) {
			shouldWarmup := true
			// 若存在旧快照且旧 reset 尚未到期，则不应触发
			if old, ok := acc.Quota[group+" 7d"]; ok {
				if oldReset, err := time.Parse(time.RFC3339, old.ResetAt); err == nil && oldReset.After(now) {
					shouldWarmup = false
				}
			}

			if shouldWarmup {
				m.mu.Lock()
				curAcc, curExists := m.findAccountLocked(id)
				avail, _ := m.effectiveAccountStateLocked(curAcc, now)
				m.mu.Unlock()

				if curExists && avail {
					targetResetTime := current.ResetAt
					targetResetAt := targetResetTime.UTC().Format(time.RFC3339)
					// 执行预热（预热失败不影响额度拉取结果，也不将账号标为 forbidden）
					_ = m.executeWarmup(ctx, id, qc, accessToken, res.ProjectID, res.Candidates, targetResetAt, targetResetTime)
				}
			}
		}
	}

	return nil
}

// handleRefreshFailure 处理刷新失败的分支状态持久化与防幽灵校验。
func (m *Manager) handleRefreshFailure(id string, err error, isTokenRefreshFailure bool) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := -1
	for i, a := range m.index.Accounts {
		if a.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return ErrAccountGone
	}

	m.index.Accounts[idx].QuotaStale = true

	if isTokenRefreshFailure {
		m.index.Accounts[idx].Status = StatusInvalid
		m.index.Accounts[idx].StatusReason = err.Error()
	} else if errors.Is(err, quota.ErrForbidden) {
		m.index.Accounts[idx].Status = StatusForbidden
		reason := err.Error()
		var fErr *quota.ForbiddenError
		if errors.As(err, &fErr) && fErr.Reason != "" {
			reason = fErr.Reason
		}
		m.index.Accounts[idx].StatusReason = reason
	}

	return errors.Join(err, m.saveIndexLocked())
}

// applyQuotaResultLocked 更新账号额度，调用方必须持有 m.mu。
func (m *Manager) applyQuotaResultLocked(idx int, res quota.Result) {
	if res.ProjectID != "" {
		m.index.Accounts[idx].ProjectID = res.ProjectID
	}
	if res.Tier != "" {
		m.index.Accounts[idx].Tier = res.Tier
	}

	quotaMap := make(map[string]QuotaGroup, len(res.Groups))
	for k, v := range res.Groups {
		resetStr := ""
		if !v.ResetAt.IsZero() {
			resetStr = v.ResetAt.UTC().Format(time.RFC3339)
		}
		quotaMap[k] = QuotaGroup{
			UsedPercent: v.UsedPercent,
			ResetAt:     resetStr,
		}
	}
	m.index.Accounts[idx].Quota = quotaMap
	m.index.Accounts[idx].QuotaReadAt = m.opts.Now().UTC().Format(time.RFC3339)
	m.index.Accounts[idx].QuotaStale = false

	if m.index.Accounts[idx].Status == StatusInvalid || m.index.Accounts[idx].Status == StatusForbidden {
		m.index.Accounts[idx].Status = StatusActive
		m.index.Accounts[idx].StatusReason = ""
	}
}
