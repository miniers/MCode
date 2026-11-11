package app

import (
	"context"
	"mcode/internal/harness"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/swarm"
	"testing"
	"time"
)

type leadPlansCapture struct{ turns chan harness.Turn }

func (*leadPlansCapture) Kind() harness.Kind { return "lead-plans-capture" }
func (h *leadPlansCapture) Run(_ context.Context, turn harness.Turn, _ func(harness.Event)) error {
	h.turns <- turn
	return nil
}

func TestLeadPlansOnlyWiring(t *testing.T) {
	ctx := context.Background()
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	b := remote.NewMemoryBackend(hub)
	defer b.Close()
	h := &leadPlansCapture{turns: make(chan harness.Turn, 1)}
	b.SetHarness(h, t.TempDir(), harness.ModeAgent)
	mgr := swarm.NewManager(swarm.Options{Driver: b, WorktreeDir: t.TempDir()})
	var global bool
	var mode string
	mgr.SetSettingsFunc(func() swarm.Settings { return swarm.Settings{Enabled: global} })
	wireSwarmWithModes(ctx, mgr, b, nil, nil, func(string) string { return mode })
	for _, tc := range []struct {
		id              string
		global, enabled bool
		mode            string
		helper          bool
		parent          string
		want            bool
	}{
		{"global-off", false, true, swarm.ModeLeadPlansOnly, false, "", false},
		{"session-off", true, false, swarm.ModeLeadPlansOnly, false, "", false},
		{"collaborative", true, true, swarm.ModeCollaborative, false, "", false},
		{"always", true, true, swarm.ModeAlwaysBees, false, "", false},
		{"lead", true, true, swarm.ModeLeadPlansOnly, false, "", true},
		{"helper", true, true, swarm.ModeLeadPlansOnly, true, "", false},
		{"unregistered-bee", true, true, swarm.ModeLeadPlansOnly, false, "lead", false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			global, mode = tc.global, tc.mode
			b.AddThread(wire.ThreadSummary{ID: tc.id, ProjectID: "p", Provider: "lead-plans-capture"}, nil)
			if tc.helper || tc.parent != "" {
				b.SetThreadRelation(tc.id, tc.parent, tc.helper)
			}
			mgr.SetEnabled(tc.id, tc.enabled)
			if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: tc.id, Text: "hello"}); err != nil {
				t.Fatal(err)
			}
			select {
			case turn := <-h.turns:
				if turn.LeadPlansOnly != tc.want {
					t.Fatalf("LeadPlansOnly = %v", turn.LeadPlansOnly)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("turn timeout")
			}
		})
	}
}
