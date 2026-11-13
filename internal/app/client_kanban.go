// 此文件包含 Client 的看板与任务方法。
package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"mcode/internal/kanban"
	"mcode/internal/store"
	mcodeui "mcode/internal/ui"
)

// notifyKanbanChanged 在看板变更时向所有注册的监听者发送通知。
func (c *Client) notifyKanbanChanged() {
	c.kanbanMu.Lock()
	var watchers []func()
	for _, fn := range c.kanbanWatchers {
		if fn != nil {
			watchers = append(watchers, fn)
		}
	}
	c.kanbanMu.Unlock()

	for _, fn := range watchers {
		fn()
	}
}

// KanbanBoard 获取看板数据，projectID 为空返回全部。
func (c *Client) KanbanBoard(ctx context.Context, projectID string) (mcodeui.KanbanBoard, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanBoard{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	board, err := k.Board(ctx)
	if err != nil {
		return mcodeui.KanbanBoard{}, wrapConflictErr(err)
	}
	return KanbanBoardToUI(board, projectID), nil
}

// CreateTask 在指定项目下创建新任务。
func (c *Client) CreateTask(ctx context.Context, projectID, title, notes string) (mcodeui.KanbanTask, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	var notesPtr *string
	if notes != "" {
		notesPtr = &notes
	}
	task, err := k.CreateTask(ctx, kanban.CreateTaskInput{
		ProjectID: projectID,
		Title:     title,
		Notes:     notesPtr,
	})
	if err != nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(err)
	}
	return KanbanTaskToUI(task, kanban.Derived{}), nil
}

// MoveTask 移动任务到指定状态。
func (c *Client) MoveTask(ctx context.Context, taskID, status string, revision int) (mcodeui.KanbanTask, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	task, err := k.MoveTask(ctx, taskID, kanban.TaskStatus(status), revision)
	if err != nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(err)
	}
	return KanbanTaskToUI(task, kanban.Derived{}), nil
}

// DeleteTask 按修订号删除任务。
func (c *Client) DeleteTask(ctx context.Context, taskID string, revision int) error {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return wrapConflictErr(errors.New("看板服务不可用"))
	}
	if err := k.DeleteTask(ctx, taskID, revision); err != nil {
		return wrapConflictErr(err)
	}
	return nil
}

// NudgeTask 给执行中的任务会话追加一条催促提醒。
func (c *Client) NudgeTask(ctx context.Context, taskID string, revision int) (mcodeui.KanbanTask, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	task, err := k.RescueTask(ctx, taskID, kanban.RescueActionNudge, revision)
	if err != nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(err)
	}
	return KanbanTaskToUI(task, kanban.Derived{}), nil
}

// StartTask 开始执行指定任务。
func (c *Client) StartTask(ctx context.Context, taskID string, revision int) (mcodeui.KanbanTask, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	task, err := k.StartTask(ctx, taskID, revision)
	if err != nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(err)
	}
	return KanbanTaskToUI(task, kanban.Derived{}), nil
}

// ReviewTask 评审任务（decision: accept/reject/requestReview）。
func (c *Client) ReviewTask(ctx context.Context, taskID, decision, feedback string, revision int) (mcodeui.KanbanTask, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	task, err := k.ReviewTask(ctx, taskID, kanban.ReviewDecision(decision), feedback, revision)
	if err != nil {
		return mcodeui.KanbanTask{}, wrapConflictErr(err)
	}
	c.recordRejectPreference(ctx, task, decision, feedback)
	return KanbanTaskToUI(task, kanban.Derived{}), nil
}

// recordRejectPreference 在用户亲手打回总管创建的目标下的任务时，把打回理由记成一条
// 待用户确认的记忆提议，让总管下次少犯同样的错；失败只记日志，不影响评审结果。
func (c *Client) recordRejectPreference(ctx context.Context, task store.Task, decision, feedback string) {
	if decision != string(kanban.ReviewReject) {
		return
	}
	reason := strings.TrimSpace(feedback)
	if reason == "" || task.GoalID == nil || strings.TrimSpace(*task.GoalID) == "" {
		return
	}
	st, err := c.memoryStoreConn()
	if err != nil {
		log.Printf("记录打回偏好失败，无法打开记忆库: %v", err)
		return
	}
	goal, err := st.GetGoal(ctx, *task.GoalID)
	if err != nil || goal.Owner != goalOwnerSteward {
		return
	}
	projectPath := ""
	if task.ProjectID != nil && c.backend != nil {
		if res, listErr := c.backend.ListProjects(ctx); listErr == nil {
			for _, p := range res.Projects {
				if p.ID == *task.ProjectID {
					projectPath = p.Path
					break
				}
			}
		}
	}
	if projectPath == "" {
		return
	}
	if _, err := st.CreateMemoryProposal(ctx, store.MemoryProposal{
		ProjectPath: projectPath,
		Title:       "用户打回任务时的要求",
		Content:     fmt.Sprintf("用户打回任务时提出的要求：%s（任务：%s）", reason, task.Title),
		UnitType:    "preference",
		Importance:  0.6,
		Source:      store.MemoryProposalSourceAgent,
		Scope:       store.MemoryProposalScopeProject,
		Status:      store.MemoryProposalStatusPending,
	}); err != nil {
		log.Printf("记录打回偏好失败: %v", err)
	}
}

// CreateGoal 在指定项目下创建新目标。
func (c *Client) CreateGoal(ctx context.Context, projectID, prompt string) (mcodeui.KanbanGoal, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	goal, err := k.CreateGoal(ctx, projectID, prompt)
	if err != nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(err)
	}
	return KanbanGoalToUI(goal, 0, 0), nil
}

// StartGoal 开始执行目标。
func (c *Client) StartGoal(ctx context.Context, goalID string, revision int) (mcodeui.KanbanGoal, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	goal, err := k.StartGoal(ctx, goalID, revision)
	if err != nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(err)
	}
	return KanbanGoalToUI(goal, 0, 0), nil
}

// PauseGoal 暂停目标执行。
func (c *Client) PauseGoal(ctx context.Context, goalID string, revision int) (mcodeui.KanbanGoal, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	goal, err := k.PauseGoal(ctx, goalID, revision)
	if err != nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(err)
	}
	return KanbanGoalToUI(goal, 0, 0), nil
}

// CancelGoal 取消目标。
func (c *Client) CancelGoal(ctx context.Context, goalID string, revision int) (mcodeui.KanbanGoal, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	goal, err := k.CancelGoal(ctx, goalID, revision)
	if err != nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(err)
	}
	return KanbanGoalToUI(goal, 0, 0), nil
}

// ReplanGoal 重新规划目标。
func (c *Client) ReplanGoal(ctx context.Context, goalID, feedback string, revision int) (mcodeui.KanbanGoal, error) {
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(errors.New("看板服务不可用"))
	}
	goal, err := k.ReplanGoal(ctx, goalID, feedback, revision)
	if err != nil {
		return mcodeui.KanbanGoal{}, wrapConflictErr(err)
	}
	return KanbanGoalToUI(goal, 0, 0), nil
}

// WatchKanban 监听看板变更，任一变更时回调，可能来自任意 goroutine。
func (c *Client) WatchKanban(onChange func()) (stop func()) {
	c.kanbanMu.Lock()
	c.nextWatchID++
	id := c.nextWatchID
	if c.kanbanWatchers == nil {
		c.kanbanWatchers = make(map[int]func())
	}
	c.kanbanWatchers[id] = onChange
	c.kanbanMu.Unlock()

	return func() {
		c.kanbanMu.Lock()
		delete(c.kanbanWatchers, id)
		c.kanbanMu.Unlock()
	}
}
