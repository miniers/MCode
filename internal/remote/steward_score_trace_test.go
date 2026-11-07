package remote

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// syncStewardScoreTrace 把计分写库换成同步执行，断言不靠 sleep；测试结束后恢复默认实现。
func syncStewardScoreTrace(t *testing.T) {
	t.Helper()
	prev := stewardScoreTraceRun
	stewardScoreTraceRun = func(fn func()) { fn() }
	t.Cleanup(func() { stewardScoreTraceRun = prev })
}

// newStewardScoreBackend 建一个挂了真实 store 的后端，返回后端与库。
func newStewardScoreBackend(t *testing.T) (*MemoryBackend, *store.Store) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "steward_scores.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	if err := backend.AttachStore(context.Background(), s); err != nil {
		t.Fatalf("AttachStore 失败: %v", err)
	}
	backend.SetDataDir(filepath.Dir(dbPath))
	return backend, s
}

// seedStewardScore 模拟 app 层登记了一条总管建议，供答复/撤卡挂点结算。
func seedStewardScore(t *testing.T, s *store.Store, requestID, threadID, kind, suggestedOption string) {
	t.Helper()
	if err := s.UpsertStewardApprovalScore(context.Background(), store.StewardApprovalScore{
		RequestID:       requestID,
		ThreadID:        threadID,
		Kind:            kind,
		SuggestedOption: suggestedOption,
		SuggestedRole:   ApprovalRoleFamily("", suggestedOption),
	}); err != nil {
		t.Fatalf("登记计分行失败: %v", err)
	}
}

// readStewardScore 读取指定请求的计分行；没有这行直接失败。
func readStewardScore(t *testing.T, s *store.Store, requestID string) store.StewardApprovalScore {
	t.Helper()
	scores, err := s.ListStewardApprovalScores(context.Background(), 0)
	if err != nil {
		t.Fatalf("读取计分行失败: %v", err)
	}
	for _, score := range scores {
		if score.RequestID == requestID {
			return score
		}
	}
	t.Fatalf("没有找到计分行 %s", requestID)
	return store.StewardApprovalScore{}
}

// 建议 allow、用户选 allow_always：同属 allow 家族，记 agree，工具名与所选项落库。
func TestStewardScoreAnswerRecordsAgree(t *testing.T) {
	syncStewardScoreTrace(t)
	backend, s := newStewardScoreBackend(t)

	threadID := "00000000-0000-0000-0000-000000002001"
	h := newWithdrawHarness(false, harness.ApprovalRequest{
		ToolID:   "call_steward_score_agree",
		ToolName: "bash",
		Summary:  "run echo score",
	})
	ev, req := startAskTraceCard(t, backend, threadID, h)
	seedStewardScore(t, s, req.RequestID, threadID, req.Kind, "allow")

	optAllowAlways := "allow_always"
	if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllowAlways,
	}); err != nil {
		t.Fatalf("RespondRequest failed: %v", err)
	}
	waitNoRequests(t, ev.requests)

	score := readStewardScore(t, s, req.RequestID)
	if score.Outcome != store.StewardScoreAgree {
		t.Errorf("建议 allow、用户选 allow_always 应记 agree，实际 %q", score.Outcome)
	}
	if score.ToolName != "bash" {
		t.Errorf("tool_name = %q，期望 bash", score.ToolName)
	}
	if score.ChosenOption != "allow_always" || score.ChosenRole != "allow" {
		t.Errorf("chosen = %q/%q，期望 allow_always/allow", score.ChosenOption, score.ChosenRole)
	}
	if score.AnswerSource != AnswerSourceDirect {
		t.Errorf("桌面缺省来源应为 direct，实际 %q", score.AnswerSource)
	}
	if score.AnsweredAt == nil {
		t.Error("已答复的行应带 answered_at")
	}
}

// 建议 allow、用户选 deny：方向相反，记 disagree；ctx 注入的来源覆盖缺省 direct。
func TestStewardScoreAnswerRecordsDisagreeAndSource(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   string
	}{
		{name: "direct", source: "", want: AnswerSourceDirect},
		{name: "confirm", source: AnswerSourceConfirm, want: AnswerSourceConfirm},
		{name: "remote", source: AnswerSourceRemote, want: AnswerSourceRemote},
		{name: "auto", source: AnswerSourceAuto, want: AnswerSourceAuto},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			syncStewardScoreTrace(t)
			backend, s := newStewardScoreBackend(t)

			threadID := "00000000-0000-0000-0000-000000002101"
			h := newWithdrawHarness(false, harness.ApprovalRequest{
				ToolID:   "call_steward_score_disagree",
				ToolName: "bash",
				Summary:  "run echo score",
			})
			ev, req := startAskTraceCard(t, backend, threadID, h)
			seedStewardScore(t, s, req.RequestID, threadID, req.Kind, "allow")

			ctx := context.Background()
			if tc.source != "" {
				ctx = WithAnswerSource(ctx, tc.source)
			}
			optDeny := "deny"
			if _, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
				ThreadID:  threadID,
				RunID:     req.RunID,
				RequestID: req.RequestID,
				OptionID:  &optDeny,
			}); err != nil {
				t.Fatalf("RespondRequest failed: %v", err)
			}
			waitNoRequests(t, ev.requests)

			score := readStewardScore(t, s, req.RequestID)
			if score.Outcome != store.StewardScoreDisagree {
				t.Errorf("建议 allow、用户选 deny 应记 disagree，实际 %q", score.Outcome)
			}
			if score.ChosenRole != "deny" {
				t.Errorf("chosen_role = %q，期望 deny", score.ChosenRole)
			}
			if score.AnswerSource != tc.want {
				t.Errorf("answer_source = %q，期望 %q", score.AnswerSource, tc.want)
			}
		})
	}
}

// 撤卡把仍待定的建议记为 unanswered 并带原因；之后再答复不会覆盖这个结局。
func TestStewardScoreWithdrawMarksUnanswered(t *testing.T) {
	syncStewardScoreTrace(t)
	backend, s := newStewardScoreBackend(t)

	threadID := "00000000-0000-0000-0000-000000002201"
	h := newWithdrawHarness(false, harness.ApprovalRequest{
		ToolID:   "call_steward_score_withdraw",
		ToolName: "bash",
		Summary:  "run sleep 10",
	})
	_, req := startAskTraceCard(t, backend, threadID, h)
	seedStewardScore(t, s, req.RequestID, threadID, req.Kind, "allow")

	archived := true
	if _, err := backend.UpdateThread(context.Background(), wire.ThreadUpdateParams{
		ThreadID: threadID,
		Archived: &archived,
	}); err != nil {
		t.Fatalf("UpdateThread failed: %v", err)
	}
	waitInteract(t, h.resultCh)

	score := readStewardScore(t, s, req.RequestID)
	if score.Outcome != store.StewardScoreUnanswered {
		t.Fatalf("撤卡后应记 unanswered，实际 %q", score.Outcome)
	}
	if score.UnansweredReason != string(withdrawArchive) {
		t.Errorf("unanswered_reason = %q，期望 %q", score.UnansweredReason, string(withdrawArchive))
	}
	if score.AnsweredAt != nil {
		t.Error("未答复的行不应带 answered_at")
	}

	// 撤卡后再答复会失败，且不能把已结算的结局翻回 agree/disagree。
	optAllow := "allow"
	if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllow,
	}); err == nil {
		t.Fatal("撤卡后的答复应当失败")
	}
	score = readStewardScore(t, s, req.RequestID)
	if score.Outcome != store.StewardScoreUnanswered || score.ChosenOption != "" {
		t.Errorf("撤卡后再答复不应改写结局，实际 %q/%q", score.Outcome, score.ChosenOption)
	}
}

// 启动对账把上次进程遗留的待答卡片与建议一起结算为 unanswered/host_restarted。
func TestStewardScoreBootReconcileMarksHostRestarted(t *testing.T) {
	syncStewardScoreTrace(t)
	backend, s := newStewardScoreBackend(t)

	threadID := "00000000-0000-0000-0000-000000002301"
	requestID := "00000000-0000-0000-0000-000000002302"
	projectID := "p-" + threadID
	backend.AddProject(wire.ProjectSummary{ID: projectID, Name: "计分测试", Path: t.TempDir()})
	backend.AddThread(wire.ThreadSummary{ID: threadID, ProjectID: projectID, Title: "计分线程"}, nil)

	seedStewardScore(t, s, requestID, threadID, string(harness.RequestKindCommand), "allow")
	if err := s.InsertPendingAsk(context.Background(), store.PendingAsk{
		RequestID: requestID,
		ThreadID:  threadID,
		Kind:      string(harness.RequestKindCommand),
		Title:     "遗留卡片",
		State:     store.PendingAskPending,
	}); err != nil {
		t.Fatalf("插入遗留卡片失败: %v", err)
	}

	backend.reconcileBoot(context.Background(), s)

	score := readStewardScore(t, s, requestID)
	if score.Outcome != store.StewardScoreUnanswered {
		t.Fatalf("启动对账后应记 unanswered，实际 %q", score.Outcome)
	}
	if score.UnansweredReason != bootAskResolution {
		t.Errorf("unanswered_reason = %q，期望 %q", score.UnansweredReason, bootAskResolution)
	}
}

// 未挂 store 的后端上计分挂点必须静默：不 panic、不写库、不影响答复。
func TestStewardScoreTraceWithoutStoreIsSilent(t *testing.T) {
	syncStewardScoreTrace(t)
	backend := newWithdrawBackend(t)

	score := store.StewardApprovalScore{RequestID: "00000000-0000-0000-0000-000000002401", ChosenOption: "deny"}
	traceSuggestionAnswered(backend, score)
	traceSuggestionAnswered(nil, score)
	traceSuggestionUnanswered(backend, score.RequestID, "interrupt")
	TraceSuggestionRegistered(backend, score)
}
