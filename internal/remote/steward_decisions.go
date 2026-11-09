package remote

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

const stewardDecisionsListMethod = "steward.decisions.list"
const maxStewardAnswerRunes = 65536

type stewardDecisionOption struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}

type remoteStewardDecision struct {
	ID          string                  `json:"id"`
	TaskID      string                  `json:"taskID"`
	ThreadID    string                  `json:"threadID"`
	Question    string                  `json:"question"`
	Options     []stewardDecisionOption `json:"options"`
	Recommended string                  `json:"recommended"`
	Status      string                  `json:"status"`
	Answer      string                  `json:"answer"`
	AnsweredBy  string                  `json:"answeredBy"`
	AnsweredAt  *string                 `json:"answeredAt"`
	CreatedAt   string                  `json:"createdAt"`
}

type stewardDecisionsListResult struct {
	Decisions []remoteStewardDecision `json:"decisions"`
	Total     int                     `json:"total"`
}

func init() {
	RegisterMethod(stewardDecisionsListMethod, handleStewardDecisionsList)
	RegisterMethod("steward.decisions.answer", handleStewardDecisionsAnswer)
}

// AnswerStewardDecision 将回答交给当前前台。
func (b *MemoryBackend) AnswerStewardDecision(ctx context.Context, id, answer, by string) error {
	b.mu.Lock()
	desk := b.stewardDesk
	b.mu.Unlock()
	receiver, ok := desk.(interface {
		AnswerStewardDecision(context.Context, string, string, string) error
	})
	if !ok {
		return ExtNotImplemented()
	}
	return receiver.AnswerStewardDecision(ctx, id, answer, by)
}

func handleStewardDecisionsAnswer(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p struct {
		ID     string `json:"id"`
		Answer string `json:"answer"`
	}
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.Answer) == "" {
		return nil, ExtInvalidParams("Invalid params: 'id' and 'answer' are required")
	}
	if utf8.RuneCountInString(p.Answer) > maxStewardAnswerRunes {
		return nil, ExtInvalidParams("Invalid params: 'answer' exceeds 65536 characters")
	}
	backend, ok := d.backend.(*MemoryBackend)
	if !ok || backend == nil {
		return nil, ExtNotImplemented()
	}
	if err := backend.AnswerStewardDecision(ctx, p.ID, p.Answer, "remote"); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			return nil, &ExtError{Code: wire.ErrCodeResourceNotFound, Message: "Resource not found"}
		case errors.Is(err, store.ErrDecisionClosed):
			return nil, &ExtError{Code: wire.ErrCodeOperationRejected, Message: err.Error()}
		default:
			return nil, err
		}
	}
	return struct {
		OK bool `json:"ok"`
	}{OK: true}, nil
}

func handleStewardDecisionsList(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p struct {
		Status string `json:"status"`
		Limit  int    `json:"limit"`
	}
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	switch p.Status {
	case "", store.DecisionStatusOpen, store.DecisionStatusAnswered, store.DecisionStatusIgnored, store.DecisionStatusSuperseded:
	default:
		return nil, ExtInvalidParams("Invalid params: invalid 'status'")
	}
	if p.Limit < 0 {
		return nil, ExtInvalidParams("Invalid params: 'limit' must be non-negative")
	}
	if p.Limit == 0 {
		p.Limit = 50
	} else if p.Limit > 200 {
		p.Limit = 200
	}
	backend, ok := d.backend.(*MemoryBackend)
	if !ok || backend == nil {
		return nil, ExtNotImplemented()
	}
	result := stewardDecisionsListResult{Decisions: []remoteStewardDecision{}}
	st := backend.Store()
	if st == nil {
		return result, nil
	}
	var err error
	result.Total, err = st.CountStewardDecisions(ctx, p.Status)
	if err != nil {
		return nil, err
	}
	decisions, err := st.ListStewardDecisions(ctx, p.Status, p.Limit)
	if err != nil {
		return nil, err
	}
	for _, decision := range decisions {
		options := []stewardDecisionOption{}
		if err := json.Unmarshal([]byte(decision.OptionsJSON), &options); err != nil || options == nil {
			options = []stewardDecisionOption{}
		}
		var answeredAt *string
		if decision.AnsweredAt != nil {
			formatted := decision.AnsweredAt.UTC().Format(time.RFC3339)
			answeredAt = &formatted
		}
		result.Decisions = append(result.Decisions, remoteStewardDecision{
			ID: decision.ID, TaskID: decision.TaskID, ThreadID: decision.ThreadID,
			Question: decision.Question, Options: options, Recommended: decision.Recommended,
			Status: decision.Status, Answer: decision.Answer, AnsweredBy: decision.AnsweredBy,
			AnsweredAt: answeredAt, CreatedAt: decision.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return result, nil
}
