package mcodeui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestTurnTimingAuthoritativeAndZeroIndex(t *testing.T) {
	start := "2026-11-06T08:00:00.123Z"
	end := "2026-11-06T08:00:12.123Z"
	msgs := []Message{
		{ID: "a", Role: "assistant", TurnID: "first", TurnIndex: 0, CreatedAt: "2026-11-06T08:00:02Z", TurnStartedAt: start, TurnCompletedAt: end},
		{ID: "other", Role: "user", TurnID: "other", TurnIndex: 0, CreatedAt: "2026-11-06T07:00:00Z"},
	}
	s := &chatState{messages: msgs}
	a, b := turnFinishedSpanFor(s, msgs[0])
	if b.Sub(a) != 12*time.Second || a.Format(time.RFC3339Nano) != start || b.Format(time.RFC3339Nano) != end {
		t.Fatalf("权威起止时间: %v %v", a, b)
	}
	if got := turnStartTime(msgs[:1], 0); !got.Equal(a) {
		t.Fatalf("运行起点: %v", got)
	}
	legacy := []Message{{Role: "user", TurnIndex: 0, CreatedAt: start}, {Role: "assistant", TurnIndex: 0, CreatedAt: end}}
	a, b = turnSpanFromMessages(legacy, 0)
	if b.Sub(a) != 12*time.Second {
		t.Fatalf("旧消息回退: %v %v", a, b)
	}
}

// turnHeaderFinishedMessages 造一个已结束、可折叠的回合：用户提问 + 思考 + 一条命令 + 最终答复。
// 时间戳让折叠头显示「用时 12s」，展开体尾部才能核对完成详情。
func turnHeaderFinishedMessages() []Message {
	start := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	at := func(offset time.Duration) string { return start.Add(offset).Format(time.RFC3339) }
	return []Message{
		{ID: "u1", Role: "user", Text: "跑一下构建", TurnIndex: 1, IsFinal: true, CreatedAt: at(0)},
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "先确认仓库\n再看构建脚本", TurnIndex: 1, CreatedAt: at(2 * time.Second)},
		{ID: "t1", Role: "tool", Text: `Bash {"command":"go build ./..."}`, TurnIndex: 1, ToolStatus: "completed", CreatedAt: at(4 * time.Second)},
		{ID: "a1", Role: "assistant", Kind: "text", Text: "构建通过", TurnIndex: 1, IsFinal: true, CreatedAt: at(12 * time.Second)},
	}
}

// newTurnHeaderUsageTester 渲染带单轮用量的会话：有用量才会画完成行或折叠头尾部的详情行。
func newTurnHeaderUsageTester(t *testing.T, msgs []Message, turns map[int]TokenUsage, ready string) *ui.Tester {
	t.Helper()
	fake := NewFakeClient()
	fake.messages["t1"] = msgs
	fake.SetThreadUsage("t1", ThreadUsage{Turns: turns})
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText(ready) })
	return tt
}

// newTurnHeaderRunningTester 渲染一个仍在运行的会话，用来核对运行态回合头。
func newTurnHeaderRunningTester(t *testing.T, msgs []Message, ready string) *ui.Tester {
	t.Helper()
	running := Thread{ID: "t1", ProjectID: "p1", Title: "运行中", Provider: "claude", UpdatedAt: time.Now(), IsRunning: true}
	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1", Path: "/workspace"}},
		map[string][]Thread{"p1": {running}},
	)
	fake.messages["t1"] = msgs
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText(ready) })
	return tt
}

// TestReasoningHeaderTitle 验证思考行标题的「标题 · 要旨」格式、清洗规则与空正文兜底。
func TestReasoningHeaderTitle(t *testing.T) {
	long := strings.Repeat("要", reasoningHeadlineRunes+5)
	cases := []struct {
		name      string
		text      string
		streaming bool
		want      string
	}{
		{"首行做要旨", "先确认仓库结构\n再看构建脚本", false, "思考过程 · 先确认仓库结构"},
		{"流式取最后一行", "先确认仓库结构\n再看构建脚本", true, "思考中… · 再看构建脚本"},
		{"清洗加粗与反引号", "**先确认 `go.mod`**", false, "思考过程 · 先确认 go.mod"},
		{"剥掉引用与列表序号", "> 1. 先看仓库", false, "思考过程 · 先看仓库"},
		{"小数不当列表", "1.5 秒后重试", false, "思考过程 · 1.5 秒后重试"},
		{"过长截断", long, false, "思考过程 · " + string([]rune(long)[:reasoningHeadlineRunes]) + "…"},
		{"空正文只留标题", " \n\t", false, "思考过程"},
		{"空正文流式只留标题", "", true, "思考中…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reasoningHeaderTitle(tc.text, tc.streaming); got != tc.want {
				t.Fatalf("reasoningHeaderTitle(%q, %v) = %q, want %q", tc.text, tc.streaming, got, tc.want)
			}
		})
	}
}

// TestTimelineTurnHeader_ReasoningShowsGist 验证思考结束后标题显示清洗后的首行要旨，正文只在展开后出现。
func TestTimelineTurnHeader_ReasoningShowsGist(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "你好", TurnIndex: 1, IsFinal: true},
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "**先确认 go.mod**\n完整正文-REASONING", TurnIndex: 1, IsFinal: true},
	}
	tt := newTimelineTester(t, msgs, "思考过程 · 先确认 go.mod")
	if tt.HasText("完整正文-REASONING") {
		t.Fatalf("未展开时不应显示思考正文: %v", tt.Texts())
	}
	clickText(t, tt, "思考过程 · 先确认 go.mod")
	if !tt.HasText("完整正文-REASONING") {
		t.Fatalf("展开后应看到完整思考正文: %v", tt.Texts())
	}
}

// TestToolGroupHeaderSummaryRunningCommands 验证工具组头在运行中追加正在运行的命令片段，
// 并行时只显示最后一条并补「等 N 条」，跑完后自动恢复成「用时 …」。
func TestToolGroupHeaderSummaryRunningCommands(t *testing.T) {
	one := []Message{
		{ID: "t1", Role: "tool", Text: `Bash {"command":"npm test"}`, ToolStatus: "running"},
	}
	got := toolGroupHeaderSummaryText(countToolGroupCategories(one), true, "", one)
	if !strings.Contains(got, "· 正在运行 npm test") {
		t.Fatalf("单条运行命令应追加命令片段，实际 %q", got)
	}
	if strings.Contains(got, "等 ") {
		t.Fatalf("只有一条命令时不应显示并行条数，实际 %q", got)
	}

	parallel := []Message{
		{ID: "t1", Role: "tool", Text: `Bash {"command":"npm test"}`, ToolStatus: "running"},
		{ID: "t2", Role: "tool", Text: `Bash {"command":"go build"}`, ToolStatus: "running"},
	}
	got = toolGroupHeaderSummaryText(countToolGroupCategories(parallel), true, "", parallel)
	if !strings.Contains(got, "· 正在运行 go build") {
		t.Fatalf("并行时应显示最后一条运行命令，实际 %q", got)
	}
	if !strings.Contains(got, "等 2 条") {
		t.Fatalf("并行多条时应补「等 2 条」，实际 %q", got)
	}
	if strings.Contains(got, "npm test") {
		t.Fatalf("并行时不应把前一条命令也塞进组头，实际 %q", got)
	}

	done := []Message{
		{ID: "t1", Role: "tool", Text: `Bash {"command":"npm test"}`, ToolStatus: "completed"},
	}
	got = toolGroupHeaderSummaryText(countToolGroupCategories(done), false, "3.2s", done)
	if strings.Contains(got, "正在运行") {
		t.Fatalf("命令跑完后组头不应再显示运行提示，实际 %q", got)
	}
	if want := "用时 3.2s · 运行 1 条命令"; got != want {
		t.Fatalf("跑完后组头 = %q，want %q", got, want)
	}
}

// TestTimelineTurnHeader_FinishedTurnKeepsSingleHeader 验证结束态只有一行可折叠的「用时」头：
// 页面不再有第二条「已完成」行，展开后详情行出现在折叠体尾部。
func TestTimelineTurnHeader_FinishedTurnKeepsSingleHeader(t *testing.T) {
	msgs := turnHeaderFinishedMessages()
	usage := map[int]TokenUsage{1: {Input: 1200, Output: 356, DurationMs: 12000}}
	tt := newTurnHeaderUsageTester(t, msgs, usage, "用时 12s")

	if tt.HasText("已完成 ·") {
		t.Fatalf("折叠态不应再出现第二条完成行: %v", tt.Texts())
	}
	if tt.HasText("已工作") {
		t.Fatalf("文案应统一为「用时」，不应残留「已工作」: %v", tt.Texts())
	}
	if !tt.HasText("构建通过") {
		t.Fatalf("最终答复应留在折叠头之外: %v", tt.Texts())
	}

	clickText(t, tt, "用时 12s")
	if !tt.HasText("已完成 ·") {
		t.Fatalf("展开后应在折叠体尾部显示完成详情: %v", tt.Texts())
	}
	if !tt.HasText("输出 356") {
		t.Fatalf("展开后的详情行不应丢 token 信息: %v", tt.Texts())
	}
	if !tt.HasText("go build") {
		t.Fatalf("展开后应看到被收起的工作过程: %v", tt.Texts())
	}
}

// TestTimelineTurnHeader_TooltipKeepsDetails 验证组头不再显示的计数、唤醒原因与 token/时间明细都在 Tooltip 里。
func TestTimelineTurnHeader_TooltipKeepsDetails(t *testing.T) {
	msgs := turnHeaderFinishedMessages()
	usage := TokenUsage{Input: 1200, Output: 356, DurationMs: 12000}
	s := &chatState{messages: msgs}
	items := applyTurnWorkFold(s, "t1", buildTimelineItems(msgs))
	var fold timelineItem
	for _, it := range items {
		if it.kind == timelineItemTurnWork {
			fold = it
		}
	}
	if fold.id == "" {
		t.Fatalf("已结束回合应折叠成一项: %v", timelineItemIDs(items))
	}
	counts := countToolGroupCategories(turnWorkSummaryTools(s, fold.tools))
	tooltip := turnWorkHeaderTooltip(s, fold, counts, usage, true, time.Now())
	for _, want := range []string{"思考 1 次", "运行 1 条命令", "输入 1.2k · 输出 356 tokens", "开始 ", "结束 "} {
		if !strings.Contains(tooltip, want) {
			t.Fatalf("折叠头 Tooltip 缺少 %q:\n%s", want, tooltip)
		}
	}

	now := time.Date(2026, 11, 3, 10, 0, 30, 0, time.UTC)
	running := timelineItem{workRunning: true, workStartedAt: now.Add(-16 * time.Second)}
	if got := turnWorkHeaderTooltip(s, running, counts, usage, true, now); got != "本回合已处理 16s" {
		t.Fatalf("运行态 Tooltip = %q, want 本回合已处理 16s", got)
	}
	if got := turnHeaderRunningLabel(16*time.Second + 900*time.Millisecond); got != "已处理 16s" {
		t.Fatalf("运行计时只取整秒，got %q", got)
	}
}

// TestTimelineTurnHeader_RunningTurnYieldsShimmerToTool 验证运行态回合头只数秒：
// 本回合有工具胶囊在扫光时头不扫光，避免同一屏出现两处同步高光。
func TestTimelineTurnHeader_RunningTurnYieldsShimmerToTool(t *testing.T) {
	start := time.Now().Add(-16 * time.Second)
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "跑一下构建", TurnIndex: 1, IsFinal: true, CreatedAt: start.Format(time.RFC3339)},
		{ID: "t1", Role: "tool", Text: `Bash {"command":"go build ./..."}`, TurnIndex: 1, ToolStatus: "running"},
	}
	tt := newTurnHeaderRunningTester(t, msgs, "已处理")

	if tt.HasText("用时") {
		t.Fatalf("运行中的回合不应出现可折叠的用时头: %v", tt.Texts())
	}
	matched := false
	re := regexp.MustCompile(`已处理 \d+s`)
	for _, text := range tt.Texts() {
		if re.MatchString(text) {
			matched = true
		}
	}
	if !matched {
		t.Fatalf("运行态头应显示整秒计时: %v", tt.Texts())
	}
	if _, ok := tt.Find(turnHeaderShimmerLabel); ok {
		t.Fatalf("本回合已有工具在扫光，回合头不应再扫光: %v", tt.Texts())
	}
	if _, ok := tt.Find(toolShimmerBandLabel); !ok {
		t.Fatalf("运行中的工具胶囊应保留扫光: %v", tt.Texts())
	}
}

// TestTimelineTurnHeader_RunningTurnShimmersWithoutTool 验证没有工具在跑时运行态头自己扫光。
func TestTimelineTurnHeader_RunningTurnShimmersWithoutTool(t *testing.T) {
	start := time.Now().Add(-5 * time.Second)
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "想一想", TurnIndex: 1, IsFinal: true, CreatedAt: start.Format(time.RFC3339)},
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "先列出可能的方案", TurnIndex: 1},
	}
	tt := newTurnHeaderRunningTester(t, msgs, "已处理")

	if _, ok := tt.Find(turnHeaderShimmerLabel); !ok {
		t.Fatalf("没有工具在跑时运行态头应扫光: %v", tt.Texts())
	}
}

// TestTimelineTurnHeader_PlainAnswerTurnKeepsFinishedLine 验证纯答复回合没有可折叠过程，
// 仍保留原来的「已完成 · …」行，而不是硬造一个折叠头。
func TestTimelineTurnHeader_PlainAnswerTurnKeepsFinishedLine(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "你好", TurnIndex: 1, IsFinal: true},
		{ID: "a1", Role: "assistant", Kind: "text", Text: "你好呀", TurnIndex: 1, IsFinal: true},
	}
	tt := newTurnHeaderUsageTester(t, msgs, map[int]TokenUsage{1: {Input: 1200, Output: 356}}, "已完成 · 输出 356")

	if tt.HasText("用时") {
		t.Fatalf("纯答复回合不应出现折叠用时头: %v", tt.Texts())
	}
	if !tt.HasText("已完成 · 输出 356") {
		t.Fatalf("纯答复回合应保留完成行: %v", tt.Texts())
	}
}

// TestTimelineTurnHeader_AutoCollapseFollowsSetting 验证历史回合的默认折叠值沿用自动折叠设置，
// 没有另改默认值：开启时收起，关闭时展开并显示尾部详情。
func TestTimelineTurnHeader_AutoCollapseFollowsSetting(t *testing.T) {
	for _, tc := range []struct {
		name         string
		autoCollapse bool
		wantFooter   bool
	}{
		{"默认自动折叠时收起", true, false},
		{"关闭自动折叠时展开", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgs := turnHeaderFinishedMessages()
			s := newAutoCollapseTestState(t, tc.autoCollapse)
			s.messages = msgs
			items := applyTurnWorkFold(s, "t1", buildTimelineItems(msgs))
			var fold timelineItem
			for _, it := range items {
				if it.kind == timelineItemTurnWork {
					fold = it
				}
			}
			if fold.id == "" {
				t.Fatalf("已结束回合应折叠成一项: %v", timelineItemIDs(items))
			}
			usage := TokenUsage{Input: 1200, Output: 356, DurationMs: 12000}
			tt := ui.NewTester(func(c *ui.Context) {
				renderTimelineItem(c, s, fold, "", "", false, true, usage, 0)
			}, 800, 600)
			for i := 0; i < 4; i++ {
				tt.Frame()
			}
			if !tt.HasText("用时 12s") {
				t.Fatalf("结束态组头应显示用时: %v", tt.Texts())
			}
			if got := tt.HasText("已完成 ·"); got != tc.wantFooter {
				t.Fatalf("自动折叠=%v 时尾部详情可见性 = %v，want %v: %v", tc.autoCollapse, got, tc.wantFooter, tt.Texts())
			}
		})
	}
}

// TestTimelineTurnHeader_IncrementalRunningToFinished 验证增量重建在「运行中 → 追加工具 → 回合结束」的
// 每一步都与全量构建完全一致：运行态头只有一处，结束后换成唯一一个可折叠的用时头。
func TestTimelineTurnHeader_IncrementalRunningToFinished(t *testing.T) {
	threadID := "t1"
	start := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "跑一下构建", TurnIndex: 1, IsFinal: true, CreatedAt: start.Format(time.RFC3339)},
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "先确认仓库结构", TurnIndex: 1},
	}
	s := &chatState{
		threads:  []Thread{{ID: threadID, ProjectID: "p1", IsRunning: true}},
		messages: msgs,
	}
	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "运行中首帧")

	countRunningHeader := func() int {
		n := 0
		for _, it := range s.timelineCacheItems {
			if it.kind == timelineItemTurnWork && it.workRunning {
				n++
			}
		}
		return n
	}
	if got := countRunningHeader(); got != 1 {
		t.Fatalf("运行中的回合应有且只有一个运行态头，got %d: %+v", got, s.timelineCacheItems)
	}

	// 同一回合追加一条仍在运行的命令：运行态头应把新命令一起收进去，仍然只有一处。
	msgs = append(msgs, Message{ID: "t1", Role: "tool", Text: `Bash {"command":"go build ./..."}`, TurnIndex: 1, ToolStatus: "running"})
	s.messages = msgs
	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "运行中追加命令")
	if got := countRunningHeader(); got != 1 {
		t.Fatalf("追加命令后运行态头仍应只有一处，got %d: %+v", got, s.timelineCacheItems)
	}

	// 回合结束并落下最终答复：运行态头换成可折叠的用时头，收尾答复打上 workHeaderID 压掉第二条完成行。
	s.mu.Lock()
	s.threads[0].IsRunning = false
	s.mu.Unlock()
	msgs = append(msgs, Message{ID: "a1", Role: "assistant", Kind: "text", Text: "构建通过", TurnIndex: 1, IsFinal: true, CreatedAt: start.Add(12 * time.Second).Format(time.RFC3339)})
	s.messages = msgs
	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "回合结束")

	folds := 0
	var fold timelineItem
	headerID := ""
	for _, it := range s.timelineCacheItems {
		if it.kind == timelineItemTurnWork {
			folds++
			fold = it
		}
		if it.workHeaderID != "" {
			headerID = it.workHeaderID
		}
	}
	if folds != 1 || fold.workRunning {
		t.Fatalf("结束后应只剩一个已结束的折叠头，got folds=%d item=%+v", folds, fold)
	}
	if headerID != fold.id {
		t.Fatalf("收尾答复应挂上折叠头 ID %q，got %q", fold.id, headerID)
	}
	if got := turnWorkHeaderText(countToolGroupCategories(fold.tools), fold.workElapsed); got != "用时 12s" {
		t.Fatalf("增量重建后的组头文案 = %q，want 用时 12s", got)
	}
}

// TestTimelineBackgroundRow_ShowsLingeringCommand 验证回合结束后仍在跑的命令只留下工具行，
// 不再在时间线末尾冒出后台任务卡片——后台项改由输入区上方的实时胶囊展示，点开胶囊才渲染浮层。
// 胶囊正文变成「最早任务名 · 计时」，按住 Label 定位更稳。
func TestTimelineBackgroundRow_ShowsLingeringCommand(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "跑一下构建", TurnIndex: 1, IsFinal: true},
		{ID: "t1", Role: "tool", Text: `Bash {"command":"npm test"}`, TurnIndex: 1, ToolStatus: "running"},
		{ID: "a1", Role: "assistant", Kind: "text", Text: "构建通过", TurnIndex: 1, IsFinal: true},
	}
	tt := newTimelineTester(t, msgs, "构建通过")

	// 后台任务胶囊仍按「后台任务 · 1 个运行中」的 Label 定位，正文换成了实时状态文案。
	if _, ok := tt.Find("后台任务 · 1 个运行中"); !ok {
		t.Fatalf("仍在跑的命令应计入输入区后台任务胶囊，当前文本 %v", tt.Texts())
	}
	// 浮层默认关闭：其表头计数「1 个运行中」与旧卡片都不应出现在可见文本里。
	if hasExactText(tt, "1 个运行中") {
		t.Fatalf("时间线不应再出现后台任务卡片或浮层: %v", tt.Texts())
	}
}
