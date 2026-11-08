package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestReanswerStewardDecision(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "reanswer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	open, err := s.CreateStewardDecision(ctx, StewardDecision{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReanswerStewardDecision(ctx, open.ID, "B", "user", time.Now()); !errors.Is(err, ErrDecisionClosed) {
		t.Fatalf("open 卡改答: %v", err)
	}

	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	answered, err := s.AnswerStewardDecision(ctx, open.ID, "A", "user", at)
	if err != nil {
		t.Fatal(err)
	}
	updatedAt := at.Add(time.Minute)
	reanswered, err := s.ReanswerStewardDecision(ctx, open.ID, "B", "other", updatedAt)
	if err != nil || reanswered.Answer != "B" || reanswered.AnsweredBy != "other" || reanswered.AnsweredAt == nil || !reanswered.AnsweredAt.Equal(updatedAt) {
		t.Fatalf("已答卡改答: %+v, %v", reanswered, err)
	}
	if answered.Answer != "A" {
		t.Fatalf("首答结果被修改: %+v", answered)
	}
}
