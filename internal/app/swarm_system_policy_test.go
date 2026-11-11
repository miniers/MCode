package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"mcode/internal/git"
	"mcode/internal/harness"
	"mcode/internal/harness/policy"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/swarm"
)

type swarmPolicyHarness struct{ turns chan harness.Turn }

func (*swarmPolicyHarness) Kind() harness.Kind { return "swarm-policy" }
func (h *swarmPolicyHarness) Run(_ context.Context, turn harness.Turn, emit func(harness.Event)) error {
	h.turns <- turn
	emit(harness.Event{Type: harness.EventText, Text: "ok"})
	return nil
}

type swarmSystemPolicyHarness struct{ *swarmPolicyHarness }

func (*swarmSystemPolicyHarness) CarriesSystemPolicy(string) bool { return true }

func TestSwarmSystemPolicyRouting(t *testing.T) {
	for _, nextPrompt := range []string{"user next", swarm.NoticePrefix + "bee done"} {
		t.Run(nextPrompt, func(t *testing.T) {
			ctx := context.Background()
			dir := swarmRepo(t)
			hub := remote.NewStreamHub(remote.StreamHubOptions{})
			defer hub.Close()
			b := remote.NewMemoryBackend(hub)
			defer b.Close()
			h := &swarmPolicyHarness{turns: make(chan harness.Turn, 8)}
			b.SetHarness(h, dir, harness.ModeFullAccess)
			b.AddProject(wire.ProjectSummary{ID: "p", Path: dir})
			b.AddThread(wire.ThreadSummary{ID: "lead", ProjectID: "p", Provider: "swarm-policy"}, nil)
			mgr := swarm.NewManager(swarm.Options{Git: git.NewRunner("", 4), Driver: b, WorktreeDir: t.TempDir(), PollInterval: testSwarmPollInterval})
			mgr.SetSettingsFunc(func() swarm.Settings {
				return swarm.Settings{Enabled: true, DefaultTier: "edit", Tiers: []swarm.Tier{{ID: "edit", Description: "常规编辑"}}}
			})
			mgr.SetEnabled("lead", true)
			wireSwarmWithPreface(ctx, mgr, b, false, func(string) string { return "kanban preface" })
			send := func(text string) harness.Turn {
				t.Helper()
				sendSwarmMessage(t, ctx, b, "lead", text)
				select {
				case turn := <-h.turns:
					return turn
				case <-time.After(5 * time.Second):
					t.Fatal("等待 Turn 超时")
					return harness.Turn{}
				}
			}
			leadPolicy := swarm.LeadPolicy(false, "off", false)
			// 先记住旧路径的规则，再切换到系统提示，确保旧去重记录会被清除。
			send("first user request")
			b.RegisterHarness(&swarmSystemPolicyHarness{h})
			for _, prompt := range []string{"second user request", swarm.NoticePrefix + "progress"} {
				turn := send(prompt)
				if strings.Contains(turn.Prompt, leadPolicy) || strings.Contains(turn.Prompt, "Bee tiers:") || strings.Contains(turn.Prompt, swarm.LeadNoticeReminder) {
					t.Fatalf("用户消息含静态规则: %s", turn.Prompt)
				}
				if !strings.Contains(turn.Prompt, "rounds used so far") {
					t.Fatalf("用户消息缺少动态段: %s", turn.Prompt)
				}
				notice := swarm.IsNotice(prompt)
				if !notice && !strings.Contains(turn.Prompt, "kanban preface") {
					t.Fatalf("用户轮缺少看板 preface: %s", turn.Prompt)
				}
				if notice && strings.Contains(turn.Prompt, "kanban preface") {
					t.Fatalf("通知轮不应带看板 preface: %s", turn.Prompt)
				}
				if !strings.Contains(turn.SystemPolicy, policy.Marker) || !strings.Contains(turn.SystemPolicy, leadPolicy) || !strings.Contains(turn.SystemPolicy, "Bee tiers:") {
					t.Fatalf("系统提示缺少静态规则: %s", turn.SystemPolicy)
				}
				if strings.Contains(turn.SystemPolicy, "rounds used so far") || strings.Contains(turn.SystemPolicy, "kanban preface") {
					t.Fatalf("系统提示混入动态规则: %s", turn.SystemPolicy)
				}
			}
			turn := send("/compact")
			if turn.Prompt != "/compact" || !strings.Contains(turn.SystemPolicy, leadPolicy) {
				t.Fatalf("compact 未保留系统规则或原始命令: %+v", turn)
			}
			b.RegisterHarness(h)
			turn = send(nextPrompt)
			if turn.SystemPolicy != "" || !strings.Contains(turn.Prompt, leadPolicy) || !strings.Contains(turn.Prompt, "Bee tiers:") {
				t.Fatalf("切回不承载的 harness 后未发送完整规则: %+v", turn)
			}
		})
	}
}

func TestSwarmSystemPolicyDisabledAndBee(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		global, enabled, helper bool
	}{
		{name: "global disabled", enabled: true},
		{name: "thread disabled", global: true},
		{name: "bee", global: true, enabled: true, helper: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := swarmRepo(t)
			hub := remote.NewStreamHub(remote.StreamHubOptions{})
			defer hub.Close()
			b := remote.NewMemoryBackend(hub)
			defer b.Close()
			h := &swarmPolicyHarness{turns: make(chan harness.Turn, 1)}
			b.SetHarness(&swarmSystemPolicyHarness{h}, dir, harness.ModeFullAccess)
			b.AddProject(wire.ProjectSummary{ID: "p", Path: dir})
			b.AddThread(wire.ThreadSummary{ID: "thread", ProjectID: "p", Provider: "swarm-policy", IsHelper: tc.helper}, nil)
			mgr := swarm.NewManager(swarm.Options{Git: git.NewRunner("", 4), Driver: b, WorktreeDir: t.TempDir(), PollInterval: testSwarmPollInterval})
			mgr.SetSettingsFunc(func() swarm.Settings { return swarm.Settings{Enabled: tc.global} })
			mgr.SetEnabled("thread", tc.enabled)
			wireSwarm(ctx, mgr, b, false)
			sendSwarmMessage(t, ctx, b, "thread", "hello")
			select {
			case turn := <-h.turns:
				if turn.SystemPolicy != "" {
					t.Fatalf("不应注入系统规则: %s", turn.SystemPolicy)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("等待 Turn 超时")
			}
		})
	}
}
