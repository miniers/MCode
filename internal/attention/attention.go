package attention

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"mcode/internal/store"
)

// Kind 表示待处理事件的类型。
type Kind string

const (
	// KindThreadStalled 表示会话长时间没有进展，疑似卡死。
	KindThreadStalled Kind = "thread_stalled"
	// KindThreadFailed 表示会话执行失败。
	KindThreadFailed Kind = "thread_failed"
	// KindThreadQuota 表示会话因额度耗尽而中断。
	KindThreadQuota Kind = "thread_quota"
	// KindThreadApproval 表示会话正在等待用户审批工具调用。
	KindThreadApproval Kind = "thread_approval"
	// KindKanbanFailed 表示看板任务执行失败。
	KindKanbanFailed Kind = "kanban_failed"
	// KindKanbanNeedsApproval 表示看板任务等待用户审批。
	KindKanbanNeedsApproval Kind = "kanban_needs_approval"
	// KindKanbanReview 表示看板任务已完成，等待用户验收。
	KindKanbanReview Kind = "kanban_review"
	// KindKanbanPlanFailed 表示看板目标规划失败，等用户重试或修改目标。
	KindKanbanPlanFailed Kind = "kanban_plan_failed"
	// KindAutomationFailed 表示自动化运行失败。
	KindAutomationFailed Kind = "automation_failed"
	// KindAutomationFinished 表示自动化运行结束，等待用户查看结果。
	KindAutomationFinished Kind = "automation_finished"
	// KindBeeFailed 表示 Bee 子代理执行失败。
	KindBeeFailed Kind = "bee_failed"
	// KindMergeReviewPending 表示 Bee 有改动等待审查（AI 审查进行中除外）。
	KindMergeReviewPending Kind = "merge_review_pending"
	// KindThreadNoProgress 表示会话或 bee 长时间没有文件改动，也没有看板状态变化。
	KindThreadNoProgress Kind = "thread_no_progress"
)

// OwnerActionKinds 是必须由用户亲手处理的事件类型白名单：会话等审批、看板待审批、
// 看板待验收、目标规划失败。总管启用时收件箱只列这四类，其余告知类事件交给总管处理。
var OwnerActionKinds = []Kind{KindThreadApproval, KindKanbanNeedsApproval, KindKanbanReview, KindKanbanPlanFailed}

// OwnerActionKindNames 返回 OwnerActionKinds 的字符串形式，供存储层过滤条件使用。
func OwnerActionKindNames() []string {
	names := make([]string, 0, len(OwnerActionKinds))
	for _, kind := range OwnerActionKinds {
		names = append(names, string(kind))
	}
	return names
}

// SubjectType 表示待处理事件所属对象的类型。
type SubjectType string

const (
	// SubjectThread 表示事件属于一个会话。
	SubjectThread SubjectType = "thread"
	// SubjectKanbanTask 表示事件属于一个看板任务。
	SubjectKanbanTask SubjectType = "kanban_task"
	// SubjectKanbanGoal 表示事件属于一个看板目标。
	SubjectKanbanGoal SubjectType = "kanban_goal"
	// SubjectAutomationRun 表示事件属于一次自动化运行。
	SubjectAutomationRun SubjectType = "automation_run"
	// SubjectBee 表示事件属于一个 Bee 子代理。
	SubjectBee SubjectType = "bee"
)

// 消解者取值约定：自动消解、用户手动消解、主代理（steward）代为消解。
const (
	// ResolvedByAuto 表示对象恢复正常后由系统自动消解。
	ResolvedByAuto = "auto"
	// ResolvedByUser 表示用户在界面上手动消解。
	ResolvedByUser = "user"
	// ResolvedBySteward 表示主代理代为消解。
	ResolvedBySteward = "steward"
)

// defaultDailyCap 是未配置 DailyCap 时，同一对象同类事件在 24 小时内允许写入的条数上限。
const defaultDailyCap = 6

// dailyWindow 是写入截停使用的滚动窗口长度。
const dailyWindow = 24 * time.Hour

// Event 是待处理事件，直接复用存储层类型；ResolvedAt 非空表示已消解。
type Event = store.AttentionEvent

// Input 描述一条待记录事件的来源信息。
type Input struct {
	Kind        Kind
	SubjectType SubjectType
	SubjectID   string
	ProjectID   string
	// ThreadID 用于点击事件后跳转到对应会话，可为空。
	ThreadID string
	Title    string
	Detail   string
	// EnteredAt 是对象进入该状态的时刻；为零时取 Service 的当前时间。
	EnteredAt time.Time
}

// Store 是 Service 依赖的存储能力集合，*store.Store 满足该接口。
type Store interface {
	// InsertAttentionEvent 写入一条待处理事件。
	InsertAttentionEvent(ctx context.Context, e store.AttentionEvent) (store.AttentionEvent, error)
	// GetAttentionEvent 按 ID 读取待处理事件，不存在时返回 store.ErrNotFound。
	GetAttentionEvent(ctx context.Context, id string) (store.AttentionEvent, error)
	// GetAttentionEventByDedupeKey 按去重键读取待处理事件，不存在时返回 store.ErrNotFound。
	GetAttentionEventByDedupeKey(ctx context.Context, key string) (store.AttentionEvent, error)
	// CountAttentionEventsSince 统计同一类型同一对象自 since 起写入的条数。
	CountAttentionEventsSince(ctx context.Context, kind, subjectType, subjectID string, since time.Time) (int, error)
	// ResolveAttentionEvent 消解单条未消解事件，返回是否有行被更新。
	ResolveAttentionEvent(ctx context.Context, id string, at time.Time, by string) (bool, error)
	// ResolveAttentionEventsBySubject 消解某个对象下指定类型的未消解事件，返回被消解的行。
	ResolveAttentionEventsBySubject(ctx context.Context, subjectType, subjectID string, kinds []string, at time.Time, by string) ([]store.AttentionEvent, error)
	// ListAttentionEvents 按过滤条件列出待处理事件。
	ListAttentionEvents(ctx context.Context, filter store.AttentionEventFilter) ([]store.AttentionEvent, error)
	// CountUnresolvedAttentionEvents 统计全部未消解事件的条数。
	CountUnresolvedAttentionEvents(ctx context.Context) (int, error)
	// CountUnresolvedAttentionEventsByKinds 统计指定类型的未消解事件条数。
	CountUnresolvedAttentionEventsByKinds(ctx context.Context, kinds []string) (int, error)
}

var _ Store = (*store.Store)(nil)

// Options 是 Service 的可选配置。
type Options struct {
	// Now 为当前时间来源，测试可注入；为空时使用 time.Now。
	Now func() time.Time
	// DailyCap 是同一对象同类事件在 24 小时内允许写入的条数上限；小于等于 0 时取 defaultDailyCap。
	DailyCap int
}

// Service 负责待处理事件的去重写入、消解与变更通知，可并发调用。
type Service struct {
	st       Store
	now      func() time.Time
	dailyCap int

	mu       sync.Mutex
	nextID   int
	watchers map[int]func(Event)
}

// New 创建待处理事件服务；opts 为零值时使用默认时间来源与默认条数上限。
func New(st Store, opts Options) *Service {
	s := &Service{
		st:       st,
		now:      opts.Now,
		dailyCap: opts.DailyCap,
		watchers: make(map[int]func(Event)),
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.dailyCap <= 0 {
		s.dailyCap = defaultDailyCap
	}
	return s
}

// Record 记录一条待处理事件：同一对象同一进入时刻（UTC 毫秒）只落一条，
// 同一对象同类事件在滚动 24 小时内超过上限时截停。返回事件本身以及是否新写入；
// 已存在或超限时返回 false 且不报错。
func (s *Service) Record(ctx context.Context, in Input) (Event, bool, error) {
	enteredAt := in.EnteredAt
	if enteredAt.IsZero() {
		enteredAt = s.now()
	}
	enteredAt = enteredAt.UTC()
	key := dedupeKey(in.Kind, in.SubjectType, in.SubjectID, enteredAt)

	existing, err := s.st.GetAttentionEventByDedupeKey(ctx, key)
	switch {
	case err == nil:
		return existing, false, nil
	case !errors.Is(err, store.ErrNotFound):
		return Event{}, false, err
	}

	since := s.now().UTC().Add(-dailyWindow)
	count, err := s.st.CountAttentionEventsSince(ctx, string(in.Kind), string(in.SubjectType), in.SubjectID, since)
	if err != nil {
		return Event{}, false, err
	}
	if count >= s.dailyCap {
		log.Printf("attention: 对象 %s/%s 的 %s 事件在 24 小时内已达 %d 条上限，跳过写入",
			in.SubjectType, in.SubjectID, in.Kind, s.dailyCap)
		return Event{}, false, nil
	}

	created, err := s.st.InsertAttentionEvent(ctx, store.AttentionEvent{
		Kind:        string(in.Kind),
		SubjectType: string(in.SubjectType),
		SubjectID:   in.SubjectID,
		ProjectID:   in.ProjectID,
		ThreadID:    in.ThreadID,
		Title:       in.Title,
		Detail:      in.Detail,
		EnteredAt:   enteredAt,
		DedupeKey:   key,
	})
	if err != nil {
		// 并发写入同一去重键时，后到者会撞上唯一约束；此时回读已落库的那条并当作重复处理。
		if existing, getErr := s.st.GetAttentionEventByDedupeKey(ctx, key); getErr == nil {
			return existing, false, nil
		}
		return Event{}, false, err
	}
	s.notify(created)
	return created, true, nil
}

// Resolve 手动消解一条事件；事件已消解或不存在时不报错，只有实际发生变化才通知订阅者。
func (s *Service) Resolve(ctx context.Context, id, by string) error {
	changed, err := s.st.ResolveAttentionEvent(ctx, id, s.now().UTC(), by)
	if err != nil || !changed {
		return err
	}
	ev, err := s.st.GetAttentionEvent(ctx, id)
	if err != nil {
		return err
	}
	s.notify(ev)
	return nil
}

// ResolveSubject 在对象恢复正常时自动消解其未消解事件，逐条通知订阅者并返回消解条数；
// kinds 为空表示该对象的全部类型。
func (s *Service) ResolveSubject(ctx context.Context, subjectType SubjectType, subjectID, by string, kinds ...Kind) (int, error) {
	kindNames := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		kindNames = append(kindNames, string(kind))
	}
	events, err := s.st.ResolveAttentionEventsBySubject(ctx, string(subjectType), subjectID, kindNames, s.now().UTC(), by)
	if err != nil {
		return 0, err
	}
	for _, ev := range events {
		s.notify(ev)
	}
	return len(events), nil
}

// List 按过滤条件列出待处理事件，按创建时间倒序。
func (s *Service) List(ctx context.Context, filter store.AttentionEventFilter) ([]Event, error) {
	return s.st.ListAttentionEvents(ctx, filter)
}

// UnresolvedCount 返回全部未消解事件的条数。
func (s *Service) UnresolvedCount(ctx context.Context) (int, error) {
	return s.st.CountUnresolvedAttentionEvents(ctx)
}

// UnresolvedCountByKinds 返回指定类型的未消解事件条数，与 List 传同一 Kinds 时的
// 列表条数口径一致；kinds 为空表示不按类型过滤。
func (s *Service) UnresolvedCountByKinds(ctx context.Context, kinds []string) (int, error) {
	return s.st.CountUnresolvedAttentionEventsByKinds(ctx, kinds)
}

// Watch 注册事件变更回调，返回可重复调用的取消函数；回调在锁外同步调用，
// 事件 ResolvedAt 非空表示这是一次消解通知。
func (s *Service) Watch(fn func(Event)) (stop func()) {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	if s.watchers == nil {
		s.watchers = make(map[int]func(Event))
	}
	s.watchers[id] = fn
	s.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.watchers, id)
			s.mu.Unlock()
		})
	}
}

// notify 在锁外按注册顺序同步调用全部订阅者。
func (s *Service) notify(ev Event) {
	s.mu.Lock()
	watchers := make([]func(Event), 0, len(s.watchers))
	for _, fn := range s.watchers {
		if fn != nil {
			watchers = append(watchers, fn)
		}
	}
	s.mu.Unlock()
	for _, fn := range watchers {
		fn(ev)
	}
}

// dedupeKey 构造去重键：类型、对象类型、对象 ID 与进入时刻（UTC 毫秒）用竖线连接。
func dedupeKey(kind Kind, subjectType SubjectType, subjectID string, enteredAt time.Time) string {
	return fmt.Sprintf("%s|%s|%s|%d", kind, subjectType, subjectID, enteredAt.UTC().UnixMilli())
}
