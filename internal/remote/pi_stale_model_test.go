package remote

import (
	"context"
	"strings"
	"testing"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

type piFakeHarness struct{ fakeMediaHarness }

func (*piFakeHarness) Kind() harness.Kind { return "pi" }

// pi 直连后线程里残留的注入模型：本轮改用默认模型，线程模型同步回写，界面标签不再显示失效的模型。
func TestRunHarnessResetsStalePiModel(t *testing.T) {
	SetPiStaleModel(func(model string) bool { return strings.HasPrefix(model, "magpie-jp/") })
	defer SetPiStaleModel(nil)

	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	b.SetDataDir(t.TempDir())
	fake := &piFakeHarness{}
	b.RegisterHarness(fake)

	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "p", Path: t.TempDir()})
	stale := "magpie-jp/auto/deepseek-v4.1-flash"
	created, err := b.CreateThread(context.Background(), wire.ThreadCreateParams{ProjectID: "p1", ActionID: newUUID(), Model: &stale})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := b.SendMessage(context.Background(), wire.SendMessageParams{ThreadID: created.ThreadID, Text: "你好", ActionID: newUUID()}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	waitForTurnDone(t, b, created.ThreadID)

	if got := fake.getLastTurn().Model; got != "default" {
		t.Fatalf("turn.Model = %q，想要 default", got)
	}
	b.mu.Lock()
	model := b.threads[created.ThreadID].summary.Model
	b.mu.Unlock()
	if model == nil || *model != "default" {
		t.Fatalf("线程模型 = %v，想要 default", model)
	}
}
