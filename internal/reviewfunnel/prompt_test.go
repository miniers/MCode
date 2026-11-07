package reviewfunnel

import (
	"fmt"
	"strings"
	"testing"

	"mcode/internal/reviewcheck"
)

// makeFileDiff 构造单个文件的 diff 文本，lines 控制体量。
func makeFileDiff(path string, lines int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", path, path)
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", path, path)
	fmt.Fprintf(&b, "@@ -1,0 +1,%d @@\n", lines)
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, "+line %d of %s with some padding to grow the diff\n", i, path)
	}
	return b.String()
}

// fenceBody 提取提示词里第一个 diff 围栏的内容。
func fenceBody(t *testing.T, prompt string) string {
	t.Helper()
	start := strings.Index(prompt, "```diff\n")
	if start < 0 {
		t.Fatalf("提示词里没有 diff 围栏")
	}
	start += len("```diff\n")
	end := strings.Index(prompt[start:], "```")
	if end < 0 {
		t.Fatalf("diff 围栏没有闭合")
	}
	return prompt[start : start+end]
}

func TestBuildPromptBatchKeepsIndexes(t *testing.T) {
	reqs := []L2Request{
		{Index: 0, Name: "alpha", Brief: "first", Diff: makeFileDiff("internal/a.go", 5)},
		{Index: 3, Name: "beta", Brief: "second", Diff: makeFileDiff("internal/b.go", 5)},
		{Index: 7, Name: "gamma", Brief: "third", Diff: makeFileDiff("internal/c.go", 5)},
	}
	prompt, truncated := BuildPrompt(reqs, DefaultBudget())
	if len(truncated) != 0 {
		t.Fatalf("小 diff 不应被截断: %+v", truncated)
	}
	for _, req := range reqs {
		if !strings.Contains(prompt, fmt.Sprintf("Index: %d", req.Index)) {
			t.Fatalf("提示词缺少 index %d", req.Index)
		}
		if !strings.Contains(prompt, "Name: "+req.Name) {
			t.Fatalf("提示词缺少 name %q", req.Name)
		}
	}
	if got := strings.Count(prompt, "=== Request index="); got != len(reqs) {
		t.Fatalf("请求小节数 = %d, want %d", got, len(reqs))
	}
}

func TestBuildPromptContainsSafetyRules(t *testing.T) {
	prompt, _ := BuildPrompt([]L2Request{{
		Index:   0,
		Name:    "alpha",
		Brief:   "改 internal/a.go",
		Diff:    makeFileDiff("internal/a.go", 3),
		Scope:   reviewcheck.Scope{Known: true, Hard: []string{"internal/z.go"}, Soft: []string{"docs/x.md"}},
		Summary: "done",
	}}, DefaultBudget())

	lower := strings.ToLower(prompt)
	for _, want := range []string{
		"untrusted input",
		"docs/superpowers/plans/",
		"escalate",
		"tests or builds were not run",
		"\"reviews\"",
		"path:line",
		"hard out-of-scope files",
		"soft out-of-scope files",
		"exactly one json object",
	} {
		if !strings.Contains(lower, strings.ToLower(want)) {
			t.Fatalf("安全规则缺少 %q", want)
		}
	}
	if !strings.Contains(prompt, "internal/z.go") || !strings.Contains(prompt, "docs/x.md") {
		t.Fatalf("提示词缺少越界文件列表")
	}
}

func TestBuildPromptBudgetTruncatesByFile(t *testing.T) {
	diff := makeFileDiff("internal/a.go", 200) +
		makeFileDiff("internal/b.go", 200) +
		makeFileDiff("docs/notes.md", 200)
	budget := Budget{TotalBytes: 6000, PerRequestBytes: 6000, PerFileBytes: 2500}
	prompt, truncated := BuildPrompt([]L2Request{{
		Index: 7,
		Name:  "oversized",
		Diff:  diff,
	}}, budget)

	if !truncated[7] {
		t.Fatalf("超预算的 bee 应标记为截断: %+v", truncated)
	}
	if !strings.Contains(prompt, "[diff truncated") {
		t.Fatalf("提示词缺少截断标记")
	}
	body := fenceBody(t, prompt)
	if len(body) > budget.TotalBytes {
		t.Fatalf("diff 长度 %d 超过预算 %d", len(body), budget.TotalBytes)
	}

	// 单文件超限时也要按文件截断并带标记。
	perFileBudget := Budget{TotalBytes: 1 << 20, PerRequestBytes: 1 << 20, PerFileBytes: 800}
	prompt, truncated = BuildPrompt([]L2Request{{
		Index: 2,
		Name:  "per-file",
		Diff:  makeFileDiff("internal/a.go", 500) + makeFileDiff("internal/b.go", 500),
	}}, perFileBudget)
	if !truncated[2] {
		t.Fatalf("单文件超限应标记为截断: %+v", truncated)
	}
	if !strings.Contains(prompt, "file size limit reached") {
		t.Fatalf("提示词缺少单文件截断标记")
	}
	for _, path := range []string{"internal/a.go", "internal/b.go"} {
		if !strings.Contains(prompt, path) {
			t.Fatalf("按文件截断时不应丢掉整个文件 %s", path)
		}
	}
}

func TestBuildPromptReworkUsesDelta(t *testing.T) {
	fullDiffMarker := "FULL_DIFF_MUST_NOT_APPEAR"
	deltaMarker := "DELTA_MUST_APPEAR"
	prompt, truncated := BuildPrompt([]L2Request{{
		Index: 1,
		Name:  "rework",
		Diff:  fullDiffMarker,
		Rework: &ReworkInput{
			PreviousReason: "上次驳回原因：internal/a.go:12 空指针",
			Delta:          "diff --git a/internal/a.go b/internal/a.go\n+fixed\n" + deltaMarker,
		},
	}}, DefaultBudget())

	if strings.Contains(prompt, fullDiffMarker) {
		t.Fatalf("返工审查不应包含完整 diff")
	}
	for _, want := range []string{"上次驳回原因：internal/a.go:12 空指针", deltaMarker, "Ignore every part"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("返工提示词缺少 %q", want)
		}
	}
	if len(truncated) != 0 {
		t.Fatalf("小 delta 不应被截断: %+v", truncated)
	}
}

func TestBuildPromptTruncatesBriefAndSummary(t *testing.T) {
	briefTail := "BRIEF_TAIL_MUST_BE_GONE"
	summaryTail := "SUMMARY_TAIL_MUST_BE_GONE"
	prompt, _ := BuildPrompt([]L2Request{{
		Index:   0,
		Name:    "long",
		Brief:   strings.Repeat("a", 8000) + briefTail,
		Summary: strings.Repeat("b", 6000) + summaryTail,
		Diff:    makeFileDiff("internal/a.go", 2),
	}}, DefaultBudget())

	if strings.Contains(prompt, briefTail) {
		t.Fatalf("brief 未截断到 6KB")
	}
	if strings.Contains(prompt, summaryTail) {
		t.Fatalf("summary 未截断到 4KB")
	}
	if !strings.Contains(prompt, "Brief") || !strings.Contains(prompt, "[truncated]") {
		t.Fatalf("提示词缺少 brief 截断标记")
	}
}

func TestDefaultBudget(t *testing.T) {
	budget := DefaultBudget()
	if budget.TotalBytes != 96*1024 {
		t.Fatalf("TotalBytes = %d, want %d", budget.TotalBytes, 96*1024)
	}
	if budget.PerRequestBytes != 32*1024 {
		t.Fatalf("PerRequestBytes = %d, want %d", budget.PerRequestBytes, 32*1024)
	}
	if budget.PerFileBytes != 24*1024 {
		t.Fatalf("PerFileBytes = %d, want %d", budget.PerFileBytes, 24*1024)
	}
	if MinRequestBytes != 8*1024 {
		t.Fatalf("MinRequestBytes = %d, want %d", MinRequestBytes, 8*1024)
	}
}
