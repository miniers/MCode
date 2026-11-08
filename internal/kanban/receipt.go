package kanban

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"mcode/internal/store"
)

// 执行会话提交结果时允许的 outcome 取值。
const (
	ReceiptOutcomeDone    = "done"
	ReceiptOutcomeBlocked = "blocked"
)

const (
	// TaskReceiptPrompt 是执行会话停止却没有提交回执时发送的追问文案。
	TaskReceiptPrompt = "这一轮还没有提交任务结果。请调用 kanban_submit_result，status 选择 done、failed、waiting、delegated 或 needs_decision，并填写对应结果。不要重做已完成的部分。"
	// receiptAskGrace 是两次追问之间、以及最后一次追问到升级人工之间的最短间隔。
	receiptAskGrace = 60 * time.Second
	// receiptMaxAsks 是为了拿到回执最多追问的次数，超过后交由人工确认。
	receiptMaxAsks = 2
	// receiptSummaryLimit 限制回执摘要长度，避免把整段日志写进卡片。
	receiptSummaryLimit = 500
	// receiptBlockedPrefix 是执行会话求助时卡片活动文本的前缀。
	receiptBlockedPrefix = "执行会话求助："
	// receiptEscalatedText 是追问满额仍然没有回执、交给人工确认时的卡片文案。
	receiptEscalatedText = "执行会话两次追问后仍未提交结果，需要人工确认"
)

// TaskReceipt 是任务执行会话在结束这一轮前提交的结果回执。
type TaskReceipt struct {
	ThreadID string
	Outcome  string
	Summary  string
	At       time.Time
	Result   ExecutorResult
}

// receiptAsk 记录某张卡片已追问回执的次数与最近一次时间。
type receiptAsk struct {
	count int
	at    time.Time
}

// receiptState 汇总看板进程内的回执、追问与求助状态。
// 它使用独立的互斥锁，避免与 Service.mu 的落库路径互相阻塞。
type receiptState struct {
	mu       sync.Mutex
	receipts map[string]TaskReceipt
	asks     map[string]receiptAsk
	blocked  map[string]bool
	// pendingDone 保存 watchdog 已消费、但留给目标调度器自动接受的完成摘要
	pendingDone map[string]string
}

// init 惰性初始化内部 map，保证零值 receiptState 也可用。
func (rs *receiptState) init() {
	if rs.receipts == nil {
		rs.receipts = make(map[string]TaskReceipt)
	}
	if rs.asks == nil {
		rs.asks = make(map[string]receiptAsk)
	}
	if rs.blocked == nil {
		rs.blocked = make(map[string]bool)
	}
	if rs.pendingDone == nil {
		rs.pendingDone = make(map[string]string)
	}
}

// nowTime 返回服务当前时间，测试可通过 Options.Now 注入。
func (s *Service) nowTime() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now()
}

// threadIDOf 返回任务执行会话的线程 ID，未启动时为空串。
func threadIDOf(t store.Task) string {
	if t.ThreadID == nil {
		return ""
	}
	return *t.ThreadID
}

// clipReceiptText 按 rune 截断文本，保证卡片活动文本与回执摘要不会过长。
func clipReceiptText(text string) string {
	runes := []rune(text)
	if len(runes) > receiptSummaryLimit {
		return string(runes[:receiptSummaryLimit])
	}
	return text
}

// SubmitTaskResult 接收任务执行会话提交的结果回执，供 watchdog 与调度器决定任务去向。
// 只有任务当前的执行会话可以提交，且 outcome 必须是 done 或 blocked。
func (s *Service) SubmitTaskResult(ctx context.Context, taskID, callerThreadID, outcome, summary string) error {
	res := ExecutorResult{Summary: strings.TrimSpace(summary)}
	switch strings.TrimSpace(outcome) {
	case ReceiptOutcomeDone:
		res.Status = ResultDone
	case ReceiptOutcomeBlocked:
		res.Status = ResultNeedsDecision
		res.Question = res.Summary
		res.Options = []DecisionOption{{Key: "A", Text: "继续"}, {Key: "B", Text: "停下"}}
		res.Recommendation = "A"
	default:
		return fmt.Errorf("%w: 无效的 outcome", ErrInvalidTransition)
	}
	return s.SubmitExecutorResult(ctx, taskID, callerThreadID, res)
}

func (s *Service) SubmitExecutorResult(ctx context.Context, taskID, callerThreadID string, res ExecutorResult) error {
	if s == nil || s.opts.Store == nil {
		return errors.New("kanban: 看板服务不可用")
	}

	taskID = strings.TrimSpace(taskID)
	callerThreadID = strings.TrimSpace(callerThreadID)
	res.Summary = strings.TrimSpace(res.Summary)
	res.clampWaitMinutes()

	if taskID == "" {
		return fmt.Errorf("%w: task_id 不能为空", ErrInvalidTransition)
	}
	if err := res.Validate(); err != nil { return fmt.Errorf("%w: %v", ErrInvalidTransition, err) }
	if res.Summary == "" {
		return fmt.Errorf("%w: summary 不能为空", ErrInvalidTransition)
	}
	res.Summary = clipReceiptText(res.Summary)

	task, err := s.opts.Store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task.Status != string(TaskStatusInProgress) && task.Status != string(TaskStatusNeedsApproval) {
		return fmt.Errorf("%w: 任务 %s 当前状态为 %s，不在执行中", ErrInvalidTransition, task.ID, task.Status)
	}
	if callerThreadID == "" || threadIDOf(task) != callerThreadID {
		return fmt.Errorf("%w: 只有任务当前的执行会话可以提交结果", ErrInvalidTransition)
	}

	rs := &s.receipts
	rs.mu.Lock()
	rs.init()
	rs.receipts[taskID] = TaskReceipt{
		ThreadID: callerThreadID,
		Outcome:  string(res.Status),
		Summary:  res.Summary,
		Result:   res,
		At:       s.nowTime(),
	}
	// 新回执替代上一轮留给调度器的完成摘要
	delete(rs.pendingDone, taskID)
	// 执行会话既然交出了结果，就不再处于求助/待回执状态
	delete(rs.blocked, taskID)
	delete(rs.asks, taskID)
	rs.mu.Unlock()

	s.revision.Add(1)
	s.notifyChange()
	return nil
}

// takeReceipt 取走并消费任务回执，保证同一条回执只被处理一次。
// ThreadID 与任务当前会话不一致的回执视为过期，直接丢弃。
func (s *Service) takeReceipt(taskID, threadID string) (TaskReceipt, bool) {
	rs := &s.receipts
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.init()
	receipt, ok := rs.receipts[taskID]
	if !ok {
		return TaskReceipt{}, false
	}
	delete(rs.receipts, taskID)
	// 新回执开始新一轮，旧的追问计数作废
	delete(rs.asks, taskID)
	if threadID == "" || receipt.ThreadID != threadID {
		return TaskReceipt{}, false
	}
	return receipt, true
}

// hasReceipt 判断任务是否有来自当前执行会话、尚未消费的回执。
func (s *Service) hasReceipt(taskID, threadID string) bool {
	rs := &s.receipts
	rs.mu.Lock()
	defer rs.mu.Unlock()
	receipt, ok := rs.receipts[taskID]
	return ok && threadID != "" && receipt.ThreadID == threadID
}

// markReceiptBlocked 记录任务因执行会话求助而停在 needs_approval。
func (s *Service) markReceiptBlocked(taskID string) {
	rs := &s.receipts
	rs.mu.Lock()
	rs.init()
	rs.blocked[taskID] = true
	rs.mu.Unlock()
}

// isReceiptBlocked 判断任务是否因执行会话求助而等待人工处理。
func (s *Service) isReceiptBlocked(taskID string) bool {
	rs := &s.receipts
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.blocked[taskID]
}

// clearReceiptBlocked 在用户回复、会话恢复运行后解除求助标记。
func (s *Service) clearReceiptBlocked(taskID string) {
	rs := &s.receipts
	rs.mu.Lock()
	delete(rs.blocked, taskID)
	rs.mu.Unlock()
}

// clearReceiptRound 在任务重新开始执行时清掉上一轮的求助标记、追问计数与未消费的回执。
func (s *Service) clearReceiptRound(taskID string) {
	rs := &s.receipts
	rs.mu.Lock()
	rs.init()
	delete(rs.blocked, taskID)
	delete(rs.asks, taskID)
	delete(rs.receipts, taskID)
	delete(rs.pendingDone, taskID)
	rs.mu.Unlock()
}

// stashPendingDone 保存 watchdog 已消费、但要留给调度器自动接受的完成摘要。
func (s *Service) stashPendingDone(taskID, summary string) {
	rs := &s.receipts
	rs.mu.Lock()
	rs.init()
	rs.pendingDone[taskID] = summary
	rs.mu.Unlock()
}

// takePendingDone 取出 watchdog 留给调度器的完成摘要。
func (s *Service) takePendingDone(taskID string) (string, bool) {
	rs := &s.receipts
	rs.mu.Lock()
	defer rs.mu.Unlock()
	summary, ok := rs.pendingDone[taskID]
	if ok {
		delete(rs.pendingDone, taskID)
	}
	return summary, ok
}

// receiptAskAction 是缺回执时追问决策的结果。
type receiptAskAction int

const (
	// receiptAskWait 表示本轮不追问，等待下一轮或人工处理。
	receiptAskWait receiptAskAction = iota
	// receiptAskSend 表示本轮需要发送追问。
	receiptAskSend
	// receiptAskEscalate 表示追问已满额，应升级给人工。
	receiptAskEscalate
)

// planReceiptAsk 决定本轮是否发送追问：同一任务两次追问之间至少间隔 receiptAskGrace，
// 追问满 receiptMaxAsks 次后再过宽限即升级。发送前先记账，避免重复追问。
func (s *Service) planReceiptAsk(taskID string, now time.Time) receiptAskAction {
	rs := &s.receipts
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.init()

	ask := rs.asks[taskID]
	if ask.count >= receiptMaxAsks {
		if now.Sub(ask.at) < receiptAskGrace {
			return receiptAskWait
		}
		return receiptAskEscalate
	}
	if ask.count > 0 && now.Sub(ask.at) < receiptAskGrace {
		return receiptAskWait
	}
	rs.asks[taskID] = receiptAsk{count: ask.count + 1, at: now}
	return receiptAskSend
}

// finishKind 是执行会话结束后的去向。
type finishKind int

const (
	// finishDone 表示会话正常完成，按目标策略送审或接受。
	finishDone finishKind = iota
	// finishBlocked 表示会话明确表示做不下去，需要人工决定。
	finishBlocked
	// finishWait 表示回执还没到，本轮不推进任务。
	finishWait
	// finishEscalate 表示追问满额仍无回执，需要人工确认。
	finishEscalate
)

// finishDecision 是 decideFinished 的决策结果，summary 会写入卡片活动文本。
// fromReceipt 表示结论来自执行会话的回执；来自旧行为时活动文本沿用原有文案。
type finishDecision struct {
	kind        finishKind
	summary     string
	fromReceipt bool
	result      ExecutorResult
}

// taskOpenForReceipt 重读任务，确认这张卡片仍在执行中或等人处理，
// 避免给刚被另一路巡视收尾的任务补发追问。
func (s *Service) taskOpenForReceipt(ctx context.Context, taskID string) bool {
	if s.opts.Store == nil {
		return false
	}
	task, err := s.opts.Store.GetTask(ctx, taskID)
	if err != nil {
		return false
	}
	return task.Status == string(TaskStatusInProgress) || task.Status == string(TaskStatusNeedsApproval)
}

// lastAssistantText 读取执行会话最后一条助手文本，作为旧行为的完成摘要。
func (s *Service) lastAssistantText(ctx context.Context, t store.Task) string {
	threadID := threadIDOf(t)
	if threadID == "" || s.opts.Driver == nil {
		return ""
	}
	st, err := s.opts.Driver.SessionState(ctx, threadID)
	if err != nil {
		return ""
	}
	return clipReceiptText(st.LastAssistantText)
}

// decideFinished 决定一个已停止、未失败、未等待的会话该如何收尾。
// watchdog 与调度器共用这套规则：有回执按回执走；没有回执时，驱动不支持追问或会话调不到
// 回执工具（hostToolsUnavailable）都保持旧行为，其余情况先追问两次，仍无回执再升级给人工。
func (s *Service) decideFinished(ctx context.Context, t store.Task, now time.Time, hostToolsUnavailable bool) finishDecision {
	if receipt, ok := s.takeReceipt(t.ID, threadIDOf(t)); ok {
		if receipt.Result.Status == ResultNeedsDecision {
			return finishDecision{kind: finishBlocked, summary: receipt.Summary, fromReceipt: true, result: receipt.Result}
		}
		return finishDecision{kind: finishDone, summary: receipt.Summary, fromReceipt: true, result: receipt.Result}
	}

	// watchdog 已经消费了完成回执并留给调度器自动接受，这里直接复用它的摘要
	if summary, ok := s.takePendingDone(t.ID); ok {
		return finishDecision{kind: finishDone, summary: summary, fromReceipt: true}
	}

	messenger, supportsFollowUp := s.opts.Driver.(SessionMessenger)
	// 会话里调不到回执工具时追问没有意义（Agent 拿不到 kanban_submit_result），按旧行为直接完成。
	if s.opts.Driver == nil || !supportsFollowUp || messenger == nil || hostToolsUnavailable {
		return finishDecision{kind: finishDone, summary: s.lastAssistantText(ctx, t)}
	}

	// 追问前重读任务：另一路巡视可能在同一轮里刚把这张卡片收尾，避免给已结束的会话补发追问
	if !s.taskOpenForReceipt(ctx, t.ID) {
		return finishDecision{kind: finishWait}
	}

	switch s.planReceiptAsk(t.ID, now) {
	case receiptAskSend:
		if threadID := threadIDOf(t); threadID != "" {
			// 追问失败不阻塞本轮处理，下一轮 Tick 会按宽限重试或升级
			_ = messenger.SendFollowUp(ctx, threadID, TaskReceiptPrompt)
		}
		return finishDecision{kind: finishWait}
	case receiptAskEscalate:
		return finishDecision{kind: finishEscalate}
	default:
		return finishDecision{kind: finishWait}
	}
}

// applyExecutorResult 将回执写入任务；完成状态由调用方继续按既有评审策略处理。
func (s *Service) applyExecutorResult(ctx context.Context, t *store.Task, res ExecutorResult, now time.Time) error {
	if res.Status == "" { return nil }
	meta, err := ParseTaskMeta(t.Meta)
	if err != nil { return err }
	meta.ResultStatus, meta.Summary = string(res.Status), res.Summary
	switch res.Status {
	case ResultFailed:
		t.Status = string(TaskStatusFailed)
		t.LastFailureReason = &res.Summary
	case ResultWaiting:
		res.clampWaitMinutes()
		t.Status = string(TaskStatusWaiting)
		meta.WaitKind, meta.WaitCheck, meta.Wakes = res.WaitKind, res.WaitCheck, 0
		meta.WakeAt = now.Add(time.Duration(res.WaitMinutes) * time.Minute).Format(time.RFC3339)
		if res.WaitingOn != "" { meta.Summary = res.WaitingOn }
	case ResultDelegated:
		for _, f := range res.Followups {
			child, err := s.createChildTask(ctx, *t, f)
			if err != nil { return err }
			meta.Children = append(meta.Children, child.ID)
		}
		t.Status, meta.WaitKind = string(TaskStatusWaiting), "children"
	case ResultNeedsDecision:
		if meta.DecisionID != "" {
			d, err := s.opts.Store.GetStewardDecision(ctx, meta.DecisionID)
			if err != nil && !errors.Is(err, store.ErrNotFound) { return err }
			if err == nil && d.Status == store.DecisionStatusOpen { t.Status = string(TaskStatusNeedsApproval); t.Meta = EncodeTaskMeta(meta); return nil }
		}
		options, err := json.Marshal(res.Options)
		if err != nil { return err }
		d, err := s.opts.Store.CreateStewardDecision(ctx, store.StewardDecision{TaskID: t.ID, ThreadID: threadIDOf(*t), Question: res.Question, OptionsJSON: string(options), Recommended: res.Recommendation})
		if err != nil { return err }
		meta.DecisionID = d.ID
		t.Status = string(TaskStatusNeedsApproval)
	}
	t.Meta = EncodeTaskMeta(meta)
	return nil
}
