package wire

// StewardTopicsListParams 对应 steward.topics.list 请求参数。
type StewardTopicsListParams struct {
	// ProjectID 为空表示列出全部项目的话题。
	ProjectID string `json:"projectID,omitempty"`
}

// StewardTopicItem 是远程只读话题列表里的一项，阶段由关联目标的状态推出。
type StewardTopicItem struct {
	ThreadID      string `json:"threadID"`
	ProjectID     string `json:"projectID"`
	Title         string `json:"title"`
	Brief         string `json:"brief"`
	OpenQuestions int    `json:"openQuestions"`
	GoalID        string `json:"goalID"`
	Closed        bool   `json:"closed"`
	UpdatedAt     string `json:"updatedAt"`
	Phase         string `json:"phase"`
	TasksDone     int    `json:"tasksDone"`
	TasksTotal    int    `json:"tasksTotal"`
}

// StewardTopicsListResult 对应 steward.topics.list 成功返回，话题按更新时间倒序。
type StewardTopicsListResult struct {
	Topics []StewardTopicItem `json:"topics"`
}

// StewardOpenParams 对应 steward.open 请求参数：projectID 用于让 web 端声明这是哪个项目的入口。
type StewardOpenParams struct {
	ProjectID string `json:"projectID"`
}

// StewardOpenResult 对应 steward.open 成功返回，threadID 是总管主会话。
type StewardOpenResult struct {
	ThreadID string `json:"threadID"`
}

// StewardTopicsStartParams 对应 steward.topics.start 请求参数。
type StewardTopicsStartParams struct {
	ProjectID string `json:"projectID"`
	// ThreadID 是提出需求的会话，可为空；非空时必须是该项目下的总管话题。
	ThreadID string `json:"threadID,omitempty"`
	Request  string `json:"request"`
}

// StewardTopicsStartResult 对应 steward.topics.start 成功返回。
type StewardTopicsStartResult struct {
	ThreadID string `json:"threadID"`
	// Queued 表示需求是否只被受理、还要排队投递；当前话题由桌面端同步创建，恒为 false。
	Queued bool `json:"queued"`
}

// StewardTopicParams 是只带话题定位的请求参数，replan/confirm/close/delete 共用。
type StewardTopicParams struct {
	ProjectID string `json:"projectID"`
	ThreadID  string `json:"threadID"`
}

// StewardTopicTaskReviewParams 对应 steward.topics.tasks.review 请求参数。
type StewardTopicTaskReviewParams struct {
	ProjectID string `json:"projectID"`
	ThreadID  string `json:"threadID"`
	TaskID    string `json:"taskID"`
	// Decision 只接受 accept 与 reject。
	Decision string `json:"decision"`
	// Feedback 是打回理由，decision 为 reject 时必填。
	Feedback string `json:"feedback,omitempty"`
	// Revision 是客户端看到的任务修订号，用于乐观锁；与后端不一致时返回版本冲突。
	Revision int `json:"revision"`
}

// StewardTopicsActionResult 是话题写操作的成功返回（空对象）。
type StewardTopicsActionResult struct{}

// StewardExtChanged 是 steward.changed 通知的载荷；projectID 为空表示全部项目。
type StewardExtChanged struct {
	ProjectID string `json:"projectID"`
}
