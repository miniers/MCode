package wire

import "encoding/json"

// StewardSilentMarker 是总管无事需要告知用户时的最终回复标记。
const StewardSilentMarker = "[[静默]]"

// --- 会话与设备配对 ---

// SessionPairParams 对应 session.pair 请求参数
type SessionPairParams struct {
	Token      string  `json:"token"`
	DeviceName *string `json:"deviceName,omitempty"`
}

// SessionPairResult 对应 session.pair 成功结果
type SessionPairResult struct {
	SessionToken string `json:"sessionToken"`
	DeviceID     string `json:"deviceID"`
	Label        string `json:"label"`
}

// SessionAuthenticateParams 对应 session.authenticate 请求参数
type SessionAuthenticateParams struct {
	Token string `json:"token"`
}

// SessionAuthenticateResult 对应 session.authenticate 成功结果
type SessionAuthenticateResult struct {
	Authenticated bool   `json:"authenticated"`
	DeviceID      string `json:"deviceID"`
	Label         string `json:"label"`
}

// SessionNewPairingCodeParams 对应 session.newPairingCode 请求参数（必须为空对象）
type SessionNewPairingCodeParams struct{}

// SessionNewPairingCodeResult 对应 session.newPairingCode 成功结果
type SessionNewPairingCodeResult struct {
	Requested           bool   `json:"requested"`
	PairingURL          string `json:"pairingURL"`
	LANPairingURL       string `json:"lanPairingURL,omitempty"`
	TailscalePairingURL string `json:"tailscalePairingURL,omitempty"`
	ExpiresAt           string `json:"expiresAt"`
}

// SessionRevokeDeviceParams 对应 session.revokeDevice 请求参数
type SessionRevokeDeviceParams struct {
	DeviceID string `json:"deviceID"`
}

// SessionRevokeDeviceResult 对应 session.revokeDevice 成功结果
type SessionRevokeDeviceResult struct {
	DeviceID string `json:"deviceID"`
	Revoked  bool   `json:"revoked"`
}

// SessionRenameDeviceParams 对应 session.renameDevice 请求参数
type SessionRenameDeviceParams struct {
	Name string `json:"name"`
}

// SessionRenameDeviceResult 对应 session.renameDevice 成功结果
type SessionRenameDeviceResult struct {
	DeviceID string `json:"deviceID"`
	Label    string `json:"label"`
}

// --- 能力声明 ---

// Capabilities 对应 capabilities.get 成功结果
// AppVersion 是宿主应用版本（与桌面端「关于」页同源）；不提供版本的宿主（如 cmd/mcode-server）省略该字段。
type Capabilities struct {
	SchemaVersion   int      `json:"schemaVersion"`
	Methods         []string `json:"methods"`
	Features        []string `json:"features"`
	WebBuildID      string   `json:"webBuildID"`
	ActionJournalID string   `json:"actionJournalID,omitempty"`
	AppVersion      string   `json:"appVersion,omitempty"`
}

// --- 看板与任务 ---

type KanbanProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type KanbanTask struct {
	ID                 string   `json:"id"`
	ProjectID          *string  `json:"projectID"`
	ThreadID           *string  `json:"threadID"`
	ReviewThreadID     *string  `json:"reviewThreadID"`
	GoalID             *string  `json:"goalID"`
	DependsOn          []string `json:"dependsOn"`
	Title              string   `json:"title"`
	Notes              string   `json:"notes"`
	AcceptanceCriteria string   `json:"acceptanceCriteria,omitempty"`
	Status             string   `json:"status"`
	Priority           string   `json:"priority"`
	UpdatedAt          string   `json:"updatedAt"`
	Blocked            bool     `json:"blocked"`
	ReviewSummary      *string  `json:"reviewSummary"`
	ReviewFeedback     *string  `json:"reviewFeedback"`
	ReviewStatus       *string  `json:"reviewStatus"`
	CanStart           bool     `json:"canStart"`
	CanMove            bool     `json:"canMove"`
	CanReview          bool     `json:"canReview"`
	CanApprove         bool     `json:"canApprove"`
	AttentionFlags     []string `json:"attentionFlags"`
	DisplayColumn      string   `json:"displayColumn"`
	LastActivityText   *string  `json:"lastActivityText"`
	AttemptCount       int      `json:"attemptCount"`
	RejectCount        int      `json:"rejectCount,omitempty"`
	LastFailureReason  *string  `json:"lastFailureReason"`
	Revision           int      `json:"revision"`
}

type KanbanGoal struct {
	ID            string         `json:"id"`
	ProjectID     string         `json:"projectID"`
	Title         string         `json:"title"`
	Prompt        string         `json:"prompt"`
	Status        string         `json:"status"`
	Owner         string         `json:"owner,omitempty"`
	Policy        map[string]any `json:"policy"`
	Progress      map[string]any `json:"progress"`
	PlanThreadID  *string        `json:"planThreadID"`
	PlanSummary   *string        `json:"planSummary"`
	StatusReason  *string        `json:"statusReason"`
	ResultSummary *string        `json:"resultSummary"`
	Revision      int            `json:"revision"`
	UpdatedAt     string         `json:"updatedAt"`
}

// KanbanBoard 对应 kanban.getBoard 成功结果
type KanbanBoard struct {
	SchemaVersion int             `json:"schemaVersion"`
	Revision      int             `json:"revision"`
	Projects      []KanbanProject `json:"projects"`
	Tasks         []KanbanTask    `json:"tasks"`
	Goals         []KanbanGoal    `json:"goals"`
}

// KanbanCreateTaskParams 对应 kanban.createTask 请求参数
type KanbanCreateTaskParams struct {
	ProjectID string  `json:"projectID"`
	Title     string  `json:"title"`
	Notes     *string `json:"notes,omitempty"`
	ThreadID  *string `json:"threadID,omitempty"`
	ActionID  string  `json:"actionID"`
}

// KanbanMoveTaskParams 对应 kanban.moveTask 请求参数
type KanbanMoveTaskParams struct {
	TaskID           string `json:"taskID"`
	Status           string `json:"status"`
	ExpectedRevision int    `json:"expectedRevision"`
	ActionID         string `json:"actionID"`
}

// KanbanStartTaskParams 对应 kanban.startTask 请求参数
type KanbanStartTaskParams struct {
	TaskID           string `json:"taskID"`
	ExpectedRevision int    `json:"expectedRevision"`
	ActionID         string `json:"actionID"`
}

// KanbanReviewTaskParams 对应 kanban.reviewTask 请求参数
type KanbanReviewTaskParams struct {
	TaskID           string `json:"taskID"`
	Decision         string `json:"decision"`
	Feedback         string `json:"feedback,omitempty"`
	ExpectedRevision int    `json:"expectedRevision"`
	ActionID         string `json:"actionID"`
}

// KanbanGetDiffParams 对应 kanban.getDiff 请求参数
type KanbanGetDiffParams struct {
	TaskID string `json:"taskID"`
}

// KanbanGetDiffResult 对应 kanban.getDiff 成功结果
type KanbanGetDiffResult struct {
	TaskID    string `json:"taskID"`
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated"`
}

// KanbanTaskMutationResult 对应看板与目标变更操作的结果（TaskID/GoalID/Revision 字段在为 null 时必须保留）
type KanbanTaskMutationResult struct {
	ActionID string  `json:"actionID"`
	TaskID   *string `json:"taskID"`
	GoalID   *string `json:"goalID"`
	Revision *int    `json:"revision"`
}

// GoalCreateParams 对应 goals.create 请求参数
type GoalCreateParams struct {
	ProjectID string `json:"projectID"`
	Prompt    string `json:"prompt"`
	ActionID  string `json:"actionID"`
}

// GoalMutationParams 对应 goals.start/goals.pause/goals.cancel 请求参数
type GoalMutationParams struct {
	GoalID           string `json:"goalID"`
	ExpectedRevision int    `json:"expectedRevision"`
	ActionID         string `json:"actionID"`
}

// GoalTaskMutationParams 对应 goals.retryTask/goals.skipTask 请求参数
type GoalTaskMutationParams struct {
	TaskID           string `json:"taskID"`
	ExpectedRevision int    `json:"expectedRevision"`
	ActionID         string `json:"actionID"`
}

// GoalReplanParams 对应 goals.replan 请求参数
type GoalReplanParams struct {
	GoalID           string `json:"goalID"`
	Feedback         string `json:"feedback"`
	ExpectedRevision int    `json:"expectedRevision"`
	ActionID         string `json:"actionID"`
}

// --- 项目列表 ---

type ProjectSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	IsExpanded  bool   `json:"isExpanded"`
	ThreadCount int    `json:"threadCount"`
}

// ProjectList 对应 projects.list 成功结果
type ProjectList struct {
	Projects []ProjectSummary `json:"projects"`
	Revision int              `json:"revision"`
}

// --- 会话列表与摘要 ---

type ThreadListParams struct {
	ProjectID       *string `json:"projectID,omitempty"`
	IncludeArchived *bool   `json:"includeArchived,omitempty"`
	Cursor          *string `json:"cursor,omitempty"`
	Limit           *int    `json:"limit,omitempty"`
}

type ThreadSummary struct {
	ID               string  `json:"id"`
	ProjectID        string  `json:"projectID"`
	Title            string  `json:"title"`
	Provider         string  `json:"provider"`
	Model            *string `json:"model"`
	Effort           *string `json:"effort"`
	IsRunning        bool    `json:"isRunning"`
	IsArchived       bool    `json:"isArchived"`
	IsPinned         bool    `json:"isPinned"`
	IsDormant        bool    `json:"isDormant"`
	HasUnread        bool    `json:"hasUnread"`
	Attention        string  `json:"attention"`
	LastActivityText *string `json:"lastActivityText"`
	// LastErrorText 是最近一轮因错误结束时的错误原文（截断到 500 字），
	// 新一轮开始或正常结束时清空；额度提醒靠它解析恢复时间。
	LastErrorText     string  `json:"lastErrorText,omitempty"`
	Section           string  `json:"section"`
	OrderKey          string  `json:"orderKey"`
	UpdatedAt         string  `json:"updatedAt"`
	LastUserMessageAt *string `json:"lastUserMessageAt,omitempty"`
	LastStatus        *string `json:"lastStatus"`
	HasPendingRequest bool    `json:"hasPendingRequest"`
	TaskID            *string `json:"taskID"`
	GoalID            *string `json:"goalID"`
	RuntimeMode       string  `json:"runtimeMode"`
	InteractionMode   string  `json:"interactionMode"`
	// Persona 是线程级人设 id：缺失或空串表示从未设置，"none" 表示用户显式选择了无人设。
	Persona string `json:"persona,omitempty"`
	// ContextUsedTokens 是最近一轮实际带入上下文的 token 数：优先取 CLI 上报的 ContextTokens，
	// 缺失时回退到「输入 + 输出」，与桌面端上下文用量圆环的口径一致。
	ContextUsedTokens int64 `json:"contextUsedTokens,omitempty"`
	// ContextWindow 是当前模型的上下文窗口上限（tokens）：优先用 CLI 上报值，缺失时回退模型目录。
	ContextWindow int64 `json:"contextWindow,omitempty"`
	// LastTurnCostUSD 是最近一轮的花费（美元）。CLI 上报的 total_cost_usd 可能已是整次会话
	// 的累计值，所以不能逐轮相加，口径与桌面端用量面板一致。用量不含费用时缺省不输出。
	LastTurnCostUSD   float64 `json:"lastTurnCostUSD,omitempty"`
	FastMode          bool    `json:"fastMode"`
	SpeedTier         *string `json:"speedTier,omitempty"`
	WorktreeRequested bool    `json:"worktreeRequested"`
	SwarmEnabled      bool    `json:"swarmEnabled"`
	SwarmPairID       *string `json:"swarmPairID"`
	SwarmBeeTiers     string  `json:"swarmBeeTiers,omitempty"`
	SwarmMode         string  `json:"swarmMode,omitempty"` // 会话级 Swarm 工作模式：inherited/collaborative/leadPlansOnly/alwaysBees
	SwarmBeesProvider *string `json:"swarmBeesProvider"`
	SwarmBeesModel    *string `json:"swarmBeesModel"`
	SwarmBeesEffort   *string `json:"swarmBeesEffort"`
	IsProvisional     bool    `json:"isProvisional,omitempty"`
	ParentThreadID    *string `json:"parentThreadID,omitempty"`
	IsHelper          bool    `json:"isHelper,omitempty"`
	// IsSteward 是否为总管主会话；由宿主注入的判定计算，不落盘。
	IsSteward bool `json:"isSteward,omitempty"`
	// LastTurnSilent 最近一轮正常结束且最终回复恰为静默标记，宿主据此跳过完成提醒，不落盘。
	LastTurnSilent bool `json:"lastTurnSilent,omitempty"`
	// ForkedFromThreadID / ForkedFromMessageID 记录会话由哪个会话的哪条消息分叉（fork）而来，
	// 约定：ForkedFromThreadID 非空且 ForkedFromMessageID 为空，就表示转发会话；两个都非空表示分支会话；
	// 两个都为空表示不是分叉会话；源会话删除后仍保留。
	ForkedFromThreadID  *string `json:"forkedFromThreadID,omitempty"`
	ForkedFromMessageID *string `json:"forkedFromMessageID,omitempty"`
	// SwarmBusy 表示该 Swarm 主线程名下仍有未结束的子线程（运行中、排队中或正在派发），
	// 或者仍有待投递给它的子线程回报；子线程恒为 false。
	// 只有 IsRunning 与 SwarmBusy 同时为 false，才算这一轮真正结束。
	SwarmBusy bool `json:"swarmBusy,omitempty"`
	// BackgroundTasks 是该会话仍需等待的后台任务数，供侧边栏在回合结束后继续标出「后台」状态：
	// ToolStatus 为 background 的工具（provider 托管的后台任务）不论会话是否运行都计入；
	// 会话未运行时，ToolStatus 仍为 running 的工具（回合结束后仍在跑的命令）也计入。
	// 子智能体不计入，Swarm 名下仍有子线程时由 SwarmBusy 单独表达。
	BackgroundTasks int `json:"backgroundTasks,omitempty"`
	// MCPDisabled 是会话级外部 MCP 禁用名单（元素为 agent:scope:name 键）；空表示沿用全局设置。
	MCPDisabled []string `json:"mcpDisabled,omitempty"`
}

// ThreadList 对应 threads.list 成功结果
type ThreadList struct {
	Threads          []ThreadSummary `json:"threads"`
	Revision         int             `json:"revision"`
	Truncated        bool            `json:"truncated"`
	TruncationReason *string         `json:"truncationReason"`
	NextCursor       *string         `json:"nextCursor"`
	HasMore          bool            `json:"hasMore"`
}

// GetThreadParams 对应 threads.get 请求参数
type GetThreadParams struct {
	ThreadID string `json:"threadID"`
}

// ThreadsGetResult 对应 threads.get 成功结果
type ThreadsGetResult struct {
	Thread ThreadSummary `json:"thread"`
}

// ThreadCreateParams 对应 threads.create 请求参数
type ThreadCreateParams struct {
	ProjectID       string           `json:"projectID"`
	ActionID        string           `json:"actionID"`
	Options         *ComposerOptions `json:"options,omitempty"`
	ActionJournalID *string          `json:"actionJournalID,omitempty"`
	RuntimeMode     *string          `json:"runtimeMode,omitempty"`
	Model           *string          `json:"model,omitempty"`
	Provisional     *bool            `json:"provisional,omitempty"`
	// MCPDisabled 是新建会话的外部 MCP 禁用名单；nil 表示套用全局默认，空数组表示全部启用。
	MCPDisabled []string `json:"mcpDisabled,omitempty"`
}

// ThreadCreateResult 对应 threads.create 成功结果
type ThreadCreateResult struct {
	ActionID string `json:"actionID"`
	ThreadID string `json:"threadID"`
}

// ThreadUpdateParams 对应 threads.update 请求参数
type ThreadUpdateParams struct {
	ThreadID    string  `json:"threadID"`
	Pinned      *bool   `json:"pinned,omitempty"`
	Dormant     *bool   `json:"dormant,omitempty"`
	Archived    *bool   `json:"archived,omitempty"`
	Read        *bool   `json:"read,omitempty"`
	Title       *string `json:"title,omitempty"`
	RuntimeMode *string `json:"runtimeMode,omitempty"`
	Provider    *string `json:"provider,omitempty"`
	Model       *string `json:"model,omitempty"`
}

// ThreadUpdateResult 对应 threads.update 成功结果
type ThreadUpdateResult struct {
	Thread    ThreadSummary `json:"thread"`
	UndoToken *string       `json:"undoToken,omitempty"`
}

type ThreadsUpdateParams = ThreadUpdateParams
type ThreadsUpdateResult = ThreadUpdateResult

// ThreadRetainParams 对应 threads.retain 请求参数
type ThreadRetainParams struct {
	ThreadID string `json:"threadId"`
}

// ThreadProvisionalReleaseParams 对应 threads.provisional.release 请求参数
type ThreadProvisionalReleaseParams struct {
	ThreadID string `json:"threadId"`
}

// --- 会话历史 ---

type HistoryParams struct {
	ThreadID string  `json:"threadID"`
	Cursor   *string `json:"cursor,omitempty"`
	Limit    *int    `json:"limit,omitempty"`
}

type MediaRef struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Name      string `json:"name"`
	MimeType  string `json:"mimeType"`
	ByteCount int    `json:"byteCount"`
	Width     *int   `json:"width,omitempty"`
	Height    *int   `json:"height,omitempty"`
}

type ThreadMessage struct {
	ID              string  `json:"id"`
	Role            string  `json:"role"`
	Text            string  `json:"text"`
	CreatedAt       string  `json:"createdAt"`
	TurnIndex       int     `json:"turnIndex"`
	TurnID          *string `json:"turnID,omitempty"`
	Kind            *string `json:"kind,omitempty"`
	TurnStatus      *string `json:"turnStatus,omitempty"`
	TurnStartedAt   *string `json:"turnStartedAt,omitempty"`
	TurnCompletedAt *string `json:"turnCompletedAt,omitempty"`
	IsFinalResponse *bool   `json:"isFinalResponse,omitempty"`
	Seq             *int    `json:"seq,omitempty"`
	// 结构化工具调用名称（仅当 role 为 tool 时出现）
	ToolName *string `json:"toolName,omitempty"`
	// 结构化工具调用完整输入（按字符截断，仅当 role 为 tool 时出现）
	ToolInput *string `json:"toolInput,omitempty"`
	// 结构化工具执行状态：running、completed、failed、blocked、background（仅当 role 为 tool 时出现）。
	// background 表示 provider 托管的后台任务仍在运行（CLI 已返回 tool_result，命令还在后台），
	// 任务结束时才落成 completed / failed。
	ToolStatus *string `json:"toolStatus,omitempty"`
	// 结构化工具完成时间（RFC3339 格式，与 CreatedAt 相同，仅当 role 为 tool 时出现）
	ToolCompletedAt *string `json:"toolCompletedAt,omitempty"`
	// 失败类别（仅当 kind 为 error 时出现，取值同 harness.ErrKind*）；为空表示未分类，
	// 界面回退到关键词识别。
	ErrKind *string    `json:"errKind,omitempty"`
	Media   []MediaRef `json:"media,omitempty"`
}

// HistoryPage 对应 threads.history 成功结果
type HistoryPage struct {
	ThreadID   string          `json:"threadID"`
	Messages   []ThreadMessage `json:"messages"`
	NextCursor *string         `json:"nextCursor"`
	HasMore    bool            `json:"hasMore"`
	Truncated  bool            `json:"truncated"`
}

// --- 会话订阅与退订 ---

type SubscribeParams struct {
	ThreadID string `json:"threadID"`
}

type ThreadStreamSnapshot struct {
	ThreadID string        `json:"threadID"`
	History  HistoryPage   `json:"history"`
	Thread   ThreadSummary `json:"thread"`
	RunID    string        `json:"runID"`
	Sequence int           `json:"sequence"`
}

// SubscribeResult 对应 threads.subscribe 成功结果
type SubscribeResult struct {
	ThreadID   string                `json:"threadID"`
	Subscribed bool                  `json:"subscribed"`
	Snapshot   *ThreadStreamSnapshot `json:"snapshot,omitempty"`
}

type UnsubscribeParams struct {
	ThreadID string `json:"threadID"`
}

// UnsubscribeResult 对应 threads.unsubscribe 成功结果
type UnsubscribeResult struct {
	ThreadID   string `json:"threadID"`
	Subscribed bool   `json:"subscribed"`
}

// --- 消息发送 ---

type ComposerOptions struct {
	Provider          *string `json:"provider,omitempty"`
	Model             *string `json:"model,omitempty"`
	Effort            *string `json:"effort,omitempty"`
	RuntimeMode       *string `json:"runtimeMode,omitempty"`
	InteractionMode   *string `json:"interactionMode,omitempty"`
	WorkspaceMode     *string `json:"workspaceMode,omitempty"`
	FastMode          *bool   `json:"fastMode,omitempty"`
	SwarmEnabled      *bool   `json:"swarmEnabled,omitempty"`
	SwarmPairID       *string `json:"swarmPairID,omitempty"`
	SwarmBeesProvider *string `json:"swarmBeesProvider,omitempty"`
	SwarmBeesModel    *string `json:"swarmBeesModel,omitempty"`
	SwarmBeesEffort   *string `json:"swarmBeesEffort,omitempty"`
}

type SendMessageParams struct {
	ThreadID        string           `json:"threadID"`
	Text            string           `json:"text,omitempty"`
	ActionID        string           `json:"actionID"`
	WhenRunning     string           `json:"whenRunning,omitempty"`
	Options         *ComposerOptions `json:"options,omitempty"`
	Attachments     []string         `json:"attachments,omitempty"`
	ActionJournalID string           `json:"actionJournalID,omitempty"`
}

// SendMessageResult 对应 threads.sendMessage 成功结果
type SendMessageResult struct {
	ActionID          string  `json:"actionID"`
	ThreadID          string  `json:"threadID"`
	MessageID         *string `json:"messageID,omitempty"`
	TurnID            *string `json:"turnID,omitempty"`
	Queued            *bool   `json:"queued,omitempty"`
	FollowUpID        *string `json:"followUpID,omitempty"`
	LocalCommand      *string `json:"localCommand,omitempty"`
	DelegatedThreadID *string `json:"delegatedThreadID,omitempty"`
}

// --- 轮次中断 ---

// InterruptParams 对应 threads.interrupt 请求参数
type InterruptParams struct {
	ThreadID string `json:"threadID"`
	TurnID   string `json:"turnID"`
}

// InterruptResult 对应 threads.interrupt 成功结果
type InterruptResult struct {
	ThreadID string `json:"threadID"`
	TurnID   string `json:"turnID"`
	State    string `json:"state"`
}

// --- 分割会话 ---

// SplitSessionParams 对应 threads.splitSession 请求参数。
type SplitSessionParams struct {
	ThreadID string `json:"threadID"`
}

// SplitSessionResult 对应 threads.splitSession 成功结果：Pending 为真表示已排队，
// 从下一条消息起生效。
type SplitSessionResult struct {
	ThreadID string `json:"threadID"`
	Pending  bool   `json:"pending"`
}

const (
	InterruptStateStopping       = "stopping"
	InterruptStateAlreadyStopped = "already_stopped"
)

// --- 终止会话中正在运行的命令 ---

// KillRunningCommandParams 对应 threads.killRunningCommand 请求参数。
// MessageID 用来定位该次工具调用的开始时间，为空时退回到本轮开始时间。
type KillRunningCommandParams struct {
	ThreadID  string `json:"threadID"`
	MessageID string `json:"messageID,omitempty"`
}

// KillRunningCommandResult 对应 threads.killRunningCommand 成功结果。
type KillRunningCommandResult struct {
	Killed int `json:"killed"`
}

// --- 服务端通知载荷 ---

type ThreadEventPayload struct {
	Messages          []ThreadMessage `json:"messages,omitempty"`
	RemovedMessageIDs []string        `json:"removedMessageIDs,omitempty"`
	Thread            ThreadSummary   `json:"thread"`
}

// ThreadEvent 对应 threads.event 通知载荷
type ThreadEvent struct {
	ThreadID string             `json:"threadID"`
	RunID    string             `json:"runID"`
	Sequence int                `json:"sequence"`
	Kind     string             `json:"kind"`
	Payload  ThreadEventPayload `json:"payload"`
}

func (e ThreadEvent) MarshalJSON() ([]byte, error) {
	type event ThreadEvent
	messages := e.Payload.Messages
	if messages == nil {
		messages = []ThreadMessage{}
	}
	removed := e.Payload.RemovedMessageIDs
	if removed == nil {
		removed = []string{}
	}
	var payload any = struct {
		Messages          []ThreadMessage `json:"messages"`
		RemovedMessageIDs []string        `json:"removedMessageIDs"`
		Thread            ThreadSummary   `json:"thread"`
	}{messages, removed, e.Payload.Thread}
	if e.Kind == "resync" {
		payload = struct {
			Thread ThreadSummary `json:"thread"`
		}{e.Payload.Thread}
	}
	return json.Marshal(struct {
		event
		Payload any `json:"payload"`
	}{
		event: event(e), Payload: payload,
	})
}

// ThreadsChanged 对应 threads.changed 通知载荷
type ThreadsChanged struct {
	Revision         int             `json:"revision"`
	Threads          []ThreadSummary `json:"threads"`
	RemovedThreadIDs []string        `json:"removedThreadIDs"`
}

func (c ThreadsChanged) MarshalJSON() ([]byte, error) {
	type changed ThreadsChanged
	if c.Threads == nil {
		c.Threads = []ThreadSummary{}
	}
	if c.RemovedThreadIDs == nil {
		c.RemovedThreadIDs = []string{}
	}
	return json.Marshal(changed(c))
}

// --- 交互请求与工具审批 ---

// RemoteRequestOption 对应 RemoteRequest 的操作选项
type RemoteRequestOption struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Role  string `json:"role"`
}

// RemoteQuestionChoice 对应问题选项
type RemoteQuestionChoice struct {
	Label  string  `json:"label"`
	Detail *string `json:"detail"`
}

// RemoteQuestion 对应提问请求中的具体问题
type RemoteQuestion struct {
	ID             string                 `json:"id"`
	Header         string                 `json:"header"`
	Prompt         string                 `json:"prompt"`
	Choices        []RemoteQuestionChoice `json:"choices"`
	AllowsMultiple bool                   `json:"allowsMultiple"`
	AllowsOther    bool                   `json:"allowsOther"`
	IsSecret       bool                   `json:"isSecret"`
}

// RemoteRequest 对应 requests.list 与 requests.changed 中的请求定义
type RemoteRequest struct {
	ThreadID      string                `json:"threadID"`
	TurnID        *string               `json:"turnID"`
	RunID         string                `json:"runID"`
	RequestID     string                `json:"requestID"`
	Kind          string                `json:"kind"`
	Title         *string               `json:"title"`
	Detail        *string               `json:"detail"`
	Reason        *string               `json:"reason"`
	DeliveryError *string               `json:"deliveryError,omitempty"`
	Options       []RemoteRequestOption `json:"options"`
	Questions     []RemoteQuestion      `json:"questions"`
	Diff          *string               `json:"diff,omitempty"`
}

// RequestsListParams 对应 requests.list 请求参数
type RequestsListParams struct {
	ThreadID string `json:"threadID"`
}

// RequestsListResult 对应 requests.list 成功结果
type RequestsListResult struct {
	ThreadID string          `json:"threadID"`
	Requests []RemoteRequest `json:"requests"`
}

// RequestsRespondParams 对应 requests.respond 请求参数
type RequestsRespondParams struct {
	ThreadID  string          `json:"threadID"`
	TurnID    *string         `json:"turnID,omitempty"`
	RunID     string          `json:"runID"`
	RequestID string          `json:"requestID"`
	OptionID  *string         `json:"optionID,omitempty"`
	Answers   json.RawMessage `json:"answers,omitempty"`
	Skip      *bool           `json:"skip,omitempty"`
}

// RequestsRespondResult 对应 requests.respond 成功结果
type RequestsRespondResult struct {
	ThreadID  string `json:"threadID"`
	RequestID string `json:"requestID"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
}

const RequestStatusAccepted = "accepted"

// RequestsChangedParams 对应 requests.changed 通知参数
type RequestsChangedParams struct {
	ThreadID string          `json:"threadID"`
	Requests []RemoteRequest `json:"requests"`
}

// --- 幂等动作状态 ---

// ActionStatusParams 对应 threads.actionStatus 请求参数
type ActionStatusParams struct {
	ActionID string `json:"actionID"`
}

// ActionError 对应 RemoteActionError
type ActionError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ActionStatusResult 对应 threads.actionStatus 成功结果
type ActionStatusResult struct {
	ActionID   string       `json:"actionID"`
	State      string       `json:"state"`
	ThreadID   *string      `json:"threadID,omitempty"`
	MessageID  *string      `json:"messageID,omitempty"`
	TurnID     *string      `json:"turnID,omitempty"`
	FollowUpID *string      `json:"followUpID,omitempty"`
	TaskID     *string      `json:"taskID,omitempty"`
	GoalID     *string      `json:"goalID,omitempty"`
	Revision   *int         `json:"revision,omitempty"`
	Error      *ActionError `json:"error,omitempty"`
}

const (
	ActionStatePending  = "pending"
	ActionStateAccepted = "accepted"
	ActionStateFailed   = "failed"
	ActionStateUnknown  = "unknown"
)

// --- 模型供应商列表 ---

// ProvidersListParams 对应 providers.list 请求参数
type ProvidersListParams struct {
	ProjectID *string `json:"projectID,omitempty"`
}

// ProvidersRememberSelectionParams 对应 providers.rememberSelection 请求参数。
// 字段全部可选，json 名与 ComposerOptions 一致：只需传用户真正改过的字段。
// provider 与 model 都非空时写主导记忆（effort 为空表示默认档位，fastMode 为 nil 视为 false）；
// 子代理三字段任一非 nil 则整组写入（空串表示跟随主导）；swarmEnabled 非 nil 时写编排开关。
type ProvidersRememberSelectionParams struct {
	ProjectID         *string `json:"projectID,omitempty"`
	Provider          *string `json:"provider,omitempty"`
	Model             *string `json:"model,omitempty"`
	Effort            *string `json:"effort,omitempty"`
	FastMode          *bool   `json:"fastMode,omitempty"`
	SwarmEnabled      *bool   `json:"swarmEnabled,omitempty"`
	SwarmBeesProvider *string `json:"swarmBeesProvider,omitempty"`
	SwarmBeesModel    *string `json:"swarmBeesModel,omitempty"`
	SwarmBeesEffort   *string `json:"swarmBeesEffort,omitempty"`
}

// ProviderModel 对应 RemoteProviderModel
type ProviderModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Detail 是模型说明；CLI 或种子给出时优先于 models.dev 的摘要。
	Detail          *string  `json:"detail"`
	Efforts         []string `json:"efforts"`
	DefaultEffort   *string  `json:"defaultEffort"`
	IsDefault       bool     `json:"isDefault"`
	SupportsFast    bool     `json:"supportsFast"`
	PreferredEffort *string  `json:"preferredEffort"`
	// ContextLimit 是模型上下文窗口上限（tokens），0 表示未知；来自 models.dev。
	ContextLimit int `json:"contextLimit,omitempty"`
	// Secondary 表示模型不属于当前登录渠道，界面默认折叠在分组底部的更多模型里。
	Secondary bool `json:"secondary,omitempty"`
	// AliasOf 表示此行是 CLI 别名，值为它解析到的实体模型 ID，界面据此折叠重复行。
	AliasOf string `json:"aliasOf,omitempty"`
	// Cost 是模型价格，单位美元/百万 token；来自 models.dev，是 API 参考价。
	Cost *ModelCost `json:"cost,omitempty"`
}

// ModelCost 对应 RemoteProviderModel.cost，单位美元/百万 token。
type ModelCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead,omitempty"`
	CacheWrite float64 `json:"cacheWrite,omitempty"`
}

// Provider 对应 RemoteProvider
type Provider struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Auth              string          `json:"auth"`
	Models            []ProviderModel `json:"models"`
	SupportsPlanMode  bool            `json:"supportsPlanMode"`
	SupportsImages    bool            `json:"supportsImages"`
	SupportsFast      bool            `json:"supportsFast"`
	Available         bool            `json:"available"`
	UnavailableReason *string         `json:"unavailableReason"`
	// Enabled 表示该 provider 在桌面设置里是否启用：nil 表示启用，false 表示桌面设置中已关闭
	// （选择器默认隐藏，但仍可用于解析名称和已有会话）。
	Enabled *bool `json:"enabled,omitempty"`
}

// SwarmTierOverride 描述针对特定档位的配对级覆盖配置
type SwarmTierOverride struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	FastMode *bool  `json:"fastMode,omitempty"`
}

// SwarmPair 对应 RemoteSwarmPair
type SwarmPair struct {
	ID             string                       `json:"id"`
	Name           *string                      `json:"name"`
	Provider       string                       `json:"provider"`
	LeadModel      *string                      `json:"leadModel"`
	LeadEffort     *string                      `json:"leadEffort"`
	WorkerProvider *string                      `json:"workerProvider"`
	WorkerModel    *string                      `json:"workerModel"`
	MaxBees        *int                         `json:"maxBees"`
	BeeTiers       map[string]SwarmTierOverride `json:"beeTiers,omitempty"`
}

// SwarmComposerInfo 对应 RemoteSwarmComposerInfo
type SwarmComposerInfo struct {
	Enabled        bool        `json:"enabled"`
	DefaultEnabled bool        `json:"defaultEnabled"`
	Pairs          []SwarmPair `json:"pairs"`
}

// SwarmCredibility 对应 bee 汇报中代码引用的可信度核验统计。
type SwarmCredibility struct {
	Cited      int      `json:"cited"`
	Verified   int      `json:"verified"`
	Unverified []string `json:"unverified,omitempty"`
	NotRead    []string `json:"notRead,omitempty"`
}

// SwarmBeeQuestion 描述 bee 当前待回答的提问，供远程客户端直接显示与转交。
type SwarmBeeQuestion struct {
	RequestID string `json:"requestID"`
	// Prompt 是第一道问题的题面文字
	Prompt        string `json:"prompt"`
	Forwarded     bool   `json:"forwarded"`
	AutoForwardAt string `json:"autoForwardAt,omitempty"`
}

// SwarmBee 对应 RemoteSwarmBee
type SwarmBee struct {
	ThreadID          string  `json:"threadID"`
	Index             int     `json:"index"`
	Name              string  `json:"name"`
	Task              string  `json:"task"`
	Kind              string  `json:"kind"`
	Origin            string  `json:"origin"`
	Status            string  `json:"status"`
	Summary           string  `json:"summary,omitempty"`
	Activity          string  `json:"activity,omitempty"`
	ToolCalls         int     `json:"toolCalls"`
	Tokens            int     `json:"tokens"`
	StartedAt         string  `json:"startedAt"`
	FinishedAt        *string `json:"finishedAt,omitempty"`
	Provider          string  `json:"provider,omitempty"`
	Model             string  `json:"model,omitempty"`
	Tier              string  `json:"tier,omitempty"`
	ReadOnly          bool    `json:"readOnly,omitempty"`
	DirectModel       bool    `json:"directModel,omitempty"`
	ShadowRepo        bool    `json:"shadowRepo,omitempty"`
	SharedNoGit       bool    `json:"sharedNoGit,omitempty"`
	SharedNoGitReason string  `json:"sharedNoGitReason,omitempty"`
	CanOpen           bool    `json:"canOpen"`
	CanStop           bool    `json:"canStop"`
	// MergeState 是合并进度汇总：pending/discarded/merged/failed，空表示未进入合并流程。
	MergeState string `json:"mergeState,omitempty"`
	// ReviewState 是合并前审查的进度：running/rejected/needs_human/approved，空表示未进入审查。
	ReviewState string `json:"reviewState,omitempty"`
	// ReviewReason 是审查模型给出的结论说明，供远程客户端展示。
	ReviewReason string `json:"reviewReason,omitempty"`
	// ReviewThreadID 是审查会话的线程 ID，便于跳转查看审查过程。
	ReviewThreadID string `json:"reviewThreadId,omitempty"`
	// ReviewLayer 是合并审查的决定层：l0/l1/l2/l3/human。
	ReviewLayer string `json:"reviewLayer,omitempty"`
	// ReviewConcerns 是审查结论中的关注点列表，最多 10 条。
	ReviewConcerns []string `json:"reviewConcerns,omitempty"`
	// ReviewTokens 是本次审查消耗的 token 数，0 表示未统计。
	ReviewTokens int `json:"reviewTokens,omitempty"`
	// ReviewTokensEstimated 表示 ReviewTokens 是估算值而非提供方回报的精确值。
	ReviewTokensEstimated bool              `json:"reviewTokensEstimated,omitempty"`
	ConflictFiles         []string          `json:"conflictFiles,omitempty"`
	ChangedFiles          []string          `json:"changedFiles,omitempty"`
	MergeError            string            `json:"mergeError,omitempty"`
	LinesAdded            int               `json:"linesAdded,omitempty"`
	LinesDeleted          int               `json:"linesDeleted,omitempty"`
	Credibility           *SwarmCredibility `json:"credibility,omitempty"`
	PendingQuestion       *SwarmBeeQuestion `json:"pendingQuestion,omitempty"`
}

// SwarmState 对应 RemoteSwarmState
type SwarmState struct {
	ThreadID       string     `json:"threadID"`
	SwarmAvailable bool       `json:"swarmAvailable"`
	SwarmEnabled   bool       `json:"swarmEnabled"`
	IsBee          bool       `json:"isBee"`
	LeadThreadID   string     `json:"leadThreadID,omitempty"`
	Bee            *SwarmBee  `json:"bee,omitempty"`
	Bees           []SwarmBee `json:"bees"`
	// Queued 是派发队列中等待启动的子代理数，尚未出现在 Bees 中
	Queued int `json:"queued,omitempty"`
	// PlansOnly 是当前生效的「只规划」取值，PlansOnlyOverridden 表示它来自会话级覆盖而非全局设置。
	PlansOnly           bool `json:"plansOnly"`
	PlansOnlyOverridden bool `json:"plansOnlyOverridden"`
}

// SwarmGetParams 对应 threads.swarm.get 请求参数
type SwarmGetParams struct {
	ThreadID string `json:"threadID"`
}

// SwarmSetParams 对应 threads.swarm.set 请求参数
type SwarmSetParams struct {
	ThreadID string `json:"threadID"`
	Enabled  bool   `json:"enabled"`
	ActionID string `json:"actionID"`
}

// SwarmSetBeeTiersParams 对应 threads.swarm.setBeeTiers 请求参数
type SwarmSetBeeTiersParams struct {
	ThreadID string  `json:"threadID"`
	BeeTiers string  `json:"beeTiers"`
	ActionID *string `json:"actionID,omitempty"`
}

// SwarmStopBeeParams 对应 threads.swarm.stopBee 请求参数
type SwarmStopBeeParams struct {
	ThreadID    string `json:"threadID"`
	BeeThreadID string `json:"beeThreadID"`
	ActionID    string `json:"actionID"`
}

// SwarmBatchParams 对应 threads.swarm.stopAll 与 threads.hydra.clearFinished 请求参数
type SwarmBatchParams struct {
	ThreadID string `json:"threadID"`
	ActionID string `json:"actionID"`
}

// SwarmSetPlansOnlyParams 对应 threads.swarm.setPlansOnly 请求参数
// plansOnly 为 null 表示清除会话级覆盖、回到全局设置。
type SwarmSetPlansOnlyParams struct {
	ThreadID  string `json:"threadID"`
	PlansOnly *bool  `json:"plansOnly"`
	ActionID  string `json:"actionID"`
}

// SwarmSetModeParams 对应 threads.swarm.setMode 请求参数
type SwarmSetModeParams struct {
	ThreadID string `json:"threadID"`
	Mode     string `json:"mode"`
	ActionID string `json:"actionID"`
}

// SwarmForwardQuestionParams 对应 threads.swarm.forwardQuestion 请求参数
type SwarmForwardQuestionParams struct {
	ThreadID    string `json:"threadID"`
	BeeThreadID string `json:"beeThreadID"`
	RequestID   string `json:"requestID"`
	ActionID    string `json:"actionID"`
}

// SwarmMergeDiffParams 对应 threads.swarm.mergeDiff 请求参数
type SwarmMergeDiffParams struct {
	ThreadID    string `json:"threadID"`
	BeeThreadID string `json:"beeThreadID"`
}

// SwarmMergeDiffResult 对应 threads.swarm.mergeDiff 响应结果
type SwarmMergeDiffResult struct {
	Diff         string   `json:"diff"`
	Truncated    bool     `json:"truncated"`
	FilesChanged []string `json:"filesChanged"`
}

// SwarmMergeActionParams 对应 threads.swarm.approveMerge 与 threads.hydra.discardMerge 请求参数
type SwarmMergeActionParams struct {
	ThreadID    string `json:"threadID"`
	BeeThreadID string `json:"beeThreadID"`
	ActionID    string `json:"actionID"`
}

// SwarmTier 对应 config.SwarmTier，描述单个 Swarm Bee 档位定义。
// Provider 与 Model 为空表示跟随会话当前生效的 Bee 模型。
type SwarmTier struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ReadOnly    bool   `json:"readOnly"`
	// WritePaths 是该档位的写入白名单 glob 列表；空表示不限制改动范围。
	WritePaths   []string `json:"writePaths,omitempty"`
	Provider     string   `json:"provider,omitempty"`
	Model        string   `json:"model,omitempty"`
	Effort       string   `json:"effort,omitempty"`
	FastMode     bool     `json:"fastMode,omitempty"`
	SystemPrompt string   `json:"systemPrompt,omitempty"`
}

// SwarmTiersResult 对应 swarm.tiers.get 与 swarm.tiers.set 响应结果
type SwarmTiersResult struct {
	Tiers []SwarmTier `json:"tiers"`
}

// SwarmTiersSetParams 对应 swarm.tiers.set 请求参数，整表替换档位配置。
type SwarmTiersSetParams struct {
	Tiers    []SwarmTier `json:"tiers"`
	ActionID string      `json:"actionID"`
}

// ModelCatalogEntry 对应 RemoteModelCatalogEntry
type ModelCatalogEntry struct {
	Provider string `json:"provider"`
	ModelID  string `json:"modelID"`
}

// ComposerDraftDefaults 对应 RemoteComposerDraftDefaults
// 全部字段均为 schema 必填项，因此不使用 omitempty，空值以 null 发送。
type ComposerDraftDefaults struct {
	Provider          string  `json:"provider"`
	Model             *string `json:"model"`
	Effort            *string `json:"effort"`
	FastMode          bool    `json:"fastMode"`
	RuntimeMode       string  `json:"runtimeMode"`
	InteractionMode   string  `json:"interactionMode"`
	WorkspaceMode     string  `json:"workspaceMode"`
	SwarmEnabled      bool    `json:"swarmEnabled"`
	SwarmPairID       *string `json:"swarmPairID"`
	SwarmBeesProvider *string `json:"swarmBeesProvider"`
	SwarmBeesModel    *string `json:"swarmBeesModel"`
	SwarmBeesEffort   *string `json:"swarmBeesEffort"`
}

// ProvidersListResult 对应 providers.list 成功结果
type ProvidersListResult struct {
	Providers []Provider             `json:"providers"`
	Catalog   []ModelCatalogEntry    `json:"catalog,omitempty"`
	Swarm     SwarmComposerInfo      `json:"swarm"`
	Draft     *ComposerDraftDefaults `json:"draft,omitempty"`
}

// --- 差异对比 (threads.getDiff) ---

// ThreadsGetDiffParams 对应 threads.getDiff 请求参数
type ThreadsGetDiffParams struct {
	ThreadID string  `json:"threadID"`
	TurnID   *string `json:"turnID,omitempty"`
}

// ReviewTurn 对应 RemoteReviewTurn
type ReviewTurn struct {
	TurnID     string `json:"turnID"`
	Index      int    `json:"index"`
	Status     string `json:"status"`
	HasChanges bool   `json:"hasChanges"`
}

// TurnDiffSummary 是 ReviewTurn 的别名，对应 RemoteTurnDiffSummary
type TurnDiffSummary = ReviewTurn

// ThreadsGetDiffResult 对应 threads.getDiff 成功结果
type ThreadsGetDiffResult struct {
	ThreadID          string       `json:"threadID"`
	TurnID            *string      `json:"turnID,omitempty"`
	Diff              string       `json:"diff"`
	Truncated         bool         `json:"truncated"`
	FilesChanged      []string     `json:"filesChanged"`
	Turns             []ReviewTurn `json:"turns"`
	FailureReason     *string      `json:"failureReason,omitempty"`
	MetadataTruncated *bool        `json:"metadataTruncated,omitempty"`
}

// --- 会话移动 (threads.move) ---

// ThreadsMoveParams 对应 threads.move 请求参数
type ThreadsMoveParams struct {
	ThreadID  string `json:"threadID"`
	Direction string `json:"direction"`
}

// ThreadsMoveResult 对应 threads.move 成功结果
type ThreadsMoveResult struct {
	Thread ThreadSummary `json:"thread"`
	Moved  bool          `json:"moved"`
}

// --- 会话队列与跟进项 (threads.queue.*) ---

// Attachment 对应 RemoteAttachment 附件定义
type Attachment struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MimeType  string `json:"mimeType"`
	ByteCount *int   `json:"byteCount"`
}

// RemoteAttachment 是 Attachment 的别名
type RemoteAttachment = Attachment

// FollowUp 对应 RemoteFollowUp 队列跟进项定义
type FollowUp struct {
	ID          string       `json:"id"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments"`
	CreatedAt   string       `json:"createdAt"`
	BundleID    *string      `json:"bundleID"`
	// System 为真表示这是 mcode 系统条目（priority 跟进：子代理汇报、提问转交与编排通知），
	// 客户端应只读展示，不提供编辑与撤回。
	System bool `json:"system,omitempty"`
	// Kind 是排队项出队落库时用户消息的 Kind（如 host_event），空表示普通用户消息。
	// 仅在服务端内存与 storage 之间流转，不进入客户端协议，因此不参与 JSON 序列化。
	Kind string `json:"-"`
	// Intent 是排队项的原始意图，取值 queue 或 insert，缺省视为 queue。
	// insert 表示用户本意是把消息插入当前回合，但服务端无法注入（Provider 不支持 steer
	// 或轮次尚未进入可注入状态），于是降级成排队；queue 或空串表示普通排队。
	// 只由服务端在降级分支写入，客户端请求不得携带。
	Intent string `json:"intent,omitempty"`
}

// RemoteFollowUp 是 FollowUp 的别名
type RemoteFollowUp = FollowUp

// ThreadsQueueListParams 对应 threads.queue.list 请求参数
type ThreadsQueueListParams struct {
	ThreadID string `json:"threadID"`
}

// ThreadsQueueSnapshot 对应会话跟进队列快照 (threads.queue.list 结果)
type ThreadsQueueSnapshot struct {
	ThreadID  string     `json:"threadID"`
	Revision  int        `json:"revision"`
	FollowUps []FollowUp `json:"followUps"`
}

// MarshalJSON 确保 ThreadsQueueSnapshot 序列化时 followUps 字段为空切片输出 [] 而不是 null
func (s ThreadsQueueSnapshot) MarshalJSON() ([]byte, error) {
	type Alias ThreadsQueueSnapshot
	followUps := s.FollowUps
	if followUps == nil {
		followUps = []FollowUp{}
	}
	return json.Marshal(&struct {
		Alias
		FollowUps []FollowUp `json:"followUps"`
	}{
		Alias:     Alias(s),
		FollowUps: followUps,
	})
}

// RemoteThreadsQueueListResult 是 ThreadsQueueSnapshot 的别名
type RemoteThreadsQueueListResult = ThreadsQueueSnapshot

// ThreadsQueueListResult 是 ThreadsQueueSnapshot 的别名
type ThreadsQueueListResult = ThreadsQueueSnapshot

// ThreadsQueueMutationParams 对应 threads.queue.update / remove / sendNow 变更请求参数
type ThreadsQueueMutationParams struct {
	ThreadID    string   `json:"threadID"`
	FollowUpID  string   `json:"followUpID"`
	Text        *string  `json:"text,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
	ActionID    string   `json:"actionID"`
}

// ThreadsQueueUpdateParams 对应 threads.queue.update 请求参数
type ThreadsQueueUpdateParams = ThreadsQueueMutationParams

// ThreadsQueueRemoveParams 对应 threads.queue.remove 请求参数
type ThreadsQueueRemoveParams = ThreadsQueueMutationParams

// ThreadsQueueSendNowParams 对应 threads.queue.sendNow 请求参数
type ThreadsQueueSendNowParams = ThreadsQueueMutationParams

// ThreadsQueueMoveParams 对应 threads.queue.move 请求参数。
// BeforeFollowUpID 为空表示移到队尾；与 FollowUpID 相同或属于同一 bundle 时视为无操作。
type ThreadsQueueMoveParams struct {
	ThreadID         string `json:"threadID"`
	FollowUpID       string `json:"followUpID"`
	BeforeFollowUpID string `json:"beforeFollowUpID,omitempty"`
	ActionID         string `json:"actionID"`
}

// ThreadsQueueMoveResult 对应 threads.queue.move 成功结果，与其它队列变更共用同一结构
type ThreadsQueueMoveResult = ThreadsQueueMutationResult

// ThreadsQueueMutationResult 对应 threads.queue.update / remove / sendNow 成功结果
type ThreadsQueueMutationResult struct {
	ActionID           string     `json:"actionID"`
	ThreadID           string     `json:"threadID"`
	Revision           int        `json:"revision"`
	FollowUps          []FollowUp `json:"followUps"`
	RemovedFollowUpIDs []string   `json:"removedFollowUpIDs"`
}

// ThreadsQueueUpdateResult 对应 threads.queue.update 成功结果
type ThreadsQueueUpdateResult = ThreadsQueueMutationResult

// ThreadsQueueRemoveResult 对应 threads.queue.remove 成功结果
type ThreadsQueueRemoveResult = ThreadsQueueMutationResult

// ThreadsQueueSendNowResult 对应 threads.queue.sendNow 成功结果
type ThreadsQueueSendNowResult = ThreadsQueueMutationResult

const (
	ThreadsQueueInterruptStateStopping    = "stopping"
	ThreadsQueueInterruptStateQueued      = "queued"
	ThreadsQueueInterruptStateSentToSwarm = "sent_to_swarm"
)

// ThreadsQueueInterruptAndSendParams 对应 threads.queue.interruptAndSend 请求参数
type ThreadsQueueInterruptAndSendParams struct {
	ThreadID    string   `json:"threadID"`
	Text        *string  `json:"text,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
	ActionID    string   `json:"actionID"`
}

// ThreadsQueueInterruptAndSendResult 对应 threads.queue.interruptAndSend 成功结果
type ThreadsQueueInterruptAndSendResult struct {
	ActionID   string               `json:"actionID"`
	ThreadID   string               `json:"threadID"`
	State      string               `json:"state"`
	TurnID     *string              `json:"turnID,omitempty"`
	FollowUpID *string              `json:"followUpID,omitempty"`
	Queue      ThreadsQueueSnapshot `json:"queue"`
}

// ThreadsQueueChangedParams 对应 threads.queue.changed 通知参数
type ThreadsQueueChangedParams = ThreadsQueueSnapshot

// ThreadsMCPDisabledSetParams 对应 threads.mcpDisabled.set 请求参数。
// Keys 为空表示恢复为「沿用全局设置」。
type ThreadsMCPDisabledSetParams struct {
	ThreadID string   `json:"threadID"`
	Keys     []string `json:"keys"`
	ActionID string   `json:"actionID"`
}

// ThreadsMCPDisabledSetResult 对应 threads.mcpDisabled.set 成功结果
type ThreadsMCPDisabledSetResult struct {
	ThreadID string   `json:"threadID"`
	Keys     []string `json:"keys"`
}

// --- 会话动作归宿 (threads.settleAction) ---

// ThreadsSettleActionParams 对应 threads.settleAction 请求参数
type ThreadsSettleActionParams struct {
	ActionID        string  `json:"actionID"`
	ActionJournalID string  `json:"actionJournalID"`
	ProjectID       *string `json:"projectID,omitempty"`
	ThreadID        *string `json:"threadID,omitempty"`
}

// ThreadsSettleActionResult 对应 threads.settleAction 成功结果
type ThreadsSettleActionResult struct {
	ActionStatusResult
	SafeToRecoverDraft bool `json:"safeToRecoverDraft"`
}

// --- 会话分支与转发 (threads.branch / threads.continue) ---

// ThreadsBranchParams 对应 threads.branch 请求参数
type ThreadsBranchParams struct {
	ThreadID  string `json:"threadID"`
	MessageID string `json:"messageID"`
	ActionID  string `json:"actionID"`
}

// ThreadsBranchResult 对应 threads.branch 成功结果
type ThreadsBranchResult struct {
	ThreadID string `json:"threadID"`
}

// ThreadsContinueParams 对应 threads.continue 请求参数
type ThreadsContinueParams struct {
	ThreadID string `json:"threadID"`
	ActionID string `json:"actionID"`
}

// ThreadsContinueResult 对应 threads.continue 成功结果
type ThreadsContinueResult struct {
	ThreadID string `json:"threadID"`
}

// --- 可用命令列表 (threads.commands) ---

// RemoteCommand 对应会话可用命令与技能定义
type RemoteCommand struct {
	Name      string `json:"name"`
	Detail    string `json:"detail"`
	IsBuiltIn bool   `json:"isBuiltIn"`
	Kind      string `json:"kind"`
}

// ThreadsCommandsParams 对应 threads.commands 请求参数
type ThreadsCommandsParams struct {
	ThreadID  *string `json:"threadID,omitempty"`
	ProjectID *string `json:"projectID,omitempty"`
	Provider  *string `json:"provider,omitempty"`
}

// ThreadsCommandsResult 对应 threads.commands 成功结果
type ThreadsCommandsResult struct {
	Commands []RemoteCommand `json:"commands"`
	Recent   []string        `json:"recent"`
	Loading  bool            `json:"loading"`
	Error    *string         `json:"error"`
}

// MarshalJSON 确保 ThreadsCommandsResult 序列化时切片为 nil 也输出 []
func (r ThreadsCommandsResult) MarshalJSON() ([]byte, error) {
	type Alias ThreadsCommandsResult
	commands := r.Commands
	if commands == nil {
		commands = []RemoteCommand{}
	}
	recent := r.Recent
	if recent == nil {
		recent = []string{}
	}
	return json.Marshal(&struct {
		Alias
		Commands []RemoteCommand `json:"commands"`
		Recent   []string        `json:"recent"`
	}{
		Alias:    Alias(r),
		Commands: commands,
		Recent:   recent,
	})
}

// --- WebPush 离线推送 ---

// PushPublicKeyResult 对应 push.publicKey 返回结果
type PushPublicKeyResult struct {
	PublicKey string `json:"publicKey"`
}

// PushSubscribeParams 对应 push.subscribe 请求参数
type PushSubscribeParams struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

// PushSubscribeResult 对应 push.subscribe 返回结果
type PushSubscribeResult struct {
	Subscribed bool `json:"subscribed"`
}

// PushUnsubscribeParams 对应 push.unsubscribe 请求参数
type PushUnsubscribeParams struct {
	Endpoint string `json:"endpoint"`
}

// PushUnsubscribeResult 对应 push.unsubscribe 返回结果
type PushUnsubscribeResult struct {
	Unsubscribed bool `json:"unsubscribed"`
}

// TurnRecallParams 对应 threads.turn.recall 请求参数
type TurnRecallParams struct {
	ThreadID string `json:"threadID"`
}

// TurnRecallResult 对应 threads.turn.recall 成功结果
type TurnRecallResult struct {
	ThreadID    string     `json:"threadID"`
	TurnID      string     `json:"turnID"`
	Prompt      string     `json:"prompt"`
	Attachments []MediaRef `json:"attachments"`
}

// ThreadsWorktreePreviewParams 对应 threads.worktree.preview 请求参数。
type ThreadsWorktreePreviewParams struct {
	ThreadID string `json:"threadID"`
}

// ThreadsWorktreePreviewResult 对应 threads.worktree.preview 成功结果。
type ThreadsWorktreePreviewResult struct {
	ThreadID        string   `json:"threadID"`
	Eligible        bool     `json:"eligible"`
	Reason          string   `json:"reason,omitempty"`
	SuggestedBranch string   `json:"suggestedBranch"`
	WorktreePath    string   `json:"worktreePath"`
	Files           []string `json:"files"`
	Untracked       []string `json:"untracked"`
}

// ThreadsWorktreeExecuteParams 对应 threads.worktree.execute 请求参数。
type ThreadsWorktreeExecuteParams struct {
	ThreadID      string `json:"threadID"`
	Branch        string `json:"branch,omitempty"`
	TransferFiles bool   `json:"transferFiles"`
}

// ThreadsWorktreeExecuteResult 对应 threads.worktree.execute 成功结果。
type ThreadsWorktreeExecuteResult struct {
	ThreadID         string `json:"threadID"`
	Branch           string `json:"branch"`
	WorktreePath     string `json:"worktreePath"`
	TransferredFiles int    `json:"transferredFiles"`
}
