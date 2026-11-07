package store

import "time"

// Project 表示项目实体。
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Thread 表示会话线程实体。
type Thread struct {
	ID                string     `json:"id"`
	ProjectID         string     `json:"project_id"`
	Title             string     `json:"title"`
	Provider          string     `json:"provider"`
	Model             string     `json:"model"`
	SessionID         string     `json:"session_id"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	Archived          bool       `json:"archived"`
	Pinned            bool       `json:"pinned"`
	RuntimeMode       string     `json:"runtime_mode"`
	SortOrder         int64      `json:"sort_order"`
	FollowUpRevision  int64      `json:"follow_up_revision"`
	Continuation      string     `json:"continuation"`
	InteractionMode   string     `json:"interaction_mode"`
	Effort            string     `json:"effort"`
	Persona           string     `json:"persona"` // 线程人设 id：空串表示从未设置，"none" 表示用户选了无人设
	BeesProvider      string     `json:"heads_provider"`
	BeesModel         string     `json:"heads_model"`
	BeesEffort        string     `json:"heads_effort"`
	FastMode          bool       `json:"fast_mode"`
	SpeedTier         string     `json:"speed_tier"`
	IsDormant         bool       `json:"is_settled"`
	DormantAt         *time.Time `json:"settled_at,omitempty"`
	IsProvisional     bool       `json:"is_provisional"`
	WorktreePath      string     `json:"worktree_path,omitempty"`
	Branch            string     `json:"branch,omitempty"`
	WorktreeRequested bool       `json:"worktree_requested"`
	SwarmPairID       string     `json:"hydra_pair_id,omitempty"`
	BeeTiers          string     `json:"head_tiers,omitempty"` // 会话级档位覆盖，JSON 编码的 map[档位ID]覆盖项；空表示无覆盖
	SwarmMode         string     `json:"hydra_mode,omitempty"`
	ParentThreadID    string     `json:"parent_thread_id,omitempty"`
	IsHelper          bool       `json:"is_helper"`
	HasUnread         bool       `json:"has_unread"`
	LastStatus        string     `json:"last_status,omitempty"`
	ProviderResumeAt  string     `json:"provider_resume_at,omitempty"`
	ProviderAnchor    string     `json:"provider_anchor,omitempty"`
	// LastUserMessageAt 是用户最后一次发消息的时间；侧边栏排序用它，nil 表示还没有用户消息。
	LastUserMessageAt *time.Time `json:"last_user_message_at,omitempty"`
	// ForkedFromThreadID / ForkedFromMessageID 记录分叉来源（会话 ID 与分叉点消息 ID），
	// 两个都为空表示不是分叉会话；源会话被删除后这两个 ID 仍保留。
	ForkedFromThreadID  string `json:"forked_from_thread_id,omitempty"`
	ForkedFromMessageID string `json:"forked_from_message_id,omitempty"`
	// MCPDisabled 是会话级外部 MCP 禁用名单（服务器名列表）；为空表示沿用全局设置。
	MCPDisabled []string `json:"mcp_disabled,omitempty"`
}

// FollowUpAttachment 表示跟进项的附件元数据。
type FollowUpAttachment struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MimeType  string `json:"mime_type"`
	ByteCount int    `json:"byte_count"`
	Path      string `json:"path"`
}

// FollowUp 表示会话待办跟进项实体。
type FollowUp struct {
	ID       string `json:"id"`
	ThreadID string `json:"thread_id"`
	Position int    `json:"position"`
	Text     string `json:"text"`
	// Kind 是出队落库时用户消息的 Kind（如 host_event），空表示普通用户消息。
	Kind string `json:"kind,omitempty"`
	// Intent 是排队项的原始意图，取值 queue 或 insert，空串视为 queue。
	Intent         string               `json:"intent,omitempty"`
	Attachments    []string             `json:"attachments"`
	AttachmentMeta []FollowUpAttachment `json:"attachment_meta"`
	CreatedAt      time.Time            `json:"created_at"`
	UpdatedAt      time.Time            `json:"updated_at"`
}

// PendingAsk 是提问/审批卡片的失效留痕行；不存答复内容，也不代表卡片还能再答。
// State 为 pending/settled/withdrawn/expired，SettledAt 只在终态时非空。
type PendingAsk struct {
	RequestID  string     `json:"request_id"`
	ThreadID   string     `json:"thread_id"`
	TurnID     string     `json:"turn_id"`
	Kind       string     `json:"kind"`
	Title      string     `json:"title"`
	State      string     `json:"state"`
	Resolution string     `json:"resolution"`
	CreatedAt  time.Time  `json:"created_at"`
	SettledAt  *time.Time `json:"settled_at,omitempty"`
}

const (
	PendingAskPending   = "pending"
	PendingAskSettled   = "settled"
	PendingAskWithdrawn = "withdrawn"
	PendingAskExpired   = "expired"
)

// Delivery 是排队消息离开队列之后的投递状态行；正文只存在于 messages 表。
// State 为 dispatching/accepted/failed/unknown，SettledAt 只在终态时非空。
type Delivery struct {
	MessageID  string     `json:"message_id"`
	ThreadID   string     `json:"thread_id"`
	FollowUpID string     `json:"follow_up_id"`
	TurnID     string     `json:"turn_id"`
	State      string     `json:"state"`
	Attempt    int        `json:"attempt"`
	Failure    string     `json:"failure"`
	CreatedAt  time.Time  `json:"created_at"`
	SettledAt  *time.Time `json:"settled_at,omitempty"`
}

const (
	DeliveryDispatching = "dispatching"
	DeliveryAccepted    = "accepted"
	DeliveryFailed      = "failed"
	DeliveryUnknown     = "unknown"
)

// MediaRef 表示消息中引用的媒体资源。
type MediaRef struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Name      string `json:"name"`
	MimeType  string `json:"mime_type"`
	ByteCount int    `json:"byte_count"`
	Width     *int   `json:"width,omitempty"`
	Height    *int   `json:"height,omitempty"`
}

// Message 表示对话消息实体。
type Message struct {
	ID        string    `json:"id"`
	ThreadID  string    `json:"thread_id"`
	Seq       int64     `json:"seq"`
	Role      string    `json:"role"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	TurnIndex int       `json:"turn_index"`
	TurnID    string    `json:"turn_id"`
	IsFinal   bool      `json:"is_final"`
	CreatedAt time.Time `json:"created_at"`
	// 结构化工具调用名称（仅当 role 为 tool 时出现）
	ToolName *string `json:"tool_name,omitempty"`
	// 结构化工具调用完整输入（按字符截断，仅当 role 为 tool 时出现）
	ToolInput *string `json:"tool_input,omitempty"`
	// 结构化工具执行状态：running、completed、failed、blocked（仅当 role 为 tool 时出现）
	ToolStatus *string `json:"tool_status,omitempty"`
	// 结构化工具完成时间（RFC3339 格式，仅当 role 为 tool 时出现）
	ToolCompletedAt *string `json:"tool_completed_at,omitempty"`
	// 失败类别（仅当 kind 为 error 时出现，取值同 harness.ErrKind*）：重启后历史里的
	// 错误横幅仍能按类别渲染；为空表示未分类，界面回退到关键词识别。
	ErrKind *string    `json:"err_kind,omitempty"`
	Media   []MediaRef `json:"media,omitempty"`
}

// TurnUsage 记录单轮对话的令牌消耗量。
type TurnUsage struct {
	ThreadID     string `json:"thread_id"`
	TurnIndex    int    `json:"turn_index"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CachedTokens int64  `json:"cached_tokens"`
	// ContextTokens 是该轮最后一次模型请求实际带入的上下文大小（输入 + 输出），
	// 只供上下文窗口占用使用；旧库该列缺省为 0，表示 provider 未上报。
	ContextTokens int64 `json:"context_tokens,omitempty"`
	// ContextWindow 是该轮 CLI 上报的当前模型上下文窗口上限（tokens）；旧库缺省为 0，
	// 表示 provider 未上报，界面回退到模型目录里的静态上限。
	ContextWindow int64 `json:"context_window,omitempty"`
	// CostUSD 是该轮花费（美元），取自 CLI 的 total_cost_usd；0 表示 provider 未上报。
	CostUSD float64 `json:"cost_usd,omitempty"`
	// RequestsJSON 是该轮每次模型请求速度样本的 JSON（harness.RequestSpeed 数组）；
	// store 不依赖 harness，故按原始文本保存，旧库为空串。
	RequestsJSON string `json:"requests_json,omitempty"`
}

// Turn 表示轮次实体。
type Turn struct {
	ID                    string     `json:"id"`
	ThreadID              string     `json:"thread_id"`
	TurnIndex             int        `json:"turn_index"`
	Status                string     `json:"status"`
	StartedAt             time.Time  `json:"started_at"`
	CompletedAt           *time.Time `json:"completed_at,omitempty"`
	BaseCheckpoint        string     `json:"base_checkpoint,omitempty"`
	FinishCheckpoint      string     `json:"finish_checkpoint,omitempty"`
	UserPrompt            string     `json:"user_prompt"`
	SystemInjectedContext string     `json:"system_injected_context,omitempty"`
	RawOutput             string     `json:"raw_output,omitempty"`
	TouchedFiles          []string   `json:"touched_files,omitempty"`
	InputTokens           int64      `json:"input_tokens"`
	OutputTokens          int64      `json:"output_tokens"`
	CostMicros            int64      `json:"cost_micros"`
	ErrorMessage          string     `json:"error_message,omitempty"`
}
