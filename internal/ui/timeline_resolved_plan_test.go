package mcodeui

import (
	"testing"
)

func TestPlanFeedbackTargetAndComposerApprovalDisabled(t *testing.T) {
	plan := Approval{ThreadID: "t1", RequestID: "plan-1", Kind: ApprovalKindPlan}
	cmd := Approval{ThreadID: "t1", RequestID: "cmd-1", Kind: ApprovalKindCommand}
	otherPlan := Approval{ThreadID: "t2", RequestID: "plan-2", Kind: ApprovalKindPlan}

	if got, ok := planFeedbackTarget([]Approval{plan}, "t1"); !ok || got.RequestID != "plan-1" {
		t.Fatalf("only plan target = %+v, %v", got, ok)
	}
	if composerApprovalDisabled(false, []Approval{plan}, "t1") {
		t.Fatal("only plan should leave composer enabled")
	}
	if _, ok := planFeedbackTarget([]Approval{plan, cmd}, "t1"); ok || !composerApprovalDisabled(false, []Approval{plan, cmd}, "t1") {
		t.Fatal("plan plus command should disable composer")
	}
	if got, ok := planFeedbackTarget([]Approval{otherPlan}, "t1"); ok || got.RequestID != "" {
		t.Fatalf("other thread plan should not be target: %+v, %v", got, ok)
	}
	if composerApprovalDisabled(false, []Approval{otherPlan}, "t1") {
		t.Fatal("other thread plan should not disable composer")
	}
	if !composerApprovalDisabled(true, nil, "t1") {
		t.Fatal("pending request without approvals should disable composer")
	}
}

func TestPlanDecisionParsingAndDropping(t *testing.T) {
	valid := Message{Kind: planDecisionMessageKind, Text: `{"requestID":"r1","plan":"实施","approved":false,"feedback":"请补充"}`, TurnIndex: 3, TurnID: "turn-3"}
	got, ok := parsePlanDecisionMessage(valid)
	if !ok || got.ID != "r1" || got.Plan != "实施" || got.Approved || got.Feedback != "请补充" || got.TurnIndex != 3 {
		t.Fatalf("parsed plan = %+v, %v", got, ok)
	}
	for _, text := range []string{`{"plan":"x","approved":true}`, `{"requestID":"r1","plan":"x"}`} {
		if _, ok := parsePlanDecisionMessage(Message{Kind: planDecisionMessageKind, Text: text}); ok {
			t.Fatalf("invalid plan decision parsed: %s", text)
		}
	}
	plans := []resolvedPlan{{ID: "r1"}, {ID: "r2"}}
	kept := dropResolvedPlansRecordedInMessages(plans, []Message{valid})
	if len(kept) != 1 || kept[0].ID != "r2" {
		t.Fatalf("dropped plans = %+v", kept)
	}
}

func TestBuildTimelineItemsPlanDecisions(t *testing.T) {
	valid := Message{ID: "m1", Kind: planDecisionMessageKind, Text: `{"requestID":"r1","plan":"实施","approved":true}`}
	invalid := Message{ID: "m2", Kind: planDecisionMessageKind, Text: `{"requestID":"r2","plan":"缺字段"}`}
	items := buildTimelineItems([]Message{valid, invalid})
	if len(items) != 1 || items[0].kind != timelineItemResolvedPlan || items[0].resolvedPlan.ID != "r1" {
		t.Fatalf("timeline items = %+v", items)
	}
}

func TestAttachPendingPlanItemsFiltersThread(t *testing.T) {
	approvals := []Approval{
		{ThreadID: "t1", RequestID: "r1", Kind: ApprovalKindPlan, Detail: "当前"},
		{ThreadID: "t2", RequestID: "r2", Kind: ApprovalKindPlan, Detail: "其它"},
		{ThreadID: "t1", RequestID: "r3", Kind: ApprovalKindCommand},
	}
	items := attachPendingPlanItems([]timelineItem{{id: "old"}}, approvals, "t1")
	if len(items) != 2 || items[1].kind != timelineItemPendingPlan || items[1].id != pendingPlanCardID("r1") {
		t.Fatalf("pending plan items = %+v", items)
	}
}

func TestResolvedPlanExport(t *testing.T) {
	if got := resolvedPlanExport(resolvedPlan{Plan: "实施", Approved: true}); got != "## 计划 · 已批准\n\n实施" {
		t.Fatalf("approved export = %q", got)
	}
	if got := resolvedPlanExport(resolvedPlan{Plan: "实施", Feedback: "第一条\n第二条"}); got != "## 计划 · 已驳回\n\n实施\n\n> 第一条\n> 第二条" {
		t.Fatalf("declined export = %q", got)
	}
}
