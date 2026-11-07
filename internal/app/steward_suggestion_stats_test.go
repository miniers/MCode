package app

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote"
	"mcode/internal/store"
)

// scoreRow 构造一条用于聚合的计分记录；id 只用于满足存储主键。
func scoreRow(id, kind, outcome string) store.StewardApprovalScore {
	return store.StewardApprovalScore{RequestID: id, Kind: kind, Outcome: outcome}
}

// TestAggregateStewardScoresSampleThreshold 覆盖样本不足的边界：
// 9 条可答复记录仍算样本不足，第 10 条起才有百分比（阈值 10 在 ui 侧判定）。
func TestAggregateStewardScoresSampleThreshold(t *testing.T) {
	nine := make([]store.StewardApprovalScore, 0, 10)
	for i := 0; i < 9; i++ {
		nine = append(nine, scoreRow(fmt.Sprintf("nine-%02d", i), "command", store.StewardScoreAgree))
	}
	got := aggregateStewardScores(nine, nine)
	if len(got) != 1 {
		t.Fatalf("9 条应为 1 个类别，得到 %d", len(got))
	}
	if got[0].All.Answered != 9 || got[0].Recent.Answered != 9 {
		t.Fatalf("9 条样本量应为 9，得到 %+v", got[0])
	}

	ten := append(nine, scoreRow("nine-09", "command", store.StewardScoreAgree))
	got = aggregateStewardScores(ten, ten)
	if got[0].All.Answered != 10 || got[0].Recent.Answered != 10 {
		t.Fatalf("10 条样本量应为 10，得到 %+v", got[0])
	}
}

// TestAggregateStewardScoresUnansweredNotInDenominator 验证未答复与待答都不进分母：
// 未答复单独计数，pending 完全忽略。
func TestAggregateStewardScoresUnansweredNotInDenominator(t *testing.T) {
	rows := []store.StewardApprovalScore{
		scoreRow("a1", "tool", store.StewardScoreAgree),
		scoreRow("a2", "tool", store.StewardScoreAgree),
		scoreRow("a3", "tool", store.StewardScoreDisagree),
		scoreRow("a4", "tool", store.StewardScoreUnanswered),
		scoreRow("a5", "tool", store.StewardScorePending),
	}
	got := aggregateStewardScores(rows, rows)
	if len(got) != 1 {
		t.Fatalf("应为 1 个类别，得到 %d", len(got))
	}
	want := struct{ answered, agree, unanswered int }{3, 2, 1}
	for _, window := range []struct {
		name string
		got  int
		want int
	}{
		{"answered", got[0].All.Answered, want.answered},
		{"agree", got[0].All.Agree, want.agree},
		{"unanswered", got[0].All.Unanswered, want.unanswered},
	} {
		if window.got != window.want {
			t.Fatalf("%s 应为 %d，得到 %d", window.name, window.want, window.got)
		}
	}
}

// TestAggregateStewardScoresKindOrder 验证类别顺序固定，且没有可计分记录的类别不出现。
func TestAggregateStewardScoresKindOrder(t *testing.T) {
	rows := []store.StewardApprovalScore{
		scoreRow("p1", string(harness.RequestKindPermissions), store.StewardScoreAgree),
		scoreRow("c1", string(harness.RequestKindCommand), store.StewardScoreAgree),
		scoreRow("t1", string(harness.RequestKindTool), store.StewardScoreDisagree),
		scoreRow("f1", string(harness.RequestKindFileChange), store.StewardScoreAgree),
		// 只有 pending 的类别没有可展示的一致率，不应出现。
		scoreRow("q1", string(harness.RequestKindQuestion), store.StewardScorePending),
	}
	got := aggregateStewardScores(rows, nil)
	want := []string{
		string(harness.RequestKindCommand),
		string(harness.RequestKindFileChange),
		string(harness.RequestKindTool),
		string(harness.RequestKindPermissions),
	}
	if len(got) != len(want) {
		t.Fatalf("类别数应为 %d，得到 %d（%+v）", len(want), len(got), got)
	}
	for i, kind := range want {
		if got[i].Kind != kind {
			t.Fatalf("第 %d 个类别应为 %s，得到 %s", i, kind, got[i].Kind)
		}
	}
}

// TestAggregateStewardScoresWindows 验证「全部」与「最近」各算各的，
// 并且只出现在最近窗口的类别也会合并进结果。
func TestAggregateStewardScoresWindows(t *testing.T) {
	all := []store.StewardApprovalScore{
		scoreRow("c1", "command", store.StewardScoreAgree),
		scoreRow("c2", "command", store.StewardScoreAgree),
		scoreRow("c3", "command", store.StewardScoreAgree),
		scoreRow("t1", "tool", store.StewardScoreAgree),
	}
	recent := []store.StewardApprovalScore{
		scoreRow("c3", "command", store.StewardScoreDisagree),
		scoreRow("t1", "tool", store.StewardScoreAgree),
	}
	got := aggregateStewardScores(all, recent)
	if len(got) != 2 {
		t.Fatalf("应为 2 个类别，得到 %d", len(got))
	}
	command := got[0]
	if command.All.Answered != 3 || command.All.Agree != 3 || command.Recent.Answered != 1 || command.Recent.Agree != 0 {
		t.Fatalf("命令两个窗口的计数不对：%+v", command)
	}
	if got[1].Kind != "tool" || got[1].All.Answered != 1 || got[1].Recent.Answered != 1 {
		t.Fatalf("工具调用两个窗口的计数不对：%+v", got[1])
	}
}

// TestAggregateStewardScoresEmpty 验证没有记录时返回空结果，而不是 nil 或占位类别。
func TestAggregateStewardScoresEmpty(t *testing.T) {
	got := aggregateStewardScores(nil, nil)
	if len(got) != 0 {
		t.Fatalf("空数据应为 0 个类别，得到 %+v", got)
	}
}

// TestStewardSuggestionStatsEmptyStore 验证没有存储或存储为空时返回空切片与 nil 错误。
func TestStewardSuggestionStatsEmptyStore(t *testing.T) {
	ctx := context.Background()
	got, err := NewClient(nil).StewardSuggestionStats(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("无后端应返回空结果，得到 %+v, %v", got, err)
	}

	client, _ := newStatsTestClient(t)
	got, err = client.StewardSuggestionStats(ctx)
	if err != nil {
		t.Fatalf("空存储不应报错：%v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("空存储应返回非 nil 空切片，得到 %+v", got)
	}
}

// TestStewardSuggestionStatsReadsTwoWindows 用真实 store 验证客户端确实读了两个口径：
// 全部 51 条，最近只有 50 条。
func TestStewardSuggestionStatsReadsTwoWindows(t *testing.T) {
	ctx := context.Background()
	client, st := newStatsTestClient(t)
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 51; i++ {
		id := fmt.Sprintf("req-%02d", i)
		created := base.Add(time.Duration(i) * time.Minute)
		if err := st.UpsertStewardApprovalScore(ctx, store.StewardApprovalScore{
			RequestID:     id,
			Kind:          string(harness.RequestKindCommand),
			SuggestedRole: "allow",
			CreatedAt:     created,
		}); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
		if err := st.RecordStewardAnswer(ctx, id, string(harness.RequestKindCommand), "Bash", "allow", "allow", "direct"); err != nil {
			t.Fatalf("record answer %s: %v", id, err)
		}
	}

	got, err := client.StewardSuggestionStats(ctx)
	if err != nil {
		t.Fatalf("读取建议计分失败：%v", err)
	}
	if len(got) != 1 || got[0].Kind != string(harness.RequestKindCommand) {
		t.Fatalf("应为 1 个命令类别，得到 %+v", got)
	}
	if got[0].All.Answered != 51 || got[0].All.Agree != 51 {
		t.Fatalf("全部口径应为 51 条一致，得到 %+v", got[0].All)
	}
	if got[0].Recent.Answered != stewardScoreRecentLimit || got[0].Recent.Agree != stewardScoreRecentLimit {
		t.Fatalf("最近口径应为 %d 条一致，得到 %+v", stewardScoreRecentLimit, got[0].Recent)
	}
}

// newStatsTestClient 造一个挂了真实 store 的客户端，store 为空。
func newStatsTestClient(t *testing.T) (*Client, *store.Store) {
	t.Helper()
	ctx := context.Background()
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	t.Cleanup(hub.Close)
	b := remote.NewMemoryBackend(hub)
	t.Cleanup(b.Close)
	st, err := store.Open(filepath.Join(t.TempDir(), "scores.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := b.AttachStore(ctx, st); err != nil {
		t.Fatalf("attach store: %v", err)
	}
	return NewClient(b), st
}
