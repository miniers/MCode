package mcodeui

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 测试 filterPaletteItems：空查询保持原序、前缀 > 子串 > 模糊、未匹配剔除、大小写不敏感、中文匹配。
func TestFilterPaletteItems(t *testing.T) {
	items := []paletteItem{
		{id: "1", kind: "command", title: "新建会话", subtitle: ""},
		{id: "2", kind: "command", title: "快速新建项目", subtitle: "desc"},
		{id: "3", kind: "thread", title: "新功能讨论并建议", subtitle: "proj"},
		{id: "4", kind: "command", title: "显示/隐藏改动", subtitle: "diff"},
	}

	// 1. 空查询保持原序与元素内容
	emptyRes := filterPaletteItems(items, "   ")
	if len(emptyRes) != len(items) {
		t.Fatalf("expected %d items for empty query, got %d", len(items), len(emptyRes))
	}
	for i := range items {
		if emptyRes[i].id != items[i].id {
			t.Errorf("expected item %d to have id %q, got %q", i, items[i].id, emptyRes[i].id)
		}
	}

	// 2. 中文匹配与优先级：前缀(3) > 子串(2) > 模糊子序列(1)，不匹配项丢弃
	// 针对 "新建" 查询：
	// id "1" 标题为 "新建会话" -> 前缀匹配 (得分 3)
	// id "2" 标题为 "快速新建项目" -> 子串匹配 (得分 2)
	// id "3" 标题为 "新功能讨论并建议" 包含 '新'...'建' -> 模糊子序列匹配 (得分 1)
	// id "4" "显示/隐藏改动" -> 不匹配，被剔除
	// 将初始顺序打乱传入
	reordered := []paletteItem{items[3], items[2], items[1], items[0]}
	matched := filterPaletteItems(reordered, "新建")
	if len(matched) != 3 {
		t.Fatalf("expected 3 matched items, got %d", len(matched))
	}
	if matched[0].id != "1" {
		t.Errorf("expected first item to be prefix match '1', got %q", matched[0].id)
	}
	if matched[1].id != "2" {
		t.Errorf("expected second item to be substring match '2', got %q", matched[1].id)
	}
	if matched[2].id != "3" {
		t.Errorf("expected third item to be fuzzy match '3', got %q", matched[2].id)
	}

	// 3. 英文大小写不敏感测试
	enItems := []paletteItem{
		{id: "fuzzy", kind: "thread", title: "The earliest step taken", subtitle: ""},
		{id: "sub", kind: "thread", title: "Unit Test Runner", subtitle: ""},
		{id: "prefix", kind: "thread", title: "Testing environment", subtitle: ""},
		{id: "nomatch", kind: "command", title: "Other thing", subtitle: ""},
	}
	enRes := filterPaletteItems(enItems, "tEsT")
	if len(enRes) != 3 {
		t.Fatalf("expected 3 matched items for 'tEsT', got %d", len(enRes))
	}
	if enRes[0].id != "prefix" {
		t.Errorf("expected prefix match first, got %q", enRes[0].id)
	}
	if enRes[1].id != "sub" {
		t.Errorf("expected substring match second, got %q", enRes[1].id)
	}
	if enRes[2].id != "fuzzy" {
		t.Errorf("expected fuzzy match third, got %q", enRes[2].id)
	}
}

func TestPaletteKeyboardNavigation(t *testing.T) {
	fake := NewFakeClient()
	fake.threads["p1"] = append(fake.threads["p1"], Thread{ID: "t2", ProjectID: "p1", Title: "测试会话2"})
	state := newChatState(fake)
	tt := ui.NewTester(func(c *ui.Context) {
		state.init(c)
		if c.Shortcut(ui.Cmd, ui.KeyK) {
			togglePalette(state)
		}
		renderCommandPalette(c, state)
	}, 1000, 700)
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return len(state.threads) > 0
	})

	tt.Key(ui.Cmd, ui.KeyK)
	tt.Type("测试会话")
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	p := getPaletteState(state)
	if p.open {
		t.Fatal("Enter did not close palette")
	}
	state.mu.Lock()
	selected := state.selectedThreadID
	state.mu.Unlock()
	if selected != "t2" {
		t.Fatalf("Down and Enter selected %q, want t2", selected)
	}

	tt.Key(ui.Cmd, ui.KeyK)
	tt.Type("no-match-xyz")
	if !tt.HasText("没有匹配项") {
		t.Fatal("missing empty state")
	}
	tt.Key(0, ui.KeyEscape)
	if p.open {
		t.Fatal("Escape did not close palette")
	}
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Key(ui.Cmd, ui.KeyK)
	if p.open {
		t.Fatal("Cmd+K did not toggle open modal off")
	}
}

// 测试设置面板内唤起命令面板：Cmd+K 能打开面板，选中会话后离开设置并关闭面板。
// 断言用面板与设置的状态，以及只可能来自面板的条目文本；不要用“打开自动化”“返回”这类
// 侧边栏/设置页也带无障碍标签的文字，它们会让断言恒为真。
func TestPaletteAvailableInSettings(t *testing.T) {
	fake := NewFakeClient()
	fake.threads["p1"] = append(fake.threads["p1"], Thread{ID: "t2", ProjectID: "p1", Title: "测试会话2"})
	var state *chatState
	tt := ui.NewTester(Root(fake, WithSettings(settingsTestDir(t), nil), func(s *chatState) {
		state = s
	}), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	st := getChatSettings(state)
	if st == nil {
		t.Fatal("settings state missing")
	}
	st.Open()
	tt.Key(ui.Cmd, ui.KeyK)
	if p := getPaletteState(state); p == nil || !p.open {
		t.Fatalf("Cmd+K did not open palette in settings: %v", tt.Texts())
	}
	if !tt.HasText("显示/隐藏改动") || !st.IsOpen() {
		t.Fatalf("palette or settings is not on screen: %v", tt.Texts())
	}
	tt.Key(ui.Cmd, ui.KeyK)
	if p := getPaletteState(state); p.open || !st.IsOpen() {
		t.Fatal("Cmd+K should close the palette while keeping settings open")
	}
	tt.Key(ui.Cmd, ui.KeyK)
	if p := getPaletteState(state); !p.open || !st.IsOpen() {
		t.Fatal("Cmd+K should reopen the palette above settings")
	}
	tt.Type("测试会话2")
	tt.Key(0, ui.KeyEnter)
	if p := getPaletteState(state); p != nil && p.open {
		t.Fatal("selecting thread did not close palette")
	}
	if !st.IsOpen() {
		t.Fatal("selecting thread should keep settings open")
	}
	st.Close()
	state.mu.Lock()
	selected := state.selectedThreadID
	state.mu.Unlock()
	if selected != "t2" {
		t.Fatalf("selecting thread in settings selected %q, want t2", selected)
	}
	if tt.HasText("显示/隐藏改动") {
		t.Fatalf("palette stayed on screen: %v", tt.Texts())
	}
}

// 测试 move 在 [0, n-1] 区间内的边界夹取行为。
func TestPaletteMoveClamping(t *testing.T) {
	p := &paletteState{}
	n := 5

	p.move(-1, n)
	if p.selected != 0 {
		t.Errorf("expected selected clamped at 0, got %d", p.selected)
	}

	p.move(2, n)
	if p.selected != 2 {
		t.Errorf("expected selected 2, got %d", p.selected)
	}

	p.move(10, n)
	if p.selected != 4 {
		t.Errorf("expected selected clamped at 4 (n-1), got %d", p.selected)
	}

	p.move(-2, n)
	if p.selected != 2 {
		t.Errorf("expected selected 2, got %d", p.selected)
	}

	// n == 0 时重置为 0
	p.move(3, 0)
	if p.selected != 0 {
		t.Errorf("expected selected 0 when n=0, got %d", p.selected)
	}
}

// 测试 executePaletteSelection 执行会话条目选中目标会话并关闭面板。
func TestExecutePaletteSelection(t *testing.T) {
	fake := NewFakeClient()
	state := newChatState(fake)

	openPalette(state)
	p := getPaletteState(state)
	if !p.open {
		t.Fatalf("expected palette to be open")
	}

	targetThreadID := "thread-target-42"
	state.mu.Lock()
	state.threads = []Thread{
		{ID: targetThreadID, Title: "目标测试会话", ProjectID: "p1"},
	}
	state.mu.Unlock()

	items := buildPaletteItems(state)
	var threadItemIdx int = -1
	for idx, it := range items {
		if it.kind == "thread" && it.id == "thread-"+targetThreadID {
			threadItemIdx = idx
			break
		}
	}
	if threadItemIdx == -1 {
		t.Fatalf("thread item not found in palette items: %+v", items)
	}

	p.selected = threadItemIdx
	executePaletteSelection(state, items)

	if p.open {
		t.Errorf("expected palette to be closed after execution")
	}

	state.mu.Lock()
	selected := state.selectedThreadID
	state.mu.Unlock()

	if selected != targetThreadID {
		t.Errorf("expected selectedThreadID %q, got %q", targetThreadID, selected)
	}
}

// 测试消息搜索的触发门槛：去掉首尾空白后至少 2 个 rune 才发起搜索。
func TestShouldSearchMessages(t *testing.T) {
	cases := []struct {
		query string
		want  bool
	}{
		{"", false},
		{"   ", false},
		{" a ", false},
		{"a", false},
		{"ab", true},
		{" ab ", true},
		{"中", false},
		{"中文", true},
		{"a中", true},
	}
	for _, c := range cases {
		if got := shouldSearchMessages(c.query); got != c.want {
			t.Errorf("shouldSearchMessages(%q) = %v, want %v", c.query, got, c.want)
		}
	}
}

// 测试合成列表：过滤后的候选条目之后追加消息结果条目，结果对不上当前查询时不展示。
// 不依赖真实 client：直接把命中写进面板状态。
func TestPaletteVisibleItemsIncludesMessageHits(t *testing.T) {
	fake := NewFakeClient()
	fake.threads["p1"] = append(fake.threads["p1"], Thread{ID: "t-msg", ProjectID: "p1", Title: "来源会话"})
	state := newChatState(fake)
	state.mu.Lock()
	state.threads = []Thread{{ID: "t-msg", ProjectID: "p1", Title: "来源会话"}}
	state.mu.Unlock()

	p := getPaletteState(state)
	p.msgHits = []MessageSearchHit{
		{
			ThreadID:    "t-msg",
			ThreadTitle: "来源会话",
			ProjectID:   "p1",
			MessageID:   "m-hit-1",
			Role:        "assistant",
			Snippet:     "…关键片段在中间…",
		},
	}
	p.msgQuery = "关键片段"

	base := filterPaletteItems(buildPaletteItems(state), "关键片段")
	merged := paletteVisibleItems(state, p, "关键片段")
	if len(merged) != len(base)+1 {
		t.Fatalf("期望合成列表比基础列表多 1 条，实际 %d vs %d", len(merged), len(base))
	}
	last := merged[len(merged)-1]
	if last.kind != "message" {
		t.Fatalf("最后一条应为消息条目，实际 kind=%q", last.kind)
	}
	if last.id != "msg-m-hit-1" {
		t.Fatalf("消息条目 id 应为 msg-m-hit-1，实际 %q", last.id)
	}
	if last.title != "…关键片段在中间…" {
		t.Fatalf("消息条目标题应为片段，实际 %q", last.title)
	}
	if last.subtitle != "来源会话" {
		t.Fatalf("消息条目副标题应为会话标题，实际 %q", last.subtitle)
	}

	// 查询串变了：旧结果不展示。
	if got := len(paletteVisibleItems(state, p, "关键片")); got != len(filterPaletteItems(buildPaletteItems(state), "关键片")) {
		t.Fatalf("旧查询的结果不该展示，实际 %d 条", got)
	}

	// 点击消息条目：切到目标会话并登记一次消息跳转。
	if last.run == nil {
		t.Fatal("消息条目缺少 run")
	}
	messageJumpMu.Lock()
	delete(messageJumps, state)
	messageJumpMu.Unlock()
	last.run(state)
	state.mu.Lock()
	selected := state.selectedThreadID
	state.mu.Unlock()
	if selected != "t-msg" {
		t.Fatalf("点击消息条目后选中会话应为 t-msg，实际 %q", selected)
	}
	messageJumpMu.Lock()
	jump := messageJumps[state]
	delete(messageJumps, state)
	messageJumpMu.Unlock()
	if jump == nil || jump.threadID != "t-msg" || jump.messageID != "m-hit-1" {
		t.Fatalf("点击消息条目后应登记消息跳转，实际 %+v", jump)
	}
}

// 测试面板关闭会清空消息结果与在飞的请求。
func TestClosePaletteClearsMessageHits(t *testing.T) {
	state := newChatState(NewFakeClient())
	p := getPaletteState(state)
	p.open = true
	p.msgHits = []MessageSearchHit{{ThreadID: "t1", MessageID: "m1", Snippet: "片段"}}
	p.msgQuery = "片段"
	seq := p.msgSeq

	closePalette(state)

	if p.msgHits != nil || p.msgQuery != "" {
		t.Fatalf("关闭面板应清空消息结果，实际 hits=%v query=%q", p.msgHits, p.msgQuery)
	}
	if p.msgSeq <= seq {
		t.Fatalf("关闭面板应推进请求序号，实际 %d -> %d", seq, p.msgSeq)
	}
}

// paletteItemByID 返回给定 ID 的候选条目，找不到返回 nil。
func paletteItemByID(items []paletteItem, id string) *paletteItem {
	for i := range items {
		if items[i].id == id {
			return &items[i]
		}
	}
	return nil
}

// paletteItemsHaveID 判断候选条目里是否含给定 ID。
func paletteItemsHaveID(items []paletteItem, id string) bool {
	return paletteItemByID(items, id) != nil
}

// TestPaletteStewardStartTopic 有项目且客户端支持话题接口时，命令面板出现「交给…」，
// 搜「总管」「默默」「交给」「话题」都能命中，选中后按当前项目打开新建话题对话框。
func TestPaletteStewardStartTopic(t *testing.T) {
	fake := newStewardTopicsFake()
	state := newChatState(fake)
	state.mu.Lock()
	state.projects = []Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}}
	state.mu.Unlock()

	items := buildPaletteItems(state)
	item := paletteItemByID(items, "cmd-steward-start-topic")
	if item == nil {
		t.Fatalf("有项目时应出现「交给总管」条目，实际 %+v", items)
	}
	if item.title != "交给总管" {
		t.Errorf("条目标题应为「交给总管」，实际 %q", item.title)
	}
	if item.subtitle != "把一件事交给总管跟进" {
		t.Errorf("条目副标题应为「把一件事交给总管跟进」，实际 %q", item.subtitle)
	}
	if item.run == nil {
		t.Fatal("条目应带执行函数")
	}

	for _, query := range []string{"总管", "默默", "交给", "话题"} {
		if matched := filterPaletteItems(items, query); !paletteItemsHaveID(matched, "cmd-steward-start-topic") {
			t.Errorf("搜 %q 应命中「交给总管」，实际 %+v", query, matched)
		}
	}

	item.run(state)
	st := getStewardTopicsState(state)
	st.mu.Lock()
	open, projectID, projectName := st.startOpen, st.startProjectID, st.startProjectName
	st.mu.Unlock()
	if !open {
		t.Fatal("选中「交给总管」应打开新建话题对话框")
	}
	if projectID != "p1" || projectName != "mcode-test" {
		t.Fatalf("对话框项目应为 p1/mcode-test，实际 %q/%q", projectID, projectName)
	}
}

// TestPaletteStewardEntriesHiddenWithoutProject 没有项目时不出现「交给…」；
// 客户端不支持总管能力时，连「打开…」也不出现。
func TestPaletteStewardEntriesHiddenWithoutProject(t *testing.T) {
	state := newChatState(newStewardTopicsFake())
	if items := buildPaletteItems(state); paletteItemsHaveID(items, "cmd-steward-start-topic") {
		t.Fatalf("没有项目时不应出现「交给总管」，实际 %+v", items)
	}

	plain := newChatState(NewFakeClient())
	if items := buildPaletteItems(plain); paletteItemsHaveID(items, "cmd-open-steward") {
		t.Fatalf("客户端不支持总管时不应出现「打开总管」，实际 %+v", items)
	}
}

// TestPaletteOpenStewardUsesNickname 「打开…」条目跟着昵称走，昵称为空时回落到「总管」。
func TestPaletteOpenStewardUsesNickname(t *testing.T) {
	fake := newStewardTopicsFake()
	fake.name = "小明"
	state := newChatState(fake)

	item := paletteItemByID(buildPaletteItems(state), "cmd-open-steward")
	if item == nil {
		t.Fatal("客户端支持总管能力时应出现「打开…」条目")
	}
	if item.title != "打开小明" {
		t.Fatalf("条目标题应为「打开小明」，实际 %q", item.title)
	}
	if matched := filterPaletteItems(buildPaletteItems(state), "总管"); !paletteItemsHaveID(matched, "cmd-open-steward") {
		t.Fatalf("搜「总管」应命中「打开小明」，实际 %+v", matched)
	}
}

// TestPaletteForwardThreadEntry 选中会话时命令面板出现「转发到新会话」，未选中时不出现。
func TestPaletteForwardThreadEntry(t *testing.T) {
	fake := NewFakeClient()
	state := newChatState(fake)
	state.mu.Lock()
	state.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "会话一"}}
	state.selectedThreadID = ""
	state.mu.Unlock()

	if items := buildPaletteItems(state); paletteItemsHaveID(items, "cmd-forward-thread") {
		t.Fatalf("未选中会话时不应出现「转发到新会话」，实际 %+v", items)
	}

	state.mu.Lock()
	state.selectedThreadID = "t1"
	state.mu.Unlock()

	items := buildPaletteItems(state)
	item := paletteItemByID(items, "cmd-forward-thread")
	if item == nil {
		t.Fatalf("选中会话时应出现「转发到新会话」，实际 %+v", items)
	}
	if item.title != "转发到新会话" {
		t.Errorf("条目标题应为「转发到新会话」，实际 %q", item.title)
	}
	if item.subtitle != "把当前会话的上下文带入新会话" {
		t.Errorf("条目副标题应为「把当前会话的上下文带入新会话」，实际 %q", item.subtitle)
	}
	if item.run == nil {
		t.Fatal("条目应带执行函数")
	}
	for _, query := range []string{"转发", "继续", "新会话", "forward", "continue"} {
		if matched := filterPaletteItems(items, query); !paletteItemsHaveID(matched, "cmd-forward-thread") {
			t.Errorf("搜 %q 应命中「转发到新会话」，实际 %+v", query, matched)
		}
	}
}

// 测试命令面板页脚：左侧三个键位提示，右侧结果计数与可见结果数一致，过滤后同步更新。
func TestPaletteFooterShowsKeyHintsAndResultCount(t *testing.T) {
	fake := NewFakeClient()
	fake.threads["p1"] = append(fake.threads["p1"], Thread{ID: "t2", ProjectID: "p1", Title: "测试会话2"})
	state := newChatState(fake)
	tt := ui.NewTester(func(c *ui.Context) {
		state.init(c)
		if c.Shortcut(ui.Cmd, ui.KeyK) {
			togglePalette(state)
		}
		renderCommandPalette(c, state)
	}, 1000, 700)

	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyK)
	waitFor(t, tt, func() bool { return tt.HasText("esc 关闭") })

	for _, want := range []string{"↑↓ 选择", "↵ 执行", "esc 关闭"} {
		if !tt.HasText(want) {
			t.Fatalf("页脚缺少 %q，当前文本：%q", want, tt.Texts())
		}
	}
	// 「↵」必须作为独立键帽渲染出来：只在提示 Label 里出现不算，键帽本身要可见。
	if _, ok := tt.Find("↵"); !ok {
		t.Fatalf("页脚应渲染「↵」键帽，当前文本：%q", tt.Texts())
	}

	p := getPaletteState(state)
	want := fmt.Sprintf("%d 项结果", len(paletteVisibleItems(state, p, p.query)))
	if !tt.HasText(want) {
		t.Fatalf("页脚应显示 %q，当前文本：%q", want, tt.Texts())
	}

	// 输入查询后计数跟随可见结果变化。
	tt.Type("测试会话2")
	waitFor(t, tt, func() bool {
		items := paletteVisibleItems(state, p, p.query)
		return p.query == "测试会话2" && len(items) > 0 && tt.HasText(fmt.Sprintf("%d 项结果", len(items)))
	})
}
