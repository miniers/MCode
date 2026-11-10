package swarm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mcode/internal/git"
	"mcode/internal/harness"
	"mcode/internal/kanban"
	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
	"mcode/internal/store"
)

// NoticePrefix 是 mcode 以 user 消息下发给主代理的系统通知前缀。
const NoticePrefix = "[编排] "

// legacyNoticePrefix 是改名前的通知前缀，历史会话里仍可能出现，识别时需兼容；新生成的文本只用 NoticePrefix。
const legacyNoticePrefix = "[Hydra]"

// ReportPrefix 是子代理汇报消息的统一前缀。
const ReportPrefix = NoticePrefix + "子代理汇报："

// BeePromptPrefix 是派发给子代理的首条任务消息前缀。
const BeePromptPrefix = "你是子代理 "

// legacyBeePromptPrefix 是改名前的子代理任务前缀，历史会话里仍可能出现，识别时需兼容；新生成的文本只用 BeePromptPrefix。
const legacyBeePromptPrefix = "你是 Hydra head"

// defaultMaxBees 是 MaxBees 配置为 0（表示不限制）时实际采用的每 Lead 并发上限，
// 不限制时也保留上限：即使不限制，一个块里二十条也不能同时起二十个 worktree 与 CLI。
const defaultMaxBees = 8

// IsNotice 判断文本是否为 mcode 下发的系统通知（含汇报），兼容旧前缀。
func IsNotice(text string) bool {
	return strings.HasPrefix(text, strings.TrimSpace(NoticePrefix)) || strings.HasPrefix(text, legacyNoticePrefix)
}

// IsBeePrompt 判断文本是否为派发给子代理的任务消息，兼容旧前缀。
func IsBeePrompt(text string) bool {
	return strings.HasPrefix(text, BeePromptPrefix) || strings.HasPrefix(text, legacyBeePromptPrefix)
}

// IsReport 判断文本是否为子代理汇报消息（BuildReport 的产出）。
func IsReport(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), ReportPrefix)
}

type leadState struct {
	leadThreadID string
	enabled      bool
	plansOnly    *bool
	projectID    string
	leadWorkDir  string
	nextIndex    int
	bees         []Bee
	dispatching  bool
	// delegationRounds 记录本次用户请求已使用的委派轮次，随 LeadRecord 持久化。
	delegationRounds int
	// requestStartedAt 记录本次用户请求的起始时间；零值表示尚未标记请求开始。
	requestStartedAt time.Time
	// reportsHeld 表示 lead 最近一轮被用户中断、汇报暂扣到用户下一句话时取 true。
	// 只存在内存里，不持久化，重启后由下一次 checkReports 重新算出来。
	reportsHeld bool
	// stoppedByUser 在用户停止全部子代理后为 true，期间不投递汇报、不回调 OnBeesIdle，直到主会话再次运行或派发新 bee。
	stoppedByUser bool
	// heldQueue 保存重启后从持久化记录里恢复出来、等待用户决定是否续跑的排队条目。
	// 它们不进入 m.queue，因此不会被 processQueue 自动派发；只有 ResumeInterrupted 才会放出。
	heldQueue []queuedBee
}

func (l *leadState) toState() State {
	var po *bool
	if l.plansOnly != nil {
		v := *l.plansOnly
		po = &v
	}
	st := State{
		LeadThreadID: l.leadThreadID,
		Enabled:      l.enabled,
		PlansOnly:    po,
		Bees:         make([]Bee, len(l.bees)),
	}
	for i, h := range l.bees {
		st.Bees[i] = h.clone()
		// 被打断的 bee 与待决定的排队条目都靠用户手动续跑，前端据此提示「续跑/放弃」。
		if h.Interrupted && h.Status == StatusStopped {
			st.Interrupted++
		}
	}
	st.Interrupted += len(l.heldQueue)
	sort.Slice(st.Bees, func(i, j int) bool {
		return st.Bees[i].Index < st.Bees[j].Index
	})
	return st
}

type workDirOwner struct {
	leadThreadID string
	projectID    string
}

// defaultProgressFlushDelay 是纯进度类变更的合并窗口：一个窗口内的多次进度变更只落盘并通知一次。
const defaultProgressFlushDelay = 150 * time.Millisecond

// reactivateProbeInterval 是已结束 bee 续跑探测的最小间隔：探测要对每个已结束 bee 查一次
// 驱动，随累计 bee 数线性增长，必须限流。
const reactivateProbeInterval = 10 * time.Second

// beeCopyReclaimIdle 是已落地的 bee 副本可回收前的最短静置时间：bee 结束后至少空闲这么久，
// 避免收尾（批次落地回调、汇报、续跑）还没走完就把副本删掉。
const beeCopyReclaimIdle = 10 * time.Minute

// beeCopyReclaimInterval 是自动回收 bee 副本的最小间隔：每个 tick 都会调用回收，
// 但一分钟内最多真正扫一次，避免持锁遍历与 git 操作随 tick 频率放大。
const beeCopyReclaimInterval = time.Minute

type progressSnapshot struct {
	toolCalls int
	tokens    int
	activity  string
	summary   string
	// progress 是驱动给出的不透明进展指纹：Manager 自身不维护 ToolCalls/Tokens/Activity，
	// 只能靠它识别 bee 的新输出（流式文本、工具调用）。
	progress string
}

// Manager 负责统一管理编排子代理的派发、状态轮询、补丁合并与生命周期清理。
type Manager struct {
	opts Options

	mu sync.Mutex
	// deliverMu 把「检查 stoppedByUser」与汇报/空闲投递串起来：StopAll 先拿它再翻转标记，
	// 因此停止要么等当前投递完成，要么先于检查生效让投递被跳过。
	// 锁顺序固定为先 deliverMu 再 mu；持 mu 时不得获取 deliverMu。
	deliverMu        sync.Mutex
	dispatchMu       sync.Mutex
	persistMu        sync.Mutex
	leads            map[string]*leadState
	startingWorkDirs map[string]workDirOwner
	mergeMus         map[string]*sync.Mutex
	mergeMuLock      sync.Mutex
	beeProgress      map[string]progressSnapshot

	settingsMu   sync.Mutex
	settingsFunc func() Settings
	// memoryRecallMu 保护 memoryRecall：派发前的记忆检索钩子可在接线后注入，
	// 与派发路径并发读取。
	memoryRecallMu  sync.Mutex
	memoryRecall    func(ctx context.Context, workDir, query string) string
	timeoutMu       sync.RWMutex
	beeTimeout      time.Duration
	queue           []queuedBee
	retriedBees     map[string]int
	emptyNudgedBees map[string]bool
	// retryingBees 记录用户手动「沿用改动续做」正在派发中的 bee 线程，
	// 防止连点在同一份半成品上派出两个子会话；派发结束即清除。
	retryingBees map[string]bool
	// continueNudgedBees 记录失败后已经发过「中断续接」催促的 bee，避免同一条 bee 反复催。
	continueNudgedBees map[string]bool
	// reactivateSeen 记录每个 bee 连续多少个 tick 被看到「已在终态、但会话又在运行」，
	// 用于识别用户在 bee 自己的线程里让它续跑的情况。
	reactivateSeen map[string]int
	// reactivateProbedAt 记录每个已结束 bee 上次做续跑探测的时间：续跑探测要对每个已结束
	// bee 查一次驱动，随累计 bee 数线性增长，因此按 reactivateProbeInterval 限流。
	reactivateProbedAt map[string]time.Time
	// lastBeeCopyReclaim 记录上一次自动回收 bee 工作副本的时间，用于把回收限流到每分钟一次；
	// 由 m.mu 保护。
	lastBeeCopyReclaim time.Time

	// 纯进度类变更（工具计数、tokens、Activity、思考分钟数）的合并窗口：标脏后在
	// progressDelay 内合并成一次落盘与一次通知，避免高频全量落盘与全量状态推送。
	progressMu    sync.Mutex
	progressDirty map[string]bool
	progressTimer *time.Timer
	progressDelay time.Duration

	tickMu  sync.Mutex
	stateMu sync.Mutex
	running bool
	stopCh  chan struct{}
	wg      sync.WaitGroup

	// funnelBusy 按 lead 串行化审查漏斗 worker；funnelCtx 供 Stop 取消 L0 命令与 L2 调用。
	// 三者都由 stateMu 保护，不写盘。
	funnelBusy   map[string]bool
	funnelCtx    context.Context
	funnelCancel context.CancelFunc

	cbMu     sync.Mutex
	onChange func(leadThreadID string)
}

type queuedBee struct {
	leadThreadID string
	projectID    string
	leadWorkDir  string
	spec         BeeSpec
	index        int
}

// SetSettingsFunc 设置配置读取函数。
func (m *Manager) SetSettingsFunc(fn func() Settings) {
	m.settingsMu.Lock()
	defer m.settingsMu.Unlock()
	m.settingsFunc = fn
}

// Settings 返回当前生效的配置快照。若未注入读取函数，返回默认配置。
func (m *Manager) Settings() Settings {
	m.settingsMu.Lock()
	fn := m.settingsFunc
	m.settingsMu.Unlock()
	if fn != nil {
		return fn()
	}
	return Settings{
		Enabled:       true,
		AutoDelegate:  true,
		MaxBees:       0,
		WorkspaceMode: "worktree",
		RetryFailed:   false,
		AutoMerge:     false,
	}
}

// 合并前审查档位：off 直接合入，ai 先由审查会话裁决，human 等用户批准。
const (
	reviewModeOff   = "off"
	reviewModeAI    = "ai"
	reviewModeHuman = "human"
)

// reviewMode 返回归一化后的合并审查档位：off、ai 或 human。空串与未知值按 off 处理。
func (m *Manager) reviewMode() string {
	switch strings.ToLower(strings.TrimSpace(m.Settings().ReviewMode)) {
	case reviewModeAI:
		return reviewModeAI
	case reviewModeHuman:
		return reviewModeHuman
	default:
		return reviewModeOff
	}
}

// reviewEnabled 报告合并前是否需要审查（ai 或 human 档）。
func (m *Manager) reviewEnabled() bool {
	return m.reviewMode() != reviewModeOff
}

// SetMemoryRecall 设置派发 bee 时检索相关记忆的钩子；nil 表示不附加记忆段落。
func (m *Manager) SetMemoryRecall(fn func(ctx context.Context, workDir, query string) string) {
	m.memoryRecallMu.Lock()
	defer m.memoryRecallMu.Unlock()
	m.memoryRecall = fn
}

// memoryRecallHook 返回当前生效的记忆检索钩子；未注入时为 nil。
func (m *Manager) memoryRecallHook() func(ctx context.Context, workDir, query string) string {
	m.memoryRecallMu.Lock()
	defer m.memoryRecallMu.Unlock()
	return m.memoryRecall
}

// DataDir 返回管理器使用的数据目录：取 WorktreeDir 的父目录，WorktreeDir 为空时返回空串。
func (m *Manager) DataDir() string {
	if m.opts.WorktreeDir == "" {
		return ""
	}
	return filepath.Dir(m.opts.WorktreeDir)
}

// SetBeeTimeout 设置 Bee 运行超时时间；加锁保存当前值。零值或负值表示清除自定义超时，恢复使用 Settings/Options 或默认值。
func (m *Manager) SetBeeTimeout(d time.Duration) {
	m.timeoutMu.Lock()
	defer m.timeoutMu.Unlock()
	m.beeTimeout = d
}

// BeeTimeout 返回当前生效的 Bee 运行超时；优先返回通过 SetBeeTimeout 动态设置的值，
// 其次若注入了 SettingsFunc 则取其配置值，再次取 Options.NoEditStallTimeout，最后回落为 DefaultBeeTimeout。
func (m *Manager) BeeTimeout() time.Duration {
	m.timeoutMu.RLock()
	defer m.timeoutMu.RUnlock()
	if m.beeTimeout > 0 {
		return m.beeTimeout
	}
	if m.settingsFunc != nil {
		st := m.Settings()
		if st.BeeTimeoutMinutes > 0 {
			return time.Duration(st.BeeTimeoutMinutes) * time.Minute
		}
	}
	if m.opts.NoEditStallTimeout > 0 {
		return m.opts.NoEditStallTimeout
	}
	return DefaultBeeTimeout
}

// profilesPath 返回 Bee Profile 定义文件路径：Options.ProfilesPath 非空时直接使用；
// 否则取 filepath.Join(filepath.Dir(WorktreeDir), ProfilesFileName)，即 <DataDir>/hydra-profiles.json（旧版遗留文件名，仅用于导入）；WorktreeDir 也为空时返回空串。
func (m *Manager) profilesPath() string {
	if m.opts.ProfilesPath != "" {
		return m.opts.ProfilesPath
	}
	if m.opts.WorktreeDir == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(m.opts.WorktreeDir), ProfilesFileName)
}

// ResolvedTier 是某个档位在当前会话里实际会跑到的服务商、模型与推理强度。
// 它只用于给主代理展示档位成本，不参与派发决策。
type ResolvedTier struct {
	Provider string
	Model    string
	Effort   string
}

// TierPreview 返回各档位在指定主会话里实际解析出的 provider/model/effort，
// 解析顺序与真实派发一致（档位 → 会话子代理模型 → 配对 → 线程覆盖），最后按设置默认值补齐。
// 只做解析：不派发、不写 Manager 状态、不占预算，解析失败的 notes 直接丢弃。
func (m *Manager) TierPreview(leadThreadID string) map[string]ResolvedTier {
	st := m.Settings()
	if len(st.Tiers) == 0 {
		return nil
	}
	var tc ThreadContext
	if m.opts.ThreadContext != nil {
		tc = m.opts.ThreadContext(leadThreadID)
	}
	profiles, _ := LoadProfiles(m.profilesPath())
	out := make(map[string]ResolvedTier, len(st.Tiers))
	for _, t := range st.Tiers {
		cfg, _ := ResolveBeeConfig(ResolveInput{
			NormalizeBee:       m.opts.NormalizeBee,
			Delegation:         Delegation{Tier: t.ID},
			Profiles:           profiles,
			Pairs:              st.Pairs,
			Tiers:              st.Tiers,
			DefaultTier:        st.DefaultTier,
			PairID:             tc.PairID,
			ThreadOverrides:    tc.BeeTiers,
			ProviderEnabled:    tc.ProviderEnabled,
			SessionBeeProvider: tc.BeeProvider,
			SessionBeeModel:    tc.BeeModel,
			SessionBeeEffort:   tc.BeeEffort,
		})
		provider, model, effort, _ := m.effectiveBeeRuntime(cfg.Provider, cfg.Model, cfg.Effort, st, tc)
		out[t.ID] = ResolvedTier{Provider: provider, Model: model, Effort: effort}
	}
	return out
}

// resolveBeeRuntime 按设置里的默认值补齐一次派发的 provider/model/effort：
// provider 为空时回退到默认 Provider，model 也为空时使用默认 Model；effort 为空且这次实际用的
// 就是默认 Provider 与 Model 时回退到默认 Effort。真实派发与档位预览共用这一套规则。
func resolveBeeRuntime(provider, model, effort string, settings Settings) (string, string, string) {
	if provider == "" {
		provider = settings.DefaultProvider
		if model == "" {
			model = settings.DefaultModel
		}
	}
	if effort == "" && provider == settings.DefaultProvider && model == settings.DefaultModel {
		effort = settings.DefaultEffort
	}
	return provider, model, effort
}

func (m *Manager) effectiveBeeRuntime(provider, model, effort string, settings Settings, tc ThreadContext) (string, string, string, error) {
	provider, model, effort = resolveBeeRuntime(provider, model, effort, settings)
	if m.opts.NormalizeBee != nil {
		provider, model = m.opts.NormalizeBee(provider, model)
	}
	if provider == "" && m.opts.DefaultBeeSource != nil {
		p, defaultModel, err := m.opts.DefaultBeeSource(tc)
		if err != nil {
			return "", "", effort, err
		}
		provider = p
		if model == "" {
			model = defaultModel
		}
	}
	return provider, model, effort, nil
}

// QueueLength 返回当前正在排队的 bee 数量。
func (m *Manager) QueueLength() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.queue)
}

// ActiveWorktreePaths 返回所有未结束（非 stopped、已合入、已丢弃）Bee 的 worktree 路径，
// 供自动清理判断哪些 worktree 仍在使用。
func (m *Manager) ActiveWorktreePaths() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var paths []string
	for _, lead := range m.leads {
		for _, h := range lead.bees {
			if h.WorktreePath == "" || h.Status == StatusStopped || h.Merged || h.MergeDiscarded {
				continue
			}
			paths = append(paths, h.WorktreePath)
		}
	}
	return paths
}

// maxBeesLimit 返回每个 Lead 的并发 bee 上限：MaxBees 小于等于 0（表示不限制）时取 defaultMaxBees。
func (m *Manager) maxBeesLimit() int {
	if maxBees := m.Settings().MaxBees; maxBees > 0 {
		return maxBees
	}
	return defaultMaxBees
}

// MaxBeesLimit 导出每个 Lead 的并发 bee 上限，供派发层按「单个 swarm 块条目数」做硬拦截。
func (m *Manager) MaxBeesLimit() int {
	return m.maxBeesLimit()
}

// requestTimeoutLimit 返回本次用户请求的总时长预算：优先使用 Settings.RequestTimeoutMinutes，
// 其次 Options.RequestTimeout，都为 0 时默认 3 小时。
func (m *Manager) requestTimeoutLimit() time.Duration {
	if minutes := m.Settings().RequestTimeoutMinutes; minutes > 0 {
		return time.Duration(minutes) * time.Minute
	}
	if m.opts.RequestTimeout > 0 {
		return m.opts.RequestTimeout
	}
	return 3 * time.Hour
}

// formatBudgetDuration 把总时长预算格式化为中文可读文本，如「3 小时」「2 小时 30 分钟」「45 分钟」。
func formatBudgetDuration(d time.Duration) string {
	minutes := int(d.Round(time.Minute) / time.Minute)
	if minutes < 1 {
		// 不足一分钟也向上取到一分钟，避免出现「0 分钟」的上限说明。
		minutes = 1
	}
	hours := minutes / 60
	rest := minutes % 60
	switch {
	case hours == 0:
		return fmt.Sprintf("%d 分钟", rest)
	case rest == 0:
		return fmt.Sprintf("%d 小时", hours)
	default:
		return fmt.Sprintf("%d 小时 %d 分钟", hours, rest)
	}
}

// requestTimedOut 判断该 Lead 本次请求是否已用完总时长预算；尚未开始计时（零值）时不算超时。
func (m *Manager) requestTimedOut(leadThreadID string) bool {
	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok || lead.requestStartedAt.IsZero() {
		m.mu.Unlock()
		return false
	}
	startedAt := lead.requestStartedAt
	m.mu.Unlock()
	return m.now().Sub(startedAt) >= m.requestTimeoutLimit()
}

// runningCountForLeadLocked 统计该 Lead 当前处于运行中的 bee 数量。
// 并发与排队都按 Lead 计：一个 Lead 的派发排满不会挡住另一个 Lead 的派发。调用方需持有 m.mu。
func (m *Manager) runningCountForLeadLocked(leadThreadID string) int {
	lead, ok := m.leads[leadThreadID]
	if !ok {
		return 0
	}
	count := 0
	for _, h := range lead.bees {
		// 审查中的 bee 虽然已经结束，仍占用一个并发名额：它的结论会决定是否合入。
		if h.Status == StatusRunning || h.ReviewState == ReviewRunning {
			count++
		}
	}
	return count
}

func (m *Manager) getRetryCount(threadID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.retriedBees[threadID]
}

func (m *Manager) setRetryCount(threadID string, count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retriedBees[threadID] = count
}

func (m *Manager) hasEmptyNudge(threadID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emptyNudgedBees[threadID]
}

func (m *Manager) setEmptyNudge(threadID string, v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.emptyNudgedBees == nil {
		m.emptyNudgedBees = make(map[string]bool)
	}
	m.emptyNudgedBees[threadID] = v
}

func (m *Manager) hasContinueNudge(threadID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.continueNudgedBees[threadID]
}

func (m *Manager) setContinueNudge(threadID string, v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.continueNudgedBees == nil {
		m.continueNudgedBees = make(map[string]bool)
	}
	m.continueNudgedBees[threadID] = v
}

// NewManager 创建编排管理器实例；PollInterval 小于等于 0 时默认设为 2 秒；StallTimeout 小于等于 0 时默认设为 10 分钟；
// ThinkingAfter 默认 2 分钟；NoEditStallTimeout 默认 DefaultBeeTimeout；SettleTimeout 默认 30 分钟；
// RequestTimeout 默认 3 小时。
func NewManager(opts Options) *Manager {
	if opts.PollInterval <= 0 {
		opts.PollInterval = 2 * time.Second
	}
	if opts.StallTimeout <= 0 {
		opts.StallTimeout = 10 * time.Minute
	}
	if opts.ThinkingAfter <= 0 {
		opts.ThinkingAfter = 2 * time.Minute
	}
	if opts.NoEditStallTimeout <= 0 {
		opts.NoEditStallTimeout = DefaultBeeTimeout
	}
	if opts.SettleTimeout <= 0 {
		opts.SettleTimeout = DefaultSettleTimeout
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = 3 * time.Hour
	}
	return &Manager{
		opts:               opts,
		leads:              make(map[string]*leadState),
		startingWorkDirs:   make(map[string]workDirOwner),
		mergeMus:           make(map[string]*sync.Mutex),
		beeProgress:        make(map[string]progressSnapshot),
		retriedBees:        make(map[string]int),
		emptyNudgedBees:    make(map[string]bool),
		continueNudgedBees: make(map[string]bool),
		reactivateSeen:     make(map[string]int),
		reactivateProbedAt: make(map[string]time.Time),
		progressDirty:      make(map[string]bool),
		progressDelay:      defaultProgressFlushDelay,
		memoryRecall:       opts.MemoryRecall,
		funnelBusy:         make(map[string]bool),
	}
}

// now 返回当前时间；注入 Options.Now 后统一走注入时钟，便于测试控制看门狗与等待超时。
func (m *Manager) now() time.Time {
	if m.opts.Now != nil {
		return m.opts.Now()
	}
	return time.Now()
}

// Start 启动后台定时轮询协程，多次调用无副作用。
func (m *Manager) Start() {
	m.stateMu.Lock()
	if m.running {
		m.stateMu.Unlock()
		return
	}
	m.running = true
	m.stopCh = make(chan struct{})
	// 漏斗 ctx 与调度循环同生命周期：Stop 时先取消它，让 L0 命令和 L2 调用随 context 退出。
	m.funnelCtx, m.funnelCancel = context.WithCancel(context.Background())
	m.stateMu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(m.opts.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-m.stopCh:
				return
			case <-ticker.C:
				m.Tick(context.Background())
			}
		}
	}()
}

// Stop 停止调度循环并等待后台轮询协程平稳退出，幂等。
func (m *Manager) Stop() {
	m.stateMu.Lock()
	running := m.running
	if running {
		m.running = false
		close(m.stopCh)
		if m.funnelCancel != nil {
			m.funnelCancel()
			m.funnelCancel = nil
		}
	}
	m.stateMu.Unlock()
	if running {
		m.wg.Wait()
	}
	// 退出前兑现合并窗口内的进度变更：最后一次状态必须已落盘并送达监听方。
	m.flushPendingChanges()
}

// SetOnChange 设置主会话状态发生变更时的回调通知函数。
func (m *Manager) SetOnChange(fn func(leadThreadID string)) {
	m.cbMu.Lock()
	defer m.cbMu.Unlock()
	m.onChange = fn
}

// notifyChange 处理状态类变更（启动、结束、合并、提问等）：立即落盘并立即通知监听方。
// 该 lead 排队中的进度类变更随本次一起落盘，由这一次通知覆盖，避免重复推送。
func (m *Manager) notifyChange(leadThreadID string) {
	m.clearProgress(leadThreadID)
	m.persistLead(leadThreadID)
	m.fireChange(leadThreadID)
}

// notifyProgress 处理纯进度类变更（工具计数、tokens、Activity、思考分钟数）：只标脏，
// 由短延时合并成一次落盘与一次通知。这类变更在子代理多时每个 tick 都会出现，逐次全量落盘
// 会随累计 bee 数放大写库开销，并拖慢状态推送。
func (m *Manager) notifyProgress(leadThreadID string) {
	if leadThreadID == "" {
		return
	}
	m.progressMu.Lock()
	defer m.progressMu.Unlock()
	if m.progressDirty == nil {
		m.progressDirty = make(map[string]bool)
	}
	if m.progressDirty[leadThreadID] {
		// 窗口内已排队：定时器到期时读的是最新状态，无需重复排队。
		return
	}
	m.progressDirty[leadThreadID] = true
	if m.progressTimer != nil {
		return
	}
	delay := m.progressDelay
	if delay <= 0 {
		delay = defaultProgressFlushDelay
	}
	m.progressTimer = time.AfterFunc(delay, func() { m.flushPendingChanges() })
}

// clearProgress 清掉指定 lead 排队中的进度类变更标记，表示它已随状态类变更一起落盘。
func (m *Manager) clearProgress(leadThreadID string) {
	m.progressMu.Lock()
	delete(m.progressDirty, leadThreadID)
	m.progressMu.Unlock()
}

// flushPendingChanges 兑现全部排队中的进度类变更，并停掉合并窗口的定时器。
func (m *Manager) flushPendingChanges() {
	m.progressMu.Lock()
	dirty := make([]string, 0, len(m.progressDirty))
	for id := range m.progressDirty {
		dirty = append(dirty, id)
	}
	m.progressDirty = make(map[string]bool, len(dirty))
	if m.progressTimer != nil {
		m.progressTimer.Stop()
		m.progressTimer = nil
	}
	m.progressMu.Unlock()
	for _, id := range dirty {
		m.persistLead(id)
		m.fireChange(id)
	}
}

// fireChange 把变更回调给监听方；回调必须在所有 Manager 锁之外执行。
func (m *Manager) fireChange(leadThreadID string) {
	m.cbMu.Lock()
	fn := m.onChange
	m.cbMu.Unlock()
	if fn != nil {
		fn(leadThreadID)
	}
}

func (m *Manager) persistLead(leadThreadID string) {
	if m.opts.Persister == nil {
		return
	}
	// 快照与写入一起串行化，避免旧快照晚于新状态落盘。
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		m.mu.Unlock()
		return
	}
	rec := LeadRecord{
		LeadThreadID:     lead.leadThreadID,
		Enabled:          lead.enabled,
		PlansOnly:        lead.plansOnly,
		ProjectID:        lead.projectID,
		LeadWorkDir:      lead.leadWorkDir,
		NextIndex:        lead.nextIndex,
		DelegationRounds: lead.delegationRounds,
		RequestStartedAt: lead.requestStartedAt,
		Bees:             make([]Bee, len(lead.bees)),
	}
	for i, h := range lead.bees {
		rec.Bees[i] = h.clone()
	}
	// 排队条目只在内存里，重启即丢：按 lead 收集 m.queue 与 heldQueue 一并落盘。
	// 同一个 lead 下两类条目都归属它，顺序按「重启前排队」在前、「重启后待决」在后。
	for _, q := range m.queue {
		if q.leadThreadID != leadThreadID {
			continue
		}
		rec.Queue = append(rec.Queue, queueRecordOf(q))
	}
	for _, q := range lead.heldQueue {
		rec.Queue = append(rec.Queue, queueRecordOf(q))
	}
	m.mu.Unlock()

	if err := m.opts.Persister.SaveLead(context.Background(), rec); err != nil {
		log.Printf("swarm: persist lead %s failed: %v", leadThreadID, err)
	}
}

// queueRecordOf 把内存里的排队条目转成可落盘的记录（BeeSpec 的 unexported 字段不落盘）。
func queueRecordOf(q queuedBee) QueuedBeeRecord {
	return QueuedBeeRecord{
		ProjectID:   q.projectID,
		LeadWorkDir: q.leadWorkDir,
		Index:       q.index,
		Spec:        q.spec,
	}
}

// Restore 从持久化存储中恢复各主会话及其子协作者状态；未完成的 running 协作者标记为已中断。
func (m *Manager) Restore(ctx context.Context) error {
	if m.opts.Persister == nil {
		return nil
	}
	records, err := m.opts.Persister.LoadLeads(ctx)
	if err != nil {
		return fmt.Errorf("load leads failed: %w", err)
	}

	now := m.now()
	var restoredLeadIDs []string

	m.mu.Lock()
	for _, rec := range records {
		var po *bool
		if rec.PlansOnly != nil {
			v := *rec.PlansOnly
			po = &v
		}
		lead := &leadState{
			leadThreadID:     rec.LeadThreadID,
			enabled:          rec.Enabled,
			plansOnly:        po,
			projectID:        rec.ProjectID,
			leadWorkDir:      rec.LeadWorkDir,
			nextIndex:        rec.NextIndex,
			delegationRounds: rec.DelegationRounds,
			requestStartedAt: rec.RequestStartedAt,
			bees:             make([]Bee, len(rec.Bees)),
		}
		for i, h := range rec.Bees {
			beeCopy := h.clone()
			// 重启后工作副本可能已被清理：清空路径，避免后续落地与清理对着不存在的目录反复失败。
			if beeCopy.WorktreePath != "" {
				if _, statErr := os.Stat(beeCopy.WorktreePath); os.IsNotExist(statErr) {
					beeCopy.WorktreePath = ""
					beeCopy.Summary += "（工作副本已不存在）"
					if beeCopy.PendingMerge {
						// 没有工作副本就再也合不进来，取消待审以免一直阻挡批次落地。
						beeCopy.PendingMerge = false
						beeCopy.MergeError = "工作副本已不存在，无法合并"
						// 审查也随之失去意义：清掉审查状态，避免整批汇报被永久卡住。
						beeCopy.ReviewState = ReviewNone
					}
				}
			}
			if beeCopy.ReviewState == ReviewRunning {
				// 审查会话随进程一起消失，交回用户裁决；清掉线程号，避免继续轮询旧会话。
				beeCopy.ReviewState = ReviewNeedsHuman
				beeCopy.ReviewReason = "MCode 重启，审查中断"
				beeCopy.ReviewThreadID = ""
			}
			if beeCopy.Status == StatusRunning {
				beeCopy.Status = StatusStopped
				// 标记为可续跑：进程重启后这个 bee 的会话已断，交回用户决定续跑还是放弃。
				beeCopy.Interrupted = true
				if beeCopy.FinishedAt == nil {
					beeCopy.FinishedAt = &now
				}
				// 中断的 bee 不再等待落地，避免残留的等待起点阻塞批次落地回调。
				beeCopy.MergeWaitSince = nil
				prefix := "MCode 重启，子代理已中断"
				if beeCopy.Summary != "" {
					beeCopy.Summary = prefix + "；" + beeCopy.Summary
				} else {
					beeCopy.Summary = prefix
				}
			}
			lead.bees[i] = beeCopy
		}
		// 重启前还在排队、尚未启动的条目不再自动派发：放进 heldQueue 等用户决定是否续跑。
		for _, q := range rec.Queue {
			lead.heldQueue = append(lead.heldQueue, queuedBee{
				leadThreadID: rec.LeadThreadID,
				projectID:    q.ProjectID,
				leadWorkDir:  q.LeadWorkDir,
				spec:         q.Spec,
				index:        q.Index,
			})
		}
		m.leads[rec.LeadThreadID] = lead
		restoredLeadIDs = append(restoredLeadIDs, rec.LeadThreadID)
	}
	m.mu.Unlock()

	for _, leadID := range restoredLeadIDs {
		m.persistLead(leadID)
	}
	return nil
}

func (m *Manager) getLeadMergeMutex(leadThreadID string) *sync.Mutex {
	m.mergeMuLock.Lock()
	defer m.mergeMuLock.Unlock()
	mu, ok := m.mergeMus[leadThreadID]
	if !ok {
		mu = &sync.Mutex{}
		m.mergeMus[leadThreadID] = mu
	}
	return mu
}

// SetEnabled 设置指定主会话的编排启用状态并返回最新状态快照。
func (m *Manager) SetEnabled(leadThreadID string, enabled bool) State {
	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		lead = &leadState{
			leadThreadID: leadThreadID,
			enabled:      enabled,
			bees:         []Bee{},
		}
		m.leads[leadThreadID] = lead
	} else {
		lead.enabled = enabled
	}
	st := lead.toState()
	m.mu.Unlock()

	m.notifyChange(leadThreadID)
	return st
}

// SetLeadPlansOnly 设置指定主会话的编排只规划开关覆盖值并返回最新状态快照。
func (m *Manager) SetLeadPlansOnly(leadThreadID string, v *bool) State {
	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		lead = &leadState{
			leadThreadID: leadThreadID,
			plansOnly:    v,
			bees:         []Bee{},
		}
		m.leads[leadThreadID] = lead
	} else {
		lead.plansOnly = v
	}
	st := lead.toState()
	m.mu.Unlock()

	m.notifyChange(leadThreadID)
	return st
}

// InheritLead 把源会话的编排状态（开关与只规划覆盖值）继承给目标会话，用于转发会话时让新会话
// 与源会话保持同一套编排配置。源会话未开启编排时目标会话保持原有状态。
// 本方法不持 m.mu，内部通过 State/SetEnabled/SetLeadPlansOnly 各自加锁，避免重入死锁。
func (m *Manager) InheritLead(fromThreadID, toThreadID string) {
	src := m.State(fromThreadID)
	if src.Enabled {
		m.SetEnabled(toThreadID, true)
	}
	if src.PlansOnly != nil {
		v := *src.PlansOnly
		m.SetLeadPlansOnly(toThreadID, &v)
	}
}

// BeginLeadRequest 标记一次新的用户请求开始：重置委派轮次并记录请求起始时间，作为 bee 预算与总时长看门狗的基准。
func (m *Manager) BeginLeadRequest(leadThreadID string) {
	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		lead = &leadState{
			leadThreadID: leadThreadID,
			bees:         []Bee{},
		}
		m.leads[leadThreadID] = lead
	}
	lead.delegationRounds = 0
	lead.requestStartedAt = m.now()
	m.mu.Unlock()

	m.notifyChange(leadThreadID)
}

// DelegationRounds 返回本次请求已使用的委派轮次数。
func (m *Manager) DelegationRounds(leadThreadID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		return 0
	}
	return lead.delegationRounds
}

// NoteDelegationRound 使委派轮次加一并持久化，返回新的轮次数。
func (m *Manager) NoteDelegationRound(leadThreadID string) int {
	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		lead = &leadState{
			leadThreadID: leadThreadID,
			bees:         []Bee{},
		}
		m.leads[leadThreadID] = lead
	}
	lead.delegationRounds++
	rounds := lead.delegationRounds
	m.mu.Unlock()

	m.persistLead(leadThreadID)
	return rounds
}

// State 获取指定主会话当前的编排状态快照；不存在时返回 Enabled=false、Bees 为空的快照。
func (m *Manager) State(leadThreadID string) State {
	m.mu.Lock()
	defer m.mu.Unlock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		return State{
			LeadThreadID: leadThreadID,
			Enabled:      false,
			Bees:         []Bee{},
			Queued:       m.queuedCountLocked(leadThreadID),
		}
	}
	st := lead.toState()
	st.Queued = m.queuedCountLocked(leadThreadID)
	return st
}

// RunningBees 返回所有处于 running 状态的 bee 副本，key 是派发它的 lead 会话 ID。
func (m *Manager) RunningBees() map[string][]Bee {
	m.mu.Lock()
	defer m.mu.Unlock()

	running := make(map[string][]Bee)
	for leadThreadID, lead := range m.leads {
		for i := range lead.bees {
			if lead.bees[i].Status != StatusRunning {
				continue
			}
			running[leadThreadID] = append(running[leadThreadID], lead.bees[i].clone())
		}
	}
	return running
}

// queuedCountLocked 统计派发队列中属于该 lead 的条目数；调用方需持有 m.mu。
func (m *Manager) queuedCountLocked(leadThreadID string) int {
	n := 0
	for _, q := range m.queue {
		if q.leadThreadID == leadThreadID {
			n++
		}
	}
	return n
}

// Busy 判断主会话名下是否还有编排活动：正在派发、有排队或运行中的子代理，
// 或仍有已结束但未汇报的 bee（这些回报投递后会开启主会话的下一轮）。
// 单个 bee 结束不影响该判定：只要整批没有全部落定并汇报，就仍算忙。
// lead 被用户中断、汇报暂扣（reportsHeld）时，未汇报的 bee 不再算忙：否则忙态要等
// 用户下一句话才清掉，web 端会一直显示运行中。
// 这里绝不能调用 Driver/后端方法：调用方 swarmBusyLocked 持有 MemoryBackend.mu，
// 再进 SessionState 会重复取锁导致死锁。
func (m *Manager) Busy(leadThreadID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.queuedCountLocked(leadThreadID) > 0 {
		return true
	}
	lead, ok := m.leads[leadThreadID]
	if !ok {
		return false
	}
	if lead.dispatching {
		return true
	}
	// 未配置汇报投递时不会再有回报开启主会话的下一轮，不能把未汇报的 bee 算成忙，
	// 否则忙态永远清不了，完成提醒会彻底静音。
	deliversReports := m.opts.OnReport != nil
	for i := range lead.bees {
		// 审查进行中算忙：结论会决定是否合入，主代理要等这一批走完。
		if lead.bees[i].ReviewState == ReviewRunning {
			return true
		}
		// 等人工审查的 bee 已经结束，结论要等用户给出，不能把主会话一直算成忙。
		if lead.bees[i].PendingMerge {
			continue
		}
		if lead.bees[i].Status == StatusRunning {
			return true
		}
		if deliversReports && !lead.reportsHeld && !lead.bees[i].Reported {
			return true
		}
	}
	return false
}

// LeadIDs 返回所有已登记主会话的 threadID，排序后供后台循环遍历。
func (m *Manager) LeadIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.leads))
	for leadID := range m.leads {
		ids = append(ids, leadID)
	}
	sort.Strings(ids)
	return ids
}

// BeeOwner 根据子会话的 threadID 反查其所属的 leadThreadID。
func (m *Manager) BeeOwner(threadID string) (leadThreadID string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for leadID, lead := range m.leads {
		for _, h := range lead.bees {
			if h.ThreadID == threadID {
				return leadID, true
			}
		}
	}
	return "", false
}

// LeadThreadForWorkDir 根据 bee 的工作目录反查所属主会话。
func (m *Manager) LeadThreadForWorkDir(workDir string) (leadThreadID string, ok bool) {
	owner, ok := m.ownerForWorkDir(workDir)
	return owner.leadThreadID, ok
}

// ProjectIDForWorkDir 使用 bee 自身的项目归属，支持跨项目委派和启动中的会话。
func (m *Manager) ProjectIDForWorkDir(workDir string) (projectID string, ok bool) {
	owner, ok := m.ownerForWorkDir(workDir)
	return owner.projectID, ok && owner.projectID != ""
}

func (m *Manager) ownerForWorkDir(workDir string) (workDirOwner, bool) {
	if workDir == "" {
		return workDirOwner{}, false
	}
	cleanWorkDir := filepath.Clean(workDir)

	m.mu.Lock()
	defer m.mu.Unlock()

	sep := string(filepath.Separator)
	for path, owner := range m.startingWorkDirs {
		if cleanWorkDir == path || strings.HasPrefix(cleanWorkDir, path+sep) {
			return owner, true
		}
	}
	for leadID, lead := range m.leads {
		for _, h := range lead.bees {
			if h.WorktreePath == "" {
				continue
			}
			cleanBeePath := filepath.Clean(h.WorktreePath)
			if cleanWorkDir == cleanBeePath || strings.HasPrefix(cleanWorkDir, cleanBeePath+sep) {
				projectID := h.ProjectID
				if projectID == "" {
					projectID = lead.projectID
				}
				return workDirOwner{leadThreadID: leadID, projectID: projectID}, true
			}
		}
	}
	return workDirOwner{}, false
}

func (m *Manager) getBaseCommit(ctx context.Context, dir, leadPrefix string) (string, error) {
	ref := fmt.Sprintf("refs/mcode/swarm-base/%s", leadPrefix)
	c, err := m.opts.Git.Checkpoint(ctx, dir, ref, "mcode swarm base checkpoint")
	if err != nil {
		c, err = m.opts.Git.RevParse(ctx, dir, "HEAD")
		if err != nil {
			return "", fmt.Errorf("checkpoint and rev-parse HEAD failed for %s: %w", dir, err)
		}
	}
	return c, nil
}

func (m *Manager) startSingleBee(ctx context.Context, leadThreadID, beeProjectID, beeWorkDir string, spec BeeSpec, index int, leadPrefix string) (Bee, error) {
	var tc ThreadContext
	if m.opts.ThreadContext != nil {
		tc = m.opts.ThreadContext(leadThreadID)
	}
	settings := m.Settings()
	provider, model, effort, err := m.effectiveBeeRuntime(spec.Provider, spec.Model, spec.Effort, settings, tc)
	if err != nil {
		return Bee{}, err
	}
	var shadowRepo bool
	var sharedNoGit bool
	var sharedNoGitReason string

	isGit := false
	if m.opts.Git != nil {
		// 这里要判断的是「项目本身是否真实 git 仓库」；带 ShadowRoot 的 Runner 用 RepoRoot 探测时，
		// 影子仓库已存在的普通目录也会返回成功，会让 bee 漏掉 ShadowRepo 标记。
		if m.opts.Git.IsRealRepo(ctx, beeWorkDir) {
			isGit = true
		}
	}

	if !isGit {
		if m.opts.Git != nil && m.opts.Git.ShadowRoot != "" {
			if _, err := m.opts.Git.EnsureShadow(ctx, beeWorkDir); err == nil {
				shadowRepo = true
			} else {
				sharedNoGit = true
				sharedNoGitReason = fmt.Sprintf("项目不是 Git 仓库，且影子仓库不可用：%v", err)
				log.Printf("swarm bee %d: %s", index, sharedNoGitReason)
			}
		} else {
			sharedNoGit = true
			if m.opts.Git == nil {
				sharedNoGitReason = "项目不是 Git 仓库，且影子仓库不可用：未配置 Git"
			} else {
				sharedNoGitReason = "项目不是 Git 仓库，且影子仓库不可用：未配置影子仓库根目录"
			}
			log.Printf("swarm bee %d: %s", index, sharedNoGitReason)
		}
	}

	// 写入白名单不为空的 bee 与只读 bee 一样不能走共享工作区，否则无法过滤白名单外的改动。
	isShared := (settings.WorkspaceMode == "shared" && !spec.ReadOnly && len(spec.WritePaths) == 0) || sharedNoGit

	var wtPath, branch, baseCommit string
	// reusedWorktree 表示复用了上一个子代理失败时保留的工作副本：这种副本不能在建会话失败时删掉，
	// 否则半成品就没了。
	reusedWorktree := false
	if !isShared {
		if spec.reuseWorktree != "" {
			// 重试接管半成品：跳过基线检查点与 worktree add，直接在保留的 worktree 上继续。
			if _, statErr := os.Stat(spec.reuseWorktree); statErr == nil {
				wtPath = spec.reuseWorktree
				branch = spec.reuseBranch
				baseCommit = spec.reuseBaseCommit
				reusedWorktree = true
			}
		}
	}
	if !isShared && !reusedWorktree {
		var err error
		baseCommit, err = m.getBaseCommit(ctx, beeWorkDir, leadPrefix)
		if err != nil {
			return Bee{}, err
		}

		wtPath = filepath.Join(m.opts.WorktreeDir, fmt.Sprintf("%s-%d", leadPrefix, index))
		if abs, err := filepath.Abs(wtPath); err == nil {
			wtPath = abs
		}
		branch = fmt.Sprintf("mcode/swarm/%s/%d", leadPrefix, index)

		if err := m.opts.Git.WorktreeAdd(ctx, beeWorkDir, wtPath, branch, baseCommit); err != nil {
			_ = m.opts.Git.BranchDelete(ctx, beeWorkDir, branch, true)
			_ = os.RemoveAll(wtPath)
			return Bee{}, fmt.Errorf("worktree add failed for bee %d: %w", index, err)
		}
	}

	sessionWorkDir := wtPath
	if isShared {
		sessionWorkDir = beeWorkDir
	}

	// 解析 bee 实际使用的提供方与模型：派发指令没有指定时回退到设置里的默认值。
	// 推理强度：派发指令与配对/档位都没指定，且这次实际用的就是默认 provider 和 model 时，
	// 回退到设置里的默认推理强度；配对与档位已有的优先级不变。规则与 TierPreview 共用。
	sessionReq := kanban.SessionRequest{
		LeadThreadID: leadThreadID,
		ProjectID:    beeProjectID,
		WorkDir:      sessionWorkDir,
		Title:        spec.Name,
		Prompt:       spec.Prompt,
		Provider:     provider,
		Model:        model,
		Effort:       effort,
		FastMode:     spec.FastMode,
		// 宿主指定的权限模式优先；为空时照旧继承主线程的运行模式。
		RuntimeMode: spec.RuntimeMode,
	}
	if spec.ReadOnly {
		sessionReq.RuntimeMode = string(harness.ModeReadOnly)
	}

	if !isShared {
		m.mu.Lock()
		m.startingWorkDirs[filepath.Clean(wtPath)] = workDirOwner{
			leadThreadID: leadThreadID,
			projectID:    beeProjectID,
		}
		m.mu.Unlock()
		defer func(path string) {
			m.mu.Lock()
			delete(m.startingWorkDirs, path)
			m.mu.Unlock()
		}(filepath.Clean(wtPath))
	}

	threadID, err := m.opts.Driver.StartSession(ctx, sessionReq)
	if err != nil {
		// 复用的 worktree 里是上一个子代理的半成品，启动失败也要保留现场。
		if !isShared && !reusedWorktree {
			_ = m.opts.Git.WorktreeRemove(ctx, beeWorkDir, wtPath, true)
			_ = m.opts.Git.BranchDelete(ctx, beeWorkDir, branch, true)
			_ = os.RemoveAll(wtPath)
		}
		return Bee{}, fmt.Errorf("start session failed for bee %d: %w", index, err)
	}

	if leadThreadID != "" {
		if s, ok := m.opts.Driver.(ThreadRelationSetter); ok {
			s.SetThreadRelation(threadID, leadThreadID, true)
		} else if s, ok := m.opts.Driver.(ThreadRelationContextSetter); ok {
			_ = s.SetThreadRelation(ctx, threadID, leadThreadID, true)
		}
		if s, ok := m.opts.Persister.(ThreadRelationContextSetter); ok {
			_ = s.SetThreadRelation(ctx, threadID, leadThreadID, true)
		} else if s, ok := m.opts.Persister.(ThreadRelationSetter); ok {
			s.SetThreadRelation(threadID, leadThreadID, true)
		}
	}

	now := m.now()
	bee := Bee{
		ThreadID:          threadID,
		Index:             index,
		Name:              spec.Name,
		Task:              spec.Task,
		Prompt:            spec.Prompt,
		Status:            StatusRunning,
		StartedAt:         now,
		LastProgressAt:    now,
		Provider:          provider,
		Model:             model,
		Effort:            effort,
		Profile:           spec.Profile,
		Tier:              spec.Tier,
		ReadOnly:          spec.ReadOnly,
		WritePaths:        append([]string(nil), spec.WritePaths...),
		RuntimeMode:       sessionReq.RuntimeMode,
		DirectModel:       spec.DirectModel,
		FastMode:          spec.FastMode,
		WorktreePath:      wtPath,
		Branch:            branch,
		BaseCommit:        baseCommit,
		ConflictFiles:     []string{},
		ProjectID:         beeProjectID,
		TargetWorkDir:     beeWorkDir,
		Reported:          false,
		ShadowRepo:        shadowRepo,
		SharedNoGit:       sharedNoGit,
		SharedNoGitReason: sharedNoGitReason,
		ReworkOf:          spec.ReworkOf,
		ReworkPatchPath:   spec.ReworkPatchPath,
		ReworkReason:      spec.ReworkReason,
	}
	return bee, nil
}

// queuedLeadIDs 返回一批排队条目涉及的 leadThreadID，按首次出现顺序去重。
func queuedLeadIDs(items []queuedBee) []string {
	seen := make(map[string]bool, len(items))
	ids := make([]string, 0, len(items))
	for _, q := range items {
		if seen[q.leadThreadID] {
			continue
		}
		seen[q.leadThreadID] = true
		ids = append(ids, q.leadThreadID)
	}
	return ids
}

func (m *Manager) processQueue(ctx context.Context) {
	m.dispatchMu.Lock()
	defer m.dispatchMu.Unlock()

	maxBees := m.maxBeesLimit()

	// 遍历整个队列：只启动所属 Lead 还有空位的条目，满额的条目留在队列里等下一轮，
	// 不在第一条满额时 break，否则一个 Lead 的排队会挡住其它 Lead 已经可以启动的条目。
	m.mu.Lock()
	var ready []queuedBee
	var remaining []queuedBee
	perLead := make(map[string]int, len(m.leads))
	for _, q := range m.queue {
		count, ok := perLead[q.leadThreadID]
		if !ok {
			count = m.runningCountForLeadLocked(q.leadThreadID)
		}
		if count >= maxBees {
			remaining = append(remaining, q)
			continue
		}
		// 同一个 Lead 本轮已放行的条目也计入，避免同一轮越过上限。
		perLead[q.leadThreadID] = count + 1
		ready = append(ready, q)
	}
	m.queue = remaining
	m.mu.Unlock()

	// 出队同样要落盘：否则重启后恢复出的排队条目会比内存里多。
	for _, leadID := range queuedLeadIDs(ready) {
		m.persistLead(leadID)
	}

	// 已经决定派出的条目照常出队，不再因为编排配置在这一轮被关掉就永远滞留在队列里。
	for _, q := range ready {
		leadPrefix := q.leadThreadID
		if len(leadPrefix) > 8 {
			leadPrefix = leadPrefix[:8]
		}

		bee, err := m.startSingleBee(ctx, q.leadThreadID, q.projectID, q.leadWorkDir, q.spec, q.index, leadPrefix)
		if err != nil {
			log.Printf("swarm: start queued bee %d failed: %v", q.index, err)
			// 启动失败也要留一条失败记录并汇报给 Lead，否则批次永远凑不齐。
			m.recordFailedBee(q.leadThreadID, q.projectID, q.leadWorkDir, q.spec, q.index, err)
			continue
		}

		m.mu.Lock()
		lead, ok := m.leads[q.leadThreadID]
		if ok {
			lead.bees = append(lead.bees, bee)
		}
		m.mu.Unlock()
		m.notifyChange(q.leadThreadID)
	}
}

// recordFailedBee 为启动失败的委派条目追加一条失败 Bee 记录并通知变更，
// 让 checkReports 照常把「启动失败」汇报给 Lead。
func (m *Manager) recordFailedBee(leadThreadID, projectID, targetWorkDir string, spec BeeSpec, index int, startErr error) {
	now := m.now()
	summary := ""
	if startErr != nil {
		summary = formatFailureReason("启动失败：" + startErr.Error())
	}
	bee := Bee{
		Index:         index,
		Name:          spec.Name,
		Task:          spec.Task,
		Status:        StatusFailed,
		Summary:       summary,
		StartedAt:     now,
		FinishedAt:    &now,
		ProjectID:     projectID,
		TargetWorkDir: targetWorkDir,
		ConflictFiles: []string{},
		Reported:      false,
	}

	m.mu.Lock()
	if lead, ok := m.leads[leadThreadID]; ok {
		lead.bees = append(lead.bees, bee)
	}
	m.mu.Unlock()
	m.notifyChange(leadThreadID)
}

// Dispatch 批量创建独立 worktree 与分支并启动子会话；单条启动失败只追加一条失败记录，
// 已启动的 bee 不回滚，其余条目照常派发。
func (m *Manager) Dispatch(ctx context.Context, req DispatchRequest) ([]Bee, error) {
	m.dispatchMu.Lock()
	defer m.dispatchMu.Unlock()
	return m.dispatch(ctx, req)
}

func (m *Manager) dispatch(ctx context.Context, req DispatchRequest) ([]Bee, error) {
	if !m.Settings().Enabled {
		return nil, errors.New("swarm: disabled")
	}
	if m.opts.Git == nil {
		return nil, errors.New("swarm: git runner is nil")
	}
	if m.opts.Driver == nil {
		return nil, errors.New("swarm: session driver is nil")
	}

	if m.opts.WorktreeDir != "" {
		if err := os.MkdirAll(m.opts.WorktreeDir, 0755); err != nil {
			return nil, fmt.Errorf("create worktree dir: %w", err)
		}
	}

	leadPrefix := req.LeadThreadID
	if len(leadPrefix) > 8 {
		leadPrefix = leadPrefix[:8]
	}

	m.mu.Lock()
	lead, ok := m.leads[req.LeadThreadID]
	if !ok {
		lead = &leadState{
			leadThreadID: req.LeadThreadID,
			enabled:      true,
			projectID:    req.ProjectID,
			leadWorkDir:  req.LeadWorkDir,
			bees:         []Bee{},
			nextIndex:    0,
		}
		m.leads[req.LeadThreadID] = lead
	} else {
		lead.projectID = req.ProjectID
		lead.leadWorkDir = req.LeadWorkDir
	}
	if len(req.Bees) > 0 {
		lead.stoppedByUser = false
	}
	lead.dispatching = true
	indices := make([]int, len(req.Bees))
	for i, spec := range req.Bees {
		indices[i] = lead.nextIndex
		if spec.index != nil {
			indices[i] = *spec.index
		}
		lead.nextIndex++
	}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		lead.dispatching = false
		m.mu.Unlock()
	}()

	var createdBees []Bee
	// 启动失败不再回滚已启动的 bee：失败条目改为追加一条失败记录并继续派后面的条目，
	// 已启动的 bee 照常在主工作区里工作。（原先整批 cleanupAll 会把已跑起来的 bee 全部停掉。）
	var startErrs []error

	for i, spec := range req.Bees {
		beeWorkDir := spec.WorkDir
		if beeWorkDir == "" {
			beeWorkDir = req.LeadWorkDir
		}
		beeProjectID := spec.ProjectID
		if beeProjectID == "" {
			beeProjectID = req.ProjectID
		}
		index := indices[i]

		maxBees := m.maxBeesLimit()

		m.mu.Lock()
		// 按 Lead 比较并发：一个 Lead 满额只让本条排队，不影响其它 Lead 的派发。
		if m.runningCountForLeadLocked(req.LeadThreadID) >= maxBees {
			m.queue = append(m.queue, queuedBee{
				leadThreadID: req.LeadThreadID,
				projectID:    beeProjectID,
				leadWorkDir:  beeWorkDir,
				spec:         spec,
				index:        index,
			})
			m.mu.Unlock()
			// 入队后立即落盘，重启后才有可能把这条未启动的委派交回用户。
			m.persistLead(req.LeadThreadID)
			continue
		}
		m.mu.Unlock()

		bee, err := m.startSingleBee(ctx, req.LeadThreadID, beeProjectID, beeWorkDir, spec, index, leadPrefix)
		if err != nil {
			log.Printf("swarm: start bee %d failed: %v", index, err)
			m.recordFailedBee(req.LeadThreadID, beeProjectID, beeWorkDir, spec, index, err)
			startErrs = append(startErrs, err)
			continue
		}

		m.mu.Lock()
		lead = m.leads[req.LeadThreadID]
		lead.bees = append(lead.bees, bee)
		m.mu.Unlock()

		createdBees = append(createdBees, bee)
		m.notifyChange(req.LeadThreadID)
	}

	m.mu.Lock()
	lead = m.leads[req.LeadThreadID]
	lead.dispatching = false
	m.mu.Unlock()

	m.notifyChange(req.LeadThreadID)

	// 一条也没启动起来时仍然返回错误：调用方靠 err 提示用户（单条委派的调用方还会直接取
	// bees[0]）。有任意一条启动成功时返回已启动的 bees 与 nil，失败原因已写进失败记录的 Summary。
	if len(createdBees) == 0 && len(startErrs) > 0 {
		return nil, startErrs[0]
	}

	result := make([]Bee, len(createdBees))
	for i, h := range createdBees {
		result[i] = h.clone()
	}
	return result, nil
}

// ReplyDispatchRequest 描述根据主会话回复文本派发子任务的请求。
type ReplyDispatchRequest struct {
	LeadThreadID string
	ProjectID    string
	LeadWorkDir  string
	ReplyText    string
}

// HostOverride 描述宿主代码直接给定的派发覆盖项：非空字段覆盖档位解析结果。
// 它由宿主代码给出，可信，不经 AuthorizeDirectModel 校验（那一条要求用户在消息里点名模型）；
// nil 表示普通的 lead 流式派发，不做任何覆盖。
type HostOverride struct {
	Provider string
	Model    string
	Effort   string
	// RuntimeMode 非空时以该权限模式创建子代理会话，不继承主线程的权限模式；
	// 例如只读的总管线程派方案子代理时必须显式指定可写模式。
	RuntimeMode string
	WritePaths  []string
}

// SingleEntryRequest 描述流式解析出的单条委派条目的即时派发请求。
type SingleEntryRequest struct {
	LeadThreadID string
	ProjectID    string
	LeadWorkDir  string
	Entry        Delegation
	// Host 非 nil 时按宿主指定的 provider/model/effort 与写入白名单覆盖档位解析结果。
	Host *HostOverride
}

// entryPlan 描述单个委派条目解析后的派发规格与需要告知 Lead 的提示。
type entryPlan struct {
	spec    BeeSpec
	notices []string
	seqIdx  int
}

// entryPlanState 读取 Lead 当前状态并填充默认项目与工作区，返回名字占用表与顺序分配游标。
func (m *Manager) entryPlanState(leadThreadID, projectID, leadWorkDir string) (string, string, map[int]BeeStatus, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	taken := make(map[int]BeeStatus)
	seqIdx := 0
	lead, ok := m.leads[leadThreadID]
	if !ok {
		return projectID, leadWorkDir, taken, seqIdx
	}
	if leadWorkDir == "" {
		leadWorkDir = lead.leadWorkDir
	}
	if projectID == "" {
		projectID = lead.projectID
	}
	if len(lead.bees) == 0 {
		lead.nextIndex = 0
	} else {
		seqIdx = lead.nextIndex
		for _, h := range lead.bees {
			taken[h.Index] = h.Status
		}
	}
	return projectID, leadWorkDir, taken, seqIdx
}

// planEntry 按现有派发规则为单个条目分配花名册名字与目标项目，供批量与流式派发共用。
// host 非 nil 时覆盖档位解析出的 provider/model/effort 与写入白名单。
func (m *Manager) planEntry(ctx context.Context, leadThreadID string, d Delegation, host *HostOverride, baseProjectID, baseWorkDir string, taken map[int]BeeStatus, seqIdx int) entryPlan {
	index, pinned := RosterIndex(d.Name)
	if pinned {
		for status, exists := taken[index]; exists && status != StatusRunning; status, exists = taken[index] {
			index += len(Roster)
		}
		_, occupied := taken[index]
		pinned = !occupied
	}
	if !pinned {
		index = seqIdx
		for {
			if _, occupied := taken[index]; !occupied {
				break
			}
			index++
		}
		seqIdx = index
	}
	seqIdx++
	taken[index] = StatusRunning
	chosenName := RosterName(index)

	plan := entryPlan{seqIdx: seqIdx}
	targetProjectID := baseProjectID
	targetWorkDir := baseWorkDir
	if d.Project != "" {
		if m.opts.ResolveProject != nil {
			projID, workDir, ok := m.opts.ResolveProject(ctx, d.Project)
			if ok {
				if projID != baseProjectID {
					targetProjectID = projID
					targetWorkDir = workDir
				}
			} else {
				plan.notices = append(plan.notices, fmt.Sprintf("未找到项目 %q，子代理 %s 留在当前项目", d.Project, chosenName))
			}
		} else {
			plan.notices = append(plan.notices, fmt.Sprintf("未找到项目 %q，子代理 %s 留在当前项目", d.Project, chosenName))
		}
	}

	var tc ThreadContext
	if m.opts.ThreadContext != nil {
		tc = m.opts.ThreadContext(leadThreadID)
	}

	// 解析配对与 Profile：Provider/Model/Effort/Profile 由 ResolveBeeConfig 逐层覆盖得出。
	profiles, err := LoadProfiles(m.profilesPath())
	if err != nil {
		plan.notices = append(plan.notices, fmt.Sprintf("读取子代理 Profile 失败：%v", err))
		profiles = nil
	}
	cfg, resolveNotices := ResolveBeeConfig(ResolveInput{
		NormalizeBee:    m.opts.NormalizeBee,
		Delegation:      d,
		Profiles:        profiles,
		Pairs:           m.Settings().Pairs,
		Tiers:           m.Settings().Tiers,
		DefaultTier:     m.Settings().DefaultTier,
		PairID:          tc.PairID,
		ThreadOverrides: tc.BeeTiers,
		UserMessage:     tc.UserMessage,
		ProviderEnabled: tc.ProviderEnabled,

		SessionBeeProvider: tc.BeeProvider,
		SessionBeeModel:    tc.BeeModel,
		SessionBeeEffort:   tc.BeeEffort,
	})
	plan.notices = append(plan.notices, resolveNotices...)

	// 宿主代码给的覆盖项在档位解析之后生效：只覆盖非空字段，DirectModel 保持原值。
	if host != nil {
		provider, model := host.Provider, host.Model
		if m.opts.NormalizeBee != nil {
			provider, model = m.opts.NormalizeBee(provider, model)
		}
		applyBeeFields(&cfg, provider, model, host.Effort)
		if len(host.WritePaths) > 0 {
			cfg.WritePaths = append([]string(nil), host.WritePaths...)
		}
	}
	if m.opts.NormalizeBee != nil {
		cfg.Provider, cfg.Model = m.opts.NormalizeBee(cfg.Provider, cfg.Model)
	}

	// 只有声明了 skills 的 bee 才追加技能提示块；Delegation.Prompt 保持不变（流式派发的去重键依赖它）。
	prompt := beePrompt(chosenName, d, cfg)
	if m.opts.SkillsGuidance != nil && len(d.Skills) > 0 {
		if block := m.opts.SkillsGuidance(targetWorkDir, d.Skills); strings.TrimSpace(block) != "" {
			prompt += "\n\n" + block
		}
	}
	// 派发前检索相关长期记忆：钩子自带超时预算，失败与空结果都静默跳过。
	if recall := m.memoryRecallHook(); recall != nil {
		if block := beeRecallBlock(ctx, recall, targetWorkDir, d); block != "" {
			prompt += "\n\n" + block
		}
	}

	plan.spec = BeeSpec{
		index:       &index,
		Name:        chosenName,
		Task:        d.Task,
		Prompt:      prompt,
		ProjectID:   targetProjectID,
		WorkDir:     targetWorkDir,
		Provider:    cfg.Provider,
		Model:       cfg.Model,
		Effort:      cfg.Effort,
		FastMode:    cfg.FastMode,
		Profile:     cfg.Profile,
		Tier:        cfg.Tier,
		ReadOnly:    cfg.ReadOnly,
		WritePaths:  append([]string(nil), cfg.WritePaths...),
		DirectModel: cfg.DirectModel,
	}
	// 返工关联：显式 reworkOf 按名字或 ThreadID 匹配被驳回的 bee；没写时按 brief 里的
	// 补丁路径推断。找不到就留空，按普通 bee 走完整审查。
	if old, ok := m.reworkTarget(leadThreadID, d.ReworkOf, d.Prompt); ok {
		plan.spec.ReworkOf = old.ThreadID
		plan.spec.ReworkPatchPath = old.ReviewPatchPath
		plan.spec.ReworkReason = old.ReviewReason
	}
	// 宿主指定的权限模式放在最后赋值：它要覆盖 spec 里其他字段的默认构造顺序，
	// 非空时子代理不再继承主线程的权限模式。
	if host != nil {
		plan.spec.RuntimeMode = host.RuntimeMode
	}
	return plan
}

// reworkTarget 找出本条派发要返工的被驳回 bee：显式 reworkOf 按 ThreadID 或名字匹配；
// 为空时看 brief 是否包含某个已驳回 bee 的补丁路径，多个命中取最新的那个。
// 只匹配 ReviewState==ReviewRejected 的 bee；找不到返回 false，不报错。
func (m *Manager) reworkTarget(leadThreadID, reworkOf, prompt string) (Bee, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		return Bee{}, false
	}
	reworkOf = strings.TrimSpace(reworkOf)
	var best Bee
	var bestTime time.Time
	found := false
	for i := range lead.bees {
		b := &lead.bees[i]
		if b.ReviewState != ReviewRejected {
			continue
		}
		matched := false
		if reworkOf != "" {
			matched = b.ThreadID == reworkOf || b.Name == reworkOf
		} else if b.ReviewPatchPath != "" {
			matched = strings.Contains(prompt, b.ReviewPatchPath)
		}
		if !matched {
			continue
		}
		at := b.StartedAt
		if b.FinishedAt != nil {
			at = *b.FinishedAt
		}
		if !found || at.After(bestTime) {
			best = b.clone()
			bestTime = at
			found = true
		}
	}
	return best, found
}

// beeBudgetReached 判断该 Lead 自本次请求开始后已启动的 bee 是否达到 DefaultBeeBudget 上限。
func (m *Manager) beeBudgetReached(leadThreadID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		return false
	}
	return m.beeBudgetUsageLocked(lead) >= DefaultBeeBudget
}

// beeBudgetUsageLocked 统计该 Lead 自本次请求开始后已启动的 bee 数，排队中的条目同样计入。
// 调用方需持有 m.mu。
func (m *Manager) beeBudgetUsageLocked(lead *leadState) int {
	count := 0
	for _, h := range lead.bees {
		if !h.StartedAt.Before(lead.requestStartedAt) {
			count++
		}
	}
	// 排队中的条目还没有 StartedAt，但一样占本次请求的预算；不统计它们，
	// 超出并发上限的派发就会经队列绕过 DefaultBeeBudget。
	for _, q := range m.queue {
		if q.leadThreadID == lead.leadThreadID {
			count++
		}
	}
	return count
}

// RequestLimits 返回本次用户请求的编排上限快照，供提示词向 Lead 说明可用预算。
// maxRounds 由调用方传入实际生效的派发轮次上限，0 表示不限制。
func (m *Manager) RequestLimits(leadThreadID string, maxRounds int) RequestLimits {
	limits := RequestLimits{
		MaxRounds:     maxRounds,
		MaxConcurrent: m.maxBeesLimit(),
		BeeBudget:     DefaultBeeBudget,
		Timeout:       m.requestTimeoutLimit(),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		return limits
	}
	limits.RoundsUsed = lead.delegationRounds
	limits.BeesStarted = m.beeBudgetUsageLocked(lead)
	if !lead.requestStartedAt.IsZero() {
		limits.Elapsed = m.now().Sub(lead.requestStartedAt)
	}
	return limits
}

// roundLimitError 判断该 Lead 是否已达配置的派发轮次上限（Settings.MaxRounds，0 表示不限制）：
// 达到上限时返回包装 ErrMaxRounds 的错误，否则返回 nil。counted 表示当前轮次已经计入 delegationRounds，
// 流式路径在派发前先记轮次，因此按已派发轮次达到上限判断。
func (m *Manager) roundLimitError(leadThreadID string, counted bool) error {
	limit := m.Settings().MaxRounds
	if limit <= 0 {
		return nil
	}
	m.mu.Lock()
	rounds := 0
	if lead, ok := m.leads[leadThreadID]; ok {
		rounds = lead.delegationRounds
	}
	m.mu.Unlock()

	dispatched := rounds
	if counted {
		dispatched = rounds - 1
	}
	if dispatched < limit {
		return nil
	}
	return fmt.Errorf("%w（%d 轮）", ErrMaxRounds, limit)
}

// DispatchSingleEntry 立即派发流式解析出的单条委派条目；与 DispatchFromReply 共用派发锁，保证并发调用按顺序分派。
// 配置未启用或未开启自动委派时返回零值 Bee 与 nil 错误（req.Host 非 nil 时返回 ErrSwarmDisabled）；本次请求已超过总时长预算时返回 ErrRequestTimeout；
// 已达配置的派发轮次上限时返回 ErrMaxRounds；已启动 bee 达到预算时返回 ErrBeeBudget。
func (m *Manager) DispatchSingleEntry(ctx context.Context, req SingleEntryRequest) (Bee, []string, error) {
	m.dispatchMu.Lock()
	defer m.dispatchMu.Unlock()
	settings := m.Settings()
	if !settings.Enabled || !settings.AutoDelegate {
		// 宿主显式指定了覆盖项的派发必须拿到明确结果，不能静默当成没派成功。
		if req.Host != nil {
			return Bee{}, nil, ErrSwarmDisabled
		}
		return Bee{}, nil, nil
	}
	// 总时长检查排在轮次检查之前：超时后派发一律拒绝，管理器这一层也不把它算作一轮。
	if m.requestTimedOut(req.LeadThreadID) {
		return Bee{}, nil, fmt.Errorf("%w（%s）", ErrRequestTimeout, formatBudgetDuration(m.requestTimeoutLimit()))
	}
	if err := m.roundLimitError(req.LeadThreadID, true); err != nil {
		return Bee{}, nil, err
	}
	if m.beeBudgetReached(req.LeadThreadID) {
		return Bee{}, nil, ErrBeeBudget
	}

	projectID, leadWorkDir, taken, seqIdx := m.entryPlanState(req.LeadThreadID, req.ProjectID, req.LeadWorkDir)
	plan := m.planEntry(ctx, req.LeadThreadID, req.Entry, req.Host, projectID, leadWorkDir, taken, seqIdx)

	createdBees, err := m.dispatch(ctx, DispatchRequest{
		LeadThreadID: req.LeadThreadID,
		ProjectID:    projectID,
		LeadWorkDir:  leadWorkDir,
		Bees:         []BeeSpec{plan.spec},
	})
	if err != nil {
		return Bee{}, plan.notices, err
	}

	m.mu.Lock()
	if lead, ok := m.leads[req.LeadThreadID]; ok {
		lead.nextIndex = plan.seqIdx
	}
	m.mu.Unlock()
	m.persistLead(req.LeadThreadID)

	var bee Bee
	if len(createdBees) > 0 {
		bee = createdBees[0]
	}
	return bee, plan.notices, nil
}

// DispatchFromReply 解析主会话回复中的委派块，完成名字分配与项目解析并派发子会话。
func (m *Manager) DispatchFromReply(ctx context.Context, req ReplyDispatchRequest) (bees []Bee, notices []string, err error) {
	m.dispatchMu.Lock()
	defer m.dispatchMu.Unlock()
	if !m.Settings().Enabled || !m.Settings().AutoDelegate {
		return nil, nil, nil
	}
	entries, found, err := ParseDelegations(req.ReplyText)
	if err != nil {
		return nil, nil, err
	}
	if !found || len(entries) == 0 {
		return nil, nil, nil
	}
	if err := m.roundLimitError(req.LeadThreadID, false); err != nil {
		return nil, nil, err
	}

	projectID, leadWorkDir, taken, seqIdx := m.entryPlanState(req.LeadThreadID, req.ProjectID, req.LeadWorkDir)

	beeSpecs := make([]BeeSpec, 0, len(entries))
	for _, d := range entries {
		plan := m.planEntry(ctx, req.LeadThreadID, d, nil, projectID, leadWorkDir, taken, seqIdx)
		seqIdx = plan.seqIdx
		notices = append(notices, plan.notices...)
		beeSpecs = append(beeSpecs, plan.spec)
	}

	createdBees, err := m.dispatch(ctx, DispatchRequest{
		LeadThreadID: req.LeadThreadID,
		ProjectID:    projectID,
		LeadWorkDir:  leadWorkDir,
		Bees:         beeSpecs,
	})
	if err != nil {
		return nil, notices, err
	}
	m.mu.Lock()
	if lead, ok := m.leads[req.LeadThreadID]; ok {
		lead.nextIndex = seqIdx
	}
	m.mu.Unlock()
	m.persistLead(req.LeadThreadID)
	return createdBees, notices, nil
}

type runningTarget struct {
	leadThreadID  string
	targetWorkDir string
	bee           Bee
}

// beeUsage 读取指定 bee 会话的累计用量：tokens 为输入+输出 token（会话用量面板同口径），
// toolCalls 为工具调用次数。驱动不支持用量读取或读取失败时返回 ok=false，保持记录原值。
func (m *Manager) beeUsage(ctx context.Context, threadID string) (tokens, toolCalls int, ok bool) {
	reader, isReader := m.opts.Driver.(kanban.SessionUsageReader)
	if !isReader {
		return 0, 0, false
	}
	tokens, toolCalls, err := reader.SessionUsage(ctx, threadID)
	if err != nil {
		return 0, 0, false
	}
	return tokens, toolCalls, true
}

// Tick 执行一次单轮调度检查，对处于 running 的 bee 检测会话状态并串行触发自动合并与结果汇报。
func (m *Manager) Tick(ctx context.Context) {
	m.tickMu.Lock()
	defer m.tickMu.Unlock()

	if m.opts.Driver == nil || m.opts.Git == nil {
		return
	}

	// 请求总时长预算：Lead 本次请求超过上限仍未结束时，停止其全部 running bee。
	requestTimeout := m.requestTimeoutLimit()
	timeoutSummary := fmt.Sprintf("看门狗：本次请求已超过总时长预算（%s），已停止；用户发送新消息后重新计时", formatBudgetDuration(requestTimeout))
	type timedOutLead struct {
		leadThreadID string
		threadIDs    []string
	}
	var timedOut []timedOutLead
	now := m.now()
	m.mu.Lock()
	for leadID, lead := range m.leads {
		if lead.requestStartedAt.IsZero() || now.Sub(lead.requestStartedAt) < requestTimeout {
			continue
		}
		var threadIDs []string
		for i := range lead.bees {
			h := &lead.bees[i]
			if h.Status != StatusRunning {
				continue
			}
			h.Status = StatusFailed
			h.FinishedAt = &now
			h.MergeWaitSince = nil
			h.Summary = timeoutSummary
			h.MergeError = timeoutSummary
			h.Merged = false
			delete(m.beeProgress, h.ThreadID)
			threadIDs = append(threadIDs, h.ThreadID)
		}
		if len(threadIDs) > 0 {
			timedOut = append(timedOut, timedOutLead{leadThreadID: leadID, threadIDs: threadIDs})
		}
	}
	m.mu.Unlock()
	for _, t := range timedOut {
		if m.opts.StopSession != nil {
			for _, id := range t.threadIDs {
				_ = m.opts.StopSession(ctx, id)
			}
		}
		m.notifyChange(t.leadThreadID)
	}

	var targets []runningTarget
	m.mu.Lock()
	for leadID, lead := range m.leads {
		for _, h := range lead.bees {
			if h.Status == StatusRunning {
				targetDir := h.TargetWorkDir
				if targetDir == "" {
					targetDir = lead.leadWorkDir
				}
				targets = append(targets, runningTarget{
					leadThreadID:  leadID,
					targetWorkDir: targetDir,
					bee:           h.clone(),
				})
			}
		}
	}
	m.mu.Unlock()

	changedLeads := make(map[string]struct{})
	// progressLeads 收集只涉及进度（思考分钟数、无进展时长）的 lead：它们走合并落盘通道，
	// 避免每个 tick 对每个运行中的 bee 都做一次全量落盘与全量状态推送。
	progressLeads := make(map[string]struct{})
	// deletedBeeIDs 收集底层线程已被删除的 bee，等这轮轮询结束后在锁外统一清理。
	var deletedBeeIDs []string
	// reviewStarts 不再需要：ai 档的 L0/L1/L2 由审查漏斗 worker 异步推进。

	for _, tgt := range targets {
		st, err := m.opts.Driver.SessionState(ctx, tgt.bee.ThreadID)
		// bee 线程已被删除：不再当成仍在运行继续计看门狗时长，先记下，等这轮遍历结束后
		// 在锁外交给 BeeThreadDeleted 清理并补一次汇报。
		if errors.Is(err, store.ErrNotFound) {
			deletedBeeIDs = append(deletedBeeIDs, tgt.bee.ThreadID)
			continue
		}
		if err != nil || st.Running || st.AwaitingApproval || st.AwaitingInput {
			now := m.now()
			runTimeout := m.BeeTimeout()
			if runTimeout <= 0 {
				runTimeout = DefaultBeeTimeout
			}
			// 卡死检测不随运行上限放宽：运行上限再长，无输出且无改动超过 maxNoEditStallTimeout 也判卡死。
			noEditStallTimeout := runTimeout
			if noEditStallTimeout > maxNoEditStallTimeout {
				noEditStallTimeout = maxNoEditStallTimeout
			}
			stallTimeout := m.opts.StallTimeout
			if stallTimeout <= 0 {
				stallTimeout = 10 * time.Minute
			}
			if stallTimeout < noEditStallTimeout {
				stallTimeout = noEditStallTimeout
			}
			thinkingAfter := m.opts.ThinkingAfter
			if thinkingAfter <= 0 {
				thinkingAfter = 2 * time.Minute
			}

			// 累计用量（tokens、工具调用数）只能从驱动/后端拿：在锁外取数，
			// 避免持 m.mu 调后端（后端会回调 Manager，锁序相反）。
			usageTokens, usageToolCalls, usageOK := 0, 0, false
			if err == nil && st.Running {
				usageTokens, usageToolCalls, usageOK = m.beeUsage(ctx, tgt.bee.ThreadID)
			}

			m.mu.Lock()
			var currentBee *Bee
			if lead, ok := m.leads[tgt.leadThreadID]; ok {
				for i := range lead.bees {
					if lead.bees[i].ThreadID == tgt.bee.ThreadID {
						currentBee = &lead.bees[i]
						break
					}
				}
			}
			if currentBee == nil || currentBee.Status != StatusRunning {
				m.mu.Unlock()
				continue
			}

			// 用量变化写回记录并当作进度变化，随进度合并落盘进 heads。
			usageChanged := false
			if usageOK && (currentBee.Tokens != usageTokens || currentBee.ToolCalls != usageToolCalls) {
				currentBee.Tokens = usageTokens
				currentBee.ToolCalls = usageToolCalls
				usageChanged = true
			}

			// 文件改动时刻晚于已记录值时更新，并把该 lead 计入进度落盘（不改变 LastProgressAt 语义）。
			if st.LastFileChangeAt.After(currentBee.LastFileChangeAt) {
				currentBee.LastFileChangeAt = st.LastFileChangeAt
				progressLeads[tgt.leadThreadID] = struct{}{}
			}

			if currentBee.LastProgressAt.IsZero() {
				if !currentBee.StartedAt.IsZero() {
					currentBee.LastProgressAt = currentBee.StartedAt
				} else {
					currentBee.LastProgressAt = now
				}
			}

			// bee 正在等用户回答问题或等主代理回复：等待期间不计入看门狗，思考时间归零并跳过卡死判定，
			// 否则用户思考期间 bee 会被当成无进展杀掉。
			if err == nil && (st.AwaitingApproval || st.AwaitingInput) {
				currentBee.LastProgressAt = now
				// 等待时长按相邻两次都在等待的轮询间隔累计，不计入运行上限。
				if !currentBee.waitTickAt.IsZero() {
					currentBee.WaitedFor += now.Sub(currentBee.waitTickAt)
				}
				currentBee.waitTickAt = now
				if currentBee.ThinkingMinutes != 0 {
					currentBee.ThinkingMinutes = 0
					progressLeads[tgt.leadThreadID] = struct{}{}
				}
				m.mu.Unlock()
				continue
			}

			// 恢复运行：只清掉等待标记，等待时长只按相邻两次都在等待的轮询间隔累计。
			currentBee.waitTickAt = time.Time{}

			currProg := progressSnapshot{
				toolCalls: currentBee.ToolCalls,
				tokens:    currentBee.Tokens,
				activity:  currentBee.Activity,
				summary:   currentBee.Summary,
			}
			if err == nil {
				currProg.progress = st.Progress
			}
			prevProg, hasPrev := m.beeProgress[currentBee.ThreadID]

			progressed := false
			if !hasPrev {
				m.beeProgress[currentBee.ThreadID] = currProg
			} else if currProg != prevProg {
				progressed = true
				currentBee.LastProgressAt = now
				m.beeProgress[currentBee.ThreadID] = currProg
			}
			if usageChanged {
				progressLeads[tgt.leadThreadID] = struct{}{}
			}

			idle := now.Sub(currentBee.LastProgressAt)

			// 思考状态感知：无输出超过 ThinkingAfter 后累计思考分钟数，一旦重新有进展立即归零。
			if progressed {
				if currentBee.ThinkingMinutes != 0 {
					currentBee.ThinkingMinutes = 0
					progressLeads[tgt.leadThreadID] = struct{}{}
				}
			} else if idle >= thinkingAfter {
				minutes := int(idle / time.Minute)
				if minutes < 1 {
					minutes = 1
				}
				if currentBee.ThinkingMinutes != minutes {
					currentBee.ThinkingMinutes = minutes
					progressLeads[tgt.leadThreadID] = struct{}{}
				}
			}
			worktreePath := currentBee.WorktreePath
			m.mu.Unlock()

			// 无输出且工作副本始终没有文件改动时按更短的卡死超时熔断；
			// git status 探测失败按存在改动处理，避免误杀正在工作的 bee。
			stuckNoEdit := false
			if idle >= noEditStallTimeout && worktreePath != "" {
				entries, statusErr := m.opts.Git.Status(ctx, worktreePath)
				stuckNoEdit = statusErr == nil && len(entries) == 0
			}

			m.mu.Lock()
			var bee *Bee
			if lead, ok := m.leads[tgt.leadThreadID]; ok {
				for i := range lead.bees {
					if lead.bees[i].ThreadID == tgt.bee.ThreadID {
						bee = &lead.bees[i]
						break
					}
				}
			}
			stopMessage := ""
			switch {
			case bee == nil || bee.Status != StatusRunning:
				// 解锁期间状态已被 stopBee/clearFinished 改变，跳过。
			case stuckNoEdit:
				stopMessage = fmt.Sprintf("看门狗：%d 分钟无输出且无文件改动，已停止", int(idle/time.Minute))
			case !bee.StartedAt.IsZero() && now.Sub(bee.StartedAt)-bee.WaitedFor >= runTimeout:
				// 持续有输出也不能无限运行：扣除等待时长后的运行时长达到设置的超时上限即停止。
				stopMessage = fmt.Sprintf("运行已达 %d 分钟上限，已自动终止", int(runTimeout/time.Minute))
			case !progressed && idle >= stallTimeout:
				stopMessage = fmt.Sprintf("%d 分钟无进展，已自动终止", int(stallTimeout/time.Minute))
			}
			if stopMessage != "" {
				bee.Status = StatusFailed
				bee.FinishedAt = &now
				bee.Summary = stopMessage
				bee.MergeError = stopMessage
				bee.Merged = false
				bee.ThinkingMinutes = 0
				delete(m.beeProgress, tgt.bee.ThreadID)
			}
			m.mu.Unlock()

			if stopMessage != "" {
				if m.opts.StopSession != nil {
					_ = m.opts.StopSession(ctx, tgt.bee.ThreadID)
				}
				// 保留半成品：停止后统计失败前的改动并写回记录，供汇报与重试使用。
				// git 操作一律在解锁后做，避免持锁跑外部命令。
				partial := tgt.bee
				m.recordPartialWork(ctx, &partial)
				m.mu.Lock()
				if lead, ok := m.leads[tgt.leadThreadID]; ok {
					for i := range lead.bees {
						if lead.bees[i].ThreadID == tgt.bee.ThreadID && lead.bees[i].Status == StatusFailed {
							lead.bees[i].ChangedFiles = partial.ChangedFiles
							lead.bees[i].LinesAdded = partial.LinesAdded
							lead.bees[i].LinesDeleted = partial.LinesDeleted
							break
						}
					}
				}
				m.mu.Unlock()
				changedLeads[tgt.leadThreadID] = struct{}{}
			}
			continue
		}

		// 会话已结束：再取一次用量，把最终 tokens 与工具调用数随结束状态一起落盘。
		if tokens, toolCalls, ok := m.beeUsage(ctx, tgt.bee.ThreadID); ok {
			tgt.bee.Tokens = tokens
			tgt.bee.ToolCalls = toolCalls
		}

		m.mu.Lock()
		delete(m.beeProgress, tgt.bee.ThreadID)
		m.mu.Unlock()

		// 会话结束到状态落盘之间可能已被 stopBee/stopAll/clearFinished 改变，
		// 以锁内最新状态为准，避免把已停止的 bee 标记为完成并合并其半成品。
		if !m.beeStillRunning(tgt.leadThreadID, tgt.bee.ThreadID) {
			continue
		}

		now := m.now()
		if tgt.bee.FinishedAt == nil {
			tgt.bee.FinishedAt = &now
		}
		tgt.bee.ThinkingMinutes = 0

		if st.Failed {
			// 续接只用一次，且不占用新 bee 的重试额度：续接之后再失败，仍可新开一个 bee 重试一次。
			if m.Settings().RetryFailed && (!m.hasContinueNudge(tgt.bee.ThreadID) || m.getRetryCount(tgt.bee.ThreadID) < 1) {
				// 先试续接原会话：线程还在时让同一个子代理从中断处继续，比新开一个 bee 更省事；
				// 驱动不支持或发送失败再落到下面的新 bee 重试。
				if !m.hasContinueNudge(tgt.bee.ThreadID) {
					m.setContinueNudge(tgt.bee.ThreadID, true)
					var sentErr error
					if messenger, ok := m.opts.Driver.(kanban.SessionMessenger); ok {
						sentErr = messenger.SendFollowUp(ctx, tgt.bee.ThreadID, "上一轮因错误中断。先判断任务是否已经完成：如果已完成且最终报告已输出，直接再输出一次最终报告，不要重新检查或重复已做的工作；如果未完成，从中断处继续，并在结束时输出最终报告。")
					} else {
						sentErr = errors.New("driver does not support SessionMessenger")
					}
					if sentErr == nil {
						m.mu.Lock()
						if lead := m.leads[tgt.leadThreadID]; lead != nil {
							for i := range lead.bees {
								if lead.bees[i].ThreadID == tgt.bee.ThreadID {
									lead.bees[i].LastProgressAt = now
									lead.bees[i].FinishedAt = nil
									lead.bees[i].Status = StatusRunning
									break
								}
							}
						}
						m.mu.Unlock()
						changedLeads[tgt.leadThreadID] = struct{}{}
						continue
					}
				}
				// 新 bee 重试只给一次机会：续接已经用过就不再新开，直接落到下面的失败收尾。
				canSpawn := m.getRetryCount(tgt.bee.ThreadID) < 1
				if canSpawn {
					m.setRetryCount(tgt.bee.ThreadID, 1)
					leadPrefix := tgt.leadThreadID
					if len(leadPrefix) > 8 {
						leadPrefix = leadPrefix[:8]
					}
					promptText := tgt.bee.Prompt
					if promptText == "" {
						promptText = tgt.bee.Task
					}
					spec := BeeSpec{
						index:       &tgt.bee.Index,
						Name:        tgt.bee.Name,
						Task:        tgt.bee.Task,
						Prompt:      promptText,
						ProjectID:   tgt.bee.ProjectID,
						WorkDir:     tgt.bee.TargetWorkDir,
						Provider:    tgt.bee.Provider,
						Model:       tgt.bee.Model,
						Effort:      tgt.bee.Effort,
						FastMode:    tgt.bee.FastMode,
						Profile:     tgt.bee.Profile,
						Tier:        tgt.bee.Tier,
						ReadOnly:    tgt.bee.ReadOnly,
						WritePaths:  append([]string(nil), tgt.bee.WritePaths...),
						RuntimeMode: tgt.bee.RuntimeMode,
						DirectModel: tgt.bee.DirectModel,
					}
					// 保留半成品：先统计失败前已有的改动，再决定是复用 worktree 接着做还是删掉重来。
					m.recordPartialWork(ctx, &tgt.bee)
					reusePartial := tgt.bee.WorktreePath != "" && len(tgt.bee.ChangedFiles) > 0
					if reusePartial {
						if _, statErr := os.Stat(tgt.bee.WorktreePath); statErr != nil {
							reusePartial = false
						}
					}
					if reusePartial {
						// 交接：新 bee 接管旧 bee 的 worktree 与基线，prompt 里说清现状与已完成部分。
						spec.reuseWorktree = tgt.bee.WorktreePath
						spec.reuseBranch = tgt.bee.Branch
						spec.reuseBaseCommit = tgt.bee.BaseCommit
						spec.Prompt = fmt.Sprintf("上一个子代理因「%s」中断，当前 worktree 里已有它的改动，文件清单为 %s。请先用 git status 与 git diff 检查现状，已完成的部分不要重做，只补齐剩余工作。\n\n原始任务：\n%s", st.FailureReason, strings.Join(tgt.bee.ChangedFiles, ", "), promptText)
					} else if tgt.bee.WorktreePath != "" {
						if m.opts.Git != nil {
							_ = m.opts.Git.WorktreeRemove(ctx, tgt.targetWorkDir, tgt.bee.WorktreePath, true)
							_ = m.opts.Git.BranchDelete(ctx, tgt.targetWorkDir, tgt.bee.Branch, true)
						}
						_ = os.RemoveAll(tgt.bee.WorktreePath)
					}
					newBee, err := m.startSingleBee(ctx, tgt.leadThreadID, tgt.bee.ProjectID, tgt.targetWorkDir, spec, tgt.bee.Index, leadPrefix)
					if err == nil {
						m.setRetryCount(newBee.ThreadID, 1)
						m.mu.Lock()
						lead := m.leads[tgt.leadThreadID]
						if lead != nil {
							for i := range lead.bees {
								if lead.bees[i].ThreadID == tgt.bee.ThreadID {
									lead.bees[i] = newBee
									break
								}
							}
						}
						m.mu.Unlock()
						m.notifyChange(tgt.leadThreadID)
						changedLeads[tgt.leadThreadID] = struct{}{}
						continue
					}
				}
			}

			// 保留半成品：重试失败后仍要把旧记录里已有的改动统计出来，供 lead 判断能否续做。
			m.recordPartialWork(ctx, &tgt.bee)
			tgt.bee.Status = StatusFailed
			if note := formatFailureReason(st.FailureReason); note != "" {
				tgt.bee.Summary = appendSummary(tgt.bee.Summary, note)
			} else {
				tgt.bee.Summary = st.FailureReason
			}

			m.mu.Lock()
			m.updateBeeLocked(tgt.leadThreadID, tgt.bee)
			m.mu.Unlock()
			changedLeads[tgt.leadThreadID] = struct{}{}
			continue
		}

		if strings.TrimSpace(st.LastAssistantText) == "" {
			if !m.hasEmptyNudge(tgt.bee.ThreadID) {
				m.setEmptyNudge(tgt.bee.ThreadID, true)
				var sentErr error
				if messenger, ok := m.opts.Driver.(kanban.SessionMessenger); ok {
					sentErr = messenger.SendFollowUp(ctx, tgt.bee.ThreadID, "请直接输出最终报告，总结你所完成的工作与改动。")
				} else {
					sentErr = errors.New("driver does not support SessionMessenger")
				}
				if sentErr == nil {
					m.mu.Lock()
					if lead := m.leads[tgt.leadThreadID]; lead != nil {
						for i := range lead.bees {
							if lead.bees[i].ThreadID == tgt.bee.ThreadID {
								lead.bees[i].LastProgressAt = now
								lead.bees[i].FinishedAt = nil
								lead.bees[i].Status = StatusRunning
								break
							}
						}
					}
					m.mu.Unlock()
					continue
				}
			}

			m.recordPartialWork(ctx, &tgt.bee)
			tgt.bee.Status = StatusFailed
			tgt.bee.Summary = "子代理未输出最终汇报"

			m.mu.Lock()
			m.updateBeeLocked(tgt.leadThreadID, tgt.bee)
			m.mu.Unlock()
			changedLeads[tgt.leadThreadID] = struct{}{}
			continue
		}

		tgt.bee.Status = StatusCompleted
		tgt.bee.Summary = st.LastAssistantText
		tgt.bee.ThinkingMinutes = 0

		root := tgt.targetWorkDir
		if root == "" {
			root = tgt.bee.TargetWorkDir
		}
		if tgt.bee.WorktreePath != "" {
			if _, err := os.Stat(tgt.bee.WorktreePath); err == nil {
				root = tgt.bee.WorktreePath
			}
		}
		cred := CheckCitations(root, tgt.bee.Summary)
		if m.opts.ReadPaths != nil {
			cred.ApplyReadTrace(m.opts.ReadPaths(tgt.bee.ThreadID))
		}
		if cred.Cited > 0 {
			tgt.bee.Credibility = &cred
		}

		if tgt.bee.ReadOnly {
			changed := false
			if tgt.bee.WorktreePath != "" && m.opts.Git != nil {
				stEntries, err := m.opts.Git.Status(ctx, tgt.bee.WorktreePath)
				if err == nil && len(stEntries) > 0 {
					changed = true
				}
				if !changed && tgt.bee.BaseCommit != "" {
					if beeRev, err := m.opts.Git.RevParse(ctx, tgt.bee.WorktreePath, "HEAD"); err == nil && beeRev != "" {
						baseRev, bErr := m.opts.Git.RevParse(ctx, tgt.bee.WorktreePath, tgt.bee.BaseCommit)
						if bErr == nil && baseRev != "" {
							if beeRev != baseRev {
								changed = true
							}
						} else if beeRev != tgt.bee.BaseCommit {
							changed = true
						}
					}
				}
			}
			if changed {
				tgt.bee.ReadOnlyViolation = true
				tgt.bee.MergeDiscarded = true
				tgt.bee.Merged = false
				tgt.bee.PendingMerge = false
				tgt.bee.MergeError = "只读档子代理改动了文件，改动已丢弃，未合并"
				if m.opts.Git != nil && tgt.bee.WorktreePath != "" {
					_ = m.opts.Git.WorktreeRemove(ctx, tgt.targetWorkDir, tgt.bee.WorktreePath, true)
					_ = m.opts.Git.BranchDelete(ctx, tgt.targetWorkDir, tgt.bee.Branch, true)
				}
				if tgt.bee.WorktreePath != "" {
					_ = os.RemoveAll(tgt.bee.WorktreePath)
				}
			} else {
				tgt.bee.Merged = true
				tgt.bee.PendingMerge = false
				tgt.bee.ConflictFiles = []string{}
			}
			m.mu.Lock()
			m.updateBeeLocked(tgt.leadThreadID, tgt.bee)
			m.mu.Unlock()
			changedLeads[tgt.leadThreadID] = struct{}{}
			continue
		}

		landed := true
		if tgt.bee.WorktreePath == "" {
			tgt.bee.Merged = true
			tgt.bee.PendingMerge = false
			tgt.bee.ConflictFiles = []string{}
		} else {
			// 串行合并同一 lead 的成果
			mergeMu := m.getLeadMergeMutex(tgt.leadThreadID)
			mergeMu.Lock()
			switch mode := m.reviewMode(); mode {
			case reviewModeOff:
				landed = m.mergeBee(ctx, tgt.leadThreadID, tgt.targetWorkDir, &tgt.bee)
			default:
				// 审查档：补丁先暂存，等审查结论后再合入。补丁为空说明没有可合入的改动，
				// 与 off 档一致直接算已合入；准备补丁失败时同 off 档一样带着 MergeError 落定。
				patch, ok := m.prepareMerge(ctx, &tgt.bee)
				if ok && len(bytes.TrimSpace(patch)) > 0 {
					tgt.bee.PendingMerge = true
					tgt.bee.MergePatch = patch
					if mode == reviewModeHuman {
						tgt.bee.ReviewState = ReviewNeedsHuman
						tgt.bee.ReviewLayer = "human"
					} else {
						tgt.bee.ReviewState = ReviewRunning
						tgt.bee.ReviewStage = ReviewStageL0
						queuedAt := m.now()
						tgt.bee.ReviewQueuedAt = &queuedAt
					}
				}
			}
			mergeMu.Unlock()
		}

		m.mu.Lock()
		if landed {
			m.updateBeeLocked(tgt.leadThreadID, tgt.bee)
		} else {
			// 主工作区尚未落定：保持 running 并记录等待起点，由下个 tick 重新尝试落地。
			m.markMergeWaitLocked(tgt.leadThreadID, tgt.bee.ThreadID, tgt.bee.MergeWaitSince, tgt.bee.FinishedAt, tgt.bee.ChangedFiles, tgt.bee.LinesAdded, tgt.bee.LinesDeleted, tgt.bee.MergeError)
		}
		m.mu.Unlock()
		changedLeads[tgt.leadThreadID] = struct{}{}
	}

	// 线程已被删除的 bee 在这里（不持锁）转交 BeeThreadDeleted 处理：它会落定 bee 状态、
	// 丢弃未落地的审查并触发一次汇报。
	for _, threadID := range deletedBeeIDs {
		m.BeeThreadDeleted(ctx, threadID)
	}

	// 审查漏斗统一在锁外起 worker：L0 检查与 L2 调用都会阻塞，不能占着 m.mu 或 mergeMu。
	m.startReviewFunnelWorkers(ctx)

	// 续跑检测：bee 结束后用户又在它自己的线程里让它继续，再次结束时也要重新落地并汇报。
	m.detectReactivatedBees(ctx)

	m.processQueue(ctx)

	for leadID := range changedLeads {
		m.notifyChange(leadID)
		// 同一 lead 已经走过即时通道，排队中的进度变更已被它兑现，不再重复合并通知。
		delete(progressLeads, leadID)
	}
	for leadID := range progressLeads {
		m.notifyProgress(leadID)
	}

	// 先推进审查结论（ai 档），再按最新状态决定这一批是否可以汇报。
	m.pollReviews(ctx)
	// L0/L2 worker 可能刚进入 l2_queued，或者上一轮已经空闲，这里再拉一次。
	m.startReviewFunnelWorkers(ctx)
	m.checkReports(ctx)
	// 汇报送达后才归档 Codex 会话，并自动清理已结束的 lead；两者都在最后一个 processQueue 之前执行。
	m.archiveFinishedBees(ctx)
	m.autoClearFinished(ctx)
	// 已安全落地的 bee 副本在这里按限流回收；bee 记录本身保留在面板里。
	m.reclaimBeeCopies(ctx)
	m.processQueue(ctx)
}

// detectReactivatedBees 识别「已结束并汇报过、但会话又被续跑起来」的 bee，把它重新置为运行中，
// 让后续 tick 的完成流程再次落地与汇报这一轮的新改动。连续两次探测（按
// reactivateProbeInterval 限流）都看到会话在运行才算续跑，避免 StopBee 刚发出、中断还没生效时误判。
func (m *Manager) detectReactivatedBees(ctx context.Context) {
	if m.opts.Driver == nil {
		return
	}
	type resumeCandidate struct {
		leadThreadID string
		bee          Bee
	}
	var candidates []resumeCandidate
	n := m.now()
	live := make(map[string]bool)
	m.mu.Lock()
	for leadID, lead := range m.leads {
		for _, h := range lead.bees {
			if h.Status == StatusRunning || !h.Reported || h.PendingMerge || h.WorktreePath == "" {
				continue
			}
			live[h.ThreadID] = true
			// 续跑探测要对每个已结束 bee 查一次驱动，随累计 bee 数线性增长：按
			// reactivateProbeInterval 限流，只是推迟识别，不影响最终结果。
			if last, probed := m.reactivateProbedAt[h.ThreadID]; probed && n.Sub(last) < reactivateProbeInterval {
				continue
			}
			m.reactivateProbedAt[h.ThreadID] = n
			candidates = append(candidates, resumeCandidate{leadThreadID: leadID, bee: h.clone()})
		}
	}
	// 已不再处于已结束状态的 bee 不再需要探测记录，避免随历史 bee 数无限增长。
	for threadID := range m.reactivateProbedAt {
		if !live[threadID] {
			delete(m.reactivateProbedAt, threadID)
		}
	}
	m.mu.Unlock()

	running := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		st, err := m.opts.Driver.SessionState(ctx, c.bee.ThreadID)
		if err != nil || !st.Running {
			continue
		}
		running[c.bee.ThreadID] = true

		m.mu.Lock()
		seen := m.reactivateSeen[c.bee.ThreadID] + 1
		m.reactivateSeen[c.bee.ThreadID] = seen
		m.mu.Unlock()
		if seen < 2 {
			continue
		}

		// 上一轮的改动已经落地：先把工作副本的当前状态固化为新基线，之后只落地续跑产生的增量，
		// 否则已合并的改动会被再合并一次。固化失败则放弃这次重新激活，保持原状态。
		baseCommit := ""
		if c.bee.Merged {
			sha, ok := m.settleResumeBaseline(ctx, c.bee.WorktreePath)
			if !ok {
				continue
			}
			baseCommit = sha
		}

		now := m.now()
		m.mu.Lock()
		lead, ok := m.leads[c.leadThreadID]
		if !ok {
			m.mu.Unlock()
			continue
		}
		var bee *Bee
		for i := range lead.bees {
			if lead.bees[i].ThreadID == c.bee.ThreadID {
				bee = &lead.bees[i]
				break
			}
		}
		// 解锁期间状态可能已被 stopBee/clearFinished 改变，以锁内最新状态为准。
		if bee == nil || bee.Status == StatusRunning || !bee.Reported {
			m.mu.Unlock()
			continue
		}
		bee.Status = StatusRunning
		bee.Reported = false
		bee.Merged = false
		bee.MergeError = ""
		bee.Delivered = false
		// 上一轮的合并进度属于已落地的旧成果，续跑后要重新走流水线。
		bee.AutoMergeState = AutoMergeNone
		bee.AutoMergeSHA = ""
		bee.AutoMergeStage = ""
		bee.AutoMergeError = ""
		bee.PendingMerge = false
		bee.MergeDiscarded = false
		bee.MergeWaitSince = nil
		bee.ThinkingMinutes = 0
		bee.FinishedAt = nil
		bee.LastProgressAt = now
		if baseCommit != "" {
			bee.BaseCommit = baseCommit
		}
		delete(m.beeProgress, c.bee.ThreadID)
		delete(m.reactivateSeen, c.bee.ThreadID)
		m.mu.Unlock()

		// 让空汇报催促、失败重试与中断续接在这一轮重新生效。
		m.setEmptyNudge(c.bee.ThreadID, false)
		m.setContinueNudge(c.bee.ThreadID, false)
		m.setRetryCount(c.bee.ThreadID, 0)
		m.notifyChange(c.leadThreadID)
	}

	// 本次真做了探测、且已不在运行的候选清零计数，下次续跑时重新累计。
	// 被限流没探测的候选保留计数，否则两轮探测永远攒不满「连续两次看到在运行」。
	m.mu.Lock()
	for _, c := range candidates {
		if !running[c.bee.ThreadID] {
			delete(m.reactivateSeen, c.bee.ThreadID)
		}
	}
	for threadID := range m.reactivateSeen {
		if !live[threadID] {
			delete(m.reactivateSeen, threadID)
		}
	}
	m.mu.Unlock()
}

// settleResumeBaseline 把 bee 工作副本的当前状态固化成一次提交并返回该提交的 sha，
// 供已合并的 bee 被续跑时作为新基线：新基线之后只剩续跑产生的改动。
// 工作副本没有未提交改动时直接取当前 HEAD；返回 ok=false 表示无法固化。
func (m *Manager) settleResumeBaseline(ctx context.Context, worktreePath string) (string, bool) {
	if m.opts.Git == nil || worktreePath == "" {
		return "", false
	}
	entries, err := m.opts.Git.Status(ctx, worktreePath)
	if err != nil {
		log.Printf("swarm: pin resumed baseline in %s failed: %v", worktreePath, err)
		return "", false
	}
	if len(entries) > 0 {
		if err := m.opts.Git.AddAll(ctx, worktreePath); err != nil {
			log.Printf("swarm: pin resumed baseline in %s failed: %v", worktreePath, err)
			return "", false
		}
		if err := m.opts.Git.Commit(ctx, worktreePath, "mcode swarm: snapshot bee copy before resumed turn"); err != nil {
			log.Printf("swarm: pin resumed baseline in %s failed: %v", worktreePath, err)
			return "", false
		}
	}
	sha, err := m.opts.Git.RevParse(ctx, worktreePath, "HEAD")
	if err != nil || sha == "" {
		log.Printf("swarm: pin resumed baseline in %s failed: resolve HEAD: %v", worktreePath, err)
		return "", false
	}
	return sha, true
}

func (m *Manager) checkReports(ctx context.Context) {
	type reportItem struct {
		leadThreadID string
		bees         []Bee
		beeIDs       map[string]bool
	}
	var reportsToSend []reportItem
	// idleLeads 收集 lead 空闲（无排队、无正在派发）的 lead，供 OnBeesIdle 推进排队汇报；
	// bees 已被清空的 lead 同样要回调，否则滞留在队列里的汇报永远放不出来。
	var idleLeads []string
	var stoppedLeads []string

	// 检查每个 lead 是否有已结束但未汇报的 bee；只要还在派发、还有排队任务或还有 bee
	// 在跑，整批就都算「仍在工作中」，等全部落定后再一次性汇报，避免局部汇报抢跑。
	reviewEnabled := m.reviewEnabled()
	m.mu.Lock()
	for leadID, lead := range m.leads {
		if lead.stoppedByUser {
			stoppedLeads = append(stoppedLeads, leadID)
		}
		hasRunning := lead.dispatching
		if !hasRunning {
			for _, q := range m.queue {
				if q.leadThreadID == leadID {
					hasRunning = true
					break
				}
			}
		}
		if !hasRunning {
			for i := range lead.bees {
				if lead.bees[i].Status == StatusRunning {
					hasRunning = true
					break
				}
			}
		}
		if !hasRunning {
			// 审查未出结论前整批都不汇报，也不回调 OnBeesIdle：只要名下还有 bee 在审查或
			// 等人工决定，就等结论落地后一次性汇报，避免局部汇报抢跑、跟进消息越过审查结论。
			reviewPending := false
			if reviewEnabled {
				for i := range lead.bees {
					if lead.bees[i].ReviewState == ReviewRunning || lead.bees[i].PendingMerge {
						reviewPending = true
						break
					}
				}
			}
			if reviewPending {
				continue
			}
			var uncommitted []Bee
			ids := make(map[string]bool)
			for i := range lead.bees {
				if !lead.bees[i].Reported && (lead.bees[i].Status == StatusCompleted || lead.bees[i].Status == StatusFailed || lead.bees[i].Status == StatusStopped) {
					ids[lead.bees[i].ThreadID] = true
					uncommitted = append(uncommitted, lead.bees[i].clone())
				}
			}
			idleLeads = append(idleLeads, leadID)
			if len(uncommitted) > 0 {
				reportsToSend = append(reportsToSend, reportItem{
					leadThreadID: leadID,
					bees:         uncommitted,
					beeIDs:       ids,
				})
			}
		}
	}
	m.mu.Unlock()

	// 主会话最近一轮被用户中断时，汇报要暂扣到用户下一句话一起走。这里先统一算出每个待汇报
	// lead 的中断状态，既供下面的投递循环复用（避免重复查驱动），也维护 reportsHeld 标记，
	// 让 Busy 在暂扣期间把「未汇报」排除出忙态；标记翻转时通知摘要重发，忙转闲才能推出去。
	// 必须在 m.mu 之外查驱动：SessionState 可能反向取后端锁。
	type leadProbe struct {
		state kanban.SessionState
		ok    bool
		held  bool
	}
	probes := make(map[string]leadProbe, len(reportsToSend))
	for _, r := range reportsToSend {
		st, ok := m.leadSessionState(ctx, r.leadThreadID)
		probes[r.leadThreadID] = leadProbe{state: st, ok: ok, held: ok && st.Interrupted}
	}
	for _, leadID := range stoppedLeads {
		if _, ok := probes[leadID]; !ok {
			st, ok := m.leadSessionState(ctx, leadID)
			probes[leadID] = leadProbe{state: st, ok: ok, held: ok && st.Interrupted}
		}
	}
	var changedLeads []string
	stoppedByUser := make(map[string]bool)
	m.mu.Lock()
	for leadID, lead := range m.leads {
		if p, ok := probes[leadID]; ok {
			if lead.stoppedByUser && p.ok && p.state.Running {
				lead.stoppedByUser = false
			}
			stoppedByUser[leadID] = lead.stoppedByUser
			held := p.held || lead.stoppedByUser
			if lead.reportsHeld != held {
				lead.reportsHeld = held
				changedLeads = append(changedLeads, leadID)
			}
			continue
		}
		// 不在本批汇报里的 lead（bees 已全部汇报或已清空）必须把暂扣标记复位，
		// 否则下一个 tick 新起的 bee 会一直被当成暂扣而漏报忙态。
		stoppedByUser[leadID] = lead.stoppedByUser
		if lead.reportsHeld && !lead.stoppedByUser {
			lead.reportsHeld = false
			changedLeads = append(changedLeads, leadID)
		}
	}
	m.mu.Unlock()
	for _, leadID := range changedLeads {
		m.notifyChange(leadID)
	}

	var sentLeads []string
	sent := make(map[string]bool)
	if m.opts.OnReport != nil {
		for _, r := range reportsToSend {
			if stoppedByUser[r.leadThreadID] {
				continue
			}
			// 主会话自身还在执行时不投递汇报：投递方只会把汇报排进主会话的跟进队列并返回成功，
			// 而队列只在「正常结束且没有进行中的 bee」的那一轮末尾才派发，那一轮早已结束，
			// 汇报就会永久滞留在队列里，bee 却已被标记为已汇报、不再重发。
			// 这里改为等主会话空闲后的下一个 tick 再发，届时汇报会直接开启一轮真正的回复。
			p := probes[r.leadThreadID]
			if p.ok && p.state.Running {
				continue
			}
			// Lead 上一轮是被用户停掉的不投递汇报：被用户停下的那一轮不自动续跑，
			// 汇报跟用户的下一句话一起走；bee 保持未汇报，Lead 正常跑完一轮后自然放行。
			if p.ok && p.state.Interrupted {
				continue
			}
			// 先存档全文再拼汇报：汇报里的超长总结会被截断，主代理和界面都靠存档取回全文。
			m.archiveReports(ctx, r.leadThreadID, r.bees)
			// 先拿 deliverMu 再读 stoppedByUser：StopAll 同样先拿 deliverMu 再翻转标记，
			// 因此停止要么等这次投递完成，要么先于检查生效让本次跳过。
			m.deliverMu.Lock()
			m.mu.Lock()
			lead := m.leads[r.leadThreadID]
			stopped := lead != nil && lead.stoppedByUser
			m.mu.Unlock()
			if stopped {
				m.deliverMu.Unlock()
				continue
			}
			if err := m.opts.OnReport(r.leadThreadID, BuildReport(r.bees)); err != nil {
				m.deliverMu.Unlock()
				log.Printf("swarm: report to %s failed: %v", r.leadThreadID, err)
				continue
			}
			m.mu.Lock()
			if lead := m.leads[r.leadThreadID]; lead != nil {
				for i := range lead.bees {
					if r.beeIDs[lead.bees[i].ThreadID] {
						lead.bees[i].Reported = true
					}
				}
			}
			m.mu.Unlock()
			m.deliverMu.Unlock()
			m.persistLead(r.leadThreadID)
			if !sent[r.leadThreadID] {
				sent[r.leadThreadID] = true
				sentLeads = append(sentLeads, r.leadThreadID)
			}
		}
	} else {
		// 未配置 OnReport 时依然推进批次落地回调。
		for _, r := range reportsToSend {
			if !sent[r.leadThreadID] {
				sent[r.leadThreadID] = true
				sentLeads = append(sentLeads, r.leadThreadID)
			}
		}
	}
	// 子代理全部结束时回调一次，供后端把此前滞留在队列里的汇报推进成一轮真实回复。
	// 放在 OnReport 投递之后：先让本批汇报落到主代理或跟进队列首位，再由本回调推进主代理
	// 消费队列，避免回调抢跑导致汇报被推迟。
	// 每个 tick 都会回调，幂等由接收方判断。
	// 被用户停掉的那一轮同样不回调：否则 Lead 会被自动续跑起来。
	if m.opts.OnBeesIdle != nil {
		for _, leadID := range idleLeads {
			if stoppedByUser[leadID] {
				continue
			}
			if st, ok := m.leadSessionState(ctx, leadID); ok && st.Interrupted {
				continue
			}
			// 与汇报投递同样先拿 deliverMu：StopAll 生效后不再回调，避免把 Lead 自动续跑。
			m.deliverMu.Lock()
			m.mu.Lock()
			lead := m.leads[leadID]
			stopped := lead != nil && lead.stoppedByUser
			m.mu.Unlock()
			if stopped {
				m.deliverMu.Unlock()
				continue
			}
			m.opts.OnBeesIdle(ctx, leadID)
			m.deliverMu.Unlock()
		}
	}
	for _, leadID := range sentLeads {
		m.maybeFireBatchLanded(ctx, leadID)
	}
}

// leadSessionState 查询主会话自身的运行时状态；未接入 Driver 或驱动不认识该会话时
// 返回 ok=false，保持与不检查时一致的行为。
func (m *Manager) leadSessionState(ctx context.Context, leadThreadID string) (kanban.SessionState, bool) {
	if m.opts.Driver == nil || leadThreadID == "" {
		return kanban.SessionState{}, false
	}
	st, err := m.opts.Driver.SessionState(ctx, leadThreadID)
	if err != nil {
		return kanban.SessionState{}, false
	}
	return st, true
}

// maybeFireBatchLanded 在该 Lead 的所有 bee 都已结束且不再等待落地后，把本批已合入且尚未上报的 bee
// 按目标工作区与项目分组，每组回调一次 OnBatchLanded（在 m.mu 之外调用）。
func (m *Manager) maybeFireBatchLanded(ctx context.Context, leadThreadID string) {
	if m.opts.OnBatchLanded == nil {
		return
	}
	type batchKey struct {
		projectID     string
		targetWorkDir string
	}

	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		m.mu.Unlock()
		return
	}
	if lead.dispatching {
		m.mu.Unlock()
		return
	}
	for _, q := range m.queue {
		if q.leadThreadID == leadThreadID {
			m.mu.Unlock()
			return
		}
	}
	for i := range lead.bees {
		h := &lead.bees[i]
		// 落地等待只对仍在运行的 bee 有意义：已结束的 bee 即使残留 MergeWaitSince 也不应卡住批次落地回调。
		if h.Status == StatusRunning || h.PendingMerge {
			m.mu.Unlock()
			return
		}
	}

	groups := make(map[batchKey][]Bee)
	var order []batchKey
	for i := range lead.bees {
		h := &lead.bees[i]
		if !h.Merged || h.MergeError != "" || h.Delivered {
			continue
		}
		projectID := h.ProjectID
		if projectID == "" {
			projectID = lead.projectID
		}
		targetWorkDir := h.TargetWorkDir
		if targetWorkDir == "" {
			targetWorkDir = lead.leadWorkDir
		}
		key := batchKey{projectID: projectID, targetWorkDir: targetWorkDir}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		h.Delivered = true
		// 交给流水线前先记为待处理：应用退出后仍能识别为未完成的合并并在重启后续做。
		if h.AutoMergeState == AutoMergeNone {
			h.AutoMergeState = AutoMergePending
		}
		h.AutoMergeError = ""
		groups[key] = append(groups[key], h.clone())
	}
	m.mu.Unlock()

	if len(order) == 0 {
		return
	}
	m.persistLead(leadThreadID)
	for _, key := range order {
		m.opts.OnBatchLanded(ctx, LandedBatch{
			LeadThreadID:  leadThreadID,
			ProjectID:     key.projectID,
			TargetWorkDir: key.targetWorkDir,
			Bees:          groups[key],
		})
	}
}

// PendingAutoMerge 描述一个重启后需要续做的自动合并批次。
type PendingAutoMerge struct {
	Batch LandedBatch
	SHA   string
	Stage string
}

// RecordAutoMerge 把流水线上报的合并结果写回批次内各 bee 并持久化。
func (m *Manager) RecordAutoMerge(leadThreadID string, beeThreadIDs []string, r BatchResult) {
	if len(beeThreadIDs) == 0 {
		return
	}
	ids := make(map[string]bool, len(beeThreadIDs))
	for _, id := range beeThreadIDs {
		ids[id] = true
	}
	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		m.mu.Unlock()
		return
	}
	for i := range lead.bees {
		h := &lead.bees[i]
		if !ids[h.ThreadID] {
			continue
		}
		h.AutoMergeState = r.State
		h.AutoMergeSHA = r.SHA
		h.AutoMergeStage = r.Stage
		h.AutoMergeError = r.Err
	}
	m.mu.Unlock()
	// 锁外通知：notifyChange 同时负责持久化。
	m.notifyChange(leadThreadID)
}

// PendingAutoMergeBatches 返回重启后需要续做的自动合并批次：
// 分组方式与 maybeFireBatchLanded 构造 LandedBatch 时一致，同组内取第一个非空的 sha 与阶段。
func (m *Manager) PendingAutoMergeBatches() []PendingAutoMerge {
	type batchKey struct {
		leadThreadID  string
		projectID     string
		targetWorkDir string
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	groups := make(map[batchKey][]Bee)
	shas := make(map[batchKey]string)
	stages := make(map[batchKey]string)
	var order []batchKey
	for leadID, lead := range m.leads {
		for i := range lead.bees {
			h := &lead.bees[i]
			if !h.Delivered || h.AutoMergeState != AutoMergePending {
				continue
			}
			projectID := h.ProjectID
			if projectID == "" {
				projectID = lead.projectID
			}
			targetWorkDir := h.TargetWorkDir
			if targetWorkDir == "" {
				targetWorkDir = lead.leadWorkDir
			}
			key := batchKey{leadThreadID: leadID, projectID: projectID, targetWorkDir: targetWorkDir}
			if _, seen := groups[key]; !seen {
				order = append(order, key)
			}
			groups[key] = append(groups[key], h.clone())
			if shas[key] == "" && h.AutoMergeSHA != "" {
				shas[key] = h.AutoMergeSHA
			}
			if stages[key] == "" && h.AutoMergeStage != "" {
				stages[key] = h.AutoMergeStage
			}
		}
	}

	out := make([]PendingAutoMerge, 0, len(order))
	for _, key := range order {
		out = append(out, PendingAutoMerge{
			Batch: LandedBatch{
				LeadThreadID:  key.leadThreadID,
				ProjectID:     key.projectID,
				TargetWorkDir: key.targetWorkDir,
				Bees:          groups[key],
			},
			SHA:   shas[key],
			Stage: stages[key],
		})
	}
	return out
}

// reportStats 是汇报中「统计」行的 JSON 结构，供界面在弹窗表头显示用时与改动规模。
type reportStats struct {
	DurationMs   int64 `json:"durationMs,omitempty"`
	ToolCalls    int   `json:"toolCalls,omitempty"`
	LinesAdded   int   `json:"linesAdded,omitempty"`
	LinesDeleted int   `json:"linesDeleted,omitempty"`
}

// BuildReport 用中文生成汇报：逐个 bee 列出改动文件、落地状态与失败原因，
// 并在两个及以上 bee 改动同一文件时给出重叠提醒。
func BuildReport(bees []Bee) string {
	if len(bees) == 0 {
		return ReportPrefix + "无已结束的子代理任务。"
	}
	var names []string
	for _, h := range bees {
		names = append(names, h.Name)
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s%s 已结束工作。\n", ReportPrefix, strings.Join(names, ", ")))

	// 重叠提醒放在各条目之前：界面解析器会把汇报条目之后的文字并入该 bee 的总结。
	if section := overlappingFilesSection(bees); section != "" {
		sb.WriteString("\n")
		sb.WriteString(section)
	}

	for _, h := range bees {
		sb.WriteString("\n\n")
		statusText := "已完成"
		switch h.Status {
		case StatusFailed:
			statusText = "失败"
		case StatusStopped:
			statusText = "已停止"
		case StatusCompleted:
			statusText = "已完成"
		case StatusRunning:
			statusText = "运行中"
		}

		sb.WriteString(fmt.Sprintf("## %s：%s (%s)\n", h.Name, h.Task, statusText))
		// 落地状态必须紧跟标题行：界面依赖该位置识别每个 bee 的分段。
		sb.WriteString(fmt.Sprintf("- 合并结果：%s\n", mergeStateText(h)))
		// 漏斗结论紧跟落地状态；off 档 ReviewLayer 为空，不输出任何审查行。
		if line := reviewReportLine(h); line != "" {
			sb.WriteString(line + "\n")
		}
		if h.ReviewLayer != "" {
			for i, c := range h.ReviewConcerns {
				if i >= 10 {
					break
				}
				if c = strings.TrimSpace(c); c != "" {
					sb.WriteString(fmt.Sprintf("- 审查疑点：%s\n", c))
				}
			}
		}
		// 审查结论紧跟落地状态：未通过的改动不在主工作区，要说清补丁在哪、可以返工。
		if h.ReviewState == ReviewRejected {
			sb.WriteString(fmt.Sprintf("- 审查意见：%s\n", h.ReviewReason))
			if h.ReviewPatchPath != "" {
				sb.WriteString(fmt.Sprintf("- 补丁：未合入，保存在 %s；需要返工就派新 bee，让它先读这个补丁和审查意见\n", h.ReviewPatchPath))
			} else {
				sb.WriteString("- 补丁：未合入，可在合并审查面板查看；需要返工就派新 bee\n")
			}
		} else if h.ReviewReason != "" {
			sb.WriteString(fmt.Sprintf("- 审查意见：%s\n", h.ReviewReason))
		}

		if h.Tier != "" {
			var modelPart string
			if h.Provider != "" && h.Model != "" {
				modelPart = h.Provider + "/" + h.Model
			} else if h.Provider != "" {
				modelPart = h.Provider
			} else if h.Model != "" {
				modelPart = h.Model
			}
			tierLine := "- 档位：" + h.Tier
			if modelPart != "" {
				tierLine += " · 模型：" + modelPart
			}
			if h.DirectModel {
				tierLine += " · 用户指定模型"
			}
			sb.WriteString(tierLine + "\n")
		}
		if h.Credibility != nil {
			if sum := h.Credibility.Summary(); sum != "" {
				sb.WriteString(fmt.Sprintf("- 可信度：%s\n", sum))
			}
		}
		if h.ReadOnlyViolation {
			sb.WriteString("- 越权写入：只读档子代理改动了文件，改动已丢弃，未合并\n")
		}
		if len(h.DiscardedPaths) > 0 {
			sb.WriteString(fmt.Sprintf("- 越权写入：该档只允许写入 %s，以下改动已丢弃未合并：%s\n", joinOrNone(h.WritePaths), joinOrNone(h.DiscardedPaths)))
		}
		if (h.Status == StatusFailed || h.Status == StatusStopped) && h.Tier != "" {
			sb.WriteString("- 提示：如需重试，先看失败原因并改 brief，重新派发时保持原档位\n")
		}

		// 文件快照随汇报保存，清理或复用 bee 后仍能查看当时的产物。
		changedFiles, _ := json.Marshal(h.ChangedFiles)
		conflictFiles, _ := json.Marshal(h.ConflictFiles)
		fmt.Fprintf(&sb, "- 改动文件：%s\n- 冲突文件：%s\n", changedFiles, conflictFiles)

		// 失败/停止的 bee 若留下半成品，告诉 lead 现场还在，可以派新 bee 接着做。
		if (h.Status == StatusFailed || h.Status == StatusStopped) && len(h.ChangedFiles) > 0 && h.WorktreePath != "" {
			sb.WriteString(fmt.Sprintf("- 半成品：失败前已改动 %d 个文件（+%d/-%d 行），worktree 保留在 %s，可选择派新 bee 在其基础上继续，或放弃重做\n", len(h.ChangedFiles), h.LinesAdded, h.LinesDeleted, h.WorktreePath))
		}

		// 统计行随汇报保存，bee 被清理后界面仍能显示用时与改动规模。
		stats := reportStats{ToolCalls: h.ToolCalls, LinesAdded: h.LinesAdded, LinesDeleted: h.LinesDeleted}
		if h.FinishedAt != nil && !h.StartedAt.IsZero() {
			if ms := h.FinishedAt.Sub(h.StartedAt).Milliseconds(); ms > 0 {
				stats.DurationMs = ms
			}
		}
		if stats != (reportStats{}) {
			if data, err := json.Marshal(stats); err == nil {
				sb.WriteString(fmt.Sprintf("- 统计：%s\n", data))
			}
		}

		if reason := failureReasonText(h); reason != "" {
			sb.WriteString(fmt.Sprintf("- 失败原因：%s\n", reason))
		}
		if len(h.ConflictFiles) > 0 {
			sb.WriteString(fmt.Sprintf("- 冲突处理：请先解决 %s 中的冲突标记（<<<<<<<、=======、>>>>>>>），再进行构建或后续改动。\n", strings.Join(h.ConflictFiles, ", ")))
		}
		if patchPath := savedPatchPath(h); patchPath != "" {
			sb.WriteString(fmt.Sprintf("- 补丁未合入，需要重做：子代理 %s，补丁保存在 %s\n", h.Name, patchPath))
			sb.WriteString(fmt.Sprintf("- 补丁改动文件：%s\n", joinOrNone(h.ChangedFiles)))
			sb.WriteString(fmt.Sprintf("- 补丁冲突或脏路径：%s\n", joinOrNone(patchBlockedPaths(h))))
			sb.WriteString(fmt.Sprintf("- 补丁失败原因：%s\n", h.MergeError))
			sb.WriteString("- 补丁处理指引：请派一个默认档位的 bee 读取该补丁文件，在主工作区当前内容之上按补丁意图重新实现；不得覆盖这些文件里已有的未提交改动，只能在其基础上追加修改；每个补丁只重试一次，仍失败则向用户报告，不要再次派发。\n")
		}

		// Summary
		// 超长总结只保留开头，全文在存档里：ID 字段排在其他字段之后、总结之前，界面会把总结之后的文字都并入总结。
		summary, truncated := truncateReportSummary(strings.TrimSpace(h.Summary))
		if truncated {
			sb.WriteString(ReportIDPrefix + h.ThreadID + "\n")
		}
		if summary != "" {
			sb.WriteString(fmt.Sprintf("- 汇报总结：%s", summary))
		} else {
			sb.WriteString("- 汇报总结：无汇报内容。")
		}
	}
	return sb.String()
}

// patchSavedMarker 是 MergeError 里标注补丁另存位置的前缀。
const patchSavedMarker = "补丁已保存到 "

// dirtyCauseMarker 是脏路径等待原因里列举路径的前缀。
const dirtyCauseMarker = "本次不落地，稍后重试: "

// savedPatchPath 从 MergeError 里取出另存的补丁绝对路径；补丁未保存或已合入时返回空字符串。
func savedPatchPath(h Bee) string {
	if h.Merged || h.MergeError == "" {
		return ""
	}
	idx := strings.LastIndex(h.MergeError, patchSavedMarker)
	if idx < 0 {
		return ""
	}
	path := strings.TrimSpace(h.MergeError[idx+len(patchSavedMarker):])
	return strings.TrimRight(path, "。，、） ")
}

// patchBlockedPaths 返回补丁未能落地涉及的冲突路径或脏路径：优先冲突文件，
// 否则从脏路径等待原因里的路径列表解析。
func patchBlockedPaths(h Bee) []string {
	if len(h.ConflictFiles) > 0 {
		return h.ConflictFiles
	}
	idx := strings.Index(h.MergeError, dirtyCauseMarker)
	if idx < 0 {
		return nil
	}
	rest := h.MergeError[idx+len(dirtyCauseMarker):]
	if cut := strings.IndexAny(rest, "）)\n"); cut >= 0 {
		rest = rest[:cut]
	}
	var out []string
	for _, p := range strings.Split(rest, "、") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// joinOrNone 把路径列表拼成中文顿号分隔的文本，空列表返回「无」。
func joinOrNone(paths []string) string {
	if len(paths) == 0 {
		return "无"
	}
	return strings.Join(paths, "、")
}

// reviewReportLine 生成汇报里的审查结论行：human 档写人工裁决，ai 档写审查层、
// 结论与估算 token；没有 ReviewLayer 的 bee（off 档或未进审查）不输出。
func reviewReportLine(h Bee) string {
	layer := strings.ToLower(strings.TrimSpace(h.ReviewLayer))
	if layer == "" {
		return ""
	}
	if layer == "human" {
		switch h.ReviewState {
		case ReviewApproved:
			return "- 审查：人工批准"
		case ReviewRejected:
			return "- 审查：人工驳回"
		default:
			return "- 审查：人工待批准"
		}
	}
	var verdict string
	switch h.ReviewState {
	case ReviewApproved:
		verdict = "通过"
	case ReviewRejected:
		verdict = "驳回"
	case ReviewNeedsHuman:
		verdict = "待人工确认"
	case ReviewRunning:
		verdict = "审查中"
	default:
		verdict = "已放弃"
	}
	line := "- 审查：" + strings.ToUpper(layer) + " " + verdict
	if h.ReviewTokens > 0 {
		tokens := formatReviewTokens(h.ReviewTokens)
		if h.ReviewTokensEstimated {
			line += "（约 " + tokens + " tokens）"
		} else {
			line += "（" + tokens + " tokens）"
		}
	}
	return line
}

// formatReviewTokens 把估算 token 数写成 3.2k / 800 这类便于阅读的形式。
func formatReviewTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n%1000 == 0 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// mergeStateText 汇总 bee 的落地状态：已放弃合并 / 失败 / 审查未通过 / 待审查 / 有冲突 / 已合入 / 未合并。
func mergeStateText(h Bee) string {
	switch {
	case h.MergeDiscarded:
		return "已放弃合并"
	case h.MergeError != "" || h.Status == StatusFailed:
		return "失败"
	case h.ReviewState == ReviewRejected:
		return "审查未通过"
	case h.PendingMerge:
		return "待审查"
	case len(h.ConflictFiles) > 0:
		return "有冲突"
	case h.Merged && h.ReviewState == ReviewApproved:
		return "已合入（审查通过）"
	case h.Merged:
		return "已合入"
	default:
		return "未合并"
	}
}

// failureReasonText 提取落地失败原因：优先 MergeError，失败或已停止的 bee 回落到 Summary。
func failureReasonText(h Bee) string {
	if h.MergeError != "" {
		return h.MergeError
	}
	if h.Status == StatusFailed || h.Status == StatusStopped {
		return strings.TrimSpace(h.Summary)
	}
	return ""
}

// overlappingFilesSection 列出被两个及以上 bee 同时改动的文件与对应的 bee 名字。
func overlappingFilesSection(bees []Bee) string {
	fileBees := make(map[string][]string)
	var overlapping []string
	for _, h := range bees {
		seen := make(map[string]bool, len(h.ChangedFiles))
		for _, f := range h.ChangedFiles {
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			fileBees[f] = append(fileBees[f], h.Name)
			if len(fileBees[f]) == 2 {
				overlapping = append(overlapping, f)
			}
		}
	}
	if len(overlapping) == 0 {
		return ""
	}
	sort.Strings(overlapping)

	var sb strings.Builder
	sb.WriteString("## 重叠文件提醒\n\n以下文件被多个子代理同时改动，请先阅读其当前内容再继续：\n")
	for _, f := range overlapping {
		sb.WriteString(fmt.Sprintf("\n- %s：%s", f, strings.Join(fileBees[f], ", ")))
	}
	sb.WriteString("\n")
	return sb.String()
}

// recordLineStats 汇总 bee 相对 base 的增删行数；失败只记一行日志，不阻断合并。
func (m *Manager) recordLineStats(ctx context.Context, h *Bee) {
	if m.opts.Git == nil || h.WorktreePath == "" || h.BaseCommit == "" {
		return
	}
	if len(h.ChangedFiles) == 0 {
		h.LinesAdded = 0
		h.LinesDeleted = 0
		return
	}
	// 只统计实际合并的文件，与 ChangedFiles 口径一致。
	stats, err := m.opts.Git.NumStat(ctx, h.WorktreePath, h.BaseCommit, "", h.ChangedFiles)
	if err != nil {
		log.Printf("swarm: 统计子代理 %d 的增删行数失败: %v", h.Index, err)
		return
	}
	var added, deleted int
	for _, st := range stats {
		// 二进制文件的增删行数为 -1，不计入总量。
		if st.Binary || st.Additions < 0 || st.Deletions < 0 {
			continue
		}
		added += st.Additions
		deleted += st.Deletions
	}
	h.LinesAdded = added
	h.LinesDeleted = deleted
}

// recordPartialWork 统计失败 bee 在保留的 worktree 里已经产生的改动，写回 ChangedFiles 与增删行数，
// 供汇报与重试复用。失败只记一行日志，不生成补丁、不合并，也不谾 MergeError/Merged，
// 避免把半成品当成待合并成果。
func (m *Manager) recordPartialWork(ctx context.Context, h *Bee) {
	// 只读档 bee 的改动会被丢弃，没有可续做的半成品。
	if h.ReadOnly || m.opts.Git == nil || h.WorktreePath == "" || h.BaseCommit == "" {
		return
	}
	if _, err := os.Stat(h.WorktreePath); err != nil {
		return
	}
	if err := m.opts.Git.AddAll(ctx, h.WorktreePath); err != nil {
		log.Printf("swarm: 暂存子代理 %d 的半成品失败: %v", h.Index, err)
		return
	}
	names, err := m.opts.Git.DiffCachedNames(ctx, h.WorktreePath, h.BaseCommit)
	if err != nil {
		log.Printf("swarm: 读取子代理 %d 的半成品改动文件失败: %v", h.Index, err)
		return
	}
	h.ChangedFiles, _ = filterWritePaths(h.WritePaths, git.FilterBuildOutputs(names))
	m.recordLineStats(ctx, h)
}

func (m *Manager) prepareMerge(ctx context.Context, h *Bee) (patch []byte, ok bool) {
	if m.opts.Git == nil || h.WorktreePath == "" {
		h.Merged = true
		h.ConflictFiles = []string{}
		return nil, true
	}

	if err := m.opts.Git.AddAll(ctx, h.WorktreePath); err != nil {
		h.MergeError = fmt.Sprintf("git add -A failed: %v", err)
		h.Merged = false
		return nil, false
	}

	names, err := m.opts.Git.DiffCachedNames(ctx, h.WorktreePath, h.BaseCommit)
	if err != nil {
		h.MergeError = fmt.Sprintf("git diff --cached --name-only failed: %v", err)
		h.Merged = false
		return nil, false
	}
	filtered := git.FilterBuildOutputs(names)
	// 写入白名单：白名单外的改动留在工作副本里，但跟构建产物一起移出暂存区，
	// 既不进入补丁，也不写回主工作区；丢弃不算合并失败。
	kept, discarded := filterWritePaths(h.WritePaths, filtered)
	filtered = kept
	if len(discarded) > 0 {
		log.Printf("swarm: 子代理 %d 有 %d 个改动不在写入白名单内，已丢弃未合并: %s", h.Index, len(discarded), strings.Join(discarded, "、"))
	}
	h.DiscardedPaths = discarded
	if dropped := droppedPaths(names, filtered); len(dropped) > 0 {
		// 构建产物与工具缓存、白名单外的改动移出暂存区，既不进入补丁，也不写回主工作区。
		if err := m.unstagePaths(ctx, h.WorktreePath, dropped); err != nil {
			log.Printf("swarm: 取消暂存子代理 %d 的构建产物失败: %v", h.Index, err)
		}
	}
	h.ChangedFiles = filtered
	m.recordLineStats(ctx, h)

	patch, err = m.opts.Git.DiffCachedBinary(ctx, h.WorktreePath, h.BaseCommit)
	if err != nil {
		h.MergeError = fmt.Sprintf("git diff --cached --binary failed: %v", err)
		h.Merged = false
		return nil, false
	}

	if len(bytes.TrimSpace(patch)) == 0 {
		h.Merged = true
		h.ConflictFiles = []string{}
		return nil, true
	}

	return patch, true
}

func (m *Manager) isMergeOrRebaseInProgress(ctx context.Context, targetWorkDir string) (bool, error) {
	if m.opts.Git == nil {
		return false, nil
	}
	gitDir, err := m.opts.Git.RevParse(ctx, targetWorkDir, "--git-dir")
	if err != nil {
		gitDir = filepath.Join(targetWorkDir, ".git")
	} else if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(targetWorkDir, gitDir)
	}

	markers := []string{"MERGE_HEAD", "rebase-merge", "rebase-apply"}
	for _, marker := range markers {
		p := filepath.Join(gitDir, marker)
		if _, err := os.Stat(p); err == nil {
			return true, nil
		}
	}
	return false, nil
}

func (m *Manager) savePatch(leadID string, beeIndex int, patch []byte) (string, error) {
	patchDir := filepath.Join(filepath.Dir(m.opts.WorktreeDir), "swarm-patches")
	if err := os.MkdirAll(patchDir, 0755); err != nil {
		return "", fmt.Errorf("create patch dir failed: %w", err)
	}
	patchPath := filepath.Join(patchDir, fmt.Sprintf("%s-%d.patch", leadID, beeIndex))
	if err := os.WriteFile(patchPath, patch, 0644); err != nil {
		return "", fmt.Errorf("write patch file failed: %w", err)
	}
	return patchPath, nil
}

func (m *Manager) saveLandedPatch(leadID string, beeIndex int, patch []byte) (string, error) {
	landedDir := filepath.Join(filepath.Dir(m.opts.WorktreeDir), "swarm-landed")
	if err := os.MkdirAll(landedDir, 0755); err != nil {
		return "", fmt.Errorf("create landed patch dir failed: %w", err)
	}
	landedPath := filepath.Join(landedDir, fmt.Sprintf("%s-%d.patch", leadID, beeIndex))
	if err := os.WriteFile(landedPath, patch, 0644); err != nil {
		return "", fmt.Errorf("write landed patch file failed: %w", err)
	}
	return landedPath, nil
}

func (m *Manager) applyMerge(ctx context.Context, leadThreadID, targetWorkDir string, h *Bee, patch []byte) {
	if leadThreadID == "" {
		if owner, ok := m.BeeOwner(h.ThreadID); ok {
			leadThreadID = owner
		}
	}
	// 走到这里说明等待已结束（落定或超时），不再保留等待起点与上一轮的等待原因。
	h.MergeWaitSince = nil
	h.MergeError = ""

	// git apply --3way 冲突时不是原子的：冲突文件写入冲突标记，其余文件照常改动。
	// 落地前先给补丁涉及的文件拍快照，冲突时逐字节复原，
	// 保证主工作区（含用户的未提交改动）与落地前完全一致。
	snap, snapErr := snapshotFiles(targetWorkDir, h.ChangedFiles)
	if snapErr != nil {
		log.Printf("swarm: 落地前给补丁路径拍快照失败: %v", snapErr)
	}

	result, err := m.opts.Git.ApplyPatch3Way(ctx, targetWorkDir, patch)
	if err != nil {
		patchPath, saveErr := m.savePatch(leadThreadID, h.Index, patch)
		if saveErr != nil || patchPath == "" {
			h.MergeError = fmt.Sprintf("git apply --3way failed: %v", err)
		} else {
			h.MergeError = fmt.Sprintf("git apply --3way failed: %v，补丁已保存到 %s", err, patchPath)
		}
		h.Merged = false
		return
	}

	if result.Conflicted {
		// 三方合并冲突：git apply 已经把带冲突标记的内容写进了目标工作区。
		// 用快照把补丁涉及的文件全部复原，冲突标记不留在用户工作区；补丁另存为 .patch 文件供人工处理。
		conflicts, scanErr := git.ScanConflictMarkers(targetWorkDir, h.ChangedFiles)
		if scanErr != nil || len(conflicts) == 0 {
			conflicts = h.ChangedFiles
		}
		reason := "补丁合并冲突"
		if snapErr != nil {
			reason = fmt.Sprintf("%s，落地前快照失败（%v），冲突标记可能留在主工作区，请手动处理", reason, snapErr)
		} else if restoreErr := snap.restore(); restoreErr != nil {
			reason = fmt.Sprintf("%s，复原主工作区失败：%v", reason, restoreErr)
		} else {
			reason += "，已复原冲突文件"
		}
		log.Printf("swarm: 子代理 %d 的补丁与目标工作区冲突（%d 个冲突文件）：%s", h.Index, len(conflicts), reason)
		h.ConflictFiles = conflicts
		h.Merged = false
		if patchPath, saveErr := m.savePatch(leadThreadID, h.Index, patch); saveErr != nil || patchPath == "" {
			h.MergeError = reason
		} else {
			h.MergeError = fmt.Sprintf("%s，补丁已保存到 %s", reason, patchPath)
		}
		return
	}

	conflicts, err := git.ScanConflictMarkers(targetWorkDir, h.ChangedFiles)
	if err != nil {
		h.MergeError = fmt.Sprintf("scan conflict markers failed: %v", err)
		h.Merged = false
		return
	}
	if len(conflicts) > 0 {
		// 补丁本身合并成功，但工作区这些文件里带着冲突标记（通常来自别的 bee 未解决的冲突）：
		// 同样不能算已合入，否则自动合并批次会把冲突标记一起提交。
		h.ConflictFiles = conflicts
		h.Merged = false
		reason := fmt.Sprintf("改动文件存在冲突标记：%s", strings.Join(conflicts, "、"))
		if patchPath, saveErr := m.savePatch(leadThreadID, h.Index, patch); saveErr == nil && patchPath != "" {
			reason = fmt.Sprintf("%s，补丁已保存到 %s", reason, patchPath)
		}
		h.MergeError = reason
		return
	}

	h.ConflictFiles = []string{}
	h.Merged = true
}

// settleDecision 表示补丁落地前的落定检查结果。
type settleDecision int

const (
	// settleReady 表示主工作区已落定，可以立即写入补丁。
	settleReady settleDecision = iota
	// settleWaiting 表示主工作区仍被 Lead 占用，本次不落地，稍后重试。
	settleWaiting
	// settleExpired 表示等待超过等待上限，补丁已另存为 .patch 文件。
	settleExpired
)

// awaitSettledCheckout 在落地前检查主工作区是否落定：Lead 正在编辑补丁涉及的文件或正在进行 merge/rebase 时，
// 记录等待起点并延后落地；等待超过 SettleTimeout 时把补丁保存为 .patch 文件。绝不阻塞或休眠。
func (m *Manager) awaitSettledCheckout(ctx context.Context, leadThreadID, targetWorkDir string, h *Bee, patch []byte) settleDecision {
	busy := false
	if m.opts.LeadTouchedPaths != nil {
		if paths, active := m.opts.LeadTouchedPaths(leadThreadID); active && pathsIntersect(h.ChangedFiles, paths) {
			busy = true
		}
	}
	if !busy {
		if inProgress, _ := m.isMergeOrRebaseInProgress(ctx, targetWorkDir); inProgress {
			busy = true
		}
	}
	if !busy {
		return settleReady
	}
	return m.deferMerge(leadThreadID, h, patch, "")
}

// settleWaitTimeout 返回本次等待的上限：Lead 正在编辑或 merge/rebase 进行中用 SettleTimeout（默认 30 分钟）。
func (m *Manager) settleWaitTimeout() time.Duration {
	if m.opts.SettleTimeout > 0 {
		return m.opts.SettleTimeout
	}
	return DefaultSettleTimeout
}

// deferMerge 记录本次不落地的等待起点；等待超过 waitTimeout 时把补丁另存为 .patch 文件、
// 标记 bee 未合入并放弃本次落地。cause 为本次等待的具体原因（可为空）。
func (m *Manager) deferMerge(leadThreadID string, h *Bee, patch []byte, cause string) settleDecision {
	now := m.now()
	if h.MergeWaitSince == nil {
		h.MergeWaitSince = &now
	}
	settleTimeout := m.settleWaitTimeout()
	if cause != "" {
		h.MergeError = cause
	}
	if now.Sub(*h.MergeWaitSince) < settleTimeout {
		return settleWaiting
	}

	reason := fmt.Sprintf("等待主工作区落定超时（%s）", formatBudgetDuration(settleTimeout))
	if cause != "" {
		reason = fmt.Sprintf("%s（%s）", reason, cause)
	}
	patchPath, saveErr := m.savePatch(leadThreadID, h.Index, patch)
	if saveErr != nil || patchPath == "" {
		h.MergeError = reason
	} else {
		h.MergeError = fmt.Sprintf("%s，补丁已保存到 %s", reason, patchPath)
	}
	h.Merged = false
	return settleExpired
}

// normalizeRelPath 把仓库相对路径清理成可比较的键（统一斜杠、去掉 "./" 前缀）。
func normalizeRelPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = filepath.ToSlash(filepath.Clean(p))
	return strings.TrimPrefix(p, "./")
}

// pathsIntersect 判断两组仓库相对路径（按斜杠清理后比较）是否存在交集。
func pathsIntersect(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	normalize := func(paths []string) map[string]struct{} {
		set := make(map[string]struct{}, len(paths))
		for _, p := range paths {
			if key := normalizeRelPath(p); key != "" {
				set[key] = struct{}{}
			}
		}
		return set
	}
	setA := normalize(a)
	for p := range normalize(b) {
		if _, ok := setA[p]; ok {
			return true
		}
	}
	return false
}

// droppedPaths 返回 names 中被 filtered 剔除的路径。
func droppedPaths(names, filtered []string) []string {
	if len(names) == len(filtered) {
		return nil
	}
	keep := make(map[string]struct{}, len(filtered))
	for _, name := range filtered {
		keep[name] = struct{}{}
	}
	var dropped []string
	for _, name := range names {
		if _, ok := keep[name]; !ok {
			dropped = append(dropped, name)
		}
	}
	return dropped
}

// filterWritePaths 按写入白名单拆分改动路径，返回保留的路径与因不在白名单而被丢弃的路径。
// writePaths 为空表示不限制，原样返回 names。
func filterWritePaths(writePaths, names []string) (kept, discarded []string) {
	if len(writePaths) == 0 {
		return names, nil
	}
	for _, name := range names {
		if MatchWritePaths(writePaths, name) {
			kept = append(kept, name)
			continue
		}
		discarded = append(discarded, name)
	}
	return kept, discarded
}

// unstagePaths 在 bee 工作副本中把指定路径移出暂存区（git reset -q -- <paths>）。
func (m *Manager) unstagePaths(ctx context.Context, worktree string, paths []string) error {
	return m.runGitPaths(ctx, worktree, []string{"reset", "-q", "--"}, paths, "git reset 取消暂存")
}

// fileSnapshot 是落地前补丁涉及文件的内容快照，用于冲突时把主工作区逐字节复原。
type fileSnapshot struct {
	root  string
	files []snapshotEntry
}

type snapshotEntry struct {
	rel     string
	exists  bool
	mode    os.FileMode
	data    []byte
	symlink string
}

// snapshotFiles 记录 paths（仓库相对路径）当前的内容、权限与是否存在。
func snapshotFiles(root string, paths []string) (*fileSnapshot, error) {
	snap := &fileSnapshot{root: root}
	seen := map[string]struct{}{}
	for _, p := range paths {
		rel := normalizeRelPath(p)
		if rel == "" {
			continue
		}
		if _, ok := seen[rel]; ok {
			continue
		}
		seen[rel] = struct{}{}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		e := snapshotEntry{rel: rel}
		info, err := os.Lstat(abs)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return nil, err
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(abs)
			if err != nil {
				return nil, err
			}
			e.exists, e.mode, e.symlink = true, info.Mode(), target
		case info.Mode().IsRegular():
			data, err := os.ReadFile(abs)
			if err != nil {
				return nil, err
			}
			e.exists, e.mode, e.data = true, info.Mode().Perm(), data
		default:
			// 目录等非普通文件不在补丁的复原范围内。
			continue
		}
		snap.files = append(snap.files, e)
	}
	return snap, nil
}

// restore 把快照中的文件写回原样：原本不存在的删除，原本存在的恢复内容与权限。
func (s *fileSnapshot) restore() error {
	var failed []string
	for _, e := range s.files {
		abs := filepath.Join(s.root, filepath.FromSlash(e.rel))
		if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
			failed = append(failed, e.rel)
			continue
		}
		if !e.exists {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			failed = append(failed, e.rel)
			continue
		}
		var err error
		if e.symlink != "" {
			err = os.Symlink(e.symlink, abs)
		} else if err = os.WriteFile(abs, e.data, e.mode); err == nil {
			err = os.Chmod(abs, e.mode)
		}
		if err != nil {
			failed = append(failed, e.rel)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("无法复原：%s", strings.Join(failed, "、"))
	}
	return nil
}

// runGitPaths 在工作副本中分批执行「gitArgs + 路径」形式的命令（gitArgs 以 "--" 结尾），
// 分批避免构建产物或改动文件过多时超出命令行长度上限。
func (m *Manager) runGitPaths(ctx context.Context, worktree string, gitArgs, paths []string, what string) error {
	if len(paths) == 0 {
		return nil
	}
	bin := "git"
	if m.opts.Git != nil && m.opts.Git.Bin != "" {
		bin = m.opts.Git.Bin
	}
	const batchSize = 200
	for start := 0; start < len(paths); start += batchSize {
		end := start + batchSize
		if end > len(paths) {
			end = len(paths)
		}
		args := append(append([]string(nil), gitArgs...), paths[start:end]...)
		cmd := procattr.HideWindow(exec.CommandContext(ctx, bin, args...))
		cmd.Dir = worktree
		// 继承登录 Shell 环境变量，保证 git hooks 能用到用户 PATH 下的工具
		cmd.Env = append(loginenv.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s失败: %w: %s", what, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// runGitCmd 在指定目录中执行 git 命令，沿用 loginenv 环境变量。
func (m *Manager) runGitCmd(ctx context.Context, dir string, args ...string) ([]byte, error) {
	bin := "git"
	if m.opts.Git != nil && m.opts.Git.Bin != "" {
		bin = m.opts.Git.Bin
	}
	cmd := procattr.HideWindow(exec.CommandContext(ctx, bin, args...))
	cmd.Dir = dir
	cmd.Env = append(loginenv.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	return cmd.CombinedOutput()
}

type integratedPatchResult struct {
	patch     []byte
	conflicts []string
	beeCommit string
	onto      string
}

// integratePatch 在对象层整合 bee 工作区与主工作区最新内容。
func (m *Manager) integratePatch(ctx context.Context, leadThreadID, targetWorkDir string, h *Bee) (integratedPatchResult, error) {
	if m.opts.Git == nil {
		return integratedPatchResult{}, errors.New("swarm: git runner is nil")
	}
	if h.WorktreePath == "" {
		return integratedPatchResult{}, errors.New("swarm: worktree path is empty")
	}
	if h.BaseCommit == "" {
		return integratedPatchResult{}, errors.New("swarm: base commit is empty")
	}

	// (a) 在 bee worktree 中从暂存区生成提交对象
	beeCommit, err := m.opts.Git.CommitIndexTree(ctx, h.WorktreePath, h.BaseCommit, fmt.Sprintf("mcode swarm bee %d", h.Index))
	if err != nil {
		return integratedPatchResult{}, fmt.Errorf("integrate patch: commit index tree: %w", err)
	}

	// (b) 在 targetWorkDir 上创建检查点，获取主工作区当前状态（含未提交改动）
	leadPrefix := leadThreadID
	if len(leadPrefix) > 8 {
		leadPrefix = leadPrefix[:8]
	}
	ref := fmt.Sprintf("refs/mcode/swarm-land/%s/%d", leadPrefix, h.Index)
	onto, err := m.opts.Git.Checkpoint(ctx, targetWorkDir, ref, fmt.Sprintf("mcode swarm landing checkpoint %d", h.Index))
	if err != nil {
		return integratedPatchResult{}, fmt.Errorf("integrate patch: checkpoint target workdir: %w", err)
	}

	// (c) 在对象层执行三方合并
	result, err := m.opts.Git.MergeTreeOnto(ctx, targetWorkDir, h.BaseCommit, onto, beeCommit)
	if err != nil {
		return integratedPatchResult{}, fmt.Errorf("integrate patch: merge tree onto: %w", err)
	}

	// (e) 存在冲突时直接返回冲突文件列表与相关提交
	if len(result.Conflicts) > 0 {
		return integratedPatchResult{
			conflicts: result.Conflicts,
			beeCommit: beeCommit,
			onto:      onto,
		}, nil
	}

	// (d) 无冲突时生成以 onto 为基线的完整补丁
	diff, err := m.opts.Git.DiffTreesBinary(ctx, targetWorkDir, onto, result.Tree)
	if err != nil {
		return integratedPatchResult{}, fmt.Errorf("integrate patch: diff trees: %w", err)
	}
	return integratedPatchResult{
		patch:     diff,
		beeCommit: beeCommit,
		onto:      onto,
	}, nil
}

// handbackConflict 把与主工作区的冲突交回 bee worktree，并启动接手的新 bee 解决冲突。
func (m *Manager) handbackConflict(ctx context.Context, leadThreadID, targetWorkDir string, h *Bee, conflicts []string, beeCommit, onto string) bool {
	if m.opts.Git == nil || m.opts.Driver == nil || h.WorktreePath == "" || h.ConflictHandbacks >= 1 {
		return false
	}
	if beeCommit == "" || onto == "" {
		return false
	}

	// (a) 在 bee worktree 执行 git reset -q --hard <onto>
	if _, err := m.runGitCmd(ctx, h.WorktreePath, "reset", "-q", "--hard", onto); err != nil {
		log.Printf("swarm: 冲突交回 reset hard 失败: %v", err)
		return false
	}

	// (b) 执行 git cherry-pick --no-commit <beeCommit>。有冲突时命令以非零退出码结束，这是预期的结果。
	_, _ = m.runGitCmd(ctx, h.WorktreePath, "cherry-pick", "--no-commit", beeCommit)

	// (c) 执行 git reset -q（mixed）清掉索引中的冲突条目，再执行 git cherry-pick --quit 清掉 sequencer 状态
	if _, err := m.runGitCmd(ctx, h.WorktreePath, "reset", "-q"); err != nil {
		log.Printf("swarm: 冲突交回 reset mixed 失败: %v", err)
		return false
	}
	_, _ = m.runGitCmd(ctx, h.WorktreePath, "cherry-pick", "--quit")

	// (d) 参考 respawnInterruptedBee 构造 BeeSpec 并启动接手的新 bee
	leadPrefix := leadThreadID
	if len(leadPrefix) > 8 {
		leadPrefix = leadPrefix[:8]
	}
	promptText := h.Prompt
	if promptText == "" {
		promptText = h.Task
	}
	workDir := h.TargetWorkDir
	if workDir == "" {
		workDir = targetWorkDir
	}
	if workDir == "" {
		m.mu.Lock()
		if lead := m.leads[leadThreadID]; lead != nil {
			workDir = lead.leadWorkDir
		}
		m.mu.Unlock()
	}

	filesStr := strings.Join(conflicts, "、")
	conflictPrompt := fmt.Sprintf("你上一轮的改动与主工作区的最新内容冲突，冲突文件：%s。这些文件里已有 <<<<<<<、=======、>>>>>>> 冲突标记：上半部分是主工作区当前内容，下半部分是你的改动。请在保留双方意图的前提下解决冲突，删除所有冲突标记，不要做与冲突无关的改动，完成后汇报。原始任务：%s", filesStr, promptText)

	spec := BeeSpec{
		index:           &h.Index,
		reuseWorktree:   h.WorktreePath,
		reuseBranch:     h.Branch,
		reuseBaseCommit: onto,
		Name:            h.Name,
		Task:            h.Task,
		Prompt:          conflictPrompt,
		ProjectID:       h.ProjectID,
		WorkDir:         workDir,
		Provider:        h.Provider,
		Model:           h.Model,
		Effort:          h.Effort,
		FastMode:        h.FastMode,
		Profile:         h.Profile,
		Tier:            h.Tier,
		ReadOnly:        h.ReadOnly,
		WritePaths:      append([]string(nil), h.WritePaths...),
		RuntimeMode:     h.RuntimeMode,
		DirectModel:     h.DirectModel,
	}

	newBee, err := m.startSingleBee(ctx, leadThreadID, h.ProjectID, workDir, spec, h.Index, leadPrefix)
	if err != nil {
		log.Printf("swarm: 冲突交回启动新 bee 失败: %v", err)
		return false
	}
	newBee.ConflictHandbacks = h.ConflictHandbacks + 1

	oldThreadID := h.ThreadID
	oldName := h.Name

	m.mu.Lock()
	if lead := m.leads[leadThreadID]; lead != nil {
		for i := range lead.bees {
			if lead.bees[i].ThreadID == oldThreadID {
				lead.bees[i] = newBee
				break
			}
		}
	}
	m.mu.Unlock()

	*h = newBee

	// (e) 通知主代理
	if m.opts.OnReport != nil {
		notice := fmt.Sprintf("%s子代理 %s 的改动与主工作区冲突（%s），主工作区未改动，已交回子代理在其副本中解决", NoticePrefix, oldName, filesStr)
		_ = m.opts.OnReport(leadThreadID, notice)
	}

	return true
}

// mergeBee 准备补丁并落地到目标工作区；主工作区尚未落定返回 false，由调用方保持 bee 处于 running 并在后续 tick 重试。
func (m *Manager) mergeBee(ctx context.Context, leadThreadID, targetWorkDir string, h *Bee) bool {
	if m.opts.Git == nil || h.WorktreePath == "" {
		h.Merged = true
		h.ConflictFiles = []string{}
		h.MergeWaitSince = nil
		return true
	}
	rawPatch, ok := m.prepareMerge(ctx, h)
	if !ok {
		h.MergeWaitSince = nil
		return true
	}
	if len(bytes.TrimSpace(rawPatch)) == 0 {
		h.MergeWaitSince = nil
		return true
	}

	patch := rawPatch
	integrated, err := m.integratePatch(ctx, leadThreadID, targetWorkDir, h)
	if err != nil {
		log.Printf("swarm: integratePatch 失败，退回原补丁流程: %v", err)
	} else if len(integrated.conflicts) > 0 {
		if h.WorktreePath != "" && h.ConflictHandbacks < 1 {
			if m.handbackConflict(ctx, leadThreadID, targetWorkDir, h, integrated.conflicts, integrated.beeCommit, integrated.onto) {
				return true
			}
		}
		h.ConflictFiles = integrated.conflicts
		h.Merged = false
		h.MergeWaitSince = nil
		patchPath, sErr := m.savePatch(leadThreadID, h.Index, rawPatch)
		if sErr != nil {
			log.Printf("swarm: 保存冲突补丁失败: %v", sErr)
		}
		h.MergeError = fmt.Sprintf("与主工作区最新内容冲突：%s，主工作区未改动，补丁已保存到 %s", strings.Join(integrated.conflicts, "、"), patchPath)
		return true
	} else {
		patch = integrated.patch
	}

	switch m.awaitSettledCheckout(ctx, leadThreadID, targetWorkDir, h, patch) {
	case settleWaiting:
		return false
	case settleExpired:
		h.MergeWaitSince = nil
		return true
	default:
		h.MergeWaitSince = nil
		m.applyMerge(ctx, leadThreadID, targetWorkDir, h, patch)
		if h.Merged {
			if landedPath, err := m.saveLandedPatch(leadThreadID, h.Index, rawPatch); err != nil {
				log.Printf("swarm: 保存落地补丁失败: %v", err)
			} else {
				h.LandedPatchPath = landedPath
			}
		}
		return true
	}
}

// beeStillRunning 判断指定 bee 在当前状态下是否仍处于 running。
func (m *Manager) beeStillRunning(leadThreadID, beeThreadID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		return false
	}
	for i := range lead.bees {
		if lead.bees[i].ThreadID == beeThreadID {
			return lead.bees[i].Status == StatusRunning
		}
	}
	return false
}

func (m *Manager) updateBeeLocked(leadThreadID string, h Bee) {
	lead, ok := m.leads[leadThreadID]
	if !ok {
		return
	}
	for i := range lead.bees {
		if lead.bees[i].ThreadID == h.ThreadID {
			// 只做 running → 终态 的单向更新，不覆盖并发发生的 stopped/清理结果。
			if lead.bees[i].Status != StatusRunning {
				return
			}
			lead.bees[i] = h.clone()
			return
		}
	}
}

func (m *Manager) replaceBeeLocked(leadThreadID string, h Bee) bool {
	lead, ok := m.leads[leadThreadID]
	if !ok {
		return false
	}
	for i := range lead.bees {
		if lead.bees[i].ThreadID == h.ThreadID {
			lead.bees[i] = h.clone()
			return true
		}
	}
	return false
}

// markMergeWaitLocked 在保持 bee 处于 running 的前提下记录落地等待起点、改动文件与未落地的原因，
// 让它在下个 tick 重新走落地检查，而不是被标记为完成。
func (m *Manager) markMergeWaitLocked(leadThreadID, beeThreadID string, since, finishedAt *time.Time, changedFiles []string, linesAdded, linesDeleted int, mergeError string) {
	lead, ok := m.leads[leadThreadID]
	if !ok {
		return
	}
	for i := range lead.bees {
		if lead.bees[i].ThreadID != beeThreadID || lead.bees[i].Status != StatusRunning {
			continue
		}
		if since != nil {
			v := *since
			lead.bees[i].MergeWaitSince = &v
		}
		if lead.bees[i].FinishedAt == nil && finishedAt != nil {
			v := *finishedAt
			lead.bees[i].FinishedAt = &v
		}
		lead.bees[i].ChangedFiles = append([]string(nil), changedFiles...)
		lead.bees[i].LinesAdded = linesAdded
		lead.bees[i].LinesDeleted = linesDeleted
		lead.bees[i].MergeError = mergeError
		// 会话已结束、只是等待落地，不应继续显示为思考中。
		lead.bees[i].ThinkingMinutes = 0
		return
	}
}

// StopBee 停止指定的子会话，并将其置为 stopped 状态，不执行自动合并。
func (m *Manager) StopBee(ctx context.Context, leadThreadID, beeThreadID string) error {
	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		m.mu.Unlock()
		return ErrLeadNotFound
	}
	var target *Bee
	for i := range lead.bees {
		if lead.bees[i].ThreadID == beeThreadID {
			target = &lead.bees[i]
			break
		}
	}
	if target == nil {
		m.mu.Unlock()
		return ErrBeeNotFound
	}
	if target.Status != StatusRunning {
		m.mu.Unlock()
		return nil
	}
	// 先在锁内落定 stopped，再中断会话：否则 Tick 可能在中断生效的瞬间把该 bee
	// 标记为 completed 并合并它的半成品。
	now := m.now()
	target.Status = StatusStopped
	target.FinishedAt = &now
	target.MergeWaitSince = nil
	delete(m.beeProgress, beeThreadID)
	m.mu.Unlock()

	if m.opts.StopSession != nil {
		_ = m.opts.StopSession(ctx, beeThreadID)
	}

	m.notifyChange(leadThreadID)
	m.processQueue(ctx)
	return nil
}

// StopAll 停止指定主会话下的所有处于 running 状态的子会话。
func (m *Manager) StopAll(ctx context.Context, leadThreadID string) error {
	// 先拿 deliverMu 再拿 m.mu：与 checkReports 的投递顺序一致，停止要么等正在进行的
	// 投递返回，要么先于投递检查生效。持 m.mu 时不得再获取 deliverMu。
	m.deliverMu.Lock()
	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		m.mu.Unlock()
		m.deliverMu.Unlock()
		return ErrLeadNotFound
	}
	now := m.now()
	var runningIDs []string
	hasUnreported := false
	for i := range lead.bees {
		bee := &lead.bees[i]
		if !bee.Reported && (bee.Status == StatusCompleted || bee.Status == StatusFailed || bee.Status == StatusStopped) {
			hasUnreported = true
		}
		if lead.bees[i].Status == StatusRunning {
			lead.bees[i].Status = StatusStopped
			lead.bees[i].Reported = true
			lead.bees[i].FinishedAt = &now
			lead.bees[i].MergeWaitSince = nil
			runningIDs = append(runningIDs, lead.bees[i].ThreadID)
			delete(m.beeProgress, lead.bees[i].ThreadID)
		}
	}

	// 一并清掉该 lead 的排队条目：停止后不应再被 processQueue 取出启动。
	// 被丢弃的条目本就没有 Bee 记录，也不需要 StopSession，只计数后记日志。
	clearedQueued := 0
	if len(m.queue) > 0 {
		filteredQueue := make([]queuedBee, 0, len(m.queue))
		for _, q := range m.queue {
			if q.leadThreadID == leadThreadID {
				clearedQueued++
				continue
			}
			filteredQueue = append(filteredQueue, q)
		}
		m.queue = filteredQueue
	}
	if len(runningIDs) > 0 || clearedQueued > 0 || hasUnreported {
		lead.stoppedByUser = true
		lead.reportsHeld = true
	}
	m.mu.Unlock()
	m.deliverMu.Unlock()

	if len(runningIDs) == 0 && clearedQueued == 0 && !hasUnreported {
		return nil
	}

	if m.opts.StopSession != nil {
		for _, id := range runningIDs {
			_ = m.opts.StopSession(ctx, id)
		}
	}

	if clearedQueued > 0 {
		log.Printf("swarm: stop all cleared %d queued bee(s) for lead %s", clearedQueued, leadThreadID)
	}

	m.persistLead(leadThreadID)
	m.notifyChange(leadThreadID)
	m.processQueue(ctx)
	return nil
}

// leadDeleter 定义可选的主会话删除持久化接口。
type leadDeleter interface {
	DeleteLead(ctx context.Context, leadThreadID string) error
}

// BeeThreadDeleted 在会话线程被删除时由底层 Hook 调用，处理 bee 中断与汇报，或清理 lead 状态。
func (m *Manager) BeeThreadDeleted(ctx context.Context, threadID string) {
	if threadID == "" {
		return
	}

	leadID, isBee := m.BeeOwner(threadID)
	if isBee {
		m.mu.Lock()
		lead, ok := m.leads[leadID]
		if !ok {
			m.mu.Unlock()
			return
		}
		var target *Bee
		for i := range lead.bees {
			if lead.bees[i].ThreadID == threadID {
				target = &lead.bees[i]
				break
			}
		}
		if target == nil || (target.Status != StatusRunning && !target.PendingMerge) {
			m.mu.Unlock()
			return
		}
		if target.Status == StatusRunning {
			now := m.now()
			target.Status = StatusStopped
			target.FinishedAt = &now
			target.Summary = ""
		}
		if target.PendingMerge {
			target.PendingMerge = false
			target.MergeDiscarded = true
		}
		delete(m.beeProgress, threadID)
		m.mu.Unlock()

		m.notifyChange(leadID)
		m.processQueue(ctx)
		m.checkReports(ctx)
		return
	}

	// 检查 threadID 是否为 Lead
	m.mu.Lock()
	lead, isLead := m.leads[threadID]
	if !isLead {
		m.mu.Unlock()
		return
	}

	now := m.now()
	var runningIDs []string
	// 删除 lead 时一并回收它的 bee 副本：先在锁内复制出待处理信息，文件与 git 操作在锁外执行。
	var copies []leadBeeCopy
	protected := m.reclaimProtectedPathsLocked()
	for i := range lead.bees {
		if lead.bees[i].Status == StatusRunning {
			lead.bees[i].Status = StatusStopped
			lead.bees[i].FinishedAt = &now
			runningIDs = append(runningIDs, lead.bees[i].ThreadID)
			delete(m.beeProgress, lead.bees[i].ThreadID)
		}
		if lead.bees[i].WorktreePath != "" {
			targetDir := lead.bees[i].TargetWorkDir
			if targetDir == "" {
				targetDir = lead.leadWorkDir
			}
			copies = append(copies, leadBeeCopy{bee: lead.bees[i].clone(), targetDir: targetDir})
		}
	}
	if len(m.queue) > 0 {
		filteredQueue := make([]queuedBee, 0, len(m.queue))
		for _, q := range m.queue {
			if q.leadThreadID != threadID {
				filteredQueue = append(filteredQueue, q)
			}
		}
		m.queue = filteredQueue
	}
	delete(m.leads, threadID)
	m.mu.Unlock()

	m.mergeMuLock.Lock()
	delete(m.mergeMus, threadID)
	m.mergeMuLock.Unlock()

	if m.opts.StopSession != nil {
		for _, id := range runningIDs {
			_ = m.opts.StopSession(ctx, id)
		}
	}

	m.reclaimLeadBeeCopies(ctx, threadID, copies, protected)

	m.persistMu.Lock()
	if deleter, ok := m.opts.Persister.(leadDeleter); ok {
		_ = deleter.DeleteLead(ctx, threadID)
	}
	m.persistMu.Unlock()

	m.notifyChange(threadID)
	m.processQueue(ctx)
}

// MergeDiff 返回审查补丁快照，已合并的 bee 也可查看。
// 旧记录没有快照时，在首次查看时采集并保存，批准前必须先查看。
func (m *Manager) MergeDiff(ctx context.Context, leadThreadID, beeThreadID string) ([]byte, error) {
	mergeMu := m.getLeadMergeMutex(leadThreadID)
	mergeMu.Lock()
	defer mergeMu.Unlock()

	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		m.mu.Unlock()
		return nil, ErrLeadNotFound
	}
	var target *Bee
	for i := range lead.bees {
		if lead.bees[i].ThreadID == beeThreadID {
			target = &lead.bees[i]
			break
		}
	}
	if target == nil {
		m.mu.Unlock()
		return nil, ErrBeeNotFound
	}
	h := target.clone()
	m.mu.Unlock()

	if len(h.MergePatch) > 0 {
		return h.MergePatch, nil
	}
	if h.WorktreePath == "" {
		// 副本已被自动回收：已落地的 bee 仍可查看存档的落地补丁。
		if h.LandedPatchPath != "" {
			if patch, err := os.ReadFile(h.LandedPatchPath); err == nil {
				return patch, nil
			}
		}
		return nil, nil
	}
	if m.opts.Git == nil {
		return nil, errors.New("swarm: git runner is nil")
	}

	if err := m.opts.Git.AddAll(ctx, h.WorktreePath); err != nil {
		return nil, fmt.Errorf("git add -A failed: %w", err)
	}
	patch, err := m.opts.Git.DiffCachedBinary(ctx, h.WorktreePath, h.BaseCommit)
	if err != nil {
		return nil, fmt.Errorf("git diff --cached --binary failed: %w", err)
	}
	if h.PendingMerge {
		names, err := m.opts.Git.DiffCachedNames(ctx, h.WorktreePath, h.BaseCommit)
		if err != nil {
			return nil, fmt.Errorf("git diff --cached --name-only failed: %w", err)
		}
		h.MergePatch = patch
		h.ChangedFiles = names
		m.recordLineStats(ctx, &h)
		m.mu.Lock()
		m.replaceBeeLocked(leadThreadID, h)
		m.mu.Unlock()
		m.notifyChange(leadThreadID)
	}
	return patch, nil
}

// errMergeNotSettled 表示主工作区仍被主代理占用，本次批准没有落地，稍后重试即可。
// AI 审查路径据此保留审查中状态并下个 Tick 重试，不转人工。
var errMergeNotSettled = errors.New("主工作区尚未落定，稍后重试批准合并")

// ApproveMerge 批准指定子会话的合并，将暂存的补丁应用至目标工作目录。
func (m *Manager) ApproveMerge(ctx context.Context, leadThreadID, beeThreadID string) (Bee, error) {
	return m.approveMerge(ctx, leadThreadID, beeThreadID, false)
}

// approveMerge 是批准合并的主体。handback 为 true（AI 审查通过）时，与主工作区冲突不再
// 保留待审查，而是仿照自动合并路径把改动交回子代理处理。
func (m *Manager) approveMerge(ctx context.Context, leadThreadID, beeThreadID string, handback bool) (Bee, error) {
	mergeMu := m.getLeadMergeMutex(leadThreadID)
	mergeMu.Lock()
	defer mergeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Bee{}, err
	}

	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		m.mu.Unlock()
		return Bee{}, ErrLeadNotFound
	}
	var target *Bee
	for i := range lead.bees {
		if lead.bees[i].ThreadID == beeThreadID {
			target = &lead.bees[i]
			break
		}
	}
	if target == nil {
		m.mu.Unlock()
		return Bee{}, ErrBeeNotFound
	}
	if !target.PendingMerge {
		m.mu.Unlock()
		return Bee{}, ErrNoPendingMerge
	}
	h := target.clone()
	targetDir := h.TargetWorkDir
	if targetDir == "" {
		targetDir = lead.leadWorkDir
	}
	m.mu.Unlock()

	if m.opts.Git == nil {
		return Bee{}, errors.New("swarm: git runner is nil")
	}
	if len(h.MergePatch) == 0 {
		return Bee{}, errors.New("缺少审查补丁，请重新打开合并审查器")
	}
	h.MergeError = ""
	patch := h.MergePatch

	worktreeExists := false
	if h.WorktreePath != "" {
		if _, err := os.Stat(h.WorktreePath); err == nil {
			worktreeExists = true
		}
	}

	if worktreeExists {
		// 确认 integratePatch 需要的前置条件：暂存区已就绪且构建产物已移出。
		// 审查模式下通常已由 prepareMerge 处理完成；若暂存区尚未就绪则按其逻辑准备，但不覆盖 h.MergePatch 原始语义。
		if h.BaseCommit != "" {
			if cached, err := m.opts.Git.DiffCachedNames(ctx, h.WorktreePath, h.BaseCommit); err == nil && len(cached) == 0 {
				origPatch := h.MergePatch
				origChanged := h.ChangedFiles
				m.prepareMerge(ctx, &h)
				h.MergePatch = origPatch
				h.MergeError = ""
				h.Merged = false
				if len(origChanged) > 0 {
					h.ChangedFiles = origChanged
				}
			}
		}

		integrated, err := m.integratePatch(ctx, leadThreadID, targetDir, &h)
		if err != nil {
			log.Printf("swarm: integratePatch 失败，退回原补丁流程: %v", err)
		} else if len(integrated.conflicts) > 0 {
			conflictText := fmt.Sprintf("与主工作区最新内容冲突：%s，主工作区未改动", strings.Join(integrated.conflicts, "、"))
			if handback {
				if m.handbackConflict(ctx, leadThreadID, targetDir, &h, integrated.conflicts, integrated.beeCommit, integrated.onto) {
					h.PendingMerge = false
					h.Merged = false
					h.ReviewState = ReviewApproved
					h.ReviewReason = "审查通过，与主工作区冲突，已交回子代理处理"
					h.MergeWaitSince = nil
					h.MergeError = ""
					h.ConflictFiles = []string{}

					m.mu.Lock()
					m.replaceBeeLocked(leadThreadID, h)
					m.mu.Unlock()

					m.notifyChange(leadThreadID)
					m.maybeFireBatchLanded(ctx, leadThreadID)
					return h.clone(), nil
				}
				// 交回失败：退回人工裁决，原因保留冲突信息。
				h.ReviewState = ReviewNeedsHuman
				h.ReviewReason = conflictText
			}
			h.ConflictFiles = integrated.conflicts
			h.PendingMerge = true
			h.Merged = false
			h.MergeWaitSince = nil
			h.MergeError = conflictText + "，请处理后再批准"

			m.mu.Lock()
			m.replaceBeeLocked(leadThreadID, h)
			m.mu.Unlock()

			m.notifyChange(leadThreadID)
			m.maybeFireBatchLanded(ctx, leadThreadID)
			return h.clone(), errors.New(h.MergeError)
		} else {
			patch = integrated.patch
		}
	}

	decision := m.awaitSettledCheckout(ctx, leadThreadID, targetDir, &h, patch)
	switch decision {
	case settleReady:
		m.applyMerge(ctx, leadThreadID, targetDir, &h, patch)
		if h.Merged {
			// 成功合入即视为审查通过：人工批准与 AI 审查通过走同一条落地路径。
			h.ReviewState = ReviewApproved
			if landedPath, err := m.saveLandedPatch(leadThreadID, h.Index, h.MergePatch); err != nil {
				log.Printf("swarm: 保存落地补丁失败: %v", err)
			} else {
				h.LandedPatchPath = landedPath
			}
		}
		h.PendingMerge = h.MergeError != ""
	case settleExpired:
		// 等待超时：补丁已另存为文件，保留待审查供用户处理。
		h.PendingMerge = true
		h.MergeWaitSince = nil
	default:
		// 主工作区尚未落定：保留待审查状态与等待起点，交由调用方稍后重试。
		// ReviewState 保持原样（AI 审查中仍为 running），下一个 Tick 会重新解析结论并重试批准。
		h.PendingMerge = true
	}

	m.mu.Lock()
	m.replaceBeeLocked(leadThreadID, h)
	m.mu.Unlock()

	m.notifyChange(leadThreadID)
	m.maybeFireBatchLanded(ctx, leadThreadID)
	switch decision {
	case settleReady:
		if h.MergeError != "" {
			return h.clone(), errors.New(h.MergeError)
		}
		return h.clone(), nil
	case settleExpired:
		// 超时后补丁已另存，AI 审查不能无限重试，交由人工处理；详细路径仍留在 MergeError 里。
		return h.clone(), errors.New("等待主工作区落定超时，补丁已另存，请人工处理")
	default:
		return h.clone(), errMergeNotSettled
	}
}

// DiscardMerge 放弃指定子会话的待审查合并。
func (m *Manager) DiscardMerge(ctx context.Context, leadThreadID, beeThreadID string) error {
	mergeMu := m.getLeadMergeMutex(leadThreadID)
	mergeMu.Lock()
	defer mergeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		m.mu.Unlock()
		return ErrLeadNotFound
	}
	var target *Bee
	for i := range lead.bees {
		if lead.bees[i].ThreadID == beeThreadID {
			target = &lead.bees[i]
			break
		}
	}
	if target == nil {
		m.mu.Unlock()
		return ErrBeeNotFound
	}
	if !target.PendingMerge {
		m.mu.Unlock()
		return ErrNoPendingMerge
	}
	h := target.clone()
	h.PendingMerge = false
	h.MergeDiscarded = true
	h.MergeError = ""
	h.Merged = false
	h.ConflictFiles = []string{}
	// 放弃后不再保留审查状态：再留着审查结论会让汇报与忙态判断误以为还有审查在跑。
	h.ReviewState = ReviewNone
	h.ReviewReason = ""
	m.replaceBeeLocked(leadThreadID, h)
	m.mu.Unlock()

	m.notifyChange(leadThreadID)
	m.maybeFireBatchLanded(ctx, leadThreadID)
	return nil
}

// reviewActionable 判断该 bee 是否还能接受审查结论：只有审查进行中或已交人工裁决的 bee
// 才可推进；已通过、已否决或已放弃的重复调用直接忽略。
func (m *Manager) reviewActionable(leadThreadID, beeThreadID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		return false
	}
	for i := range lead.bees {
		if lead.bees[i].ThreadID != beeThreadID {
			continue
		}
		state := lead.bees[i].ReviewState
		return state == ReviewRunning || state == ReviewNeedsHuman
	}
	return false
}

// applyReviewApprove 应用审查通过结论：把补丁合入主工作区，与主工作区冲突时交回子代理处理；
// 合入成功后把审查模型给出的 reason 写入 ReviewReason，供界面与汇报展示。
func (m *Manager) applyReviewApprove(ctx context.Context, leadThreadID, beeThreadID, reason string) {
	if !m.reviewActionable(leadThreadID, beeThreadID) {
		return
	}
	if _, err := m.approveMerge(ctx, leadThreadID, beeThreadID, true); err != nil {
		// 主工作区尚未落定只是本次没落地：保持审查中，下个 Tick 的 pollReviews 会重试批准。
		if errors.Is(err, errMergeNotSettled) {
			return
		}
		m.markReviewNeedsHuman(leadThreadID, beeThreadID, "审查通过但合入失败："+err.Error())
		return
	}

	// 合入成功才记录审查理由：冲突交回子代理时已写入的原因不能被覆盖。
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return
	}
	written := false
	m.mu.Lock()
	if lead, ok := m.leads[leadThreadID]; ok {
		for i := range lead.bees {
			if lead.bees[i].ThreadID != beeThreadID {
				continue
			}
			if lead.bees[i].ReviewState == ReviewApproved && strings.TrimSpace(lead.bees[i].ReviewReason) == "" {
				lead.bees[i].ReviewReason = trimmed
				written = true
			}
			break
		}
	}
	m.mu.Unlock()
	if written {
		m.notifyChange(leadThreadID)
	}
}

// applyReviewReject 应用审查未通过结论：保留补丁与工作副本供主代理返工，但不合入、不自动派新 bee。
func (m *Manager) applyReviewReject(leadThreadID, beeThreadID, reason string) {
	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		m.mu.Unlock()
		return
	}
	var target *Bee
	for i := range lead.bees {
		if lead.bees[i].ThreadID == beeThreadID {
			target = &lead.bees[i]
			break
		}
	}
	if target == nil || (target.ReviewState != ReviewRunning && target.ReviewState != ReviewNeedsHuman) {
		m.mu.Unlock()
		return
	}
	target.ReviewState = ReviewRejected
	target.ReviewReason = reason
	target.PendingMerge = false
	target.Merged = false
	// MergeDiscarded 必须保持 false：副本回收会据此删掉主代理返工要用的工作副本。
	target.MergeDiscarded = false
	m.mu.Unlock()

	// notifyChange 内含 persistLead，随后触发界面刷新。
	m.notifyChange(leadThreadID)
	m.maybeFireBatchLanded(context.Background(), leadThreadID)
}

// markReviewNeedsHuman 把审查结论交给人工裁决：补丁继续待审，等用户批准或放弃。
func (m *Manager) markReviewNeedsHuman(leadThreadID, beeThreadID, reason string) {
	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		m.mu.Unlock()
		return
	}
	var target *Bee
	for i := range lead.bees {
		if lead.bees[i].ThreadID == beeThreadID {
			target = &lead.bees[i]
			break
		}
	}
	if target == nil || (target.ReviewState != ReviewRunning && target.ReviewState != ReviewNeedsHuman) {
		m.mu.Unlock()
		return
	}
	target.ReviewState = ReviewNeedsHuman
	target.ReviewReason = reason
	// PendingMerge 保持 true：补丁仍待人工批准，不能算已落定。
	m.mu.Unlock()

	m.notifyChange(leadThreadID)
	m.maybeFireBatchLanded(context.Background(), leadThreadID)
}

// ClearFinished 清理已结束的子会话；待审查或尚待汇报的审查结果必须保留。
func (m *Manager) ClearFinished(ctx context.Context, leadThreadID string) error {
	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		m.mu.Unlock()
		return ErrLeadNotFound
	}
	leadWorkDir := lead.leadWorkDir
	finishedIDs := make(map[string]struct{})
	var finished []Bee
	for i := range lead.bees {
		h := &lead.bees[i]
		// 多 bee 分批审查时，不能在最终汇报前清掉已批准或已放弃的结果。
		awaitingReport := m.reviewEnabled() && m.opts.OnReport != nil && !h.Reported
		// 因 MCode 重启被打断的 bee 保留到用户续跑或放弃，不能当成已结束的 bee 清掉。
		if h.Interrupted {
			continue
		}
		if h.Status != StatusRunning && h.ReviewState != ReviewRunning && !h.PendingMerge && !awaitingReport {
			// 已结束的 bee 不再等待落地，清掉残留的等待起点，避免后续批次落地判定被卡住。
			h.MergeWaitSince = nil
			finishedIDs[h.ThreadID] = struct{}{}
			finished = append(finished, h.clone())
		}
	}
	m.mu.Unlock()

	if len(finished) == 0 {
		return nil
	}

	for _, h := range finished {
		targetDir := h.TargetWorkDir
		if targetDir == "" {
			targetDir = leadWorkDir
		}
		if m.opts.Git != nil && h.WorktreePath != "" {
			_ = m.opts.Git.WorktreeRemove(ctx, targetDir, h.WorktreePath, true)
			_ = m.opts.Git.BranchDelete(ctx, targetDir, h.Branch, true)
		}
		if h.WorktreePath != "" {
			_ = os.RemoveAll(h.WorktreePath)
		}
	}

	m.mu.Lock()
	// 按 threadID 过滤而非整体替换：清理期间新派发的 bee 必须保留在状态中。
	if lead, ok := m.leads[leadThreadID]; ok {
		remaining := make([]Bee, 0, len(lead.bees))
		for _, h := range lead.bees {
			if _, drop := finishedIDs[h.ThreadID]; !drop {
				remaining = append(remaining, h)
			}
		}
		lead.bees = remaining
	}
	for id := range finishedIDs {
		delete(m.beeProgress, id)
	}
	m.mu.Unlock()

	m.notifyChange(leadThreadID)
	m.processQueue(ctx)
	return nil
}

// reclaimCandidate 描述一个待回收的 bee 工作副本；字段在锁内复制，git 与文件操作在锁外执行。
type reclaimCandidate struct {
	leadID       string
	threadID     string
	targetDir    string
	worktreePath string
	branch       string
}

// leadBeeCopy 是删除 lead 时待回收的一个 bee 副本。
type leadBeeCopy struct {
	bee       Bee
	targetDir string
}

// reclaimBeeCopies 回收已安全落地的 bee 工作副本：包括已合并的 bee 与无改动的只读 bee。
// 每个 beeCopyReclaimInterval 最多真正扫一次；bee 记录本身保留在面板里，只清空 WorktreePath
// 与 Branch，用户仍可通过 MergeDiff 查看存档的落地补丁。
func (m *Manager) reclaimBeeCopies(ctx context.Context) {
	if m.opts.WorktreeDir == "" {
		return
	}
	m.mu.Lock()
	if !m.beeCopyReclaimDueLocked(m.now()) {
		m.mu.Unlock()
		return
	}
	candidates := m.reclaimCandidatesLocked()
	m.mu.Unlock()
	if len(candidates) == 0 {
		return
	}

	changedLeads := make(map[string]struct{})
	for _, c := range candidates {
		// 与合并、审查共用同一 lead 的合并互斥锁：回收时不能还有补丁在读同一份副本。
		mergeMu := m.getLeadMergeMutex(c.leadID)
		mergeMu.Lock()
		removed := m.removeBeeCopy(ctx, c.targetDir, c.worktreePath, c.branch)
		mergeMu.Unlock()
		if !removed {
			log.Printf("swarm: 回收 bee 副本 %s 失败，下轮再试", c.worktreePath)
			continue
		}
		if m.clearBeeCopy(c) {
			changedLeads[c.leadID] = struct{}{}
		}
	}
	for leadID := range changedLeads {
		m.notifyChange(leadID)
	}
}

// beeCopyReclaimDueLocked 判断本轮是否该跑自动回收，并把本次运行时间记为基准；调用方必须持有 m.mu。
func (m *Manager) beeCopyReclaimDueLocked(now time.Time) bool {
	if !m.lastBeeCopyReclaim.IsZero() && now.Sub(m.lastBeeCopyReclaim) < beeCopyReclaimInterval {
		return false
	}
	m.lastBeeCopyReclaim = now
	return true
}

// reclaimCandidatesLocked 收集本轮满足回收条件的 bee 副本；调用方必须持有 m.mu。
func (m *Manager) reclaimCandidatesLocked() []reclaimCandidate {
	now := m.now()
	protected := m.reclaimProtectedPathsLocked()
	// 同一份副本可能被失败重试的新 bee 复用，仍在被别处引用时不能回收。
	references := make(map[string]int)
	for _, lead := range m.leads {
		for _, h := range lead.bees {
			if h.WorktreePath != "" {
				references[filepath.Clean(h.WorktreePath)]++
			}
		}
	}
	var out []reclaimCandidate
	for leadID, lead := range m.leads {
		for i := range lead.bees {
			h := &lead.bees[i]
			if !m.beeCopyReclaimableLocked(h, now) {
				continue
			}
			path := filepath.Clean(h.WorktreePath)
			if !pathUnderDir(m.opts.WorktreeDir, path) {
				continue
			}
			if references[path] > 1 || sweepPathReferenced(protected, path) {
				continue
			}
			targetDir := h.TargetWorkDir
			if targetDir == "" {
				targetDir = lead.leadWorkDir
			}
			out = append(out, reclaimCandidate{
				leadID:       leadID,
				threadID:     h.ThreadID,
				targetDir:    targetDir,
				worktreePath: path,
				branch:       h.Branch,
			})
		}
	}
	return out
}

// reclaimProtectedPathsLocked 收集绝不能删掉的路径：各 lead 的主工作区、bee 的目标工作区
// 与正在创建的副本目录。调用方必须持有 m.mu。
func (m *Manager) reclaimProtectedPathsLocked() map[string]struct{} {
	var paths []string
	for _, lead := range m.leads {
		if lead.leadWorkDir != "" {
			paths = append(paths, lead.leadWorkDir)
		}
		for _, h := range lead.bees {
			if h.TargetWorkDir != "" {
				paths = append(paths, h.TargetWorkDir)
			}
		}
	}
	for path := range m.startingWorkDirs {
		paths = append(paths, path)
	}
	return sweepPathSet(paths)
}

// beeCopyReclaimableLocked 判断单个 bee 的副本是否已可安全回收：已结束且静置足够久，
// 并且成果已落地（已合并，或只读且没有任何改动）。调用方必须持有 m.mu。
func (m *Manager) beeCopyReclaimableLocked(h *Bee, now time.Time) bool {
	if h.WorktreePath == "" || h.Status == StatusRunning {
		return false
	}
	if h.PendingMerge || h.Interrupted || h.ReviewState == ReviewRunning {
		return false
	}
	if h.MergeError != "" || len(h.ConflictFiles) > 0 {
		return false
	}
	if h.FinishedAt == nil || now.Sub(*h.FinishedAt) < beeCopyReclaimIdle {
		return false
	}
	// 审查模式下结果还没汇报给用户时，补丁与副本都要留到汇报之后。
	if m.reviewEnabled() && m.opts.OnReport != nil && !h.Reported {
		return false
	}
	if h.MergeDiscarded || h.ReviewState == ReviewRejected {
		// 改动已放弃或审查未通过，副本不再有用，可以回收；两种情况的补丁都还在 MergePatch 里，
		// 审查未通过的另有 ReviewPatchPath 指向 swarm-reviews 下的补丁文件，随时可查 diff。
		return true
	}
	if h.Merged {
		// 已合并且没有残留改动、或有补丁存档可查 diff 时才回收，避免用户再也看不到改动内容。
		return len(h.ChangedFiles) == 0 || len(h.MergePatch) > 0 || readablePatchFile(h.LandedPatchPath)
	}
	if h.ReadOnly {
		return !h.ReadOnlyViolation && len(h.ChangedFiles) == 0
	}
	// 未合并的已停止/失败 bee 可能还有成果没落地，一律保留。
	return false
}

// clearBeeCopy 在副本删除后清空 bee 的 WorktreePath 与 Branch；路径已被别的流程改写时不动。
func (m *Manager) clearBeeCopy(c reclaimCandidate) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	lead, ok := m.leads[c.leadID]
	if !ok {
		return false
	}
	for i := range lead.bees {
		h := &lead.bees[i]
		if h.ThreadID != c.threadID || filepath.Clean(h.WorktreePath) != c.worktreePath {
			continue
		}
		h.WorktreePath = ""
		h.Branch = ""
		return true
	}
	return false
}

// removeBeeCopy 删除单个 bee 副本：先在目标仓库里摘掉 worktree 与分支，再删除残留目录。
func (m *Manager) removeBeeCopy(ctx context.Context, targetDir, worktreePath, branch string) bool {
	if m.opts.Git != nil && worktreePath != "" {
		_ = m.opts.Git.WorktreeRemove(ctx, targetDir, worktreePath, true)
		if branch != "" {
			_ = m.opts.Git.BranchDelete(ctx, targetDir, branch, true)
		}
	}
	if worktreePath == "" {
		return false
	}
	return os.RemoveAll(worktreePath) == nil
}

// reclaimLeadBeeCopies 在 lead 被删除后清理它的 bee 副本：成果未落地的 bee 先把改动存档成
// swarm-patches 补丁，存档成功后才删副本；采集或存档失败时保留副本，避免改动丢失。
func (m *Manager) reclaimLeadBeeCopies(ctx context.Context, leadID string, copies []leadBeeCopy, protected map[string]struct{}) {
	for _, c := range copies {
		path := filepath.Clean(c.bee.WorktreePath)
		if !pathUnderDir(m.opts.WorktreeDir, path) || sweepPathReferenced(protected, path) {
			continue
		}
		// 删除 lead 时正在运行的 bee 的 ChangedFiles 可能还没更新，不能据此判断副本里没有成果，
		// 因此只有已合并的才直接删；archiveBeeCopyPatch 在补丁为空时返回 true，没有改动的副本照样会被删除。
		landed := c.bee.Merged
		if !landed {
			if !m.archiveBeeCopyPatch(ctx, leadID, c.bee, path) {
				continue
			}
		}
		if !m.removeBeeCopy(ctx, c.targetDir, path, c.bee.Branch) {
			log.Printf("swarm: lead %s 删除时移除 bee 副本 %s 失败", leadID, path)
		}
	}
}

// archiveBeeCopyPatch 把副本里未落地的改动存档到 swarm-patches；返回 true 表示可以安全删副本。
func (m *Manager) archiveBeeCopyPatch(ctx context.Context, leadID string, h Bee, path string) bool {
	if m.opts.Git == nil {
		log.Printf("swarm: lead %s 删除时保留 bee 副本 %s：未配置 Git，无法存档改动", leadID, path)
		return false
	}
	if err := m.opts.Git.AddAll(ctx, path); err != nil {
		log.Printf("swarm: lead %s 删除时暂存 bee 副本改动失败，保留 %s: %v", leadID, path, err)
		return false
	}
	patch, err := m.opts.Git.DiffCachedBinary(ctx, path, h.BaseCommit)
	if err != nil {
		log.Printf("swarm: lead %s 删除时生成 bee 补丁失败，保留 %s: %v", leadID, path, err)
		return false
	}
	if len(bytes.TrimSpace(patch)) == 0 {
		return true
	}
	patchPath, err := m.savePatch(leadID, h.Index, patch)
	if err != nil {
		log.Printf("swarm: lead %s 删除时保存 bee 补丁失败，保留 %s: %v", leadID, path, err)
		return false
	}
	log.Printf("swarm: lead %s 删除时已把 bee 副本改动存档到 %s", leadID, patchPath)
	return true
}

// pathUnderDir 判断路径是否位于 root 之下：Clean 与解析符号链接后都必须命中，
// 以兼容 macOS 上 /var 到 /private/var 的映射。
func pathUnderDir(root, p string) bool {
	if root == "" || p == "" {
		return false
	}
	sep := string(filepath.Separator)
	cleanRoot := filepath.Clean(root)
	cleanPath := filepath.Clean(p)
	if cleanPath != cleanRoot && strings.HasPrefix(cleanPath, cleanRoot+sep) {
		return true
	}
	resolvedRoot, err := filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(cleanPath)
	if err != nil {
		return false
	}
	resolvedRoot = filepath.Clean(resolvedRoot)
	resolved = filepath.Clean(resolved)
	return resolved != resolvedRoot && strings.HasPrefix(resolved, resolvedRoot+sep)
}

// readablePatchFile 判断补丁存档路径是否指向一个存在且可读的普通文件。
func readablePatchFile(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// archiveFinishedBees 在 bee 汇报送达后归档其 Codex 会话：只归档会话、保留 worktree，
// 绝不调用任何 git push 或 Pipeline 推送逻辑。会话保留到汇报送达后才归档。
func (m *Manager) archiveFinishedBees(ctx context.Context) {
	if !m.Settings().ArchivesCodexBees {
		return
	}
	archiver, ok := m.opts.Driver.(SessionArchiver)
	if !ok {
		return
	}

	type archiveTarget struct {
		leadThreadID string
		bee          Bee
	}
	var targets []archiveTarget
	m.mu.Lock()
	for leadID, lead := range m.leads {
		for _, h := range lead.bees {
			if h.Status != StatusCompleted || !h.Reported || h.PendingMerge || h.ArchiveHandled {
				continue
			}
			targets = append(targets, archiveTarget{leadThreadID: leadID, bee: h.clone()})
		}
	}
	m.mu.Unlock()

	for _, tgt := range targets {
		archived, err := archiver.ArchiveBeeSession(ctx, tgt.bee.ThreadID)
		if err != nil {
			// 归档失败时保持未处理，交由下个 tick 重试。
			continue
		}
		next := tgt.bee
		next.ArchiveHandled = true
		next.Archived = archived
		m.mu.Lock()
		m.replaceBeeLocked(tgt.leadThreadID, next)
		m.mu.Unlock()
		m.notifyChange(tgt.leadThreadID)
	}
}

// autoClearFinished 在开关开启时，把全部 bee 已结束、已汇报且落地完成的 lead 交给 ClearFinished 清理。
func (m *Manager) autoClearFinished(ctx context.Context) {
	settings := m.Settings()
	if !settings.AutoClearFinished {
		return
	}

	var leadIDs []string
	m.mu.Lock()
	for leadID, lead := range m.leads {
		if len(lead.bees) == 0 {
			continue
		}
		// 重启后恢复出来、等待用户决定续跑或放弃的排队条目必须保留，否则用户来不及处理。
		if len(lead.heldQueue) > 0 {
			continue
		}
		clearable := true
		for _, h := range lead.bees {
			// 因 MCode 重启被打断的 bee 要等用户续跑或放弃，不能自动清理。
			if h.Interrupted {
				clearable = false
				break
			}
			if h.Status == StatusRunning || h.ReviewState == ReviewRunning || h.PendingMerge || !h.Reported {
				clearable = false
				break
			}
			// 落地失败或带冲突的 bee，其 worktree 可能是成果唯一副本，不自动清理，留给用户处理。
			if h.MergeError != "" || len(h.ConflictFiles) > 0 {
				clearable = false
				break
			}
			// 有批次落地回调时，已合入的 bee 必须先上报过才可清理。
			if m.opts.OnBatchLanded != nil && h.Merged && !h.Delivered {
				clearable = false
				break
			}
			// 开启 Codex 归档时，已完成的 bee 必须先归档再清理。
			if settings.ArchivesCodexBees && h.Status == StatusCompleted && !h.ArchiveHandled {
				clearable = false
				break
			}
		}
		if !clearable {
			continue
		}
		for _, q := range m.queue {
			if q.leadThreadID == leadID {
				clearable = false
				break
			}
		}
		if clearable {
			leadIDs = append(leadIDs, leadID)
		}
	}
	m.mu.Unlock()

	for _, leadID := range leadIDs {
		_ = m.ClearFinished(ctx, leadID)
	}
}

// formatFailureReason 格式化失败原因：截断到 500 字符、去掉首尾空白，并按场景补齐前缀。
func formatFailureReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	switch strings.ToLower(reason) {
	case "failed", "error", "attention: failed":
		return ""
	}
	const maxLen = 500
	runes := []rune(reason)
	if len(runes) > maxLen {
		reason = string(runes[:maxLen])
	}
	reason = strings.TrimSpace(reason)
	if strings.HasPrefix(reason, "启动失败：") || strings.HasPrefix(reason, "启动失败:") ||
		strings.HasPrefix(reason, "运行失败：") || strings.HasPrefix(reason, "运行失败:") ||
		strings.HasPrefix(reason, "续跑失败：") || strings.HasPrefix(reason, "续跑失败:") ||
		strings.HasPrefix(reason, "失败：") || strings.HasPrefix(reason, "失败:") {
		return reason
	}
	lower := strings.ToLower(reason)
	if strings.Contains(lower, "start") || strings.Contains(reason, "启动") || strings.Contains(lower, "exec:") {
		return "启动失败：" + reason
	}
	return "运行失败：" + reason
}

// appendSummary 在已有的 summary 末尾追加一句说明；原有 summary 为空时直接使用这句话。
func appendSummary(summary, note string) string {
	if strings.TrimSpace(summary) == "" {
		return note
	}
	return summary + "；" + note
}

// ResumeInterrupted 续跑指定主会话下因 MCode 重启被打断的 bee：先试原会话，再回退新开会话，
// 并把重启前排队、尚未启动的条目重新排入派发队列；返回成功续跑或重新入队的条目总数。
// 没有待续跑条目时返回 0 且不报错；全部续跑失败时返回 0 和合并后的错误。
func (m *Manager) ResumeInterrupted(ctx context.Context, leadThreadID string) (int, error) {
	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		m.mu.Unlock()
		return 0, ErrLeadNotFound
	}
	var pending []Bee
	for _, h := range lead.bees {
		if h.Interrupted && h.Status == StatusStopped && !h.Merged {
			pending = append(pending, h.clone())
		}
	}
	held := append([]queuedBee(nil), lead.heldQueue...)
	if len(pending) == 0 && len(held) == 0 {
		m.mu.Unlock()
		return 0, nil
	}
	// 取出即清空：heldQueue 里的条目要么重新入队，要么随这次调用一起消失。
	lead.heldQueue = nil
	if !lead.requestStartedAt.IsZero() {
		// 重启期间的时间不计入本次请求的时长预算，否则预算可能已用尽、续跑立刻被判超时。
		lead.requestStartedAt = m.now()
	}
	m.mu.Unlock()

	var errs []error
	resumed := 0
	for _, old := range pending {
		sent, ok := m.continueInterruptedBee(ctx, leadThreadID, old)
		if ok {
			resumed++
			continue
		}
		if sent {
			continue
		}
		newBee, err := m.respawnInterruptedBee(ctx, leadThreadID, old)
		if err != nil {
			log.Printf("swarm: 续跑被打断的子代理 %d 失败: %v", old.Index, err)
			errs = append(errs, fmt.Errorf("子代理 %d（%s）续跑失败: %w", old.Index, old.Name, err))
			m.mu.Lock()
			if lead := m.leads[leadThreadID]; lead != nil {
				for i := range lead.bees {
					if lead.bees[i].ThreadID != old.ThreadID {
						continue
					}
					// 续跑失败就地收尾：清掉中断标记，避免用户反复点到一条必失败的续跑。
					lead.bees[i].Interrupted = false
					lead.bees[i].Summary = appendSummary(lead.bees[i].Summary, "续跑失败："+err.Error())
					break
				}
			}
			m.mu.Unlock()
			continue
		}
		// 续跑出来的新 bee 只给一次机会，不再叠加上一轮失败重试的自动重试。
		m.setRetryCount(newBee.ThreadID, 1)
		m.mu.Lock()
		if lead := m.leads[leadThreadID]; lead != nil {
			for i := range lead.bees {
				if lead.bees[i].ThreadID == old.ThreadID {
					lead.bees[i] = newBee
					break
				}
			}
		}
		m.mu.Unlock()
		resumed++
	}

	if len(held) > 0 {
		m.mu.Lock()
		for _, q := range held {
			q.leadThreadID = leadThreadID
			m.queue = append(m.queue, q)
		}
		m.mu.Unlock()
		resumed += len(held)
		// 重新入队的条目立即参与派发，但受并发上限约束，超出的继续留在队列里。
		m.processQueue(ctx)
	}

	m.notifyChange(leadThreadID)
	if len(errs) > 0 && resumed == 0 {
		return 0, errors.Join(errs...)
	}
	return resumed, nil
}

// continueInterruptedBee 返回消息是否发出及 bee 是否重新激活；未发出时可回退新会话，发出但未激活时尝试中断原会话。
func (m *Manager) continueInterruptedBee(ctx context.Context, leadThreadID string, old Bee) (sent bool, resumed bool) {
	messenger, ok := m.opts.Driver.(kanban.SessionMessenger)
	if !ok || old.ThreadID == "" {
		return false, false
	}
	if !old.ReadOnly {
		if old.WorktreePath == "" {
			return false, false
		}
		if _, err := os.Stat(old.WorktreePath); err != nil {
			return false, false
		}
	}
	message := "上一轮因 MCode 重启中断。先判断任务是否已经完成：如果已完成且最终报告已输出，直接再输出一次最终报告，不要重复已做的工作；如果未完成，从中断处继续，并在结束时输出最终报告。"
	promptText := old.Prompt
	if promptText == "" {
		promptText = old.Task
	}
	if promptText != "" {
		message += "\n\n如果你看不到上一轮的对话，就按下面的原始任务从头完成。\n\n原始任务：\n" + promptText
	}
	if err := messenger.SendFollowUp(ctx, old.ThreadID, message); err != nil {
		log.Printf("swarm: 续接被打断的子代理 %s 失败: %v", old.ThreadID, err)
		return false, false
	}

	m.mu.Lock()
	var bee *Bee
	if lead := m.leads[leadThreadID]; lead != nil {
		for i := range lead.bees {
			if lead.bees[i].ThreadID == old.ThreadID {
				bee = &lead.bees[i]
				break
			}
		}
	}
	// 投递期间可能已放弃续跑或改变状态，不能用旧快照重新激活。
	if bee == nil || !bee.Interrupted || bee.Status != StatusStopped {
		m.mu.Unlock()
		if interrupter, ok := m.opts.Driver.(kanban.SessionInterrupter); ok {
			if err := interrupter.InterruptSession(ctx, old.ThreadID); err != nil {
				log.Printf("swarm: 中断已取消续接的子代理 %s 失败: %v", old.ThreadID, err)
			}
		}
		return true, false
	}
	bee.Status = StatusRunning
	bee.Interrupted = false
	bee.Reported = false
	bee.Delivered = false
	bee.FinishedAt = nil
	bee.LastProgressAt = m.now()
	// 停机时间与上一轮等待不能占用续跑的运行预算。
	bee.StartedAt = bee.LastProgressAt
	bee.WaitedFor = 0
	bee.waitTickAt = time.Time{}
	bee.ThinkingMinutes = 0
	bee.MergeWaitSince = nil
	const prefix = "MCode 重启，子代理已中断"
	if bee.Summary == prefix {
		bee.Summary = ""
	} else {
		bee.Summary = strings.TrimPrefix(bee.Summary, prefix+"；")
	}
	delete(m.reactivateSeen, old.ThreadID)
	delete(m.reactivateProbedAt, old.ThreadID)
	m.mu.Unlock()

	m.setContinueNudge(old.ThreadID, true)
	m.setRetryCount(old.ThreadID, 0)
	m.setEmptyNudge(old.ThreadID, false)
	m.mu.Lock()
	delete(m.beeProgress, old.ThreadID)
	m.mu.Unlock()
	return true, true
}

// respawnInterruptedBee 在原会话无法续接时，为因 MCode 重启被打断的 bee 新开子会话。
// 这里的做法刻意与 checkReports 里「失败重试」那段（构造 BeeSpec、recordPartialWork、
// 复用半成品 worktree、startSingleBee、替换 lead.bees 中对应条目）保持一致；
// 改动其中一处时必须同步另一处，否则两条重派路径会越走越远。
func (m *Manager) respawnInterruptedBee(ctx context.Context, leadThreadID string, old Bee) (Bee, error) {
	return m.respawnBeeWithHandoff(ctx, leadThreadID, old, "因 MCode 重启中断", false)
}

// respawnBeeWithHandoff 为一次重派构造 spec 并把旧 bee 的半成品交接给新 bee：
// 先统计旧 bee 中断前的改动，原 worktree 还在就复用它与基线，不在就清理后新建；
// cause 是要写进提示的中断原因（如「因 MCode 重启中断」），noteLostWorktree 为真时
// 会在 worktree 已不存在的情况下额外说明半成品已丢失。
// 该函数只负责派出新会话，替换 lead.bees 里的旧条目由调用方完成。
func (m *Manager) respawnBeeWithHandoff(ctx context.Context, leadThreadID string, old Bee, cause string, noteLostWorktree bool) (Bee, error) {
	if m.opts.Git == nil {
		return Bee{}, errors.New("swarm: git runner is nil")
	}
	if m.opts.Driver == nil {
		return Bee{}, errors.New("swarm: session driver is nil")
	}
	leadPrefix := leadThreadID
	if len(leadPrefix) > 8 {
		leadPrefix = leadPrefix[:8]
	}
	promptText := old.Prompt
	if promptText == "" {
		promptText = old.Task
	}
	workDir := old.TargetWorkDir
	if workDir == "" {
		m.mu.Lock()
		if lead := m.leads[leadThreadID]; lead != nil {
			workDir = lead.leadWorkDir
		}
		m.mu.Unlock()
	}
	spec := BeeSpec{
		index:       &old.Index,
		Name:        old.Name,
		Task:        old.Task,
		Prompt:      promptText,
		ProjectID:   old.ProjectID,
		WorkDir:     old.TargetWorkDir,
		Provider:    old.Provider,
		Model:       old.Model,
		Effort:      old.Effort,
		FastMode:    old.FastMode,
		Profile:     old.Profile,
		Tier:        old.Tier,
		ReadOnly:    old.ReadOnly,
		WritePaths:  append([]string(nil), old.WritePaths...),
		RuntimeMode: old.RuntimeMode,
		DirectModel: old.DirectModel,
	}
	// 保留半成品：先统计中断前已有的改动，再决定是复用 worktree 接着做还是清理后重来。
	partial := old
	m.recordPartialWork(ctx, &partial)
	reusePartial := partial.WorktreePath != "" && len(partial.ChangedFiles) > 0
	if reusePartial {
		if _, statErr := os.Stat(partial.WorktreePath); statErr != nil {
			reusePartial = false
		}
	}
	if reusePartial {
		// 交接：新 bee 接管旧 bee 的 worktree 与基线，prompt 里说清现状与已完成部分。
		spec.reuseWorktree = partial.WorktreePath
		spec.reuseBranch = partial.Branch
		spec.reuseBaseCommit = partial.BaseCommit
		spec.Prompt = fmt.Sprintf("上一个子代理%s，当前 worktree 里已有它的改动，文件清单为 %s。请先用 git status 与 git diff 检查现状，已完成的部分不要重做，只补齐剩余工作。\n\n原始任务：\n%s", cause, strings.Join(partial.ChangedFiles, ", "), promptText)
	} else {
		if partial.WorktreePath != "" {
			if m.opts.Git != nil {
				_ = m.opts.Git.WorktreeRemove(ctx, workDir, partial.WorktreePath, true)
				_ = m.opts.Git.BranchDelete(ctx, workDir, partial.Branch, true)
			}
			_ = os.RemoveAll(partial.WorktreePath)
		}
		if noteLostWorktree {
			// 半成品已不存在：说清楚要从头做，避免新 bee 以为还有现场可接手。
			spec.Prompt = fmt.Sprintf("上一个子代理%s，但它的工作副本已经不存在，之前的半成品改动无法接手，请从头完成任务。\n\n原始任务：\n%s", cause, promptText)
		}
	}
	return m.startSingleBee(ctx, leadThreadID, old.ProjectID, workDir, spec, old.Index, leadPrefix)
}

// RetryBee 让用户手动沿用指定 bee 的原 worktree 与半成品改动重新派发一个子会话。
// 自动重试与重启续跑各自只在 tick 或重启时触发，失败后想再沿用手头的改动重做一次只能等自动重试；
// 这里把同一套构造 spec、记录半成品、复用 worktree 的逻辑开放成可按单个 bee 调用的入口。
// 新 bee 沿用旧 bee 的 Index 并替换 lead.bees 中对应条目（旧线程仍可回看），
// 与自动重试一样只给一次重试机会；只有失败或已停止、且改动未合并的子代理允许调用。
func (m *Manager) RetryBee(ctx context.Context, leadThreadID, beeThreadID string) (Bee, error) {
	if strings.TrimSpace(beeThreadID) == "" {
		return Bee{}, ErrBeeNotFound
	}

	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		m.mu.Unlock()
		return Bee{}, ErrLeadNotFound
	}
	var old Bee
	found := false
	for i := range lead.bees {
		if lead.bees[i].ThreadID == beeThreadID {
			old = lead.bees[i].clone()
			found = true
			break
		}
	}
	busy := found && m.retryingBees[beeThreadID]
	if found && !busy {
		if m.retryingBees == nil {
			m.retryingBees = make(map[string]bool)
		}
		m.retryingBees[beeThreadID] = true
	}
	m.mu.Unlock()

	if !found {
		// 只有属于该主会话的 bee 才能重试，避免跨会话误操作别人的 worktree。
		return Bee{}, fmt.Errorf("%w: %s", ErrBeeNotFound, beeThreadID)
	}
	if busy {
		return Bee{}, fmt.Errorf("子代理 %s 正在重新派发，请稍候", old.Name)
	}
	defer func() {
		m.mu.Lock()
		delete(m.retryingBees, beeThreadID)
		m.mu.Unlock()
	}()

	switch old.Status {
	case StatusFailed, StatusStopped:
	default:
		return Bee{}, fmt.Errorf("只有失败或已停止的子代理可以沿用改动续做（当前状态：%s）", old.Status)
	}
	if old.ReviewState == ReviewRunning {
		return Bee{}, errors.New("审查进行中，不能续做")
	}
	if old.Merged || old.PendingMerge || old.MergeWaitSince != nil {
		return Bee{}, errors.New("该子代理的改动已合并或正在合并，不能沿用改动续做")
	}

	// 沿用旧 bee 的失败原因写进接手提示；没有摘要时按状态给一句通用说明。
	cause := strings.TrimSpace(old.Summary)
	if cause == "" {
		if old.Status == StatusFailed {
			cause = "执行失败"
		} else {
			cause = "被手动停止"
		}
	}
	newBee, err := m.respawnBeeWithHandoff(ctx, leadThreadID, old, fmt.Sprintf("因「%s」中断", cause), true)
	if err != nil {
		return Bee{}, err
	}
	// 与自动重试、重启续跑一致：重派出来的 bee 只给一次机会，不再自动重试。
	m.setRetryCount(newBee.ThreadID, 1)
	m.mu.Lock()
	if lead := m.leads[leadThreadID]; lead != nil {
		for i := range lead.bees {
			if lead.bees[i].ThreadID == old.ThreadID {
				lead.bees[i] = newBee
				break
			}
		}
	}
	m.mu.Unlock()
	m.notifyChange(leadThreadID)
	return newBee, nil
}

// DismissInterrupted 放弃指定主会话下所有因重启被打断的 bee 与尚未启动的排队条目：
// bee 保持 stopped 只清掉中断标记，heldQueue 直接丢弃；返回清理掉的条目总数。
func (m *Manager) DismissInterrupted(leadThreadID string) int {
	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		m.mu.Unlock()
		return 0
	}
	cleared := 0
	for i := range lead.bees {
		if !lead.bees[i].Interrupted {
			continue
		}
		lead.bees[i].Interrupted = false
		lead.bees[i].Summary = appendSummary(lead.bees[i].Summary, "已放弃续跑")
		cleared++
	}
	cleared += len(lead.heldQueue)
	lead.heldQueue = nil
	m.mu.Unlock()

	if cleared == 0 {
		return 0
	}
	m.notifyChange(leadThreadID)
	return cleared
}
