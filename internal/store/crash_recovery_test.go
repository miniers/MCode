package store

import (
	"context"
	"testing"
	"time"
)

func TestFinalizeInterruptedTurnUsesLastActivityAndSeparateToolTime(t *testing.T) {
	s, threadID := newCrashRecoveryStore(t)
	ctx := context.Background()
	started := time.Date(2026, 11, 6, 1, 0, 0, 0, time.UTC)
	if err := s.CreateTurn(ctx, Turn{ID: "turn-timing", ThreadID: threadID, TurnIndex: 0, Status: "running", StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	activity := started.Add(2 * time.Minute)
	if _, err := s.AppendMessage(ctx, Message{ID: "user-timing", ThreadID: threadID, Role: "user", Text: "x", TurnID: "turn-timing", CreatedAt: activity}); err != nil {
		t.Fatal(err)
	}
	running := "running"
	if _, err := s.AppendMessage(ctx, Message{ID: "tool-timing", ThreadID: threadID, Role: "tool", TurnID: "turn-timing", CreatedAt: started.Add(time.Minute), ToolStatus: &running}); err != nil {
		t.Fatal(err)
	}
	recoveryAt := started.Add(time.Hour)
	toolEndedAt := recoveryAt.Add(time.Second)
	if err := s.FinalizeInterruptedTurn(ctx, "turn-timing", recoveryAt, toolEndedAt, "宿主重启", "已停止"); err != nil {
		t.Fatal(err)
	}
	turn, err := s.GetTurn(ctx, "turn-timing")
	if err != nil {
		t.Fatal(err)
	}
	if turn.CompletedAt == nil || !turn.CompletedAt.Equal(activity) {
		t.Fatalf("turn completion = %v, want last activity %v", turn.CompletedAt, activity)
	}
	tool := toolsByID(t, s, threadID)["tool-timing"]
	if tool.ToolCompletedAt == nil || *tool.ToolCompletedAt != formatTime(toolEndedAt) {
		t.Fatalf("tool completion = %v, want recovery time %v", tool.ToolCompletedAt, toolEndedAt)
	}
}
