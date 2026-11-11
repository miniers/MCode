package kanban

import (
	"context"
	"encoding/json"

	"mcode/internal/store"
)

type TaskMeta struct {
	Origin          string   `json:"origin"`
	ParentID        string   `json:"parent,omitempty"`
	SourceMessageID string   `json:"source_message,omitempty"`
	AmendMessageIDs []string `json:"amend_messages,omitempty"`
	Children        []string `json:"children,omitempty"`
	ResultStatus    string   `json:"result_status,omitempty"`
	Summary         string   `json:"summary,omitempty"`
	WaitKind        string   `json:"wait_kind,omitempty"`
	WakeAt          string   `json:"wake_at,omitempty"`
	WaitCheck       string   `json:"wait_check,omitempty"`
	WaitBase        string   `json:"wait_check_base,omitempty"`
	Wakes           int      `json:"wakes,omitempty"`
	DecisionID      string   `json:"decision_id,omitempty"`
	GateOverride    string   `json:"gate_override,omitempty"`
	Follows         string   `json:"follows,omitempty"`
	Provider        string   `json:"provider,omitempty"`
	Model           string   `json:"model,omitempty"`
	NoChange        bool     `json:"no_change,omitempty"`
}

func ParseTaskMeta(s string) (TaskMeta, error) {
	var m TaskMeta
	if s == "" {
		return m, nil
	}
	err := json.Unmarshal([]byte(s), &m)
	return m, err
}

func EncodeTaskMeta(m TaskMeta) string {
	b, _ := json.Marshal(m)
	if string(b) == `{"origin":""}` {
		return "{}"
	}
	return string(b)
}

func (s *Service) PatchTaskMeta(ctx context.Context, taskID string, expectedRevision int, fn func(*TaskMeta)) (store.Task, error) {
	task, err := s.opts.Store.GetTask(ctx, taskID)
	if err != nil {
		return store.Task{}, err
	}
	if task.Revision != expectedRevision {
		return store.Task{}, store.ErrRevisionConflict
	}
	meta, err := ParseTaskMeta(task.Meta)
	if err != nil {
		return store.Task{}, err
	}
	fn(&meta)
	task.Meta = EncodeTaskMeta(meta)
	updated, err := s.opts.Store.UpdateTask(ctx, task, expectedRevision)
	if err != nil {
		return store.Task{}, err
	}
	s.revision.Add(1)
	s.notifyChange()
	return updated, nil
}
