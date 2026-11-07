package swarm

import (
	"context"
	"strings"
	"testing"
)

// 验证失败的 bee 留下半成品时，汇报里追加「半成品」一行，且原有的改动文件行照旧。
func TestBuildReport_FailedBeeShowsPartialWork(t *testing.T) {
	bee := Bee{
		Name:         "Bee-Partial",
		Task:         "改一半",
		Status:       StatusFailed,
		WorktreePath: "/tmp/wt",
		ChangedFiles: []string{"a.go"},
		LinesAdded:   3,
		LinesDeleted: 1,
		Summary:      "x",
	}
	report := BuildReport([]Bee{bee})
	if !strings.Contains(report, "半成品：失败前已改动 1 个文件") {
		t.Fatalf("汇报中缺少半成品行:\n%s", report)
	}
	if !strings.Contains(report, "- 改动文件：") {
		t.Fatalf("汇报中缺少改动文件行:\n%s", report)
	}

	// 没有改动文件的失败 bee 不输出半成品行。
	empty := Bee{Name: "Bee-NoPartial", Task: "无改动", Status: StatusFailed, WorktreePath: "/tmp/wt", Summary: "x"}
	if got := BuildReport([]Bee{empty}); strings.Contains(got, "半成品") {
		t.Fatalf("无改动不应输出半成品行:\n%s", got)
	}
}

// 验证未注入 Git 时 recordPartialWork 不 panic，也不改已有改动清单。
func TestRecordPartialWork_NilGit(t *testing.T) {
	m := NewManager(Options{})
	bee := Bee{
		Status:       StatusFailed,
		WorktreePath: t.TempDir(),
		BaseCommit:   "deadbeef",
		ChangedFiles: []string{"a.go"},
	}
	m.recordPartialWork(context.Background(), &bee)
	if len(bee.ChangedFiles) != 1 || bee.ChangedFiles[0] != "a.go" {
		t.Fatalf("Git 为 nil 时不应改动 ChangedFiles，得到 %v", bee.ChangedFiles)
	}
	if bee.MergeError != "" || bee.Merged {
		t.Fatalf("recordPartialWork 不应改动合并状态，得到 MergeError=%q Merged=%v", bee.MergeError, bee.Merged)
	}
}
