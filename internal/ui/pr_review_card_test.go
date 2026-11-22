package mcodeui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

func TestPRReviewCardParseAndTitle(t *testing.T) {
	res, ok := parsePRReview(`[{"path":"internal/a.go","line":12,"body":"空指针风险"},{"path":"internal/b.go","line":0,"body":"缺少锁保护"}]`)
	if !ok || res.Verdict != "" || res.Summary != "" || len(res.Comments) != 2 {
		t.Fatalf("期望解析出 2 条问题，得到 ok=%v res=%+v", ok, res)
	}
	if res.Comments[0].Path != "internal/a.go" || res.Comments[0].Line != 12 || res.Comments[0].Body != "空指针风险" {
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

	if got := prReviewCardTitle("", 0); got != "没有发现问题" {
		t.Fatalf("空数组标题 = %q", got)
	}
	if got := prReviewCardTitle("", 2); got != "2 个问题" {
		t.Fatalf("两条问题标题 = %q", got)
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
		count   int
		title   string
	}{
		{verdict: "merge", label: "可以合并", count: 0, title: "可以合并"},
		{verdict: "comment", label: "评论给作者", count: 3, title: "评论给作者 · 3 个问题"},
		{verdict: "fix", label: "合并后修复", count: 1, title: "合并后修复 · 1 个问题"},
		{verdict: "close", label: "不建议合并", count: 0, title: "不建议合并"},
		{verdict: "other", label: "", count: 2, title: "2 个问题"},
	}
	for _, tc := range cases {
		if got := prReviewVerdictLabel(tc.verdict); got != tc.label {
			t.Fatalf("prReviewVerdictLabel(%q) = %q，期望 %q", tc.verdict, got, tc.label)
		}
		if got := prReviewCardTitle(tc.verdict, tc.count); got != tc.title {
			t.Fatalf("prReviewCardTitle(%q, %d) = %q，期望 %q", tc.verdict, tc.count, got, tc.title)
		}
	}

	rawObj := `{"verdict":"comment","summary":"这个 PR 修复了并发写问题，但设计上需要作者调整接口签名。","comments":[{"path":"internal/a.go","line":18,"body":"缺少上下文取消检查"},{"path":"internal/b.go","line":24,"body":"返回值未透传错误"}]}`
	res, ok := parsePRReview(rawObj)
	if !ok {
		t.Fatal("对象格式解析失败")
	}
	if res.Verdict != "comment" || res.Summary != "这个 PR 修复了并发写问题，但设计上需要作者调整接口签名。" || len(res.Comments) != 2 {
		t.Fatalf("对象格式解析字段不符：%+v", res)
	}

	unknownRes, ok := parsePRReview(`{"verdict":"shipit","summary":"未知结论测试","comments":[]}`)
	if !ok {
		t.Fatal("未知 verdict 对象应允许解析")
	}
	if unknownRes.Verdict != "" {
		t.Fatalf("未知 verdict 应按空处理，得到 %q", unknownRes.Verdict)
	}
	if got := prReviewCardTitle(unknownRes.Verdict, len(unknownRes.Comments)); got != "没有发现问题" {
		t.Fatalf("未知 verdict 且无问题时标题 = %q，期望 没有发现问题", got)
	}

	emptyCommentsRes, ok := parsePRReview(`{"verdict":"merge","summary":"已核对实现，可直接合入。","comments":[]}`)
	if !ok || emptyCommentsRes.Verdict != "merge" || len(emptyCommentsRes.Comments) != 0 {
		t.Fatalf("空 comments 对象解析异常：ok=%v res=%+v", ok, emptyCommentsRes)
	}
	if got := prReviewCardTitle(emptyCommentsRes.Verdict, len(emptyCommentsRes.Comments)); got != "可以合并" {
		t.Fatalf("comments 为 [] 时标题 = %q，期望 可以合并", got)
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
		"评论给作者 · 2 个问题",
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

	mergeSource := "```pr-review\n" + `{"verdict":"merge","summary":"已核对实现，可直接合入。","comments":[]}` + "\n```\n"
	ttMerge := ui.NewTester(func(c *ui.Context) {
		ensureChatMarkdown(c, s)
		ensureCodeBlockContainer(s)
		markdown.View(c, mergeSource, &s.mdStaticOpts)
	}, 600, 300)
	ttMerge.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	ttMerge.Frame()

	mergeText := strings.Join(ttMerge.Texts(), "\n")
	if !strings.Contains(mergeText, "可以合并") || strings.Contains(mergeText, "个问题") {
		t.Fatalf("comments 为 [] 时应只显示标签「可以合并」：%v", ttMerge.Texts())
	}
	if !strings.Contains(mergeText, "已核对实现，可直接合入。") {
		t.Fatalf("渲染输出缺少 summary：%v", ttMerge.Texts())
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
		"2 个问题",
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
	if strings.Contains(invalidText, "没有发现问题") || strings.Contains(invalidText, "个问题") {
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
	if !strings.Contains(streamingText, "pr-review") || strings.Contains(streamingText, "1 个问题") {
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
	}, 600, 300)
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
}
