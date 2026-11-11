package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

// TestIsFileEditTool 验证文件修改类工具的判定。
func TestIsFileEditTool(t *testing.T) {
	cases := []struct {
		msg  Message
		want bool
	}{
		{Message{Role: "tool", Text: `Edit {"file_path":"/repo/a.go"}`}, true},
		{Message{Role: "tool", Text: `MultiEdit {"file_path":"/repo/a.go"}`}, true},
		{Message{Role: "tool", Text: `Write {"file_path":"/repo/a.go"}`}, true},
		{Message{Role: "tool", Text: `write_file {"path":"a.go"}`}, true},
		{Message{Role: "tool", Text: `edit_file {"path":"a.go"}`}, true},
		{Message{Role: "tool", Text: `apply_patch {"input":"*** Begin Patch"}`}, true},
		{Message{Role: "tool", Text: `applyPatch`}, true},
		{Message{Role: "tool", Text: `str_replace_editor {"path":"a.go"}`}, true},
		{Message{Role: "tool", Text: `mcp__fs__edit_file {"path":"a.go"}`}, true},
		{Message{Role: "tool", Text: `bash {"cmd":"ls"}`}, false},
		{Message{Role: "tool", Text: `Read {"file_path":"/repo/a.go"}`}, false},
		{Message{Role: "tool", Text: ""}, false},
		{Message{Role: "user", Text: `Edit {"file_path":"/repo/a.go"}`}, false},
	}
	for _, tc := range cases {
		if got := isFileEditTool(tc.msg); got != tc.want {
			t.Errorf("isFileEditTool(%q) = %v, want %v", tc.msg.Text, got, tc.want)
		}
	}
}

// TestToolEditedPaths 验证从工具消息文本中提取文件路径。
func TestToolEditedPaths(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{`Edit {"file_path":"/repo/pkg/math.go","old_string":"a","new_string":"b"}` + "\nok", []string{"/repo/pkg/math.go"}},
		{`write_file {"path":"pkg/new.go","content":"x"}`, []string{"pkg/new.go"}},
		{`apply_patch {"input":"*** Begin Patch\n*** Update File: src/a.go\n@@\n*** Add File: src/b.go\n"}`, []string{"src/a.go", "src/b.go"}},
		{`Edit {"file_path":"C:\\repo\\a.go"}`, []string{`C:\repo\a.go`}},
		{`bash {"cmd":"ls"}`, nil},
		{`apply_patch {"input":"*** Begin Patch\n*** Update File: src/trunca`, nil},
	}
	for _, tc := range cases {
		got := toolEditedPaths(Message{Role: "tool", Text: tc.text})
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("toolEditedPaths(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestInlineToolDiffLineLimitAndBinary(t *testing.T) {
	lines := make([]git.Line, toolDiffMaxLines+1)
	for i := range lines {
		lines[i] = git.Line{Kind: '+', Text: fmt.Sprintf("LINE-%d", i)}
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() {
			renderInlineToolDiffFile(c, nil, "m1", 0, git.FileDiff{
				NewPath: "large.go", Hunks: []git.Hunk{{Lines: lines}},
			})
		})
	}, 1000, 10000)
	if !tt.HasText("LINE-199") || tt.HasText("LINE-200") || !tt.HasText("已截断，还有 1 行未显示") {
		t.Fatal("单文件 diff 未按 200 行限制显示")
	}
	binary := ui.NewTester(func(c *ui.Context) {
		renderInlineToolDiffFile(c, nil, "m2", 0, git.FileDiff{OldPath: "old.bin", Status: "deleted", Binary: true})
	}, 1000, 700)
	if !binary.HasText("old.bin") || !binary.HasText("二进制文件") {
		t.Fatal("二进制删除应显示原路径及文件类型提示")
	}
}

const toolDiffTestUnified = `diff --git a/pkg/math.go b/pkg/math.go
--- a/pkg/math.go
+++ b/pkg/math.go
@@ -1,3 +1,3 @@
 func Add(a, b int) int {
-	return a - b
+	return a + b
 }
diff --git a/pkg/other.go b/pkg/other.go
--- a/pkg/other.go
+++ b/pkg/other.go
@@ -1 +1 @@
-OTHER-OLD-LINE
+OTHER-NEW-LINE
`

func TestInlineDiffFourDigitLineNumbers(t *testing.T) {
	file := git.FileDiff{NewPath: "numbers.go", Hunks: []git.Hunk{{Header: "@@ -1234 +1234 @@", Lines: []git.Line{
		{Kind: '-', OldNo: 1234, Text: "old"},
		{Kind: '+', NewNo: 5678, Text: "new"},
	}}}}
	tt := ui.NewTester(func(c *ui.Context) { renderInlineToolDiffFile(c, nil, "number-test", 0, file) }, 800, 600)
	oldRect, oldOK := tt.Find("1234")
	newRect, newOK := tt.Find("5678")
	if !oldOK || !newOK || oldRect.W != toolDiffLineNumberWidth || newRect.W != toolDiffLineNumberWidth {
		t.Fatal("四位行号应显示在固定宽度列中")
	}
	if !tt.HasText("+1") || !tt.HasText("-1") {
		t.Fatal("应显示增删统计")
	}
}

// newToolDiffTester 用给定消息与客户端启动 Root，并等待 ready 文本出现。
func newToolDiffTester(t *testing.T, client Client, ready string) *ui.Tester {
	t.Helper()
	tt := ui.NewTester(Root(legacyDiffClient{client}), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText(ready) })
	return tt
}

// TestInlineToolDiff_ExpandShowsLines 验证展开文件修改工具后按路径筛出该文件的增删行，收起后消失。
func TestInlineToolDiff_ExpandShowsLines(t *testing.T) {
	fake := NewFakeClient()
	fake.messages["t1"] = []Message{
		{ID: "u1", Role: "user", Text: "改一下加法", TurnIndex: 1, IsFinal: true},
		{ID: "e1", Role: "tool", Text: `Edit {"file_path":"/workspace/pkg/math.go"}` + "\nok", TurnIndex: 1},
	}
	fake.SetThreadDiff("t1", "", ThreadDiff{
		Diff:  toolDiffTestUnified,
		Turns: []DiffTurn{{TurnID: "turn-1", Index: 1, HasChanges: true}},
	})
	fake.SetThreadDiff("t1", "turn-1", ThreadDiff{Diff: toolDiffTestUnified})

	tt := newToolDiffTester(t, fake, "改一下加法")
	if tt.HasText("return a - b") {
		t.Fatalf("默认应折叠内嵌 diff")
	}

	clickText(t, tt, "查看改动 math.go")
	waitFor(t, tt, func() bool {
		return tt.HasText("pkg/math.go") && tt.HasText("return a - b") && tt.HasText("return a + b")
	})
	if tt.HasText("OTHER-NEW-LINE") {
		t.Fatalf("不应显示本步骤未修改的文件: %q", tt.Texts())
	}

	clickText(t, tt, "收起改动")
	if tt.HasText("return a - b") {
		t.Fatalf("收起后不应再显示 diff 行")
	}
}

// TestInlineToolDiff_EmbeddedDiff 验证工具文本自带 unified diff 时直接解析显示。
func TestInlineToolDiff_EmbeddedDiff(t *testing.T) {
	fake := NewFakeClient()
	fake.messages["t1"] = []Message{
		{ID: "u1", Role: "user", Text: "打补丁", TurnIndex: 1, IsFinal: true},
		{ID: "p1", Role: "tool", Text: "apply_patch {}\ndiff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-EMBED-OLD\n+EMBED-NEW\n", TurnIndex: 1},
	}
	tt := newToolDiffTester(t, fake, "打补丁")

	clickText(t, tt, "查看改动 x.go")
	waitFor(t, tt, func() bool {
		return tt.HasText("x.go") && tt.HasText("EMBED-OLD") && tt.HasText("EMBED-NEW")
	})
}

// TestInlineToolDiff_NonEditToolHasNoToggle 验证非文件修改工具不提供内嵌 diff 开关。
func TestInlineToolDiff_NonEditToolHasNoToggle(t *testing.T) {
	fake := NewFakeClient()
	fake.messages["t1"] = []Message{
		{ID: "u1", Role: "user", Text: "看看目录", TurnIndex: 1, IsFinal: true},
		{ID: "b1", Role: "tool", Text: `bash {"cmd":"ls"}`, TurnIndex: 1},
	}
	fake.SetThreadDiff("t1", "", ThreadDiff{
		Diff:  toolDiffTestUnified,
		Turns: []DiffTurn{{TurnID: "turn-1", Index: 1, HasChanges: true}},
	})
	tt := newToolDiffTester(t, fake, "看看目录")

	if tt.HasText("查看改动") {
		t.Fatalf("非文件修改工具不应显示内嵌 diff 开关")
	}
	clickText(t, tt, "$ ls")
	if tt.HasText("查看改动") || tt.HasText("return a - b") {
		t.Fatalf("展开非文件修改工具后不应出现内嵌 diff: %q", tt.Texts())
	}
}

// TestInlineToolDiff_InToolGroup 验证工具组展开后其中的文件修改工具也能展开内嵌 diff。
func TestInlineToolDiff_InToolGroup(t *testing.T) {
	fake := NewFakeClient()
	fake.messages["t1"] = []Message{
		{ID: "u1", Role: "user", Text: "先看再改", TurnIndex: 1, IsFinal: true},
		{ID: "b1", Role: "tool", Text: `bash {"cmd":"ls"}`, TurnIndex: 1},
		{ID: "e1", Role: "tool", Text: `Edit {"file_path":"pkg/other.go"}`, TurnIndex: 1},
	}
	fake.SetThreadDiff("t1", "", ThreadDiff{
		Turns: []DiffTurn{{TurnID: "turn-1", Index: 1, HasChanges: true}},
	})
	fake.SetThreadDiff("t1", "turn-1", ThreadDiff{Diff: toolDiffTestUnified})
	tt := newToolDiffTester(t, fake, "命令 · 编辑")

	clickText(t, tt, "命令 · 编辑")
	clickText(t, tt, "查看改动 other.go")
	waitFor(t, tt, func() bool {
		return tt.HasText("OTHER-OLD-LINE") && tt.HasText("OTHER-NEW-LINE")
	})
	if tt.HasText("return a - b") {
		t.Fatalf("不应显示本步骤未修改的文件")
	}
}

// diffErrClient 在 FakeClient 基础上让 Diff 总是返回错误。
type diffErrClient struct {
	*FakeClient
	err error
}

func (d *diffErrClient) Diff(ctx context.Context, threadID, turnID string) (ThreadDiff, error) {
	return ThreadDiff{}, d.err
}

// TestInlineToolDiff_LoadError 验证 Diff 返回错误时显示失败状态。
func TestInlineToolDiff_LoadError(t *testing.T) {
	fake := NewFakeClient()
	fake.messages["t1"] = []Message{
		{ID: "u1", Role: "user", Text: "改文件", TurnIndex: 1, IsFinal: true},
		{ID: "e1", Role: "tool", Text: `Edit {"file_path":"pkg/math.go"}`, TurnIndex: 1},
	}
	client := &diffErrClient{FakeClient: fake, err: errors.New("DIFF-BOOM")}
	tt := newToolDiffTester(t, client, "改文件")

	clickText(t, tt, "查看改动 math.go")
	waitFor(t, tt, func() bool {
		return tt.HasText("读取改动失败") && tt.HasText("DIFF-BOOM")
	})
}

func TestInlineToolDiffUsesMessageTurnID(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "u51", Role: "user", Text: "第 51 轮", TurnIndex: 50},
		{ID: "e51", Role: "tool", Text: `Edit {"path":"pkg/math.go"}`, TurnIndex: 50, TurnID: "turn-51"},
	})
	// 整库查询没有第 51 轮的摘要，直接用消息已有的 TurnID 仍可读取。
	fake.SetThreadDiff("t1", "", ThreadDiff{Turns: []DiffTurn{{TurnID: "turn-1", Index: 0}}})
	fake.SetThreadDiff("t1", "turn-51", ThreadDiff{Diff: toolDiffTestUnified})
	tt := newToolDiffTester(t, fake, "第 51 轮")
	clickText(t, tt, "查看改动 math.go")
	waitFor(t, tt, func() bool { return tt.HasText("return a + b") })
}

func TestInlineToolDiffRefreshesAfterToolUpdate(t *testing.T) {
	fake := NewFakeClient()
	m := Message{ID: "e1", Role: "tool", Text: `Edit {"path":"pkg/math.go"}`, TurnIndex: 1, TurnID: "turn-1"}
	fake.SetMessages("t1", []Message{
		{ID: "u1", Role: "user", Text: "等待修改", TurnIndex: 1}, m,
	})
	fake.SetThreadDiff("t1", "turn-1", ThreadDiff{})
	s, view := buildRoot(legacyDiffClient{fake})
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("等待修改") })
	clickText(t, tt, "查看改动 math.go")
	waitFor(t, tt, func() bool { return tt.HasText("这一步没有可显示的改动") })
	fake.SetThreadDiff("t1", "turn-1", ThreadDiff{Diff: toolDiffTestUnified})
	s.onWatchUpdate(ThreadUpdate{
		ThreadID: "t1", Thread: Thread{ID: "t1", ProjectID: "p1"},
		Messages: []Message{m},
	})
	waitFor(t, tt, func() bool { return tt.HasText("return a + b") })
}

func TestToolDiffDiscardsInvalidatedLoad(t *testing.T) {
	fake := NewFakeClient()
	fake.SetThreadDiff("t1", "turn-1", ThreadDiff{Diff: toolDiffTestUnified})
	s := newChatState(fake)
	s.toolDiff.generation = 1
	s.loadToolDiff("t1", 1, "turn-1", 0)
	if len(s.toolDiff.entries) != 0 {
		t.Fatal("已失效的加载结果不应重新写入缓存")
	}
}

// inlineToolDiffMsg 造一条自带 unified diff 的工具消息。
func inlineToolDiffMsg(id, patch string) Message {
	return Message{ID: id, Role: "tool", Text: "apply_patch {}\n" + patch}
}

// inlineToolDiffTinyPatch 造一个单文件两行补丁，body 用于区分不同补丁内容。
func inlineToolDiffTinyPatch(body string) string {
	return "diff --git a/pkg/tiny.go b/pkg/tiny.go\n" +
		"--- a/pkg/tiny.go\n" +
		"+++ b/pkg/tiny.go\n" +
		"@@ -1 +1 @@\n" +
		"-OLD-" + body + "\n" +
		"+NEW-" + body + "\n"
}

// inlineToolDiffLoadForTest 走一遍内嵌 diff 的缓存取数路径，返回是否拿到可渲染数据。
func inlineToolDiffLoadForTest(s *chatState, m Message) bool {
	raw := toolEmbeddedDiff(m.Text)
	_, ok := s.toolDiffDataFor(m, "embedded", func() []git.FileDiff {
		files, err := toolDiffParse([]byte(raw))
		if err != nil {
			return nil
		}
		return files
	})
	return ok
}

// TestInlineToolDiffParseOnce 验证同一补丁只在首帧解析一次，静止帧命中缓存。
func TestInlineToolDiffParseOnce(t *testing.T) {
	s := newChatState(NewFakeClient())
	msg := inlineToolDiffMsg("parse-once", inlineToolDiffTinyPatch("AAA"))

	orig := toolDiffParse
	var parses int
	toolDiffParse = func(data []byte) ([]git.FileDiff, error) {
		parses++
		return orig(data)
	}
	t.Cleanup(func() { toolDiffParse = orig })

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() { renderInlineToolDiff(c, s, msg) })
	}, 900, 700)

	tt.Frame()
	if parses != 1 {
		t.Fatalf("首帧应解析一次，实际 %d 次", parses)
	}
	if !tt.HasText("OLD-AAA") || !tt.HasText("NEW-AAA") {
		t.Fatalf("首帧应显示补丁内容: %q", tt.Texts())
	}

	tt.Frame()
	tt.Frame()
	if parses != 1 {
		t.Fatalf("静止帧不应重复解析，实际解析 %d 次", parses)
	}
}

// TestInlineToolDiffReparsesWhenPatchChanges 验证补丁内容变化后重新解析并更新显示。
func TestInlineToolDiffReparsesWhenPatchChanges(t *testing.T) {
	s := newChatState(NewFakeClient())
	msg := inlineToolDiffMsg("patch-change", inlineToolDiffTinyPatch("AAA"))

	orig := toolDiffParse
	var parses int
	toolDiffParse = func(data []byte) ([]git.FileDiff, error) {
		parses++
		return orig(data)
	}
	t.Cleanup(func() { toolDiffParse = orig })

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() { renderInlineToolDiff(c, s, msg) })
	}, 900, 700)
	tt.Frame()
	tt.Frame()
	if parses != 1 {
		t.Fatalf("静止帧不应重复解析，实际解析 %d 次", parses)
	}

	// 长度相同、只有内容不同，也必须重新解析（不能只按长度判定缓存有效）
	msg.Text = "apply_patch {}\n" + inlineToolDiffTinyPatch("BBB")
	tt.Frame()
	if parses != 2 {
		t.Fatalf("补丁内容变化后应重新解析，实际解析 %d 次", parses)
	}
	if !tt.HasText("OLD-BBB") || tt.HasText("OLD-AAA") {
		t.Fatalf("补丁内容变化后应显示新补丁: %q", tt.Texts())
	}
}

// inlineToolDiffFoldPatch 造一个尾部带 10 行连续上下文的补丁，用于折叠显示行缓存。
const inlineToolDiffFoldPatch = `diff --git a/pkg/fold.go b/pkg/fold.go
--- a/pkg/fold.go
+++ b/pkg/fold.go
@@ -1,11 +1,11 @@
-OLD-FOLD
+NEW-FOLD
 CTX-A0
 CTX-A1
 CTX-A2
 CTX-A3
 CTX-A4
 CTX-A5
 CTX-A6
 CTX-A7
 CTX-A8
 CTX-A9
`

// 摘要不再自行折叠；完整版本的上下文展开由共享 DiffView 管理。
func TestInlineToolDiffPatchContext(t *testing.T) {
	s := newChatState(NewFakeClient())
	msg := inlineToolDiffMsg("fold-1", inlineToolDiffFoldPatch)

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() { renderInlineToolDiff(c, s, msg) })
	}, 900, 700)
	tt.Frame()
	if !tt.HasText("CTX-A2") || !tt.HasText("CTX-A9") || !tt.HasText("在改动中打开") {
		t.Fatalf("patch 摘要应保留真实上下文并提供全文入口: %q", tt.Texts())
	}
}

// TestInlineToolDiffCacheEvictsOldest 验证内嵌 diff 缓存超过上限后淘汰最旧条目。
func TestInlineToolDiffCacheEvictsOldest(t *testing.T) {
	s := newChatState(NewFakeClient())
	for i := 0; i < toolDiffInlineCacheLimit+1; i++ {
		msg := inlineToolDiffMsg(fmt.Sprintf("evict-%d", i), inlineToolDiffTinyPatch(fmt.Sprintf("B%02d", i)))
		if !inlineToolDiffLoadForTest(s, msg) {
			t.Fatalf("第 %d 条消息应能取到渲染数据", i)
		}
	}

	s.mu.Lock()
	cached := len(s.toolDiff.inline)
	_, oldestKept := s.toolDiff.inline["evict-0"]
	newestKept := s.toolDiff.inline["evict-"+fmt.Sprintf("%d", toolDiffInlineCacheLimit)] != nil
	s.mu.Unlock()

	if cached != toolDiffInlineCacheLimit {
		t.Fatalf("缓存条目数 = %d，期望 %d", cached, toolDiffInlineCacheLimit)
	}
	if oldestKept {
		t.Fatal("超出上限后应淘汰最旧的消息缓存")
	}
	if !newestKept {
		t.Fatal("最新写入的消息缓存应保留")
	}
}
