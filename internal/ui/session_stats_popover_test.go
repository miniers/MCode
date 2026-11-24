package mcodeui

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestSessionSpeedSamplesUsesTurns 验证会话速度样本只来自各轮明细（Total 里不带样本），
// 拼接后能汇总出跨回合的平均值与范围。
func TestSessionSpeedSamplesUsesTurns(t *testing.T) {
	turns := map[int]TokenUsage{
		1: {Requests: []RequestSpeed{{OutputTokens: 300, DurationMs: 10_000}}},
		2: {Requests: []RequestSpeed{{OutputTokens: 300, DurationMs: 5_000}}},
	}
	st, ok := requestSpeedStatsFor(sessionSpeedSamples(turns))
	if !ok {
		t.Fatal("跨回合样本应能汇总")
	}
	if st.n != 2 || st.min != 30 || st.max != 60 || st.avg != 40 {
		t.Fatalf("汇总 = %+v, want n=2 min=30 max=60 avg=40", st)
	}
	if len(sessionSpeedSamples(nil)) != 0 {
		t.Fatal("空会话不应有样本")
	}
}

func TestSessionDurationMixedTurnSources(t *testing.T) {
	msgs := []Message{
		{TurnID: "a", TurnIndex: 0, TurnStartedAt: "2026-11-06T08:00:00Z", TurnCompletedAt: "2026-11-06T08:00:10Z"},
		{TurnID: "a", TurnIndex: 0, TurnStartedAt: "2026-11-06T08:00:00Z", TurnCompletedAt: "2026-11-06T08:00:10Z"},
		{TurnID: "b", TurnIndex: 1},
		{Role: "user", TurnIndex: 2, CreatedAt: "2026-11-06T08:00:00Z"},
		{Role: "assistant", TurnIndex: 2, CreatedAt: "2026-11-06T08:00:30Z"},
		{TurnID: "running", TurnIndex: 3, TurnStatus: "running", TurnStartedAt: "2026-11-06T08:00:00Z"},
	}
	usage := map[int]TokenUsage{0: {DurationMs: 99000}, 1: {DurationMs: 20000}, 3: {DurationMs: 5000}}
	if got := sessionDurationForTurns(msgs, usage); got != time.Minute {
		t.Fatalf("总耗时 = %v, want 1m", got)
	}
}

// newSessionStatsLiveFixture 构造「已完成历史轮 + 当前运行轮」的会话快照：
// 轮 0 用权威起止 10s（usage 99s 是干扰项）、轮 1 用 usage 20s、轮 2 用消息回退 30s，
// 轮 3 正在运行：起点在 now-5s，带重复消息与 usage 5s（都不应双计）。
// 时间戳按真实精度对齐到整秒，避免帧格边界漂移；返回创建时刻。
func newSessionStatsLiveFixture(c *ui.Context) (*chatState, time.Time) {
	now := c.Now().Truncate(time.Second)
	hist := now.Add(-time.Hour)
	stamp := func(t time.Time) string { return t.Format(time.RFC3339Nano) }
	start := now.Add(-5 * time.Second)
	s := &chatState{
		selectedThreadID: "t1",
		threads:          []Thread{{ID: "t1", IsRunning: true}},
		messages: []Message{
			{TurnID: "a", TurnIndex: 0, TurnStartedAt: stamp(hist), TurnCompletedAt: stamp(hist.Add(10 * time.Second))},
			{TurnID: "b", TurnIndex: 1},
			{Role: "user", TurnIndex: 2, CreatedAt: stamp(hist)},
			{Role: "assistant", TurnIndex: 2, CreatedAt: stamp(hist.Add(30 * time.Second))},
			{ID: "u3", Role: "user", TurnIndex: 3, CreatedAt: stamp(start)},
			{ID: "u3-dup", Role: "user", TurnIndex: 3, CreatedAt: stamp(start)},
			{ID: "a3", Role: "assistant", TurnID: "run", TurnIndex: 3, TurnStatus: "running", TurnStartedAt: stamp(start)},
		},
		threadUsage: ThreadUsage{
			Requests: 3,
			Total:    TokenUsage{Output: 600},
			Turns: map[int]TokenUsage{
				0: {DurationMs: 99_000},
				1: {DurationMs: 20_000},
				3: {DurationMs: 5_000},
			},
		},
	}
	return s, now
}

// TestSessionStatsLiveTotalGrowsWithoutMessages 验证无新消息、无 rev 变化时，
// 仅靠 fake clock 推进，浮层总用时每秒增长（历史累计 1m + 当前轮实时）；
// ReduceMotion 开关两种状态都读秒，平均响应与输出速度不随实时值变化。全程不用真实 sleep。
func TestSessionStatsLiveTotalGrowsWithoutMessages(t *testing.T) {
	for _, reduced := range []bool{false, true} {
		t.Run(map[bool]string{true: "reduced", false: "normal"}[reduced], func(t *testing.T) {
			setAppReduceMotion(t, reduced)
			var s *chatState
			tt := ui.NewTesterWith(func(c *ui.Context) {
				if s == nil {
					s, _ = newSessionStatsLiveFixture(c)
				}
				renderSessionStatsPopover(c, s, "t1", "stats", true)
			}, 330, 400, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduced}})
			assert := func(want string) {
				t.Helper()
				if !hasExactText(tt, want) {
					t.Fatalf("总用时应为 %q: %v", want, tt.Texts())
				}
				// 平均响应 = 60s/3 次请求 = 20s，输出速度 = 600/60s = 10 t/s，都不随读秒变化。
				if !hasExactText(tt, "20s") || !hasExactText(tt, "10 t/s") {
					t.Fatalf("平均响应/输出速度不应随实时值变化: %v", tt.Texts())
				}
			}
			assert("1m 05s")
			for secs := 6; secs <= 8; secs++ {
				tt.Advance(time.Second)
				assert(fmt.Sprintf("1m %02ds", secs))
			}
		})
	}
}

// TestSessionStatsLiveZeroStartTicks 验证起点 0 秒时也安排下一秒刷新并显示 0s，
// 不用实时值 > 0 作守卫；读秒帧由渲染计数观察（Tester 的 FramePending 不含 After 定时器）。
func TestSessionStatsLiveZeroStartTicks(t *testing.T) {
	var s *chatState
	renders := 0
	tt := ui.NewTester(func(c *ui.Context) {
		renders++
		if s == nil {
			s, _ = newSessionStatsLiveFixture(c)
			start := c.Now().Truncate(time.Second).Format(time.RFC3339Nano)
			s.messages = []Message{{ID: "u1", Role: "user", TurnIndex: 1, CreatedAt: start}}
			s.threadUsage = ThreadUsage{}
		}
		renderSessionStatsPopover(c, s, "t1", "stats", true)
	}, 330, 400)
	if !hasExactText(tt, "0s") {
		t.Fatalf("起点 0 秒应显示 0s: %v", tt.Texts())
	}
	before := renders
	tt.Advance(time.Second)
	if renders <= before {
		t.Fatal("起点 0 秒也应安排下一秒刷新")
	}
	if !hasExactText(tt, "1s") {
		t.Fatalf("下一秒应读到 1s: %v", tt.Texts())
	}
}

// TestSessionStatsLiveFrozenWhenMainTurnDone 验证主轮已完成即冻结：完成时间/终态、
// 仅 SwarmBusy 等子代理、乐观运行三种情况都不再把 now-start 追加进总用时，也不再续帧。
func TestSessionStatsLiveFrozenWhenMainTurnDone(t *testing.T) {
	cases := []struct {
		name  string
		setup func(s *chatState, now time.Time)
		want  string
	}{
		{"完成时间与终态", func(s *chatState, now time.Time) {
			done := now.Add(-2 * time.Second).Format(time.RFC3339Nano)
			s.messages[6].TurnStatus = "completed"
			s.messages[6].TurnCompletedAt = done
		}, "1m 03s"},
		{"主轮完成仅子代理等待", func(s *chatState, now time.Time) {
			s.threads[0].IsRunning = false
			s.threads[0].SwarmBusy = true
			done := now.Add(-2 * time.Second).Format(time.RFC3339Nano)
			s.messages[6].TurnStatus = "completed"
			s.messages[6].TurnCompletedAt = done
		}, "1m 03s"},
		{"乐观运行不读秒", func(s *chatState, now time.Time) {
			s.optimisticRunning = map[string]bool{"t1": true}
		}, "1m 00s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s *chatState
			renders := 0
			tt := ui.NewTester(func(c *ui.Context) {
				renders++
				if s == nil {
					var now time.Time
					s, now = newSessionStatsLiveFixture(c)
					tc.setup(s, now)
				}
				renderSessionStatsPopover(c, s, "t1", "stats", true)
			}, 330, 400)
			if !hasExactText(tt, tc.want) {
				t.Fatalf("总用时应冻结为 %q: %v", tc.want, tt.Texts())
			}
			before := renders
			tt.Advance(2 * time.Second)
			if renders != before {
				t.Fatalf("终态/非运行不应继续安排刷新: %d -> %d", before, renders)
			}
			if !hasExactText(tt, tc.want) {
				t.Fatalf("时钟推进后总用时仍应为 %q: %v", tc.want, tt.Texts())
			}
			if tt.FramePending() {
				t.Fatal("终态/非运行不应继续安排刷新")
			}
		})
	}
}

// TestSessionStatsLiveReplacesRunningTurnContribution 验证运行轮的旧 usage 不会双计：
// 无历史消息时总用时只有 now-start；当前轮完成后改用权威耗时，不含等待时间。
func TestSessionStatsLiveReplacesRunningTurnContribution(t *testing.T) {
	var s *chatState
	renders := 0
	tt := ui.NewTester(func(c *ui.Context) {
		renders++
		if s == nil {
			now := c.Now().Truncate(time.Second)
			start := now.Add(-30 * time.Second)
			s = &chatState{
				selectedThreadID: "t1",
				threads:          []Thread{{ID: "t1", IsRunning: true}},
				messages: []Message{
					{Role: "user", TurnIndex: 1, CreatedAt: start.Format(time.RFC3339Nano)},
				},
				threadUsage: ThreadUsage{Turns: map[int]TokenUsage{1: {DurationMs: 25_000}}},
			}
		}
		renderSessionStatsPopover(c, s, "t1", "stats", true)
	}, 330, 400)
	if !hasExactText(tt, "30s") {
		t.Fatalf("运行轮 usage 25s 不应叠加到实时 30s 上: %v", tt.Texts())
	}
	s.mu.Lock()
	now := tt.Now()
	s.threads[0].IsRunning = false
	s.messages[0].TurnStatus = "completed"
	s.messages[0].TurnStartedAt = now.Add(-30 * time.Second).Format(time.RFC3339Nano)
	s.messages[0].TurnCompletedAt = now.Add(-10 * time.Second).Format(time.RFC3339Nano)
	s.mu.Unlock()
	tt.Frame()
	if !hasExactText(tt, "20s") {
		t.Fatalf("完成后应按权威耗时 20s 冻结: %v", tt.Texts())
	}
	before := renders
	tt.Advance(3 * time.Second)
	if renders != before {
		t.Fatalf("完成后不应继续安排刷新: %d -> %d", before, renders)
	}
	if !hasExactText(tt, "20s") {
		t.Fatalf("完成后不应再增长: %v", tt.Texts())
	}
}

// TestSessionStatsLiveRequiresSelectedRunningThread 验证选中线程不一致、
// 线程不在运行或当前轮无可解析起点时不读秒，也不伪造 now 作起点。
func TestSessionStatsLiveRequiresSelectedRunningThread(t *testing.T) {
	cases := []struct {
		name  string
		setup func(s *chatState)
	}{
		{"非选中线程", func(s *chatState) { s.selectedThreadID = "other" }},
		{"线程不在运行", func(s *chatState) { s.threads[0].IsRunning = false }},
		{"无可解析起点", func(s *chatState) {
			s.messages = []Message{{Role: "user", TurnIndex: 3}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s *chatState
			var now time.Time
			renders := 0
			tt := ui.NewTester(func(c *ui.Context) {
				renders++
				if s == nil {
					s, now = newSessionStatsLiveFixture(c)
					tc.setup(s)
				}
				renderSessionStatsPopover(c, s, "t1", "stats", true)
			}, 330, 400)
			st := collectSessionStats(s, "t1", now)
			if st.liveRunning {
				t.Fatal("不应判定为实时运行")
			}
			if st.liveDuration != st.totalDuration {
				t.Fatalf("非实时总用时应等于既有基数: %v vs %v", st.liveDuration, st.totalDuration)
			}
			before := renders
			tt.Advance(2 * time.Second)
			if renders != before || tt.FramePending() {
				t.Fatalf("非实时不应安排刷新: %d -> %d", before, renders)
			}
		})
	}
}

// TestSessionStatsCostSumsReportedTurns 验证上报金额的累加方式随 provider 语义变化：
// pi/omp 报的是本轮增量，逐轮相加；ACP 家族与 claude 报的是整次会话累计，
// 只取最后一轮的值，相加会把同一个递增数字重复计费。一轮都没上报时才退回目录参考价估算。
func TestSessionStatsCostSumsReportedTurns(t *testing.T) {
	providers := []ProviderInfo{{ID: "omp", Models: []ModelInfo{{
		ID:   "m",
		Cost: &ModelCost{Input: 1000, Output: 1000},
	}}}, {ID: "maki", ACP: true}}
	usage := ThreadUsage{
		Total: TokenUsage{Input: 1_000_000, Output: 0},
		Turns: map[int]TokenUsage{
			0: {Input: 500_000, CostUSD: 0.25},
			1: {Input: 500_000, CostUSD: 0.75},
		},
	}
	got, reported, ok := sessionStatsCost(usage, providers, "omp", "m")
	if !ok || !reported || math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("逐轮增量语义应等于各轮之和 1.0，得到 %v (reported=%v ok=%v)", got, reported, ok)
	}

	cumulative := ThreadUsage{
		Total: TokenUsage{Input: 1_000_000, Output: 0},
		Turns: map[int]TokenUsage{
			0: {CostUSD: 0.7266606},
			1: {CostUSD: 0.7423316},
			2: {CostUSD: 7.8463124},
		},
	}
	got, reported, ok = sessionStatsCost(cumulative, providers, "maki", "m")
	if !ok || !reported || math.Abs(got-7.8463124) > 1e-9 {
		t.Fatalf("会话累计语义应取最后一轮 7.8463124，得到 %v (reported=%v ok=%v)", got, reported, ok)
	}
	if got, _, _ := sessionStatsCost(cumulative, providers, "claude", "m"); math.Abs(got-7.8463124) > 1e-9 {
		t.Fatalf("claude 的 total_cost_usd 同样是会话累计，得到 %v", got)
	}
	if got, reported, ok := sessionStatsCost(cumulative, providers, "cursor", "m"); !ok || !reported || math.Abs(got-9.3153046) > 1e-9 {
		t.Fatalf("provider 表里未标记 ACP 时应按逐轮增量求和，得到 %v (reported=%v ok=%v)", got, reported, ok)
	}

	usage.Turns = map[int]TokenUsage{0: {Input: 500_000}, 1: {Input: 500_000}}
	got, reported, ok = sessionStatsCost(usage, providers, "omp", "m")
	if !ok || reported || math.Abs(got-1000) > 1e-9 {
		t.Fatalf("无上报时按目录价估算应为 1000，得到 %v (reported=%v ok=%v)", got, reported, ok)
	}

	usage.Turns = nil
	usage.Total = TokenUsage{}
	if _, _, ok := sessionStatsCost(usage, nil, "", ""); ok {
		t.Fatal("既无上报金额也无用量时不应显示花费")
	}
}

// TestSessionStatsCostLatestIgnoresMapOrder 验证累计语义取轮次索引最大的那一轮，
// 与 map 迭代顺序无关；末尾轮次没有金额时回退到更早有金额的轮次。
func TestSessionStatsCostLatestIgnoresMapOrder(t *testing.T) {
	providers := []ProviderInfo{{ID: "maki", ACP: true}}
	usage := ThreadUsage{
		Turns: map[int]TokenUsage{
			9:  {CostUSD: 3.0},
			2:  {CostUSD: 1.0},
			11: {},
			5:  {CostUSD: 2.0},
		},
	}
	got, reported, ok := sessionStatsCost(usage, providers, "maki", "")
	if !ok || !reported || math.Abs(got-3.0) > 1e-9 {
		t.Fatalf("ACP provider 应取索引 9 的 3.0，得到 %v (reported=%v ok=%v)", got, reported, ok)
	}
	if got, _, _ := sessionStatsCost(usage, nil, "pi", ""); math.Abs(got-6.0) > 1e-9 {
		t.Fatalf("逐轮增量语义应求和 6.0，得到 %v", got)
	}
}

// TestSessionStatsCostRowRendered 验证累计花费在有上报金额时出现在统计卡片里。
func TestSessionStatsCostRowRendered(t *testing.T) {
	s := &chatState{
		selectedThreadID: "t1",
		threads:          []Thread{{ID: "t1", Provider: "omp"}},
		threadUsage: ThreadUsage{
			Turns: map[int]TokenUsage{0: {Input: 10, CostUSD: 1.5}},
			Total: TokenUsage{Input: 10},
		},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderSessionStatsPopover(c, s, "t1", "stats", true)
	}, 330, 400)
	if !hasExactText(tt, "累计花费") || !hasExactText(tt, "$1.50") {
		t.Fatalf("应显示累计花费行: %v", tt.Texts())
	}
}

// TestSessionStatsCostIncludesBees 验证 bee 会话的花费并入累计花费：bee 是独立会话，
// 主会话上报的累计值不含它们，统计必须自己加总。
func TestSessionStatsCostIncludesBees(t *testing.T) {
	s := &chatState{
		selectedThreadID: "t1",
		threads:          []Thread{{ID: "t1", Provider: "omp"}},
		threadUsage: ThreadUsage{
			Turns: map[int]TokenUsage{0: {Input: 10, CostUSD: 1.5}},
			Total: TokenUsage{Input: 10},
		},
	}
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.setStateLocked(SwarmState{Bees: []SwarmBee{
		{ThreadID: "b1", Tokens: 100, CostUSD: 2.25},
		{ThreadID: "b2", Tokens: 50, CostUSD: 0.75},
	}})
	swarmPanelMu.Unlock()

	st := collectSessionStats(s, "t1", time.Time{})
	if !st.hasCost || math.Abs(st.costUSD-4.5) > 1e-9 {
		t.Fatalf("累计花费应为 1.5 + 2.25 + 0.75 = 4.5，得到 %v (hasCost=%v)", st.costUSD, st.hasCost)
	}

	tt := ui.NewTester(func(c *ui.Context) {
		renderSessionStatsPopover(c, s, "t1", "stats", true)
	}, 330, 500)
	if !hasExactText(tt, "$4.50") || !hasExactText(tt, "含子代理累计花费") {
		t.Fatalf("卡片应显示含子代理的累计花费: %v", tt.Texts())
	}
	if !hasExactText(tt, "$3.00") {
		t.Fatalf("子代理分组应显示自身累计花费 2.25 + 0.75: %v", tt.Texts())
	}
}

// TestSessionStatsCostBeesOnly 验证主会话没有上报金额、只有 bee 上报时仍显示累计花费。
func TestSessionStatsCostBeesOnly(t *testing.T) {
	s := &chatState{
		selectedThreadID: "t1",
		threads:          []Thread{{ID: "t1", Provider: "pi"}},
	}
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.setStateLocked(SwarmState{Bees: []SwarmBee{{ThreadID: "b1", CostUSD: 3.0}}})
	swarmPanelMu.Unlock()

	st := collectSessionStats(s, "t1", time.Time{})
	if !st.hasCost || !st.costReported || math.Abs(st.costUSD-3.0) > 1e-9 {
		t.Fatalf("只有 bee 上报时应显示 3.0，得到 %v (hasCost=%v reported=%v)", st.costUSD, st.hasCost, st.costReported)
	}
}

// TestFormatLiveStatsDuration 验证实时总用时按整秒截断且整分整时省略零秒：
// 1m0s→1m、1h0m0s→1h、1h2m0s→1h2m，非零秒仍读秒。
func TestFormatLiveStatsDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{-time.Second, "0s"},
		{500 * time.Millisecond, "0s"},
		{10 * time.Second, "10s"},
		{59 * time.Second, "59s"},
		{time.Minute, "1m 00s"},
		{time.Minute + 500*time.Millisecond, "1m 00s"},
		{time.Minute + time.Second, "1m 01s"},
		{time.Hour, "1h 00m"},
		{time.Hour + 500*time.Millisecond, "1h 00m"},
		{time.Hour + 2*time.Minute, "1h 02m"},
		{time.Hour + 3*time.Second, "1h 00m"},
		{time.Hour + 2*time.Minute + 3*time.Second, "1h 02m"},
	}
	for _, tc := range cases {
		if got := formatLiveStatsDuration(tc.d); got != tc.want {
			t.Fatalf("formatLiveStatsDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
	if formatStatsDuration(time.Minute) != "1m 00s" {
		t.Fatal("平均响应格式不应改变")
	}
}
