package remote

import (
	"context"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

type systemPolicyHarness struct{ mockHarness }

func (*systemPolicyHarness) CarriesSystemPolicy(threadID string) bool { return threadID != "denied" }

func TestCarriesSystemPolicyRouting(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	b.SetHarness(&systemPolicyHarness{mockHarness{kind: "carrier"}}, t.TempDir(), harness.ModeAgent)
	b.RegisterHarness(&mockHarness{kind: "other"})
	for _, tc := range []struct {
		threadID, provider string
		want               bool
	}{
		{"carrier", "carrier", true},
		{"default", "", true},
		{"memory", "memory", true},
		{"other", "other", false},
		{"unknown provider", "unregistered", false},
		{"denied", "carrier", false},
	} {
		b.AddThread(wire.ThreadSummary{ID: tc.threadID, Provider: tc.provider}, nil)
		if got := b.CarriesSystemPolicy(tc.threadID); got != tc.want {
			t.Errorf("thread=%q provider=%q: got=%v want=%v", tc.threadID, tc.provider, got, tc.want)
		}
	}
	if b.CarriesSystemPolicy("missing") {
		t.Fatal("不存在的会话不应承载系统规则")
	}
	b.UnregisterHarness("carrier")
	if b.CarriesSystemPolicy("carrier") {
		t.Fatal("注销后不应承载系统规则")
	}
}

func TestSystemPolicyCallbackOutsideLock(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	turns := make(chan harness.Turn, 1)
	b.SetHarness(&systemPolicyHarness{mockHarness{kind: "carrier", runFunc: func(_ context.Context, turn harness.Turn, _ func(harness.Event)) error {
		turns <- turn
		return nil
	}}}, t.TempDir(), harness.ModeAgent)
	b.AddThread(wire.ThreadSummary{ID: "thread", Provider: "carrier"}, nil)
	b.SetSystemPolicyFunc(func(threadID string) string {
		// 回调可以重入后端，不能持有 b.mu。
		if !b.CarriesSystemPolicy(threadID) {
			t.Error("回调路由错误")
		}
		return "host policy"
	})
	if _, err := b.SendMessage(context.Background(), wire.SendMessageParams{ThreadID: "thread", Text: "/compact"}); err != nil {
		t.Fatal(err)
	}
	select {
	case turn := <-turns:
		if turn.Prompt != "/compact" || turn.SystemPolicy != "host policy" {
			t.Fatalf("compact 系统规则错误: %+v", turn)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待 Turn 超时")
	}
}

func TestSystemPolicyConcurrentRoutingAndSetter(t *testing.T) {
	b := NewMemoryBackend(nil)
	defer b.Close()
	b.AddThread(wire.ThreadSummary{ID: "thread", Provider: "carrier"}, nil)
	carrier := &systemPolicyHarness{mockHarness{kind: "carrier"}}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				b.SetSystemPolicyFunc(func(string) string { return "policy" })
				b.RegisterHarness(carrier)
				b.CarriesSystemPolicy("thread")
			}
		}()
	}
	wg.Wait()
}
