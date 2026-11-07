package swarm

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"mcode/internal/store"
)

// 短汇报与改动前逐字一致：不加 ID 字段，也没有截断提示。
func TestBuildReport_ShortSummaryUnchanged(t *testing.T) {
	bee := Bee{ThreadID: "bee-short", Name: "Ada", Task: "改文件", Status: StatusCompleted, Merged: true, Summary: "  改好了  "}
	want := ReportPrefix + "Ada 已结束工作。\n\n\n" +
		"## Ada：改文件 (已完成)\n" +
		"- 合并结果：已合入\n" +
		"- 改动文件：null\n- 冲突文件：null\n" +
		"- 汇报总结：改好了"
	if got := BuildReport([]Bee{bee}); got != want {
		t.Fatalf("短汇报输出变了:\n got: %q\nwant: %q", got, want)
	}
}

// 超过上限的汇报只保留前 ReportSummaryLimit 个 rune，带 ID 字段与截断提示行。
func TestBuildReport_LongSummaryTruncated(t *testing.T) {
	full := strings.Repeat("长", ReportSummaryLimit+500)
	bee := Bee{ThreadID: "bee-long", Name: "Ada", Task: "改文件", Status: StatusCompleted, Merged: true, Summary: full}
	got := BuildReport([]Bee{bee})

	idLine := ReportIDPrefix + "bee-long\n- 汇报总结："
	if !strings.Contains(got, "- 改动文件：null\n- 冲突文件：null\n"+idLine) {
		t.Fatalf("ID 字段应排在其他字段之后、汇报总结之前:\n%s", got)
	}
	_, summary, _ := strings.Cut(got, "- 汇报总结：")
	head, note, ok := strings.Cut(summary, "\n")
	if !ok {
		t.Fatalf("截断后应另起一行写提示:\n%s", summary)
	}
	if head != strings.Repeat("长", ReportSummaryLimit) {
		t.Fatalf("应只保留前 %d 个 rune, 实际 %d", ReportSummaryLimit, utf8.RuneCountInString(head))
	}
	wantNote := fmt.Sprintf("%s全文共 %d 字", ReportTruncatedPrefix, ReportSummaryLimit+500)
	if !strings.HasPrefix(note, wantNote) || !strings.Contains(note, "swarm_report_get") {
		t.Fatalf("截断提示不对: %q", note)
	}
}

// 全文存档在投递汇报前写入，ClearFinished 丢掉 bee 记录之后仍能读到。
func TestArchiveReport_SurvivesClearFinished(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	defer st.Close()

	const leadID = "lead-archive"
	full := strings.Repeat("全", ReportSummaryLimit*2)
	var reported string
	mgr := NewManager(Options{
		Driver:        newFakeDriver(),
		ArchiveReport: st.SaveSwarmReport,
		OnReport: func(leadThreadID, message string) error {
			// 投递时存档必须已经写好。
			if _, err := st.GetSwarmReport(ctx, "bee-done"); err != nil {
				t.Errorf("投递汇报时存档还没写入: %v", err)
			}
			reported = message
			return nil
		},
	})
	mgr.mu.Lock()
	mgr.leads[leadID] = &leadState{
		leadThreadID: leadID,
		enabled:      true,
		bees:         []Bee{{ThreadID: "bee-done", Name: "Aspen", Task: "调研", Status: StatusCompleted, Summary: full}},
	}
	mgr.mu.Unlock()

	mgr.checkReports(ctx)
	if !strings.Contains(reported, ReportIDPrefix+"bee-done") {
		t.Fatalf("长汇报应带 ID 字段:\n%s", reported)
	}
	if err := mgr.ClearFinished(ctx, leadID); err != nil {
		t.Fatalf("ClearFinished 失败: %v", err)
	}
	if n := len(mgr.State(leadID).Bees); n != 0 {
		t.Fatalf("ClearFinished 后应没有 bee, 实际 %d", n)
	}

	rec, err := st.GetSwarmReport(ctx, "bee-done")
	if err != nil {
		t.Fatalf("ClearFinished 后存档应仍可读: %v", err)
	}
	if rec.LeadThreadID != leadID || rec.Name != "Aspen" || rec.Task != "调研" || rec.Summary != full {
		t.Fatalf("存档内容不完整: %+v", rec)
	}
}
