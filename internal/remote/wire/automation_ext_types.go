package wire

// 本文件定义自动化（automations.*）远程扩展方法的消息类型。
// 桌面端同源结构见 internal/ui/client.go 的 Automation、AutomationInput 与 AutomationRun，
// 已读/归档状态来自 mcodeui.AutomationRunsState。
// 查看、手动运行、已读与归档之外，写操作（create/update/delete/setEnabled/reset）的
// 入参只覆盖桌面端表单能改的字段：不包含运行时状态（status/nextRunAt/revision 等），
// 也不允许通过 update 改 projectID。

// AutomationExtSchedule 描述自动化的调度规则，字段与桌面端 AutomationSchedule 一一对应。
type AutomationExtSchedule struct {
	// Type 是调度类型：interval/daily/weekdays/weekly/cron/once，空表示未设置调度。
	Type string `json:"type"`
	// Seconds 是 interval 调度的间隔秒数。
	Seconds int `json:"seconds,omitempty"`
	// Date 是 once 调度的执行时间（RFC3339）。
	Date string `json:"date,omitempty"`
	// TimeOfDay 是 daily/weekdays/weekly 调度的执行时刻，形如 09:00。
	TimeOfDay string `json:"timeOfDay,omitempty"`
	// Timezone 是调度使用的时区名。
	Timezone string `json:"timezone,omitempty"`
	// DayOfWeek 是 weekly 调度的星期（0 为周日）。
	DayOfWeek int `json:"dayOfWeek,omitempty"`
	// Expression 是 cron 调度的表达式。
	Expression string `json:"expression,omitempty"`
}

type AutomationExtPreviewParams struct {
	Schedule AutomationExtSchedule `json:"schedule"`
	After    *string               `json:"after,omitempty"`
	Count    *int                  `json:"count,omitempty"`
}

type AutomationExtPreviewResult struct {
	Times     []string `json:"times"`
	Exhausted bool     `json:"exhausted"`
}

// AutomationExtAutomation 是自动化列表项，供客户端直接渲染与回填编辑表单。
type AutomationExtAutomation struct {
	ID string `json:"id"`
	// ProjectID 为空表示该自动化不属于任何项目。
	ProjectID string `json:"projectID,omitempty"`
	Name      string `json:"name"`
	// Prompt 是发给模型的任务提示词，StopWhen 是停止条件；远程编辑表单需要它们回填。
	Prompt   string `json:"prompt"`
	StopWhen string `json:"stopWhen,omitempty"`
	// Schedule 是结构化调度规则，ScheduleText 是它的中文描述（如「每 60 分钟」）。
	Schedule     AutomationExtSchedule `json:"schedule"`
	ScheduleText string                `json:"scheduleText"`
	// Enabled 表示是否启用；Status 是桌面端的运行状态（running/paused/stopped 等）。
	Enabled bool   `json:"enabled"`
	Status  string `json:"status,omitempty"`
	// LastRunAt 是上次运行时间（RFC3339），零值省略。
	LastRunAt string `json:"lastRunAt,omitempty"`
	// LastRunStatus 是上次运行结果（succeeded/failed 等）。
	LastRunStatus string `json:"lastRunStatus,omitempty"`
	// NextRunAt 是下一次计划运行时间（RFC3339），零值省略。
	NextRunAt string `json:"nextRunAt,omitempty"`
	// UnreadCount 是该项目下未读运行记录数。
	UnreadCount int `json:"unreadCount"`
	// ConsecutiveFailures 是当前连续失败次数，非零时桌面端会显示「重置」入口。
	ConsecutiveFailures int `json:"consecutiveFailures"`
	// MaxConsecutiveFailures 与 MaxIterations 是失败上限与最大运行次数（0 表示不限制）。
	MaxConsecutiveFailures int `json:"maxConsecutiveFailures"`
	// MaxIterations 是旧字段，仅为兼容旧客户端保留；新客户端读 MaxRuns。
	MaxIterations int `json:"maxIterations"`
	// MaxRuns 是规范的最大运行次数（0 表示不限制），CatchUp 表示错过触发点时是否补跑。
	MaxRuns int  `json:"maxRuns"`
	CatchUp bool `json:"catchUp"`
	// Provider/Model/Effort 是每次运行新建会话使用的供应商、模型与推理强度，空表示跟随默认。
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	// RuntimeMode 是新建会话的权限模式（readonly/supervised/autoAcceptEdits/auto/fullAccess），空表示跟随默认。
	RuntimeMode string `json:"runtimeMode,omitempty"`
	// RunMode 是运行方式（standalone/dedicated/heartbeat）；heartbeat 投递到已有会话，上面四项不生效。
	RunMode string `json:"runMode,omitempty"`
	// Revision 是乐观并发版本号，update/setEnabled/reset 需要原样回传。
	Revision int `json:"revision"`
}

// AutomationExtListParams 是 automations.list 的参数。
type AutomationExtListParams struct {
	// ProjectID 限定项目；为空表示列出全部项目的自动化。
	ProjectID string `json:"projectID"`
}

// AutomationExtListResult 是 automations.list 的结果。
type AutomationExtListResult struct {
	Automations []AutomationExtAutomation `json:"automations"`
	// UnreadCounts 是 automationID -> 未读运行记录数，只包含有未读记录的自动化。
	UnreadCounts map[string]int `json:"unreadCounts,omitempty"`
}

// AutomationExtRun 是一条自动化运行记录。
type AutomationExtRun struct {
	ID           string `json:"id"`
	AutomationID string `json:"automationID"`
	// Status 是运行状态：running/succeeded/failed/stopped 等。
	Status string `json:"status"`
	// StartedAt 是开始时间（RFC3339），FinishedAt 在运行未结束时省略。
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
	// ThreadID 是本次运行产生的会话；为空表示没有对应会话。
	ThreadID string `json:"threadID,omitempty"`
	// Read 为 false 表示用户还没看过这条运行记录，Archived 表示已归档。
	Read     bool `json:"read"`
	Archived bool `json:"archived"`
	// Summary 是运行结果摘要，Error 是失败原因，两者都可能为空。
	Summary string `json:"summary,omitempty"`
	Error   string `json:"error,omitempty"`
	// Trigger 是运行来源（scheduled/catch_up/manual），MissedCount 是补跑折叠的错过触发点数量。
	Trigger     string `json:"trigger"`
	MissedCount int    `json:"missedCount"`
}

// AutomationExtRunsParams 是 automations.runs 的参数。
type AutomationExtRunsParams struct {
	AutomationID string `json:"automationID"`
	// Limit 是返回条数，省略时取默认值，超过上限按上限返回。
	Limit *int `json:"limit,omitempty"`
	// IncludeArchived 为 true 时一并返回已归档的运行记录。
	IncludeArchived bool `json:"includeArchived,omitempty"`
}

// AutomationExtRunsResult 是 automations.runs 的结果。
type AutomationExtRunsResult struct {
	AutomationID string             `json:"automationID"`
	Runs         []AutomationExtRun `json:"runs"`
}

// AutomationExtRunParams 是 automations.run 的参数。
type AutomationExtRunParams struct {
	AutomationID string `json:"automationID"`
}

// AutomationExtRunResult 是 automations.run 的结果，Run 是本次新建的运行记录。
type AutomationExtRunResult struct {
	Run AutomationExtRun `json:"run"`
}

// AutomationExtMarkReadParams 是 automations.markRead 的参数。
type AutomationExtMarkReadParams struct {
	RunID string `json:"runID"`
}

// AutomationExtMarkReadResult 是 automations.markRead 的结果。
type AutomationExtMarkReadResult struct {
	RunID string `json:"runID"`
}

// AutomationExtArchiveRunParams 是 automations.archiveRun 的参数。
type AutomationExtArchiveRunParams struct {
	RunID string `json:"runID"`
	// Archived 为 true 表示归档，false 表示取消归档。
	Archived bool `json:"archived"`
}

// AutomationExtArchiveRunResult 是 automations.archiveRun 的结果。
type AutomationExtArchiveRunResult struct {
	RunID    string `json:"runID"`
	Archived bool   `json:"archived"`
}

// AutomationExtInput 是 automations.create / automations.update 共用的可写字段。
// 与桌面端 AutomationInput 对齐：projectID 只在 create 的外层参数里给，update 不允许改项目；
// 运行时状态（status、nextRunAt、consecutiveFailures、revision）都不可写。
type AutomationExtInput struct {
	Name string `json:"name"`
	// Prompt 是任务提示词，StopWhen 是停止条件（可空）。
	Prompt   string `json:"prompt"`
	StopWhen string `json:"stopWhen,omitempty"`
	// Schedule 是调度规则；Enabled 决定保存后是否立即启用。
	Schedule AutomationExtSchedule `json:"schedule"`
	Enabled  bool                  `json:"enabled,omitempty"`
	// MaxConsecutiveFailures 省略时按桌面端默认值 3；MaxIterations 省略按 0（不限制）。
	MaxConsecutiveFailures *int `json:"maxConsecutiveFailures,omitempty"`
	// MaxIterations 是旧字段，仅为兼容旧客户端保留；与新字段同时出现时必须一致。
	MaxIterations *int `json:"maxIterations,omitempty"`
	// MaxRuns 是规范的最大运行次数（0..1000，0 表示不限制）；CatchUp 省略时按开启处理。
	MaxRuns *int  `json:"maxRuns,omitempty"`
	CatchUp *bool `json:"catchUp,omitempty"`
	// Provider/Model/Effort/RuntimeMode 是每次运行新建会话的模型与权限设置。
	// 指针为空表示不改（create 时即跟随默认）；传空串表示恢复默认。
	// heartbeat 运行方式投递到已有会话，这四项不生效。
	Provider    *string `json:"provider,omitempty"`
	Model       *string `json:"model,omitempty"`
	Effort      *string `json:"effort,omitempty"`
	RuntimeMode *string `json:"runtimeMode,omitempty"`
}

// AutomationExtCreateParams 是 automations.create 的参数。
type AutomationExtCreateParams struct {
	// ProjectID 是新建自动化所属的项目，必须是当前可见的项目。
	ProjectID string             `json:"projectID"`
	Input     AutomationExtInput `json:"input"`
}

// AutomationExtAutomationResult 是写操作返回单个自动化时的结果，
// automations.create / automations.update / automations.setEnabled / automations.reset 共用。
type AutomationExtAutomationResult struct {
	Automation AutomationExtAutomation `json:"automation"`
}

// AutomationExtUpdateParams 是 automations.update 的参数。
type AutomationExtUpdateParams struct {
	AutomationID string             `json:"automationID"`
	Input        AutomationExtInput `json:"input"`
	// Revision 是客户端读到的版本号；省略或 0 表示不校验并发（按最新版本覆盖）。
	Revision int `json:"revision,omitempty"`
}

// AutomationExtDeleteParams 是 automations.delete 的参数。
// 桌面端删除有二次确认，远程同样要求显式传 confirm=true。
type AutomationExtDeleteParams struct {
	AutomationID string `json:"automationID"`
	Confirm      bool   `json:"confirm"`
}

// AutomationExtDeleteResult 是 automations.delete 的结果。
type AutomationExtDeleteResult struct {
	AutomationID string `json:"automationID"`
	Deleted      bool   `json:"deleted"`
}

// AutomationExtSetEnabledParams 是 automations.setEnabled 的参数（暂停/恢复）。
type AutomationExtSetEnabledParams struct {
	AutomationID string `json:"automationID"`
	// Enabled 为 true 时同时清空连续失败计数，与桌面端「恢复」一致。
	Enabled  bool `json:"enabled"`
	Revision int  `json:"revision,omitempty"`
}

// AutomationExtResetParams 是 automations.reset 的参数。
// 桌面端的「重置」也走同一个更新调用：启用自动化并清空连续失败计数，所以没有额外字段。
type AutomationExtResetParams struct {
	AutomationID string `json:"automationID"`
	Revision     int    `json:"revision,omitempty"`
}

// AutomationExtChanged 是 automations.changed 通知的载荷。
// 自动化级操作带 AutomationID，运行级操作（已读/归档）只能拿到 RunID：
// 存储层没有按 runID 反查 automationID 的接口，客户端收到任一字段都应刷新即可。
type AutomationExtChanged struct {
	AutomationID string `json:"automationID,omitempty"`
	RunID        string `json:"runID,omitempty"`
}
