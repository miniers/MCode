package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"mcode/internal/agent/llm"
	"mcode/internal/frontdesk"
	"mcode/internal/kanban"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

type frontdeskServices struct {
	store            *store.Store
	backend          *remote.MemoryBackend
	kanban           *kanban.Service
	homeID, homePath string
	mu               sync.Mutex
}

func (s *frontdeskServices) ListTasks(ctx context.Context, project string) ([]store.Task, error) {
	return s.store.ListTasks(ctx, project)
}
func (s *frontdeskServices) GetTask(ctx context.Context, id string) (store.Task, error) {
	return s.store.GetTask(ctx, id)
}
func (s *frontdeskServices) List(ctx context.Context) ([]frontdesk.Project, error) {
	list, err := s.backend.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	var result []frontdesk.Project
	for _, p := range list.Projects {
		if p.ID != s.homeID && (s.homePath == "" || p.Path == "" || filepath.Clean(p.Path) != filepath.Clean(s.homePath)) {
			result = append(result, frontdesk.Project{ID: p.ID, Name: p.Name, Path: p.Path})
		}
	}
	return result, nil
}
func (s *frontdeskServices) CreateTask(ctx context.Context, in frontdesk.CreateTaskInput) (store.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	projects, err := s.List(ctx)
	if err != nil {
		return store.Task{}, err
	}
	projectID := in.ProjectID
	if projectID == "" {
		var latest time.Time
		for _, p := range projects {
			threads, e := s.store.ListThreads(ctx, p.ID, false)
			if e != nil {
				return store.Task{}, e
			}
			if projectID == "" {
				projectID = p.ID
			}
			for _, th := range threads {
				if th.UpdatedAt.After(latest) {
					latest = th.UpdatedAt
					projectID = p.ID
				}
			}
		}
	}
	valid := false
	for _, p := range projects {
		if p.ID == projectID {
			valid = true
			break
		}
	}
	if !valid {
		return store.Task{}, errors.New("没有可用的项目")
	}
	goals, err := s.store.ListGoals(ctx, projectID)
	if err != nil {
		return store.Task{}, err
	}
	var goal store.Goal
	for _, g := range goals {
		if g.Title == "总管" && g.Owner == "steward" && (g.Status == "running" || g.Status == "paused" || g.Status == "completed") {
			goal = g
			break
		}
	}
	if goal.ID == "" {
		goal = store.Goal{ID: uuid.NewString(), ProjectID: projectID, Title: "总管", Prompt: "总管", Status: "running", Owner: "steward", Policy: map[string]any{"maxConcurrency": 1}}
		if err = s.store.CreateGoal(ctx, goal); err != nil {
			return store.Task{}, err
		}
		goal, err = s.store.GetGoal(ctx, goal.ID)
		if err != nil {
			return store.Task{}, err
		}
	}
	if goal.Status == "completed" {
		goal.Status = "running"
		goal, err = s.store.UpdateGoal(ctx, goal, goal.Revision)
		if err != nil {
			return store.Task{}, err
		}
	}
	priority := kanban.Priority(in.Priority)
	if !priority.Valid() {
		priority = kanban.PriorityNone
	}
	if strings.TrimSpace(in.Title) == "" {
		return store.Task{}, errors.New("任务标题不能为空")
	}
	t := store.Task{ID: uuid.NewString(), ProjectID: &projectID, GoalID: &goal.ID, Title: in.Title, Notes: in.Body, Status: "todo", Priority: string(priority), DependsOn: []string{}, Meta: kanban.EncodeTaskMeta(kanban.TaskMeta{Origin: "owner", Provider: in.Provider, Model: in.Model})}
	if err := s.store.CreateGoalTask(ctx, t, goal.Revision); err != nil {
		return store.Task{}, err
	}
	return s.store.GetTask(ctx, t.ID)
}
func (s *frontdeskServices) AmendTask(ctx context.Context, id, text string) error {
	t, err := s.store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	meta, err := kanban.ParseTaskMeta(t.Meta)
	if err != nil {
		return err
	}
	if meta.DecisionID != "" {
		d, err := s.store.GetStewardDecision(ctx, meta.DecisionID)
		if err != nil {
			return err
		}
		if d.Status == store.DecisionStatusOpen {
			return s.AnswerDecision(ctx, d.ID, text, "owner")
		}
	}
	if t.Status == "in_progress" {
		if t.ThreadID == nil {
			return errors.New("任务没有执行会话")
		}
		_, err = s.backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: *t.ThreadID, Text: text})
		return err
	}
	if t.Notes != "" {
		t.Notes += "\n"
	}
	t.Notes += text
	switch t.Status {
	case "waiting", "done", "failed", "cancelled", "rejected":
		t.Status = "todo"
		t.ThreadID, t.ReviewThreadID = nil, nil
		t.ReviewSummary, t.ReviewFeedback, t.ReviewStatus = nil, nil, nil
		t.LastFailureReason, t.LastActivityText = nil, nil
		t.AttemptCount, t.RejectCount = 0, 0
		meta.ResultStatus, meta.Summary, meta.DecisionID, meta.GateOverride = "", "", "", ""
		meta.WaitKind, meta.WakeAt, meta.WaitCheck, meta.WaitBase, meta.Wakes = "", "", "", "", 0
		meta.NoChange = false
		t.Meta = kanban.EncodeTaskMeta(meta)
		if t.GoalID != nil {
			goal, err := s.store.GetGoal(ctx, *t.GoalID)
			if err != nil {
				return err
			}
			if goal.Owner == "steward" && goal.Title == "总管" && goal.Status == "completed" {
				goal.Status = "running"
				if _, err := s.store.UpdateGoal(ctx, goal, goal.Revision); err != nil {
					return err
				}
			}
		}
	case "todo":
	default:
		return fmt.Errorf("不能追加任务状态 %s", t.Status)
	}
	_, err = s.store.UpdateTask(ctx, t, t.Revision)
	return err
}
func (s *frontdeskServices) CancelTask(ctx context.Context, id string) error {
	t, e := s.store.GetTask(ctx, id)
	if e != nil {
		return e
	}
	_, e = s.kanban.MoveTask(ctx, id, kanban.TaskStatus("cancelled"), t.Revision)
	return e
}
func (s *frontdeskServices) SetPriority(ctx context.Context, id, priority string) error {
	p := kanban.Priority(priority)
	if !p.Valid() {
		return errors.New("无效的优先级")
	}
	t, e := s.store.GetTask(ctx, id)
	if e != nil {
		return e
	}
	t.Priority = priority
	_, e = s.store.UpdateTask(ctx, t, t.Revision)
	return e
}
func (s *frontdeskServices) ReassignTask(ctx context.Context, id, provider, model string) error {
	t, e := s.store.GetTask(ctx, id)
	if e != nil {
		return e
	}
	if t.Status != "todo" && t.Status != "waiting" {
		return errors.New("任务正在执行或已结束")
	}
	_, e = s.kanban.PatchTaskMeta(ctx, id, t.Revision, func(m *kanban.TaskMeta) { m.Provider = provider; m.Model = model })
	return e
}
func (s *frontdeskServices) RecentTask(ctx context.Context, threadID string) (store.Task, time.Time, error) {
	messages, _, err := s.store.ListMessages(ctx, threadID, 0, 100)
	if err != nil {
		return store.Task{}, time.Time{}, err
	}
	tasks, err := s.store.ListTasks(ctx, "")
	if err != nil {
		return store.Task{}, time.Time{}, err
	}
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != "assistant" && m.Role != "system" {
			continue
		}
		var payload struct {
			Task   string `json:"task"`
			TaskID string `json:"task_id"`
		}
		id := ""
		if (m.Kind == "steward_result" || m.Kind == "steward_decision") && json.Unmarshal([]byte(m.Text), &payload) == nil {
			if m.Kind == "steward_result" {
				id = payload.Task
			} else {
				id = payload.TaskID
			}
			if id != "" {
				task, err := s.store.GetTask(ctx, id)
				return task, m.CreatedAt, err
			}
		}
		var match store.Task
		for _, task := range tasks {
			if (task.ID != "" && strings.Contains(m.Text, task.ID)) || (task.Title != "" && strings.Contains(m.Text, task.Title)) {
				if match.ID != "" && match.ID != task.ID {
					return store.Task{}, time.Time{}, store.ErrNotFound
				}
				match = task
			}
		}
		if match.ID != "" {
			return match, m.CreatedAt, nil
		}
	}
	return store.Task{}, time.Time{}, store.ErrNotFound
}
func (s *frontdeskServices) ListDecisions(ctx context.Context, status string, limit int) ([]store.StewardDecision, error) {
	return s.store.ListStewardDecisions(ctx, status, limit)
}
func (s *frontdeskServices) AnswerDecision(ctx context.Context, id, answer, by string) error {
	d, e := s.kanban.AnswerDecision(ctx, id, answer, by)
	if e != nil {
		return e
	}
	payload, e := json.Marshal(map[string]string{"id": d.ID, "answer": d.Answer})
	if e != nil {
		return e
	}
	feedID, _, err := s.store.GetSetting(ctx, "steward.feed_thread_id")
	if err != nil {
		return err
	}
	return s.backend.AppendStewardMessage(ctx, feedID, "system", "steward_decision_answer", string(payload))
}
func (s *frontdeskServices) AppendAssistant(ctx context.Context, thread, kind, text string) error {
	return s.backend.AppendStewardMessage(ctx, thread, "assistant", kind, text)
}
func (s *frontdeskServices) SetBusy(thread string, busy bool) {
	s.backend.SetStewardBusy(thread, busy)
}
func (s *frontdeskServices) Recent(ctx context.Context, thread string, limit int) ([]llm.Message, error) {
	messages, _, err := s.store.ListMessages(ctx, thread, 0, limit)
	if err != nil {
		return nil, err
	}
	out := make([]llm.Message, 0, len(messages))
	for _, m := range messages {
		if m.Role == "user" || m.Role == "assistant" {
			out = append(out, llm.Message{Role: m.Role, Content: []llm.Part{{Type: "text", Text: m.Text}}})
		}
	}
	return out, nil
}
