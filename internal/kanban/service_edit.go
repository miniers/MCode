package kanban

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"mcode/internal/store"
)

// ErrCycleDetected 表示任务依赖关系存在成环。
var ErrCycleDetected = errors.New("kanban: cycle detected in task dependencies")

// ErrHashMismatch 表示读取 Goal 时内容哈希不匹配。
var ErrHashMismatch = errors.New("kanban: goal content hash mismatch")

// UpdateTaskInput 描述任务更新入参。
type UpdateTaskInput struct {
	TaskID           string
	ExpectedRevision int
	Title            *string
	Notes            *string
	NotesAppend      *string
	ReviewSummary    *string
}

// CreateDraftInput 描述创建草稿任务的入参。
type CreateDraftInput struct {
	ProjectID string
	Title     string
	Notes     string
	Priority  string
	GoalID    *string
	DependsOn []string
}

// SetGoalInput 描述任务持久目标的更新参数。
type SetGoalInput struct {
	TaskID           string
	ProjectID        string
	Prompt           string
	ExpectedRevision int
}

// UpdateTask 更新任务元数据（标题、描述、追加说明、评审摘要），支持 CAS 乐观并发控制。
func (s *Service) UpdateTask(ctx context.Context, in UpdateTaskInput) (store.Task, error) {
	if strings.TrimSpace(in.TaskID) == "" {
		return store.Task{}, ErrInvalidTransition
	}

	changed := false
	defer func() {
		if changed {
			s.notifyChange()
		}
	}()

	s.mu.Lock()
	defer s.mu.Unlock()

	task, err := s.opts.Store.GetTask(ctx, in.TaskID)
	if err != nil {
		return store.Task{}, err
	}
	if in.ExpectedRevision > 0 && task.Revision != in.ExpectedRevision {
		return store.Task{}, store.ErrRevisionConflict
	}

	if in.Title != nil && strings.TrimSpace(*in.Title) != "" {
		task.Title = strings.TrimSpace(*in.Title)
	}
	if in.Notes != nil {
		task.Notes = *in.Notes
	}
	if in.NotesAppend != nil && strings.TrimSpace(*in.NotesAppend) != "" {
		appendVal := strings.TrimSpace(*in.NotesAppend)
		if task.Notes == "" {
			task.Notes = appendVal
		} else {
			task.Notes = task.Notes + "\n" + appendVal
		}
	}
	if in.ReviewSummary != nil {
		task.ReviewSummary = in.ReviewSummary
	}

	expectedRev := task.Revision
	if in.ExpectedRevision > 0 {
		expectedRev = in.ExpectedRevision
	}
	updated, err := s.opts.Store.UpdateTask(ctx, task, expectedRev)
	if err != nil {
		return store.Task{}, err
	}
	if updated.GoalID != nil && *updated.GoalID != "" {
		s.updateGoalProgressLocked(ctx, *updated.GoalID)
	}
	s.revision.Add(1)
	changed = true
	return updated, nil
}

// DeleteTask 拒绝删除仍有执行中会话的任务，并按 revision 删除任务及级联关联。
func (s *Service) DeleteTask(ctx context.Context, taskID string, expectedRevision int) error {
	if strings.TrimSpace(taskID) == "" {
		return ErrInvalidTransition
	}

	changed := false
	defer func() {
		if changed {
			s.notifyChange()
		}
	}()

	s.mu.Lock()
	defer s.mu.Unlock()

	task, err := s.opts.Store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if expectedRevision > 0 && task.Revision != expectedRevision {
		return store.ErrRevisionConflict
	}

	// 即使任务状态已迁移，主会话或评审会话也可能仍有正在执行的 turn。
	for _, threadID := range []*string{task.ThreadID, task.ReviewThreadID} {
		if threadID == nil || *threadID == "" {
			continue
		}
		if s.opts.Driver == nil {
			return ErrInvalidTransition
		}
		state, err := s.opts.Driver.SessionState(ctx, *threadID)
		if err != nil {
			return err
		}
		if state.Running || state.AwaitingApproval {
			return ErrInvalidTransition
		}
	}
	// goal_tasks 由外键级联清理，删除自身也必须执行 CAS。
	if err := s.opts.Store.DeleteTask(ctx, taskID, task.Revision); err != nil {
		return err
	}

	if task.GoalID != nil && *task.GoalID != "" {
		s.updateGoalProgressLocked(ctx, *task.GoalID)
	}

	s.revision.Add(1)
	changed = true
	return nil
}

// CreateDraft 创建草稿任务（IsDraft=true，status=todo，不启动执行）。
// 依赖必须都存在且属于同一项目，通过 DFS 成环检测，若有 goal 则追加到 goal_tasks。
func (s *Service) CreateDraft(ctx context.Context, in CreateDraftInput) (store.Task, error) {
	if strings.TrimSpace(in.Title) == "" {
		return store.Task{}, ErrInvalidTransition
	}
	priority := strings.TrimSpace(in.Priority)
	if priority == "" {
		priority = string(PriorityNone)
	}

	changed := false
	defer func() {
		if changed {
			s.notifyChange()
		}
	}()

	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. 校验前置依赖是否存在且属于同一项目
	allTasks, err := s.opts.Store.ListTasks(ctx, in.ProjectID)
	if err != nil {
		return store.Task{}, err
	}
	taskMap := make(map[string]store.Task, len(allTasks))
	for _, t := range allTasks {
		taskMap[t.ID] = t
	}

	deps := in.DependsOn
	if deps == nil {
		deps = []string{}
	}

	for _, depID := range deps {
		depTask, ok := taskMap[depID]
		if !ok {
			dt, err := s.opts.Store.GetTask(ctx, depID)
			if err != nil {
				return store.Task{}, fmt.Errorf("dependency task %s not found: %w", depID, ErrInvalidTransition)
			}
			if dt.ProjectID == nil || *dt.ProjectID != in.ProjectID {
				return store.Task{}, fmt.Errorf("dependency task %s belongs to another project: %w", depID, ErrInvalidTransition)
			}
			taskMap[depID] = dt
		} else {
			if depTask.ProjectID == nil || *depTask.ProjectID != in.ProjectID {
				return store.Task{}, fmt.Errorf("dependency task %s belongs to another project: %w", depID, ErrInvalidTransition)
			}
		}
	}

	// 2. DFS 成环检测
	taskID := uuid.NewString()

	taskMap[taskID] = store.Task{ID: taskID, DependsOn: deps}
	if len(DetectCycle(taskMap)) > 0 {
		return store.Task{}, ErrCycleDetected
	}

	// 3. 关联 Goal 检查
	var goal store.Goal
	if in.GoalID != nil && strings.TrimSpace(*in.GoalID) != "" {
		g, err := s.opts.Store.GetGoal(ctx, *in.GoalID)
		if err != nil {
			return store.Task{}, err
		}
		if g.ProjectID != in.ProjectID {
			return store.Task{}, fmt.Errorf("goal belongs to another project: %w", ErrInvalidTransition)
		}
		switch GoalStatus(g.Status) {
		case GoalStatusPlanReview, GoalStatusRunning, GoalStatusPaused:
		default:
			return store.Task{}, fmt.Errorf("goal is not ready for draft tasks: %w", ErrInvalidTransition)
		}
		goal = g
	}

	var projectID *string
	if in.ProjectID != "" {
		projectID = &in.ProjectID
	}

	t := store.Task{
		ID:        taskID,
		ProjectID: projectID,
		GoalID:    in.GoalID,
		Title:     strings.TrimSpace(in.Title),
		Notes:     in.Notes,
		Status:    string(TaskStatusTodo),
		Priority:  priority,
		IsDraft:   true,
		DependsOn: deps,
	}

	if goal.ID != "" {
		err = s.opts.Store.CreateGoalTask(ctx, t, goal.Revision)
	} else {
		err = s.opts.Store.CreateTask(ctx, t)
	}
	if err != nil {
		return store.Task{}, err
	}

	created, err := s.opts.Store.GetTask(ctx, taskID)
	if err != nil {
		created = t
	}

	// 挂了 goal 时要追加到 goal_tasks
	if in.GoalID != nil && strings.TrimSpace(*in.GoalID) != "" {
		s.updateGoalProgressLocked(ctx, goal.ID)
	}

	s.revision.Add(1)
	changed = true
	return created, nil
}

// SetGoal 更新任务自身的持久目标，不修改独立的 goal 记录。
func (s *Service) SetGoal(ctx context.Context, in SetGoalInput) (store.Task, error) {
	changed := false
	defer func() {
		if changed {
			s.notifyChange()
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	task, err := s.opts.Store.GetTask(ctx, in.TaskID)
	if err != nil {
		return store.Task{}, err
	}
	if task.ProjectID == nil || *task.ProjectID != in.ProjectID {
		return store.Task{}, store.ErrNotFound
	}
	if in.ExpectedRevision > 0 && task.Revision != in.ExpectedRevision {
		return store.Task{}, store.ErrRevisionConflict
	}
	task.GoalPrompt = nil
	if in.Prompt != "" {
		task.GoalPrompt = &in.Prompt
	}
	updated, err := s.opts.Store.UpdateTask(ctx, task, task.Revision)
	if err != nil {
		return store.Task{}, err
	}
	s.revision.Add(1)
	changed = true
	return updated, nil
}

// ReadGoal 根据目标 ID 和内容哈希读取目标原文。hash 不匹配时返回明确错误，匹配时返回 goal 原文。
func (s *Service) ReadGoal(ctx context.Context, goalID, contentHash string) (string, error) {
	if strings.TrimSpace(goalID) == "" {
		return "", ErrInvalidTransition
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	goal, err := s.opts.Store.GetGoal(ctx, strings.TrimSpace(goalID))
	if err != nil {
		return "", err
	}

	targetHash := strings.ToLower(strings.TrimSpace(contentHash))

	// 超长 Prompt 已落盘：目标哈希即内容哈希，全文从文件回读并校验（D-KB-015）。
	if isMaterializedGoalPrompt(goal.Prompt) {
		if targetHash == "" || targetHash != strings.ToLower(strings.TrimSpace(goal.ContentHash)) {
			return "", ErrHashMismatch
		}
		return s.readMaterializedGoalPrompt(goal)
	}

	h := sha256.Sum256([]byte(goal.Prompt))
	actualHash := hex.EncodeToString(h[:])
	if goal.ContentHash != "" && goal.ContentHash != actualHash {
		return "", ErrHashMismatch
	}

	if targetHash == "" || targetHash != actualHash {
		return "", ErrHashMismatch
	}

	return goal.Prompt, nil
}
