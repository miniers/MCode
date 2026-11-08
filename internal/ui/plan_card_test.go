package mcodeui

import (
	"testing"
)

// TestRecordResolvedPlanDedupByPlan 覆盖同正文去重：同会话内正文相同的计划只保留一条，
// 且命中时保留原条目 ID 与折叠状态。
func TestRecordResolvedPlanDedupByPlan(t *testing.T) {
	s := newChatState(nil)
	s.selectedThreadID = "t1"
	s.collapsedPlans = make(map[string]bool)
	s.resolvedPlans = make(map[string][]resolvedPlan)

	first := Approval{ThreadID: "t1", RequestID: "r1", Kind: ApprovalKindPlan, Detail: "1. 改 UI\n2. 补测试"}
	s.recordResolvedPlan(first, true)
	if got := len(s.resolvedPlans["t1"]); got != 1 {
		t.Fatalf("首次记录后条目数 = %d, want 1", got)
	}
	if !s.collapsedPlans["r1"] {
		t.Errorf("新条目应默认折叠")
	}
	// 用户展开过：命中更新时不能改写折叠选择。
	s.collapsedPlans["r1"] = false

	// 不同 RequestID、正文仅空白差异：视为同一份计划。
	again := Approval{ThreadID: "t1", RequestID: "r2", Kind: ApprovalKindPlan, Detail: "  1. 改 UI\n2. 补测试\n"}
	s.recordResolvedPlan(again, false)
	list := s.resolvedPlans["t1"]
	if len(list) != 1 {
		t.Fatalf("同正文计划应去重，got %d 条：%+v", len(list), list)
	}
	if list[0].ID != "r1" {
		t.Errorf("命中更新时应保留原 ID，got %q", list[0].ID)
	}
	if list[0].Approved {
		t.Errorf("命中更新后应改为 Dismissed")
	}
	if collapsed, known := s.collapsedPlans["r1"]; !known || collapsed {
		t.Errorf("命中更新不应改写用户展开的折叠状态，got known=%v collapsed=%v", known, collapsed)
	}

	// 不同正文：追加新条目。
	other := Approval{ThreadID: "t1", RequestID: "r3", Kind: ApprovalKindPlan, Detail: "另一份计划"}
	s.recordResolvedPlan(other, true)
	if got := len(s.resolvedPlans["t1"]); got != 2 {
		t.Fatalf("不同正文应追加，got %d 条", got)
	}
	if !s.collapsedPlans["r3"] {
		t.Errorf("追加的新条目应默认折叠")
	}
}

// TestRecordResolvedPlanDedupByRequestID 覆盖 RequestID 去重（正文不同也视为同一条）。
func TestRecordResolvedPlanDedupByRequestID(t *testing.T) {
	s := newChatState(nil)
	s.selectedThreadID = "t1"
	s.resolvedPlans = make(map[string][]resolvedPlan)
	s.collapsedPlans = make(map[string]bool)

	s.recordResolvedPlan(Approval{ThreadID: "t1", RequestID: "r1", Kind: ApprovalKindPlan, Detail: "旧正文"}, true)
	s.recordResolvedPlan(Approval{ThreadID: "t1", RequestID: "r1", Kind: ApprovalKindPlan, Detail: "新正文"}, true)
	if got := len(s.resolvedPlans["t1"]); got != 1 {
		t.Fatalf("同 RequestID 应去重，got %d 条", got)
	}
	if got := s.resolvedPlans["t1"][0].Plan; got != "旧正文" {
		t.Errorf("命中更新保留原正文，got %q", got)
	}
}
