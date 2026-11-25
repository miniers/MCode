package mcodeui

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"
	"mcode/internal/git"
)

func TestPRReviewCardParseAndTitle(t *testing.T) {
	res, ok := parsePRReview(`[{"path":"internal/a.go","line":12,"body":"空指针风险"},{"path":"internal/b.go","line":0,"body":"缺少锁保护"}]`)
	if !ok || res.Verdict != "" || res.Reason != "" || len(res.Comments) != 2 {
		t.Fatalf("期望解析出 2 条问题，得到 ok=%v res=%+v", ok, res)
	}
	if res.Comments[0].Path != "internal/a.go" || res.Comments[0].Line != 12 || res.Comments[0].Headline != "空指针风险" || res.Comments[0].Severity != "suggestion" {
		t.Fatalf("第 1 条解析异常：%+v", res.Comments[0])
	}

	empty, ok := parsePRReview(`[]`)
	if !ok || empty.Verdict != "" || len(empty.Comments) != 0 {
		t.Fatalf("期望 [] 解析成功且为空，得到 ok=%v res=%+v", ok, empty)
	}

	for _, raw := range []string{"", "null", "{", "{}", `{"path":"a.go"}`} {
		if got, ok := parsePRReview(raw); ok {
			t.Fatalf("输入 %q 应解析失败，却得到 %+v", raw, got)
		}
	}

	if got := prReviewCountText(0, 0); got != "" {
		t.Fatalf("无问题计数 = %q，期望空串", got)
	}
	if got := prReviewCountText(1, 1); got != "1 阻塞 · 1 建议" {
		t.Fatalf("混合计数 = %q", got)
	}
	if got := prReviewLocationText("internal/a.go", 12); got != "internal/a.go:12" {
		t.Fatalf("带行号定位 = %q", got)
	}
	if got := prReviewLocationText("internal/b.go", 0); got != "internal/b.go" {
		t.Fatalf("无行号定位 = %q", got)
	}
	if got := prReviewLocationText("   ", 5); got != "" {
		t.Fatalf("空路径定位 = %q", got)
	}
}

func TestPRReviewCardVerdict(t *testing.T) {
	cases := []struct {
		verdict string
		label   string
	}{
		{verdict: "merge", label: "可直接合并"},
		{verdict: "fix", label: "修复后合并"},
		{verdict: "comment", label: "需作者修改"},
		{verdict: "close", label: "不建议合并"},
		{verdict: "other", label: ""},
	}
	for _, tc := range cases {
		if got := prReviewVerdictLabel(tc.verdict); got != tc.label {
			t.Fatalf("prReviewVerdictLabel(%q) = %q，期望 %q", tc.verdict, got, tc.label)
		}
	}

	rawObj := `{"verdict":"comment","summary":"这个 PR 修复了并发写问题，但设计上需要作者调整接口签名。","comments":[{"path":"internal/a.go","line":18,"body":"缺少上下文取消检查"},{"path":"internal/b.go","line":24,"body":"返回值未透传错误"}]}`
	res, ok := parsePRReview(rawObj)
	if !ok {
		t.Fatal("对象格式解析失败")
	}
	if res.Verdict != "comment" || res.Reason != "这个 PR 修复了并发写问题，但设计上需要作者调整接口签名。" || len(res.Comments) != 2 {
		t.Fatalf("对象格式解析字段不符：%+v", res)
	}

	unknownRes, ok := parsePRReview(`{"verdict":"shipit","summary":"未知结论测试","comments":[]}`)
	if !ok {
		t.Fatal("未知 verdict 对象应允许解析")
	}
	if unknownRes.Verdict != "" {
		t.Fatalf("未知 verdict 应按空处理，得到 %q", unknownRes.Verdict)
	}

	emptyCommentsRes, ok := parsePRReview(`{"verdict":"merge","summary":"已核对实现，可直接合入。","comments":[]}`)
	if !ok || emptyCommentsRes.Verdict != "merge" || len(emptyCommentsRes.Comments) != 0 {
		t.Fatalf("空 comments 对象解析异常：ok=%v res=%+v", ok, emptyCommentsRes)
	}

	resetRuntimeTheme(t)
	setAppReduceMotion(t, true)

	fake := NewFakeClient()
	fake.SetThreadWorkDir(t.TempDir())
	s := newChatState(fake)
	s.selectedThreadID = "thread-1"
	s.threads = []Thread{{ID: "thread-1"}}
	s.messages = []Message{{
		ID:        "assistant-verdict",
		Role:      "assistant",
		Kind:      "text",
		TurnIndex: 1,
		IsFinal:   true,
		Text:      "审查完成。\n\n```pr-review\n" + rawObj + "\n```\n",
	}}
	s.markRevealBaselineLocked(s.messages)

	tt := ui.NewTester(func(c *ui.Context) { renderChat(c, s) }, 1000, 700)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	tt.Frame()

	allText := strings.Join(tt.Texts(), "\n")
	for _, want := range []string{
		"需作者修改",
		"2 建议",
		"这个 PR 修复了并发写问题，但设计上需要作者调整接口签名。",
		"internal/a.go:18",
		"缺少上下文取消检查",
		"internal/b.go:24",
		"返回值未透传错误",
	} {
		if !strings.Contains(allText, want) {
			t.Fatalf("渲染输出缺少 %q：%v", want, tt.Texts())
		}
	}

	mergeSource := "```pr-review\n" + `{"verdict":"merge","reason":"已核对实现，可直接合入。","comments":[]}` + "\n```\n"
	ttMerge := ui.NewTester(func(c *ui.Context) {
		ensureChatMarkdown(c, s)
		ensureCodeBlockContainer(s)
		markdown.View(c, mergeSource, &s.mdStaticOpts)
	}, 600, 300)
	ttMerge.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	ttMerge.Frame()

	mergeText := strings.Join(ttMerge.Texts(), "\n")
	if !strings.Contains(mergeText, "可直接合并") || strings.Contains(mergeText, "阻塞") || strings.Contains(mergeText, "建议") {
		t.Fatalf("comments 为 [] 时应只显示标签「可直接合并」且不显示计数：%v", ttMerge.Texts())
	}
	if !strings.Contains(mergeText, "已核对实现，可直接合入。") {
		t.Fatalf("渲染输出缺少 reason：%v", ttMerge.Texts())
	}
}

func TestPRReviewCardTwoIssuesInAssistantMessage(t *testing.T) {
	resetRuntimeTheme(t)
	setAppReduceMotion(t, true)

	fake := NewFakeClient()
	fake.SetThreadWorkDir(t.TempDir())
	fake.SetFileContents(map[string]FileContent{
		"internal/a.go": {Path: "internal/a.go", Kind: "text", Text: "package a\n"},
	})
	s := newChatState(fake)
	s.selectedThreadID = "thread-1"
	s.threads = []Thread{{ID: "thread-1"}}
	s.messages = []Message{{
		ID:        "assistant-1",
		Role:      "assistant",
		Kind:      "text",
		TurnIndex: 1,
		IsFinal:   true,
		Text: "审查完成。\n\n```pr-review\n" +
			`[{"path":"internal/a.go","line":12,"body":"未检查 nil 返回值可能导致崩溃"},{"path":"internal/b.go","line":0,"body":"循环内重复加锁存在死锁风险"}]` +
			"\n```\n",
	}}
	s.markRevealBaselineLocked(s.messages)

	tt := ui.NewTester(func(c *ui.Context) { renderChat(c, s) }, 1000, 700)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	tt.Frame()

	allText := strings.Join(tt.Texts(), "\n")
	for _, want := range []string{
		"2 建议",
		"internal/a.go:12",
		"未检查 nil 返回值可能导致崩溃",
		"internal/b.go",
		"循环内重复加锁存在死锁风险",
	} {
		if !strings.Contains(allText, want) {
			t.Fatalf("渲染输出缺少 %q：%v", want, tt.Texts())
		}
	}
	if strings.Contains(allText, `"path"`) {
		t.Fatalf("审查卡片不应露出原始 JSON 字段：%v", tt.Texts())
	}

	if err := tt.Click("internal/a.go:12"); err != nil {
		t.Fatalf("点击定位行失败：%v", err)
	}
	if !isFileManagerOpen(s) {
		t.Fatal("点击定位行后应打开文件面板")
	}
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	active := st.active
	tab := st.findTabLocked("internal/a.go")
	var hlFirst, hlLast int
	var reveal bool
	if tab != nil {
		hlFirst, hlLast, reveal = tab.hlFirst, tab.hlLast, tab.reveal
	}
	fileManagerMu.Unlock()
	if active != "internal/a.go" || hlFirst != 12 || hlLast != 12 || !reveal {
		t.Fatalf("打开的文件或定位行号不符：active=%q first=%d last=%d reveal=%v", active, hlFirst, hlLast, reveal)
	}
}

func TestPRReviewCardEmptyArray(t *testing.T) {
	resetRuntimeTheme(t)
	setAppReduceMotion(t, true)

	s := newChatState(NewFakeClient())
	source := "```pr-review\n[]\n```\n"
	tt := ui.NewTester(func(c *ui.Context) {
		ensureChatMarkdown(c, s)
		ensureCodeBlockContainer(s)
		markdown.View(c, source, &s.mdStaticOpts)
	}, 600, 300)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	tt.Frame()

	allText := strings.Join(tt.Texts(), "\n")
	if !strings.Contains(allText, "没有发现问题") {
		t.Fatalf("空数组应渲染「没有发现问题」：%v", tt.Texts())
	}
	if strings.Contains(allText, "[]") {
		t.Fatalf("空数组卡片不应渲染正文 JSON：%v", tt.Texts())
	}

	if err := tt.Click("复制代码"); err != nil {
		t.Fatalf("点击复制按钮失败：%v", err)
	}
	if strings.TrimSpace(tt.Clipboard()) != "[]" {
		t.Fatalf("复制内容 = %q，期望 []", tt.Clipboard())
	}
	if s.toastMessage != "已复制" {
		t.Fatalf("复制提示 = %q，期望 已复制", s.toastMessage)
	}
}

func TestPRReviewCardInvalidJSONAndStreamingFallback(t *testing.T) {
	resetRuntimeTheme(t)
	setAppReduceMotion(t, true)

	s := newChatState(NewFakeClient())
	invalidSource := "```pr-review\n{not-valid-json\n```\n"
	ttInvalid := ui.NewTester(func(c *ui.Context) {
		ensureChatMarkdown(c, s)
		ensureCodeBlockContainer(s)
		markdown.View(c, invalidSource, &s.mdStaticOpts)
	}, 600, 300)
	ttInvalid.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	ttInvalid.Frame()

	invalidText := strings.Join(ttInvalid.Texts(), "\n")
	if !strings.Contains(invalidText, "pr-review") || !strings.Contains(invalidText, "not-valid-json") {
		t.Fatalf("非法 JSON 应退回普通代码块显示：%v", ttInvalid.Texts())
	}
	if strings.Contains(invalidText, "没有发现问题") || strings.Contains(invalidText, "阻塞合并") {
		t.Fatalf("非法 JSON 不应显示审查卡片标题：%v", ttInvalid.Texts())
	}

	streamingSource := "```pr-review\n[{\"path\":\"a.go\",\"line\":1,\"body\":\"流式未闭合\"}]\n"
	ttStreaming := ui.NewTester(func(c *ui.Context) {
		ensureChatMarkdown(c, s)
		ensureCodeBlockContainer(s)
		markdown.View(c, streamingSource, &s.mdOpts)
	}, 600, 300)
	ttStreaming.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	ttStreaming.Frame()

	streamingText := strings.Join(ttStreaming.Texts(), "\n")
	if !strings.Contains(streamingText, "pr-review") || strings.Contains(streamingText, "1 建议") {
		t.Fatalf("流式未闭合代码块应原样展示，不提前渲染卡片：%v", ttStreaming.Texts())
	}
}

func TestPRReviewCardHoverMotionSettles(t *testing.T) {
	resetRuntimeTheme(t)
	setAppReduceMotion(t, false)

	s := newChatState(NewFakeClient())
	source := "```pr-review\n" + `[{"path":"internal/a.go","line":12,"body":"问题正文"}]` + "\n```\n"
	tt := ui.NewTester(func(c *ui.Context) {
		ensureChatMarkdown(c, s)
		ensureCodeBlockContainer(s)
		markdown.View(c, source, &s.mdStaticOpts)
	}, 600, 400)
	tt.SetPreferences(ui.Preferences{ReduceMotion: false, TextScale: 1})
	tt.Frame()

	rect, ok := tt.Find("internal/a.go:12")
	if !ok {
		t.Fatalf("未找到定位行元素：%v", tt.Texts())
	}
	tt.Move(rect.X+rect.W/2, rect.Y+rect.H/2)
	tt.Advance(DurSnap + 50*time.Millisecond)
	if tt.FramePending() {
		t.Fatal("悬停过渡结束后不应持续请求重绘")
	}
	tt.Move(0, 0)
	tt.Advance(DurSnap + 50*time.Millisecond)
	if tt.FramePending() {
		t.Fatal("移出悬停并结束过渡后不应持续请求重绘")
	}

	if err := tt.Click("问题正文"); err != nil {
		t.Fatalf("点击展开问题失败：%v", err)
	}
	tt.Advance(DurBase + 50*time.Millisecond)
	if tt.FramePending() {
		t.Fatal("展开过渡结束后不应持续请求重绘")
	}

	if err := tt.Click("问题正文"); err != nil {
		t.Fatalf("点击收起问题失败：%v", err)
	}
	tt.Advance(DurSnap + 50*time.Millisecond)
	if tt.FramePending() {
		t.Fatal("收起过渡结束后不应持续请求重绘")
	}
}

func TestPRReviewCardStructured(t *testing.T) {
	resetRuntimeTheme(t)
	setAppReduceMotion(t, true)

	// 1. 解析与旧格式降级：只有 reason 键也应视为有效对象；缺省/未知 severity 视为 suggestion。
	reasonOnly, ok := parsePRReview(`{"reason":"仅包含 reason 字段"}`)
	if !ok || reasonOnly.Reason != "仅包含 reason 字段" {
		t.Fatalf("仅含 reason 的对象应解析成功：ok=%v res=%+v", ok, reasonOnly)
	}

	legacyObj, ok := parsePRReview(`{"verdict":"fix","summary":"旧摘要降级","comments":[{"path":"a.go","line":3,"body":"旧正文"},{"path":"b.go","line":8,"severity":"unknown","headline":"未知级别"}]}`)
	if !ok || legacyObj.Reason != "旧摘要降级" || len(legacyObj.Comments) != 2 {
		t.Fatalf("旧格式对象降级失败：ok=%v res=%+v", ok, legacyObj)
	}
	if legacyObj.Comments[0].Headline != "旧正文" || legacyObj.Comments[0].Severity != "suggestion" {
		t.Fatalf("旧格式条目降级失败：%+v", legacyObj.Comments[0])
	}
	if legacyObj.Comments[1].Severity != "suggestion" {
		t.Fatalf("无法识别的 severity 应降级为 suggestion，得到 %q", legacyObj.Comments[1].Severity)
	}

	// 2. 新格式解析、分组顺序（故意把 suggestion 放在 JSON 第一条）与第一条阻塞项默认展开。
	raw := `{"verdict":"fix","reason":"存在 1 个阻塞问题需先修复。","comments":[` +
		`{"path":"internal/ui/chat.go","line":544,"severity":"suggestion","headline":"windowKnown 分支冗余","impact":"后续需同步维护两处阈值","fix":"收拢到统一阈值函数"},` +
		`{"path":"internal/ui/chat.go","line":594,"severity":"blocking","headline":"上下文判定不一致","impact":"长会话会提前误弹压缩提示","fix":"改用当前上下文占用判定","patch":"@@ -588,2 +588,2 @@\n- used := fallback()\n+ used := current()"},` +
		`{"path":"internal/ui/model.go","line":102,"severity":"blocking","headline":"第二条阻塞问题","impact":"第二个影响描述","fix":"第二个修复建议"}` +
		`]}`

	parsed, ok := parsePRReview(raw)
	if !ok || parsed.Verdict != "fix" || len(parsed.Comments) != 3 {
		t.Fatalf("新格式解析失败：ok=%v res=%+v", ok, parsed)
	}
	if parsed.Comments[1].Patch == "" {
		t.Fatalf("新格式 patch 未解析：%+v", parsed.Comments[1])
	}

	s := newChatState(NewFakeClient())
	s.selectedThreadID = "thread-structured"
	source := "```pr-review\n" + raw + "\n```\n"
	tt := ui.NewTester(func(c *ui.Context) {
		ensureChatMarkdown(c, s)
		ensureCodeBlockContainer(s)
		markdown.View(c, source, &s.mdStaticOpts)
	}, 900, 900)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	tt.Frame()

	allText := strings.Join(tt.Texts(), "\n")
	for _, want := range []string{
		"修复后合并",
		"2 阻塞 · 1 建议",
		"存在 1 个阻塞问题需先修复。",
		"阻塞合并",
		"建议修复",
		"长会话会提前误弹压缩提示",
		"改用当前上下文占用判定",
		"参考改法",
	} {
		if !strings.Contains(allText, want) {
			t.Fatalf("结构化卡片缺少 %q：%v", want, tt.Texts())
		}
	}

	// 验证第一条阻塞项默认展开，第二条阻塞项与建议项默认收起，patch 默认折叠。
	if strings.Contains(allText, "第二个影响描述") || strings.Contains(allText, "后续需同步维护两处阈值") {
		t.Fatalf("非首条阻塞项应默认收起：%v", tt.Texts())
	}
	if strings.Contains(allText, "- used := fallback()") {
		t.Fatalf("参考改法 patch 应默认折叠：%v", tt.Texts())
	}

	// 验证分组顺序：「阻塞合并」在「建议修复」之前。
	blockingRect, okB := tt.Find("阻塞合并")
	suggestionRect, okS := tt.Find("建议修复")
	if !okB || !okS {
		t.Fatalf("未找到分组标题：okB=%v okS=%v", okB, okS)
	}
	if blockingRect.Y >= suggestionRect.Y {
		t.Fatalf("阻塞合并组应排在建议修复组前面：blockingY=%v suggestionY=%v", blockingRect.Y, suggestionRect.Y)
	}

	// 展开参考改法与第二条阻塞项。
	if err := tt.Click("参考改法"); err != nil {
		t.Fatalf("点击参考改法失败：%v", err)
	}
	tt.Frame()
	if !strings.Contains(strings.Join(tt.Texts(), "\n"), "+ used := current()") {
		t.Fatalf("点击参考改法后应显示 diff 行：%v", tt.Texts())
	}

	if err := tt.Click("windowKnown 分支冗余"); err != nil {
		t.Fatalf("点击建议项展开失败：%v", err)
	}
	tt.Frame()
	if !strings.Contains(strings.Join(tt.Texts(), "\n"), "后续需同步维护两处阈值") {
		t.Fatalf("点击建议项后应展开影响描述：%v", tt.Texts())
	}
}

func TestPRReviewCardActions(t *testing.T) {
	resetRuntimeTheme(t)
	setAppReduceMotion(t, true)

	raw := `{"verdict":"fix","reason":"需先处理阻塞项。","comments":[` +
		`{"path":"internal/a.go","line":12,"severity":"blocking","headline":"空指针风险","impact":"可能触发 panic","fix":"增加 nil 检查"},` +
		`{"path":"internal/b.go","line":24,"severity":"suggestion","headline":"补充错误日志","impact":"排查困难","fix":"记录失败原因"}` +
		`]}`
	source := "```pr-review\n" + raw + "\n```\n"

	// 1. PR 作用域之外不显示「发到 PR」与「合并 PR」。
	t.Run("outside_pr_scope", func(t *testing.T) {
		s, _, dp := reviewPRSetup(t)
		diffPanelMu.Lock()
		dp.scope = "turn"
		dp.prNumber = 0
		diffPanelMu.Unlock()

		tt := ui.NewTester(func(c *ui.Context) {
			ensureChatMarkdown(c, s)
			ensureCodeBlockContainer(s)
			markdown.View(c, source, &s.mdStaticOpts)
		}, 900, 800)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
		tt.Frame()

		allText := strings.Join(tt.Texts(), "\n")
		if strings.Contains(allText, "发到 PR") {
			t.Fatalf("PR 作用域之外不应显示「发到 PR」：%v", tt.Texts())
		}
		if strings.Contains(allText, "合并 PR") {
			t.Fatalf("PR 作用域之外不应显示「合并 PR」：%v", tt.Texts())
		}
	})

	// 2. 单条发到 PR：调用一次 GitPRSubmitReview，参数为 COMMENT 和规范化 body，成功后删除对应草稿。
	t.Run("send_single_and_delete_draft", func(t *testing.T) {
		s, f, dp := reviewPRSetup(t)
		calls := make(chan reviewPRCall, 4)
		f.submit = func(_ context.Context, call reviewPRCall) error {
			calls <- call
			return nil
		}

		diffPanelMu.Lock()
		dp.comments = []*reviewComment{
			{id: 1, key: reviewDiffKey{scope: "pr", prNumber: 7, path: "internal/a.go"}, lines: editor.DiffLineRange{Side: editor.DiffNew, Start: 11, End: 11}, body: "草稿 A"},
			{id: 2, key: reviewDiffKey{scope: "pr", prNumber: 7, path: "internal/c.go"}, lines: editor.DiffLineRange{Side: editor.DiffNew, Start: 98, End: 98}, body: "无关草稿"},
		}
		diffPanelMu.Unlock()

		tt := ui.NewTester(func(c *ui.Context) {
			ensureChatMarkdown(c, s)
			ensureCodeBlockContainer(s)
			markdown.View(c, source, &s.mdStaticOpts)
		}, 900, 900)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
		tt.Frame()

		if err := tt.Click("将第 1 条发到 PR"); err != nil {
			t.Fatalf("点击单条发到 PR 失败：%v", err)
		}

		var got reviewPRCall
		select {
		case got = <-calls:
		case <-time.After(2 * time.Second):
			t.Fatal("未调用 GitPRSubmitReview")
		}
		select {
		case extra := <-calls:
			t.Fatalf("应只调用一次 GitPRSubmitReview，多出调用：%+v", extra)
		case <-time.After(50 * time.Millisecond):
		}

		want := reviewPRCall{
			threadID: "t1",
			number:   7,
			event:    "COMMENT",
			body:     "",
			comments: []git.PRReviewComment{{
				Path: "internal/a.go",
				Line: 12,
				Side: "RIGHT",
				Body: "[阻塞] 空指针风险\n\n影响：可能触发 panic\n\n建议：增加 nil 检查",
			}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("GitPRSubmitReview 参数 = %+v，期望 %+v", got, want)
		}

		waitReviewComments(t, func() bool {
			diffPanelMu.Lock()
			defer diffPanelMu.Unlock()
			return len(dp.comments) == 1 && dp.comments[0].key.path == "internal/c.go"
		})
		tt.Frame()
		if !strings.Contains(strings.Join(tt.Texts(), "\n"), "已发到 PR") {
			t.Fatalf("发送成功后状态应变为「已发到 PR」：%v", tt.Texts())
		}
	})

	// 3. 忽略某条后：删除其草稿，且底部「发到 PR」不再包含该条；恢复后重新待处理。
	t.Run("ignore_excludes_from_footer_publish", func(t *testing.T) {
		s, f, dp := reviewPRSetup(t)
		calls := make(chan reviewPRCall, 4)
		f.submit = func(_ context.Context, call reviewPRCall) error {
			calls <- call
			return nil
		}

		diffPanelMu.Lock()
		dp.comments = []*reviewComment{
			{id: 1, key: reviewDiffKey{scope: "pr", prNumber: 7, path: "internal/b.go"}, lines: editor.DiffLineRange{Side: editor.DiffNew, Start: 23, End: 23}, body: "草稿 B"},
		}
		diffPanelMu.Unlock()

		tt := ui.NewTester(func(c *ui.Context) {
			ensureChatMarkdown(c, s)
			ensureCodeBlockContainer(s)
			markdown.View(c, source, &s.mdStaticOpts)
		}, 900, 900)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
		tt.Frame()

		// 展开第 2 条建议项并忽略。
		if err := tt.Click("补充错误日志"); err != nil {
			t.Fatalf("展开建议项失败：%v", err)
		}
		tt.Frame()
		if err := tt.Click("忽略第 2 条"); err != nil {
			t.Fatalf("点击忽略失败：%v", err)
		}
		tt.Frame()

		allText := strings.Join(tt.Texts(), "\n")
		if !strings.Contains(allText, "已忽略") || !strings.Contains(allText, "恢复") {
			t.Fatalf("忽略后应显示「已忽略」与「恢复」：%v", tt.Texts())
		}
		diffPanelMu.Lock()
		remainingDrafts := len(dp.comments)
		diffPanelMu.Unlock()
		if remainingDrafts != 0 {
			t.Fatalf("忽略后应删除对应草稿，剩余草稿数 = %d", remainingDrafts)
		}

		// 点击底部「发到 PR」：只应提交未忽略的第 1 条。
		if err := tt.Click("发全部到 PR"); err != nil {
			t.Fatalf("点击底部发到 PR 失败：%v", err)
		}
		var got reviewPRCall
		select {
		case got = <-calls:
		case <-time.After(2 * time.Second):
			t.Fatal("未调用 GitPRSubmitReview")
		}
		if len(got.comments) != 1 || got.comments[0].Path != "internal/a.go" {
			t.Fatalf("底部发到 PR 应只包含未忽略条目，实际 = %+v", got.comments)
		}

		// 验证「恢复」把状态改回待处理但不恢复草稿。
		if err := tt.Click("恢复第 2 条"); err != nil {
			t.Fatalf("点击恢复失败：%v", err)
		}
		tt.Frame()
		if strings.Contains(strings.Join(tt.Texts(), "\n"), "已忽略") {
			t.Fatalf("恢复后不应再显示「已忽略」：%v", tt.Texts())
		}
		diffPanelMu.Lock()
		remainingAfterRestore := len(dp.comments)
		diffPanelMu.Unlock()
		if remainingAfterRestore != 0 {
			t.Fatalf("恢复不应恢复已删草稿，实际草稿数 = %d", remainingAfterRestore)
		}
	})

	// 4. 「让 agent 修复」与「复审」动作语义。
	t.Run("agent_fix_and_rereview", func(t *testing.T) {
		s, f, dp := reviewPRSetup(t)
		tt := ui.NewTester(func(c *ui.Context) {
			ensureChatMarkdown(c, s)
			ensureCodeBlockContainer(s)
			markdown.View(c, source, &s.mdStaticOpts)
		}, 900, 900)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
		tt.Frame()

		if err := tt.Click("让 agent 修复第 1 条"); err != nil {
			t.Fatalf("点击让 agent 修复失败：%v", err)
		}
		tt.Frame()
		if !strings.Contains(strings.Join(tt.Texts(), "\n"), "已交给 agent") {
			t.Fatalf("点击让 agent 修复后应显示「已交给 agent」：%v", tt.Texts())
		}
		waitReviewComments(t, func() bool { return f.SendCalls() >= 1 })
		sent := f.LastSentText()
		if !strings.Contains(sent, "internal/a.go:12") || !strings.Contains(sent, "空指针风险") || !strings.Contains(sent, "增加 nil 检查") {
			t.Fatalf("发送给 agent 的修复指令内容不符：%q", sent)
		}

		// 等待 sendTextOnly 异步发送完成，避免 s.sending 阻塞后续复审指令。
		waitReviewComments(t, func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return !s.sending
		})

		if err := tt.Click("复审"); err != nil {
			t.Fatalf("点击复审失败：%v", err)
		}
		diffPanelMu.Lock()
		auto := dp.prAIAuto
		diffPanelMu.Unlock()
		if !auto {
			t.Fatal("点击复审后应设置 dp.prAIAuto = true")
		}
		waitReviewComments(t, func() bool { return f.SendCalls() >= 2 })
		if got := f.LastSentText(); got != "代码已更新，请按同样的要求和输出格式重新审查当前分支" {
			t.Fatalf("复审指令 = %q", got)
		}
	})
}
