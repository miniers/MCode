package remote

import (
	"context"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

func newReviewRuntimeBackend(t *testing.T) *MemoryBackend {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)
	b := NewMemoryBackend(hub)
	t.Cleanup(b.Close)
	b.SetHarness(&mockHarness{kind: "claude"}, t.TempDir(), harness.ModeFullAccess)
	b.AddProject(wire.ProjectSummary{ID: "p1", Path: t.TempDir()})
	return b
}

func TestLeadRuntime_FollowsLeadModel(t *testing.T) {
	ctx := context.Background()
	b := newReviewRuntimeBackend(t)

	model := "sonnet"
	lead, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1", Model: &model})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.SetThreadComposerPrefs(ctx, lead.ThreadID, ThreadComposerPrefs{
		InteractionMode: "build",
		Effort:          "high",
	}); err != nil {
		t.Fatal(err)
	}

	provider, gotModel, effort, ok := b.LeadRuntime(ctx, lead.ThreadID)
	if !ok {
		t.Fatal("应返回主会话当前模型")
	}
	if provider != "claude" || gotModel != "sonnet" || effort != "high" {
		t.Fatalf("返回值不符: %q/%q/%q", provider, gotModel, effort)
	}
}

func TestLeadRuntime_MissingThread(t *testing.T) {
	b := newReviewRuntimeBackend(t)
	if _, _, _, ok := b.LeadRuntime(context.Background(), "missing-thread"); ok {
		t.Fatal("不存在的线程应返回 ok=false")
	}
}
