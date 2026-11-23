package mcodeui

import (
	"context"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestUIReview23ConcurrentInspectorBodyReadWrite(t *testing.T) {
	s := sidePaneTestState(t)
	defer s.close()
	stubSidePanePanelRender(t, sidePaneFileID)
	stubSidePanePanelRender(t, sidePaneGitID)
	inspectorSelect(s, sidePaneFileID, false)

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("review23-root")).Fill().Children(func() {
			renderInspectorBody(c, s, 320, false)
		})
	}, 400, 600)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			switch i % 3 {
			case 0:
				inspectorSelect(s, sidePaneFileID, false)
			case 1:
				inspectorSelect(s, sidePaneGitID, false)
			case 2:
				inspectorClose(s)
			}
		}
	})

	for range 40 {
		tt.Frame()
	}
	close(stop)
	wg.Wait()
}

type uiReview23DiffDeadlineClient struct {
	Client
	mu        sync.Mutex
	deadlines []bool
}

func (c *uiReview23DiffDeadlineClient) Diff(ctx context.Context, threadID, turnID string) (ThreadDiff, error) {
	_, ok := ctx.Deadline()
	c.mu.Lock()
	c.deadlines = append(c.deadlines, ok)
	c.mu.Unlock()
	return c.Client.Diff(ctx, threadID, turnID)
}

func TestUIReview23LoadToolDiffContextHasDeadline(t *testing.T) {
	fake := NewFakeClient()
	fake.SetThreadDiff("t1", "", ThreadDiff{
		Turns: []DiffTurn{{TurnID: "turn-1", Index: 1}},
	})
	fake.SetThreadDiff("t1", "turn-1", ThreadDiff{Diff: toolDiffTestUnified})

	client := &uiReview23DiffDeadlineClient{Client: fake}
	s := newChatState(client)
	defer s.close()

	s.loadToolDiff("t1", 1, "", 0)

	client.mu.Lock()
	deadlines := append([]bool(nil), client.deadlines...)
	client.mu.Unlock()

	if len(deadlines) != 2 {
		t.Fatalf("期望 Diff 调用 2 次，实际 %d 次", len(deadlines))
	}
	for i, hasDeadline := range deadlines {
		if !hasDeadline {
			t.Fatalf("第 %d 次 Diff 的 ctx 缺少 Deadline", i)
		}
	}
}
