package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"mcode/internal/swarm"
	"mcode/internal/testenv"
)

// TestSwarmStreamDispatcherQueueExitsWhenIdle 验证派发队列在空闲后清理映射条目并退出协程，
// 再次入队时仍能重新创建队列并正常派发。
func TestSwarmStreamDispatcherQueueExitsWhenIdle(t *testing.T) {
	testenv.TempHome(t)

	var (
		mu      sync.Mutex
		prompts []string
		rounds  int
	)
	d := newSwarmStreamDispatcher(context.Background(),
		func(ctx context.Context, req swarm.SingleEntryRequest) (swarm.Bee, []string, error) {
			mu.Lock()
			prompts = append(prompts, req.Entry.Prompt)
			mu.Unlock()
			return swarm.Bee{ThreadID: "bee-" + req.Entry.Name, Name: req.Entry.Name}, nil, nil
		},
		func(string) int { mu.Lock(); defer mu.Unlock(); return rounds },
		func(string) int { mu.Lock(); defer mu.Unlock(); rounds++; return rounds },
		func(string) (string, bool) { return t.TempDir(), true },
		func(threadID, text string) error { return nil },
		nil,
	)

	d.reset("lead")
	d.feed("lead", "project", "```swarm\n[{\"prompt\":\"alpha\",\"bee\":\"Ives\"}]\n```\n")
	d.finishTurn("lead")
	d.drain("lead", 2*time.Second)

	mu.Lock()
	gotFirst := len(prompts)
	mu.Unlock()
	if gotFirst != 1 {
		t.Fatalf("首次入队后期望派发 1 条，实际 %d", gotFirst)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		d.mu.Lock()
		_, exists := d.queues["lead"]
		d.mu.Unlock()
		if !exists {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("派发队列空闲 2 秒后仍未从 d.queues 移除")
		}
		time.Sleep(10 * time.Millisecond)
	}

	d.reset("lead")
	d.feed("lead", "project", "```swarm\n[{\"prompt\":\"beta\",\"bee\":\"Aspen\"}]\n```\n")
	d.finishTurn("lead")
	d.drain("lead", 2*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 2 || prompts[0] != "alpha" || prompts[1] != "beta" {
		t.Fatalf("队列重建后派发结果不正确: %q", prompts)
	}
}
