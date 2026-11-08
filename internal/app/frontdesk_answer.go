package app

import (
	"context"
	"errors"
)

// AnswerStewardDecision 将回答交给前台处理。
func (r frontdeskReceiver) AnswerStewardDecision(ctx context.Context, id, answer, by string) error {
	if r.desk == nil {
		return errors.New("前台不可用")
	}
	return r.desk.AnswerDecision(ctx, id, answer, by)
}
