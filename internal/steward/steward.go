// Package steward 实现「总管」（steward）常驻会话的运行时。
//
// 总管是一个长期驻留的同事会话：宿主在其他会话卡住、出错、额度用尽、看板任务失败或待审批、
// 自动化失败、Bee 失败时把待处理事件按批发给它，它用 attention_list 等专属 MCP 工具分诊，
// 并通过 notify_owner 只把需要用户拍板的事告诉用户。本包负责三件事：创建并复用总管会话、
// 把待处理事件聚合后投递给它（带每日唤醒上限与冷却时间）、以及在安静时段做兜底巡检。
// 总管可以通过四个动作工具催促会话、中断一轮、重跑自动化、移动看板任务，动作受宿主护栏约束；
// 写权限与普通会话一致，跟随用户的全局默认权限档位。
package steward

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"mcode/internal/attention"
	"mcode/internal/config"
	"mcode/internal/kanban"
	"mcode/internal/store"
)

// dateLayout 是唤醒计数按「本地日期」分桶时使用的日期格式。
const dateLayout = "2006-01-02"

// 总管会话使用的持久化设置键；计数与时间落在应用级键值设置里，进程重启后继续生效。
const (
	// settingThreadID 保存总管会话 ID。
	settingThreadID = "steward.thread_id"
	// settingWakeDay 保存唤醒计数所属的本地日期（YYYY-MM-DD）。
	settingWakeDay = "steward.wake_day"
	// settingWakeCount 保存 settingWakeDay 当天已唤醒次数。
	settingWakeCount = "steward.wake_count"
	// settingLastWakeAt 保存上次唤醒时刻（RFC3339，UTC），供冷却与兜底巡检判断。
	settingLastWakeAt = "steward.last_wake_at"
	// settingWakeLimitNoticePrefix 是「今日已到唤醒上限」说明的去重键前缀，按本地日期分桶，
	// 与话题回执的「一次做完就不再做」同一套 setting 去重写法：同一天只提示用户一次。
	settingWakeLimitNoticePrefix = "steward.wake_limit_notice."
)

// 唤醒节奏相关的常量。
const (
	// aggregateWindow 是第一条事件到来后等待聚合的时长，窗口内的同类事件合成一次唤醒。
	aggregateWindow = 20 * time.Second
	// ownerActionGrace 让会话等待用户审批/回答的事件先等这段时间；期间被消解就不唤醒总管，超时仍未处理才并入待投递批次。
	ownerActionGrace = 10 * time.Minute
	// minWakeGap 是两次唤醒之间的最小间隔，避免连续投递打断总管正在进行的处理。
	minWakeGap = 2 * time.Minute
	// maxBatchEvents 是一次唤醒消息最多携带的事件条数，超出的只提示条数。
	maxBatchEvents = 20
	// maxRideNotes 是顺风车通知最多保留的条数，超出时丢掉最旧的。
	maxRideNotes = 20
	// patrolInterval 是兜底巡检的检查间隔。
	patrolInterval = 10 * time.Minute
	// patrolWakeGap 是兜底巡检要求距上次唤醒的最小间隔。
	patrolWakeGap = 2 * time.Hour
	// detailMaxRunes 是唤醒消息里单条事件详情的截断长度（按 rune 计）。
	detailMaxRunes = 140
	// titleMaxRunes 是唤醒消息里单条事件标题的截断长度（按 rune 计）。
	titleMaxRunes = 60
	// eventsBuffer 是事件回调到聚合循环之间的缓冲深度；溢出只影响聚合，事件本身已落库。
	eventsBuffer = 128
	// sendTimeout 是单次投递（含建会话）的超时，避免后台循环永久阻塞。
	sendTimeout = 2 * time.Minute
	// userBusyDeferral 是「用户正在与总管打交道」时这一批唤醒的重查间隔：
	// 到点再问一次，用户停下来就照常投递，不丢事件。
	userBusyDeferral = time.Minute
	// wakeStateTimeout 是读唤醒状态（设置表）的超时，供 WakesRemainingToday 这类查询使用。
	wakeStateTimeout = 5 * time.Second
	// deliveryRetryFirst/Second/Third 是同一批唤醒连续投递失败后的重试间隔，按次序使用。
	// 三档都退避过仍失败就放弃这一批，避免后台循环对着一个坏会话无限重试。
	deliveryRetryFirst  = 30 * time.Second
	deliveryRetrySecond = 2 * time.Minute
	deliveryRetryThird  = 10 * time.Minute
	// maxCarryOver 是被每日上限挡下、留到次日第一次唤醒时补上的事项条数上限，超出时丢掉最旧的。
	maxCarryOver = 50
)

// deliveryRetryBackoffs 是按次序使用的重试间隔，下标从 0 起：第 1 次失败等 30 秒，第 2 次 2 分钟，第 3 次 10 分钟。
var deliveryRetryBackoffs = []time.Duration{deliveryRetryFirst, deliveryRetrySecond, deliveryRetryThird}

// StewardRuntimeMode 是创建总管与话题会话时使用的权限模式。
//
// 空值表示跟随全局默认权限档位：后端遇到空值不写入固定档位，落库时按全局档位解析
// （internal/remote/memory_backend.go 的 defaultRuntimeModeLocked：未设置视为 autoAcceptEdits，
// 全局只读时为 supervised，其余全局档位照搬）。总管与普通会话一样会弹审批卡，风险动作仍由用户拍板。
const StewardRuntimeMode = ""

// Events 是总管依赖的待处理事件能力；*attention.Service 满足该接口。
type Events interface {
	// Watch 订阅事件变更，返回取消订阅的函数；消解通知的 ResolvedAt 非空。
	Watch(fn func(attention.Event)) (stop func())
	// List 按过滤条件列出待处理事件。
	List(ctx context.Context, filter store.AttentionEventFilter) ([]attention.Event, error)
	// UnresolvedCount 返回全部未消解事件的条数。
	UnresolvedCount(ctx context.Context) (int, error)
	// Resolve 消解一条事件，by 记录消解者。
	Resolve(ctx context.Context, id, by string) error
}

// EventsByKinds 是兜底巡检需要的按类型计数能力；*attention.Service 满足它。
// 单独定义而不并进 Events，是为了不要求只用于其他场景的测试替身也实现类型过滤。
type EventsByKinds interface {
	// UnresolvedCountByKinds 返回指定类型的未消解事件条数，kinds 为空表示不按类型过滤。
	UnresolvedCountByKinds(ctx context.Context, kinds []string) (int, error)
}

// Sessions 是总管依赖的会话能力；*remote.MemoryBackend 满足该接口。
type Sessions interface {
	// StartSession 新建代理会话；Prompt 为空时只建会话不发首条消息。
	StartSession(ctx context.Context, req kanban.SessionRequest) (threadID string, err error)
	// SessionState 返回会话的运行时状态，会话不存在时返回 store.ErrNotFound。
	SessionState(ctx context.Context, threadID string) (kanban.SessionState, error)
	// SendSwarmReport 向会话投递一条文本，会话运行中时排队。
	SendSwarmReport(ctx context.Context, threadID, text string) error
	// SendHostEvent 向会话投递一条宿主事件，落库消息的 Kind 为 host_event，会话运行中时排队。
	SendHostEvent(ctx context.Context, threadID, text string) error
	// SendStewardWake 向会话投递一条总管的唤醒触发，落库消息的 Kind 为 steward_wake、客户端不显示；会话运行中时排队。
	SendStewardWake(ctx context.Context, threadID, text string) error
	// AppendAssistantMessage 追加一条总管主动发出的助手消息，作为可见回复展示。
	AppendAssistantMessage(threadID, text string) error
}

// Settings 是总管依赖的应用级键值设置能力；*store.Store 满足该接口。
type Settings interface {
	// GetSetting 读取键值，键不存在时 ok 为 false。
	GetSetting(ctx context.Context, key string) (value string, ok bool, err error)
	// SetSetting 写入或覆盖键值。
	SetSetting(ctx context.Context, key, value string) error
}

// Deps 是 Runtime 的依赖集合；除 Now 外都必须提供。
type Deps struct {
	// Events 提供待处理事件的订阅、列表、计数与消解。
	Events Events
	// Sessions 提供总管会话的创建、状态查询与消息投递。
	Sessions Sessions
	// Settings 持久化总管会话 ID 与唤醒计数。
	Settings Settings
	// Config 返回当前的总管配置；为空时按未启用处理。
	Config func() config.StewardConfig
	// DataDir 是应用数据目录，用于在配置未指定主目录时解析 <dataDir>/steward。
	DataDir string
	// EnsureHomeProject 确保总管主目录对应一个项目并返回项目 ID，由 app 层提供。
	EnsureHomeProject func(ctx context.Context, dir string) (projectID string, err error)
	// ProjectRoot 把项目 ID 或目录解析成项目 ID 与项目根目录，作为话题会话的 ProjectID 与工作目录；由 app 层提供。
	ProjectRoot func(ctx context.Context, projectRef string) (projectID, dir string, err error)
	// Kanban 提供看板读取、目标创建与开工；为空时话题无法确认开工、也无法自审开工。
	Kanban Kanban
	// Names 提供总管会话标题与家目录项目名的读写，让旧版本的旧名字跟上当前配置名；
	// 为空时不做名字同步，只在新建会话时用当时的配置名当标题。
	Names Names
	// Now 是当前时间来源，测试可注入；为空时使用 time.Now。
	Now func() time.Time
	// RotateIdle 是主会话换新的空闲阈值，0 或负数表示关闭空闲换新。
	RotateIdle time.Duration
	// RotateContextTokens 是主会话换新的上下文阈值，0 或负数取 DefaultRotateContextTokens。
	RotateContextTokens int64
	// RecentBees 返回主会话最近派出的子代理（新到旧），用于换新前言；为空时前言不带这一小节。
	RecentBees func(leadThreadID string) []RotationBee
	// UserActedRecently 报告用户是否刚在总管会话里亲自操作（10 分钟内发过消息，或会话正在跑一轮），
	// 由 app 层注入；命中时这一批唤醒推迟投递，事件留在积压里等用户停下来，为空时不做推迟。
	UserActedRecently func(ctx context.Context, threadID string) bool
}

// Kanban 是话题依赖的看板能力；*kanban.Service 满足该接口。
// 只声明话题用到的六个方法，避免 steward 包跟着看板服务一起变形。
type Kanban interface {
	// Board 返回当前看板（含全部目标与任务）。
	Board(ctx context.Context) (kanban.Board, error)
	// CreateGoalWithOptions 创建目标并按选项触发后台规划。
	CreateGoalWithOptions(ctx context.Context, opts kanban.CreateGoalOptions) (store.Goal, error)
	// StartGoal 把处于计划待审的目标推进到执行，expectedRevision 用于乐观并发。
	StartGoal(ctx context.Context, goalID string, expectedRevision int) (store.Goal, error)
	// ReplanGoal 对规划失败的目标清掉待办任务并重新规划，expectedRevision 用于乐观并发。
	ReplanGoal(ctx context.Context, goalID, feedback string, expectedRevision int) (store.Goal, error)
	// CancelGoal 取消目标，expectedRevision 用于乐观并发；删除话题时用来收尾目标。
	CancelGoal(ctx context.Context, goalID string, expectedRevision int) (store.Goal, error)
	// InterruptTask 中断任务正在运行的一轮；删除话题时用来停下还在跑的执行线程。
	InterruptTask(ctx context.Context, task store.Task) error
}

// carryOverBatch 是被每日上限挡下的事件与单行通知，等日期切换后第一次成功唤醒时补上。
type carryOverBatch struct {
	events []attention.Event
	notes  []string
}

// Runtime 是总管会话的运行时：负责会话的创建与复用、事件聚合投递与兜底巡检。
// 并发安全；Start 与 Stop 可成对重复调用。
type Runtime struct {
	events        Events
	sessions      Sessions
	settings      Settings
	cfg           func() config.StewardConfig
	dataDir       string
	ensureProject func(ctx context.Context, dir string) (projectID string, err error)
	projectRoot   func(ctx context.Context, projectRef string) (projectID, dir string, err error)
	kanban        Kanban
	names         Names
	now           func() time.Time
	// recentBees 返回主会话最近派出的子代理，供换新前言使用；为空时前言不带这一小节。
	recentBees func(leadThreadID string) []RotationBee
	// userActedRecently 报告用户是否刚在总管会话里亲自操作；为空时不做推迟。
	userActedRecently func(ctx context.Context, threadID string) bool
	// after 是等待定时器的来源，默认 time.After，测试注入可控时钟。
	after func(d time.Duration) <-chan time.Time

	// mu 保护 threadID 与已同步的名字；ensureMu 串行化会话创建，避免并发 Ensure 建出两个总管会话。
	mu       sync.Mutex
	threadID string
	// syncedThreadID/syncedThreadName 与 syncedProjectDir/syncedProjectName 记住本进程
	// 上次同步成功的名字：读不到当前名字时用它判断，避免每次唤醒都写库。
	syncedThreadID    string
	syncedThreadName  string
	syncedProjectDir  string
	syncedProjectName string
	ensureMu          sync.Mutex
	// runMu 保护运行状态；runCtx 是 Start 创建、Stop 取消的运行 ctx，后台任务从它派生，
	// 这样 Stop 的 cancel 连同 wg.Wait 能收尾总管会话的准备。
	runMu     sync.Mutex
	running   bool
	runCtx    context.Context
	cancel    context.CancelFunc
	stopWatch func()
	wg        sync.WaitGroup

	// 以下字段只由聚合循环 goroutine 读写，不需要加锁。
	wakeDay   string
	wakeCount int
	lastWake  time.Time
	// deferredByQuiet 表示当前积压的事件曾因安静时段推迟投递，投递时提示总管合并汇总。
	deferredByQuiet bool
	// sendFailures 是当前这一批连续投递失败的次数，投递成功或放弃后归零。
	sendFailures int
	// carryOver 是被每日上限挡下、留到次日第一次成功唤醒时补上的事件与通知，carryOverDay 是
	// 它们被挡下时的本地日期。只放在内存里，重启后丢失可以接受：事件本身仍在收件箱，
	// 用户与总管都能从 attention_list 看到，只是不再自动并入唤醒消息。
	carryOver    carryOverBatch
	carryOverDay string

	// 话题表的进程内缓存与订阅者：缓存首次使用时从设置整表加载，之后由本进程的读改写维护，
	// 因此 IsTopic 这类每轮都要问的判定不必每次都读存储。
	topicsMu       sync.Mutex
	topicsCache    []Topic
	topicsLoaded   bool
	topicWatchers  map[int]func()
	nextTopicWatch int
	// topicWriteMu 串行化话题表的读改写：整张表一次写回，两个读改写交错就会互相覆盖、丢掉一份改动；
	// 只包住读表到写表这一段，建会话与投递宿主事件都在锁外。
	topicWriteMu sync.Mutex
	// topicConfirmMu 串行化确认开工：用户连点两次时，第二次必须走到「已开工」分支，不能各建一个目标。
	topicConfirmMu sync.Mutex

	// ledgerMu 串行化账本的读改写：工具调用与话题回执可能同时改账本，整表替换不能互相覆盖。
	ledgerMu sync.Mutex
	// receiptMu 串行化话题回执：结项与目标完成可能同时触发同一个话题的回执。
	receiptMu sync.Mutex
	// notes 是并入下一次唤醒批次的单行通知（目前是话题回执），由聚合循环消费。
	notes chan string
	// rideNotes 是顺风车通知：只随下一次事件唤醒送达，自己不触发唤醒。
	rideNotes chan string

	// passthrough 直通：队列、去重记录、看板索引缓存与按话题的合并窗口状态。
	// passIndex / passSeen 只由聚合循环读写；passPending / passLast 只由 passthroughLoop 读写。
	passCh      chan passthroughItem
	passSeen    map[string]time.Time
	passIndex   passthroughIndex
	passPending map[string][]passthroughItem
	passLast    map[string]time.Time

	// rotateIdle 与 rotateTokens 是主会话换新的空闲阈值与上下文阈值，见 rotation.go。
	rotateIdle   time.Duration
	rotateTokens int64
}

// New 创建总管运行时；未提供的 Now 用 time.Now 补齐。
func New(d Deps) *Runtime {
	r := &Runtime{
		events:            d.Events,
		sessions:          d.Sessions,
		settings:          d.Settings,
		cfg:               d.Config,
		dataDir:           d.DataDir,
		ensureProject:     d.EnsureHomeProject,
		projectRoot:       d.ProjectRoot,
		kanban:            d.Kanban,
		names:             d.Names,
		now:               d.Now,
		recentBees:        d.RecentBees,
		userActedRecently: d.UserActedRecently,
		notes:             make(chan string, eventsBuffer),
		rideNotes:         make(chan string, eventsBuffer),
		passCh:            make(chan passthroughItem, passthroughBuffer),
		passSeen:          make(map[string]time.Time),
		passPending:       make(map[string][]passthroughItem),
		passLast:          make(map[string]time.Time),
		rotateIdle:        d.RotateIdle,
		rotateTokens:      d.RotateContextTokens,
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.rotateTokens <= 0 {
		r.rotateTokens = DefaultRotateContextTokens
	}
	r.after = time.After
	return r
}

// ThreadID 返回当前总管会话 ID；尚未创建或已失效时为空。
func (r *Runtime) ThreadID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.threadID
}

// IsSteward 报告 threadID 是否就是总管会话；空 ID 一律不是。
// MCP 层用它决定是否向该会话暴露总管专属工具。
func (r *Runtime) IsSteward(threadID string) bool {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.threadID != "" && r.threadID == threadID
}

// IdentitySection 返回该会话的身份与职责段：不是总管会话也不是话题会话时返回空串。
// 宿主的提示词装饰器把它填进 policy.Sections.Steward，确保每一轮都带在当前配置的身份。
//
// 总管主会话拿到的是分诊职责加「收需求」；话题会话拿到的是当前阶段的指令（追问、规划、
// 计划待审、执行、结项），同一段提示词随阶段变化，总管因此不需要额外记住自己在哪一步。
func (r *Runtime) IdentitySection(threadID string) string {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return ""
	}
	cfg := r.config()
	if r.IsSteward(threadID) {
		return IdentitySection(cfg.Label())
	}
	ctx, cancel := context.WithTimeout(context.Background(), topicLookupTimeout)
	defer cancel()
	topic, ok, err := r.Topic(ctx, threadID)
	if err != nil || !ok {
		return ""
	}
	return TopicIdentitySection(cfg.Label(), topic, r.TopicPhase(ctx, topic))
}

// setThreadID 记录（或清空）总管会话 ID。
func (r *Runtime) setThreadID(threadID string) {
	r.mu.Lock()
	r.threadID = threadID
	r.mu.Unlock()
}

// config 现读一次总管配置；未注入配置读取函数时按零值（未启用）处理。
func (r *Runtime) config() config.StewardConfig {
	if r.cfg == nil {
		return config.StewardConfig{}
	}
	return r.cfg()
}

// Ensure 确保总管会话存在并返回其 ID：配置未启用时返回空 ID 且不报错；否则复用
// setting 里记录的会话，会话已丢失时在总管主目录下重建。可并发调用。
func (r *Runtime) Ensure(ctx context.Context) (string, error) {
	cfg := r.config()
	if !cfg.Enabled {
		return "", nil
	}

	r.ensureMu.Lock()
	defer r.ensureMu.Unlock()

	stored, err := r.storedThreadID(ctx)
	if err != nil {
		return "", err
	}
	if stored != "" {
		if _, stateErr := r.sessions.SessionState(ctx, stored); stateErr == nil {
			r.setThreadID(stored)
			// 旧版本可能用旧名字建过会话与项目：复用时就地改名，让界面标题栏、搜索与项目选择跟上配置。
			r.syncNames(ctx, cfg, stored)
			return stored, nil
		} else if !errors.Is(stateErr, store.ErrNotFound) {
			return "", fmt.Errorf("steward: 查询总管会话状态失败: %w", stateErr)
		}
		// 会话已不存在：继续往下重建，新 ID 会覆盖旧记录。
	}

	dir := cfg.ResolvedHomeDir(r.dataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("steward: 创建总管主目录 %s 失败: %w", dir, err)
	}
	if r.ensureProject == nil {
		return "", errors.New("steward: 未提供 EnsureHomeProject，无法定位总管项目")
	}
	projectID, err := r.ensureProject(ctx, dir)
	if err != nil {
		return "", fmt.Errorf("steward: 确保总管主目录项目失败: %w", err)
	}

	threadID, err := r.sessions.StartSession(ctx, kanban.SessionRequest{
		ProjectID: projectID,
		WorkDir:   dir,
		// 标题用总管当前的称呼（昵称 · 总管）；Prompt 留空：首条可见消息是总管的开场白，不是指令。
		// 不传 RuntimeMode：空值由后端按全局默认权限档位落库。
		Title:    cfg.Label(),
		Prompt:   "",
		Provider: cfg.Provider,
		Model:    cfg.Model,
		Effort:   cfg.Effort,
	})
	if err != nil {
		return "", fmt.Errorf("steward: 创建总管会话失败: %w", err)
	}
	if err := r.settings.SetSetting(ctx, settingThreadID, threadID); err != nil {
		return "", fmt.Errorf("steward: 记录总管会话 ID 失败: %w", err)
	}
	r.setThreadID(threadID)
	// 新建会话的标题已是配置名，但主目录可能沿用旧项目的旧名字（或旧版本建的项目），再对齐一次。
	r.syncNames(ctx, cfg, threadID)
	// 开场白失败不影响会话可用：用户仍可直接跟总管对话，宿主也会照常唤醒它。
	if err := r.sessions.AppendAssistantMessage(threadID, OpeningMessage(cfg.Label())); err != nil {
		log.Printf("steward: 写入开场白失败: %v", err)
	}
	return threadID, nil
}

// storedThreadID 读取 setting 里记录的总管会话 ID，并同步到内存缓存。
func (r *Runtime) storedThreadID(ctx context.Context) (string, error) {
	raw, ok, err := r.settings.GetSetting(ctx, settingThreadID)
	if err != nil {
		return "", fmt.Errorf("steward: 读取总管会话 ID 失败: %w", err)
	}
	if !ok {
		return "", nil
	}
	return strings.TrimSpace(raw), nil
}

// Start 启动事件订阅与聚合循环；重复调用无效。
func (r *Runtime) Start() {
	r.runMu.Lock()
	defer r.runMu.Unlock()
	if r.running {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.runCtx = ctx
	r.cancel = cancel
	r.running = true

	// 先把已持久化的会话 ID 读进缓存，IsSteward 在 Start 之后立刻可用。
	if id, err := r.storedThreadID(ctx); err != nil {
		log.Printf("steward: %v", err)
	} else if id != "" {
		r.setThreadID(id)
	}

	events := make(chan attention.Event, eventsBuffer)
	resolved := make(chan attention.Event, eventsBuffer)
	r.stopWatch = r.events.Watch(func(ev attention.Event) {
		// 消解通知撤下尚未投递的事件，并关闭账本里关联该对象的 pending_user 条目；
		// 送到聚合循环里处理，账本读写不会阻塞事件回调。
		if ev.ResolvedAt != nil {
			select {
			case resolved <- ev:
			default:
				log.Printf("steward: 消解通知缓冲已满，丢弃事件 %s 的账本关闭通知", ev.ID)
			}
			return
		}
		select {
		case events <- ev:
		default:
			// 缓冲满时丢弃回调只影响聚合与合并投递；事件本身已落库，兜底巡检与工具查询仍覆盖它。
			log.Printf("steward: 事件回调缓冲已满，丢弃事件 %s 的聚合通知", ev.ID)
		}
	})
	r.wg.Add(1)
	go r.loop(ctx, events, resolved)
	r.wg.Add(1)
	go r.passthroughLoop(ctx)
}

// EnsureInBackground 在后台准备总管会话，并把它登记到 Runtime 的 wait group 里。
//
// 建会话要走一次完整的 CLI 首轮启动，可能耗时数分钟，所以必须异步做；但不能像裸 goroutine
// 那样不受管理：宿主退出时 App.Close 先 Stop、再关 store，若这次准备还在跑，就会在关库之后
// 继续写数据库（测试里表现为「sql: database is closed」与 TempDir 清理「directory not empty」）。
// 登记之后，Stop 的 cancel 会把 ctx 连带取消，wg.Wait 会等它真正退出，关库时不会有残留写入。
// 未 Start（或已 Stop）时是空操作。
func (r *Runtime) EnsureInBackground(timeout time.Duration) {
	r.runMu.Lock()
	if !r.running || r.runCtx == nil {
		r.runMu.Unlock()
		return
	}
	parent := r.runCtx
	// 在 runMu 下 Add：Stop 持有 runMu 时才 Wait，因此 Add 不会与 Wait 并发。
	r.wg.Add(1)
	r.runMu.Unlock()

	go func() {
		defer r.wg.Done()
		ctx, cancel := context.WithTimeout(parent, timeout)
		defer cancel()
		threadID, err := r.Ensure(ctx)
		if err != nil {
			log.Printf("steward: 启动时准备总管会话失败: %v", err)
			return
		}
		if threadID != "" {
			log.Printf("steward: 总管会话已就绪（%s）", threadID)
		}
	}()
}

// Stop 停止事件订阅与聚合循环，并等待 goroutine 退出；可重复调用。
func (r *Runtime) Stop() {
	r.runMu.Lock()
	defer r.runMu.Unlock()
	if !r.running {
		return
	}
	r.running = false
	stop := r.stopWatch
	r.stopWatch = nil
	cancel := r.cancel
	r.cancel = nil
	r.runCtx = nil
	if stop != nil {
		stop()
	}
	if cancel != nil {
		cancel()
	}
	r.wg.Wait()
}

// loop 是聚合循环：收集事件、按 20 秒窗口与 2 分钟冷却投递唤醒、每 10 分钟做一次兜底巡检。
// 消解通知经 resolved 送达，撤下尚未投递的事件，并关闭账本里关联该对象的 pending_user 条目。
// 所有定时器都通过 r.after 与 r.now 取时间，Stop 取消 ctx 后立即退出。
func (r *Runtime) loop(ctx context.Context, events, resolved <-chan attention.Event) {
	defer r.wg.Done()
	r.loadWakeState(ctx)

	var (
		pending []attention.Event
		notes   []string
		rides   []string
		seen    = make(map[string]bool)
		flushAt time.Time
	)
	deferred := make(map[string]struct {
		event attention.Event
		dueAt time.Time
	})
	nextPatrol := r.now().Add(patrolInterval)

	queueEvent := func(ev attention.Event) {
		seen[ev.ID] = true
		if r.routeEvent(ctx, ev) {
			// 属于某个话题：已交给话题会话，主会话不再处理这条。
			return
		}
		pending = append(pending, ev)
		if flushAt.IsZero() {
			flushAt = r.now().Add(aggregateWindow)
		}
	}

	// resetBatch 清空当前批次与兜底计数：投递成功、整批丢弃或结转后都走它。
	resetBatch := func() {
		pending = nil
		notes = nil
		rides = nil
		seen = make(map[string]bool)
		flushAt = time.Time{}
		r.deferredByQuiet = false
		r.sendFailures = 0
	}

	for {
		now := r.now()

		for id, item := range deferred {
			if !now.Before(item.dueAt) {
				delete(deferred, id)
				queueEvent(item.event)
			}
		}

		// 1) 聚合窗口到点：处理冷却（不足 2 分钟则推迟），然后投递。
		if !flushAt.IsZero() && !now.Before(flushAt) {
			if gapEnd := r.lastWake.Add(minWakeGap); !r.lastWake.IsZero() && now.Before(gapEnd) {
				flushAt = gapEnd
				continue
			}
			// 安静时段内不投递：事件继续累积，每分钟现读配置重算一次，用户中途关闭或改时间最多一分钟生效。
			// 例外是审批、额度与失败这三类：安静时段里用户仍要知道，这几条照常投递，其余继续攒到时段结束。
			if inQuiet, quietEnd := r.config().QuietWindow(now); inQuiet {
				// 用户刚在总管会话里说过话（或总管正在跑一轮）：安静时段放行的三类也一起推迟，
				// 用户就在总管会话里，不差这一分钟；事件留在积压里不丢，下面的每分钟重查会照常送达。
				if !r.deferForUserActivity(ctx) {
					if pass, rest := splitQuietPassThrough(pending); len(pass) > 0 {
						// 只把成功放行的几条从积压里摘掉；发送失败就照旧留在积压里，下一分钟再试。
						if r.deliverBatch(ctx, pass, nil, nil, false) != deliveryFailed {
							pending = rest
							if len(pending) == 0 && len(notes) == 0 {
								seen = make(map[string]bool)
								flushAt = time.Time{}
								// 积压已清空，下一批不再标成「积压于安静时段」。
								r.deferredByQuiet = false
								continue
							}
						}
					}
				}
				r.deferredByQuiet = true
				flushAt = now.Add(time.Minute)
				if quietEnd.Before(flushAt) {
					flushAt = quietEnd
				}
				continue
			}
			// 用户刚在总管会话里说过话（或总管正在跑一轮）：这批推迟，事件留在积压里不丢，
			// 每分钟重查一次，用户停下来后照常送达。
			if r.deferForUserActivity(ctx) {
				flushAt = now.Add(userBusyDeferral)
				continue
			}
			switch r.deliverBatch(ctx, pending, notes, rides, r.deferredByQuiet) {
			case deliverySent, deliveryCapped, deliverySkipped:
				// 投递成功、达到上限已结转、配置被关闭：这一批都算处理完。
				resetBatch()
			case deliveryFailed:
				// 只有发送失败才重试：整批原样保留（含 seen 去重），退避期间进来的新事件
				// 合并进同一批；退避次数用完仍失败就放弃这一批，并告诉用户去收件箱查看。
				r.sendFailures++
				if r.sendFailures > len(deliveryRetryBackoffs) {
					r.notifyDeliveryFailed(ctx, len(pending)+len(notes))
					resetBatch()
				} else {
					flushAt = now.Add(deliveryRetryBackoffs[r.sendFailures-1])
				}
			}
			continue
		}

		// 2) 兜底巡检到点：有未消解事件且距上次唤醒满 2 小时时发一条巡检消息。
		if !now.Before(nextPatrol) {
			nextPatrol = now.Add(patrolInterval)
			r.patrol(ctx)
			continue
		}

		// 3) 等待下一个到期的定时器或新事件。
		deadline := nextPatrol
		if !flushAt.IsZero() && flushAt.Before(deadline) {
			deadline = flushAt
		}
		for _, item := range deferred {
			if item.dueAt.Before(deadline) {
				deadline = item.dueAt
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-r.after(deadline.Sub(now)):
			// 到点后回到循环顶部统一判定。
		case ev := <-resolved:
			// 消解通知不唤醒，撤下尚未投递的事件并关闭账本里等这个对象的条目。
			r.closePendingUserOnResolve(ctx, ev)
			delete(deferred, ev.ID)
			kept := pending[:0]
			for _, item := range pending {
				if item.ID != ev.ID {
					kept = append(kept, item)
				}
			}
			pending = kept
			if len(pending) == 0 && len(notes) == 0 {
				flushAt = time.Time{}
			}
		case ev := <-events:
			if !r.acceptEvent(ev, seen) {
				continue
			}
			// 其他批次可能已重置 seen，暂缓中的事件仍不能重复入队或延长宽限期。
			if _, ok := deferred[ev.ID]; ok {
				continue
			}
			if ev.Kind == string(attention.KindThreadApproval) {
				deferred[ev.ID] = struct {
					event attention.Event
					dueAt time.Time
				}{event: ev, dueAt: r.now().Add(ownerActionGrace)}
				seen[ev.ID] = true
				continue
			}
			queueEvent(ev)
		case note := <-r.notes:
			// 通知与事件共用聚合窗口与冷却：几个话题接连结项也只唤醒一次。
			notes = append(notes, note)
			if flushAt.IsZero() {
				flushAt = r.now().Add(aggregateWindow)
			}
		case ride := <-r.rideNotes:
			// 顺风车通知不设聚合窗口：它不自己触发唤醒，只在下一批投递时顺带送出。
			rides = append(rides, ride)
			if len(rides) > maxRideNotes {
				rides = rides[len(rides)-maxRideNotes:]
			}
		}
	}
}

// acceptEvent 判断一条事件是否值得立刻唤醒总管：消解通知不唤醒，总管自身会话的事件也丢弃，已见过的也不重复唤醒。
// 事件类型按配置的唤醒策略过滤：只有 wake 进入待投递批次，非 wake（即 patrol）都不立刻唤醒；
// 这类事件是否由兜底巡检提醒，由 patrolCountKinds 决定。
func (r *Runtime) acceptEvent(ev attention.Event, seen map[string]bool) bool {
	if ev.ResolvedAt != nil {
		return false
	}
	if strings.TrimSpace(ev.ID) == "" || seen[ev.ID] {
		return false
	}
	if r.config().EventPolicy(ev.Kind) != config.StewardEventWake {
		return false
	}
	return !r.IsSteward(ev.ThreadID)
}

// closePendingUserOnResolve 处理一条消解通知：关闭账本里关联该对象的 pending_user 条目，
// 让总管下次唤醒时知道这件事已经办完。总管自己通过 attention_resolve 发起的消解同样关闭条目，
// 但不投通知：那是总管已经知道的动作，不必再告诉自己一遍。
func (r *Runtime) closePendingUserOnResolve(ctx context.Context, ev attention.Event) {
	closed, err := r.closePendingUserForSubject(ctx, ev.SubjectType, ev.SubjectID)
	if err != nil {
		log.Printf("steward: 关闭事件 %s 关联的 pending_user 账本条目失败: %v", ev.ID, err)
		return
	}
	if len(closed) == 0 || ev.ResolvedBy == attention.ResolvedBySteward {
		return
	}
	// enqueueNote 只把通知并进下一批唤醒，不单独触发唤醒。
	for _, text := range closed {
		r.enqueueNote("用户已处理：" + text)
	}
}

// stewardEventKinds 列出总管可能收到的全部事件类型；新增 attention.Kind 常量时要同步加到这里。
var stewardEventKinds = []attention.Kind{
	attention.KindThreadStalled,
	attention.KindThreadFailed,
	attention.KindThreadQuota,
	attention.KindThreadApproval,
	attention.KindKanbanFailed,
	attention.KindKanbanNeedsApproval,
	attention.KindKanbanReview,
	attention.KindAutomationFailed,
	attention.KindAutomationFinished,
	attention.KindBeeFailed,
	attention.KindMergeReviewPending,
	attention.KindThreadNoProgress,
}

// patrolCountKinds 返回兜底巡检要统计的事件类型：唤醒策略为 wake，且不属于必须由用户
// 亲手处理的 attention.OwnerActionKinds。后者收件箱已经展示，总管不必反复催。
func (r *Runtime) patrolCountKinds() []string {
	ownerAction := make(map[string]bool, len(attention.OwnerActionKinds))
	for _, kind := range attention.OwnerActionKinds {
		ownerAction[string(kind)] = true
	}
	cfg := r.config()
	kinds := make([]string, 0, len(stewardEventKinds))
	for _, kind := range stewardEventKinds {
		name := string(kind)
		if ownerAction[name] || cfg.EventPolicy(name) != config.StewardEventWake {
			continue
		}
		kinds = append(kinds, name)
	}
	return kinds
}

// deliveryOutcome 是一次批量投递的结果：聚合循环据此决定重试、结转还是丢弃。
type deliveryOutcome int

const (
	// deliverySkipped 表示配置被关闭（或这一批没有内容），整批丢弃且不重试。
	deliverySkipped deliveryOutcome = iota
	// deliverySent 表示投递成功。
	deliverySent
	// deliveryFailed 表示发送失败，整批保留、退避后重试。
	deliveryFailed
	// deliveryCapped 表示达到每日上限，事件已结转到 carryOver，留到次日补上。
	deliveryCapped
)

// deliverBatch 投递一批聚合事件、单行通知与顺风车通知；deferred 表示这批事件积压于安静时段。
// 顺风车通知只随事件或通知的投递顺带送出，单独存在时不投递。
// 返回投递结果：关闭与达到上限不重试（上限会把整批结转到 carryOver），只有发送失败才要求重试。
func (r *Runtime) deliverBatch(ctx context.Context, events []attention.Event, notes, rides []string, deferred bool) deliveryOutcome {
	if len(events) == 0 && len(notes) == 0 {
		return deliverySkipped
	}
	cfg := r.config()
	if !cfg.Enabled {
		// 配置在此期间被关闭：整批丢弃，不再投递。
		return deliverySkipped
	}
	today := r.now().Format(dateLayout)
	if r.wakeDay != today {
		r.wakeDay = today
		r.wakeCount = 0
	}
	limit := cfg.WakeLimit()
	if r.wakeCount >= limit {
		log.Printf("steward: 今日处理上限已到 %d 次，把 %d 条待处理事件与 %d 条通知结转明天", limit, len(events), len(notes))
		// 兑现「剩下的我先记着」：整批结转到次日第一次成功唤醒时补上，不在今天反复重试。
		r.stashCarryOver(events, notes)
		r.notifyWakeLimit(ctx, today, limit)
		return deliveryCapped
	}

	// 日期切换后把昨天被上限挡下的事项并入本批；同一事件 ID 不重复加入。
	events, notes, carried := r.mergeCarryOver(events, notes, today)

	var text string
	if len(events) > 0 {
		text = buildWakeMessage(events, deferred)
		if len(notes) > 0 {
			text += "\n\n" + buildNotesSection(notes)
		}
	} else {
		text = buildNotesMessage(notes)
	}
	if carried > 0 {
		text += fmt.Sprintf("\n\n以下是昨天超过上限后留到今天的事项，共 %d 条，请一并处理。", carried)
	}
	if len(rides) > 0 {
		text += "\n\n" + buildRideSection(rides)
	}
	if !r.send(ctx, today, text) {
		return deliveryFailed
	}
	if carried > 0 {
		// 已补上：结转清空，避免下一天重复列出这些事项。
		r.carryOver = carryOverBatch{}
		r.carryOverDay = ""
	}
	return deliverySent
}

// mergeCarryOver 在日期切换后把被上限挡下的结转事项并进本批，返回合并后的事件、通知与并入条数。
// 同一天不合并（上限仍在），事件 ID 已在本批里时不重复加入；carried 为 0 表示没有并进任何东西。
func (r *Runtime) mergeCarryOver(events []attention.Event, notes []string, today string) ([]attention.Event, []string, int) {
	if len(r.carryOver.events) == 0 && len(r.carryOver.notes) == 0 {
		return events, notes, 0
	}
	if r.carryOverDay == "" || r.carryOverDay == today {
		return events, notes, 0
	}
	known := make(map[string]bool, len(events))
	for _, ev := range events {
		known[ev.ID] = true
	}
	carried := 0
	merged := make([]attention.Event, 0, len(r.carryOver.events)+len(events))
	for _, ev := range r.carryOver.events {
		if known[ev.ID] {
			continue
		}
		known[ev.ID] = true
		merged = append(merged, ev)
		carried++
	}
	merged = append(merged, events...)
	mergedNotes := make([]string, 0, len(r.carryOver.notes)+len(notes))
	mergedNotes = append(mergedNotes, r.carryOver.notes...)
	mergedNotes = append(mergedNotes, notes...)
	carried += len(r.carryOver.notes)
	return merged, mergedNotes, carried
}

// stashCarryOver 把被每日上限挡下的事件与通知记进 carryOver：同一事件 ID 去重，
// 总数超过 maxCarryOver 时丢掉最旧的。carryOver 只放在内存里，重启后丢失可以接受：
// 事件本身仍在收件箱里，用户与总管都能从 attention_list 看到。
func (r *Runtime) stashCarryOver(events []attention.Event, notes []string) {
	if len(events) == 0 && len(notes) == 0 {
		return
	}
	r.carryOverDay = r.now().Format(dateLayout)
	known := make(map[string]bool, len(r.carryOver.events))
	for _, ev := range r.carryOver.events {
		known[ev.ID] = true
	}
	for _, ev := range events {
		if known[ev.ID] {
			continue
		}
		known[ev.ID] = true
		r.carryOver.events = append(r.carryOver.events, ev)
	}
	r.carryOver.notes = append(r.carryOver.notes, notes...)
	if len(r.carryOver.events) > maxCarryOver {
		r.carryOver.events = r.carryOver.events[len(r.carryOver.events)-maxCarryOver:]
	}
	if len(r.carryOver.notes) > maxCarryOver {
		r.carryOver.notes = r.carryOver.notes[len(r.carryOver.notes)-maxCarryOver:]
	}
}

// patrol 做一次兜底巡检：安静时段内、用户正在与总管打交道、没有总管该管的未消解事件
// 或距上次唤醒不足 2 小时时不打扰用户。
func (r *Runtime) patrol(ctx context.Context) {
	cfg := r.config()
	if !cfg.Enabled {
		return
	}
	if inQuiet, _ := cfg.QuietWindow(r.now()); inQuiet {
		return
	}
	// 只统计总管该管的事：用户亲手处理的审批与验收（attention.OwnerActionKinds）
	// 以及配置成 patrol 的事件都不算，否则同一件事会被反复催，还白耗每日唤醒额度。
	kinds := r.patrolCountKinds()
	if len(kinds) == 0 {
		return
	}
	counter, ok := r.events.(EventsByKinds)
	if !ok {
		log.Printf("steward: 事件来源不支持按类型计数，跳过兜底巡检")
		return
	}
	// 只问条数：巡检不需要事件明细，用计数查询省掉一次整表列举。
	count, err := counter.UnresolvedCountByKinds(ctx, kinds)
	if err != nil {
		log.Printf("steward: 统计未消解事件失败: %v", err)
		return
	}
	if count <= 0 {
		return
	}
	if !r.lastWake.IsZero() && r.now().Sub(r.lastWake) < patrolWakeGap {
		return
	}
	// 用户刚在总管会话里说过话（或总管正在跑一轮）时也不插话：
	// 巡检下一轮再来，不会因为这一次推迟而丢掉未消解事件。
	if r.deferForUserActivity(ctx) {
		return
	}
	today := r.now().Format(dateLayout)
	if r.wakeDay != today {
		r.wakeDay = today
		r.wakeCount = 0
	}
	limit := cfg.WakeLimit()
	if r.wakeCount >= limit {
		log.Printf("steward: 今日处理上限已到 %d 次，跳过兜底巡检（仍有 %d 条未消解事件）", limit, count)
		r.notifyWakeLimit(ctx, today, limit)
		return
	}
	r.send(ctx, today, buildPatrolMessage(count))
}

// send 把一条消息投递给总管会话，成功时推进当日计数并持久化唤醒状态。
func (r *Runtime) send(ctx context.Context, today, text string) bool {
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	threadID, err := r.Ensure(sendCtx)
	if err != nil {
		log.Printf("steward: 确保总管会话失败: %v", err)
		return false
	}
	if threadID == "" {
		return false
	}
	if err := r.sessions.SendStewardWake(sendCtx, threadID, text); err != nil {
		log.Printf("steward: 投递总管消息失败: %v", err)
		return false
	}

	r.wakeCount++
	r.lastWake = r.now()
	r.persistWakeState(sendCtx, today)
	return true
}

// sendVisible 把一条用户可见的助手消息写进总管会话，不推进当日唤醒计数。
// 用于「今天不再主动找你」这类说明：用户要能在会话里看到，但它不是一次唤醒，
// 因此不能算进每日上限，否则说明自身会把上限越用越紧。
func (r *Runtime) sendVisible(ctx context.Context, text string) bool {
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	threadID, err := r.Ensure(sendCtx)
	if err != nil {
		log.Printf("steward: 确保总管会话失败: %v", err)
		return false
	}
	if threadID == "" {
		return false
	}
	if err := r.sessions.AppendAssistantMessage(threadID, text); err != nil {
		log.Printf("steward: 写总管会话消息失败: %v", err)
		return false
	}
	return true
}

// notifyWakeLimit 在当天第一次撞到每日唤醒上限时给用户一句说明，同一天只发一次。
// 去重键按本地日期分桶（setting.wake_limit_notice.YYYY-MM-DD），跨重启仍然只有一条。
func (r *Runtime) notifyWakeLimit(ctx context.Context, today string, limit int) {
	key := settingWakeLimitNoticePrefix + today
	if _, ok, err := r.settings.GetSetting(ctx, key); err != nil {
		log.Printf("steward: 读取今日上限提示标记失败: %v", err)
		return
	} else if ok {
		return
	}
	text := fmt.Sprintf("今天已经主动找你 %d 次了，到了上限；剩下的我先记着，明天第一次唤醒时一并说。"+
		"你可以在「设置 → 总管」里调高上限。", limit)
	if !r.sendVisible(ctx, text) {
		return
	}
	if err := r.settings.SetSetting(ctx, key, "1"); err != nil {
		log.Printf("steward: 记录今日上限提示失败: %v", err)
	}
}

// notifyDeliveryFailed 在放弃一批连续投递失败的事件时告诉用户：这批事项仍在收件箱里，
// 只是总管没收到唤醒。走可见助手消息，不计入每日唤醒次数。
func (r *Runtime) notifyDeliveryFailed(ctx context.Context, count int) {
	text := fmt.Sprintf("%s唤醒连续投递失败，%d 件待处理事项请到收件箱查看。", r.config().Label(), count)
	if !r.sendVisible(ctx, text) {
		log.Printf("steward: 报告唤醒投递失败失败（%d 件待处理事项）", count)
	}
}

// WakesRemainingToday 返回今天已经主动找过用户的次数与每日上限，供设置页显示今日剩余次数。
// 次数以持久化的当日计数为准（与唤醒循环同一口径，跨日、跨进程都成立）；读不到时按 0 次。
func (r *Runtime) WakesRemainingToday() (used, limit int) {
	limit = r.config().WakeLimit()
	if r.settings == nil {
		return 0, limit
	}
	ctx, cancel := context.WithTimeout(context.Background(), wakeStateTimeout)
	defer cancel()

	day, ok, err := r.settings.GetSetting(ctx, settingWakeDay)
	if err != nil || !ok || strings.TrimSpace(day) != r.now().Format(dateLayout) {
		return 0, limit
	}
	raw, ok, err := r.settings.GetSetting(ctx, settingWakeCount)
	if err != nil || !ok {
		return 0, limit
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(raw))
	if convErr != nil || n < 0 {
		return 0, limit
	}
	return n, limit
}

// deferForUserActivity 报告这一批唤醒是否应因用户正在与总管打交道而推迟。
// 判定由 app 层注入（UserActedRecently：用户 10 分钟内在总管会话里发过消息，或会话正在跑一轮）；
// 未注入时不推迟。会话 ID 还没解析出来时也照样问一次注入方：它自己会按「还没有会话就不算用户在操作」
// 返回 false（见 internal/app/steward_wiring.go 的 stewardUserActedRecently），这里再拦一道只会让
// 首次投递前的推迟判定与注入方口径不一致。
func (r *Runtime) deferForUserActivity(ctx context.Context) bool {
	if r.userActedRecently == nil {
		return false
	}
	return r.userActedRecently(ctx, r.ThreadID())
}

// quietPassKinds 是安静时段内仍然照常提醒用户的事件类型：等待审批、额度用尽、执行失败。
// 这三类不处理就会一直卡着，其余事件在时段内只积压，时段结束后一并送达。
var quietPassKinds = map[string]bool{
	string(attention.KindThreadApproval): true,
	string(attention.KindThreadQuota):    true,
	string(attention.KindThreadFailed):   true,
}

// isQuietPassKind 报告事件类型是否可以在安静时段内照常提醒用户。
func isQuietPassKind(kind string) bool {
	return quietPassKinds[strings.TrimSpace(kind)]
}

// splitQuietPassThrough 把积压事件拆成「安静时段也要送」与「留到时段结束」两组，各自保持原顺序。
func splitQuietPassThrough(events []attention.Event) (pass, rest []attention.Event) {
	for _, ev := range events {
		if isQuietPassKind(ev.Kind) {
			pass = append(pass, ev)
			continue
		}
		rest = append(rest, ev)
	}
	return pass, rest
}

// persistWakeState 把当日日期、当日计数与上次唤醒时刻写回设置；失败只记日志，不影响已投递的消息。
func (r *Runtime) persistWakeState(ctx context.Context, today string) {
	if err := r.settings.SetSetting(ctx, settingWakeDay, today); err != nil {
		log.Printf("steward: 记录唤醒日期失败: %v", err)
	}
	if err := r.settings.SetSetting(ctx, settingWakeCount, strconv.Itoa(r.wakeCount)); err != nil {
		log.Printf("steward: 记录唤醒次数失败: %v", err)
	}
	if err := r.settings.SetSetting(ctx, settingLastWakeAt, r.lastWake.UTC().Format(time.RFC3339)); err != nil {
		log.Printf("steward: 记录上次唤醒时间失败: %v", err)
	}
}

// loadWakeState 在循环启动时装载持久化的唤醒状态，让每日上限与冷却在重启后继续生效。
func (r *Runtime) loadWakeState(ctx context.Context) {
	if raw, ok, err := r.settings.GetSetting(ctx, settingWakeDay); err == nil && ok {
		r.wakeDay = strings.TrimSpace(raw)
	}
	if raw, ok, err := r.settings.GetSetting(ctx, settingWakeCount); err == nil && ok {
		if n, convErr := strconv.Atoi(strings.TrimSpace(raw)); convErr == nil {
			r.wakeCount = n
		}
	}
	if raw, ok, err := r.settings.GetSetting(ctx, settingLastWakeAt); err == nil && ok {
		if at, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(raw)); parseErr == nil {
			r.lastWake = at
		}
	}
}
