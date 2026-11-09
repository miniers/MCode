package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mcode/internal/agentconf"
	"mcode/internal/config"
	"mcode/internal/git"
	"mcode/internal/harness"
	"mcode/internal/memspace"
	"mcode/internal/nmem"
	"mcode/internal/procs"
	"mcode/internal/projctx"
	"mcode/internal/providers/agy/accounts"
	"mcode/internal/remote/wire"
	"mcode/internal/startuptrace"
	"mcode/internal/store"
	"mcode/internal/swarm"
	"mcode/internal/webassets"
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
	bgCount bgCountCache

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
	mu                sync.Mutex
	stewardFeedMu     sync.Mutex
	stewardFeedID     string
	stewardFeedLoaded bool
	stewardDesk       StewardDesk
	queuePersistMu    sync.Mutex
	turnPersistMu     sync.Mutex
	wg                sync.WaitGroup
	hub               *StreamHub
	projects          map[string]wire.ProjectSummary
	threads           map[string]*memoryThread
	usage             map[string]map[int]harness.Usage
	actionThreads     map[string]string
	actions           *actionLedger
	store             *store.Store

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
	watchers        map[string]git.Watcher
	swarmAvailable  bool
	swarmEnabledFor func(threadID string) bool
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

func (b *MemoryBackend) nowFormatted() string {
	return b.now().Format(time.RFC3339Nano)
}

// SetSwarm 设置 Swarm 能力可用性及会话启用判断函数。
func (b *MemoryBackend) SetSwarm(available bool, enabledFor func(threadID string) bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.swarmAvailable = available
	b.swarmEnabledFor = enabledFor
}

// SetRevertBusyCheck 设置撤销前的额外占用检查：返回非空字符串表示该工作区正被占用，
// 内容作为拒绝撤销的原因展示给用户。未注入时撤销只检查会话自身的运行状态。
func (b *MemoryBackend) SetRevertBusyCheck(fn func(threadID, root string) string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.revertBusyCheck = fn
}

// SetSwarmEnabler 设置新建会话时立即开启编排的回调（与 threads.swarm.set 同一后端实现）。
// 未注入时新建会话只把编排开关记在摘要里，等客户端自行调用 threads.swarm.set。
func (b *MemoryBackend) SetSwarmEnabler(fn func(threadID string, enabled bool)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.swarmEnablerFunc = fn
}

// SetSwarmInheritor 设置转发会话时继承编排状态的回调：新会话从源会话继承编排开关与只规划覆盖值。
// 未注入时转发出的新会话不继承编排状态。
func (b *MemoryBackend) SetSwarmInheritor(fn func(fromThreadID, toThreadID string)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.swarmInheritFunc = fn
}

// SetSwarmBusy 设置 Swarm 忙闲判定函数：返回 true 表示该主会话名下仍有
// 未结束的子线程，或仍有待投递给它的子线程回报。
func (b *MemoryBackend) SetSwarmBusy(busy func(threadID string) bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.swarmBusyFor = busy
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

// SetPromptDecorator 设置发送给 Provider 之前的 Prompt 装饰钩子。
func (b *MemoryBackend) SetPromptDecorator(fn func(threadID, prompt string) string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.promptDecorator = fn
}

// SetNowledge 注入 Nowledge Mem 客户端。为 nil 时所有 Nowledge 功能（首轮 Working Memory
// 注入、会话同步）一律关闭，不发起任何网络请求。
func (b *MemoryBackend) SetNowledge(c *nmem.Client) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nowledge = c
}

// SetLocalMemory 注入本地记忆的全局规则生成函数。
func (b *MemoryBackend) SetLocalMemory(guidance func(maxRunes int) string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.localMemoryGuidance = guidance
}

// nowledgeClient 返回已注入的 Nowledge 客户端；未注入时返回 nil。
// 只持锁取出指针，实际请求在锁外进行。
func (b *MemoryBackend) nowledgeClient() *nmem.Client {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nowledge
}

// turnMemoryEnv 返回本轮额外注入的环境变量：本地记忆后端自己管理存储空间，不需要
// Nowledge Mem 的 NMEM_SPACE；只有未注册本地回调（可继续使用本机 nmem CLI）时才按项目目录
// 注入该变量。
func (b *MemoryBackend) turnMemoryEnv(workDir string) []string {
	b.mu.Lock()
	guidance := b.localMemoryGuidance
	b.mu.Unlock()
	if guidance != nil {
		return nil
	}
	return memspace.Env(workDir)
}

// SetPersonaTagResolver 设置线程人设的标签前缀解析钩子：按会话的 provider/model 把 persona
// 换算成拼在 Turn.Prompt 最前面的前缀，返回空串表示本轮不注入；nil 或未设置时不注入。
func (b *MemoryBackend) SetPersonaTagResolver(fn func(provider, model, persona string) string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.personaTag = fn
}

// SetGuidanceBuilder 覆盖首轮项目 Context 指导词的构建函数，便于测试注入。
func (b *MemoryBackend) SetGuidanceBuilder(fn func(workDir string) string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.guidanceBuilder = fn
}

// SetFirstTurnExtra 设置随首轮注入一起前置的额外段落（普通会话里「可以交给总管」的提示），
// 返回空串表示本轮不附加；nil 表示永不附加。只在本轮触发首轮指导词注入时生效。
func (b *MemoryBackend) SetFirstTurnExtra(fn func(threadID string) string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.firstTurnExtra = fn
}

// beeSkillIndexDescriptionMaxRunes 是子代理（bee）精简技能目录里单条描述保留的最大字符数，
// 必须与 projctx.skillIndexDescriptionMaxRunes 对齐（该常量未导出，这里按同一数值固定）。
const beeSkillIndexDescriptionMaxRunes = 100

const localMemoryToolsNote = "## Local memory tools\n\n" +
	"Use the memory tool's propose action to remember information instead of writing your own memory file. " +
	"Use its search action to search project and global memories. " +
	"When the user explicitly asks you to remember something, set user_requested to true. " +
	"Memories are written to project or global documents; to update the project 当前重点, set target_memory_id to project:overview#当前重点."

// defaultGuidanceBuilder 组装首轮注入的内容：项目 Context 指导词加技能精简目录（描述放宽到
// projctx.LeadSkillIndexDescriptionMaxRunes 个字符），空的部分省略。
func (b *MemoryBackend) defaultGuidanceBuilder(workDir string) string {
	return b.buildGuidance(workDir, projctx.LeadSkillIndexDescriptionMaxRunes, false)
}

// beeGuidanceBuilder 组装派发给子代理（bee）的首轮注入内容：在项目 Context 指导词之外附带
// 一份精简技能目录（每个技能一行，描述截断），避免全量描述挤占 bee 的上下文，bee 需要时
// 自行读取对应 SKILL.md。
func (b *MemoryBackend) beeGuidanceBuilder(workDir string) string {
	return b.buildGuidance(workDir, beeSkillIndexDescriptionMaxRunes, true)
}

// buildGuidance 组装首轮注入的内容：项目 Context 指导词加技能段落，空的部分省略。
// maxSkillRunes 大于 0 时技能段落输出精简技能目录（描述按该值截断），等于 0 时输出带完整
// 描述的全量清单。forBee 为 true 时代表为子代理（bee）组装，跳过记忆工具说明等仅主会话适用的段落。
func (b *MemoryBackend) buildGuidance(workDir string, maxSkillRunes int, forBee bool) string {
	var parts []string
	workbench := b.workbenchSettings()
	if workbench.ContextGuidanceEnabled() {
		maxChars := workbench.ContextInjectMaxCharacters
		if maxChars <= 0 {
			maxChars = config.DefaultContextInjectMaxCharacters
		}
		guidance, err := projctx.BuildGuidanceWithOptions(workDir, projctx.GuidanceOptions{
			MaxChars:   maxChars,
			CustomDocs: workbench.ContextInjectCustomDocs,
		})
		if err == nil && strings.TrimSpace(guidance) != "" {
			parts = append(parts, guidance)
		}
	}
	if b.generalSettings().Session.SkillsInjectPromptEnabled() {
		if home, err := os.UserHomeDir(); err == nil {
			// 停用状态由设置页写入 <dataDir>/skills-projects/ 下，这里只做剔除，不改注入格式。
			disabled := b.disabledSkillPaths(home, workDir)
			var excluded func(path string) bool
			if len(disabled) > 0 {
				excluded = func(path string) bool { return disabled[normalizeSkillPath(path)] }
			}
			var skills string
			if maxSkillRunes > 0 {
				skills = projctx.BuildSkillsIndexWithLimitExcluding(workDir, home, excluded, maxSkillRunes)
			} else {
				skills = projctx.BuildSkillsGuidanceExcluding(workDir, home, excluded)
			}
			if strings.TrimSpace(skills) != "" {
				parts = append(parts, skills)
			}
		}
	}
	// Nowledge 已接入且非 bee 会话时，若配置开启则说明记忆工具的用法：用户要求记住的内容走 memory_propose，不写 agent 自己的记忆文件。
	if b.nowledgeClient() != nil && !forBee && b.nowledgeSettings().MemoryToolsNoteEnabled() {
		parts = append(parts, nowledgeMemoryToolsNote)
	}
	// 本地记忆后端：工具说明只给非 bee 会话，全局规则对 bee 与会话都注入（Working Memory 同样
	// 注入给 bee），空规则不追加；guidance 回调在锁外调用。
	if b.nowledgeClient() == nil {
		b.mu.Lock()
		guidance := b.localMemoryGuidance
		b.mu.Unlock()
		if guidance != nil {
			settings := b.nowledgeSettings()
			if !forBee && settings.MemoryToolsNoteEnabled() {
				parts = append(parts, localMemoryToolsNote)
			}
			if rules := guidance(settings.WorkingMemoryBudget()); strings.TrimSpace(rules) != "" {
				parts = append(parts, rules)
			}
		}
	}
	// Nowledge Working Memory 放在最后，作为项目文档与技能目录之后的背景参考。
	if wm := b.nowledgeWorkingMemory(workDir); wm != "" {
		parts = append(parts, wm)
	}
	return strings.Join(parts, "\n\n")
}

// disabledSkillPaths 返回设置页已停用技能的规范路径集合（键为 SKILL.md 所在目录）。
// 扫描参数与设置页一致（Home/ProjectDir/DataDir），保证读到同一份停用配置（项目级停用现在
// 存放在 <dataDir>/skills-projects/ 下，仍兼容读旧的 <项目>/.mcode/skills.json）；
// 扫描失败时返回 nil，由调用方不过滤（注入全部技能）。
func (b *MemoryBackend) disabledSkillPaths(home, workDir string) map[string]bool {
	b.mu.Lock()
	dataDir := b.dataDir
	b.mu.Unlock()

	skills, err := agentconf.ScanSkills(agentconf.Options{Home: home, ProjectDir: workDir, DataDir: dataDir})
	if err != nil {
		log.Printf("扫描技能停用状态失败，改为注入全部技能: %v", err)
		return nil
	}
	disabled := make(map[string]bool)
	for _, skill := range skills {
		// ScanSkills 已按 IsSkillActive 填好 Disabled（同时看项目级与全局 skills.json）。
		if skill.Disabled {
			disabled[normalizeSkillPath(skill.Path)] = true
		}
	}
	return disabled
}

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

// guidancePending 判断该线程是否仍应注入首轮指导词：尚未成功交付，且历史里还不存在
// 已获得成功回复的普通用户消息。只有正文回复（assistant/text）才算成功回复，错误、
// 思考与工具消息都不算；上一轮失败或中断时保持待注入，下一条普通消息会重试。
func (b *MemoryBackend) guidancePending(threadID string, priorHistory []wire.ThreadMessage) bool {
	b.mu.Lock()
	delivered := b.guidanceDelivered[threadID]
	b.mu.Unlock()
	if delivered {
		return false
	}

	ordinaryUserSeen := false
	answered := false
	for _, msg := range priorHistory {
		switch msg.Role {
		case "user":
			if isGuidanceTriggerText(msg.Text) {
				ordinaryUserSeen = true
			}
		case "assistant":
			if msg.Kind == nil || (*msg.Kind != "assistant" && *msg.Kind != "text") {
				continue
			}
			if ordinaryUserSeen && strings.TrimSpace(msg.Text) != "" {
				answered = true
			}
		}
	}
	return !answered
}

// markGuidanceDelivered 记录线程已成功交付首轮指导词。
func (b *MemoryBackend) markGuidanceDelivered(threadID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.guidanceDelivered == nil {
		b.guidanceDelivered = make(map[string]bool)
	}
	b.guidanceDelivered[threadID] = true
}

// SetSwarmQueueHooks 设置 Swarm 队列决策钩子。
func (b *MemoryBackend) SetSwarmQueueHooks(hasWorkingBees func(threadID string) bool, sendToBee func(ctx context.Context, threadID, text string) (string, error)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.swarmHasWorkingBees = hasWorkingBees
	b.swarmSendToBee = sendToBee
}

func (b *MemoryBackend) hasWorkingSwarmBees(threadID string) bool {
	b.mu.Lock()
	fn := b.swarmHasWorkingBees
	b.mu.Unlock()
	return fn != nil && fn(threadID)
}

// SetGeneralSettingsFunc 设置通用设置获取函数（返回整份配置，调用方按段取用）。
func (b *MemoryBackend) SetGeneralSettingsFunc(fn func() config.Config) {
	if fn == nil {
		b.generalSettingsFunc.Store(nil)
		return
	}
	b.generalSettingsFunc.Store(&fn)
}

// generalSettings 返回当前通用配置（若未设置则返回零值配置）。
// 本函数不加 b.mu，可在持锁时调用，但传入的回调本身不得再获取 b.mu。
func (b *MemoryBackend) generalSettings() config.Config {
	if p := b.generalSettingsFunc.Load(); p != nil && *p != nil {
		return (*p)()
	}
	return config.Config{}
}

// SetWorkbenchSettingsFunc 设置工作台设置获取函数。
func (b *MemoryBackend) SetWorkbenchSettingsFunc(fn func() config.WorkbenchConfig) {
	if fn == nil {
		b.workbenchSettingsFunc.Store(nil)
		return
	}
	b.workbenchSettingsFunc.Store(&fn)
}

// SetNowledgeSettingsFunc 设置 Nowledge Mem 集成设置的获取函数（含两个开关、注入预算与空间覆盖）。
func (b *MemoryBackend) SetNowledgeSettingsFunc(fn func() config.MemoryConfig) {
	if fn == nil {
		b.nowledgeSettingsFunc.Store(nil)
		return
	}
	b.nowledgeSettingsFunc.Store(&fn)
}

// workbenchSettings 返回当前工作台配置（若未设置则返回零值配置）。
// 本函数不加 b.mu，可在持锁时调用，但传入的回调本身不得再获取 b.mu。
func (b *MemoryBackend) workbenchSettings() config.WorkbenchConfig {
	if p := b.workbenchSettingsFunc.Load(); p != nil && *p != nil {
		return (*p)()
	}
	return config.WorkbenchConfig{}
}

// nowledgeSettings 返回当前 Nowledge Mem 集成设置（若未设置则返回零值配置）。
// 本函数不加 b.mu，可在持锁时调用，但传入的回调本身不得再获取 b.mu。
func (b *MemoryBackend) nowledgeSettings() config.MemoryConfig {
	if p := b.nowledgeSettingsFunc.Load(); p != nil && *p != nil {
		return (*p)()
	}
	return config.MemoryConfig{}
}

// SetSwarmConfigFunc 设置编排（Swarm）配置获取函数。
func (b *MemoryBackend) SetSwarmConfigFunc(fn func() config.OrchestrationConfig) {
	if fn == nil {
		b.swarmConfigFunc.Store(nil)
		return
	}
	b.swarmConfigFunc.Store(&fn)
}

// swarmConfig 返回当前 Swarm 配置（若未设置则返回零值配置）。
// 本函数不加 b.mu，可在持锁时调用，但传入的回调本身不得再获取 b.mu。
func (b *MemoryBackend) swarmConfig() config.OrchestrationConfig {
	if p := b.swarmConfigFunc.Load(); p != nil && *p != nil {
		return (*p)()
	}
	return config.OrchestrationConfig{}
}

// SetProviderEnabledFunc 设置 provider 启用状态获取函数（键为 provider kind，false 表示在桌面设置里已关闭）。
// 未注入时所有 provider 都视为启用。
func (b *MemoryBackend) SetProviderEnabledFunc(fn func() map[string]bool) {
	if fn == nil {
		b.providerEnabledFunc.Store(nil)
		return
	}
	b.providerEnabledFunc.Store(&fn)
}

// providerEnabled 返回 provider 启用状态表；未注入时返回 nil（调用方按「未知视为启用」处理）。
// 本函数不加 b.mu，可在持锁时调用，但传入的回调本身不得再获取 b.mu。
func (b *MemoryBackend) providerEnabled() map[string]bool {
	if p := b.providerEnabledFunc.Load(); p != nil && *p != nil {
		return (*p)()
	}
	return nil
}

// SetDraftDefaultsFunc 设置新建会话草稿默认值构建函数（桌面模型记忆 + 编排选择）。
// 未注入时 providers.list 的 draft 回落到宿主默认 provider 与默认模型。
func (b *MemoryBackend) SetDraftDefaultsFunc(fn func() wire.ComposerDraftDefaults) {
	if fn == nil {
		b.draftDefaultsFunc.Store(nil)
		return
	}
	b.draftDefaultsFunc.Store(&fn)
}

// draftDefaults 返回注入的草稿默认值；未注入时 ok 为 false。
// 本函数不加 b.mu，可在持锁时调用，但传入的回调本身不得再获取 b.mu。
func (b *MemoryBackend) draftDefaults() (wire.ComposerDraftDefaults, bool) {
	if p := b.draftDefaultsFunc.Load(); p != nil && *p != nil {
		return (*p)(), true
	}
	return wire.ComposerDraftDefaults{}, false
}

// fillThreadCreateDefaultsLocked 用宿主记忆（draft defaults）补齐用户新建会话没传的选择。
// 按组补齐，避免把属于别的模型/别的组合的取值套上去：
//   - 主导组：params.Model 与 Options 里的 provider、model 都没给时，
//     provider、model、effort、fastMode 一起取默认值；只要给了 provider 或 model 中的任意一个，
//     effort 与 fastMode 一律不补。
//   - 子代理组：三个字段都为 nil 时一起取默认值（nil 表示空串，provider 与 model 都空表示跟随主导）。
//   - 编排开关：Options.SwarmEnabled 为 nil 时取默认值。
//
// 补齐的 provider 必须已注册、model 必须通过 validModel（目录命中或已固定），否则主导组保持原样（回到默认值）。
// 调用方必须持有 b.mu。
func (b *MemoryBackend) fillThreadCreateDefaultsLocked(params *wire.ThreadCreateParams) {
	draft, ok := b.draftDefaults()
	if !ok {
		return
	}
	opts := wire.ComposerOptions{}
	if params.Options != nil {
		opts = *params.Options
	}
	if params.Model == nil && opts.Provider == nil && opts.Model == nil && draft.Provider != "" {
		_, providerKnown := b.harnesses[draft.Provider]
		draftModel := derefString(draft.Model)
		if providerKnown && (draftModel == "" || b.validModel(draft.Provider, draftModel)) {
			provider := draft.Provider
			opts.Provider = &provider
			opts.Model = draft.Model
			opts.Effort = draft.Effort
			fastMode := draft.FastMode
			opts.FastMode = &fastMode
		}
	}
	if opts.SwarmBeesProvider == nil && opts.SwarmBeesModel == nil && opts.SwarmBeesEffort == nil {
		opts.SwarmBeesProvider = draft.SwarmBeesProvider
		opts.SwarmBeesModel = draft.SwarmBeesModel
		opts.SwarmBeesEffort = draft.SwarmBeesEffort
	}
	if opts.SwarmEnabled == nil {
		swarmEnabled := draft.SwarmEnabled
		opts.SwarmEnabled = &swarmEnabled
	}
	params.Options = &opts
}

// SetRememberSelectionFunc 设置 providers.rememberSelection 写回宿主记忆的回调。
func (b *MemoryBackend) SetRememberSelectionFunc(fn func(params wire.ProvidersRememberSelectionParams)) {
	if fn == nil {
		b.rememberSelectionFunc.Store(nil)
		return
	}
	b.rememberSelectionFunc.Store(&fn)
}

// rememberSelection 返回写回宿主记忆的回调；未注入时返回 nil。
func (b *MemoryBackend) rememberSelection() func(params wire.ProvidersRememberSelectionParams) {
	if p := b.rememberSelectionFunc.Load(); p != nil && *p != nil {
		return *p
	}
	return nil
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

// AppendNotice 向会话时间线追加一条系统提示条目，并进行持久化与增量推送。
func (b *MemoryBackend) AppendNotice(threadID, text string) error {
	return b.appendSystemMessage(threadID, "info", text)
}

// appendSystemMessage 向会话时间线追加一条指定 Kind 的系统消息，并进行持久化与增量推送。
func (b *MemoryBackend) appendSystemMessage(threadID, kind, text string) error {
	nowTime := time.Now().UTC()

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return store.ErrNotFound
	}
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		b.mu.Unlock()
		return err
	}
	b.ensureMessagesLoadedLocked(td)
	msg, currentSummary := b.appendSystemMessageLocked(td, kind, text, nowTime)
	b.mu.Unlock()

	b.publishSystemMessage(threadID, msg, currentSummary, nowTime)
	return nil
}

// appendSystemMessageLocked 在已持有 b.mu 的前提下追加系统消息，返回消息副本与推送用摘要。
func (b *MemoryBackend) appendSystemMessageLocked(td *memoryThread, kind, text string, nowTime time.Time) (wire.ThreadMessage, wire.ThreadSummary) {
	now := nowTime.Format(time.RFC3339Nano)
	b.touchThreadLocked(td)

	turnIndex := 0
	var turnIDPtr *string
	if td.runTurnID != "" {
		tid := td.runTurnID
		turnIDPtr = &tid
	}
	if len(td.messages) > 0 {
		turnIndex = td.messages[len(td.messages)-1].TurnIndex
	}

	msg := wire.ThreadMessage{
		ID:        newUUID(),
		Role:      "system",
		Text:      text,
		CreatedAt: now,
		TurnIndex: turnIndex,
		TurnID:    turnIDPtr,
		Kind:      &kind,
	}

	td.messages = append(td.messages, msg)
	td.summary.UpdatedAt = now
	return msg, b.summaryForPublishLocked(td)
}

// publishSystemMessage 把新追加的系统消息写库并增量推送，最后刷新会话时间。
func (b *MemoryBackend) publishSystemMessage(threadID string, msg wire.ThreadMessage, currentSummary wire.ThreadSummary, nowTime time.Time) {
	b.persistMessages(threadID, []wire.ThreadMessage{msg})
	b.hub.Publish(threadID, []wire.ThreadMessage{msg}, nil, currentSummary)

	if s := b.store; s != nil {
		pCtx, pCancel := persistCtx()
		_ = s.TouchThread(pCtx, threadID, nowTime)
		pCancel()
	}
}

// RequestSplit 请求分割会话：在时间线末尾追加一条分割线（split_session），
// 从下一条消息起 provider 只看到分割线之后的历史。
// 分割线本身就是持久标记，重启后状态一致；已经处于待分割状态时幂等返回 nil。
func (b *MemoryBackend) RequestSplit(threadID string) error {
	nowTime := time.Now().UTC()

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return store.ErrNotFound
	}
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		b.mu.Unlock()
		return err
	}
	b.ensureMessagesLoadedLocked(td)
	if td.runTurnID != "" {
		b.mu.Unlock()
		return ErrSplitRunning
	}
	conversed := false
	for _, msg := range td.messages {
		if msg.Role == "user" || msg.Role == "assistant" {
			conversed = true
			break
		}
	}
	if !conversed {
		b.mu.Unlock()
		return ErrSplitEmpty
	}
	if pendingSplit(td.messages) {
		// 已经有一条还没生效的分割线，不追加第二条。
		b.mu.Unlock()
		return nil
	}
	msg, currentSummary := b.appendSystemMessageLocked(td, splitSessionKind, splitSessionText, nowTime)
	b.mu.Unlock()

	b.publishSystemMessage(threadID, msg, currentSummary, nowTime)
	return nil
}

// AppendAssistantMessage 追加一条总管主动发出的助手消息：Role 为 assistant、Kind 为空，
// 并标记为最终回复，作为可见回复展示（开场白就是这样做显的）。与 AppendNotice 一样持久化
// 并增量推送，只是不摥任何 Kind。
func (b *MemoryBackend) AppendAssistantMessage(threadID, text string) error {
	nowTime := time.Now().UTC()
	now := nowTime.Format(time.RFC3339Nano)

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return store.ErrNotFound
	}
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		b.mu.Unlock()
		return err
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)

	turnIndex := 0
	var turnIDPtr *string
	if td.runTurnID != "" {
		tid := td.runTurnID
		turnIDPtr = &tid
	}
	if len(td.messages) > 0 {
		turnIndex = td.messages[len(td.messages)-1].TurnIndex
	}

	final := true
	msg := wire.ThreadMessage{
		ID:              newUUID(),
		Role:            "assistant",
		Text:            text,
		CreatedAt:       now,
		TurnIndex:       turnIndex,
		TurnID:          turnIDPtr,
		IsFinalResponse: &final,
	}

	td.messages = append(td.messages, msg)
	td.summary.UpdatedAt = now
	currentSummary := b.summaryForPublishLocked(td)
	b.mu.Unlock()

	b.persistMessages(threadID, []wire.ThreadMessage{msg})
	b.hub.Publish(threadID, []wire.ThreadMessage{msg}, nil, currentSummary)

	if s := b.store; s != nil {
		pCtx, pCancel := persistCtx()
		_ = s.TouchThread(pCtx, threadID, nowTime)
		pCancel()
	}

	return nil
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

// SetGitRunner 设置 Git 执行器。
func (b *MemoryBackend) SetGitRunner(r *git.Runner) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.git = r
}

// watcherFor 返回指定仓库根目录对应的检查点文件监听器，按需懒创建并缓存。
// 调用方不得持有 b.mu。
func (b *MemoryBackend) watcherFor(root string) git.Watcher {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.watchers == nil {
		b.watchers = make(map[string]git.Watcher)
	}
	if w, ok := b.watchers[root]; ok {
		return w
	}
	w := git.NewWatcher(root)
	b.watchers[root] = w
	return w
}

// RegisterHarness 注册或替换同 Kind 的 harness，如果 defaultProvider 为空，就把它设为该 Kind。
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
	if b.defaultProvider == "" {
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
// 若注销的是 defaultProvider，则将其更新为剩余已注册 kind 中字典序最小者；若无剩余则置为空串。
func (b *MemoryBackend) UnregisterHarness(kind string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.harnesses, kind)
	if b.defaultProvider == kind {
		var remaining []string
		for k := range b.harnesses {
			remaining = append(remaining, k)
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

// AddProject 预置或更新内存中的项目数据。
func (b *MemoryBackend) AddProject(p wire.ProjectSummary) {
	b.mu.Lock()
	b.projects[p.ID] = p
	s := b.store
	b.mu.Unlock()

	if s != nil {
		pCtx, pCancel := persistCtx()
		defer pCancel()
		if err := s.UpsertProject(pCtx, projectToStore(p)); err != nil {
			log.Printf("store upsert project %s error: %v", p.ID, err)
		}
	}
}

// AddProjectPath 把路径转换为绝对路径并 Clean，路径必须是已存在的目录，否则返回错误；
// 如果已有项目的 Path 相同，直接返回该项目；否则新建项目，ID 用 newUUID()，Name 用目录的 base 名，调用 AddProject 并返回。
func (b *MemoryBackend) AddProjectPath(ctx context.Context, path string) (wire.ProjectSummary, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return wire.ProjectSummary{}, fmt.Errorf("resolve absolute path: %w", err)
	}
	cleanPath := filepath.Clean(absPath)

	fi, err := os.Stat(cleanPath)
	if err != nil {
		return wire.ProjectSummary{}, fmt.Errorf("project path does not exist: %w", err)
	}
	if !fi.IsDir() {
		return wire.ProjectSummary{}, fmt.Errorf("project path is not a directory: %s", cleanPath)
	}

	b.mu.Lock()
	for _, p := range b.projects {
		if filepath.Clean(p.Path) == cleanPath {
			b.mu.Unlock()
			return p, nil
		}
	}
	b.mu.Unlock()

	name := filepath.Base(cleanPath)
	proj := wire.ProjectSummary{
		ID:          newUUID(),
		Name:        name,
		Path:        cleanPath,
		IsExpanded:  true,
		ThreadCount: 0,
	}
	b.AddProject(proj)
	return proj, nil
}

// AddThread 预置或更新内存中的会话及其消息历史。
func (b *MemoryBackend) AddThread(t wire.ThreadSummary, messages []wire.ThreadMessage) {
	b.mu.Lock()
	if t.RuntimeMode == "" {
		t.RuntimeMode = b.defaultRuntimeModeLocked()
	}
	if t.OrderKey == "" || t.OrderKey == "0" {
		t.OrderKey = formatOrderKey(0)
	}
	msgs := make([]wire.ThreadMessage, len(messages))
	copy(msgs, messages)
	persisted := make(map[string]bool, len(messages))
	var dormantAt *time.Time
	if t.IsDormant {
		nowTime := time.Now().UTC()
		dormantAt = &nowTime
	}
	var parentID string
	if t.ParentThreadID != nil {
		parentID = *t.ParentThreadID
	}
	t.ParentThreadID = optionalString(parentID)
	td := &memoryThread{
		summary:        t,
		sortOrder:      0,
		queueRevision:  1,
		messages:       msgs,
		messagesLoaded: true,
		persisted:      persisted,
		pending:        make(map[string]*pendingRequest),
		alwaysAllow:    make(map[string]bool),
		parentThreadID: parentID,
		isHelper:       t.IsHelper,
		dormantAt:      dormantAt,
	}
	b.threads[t.ID] = td
	s := b.store
	b.mu.Unlock()

	if s != nil {
		pCtx, pCancel := persistCtx()
		st := threadToStore(t, "", 0)
		st.DormantAt = dormantAt
		st.ParentThreadID = td.parentThreadID
		st.IsHelper = td.isHelper
		if err := s.CreateThread(pCtx, st); err != nil {
			log.Printf("store create thread %s error: %v", t.ID, err)
		}
		pCancel()
		b.persistMessages(t.ID, msgs)
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

// KanbanBoard 返回空任务看板（各列表初始化为非 nil 空切片）。
func (b *MemoryBackend) KanbanBoard(ctx context.Context) (wire.KanbanBoard, error) {
	return wire.KanbanBoard{
		SchemaVersion: 1,
		Revision:      1,
		Projects:      []wire.KanbanProject{},
		Tasks:         []wire.KanbanTask{},
		Goals:         []wire.KanbanGoal{},
	}, nil
}

// ListProjects 返回已登记的项目列表。
func (b *MemoryBackend) ListProjects(ctx context.Context) (wire.ProjectList, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	counts := make(map[string]int, len(b.projects))
	for _, t := range b.threads {
		counts[t.summary.ProjectID]++
	}

	list := make([]wire.ProjectSummary, 0, len(b.projects))
	for _, p := range b.projects {
		if c, ok := counts[p.ID]; ok {
			p.ThreadCount = c
		} else {
			p.ThreadCount = 0
		}
		list = append(list, p)
	}
	// map 遍历无序，不排序的话侧边栏项目顺序每次刷新都会变
	sort.Slice(list, func(i, j int) bool {
		ni, nj := strings.ToLower(list[i].Name), strings.ToLower(list[j].Name)
		if ni != nj {
			return ni < nj
		}
		return list[i].ID < list[j].ID
	})

	return wire.ProjectList{
		Projects: list,
		Revision: 1,
	}, nil
}

// ListThreads 返回已登记的会话列表，支持基本的 projectID 过滤、归档过滤与游标分页。
func (b *MemoryBackend) ListThreads(ctx context.Context, params wire.ThreadListParams) (wire.ThreadList, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	type threadSortItem struct {
		summary   wire.ThreadSummary
		sortOrder int64
	}

	var candidates []threadSortItem
	for _, t := range b.threads {
		s := b.summaryForPublishLocked(t)
		if params.ProjectID != nil && *params.ProjectID != "" && s.ProjectID != *params.ProjectID {
			continue
		}
		includeArchived := params.IncludeArchived != nil && *params.IncludeArchived
		if !includeArchived && s.IsArchived {
			continue
		}
		if b.swarmEnabledFor != nil {
			s.SwarmEnabled = b.swarmEnabledFor(s.ID)
		}
		s.OrderKey = formatOrderKey(t.sortOrder)
		candidates = append(candidates, threadSortItem{
			summary:   s,
			sortOrder: t.sortOrder,
		})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].sortOrder != candidates[j].sortOrder {
			return candidates[i].sortOrder < candidates[j].sortOrder
		}
		ti, errI := parseTime(candidates[i].summary.UpdatedAt)
		tj, errJ := parseTime(candidates[j].summary.UpdatedAt)
		if errI == nil && errJ == nil && !ti.Equal(tj) {
			return ti.After(tj)
		}
		return candidates[i].summary.UpdatedAt > candidates[j].summary.UpdatedAt
	})

	var filtered []wire.ThreadSummary
	for _, c := range candidates {
		filtered = append(filtered, c.summary)
	}

	startIdx := 0
	if params.Cursor != nil && *params.Cursor != "" {
		if c, err := strconv.Atoi(*params.Cursor); err == nil && c >= 0 && c < len(filtered) {
			startIdx = c
		}
	}

	total := len(filtered)
	endIdx := total
	if params.Limit != nil && *params.Limit > 0 {
		endIdx = startIdx + *params.Limit
		if endIdx > total {
			endIdx = total
		}
	}

	var paged []wire.ThreadSummary
	if startIdx < total {
		paged = filtered[startIdx:endIdx]
	} else {
		paged = []wire.ThreadSummary{}
	}

	hasMore := endIdx < total
	var nextCursor *string
	if hasMore {
		nc := strconv.Itoa(endIdx)
		nextCursor = &nc
	}

	// C-ST-07：列表接口顺带淘汰冷归档会话的消息，避免全部历史长期占用内存。
	b.evictColdThreadsLocked()

	return wire.ThreadList{
		Threads:    paged,
		Revision:   1,
		HasMore:    hasMore,
		NextCursor: nextCursor,
	}, nil
}

// GetThread 获取单个会话的摘要信息。
func (b *MemoryBackend) GetThread(ctx context.Context, threadID string) (wire.ThreadSummary, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	td, ok := b.threads[threadID]
	if !ok {
		return wire.ThreadSummary{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		return wire.ThreadSummary{}, err
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)
	summary := b.summaryForPublishLocked(td)
	summary.ParentThreadID = optionalString(td.parentThreadID)
	summary.IsHelper = td.isHelper
	if b.swarmEnabledFor != nil {
		summary.SwarmEnabled = b.swarmEnabledFor(threadID)
	}
	return summary, nil
}

// ThreadUsage 查询指定会话每一轮的 token 用量以及全会话合计。
func (b *MemoryBackend) ThreadUsage(ctx context.Context, threadID string) (turns map[int]harness.Usage, total harness.Usage, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.threads[threadID]; !ok {
		return nil, harness.Usage{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}

	rawTurns := b.usage[threadID]
	turns = make(map[int]harness.Usage, len(rawTurns))
	for turnIdx, u := range rawTurns {
		turns[turnIdx] = u
	}
	return turns, b.usageTotalsLocked(threadID), nil
}

// usageTotalsLocked 汇总指定会话的累计用量；调用方需持有 b.mu。
// 会话用量面板与编排记录的 bee tokens 共用本函数，保证两处口径一致。
func (b *MemoryBackend) usageTotalsLocked(threadID string) harness.Usage {
	var total harness.Usage
	for _, u := range b.usage[threadID] {
		total.InputTokens += u.InputTokens
		total.OutputTokens += u.OutputTokens
		total.CachedTokens += u.CachedTokens
	}
	return total
}

// SessionUsage 实现 kanban.SessionUsageReader：返回子会话累计的输入+输出 token（与会话用量面板同口径）
// 以及工具调用次数（按 tool 消息统计，与 ThreadActivityFromMessages 一致）。
func (b *MemoryBackend) SessionUsage(ctx context.Context, threadID string) (tokens int, toolCalls int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	td, ok := b.threads[threadID]
	if !ok {
		return 0, 0, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}

	total := b.usageTotalsLocked(threadID)
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		return 0, 0, err
	}
	b.ensureMessagesLoadedLocked(td)
	for _, msg := range td.messages {
		if msg.Role == "tool" || (msg.Kind != nil && *msg.Kind == "tool") {
			toolCalls++
		}
	}
	return int(total.InputTokens + total.OutputTokens), toolCalls, nil
}

// ThreadHistory 返回指定会话的历史消息页面（支持反向翻页语义）。
func (b *MemoryBackend) ThreadHistory(ctx context.Context, params wire.HistoryParams) (wire.HistoryPage, error) {
	b.mu.Lock()
	page, err := b.threadHistoryLocked(params)
	b.mu.Unlock()
	if err == nil {
		b.publishCompactSuggestion(params.ThreadID)
	}
	return page, err
}

func (b *MemoryBackend) threadHistoryLocked(params wire.HistoryParams) (wire.HistoryPage, error) {
	td, ok := b.threads[params.ThreadID]
	if !ok {
		return wire.HistoryPage{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", params.ThreadID),
		}
	}
	if err := b.ensureMessagesLocked(td, params.ThreadID); err != nil {
		return wire.HistoryPage{}, err
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)

	allMsgs := td.messages
	total := len(allMsgs)

	limit := 50
	if params.Limit != nil && *params.Limit > 0 {
		limit = *params.Limit
	}

	endIdx := total
	if params.Cursor != nil && *params.Cursor != "" {
		if c, err := strconv.Atoi(*params.Cursor); err == nil && c >= 0 && c <= total {
			endIdx = c
		}
	}

	startIdx := endIdx - limit
	if startIdx < 0 {
		startIdx = 0
	}

	var pageMsgs []wire.ThreadMessage
	if startIdx < endIdx {
		pageMsgs = make([]wire.ThreadMessage, endIdx-startIdx)
		copy(pageMsgs, allMsgs[startIdx:endIdx])
	} else {
		pageMsgs = []wire.ThreadMessage{}
	}
	turnsByID := make(map[string]store.Turn)
	if b.store != nil && len(pageMsgs) > 0 {
		ctx, cancel := persistCtx()
		turns, err := b.store.ListTurns(ctx, params.ThreadID)
		cancel()
		if err != nil {
			log.Printf("store list turns %s error: %v", params.ThreadID, err)
		} else {
			for _, turn := range turns {
				turnsByID[turn.ID] = turn
			}
		}
	}
	for id, turn := range td.turnTiming {
		turnsByID[id] = turn
	}
	for i := range pageMsgs {
		applyTurnTiming(&pageMsgs[i], turnsByID)
	}

	hasMore := startIdx > 0
	var nextCursor *string
	if hasMore {
		nc := strconv.Itoa(startIdx)
		nextCursor = &nc
	}

	return wire.HistoryPage{
		ThreadID:   params.ThreadID,
		Messages:   pageMsgs,
		NextCursor: nextCursor,
		HasMore:    hasMore,
	}, nil
}

// Subscribe 登记会话订阅并返回包含当前水位的快照。
func (b *MemoryBackend) Subscribe(ctx context.Context, c Conn, threadID string) (wire.SubscribeResult, error) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return wire.SubscribeResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}
	summary := b.summaryForPublishLocked(td)
	history, _ := b.threadHistoryLocked(wire.HistoryParams{ThreadID: threadID})
	b.mu.Unlock()

	runID, seq := b.hub.Subscribe(c, threadID)
	if runID == "" {
		runID = b.hub.BeginRun(threadID)
	}
	return wire.SubscribeResult{
		ThreadID:   threadID,
		Subscribed: true,
		Snapshot: &wire.ThreadStreamSnapshot{
			ThreadID: threadID,
			History:  history,
			Thread:   summary,
			RunID:    runID,
			Sequence: seq,
		},
	}, nil
}

// Unsubscribe 取消连接对指定会话的订阅。
func (b *MemoryBackend) Unsubscribe(ctx context.Context, c Conn, threadID string) error {
	b.hub.Unsubscribe(c, threadID)
	return nil
}

// SetProvisionalCleanupDelay 设置临时线程清理延迟时间，供测试或配置使用。
func (b *MemoryBackend) SetProvisionalCleanupDelay(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.provisionalCleanupDelay = d
}

// CanDiscardEmptyProvisional 判断临时线程是否为空且可被自动废弃清理。
// 同时满足以下条件才为 true：是临时线程、没有任何消息、没有进行中的轮次（IsRunning）、Follow-Up 队列为空。
func (b *MemoryBackend) CanDiscardEmptyProvisional(threadID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	td, ok := b.threads[threadID]
	if !ok {
		return false
	}
	if !td.summary.IsProvisional {
		return false
	}
	// 消息被淘汰时按需回载，避免把有历史的会话误判为空。
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		return false
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)
	if len(td.messages) > 0 {
		return false
	}
	if td.summary.IsRunning {
		return false
	}
	if len(td.followUps) > 0 || td.pendingSend != nil {
		return false
	}
	return true
}

// RetainThread 将临时线程转为正式线程（IsProvisional=false，落盘，取消待执行的清理定时器，广播 threads.changed）；对非临时线程不做任何事。
func (b *MemoryBackend) RetainThread(ctx context.Context, threadID string) error {
	return b.retainThreadInternal(ctx, threadID)
}

func (b *MemoryBackend) retainThreadInternal(ctx context.Context, threadID string) error {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return store.ErrNotFound
	}
	if !td.summary.IsProvisional {
		b.mu.Unlock()
		return nil
	}
	td.summary.IsProvisional = false
	if timer, ok := b.provisionalTimers[threadID]; ok {
		timer.Stop()
		delete(b.provisionalTimers, threadID)
	}
	nowStr := b.nowLocked().Format(time.RFC3339Nano)
	td.summary.UpdatedAt = nowStr
	currentSummary := b.summaryForPublishLocked(td)
	hook := b.threadsChangedHook
	s := b.store
	dormantAt := td.dormantAt
	b.mu.Unlock()

	if s != nil {
		pCtx, pCancel := persistCtx()
		if err := s.UpdateThreadLifecycle(pCtx, threadID, currentSummary.IsDormant, dormantAt, currentSummary.IsProvisional, currentSummary.HasUnread); err != nil {
			log.Printf("store update thread lifecycle error (thread %s): %v", threadID, err)
		}
		pCancel()
	}

	payload := wire.ThreadsChanged{
		Threads: []wire.ThreadSummary{currentSummary},
	}
	b.hub.Publish(threadID, nil, nil, currentSummary)
	if hook != nil {
		hook(payload)
	}
	return nil
}

// ScheduleProvisionalCleanup 调度临时线程的延迟自动清理。
// 如果延迟后 CanDiscardEmptyProvisional 仍为 true，就调用 b.DeleteThread 静默删除。
func (b *MemoryBackend) ScheduleProvisionalCleanup(threadID string) {
	b.mu.Lock()
	if timer, ok := b.provisionalTimers[threadID]; ok {
		timer.Stop()
	}
	delay := b.provisionalCleanupDelay
	if delay <= 0 {
		delay = 2 * time.Second
	}
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		b.mu.Lock()
		if cur, ok := b.provisionalTimers[threadID]; ok && cur == timer {
			delete(b.provisionalTimers, threadID)
		}
		b.mu.Unlock()

		if b.CanDiscardEmptyProvisional(threadID) {
			_ = b.DeleteThread(context.Background(), threadID)
		}
	})
	b.provisionalTimers[threadID] = timer
	b.mu.Unlock()
}

func (b *MemoryBackend) reactivateThreadIfDormant(ctx context.Context, threadID string) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok || !td.summary.IsDormant {
		b.mu.Unlock()
		return
	}
	td.summary.IsDormant = false
	td.dormantAt = nil
	nowStr := b.nowLocked().Format(time.RFC3339Nano)
	td.summary.UpdatedAt = nowStr
	currentSummary := b.summaryForPublishLocked(td)
	hook := b.threadsChangedHook
	s := b.store
	b.mu.Unlock()

	if s != nil {
		pCtx, pCancel := persistCtx()
		if err := s.UpdateThreadLifecycle(pCtx, threadID, currentSummary.IsDormant, nil, currentSummary.IsProvisional, currentSummary.HasUnread); err != nil {
			log.Printf("store update thread lifecycle error (thread %s): %v", threadID, err)
		}
		pCancel()
	}

	payload := wire.ThreadsChanged{
		Threads: []wire.ThreadSummary{currentSummary},
	}
	b.hub.Publish(threadID, nil, nil, currentSummary)
	if hook != nil {
		hook(payload)
	}
}

// familyThreadIDsLocked 返回以 rootID 为根的线程家族（包括其直接与递归子线程、helper 会话），根节点排在首位。
// 调用方必须持有 b.mu。
func (b *MemoryBackend) familyThreadIDsLocked(rootID string) []string {
	if _, ok := b.threads[rootID]; !ok {
		return nil
	}
	result := []string{rootID}
	visited := map[string]bool{rootID: true}
	queue := []string{rootID}

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		for tid, td := range b.threads {
			if visited[tid] {
				continue
			}
			if td.parentThreadID == curr {
				visited[tid] = true
				result = append(result, tid)
				queue = append(queue, tid)
			}
		}
	}
	return result
}

// ThreadDormantAt 返回指定线程的休眠时间与是否存在标记。
func (b *MemoryBackend) ThreadDormantAt(threadID string) (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	td, ok := b.threads[threadID]
	if !ok || td.dormantAt == nil {
		return time.Time{}, false
	}
	return *td.dormantAt, true
}

// SetThreadDormantAt 直接设置指定会话的休眠时间（直接写内存和 store，不触发级联），给批量休眠和撤销使用。
func (b *MemoryBackend) SetThreadDormantAt(ctx context.Context, threadID string, t time.Time) error {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return store.ErrNotFound
	}
	if t.IsZero() {
		td.dormantAt = nil
		td.summary.IsDormant = false
	} else {
		dormantCopy := t
		td.dormantAt = &dormantCopy
		td.summary.IsDormant = true
	}
	nowStr := b.nowLocked().Format(time.RFC3339Nano)
	td.summary.UpdatedAt = nowStr
	currentSummary := b.summaryForPublishLocked(td)
	hook := b.threadsChangedHook
	s := b.store
	dormantAt := td.dormantAt
	b.mu.Unlock()

	if s != nil {
		pCtx, pCancel := persistCtx()
		if err := s.UpdateThreadLifecycle(pCtx, threadID, currentSummary.IsDormant, dormantAt, currentSummary.IsProvisional, currentSummary.HasUnread); err != nil {
			log.Printf("store update thread lifecycle error (thread %s): %v", threadID, err)
		}
		pCancel()
	}

	payload := wire.ThreadsChanged{
		Threads: []wire.ThreadSummary{currentSummary},
	}
	b.hub.Publish(threadID, nil, nil, currentSummary)
	if hook != nil {
		hook(payload)
	}
	return nil
}

// SetThreadRelation 设置会话的父会话 ID 与 helper 标记，供测试与子会话关系建立使用。
func (b *MemoryBackend) SetThreadRelation(threadID, parentThreadID string, isHelper bool) {
	b.mu.Lock()
	var currentSummary wire.ThreadSummary
	var found bool
	if td, ok := b.threads[threadID]; ok {
		td.parentThreadID = parentThreadID
		td.isHelper = isHelper
		td.summary.ParentThreadID = optionalString(parentThreadID)
		td.summary.IsHelper = isHelper
		currentSummary = b.summaryForPublishLocked(td)
		found = true
	}
	// 父会话只发布父会话自己的摘要：直接复用子会话摘要会让 UI 按事件的 ThreadID
	// 用子会话的 IsRunning 覆盖父会话卡片，导致父会话永久卡在运行中。
	var parentSummary wire.ThreadSummary
	var parentFound bool
	if parentThreadID != "" {
		if ptd, ok := b.threads[parentThreadID]; ok {
			parentSummary = b.summaryForPublishLocked(ptd)
			parentFound = true
		}
	}
	hook := b.threadsChangedHook
	s := b.store
	b.mu.Unlock()

	if found {
		b.hub.Publish(threadID, nil, nil, currentSummary)
		if parentFound {
			b.hub.Publish(parentThreadID, nil, nil, parentSummary)
		}
		if hook != nil {
			hook(wire.ThreadsChanged{
				Threads: []wire.ThreadSummary{currentSummary},
			})
		}
	}

	if s != nil {
		pCtx, pCancel := persistCtx()
		_ = s.UpdateThreadRelation(pCtx, threadID, parentThreadID, isHelper)
		pCancel()
	}
}

// SendMessage 接收用户消息并启动后台模拟回复。
func (b *MemoryBackend) SendMessage(ctx context.Context, params wire.SendMessageParams) (wire.SendMessageResult, error) {
	b.mu.Lock()
	desk := b.stewardDesk
	b.mu.Unlock()
	if desk != nil && desk.IsFeed(params.ThreadID) {
		return b.acceptStewardMessage(ctx, params, desk)
	}
	b.cancelAutoContinue(params.ThreadID)
	// C-TH-02/03: 临时线程发消息时转为正式线程
	_ = b.retainThreadInternal(ctx, params.ThreadID)
	// C-TH-06: 线程已休眠时自动取消休眠
	b.reactivateThreadIfDormant(ctx, params.ThreadID)
	trimmed := strings.TrimSpace(params.Text)

	// /compact 保持原样，交给 Provider 自身处理上下文压缩
	if trimmed == "/compact" {
		return b.sendMessage(ctx, params, false)
	}

	if trimmed == "/plan" {
		if params.ActionID != "" && b.isTombstoned(params.ActionID) {
			return wire.SendMessageResult{}, &wire.RPCError{
				Code:    wire.ErrCodeOperationRejected,
				Message: "动作已被客户端结算取消，未曾执行",
			}
		}

		b.mu.Lock()
		td, ok := b.threads[params.ThreadID]
		if !ok {
			b.mu.Unlock()
			rpcErr := &wire.RPCError{
				Code:    wire.ErrCodeResourceNotFound,
				Message: fmt.Sprintf("thread %s not found", params.ThreadID),
			}
			if params.ActionID != "" {
				b.actions.record(wire.ActionStatusResult{
					ActionID: params.ActionID,
					State:    wire.ActionStateFailed,
					ThreadID: &params.ThreadID,
					Error: &wire.ActionError{
						Code:    rpcErr.Code,
						Message: rpcErr.Message,
					},
				})
			}
			return wire.SendMessageResult{}, rpcErr
		}

		currMode := normalizeInteractionMode(td.summary.InteractionMode)
		newMode := "plan"
		if currMode == "plan" {
			newMode = "build"
		}

		// 复用已有模式更新与持久化路径
		if b.store != nil {
			pCtx, pCancel := persistCtx()
			effort := ""
			if td.summary.Effort != nil {
				effort = *td.summary.Effort
			}
			beesProvider := ""
			if td.summary.SwarmBeesProvider != nil {
				beesProvider = *td.summary.SwarmBeesProvider
			}
			beesModel := ""
			if td.summary.SwarmBeesModel != nil {
				beesModel = *td.summary.SwarmBeesModel
			}
			beesEffort := ""
			if td.summary.SwarmBeesEffort != nil {
				beesEffort = *td.summary.SwarmBeesEffort
			}
			if err := b.store.UpdateThreadComposerPrefs(pCtx, params.ThreadID, newMode, effort, beesProvider, beesModel, beesEffort, td.summary.Persona); err != nil {
				pCancel()
				b.mu.Unlock()
				return wire.SendMessageResult{}, err
			}
			pCancel()
		}

		td.summary.InteractionMode = newMode
		now := time.Now().UTC().Format(time.RFC3339Nano)
		td.summary.UpdatedAt = now
		currentSummary := b.summaryForPublishLocked(td)
		b.mu.Unlock()

		b.hub.Publish(params.ThreadID, nil, nil, currentSummary)

		result := wire.SendMessageResult{
			ActionID: params.ActionID,
			ThreadID: params.ThreadID,
		}
		if params.ActionID != "" {
			b.actions.record(wire.ActionStatusResult{
				ActionID: params.ActionID,
				State:    wire.ActionStateAccepted,
				ThreadID: &params.ThreadID,
			})
		}
		return result, nil
	}

	if strings.HasPrefix(trimmed, "/plan ") {
		planMode := "plan"
		if params.Options == nil {
			params.Options = &wire.ComposerOptions{
				InteractionMode: &planMode,
			}
		} else {
			params.Options.InteractionMode = &planMode
		}

		b.mu.Lock()
		if td, ok := b.threads[params.ThreadID]; ok {
			td.summary.InteractionMode = "plan"
			if b.store != nil {
				pCtx, pCancel := persistCtx()
				effort := ""
				if td.summary.Effort != nil {
					effort = *td.summary.Effort
				}
				beesProvider := ""
				if td.summary.SwarmBeesProvider != nil {
					beesProvider = *td.summary.SwarmBeesProvider
				}
				beesModel := ""
				if td.summary.SwarmBeesModel != nil {
					beesModel = *td.summary.SwarmBeesModel
				}
				beesEffort := ""
				if td.summary.SwarmBeesEffort != nil {
					beesEffort = *td.summary.SwarmBeesEffort
				}
				_ = b.store.UpdateThreadComposerPrefs(pCtx, params.ThreadID, "plan", effort, beesProvider, beesModel, beesEffort, td.summary.Persona)
				pCancel()
			}
			td.summary.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			currentSummary := b.summaryForPublishLocked(td)
			b.hub.Publish(params.ThreadID, nil, nil, currentSummary)
		}
		b.mu.Unlock()

		params.Text = strings.TrimSpace(trimmed[len("/plan "):])
	}

	return b.sendMessage(ctx, params, false)
}

// SendSwarmReport 在主会话忙碌时将汇报放在普通跟进之前。
func (b *MemoryBackend) SendSwarmReport(ctx context.Context, threadID, text string) error {
	_, err := b.sendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID, Text: text, WhenRunning: "queue",
	}, true)
	return err
}

// SendHostEvent 向总管会话投递一条宿主事件：行为与 SendSwarmReport 一致（忙碌时排队，
// 随后开新一轮模型回合，提示词装饰器照常生效），只把落库并广播的那条用户消息 Kind 换成
// host_event，让客户端能把它当成事件通知而不是用户输入。
func (b *MemoryBackend) SendHostEvent(ctx context.Context, threadID, text string) error {
	_, err := b.sendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID, Text: text, WhenRunning: "queue",
	}, true, threadMessageKindHostEvent)
	return err
}

func (b *MemoryBackend) sendMessage(ctx context.Context, params wire.SendMessageParams, priority bool, userKind ...string) (wire.SendMessageResult, error) {
	// userKind 指定落库的用户消息 Kind（如 host_event）；为空表示普通用户消息。
	kind := ""
	if len(userKind) > 0 {
		kind = strings.TrimSpace(userKind[0])
	}
	if params.ActionID != "" && b.isTombstoned(params.ActionID) {
		return wire.SendMessageResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: "动作已被客户端结算取消，未曾执行",
		}
	}

	if params.WhenRunning != "" && params.WhenRunning != "queue" {
		rpcErr := &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: fmt.Sprintf("invalid whenRunning %q", params.WhenRunning),
		}
		if params.ActionID != "" {
			b.actions.record(wire.ActionStatusResult{
				ActionID: params.ActionID,
				State:    wire.ActionStateFailed,
				ThreadID: &params.ThreadID,
				Error: &wire.ActionError{
					Code:    rpcErr.Code,
					Message: rpcErr.Message,
				},
			})
		}
		return wire.SendMessageResult{}, rpcErr
	}

	if params.ActionID != "" {
		b.actions.record(wire.ActionStatusResult{
			ActionID: params.ActionID,
			State:    wire.ActionStatePending,
			ThreadID: &params.ThreadID,
		})
	}

	hasBees := b.hasWorkingSwarmBees(params.ThreadID)
	if params.WhenRunning == "" && len(params.Attachments) == 0 {
		b.mu.Lock()
		sendToBee := b.swarmSendToBee
		thread, exists := b.threads[params.ThreadID]
		mode := "build"
		if exists {
			mode = normalizeInteractionMode(thread.summary.InteractionMode)
		}
		if params.Options != nil && params.Options.InteractionMode != nil {
			mode = *params.Options.InteractionMode
		}
		b.mu.Unlock()
		// priority（swarm 汇报）只回投主代理，绝不转发给子代理。
		if sendToBee != nil && exists && mode != "plan" && !priority {
			beeID, err := sendToBee(ctx, params.ThreadID, params.Text)
			if err != nil {
				if params.ActionID != "" {
					b.actions.record(wire.ActionStatusResult{
						ActionID: params.ActionID, ThreadID: &params.ThreadID, State: wire.ActionStateFailed,
						Error: &wire.ActionError{Code: wire.ErrCodeOperationRejected, Message: err.Error()},
					})
				}
				return wire.SendMessageResult{}, err
			}
			if beeID != "" {
				if params.ActionID != "" {
					b.actions.record(wire.ActionStatusResult{
						ActionID: params.ActionID, ThreadID: &params.ThreadID, State: wire.ActionStateAccepted,
					})
				}
				return wire.SendMessageResult{
					ActionID: params.ActionID, ThreadID: params.ThreadID, DelegatedThreadID: &beeID,
				}, nil
			}
		}
	}
	general := b.generalSettings()
	queueWhileRunning := general.Session.QueueWhileRunning
	insertIntoRunningTurn := general.Session.InsertIntoRunningTurn
	b.mu.Lock()
	td, ok := b.threads[params.ThreadID]
	if !ok {
		b.mu.Unlock()
		rpcErr := &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", params.ThreadID),
		}
		if params.ActionID != "" {
			b.actions.record(wire.ActionStatusResult{
				ActionID: params.ActionID,
				State:    wire.ActionStateFailed,
				ThreadID: &params.ThreadID,
				Error: &wire.ActionError{
					Code:    rpcErr.Code,
					Message: rpcErr.Message,
				},
			})
		}
		return wire.SendMessageResult{}, rpcErr
	}

	// C-ST-07：消息被淘汰的归档会话在发送前先回载历史，避免上下文丢失。
	if err := b.ensureMessagesLocked(td, params.ThreadID); err != nil {
		b.mu.Unlock()
		if params.ActionID != "" {
			b.actions.record(wire.ActionStatusResult{
				ActionID: params.ActionID,
				State:    wire.ActionStateFailed,
				ThreadID: &params.ThreadID,
				Error:    &wire.ActionError{Code: wire.InternalError, Message: err.Error()},
			})
		}
		return wire.SendMessageResult{}, err
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)

	if td.takingBack {
		b.mu.Unlock()
		return wire.SendMessageResult{}, &wire.RPCError{Code: wire.ErrCodeOperationRejected, Message: "turn is being taken back"}
	}
	isRunning := td.summary.IsRunning

	// followUpIntent 记录用户本意是插入当前回合、但服务端无法注入而降级成排队的情况；
	// 普通排队保持空串。
	followUpIntent := ""

	// 运行中的普通发送：开启「插入当前回合」时优先把消息注入正在执行的轮次
	// （harness.Steerer，Provider 相关），无法注入时退回排队而不是直接拒绝。
	if isRunning && params.WhenRunning == "" && !priority {
		if td.phase != phaseRunning {
			// 轮次仍在启动阶段，服务商还没进入可注入状态：开启插入时退回排队。
			if insertIntoRunningTurn {
				params.WhenRunning = "queue"
				followUpIntent = "insert"
			}
		} else if steerer, ok := b.threadHarnessLocked(td.summary.Provider).(harness.Steerer); ok && td.sessionID != "" && len(params.Attachments) == 0 && params.Options == nil {
			turnID, sessionID := td.runTurnID, td.sessionID
			b.mu.Unlock()
			err := steerer.Steer(ctx, sessionID, params.Text)
			b.mu.Lock()
			if err == nil {
				if b.threads[params.ThreadID] != td || td.runTurnID != turnID {
					b.mu.Unlock()
					return wire.SendMessageResult{}, &wire.RPCError{Code: wire.ErrCodeOperationRejected, Message: "turn ended while steering"}
				}
				index := td.messages[len(td.messages)-1].TurnIndex
				msg := wire.ThreadMessage{ID: newUUID(), Role: "user", Text: params.Text, TurnID: &turnID, TurnIndex: index, CreatedAt: b.nowLocked().Format(time.RFC3339Nano)}
				applyTurnTiming(&msg, td.turnTiming)
				td.messages = append(td.messages, msg)
				// 转向的消息同样是用户发来的，刷新最后用户消息时间，与 startTurnLocked 保持一致。
				td.summary.LastUserMessageAt = &msg.CreatedAt
				summary := b.summaryForPublishLocked(td)
				b.mu.Unlock()
				b.persistMessages(params.ThreadID, []wire.ThreadMessage{msg})
				b.hub.Publish(params.ThreadID, []wire.ThreadMessage{msg}, nil, summary)
				if params.ActionID != "" {
					b.actions.record(wire.ActionStatusResult{ActionID: params.ActionID, State: wire.ActionStateAccepted, ThreadID: &params.ThreadID, TurnID: &turnID, MessageID: &msg.ID})
				}
				return wire.SendMessageResult{ActionID: params.ActionID, ThreadID: params.ThreadID, TurnID: &turnID, MessageID: &msg.ID}, nil
			}
			if !errors.Is(err, harness.ErrNotRunning) {
				b.mu.Unlock()
				return wire.SendMessageResult{}, err
			}
			if b.threads[params.ThreadID] != td {
				b.mu.Unlock()
				return wire.SendMessageResult{}, store.ErrNotFound
			}
			isRunning = td.summary.IsRunning
			params.WhenRunning = "queue"
			followUpIntent = "insert"
		} else if insertIntoRunningTurn {
			// 当前 Provider 不实现 harness.Steerer（或消息带附件/选项）时无法注入，退回排队。
			params.WhenRunning = "queue"
			followUpIntent = "insert"
		}
	}

	// 子代理汇报必须等整批落定：还有 bee 在跑时先把汇报排进队列，等全部结束时由统一的
	// 汇报开轮，避免单条汇报抢跑。priority 里的提问转交与通知不受此限：子代理提问要能当即
	// 送到主代理，否则子代理等回答、主代理等子代理收尾，会形成死锁。
	batchedReport := priority && swarm.IsReport(params.Text)
	if isRunning || (params.WhenRunning == "queue" && hasBees && (batchedReport || !priority)) {
		if params.WhenRunning == "queue" || (isRunning && queueWhileRunning) {
			queueAtts, err := b.attachments.pinAttachments(params.Attachments)
			if err != nil {
				b.mu.Unlock()
				if params.ActionID != "" {
					rpcErr := err.(*wire.RPCError)
					b.actions.record(wire.ActionStatusResult{
						ActionID: params.ActionID, State: wire.ActionStateFailed, ThreadID: &params.ThreadID,
						Error: &wire.ActionError{Code: rpcErr.Code, Message: rpcErr.Message},
					})
				}
				return wire.SendMessageResult{}, err
			}

			fuID := newUUID()
			nowStr := time.Now().UTC().Format(time.RFC3339Nano)
			fu := wire.FollowUp{
				ID:          fuID,
				Text:        params.Text,
				Attachments: queueAtts,
				CreatedAt:   nowStr,
				BundleID:    nil,
				Kind:        kind,
				Intent:      followUpIntent,
			}
			if priority {
				if td.priorityFollowUps == nil {
					td.priorityFollowUps = make(map[string]bool)
				}
				td.priorityFollowUps[fuID] = true
				td.followUps = append([]wire.FollowUp{fu}, td.followUps...)
			} else {
				td.followUps = append(td.followUps, fu)
			}
			if td.queueRevision == 0 {
				td.queueRevision = 1
			}
			td.queueRevision++
			rev := td.queueRevision
			b.mu.Unlock()

			b.commitQueueChange(params.ThreadID)

			if params.ActionID != "" {
				b.actions.record(wire.ActionStatusResult{
					ActionID:   params.ActionID,
					State:      wire.ActionStateAccepted,
					ThreadID:   &params.ThreadID,
					FollowUpID: &fuID,
					Revision:   &rev,
				})
			}

			queuedTrue := true
			return wire.SendMessageResult{
				ActionID:   params.ActionID,
				ThreadID:   params.ThreadID,
				Queued:     &queuedTrue,
				FollowUpID: &fuID,
			}, nil
		}

		b.mu.Unlock()
		rpcErr := &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: fmt.Sprintf("thread %s is already running", params.ThreadID),
		}
		if params.ActionID != "" {
			b.actions.record(wire.ActionStatusResult{
				ActionID: params.ActionID,
				State:    wire.ActionStateFailed,
				ThreadID: &params.ThreadID,
				Error: &wire.ActionError{
					Code:    rpcErr.Code,
					Message: rpcErr.Message,
				},
			})
		}
		return wire.SendMessageResult{}, rpcErr
	}

	// 每次 send 时顺带清理超过 1 小时仍未被引用的附件
	if b.attachments != nil {
		b.attachments.cleanupExpired()
	}

	// 非排队路径：在发起 turn 之前逐个解析 attachmentID 并移动到会话媒体目录
	mediaRefs, mediaFilePaths, err := b.consumeAttachmentsLocked(params.ThreadID, params.Attachments, false)
	if err != nil {
		b.mu.Unlock()
		var rpcErr *wire.RPCError
		if !errors.As(err, &rpcErr) {
			rpcErr = &wire.RPCError{
				Code:    wire.InternalError,
				Message: err.Error(),
			}
		}
		if params.ActionID != "" {
			b.actions.record(wire.ActionStatusResult{
				ActionID: params.ActionID,
				State:    wire.ActionStateFailed,
				ThreadID: &params.ThreadID,
				Error: &wire.ActionError{
					Code:    rpcErr.Code,
					Message: rpcErr.Message,
				},
			})
		}
		return wire.SendMessageResult{}, rpcErr
	}

	st := b.startTurnLocked(td, params.Text, kind)
	if len(mediaRefs) > 0 {
		st.userMsg.Media = mediaRefs
		if len(td.messages) > 0 && td.messages[len(td.messages)-1].ID == st.userMsg.ID {
			td.messages[len(td.messages)-1].Media = mediaRefs
		}
		st.imagePaths = mediaFilePaths
		st.imageNames = mediaRefNames(mediaRefs)
	}
	// 本次消息携带的 composer 选项只作用于本轮，不改写会话偏好。
	if opts := params.Options; opts != nil {
		if opts.InteractionMode != nil && *opts.InteractionMode != "" {
			st.interactionMode = *opts.InteractionMode
		}
		if opts.Effort != nil {
			st.effort = *opts.Effort
		}
	}
	b.mu.Unlock()

	b.publishStartedTurn(params.ThreadID, st)

	result := wire.SendMessageResult{
		ActionID:  params.ActionID,
		ThreadID:  params.ThreadID,
		MessageID: &st.userMsg.ID,
		TurnID:    &st.turnID,
	}
	if params.ActionID != "" {
		b.actions.record(wire.ActionStatusResult{
			ActionID:  params.ActionID,
			State:     wire.ActionStateAccepted,
			ThreadID:  &params.ThreadID,
			MessageID: &st.userMsg.ID,
			TurnID:    result.TurnID,
		})
	}

	return result, nil
}

// publishStartedTurn 落库并广播刚开启的一轮，再启动它的后台运行。普通发送、排队出队与接住
// 自发输出的轮次都走这里，保证开轮与运行之间的步骤顺序一致。
func (b *MemoryBackend) publishStartedTurn(threadID string, st *startedTurn) {
	b.persistMessages(threadID, []wire.ThreadMessage{st.userMsg})
	if st.titleChanged {
		b.persistThreadTitle(threadID, st.currentSummary.Title)
	}

	b.hub.Flush(threadID)
	b.hub.BeginRun(threadID)
	b.hub.Publish(threadID, []wire.ThreadMessage{st.userMsg}, nil, st.currentSummary)

	// 发启动运行前先记下这是要接自发输出的轮次：runHarness 据此把 Turn.Attach 置真。
	if st.attach {
		b.mu.Lock()
		if cur, ok := b.threads[threadID]; ok && cur.runTurnID == st.turnID {
			cur.attachTurnID = st.turnID
		}
		b.mu.Unlock()
	}
	b.launchTurnRun(threadID, st, st.userMsg.Text)
}

// handleUnsolicitedTurn 接住常驻进程自发跑出的一段输出：线程空闲时开一轮用 Turn.Attach 把它读回来，
// 输出照常落进线程消息。线程忙时记下通知，空闲后再尝试接回输出。
func (b *MemoryBackend) handleUnsolicitedTurn(threadID string) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok || td.isHelper || td.pendingSend != nil {
		b.mu.Unlock()
		return
	}
	if td.runTurnID != "" || td.summary.IsRunning || td.takingBack {
		td.unsolicitedPending = true
		b.mu.Unlock()
		return
	}
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		log.Printf("ensure messages for thread %s error: %v", threadID, err)
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)
	st := b.startTurnLocked(td, unsolicitedTurnText, threadMessageKindHostEvent)
	st.attach = true
	b.mu.Unlock()

	b.publishStartedTurn(threadID, st)
}

type unsolicitedOutputChecker interface {
	HasUnsolicitedOutput(threadID string) bool
}

func (b *MemoryBackend) retryUnsolicitedTurn(threadID string) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return
	}
	h := b.threadHarnessLocked(td.summary.Provider)
	b.mu.Unlock()
	if checker, ok := h.(unsolicitedOutputChecker); ok && !checker.HasUnsolicitedOutput(threadID) {
		return
	}
	b.handleUnsolicitedTurn(threadID)
}

// takeUnsolicitedPendingLocked 在真正空闲时取走待重试通知。
func (td *memoryThread) takeUnsolicitedPendingLocked() bool {
	if td.takingBack || td.runTurnID != "" || td.summary.IsRunning || !td.unsolicitedPending {
		return false
	}
	td.unsolicitedPending = false
	return true
}

// bindUnsolicitedTurnHandlerLocked 把自发轮次回调登记到支持它的 harness 上（调用方持有 b.mu）。
// 常驻进程空闲时自己跑出新一轮时，harness 通过它请后端开一轮把这段输出接住。
func (b *MemoryBackend) bindUnsolicitedTurnHandlerLocked(h harness.Harness) {
	if h == nil {
		return
	}
	if notifier, ok := h.(harness.UnsolicitedTurnNotifier); ok {
		notifier.SetUnsolicitedTurnHandler(b.handleUnsolicitedTurn)
	}
	if notifier, ok := h.(harness.ProcessExitNotifier); ok {
		notifier.SetProcessExitHandler(func(threadID string, before time.Time) {
			go b.finalizeThreadBackgroundTasks(threadID, before)
		})
	}
}

// finalizeThreadBackgroundTasks 在常驻进程退出后收敛该会话未收尾的后台任务。
// 工具消息状态收敛为 failed，后台任务计数归零，并广播给前端与落库。
func (b *MemoryBackend) finalizeThreadBackgroundTasks(threadID string, before time.Time) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return
	}

	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		b.mu.Unlock()
		return
	}
	b.ensureMessagesLoadedLocked(td)

	nowTime := b.nowLocked()
	nowStr := formatTime(nowTime)

	var msgToPublish []wire.ThreadMessage
	for idx := range td.messages {
		if td.messages[idx].ToolStatus == nil || *td.messages[idx].ToolStatus != "background" {
			continue
		}
		if !before.IsZero() {
			createdAt, err := time.Parse(time.RFC3339Nano, td.messages[idx].CreatedAt)
			if err != nil || !createdAt.Before(before) {
				continue
			}
		}
		failed := "failed"
		stamp := nowStr
		td.messages[idx].ToolStatus = &failed
		td.messages[idx].ToolCompletedAt = &stamp
		td.messages[idx].Text = appendToolNoticeText(td.messages[idx].Text, residentExitToolNotice)
		msgToPublish = append(msgToPublish, td.messages[idx])
	}
	b.enrichTurnMessagesLocked(td, msgToPublish)

	if len(msgToPublish) == 0 {
		b.mu.Unlock()
		return
	}

	changedIDs := make(map[string]bool, len(msgToPublish))
	for _, m := range msgToPublish {
		changedIDs[m.ID] = true
	}
	for toolID, msgID := range td.backgroundTools {
		if changedIDs[msgID] {
			delete(td.backgroundTools, toolID)
			delete(td.backgroundPending, toolID)
			for taskID, taskToolID := range td.backgroundTaskTools {
				if taskToolID == toolID {
					delete(td.backgroundTaskTools, taskID)
				}
			}
		}
	}
	td.bgCount.invalidate()
	td.summary.UpdatedAt = nowStr
	currentSummary := b.summaryForPublishLocked(td)
	b.mu.Unlock()

	b.persistMessages(threadID, msgToPublish)

	if len(msgToPublish) > 0 {
		b.hub.Publish(threadID, msgToPublish, nil, currentSummary)
	}
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

// startTurnLocked 必须在持有 b.mu 时调用：追加用户消息、更新会话摘要，并为本轮创建运行上下文。
// SendMessage 的正常路径与排队消息出队路径共用该函数，保证出队时不会出现会话短暂空闲的窗口。
// kind 可选：指定本轮用户消息的 Kind（如 host_event），为空时按普通用户消息落库。
func (b *MemoryBackend) startTurnLocked(td *memoryThread, text string, kind ...string) *startedTurn {
	var kindPtr *string
	if len(kind) > 0 && kind[0] != "" {
		k := kind[0]
		kindPtr = &k
	}
	b.ensureMessagesLoadedLocked(td)
	userMsgID := newUUID()
	startedAt := b.nowLocked().UTC()
	now := startedAt.Format(time.RFC3339Nano)

	// 统计 td.messages 中已有消息出现过的不同有效 TurnID 个数作为历史轮次数。
	// 相比仅统计 Role 为 "user" 的消息数，统计不同 TurnID 的个数更稳妥：
	// 每一轮对话（包含用户提问、思考过程、工具调用、助手答复及错误）均绑定同一个唯一的 TurnID。
	// 若存在无 user 角色的特殊轮次或消息过滤，不同 TurnID 集合依然能真实反映轮次推进；
	// 若遇到无 TurnID 的历史消息，兜底按 Role == "user" 累加，保证兼容性。
	// 起始值取 0（轮次编号从 0 开始递增）。
	seenTurns := make(map[string]struct{})
	userMsgCountWithoutTurnID := 0
	for _, m := range td.messages {
		if m.TurnID != nil && *m.TurnID != "" {
			seenTurns[*m.TurnID] = struct{}{}
		} else if m.Role == "user" {
			userMsgCountWithoutTurnID++
		}
	}
	startTurnIndex := 0
	turnIndex := startTurnIndex + len(seenTurns) + userMsgCountWithoutTurnID
	if turnIndex < td.nextTurnIndex {
		turnIndex = td.nextTurnIndex
	}
	if b.store != nil {
		ctx, cancel := persistCtx()
		if turns, err := b.store.ListRecentTurns(ctx, td.summary.ID, 1); err == nil && len(turns) > 0 && turnIndex <= turns[0].TurnIndex {
			turnIndex = turns[0].TurnIndex + 1
		}
		cancel()
	}
	td.nextTurnIndex = turnIndex + 1

	turnID := newUUID()
	turn := store.Turn{ID: turnID, ThreadID: td.summary.ID, TurnIndex: turnIndex, Status: "running", StartedAt: startedAt, UserPrompt: text}
	if td.turnTiming == nil {
		td.turnTiming = make(map[string]store.Turn)
	}
	td.turnTiming[turnID] = turn
	runCtx, runCancel := context.WithCancel(b.ctx)
	td.runTurnID = turnID
	td.runCancel = runCancel
	// 新一轮开始：清空上一轮记录的文件触碰路径。
	td.touchedPaths = nil
	// 新轮次开始也算一次落地时刻，避免上一轮的文件改动时间被长期沿用。
	td.lastFileChangeAt = startedAt
	// 记录本轮开轮输入的 Kind 与文本，供「本轮是否由用户亲自发消息触发」的判断使用。
	if kindPtr != nil {
		td.turnInputKind = *kindPtr
	} else {
		td.turnInputKind = ""
	}
	td.turnInputText = text

	userMsg := wire.ThreadMessage{
		ID:        userMsgID,
		Role:      "user",
		Text:      text,
		CreatedAt: now,
		TurnIndex: turnIndex,
		TurnID:    &turnID,
		Kind:      kindPtr,
	}
	applyTurnTiming(&userMsg, td.turnTiming)
	td.messages = append(td.messages, userMsg)

	var titleChanged bool
	// 宿主事件与唤醒触发不是用户输入，不能用它们的正文给会话起名。
	if kindPtr == nil && td.summary.Title == defaultThreadTitle {
		newTitle := generateThreadTitle(text)
		if newTitle != defaultThreadTitle {
			td.summary.Title = newTitle
			titleChanged = true
		}
	}

	td.summary.IsRunning = true
	td.phase = phaseStarting
	// 新的一轮开始：清掉上一轮的出错标记，避免结束后仍被当作失败。
	if td.summary.Attention == "failed" {
		td.summary.Attention = "running"
	}
	// 新一轮开始也清掉上一轮的错误原文，避免旧的额度提示重复触发。
	td.summary.LastErrorText = ""
	td.summary.UpdatedAt = now
	td.summary.LastUserMessageAt = &now
	actText := "Thinking..."
	td.summary.LastActivityText = &actText

	// 注释：params.Options.Provider 若与会话的 Provider 不同则直接忽略，因为 SessionID 只在同一个 provider 内有效。
	h := b.threadHarnessLocked(td.summary.Provider)
	if b.store != nil {
		ctx, cancel := persistCtx()
		err := b.store.CreateTurn(ctx, turn)
		cancel()
		if err != nil {
			log.Printf("store create turn %s error: %v", turnID, err)
		}
	}

	return &startedTurn{
		userMsg:         userMsg,
		turnID:          turnID,
		runCtx:          runCtx,
		turnIndex:       turnIndex,
		currentSummary:  td.summary,
		titleChanged:    titleChanged,
		h:               h,
		interactionMode: normalizeInteractionMode(td.summary.InteractionMode),
		effort:          derefString(td.summary.Effort),
	}
}

// launchTurnRun 为刚开始的执行轮启动后台 harness/模拟回复 goroutine。
func (b *MemoryBackend) launchTurnRun(threadID string, st *startedTurn, text string) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()

		// 步骤 2: C-RT-01 历史等待锁
		b.mu.Lock()
		td, ok := b.threads[threadID]
		if !ok {
			b.mu.Unlock()
			b.finishTurnEarly(nil, threadID, st.turnID, st.turnIndex, false, st.runCtx.Err() != nil)
			return
		}
		historyOp := td.historyOp
		b.mu.Unlock()

		if historyOp != nil {
			select {
			case <-historyOp:
			case <-st.runCtx.Done():
				b.finishTurnEarly(td, threadID, st.turnID, st.turnIndex, false, true)
				return
			}
		}

		// 步骤 1: 真正调用 runHarness 或 simulateAssistantReply 之前置为 phaseRunning（持锁设置，并确认 runTurnID 仍是本轮）
		b.mu.Lock()
		td, ok = b.threads[threadID]
		if !ok || td.runTurnID != st.turnID || st.runCtx.Err() != nil {
			b.mu.Unlock()
			// 本轮已被取消/被接替：startTurnLocked 已置 IsRunning，这里必须补上空闲收尾，
			// 否则该线程会永久卡在运行中。
			b.finishTurnEarly(td, threadID, st.turnID, st.turnIndex, false, st.runCtx.Err() != nil)
			return
		}
		td.phase = phaseRunning
		b.mu.Unlock()

		if st.h != nil {
			b.runHarness(st.runCtx, threadID, st.turnID, text, st.turnIndex, st.imagePaths, st.imageNames, st.interactionMode, st.effort)
		} else {
			b.simulateAssistantReply(st.runCtx, threadID, st.turnID, text, st.turnIndex)
		}
	}()
}

func (b *MemoryBackend) enrichTurnMessagesLocked(td *memoryThread, messages []wire.ThreadMessage) {
	missing := false
	for _, msg := range messages {
		if msg.TurnID != nil && *msg.TurnID != "" {
			if _, ok := td.turnTiming[*msg.TurnID]; !ok {
				missing = true
				break
			}
		}
	}
	if missing && b.store != nil {
		ctx, cancel := persistCtx()
		turns, err := b.store.ListTurns(ctx, td.summary.ID)
		cancel()
		if err != nil {
			log.Printf("store list turns %s error: %v", td.summary.ID, err)
		} else {
			if td.turnTiming == nil {
				td.turnTiming = make(map[string]store.Turn)
			}
			for _, turn := range turns {
				if _, ok := td.turnTiming[turn.ID]; !ok {
					td.turnTiming[turn.ID] = turn
				}
			}
		}
	}
	for i := range messages {
		applyTurnTiming(&messages[i], td.turnTiming)
	}
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

func (b *MemoryBackend) updateTurnFinish(threadID, turnID string, turnIndex int, status, errMsg string) {
	b.turnPersistMu.Lock()
	defer b.turnPersistMu.Unlock()

	completedAt := b.now().UTC()
	var existing store.Turn
	var getErr error
	if b.store != nil {
		ctx, cancel := persistCtx()
		existing, getErr = b.store.GetTurn(ctx, turnID)
		cancel()
		if getErr == nil && existing.Status != "running" && existing.Status != "starting" {
			b.mu.Lock()
			if td := b.threads[threadID]; td != nil {
				if td.turnTiming == nil {
					td.turnTiming = make(map[string]store.Turn)
				}
				td.turnTiming[turnID] = existing
				for i := range td.messages {
					applyTurnTiming(&td.messages[i], td.turnTiming)
				}
			}
			b.mu.Unlock()
			return
		}
	}
	b.mu.Lock()
	updateSummary := false
	var timing store.Turn
	if td, ok := b.threads[threadID]; ok {
		turn, exists := td.turnTiming[turnID]
		if exists && turn.Status != "running" && turn.Status != "starting" {
			if b.store == nil || (getErr != nil && !errors.Is(getErr, store.ErrNotFound)) || (getErr == nil && existing.Status != "running" && existing.Status != "starting") {
				b.mu.Unlock()
				return
			}
			status = turn.Status
			if turn.CompletedAt != nil {
				completedAt = *turn.CompletedAt
			}
		}
		if !exists {
			if b.store != nil && getErr == nil {
				turn = existing
			} else {
				turn = store.Turn{ID: turnID, ThreadID: threadID, TurnIndex: turnIndex}
			}
		}
		turn.Status, turn.CompletedAt = status, &completedAt
		timing = turn
		if td.turnTiming == nil {
			td.turnTiming = make(map[string]store.Turn)
		}
		td.turnTiming[turnID] = turn
		for i := range td.messages {
			applyTurnTiming(&td.messages[i], td.turnTiming)
		}
		if status != "" && (td.runTurnID == "" || td.runTurnID == turnID) {
			updateSummary = true
			lastStatus := status
			td.summary.LastStatus = &lastStatus
		}
	}
	b.mu.Unlock()

	if b.store == nil {
		return
	}

	ctx, cancel := persistCtx()
	defer cancel()

	if status != "" && updateSummary {
		if err := b.store.UpdateThreadLastStatus(ctx, threadID, status); err != nil && !errors.Is(err, store.ErrNotFound) {
			log.Printf("update thread last status %s: %v", threadID, err)
		}
	}

	if getErr != nil {
		if errors.Is(getErr, store.ErrNotFound) {
			if !timing.StartedAt.IsZero() {
				timing.ErrorMessage = errMsg
				if err := b.store.CreateTurn(ctx, timing); err != nil {
					log.Printf("create finished turn %s: %v", turnID, err)
				}
			} else {
				log.Printf("cannot finish turn %s: missing start time", turnID)
			}
		} else {
			log.Printf("get turn %s for finish error: %v", turnID, getErr)
		}
		return
	}

	if existing.Status != "running" && existing.Status != "starting" {
		return
	}
	b.mu.Lock()
	td, ok := b.threads[threadID]
	workDir := b.harnessWorkDir
	var touchedPaths []string
	if ok {
		if td.workDir != "" {
			workDir = td.workDir
		} else if proj, ok := b.projects[td.summary.ProjectID]; ok && proj.Path != "" {
			workDir = proj.Path
		}
		touchedPaths = append([]string(nil), td.touchedPaths...)
	}
	gitRunner := b.git
	var inTokens, outTokens int64
	if turnMap, ok := b.usage[threadID]; ok {
		if u, ok := turnMap[turnIndex]; ok {
			inTokens = int64(u.InputTokens)
			outTokens = int64(u.OutputTokens)
		}
	}
	b.mu.Unlock()

	existing.Status = status
	existing.CompletedAt = &completedAt
	existing.ErrorMessage = errMsg
	existing.InputTokens = inTokens
	existing.OutputTokens = outTokens

	if gitRunner != nil && workDir != "" {
		gitCtx, gitCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if root, err := gitRunner.RepoRoot(gitCtx, workDir); err == nil && root != "" {
			ref := git.CheckpointRef(threadID, turnID)
			if start, err := gitRunner.RevParse(gitCtx, root, ref+"/start"); err == nil {
				existing.BaseCheckpoint = start
			}
			if end, err := gitRunner.RevParse(gitCtx, root, ref+"/end"); err == nil {
				existing.FinishCheckpoint = end
			}
			// 优先记录本会话工具实际触碰的路径；provider 未上报路径（touchedPaths 为空）时，
			// 保留按 start→finish 全库差异反推的回退逻辑。
			if files := normalizeTouchedFiles(workDir, root, touchedPaths); len(files) > 0 {
				existing.TouchedFiles = files
			} else if existing.BaseCheckpoint != "" {
				diffs, err := gitRunner.Diff(gitCtx, root, existing.BaseCheckpoint, existing.FinishCheckpoint, nil)
				if err == nil {
					seen := make(map[string]bool)
					var files []string
					for _, d := range diffs {
						p := d.NewPath
						if p == "" {
							p = d.OldPath
						}
						if p != "" && !seen[p] {
							seen[p] = true
							files = append(files, p)
						}
					}
					existing.TouchedFiles = files
				}
			}
		}
		gitCancel()
	}

	writeCtx, writeCancel := persistCtx()
	defer writeCancel()
	if err := b.store.UpdateTurn(writeCtx, existing); err != nil {
		log.Printf("store update turn %s error: %v", turnID, err)
	}

	// 一轮结束只是触发防抖：线程空闲一段时间后才真正同步，消息没变时不会再发请求（后台串行、可合并，失败只记日志）。
	b.scheduleNowledgeSync(threadID)
}

// CreateThread 创建新线程，支持按 ActionID 幂等。
func (b *MemoryBackend) CreateThread(ctx context.Context, params wire.ThreadCreateParams) (wire.ThreadCreateResult, error) {
	return b.createThread(ctx, params, true)
}

// createThread 创建新线程；userInitiated 表示用户主动新建（threads.create）。
// 用户主动新建时会用宿主记忆补齐没传的选择；编排子代理、看板任务与自动化会话
// 走的是 StartSession 这条系统路径，不读也不写这份记忆。记忆只由用户主动操作
// （providers.rememberSelection）写入，建会话绝不回写。
func (b *MemoryBackend) createThread(ctx context.Context, params wire.ThreadCreateParams, userInitiated bool) (wire.ThreadCreateResult, error) {
	// 客户端没传（nil）时套用全局「新会话默认停用」名单；读盘必须在持锁前完成。
	// 空数组表示明确要求全部启用，不套默认。
	if params.MCPDisabled == nil {
		b.mu.Lock()
		dataDir := b.dataDir
		b.mu.Unlock()
		params.MCPDisabled = agentconf.NewThreadDisabledServers(agentconf.Options{DataDir: dataDir})
	}

	b.mu.Lock()

	if params.ActionID != "" {
		if tid, ok := b.actionThreads[params.ActionID]; ok {
			b.mu.Unlock()
			b.actions.record(wire.ActionStatusResult{
				ActionID: params.ActionID,
				State:    wire.ActionStateAccepted,
				ThreadID: &tid,
			})
			return wire.ThreadCreateResult{
				ActionID: params.ActionID,
				ThreadID: tid,
			}, nil
		}
	}

	proj, ok := b.projects[params.ProjectID]
	if !ok {
		b.mu.Unlock()
		rpcErr := &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("project %s not found", params.ProjectID),
		}
		if params.ActionID != "" {
			b.actions.record(wire.ActionStatusResult{
				ActionID: params.ActionID,
				State:    wire.ActionStateFailed,
				Error: &wire.ActionError{
					Code:    rpcErr.Code,
					Message: rpcErr.Message,
				},
			})
		}
		return wire.ThreadCreateResult{}, rpcErr
	}

	threadID := newUUID()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// 用户主动新建时才用宿主记忆补齐没传的选择；只读记忆，不写记忆。
	if userInitiated {
		b.fillThreadCreateDefaultsLocked(&params)
	}

	provider := "memory"
	if b.defaultProvider != "" {
		provider = b.defaultProvider
	}
	if params.Options != nil && params.Options.Provider != nil {
		reqProvider := *params.Options.Provider
		if _, ok := b.harnesses[reqProvider]; !ok {
			b.mu.Unlock()
			rpcErr := &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: fmt.Sprintf("unknown provider %q", reqProvider),
			}
			if params.ActionID != "" {
				b.actions.record(wire.ActionStatusResult{
					ActionID: params.ActionID,
					State:    wire.ActionStateFailed,
					Error: &wire.ActionError{
						Code:    rpcErr.Code,
						Message: rpcErr.Message,
					},
				})
			}
			return wire.ThreadCreateResult{}, rpcErr
		}
		provider = reqProvider
	}

	var reqRuntimeMode string
	if params.RuntimeMode != nil && *params.RuntimeMode != "" {
		reqRuntimeMode = *params.RuntimeMode
	} else if params.Options != nil && params.Options.RuntimeMode != nil && *params.Options.RuntimeMode != "" {
		reqRuntimeMode = *params.Options.RuntimeMode
	}

	runtimeMode := b.defaultRuntimeModeLocked()
	if reqRuntimeMode != "" {
		if !isValidRuntimeMode(reqRuntimeMode) {
			b.mu.Unlock()
			rpcErr := &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: fmt.Sprintf("invalid runtimeMode %q", reqRuntimeMode),
			}
			if params.ActionID != "" {
				b.actions.record(wire.ActionStatusResult{
					ActionID: params.ActionID,
					State:    wire.ActionStateFailed,
					Error: &wire.ActionError{
						Code:    rpcErr.Code,
						Message: rpcErr.Message,
					},
				})
			}
			return wire.ThreadCreateResult{}, rpcErr
		}
		runtimeMode = harness.NormalizeMode(reqRuntimeMode)
	}

	var reqModel *string
	if params.Model != nil {
		reqModel = params.Model
	} else if params.Options != nil && params.Options.Model != nil {
		reqModel = params.Options.Model
	}

	modelVal := DefaultModel(provider)
	if reqModel != nil {
		if !b.validModel(provider, *reqModel) {
			b.mu.Unlock()
			rpcErr := &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: fmt.Sprintf("invalid model %q for provider %q", *reqModel, provider),
			}
			if params.ActionID != "" {
				b.actions.record(wire.ActionStatusResult{
					ActionID: params.ActionID,
					State:    wire.ActionStateFailed,
					Error: &wire.ActionError{
						Code:    rpcErr.Code,
						Message: rpcErr.Message,
					},
				})
			}
			return wire.ThreadCreateResult{}, rpcErr
		}
		modelVal = *reqModel
	}

	swarmEnabled := false
	if b.swarmEnabledFor != nil {
		swarmEnabled = b.swarmEnabledFor(threadID)
	}
	// 编排开关为 true（显式传入或从记忆补齐）时在创建时就开启编排，
	// 与 threads.swarm.set 走同一后端实现，否则首轮会按普通会话执行、不派子代理。
	swarmEnabler := b.swarmEnablerFunc
	wantSwarm := userInitiated && params.Options != nil && params.Options.SwarmEnabled != nil && *params.Options.SwarmEnabled

	var (
		hasExisting bool
		minSort     int64
	)
	for _, t := range b.threads {
		if t.summary.ProjectID == params.ProjectID {
			if !hasExisting || t.sortOrder < minSort {
				minSort = t.sortOrder
				hasExisting = true
			}
		}
	}
	var sortOrder int64
	if hasExisting {
		sortOrder = minSort - 1
	} else {
		sortOrder = 0
	}

	isProvisional := params.Provisional != nil && *params.Provisional
	summary := wire.ThreadSummary{
		ID:                threadID,
		ProjectID:         params.ProjectID,
		Title:             defaultThreadTitle,
		Provider:          provider,
		Model:             &modelVal,
		IsRunning:         false,
		IsArchived:        false,
		IsPinned:          false,
		IsDormant:         false,
		HasUnread:         false,
		Attention:         "normal",
		Section:           "recent",
		OrderKey:          formatOrderKey(sortOrder),
		UpdatedAt:         now,
		RuntimeMode:       runtimeMode,
		InteractionMode:   "build",
		FastMode:          false,
		MCPDisabled:       params.MCPDisabled,
		WorktreeRequested: false,
		SwarmEnabled:      swarmEnabled,
		IsProvisional:     isProvisional,
	}
	if params.Options != nil {
		summary.SwarmBeesProvider = optionalString(derefString(params.Options.SwarmBeesProvider))
		summary.SwarmBeesModel = optionalString(derefString(params.Options.SwarmBeesModel))
		if params.Options.SwarmBeesEffort != nil {
			summary.SwarmBeesEffort = optionalString(*params.Options.SwarmBeesEffort)
		}
		if params.Options.Effort != nil {
			summary.Effort = optionalString(*params.Options.Effort)
		}
		if params.Options.SwarmPairID != nil {
			summary.SwarmPairID = optionalString(*params.Options.SwarmPairID)
		}
		if params.Options.FastMode != nil {
			summary.FastMode = *params.Options.FastMode
		}
	}

	b.threads[threadID] = &memoryThread{
		summary:        summary,
		sortOrder:      sortOrder,
		queueRevision:  1,
		messages:       []wire.ThreadMessage{},
		messagesLoaded: true,
		persisted:      make(map[string]bool),
		pending:        make(map[string]*pendingRequest),
		alwaysAllow:    make(map[string]bool),
		parentThreadID: "",
		isHelper:       false,
		dormantAt:      nil,
	}
	if params.ActionID != "" {
		b.actionThreads[params.ActionID] = threadID
	}
	proj.ThreadCount++
	b.projects[params.ProjectID] = proj

	s := b.store
	b.mu.Unlock()

	if s != nil {
		storeThread := threadToStore(summary, "", sortOrder)
		storeThread.IsProvisional = isProvisional
		if err := s.CreateThread(ctx, storeThread); err != nil {
			b.mu.Lock()
			delete(b.threads, threadID)
			if params.ActionID != "" {
				delete(b.actionThreads, params.ActionID)
			}
			if p, ok := b.projects[params.ProjectID]; ok {
				p.ThreadCount--
				b.projects[params.ProjectID] = p
			}
			b.mu.Unlock()

			rpcErr := &wire.RPCError{
				Code:    wire.InternalError,
				Message: fmt.Sprintf("failed to persist thread: %v", err),
			}
			if params.ActionID != "" {
				b.actions.record(wire.ActionStatusResult{
					ActionID: params.ActionID,
					State:    wire.ActionStateFailed,
					Error: &wire.ActionError{
						Code:    rpcErr.Code,
						Message: rpcErr.Message,
					},
				})
			}
			return wire.ThreadCreateResult{}, rpcErr
		}
	}

	b.hub.Publish(threadID, nil, nil, summary)

	if wantSwarm && swarmEnabler != nil {
		swarmEnabler(threadID, true)
	}

	b.mu.Lock()
	hook := b.threadsChangedHook
	b.mu.Unlock()
	if hook != nil {
		hook(wire.ThreadsChanged{
			Threads: []wire.ThreadSummary{summary},
		})
	}

	if params.ActionID != "" {
		b.actions.record(wire.ActionStatusResult{
			ActionID: params.ActionID,
			State:    wire.ActionStateAccepted,
			ThreadID: &threadID,
		})
	}

	return wire.ThreadCreateResult{
		ActionID: params.ActionID,
		ThreadID: threadID,
	}, nil
}

// UpdateThread 更新指定会话的属性（支持 runtimeMode、title、provider、model），并通过 StreamHub 推送更新。
func (b *MemoryBackend) UpdateThread(ctx context.Context, params wire.ThreadUpdateParams) (wire.ThreadUpdateResult, error) {
	if params.Effort != nil && !isValidEffort(*params.Effort) {
		return wire.ThreadUpdateResult{}, &wire.RPCError{Code: wire.InvalidParams, Message: fmt.Sprintf("invalid effort %q", *params.Effort)}
	}
	if params.RuntimeMode != nil && !isValidRuntimeMode(*params.RuntimeMode) {
		return wire.ThreadUpdateResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: fmt.Sprintf("invalid runtimeMode %q", *params.RuntimeMode),
		}
	}
	if params.Title != nil && len(*params.Title) == 0 {
		return wire.ThreadUpdateResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "title cannot be empty",
		}
	}

	b.mu.Lock()
	td, ok := b.threads[params.ThreadID]
	if !ok {
		b.mu.Unlock()
		return wire.ThreadUpdateResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", params.ThreadID),
		}
	}

	titleChanged := false
	runtimeModeChanged := false
	providerChanged := false
	modelChanged := false
	pinnedChanged := false
	archivedChanged := false
	dormantChanged := false
	unreadChanged := false

	targetProvider := td.summary.Provider
	if params.Provider != nil && *params.Provider != "" && *params.Provider != td.summary.Provider {
		newProvider := *params.Provider
		if td.summary.IsRunning || td.takingBack {
			b.mu.Unlock()
			return wire.ThreadUpdateResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: "运行或撤回期间无法更换供应商",
			}
		}
		if _, exists := b.harnesses[newProvider]; !exists {
			b.mu.Unlock()
			return wire.ThreadUpdateResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: fmt.Sprintf("unknown or unregistered provider %q", newProvider),
			}
		}
		targetProvider = newProvider
		providerChanged = true
	}

	var targetModel string
	if params.Model != nil {
		reqModel := *params.Model
		if !b.validModel(targetProvider, reqModel) {
			b.mu.Unlock()
			return wire.ThreadUpdateResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: fmt.Sprintf("invalid model %q for provider %q", reqModel, targetProvider),
			}
		}
		targetModel = reqModel
		var currentModel string
		if td.summary.Model != nil {
			currentModel = *td.summary.Model
		}
		if td.summary.Model == nil || targetModel != currentModel || providerChanged {
			modelChanged = true
		}
	} else if providerChanged {
		targetModel = DefaultModel(targetProvider)
		modelChanged = true
	}
	effortChanged := params.Effort != nil && *params.Effort != derefString(td.summary.Effort)
	if effortChanged && b.store != nil {
		if err := b.store.UpdateThreadComposerPrefs(ctx, params.ThreadID, td.summary.InteractionMode, *params.Effort, derefString(td.summary.SwarmBeesProvider), derefString(td.summary.SwarmBeesModel), derefString(td.summary.SwarmBeesEffort), td.summary.Persona); err != nil {
			b.mu.Unlock()
			return wire.ThreadUpdateResult{}, err
		}
	}

	var (
		cascadedArchived  []wire.ThreadSummary
		cascadedDormant   []wire.ThreadSummary
		cascadedDormantAt *time.Time
	)

	if params.Title != nil && *params.Title != "" && *params.Title != td.summary.Title {
		td.summary.Title = *params.Title
		titleChanged = true
	}
	if params.RuntimeMode != nil && *params.RuntimeMode != "" && harness.NormalizeMode(*params.RuntimeMode) != td.summary.RuntimeMode {
		td.summary.RuntimeMode = harness.NormalizeMode(*params.RuntimeMode)
		runtimeModeChanged = true
	}
	if params.Pinned != nil && *params.Pinned != td.summary.IsPinned {
		td.summary.IsPinned = *params.Pinned
		pinnedChanged = true
	}
	if params.Archived != nil {
		if *params.Archived {
			// 对该线程整个家族（递归子线程及 helper 会话）全部归档，同时取消置顶、清除休眠；
			// 先撤掉家族内的待处理卡片，正在运行的成员再中断。
			familyIDs := b.familyThreadIDsLocked(params.ThreadID)
			type runningTarget struct {
				id     string
				turnID string
			}
			var toInterrupt []runningTarget
			var withdrew []*withdrawBatch
			for _, fid := range familyIDs {
				ftd, ok := b.threads[fid]
				if !ok {
					continue
				}
				if batch := b.takeWithdrawBatchLocked(ftd, withdrawArchive); batch != nil {
					withdrew = append(withdrew, batch)
				}
				if ftd.summary.IsRunning {
					toInterrupt = append(toInterrupt, runningTarget{id: fid, turnID: ftd.runTurnID})
				}
			}
			if len(withdrew) > 0 || len(toInterrupt) > 0 {
				b.mu.Unlock()
				// 先把卡片已撤销的消息发出去，再中断运行中的成员，避免被中断入口写成"已中断"。
				for _, batch := range withdrew {
					b.finishWithdraw(batch, withdrawArchive)
				}
				for _, target := range toInterrupt {
					_, _ = b.Interrupt(ctx, wire.InterruptParams{
						ThreadID: target.id,
						TurnID:   target.turnID,
					})
				}
				b.mu.Lock()
				td, ok = b.threads[params.ThreadID]
				if !ok {
					b.mu.Unlock()
					return wire.ThreadUpdateResult{}, &wire.RPCError{
						Code:    wire.ErrCodeResourceNotFound,
						Message: fmt.Sprintf("thread %s not found", params.ThreadID),
					}
				}
				familyIDs = b.familyThreadIDsLocked(params.ThreadID)
			}

			nowTime := b.nowLocked()
			nowStr := nowTime.Format(time.RFC3339Nano)
			for _, fid := range familyIDs {
				if ftd, ok := b.threads[fid]; ok {
					ftd.summary.IsArchived = true
					ftd.summary.IsPinned = false
					ftd.summary.IsDormant = false
					ftd.dormantAt = nil
					ftd.summary.UpdatedAt = nowStr
					if fid != params.ThreadID {
						cascadedArchived = append(cascadedArchived, ftd.summary)
					}
				}
			}
			archivedChanged = true
			pinnedChanged = true
			dormantChanged = true
		} else {
			if td.summary.IsArchived {
				// C-ST-07：取消归档时按需回载消息，让会话恢复为可完整访问的状态。
				if err := b.ensureMessagesLocked(td, params.ThreadID); err != nil {
					b.mu.Unlock()
					return wire.ThreadUpdateResult{}, err
				}
				b.touchThreadLocked(td)
				td.summary.IsArchived = false
				archivedChanged = true
			}
		}
	}
	if params.Dormant != nil {
		if *params.Dormant {
			// 休眠不撤卡：会话只是暂时收起，待处理卡片在唤醒后仍需要可答。
			nowTime := b.nowLocked()
			nowStr := nowTime.Format(time.RFC3339Nano)
			td.summary.IsDormant = true
			td.dormantAt = &nowTime
			td.summary.IsPinned = false
			td.summary.HasUnread = false
			td.summary.UpdatedAt = nowStr
			dormantChanged = true
			pinnedChanged = true
			cascadedDormantAt = &nowTime

			// 级联到 helper 子线程
			familyIDs := b.familyThreadIDsLocked(params.ThreadID)
			for _, fid := range familyIDs {
				if fid == params.ThreadID {
					continue
				}
				if ftd, ok := b.threads[fid]; ok && ftd.isHelper {
					ftd.summary.IsDormant = true
					ftd.dormantAt = &nowTime
					ftd.summary.IsPinned = false
					ftd.summary.HasUnread = false
					ftd.summary.UpdatedAt = nowStr
					cascadedDormant = append(cascadedDormant, ftd.summary)
				}
			}
		} else {
			nowStr := b.nowLocked().Format(time.RFC3339Nano)
			td.summary.IsDormant = false
			td.dormantAt = nil
			dormantChanged = true

			// 取消休眠同步级联到 helper 子线程，避免父子会话休眠状态不一致
			familyIDs := b.familyThreadIDsLocked(params.ThreadID)
			for _, fid := range familyIDs {
				if fid == params.ThreadID {
					continue
				}
				if ftd, ok := b.threads[fid]; ok && ftd.isHelper {
					ftd.summary.IsDormant = false
					ftd.dormantAt = nil
					ftd.summary.UpdatedAt = nowStr
					cascadedDormant = append(cascadedDormant, ftd.summary)
				}
			}
		}
	}
	if params.Read != nil {
		targetUnread := !*params.Read
		if td.summary.HasUnread != targetUnread {
			td.summary.HasUnread = targetUnread
			unreadChanged = true
		}
	}
	if providerChanged {
		td.summary.Provider = targetProvider
		td.sessionID = ""
		td.sessionEpoch++
		// 会话被替换，转发上下文需要在新会话里重新送达。
		td.continuationSent = false
		td.needsHandoff = true
	}
	if modelChanged {
		m := targetModel
		td.summary.Model = &m
	}
	if effortChanged {
		td.summary.Effort = optionalString(*params.Effort)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if titleChanged || runtimeModeChanged || providerChanged || modelChanged || effortChanged || pinnedChanged || archivedChanged || dormantChanged || unreadChanged {
		td.summary.UpdatedAt = now
	}

	currentSummary := b.summaryForPublishLocked(td)
	currentDormantAt := td.dormantAt
	hook := b.threadsChangedHook
	b.mu.Unlock()

	if titleChanged {
		b.persistThreadTitle(params.ThreadID, currentSummary.Title)
	}
	if runtimeModeChanged {
		b.persistThreadRuntimeMode(params.ThreadID, currentSummary.RuntimeMode)
	}
	if providerChanged {
		b.persistThreadProvider(params.ThreadID, currentSummary.Provider)
		b.persistThreadSessionReset(params.ThreadID)
	}
	if modelChanged {
		var m string
		if currentSummary.Model != nil {
			m = *currentSummary.Model
		}
		b.persistThreadModel(params.ThreadID, m)
	}
	if pinnedChanged {
		b.persistThreadPinned(params.ThreadID, currentSummary.IsPinned)
	}
	if archivedChanged {
		b.persistThreadArchived(params.ThreadID, currentSummary.IsArchived)
	}
	if unreadChanged {
		b.persistThreadUnread(params.ThreadID, currentSummary.HasUnread)
	}
	if dormantChanged {
		b.persistThreadLifecycle(params.ThreadID, currentSummary.IsDormant, currentDormantAt, currentSummary.IsProvisional, currentSummary.HasUnread)
	}

	for _, sum := range cascadedArchived {
		b.persistThreadArchived(sum.ID, true)
		b.persistThreadPinned(sum.ID, false)
		b.persistThreadLifecycle(sum.ID, false, nil, sum.IsProvisional, sum.HasUnread)
		b.hub.Publish(sum.ID, nil, nil, sum)
	}
	for _, sum := range cascadedDormant {
		b.persistThreadPinned(sum.ID, sum.IsPinned)
		b.persistThreadLifecycle(sum.ID, sum.IsDormant, cascadedDormantAt, sum.IsProvisional, sum.HasUnread)
		b.hub.Publish(sum.ID, nil, nil, sum)
	}

	b.hub.Publish(params.ThreadID, nil, nil, currentSummary)

	if hook != nil {
		allChanged := []wire.ThreadSummary{currentSummary}
		allChanged = append(allChanged, cascadedArchived...)
		allChanged = append(allChanged, cascadedDormant...)
		hook(wire.ThreadsChanged{Threads: allChanged})
	}

	return wire.ThreadUpdateResult{
		Thread: currentSummary,
	}, nil
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

// SetThreadComposerPrefs 更新会话级 composer 偏好并写穿到 store。仅供桌面端直接调用，不注册为 RPC。
func (b *MemoryBackend) SetThreadComposerPrefs(ctx context.Context, threadID string, p ThreadComposerPrefs) (wire.ThreadSummary, error) {
	if p.InteractionMode == "" {
		p.InteractionMode = "build"
	}
	if p.InteractionMode != "build" && p.InteractionMode != "plan" {
		return wire.ThreadSummary{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: fmt.Sprintf("invalid interactionMode %q", p.InteractionMode),
		}
	}
	if !isValidEffort(p.Effort) {
		return wire.ThreadSummary{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: fmt.Sprintf("invalid effort %q", p.Effort),
		}
	}
	if !isValidEffort(p.BeesEffort) {
		return wire.ThreadSummary{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: fmt.Sprintf("invalid bees effort %q", p.BeesEffort),
		}
	}
	if !isValidPersona(p.Persona) {
		return wire.ThreadSummary{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: fmt.Sprintf("invalid persona %q", p.Persona),
		}
	}

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return wire.ThreadSummary{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}
	// 先落盘再更新内存，保存失败不得向界面报告成功；锁内写入保证并发更新顺序一致。
	if b.store != nil {
		if err := b.store.UpdateThreadComposerPrefs(ctx, threadID, p.InteractionMode, p.Effort, p.BeesProvider, p.BeesModel, p.BeesEffort, p.Persona); err != nil {
			b.mu.Unlock()
			return wire.ThreadSummary{}, err
		}
	}
	td.summary.InteractionMode = p.InteractionMode
	td.summary.Effort = optionalString(p.Effort)
	td.summary.Persona = p.Persona
	td.summary.SwarmBeesProvider = optionalString(p.BeesProvider)
	td.summary.SwarmBeesModel = optionalString(p.BeesModel)
	td.summary.SwarmBeesEffort = optionalString(p.BeesEffort)
	currentSummary := b.summaryForPublishLocked(td)
	b.mu.Unlock()

	b.hub.Publish(threadID, nil, nil, currentSummary)

	return currentSummary, nil
}

// SetThreadMCPDisabled 更新会话级外部 MCP 禁用名单（agent:scope:name 键）并广播会话摘要。
// 空名单表示恢复为「沿用全局设置」。名单写穿到 store，重启后由 AttachStore 还原。
func (b *MemoryBackend) SetThreadMCPDisabled(ctx context.Context, threadID string, keys []string) (wire.ThreadSummary, error) {
	cleaned := make([]string, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		cleaned = append(cleaned, key)
	}

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return wire.ThreadSummary{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}
	// 先落盘再更新内存，保存失败不得向界面报告成功；锁内写入保证并发更新顺序一致。
	if b.store != nil {
		if err := b.store.SetThreadMCPDisabled(ctx, threadID, cleaned); err != nil {
			b.mu.Unlock()
			return wire.ThreadSummary{}, err
		}
	}
	td.summary.MCPDisabled = cleaned
	currentSummary := b.summaryForPublishLocked(td)
	b.mu.Unlock()

	b.hub.Publish(threadID, nil, nil, currentSummary)

	return currentSummary, nil
}

// ThreadMCPDisabled 返回会话级外部 MCP 禁用名单的副本；会话不存在时返回 nil。
// 注入外部 MCP 时用它判断哪些服务被本会话关掉。
func (b *MemoryBackend) ThreadMCPDisabled(threadID string) []string {
	if threadID == "" {
		return nil
	}
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return nil
	}
	keys := append([]string(nil), td.summary.MCPDisabled...)
	b.mu.Unlock()
	return keys
}

// SetThreadFastMode 更新会话的快速模式与速率档位并写穿到 store。仅供桌面端直接调用，不注册为 RPC。
func (b *MemoryBackend) SetThreadFastMode(ctx context.Context, threadID string, fastMode bool, speedTier string) (wire.ThreadSummary, error) {
	// 关闭快速模式时速率档位无意义，强制置空避免残留陈旧档位。
	if !fastMode {
		speedTier = ""
	}

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return wire.ThreadSummary{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}
	// 先落盘再更新内存，保存失败不得向界面报告成功；锁内写入保证并发更新顺序一致。
	if b.store != nil {
		if err := b.store.UpdateThreadFastMode(ctx, threadID, fastMode, speedTier); err != nil {
			b.mu.Unlock()
			return wire.ThreadSummary{}, err
		}
	}
	td.summary.FastMode = fastMode
	td.summary.SpeedTier = optionalString(speedTier)
	currentSummary := b.summaryForPublishLocked(td)
	b.mu.Unlock()

	b.hub.Publish(threadID, nil, nil, currentSummary)

	return currentSummary, nil
}

// SetThreadSwarmMode 更新会话级 Swarm 工作模式并写穿到 store。仅供桌面端直接调用，不注册为 RPC。
// 空值或未知模式归一为 inherited（跟随全局设置）。
func (b *MemoryBackend) SetThreadSwarmMode(ctx context.Context, threadID, mode string) (wire.ThreadSummary, error) {
	mode = swarm.NormalizeMode(mode)

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return wire.ThreadSummary{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}
	// 先落盘再更新内存，保存失败不得向界面报告成功；锁内写入保证并发更新顺序一致。
	if b.store != nil {
		if err := b.store.UpdateThreadSwarmMode(ctx, threadID, mode); err != nil {
			b.mu.Unlock()
			return wire.ThreadSummary{}, err
		}
	}
	td.summary.SwarmMode = mode
	currentSummary := b.summaryForPublishLocked(td)
	b.mu.Unlock()

	b.hub.Publish(threadID, nil, nil, currentSummary)

	return currentSummary, nil
}

// SwarmThreadContext 在持锁状态下由新到旧扫描会话消息，寻找首条文本不是编排通知且 Trim 后不以 / 开头的 user 角色消息；
// 返回其文本内容、会话生效的配对 ID（显式选择优先，否则按模型推断）、会话存储的档位覆盖 JSON，
// 以及会话里选的子线程默认服务商、模型与推理强度。
func (b *MemoryBackend) SwarmThreadContext(threadID string) (userMessage, pairID, beeTiers, beeProvider, beeModel, beeEffort string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	td, ok := b.threads[threadID]
	if !ok {
		return "", "", "", "", "", ""
	}
	_ = b.ensureMessagesLocked(td, threadID)
	b.ensureMessagesLoadedLocked(td)

	beeTiers = td.summary.SwarmBeeTiers
	if td.summary.SwarmBeesProvider != nil {
		beeProvider = *td.summary.SwarmBeesProvider
	}
	if td.summary.SwarmBeesModel != nil {
		beeModel = *td.summary.SwarmBeesModel
	}
	beeEffort = derefString(td.summary.SwarmBeesEffort)
	// 生效配对在读取时计算，不回写数据库：历史会话没绑配对时也能按模型套用配对里的档位映射。
	swarmCfg := b.swarmConfig()
	pairID = effectivePairID(swarmPairsFromConfig(swarmCfg.Pairs), derefString(td.summary.SwarmPairID),
		td.summary.Provider, derefString(td.summary.Model),
		trimOr(beeProvider, swarmCfg.DefaultProvider), trimOr(beeModel, swarmCfg.DefaultModel))

	for i := len(td.messages) - 1; i >= 0; i-- {
		m := td.messages[i]
		if m.Role != "user" {
			continue
		}
		text := m.Text
		if swarm.IsNotice(text) || strings.HasPrefix(strings.TrimSpace(text), "/") {
			continue
		}
		userMessage = text
		break
	}
	return userMessage, pairID, beeTiers, beeProvider, beeModel, beeEffort
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

// SwarmProviderEnabled 判断指定 Provider 是否已启用且可用，判定依据与 ListProviders 保持一致。
func (b *MemoryBackend) SwarmProviderEnabled(providerID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	h, ok := b.harnesses[providerID]
	if !ok {
		return false
	}
	if rep, ok := h.(availabilityReporter); ok {
		available, _ := rep.Available()
		return available
	}
	return true
}

// SetThreadBeeTiers 校验 beeTiers（必须为空或合法 JSON 对象），通过 store 的 SetThreadBeeTiers 持久化，
// 并更新内存中的会话摘要及向前端广播 threads.changed 通知。
func (b *MemoryBackend) SetThreadBeeTiers(ctx context.Context, threadID, beeTiers string) error {
	trimmed := strings.TrimSpace(beeTiers)
	if trimmed != "" {
		var obj map[string]any
		if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
			return &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: "beeTiers must be empty or a valid JSON object",
			}
		}
	} else {
		trimmed = ""
	}

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}

	if b.store != nil {
		if err := b.store.SetThreadBeeTiers(ctx, threadID, trimmed); err != nil {
			b.mu.Unlock()
			return err
		}
	}

	td.summary.SwarmBeeTiers = trimmed
	currentSummary := b.summaryForPublishLocked(td)
	hook := b.threadsChangedHook
	b.mu.Unlock()

	b.hub.Publish(threadID, nil, nil, currentSummary)
	if hook != nil {
		hook(wire.ThreadsChanged{Threads: []wire.ThreadSummary{currentSummary}})
	}

	return nil
}

// SetThreadSwarmPairID 更新会话的 Swarm 配对 ID。若配对发生实质变更，清空会话的档位覆盖配置。
func (b *MemoryBackend) SetThreadSwarmPairID(ctx context.Context, threadID, pairID string) error {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}

	oldPairID := derefString(td.summary.SwarmPairID)
	pairChanged := (oldPairID != pairID)
	td.summary.SwarmPairID = optionalString(pairID)
	if b.store != nil {
		if err := b.store.SetThreadSwarmPairID(ctx, threadID, pairID); err != nil {
			b.mu.Unlock()
			return err
		}
	}
	if pairChanged {
		td.summary.SwarmBeeTiers = ""
		if b.store != nil {
			if err := b.store.SetThreadBeeTiers(ctx, threadID, ""); err != nil {
				b.mu.Unlock()
				return err
			}
		}
	}
	currentSummary := b.summaryForPublishLocked(td)
	hook := b.threadsChangedHook
	b.mu.Unlock()

	b.hub.Publish(threadID, nil, nil, currentSummary)
	if hook != nil {
		hook(wire.ThreadsChanged{Threads: []wire.ThreadSummary{currentSummary}})
	}

	return nil
}

// SetThreadsChangedHook 设置会话列表变动的监听回调函数。
func (b *MemoryBackend) SetThreadsChangedHook(fn func(wire.ThreadsChanged)) {
	b.mu.Lock()
	b.threadsChangedHook = fn
	b.mu.Unlock()
}

// AddThreadsChangedListener 追加一个会话列表变动监听（不覆盖已有钩子），返回注销函数。
func (b *MemoryBackend) AddThreadsChangedListener(fn func(wire.ThreadsChanged)) (remove func()) {
	if fn == nil {
		return func() {}
	}
	b.mu.Lock()
	prev := b.threadsChangedHook
	b.threadsChangedHook = func(tc wire.ThreadsChanged) {
		if prev != nil {
			prev(tc)
		}
		fn(tc)
	}
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.threadsChangedHook != nil {
			b.threadsChangedHook = prev
		}
	}
}

// SetThreadDeletedHook 设置会话被删除前的钩子函数。
func (b *MemoryBackend) SetThreadDeletedHook(fn func(ctx context.Context, threadID string)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.threadDeletedHook = fn
}

// DeleteThread 删除指定会话及其整个家族子会话：从叶子到根递归删除、触发删除钩子、清理临时定时器、中断运行轮次，并发出 threads.changed 通知。
func (b *MemoryBackend) DeleteThread(ctx context.Context, threadID string) error {
	b.mu.Lock()
	if _, ok := b.threads[threadID]; !ok {
		b.mu.Unlock()
		return store.ErrNotFound
	}
	familyIDs := b.familyThreadIDsLocked(threadID)
	b.mu.Unlock()

	// 从叶子到根（familyThreadIDsLocked 返回根在前，因此反向遍历）
	deleteOrder := make([]string, len(familyIDs))
	for i, id := range familyIDs {
		deleteOrder[len(familyIDs)-1-i] = id
	}

	var removedIDs []string
	for _, targetID := range deleteOrder {
		b.mu.Lock()
		td, ok := b.threads[targetID]
		if !ok {
			b.mu.Unlock()
			continue
		}
		isRunning := td.summary.IsRunning
		runTurnID := td.runTurnID
		// 删除前先撤掉待处理卡片：会话即将消失，不写时间线提示，只在锁外广播空列表。
		withdrawnBatch := b.takeWithdrawBatchLocked(td, withdrawDelete)
		delHook := b.threadDeletedHook
		// 先在锁内取出 harness 引用，EvictThread 可能阻塞，必须放到锁外调用。
		evictor, _ := b.threadHarnessLocked(td.summary.Provider).(harness.ThreadEvictor)
		workDir := b.harnessWorkDir
		if td.workDir != "" {
			workDir = td.workDir
		} else if proj, ok := b.projects[td.summary.ProjectID]; ok && proj.Path != "" {
			workDir = proj.Path
		}
		gitRunner := b.git
		b.mu.Unlock()

		b.publishWithdrawn(withdrawnBatch)

		if isRunning {
			_, _ = b.Interrupt(ctx, wire.InterruptParams{
				ThreadID: targetID,
				TurnID:   runTurnID,
			})
		}

		// 每个被删线程在移除前调用 hook（不能持锁调用）
		if delHook != nil {
			delHook(ctx, targetID)
		}

		// C-GIT-11：删除线程前清理其检查点 ref，失败仅记录日志，不阻断删除
		if gitRunner != nil && workDir != "" {
			if root, err := gitRunner.RepoRoot(ctx, workDir); err == nil && root != "" {
				if err := gitRunner.DeleteCheckpoints(ctx, root, targetID); err != nil {
					log.Printf("delete checkpoints for thread %s failed: %v", targetID, err)
				}
			}
		}

		b.mu.Lock()
		// 取消该线程的临时清理定时器
		if timer, hasTimer := b.provisionalTimers[targetID]; hasTimer {
			timer.Stop()
			delete(b.provisionalTimers, targetID)
		}
		delete(b.threads, targetID)
		delete(b.usage, targetID)
		b.readPathsMu.Lock()
		delete(b.readPaths, targetID)
		b.readPathsMu.Unlock()
		for aid, tid := range b.actionThreads {
			if tid == targetID {
				delete(b.actionThreads, aid)
			}
		}
		s := b.store
		b.mu.Unlock()

		// 会话已从内存移除，顺手释放该 thread 空闲常驻的后端进程（不能持锁调用）。
		if evictor != nil {
			evictor.EvictThread(targetID)
		}

		// 删除消息之前先把会话同步到 Nowledge：删除后本地不再有可导入的正文。
		b.syncNowledgeThread(targetID, nowledgeSyncDeleteTimeout)

		if s != nil {
			if err := s.DeleteThread(ctx, targetID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		// 正文已删除：清掉该会话的同步状态与持久待同步标记。删除前同步失败的不再补传。
		b.forgetNowledgeSync(targetID)

		removedIDs = append(removedIDs, targetID)
	}

	b.mu.Lock()
	hook := b.threadsChangedHook
	b.mu.Unlock()

	payload := wire.ThreadsChanged{
		RemovedThreadIDs: removedIDs,
	}
	b.hub.Notify(threadID, "threads.changed", payload)
	if hook != nil {
		hook(payload)
	}

	return nil
}

// RemoveProject 从内存与 store 中移除指定项目行，不删磁盘文件。
// store 删除项目时会在同一事务里按外键级联删除该项目的会话，因此内存中也同步移除这些会话
// （含以会话 ID 为键的用量、审批、定时器等缓存），否则它们会以悬挂项目分组的形式留在内存里。
func (b *MemoryBackend) RemoveProject(ctx context.Context, projectID string) error {
	b.mu.Lock()
	if _, ok := b.projects[projectID]; !ok {
		b.mu.Unlock()
		return store.ErrNotFound
	}
	delete(b.projects, projectID)

	// 先按 DeleteThread 的方式中断该项目下正在运行的会话，再统一移除。
	// evictors 记录各会话所属的后端 harness，移除后要在锁外释放它们的空闲常驻进程。
	var removedIDs []string
	evictors := make(map[string]harness.ThreadEvictor)
	for id, td := range b.threads {
		if td.summary.ProjectID != projectID {
			continue
		}
		removedIDs = append(removedIDs, id)
		if ev, ok := b.threadHarnessLocked(td.summary.Provider).(harness.ThreadEvictor); ok {
			evictors[id] = ev
		}
		if td.summary.IsRunning {
			// 会话随项目一起被移除，撤下的卡片无需再广播或写时间线提示。
			_, _, _ = b.interruptLocked(td, td.runTurnID)
		}
	}
	hook := b.threadDeletedHook
	b.mu.Unlock()

	// 每个被移除的会话在移除前调用 hook（不能持锁调用）
	if hook != nil {
		for _, id := range removedIDs {
			hook(ctx, id)
		}
	}

	b.mu.Lock()
	for _, id := range removedIDs {
		td := b.threads[id]
		if td == nil {
			continue
		}
		// 中断后残留的取消函数与看门狗定时器一并关闭，避免它们继续引用已移除的会话。
		if td.runCancel != nil {
			td.runCancel()
			td.runCancel = nil
		}
		if td.watchdogStop != nil {
			td.watchdogStop()
			td.watchdogStop = nil
		}
		if timer, hasTimer := b.provisionalTimers[id]; hasTimer {
			timer.Stop()
			delete(b.provisionalTimers, id)
		}
		delete(b.threads, id)
		delete(b.usage, id)
		delete(b.guidanceDelivered, id)
		delete(b.autoContinueWaits, id)
		b.readPathsMu.Lock()
		delete(b.readPaths, id)
		b.readPathsMu.Unlock()
		for aid, tid := range b.actionThreads {
			if tid == id {
				delete(b.actionThreads, aid)
			}
		}
	}
	s := b.store
	changedHook := b.threadsChangedHook
	b.mu.Unlock()

	// 会话已从内存移除，释放它们空闲常驻的后端进程（EvictThread 可能阻塞，不能持锁调用）。
	for _, id := range removedIDs {
		if ev, ok := evictors[id]; ok {
			ev.EvictThread(id)
		}
	}

	// 与 DeleteThread 一致：会话已从内存移除后广播 threads.changed（不能持锁回调），
	// 否则 web 端等远程客户端会把这些会话一直留在列表里。
	if len(removedIDs) > 0 {
		payload := wire.ThreadsChanged{RemovedThreadIDs: removedIDs}
		for _, id := range removedIDs {
			b.hub.Notify(id, "threads.changed", payload)
		}
		if changedHook != nil {
			changedHook(payload)
		}
	}

	if s != nil {
		if err := s.DeleteProject(ctx, projectID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}

// ActionStatus 查询指定动作的执行状态。若动作不存在或已超时，返回 unknown 状态，不返回错误。
func (b *MemoryBackend) ActionStatus(ctx context.Context, actionID string) (wire.ActionStatusResult, error) {
	if res, ok := b.actions.get(actionID); ok {
		return res, nil
	}
	return wire.ActionStatusResult{
		ActionID: actionID,
		State:    wire.ActionStateUnknown,
	}, nil
}

func (b *MemoryBackend) interruptLocked(td *memoryThread, turnID string) (wire.InterruptResult, *withdrawBatch, error) {
	b.ensureMessagesLoadedLocked(td)
	if !td.summary.IsRunning {
		// 本轮已结束但仍有遗留卡片时，中断入口也顺手撤掉，避免它们一直挂起。
		batch := b.takeWithdrawBatchLocked(td, withdrawInterrupt)
		turnIDVal := turnID
		if turnIDVal == "" {
			turnIDVal = td.runTurnID
			if turnIDVal == "" {
				for i := len(td.messages) - 1; i >= 0; i-- {
					if td.messages[i].TurnID != nil && *td.messages[i].TurnID != "" {
						turnIDVal = *td.messages[i].TurnID
						break
					}
				}
			}
		}
		return wire.InterruptResult{
			ThreadID: td.summary.ID,
			TurnID:   turnIDVal,
			State:    wire.InterruptStateAlreadyStopped,
		}, batch, nil
	}

	if td.stewardBusy && td.runTurnID == "" && td.runCancel == nil {
		return wire.InterruptResult{ThreadID: td.summary.ID, State: wire.InterruptStateAlreadyStopped}, nil, nil
	}

	if turnID != "" && turnID != td.runTurnID {
		return wire.InterruptResult{}, nil, &wire.RPCError{
			Code:    wire.ErrCodeTurnMismatch,
			Message: fmt.Sprintf("turnID %s does not match active turn %s", turnID, td.runTurnID),
		}
	}

	activeTurnID := td.runTurnID

	if td.watchdogStop != nil {
		return wire.InterruptResult{ThreadID: td.summary.ID, TurnID: activeTurnID, State: wire.InterruptStateStopping}, nil, nil
	}

	// 先撤掉待处理卡片再取消运行：撤卡不依赖 runCancel，卡片会立刻从列表和收件箱消失。
	batch := b.takeWithdrawBatchLocked(td, withdrawInterrupt)

	if td.phase == phaseStarting {
		b.forceInterruptFinishLocked(td, activeTurnID, "")
		return wire.InterruptResult{ThreadID: td.summary.ID, TurnID: activeTurnID, State: wire.InterruptStateStopping}, batch, nil
	}

	// phaseRunning
	if td.runCancel != nil {
		td.runCancel()
	}

	timeout := b.interruptWatchdogTimeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	targetThreadID := td.summary.ID
	targetTurnID := activeTurnID
	stopFn := scheduleAfter(b.afterFunc, timeout, func() {
		b.mu.Lock()
		td, ok := b.threads[targetThreadID]
		if !ok || td.runTurnID != targetTurnID || !td.summary.IsRunning {
			b.mu.Unlock()
			return
		}
		batch := b.forceInterruptFinishLocked(td, targetTurnID, "打断超时，已强制结束本轮")
		b.mu.Unlock()
		// 观察者在锁外回调，广播必须在解锁后进行。
		b.finishWithdraw(batch, withdrawInterrupt)
	})
	td.watchdogStop = stopFn

	return wire.InterruptResult{
		ThreadID: td.summary.ID,
		TurnID:   activeTurnID,
		State:    wire.InterruptStateStopping,
	}, batch, nil
}

func (b *MemoryBackend) forceInterruptFinishLocked(td *memoryThread, turnID string, noticeText string) *withdrawBatch {
	b.ensureMessagesLoadedLocked(td)
	// 强制收尾同样要先撤卡：卡片不再可答，Provider 侧也不必等 runCancel 生效。
	batch := b.takeWithdrawBatchLocked(td, withdrawInterrupt)
	if td.watchdogStop != nil {
		td.watchdogStop()
		td.watchdogStop = nil
	}
	if td.runCancel != nil {
		td.runCancel()
		td.runCancel = nil
	}
	td.sessionEpoch++
	// 会话被替换，转发上下文需要在新会话里重新送达。
	td.continuationSent = false
	td.runTurnID = ""
	td.setIdleLocked()
	retryUnsolicited := td.takeUnsolicitedPendingLocked()
	// 强制收尾不会经过 Interact 的清理块，这里显式复位等待标记，避免摘要一直显示"等待审批"。
	td.summary.HasPendingRequest = false
	td.summary.LastActivityText = nil
	now := b.nowLocked().Format(time.RFC3339Nano)
	td.summary.UpdatedAt = now
	turnIndex := 0
	for _, m := range td.messages {
		if m.TurnID != nil && *m.TurnID == turnID {
			turnIndex = m.TurnIndex
		}
	}
	var msgs []wire.ThreadMessage
	if noticeText != "" {
		kind := "info"
		msg := wire.ThreadMessage{ID: newUUID(), Role: "system", Text: noticeText, CreatedAt: now, TurnIndex: turnIndex, TurnID: &turnID, Kind: &kind}
		td.messages = append(td.messages, msg)
		msgs = append(msgs, msg)
	}
	var msgsToPersist []wire.ThreadMessage
	for _, msg := range td.messages {
		if msg.TurnID != nil && *msg.TurnID == turnID {
			msgsToPersist = append(msgsToPersist, msg)
		}
	}
	threadID := td.summary.ID
	h, sessionID := b.threadHarnessLocked(td.summary.Provider), td.sessionID
	previousHistory := td.historyOp
	done := make(chan struct{})
	td.historyOp = done
	if noticeText != "" {
		td.sessionID = ""
		td.needsHandoff = true
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		if noticeText != "" {
			if stopper, ok := h.(harness.TurnStopper); ok {
				stopper.ForceStopTurn(turnID)
			}
			if sessionID != "" {
				b.persistThreadSessionReset(threadID)
			}
		}
		if previousHistory != nil {
			<-previousHistory
		}
		b.persistMessages(threadID, msgsToPersist)
		b.updateTurnFinish(threadID, turnID, turnIndex, "interrupted", noticeText)
		b.mu.Lock()
		msgs = nil
		for _, msg := range td.messages {
			if msg.TurnID != nil && *msg.TurnID == turnID {
				msgs = append(msgs, msg)
			}
		}
		summary := b.summaryForPublishLocked(td)
		b.mu.Unlock()
		b.hub.Publish(threadID, msgs, nil, summary)
		b.hub.Flush(threadID)
		b.mu.Lock()
		if td.historyOp == done {
			td.historyOp = nil
		}
		close(done)
		b.mu.Unlock()
		b.mu.Lock()
		var next *startedTurn
		var text string
		var changed bool
		if b.threads[threadID] == td && !td.summary.IsRunning && !td.takingBack && b.ctx.Err() == nil {
			// 强制打断路径：priority 汇报也不立即开轮，留给用户下一句话。
			next, text, changed = b.startNextQueuedTurnLocked(td, false, false)
		}
		b.mu.Unlock()
		if next != nil {
			// 正文先落库再删队列行，崩溃时最多重复出现一条已写正文的排队项，不会丢消息。
			b.persistMessages(threadID, []wire.ThreadMessage{next.userMsg})
			b.recordDispatchingForTurn(threadID, next)
		}
		if changed {
			b.commitQueueChange(threadID)
		}
		if next != nil {
			b.hub.BeginRun(threadID)
			b.hub.Publish(threadID, []wire.ThreadMessage{next.userMsg}, nil, next.currentSummary)
			b.launchTurnRun(threadID, next, text)
		}
		if retryUnsolicited {
			go b.retryUnsolicitedTurn(threadID)
		}
	}()
	return batch
}

func (b *MemoryBackend) persistThreadSessionReset(threadID string) {
	if b.store != nil {
		ctx, cancel := persistCtx()
		defer cancel()
		if err := b.store.UpdateThreadSession(ctx, threadID, ""); err != nil {
			log.Printf("store reset session %s: %v", threadID, err)
		}
	}
}

// Interrupt 中断指定线程的当前回复轮次。
func (b *MemoryBackend) Interrupt(ctx context.Context, params wire.InterruptParams) (wire.InterruptResult, error) {
	b.mu.Lock()

	b.cancelAutoContinueLocked(params.ThreadID)

	td, ok := b.threads[params.ThreadID]
	if !ok {
		b.mu.Unlock()
		return wire.InterruptResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", params.ThreadID),
		}
	}

	res, batch, err := b.interruptLocked(td, params.TurnID)
	b.mu.Unlock()
	// 先解锁再广播：观察者可能在回调里重新进入后端。
	b.finishWithdraw(batch, withdrawInterrupt)
	return res, err
}

// InterruptCurrent 中断指定线程当前正在运行的这一轮，不校验 turnID；未运行时返回 already_stopped。
func (b *MemoryBackend) InterruptCurrent(ctx context.Context, threadID string) (wire.InterruptResult, error) {
	b.mu.Lock()

	b.cancelAutoContinueLocked(threadID)

	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return wire.InterruptResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}

	res, batch, err := b.interruptLocked(td, "")
	b.mu.Unlock()
	b.finishWithdraw(batch, withdrawInterrupt)
	return res, err
}

// KillRunningCommand 终止指定会话当前正在运行的那条命令的子进程树，不中断本轮对话：
// agent 会收到命令失败的结果后继续工作。只杀本轮新起的进程子树，会话开始时就存在的常驻子进程
// （MCP server 等）不受影响。没有运行中的轮次或 Provider 不支持时报 killed=0，不返回错误。
func (b *MemoryBackend) KillRunningCommand(ctx context.Context, threadID, messageID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return 0, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}
	if td.runTurnID == "" {
		b.mu.Unlock()
		return 0, nil
	}
	b.ensureMessagesLoadedLocked(td)
	// 首选用工具消息的创建时间：它就是这条命令开始跑的时刻。拿不到时退回本轮开始时间。
	since := td.runStartedAt
	if messageID != "" {
		for _, msg := range td.messages {
			if msg.ID != messageID {
				continue
			}
			if t, err := time.Parse(time.RFC3339Nano, msg.CreatedAt); err == nil {
				since = t
			}
			break
		}
	}
	rooter, _ := b.threadHarnessLocked(td.summary.Provider).(harness.ProcessRooter)
	b.mu.Unlock()

	if rooter == nil || since.IsZero() {
		return 0, nil
	}
	var (
		killed   int
		firstErr error
	)
	for _, root := range rooter.ProcessRootPIDs(threadID) {
		n, err := procs.KillTreesStartedAfter(root, since, killRunningCommandGrace)
		if err != nil {
			// 平台不支持时不报错，其余错误先记下，成功杀掉进程时不再上抛。
			if !errors.Is(err, procs.ErrNotSupported) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		killed += n
	}
	if killed == 0 && firstErr != nil {
		return 0, firstErr
	}
	return killed, nil
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

// backgroundTaskMessageLocked 查找后台任务消息，调用方持有 b.mu。
func (b *MemoryBackend) backgroundTaskMessageLocked(threadID, messageID string) (*memoryThread, int, error) {
	td, ok := b.threads[threadID]
	if !ok {
		return nil, -1, &wire.RPCError{Code: wire.ErrCodeResourceNotFound, Message: "会话不存在"}
	}
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		return nil, -1, err
	}
	b.ensureMessagesLoadedLocked(td)
	for i := range td.messages {
		if td.messages[i].ID == messageID {
			return td, i, nil
		}
	}
	return nil, -1, &wire.RPCError{Code: wire.ErrCodeResourceNotFound, Message: "消息不存在"}
}

// BackgroundTaskLog 读取后台任务日志的末尾。
func (b *MemoryBackend) BackgroundTaskLog(ctx context.Context, threadID, messageID string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	b.mu.Lock()
	td, idx, err := b.backgroundTaskMessageLocked(threadID, messageID)
	var text string
	if err == nil && td.messages[idx].ToolStatus != nil && *td.messages[idx].ToolStatus == "background" {
		text = td.messages[idx].Text
	}
	b.mu.Unlock()
	if err != nil {
		return "", false, err
	}
	_, path := backgroundTaskDetails(text)
	if !filepath.IsAbs(path) {
		return "", false, nil
	}
	path = filepath.Clean(path)
	if filepath.Base(filepath.Dir(path)) != "tasks" || !backgroundTaskFilePattern.MatchString(filepath.Base(path)) {
		return "", false, nil
	}
	validParent := false
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if strings.HasPrefix(part, "claude-") {
			validParent = true
			break
		}
	}
	if !validParent {
		return "", false, nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", true, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", true, nil
	}
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil {
		return "", false, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return "", false, nil
	}
	offset := openedInfo.Size() - backgroundTaskLogMaxBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", false, err
	}
	data, err := io.ReadAll(io.LimitReader(f, backgroundTaskLogMaxBytes))
	if err != nil {
		return "", false, err
	}
	if openedInfo.Size() > backgroundTaskLogMaxBytes {
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
			data = data[newline+1:]
		} else {
			data = nil
		}
	}
	lines := strings.Split(string(data), "\n")
	end := len(lines)
	if end > 0 && lines[end-1] == "" {
		end--
	}
	if end > backgroundTaskLogMaxLines {
		lines = lines[end-backgroundTaskLogMaxLines:]
	}
	return strings.Join(lines, "\n"), true, nil
}

// finishBackgroundTaskLocked 回写状态并使后台计数失效，调用方持有 b.mu。
func finishBackgroundTaskLocked(td *memoryThread, idx int, status, now string) {
	td.messages[idx].ToolStatus = &status
	td.messages[idx].ToolCompletedAt = &now
	td.bgCount.invalidate()
}

// StopBackgroundTask 请求 provider 停止任务并回写工具消息。
func (b *MemoryBackend) StopBackgroundTask(ctx context.Context, threadID, messageID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	b.mu.Lock()
	td, idx, err := b.backgroundTaskMessageLocked(threadID, messageID)
	if err != nil {
		b.mu.Unlock()
		return false, err
	}
	msg := td.messages[idx]
	if msg.ToolStatus == nil || *msg.ToolStatus != "background" {
		b.mu.Unlock()
		return false, errors.New("任务未在后台运行")
	}
	taskID, _ := backgroundTaskDetails(msg.Text)
	if taskID == "" {
		for id, toolID := range td.backgroundTaskTools {
			if td.backgroundTools[toolID] == messageID {
				taskID = id
				break
			}
		}
	}
	stopper, ok := b.threadHarnessLocked(td.summary.Provider).(backgroundTaskStopper)
	b.mu.Unlock()
	if taskID == "" {
		return false, errors.New("找不到后台任务 ID")
	}
	if !ok {
		return false, errors.New("当前 provider 不支持停止后台任务")
	}
	if err := stopper.StopBackgroundTask(ctx, threadID, taskID); err != nil {
		return false, err
	}
	b.mu.Lock()
	td, idx, err = b.backgroundTaskMessageLocked(threadID, messageID)
	if err != nil {
		b.mu.Unlock()
		return false, err
	}
	if td.messages[idx].ToolStatus == nil || *td.messages[idx].ToolStatus != "background" {
		b.mu.Unlock()
		return true, nil
	}
	now := formatTime(b.nowLocked())
	finishBackgroundTaskLocked(td, idx, "stopped", now)
	for toolID, id := range td.backgroundTools {
		if id == messageID {
			delete(td.backgroundTools, toolID)
			delete(td.backgroundPending, toolID)
			for id, taskToolID := range td.backgroundTaskTools {
				if taskToolID == toolID {
					delete(td.backgroundTaskTools, id)
				}
			}
		}
	}
	td.summary.UpdatedAt = now
	msg = td.messages[idx]
	messages := []wire.ThreadMessage{msg}
	b.enrichTurnMessagesLocked(td, messages)
	summary := b.summaryForPublishLocked(td)
	b.mu.Unlock()
	b.persistMessages(threadID, messages)
	b.hub.Publish(threadID, messages, nil, summary)
	return true, nil
}

// killRunningCommandGrace 是 SIGTERM 到 SIGKILL 之间的宽限期。
const killRunningCommandGrace = 3 * time.Second

// RecallTurn 收回指定会话正在运行的一轮：打断并等待空闲，从消息与数据库中删除该轮消息，将 turns 状态改为 taken_back，返回原 Prompt。
func (b *MemoryBackend) RecallTurn(ctx context.Context, threadID string) (wire.TurnRecallResult, error) {
	return b.recallTurn(ctx, threadID, false)
}

func (b *MemoryBackend) recallTurn(ctx context.Context, threadID string, draftOnly bool) (wire.TurnRecallResult, error) {
	if err := ctx.Err(); err != nil {
		return wire.TurnRecallResult{}, err
	}
	hasBees := b.hasWorkingSwarmBees(threadID)
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return wire.TurnRecallResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}
	b.ensureMessagesLoadedLocked(td)

	if td.takingBack || !td.summary.IsRunning || td.runTurnID == "" {
		b.mu.Unlock()
		return wire.TurnRecallResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: "no running turn to take back",
		}
	}

	var draftMessageID string
	if draftOnly {
		if len(td.messages) == 0 || td.pendingSend != nil || len(td.pending) > 0 {
			b.mu.Unlock()
			return wire.TurnRecallResult{}, errors.New("当前轮次不可撤回")
		}
		last := td.messages[len(td.messages)-1]
		if last.Role != "user" || last.TurnID == nil || *last.TurnID != td.runTurnID || len(last.Media) > 0 {
			b.mu.Unlock()
			return wire.TurnRecallResult{}, errors.New("当前轮次已有输出或附件，不能撤回")
		}
		draftMessageID = last.ID
	}

	if hasBees {
		b.mu.Unlock()
		return wire.TurnRecallResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: "cannot take back turn while swarm bees are working",
		}
	}

	turnID := td.runTurnID
	var userPrompt string
	var attachments []wire.MediaRef
	for i := 0; i < len(td.messages); i++ {
		if td.messages[i].TurnID != nil && *td.messages[i].TurnID == turnID && td.messages[i].Role == "user" {
			userPrompt = td.messages[i].Text
			attachments = append([]wire.MediaRef{}, td.messages[i].Media...)
			break
		}
	}

	if swarm.IsNotice(userPrompt) || swarm.IsBeePrompt(userPrompt) {
		b.mu.Unlock()
		return wire.TurnRecallResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: "cannot take back turn dispatched by swarm",
		}
	}

	td.takingBack = true
	defer func(target *memoryThread) {
		b.mu.Lock()
		target.takingBack = false
		retryUnsolicited := b.threads[threadID] == target && target.takeUnsolicitedPendingLocked()
		b.mu.Unlock()
		if retryUnsolicited {
			go b.retryUnsolicitedTurn(threadID)
		}
	}(td)
	b.cancelAutoContinueLocked(threadID)
	_, batch, err := b.interruptLocked(td, turnID)
	if err != nil {
		b.mu.Unlock()
		return wire.TurnRecallResult{}, err
	}

	var waitCh chan struct{}
	if td.phase != phaseIdle {
		waitCh = make(chan struct{})
		td.idleWaiters = append(td.idleWaiters, waitCh)
	}
	b.mu.Unlock()
	// 撤回整轮时顺手撤下的卡片只从列表消失，不额外宣称"已中断"。
	b.publishWithdrawn(batch)

	timeoutCh := make(chan struct{})
	stopTimer := b.scheduleAfter(10*time.Second, func() {
		close(timeoutCh)
	})
	defer stopTimer()
	if waitCh != nil {
		defer func(target *memoryThread) {
			b.mu.Lock()
			for i, waiter := range target.idleWaiters {
				if waiter == waitCh {
					target.idleWaiters = append(target.idleWaiters[:i], target.idleWaiters[i+1:]...)
					break
				}
			}
			b.mu.Unlock()
		}(td)
		select {
		case <-waitCh:
		case <-timeoutCh:
			return wire.TurnRecallResult{}, errors.New("timeout waiting for turn to become idle")
		case <-ctx.Done():
			return wire.TurnRecallResult{}, ctx.Err()
		}
	}

	b.mu.Lock()
	td, ok = b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return wire.TurnRecallResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}

	historyOp := td.historyOp
	b.mu.Unlock()
	if historyOp != nil {
		select {
		case <-historyOp:
		case <-timeoutCh:
			return wire.TurnRecallResult{}, errors.New("timeout waiting for turn history")
		case <-ctx.Done():
			return wire.TurnRecallResult{}, ctx.Err()
		}
	}
	b.mu.Lock()
	if b.threads[threadID] != td {
		b.mu.Unlock()
		return wire.TurnRecallResult{}, store.ErrNotFound
	}
	if draftOnly && (len(td.messages) == 0 || td.messages[len(td.messages)-1].ID != draftMessageID) {
		b.mu.Unlock()
		return wire.TurnRecallResult{}, errors.New("当前轮次已有输出，不能撤回")
	}
	if b.store != nil {
		pCtx, pCancel := persistCtx()
		err := b.store.RecallTurn(pCtx, threadID, turnID)
		pCancel()
		if err != nil {
			b.mu.Unlock()
			return wire.TurnRecallResult{}, err
		}
	}
	var removedIDs []string
	var keptMsgs []wire.ThreadMessage
	for _, m := range td.messages {
		if m.TurnID != nil && *m.TurnID == turnID {
			removedIDs = append(removedIDs, m.ID)
			delete(td.persisted, m.ID)
			delete(b.usage[threadID], m.TurnIndex)
			if m.Role == "user" && userPrompt == "" {
				userPrompt = m.Text
			}
		} else {
			keptMsgs = append(keptMsgs, m)
		}
	}
	td.messages = keptMsgs
	if td.takenBack == nil {
		td.takenBack = make(map[string]bool)
	}
	td.takenBack[turnID] = true
	td.summary.UpdatedAt = b.nowLocked().Format(time.RFC3339Nano)
	summary := b.summaryForPublishLocked(td)
	td.sessionID = ""
	td.sessionEpoch++
	// 会话被替换，转发上下文需要在新会话里重新送达。
	td.continuationSent = false
	td.needsHandoff = true
	b.mu.Unlock()

	b.persistThreadSessionReset(threadID)

	if b.hub != nil {
		b.hub.Publish(threadID, nil, removedIDs, summary)
	}

	return wire.TurnRecallResult{
		ThreadID:    threadID,
		TurnID:      turnID,
		Prompt:      userPrompt,
		Attachments: attachments,
	}, nil
}

// turnRevertTarget 描述撤销某一轮次所需的 git 环境、检查点参数与线程全部轮次记录。
type turnRevertTarget struct {
	runner *git.Runner
	root   string
	base   string
	finish string
	turns  []store.Turn
}

// resolveTurnRevert 解析撤销指定轮次所需的 git 执行器、仓库根目录、基础/完成检查点与线程全部轮次。
// base 取该轮的 BaseCheckpoint，缺失时回退到 start ref；
// finish 取最后一个非空的 FinishCheckpoint，缺失时回退到最后一轮的 end ref，再缺失时对当前工作区打临时快照。
func (b *MemoryBackend) resolveTurnRevert(ctx context.Context, threadID, turnID string) (*turnRevertTarget, error) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return nil, fmt.Errorf("thread %s not found", threadID)
	}
	if td.runTurnID != "" || td.summary.IsRunning {
		b.mu.Unlock()
		return nil, errors.New("会话正在运行，无法撤销")
	}
	// C-ST-07：撤销要按内存消息回滚历史，先确保历史已回载。
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		b.mu.Unlock()
		return nil, err
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)
	runner := b.git
	s := b.store
	b.mu.Unlock()

	if runner == nil {
		return nil, errors.New("git 不可用，无法撤销")
	}
	if s == nil {
		return nil, errors.New("store 不可用，无法撤销")
	}

	workDir, ok := b.ThreadWorkDir(threadID)
	if !ok || workDir == "" {
		return nil, fmt.Errorf("thread %s work dir not found", threadID)
	}
	root, err := runner.RepoRoot(ctx, workDir)
	if err != nil {
		return nil, fmt.Errorf("resolve repo root for thread %s: %w", threadID, err)
	}
	if root == "" {
		return nil, fmt.Errorf("thread %s work dir is not a git repository", threadID)
	}

	// 撤销会还原工作区文件：同一工作区还有别的会话在跑，或有子代理正在把改动合并进来时，
	// 先拒绝撤销，避免两边的改动互相覆盖。两处检查都在改动任何文件之前完成。
	b.mu.Lock()
	busyReason := b.revertWorkspaceBusyLocked(threadID, root)
	busyCheck := b.revertBusyCheck
	b.mu.Unlock()
	if busyReason != "" {
		return nil, errors.New(busyReason)
	}
	// 钩子由宿主注入，可能查询编排管理等其他锁，必须放在 b.mu 之外调用。
	if busyCheck != nil {
		if reason := busyCheck(threadID, root); reason != "" {
			return nil, errors.New(reason)
		}
	}

	target := &turnRevertTarget{runner: runner, root: root}

	if t, gerr := s.GetTurn(ctx, turnID); gerr == nil {
		target.base = t.BaseCheckpoint
	} else if !errors.Is(gerr, store.ErrNotFound) {
		return nil, fmt.Errorf("get turn %s: %w", turnID, gerr)
	}
	if target.base == "" {
		if sha, perr := runner.RevParse(ctx, root, git.CheckpointRef(threadID, turnID)+"/start"); perr == nil {
			target.base = sha
		}
	}
	if target.base == "" {
		return nil, fmt.Errorf("turn %s has no base checkpoint, cannot revert", turnID)
	}

	turns, err := s.ListTurns(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("list turns for thread %s: %w", threadID, err)
	}
	target.turns = turns
	for _, t := range turns {
		if t.FinishCheckpoint != "" {
			target.finish = t.FinishCheckpoint
		}
	}
	if target.finish == "" && len(turns) > 0 {
		last := turns[len(turns)-1]
		if sha, perr := runner.RevParse(ctx, root, git.CheckpointRef(threadID, last.ID)+"/end"); perr == nil {
			target.finish = sha
		}
	}
	if target.finish == "" {
		// 没有可用的结束检查点时，对当前工作区打一个临时快照作为比较基准。
		snapshotRef := git.CheckpointRef(threadID, "revert-now")
		sha, cerr := runner.CheckpointWatched(ctx, b.watcherFor(root), root, snapshotRef, "revert now")
		if cerr != nil {
			return nil, fmt.Errorf("create fallback checkpoint for thread %s: %w", threadID, cerr)
		}
		target.finish = sha
	}

	return target, nil
}

// PreviewTurnRevert 预览撤销指定轮次及其之后所有轮次的代码变更效果。
func (b *MemoryBackend) PreviewTurnRevert(ctx context.Context, threadID, turnID string) (*git.RestorePreview, error) {
	target, err := b.resolveTurnRevert(ctx, threadID, turnID)
	if err != nil {
		return nil, err
	}
	return target.runner.PreviewRestore(ctx, target.root, target.base, target.finish)
}

// ExecuteTurnRevert 执行撤销指定轮次及其之后所有轮次的代码变更，并同步回滚历史。
// 文件层面只还原自结束检查点以来未被二次修改的内容；历史层面把该轮及其之后的轮次标记为撤回，
// 删除这些轮次的消息与用量，并清空 provider 会话以便下一轮从剩余历史重建上下文。
func (b *MemoryBackend) ExecuteTurnRevert(ctx context.Context, threadID, turnID string) (*git.RestorePreview, error) {
	target, err := b.resolveTurnRevert(ctx, threadID, turnID)
	if err != nil {
		return nil, err
	}

	preview, err := target.runner.ExecuteRevert(ctx, target.root, target.base, target.finish)
	if err != nil {
		return nil, err
	}

	// 目标轮及其之后的所有轮次（ListTurns 已按 TurnIndex 升序）
	var rollbackIDs []string
	targetIndex := -1
	for _, t := range target.turns {
		if t.ID == turnID {
			targetIndex = t.TurnIndex
			break
		}
	}
	if targetIndex >= 0 {
		for _, t := range target.turns {
			if t.TurnIndex >= targetIndex {
				rollbackIDs = append(rollbackIDs, t.ID)
			}
		}
	}

	for _, id := range rollbackIDs {
		if err := b.store.RecallTurn(ctx, threadID, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return preview, fmt.Errorf("take back turn %s: %w", id, err)
		}
	}

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return preview, nil
	}
	if td.runTurnID != "" || td.summary.IsRunning {
		b.mu.Unlock()
		return preview, errors.New("会话正在运行，无法撤销")
	}
	// C-ST-07：resolveTurnRevert 放锁后会话可能又被淘汰，这里要按内存消息裁剪，需重新确保已回载。
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		b.mu.Unlock()
		return preview, err
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)
	rollbackSet := make(map[string]bool, len(rollbackIDs))
	for _, id := range rollbackIDs {
		rollbackSet[id] = true
	}
	var removedIDs []string
	keptMsgs := make([]wire.ThreadMessage, 0, len(td.messages))
	for _, m := range td.messages {
		drop := targetIndex >= 0 && (m.TurnIndex >= targetIndex || (m.TurnID != nil && rollbackSet[*m.TurnID]))
		if !drop {
			keptMsgs = append(keptMsgs, m)
			continue
		}
		removedIDs = append(removedIDs, m.ID)
		delete(td.persisted, m.ID)
		delete(b.usage[threadID], m.TurnIndex)
	}
	td.messages = keptMsgs
	if len(rollbackIDs) > 0 {
		if td.takenBack == nil {
			td.takenBack = make(map[string]bool)
		}
		for _, id := range rollbackIDs {
			td.takenBack[id] = true
		}
		// provider 会话里仍保留被回滚的历史，清空会话以便下一轮从剩余历史重建。
		td.sessionID = ""
		td.sessionEpoch++
		// 会话被替换，转发上下文需要在新会话里重新送达。
		td.continuationSent = false
		td.needsHandoff = true
	}
	td.summary.UpdatedAt = b.nowLocked().Format(time.RFC3339Nano)
	summary := b.summaryForPublishLocked(td)
	b.mu.Unlock()

	if len(rollbackIDs) > 0 {
		b.persistThreadSessionReset(threadID)
	}
	if b.hub != nil {
		b.hub.Publish(threadID, nil, removedIDs, summary)
	}

	return preview, nil
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
	for _, marker := range []string{"signal: segmentation fault", "signal: killed", "process exited", "app-server exited", "codex turn incomplete", "process closed connection unexpectedly", "sigsegv", "out of memory"} {
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

// finishTurnEarly 收尾轮末早退分支：仅当该线程没有其他轮次在跑时恢复空闲、发布空闲摘要，
// 并按正常路径的条件启动下一条排队消息，避免 IsRunning 永久卡在 true、跟进队列永不出队。
func (b *MemoryBackend) finishTurnEarly(td *memoryThread, threadID, turnID string, turnIndex int, isNormal, isInterrupted bool) {
	status := "failed"
	if isInterrupted {
		status = "interrupted"
	} else if isNormal {
		status = "completed"
	}
	b.updateTurnFinish(threadID, turnID, turnIndex, status, "")
	b.mu.Lock()
	if td == nil || b.threads[threadID] != td {
		b.mu.Unlock()
		return
	}
	// 该线程已有别的轮次在跑（例如运行中切换 Provider 后新 epoch 启动的新轮次），不能置空闲。
	if td.runTurnID != "" && td.runTurnID != turnID {
		var msgs []wire.ThreadMessage
		for _, msg := range td.messages {
			if msg.TurnID != nil && *msg.TurnID == turnID {
				msgs = append(msgs, msg)
			}
		}
		summary := b.summaryForPublishLocked(td)
		b.mu.Unlock()
		b.hub.Publish(threadID, msgs, nil, summary)
		b.hub.Flush(threadID)
		return
	}
	if td.watchdogStop != nil {
		td.watchdogStop()
		td.watchdogStop = nil
	}
	if td.runCancel != nil {
		td.runCancel()
		td.runCancel = nil
	}
	td.runTurnID = ""
	td.summary.LastActivityText = nil
	td.summary.UpdatedAt = b.nowLocked().Format(time.RFC3339Nano)
	td.setIdleLocked()
	retryUnsolicited := td.takeUnsolicitedPendingLocked()
	if isNormal || isInterrupted {
		td.summary.Attention = "ready"
	} else {
		td.summary.Attention = "failed"
	}
	finalSummary := b.summaryForPublishLocked(td)
	var finalMsgs []wire.ThreadMessage
	for _, msg := range td.messages {
		if msg.TurnID != nil && *msg.TurnID == turnID {
			finalMsgs = append(finalMsgs, msg)
		}
	}
	b.mu.Unlock()
	b.hub.Publish(threadID, finalMsgs, nil, finalSummary)
	b.hub.Flush(threadID)

	hasBees := b.hasWorkingSwarmBees(threadID)
	b.mu.Lock()
	var (
		nextStarted  *startedTurn
		nextText     string
		queueChanged bool
	)
	// 宿主关闭时保留队列不动，与正常结束路径一致。
	if b.threads[threadID] == td && b.ctx.Err() == nil {
		nextStarted, nextText, queueChanged = b.startNextQueuedTurnLocked(td, isNormal && !hasBees, isNormal)
	}
	b.mu.Unlock()

	if nextStarted != nil {
		b.hub.Flush(threadID)
		// 正文先落库再删队列行，崩溃时最多重复出现一条已写正文的排队项，不会丢消息。
		b.persistMessages(threadID, []wire.ThreadMessage{nextStarted.userMsg})
		b.recordDispatchingForTurn(threadID, nextStarted)
	}
	if queueChanged {
		b.commitQueueChange(threadID)
	}
	if nextStarted != nil {
		if nextStarted.titleChanged {
			b.persistThreadTitle(threadID, nextStarted.currentSummary.Title)
		}
		b.hub.BeginRun(threadID)
		b.hub.Publish(threadID, []wire.ThreadMessage{nextStarted.userMsg}, nil, nextStarted.currentSummary)
		b.launchTurnRun(threadID, nextStarted, nextText)
	}
	if retryUnsolicited {
		go b.retryUnsolicitedTurn(threadID)
	}
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

// interactionModeOverride/effortOverride 非空时覆盖会话偏好，仅作用于本轮。
// imageNames 与 imagePaths 等长且同序，元素可能为空串（无原始文件名）。
func (b *MemoryBackend) runHarness(runCtx context.Context, threadID string, turnID string, userText string, turnIndex int, imagePaths []string, imageNames []string, interactionMode, effort string) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok || td.runTurnID != turnID || runCtx.Err() != nil {
		// runTurnID 已被清空说明没有任何轮次在跑：本轮无人接管，而 startTurnLocked 已置
		// IsRunning，必须补上空闲收尾。本轮 runCtx 已取消但 runTurnID 仍指向本轮时，
		// 中断路径只挂了 watchdog 超时兜底，也要就地收尾，避免线程卡在运行中。
		// finishTurnEarly 自带守卫，另有轮次在跑时会跳过。
		b.mu.Unlock()
		b.finishTurnEarly(td, threadID, turnID, turnIndex, false, runCtx.Err() != nil)
		return
	}
	recordedEpoch := td.sessionEpoch
	// 本轮由排队项出队而来时记下投递行标识；accepted/failed 的迁移在 emit 与收尾处触发。
	deliveryMessageID, deliveryFollowUpID := "", ""
	if td.deliveryTurnID == turnID {
		deliveryMessageID, deliveryFollowUpID = td.deliveryMessageID, td.deliveryFollowUpID
	}
	// attachTurnID 指向本轮时，说明这一轮要接住常驻进程自发跑出的一段输出（不发用户帧）。
	attachTurn := td.attachTurnID == turnID
	// 原生 /compact 必须原样送到 Provider：本轮用户文本去掉首尾空白后恰为该命令时，下面所有装饰
	// （人设前缀、mention/continuation/plan 前缀、附件说明、guidance、decorator、ConciseRepliesPrompt）
	// 一律跳过，否则 Provider 认不出这个斜杠命令。
	compactOnly := strings.TrimSpace(userText) == compactCommandText
	// 本轮快速模式与速率档位取自会话摘要，与 effort 一样在启动锁内固定。
	fastMode := td.summary.FastMode
	speedTier := derefString(td.summary.SpeedTier)
	transient := isHelperThread(td.summary)
	h := b.threadHarnessLocked(td.summary.Provider)
	workDir := b.harnessWorkDir
	turnMode := harness.PermissionMode(td.summary.RuntimeMode)
	if b.harnessMode == harness.ModeReadOnly || b.harnessMode == "readonly" {
		turnMode = harness.ModeReadOnly
	} else if turnMode == "" {
		turnMode = harness.PermissionMode(b.defaultRuntimeModeLocked())
	}
	if !ok || h == nil {
		b.mu.Unlock()
		// 没有可用 harness 等于这一轮没交给 provider：投递行直接记为 failed，不留在 dispatching。
		b.markDeliveryFailed(threadID, turnID, deliveryMessageID, deliveryFollowUpID, "provider 未就绪")
		// 找不到 harness 也要走标准收尾：由 finishTurnEarly 取消本轮、清 runTurnID 并置空闲，
		// 否则 IsRunning 会永久为 true 且缺少空闲摘要。
		b.finishTurnEarly(td, threadID, turnID, turnIndex, false, false)
		return
	}
	if td.workDir != "" {
		workDir = td.workDir
	} else if proj, ok := b.projects[td.summary.ProjectID]; ok && proj.Path != "" {
		workDir = proj.Path
	}
	sessionID := td.sessionID
	needsHandoff := td.needsHandoff
	// 会话文件已丢失时（例如本地会话被清理），把可见历史交接进本轮提示词，避免模型失去上下文。
	// 只影响本轮，不写回线程状态；实现者本轮重建会话后，下一轮就不会再交接。
	if !needsHandoff && sessionID != "" {
		if checker, ok := h.(harness.SessionChecker); ok && !checker.SessionExists(sessionID) {
			needsHandoff = true
		}
	}
	// C-ST-07：本轮启动前确保历史消息在内存中（已归档的空闲会话可能已被淘汰）。
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		log.Printf("ensure messages for thread %s error: %v", threadID, err)
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)
	// 记录本轮开始时刻，供「终止运行中的命令」排除本轮之前就已启动的常驻子进程。
	td.runStartedAt = b.nowLocked()
	var priorHistory []wire.ThreadMessage
	var mentions []mentionedThread
	for _, msg := range td.messages {
		if msg.TurnID == nil || *msg.TurnID != turnID {
			priorHistory = append(priorHistory, msg)
		}
	}
	for id, other := range b.threads {
		// compact 轮次要原样送达命令，不拼 mention 前缀，也就不必扫其它线程。
		if !compactOnly && id != threadID && other.summary.ProjectID == td.summary.ProjectID &&
			(strings.Contains(userText, "@"+other.summary.Title) || strings.Contains(userText, "@"+id)) {
			if err := b.ensureMessagesLocked(other, id); err != nil {
				log.Printf("ensure messages for mentioned thread %s error: %v", id, err)
				continue
			}
			b.ensureMessagesLoadedLocked(other)
			b.touchThreadLocked(other)
			mentions = append(mentions, mentionedThread{ID: id, Title: other.summary.Title, Messages: append([]wire.ThreadMessage(nil), other.messages...)})
		}
	}
	sort.Slice(mentions, func(i, j int) bool { return mentions[i].ID < mentions[j].ID })
	var model string
	if ok && td.summary.Model != nil {
		model = *td.summary.Model
	}
	// Plan 回合按只读跑，但记下用户选的档位：计划获批后 provider 要切回它，否则这一轮剩下的操作会逐个审批。
	var planExitMode harness.PermissionMode
	if interactionMode == "plan" {
		planExitMode = turnMode
		turnMode = harness.ModeReadOnly
	}
	var continuationText string
	if ok && td.continuation != "" && !td.continuationSent {
		continuationText = td.continuation
	}
	gitRunner := b.git
	decorator := b.promptDecorator
	// 人设解析器与线程当前人设、provider 快照在锁内取出；解析调用放到解锁之后，避免持锁调用外部函数。
	personaResolver := b.personaTag
	personaID := td.summary.Persona
	personaProvider := td.summary.Provider
	if personaProvider == "" {
		personaProvider = b.defaultProvider
	}
	guidanceBuilder := b.guidanceBuilder
	if guidanceBuilder == nil {
		// 派发给子代理（bee）的线程带精简技能目录，其余线程带全量技能清单。
		if swarm.IsBeePrompt(userText) {
			guidanceBuilder = b.beeGuidanceBuilder
		} else {
			guidanceBuilder = b.defaultGuidanceBuilder
		}
	}
	// 首轮额外段落（普通会话的移交提示）与指导词一起只注入一次。
	firstTurnExtra := b.firstTurnExtra

	// 只有本轮本要续接 provider 会话、或本线程已分割待生效时才询问换新钩子；原生 /compact 要压缩的正是旧会话，不参与。
	// priorHistory 已排除本轮消息（按 TurnID 过滤），所以这里的待分割判定看的是本轮之前的状态。
	splitPending := !compactOnly && pendingSplit(priorHistory)
	var rotator SessionRotator
	var rotationInfo SessionRotationInfo
	if b.sessionRotator != nil && (sessionID != "" || splitPending) && !compactOnly {
		rotator = b.sessionRotator
		rotationInfo = b.sessionRotationInfoLocked(threadID, turnIndex, priorHistory)
		rotationInfo.Manual = splitPending
	}
	b.mu.Unlock()

	// pi 直连后线程里残留的注入模型已失效：回写为默认，界面标签与实际使用的模型一致。
	if personaProvider == "pi" && model != "" && model != "default" && isPiStaleModel(model) {
		model = "default"
		b.resetThreadModel(threadID, model)
	}

	// 总管主会话与话题会话把已注册项目目录当附加目录，普通会话为空。
	// 函数自己读项目表，必须在主锁之外算。

	// 钩子会读设置，必须在锁外调用；超时或 panic 一律视为不换新。
	rotated := false
	rotationPreamble := ""
	if rotator != nil {
		if rotate, preamble := callSessionRotator(runCtx, rotator, rotationInfo); rotate {
			if extra, ok := b.rotateProviderSession(td, threadID, turnID, sessionID); ok {
				rotated = true
				rotationPreamble = strings.TrimSpace(preamble)
				sessionID = ""
				if continuationText == "" {
					continuationText = extra
				}
				if !rotationInfo.Manual {
					// 手动分割的那条可见消息由 RequestSplit 自己写，不再冒出一条自动换新提示。
					_ = b.AppendNotice(threadID, sessionRotationNotice(rotationInfo))
				}
			}
		}
	}

	var (
		repoRoot string
		gitRun   *git.Runner
	)
	if gitRunner != nil && workDir != "" {
		if root, err := gitRunner.RepoRoot(runCtx, workDir); err == nil && root != "" {
			repoRoot = root
			gitRun = gitRunner
			startRef := git.CheckpointRef(threadID, turnID) + "/start"
			if _, cpErr := gitRun.CheckpointWatched(runCtx, b.watcherFor(repoRoot), repoRoot, startRef, "turn "+turnID+" start"); cpErr != nil {
				log.Printf("checkpoint start failed for thread %s turn %s: %v", threadID, turnID, cpErr)
			}
		}
	}

	// 人设标签前缀只拼在发给 harness 的 Turn.Prompt 最前面：不写进 harnessPrompt，
	// 以免污染时间线里的用户消息、标题生成与搜索。
	personaPrefix := ""
	if !compactOnly && personaResolver != nil && personaID != "" {
		personaPrefix = personaResolver(personaProvider, model, personaID)
	}

	harnessPrompt := userText
	if compactOnly {
		harnessPrompt = compactCommandText
	}
	if rotated && !rotationInfo.Manual {
		// 换新会话只带最近几条对话原文，不叠加整段历史交接；前言在 guidance 注入后补到最前面。
		// 历史先去掉分割线以上的部分，避免分割后又被自动换新把旧内容回放回去。
		harnessPrompt = buildRecentConversationPrompt(historyAfterLastSplit(priorHistory), harnessPrompt)
	} else if !rotated && !compactOnly && !rotationInfo.Manual && needsHandoff && len(priorHistory) > 0 {
		harnessPrompt = buildProviderHandoffPrompt(historyAfterLastSplit(priorHistory), harnessPrompt)
	}
	if contextText := buildMentionContext(mentions); !compactOnly && contextText != "" {
		harnessPrompt = contextText + "\n\n" + harnessPrompt
	}
	if !compactOnly && continuationText != "" {
		harnessPrompt = continuationText + "\n\n" + harnessPrompt
	}
	if !compactOnly && interactionMode == "plan" {
		harnessPrompt = "当前为 Plan 模式：仅分析并制定计划，不修改文件、不执行写操作、不派发执行型子任务。\n\n" + harnessPrompt
	}
	if !compactOnly && len(imagePaths) > 0 {
		var filePrompt strings.Builder
		filePrompt.WriteString("\n\n用户附带了以下本地文件附件，可使用文件读取工具查看：")
		for i, filePath := range imagePaths {
			filePrompt.WriteString("\n- ")
			// 附件不止图片（PDF、docx、xlsx 等），带上原始文件名，先让 agent 判断类型再读文件。
			name := ""
			if i < len(imageNames) {
				name = attachmentPromptName(imageNames[i], filePath)
			}
			if name != "" {
				filePrompt.WriteString(name)
				filePrompt.WriteString("：")
			}
			filePrompt.WriteString(filePath)
		}
		harnessPrompt += filePrompt.String()
	}

	// 首轮注入项目 Context 与已启用技能：仅普通消息、未交付过、且该线程尚未有
	// 已完成回复时前置；交付只在本轮运行成功后记录，失败或中断的下一条普通消息会重试。
	guidanceInjected := false
	// 换新后的 provider 会话里没有首轮指导词，不论历史与交付状态都重新注入。
	if !compactOnly && workDir != "" && guidanceBuilder != nil &&
		(rotated || (isGuidanceTriggerText(userText) && b.guidancePending(threadID, priorHistory))) {
		guidance := guidanceBuilder(workDir)
		// 额外的首轮段落（普通会话的「可以交给总管」提示）同样只在首轮出现：
		// 与指导词拼成一段前置，后续轮次不再带，也不会随历史累积。
		extra := ""
		if firstTurnExtra != nil {
			extra = firstTurnExtra(threadID)
		}
		parts := make([]string, 0, 2)
		for _, part := range []string{guidance, extra} {
			if strings.TrimSpace(part) != "" {
				parts = append(parts, part)
			}
		}
		if merged := strings.Join(parts, "\n\n"); merged != "" {
			harnessPrompt = merged + "\n\n" + harnessPrompt
			guidanceInjected = true
		}
	}
	if rotated && rotationPreamble != "" {
		harnessPrompt = rotationPreamble + "\n\n" + harnessPrompt
	}

	// attach 轮不发用户帧，装饰器里带状态的段落（运维提醒等）不能在这里就算「已投递」。
	if !compactOnly && !attachTurn && decorator != nil {
		harnessPrompt = decorator(threadID, harnessPrompt)
	}
	if !compactOnly && b.generalSettings().Session.ConciseReplies() {
		harnessPrompt = ConciseRepliesPrompt + "\n\n" + harnessPrompt
	}

	var (
		currPrompt          = harnessPrompt
		currSessionID       = sessionID
		lostSessionRetried  = false
		crashedRetried      = false
		transientRetryCount = 0

		runErr             error
		receivedErrorEvent bool
		errorEventText     string
		errorEventKind     string
		finalErrText       string
		finalErrKind       string
	)

	// 出队轮次的投递状态每轮只迁移一次：显式回执或首个上游事件把 dispatching 迁到 accepted。
	deliverySettled := deliveryMessageID == ""

	// 本轮开始运行的时刻，用于给上报的用量附上耗时（界面据此算输出速度）。
	turnStartedAt := b.now()
	// attach 只在第一次尝试里生效：那段输出已经读走，重试不该再当 attach 轮。
	firstAttempt := true
	home, _ := os.UserHomeDir()
	readOnlyDirs := projctx.SkillReadDirs(workDir, home)
	// 只有看板任务的执行线程（tasks.thread_id 等于当前线程）才加硬锁；普通交互会话不受影响。
	hardLocked := false
	hardLockOverride := ""
	if b.store != nil {
		if task, err := b.store.TaskByThreadID(runCtx, threadID); err == nil {
			hardLocked = true
			hardLockOverride = taskGateOverride(task.Meta)
		}
	}
	for {
		if runCtx.Err() != nil {
			break
		}

		confirmsDelivery := false
		if dc, ok := h.(harness.DeliveryConfirmer); ok && dc.ConfirmsApprovalDelivery() {
			confirmsDelivery = true
		}

		env := harness.NonInteractiveEnv()
		env = append(env, b.turnMemoryEnv(workDir)...)

		approver := harness.Approver(&threadApprover{b: b, threadID: threadID, turnID: turnID, sessionEpoch: recordedEpoch, confirmsDelivery: confirmsDelivery})
		if hardLocked {
			approver = &hardLockApprover{inner: approver, b: b, threadID: threadID, hardLocked: hardLocked, override: hardLockOverride}
		}

		turn := harness.Turn{
			ID:               turnID,
			ThreadID:         threadID,
			Prompt:           harnessTurnPrompt(compactOnly, personaPrefix, currPrompt),
			WorkDir:          workDir,
			ReadOnlyDirs:     readOnlyDirs,
			SessionID:        currSessionID,
			Model:            model,
			Mode:             turnMode,
			HardLocked:       hardLocked,
			HardLockOverride: hardLockOverride,
			Approver:         approver,
			Images:           imagePaths,
			InteractionMode:  interactionMode,
			PlanExitMode:     planExitMode,
			Effort:           effort,
			FastMode:         fastMode,
			SpeedTier:        speedTier,
			Transient:        transient,
			// 让 CLI 派生的命令在非交互环境下失败而不是等待 stdin、编辑器或分页器。
			// 同时按项目目录注入 Nowledge Mem 的 NMEM_SPACE（仅当同名空间已存在，本地记忆后端跳过），
			// 避免各项目记忆混在 Default。
			Env: env,
		}
		// attach 轮不发用户帧，只接住常驻进程自发输出的那一段。
		turn.Attach = attachTurn && firstAttempt

		var (
			currentTextMsgID      string
			currentReasoningMsgID string
			toolMsgIDs            = make(map[string]string)
			// toolStreams 收集各工具条目运行期间的增量输出，同一 ToolID 的片段先合并节流再下发。
			toolStreams     = make(map[string]*toolOutputStream)
			streamResetSent bool
		)
		receivedErrorEvent = false
		errorEventText = ""
		errorEventKind = ""

		attemptEpoch := recordedEpoch
		var emitMu sync.Mutex
		acceptEvents := true
		emit := func(e harness.Event) {
			emitMu.Lock()
			defer emitMu.Unlock()
			if !acceptEvents {
				return
			}

			// 出队轮次的投递状态：显式回执或首个上游事件到达表示 provider 已收下 prompt。
			// 失败要等本轮重试耗尽后再由收尾处判定，避免首次报错后重试成功却被记成 failed。
			upstream := e.Type == harness.EventText || e.Type == harness.EventReasoning ||
				e.Type == harness.EventToolStart || e.Type == harness.EventSession
			if !deliverySettled && (e.Type == harness.EventTurnAccepted || upstream) {
				deliverySettled = true
				b.markDeliveryAccepted(threadID, turnID, deliveryMessageID, deliveryFollowUpID)
			}

			// 本轮首个事件到达时先重置助手流式缓冲；事件序列由 emitMu 串行，重置一定在首个正文增量之前。
			if !streamResetSent {
				streamResetSent = true
				var (
					resetHook      func(threadID, projectID string, reset bool, delta string)
					resetProjectID string
				)
				b.mu.Lock()
				if cur, ok := b.threads[threadID]; ok && cur.sessionEpoch == attemptEpoch && cur.runTurnID == turnID {
					resetHook = b.assistantStreamHook
					resetProjectID = cur.summary.ProjectID
				}
				b.mu.Unlock()
				if resetHook != nil {
					resetHook(threadID, resetProjectID, true, "")
				}
			}

			var (
				msgToPublish   []wire.ThreadMessage
				currentSummary wire.ThreadSummary
				streamHook     func(threadID, projectID string, reset bool, delta string)
				streamDelta    string
				streamProject  string
				// bgPersistMsgs 是后台任务事件改写的工具消息：它们可能属于更早回合，回合末的批量落库
				// 按 TurnIndex 筛不到它们，必须解锁后单独写穿到 store。
				bgPersistMsgs []wire.ThreadMessage
			)

			b.mu.Lock()
			td, ok := b.threads[threadID]
			if !ok || td.sessionEpoch != attemptEpoch || td.runTurnID != turnID {
				b.mu.Unlock()
				return
			}
			now := time.Now().UTC().Format(time.RFC3339Nano)

			switch e.Type {
			case harness.EventSession:
				changed := false
				if e.SessionID != "" && td.sessionID != e.SessionID {
					td.sessionID = e.SessionID
					currSessionID = e.SessionID
					changed = true
				}
				s := b.store
				sessID := td.sessionID
				b.mu.Unlock()

				if changed && s != nil {
					pCtx, pCancel := persistCtx()
					if err := s.UpdateThreadSession(pCtx, threadID, sessID); err != nil {
						log.Printf("store update thread session %s error: %v", threadID, err)
					}
					pCancel()
				}
				return

			case harness.EventText:
				// C-RT-20 排版清洗：增量里的破折号先替换为普通连字符，再写入消息与流式缓冲。
				text := withoutEmDashes(e.Text)
				kind := "assistant"
				needNew := currentTextMsgID == ""
				if !needNew && len(td.messages) > 0 {
					lastMsg := td.messages[len(td.messages)-1]
					if lastMsg.Role == "tool" || (lastMsg.Kind != nil && *lastMsg.Kind == "tool") {
						needNew = true
					}
				}

				if needNew {
					// C-RT-21：新建消息时跳过前导空增量——不建消息、不发布、不触发流式钩子，
					// 也不更新 LastActivityText；首个非空增量去掉前导换行后再建消息。
					// 已存在消息上的追加增量不做过滤。
					if isBlankDelta(text) {
						b.mu.Unlock()
						return
					}
					text = strings.TrimLeft(text, "\r\n")
					currentTextMsgID = newUUID()
					msg := wire.ThreadMessage{
						ID:        currentTextMsgID,
						Role:      "assistant",
						Text:      text,
						CreatedAt: now,
						TurnIndex: turnIndex,
						TurnID:    &turnID,
						Kind:      &kind,
					}
					td.messages = append(td.messages, msg)
					msgToPublish = []wire.ThreadMessage{msg}
				} else {
					found := false
					for idx, m := range td.messages {
						if m.ID == currentTextMsgID {
							td.messages[idx].Text += text
							msgToPublish = []wire.ThreadMessage{td.messages[idx]}
							found = true
							break
						}
					}
					if !found {
						currentTextMsgID = newUUID()
						msg := wire.ThreadMessage{
							ID:        currentTextMsgID,
							Role:      "assistant",
							Text:      text,
							CreatedAt: now,
							TurnIndex: turnIndex,
							TurnID:    &turnID,
							Kind:      &kind,
						}
						td.messages = append(td.messages, msg)
						msgToPublish = []wire.ThreadMessage{msg}
					}
				}
				actText := "Generating..."
				td.summary.LastActivityText = &actText
				td.summary.UpdatedAt = now
				currentSummary = b.summaryForPublishLocked(td)
				// EventText 携带的是增量文本（各 Provider 均按 delta 派发），清洗后透传给流式钩子。
				streamHook = b.assistantStreamHook
				streamDelta = text
				streamProject = td.summary.ProjectID

			case harness.EventTextRetract:
				// 上游流中断、残文没被确认：从当前正文消息末尾裁掉这段（写入时做过同样的破折号清洗），
				// 裁完只剩空白就整条移除。新建消息时去掉过前导换行，所以消息全文是残文后缀时也算整条残文。
				retracted := withoutEmDashes(e.Text)
				var removedIDs []string
				for idx, m := range td.messages {
					if currentTextMsgID == "" || m.ID != currentTextMsgID {
						continue
					}
					rest, ok := strings.CutSuffix(m.Text, retracted)
					if !ok {
						if !strings.HasSuffix(retracted, m.Text) {
							log.Printf("thread %s: retracted text is not the tail of message %s, keep it", threadID, m.ID)
							break
						}
						rest = ""
					}
					if isBlankDelta(rest) {
						td.messages = append(td.messages[:idx], td.messages[idx+1:]...)
						removedIDs = []string{currentTextMsgID}
						currentTextMsgID = ""
					} else {
						td.messages[idx].Text = rest
						msgToPublish = []wire.ThreadMessage{td.messages[idx]}
					}
					break
				}
				if len(removedIDs) == 0 && len(msgToPublish) == 0 {
					b.mu.Unlock()
					return
				}
				td.summary.UpdatedAt = now
				b.enrichTurnMessagesLocked(td, msgToPublish)
				currentSummary = b.summaryForPublishLocked(td)
				b.mu.Unlock()
				b.hub.Publish(threadID, msgToPublish, removedIDs, currentSummary)
				return

			case harness.EventReasoning:
				kind := "thinking"
				needNew := currentReasoningMsgID == ""
				if !needNew && len(td.messages) > 0 {
					lastMsg := td.messages[len(td.messages)-1]
					if lastMsg.Role == "tool" || (lastMsg.Kind != nil && *lastMsg.Kind == "tool") {
						needNew = true
					}
				}

				if needNew {
					currentReasoningMsgID = newUUID()
					msg := wire.ThreadMessage{
						ID:        currentReasoningMsgID,
						Role:      "assistant",
						Text:      e.Text,
						CreatedAt: now,
						TurnIndex: turnIndex,
						TurnID:    &turnID,
						Kind:      &kind,
					}
					td.messages = append(td.messages, msg)
					msgToPublish = []wire.ThreadMessage{msg}
				} else {
					found := false
					for idx, m := range td.messages {
						if m.ID == currentReasoningMsgID {
							td.messages[idx].Text += e.Text
							msgToPublish = []wire.ThreadMessage{td.messages[idx]}
							found = true
							break
						}
					}
					if !found {
						currentReasoningMsgID = newUUID()
						msg := wire.ThreadMessage{
							ID:        currentReasoningMsgID,
							Role:      "assistant",
							Text:      e.Text,
							CreatedAt: now,
							TurnIndex: turnIndex,
							TurnID:    &turnID,
							Kind:      &kind,
						}
						td.messages = append(td.messages, msg)
						msgToPublish = []wire.ThreadMessage{msg}
					}
				}
				actText := "Thinking..."
				td.summary.LastActivityText = &actText
				td.summary.UpdatedAt = now
				currentSummary = b.summaryForPublishLocked(td)

			case harness.EventContextCompacted:
				// 上下文压缩是独立边界：先结束正在累积的正文与思考段，再落一条独立系统消息，
				// 后面的正文与思考各自新开一条消息（否则边界会把上一段正文粘到一起）。
				currentTextMsgID = ""
				currentReasoningMsgID = ""
				msg, summary := b.appendSystemMessageLocked(td, contextCompactedKind,
					contextCompactedText(e.CompactTokensBefore, e.CompactTokensAfter), b.nowLocked())
				msgToPublish = []wire.ThreadMessage{msg}
				currentSummary = summary

			case harness.EventToolStart:
				currentTextMsgID = "" // 结束当前正文与思考消息，使工具调用后的正文和思考都新开一条
				currentReasoningMsgID = ""
				toolText := e.ToolName
				if len(e.Input) > 0 {
					inputSummary := string(e.Input)
					if runes := []rune(inputSummary); len(runes) > 200 {
						inputSummary = string(runes[:200])
					}
					if inputSummary != "" {
						toolText += " " + inputSummary
					}
				}
				var toolName *string
				if e.ToolName != "" {
					tn := e.ToolName
					toolName = &tn
				}
				var toolInput *string
				if len(e.Input) > 0 {
					inp := string(e.Input)
					if runes := []rune(inp); len(runes) > 16000 {
						inp = string(runes[:16000])
					}
					toolInput = &inp
				}
				toolStatus := "running"
				toolMsgID := newUUID()
				toolMsgIDs[e.ToolID] = toolMsgID
				toolStreams[e.ToolID] = newToolOutputStream(toolText)
				kind := "tool"
				msg := wire.ThreadMessage{
					ID:         toolMsgID,
					Role:       "tool",
					Text:       toolText,
					CreatedAt:  now,
					TurnIndex:  turnIndex,
					TurnID:     &turnID,
					Kind:       &kind,
					ToolName:   toolName,
					ToolInput:  toolInput,
					ToolStatus: &toolStatus,
				}
				td.messages = append(td.messages, msg)
				msgToPublish = []wire.ThreadMessage{msg}

				// 记录写文件类工具触碰的路径，供运行中轮次的"触碰文件"查询使用。
				if touched := touchedPathsFromToolEvent(e.ToolName, e.Input); len(touched) > 0 {
					td.touchedPaths = append(td.touchedPaths, touched...)
					td.lastFileChangeAt = b.nowLocked()
				}

				// 记录工具读取的文件路径，供 Swarm 报告可信度比对。
				threadWorkDir, _ := b.threadWorkDirLocked(threadID)
				for _, readPath := range extractReadPaths(e.ToolName, e.Input, threadWorkDir) {
					b.recordReadPath(threadID, readPath)
				}

				actText := "Running " + e.ToolName
				td.summary.LastActivityText = &actText
				td.summary.UpdatedAt = now
				currentSummary = b.summaryForPublishLocked(td)

			case harness.EventToolOutput:
				// 运行中的增量输出：按 tool_id 追加到对应条目上，同一 ToolID 的片段先合并节流再下发；
				// 找不到对应条目（tool_start 还没到或条目已结束）时静默忽略。
				stream, streamed := toolStreams[e.ToolID]
				if !streamed {
					b.mu.Unlock()
					return
				}
				nowTime := b.nowLocked()
				if !stream.append(e.Text, nowTime) {
					b.mu.Unlock()
					return
				}
				toolMsgID := toolMsgIDs[e.ToolID]
				for idx, m := range td.messages {
					if m.ID == toolMsgID {
						stream.markFlushed(nowTime)
						td.messages[idx].Text = stream.render()
						msgToPublish = []wire.ThreadMessage{td.messages[idx]}
						break
					}
				}
				if len(msgToPublish) == 0 {
					b.mu.Unlock()
					return
				}
				td.summary.UpdatedAt = now
				currentSummary = b.summaryForPublishLocked(td)

			case harness.EventToolEnd:
				toolMsgID, exists := toolMsgIDs[e.ToolID]
				if exists {
					for idx, m := range td.messages {
						if m.ID == toolMsgID {
							out := e.Output
							if runes := []rune(out); len(runes) > toolOutputStreamMaxRunes {
								out = string(runes[:toolOutputStreamMaxRunes])
							}
							// 运行期间已流出的增量只是过程展示：tool_end 带来最终 output 时用它覆盖，
							// 避免 provider 既推增量又重复一次全量时出现两遍文本；没有最终 output 时保留增量。
							if stream, streamed := toolStreams[e.ToolID]; streamed {
								td.messages[idx].Text = stream.override(out)
							} else if out != "" {
								if td.messages[idx].Text != "" {
									td.messages[idx].Text += "\n" + out
								} else {
									td.messages[idx].Text = out
								}
							}
							// blocked 表示工具被 hook 拦截（或改道），不是工具自身故障：正文不拼 [failed]，状态单列为 blocked。
							if e.IsError && !e.Blocked {
								td.messages[idx].Text += "\n[failed]"
							}
							status := "completed"
							if e.IsError && e.Blocked {
								status = "blocked"
							} else if e.IsError {
								status = "failed"
							}
							// task_started 早于 tool_result 到达时，这里还判不出后台状态：改成 background
							// 且不设完成时间，等 task_notification 按 tool_use_id 回写最终状态。
							background := false
							if !e.IsError && td.backgroundPending[e.ToolID] {
								status = "background"
								background = true
								delete(td.backgroundPending, e.ToolID)
								if td.backgroundTools == nil {
									td.backgroundTools = make(map[string]string)
								}
								td.backgroundTools[e.ToolID] = td.messages[idx].ID
							}
							// 工具自身出错就不会转成后台任务：清掉 pending 与索引，避免残留到后续事件。
							if e.IsError {
								delete(td.backgroundPending, e.ToolID)
								delete(td.backgroundTools, e.ToolID)
							}
							td.messages[idx].ToolStatus = &status
							// 摘要里的后台任务计数按工具状态统计，状态改写后要重新算。
							td.bgCount.invalidate()
							if background {
								td.messages[idx].ToolCompletedAt = nil
							} else {
								completedAt := now
								td.messages[idx].ToolCompletedAt = &completedAt
							}
							msgToPublish = []wire.ThreadMessage{td.messages[idx]}
							break
						}
					}
				}
				// 条目已落定，不再需要增量缓冲。
				delete(toolStreams, e.ToolID)
				td.summary.UpdatedAt = now
				currentSummary = b.summaryForPublishLocked(td)

			case harness.EventBackgroundTaskStart:
				// provider 托管的后台任务开始：CLI 通常先发 tool_result（工具消息已是 completed），
				// 后发 task_started。把发起它的工具消息改成 background 并清空完成时间；tool_end
				// 还没到时先记 pending，等 tool_end 按 background 落定。
				toolMsgID, known := td.backgroundTools[e.ToolID]
				if !known {
					toolMsgID = toolMsgIDs[e.ToolID]
				}
				idx := -1
				for i, m := range td.messages {
					if m.ID == toolMsgID {
						idx = i
						break
					}
				}
				if toolMsgID == "" || idx < 0 {
					// 找不到对应的工具条目（例如 tool_start 已被淘汰）时静默忽略。
					b.mu.Unlock()
					return
				}
				if td.backgroundTools == nil {
					td.backgroundTools = make(map[string]string)
				}
				td.backgroundTools[e.ToolID] = toolMsgID
				if e.TaskID != "" {
					if td.backgroundTaskTools == nil {
						td.backgroundTaskTools = make(map[string]string)
					}
					td.backgroundTaskTools[e.TaskID] = e.ToolID
				}
				if td.messages[idx].ToolStatus != nil && *td.messages[idx].ToolStatus == "running" {
					if td.backgroundPending == nil {
						td.backgroundPending = make(map[string]bool)
					}
					td.backgroundPending[e.ToolID] = true
				} else {
					status := "background"
					td.messages[idx].ToolStatus = &status
					td.messages[idx].ToolCompletedAt = nil
					td.bgCount.invalidate()
					msgToPublish = []wire.ThreadMessage{td.messages[idx]}
					bgPersistMsgs = append(bgPersistMsgs, td.messages[idx])
				}
				td.summary.UpdatedAt = now
				currentSummary = b.summaryForPublishLocked(td)

			case harness.EventBackgroundTaskEnd:
				// provider 托管的后台任务结束：按 tool id 回写最终状态。结束帧常出现在后续 attach
				// 续跑轮次，此时消息可能已被冷会话淘汰，需要先回载。
				if e.ToolID == "" && e.TaskID != "" {
					e.ToolID = td.backgroundTaskTools[e.TaskID]
				}
				delete(td.backgroundTaskTools, e.TaskID)
				toolMsgID, known := td.backgroundTools[e.ToolID]
				if !known {
					toolMsgID = toolMsgIDs[e.ToolID]
				}
				if toolMsgID == "" {
					b.mu.Unlock()
					return
				}
				if err := b.ensureMessagesLocked(td, threadID); err != nil {
					b.mu.Unlock()
					return
				}
				b.ensureMessagesLoadedLocked(td)
				idx := -1
				for i, m := range td.messages {
					if m.ID == toolMsgID {
						idx = i
						break
					}
				}
				if idx < 0 {
					delete(td.backgroundTools, e.ToolID)
					delete(td.backgroundPending, e.ToolID)
					b.mu.Unlock()
					return
				}
				// 只回写仍在运行或已标成 background 的条目，避免覆盖之后已经落定的结果。
				current := ""
				if td.messages[idx].ToolStatus != nil {
					current = *td.messages[idx].ToolStatus
				}
				if current != "background" && current != "running" {
					delete(td.backgroundTools, e.ToolID)
					delete(td.backgroundPending, e.ToolID)
					b.mu.Unlock()
					return
				}
				status := "completed"
				if e.TaskStatus != "" && !strings.EqualFold(e.TaskStatus, "completed") {
					status = "failed"
					td.messages[idx].Text += "\n[后台任务结束：" + e.TaskStatus + "]"
				}
				finishBackgroundTaskLocked(td, idx, status, now)
				delete(td.backgroundTools, e.ToolID)
				delete(td.backgroundPending, e.ToolID)
				msgToPublish = []wire.ThreadMessage{td.messages[idx]}
				bgPersistMsgs = append(bgPersistMsgs, td.messages[idx])
				td.summary.UpdatedAt = now
				currentSummary = b.summaryForPublishLocked(td)

			case harness.EventError:
				receivedErrorEvent = true
				errorEventText = e.Err
				// 失败类别随错误正文一起带出去，界面据此直接选横幅样式，不再靠关键词猜。
				errorEventKind = e.ErrKind
				b.mu.Unlock()
				return

			case harness.EventUsage:
				if e.Usage == nil {
					b.mu.Unlock()
					return
				}
				u := *e.Usage
				if elapsed := b.nowLocked().Sub(turnStartedAt).Milliseconds(); elapsed > 0 {
					u.DurationMs = elapsed
				}
				if b.usage == nil {
					b.usage = make(map[string]map[int]harness.Usage)
				}
				if b.usage[threadID] == nil {
					b.usage[threadID] = make(map[int]harness.Usage)
				}
				// provider 最后一条整轮 usage 通常不带逐请求样本，保留本轮已收集的样本，避免被抹掉。
				if len(u.Requests) == 0 {
					if prev, ok := b.usage[threadID][turnIndex]; ok && len(prev.Requests) > 0 {
						u.Requests = append([]harness.RequestSpeed(nil), prev.Requests...)
					}
				}
				b.usage[threadID][turnIndex] = u
				currentSummary = b.summaryForPublishLocked(td)
				b.mu.Unlock()

				b.persistTurnUsage(threadID, turnIndex, u)
				b.hub.Publish(threadID, nil, nil, currentSummary)
				return

			case harness.EventFileChange:
				// Provider 明确上报的文件变更事件（如 Claude 的 Edit/Write/MultiEdit）。
				if e.Path != "" {
					td.touchedPaths = append(td.touchedPaths, e.Path)
					td.lastFileChangeAt = b.nowLocked()
				}
				b.mu.Unlock()
				return

			case harness.EventDone:
				// 忽略或无需单独处理
				b.mu.Unlock()
				return

			default:
				b.mu.Unlock()
				return
			}
			b.enrichTurnMessagesLocked(td, msgToPublish)
			for i := range td.messages {
				applyTurnTiming(&td.messages[i], td.turnTiming)
			}
			b.mu.Unlock()

			// 后台任务的工具消息可能属于更早回合，回合末批量落库覆盖不到，必须显式写穿。
			if len(bgPersistMsgs) > 0 {
				b.persistMessages(threadID, bgPersistMsgs)
			}

			// 流式钩子在锁外同步调用，保证与事件顺序一致。
			if streamHook != nil {
				streamHook(threadID, streamProject, false, streamDelta)
			}

			if len(msgToPublish) > 0 {
				b.hub.Publish(threadID, msgToPublish, nil, currentSummary)
			}
		}

		runErr = h.Run(runCtx, turn, emit)
		firstAttempt = false
		emitMu.Lock()
		acceptEvents = false
		emitMu.Unlock()

		candidateErr := ""
		candidateErrKind := ""
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			candidateErr = runErr.Error()
			// Run 返回的错误本身不带类别，同一轮里已经收到带类别的错误事件时沿用它。
			if receivedErrorEvent {
				candidateErrKind = errorEventKind
			}
		} else if receivedErrorEvent && errorEventText != "" {
			candidateErr = errorEventText
			candidateErrKind = errorEventKind
		}

		if runCtx.Err() != nil {
			break
		}

		if candidateErr != "" {
			// 1. 会话失效自愈：本轮带着非空 sessionID 恢复会话，并且报错命中失效特征时清空 sessionID，追加 notice 并用重建上下文重跑一次
			if currSessionID != "" && !lostSessionRetried && isLostSessionError(candidateErr) {
				lostSessionRetried = true

				// extra 记录本次需要单独补发到新会话的转发上下文（仅当重跑提示里还没有它时）。
				var extra string
				b.mu.Lock()
				if td, ok := b.threads[threadID]; ok {
					td.sessionID = ""
					td.sessionEpoch++
					// 会话被替换，转发上下文需要在新会话里重新送达。
					td.continuationSent = false
					if continuationText == "" {
						continuationText = td.continuation
						extra = td.continuation
					}
					recordedEpoch = td.sessionEpoch
				}
				s := b.store
				var priorMsgs []wire.ThreadMessage
				if td, ok := b.threads[threadID]; ok {
					for _, m := range td.messages {
						if m.TurnIndex < turnIndex && (m.TurnID == nil || *m.TurnID != turnID) {
							priorMsgs = append(priorMsgs, m)
						}
					}
				}
				b.mu.Unlock()

				if s != nil {
					pCtx, pCancel := persistCtx()
					if err := s.UpdateThreadSession(pCtx, threadID, ""); err != nil {
						log.Printf("store update thread session %s error: %v", threadID, err)
					}
					pCancel()
				}

				_ = b.AppendNotice(threadID, "原会话已失效，已用本地历史重建上下文")

				currSessionID = ""
				currPrompt = buildProviderHandoffPrompt(historyAfterLastSplit(priorMsgs), harnessPrompt)
				if extra != "" {
					currPrompt = extra + "\n\n" + currPrompt
				}
				continue
			}

			// 2. Provider 进程非预期退出自愈 (recoverCrashedCodexTurn)
			if !crashedRetried && isProviderCrashError(candidateErr) && runCtx.Err() == nil {
				crashedRetried = true
				log.Printf("provider crash recovery: thread=%s turn=%s provider=%s model=%q session_present=%t run_error=%t error_kind=%q err=%q", threadID, turnID, h.Kind(), turn.Model, currSessionID != "", runErr != nil, candidateErrKind, RedactSecrets(candidateErr))

				// extra 记录本次需要单独补发到新会话的转发上下文（仅当重跑提示里还没有它时）。
				var extra string
				b.mu.Lock()
				if td, ok := b.threads[threadID]; ok {
					td.sessionID = ""
					td.sessionEpoch++
					// 会话被替换，转发上下文需要在新会话里重新送达。
					td.continuationSent = false
					if continuationText == "" {
						continuationText = td.continuation
						extra = td.continuation
					}
					recordedEpoch = td.sessionEpoch
				}
				s := b.store
				var priorMsgs []wire.ThreadMessage
				if td, ok := b.threads[threadID]; ok {
					for _, m := range td.messages {
						if m.TurnIndex < turnIndex && (m.TurnID == nil || *m.TurnID != turnID) {
							priorMsgs = append(priorMsgs, m)
						}
					}
				}
				b.mu.Unlock()

				if s != nil {
					pCtx, pCancel := persistCtx()
					if err := s.UpdateThreadSession(pCtx, threadID, ""); err != nil {
						log.Printf("store update thread session %s error: %v", threadID, err)
					}
					pCancel()
				}

				_ = b.AppendNotice(threadID, "Provider 进程意外退出，已用本地历史重建上下文")

				currSessionID = ""
				currPrompt = buildProviderHandoffPrompt(historyAfterLastSplit(priorMsgs), harnessPrompt)
				if extra != "" {
					currPrompt = extra + "\n\n" + currPrompt
				}
				continue
			}

			// 网络瞬断最多重试 3 次；计数仅作用于本轮。
			if transientRetryCount < 3 && isTransientNetworkError(candidateErr) {
				transientRetryCount++
				notice := fmt.Sprintf("网络中断，正在重新发送（第 %d 次）", transientRetryCount)
				_ = b.AppendNotice(threadID, notice)

				waitTime := time.Duration(transientRetryCount*3) * time.Second
				retryReady := make(chan struct{})
				stopRetry := b.scheduleAfter(waitTime, func() { close(retryReady) })
				select {
				case <-runCtx.Done():
				case <-retryReady:
				}
				stopRetry()
				if runCtx.Err() != nil {
					break
				}
				continue
			}

			finalErrText = candidateErr
			finalErrKind = candidateErrKind
			if isOutdatedCLIError(candidateErr) {
				_ = b.AppendNotice(threadID, "CLI 版本已过旧，请升级对应 Provider CLI 后重新发送本轮消息；不会自动执行全局安装命令。")
			}
			break
		}

		finalErrText = ""
		finalErrKind = ""
		b.mu.Lock()
		if td.runTurnID == turnID && td.sessionEpoch == recordedEpoch {
			// 换新会话的那一轮若没能建起新会话（provider 没上报会话 ID），保留历史交接，
			// 让下一轮按分割线之后的历史补上下文，而不是把历史凭空丢掉。
			if !rotated || td.sessionID != "" {
				td.needsHandoff = false
			}
		}
		b.mu.Unlock()
		if guidanceInjected {
			b.markGuidanceDelivered(threadID)
		}
		break
	}

	// 重试耗尽仍没有上游事件：provider 启动失败或首个事件就是错误，这一轮没有真正交给 provider，
	// 把出队投递行从 dispatching 记为 failed；已经 accepted（显式回执或上游输出）的轮次不再改判，
	// 已是终态时 store 侧幂等忽略。
	if finalErrText != "" && !deliverySettled {
		b.markDeliveryFailed(threadID, turnID, deliveryMessageID, deliveryFollowUpID, finalErrText)
	}

	b.mu.Lock()
	validTurn := b.threads[threadID] == td && td.sessionEpoch == recordedEpoch && td.runTurnID == turnID
	// 这一轮的 attach 标记用完即清，避免下一轮误当成接自发输出的轮次。
	if td.attachTurnID == turnID {
		td.attachTurnID = ""
	}
	// 送达以 provider 会话已建立为准；失败、未建会话或会话已被替换时保持待送达。
	if validTurn && continuationText != "" && td.sessionID != "" {
		td.continuationSent = true
	}
	b.mu.Unlock()
	if !validTurn {
		// 运行中切换 Provider 会递增 sessionEpoch，本轮随之失效；若新 epoch 没启动新轮次，
		// 必须在这里恢复空闲，否则 IsRunning 永久为 true。
		isNormalEarly, isInterruptedEarly := turnEndState(runCtx, finalErrText)
		b.finishTurnEarly(td, threadID, turnID, turnIndex, isNormalEarly, isInterruptedEarly)
		return
	}
	if gitRun != nil && repoRoot != "" {
		endCtx, endCancel := context.WithTimeout(context.Background(), 30*time.Second)
		endRef := git.CheckpointRef(threadID, turnID) + "/end"
		if _, cpErr := gitRun.CheckpointWatched(endCtx, b.watcherFor(repoRoot), repoRoot, endRef, "turn "+turnID+" end"); cpErr != nil {
			log.Printf("checkpoint end failed for thread %s turn %s: %v", threadID, turnID, cpErr)
		}
		endCancel()
	}

	var (
		finalChangedMsgs []wire.ThreadMessage
		msgsToPersist    []wire.ThreadMessage
		nowTime          time.Time
	)

	b.mu.Lock()
	td, ok = b.threads[threadID]
	if !ok || td.sessionEpoch != recordedEpoch || td.runTurnID != turnID {
		b.mu.Unlock()
		isNormalEarly, isInterruptedEarly := turnEndState(runCtx, finalErrText)
		b.finishTurnEarly(td, threadID, turnID, turnIndex, isNormalEarly, isInterruptedEarly)
		return
	}
	historyOp := make(chan struct{})
	td.historyOp = historyOp

	nowTime = time.Now().UTC()
	now := nowTime.Format(time.RFC3339Nano)

	isInterrupted := runCtx.Err() == context.Canceled && b.ctx.Err() == nil
	if finalErrText != "" && !isInterrupted {
		errKind := "error"
		errMsg := wire.ThreadMessage{
			ID:        newUUID(),
			Role:      "assistant",
			Text:      "出错：" + finalErrText,
			CreatedAt: now,
			TurnIndex: turnIndex,
			TurnID:    &turnID,
			Kind:      &errKind,
		}
		// 类别随消息一起落库，重启后历史里的错误横幅仍能按类别渲染；判不准时不写，界面回退到关键词。
		if finalErrKind != "" {
			kindCopy := finalErrKind
			errMsg.ErrKind = &kindCopy
		}
		td.messages = append(td.messages, errMsg)
		finalChangedMsgs = append(finalChangedMsgs, errMsg)
	}

	// 最后一条 assistant 正文消息的 IsFinalResponse 设为 true
	for i := len(td.messages) - 1; i >= 0; i-- {
		if td.messages[i].TurnID != nil && *td.messages[i].TurnID == turnID && td.messages[i].Role == "assistant" && td.messages[i].Kind != nil && (*td.messages[i].Kind == "assistant" || *td.messages[i].Kind == "text") {
			isFinal := true
			td.messages[i].IsFinalResponse = &isFinal
			finalChangedMsgs = append(finalChangedMsgs, td.messages[i])
			break
		}
	}

	td.summary.UpdatedAt = now

	for _, m := range td.messages {
		if m.TurnIndex == turnIndex {
			msgsToPersist = append(msgsToPersist, m)
		}
	}
	s := b.store
	b.mu.Unlock()

	b.persistMessages(threadID, msgsToPersist)
	if s != nil {
		pCtx, pCancel := persistCtx()
		if err := s.TouchThread(pCtx, threadID, nowTime); err != nil {
			log.Printf("store touch thread %s error: %v", threadID, err)
		}
		pCancel()
	}

	status := "completed"
	if runCtx.Err() != nil {
		status = "interrupted"
	} else if finalErrText != "" {
		status = "failed"
	}
	b.updateTurnFinish(threadID, turnID, turnIndex, status, finalErrText)
	b.mu.Lock()
	var timingSummary wire.ThreadSummary
	if cur := b.threads[threadID]; cur != nil {
		finalChangedMsgs = nil
		for _, msg := range cur.messages {
			if msg.TurnID != nil && *msg.TurnID == turnID {
				finalChangedMsgs = append(finalChangedMsgs, msg)
			}
		}
		timingSummary = b.summaryForPublishLocked(cur)
	}
	b.mu.Unlock()
	b.hub.Publish(threadID, finalChangedMsgs, nil, timingSummary)
	b.hub.Flush(threadID)
	close(historyOp)
	b.mu.Lock()
	if curTd, exists := b.threads[threadID]; exists && curTd.historyOp == historyOp {
		curTd.historyOp = nil
	}
	b.mu.Unlock()

	var finalSummary wire.ThreadSummary
	var (
		nextText     string
		queueChanged bool
		nextStarted  *startedTurn

		hookProjectID         string
		hookLastAssistantText string
		hookCompleted         bool
		turnHook              func(threadID, projectID, assistantText string, completed bool)
		retryUnsolicited      bool
	)
	autoContinueAfterLimit := b.generalSettings().Session.AutoContinueAfterLimit
	b.mu.Lock()
	if td, ok := b.threads[threadID]; ok {
		if td.sessionEpoch != recordedEpoch || td.runTurnID != turnID {
			b.mu.Unlock()
			isNormalEarly, isInterruptedEarly := turnEndState(runCtx, finalErrText)
			b.finishTurnEarly(td, threadID, turnID, turnIndex, isNormalEarly, isInterruptedEarly)
			return
		}
		if td.watchdogStop != nil {
			td.watchdogStop()
			td.watchdogStop = nil
		}
		td.summary.LastActivityText = nil
		td.summary.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		// 注意：td.runCancel() 会取消本轮 runCtx，必须在调用前判断本轮是否被中断。
		isInterrupted := runCtx.Err() != nil
		isNormal := !isInterrupted && finalErrText == ""
		if !isNormal && !isInterrupted && finalErrText != "" {
			if autoContinueAfterLimit && isUsageLimitError(finalErrText) {
				b.scheduleAutoContinueLocked(threadID, finalErrText)
			}
		}
		if td.runCancel != nil {
			td.runCancel()
			td.runCancel = nil
		}
		td.runTurnID = ""
		for i := len(td.messages) - 1; i >= 0; i-- {
			if td.messages[i].Role == "assistant" && td.messages[i].TurnID != nil && *td.messages[i].TurnID == turnID {
				hookLastAssistantText = td.messages[i].Text
				break
			}
		}
		finalSummary = b.summaryForPublishLocked(td)

		hookProjectID = td.summary.ProjectID
		hookCompleted = isNormal
		turnHook = b.turnFinishedHook

		// 派发完成前仍视为运行中，避免跟进消息抢在 bee 前启动。
		b.mu.Unlock()
		// Plan 回复只供审阅，不触发执行型子任务派发。
		if turnHook != nil && interactionMode != "plan" {
			turnHook(threadID, hookProjectID, hookLastAssistantText, hookCompleted)
		}
		hasBees := b.hasWorkingSwarmBees(threadID)
		b.mu.Lock()
		if b.threads[threadID] != td || td.sessionEpoch != recordedEpoch {
			b.mu.Unlock()
			b.finishTurnEarly(td, threadID, turnID, turnIndex, isNormal, isInterrupted)
			return
		}
		td.setIdleLocked()
		retryUnsolicited = td.takeUnsolicitedPendingLocked()
		if isNormal || isInterrupted {
			td.summary.Attention = "ready"
			// 正常结束（含被中断）时清空错误原文，摘要里不再保留上一轮的失败原因。
			td.summary.LastErrorText = ""
		} else {
			// 出错结束：时间线上已有“Stopped with an error.”提示，
			// 这里用 attention "failed" 把结果带给手机端推送与桌面通知；
			// 错误原文随摘要下发，供额度提醒解析恢复时间。
			td.summary.Attention = "failed"
			td.summary.LastErrorText = truncateLastErrorText(finalErrText)
		}

		finalSummary = b.summaryForPublishLocked(td)

		// 宿主关闭时保留队列不动，避免排队项在关闭途中被移除后既没发送也无法恢复。
		if b.ctx.Err() == nil {
			nextStarted, nextText, queueChanged = b.startNextQueuedTurnLocked(td, isNormal && !hasBees, isNormal)
		}
	}
	b.mu.Unlock()

	b.hub.Publish(threadID, nil, nil, finalSummary)
	if nextStarted != nil {
		b.hub.Flush(threadID)
		// 正文先落库再删队列行，崩溃时最多重复出现一条已写正文的排队项，不会丢消息。
		b.persistMessages(threadID, []wire.ThreadMessage{nextStarted.userMsg})
		b.recordDispatchingForTurn(threadID, nextStarted)
	}
	if queueChanged {
		b.commitQueueChange(threadID)
	}

	if nextStarted != nil {
		if nextStarted.titleChanged {
			b.persistThreadTitle(threadID, nextStarted.currentSummary.Title)
		}
		b.hub.BeginRun(threadID)
		b.hub.Publish(threadID, []wire.ThreadMessage{nextStarted.userMsg}, nil, nextStarted.currentSummary)
		b.launchTurnRun(threadID, nextStarted, nextText)
	} else {
		b.hub.Publish(threadID, nil, nil, finalSummary)
	}
	if retryUnsolicited {
		go b.retryUnsolicitedTurn(threadID)
	}
}

func (b *MemoryBackend) simulateAssistantReply(runCtx context.Context, threadID string, turnID string, userText string, turnIndex int) {
	fullReply := "echo: " + userText
	runes := []rune(fullReply)
	n := len(runes)

	segments := make([]string, 4)
	for i := 1; i <= 4; i++ {
		end := (n * i) / 4
		if i == 4 {
			end = n
		}
		segments[i-1] = string(runes[:end])
	}

	assistantMsgID := newUUID()
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)

	// 模拟回复的分段是累积前缀：先以 reset=true 重置流式缓冲，再逐段给出增量后缀。
	var (
		streamHook      func(threadID, projectID string, reset bool, delta string)
		streamProjectID string
	)
	b.mu.Lock()
	if td, ok := b.threads[threadID]; ok {
		streamHook = b.assistantStreamHook
		streamProjectID = td.summary.ProjectID
	}
	b.mu.Unlock()
	if streamHook != nil {
		streamHook(threadID, streamProjectID, true, "")
	}
	prevText := ""

loop:
	for i, seg := range segments {
		select {
		case <-time.After(simulatedReplyStepDelay):
		case <-runCtx.Done():
			break loop
		}
		if runCtx.Err() != nil {
			break loop
		}

		// seg 是累积前缀，只有新增部分作为流式增量。
		delta := seg
		if suffix, ok := strings.CutPrefix(seg, prevText); ok {
			delta = suffix
		}
		prevText = seg

		b.mu.Lock()
		td, ok := b.threads[threadID]
		if !ok || td.runTurnID != turnID {
			b.mu.Unlock()
			b.finishTurnEarly(td, threadID, turnID, turnIndex, false, runCtx.Err() != nil)
			return
		}

		now := time.Now().UTC().Format(time.RFC3339Nano)
		kind := "text"
		isFinal := i == 3
		assistantMsg := wire.ThreadMessage{
			ID:              assistantMsgID,
			Role:            "assistant",
			Text:            seg,
			CreatedAt:       createdAt,
			TurnIndex:       turnIndex,
			TurnID:          &turnID,
			Kind:            &kind,
			IsFinalResponse: &isFinal,
		}

		applyTurnTiming(&assistantMsg, td.turnTiming)
		found := false
		for idx, m := range td.messages {
			if m.ID == assistantMsgID {
				td.messages[idx] = assistantMsg
				found = true
				break
			}
		}
		if !found {
			td.messages = append(td.messages, assistantMsg)
		}

		td.summary.UpdatedAt = now
		if !isFinal {
			actText := "Generating..."
			td.summary.LastActivityText = &actText
		}
		currentSummary := b.summaryForPublishLocked(td)
		b.mu.Unlock()

		if streamHook != nil && delta != "" {
			streamHook(threadID, streamProjectID, false, delta)
		}
		if !isFinal {
			b.hub.Publish(threadID, []wire.ThreadMessage{assistantMsg}, nil, currentSummary)
		}
	}

	// 结束时先落库，再把线程摘要里表示运行状态的字段改回空闲并 Publish
	var (
		msgsToPersist []wire.ThreadMessage
		nowTime       time.Time
	)
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok || td.runTurnID != turnID {
		b.mu.Unlock()
		b.finishTurnEarly(td, threadID, turnID, turnIndex, false, runCtx.Err() != nil)
		return
	}
	nowTime = time.Now().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	td.summary.UpdatedAt = now

	for _, m := range td.messages {
		if m.TurnIndex == turnIndex {
			msgsToPersist = append(msgsToPersist, m)
		}
	}
	s := b.store
	b.mu.Unlock()

	b.persistMessages(threadID, msgsToPersist)
	if s != nil {
		pCtx, pCancel := persistCtx()
		if err := s.TouchThread(pCtx, threadID, nowTime); err != nil {
			log.Printf("store touch thread %s error: %v", threadID, err)
		}
		pCancel()
	}

	status := "completed"
	if runCtx.Err() != nil {
		status = "interrupted"
	}
	b.updateTurnFinish(threadID, turnID, turnIndex, status, "")
	var finalTurnMsg []wire.ThreadMessage
	b.mu.Lock()
	if cur := b.threads[threadID]; cur != nil {
		for _, msg := range cur.messages {
			if msg.TurnID != nil && *msg.TurnID == turnID {
				finalTurnMsg = append(finalTurnMsg, msg)
			}
		}
	}
	projectionSummary := b.summaryForPublishLocked(td)
	b.mu.Unlock()
	b.hub.Publish(threadID, finalTurnMsg, nil, projectionSummary)
	b.hub.Flush(threadID)
	var finalSummary wire.ThreadSummary
	var (
		nextText     string
		queueChanged bool
		nextStarted  *startedTurn

		hookProjectID         string
		hookLastAssistantText string
		hookCompleted         bool
		turnHook              func(threadID, projectID, assistantText string, completed bool)
		retryUnsolicited      bool
	)
	b.mu.Lock()
	if td, ok := b.threads[threadID]; ok && td.runTurnID == turnID {
		td.summary.LastActivityText = nil
		td.summary.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		// 注意：td.runCancel() 会取消本轮 runCtx，必须在调用前判断本轮是否被中断。
		isInterrupted := runCtx.Err() != nil
		isNormal := !isInterrupted
		if td.runCancel != nil {
			td.runCancel()
			td.runCancel = nil
		}
		td.runTurnID = ""
		finalSummary = b.summaryForPublishLocked(td)

		hookProjectID = td.summary.ProjectID
		hookCompleted = isNormal
		for i := len(td.messages) - 1; i >= 0; i-- {
			if td.messages[i].Role == "assistant" && td.messages[i].TurnID != nil && *td.messages[i].TurnID == turnID {
				hookLastAssistantText = td.messages[i].Text
				break
			}
		}
		turnHook = b.turnFinishedHook

		// 派发完成前仍视为运行中，避免跟进消息抢在 bee 前启动。
		b.mu.Unlock()
		if turnHook != nil {
			turnHook(threadID, hookProjectID, hookLastAssistantText, hookCompleted)
		}
		hasBees := b.hasWorkingSwarmBees(threadID)
		b.mu.Lock()
		if b.threads[threadID] != td {
			b.mu.Unlock()
			return
		}
		td.setIdleLocked()
		retryUnsolicited = td.takeUnsolicitedPendingLocked()
		finalSummary = b.summaryForPublishLocked(td)

		// 宿主关闭时保留队列不动，避免排队项在关闭途中被移除后既没发送也无法恢复。
		if b.ctx.Err() == nil {
			nextStarted, nextText, queueChanged = b.startNextQueuedTurnLocked(td, isNormal && !hasBees, isNormal)
		}
	}
	b.mu.Unlock()

	b.hub.Publish(threadID, nil, nil, finalSummary)
	if nextStarted != nil {
		b.hub.Flush(threadID)
		// 正文先落库再删队列行，崩溃时最多重复出现一条已写正文的排队项，不会丢消息。
		b.persistMessages(threadID, []wire.ThreadMessage{nextStarted.userMsg})
		b.recordDispatchingForTurn(threadID, nextStarted)
	}
	if queueChanged {
		b.commitQueueChange(threadID)
	}

	if nextStarted != nil {
		if nextStarted.titleChanged {
			b.persistThreadTitle(threadID, nextStarted.currentSummary.Title)
		}
		b.hub.Flush(threadID)
		b.hub.BeginRun(threadID)
		b.hub.Publish(threadID, []wire.ThreadMessage{nextStarted.userMsg}, nil, nextStarted.currentSummary)
		b.launchTurnRun(threadID, nextStarted, nextText)
	} else {
		b.hub.Publish(threadID, nil, nil, finalSummary)
	}
	if retryUnsolicited {
		go b.retryUnsolicitedTurn(threadID)
	}
}

// ListRequests 查询会话中待处理的交互与审批请求。
func (b *MemoryBackend) ListRequests(ctx context.Context, threadID string) (wire.RequestsListResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	td, ok := b.threads[threadID]
	if !ok {
		return wire.RequestsListResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}

	requests := make([]wire.RemoteRequest, 0, len(td.pending))
	for _, pr := range td.pending {
		requests = append(requests, pr.req)
	}

	return wire.RequestsListResult{
		ThreadID: threadID,
		Requests: requests,
	}, nil
}

// RespondRequest 答复指定会话中的待处理审批请求。
func (b *MemoryBackend) RespondRequest(ctx context.Context, params wire.RequestsRespondParams) (wire.RequestsRespondResult, error) {
	// Answers 非空时解析为 {"<questionID>": ["值"]}，解析失败返回 InvalidParams
	var answers map[string][]string
	hasAnswers := len(params.Answers) > 0 && !bytes.Equal(bytes.TrimSpace(params.Answers), []byte("null"))
	if hasAnswers {
		if err := json.Unmarshal(params.Answers, &answers); err != nil {
			return wire.RequestsRespondResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: "invalid answers: must be an object of string arrays",
			}
		}
	}

	// OptionID 不在 allow/allow_always/deny 之内返回 InvalidParams
	if params.OptionID != nil {
		opt := *params.OptionID
		if opt != "allow" && opt != "allow_always" && opt != "deny" {
			return wire.RequestsRespondResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: "invalid optionID: must be allow, allow_always, or deny",
			}
		}
	}

	b.mu.Lock()
	locked := true
	defer func() {
		if locked {
			b.mu.Unlock()
		}
	}()

	td, ok := b.threads[params.ThreadID]
	if !ok {
		return wire.RequestsRespondResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", params.ThreadID),
		}
	}

	pr, ok := td.pending[params.RequestID]
	if !ok {
		// 已处理的请求会从 pending 移除，重复答复（如另一台设备已处理）统一返回 -32010。
		return wire.RequestsRespondResult{}, &wire.RPCError{
			Code:    wire.ErrCodeRequestAlreadyHandled,
			Message: "request not found or already handled",
		}
	}

	// 已经标记 deliveryError 的请求，允许用户再次答复重试：清空 deliveryError 后重新投递。
	// 如果底层 Provider 已经不再等待这条请求，就把它从列表移除，并返回 already resolved 或 stale 一类的现有错误码。
	if pr.req.DeliveryError != nil {
		providerStale := false
		if (pr.ctx != nil && pr.ctx.Err() != nil) || !td.summary.IsRunning || !pr.providerWaiting {
			providerStale = true
		}
		if providerStale {
			delete(td.pending, params.RequestID)
			if len(td.pending) == 0 {
				td.summary.HasPendingRequest = false
				if td.summary.IsRunning {
					td.summary.Attention = "running"
				} else {
					td.summary.Attention = "ready"
				}
			}
			now := time.Now().UTC().Format(time.RFC3339Nano)
			td.summary.UpdatedAt = now
			staleSummary := b.summaryForPublishLocked(td)
			stalePending := make([]wire.RemoteRequest, 0, len(td.pending))
			for _, p := range td.pending {
				stalePending = append(stalePending, p.req)
			}
			locked = false
			b.mu.Unlock()

			b.hub.Publish(params.ThreadID, nil, nil, staleSummary)
			b.hub.Notify(params.ThreadID, "requests.changed", wire.RequestsChangedParams{
				ThreadID: params.ThreadID,
				Requests: stalePending,
			})

			return wire.RequestsRespondResult{}, &wire.RPCError{
				Code:    wire.ErrCodeRequestExpired,
				Message: "request is already resolved or stale",
			}
		}

	} else if pr.handled {
		return wire.RequestsRespondResult{}, &wire.RPCError{
			Code:    wire.ErrCodeRequestAlreadyHandled,
			Message: "request not found or already handled",
		}
	}

	if (params.RunID != "" && params.RunID != pr.req.RunID) || (pr.ctx != nil && pr.ctx.Err() != nil) {
		return wire.RequestsRespondResult{}, &wire.RPCError{Code: wire.ErrCodeOperationRejected, Message: "request is no longer deliverable"}
	}

	kind := pr.req.Kind
	confirmsDelivery := pr.confirmsDelivery
	// 问答记录在锁外写入，先拷一份请求快照（题目列表只在创建时写入，浅拷贝足够）。
	questionReq := pr.req

	// 仅提问与计划类请求接受 answers
	if hasAnswers && kind != string(harness.RequestKindQuestion) && kind != string(harness.RequestKindPlan) {
		return wire.RequestsRespondResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: fmt.Sprintf("answers are not supported for %s requests", kind),
		}
	}
	var optID string
	if params.OptionID != nil {
		optID = *params.OptionID
	}
	isSkip := params.Skip != nil && *params.Skip

	switch kind {
	case string(harness.RequestKindQuestion):
		if (optID != "" && optID != "deny") || (hasAnswers && (optID != "" || isSkip)) {
			return wire.RequestsRespondResult{}, &wire.RPCError{Code: wire.InvalidParams, Message: "questions require answers or skip"}
		}
		if !isSkip && optID != "deny" {
			if err := validateQuestionAnswers(pr.req.Questions, answers); err != nil {
				return wire.RequestsRespondResult{}, &wire.RPCError{Code: wire.InvalidParams, Message: err.Error()}
			}
		}
	case string(harness.RequestKindPlan):
		if optID == "allow_always" || (isSkip && (optID != "" || hasAnswers)) || (!isSkip && optID == "" && !hasAnswers) {
			return wire.RequestsRespondResult{}, &wire.RPCError{Code: wire.InvalidParams, Message: "plans require approval or rejection"}
		}
		if hasAnswers && (optID == "allow" || len(answers) != 1 || len(answers["feedback"]) != 1 || strings.TrimSpace(answers["feedback"][0]) == "") {
			return wire.RequestsRespondResult{}, &wire.RPCError{Code: wire.InvalidParams, Message: "plan answers must contain rejection feedback"}
		}
	}

	ack := make(chan error, 1)
	deliveryAck := make(chan error, 1)
	select {
	case pr.resCh <- approvalResponse{optionID: optID, skip: isSkip, answers: answers, ack: ack}:
	default:
		return wire.RequestsRespondResult{}, &wire.RPCError{Code: wire.ErrCodeOperationRejected, Message: "request response queue is full"}
	}
	pr.req.DeliveryError = nil
	pr.deliveryAck = deliveryAck
	pr.awaitingAck = confirmsDelivery
	pr.respondedAt = time.Now()
	pr.handled = true
	done, providerCtx := pr.done, pr.ctx
	timeout := b.approvalDeliveryTimeout

	locked = false
	b.mu.Unlock()

	var timeoutCh chan struct{}
	if confirmsDelivery {
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		timeoutCh = make(chan struct{})
		stopTimer := b.scheduleAfter(timeout, func() { close(timeoutCh) })
		defer stopTimer()
	}
	if done != nil {
		select {
		case err := <-ack:
			if err != nil {
				return wire.RequestsRespondResult{}, &wire.RPCError{Code: wire.ErrCodeOperationRejected, Message: "request response was not received"}
			}
		case <-done:
			select {
			case err := <-ack:
				if err != nil {
					return wire.RequestsRespondResult{}, err
				}
			default:
				return wire.RequestsRespondResult{}, &wire.RPCError{Code: wire.ErrCodeOperationRejected, Message: "request ended before response was received"}
			}
		case <-ctx.Done():
			if !confirmsDelivery {
				return wire.RequestsRespondResult{}, ctx.Err()
			}
		case <-timeoutCh:
		}
	}

	// confirmsDelivery 为 false 时，行为与现在完全一致（Interact 已在收到答复后完成卡片清理与广播）
	if !confirmsDelivery {
		// 提问类请求：答复已交给 Provider，把问答记录写进会话历史，前端重启后仍能看到。
		// 审批类与计划类请求不写这条记录。
		if kind == string(harness.RequestKindQuestion) {
			b.appendQuestionAnswer(params.ThreadID, questionReq, answers, isSkip || optID == "deny")
		}
		return wire.RequestsRespondResult{
			ThreadID:  params.ThreadID,
			RequestID: params.RequestID,
			Kind:      kind,
			Status:    wire.RequestStatusAccepted,
		}, nil
	}

	// 接收与底层写入共用同一个期限，取消或超时也要保留送达错误。
	var failed bool
	var failReason string

	var prCtxDone <-chan struct{}
	if providerCtx != nil {
		prCtxDone = providerCtx.Done()
	}

	select {
	case err := <-deliveryAck:
		if err != nil {
			failed = true
			failReason = err.Error()
		}
	case <-timeoutCh:
		select {
		case err := <-deliveryAck:
			if err != nil {
				failed = true
				failReason = err.Error()
			}
		default:
			failed = true
			failReason = "delivery confirmation timed out"
		}
	case <-prCtxDone:
		select {
		case err := <-deliveryAck:
			if err != nil {
				failed = true
				failReason = err.Error()
			}
		default:
			failed = true
			failReason = "turn ended before delivery confirmation"
		}
	case <-ctx.Done():
		select {
		case err := <-deliveryAck:
			if err != nil {
				failed = true
				failReason = err.Error()
			}
		default:
			failed = true
			failReason = "request canceled before delivery confirmation"
		}
	}

	if failed {
		// 送达失败或超时：不移除这条请求，在 pendingRequest 上记录 deliveryError 文本，发布请求变化通知
		b.mu.Lock()
		// Provider 可能已经重新等待同一请求，旧答复不能覆盖新的送达状态。
		if pr.done == done && pr.deliveryAck == deliveryAck {
			pr.awaitingAck = false
			errText := failReason
			pr.req.DeliveryError = &errText
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		var failPending []wire.RemoteRequest
		var failSummary wire.ThreadSummary
		if td, ok := b.threads[params.ThreadID]; ok {
			td.summary.UpdatedAt = now
			failSummary = b.summaryForPublishLocked(td)
			failPending = make([]wire.RemoteRequest, 0, len(td.pending))
			for _, p := range td.pending {
				failPending = append(failPending, p.req)
			}
		}
		b.mu.Unlock()

		if failSummary.ID != "" {
			b.hub.Publish(params.ThreadID, nil, nil, failSummary)
			b.hub.Notify(params.ThreadID, "requests.changed", wire.RequestsChangedParams{
				ThreadID: params.ThreadID,
				Requests: failPending,
			})
		}

		return wire.RequestsRespondResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: fmt.Sprintf("delivery failed: %s", failReason),
		}
	}

	// 送达成功：维持现有成功路径，从 td.pending 中移除卡片，更新 summary，发布广播并返回 accepted
	interactive := kind == string(harness.RequestKindPlan) || kind == string(harness.RequestKindQuestion)
	b.mu.Lock()
	if pr.done != done || pr.deliveryAck != deliveryAck {
		b.mu.Unlock()
		return wire.RequestsRespondResult{}, &wire.RPCError{Code: wire.ErrCodeOperationRejected, Message: "request response was superseded"}
	}
	pr.awaitingAck = false
	if done != nil {
		select {
		case <-done:
		default:
			close(done)
		}
	}
	td, ok = b.threads[params.ThreadID]
	var cleanupPending []wire.RemoteRequest
	var cleanupSummary wire.ThreadSummary
	if ok {
		delete(td.pending, params.RequestID)
		if len(td.pending) == 0 {
			td.summary.HasPendingRequest = false
			if td.summary.IsRunning {
				td.summary.Attention = "running"
			} else {
				td.summary.Attention = "ready"
			}
		}
		if optID == "allow_always" && !interactive && pr.allowKey != "" {
			if td.alwaysAllow == nil {
				td.alwaysAllow = make(map[string]bool)
			}
			td.alwaysAllow[pr.allowKey] = true
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		td.summary.UpdatedAt = now
		cleanupSummary = b.summaryForPublishLocked(td)

		cleanupPending = make([]wire.RemoteRequest, 0, len(td.pending))
		for _, p := range td.pending {
			cleanupPending = append(cleanupPending, p.req)
		}
	}
	b.mu.Unlock()

	if cleanupSummary.ID != "" {
		b.hub.Publish(params.ThreadID, nil, nil, cleanupSummary)
		b.hub.Notify(params.ThreadID, "requests.changed", wire.RequestsChangedParams{
			ThreadID: params.ThreadID,
			Requests: cleanupPending,
		})
	}

	// 送达确认路径：确认答复已经交给 Provider 之后再写问答记录。
	if kind == string(harness.RequestKindQuestion) {
		b.appendQuestionAnswer(params.ThreadID, questionReq, answers, isSkip || optID == "deny")
	}

	return wire.RequestsRespondResult{
		ThreadID:  params.ThreadID,
		RequestID: params.RequestID,
		Kind:      kind,
		Status:    wire.RequestStatusAccepted,
	}, nil
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

// ListProviders 列出已注册的所有 Provider。
func (b *MemoryBackend) ListProviders(ctx context.Context) (wire.ProvidersListResult, error) {
	kinds := b.Providers()

	b.mu.Lock()
	harnesses := make(map[string]harness.Harness, len(b.harnesses))
	for k, h := range b.harnesses {
		harnesses[k] = h
	}
	swarmAvail := b.swarmAvailable
	defaultProvider := b.defaultProvider
	defaultRuntimeMode := b.defaultRuntimeModeLocked()
	b.mu.Unlock()

	providers := make([]wire.Provider, 0, len(kinds))
	// enabled 是桌面设置里的 provider 启用状态；未知的 provider 不视为关闭。
	enabled := b.providerEnabled()
	// providerDisabled 判断某个 provider 是否在桌面设置里被关闭。
	providerDisabled := func(id string) bool {
		on, known := enabled[id]
		return known && !on
	}
	for _, k := range kinds {
		// 已关闭的 provider 照常下发（带 enabled=false）：远程端还需要用它解析
		// 已有会话的 provider/模型名称，是否在选择器里隐藏由前端决定。
		var enabledFlag *bool
		if providerDisabled(k) {
			off := false
			enabledFlag = &off
		}
		available := true
		var unavailableReason *string
		auth := "signedIn"

		if h, ok := harnesses[k]; ok {
			if rep, ok := h.(availabilityReporter); ok {
				var reason string
				available, reason = rep.Available()
				if !available {
					unavailableReason = &reason
					auth = "signedOut"
				}
			}
		}

		name := providerDisplayName(k)
		if h, ok := harnesses[k]; ok {
			if dn, ok := h.(interface{ DisplayName() string }); ok {
				if d := dn.DisplayName(); d != "" {
					name = d
				}
			}
		}

		// 未登录或没配 API Key 的供应商没有可选模型：不展示 openai 起始目录这类占位行，
		// 也不为它起后台探测目录。
		models := []wire.ProviderModel{}
		if available {
			models = ProviderModels(k)
			if k == "openai" {
				// 配置里指定的模型作为首选行并入目录；没有配置时保留起始目录，避免整列不可选。
				var ids []string
				if h, ok := harnesses[k]; ok {
					if mi, ok := h.(interface{ ModelIDs() []string }); ok {
						ids = mi.ModelIDs()
					}
				}
				if len(ids) > 0 {
					models = withConfiguredModel(models, ids[0])
					seen := make(map[string]bool, len(models))
					for _, m := range models {
						seen[m.ID] = true
					}
					for _, id := range ids[1:] {
						if !seen[id] {
							models = append(models, wire.ProviderModel{ID: id, Name: id})
							seen[id] = true
						}
					}
				}
				applyModelsDev(k, models)
			}
			if len(models) == 0 {
				if h, ok := harnesses[k]; ok {
					if mi, ok := h.(interface{ ModelIDs() []string }); ok {
						for _, id := range mi.ModelIDs() {
							models = append(models, wire.ProviderModel{
								ID:   id,
								Name: id,
							})
						}
					}
				}
			}
		}

		// 供应商可用但目录为空（例如 CLI 未安装、还在探测）时带上原因：
		// available 保持 true，auth 不变，由桌面端在选择器里展示原因与“重新探测”入口。
		// 已有可用性原因（未登录等）时不覆盖。
		if available && len(models) == 0 && unavailableReason == nil {
			if notice := ModelCatalogNotice(k); notice != "" {
				reason := notice
				unavailableReason = &reason
			}
		}

		providers = append(providers, wire.Provider{
			ID:                k,
			Name:              name,
			Auth:              auth,
			Models:            models,
			SupportsPlanMode:  true,
			SupportsImages:    providerSupportsImages(k),
			SupportsFast:      false,
			Available:         available,
			UnavailableReason: unavailableReason,
			Enabled:           enabledFlag,
		})
	}

	// 编排组合直接来自桌面配置的配对模板：两侧 provider 都可用（Available）且未被桌面设置关闭，
	// 且配置的模型在该 provider 目录里或已加入用户固定列表（对齐桌面的 readySwarmPairs 过滤）。
	// usableByID 只收录可用于新建会话的 provider：已关闭的 provider 可以用在已有会话里，
	// 但不应该被选进新的草稿或编排组合。
	swarmCfg := b.swarmConfig()
	usableByID := make(map[string]wire.Provider, len(providers))
	for _, p := range providers {
		if p.Available && !providerDisabled(p.ID) {
			usableByID[p.ID] = p
		}
	}
	// 通用配置在配对过滤、常用模型与草稿默认值里共用，只读取一次。
	generalCfg := b.generalSettings()
	swarmPairs := make([]wire.SwarmPair, 0, len(swarmCfg.Pairs))
	if swarmAvail {
		// 全局并发 Bee 上限沿用桌面设置；0 表示不限，留空由远程端按会话默认值处理。
		var maxBees *int
		if swarmCfg.MaxBees > 0 {
			mh := swarmCfg.MaxBees
			maxBees = &mh
		}
		for _, pair := range swarmCfg.Pairs {
			if pair.ID == "" || pair.LeadProvider == "" {
				continue
			}
			workerProvider := pair.WorkerProvider
			if workerProvider == "" {
				workerProvider = pair.LeadProvider
			}
			lead, ok := usableByID[pair.LeadProvider]
			if !ok || !providerAllowsModel(lead, pair.LeadModel, generalCfg.Models.Pins) {
				continue
			}
			worker, ok := usableByID[workerProvider]
			if !ok || !providerAllowsModel(worker, pair.WorkerModel, generalCfg.Models.Pins) {
				continue
			}
			wp := workerProvider
			row := wire.SwarmPair{
				ID:             pair.ID,
				Provider:       pair.LeadProvider,
				WorkerProvider: &wp,
				MaxBees:        maxBees,
			}
			if name := strings.TrimSpace(pair.Name); name != "" {
				row.Name = &name
			}
			if leadModel := strings.TrimSpace(pair.LeadModel); leadModel != "" {
				row.LeadModel = &leadModel
			}
			if pair.LeadEffort != nil {
				if effort := strings.TrimSpace(*pair.LeadEffort); effort != "" {
					row.LeadEffort = &effort
				}
			}
			if workerModel := strings.TrimSpace(pair.WorkerModel); workerModel != "" {
				row.WorkerModel = &workerModel
			}
			if len(pair.BeeTiers) > 0 {
				row.BeeTiers = make(map[string]wire.SwarmTierOverride, len(pair.BeeTiers))
				for tid, ov := range pair.BeeTiers {
					row.BeeTiers[tid] = wire.SwarmTierOverride{
						Provider: ov.Provider,
						Model:    ov.Model,
						Effort:   ov.Effort,
						FastMode: ov.FastMode,
					}
				}
			}
			swarmPairs = append(swarmPairs, row)
		}
	}

	// 常用模型（桌面「我的模型」固定列表）按配置顺序下发，与桌面模型选择器的精选顺序一致。
	catalog := make([]wire.ModelCatalogEntry, 0, len(generalCfg.Models.Pins))
	for _, pin := range generalCfg.Models.Pins {
		if pin.Provider == "" || pin.ModelID == "" {
			continue
		}
		// 常用模型不收录已关闭的 provider（它只用于已有会话的解析）。
		if providerDisabled(pin.Provider) {
			continue
		}
		catalog = append(catalog, wire.ModelCatalogEntry{Provider: pin.Provider, ModelID: pin.ModelID})
	}

	// 新建会话的默认草稿：宿主端的默认 Provider / 模型与各模式，
	// 移动端据此初始化 Composer，不必自行猜测或留空。
	if defaultProvider == "" {
		defaultProvider = "memory"
	}
	// 回落用的默认 Provider 也不能是已关闭的：桌面设置里关掉它之后，
	// 新草稿要落到一个未被关闭且可用的 provider 上。
	if providerDisabled(defaultProvider) {
		for _, p := range providers {
			if !p.Available || providerDisabled(p.ID) {
				continue
			}
			defaultProvider = p.ID
			break
		}
	}
	defaultSwarmEnabled := false
	draft := wire.ComposerDraftDefaults{
		Provider:        defaultProvider,
		FastMode:        false,
		RuntimeMode:     defaultRuntimeMode,
		InteractionMode: "build",
		WorkspaceMode:   "worktree",
		SwarmEnabled:    swarmAvail && defaultSwarmEnabled,
	}
	if m := DefaultModel(defaultProvider); m != "" {
		draftModel := m
		draft.Model = &draftModel
	}
	// 宿主注入的草稿复刻桌面新建会话的模型记忆优先级；provider 不可用时整体回落到宿主默认值。
	if injected, ok := b.draftDefaults(); ok {
		if injected.Provider == "" {
			injected.Provider = defaultProvider
		}
		// 注入的 provider 若已被关闭，与不可用一样整体回落到宿主默认草稿。
		if _, known := usableByID[injected.Provider]; known {
			draft = injected
			draft.SwarmEnabled = draft.SwarmEnabled && swarmAvail
			if draft.RuntimeMode == "" {
				draft.RuntimeMode = defaultRuntimeMode
			}
			if draft.InteractionMode == "" {
				draft.InteractionMode = "build"
			}
			if draft.WorkspaceMode != config.WorkspaceModeWorktree {
				draft.WorkspaceMode = config.WorkspaceModeLocal
			}
			if p := usableByID[draft.Provider]; draft.Model == nil || (derefString(draft.Model) != "" && !providerAllowsModel(p, derefString(draft.Model), generalCfg.Models.Pins)) {
				m := DefaultModel(draft.Provider)
				draft.Model = &m
			}
		}
	}
	// 模型记忆不记录编排组合 ID：主导与执行侧选择与某条已下发的组合完全一致时补上，
	// 让远程端新建会话默认选中桌面上的同一条组合。
	if draft.SwarmEnabled && derefString(draft.SwarmPairID) == "" {
		if id := matchSwarmPairID(swarmPairs, draft.Provider, derefString(draft.Model), derefString(draft.SwarmBeesProvider), derefString(draft.SwarmBeesModel)); id != "" {
			draft.SwarmPairID = &id
		}
	}

	// providers.list 在桌面启动路径上：首次调用完成时打点，便于看实测耗时。
	startuptrace.Mark("providers: listProviders done")
	return wire.ProvidersListResult{
		Providers: providers,
		Catalog:   catalog,
		Swarm: wire.SwarmComposerInfo{
			Enabled:        swarmAvail,
			DefaultEnabled: defaultSwarmEnabled,
			Pairs:          swarmPairs,
		},
		Draft: &draft,
	}, nil
}

// validModel 在目录校验之外放行用户已固定的模型 ID（设置页「添加自定义模型」写入的 pin）。
// 不修改 IsValidModel 本身，避免影响不认用户固定列表的调用方。
func (b *MemoryBackend) validModel(provider, model string) bool {
	if IsValidModel(provider, model) {
		return true
	}
	if provider == "" || model == "" {
		return false
	}
	return b.generalSettings().Models.HasModelPin(provider, model)
}

// providerAllowsModel 判断配对模板里指定的模型是否在该 provider 的目录里（空模型表示不指定）。
// 目录为空、只有占位行，或由 CLI 动态列出的 provider 放行任意非空模型名，对齐桌面的 providerHasModel；
// pins 是用户固定的模型列表，命中时同样放行，避免自定义模型在配对与草稿里被过滤掉。
func providerAllowsModel(p wire.Provider, model string, pins []config.ModelPin) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return true
	}
	if hasPlaceholderCatalog(p.Models) || cliCatalogProvider(p.ID) || strings.HasPrefix(p.ID, "custom-") {
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

// ListProvidersForProject 接收 providers.list 的 projectID 透传。
// 当前 provider 目录与可用性均为全局设置（不随项目变化），因此结果与 ListProviders 一致；
// 保留该方法是为了让 Dispatcher 能把 projectID 交给后端，后续按项目覆盖目录时无需再改协议层。
func (b *MemoryBackend) ListProvidersForProject(ctx context.Context, projectID *string) (wire.ProvidersListResult, error) {
	return b.ListProviders(ctx)
}

// RememberSelection 记录用户主动确认的模型与编排选择（providers.rememberSelection）。
// 记忆只由用户主动操作写入：建会话只读记忆，不回写。
// 主导 provider 未注册或模型既不在目录里也未固定时返回 InvalidParams，且什么都不写；
// 未注入写回回调时只做校验、不写，仍正常返回。
func (b *MemoryBackend) RememberSelection(ctx context.Context, params wire.ProvidersRememberSelectionParams) error {
	provider := derefString(params.Provider)
	model := derefString(params.Model)
	if provider != "" {
		b.mu.Lock()
		_, known := b.harnesses[provider]
		b.mu.Unlock()
		if !known {
			return &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: fmt.Sprintf("unknown provider %q", provider),
			}
		}
		if model != "" && !b.validModel(provider, model) {
			return &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: fmt.Sprintf("invalid model %q for provider %q", model, provider),
			}
		}
	}
	// 子代理字段任意一个非 nil 就整组写入，空串表示跟随主导。
	if fn := b.rememberSelection(); fn != nil {
		fn(params)
	}
	return nil
}

// RemoveConn 从事件推送中心移除指定连接。
func (b *MemoryBackend) RemoveConn(c Conn) {
	if b.hub != nil {
		b.hub.RemoveConn(c)
	}
}

// turnMetadataLimit 是 threads.getDiff 下发的轮次摘要上限，超出的轮次只置 metadataTruncated。
const turnMetadataLimit = 50

// GetDiff 获取会话整库或指定轮次的代码差异。
func (b *MemoryBackend) GetDiff(ctx context.Context, params wire.ThreadsGetDiffParams) (wire.ThreadsGetDiffResult, error) {
	b.mu.Lock()
	td, ok := b.threads[params.ThreadID]
	if !ok {
		b.mu.Unlock()
		return wire.ThreadsGetDiffResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", params.ThreadID),
		}
	}
	// C-ST-07：diff 需要按轮次匹配消息，先确保历史已回载。
	if err := b.ensureMessagesLocked(td, params.ThreadID); err != nil {
		b.mu.Unlock()
		return wire.ThreadsGetDiffResult{}, err
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)

	workDir := b.harnessWorkDir
	if td.workDir != "" {
		workDir = td.workDir
	} else if proj, ok := b.projects[td.summary.ProjectID]; ok && proj.Path != "" {
		workDir = proj.Path
	}
	gitRunner := b.git
	msgs := make([]wire.ThreadMessage, len(td.messages))
	copy(msgs, td.messages)
	isRunning := td.summary.IsRunning
	runningTurnID := td.runTurnID
	takenBack := make(map[string]bool, len(td.takenBack))
	for id := range td.takenBack {
		takenBack[id] = true
	}
	s := b.store
	b.mu.Unlock()
	storeTurns := make(map[string]store.Turn)
	if s != nil {
		turns, err := s.ListTurns(ctx, params.ThreadID)
		if err != nil {
			return wire.ThreadsGetDiffResult{}, err
		}
		for _, turn := range turns {
			if turn.Status == "taken_back" {
				takenBack[turn.ID] = true
			}
			storeTurns[turn.ID] = turn
		}
	}

	neutralResult := func(reason string) (wire.ThreadsGetDiffResult, error) {
		return wire.ThreadsGetDiffResult{
			ThreadID:      params.ThreadID,
			TurnID:        params.TurnID,
			Diff:          "",
			Truncated:     false,
			FilesChanged:  []string{},
			Turns:         []wire.ReviewTurn{},
			FailureReason: &reason,
		}, nil
	}

	if gitRunner == nil {
		return neutralResult("git is not available")
	}
	if workDir == "" {
		return neutralResult("work directory not specified")
	}

	repoRoot, err := gitRunner.RepoRoot(ctx, workDir)
	if err != nil {
		return neutralResult("not a git repository")
	}

	prefix := fmt.Sprintf("refs/mcode/checkpoints/%s/", params.ThreadID)
	refs, err := gitRunner.ListRefs(ctx, repoRoot, prefix)
	if err != nil {
		return neutralResult("failed to list checkpoint refs: " + err.Error())
	}

	type turnRefPair struct {
		turnID   string
		startRef string
		endRef   string
	}
	turnMap := make(map[string]*turnRefPair)
	for _, ref := range refs {
		rest := strings.TrimPrefix(ref.Name, prefix)
		parts := strings.Split(rest, "/")
		if len(parts) != 2 {
			continue
		}
		tID, kind := parts[0], parts[1]
		if takenBack[tID] {
			continue
		}
		tr, exists := turnMap[tID]
		if !exists {
			tr = &turnRefPair{turnID: tID}
			turnMap[tID] = tr
		}
		if kind == "start" {
			tr.startRef = ref.Name
		} else if kind == "end" {
			tr.endRef = ref.Name
		}
	}

	// 收集会话历史消息中涉及的 turnID 的出现顺序
	var orderedTurnIDs []string
	turnIndices := make(map[string]int)
	turnStatus := make(map[string]string)
	seenTurn := make(map[string]bool)

	for _, msg := range msgs {
		if msg.TurnID != nil && *msg.TurnID != "" {
			tID := *msg.TurnID
			if !seenTurn[tID] {
				seenTurn[tID] = true
				orderedTurnIDs = append(orderedTurnIDs, tID)
				turnIndices[tID] = msg.TurnIndex
			}
			if msg.Kind != nil && *msg.Kind == "error" {
				turnStatus[tID] = "failed"
			}
		}
	}

	var orderedTurns []*turnRefPair
	processed := make(map[string]bool)

	// 首先按会话消息中 turn 的出现顺序排序
	for _, tID := range orderedTurnIDs {
		if tr, ok := turnMap[tID]; ok {
			orderedTurns = append(orderedTurns, tr)
			processed[tID] = true
		}
	}

	// 对于不在消息历史中但在 Git 检查点中存在的 turn（例如测试或未记录消息的轮次），
	// 按 turnID 升序排序作为保底，确保返回顺序确定性
	var remaining []*turnRefPair
	for tID, tr := range turnMap {
		if !processed[tID] {
			remaining = append(remaining, tr)
		}
	}
	if len(remaining) > 0 {
		sort.Slice(remaining, func(i, j int) bool {
			return remaining[i].turnID < remaining[j].turnID
		})
		for _, tr := range remaining {
			orderedTurns = append(orderedTurns, tr)
		}
	}

	// 检查点优先取 store 中持久化的字段，缺失时回退到 refs，兼容旧会话数据。
	turnStart := func(tr *turnRefPair) string {
		if st, ok := storeTurns[tr.turnID]; ok && st.BaseCheckpoint != "" {
			return st.BaseCheckpoint
		}
		return tr.startRef
	}
	turnFinish := func(tr *turnRefPair) string {
		if st, ok := storeTurns[tr.turnID]; ok && st.FinishCheckpoint != "" {
			return st.FinishCheckpoint
		}
		return tr.endRef
	}
	// turnPaths 返回该轮记录的文件集合（相对仓库根目录规范化）；空集合表示历史数据缺失。
	turnPaths := func(turnID string) []string {
		st, ok := storeTurns[turnID]
		if !ok || len(st.TouchedFiles) == 0 {
			return nil
		}
		return normalizeTouchedFiles(repoRoot, repoRoot, st.TouchedFiles)
	}

	// 构造 turns 摘要列表（只统计最近 turnMetadataLimit 轮：截断后的轮次不会下发，
	// 长会话里为每一轮起一个 git numstat 子进程代价过高）。
	statTurns := orderedTurns
	if len(statTurns) > turnMetadataLimit {
		statTurns = statTurns[:turnMetadataLimit]
	}
	var turns []wire.ReviewTurn
	for idx, tr := range statTurns {
		tIdx := idx
		if ti, ok := turnIndices[tr.turnID]; ok {
			tIdx = ti
		}
		status := "completed"
		if isRunning && runningTurnID == tr.turnID {
			status = "running"
		} else if st, ok := turnStatus[tr.turnID]; ok {
			status = st
		}

		hasChanges := false
		if start := turnStart(tr); start != "" {
			stats, sErr := gitRunner.NumStat(ctx, repoRoot, start, turnFinish(tr), turnPaths(tr.turnID))
			if sErr == nil && len(stats) > 0 {
				hasChanges = true
			}
		}

		turns = append(turns, wire.ReviewTurn{
			TurnID:     tr.turnID,
			Index:      tIdx,
			Status:     status,
			HasChanges: hasChanges,
		})
	}
	if turns == nil {
		turns = []wire.ReviewTurn{}
	}

	var metadataTruncated *bool
	if len(orderedTurns) > turnMetadataLimit {
		mt := true
		metadataTruncated = &mt
	}

	// 计算 diff 和 filesChanged
	var (
		diffFrom     string
		diffTo       string
		diffText     string
		truncated    bool
		diffPaths    []string
		filesChanged = []string{}
	)

	const maxDiffBytes = 512 * 1024

	if params.TurnID != nil && *params.TurnID != "" {
		tID := *params.TurnID
		if tr, ok := turnMap[tID]; ok {
			diffFrom = turnStart(tr)
			diffTo = turnFinish(tr) // 若该轮没有结束检查点，则与当前工作区比较
		} else if st, ok := storeTurns[tID]; ok && !takenBack[tID] {
			diffFrom = st.BaseCheckpoint
			diffTo = st.FinishCheckpoint
		}
		diffPaths = turnPaths(tID)
	} else if len(orderedTurns) > 0 {
		firstIdx := -1
		for i, tr := range orderedTurns {
			if turnStart(tr) != "" {
				firstIdx = i
				break
			}
		}
		if firstIdx >= 0 {
			diffFrom = turnStart(orderedTurns[firstIdx])
			lastEndIdx := -1
			for i, tr := range orderedTurns {
				if end := turnFinish(tr); end != "" {
					lastEndIdx = i
					diffTo = end
				}
			}
			endIdx := lastEndIdx
			if endIdx < firstIdx {
				// 没有任何轮次记录结束检查点：退回与当前工作区比较。
				diffTo = ""
				endIdx = len(orderedTurns) - 1
			}
			// 只有区间内所有轮次都有 TouchedFiles 时才按路径过滤；历史数据缺少该字段时
			// 退回全库 diff，避免旧会话因过滤变成空态。
			complete := true
			var union []string
			for _, tr := range orderedTurns[firstIdx : endIdx+1] {
				paths := turnPaths(tr.turnID)
				if len(paths) == 0 {
					complete = false
					break
				}
				union = append(union, paths...)
			}
			if complete {
				diffPaths = normalizeTouchedFiles(repoRoot, repoRoot, union)
			}
		}
	}

	if diffFrom != "" {
		dt, trunc, dErr := gitRunner.DiffText(ctx, repoRoot, diffFrom, diffTo, diffPaths, maxDiffBytes)
		if dErr != nil {
			return neutralResult("git diff failed: " + dErr.Error())
		}
		diffText = dt
		truncated = trunc

		stats, sErr := gitRunner.NumStat(ctx, repoRoot, diffFrom, diffTo, diffPaths)
		if sErr == nil {
			seenPaths := make(map[string]bool)
			for _, st := range stats {
				if st.Path != "" && !seenPaths[st.Path] {
					seenPaths[st.Path] = true
					filesChanged = append(filesChanged, st.Path)
				}
				if st.OldPath != "" && !seenPaths[st.OldPath] {
					seenPaths[st.OldPath] = true
					filesChanged = append(filesChanged, st.OldPath)
				}
			}
			sort.Strings(filesChanged)
		}
	}

	return wire.ThreadsGetDiffResult{
		ThreadID:          params.ThreadID,
		TurnID:            params.TurnID,
		Diff:              diffText,
		Truncated:         truncated,
		FilesChanged:      filesChanged,
		Turns:             turns,
		MetadataTruncated: metadataTruncated,
	}, nil
}

// DrainQueuedFollowUps 在主代理空闲时把此前排队的汇报推进成一轮回复。
// 线程不存在、正在运行或队列为空时直接返回 nil，不产生副作用。
func (b *MemoryBackend) DrainQueuedFollowUps(ctx context.Context, threadID string) error {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok || td.summary.IsRunning || td.runTurnID != "" || td.runCancel != nil || td.takingBack || len(td.followUps) == 0 {
		b.mu.Unlock()
		return nil
	}
	nextStarted, nextText, queueChanged := b.startNextQueuedTurnLocked(td, true, true)
	if nextStarted == nil || !queueChanged {
		b.mu.Unlock()
		return nil
	}
	finalSummary := nextStarted.currentSummary
	b.mu.Unlock()

	b.hub.Publish(threadID, nil, nil, finalSummary)
	// 正文先落库再删队列行，崩溃时最多重复出现一条已写正文的排队项，不会丢消息。
	b.persistMessages(threadID, []wire.ThreadMessage{nextStarted.userMsg})
	b.recordDispatchingForTurn(threadID, nextStarted)
	b.commitQueueChange(threadID)
	b.hub.Flush(threadID)
	if nextStarted.titleChanged {
		b.persistThreadTitle(threadID, nextStarted.currentSummary.Title)
	}
	b.hub.BeginRun(threadID)
	b.hub.Publish(threadID, []wire.ThreadMessage{nextStarted.userMsg}, nil, nextStarted.currentSummary)
	b.launchTurnRun(threadID, nextStarted, nextText)
	return nil
}

// startNextQueuedTurnLocked 只有附件准备成功才移除排队项；失败时保留供重试。
// allowQueue 为真时普通排队项可以出队。allowPriority 为真时，队首的 priority 条目
// （swarm 汇报与提问转交）即使 allowQueue 为假也能立即开轮，用于 lead 一轮正常结束但仍有
// 子代理在跑的情况；用户打断或本轮出错结束时调用方传 false，priority 条目留在队列里，
// 等用户下一句话或 DrainQueuedFollowUps 再投递，避免用户按了停止后主代理又自己跑起来。
func (b *MemoryBackend) startNextQueuedTurnLocked(td *memoryThread, allowQueue, allowPriority bool) (*startedTurn, string, bool) {
	b.ensureMessagesLoadedLocked(td)
	if td.takingBack {
		return nil, "", false
	}
	var selected []wire.FollowUp
	fromPendingSend := td.pendingSend != nil
	if fromPendingSend {
		selected = []wire.FollowUp{*td.pendingSend}
	} else if len(td.followUps) > 0 && (allowQueue || (allowPriority && td.isPriorityFollowUpLocked(td.followUps[0]))) {
		// priority 条目（swarm 汇报与提问转交）在 allowPriority 为真时不受 allowQueue 限制：只排在
		// 正在运行的那一轮之后，否则子代理提问会一直等主代理回答，而主代理等子代理收尾，形成死锁。
		count := 1
		first := td.followUps[0]
		if first.BundleID != nil && *first.BundleID != "" {
			for count < len(td.followUps) {
				next := td.followUps[count]
				if next.BundleID == nil || *next.BundleID != *first.BundleID {
					break
				}
				count++
			}
		}
		selected = td.followUps[:count]
	} else {
		return nil, "", false
	}
	var attIDs []string
	for _, fu := range selected {
		for _, att := range fu.Attachments {
			attIDs = append(attIDs, att.ID)
		}
	}
	// 出队前先取出排队项上记下的 Kind（如 host_event），写库时跟着本轮用户消息一起存。
	queuedKind := ""
	for _, fu := range selected {
		if fu.Kind != "" {
			queuedKind = fu.Kind
			break
		}
	}
	media, paths, err := b.consumeAttachmentsLocked(td.summary.ID, attIDs, true)
	if err != nil {
		log.Printf("consume attachments error on dequeue for thread %s: %v", td.summary.ID, err)
		return nil, "", false
	}
	if td.pendingSend != nil {
		td.pendingSend = nil
	} else {
		for _, fu := range selected {
			delete(td.priorityFollowUps, fu.ID)
		}
		td.followUps = normalizeBundles(td.followUps[len(selected):])
		if len(td.followUps) == 0 {
			// 队列清空时一并回收 priority 标记，避免已出队（或被删除/改写）的 ID 长期残留。
			td.priorityFollowUps = nil
		}
	}
	if td.queueRevision == 0 {
		td.queueRevision = 1
	}
	td.queueRevision++
	text := bundleFollowUpText(selected)
	st := b.startTurnLocked(td, text, queuedKind)
	// 投递状态行只记第一条排队项；审计事件仍按每个排队项逐条写 dispatching。
	b.bindDeliveryLocked(td, st, selected[0].ID)
	dispatchReason := ""
	if fromPendingSend {
		dispatchReason = "pending_send"
	}
	for _, fu := range selected {
		td.recordFollowUpEvents(newFollowUpEvent(store.FollowUpEventDispatching, dispatchReason, fu, st.turnID))
	}
	st.userMsg.Media = media
	td.messages[len(td.messages)-1].Media = media
	st.imagePaths = paths
	st.imageNames = mediaRefNames(media)
	return st, text, true
}

// commitQueueChange 在持锁复制快照后释放锁，并写穿至 store 及触发 queueChangedHook。
func (b *MemoryBackend) commitQueueChange(threadID string) {
	b.queuePersistMu.Lock()
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		b.queuePersistMu.Unlock()
		return
	}
	if td.queueRevision == 0 {
		td.queueRevision = 1
	}
	revision := td.queueRevision
	// 事件与快照在同一把锁下取出，避免事件与队列快照错位；未挂载 store 时事件随本次提交一起丢弃。
	events := td.pendingFollowUpEvents
	td.pendingFollowUpEvents = nil
	fus := td.queueSnapshotFollowUpsLocked()
	snap := wire.ThreadsQueueSnapshot{
		ThreadID:  threadID,
		Revision:  revision,
		FollowUps: fus,
	}
	// pendingSend 对客户端已出队，但真正开轮前仍须持久化，以免关闭时丢失。
	if td.pendingSend != nil {
		fus = append([]wire.FollowUp{*td.pendingSend}, fus...)
	}
	s := b.store
	hook := b.queueChangedHook
	var listeners []func(wire.ThreadsQueueSnapshot)
	if len(b.queueListeners) > 0 {
		listeners = make([]func(wire.ThreadsQueueSnapshot), 0, len(b.queueListeners))
		for _, fn := range b.queueListeners {
			if fn != nil {
				listeners = append(listeners, fn)
			}
		}
	}
	b.mu.Unlock()

	if s != nil {
		storeItems := make([]store.FollowUp, 0, len(fus))
		for i, fu := range fus {
			createdAt, err := parseTime(fu.CreatedAt)
			if err != nil || createdAt.IsZero() {
				createdAt = time.Now().UTC()
			}
			attIDs := make([]string, 0, len(fu.Attachments))
			attMetas := make([]store.FollowUpAttachment, 0, len(fu.Attachments))
			for _, att := range fu.Attachments {
				attIDs = append(attIDs, att.ID)
				meta := store.FollowUpAttachment{
					ID:       att.ID,
					Name:     att.Name,
					MimeType: att.MimeType,
				}
				if att.ByteCount != nil {
					meta.ByteCount = *att.ByteCount
				}
				if b.attachments != nil {
					if rec, ok := b.attachments.get(att.ID); ok {
						meta.Path = rec.path
						if meta.Name == "" {
							meta.Name = rec.name
						}
						if meta.MimeType == "" {
							meta.MimeType = rec.mimeType
						}
						if meta.ByteCount == 0 {
							meta.ByteCount = rec.byteCount
						}
					}
				}
				attMetas = append(attMetas, meta)
			}
			storeItems = append(storeItems, store.FollowUp{
				ID:             fu.ID,
				ThreadID:       threadID,
				Position:       i,
				Text:           fu.Text,
				Kind:           fu.Kind,
				Intent:         fu.Intent,
				Attachments:    attIDs,
				AttachmentMeta: attMetas,
				CreatedAt:      createdAt,
				UpdatedAt:      time.Now().UTC(),
			})
		}
		pCtx, pCancel := persistCtx()
		if err := s.ReplaceFollowUpsWithAudit(pCtx, threadID, int64(revision), storeItems, events); err != nil {
			log.Printf("store replace follow-ups error for thread %s: %v", threadID, err)
		}
		pCancel()
	}

	b.queuePersistMu.Unlock()
	if hook != nil {
		hook(snap)
	}
	for _, fn := range listeners {
		fn(snap)
	}
}

// resetThreadModel 把线程模型改成 model 并发布变更、写穿到 store；线程不存在时什么都不做。
func (b *MemoryBackend) resetThreadModel(threadID, model string) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return
	}
	m := model
	td.summary.Model = &m
	td.summary.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	currentSummary := b.summaryForPublishLocked(td)
	hook := b.threadsChangedHook
	b.mu.Unlock()

	b.persistThreadModel(threadID, model)
	b.hub.Publish(threadID, nil, nil, currentSummary)
	if hook != nil {
		hook(wire.ThreadsChanged{Threads: []wire.ThreadSummary{currentSummary}})
	}
}
