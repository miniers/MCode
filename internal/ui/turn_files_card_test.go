package mcodeui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// turnFileTool 构造一条编辑类工具消息：tool 名称、状态与输入里的 file_path。
func turnFileTool(id, name, status, path string, turnIndex int, text string) Message {
	return Message{
		ID:         id,
		Role:       "tool",
		ToolName:   name,
		ToolInput:  `{"file_path":` + strconv.Quote(path) + `}`,
		ToolStatus: status,
		Text:       text,
		TurnIndex:  turnIndex,
	}
}

// TestCollectTurnEditedFiles 验证本回合改动文件列表：成功完成的编辑类工具才算，
// 同路径按首次出现去重，失败 / 被拦截 / 运行中的不算，其他回合与 turnIndex 为 0 时不算。
func TestCollectTurnEditedFiles(t *testing.T) {
	msgs := []Message{
		turnFileTool("t1", "write", "completed", "a.go", 1, ""),
		turnFileTool("t2", "edit", "completed", "b.go", 1, ""),
		turnFileTool("t3", "write", "completed", "a.go", 1, ""),
		turnFileTool("t4", "write", "failed", "failed.go", 1, ""),
		turnFileTool("t5", "write", "blocked", "blocked.go", 1, ""),
		turnFileTool("t6", "write", "running", "running.go", 1, ""),
		turnFileTool("t7", "write", "completed", "other-turn.go", 2, ""),
		{ID: "t8", Role: "tool", ToolName: "bash", ToolStatus: "completed", Text: `Bash {"file_path":"cmd.go"}`, TurnIndex: 1},
		{ID: "u1", Role: "user", Text: "改文件", TurnIndex: 1},
	}
	got := collectTurnEditedFiles(msgs, 1)
	want := []string{"a.go", "b.go"}
	if len(got) != len(want) {
		t.Fatalf("collectTurnEditedFiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("collectTurnEditedFiles = %v, want %v", got, want)
		}
	}

	if got := collectTurnEditedFiles(msgs, 0); len(got) != 0 {
		t.Fatalf("turnIndex 为 0 时应返回空，实际 %v", got)
	}
	if got := collectTurnEditedFiles(msgs, 3); len(got) != 0 {
		t.Fatalf("没有工具消息的回合应返回空，实际 %v", got)
	}
}

// TestCollectTurnEditedPaths 验证路径条目带首次出现的工具消息 ID（stat 缓存按它区分）。
func TestCollectTurnEditedPaths(t *testing.T) {
	msgs := []Message{
		turnFileTool("m1", "write", "completed", "a.go", 2, ""),
		turnFileTool("m2", "edit", "completed", "a.go", 2, ""),
		turnFileTool("m3", "edit", "completed", "b.go", 2, ""),
	}
	paths := collectTurnEditedPaths(msgs, 2)
	if len(paths) != 2 {
		t.Fatalf("应去重成 2 条，实际 %v", paths)
	}
	if paths[0].path != "a.go" || paths[0].msgID != "m1" {
		t.Fatalf("首条应为 a.go / m1，实际 %+v", paths[0])
	}
	if paths[1].path != "b.go" || paths[1].msgID != "m3" {
		t.Fatalf("第二条应为 b.go / m3，实际 %+v", paths[1])
	}
}

// TestResolveTurnFilePath 验证路径解析：相对路径拼会话工作目录，绝对路径原样清洗，
// 拿不到工作目录的相对路径解析失败返回空串。
func TestResolveTurnFilePath(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		workDir string
		path    string
		want    string
	}{
		{"相对路径", dir, "internal/ui/a.go", filepath.Join(dir, "internal/ui/a.go")},
		{"相对路径清理", dir, "./a/../b.go", filepath.Join(dir, "b.go")},
		{"绝对路径", dir, filepath.Join(dir, "x/y.go"), filepath.Join(dir, "x/y.go")},
		{"绝对路径清理", "", dir + "/x/../y.go", filepath.Join(dir, "y.go")},
		{"空路径", dir, "  ", ""},
		{"没有工作目录的相对路径", "", "a.go", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveTurnFilePath(tc.workDir, tc.path); got != tc.want {
				t.Fatalf("resolveTurnFilePath(%q, %q) = %q, want %q", tc.workDir, tc.path, got, tc.want)
			}
		})
	}
}

// TestTurnEditedFilesFor 验证只在文件真实存在时展示：相对与绝对路径都能解析，
// 本机不存在的路径被过滤掉。
func TestTurnEditedFilesFor(t *testing.T) {
	dir := t.TempDir()
	relName := "keep-relative.txt"
	absName := "keep-absolute.txt"
	for _, name := range []string{relName, absName} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644); err != nil {
			t.Fatalf("写测试文件 %s: %v", name, err)
		}
	}
	absPath := filepath.Join(dir, absName)

	msgs := []Message{
		turnFileTool("exists-rel", "write", "completed", relName, 1, ""),
		turnFileTool("exists-abs", "edit", "completed", absPath, 1, ""),
		turnFileTool("missing", "write", "completed", "missing.txt", 1, ""),
	}
	files := turnEditedFilesFor(msgs, 1, dir)
	if len(files) != 2 {
		t.Fatalf("应只剩 2 个存在的文件，实际 %+v", files)
	}
	if files[0].name != relName || files[0].abs != filepath.Join(dir, relName) {
		t.Fatalf("首条应为相对路径解析结果，实际 %+v", files[0])
	}
	if files[1].name != absName || files[1].abs != absPath {
		t.Fatalf("第二条应为绝对路径，实际 %+v", files[1])
	}

	// 目录不算文件。
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	if got := turnEditedFilesFor([]Message{turnFileTool("is-dir", "write", "completed", sub, 1, "")}, 1, dir); len(got) != 0 {
		t.Fatalf("目录不应算改动文件，实际 %+v", got)
	}
}

// TestTurnFilesPlan 验证收起 / 展开时的显示条数：不超过上限全显示，
// 收起态截断到上限并折出剩余数量，展开后全部显示。
func TestTurnFilesPlan(t *testing.T) {
	cases := []struct {
		name            string
		total           int
		expanded        bool
		visible, hidden int
	}{
		{"没有文件", 0, false, 0, 0},
		{"少量收起", 2, false, 2, 0},
		{"正好上限", turnFilesDisplayLimit, false, turnFilesDisplayLimit, 0},
		{"超过上限收起", 8, false, turnFilesDisplayLimit, 8 - turnFilesDisplayLimit},
		{"超过上限展开", 8, true, 8, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			visible, hidden := turnFilesPlan(tc.total, tc.expanded)
			if visible != tc.visible || hidden != tc.hidden {
				t.Fatalf("turnFilesPlan(%d, %v) = (%d, %d), want (%d, %d)",
					tc.total, tc.expanded, visible, hidden, tc.visible, tc.hidden)
			}
		})
	}
}

// TestTurnFilePreviewPath 验证文件面板可用的相对路径：工具给的相对路径原样用，
// 绝对路径按工作目录换算，工作目录外的绝对路径返回空串。
func TestTurnFilePreviewPath(t *testing.T) {
	dir := t.TempDir()
	inside := turnEditedFile{path: filepath.Join(dir, "internal/ui/a.go"), abs: filepath.Join(dir, "internal/ui/a.go")}
	if got := turnFilePreviewPath(dir, inside); got != filepath.Join("internal/ui", "a.go") {
		t.Fatalf("工作目录内的绝对路径应换算成相对路径，实际 %q", got)
	}
	outside := turnEditedFile{path: "/elsewhere/b.go", abs: "/elsewhere/b.go"}
	if got := turnFilePreviewPath(dir, outside); got != "" {
		t.Fatalf("工作目录外的绝对路径应返回空串，实际 %q", got)
	}
	rel := turnEditedFile{path: "c.go", abs: filepath.Join(dir, "c.go")}
	if got := turnFilePreviewPath(dir, rel); got != "c.go" {
		t.Fatalf("相对路径应原样返回，实际 %q", got)
	}
}

// turnFilesTestDiff 构造测试用 unified diff：每个文件一个 hunk，按给定增删行数生成内容。
func turnFilesTestDiff(stats map[string][2]int) string {
	paths := make([]string, 0, len(stats))
	for p := range stats {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		adds, dels := stats[p][0], stats[p][1]
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1,%d +1,%d @@\n", p, p, p, p, 1+dels, 1+adds)
		b.WriteString(" context line\n")
		for i := 0; i < adds; i++ {
			fmt.Fprintf(&b, "+added %d\n", i)
		}
		for i := 0; i < dels; i++ {
			fmt.Fprintf(&b, "-removed %d\n", i)
		}
	}
	return b.String()
}

// turnFilesDiffMessages 构造一个回合：每条路径一条写文件工具，最后是带 TurnID 的完成消息。
func turnFilesDiffMessages(paths []string) []Message {
	msgs := []Message{{ID: "u1", Role: "user", Kind: "text", Text: "改文件", TurnIndex: 1, IsFinal: true}}
	for i, p := range paths {
		msgs = append(msgs, turnFileTool(fmt.Sprintf("t%d", i+1), "write", "completed", p, 1, ""))
	}
	msgs = append(msgs, Message{ID: "a1", Role: "assistant", Kind: "text", Text: "改好了",
		TurnIndex: 1, TurnID: "turn-1", IsFinal: true})
	return msgs
}

// setupTurnFilesDiffFake 准备一个真实存在文件、且带回合级 diff 的 fake 会话。
func setupTurnFilesDiffFake(t *testing.T, paths []string, diff string) *FakeClient {
	t.Helper()
	dir := t.TempDir()
	for _, p := range paths {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("建目录 %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte("package a\n"), 0o644); err != nil {
			t.Fatalf("写测试文件 %s: %v", full, err)
		}
	}
	fake := NewFakeClient()
	fake.messages["t1"] = turnFilesDiffMessages(paths)
	fake.SetThreadWorkDir(dir)
	fake.SetThreadUsage("t1", ThreadUsage{
		Turns: map[int]TokenUsage{1: {Output: 120}},
		Total: TokenUsage{Output: 120},
	})
	fake.SetThreadDiff("t1", "turn-1", ThreadDiff{Diff: diff})
	return fake
}

// TestTurnFilesCardShowsDiffStats 验证两个文件都有统计时：胶囊内有各自的 +x / -y，标题出现总计。
func TestTurnFilesCardShowsDiffStats(t *testing.T) {
	fake := setupTurnFilesDiffFake(t, []string{"first.go", "second.go"}, turnFilesTestDiff(map[string][2]int{
		"first.go":  {12, 3},
		"second.go": {5, 1},
	}))
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("改动文件") })
	waitFor(t, tt, func() bool { return tt.HasText("+12") && tt.HasText("-3") })
	for _, text := range []string{"+5", "-1", "+17", "-4"} {
		if !tt.HasText(text) {
			t.Fatalf("应显示 %q，实际 %q", text, tt.Texts())
		}
	}
}

// turnFilesBlockingDiffClient 让 Diff 挂起，用来稳定观察「diff 加载中」的界面。
type turnFilesBlockingDiffClient struct {
	*FakeClient
	release chan struct{}
}

func (f *turnFilesBlockingDiffClient) Diff(ctx context.Context, threadID, turnID string) (ThreadDiff, error) {
	<-f.release
	return f.FakeClient.Diff(ctx, threadID, turnID)
}

// TestTurnFilesCardLoadingNoStats 验证 diff 未就绪时只有文件数，不出现任何增删行数。
func TestTurnFilesCardLoadingNoStats(t *testing.T) {
	fake := setupTurnFilesDiffFake(t, []string{"first.go", "second.go"}, turnFilesTestDiff(map[string][2]int{
		"first.go":  {12, 3},
		"second.go": {5, 1},
	}))
	blocking := &turnFilesBlockingDiffClient{FakeClient: fake, release: make(chan struct{})}
	defer close(blocking.release)
	tt := ui.NewTester(Root(blocking), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("改动文件") })
	for _, text := range []string{"+12", "-3", "+17", "-4"} {
		if tt.HasText(text) {
			t.Fatalf("diff 加载中不应显示 %q", text)
		}
	}
}

// turnFilesTestDiffWithStatus 构造带文件状态的 unified diff：A 用 /dev/null 作旧路径，
// D 用 /dev/null 作新路径，其余按普通修改生成；stats 控制每个文件的增删行数。
func turnFilesTestDiffWithStatus(status map[string]string, stats map[string][2]int) string {
	paths := make([]string, 0, len(stats))
	for p := range stats {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		adds, dels := stats[p][0], stats[p][1]
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n", p, p)
		switch status[p] {
		case "A":
			fmt.Fprintf(&b, "new file mode 100644\n--- /dev/null\n+++ b/%s\n", p)
		case "D":
			fmt.Fprintf(&b, "deleted file mode 100644\n--- a/%s\n+++ /dev/null\n", p)
		default:
			fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", p, p)
		}
		fmt.Fprintf(&b, "@@ -1,%d +1,%d @@\n", 1+dels, 1+adds)
		b.WriteString(" context line\n")
		for i := 0; i < adds; i++ {
			fmt.Fprintf(&b, "+added %d\n", i)
		}
		for i := 0; i < dels; i++ {
			fmt.Fprintf(&b, "-removed %d\n", i)
		}
	}
	return b.String()
}

// turnFilesHasExactText 判断界面是否有完全等于 want 的文字（状态字母只有单字，不能用包含匹配）。
func turnFilesHasExactText(tt *ui.Tester, want string) bool {
	for _, text := range tt.Texts() {
		if text == want {
			return true
		}
	}
	return false
}

// TestTurnFilesCardRowsAlignStats 验证行式列表：不同位数增删数字与文件名在同一行，
// 每个文件都能找到自己的 +N / -N。
func TestTurnFilesCardRowsAlignStats(t *testing.T) {
	fake := setupTurnFilesDiffFake(t, []string{"first.go", "second.go", "third.go"}, turnFilesTestDiff(map[string][2]int{
		"first.go":  {12, 3},
		"second.go": {5, 1},
		"third.go":  {1234, 567},
	}))
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("+1234") && tt.HasText("-567") })
	cases := []struct{ name, add, del string }{
		{"first.go", "+12", "-3"},
		{"second.go", "+5", "-1"},
		{"third.go", "+1234", "-567"},
	}
	for _, tc := range cases {
		nameRect, ok := tt.Find(tc.name)
		if !ok {
			t.Fatalf("找不到文件名 %q，实际 %q", tc.name, tt.Texts())
		}
		for _, stat := range []string{tc.add, tc.del} {
			statRect, ok := tt.Find(stat)
			if !ok {
				t.Fatalf("找不到 %q（%s 行），实际 %q", stat, tc.name, tt.Texts())
			}
			statMid := statRect.Y + statRect.H/2
			nameMid := nameRect.Y + nameRect.H/2
			if diff := statMid - nameMid; diff < -4 || diff > 4 {
				t.Fatalf("%q 与文件名 %q 垂直中心不齐：mid=%v / %v", stat, tc.name, statMid, nameMid)
			}
		}
	}
}

// TestTurnFilesCardStatusColumn 验证状态字母胶囊：状态全空时整列不画，新增文件显示 A、修改显示 M。
func TestTurnFilesCardStatusColumn(t *testing.T) {
	t.Run("状态全空时整列不画", func(t *testing.T) {
		fake := setupTurnFilesDiffFake(t, []string{"first.go"}, "")
		tt := ui.NewTester(Root(fake), 1000, 700)
		waitFor(t, tt, func() bool { return tt.HasText("改动文件") })
		for _, letter := range []string{"A", "M", "D"} {
			if turnFilesHasExactText(tt, letter) {
				t.Fatalf("状态全空时不应出现 %q，实际 %q", letter, tt.Texts())
			}
		}
	})
	t.Run("新增与修改显示状态字母", func(t *testing.T) {
		fake := setupTurnFilesDiffFake(t, []string{"new.go", "edited.go"}, turnFilesTestDiffWithStatus(
			map[string]string{"new.go": "A", "edited.go": "M"},
			map[string][2]int{"new.go": {2, 0}, "edited.go": {1, 1}},
		))
		tt := ui.NewTester(Root(fake), 1000, 700)
		waitFor(t, tt, func() bool { return tt.HasText("+2") && turnFilesHasExactText(tt, "A") })
		if !turnFilesHasExactText(tt, "M") {
			t.Fatalf("修改文件应显示 M，实际 %q", tt.Texts())
		}
	})
}

// TestTurnFilesCardCollapsesAtSixRows 验证默认只显示 6 行，其余折成「还有 N 个」。
func TestTurnFilesCardCollapsesAtSixRows(t *testing.T) {
	paths := make([]string, 8)
	stats := make(map[string][2]int, 8)
	for i := range paths {
		paths[i] = fmt.Sprintf("f%d.go", i)
		stats[paths[i]] = [2]int{i + 1, 1}
	}
	fake := setupTurnFilesDiffFake(t, paths, turnFilesTestDiff(stats))
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("改动文件") })
	waitFor(t, tt, func() bool { return turnFilesHasExactText(tt, "还有 2 个") })
	for i, p := range paths {
		has := turnFilesHasExactText(tt, p)
		if i < turnFilesDisplayLimit && !has {
			t.Fatalf("收起时应显示第 %d 个文件 %q，实际 %q", i+1, p, tt.Texts())
		}
		if i >= turnFilesDisplayLimit && has {
			t.Fatalf("收起时不应显示第 %d 个文件 %q，实际 %q", i+1, p, tt.Texts())
		}
	}
}

// TestTurnFilesCardPartialStats 验证有文件匹配不到 diff 时：文件数照常，匹配到的胶囊有数字，标题不给总计。
func TestTurnFilesCardPartialStats(t *testing.T) {
	fake := setupTurnFilesDiffFake(t, []string{"first.go", "second.go", "third.go"}, turnFilesTestDiff(map[string][2]int{
		"first.go":  {7, 2},
		"second.go": {4, 1},
	}))
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("改动文件") })
	waitFor(t, tt, func() bool { return tt.HasText("+7") && tt.HasText("-2") })
	for _, text := range []string{"+4", "-1"} {
		if !tt.HasText(text) {
			t.Fatalf("匹配到的文件应显示 %q，实际 %q", text, tt.Texts())
		}
	}
	// 若错误地给出总计，会出现 +11 / -3；部分未知时不应出现。
	for _, text := range []string{"+11", "-3"} {
		if tt.HasText(text) {
			t.Fatalf("部分文件未知时不应出现总计 %q，实际 %q", text, tt.Texts())
		}
	}
}
