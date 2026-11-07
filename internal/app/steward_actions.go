package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"mcode/internal/automation"
	"mcode/internal/kanban"
	"mcode/internal/remote/wire"
	"mcode/internal/steward"
	"mcode/internal/store"
)

// stewardRecentUserWindow 是「用户刚发过消息」的判断窗口：这段时间内用户正在与那个会话
// 打交道，总管不该去打扰，改为通知用户。
const stewardRecentUserWindow = 10 * time.Minute

// 动作护栏的对象类型取值，与 steward.ActionGuard 的记录口径一致。
const (
	// stewardSubjectThread 表示动作对象是一个会话。
	stewardSubjectThread = "thread"
	// stewardSubjectAutomation 表示动作对象是一个自动化。
	stewardSubjectAutomation = "automation"
	// stewardSubjectKanbanTask 表示动作对象是一个看板任务。
	stewardSubjectKanbanTask = "kanban_task"
)

// 动作被拒绝时的固定错误；文本会原样交给总管模型，所以每条都要说明下一步怎么做。
var (
	// errStewardActionNotAllowed 是总管未启用时的错误（多一层防御，MCP 层已拦一次）。
	errStewardActionNotAllowed = errors.New("总管未启用，不能催促、中断、重跑或移动看板。" +
		"请改用 notify_owner 把情况告诉用户，必要时请用户在设置 → 总管中打开总管")
	// errStewardActionReasonRequired 是缺少 reason 时的错误。
	errStewardActionReasonRequired = errors.New("缺少 reason：请用一句话说明这次动手的原因后重试，" +
		"理由会记录并可能展示给用户")
	// errStewardActionThreadIDRequired 是缺少会话 ID 时的错误。
	errStewardActionThreadIDRequired = errors.New("缺少会话 ID，请先用 thread_status 或 attention_list 确认要处理的会话")
	// errStewardAutomationAlreadyRunning 表示该自动化已有运行在进行中，无需重跑。
	errStewardAutomationAlreadyRunning = errors.New("该自动化正在运行，无需重跑；等这次运行结束后再看它的结果")
)

// stewardActionBackend 是动作需要的会话后端能力；测试用假实现替换。
type stewardActionBackend interface {
	// GetThread 读取会话摘要，用于动作前的目标检查。
	GetThread(ctx context.Context, threadID string) (wire.ThreadSummary, error)
	// SendFollowUp 把一条消息排队投递给会话，不打断正在进行的一轮。
	SendFollowUp(ctx context.Context, threadID, text string) error
	// InterruptCurrent 打断会话当前正在做的一轮。
	InterruptCurrent(ctx context.Context, threadID string) (wire.InterruptResult, error)
	// AppendNotice 在会话时间线追加一条系统提示，让用户看到总管动过手。
	AppendNotice(threadID, text string) error
}

// stewardActionKanban 是动作需要的看板能力；测试用假实现替换。
type stewardActionKanban interface {
	// MoveTask 按乐观版本号移动任务状态。
	MoveTask(ctx context.Context, taskID string, status kanban.TaskStatus, expectedRevision int) (store.Task, error)
}

// stewardTaskReader 是读取看板任务的能力：动作前要先拿到任务当前版本号与关联会话。
type stewardTaskReader interface {
	// GetTask 按 ID 读取任务。
	GetTask(ctx context.Context, id string) (store.Task, error)
}

// stewardActionAutomation 是动作需要的自动化能力；测试用假实现替换。
type stewardActionAutomation interface {
	// RunNow 立刻触发一次运行并返回运行记录。
	RunNow(ctx context.Context, id string) (store.AutomationRun, error)
}

// stewardForegroundReporter 报告界面当前正在看的会话；空串表示没在看任何会话。
type stewardForegroundReporter interface {
	ForegroundThread() string
}

// stewardActionRuntime 是动作需要的运行时能力：启用判定与总管自身会话 ID。
type stewardActionRuntime interface {
	// CanAct 报告总管是否已启用。
	CanAct() bool
	// ThreadID 返回总管自己的会话 ID。
	ThreadID() string
}

// stewardActionGuard 是动作护栏：按对象限制滚动窗口内的动作次数。
type stewardActionGuard interface {
	// Allow 判断现在是否还能对该对象动手。
	Allow(ctx context.Context, subjectType, subjectID string) error
	// Record 记一次动作。
	Record(ctx context.Context, subjectType, subjectID string) error
}

// stewardActions 实现总管的四个动作工具：催促会话、打断会话、重跑自动化、移动看板任务。
// 每个动作都先校验启用状态、目标会话与动作护栏，成功后才记次并在目标会话时间线上留一条提示。
type stewardActions struct {
	// backend 提供会话读取、消息投递、中断与提示追加。
	backend stewardActionBackend
	// kanban 提供看板任务状态移动。
	kanban stewardActionKanban
	// tasks 提供看板任务读取（版本号与关联会话）。
	tasks stewardTaskReader
	// automation 提供自动化立即运行。
	automation stewardActionAutomation
	// client 报告用户当前正在看的会话。
	client stewardForegroundReporter
	// runtime 提供启用判定与总管自身会话 ID。
	runtime stewardActionRuntime
	// guard 限制同一对象在滚动窗口内的动作次数。
	guard stewardActionGuard
	// now 是当前时间来源，为空时用 time.Now。
	now func() time.Time
}

// checkStewardThreadTarget 判断总管是否可以对 summary 指向的会话动手；返回 nil 表示可以。
// 依次拒绝总管自己、helper/bee 会话、用户正在看的会话与用户 10 分钟内发过消息的会话，
// 每种拒绝都给出不同说明，并提示改用 notify_owner。
func checkStewardThreadTarget(summary wire.ThreadSummary, stewardThreadID, foregroundThreadID string, now time.Time) error {
	if stewardThreadID != "" && summary.ID == stewardThreadID {
		return errors.New("这是总管自己的会话，不能对自己动手；需要汇报时请用 notify_owner 告诉用户")
	}
	if summary.IsHelper || (summary.ParentThreadID != nil && strings.TrimSpace(*summary.ParentThreadID) != "") {
		return errors.New("这是子代理（helper 或 bee）会话，不在总管能操作的范围；" +
			"请改为处理它的主会话，或用 notify_owner 告诉用户")
	}
	if foregroundThreadID != "" && summary.ID == foregroundThreadID {
		return errors.New("用户此刻正在看着这个会话，不要从旁插手；请改用 notify_owner 告诉用户看到了什么")
	}
	if stewardUserMessagedRecently(summary.LastUserMessageAt, now) {
		return fmt.Errorf("用户在 %d 分钟内给这个会话发过消息，正在自己处理；"+
			"请改用 notify_owner 告诉用户，等用户停下来再考虑动手", int(stewardRecentUserWindow.Minutes()))
	}
	return nil
}

// stewardUserMessagedRecently 判断会话摘要里的最后用户消息是否落在这段窗口内（stewardRecentUserWindow）。
// 未来时间（时钟偏差）也算刚发过。动作护栏与总管唤醒的「用户刚说完话就先不插话」共用这一份判定，
// 两边口径必须一致：否则会出现「不能动手但可以插话」或者相反。
func stewardUserMessagedRecently(raw *string, now time.Time) bool {
	at, ok := parseStewardLastUserMessage(raw)
	if !ok {
		return false
	}
	return now.Sub(at) < stewardRecentUserWindow
}

// parseStewardLastUserMessage 解析会话摘要里的最后用户消息时间；缺失或格式错误时返回 false，
// 调用方据此按「没有近期用户消息」放行，避免格式问题让总管彻底动不了手。
func parseStewardLastUserMessage(raw *string) (time.Time, bool) {
	if raw == nil {
		return time.Time{}, false
	}
	text := strings.TrimSpace(*raw)
	if text == "" {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		at, err = time.Parse(time.RFC3339, text)
		if err != nil {
			return time.Time{}, false
		}
	}
	return at, true
}

// SendMessage 实现 thread_send_message：把一条消息排队投递给目标会话，不打断它正在进行的一轮。
func (a *stewardActions) SendMessage(ctx context.Context, threadID, text, reason string) error {
	if err := a.precheck(reason); err != nil {
		return err
	}
	if err := a.checkThreadTarget(ctx, threadID); err != nil {
		return err
	}
	if err := a.guard.Allow(ctx, stewardSubjectThread, threadID); err != nil {
		return err
	}
	if err := a.backend.SendFollowUp(ctx, threadID, text); err != nil {
		return fmt.Errorf("给会话 %s 发送跟进消息失败: %w", threadID, err)
	}
	a.record(ctx, stewardSubjectThread, threadID)
	a.notice(threadID, "总管：已发送跟进消息（"+reason+"）")
	log.Printf("steward: 已向会话 %s 发送跟进消息: %s", threadID, reason)
	return nil
}

// InterruptThread 实现 thread_interrupt：打断目标会话当前正在做的一轮，进行中的工作会丢失。
func (a *stewardActions) InterruptThread(ctx context.Context, threadID, reason string) error {
	if err := a.precheck(reason); err != nil {
		return err
	}
	if err := a.checkThreadTarget(ctx, threadID); err != nil {
		return err
	}
	if err := a.guard.Allow(ctx, stewardSubjectThread, threadID); err != nil {
		return err
	}
	if _, err := a.backend.InterruptCurrent(ctx, threadID); err != nil {
		return fmt.Errorf("中断会话 %s 失败: %w", threadID, err)
	}
	a.record(ctx, stewardSubjectThread, threadID)
	a.notice(threadID, "总管：已中断本轮（"+reason+"）")
	log.Printf("steward: 已中断会话 %s 的一轮: %s", threadID, reason)
	return nil
}

// RunAutomation 实现 automation_run_now：立刻跑一次自动化并返回运行 ID。
// 该自动化正在运行时不计次，只提示无需重跑。
func (a *stewardActions) RunAutomation(ctx context.Context, automationID, reason string) (string, error) {
	if err := a.precheck(reason); err != nil {
		return "", err
	}
	if strings.TrimSpace(automationID) == "" {
		return "", errors.New("缺少自动化 ID，请先用 automation_list 确认要重跑的自动化")
	}
	if err := a.guard.Allow(ctx, stewardSubjectAutomation, automationID); err != nil {
		return "", err
	}
	run, err := a.automation.RunNow(ctx, automationID)
	if err != nil {
		if errors.Is(err, automation.ErrAlreadyRunning) {
			return "", errStewardAutomationAlreadyRunning
		}
		return "", fmt.Errorf("触发自动化 %s 运行失败: %w", automationID, err)
	}
	a.record(ctx, stewardSubjectAutomation, automationID)
	log.Printf("steward: 已触发自动化 %s 的额外运行（%s）: %s", automationID, run.ID, reason)
	return run.ID, nil
}

// MoveKanbanTask 实现 kanban_move_task：把任务移到目标状态。
// 任务关联了会话时同样要做目标检查，避免动到用户正在处理的会话。
func (a *stewardActions) MoveKanbanTask(ctx context.Context, taskID, status, reason string) error {
	if err := a.precheck(reason); err != nil {
		return err
	}
	trimmedTaskID := strings.TrimSpace(taskID)
	if trimmedTaskID == "" {
		return errors.New("缺少看板任务 ID，请先用看板工具确认要移动的任务")
	}
	target := kanban.TaskStatus(strings.TrimSpace(status))
	if !target.Valid() {
		return fmt.Errorf("不支持的目标状态 %q：只能是 todo、review、done 或 rejected", status)
	}
	task, err := a.tasks.GetTask(ctx, trimmedTaskID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("看板任务 %s 不存在，请先用看板工具确认任务 ID", trimmedTaskID)
		}
		return fmt.Errorf("读取看板任务 %s 失败: %w", trimmedTaskID, err)
	}
	linkedThreadID := ""
	if task.ThreadID != nil {
		linkedThreadID = strings.TrimSpace(*task.ThreadID)
	}
	if linkedThreadID != "" {
		if err := a.checkThreadTarget(ctx, linkedThreadID); err != nil {
			return err
		}
	}
	if err := a.guard.Allow(ctx, stewardSubjectKanbanTask, trimmedTaskID); err != nil {
		return err
	}
	if task.Status == string(kanban.TaskStatusReview) && target == kanban.TaskStatusDone {
		return fmt.Errorf("看板任务 %s 处于待验收，不能直接移到完成：验收和合并只能走评审接受（由用户或目标协调员完成），请改用 notify_owner 请用户验收", trimmedTaskID)
	}
	if _, err := a.kanban.MoveTask(ctx, trimmedTaskID, target, task.Revision); err != nil {
		switch {
		case errors.Is(err, kanban.ErrInvalidTransition):
			return fmt.Errorf("看板任务 %s 不能从 %s 移到 %s：状态流转不合法，"+
				"请确认任务当前状态后重试，或改用 notify_owner 请用户处理", trimmedTaskID, task.Status, target)
		case errors.Is(err, store.ErrRevisionConflict):
			return fmt.Errorf("看板任务 %s 刚被改动过（版本号已变），请重新读取任务后再试", trimmedTaskID)
		default:
			return fmt.Errorf("移动看板任务 %s 失败: %w", trimmedTaskID, err)
		}
	}
	a.record(ctx, stewardSubjectKanbanTask, trimmedTaskID)
	if linkedThreadID != "" {
		a.notice(linkedThreadID, "总管：已把看板任务移到 "+string(target)+"（"+reason+"）")
	}
	log.Printf("steward: 已把看板任务 %s 移到 %s: %s", trimmedTaskID, target, reason)
	return nil
}

// precheck 做所有动作共同的校验：总管已启用、reason 不为空。
func (a *stewardActions) precheck(reason string) error {
	if a.runtime == nil || a.guard == nil || a.backend == nil {
		return errors.New("总管的动作依赖尚未接线，请改用 notify_owner 告诉用户")
	}
	if !a.runtime.CanAct() {
		return errStewardActionNotAllowed
	}
	if strings.TrimSpace(reason) == "" {
		return errStewardActionReasonRequired
	}
	return nil
}

// checkThreadTarget 读取会话摘要并做目标检查；读取失败或检查不通过时返回可读错误。
func (a *stewardActions) checkThreadTarget(ctx context.Context, threadID string) error {
	id := strings.TrimSpace(threadID)
	if id == "" {
		return errStewardActionThreadIDRequired
	}
	summary, err := a.backend.GetThread(ctx, id)
	if err != nil {
		return fmt.Errorf("会话 %s 不存在或读取失败，请先用 thread_status 确认会话 ID: %w", id, err)
	}
	return checkStewardThreadTarget(summary, a.stewardThreadID(), a.foregroundThread(), a.nowTime())
}

// record 记一次动作；写日志失败只记日志，不影响已经完成的动作。
func (a *stewardActions) record(ctx context.Context, subjectType, subjectID string) {
	if err := a.guard.Record(ctx, subjectType, subjectID); err != nil {
		log.Printf("steward: 记录动作失败（%s/%s）: %v", subjectType, subjectID, err)
	}
}

// notice 在会话时间线追加一条提示；失败只记日志，不影响动作本身的结果。
func (a *stewardActions) notice(threadID, text string) {
	if err := a.backend.AppendNotice(threadID, text); err != nil {
		log.Printf("steward: 向会话 %s 追加提示失败: %v", threadID, err)
	}
}

// stewardThreadID 返回总管自己的会话 ID；运行时未接线时为空串。
func (a *stewardActions) stewardThreadID() string {
	if a.runtime == nil {
		return ""
	}
	return a.runtime.ThreadID()
}

// foregroundThread 返回用户当前正在看的会话 ID；界面未接线时为空串。
func (a *stewardActions) foregroundThread() string {
	if a.client == nil {
		return ""
	}
	return a.client.ForegroundThread()
}

// nowTime 返回当前时间；未注入时钟时用 time.Now。
func (a *stewardActions) nowTime() time.Time {
	if a.now == nil {
		return time.Now()
	}
	return a.now()
}

// 让 *steward.Runtime 与 *steward.ActionGuard 满足动作所需的接口。
var (
	_ stewardActionRuntime = (*steward.Runtime)(nil)
	_ stewardActionGuard   = (*steward.ActionGuard)(nil)
)
