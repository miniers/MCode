package swarm

import (
	"context"
	"errors"
	"time"

	"mcode/internal/git"
	"mcode/internal/kanban"
	"mcode/internal/reviewcheck"
	"mcode/internal/store"
)

// BeeStatus 表示编排子代理的状态。
type BeeStatus string

const (
	StatusRunning   BeeStatus = "running"
	StatusCompleted BeeStatus = "completed"
	StatusFailed    BeeStatus = "failed"
	StatusStopped   BeeStatus = "stopped"
)

// 自动合并进度（Bee.AutoMergeState）的取值。
const (
	// AutoMergeNone 表示尚未交给自动合并流水线。
	AutoMergeNone = ""
	// AutoMergePending 表示已交给流水线但尚未完成，含等待重试的批次。
	AutoMergePending = "pending"
	// AutoMergeDone 表示流水线已处理完成（本地提交完成或远端阶段已完成）。
	AutoMergeDone = "done"
	// AutoMergeSkipped 表示自动合并关闭，未做处理。
	AutoMergeSkipped = "skipped"
	// AutoMergeFailed 表示合并失败。
	AutoMergeFailed = "failed"
)

// ReviewState 表示合并前审查的进度；空串表示未进入审查。
type ReviewState string

const (
	// ReviewNone 表示没有审查任务（审查模式为 off 或尚未开始）。
	ReviewNone ReviewState = ""
	// ReviewRunning 表示审查会话正在运行。
	ReviewRunning ReviewState = "running"
	// ReviewApproved 表示审查已通过，可以合入。
	ReviewApproved ReviewState = "approved"
	// ReviewRejected 表示审查未通过，改动不合入。
	ReviewRejected ReviewState = "rejected"
	// ReviewNeedsHuman 表示需要人工裁决。
	ReviewNeedsHuman ReviewState = "needs_human"
)

// ReviewStage 表示合并审查漏斗内部所处的阶段：l0 确定性检查、l2_queued 等待攒批、
// l2 模型审查、l3 升级到只读审查会话；空串表示不在漏斗中。
type ReviewStage string

const (
	ReviewStageL0       ReviewStage = "l0"
	ReviewStageL2Queued ReviewStage = "l2_queued"
	ReviewStageL2       ReviewStage = "l2"
	ReviewStageL3       ReviewStage = "l3"
)

// Bee 表示单个编排子代理会话的完整元信息与运行时状态。
type Bee struct {
	ThreadID string `json:"threadId"`
	Index    int    `json:"index"`
	Name     string `json:"name"`
	Task     string `json:"task"`
	// Prompt 保存派发时写给该 bee 的完整 brief，失败重试时按它重建会话，避免重试丢 brief。
	Prompt string `json:"prompt,omitempty"`
	// Interrupted 标记该 bee 因 MCode 重启被打断、等待用户决定续跑还是放弃；续跑或放弃后清除。
	Interrupted    bool       `json:"interrupted,omitempty"`
	Status         BeeStatus  `json:"status"`
	Summary        string     `json:"summary"`
	Activity       string     `json:"activity"`
	ToolCalls      int        `json:"toolCalls"`
	Tokens         int        `json:"tokens"`
	StartedAt      time.Time  `json:"startedAt"`
	FinishedAt     *time.Time `json:"finishedAt"`
	LastProgressAt time.Time  `json:"lastProgressAt,omitempty"`
	// LastFileChangeAt 是最近一次工具写文件或轮次开始的时刻，供进度看门狗判断是否有落地。
	LastFileChangeAt time.Time `json:"lastFileChangeAt,omitempty"`
	// WaitedFor 累计 bee 等待用户回答或审批的时长，运行上限扣除这段时间；waitTickAt 是最近一次看到等待的轮询时刻。
	WaitedFor  time.Duration `json:"waitedFor,omitempty"`
	waitTickAt time.Time
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	// Effort 是 bee 的推理强度档位，Profile 是派发时命中的 Bee Profile 名字；均来自配对/Profile 解析。
	Effort   string `json:"effort,omitempty"`
	Profile  string `json:"profile,omitempty"`
	Tier     string `json:"tier,omitempty"`
	ReadOnly bool   `json:"read_only,omitempty"`
	// WritePaths 是派发时档位带来的写入白名单，非空时只合并匹配这些 glob 的改动。
	WritePaths []string `json:"write_paths,omitempty"`
	// RuntimeMode 是派发时由宿主指定的权限模式，非空时子代理不继承主线程的权限模式；
	// 失败重试时原样继承。
	RuntimeMode    string   `json:"runtime_mode,omitempty"`
	DiscardedPaths []string `json:"discarded_paths,omitempty"`
	DirectModel    bool     `json:"direct_model,omitempty"`
	// FastMode 记录派发时是否开启快速模式，失败重试时原样继承。
	FastMode          bool `json:"fast_mode,omitempty"`
	ReadOnlyViolation bool `json:"read_only_violation,omitempty"`
	// ShadowRepo 表示项目不是 git 仓库、bee 用 mcode 影子仓库隔离。
	ShadowRepo bool `json:"shadow_repo,omitempty"`
	// SharedNoGit 表示项目不是 git 仓库且影子仓库不可用，bee 直接在主工作区工作。
	SharedNoGit bool `json:"shared_no_git,omitempty"`
	// SharedNoGitReason 记录项目不是 git 仓库且影子仓库不可用时的原因说明。
	SharedNoGitReason string `json:"shared_no_git_reason,omitempty"`
	// LandedPatchPath 记录本 bee 落地补丁的存档路径，供后续自动合并使用。
	LandedPatchPath string `json:"landed_patch_path,omitempty"`
	// LandedCommit 是本 bee 落地时已创建的提交，批次结束后无需再重放补丁。
	LandedCommit string `json:"landed_commit,omitempty"`
	// LandMessage 是落地提交信息，生成一次后在重试中复用。
	LandMessage string `json:"land_message,omitempty"`
	// ArchivedPatchPath 是副本回收前把未落地改动存档到 swarm-patches 的补丁路径。
	ArchivedPatchPath string   `json:"archivedPatchPath,omitempty"`
	WorktreePath      string   `json:"worktreePath"`
	Branch            string   `json:"branch"`
	BaseCommit        string   `json:"baseCommit"`
	Merged            bool     `json:"merged"`
	ConflictFiles     []string `json:"conflictFiles"`
	// ConflictHandbacks 因与主工作区冲突被交回重做的次数。
	ConflictHandbacks int      `json:"conflict_handbacks,omitempty"`
	ChangedFiles      []string `json:"changedFiles"`
	// LinesAdded/LinesDeleted 是该 bee 相对 base 的增删行数，供汇报统计行显示改动规模。
	LinesAdded     int    `json:"linesAdded"`
	LinesDeleted   int    `json:"linesDeleted"`
	MergeError     string `json:"mergeError"`
	PendingMerge   bool   `json:"pendingMerge"`
	MergeDiscarded bool   `json:"mergeDiscarded"`
	// MergePatch 保存审查与批准共用的补丁快照，避免合入审查后新增的改动。
	MergePatch    []byte `json:"mergePatch,omitempty"`
	ProjectID     string `json:"projectId"`
	TargetWorkDir string `json:"targetWorkDir"`
	Reported      bool   `json:"reported"`
	// 合并前审查的进度与产物：ReviewState 为空表示未进入审查，其余字段记录审查会话与补丁。
	ReviewState       ReviewState `json:"reviewState,omitempty"`
	ReviewReason      string      `json:"reviewReason,omitempty"`
	ReviewThreadID    string      `json:"reviewThreadId,omitempty"`
	ReviewPatchPath   string      `json:"reviewPatchPath,omitempty"`
	ReviewPatchSHA    string      `json:"reviewPatchSha,omitempty"`
	ReviewStartedAt   *time.Time  `json:"reviewStartedAt,omitempty"`
	ReviewFingerprint string      `json:"reviewFingerprint,omitempty"`
	ReviewProvider    string      `json:"reviewProvider,omitempty"`
	ReviewModel       string      `json:"reviewModel,omitempty"`
	ReviewEffort      string      `json:"reviewEffort,omitempty"`
	// 审查漏斗的调度状态与结论：ReviewStage 只在内部使用，不进入远程协议。
	ReviewStage           ReviewStage `json:"reviewStage,omitempty"`
	ReviewLayer           string      `json:"reviewLayer,omitempty"` // l0 | l1 | l2 | l3 | human
	ReviewConcerns        []string    `json:"reviewConcerns,omitempty"`
	ReviewTokens          int         `json:"reviewTokens,omitempty"`
	ReviewTokensEstimated bool        `json:"reviewTokensEstimated,omitempty"`
	ReviewQueuedAt        *time.Time  `json:"reviewQueuedAt,omitempty"`
	ReviewAttempts        int         `json:"reviewAttempts,omitempty"`
	ReviewApplyPending    bool        `json:"reviewApplyPending,omitempty"`
	ReworkOf              string      `json:"reworkOf,omitempty"`
	ReworkPatchPath       string      `json:"reworkPatchPath,omitempty"`
	ReworkReason          string      `json:"reworkReason,omitempty"`
	// Archived/ArchiveHandled 记录 Codex 会话归档：ArchiveHandled 置位后不再重复归档，Archived 表示归档是否成功。
	Archived       bool `json:"archived,omitempty"`
	ArchiveHandled bool `json:"archiveHandled,omitempty"`
	// ThinkingMinutes 记录 bee 无进展的分钟数，重新出现进展后归零。
	ThinkingMinutes int `json:"thinkingMinutes,omitempty"`
	// MergeWaitSince 记录因主工作区未落定而开始等待落地的时间；nil 表示没有在等待。
	MergeWaitSince *time.Time `json:"mergeWaitSince,omitempty"`
	// Delivered 表示该 bee 的合入成果已通过批次落地回调上报，避免重复回调。
	Delivered bool `json:"delivered,omitempty"`
	// AutoMergeState 记录自动合并进度：空表示尚未交给流水线，pending 表示已交给流水线但未完成，
	// done/skipped/failed 为终态。
	AutoMergeState string `json:"autoMergeState,omitempty"`
	// AutoMergeSHA 是流水线在本地创建的提交 sha；空表示没有可提交的改动。
	AutoMergeSHA string `json:"autoMergeSha,omitempty"`
	// AutoMergeStage 是远端阶段失败或待重试时所处的阶段（MergeStage 的字符串形式），供重启后续做。
	AutoMergeStage string `json:"autoMergeStage,omitempty"`
	// AutoMergeError 是最近一次合并失败的说明。
	AutoMergeError string `json:"autoMergeError,omitempty"`
	// Credibility 记录汇报中代码引用的可信度核验统计；无引用时为 nil。
	Credibility *Credibility `json:"credibility,omitempty"`
}

// State 描述特定主会话（Lead）下的编排状态快照。
type State struct {
	LeadThreadID string `json:"leadThreadId"`
	Enabled      bool   `json:"enabled"`
	PlansOnly    *bool  `json:"plansOnly,omitempty"`
	Bees         []Bee  `json:"heads"`
	// Queued 是该 lead 在派发队列中等待启动的条目数；这些条目尚未出现在 Bees 中。
	Queued int `json:"queued,omitempty"`
	// Interrupted 是因 MCode 重启被打断、等待用户决定续跑或放弃的 bee 与排队条目总数。
	Interrupted int `json:"interrupted,omitempty"`
}

// BeeSpec 描述派发子会话的规格入参。
type BeeSpec struct {
	index *int
	// reuseWorktree/reuseBranch/reuseBaseCommit 用于失败重试时复用上一个子代理保留的 worktree：
	// 新 bee 直接在它的半成品上继续，不再新建工作副本；为空时照常新建。
	reuseWorktree   string
	reuseBranch     string
	reuseBaseCommit string
	Name            string `json:"name"`
	Task            string `json:"task"`
	Prompt          string `json:"prompt"`
	ProjectID       string `json:"projectId,omitempty"`
	WorkDir         string `json:"workDir,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	Effort          string `json:"effort,omitempty"`
	FastMode        bool   `json:"fastMode,omitempty"`
	Profile         string `json:"profile,omitempty"`
	Tier            string `json:"tier,omitempty"`
	ReadOnly        bool   `json:"read_only,omitempty"`
	// WritePaths 是档位带来的写入白名单，非空时只合并匹配这些 glob 的改动；失败重试时原样继承。
	WritePaths []string `json:"write_paths,omitempty"`
	// RuntimeMode 是派发时由宿主指定的权限模式，非空时子代理不继承主线程的权限模式；
	// 失败重试时原样继承。
	RuntimeMode string `json:"runtime_mode,omitempty"`
	DirectModel bool   `json:"direct_model,omitempty"`
	// ReworkOf/ReworkPatchPath/ReworkReason 记录返工对象；planEntry 解析后随 spec
	// 一次传给 startSingleBee，避免创建 Bee 时再次查 lead。
	ReworkOf        string `json:"reworkOf,omitempty"`
	ReworkPatchPath string `json:"reworkPatchPath,omitempty"`
	ReworkReason    string `json:"reworkReason,omitempty"`
}

// QueuedBeeRecord 描述重启前还在派发队列里、尚未启动的一条排队记录。
// 它随 LeadRecord 一起落盘，重启后恢复成 waiting 状态等用户决定是否续跑，不自动派发。
type QueuedBeeRecord struct {
	ProjectID   string  `json:"projectId,omitempty"`
	LeadWorkDir string  `json:"leadWorkDir,omitempty"`
	Index       int     `json:"index"`
	Spec        BeeSpec `json:"spec"`
}

// Settings 定义编排运行时的动态配置。
type Settings struct {
	Enabled         bool
	AutoDelegate    bool
	MaxBees         int
	WorkspaceMode   string // "worktree" | "shared"
	RetryFailed     bool
	AutoMerge       bool
	AutoPush        bool
	DefaultProvider string
	DefaultModel    string
	// DefaultEffort 是 bee 的默认推理强度，派发指令与配对/档位都没指定时使用，空表示跟随模型默认。
	DefaultEffort string
	// MaxRounds 限制主会话可派发 bee 的轮次上限，0 表示不限制。
	MaxRounds int
	// Pairs 是「Lead + Bee」模型配对列表，供派发时按配对解析 bee 的模型与强度。
	Pairs []Pair
	// Tiers 是可用档位列表，供派发时按任务难度解析模型。
	Tiers []Tier
	// DefaultTier 是默认档位标识（ID 或 Name）。
	DefaultTier string
	// BeeTimeoutMinutes 定义 Bee 运行超时（分钟），0 表示使用默认值 40。
	BeeTimeoutMinutes int
	// RequestTimeoutMinutes 是单个用户请求的总时长预算（分钟），0 表示使用 Options.RequestTimeout 或默认 3 小时。
	RequestTimeoutMinutes int
	// AutoClearFinished 开启后，已结束且汇报完的 bee 会被自动清理出状态。
	AutoClearFinished bool
	// AutoHidesIdleBees 开启后，空闲 lead 的 bee 卡片在侧栏隐藏（侧栏过滤另行实现）。
	AutoHidesIdleBees bool
	// ArchivesCodexBees 开启后，Codex 驱动的 bee 会话在汇报送达后归档。
	ArchivesCodexBees bool
	// DelegateTool 开启后，主会话除 ```swarm 块外还能调用 swarm_delegate 工具派发子代理。
	DelegateTool bool
	// ReviewMode 是合并前审查模式：off | ai | human，空串按 off 处理。
	ReviewMode string
	// ReviewProvider/ReviewModel/ReviewEffort 是审查会话的模型三件套，空串表示跟随主会话。
	ReviewProvider string
	ReviewModel    string
	ReviewEffort   string
	// ReviewCheckCommand 是项目检查命令，空表示自动识别；ReviewMaxChangedLines 是
	// L1 自动通过的行数上限，0 表示关闭行数规则。
	ReviewCheckCommand    string
	ReviewMaxChangedLines int
}

// RequestLimits 描述单个用户请求下已用与剩余可用的编排上限，供向 Lead 提示可用预算。
type RequestLimits struct {
	// MaxRounds 是本次请求的派发轮次上限，0 表示不限制；RoundsUsed 是已用轮次。
	MaxRounds  int
	RoundsUsed int
	// MaxConcurrent 是同一时刻可运行的 bee 数量上限。
	MaxConcurrent int
	// BeeBudget 是本次请求可启动的 bee 总数上限，BeesStarted 是已启动的数量。
	BeeBudget   int
	BeesStarted int
	// Timeout 是本次请求的总时长预算，Elapsed 是从用户最新消息起算的已用时长。
	Timeout time.Duration
	Elapsed time.Duration
}

const (
	// MaxDelegationRounds 限制单个用户请求下的委派轮次上限。
	MaxDelegationRounds = 3
	// DefaultBeeTimeout 是 bee 运行上限（扣除等待时长）的默认值。
	DefaultBeeTimeout = 40 * time.Minute
	// maxNoEditStallTimeout 是无输出且无改动判卡死的最长等待，不随运行上限放宽。
	maxNoEditStallTimeout = 15 * time.Minute
	// DefaultBeeBudget 限制单个用户请求下已启动 bee 的数量上限。
	DefaultBeeBudget = 24
	// DefaultSettleTimeout 是 Lead 正在编辑或 merge/rebase 进行中时等待主工作区落定的默认上限。
	DefaultSettleTimeout = 30 * time.Minute
)

// LandedBatch 描述一批已合入且完成回调的 bee，按项目与目标工作区分组。
type LandedBatch struct {
	LeadThreadID  string
	ProjectID     string
	TargetWorkDir string
	Bees          []Bee
}

// DispatchRequest 描述批量派发编排子代理会话的请求。
type DispatchRequest struct {
	LeadThreadID string    `json:"leadThreadId"`
	ProjectID    string    `json:"projectId"`
	LeadWorkDir  string    `json:"leadWorkDir"`
	Bees         []BeeSpec `json:"heads"`
}

var (
	// ErrLeadNotFound 表示未找到对应的主会话记录。
	ErrLeadNotFound = errors.New("swarm: lead not found")
	// ErrBeeNotFound 表示未找到指定的子会话记录。
	ErrBeeNotFound = errors.New("swarm: bee not found")
	// ErrNoPendingMerge 表示该子代理没有待审查的合并。
	ErrNoPendingMerge = errors.New("该子代理没有待审查的合并")
	// ErrBadDelegationBlock 表示委派块格式非法或未能解析为合法的 JSON 数据。
	ErrBadDelegationBlock = errors.New("swarm: delegation block is not valid JSON")
	// ErrDelegationBudget 表示本次请求的委派轮次已达上限。
	ErrDelegationBudget = errors.New("swarm: 已达本次请求的委派轮次上限")
	// ErrBeeBudget 表示本次请求已启动的子代理数量已达上限。
	ErrBeeBudget = errors.New("swarm: 已达本次请求的子代理数量上限")
	// ErrMaxRounds 表示该主会话已达配置的派发轮次上限（swarm.max_rounds）。
	ErrMaxRounds = errors.New("swarm: 已达派发轮次上限")
	// ErrRequestTimeout 表示本次用户请求已用完总时长预算，不再派发新的子代理。
	ErrRequestTimeout = errors.New("swarm: 已超过本次请求的总时长预算")
	// ErrSwarmDisabled 表示编排未开启，宿主指定覆盖项的派发无法执行。
	ErrSwarmDisabled = errors.New("编排未开启，无法派发子代理")
)

// LeadRecord 表示单个主会话的完整持久化记录。
type LeadRecord struct {
	LeadThreadID string
	Enabled      bool
	PlansOnly    *bool
	ProjectID    string
	LeadWorkDir  string
	NextIndex    int
	// DelegationRounds 记录本次用户请求已使用的委派轮次，重启后继续累计。
	DelegationRounds int
	// RequestStartedAt 记录本次用户请求的起始时间，用于 bee 预算与总时长看门狗。
	RequestStartedAt time.Time
	Bees             []Bee
	// Queue 保存重启前仍在排队、尚未启动的条目，重启后不再自动派发，交回用户决定。
	Queue []QueuedBeeRecord
}

// Persister 定义主会话状态的持久化存储接口。
type Persister interface {
	SaveLead(ctx context.Context, rec LeadRecord) error
	LoadLeads(ctx context.Context) ([]LeadRecord, error)
}

// SessionArchiver 由 Driver 可选实现，仅归档 Codex 驱动、没有用户追问的 Bee 会话，
// 保留会话历史供后续查看；它不触碰 worktree，也不做任何推送。
type SessionArchiver interface {
	ArchiveBeeSession(ctx context.Context, threadID string) (archived bool, err error)
}

// ThreadRelationSetter 由 Driver 或 Persister 可选实现，用于建立子会话与主会话的父子关联。
type ThreadRelationSetter interface {
	SetThreadRelation(threadID, parentThreadID string, isHelper bool)
}

// ThreadRelationContextSetter 由 Persister 或 Driver 可选实现，支持带 context 的父子关联更新。
type ThreadRelationContextSetter interface {
	SetThreadRelation(ctx context.Context, threadID, parentThreadID string, isHelper bool) error
}

// LeadRuntimeSource 由 Driver 可选实现：审查会话可以按主会话当前的模型起会话。
type LeadRuntimeSource interface {
	LeadRuntime(ctx context.Context, leadThreadID string) (provider, model, effort string, ok bool)
}

// ThreadContext 包含主会话的上下文信息，供派发解析档位与模型使用。
type ThreadContext struct {
	Provider string
	Model    string
	// UserMessage 为主会话最近一条真实用户输入，非 [编排] 汇报消息。
	UserMessage string
	// PairID 为主会话当前选中的配对 ID。
	PairID string
	// BeeTiers 为该会话针对各档位的自定义覆盖映射。
	BeeTiers map[string]TierOverride
	// BeeProvider 是会话里选的子线程默认服务商。
	BeeProvider string
	// BeeModel 是会话里选的子线程默认模型。
	BeeModel string
	// BeeEffort 是会话里选的子线程默认推理强度。
	BeeEffort string
	// ProviderEnabled 检查指定服务商是否已启用。
	ProviderEnabled func(string) bool
}

// Options 包含编排管理器运行所需的依赖与配置项。
type Options struct {
	NormalizeBee     func(provider, model string) (string, string)
	DefaultBeeSource func(lead ThreadContext) (provider, model string, err error)
	Git              *git.Runner
	// Describe 为可选的提交说明生成器，入参为限定路径的 git diff 文本。
	Describe func(ctx context.Context, diff string) (string, error)
	// DescribeTimeout 限制单次提交说明生成的耗时；零值时默认 30 秒。
	DescribeTimeout time.Duration
	Driver          kanban.SessionDriver
	StopSession     func(ctx context.Context, threadID string) error
	WorktreeDir     string
	DataDir         string
	// WorktreeLocation 返回新副本的存储位置；nil 时使用集中目录。
	WorktreeLocation func() string
	// ProfilesPath 是子代理 Profile 定义文件路径；为空时默认取
	// <DataDir>/hydra-profiles.json
	// （该文件名是旧版遗留命名，仅用于导入旧数据）。
	ProfilesPath string
	PollInterval time.Duration
	// StallTimeout 定义子会话无进展的看门狗超时时间；零值时默认取 10 分钟。
	StallTimeout time.Duration
	Persister    Persister
	OnReport     func(leadThreadID, message string) error
	// ArchiveReport 在投递汇报前存档每个已结束 bee 的完整汇报总结；nil 表示不存档。
	ArchiveReport func(ctx context.Context, rec store.SwarmReport) error
	// OnBeesIdle 在某个主会话下的子代理全部结束（没有运行中、没有排队待派、没有正在派发）时，
	// 每个 tick 回调一次，供后端把此前排队的汇报推进成一轮回复。回调需幂等：重复调用由接收方
	// 自行判断是否需要开轮。
	OnBeesIdle     func(ctx context.Context, leadThreadID string)
	ResolveProject func(ctx context.Context, ref string) (projectID, workDir string, ok bool)
	// SkillsGuidance 按名字解析 bee 要带的技能提示块（names 来自条目声明的 skills）；
	// workDir 是该 bee 的工作副本。nil 表示不注入技能块。
	SkillsGuidance func(workDir string, names []string) string
	// MemoryRecall 在派发 bee 前检索相关长期记忆，返回现成的 Markdown 段落；
	// workDir 是该 bee 的目标工作目录，query 由任务与提示词前缀组成。
	// nil 或返回空串时不附加任何内容。
	MemoryRecall func(ctx context.Context, workDir, query string) string
	// Now 返回当前时间，便于测试注入时钟；nil 时使用 time.Now。
	Now func() time.Time
	// ThinkingAfter 定义 bee 无输出多久后在状态中标记为思考中；零值时默认取 2 分钟。
	ThinkingAfter time.Duration
	// NoEditStallTimeout 定义 bee 无输出且工作副本无文件改动时的卡死看门狗超时；零值时默认取 15 分钟。
	NoEditStallTimeout time.Duration
	// LeadTouchedPaths 返回 Lead 当前轮次正在编辑的文件与是否处于活跃编辑状态。
	LeadTouchedPaths func(leadThreadID string) (paths []string, active bool)
	// ThreadContext 获取指定主会话的线程上下文；nil 表示不提供线程上下文。
	ThreadContext func(leadThreadID string) ThreadContext
	// SettleTimeout 定义等待主工作区落定的最长时间；零值时默认取 30 分钟。
	SettleTimeout time.Duration
	// RequestTimeout 定义单个用户请求的总时长预算；零值时默认取 3 小时。
	RequestTimeout time.Duration
	// OnBatchLanded 在一批 bee 全部结束且落地完成后，按项目与目标工作区各回调一次。
	OnBatchLanded func(ctx context.Context, b LandedBatch)
	// ReadPaths 返回指定 bee 会话记录的读取文件路径；nil 表示读取记录不可用。
	ReadPaths func(beeThreadID string) []string
	// 审查漏斗的可注入依赖：nil 时使用真实实现（reviewcheck/git/textgen）。
	ReviewRunCheck      func(ctx context.Context, worktree string, changed []string, configured string, timeout time.Duration) reviewcheck.Result
	ReviewGenerate      func(ctx context.Context, provider, model, effort, prompt string) (string, error)
	ReviewDiff          func(ctx context.Context, worktree, base string, paths []string, maxBytes int) (string, bool, error)
	ReviewDiffBetween   func(ctx context.Context, oldRoot, newRoot string, paths []string, maxBytes int) (string, error)
	ReviewProjectConfig func(dataDir, projectDir string) (reviewcheck.ProjectConfig, error)
}

// clone 返回 Bee 结构的深拷贝副本。
func (h Bee) clone() Bee {
	cp := h
	cp.MergePatch = append([]byte(nil), h.MergePatch...)
	if h.FinishedAt != nil {
		t := *h.FinishedAt
		cp.FinishedAt = &t
	}
	if h.MergeWaitSince != nil {
		t := *h.MergeWaitSince
		cp.MergeWaitSince = &t
	}
	if h.ReviewStartedAt != nil {
		t := *h.ReviewStartedAt
		cp.ReviewStartedAt = &t
	}
	if h.ReviewQueuedAt != nil {
		t := *h.ReviewQueuedAt
		cp.ReviewQueuedAt = &t
	}
	if h.ReviewConcerns != nil {
		cp.ReviewConcerns = append([]string(nil), h.ReviewConcerns...)
	}
	if h.Credibility != nil {
		c := *h.Credibility
		if h.Credibility.Unverified != nil {
			c.Unverified = append([]string(nil), h.Credibility.Unverified...)
		}
		cp.Credibility = &c
	}
	if h.ConflictFiles != nil {
		cp.ConflictFiles = append([]string(nil), h.ConflictFiles...)
	} else {
		cp.ConflictFiles = []string{}
	}
	if h.ChangedFiles != nil {
		cp.ChangedFiles = append([]string(nil), h.ChangedFiles...)
	} else {
		cp.ChangedFiles = []string{}
	}
	return cp
}
