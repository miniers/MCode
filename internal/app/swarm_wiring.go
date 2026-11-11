package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcode/internal/config"
	"mcode/internal/harness/policy"
	"mcode/internal/localmem"
	"mcode/internal/mcp"
	"mcode/internal/memspace"
	"mcode/internal/nmem"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"mcode/internal/swarm"
)

// SwarmSettingsFromConfig 将 config.OrchestrationConfig 转换为 swarm.Settings。
func SwarmSettingsFromConfig(c config.OrchestrationConfig) swarm.Settings {
	return swarm.Settings{
		Enabled:         c.IsEnabled(),
		AutoDelegate:    c.AutoDelegateEnabled(),
		MaxBees:         c.MaxBees,
		MaxRounds:       c.MaxRounds,
		WorkspaceMode:   c.WorkspaceMode,
		RetryFailed:     c.RetryFailed,
		AutoMerge:       c.AutoMerge,
		AutoPush:        c.AutoPush,
		DefaultProvider: c.DefaultProvider,
		DefaultModel:    c.DefaultModel,
		DefaultEffort:   c.DefaultEffort,
		Pairs:           swarmPairsFromConfig(c.Pairs),
		Tiers:           swarmTiersFromConfig(c.Tiers),
		DefaultTier:     c.DefaultTier,

		BeeTimeoutMinutes: c.BeeTimeoutMinutes,

		RequestTimeoutMinutes: c.RequestTimeoutMinutes,

		AutoClearFinished: c.AutoClearFinished,
		AutoHidesIdleBees: c.AutoHidesIdleBees,
		ArchivesCodexBees: c.ArchivesCodexBees,
		DelegateTool:      c.DelegateTool,

		ReviewMode:     c.ReviewModeValue(),
		ReviewProvider: c.ReviewProvider,
		ReviewModel:    c.ReviewModel,
		ReviewEffort:   c.ReviewEffort,

		ReviewCheckCommand:    c.ReviewCheckCommand,
		ReviewMaxChangedLines: c.ReviewMaxChangedLines,
	}
}

// swarmTiersFromConfig 将配置中的 SwarmTier 逐字段转换为编排运行时的 Tier。
func swarmTiersFromConfig(tiers []config.SwarmTier) []swarm.Tier {
	if len(tiers) == 0 {
		return nil
	}
	out := make([]swarm.Tier, 0, len(tiers))
	for _, t := range tiers {
		out = append(out, swarm.Tier{
			ID:           t.ID,
			Name:         t.Name,
			Description:  t.Description,
			ReadOnly:     t.ReadOnly,
			WritePaths:   append([]string(nil), t.WritePaths...),
			Provider:     t.Provider,
			Model:        t.Model,
			Effort:       t.Effort,
			FastMode:     t.FastMode,
			SystemPrompt: t.SystemPrompt,
		})
	}
	return out
}

// swarmPairsFromConfig 把配置里的模型配对转换为编排运行时的配对；Worker 侧映射到子代理侧，
// *string 类型的 effort 通过 swarmStringOrEmpty 做 nil 安全解引用。
func swarmPairsFromConfig(pairs []config.SwarmPair) []swarm.Pair {
	if len(pairs) == 0 {
		return nil
	}
	out := make([]swarm.Pair, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, swarm.Pair{
			ID:           p.ID,
			Name:         p.Name,
			LeadProvider: p.LeadProvider,
			LeadModel:    p.LeadModel,
			LeadEffort:   swarmStringOrEmpty(p.LeadEffort),
			LeadFastMode: p.LeadFastMode,
			BeeProvider:  p.WorkerProvider,
			BeeModel:     p.WorkerModel,
			BeeEffort:    swarmStringOrEmpty(p.WorkerEffort),
			BeeFastMode:  p.WorkerFastMode,
			BeeTiers:     swarmBeeTiersFromConfig(p.BeeTiers),
		})
	}
	return out
}

// swarmBeeTiersFromConfig 转换配对下的档位覆盖配置，深拷贝 FastMode 指针；nil map 保持 nil。
func swarmBeeTiersFromConfig(m map[string]config.SwarmTierOverride) map[string]swarm.TierOverride {
	if m == nil {
		return nil
	}
	out := make(map[string]swarm.TierOverride, len(m))
	for k, v := range m {
		var fastMode *bool
		if v.FastMode != nil {
			fm := *v.FastMode
			fastMode = &fm
		}
		out[k] = swarm.TierOverride{
			Provider: v.Provider,
			Model:    v.Model,
			Effort:   v.Effort,
			FastMode: fastMode,
		}
	}
	return out
}

// swarmStringOrEmpty 解引用可空的字符串指针，nil 时返回空串。
func swarmStringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// 编译期断言：MemoryBackend 实现了编排会话归档接口（仅归档 Codex 子代理会话）。
var _ swarm.SessionArchiver = (*remote.MemoryBackend)(nil)

// swarmStoreAdapter 适配 store.Store 到 swarm.Persister 接口。
type swarmStoreAdapter struct {
	s *store.Store
}

// SetThreadRelation 更新子会话在 store 中的父会话关联与 helper 标记。
func (a swarmStoreAdapter) SetThreadRelation(ctx context.Context, threadID, parentThreadID string, isHelper bool) error {
	if a.s == nil {
		return nil
	}
	return a.s.UpdateThreadRelation(ctx, threadID, parentThreadID, isHelper)
}

// SaveLead 将 LeadRecord 中的 Bees 与排队条目序列化为 JSON 后持久化至 store。
func (a swarmStoreAdapter) SaveLead(ctx context.Context, rec swarm.LeadRecord) error {
	if a.s == nil {
		return nil
	}
	beesData, err := json.Marshal(rec.Bees)
	if err != nil {
		return err
	}
	// 排队条目只在内存里，重启就会丢：与 bees 一并落盘，重启后作为待续跑条目恢复。
	queueData, err := json.Marshal(rec.Queue)
	if err != nil {
		return err
	}
	return a.s.SaveSwarmLead(ctx, store.SwarmLeadRecord{
		LeadThreadID:     rec.LeadThreadID,
		Enabled:          rec.Enabled,
		PlansOnly:        rec.PlansOnly,
		ProjectID:        rec.ProjectID,
		LeadWorkDir:      rec.LeadWorkDir,
		NextIndex:        rec.NextIndex,
		DelegationRounds: rec.DelegationRounds,
		RequestStartedAt: rec.RequestStartedAt,
		BeesJSON:         string(beesData),
		QueueJSON:        string(queueData),
		UpdatedAt:        time.Now().UTC(),
	})
}

// DeleteLead 从 store 删除指定主会话的编排记录。
func (a swarmStoreAdapter) DeleteLead(ctx context.Context, leadThreadID string) error {
	if a.s == nil {
		return nil
	}
	return a.s.DeleteSwarmLead(ctx, leadThreadID)
}

// LoadLeads 从 store 读取全部已保存的编排主代理记录并反序列化。
func (a swarmStoreAdapter) LoadLeads(ctx context.Context) ([]swarm.LeadRecord, error) {
	if a.s == nil {
		return nil, nil
	}
	records, err := a.s.ListSwarmLeads(ctx)
	if err != nil {
		return nil, err
	}
	leads := make([]swarm.LeadRecord, 0, len(records))
	for _, r := range records {
		var bees []swarm.Bee
		if r.BeesJSON != "" {
			if err := json.Unmarshal([]byte(r.BeesJSON), &bees); err != nil {
				return nil, err
			}
		}
		// 排队条目解析失败只丢排队，不影响整条 lead 的加载。
		var queued []swarm.QueuedBeeRecord
		if r.QueueJSON != "" {
			if err := json.Unmarshal([]byte(r.QueueJSON), &queued); err != nil {
				log.Printf("swarm: 解析主会话 %s 的排队条目失败，按空队列处理: %v", r.LeadThreadID, err)
				queued = nil
			}
		}
		leads = append(leads, swarm.LeadRecord{
			LeadThreadID:     r.LeadThreadID,
			Enabled:          r.Enabled,
			PlansOnly:        r.PlansOnly,
			ProjectID:        r.ProjectID,
			LeadWorkDir:      r.LeadWorkDir,
			NextIndex:        r.NextIndex,
			DelegationRounds: r.DelegationRounds,
			RequestStartedAt: r.RequestStartedAt,
			Bees:             bees,
			Queue:            queued,
		})
	}
	return leads, nil
}

// resolveSwarmProject 遍历 backend.ListProjects，根据项目名称或根路径匹配 ref 并返回 projectID 与 ProjectRoot。
func resolveSwarmProject(backend *remote.MemoryBackend) func(ctx context.Context, ref string) (string, string, bool) {
	return func(ctx context.Context, ref string) (string, string, bool) {
		if backend == nil || strings.TrimSpace(ref) == "" {
			return "", "", false
		}
		pList, err := backend.ListProjects(ctx)
		if err != nil {
			return "", "", false
		}
		ref = strings.TrimSpace(ref)
		if ref == "~" || strings.HasPrefix(ref, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				if ref == "~" {
					ref = home
				} else {
					ref = filepath.Join(home, strings.TrimPrefix(ref, "~/"))
				}
			}
		}
		cleanRef := filepath.Clean(ref)
		for _, p := range pList.Projects {
			if strings.EqualFold(p.Name, ref) {
				return p.ID, p.Path, true
			}
		}
		var best *wire.ProjectSummary
		for _, p := range pList.Projects {
			rel, err := filepath.Rel(filepath.Clean(p.Path), cleanRef)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				if best == nil || len(p.Path) > len(best.Path) {
					copy := p
					best = &copy
				}
			}
		}
		if best != nil {
			return best.ID, best.Path, true
		}
		if !filepath.IsAbs(cleanRef) {
			return "", "", false
		}
		if info, err := os.Stat(cleanRef); err != nil || !info.IsDir() {
			return "", "", false
		}
		for dir := cleanRef; ; dir = filepath.Dir(dir) {
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				p, err := backend.AddProjectPath(ctx, dir)
				if err == nil {
					return p.ID, p.Path, true
				}
				return "", "", false
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
		return "", "", false
	}
}

// swarmStartedEntry 记录一条已经入队派发的条目，跨回合去重与回送文案都读它。
// bee 的名字与线程 ID 由派发时才分配，派发成功后回填，所以点名回送前要先等队列落地。
type swarmStartedEntry struct {
	prompt       string // 归一化（TrimSpace）后的 prompt，作为去重键
	task         string // 条目标题，回送主代理时用它说明是哪一条
	name         string // bee 的名字，派发成功后回填
	beeThreadID  string // bee 的线程 ID，用于查它的状态；为空表示还在派发队列里排队
	leadThreadID string // 所属主会话，查 bee 状态时用
}

// swarmPendingEntry 是一条待入队的条目，rec 为它在本次请求去重集合里的登记。
type swarmPendingEntry struct {
	entry swarm.Delegation
	rec   *swarmStartedEntry
}

// swarmStreamJob 表示一条等待串行派发的流式委派条目；done 非空时为仅用于等待队列排空的哨兵任务。
type swarmStreamJob struct {
	leadThreadID string
	projectID    string
	leadWorkDir  string
	entry        swarm.Delegation
	rec          *swarmStartedEntry
	done         chan struct{}
}

// swarmStreamState 维护单个主会话当前轮次的流式解析状态。
type swarmStreamState struct {
	parser      *swarm.StreamParser
	streamed    int             // 本轮已派发的条目数
	suppressed  bool            // 本轮已达委派轮次上限，后续条目全部丢弃
	limitHit    int             // 触发抑制的轮次上限，供回合末回送主代理时说明原因
	counted     bool            // 本轮是否已计入委派轮次
	overCap     int             // 本回合因超过单块条目上限而未派发的条目数
	planRefused int             // 本回合因 Plan 模式限制而未派发的条目数
	settled     int             // 本回合由 settleRemaining 补派的条目数（不计入 streamed，只用于回送时统计已启动数）
	dispatched  map[string]bool // 本轮已派发条目的 prompt（TrimSpace 后），用于回合末按整段回复补派

	// 跨回合去重与本回合回送所需的记录：本回合启动过哪些条目、因去重跳过了哪些。
	started     []*swarmStartedEntry
	skipped     []*swarmStartedEntry
	skippedKeys map[string]bool
}

// swarmDispatchQueue 是单个主会话的无界派发队列。
// 流回调在持锁时同步调用 enqueue，写满就会卡住整个会话，所以入队只追加切片；
// 由唯一的消费协程按 FIFO 串行派发，用容量 1 的唤醒 channel 非阻塞通知新任务。
type swarmDispatchQueue struct {
	mu      sync.Mutex
	pending []swarmStreamJob
	wake    chan struct{}
}

// push 追加任务并唤醒消费协程；唤醒信号已满时直接丢弃，消费协程醒来后会取完整个队列。
func (q *swarmDispatchQueue) push(job swarmStreamJob) {
	q.mu.Lock()
	q.pending = append(q.pending, job)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// pop 取出队首任务；队列为空时返回 false。
func (q *swarmDispatchQueue) pop() (swarmStreamJob, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return swarmStreamJob{}, false
	}
	job := q.pending[0]
	q.pending[0] = swarmStreamJob{}
	q.pending = q.pending[1:]
	return job, true
}

// swarmStreamDispatcher 把助手正文增量解析为委派条目，并按主会话串行派发。
// 派发在独立工作协程中完成，不阻塞轮次协程；同一主会话共用一个队列，
// 保证流式条目先于轮次结束时补漏的条目派发。依赖均以函数字段注入，便于脱离 git 测试。
type swarmStreamDispatcher struct {
	ctx       context.Context
	dispatch  func(ctx context.Context, req swarm.SingleEntryRequest) (swarm.Bee, []string, error)
	rounds    func(leadThreadID string) int
	noteRound func(leadThreadID string) int
	maxRounds func() int
	workDir   func(threadID string) (string, bool)
	notify    func(threadID, text string) error
	// report 把「块没有生效」回送主代理（进入模型历史）；构造后由调用方整体赋值，使用中只读。
	report func(threadID, text string) error
	// maxEntries 返回单个 swarm 块允许的条目数上限（即并发上限），为 nil 或返回非正数表示不限制；
	// 构造后由调用方赋值，使用中只读。
	maxEntries func() int
	planGate   func(threadID string, entry swarm.Delegation) (swarm.Delegation, bool)
	// bees 返回该主会话名下的 bee 快照，用于判断跨回合去重时同一条目的 bee 是否还在跑；
	// 构造后由调用方赋值，使用中只读。
	bees func(leadThreadID string) []swarm.Bee
	// stopBee 停掉指定的 bee（swarm.Manager.StopBee），带 replaces 的条目先停旧 bee 再派新条目；
	// 构造后由调用方赋值，使用中只读。
	stopBee func(ctx context.Context, leadThreadID, beeThreadID string) error

	mu           sync.Mutex
	states       map[string]*swarmStreamState
	queues       map[string]*swarmDispatchQueue
	blockReports map[string]int // 各主会话连续被回送的次数，成功派出条目后清零

	// sent 记录已经派出且尚未落定的条目（主会话 → 归一化 prompt → 记录），跨助手回合与用户请求
	// 一起保留，避免主代理重发同一块、或换个措辞重发时把已经在跑的同一条目再派一次；
	// 新的用户请求开始时只清掉已失败或已停止的记录。
	sent map[string]map[string]*swarmStartedEntry
}

// newSwarmStreamDispatcher 创建流式派发器。
func newSwarmStreamDispatcher(
	ctx context.Context,
	dispatch func(ctx context.Context, req swarm.SingleEntryRequest) (swarm.Bee, []string, error),
	rounds func(leadThreadID string) int,
	noteRound func(leadThreadID string) int,
	workDir func(threadID string) (string, bool),
	notify func(threadID, text string) error,
	maxRounds func() int,
) *swarmStreamDispatcher {
	if ctx == nil {
		ctx = context.Background()
	}
	return &swarmStreamDispatcher{
		ctx:          ctx,
		dispatch:     dispatch,
		rounds:       rounds,
		noteRound:    noteRound,
		maxRounds:    maxRounds,
		workDir:      workDir,
		notify:       notify,
		states:       make(map[string]*swarmStreamState),
		queues:       make(map[string]*swarmDispatchQueue),
		blockReports: make(map[string]int),
		sent:         make(map[string]map[string]*swarmStartedEntry),
	}
}

// roundLimit 返回本次请求实际生效的委派轮次上限；未注入配置读取函数时使用默认上限。
func (d *swarmStreamDispatcher) roundLimit() int {
	if d.maxRounds != nil {
		return d.maxRounds()
	}
	return swarm.MaxDelegationRounds
}

// swarmBlockReportLimit 限制同一主会话连续被回送主代理的次数，达到上限后只写系统通知，
// 避免主代理与派发器来回重试；主代理有条目成功派出时计数清零。
const swarmBlockReportLimit = 2

// swarmDrainTimeout 是一轮结束时等待本轮派发落地的最长时间；超时后只记日志，后台派发照常继续。
const swarmDrainTimeout = 60 * time.Second

// entryCap 返回单个 swarm 块允许派发的条目数上限；未注入或返回非正数时返回 0，表示不限制。
func (d *swarmStreamDispatcher) entryCap() int {
	if d.maxEntries == nil {
		return 0
	}
	if limit := d.maxEntries(); limit > 0 {
		return limit
	}
	return 0
}

// beginRoundLocked 在派发本轮首个条目之前校验委派轮次上限：允许时记一次轮次并置 counted，
// 已达上限时置抑制标记并记下上限值。调用方需持有 d.mu，保证流式与回合末补派不会重复计数。
func (d *swarmStreamDispatcher) beginRoundLocked(st *swarmStreamState, threadID string) bool {
	if st.suppressed {
		return false
	}
	if st.counted {
		return true
	}
	// 0 表示不限轮次：配置成 0 时直接放行，与 swarm.Manager 的判定口径一致。
	if limit := d.roundLimit(); limit > 0 && d.rounds(threadID) >= limit {
		st.suppressed = true
		st.limitHit = limit
		return false
	}
	d.noteRound(threadID)
	st.counted = true
	return true
}

// reportBlockProblem 把「块里的条目没有派出去」回送主代理。
// AppendNotice 写的是 system/info 消息，构建模型历史时会被跳过，主代理根本看不到，
// 于是停下来等；改走 SendSwarmReport 投递后主代理能在下一轮修正块或自己接手。
// 连续回送达到上限后只写系统通知，防止两边来回死循环。
func (d *swarmStreamDispatcher) reportBlockProblem(threadID, text string) {
	d.mu.Lock()
	n := d.blockReports[threadID]
	escalate := d.report != nil && n < swarmBlockReportLimit
	if escalate {
		d.blockReports[threadID] = n + 1
	}
	report := d.report
	d.mu.Unlock()

	if !escalate {
		_ = d.notify(threadID, swarm.NoticePrefix+text)
		return
	}
	if err := report(threadID, swarm.NoticePrefix+text); err != nil {
		_ = d.notify(threadID, swarm.NoticePrefix+text)
	}
}

// beginRequest 开始一次新的用户请求：只保留仍在排队或仍在运行的去重记录，已落定的条目
// （完成、失败、停止）一律清掉，这样主代理在新请求里要求重做同一件事时还能重新派发。
// 调用点与 mgr.BeginLeadRequest 一致——用户消息进入主会话、重置委派轮次计时的同一处。
func (d *swarmStreamDispatcher) beginRequest(threadID string) {
	d.mu.Lock()
	for key, rec := range d.sent[threadID] {
		if !d.sentStillRunningLocked(rec) {
			delete(d.sent[threadID], key)
		}
	}
	d.mu.Unlock()
}

// rememberSentLocked 把一条即将入队的条目记入去重集合，并挂到本回合的已启动列表。
// 调用方需持有 d.mu。
func (d *swarmStreamDispatcher) rememberSentLocked(threadID string, entry swarm.Delegation, st *swarmStreamState) *swarmStartedEntry {
	rec := &swarmStartedEntry{
		prompt:       strings.TrimSpace(entry.Prompt),
		task:         strings.TrimSpace(entry.Task),
		leadThreadID: threadID,
	}
	if rec.task == "" {
		rec.task = rec.prompt
	}
	if d.sent == nil {
		d.sent = make(map[string]map[string]*swarmStartedEntry)
	}
	if d.sent[threadID] == nil {
		d.sent[threadID] = make(map[string]*swarmStartedEntry)
	}
	d.sent[threadID][rec.prompt] = rec
	st.started = append(st.started, rec)
	return rec
}

// forgetSent 撤销一条记录的登记：派发失败时调用，让主代理重发同一条目时还能再派一次。
func (d *swarmStreamDispatcher) forgetSent(rec *swarmStartedEntry) {
	if rec == nil {
		return
	}
	d.mu.Lock()
	if entries := d.sent[rec.leadThreadID]; entries != nil && entries[rec.prompt] == rec {
		delete(entries, rec.prompt)
	}
	d.mu.Unlock()
}

// sentStillActiveLocked 判断一条已登记的记录是否还占着这个 prompt：排队中、运行中或已完成都算占用，
// 已失败或已停止则允许重新派发。调用方需持有 d.mu。
func (d *swarmStreamDispatcher) sentStillActiveLocked(rec *swarmStartedEntry) bool {
	// 还没分到线程 ID：要么在派发队列里排队，要么 manager 因并发已满把它挂进了排队列表，
	// 两种情况都还没开始跑、也没有失败可谈，按占用处理。
	if rec.beeThreadID == "" || d.bees == nil {
		return true
	}
	for _, bee := range d.bees(rec.leadThreadID) {
		if bee.ThreadID != rec.beeThreadID {
			continue
		}
		return bee.Status != swarm.StatusFailed && bee.Status != swarm.StatusStopped
	}
	// 快照里找不到（bee 已被清理）：当作可以重派，宁可多派一次也不让主代理卡在这里。
	return false
}

// sentStillRunningLocked 判断一条已登记的记录是否还在跑：排队中和运行中算在跑，
// 已完成、已失败或已停止都算落定。用于跨用户请求的去重——上一件事做完了，
// 新请求里再要一次就该重新派发。调用方需持有 d.mu。
func (d *swarmStreamDispatcher) sentStillRunningLocked(rec *swarmStartedEntry) bool {
	// 还没分到线程 ID：要么在派发队列里排队，要么 manager 因并发已满把它挂进了排队列表，
	// 两种情况都还没开始跑，按在跑处理。
	if rec.beeThreadID == "" || d.bees == nil {
		return true
	}
	for _, bee := range d.bees(rec.leadThreadID) {
		if bee.ThreadID != rec.beeThreadID {
			continue
		}
		return bee.Status == swarm.StatusRunning
	}
	// 快照里找不到（bee 已被清理）：当作可以重派，宁可多派一次也不让主代理卡在这里。
	return false
}

// activeSentLocked 返回同一条目仍在占用的记录：先按归一化 prompt 查（同一请求内完全相同的
// brief 在 bee 完成后仍算重复），查不到再按任务标题查仍在跑的记录，最后回退到 manager
// 快照里同 task 且仍在跑的 bee。没有匹配或已失败/已停止/已完成时返回 nil。
// 调用方需持有 d.mu。
func (d *swarmStreamDispatcher) activeSentLocked(threadID, prompt, task string) *swarmStartedEntry {
	key := strings.TrimSpace(prompt)
	if rec := d.sent[threadID][key]; rec != nil && d.sentStillActiveLocked(rec) {
		return rec
	}
	// 主代理换了措辞重写同一条目时 prompt 已经不同，按任务标题兜底，避免同一件事派两次；
	// 这里只认还在跑的记录，已经完成的同名任务不该拦住新条目（例如又一个「修复编译错误」）。
	task = strings.TrimSpace(task)
	if task == "" {
		return nil
	}
	for _, rec := range d.sent[threadID] {
		if rec == nil || !strings.EqualFold(strings.TrimSpace(rec.task), task) || !d.sentStillRunningLocked(rec) {
			continue
		}
		return rec
	}
	if d.bees == nil {
		return nil
	}
	// 登记表里没有（例如进程重启后只剩快照）：只合成一条跳过回执，不写回 d.sent，
	// 免得派发失败时还要额外清理一条并不存在的登记。
	for _, bee := range d.bees(threadID) {
		if bee.Status != swarm.StatusRunning || !strings.EqualFold(strings.TrimSpace(bee.Task), task) {
			continue
		}
		// 回执里用 bee 自己的任务标题：那才是主代理认得的那一条。
		beeTask := strings.TrimSpace(bee.Task)
		if beeTask == "" {
			beeTask = task
		}
		return &swarmStartedEntry{task: beeTask, prompt: key, name: bee.Name, beeThreadID: bee.ThreadID, leadThreadID: threadID}
	}
	return nil
}

// skipSentLocked 判断该 prompt 是否应因跨回合去重而跳过，跳过时记入本回合的跳过列表，
// 供回合末 reportSkipped 汇总回送。同一回合里流式阶段与回合末补派会各看一遍同一条目，
// 靠 skippedKeys 保证只记一次。调用方需持有 d.mu。
func (d *swarmStreamDispatcher) skipSentLocked(threadID string, st *swarmStreamState, prompt, task string) bool {
	rec := d.activeSentLocked(threadID, prompt, task)
	if rec == nil {
		return false
	}
	if st.skippedKeys == nil {
		st.skippedKeys = make(map[string]bool)
	}
	if !st.skippedKeys[rec.prompt] {
		st.skippedKeys[rec.prompt] = true
		st.skipped = append(st.skipped, rec)
	}
	return true
}

// describeStarted 把若干条目渲染成「任务（bee 名）」列表，供回送主代理时点名。
// bee 名在派发时才分配，还没落地时写「排队中」。
func describeStarted(entries []*swarmStartedEntry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.name
		if name == "" {
			name = "排队中"
		}
		parts = append(parts, fmt.Sprintf("%s（%s）", e.task, name))
	}
	return strings.Join(parts, "、")
}

// reset 开始新一轮助手流：替换解析器并清空本轮计数与抑制标记。
func (d *swarmStreamDispatcher) reset(threadID string) {
	d.mu.Lock()
	d.states[threadID] = &swarmStreamState{parser: swarm.NewStreamParser(), dispatched: make(map[string]bool)}
	d.mu.Unlock()
}

// clear 丢弃指定主会话的流式状态；已入队的派发不受影响。
func (d *swarmStreamDispatcher) clear(threadID string) {
	d.mu.Lock()
	delete(d.states, threadID)
	d.mu.Unlock()
}

func swarmPlanGate(settings swarm.Settings, entry swarm.Delegation) (swarm.Delegation, bool) {
	if strings.TrimSpace(entry.Tier) == "" {
		if tier, ok := swarm.FindTier(settings.Tiers, settings.DefaultTier); ok && tier.ReadOnly {
			return entry, true
		}
		for _, tier := range settings.Tiers {
			if tier.ReadOnly {
				entry.Tier = tier.ID
				return entry, true
			}
		}
		return entry, false
	}
	tier, ok := swarm.FindTier(settings.Tiers, entry.Tier)
	return entry, ok && tier.ReadOnly
}

func (d *swarmStreamDispatcher) gateEntry(threadID string, st *swarmStreamState, entry swarm.Delegation, report bool) (swarm.Delegation, bool) {
	if d.planGate == nil {
		return entry, true
	}
	gated, ok := d.planGate(threadID, entry)
	if !ok {
		if st.dispatched == nil {
			st.dispatched = make(map[string]bool)
		}
		key := strings.TrimSpace(entry.Prompt)
		if report && !st.dispatched[key] {
			st.planRefused++
		}
		st.dispatched[key] = true
	}
	return gated, ok
}

// feed 追加一段助手正文增量，条目在闭合大括号到达时立即入队派发。
// 本轮首个条目先校验委派轮次上限；超限后置抑制标记并丢弃本轮全部条目，
// 原因在回合末由 settleTurn 回送主代理，feed 里只记录，不阻塞流回调。
func (d *swarmStreamDispatcher) feed(threadID, projectID, delta string) {
	d.mu.Lock()
	st, ok := d.states[threadID]
	if !ok {
		st = &swarmStreamState{parser: swarm.NewStreamParser()}
		d.states[threadID] = st
	}
	var entries []swarmPendingEntry
	for _, entry := range st.parser.Feed(delta) {
		var allowed bool
		entry, allowed = d.gateEntry(threadID, st, entry, true)
		if !allowed {
			continue
		}
		// 跨回合去重先于轮次与条目上限：跳过的条目既不占名额，也不消耗派发轮次。
		// 带 replaces 的条目本来就要换掉在跑的 bee，跨回合去重会让取代失效，只保留同回合的 prompt 去重。
		if strings.TrimSpace(entry.Replaces) != "" {
			if st.dispatched[strings.TrimSpace(entry.Prompt)] {
				continue
			}
		} else if d.skipSentLocked(threadID, st, entry.Prompt, entry.Task) {
			continue
		}
		if !d.beginRoundLocked(st, threadID) {
			continue // 已抑制：本轮条目全部丢弃
		}
		if st.dispatched == nil {
			st.dispatched = make(map[string]bool)
		}
		// 单块条目数硬上限：超出的条目不入队、不启动，只记一笔等回合末回送主代理。
		// 同时记入已派集合，避免回合末按整段回复补派时又把它当作未派发条目派出去。
		if limit := d.entryCap(); limit > 0 && st.streamed >= limit {
			st.overCap++
			st.dispatched[strings.TrimSpace(entry.Prompt)] = true
			continue
		}
		st.streamed++
		st.dispatched[strings.TrimSpace(entry.Prompt)] = true
		entries = append(entries, swarmPendingEntry{entry: entry, rec: d.rememberSentLocked(threadID, entry, st)})
	}
	d.mu.Unlock()

	d.enqueue(threadID, projectID, entries)
}

// submit 派发 swarm_delegate 工具提交的条目。它与 swarm 块共用同一份本轮状态：轮次上限、
// 单块条目数上限与按 prompt 去重都照常生效，所以同一条目在工具和块里各写一遍只会派一次。
// 返回的文本交给模型，说明启动了几个、其余为什么没启动；派发本身仍在队列里异步完成，
// 之后的派发失败照旧经 reportBlockProblem 回送主代理。
func (d *swarmStreamDispatcher) submit(threadID, projectID string, entries []swarm.Delegation) string {
	d.mu.Lock()
	st, ok := d.states[threadID]
	if !ok {
		st = &swarmStreamState{parser: swarm.NewStreamParser()}
		d.states[threadID] = st
	}
	if st.dispatched == nil {
		st.dispatched = make(map[string]bool)
	}
	var queued []swarmPendingEntry
	var skippedEntries []*swarmStartedEntry
	var duplicates, overCap, refused, skipped, planRefused int
	for _, entry := range entries {
		var allowed bool
		entry, allowed = d.gateEntry(threadID, st, entry, false)
		if !allowed {
			planRefused++
			continue
		}
		key := strings.TrimSpace(entry.Prompt)
		if st.dispatched[key] {
			duplicates++
			continue
		}
		// 跨回合去重：同 prompt 或同任务的 bee 还在跑就不再派，跳过的条目不计入上限。
		// 带 replaces 的条目要换掉在跑的那个 bee，跳过它等于放弃取代，所以不做这项检查。
		// 工具调用的结果直接交给模型，这里当场点名，不再留给回合末的汇总回送。
		if strings.TrimSpace(entry.Replaces) == "" {
			if rec := d.activeSentLocked(threadID, key, entry.Task); rec != nil {
				skipped++
				skippedEntries = append(skippedEntries, rec)
				continue
			}
		}
		if !d.beginRoundLocked(st, threadID) {
			refused++
			continue
		}
		if limit := d.entryCap(); limit > 0 && st.streamed >= limit {
			overCap++
			continue
		}
		st.streamed++
		st.dispatched[key] = true
		queued = append(queued, swarmPendingEntry{entry: entry, rec: d.rememberSentLocked(threadID, entry, st)})
	}
	limitHit := st.limitHit
	capLimit := d.entryCap()
	d.mu.Unlock()

	d.enqueue(threadID, projectID, queued)

	parts := []string{fmt.Sprintf("Started %d bee(s).", len(queued))}
	if planRefused > 0 {
		parts = append(parts, fmt.Sprintf("%d entry(ies) not started: plan mode allows only read-only tier bees.", planRefused))
	}
	if duplicates > 0 {
		parts = append(parts, fmt.Sprintf("%d entry(ies) skipped: same prompt already dispatched this turn.", duplicates))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d entry(ies) skipped: already dispatched earlier and still running (%s); do not resend them.", skipped, describeStarted(skippedEntries)))
	}
	if overCap > 0 {
		parts = append(parts, fmt.Sprintf("%d entry(ies) not started: at most %d bees per turn. Send them after these bees report.", overCap, capLimit))
	}
	if refused > 0 {
		parts = append(parts, fmt.Sprintf("%d entry(ies) not started: the request reached its limit of %d delegation rounds. Summarize what you have or finish the work yourself.", refused, limitHit))
	}
	return strings.Join(parts, " ")
}

// takeState 取出并删除指定主会话的流式状态。
func (d *swarmStreamDispatcher) takeState(threadID string) *swarmStreamState {
	d.mu.Lock()
	defer d.mu.Unlock()
	st, ok := d.states[threadID]
	if !ok {
		return nil
	}
	delete(d.states, threadID)
	return st
}

// finishTurn 读取并清空指定主会话的流式状态，返回本轮已派发的条目数与是否已被轮次上限抑制。
func (d *swarmStreamDispatcher) finishTurn(threadID string) (streamed int, suppressed bool) {
	st := d.takeState(threadID)
	if st == nil {
		return 0, false
	}
	return st.streamed, st.suppressed
}

// settleTurn 结束一轮助手流：读取并清空流式状态，按整段回复补派流式阶段没有派出的条目，
// 再最多等待 swarmDrainTimeout 让本轮派发落地。补派条目仍走同一派发队列，排在已派发条目之后，
// 维持「派发完成前仍视为运行中」的既有语义，让排队中的跟进消息不会抢在 bee 前启动。
func (d *swarmStreamDispatcher) settleTurn(threadID, projectID, fullText string) (streamed int, suppressed bool) {
	st := d.takeState(threadID)
	if st == nil {
		// 有的 provider 不推流式增量，只在回合末给出全文：这时没有流式状态，用空状态记账。
		st = &swarmStreamState{}
	}
	d.settleRemaining(threadID, projectID, fullText, st)
	d.reportOverCap(threadID, st)
	d.reportPlanRefused(threadID, st)
	d.reportSkipped(threadID, st)
	d.drain(threadID, swarmDrainTimeout)
	return st.streamed, st.suppressed
}

// reportOverCap 在单个 swarm 块的条目数超过并发上限时回送主代理：告知已启动几个、
// 还有几个没有派发，并让它等这批 bee 汇报后再发新块。每回合最多回送一次。
func (d *swarmStreamDispatcher) reportOverCap(threadID string, st *swarmStreamState) {
	if st.overCap <= 0 || st.suppressed {
		return
	}
	limit := d.entryCap()
	started := st.streamed + st.settled
	d.reportBlockProblem(threadID, fmt.Sprintf("本块条目数超过并发上限 %d：已启动前 %d 个，其余 %d 个条目未派发。请等这批子代理汇报后，在新的 swarm 块里重新发送剩余条目，每个块的条目数不要超过 %d。", limit, started, st.overCap, limit))
}

// reportPlanRefused 汇总本回合被 Plan 模式限制的条目。
func (d *swarmStreamDispatcher) reportPlanRefused(threadID string, st *swarmStreamState) {
	if st.planRefused > 0 {
		d.reportBlockProblem(threadID, fmt.Sprintf("当前为 Plan 模式：只能派发只读档的 bee，本回合有 %d 个其他档的条目没有派发。请把调研改派只读档，改动等计划获批后再派。", st.planRefused))
	}
}

// reportParseFailure 在委派块整块解析失败时回送主代理。本回合已经流式派出的条目必须点名：
// 否则主代理不知道它们已经在跑，会照旧重发整块，同一批工作被派两次。
func (d *swarmStreamDispatcher) reportParseFailure(threadID string, st *swarmStreamState, err error) {
	if len(st.started) == 0 {
		d.reportBlockProblem(threadID, "委派块没能解析："+err.Error()+"。请修正后重新输出 ```swarm 块，或自己完成这些工作。")
		return
	}
	// bee 的名字在派发时才分配，先等队列落地再回送，文案里才能点名。
	d.drain(threadID, swarmDrainTimeout)
	d.reportBlockProblem(threadID, fmt.Sprintf("委派块没能解析：%s。本回合已启动 %d 条：%s。这些已在运行，不要重发；只在新的 swarm 块里输出尚未启动的条目。", err.Error(), len(st.started), describeStarted(st.started)))
}

// reportSkipped 汇总本回合因跨回合去重跳过的条目，每回合最多回送一次。
func (d *swarmStreamDispatcher) reportSkipped(threadID string, st *swarmStreamState) {
	if len(st.skipped) == 0 {
		return
	}
	d.reportBlockProblem(threadID, fmt.Sprintf("跳过 %d 条此前已经派出的条目：%s。这些已在运行，不要重发；只在新的 swarm 块里输出尚未启动的条目。", len(st.skipped), describeStarted(st.skipped)))
}

// settleRemaining 按整段回复补齐本轮没有派出去的条目：以 prompt 文本为键比对已派集合，
// 因此不依赖流式计数与整块解析结果是否一致。块本身没有生效时把原因回送主代理。
func (d *swarmStreamDispatcher) settleRemaining(threadID, projectID, fullText string, st *swarmStreamState) {
	entries, found, err := swarm.ParseDelegations(fullText)
	if err == nil {
		d.mu.Lock()
		allowed := entries[:0]
		for _, entry := range entries {
			entry, ok := d.gateEntry(threadID, st, entry, true)
			if ok {
				allowed = append(allowed, entry)
			}
		}
		entries = allowed
		d.mu.Unlock()
	}
	if st.suppressed {
		bee := "本轮委派块没有派出"
		if err == nil && len(entries) > 0 {
			bee = fmt.Sprintf("本轮委派块里的 %d 条没有派出", len(entries))
		}
		d.reportBlockProblem(threadID, fmt.Sprintf("%s：已达本次请求的 %d 轮派发上限，请汇总已有结果或自己完成剩余工作。", bee, st.limitHit))
		return
	}
	if !found {
		return
	}
	if err != nil {
		d.reportParseFailure(threadID, st, err)
		return
	}

	d.mu.Lock()
	pending := make([]swarmPendingEntry, 0, len(entries))
	for _, entry := range entries {
		key := strings.TrimSpace(entry.Prompt)
		if st.dispatched[key] {
			continue
		}
		// 跨回合去重：同 prompt 或同任务的 bee 还在跑就不再补派，回合末由 reportSkipped 汇总回送。
		// 带 replaces 的条目要换掉在跑的那个 bee，这里放行，交给 dispatchJob 先停旧再派新。
		if strings.TrimSpace(entry.Replaces) == "" && d.skipSentLocked(threadID, st, key, entry.Task) {
			continue
		}
		pending = append(pending, swarmPendingEntry{entry: entry})
	}
	// 空块（[]）与全部已派发都无需补派；只有真的有条目没出去才可能触发上限回送。
	if len(pending) == 0 {
		d.mu.Unlock()
		return
	}
	if !d.beginRoundLocked(st, threadID) {
		d.mu.Unlock()
		d.reportBlockProblem(threadID, fmt.Sprintf("本轮委派块里的 %d 条没有派出：已达本次请求的 %d 轮派发上限，请汇总已有结果或自己完成剩余工作。", len(pending), st.limitHit))
		return
	}
	// 同一回合的补派也不得越过单块条目上限：只派 cap-streamed 条，其余计入 overCap 等回合末回送。
	if limit := d.entryCap(); limit > 0 {
		allowedEntries := limit - st.streamed
		if allowedEntries < 0 {
			allowedEntries = 0
		}
		if len(pending) > allowedEntries {
			st.overCap += len(pending) - allowedEntries
			pending = pending[:allowedEntries]
		}
	}
	for i := range pending {
		pending[i].rec = d.rememberSentLocked(threadID, pending[i].entry, st)
	}
	st.settled += len(pending)
	d.mu.Unlock()
	d.enqueue(threadID, projectID, pending)
}

// stopReplacedBee 在派发带 replaces 的条目之前停掉被点名且仍在跑的 bee，并撤销它在去重集合里的登记，
// 免得旧记录把取代条目挡住。找不到该 bee 或停止失败时只回送一条提示，本条照常派发。
func (d *swarmStreamDispatcher) stopReplacedBee(job swarmStreamJob) {
	name := strings.TrimSpace(job.entry.Replaces)
	if name == "" || d.bees == nil || d.stopBee == nil {
		return
	}
	var target swarm.Bee
	found := false
	for _, bee := range d.bees(job.leadThreadID) {
		if bee.Status == swarm.StatusRunning && strings.EqualFold(strings.TrimSpace(bee.Name), name) {
			target = bee
			found = true
			break
		}
	}
	if !found {
		_ = d.notify(job.leadThreadID, swarm.NoticePrefix+fmt.Sprintf("没有找到名为 %s 的在跑子代理，本条已按新条目派出。", name))
		return
	}
	if err := d.stopBee(d.ctx, job.leadThreadID, target.ThreadID); err != nil {
		_ = d.notify(job.leadThreadID, swarm.NoticePrefix+fmt.Sprintf("停止子代理 %s 失败：%v，本条已按新条目派出。", name, err))
		return
	}
	// 被取代的 bee 已经不是「在跑」了：删掉它的登记，同 prompt 重发才不会被误判为重复。
	d.mu.Lock()
	for key, rec := range d.sent[job.leadThreadID] {
		if rec != nil && rec.beeThreadID == target.ThreadID {
			delete(d.sent[job.leadThreadID], key)
		}
	}
	d.mu.Unlock()
}

// enqueue 把条目按顺序追加到该主会话的派发队列，必要时惰性启动消费协程。
func (d *swarmStreamDispatcher) enqueue(threadID, projectID string, entries []swarmPendingEntry) {
	if len(entries) == 0 {
		return
	}
	dir, _ := d.workDir(threadID)

	d.mu.Lock()
	q, ok := d.queues[threadID]
	if !ok {
		q = &swarmDispatchQueue{wake: make(chan struct{}, 1)}
		d.queues[threadID] = q
		go d.runQueue(q)
	}
	d.mu.Unlock()

	for _, pending := range entries {
		q.push(swarmStreamJob{leadThreadID: threadID, projectID: projectID, leadWorkDir: dir, entry: pending.entry, rec: pending.rec})
	}
}

// drain 等待该主会话队列清空且没有正在执行的任务：哨兵任务排在队尾，FIFO 队列保证它之前的任务都已处理。
// 超过 timeout 仍未清空时只记日志，后台派发照常继续，不再拖着轮次状态不放。
func (d *swarmStreamDispatcher) drain(threadID string, timeout time.Duration) {
	d.mu.Lock()
	q := d.queues[threadID]
	d.mu.Unlock()
	if q == nil {
		return
	}
	done := make(chan struct{})
	q.push(swarmStreamJob{done: done})

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		log.Printf("swarm: 主会话 %s 的派发队列在 %s 内没有清空，改为后台继续派发", threadID, timeout)
	}
}

// runQueue 串行消费派发队列，保证同一主会话内的条目按入队顺序派出。
func (d *swarmStreamDispatcher) runQueue(q *swarmDispatchQueue) {
	for {
		job, ok := q.pop()
		if !ok {
			<-q.wake
			continue
		}
		if job.done != nil {
			close(job.done)
			continue
		}
		d.dispatchJob(job)
	}
}

// dispatchJob 派发单条条目，并把派发结果与提示回送主会话。
func (d *swarmStreamDispatcher) dispatchJob(job swarmStreamJob) {
	// 带 replaces 的条目先停掉被取代的在跑 bee，否则新旧两个 bee 会同时干活。
	d.stopReplacedBee(job)
	bee, notices, err := d.dispatch(d.ctx, swarm.SingleEntryRequest{
		LeadThreadID: job.leadThreadID,
		ProjectID:    job.projectID,
		LeadWorkDir:  job.leadWorkDir,
		Entry:        job.entry,
	})
	if err != nil {
		// 派发失败就不该再占着去重名额，否则主代理重发同一条目会被误判成重复而跳过。
		d.forgetSent(job.rec)
		if errors.Is(err, swarm.ErrRequestTimeout) {
			// 超时后派发一律被拒：告知主代理汇总现有结果，等用户发新消息后重新计时。
			d.reportBlockProblem(job.leadThreadID, "本轮委派块里的条目没有派出："+err.Error()+"，计时从用户最新消息起算。请汇总已有结果；如需继续派发，请用户发送新消息。")
			return
		}
		d.reportBlockProblem(job.leadThreadID, "子代理没能派出："+err.Error()+"，请重新规划或自己完成这部分工作。")
		return
	}
	// 记下派发时才分配的 bee 名与线程 ID：跨回合去重要靠线程 ID 查状态，点名回送要靠名字。
	if job.rec != nil {
		d.mu.Lock()
		job.rec.name = bee.Name
		job.rec.beeThreadID = bee.ThreadID
		d.mu.Unlock()
	}
	// 有条目成功派出说明块生效了，清空连续回送计数。
	d.mu.Lock()
	delete(d.blockReports, job.leadThreadID)
	d.mu.Unlock()
	for _, n := range notices {
		_ = d.notify(job.leadThreadID, swarm.NoticePrefix+n)
	}
}

// wireSwarm 将编排管理器与 remote MemoryBackend 的钩子与队列进行双向连接。
func wireSwarm(ctx context.Context, mgr *swarm.Manager, backend *remote.MemoryBackend, sendToBees bool, leadPlansOnly ...func(threadID string) bool) {
	wireSwarmWithPreface(ctx, mgr, backend, sendToBees, nil, leadPlansOnly...)
}

// wireSwarmWithPreface 同 wireSwarm，并在提示词前额外注入协调员 preface（preface 可为 nil）。
// 旧调用方只持有全局开关：把 sendToBees/leadPlansOnly 折算成「跟随设置」下的有效模式。
func wireSwarmWithPreface(ctx context.Context, mgr *swarm.Manager, backend *remote.MemoryBackend, sendToBees bool, preface func(threadID string) string, leadPlansOnly ...func(threadID string) bool) {
	var plansOnlyResolver func(threadID string) bool
	if len(leadPlansOnly) > 0 {
		plansOnlyResolver = leadPlansOnly[0]
	}
	wireSwarmWithModes(ctx, mgr, backend, nil, preface, func(threadID string) string {
		global := swarm.GlobalMode{AlwaysBees: sendToBees}
		if plansOnlyResolver != nil {
			global.LeadPlansOnly = plansOnlyResolver(threadID)
		}
		return swarm.EffectiveMode(swarm.ModeInherited, global)
	})
}

// hostPolicyCaps 返回宿主规则渲染的能力开关：Swarm 由调用处已确认的会话编排启用状态决定
// （本函数只在启用分支内使用），看板、总管身份段、项目上下文与自动化目前没有可用的独立开关，恒为 true；
// 各段内容是否为空仍由原模块自行判定，空段由 policy.Render 跳过。
func hostPolicyCaps() policy.Capabilities {
	return policy.Capabilities{Swarm: true, Kanban: true, Context: true, Automation: true}
}

// swarmStaticPolicy 保存主代理策略与档位说明等静态文本，用于判断通知轮次规则是否发生变化。
type swarmStaticPolicy struct {
	lead  string
	tiers string
}

// swarmLeadPolicyTracker 维护各主线程最近一次完整注入的静态策略文本，
// 供通知轮次决定下发完整规则还是精简提醒。
type swarmLeadPolicyTracker struct {
	mu   sync.Mutex
	last map[string]swarmStaticPolicy
}

func newSwarmLeadPolicyTracker() *swarmLeadPolicyTracker {
	return &swarmLeadPolicyTracker{
		last: make(map[string]swarmStaticPolicy),
	}
}

// Forget 清理指定会话的静态规则记录，供会话删除时释放。
func (t *swarmLeadPolicyTracker) Forget(threadID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.last, threadID)
}

// RenderSections 根据是否为通知轮次及静态文本是否有变，返回本次需渲染的 policy.Sections。
// 非通知轮次或静态文本无记录/变化时返回完整 Sections 并更新记录；
// 通知轮次且静态文本与最近一次完整注入一致时返回精简 Sections（SwarmLead 换为精简提醒，SwarmTiers 留空）。
func (t *swarmLeadPolicyTracker) RenderSections(threadID string, isNotice bool, plansOnly bool, delegateTool bool, leadPolicy, tiers, limits string) policy.Sections {
	current := swarmStaticPolicy{
		lead:  leadPolicy,
		tiers: tiers,
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	last, ok := t.last[threadID]
	if isNotice && ok && last == current {
		return policy.Sections{
			SwarmLead:   swarm.LeadNoticePolicy(plansOnly, delegateTool),
			SwarmTiers:  "",
			SwarmLimits: limits,
		}
	}

	t.last[threadID] = current
	return policy.Sections{
		SwarmLead:   leadPolicy,
		SwarmTiers:  tiers,
		SwarmLimits: limits,
	}
}

// wireSwarmWithModes 是编排接线的主体：modeResolver 返回会话当前生效的工作模式
// （swarm.Mode* 常量，已按会话级覆盖与全局设置解析）。
//
// mcpSrv 非空时把 swarm_delegate 工具接到派发器上；工具只对开启了 Swarm.DelegateTool 的主会话可见。
func wireSwarmWithModes(ctx context.Context, mgr *swarm.Manager, backend *remote.MemoryBackend, mcpSrv *mcp.Server, preface func(threadID string) string, modeResolver func(threadID string) string) {
	if mgr == nil || backend == nil {
		return
	}

	// 派发 bee 前按当前设置检索相关长期记忆，附加到 bee 提示词末尾。
	mgr.SetMemoryRecall(newBeeMemoryRecall(mgr))

	// effectiveMode 返回会话生效模式；没有解析器时视为协作模式，与开启 Swarm 的默认行为一致。
	effectiveMode := func(threadID string) string {
		if modeResolver == nil {
			return swarm.ModeCollaborative
		}
		return swarm.NormalizeMode(modeResolver(threadID))
	}

	// bee 登记晚于首轮消息，登记前的首轮要看辅助会话标记兜底。
	isBeeThread := func(threadID string) bool {
		if _, ok := mgr.BeeOwner(threadID); ok {
			return true
		}
		thread, err := backend.GetThread(ctx, threadID)
		return err == nil && thread.IsHelper
	}

	backend.SetSwarmLeadPlansOnlyFunc(func(threadID string) bool {
		if !mgr.Settings().Enabled || !mgr.State(threadID).Enabled || effectiveMode(threadID) != swarm.ModeLeadPlansOnly || isBeeThread(threadID) {
			return false
		}
		thread, err := backend.GetThread(ctx, threadID)
		return err == nil && (thread.ParentThreadID == nil || *thread.ParentThreadID == "")
	})

	// canAutoDelegate 判断该会话是否允许自动委派：两个开关开启、会话启用且不是 bee。
	canAutoDelegate := func(threadID string) bool {
		if !mgr.Settings().Enabled || !mgr.Settings().AutoDelegate || !mgr.State(threadID).Enabled {
			return false
		}
		if isBeeThread(threadID) {
			return false
		}
		return true
	}

	// maxRounds 返回配置的委派轮次上限，原样透传：0 表示不限，负值同样按不限处理。
	maxRounds := func() int {
		return mgr.Settings().MaxRounds
	}

	dispatcher := newSwarmStreamDispatcher(ctx,
		mgr.DispatchSingleEntry,
		mgr.DelegationRounds,
		mgr.NoteDelegationRound,
		backend.ThreadWorkDir,
		backend.AppendNotice,
		maxRounds,
	)
	// 块没有生效时必须回送主代理：AppendNotice 写的 system 消息不进模型历史，
	// 主代理看不到就会停下来等，所以改走会进入对话的 SendSwarmReport。
	dispatcher.report = func(threadID, text string) error {
		return backend.SendSwarmReport(ctx, threadID, text)
	}
	// 单个 swarm 块的条目数硬上限取用户设置的并发上限：超出的条目不入队，回合末回送主代理另发一块。
	dispatcher.maxEntries = mgr.MaxBeesLimit
	dispatcher.planGate = func(threadID string, entry swarm.Delegation) (swarm.Delegation, bool) {
		thread, err := backend.GetThread(ctx, threadID)
		if err != nil || thread.InteractionMode != "plan" {
			return entry, true
		}
		return swarmPlanGate(mgr.Settings(), entry)
	}
	// 跨回合去重要看 bee 的实时状态：同一条目已经派出且仍在跑时不再派第二次。
	dispatcher.bees = func(threadID string) []swarm.Bee {
		return mgr.State(threadID).Bees
	}
	// 带 replaces 的条目要先把被取代的 bee 停掉再派发新条目。
	dispatcher.stopBee = mgr.StopBee

	if mcpSrv != nil {
		mcpSrv.SetSwarm(&mcp.SwarmHooks{
			Available: func(threadID string) bool {
				return mgr.Settings().DelegateTool && canAutoDelegate(threadID)
			},
			Delegate: func(ctx context.Context, threadID string, raw []map[string]any) (string, error) {
				entries := make([]swarm.Delegation, 0, len(raw))
				for _, item := range raw {
					if entry, ok := swarm.ParseEntry(item); ok {
						entries = append(entries, entry)
					}
				}
				if len(entries) == 0 {
					return "", errors.New("no entry has a prompt; each entry needs a non-empty prompt")
				}
				thread, err := backend.GetThread(ctx, threadID)
				if err != nil {
					return "", fmt.Errorf("thread not found: %w", err)
				}
				text := dispatcher.submit(threadID, thread.ProjectID, entries)
				if invalid := len(raw) - len(entries); invalid > 0 {
					text += fmt.Sprintf(" %d entry(ies) ignored: no prompt.", invalid)
				}
				return text, nil
			},
		})
	}

	tracker := newSwarmLeadPolicyTracker()
	staticPolicy := func(threadID string) (plansOnly, delegateTool bool, leadPolicy, tiers string) {
		settings := mgr.Settings()
		plansOnly = effectiveMode(threadID) == swarm.ModeLeadPlansOnly
		delegateTool = settings.DelegateTool
		leadPolicy = swarm.LeadPolicy(plansOnly, settings.ReviewMode, delegateTool)
		if thread, err := backend.GetThread(ctx, threadID); err == nil && thread.InteractionMode == "plan" {
			leadPolicy += "\n\n[Swarm · plan mode] This turn is in plan mode: only read-only tier bees may be dispatched; entries for other tiers are refused. Send editing bees after the plan is approved."
		}
		tiers = swarm.TierPromptSection(settings.Tiers, settings.DefaultTier, mgr.TierPreview(threadID))
		return
	}
	backend.SetSystemPolicyFunc(func(threadID string) string {
		if !mgr.Settings().Enabled || !mgr.State(threadID).Enabled || isBeeThread(threadID) {
			return ""
		}
		_, _, leadPolicy, tiers := staticPolicy(threadID)
		return policy.Render(hostPolicyCaps(), policy.Sections{SwarmLead: leadPolicy, SwarmTiers: tiers})
	})

	backend.SetPromptDecorator(prefaceDecorator(preface, func(threadID, prompt string) string {
		if !swarm.IsNotice(prompt) {
			if !isBeeThread(threadID) {
				mgr.BeginLeadRequest(threadID)
				// 新的用户请求开始：已失败或已停止的去重记录作废，仍在跑或排队的记录保留，
				// 免得主代理换个措辞又把同一批工作派一遍。
				dispatcher.beginRequest(threadID)
			}
		}

		if mgr.Settings().Enabled && mgr.State(threadID).Enabled {
			if !isBeeThread(threadID) {
				// 宿主规则统一由 harness/policy 渲染：版本标记 + 固定顺序的启用段。
				// 「主导只规划」时在策略末尾追加只规划规则（swarm.LeadPolicy 复用已有文案）；
				// 轮次上限取 maxRounds()，与派发器实际执行的拦截口径一致。
				// 若当前为通知 prompt 且静态文本未变，只注入带提醒语的精简版规则，避免重复发送 2-4K 字符。
				plansOnly, delegateTool, leadPolicy, tiers := staticPolicy(threadID)
				limits := swarm.LimitsPromptSection(mgr.RequestLimits(threadID, maxRounds()))
				var sections policy.Sections
				if backend.CarriesSystemPolicy(threadID) {
					sections.SwarmLimits = limits
					tracker.Forget(threadID)
				} else {
					sections = tracker.RenderSections(threadID, swarm.IsNotice(prompt), plansOnly, delegateTool, leadPolicy, tiers, limits)
				}
				// 在跑 bee 清单同样每轮都带、同样不参与静态段去重：bee 的进度一直在变，
				// 去重会让主代理看不到谁还在跑，从而把同一件事又派一遍。
				state := mgr.State(threadID)
				sections.SwarmRoster = swarm.RosterPromptSection(state.Bees, state.Queued, time.Now())
				block := policy.Render(hostPolicyCaps(), sections)
				if block == "" {
					return prompt
				}
				return block + "\n\n---\n\n" + prompt
			}
		}
		// 未开启编排的主线程（也不是 bee）：默认禁用原生子代理工具。
		if !swarm.IsNotice(prompt) {
			if !isBeeThread(threadID) {
				return swarm.NativeSubagentRule + "\n\n---\n\n" + prompt
			}
		}
		return prompt
	}))

	backend.SetAssistantStreamHook(func(threadID, projectID string, reset bool, delta string) {
		if reset {
			dispatcher.reset(threadID)
			return
		}
		if delta == "" || !canAutoDelegate(threadID) {
			return
		}
		dispatcher.feed(threadID, projectID, delta)
	})

	backend.SetTurnFinishedHook(func(threadID, projectID, assistantText string, completed bool) {
		if !completed {
			dispatcher.clear(threadID)
			return
		}
		applyBeeAnswers(ctx, backend, mgr, threadID, assistantText)
		if !canAutoDelegate(threadID) {
			return
		}

		// settleTurn 用整段回复补派流式阶段漏掉的条目（不推流式增量的 provider 也在这一步补上），
		// 轮次上限与「块没生效」的回送都在派发器内部完成，这里不再重复调用 DispatchFromReply。
		dispatcher.settleTurn(threadID, projectID, assistantText)
	})

	backend.SetThreadDeletedHook(func(ctx context.Context, threadID string) {
		mgr.BeeThreadDeleted(ctx, threadID)
		tracker.Forget(threadID)
		// 会话已删除，指向它的「等你审批」提醒立即消解，避免收件箱残留死链接。
		resolveThreadApprovalsOnDelete(threadID)
	})

	// 主会话摘要的 SwarmBusy 由 Manager 判定：名下有未结束的子代理或待投递的回报时算忙，
	// 提醒检测据此只在整批落定后发一次完成通知。
	backend.SetSwarmBusy(func(threadID string) bool {
		if !mgr.Settings().Enabled {
			return false
		}
		return mgr.Busy(threadID)
	})

	backend.SetSwarmQueueHooks(
		func(threadID string) bool {
			if !mgr.Settings().Enabled {
				return false
			}
			st := mgr.State(threadID)
			for _, h := range st.Bees {
				if h.Status == swarm.StatusRunning || h.PendingMerge || h.ReviewState == swarm.ReviewRunning {
					return true
				}
			}
			return false
		},
		func(ctx context.Context, threadID, text string) (string, error) {
			// 只有「每条都派」模式把用户消息直接交给 bee；协作与只规划都由主导先规划。
			if effectiveMode(threadID) != swarm.ModeAlwaysBees || !mgr.Settings().Enabled || !mgr.State(threadID).Enabled ||
				strings.TrimSpace(text) == "" ||
				strings.HasPrefix(strings.TrimSpace(text), "/") || swarm.IsNotice(text) {
				return "", nil
			}
			if _, isBee := mgr.BeeOwner(threadID); isBee {
				return "", nil
			}
			thread, err := backend.GetThread(ctx, threadID)
			if err != nil {
				return "", err
			}
			if thread.InteractionMode == "plan" {
				return "", nil
			}
			dir, _ := backend.ThreadWorkDir(threadID)
			body, err := json.Marshal([]map[string]string{{"prompt": text}})
			if err != nil {
				return "", err
			}
			bees, _, err := mgr.DispatchFromReply(ctx, swarm.ReplyDispatchRequest{
				LeadThreadID: threadID,
				ProjectID:    thread.ProjectID,
				LeadWorkDir:  dir,
				ReplyText:    "```swarm\n" + string(body) + "\n```",
			})
			if err != nil {
				// 同样要让主代理看到：这条用户消息没能转给子代理，得由主代理自己接手。
				_ = backend.SendSwarmReport(ctx, threadID, swarm.NoticePrefix+"用户消息没能转给子代理："+err.Error()+"，请直接处理。")
				return "", err
			}
			if len(bees) == 0 {
				// Manager 在部分条目启动失败时只记录失败 bee、不再返回 err，这里同样交给主代理接手。
				err := fmt.Errorf("没有子代理启动成功")
				_ = backend.SendSwarmReport(ctx, threadID, swarm.NoticePrefix+"用户消息没能转给子代理："+err.Error()+"，请直接处理。")
				return "", err
			}
			return bees[0].ThreadID, nil
		},
	)
}

// applyBeeAnswers 处理主线程回复里的答案块：把答案提交给子线程的提问，或跳过、交还用户回答。
func applyBeeAnswers(ctx context.Context, backend *remote.MemoryBackend, mgr *swarm.Manager, leadThreadID, text string) {
	answers := swarm.ParseBeeAnswers(text)
	if len(answers) == 0 {
		return
	}
	for _, a := range answers {
		if owner, ok := mgr.BeeOwner(a.BeeThreadID); !ok || owner != leadThreadID {
			_ = backend.AppendNotice(leadThreadID, swarm.NoticePrefix+"答案块指向的子线程不属于本会话，已忽略")
			continue
		}
		name := beeLabel(mgr, leadThreadID, a.BeeThreadID)
		if a.Defer {
			_ = backend.AppendNotice(leadThreadID, swarm.NoticePrefix+"主线程把 "+name+" 的提问交还给你回答")
			continue
		}
		if err := respondBeeAnswer(ctx, backend, a); err != nil {
			_ = backend.AppendNotice(leadThreadID, swarm.NoticePrefix+"答案未送达："+err.Error()+"（子线程这一轮可能已结束）")
			continue
		}
		_ = backend.AppendNotice(leadThreadID, swarm.NoticePrefix+"已代为回答 "+name+" 的提问")
	}
}

// respondBeeAnswer 把一条答案块提交给子线程对应的待处理提问。
func respondBeeAnswer(ctx context.Context, backend *remote.MemoryBackend, a swarm.BeeAnswer) error {
	res, err := backend.ListRequests(ctx, a.BeeThreadID)
	if err != nil {
		return err
	}
	for _, r := range res.Requests {
		if r.RequestID != a.RequestID {
			continue
		}
		params := wire.RequestsRespondParams{
			ThreadID:  r.ThreadID,
			TurnID:    r.TurnID,
			RunID:     r.RunID,
			RequestID: r.RequestID,
		}
		if len(a.Answers) > 0 {
			raw, err := json.Marshal(a.Answers)
			if err != nil {
				return err
			}
			params.Answers = raw
		}
		if a.Skip {
			skip := true
			params.Skip = &skip
		}
		// 子线程提问的内部自动转发：来源记为 auto，不随远程/桌面路径统计。
		ctx = remote.WithAnswerSource(ctx, remote.AnswerSourceAuto)
		_, err = backend.RespondRequest(ctx, params)
		return err
	}
	return errors.New("子线程的提问已结束")
}

// beeLabel 返回子线程的显示名，取不到时退回线程 ID。
func beeLabel(mgr *swarm.Manager, leadThreadID, beeThreadID string) string {
	for _, h := range mgr.State(leadThreadID).Bees {
		if h.ThreadID == beeThreadID && strings.TrimSpace(h.Name) != "" {
			return h.Name
		}
	}
	return beeThreadID
}

// prefaceDecorator 把协调员 preface 前置到 decorate 的结果之上：
// preface 为 nil、取到空串，或原提示词本身来自编排注入时保持结果原样。
func prefaceDecorator(preface func(threadID string) string, decorate func(threadID, prompt string) string) func(threadID, prompt string) string {
	return func(threadID, prompt string) string {
		result := prompt
		if decorate != nil {
			result = decorate(threadID, prompt)
		}
		if preface == nil || swarm.IsNotice(prompt) {
			return result
		}
		text := preface(threadID)
		if text == "" {
			return result
		}
		return text + "\n\n---\n\n" + result
	}
}

// beeRecallItemRunes 是单条记忆摘要的 rune 上限；beeRecallMaxRunes 是整段记忆的长度上限。
const (
	beeRecallItemRunes = 300
	beeRecallMaxRunes  = 3000
)

// beeRecallDefaultsSpace 是解析不出项目空间时使用的空间名。
const beeRecallDefaultSpace = "default"

var (
	beeRecallClientOnce sync.Once
	beeRecallClient     *nmem.Client
)

// newBeeMemoryRecall 返回派发 bee 时检索 Nowledge Mem 记忆的钩子：按当前设置的开关决定是否检索，
// 客户端懒加载，任何失败都返回空串（不附加记忆段落）。
func newBeeMemoryRecall(mgr *swarm.Manager) func(ctx context.Context, workDir, query string) string {
	return func(ctx context.Context, workDir, query string) string {
		if strings.TrimSpace(query) == "" {
			return ""
		}
		c, err := config.Load(mgr.DataDir())
		if err != nil {
			return ""
		}
		config.ApplyEnv(&c, os.LookupEnv)
		if !c.Memory.BeeRecallEnabled() {
			return ""
		}
		if getMemoryBackend() == config.MemoryBackendLocal {
			memories, err := localmem.New("").Search(ctx, workDir, nmem.SearchQuery{Query: query, Limit: 5})
			if err != nil || len(memories) == 0 {
				return ""
			}
			return formatBeeRecall(memories)
		}
		client := beeRecallNowledgeClient()
		if client == nil {
			return ""
		}
		space := memspace.Resolve(ctx, client, workDir, c.Memory.SpaceFor(workDir))
		if strings.TrimSpace(space) == "" {
			space = beeRecallDefaultSpace
		}
		memories, err := client.SearchMemories(ctx, space, query, 5)
		if err != nil || len(memories) == 0 {
			return ""
		}
		return formatBeeRecall(memories)
	}
}

// beeRecallNowledgeClient 懒加载 Nowledge Mem 客户端；创建失败时返回 nil 并复用同一结果。
func beeRecallNowledgeClient() *nmem.Client {
	beeRecallClientOnce.Do(func() {
		client, err := nmem.Default()
		if err != nil {
			// 未配置 key 是常见状态，静默跳过；其它错误（例如配置损坏）才打日志。
			if !errors.Is(err, nmem.ErrNotConfigured) {
				log.Printf("初始化 Nowledge Mem 客户端失败，派发 bee 时跳过记忆注入: %v", err)
			}
			return
		}
		beeRecallClient = client
	})
	return beeRecallClient
}

// formatBeeRecall 把检索结果渲染成 Markdown 列表：单条摘要截断，整段超长时按 rune 截断。
func formatBeeRecall(memories []nmem.Memory) string {
	var b strings.Builder
	for _, mem := range memories {
		title := strings.TrimSpace(mem.Title)
		if title == "" {
			title = "未命名记忆"
		}
		kind := strings.TrimSpace(mem.UnitType)
		summary := beeRecallSummary(mem.Content)
		if summary == "" {
			continue
		}
		b.WriteString("- **")
		b.WriteString(title)
		b.WriteString("**")
		if kind != "" {
			b.WriteString("（")
			b.WriteString(kind)
			b.WriteString("）")
		}
		b.WriteString("：")
		b.WriteString(summary)
		b.WriteString("\n")
	}
	out := []rune(strings.TrimSpace(b.String()))
	if len(out) > beeRecallMaxRunes {
		out = out[:beeRecallMaxRunes]
	}
	return string(out)
}

// beeRecallSummary 把记忆正文压成单行摘要并截断到 beeRecallItemRunes。
func beeRecallSummary(content string) string {
	runes := []rune(strings.Join(strings.Fields(content), " "))
	if len(runes) > beeRecallItemRunes {
		return string(runes[:beeRecallItemRunes]) + "…"
	}
	return string(runes)
}
