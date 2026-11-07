package mcodeui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"mcode/internal/config"
)

// stewardWakeEvent 是被唤醒正文里的一条事件，字段与 internal/steward/wake.go 的事件行对应。
type stewardWakeEvent struct {
	id    string
	kind  string
	title string
}

// stewardWakeMessageText 构造贴近 internal/steward/wake.go buildWakeMessage 的唤醒正文：
// 事件行形如「1. 事件 ev-1（看板任务失败）」，标题行缩进四格写在事件行下面。
func stewardWakeMessageText(events ...stewardWakeEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "有 %d 条新情况，需要你先判断一下。\n\n", len(events))
	for i, ev := range events {
		fmt.Fprintf(&b, "%d. 事件 %s（%s）\n", i+1, ev.id, ev.kind)
		if ev.title != "" {
			fmt.Fprintf(&b, "   标题：%s\n", ev.title)
		}
		b.WriteString("   详情：后台任务连续失败两次\n")
	}
	b.WriteString("\n请先自己判断：需要动手就处理，不需要就回 [[静默]]。")
	return b.String()
}

// stewardWakeTestMessage 造一条隐藏的总管唤醒消息。
func stewardWakeTestMessage(id string, turn int, events ...stewardWakeEvent) Message {
	return Message{ID: id, Role: "user", Kind: stewardWakeKind, Text: stewardWakeMessageText(events...), TurnIndex: turn}
}

// TestStewardWakeReason 验证唤醒原因的解析：单事件、多事件、无标题、兜底巡检与解析不到。
func TestStewardWakeReason(t *testing.T) {
	single := stewardWakeMessageText(stewardWakeEvent{id: "ev-1", kind: "看板任务失败", title: "登录重构"})
	if got, want := stewardWakeReason(single), "因「登录重构」被唤醒"; got != want {
		t.Fatalf("单事件唤醒原因 = %q, want %q", got, want)
	}

	multi := stewardWakeMessageText(
		stewardWakeEvent{id: "ev-1", kind: "看板任务失败", title: "登录重构"},
		stewardWakeEvent{id: "ev-2", kind: "自动化失败", title: "每日构建"},
	)
	if got, want := stewardWakeReason(multi), "因「登录重构」等 2 件事被唤醒"; got != want {
		t.Fatalf("多事件唤醒原因 = %q, want %q", got, want)
	}

	// 第一条事件没有标题：退回它括号里的类型。
	noTitle := stewardWakeMessageText(
		stewardWakeEvent{id: "ev-1", kind: "会话失败", title: ""},
		stewardWakeEvent{id: "ev-2", kind: "自动化失败", title: "每日构建"},
	)
	if got, want := stewardWakeReason(noTitle), "因「会话失败」等 2 件事被唤醒"; got != want {
		t.Fatalf("无标题唤醒原因 = %q, want %q", got, want)
	}

	// 兜底巡检不用解析事件行。
	patrol := "兜底巡检：目前有 2 条待处理事件尚未消解。\n\n1. 事件 ev-9（看板任务失败）\n"
	if got, want := stewardWakeReason(patrol), "因兜底巡检被唤醒"; got != want {
		t.Fatalf("兜底巡检唤醒原因 = %q, want %q", got, want)
	}

	// 解析不到事件行：返回空串，组头就不加唤醒原因。
	for _, text := range []string{"", "随便一段没有事件行的话"} {
		if got := stewardWakeReason(text); got != "" {
			t.Fatalf("无法解析的正文 %q 应返回空串，实际 %q", text, got)
		}
	}

	// 超长标题按 rune 截断并补省略号。
	long := strings.Repeat("很长的标题", 10)
	got := stewardWakeReason(stewardWakeMessageText(stewardWakeEvent{id: "ev-1", kind: "看板任务失败", title: long}))
	if want := "因「" + truncateRunes(long, stewardWakeReasonRunes) + "」被唤醒"; got != want {
		t.Fatalf("超长标题应截断并补省略号，got %q want %q", got, want)
	}

	// 事件太多时正文只列前若干条：事件数以抬头里的总数和实际事件行数的较大值为准。
	truncated := strings.Replace(
		stewardWakeMessageText(
			stewardWakeEvent{id: "ev-1", kind: "看板任务失败", title: "登录重构"},
			stewardWakeEvent{id: "ev-2", kind: "自动化失败", title: "每日构建"},
		),
		"有 2 条新情况", "有 12 条新情况", 1,
	)
	if got, want := stewardWakeReason(truncated), "因「登录重构」等 12 件事被唤醒"; got != want {
		t.Fatalf("抬头总数大于事件行数时唤醒原因 = %q, want %q", got, want)
	}
}

// TestReasoningHeadline 验证单行思考摘要：首行、末行、Markdown 符号与超长截断。
func TestReasoningHeadline(t *testing.T) {
	text := "先看看板任务\n再动手改代码"
	if got, want := reasoningHeadline(text, false), "先看看板任务"; got != want {
		t.Fatalf("首行摘要 = %q, want %q", got, want)
	}
	if got, want := reasoningHeadline(text, true), "再动手改代码"; got != want {
		t.Fatalf("末行摘要 = %q, want %q", got, want)
	}

	// 多级标记叠在一起（引用里再列一项）也能剥干净。
	if got, want := reasoningHeadline("> - **要点一**", false), "要点一"; got != want {
		t.Fatalf("多层 Markdown 标记应被剥掉，got %q want %q", got, want)
	}
	md := "# 标题\n\n- **要点一**\n- 要点二"
	if got, want := reasoningHeadline(md, false), "标题"; got != want {
		t.Fatalf("Markdown 符号应被剥掉，got %q, want %q", got, want)
	}
	if got, want := reasoningHeadline(md, true), "要点二"; got != want {
		t.Fatalf("末行摘要应剥掉 Markdown 符号，got %q, want %q", got, want)
	}

	// 流式中最后一行常常是空的（换行刚写出来），要跳过空行往前找。
	trailing := "先想一想\n\n   \n"
	if got, want := reasoningHeadline(trailing, true), "先想一想"; got != want {
		t.Fatalf("末行跳过空行后 = %q, want %q", got, want)
	}

	long := strings.Repeat("长", 80)
	got := reasoningHeadline(long, false)
	if len([]rune(got)) != 61 || !strings.HasSuffix(got, "…") {
		t.Fatalf("超长摘要应截断到 60 个 rune 并补省略号，实际 %d 个字符: %q", len([]rune(got)), got)
	}

	for _, empty := range []string{"", "  \n\n", "###\n**"} {
		if got := reasoningHeadline(empty, false); got != "" {
			t.Fatalf("空文本 %q 应返回空串，实际 %q", empty, got)
		}
	}
}

// TestReasoningHoldOpen 验证「刚结束流式输出」时是否暂时保持展开的全部分支。
func TestReasoningHoldOpen(t *testing.T) {
	cases := []struct {
		name         string
		wasLive      bool
		autoCollapse bool
		atEnd        bool
		wantHold     bool
		wantForget   bool
	}{
		{"从未流式", false, true, true, false, false},
		{"从未流式且关闭自动折叠", false, false, false, false, false},
		{"自动折叠且用户不在底部", true, true, false, true, false},
		{"自动折叠且用户在底部", true, true, true, false, true},
		{"关闭自动折叠", true, false, true, false, true},
	}
	for _, tc := range cases {
		hold, forget := reasoningHoldOpen(tc.wasLive, tc.autoCollapse, tc.atEnd)
		if hold != tc.wantHold || forget != tc.wantForget {
			t.Fatalf("[%s] reasoningHoldOpen(%v, %v, %v) = (%v, %v), want (%v, %v)",
				tc.name, tc.wasLive, tc.autoCollapse, tc.atEnd, hold, forget, tc.wantHold, tc.wantForget)
		}
	}
}

// stewardWakeTurnMessages 造一个完整的唤醒回合：隐藏唤醒、思考、两条命令、最终答复。
func stewardWakeTurnMessages() []Message {
	return []Message{
		{ID: "u1", Role: "user", Text: "先了解一下仓库", TurnIndex: 1, IsFinal: true},
		{ID: "a1", Role: "assistant", Kind: "text", Text: "看完了。", TurnIndex: 1, IsFinal: true},
		stewardWakeTestMessage("w2", 2, stewardWakeEvent{id: "ev-1", kind: "看板任务失败", title: "登录重构"}),
		{ID: "r2", Role: "assistant", Kind: "reasoning", Text: "先看看板任务\n再改代码", TurnIndex: 2},
		{ID: "t2a", Role: "tool", Text: `Bash {"command":"kanban list"}`, TurnIndex: 2, ToolStatus: "completed"},
		{ID: "t2b", Role: "tool", Text: `Read {"file_path":"/workspace/login.go"}`, TurnIndex: 2, ToolStatus: "completed"},
		{ID: "a2", Role: "assistant", Kind: "text", Text: "STEWARD-WAKE-ANSWER", TurnIndex: 2, IsFinal: true},
	}
}

// TestStewardTimeline_WakeTurnFolds 验证唤醒回合折叠成一项：答复留在折叠项之外，
// ID 来自唤醒消息，组头带上唤醒原因。
func TestStewardTimeline_WakeTurnFolds(t *testing.T) {
	msgs := stewardWakeTurnMessages()
	items := applyTurnWorkFold(&chatState{}, "steward-thread", buildTimelineItems(msgs, nil))

	// 第一回合不折叠（只有用户消息与答复），第二回合的唤醒、思考、工具收进一项，答复留在外面。
	if len(items) != 4 {
		t.Fatalf("唤醒回合应折叠成一项且答复留在外面，实际 %s", timelineItemIDs(items))
	}
	fold := items[2]
	if fold.kind != timelineItemTurnWork {
		t.Fatalf("唤醒回合应折叠，实际第 3 项类型 %v (%s)", fold.kind, timelineItemIDs(items))
	}
	if want := turnWorkFoldID("w2"); fold.id != want {
		t.Fatalf("折叠项 ID 应基于唤醒消息，got %q want %q", fold.id, want)
	}
	if fold.turnIndex != 2 {
		t.Fatalf("折叠项回合号 = %d, want 2", fold.turnIndex)
	}
	if want := "因「登录重构」被唤醒"; fold.wakeReason != want {
		t.Fatalf("唤醒原因 = %q, want %q", fold.wakeReason, want)
	}
	// 折叠区里是这一个工具组（思考与两条命令同属一组），最终答复不在里面。
	groups := 0
	for _, it := range fold.workItems {
		if it.kind == timelineItemToolGroup {
			groups++
		}
	}
	if groups != 1 {
		t.Fatalf("折叠区应含一个工具组，实际 %d 个 (%s)", groups, timelineItemIDs(fold.workItems))
	}
	if len(fold.tools) != 3 {
		t.Fatalf("折叠区应含思考与两条命令，实际 %d 条", len(fold.tools))
	}
	if items[3].msg.ID != "a2" {
		t.Fatalf("最终答复应留在折叠项之外，实际 %s", timelineItemIDs(items))
	}

	// 组头只留用时（没有时间戳时写「过程」）；唤醒原因与计数移到 Tooltip，不再占组头。
	header := turnWorkHeaderLabel(fold, countToolGroupCategories(turnWorkSummaryTools(&chatState{}, fold.tools)))
	if strings.Contains(header, "被唤醒") || strings.Contains(header, "思考") {
		t.Fatalf("组头应只留用时，唤醒原因与计数移到 Tooltip，实际 %q", header)
	}
}

// TestStewardTimeline_PlainTurnWithoutUserHead 验证没有唤醒消息、首行也不是用户消息的回合照旧不折叠。
func TestStewardTimeline_PlainTurnWithoutUserHead(t *testing.T) {
	msgs := []Message{
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "没有用户消息的开头", TurnIndex: 1},
		{ID: "t1", Role: "tool", Text: `Bash {"command":"ls"}`, TurnIndex: 1, ToolStatus: "completed"},
		{ID: "t2", Role: "tool", Text: `Bash {"command":"pwd"}`, TurnIndex: 1, ToolStatus: "completed"},
		{ID: "a1", Role: "assistant", Kind: "text", Text: "PLAIN-ANSWER", TurnIndex: 1, IsFinal: true},
	}
	items := applyTurnWorkFold(&chatState{}, "thread-1", buildTimelineItems(msgs, nil))
	for _, it := range items {
		if it.kind == timelineItemTurnWork {
			t.Fatalf("没有用户消息首行的普通回合不应折叠: %s", timelineItemIDs(items))
		}
	}
}

// stewardSilentRoundMessages 造一个静默回合：隐藏唤醒、思考、两条命令、[[静默]] 收尾。
func stewardSilentRoundMessages(turn int, marker string) []Message {
	return []Message{
		stewardWakeTestMessage(marker+"-w", turn, stewardWakeEvent{id: marker + "-ev", kind: "自动化失败", title: "每日构建"}),
		{ID: marker + "-r", Role: "assistant", Kind: "reasoning", Text: "看一眼事件\n没什么要做的", TurnIndex: turn},
		{ID: marker + "-t1", Role: "tool", Text: `Bash {"command":"kanban list"}`, TurnIndex: turn, ToolStatus: "completed"},
		{ID: marker + "-t2", Role: "tool", Text: `Bash {"command":"kanban show ev-1"}`, TurnIndex: turn, ToolStatus: "completed"},
		{ID: marker + "-a", Role: "assistant", Kind: "text", Text: stewardSilentMarker, TurnIndex: turn, IsFinal: true},
	}
}

// TestStewardTimeline_SilentRoundsMerge 验证相邻的两个静默回合合并成一项，巡视次数累加。
func TestStewardTimeline_SilentRoundsMerge(t *testing.T) {
	msgs := append(stewardSilentRoundMessages(2, "s1"), stewardSilentRoundMessages(3, "s2")...)
	items := applyTurnWorkFold(&chatState{}, "steward-thread", buildTimelineItems(msgs, nil))

	if len(items) != 1 {
		t.Fatalf("两个相邻静默回合应合并为一项，实际 %s", timelineItemIDs(items))
	}
	item := items[0]
	if item.kind != timelineItemTurnWork || item.silentRounds != 2 {
		t.Fatalf("静默回合合并项不对：kind=%v silentRounds=%d", item.kind, item.silentRounds)
	}
	if want := turnWorkFoldID("s1-w"); item.id != want {
		t.Fatalf("合并项的 ID 应保留首轮（唤醒消息 ID），got %q want %q", item.id, want)
	}
	if item.turnIndex != 3 {
		t.Fatalf("合并项的回合号应取最新一轮，got %d want 3", item.turnIndex)
	}
	// 每轮两条命令加一条思考，合并后四轮工具消息都在折叠区里。
	toolMsgs, reasoningMsgs := 0, 0
	for _, m := range item.tools {
		switch {
		case m.Role == "tool":
			toolMsgs++
		case isReasoningMessage(m):
			reasoningMsgs++
		}
	}
	if toolMsgs != 4 || reasoningMsgs != 2 {
		t.Fatalf("合并后应含两轮的工具与思考，实际工具 %d 条、思考 %d 条", toolMsgs, reasoningMsgs)
	}

	// 静默回合没有答复，不该挂回合完成块。
	if itemHoldsTurnEnd(item, map[int]string{3: "s2-a"}) {
		t.Fatalf("静默折叠项不应承载回合完成块")
	}

	// 组头文案：只说巡视次数，不带「已工作」。
	header := turnWorkHeaderLabel(item, countToolGroupCategories(turnWorkSummaryTools(&chatState{}, item.tools)))
	if want := "后台巡视 2 次，无需处理"; header != want {
		t.Fatalf("静默组头 = %q, want %q", header, want)
	}

	// 有完成时间时补上本地时刻。
	finished := time.Date(2026, 11, 1, 9, 42, 0, 0, time.Local)
	item.finishedAt = finished
	header = turnWorkHeaderLabel(item, countToolGroupCategories(turnWorkSummaryTools(&chatState{}, item.tools)))
	if want := "后台巡视 2 次，无需处理 · " + finished.Format("15:04"); header != want {
		t.Fatalf("带时间的静默组头 = %q, want %q", header, want)
	}
}

// TestStewardTimeline_SilentRoundsNotMergedAcrossAnswer 验证中间夹一个有答复的回合时，
// 前后两个静默回合不会合并。
func TestStewardTimeline_SilentRoundsNotMergedAcrossAnswer(t *testing.T) {
	msgs := stewardSilentRoundMessages(2, "s1")
	msgs = append(msgs,
		stewardWakeTestMessage("aw-w", 3, stewardWakeEvent{id: "ev-3", kind: "看板任务失败", title: "登录重构"}),
		Message{ID: "aw-r", Role: "assistant", Kind: "reasoning", Text: "改一下登录流程", TurnIndex: 3},
		Message{ID: "aw-t1", Role: "tool", Text: `Edit {"file_path":"/workspace/login.go"}`, TurnIndex: 3, ToolStatus: "completed"},
		Message{ID: "aw-t2", Role: "tool", Text: `Bash {"command":"go test ./..."}`, TurnIndex: 3, ToolStatus: "completed"},
		Message{ID: "aw-a", Role: "assistant", Kind: "text", Text: "ANSWERED-TURN", TurnIndex: 3, IsFinal: true},
	)
	msgs = append(msgs, stewardSilentRoundMessages(4, "s2")...)

	items := applyTurnWorkFold(&chatState{}, "steward-thread", buildTimelineItems(msgs, nil))

	silents := 0
	for _, it := range items {
		if it.silentRounds > 0 {
			silents++
			if it.silentRounds != 1 {
				t.Fatalf("夹了有答复的回合后两个静默回合不应合并，got silentRounds=%d", it.silentRounds)
			}
		}
	}
	if silents != 2 {
		t.Fatalf("应有两个各自独立的静默折叠项，实际 %d 个 (%s)", silents, timelineItemIDs(items))
	}
	// 有答复的回合照常折叠成带「用时」的折叠项，答复留在外面。
	answered := false
	for _, it := range items {
		if it.kind == timelineItemTurnWork && it.silentRounds == 0 && it.wakeReason == "因「登录重构」被唤醒" {
			answered = true
		}
	}
	if !answered {
		t.Fatalf("有答复的唤醒回合应折叠成带唤醒原因的折叠项: %s", timelineItemIDs(items))
	}
}

// TestStewardTimeline_RunningWakeTurnNotFolded 验证运行中的唤醒回合不折叠（哪怕暂时还没有答复）。
func TestStewardTimeline_RunningWakeTurnNotFolded(t *testing.T) {
	msgs := []Message{
		stewardWakeTestMessage("w2", 2, stewardWakeEvent{id: "ev-1", kind: "看板任务失败", title: "登录重构"}),
		{ID: "r2", Role: "assistant", Kind: "reasoning", Text: "先看看板任务", TurnIndex: 2},
		{ID: "t2", Role: "tool", Text: `Bash {"command":"kanban list"}`, TurnIndex: 2, ToolStatus: "running"},
	}
	s := &chatState{threads: []Thread{{ID: "steward-thread", IsRunning: true}}, messages: msgs}
	items := applyTurnWorkFold(s, "steward-thread", buildTimelineItems(msgs, nil))
	for _, it := range items {
		if it.kind == timelineItemTurnWork {
			t.Fatalf("运行中的唤醒回合不应折叠: %s", timelineItemIDs(items))
		}
	}
}

// TestStewardTimeline_IncrementalMatchesFull 验证唤醒与静默回合在消息逐条增长时，
// 增量重建与全量构建逐项一致。
func TestStewardTimeline_IncrementalMatchesFull(t *testing.T) {
	silentWithAnswer := stewardSilentRoundMessages(2, "s1")
	// 静默回合之后追加一条答复：摘要要从「后台巡视」变回「用时」，增量不能复用旧折叠项。
	silentWithAnswer = append(silentWithAnswer, Message{ID: "s1-late", Role: "assistant", Kind: "text", Text: "其实还是改了一行", TurnIndex: 2, IsFinal: true})

	cases := []struct {
		name string
		msgs []Message
	}{
		{"唤醒回合", stewardWakeTurnMessages()},
		{"两个静默回合", append(stewardSilentRoundMessages(2, "s1"), stewardSilentRoundMessages(3, "s2")...)},
		{"静默回合补答复", silentWithAnswer},
	}
	for _, tc := range cases {
		s := &chatState{}
		for i := 1; i <= len(tc.msgs); i++ {
			helperBuildAndAssertIncremental(t, s, "steward-thread", tc.msgs[:i], nil, tc.name)
		}
	}
}

// reasoningAutoCollapse 按会话类型取开关：总管会话只看总管自己的开关，其他会话只看通用开关，
// 关键是用量是「通用关掉 + 总管默认」时，总管仍折叠、普通会话展开。
func TestReasoningAutoCollapse(t *testing.T) {
	autoOff := false
	cases := []struct {
		name          string
		session       *bool
		stewardOff    bool
		isSteward     bool
		wantCollapsed bool
	}{
		{"通用默认/总管默认/总管会话", nil, false, true, true},
		{"通用默认/总管默认/普通会话", nil, false, false, true},
		{"通用关/总管默认/总管会话", &autoOff, false, true, true},
		{"通用关/总管默认/普通会话", &autoOff, false, false, false},
		{"通用默认/总管关/总管会话", nil, true, true, false},
		{"通用默认/总管关/普通会话", nil, true, false, true},
		{"通用关/总管关/总管会话", &autoOff, true, true, false},
		{"通用关/总管关/普通会话", &autoOff, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Session.AutoCollapseReasoning = tc.session
			cfg.Orchestration.Steward.AutoCollapseReasoningOff = tc.stewardOff
			if got := reasoningAutoCollapse(cfg, tc.isSteward); got != tc.wantCollapsed {
				t.Fatalf("reasoningAutoCollapse = %v, 期望 %v", got, tc.wantCollapsed)
			}
		})
	}
}
