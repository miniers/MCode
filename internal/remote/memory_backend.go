package remote

import (
	"context"
	"encoding/json"
	"fmt"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mcode/internal/config"
	"mcode/internal/git"
	"mcode/internal/harness"
	"mcode/internal/nmem"
	"mcode/internal/providers/agy/accounts"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"mcode/internal/swarm"
	"mcode/internal/webassets"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var _ Backend = (*MemoryBackend)(nil)

// 模拟回复每段的输出间隔，测试中调小
var simulatedReplyStepDelay = 200 * time.Millisecond

const defaultThreadTitle = "新会话"

// threadMessageKindHostEvent 是宿主投递事件时那条用户消息的 Kind：客户端据此把它显示为
// 事件通知，而不是用户手动输入。与「info」「notice」等已有 Kind 并列，随消息一起落库。
const threadMessageKindHostEvent = "host_event"

// unsolicitedTurnText 是接住常驻进程自发输出那一轮的开轮文本：它只用于开轮记录与时间线，
// 不会写进 Provider（该轮不发用户帧），带编排通知前缀以便客户端与日志里看出这是事件而非用户输入。
const unsolicitedTurnText = swarm.NoticePrefix + "后台任务已结束，模型继续处理。"

// contextCompactedKind 是「上下文已压缩」边界消息的 Kind（取值与 internal/ui 侧一致）：
// provider 压缩过上下文后落一条独立系统消息，时间线渲染成一行独立边界，不进回合折叠。
const contextCompactedKind = "context_compacted"

// contextCompactedText 把压缩前后的 token 数拼成边界行正文：
// 两个数字都有时写「已压缩上下文 · 120k → 12k」，只有压缩前时写「已压缩上下文 · 压缩前 120k」，
// 都没有（provider 不上报）时只写「已压缩上下文」。
func contextCompactedText(tokensBefore, tokensAfter int64) string {
	switch {
	case tokensBefore > 0 && tokensAfter > 0:
		return "已压缩上下文 · " + compactTokensLabel(tokensBefore) + " → " + compactTokensLabel(tokensAfter)
	case tokensBefore > 0:
		return "已压缩上下文 · 压缩前 " + compactTokensLabel(tokensBefore)
	default:
		return "已压缩上下文"
	}
}

// compactTokensLabel 把 token 数按 k 取整（四舍五入）后加单位，例如 120000 → "120k"。
func compactTokensLabel(tokens int64) string {
	if tokens <= 0 {
		return "0"
	}
	return fmt.Sprintf("%dk", (tokens+500)/1000)
}

// generateThreadTitle 根据首条消息文本生成会话标题：去掉首尾空白，取第一个非空行，
// 把连续空白压成一个空格，按 rune 截取前 40 个字符，超出时末尾加 "…"；结果为空则保持默认值。
func generateThreadTitle(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return defaultThreadTitle
	}

	var firstLine string
	for _, line := range strings.Split(trimmed, "\n") {
		l := strings.TrimSpace(line)
		if l != "" {
			firstLine = l
			break
		}
	}
	if firstLine == "" {
		return defaultThreadTitle
	}

	words := strings.Fields(firstLine)
	combined := strings.Join(words, " ")
	if combined == "" {
		return defaultThreadTitle
	}

	runes := []rune(combined)
	if len(runes) > 40 {
		return string(runes[:40]) + "…"
	}
	return string(runes)
}

// ThreadTitleFromText 返回按会话首条消息本地截取的标题（与首轮自动标题同一算法）。
// 供 app 层判断当前标题是否仍是自动派生、可被 AI 生成的标题替换。
func ThreadTitleFromText(text string) string { return generateThreadTitle(text) }

func formatOrderKey(sortOrder int64) string {
	return fmt.Sprintf("%020d", sortOrder+(1<<40))
}

type runtimePhase int

const (
	phaseIdle runtimePhase = iota
	phaseStarting
	phaseRunning
)

type memoryThread struct {
	summary       wire.ThreadSummary
	sortOrder     int64
	queueRevision int
	followUps     []wire.FollowUp
	pendingSend   *wire.FollowUp
	continuation  string
	// continuationSent 转发上下文是否已送达当前 provider 会话；会话被替换（sessionEpoch 自增）时重置，以便新会话重新收到。
	continuationSent bool
	messages         []wire.ThreadMessage
	turnTiming       map[string]store.Turn
	// messagesLoaded 为真表示消息已加载到内存；从 store 恢复的线程初始为 false，首次访问时按需加载。
	messagesLoaded bool
	sessionID      string
	persisted      map[string]bool
	persistRetry   map[string]bool // 只保存待重写的消息 ID，由 b.mu 保护。
	pending        map[string]*pendingRequest
	alwaysAllow    map[string]bool
	runTurnID      string
	runCancel      context.CancelFunc
	// stewardBusy 在前台处理主会话消息期间为真，不是执行轮次。
	stewardBusy bool
	// runStartedAt 是本轮开始运行的时刻，「终止运行中的命令」在缺少工具消息时间时用它兜底。
	runStartedAt time.Time
	// deliveryTurnID/deliveryMessageID/deliveryFollowUpID 标记当前这轮由排队项出队开始：
	// 出队写正文后由 bindDeliveryLocked 赋值，accepted/failed 按 turnID 匹配；普通发送保持为空。
	deliveryTurnID     string
	deliveryMessageID  string
	deliveryFollowUpID string
	workDir            string
	// touchedPaths 记录当前执行轮内工具事件触碰过的文件路径；新轮次开始时清空。
	touchedPaths []string
	// lastFileChangeAt 是最近一次工具写文件或轮次开始的时刻，供进度看门狗判断是否有落地。
	lastFileChangeAt time.Time
	// turnInputKind、turnInputText 是当前或最近一轮开轮输入的 Kind 与文本；新一轮开始时覆盖。
	// 中途 steer 插入的消息不改写它们，便于判断本轮是否由用户亲自发消息触发。
	turnInputKind string
	turnInputText string
	// attachTurnID 是要接住常驻进程自发输出（后台任务结束后的续跑）的那一轮：
	// 这一轮不发用户帧，只把进程已经产出的这一段读完。为空表示没有待接的轮次。
	attachTurnID string
	// unsolicitedPending 自发续跑通知到达时线程正忙，空闲后需要补开 attach 轮。
	unsolicitedPending bool

	// backgroundTools 记录 provider 托管的后台任务：tool id → 发起它的工具消息 ID。
	// 进程内索引，宿主重启即丢；重启后遗留的 background 消息由启动对账改成失败。
	backgroundTools map[string]string
	// backgroundTaskTools 记录 provider 托管的后台任务：task id → tool id。
	backgroundTaskTools map[string]string
	// backgroundPending 记录 task_started 早于 tool_result 到达的工具：tool_end 到达时据此把状态
	// 改成 background，而不是先落成 completed 再回写。
	backgroundPending map[string]bool
	// bgCount 缓存摘要里的 BackgroundTasks 计数，避免每次组装摘要都全量遍历消息；
	// 工具状态在原地改写不改消息条数，改状态处需显式失效（见 invalidate）。
	bgCount   bgCountCache
	toolCount struct {
		count, msgCount int
		loaded, evicted bool
		valid           bool
	}

	phase         runtimePhase
	historyOp     chan struct{}
	sessionEpoch  int
	watchdogStop  func() bool
	idleWaiters   []chan struct{}
	takingBack    bool
	nextTurnIndex int
	needsHandoff  bool
	composeEdit   *queueComposeEdit
	// pendingFollowUpEvents 暂存本次队列变更要写入的审计事件；commitQueueChange 取走后清空。
	// 只在持有 b.mu 期间读写。
	pendingFollowUpEvents []store.FollowUpEvent
	queueRetryPending     bool
	takenBack             map[string]bool

	parentThreadID string
	isHelper       bool
	dormantAt      *time.Time

	// messagesEvicted 为真表示消息已从内存淘汰（仅发生在已归档的空闲会话上），
	// 下次访问时由 ensureMessagesLocked 从 store 回载；lastAccess 供冷会话淘汰排序使用。
	messagesEvicted bool
	lastAccess      time.Time

	// priorityFollowUps 记录以 priority 入队的跟进项 ID（swarm 汇报与提问转交）。
	// 这些条目不等子代理收尾，上一轮结束就立刻投递；普通排队消息仍按 allowQueue 决定。
	priorityFollowUps map[string]bool
}

// isPriorityFollowUpLocked 判断跟进项是否为 priority 条目（swarm 汇报与提问转交），调用方需持有 b.mu。
func (td *memoryThread) isPriorityFollowUpLocked(fu wire.FollowUp) bool {
	return td.priorityFollowUps[fu.ID]
}

func (td *memoryThread) setIdleLocked() {
	td.summary.IsRunning = false
	td.phase = phaseIdle
	for _, ch := range td.idleWaiters {
		close(ch)
	}
	td.idleWaiters = nil
}

// MemoryBackend 提供纯内存实现的 Backend 接口，供测试与本地开发模拟。
type MemoryBackend struct {
	mu                     sync.Mutex
	stewardFeedMu          sync.Mutex
	stewardFeedID          string
	stewardFeedLoaded      bool
	stewardDesk            StewardDesk
	queuePersistMu         sync.Mutex
	branchActionMu         sync.Mutex
	turnPersistMu          sync.Mutex
	wg                     sync.WaitGroup
	hub                    *StreamHub
	projects               map[string]wire.ProjectSummary
	threads                map[string]*memoryThread
	usage                  map[string]map[int]harness.Usage
	actionThreads          map[string]string
	actions                *actionLedger
	store                  *store.Store
	replaceFollowUpsHook   func(context.Context, string, int64, []store.FollowUp, []store.FollowUpEvent) error
	createBranchThreadHook func(context.Context, store.Thread) error

	// scriptRuns 保存最近的脚本运行结果，只在内存里供 scripts.runStatus 查询，不写回会话时间线。
	scriptRuns scriptRunStore

	actionJournalID     string
	queueChangedHook    func(wire.ThreadsQueueSnapshot)
	queueListeners      map[int]func(wire.ThreadsQueueSnapshot)
	nextQueueListenerID int
	threadsChangedHook  func(wire.ThreadsChanged)

	harnesses       map[string]harness.Harness
	defaultProvider string
	harnessWorkDir  string
	harnessMode     harness.PermissionMode
	git             *git.Runner
	// projectCloner 是 projects.clone 的 git 执行器，nil 时用默认的 git 子进程实现。
	projectCloner projectCloneRunner
	// appVersion 是对外声明的桌面端版本号（与「关于」页同源），由 app 层注入。
	appVersion string
	// watchers 按仓库根目录缓存检查点文件监听器，由 b.mu 保护；见 watcherFor。
	watchers              map[string]git.Watcher
	swarmAvailable        bool
	swarmEnabledFor       func(threadID string) bool
	swarmLeadPlansOnlyFor func(threadID string) bool

	// inspectorPreview 是文件预览专用的 loopback 内容服务，懒启动；由 inspectorPreviewMu 保护。
	inspectorPreviewMu     sync.Mutex
	inspectorPreview       *inspectorPreviewServer
	inspectorPreviewClosed bool
	// swarmEnablerFunc 在新建会话需要立即开启编排时启用它，由宿主注入 threads.swarm.set 背后的同一实现。
	swarmEnablerFunc func(threadID string, enabled bool)
	// swarmInheritFunc 在转发会话时把源会话的编排开关与只规划覆盖值带到新会话。
	swarmInheritFunc func(fromThreadID, toThreadID string)
	// swarmBusyFor 判断主会话名下是否仍有子线程活动或待投递的回报，供 summary.SwarmBusy 使用。
	swarmBusyFor func(threadID string) bool
	// revertBusyCheck 判断该工作区是否有别的占用（如正在合并改动的子代理），
	// 非空返回值就是拒绝撤销的原因；nil 表示不做额外检查。
	revertBusyCheck func(threadID, root string) string

	turnFinishedHook    func(threadID, projectID, assistantText string, completed bool)
	assistantStreamHook func(threadID, projectID string, reset bool, delta string)
	promptDecorator     func(threadID, prompt string) string
	systemPolicyFunc    func(threadID string) string
	// sessionRotator 在续接 provider 会话的轮次开始前判定是否换新会话；nil 表示从不换新。
	sessionRotator SessionRotator
	// nowledge 是宿主注入的 Nowledge Mem 客户端；nil 表示不启用任何 Nowledge 功能
	// （首轮 Working Memory 注入与会话同步），此时不发任何网络请求。
	nowledge            *nmem.Client
	localMemoryGuidance func(maxRunes int) string
	// nowledgeMu 保护 Working Memory 段落的短期缓存；与 b.mu 分开，避免发请求时长时间持锁。
	nowledgeMu      sync.Mutex
	nowledgeWMCache *nowledgeWorkingMemoryEntry
	// nowledgeSync 记录按线程的会话同步状态（进行中/待补跑），由 b.mu 保护。
	nowledgeSync map[string]*nowledgeSyncState
	// nowledgePendingMu 串行化持久待同步集合（app_settings 的 nowledge_sync_pending）的读改写，
	// 并把 generation 检查与增代记账放在同一临界区内。加锁顺序固定为 nowledgePendingMu → b.mu；
	// 数据库读写期间不持 b.mu，HTTP 请求期间两把锁都不持。详见 nmem_sync_pending.go。
	nowledgePendingMu sync.Mutex
	// personaTag 把线程人设换算成 Turn.Prompt 最前面的标签前缀（不需要时返回空串）；nil 表示不注入。
	personaTag        func(provider, model, persona string) string
	guidanceDelivered map[string]bool             // threadID 到是否已成功交付首轮项目 Context 指导词，由 b.mu 保护
	guidanceBuilder   func(workDir string) string // 覆盖首轮指导词构建函数，便于测试注入
	// firstTurnExtra 是随首轮指导词一起注入一次的额外段落（普通会话的「可以交给总管」提示）；nil 表示不注入。
	firstTurnExtra      func(threadID string) string
	swarmHasWorkingBees func(threadID string) bool
	swarmSendToBee      func(ctx context.Context, threadID, text string) (string, error)

	ctx    context.Context
	cancel context.CancelFunc

	dataDir     string
	attachments *attachmentStore
	thumbCache  *thumbCache
	agyAccounts *accounts.Manager

	// 使用原子指针是为了让 generalSettings 可以在持有 b.mu 时安全调用。
	generalSettingsFunc   atomic.Pointer[func() config.Config]
	workbenchSettingsFunc atomic.Pointer[func() config.WorkbenchConfig]
	nowledgeSettingsFunc  atomic.Pointer[func() config.MemoryConfig]
	swarmConfigFunc       atomic.Pointer[func() config.OrchestrationConfig]
	// providerEnabledFunc 返回各 provider 在桌面设置里的启用状态（键为 provider kind）。
	// 已关闭的 provider 不下发给远程端，与桌面模型选择器的过滤一致。
	providerEnabledFunc atomic.Pointer[func() map[string]bool]
	// draftDefaultsFunc 返回新建会话草稿的默认值（桌面模型记忆与编排选择）。
	draftDefaultsFunc atomic.Pointer[func() wire.ComposerDraftDefaults]
	// rememberSelectionFunc 把用户主动确认的模型与编排选择写回宿主记忆（providers.rememberSelection）。
	rememberSelectionFunc atomic.Pointer[func(params wire.ProvidersRememberSelectionParams)]
	autoContinueWaits     map[string]*autoContinueWait

	interruptWatchdogTimeout time.Duration
	approvalDeliveryTimeout  time.Duration
	afterFunc                func(d time.Duration, f func()) func() bool
	clock                    func() time.Time

	threadDeletedHook       func(ctx context.Context, threadID string)
	provisionalCleanupDelay time.Duration
	provisionalTimers       map[string]*time.Timer

	// 批量整理撤销状态（见 thread_ops_backend.go）
	bulk bulkUndoState

	// 子会话工具读取文件路径记录（供 Swarm 报告可信度比对）
	readPathsMu sync.Mutex
	readPaths   map[string]map[string]struct{}
}

// SetDataDir 设置数据存储根目录。
func (b *MemoryBackend) SetDataDir(dir string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dataDir = dir
	if dir != "" {
		ledgerPath := filepath.Join(dir, "remote-action-ledger.json")
		var nowFn func() time.Time
		if b.actions != nil {
			nowFn = b.actions.now
		}
		persistent := newPersistentActionLedger(ledgerPath, nowFn)
		if b.actions != nil {
			b.actions.migrateTo(persistent)
		}
		b.actions = persistent
	}
}

// AgentConfDataDir 返回数据存储根目录，供技能扫描读取与设置页同一份停用配置（可选接口）。
func (b *MemoryBackend) AgentConfDataDir() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dataDir
}

// Hub 返回关联的 StreamHub 实例。
func (b *MemoryBackend) Hub() *StreamHub {
	return b.hub
}

// SetClock 设置附件存储注入的时钟函数，便于测试。
func (b *MemoryBackend) SetClock(now func() time.Time) {
	b.mu.Lock()
	b.clock = now
	if b.actions != nil {
		b.actions.setNow(now)
	}
	b.mu.Unlock()
	if b.attachments != nil {
		b.attachments.setNow(now)
	}
}

// SetAfterFunc 设置看门狗定时器函数，便于测试注入。
func (b *MemoryBackend) SetAfterFunc(fn func(time.Duration, func()) func() bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.afterFunc = fn
}

// SetApprovalDeliveryTimeout 设置审批送达确认等待超时时间。
func (b *MemoryBackend) SetApprovalDeliveryTimeout(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.approvalDeliveryTimeout = d
}

func (b *MemoryBackend) scheduleAfter(d time.Duration, f func()) func() bool {
	b.mu.Lock()
	fn := b.afterFunc
	b.mu.Unlock()
	return scheduleAfter(fn, d, f)
}

func scheduleAfter(fn func(time.Duration, func()) func() bool, d time.Duration, f func()) func() bool {
	if fn != nil {
		return fn(d, f)
	}
	t := time.AfterFunc(d, f)
	return t.Stop
}

func (b *MemoryBackend) now() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nowLocked()
}

func (b *MemoryBackend) nowLocked() time.Time {
	if b.clock != nil {
		return b.clock()
	}
	return time.Now().UTC()
}

// bgCountCache 缓存摘要里 BackgroundTasks 的统计结果：provider 托管的后台任务数与仍 running 的
// 工具数分开记，后者只在会话空闲时计入，所以会话忙闲翻转不必重算。
// msgCount/loaded/evicted 是缓存匹配键：消息集合（增删、回载、淘汰）一变就重算。
// 工具状态在原消息上改写不会改变这些键，因此必须在改写处显式调用 invalidate。
type bgCountCache struct {
	tasks, running  int
	msgCount        int
	loaded, evicted bool
	valid           bool
}

// invalidate 标记计数缓存失效。
func (c *bgCountCache) invalidate() { c.valid = false }

// backgroundTaskCountLocked 返回摘要里的 BackgroundTasks 计数，口径与桌面前端的后台任务胶囊一致：
// ToolStatus 为 background 的工具（provider 托管的后台任务）不论会话是否运行都计入；
// ToolStatus 仍为 running 的工具只在会话没在运行时计入，因为回合内运行中的命令由运行中工具组显示。
// 子智能体不计入，Swarm 名下仍有子线程时由 SwarmBusy 单独表达。
// 调用方需持有 b.mu。
func (b *MemoryBackend) backgroundTaskCountLocked(td *memoryThread) int {
	if td == nil {
		return 0
	}
	c := &td.bgCount
	if !c.valid || c.msgCount != len(td.messages) || c.loaded != td.messagesLoaded || c.evicted != td.messagesEvicted {
		c.tasks, c.running = 0, 0
		for i := range td.messages {
			if td.messages[i].ToolStatus == nil {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(*td.messages[i].ToolStatus)) {
			case "background":
				c.tasks++
			case "running":
				c.running++
			}
		}
		c.valid = true
		c.msgCount = len(td.messages)
		c.loaded = td.messagesLoaded
		c.evicted = td.messagesEvicted
	}
	if td.runTurnID != "" || td.runCancel != nil {
		return c.tasks
	}
	return c.tasks + c.running
}

// swarmBusyLocked 计算会话的 Swarm 忙闲标记：helper 子会话恒为 false。
// 调用方需持有 b.mu；钩子不得再获取 b.mu。
func (b *MemoryBackend) swarmBusyLocked(td *memoryThread) bool {
	if td == nil || td.isHelper || td.parentThreadID != "" || b.swarmBusyFor == nil {
		return false
	}
	return b.swarmBusyFor(td.summary.ID)
}

// stewardLocked 计算会话的总管标记：helper 子会话或有父会话时恒为 false。
// 调用方需持有 b.mu。
func (b *MemoryBackend) stewardLocked(td *memoryThread) bool {
	if td == nil || td.isHelper || td.parentThreadID != "" || b.stewardFeedID == "" {
		return false
	}
	return b.stewardFeedID == td.summary.ID
}

// summaryForPublishLocked 返回用于推送与回调的会话摘要副本：
// 补齐权威的父子关系标记，并按当前 Swarm 状态刷新 SwarmBusy。
// 只读路径（GetThread 等）也会调用它，所以不能把忙闲标记写回存储中的摘要：
// 否则读请求会把忙态提前写入，RepublishThreadSummary 的变更比较随之失真，忙转闲时观察者收不到事件。
// 调用方需持有 b.mu。
func (b *MemoryBackend) summaryForPublishLocked(td *memoryThread) wire.ThreadSummary {
	s := td.summary
	// 有执行轮次或前台正在处理消息时报告运行中；否则纠正异常收尾残留的标记。
	if td.runTurnID != "" || td.runCancel != nil || td.stewardBusy {
		s.IsRunning = true
	} else if s.IsRunning {
		s.IsRunning = false
		td.summary.IsRunning = false
	}
	if td.isHelper || s.IsHelper {
		s.IsHelper = true
	}
	if td.parentThreadID != "" {
		s.ParentThreadID = optionalString(td.parentThreadID)
	}
	s.SwarmBusy = b.swarmBusyLocked(td)
	s.BackgroundTasks = b.backgroundTaskCountLocked(td)
	s.IsSteward = b.stewardLocked(td)
	b.usageSummaryLocked(&s)
	return s
}

// usageSummaryLocked 按该会话已记录的各轮用量刷新摘要里的上下文占用与本轮费用：
// 上下文占用取最近一轮（turnIndex 最大）的 ContextTokens，缺失时回退「输入 + 输出」，
// 口径与桌面端上下文用量圆环一致；窗口上限优先用 CLI 上报值，否则回退静态模型表；
// 费用只取最近一轮的 CostUSD，不逐轮相加：CLI 的 total_cost_usd 可能是整次会话的累计值
// （见 internal/harness/event.go 的 Usage.CostUSD 注释），相加会重复计费，桌面端用量面板
// 同样只看最新一轮。没有任何用量时三个字段全为 0，靠 omitempty 不输出。
// 调用方需持有 b.mu。
func (b *MemoryBackend) usageSummaryLocked(s *wire.ThreadSummary) {
	latestIndex := -1
	var latest harness.Usage
	for idx, u := range b.usage[s.ID] {
		if idx > latestIndex {
			latestIndex = idx
			latest = u
		}
	}
	used := latest.ContextTokens
	if used <= 0 {
		used = latest.InputTokens + latest.OutputTokens
	}
	if used < 0 {
		used = 0
	}
	window := int64(0)
	if used > 0 {
		window = latest.ContextWindow
		if window <= 0 {
			modelID := ""
			if s.Model != nil {
				modelID = *s.Model
			}
			window = int64(fallbackContextLimit(s.Provider, modelID))
		}
	}
	s.ContextUsedTokens = used
	s.ContextWindow = window
	s.LastTurnCostUSD = latest.CostUSD
}

// RepublishThreadSummary 重新计算会话摘要（含 SwarmBusy）并推送到 StreamHub。
// Swarm 状态变更不走会话事件，必须靠这里重新发布，忙闲检测才能及时看到变化。
// 这里无条件发布：是否需要通知观察者由 StreamHub 按 observedSwarmBusy 的实际值变化判定，
// 本层再做值比较会因为存储里的标记可能来自未发布的读路径而漏掉“忙转闲”这一帧。
func (b *MemoryBackend) RepublishThreadSummary(threadID string) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return
	}
	summary := b.summaryForPublishLocked(td)
	hub := b.hub
	b.mu.Unlock()
	if hub == nil {
		return
	}
	hub.Publish(threadID, nil, nil, summary)
}

// SetTurnFinishedHook 设置轮次结束钩子。
func (b *MemoryBackend) SetTurnFinishedHook(fn func(threadID, projectID, assistantText string, completed bool)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.turnFinishedHook = fn
}

// SetAssistantStreamHook 设置助手正文流式钩子：轮次开始流式输出时先以 reset=true、delta="" 调用一次，
// 之后每条正文增量以 reset=false 携带增量文本调用。
func (b *MemoryBackend) SetAssistantStreamHook(fn func(threadID, projectID string, reset bool, delta string)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.assistantStreamHook = fn
}

// beeSkillIndexDescriptionMaxRunes 是子代理（bee）精简技能目录里单条描述保留的最大字符数，
// 必须与 projctx.skillIndexDescriptionMaxRunes 对齐（该常量未导出，这里按同一数值固定）。
const beeSkillIndexDescriptionMaxRunes = 100

const localMemoryToolsNote = "## Local memory tools\n\n" +
	"Use the memory tool's propose action to remember information instead of writing your own memory file. " +
	"Use its search action to search project and global memories. " +
	"When the user explicitly asks you to remember something, set user_requested to true. " +
	"Memories are written to project or global documents; to update the project 当前重点, set target_memory_id to project:overview#当前重点."

// normalizeSkillPath 把技能路径统一成「技能目录」的规范形式：agentconf 与 projctx
// 的 Path 粒度可能不同（SKILL.md 或技能目录），先统一到 SKILL.md 所在目录，再做
// Clean 与符号链接解析（路径不存在等解析失败时保留 Clean 结果）。
func normalizeSkillPath(path string) string {
	cleaned := filepath.Clean(path)
	if strings.EqualFold(filepath.Base(cleaned), "SKILL.md") {
		cleaned = filepath.Dir(cleaned)
	}
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return filepath.Clean(resolved)
	}
	return cleaned
}

// isGuidanceTriggerText 判断用户文本是否会触发首轮注入：斜杠命令与编排派发
// 消息既不注入，也不消耗交付。
func isGuidanceTriggerText(text string) bool {
	trimmed := strings.TrimSpace(text)
	return !strings.HasPrefix(trimmed, "/") && !swarm.IsNotice(trimmed)
}

// threadWorkDirLocked 返回该会话实际运行的工作目录，调用方必须持有 b.mu。
func (b *MemoryBackend) threadWorkDirLocked(threadID string) (string, bool) {
	td, ok := b.threads[threadID]
	if !ok {
		return "", false
	}
	if td.workDir != "" {
		return td.workDir, true
	}
	if proj, ok := b.projects[td.summary.ProjectID]; ok && proj.Path != "" {
		return proj.Path, true
	}
	return "", false
}

// dirWithinRoot 判断 dir 是否等于 root 或位于 root 之内。
// 两侧都先按符号链接解析：git 返回的仓库根目录已是真实路径，而会话工作目录可能仍是符号链接写法。
func dirWithinRoot(dir, root string) bool {
	rel, err := filepath.Rel(resolveForCompare(root), resolveForCompare(dir))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveForCompare 返回用于路径比较的形式：能解析符号链接就用真实路径，否则退回清理后的原值。
func resolveForCompare(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

// revertWorkspaceBusyLocked 返回同一工作区里仍在运行的其他会话标题；调用方必须持有 b.mu。
// 撤销会还原工作区文件，与另一个会话的改动互相覆盖，因此这类会话存在时直接拒绝。
func (b *MemoryBackend) revertWorkspaceBusyLocked(threadID, root string) string {
	for id, td := range b.threads {
		if id == threadID || td == nil {
			continue
		}
		if td.runTurnID == "" && !td.summary.IsRunning {
			continue
		}
		workDir, ok := b.threadWorkDirLocked(id)
		if !ok || workDir == "" || !dirWithinRoot(workDir, root) {
			continue
		}
		title := td.summary.Title
		if title == "" {
			title = id
		}
		return fmt.Sprintf("同一工作区还有会话「%s」在运行，等它结束再撤销", title)
	}
	return ""
}

// ThreadWorkDir 返回该会话实际运行的工作目录（优先返回 StartSession 传入的 WorkDir，没有时回落到所属项目的根目录）。
func (b *MemoryBackend) ThreadWorkDir(threadID string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.threadWorkDirLocked(threadID)
}

// ThreadReadPaths 返回指定会话已记录的读取文件路径副本（按字典序排序，若无记录返回 nil）。
func (b *MemoryBackend) ThreadReadPaths(threadID string) []string {
	b.readPathsMu.Lock()
	defer b.readPathsMu.Unlock()
	if b.readPaths == nil {
		return nil
	}
	set := b.readPaths[threadID]
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (b *MemoryBackend) recordReadPath(threadID, path string) {
	if threadID == "" || path == "" {
		return
	}
	b.readPathsMu.Lock()
	defer b.readPathsMu.Unlock()
	if b.readPaths == nil {
		b.readPaths = make(map[string]map[string]struct{})
	}
	set, ok := b.readPaths[threadID]
	if !ok {
		set = make(map[string]struct{})
		b.readPaths[threadID] = set
	}
	if _, exists := set[path]; !exists {
		if len(set) >= 1000 {
			return
		}
		set[path] = struct{}{}
	}
}

// readPathKeys 是工具输入中可能携带被读取文件路径的字段，按优先级排列。
var readPathKeys = []string{
	"file_path", "filePath", "filepath", "path", "absolute_path", "AbsolutePath",
	"target_file", "TargetFile", "targetFile", "notebook_path", "file",
}

// normalizeReadPath 把路径换算为相对工作目录的斜杠清理形式；空串原样返回。
func normalizeReadPath(p, workDir string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) && workDir != "" {
		if rel, ok := relativizeToWorkDir(workDir, p); ok {
			p = rel
		}
	}
	return filepath.ToSlash(filepath.Clean(p))
}

// extractReadPath 从工具输入中提取读取的文件路径，并换算为相对工作目录的规范路径。
func extractReadPath(input json.RawMessage, workDir string) (string, bool) {
	if len(input) == 0 {
		return "", false
	}
	var data map[string]any
	if err := json.Unmarshal(input, &data); err != nil {
		return "", false
	}
	for _, k := range readPathKeys {
		v, ok := data[k]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		if p := normalizeReadPath(s, workDir); p != "" {
			return p, true
		}
	}
	return "", false
}

// readPathIsDir 判断路径在磁盘上是否为目录；stat 失败时按文件处理（返回 false）。
func readPathIsDir(p, workDir string) bool {
	candidate := p
	if !filepath.IsAbs(candidate) {
		if workDir == "" {
			return false
		}
		candidate = filepath.Join(workDir, candidate)
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// isNonReadTool 判断工具是否不属于「读取单个文件」的工具（写入、编辑、搜索、列目录等）。
func isNonReadTool(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"write", "edit", "patch", "replace", "create", "grep", "glob", "find",
		"ls", "list", "search", "ast_grep", "delete", "remove",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// extractReadPaths 汇总一次工具调用中被读取的文件路径：
//   - Bash/shell 类调用按其中的 cat/sed/head 等命令识别被读文件；
//   - 非写入/搜索类工具按输入字段（file_path 等）识别；
//   - 磁盘上是目录的路径丢弃，同一次调用的结果去重。
func extractReadPaths(toolName string, input json.RawMessage, workDir string) []string {
	if len(input) == 0 {
		return nil
	}
	var data map[string]any
	if err := json.Unmarshal(input, &data); err != nil {
		return nil
	}
	var out []string
	seen := make(map[string]struct{})
	emit := func(p string) {
		p = normalizeReadPath(p, workDir)
		if p == "" || readPathIsDir(p, workDir) {
			return
		}
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}

	for _, raw := range []any{data["command"], data["cmd"]} {
		cmd, ok := shellCommandString(raw)
		if !ok {
			continue
		}
		for _, p := range readPathsFromShellCommand(cmd) {
			emit(p)
		}
	}

	if !isNonReadTool(toolName) {
		if p, ok := extractReadPath(input, workDir); ok {
			emit(p)
		}
	}
	return out
}

// readShellTools 是会把参数中的文件内容读出来的命令。
var readShellTools = map[string]struct{}{
	"cat": {}, "head": {}, "tail": {}, "sed": {}, "nl": {}, "bat": {},
	"batcat": {}, "less": {}, "more": {}, "awk": {}, "wc": {},
}

// isShellWrapper 判断词是否为命令前的包装命令（sudo/env），需跳过后再取真正的命令名。
func isShellWrapper(word string) bool {
	base := strings.ToLower(filepath.Base(word))
	return base == "sudo" || base == "env"
}

// shellCommandString 从输入值中取出命令行文本：字符串直接使用，字符串数组按 shell 形式拼接。
func shellCommandString(raw any) (string, bool) {
	switch v := raw.(type) {
	case string:
		v = strings.TrimSpace(v)
		return v, v != ""
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				continue
			}
			parts = append(parts, s)
		}
		if len(parts) == 0 {
			return "", false
		}
		// 形如 ["bash", "-lc", "cat a.go"] 时，真正的脚本是最后一段。
		if len(parts) > 1 {
			head := strings.ToLower(filepath.Base(parts[0]))
			switch head {
			case "bash", "sh", "zsh", "dash", "ksh", "fish", "ash":
				last := strings.TrimSpace(parts[len(parts)-1])
				return last, last != ""
			}
		}
		joined := strings.TrimSpace(strings.Join(parts, " "))
		return joined, joined != ""
	}
	return "", false
}

// splitShellSegments 按管道、逻辑运算符、分号与换行把命令行切成片段，
// 再按空白切词（简单的单双引号处理，不实现完整 shell 解析）。
func splitShellSegments(cmd string) [][]string {
	segments := make([][]string, 0, 4)
	var cur []string
	var word strings.Builder
	inWord := false
	var quote rune

	flushWord := func() {
		if inWord {
			cur = append(cur, word.String())
			word.Reset()
			inWord = false
		}
	}
	flushSegment := func() {
		flushWord()
		if len(cur) > 0 {
			segments = append(segments, cur)
			cur = nil
		}
	}

	for _, r := range cmd {
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			word.WriteRune(r)
			inWord = true
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
			inWord = true
		case ' ', '\t', '\r':
			flushWord()
		case '\n', ';', '|', '&':
			flushSegment()
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	flushSegment()
	return segments
}

// looksLikeShellFilePath 判断词是否像文件路径（含 / 或带 1~8 位字母数字扩展名）。
func looksLikeShellFilePath(word string) bool {
	if strings.Contains(word, "/") {
		return true
	}
	dot := strings.LastIndex(word, ".")
	if dot <= 0 || dot == len(word)-1 {
		return false
	}
	ext := word[dot+1:]
	if len(ext) > 8 {
		return false
	}
	for _, r := range ext {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !isAlphaNum {
			return false
		}
	}
	return true
}

// readPathsFromShellCommand 从命令行中识别被 cat/head/sed 等命令读取的文件路径。
func readPathsFromShellCommand(cmd string) []string {
	var paths []string
	for _, seg := range splitShellSegments(cmd) {
		i := 0
		for i < len(seg) {
			word := seg[i]
			if isShellWrapper(word) {
				i++
				continue
			}
			// 跳过 sudo/env 的选项参数
			if i > 0 && isShellWrapper(seg[i-1]) && strings.HasPrefix(word, "-") {
				i++
				continue
			}
			break
		}
		if i >= len(seg) {
			continue
		}
		name := strings.ToLower(filepath.Base(seg[i]))
		if _, ok := readShellTools[name]; !ok {
			continue
		}
		for _, arg := range seg[i+1:] {
			if strings.HasPrefix(arg, "-") {
				continue
			}
			if !looksLikeShellFilePath(arg) {
				continue
			}
			paths = append(paths, arg)
		}
	}
	return paths
}

// CurrentTurnInput 返回指定会话当前（或最近）一轮开轮输入的 Kind 和文本：
// Kind 为空串表示普通用户输入，host_event 等表示宿主投递的事件；active 表示该会话当前是否有轮次在运行。
// 中途 steer 插入的消息不会改写返回值，只有新一轮开始才会覆盖。
func (b *MemoryBackend) CurrentTurnInput(threadID string) (kind, text string, active bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	td, ok := b.threads[threadID]
	if !ok {
		return "", "", false
	}
	return td.turnInputKind, td.turnInputText, td.runTurnID != ""
}

// CurrentTurnTouchedPaths 返回指定会话当前轮次中被工具事件触碰过的文件路径：
// 相对会话工作目录、清理为斜杠分隔形式、去重并排序；active 表示该会话当前是否有轮次在运行。
// 路径在新轮次开始时清空，轮次结束后保留到下一轮开始。
func (b *MemoryBackend) CurrentTurnTouchedPaths(threadID string) (paths []string, active bool) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return nil, false
	}
	active = td.runTurnID != ""
	raw := append([]string(nil), td.touchedPaths...)
	workDir := b.turnWorkDirLocked(td)
	b.mu.Unlock()

	if len(raw) == 0 {
		return nil, active
	}

	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if filepath.IsAbs(p) {
			if workDir == "" {
				// 没有工作目录可换算时保留清理后的绝对路径
				p = filepath.ToSlash(filepath.Clean(p))
			} else {
				rel, ok := relativizeToWorkDir(workDir, p)
				if !ok {
					// 工作目录之外的路径不属于该会话的工作内容
					continue
				}
				p = rel
			}
		}
		cleaned := filepath.ToSlash(filepath.Clean(p))
		if cleaned == "" || cleaned == "." {
			continue
		}
		if _, dup := seen[cleaned]; dup {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	sort.Strings(out)
	return out, active
}

// turnWorkDirLocked 返回轮次实际运行的工作目录，优先级与 runHarness 一致；调用方必须持有 b.mu。
func (b *MemoryBackend) turnWorkDirLocked(td *memoryThread) string {
	workDir := b.harnessWorkDir
	if td.workDir != "" {
		workDir = td.workDir
	} else if proj, ok := b.projects[td.summary.ProjectID]; ok && proj.Path != "" {
		workDir = proj.Path
	}
	return workDir
}

// relativizeToWorkDir 将绝对路径换算为相对工作目录的路径；路径落在工作目录之外时返回 false。
// macOS 上 /var 与 /private/var 互为符号链接，因此工作目录与待换算路径都同时尝试解析形式。
func relativizeToWorkDir(workDir, path string) (string, bool) {
	if workDir == "" || !filepath.IsAbs(path) {
		return "", false
	}
	bases := []string{workDir}
	if resolved, err := filepath.EvalSymlinks(workDir); err == nil && resolved != workDir {
		bases = append(bases, resolved)
	}
	candidates := []string{path}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != path {
		candidates = append(candidates, resolved)
	}
	for _, candidate := range candidates {
		for _, base := range bases {
			rel, err := filepath.Rel(base, candidate)
			if err != nil {
				continue
			}
			if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			return rel, true
		}
	}
	return "", false
}

// normalizeTouchedFiles 将工具事件采集的路径去重、排序，并换算为相对仓库根目录的斜杠路径。
// 相对路径按 workDir 解析；落在仓库之外的路径直接丢弃。
func normalizeTouchedFiles(workDir, repoRoot string, paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		abs := p
		if !filepath.IsAbs(abs) {
			if workDir == "" {
				continue
			}
			abs = filepath.Join(workDir, abs)
		}
		rel, ok := relativizeToWorkDir(repoRoot, abs)
		if !ok {
			continue
		}
		cleaned := filepath.ToSlash(filepath.Clean(rel))
		if cleaned == "." {
			continue
		}
		if _, dup := seen[cleaned]; dup {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	sort.Strings(out)
	return out
}

// touchedPathsFromToolEvent 从工具调用事件中提取被触碰的文件路径：
// 写文件类工具取 file_path / path / notebook_path 等字段（含 codex FileChange 的 changes[].path），
// 任意工具的输入中若含 apply_patch 的 "*** Update File:" 等文件头也会被采集。
func touchedPathsFromToolEvent(toolName string, input json.RawMessage) []string {
	if len(input) == 0 {
		return nil
	}
	var raw any
	if err := json.Unmarshal(input, &raw); err != nil {
		return nil
	}
	writeTool := isFileWritingTool(toolName)
	var paths []string

	var walk func(v any, key string)
	walk = func(v any, key string) {
		switch val := v.(type) {
		case string:
			if writeTool && (key == "file_path" || key == "filePath" || key == "path" || key == "notebook_path") {
				if strings.TrimSpace(val) != "" {
					paths = append(paths, val)
				}
			}
			if strings.Contains(val, "*** ") {
				paths = append(paths, parsePatchFileHeaders(val)...)
			}
		case []any:
			for _, item := range val {
				walk(item, key)
			}
		case map[string]any:
			for k, item := range val {
				walk(item, k)
			}
		}
	}
	walk(raw, "")
	return paths
}

// isFileWritingTool 判断工具名是否属于会写文件的工具（编辑、写入、打补丁等）。
func isFileWritingTool(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return false
	}
	for _, marker := range []string{"edit", "write", "create_file", "createfile", "apply_patch", "applypatch", "filechange", "notebook", "replace"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// parsePatchFileHeaders 从 apply_patch 补丁文本中提取 "*** Add/Update/Delete File: <路径>" 文件头。
func parsePatchFileHeaders(text string) []string {
	var paths []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		for _, prefix := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: "} {
			if rest, ok := strings.CutPrefix(line, prefix); ok {
				if p := strings.TrimSpace(rest); p != "" {
					paths = append(paths, p)
				}
			}
		}
	}
	return paths
}

// NewMemoryBackend 构建并初始化 MemoryBackend。
func NewMemoryBackend(hub *StreamHub) *MemoryBackend {
	ctx, cancel := context.WithCancel(context.Background())
	var gitRunner *git.Runner
	if _, err := exec.LookPath("git"); err == nil {
		gitRunner = git.NewRunner("", 4)
	}
	return &MemoryBackend{
		hub:                      hub,
		projects:                 make(map[string]wire.ProjectSummary),
		threads:                  make(map[string]*memoryThread),
		usage:                    make(map[string]map[int]harness.Usage),
		actionThreads:            make(map[string]string),
		actions:                  newActionLedger(nil),
		actionJournalID:          newUUID(),
		harnesses:                make(map[string]harness.Harness),
		git:                      gitRunner,
		ctx:                      ctx,
		cancel:                   cancel,
		attachments:              newAttachmentStore(),
		thumbCache:               newThumbCache(256),
		autoContinueWaits:        make(map[string]*autoContinueWait),
		interruptWatchdogTimeout: 8 * time.Second,
		approvalDeliveryTimeout:  5 * time.Second,
		afterFunc: func(d time.Duration, f func()) func() bool {
			t := time.AfterFunc(d, f)
			return t.Stop
		},
		provisionalCleanupDelay: 2 * time.Second,
		provisionalTimers:       make(map[string]*time.Timer),
	}
}

// RegisterHarness 注册或替换同 Kind 的 harness，默认来源为空时选择非 mcode 的 Kind。
func (b *MemoryBackend) RegisterHarness(h harness.Harness) {
	if h == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.harnesses == nil {
		b.harnesses = make(map[string]harness.Harness)
	}
	kind := string(h.Kind())
	if receiver, ok := h.(interface{ PreserveSessionsFrom(harness.Harness) }); ok {
		receiver.PreserveSessionsFrom(b.harnesses[kind])
	}
	b.bindUnsolicitedTurnHandlerLocked(h)
	b.harnesses[kind] = h
	if b.defaultProvider == "" && kind != config.ProviderMcode {
		b.defaultProvider = kind
	}
}

// RegisteredHarness 返回指定 kind 已注册的 harness，未注册时返回 nil。
func (b *MemoryBackend) RegisteredHarness(kind string) harness.Harness {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.harnesses[kind]
}

// UnregisterHarness 注销指定 kind 的 harness。
// 若注销的是 defaultProvider，则回退到字典序最小的非 mcode 来源；没有则置空。
func (b *MemoryBackend) UnregisterHarness(kind string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.harnesses, kind)
	if b.defaultProvider == kind {
		var remaining []string
		for k := range b.harnesses {
			if k != config.ProviderMcode {
				remaining = append(remaining, k)
			}
		}
		if len(remaining) == 0 {
			b.defaultProvider = ""
		} else {
			sort.Strings(remaining)
			b.defaultProvider = remaining[0]
		}
	}
}

type unavailableCustomHarness struct {
	kind harness.Kind
}

func (h unavailableCustomHarness) Kind() harness.Kind { return h.kind }

func (h unavailableCustomHarness) Run(context.Context, harness.Turn, func(harness.Event)) error {
	return fmt.Errorf("供应商 %q 未注册、已停用或删除，请重新选择供应商", h.kind)
}

// 未注册的 provider（自定义端点被删、供应商被关闭）不得回落到默认厂商，
// 否则已有会话的历史与本轮内容会发给另一家；空 provider 与内存演示会话（memory）仍走默认。
func (b *MemoryBackend) threadHarnessLocked(provider string) harness.Harness {
	if h := b.harnesses[provider]; h != nil {
		return h
	}
	if provider != "" && provider != "memory" {
		return unavailableCustomHarness{kind: harness.Kind(provider)}
	}
	return b.harnesses[b.defaultProvider]
}

// SetHarness 设置后台 Agent Harness 实例及其工作目录与权限模式。
func (b *MemoryBackend) SetHarness(h harness.Harness, workDir string, mode harness.PermissionMode) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.harnesses == nil {
		b.harnesses = make(map[string]harness.Harness)
	}
	if h != nil {
		kind := string(h.Kind())
		b.bindUnsolicitedTurnHandlerLocked(h)
		b.harnesses[kind] = h
		b.defaultProvider = kind
	}
	b.harnessWorkDir = workDir
	b.harnessMode = mode
}

func isValidRuntimeMode(mode string) bool {
	switch harness.NormalizeMode(mode) {
	case string(harness.ModeReadOnly), "supervised", "auto", "fullAccess":
		return true
	default:
		return false
	}
}

func (b *MemoryBackend) defaultRuntimeModeLocked() string {
	mode, err := harness.ParseMode(string(b.harnessMode))
	if err != nil || mode == "" {
		return "auto"
	}
	if mode == harness.ModeReadOnly {
		return "supervised"
	}
	return string(mode)
}

// SetDefaultProvider 设置默认的 Provider Kind，未注册时返回错误。
func (b *MemoryBackend) SetDefaultProvider(kind string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.harnesses[kind]; !ok {
		return fmt.Errorf("unknown provider %q", kind)
	}
	b.defaultProvider = kind
	return nil
}

// Providers 返回已注册 Kind 的排序列表，默认的排在最前。
func (b *MemoryBackend) Providers() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	var others []string
	for k := range b.harnesses {
		if k != b.defaultProvider {
			others = append(others, k)
		}
	}
	sort.Strings(others)

	var res []string
	if b.defaultProvider != "" {
		if _, ok := b.harnesses[b.defaultProvider]; ok {
			res = append(res, b.defaultProvider)
		}
	}
	res = append(res, others...)
	return res
}

// Close 取消正在后台运行的模拟回复 goroutine、等待后台任务全部结束，并回收所有注册的 harness
// （实现了 io.Closer 的会被关闭，例如释放常驻子进程）。
func (b *MemoryBackend) Close() {
	// 先把所有待处理的审批/提问卡统一撤回：撤卡不依赖 ctx 取消，Provider 侧会立刻收到明确错误。
	b.mu.Lock()
	batches := make([]*withdrawBatch, 0, len(b.threads))
	for _, td := range b.threads {
		if batch := b.takeWithdrawBatchLocked(td, withdrawShutdown); batch != nil {
			batches = append(batches, batch)
		}
	}
	b.mu.Unlock()
	for _, batch := range batches {
		b.finishWithdraw(batch, withdrawShutdown)
	}
	b.cancel()
	// 先在关闭会话与工作区状态前停掉本地内容服务，避免预览请求落在半关闭的后端上。
	b.closeInspectorPreviewServer()
	b.mu.Lock()
	for _, td := range b.threads {
		if td.watchdogStop != nil {
			td.watchdogStop()
			td.watchdogStop = nil
		}
	}
	// 停止所有待触发的会话同步定时器：退出时不再补同步，未传完的会话由持久待同步标记
	// 在下次启动时恢复（见 ResumeNowledgeSync），这样 Close 不必等网络。
	for _, state := range b.nowledgeSync {
		if state.timer != nil {
			state.timer.Stop()
			state.timer = nil
		}
	}
	b.mu.Unlock()
	b.wg.Wait()
	// 与在飞的待同步集合读改写做屏障，并把退出期间才置脏、尚未确保写进持久集合的会话
	// 补做一次本地落盘（只读写本地 SQLite，不发网络）。否则本地已有消息、重启却没有待同步标记。
	b.flushNowledgePendingFinal()

	b.mu.Lock()
	// 所有运行中的轮次已结束，此时关闭并清空检查点文件监听器。
	watchers := make([]git.Watcher, 0, len(b.watchers))
	for root, w := range b.watchers {
		watchers = append(watchers, w)
		delete(b.watchers, root)
	}
	harnesses := make([]harness.Harness, 0, len(b.harnesses))
	for _, h := range b.harnesses {
		harnesses = append(harnesses, h)
	}
	b.mu.Unlock()

	for _, w := range watchers {
		_ = w.Close()
	}

	for _, h := range harnesses {
		if c, ok := h.(io.Closer); ok {
			_ = c.Close()
		}
	}
}

// attachmentLimit 返回桌面端配置的单条消息附件上限；读不到配置时按默认值处理。
// 调用方不能在持有 b.mu 时调用本函数。
func (b *MemoryBackend) attachmentLimit() int {
	b.mu.Lock()
	dataDir := b.dataDir
	b.mu.Unlock()
	if dataDir == "" {
		return config.DefaultComposerAttachmentLimit
	}
	cfg, err := config.Load(dataDir)
	if err != nil {
		return config.DefaultComposerAttachmentLimit
	}
	config.ApplyEnv(&cfg, os.LookupEnv)
	return cfg.Session.AttachmentLimit()
}

// Capabilities 返回当前远程服务的能力声明。
func (b *MemoryBackend) Capabilities(ctx context.Context) (wire.Capabilities, error) {
	buildID := webassets.BuildID()
	if buildID == "" {
		buildID = "mcode-memory"
	}
	return wire.Capabilities{
		SchemaVersion:   1,
		Methods:         ImplementedMethods(),
		Features:        DefaultFeatures(),
		WebBuildID:      buildID,
		ActionJournalID: b.actionJournalID,
		AppVersion:      b.AppVersion(),
		AttachmentLimit: b.attachmentLimit(),
	}, nil
}

// SetAppVersion 设置对外声明的桌面端版本号（internal/app 的 AppVersion）。
func (b *MemoryBackend) SetAppVersion(version string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.appVersion = version
}

// AppVersion 返回当前声明的桌面端版本号，空串表示未提供。
func (b *MemoryBackend) AppVersion() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.appVersion
}

type unsolicitedOutputChecker interface {
	HasUnsolicitedOutput(threadID string) bool
}

// startedTurn 描述一轮刚在持锁状态下开启的执行，供解锁后完成落库、广播与后台运行。
// imageNames 与 imagePaths 按下标一一对应，保存附件原始文件名（可能为空），供 prompt 提示使用。
type startedTurn struct {
	userMsg        wire.ThreadMessage
	turnID         string
	runCtx         context.Context
	turnIndex      int
	currentSummary wire.ThreadSummary
	titleChanged   bool
	h              harness.Harness
	imagePaths     []string
	imageNames     []string
	// interactionMode、effort 在本轮开始时固定偏好，避免后台启动前的切换改变本轮行为。
	interactionMode string
	effort          string
	// attach 为真表示这一轮不发用户帧，只接住常驻进程自发输出的那一段。
	attach bool
	// deliveryFollowUpID 是本轮出队时选中的第一条排队项；非空表示这一轮要写 thread_deliveries。
	deliveryFollowUpID string
}

func applyTurnTiming(msg *wire.ThreadMessage, turns map[string]store.Turn) {
	if msg.TurnID == nil || *msg.TurnID == "" {
		return
	}
	turn, ok := turns[*msg.TurnID]
	if !ok {
		return
	}
	status := turn.Status
	if status == "starting" {
		status = "running"
	}
	switch status {
	case "running", "completed", "failed", "interrupted":
		msg.TurnStatus = &status
	default:
		msg.TurnStatus = nil
	}
	if !turn.StartedAt.IsZero() {
		started := turn.StartedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00")
		msg.TurnStartedAt = &started
	}
	if turn.CompletedAt != nil && !turn.CompletedAt.IsZero() {
		completed := turn.CompletedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00")
		msg.TurnCompletedAt = &completed
	}
}

// ThreadComposerPrefs 描述会话级 composer 偏好（交互模式、Lead effort、Bees 模型与 effort、人设）。
type ThreadComposerPrefs struct {
	InteractionMode, Effort, BeesProvider, BeesModel, BeesEffort string
	// Persona 是线程人设 id：空串表示从未设置，具体 id 为选中的人设，字面量 "none" 表示无人设。
	Persona string
}

// isValidEffort 校验 effort 取值，空串表示供应商默认。
func isValidEffort(effort string) bool {
	switch effort {
	case "", "low", "medium", "high", "xhigh", "max":
		return true
	}
	return false
}

// personaIDPattern 限定人设 id 的合法字符集与长度，防止任意文本被拼进 Turn.Prompt 前缀。
var personaIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// isValidPersona 校验人设取值：空串（从未设置）与字面量 none（无人设）合法，其余须是合法 id。
func isValidPersona(persona string) bool {
	if persona == "" || persona == "none" {
		return true
	}
	return personaIDPattern.MatchString(persona)
}

// effectivePairID 计算会话生效的配对 ID：显式选中的配对仍与当前模型一致时用它，
// 否则按当前主导与子代理模型推断；都没命中时返回空串，表示会话不套用任何配对。
func effectivePairID(pairs []swarm.Pair, explicitID, leadProvider, leadModel, beeProvider, beeModel string) string {
	if p, ok := swarm.FindPair(pairs, explicitID); ok && swarm.PairStillMatches(p, leadProvider, leadModel, beeProvider, beeModel) {
		return p.ID
	}
	if p, ok := swarm.MatchPair(pairs, leadProvider, leadModel, beeProvider, beeModel); ok {
		return p.ID
	}
	return ""
}

// swarmPairsFromConfig 把配置里的配对模板转成编排运行时的配对，只保留匹配生效配对所需的字段，
// Worker 侧对应子代理侧（与 app 层的配置转换保持同一套字段映射）。
func swarmPairsFromConfig(pairs []config.SwarmPair) []swarm.Pair {
	if len(pairs) == 0 {
		return nil
	}
	out := make([]swarm.Pair, 0, len(pairs))
	for _, p := range pairs {
		// 没有 ID 的配对无法被引用，也不能成为会话生效配对（与 providers.list 的过滤口径一致）。
		if p.ID == "" {
			continue
		}
		out = append(out, swarm.Pair{
			ID:           p.ID,
			Name:         p.Name,
			LeadProvider: p.LeadProvider,
			LeadModel:    p.LeadModel,
			BeeProvider:  p.WorkerProvider,
			BeeModel:     p.WorkerModel,
		})
	}
	return out
}

// trimOr 返回去除首尾空白的 value，为空时回退到 fallback（同样去除首尾空白）。
func trimOr(value, fallback string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(fallback)
}

const (
	backgroundTaskLogMaxBytes = 64 * 1024
	backgroundTaskLogMaxLines = 20
)

var (
	backgroundTaskIDPattern   = regexp.MustCompile(`Command running in background with ID:\s*([^\s.]+)`)
	backgroundTaskPathPattern = regexp.MustCompile(`Output is being written to:\s*(\S+)`)
	backgroundTaskFilePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+\.output$`)
)

type backgroundTaskStopper interface {
	StopBackgroundTask(ctx context.Context, threadID, taskID string) error
}

func backgroundTaskDetails(text string) (string, string) {
	var taskID, path string
	if match := backgroundTaskIDPattern.FindStringSubmatch(text); len(match) > 1 {
		taskID = match[1]
	}
	if match := backgroundTaskPathPattern.FindStringSubmatch(text); len(match) > 1 {
		path = strings.TrimRight(match[1], ".")
	}
	return taskID, path
}

// finishBackgroundTaskLocked 回写状态并使后台计数失效，调用方持有 b.mu。
func finishBackgroundTaskLocked(td *memoryThread, idx int, status, now string) {
	td.messages[idx].ToolStatus = &status
	td.messages[idx].ToolCompletedAt = &now
	td.bgCount.invalidate()
}

// killRunningCommandGrace 是 SIGTERM 到 SIGKILL 之间的宽限期。
const killRunningCommandGrace = 3 * time.Second

// turnRevertTarget 描述撤销某一轮次所需的 git 环境、检查点参数与线程全部轮次记录。
type turnRevertTarget struct {
	runner *git.Runner
	root   string
	base   string
	finish string
	turns  []store.Turn
}

// isLostSessionError 检查错误文本是否匹配会话失效特征（不区分大小写）。
func isLostSessionError(text string) bool {
	lowered := strings.ToLower(text)
	targets := []string{
		"no conversation found",
		"session not found",
		"thread not found",
		"conversation not found",
		"no rollout found",
		"unknown session",
	}
	for _, target := range targets {
		if strings.Contains(lowered, target) {
			return true
		}
	}
	return false
}

// isTransientNetworkError 检查错误文本是否匹配网络瞬断特征（不区分大小写）。
func isTransientNetworkError(text string) bool {
	lowered := strings.ToLower(text)
	targets := []string{
		"socket hang up",
		"connection reset",
		"broken pipe",
		"unexpected eof",
		"econnreset",
		"und_err_socket",
		"socket connection was closed unexpectedly",
		"network error",
		"stream was interrupted",
		// provider 端的流空闲超时与流中断同样值得重试。
		"stream idle timeout",
		"stream disconnected",
		"rst_stream",
		"fetch failed",
	}
	for _, target := range targets {
		if strings.Contains(lowered, target) {
			return true
		}
	}
	return false
}

func isProviderCrashError(text string) bool {
	text = strings.ToLower(text)
	if strings.Contains(text, "api error") || strings.Contains(text, "api 错误") || strings.Contains(text, "http ") || strings.Contains(text, "http/") || strings.Contains(text, "status ") || strings.Contains(text, "status:") {
		return false
	}
	for _, marker := range []string{"signal: ", "process exited", "app-server exited", "codex turn incomplete", "process closed connection unexpectedly"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func isOutdatedCLIError(text string) bool {
	text = strings.ToLower(text)
	for _, marker := range []string{
		"cli version is no longer supported",
		"please update your cli",
		"please upgrade your cli",
		"client version is no longer supported",
		"cli is out of date",
		// 模型版本高于 CLI 版本时的报错（例如 “requires Claude Code CLI 2.x or newer is required”）。
		"or newer is required",
		"requires a newer version",
		"claude update",
		"codex update",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// turnEndState 依据 runCtx 与错误文本还原轮次结束状态（正常/被中断）。
func turnEndState(runCtx context.Context, finalErrText string) (isNormal, isInterrupted bool) {
	isInterrupted = runCtx.Err() != nil
	isNormal = !isInterrupted && finalErrText == ""
	return isNormal, isInterrupted
}

// compactCommandText 是原生上下文压缩命令的原文。
const compactCommandText = "/compact"

// harnessTurnPrompt 返回交给 harness.Turn 的 Prompt：原生 /compact 必须原样送达（Provider 靠字面
// 匹配识别斜杠命令，任何前后缀都会让它失效），其它消息按「人设前缀 + 已装饰正文」拼接。
func harnessTurnPrompt(compactOnly bool, personaPrefix, decoratedPrompt string) string {
	if compactOnly {
		return compactCommandText
	}
	return personaPrefix + decoratedPrompt
}

// mediaRefNames 按下标取出附件的原始文件名，与 consumeAttachmentsLocked 返回的路径切片同序。
func mediaRefNames(refs []wire.MediaRef) []string {
	if len(refs) == 0 {
		return nil
	}
	names := make([]string, len(refs))
	for i, ref := range refs {
		names[i] = ref.Name
	}
	return names
}

// attachmentPromptName 返回附件在 prompt 提示里可用的文件名：换行/回车替换成空格以免破坏列表结构；
// 名为空或与路径 basename 相同（原始文件名缺失，只剩存储 ID）时返回空串，调用方只提示路径。
func attachmentPromptName(name, path string) string {
	name = strings.NewReplacer("\r", " ", "\n", " ").Replace(strings.TrimSpace(name))
	if name == "" || name == filepath.Base(path) {
		return ""
	}
	return name
}

// maxLastErrorTextRunes 是会话摘要里保留的错误原文上限（按 rune 计）。
// 500 字足够覆盖额度提示里的重置时间片段，又不至于把整段日志塞进每次摘要推送。
const maxLastErrorTextRunes = 500

// truncateLastErrorText 把错误原文截断到 maxLastErrorTextRunes 个字符，供会话摘要下发。
func truncateLastErrorText(text string) string {
	runes := []rune(text)
	if len(runes) <= maxLastErrorTextRunes {
		return text
	}
	return string(runes[:maxLastErrorTextRunes])
}

// availabilityReporter 是探测 Provider 运行时可用状态的可选接口。
// 由具体 harness 实现（例如探测可执行文件是否存在），未实现该接口的 Provider 默认视为可用。
type availabilityReporter interface {
	Available() (bool, string)
}

func providerDisplayName(kind string) string {
	switch kind {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "memory":
		return "Memory"
	case "cline":
		return "Cline"
	case "cursor":
		return "Cursor"
	case "opencode":
		return "OpenCode"
	case "grok":
		return "Grok"
	case "devin":
		return "Devin"
	case "droid":
		return "Factory Droid"
	case "openai":
		return "OpenAI"
	default:
		return kind
	}
}

// providerSupportsImages 与 provider 能力表保持一致：
// claude / codex / openai 与 ACP agents 中 cline, cursor, opencode, devin, droid 支持图片；
// grok 在能力表中为 false，此处同样不声明图片能力。
func providerSupportsImages(kind string) bool {
	switch kind {
	case "claude", "codex", "openai", "cline", "cursor", "opencode", "devin", "droid":
		return true
	default:
		return false
	}
}

// withConfiguredModel 把配置中指定的模型并入目录并置于首位；
// 已在目录中的模型只提升位置，保留其名称与强度档位，空值原样返回目录拷贝。
func withConfiguredModel(models []wire.ProviderModel, model string) []wire.ProviderModel {
	model = strings.TrimSpace(model)
	out := make([]wire.ProviderModel, 0, len(models)+1)
	if model == "" {
		return append(out, models...)
	}
	var existing *wire.ProviderModel
	for _, m := range models {
		if m.ID == model {
			found := m
			existing = &found
			break
		}
	}
	if existing != nil {
		row := *existing
		row.IsDefault = true
		out = append(out, row)
		for _, m := range models {
			if m.ID == model {
				continue
			}
			row := m
			row.IsDefault = false
			out = append(out, row)
		}
		return out
	}
	out = append(out, wire.ProviderModel{ID: model, Name: model, IsDefault: true})
	for _, m := range models {
		row := m
		row.IsDefault = false
		out = append(out, row)
	}
	return out
}

// providerAllowsModel 判断配对模板里指定的模型是否在该 provider 的目录里（空模型表示不指定）。
// 目录为空、只有占位行，或由 CLI 动态列出的 provider 放行任意非空模型名，对齐桌面的 providerHasModel；
// pins 是用户固定的模型列表，命中时同样放行，避免自定义模型在配对与草稿里被过滤掉。
func providerAllowsModel(p wire.Provider, model string, pins []config.ModelPin) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return true
	}
	if hasPlaceholderCatalog(p.Models) || harness.IsCLICatalogProvider(p.ID) || strings.HasPrefix(p.ID, "custom-") {
		return true
	}
	for _, m := range p.Models {
		if m.ID == model {
			return true
		}
	}
	for _, pin := range pins {
		if pin.Provider == p.ID && pin.ModelID == model {
			return true
		}
	}
	return false
}

// matchSwarmPairID 在一组已下发的编排组合里按主导与执行侧选择找第一条完全匹配的组合 ID。
// 执行侧 provider/model 为空表示跟随主导，worker 侧为空的模型跟随配对主导模型；
// 匹配规则统一由 swarm.MatchPair 提供（主导模型一致的配对优先，其次才是主导模型为空的通配配对）。
func matchSwarmPairID(pairs []wire.SwarmPair, leadProvider, leadModel, beesProvider, beesModel string) string {
	candidates := make([]swarm.Pair, 0, len(pairs))
	for _, p := range pairs {
		candidates = append(candidates, swarm.Pair{
			ID:           p.ID,
			LeadProvider: p.Provider,
			LeadModel:    derefString(p.LeadModel),
			BeeProvider:  derefString(p.WorkerProvider),
			BeeModel:     derefString(p.WorkerModel),
		})
	}
	if p, ok := swarm.MatchPair(candidates, leadProvider, leadModel, beesProvider, beesModel); ok {
		return p.ID
	}
	return ""
}

// RemoveConn 从事件推送中心移除指定连接。
func (b *MemoryBackend) RemoveConn(c Conn) {
	if b.hub != nil {
		b.hub.RemoveConn(c)
	}
}

// turnMetadataLimit 是 threads.getDiff 下发的轮次摘要上限，超出的轮次只置 metadataTruncated。
const turnMetadataLimit = 50

var queueRetryDelay = 5 * time.Second
