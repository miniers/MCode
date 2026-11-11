package remote

import (
	"context"
	"encoding/json"

	"mcode/internal/remote/wire"
)

type reviewExtBackend interface {
	ReviewGet(context.Context, wire.ThreadsReviewGetParams) (wire.ThreadsReviewGetResult, error)
}

func init() {
	RegisterMethod("threads.review.get", handleThreadsReviewGet)
	RegisterMethod("threads.review.revertHunk", handleThreadsReviewRevertHunk)
}

func handleThreadsReviewGet(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.ThreadsReviewGetParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if !isValidUUID(p.ThreadID) {
		return nil, ExtInvalidParams("Invalid params: 'threadID' must be a valid UUID")
	}
	if !reviewScopeValid(p.Scope) {
		return nil, ExtInvalidParams("Invalid params: 'scope' must be turn, session or uncommitted")
	}
	if p.TurnID != "" && (!isValidUUID(p.TurnID) || p.Scope != wire.ReviewScopeTurn) {
		return nil, ExtInvalidParams("Invalid params: 'turnID' must be a valid UUID in turn scope")
	}
	// JSON null 不能代替可省略的字符串字段。
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return nil, ExtInvalidParams(err.Error())
	}
	for _, key := range []string{"turnID", "path"} {
		if raw, ok := fields[key]; ok && string(raw) == "null" {
			return nil, ExtInvalidParams("Invalid params: '" + key + "' must be a string")
		}
	}
	backend, ok := d.backend.(reviewExtBackend)
	if !ok {
		return nil, ExtNotImplemented()
	}
	result, err := backend.ReviewGet(ctx, p)
	if err != nil {
		return nil, revertExtWrapError(err)
	}
	return result, nil
}
