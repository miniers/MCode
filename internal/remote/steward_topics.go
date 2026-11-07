package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// 话题只读列表的远程扩展方法（steward.topics.list）。
// 话题表是宿主设置里的一个 JSON 数组；这里只读取，不依赖 internal/steward，避免循环依赖。

const stewardTopicsListMethod = "steward.topics.list"

// 话题阶段取值与 internal/steward 的同名字符串保持一致，但这里独立定义映射，
// 只依赖目标状态字符串，不引入 steward 包。
const (
	stewardPhaseDrafting   = "drafting"
	stewardPhasePlanning   = "planning"
	stewardPhasePlanFailed = "plan_failed"
	stewardPhasePlanReview = "plan_review"
	stewardPhaseRunning    = "running"
	stewardPhasePaused     = "paused"
	stewardPhaseCompleted  = "completed"
	stewardPhaseCancelled  = "cancelled"
)

// stewardTopicSetting 是话题表里本方法关心的字段；其余字段解析时忽略。
type stewardTopicSetting struct {
	ThreadID      string    `json:"threadId"`
	ProjectID     string    `json:"projectId"`
	Title         string    `json:"title"`
	Brief         string    `json:"brief"`
	OpenQuestions int       `json:"openQuestions"`
	GoalID        string    `json:"goalId"`
	Closed        bool      `json:"closed"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// 总管的写操作扩展方法（steward.open 与话题类方法）与 steward.changed 推送。
// 处理函数只负责参数校验、归属校验与透传，真正的动作由 app 层注入的 stewardExtService 完成。
const (
	stewardExtServiceName   = "steward"
	stewardExtChangedMethod = "steward.changed"

	stewardOpenMethod          = "steward.open"
	stewardTopicsStartMethod   = "steward.topics.start"
	stewardTopicsReplanMethod  = "steward.topics.replan"
	stewardTopicsConfirmMethod = "steward.topics.confirm"
	stewardTopicsCloseMethod   = "steward.topics.close"
	stewardTopicsDeleteMethod  = "steward.topics.delete"
	stewardTopicsReviewMethod  = "steward.topics.tasks.review"
)

// 评审决定取值与看板的 ReviewDecision 一致。
const (
	stewardReviewAccept = "accept"
	stewardReviewReject = "reject"
)

// ErrStewardRevisionConflict 表示任务修订号已过期：app 层注入的服务碰到 store.ErrRevisionConflict 时
// 用这个哨兵包一层，远程层据此回复版本冲突错误码，web 端可以提示用户刷新后重试。
var ErrStewardRevisionConflict = errors.New("steward: revision conflict")

// stewardExtService 是总管写操作的能力集合，由 internal/app 注入实现。
type stewardExtService interface {
	Open(ctx context.Context, projectID string) (string, error)
	StartTopic(ctx context.Context, projectID, threadID, request string) (string, error)
	ReplanTopic(ctx context.Context, threadID string) error
	ConfirmTopic(ctx context.Context, threadID string) error
	CloseTopic(ctx context.Context, threadID string) error
	DeleteTopic(ctx context.Context, threadID string) error
	ReviewTask(ctx context.Context, threadID, taskID, decision, feedback string, revision int) error
}

func init() {
	RegisterMethod(stewardTopicsListMethod, handleStewardTopicsList)
	RegisterMethod(stewardOpenMethod, handleStewardOpen)
	RegisterMethod(stewardTopicsStartMethod, handleStewardTopicsStart)
	RegisterMethod(stewardTopicsReplanMethod, handleStewardTopicReplan)
	RegisterMethod(stewardTopicsConfirmMethod, handleStewardTopicConfirm)
	RegisterMethod(stewardTopicsCloseMethod, handleStewardTopicClose)
	RegisterMethod(stewardTopicsDeleteMethod, handleStewardTopicDelete)
	RegisterMethod(stewardTopicsReviewMethod, handleStewardTopicTaskReview)
}

// stewardExtServiceOf 取出注入的总管服务；未注入时返回 false，由调用方回复 MethodNotFound。
func stewardExtServiceOf(d *Dispatcher) (stewardExtService, bool) {
	if d == nil {
		return nil, false
	}
	return backendService[stewardExtService](d.backend, stewardExtServiceName)
}

// NotifyStewardChanged 向所有已认证连接广播 steward.changed；projectID 为空表示全部项目。
func (d *Dispatcher) NotifyStewardChanged(projectID string) {
	if d == nil {
		return
	}
	d.NotifyAuthed(stewardExtChangedMethod, wire.StewardExtChanged{ProjectID: strings.TrimSpace(projectID)})
}

// handleStewardOpen 返回总管主会话（没有就新建），对应桌面端「找总管」入口。
func handleStewardOpen(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.StewardOpenParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(p.ProjectID)
	if projectID == "" {
		return nil, ExtInvalidParams("Invalid params: 'projectID' is required")
	}
	svc, ok := stewardExtServiceOf(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	threadID, err := svc.Open(ctx, projectID)
	if err != nil {
		return nil, stewardExtWrapError(err)
	}
	return wire.StewardOpenResult{ThreadID: threadID}, nil
}

// handleStewardTopicsStart 用一句需求开新话题，对应桌面端的「新开话题」。
func handleStewardTopicsStart(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.StewardTopicsStartParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(p.ProjectID)
	threadID := strings.TrimSpace(p.ThreadID)
	request := strings.TrimSpace(p.Request)
	if projectID == "" || request == "" {
		return nil, ExtInvalidParams("Invalid params: 'projectID' and 'request' are required")
	}
	svc, ok := stewardExtServiceOf(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	if threadID != "" {
		if err := stewardExtRequireTopic(ctx, d, projectID, threadID); err != nil {
			return nil, err
		}
	}
	newThreadID, err := svc.StartTopic(ctx, projectID, threadID, request)
	if err != nil {
		return nil, stewardExtWrapError(err)
	}
	d.NotifyStewardChanged(projectID)
	return wire.StewardTopicsStartResult{ThreadID: newThreadID}, nil
}

// handleStewardTopicAction 是 replan/confirm/close/delete 的公共实现：
// 严格解码、取服务、校验话题归属、调服务，最后广播 steward.changed。
func handleStewardTopicAction(ctx context.Context, d *Dispatcher, params json.RawMessage, action func(svc stewardExtService, threadID string) error) (any, error) {
	var p wire.StewardTopicParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(p.ProjectID)
	threadID := strings.TrimSpace(p.ThreadID)
	if projectID == "" || threadID == "" {
		return nil, ExtInvalidParams("Invalid params: 'projectID' and 'threadID' are required")
	}
	svc, ok := stewardExtServiceOf(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	if err := stewardExtRequireTopic(ctx, d, projectID, threadID); err != nil {
		return nil, err
	}
	if err := action(svc, threadID); err != nil {
		return nil, stewardExtWrapError(err)
	}
	d.NotifyStewardChanged(projectID)
	return wire.StewardTopicsActionResult{}, nil
}

// handleStewardTopicReplan 重新发起规划，对应话题卡片上的「重新规划」。
func handleStewardTopicReplan(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	return handleStewardTopicAction(ctx, d, params, func(svc stewardExtService, threadID string) error {
		return svc.ReplanTopic(ctx, threadID)
	})
}

// handleStewardTopicConfirm 确认计划卡并开工，对应话题卡片上的「确认计划」。
func handleStewardTopicConfirm(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	return handleStewardTopicAction(ctx, d, params, func(svc stewardExtService, threadID string) error {
		return svc.ConfirmTopic(ctx, threadID)
	})
}

// handleStewardTopicClose 结项，对应话题卡片上的「结项」。
func handleStewardTopicClose(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	return handleStewardTopicAction(ctx, d, params, func(svc stewardExtService, threadID string) error {
		return svc.CloseTopic(ctx, threadID)
	})
}

// handleStewardTopicDelete 删除话题，对应话题卡片上的「删除」。
func handleStewardTopicDelete(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	return handleStewardTopicAction(ctx, d, params, func(svc stewardExtService, threadID string) error {
		return svc.DeleteTopic(ctx, threadID)
	})
}

// handleStewardTopicTaskReview 验收通过或打回话题下的任务，对应卡片上的两个按钮。
func handleStewardTopicTaskReview(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.StewardTopicTaskReviewParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(p.ProjectID)
	threadID := strings.TrimSpace(p.ThreadID)
	taskID := strings.TrimSpace(p.TaskID)
	decision := strings.TrimSpace(p.Decision)
	feedback := strings.TrimSpace(p.Feedback)
	if projectID == "" || threadID == "" || taskID == "" {
		return nil, ExtInvalidParams("Invalid params: 'projectID', 'threadID' and 'taskID' are required")
	}
	if decision != stewardReviewAccept && decision != stewardReviewReject {
		return nil, ExtInvalidParams("Invalid params: 'decision' must be 'accept' or 'reject'")
	}
	if decision == stewardReviewReject && feedback == "" {
		return nil, ExtInvalidParams("Invalid params: 'feedback' is required when 'decision' is 'reject'")
	}
	svc, ok := stewardExtServiceOf(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	if err := stewardExtRequireTopic(ctx, d, projectID, threadID); err != nil {
		return nil, err
	}
	if err := svc.ReviewTask(ctx, threadID, taskID, decision, feedback, p.Revision); err != nil {
		return nil, stewardExtWrapError(err)
	}
	d.NotifyStewardChanged(projectID)
	return wire.StewardTopicsActionResult{}, nil
}

// stewardExtRequireTopic 校验 threadID 属于该项目且是总管话题：
// 与 steward.topics.list 一样只读设置里的话题表，不引入 internal/steward，避免循环依赖。
func stewardExtRequireTopic(ctx context.Context, d *Dispatcher, projectID, threadID string) error {
	backend, ok := d.backend.(*MemoryBackend)
	if !ok || backend == nil {
		return ExtNotImplemented()
	}
	found, err := backend.IsStewardTopicOf(ctx, projectID, threadID)
	if err != nil {
		return &ExtError{Code: wire.ErrCodeOperationRejected, Message: err.Error()}
	}
	if !found {
		return ExtInvalidParams("Invalid params: 'threadID' is not a steward topic of this project")
	}
	return nil
}

// IsStewardTopicOf 报告 threadID 是不是该项目下的话题；projectID 为空时不限项目。
func (b *MemoryBackend) IsStewardTopicOf(ctx context.Context, projectID, threadID string) (bool, error) {
	threadID = strings.TrimSpace(threadID)
	if b == nil || threadID == "" {
		return false, nil
	}
	st := b.Store()
	if st == nil {
		return false, nil
	}
	raw, found, err := st.GetSetting(ctx, stewardTopicsSettingKey)
	if err != nil {
		return false, fmt.Errorf("read steward topics: %w", err)
	}
	if !found || strings.TrimSpace(raw) == "" {
		return false, nil
	}
	var topics []stewardTopicSetting
	if err := json.Unmarshal([]byte(raw), &topics); err != nil {
		return false, fmt.Errorf("parse steward topics: %w", err)
	}
	for _, topic := range topics {
		if strings.TrimSpace(topic.ThreadID) != threadID {
			continue
		}
		if projectID != "" && topic.ProjectID != projectID {
			return false, nil
		}
		return true, nil
	}
	return false, nil
}

// stewardExtWrapError 把服务错误转成带错误码的扩展错误；修订号冲突单独用版本冲突码。
func stewardExtWrapError(err error) error {
	if err == nil {
		return nil
	}
	var extErr *ExtError
	if errors.As(err, &extErr) {
		return extErr
	}
	if errors.Is(err, ErrStewardRevisionConflict) {
		return &ExtError{Code: wire.ErrCodeVersionConflict, Message: err.Error()}
	}
	return &ExtError{Code: wire.ErrCodeOperationRejected, Message: err.Error()}
}

// handleStewardTopicsList 列出总管的只读话题，供网页端侧栏展示。
func handleStewardTopicsList(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.StewardTopicsListParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	backend, ok := d.backend.(*MemoryBackend)
	if !ok || backend == nil {
		return nil, ExtNotImplemented()
	}
	return backend.ListStewardTopics(ctx, strings.TrimSpace(p.ProjectID))
}

// ListStewardTopics 读取设置里的话题表，并按目标状态补齐阶段与任务进度。
func (b *MemoryBackend) ListStewardTopics(ctx context.Context, projectID string) (wire.StewardTopicsListResult, error) {
	result := wire.StewardTopicsListResult{Topics: []wire.StewardTopicItem{}}
	st := b.Store()
	if st == nil {
		return result, nil
	}
	raw, found, err := st.GetSetting(ctx, stewardTopicsSettingKey)
	if err != nil {
		return result, fmt.Errorf("read steward topics: %w", err)
	}
	if !found {
		return result, nil
	}
	goals, err := st.ListGoals(ctx, "")
	if err != nil {
		return result, fmt.Errorf("list goals for steward topics: %w", err)
	}
	items, err := stewardTopicItems(raw, goals)
	if err != nil {
		return result, err
	}
	for _, item := range items {
		if projectID != "" && item.ProjectID != projectID {
			continue
		}
		result.Topics = append(result.Topics, item)
	}
	return result, nil
}

// stewardTopicItems 把话题表 JSON 与目标列表合成远程列表项，便于单测覆盖阶段计算。
func stewardTopicItems(raw string, goals []store.Goal) ([]wire.StewardTopicItem, error) {
	items := make([]wire.StewardTopicItem, 0)
	if strings.TrimSpace(raw) == "" {
		return items, nil
	}
	var topics []stewardTopicSetting
	if err := json.Unmarshal([]byte(raw), &topics); err != nil {
		return nil, fmt.Errorf("parse steward topics: %w", err)
	}
	goalByID := make(map[string]store.Goal, len(goals))
	for _, goal := range goals {
		goalByID[goal.ID] = goal
	}
	items = make([]wire.StewardTopicItem, 0, len(topics))
	for _, topic := range topics {
		phase := stewardPhaseDrafting
		tasksDone, tasksTotal := 0, 0
		if goal, ok := goalByID[topic.GoalID]; ok && strings.TrimSpace(topic.GoalID) != "" {
			phase = stewardPhaseFromGoalStatus(goal.Status)
			tasksDone = stewardProgressCount(goal.Progress, "done")
			tasksTotal = stewardProgressCount(goal.Progress, "total")
		}
		items = append(items, wire.StewardTopicItem{
			ThreadID:      topic.ThreadID,
			ProjectID:     topic.ProjectID,
			Title:         topic.Title,
			Brief:         topic.Brief,
			OpenQuestions: topic.OpenQuestions,
			GoalID:        topic.GoalID,
			Closed:        topic.Closed,
			UpdatedAt:     topic.UpdatedAt.UTC().Format(time.RFC3339),
			Phase:         phase,
			TasksDone:     tasksDone,
			TasksTotal:    tasksTotal,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].UpdatedAt > items[j].UpdatedAt
	})
	return items, nil
}

// stewardPhaseFromGoalStatus 把目标状态映射成话题阶段；plan_failed 单独成一个阶段。
func stewardPhaseFromGoalStatus(status string) string {
	switch strings.TrimSpace(status) {
	case stewardPhasePlanning:
		return stewardPhasePlanning
	case "plan_failed":
		return stewardPhasePlanFailed
	case stewardPhasePlanReview:
		return stewardPhasePlanReview
	case stewardPhaseRunning:
		return stewardPhaseRunning
	case stewardPhasePaused:
		return stewardPhasePaused
	case stewardPhaseCompleted:
		return stewardPhaseCompleted
	case stewardPhaseCancelled:
		return stewardPhaseCancelled
	default:
		return stewardPhaseDrafting
	}
}

// stewardProgressCount 从目标进度里取非负整数，兼容 JSON 解码出的 float64 与测试构造的 int。
func stewardProgressCount(progress map[string]any, key string) int {
	value, ok := progress[key]
	if !ok {
		return 0
	}
	switch n := value.(type) {
	case int:
		return maxInt(n, 0)
	case int64:
		return maxInt(int(n), 0)
	case float64:
		return maxInt(int(n), 0)
	case json.Number:
		parsed, err := n.Int64()
		if err != nil {
			return 0
		}
		return maxInt(int(parsed), 0)
	default:
		return 0
	}
}

func maxInt(value, floor int) int {
	if value < floor {
		return floor
	}
	return value
}
