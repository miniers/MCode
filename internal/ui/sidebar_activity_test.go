package mcodeui

import (
	"testing"
	"time"
)

// TestGroupThreadsByActivity 验证动态视图分组：标题顺序、组内倒序、跨日边界与空组省略。
func TestGroupThreadsByActivity(t *testing.T) {
	now := time.Date(2026, 10, 18, 15, 0, 0, 0, time.Local)
	local := func(day, hour, minute int) time.Time {
		return time.Date(2026, 10, day, hour, minute, 0, 0, time.Local)
	}

	threads := []Thread{
		{ID: "old", Title: "更早", UpdatedAt: local(11, 9, 0)},         // 今天 - 7 天：更早
		{ID: "week-edge", Title: "第七天", UpdatedAt: local(12, 9, 0)},  // 今天 - 6 天：过去 7 天边界
		{ID: "week", Title: "过去 7 天", UpdatedAt: local(13, 8, 0)},    // 过去 7 天
		{ID: "yesterday", Title: "昨天", UpdatedAt: local(17, 23, 59)}, // 昨天边界（今天零点前）
		{ID: "today-old", Title: "今天早", UpdatedAt: local(18, 0, 1)},  // 今天边界（零点后）
		{ID: "today-new", Title: "今天新", UpdatedAt: local(18, 14, 30)},
		{ID: "dormant", Title: "已休眠", Dormant: true, UpdatedAt: local(18, 12, 0)},
		{ID: "zero", Title: "无时间", UpdatedAt: time.Time{}},
	}

	groups := groupThreadsByActivity(threads, now)

	wantTitles := []string{"今天", "昨天", "过去 7 天", "更早"}
	if len(groups) != len(wantTitles) {
		t.Fatalf("分组数 = %d, want %d（%+v）", len(groups), len(wantTitles), groups)
	}
	for i, want := range wantTitles {
		if groups[i].Title != want {
			t.Fatalf("第 %d 组标题 = %q, want %q", i, groups[i].Title, want)
		}
	}

	wantIDs := map[string][]string{
		"今天":     {"today-new", "today-old"},
		"昨天":     {"yesterday"},
		"过去 7 天": {"week", "week-edge"},
		"更早":     {"old", "zero"},
	}
	for _, g := range groups {
		got := make([]string, 0, len(g.Threads))
		for _, th := range g.Threads {
			got = append(got, th.ID)
		}
		want := wantIDs[g.Title]
		if len(got) != len(want) {
			t.Fatalf("组 %q 的会话 = %v, want %v", g.Title, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("组 %q 的会话 = %v, want %v（组内按时间倒序）", g.Title, got, want)
			}
		}
	}
}

// TestGroupThreadsByActivityOmitsEmptyGroups 验证空组省略：只有今天有会话时只返回一组。
func TestGroupThreadsByActivityOmitsEmptyGroups(t *testing.T) {
	now := time.Date(2026, 10, 18, 15, 0, 0, 0, time.Local)
	threads := []Thread{{ID: "t1", Title: "今天", UpdatedAt: now.Add(-time.Hour)}}

	groups := groupThreadsByActivity(threads, now)
	if len(groups) != 1 || groups[0].Title != "今天" || len(groups[0].Threads) != 1 {
		t.Fatalf("空组应省略，got %+v", groups)
	}

	if got := groupThreadsByActivity(nil, now); len(got) != 0 {
		t.Fatalf("无会话时不应有分组，got %+v", got)
	}

	// 全部为休眠会话时同样没有分组（已休眠会话不进入动态视图）
	dormant := []Thread{{ID: "s1", Title: "已休眠", Dormant: true, UpdatedAt: now}}
	if got := groupThreadsByActivity(dormant, now); len(got) != 0 {
		t.Fatalf("休眠会话应被排除，got %+v", got)
	}
}

// TestGroupThreadsByActivityExcludesHelpers 验证子代理会话不进入动态视图：
// IsHelper 或 ParentThreadID 非空即排除，派生会话（ForkedFromThreadID）仍保留。
func TestGroupThreadsByActivityExcludesHelpers(t *testing.T) {
	now := time.Date(2026, 10, 18, 15, 0, 0, 0, time.Local)

	threads := []Thread{
		{ID: "plain", Title: "普通会话", UpdatedAt: now},
		{ID: "bee", Title: "子任务", IsHelper: true, ParentThreadID: "plain", UpdatedAt: now.Add(-time.Minute)},
		{ID: "helper-only", Title: "只有标记", IsHelper: true, UpdatedAt: now.Add(-2 * time.Minute)},
		{ID: "parent-only", Title: "只有父会话", ParentThreadID: "plain", UpdatedAt: now.Add(-3 * time.Minute)},
		{ID: "fork", Title: "派生会话", ForkedFromThreadID: "plain", UpdatedAt: now.Add(-4 * time.Minute)},
	}

	groups := groupThreadsByActivity(threads, now)
	if len(groups) != 1 || groups[0].Title != "今天" {
		t.Fatalf("只应有一组「今天」，got %+v", groups)
	}
	got := make([]string, 0, len(groups[0].Threads))
	for _, th := range groups[0].Threads {
		got = append(got, th.ID)
	}
	want := []string{"plain", "fork"}
	if len(got) != len(want) {
		t.Fatalf("会话 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("会话 = %v, want %v（组内按时间倒序）", got, want)
		}
	}

	// 输入全是子代理时没有分组
	onlyHelpers := []Thread{
		{ID: "h1", Title: "子任务 1", IsHelper: true, ParentThreadID: "lead", UpdatedAt: now},
		{ID: "h2", Title: "子任务 2", ParentThreadID: "lead", UpdatedAt: now},
	}
	if got := groupThreadsByActivity(onlyHelpers, now); len(got) != 0 {
		t.Fatalf("全部为子代理时不应有分组，got %+v", got)
	}
}

// TestSearchHasAnyMatch 验证「是否全部未命中」判定：项目名与会话标题、大小写与空词。
func TestSearchHasAnyMatch(t *testing.T) {
	projects := []Project{{ID: "p1", Name: "mcode"}, {ID: "p2", Name: "Aspen"}}
	threadsByProj := map[string][]Thread{
		"p1": {{ID: "t1", ProjectID: "p1", Title: "侧栏列表改造"}},
		"p2": {{ID: "t2", ProjectID: "p2", Title: "Mcode 打包"}},
	}

	tests := []struct {
		name  string
		query string
		want  bool
	}{
		{"空搜索词视为命中", "", true},
		{"仅空白视为命中", "   ", true},
		{"命中项目名", "mcode", true},
		{"命中项目名忽略大小写", "ASPEN", true},
		{"命中会话标题", "侧栏", true},
		{"命中会话标题忽略大小写", "打包", true},
		{"全部未命中", "不存在的词", false},
	}
	for _, tc := range tests {
		if got := searchHasAnyMatch(projects, threadsByProj, tc.query); got != tc.want {
			t.Errorf("%s: searchHasAnyMatch(%q) = %v, want %v", tc.name, tc.query, got, tc.want)
		}
	}

	// 没有任何项目与会话时，非空搜索词必然全部未命中
	if searchHasAnyMatch(nil, nil, "任意") {
		t.Fatal("空数据下非空搜索词应为未命中")
	}
}

// TestSidebarActivityToggle 验证动态视图开关按窗口独立保存，翻转后状态随之变化。
func TestSidebarActivityToggle(t *testing.T) {
	s := newChatState(NewFakeClient())
	if s.activityViewEnabled() {
		t.Fatal("默认应为关闭")
	}

	s.toggleActivityView()
	if !s.activityViewEnabled() {
		t.Fatal("翻转后应为开启")
	}

	s.toggleActivityView()
	if s.activityViewEnabled() {
		t.Fatal("再次翻转后应为关闭")
	}

	// 不同窗口互不影响
	other := newChatState(NewFakeClient())
	s.toggleActivityView()
	if other.activityViewEnabled() {
		t.Fatal("另一个窗口不应受本窗口开关影响")
	}

	// nil 接收者安全
	var nilState *chatState
	nilState.toggleActivityView()
	if nilState.activityViewEnabled() {
		t.Fatal("nil chatState 应返回关闭")
	}
}
