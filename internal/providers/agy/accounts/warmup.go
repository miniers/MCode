package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"time"

	"mcode/internal/providers/agy/quota"
)

// Warmup 统一的预热入口：
// id 为空时批量预热所有有效账号（跳过停用、封禁及冷却中的账号，账号间随机等待 0.8~2.5s）；
// id 非空时针对指定账号预热，跳过条件返回明确原因；网络请求绝不持有 m.mu。
func (m *Manager) Warmup(ctx context.Context, id string) error {
	if id == "" {
		return m.warmupAll(ctx)
	}
	return m.warmupSingle(ctx, id)
}

// warmupAll 批量执行所有有效账号的预热
func (m *Manager) warmupAll(ctx context.Context) error {
	m.mu.Lock()
	now := m.opts.Now()
	var targetIDs []string
	for _, acc := range m.index.Accounts {
		if avail, _ := m.effectiveAccountStateLocked(acc, now); !avail {
			continue
		}
		targetIDs = append(targetIDs, acc.ID)
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

		if err := m.warmupSingle(ctx, accID); err != nil {
			log.Printf("agy: warmup for %s: %v", accID, err)
			errs = append(errs, fmt.Errorf("%s: %w", accID, err))
		}
	}
	return errors.Join(errs...)
}

const maxWarmupAttempts = 3

// warmupSingle 单账号预热调度与门闩串行控制
func (m *Manager) warmupSingle(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	// 初始状态与有效性检查
	m.mu.Lock()
	acc, exists := m.findAccountLocked(id)
	now := m.opts.Now()
	avail, reason := m.effectiveAccountStateLocked(acc, now)
	m.mu.Unlock()
	if !exists {
		return ErrAccountGone
	}
	if !avail {
		return errors.New(reason)
	}

	// 获取账号操作门闩，确保与 RefreshQuota 串行执行
	release, err := m.acquireAccountGate(ctx, id)
	if err != nil {
		return err
	}
	defer release()

	// 拿到门闩后重新读取最新账号快照，网络调用不持 m.mu
	m.mu.Lock()
	acc, exists = m.findAccountLocked(id)
	now = m.opts.Now()
	avail, reason = m.effectiveAccountStateLocked(acc, now)
	m.mu.Unlock()
	if !exists {
		return ErrAccountGone
	}
	if !avail {
		return errors.New(reason)
	}

	accessToken, err := m.ensureAccessToken(ctx, acc)
	if err != nil {
		return err
	}

	qc := &quota.Client{
		HTTPClient: m.opts.HTTPClient,
		Hosts:      m.opts.QuotaHosts,
	}

	// 实时拉取最新配额与候选，绝不使用过时缓存掩盖查询失败
	fetchRes, fetchErr := qc.Fetch(ctx, accessToken, acc.ProjectID)
	if fetchErr != nil {
		return fmt.Errorf("agy: fetch quota for warmup: %w", fetchErr)
	}

	m.mu.Lock()
	idx := m.findAccountIndexLocked(id)
	if idx == -1 {
		m.mu.Unlock()
		return ErrAccountGone
	}
	m.applyQuotaResultLocked(idx, fetchRes)
	if err := m.saveIndexLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()

	// 以最新拉取到的 reset 作为基线；若无旧周期重置时间，则以 "initial" 作为防重标识
	targetResetTime := fetchRes.Groups[quota.GroupGemini+" 7d"].ResetAt
	targetResetAt := ""
	if !targetResetTime.IsZero() {
		targetResetAt = targetResetTime.UTC().Format(time.RFC3339)
	} else {
		targetResetAt = "initial"
	}

	return m.executeWarmup(ctx, id, qc, accessToken, fetchRes.ProjectID, fetchRes.Candidates, targetResetAt, targetResetTime)
}

// executeWarmup 单账号预热核心实现：去重判定、状态前置持久化、模型生成与结果验证
func (m *Manager) executeWarmup(ctx context.Context, id string, qc *quota.Client, accessToken, projectID string, candidates []quota.ModelCandidate, targetResetAt string, targetResetTime time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	now := m.opts.Now()

	m.mu.Lock()
	acc, exists := m.findAccountLocked(id)
	if !exists {
		m.mu.Unlock()
		return ErrAccountGone
	}
	if avail, reason := m.effectiveAccountStateLocked(acc, now); !avail {
		m.mu.Unlock()
		return errors.New(reason)
	}

	// 1. 若当前服务端返回的重置时间在未来（尚未到期）：
	// 当前周期尚未到期，绝不提前生成，避免记录错误 pending_confirmation 阻塞未来周期；
	// 若已确认或服务端自然存在有效未来周期，直接返回成功。
	if !targetResetTime.IsZero() && targetResetTime.After(now) {
		m.mu.Unlock()
		return nil
	}

	// 若已由上一次预热确认（ConfirmedResetAt 等于本次真实返回的未来 reset，或初始过渡后 reset 仍在未来），
	// 当前周期已确认，重复单账号、批量、重启后一律不生成；到达该 reset 后（已到期）则允许正常发起新预热。
	if acc.LastWarmup != nil && acc.LastWarmup.ConfirmedResetAt != "" {
		if crt, err := time.Parse(time.RFC3339, acc.LastWarmup.ConfirmedResetAt); err == nil && crt.After(now) {
			if targetResetAt == acc.LastWarmup.ConfirmedResetAt || targetResetAt == "initial" {
				m.mu.Unlock()
				return nil
			}
		}
	}

	// 2. 周期去重与退避检查：针对当前到期周期 targetResetAt
	if acc.LastWarmup != nil && acc.LastWarmup.TargetResetAt == targetResetAt {
		lw := acc.LastWarmup
		switch lw.Status {
		case "success":
			// 该周期已经成功预热并推进，重复请求直接跳过
			m.mu.Unlock()
			return nil
		case "pending_confirmation", "network_unknown":
			// 明确已执行或结果未决：绝不重新生成，仅补查额度验证推进；网络未决不单凭过了 10 分钟重发
			m.mu.Unlock()
			return m.verifyAndAdvanceReset(ctx, id, qc, accessToken, projectID, targetResetAt, targetResetTime)
		case "failed":
			// 同一目标周期已达到明确失败尝试上限（3次），不再自动生成
			if lw.Attempts >= maxWarmupAttempts {
				m.mu.Unlock()
				return fmt.Errorf("agy: warmup reached max attempts (%d) for cycle: %s", lw.Attempts, lw.Message)
			}
			// 明确失败有限退避 5 分钟，不无限循环
			if t, err := time.Parse(time.RFC3339, lw.AttemptedAt); err == nil && now.Sub(t) < 5*time.Minute {
				m.mu.Unlock()
				return fmt.Errorf("agy: warmup recently failed: %s", lw.Message)
			}
		}
	}

	if len(candidates) == 0 {
		idx := m.findAccountIndexLocked(id)
		if idx != -1 {
			prevAttempts := 0
			if m.index.Accounts[idx].LastWarmup != nil && m.index.Accounts[idx].LastWarmup.TargetResetAt == targetResetAt {
				prevAttempts = m.index.Accounts[idx].LastWarmup.Attempts
			}
			m.index.Accounts[idx].LastWarmup = &WarmupState{
				TargetResetAt: targetResetAt,
				AttemptedAt:   now.UTC().Format(time.RFC3339),
				Status:        "failed",
				Message:       "agy: no available Gemini models for warmup",
				Attempts:      prevAttempts + 1,
			}
			_ = m.saveIndexLocked()
		}
		m.mu.Unlock()
		return errors.New("agy: no available Gemini models for warmup")
	}

	attemptedAt := now.UTC().Format(time.RFC3339)
	lastModel := ""
	prevAttempts := 0
	if acc.LastWarmup != nil {
		lastModel = acc.LastWarmup.Model
		if acc.LastWarmup.TargetResetAt == targetResetAt {
			prevAttempts = acc.LastWarmup.Attempts
		}
	}

	// 真正发生成请求前，先持久化“准备/结果未确认”的记录；保存失败必须终止请求，防退出后重复执行
	idx := m.findAccountIndexLocked(id)
	if idx == -1 {
		m.mu.Unlock()
		return ErrAccountGone
	}
	m.index.Accounts[idx].LastWarmup = &WarmupState{
		TargetResetAt: targetResetAt,
		AttemptedAt:   attemptedAt,
		Model:         lastModel, // 保留上次成功模型
		Status:        "pending_confirmation",
		Message:       "预热请求已发出，等待服务端更新周期",
		Attempts:      prevAttempts,
	}
	if err := m.saveIndexLocked(); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("agy: save pre-warmup state: %w", err)
	}
	m.mu.Unlock()

	// 发起微量生成请求（绝不持有 m.mu）
	usedModel, warmupErr := qc.Warmup(ctx, accessToken, projectID, candidates, lastModel)

	m.mu.Lock()
	idx = m.findAccountIndexLocked(id)
	if idx == -1 {
		m.mu.Unlock()
		return ErrAccountGone
	}

	if warmupErr == nil {
		m.index.Accounts[idx].LastWarmup.Model = usedModel
		m.index.Accounts[idx].LastWarmup.Status = "pending_confirmation"
		m.index.Accounts[idx].LastWarmup.Message = "生成成功，等待服务端更新周期"
		m.index.Accounts[idx].LastWarmup.Attempts = 0
		if err := m.saveIndexLocked(); err != nil {
			m.mu.Unlock()
			return err
		}
		m.mu.Unlock()

		// 生成成功后补查一次额度确认重置时间推进
		return m.verifyAndAdvanceReset(ctx, id, qc, accessToken, projectID, targetResetAt, targetResetTime)
	}

	// 错误处理：区分网络未决（5xx/超时/连接中断）与明确失败
	status := "failed"
	attempts := prevAttempts + 1
	if errors.Is(warmupErr, quota.ErrResultUncertain) {
		status = "network_unknown"
		attempts = prevAttempts // 网络未决不计入明确拒绝尝试上限
	}
	m.index.Accounts[idx].LastWarmup.Status = status
	m.index.Accounts[idx].LastWarmup.Message = warmupErr.Error()
	m.index.Accounts[idx].LastWarmup.Attempts = attempts
	// 注意保留 lastModel，不要在失败时丢失
	saveErr := m.saveIndexLocked()
	m.mu.Unlock()

	return errors.Join(warmupErr, saveErr)
}

// verifyAndAdvanceReset 补查额度并在服务端新重置时间生效后推进状态为 success
func (m *Manager) verifyAndAdvanceReset(ctx context.Context, id string, qc *quota.Client, accessToken, projectID, targetResetAt string, targetResetTime time.Time) error {
	updatedRes, err := qc.Fetch(ctx, accessToken, projectID)
	if err != nil {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	idx := m.findAccountIndexLocked(id)
	if idx == -1 {
		return ErrAccountGone
	}
	m.applyQuotaResultLocked(idx, updatedRes)

	newReset := updatedRes.Groups[quota.GroupGemini+" 7d"].ResetAt
	if !newReset.IsZero() {
		advanced := false
		if targetResetAt == "initial" {
			advanced = true
		} else if !targetResetTime.IsZero() && newReset.After(targetResetTime) {
			advanced = true
		}
		if advanced && m.index.Accounts[idx].LastWarmup != nil {
			m.index.Accounts[idx].LastWarmup.Status = "success"
			m.index.Accounts[idx].LastWarmup.ConfirmedResetAt = newReset.UTC().Format(time.RFC3339)
			m.index.Accounts[idx].LastWarmup.Message = "新周期已生效"
			m.index.Accounts[idx].LastWarmup.Attempts = 0
		}
	}
	return m.saveIndexLocked()
}

// ensureAccessToken 获取有效 access token，并在过期时刷新
func (m *Manager) ensureAccessToken(ctx context.Context, acc Account) (string, error) {
	id := acc.ID
	now := m.opts.Now()

	var tokenFile OAuthTokenFile
	var readErr error
	if id == SystemAccountID && m.opts.SystemTokenReader != nil {
		tokenFile, readErr = m.opts.SystemTokenReader(ctx)
	}
	if tokenFile.Token.AccessToken == "" && tokenFile.Token.RefreshToken == "" {
		m.mu.Lock()
		if _, exists := m.findAccountLocked(id); !exists {
			m.mu.Unlock()
			return "", ErrAccountGone
		}
		readErr = m.withTokenLock(id, func(p string) error {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return json.Unmarshal(data, &tokenFile)
		})
		m.mu.Unlock()
	}
	if readErr != nil {
		return "", fmt.Errorf("read token file: %w", readErr)
	}

	accessToken := tokenFile.Token.AccessToken
	refreshToken := tokenFile.Token.RefreshToken
	expiry := tokenFile.Token.Expiry
	tokenURL := m.opts.Endpoints.withDefaults().TokenURL

	if accessToken == "" || (!expiry.IsZero() && now.After(expiry)) {
		previous := tokenFile
		newAccess, newExp, err := quota.RefreshAccessToken(ctx, m.opts.HTTPClient, tokenURL, OfficialClientID, OfficialClientSecret, refreshToken)
		if err != nil {
			return "", err
		}
		accessToken = newAccess
		tokenFile.Token.AccessToken = newAccess
		tokenFile.Token.Expiry = newExp

		if id != SystemAccountID {
			m.mu.Lock()
			defer m.mu.Unlock()
			if _, exists := m.findAccountLocked(id); !exists {
				return "", ErrAccountGone
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
				if current.Token != previous.Token {
					tokenFile = current
					accessToken = current.Token.AccessToken
					return nil
				}
				return atomicWriteJSON(p, tokenFile)
			}); err != nil {
				return "", err
			}
		}
	}

	return accessToken, nil
}

func (m *Manager) findAccountIndexLocked(id string) int {
	for i, a := range m.index.Accounts {
		if a.ID == id {
			return i
		}
	}
	return -1
}
