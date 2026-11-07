package mcodeui

import (
	"context"
	"time"
)

// StewardTopic 是总管为一条需求开的话题：追问、任务书、规划与执行都在同一个话题会话里。
// 本文件只定义数据类型与接口，渲染留给话题卡片自己实现。
type StewardTopic struct {
	// ThreadID 是话题会话 ID，也是确认开工时 Goal 的 PlanThreadID。
	ThreadID string
	// ProjectID 与 ProjectName 说明这条需求属于哪个项目。
	ProjectID   string
	ProjectName string
	// Title 是用户给这件事起的短名，Brief 是当前的任务书全文（可能还没写）。
	Title string
	Brief string
	// OpenQuestions 是任务书里还没定下来的问题条数；为 0 且 Brief 非空时可以确认开工。
	OpenQuestions int
	// Phase 是话题当前阶段：drafting、planning、plan_review、running、paused、completed、cancelled。
	Phase string
	// GoalID 是确认开工后建出的看板目标；为空表示还在追问阶段。
	GoalID string
	// TasksDone 与 TasksTotal 是目标下已完成与全部的任务数，没有目标时都是 0。
	TasksDone  int
	TasksTotal int
	// PendingUser 是等用户拍板的事数：待验收的任务、就绪待确认的任务书、待确认的结项。
	PendingUser int
	// Closed 表示用户已关闭这条话题，界面不再提示它待处理。
	Closed bool
	// UpdatedAt 是最后一次变化的时刻，话题列表按它倒序。
	UpdatedAt time.Time
}

// 话题阶段取值，与 internal/steward 的阶段常量保持一致。
const (
	StewardTopicDrafting   = "drafting"
	StewardTopicPlanning   = "planning"
	StewardTopicPlanFailed = "plan_failed"
	StewardTopicPlanReview = "plan_review"
	StewardTopicRunning    = "running"
	StewardTopicPaused     = "paused"
	StewardTopicCompleted  = "completed"
	StewardTopicCancelled  = "cancelled"
)

// StewardTopicsClient 是话题列表与话题操作的可选客户端能力：与 StewardClient 一样通过类型断言取用，
// 旧客户端与测试替身可以不实现，界面据此隐藏话题卡片。
type StewardTopicsClient interface {
	// StewardTopics 返回全部话题，按最近变化倒序；未启用总管时返回空列表。
	StewardTopics(ctx context.Context) ([]StewardTopic, error)
	// StartStewardTopic 为用户的一句需求开话题，返回话题会话 ID。
	StartStewardTopic(ctx context.Context, projectID, request string) (string, error)
	// ConfirmStewardTopic 是任务书卡片上的「直接开工」，模型没自动开工或用户想提前开工时的手动兜底。
	ConfirmStewardTopic(ctx context.Context, threadID string) error
	// CloseStewardTopic 关闭话题，之后不再计入待处理。
	CloseStewardTopic(ctx context.Context, threadID string) error
	// DeleteStewardTopic 删除话题：会话、话题记录与账本条目一并移除，无法恢复。
	DeleteStewardTopic(ctx context.Context, threadID string) error
	// ReplanStewardTopic 规划失败后重新规划：保留任务书，让总管再出一次计划。
	ReplanStewardTopic(ctx context.Context, threadID string) error
	// StartStewardTopicGoal 让 plan_review 的话题跳过等审直接开工：计划本该由总管自审后开工，
	// 这里只是让用户能手动补上这一步，防止总管漏掉自审时话题卡住。
	StartStewardTopicGoal(ctx context.Context, threadID string) error
	// WatchStewardTopics 在话题或看板变化时回调 fn，返回取消订阅的函数。
	WatchStewardTopics(fn func()) (cancel func())
}
