package app

import (
	"context"
	"testing"
)

func TestFrontdeskAnswerWithoutDesk(t *testing.T) {
	if err := (frontdeskReceiver{}).AnswerStewardDecision(context.Background(), "D-0001", "A", "owner"); err == nil {
		t.Fatal("前台为空时应返回错误")
	}
}
