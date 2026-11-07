package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// swarmQuestionBannerState 构造主会话 lead 的 Swarm 状态，其中一个运行中的 bee 正在提问。
func swarmQuestionBannerState(forwarded bool) SwarmState {
	return SwarmState{
		LeadThreadID: "lead",
		Available:    true,
		Enabled:      true,
		Bees: []SwarmBee{
			{ThreadID: "h0", Name: "疾风", Status: "running"},
			{
				ThreadID: "h1",
				Name:     "快手",
				Status:   "running",
				PendingQuestion: &SwarmBeeQuestion{
					RequestID: "req-1",
					Questions: []ApprovalQuestion{
						{ID: "q1", Prompt: "要不要删掉旧配置？"},
						{ID: "q2", Prompt: "保留哪一份迁移脚本？"},
					},
					Forwarded: forwarded,
				},
			},
		},
	}
}

func TestSwarmQuestionBannerShowsPendingQuestion(t *testing.T) {
	fake := NewFakeClient()
	st := swarmQuestionBannerState(false)
	fake.SetSwarmState("lead", st)

	s := newChatState(fake)
	s.selectedThreadID = "lead"
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.loadedFor = "lead"
	hp.setStateLocked(st)
	swarmPanelMu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		renderSwarmQuestionBanner(c, s)
	}, 900, 400)

	tt.Frame()
	if !tt.HasText("在提问") {
		t.Fatalf("banner missing question text, got %v", tt.Texts())
	}
	if !tt.HasText("快手 在提问：要不要删掉旧配置？") {
		t.Fatalf("banner missing bee name and prompt, got %v", tt.Texts())
	}
	if !tt.HasText("共 2 题") {
		t.Fatalf("banner missing question count, got %v", tt.Texts())
	}
	// 没有提问的 bee 不占行
	if tt.HasText("疾风") {
		t.Fatalf("banner rendered a bee without pending question, got %v", tt.Texts())
	}

	if err := tt.Click("交给主线程回答"); err != nil {
		t.Fatalf("click forward failed: %v", err)
	}
	waitFor(t, tt, func() bool { return len(fake.ForwardQuestionCalls()) == 1 })

	calls := fake.ForwardQuestionCalls()
	if calls[0].LeadThreadID != "lead" || calls[0].BeeThreadID != "h1" || calls[0].RequestID != "req-1" {
		t.Fatalf("forward call = %+v, want lead/h1/req-1", calls[0])
	}
}

func TestSwarmQuestionBannerForwarded(t *testing.T) {
	fake := NewFakeClient()
	st := swarmQuestionBannerState(true)
	fake.SetSwarmState("lead", st)

	s := newChatState(fake)
	s.selectedThreadID = "lead"
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.loadedFor = "lead"
	hp.setStateLocked(st)
	swarmPanelMu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		renderSwarmQuestionBanner(c, s)
	}, 900, 400)

	tt.Frame()
	if !tt.HasText("已交给主线程") {
		t.Fatalf("banner missing forwarded hint, got %v", tt.Texts())
	}
	// 已转交后仍可自己去回答
	if !tt.HasText("去回答") {
		t.Fatalf("banner missing open-float action, got %v", tt.Texts())
	}
}

func TestSwarmQuestionBannerSkipsEmbeddedBee(t *testing.T) {
	fake := NewFakeClient()
	st := swarmQuestionBannerState(false)
	s := newChatState(fake)
	s.selectedThreadID = "lead"
	s.embeddedBee = true
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.loadedFor = "lead"
	hp.setStateLocked(st)
	swarmPanelMu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		renderSwarmQuestionBanner(c, s)
	}, 900, 400)

	tt.Frame()
	if tt.HasText("在提问") {
		t.Fatalf("embedded bee state should not render the banner, got %v", tt.Texts())
	}
}

func TestSwarmQuestionAutoForwardHint(t *testing.T) {
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		q    *SwarmBeeQuestion
		want string
	}{
		{nil, ""},
		{&SwarmBeeQuestion{}, ""},
		{&SwarmBeeQuestion{AutoForwardAt: now.Add(time.Minute), Forwarded: true}, ""},
		{&SwarmBeeQuestion{AutoForwardAt: now.Add(150 * time.Second)}, "3 分钟后自动交给主线程"},
		{&SwarmBeeQuestion{AutoForwardAt: now.Add(42 * time.Second)}, "42 秒后自动交给主线程"},
		{&SwarmBeeQuestion{AutoForwardAt: now.Add(-time.Second)}, "即将自动交给主线程"},
	}
	for i, tc := range cases {
		if got := swarmQuestionAutoForwardHint(tc.q, now); got != tc.want {
			t.Fatalf("case %d: got %q, want %q", i, got, tc.want)
		}
	}
}
