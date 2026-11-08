package remote

import (
	"context"
	"encoding/json"
	"time"

	"mcode/internal/store"
)

const stewardDecisionsListMethod = "steward.decisions.list"

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
}

func init() {
	RegisterMethod(stewardDecisionsListMethod, handleStewardDecisionsList)
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
