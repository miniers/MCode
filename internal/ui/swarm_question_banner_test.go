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

func TestUIReview4_SwarmQuestionCountdownNextChange(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

	// 1. 纯函数：≥1 分钟按分钟边界，<1 分钟按秒边界，已到期/已转交/未设置不请求下一帧
	cases := []struct {
		name   string
		q      *SwarmBeeQuestion
		wantD  time.Duration
		wantOK bool
	}{
		{name: "nil", q: nil, wantD: 0, wantOK: false},
		{name: "zero", q: &SwarmBeeQuestion{}, wantD: 0, wantOK: false},
		{name: "forwarded", q: &SwarmBeeQuestion{AutoForwardAt: now.Add(time.Minute), Forwarded: true}, wantD: 0, wantOK: false},
		{name: "expired_zero", q: &SwarmBeeQuestion{AutoForwardAt: now}, wantD: 0, wantOK: false},
		{name: "expired_past", q: &SwarmBeeQuestion{AutoForwardAt: now.Add(-5 * time.Second)}, wantD: 0, wantOK: false},
		{name: "ge_1m_partial", q: &SwarmBeeQuestion{AutoForwardAt: now.Add(150 * time.Second)}, wantD: 30 * time.Second, wantOK: true},
		{name: "ge_1m_exact_2m", q: &SwarmBeeQuestion{AutoForwardAt: now.Add(2 * time.Minute)}, wantD: time.Minute, wantOK: true},
		{name: "ge_1m_exact_1m", q: &SwarmBeeQuestion{AutoForwardAt: now.Add(time.Minute)}, wantD: time.Second, wantOK: true},
		{name: "lt_1m_partial", q: &SwarmBeeQuestion{AutoForwardAt: now.Add(42*time.Second + 300*time.Millisecond)}, wantD: 300 * time.Millisecond, wantOK: true},
		{name: "lt_1m_exact", q: &SwarmBeeQuestion{AutoForwardAt: now.Add(42 * time.Second)}, wantD: time.Second, wantOK: true},
	}
	for _, tc := range cases {
		gotD, gotOK := swarmQuestionAutoForwardNextChange(tc.q, now)
		if gotD != tc.wantD || gotOK != tc.wantOK {
			t.Fatalf("%s: got (%v, %v), want (%v, %v)", tc.name, gotD, gotOK, tc.wantD, tc.wantOK)
		}
	}

	// 2. headless 渲染：使用 c.Now() 与按需 c.After，且数字等宽、已到期后不再重绘
	fake := NewFakeClient()
	s := newChatState(fake)
	s.selectedThreadID = "lead"
	hp := getSwarmPanelState(s)

	var baseTime time.Time
	setCountdown := func(d time.Duration) {
		st := swarmQuestionBannerState(false)
		st.Bees[1].PendingQuestion.AutoForwardAt = baseTime.Add(d)
		swarmPanelMu.Lock()
		hp.loadedFor = "lead"
		hp.setStateLocked(st)
		swarmPanelMu.Unlock()
	}

	builds := 0
	tt := ui.NewTester(func(c *ui.Context) {
		if baseTime.IsZero() {
			baseTime = c.Now()
			setCountdown(150 * time.Second)
		}
		builds++
		s.init(c)
		renderSwarmQuestionBanner(c, s)
	}, 900, 400)

	tt.Frame()
	if !tt.HasText("3 分钟后自动交给主线程") {
		t.Fatalf("应显示 3 分钟倒计时，got %v", tt.Texts())
	}
	// ≥1 分钟期间（距下一次分钟边界 30s 前）推进 29s 不应重绘
	before := builds
	tt.Advance(29 * time.Second)
	if builds != before {
		t.Fatalf("剩余 ≥1 分钟时不应每秒重绘：builds %d -> %d", before, builds)
	}
	// 到达 30s 边界时触发一次重绘并更新为 2 分钟
	tt.Advance(time.Second)
	if !tt.HasText("2 分钟后自动交给主线程") {
		t.Fatalf("到达分钟边界后应更新为 2 分钟，got %v", tt.Texts())
	}

	// 切到 <1 分钟（42s）：等宽数字、按秒边界刷新
	baseTime = tt.Now()
	setCountdown(42 * time.Second)
	tt.Frame()
	r42, ok := tt.Find("42 秒后自动交给主线程")
	if !ok {
		t.Fatalf("应显示 42 秒倒计时，got %v", tt.Texts())
	}
	before = builds
	tt.Advance(900 * time.Millisecond)
	if builds != before {
		t.Fatalf("未到整秒边界不应提前重绘：builds %d -> %d", before, builds)
	}
	tt.Advance(100 * time.Millisecond)
	r41, ok := tt.Find("41 秒后自动交给主线程")
	if !ok {
		t.Fatalf("到达秒边界后应更新为 41 秒，got %v", tt.Texts())
	}
	if r42.W != r41.W {
		t.Errorf("倒计时数字应等宽显示，避免右侧按钮晃动：42=%v 41=%v", r42.W, r41.W)
	}

	// 推进到已到期：显示即将自动转交，之后不再请求下一帧
	tt.Advance(45 * time.Second)
	if !tt.HasText("即将自动交给主线程") {
		t.Fatalf("到期后应显示即将自动转交，got %v", tt.Texts())
	}
	before = builds
	tt.Advance(10 * time.Second)
	if builds != before {
		t.Fatalf("已到期后不应再请求下一帧：builds %d -> %d", before, builds)
	}
}
