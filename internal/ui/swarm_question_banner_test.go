package mcodeui

import (
	"fmt"
	"strings"
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

func TestSwarmQuestionBannerNarrowWrap(t *testing.T) {
	longPrompt := strings.Repeat("请确认数据库迁移策略与历史数据清理方案，避免上线回滚风险", 2)
	makeState := func(baseTime time.Time) SwarmState {
		return SwarmState{
			LeadThreadID: "lead",
			Available:    true,
			Enabled:      true,
			Bees: []SwarmBee{
				{
					ThreadID: "h1",
					Name:     "快手",
					Status:   "running",
					PendingQuestion: &SwarmBeeQuestion{
						RequestID: "req-wrap",
						Questions: []ApprovalQuestion{
							{ID: "q1", Prompt: longPrompt},
							{ID: "q2", Prompt: "是否保留旧索引？"},
						},
						AutoForwardAt: baseTime.Add(150 * time.Second),
					},
				},
			},
		}
	}

	rectsOverlap := func(a, b ui.Rect) bool {
		return a.X < b.X+b.W-0.5 && b.X < a.X+a.W-0.5 && a.Y < b.Y+b.H-0.5 && b.Y < a.Y+a.H-0.5
	}

	for _, winW := range []int{900, 460, 360} {
		t.Run(fmt.Sprintf("width_%d", winW), func(t *testing.T) {
			fake := NewFakeClient()
			s := newChatState(fake)
			t.Cleanup(s.close)
			s.selectedThreadID = "lead"
			hp := getSwarmPanelState(s)

			var (
				baseTime    time.Time
				summaryText string
			)
			tt := ui.NewTester(func(c *ui.Context) {
				if baseTime.IsZero() {
					baseTime = c.Now()
					st := makeState(baseTime)
					summaryText = swarmQuestionSingleLine(swarmQuestionBannerFullSummary(st.Bees[0]), swarmQuestionBannerMaxRunes)
					swarmPanelMu.Lock()
					hp.loadedFor = "lead"
					hp.setStateLocked(st)
					swarmPanelMu.Unlock()
				}
				s.init(c)
				renderSwarmQuestionBanner(c, s)
			}, winW, 400)

			tt.Frame()

			avatarRect, ok := tt.Find("快手 头像")
			if !ok {
				t.Fatalf("winW=%d: 缺少头像元素", winW)
			}
			if avatarRect.W != 22 || avatarRect.H != 22 {
				t.Fatalf("winW=%d: 头像尺寸应保持 22x22，got %+v", winW, avatarRect)
			}

			summaryRect, ok := tt.Find(summaryText)
			if !ok || summaryRect.W <= 0 || summaryRect.H <= 0 {
				t.Fatalf("winW=%d: 摘要布局异常 ok=%v rect=%+v texts=%v", winW, ok, summaryRect, tt.Texts())
			}
			hintRect, ok := tt.Find("3 分钟后自动交给主线程")
			if !ok || hintRect.W <= 0 || hintRect.H <= 0 {
				t.Fatalf("winW=%d: 倒计时布局异常 ok=%v rect=%+v", winW, ok, hintRect)
			}
			forwardInfo, ok := tt.AccessByRole(ui.RoleButton, "交给主线程回答")
			if !ok || forwardInfo.Bounds.W <= 0 || forwardInfo.Bounds.H <= 0 {
				t.Fatalf("winW=%d: 缺少「交给主线程回答」按钮", winW)
			}
			openInfo, ok := tt.AccessByRole(ui.RoleButton, "去回答")
			if !ok || openInfo.Bounds.W <= 0 || openInfo.Bounds.H <= 0 {
				t.Fatalf("winW=%d: 缺少「去回答」按钮", winW)
			}
			forwardRect := forwardInfo.Bounds
			openRect := openInfo.Bounds

			items := []struct {
				name string
				r    ui.Rect
			}{
				{"avatar", avatarRect},
				{"summary", summaryRect},
				{"hint", hintRect},
				{"forward", forwardRect},
				{"open", openRect},
			}
			for _, it := range items {
				if it.r.X < -0.5 || it.r.X+it.r.W > float32(winW)+0.5 {
					t.Fatalf("winW=%d: %s 越出容器边界: %+v", winW, it.name, it.r)
				}
			}
			for i := 0; i < len(items); i++ {
				for j := i + 1; j < len(items); j++ {
					if rectsOverlap(items[i].r, items[j].r) {
						t.Fatalf("winW=%d: %s (%+v) 与 %s (%+v) 重叠", winW, items[i].name, items[i].r, items[j].name, items[j].r)
					}
				}
			}

			if winW >= 900 {
				if summaryRect.X+summaryRect.W > hintRect.X+0.5 {
					t.Fatalf("winW=%d: 宽窗下摘要与操作栏应在同一行，summary=%+v hint=%+v", winW, summaryRect, hintRect)
				}
				if max(summaryRect.Y, openRect.Y) >= min(summaryRect.Y+summaryRect.H, openRect.Y+openRect.H) {
					t.Fatalf("winW=%d: 宽窗下摘要与按钮应同一行对齐，summary=%+v open=%+v", winW, summaryRect, openRect)
				}
			} else {
				if summaryRect.W < 120 {
					t.Fatalf("winW=%d: 窄窗下摘要宽度过窄: %+v", winW, summaryRect)
				}
				for _, action := range []struct {
					name string
					r    ui.Rect
				}{
					{"hint", hintRect},
					{"forward", forwardRect},
					{"open", openRect},
				} {
					if action.r.Y < summaryRect.Y+summaryRect.H-0.5 {
						t.Fatalf("winW=%d: 窄窗下 %s 应折到摘要下方，summary=%+v %s=%+v", winW, action.name, summaryRect, action.name, action.r)
					}
				}
			}
		})
	}
}
