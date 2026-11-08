package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStewardDecisions(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "decisions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, err := s.CreateStewardDecision(ctx, StewardDecision{TaskID: "task"})
	if err != nil || first.ID != "D-0001" {
		t.Fatalf("首张决策卡: %+v, %v", first, err)
	}
	second, err := s.CreateStewardDecision(ctx, StewardDecision{TaskID: "task"})
	if err != nil || second.ID != "D-0002" {
		t.Fatalf("第二张决策卡: %+v, %v", second, err)
	}
	old, err := s.GetStewardDecision(ctx, first.ID)
	if err != nil || old.Status != DecisionStatusSuperseded {
		t.Fatalf("旧卡状态: %+v, %v", old, err)
	}
	answered, err := s.AnswerStewardDecision(ctx, second.ID, "A", "user", time.Now())
	if err != nil || answered.Status != DecisionStatusAnswered || answered.AnsweredAt == nil {
		t.Fatalf("回答: %+v, %v", answered, err)
	}
	if _, err = s.AnswerStewardDecision(ctx, second.ID, "B", "user", time.Now()); !errors.Is(err, ErrDecisionClosed) {
		t.Fatalf("重复回答: %v", err)
	}
	list, err := s.ListStewardDecisions(ctx, DecisionStatusAnswered, 1)
	if err != nil || len(list) != 1 || list[0].ID != second.ID {
		t.Fatalf("筛选决策卡: %+v, %v", list, err)
	}
}

func TestStewardDecisionConcurrentIDs(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "concurrent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ids := make(chan string, 10)
	errs := make(chan error, 10)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := s.CreateStewardDecision(ctx, StewardDecision{})
			if err != nil {
				errs <- err
				return
			}
			ids <- d.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	seen := make(map[string]bool)
	for id := range ids {
		if seen[id] {
			t.Errorf("重复编号 %s", id)
		}
		seen[id] = true
	}
	for i := 1; i <= 10; i++ {
		if !seen[fmt.Sprintf("D-%04d", i)] {
			t.Errorf("缺少编号 %d", i)
		}
	}
}

func TestStewardDecisionIDBeyond9999(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "large.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := formatTime(time.Now())
	if _, err := s.db.ExecContext(ctx, `INSERT INTO steward_decisions (id, created_at, updated_at) VALUES ('D-9999', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"D-10000", "D-10001"} {
		d, err := s.CreateStewardDecision(ctx, StewardDecision{})
		if err != nil || d.ID != want {
			t.Fatalf("编号 = %s，期望 %s，错误 %v", d.ID, want, err)
		}
	}
}
