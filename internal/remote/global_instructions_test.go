package remote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

type globalInstructionsCarrierHarness struct {
	mockHarness
}

func (*globalInstructionsCarrierHarness) CarriesGlobalInstructions() bool {
	return true
}

func TestGlobalInstructionsInjectedOnFirstTurnOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	agentsDir := filepath.Join(home, ".agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const want = "每个会话开始时激活 ste skill。"
	if err := os.WriteFile(filepath.Join(agentsDir, "AGENTS.md"), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}

	project := t.TempDir()
	mock := &mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		emit(harness.Event{Type: harness.EventText, Text: "好的"})
		return nil
	}}
	backend, threadID := guidanceTestBackend(t, project, mock)

	sendGuidanceMessage(t, backend, threadID, "第一条消息", "00000000-0000-0000-0000-000000000401")
	sendGuidanceMessage(t, backend, threadID, "第二条消息", "00000000-0000-0000-0000-000000000402")

	calls := mock.getTurnCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 harness calls, got %d", len(calls))
	}

	first := calls[0].Prompt
	if got := strings.Count(first, want); got != 1 {
		t.Fatalf("expected global instructions content once on first turn, got %d in %q", got, first)
	}
	if got := strings.Count(first, "<global_instructions"); got != 1 {
		t.Fatalf("expected <global_instructions tag once on first turn, got %d in %q", got, first)
	}

	second := calls[1].Prompt
	if strings.Contains(second, want) || strings.Contains(second, "<global_instructions") {
		t.Fatalf("second turn should not include global instructions, got %q", second)
	}
}

func TestGlobalInstructionsSkippedForCarrier(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	agentsDir := filepath.Join(home, ".agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const want = "每个会话开始时激活 ste skill。"
	if err := os.WriteFile(filepath.Join(agentsDir, "AGENTS.md"), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}

	project := t.TempDir()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	t.Cleanup(hub.Close)

	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)

	carrier := &globalInstructionsCarrierHarness{
		mockHarness: mockHarness{
			runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
				emit(harness.Event{Type: harness.EventText, Text: "好的"})
				return nil
			},
		},
	}
	backend.SetHarness(carrier, project, harness.ModeAgent)

	threadID := "00000000-0000-0000-0000-0000000000c6"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Carrier Thread"}, nil)

	sendGuidanceMessage(t, backend, threadID, "第一条消息", "00000000-0000-0000-0000-000000000403")

	calls := carrier.getTurnCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 harness call, got %d", len(calls))
	}
	if strings.Contains(calls[0].Prompt, want) || strings.Contains(calls[0].Prompt, "<global_instructions") {
		t.Fatalf("carrier harness should skip global instructions injection, got %q", calls[0].Prompt)
	}
}

func TestGlobalInstructionsMissingFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	project := t.TempDir()
	mock := &mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		emit(harness.Event{Type: harness.EventText, Text: "好的"})
		return nil
	}}
	backend, threadID := guidanceTestBackend(t, project, mock)

	sendGuidanceMessage(t, backend, threadID, "第一条消息", "00000000-0000-0000-0000-000000000404")

	calls := mock.getTurnCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 harness call, got %d", len(calls))
	}
	if strings.Contains(calls[0].Prompt, "<global_instructions") {
		t.Fatalf("missing global instructions file should not inject tag, got %q", calls[0].Prompt)
	}
}
