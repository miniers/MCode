package mcodeui

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// scoreFakeClient 在 FakeClient 之上补一个可计数的建议计分读取，用于缓存测试。
type scoreFakeClient struct {
	*FakeClient

	mu    sync.Mutex
	rows  []SuggestionCategoryScore
	calls int
}

func (f *scoreFakeClient) StewardSuggestionStats(context.Context) ([]SuggestionCategoryScore, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.rows, nil
}

func (f *scoreFakeClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestFormatSuggestionScoreLine 覆盖单类别文案：阈值边界、四舍五入与未答复不进分母。
func TestFormatSuggestionScoreLine(t *testing.T) {
	tests := []struct {
		name   string
		kind   string
		window SuggestionScoreWindow
		want   string
	}{
		{"样本足够给百分比", "command", SuggestionScoreWindow{Answered: 25, Agree: 23}, "命令 92%（25 条）"},
		{"四舍五入进位", "tool", SuggestionScoreWindow{Answered: 12, Agree: 2}, "工具调用 17%（12 条）"},
		{"四舍五入舍去", "tool", SuggestionScoreWindow{Answered: 12, Agree: 1}, "工具调用 8%（12 条）"},
		{"9 条样本不足", "command", SuggestionScoreWindow{Answered: 9, Agree: 9}, "命令 样本不足（9 条）"},
		{"10 条达到阈值", "command", SuggestionScoreWindow{Answered: 10, Agree: 9}, "命令 90%（10 条）"},
		{"未答复不进分母", "fileChange", SuggestionScoreWindow{Answered: 3, Agree: 1, Unanswered: 7}, "文件改动 样本不足（3 条）"},
		{"未知类别原样显示", "custom", SuggestionScoreWindow{Answered: 1}, "custom 样本不足（1 条）"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := formatSuggestionScoreLine(test.kind, test.window); got != test.want {
				t.Fatalf("文案应为 %q，得到 %q", test.want, got)
			}
		})
	}
}

// TestFormatSuggestionScoreLines 覆盖多类别拼接顺序与没有数据时的提示。
func TestFormatSuggestionScoreLines(t *testing.T) {
	scores := []SuggestionCategoryScore{
		{Kind: "command", All: SuggestionScoreWindow{Answered: 25, Agree: 23}, Recent: SuggestionScoreWindow{Answered: 10, Agree: 9, Unanswered: 1}},
		{Kind: "fileChange", All: SuggestionScoreWindow{Answered: 3, Agree: 1, Unanswered: 2}, Recent: SuggestionScoreWindow{Answered: 2, Agree: 1}},
		{Kind: "tool", All: SuggestionScoreWindow{}, Recent: SuggestionScoreWindow{}},
	}
	all := formatSuggestionScoreLines(scores, suggestionScoreWindowAll)
	wantAll := []string{
		"命令 92%（25 条）",
		"文件改动 样本不足（3 条）",
		"工具调用 样本不足（0 条）",
	}
	if len(all) != len(wantAll) {
		t.Fatalf("应有 %d 行，得到 %d（%q）", len(wantAll), len(all), all)
	}
	for i, want := range wantAll {
		if all[i] != want {
			t.Fatalf("第 %d 行应为 %q，得到 %q", i, want, all[i])
		}
	}

	recent := formatSuggestionScoreLines(scores, suggestionScoreWindowRecent)
	wantRecent := []string{
		"命令 90%（10 条）",
		"文件改动 样本不足（2 条）",
		"工具调用 样本不足（0 条）",
	}
	for i, want := range wantRecent {
		if recent[i] != want {
			t.Fatalf("最近口径第 %d 行应为 %q，得到 %q", i, want, recent[i])
		}
	}

	empty := formatSuggestionScoreLines(nil, suggestionScoreWindowAll)
	if len(empty) != 1 || empty[0] != "还没有可计分的建议" {
		t.Fatalf("空数据文案不对：%q", empty)
	}
}

// TestSuggestionScoreUnansweredNote 验证未答复尾句只在条数大于 0 时出现。
func TestSuggestionScoreUnansweredNote(t *testing.T) {
	scores := []SuggestionCategoryScore{
		{Kind: "command", All: SuggestionScoreWindow{Unanswered: 3}, Recent: SuggestionScoreWindow{Unanswered: 1}},
		{Kind: "tool", All: SuggestionScoreWindow{Unanswered: 1}},
	}
	if got := suggestionScoreUnansweredNote(scores, suggestionScoreWindowAll); got != "另有 4 条未答复，不计入一致率" {
		t.Fatalf("全部口径尾句不对：%q", got)
	}
	if got := suggestionScoreUnansweredNote(scores, suggestionScoreWindowRecent); got != "另有 1 条未答复，不计入一致率" {
		t.Fatalf("最近口径尾句不对：%q", got)
	}
	if got := suggestionScoreUnansweredNote([]SuggestionCategoryScore{{Kind: "command"}}, suggestionScoreWindowAll); got != "" {
		t.Fatalf("没有未答复时不应出现尾句，得到 %q", got)
	}
}

// TestSuggestionScoreWindowMapping 验证下拉框文案与内部取值互转，未知值按「全部」处理。
func TestSuggestionScoreWindowMapping(t *testing.T) {
	tests := []struct {
		window string
		label  string
		value  string
	}{
		{"", suggestionScoreWindowAllLabel, suggestionScoreWindowAll},
		{suggestionScoreWindowAll, suggestionScoreWindowAllLabel, suggestionScoreWindowAll},
		{suggestionScoreWindowRecent, suggestionScoreWindowRecentLabel, suggestionScoreWindowRecent},
	}
	for _, test := range tests {
		if label := suggestionScoreWindowLabel(test.window); label != test.label {
			t.Fatalf("%q 的文案应为 %q，得到 %q", test.window, test.label, label)
		}
		if value := suggestionScoreWindowValue(test.label); value != test.value {
			t.Fatalf("%q 的取值应为 %q，得到 %q", test.label, test.value, value)
		}
	}
	if got := suggestionScoreWindowValue("未知"); got != suggestionScoreWindowAll {
		t.Fatalf("未知文案应按全部处理，得到 %q", got)
	}
}

// TestRenderStewardScoreSectionShowsScores 验证分节按可见文字渲染类别行与未答复尾句。
func TestRenderStewardScoreSectionShowsScores(t *testing.T) {
	chat := newChatState(NewFakeClient())
	t.Cleanup(chat.close)
	st := &SettingsState{
		chat:               chat,
		stewardScoreWindow: suggestionScoreWindowAll,
	}
	st.stewardScore = stewardScoreCache{
		at: time.Now(),
		ok: true,
		rows: []SuggestionCategoryScore{
			{Kind: "command", All: SuggestionScoreWindow{Answered: 25, Agree: 23}},
			{Kind: "fileChange", All: SuggestionScoreWindow{Answered: 3, Agree: 1, Unanswered: 2}},
		},
	}
	tt := ui.NewTester(func(c *ui.Context) { renderStewardScoreSection(c, st) }, 700, 500)
	waitFor(t, tt, func() bool {
		return tt.HasText("建议计分") &&
			tt.HasText("命令 92%（25 条）") &&
			tt.HasText("文件改动 样本不足（3 条）") &&
			tt.HasText("另有 2 条未答复，不计入一致率")
	})
}

// TestStewardScoreCacheTTL 验证缓存未过期不重复读取，过期后先给旧值再后台刷新。
func TestStewardScoreCacheTTL(t *testing.T) {
	fake := &scoreFakeClient{
		FakeClient: NewFakeClient(),
		rows:       []SuggestionCategoryScore{{Kind: "command", All: SuggestionScoreWindow{Answered: 10, Agree: 9}}},
	}
	st := &SettingsState{chat: newChatState(fake)}
	t.Cleanup(st.chat.close)

	if _, ok := stewardScoreCached(st); ok {
		t.Fatal("首次读取时缓存尚未就绪，ok 应为 false")
	}
	waitUntil(t, "首次加载完成", func() bool {
		_, ok := stewardScoreCached(st)
		return ok
	})
	if got := fake.callCount(); got != 1 {
		t.Fatalf("过期前不应重复读取，得到 %d 次", got)
	}

	st.stewardScore.mu.Lock()
	st.stewardScore.at = time.Now().Add(-stewardScoreCacheTTL - time.Second)
	st.stewardScore.mu.Unlock()
	rows, ok := stewardScoreCached(st)
	if !ok || len(rows) != 1 {
		t.Fatalf("过期当帧应仍返回旧值，得到 %+v, %v", rows, ok)
	}
	waitUntil(t, "缓存过期后重新读取", func() bool { return fake.callCount() == 2 })
}

// TestStewardScoreCacheUnsupportedClient 验证客户端不支持该接口时缓存 ok 为 false，分节不渲染。
func TestStewardScoreCacheUnsupportedClient(t *testing.T) {
	st := &SettingsState{chat: newChatState(NewFakeClient())}
	t.Cleanup(st.chat.close)
	if _, ok := stewardScoreCached(st); ok {
		t.Fatal("客户端不支持时 ok 应为 false")
	}
	waitUntil(t, "不支持时也要结束加载状态", func() bool {
		st.stewardScore.mu.Lock()
		defer st.stewardScore.mu.Unlock()
		return !st.stewardScore.loading && !st.stewardScore.at.IsZero()
	})
	if _, ok := stewardScoreCached(st); ok {
		t.Fatal("客户端不支持时缓存不应变成就绪")
	}

	tt := ui.NewTester(func(c *ui.Context) { renderStewardScoreSection(c, st) }, 400, 300)
	tt.Frame()
	if tt.HasText("建议计分") {
		t.Fatalf("客户端不支持时不应渲染分节，当前文字：%q", tt.Texts())
	}
}
