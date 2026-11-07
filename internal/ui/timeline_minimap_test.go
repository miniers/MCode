package mcodeui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// minimapTestEntries 构造 n 条等间隔的合成条目，用于刻度规划与轨道几何测试。
func minimapTestEntries(n int) []minimapEntry {
	entries := make([]minimapEntry, 0, n)
	for i := 0; i < n; i++ {
		entries = append(entries, minimapEntry{
			ID:     fmt.Sprintf("mm-synth-%d", i),
			Index:  i,
			Title:  fmt.Sprintf("title %d", i),
			Weight: 0.5,
		})
	}
	return entries
}

// TestBuildMinimapEntries_UserMessagesOnly 验证只为用户发送的单条消息生成刻度：
// 助手回复、工具调用（含合并的工具组）都不生成。
func TestBuildMinimapEntries_UserMessagesOnly(t *testing.T) {
	messages := []Message{
		{ID: "mm-u1", Role: "user", Text: "第一条用户消息", TurnIndex: 1},
		{ID: "mm-r1", Role: "assistant", Kind: "text", Text: "回复内容", TurnIndex: 1},
		{ID: "mm-t1", Role: "tool", Text: "go build", TurnIndex: 1},
		{ID: "mm-t2", Role: "tool", Text: "go test", TurnIndex: 1},
		{ID: "mm-u2", Role: "user", Text: "第二条用户消息 a b c d e f g h i", TurnIndex: 2},
		{ID: "mm-r2", Role: "assistant", Kind: "reasoning", Text: "思考中", TurnIndex: 2},
	}
	items := buildTimelineItems(messages)
	if items[2].kind != timelineItemToolGroup {
		t.Fatalf("precondition: 工具调用应合并为工具组，got kind=%v", items[2].kind)
	}

	entries := buildMinimapEntries(items)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (user messages only), got %d", len(entries))
	}
	if entries[0].ID != "mm-u1" || entries[1].ID != "mm-u2" {
		t.Errorf("entry IDs = %q, %q; want mm-u1, mm-u2", entries[0].ID, entries[1].ID)
	}
	// Index 指向 timelineItems 中对应的用户消息项
	for _, entry := range entries {
		item := items[entry.Index]
		if item.kind != timelineItemSingle || item.msg.Role != "user" || item.msg.ID != entry.ID {
			t.Errorf("entry %q index %d 未指向用户消息项: kind=%v role=%q id=%q",
				entry.ID, entry.Index, item.kind, item.msg.Role, item.msg.ID)
		}
	}
	if entries[0].Index != 0 || entries[1].Index != 3 {
		t.Errorf("indices = %d, %d; want 0, 3", entries[0].Index, entries[1].Index)
	}
	// 标题取前 8 个词，词数超出时以省略号结尾
	if entries[1].Title != "第二条用户消息 a b c d e f g…" {
		t.Errorf("title = %q, want 前 8 个词加省略号", entries[1].Title)
	}
}

// TestBuildMinimapEntries_TitleTruncation 验证标题按空白分词截断：
// 不足 8 个词时不加省略号，换行与多余空白折叠为单个空格。
func TestBuildMinimapEntries_TitleTruncation(t *testing.T) {
	messages := []Message{
		{ID: "mm-t8", Role: "user", Text: "one two three four five six seven eight", TurnIndex: 1},
		{ID: "mm-t9", Role: "user", Text: "one two three four five six seven eight nine", TurnIndex: 2},
		{ID: "mm-tws", Role: "user", Text: "第一行\n第二行   带\t空白", TurnIndex: 3},
	}
	entries := buildMinimapEntries(buildTimelineItems(messages))
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].Title != "one two three four five six seven eight" {
		t.Errorf("8 词标题不应加省略号, got %q", entries[0].Title)
	}
	if entries[1].Title != "one two three four five six seven eight…" {
		t.Errorf("9 词标题应截到 8 个词加省略号, got %q", entries[1].Title)
	}
	if entries[2].Title != "第一行 第二行 带 空白" {
		t.Errorf("换行与多余空白应折叠, got %q", entries[2].Title)
	}
}

// TestBuildMinimapEntries_EmptyTextFallsBack 验证空文本时的标题兜底：
// 图片数量、首个附件名、最后是「消息」。
func TestBuildMinimapEntries_EmptyTextFallsBack(t *testing.T) {
	messages := []Message{
		{ID: "mm-e1", Role: "user", Text: "   ", TurnIndex: 1},
		{ID: "mm-e2", Role: "user", Text: "", TurnIndex: 2, Media: []MessageMedia{
			{Name: "screenshot.png", MimeType: "image/png"},
		}},
		{ID: "mm-e3", Role: "user", Text: "", TurnIndex: 3, Media: []MessageMedia{
			{Name: "a.png", MimeType: "image/png"},
			{Name: "b.jpg", MimeType: "image/jpeg"},
		}},
		{ID: "mm-e4", Role: "user", Text: "", TurnIndex: 4, Media: []MessageMedia{
			{Name: "doc.pdf", MimeType: "application/pdf"},
		}},
		{ID: "mm-e5", Role: "user", Text: "", TurnIndex: 5, Media: []MessageMedia{
			{Name: "  ", MimeType: "application/octet-stream"},
		}},
	}
	entries := buildMinimapEntries(buildTimelineItems(messages))
	want := []string{"消息", "1 张图片", "2 张图片", "doc.pdf", "消息"}
	if len(entries) != len(want) {
		t.Fatalf("expected %d entries, got %d", len(want), len(entries))
	}
	for i, w := range want {
		if entries[i].Title != w {
			t.Errorf("entry %d title = %q, want %q", i, entries[i].Title, w)
		}
	}
}

// TestBuildMinimapEntries_SnippetRules 验证摘要规则：折叠空白后截取 160 个字符，
// 与标题相同时置空。
func TestBuildMinimapEntries_SnippetRules(t *testing.T) {
	short := "只 有 几 个 词 的 消 息"
	long := "word0 word1 word2 word3 word4 word5 word6 word7 word8 word9 尾部内容"
	huge := strings.Repeat("长", 500)
	messages := []Message{
		{ID: "mm-s1", Role: "user", Text: short, TurnIndex: 1},
		{ID: "mm-s2", Role: "user", Text: long, TurnIndex: 2},
		{ID: "mm-s3", Role: "user", Text: huge, TurnIndex: 3},
	}
	entries := buildMinimapEntries(buildTimelineItems(messages))

	if entries[0].Snippet != "" {
		t.Errorf("短消息摘要与标题相同应置空, got %q", entries[0].Snippet)
	}
	if entries[1].Snippet == "" || entries[1].Snippet == entries[1].Title {
		t.Errorf("长消息摘要应保留且不同于标题, title=%q snippet=%q", entries[1].Title, entries[1].Snippet)
	}
	if !strings.HasPrefix(entries[1].Snippet, "word0 word1") {
		t.Errorf("摘要应为折叠空白后的原文, got %q", entries[1].Snippet)
	}
	if got := []rune(entries[2].Snippet); len(got) != minimapSnippetLimit+1 || !strings.HasSuffix(entries[2].Snippet, "…") {
		t.Errorf("超长消息摘要应按字符截断到 %d 字符并加省略号, got runes=%d", minimapSnippetLimit, len(got))
	}
	// 权重：短消息取下限，超长消息取上限
	if entries[0].Weight != minimapWeightMin || entries[2].Weight != 1 {
		t.Errorf("weights = %v, %v; want %v, 1", entries[0].Weight, entries[2].Weight, minimapWeightMin)
	}
}

// TestBuildMinimapEntries_CacheStable 验证按消息 ID 缓存后重复构建结果一致，
// 且 Index 随时间线位置重新写入而不受缓存影响。
func TestBuildMinimapEntries_CacheStable(t *testing.T) {
	solo := []Message{
		{ID: "mm-c1", Role: "user", Text: "缓存命中测试消息 a b c d e f g h i j", TurnIndex: 1},
	}
	shifted := []Message{
		{ID: "mm-c-a", Role: "assistant", Kind: "text", Text: "前置回复", TurnIndex: 1},
		{ID: "mm-c1", Role: "user", Text: "缓存命中测试消息 a b c d e f g h i j", TurnIndex: 1},
	}

	first := buildMinimapEntries(buildTimelineItems(solo))
	second := buildMinimapEntries(buildTimelineItems(solo))
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("expected 1 entry each, got %d, %d", len(first), len(second))
	}
	if first[0] != second[0] {
		t.Errorf("缓存返回应完全一致: first=%+v second=%+v", first[0], second[0])
	}

	third := buildMinimapEntries(buildTimelineItems(shifted))
	if len(third) != 1 {
		t.Fatalf("expected 1 entry after shift, got %d", len(third))
	}
	if third[0].Index != 1 {
		t.Errorf("前置条目后 Index 应重新写入为 1, got %d", third[0].Index)
	}
	if third[0].Title != first[0].Title || third[0].Snippet != first[0].Snippet || third[0].Weight != first[0].Weight {
		t.Errorf("缓存命中时文本字段应一致: %+v vs %+v", third[0], first[0])
	}
}

// TestBuildMinimapEntries_OrdinalSequential 验证 Ordinal 为完整 entries 中的 1 基序号：
// 从 1 起按条目顺序递增，缓存命中的条目也按序重写（不从缓存读）。
func TestBuildMinimapEntries_OrdinalSequential(t *testing.T) {
	messages := []Message{
		{ID: "mm-o1", Role: "user", Text: "第一条提问", TurnIndex: 1},
		{ID: "mm-o-r1", Role: "assistant", Kind: "text", Text: "回复", TurnIndex: 1},
		{ID: "mm-o2", Role: "user", Text: "第二条提问", TurnIndex: 2},
		{ID: "mm-o3", Role: "user", Text: "第三条提问", TurnIndex: 3},
	}
	entries := buildMinimapEntries(buildTimelineItems(messages))
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	for i, entry := range entries {
		if entry.Ordinal != i+1 {
			t.Errorf("entries[%d].Ordinal = %d, want %d", i, entry.Ordinal, i+1)
		}
	}
	// 第二次构建命中文本缓存，Ordinal 仍应按序重写
	again := buildMinimapEntries(buildTimelineItems(messages))
	for i, entry := range again {
		if entry.Ordinal != i+1 {
			t.Errorf("缓存命中后 entries[%d].Ordinal = %d, want %d", i, entry.Ordinal, i+1)
		}
	}
}

// TestMinimapTickLabel_HidesTitle 验证刻度标签为「跳到第 N 条提问」的序号文案，
// 不包含标题或摘要正文（正文只在悬停预览卡片里），避免刻度被
// ui.Tester 的 HasText/Texts 当作可见消息内容。
func TestMinimapTickLabel_HidesTitle(t *testing.T) {
	messages := []Message{
		{ID: "mm-l1", Role: "user", Text: "消息-79 标题之后的更多内容 a b c d e f g h i j k", TurnIndex: 1},
		{ID: "mm-l2", Role: "user", Text: "第二条提问", TurnIndex: 2},
		{ID: "mm-l3", Role: "user", Text: "第三条提问", TurnIndex: 3},
	}
	entries := buildMinimapEntries(buildTimelineItems(messages))
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if got := minimapTickLabel(entries[2]); got != "跳到第 3 条提问" {
		t.Errorf("minimapTickLabel = %q, want %q", got, "跳到第 3 条提问")
	}
	for _, entry := range entries {
		label := minimapTickLabel(entry)
		if strings.Contains(label, entry.Title) || (entry.Snippet != "" && strings.Contains(label, entry.Snippet)) {
			t.Errorf("刻度标签不应包含标题或摘要正文: label=%q entry=%+v", label, entry)
		}
	}
}

// TestMinimapActiveIndex_Boundaries 验证视口高亮的边界：最后一个 Index <= firstVisible
// 的条目，没有则取第一个；列表未布局时按顶部处理。
func TestMinimapActiveIndex_Boundaries(t *testing.T) {
	entries := []minimapEntry{
		{ID: "a", Index: 0},
		{ID: "b", Index: 5},
		{ID: "c", Index: 12},
	}
	cases := []struct {
		name               string
		firstVisible, last int
		want               int
	}{
		{"顶部视口", 0, 8, 0},
		{"视口恰在第二条", 5, 9, 1},
		{"视口在两条之间", 4, 9, 0},
		{"视口越过全部条目", 99, 120, 2},
		{"列表未布局", 0, -1, 0},
		{"lastVisible 在 firstVisible 之上", 7, 2, 0},
		{"负的 firstVisible", -3, -3, 0},
	}
	for _, tc := range cases {
		if got := minimapActiveIndex(entries, tc.firstVisible, tc.last); got != tc.want {
			t.Errorf("%s: minimapActiveIndex(%d, %d) = %d, want %d", tc.name, tc.firstVisible, tc.last, got, tc.want)
		}
	}
	if got := minimapActiveIndex(nil, 0, 0); got != -1 {
		t.Errorf("空条目应返回 -1, got %d", got)
	}
}

// TestMinimapShown_Threshold 验证出场门槛：提问不足 4 条时不显示轨道，4 条起显示。
func TestMinimapShown_Threshold(t *testing.T) {
	if minimapMinEntries != 4 {
		t.Fatalf("minimapMinEntries = %d, want 4", minimapMinEntries)
	}
	for _, count := range []int{0, 1, 2, 3} {
		if minimapShown(minimapTestEntries(count)) {
			t.Errorf("%d 条提问不应显示轨道", count)
		}
	}
	if !minimapShown(minimapTestEntries(4)) {
		t.Error("4 条提问应显示轨道")
	}
}

// TestMinimapPlanRows_Table 验证刻度规划：放得下就全部显示；放不下先把间距压到最紧；
// 仍然放不下只保留最近的一段连续条目，最上面留一格「+N」表示被隐藏的更早条目数。
func TestMinimapPlanRows_Table(t *testing.T) {
	roomy := minimapFits(minimapRailMaxHeight, minimapRailSpacingRoomy)
	tight := minimapMaxTicks()
	keep := minimapFits(minimapRailMaxHeight-minimapPlusRowHeight, minimapRailSpacingTight)
	cases := []struct {
		name        string
		count       int
		active      int
		wantRows    int
		wantHidden  int
		wantSpacing float32
		wantActive  int // 点亮的行下标，-1 表示没有行点亮
	}{
		{"放得下就全部显示", 8, 3, 8, 0, minimapRailSpacingRoomy, 3},
		{"宽松刚好放满", roomy, roomy - 1, roomy, 0, minimapRailSpacingRoomy, roomy - 1},
		{"放不下先压缩间距", roomy + 1, 5, roomy + 1, 0, minimapRailSpacingTight, 5},
		{"压缩刚好放满", tight, 6, tight, 0, minimapRailSpacingTight, 6},
		{"仍然放不下就截断加 +N", 2000, 1999, keep + 1, 2000 - keep, minimapRailSpacingTight, keep},
		{"截断后视口落在被隐藏的区间", 2000, 137, keep + 1, 2000 - keep, minimapRailSpacingTight, 0},
		{"视口未布局", 2000, -1, keep + 1, 2000 - keep, minimapRailSpacingTight, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries := minimapTestEntries(tc.count)
			plan := minimapPlanRows(entries, tc.active)
			if len(plan.Rows) != tc.wantRows {
				t.Fatalf("rows = %d, want %d", len(plan.Rows), tc.wantRows)
			}
			if plan.Hidden != tc.wantHidden {
				t.Errorf("Hidden = %d, want %d", plan.Hidden, tc.wantHidden)
			}
			if plan.Spacing != tc.wantSpacing {
				t.Errorf("Spacing = %v, want %v", plan.Spacing, tc.wantSpacing)
			}
			// 轨道总高不超预算（截断时首行是更矮的「+N」格之外的那一格更高）
			height := float32(len(plan.Rows)) * (minimapTickHeight + plan.Spacing)
			if tc.wantHidden > 0 {
				height = minimapPlusRowHeight + float32(len(plan.Rows)-1)*(minimapTickHeight+plan.Spacing)
			}
			if height > minimapRailMaxHeight {
				t.Errorf("轨道高度 %.1f 超出预算 %.1f", height, minimapRailMaxHeight)
			}
			// 最末一行的刻度总是最后一条提问
			last := plan.Rows[len(plan.Rows)-1]
			if last.Plus != 0 || last.Entry.Index != tc.count-1 {
				t.Errorf("最末一行的刻度应是最后一条提问, got index=%d plus=%d", last.Entry.Index, last.Plus)
			}
			// 「+N」格只在截断时出现，显示被隐藏的条数、跳转目标是最早一条被隐藏的条目
			ticks := plan.Rows
			if tc.wantHidden == 0 {
				for i, row := range plan.Rows {
					if row.Plus != 0 {
						t.Errorf("第 %d 行不应是「+N」", i)
					}
				}
			} else {
				head := plan.Rows[0]
				if head.Plus != tc.wantHidden {
					t.Errorf("顶格 +N = %d, want %d", head.Plus, tc.wantHidden)
				}
				if head.Entry.Index != 0 {
					t.Errorf("「+N」应跳到最早一条被隐藏的条目, got index=%d", head.Entry.Index)
				}
				ticks = plan.Rows[1:]
			}
			for i := 1; i < len(ticks); i++ {
				if ticks[i].Entry.Index <= ticks[i-1].Entry.Index {
					t.Fatalf("刻度索引应严格递增: [%d]=%d, [%d]=%d",
						i-1, ticks[i-1].Entry.Index, i, ticks[i].Entry.Index)
				}
			}
			// 点亮的行至多一行，且位置与当前条目一致
			activeCount, activeAt := 0, -1
			for i, row := range plan.Rows {
				if row.Active {
					activeCount++
					activeAt = i
				}
			}
			wantCount := 1
			if tc.wantActive < 0 {
				wantCount = 0
			}
			if activeCount != wantCount {
				t.Fatalf("点亮行数 = %d, want %d", activeCount, wantCount)
			}
			if wantCount == 1 && activeAt != tc.wantActive {
				t.Errorf("点亮行下标 = %d, want %d", activeAt, tc.wantActive)
			}
		})
	}

	// 没有条目时规划结果为空
	if plan := minimapPlanRows(nil, 0); len(plan.Rows) != 0 || plan.Hidden != 0 {
		t.Errorf("空条目应返回空规划, got rows=%d hidden=%d", len(plan.Rows), plan.Hidden)
	}
}

// TestMinimapPlusLabel_Hint 验证「+N」格的悬停提示只写更早的条数。
func TestMinimapPlusLabel_Hint(t *testing.T) {
	if got := minimapPlusLabel(12); got != "更早还有 12 条" {
		t.Errorf("minimapPlusLabel(12) = %q, want %q", got, "更早还有 12 条")
	}
	if got := minimapPlusLabel(1935); got != "更早还有 1935 条" {
		t.Errorf("minimapPlusLabel(1935) = %q", got)
	}
}

// TestMinimapOpacity_IdleFade 验证空闲变淡：悬停或刚滚过时完全不透明，
// 静止超过 2 秒后降到约 40%。
func TestMinimapOpacity_IdleFade(t *testing.T) {
	if minimapIdleDelay != 2*time.Second {
		t.Fatalf("minimapIdleDelay = %v, want 2s", minimapIdleDelay)
	}
	if minimapIdleAlpha < 0.35 || minimapIdleAlpha > 0.45 {
		t.Fatalf("minimapIdleAlpha = %v, want 约 0.4", minimapIdleAlpha)
	}
	cases := []struct {
		name      string
		hovered   bool
		sinceRoll time.Duration
		want      float32
	}{
		{"悬停时完全不透明", true, 10 * time.Second, 1},
		{"悬停时刚滚过", true, 0, 1},
		{"刚滚过", false, 0, 1},
		{"静止 1.9 秒仍不透明", false, 1900 * time.Millisecond, 1},
		{"静止 2 秒时开始变淡", false, minimapIdleDelay, minimapIdleAlpha},
		{"静止 5 秒已变淡", false, 5 * time.Second, minimapIdleAlpha},
	}
	for _, tc := range cases {
		if got := minimapOpacity(tc.hovered, tc.sinceRoll); got != tc.want {
			t.Errorf("%s: minimapOpacity(%v, %v) = %v, want %v", tc.name, tc.hovered, tc.sinceRoll, got, tc.want)
		}
	}
}

// TestMinimapTouch_TracksScroll 验证「刚滚过」的判定：可见范围或高亮条目一变就算刚滚过，
// 两者不变时返回距上次滚过的时间；没有会话 ID 时不记账。
func TestMinimapTouch_TracksScroll(t *testing.T) {
	const thread = "mm-touch-synthetic"
	base := time.Unix(1700000000, 0)
	if got := minimapTouch(thread, 0, 8, 0, base); got != 0 {
		t.Fatalf("首次活动应算刚滚过, got %v", got)
	}
	if got := minimapTouch(thread, 0, 8, 0, base.Add(3*time.Second)); got != 3*time.Second {
		t.Errorf("可见范围不变应返回距上次滚过的时间, got %v", got)
	}
	if got := minimapTouch(thread, 9, 17, 1, base.Add(4*time.Second)); got != 0 {
		t.Errorf("可见范围变化应算刚滚过, got %v", got)
	}
	if got := minimapTouch(thread, 9, 17, 2, base.Add(5*time.Second)); got != 0 {
		t.Errorf("高亮条目变化应算刚滚过, got %v", got)
	}
	if got := minimapTouch("", 0, 8, 0, base.Add(9*time.Second)); got != 0 {
		t.Errorf("无会话 ID 时应看作刚活动过, got %v", got)
	}
}

// TestMinimapRailSpacing_Compresses 验证间距压缩：条目少时宽松，接近上限时压到最紧。
func TestMinimapRailSpacing_Compresses(t *testing.T) {
	if got := minimapRailSpacing(3); got != minimapRailSpacingRoomy {
		t.Errorf("少量条目应为宽松间距 %v, got %v", minimapRailSpacingRoomy, got)
	}
	if got := minimapRailSpacing(minimapMaxTicks()); got != minimapRailSpacingTight {
		t.Errorf("上限数量应为最紧间距 %v, got %v", minimapRailSpacingTight, got)
	}
}

// TestTimelineMinimapMore_PlusCountRolls 验证「+N」的 N 接上 rollingNumber：
// 隐藏条数变化时数字滚动，滚动结束后显示新数字，未变化的那一位不位移。
func TestTimelineMinimapMore_PlusCountRolls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := sidePaneTestState(t)
	defer s.close()

	row := minimapRow{Plus: 9, Entry: minimapEntry{Index: 0}}
	tt := ui.NewTester(func(c *ui.Context) {
		renderTimelineMinimapMore(c, s, row, 1)
	}, 80, 40)
	tt.Frame()
	// 首次渲染不滚动：数字位的 Label 就是当前值。
	if _, ok := tt.Find("9"); !ok {
		t.Fatalf("首次应直接显示 9, texts=%v", tt.Texts())
	}
	if _, ok := tt.Find("更早还有 9 条"); !ok {
		t.Fatalf("悬停提示应带上隐藏条数, texts=%v", tt.Texts())
	}

	// 9 → 10：位数变多，十位从空变 1、个位从 9 变 0。
	row.Plus = 10
	tt.Frame()
	// 多给一段：帧时间是 1/60 秒的整数倍，正好 900ms 的那一帧可能落在时长之内。
	tt.Advance(2 * motionRoll)
	tt.Frame()
	if _, ok := tt.Find("10"); !ok {
		t.Fatalf("滚动结束后应显示 10, texts=%v", tt.Texts())
	}
	if _, ok := tt.Find("更早还有 10 条"); !ok {
		t.Fatalf("悬停提示应更新为 10, texts=%v", tt.Texts())
	}
	if tt.HasText("9") {
		t.Fatalf("滚动结束后不应残留旧的 9, texts=%v", tt.Texts())
	}

	// 19 → 29：只有十位变化，个位的 9 原地不动（纵向零位移）。
	row.Plus = 19
	tt.Frame()
	tt.Advance(2 * motionRoll)
	tt.Frame()
	before, ok := tt.Find("9")
	if !ok {
		t.Fatalf("19 应显示个位 9, texts=%v", tt.Texts())
	}
	row.Plus = 29
	tt.Frame()
	tt.Advance(motionRoll / 2)
	after, ok := tt.Find("9")
	if !ok {
		t.Fatalf("滚动中个位 9 仍应存在, texts=%v", tt.Texts())
	}
	if after.Y != before.Y {
		t.Fatalf("未变化的个位不应位移: %v -> %v", before.Y, after.Y)
	}
	tt.Advance(2 * motionRoll)
	tt.Frame()
	if _, ok := tt.Find("29"); !ok {
		t.Fatalf("滚动结束后应显示 29, texts=%v", tt.Texts())
	}
}
