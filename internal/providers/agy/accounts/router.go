package accounts

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"mcode/internal/providers/agy/quota"
	"mcode/internal/providers/multiaccount"
)

// Route 实现 agy.Router 接口中的 Route 方法。
// 将账号元数据、路由覆盖配置与额度快照转换为多账号公共包的账号视图，
// 由 multiaccount.Router 完成亲和性判断、调度选号与会话绑定。
func (m *Manager) Route(conversationID, model string, exclude []string) (accountID string, env []string, args []string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.opts.Now()
	accounts, _ := m.accountStatesLocked(quota.ModelGroup(model), now)

	chosenID, err := m.pickAccountLocked(conversationID, accounts, exclude, now)
	if err != nil {
		if errors.Is(err, multiaccount.ErrNoAccount) {
			// 保持 agy 对外错误文案不变。
			return "", nil, nil, fmt.Errorf("agy: %w", err)
		}
		return "", nil, nil, err
	}

	return m.buildRuntimeEnv(chosenID)
}

// QuotaView 是额度弹窗需要的账号视图。
// On 是每个账号应用路由覆盖（agy-routing.json）后的有效启用状态；
// Target 是新会话当前会被路由到的账号 ID，没有可用账号时为空串。
type QuotaView struct {
	On     map[string]bool
	Target string
}

// QuotaView 返回额度弹窗需要的账号有效启用状态与新会话目标账号。
// 默认账号优先，不可用时按调度策略回退；整条路径只预览不写状态：不写会话绑定、
// 不推进轮询计数与加权轮询权重，否则打开额度弹窗会扰动真实请求的选号。
func (m *Manager) QuotaView() QuotaView {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.opts.Now()
	// 界面没有模型上下文，按默认分组（gemini）估算目标账号的可调度性。
	states, on := m.accountStatesLocked(quota.ModelGroup(""), now)
	view := QuotaView{On: on}
	if id := m.defaultAccountLocked("", states, nil, now); id != "" {
		view.Target = id
	} else if id, err := m.router.Peek(m.routing.Config, "", states, nil, now); err == nil {
		view.Target = id
	}
	return view
}

// accountStatesLocked 把账号元数据、路由覆盖与额度快照转换成路由视图，
// 同时返回每个账号应用覆盖后的有效启用状态（供额度弹窗区分在用与已停止）。
func (m *Manager) accountStatesLocked(group string, now time.Time) ([]multiaccount.AccountState, map[string]bool) {
	states := make([]multiaccount.AccountState, 0, len(m.index.Accounts))
	on := make(map[string]bool, len(m.index.Accounts))
	for _, acc := range m.index.Accounts {
		override := m.routing.Accounts[acc.ID]
		enabled := acc.On
		if override.On != nil {
			enabled = *override.On
		}
		order := acc.Order
		if override.Order != nil {
			order = *override.Order
		}
		weight := acc.Weight
		if override.Weight != nil {
			weight = *override.Weight
		}

		on[acc.ID] = enabled
		usedPercent, resetAt := evaluateCandidateQuota(acc, group, now)

		states = append(states, multiaccount.AccountState{
			ID:           acc.ID,
			Enabled:      acc.Status == StatusActive && enabled,
			Order:        order,
			Weight:       weight,
			UsedPercent:  usedPercent,
			ResetAt:      resetAt,
			RecentTokens: 0,
		})
	}
	return states, on
}

// pickAccountLocked 选号：会话还没有绑定账号时优先用「默认账号」。
// UI 的「设为默认」表达的就是新会话用哪个账号，之前 DefaultAccountID 只用于展示与未绑定
// 会话的兜底，新会话实际上仍按调度策略选号。默认账号不可用（已停用、被排除、冷却中或额度
// 已用尽）时仍交给调度策略，避免把一个跑不了的号钉到新会话上。
func (m *Manager) pickAccountLocked(conversationID string, accounts []multiaccount.AccountState, exclude []string, now time.Time) (string, error) {
	if defaultID := m.defaultAccountLocked(conversationID, accounts, exclude, now); defaultID != "" {
		// 与 Pick 一致：把本轮实际使用的账号记到会话绑定上，界面「已绑定会话」才与实际一致。
		_ = m.router.Bind(conversationID, defaultID)
		return defaultID, nil
	}
	return m.router.Pick(m.routing.Config, conversationID, accounts, exclude, now)
}

// defaultAccountLocked 返回可以直用的默认账号 ID，不适用时返回空串。
func (m *Manager) defaultAccountLocked(conversationID string, accounts []multiaccount.AccountState, exclude []string, now time.Time) string {
	defaultID := m.index.DefaultAccountID
	if defaultID == "" || m.routing.Affinity == multiaccount.AffinityTurn {
		// turn 亲和性就是每轮重新选号，此时不强制默认账号。
		return ""
	}
	if _, bound := m.router.Bound(conversationID); bound {
		return ""
	}
	for _, acc := range accounts {
		if acc.ID != defaultID {
			continue
		}
		if !acc.Enabled || acc.UsedPercent >= 100 || slices.Contains(exclude, defaultID) || m.router.Cooling(defaultID, now) {
			return ""
		}
		return defaultID
	}
	return ""
}

// Bind 实现 agy.Router 接口中的 Bind 方法，将会话与指定账号绑定并持久化。
func (m *Manager) Bind(conversationID, accountID string) {
	_ = m.router.Bind(conversationID, accountID)
}

// MarkLimited 实现 agy.Router 接口中的 MarkLimited 方法。
// 将限流账号置于冷却队列；当前为 strict 严格模式时返回 false，否则返回 true 表示允许平滑换号。
func (m *Manager) MarkLimited(conversationID, accountID string, resetAt time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.router.MarkLimited(accountID, resetAt, m.opts.Now())

	if m.routing.Affinity == multiaccount.AffinityStrict {
		return false
	}
	return true
}

// Pin 强制将会话锁定至指定账号（§7.1），为后续 UI 选择具体账号交互预留入口。
func (m *Manager) Pin(conversationID, accountID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.findAccountLocked(accountID); !exists {
		return ErrAccountNotFound
	}

	return m.router.Bind(conversationID, accountID)
}

func (m *Manager) findAccountLocked(id string) (Account, bool) {
	for _, acc := range m.index.Accounts {
		if acc.ID == id {
			return acc, true
		}
	}
	return Account{}, false
}

func (m *Manager) buildRuntimeEnv(accountID string) (string, []string, []string, error) {
	if accountID == SystemAccountID {
		return SystemAccountID, []string{"AGY_CLI_DISABLE_AUTO_UPDATE=true"}, nil, nil
	}

	homeDir := m.accountHomeDir(accountID)
	env, args := isolatedAgyEnvArgs(homeDir)
	return accountID, env, args, nil
}

// goos 是平台判断的可注入副本：测试里替换它即可覆盖各平台的 env 与命令选择。
var goos = runtime.GOOS

// isolatedAgyEnvArgs 组装隔离账号启动 agy 所需的环境变量与命令行参数。
// 隔离账号以 homeDir 作为 HOME（Windows 上还要覆盖 USERPROFILE），
// 并用 --gemini_dir / ANTIGRAVITY_GEMINI_DIR 把配置目录固定到 homeDir/.gemini。
func isolatedAgyEnvArgs(homeDir string) (env []string, args []string) {
	env = []string{
		"HOME=" + homeDir,
		// GEMINI_FORCE_FILE_STORAGE 在当前 agy 版本中已无效（二进制里没有这个变量名），
		// 保留它只为兼容可能仍读取该变量的旧版 agy，对现有版本无害。
		"GEMINI_FORCE_FILE_STORAGE=true",
		"AGY_CLI_DISABLE_AUTO_UPDATE=true",
	}
	// Windows 版 agy 用 os.UserHomeDir 读取 %USERPROFILE% 并忽略 HOME，
	// 必须额外覆盖 USERPROFILE 才能把配置真正隔离到 homeDir。
	if goos == "windows" {
		env = append(env, "USERPROFILE="+homeDir)
	}

	geminiDir := filepath.Join(homeDir, ".gemini")
	// ANTIGRAVITY_GEMINI_DIR 与 --gemini_dir 等价，作为参数被丢弃时的冗余保障。
	env = append(env, "ANTIGRAVITY_GEMINI_DIR="+geminiDir)
	args = []string{"--gemini_dir=" + geminiDir}
	return env, args
}

// EffectiveQuota 在缓存早于重置时间时乐观恢复额度，供路由和展示共用。
func EffectiveQuota(qg QuotaGroup, readAt string, now time.Time) QuotaGroup {
	reset, resetErr := time.Parse(time.RFC3339, qg.ResetAt)
	read, readErr := time.Parse(time.RFC3339, readAt)
	if resetErr == nil && readErr == nil && !now.Before(reset) && read.Before(reset) {
		return QuotaGroup{}
	}
	return qg
}

// evaluateCandidateQuota 合并滚动与周额度；排序采用最长窗口的重置时间。
func evaluateCandidateQuota(acc Account, group string, now time.Time) (float64, time.Time) {
	evalGroup := func(qg QuotaGroup) (float64, time.Time) {
		qg = EffectiveQuota(qg, acc.QuotaReadAt, now)
		reset, _ := time.Parse(time.RFC3339, qg.ResetAt)
		return qg.UsedPercent, reset
	}

	var usedPercent float64
	var resetAt time.Time

	if qg5h, ok := acc.Quota[group]; ok {
		usedPercent, resetAt = evalGroup(qg5h)
	}

	if qg7d, ok := acc.Quota[group+" 7d"]; ok {
		used7d, reset7d := evalGroup(qg7d)
		if used7d > usedPercent {
			usedPercent = used7d
		}
		if !reset7d.IsZero() {
			resetAt = reset7d
		}
	}

	return usedPercent, resetAt
}
