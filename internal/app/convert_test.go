package app

import (
	"reflect"
	"strings"
	"testing"

	"mcode/internal/harness"
	"mcode/internal/kanban"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"mcode/internal/swarm"
	mcodeui "mcode/internal/ui"
)

func TestKanbanTaskToUISource(t *testing.T) {
	task := store.Task{Meta: kanban.EncodeTaskMeta(kanban.TaskMeta{Origin: "owner", ParentID: "parent-1", SourceMessageID: "message-1", AmendMessageIDs: []string{"message-2"}})}
	got := KanbanTaskToUI(task, kanban.Derived{})
	if got.Origin != "owner" || got.ParentID != "parent-1" || got.SourceMessageID != "message-1" || !reflect.DeepEqual(got.AmendMessageIDs, []string{"message-2"}) {
		t.Fatalf("任务来源错误: %+v", got)
	}
}

func TestThreadMessageToUITurnTiming(t *testing.T) {
	id, status := "turn-0", "completed"
	start, end := "2026-11-06T08:00:00.123Z", "2026-11-06T08:00:12.123Z"
	m := wire.ThreadMessage{ID: "m", Role: "assistant", TurnID: &id, TurnIndex: 0, TurnStatus: &status, TurnStartedAt: &start, TurnCompletedAt: &end}
	for _, got := range []mcodeui.Message{ThreadMessageToUI(m), ThreadUpdateFromWire(wire.ThreadEvent{Payload: wire.ThreadEventPayload{Messages: []wire.ThreadMessage{m}}}).Messages[0]} {
		if got.TurnID != id || got.TurnIndex != 0 || got.TurnStatus != status || got.TurnStartedAt != start || got.TurnCompletedAt != end {
			t.Fatalf("轮次元数据丢失: %+v", got)
		}
	}
}

func TestUsageHarnessToUICopiesContextWindowAndCost(t *testing.T) {
	got := UsageHarnessToUI(harness.Usage{
		InputTokens:   1200,
		OutputTokens:  456,
		CachedTokens:  300,
		DurationMs:    12_000,
		ContextTokens: 1656,
		ContextWindow: 1_000_000,
		CostUSD:       0.0421,
	})
	want := mcodeui.TokenUsage{
		Input:         1200,
		Output:        456,
		Cached:        300,
		DurationMs:    12_000,
		ContextTokens: 1656,
		ContextWindow: 1_000_000,
		CostUSD:       0.0421,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("UsageHarnessToUI = %+v, want %+v", got, want)
	}
	// 后端未上报窗口与费用时保持零值，界面沿用原有口径。
	if zero := UsageHarnessToUI(harness.Usage{InputTokens: 10}); zero.ContextWindow != 0 || zero.CostUSD != 0 {
		t.Errorf("未上报时应为零值: %+v", zero)
	}
}

// TestUsageHarnessToUICopiesRequestSpeeds 校验每次模型请求的速度样本被逐项转换到界面用量。
func TestUsageHarnessToUICopiesRequestSpeeds(t *testing.T) {
	got := UsageHarnessToUI(harness.Usage{Requests: []harness.RequestSpeed{
		{OutputTokens: 120, DurationMs: 2_400},
		{OutputTokens: 340, DurationMs: 6_100},
	}})
	want := []mcodeui.RequestSpeed{{OutputTokens: 120, DurationMs: 2_400}, {OutputTokens: 340, DurationMs: 6_100}}
	if !reflect.DeepEqual(got.Requests, want) {
		t.Fatalf("Requests = %+v, want %+v", got.Requests, want)
	}
	if nil != UsageHarnessToUI(harness.Usage{InputTokens: 10}).Requests {
		t.Error("后端未提供样本时应保持 nil")
	}
}

// TestThreadUsageHarnessToUIKeepsRequestSpeeds 校验轮次明细与合计都带请求速度样本。
func TestThreadUsageHarnessToUIKeepsRequestSpeeds(t *testing.T) {
	turn := harness.Usage{Requests: []harness.RequestSpeed{{OutputTokens: 10, DurationMs: 500}}}
	total := harness.Usage{Requests: []harness.RequestSpeed{{OutputTokens: 10, DurationMs: 500}, {OutputTokens: 20, DurationMs: 900}}}
	got := ThreadUsageHarnessToUI(map[int]harness.Usage{0: turn}, total)
	if len(got.Turns[0].Requests) != 1 || !reflect.DeepEqual(got.Total.Requests[1], mcodeui.RequestSpeed{OutputTokens: 20, DurationMs: 900}) {
		t.Fatalf("轮次与合计未带上请求样本: %+v", got)
	}
}

func TestThreadUsageHarnessToUIKeepsContextWindow(t *testing.T) {
	got := ThreadUsageHarnessToUI(
		map[int]harness.Usage{0: {InputTokens: 10, ContextWindow: 200_000}},
		harness.Usage{InputTokens: 10, ContextWindow: 200_000, CostUSD: 0.5},
	)
	if got.Turns[0].ContextWindow != 200_000 || got.Total.CostUSD != 0.5 {
		t.Fatalf("轮次与合计未带上新字段: %+v", got)
	}
}

func TestSwarmBeeToUIFiles(t *testing.T) {
	bee := swarm.Bee{ChangedFiles: []string{"parser.go"}, ConflictFiles: []string{"conflict.go"}}
	got := swarmBeeToUI(bee)
	if !reflect.DeepEqual(got.ChangedFiles, bee.ChangedFiles) || !reflect.DeepEqual(got.ConflictFiles, bee.ConflictFiles) {
		t.Fatalf("界面文件列表不匹配: %+v", got)
	}
	got.ChangedFiles[0], got.ConflictFiles[0] = "changed", "changed"
	if bee.ChangedFiles[0] != "parser.go" || bee.ConflictFiles[0] != "conflict.go" {
		t.Fatal("界面转换未独立复制文件列表")
	}
}

func TestSwarmBeeToUI_NewFields(t *testing.T) {
	bee := swarm.Bee{
		ThreadID:          "bee-1",
		Tier:              "deep",
		ReadOnly:          true,
		DirectModel:       true,
		ShadowRepo:        true,
		SharedNoGit:       true,
		SharedNoGitReason: "git missing",
	}
	got := swarmBeeToUI(bee)
	if got.Tier != "deep" {
		t.Errorf("Tier = %q, want %q", got.Tier, "deep")
	}
	if !got.ReadOnly {
		t.Errorf("ReadOnly = %v, want true", got.ReadOnly)
	}
	if !got.DirectModel {
		t.Errorf("DirectModel = %v, want true", got.DirectModel)
	}
	if got.ReadOnlyViolation {
		t.Errorf("ReadOnlyViolation = %v, want false", got.ReadOnlyViolation)
	}
	if !got.ShadowRepo {
		t.Errorf("ShadowRepo = %v, want true", got.ShadowRepo)
	}
	if !got.SharedNoGit {
		t.Errorf("SharedNoGit = %v, want true", got.SharedNoGit)
	}
	if got.SharedNoGitReason != "git missing" {
		t.Errorf("SharedNoGitReason = %q, want %q", got.SharedNoGitReason, "git missing")
	}
}

func TestSwarmBeeToUI_ReviewFields(t *testing.T) {
	bee := swarm.Bee{
		ThreadID:              "bee-1",
		ReviewState:           swarm.ReviewNeedsHuman,
		ReviewReason:          "改动与主工作区冲突",
		ReviewThreadID:        "review-thread-1",
		ReviewLayer:           "l2",
		ReviewConcerns:        []string{"foo.go:10 缺少错误处理", "bar.go:20 测试不足"},
		ReviewTokens:          3200,
		ReviewTokensEstimated: true,
	}
	got := swarmBeeToUI(bee)
	if got.ReviewState != "needs_human" {
		t.Errorf("ReviewState = %q, want %q", got.ReviewState, "needs_human")
	}
	if got.ReviewReason != "改动与主工作区冲突" {
		t.Errorf("ReviewReason = %q, want %q", got.ReviewReason, "改动与主工作区冲突")
	}
	if got.ReviewThreadID != "review-thread-1" {
		t.Errorf("ReviewThreadID = %q, want %q", got.ReviewThreadID, "review-thread-1")
	}
	if got.ReviewLayer != "l2" {
		t.Errorf("ReviewLayer = %q, want %q", got.ReviewLayer, "l2")
	}
	if len(got.ReviewConcerns) != 2 || got.ReviewConcerns[0] != "foo.go:10 缺少错误处理" || got.ReviewConcerns[1] != "bar.go:20 测试不足" {
		t.Errorf("ReviewConcerns = %v, want 两条疑点", got.ReviewConcerns)
	}
	if got.ReviewTokens != 3200 {
		t.Errorf("ReviewTokens = %d, want 3200", got.ReviewTokens)
	}
	if !got.ReviewTokensEstimated {
		t.Errorf("ReviewTokensEstimated = %v, want true", got.ReviewTokensEstimated)
	}

	// 修改原 bee 的疑点不应影响转换结果
	bee.ReviewConcerns[0] = "changed"
	if got.ReviewConcerns[0] != "foo.go:10 缺少错误处理" {
		t.Errorf("ReviewConcerns 未深拷贝，got %q", got.ReviewConcerns[0])
	}
}

func TestSwarmBeeToUI_Credibility(t *testing.T) {
	// 1. nil stays nil
	beeNil := swarm.Bee{ThreadID: "bee-nil"}
	gotNil := swarmBeeToUI(beeNil)
	if gotNil.Credibility != nil {
		t.Fatalf("expected Credibility to be nil, got %+v", gotNil.Credibility)
	}

	// 2. maps Credibility incl. Summary and copies Unverified
	bee := swarm.Bee{
		ThreadID: "bee-cred",
		Credibility: &swarm.Credibility{
			Cited:      3,
			Verified:   2,
			Unverified: []string{"parser.go:10"},
		},
	}
	got := swarmBeeToUI(bee)
	if got.Credibility == nil {
		t.Fatal("expected Credibility not to be nil")
	}
	if got.Credibility.Cited != 3 || got.Credibility.Verified != 2 {
		t.Errorf("Cited/Verified = %d/%d, want 3/2", got.Credibility.Cited, got.Credibility.Verified)
	}
	wantSummary := bee.Credibility.Summary()
	if got.Credibility.Summary != wantSummary {
		t.Errorf("Summary = %q, want %q", got.Credibility.Summary, wantSummary)
	}
	if !strings.Contains(got.Credibility.Summary, "读取记录不可用") {
		t.Errorf("Summary = %q, want containing %q", got.Credibility.Summary, "读取记录不可用")
	}
	if !reflect.DeepEqual(got.Credibility.Unverified, []string{"parser.go:10"}) {
		t.Errorf("Unverified = %+v, want [parser.go:10]", got.Credibility.Unverified)
	}
	// 验证切片为独立拷贝
	got.Credibility.Unverified[0] = "other.go:20"
	if bee.Credibility.Unverified[0] != "parser.go:10" {
		t.Error("Unverified 切片未被独立深拷贝")
	}
}

func TestThreadMessageToUI_Media(t *testing.T) {
	width := 1920
	height := 1080

	msgWithMedia := wire.ThreadMessage{
		ID:        "msg-1",
		Role:      "assistant",
		Text:      "Here are some images",
		TurnIndex: 1,
		Media: []wire.MediaRef{
			{
				ID:        "med-1",
				Source:    "upload",
				Name:      "with-dimensions.png",
				MimeType:  "image/png",
				ByteCount: 4096,
				Width:     &width,
				Height:    &height,
			},
			{
				ID:        "med-2",
				Source:    "attachment",
				Name:      "no-dimensions.jpg",
				MimeType:  "image/jpeg",
				ByteCount: 2048,
				Width:     nil,
				Height:    nil,
			},
		},
	}

	uiMsg := ThreadMessageToUI(msgWithMedia)

	if len(uiMsg.Media) != 2 {
		t.Fatalf("期望转换后媒体数量为 2，实际为 %d", len(uiMsg.Media))
	}

	// 检查第一个媒体（有尺寸）
	m1 := uiMsg.Media[0]
	if m1.ID != "med-1" || m1.Name != "with-dimensions.png" || m1.MimeType != "image/png" {
		t.Errorf("媒体 1 元数据不匹配: %+v", m1)
	}
	if m1.Width != 1920 || m1.Height != 1080 {
		t.Errorf("媒体 1 尺寸期望 (1920, 1080)，实际为 (%d, %d)", m1.Width, m1.Height)
	}

	// 检查第二个媒体（无尺寸，nil 应转为 0）
	m2 := uiMsg.Media[1]
	if m2.ID != "med-2" || m2.Name != "no-dimensions.jpg" || m2.MimeType != "image/jpeg" {
		t.Errorf("媒体 2 元数据不匹配: %+v", m2)
	}
	if m2.Width != 0 || m2.Height != 0 {
		t.Errorf("媒体 2 尺寸期望 (0, 0)，实际为 (%d, %d)", m2.Width, m2.Height)
	}

	// 检查无媒体时转换结果为 nil
	msgWithoutMedia := wire.ThreadMessage{
		ID:        "msg-2",
		Role:      "user",
		Text:      "No media here",
		TurnIndex: 2,
		Media:     nil,
	}
	uiMsgNoMedia := ThreadMessageToUI(msgWithoutMedia)
	if uiMsgNoMedia.Media != nil {
		t.Errorf("期望无媒体时 Media 为 nil，实际为 %+v", uiMsgNoMedia.Media)
	}
}

func TestThreadMessageToUIPreservesTurnID(t *testing.T) {
	turnID := "turn-51"
	got := ThreadMessageToUI(wire.ThreadMessage{ID: "m1", Role: "tool", TurnID: &turnID})
	if got.TurnID != turnID {
		t.Fatalf("TurnID = %q, want %q", got.TurnID, turnID)
	}
	if got := ThreadMessageToUI(wire.ThreadMessage{}); got.TurnID != "" {
		t.Fatalf("nil TurnID 转换为 %q", got.TurnID)
	}
}

func TestRemoteRequestToUI_KindAndQuestions(t *testing.T) {
	reason := "需要确认"
	detail := "甲"
	r := wire.RemoteRequest{
		ThreadID:  "t1",
		RunID:     "r1",
		RequestID: "req1",
		Kind:      "question",
		Reason:    &reason,
		Options:   []wire.RemoteRequestOption{},
		Questions: []wire.RemoteQuestion{{
			ID:             "q1",
			Header:         "库",
			Prompt:         "用哪个库？",
			Choices:        []wire.RemoteQuestionChoice{{Label: "A", Detail: &detail}, {Label: "B"}},
			AllowsMultiple: true,
			AllowsOther:    true,
			IsSecret:       true,
		}},
	}

	a := RemoteRequestToUI(r)
	if a.Kind != "question" || a.Reason != "需要确认" {
		t.Errorf("unexpected kind/reason: %q %q", a.Kind, a.Reason)
	}
	want := []mcodeui.ApprovalQuestion{{
		ID:             "q1",
		Header:         "库",
		Prompt:         "用哪个库？",
		Choices:        []mcodeui.ApprovalQuestionChoice{{Label: "A", Detail: "甲"}, {Label: "B"}},
		AllowsMultiple: true,
		AllowsOther:    true,
		IsSecret:       true,
	}}
	if !reflect.DeepEqual(a.Questions, want) {
		t.Errorf("questions = %+v, want %+v", a.Questions, want)
	}

	plan := RemoteRequestToUI(wire.RemoteRequest{Kind: "plan", Questions: []wire.RemoteQuestion{}})
	if plan.Kind != "plan" || plan.Questions != nil || plan.Reason != "" {
		t.Errorf("unexpected plan approval: %+v", plan)
	}
}

func TestThreadSummaryToUI_FastMode(t *testing.T) {
	tier := "fast"
	got := ThreadSummaryToUI(wire.ThreadSummary{ID: "t1", FastMode: true, SpeedTier: &tier})
	if !got.FastMode || got.SpeedTier != "fast" {
		t.Fatalf("unexpected fast mode mapping: %+v", got)
	}

	empty := ThreadSummaryToUI(wire.ThreadSummary{ID: "t2"})
	if empty.FastMode || empty.SpeedTier != "" {
		t.Fatalf("expected fast mode off by default, got %+v", empty)
	}
}

func TestThreadSummaryToUI_ComposerPrefs(t *testing.T) {
	effort, hp, hm, he := "high", "codex", "o3-mini", "low"
	got := ThreadSummaryToUI(wire.ThreadSummary{
		ID:                "t1",
		InteractionMode:   "plan",
		Effort:            &effort,
		SwarmBeesProvider: &hp,
		SwarmBeesModel:    &hm,
		SwarmBeesEffort:   &he,
	})
	if got.InteractionMode != "plan" || got.Effort != "high" || got.BeesProvider != "codex" || got.BeesModel != "o3-mini" || got.BeesEffort != "low" {
		t.Fatalf("unexpected composer prefs mapping: %+v", got)
	}

	empty := ThreadSummaryToUI(wire.ThreadSummary{ID: "t2"})
	if empty.InteractionMode != "build" || empty.Effort != "" || empty.BeesProvider != "" || empty.BeesModel != "" || empty.BeesEffort != "" {
		t.Fatalf("expected build default and empty prefs, got %+v", empty)
	}
}

func TestThreadSummaryToUI_ParentAndHelper(t *testing.T) {
	parentID := "p-123"
	got := ThreadSummaryToUI(wire.ThreadSummary{
		ID:             "t-child",
		ParentThreadID: &parentID,
		IsHelper:       true,
	})
	if got.ParentThreadID != "p-123" {
		t.Errorf("ParentThreadID = %q, want %q", got.ParentThreadID, "p-123")
	}
	if !got.IsHelper {
		t.Errorf("IsHelper = %v, want true", got.IsHelper)
	}

	empty := ThreadSummaryToUI(wire.ThreadSummary{
		ID: "t-root",
	})
	if empty.ParentThreadID != "" {
		t.Errorf("ParentThreadID = %q, want empty string", empty.ParentThreadID)
	}
	if empty.IsHelper {
		t.Errorf("IsHelper = %v, want false", empty.IsHelper)
	}
}

// TestThreadSummaryToUI_SwarmBusy 覆盖 R7-012：子线程还在跑时主会话的 SwarmBusy 必须传到 UI，
// 否则桌面侧会在主会话回合结束后提前停止转圈、解除输入区限制。
func TestThreadSummaryToUI_SwarmBusy(t *testing.T) {
	got := ThreadSummaryToUI(wire.ThreadSummary{ID: "t1", IsRunning: false, SwarmBusy: true})
	if !got.SwarmBusy || got.IsRunning {
		t.Fatalf("unexpected swarm busy mapping: %+v", got)
	}

	empty := ThreadSummaryToUI(wire.ThreadSummary{ID: "t2"})
	if empty.SwarmBusy {
		t.Fatalf("expected swarm busy off by default, got %+v", empty)
	}
}

// TestThreadUpdateFromWire_Resync 覆盖 U-014：帧体超过上限时后端改发的 resync 帧要标记为
// 重新同步，UI 才知道要按会话重拉消息，而不是当成空更新丢掉。
func TestThreadUpdateFromWire_Resync(t *testing.T) {
	got := ThreadUpdateFromWire(wire.ThreadEvent{
		ThreadID: "t1",
		Kind:     "resync",
		Payload:  wire.ThreadEventPayload{Thread: wire.ThreadSummary{ID: "t1"}},
	})
	if !got.Resync {
		t.Fatalf("resync 帧未标记为重新同步: %+v", got)
	}
	if len(got.Messages) != 0 {
		t.Fatalf("resync 帧不应带消息: %+v", got.Messages)
	}

	normal := ThreadUpdateFromWire(wire.ThreadEvent{
		ThreadID: "t1",
		Kind:     "append",
		Payload:  wire.ThreadEventPayload{Thread: wire.ThreadSummary{ID: "t1"}},
	})
	if normal.Resync {
		t.Fatalf("普通增量帧不应标记为重新同步: %+v", normal)
	}
}

// TestRemoteRequestToUI_DeliveryError 覆盖 U-032：上次答复未送达的原因要传到审批卡片。
func TestRemoteRequestToUI_DeliveryError(t *testing.T) {
	deliveryErr := "pipe closed"
	got := RemoteRequestToUI(wire.RemoteRequest{
		ThreadID:      "t1",
		RequestID:     "req1",
		Kind:          "command",
		DeliveryError: &deliveryErr,
	})
	if got.DeliveryError != "pipe closed" {
		t.Fatalf("DeliveryError = %q, want %q", got.DeliveryError, "pipe closed")
	}

	empty := RemoteRequestToUI(wire.RemoteRequest{ThreadID: "t1", RequestID: "req2"})
	if empty.DeliveryError != "" {
		t.Fatalf("无送达错误时应为空，got %q", empty.DeliveryError)
	}
}
