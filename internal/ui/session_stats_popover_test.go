package mcodeui

import (
	"testing"
	"time"
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
