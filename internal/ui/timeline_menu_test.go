package mcodeui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSelectedQuoteText(t *testing.T) {
	parts := []ui.SelectedText{
		{Text: "  第一段内容  ", Start: 2, End: 5},
		{Text: "\n\n", Start: 0, End: 2},
		{Text: "中文片段", Start: 1, End: 3},
	}
	if got, want := selectedQuoteText(parts), "第一段\n\n文片"; got != want {
		t.Fatalf("selectedQuoteText = %q, want %q", got, want)
	}
	if got := selectedQuoteText([]ui.SelectedText{{Text: "空", Start: 1, End: 1}}); got != "" {
		t.Fatalf("空选区 = %q, want empty", got)
	}
}

func TestMarkdownToPlain(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"标题", "# 标题\n## 二级", "标题\n二级"},
		{"引用", "> 引用内容", "引用内容"},
		{"提及与议题引用", "请 @octocat 看一下 #123 和 owner/repo#45", "请 @octocat 看一下 #123 和 owner/repo#45"},
		{"提示块", "> [!NOTE]\n> 注意事项", "注意事项"},
		{"列表", "- 一\n* 二\n+ 三\n1. 四\n   - 嵌套", "一\n二\n三\n四\n嵌套"},
		{"粗体与行内代码", "**粗体** 和 __下划__ 以及 `code`", "粗体 和 下划 以及 code"},
		{"链接", "见 [文档](https://example.com) 和 ![图](a.png)", "见 文档 和 图"},
		{"代码围栏", "前\n```go\nfmt.Println(\"# 不处理\")\n```\n后", "前\nfmt.Println(\"# 不处理\")\n后"},
		{"斜体删除线与转义", `*斜体* ~~删除~~ \*原样\*`, "斜体 删除 *原样*"},
		{"代码与标识符", "`a__b` 和 a__b", "a__b 和 a__b"},
		{"嵌套围栏", "````md\n```go\ncode\n```\n````", "```go\ncode\n```"},
		{"引用链接", "[文档][ref]\n\n[ref]: https://example.com", "文档"},
		{"表格", "| A | B |\n| - | - |\n| x | y |", "A\tB\nx\ty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := markdownToPlain(tc.in); got != tc.want {
				t.Fatalf("markdownToPlain(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestLastCodeBlock 验证从 Markdown 提取最后一个围栏代码块的规则。
func TestLastCodeBlock(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"无代码块", "正文没有任何围栏", "", false},
		{"只有行内代码", "使用 `go test` 与 ``a`b`` 即可", "", false},
		{"单个代码块", "说明\n```\nfmt.Println(\"hi\")\n```", "fmt.Println(\"hi\")", true},
		{"多个代码块取最后一个", "```\nfirst\n```\n中间说明\n```\nsecond\n```", "second", true},
		{"带语言标记", "```go\nfmt.Println(\"hi\")\n```", "fmt.Println(\"hi\")", true},
		{"四反引号嵌套三反引号", "````md\n```go\ncode\n```\n````", "```go\ncode\n```", true},
		{"波浪线围栏", "~~~sh\necho hi\n~~~", "echo hi", true},
		{"未闭合块取到文末", "说明\n```\n第一行\n第二行", "第一行\n第二行", true},
		{"闭合围栏更长也算闭合", "```\ncode\n````", "code", true},
		{"闭合围栏字符不一致则不闭合", "```\ncode\n~~~", "code\n~~~", true},
		{"去掉末尾换行", "```\ncode\n\n```", "code", true},
		{"列表项缩进的围栏", "1. 步骤\n   ```sh\n   echo hi\n   ```\n", "   echo hi", true},
		{"同一行的三反引号是行内代码", "用 ```x``` 包起来", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := lastCodeBlock(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("lastCodeBlock(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestQuoteForReply 验证引用块生成：每行前加「> 」，去掉尾部换行，空白内容返回空。
func TestQuoteForReply(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"单行", "结论", "> 结论"},
		{"多行", "第一行\n第二行", "> 第一行\n> 第二行"},
		{"空行保留引用前缀", "第一行\n\n第二行", "> 第一行\n> \n> 第二行"},
		{"去掉尾部换行", "结论\n\n", "> 结论"},
		{"空白内容", "   \n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quoteForReply(tc.in); got != tc.want {
				t.Fatalf("quoteForReply(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestParentReportText 验证回报父会话的文本格式。
func TestParentReportText(t *testing.T) {
	if got := parentReportText("修复登录", "结论正文"); got != "[来自子会话 修复登录] 结论正文" {
		t.Fatalf("parentReportText = %q", got)
	}
	if got := parentReportText("  ", "结论"); got != "[来自子会话 未命名会话] 结论" {
		t.Fatalf("空标题应回退到未命名会话，got %q", got)
	}
}

// TestQuoteIntoDraft 验证右键「引用到回复」把纯文本引用块追加到草稿末尾。
func TestQuoteIntoDraft(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "a1", Role: "assistant", Text: "**重点**结论", IsFinal: true},
	})
	s, tt := newMenuTestState(t, fake, 1)
	s.draft = "原有草稿"

	if err := tt.RightClick("重点结论"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("引用到回复"); err != nil {
		t.Fatal(err)
	}

	want := "原有草稿\n\n> 重点结论"
	if got := draftOf(s); got != want {
		t.Fatalf("draft = %q, want %q", got, want)
	}
}

// TestQuoteIntoEmptyDraft 验证空草稿时引用块直接填入。
func TestQuoteIntoEmptyDraft(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{{ID: "a1", Role: "assistant", Text: "结论", IsFinal: true}})
	s, _ := newMenuTestState(t, fake, 1)

	s.quoteIntoDraft("结论")

	if got := draftOf(s); got != "> 结论" {
		t.Fatalf("draft = %q, want %q", got, "> 结论")
	}
}

func TestMessageContextMenusOnSelectableContent(t *testing.T) {
	for _, role := range []string{"user", "assistant"} {
		t.Run(role, func(t *testing.T) {
			fake := NewFakeClient()
			fake.SetMessages("t1", []Message{
				{ID: "m1", Role: role, Text: "**菜单正文**", IsFinal: true},
			})
			_, tt := newMenuTestState(t, fake, 1)
			for _, copyItem := range []string{"复制", "复制 Markdown"} {
				target := "菜单正文"
				if role == "user" {
					target = "**菜单正文**"
				}
				if err := tt.RightClick(target); err != nil {
					t.Fatal(err)
				}
				action := "分支"
				if role == "user" {
					action = "编辑"
				}
				if !slices.Contains(tt.Menu(), action) {
					t.Fatalf("正文右键缺少 %s: %v", action, tt.Menu())
				}
				if !slices.Contains(tt.Menu(), "引用到回复") {
					t.Fatalf("正文右键缺少 引用到回复: %v", tt.Menu())
				}
				if err := tt.ChooseMenuItem(copyItem); err != nil {
					t.Fatal(err)
				}
				want := "菜单正文"
				if copyItem == "复制 Markdown" {
					want = "**菜单正文**"
				}
				if got := tt.Clipboard(); got != want {
					t.Fatalf("clipboard = %q, want %q", got, want)
				}
			}
		})
	}
}

// TestInlineEditMenuOpensEditor 验证右键「编辑」进入就地编辑：原位出现预填原文的输入框。
func TestInlineEditMenuOpensEditor(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "a1", Role: "assistant", Text: "上一答", IsFinal: true},
		{ID: "u2", Role: "user", Text: "旧问题", TurnIndex: 1},
	})
	s, tt := newMenuTestState(t, fake, 2)

	if err := tt.RightClick("旧问题"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("编辑"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, tt, func() bool { st, ok := inlineEditOf(s); return ok && st.msgID == "u2" })
	st, _ := inlineEditOf(s)
	if st.draft != "旧问题" {
		t.Fatalf("就地编辑应预填原文，got %q", st.draft)
	}
	for _, want := range []string{"取消编辑", "发送编辑"} {
		if !tt.HasText(want) {
			t.Fatalf("就地编辑缺少 %q: %q", want, tt.Texts())
		}
	}
}

// TestInlineEditCancelKeepsMessage 验证取消就地编辑后恢复原消息气泡。
func TestInlineEditCancelKeepsMessage(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{{ID: "u1", Role: "user", Text: "第一问", TurnIndex: 1}})
	s, tt := newMenuTestState(t, fake, 1)

	startInlineEdit(s, Message{ID: "u1", Role: "user", Text: "第一问"})
	waitFor(t, tt, func() bool { _, ok := inlineEditOf(s); return ok })
	cancelInlineEdit(s)
	waitFor(t, tt, func() bool { _, ok := inlineEditOf(s); return !ok })
	if !tt.HasText("第一问") {
		t.Fatalf("取消编辑后应恢复原消息: %q", tt.Texts())
	}
}

// TestInlineEditSendsEditedTextToNewThread 验证就地编辑提交后从上一轮最终答复分支，并把编辑后的文本直接发送到新会话。
func TestInlineEditSendsEditedTextToNewThread(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "u1", Role: "user", Text: "第一问", TurnIndex: 1, IsFinal: true},
		{ID: "a1", Role: "assistant", Text: "第一答", TurnIndex: 1, IsFinal: true},
		{ID: "u2", Role: "user", Text: "第二问", TurnIndex: 2, IsFinal: true},
		{ID: "a2", Role: "assistant", Text: "第二答", TurnIndex: 2, IsFinal: true},
	})
	fake.SetBranchResult("t-branch", nil)
	s, tt := newMenuTestState(t, fake, 4)

	if err := tt.RightClick("第二问"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("编辑"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { st, ok := inlineEditOf(s); return ok && st.msgID == "u2" })

	st, _ := inlineEditOf(s)
	st.draft = "第二问（已改）"
	tt.Frame()
	clickText(t, tt, "发送编辑")

	waitFor(t, tt, func() bool { return len(sendCallsOf(fake)) == 1 })
	if got := selectedThreadOf(s); got != "t-branch" {
		t.Fatalf("selectedThreadID = %q, want t-branch", got)
	}
	if calls := branchCallsOf(fake); len(calls) != 1 || calls[0] != [2]string{"t1", "a1"} {
		t.Fatalf("BranchCalls = %v, want [[t1 a1]]", calls)
	}
	if calls := sendCallsOf(fake); calls[0].ThreadID != "t-branch" || calls[0].Text != "第二问（已改）" {
		t.Fatalf("SendCalls = %+v, want 发送编辑后的文本到 t-branch", calls)
	}
	if _, ok := inlineEditOf(s); ok {
		t.Fatal("提交成功后应退出就地编辑")
	}
}

// TestInlineEditWithoutPreviousFinalCreatesThread 验证首条用户消息就地编辑时新建会话并发送编辑后的文本。
func TestInlineEditWithoutPreviousFinalCreatesThread(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "u1", Role: "user", Text: "第一问", TurnIndex: 1, IsFinal: true},
		{ID: "a1", Role: "assistant", Text: "第一答", TurnIndex: 1, IsFinal: true},
	})
	s, tt := newMenuTestState(t, fake, 2)

	startInlineEdit(s, Message{ID: "u1", Role: "user", Text: "第一问"})
	waitFor(t, tt, func() bool { _, ok := inlineEditOf(s); return ok })
	s.submitInlineEdit("u1", "第一问（已改）")

	waitFor(t, tt, func() bool { return len(sendCallsOf(fake)) == 1 })
	if n := fake.CreateThreadCalls(); n != 1 {
		t.Fatalf("CreateThread 调用 %d 次，期望 1", n)
	}
	if calls := branchCallsOf(fake); len(calls) != 0 {
		t.Fatalf("BranchCalls = %v, want 空", calls)
	}
	calls := sendCallsOf(fake)
	if calls[0].ThreadID == "t1" || calls[0].Text != "第一问（已改）" {
		t.Fatalf("SendCalls = %+v, want 新会话收到编辑后的文本", calls)
	}
	if got := selectedThreadOf(s); got == "t1" || got == "" {
		t.Fatalf("selectedThreadID = %q, want 新会话", got)
	}
}

// TestInlineEditBranchFailureKeepsEditingState 验证分支失败时保留就地编辑状态、选中会话与草稿，且不发送消息。
func TestInlineEditBranchFailureKeepsEditingState(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "a1", Role: "assistant", Text: "上一答", IsFinal: true},
		{ID: "u2", Role: "user", Text: "旧问题", TurnIndex: 1},
	})
	fake.SetBranchResult("", errors.New("无法分支"))
	s, tt := newMenuTestState(t, fake, 2)

	startInlineEdit(s, Message{ID: "u2", Role: "user", Text: "旧问题"})
	waitFor(t, tt, func() bool { _, ok := inlineEditOf(s); return ok })
	st, _ := inlineEditOf(s)
	st.draft = "改过的提问"
	s.submitInlineEdit(st.msgID, st.draft)

	waitFor(t, tt, func() bool { return tt.HasText("分支失败：无法分支") })
	if selectedThreadOf(s) != "t1" {
		t.Fatal("分支失败改变了选中会话")
	}
	if cur, ok := inlineEditOf(s); !ok || cur.draft != "改过的提问" {
		t.Fatalf("分支失败应保留就地编辑状态与草稿，got %+v ok=%v", cur, ok)
	}
	if n := fake.SendCalls(); n != 0 {
		t.Fatalf("分支失败不应发送消息，Send 调用 %d 次", n)
	}
}

type failedMessageCreateClient struct{ *FakeClient }

func (f *failedMessageCreateClient) CreateThread(context.Context, string, string) (string, error) {
	return "", errors.New("新建失败")
}

// TestInlineEditCreateFailureKeepsEditingState 验证首条消息编辑时新建会话失败后仍可继续编辑。
func TestInlineEditCreateFailureKeepsEditingState(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{{ID: "u1", Role: "user", Text: "第一问"}})
	s, root := buildRoot(&failedMessageCreateClient{fake})
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.selectedThreadID == "t1" && len(s.messages) == 1
	})

	startInlineEdit(s, Message{ID: "u1", Role: "user", Text: "第一问"})
	waitFor(t, tt, func() bool { _, ok := inlineEditOf(s); return ok })
	s.submitInlineEdit("u1", "第一问（已改）")

	waitFor(t, tt, func() bool { return tt.HasText("分支失败：新建失败") })
	if selectedThreadOf(s) != "t1" {
		t.Fatal("新建失败改变了选中会话")
	}
	if _, ok := inlineEditOf(s); !ok {
		t.Fatal("新建失败应保留就地编辑状态")
	}
}

type delayedMessageBranchClient struct {
	*FakeClient
	started chan struct{}
	release chan struct{}
}

func (f *delayedMessageBranchClient) BranchThread(ctx context.Context, tid, mid string) (string, error) {
	f.started <- struct{}{}
	<-f.release
	return f.FakeClient.BranchThread(ctx, tid, mid)
}

// TestInlineEditDoesNotOverrideLaterUserActions 验证后台分支完成前用户取消编辑或切换会话时，界面不被抢占。
func TestInlineEditDoesNotOverrideLaterUserActions(t *testing.T) {
	for _, action := range []string{"切换会话", "取消编辑"} {
		t.Run(action, func(t *testing.T) {
			fake := NewFakeClient()
			fake.SetMessages("t1", []Message{
				{ID: "a1", Role: "assistant", Text: "上一答", IsFinal: true},
				{ID: "u2", Role: "user", Text: "旧问题", TurnIndex: 1},
			})
			client := &delayedMessageBranchClient{
				FakeClient: fake, started: make(chan struct{}, 1), release: make(chan struct{}),
			}
			s, root := buildRoot(client)
			tt := ui.NewTester(root, 1000, 700)
			waitFor(t, tt, func() bool {
				s.mu.Lock()
				defer s.mu.Unlock()
				return s.selectedThreadID == "t1" && len(s.messages) == 2
			})

			startInlineEdit(s, Message{ID: "u2", Role: "user", Text: "旧问题"})
			waitFor(t, tt, func() bool { _, ok := inlineEditOf(s); return ok })
			s.submitInlineEdit("u2", "编辑后的提问")
			waitFor(t, tt, func() bool { return len(client.started) > 0 })

			wantThread := "t1"
			if action == "切换会话" {
				s.selectThread("t2")
				wantThread = "t2"
			} else {
				cancelInlineEdit(s)
			}
			close(client.release)
			waitFor(t, tt, func() bool { return tt.HasText("已创建新会话，请从侧边栏打开") })
			if selectedThreadOf(s) != wantThread {
				t.Fatalf("后台分支覆盖了用户后续操作: selectedThreadID = %q, want %q", selectedThreadOf(s), wantThread)
			}
		})
	}
}

func TestAssistantCodeContextMenu(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "a1", Role: "assistant", Text: "```\nCODE-MENU-CONTENT\n```", IsFinal: true},
	})
	_, tt := newMenuTestState(t, fake, 1)
	if err := tt.RightClick("CODE-MENU-CONTENT"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tt.Menu(), "分支") || !slices.Contains(tt.Menu(), "复制 Markdown") {
		t.Fatalf("代码块右键没有消息菜单: %v", tt.Menu())
	}
}

// TestCodeBlockCopyButton 验证代码块容器带语言标题栏与复制按钮，点击后写入剪贴板。
func TestCodeBlockCopyButton(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "a1", Role: "assistant", Text: "```go\nfmt.Println(\"hi\")\n```", IsFinal: true},
	})
	_, tt := newMenuTestState(t, fake, 1)

	if !tt.HasText("go") {
		t.Fatalf("代码块应显示语言名: %q", tt.Texts())
	}
	if !tt.HasText("复制代码") {
		t.Fatalf("代码块应显示复制按钮: %q", tt.Texts())
	}
	if err := tt.Click("复制代码"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Clipboard(); !strings.Contains(got, `fmt.Println("hi")`) {
		t.Fatalf("剪贴板内容不符合预期: %q", got)
	}
}

// TestMessageMoreButtonOpensContextMenu 验证消息操作栏末尾的「⋯」按钮弹出的是同一个消息菜单：
// 条目与右键菜单逐一相同，选中「复制」的效果也与右键一致。
func TestMessageMoreButtonOpensContextMenu(t *testing.T) {
	msgs := []Message{{ID: "a1", Role: "assistant", Text: "结论是……", IsFinal: true}}
	fake := NewFakeClient()
	fake.SetMessages("t1", msgs)
	s, app := newMenuTestState(t, fake, 1)

	if err := app.RightClick("结论是……"); err != nil {
		t.Fatal(err)
	}
	want := app.Menu()
	app.CloseMenu()
	if len(want) == 0 {
		t.Fatal("右键没有弹出消息菜单")
	}

	// 单独渲染⋯按钮：完整界面里侧边栏底部也有一个「更多」，定位会歧义
	m := msgs[0]
	bar := ui.NewTester(func(c *ui.Context) {
		ui.Row(c).Children(func() { renderMessageMoreButton(c, s, m) })
	}, 400, 120)
	if !bar.HasText("更多") {
		t.Fatalf("⋯按钮没有可读标签: %q", bar.Texts())
	}
	r, ok := bar.Find("更多")
	if !ok {
		t.Fatalf("找不到⋯按钮: %q", bar.Texts())
	}
	bar.Press(r.X+r.W/2, r.Y+r.H/2)
	if got := bar.Menu(); !slices.Equal(got, want) {
		t.Fatalf("⋯菜单 = %q, want %q", got, want)
	}
	bar.Release(r.X+r.W/2, r.Y+r.H/2)
	if err := bar.ChooseMenuItem("复制"); err != nil {
		t.Fatalf("⋯菜单里选「复制」失败: %v", err)
	}
	if got := bar.Clipboard(); !strings.Contains(got, "结论是……") {
		t.Fatalf("⋯菜单的复制没有写入剪贴板: %q", got)
	}
}

// newMenuTestState 构建根视图并等待初始会话 t1 选中及历史加载完成。
func newMenuTestState(t *testing.T, fake *FakeClient, wantMessages int) (*chatState, *ui.Tester) {
	t.Helper()
	s, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.selectedThreadID == "t1" && len(s.messages) == wantMessages
	})
	return s, tt
}

func selectedThreadOf(s *chatState) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectedThreadID
}

func draftOf(s *chatState) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.draft
}

func branchCallsOf(f *FakeClient) [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]string(nil), f.BranchCalls...)
}

func sendCallsOf(f *FakeClient) []sentCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentCall(nil), f.sendCalls...)
}

func TestBranchFromMessageSelectsNewThread(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "u1", Role: "user", Text: "问题", TurnIndex: 1, IsFinal: true},
		{ID: "a1", Role: "assistant", Text: "答复", TurnIndex: 1, IsFinal: true},
	})
	fake.SetBranchResult("t-branch", nil)
	s, tt := newMenuTestState(t, fake, 2)

	if err := tt.RightClick("答复"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("分支"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, tt, func() bool { return selectedThreadOf(s) == "t-branch" })
	calls := branchCallsOf(fake)
	if len(calls) != 1 || calls[0] != [2]string{"t1", "a1"} {
		t.Fatalf("BranchCalls = %v, want [[t1 a1]]", calls)
	}
}

// TestBranchButtonConfirmFlow 验证助手操作栏的分支按钮先弹确认浮层，确认后才分支。
func TestBranchButtonConfirmFlow(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "u1", Role: "user", Text: "问题", TurnIndex: 1, IsFinal: true},
		{ID: "a1", Role: "assistant", Text: "答复", TurnIndex: 1, IsFinal: true},
	})
	fake.SetBranchResult("t-branch", nil)
	s, tt := newMenuTestState(t, fake, 2)

	clickText(t, tt, "分支")
	if !tt.HasText("从这条回复分支？") || !tt.HasText("新会话将包含到这条回复为止的对话。") {
		t.Fatalf("分支确认浮层文案缺失: %q", tt.Texts())
	}
	if len(branchCallsOf(fake)) != 0 {
		t.Fatal("确认前不应分支")
	}

	clickText(t, tt, "确认分支")
	waitFor(t, tt, func() bool { return selectedThreadOf(s) == "t-branch" })
	calls := branchCallsOf(fake)
	if len(calls) != 1 || calls[0] != [2]string{"t1", "a1"} {
		t.Fatalf("BranchCalls = %v, want [[t1 a1]]", calls)
	}
}

// TestSendToParentOnlyInChildThread 验证普通会话不显示回报父会话的按钮与菜单项。
func TestSendToParentOnlyInChildThread(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "u1", Role: "user", Text: "问题", TurnIndex: 1, IsFinal: true},
		{ID: "a1", Role: "assistant", Text: "答复", TurnIndex: 1, IsFinal: true},
	})
	_, tt := newMenuTestState(t, fake, 2)

	if tt.HasText("回报父会话") {
		t.Fatalf("普通会话不应显示回报父会话按钮: %q", tt.Texts())
	}
	if err := tt.RightClick("答复"); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(tt.Menu(), "发送回父会话") {
		t.Fatalf("普通会话不应有发送回父会话菜单项: %v", tt.Menu())
	}
}

// TestSendToParentThreadFromChild 验证子会话中回报父会话按钮把回复全文发送到 Lead 会话。
func TestSendToParentThreadFromChild(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "u1", Role: "user", Text: "子任务", TurnIndex: 1, IsFinal: true},
		{ID: "a1", Role: "assistant", Text: "子会话结论", TurnIndex: 1, IsFinal: true},
	})
	fake.SetSwarmState("t1", SwarmState{LeadThreadID: "lead-1", Available: true, Enabled: true, IsBee: true})
	s, tt := newMenuTestState(t, fake, 2)

	waitFor(t, tt, func() bool { return tt.HasText("回报父会话") })
	clickText(t, tt, "回报父会话")

	waitFor(t, tt, func() bool { return len(sendCallsOf(fake)) == 1 })
	calls := sendCallsOf(fake)
	if calls[0].ThreadID != "lead-1" {
		t.Fatalf("SendCalls = %+v, want 发到 lead-1", calls)
	}
	if !strings.Contains(calls[0].Text, "[来自子会话 ") || !strings.Contains(calls[0].Text, "子会话结论") {
		t.Fatalf("回报文本格式不符: %q", calls[0].Text)
	}
	if parentThreadID(s) != "lead-1" {
		t.Fatalf("parentThreadID = %q, want lead-1", parentThreadID(s))
	}
}

// TestSendToParentFromContextMenu 验证子会话右键菜单的「发送回父会话」。
func TestSendToParentFromContextMenu(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "u1", Role: "user", Text: "子任务", TurnIndex: 1, IsFinal: true},
		{ID: "a1", Role: "assistant", Text: "子会话结论", TurnIndex: 1, IsFinal: true},
	})
	fake.SetSwarmState("t1", SwarmState{LeadThreadID: "lead-1", Available: true, Enabled: true, IsBee: true})
	_, tt := newMenuTestState(t, fake, 2)

	waitFor(t, tt, func() bool { return tt.HasText("回报父会话") })
	if err := tt.RightClick("子会话结论"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tt.Menu(), "发送回父会话") {
		t.Fatalf("子会话右键菜单缺少发送回父会话: %v", tt.Menu())
	}
	if err := tt.ChooseMenuItem("发送回父会话"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return len(sendCallsOf(fake)) == 1 })
	if got := sendCallsOf(fake)[0].ThreadID; got != "lead-1" {
		t.Fatalf("SendCalls thread = %q, want lead-1", got)
	}
}

func TestBranchFromMessageErrorShowsToast(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "a1", Role: "assistant", Text: "答复", TurnIndex: 1, IsFinal: true},
	})
	fake.SetBranchResult("", errors.New("会话正在运行"))
	s, tt := newMenuTestState(t, fake, 1)

	s.branchFromMessage(Message{ID: "a1", Role: "assistant", Text: "答复", IsFinal: true})

	waitFor(t, tt, func() bool { return tt.HasText("分支失败：会话正在运行") })
	if got := selectedThreadOf(s); got != "t1" {
		t.Fatalf("selectedThreadID = %q, want t1", got)
	}
}
