package remote

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// TestSessionState_FailureReasonFromRecentTurnError 验证当最近一轮执行失败并记录错误信息时，
// SessionState 会将 FailureReason 提取为该轮次的真实错误文本（去除首尾空白）。
func TestSessionState_FailureReasonFromRecentTurnError(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("attach store: %v", err)
	}

	backend.AddProject(wire.ProjectSummary{ID: "p1", Path: t.TempDir()})

	thID := "thread-failed-pi"
	lastStatus := "failed"
	summary := wire.ThreadSummary{
		ID:         thID,
		ProjectID:  "p1",
		Title:      "Failed Pi Thread",
		LastStatus: &lastStatus,
		Attention:  "failed",
	}
	backend.AddThread(summary, nil)

	turnErr := `pi: start: exec: "pi": executable file not found in $PATH`
	turn := store.Turn{
		ID:           "turn-pi-1",
		ThreadID:     thID,
		TurnIndex:    1,
		Status:       "failed",
		StartedAt:    time.Now().UTC(),
		ErrorMessage: "  " + turnErr + "  \n",
	}
	if err := s.CreateTurn(ctx, turn); err != nil {
		t.Fatalf("create turn: %v", err)
	}

	st, err := backend.SessionState(ctx, thID)
	if err != nil {
		t.Fatalf("SessionState failed: %v", err)
	}
	if !st.Failed {
		t.Fatalf("expected Failed to be true, got false")
	}
	if !strings.Contains(st.FailureReason, "executable file not found") {
		t.Fatalf("expected FailureReason to contain %q, got %q", "executable file not found", st.FailureReason)
	}
	if st.FailureReason != turnErr {
		t.Fatalf("expected FailureReason to equal %q, got %q", turnErr, st.FailureReason)
	}
}

// TestSessionState_FailureReasonFallbackWhenNoError 验证当最近一轮没有错误信息或 store 中无轮次时，
// FailureReason 正确回退到状态字。
func TestSessionState_FailureReasonFallbackWhenNoError(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("attach store: %v", err)
	}

	backend.AddProject(wire.ProjectSummary{ID: "p1", Path: t.TempDir()})

	// 1. 最近一轮 ErrorMessage 为空白，回退到 LastStatus 状态字
	thID1 := "thread-failed-empty-err"
	lastStatus := "failed"
	summary1 := wire.ThreadSummary{
		ID:         thID1,
		ProjectID:  "p1",
		Title:      "Failed Thread Empty Err",
		LastStatus: &lastStatus,
		Attention:  "failed",
	}
	backend.AddThread(summary1, nil)

	turn := store.Turn{
		ID:           "turn-empty-err-1",
		ThreadID:     thID1,
		TurnIndex:    1,
		Status:       "failed",
		StartedAt:    time.Now().UTC(),
		ErrorMessage: "   \t\n",
	}
	if err := s.CreateTurn(ctx, turn); err != nil {
		t.Fatalf("create turn: %v", err)
	}

	st1, err := backend.SessionState(ctx, thID1)
	if err != nil {
		t.Fatalf("SessionState failed: %v", err)
	}
	if !st1.Failed {
		t.Fatalf("expected Failed to be true, got false")
	}
	if st1.FailureReason != "failed" {
		t.Fatalf("expected FailureReason to fallback to 'failed', got %q", st1.FailureReason)
	}

	// 2. 无 turn 且仅有 Attention="failed" 时，回退到 "attention: failed"
	thID2 := "thread-attention-failed"
	summary2 := wire.ThreadSummary{
		ID:        thID2,
		ProjectID: "p1",
		Title:     "Attention Failed Thread",
		Attention: "failed",
	}
	backend.AddThread(summary2, nil)

	st2, err := backend.SessionState(ctx, thID2)
	if err != nil {
		t.Fatalf("SessionState failed: %v", err)
	}
	if !st2.Failed {
		t.Fatalf("expected Failed to be true, got false")
	}
	if st2.FailureReason != "attention: failed" {
		t.Fatalf("expected FailureReason to fallback to 'attention: failed', got %q", st2.FailureReason)
	}
}
