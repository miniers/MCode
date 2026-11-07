package mcodeui

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestFindMessageMatches 覆盖会话内查找的匹配规则：
// 大小写敏感、空查询、多条命中按时间顺序、忽略工具与思考消息。
func TestFindMessageMatches(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "帮我修一下 Build 脚本"},
		{ID: "r1", Role: "reasoning", Kind: "reasoning", Text: "先看 build 脚本"},
		{ID: "a1", Role: "assistant", Text: "已经改好 build 的入口"},
		{ID: "t1", Role: "tool", Text: "cat build.sh 输出 build 内容"},
		{ID: "s1", Role: "system", Text: "build 系统提示"},
		{ID: "u2", Role: "user", Text: "再检查 build 日志"},
		{ID: "a2", Role: "assistant", Text: "无关内容"},
	}

	cases := []struct {
		name          string
		query         string
		caseSensitive bool
		want          []string
	}{
		{name: "忽略大小写", query: "build", caseSensitive: false, want: []string{"u1", "a1", "u2"}},
		{name: "区分大小写", query: "Build", caseSensitive: true, want: []string{"u1"}},
		{name: "区分大小写时小写不命中", query: "BUILD", caseSensitive: true, want: nil},
		{name: "空查询无命中", query: "", caseSensitive: false, want: nil},
		{name: "纯空白查询无命中", query: "   ", caseSensitive: false, want: nil},
		{name: "无匹配", query: "不存在的内容", caseSensitive: false, want: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := findMessageMatches(msgs, tc.query, tc.caseSensitive)
			if len(got) != len(tc.want) {
				t.Fatalf("命中数 = %v，期望 %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("第 %d 条命中 = %q，期望 %q（完整结果 %v）", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// TestFindMessageMatchesSkipsUnlocatable 覆盖两种不参与定位的消息：
// 工具输出与思考块不参与匹配，没有消息 ID 的消息也无从跳转，一并跳过。
func TestFindMessageMatchesSkipsUnlocatable(t *testing.T) {
	msgs := []Message{
		{ID: "t1", Role: "tool", Text: "grep keyword 结果"},
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "思考 keyword"},
		{ID: "", Role: "assistant", Text: "没有 ID 的 keyword"},
		{ID: "a1", Role: "assistant", Text: "正文提到 keyword"},
	}

	got := findMessageMatches(msgs, "keyword", false)
	if len(got) != 1 || got[0] != "a1" {
		t.Fatalf("命中结果 = %v，期望仅 [a1]", got)
	}
}

// TestChatFindMoveCursor 覆盖命中游标的循环移动：首尾相接，无命中时不动。
func TestChatFindMoveCursor(t *testing.T) {
	f := &chatFindState{matches: []string{"a", "b", "c"}}

	if !f.moveCursor(1) || f.cursor != 1 {
		t.Fatalf("向后移动一格后 cursor = %d，期望 1", f.cursor)
	}
	if !f.moveCursor(-1) || f.cursor != 0 {
		t.Fatalf("向前移动一格后 cursor = %d，期望 0", f.cursor)
	}
	if !f.moveCursor(-1) || f.cursor != 2 {
		t.Fatalf("从首条向前应循环到末条，cursor = %d，期望 2", f.cursor)
	}
	if !f.moveCursor(1) || f.cursor != 0 {
		t.Fatalf("从末条向后应循环到首条，cursor = %d，期望 0", f.cursor)
	}

	empty := &chatFindState{}
	if empty.moveCursor(1) {
		t.Fatal("没有命中时不应移动游标")
	}
	if empty.cursor != 0 {
		t.Fatalf("没有命中时 cursor 应保持 0，得到 %d", empty.cursor)
	}
}

// TestChatFindCounterText 覆盖查找条计数文案。
func TestChatFindCounterText(t *testing.T) {
	if got := chatFindCounterText(&chatFindState{query: "a", matches: []string{"m1", "m2"}, cursor: 1}); got != "2/2" {
		t.Fatalf("计数文案 = %q，期望 \"2/2\"", got)
	}
	if got := chatFindCounterText(&chatFindState{query: "a"}); got != "0/0" {
		t.Fatalf("无命中时计数文案 = %q，期望 \"0/0\"", got)
	}
	if got := chatFindCounterText(&chatFindState{query: "  "}); got != "" {
		t.Fatalf("空查询时计数文案 = %q，期望空串", got)
	}
}

// TestMarkMarkdownSource 覆盖源码层高亮注入：普通命中、当前命中带下划线、大小写规则、
// 多次命中，以及代码围栏、行内代码、链接目标与 HTML 标签内部不注入。
func TestMarkMarkdownSource(t *testing.T) {
	cases := []struct {
		name          string
		src           string
		query         string
		caseSensitive bool
		active        int
		want          string
	}{
		{
			name:   "普通命中带下划线",
			src:    "hello world",
			query:  "world",
			active: 0,
			want:   "hello <mark><u>world</u></mark>",
		},
		{
			name:   "非当前命中只加背景",
			src:    "hello world",
			query:  "world",
			active: -1,
			want:   "hello <mark>world</mark>",
		},
		{
			name:   "多次命中只给当前项加下划线",
			src:    "build build build",
			query:  "build",
			active: 1,
			want:   "<mark>build</mark> <mark><u>build</u></mark> <mark>build</mark>",
		},
		{
			name:          "不区分大小写",
			src:           "Build build",
			query:         "build",
			active:        -1,
			want:          "<mark>Build</mark> <mark>build</mark>",
			caseSensitive: false,
		},
		{
			name:          "区分大小写",
			src:           "Build build",
			query:         "Build",
			active:        -1,
			want:          "<mark>Build</mark> build",
			caseSensitive: true,
		},
		{
			name:   "代码围栏内不注入",
			src:    "```\nbuild\n```\nbuild",
			query:  "build",
			active: 0,
			want:   "```\nbuild\n```\n<mark><u>build</u></mark>",
		},
		{
			name:   "波浪号围栏内不注入",
			src:    "~~~go\nbuild\n~~~\nbuild",
			query:  "build",
			active: 0,
			want:   "~~~go\nbuild\n~~~\n<mark><u>build</u></mark>",
		},
		{
			name:   "行内代码内不注入",
			src:    "`build` build",
			query:  "build",
			active: 0,
			want:   "`build` <mark><u>build</u></mark>",
		},
		{
			name:   "链接目标内不注入",
			src:    "[build](https://example.com/build)",
			query:  "build",
			active: 0,
			want:   "[<mark><u>build</u></mark>](https://example.com/build)",
		},
		{
			name:   "HTML 标签内不注入",
			src:    `<span class="build">build</span>`,
			query:  "build",
			active: 0,
			want:   `<span class="build"><mark><u>build</u></mark></span>`,
		},
		{
			name:   "空查询原样返回",
			src:    "build 脚本",
			query:  "",
			active: 0,
			want:   "build 脚本",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := markMarkdownSource(tc.src, []rune(tc.query), tc.caseSensitive, tc.active)
			if got != tc.want {
				t.Fatalf("注入结果 = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// TestFindHighlightSource 覆盖渲染入口的开关条件：查找条关闭、查询为空、消息不在命中列表时
// 一律原样返回；命中时当前消息的第一个命中带下划线，其它命中消息只加背景高亮。
func TestFindHighlightSource(t *testing.T) {
	s := newChatState(NewFakeClient())
	f := getChatFindState(s)
	src := "build 脚本 build"

	if got := findHighlightSource(s, "a1", src); got != src {
		t.Fatalf("查找条关闭时应原样返回，got %q", got)
	}

	f.open = true
	if got := findHighlightSource(s, "a1", src); got != src {
		t.Fatalf("查询为空时应原样返回，got %q", got)
	}

	f.query = "build"
	f.matches = []string{"a1", "a2"}
	f.cursor = 0
	if got := findHighlightSource(s, "a2", src); got != "<mark>build</mark> 脚本 <mark>build</mark>" {
		t.Fatalf("非当前命中只加背景，got %q", got)
	}
	if got := findHighlightSource(s, "a1", src); got != "<mark><u>build</u></mark> 脚本 <mark>build</mark>" {
		t.Fatalf("当前命中第一个命中带下划线，got %q", got)
	}
	if got := findHighlightSource(s, "a9", src); got != src {
		t.Fatalf("未命中的消息应原样返回，got %q", got)
	}
}

// turnWorkFindMessages 在 turnWorkSplitToolMessages 之后补若干回合，让时间线超过一屏，
// 便于断言滚动落点。
func turnWorkFindMessages() []Message {
	msgs := turnWorkSplitToolMessages()
	for turn := 3; turn <= 12; turn++ {
		msgs = append(msgs,
			Message{ID: fmt.Sprintf("u%d", turn), Role: "user", Text: fmt.Sprintf("第 %d 轮提问", turn), TurnIndex: turn, IsFinal: true},
			Message{ID: fmt.Sprintf("a%d", turn), Role: "assistant", Kind: "text", Text: fmt.Sprintf("第 %d 轮答复", turn), TurnIndex: turn, IsFinal: true},
		)
	}
	return msgs
}

// TestScrollToMessageIDExpandsTurnWorkFold 验证跳转目标落在回合折叠区里时会先展开折叠项
// 再滚动到该条目：折叠区里的命令在展开后可见，视口包含折叠项所在行。
func TestScrollToMessageIDExpandsTurnWorkFold(t *testing.T) {
	msgs := turnWorkFindMessages()
	fake := NewFakeClient()
	fake.messages["t1"] = msgs
	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)
	// 回合折叠项在第一屏之外，虚拟化列表不构建屏外行，所以等最后那条可见的答复渲染出来。
	waitFor(t, tt, func() bool { return tt.HasText("第 12 轮答复") })

	state.mu.Lock()
	threadID := state.selectedThreadID
	state.mu.Unlock()

	items := applyTurnWorkFold(state, threadID, buildTimelineItems(msgs))
	foldRow := -1
	for i, it := range items {
		if it.kind == timelineItemTurnWork {
			foldRow = i
		}
	}
	if foldRow < 0 {
		t.Fatalf("时间线里应有一个回合折叠项: %v", items)
	}
	foldID := turnWorkFoldID("u1")
	state.mu.Lock()
	open := state.expandedTool[foldID]
	state.mu.Unlock()
	if open {
		t.Fatal("回合折叠项默认应处于收起状态")
	}

	// t2 是折叠区里的第二条命令，不在 items 顶层露面。
	if !scrollToMessageID(state, items, "t2") {
		t.Fatal("跳转到折叠区内的消息应成功")
	}
	state.mu.Lock()
	open = state.expandedTool[foldID]
	state.mu.Unlock()
	if !open {
		t.Fatal("跳转到折叠区内的消息后折叠项应展开")
	}

	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if !tt.HasText("go build ./...") {
		t.Fatalf("展开后应看到折叠区内的命令: %v", tt.Texts())
	}
	first, last := state.chatListState.Visible()
	if first > foldRow || last < foldRow {
		t.Fatalf("视口应包含折叠项所在行 %d，got [%d,%d]", foldRow, first, last)
	}
}
