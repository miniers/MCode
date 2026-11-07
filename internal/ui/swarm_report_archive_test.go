package mcodeui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"mcode/internal/swarm"
)

// 短汇报不带 ID 字段、也不标记截断，解析结果与改动前一致。
func TestParseSwarmReport_ShortSummaryNoID(t *testing.T) {
	text := swarm.BuildReport([]swarm.Bee{{ThreadID: "bee-short", Name: "Ada", Task: "改文件", Status: swarm.StatusCompleted, Merged: true, Summary: "改好了"}})
	_, entries := parseSwarmReport(text)
	if len(entries) != 1 {
		t.Fatalf("汇报项数量 = %d，预期 1", len(entries))
	}
	e := entries[0]
	if e.BeeID != "" || e.Truncated {
		t.Fatalf("短汇报不应带 ID 或截断标记: %+v", e)
	}
	if e.Summary != "改好了" {
		t.Fatalf("Summary = %q，预期 改好了", e.Summary)
	}
}

// 超长汇报被后端截断后：解析出 bee ID 与截断标记，截断提示行不并入总结正文。
func TestParseSwarmReport_TruncatedSummary(t *testing.T) {
	full := strings.Repeat("长", swarm.ReportSummaryLimit+300)
	text := swarm.BuildReport([]swarm.Bee{{ThreadID: "bee-long", Name: "Ada", Task: "改文件", Status: swarm.StatusCompleted, Merged: true, Summary: full}})
	_, entries := parseSwarmReport(text)
	if len(entries) != 1 {
		t.Fatalf("汇报项数量 = %d，预期 1", len(entries))
	}
	e := entries[0]
	if e.BeeID != "bee-long" {
		t.Errorf("BeeID = %q，预期 bee-long", e.BeeID)
	}
	if !e.Truncated {
		t.Error("Truncated = false，预期 true")
	}
	if n := utf8.RuneCountInString(e.Summary); n != swarm.ReportSummaryLimit {
		t.Errorf("Summary 长度 = %d，预期 %d", n, swarm.ReportSummaryLimit)
	}
	if strings.Contains(e.Summary, swarm.ReportTruncatedPrefix) || strings.Contains(e.Summary, "swarm_report_get") {
		t.Errorf("截断提示行不应并入总结正文: %q", e.Summary)
	}
}

// 存档读不到时，展开卡片退回显示消息里的截断文本。
func TestSwarmReportDisplaySummary_FallsBackWhenArchiveMissing(t *testing.T) {
	s := &chatState{client: NewFakeClient()}
	e := swarmReportEntry{BeeID: "bee-missing", Truncated: true, Summary: "前一千五百字"}
	if got := swarmReportDisplaySummary(s, e); got != e.Summary {
		t.Fatalf("存档读不到时应退回截断文本，实际 %q", got)
	}
	// 未被截断的汇报直接用消息里的文本，不读存档。
	short := swarmReportEntry{BeeID: "bee-short", Summary: "完整汇报"}
	if got := swarmReportDisplaySummary(s, short); got != short.Summary {
		t.Fatalf("未截断汇报应原样返回，实际 %q", got)
	}
}
