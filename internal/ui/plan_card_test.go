package mcodeui

import (
	"testing"
)

// 同正文的新请求仍是独立版本，旧版裁决不能被覆盖。
func TestRecordResolvedPlanKeepsVersions(t *testing.T) {
	s := newChatState(nil)
	s.selectedThreadID = "t1"
	a := Approval{ThreadID: "t1", RequestID: "r1", Kind: ApprovalKindPlan, Detail: "同一份计划"}
	s.recordResolvedPlan(a, false, "请补测试")
	s.collapsedPlans["r1"] = false
	a.RequestID = "r2"
	s.recordResolvedPlan(a, true)
	plans := s.resolvedPlans["t1"]
	if len(plans) != 2 || plans[0].Approved || plans[0].Feedback != "请补测试" || !plans[1].Approved {
		t.Fatalf("应保留两次裁决：%+v", plans)
	}
	if s.collapsedPlans["r1"] || !s.collapsedPlans["r2"] {
		t.Fatal("不能改写旧版展开状态")
	}
}

func TestPlanTitle(t *testing.T) {
	for _, tc := range []struct{ plan, want string }{
		{"", "计划"}, {"正文", "计划"}, {"# 首个标题\n## 第二个", "首个标题"},
		{"正文\n### 三级标题", "三级标题"}, {"#### 四级\n## 二级", "二级"},
		{"#无空格", "计划"}, {"## \n# 标题 ###", "标题"}, {"# C#", "C#"},
		{"```md\n# 示例\n```\n# 正文", "正文"}, {"~~~\n## 示例\n~~~\n### 正文", "正文"},
		{"# 标题\r\n正文", "标题"},
	} {
		if got := planTitle(tc.plan); got != tc.want {
			t.Errorf("planTitle(%q) = %q，期望 %q", tc.plan, got, tc.want)
		}
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
