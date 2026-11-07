package mcodeui

import (
	"strings"
	"testing"
	"time"
)

// TestPlanOutline 覆盖大纲提取：忽略代码块里的 # 行、只认 ATX 标题、最多 8 条。
func TestPlanOutline(t *testing.T) {
	plan := "# 总览\n" +
		"正文一段\n" +
		"## 步骤一\n" +
		"```sh\n" +
		"# 这不是标题，是注释\n" +
		"## 这也不是标题\n" +
		"```\n" +
		"### 细节\n" +
		"1. 普通列表项\n" +
		"####### 七个井号不是标题\n" +
		"#没有空格也不是标题\n" +
		"    ~~~~\n" +
		"    # 波浪线围栏里的也不算\n" +
		"    ~~~~\n" +
		"## 测试\n"
	got := planOutline(plan, planOutlineMaxItems)
	want := []planHeading{
		{Level: 1, Text: "总览"},
		{Level: 2, Text: "步骤一"},
		{Level: 3, Text: "细节"},
		{Level: 2, Text: "测试"},
	}
	if len(got) != len(want) {
		t.Fatalf("planOutline 条数 = %d (%+v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("planOutline[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestPlanOutlineLimit 覆盖最多 8 条截断。
func TestPlanOutlineLimit(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 12; i++ {
		b.WriteString("# 标题")
		b.WriteByte(byte('A' + i))
		b.WriteString("\n")
	}
	got := planOutline(b.String(), planOutlineMaxItems)
	if len(got) != planOutlineMaxItems {
		t.Fatalf("planOutline 条数 = %d, want %d", len(got), planOutlineMaxItems)
	}
	if got[0].Text != "标题A" || got[planOutlineMaxItems-1].Text != "标题H" {
		t.Errorf("planOutline 首尾 = %q / %q", got[0].Text, got[planOutlineMaxItems-1].Text)
	}
	if planOutline("没有标题的正文\n第二行", planOutlineMaxItems) != nil {
		t.Errorf("没有标题时应返回 nil")
	}
}

// TestPlanPreviewLines 覆盖预览截断。
func TestPlanPreviewLines(t *testing.T) {
	plan := "1\n2\n3\n4\n5\n6\n7\n8"
	if got := planPreviewLines(plan, planPreviewMaxLines); got != "1\n2\n3\n4\n5\n6" {
		t.Errorf("planPreviewLines = %q", got)
	}
	short := "1\n2"
	if got := planPreviewLines(short, planPreviewMaxLines); got != short {
		t.Errorf("短正文应原样返回，got %q", got)
	}
}

// TestPlanReaderDocumentTOC 覆盖目录拼接：标题达到阈值才拼，且链接锚点用 markdown.Slug。
func TestPlanReaderDocumentTOC(t *testing.T) {
	plan := "## 第一步 改 UI\n\n正文\n\n## 第二步 补测试\n\n正文\n"
	two := planOutline(plan, planTOCMaxItems)
	if len(two) != 2 {
		t.Fatalf("标题数 = %d, want 2", len(two))
	}
	if got := planReaderDocument(plan, two); got != plan {
		t.Errorf("标题少于 %d 条时不应拼目录，got %q", planOutlineForTOCThreshold, got)
	}

	threePlan := plan + "## 第三步 验收\n"
	three := planOutline(threePlan, planTOCMaxItems)
	doc := planReaderDocument(threePlan, three)
	if !strings.HasPrefix(doc, "## 目录\n\n") {
		t.Fatalf("文档应以目录开头，got %q", doc)
	}
	for _, want := range []string{
		"- [第一步 改 UI](#第一步-改-ui)",
		"- [第二步 补测试](#第二步-补测试)",
		"- [第三步 验收](#第三步-验收)",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("目录缺少 %q，got %q", want, doc)
		}
	}
	if !strings.HasSuffix(doc, threePlan) {
		t.Errorf("目录之后应原样拼上计划正文")
	}
}

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

// TestPlanReaderPlanForLocked 覆盖阅读弹窗的正文来源查找。
func TestPlanReaderPlanForLocked(t *testing.T) {
	s := newChatState(nil)
	s.approvals = []Approval{
		{RequestID: "r-plan", Kind: ApprovalKindPlan, Detail: "待批计划正文"},
		{RequestID: "r-cmd", Kind: ApprovalKindCommand, Detail: "不是计划"},
	}
	s.resolvedPlans = map[string][]resolvedPlan{
		"t1": {{ID: "done-1", Plan: "已批准计划正文", Approved: true, Time: time.Now()}},
	}

	entry, ok := s.planReaderPlanForLocked("r-plan")
	if !ok || entry.Plan != "待批计划正文" || entry.PlanID != "" {
		t.Errorf("待批计划查找失败：%+v ok=%v", entry, ok)
	}
	entry, ok = s.planReaderPlanForLocked("done-1")
	if !ok || entry.Plan != "已批准计划正文" || entry.PlanID != "done-1" {
		t.Errorf("历史计划查找失败：%+v ok=%v", entry, ok)
	}
	if _, ok := s.planReaderPlanForLocked("r-cmd"); ok {
		t.Errorf("非计划类审批不应被当作计划正文来源")
	}
	if _, ok := s.planReaderPlanForLocked("missing"); ok {
		t.Errorf("找不到来源时应返回 false")
	}
	if _, ok := s.planReaderPlanForLocked(""); ok {
		t.Errorf("空 target 应返回 false")
	}
}
