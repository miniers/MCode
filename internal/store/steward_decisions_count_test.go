package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCountStewardDecisions(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "decisions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, status := range []string{DecisionStatusOpen, DecisionStatusOpen, DecisionStatusOpen, DecisionStatusAnswered} {
		if _, err := st.CreateStewardDecision(ctx, StewardDecision{Question: "问题", Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		status string
		want   int
	}{{"", 4}, {DecisionStatusOpen, 3}, {DecisionStatusAnswered, 1}, {DecisionStatusIgnored, 0}} {
		count, err := st.CountStewardDecisions(ctx, tc.status)
		if err != nil || count != tc.want {
			t.Errorf("status %q: count = %d, err = %v, want %d", tc.status, count, err, tc.want)
		}
	}
}
