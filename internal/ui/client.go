package mcodeui

import (
	"context"
	"errors"
	"time"

	"mcode/internal/agentconf"
	"mcode/internal/config"
	"mcode/internal/providers/agy/accounts"
	"mcode/internal/providers/multiaccount"
)

// Project 表示项目元数据。
type Project struct {
	ID, Name, Path string
}

// ContextDoc 表示项目文档中的一篇文档。Name 为引用格式：标准文档（instructions.md、overview.md、
// decisions.md、notes.md）位于 .mcode/context/ 根部，Name 为文件名；自定义文档位于 .mcode/context/docs/，
// Name 为 docs/<文件名>（如 docs/api-spec.md）。
type ContextDoc struct {
	Name, Content, Hash string
	ModTime             time.Time
}

// ErrContextConflict 表示保存时 CAS 哈希不一致。
var ErrContextConflict = errors.New("mcodeui: context doc conflict")

// MemoryProposal 是一条记忆提议：先落在本地收件箱，用户确认后才写入 Nowledge Mem。
// 字段与 store.MemoryProposal 一致；Source 取 agent / distill / user，Status 取 pending / accepted / rejected。
type MemoryProposal struct {
	ID             string
	ProjectPath    string
	ThreadID       string
	Title          string
	Content        string
	UnitType       string
	Importance     float64
	Labels         []string
	Source         string
	Status         string
	RemoteMemoryID string
	Error          string
	// Scope 为写入范围：project 写项目空间，global 写全局空间。
	Scope string
	// TargetMemoryID 非空表示改写该已有记忆，而不是新增。
	TargetMemoryID string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// MemoryItem 是 Nowledge Mem 里的一条记忆（语义检索的返回项）。
type MemoryItem struct {
	ID         string
	Title      string
	Content    string
	UnitType   string
	Importance float64
	Labels     []string
	CreatedAt  time.Time
}

// MemorySpace 是 Nowledge Mem 里的一个记忆空间（设置页下拉框的选项）。
type MemorySpace struct {
	ID   string
	Name string
}

// MemorySpaceResolution 是某个项目路径解析出的记忆空间结果，供设置页展示。
type MemorySpaceResolution struct {
	// Space 是候选或命中的空间名。
	Space string
	// FromOverride 表示这次解析用的是配置里手动指定的空间。
	FromOverride bool
	// Hit 表示在已有空间列表里找到了匹配的空间。
	Hit bool
}

// ModelCost 表示模型的 API 参考价，单位为美元/百万 token。
type ModelCost struct {
	Input, Output, CacheRead, CacheWrite float64
}

// ModelInfo 表示模型信息。
type ModelInfo struct {
	ID            string
	Name          string
	Detail        string     // 模型说明，来自 CLI 或 models.dev 摘要，空表示没有
	Cost          *ModelCost // API 参考价，nil 表示未知
	IsDefault     bool
	Efforts       []string // 推理强度档位列表，空表示单档
	DefaultEffort string   // 默认推理强度档位
	SupportsFast  bool     // 是否支持快速模式（Fast Mode）
	ContextLimit  int64    // 上下文窗口上限（tokens），0 表示未知
	Secondary     bool     // 次要模型，默认折叠在分组底部的更多模型里
}

// ProviderInfo 表示 Agent 提供方信息。
type ProviderInfo struct {
	ID, Name  string
	Available bool
	// UnavailableReason 说明供应商可用但模型目录为空的原因（例如 CLI 未安装）；空表示没有说明。
	UnavailableReason string
	Models            []ModelInfo
}

// Thread 表示会话元数据。
type Thread struct {
	ID, ProjectID, Title         string
	ParentThreadID               string // 所属主会话 ID（若是 Swarm bee 等 helper 会话）
	IsHelper                     bool   // 是否为 helper / bee 会话
	Provider                     string
	Model                        string
	RuntimeMode                  string
	InteractionMode              string // "build" 或 "plan"，空视为 build
	Effort                       string // Lead 推理强度：low/medium/high，空为默认
	BeesProvider                 string // Swarm bee 使用的供应商，空为跟随 Lead
	BeesModel                    string // Swarm bee 使用的模型，空为默认
	BeesEffort                   string // Swarm bee 推理强度，空为默认
	Persona                      string // 线程人设 id：空表示从未设置，"none" 表示用户选了无人设
	BeeTiers                     string // 档位模型覆盖 JSON（map[string]SwarmTierOverride，空表示无覆盖）
	PairID                       string // 会话选中的 Swarm 配对 ID，空表示未显式选择（按模型推断）
	SwarmMode                    string // 会话级 Swarm 工作模式：inherited/collaborative/leadPlansOnly/alwaysBees
	FastMode                     bool   // 会话快速模式
	SpeedTier                    string // 快速模式速率档位 ID，空为供应商默认
	Pinned                       bool
	Dormant                      bool
	HasUnread                    bool
	IsRunning, HasPendingRequest bool
	// SwarmBusy 表示 Swarm 主会话名下仍有未结束的子线程（运行、排队或待投递回报）。
	// 后端约定只有 IsRunning 与 SwarmBusy 都为假，才算这一轮真正结束。
	SwarmBusy bool
	// BackgroundTasks 是回合结束后仍需等待的后台任务数（provider 托管的后台任务，
	// 以及会话空闲时仍 running 的命令）；子智能体不计入，由 SwarmBusy 表达。
	BackgroundTasks int
	UpdatedAt       time.Time

	// LastUserMessageAt 是用户最后一次发消息的时间：侧边栏排序以它为准，助手回复、
	// 改名等只改 UpdatedAt 的操作不应把会话顶到前面。零值表示未知。
	LastUserMessageAt time.Time

	// LastStatus 是会话最近一轮的结束状态（completed/failed/interrupted 等），空表示未知。
	LastStatus string

	// ForkedFromThreadID / ForkedFromMessageID 记录会话由哪个会话的哪条消息分叉而来；
	// 两个都为空表示不是分叉会话。源会话已删除时分叉会话仍保留这两个 ID。
	ForkedFromThreadID  string
	ForkedFromMessageID string

	// MCPDisabled 是会话级外部 MCP 禁用名单（元素为 agent:scope:name 键）；空表示沿用全局设置。
	MCPDisabled []string
}

// ComposerPrefs 表示 composer 保存在会话上的偏好：Build/Plan 模式、Lead 推理强度与 Bees 模型配置。
// 写入时整体覆盖，调用方需带上未修改字段的原值。
type ComposerPrefs struct {
	InteractionMode, Effort, BeesProvider, BeesModel, BeesEffort string
	// Persona 是线程人设 id：空串表示从未设置，具体 id 为选中的人设，字面量 "none" 表示无人设。
	Persona string
}

// QueuedFollowUp 表示会话运行中排队等待发送的一条跟进消息。
type QueuedFollowUp struct {
	ID, Text, CreatedAt string
	AttachmentCount     int
	// System 表示这是 mcode 系统条目（priority 跟进），只读展示，不提供编辑与删除。
	System bool
	// Intent 是排队项的原始意图：insert 表示用户本意插入当前回合、被降级成排队；
	// 空串或 queue 表示普通排队。
	Intent string
}

// ForwardedContext 是转发会话实际带入新会话的历史上下文，供顶部卡片展示全文。
// Content 是标记之间的原文（不含导言），Truncated 表示导言里带过截断提示。
type ForwardedContext struct {
	SourceTitle string
	Content     string
	Truncated   bool
}

// GitWorktree 描述一个 Git worktree。
type GitWorktree struct {
	Path, Branch string
	Current      bool
}

// GitState 描述会话工作目录的 Git 状态。
type GitState struct {
	WorkDir   string
	Branch    string // 当前分支，detached 时为空
	Detached  bool
	Head      string // 7 位短哈希
	Branches  []string
	Worktrees []GitWorktree
	Changes   int // 未提交变更数
	RemoteURL string
	// ChangedFiles 统计改动文件数（与 Changes 相同，Git 菜单标题使用）。
	ChangedFiles int
	Ahead        int  // 当前分支相对上游领先的提交数
	Behind       int  // 当前分支相对上游落后的提交数
	HasUpstream  bool // 当前分支是否配置了上游
	HasStash     bool // 仓库是否有贮藏记录
	// CanFastForward 当前分支是否落后于上游（拉取可快进）。
	CanFastForward bool
}

// GitChangedFile 描述提交窗口里一个可勾选的改动文件。
type GitChangedFile struct {
	Path     string
	OrigPath string // 重命名或复制时的来源路径
	Status   string // 单字符状态：M / A / D / R / ?
}

// MessageMedia 表示消息关联的媒体资源（桌面端展示用）。
type MessageMedia struct {
	ID, Name, MimeType string
	Width, Height      int
}

// Message 表示一条聊天消息。
type Message struct {
	ID, Role, Kind, Text string
	IsFinal              bool
	TurnIndex            int
	TurnID               string
	TurnStatus           string
	TurnStartedAt        string
	TurnCompletedAt      string
	Media                []MessageMedia
	// 消息创建时间（RFC3339，来自 wire ThreadMessage.createdAt，可能为空）。
	CreatedAt string
	// 工具调用结构化字段（来自 wire.ThreadMessage，仅在 role 为 tool 时出现，旧消息中可能为空）。
	ToolName        string // 工具名称（如 bash、edit_file 等）
	ToolInput       string // 完整输入 JSON 字符串（最长 16000 字符）
	ToolStatus      string // 执行状态（running、background、completed、failed、blocked）
	ToolCompletedAt string // 工具执行完成时间（RFC3339）
	// ErrKind 是错误消息的失败类别（来自 wire.ThreadMessage.errKind，取值见 errKind* 常量）；
	// 为空表示未分类，渲染时回退到关键词识别。
	ErrKind string
}

// ApprovalOption 表示审批选项。
type ApprovalOption struct {
	ID, Title, Role string
}

// 审批请求类型，与 internal/remote/wire 的 RemoteRequest.Kind 取值一致。
const (
	ApprovalKindCommand     = "command"
	ApprovalKindFileChange  = "fileChange"
	ApprovalKindTool        = "tool"
	ApprovalKindPermissions = "permissions"
	ApprovalKindPlan        = "plan"
	ApprovalKindCommit      = "commit"
	ApprovalKindQuestion    = "question"
)

// ApprovalQuestionChoice 表示提问的一个候选项。
type ApprovalQuestionChoice struct {
	Label, Detail string
}

// ApprovalQuestion 表示提问类请求中的一道题。
type ApprovalQuestion struct {
	ID, Header, Prompt                    string
	Choices                               []ApprovalQuestionChoice
	AllowsMultiple, AllowsOther, IsSecret bool
}

// Approval 表示审批请求。
type Approval struct {
	ThreadID, RunID, TurnID, RequestID, Title, Detail string
	Kind, Reason                                      string
	Options                                           []ApprovalOption
	Questions                                         []ApprovalQuestion
	Diff                                              string
	// DeliveryError 是上一次答复没有送达的原因，空表示无。非空时后端保留卡片，
	// 用户可以重新选择，卡片需要说明上次没送达。
	DeliveryError string
}

// ThreadUpdate 表示会话消息与状态增量更新。
type ThreadUpdate struct {
	ThreadID   string
	Thread     Thread
	Messages   []Message
	RemovedIDs []string
	// Resync 表示后端因帧体超过上限改发的重新同步帧：帧里没有消息，
	// 收到后必须按会话重拉全量消息，否则这一批消息就永久缺失。
	Resync bool
}

// RequestSpeed 是一次模型请求的输出 tokens 与生成时长（毫秒）。
// 只统计模型生成时间、不含工具执行，用于按请求口径计算输出速度。
type RequestSpeed struct {
	OutputTokens int64
	DurationMs   int64
}

// TokenUsage 记录令牌消耗量。
type TokenUsage struct {
	Input, Output, Cached int64
	DurationMs            int64 // 后端记录的本轮耗时（毫秒），用于计算输出速度；0 表示后端未提供
	// Requests 是本轮每次模型请求的速度样本，空表示后端未提供，回退到整轮耗时口径。
	Requests []RequestSpeed
	// ContextTokens 是该轮最后一次模型请求实际带入的上下文大小（输入 + 输出），
	// 只用于上下文窗口占用；0 表示后端未上报，回退到 Input + Output。
	ContextTokens int64
	// ContextWindow 是 CLI 上报的当前模型上下文窗口上限（tokens），比模型目录里的静态值准；
	// 0 表示后端未上报，回退到模型目录。与 ContextTokens 一样随用量落库，重启后可恢复。
	ContextWindow int64
	// CostUSD 是该轮花费（美元），0 表示后端未上报；用量面板展示最近一轮的费用。
	CostUSD float64
}

// ThreadUsage 记录会话的令牌消耗统计，包含每轮明细与全会话合计，
// 以及从已持久化消息推导出的活动统计（请求数、工具调用），后三者缺数据时为 0。
type ThreadUsage struct {
	Turns map[int]TokenUsage
	Total TokenUsage
	// Requests 是模型请求次数：按助手消息段数估算（一个回合内多次请求会产生多段回复）。
	Requests int
	// ToolCalls 是工具调用总次数（按 tool 消息统计）。
	ToolCalls int
	// TopTools 是按调用次数降序排列的常用工具，最多 5 项。
	TopTools []ToolCallCount
}

// ToolCallCount 记录某个工具的调用次数，用于会话统计里的常用工具排行。
type ToolCallCount struct {
	Name  string
	Count int
}

// DiffTurn 表示轮次改动状态。
type DiffTurn struct {
	TurnID     string
	Index      int
	Status     string
	HasChanges bool
}

// ThreadDiff 表示会话或指定轮次的代码改动对比。
type ThreadDiff struct {
	Diff          string
	Truncated     bool
	FilesChanged  []string
	Turns         []DiffTurn
	FailureReason string
}

// ErrRevisionConflict 表示数据已被其他地方修改，调用方应提示用户刷新。
var ErrRevisionConflict = errors.New("revision conflict")

// ErrAutomationSchedulerPassive 表示调度锁由其他实例持有或当前不可用。
var ErrAutomationSchedulerPassive = errors.New("automation: scheduler is passive")

// KanbanTask 表示看板任务卡片。
type KanbanTask struct {
	ID, ProjectID, ThreadID, GoalID, Title, Notes, Status, Priority, Column string
	DependsOn                                                               []string
	Blocked, CanStart, CanMove, CanReview                                   bool
	AttentionFlags                                                          []string
	LastFailureReason, LastActivity, ReviewSummary                          string
	ResultSummary, WaitKind, WakeAt                                         string
	AttemptCount, Revision                                                  int
	UpdatedAt                                                               time.Time
}

// KanbanGoal 表示看板目标。
type KanbanGoal struct {
	ID, ProjectID, Title, Prompt, Status, StatusReason, PlanSummary, ResultSummary string
	Done, Total, Revision                                                          int
	UpdatedAt                                                                      time.Time
}

// KanbanBoard 表示完整看板快照。
type KanbanBoard struct {
	Revision int
	Tasks    []KanbanTask
	Goals    []KanbanGoal
}

// AutomationSchedule 表示自动化执行计划调度。
type AutomationSchedule struct {
	Type                string
	Seconds             int
	Date                time.Time
	TimeOfDay, Timezone string
	DayOfWeek           int
	Expression          string
}

// Automation 表示自动化任务配置与状态。
type Automation struct {
	ID, ProjectID, Name, Prompt, Status, StopWhen, LastRunStatus, StopReason                     string
	Schedule                                                                                     AutomationSchedule
	Enabled                                                                                      bool
	NextRunAt, LastRunAt                                                                         time.Time
	TotalRunCount, ConsecutiveFailures, MaxConsecutiveFailures, MaxIterations, MaxRuns, Revision int
	// CatchUp 表示错过调度触发时是否补跑一次。
	CatchUp bool
	// Provider/Model/Effort 是每次运行新建会话使用的模型与推理强度，空串表示跟随默认。
	Provider, Model, Effort string
	// RuntimeMode 是新建会话的权限模式（readonly/supervised/autoAcceptEdits/auto/fullAccess），空串表示跟随默认。
	RuntimeMode string
	// RunMode 是运行方式（standalone/dedicated/heartbeat）；heartbeat 投递到已有会话，模型与权限设置不生效。
	RunMode string
}

// AutomationInput 表示创建或更新自动化任务的入参。
type AutomationInput struct {
	ProjectID, Name, Prompt, StopWhen              string
	Schedule                                       AutomationSchedule
	Enabled                                        bool
	MaxConsecutiveFailures, MaxIterations, MaxRuns int
	// CatchUp 表示错过调度触发时是否补跑一次；nil 表示更新时不修改。
	CatchUp *bool
	// Provider/Model/Effort 与 RuntimeMode 为空表示跟随默认。
	Provider, Model, Effort, RuntimeMode string
}

// AutomationRun 表示一次自动化运行记录。
type AutomationRun struct {
	ID, AutomationID, ThreadID, Status, Summary, Error string
	// Trigger 是本次运行触发方式：scheduled、catch_up 或 manual。
	Trigger string
	// MissedCount 是补跑时代偿过的错过触发次数。
	MissedCount           int
	StartedAt, FinishedAt time.Time
}

// AutomationSchedulerState 表示定时调度锁的持有状态。
type AutomationSchedulerState string

const (
	AutomationSchedulerActive       AutomationSchedulerState = "active"
	AutomationSchedulerPassiveHeld  AutomationSchedulerState = "passive_held"
	AutomationSchedulerPassiveError AutomationSchedulerState = "passive_error"
)

// AutomationSchedulerStatus 表示当前实例能否参与定时调度。
type AutomationSchedulerStatus struct {
	State AutomationSchedulerState
	Error string
}

// SwarmCredibility 记录 Bee 汇报中代码引用的可信度核验统计及摘要。
type SwarmCredibility struct {
	Cited      int
	Verified   int
	Unverified []string
	Summary    string
}

// SwarmBee 表示一个 Swarm 子 Bee 的执行状态与信息。
type SwarmBee struct {
	ThreadID      string
	Name          string
	Task          string
	Status        string
	Summary       string
	Activity      string
	Provider      string
	Model         string
	MergeError    string
	Index         int
	ToolCalls     int
	Tokens        int
	StartedAt     time.Time
	FinishedAt    *time.Time
	Merged        bool
	ConflictFiles []string
	ChangedFiles  []string
	// 等待人工审查合并
	PendingMerge bool
	// 合并前审查进度：空串表示未进入审查，其余为 running/approved/rejected/needs_human
	ReviewState string
	// 审查模型给出的结论或意见
	ReviewReason string
	// 审查会话的线程 ID
	ReviewThreadID string
	// 审查决定层：l0/l1/l2/l3/human，空串表示未进入审查
	ReviewLayer string
	// 审查疑点，每条通常为 path:line 加一句话
	ReviewConcerns []string
	// 审查消耗的 token 数
	ReviewTokens int
	// token 数是否为估算值
	ReviewTokensEstimated bool
	// 已放弃合并
	MergeDiscarded bool
	// 执行档位 ID
	Tier string
	// 只读调研模式，不允许修改文件
	ReadOnly bool
	// 用户直指模型而非档位默认模型
	DirectModel bool
	// 标记只读 Bee 发生了写文件违规
	ReadOnlyViolation bool
	// 项目不是 git 仓库，bee 在 mcode 私有影子仓库的独立副本中工作
	ShadowRepo bool
	// 项目不是 git 仓库且影子仓库不可用，bee 直接在主工作区修改文件
	SharedNoGit bool
	// 回退原因
	SharedNoGitReason string
	// 汇报代码引用的可信度核验统计及摘要
	Credibility *SwarmCredibility
	// 子线程待回答的提问，nil 表示没有
	PendingQuestion *SwarmBeeQuestion
}

// SwarmBeeQuestion 表示子线程当前待回答的提问，Forwarded 为 true 表示已转交主线程回答。
type SwarmBeeQuestion struct {
	RequestID string
	Questions []ApprovalQuestion
	Forwarded bool
	// AutoForwardAt 计划自动转交主线程的时间，零值表示未开启或已转交。
	AutoForwardAt time.Time
}

// SwarmState 表示会话的 Swarm 多 Bee 协作状态。
type SwarmState struct {
	LeadThreadID        string
	Available           bool
	Enabled             bool
	PlansOnly           bool
	PlansOnlyOverridden bool
	IsBee               bool
	Bees                []SwarmBee
	// Queued 是派发队列中等待启动的 bee 数。
	Queued int
	// Interrupted 是重启后被中断、等待用户续跑的子代理数与排队条目数。
	Interrupted int
}

// SlashCommand 斜杠补全候选项。
type SlashCommand struct {
	Name      string
	Detail    string
	Kind      string
	IsBuiltIn bool
}

// FileMatch @ 提及候选文件，Path 为相对项目根的路径。
type FileMatch struct {
	Name  string
	Path  string
	IsDir bool
}

// FileEntry 工作区文件列表中的单个条目，Path 为相对工作区根的路径。
type FileEntry struct {
	Name      string
	Path      string
	IsDir     bool
	IsSymlink bool
	Size      int64
	Ignored   bool
	Protected bool
}

// FileListing 单层目录列举结果。
type FileListing struct {
	Path      string
	Entries   []FileEntry
	Truncated bool
}

// FileContent 文件分块读取结果。
// Kind 为 "text"、"image" 或 "binary"：text 时 Text 为本块文本；
// image 时 Data 为本块已解码的原始字节；binary 时只有 TotalSize 有意义。
// NextOffset 为下一块起始偏移，HasMore 表示还有后续块。
type FileContent struct {
	Path       string
	Kind       string
	Text       string
	Data       []byte
	MIME       string
	TotalSize  int64
	NextOffset int64
	HasMore    bool
}

// Attachment 已上传的图片附件。
type Attachment struct {
	ID        string
	Name      string
	MimeType  string
	ByteCount int
}

// Script 表示 package.json 中声明的单个脚本或 mcode 的项目级脚本。
// Kind 为 "package"（package.json 脚本，只读）或 "project"（项目级脚本，可在弹窗里编辑）。
type Script struct {
	Name, Command, Runner string
	Favorite              bool
	Kind                  string
	ID                    string
	Symbol                string
	RunOnWorktreeCreate   bool
}

// ProjectScript 表示 mcode 自己的项目级脚本，
// 与工作区 package.json 无关，由脚本弹窗维护。
type ProjectScript struct {
	ID                  string
	Name                string
	Command             string
	Symbol              string
	RunOnWorktreeCreate bool
}

// NewThreadOptions 新建会话选项，空字段表示使用默认值。
type NewThreadOptions struct {
	Provider     string
	Model        string
	Effort       string
	Fast         bool
	BeesProvider string
	BeesModel    string
	// BeesEffort 与 SwarmEnabled 创建接口不接收，由 createThread 在创建后补写。
	BeesEffort   string
	SwarmEnabled bool
	// PairID 是创建时选中的 Swarm 配对 ID，非空时随 ComposerOptions 一起下发。
	PairID string
	// Provisional 为 true 时创建临时会话，未保留前会被延迟清理。
	Provisional bool
}

// RemoteServiceState 表示远程控制服务的运行状态。
type RemoteServiceState string

const (
	RemoteStateStopped RemoteServiceState = "stopped"
	RemoteStateRunning RemoteServiceState = "running"
	RemoteStateError   RemoteServiceState = "error"
)

// RemoteStatus 描述远程控制服务的当前状态与监听信息。
type RemoteStatus struct {
	State             RemoteServiceState
	ListenAddr        string
	Port              int
	LANURLs           []string
	Error             string
	ConnectedClients  int
	StartedListen     string
	StartedExtraHosts []string
	// PushSubscriptions 为当前登记的 Web Push 订阅总数。
	PushSubscriptions int
}

// RemotePairing 描述远程控制配对信息及有效期。
// State 取值：active、expired、used、unavailable。
type RemotePairing struct {
	PairingURL    string
	LANPairingURL string
	// TLSPairingURL 是 TLS 入口的配对链接；主链接被 Tailscale 入口占用时，设置页仍可展示它。
	TLSPairingURL string
	// TailscalePairingURL 是内嵌 Tailscale 入口的配对链接，入口未开始服务时为空。
	TailscalePairingURL string
	ShortCode           string
	ExpiresAt           time.Time
	State               string
}

// RemoteDevice 描述已授权远程控制的设备。
type RemoteDevice struct {
	ID          string
	Name        string
	Platform    string
	LastSeenAt  time.Time
	PairedAt    time.Time
	Online      bool
	LastAddress string
	LastEntry   string
	UserAgent   string
}

// RemoteTLSStatus 描述远程 HTTPS 入口的证书状态。
type RemoteTLSStatus struct {
	Configured         bool
	Domain             string
	TLSPort            int
	StartedURL         string
	CertDomains        []string
	Issuer             string
	NotAfter           time.Time
	Source             string
	Error              string
	HasCloudflareToken bool
}

// RemoteTailscaleStatus 描述内嵌 Tailscale 入口的状态。State 取值见 RemoteTailscaleDisabled 等常量。
type RemoteTailscaleStatus struct {
	Enabled  bool
	Hostname string
	State    string
	// AuthURL 仅 login_required 且已拿到授权地址时非空，可直接在浏览器打开完成登录。
	AuthURL string
	// FQDN 仅 running 有值，是 tailnet 内的 MagicDNS 域名。
	FQDN string
	// URL 是入口真正开始服务后的 https://<fqdn>；启动过程中为空。
	URL string
	// Error 是失败原因，供界面直接展示。
	Error string
	// LastLoginName / LastNodeName 是最近一次通过 Tailscale 建立连接的身份，仅用于展示与审计。
	LastLoginName string
	LastNodeName  string
}

// Tailscale 入口的状态取值，与 internal/remote/tailnet 的状态一一对应，另加 starting。
const (
	RemoteTailscaleDisabled      = "disabled"
	RemoteTailscaleStarting      = "starting"
	RemoteTailscaleLoginRequired = "login_required"
	RemoteTailscaleNeedsHTTPS    = "needs_https"
	RemoteTailscaleRunning       = "running"
	// RemoteTailscaleFailed 表示上次启动失败，Error 带原因；再次启用即可重试。
	RemoteTailscaleFailed = "failed"
)

// CustomEndpoint 描述一个自定义 OpenAI 兼容端点（桌面专用）；API Key 不回传明文，只用 HasAPIKey 表示 keyring 中是否已保存。
type CustomEndpoint struct {
	ID, Name, BaseURL, Model string
	Enabled                  bool
	HasAPIKey                bool
	Magpie                   bool
	// QuotaProviders 控制菜单栏额度面板展示的 Magpie 供应商列表：
	// nil 表示旧配置未设置，按只选 codex 处理；
	// 空切片表示一个都不展示；
	// 否则只展示列出的供应商 id（小写，等于 magpie.AccountProvider 的结果）。
	// 只对 Magpie 端点生效。
	QuotaProviders []string
	// PersonaWebURL 是 magpie web 管理地址（含端口，如 https://host:3430），mcode 经它读人设。
	PersonaWebURL string
	// PersonasJSON 是用户手动粘贴的人设列表 JSON。
	PersonasJSON string
	// PersonaRules 表示用户声明已在 magpie 部署 persona-rules，仅控制手动列表是否生效。
	PersonaRules bool
	// HasWebKey 表示密钥库里已保存 magpie web 管理密钥；明文永远不进这个结构体。
	HasWebKey bool
}

// PersonaSourceTestResult 是一条人设来源的测试结果（桌面专用）；Source 取值为
// "local"、"web"、"manual" 或空串，Message 是给用户看的中文结果文案，失败时是已脱敏的原因。
type PersonaSourceTestResult struct {
	OK      bool
	Source  string
	Count   int
	Message string
}

// ProviderSetting 设置页中单个 Provider 的可编辑配置（桌面专用）；API Key 不回传明文。
// Kind 取值 "cli"（可执行文件与更新监控）或 "api"（BaseURL、Model 与 API Key）。
type ProviderSetting struct {
	ID, Name, Kind string
	Enabled        bool
	Bin            string
	UpdateMonitor  bool
	BaseURL        string
	Model          string
	DefaultBaseURL string
	DefaultModel   string
	HasAPIKey      bool
	Auth           string
	// PiCacheLong 仅 pi 使用：开启后给 pi 子进程设 PI_CACHE_RETENTION=long，
	// 支持的服务商（如 Anthropic）改用更长的 prompt 缓存。
	PiCacheLong bool
	// PiVisionModel 仅 pi 使用：格式为 provider/model，填了以后当前模型看不了图片时
	// 由它描述图片；留空则不启用。
	PiVisionModel string
}

// CustomEndpointTestResult 连通性测试结果。
type CustomEndpointTestResult struct {
	OK         bool
	StatusCode int
	Message    string
	Models     []string
	Latency    time.Duration
}

// ModelPin 表示模型选择器中的固定模型（复用 config.ModelPin）。
type ModelPin = config.ModelPin

// ModelPreference 记录模型偏好设置（复用 config.ModelPreference）。
type ModelPreference = config.ModelPreference

// SwarmPair 表示一组 Swarm 主导与执行模型配对（复用 config.SwarmPair）。
type SwarmPair = config.SwarmPair

// RevertFile 描述撤销轮次预览中的单个文件改动。
type RevertFile struct {
	Path      string
	IsKept    bool
	Additions int
	Deletions int
}

// RevertPreview 描述撤销轮次的预览信息。
type RevertPreview struct {
	TurnID    string
	Files     []RevertFile
	Conflicts []string
}

// WorktreeInfo 描述一个 Git worktree 的详细信息。
type WorktreeInfo struct {
	Path        string
	Branch      string
	ProjectPath string
	Merged      bool
	Active      bool
	AgeDays     int
	SizeBytes   int64
	// Clean 表示该 worktree 没有未提交改动。
	Clean bool
	// Orphan 表示该 worktree 的目录已不存在。
	Orphan bool
}

// ProviderVersionInfo 描述提供方的版本与更新状态。
type ProviderVersionInfo struct {
	Provider        string
	Installed       string
	Latest          string
	UpdateAvailable bool
	ExecutablePath  string
	// Channel 是从可执行文件路径认出的安装渠道（brew/npm/bun/pnpm/native），空串表示未知。
	Channel string
	// UpdateCommand 是按安装渠道生成的升级命令；未知渠道时回落到 CLI 自带更新器，没有则为空。
	UpdateCommand string
}

// ProviderAccountInfo 描述提供方的账户信息。
type ProviderAccountInfo struct {
	Provider string
	Email    string
	Plan     string
}

// ProviderQuotaWindow 表示提供方额度使用窗口。
// 多账户提供方（如 agy）会把账户名写进 Label 前缀「Account · 窗口名」；
// Account 与 Active 由后端填写，为空时 UI 侧从 Label 推断。
type ProviderQuotaWindow struct {
	// ID 标识窗口来源（如 extra-usage、tools-0、standard-fiveHour），
	// 额度条按它排除 MCP tools、附加用量等辅助窗口；后端没给时为空串。
	ID string
	// Period 是后端给出的周期标记（5h、1d、7d、30d），为空时 UI 再从 Label 推断。
	Period      string
	Label       string
	UsedPercent float64
	ResetsAt    time.Time
	Account     string
	// Active 表示这是后端认定新会话当前会用到的账号。
	Active bool
	// Disabled 表示账号已停用（路由覆盖关了它），不算法在用，界面显示「已停止」。
	Disabled bool
	// Source 是额度数据来源：magpie 表示来自 Magpie 网关缓存。只有后端给出的唯一
	// Active 标记才认在用，没有标记时展示最近应答账户或未知；空表示直连本机登录。
	Source string
	// Plan 是该窗口所属账号的套餐；为空时不展示。Magpie 卡片自带套餐，与本机登录套餐无关。
	Plan string
	// ReadAt 是数据在来源端的读取时间，零值表示未知（不能当作当前时间）。
	ReadAt time.Time
	// Provider 是 Magpie 的 provider id（如 claude、codex），空表示不是 Magpie 卡片。
	Provider string
	// User 是这张卡所属账号的邮箱或 login；Magpie 没给时为空，Account 只是展示名。
	User string
	// Kind 是卡片粒度：account（账号）、key（API key）或 provider（整家供应商）。
	Kind string
	// Alias 是用户给账号起的展示别名，可能为空。
	Alias string
	// Last 表示这是 Magpie 记录的该 provider 最近应答的那张卡；它只证明「最近使用」，
	// 不能证明当前会话在用。
	Last bool
	// LastServedAt 是 Magpie 记录的该 provider 最近一次应答时间，零值表示未知。
	LastServedAt time.Time
	// Display 是 Magpie 给的展示文字，例如 $12.30 / $40。
	Display string
	// Text 为 true 表示只有文字、没有占比的窗口，UsedPercent 不可用。
	Text bool
	// Unlimited 为 true 表示不限量。
	Unlimited bool
	// Balance 是剩余数量（如钱包余额/点数），nil 表示未提供。
	Balance *float64
	// Unit 是额度单位（如 "credits"）。
	Unit string
}

// PiMagpieModel 是 Magpie 端点下可注入 pi 线程的一个模型。
// Name 为精简显示名（如 deepseek-v4.1-flash），ID 是注入后用于拼装 pi 模型 ID 的原始标识。
type PiMagpieModel struct {
	ID   string
	Name string
}

// PiMagpieEndpoint 表示一个已启用的 Magpie 端点及其可注入 pi 的模型。
type PiMagpieEndpoint struct {
	ID   string
	Name string
	// ProviderID 是 pi 侧该端点对应的 provider id（如 magpie-jp），
	// 注入后 pi 线程的模型 ID 形如「 ProviderID/auto/模型名 」。
	ProviderID string
	Models     []PiMagpieModel
	// Selected 是用户已勾选的模型 ID；后端读取失败时 Err 非空、Models 为空。
	Selected []string
	Err      string
}

// AgentMagpieGlobal 是某 agent 写全局配置的状态。
type AgentMagpieGlobal struct {
	// Supported 表示该 agent 是否支持写全局配置。
	Supported bool
	// State 取 none、applied、drifted、kept、missing、error。
	State string
	// Path 是配置文件路径。
	Path string
	// Drifted 是被改动的键。
	Drifted []string
	// Err 是上一次读写全局配置失败的原因。
	Err string
	// ManagedPath 非空表示系统管理员配置存在、可能覆盖本设置。
	ManagedPath string
}

// AgentMagpieSetting 是某 agent 的 magpie 设置：模式、各端点模型与勾选、全局配置状态。
type AgentMagpieSetting struct {
	// Agent 是该设置对应的 agent id（如 claude）。
	Agent string
	// Mode 取 inject 或 global。
	Mode string
	// Endpoints 是可注入该 agent 的 Magpie 端点及其模型。
	Endpoints []PiMagpieEndpoint
	// DefaultEndpointID 是用户指定的默认模型所在端点 ID；空串表示按自动规则选。
	DefaultEndpointID string
	// DefaultModel 是用户指定的默认模型 ID；空串表示按自动规则选。
	DefaultModel string
	// Global 是写全局配置的状态。
	Global AgentMagpieGlobal
	// InjectSupported 表示该 agent 是否有运行时注入通道；false 表示只能用
	// 全局配置（例如 cline），UI 据此只显示一个“写全局配置”开关。
	InjectSupported bool
	// Tiers 是写全局配置时 Claude Code 各档位实际指向的模型，按 opus、sonnet、
	// haiku、fable 顺序；没有可用端点或勾选时为空。只有支持写全局配置的 agent 才填。
	Tiers []AgentMagpieTier
}

// AgentMagpieTier 是 Claude Code 一个档位（/model opus、子代理的 model: haiku 等）
// 在全局配置里指向的模型。
type AgentMagpieTier struct {
	// Tier 是档位名：opus、sonnet、haiku 或 fable。
	Tier string
	// EndpointID 是该档位模型所在端点 ID。
	EndpointID string
	// ModelID 是端点侧原始模型 ID；空串表示该档不写，Claude Code 用自带默认。
	ModelID string
}

// MagpiePersona 是 magpie 网关提供的一个可注入人设。
type MagpiePersona struct {
	ID          string
	Name        string
	Description string
}

// ProviderCredits 表示按量计费提供方的预付费余额。
type ProviderCredits struct {
	Provider string
	Total    float64
	Currency string
	// AmountText 与 BreakdownText 由后端格式化，UI 侧不做货币格式化。
	AmountText    string
	BreakdownText string
	Usable        bool
	ReadAt        time.Time
}

// Depleted 表示余额不可用或已耗尽。
func (c ProviderCredits) Depleted() bool { return !c.Usable || c.Total <= 0 }

// ErrNotImplemented 表示功能尚未实现。
var ErrNotImplemented = errors.New("mcode: 功能尚未实现")

// quotaThreadKey 是额度请求上下文携带会话 ID 的键。
type quotaThreadKey struct{}

// WithQuotaThread 把界面当前会话 ID 放进 ctx，额度弹窗据此把「在用」标到该会话
// 实际绑定的账号上（同一个 provider 的多账号场景）。会话还没跑过一轮、后端解析不出
// provider 会话时，后端按新会话的目标账号兜底。
func WithQuotaThread(ctx context.Context, threadID string) context.Context {
	if ctx == nil || threadID == "" {
		return ctx
	}
	return context.WithValue(ctx, quotaThreadKey{}, threadID)
}

// QuotaThread 取出 ctx 里的会话 ID；没有时返回空串。
func QuotaThread(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(quotaThreadKey{}).(string)
	return id
}

// Client 定义与后端服务通信的客户端接口。
type Client interface {
	// Projects 获取所有项目列表。
	Projects(ctx context.Context) ([]Project, error)
	// AddProject 根据本地目录路径添加项目。
	AddProject(ctx context.Context, path string) (Project, error)
	// Threads 获取指定项目的会话列表。
	Threads(ctx context.Context, projectID string) ([]Thread, error)
	// ArchivedThreads 获取所有已归档的会话列表。
	ArchivedThreads(ctx context.Context) ([]Thread, error)
	// History 获取指定会话的历史消息。
	History(ctx context.Context, threadID string) ([]Message, error)
	// Providers 获取当前所有可用的 Agent 提供方列表。
	Providers(ctx context.Context) ([]ProviderInfo, error)
	// RefreshProviders 强制刷新供应商模型目录；部分失败时仍返回可用的目录。
	RefreshProviders(ctx context.Context) ([]ProviderInfo, error)
	// CreateThread 在指定项目下创建新会话，provider 为空时表示使用默认值。
	CreateThread(ctx context.Context, projectID, provider string) (threadID string, err error)
	// CreateThreadWithOptions 在指定项目下创建新会话，支持指定模型与 Swarm Bee 配置。
	CreateThreadWithOptions(ctx context.Context, projectID string, opts NewThreadOptions) (threadID string, err error)
	// Send 向指定会话发送消息；delegated 为 true 表示消息被交给子线程，本会话不会开始新一轮。
	Send(ctx context.Context, threadID, text string) (delegated bool, err error)
	// Interrupt 中断当前正在运行的这一轮，没有在运行时什么也不做；alreadyStopped 为 true 表示后端确认无运行轮次。
	Interrupt(ctx context.Context, threadID string) (alreadyStopped bool, err error)
	// SplitSession 在指定会话里插一条分割线：成功表示后端已追加一条 system 消息（Kind 为
	// split_session），从下一条消息起发给模型的上下文不再包含分割线以上的历史。
	// 失败时错误消息已经是中文，可直接展示给用户
	// （例如「这一轮还在进行，等它结束再分割」「会话还没有内容，不需要分割」）。
	SplitSession(ctx context.Context, threadID string) error
	// KillRunningCommand 只终止该工具消息对应的正在运行的命令子进程，不中断本轮；
	// killed 为 0 表示没找到进程。
	KillRunningCommand(ctx context.Context, threadID, messageID string) (killed int, err error)
	// BackgroundTaskLog 获取指定后台任务的日志。
	BackgroundTaskLog(ctx context.Context, threadID, messageID string) (text string, available bool, err error)
	// StopBackgroundTask 停止指定的后台任务。
	StopBackgroundTask(ctx context.Context, threadID, messageID string) (stopped bool, err error)
	// Approvals 获取指定会话的待处理审批请求。
	Approvals(ctx context.Context, threadID string) ([]Approval, error)
	// Respond 对审批请求做出决策回复。
	Respond(ctx context.Context, a Approval, optionID string) error
	// RespondAnswers 携带答案答复提问或计划驳回；answers 键为题目 ID（计划驳回为 "feedback"），skip 为 true 表示跳过提问。
	RespondAnswers(ctx context.Context, a Approval, optionID string, answers map[string][]string, skip bool) error
	// Watch 监听指定会话的状态更新与审批变化。
	Watch(threadID string, onUpdate func(ThreadUpdate), onApprovals func([]Approval)) (stop func())
	// Usage 获取指定会话的 token 用量统计（每轮及全会话合计）。
	Usage(ctx context.Context, threadID string) (ThreadUsage, error)
	// SetThreadRuntimeMode 设置指定会话的运行模式。
	SetThreadRuntimeMode(threadID, mode string) error
	// SetThreadAgent 设置指定会话的 Agent 与模型，provider 为空时表示不修改供应商。
	SetThreadAgent(threadID, provider, model string) error
	// RenameThread 重命名指定会话。
	RenameThread(threadID, title string) error
	// SetThreadPinned 设置指定会话的置顶状态。
	SetThreadPinned(threadID string, pinned bool) error
	// SetThreadArchived 设置指定会话的归档状态。
	SetThreadArchived(threadID string, archived bool) error
	// SetThreadDormant 设置指定会话的休眠状态。
	SetThreadDormant(threadID string, dormant bool) error
	// DeleteThread 删除指定会话。
	DeleteThread(threadID string) error
	// RemoveProject 从侧边栏移除指定项目。
	RemoveProject(projectID string) error
	// Diff 获取指定会话或指定轮次的代码改动，turnID 为空表示整个会话。
	Diff(ctx context.Context, threadID, turnID string) (ThreadDiff, error)
	// KanbanBoard 获取看板数据，projectID 为空返回全部。
	KanbanBoard(ctx context.Context, projectID string) (KanbanBoard, error)
	// CreateTask 在指定项目下创建新任务。
	CreateTask(ctx context.Context, projectID, title, notes string) (KanbanTask, error)
	// MoveTask 移动任务到指定状态。
	MoveTask(ctx context.Context, taskID, status string, revision int) (KanbanTask, error)
	// StartTask 开始执行指定任务。
	StartTask(ctx context.Context, taskID string, revision int) (KanbanTask, error)
	// ReviewTask 评审任务（decision: accept/reject/requestReview）。
	ReviewTask(ctx context.Context, taskID, decision, feedback string, revision int) (KanbanTask, error)
	// CreateGoal 在指定项目下创建新目标。
	CreateGoal(ctx context.Context, projectID, prompt string) (KanbanGoal, error)
	// StartGoal 开始执行目标。
	StartGoal(ctx context.Context, goalID string, revision int) (KanbanGoal, error)
	// PauseGoal 暂停目标执行。
	PauseGoal(ctx context.Context, goalID string, revision int) (KanbanGoal, error)
	// CancelGoal 取消目标。
	CancelGoal(ctx context.Context, goalID string, revision int) (KanbanGoal, error)
	// ReplanGoal 重新规划目标。
	ReplanGoal(ctx context.Context, goalID, feedback string, revision int) (KanbanGoal, error)
	// WatchKanban 看板任一变更时回调，可能来自任意 goroutine。
	WatchKanban(onChange func()) (stop func())
	// Automations 获取指定项目的自动化列表。
	Automations(ctx context.Context, projectID string) ([]Automation, error)
	// CreateAutomation 创建自动化任务。
	CreateAutomation(ctx context.Context, in AutomationInput) (Automation, error)
	// UpdateAutomation 更新自动化任务。
	UpdateAutomation(ctx context.Context, id string, in AutomationInput, revision int) (Automation, error)
	// DeleteAutomation 删除自动化任务。
	DeleteAutomation(ctx context.Context, id string) error
	// RunAutomation 立即触发运行一次自动化任务。
	RunAutomation(ctx context.Context, id string) (AutomationRun, error)
	// AutomationRuns 获取指定自动化任务的历史运行记录。
	AutomationRuns(ctx context.Context, id string, limit int) ([]AutomationRun, error)
	// AutomationSchedulerStatus 获取当前实例的定时调度锁状态。
	AutomationSchedulerStatus(ctx context.Context) (AutomationSchedulerStatus, error)
	// WatchAutomations 在配置或运行记录变化时回调，可能来自任意 goroutine。
	WatchAutomations(onChange func()) (stop func())
	// Swarm 获取指定会话的 Swarm 状态。
	Swarm(ctx context.Context, threadID string) (SwarmState, error)
	// SetSwarmEnabled 设置会话是否开启 Swarm 多 bee 协作。
	SetSwarmEnabled(ctx context.Context, threadID string, enabled bool) (SwarmState, error)
	// SetSwarmPlansOnly 设置会话是否开启 Swarm 只规划开关覆盖。
	SetSwarmPlansOnly(ctx context.Context, threadID string, plansOnly bool) (SwarmState, error)
	// StopSwarmBee 停止指定的 Swarm bee。
	StopSwarmBee(ctx context.Context, leadThreadID, beeThreadID string) error
	// StopAllSwarmBees 停止所有运行中的 Swarm bee。
	StopAllSwarmBees(ctx context.Context, leadThreadID string) error
	// ClearFinishedSwarmBees 清理已完成或结束的 Swarm bee。
	ClearFinishedSwarmBees(ctx context.Context, leadThreadID string) error
	// ForwardBeeQuestion 把子线程提问转给主线程回答。
	ForwardBeeQuestion(ctx context.Context, leadThreadID, beeThreadID, requestID string) error
	// SwarmMergeDiff 获取 bee 待合并改动（Diff 为 unified diff 文本，FilesChanged 为改动文件）。
	SwarmMergeDiff(ctx context.Context, leadThreadID, beeThreadID string) (ThreadDiff, error)
	// SwarmReportSummary 从存档读取 bee 的完整汇报总结；只能读该主会话自己的 bee，读不到时返回错误。
	SwarmReportSummary(ctx context.Context, leadThreadID, beeID string) (string, error)
	// ApproveSwarmMerge 批准并应用 bee 的改动。
	ApproveSwarmMerge(ctx context.Context, leadThreadID, beeThreadID string) error
	// DiscardSwarmMerge 放弃 bee 的改动。
	DiscardSwarmMerge(ctx context.Context, leadThreadID, beeThreadID string) error
	// WatchSwarm 监听 Swarm 状态变化，并在变更时以 leadThreadID 触发回调。
	WatchSwarm(onChange func(leadThreadID string)) (stop func())
	// MCPSignIn 执行指定 MCP 服务的 OAuth 授权登录流程。
	MCPSignIn(ctx context.Context, serverID, serverURL string) error
	// MCPSignInWithAuth 按目录条目声明的登录方式执行登录；auth 为 nil 时与 MCPSignIn 一致。
	MCPSignInWithAuth(ctx context.Context, serverID, serverURL string, auth *agentconf.MCPCatalogAuth) error
	// MCPSignOut 清除指定 MCP 服务的授权凭据及客户端注册信息。
	MCPSignOut(serverID string) error
	// MCPIsSignedIn 查询指定 MCP 服务当前是否已登录授权。
	MCPIsSignedIn(serverID string) bool
	// Commands 获取指定会话可用的斜杠命令列表（包含内置命令与技能）。
	Commands(ctx context.Context, threadID string) ([]SlashCommand, error)
	// SearchFiles 在指定会话的工作区内模糊搜索文件和目录。
	SearchFiles(ctx context.Context, threadID, query string) ([]FileMatch, error)
	// FilesList 列出指定会话工作区内某个目录的单层条目，subpath 为空表示工作区根目录。
	FilesList(ctx context.Context, threadID, subpath string) (FileListing, error)
	// FilesRead 读取指定会话工作区内的文件分块，offset 为起始字节偏移，length 为期望字节数。
	FilesRead(ctx context.Context, threadID, path string, offset, length int64) (FileContent, error)
	// UploadAttachment 上传单张图片附件，返回附件元数据。
	UploadAttachment(ctx context.Context, name, mimeType string, data []byte) (Attachment, error)
	// SendWithAttachments 向指定会话发送附带附件的消息；delegated 含义同 Send。
	SendWithAttachments(ctx context.Context, threadID, text string, attachmentIDs []string) (delegated bool, err error)
	// Scripts 列出会话的项目级脚本与工作区 package.json 中的脚本。
	Scripts(ctx context.Context, threadID string) ([]Script, error)
	// ProjectScripts 读取会话所属项目的项目级脚本。
	ProjectScripts(ctx context.Context, threadID string) ([]ProjectScript, error)
	// SetProjectScripts 覆盖保存会话所属项目的项目级脚本。
	SetProjectScripts(ctx context.Context, threadID string, scripts []ProjectScript) error
	// ThreadWorkDir 返回会话的实际工作目录（包括 worktree）。
	ThreadWorkDir(threadID string) (string, bool)
	// AgyAccounts 获取所有 agy 账号列表。
	AgyAccounts() ([]accounts.Account, error)
	// AgyDefaultAccountID 获取当前默认的 agy 账号 ID。
	AgyDefaultAccountID() string
	// AgyLoginStart 发起浏览器 OAuth 登录流程，返回授权跳转 URL。
	AgyLoginStart(ctx context.Context) (string, error)
	// AgyLoginSubmit 提交手动输入的回调 URL 或授权码完成登录。
	AgyLoginSubmit(input string) error
	// AgyLoginWait 等待后台浏览器 OAuth 流程完成，返回新登录的账号元数据。
	AgyLoginWait(ctx context.Context) (accounts.Account, error)
	// AgyLoginCancel 取消正在进行的登录流程。
	AgyLoginCancel()
	// AgyTerminalLogin 准备终端隔离登录，返回临时标识、环境变量及命令行参数。
	AgyTerminalLogin() (id string, env []string, args []string, err error)
	// AgyAdoptTerminalLogin 接纳已完成的终端登录，返回新导入的账号元数据。
	AgyAdoptTerminalLogin(ctx context.Context, id string) (accounts.Account, error)
	// AgyRemove 删除指定的 agy 账号。
	AgyRemove(id string) error
	// AgyBoundConversations 返回绑定在指定账号上的活跃会话 ID 列表。
	AgyBoundConversations(id string) []string
	// AgySetDefault 将指定账号设为默认账号。
	AgySetDefault(id string) error
	// AgyUpdate 更新指定账号的配置补丁（标签、启用状态、顺序、权重等）。
	AgyUpdate(id string, p accounts.UpdatePatch) error
	// AgyRouting 获取当前 agy 账号的全局路由调度配置。
	AgyRouting() (multiaccount.Config, error)
	// AgySetRouting 更新当前 agy 账号的全局路由调度配置。
	AgySetRouting(cfg multiaccount.Config) error
	// AgyRefreshQuota 刷新指定账号的额度快照（id 为空表示刷新全部账号）。
	AgyRefreshQuota(ctx context.Context, id string) error
	// AgyWarmup 对指定账号（或全部有效账号，id 为空）执行 Gemini 预热。
	AgyWarmup(ctx context.Context, id string) error
	// AgyAutoWarmup 获取当前 AGY 自动预热开关状态。
	AgyAutoWarmup() (bool, error)
	// AgySetAutoWarmup 设置当前 AGY 自动预热开关状态。
	AgySetAutoWarmup(enabled bool) error
	// GitState 获取会话工作目录的 Git 状态（分支、worktree、未提交变更数）。
	GitState(ctx context.Context, threadID string) (GitState, error)
	// GitCheckout 在会话工作目录切换到指定分支。
	GitCheckout(ctx context.Context, threadID, branch string) error
	// GitSwitchWorktree 将会话工作目录切换到指定 worktree 路径。
	GitSwitchWorktree(ctx context.Context, threadID, path string) error
	// GitCommit 提交会话工作目录的全部未提交变更。
	GitCommit(ctx context.Context, threadID, message string) error
	// GitChangedFiles 列出会话工作目录里可勾选提交的改动文件。
	GitChangedFiles(ctx context.Context, threadID string) ([]GitChangedFile, error)
	// GitCommitSelected 只提交会话工作目录里勾选的改动（重命名需同时给出新旧路径）。
	GitCommitSelected(ctx context.Context, threadID string, paths []string, message string) error
	// GitPush 推送会话工作目录的当前分支到远程。
	GitPush(ctx context.Context, threadID string) error
	// GitCreateBranch 在会话工作目录创建并切换到新分支。
	GitCreateBranch(ctx context.Context, threadID, name string) error
	// GitFetch 抓取会话工作目录所在仓库的远程分支。
	GitFetch(ctx context.Context, threadID string) error
	// GitPull 快进拉取会话工作目录当前分支的上游。
	GitPull(ctx context.Context, threadID string) error
	// GitFastForwardInto 把当前分支快进合并到 branch（branch 是被合并进来的分支）。
	GitFastForwardInto(ctx context.Context, threadID, branch string) error
	// GitMergeFrom 把 branch 合并到当前分支。
	GitMergeFrom(ctx context.Context, threadID, branch string) error
	// GitStash 贮藏会话工作目录的改动。
	GitStash(ctx context.Context, threadID string) error
	// GitStashPop 恢复最近一条贮藏。
	GitStashPop(ctx context.Context, threadID string) error
	// GenerateCommitMessage 为指定会话的未提交变更生成提交信息。
	GenerateCommitMessage(ctx context.Context, threadID string) (string, error)
	// BranchThread 从指定的助手最终答复分支出新会话，返回新会话 ID。
	BranchThread(ctx context.Context, threadID, messageID string) (string, error)
	// ForwardThread 将已有会话的历史上下文转发为新会话，返回新会话 ID。
	ForwardThread(ctx context.Context, threadID string) (string, error)
	// ThreadForwardedContext 返回转发会话实际带入新会话的历史上下文，第二个返回值为是否可用。
	ThreadForwardedContext(ctx context.Context, threadID string) (ForwardedContext, bool, error)
	// SetThreadComposerPrefs 保存指定会话的 composer 偏好（Build/Plan、effort、Bees 模型），整体覆盖。
	SetThreadComposerPrefs(threadID string, prefs ComposerPrefs) error
	// SetThreadBeeTiers 保存指定会话的 Swarm Bee 档位覆盖配置（JSON 格式，空表示无覆盖）。
	SetThreadBeeTiers(threadID, beeTiers string) error
	// SetThreadSwarmPairID 记录会话选中的 Swarm 配对 ID，空表示改为按模型推断。
	SetThreadSwarmPairID(threadID, pairID string) error
	// SetThreadFastMode 设置指定会话的快速模式与速率档位；fastMode 为 false 时忽略 speedTier。
	// 会话当前值通过 Thread.FastMode/SpeedTier 读取。
	SetThreadFastMode(threadID string, fastMode bool, speedTier string) error
	// SetThreadSwarmMode 设置指定会话的 Swarm 工作模式（inherited/collaborative/leadPlansOnly/alwaysBees）。
	// 会话当前值通过 Thread.SwarmMode 读取，空值视同 inherited。
	SetThreadSwarmMode(threadID, mode string) error
	// SetThreadMCPDisabled 保存指定会话的外部 MCP 禁用名单（元素为 agent:scope:name 键），空表示沿用全局设置。
	// 会话当前值通过 Thread.MCPDisabled 读取。
	SetThreadMCPDisabled(ctx context.Context, threadID string, keys []string) error
	// Queue 列出指定会话排队等待发送的消息列表。
	Queue(ctx context.Context, threadID string) ([]QueuedFollowUp, error)
	// Enqueue 在会话运行中把消息加入排队队列。
	Enqueue(ctx context.Context, threadID, text string, attachmentIDs []string) error
	// RemoveQueued 从指定会话的排队队列中移除跟进消息。
	RemoveQueued(ctx context.Context, threadID, followUpID string) error
	// SendQueuedNow 立即从队列发送指定跟进项。
	SendQueuedNow(ctx context.Context, threadID, followUpID string) error
	// MoveQueued 把指定跟进项（连同所在 bundle）移到 beforeFollowUpID 之前，空表示移到队尾。
	MoveQueued(ctx context.Context, threadID, followUpID, beforeFollowUpID string) error
	// WatchQueue 监听任一会话队列变化的回调，可能来自任意 goroutine，返回 stop 函数用于注销监听。
	WatchQueue(onChange func(threadID string, items []QueuedFollowUp)) (stop func())
	// WatchThreadSummaries 监听任一会话运行态的切换，回调带上该会话的最新摘要，可能来自任意 goroutine。
	// 侧栏的运行图标据此纠正只来自会话列表快照的运行标记。
	WatchThreadSummaries(onChange func(update Thread)) (stop func())
	// ContextDocs 获取指定项目文档中已存在的文档列表，Name 为引用格式；缺失的标准文档不返回，由产品端补齐。
	ContextDocs(projectPath string) ([]ContextDoc, error)
	// ReadContextDoc 读取指定项目文档中的单篇文档；name 为引用格式（instructions.md 或 docs/x.md），文档不存在时返回包装了 projctx.ErrNotFound 的错误。
	ReadContextDoc(projectPath, name string) (ContextDoc, error)
	// WriteContextDoc 写入或更新项目文档中的单篇文档；name 为引用格式；expectedHash 为空表示强制覆盖，发生冲突时返回的 error 必须满足 errors.Is(err, ErrContextConflict)。
	WriteContextDoc(projectPath, name, content, expectedHash string) (ContextDoc, error)
	// WatchContextDocs 监听项目文档文档变更，onChange 可能在任意 goroutine 上调用，返回 stop 函数用于注销监听。
	WatchContextDocs(projectPath string, onChange func()) (stop func(), err error)
	// CreateContextDoc 按引用格式创建文档（标准文档用标准模板，自定义文档只带一级标题）；已有同名文档时返回错误，不覆盖。
	CreateContextDoc(projectPath, name string) (ContextDoc, error)
	// MemoryProposals 列出指定项目的记忆提议；status 为空表示不按状态过滤，按创建时间倒序返回。
	MemoryProposals(projectPath, status string) ([]MemoryProposal, error)
	// CountPendingMemoryProposals 统计指定项目待审核（pending）的记忆提议条数，用于标题栏角标。
	CountPendingMemoryProposals(projectPath string) (int, error)
	// UpdateMemoryProposal 保存用户对提议的标题、内容、类型与重要度的修改，其余字段保持不变。
	UpdateMemoryProposal(p MemoryProposal) error
	// AcceptMemoryProposal 接受一条待审提议并写入 Nowledge Mem；写入失败时提议保持 pending 并带回错误。
	AcceptMemoryProposal(id string) (MemoryProposal, error)
	// RejectMemoryProposal 驳回一条待审提议，只改本地状态，不写远端。
	RejectMemoryProposal(id string) error
	// CreateMemoryProposal 用户手动新建一条记忆提议，Source 记为 user，初始状态为 pending。
	CreateMemoryProposal(projectPath, threadID, title, content string) (MemoryProposal, error)
	// DistillThreadMemories 从 Nowledge 已同步的会话提炼候选记忆，作为 distill 来源的待审提议放进收件箱，返回新增条数。
	DistillThreadMemories(projectPath, threadID string) (int, error)
	// SearchMemories 在项目对应的空间里语义检索记忆。
	SearchMemories(projectPath, query string) ([]MemoryItem, error)
	// ForgetMemory 忘掉一条记忆：服务端支持删除时直接删除；deleted 为 false 表示服务端不支持删除，
	// 已改为给标题打上“已废弃”前缀。
	ForgetMemory(projectPath, id string) (deleted bool, err error)
	// ExportMemories 把项目空间的全部记忆导出成 Markdown（跳过已废弃的记忆）。
	ExportMemories(projectPath string) (string, error)
	// SetMemoryProposalTarget 把待审提议设为改写某条已有记忆（合并目标）；targetMemoryID 为空表示取消合并、恢复为新增。
	SetMemoryProposalTarget(id, targetMemoryID string) error
	// CondenseMemory 用模型精简记忆，返回精简后的标题与正文；mergeWith 非空时为要合并进来的已有记忆全文（标题+正文），结果应合并去重后精简。
	CondenseMemory(projectPath, title, content, mergeWith string) (string, string, error)
	// WorkingMemory 读取项目对应空间的今日 Working Memory 正文（Markdown）。
	WorkingMemory(projectPath string) (string, error)
	// MemoryBackend 返回当前生效的记忆后端。
	MemoryBackend() string
	// MemorySpaces 列出 Nowledge Mem 中所有记忆空间，供设置页的项目记忆空间下拉框使用。
	MemorySpaces(ctx context.Context) ([]MemorySpace, error)
	// ResolveMemorySpace 解析项目路径对应的记忆空间：override 非空时按它匹配，否则按仓库目录同名匹配。
	ResolveMemorySpace(ctx context.Context, projectPath, override string) (MemorySpaceResolution, error)
	// RemoteStatus 获取远程控制服务的运行状态（桌面专用，不走 RPC）。
	RemoteStatus(ctx context.Context) (RemoteStatus, error)
	// SetRemoteEnabled 开启或关闭远程控制服务（桌面专用，不走 RPC）；enabled 为 true 时按当前配置启动服务，为 false 时停止。
	SetRemoteEnabled(ctx context.Context, enabled bool) (RemoteStatus, error)
	// RemotePairing 获取或换发远程控制配对信息（桌面专用，不走 RPC）；refresh 为 true 时换发新码，否则返回当前有效码（过期则自动换发），服务未运行时返回错误。
	RemotePairing(ctx context.Context, refresh bool) (RemotePairing, error)
	// RemoteDevices 获取所有已授权的远程设备列表（桌面专用，不走 RPC）。
	RemoteDevices(ctx context.Context) ([]RemoteDevice, error)
	// RevokeRemoteDevice 撤销指定的远程设备（桌面专用，不走 RPC）。
	RevokeRemoteDevice(ctx context.Context, deviceID string) error
	// RenameRemoteDevice 重命名指定的远程设备（桌面专用，不走 RPC）。
	RenameRemoteDevice(ctx context.Context, deviceID, name string) error
	// RevokeAllRemoteDevices 撤销所有已授权的远程设备（桌面专用，不走 RPC）。
	RevokeAllRemoteDevices(ctx context.Context) error
	// RestartRemote 重启远程控制服务并返回最新状态（桌面专用，不走 RPC）。
	RestartRemote(ctx context.Context) (RemoteStatus, error)
	// SendRemoteTestPush 向所有已登记的 Web Push 订阅发送一条测试通知，返回实际发送的端点数（桌面专用，不走 RPC）。
	SendRemoteTestPush(ctx context.Context) (int, error)
	// RemoteTLSStatus 获取远程 HTTPS 入口的证书状态（桌面专用，不走 RPC）。
	RemoteTLSStatus(ctx context.Context) (RemoteTLSStatus, error)
	// SaveRemoteCloudflareToken 保存或清除 Cloudflare API Token（桌面专用，不走 RPC）；token 为空表示清除。
	SaveRemoteCloudflareToken(token string) error
	// HasRemoteCloudflareToken 查询是否已保存 Cloudflare API Token（桌面专用，不走 RPC）。
	HasRemoteCloudflareToken() bool
	// IssueRemoteTLSCertificate 通过 Cloudflare DNS-01 申请并安装远程 TLS 证书（桌面专用，不走 RPC）。
	IssueRemoteTLSCertificate(ctx context.Context) (RemoteTLSStatus, error)
	// ImportRemoteTLSCertificate 导入证书链与私钥并安装为远程 TLS 证书（桌面专用，不走 RPC）。
	ImportRemoteTLSCertificate(ctx context.Context, chainPath, keyPath string) (RemoteTLSStatus, error)
	// RemoveRemoteTLSCertificate 移除已安装的远程 TLS 证书（桌面专用，不走 RPC）。
	RemoveRemoteTLSCertificate(ctx context.Context) error
	// RemoteTailscaleStatus 获取内嵌 Tailscale 入口的状态（桌面专用，不走 RPC）。
	RemoteTailscaleStatus(ctx context.Context) (RemoteTailscaleStatus, error)
	// SetRemoteTailscaleEnabled 在当前运行中的远程服务上启用或停用 Tailscale 入口（桌面专用，不走 RPC）；
	// 配置由界面先行保存，这里只让运行时生效，服务未运行时不做任何事。
	SetRemoteTailscaleEnabled(ctx context.Context, enabled bool) (RemoteTailscaleStatus, error)
	// ResetRemoteTailscale 停止 Tailscale 入口并删除其状态目录，下次启用需要重新登录 tailnet（桌面专用，不走 RPC）。
	ResetRemoteTailscale(ctx context.Context) error
	// CustomEndpoints 获取所有自定义 OpenAI 兼容端点列表（桌面专用，不走 RPC）。
	CustomEndpoints(ctx context.Context) ([]CustomEndpoint, error)
	// SaveCustomEndpoint 保存或更新自定义端点（桌面专用，不走 RPC）；ep.ID 为空表示新建；
	// apiKey 为空表示保留已保存的 Key；webKey 为空表示保留已保存的 web 管理密钥，非空表示覆盖；
	// ep.PersonaWebURL 为空串时删除已保存的 web 管理密钥。
	SaveCustomEndpoint(ctx context.Context, ep CustomEndpoint, apiKey, webKey string) (CustomEndpoint, error)
	// DeleteCustomEndpoint 删除指定自定义端点（桌面专用，不走 RPC）。
	DeleteCustomEndpoint(ctx context.Context, id string) error
	// TestCustomEndpoint 测试自定义端点连通性（桌面专用，不走 RPC）；apiKey 为空时后端使用已保存的 Key。
	TestCustomEndpoint(ctx context.Context, ep CustomEndpoint, apiKey string) (CustomEndpointTestResult, error)
	// TestMagpiePersonas 测试 Magpie 端点的人设来源（桌面专用，不走 RPC）；ep 可以是还没保存的表单值，
	// webKey 为空且 ep.ID 对应已保存端点时回落读密钥库里已保存的 web 管理密钥。
	TestMagpiePersonas(ctx context.Context, ep CustomEndpoint, webKey string) (PersonaSourceTestResult, error)
	// MagpieQuotaProviders 读取已保存的 Magpie 端点卡片上出现的供应商 id（小写、去重、排序），用于设置页勾选菜单栏要展示的额度；端点不存在、不是 Magpie 或没有 key 时返回错误
	MagpieQuotaProviders(ctx context.Context, endpointID string) ([]string, error)
	// ProviderSettings 获取设置页的 Provider 配置列表（桌面专用，不走 RPC）。
	ProviderSettings(ctx context.Context) ([]ProviderSetting, error)
	// SaveProviderSetting 保存单个 Provider 的配置（桌面专用，不走 RPC）；apiKey 为空表示保留已保存的 Key。
	SaveProviderSetting(ctx context.Context, s ProviderSetting, apiKey string) (ProviderSetting, error)
	// MoveToWorktree 将指定会话迁移至独立的 Git worktree 分支，支持是否迁移未提交文件。
	MoveToWorktree(ctx context.Context, threadID, branch string, transferFiles bool) error
	// BulkMarkRead 批量将指定会话标记为已读。
	BulkMarkRead(ctx context.Context, projectID string, threadIDs []string) error
	// ArchiveThreadWithUndo 归档指定会话，返回 10 秒内可通过 UndoBulkOperation 撤销的令牌。
	ArchiveThreadWithUndo(threadID string) (token string, err error)
	// MarkDormantWithUndo 休眠指定会话，返回 10 秒内可通过 UndoBulkOperation 撤销的令牌。
	MarkDormantWithUndo(threadID string) (token string, err error)
	// RetainThread 将临时会话转为正式会话。
	RetainThread(threadID string) error
	// ReleaseProvisionalThread 释放临时会话，调度延迟自动清理。
	ReleaseProvisionalThread(threadID string) error
	// BulkArchive 批量归档指定会话，返回用于撤销操作的 token。
	BulkArchive(ctx context.Context, projectID string, threadIDs []string) (string, error)
	// UndoBulkOperation 根据 token 撤销上一次的批量操作。
	UndoBulkOperation(ctx context.Context, projectID, token string) error
	// RecallTurn 收回指定会话最近一轮消息，返回收回的提示词文本。
	RecallTurn(ctx context.Context, threadID string) (string, error)
	// PreviewRevert 预览撤销指定会话中某一轮代码变更的效果。
	PreviewRevert(ctx context.Context, threadID, turnID string) (*RevertPreview, error)
	// ExecuteRevert 执行撤销指定会话中某一轮代码变更。
	ExecuteRevert(ctx context.Context, threadID, turnID string) error
	// ScanWorktrees 扫描并返回所有 Git worktree 列表。
	ScanWorktrees(ctx context.Context) ([]WorktreeInfo, error)
	// CleanSafeWorktrees 清理已合并且安全的空闲 Git worktree，返回清理的数量。
	CleanSafeWorktrees(ctx context.Context) (int, error)
	// TriggerAutoMerge 触发对指定 lead 会话下所有就绪 bee 的自动合并。
	TriggerAutoMerge(ctx context.Context, leadThreadID string) error
	// UpdateModelPreference 更新指定固定模型的偏好配置。
	UpdateModelPreference(ctx context.Context, pin ModelPin, pref ModelPreference) error
	// ReorderModelList 重新排序固定模型列表。
	ReorderModelList(ctx context.Context, pins []ModelPin) error
	// ModelList 获取当前固定模型列表及上限容量。
	ModelList(ctx context.Context) ([]ModelPin, int, error)
	// AddModelToList 向固定模型列表追加模型。
	AddModelToList(ctx context.Context, pin ModelPin) (bool, error)
	// RemoveModelFromList 从固定模型列表移除模型。
	RemoveModelFromList(ctx context.Context, pin ModelPin) (bool, error)
	// ModelPreferences 获取指定固定模型的偏好设置。
	ModelPreferences(ctx context.Context, pin ModelPin) (ModelPreference, error)
	// SwarmPairs 获取所有 Swarm 主导与执行模型配对列表。
	SwarmPairs(ctx context.Context) ([]SwarmPair, error)
	// AddSwarmPair 添加一组 Swarm 主导与执行模型配对。
	AddSwarmPair(ctx context.Context, p SwarmPair) (SwarmPair, error)
	// UpdateSwarmPair 更新一组已有的 Swarm 主导与执行模型配对。
	UpdateSwarmPair(ctx context.Context, p SwarmPair) error
	// RemoveSwarmPair 根据 ID 移除一组 Swarm 配对。
	RemoveSwarmPair(ctx context.Context, id string) error
	// ProviderVersion 获取指定提供方的当前安装版本与最新可用版本信息。
	ProviderVersion(ctx context.Context, provider string) (ProviderVersionInfo, error)
	// CheckProviderUpdate 检查指定提供方的版本更新。
	CheckProviderUpdate(ctx context.Context, provider string) (ProviderVersionInfo, error)
	// ProviderAccount 获取指定提供方的当前登录账户及套餐信息。
	ProviderAccount(ctx context.Context, provider string) (ProviderAccountInfo, error)
	// ProviderQuota 获取指定提供方的额度使用窗口列表。
	ProviderQuota(ctx context.Context, provider string) ([]ProviderQuotaWindow, error)
	// RefreshProviderQuota 让上游网关（目前是 Magpie）重新向厂商读取额度，直连提供方按空操作处理。
	RefreshProviderQuota(ctx context.Context, provider string) error
	// RefreshThreadQuota 只重读指定会话实际应答账号的额度，查不到账号或直连提供方时按空操作处理。
	RefreshThreadQuota(ctx context.Context, threadID, provider string) error
	// PiMagpieEndpoints 列出启用的 Magpie 端点及可注入 pi 的模型。
	PiMagpieEndpoints(ctx context.Context) ([]PiMagpieEndpoint, error)
	// MagpiePersonas 返回当前 provider/model 对应 magpie 网关的人设列表，非 magpie 或读不到时返回空。
	MagpiePersonas(ctx context.Context, provider, model string) ([]MagpiePersona, error)
	// SetPiMagpieModels 保存指定端点勾选的模型。
	SetPiMagpieModels(endpointID string, modelIDs []string) error
	// AgentMagpie 读取指定 agent 的 magpie 设置：模式、端点模型与全局配置状态。
	AgentMagpie(ctx context.Context, agent string) (AgentMagpieSetting, error)
	// SetAgentMagpieModels 保存指定端点勾选注入该 agent 的模型。
	SetAgentMagpieModels(ctx context.Context, agent, endpointID string, modelIDs []string) error
	// SetAgentMagpieDefaultModel 保存该 agent 的默认模型；两个参数都为空表示恢复自动。
	SetAgentMagpieDefaultModel(ctx context.Context, agent, endpointID, modelID string) error
	// SetAgentMagpieMode 切换该 agent 的 magpie 使用方式，mode 取 inject 或 global。
	SetAgentMagpieMode(ctx context.Context, agent, mode string) error
	// AgentMagpieGlobalAction 对全局配置执行 reapply 或 keep。
	AgentMagpieGlobalAction(ctx context.Context, agent, action string) error
	// ProviderCredits 获取按量计费提供方的余额；提供方不支持时返回 ErrNotImplemented。
	ProviderCredits(ctx context.Context, provider string) (ProviderCredits, error)
}
