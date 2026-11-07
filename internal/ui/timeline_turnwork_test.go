package mcodeui

import (
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// turnWorkSplitToolMessages 造一个「工具调用被中途正文切断」的已结束回合：
// 思考与第一条命令之后插入一段中途正文，再跑第二条命令，最后才是最终答复。
// 这种回合在折叠前会被切成两个工具组，折叠后必须合成同一个折叠项。
func turnWorkSplitToolMessages() []Message {
	return []Message{
		{ID: "u1", Role: "user", Text: "先看看仓库再改", TurnIndex: 1, IsFinal: true},
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "先确认仓库结构\n再看构建脚本\n最后核对测试入口", TurnIndex: 1},
		{ID: "t1", Role: "tool", Text: `Bash {"command":"git status"}`, TurnIndex: 1, ToolStatus: "completed"},
		{ID: "amid", Role: "assistant", Kind: "text", Text: "MID-STATUS-TEXT", TurnIndex: 1},
		{ID: "t2", Role: "tool", Text: `Bash {"command":"go build ./..."}`, TurnIndex: 1, ToolStatus: "completed"},
		{ID: "a1", Role: "assistant", Kind: "text", Text: "FOLD-ANSWER-ONE", TurnIndex: 1, IsFinal: true},
		{ID: "u2", Role: "user", Text: "再跑一遍测试", TurnIndex: 2, IsFinal: true},
		{ID: "a2", Role: "assistant", Kind: "text", Text: "FOLD-ANSWER-TWO", TurnIndex: 2, IsFinal: true},
	}
}

// turnWorkFoldHeaderText 是第一回合折叠项的组头：耗时拿不到时只写「过程」，
// 计数与唤醒原因移到 Tooltip，不再占组头。
const turnWorkFoldHeaderText = "过程"

// TestTimelineRows_TurnWorkFoldCollapsesFinishedTurn 验证一轮里的思考与工具调用被收进一个折叠项，
// 最终正文留在折叠项之外，点开后折叠区内的内容重新出现。
func TestTimelineRows_TurnWorkFoldCollapsesFinishedTurn(t *testing.T) {
	tt := newTimelineTester(t, turnWorkSplitToolMessages(), "FOLD-ANSWER-ONE")

	if !tt.HasText(turnWorkFoldHeaderText) {
		t.Fatalf("缺少回合折叠组头 %q，当前文本 %v", turnWorkFoldHeaderText, tt.Texts())
	}
	if !tt.HasText("FOLD-ANSWER-ONE") {
		t.Fatalf("最终正文应当留在折叠项之外: %v", tt.Texts())
	}
	if tt.HasText("MID-STATUS-TEXT") || tt.HasText("git status") {
		t.Fatalf("折叠区内的工作过程默认应收起: %v", tt.Texts())
	}

	clickText(t, tt, turnWorkFoldHeaderText)

	if !tt.HasText("git status") || !tt.HasText("go build ./...") {
		t.Fatalf("展开后应看到折叠区内的命令: %v", tt.Texts())
	}
	if !tt.HasText("MID-STATUS-TEXT") {
		t.Fatalf("展开后应看到被切断的中途正文: %v", tt.Texts())
	}

	// 再点一次收起，回到只剩组头
	clickText(t, tt, turnWorkFoldHeaderText)
	if tt.HasText("git status") {
		t.Fatalf("再次点击应收起折叠区: %v", tt.Texts())
	}
}

// TestTimelineRows_TurnWorkFoldKeepsSplitToolsInOneFold 验证被中途正文切断的工具调用归入同一个折叠项，
// 而不是各自成折叠项。
func TestTimelineRows_TurnWorkFoldKeepsSplitToolsInOneFold(t *testing.T) {
	msgs := turnWorkSplitToolMessages()
	items := buildTimelineItems(msgs)
	if len(items) < 4 {
		t.Fatalf("折叠前应有用户消息、两个工具组、中途正文与最终正文，got %d 项", len(items))
	}
	s := newChatState(NewFakeClient())
	s.mu.Lock()
	s.messages = msgs
	s.mu.Unlock()

	folded := applyTurnWorkFold(s, "t1", items)
	var folds []timelineItem
	for _, it := range folded {
		if it.kind == timelineItemTurnWork {
			folds = append(folds, it)
		}
	}
	if len(folds) != 1 {
		t.Fatalf("第一回合应折叠成 1 项，got %d 项", len(folds))
	}
	fold := folds[0]
	if fold.id != turnWorkFoldID("u1") {
		t.Fatalf("折叠项 key = %q，want %q", fold.id, turnWorkFoldID("u1"))
	}
	groups, commands := 0, 0
	for _, inner := range fold.workItems {
		switch inner.kind {
		case timelineItemToolGroup:
			groups++
		case timelineItemSingle:
			if inner.msg.Role == "tool" {
				commands++
			}
		}
	}
	// 被中途正文切开的两条命令都是活动消息：前一条与思考合成工具组，后一条单独成行，
	// 但两者都必须落进同一个折叠项。
	if groups != 1 || commands != 1 {
		t.Fatalf("折叠项内应有 1 个工具组与 1 条单独命令，got groups=%d commands=%d", groups, commands)
	}
	if !containsMessageID(fold.tools, "t1") || !containsMessageID(fold.tools, "t2") {
		t.Fatalf("被正文切断的两条命令应归入同一折叠项，got %d 条", len(fold.tools))
	}
	if got := turnWorkHeaderText(countToolGroupCategories(fold.tools), fold.workElapsed); got != turnWorkFoldHeaderText {
		t.Fatalf("折叠项组头 = %q，want %q", got, turnWorkFoldHeaderText)
	}
	// 折叠区覆盖 r1 到第二条命令：msgIndex 指向折叠区内第一条消息，msgLastIndex 指向第二条命令。
	if fold.msgIndex != 1 || fold.msgLastIndex != 4 {
		t.Fatalf("折叠项下标 = [%d,%d]，want [1,4]", fold.msgIndex, fold.msgLastIndex)
	}
	// 最终正文仍在折叠项之后，与折叠前位置一致
	answerIdx := -1
	for i, it := range folded {
		if it.kind == timelineItemTurnWork {
			answerIdx = i
			continue
		}
		if answerIdx >= 0 && it.kind == timelineItemSingle && it.msg.ID == "a1" {
			break
		}
		if answerIdx >= 0 && it.kind == timelineItemSingle && it.msg.ID != "a1" {
			t.Fatalf("折叠项与最终正文之间不应插入其它条目: %q", it.id)
		}
	}
}

// TestTimelineRows_TurnWorkFoldUnfoldWithExpandedToolGroup 验证折叠区里的工具组已经处于展开状态时，
// 展开折叠项不会把同一个节拍渲染两次（同一父节点下的 Key 重复会直接 panic）。
// 真实使用中「跳转到折叠区内的消息」会同时打开折叠项与它内部的工具组，两条路径重叠就踩到这个坑。
func TestTimelineRows_TurnWorkFoldUnfoldWithExpandedToolGroup(t *testing.T) {
	fake := NewFakeClient()
	fake.messages["t1"] = turnWorkSplitToolMessages()
	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("FOLD-ANSWER-ONE") })

	// 折叠项与它内部的工具组同时展开：工具组 ID 由组内首条活动消息生成。
	state.setToolGroupOpen(turnWorkFoldID("u1"), true)
	state.setToolGroupOpen("toolgroup-r1", true)
	for i := 0; i < 4; i++ {
		tt.Frame()
	}

	// 每个条目只能出现一次，并且命令与中途正文都还在折叠区里。
	for _, want := range []string{"git status", "go build ./...", "MID-STATUS-TEXT"} {
		if !tt.HasText(want) {
			t.Fatalf("展开折叠项后应看到折叠区里的 %q: %v", want, tt.Texts())
		}
	}
}

// TestTimelineRows_TurnWorkFoldKeepsRunningTurn 验证运行中的回合不折叠。
func TestTimelineRows_TurnWorkFoldKeepsRunningTurn(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "跑一下构建", TurnIndex: 1, IsFinal: true},
		{ID: "t1", Role: "tool", Text: `Bash {"command":"go build ./..."}`, TurnIndex: 1, ToolStatus: "completed"},
		{ID: "a1", Role: "assistant", Kind: "text", Text: "FOLD-RUNNING-ANSWER", TurnIndex: 1, IsFinal: true},
	}
	running := Thread{ID: "t1", ProjectID: "p1", Title: "运行中", Provider: "claude", UpdatedAt: time.Now(), IsRunning: true}
	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1", Path: "/workspace"}},
		map[string][]Thread{"p1": {running}},
	)
	fake.messages["t1"] = msgs
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("go build ./...") })

	if !tt.HasText("已处理") {
		t.Fatalf("运行中的回合应显示运行态回合头: %v", tt.Texts())
	}
	if tt.HasText("用时") {
		t.Fatalf("运行中的回合不应出现可折叠的用时头: %v", tt.Texts())
	}
}

// TestTimelineRows_TurnWorkFoldShowsAttentionOnFailedTool 验证含失败工具的回合在组头给出警示标记。
func TestTimelineRows_TurnWorkFoldShowsAttentionOnFailedTool(t *testing.T) {
	msgs := turnWorkSplitToolMessages()
	msgs[2].ToolStatus = "failed"
	tt := newTimelineTester(t, msgs, "FOLD-ANSWER-ONE")

	if !tt.HasText(turnWorkFoldHeaderText) {
		t.Fatalf("缺少回合折叠组头 %q，当前文本 %v", turnWorkFoldHeaderText, tt.Texts())
	}
	if !tt.HasText("工具执行失败") {
		t.Fatalf("含失败工具时组头应有警示标记: %v", tt.Texts())
	}
}

// TestTimelineRows_TurnWorkFoldSkipsTurnsWithoutBody 验证没有助手正文的回合不折叠。
func TestTimelineRows_TurnWorkFoldSkipsTurnsWithoutBody(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "跑一下构建", TurnIndex: 1, IsFinal: true},
		{ID: "t1", Role: "tool", Text: `Bash {"command":"go build ./..."}`, TurnIndex: 1, ToolStatus: "completed"},
		{ID: "t2", Role: "tool", Text: `Bash {"command":"go vet ./..."}`, TurnIndex: 1, ToolStatus: "completed"},
		{ID: "a1", Role: "assistant", Kind: "text", Text: "   ", TurnIndex: 1, IsFinal: true},
	}
	s := newChatState(NewFakeClient())
	s.mu.Lock()
	s.messages = msgs
	s.mu.Unlock()

	folded := applyTurnWorkFold(s, "t1", buildTimelineItems(msgs))
	for _, it := range folded {
		if it.kind == timelineItemTurnWork {
			t.Fatalf("没有助手正文的回合不应折叠，got 折叠项 %q", it.id)
		}
	}
}

// TestTimelineRows_TurnWorkFoldSkippedWhenFindMatchesInside 验证查找命中落在折叠区内时不折叠，
// 否则高亮与跳转会落到看不见的行上。
func TestTimelineRows_TurnWorkFoldSkippedWhenFindMatchesInside(t *testing.T) {
	msgs := turnWorkSplitToolMessages()
	s := newChatState(NewFakeClient())
	s.mu.Lock()
	s.messages = msgs
	s.mu.Unlock()
	items := buildTimelineItems(msgs)

	st := getChatFindState(s)
	// 查找只搜用户与助手正文，命中落在折叠区内的中途正文上。
	st.query = "MID-STATUS-TEXT"
	for _, it := range applyTurnWorkFold(s, "t1", items) {
		if it.kind == timelineItemTurnWork {
			t.Fatalf("折叠区内有查找命中时不应折叠")
		}
	}

	st.query = ""
	found := false
	for _, it := range applyTurnWorkFold(s, "t1", items) {
		if it.kind == timelineItemTurnWork {
			found = true
		}
	}
	if !found {
		t.Fatalf("清除查找条件后应恢复折叠")
	}
}

// TestTimelineRows_TurnWorkUnfoldAnimatesHeight 验证展开折叠区时高度从 0 过渡到完整高度：
// 用折叠项下方最终正文相对组头的纵向距离作为高度代理，逐帧测中间值。
func TestTimelineRows_TurnWorkUnfoldAnimatesHeight(t *testing.T) {
	tt := newTimelineTester(t, turnWorkSplitToolMessages(), "FOLD-ANSWER-ONE")

	distance := func() float32 {
		t.Helper()
		header, ok := tt.Find(turnWorkFoldHeaderText)
		if !ok {
			t.Fatalf("找不到折叠组头，当前文本 %v", tt.Texts())
		}
		answer, ok := tt.Find("FOLD-ANSWER-ONE")
		if !ok {
			t.Fatalf("找不到最终正文，当前文本 %v", tt.Texts())
		}
		return answer.Y - header.Y
	}

	collapsed := distance()
	clickText(t, tt, turnWorkFoldHeaderText)
	start := distance()

	tt.Advance(motionUnfold / 2)
	middle := distance()

	tt.Advance(motionUnfold)
	done := distance()

	if !(start <= collapsed) {
		t.Fatalf("刚点开时高度应还接近收起态：start=%v collapsed=%v", start, collapsed)
	}
	if !(middle > start && middle < done) {
		t.Fatalf("展开途中高度应介于 0 与完整高度之间：start=%v middle=%v done=%v", start, middle, done)
	}
}

// TestToolCallVerbTense_ChineseStates 验证动作词时态：运行中用「正在…」，结束后用「已…」，
// 未知动词、命令与失败状态保持原样。
func TestToolCallVerbTense_ChineseStates(t *testing.T) {
	cases := []struct {
		verb    string
		running bool
		failed  bool
		want    string
	}{
		{"读取", true, false, "正在读取"},
		{"读取", false, false, "已读取"},
		{"编辑", true, false, "正在编辑"},
		{"编辑", false, false, "已编辑"},
		{"新建", true, false, "正在新建"},
		{"新建", false, false, "已新建"},
		{"搜索", true, false, "正在搜索"},
		{"搜索", false, false, "已搜索"},
		{"读取", true, true, "已读取"},
		{"$", true, false, "$"},
		{"mcp__foo", true, false, "mcp__foo"},
	}
	for _, tc := range cases {
		if got := toolCallVerbTense(tc.verb, tc.running, tc.failed); got != tc.want {
			t.Fatalf("toolCallVerbTense(%q, running=%v, failed=%v) = %q, want %q", tc.verb, tc.running, tc.failed, got, tc.want)
		}
	}
}

// TestTimelineRows_ToolChipVerbTense 验证动作胶囊上的动词随工具状态变化。
func TestTimelineRows_ToolChipVerbTense(t *testing.T) {
	base := []Message{
		{ID: "u1", Role: "user", Text: "看一下这个文件", TurnIndex: 1, IsFinal: true},
		{ID: "t1", Role: "tool", Text: `Read {"file_path":"/workspace/main.go"}`, TurnIndex: 1, ToolStatus: "running"},
	}
	runningTT := newTimelineTester(t, base, "正在读取")
	if !runningTT.HasText("正在读取") {
		t.Fatalf("运行中的工具行动词应为「正在读取」: %v", runningTT.Texts())
	}

	done := []Message{
		{ID: "u1", Role: "user", Text: "看一下这个文件", TurnIndex: 1, IsFinal: true},
		{ID: "t1", Role: "tool", Text: `Read {"file_path":"/workspace/main.go"}`, TurnIndex: 1, ToolStatus: "completed"},
	}
	doneTT := newTimelineTester(t, done, "已读取")
	if !doneTT.HasText("已读取") {
		t.Fatalf("完成后的工具行动词应为「已读取」: %v", doneTT.Texts())
	}
	if doneTT.HasText("正在读取") {
		t.Fatalf("完成后不应残留运行中的动词: %v", doneTT.Texts())
	}
}

// TestTurnWorkLastAnswers 验证折叠用的「回合最后一条助手正文」只认非思考、非空正文的消息。
func TestTurnWorkLastAnswers(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "问题", TurnIndex: 1},
		{ID: "a1", Role: "assistant", Kind: "text", Text: "先说一句", TurnIndex: 1},
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "想一想", TurnIndex: 1},
		{ID: "a2", Role: "assistant", Kind: "text", Text: "最终答复", TurnIndex: 1},
		{ID: "a3", Role: "assistant", Kind: "text", Text: "   ", TurnIndex: 2},
	}
	got := turnWorkLastAnswers(buildTimelineItems(msgs))
	want := map[int]string{1: "a2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("turnWorkLastAnswers() = %v, want %v", got, want)
	}
}

// TestTimelineRows_ToolChipShimmer 验证动词扫光只在工具运行时出现：
// 运行中的胶囊画出扫光条，完成后同一个胶囊不再画（高光由 ui.Keyframes 驱动，不再用 c.After 轮询重建整个时间线）。
func TestTimelineRows_ToolChipShimmer(t *testing.T) {
	running := []Message{
		{ID: "u1", Role: "user", Text: "看一下这个文件", TurnIndex: 1, IsFinal: true},
		{ID: "t1", Role: "tool", Text: `Read {"file_path":"/workspace/main.go"}`, TurnIndex: 1, ToolStatus: "running"},
	}
	runningTT := newTimelineTester(t, running, "正在读取")
	if _, ok := runningTT.Find(toolShimmerBandLabel); !ok {
		t.Fatalf("运行中的工具胶囊应渲染扫光条，当前文本 %v", runningTT.Texts())
	}

	done := []Message{
		{ID: "u1", Role: "user", Text: "看一下这个文件", TurnIndex: 1, IsFinal: true},
		{ID: "t1", Role: "tool", Text: `Read {"file_path":"/workspace/main.go"}`, TurnIndex: 1, ToolStatus: "completed"},
	}
	doneTT := newTimelineTester(t, done, "已读取")
	if _, ok := doneTT.Find(toolShimmerBandLabel); ok {
		t.Fatalf("完成后不应再渲染扫光条，当前文本 %v", doneTT.Texts())
	}
}
