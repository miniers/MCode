package mcodeui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// copyMarkdownTestMessages 是复制接线测试用的最小会话内容。
func copyMarkdownTestMessages() []Message {
	return []Message{
		{ID: "m1", Role: "user", Kind: "text", Text: "复制这一条", TurnIndex: 1, IsFinal: true},
		{ID: "m2", Role: "assistant", Kind: "text", Text: "收到", TurnIndex: 1, IsFinal: true},
	}
}

// stripExportTimeLine 去掉导出头里的「导出时间」行：它取当前时刻，逐字比对时需要屏蔽。
func stripExportTimeLine(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if strings.HasPrefix(ln, "- 导出时间：") {
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// copyMarkdownTester 起一个带内容的根视图，返回 tester 与期望的预算版导出文本。
func copyMarkdownTester(t *testing.T) (*ui.Tester, *chatState, string) {
	t.Helper()
	fake := NewFakeClient()
	fake.messages["t1"] = copyMarkdownTestMessages()
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1200, 800)
	// 等到会话标题与正文都渲染出来，保证 state.messages 已装载，
	// 否则期望值会比点击时实际复制的正文少几条。
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") && tt.HasText("复制这一条") })

	state.mu.Lock()
	thread := Thread{ID: "t1"}
	for i := range state.threads {
		if state.threads[i].ID == "t1" {
			thread = state.threads[i]
			break
		}
	}
	msgs := append([]Message(nil), state.messages...)
	state.mu.Unlock()

	want, _ := buildThreadExportMarkdownBudgeted(thread, msgs, time.Now(), threadExportBudgetChars)
	return tt, state, stripExportTimeLine(want)
}

// TestThreadContextMenu_CopyMarkdown 覆盖跨组接线 X2(a)：会话右键菜单的「复制为 Markdown」
// 必须把预算版导出文本写进剪贴板。
func TestThreadContextMenu_CopyMarkdown(t *testing.T) {
	tt, _, want := copyMarkdownTester(t)
	if err := tt.RightClick("测试会话1"); err != nil {
		t.Fatalf("右键会话失败: %v", err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("复制为 Markdown"); err != nil {
		t.Fatalf("菜单缺少「复制为 Markdown」: %v", err)
	}
	tt.Frame()

	got := tt.Clipboard()
	if got == "" {
		t.Fatalf("剪贴板为空，菜单项没有落地复制")
	}
	if stripExportTimeLine(got) != want {
		t.Fatalf("剪贴板内容与预算版导出不一致:\n got: %q\nwant: %q", got, want)
	}
}

// TestComposerSlashCommand_CopyMarkdown 覆盖跨组接线 X2(b)：/copy 命令复制当前会话的
// 预算版 Markdown，并在下一帧由渲染期写进剪贴板。
func TestComposerSlashCommand_CopyMarkdown(t *testing.T) {
	// 清掉可能残留的待执行请求，避免测试之间串味。
	if _, ok := consumeCopyThreadMarkdown(); ok {
		t.Fatalf("测试开始前不应有未执行的复制请求")
	}
	t.Cleanup(func() { consumeCopyThreadMarkdown() })

	tt, state, want := copyMarkdownTester(t)
	state.mu.Lock()
	state.draft = "/copy"
	state.mu.Unlock()
	if !state.runLocalSlashCommand("t1", "/copy") {
		t.Fatalf("/copy 应被本地拦截，不该作为文本发给后端")
	}
	state.mu.Lock()
	draft := state.draft
	state.mu.Unlock()
	if draft != "" {
		t.Fatalf("/copy 后应清空草稿，got %q", draft)
	}

	tt.Frame()
	got := tt.Clipboard()
	if got == "" {
		t.Fatalf("剪贴板为空，/copy 没有落地复制")
	}
	if stripExportTimeLine(got) != want {
		t.Fatalf("剪贴板内容与预算版导出不一致:\n got: %q\nwant: %q", got, want)
	}
}
