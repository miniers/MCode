package accounts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"mcode/internal/providers/multiaccount"
)

var (
	// ErrSystemAccount 系统账号不可被删除。
	ErrSystemAccount = errors.New("cannot delete system account")
	// ErrAccountNotFound 找不到指定的账号。
	ErrAccountNotFound = errors.New("account not found")
)

// ErrAccountGone 账号已不存在（已删除），用于异步任务防幽灵写入校验。
var ErrAccountGone = errors.New("agy: account no longer exists")

// Options 包含 Manager 初始化的各项可注入选项。
type Options struct {
	DataDir           string
	HostHome          string
	Endpoints         OAuthEndpoints
	QuotaHosts        []string
	Opener            func(url string) error
	HTTPClient        *http.Client
	Now               func() time.Time
	SystemTokenReader func(context.Context) (OAuthTokenFile, error)
}

// Manager 统一管理 agy 多账号元数据、隔离目录布局、凭证锁与自动路由。
type Manager struct {
	mu            sync.Mutex
	opts          Options
	index         Index
	routing       RoutingStore
	router        *multiaccount.Router
	tokenLocksMu  sync.Mutex
	tokenLocks    map[string]*sync.Mutex
	autoQuota     atomic.Bool
	lastRefreshMu sync.Mutex
	lastRefresh   map[string]time.Time
	accountOps    map[string]chan struct{}
	refreshErr    map[string]error
	activeQuota   map[string]int
	tokenModTime  map[string]time.Time
}

// NewManager 创建并初始化 Manager 实例。
func NewManager(opts Options) (*Manager, error) {
	if opts.HostHome == "" {
		if opts.SystemTokenReader == nil {
			opts.SystemTokenReader = readSystemKeychainToken
		}
		home, err := os.UserHomeDir()
		if err == nil {
			opts.HostHome = home
		}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}

	if err := os.MkdirAll(opts.DataDir, dirPerm0700); err != nil {
		return nil, fmt.Errorf("create data directory %s: %w", opts.DataDir, err)
	}

	m := &Manager{
		opts:         opts,
		tokenLocks:   make(map[string]*sync.Mutex),
		lastRefresh:  make(map[string]time.Time),
		accountOps:   make(map[string]chan struct{}),
		refreshErr:   make(map[string]error),
		activeQuota:  make(map[string]int),
		tokenModTime: make(map[string]time.Time),
	}

	if err := m.loadIndex(); err != nil {
		return nil, fmt.Errorf("load accounts index: %w", err)
	}
	if err := m.loadRouting(); err != nil {
		return nil, fmt.Errorf("load routing config: %w", err)
	}
	bindings, err := m.loadBindings()
	if err != nil {
		return nil, fmt.Errorf("load conversation bindings: %w", err)
	}
	m.router = multiaccount.NewRouter(bindings, m.saveBindings)

	return m, nil
}

// cloneWarmupState 返回 WarmupState 的深拷贝。
func cloneWarmupState(ws *WarmupState) *WarmupState {
	if ws == nil {
		return nil
	}
	cloned := *ws
	return &cloned
}

// cloneAccountLocked 返回 Account 的深拷贝，防止指针与映射共享竞态。
func cloneAccount(a Account) Account {
	cloned := a
	if a.Quota != nil {
		cloned.Quota = make(map[string]QuotaGroup, len(a.Quota))
		for k, v := range a.Quota {
			cloned.Quota[k] = v
		}
	}
	if a.LastWarmup != nil {
		cloned.LastWarmup = cloneWarmupState(a.LastWarmup)
	}
	return cloned
}

// List 返回当前所有账号的元数据副本（线程安全且完成深拷贝）。
func (m *Manager) List() []Account {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]Account, len(m.index.Accounts))
	for i, acc := range m.index.Accounts {
		result[i] = cloneAccount(acc)
		if m.router != nil {
			result[i].CoolingUntil = m.router.CoolingUntil(result[i].ID)
		}
	}
	return result
}

// Get 返回指定 ID 的账号元数据，若不存在返回 false。
func (m *Manager) Get(id string) (Account, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, acc := range m.index.Accounts {
		if acc.ID == id {
			res := cloneAccount(acc)
			if m.router != nil {
				res.CoolingUntil = m.router.CoolingUntil(res.ID)
			}
			return res, true
		}
	}
	return Account{}, false
}

// effectiveAccountStateLocked 检查账号是否有效启用且未在冷却中。
// 调用方必须持有 m.mu。不得调用 Pick/Route 或更改绑定、权重、默认账号。
func (m *Manager) effectiveAccountStateLocked(acc Account, now time.Time) (bool, string) {
	override := m.routing.Accounts[acc.ID]
	enabled := acc.On
	if override.On != nil {
		enabled = *override.On
	}
	if !enabled {
		return false, fmt.Sprintf("agy: account %s is turned off", acc.ID)
	}
	if acc.Status == StatusDisabled {
		return false, fmt.Sprintf("agy: account %s is disabled", acc.ID)
	}
	if acc.Status == StatusForbidden {
		return false, fmt.Sprintf("agy: account %s is forbidden (%s)", acc.ID, acc.StatusReason)
	}
	if m.router != nil && m.router.Cooling(acc.ID, now) {
		coolingUntil := m.router.CoolingUntil(acc.ID)
		return false, fmt.Sprintf("agy: account %s is cooling until %s", acc.ID, coolingUntil.Local().Format("15:04:05"))
	}
	return true, ""
}

// acquireAccountGate 获取单账号操作门闩，确保刷新、手动预热与自动预热对同账号严格串行化。
// 在等待门闩时不持有任何业务互斥锁。
func (m *Manager) acquireAccountGate(ctx context.Context, id string) (release func(), err error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m.lastRefreshMu.Lock()
		done := m.accountOps[id]
		if done == nil {
			done = make(chan struct{})
			m.accountOps[id] = done
			m.lastRefreshMu.Unlock()
			return func() {
				m.lastRefreshMu.Lock()
				delete(m.accountOps, id)
				close(done)
				m.lastRefreshMu.Unlock()
			}, nil
		}
		m.lastRefreshMu.Unlock()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-done:
		}
	}
}

func (m *Manager) AccountForConversation(conversationID string) (Account, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, _ := m.router.Bound(conversationID)
	if id == "" {
		id = m.index.DefaultAccountID
	}
	return m.findAccountLocked(id)
}

// Remove 删除指定隔离账号。
// 系统账号禁止删除；先从索引中移除并原子落盘，随后清理物理目录、绑定的会话，并将默认账号回退至 system（§4.4）。
func (m *Manager) Remove(id string) error {
	if id == SystemAccountID {
		return ErrSystemAccount
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	idx := -1
	for i, acc := range m.index.Accounts {
		if acc.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return ErrAccountNotFound
	}

	// 1. 从索引移除并原子落盘
	previousAccounts := append([]Account(nil), m.index.Accounts...)
	previousDefault := m.index.DefaultAccountID
	m.index.Accounts = append(m.index.Accounts[:idx], m.index.Accounts[idx+1:]...)
	if m.index.DefaultAccountID == id {
		m.index.DefaultAccountID = SystemAccountID
	}
	if err := m.saveIndexLocked(); err != nil {
		m.index.Accounts = previousAccounts
		m.index.DefaultAccountID = previousDefault
		return err
	}

	// 2. 清理相关会话绑定
	_ = m.router.ForgetAccount(id)

	// 3. 物理删除账号目录
	_ = os.RemoveAll(m.accountDir(id))

	return nil
}

// BoundAccountID 只查会话绑定，不回退默认账号：额度弹窗用它标出「这个会话实际在用的账号」。
// 会话还没跑过一轮（没有会话 ID）或尚未绑定账号时返回 false，调用方按新会话目标账号兜底。
func (m *Manager) BoundAccountID(conversationID string) (string, bool) {
	if conversationID == "" {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	id, ok := m.router.Bound(conversationID)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// BoundConversations 返回绑定在该账号上的活跃会话 ID 列表，供 UI 删除前安全确认使用（§4.2）。
func (m *Manager) BoundConversations(id string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.router.BoundSessions(id)
}

// SetDefault 仅修改索引里的 defaultAccountId 并落盘，不做 §6.3 的系统环境与 IDE 同步。
func (m *Manager) SetDefault(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	found := false
	for _, acc := range m.index.Accounts {
		if acc.ID == id {
			found = true
			break
		}
	}
	if !found {
		return ErrAccountNotFound
	}

	m.index.DefaultAccountID = id
	return m.saveIndexLocked()
}

// Update 更新账号的标签、负载均衡使能、序号及权重等字段（§7.1）。
func (m *Manager) Update(id string, p UpdatePatch) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := -1
	for i, acc := range m.index.Accounts {
		if acc.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return ErrAccountNotFound
	}

	if p.Label != nil {
		m.index.Accounts[idx].Label = *p.Label
	}
	if p.On != nil {
		m.index.Accounts[idx].On = *p.On
	}
	if p.Order != nil {
		m.index.Accounts[idx].Order = *p.Order
	}
	if p.Weight != nil {
		m.index.Accounts[idx].Weight = *p.Weight
	}

	return m.saveIndexLocked()
}

// Routing 获取当前的全局路由调度配置。
func (m *Manager) Routing() multiaccount.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.routing.Config
}

// SetRouting 先规范化路由配置，随后更新并原子落盘。
func (m *Manager) SetRouting(c multiaccount.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.routing.Config = c.Normalize()
	return m.saveRoutingLocked()
}

// AutoWarmup 返回当前自动预热开关状态（缺失时默认开启）。
func (m *Manager) AutoWarmup() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.routing.IsAutoWarmupEnabled()
}

// SetAutoWarmup 更新自动预热开关状态并原子落盘。
func (m *Manager) SetAutoWarmup(enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.routing.AutoWarmup
	m.routing.AutoWarmup = &enabled
	if err := m.saveRoutingLocked(); err != nil {
		m.routing.AutoWarmup = prev
		return err
	}
	return nil
}

// WriteToken 是给后续额度和刷新任务用的回写入口，带防幽灵校验。
// 先持有 m.mu，用已有的 findAccountLocked 确认 id 存在，不存在就返回 ErrAccountGone，不碰磁盘；
// id 为 "system" 时返回错误，不写系统凭证。
// 账号存在时在 m.mu 内调用 withTokenLock 落盘，锁顺序与 oauth.go 中 mergeAndSaveAccount 一致（先 m.mu 再 token 锁）。
func (m *Manager) WriteToken(id string, tok OAuthTokenFile) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if id == SystemAccountID {
		return errors.New("cannot write token for system account")
	}

	if _, exists := m.findAccountLocked(id); !exists {
		return ErrAccountGone
	}

	return m.withTokenLock(id, func(p string) error {
		return atomicWriteJSON(p, tok)
	})
}
