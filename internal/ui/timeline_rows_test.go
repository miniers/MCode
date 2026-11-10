package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

func TestParseForwardedTurns(t *testing.T) {
	cases := []struct {
		content string
		want    []forwardedTurn
	}{
		{"User:\n第一句\n\nAssistant:\n第一答\n\nUser:\n第二句\n\nAssistant:\n第二答", []forwardedTurn{{"user", "第一句"}, {"assistant", "第一答"}, {"user", "第二句"}, {"assistant", "第二答"}}},
		{"User:\n一\n[trimmed]\n\n[… omitted …]\n\nAssistant:\n二", []forwardedTurn{{"user", "一\n…"}, {"omitted", "[… omitted …]"}, {"assistant", "二"}}},
		{"  没有前缀\n正文  ", []forwardedTurn{{"assistant", "没有前缀\n正文"}}},
	}
	for _, tc := range cases {
		if got := parseForwardedTurns(tc.content); !slices.Equal(got, tc.want) {
			t.Errorf("parseForwardedTurns(%q) = %#v, want %#v", tc.content, got, tc.want)
		}
	}
}

func TestSplitForwardedTail(t *testing.T) {
	turns := []forwardedTurn{{"user", "a"}, {"assistant", "b"}, {"user", "c"}, {"assistant", "d"}}
	earlier, tail := splitForwardedTail(turns)
	if !slices.Equal(earlier, turns[:2]) || !slices.Equal(tail, turns[2:]) {
		t.Fatalf("unexpected split: %#v, %#v", earlier, tail)
	}
	turns = []forwardedTurn{{"omitted", "…"}, {"assistant", "d"}}
	earlier, tail = splitForwardedTail(turns)
	if !slices.Equal(earlier, turns[:1]) || !slices.Equal(tail, turns[1:]) {
		t.Fatalf("unexpected split without user: %#v, %#v", earlier, tail)
	}
}

func TestWithForkOriginItemForwardedContext(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.threads = []Thread{
		{ID: "src", Title: "来源"},
		{ID: "forward", ForkedFromThreadID: "src"},
		{ID: "fork", ForkedFromThreadID: "src", ForkedFromMessageID: "m"},
	}
	st := getForwardedContextState(s)
	for _, id := range []string{"forward", "fork"} {
		st.loaded[id] = true
		st.contexts[id] = ForwardedContext{Content: "User:\n内容"}
		s.selectedThreadID = id
		items := s.withForkOriginItem([]timelineItem{{kind: timelineItemSingle}})
		if len(items) != 2 || items[0].kind != timelineItemForwardedContext {
			t.Fatalf("%s: unexpected items: %#v", id, items)
		}
	}
	st.contexts["fork"] = ForwardedContext{Content: "  "}
	s.selectedThreadID = "fork"
	if items := s.withForkOriginItem(nil); len(items) != 1 || items[0].kind != timelineItemForkOrigin {
		t.Fatalf("empty fork: unexpected items: %#v", items)
	}
	s.selectedThreadID = "forward"
	st.contexts["forward"] = ForwardedContext{}
	if items := s.withForkOriginItem(nil); len(items) != 0 {
		t.Fatalf("empty forward: unexpected items: %#v", items)
	}
}

// TestTimelineRows_KeyStabilityOnToggle 验证展开或折叠思考块与工具组后，时间线行的 key 绝对保持稳定。
func TestTimelineRows_KeyStabilityOnToggle(t *testing.T) {
	messages := []Message{
		{ID: "u1", Role: "user", Text: "Hello", TurnIndex: 1},
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "Let me think...", TurnIndex: 1},
		{ID: "t1", Role: "tool", Text: "git status", TurnIndex: 1},
		{ID: "t2", Role: "tool", Text: "cat file.go", TurnIndex: 1},
		{ID: "t3", Role: "tool", Text: "go build", TurnIndex: 1},
		{ID: "a1", Role: "assistant", Kind: "text", Text: "Done!", TurnIndex: 1},
	}

	itemsBefore := buildTimelineItems(messages)
	keysBefore := make([]string, len(itemsBefore))
	for i, item := range itemsBefore {
		keysBefore[i] = item.id
	}

	fake := NewFakeClient()
	s := newChatState(fake)

	// 模拟折叠展开操作
	s.toggleReasoning("r1")
	s.toggleTool("toolgroup-t1")

	// 再次构建时间线
	itemsAfter := buildTimelineItems(messages)
	if len(itemsAfter) != len(itemsBefore) {
		t.Fatalf("expected %d items after toggle, got %d", len(itemsBefore), len(itemsAfter))
	}

	for i, item := range itemsAfter {
		if item.id != keysBefore[i] {
			t.Errorf("item %d key changed: before=%q, after=%q", i, keysBefore[i], item.id)
		}
	}
}

// TestTimelineRows_StreamingAppendNonLastRowNotRebuilt 验证流式追加文字时，非最后一行不被重建且状态稳定。
func TestTimelineRows_StreamingAppendNonLastRowNotRebuilt(t *testing.T) {
	fake := NewFakeClient()
	now := time.Now()
	running := Thread{ID: "t1", ProjectID: "p1", Title: "StreamTest", Provider: "claude", UpdatedAt: now, IsRunning: true}
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1", Path: "/workspace"}},
		map[string][]Thread{"p1": {running}},
	)
	fake.messages["t1"] = []Message{
		{ID: "u1", Role: "user", Text: "请输出一段很长的文本", TurnIndex: 1, IsFinal: true},
		{ID: "a1", Role: "assistant", Text: "第一行回复内容", TurnIndex: 1, IsFinal: false},
	}

	tt := ui.NewTester(Root(fake), 1000, 700)
	// 最后一条助手消息逐字显示，Tester 时间不前进时只会出现首字
	waitFor(t, tt, func() bool {
		return tt.HasText("请输出一段很长的文本") && tt.HasText("第")
	})

	// 模拟流式追加多行文字，触发多轮换行
	lines := []string{
		"\n第二行：换行测试第一句，长度持续增长中……",
		"\n第三行：换行测试第二句，确保行高不断递增……",
		"\n第四行：换行测试第三句，验证上一行不被卸载……",
	}

	accumulated := "第一行回复内容"
	for _, chunk := range lines {
		accumulated += chunk
		fake.TriggerUpdate(ThreadUpdate{
			ThreadID: "t1",
			Thread:   running,
			Messages: []Message{{ID: "a1", Role: "assistant", Text: accumulated, TurnIndex: 1, IsFinal: false}},
		})

		tt.Frame()

		// 验证历史非最后一行（用户提问）依然稳固存在
		if !tt.HasText("请输出一段很长的文本") {
			t.Fatalf("非最后一行用户消息在流式换行时丢失或被重建")
		}
	}

	// 推进时间让逐字显示追上，再断言完整文本
	tt.Advance(10 * time.Second)
	if !tt.HasText("第四行：换行测试第三句") {
		// 流式结束：消息置为 IsFinal 且会话停止运行，改为整段渲染
		done := running
		done.IsRunning = false
		fake.TriggerUpdate(ThreadUpdate{
			ThreadID: "t1",
			Thread:   done,
			Messages: []Message{{ID: "a1", Role: "assistant", Text: accumulated, TurnIndex: 1, IsFinal: true}},
		})
		waitFor(t, tt, func() bool { return tt.HasText("第四行：换行测试第三句") })
	}
	if !tt.HasText("请输出一段很长的文本") {
		t.Fatalf("流式结束后用户消息丢失")
	}
}

// TestTimelineRows_RunningIndicatorShownWhileWaitingReply 验证会话运行中但还没有流式输出时，
// 时间线末尾显示“回复中”指示；后端报告 turn 结束后指示消失。
func TestTimelineRows_RunningIndicatorShownWhileWaitingReply(t *testing.T) {
	fake := NewFakeClient()
	running := Thread{ID: "t1", ProjectID: "p1", Title: "等待回复", Provider: "claude", UpdatedAt: time.Now(), IsRunning: true}
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1", Path: "/workspace"}},
		map[string][]Thread{"p1": {running}},
	)
	fake.messages["t1"] = []Message{
		{ID: "u1", Role: "user", Text: "开始吧", TurnIndex: 1, IsFinal: true},
	}

	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("正在回复…") })

	// 助手开始流式输出后指示仍然显示。
	fake.TriggerUpdate(ThreadUpdate{
		ThreadID: "t1",
		Thread:   running,
		Messages: []Message{{ID: "a1", Role: "assistant", Text: "好的，我先看看。", TurnIndex: 1, IsFinal: false}},
	})
	waitFor(t, tt, func() bool { return tt.HasText("好") && tt.HasText("正在回复…") })

	// 后端报告 turn 结束后指示消失。
	done := running
	done.IsRunning = false
	fake.TriggerUpdate(ThreadUpdate{ThreadID: "t1", Thread: done})
	waitFor(t, tt, func() bool { return !tt.HasText("正在回复…") })
}

// TestTimelineRows_RunningIndicatorDuringStreaming 验证流式输出期间指示常驻，结束后消失。
func TestTimelineRows_RunningIndicatorDuringStreaming(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message Message
	}{
		{"assistant", Message{ID: "a1", Role: "assistant", Kind: "text", Text: "回复内容", TurnIndex: 1}},
		{"reasoning", Message{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "思考内容", TurnIndex: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := NewFakeClient()
			running := Thread{ID: "t1", ProjectID: "p1", Title: "流式回复", Provider: "claude", UpdatedAt: time.Now(), IsRunning: true}
			fake.SetProjectsAndThreads(
				[]Project{{ID: "p1", Name: "Proj1", Path: "/workspace"}},
				map[string][]Thread{"p1": {running}},
			)
			fake.messages["t1"] = []Message{
				{ID: "u1", Role: "user", Text: "开始吧", TurnIndex: 1, IsFinal: true},
				tc.message,
			}

			tt := ui.NewTester(Root(fake), 1000, 700)
			waitFor(t, tt, func() bool { return tt.HasText("开始吧") && tt.HasText("正在回复…") })

			done := running
			done.IsRunning = false
			fake.TriggerUpdate(ThreadUpdate{ThreadID: "t1", Thread: done})
			waitFor(t, tt, func() bool { return !tt.HasText("正在回复…") })
		})
	}
}

// TestTimelineRows_RunningIndicatorInEmptyThread 验证新会话还没有任何消息时运行中也显示“回复中”：
// 首轮发送后引导屏否则毫无变化，看起来像没有反应。
func TestTimelineRows_RunningIndicatorInEmptyThread(t *testing.T) {
	fake := NewFakeClient()
	running := Thread{ID: "t1", ProjectID: "p1", Title: "新会话", Provider: "claude", UpdatedAt: time.Now(), IsRunning: true}
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1", Path: "/workspace"}},
		map[string][]Thread{"p1": {running}},
	)

	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("正在回复…") })
}

// TestTimelineRows_SessionUsageRow 验证时间线底部显示「会话统计」入口按钮（完整用量在浮层里）。
func TestTimelineRows_SessionUsageRow(t *testing.T) {
	fake := NewFakeClient()
	fake.messages["t1"] = []Message{
		{ID: "u1", Role: "user", Text: "你好", TurnIndex: 1, IsFinal: true},
		{ID: "a1", Role: "assistant", Text: "世界", TurnIndex: 1, IsFinal: true},
	}
	fake.SetThreadUsage("t1", ThreadUsage{Total: TokenUsage{Input: 3700, Output: 956, Cached: 8400}})

	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("会话统计") })
	if tt.HasText("本会话共") {
		t.Fatalf("底部不再渲染「本会话共 …」整行: %q", tt.Texts())
	}
}

// TestTimelineRows_SessionUsageRowHiddenWithoutUsage 验证没有用量时不显示会话统计入口。
func TestTimelineRows_SessionUsageRowHiddenWithoutUsage(t *testing.T) {
	fake := NewFakeClient()
	fake.messages["t1"] = []Message{
		{ID: "u1", Role: "user", Text: "你好", TurnIndex: 1, IsFinal: true},
		{ID: "a1", Role: "assistant", Text: "世界", TurnIndex: 1, IsFinal: true},
	}

	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("世界") })
	if tt.HasText("会话统计") {
		t.Fatalf("没有用量时不应显示会话统计入口: %q", tt.Texts())
	}
}

// TestTimelineRows_FollowAnimationAndOffsetConfig 验证 Root 实际使用的 chatState 已禁用跟随动画且未开启 KeepOffset。
func TestTimelineRows_FollowAnimationAndOffsetConfig(t *testing.T) {
	fake := NewFakeClient()
	fake.messages["t1"] = []Message{
		{ID: "u1", Role: "user", Text: "Hi", TurnIndex: 1, IsFinal: true},
		{ID: "a1", Role: "assistant", Text: "Hello", TurnIndex: 1, IsFinal: true},
	}

	// 通过 Option 取得 Root 内部真实使用的 chatState
	var s *chatState
	tt := ui.NewTester(Root(fake, func(cs *chatState) { s = cs }), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("Hello") })
	if s == nil {
		t.Fatalf("未能取得 Root 使用的 chatState")
	}

	if s.chatListState.FollowAnimation != nil {
		t.Fatalf("FollowAnimation should be nil to prevent glideToEnd jumping on newline/expand")
	}
	if s.chatListState.KeepOffset {
		t.Fatalf("KeepOffset should be false to allow anchorKey tracking")
	}
	if !s.chatListState.FollowEnd {
		t.Fatalf("FollowEnd should be true for chat log following")
	}
}

// newTimelineTester 用给定消息启动 Root，并等待 ready 文本出现。
func newTimelineTester(t *testing.T, msgs []Message, ready string) *ui.Tester {
	t.Helper()
	fake := NewFakeClient()
	fake.messages["t1"] = msgs
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText(ready) })
	return tt
}

// clickText 点击文字所在元素并跑几帧，使状态变化落地。
func clickText(t *testing.T, tt *ui.Tester, text string) {
	t.Helper()
	if err := tt.Click(text); err != nil {
		t.Fatalf("click %q: %v", text, err)
	}
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
}

// hoverText 把指针移到文字所在元素上并跑几帧，使依赖整行 hover 的消息操作栏显示出来。
func hoverText(t *testing.T, tt *ui.Tester, text string) {
	t.Helper()
	r, ok := tt.Find(text)
	if !ok {
		t.Fatalf("no element shows %q", text)
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
}

// TestTimelineRows_ScrollUpStopsFollowing 验证用户上滚后视口不会被每帧拉回底部。
func TestTimelineRows_ScrollUpStopsFollowing(t *testing.T) {
	var msgs []Message
	for i := 0; i < 80; i++ {
		msgs = append(msgs, Message{ID: fmt.Sprintf("u%02d", i), Role: "user", Text: fmt.Sprintf("消息-%02d", i), TurnIndex: i + 1, IsFinal: true})
	}
	tt := newTimelineTester(t, msgs, "消息-79")

	tt.Scroll(600, 300, 0, -3000)
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	if tt.HasText("消息-79") {
		t.Fatalf("上滚后仍显示最后一条，视口被拉回底部: %q", tt.Texts())
	}
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	if tt.HasText("消息-79") {
		t.Fatalf("继续渲染几帧后视口被拉回底部")
	}

	// 滚回底部后恢复跟随
	tt.Scroll(600, 300, 0, 100000)
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	if !tt.HasText("消息-79") {
		t.Fatalf("滚回底部后应显示最后一条")
	}
}

// TestTimelineRows_ToolRowToggle 验证单条工具渲染成动作胶囊：点开胶囊在下方展开详情，再点一次收起。
func TestTimelineRows_ToolRowToggle(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "你好", TurnIndex: 1, IsFinal: true},
		{ID: "t1", Role: "tool", Text: "ls -la\nTOOL-DETAIL-BODY", TurnIndex: 1},
	}
	tt := newTimelineTester(t, msgs, "ls -la")
	if tt.HasText("TOOL-DETAIL-BODY") {
		t.Fatalf("初始不应显示工具详情")
	}

	// 点击胶囊本体
	clickText(t, tt, "ls -la")
	if !tt.HasText("TOOL-DETAIL-BODY") {
		t.Fatalf("点击胶囊后应展开详情")
	}
	// 再点一次收起
	clickText(t, tt, "ls -la")
	if tt.HasText("TOOL-DETAIL-BODY") {
		t.Fatalf("再次点击胶囊后应收起详情")
	}
}

// TestTimelineRows_ToolGroupToggle 验证工具组的展开与收起：展开后组内只剩“思考段落 + 胶囊行”的节拍，
// 不再有子组行与树干，两条命令直接以胶囊呈现。
func TestTimelineRows_ToolGroupToggle(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "你好", TurnIndex: 1, IsFinal: true},
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "先看看仓库状态\nBODY-MARKER", TurnIndex: 1},
		{ID: "t1", Role: "tool", Text: `Bash {"command":"git status"}`, TurnIndex: 1},
		{ID: "t2", Role: "tool", Text: `Bash {"command":"go build ./..."}`, TurnIndex: 1},
	}
	tt := newTimelineTester(t, msgs, "思考 · 命令")
	if tt.HasText("BODY-MARKER") {
		t.Fatalf("初始不应显示思考正文: %q", tt.Texts())
	}

	// 组展开：思考段落与它触发的命令胶囊一起出现
	clickText(t, tt, "思考 · 命令")
	if !tt.HasText("BODY-MARKER") {
		t.Fatalf("展开组后应显示思考正文: %q", tt.Texts())
	}
	if !tt.HasText("git status") || !tt.HasText("go build") {
		t.Fatalf("展开组后应直接列出两条命令胶囊: %q", tt.Texts())
	}
	// 组内不再有子组行
	if _, ok := tt.Find("运行 2 条命令"); ok {
		t.Fatalf("组内不应再出现子组行: %q", tt.Texts())
	}

	// 组收起：思考正文与胶囊一起消失
	clickText(t, tt, "收起")
	if tt.HasText("BODY-MARKER") {
		t.Fatalf("收起组后不应显示思考正文: %q", tt.Texts())
	}
	if _, ok := tt.Find("$ git status"); ok {
		t.Fatalf("收起组后不应显示工具胶囊: %q", tt.Texts())
	}
}

// findContaining 查找包含指定内容的完整文本节点。
func findContaining(tt *ui.Tester, sub string) (ui.Rect, bool) {
	for _, text := range tt.Texts() {
		if strings.Contains(text, sub) {
			return tt.Find(text)
		}
	}
	return ui.Rect{}, false
}

// TestTimelineRows_ToolBeatChips 验证展开后的工具组里动作胶囊竖排，点开胶囊在其下方展开详情，
// 且同一节拍同一时刻只展开一个。
func TestTimelineRows_ToolBeatChips(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "看看状态", TurnIndex: 1, IsFinal: true},
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "先看仓库状态", TurnIndex: 1},
		{ID: "t1", Role: "tool", Text: `Bash {"command":"git status"}` + "\nOUT-STATUS", TurnIndex: 1},
		{ID: "t2", Role: "tool", Text: `Bash {"command":"git diff"}` + "\nOUT-DIFF", TurnIndex: 1},
		{ID: "t3", Role: "tool", Text: `Bash {"command":"git log"}` + "\nOUT-LOG", TurnIndex: 1},
	}
	tt := newTimelineTester(t, msgs, "思考 · 命令")
	clickText(t, tt, "思考 · 命令")
	if !tt.HasText("先看仓库状态") {
		t.Fatalf("展开组后应显示思考叙事: %q", tt.Texts())
	}
	if tt.HasText("OUT-STATUS") || tt.HasText("OUT-DIFF") {
		t.Fatalf("胶囊未展开时不应显示详情: %q", tt.Texts())
	}
	first, ok1 := tt.Find("$ git status")
	middle, ok2 := tt.Find("$ git diff")
	last, ok3 := tt.Find("$ git log")
	if !ok1 || !ok2 || !ok3 || first.Y >= middle.Y || middle.Y >= last.Y || first.X != middle.X || middle.X != last.X {
		t.Fatalf("工具胶囊应左对齐并按顺序竖排: first=%+v middle=%+v last=%+v", first, middle, last)
	}

	clickText(t, tt, "$ git status")
	if !tt.HasText("OUT-STATUS") {
		t.Fatalf("点击胶囊后应展开详情: %q", tt.Texts())
	}

	// 同一节拍只展开一个：点开第二个胶囊时第一个自动收起
	clickText(t, tt, "$ git diff")
	if !tt.HasText("OUT-DIFF") || tt.HasText("OUT-STATUS") {
		t.Fatalf("同一节拍应只展开一个胶囊详情: %q", tt.Texts())
	}
	middle, ok2 = tt.Find("$ git diff")
	detail, okDetail := findContaining(tt, "OUT-DIFF")
	last, ok3 = tt.Find("$ git log")
	if !ok2 || !okDetail || !ok3 || middle.Y >= detail.Y || detail.Y >= last.Y {
		t.Fatalf("详情应位于对应胶囊与下一胶囊之间: middle=%+v detail=%+v last=%+v", middle, detail, last)
	}
	if detail.W <= 0 {
		t.Fatalf("详情文本宽度应大于零: detail=%+v", detail)
	}
	if detail.X-middle.X >= toolBeatPadLeft+2*Space4 {
		t.Fatalf("详情不应双重缩进: middle=%+v detail=%+v", middle, detail)
	}

	// 再点一次收起
	clickText(t, tt, "$ git diff")
	if tt.HasText("OUT-DIFF") {
		t.Fatalf("再次点击胶囊后应收起详情: %q", tt.Texts())
	}
}

// TestToolBeatReasoningNeedsTruncation 验证节拍内思考叙事的折叠阈值：超过 2 行或超过 180 个字符才折叠。
func TestToolBeatReasoningNeedsTruncation(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"单行短文本", "先看看仓库状态", false},
		{"两行", "第一行\n第二行", false},
		{"三行", "第一行\n第二行\n第三行", true},
		{"刚好 180 个字符", strings.Repeat("想", 180), false},
		{"超长单行", strings.Repeat("想", 181), true},
		{"首尾空白不计入行数", "\n 只有一行 \n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolBeatReasoningNeedsTruncation(tc.text); got != tc.want {
				t.Fatalf("toolBeatReasoningNeedsTruncation(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestTimelineRows_ReasoningToggle 验证点击思考行后正文出现。
func TestTimelineRows_ReasoningToggle(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "你好", TurnIndex: 1, IsFinal: true},
		// 正文单独占一行：标题会取首行做要旨，这里用它验证正文只在展开后出现。
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "先想一想\nREASONING-BODY", TurnIndex: 1, IsFinal: true},
	}
	tt := newTimelineTester(t, msgs, "思考过程 · 先想一想")
	if tt.HasText("REASONING-BODY") {
		t.Fatalf("初始不应显示思考正文")
	}
	clickText(t, tt, "思考过程 · 先想一想")
	if !tt.HasText("REASONING-BODY") {
		t.Fatalf("点击后应显示思考正文")
	}
	clickText(t, tt, "思考过程 · 先想一想")
	if tt.HasText("REASONING-BODY") {
		t.Fatalf("再次点击后应收起思考正文")
	}
}

// TestTimelineRows_AssistantCopyButton 验证助手回复生成完毕后出现复制按钮，点击后内容写入系统剪贴板。
func TestTimelineRows_AssistantCopyButton(t *testing.T) {
	msgs := []Message{
		{ID: "a1", Role: "assistant", Text: "这是一条测试助手的完整回复内容", TurnIndex: 1, IsFinal: true},
	}
	tt := newTimelineTester(t, msgs, "这是一条测试助手的完整回复内容")
	if !tt.HasText("复制") {
		t.Fatalf("期望助手回复下方出现'复制'按钮")
	}

	if err := tt.Click("复制"); err != nil {
		t.Fatalf("点击'复制'按钮失败: %v", err)
	}

	if got := tt.Clipboard(); got != "这是一条测试助手的完整回复内容" {
		t.Fatalf("剪贴板内容不符合预期: got %q, want %q", got, "这是一条测试助手的完整回复内容")
	}
}

// TestTimelineRows_UserMessageActions 验证用户消息行下方渲染复制与修改按钮，未 hover 时按钮不可见，
// hover 该行后点击「修改」进入就地编辑。
func TestTimelineRows_UserMessageActions(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "这条提问需要改", TurnIndex: 1, IsFinal: true},
		{ID: "a1", Role: "assistant", Text: "好的", TurnIndex: 1, IsFinal: true},
	}
	tt := newTimelineTester(t, msgs, "这条提问需要改")
	if !tt.HasText("这条提问需要改") {
		t.Fatalf("用户消息文字应可见: %q", tt.Texts())
	}

	// 未 hover 时操作栏只把不透明度置零，文字仍占据原来的位置
	if _, ok := tt.Find("修改"); !ok {
		t.Fatalf("用户消息下方应保留复制与修改按钮的占位: %q", tt.Texts())
	}

	hoverText(t, tt, "这条提问需要改")
	if err := tt.Click("修改"); err != nil {
		t.Fatalf("点击'修改'按钮失败: %v", err)
	}
	// 点击后原位换成就地编辑输入框，操作栏随行消失
	waitFor(t, tt, func() bool { return tt.HasText("Esc 取消 · ⌘Enter 发送") })
	if tt.HasText("修改") {
		t.Fatalf("就地编辑中不应再显示操作栏: %q", tt.Texts())
	}
}

// TestShouldShowAssistantActions 验证只有同一轮里最后一条助手回复才带操作栏，同轮中间、
// 流式中与正文为空的回复都不渲染。
func TestShouldShowAssistantActions(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "先查一下再回答", TurnIndex: 1},
		{ID: "a1", Role: "assistant", Text: "我先看看这个仓库", TurnIndex: 1},
		{ID: "t1", Role: "tool", Text: "ls", TurnIndex: 1},
		{ID: "a2", Role: "assistant", Text: "结论是……", TurnIndex: 1, IsFinal: true},
		{ID: "a3", Role: "assistant", Text: "第二轮的答复", TurnIndex: 2, IsFinal: true},
		{ID: "a4", Role: "assistant", Text: "  ", TurnIndex: 3, IsFinal: true},
	}

	// 与 chat.go 的 lastAssistantPerTurn 同构：每轮记录最后一条助手消息
	lastAssistant := make(map[int]string)
	for _, m := range msgs {
		if m.Role == "assistant" {
			lastAssistant[m.TurnIndex] = m.ID
		}
	}

	cases := []struct {
		id        string
		streaming bool
		revealed  bool
		want      bool
	}{
		{"a1", false, true, false},  // 同轮里后面还有一条助手回复
		{"a2", false, true, true},   // 本轮的最终回复
		{"a2", true, true, false},   // 流式中不显示
		{"a2", false, false, false}, // 正文还没完全显示
		{"a3", false, true, true},   // 下一轮的最后一条回复
		{"a4", false, true, false},  // 正文只有空白
	}
	for _, tc := range cases {
		var msg Message
		for _, m := range msgs {
			if m.ID == tc.id {
				msg = m
				break
			}
		}
		isLast := lastAssistant[msg.TurnIndex] == msg.ID
		if got := shouldShowAssistantActions(msg, tc.streaming, isLast, tc.revealed); got != tc.want {
			t.Errorf("shouldShowAssistantActions(%s, streaming=%v, isLast=%v, revealed=%v) = %v, want %v", tc.id, tc.streaming, isLast, tc.revealed, got, tc.want)
		}
	}
}

// TestMessageActionBarVisible 验证操作栏默认隐藏，hover 消息行或复制反馈窗口内才显示。
func TestMessageActionBarVisible(t *testing.T) {
	if messageActionBarVisible(false, false) {
		t.Fatalf("未 hover 且没有复制反馈时应隐藏操作栏")
	}
	if !messageActionBarVisible(true, false) {
		t.Fatalf("hover 消息行后应显示操作栏")
	}
	if !messageActionBarVisible(false, true) {
		t.Fatalf("复制反馈窗口内应保持操作栏可见")
	}
}

// TestTimelineRows_SwarmReportItem 验证 Swarm 汇报消息被识别并生成 timelineItemSwarmReport，普通消息与非 user 汇报消息不受影响。
func TestTimelineRows_SwarmReportItem(t *testing.T) {
	msgs := []Message{
		{ID: "m1", Role: "user", Text: "[Hydra] 协作者汇报：Ives 已结束工作。\n\n## Ives：Fix (已完成)", TurnIndex: 1},
		{ID: "m2", Role: "user", Text: "这是一条普通的 user 消息", TurnIndex: 1},
		{ID: "m3", Role: "assistant", Text: "[编排] 子代理汇报：Dune 已结束工作。", TurnIndex: 2},
		{ID: "m4", Role: "user", Text: "   \n\t  [编排] 子代理汇报：前缀前有空白也能识别", TurnIndex: 3},
		{ID: "m5", Role: "user", Text: "[编排] 普通通知", TurnIndex: 4},
		{ID: "m6", Role: "user", Text: "示例：[编排] 子代理汇报：不在开头", TurnIndex: 4},
		{ID: "m7", Role: "user", Text: "[编排] 子代理汇报：Ives 已结束工作。", TurnIndex: 5},
	}

	items := buildTimelineItems(msgs)
	if len(items) != len(msgs) {
		t.Fatalf("expected %d items, got %d", len(msgs), len(items))
	}

	// 1. 旧前缀 [Hydra] 协作者汇报：... 开头的 user 消息仍识别为 timelineItemSwarmReport（兼容历史会话）
	if items[0].kind != timelineItemSwarmReport {
		t.Errorf("item 0 kind = %v, want timelineItemSwarmReport", items[0].kind)
	}

	// 2. 普通 user 消息仍为 timelineItemSingle
	if items[1].kind != timelineItemSingle {
		t.Errorf("item 1 kind = %v, want timelineItemSingle", items[1].kind)
	}

	// 3. role 为 assistant 但带相同前缀的消息仍为 timelineItemSingle
	if items[2].kind != timelineItemSingle {
		t.Errorf("item 2 kind = %v, want timelineItemSingle", items[2].kind)
	}

	// 4. 前缀前有空白也能识别
	if items[3].kind != timelineItemSwarmReport {
		t.Errorf("item 3 kind = %v, want timelineItemSwarmReport", items[3].kind)
	}
	for _, i := range []int{4, 5} {
		if items[i].kind != timelineItemSingle {
			t.Errorf("item %d 不应识别为汇报", i)
		}
	}
	// 5. 新前缀的汇报同样识别
	if items[6].kind != timelineItemSwarmReport {
		t.Errorf("item 6 kind = %v, want timelineItemSwarmReport", items[6].kind)
	}
}

// TestTimelineRows_SwarmReportsMergeIntoOneRow 验证相邻的 Swarm 汇报消息合并成一个时间线条目，
// 中间夹其它消息时不合并。
func TestTimelineRows_SwarmReportsMergeIntoOneRow(t *testing.T) {
	msgs := []Message{
		{ID: "r1", Role: "user", Text: "[编排] 子代理汇报：Ives 已结束工作。", TurnIndex: 1},
		{ID: "r2", Role: "user", Text: "[编排] 子代理汇报：Dune 已结束工作。", TurnIndex: 1},
		{ID: "a1", Role: "assistant", Text: "收到", TurnIndex: 1},
		{ID: "r3", Role: "user", Text: "[编排] 子代理汇报：Cove 已结束工作。", TurnIndex: 2},
	}

	items := buildTimelineItems(msgs)
	if len(items) != 3 {
		t.Fatalf("相邻汇报应合并，expected 3 items, got %d: %+v", len(items), items)
	}
	if items[0].kind != timelineItemSwarmReport || items[0].id != "r1" || len(items[0].reportMsgs) != 2 {
		t.Fatalf("相邻汇报未合并成一个条目: %+v", items[0])
	}
	if items[0].reportMsgs[0].ID != "r1" || items[0].reportMsgs[1].ID != "r2" {
		t.Fatalf("合并顺序错误: %+v", items[0].reportMsgs)
	}
	if items[1].kind != timelineItemSingle {
		t.Fatalf("普通消息不应被合并: %+v", items[1])
	}
	if items[2].kind != timelineItemSwarmReport || len(items[2].reportMsgs) != 1 {
		t.Fatalf("被其它消息隔开的汇报不应合并: %+v", items[2])
	}
}

// TestTimelineRows_ShouldCollapseUserText 验证用户长消息折叠判断规则（> 8 行或 > 600 个字符）。
func TestTimelineRows_ShouldCollapseUserText(t *testing.T) {
	// 1. 空文本与短单行不折叠
	if shouldCollapseUserText("", userCollapseLines, userCollapseRunes) {
		t.Errorf("empty text should not collapse")
	}
	if shouldCollapseUserText("短问题", userCollapseLines, userCollapseRunes) {
		t.Errorf("short text should not collapse")
	}

	// 2. 刚好 8 行不折叠，9 行折叠
	eightLines := "1\n2\n3\n4\n5\n6\n7\n8"
	if shouldCollapseUserText(eightLines, userCollapseLines, userCollapseRunes) {
		t.Errorf("8 lines should not collapse")
	}
	nineLines := "1\n2\n3\n4\n5\n6\n7\n8\n9"
	if !shouldCollapseUserText(nineLines, userCollapseLines, userCollapseRunes) {
		t.Errorf("9 lines should collapse")
	}

	// 3. 字符数边界：刚好 600 字符不折叠，601 字符折叠
	sixHundredChars := strings.Repeat("字", 600)
	if shouldCollapseUserText(sixHundredChars, userCollapseLines, userCollapseRunes) {
		t.Errorf("600 runes should not collapse")
	}
	sixHundredOneChars := strings.Repeat("字", 601)
	if !shouldCollapseUserText(sixHundredOneChars, userCollapseLines, userCollapseRunes) {
		t.Errorf("601 runes should collapse")
	}

	// 4. 预览截断验证：行数截断与超长截断，预览不再补省略号
	previewLines := userPromptPreviewWithLimits(nineLines, userCollapseLines, userCollapseRunes)
	if strings.Contains(previewLines, "9") {
		t.Errorf("preview of 9 lines should not include 9th line, got %q", previewLines)
	}
	if len(strings.Split(previewLines, "\n")) != 8 {
		t.Errorf("expected 8 lines in preview, got %d", len(strings.Split(previewLines, "\n")))
	}

	previewLong := userPromptPreviewWithLimits(sixHundredOneChars, userCollapseLines, userCollapseRunes)
	if strings.Contains(previewLong, "...") {
		t.Errorf("preview 不再补省略号, got %q", previewLong)
	}
	if n := len([]rune(previewLong)); n > 600 {
		t.Errorf("preview 超过 600 字符: %d", n)
	}

	// 5. 按字数截断时退到词边界，不出现半个词
	words := strings.Repeat("word ", 200)
	for _, tok := range strings.Fields(userPromptPreviewWithLimits(words, userCollapseLines, userCollapseRunes)) {
		if tok != "word" {
			t.Errorf("预览应在词边界截断，出现半截词 %q", tok)
			break
		}
	}
}

// TestTimelineRows_UserHiddenLineCount 验证折叠后隐藏行数的计算。
func TestTimelineRows_UserHiddenLineCount(t *testing.T) {
	// 1. 9 行折成 8 行：藏了 1 行
	nineLines := "1\n2\n3\n4\n5\n6\n7\n8\n9"
	preview := userPromptPreviewWithLimits(nineLines, userCollapseLines, userCollapseRunes)
	if got := userHiddenLineCount(nineLines, preview); got != 1 {
		t.Errorf("userHiddenLineCount = %d, want 1", got)
	}

	// 2. 只因字数折叠：行数没变，隐藏行数为 0
	longLine := strings.Repeat("字", 601)
	if got := userHiddenLineCount(longLine, userPromptPreviewWithLimits(longLine, userCollapseLines, userCollapseRunes)); got != 0 {
		t.Errorf("userHiddenLineCount = %d, want 0", got)
	}

	// 3. 预览反而比原文长时按 0 处理
	if got := userHiddenLineCount("短", "更长的预览\n第二行"); got != 0 {
		t.Errorf("userHiddenLineCount = %d, want 0", got)
	}
}

// TestTimelineRows_UserCollapseLimits 验证子代理嵌入会话用更小的折叠阈值（5 行 / 400 字）。
func TestTimelineRows_UserCollapseLimits(t *testing.T) {
	// 1. 普通会话 8 / 600
	if lines, runes := userCollapseLimits(nil); lines != 8 || runes != 600 {
		t.Errorf("userCollapseLimits(nil) = %d, %d, want 8, 600", lines, runes)
	}
	if lines, runes := userCollapseLimits(&chatState{}); lines != 8 || runes != 600 {
		t.Errorf("userCollapseLimits(普通会话) = %d, %d, want 8, 600", lines, runes)
	}

	// 2. embeddedBee（子代理浮动面板 / 独立窗口里的嵌入会话）5 / 400
	lines, runes := userCollapseLimits(&chatState{embeddedBee: true})
	if lines != 5 || runes != 400 {
		t.Fatalf("userCollapseLimits(嵌入会话) = %d, %d, want 5, 400", lines, runes)
	}

	// 3. 6 行在嵌入会话里折叠，在普通会话里不折叠
	sixLines := "1\n2\n3\n4\n5\n6"
	if !shouldCollapseUserText(sixLines, lines, runes) {
		t.Errorf("嵌入会话里 6 行应折叠")
	}
	if shouldCollapseUserText(sixLines, userCollapseLines, userCollapseRunes) {
		t.Errorf("普通会话里 6 行不应折叠")
	}

	// 4. 401 字在嵌入会话里折叠，400 字不折叠
	if !shouldCollapseUserText(strings.Repeat("字", 401), lines, runes) {
		t.Errorf("嵌入会话里 401 字应折叠")
	}
	if shouldCollapseUserText(strings.Repeat("字", 400), lines, runes) {
		t.Errorf("嵌入会话里 400 字不应折叠")
	}

	// 5. 嵌入会话的预览只取前 5 行
	if got := len(strings.Split(userPromptPreviewWithLimits("1\n2\n3\n4\n5\n6\n7", lines, runes), "\n")); got != 5 {
		t.Errorf("嵌入会话预览行数 = %d, want 5", got)
	}
}

// TestTimelineRows_ParseCommandToolInfo 验证各类命令工具信息的解析（正文截断、退出码、耗时）。
func TestTimelineRows_ParseCommandToolInfo(t *testing.T) {
	// 1. 非命令类工具返回 IsCommand = false
	nonCmd := Message{Role: "tool", Text: `Edit {"file_path":"/repo/main.go"}`}
	if info := parseCommandToolInfo(nonCmd); info.IsCommand {
		t.Errorf("Edit tool should not be command tool")
	}

	// 2. bash 工具提取 command 字段
	bashMsg := Message{Role: "tool", Text: `bash {"command":"git status --short"}`}
	infoBash := parseCommandToolInfo(bashMsg)
	if !infoBash.IsCommand {
		t.Fatalf("bash tool must be identified as command")
	}
	if infoBash.Command != "git status --short" {
		t.Errorf("expected command 'git status --short', got %q", infoBash.Command)
	}
	if infoBash.ExitCode != nil {
		t.Errorf("expected nil exit code, got %v", *infoBash.ExitCode)
	}
	if infoBash.Duration != "" {
		t.Errorf("expected empty duration, got %q", infoBash.Duration)
	}

	// 3. exec_command 提取 CommandLine、退出码 0 与耗时（秒）
	execMsg := Message{Role: "tool", Text: "exec_command {\"CommandLine\":\"go test ./...\"}\n{\"exit_code\":0,\"duration\":1.234}"}
	infoExec := parseCommandToolInfo(execMsg)
	if !infoExec.IsCommand {
		t.Fatalf("exec_command must be identified as command")
	}
	if infoExec.Command != "go test ./..." {
		t.Errorf("expected command 'go test ./...', got %q", infoExec.Command)
	}
	if infoExec.ExitCode == nil || *infoExec.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %v", infoExec.ExitCode)
	}
	if infoExec.Duration != "1.2s" {
		t.Errorf("expected duration '1.2s', got %q", infoExec.Duration)
	}

	// 4. 退出码非 0 与毫秒耗时解析
	failMsg := Message{Role: "tool", Text: "shell {\"cmd\":\"rm -rf /root\"}\n{\"exitCode\":1,\"duration_ms\":2500}"}
	infoFail := parseCommandToolInfo(failMsg)
	if !infoFail.IsCommand {
		t.Fatalf("shell must be identified as command")
	}
	if infoFail.Command != "rm -rf /root" {
		t.Errorf("expected command 'rm -rf /root', got %q", infoFail.Command)
	}
	if infoFail.ExitCode == nil || *infoFail.ExitCode != 1 {
		t.Errorf("expected exit code 1, got %v", infoFail.ExitCode)
	}
	if infoFail.Duration != "2.5s" {
		t.Errorf("expected duration '2.5s', got %q", infoFail.Duration)
	}

	// 5. 文本形式输出中解析 exit status 与耗时
	textMsg := Message{Role: "tool", Text: "command ping google.com\n64 bytes from 8.8.8.8 (exit 0) took 0.8s"}
	infoText := parseCommandToolInfo(textMsg)
	if !infoText.IsCommand {
		t.Fatalf("command tool must be identified as command")
	}
	if infoText.ExitCode == nil || *infoText.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %v", infoText.ExitCode)
	}
	if infoText.Duration != "0.8s" {
		t.Errorf("expected duration '0.8s', got %q", infoText.Duration)
	}

	// 6. MCP 带前缀命令工具识别
	mcpMsg := Message{Role: "tool", Text: `mcp__terminal__run_command {"command":"cargo build"}`}
	infoMCP := parseCommandToolInfo(mcpMsg)
	if !infoMCP.IsCommand {
		t.Fatalf("mcp terminal command must be identified as command")
	}
	if infoMCP.Command != "cargo build" {
		t.Errorf("expected command 'cargo build', got %q", infoMCP.Command)
	}

	// 7. 超长命令单行截断（>80 字符后附带 ...）
	longCmd := strings.Repeat("x", 120)
	longMsg := Message{Role: "tool", Text: `bash {"command":"` + longCmd + `"}`}
	infoLong := parseCommandToolInfo(longMsg)
	if !strings.HasSuffix(infoLong.Command, "...") {
		t.Errorf("long command should end with ..., got %q", infoLong.Command)
	}
	if len([]rune(infoLong.Command)) > 83 {
		t.Errorf("long command runes length should not exceed 83, got %d", len([]rune(infoLong.Command)))
	}
}

// TestTimelineRows_UserMessageFoldingAndAttachments 验证长提问折叠展开与附件缩略图展示。
func TestTimelineRows_UserMessageFoldingAndAttachments(t *testing.T) {
	// 验证折叠切换状态函数
	msgID := "test-msg-toggle-id"
	if isUserMessageExpanded(msgID) {
		t.Fatalf("initially should not be expanded")
	}
	toggleUserMessageExpanded(nil, msgID)
	if !isUserMessageExpanded(msgID) {
		t.Fatalf("after toggle should be expanded")
	}
	toggleUserMessageExpanded(nil, msgID)
	if isUserMessageExpanded(msgID) {
		t.Fatalf("after second toggle should be collapsed")
	}

	// 验证图片媒体识别
	img1 := MessageMedia{Name: "screenshot.png", MimeType: "image/png"}
	if !isImageMedia(img1) {
		t.Errorf("image/png should be recognized as image")
	}
	img2 := MessageMedia{Name: "photo.jpg"}
	if !isImageMedia(img2) {
		t.Errorf(".jpg should be recognized as image")
	}
	pdf := MessageMedia{Name: "doc.pdf", MimeType: "application/pdf"}
	if isImageMedia(pdf) {
		t.Errorf("pdf should not be recognized as image")
	}
}

// TestTimelineRows_UserMessageExpandedStaysInBubble 验证含超宽表格行、无空格长标识符的长提问
// 展开后正文仍在气泡内部折行：正文布局宽度不超过气泡 MaxWidth，不会按固有宽度向左溢出气泡。
func TestTimelineRows_UserMessageExpandedStaysInBubble(t *testing.T) {
	const winW, winH = 900, 700

	fake := NewFakeClient()
	s := newChatState(fake)
	bubbleMaxWidth := userBubbleMaxWidth(s) - 2*Space5

	// 触发溢出的两类行：单元格里带空格的很宽表格行，以及 min-content 极大、没有空格的
	// 表格分隔行与长标识符（气泡是 AlignItems(End) 的列，子元素按 min-content 取固有宽度）。
	wideRow := "| P0 | 这是一段很长的表格单元格内容，用来把表格行撑得很宽 | " + strings.Repeat("再补一段单元格文字 ", 12) + " |"
	separator := "|" + strings.Repeat("-", 400) + "|" + strings.Repeat("-", 400) + "|"
	longToken := "internal/ui/" + strings.Repeat("timeline_rows_", 40)
	text := strings.Join([]string{
		"请按下面的表格逐条核对这次改动：",
		"| 优先级 | 说明 | 备注 |",
		"| --- | --- | --- |",
		wideRow,
		separator,
		"标识符：" + longToken,
		wideRow,
		"最后再补一行说明。",
	}, "\n")
	if !shouldCollapseUserText(text, userCollapseLines, userCollapseRunes) {
		t.Fatalf("测试用的长提问应当默认折叠，实际 %d 行、%d 字", strings.Count(text, "\n")+1, utf8.RuneCountInString(text))
	}

	msg := Message{ID: "u-wide-table", Role: "user", Text: text, TurnIndex: 1, IsFinal: true}
	// 直接置为展开态：这里验证的是展开后的排版，不是点击切换本身。
	toggleUserMessageExpanded(nil, msg.ID)
	t.Cleanup(func() {
		if isUserMessageExpanded(msg.ID) {
			toggleUserMessageExpanded(nil, msg.ID)
		}
	})

	tt := ui.NewTester(func(c *ui.Context) {
		renderUserMessage(c, s, msg)
	}, winW, winH)

	var rect ui.Rect
	waitFor(t, tt, func() bool {
		var ok bool
		rect, ok = tt.Find(text)
		return ok
	})
	if rect.W > bubbleMaxWidth {
		t.Errorf("展开后正文布局宽度 = %.1f，超过气泡 MaxWidth %.0f", rect.W, bubbleMaxWidth)
	}
	if rect.X < 0 || rect.X+rect.W > winW {
		t.Errorf("展开后正文横向越界：X=%.1f W=%.1f，窗口宽 %d", rect.X, rect.W, winW)
	}
}

// TestTimelineRows_ShowReasoningFiltering 验证 ShowReasoning 设置为 false 时，思考行被过滤不渲染。
func TestTimelineRows_ShowReasoningFiltering(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	dataDir := settingsTestDir(t)

	reasoningMsg := Message{
		ID:        "r1",
		Role:      "assistant",
		Kind:      "reasoning",
		Text:      "Thinking about life...",
		TurnIndex: 1,
	}
	item := timelineItem{
		kind:      timelineItemSingle,
		msg:       reasoningMsg,
		id:        reasoningMsg.ID,
		turnIndex: 1,
	}

	// 1. 默认或开启状态
	st := NewSettingsState(dataDir, nil, s)
	bTrue := true
	st.cfg.Session.ShowReasoning = &bTrue
	setChatSettings(s, st)

	var renderedOpen ui.Element
	ttOpen := ui.NewTester(func(c *ui.Context) {
		renderedOpen = renderTimelineItem(c, s, item, "", "", false, false, TokenUsage{}, 0)
	}, 800, 600)
	if renderedOpen == (ui.Element{}) {
		t.Fatalf("expected reasoning row to be rendered when ShowReasoning is enabled")
	}
	if !ttOpen.HasText("思考过程") {
		t.Fatalf("expected reasoning row to render '思考过程' when ShowReasoning is enabled")
	}

	// 2. 关闭状态
	bFalse := false
	st.cfg.Session.ShowReasoning = &bFalse
	var renderedClosed ui.Element
	ttClosed := ui.NewTester(func(c *ui.Context) {
		renderedClosed = renderTimelineItem(c, s, item, "", "", false, false, TokenUsage{}, 0)
	}, 800, 600)
	if renderedClosed != (ui.Element{}) {
		t.Fatalf("expected reasoning row to be filtered (zero Element) when ShowReasoning is disabled, got %v", renderedClosed)
	}
	if ttClosed.HasText("思考过程") {
		t.Fatalf("expected reasoning row to be hidden when ShowReasoning is disabled")
	}

	// 3. renderMessageItem 直接调用校验
	var msgRenderedClosed ui.Element
	ui.NewTester(func(c *ui.Context) {
		msgRenderedClosed = renderMessageItem(c, s, reasoningMsg, "", "", false, false, TokenUsage{}, 0, false)
	}, 800, 600)
	if msgRenderedClosed != (ui.Element{}) {
		t.Fatalf("expected renderMessageItem to return zero Element for reasoning when ShowReasoning is disabled")
	}
}

// TestReasoningHeaderLabel 验证思考行标题在流式与结束后的文案。
func TestReasoningHeaderLabel(t *testing.T) {
	if got := reasoningHeaderLabel(true); got != "思考中…" {
		t.Fatalf("reasoningHeaderLabel(true) = %q, want 思考中…", got)
	}
	if got := reasoningHeaderLabel(false); got != "思考过程" {
		t.Fatalf("reasoningHeaderLabel(false) = %q, want 思考过程", got)
	}
}

// TestToolGroupSummary 验证工具组摘要按类型统计、计数为 0 的类别省略。
func TestToolGroupSummary(t *testing.T) {
	cases := []struct {
		name  string
		items []Message
		want  string
	}{
		{
			name: "混合类型",
			items: []Message{
				{Role: "assistant", Kind: "reasoning", Text: "先想一想"},
				{Role: "assistant", Kind: "reasoning", Text: "再想一想"},
				{Role: "tool", Text: `Bash {"command":"go build ./..."}`},
				{Role: "tool", Text: `exec {"cmd":"ls -la"}`},
				{Role: "tool", Text: `Edit {"file_path":"a.go"}`},
				{Role: "tool", Text: `write_file {"path":"b.go"}`},
				{Role: "tool", Text: `apply_patch {"input":"*** Begin Patch"}`},
				{Role: "tool", Text: `Read {"file_path":"a.go"}`},
				{Role: "tool", Text: `Grep {"pattern":"todo"}`},
			},
			want: "思考 2 次 · 运行 2 条命令 · 编辑 3 个文件 · 读取 1 个文件 · 搜索 1 次",
		},
		{
			name:  "只统计出现过的类别",
			items: []Message{{Role: "tool", Text: `Glob {"pattern":"**/*.go"}`}},
			want:  "搜索 1 次",
		},
		{
			name:  "带 MCP 前缀的读取工具",
			items: []Message{{Role: "tool", Text: `mcp__fs__read_file {"path":"a.go"}`}},
			want:  "读取 1 个文件",
		},
		{
			name:  "未知工具单独计数",
			items: []Message{{Role: "tool", Text: `mcp__foo__bar {"x":1}`}},
			want:  "调用 1 个工具",
		},
		{
			name:  "空列表回退到计数",
			items: nil,
			want:  "已运行 0 个工具",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolGroupSummary(tc.items); got != tc.want {
				t.Fatalf("toolGroupSummary() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildTimelineItemsCollapsesTodo(t *testing.T) {
	first := todoToolMsg("p1", `{"plan":[{"step":"一","status":"pending"}]}`)
	second := todoToolMsg("p2", `{"plan":[{"step":"二","status":"completed"}]}`)
	first.ToolName, second.ToolName = "update_plan", "update_plan"
	items := buildTimelineItems([]Message{{Role: "user", ID: "u"}, first, second})
	if len(items) != 2 || items[1].msg.ID != "p2" {
		t.Fatalf("时间线条目 = %+v", items)
	}
	if todos, ok := todoItemsFromMessage(items[1].msg); !ok || len(todos) != 1 || todos[0].Text != "二" {
		t.Fatalf("待办清单 = %+v", todos)
	}
}

// wantTimelineItem 描述 buildTimelineItems 期望生成的一个时间线项。
type wantTimelineItem struct {
	kind          timelineItemKind
	id            string   // 单条项的消息 ID，或节拍项内思考消息的 ID（无前置思考的节拍为空）
	tools         []string // 工具组项内按顺序的消息 ID
	beatTools     []string // 节拍内由思考触发的工具消息 ID
	groupExpanded bool     // 工具组项是否处于展开状态
	msgIndex      int      // 对应消息在 messages 中的下标
	isFirst       bool
	isLast        bool
}

// messageIDs 把消息列表压成逗号分隔的 ID 串，方便比对。
func messageIDs(messages []Message) string {
	ids := make([]string, len(messages))
	for i, m := range messages {
		ids[i] = m.ID
	}
	return strings.Join(ids, ",")
}

// timelineItemIDs 把构建结果压成便于排查的短字符串。
func timelineItemIDs(items []timelineItem) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = fmt.Sprintf("%d:%s@%d", it.kind, it.id, it.msgIndex)
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// checkTimelineItems 逐项比对构建结果与期望。
func checkTimelineItems(t *testing.T, items []timelineItem, want []wantTimelineItem) {
	t.Helper()
	if len(items) != len(want) {
		t.Fatalf("项数 = %d, want %d, 实际 %s", len(items), len(want), timelineItemIDs(items))
	}
	for i, w := range want {
		it := items[i]
		if it.kind != w.kind {
			t.Errorf("items[%d].kind = %v, want %v", i, it.kind, w.kind)
			continue
		}
		if it.msgIndex != w.msgIndex {
			t.Errorf("items[%d].msgIndex = %d, want %d", i, it.msgIndex, w.msgIndex)
		}
		switch w.kind {
		case timelineItemToolGroup:
			if it.groupExpanded != w.groupExpanded {
				t.Errorf("items[%d].groupExpanded = %v, want %v", i, it.groupExpanded, w.groupExpanded)
			}
			if got := messageIDs(it.tools); got != strings.Join(w.tools, ",") {
				t.Errorf("items[%d].tools = %v, want %v", i, got, w.tools)
			}
		case timelineItemToolBeat:
			if it.msg.ID != w.id || it.isFirst != w.isFirst || it.isLast != w.isLast {
				t.Errorf("items[%d] = {thought:%q isFirst:%v isLast:%v}, want {thought:%q isFirst:%v isLast:%v}",
					i, it.msg.ID, it.isFirst, it.isLast, w.id, w.isFirst, w.isLast)
			}
			if got := messageIDs(it.beatTools); got != strings.Join(w.beatTools, ",") {
				t.Errorf("items[%d].beatTools = %v, want %v", i, got, w.beatTools)
			}
		default:
			if it.msg.ID != w.id {
				t.Errorf("items[%d].msg.ID = %q, want %q", i, it.msg.ID, w.id)
			}
		}
	}
}

// TestGroupSegments 验证节拍的分段规则：一条思考消息加上它后面紧跟的所有连续工具调用为一段，
// 组开头没有前置思考的工具单独成段，连续两条思考各成一段，组末尾只有思考也单独成段。
func TestGroupSegments(t *testing.T) {
	reasoning := func(id string) Message {
		return Message{ID: id, Role: "assistant", Kind: "reasoning", Text: "想 " + id, TurnIndex: 1}
	}
	tool := func(id string) Message {
		return Message{ID: id, Role: "tool", Text: `Bash {"command":"ls"}`, TurnIndex: 1}
	}
	cases := []struct {
		name string
		run  []Message
		want [][2]int
	}{
		{"开头是无思考的工具", []Message{tool("t1"), tool("t2"), reasoning("r1"), tool("t3")}, [][2]int{{0, 2}, {2, 4}}},
		{"思考后跟多个工具", []Message{reasoning("r1"), tool("t1"), tool("t2")}, [][2]int{{0, 3}}},
		{"连续两条思考", []Message{reasoning("r1"), reasoning("r2"), tool("t1")}, [][2]int{{0, 1}, {1, 3}}},
		{"组末尾只有思考", []Message{reasoning("r1"), tool("t1"), reasoning("r2")}, [][2]int{{0, 2}, {2, 3}}},
		{"思考后没有工具", []Message{reasoning("r1")}, [][2]int{{0, 1}}},
		{"全是工具", []Message{tool("t1"), tool("t2")}, [][2]int{{0, 2}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			segs := groupSegments(tc.run)
			if len(segs) != len(tc.want) {
				t.Fatalf("段数 = %d, want %d（%+v）", len(segs), len(tc.want), segs)
			}
			for i, w := range tc.want {
				if segs[i].start != w[0] || segs[i].end != w[1] {
					t.Fatalf("segs[%d] = {%d, %d}, want {%d, %d}", i, segs[i].start, segs[i].end, w[0], w[1])
				}
			}
		})
	}
}

// TestBuildTimelineItems 验证思考与工具调用同回合合并成组、整组不拆出尾部、跨回合切开，
// 以及展开后按节拍生成组内行（一条思考加它触发的连续工具调用）的下标与首尾标记。
// 参考：groupSegments 的分段规则。
func TestBuildTimelineItems(t *testing.T) {
	reasoning := func(id string, turn int) Message {
		return Message{ID: id, Role: "assistant", Kind: "reasoning", Text: "想 " + id, TurnIndex: turn}
	}
	tool := func(id string, turn int) Message {
		return Message{ID: id, Role: "tool", Text: `Bash {"command":"ls"}`, TurnIndex: turn}
	}
	assistant := func(id string, turn int) Message {
		return Message{ID: id, Role: "assistant", Kind: "text", Text: "答 " + id, TurnIndex: turn}
	}

	cases := []struct {
		name     string
		messages []Message
		expanded map[string]bool
		want     []wantTimelineItem
	}{
		{
			name: "同回合思考与工具合并为一组",
			messages: []Message{
				reasoning("r1", 1), tool("t1", 1), reasoning("r2", 1), tool("t2", 1), reasoning("r3", 1),
				assistant("a1", 1),
			},
			want: []wantTimelineItem{
				{kind: timelineItemToolGroup, tools: []string{"r1", "t1", "r2", "t2", "r3"}, msgIndex: 0},
				{kind: timelineItemSingle, id: "a1", msgIndex: 5},
			},
		},
		{
			name:     "只有思考行时保持单条",
			messages: []Message{reasoning("r1", 1), assistant("a1", 1)},
			want: []wantTimelineItem{
				{kind: timelineItemSingle, id: "r1", msgIndex: 0},
				{kind: timelineItemSingle, id: "a1", msgIndex: 1},
			},
		},
		{
			name:     "助手之间的单条工具保持单条",
			messages: []Message{assistant("a1", 1), tool("t1", 2), assistant("a2", 2)},
			want: []wantTimelineItem{
				{kind: timelineItemSingle, id: "a1", msgIndex: 0},
				{kind: timelineItemSingle, id: "t1", msgIndex: 1},
				{kind: timelineItemSingle, id: "a2", msgIndex: 2},
			},
		},
		{
			name:     "流式末尾的活动行不再拆出单条",
			messages: []Message{reasoning("r1", 1), tool("t1", 1), reasoning("r2", 1)},
			want: []wantTimelineItem{
				{kind: timelineItemToolGroup, tools: []string{"r1", "t1", "r2"}, msgIndex: 0},
			},
		},
		{
			name:     "回合以思考行收尾时整组保留",
			messages: []Message{tool("t1", 1), tool("t2", 1), reasoning("r1", 1)},
			want: []wantTimelineItem{
				{kind: timelineItemToolGroup, tools: []string{"t1", "t2", "r1"}, msgIndex: 0},
			},
		},
		{
			name:     "跨回合的活动行在回合边界切开",
			messages: []Message{reasoning("r1", 1), tool("t1", 1), reasoning("r2", 2), tool("t2", 2)},
			want: []wantTimelineItem{
				{kind: timelineItemToolGroup, tools: []string{"r1", "t1"}, msgIndex: 0},
				{kind: timelineItemToolGroup, tools: []string{"r2", "t2"}, msgIndex: 2},
			},
		},
		{
			name: "展开后逐个思考各自成节拍",
			messages: []Message{
				reasoning("r1", 1), tool("t1", 1), reasoning("r2", 1), tool("t2", 1), reasoning("r3", 1),
				assistant("a1", 1),
			},
			expanded: map[string]bool{"toolgroup-r1": true},
			want: []wantTimelineItem{
				{kind: timelineItemToolGroup, tools: []string{"r1", "t1", "r2", "t2", "r3"}, groupExpanded: true, msgIndex: 0},
				{kind: timelineItemToolBeat, id: "r1", beatTools: []string{"t1"}, msgIndex: 0, isFirst: true},
				{kind: timelineItemToolBeat, id: "r2", beatTools: []string{"t2"}, msgIndex: 2},
				{kind: timelineItemToolBeat, id: "r3", msgIndex: 4, isLast: true},
				{kind: timelineItemSingle, id: "a1", msgIndex: 5},
			},
		},
		{
			name: "思考后紧跟的多个工具合成一个节拍",
			messages: []Message{
				reasoning("r1", 1), tool("t1", 1), tool("t2", 1), reasoning("r2", 1), tool("t3", 1),
				assistant("a1", 1),
			},
			expanded: map[string]bool{"toolgroup-r1": true},
			want: []wantTimelineItem{
				{kind: timelineItemToolGroup, tools: []string{"r1", "t1", "t2", "r2", "t3"}, groupExpanded: true, msgIndex: 0},
				{kind: timelineItemToolBeat, id: "r1", beatTools: []string{"t1", "t2"}, msgIndex: 0, isFirst: true},
				{kind: timelineItemToolBeat, id: "r2", beatTools: []string{"t3"}, msgIndex: 3, isLast: true},
				{kind: timelineItemSingle, id: "a1", msgIndex: 5},
			},
		},
		{
			name: "组开头没有思考的工具单独成节拍",
			messages: []Message{
				tool("t1", 1), tool("t2", 1), reasoning("r1", 1), tool("t3", 1),
				assistant("a1", 1),
			},
			expanded: map[string]bool{"toolgroup-t1": true},
			want: []wantTimelineItem{
				{kind: timelineItemToolGroup, tools: []string{"t1", "t2", "r1", "t3"}, groupExpanded: true, msgIndex: 0},
				{kind: timelineItemToolBeat, beatTools: []string{"t1", "t2"}, msgIndex: 0, isFirst: true},
				{kind: timelineItemToolBeat, id: "r1", beatTools: []string{"t3"}, msgIndex: 2, isLast: true},
				{kind: timelineItemSingle, id: "a1", msgIndex: 4},
			},
		},
		{
			name: "纯工具组展开后合成一个节拍",
			messages: []Message{
				tool("t1", 1), tool("t2", 1), tool("t3", 1),
				assistant("a1", 1),
			},
			expanded: map[string]bool{"toolgroup-t1": true},
			want: []wantTimelineItem{
				{kind: timelineItemToolGroup, tools: []string{"t1", "t2", "t3"}, groupExpanded: true, msgIndex: 0},
				{kind: timelineItemToolBeat, beatTools: []string{"t1", "t2", "t3"}, msgIndex: 0, isFirst: true, isLast: true},
				{kind: timelineItemSingle, id: "a1", msgIndex: 3},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checkTimelineItems(t, buildTimelineItems(tc.messages, tc.expanded), tc.want)
		})
	}
}

// TestItemHoldsTurnEnd 验证回合完成块挂在哪个时间线项上：单条消息、折叠的组头、展开后的末段节拍。
func TestItemHoldsTurnEnd(t *testing.T) {
	single := []Message{{ID: "a1", Role: "assistant", Kind: "text", Text: "好了", TurnIndex: 1}}
	items := buildTimelineItems(single)
	if len(items) != 1 {
		t.Fatalf("单条助手消息应生成一项，实际 %s", timelineItemIDs(items))
	}
	if !itemHoldsTurnEnd(items[0], map[int]string{1: "a1"}) {
		t.Fatalf("单条助手消息应承载回合完成块")
	}
	if itemHoldsTurnEnd(items[0], map[int]string{}) {
		t.Fatalf("没有本回合最后一条助手消息时不应承载完成块")
	}

	group := []Message{
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "先看看", TurnIndex: 1},
		{ID: "t1", Role: "tool", Text: `Bash {"command":"git status"}`, TurnIndex: 1},
	}
	lastAssistant := map[int]string{1: "r1"}

	// 末段活动组默认展开，验证折叠态时显式传入折叠状态
	collapsed := buildTimelineItems(group, map[string]bool{"toolgroup-r1": false})
	if len(collapsed) != 1 {
		t.Fatalf("折叠时应只有组头一项，实际 %s", timelineItemIDs(collapsed))
	}
	if !itemHoldsTurnEnd(collapsed[0], lastAssistant) {
		t.Fatalf("折叠的工具组头应承载回合完成块")
	}

	expanded := buildTimelineItems(group, map[string]bool{"toolgroup-r1": true})
	if len(expanded) != 2 {
		t.Fatalf("展开后应是组头加一个节拍，实际 %s", timelineItemIDs(expanded))
	}
	if itemHoldsTurnEnd(expanded[0], lastAssistant) {
		t.Fatalf("展开的工具组头不应再承载回合完成块")
	}
	if !itemHoldsTurnEnd(expanded[1], lastAssistant) {
		t.Fatalf("组内末段节拍应承载回合完成块")
	}
}

// TestToolGroupSummaryReasoningAndTools 验证思考与工具混合成组后的摘要文本。
func TestToolGroupSummaryReasoningAndTools(t *testing.T) {
	msgs := []Message{
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "先看看", TurnIndex: 1},
		{ID: "t1", Role: "tool", Text: `Bash {"command":"git status"}`, TurnIndex: 1},
		{ID: "r2", Role: "assistant", Kind: "reasoning", Text: "再跑一次", TurnIndex: 1},
		{ID: "t2", Role: "tool", Text: `Bash {"command":"go build ./..."}`, TurnIndex: 1},
		{ID: "a1", Role: "assistant", Kind: "text", Text: "好了", TurnIndex: 1},
	}
	items := buildTimelineItems(msgs, nil)
	if len(items) != 2 || items[0].kind != timelineItemToolGroup {
		t.Fatalf("期望工具组加助手回复两行，实际 %s", timelineItemIDs(items))
	}
	if got, want := toolGroupSummary(items[0].tools), "思考 2 次 · 运行 2 条命令"; got != want {
		t.Fatalf("toolGroupSummary() = %q, want %q", got, want)
	}
}

// TestParseTodoItems 验证 TodoWrite / update_plan 输入的解析。
func TestParseTodoItems(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []todoItem
		ok   bool
	}{
		{
			name: "todos 数组",
			in:   `TodoWrite {"todos":[{"content":"读代码","status":"completed"},{"content":"改代码","status":"in_progress"},{"content":"跑测试","status":"pending"}]}`,
			want: []todoItem{{Text: "读代码", Status: "completed"}, {Text: "改代码", Status: "in_progress"}, {Text: "跑测试", Status: "pending"}},
			ok:   true,
		},
		{
			name: "plan 数组与 step 字段",
			in:   `update_plan {"plan":[{"step":"第一步","status":"in-progress"},{"step":"第二步","status":"done"}]}`,
			want: []todoItem{{Text: "第一步", Status: "in_progress"}, {Text: "第二步", Status: "completed"}},
			ok:   true,
		},
		{
			name: "Codex 计划驼峰状态",
			in:   `{"plan":[{"step":"执行中","status":"inProgress"},{"step":"等待","status":"pending"}],"explanation":"计划说明"}`,
			want: []todoItem{{Text: "执行中", Status: "in_progress"}, {Text: "等待", Status: "pending"}},
			ok:   true,
		},
		{
			name: "JSON 之后还有工具输出",
			in:   "TodoWrite {\"todos\":[{\"content\":\"只有一项\",\"status\":\"pending\"}]}\n已更新 1 项",
			want: []todoItem{{Text: "只有一项", Status: "pending"}},
			ok:   true,
		},
		{
			name: "缺少 status 视为待处理",
			in:   `TodoWrite {"todos":[{"content":"未标注"}]}`,
			want: []todoItem{{Text: "未标注", Status: "pending"}},
			ok:   true,
		},
		{name: "非 JSON", in: `ls -la`, ok: false},
		{name: "普通工具输入", in: `Read {"file_path":"a.go"}`, ok: false},
		{name: "空数组", in: `TodoWrite {"todos":[]}`, ok: false},
		{name: "截断的 JSON", in: `TodoWrite {"todos":[{"content":"半`, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseTodoItems(tc.in)
			if ok != tc.ok {
				t.Fatalf("parseTodoItems(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			}
			if !ok {
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parseTodoItems(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("parseTodoItems(%q)[%d] = %+v, want %+v", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestTodoCollapsedSummary 验证待办卡片收起态摘要的文本规则：有进行中项取进行中项，
// 没有进行中项取第一项未完成，全部完成显示「已全部完成」，空列表显示「暂无待办」。
func TestTodoCollapsedSummary(t *testing.T) {
	cases := []struct {
		name  string
		items []todoItem
		want  string
	}{
		{
			name: "有进行中项",
			items: []todoItem{
				{Text: "读代码", Status: "completed"},
				{Text: "改代码", Status: "in_progress"},
				{Text: "跑测试", Status: "pending"},
			},
			want: "1/3 · 改代码",
		},
		{
			name: "没有进行中项",
			items: []todoItem{
				{Text: "读代码", Status: "completed"},
				{Text: "改代码", Status: "pending"},
				{Text: "跑测试", Status: "pending"},
			},
			want: "1/3 · 改代码",
		},
		{
			name: "进行中项排在已完成项之前",
			items: []todoItem{
				{Text: "改代码", Status: "in_progress"},
				{Text: "读代码", Status: "completed"},
			},
			want: "1/2 · 改代码",
		},
		{
			name: "全部完成",
			items: []todoItem{
				{Text: "读代码", Status: "completed"},
				{Text: "改代码", Status: "completed"},
			},
			want: "2/2 · 已全部完成",
		},
		{name: "空列表", items: nil, want: "暂无待办"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := todoCollapsedSummary(tc.items); got != tc.want {
				t.Fatalf("todoCollapsedSummary() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTodoProgressRatio 验证进度条填充比例：完成数除以总数，空列表为 0。
func TestTodoProgressRatio(t *testing.T) {
	cases := []struct {
		name  string
		items []todoItem
		want  float32
	}{
		{"没有完成", []todoItem{{Status: "in_progress"}, {Status: "pending"}, {Status: "pending"}}, 0},
		{"完成一项", []todoItem{{Status: "completed"}, {Status: "in_progress"}, {Status: "pending"}}, 1.0 / 3.0},
		{"完成两项", []todoItem{{Status: "completed"}, {Status: "completed"}, {Status: "pending"}}, 2.0 / 3.0},
		{"全部完成", []todoItem{{Status: "completed"}, {Status: "completed"}}, 1},
		{"空列表", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := todoProgressRatio(tc.items); got != tc.want {
				t.Fatalf("todoProgressRatio() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLatestTodoMessageID 验证只把最后一条待办消息当作「当前待办」，
// 普通工具调用与普通文本不被误认。
func TestLatestTodoMessageID(t *testing.T) {
	oldTodo := `TodoWrite {"todos":[{"content":"读代码","status":"completed"}]}`
	newTodo := `update_plan {"plan":[{"step":"跑测试","status":"in_progress"}]}`
	messages := []Message{
		{ID: "t0", Role: "tool", Text: "ls -la"},
		{ID: "t1", Role: "tool", ToolInput: oldTodo},
		{ID: "u1", Role: "user", Text: "继续"},
		{ID: "t2", Role: "tool", ToolInput: newTodo},
		{ID: "a1", Role: "assistant", Text: "好了", IsFinal: true},
	}
	if got := latestTodoMessageID(messages); got != "t2" {
		t.Fatalf("latestTodoMessageID() = %q, want %q", got, "t2")
	}
	if got := latestTodoMessageID(messages[:4]); got != "t2" {
		t.Fatalf("latestTodoMessageID() 截掉末尾后 = %q, want %q", got, "t2")
	}
	if got := latestTodoMessageID(messages[:2]); got != "t1" {
		t.Fatalf("latestTodoMessageID() 只剩一条待办时 = %q, want %q", got, "t1")
	}
	if got := latestTodoMessageID([]Message{{ID: "t0", Role: "tool", Text: "ls -la"}}); got != "" {
		t.Fatalf("没有待办消息时 latestTodoMessageID() = %q, want 空", got)
	}
	if got := latestTodoMessageID(nil); got != "" {
		t.Fatalf("空消息列表 latestTodoMessageID() = %q, want 空", got)
	}
}

// TestTodoCardExpandedDefault 验证折叠默认规则：当前线程最新的一条待办且线程正在运行时默认展开，
// 线程停下或不是最新一条时默认收起，用户点过以后以用户选择为准。
func TestTodoCardExpandedDefault(t *testing.T) {
	todoInput := `TodoWrite {"todos":[{"content":"改代码","status":"in_progress"}]}`
	s := newChatState(NewFakeClient())
	s.mu.Lock()
	s.threads = []Thread{{ID: "t1", IsRunning: true}}
	s.selectedThreadID = "t1"
	s.messages = []Message{
		{ID: "old", Role: "tool", ToolInput: todoInput},
		{ID: "u1", Role: "user", Text: "继续"},
		{ID: "cur", Role: "tool", ToolInput: todoInput},
	}
	s.mu.Unlock()

	running := s.isThreadRunning(selectedThreadIDSnapshot(s))
	if !running {
		t.Fatal("测试会话应处于运行中")
	}
	if !s.todoCardExpanded(Message{ID: "cur"}, running) {
		t.Fatal("最新一条待办在线程运行时应默认展开")
	}
	if s.todoCardExpanded(Message{ID: "old"}, running) {
		t.Fatal("不是最新一条待办时应默认收起")
	}

	s.mu.Lock()
	s.threads = []Thread{{ID: "t1"}}
	s.mu.Unlock()
	stopped := s.isThreadRunning(selectedThreadIDSnapshot(s))
	if s.todoCardExpanded(Message{ID: "cur"}, stopped) {
		t.Fatal("线程停下后不应再默认展开")
	}

	// 用户点过以后以用户选择为准
	s.mu.Lock()
	s.expandedTool[todoCardKey("cur")] = true
	s.mu.Unlock()
	if !s.todoCardExpanded(Message{ID: "cur"}, stopped) {
		t.Fatal("用户点开后应保持展开")
	}
	s.mu.Lock()
	s.expandedTool[todoCardKey("cur")] = false
	s.mu.Unlock()
	if s.todoCardExpanded(Message{ID: "cur"}, running) {
		t.Fatal("用户收起后即使是最新一条也不应展开")
	}
}

// TestAssistantMetaText 验证助手头部元信息的拼装与省略规则。
func TestAssistantMetaText(t *testing.T) {
	at := time.Date(2026, 10, 18, 9, 5, 0, 0, time.Local)
	cases := []struct {
		name     string
		model    string
		duration time.Duration
		at       time.Time
		want     string
	}{
		{"全部字段", "claude-opus-4", 12 * time.Second, at, "claude-opus-4 · 12s · 09:05"},
		{"亚秒耗时", "gpt-5", 1200 * time.Millisecond, time.Time{}, "gpt-5 · 1.2s"},
		{"分钟耗时", "gpt-5", 150 * time.Second, time.Time{}, "gpt-5 · 2m30s"},
		{"只有模型", "gpt-5", 0, time.Time{}, "gpt-5"},
		{"模型带空白", "  gpt-5  ", 0, time.Time{}, "gpt-5"},
		{"全部为空", "", 0, time.Time{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := assistantMetaText(tc.model, tc.duration, tc.at); got != tc.want {
				t.Fatalf("assistantMetaText(%q, %v, %v) = %q, want %q", tc.model, tc.duration, tc.at, got, tc.want)
			}
		})
	}
}

// TestTimelineRows_TodoCard 验证 TodoWrite 工具调用渲染成带进度与状态的待办卡片。
func TestTimelineRows_TodoCard(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "规划一下", TurnIndex: 1, IsFinal: true},
		{ID: "t1", Role: "tool", TurnIndex: 1, Text: `TodoWrite {"todos":[{"content":"读代码","status":"completed"},{"content":"改代码","status":"in_progress"},{"content":"跑测试","status":"pending"}]}`},
	}
	tt := newTimelineTester(t, msgs, "待办清单")
	// 线程未运行时卡片默认收起：头部显示进度，摘要只给进行中的一项
	for _, want := range []string{"1/3", "1/3 · 改代码"} {
		if !tt.HasText(want) {
			t.Fatalf("收起态待办卡片缺少 %q: %q", want, tt.Texts())
		}
	}
	if tt.HasText("读代码") {
		t.Fatalf("收起态不应列出待办项: %q", tt.Texts())
	}
	// 点击头部展开后完整列出每一项
	clickText(t, tt, "展开待办清单")
	for _, want := range []string{"读代码", "改代码", "跑测试"} {
		if !tt.HasText(want) {
			t.Fatalf("展开后待办卡片缺少 %q: %q", want, tt.Texts())
		}
	}
}

// TestTimelineRows_StreamingCursorAndActions 验证流式中的助手消息末尾带光标且不显示操作栏，定稿后恢复操作栏并去掉光标。
func TestTimelineRows_StreamingCursorAndActions(t *testing.T) {
	fake := NewFakeClient()
	running := Thread{ID: "t1", ProjectID: "p1", Title: "流式会话", Provider: "claude", UpdatedAt: time.Now(), IsRunning: true}
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1", Path: "/workspace"}},
		map[string][]Thread{"p1": {running}},
	)
	fake.messages["t1"] = []Message{
		{ID: "u1", Role: "user", Text: "你好", TurnIndex: 1, IsFinal: true},
		{ID: "a1", Role: "assistant", Text: "正在回答", TurnIndex: 1, IsFinal: false},
	}

	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("你好") })
	// 流式正文按字符逐个显示，推进时钟让整段（含光标）显出来
	tt.Advance(2 * time.Second)
	if !tt.HasText("▍") {
		t.Fatalf("流式回复末尾应显示光标: %q", tt.Texts())
	}
	// 用户消息的操作栏也带「复制」按钮，这里用只属于助手操作栏的「分支」判定它是否渲染
	if _, ok := tt.Find("分支"); ok {
		t.Fatalf("流式回复不应显示操作栏: %q", tt.Texts())
	}

	done := running
	done.IsRunning = false
	fake.TriggerUpdate(ThreadUpdate{
		ThreadID: "t1",
		Thread:   done,
		Messages: []Message{{ID: "a1", Role: "assistant", Text: "正在回答", TurnIndex: 1, IsFinal: true}},
	})
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("复制")
		return ok
	})
	if tt.HasText("▍") {
		t.Fatalf("定稿后不应再显示流式光标: %q", tt.Texts())
	}
}

// TestCopyFeedbackActive 验证复制对勾反馈的 1.4 秒显示窗口与边界。
func TestCopyFeedbackActive(t *testing.T) {
	const key = "copy-btn-test"
	at := time.Now()
	copyFeedbackMu.Lock()
	copyFeedbackAt[key] = at
	copyFeedbackMu.Unlock()
	defer func() {
		copyFeedbackMu.Lock()
		delete(copyFeedbackAt, key)
		copyFeedbackMu.Unlock()
	}()

	if !copyFeedbackActive(key, at) {
		t.Fatalf("刚复制后应显示对勾")
	}
	if !copyFeedbackActive(key, at.Add(2500*time.Millisecond-time.Millisecond)) {
		t.Fatalf("2.5s 边界内应显示对勾")
	}
	if copyFeedbackActive(key, at.Add(2500*time.Millisecond)) {
		t.Fatalf("满 2.5s 应恢复复制图标")
	}
	if copyFeedbackActive(key, at.Add(copyFeedbackDuration+time.Millisecond)) {
		t.Fatalf("超过反馈时长应恢复复制图标")
	}
	if copyFeedbackActive("", at) {
		t.Fatalf("空 key 不应显示对勾")
	}
	if copyFeedbackActive("copy-btn-other", at) {
		t.Fatalf("未复制的按钮不应显示对勾")
	}
}

// TestClassifyNotice 验证系统通知的类型判定（错误 / 警告 / 工作树 / 模型 / 压缩 / 普通信息）。
func TestClassifyNotice(t *testing.T) {
	cases := []struct {
		name string
		text string
		want noticeKind
	}{
		{"普通信息", "已建立上下文", noticeInfo},
		{"子代理派发失败", "[编排] 子代理派发失败：boom", noticeError},
		{"英文错误", "provider failed: exit status 1", noticeError},
		{"网络重试", "网络中断，正在重新发送（第 1 次）", noticeWarning},
		{"打断超时", "打断超时，已强制结束本轮", noticeWarning},
		{"额度用尽", "额度用尽，将在 5 分钟后自动继续", noticeWarning},
		{"CLI 过旧", "CLI 版本已过旧，请升级对应 Provider CLI 后重新发送本轮消息；不会自动执行全局安装命令。", noticeWarning},
		{"会话重建", "原会话已失效，已用本地历史重建上下文", noticeWarning},
		{"工作树切换", "已切换到工作树 worktree-2", noticeWorktree},
		{"分支切换", "已切换到分支 feature/login", noticeWorktree},
		{"模型切换", "已切换到模型 gpt-5", noticeModel},
		{"上下文压缩", "上下文已压缩", noticeCompact},
		{"英文压缩", "Compacting the conversation…", noticeCompact},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyNotice(tc.text); got != tc.want {
				t.Fatalf("classifyNotice(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestFormatTurnFinishedText 验证回合完成块文本的拼装与省略规则。
func TestFormatTurnFinishedText(t *testing.T) {
	cases := []struct {
		name     string
		usage    TokenUsage
		duration time.Duration
		want     string
	}{
		{"全部为空", TokenUsage{}, 0, ""},
		{"输入输出", TokenUsage{Input: 1200, Output: 456}, 0, "已完成 · 输出 456"},
		{"含缓存命中", TokenUsage{Input: 100, Output: 50, Cached: 80}, 0, "已完成 · 输出 50"},
		{"含耗时", TokenUsage{Input: 1200, Output: 456}, 12 * time.Second, "已完成 · 用时 12s · 输出 456"},
		{"只有耗时", TokenUsage{}, 12 * time.Second, "已完成 · 用时 12s"},
		{"不足一秒不显示用时", TokenUsage{Output: 456}, 300 * time.Millisecond, "已完成 · 输出 456"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatTurnFinishedText(tc.usage, tc.duration); got != tc.want {
				t.Fatalf("formatTurnFinishedText(%+v, %v) = %q, want %q", tc.usage, tc.duration, got, tc.want)
			}
		})
	}
}

// TestCollectTurnToolStats 验证按回合统计工具调用次数、改动文件数与常用工具 Top3：
// 只统计目标回合的 tool 消息，编辑 / 写入类工具的路径去重后计入改动文件，次数相同的工具按名称升序。
func TestCollectTurnToolStats(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "问题", TurnIndex: 1},
		{ID: "t1", Role: "tool", ToolName: "read_file", TurnIndex: 1},
		{ID: "t2", Role: "tool", ToolName: "bash", TurnIndex: 1},
		{ID: "t3", Role: "tool", ToolName: "bash", TurnIndex: 1},
		{ID: "t4", Role: "tool", ToolName: "edit_file", ToolInput: `{"file_path":"/repo/a.go"}`, TurnIndex: 1},
		{ID: "t5", Role: "tool", ToolName: "write_file", ToolInput: `{"file_path":"/repo/b.go"}`, TurnIndex: 1},
		{ID: "t6", Role: "tool", ToolName: "edit_file", ToolInput: `{"file_path":"/repo/a.go"}`, TurnIndex: 1},
		{ID: "t7", Role: "tool", ToolName: "bash", TurnIndex: 2},
		{ID: "t8", Role: "tool", ToolName: "bash", TurnIndex: 0},
	}

	got := collectTurnToolStats(msgs, 1)
	if got.calls != 6 {
		t.Fatalf("工具调用次数 = %d, want 6", got.calls)
	}
	if got.changedFiles != 2 {
		t.Fatalf("改动文件数 = %d, want 2（a.go 去重、b.go）", got.changedFiles)
	}
	wantTop := []ToolCallCount{{Name: "bash", Count: 2}, {Name: "edit_file", Count: 2}, {Name: "read_file", Count: 1}}
	if len(got.topTools) != len(wantTop) {
		t.Fatalf("常用工具 = %+v, want %+v", got.topTools, wantTop)
	}
	for i := range wantTop {
		if got.topTools[i] != wantTop[i] {
			t.Fatalf("常用工具[%d] = %+v, want %+v", i, got.topTools[i], wantTop[i])
		}
	}

	// 回合序号缺失（0）时不统计，避免把整段历史算到同一回合
	if stats := collectTurnToolStats(msgs, 0); stats.calls != 0 || stats.changedFiles != 0 || len(stats.topTools) != 0 {
		t.Fatalf("回合序号为 0 时应为空，got %+v", stats)
	}

	// 超过 3 个工具时只保留次数最多的 3 项
	top4 := []Message{
		{ID: "a", Role: "tool", ToolName: "bash", TurnIndex: 3},
		{ID: "b", Role: "tool", ToolName: "grep", TurnIndex: 3},
		{ID: "c", Role: "tool", ToolName: "read", TurnIndex: 3},
		{ID: "d", Role: "tool", ToolName: "bash", TurnIndex: 3},
		{ID: "e", Role: "tool", ToolName: "task", TurnIndex: 3},
	}
	if stats := collectTurnToolStats(top4, 3); len(stats.topTools) != turnTopToolsLimit || stats.topTools[0] != (ToolCallCount{Name: "bash", Count: 2}) {
		t.Fatalf("Top3 = %+v, want 3 项且 bash 居首", stats.topTools)
	}
}

// TestFormatTurnFinishedSummary 验证带模型与工具信息的完成行主体文本：顺序为模型、用时、
// 工具调用、改动文件数、输出 tokens，缺数据的分段整段省略。
func TestFormatTurnFinishedSummary(t *testing.T) {
	stats := turnToolStats{calls: 12, changedFiles: 3}
	usage := TokenUsage{Input: 1200, Output: 1700}
	cases := []struct {
		name     string
		model    string
		stats    turnToolStats
		usage    TokenUsage
		duration time.Duration
		want     string
	}{
		{"全部为空", "", turnToolStats{}, TokenUsage{}, 0, ""},
		{"只有模型", "Opus 4.5", turnToolStats{}, TokenUsage{}, 0, "已完成 · Opus 4.5"},
		{"完整信息", "Opus 4.5", stats, usage, 35 * time.Second, "已完成 · Opus 4.5 · 用时 35s · 工具 12 · 改动 3 个文件 · 输出 1.7k"},
		{"没有模型", "", stats, usage, 35 * time.Second, "已完成 · 用时 35s · 工具 12 · 改动 3 个文件 · 输出 1.7k"},
		{"没有工具信息", "Sonnet 4.5", turnToolStats{}, usage, 35 * time.Second, "已完成 · Sonnet 4.5 · 用时 35s · 输出 1.7k"},
		{"不足一秒不显示用时", "Sonnet 4.5", turnToolStats{calls: 2}, usage, 300 * time.Millisecond, "已完成 · Sonnet 4.5 · 工具 2 · 输出 1.7k"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatTurnFinishedSummary(tc.model, tc.stats, tc.usage, tc.duration); got != tc.want {
				t.Fatalf("formatTurnFinishedSummary = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFormatTurnFinishedBlockTextWithInfo 验证模型与工具信息后追加输出速度：
// 没有逐请求样本时按整轮口径（输出 tokens / 用时），有样本时用平均值与范围。
func TestFormatTurnFinishedBlockTextWithInfo(t *testing.T) {
	got := formatTurnFinishedBlockTextWithInfo("Opus 4.5", turnToolStats{calls: 12, changedFiles: 3},
		TokenUsage{Input: 1200, Output: 456, DurationMs: 12_000}, 12*time.Second)
	want := "已完成 · Opus 4.5 · 用时 12s · 工具 12 · 改动 3 个文件 · 输出 456 · 38 t/s"
	if got != want {
		t.Fatalf("完成块文本 = %q, want %q", got, want)
	}

	// 三个样本：300/10s = 30 t/s，200/2s = 100 t/s，100/1s = 100 t/s；
	// 平均按 token 加权 600/13s ≈ 46 t/s，范围 30–100。
	sampled := TokenUsage{Output: 600, DurationMs: 13_000, Requests: []RequestSpeed{
		{OutputTokens: 300, DurationMs: 10_000},
		{OutputTokens: 200, DurationMs: 2_000},
		{OutputTokens: 100, DurationMs: 1_000},
	}}
	got = formatTurnFinishedBlockTextWithInfo("Opus 4.5", turnToolStats{}, sampled, 13*time.Second)
	want = "已完成 · Opus 4.5 · 用时 13s · 输出 600 · 46 t/s（30–100）"
	if got != want {
		t.Fatalf("带样本的完成块文本 = %q, want %q", got, want)
	}
}

// TestAppendTurnEndClock 验证完成行末尾结束时间的拼接：空文本与零值时间原样返回，
// 且结束时间会转为本地时区并带月日。
func TestAppendTurnEndClock(t *testing.T) {
	end := time.Date(2026, 10, 22, 14, 32, 5, 0, time.Local)
	utcEnd := time.Date(2026, 10, 22, 23, 16, 27, 0, time.UTC)
	cases := []struct {
		name string
		text string
		end  time.Time
		want string
	}{
		{"空文本不拼接", "", end, ""},
		{"零值时间不拼接", "已完成 · Sonnet 5.5", time.Time{}, "已完成 · Sonnet 5.5"},
		{"正常拼接结束时间", "已完成 · Sonnet 5.5 · 95 t/s", end, "已完成 · Sonnet 5.5 · 95 t/s · 结束 10-22 14:32:05"},
		{"UTC 时间转本地时区", "已完成 · Sonnet 5.5 · 95 t/s", utcEnd,
			"已完成 · Sonnet 5.5 · 95 t/s · 结束 " + utcEnd.Local().Format("01-02 15:04:05")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := appendTurnEndClock(tc.text, tc.end); got != tc.want {
				t.Fatalf("appendTurnEndClock = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFormatTurnFinishedTooltip 验证完成行 Tooltip 的多行内容：token 明细、缓存命中率、
// 估算费用、常用工具 Top3 与起止时间；没有数据时整行省略。
func TestFormatTurnFinishedTooltip(t *testing.T) {
	start := time.Date(2026, 10, 19, 10, 0, 0, 0, time.Local)
	info := turnFinishedInfo{
		usage:   TokenUsage{Input: 1200, Output: 456, Cached: 300},
		stats:   turnToolStats{calls: 6, changedFiles: 2, topTools: []ToolCallCount{{Name: "bash", Count: 2}, {Name: "edit_file", Count: 2}}},
		costUSD: 0.0123,
		hasCost: true,
		start:   start,
		end:     start.Add(35 * time.Second),
	}
	want := "输入 1.2k · 输出 456 · 缓存 300 tokens\n" +
		"缓存命中 20.0% · 估算 ≈ $0.01\n" +
		"工具 bash×2 · edit_file×2\n" +
		"开始 " + start.Local().Format("01-02 15:04:05") + " · 结束 " + start.Add(35*time.Second).Local().Format("01-02 15:04:05")
	if got := formatTurnFinishedTooltip(info); got != want {
		t.Fatalf("Tooltip = %q, want %q", got, want)
	}

	// 没有任何可用数据时不显示 Tooltip
	if got := formatTurnFinishedTooltip(turnFinishedInfo{}); got != "" {
		t.Fatalf("空数据 Tooltip = %q, want 空", got)
	}
}

// TestFormatTurnFinishedTooltipRequestSpeed 验证有逐请求样本时 Tooltip 在 token 明细后
// 多一行速度详情（平均、范围、请求数）。
func TestFormatTurnFinishedTooltipRequestSpeed(t *testing.T) {
	info := turnFinishedInfo{usage: TokenUsage{Output: 600, Requests: []RequestSpeed{
		{OutputTokens: 300, DurationMs: 10_000},
		{OutputTokens: 300, DurationMs: 5_000},
	}}}
	want := "输出 600 tokens\n输出速度 平均 40 t/s · 范围 30–60 t/s · 2 次请求"
	if got := formatTurnFinishedTooltip(info); got != want {
		t.Fatalf("Tooltip = %q, want %q", got, want)
	}
}

// TestTurnSpanFromMessages 验证从消息时间戳推导本回合起止时间：开始取本回合用户消息，
// 结束取本回合最后一条消息；回合序号缺失或没有时间戳时返回零值。
func TestTurnSpanFromMessages(t *testing.T) {
	start := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	msgs := []Message{
		{ID: "u1", Role: "user", TurnIndex: 1, CreatedAt: start.Format(time.RFC3339)},
		{ID: "t1", Role: "tool", TurnIndex: 1, CreatedAt: start.Add(20 * time.Second).Format(time.RFC3339)},
		{ID: "a1", Role: "assistant", TurnIndex: 1, CreatedAt: start.Add(35 * time.Second).Format(time.RFC3339)},
		{ID: "u2", Role: "user", TurnIndex: 2, CreatedAt: start.Add(time.Minute).Format(time.RFC3339)},
	}
	gotStart, gotEnd := turnSpanFromMessages(msgs, 1)
	if !gotStart.Equal(start) || !gotEnd.Equal(start.Add(35*time.Second)) {
		t.Fatalf("回合 1 起止 = %v, %v", gotStart, gotEnd)
	}
	if s, e := turnSpanFromMessages(msgs, 0); !s.IsZero() || !e.IsZero() {
		t.Fatalf("回合序号为 0 时应返回零值，got %v, %v", s, e)
	}
}

// TestShortenModelNameAndCost 验证模型短名的日期后缀裁剪、超长截断与费用文本的精度规则。
func TestShortenModelNameAndCost(t *testing.T) {
	if got := shortenModelName("claude-sonnet-4-5-20250929"); got != "claude-sonnet-4-5" {
		t.Fatalf("模型短名 = %q, want claude-sonnet-4-5", got)
	}
	long := "a-very-long-model-name-with-extra-tokens"
	if got := shortenModelName(long); utf8.RuneCountInString(got) != 24 || !strings.HasSuffix(got, "…") {
		t.Fatalf("超长模型名 = %q, want 24 个字符且以省略号结尾", got)
	}
	if got := shortenModelName("  Opus 4.5  "); got != "Opus 4.5" {
		t.Fatalf("模型短名 = %q, want Opus 4.5", got)
	}
	if got := formatTurnCost(0.0123); got != "≈ $0.01" {
		t.Fatalf("formatTurnCost(0.0123) = %q", got)
	}
	if got := formatTurnCost(0.0004); got != "≈ $0.0004" {
		t.Fatalf("formatTurnCost(0.0004) = %q", got)
	}
}

// TestFormatTokensPerSecond 验证输出速度的计算、格式与省略规则。
func TestFormatTokensPerSecond(t *testing.T) {
	cases := []struct {
		name    string
		tokens  int
		elapsed time.Duration
		want    string
	}{
		{"常规速度", 456, 12 * time.Second, "38 t/s"},
		{"快于百 token 每秒", 1000, 8 * time.Second, "125 t/s"},
		{"恰好半秒保留", 100, 500 * time.Millisecond, "200 t/s"},
		{"整数速度保持不变", 37, time.Second, "37 t/s"},
		{"取整向下", 364, 10 * time.Second, "36 t/s"},
		{"取整向上", 376, 10 * time.Second, "38 t/s"},
		{"耗时不足半秒不显示", 400, 400 * time.Millisecond, ""},
		{"耗时为零不显示", 400, 0, ""},
		{"耗时为负不显示", 400, -time.Second, ""},
		{"没有输出 token 不显示", 0, 12 * time.Second, ""},
		{"输出 token 为负不显示", -5, 12 * time.Second, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatTokensPerSecond(tc.tokens, tc.elapsed); got != tc.want {
				t.Fatalf("formatTokensPerSecond(%d, %v) = %q, want %q", tc.tokens, tc.elapsed, got, tc.want)
			}
		})
	}
}

// TestRequestSpeedStatsFor 验证请求速度样本的汇总：过滤没有输出或时长不足 0.2 秒的样本，
// 平均值按 Σ 输出 tokens / Σ 秒数加权，min/max 取各样本速度，没有有效样本时返回 false。
func TestRequestSpeedStatsFor(t *testing.T) {
	reqs := []RequestSpeed{
		{OutputTokens: 400, DurationMs: 10_000}, // 40 t/s
		{OutputTokens: 3, DurationMs: 100},      // 不足 0.2 秒，忽略
		{OutputTokens: 900, DurationMs: 0},      // 没有时长，忽略
		{OutputTokens: 0, DurationMs: 5_000},    // 没有输出，忽略
		{OutputTokens: 100, DurationMs: 2_000},  // 50 t/s
		{OutputTokens: 60, DurationMs: 1_000},   // 60 t/s
	}
	st, ok := requestSpeedStatsFor(reqs)
	if !ok {
		t.Fatal("有有效样本时应返回 true")
	}
	if st.n != 3 {
		t.Fatalf("有效样本数 = %d, want 3", st.n)
	}
	if st.min != 40 || st.max != 60 {
		t.Fatalf("min/max = %v/%v, want 40/60", st.min, st.max)
	}
	// 按 token 加权：560 / 13 ≈ 43.08，而不是算术平均 50。
	if want := 560.0 / 13; math.Abs(st.avg-want) > 1e-9 {
		t.Fatalf("avg = %v, want %v", st.avg, want)
	}

	// 0.2 秒是保留边界，刚好等于时仍统计（浮点除法不保证整数，按下取整后比较）。
	if st, ok := requestSpeedStatsFor([]RequestSpeed{{OutputTokens: 40, DurationMs: 200}}); !ok || st.n != 1 || int(math.Round(st.avg)) != 200 {
		t.Fatalf("边界样本 = %+v, ok = %v, want 1 个 200 t/s", st, ok)
	}

	// 空输入与全部被过滤时返回 false。
	for _, tc := range [][]RequestSpeed{
		nil,
		{},
		{{OutputTokens: 0, DurationMs: 5_000}, {OutputTokens: 50, DurationMs: 100}},
	} {
		if _, ok := requestSpeedStatsFor(tc); ok {
			t.Fatalf("requestSpeedStatsFor(%v) 应返回 false", tc)
		}
	}
}

// TestFormatRequestSpeedShort 验证完成行紧凑速度文本：单样本只显示平均，
// 多样本取整后极值不同时带上范围，极值相同或被取整抹平时只显示平均。
func TestFormatRequestSpeedShort(t *testing.T) {
	cases := []struct {
		name string
		st   requestSpeedStats
		want string
	}{
		{"单样本", requestSpeedStats{min: 41.3, max: 41.3, avg: 41.3, n: 1}, "41 t/s"},
		{"多样本带范围", requestSpeedStats{min: 32, max: 58, avg: 41, n: 5}, "41 t/s（32–58）"},
		{"取整后极值相同", requestSpeedStats{min: 40.6, max: 41.4, avg: 41, n: 3}, "41 t/s"},
		{"平均取整向上", requestSpeedStats{min: 30.2, max: 60.7, avg: 41.6, n: 2}, "42 t/s（30–61）"},
		{"没有样本", requestSpeedStats{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatRequestSpeedShort(tc.st); got != tc.want {
				t.Fatalf("formatRequestSpeedShort(%+v) = %q, want %q", tc.st, got, tc.want)
			}
		})
	}
}

// TestFormatRequestSpeedDetail 验证悬停详情的完整文本：多样本给平均、范围与请求数，单样本只给平均。
func TestFormatRequestSpeedDetail(t *testing.T) {
	if got := formatRequestSpeedDetail(requestSpeedStats{min: 32, max: 58, avg: 41.4, n: 5}); got != "输出速度 平均 41 t/s · 范围 32–58 t/s · 5 次请求" {
		t.Fatalf("多样本详情 = %q", got)
	}
	if got := formatRequestSpeedDetail(requestSpeedStats{min: 41, max: 41, avg: 41, n: 1}); got != "输出速度 41 t/s · 1 次请求" {
		t.Fatalf("单样本详情 = %q", got)
	}
	if got := formatRequestSpeedDetail(requestSpeedStats{}); got != "" {
		t.Fatalf("空样本详情 = %q, want 空", got)
	}
}

// TestTurnFinishedBlockShowsTokensPerSecond 验证完成块用输出 tokens 除以生成时长算出并显示速度：
// 优先后端记录的 TokenUsage.DurationMs，没有时用界面推导出的时长；格式为「已完成 · 用时 … · 输出 … · 38 t/s」。
func TestTurnFinishedBlockShowsTokensPerSecond(t *testing.T) {
	usage := TokenUsage{Input: 1200, Output: 456, Cached: 300, DurationMs: 12_000}
	want := "已完成 · 用时 12s · 输出 456 · 38 t/s"
	if got := formatTurnFinishedBlockText(usage, resolveTurnDuration(usage, 0)); got != want {
		t.Fatalf("完成块文本 = %q, want %q", got, want)
	}

	// 后端没给耗时：用界面从用户消息时间推导出的时长（16 秒 → 456/16 ≈ 29）。
	start := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	derived, ok := turnEndDuration(start, start.Add(16*time.Second))
	if !ok || derived != 16*time.Second {
		t.Fatalf("turnEndDuration = %v, %v; want 16s, true", derived, ok)
	}
	if got := formatTurnFinishedBlockText(usage, resolveTurnDuration(TokenUsage{Output: 456}, derived)); !strings.HasSuffix(got, " · 29 t/s") {
		t.Fatalf("推导时长算出的速度不对: %q", got)
	}
}

// TestTurnFinishedBlockHidesTokensPerSecond 验证拿不到时长时不显示速度：完成块只保留输出 tokens。
func TestTurnFinishedBlockHidesTokensPerSecond(t *testing.T) {
	usage := TokenUsage{Input: 1200, Output: 456}
	start := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	derived, ok := turnEndDuration(time.Time{}, start) // 用户消息没有 CreatedAt
	if ok || derived != 0 {
		t.Fatalf("没有开始时间时应返回 0, false，got %v, %v", derived, ok)
	}
	want := "已完成 · 输出 456"
	if got := formatTurnFinishedBlockText(usage, resolveTurnDuration(usage, derived)); got != want {
		t.Fatalf("完成块文本 = %q, want %q", got, want)
	}
	// 时长不足 0.5 秒时用时段和速度都不显示；0.5–1 秒之间只隐藏用时段、仍显示速度。
	if got := formatTurnFinishedBlockText(usage, 300*time.Millisecond); got != want {
		t.Fatalf("时长不足半秒时文本 = %q, want %q", got, want)
	}
	if got := formatTurnFinishedBlockText(usage, 800*time.Millisecond); got != "已完成 · 输出 456 · 570 t/s" {
		t.Fatalf("时长介于半秒到一秒时文本 = %q, want %q", got, "已完成 · 输出 456 · 570 t/s")
	}
}

// TestParseContinuationStartTitle 验证转发/分叉标题前缀的解析：返回来源标题与提示语，
// 普通标题或前缀后没有标题时不返回内容。
func TestParseContinuationStartTitle(t *testing.T) {
	cases := []struct {
		name       string
		title      string
		wantSource string
		wantLabel  string
		wantOK     bool
	}{
		{"转发前缀", "Continued: 修复登录流程", "修复登录流程", "继续自", true},
		{"分叉前缀", "Branch: 修复登录流程", "修复登录流程", "分叉自", true},
		{"前缀前有空白", "  Continued: 修复登录流程  ", "修复登录流程", "继续自", true},
		{"普通标题", "修复登录流程", "", "", false},
		{"空标题", "", "", "", false},
		{"前缀后没有标题", "Continued:  ", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source, label, ok := parseContinuationStartTitle(tc.title)
			if ok != tc.wantOK || source != tc.wantSource || label != tc.wantLabel {
				t.Fatalf("parseContinuationStartTitle(%q) = %q, %q, %v; want %q, %q, %v",
					tc.title, source, label, ok, tc.wantSource, tc.wantLabel, tc.wantOK)
			}
		})
	}
}

// TestTruncateContinuationSourceTitle 验证来源标题的安全截断：不超过 36 个字符时原样返回，
// 超出时保留前 33 个字符再补 "..."，且按 rune 截断不会切坏多字节字符。
func TestTruncateContinuationSourceTitle(t *testing.T) {
	longCJK := strings.Repeat("会", 40)
	cases := []struct {
		name  string
		title string
		want  string
	}{
		{"短标题不变", "修复登录流程", "修复登录流程"},
		{"刚好 36 个字符不变", strings.Repeat("会", 36), strings.Repeat("会", 36)},
		{"超出 36 个字符截断", strings.Repeat("会", 37), strings.Repeat("会", 33) + "..."},
		{"长中文标题截断", longCJK, strings.Repeat("会", 33) + "..."},
		{"首尾空白先裁掉", "  修复登录流程  ", "修复登录流程"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateContinuationSourceTitle(tc.title)
			if got != tc.want {
				t.Fatalf("truncateContinuationSourceTitle(%q) = %q, want %q", tc.title, got, tc.want)
			}
			if n := len([]rune(got)); n > continuationSourceTitleMaxRunes {
				t.Fatalf("截断后长度 %d 超过上限 %d: %q", n, continuationSourceTitleMaxRunes, got)
			}
		})
	}
}

// TestContinuationStartFor_TruncatesLongSourceTitle 验证引导卡片只截断展示标题：
// SourceTitle 保留完整原文（用于在同项目内匹配来源会话），DisplayTitle 截断后带省略号。
func TestContinuationStartFor_TruncatesLongSourceTitle(t *testing.T) {
	longTitle := strings.Repeat("很长的来源会话标题", 5)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.mu.Lock()
	s.threads = []Thread{
		{ID: "t0", ProjectID: "p1", Title: longTitle},
		{ID: "t1", ProjectID: "p1", Title: "Continued: " + longTitle},
	}
	s.mu.Unlock()

	info, ok := continuationStartFor(s, "t1")
	if !ok {
		t.Fatalf("转发会话应展示引导卡片")
	}
	if info.SourceTitle != longTitle {
		t.Fatalf("SourceTitle 应保留完整原文，got %q", info.SourceTitle)
	}
	wantDisplay := string([]rune(longTitle)[:33]) + "..."
	if info.DisplayTitle != wantDisplay {
		t.Fatalf("DisplayTitle = %q, want %q", info.DisplayTitle, wantDisplay)
	}
	if info.TargetThreadID != "t0" {
		t.Fatalf("超长标题仍应按原文匹配到源会话 t0，got %q", info.TargetThreadID)
	}
}

// TestContinuationStartFor 验证引导卡片的判定：只有消息为空的转发/分叉会话才会返回卡片内容，
// 普通标题的会话或有消息的会话都不展示，来源会话只在同一项目内按原标题匹配。
func TestContinuationStartFor(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.mu.Lock()
	s.threads = []Thread{
		{ID: "t0", ProjectID: "p1", Title: "原会话"},
		{ID: "t1", ProjectID: "p1", Title: "Continued: 原会话"},
		{ID: "t2", ProjectID: "p1", Title: "Branch: 原会话"},
		{ID: "t3", ProjectID: "p1", Title: "普通会话"},
		{ID: "t4", ProjectID: "p2", Title: "Continued: 别的项目里的会话"},
	}
	s.mu.Unlock()

	setMessages := func(msgs []Message) {
		s.mu.Lock()
		s.messages = msgs
		s.mu.Unlock()
	}

	setMessages(nil)
	info, ok := continuationStartFor(s, "t1")
	if !ok || info.Label != "继续自" || info.SourceTitle != "原会话" || info.TargetThreadID != "t0" {
		t.Fatalf("转发会话应展示“继续自 原会话”并指向 t0，got %+v, %v", info, ok)
	}

	info, ok = continuationStartFor(s, "t2")
	if !ok || info.Label != "分叉自" || info.SourceTitle != "原会话" || info.TargetThreadID != "t0" {
		t.Fatalf("分叉会话应展示“分叉自 原会话”并指向 t0，got %+v, %v", info, ok)
	}

	// 来源会话不在同一项目：仍然展示来源提示，但没有可跳转的目标。
	info, ok = continuationStartFor(s, "t4")
	if !ok || info.SourceTitle != "别的项目里的会话" || info.TargetThreadID != "" {
		t.Fatalf("跨项目时不应给出跳转目标，got %+v, %v", info, ok)
	}

	// 普通标题的会话不展示引导卡片。
	if info, ok = continuationStartFor(s, "t3"); ok {
		t.Fatalf("普通会话不应展示引导卡片，got %+v", info)
	}

	// 已经有消息的转发会话不再展示引导卡片。
	setMessages([]Message{{ID: "u1", Role: "user", Text: "已经发出的消息", TurnIndex: 1, IsFinal: true}})
	if info, ok = continuationStartFor(s, "t1"); ok {
		t.Fatalf("有消息的会话不应展示引导卡片，got %+v", info)
	}
}

// TestTimelineRows_ContinuationStartCard 验证空转发会话在空态渲染来源引导卡片，
// 点击“打开原会话”跳回来源会话；普通空会话与已有消息的转发会话都不会多出这张卡片。
func TestTimelineRows_ContinuationStartCard(t *testing.T) {
	now := time.Now()
	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1", Path: "/workspace"}},
		map[string][]Thread{"p1": {
			{ID: "t0", ProjectID: "p1", Title: "原会话", UpdatedAt: now.Add(-2 * time.Minute)},
			{ID: "t1", ProjectID: "p1", Title: "Continued: 原会话", UpdatedAt: now},
		}},
	)
	fake.messages["t0"] = []Message{{ID: "u0", Role: "user", Text: "原来的第一句", TurnIndex: 1, IsFinal: true}}

	s, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("继续自「原会话」") })

	if !tt.HasText("打开原会话") {
		t.Fatalf("引导卡片缺少“打开原会话”按钮: %q", tt.Texts())
	}
	if tt.HasText(" 中构建什么？") {
		t.Fatalf("转发会话不应再显示通用的新会话引导")
	}

	clickText(t, tt, "打开原会话")
	waitFor(t, tt, func() bool { return tt.HasText("原来的第一句") })
	s.mu.Lock()
	selected := s.selectedThreadID
	s.mu.Unlock()
	if selected != "t0" {
		t.Fatalf("点击“打开原会话”后应切到 t0，got %q", selected)
	}

	// 普通空会话：仍然显示通用引导，没有来源卡片。
	plainFake := NewFakeClient()
	plainTT := ui.NewTester(Root(plainFake), 1000, 700)
	waitFor(t, plainTT, func() bool { return plainTT.HasText(" 中构建什么？") })
	if plainTT.HasText("继续自") || plainTT.HasText("分叉自") || plainTT.HasText("打开原会话") {
		t.Fatalf("普通空会话不应渲染引导卡片: %q", plainTT.Texts())
	}

	// 已有消息的转发会话：正常显示时间线，不渲染空态引导卡片。
	busyFake := NewFakeClient()
	busyFake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1", Path: "/workspace"}},
		map[string][]Thread{"p1": {{ID: "t1", ProjectID: "p1", Title: "Continued: 原会话", UpdatedAt: now}}},
	)
	busyFake.messages["t1"] = []Message{{ID: "u1", Role: "user", Text: "已经发出的消息", TurnIndex: 1, IsFinal: true}}
	busyTT := ui.NewTester(Root(busyFake), 1000, 700)
	waitFor(t, busyTT, func() bool { return busyTT.HasText("已经发出的消息") })
	if busyTT.HasText("继续自") || busyTT.HasText("打开原会话") {
		t.Fatalf("有消息的会话不应渲染引导卡片: %q", busyTT.Texts())
	}

	// 来源标题过长：卡片上展示截断后的标题，完整标题不会被塞进卡片挤压布局。
	longTitle := strings.Repeat("很长的来源会话标题", 5)
	longFake := NewFakeClient()
	longFake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1", Path: "/workspace"}},
		map[string][]Thread{"p1": {
			{ID: "t0", ProjectID: "p1", Title: longTitle, UpdatedAt: now.Add(-2 * time.Minute)},
			{ID: "t1", ProjectID: "p1", Title: "Continued: " + longTitle, UpdatedAt: now},
		}},
	)
	longTT := ui.NewTester(Root(longFake), 1000, 700)
	truncated := truncateContinuationSourceTitle(longTitle)
	waitFor(t, longTT, func() bool { return longTT.HasText("继续自「" + truncated + "」") })
	if longTT.HasText("继续自「" + longTitle + "」") {
		t.Fatalf("引导卡片不应展示未截断的超长标题: %q", longTT.Texts())
	}
}

// TestToolGroupElapsedText 验证工具组耗时：运行中的组按当前时间算实时耗时，跑完的组仍用首尾时间戳的差值。
func TestToolGroupElapsedText(t *testing.T) {
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	tools := []Message{
		{ID: "t1", Role: "tool", CreatedAt: start.Format(time.RFC3339)},
		{ID: "t2", Role: "tool",
			CreatedAt:       start.Add(2 * time.Second).Format(time.RFC3339),
			ToolCompletedAt: start.Add(4 * time.Second).Format(time.RFC3339)},
	}
	if got := toolGroupElapsedText(tools, true, start.Add(3200*time.Millisecond)); got != "3.2s" {
		t.Fatalf("运行中耗时 = %q, want 3.2s", got)
	}
	// 实时耗时随当前时间增长，而不是卡在首尾时间戳上
	if got := toolGroupElapsedText(tools, true, start.Add(6200*time.Millisecond)); got != "6.2s" {
		t.Fatalf("运行中耗时 = %q, want 6.2s", got)
	}
	// 跑完的组不看当前时间，仍用首尾时间戳的差值
	if got := toolGroupElapsedText(tools, false, start.Add(time.Hour)); got != "4.0s" {
		t.Fatalf("完成耗时 = %q, want 4.0s", got)
	}
	// 组首是思考行时，实时耗时从第一条工具消息的开始时间算起
	withReasoning := []Message{
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "先看看", CreatedAt: start.Format(time.RFC3339)},
		{ID: "t1", Role: "tool", CreatedAt: start.Add(time.Second).Format(time.RFC3339)},
	}
	if got := toolGroupElapsedText(withReasoning, true, start.Add(3200*time.Millisecond)); got != "2.2s" {
		t.Fatalf("组首为思考行时的实时耗时 = %q, want 2.2s", got)
	}
	// 运行中即使只有一条工具消息也能给出耗时（首尾相同也能算）
	if got := toolGroupElapsedText(tools[:1], true, start.Add(1500*time.Millisecond)); got != "1.5s" {
		t.Fatalf("单条工具消息的运行中耗时 = %q, want 1.5s", got)
	}
	if got := toolGroupElapsedText(tools[:1], false, start.Add(time.Second)); got != "" {
		t.Fatalf("单条工具消息的组完成后应无耗时，got %q", got)
	}
	if got := toolGroupElapsedText([]Message{{ID: "t1", Role: "tool"}}, true, start); got != "" {
		t.Fatalf("拿不到开始时间时应返回空，got %q", got)
	}
	if got := toolGroupElapsedText(nil, true, start); got != "" {
		t.Fatalf("空组应返回空，got %q", got)
	}
	if got := toolGroupElapsedText(tools, true, start); got != "" {
		t.Fatalf("当前时间不晚于开始时间时应返回空，got %q", got)
	}
}

// TestToolGroupHeaderSummaryText 验证组头摘要前缀：运行中为「工作中 · 」，完成且有耗时为「用时 Xs · 」。
func TestToolGroupHeaderSummaryText(t *testing.T) {
	counts := countToolGroupCategories([]Message{
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "先看看仓库状态"},
		{ID: "t1", Role: "tool", Text: `Bash {"command":"git status"}`},
		{ID: "t2", Role: "tool", Text: `Read {"path":"/tmp/a.go"}`},
	})
	base := toolGroupSummaryText(counts)
	if base != "思考 1 次 · 运行 1 条命令 · 读取 1 个文件" {
		t.Fatalf("分类计数摘要 = %q", base)
	}
	if got := toolGroupHeaderSummaryText(counts, false, "", nil); got != base {
		t.Fatalf("无耗时时摘要 = %q, want %q", got, base)
	}
	// 运行中只用「工作中」前缀，不再叠加「用时 Xs」
	wantRunning := "工作中 · " + base
	if got := toolGroupHeaderSummaryText(counts, true, "", nil); got != wantRunning {
		t.Fatalf("运行中摘要 = %q, want %q", got, wantRunning)
	}
	if got := toolGroupHeaderSummaryText(counts, true, "3.2s", nil); got != wantRunning {
		t.Fatalf("运行中摘要不应重复耗时 = %q, want %q", got, wantRunning)
	}
	wantDone := "用时 3.2s · " + base
	if got := toolGroupHeaderSummaryText(counts, false, "3.2s", nil); got != wantDone {
		t.Fatalf("完成摘要 = %q, want %q", got, wantDone)
	}
}

// TestToolGroupPreviewTools 验证运行中工具组的折叠预览只取组内最后 3 条工具行，且保持原始顺序。
func TestToolGroupPreviewTools(t *testing.T) {
	if toolGroupPreviewCount != 3 {
		t.Fatalf("预览条数常量应为 3，got %d", toolGroupPreviewCount)
	}
	tools := []Message{
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "先看仓库状态"},
		{ID: "t1", Role: "tool"},
		{ID: "t2", Role: "tool"},
		{ID: "t3", Role: "tool"},
		{ID: "t4", Role: "tool"},
		{ID: "t5", Role: "tool"},
	}
	got := toolGroupPreviewTools(tools, toolGroupPreviewCount)
	want := []string{"t3", "t4", "t5"}
	if len(got) != len(want) {
		t.Fatalf("预览条数 = %d, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("预览第 %d 条 = %q, want %q", i+1, got[i].ID, id)
		}
	}
	// 工具不足 3 条时全部返回，思考行不进预览
	short := toolGroupPreviewTools(tools[:3], toolGroupPreviewCount)
	if len(short) != 2 || short[0].ID != "t1" || short[1].ID != "t2" {
		t.Fatalf("工具不足时预览 = %v", short)
	}
	if got := toolGroupPreviewTools(tools, 0); len(got) != 0 {
		t.Fatalf("limit 为 0 时应返回空，got %v", got)
	}
	if got := toolGroupPreviewTools([]Message{{ID: "r1", Role: "assistant", Kind: "reasoning"}}, toolGroupPreviewCount); len(got) != 0 {
		t.Fatalf("没有工具行时应返回空，got %v", got)
	}
}

// TestTimelineRows_ToolGroupRunningPreview 验证运行中的工具组默认折叠：组头显示「读取」，
// 折叠态只预览组内最后 3 条工具行；用户展开后列出全部，跑完后预览消失。
func TestTimelineRows_ToolGroupRunningPreview(t *testing.T) {
	running := Thread{ID: "t1", ProjectID: "p1", Title: "运行中", Provider: "claude", UpdatedAt: time.Now(), IsRunning: true}
	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1", Path: "/workspace"}},
		map[string][]Thread{"p1": {running}},
	)
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "读一下这几个文件", TurnIndex: 1, IsFinal: true},
	}
	for i := 1; i <= 5; i++ {
		msgs = append(msgs, Message{
			ID:        fmt.Sprintf("t%d", i),
			Role:      "tool",
			Text:      fmt.Sprintf(`Read {"path":"/tmp/a%d.go"}`, i),
			TurnIndex: 1,
		})
	}
	fake.messages["t1"] = msgs

	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("读取") })

	// 默认折叠：组头是「展开」，摘要只显示分类名
	if _, ok := tt.Find("展开"); !ok {
		t.Fatalf("运行中的工具组应默认折叠: %q", tt.Texts())
	}
	if !tt.HasText("读取") {
		t.Fatalf("运行中的组头应显示「读取」: %q", tt.Texts())
	}
	// 折叠预览只显示最近 3 条工具行
	for _, name := range []string{"a5.go", "a4.go", "a3.go"} {
		if !tt.HasText(name) {
			t.Fatalf("折叠预览应显示 %s: %q", name, tt.Texts())
		}
	}
	for _, name := range []string{"a1.go", "a2.go"} {
		if tt.HasText(name) {
			t.Fatalf("折叠预览不应显示更早的 %s: %q", name, tt.Texts())
		}
	}

	preview, ok := tt.Find("a3.go")
	if !ok {
		t.Fatal("找不到预览胶囊")
	}

	// 用户展开后以用户的选择为准：完整列出 5 条工具行
	clickText(t, tt, "读取")
	if _, ok := tt.Find("收起"); !ok {
		t.Fatalf("点击组头后应变为收起: %q", tt.Texts())
	}
	for i := 1; i <= 5; i++ {
		name := fmt.Sprintf("a%d.go", i)
		if !tt.HasText(name) {
			t.Fatalf("展开后应列出 %s: %q", name, tt.Texts())
		}
	}
	expanded, ok := tt.Find("a3.go")
	if !ok || preview.X != expanded.X {
		t.Fatalf("预览和展开胶囊应使用相同缩进: preview=%+v expanded=%+v", preview, expanded)
	}

	// 再点一次收起，回到折叠预览
	clickText(t, tt, "收起")
	if tt.HasText("a1.go") {
		t.Fatalf("收起后不应再列出更早的工具行: %q", tt.Texts())
	}
}

// failingMediaFakeClient 在 FakeClient 之上补一个总是失败的 ReadMessageMedia，
// 用来验证图片附件加载失败时的占位卡。
type failingMediaFakeClient struct {
	*FakeClient
}

// ReadMessageMedia 固定返回读取失败。
func (f *failingMediaFakeClient) ReadMessageMedia(ctx context.Context, threadID, messageID, mediaID, variant string) (string, []byte, error) {
	return "", nil, errors.New("网络中断")
}

// TestTimelineRows_UserImagePlaceholderFailed 验证图片缩略图加载失败时，用户消息附件条
// 渲染带「图片加载失败」「点击重试」的失败占位卡，而不是可点击放大的缩略图或回形针胶囊。
func TestTimelineRows_UserImagePlaceholderFailed(t *testing.T) {
	msgs := []Message{
		{
			ID:        "u-img",
			Role:      "user",
			Text:      "看看这张图",
			TurnIndex: 1,
			IsFinal:   true,
			Media: []MessageMedia{
				{ID: "img-1", Name: "photo.png", MimeType: "image/png", Width: 800, Height: 600},
			},
		},
	}
	fake := &failingMediaFakeClient{FakeClient: NewFakeClient()}
	fake.messages["t1"] = msgs
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("看看这张图") })
	waitFor(t, tt, func() bool { return tt.HasText("图片加载失败") })
	if !tt.HasText("点击重试") {
		t.Fatalf("失败占位卡应提示点击重试: %q", tt.Texts())
	}
}

// TestTimelineRows_UserImagePlaceholderSize 验证占位卡尺寸沿用缩略图的等比规则，
// 并在极端比例下保留能放下两行提示的最小边长。
func TestTimelineRows_UserImagePlaceholderSize(t *testing.T) {
	cases := []struct {
		name string
		med  MessageMedia
		w, h float32
	}{
		{"宽高未知", MessageMedia{}, 160, 160},
		{"横图", MessageMedia{Width: 400, Height: 200}, 160, 80},
		{"竖图", MessageMedia{Width: 200, Height: 400}, 80, 160},
		{"极端细长", MessageMedia{Width: 4000, Height: 100}, 160, 48},
	}
	for _, tc := range cases {
		w, h := userAttachmentPlaceholderSize(tc.med)
		if w != tc.w || h != tc.h {
			t.Fatalf("%s: 期望 %vx%v，实际 %vx%v", tc.name, tc.w, tc.h, w, h)
		}
	}
}

// TestTimelineTurnFilesCard 验证回合完成行下面出现「本回合改动 N 个文件」卡片：
// 本回合两条成功完成的写文件工具（t.TempDir 里真实存在的文件）算 2 个。
func TestTimelineTurnFilesCard(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.go")
	second := filepath.Join(dir, "second.go")
	for _, p := range []string{first, second} {
		if err := os.WriteFile(p, []byte("package a\n"), 0o644); err != nil {
			t.Fatalf("写测试文件 %s: %v", p, err)
		}
	}
	msgs := []Message{
		{ID: "u1", Role: "user", Kind: "text", Text: "改两个文件", TurnIndex: 1, IsFinal: true},
		{ID: "t1", Role: "tool", ToolName: "write", ToolStatus: "completed", Text: "写入完成",
			ToolInput: `{"file_path":` + strconv.Quote(first) + `}`, TurnIndex: 1},
		{ID: "t2", Role: "tool", ToolName: "edit", ToolStatus: "completed", Text: "修改完成",
			ToolInput: `{"file_path":` + strconv.Quote(second) + `}`, TurnIndex: 1},
		{ID: "a1", Role: "assistant", Kind: "text", Text: "改好了", TurnIndex: 1, IsFinal: true},
	}
	fake := NewFakeClient()
	fake.messages["t1"] = msgs
	fake.SetThreadWorkDir(dir)
	fake.SetThreadUsage("t1", ThreadUsage{
		Turns: map[int]TokenUsage{1: {Output: 120}},
		Total: TokenUsage{Output: 120},
	})
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("改动文件") })
}

// TestTimelineRows_SkipsStewardWakeMessage 验证总管唤醒消息不产生时间线行，全量与增量两条路径一致。
func TestTimelineRows_SkipsStewardWakeMessage(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "帮我看看", TurnIndex: 1},
		{ID: "a1", Role: "assistant", Text: "好", TurnIndex: 1, IsFinal: true},
		{ID: "w1", Role: "user", Kind: stewardWakeKind, Text: "宿主汇总了 3 条待处理事件", TurnIndex: 2},
		{ID: "a2", Role: "assistant", Text: "都处理完了。", TurnIndex: 2, IsFinal: true},
	}

	items := buildTimelineItems(msgs)
	if len(items) != 3 {
		t.Fatalf("唤醒消息不应产生行，期望 3 行，实际 %s", timelineItemIDs(items))
	}
	for _, it := range items {
		if it.msg.ID == "w1" {
			t.Fatalf("唤醒消息不应产生时间线行: %s", timelineItemIDs(items))
		}
	}

	// 增量路径：同一组消息依次增长，每一步都要与全量结果严格一致。
	s := &chatState{}
	threadID := "steward-thread"
	for i := 1; i <= len(msgs); i++ {
		helperBuildAndAssertIncremental(t, s, threadID, msgs[:i], nil, "总管唤醒增量构建")
	}
}

// TestTimelineRows_SkipsSilentAssistantMessage 验证整轮只有静默标记的助手消息不产生行，
// 标记后面还有别的话时照常显示。
func TestTimelineRows_SkipsSilentAssistantMessage(t *testing.T) {
	msgs := []Message{
		{ID: "w1", Role: "user", Kind: stewardWakeKind, Text: "宿主汇总了 1 条待处理事件", TurnIndex: 1},
		{ID: "a1", Role: "assistant", Text: "  " + stewardSilentMarker + "\n", TurnIndex: 1, IsFinal: true},
		{ID: "w2", Role: "user", Kind: stewardWakeKind, Text: "宿主汇总了 2 条待处理事件", TurnIndex: 2},
		{ID: "a2", Role: "assistant", Text: stewardSilentMarker + " 另外我还顺手清理了日志。", TurnIndex: 2, IsFinal: true},
	}

	items := buildTimelineItems(msgs)
	if len(items) != 1 {
		t.Fatalf("静默轮次不应产生行，期望只剩 1 行，实际 %s", timelineItemIDs(items))
	}
	if items[0].msg.ID != "a2" {
		t.Fatalf("标记后面还有正文时应正常显示，实际 %s", timelineItemIDs(items))
	}

	s := &chatState{}
	for i := 1; i <= len(msgs); i++ {
		helperBuildAndAssertIncremental(t, s, "steward-thread", msgs[:i], nil, "静默轮次增量构建")
	}
}

// TestTimelineRows_SkipsStreamingSilentPrefix 验证流式输出中正文只是标记前缀时先不显示，
// 定稿后不是标记就照常显示。
func TestTimelineRows_SkipsStreamingSilentPrefix(t *testing.T) {
	streaming := []Message{
		{ID: "a1", Role: "assistant", Text: "[[静", TurnIndex: 1},
	}
	if items := buildTimelineItems(streaming); len(items) != 0 {
		t.Fatalf("流式中的标记前缀不应产生行，实际 %s", timelineItemIDs(items))
	}
	final := []Message{
		{ID: "a1", Role: "assistant", Text: "[[静默]] 这句是正文。", TurnIndex: 1, IsFinal: true},
	}
	if items := buildTimelineItems(final); len(items) != 1 {
		t.Fatalf("定稿后不是标记的回复应正常显示，实际 %s", timelineItemIDs(items))
	}
}

// TestTimelineRows_HostEventStillProducesItem 验证宿主事件照旧进入时间线（渲染成卡片），
// 只有总管的唤醒消息被跳过。
func TestTimelineRows_HostEventStillProducesItem(t *testing.T) {
	msgs := []Message{
		{ID: "h1", Role: "user", Kind: hostEventKind, Text: "后台任务连续失败两次。", TurnIndex: 1},
	}
	items := buildTimelineItems(msgs)
	if len(items) != 1 || items[0].msg.ID != "h1" {
		t.Fatalf("宿主事件应照常进入时间线，实际 %s", timelineItemIDs(items))
	}
}

// turnWorkSplitSessionMessages 造一段真实历史：第一轮有思考与两条命令（满足折叠条件），
// 一轮结束后后端追加分割线，TurnIndex 取的仍是上一轮，后面才是新的提问。
func turnWorkSplitSessionMessages() []Message {
	return []Message{
		{ID: "u1", Role: "user", Text: "先看看仓库", TurnIndex: 1, IsFinal: true},
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "先确认仓库结构", TurnIndex: 1},
		{ID: "t1", Role: "tool", Text: `Bash {"command":"git status"}`, TurnIndex: 1, ToolStatus: "completed"},
		{ID: "t2", Role: "tool", Text: `Bash {"command":"go build ./..."}`, TurnIndex: 1, ToolStatus: "completed"},
		{ID: "a1", Role: "assistant", Kind: "text", Text: "FIRST-TURN-ANSWER", TurnIndex: 1, IsFinal: true},
		{ID: "s1", Role: "system", Kind: splitSessionKind, Text: splitSessionNotice, TurnIndex: 1},
		{ID: "u2", Role: "user", Text: "分割之后的问题", TurnIndex: 2, IsFinal: true},
		{ID: "a2", Role: "assistant", Kind: "text", Text: "SECOND-TURN-ANSWER", TurnIndex: 2, IsFinal: true},
	}
}

// splitSessionItemInsideFold 返回某个折叠项内部是否夹带了分割线；没有折叠项时返回 false。
func splitSessionItemInsideFold(items []timelineItem) bool {
	for _, it := range items {
		if it.kind != timelineItemTurnWork {
			continue
		}
		if containsMessageID(it.tools, "s1") {
			return true
		}
		for _, inner := range it.workItems {
			if isSplitSessionItem(inner) {
				return true
			}
		}
	}
	return false
}

// TestTimelineRows_SplitDividerSurvivesTurnFold 验证分割线不会被回合折叠收起：
// 上一轮被折成折叠项时，分割线仍然作为独立一行出现在渲染结果里。
func TestTimelineRows_SplitDividerSurvivesTurnFold(t *testing.T) {
	msgs := turnWorkSplitSessionMessages()

	state := newChatState(NewFakeClient())
	state.mu.Lock()
	state.messages = msgs
	state.mu.Unlock()
	folded := applyTurnWorkFold(state, "t1", buildTimelineItems(msgs))
	if splitSessionItemInsideFold(folded) {
		t.Fatalf("分割线不应被收进折叠区: %s", timelineItemIDs(folded))
	}

	tt := newTimelineTester(t, msgs, "FIRST-TURN-ANSWER")
	if tt.HasText("git status") {
		t.Fatalf("上一轮的工具调用应已折叠收起: %v", tt.Texts())
	}
	if !tt.HasText(splitSessionNotice) {
		t.Fatalf("折叠状态下分割线仍应可见: %v", tt.Texts())
	}
}

// TestTimelineRows_SplitDividerNeverFoldedEvenBeforeAnswer 验证分割线夹在同一轮答复之前时也不被折叠：
// 分割线结束当前折叠区间，自己另起一段独立渲染。
func TestTimelineRows_SplitDividerNeverFoldedEvenBeforeAnswer(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "先看看仓库", TurnIndex: 1, IsFinal: true},
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "先确认仓库结构", TurnIndex: 1},
		{ID: "t1", Role: "tool", Text: `Bash {"command":"git status"}`, TurnIndex: 1, ToolStatus: "completed"},
		{ID: "t2", Role: "tool", Text: `Bash {"command":"go build ./..."}`, TurnIndex: 1, ToolStatus: "completed"},
		{ID: "s1", Role: "system", Kind: splitSessionKind, Text: splitSessionNotice, TurnIndex: 1},
		{ID: "a1", Role: "assistant", Kind: "text", Text: "FIRST-TURN-ANSWER", TurnIndex: 1, IsFinal: true},
	}

	state := newChatState(NewFakeClient())
	state.mu.Lock()
	state.messages = msgs
	state.mu.Unlock()
	folded := applyTurnWorkFold(state, "t1", buildTimelineItems(msgs))
	if splitSessionItemInsideFold(folded) {
		t.Fatalf("分割线不应被收进折叠区: %s", timelineItemIDs(folded))
	}

	tt := newTimelineTester(t, msgs, "FIRST-TURN-ANSWER")
	if !tt.HasText(splitSessionNotice) {
		t.Fatalf("折叠状态下分割线仍应可见: %v", tt.Texts())
	}
}

// contextCompactedItemInsideFold 返回某个折叠项内部是否夹带了压缩边界行。
func contextCompactedItemInsideFold(items []timelineItem) bool {
	for _, it := range items {
		if it.kind != timelineItemTurnWork {
			continue
		}
		if containsMessageID(it.tools, "c1") {
			return true
		}
		for _, inner := range it.workItems {
			if isContextCompactedItem(inner) || inner.msg.ID == "c1" {
				return true
			}
		}
	}
	return false
}

// TestTimelineRows_ContextCompactedNeverFolded 验证「已压缩上下文」边界行不会被回合折叠收起：
// 它自己另起一段独立渲染，也不计入折叠区的消息（折叠组头的「X 次」与耗时都不把它算进去）。
func TestTimelineRows_ContextCompactedNeverFolded(t *testing.T) {
	compacted := Message{ID: "c1", Role: "system", Kind: contextCompactedKind, Text: "已压缩上下文 · 120k → 12k", TurnIndex: 1}
	base := func() []Message {
		return []Message{
			{ID: "u1", Role: "user", Text: "先看看仓库", TurnIndex: 1, IsFinal: true},
			{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "先确认仓库结构", TurnIndex: 1},
			{ID: "t1", Role: "tool", Text: `Bash {"command":"git status"}`, TurnIndex: 1, ToolStatus: "completed"},
			{ID: "t2", Role: "tool", Text: `Bash {"command":"go build ./..."}`, TurnIndex: 1, ToolStatus: "completed"},
		}
	}
	tests := []struct {
		name         string
		msgs         []Message
		wantFold     bool
		hasCompacted bool
	}{
		{
			name:         "没有边界行时照常折叠",
			msgs:         append(base(), Message{ID: "a1", Role: "assistant", Kind: "text", Text: "FIRST-TURN-ANSWER", TurnIndex: 1, IsFinal: true}),
			wantFold:     true,
			hasCompacted: false,
		},
		{
			name:         "边界行在答复之后",
			msgs:         append(base(), Message{ID: "a1", Role: "assistant", Kind: "text", Text: "FIRST-TURN-ANSWER", TurnIndex: 1, IsFinal: true}, compacted),
			wantFold:     true,
			hasCompacted: true,
		},
		{
			name:         "边界行夹在回合工作中途",
			msgs:         append(append(base()[:3], compacted), base()[3], Message{ID: "a1", Role: "assistant", Kind: "text", Text: "FIRST-TURN-ANSWER", TurnIndex: 1, IsFinal: true}),
			wantFold:     false,
			hasCompacted: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := newChatState(NewFakeClient())
			state.mu.Lock()
			state.messages = tt.msgs
			state.mu.Unlock()
			folded := applyTurnWorkFold(state, "t1", buildTimelineItems(tt.msgs))
			if contextCompactedItemInsideFold(folded) {
				t.Fatalf("压缩边界行不应被收进折叠区: %s", timelineItemIDs(folded))
			}
			hasFold := false
			standalone := false
			for _, it := range folded {
				if it.kind == timelineItemTurnWork {
					hasFold = true
				}
				if it.kind == timelineItemSingle && it.msg.ID == "c1" {
					standalone = true
				}
			}
			if hasFold != tt.wantFold {
				t.Fatalf("折叠项存在 = %v, want %v: %s", hasFold, tt.wantFold, timelineItemIDs(folded))
			}
			if tt.hasCompacted && !standalone {
				t.Fatalf("压缩边界行应作为独立一行出现: %s", timelineItemIDs(folded))
			}
		})
	}
}

// TestTimelineRows_ForwardedContextCard 验证带入历史作为时间线显示。
func TestTimelineRows_ForwardedContextCard(t *testing.T) {
	now := time.Now()
	const content = "User:\n原来的第一句"
	const dockLabel = "继续自「原会话」"

	newFake := func(title string, withMessages bool) *FakeClient {
		fake := NewFakeClient()
		fake.SetProjectsAndThreads(
			[]Project{{ID: "p1", Name: "Proj1", Path: "/workspace"}},
			map[string][]Thread{"p1": {
				{ID: "t0", ProjectID: "p1", Title: "原会话", UpdatedAt: now.Add(-2 * time.Minute)},
				{ID: "t1", ProjectID: "p1", Title: title, UpdatedAt: now},
			}},
		)
		if withMessages {
			fake.messages["t1"] = []Message{{ID: "u1", Role: "user", Text: "已经发出的消息", TurnIndex: 1, IsFinal: true}}
		}
		fake.forwardedContexts = map[string]ForwardedContext{
			"t1": {SourceTitle: "原会话", Content: content},
		}
		return fake
	}

	t.Run("空会话直接显示历史", func(t *testing.T) {
		tt := ui.NewTester(Root(newFake("Continued: 原会话", false)), 1000, 700)
		waitFor(t, tt, func() bool { return tt.HasText("原来的第一句") })
		if !tt.HasText("原来的第一句") {
			t.Fatalf("Hero 模式应直接看到带入的正文: %q", tt.Texts())
		}
		if tt.HasText("在下方选择模型或配对，发送消息继续对话。") {
			t.Fatalf("不应显示引导文案: %q", tt.Texts())
		}
	})

	t.Run("已有消息时显示历史", func(t *testing.T) {
		tt := ui.NewTester(Root(newFake("Continued: 原会话", true)), 1000, 700)
		waitFor(t, tt, func() bool { return tt.HasText(dockLabel) })
		if !tt.HasText("原来的第一句") {
			t.Fatalf("应显示最后一轮正文: %q", tt.Texts())
		}
		if tt.HasText("在下方选择模型或配对") {
			t.Fatalf("停靠模式不应显示引导文案: %q", tt.Texts())
		}
	})

	t.Run("标题改过后仍显示来源标题", func(t *testing.T) {
		tt := ui.NewTester(Root(newFake("改过的标题", true)), 1000, 700)
		waitFor(t, tt, func() bool { return tt.HasText(dockLabel) })
	})

	t.Run("更早消息可展开", func(t *testing.T) {
		fake := newFake("Continued: 原会话", false)
		fake.forwardedContexts["t1"] = ForwardedContext{SourceTitle: "原会话", Content: "User:\n更早的问题\n\nAssistant:\n更早的回答\n\nUser:\n最后的问题\n\nAssistant:\n最后的回答"}
		tt := ui.NewTester(Root(fake), 1000, 700)
		waitFor(t, tt, func() bool { return tt.HasText("更早 2 条") && tt.HasText("最后的问题") })
		if tt.HasText("更早的问题") {
			t.Fatalf("更早消息应默认收起: %q", tt.Texts())
		}
		clickText(t, tt, "更早 2 条")
		waitFor(t, tt, func() bool { return tt.HasText("更早的问题") })
	})

	t.Run("截断时不显示说明", func(t *testing.T) {
		fake := newFake("Continued: 原会话", false)
		fake.forwardedContexts["t1"] = ForwardedContext{SourceTitle: "原会话", Content: content, Truncated: true}
		tt := ui.NewTester(Root(fake), 1000, 700)
		waitFor(t, tt, func() bool { return tt.HasText(dockLabel) })
		if tt.HasText("部分已截断") {
			t.Fatalf("不应显示截断说明: %q", tt.Texts())
		}
	})

	t.Run("普通会话不显示卡片", func(t *testing.T) {
		fake := NewFakeClient()
		fake.SetProjectsAndThreads(
			[]Project{{ID: "p1", Name: "Proj1", Path: "/workspace"}},
			map[string][]Thread{"p1": {{ID: "t9", ProjectID: "p1", Title: "普通会话", UpdatedAt: now}}},
		)
		fake.messages["t9"] = []Message{{ID: "u9", Role: "user", Text: "普通消息", TurnIndex: 1, IsFinal: true}}
		tt := ui.NewTester(Root(fake), 1000, 700)
		waitFor(t, tt, func() bool { return tt.HasText("普通消息") })
		if tt.HasText("继续自") || tt.HasText("已带入") {
			t.Fatalf("普通会话不应显示上下文卡片: %q", tt.Texts())
		}
	})

	t.Run("来源分隔线可跳转", func(t *testing.T) {
		tt := ui.NewTester(Root(newFake("Continued: 原会话", true)), 1000, 700)
		waitFor(t, tt, func() bool { return tt.HasText(dockLabel) })
		if tt.HasText("打开原会话") {
			t.Fatalf("停靠模式默认折叠，不应显示“打开原会话”: %q", tt.Texts())
		}
		clickText(t, tt, dockLabel)
		waitFor(t, tt, func() bool { return !tt.HasText(dockLabel) })
	})

	t.Run("改名后的空转发会话仍显示来源卡片", func(t *testing.T) {
		// 标题被改过，解析不到 “Continued: ” 前缀：靠着已加载上下文的来源标题撑起 Hero 卡片。
		tt := ui.NewTester(Root(newFake("改过的标题", false)), 1000, 700)
		waitFor(t, tt, func() bool { return tt.HasText("继续自「原会话」") })
		if !tt.HasText("原来的第一句") {
			t.Fatalf("改名后的空转发会话应看到带入的正文: %q", tt.Texts())
		}
		if tt.HasText(" 中构建什么？") {
			t.Fatalf("转发会话不应再显示通用的新会话引导")
		}
	})

	t.Run("上下文为空时不显示停靠卡片", func(t *testing.T) {
		fake := newFake("Continued: 原会话", true)
		fake.forwardedContexts["t1"] = ForwardedContext{SourceTitle: "原会话"}
		s, root := buildRoot(fake)
		tt := ui.NewTester(root, 1000, 700)
		waitFor(t, tt, func() bool {
			_, loaded := s.loadedForwardedContext("t1")
			return loaded && tt.HasText("已经发出的消息")
		})
		if tt.HasText("继续自") || tt.HasText("已带入") || tt.HasText("打开原会话") {
			t.Fatalf("上下文为空时不应显示停靠卡片: %q", tt.Texts())
		}
	})
}

// toolDetailTestLines 生成 n 行形如 LINE-01 的工具输出，供详情区截断用例使用。
func toolDetailTestLines(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("LINE-%02d", i+1)
	}
	return strings.Join(lines, "\n")
}

// TestRenderToolDetail_TailPreview 验证命令工具超过 30 行时详情默认显示尾部 30 行，并提示前面省略的行数。
func TestRenderToolDetail_TailPreview(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "u1", Role: "user", Text: "跑一下构建", TurnIndex: 1, IsFinal: true},
		{ID: "t1", Role: "tool", ToolName: "bash", ToolInput: `{"command":"ls"}`, Text: toolDetailTestLines(40), TurnIndex: 1},
	})
	s, root := buildRoot(fake)
	s.mu.Lock()
	s.expandedTool["t1"] = true
	s.mu.Unlock()
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("LINE-01") })
	if tt.HasText("LINE-40") {
		t.Fatalf("折叠时不应显示末尾: %q", tt.Texts())
	}
	if !tt.HasText("展开全部") {
		t.Fatalf("应提供展开入口: %q", tt.Texts())
	}
}

// TestRenderToolDetail_TailExpand 验证点开「显示完整输出」后改用独立弹窗展示全文：
// 时间线保持 30 行尾部预览（不再展开全文），弹窗里能看全文，并提供「复制输出」/「关闭」。
func TestRenderToolDetail_TailExpand(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "u1", Role: "user", Text: "跑一下构建", TurnIndex: 1, IsFinal: true},
		{ID: "t1", Role: "tool", ToolName: "bash", ToolInput: `{"command":"ls"}`, Text: toolDetailTestLines(40), TurnIndex: 1},
	})
	s, root := buildRoot(fake)
	s.mu.Lock()
	s.expandedTool["t1"] = true
	s.mu.Unlock()
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("展开全部") })
	clickText(t, tt, "展开全部")
	waitFor(t, tt, func() bool { return tt.HasText("LINE-40") })
	if !tt.HasText("LINE-01") {
		t.Fatalf("展开后应显示完整输出: %q", tt.Texts())
	}
}

// TestRenderToolDetail_ReadKeepsHead 验证读文件类工具超过 30 行时详情仍保留头部，不显示省略提示。
func TestAssistantHeaderOmitsModelName(t *testing.T) {
	s := newChatState(NewFakeClient())
	m := Message{ID: "assistant-model", Role: "assistant", IsFinal: true}
	s.selectedThreadID = "thread"
	s.threads = []Thread{{ID: "thread", Provider: "openai", Model: "unique-model-name"}}
	s.messages = []Message{m}
	tt := ui.NewTester(func(c *ui.Context) { renderAssistantHeader(c, s, m, true) }, 800, 600)
	if tt.HasText("unique-model-name") {
		t.Fatal("助手元信息行不应显示模型名")
	}
}

func TestPendingToolInputClosesAfterCompletion(t *testing.T) {
	s := newChatState(NewFakeClient())
	m := Message{ID: "pending-input", Role: "tool", ToolName: "read", ToolStatus: "pending", ToolInput: `{"path":"unique-pending-file"}`}
	tt := ui.NewTester(func(c *ui.Context) { renderSingleToolItem(c, s, m) }, 800, 600)
	if !tt.HasText(m.ToolInput) {
		t.Fatal("等待时应显示入参")
	}
	m.ToolStatus = "completed"
	tt = ui.NewTester(func(c *ui.Context) { renderSingleToolItem(c, s, m) }, 800, 600)
	if tt.HasText(m.ToolInput) {
		t.Fatal("完成后应收起入参")
	}
}

func TestRenderToolDetail_ReadKeepsHead(t *testing.T) {
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "u1", Role: "user", Text: "看看这个文件", TurnIndex: 1, IsFinal: true},
		{ID: "t1", Role: "tool", ToolName: "read", ToolInput: `{"file_path":"/workspace/main.go"}`, Text: toolDetailTestLines(40), TurnIndex: 1},
	})
	s, root := buildRoot(fake)
	s.mu.Lock()
	s.expandedTool["t1"] = true
	s.mu.Unlock()
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("LINE-01") })

	if tt.HasText("… 前面省略") {
		t.Fatalf("读文件保留头部时不应显示省略提示: %q", tt.Texts())
	}
	if tt.HasText("LINE-40") {
		t.Fatalf("读文件预览不应显示末尾的行: %q", tt.Texts())
	}
}

// TestTimelineRows_ExpandKeepsClickedRowInPlace 验证跟随末尾的列表里展开最后一条消息的折叠时，
// 被点的那一行留在原位：下一帧不再被拉回末尾（HoldPlace）。
func TestTimelineRows_ExpandKeepsClickedRowInPlace(t *testing.T) {
	var msgs []Message
	for i := 0; i < 40; i++ {
		msgs = append(msgs, Message{ID: fmt.Sprintf("u%02d", i), Role: "user", Text: fmt.Sprintf("消息-%02d", i), TurnIndex: i + 1, IsFinal: true})
	}
	msgs = append(msgs, Message{ID: "t1", Role: "tool", Text: "ls -la\nTOOL-DETAIL-BODY", TurnIndex: 41})
	tt := newTimelineTester(t, msgs, "ls -la")

	before, ok := tt.Find("ls -la")
	if !ok {
		t.Fatalf("最后一条工具行应在视口内")
	}
	clickText(t, tt, "ls -la")
	if !tt.HasText("TOOL-DETAIL-BODY") {
		t.Fatalf("点击后应展开工具详情")
	}
	after, ok := tt.Find("ls -la")
	if !ok {
		t.Fatalf("展开后工具行跑出视口")
	}
	if math.Abs(float64(after.Y-before.Y)) > 1 {
		t.Fatalf("展开后胶囊行从 Y=%.1f 移到 Y=%.1f，列表被拉回末尾", float64(before.Y), float64(after.Y))
	}
}

// TestAssistantHeaderFirstFrameForHistoryMessage 验证打开历史会话时，未流式输出过的最终回复
// 第一帧就显示时间，不会晚一帧出现造成跳动。
func TestAssistantHeaderFirstFrameForHistoryMessage(t *testing.T) {
	s := newChatState(NewFakeClient())
	m := Message{ID: "a-history", Role: "assistant", Text: "历史回复", TurnIndex: 1, IsFinal: true}
	s.selectedThreadID = "thread"
	s.threads = []Thread{{ID: "thread", Provider: "claude", UpdatedAt: time.Date(2024, 1, 2, 3, 4, 5, 0, time.Local)}}
	s.messages = []Message{m}

	tt := ui.NewTester(func(c *ui.Context) {
		renderAssistantMessage(c, s, m, "", "", true, false, TokenUsage{}, 0, false)
	}, 800, 600)
	if !tt.HasText("03:04") {
		t.Fatalf("历史消息首帧就应显示时间: %q", tt.Texts())
	}
}

// TestAssistantRevealGate 验证本会话里流式输出过的消息要等正文完全显示后才露出时间与操作栏，
// 而没流式过的消息直接视为已显示。
func TestAssistantRevealGate(t *testing.T) {
	s := newChatState(NewFakeClient())
	m := Message{ID: "a-stream", Role: "assistant", Text: "流式回复", TurnIndex: 1, IsFinal: true}
	s.selectedThreadID = "thread"
	s.threads = []Thread{{ID: "thread", Provider: "claude", UpdatedAt: time.Date(2024, 1, 2, 3, 4, 5, 0, time.Local)}}
	s.messages = []Message{m}

	if !s.assistantRevealed(m.ID) {
		t.Fatalf("没有流式输出过的消息应视为已完全显示")
	}
	// 以流式状态渲染过、正文还没显示完：时间与操作栏都不出现。
	s.markAssistantRevealed(m.ID, true, false)
	if s.assistantRevealed(m.ID) {
		t.Fatalf("流式正文未完全显示时不应算已显示")
	}
	if shouldShowAssistantActions(m, false, true, s.assistantRevealed(m.ID)) {
		t.Fatalf("正文未完全显示时不应显示操作栏")
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderAssistantHeader(c, s, m, s.assistantRevealed(m.ID))
	}, 800, 600)
	if tt.HasText("03:04") {
		t.Fatalf("正文未完全显示时不应显示时间: %q", tt.Texts())
	}

	// 标记为已完全显示后，下一帧补上时间。
	s.markAssistantRevealed(m.ID, true, true)
	if !s.assistantRevealed(m.ID) {
		t.Fatalf("标记为完全显示后应算已显示")
	}
	tt.Frame()
	if !tt.HasText("03:04") {
		t.Fatalf("完全显示后应补上时间: %q", tt.Texts())
	}
}
