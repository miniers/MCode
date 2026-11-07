package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// 总管（steward，职能称总管）专属工具名。这些工具只对总管的会话可见、可调用。
// approval_suggest 只登记建议、不执行审批，因此与观察类工具同档，不受启用开关影响。
const (
	stewardAttentionListToolName    = "attention_list"
	stewardAttentionResolveToolName = "attention_resolve"
	stewardThreadStatusToolName     = "thread_status"
	stewardNotifyOwnerToolName      = "notify_owner"
	stewardApprovalSuggestToolName  = "approval_suggest"
	// stewardOpenTopicToolName 只给总管主会话：用户提出要做的事时开一个话题会话。
	stewardOpenTopicToolName = "steward_open_topic"
	// stewardProjectsListToolName 也只给总管主会话：列出现有项目，免得它猜项目 ID 或目录。
	stewardProjectsListToolName = "steward_projects_list"
	// stewardPlanRequestToolName 也只给总管主会话：把需要认真设计的任务交给宿主派强模型出方案。
	stewardPlanRequestToolName = "plan_request"
	// stewardAssignTaskToolName 也只给总管主会话：把动手的活派给目标项目的可写子代理，
	// 总管自己仍然只读，不改文件、不执行命令。
	stewardAssignTaskToolName = "steward_assign_task"
	// stewardLedgerGetToolName 与 stewardLedgerUpdateToolName 只给总管主会话：读写它自己的账本。
	stewardLedgerGetToolName    = "steward_ledger_get"
	stewardLedgerUpdateToolName = "steward_ledger_update"
	// stewardUpdateBriefToolName 与 stewardStartGoalToolName 只给话题会话。
	stewardUpdateBriefToolName = "steward_update_brief"
	stewardStartGoalToolName   = "steward_start_goal"
)

// 总管的动作工具名。这些工具要真的动手，因此只在总管启用时才可见、可调用；
// 名字与观察类工具分开登记，两套白名单互不影响。
const (
	stewardThreadSendMessageToolName = "thread_send_message"
	stewardThreadInterruptToolName   = "thread_interrupt"
	stewardAutomationRunNowToolName  = "automation_run_now"
	stewardKanbanMoveTaskToolName    = "kanban_move_task"
	// stewardAutomationCreateToolName 是既有的 automation_create：总管也要能按固定节奏建自动化。
	stewardAutomationCreateToolName = "automation_create"
)

// 动作工具的参数上限。
const (
	// stewardSendMessageTextMaxRunes 限制发给其他会话的消息长度，更长的内容应拆成几条。
	stewardSendMessageTextMaxRunes = 2000
	// stewardActionReasonMaxRunes 限制动作理由的长度：理由是一行短语，会记账也可能展示给用户。
	stewardActionReasonMaxRunes = 200
)

// 话题工具的参数上限。
const (
	// stewardTopicTitleMaxRunes 限制话题标题长度：标题会出现在会话列表与话题卡片上。
	stewardTopicTitleMaxRunes = 30
	// stewardTopicRequestMaxRunes 限制转给话题会话的需求原文长度，更长的需求应让用户分段说。
	stewardTopicRequestMaxRunes = 4000
	// stewardBriefMaxRunes 限制任务书全文长度：任务书会变成 Goal 的提示词，太长会挤掉真正的指令。
	stewardBriefMaxRunes = 12000
	// stewardBriefMaxOpenQuestions 是待定问题条数的合理上限，超过说明任务书还没收敛。
	stewardBriefMaxOpenQuestions = 50
)

// 账本工具的参数上限与取值，与 internal/steward/ledger.go 保持一致。
const (
	// stewardLedgerTextMaxRunes 限制单条账本正文的长度：账本每条一行。
	stewardLedgerTextMaxRunes = 300
)

// stewardLedgerSections 是账本分区的合法取值。
var stewardLedgerSections = map[string]bool{
	"in_progress":  true,
	"decision":     true,
	"pending_user": true,
}

// stewardLedgerOps 是 steward_ledger_update 支持的操作。
var stewardLedgerOps = map[string]bool{
	"add":    true,
	"update": true,
	"remove": true,
}

// stewardKanbanMoveStatuses 是总管允许把任务移入的状态。
// todo 表示重新排队执行；review、done、rejected 只改看板状态。
var stewardKanbanMoveStatuses = map[string]bool{
	"todo":     true,
	"review":   true,
	"done":     true,
	"rejected": true,
}

// stewardApprovalSuggestOptions 是总管可以建议的审批选项：只有允许与拒绝两种。
var stewardApprovalSuggestOptions = map[string]bool{
	"allow": true,
	"deny":  true,
}

// 总管工具的参数默认值与上限。
const (
	attentionListDefaultLimit = 50
	attentionListMaxLimit     = 200
	// notifyOwnerTitleMaxRunes 限制通知标题的长度：通知是 IM 式短消息，标题只写结论。
	notifyOwnerTitleMaxRunes = 40
	// notifyOwnerBodyMaxRunes 限制通知正文的长度：通知是 IM 式短消息，正文只写结论和要用户做什么。
	notifyOwnerBodyMaxRunes = 200
	// stewardAssignTaskMaxRunes 限制交给子代理的任务书长度：任务书要写清改成什么样与验收要求。
	stewardAssignTaskMaxRunes = 4000
	// stewardAssignContextMaxRunes 限制补充信息的长度：已知现象、相关文件与约束。
	stewardAssignContextMaxRunes = 8000
	// stewardAssignWritePathsMax 是允许落地路径的条数上限，过多说明任务范围还没收敛。
	stewardAssignWritePathsMax = 20
)

// stewardToolNames 是总管（总管）专属工具：只有 IsSteward 为真的会话能看到并调用。
var stewardToolNames = map[string]bool{
	stewardAttentionListToolName:    true,
	stewardAttentionResolveToolName: true,
	stewardThreadStatusToolName:     true,
	stewardNotifyOwnerToolName:      true,
	stewardApprovalSuggestToolName:  true,
	stewardOpenTopicToolName:        true,
	stewardProjectsListToolName:     true,
	stewardPlanRequestToolName:      true,
	stewardAssignTaskToolName:       true,
	stewardLedgerGetToolName:        true,
	stewardLedgerUpdateToolName:     true,
}

// stewardTopicToolNames 是话题会话专属工具：只有 IsTopic 为真的会话能看到并调用，
// 总管主会话反而看不到——任务书与开工都只能在话题里做。
var stewardTopicToolNames = map[string]bool{
	stewardUpdateBriefToolName: true,
	stewardStartGoalToolName:   true,
}

// stewardTopicReadOnlyTools 是话题会话额外可见的既有只读工具：向用户汇报、检索历史与长期记忆。
// 看板工具另见 stewardTopicKanbanTools，它们的可见性还跟是否已建目标有关。
var stewardTopicReadOnlyTools = map[string]bool{
	stewardNotifyOwnerToolName: true,
	"thread_search":            true,
	"thread_fetch_messages":    true,
	"memory_search":            true,
	"memory_get":               true,
}

// stewardTopicKanbanTools 是话题会话可用的看板工具：验收与开工之后要用到的全部工具。
var stewardTopicKanbanTools = map[string]bool{
	"kanban_read_board":  true,
	"kanban_list_tasks":  true,
	"kanban_read_card":   true,
	"kanban_task_diff":   true,
	"kanban_read_goal":   true,
	"kanban_goal_status": true,
	"kanban_review_task": true,
}

// stewardTopicKanbanPlanOnly 是话题建目标之后（计划角色）才给的看板工具：
// 建目标前没有任务可验收，提前给只会诱使模型乱调。
var stewardTopicKanbanPlanOnly = map[string]bool{
	"kanban_review_task": true,
}

// stewardTopicActionTools 是话题会话可用的动作工具：催促、中断本目标下任务的执行会话，
// 以及移动本目标的任务。automation_run_now 与 automation_create 不在这里，话题会话不跑自动化。
var stewardTopicActionTools = map[string]bool{
	stewardThreadSendMessageToolName: true,
	stewardThreadInterruptToolName:   true,
	stewardKanbanMoveTaskToolName:    true,
}

// stewardActionToolNames 是总管的动作工具：只有总管启用时才列出、可调用。
// 其中 automation_create 与普通会话共用，见 stewardSharedActionToolNames。
var stewardActionToolNames = map[string]bool{
	stewardThreadSendMessageToolName: true,
	stewardThreadInterruptToolName:   true,
	stewardAutomationRunNowToolName:  true,
	stewardKanbanMoveTaskToolName:    true,
	stewardAutomationCreateToolName:  true,
}

// stewardSharedActionToolNames 是总管与普通会话共用的动作工具：只受 CanAct 控制是否给总管，
// 普通会话照旧可见可用，excludeStewardTools 不会把它们藏起来。
var stewardSharedActionToolNames = map[string]bool{
	stewardAutomationCreateToolName: true,
}

// stewardReadOnlyTools 是总管额外可见的既有只读工具：看板与目标只能看，
// 自动化、历史对话、长期记忆与子代理汇报全文只能查。它们对总管常驻可见；动作工具另见 stewardActionToolNames，
// 是否真的可用由 CanAct 决定。
var stewardReadOnlyTools = map[string]bool{
	"kanban_read_board":     true,
	"kanban_list_tasks":     true,
	"kanban_read_card":      true,
	"kanban_task_diff":      true,
	"kanban_read_goal":      true,
	"kanban_goal_status":    true,
	"automation_list":       true,
	"thread_search":         true,
	"thread_fetch_messages": true,
	"memory_search":         true,
	"memory_get":            true,
	"swarm_report_get":      true,
}

// stewardReceiptTools 是总管额外可见的回执工具：总管线程可能是 heartbeat 自动化的来源会话，
// 每轮提示词投递回总管线程后需要提交运行回执。它只能回报本线程自己的 run
// （自动化侧按线程作用域校验归属），不扩大权限；也不进 stewardToolNames——
// 那个名单会把工具从普通会话里隐去，而普通会话照旧要能提交回执。
var stewardReceiptTools = map[string]bool{
	"automation_report_result": true,
}

// stewardCrossProjectTools 是总管可以跨项目查询的工具：分诊要看到所有项目的待处理事件
// 与会话状态，因此这两个工具的 project_id 不受“必须是当前会话项目”的限制。
var stewardCrossProjectTools = map[string]bool{
	stewardAttentionListToolName: true,
	stewardThreadStatusToolName:  true,
	// 总管的 automation_create 要按 project_id 指定在哪个项目建自动化，
	// 因此它的 project_id 也不受“必须是当前会话项目”的限制。
	stewardAutomationCreateToolName: true,
}

// stewardToolAllowed 报告某个观察类工具是否对总管可见、可调用：专属工具、既有只读工具与回执工具。
// 动作工具另由 stewardActionToolNames 与 CanAct 判定，不混进这条规则。
func stewardToolAllowed(name string) bool {
	return stewardToolNames[name] || stewardReadOnlyTools[name] || stewardReceiptTools[name]
}

// stewardCallAllowed 是 tools/call 的拦截规则：观察类工具按白名单放行，动作工具只校验名字。
// 动作工具是否真的能执行由 handler 里的 CanAct 校验决定，所以总管未启用时模型看到的是
// 可读的权限说明，而不是“未知工具”。
func stewardCallAllowed(name string) bool {
	return stewardToolAllowed(name) || stewardActionToolNames[name]
}

// stewardTopicCallAllowed 是话题会话在 tools/call 上的白名单：话题专属工具、话题只读工具、
// 话题可用的看板工具与话题动作工具。总管的分诊工具、自动化动作与入口工具都不在名单里。
func stewardTopicCallAllowed(name string) bool {
	return stewardTopicToolNames[name] || stewardTopicReadOnlyTools[name] ||
		stewardTopicKanbanTools[name] || stewardTopicActionTools[name]
}

// stewardScopedCallAllowed 按会话角色选白名单：总管主会话用分诊白名单，话题会话用话题白名单。
// scoped 为 false 表示这个会话不受总管规则约束（普通会话、看板线程与共享端点）。
func stewardScopedCallAllowed(hooks *StewardHooks, ctx context.Context, name string) (allowed, scoped bool) {
	if stewardUsable(hooks, ctx) {
		return stewardCallAllowed(name), true
	}
	if stewardTopicUsable(hooks, ctx) {
		return stewardTopicCallAllowed(name), true
	}
	return false, false
}

// stewardCanAct 报告总管现在是否允许动手：只看总管是否启用，不再有专属权限档；
// CanAct 未接线时按不可动手处理。
func stewardCanAct(hooks *StewardHooks) bool {
	return hooks != nil && hooks.CanAct != nil && hooks.CanAct()
}

// AttentionItem 描述一条待处理事件，即总管需要分诊并汇报给用户的对象
// （卡住的会话、出错的会话、额度用尽、看板任务失败、自动化失败等）。
// 时间字段是 RFC3339 字符串，由后端格式化，MCP 层不再解析。
type AttentionItem struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	SubjectType string `json:"subjectType"`
	SubjectID   string `json:"subjectId"`
	ProjectID   string `json:"projectId"`
	ThreadID    string `json:"threadId"`
	Title       string `json:"title"`
	Detail      string `json:"detail"`
	EnteredAt   string `json:"enteredAt"`
	CreatedAt   string `json:"createdAt"`
	Resolved    bool   `json:"resolved"`
}

// StewardPendingRequest 描述某个会话里一条等待用户审批的请求，供总管提出审批建议。
type StewardPendingRequest struct {
	// RequestID 是这条请求的 ID，approval_suggest 用它指向请求。
	RequestID string `json:"requestId"`
	// Kind 是请求类别：command、fileChange、tool、permissions、plan、commit、question 之一。
	Kind string `json:"kind"`
	// Title 是请求的一行概述。
	Title string `json:"title"`
	// Detail 是请求的具体内容，例如要执行的命令或要申请的权限。
	Detail string `json:"detail"`
	// Options 是可选的 option ID，例如 allow、allow_always、deny。
	Options []string `json:"options"`
	// Suggestion 是总管已给出的建议（allow 或 deny）；为空表示还没有建议。
	Suggestion string `json:"suggestion,omitempty"`
}

// ThreadStatusItem 描述一个会话当前的运行状态，供总管向用户说明“谁在跑、谁在等、谁刚出错”。
type ThreadStatusItem struct {
	ThreadID         string `json:"threadId"`
	ProjectID        string `json:"projectId"`
	Title            string `json:"title"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	Running          bool   `json:"running"`
	AwaitingApproval bool   `json:"awaitingApproval"`
	AwaitingInput    bool   `json:"awaitingInput"`
	Failed           bool   `json:"failed"`
	FailureReason    string `json:"failureReason"`
	LastActivityText string `json:"lastActivityText"`
	UpdatedAt        string `json:"updatedAt"`
	// PendingRequests 是该会话等待审批的请求，只在 AwaitingApproval 为真时由宿主填充。
	PendingRequests []StewardPendingRequest `json:"pendingRequests,omitempty"`
}

// ProjectItem 描述用户已添加的一个项目，供总管对照项目清单判断需求属于哪个项目，
// 以及该给 steward_open_topic 填哪个项目 ID 或目录。
// 总管自己的家目录项目也在清单里，不特殊隐藏。
type ProjectItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// PlanRequestInput 是 plan_request 的入参：Project 可以是项目 ID、名字或目录，
// 空表示总管主目录；Goal 是要解决的问题或目标；Context 是已知现象、约束、相关会话等补充信息。
type PlanRequestInput struct {
	Project string
	Goal    string
	Context string
}

// PlanRequestResult 是一次派活的结果：BeeName 是子代理名，ProjectName 与 WorkDir 是它所在的项目，
// PlanDir 是方案文档要写的目录。
type PlanRequestResult struct {
	BeeName     string
	ProjectName string
	WorkDir     string
	PlanDir     string
}

// AssignTaskInput 是 steward_assign_task 的入参：Project 可以是项目 ID、名字或目录；
// Task 是要改成什么样与验收要求；Context 是已知现象、相关文件（仓库相对路径）与约束；
// WritePaths 是允许落地的仓库相对路径通配，空表示不限。
type AssignTaskInput struct {
	Project    string
	Task       string
	Context    string
	WritePaths []string
}

// AssignTaskResult 是一次派活的结果：Dispatched 为 false 表示宿主没派出去（例如编排未开启），
// 此时 Message 说明原因、总管应当自己做；Dispatched 为 true 时 BeeName 是子代理名，ThreadID 是它的会话，
// ProjectName 与 WorkDir 是它所在的项目，完成后汇报自动送回总管的会话。
type AssignTaskResult struct {
	Dispatched  bool
	Message     string
	BeeName     string
	ThreadID    string
	ProjectName string
	WorkDir     string
}

// LedgerItem 描述总管账本里的一条记录。时间字段是 RFC3339 字符串，由后端格式化。
type LedgerItem struct {
	ID            string `json:"id"`
	Section       string `json:"section"`
	Text          string `json:"text"`
	TopicThreadID string `json:"topicThreadId,omitempty"`
	// SubjectType 与 SubjectID 是这条记录关联的待处理事件对象，例如看板任务或会话。
	SubjectType string `json:"subjectType,omitempty"`
	SubjectID   string `json:"subjectId,omitempty"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// StewardHooks 把总管的后端能力接给它的专属工具。总管在 MCP 服务之后才接线，
// 所以由 app 用 Server.SetSteward 注入；没有注入时工具既不列出也不能调用。
// 每个函数字段都可以为 nil，为 nil 时对应工具返回明确的“未启用”错误。
type StewardHooks struct {
	// IsSteward 报告该会话是不是总管。
	IsSteward func(threadID string) bool
	// ListAttention 列出待处理事件；unresolvedOnly 为真时只返回尚未处理的。
	// projectID 为空表示所有项目。
	ListAttention func(ctx context.Context, unresolvedOnly bool, projectID string, limit int) ([]AttentionItem, error)
	// ResolveAttention 把一条待处理事件标记为已处理。
	ResolveAttention func(ctx context.Context, id string) error
	// ThreadStatus 返回需要关注的会话状态；projectID 为空表示所有项目。
	ThreadStatus func(ctx context.Context, projectID string) ([]ThreadStatusItem, error)
	// NotifyOwner 给用户发一条桌面通知；threadID 为空表示通知不指向具体会话。
	NotifyOwner func(ctx context.Context, title, body, threadID string) error
	// CanAct 报告总管是否已启用。为 nil 或返回 false 时四个动作工具
	// 既不出现在 tools/list，也不能调用（调用返回权限错误）。
	CanAct func() bool
	// SendMessage 把一条消息排队投递给 threadID 指向的会话；它不会打断正在进行的一轮。
	SendMessage func(ctx context.Context, threadID, text, reason string) error
	// InterruptThread 打断 threadID 指向的会话当前正在做的事。
	InterruptThread func(ctx context.Context, threadID, reason string) error
	// RunAutomation 立刻跑一次 automationID 指向的自动化，返回本次运行的 runID。
	RunAutomation func(ctx context.Context, automationID, reason string) (runID string, err error)
	// MoveKanbanTask 把 taskID 指向的任务移到 status 状态。
	MoveKanbanTask func(ctx context.Context, taskID, status, reason string) error
	// SuggestApproval 为 threadID 会话里 requestID 指向的待审批请求登记一条建议（optionID 为 allow 或 deny）。
	// 它只登记建议、不执行审批：宿主把建议展示给用户，用户确认后才真正执行。
	SuggestApproval func(ctx context.Context, threadID, requestID, optionID, reason string) error
	// IsTopic 报告该会话是不是某个话题的会话；为 nil 时话题工具既不可见也不可调用。
	IsTopic func(threadID string) bool
	// TopicOwnsSubject 报告某个对象是否属于该话题的目标；subjectType 取 thread（任务的执行会话或评审会话）
	// 或 kanban_task（看板任务）。为 nil 时话题会话没有动作工具。
	TopicOwnsSubject func(ctx context.Context, topicThreadID, subjectType, subjectID string) (bool, error)
	// OpenTopic 为一条需求开话题会话并返回会话 ID；projectID 由接线方把项目 ID 或目录解析好。
	OpenTopic func(ctx context.Context, projectID, title, request string) (threadID string, err error)
	// UpdateBrief 写入话题的任务书全文与待定问题条数。
	UpdateBrief func(ctx context.Context, threadID, brief string, openQuestions int) error
	// StartTopicGoal 让话题的目标从计划待审推进到执行；reason 说明为什么可以直接开工。
	StartTopicGoal func(ctx context.Context, threadID, reason string) error
	// ConfirmTopic 让宿主把话题直接开工：任务书待定问题清零后由 steward_update_brief 触发，
	// 界面上的「直接开工」按钮也走同一条路径。话题已开工、任务书为空等既有校验留在宿主侧。
	ConfirmTopic func(ctx context.Context, threadID string) error
	// ListProjects 列出用户已添加的全部项目（含总管自己的家目录项目），
	// 供总管判断需求属于哪个项目，而不是猜项目 ID 或目录。
	ListProjects func(ctx context.Context) ([]ProjectItem, error)
	// LedgerGet 返回账本全部条目，以及渲染给模型看的账本文本。
	LedgerGet func(ctx context.Context) (items []LedgerItem, rendered string, err error)
	// LedgerAdd 新增一条账本记录；topicThreadID 可空，subjectType 与 subjectID 用来关联
	// 待处理事件的对象，两者要么都给要么都空。
	LedgerAdd func(ctx context.Context, section, text, topicThreadID, subjectType, subjectID string) (LedgerItem, error)
	// LedgerEdit 按 id 改写一条记录；section 或 text 为空表示保持原值。
	LedgerEdit func(ctx context.Context, id, section, text string) (LedgerItem, error)
	// LedgerRemove 按 id 删除一条记录。
	LedgerRemove func(ctx context.Context, id string) error
	// TurnStartedByUser 报告该会话当前这一轮是否由用户亲自发消息开起：
	// 宿主事件唤醒的轮次（会话卡住、任务失败、日报等）与子代理汇报唤起的轮次都返回 false。
	TurnStartedByUser func(threadID string) bool
	// PlanRequest 把需要认真设计的任务交给宿主：宿主用强模型在 req.Project 指向的项目里
	// 派一个只允许写方案文档的子代理，完成后汇报自动送回该会话。
	PlanRequest func(ctx context.Context, threadID string, req PlanRequestInput) (PlanRequestResult, error)
	// AssignTask 把一件事交给宿主：宿主在 req.Project 指向的项目里派一个可写子代理做完，
	// 完成汇报自动送回该会话。RuntimeMode 与 ProjectID 必须由宿主显式给出，
	// 不能继承总管自己的只读档——否则子代理的改动会被判为只读违规并丢弃。
	AssignTask func(ctx context.Context, threadID string, req AssignTaskInput) (AssignTaskResult, error)
}

// SetSteward 并发安全地设置总管钩子。
func (s *Server) SetSteward(hooks *StewardHooks) {
	s.stewardMu.Lock()
	defer s.stewardMu.Unlock()
	s.steward = hooks
}

func (s *Server) stewardHooks() *StewardHooks {
	s.stewardMu.RLock()
	defer s.stewardMu.RUnlock()
	return s.steward
}

// stewardAvailable 判断请求所在的会话是不是总管；没有会话作用域的共享端点一律不是。
func (s *Server) stewardAvailable(ctx context.Context) bool {
	return stewardUsable(s.stewardHooks(), ctx)
}

// stewardUsable 用调用方已取到的钩子做判定，tools/list 与 tools/call 因此共用同一条规则，
// 也让 tools/call 之后复用同一个钩子指针，避免两次取钩子之间被 SetSteward(nil) 置空。
func stewardUsable(hooks *StewardHooks, ctx context.Context) bool {
	threadID := scopeThreadID(ctx)
	return threadID != "" && hooks != nil && hooks.IsSteward != nil && hooks.IsSteward(threadID)
}

// filterStewardTools 按白名单裁剪总管的工具列表：观察类工具与既有只读工具常驻，
// 动作工具只在总管启用时出现。采用白名单而不是黑名单，新增的写工具默认不会漏给总管。
func filterStewardTools(hooks *StewardHooks, tools []ToolDefinition) []ToolDefinition {
	canAct := stewardCanAct(hooks)
	out := make([]ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		if stewardActionToolNames[tool.Name] {
			if canAct {
				out = append(out, tool)
			}
			continue
		}
		if stewardToolAllowed(tool.Name) {
			out = append(out, tool)
		}
	}
	return out
}

// stewardTopicUsable 报告请求所在的会话是不是话题会话：话题会话是总管为该话题开的专用会话。
func stewardTopicUsable(hooks *StewardHooks, ctx context.Context) bool {
	threadID := scopeThreadID(ctx)
	return threadID != "" && hooks != nil && hooks.IsTopic != nil && hooks.IsTopic(threadID)
}

// stewardTopicGuard 在话题工具的 handler 里做二次校验：非话题会话按未知工具拒绝，
// 与 tools/list 的裁剪共用同一条判定，因此绕过列表直接调用也拿不到别的工具。
func stewardTopicGuard(tc ToolContext, name string) (*StewardHooks, *ToolResult) {
	hooks := tc.Server.stewardHooks()
	if !stewardTopicUsable(hooks, tc.Context) {
		res := toolError(CodeNotFound, "Unknown tool: "+name, false)
		return nil, &res
	}
	return hooks, nil
}

// stewardOwnerGuard 用于总管与话题会话共用的工具（目前是 notify_owner）：
// 两者都可以向用户汇报，其他会话一律按未知工具拒绝。
func stewardOwnerGuard(tc ToolContext, name string) (*StewardHooks, *ToolResult) {
	hooks := tc.Server.stewardHooks()
	if stewardUsable(hooks, tc.Context) || stewardTopicUsable(hooks, tc.Context) {
		return hooks, nil
	}
	res := toolError(CodeNotFound, "Unknown tool: "+name, false)
	return nil, &res
}

// excludeStewardTools 在非总管的会话里隐藏总管的观察类与动作类工具；它们仍被注册，
// 直接调用时由 handler 里的二次校验按未知工具拒绝。共用动作工具（stewardSharedActionToolNames）
// 不算总管专属，普通会话照旧可见可用，因此不在这里隐藏。
func excludeStewardTools(tools []ToolDefinition) []ToolDefinition {
	out := make([]ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		if stewardToolNames[tool.Name] || stewardTopicToolNames[tool.Name] {
			continue
		}
		if stewardActionToolNames[tool.Name] && !stewardSharedActionToolNames[tool.Name] {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// filterTopicTools 按白名单裁剪话题会话的工具列表：话题专属工具、话题只读工具、
// 建目标后（计划角色）要用的看板工具，以及计划角色且能动手时的话题动作工具。
// 总管的分诊工具与看板入口工具都不给：话题会话只做一件事，工具越少越不容易走偏。
func filterTopicTools(hooks *StewardHooks, tools []ToolDefinition, planRole bool) []ToolDefinition {
	out := make([]ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		switch {
		case stewardTopicToolNames[tool.Name]:
			// steward_start_goal 只在建目标之后才有意义。
			if tool.Name == stewardStartGoalToolName && !planRole {
				continue
			}
			out = append(out, tool)
		case stewardTopicReadOnlyTools[tool.Name]:
			out = append(out, tool)
		case stewardTopicActionTools[tool.Name]:
			// 动作工具必须先于 kanban_ 前缀分支判断：kanban_move_task 也以 kanban_ 开头。
			if planRole && stewardCanAct(hooks) && hooks.TopicOwnsSubject != nil {
				out = append(out, tool)
			}
		case strings.HasPrefix(tool.Name, kanbanToolNamePrefix):
			if stewardTopicKanbanTools[tool.Name] && (planRole || !stewardTopicKanbanPlanOnly[tool.Name]) {
				out = append(out, tool)
			}
		}
	}
	return out
}

// stewardDisabled 返回“该工具未接线”的错误；钩子字段为 nil 时用它，措辞直接说明未启用。
func stewardDisabled(name string) ToolResult {
	return toolError(CodeInternal, name+" is not enabled: the steward hooks are not wired in this mcode build.", false)
}

// stewardGuard 在 handler 里做二次校验：非总管调用按未知工具拒绝，不暴露工具存在。
// 返回钩子指针供后续使用，钩子只取一次，中途 SetSteward(nil) 也不会让后面拿到 nil。
func stewardGuard(tc ToolContext, name string) (*StewardHooks, *ToolResult) {
	hooks := tc.Server.stewardHooks()
	if !stewardUsable(hooks, tc.Context) {
		res := toolError(CodeNotFound, "Unknown tool: "+name, false)
		return nil, &res
	}
	return hooks, nil
}

// stewardActionGuard 在动作工具 handler 里做二次校验：总管主会话先确认总管已启用，
// 未启用时给出“去设置里打开总管”的说明。话题会话只能调用 stewardTopicActionTools 里的
// 动作工具，且要求能动手并接线了归属判定。钩子只取一次，中途 SetSteward(nil) 也不会让
// 后面拿到 nil，话题判定因此不会因为钩子被换成 nil 而恒为假。
func stewardActionGuard(tc ToolContext, name string) (*StewardHooks, *ToolResult) {
	hooks := tc.Server.stewardHooks()
	if stewardUsable(hooks, tc.Context) {
		if !stewardCanAct(hooks) {
			res := stewardCannotAct(name)
			return nil, &res
		}
		return hooks, nil
	}
	if stewardTopicUsable(hooks, tc.Context) && stewardTopicActionTools[name] &&
		stewardCanAct(hooks) && hooks.TopicOwnsSubject != nil {
		return hooks, nil
	}
	res := toolError(CodeNotFound, "Unknown tool: "+name, false)
	return nil, &res
}

// stewardTopicScopeCheck 校验话题会话只能操作本目标下的对象：subjectType 取 thread 或 kanban_task。
// 总管主会话与非话题调用方直接放行；话题会话没有接线归属判定时按内部错误处理。
func stewardTopicScopeCheck(tc ToolContext, hooks *StewardHooks, subjectType, subjectID string) *ToolResult {
	if stewardUsable(hooks, tc.Context) || !stewardTopicUsable(hooks, tc.Context) {
		return nil
	}
	if hooks.TopicOwnsSubject == nil {
		res := toolError(CodeInternal, "topic scope check is not enabled: the steward hooks are not wired in this mcode build.", false)
		return &res
	}
	owned, err := hooks.TopicOwnsSubject(tc.Context, scopeThreadID(tc.Context), subjectType, subjectID)
	if err != nil {
		res := toolError(CodeInternal, err.Error(), false)
		return &res
	}
	if owned {
		return nil
	}
	res := toolError(CodeForbidden,
		"this topic can only act on tasks and conversations under its own goal",
		false,
		"这个话题只能操作本目标下的任务与执行会话；其他对象请交给总管或用户处理。")
	return &res
}

// stewardCannotAct 说明总管未启用导致动作工具不可用，并指出用户该去哪里打开。
// 主信息用英文给模型读，中文说明只放在 hint 里，不再中英拼接成一句。
func stewardCannotAct(name string) ToolResult {
	cannotAct := "总管未启用，这个工具用不了，请在「设置 → 总管」里打开总管。"
	return toolError(CodeForbidden,
		name+" is not allowed while the steward is disabled. "+
			"Ask the user to turn the steward on in Settings → Steward.",
		false,
		cannotAct)
}

// stewardRequiredArg 取必填字符串参数并去掉首尾空白；缺失或全为空白时给出可直接重试的说明。
func stewardRequiredArg(args map[string]any, name, hint string) (string, *ToolResult) {
	value, _ := args[name].(string)
	value = strings.TrimSpace(value)
	if value == "" {
		res := toolError(CodeInvalidArguments, fmt.Sprintf("Missing required argument: '%s' (%s).", name, hint), false)
		return "", &res
	}
	return value, nil
}

// stewardCheckMaxRunes 按 rune 计数校验长度；超长时要求精简后重试，避免下游静默截断。
func stewardCheckMaxRunes(name, value string, max int) *ToolResult {
	if n := utf8.RuneCountInString(value); n > max {
		res := toolError(CodeInvalidArguments, fmt.Sprintf(
			"Argument '%s' is %d characters long, but at most %d are allowed. Shorten it, then retry.",
			name, n, max), false)
		return &res
	}
	return nil
}

func (r *Registry) registerStewardTools(s *Server) {
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	// reason 是动作工具共用的理由参数：一行短语，便于审计与转述给用户。
	reason := func(what string) map[string]any {
		return str(fmt.Sprintf("总管%s的原因，一句短语。它会记进账本，也可能展示给用户。最多 %d 字。",
			what, stewardActionReasonMaxRunes))
	}
	r.Register(ToolDefinition{
		Name: stewardAttentionListToolName,
		Description: "列出等待分诊的待处理事件：卡住、报错或额度用尽的会话，失败的看板任务，失败的自动化运行。" +
			"每条事件带类别、所属对象、项目与会话、短标题与详情，以及进入队列的时间。" +
			"这个工具只在总管的会话里可用。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"unresolved_only": map[string]any{
					"type":        "boolean",
					"default":     true,
					"description": "只返回尚未处理的事件。默认 true；传 false 时连已处理的一起返回。",
				},
				"project_id": str("只看某一个项目的事件，可以填用户任意一个项目的 ID。留空表示所有项目。"),
				"limit": map[string]any{
					"type":        "integer",
					"default":     attentionListDefaultLimit,
					"minimum":     1,
					"maximum":     attentionListMaxLimit,
					"description": fmt.Sprintf("最多返回多少条，1 到 %d 之间。默认 %d。", attentionListMaxLimit, attentionListDefaultLimit),
				},
			},
		},
		Handler: handleAttentionList,
	})

	r.Register(ToolDefinition{
		Name: stewardAttentionResolveToolName,
		Description: "把一条待处理事件标记为已处理：分诊完、并且已经告诉过用户之后再调用。" +
			"id 取自 attention_list。标记后事件从队列里清掉，但不会改动它指向的会话或任务。" +
			"这个工具只在总管的会话里可用。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": str("要标记为已处理的事件 ID，取自 attention_list 的返回。"),
			},
			"required": []string{"id"},
		},
		IsWrite: true,
		Handler: handleAttentionResolve,
	})

	r.Register(ToolDefinition{
		Name: stewardThreadStatusToolName,
		Description: "列出正在运行、等待审批、等待输入和刚刚失败的会话，带上它们的模型提供方、模型、失败原因与最近活动。" +
			"用来说明现在发生了什么、哪个会话需要用户处理。这个工具只在总管的会话里可用。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project_id": str("只看某一个项目的会话，可以填用户任意一个项目的 ID。留空表示所有项目。"),
			},
		},
		Handler: handleThreadStatus,
	})

	r.Register(ToolDefinition{
		Name: stewardProjectsListToolName,
		Description: "列出用户已添加的全部项目，每条带 ID、名字与目录。挑项目之前先调用：用它判断需求属于哪个项目，" +
			"也给 steward_open_topic 的 project 参数取值。不要猜项目 ID 或项目目录。这个工具只在总管的会话里可用。",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
			"required":   []string{},
		},
		Handler: handleStewardProjectsList,
	})

	r.Register(ToolDefinition{
		Name: stewardPlanRequestToolName,
		Description: "把一个需要认真设计的任务交给宿主：宿主会用强模型在目标项目里派一个子代理，只允许它写方案文档，" +
			"完成后汇报自动送回本会话。只在用户亲自发消息的那一轮、且用户亲自要求出方案或明确同意出方案时调用；被事件唤醒的轮次（包括收到子代理汇报）" +
			"不要调用，改用 notify_owner 问用户要不要出方案。任何权限档下都可以用。" +
			"调用后子代理在后台运行，完成汇报会自动送回本会话，不要轮询。" +
			"收到子代理汇报后，用 notify_owner 把方案文档路径和结论告诉用户；方案落地前要用户确认，不要自己开工。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project": str("项目 ID、名字或目录。与需求相关的项目必须填；和任何项目都无关时留空，方案写进总管主目录。"),
				"goal":    str("要解决的问题或目标。"),
				"context": str("已知现象、约束、相关会话等补充信息。"),
			},
			"required": []string{"goal"},
		},
		IsWrite: true,
		Handler: handleStewardPlanRequest,
	})

	r.Register(ToolDefinition{
		Name: stewardAssignTaskToolName,
		Description: "把要动手的事交给宿主：宿主在目标项目里派一个可写子代理做完，完成后汇报自动送回本会话。" +
			"这是派子代理动手，不算你自己修改文件，任何权限档下都可以用。只在用户亲自发消息的那一轮调用；" +
			"被事件唤醒的轮次（包括收到子代理汇报）不要调用，改用 notify_owner 总结结果或询问用户。" +
			"task 与 context 里只写仓库相对路径，禁止写宿主主检出的绝对路径。一次只派一个，派完不要轮询。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project": str("项目 ID、名字或目录。先让 steward_projects_list 查一下，不要猜。"),
				"task": str(fmt.Sprintf("交给子代理的任务书：要改成什么样、验收要求，最多 %d 字。",
					stewardAssignTaskMaxRunes)),
				"context": str(fmt.Sprintf("已知现象、相关文件（写仓库相对路径）、约束。可选，最多 %d 字。",
					stewardAssignContextMaxRunes)),
				"write_paths": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
					"description": fmt.Sprintf("允许落地的仓库相对路径通配，例如 internal/steward/**；空表示不限，最多 %d 项。"+
						"绝对路径与以 .. 开头的项会被拒绝。", stewardAssignWritePathsMax),
				},
			},
			"required": []string{"project", "task"},
		},
		IsWrite: true,
		Handler: handleStewardAssignTask,
	})

	r.Register(ToolDefinition{
		Name: stewardLedgerGetToolName,
		Description: "读总管的账本，也就是这条对话的长期记忆，分三个区：in_progress（还在办的事）、decision（已定的决策）、" +
			"pending_user（等用户确认的问题）。返回每条记录及其 ID，以及对话换新后展示给模型的账本文本。" +
			"这个工具只在总管的会话里可用。",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
			"required":   []string{},
		},
		Handler: handleStewardLedgerGet,
	})

	r.Register(ToolDefinition{
		Name: stewardLedgerUpdateToolName,
		Description: "改动账本里的一条记录。对话过一段时间会换新，之后只剩账本、话题索引和最近几条消息，" +
			"所以不能丢的内容要记在这里。op=add 需要 section 与 text；op=update 需要 id 以及新的 section 或 text；" +
			"op=remove 需要 id。每条记录一行。这个工具只改总管自己的账本，且只在总管的会话里可用。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"op": map[string]any{
					"type":        "string",
					"enum":        []string{"add", "update", "remove"},
					"description": "要做什么：add 新增一条，update 改写这个 ID 的记录，remove 删掉它。",
				},
				"id": str("要改写或删除的记录 ID，取自 steward_ledger_get。add 不用填。"),
				"section": map[string]any{
					"type":        "string",
					"enum":        []string{"in_progress", "decision", "pending_user"},
					"description": "记录所在分区：in_progress 是还在办的事，decision 是已定的决策，pending_user 是等用户确认的问题。add 必填，update 可选。",
				},
				"text":            str(fmt.Sprintf("记录正文，一行，最多 %d 字。add 必填，update 可选。", stewardLedgerTextMaxRunes)),
				"topic_thread_id": str("记录所属的话题会话。可选，只在 add 时使用。"),
				"subject_type": str("等用户处理某个具体对象时填写（仅 pending_user 分区），取自 attention_list 里事件的对象类型" +
					"（thread、kanban_task、kanban_goal、automation_run、bee）；与 subject_id 一起填。"),
				"subject_id": str("等用户处理某个具体对象时填写（仅 pending_user 分区），取自 attention_list 里事件的对象 ID；" +
					"对象被处理后这条记录会自动关闭。"),
			},
			"required": []string{"op"},
		},
		IsWrite: true,
		Handler: handleStewardLedgerUpdate,
	})

	r.Register(ToolDefinition{
		Name: stewardNotifyOwnerToolName,
		Description: fmt.Sprintf("给用户发一条桌面通知，说清他现在就该知道的事，例如某个会话失败了、某个会话额度用完了、"+
			"某个任务等他拍板。通知是 IM 式短消息，不是报告：标题最多 %d 字，正文最多 %d 字，只写结论和用户要做的事。"+
			"总管会话和话题会话都能用这个工具。写完日报或完成汇报后，也发一条简短摘要通知，让用户知道去会话里看全文。",
			notifyOwnerTitleMaxRunes, notifyOwnerBodyMaxRunes),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":     str(fmt.Sprintf("一行说清发生了什么。最多 %d 字：只写结论。", notifyOwnerTitleMaxRunes)),
				"body":      str(fmt.Sprintf("用户需要知道什么、接下来做什么。最多 %d 字：只写结论与要做的事。", notifyOwnerBodyMaxRunes)),
				"thread_id": str("通知指向的会话，用户可以从通知跳回去。可选。"),
			},
			"required": []string{"title", "body"},
		},
		IsWrite: true,
		Handler: handleNotifyOwner,
	})

	// approval_suggest 不标 IsWrite：IsWrite 会把 request_id 当作幂等键，
	// 同一请求换个建议重提会被判为参数冲突，而这里要求重复建议覆盖旧建议。
	r.Register(ToolDefinition{
		Name: stewardApprovalSuggestToolName,
		Description: "对一个会话正在等用户审批的请求给出建议。建议不会被直接执行：它只展示给用户，" +
			"用户点一下确认后才生效。request_id 取自 thread_status 返回的 pendingRequests。" +
			"只支持 command、fileChange、tool 和 permissions 四类请求。对同一个请求再提一次建议会覆盖之前的建议。" +
			"这个工具只在总管的会话里可用。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"thread_id":  str("正在等待审批的会话 ID。"),
				"request_id": str("待审批请求的 ID，取自 thread_status 返回的 pendingRequests。"),
				"option_id": map[string]any{
					"type":        "string",
					"enum":        []string{"allow", "deny"},
					"description": "建议的答复：allow 是同意这个请求，deny 是拒绝。",
				},
				"reason": reason("给出这个答复"),
			},
			"required": []string{"thread_id", "request_id", "option_id", "reason"},
		},
		Handler: handleApprovalSuggest,
	})

	r.Register(ToolDefinition{
		Name: stewardThreadSendMessageToolName,
		Description: "给另一个会话发消息。消息会排队按序投递，所以不会打断它正在进行的那一轮：" +
			"它会在那一轮的下一步或结束时读到。用来催促看起来卡住的会话，或者等额度恢复后让它继续。" +
			"这个工具只在总管的会话里可用，且要求权限档允许催促与重跑。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"thread_id": str("要收消息的会话 ID。"),
				"text": str(fmt.Sprintf("要投递的消息正文。最多 %d 字；更长的内容拆成几条发。",
					stewardSendMessageTextMaxRunes)),
				"reason": reason("发送这条消息"),
			},
			"required": []string{"thread_id", "text", "reason"},
		},
		IsWrite: true,
		Handler: handleThreadSendMessage,
	})

	r.Register(ToolDefinition{
		Name: stewardThreadInterruptToolName,
		Description: "立刻打断一个会话正在做的事。只在会话明显卡住或明显在做错事时用，因为正在做的工作会丢失：" +
			"只需要催一下时优先用 thread_send_message。" +
			"这个工具只在总管的会话里可用，且要求权限档允许催促与重跑。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"thread_id": str("要打断的会话 ID。"),
				"reason":    reason("打断这个会话"),
			},
			"required": []string{"thread_id", "reason"},
		},
		IsWrite: true,
		Handler: handleThreadInterrupt,
	})

	r.Register(ToolDefinition{
		Name: stewardAutomationRunNowToolName,
		Description: "立刻跑一次某个自动化，返回本次运行的 runID。用在自动化因为提供方报错、额度限制这类临时原因失败之后补跑一次。" +
			"自动化保留自己的节奏，这次是额外的。这个工具只在总管的会话里可用，且要求权限档允许催促与重跑。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"automation_id": str("要运行的自动化 ID，取自 automation_list 的返回。"),
				"reason":        reason("立刻运行这个自动化"),
			},
			"required": []string{"automation_id", "reason"},
		},
		IsWrite:    true,
		Capability: CapAutomation,
		Handler:    handleAutomationRunNow,
	})

	r.Register(ToolDefinition{
		Name: stewardKanbanMoveTaskToolName,
		Description: "把一个看板任务移到另一个状态。todo 是重新排队执行，review 与 done 是移到对应列，rejected 是标记为不通过。" +
			"用来把失败的任务重新排队，或关掉已经不要的任务。" +
			"这个工具只在总管的会话里可用，且要求权限档允许催促与重跑。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": str("要移动的任务 ID。"),
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"todo", "review", "done", "rejected"},
					"description": "新状态：todo 重新排队执行，review 与 done 是移到对应列，rejected 是标记为不通过。",
				},
				"reason": reason("移动这个任务"),
			},
			"required": []string{"task_id", "status", "reason"},
		},
		IsWrite:    true,
		Capability: CapKanban,
		Handler:    handleKanbanMoveTask,
	})

	r.Register(ToolDefinition{
		Name: stewardOpenTopicToolName,
		Description: "为用户要做的事开一个话题：一个全新的会话，由它问用户最要紧的几个问题、写任务书，然后推进这件事。" +
			"一旦确定需求属于某个项目就调用，不要在这条对话里追问细节：追问交给话题会话。" +
			"返回新话题会话的 ID。这个工具只在总管的会话里可用。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project": str("需求所属的项目 ID 或项目目录。"),
				"title":   str(fmt.Sprintf("话题标题，简短，最多 %d 字。用用户自己的话。", stewardTopicTitleMaxRunes)),
				"request": str(fmt.Sprintf("用户的原话需求，连同他给出的背景。最多 %d 字。", stewardTopicRequestMaxRunes)),
			},
			"required": []string{"project", "title", "request"},
		},
		IsWrite: true,
		Handler: handleStewardOpenTopic,
	})

	r.Register(ToolDefinition{
		Name: stewardUpdateBriefToolName,
		Description: "整体写入本话题的任务书，并说明还剩几个待定问题。追问用户的每一轮结束都要调用，" +
			"任务书才会跟已经定下来的内容一致。任务书 7 节：目标；带 file:line 引用的现状；范围（做与不做）；" +
			"已定决策（按默认答案定下的要标出来）；每条标 [命令] [审阅] [你验] 的验收标准；约束；每条附推荐答案的待定问题。" +
			"这个工具只在话题会话里可用。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"brief": str(fmt.Sprintf("任务书全文（markdown），7 节齐全，最多 %d 字。它会整体替换上一版。", stewardBriefMaxRunes)),
				"open_questions": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"maximum":     stewardBriefMaxOpenQuestions,
					"description": "还有几个问题在等用户回答。0 表示任务书已经就绪：宿主会立刻开始规划，所以只有确实没问题时才传 0。",
				},
			},
			"required": []string{"brief", "open_questions"},
		},
		IsWrite: true,
		Handler: handleStewardUpdateBrief,
	})

	r.Register(ToolDefinition{
		Name: stewardStartGoalToolName,
		Description: "计划看着没问题时开工：把目标从计划待审推进到执行，任务开始跑。先自己审过拆分再调用；" +
			"有拿不准的地方就用 notify_owner 问用户，不要自己开工。" +
			"这个工具只在目标处于计划待审状态的话题会话里可用。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reason": reason("不等用户确认就开工"),
			},
			"required": []string{"reason"},
		},
		IsWrite: true,
		Handler: handleStewardStartGoal,
	})
}

func handleAttentionList(tc ToolContext, args map[string]any) ToolResult {
	hooks, guard := stewardGuard(tc, stewardAttentionListToolName)
	if guard != nil {
		return *guard
	}
	if hooks.ListAttention == nil {
		return stewardDisabled(stewardAttentionListToolName)
	}

	unresolvedOnly := true
	if raw, ok := args["unresolved_only"].(bool); ok {
		unresolvedOnly = raw
	}
	projectID, _ := args["project_id"].(string)
	limit := clampIntArg(args["limit"], attentionListDefaultLimit, 1, attentionListMaxLimit)

	items, err := hooks.ListAttention(tc.Context, unresolvedOnly, strings.TrimSpace(projectID), limit)
	if err != nil {
		return toolError(CodeInternal, "Failed to list attention items: "+err.Error(), true)
	}
	if items == nil {
		items = []AttentionItem{}
	}
	return toolSuccess(map[string]any{"count": len(items), "items": items})
}

func handleAttentionResolve(tc ToolContext, args map[string]any) ToolResult {
	hooks, guard := stewardGuard(tc, stewardAttentionResolveToolName)
	if guard != nil {
		return *guard
	}
	if hooks.ResolveAttention == nil {
		return stewardDisabled(stewardAttentionResolveToolName)
	}
	id, _ := args["id"].(string)
	id = strings.TrimSpace(id)
	if id == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'id' (the attention item from attention_list).", false)
	}

	if err := hooks.ResolveAttention(tc.Context, id); err != nil {
		return toolError(CodeInternal, "Failed to resolve attention item "+id+": "+err.Error(), true)
	}
	return toolSuccess("Attention item " + id + " is marked as handled.")
}

func handleThreadStatus(tc ToolContext, args map[string]any) ToolResult {
	hooks, guard := stewardGuard(tc, stewardThreadStatusToolName)
	if guard != nil {
		return *guard
	}
	if hooks.ThreadStatus == nil {
		return stewardDisabled(stewardThreadStatusToolName)
	}

	projectID, _ := args["project_id"].(string)
	items, err := hooks.ThreadStatus(tc.Context, strings.TrimSpace(projectID))
	if err != nil {
		return toolError(CodeInternal, "Failed to read thread status: "+err.Error(), true)
	}
	if items == nil {
		items = []ThreadStatusItem{}
	}
	return toolSuccess(map[string]any{"count": len(items), "threads": items})
}

// handleStewardProjectsList 返回用户已添加的全部项目，让总管先对照清单再决定项目。
func handleStewardProjectsList(tc ToolContext, _ map[string]any) ToolResult {
	hooks, guard := stewardGuard(tc, stewardProjectsListToolName)
	if guard != nil {
		return *guard
	}
	if hooks.ListProjects == nil {
		return stewardDisabled(stewardProjectsListToolName)
	}

	projects, err := hooks.ListProjects(tc.Context)
	if err != nil {
		return toolError(CodeInternal, "Failed to list projects: "+err.Error(), true)
	}
	if projects == nil {
		projects = []ProjectItem{}
	}
	return toolSuccess(map[string]any{"count": len(projects), "projects": projects})
}

// handleStewardPlanRequest 把需要认真设计的任务交给宿主：宿主派一个只写方案文档的子代理，
// 完成后汇报自动回到本会话。只有用户亲自发消息的那一轮才能调用，任何权限档下都允许。
func handleStewardPlanRequest(tc ToolContext, args map[string]any) ToolResult {
	hooks, guard := stewardGuard(tc, stewardPlanRequestToolName)
	if guard != nil {
		return *guard
	}
	goal, bad := stewardRequiredArg(args, "goal", "the problem to solve or the goal to reach")
	if bad != nil {
		return *bad
	}
	if hooks.PlanRequest == nil {
		return stewardDisabled(stewardPlanRequestToolName)
	}
	threadID := scopeThreadID(tc.Context)
	if hooks.TurnStartedByUser == nil || !hooks.TurnStartedByUser(threadID) {
		return toolError(CodeForbidden,
			"plan_request 只能在用户亲自发消息的那一轮调用；被事件唤醒或收到子代理汇报时，请用 notify_owner 询问用户是否需要出方案。",
			false)
	}
	project, _ := args["project"].(string)
	supplementary, _ := args["context"].(string)
	result, err := hooks.PlanRequest(tc.Context, threadID, PlanRequestInput{
		Project: strings.TrimSpace(project),
		Goal:    goal,
		Context: strings.TrimSpace(supplementary),
	})
	if err != nil {
		return toolError(CodeInternal, err.Error(), false)
	}
	return toolSuccess(fmt.Sprintf("已派出子代理 %s 在项目 %s 中出方案，方案文档写在 %s 下，完成后汇报会自动送回本会话。",
		result.BeeName, result.ProjectName, result.PlanDir))
}

// stewardAssignWritePaths 解析 write_paths：只接受仓库相对路径。绝对路径或以 .. 开头的项
// 会把改动写到主检出之外，因此直接按参数错误拒绝，钩子不会被调用。
func stewardAssignWritePaths(args map[string]any) ([]string, *ToolResult) {
	raw, ok := args["write_paths"]
	if !ok || raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		res := toolError(CodeInvalidArguments,
			"Argument 'write_paths' must be an array of repository-relative path patterns.", false)
		return nil, &res
	}
	if len(items) > stewardAssignWritePathsMax {
		res := toolError(CodeInvalidArguments, fmt.Sprintf(
			"Argument 'write_paths' has %d items, but at most %d are allowed. Narrow the task, then retry.",
			len(items), stewardAssignWritePathsMax), false)
		return nil, &res
	}
	paths := make([]string, 0, len(items))
	for _, item := range items {
		value, _ := item.(string)
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if filepath.IsAbs(value) || strings.HasPrefix(value, "..") {
			res := toolError(CodeInvalidArguments, fmt.Sprintf(
				"Argument 'write_paths' must use repository-relative paths, but got '%s'. "+
					"Absolute paths and paths starting with '..' are not allowed.", value), false)
			return nil, &res
		}
		paths = append(paths, value)
	}
	return paths, nil
}

// handleStewardAssignTask 把要动手的事交给宿主：宿主在目标项目里派一个可写子代理，
// 完成后汇报自动回到本会话。只有用户亲自发消息的那一轮才能调用，任何权限档下都允许；宿主没开编排时只返回软结果。
func handleStewardAssignTask(tc ToolContext, args map[string]any) ToolResult {
	hooks, guard := stewardGuard(tc, stewardAssignTaskToolName)
	if guard != nil {
		return *guard
	}
	project, bad := stewardRequiredArg(args, "project", "the project ID, name or directory the task belongs to")
	if bad != nil {
		return *bad
	}
	task, bad := stewardRequiredArg(args, "task", "what to change and how it will be accepted")
	if bad != nil {
		return *bad
	}
	if bad := stewardCheckMaxRunes("task", task, stewardAssignTaskMaxRunes); bad != nil {
		return *bad
	}
	supplementary, _ := args["context"].(string)
	supplementary = strings.TrimSpace(supplementary)
	if bad := stewardCheckMaxRunes("context", supplementary, stewardAssignContextMaxRunes); bad != nil {
		return *bad
	}
	writePaths, bad := stewardAssignWritePaths(args)
	if bad != nil {
		return *bad
	}
	if hooks.AssignTask == nil {
		return stewardDisabled(stewardAssignTaskToolName)
	}
	threadID := scopeThreadID(tc.Context)
	if hooks.TurnStartedByUser == nil || !hooks.TurnStartedByUser(threadID) {
		return toolError(CodeForbidden,
			"steward_assign_task 只能在用户亲自发消息的那一轮调用；被事件唤醒或收到子代理汇报时，请用 notify_owner 总结结果或询问用户。",
			false)
	}
	result, err := hooks.AssignTask(tc.Context, threadID, AssignTaskInput{
		Project:    project,
		Task:       task,
		Context:    supplementary,
		WritePaths: writePaths,
	})
	if err != nil {
		return toolError(CodeInternal, err.Error(), false)
	}
	if !result.Dispatched {
		message := strings.TrimSpace(result.Message)
		if message == "" {
			message = "宿主没有派出子代理，请你自己做。"
		}
		return toolSuccess(map[string]any{"ok": false, "dispatched": false, "message": message})
	}
	return toolSuccess(map[string]any{
		"ok":          true,
		"dispatched":  true,
		"beeName":     result.BeeName,
		"threadId":    result.ThreadID,
		"projectName": result.ProjectName,
		"workDir":     result.WorkDir,
		"message": fmt.Sprintf("已派出子代理 %s 在项目 %s 中执行，完成后汇报会自动送回本会话。",
			result.BeeName, result.ProjectName),
	})
}

// handleStewardLedgerGet 返回账本全部条目与渲染文本，只给总管主会话。
func handleStewardLedgerGet(tc ToolContext, _ map[string]any) ToolResult {
	hooks, guard := stewardGuard(tc, stewardLedgerGetToolName)
	if guard != nil {
		return *guard
	}
	if hooks.LedgerGet == nil {
		return stewardDisabled(stewardLedgerGetToolName)
	}
	items, rendered, err := hooks.LedgerGet(tc.Context)
	if err != nil {
		return toolError(CodeInternal, "Failed to read the ledger: "+err.Error(), true)
	}
	if items == nil {
		items = []LedgerItem{}
	}
	return toolSuccess(map[string]any{"count": len(items), "entries": items, "rendered": rendered})
}

// handleStewardLedgerUpdate 按 op 增、改、删一条账本记录，只给总管主会话。
// 钩子错误原样返回：分区已满、条目不存在这类中文说明要让总管直接读到。
func handleStewardLedgerUpdate(tc ToolContext, args map[string]any) ToolResult {
	hooks, guard := stewardGuard(tc, stewardLedgerUpdateToolName)
	if guard != nil {
		return *guard
	}
	op, bad := stewardRequiredArg(args, "op", "add, update or remove")
	if bad != nil {
		return *bad
	}
	if !stewardLedgerOps[op] {
		return toolError(CodeInvalidArguments, fmt.Sprintf(
			"Argument 'op' must be add, update or remove, but got '%s'.", op), false)
	}
	section, _ := args["section"].(string)
	section = strings.TrimSpace(section)
	if section != "" && !stewardLedgerSections[section] {
		return toolError(CodeInvalidArguments, fmt.Sprintf(
			"Argument 'section' must be in_progress, decision or pending_user, but got '%s'.", section), false)
	}
	text, _ := args["text"].(string)
	text = strings.TrimSpace(text)
	if bad := stewardCheckMaxRunes("text", text, stewardLedgerTextMaxRunes); bad != nil {
		return *bad
	}

	switch op {
	case "add":
		if hooks.LedgerAdd == nil {
			return stewardDisabled(stewardLedgerUpdateToolName)
		}
		if section == "" {
			return toolError(CodeInvalidArguments, "Missing required argument: 'section' (in_progress, decision or pending_user) for op=add.", false)
		}
		if text == "" {
			return toolError(CodeInvalidArguments, "Missing required argument: 'text' (the entry, one line) for op=add.", false)
		}
		topicThreadID, _ := args["topic_thread_id"].(string)
		subjectType, _ := args["subject_type"].(string)
		subjectID, _ := args["subject_id"].(string)
		subjectType = strings.TrimSpace(subjectType)
		subjectID = strings.TrimSpace(subjectID)
		if (subjectType == "") != (subjectID == "") {
			return toolError(CodeInvalidArguments, "Arguments 'subject_type' and 'subject_id' must be given together. If this entry waits for a specific object from attention_list, fill both; otherwise leave both out.", false)
		}
		item, err := hooks.LedgerAdd(tc.Context, section, text, strings.TrimSpace(topicThreadID), subjectType, subjectID)
		if err != nil {
			return toolError(CodeInternal, err.Error(), false)
		}
		return toolSuccess(map[string]any{"ok": true, "op": op, "entry": item})
	case "update":
		if hooks.LedgerEdit == nil {
			return stewardDisabled(stewardLedgerUpdateToolName)
		}
		id, bad := stewardRequiredArg(args, "id", "the entry from steward_ledger_get")
		if bad != nil {
			return *bad
		}
		if section == "" && text == "" {
			return toolError(CodeInvalidArguments, "op=update needs a new 'section' or 'text'.", false)
		}
		item, err := hooks.LedgerEdit(tc.Context, id, section, text)
		if err != nil {
			return toolError(CodeInternal, err.Error(), false)
		}
		return toolSuccess(map[string]any{"ok": true, "op": op, "entry": item})
	default:
		if hooks.LedgerRemove == nil {
			return stewardDisabled(stewardLedgerUpdateToolName)
		}
		id, bad := stewardRequiredArg(args, "id", "the entry from steward_ledger_get")
		if bad != nil {
			return *bad
		}
		if err := hooks.LedgerRemove(tc.Context, id); err != nil {
			return toolError(CodeInternal, err.Error(), false)
		}
		return toolSuccess(map[string]any{"ok": true, "op": op, "id": id})
	}
}

func handleNotifyOwner(tc ToolContext, args map[string]any) ToolResult {
	hooks, guard := stewardOwnerGuard(tc, stewardNotifyOwnerToolName)
	if guard != nil {
		return *guard
	}
	if hooks.NotifyOwner == nil {
		return stewardDisabled(stewardNotifyOwnerToolName)
	}
	title, _ := args["title"].(string)
	title = strings.TrimSpace(title)
	if title == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'title' (one short line naming what happened).", false)
	}
	body, _ := args["body"].(string)
	body = strings.TrimSpace(body)
	if body == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'body' (what the user needs to know).", false)
	}
	// 通知会被系统横幅直接展示，超长文本会被截断，因此按 rune 计数后要求精简重试。
	if n := utf8.RuneCountInString(title); n > notifyOwnerTitleMaxRunes {
		return toolError(CodeInvalidArguments, fmt.Sprintf(
			"Argument 'title' is %d characters long, but at most %d are allowed. Shorten it to one line, then retry.",
			n, notifyOwnerTitleMaxRunes), false)
	}
	if n := utf8.RuneCountInString(body); n > notifyOwnerBodyMaxRunes {
		return toolError(CodeInvalidArguments, fmt.Sprintf(
			"Argument 'body' is %d characters long, but at most %d are allowed. Condense it to what happened and what to do next, then retry.",
			n, notifyOwnerBodyMaxRunes), false)
	}

	threadID, _ := args["thread_id"].(string)
	if err := hooks.NotifyOwner(tc.Context, title, body, strings.TrimSpace(threadID)); err != nil {
		return toolError(CodeInternal, "Failed to notify the user: "+err.Error(), true)
	}
	return toolSuccess("The user was notified.")
}

// handleApprovalSuggest 的钩子错误与动作工具一样原样返回：宿主拒绝建议的理由要直接让总管读到。
func handleApprovalSuggest(tc ToolContext, args map[string]any) ToolResult {
	hooks, guard := stewardGuard(tc, stewardApprovalSuggestToolName)
	if guard != nil {
		return *guard
	}
	if hooks.SuggestApproval == nil {
		return stewardDisabled(stewardApprovalSuggestToolName)
	}
	threadID, bad := stewardRequiredArg(args, "thread_id", "the conversation that is waiting for approval")
	if bad != nil {
		return *bad
	}
	requestID, bad := stewardRequiredArg(args, "request_id", "the pending request from thread_status")
	if bad != nil {
		return *bad
	}
	optionID, bad := stewardRequiredArg(args, "option_id", "the suggested answer: allow or deny")
	if bad != nil {
		return *bad
	}
	if !stewardApprovalSuggestOptions[optionID] {
		return toolError(CodeInvalidArguments, fmt.Sprintf(
			"Argument 'option_id' must be allow or deny, but got '%s'.", optionID), false)
	}
	reason, bad := stewardRequiredArg(args, "reason", "why the steward is suggesting this answer")
	if bad != nil {
		return *bad
	}
	if bad := stewardCheckMaxRunes("reason", reason, stewardActionReasonMaxRunes); bad != nil {
		return *bad
	}

	if err := hooks.SuggestApproval(tc.Context, threadID, requestID, optionID, reason); err != nil {
		return toolError(CodeInternal, err.Error(), false)
	}
	return toolSuccess(map[string]any{"ok": true, "threadId": threadID, "requestId": requestID, "optionId": optionID})
}

// 以下四个动作工具的钩子错误原样作为工具错误返回：宿主护栏的拒绝理由就是通过它告诉总管的，
// 因此不加修饰性前缀，让模型读到的就是后端给的那句话。
func handleThreadSendMessage(tc ToolContext, args map[string]any) ToolResult {
	hooks, guard := stewardActionGuard(tc, stewardThreadSendMessageToolName)
	if guard != nil {
		return *guard
	}
	if hooks.SendMessage == nil {
		return stewardDisabled(stewardThreadSendMessageToolName)
	}
	threadID, bad := stewardRequiredArg(args, "thread_id", "the conversation to send the message to")
	if bad != nil {
		return *bad
	}
	text, bad := stewardRequiredArg(args, "text", "the message text to deliver")
	if bad != nil {
		return *bad
	}
	if bad := stewardCheckMaxRunes("text", text, stewardSendMessageTextMaxRunes); bad != nil {
		return *bad
	}
	reason, bad := stewardRequiredArg(args, "reason", "why the steward is sending this message")
	if bad != nil {
		return *bad
	}
	if bad := stewardCheckMaxRunes("reason", reason, stewardActionReasonMaxRunes); bad != nil {
		return *bad
	}
	if bad := stewardTopicScopeCheck(tc, hooks, "thread", threadID); bad != nil {
		return *bad
	}

	if err := hooks.SendMessage(tc.Context, threadID, text, reason); err != nil {
		return toolError(CodeInternal, err.Error(), false)
	}
	return toolSuccess("Message queued for conversation " + threadID + ". It is delivered in order without interrupting the running turn.")
}

func handleThreadInterrupt(tc ToolContext, args map[string]any) ToolResult {
	hooks, guard := stewardActionGuard(tc, stewardThreadInterruptToolName)
	if guard != nil {
		return *guard
	}
	if hooks.InterruptThread == nil {
		return stewardDisabled(stewardThreadInterruptToolName)
	}
	threadID, bad := stewardRequiredArg(args, "thread_id", "the conversation to interrupt")
	if bad != nil {
		return *bad
	}
	reason, bad := stewardRequiredArg(args, "reason", "why the steward is interrupting this conversation")
	if bad != nil {
		return *bad
	}
	if bad := stewardCheckMaxRunes("reason", reason, stewardActionReasonMaxRunes); bad != nil {
		return *bad
	}
	if bad := stewardTopicScopeCheck(tc, hooks, "thread", threadID); bad != nil {
		return *bad
	}

	if err := hooks.InterruptThread(tc.Context, threadID, reason); err != nil {
		return toolError(CodeInternal, err.Error(), false)
	}
	return toolSuccess("Conversation " + threadID + " was interrupted.")
}

func handleAutomationRunNow(tc ToolContext, args map[string]any) ToolResult {
	hooks, guard := stewardActionGuard(tc, stewardAutomationRunNowToolName)
	if guard != nil {
		return *guard
	}
	if hooks.RunAutomation == nil {
		return stewardDisabled(stewardAutomationRunNowToolName)
	}
	automationID, bad := stewardRequiredArg(args, "automation_id", "the automation to run now")
	if bad != nil {
		return *bad
	}
	reason, bad := stewardRequiredArg(args, "reason", "why the steward is running this automation now")
	if bad != nil {
		return *bad
	}
	if bad := stewardCheckMaxRunes("reason", reason, stewardActionReasonMaxRunes); bad != nil {
		return *bad
	}

	runID, err := hooks.RunAutomation(tc.Context, automationID, reason)
	if err != nil {
		return toolError(CodeInternal, err.Error(), false)
	}
	return toolSuccess(map[string]any{"automationId": automationID, "runId": runID})
}

func handleKanbanMoveTask(tc ToolContext, args map[string]any) ToolResult {
	hooks, guard := stewardActionGuard(tc, stewardKanbanMoveTaskToolName)
	if guard != nil {
		return *guard
	}
	if hooks.MoveKanbanTask == nil {
		return stewardDisabled(stewardKanbanMoveTaskToolName)
	}
	taskID, bad := stewardRequiredArg(args, "task_id", "the task to move")
	if bad != nil {
		return *bad
	}
	if bad := stewardTopicScopeCheck(tc, hooks, "kanban_task", taskID); bad != nil {
		return *bad
	}
	status, bad := stewardRequiredArg(args, "status", "the status to move the task to: todo, review, done or rejected")
	if bad != nil {
		return *bad
	}
	if !stewardKanbanMoveStatuses[status] {
		return toolError(CodeInvalidArguments, fmt.Sprintf(
			"Argument 'status' must be one of todo, review, done or rejected, but got '%s'. Use todo to requeue the task so it runs again.",
			status), false)
	}
	reason, bad := stewardRequiredArg(args, "reason", "why the steward is moving this task")
	if bad != nil {
		return *bad
	}
	if bad := stewardCheckMaxRunes("reason", reason, stewardActionReasonMaxRunes); bad != nil {
		return *bad
	}

	if err := hooks.MoveKanbanTask(tc.Context, taskID, status, reason); err != nil {
		return toolError(CodeInternal, err.Error(), false)
	}
	return toolSuccess("Task " + taskID + " moved to " + status + ".")
}

// handleStewardOpenTopic 只给总管主会话：把用户的需求转成一个话题会话，主对话不再追问细节。
// project 允许写项目 ID 或目录，解析成项目根目录交给钩子，工具层不重复实现项目查找。
func handleStewardOpenTopic(tc ToolContext, args map[string]any) ToolResult {
	hooks, guard := stewardGuard(tc, stewardOpenTopicToolName)
	if guard != nil {
		return *guard
	}
	if hooks.OpenTopic == nil {
		return stewardDisabled(stewardOpenTopicToolName)
	}
	project, bad := stewardRequiredArg(args, "project", "the project ID or directory the request belongs to")
	if bad != nil {
		return *bad
	}
	request, bad := stewardRequiredArg(args, "request", "what the user asked for, in their own words")
	if bad != nil {
		return *bad
	}
	if bad := stewardCheckMaxRunes("request", request, stewardTopicRequestMaxRunes); bad != nil {
		return *bad
	}
	title, _ := args["title"].(string)
	title = strings.TrimSpace(title)
	if bad := stewardCheckMaxRunes("title", title, stewardTopicTitleMaxRunes); bad != nil {
		return *bad
	}

	threadID, err := hooks.OpenTopic(tc.Context, project, title, request)
	if err != nil {
		return toolError(CodeInternal, "Failed to open the topic: "+err.Error(), true)
	}
	return toolSuccess(map[string]any{
		"ok":       true,
		"threadId": threadID,
		"message":  "已开话题，追问与任务书都在这个会话里进行。",
	})
}

// handleStewardUpdateBrief 只给话题会话：每轮结束都把任务书全文与待定问题条数写回去。
// open_questions 必须显式给出：缺省成 0 会误触发“任务书已就绪”的提示。
func handleStewardUpdateBrief(tc ToolContext, args map[string]any) ToolResult {
	hooks, guard := stewardTopicGuard(tc, stewardUpdateBriefToolName)
	if guard != nil {
		return *guard
	}
	if hooks.UpdateBrief == nil {
		return stewardDisabled(stewardUpdateBriefToolName)
	}
	brief, bad := stewardRequiredArg(args, "brief", "the whole brief in markdown, all seven sections")
	if bad != nil {
		return *bad
	}
	if bad := stewardCheckMaxRunes("brief", brief, stewardBriefMaxRunes); bad != nil {
		return *bad
	}
	raw, ok := args["open_questions"]
	if !ok {
		return toolError(CodeInvalidArguments,
			"Missing required argument: 'open_questions' (how many questions are still waiting for the user).", false)
	}
	openQuestions := clampIntArg(raw, 0, 0, stewardBriefMaxOpenQuestions)

	if err := hooks.UpdateBrief(tc.Context, scopeThreadID(tc.Context), brief, openQuestions); err != nil {
		return toolError(CodeInternal, "Failed to update the brief: "+err.Error(), true)
	}
	result := map[string]any{"ok": true, "openQuestions": openQuestions}
	// 待定问题清零即自动开工：总管不必再停下来等用户点确认。
	// 开工失败不算工具失败（话题已经开工、任务书为空等），把原错误文案交给总管看。
	if openQuestions == 0 && hooks.ConfirmTopic != nil {
		if err := hooks.ConfirmTopic(tc.Context, scopeThreadID(tc.Context)); err != nil {
			result["started"] = false
			result["note"] = err.Error()
			return toolSuccess(result)
		}
		result["started"] = true
		result["message"] = "任务书已就绪，宿主已开始规划"
	}
	return toolSuccess(result)
}

// handleStewardStartGoal 只给话题会话：自审计划没问题后直接开工，拿不准就该先问用户。
func handleStewardStartGoal(tc ToolContext, args map[string]any) ToolResult {
	hooks, guard := stewardTopicGuard(tc, stewardStartGoalToolName)
	if guard != nil {
		return *guard
	}
	if hooks.StartTopicGoal == nil {
		return stewardDisabled(stewardStartGoalToolName)
	}
	reason, bad := stewardRequiredArg(args, "reason", "why the plan can start without asking the user")
	if bad != nil {
		return *bad
	}
	if bad := stewardCheckMaxRunes("reason", reason, stewardActionReasonMaxRunes); bad != nil {
		return *bad
	}

	if err := hooks.StartTopicGoal(tc.Context, scopeThreadID(tc.Context), reason); err != nil {
		return toolError(CodeInternal, "Failed to start the goal: "+err.Error(), false)
	}
	return toolSuccess("The goal is running now.")
}
