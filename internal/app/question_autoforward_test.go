package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/swarm"
)

// newAutoForwardFixture 造一个带待答提问的子线程，并按 seconds 写入自动转交配置。
// 循环通过 SetSwarmManager 启动后立即停止，测试里只手动驱动单次检查。
func newAutoForwardFixture(t *testing.T, seconds int) (*Client, *remote.MemoryBackend) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	dir := t.TempDir()

	dataDir := t.TempDir()
	cfg := config.Defaults()
	cfg.Orchestration.AutoForwardQuestionSeconds = seconds
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("config.Save failed: %v", err)
	}

	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	t.Cleanup(hub.Close)
	b := remote.NewMemoryBackend(hub)
	t.Cleanup(b.Close)
	asker := &questionMockHarness{answers: make(chan map[string][]string, 1)}
	b.SetHarness(asker, dir, harness.ModeFullAccess)
	b.AddProject(wire.ProjectSummary{ID: "p", Name: "P", Path: dir})
	b.AddThread(wire.ThreadSummary{ID: "lead", ProjectID: "p", Provider: "question-mock"}, nil)
	b.AddThread(wire.ThreadSummary{ID: "head", ProjectID: "p", Provider: "question-mock"}, nil)

	mgr := swarm.NewManager(swarm.Options{Persister: staticLeadPersister{records: []swarm.LeadRecord{{
		LeadThreadID: "lead",
		Enabled:      true,
		Bees:         []swarm.Bee{{ThreadID: "head", Name: "Ives", Status: swarm.StatusRunning}},
	}}}})
	if err := mgr.Restore(ctx); err != nil {
		t.Fatalf("restore failed: %v", err)
	}

	client := NewClient(b)
	client.SetProviderStore(dataDir, config.NewMemorySecretStore())
	client.SetSwarmManager(mgr)
	client.autoForwardMu.Lock()
	running := client.autoForwardCancel != nil
	client.autoForwardMu.Unlock()
	if !running {
		t.Fatal("SetSwarmManager must start the auto-forward loop")
	}
	// 停掉后台循环，避免定时器干扰手动的单次检查。
	client.stopQuestionAutoForward()

	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: "head", Text: "HEAD_ASK"}); err != nil {
		t.Fatalf("send to bee failed: %v", err)
	}
	waitSwarm(t, func() bool {
		res, err := b.ListRequests(ctx, "head")
		return err == nil && len(res.Requests) > 0
	})
	return client, b
}

// countLeadQuestionMessages 统计主线程收到的提问转交消息条数。
func countLeadQuestionMessages(t *testing.T, b *remote.MemoryBackend, leadThreadID string) int {
	t.Helper()
	page, err := b.ThreadHistory(context.Background(), wire.HistoryParams{ThreadID: leadThreadID})
	if err != nil {
		t.Fatalf("thread history failed: %v", err)
	}
	n := 0
	for _, m := range page.Messages {
		if strings.Contains(m.Text, swarm.NoticePrefix+"子代理提问：") {
			n++
		}
	}
	return n
}

// firstLeadQuestionMessage 返回主线程收到的第一条提问转交消息。
func firstLeadQuestionMessage(t *testing.T, b *remote.MemoryBackend, leadThreadID string) string {
	t.Helper()
	page, err := b.ThreadHistory(context.Background(), wire.HistoryParams{ThreadID: leadThreadID})
	if err != nil {
		t.Fatalf("thread history failed: %v", err)
	}
	for _, m := range page.Messages {
		if strings.Contains(m.Text, swarm.NoticePrefix+"子代理提问：") {
			return m.Text
		}
	}
	return ""
}

func TestSwarmQuestionAutoForwardAfterDelay(t *testing.T) {
	client, b := newAutoForwardFixture(t, 60)
	ctx := context.Background()

	// 未到等待时长：不转交。
	client.autoForwardQuestionsOnce(ctx, time.Now())
	if got := countLeadQuestionMessages(t, b, "lead"); got != 0 {
		t.Fatalf("未到时间不应转交，实际转交了 %d 次", got)
	}

	// 到时：转交一次，消息带编排前缀与自动转交说明。
	client.autoForwardQuestionsOnce(ctx, time.Now().Add(time.Minute))
	if got := countLeadQuestionMessages(t, b, "lead"); got != 1 {
		t.Fatalf("到时间应转交一次，实际 %d 次", got)
	}
	msg := firstLeadQuestionMessage(t, b, "lead")
	if !strings.HasPrefix(msg, swarm.NoticePrefix) {
		t.Fatalf("转交消息必须以编排前缀开头: %s", msg)
	}
	for _, want := range []string{"已自动转交", "用户 1 分钟内未回答", "head", "q1"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("转交消息缺少 %q: %s", want, msg)
		}
	}

	// 再检查一次：已转交的提问不会重复转交。
	client.autoForwardQuestionsOnce(ctx, time.Now().Add(10*time.Minute))
	if got := countLeadQuestionMessages(t, b, "lead"); got != 1 {
		t.Fatalf("已转交的提问不应重复转交，实际 %d 次", got)
	}
}

func TestSwarmQuestionAutoForwardDisabledWhenDelayZero(t *testing.T) {
	client, b := newAutoForwardFixture(t, 0)

	client.autoForwardQuestionsOnce(context.Background(), time.Now().Add(time.Hour))
	if got := countLeadQuestionMessages(t, b, "lead"); got != 0 {
		t.Fatalf("延迟为 0 时不应转交，实际转交了 %d 次", got)
	}
}
