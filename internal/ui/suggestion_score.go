package mcodeui

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// stewardScoreSampleThreshold 是一致率的最小样本量：少于这么多条时只报样本量，
// 不给百分比，避免三五条样本算出的比例被当成稳定结论。
const stewardScoreSampleThreshold = 10

// stewardScoreCacheTTL 是建议计分的缓存时长：计分是慢变量，60 秒足够新鲜；
// 过期后由下一次渲染发起后台刷新，本帧仍用旧值。
const stewardScoreCacheTTL = 60 * time.Second

// suggestionScoreWindow 是统计范围的取值；空串与 all 等价。
const (
	suggestionScoreWindowAll    = "all"
	suggestionScoreWindowRecent = "recent"
)

// 统计范围下拉框的显示文案。
const (
	suggestionScoreWindowAllLabel    = "全部"
	suggestionScoreWindowRecentLabel = "最近 50 条"
)

// SuggestionScoreWindow 是一个统计窗口里的计数。
// Answered 是一致率的分母（agree + disagree），Unanswered 不计入分母。
type SuggestionScoreWindow struct {
	Answered   int
	Agree      int
	Unanswered int
}

// SuggestionCategoryScore 是一类请求在两个口径下的一致率计数。
// Kind 取 harness 的请求类别：command / fileChange / tool / permissions。
type SuggestionCategoryScore struct {
	Kind   string
	All    SuggestionScoreWindow
	Recent SuggestionScoreWindow
}

// suggestionScoreClient 是建议计分的可选客户端能力：设置页用类型断言取用，
// 旧客户端与测试替身可以不实现，此时整节不渲染。
type suggestionScoreClient interface {
	// StewardSuggestionStats 返回按请求类别聚合的建议一致率；数据只读本地存储。
	StewardSuggestionStats(ctx context.Context) ([]SuggestionCategoryScore, error)
}

// suggestionKindLabel 把请求类别换成界面上的中文名；未知类别原样返回。
func suggestionKindLabel(kind string) string {
	switch kind {
	case "command":
		return "命令"
	case "fileChange":
		return "文件改动"
	case "tool":
		return "工具调用"
	case "permissions":
		return "权限"
	default:
		return kind
	}
}

// suggestionScoreWindowFor 选出要展示的窗口：未知取值按「全部」处理，与空串一致。
func suggestionScoreWindowFor(score SuggestionCategoryScore, window string) SuggestionScoreWindow {
	if window == suggestionScoreWindowRecent {
		return score.Recent
	}
	return score.All
}

// formatSuggestionScoreLine 格式化一个类别的一行文案：样本够时给四舍五入后的整数百分比，
// 不够时标「样本不足」但仍给样本量，让人知道离阈值多远。
// 括号里的 N 是 Answered（一致率分母），未答复只出现在尾句里。
func formatSuggestionScoreLine(kind string, window SuggestionScoreWindow) string {
	label := suggestionKindLabel(kind)
	if window.Answered < stewardScoreSampleThreshold {
		return fmt.Sprintf("%s 样本不足（%d 条）", label, window.Answered)
	}
	// 整数四舍五入：(agree*100 + answered/2) / answered。
	percent := (window.Agree*100 + window.Answered/2) / window.Answered
	return fmt.Sprintf("%s %d%%（%d 条）", label, percent, window.Answered)
}

// formatSuggestionScoreLines 把多个类别按传入顺序（聚合侧已固定为
// command → fileChange → tool → permissions）格式化成多行；没有数据时给一句提示。
func formatSuggestionScoreLines(scores []SuggestionCategoryScore, window string) []string {
	if len(scores) == 0 {
		return []string{"还没有可计分的建议"}
	}
	lines := make([]string, 0, len(scores))
	for _, score := range scores {
		lines = append(lines, formatSuggestionScoreLine(score.Kind, suggestionScoreWindowFor(score, window)))
	}
	return lines
}

// suggestionScoreUnansweredNote 汇总所选窗口里各类别的未答复条数：
// 大于 0 时给一句「另有 N 条未答复，不计入一致率」，否则返回空串。
func suggestionScoreUnansweredNote(scores []SuggestionCategoryScore, window string) string {
	total := 0
	for _, score := range scores {
		total += suggestionScoreWindowFor(score, window).Unanswered
	}
	if total <= 0 {
		return ""
	}
	return fmt.Sprintf("另有 %d 条未答复，不计入一致率", total)
}

// suggestionScoreWindowValue 把下拉框的显示文案换回内部取值。
func suggestionScoreWindowValue(label string) string {
	if label == suggestionScoreWindowRecentLabel {
		return suggestionScoreWindowRecent
	}
	return suggestionScoreWindowAll
}

// suggestionScoreWindowLabel 把内部取值换成下拉框的显示文案。
func suggestionScoreWindowLabel(window string) string {
	if window == suggestionScoreWindowRecent {
		return suggestionScoreWindowRecentLabel
	}
	return suggestionScoreWindowAllLabel
}

// stewardScoreCache 缓存建议计分：读取要走存储，渲染路径只读这份缓存，
// 取值放后台 goroutine，与今日提醒次数的做法一致。
// 它作为 SettingsState 的字段存在，各设置页实例与测试用例各用一份，互不串数据；
// 结构体自带锁，不得按值复制。
type stewardScoreCache struct {
	mu      sync.Mutex
	loading bool
	at      time.Time
	rows    []SuggestionCategoryScore
	ok      bool
}

// stewardScoreCached 返回缓存的建议计分；缓存缺失或过期时发起后台刷新，
// 本帧仍返回上一次结果，不阻塞渲染。客户端不支持时 ok 为 false，调用方整节不渲染。
func stewardScoreCached(st *SettingsState) ([]SuggestionCategoryScore, bool) {
	if st == nil {
		return nil, false
	}
	cache := &st.stewardScore
	now := time.Now()
	cache.mu.Lock()
	stale := cache.at.IsZero() || now.Sub(cache.at) >= stewardScoreCacheTTL
	if stale && !cache.loading {
		cache.loading = true
		go loadStewardScores(st)
	}
	rows, ok := cache.rows, cache.ok
	cache.mu.Unlock()
	return rows, ok
}

// loadStewardScores 在后台取一次建议计分，写回缓存后重绘设置页。
func loadStewardScores(st *SettingsState) {
	rows, ok := []SuggestionCategoryScore(nil), false
	st.mu.Lock()
	chat := st.chat
	st.mu.Unlock()
	if chat != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		rows, ok = chat.suggestionScores(ctx)
		cancel()
	}

	cache := &st.stewardScore
	cache.mu.Lock()
	cache.loading = false
	cache.at = time.Now()
	cache.rows = rows
	cache.ok = ok
	cache.mu.Unlock()

	st.invalidate()
}

// ---------------------------------------------------------------------------
// 今日提醒次数
// ---------------------------------------------------------------------------
//
// 与建议计分同一套做法：计数要走存储与运行时锁，渲染路径只读缓存，取值放后台 goroutine，
// 本帧仍返回上一次结果，不阻塞渲染。缓存跟着 SettingsState 走，多个设置页实例与各测试
// 用例各用一份，互不串数据；结构体自带锁，不得按值复制。

// stewardWakeTodayCacheTTL 是「今天还剩多少次主动提醒」的缓存时长：计数一天只变几次，
// 短时间重复读存储没意义；过期后由下一次渲染发起后台刷新。
const stewardWakeTodayCacheTTL = 30 * time.Second

// stewardWakeTodayState 缓存今日提醒次数。
type stewardWakeTodayState struct {
	mu      sync.Mutex
	loading bool
	at      time.Time
	used    int
	limit   int
	ok      bool
}

// stewardWakesTodayCached 返回缓存的今日提醒次数；缓存缺失或过期时发起后台刷新，
// 本帧仍返回上一次结果，不阻塞渲染。
func stewardWakesTodayCached(st *SettingsState) (used, limit int, ok bool) {
	if st == nil {
		return 0, 0, false
	}
	cache := &st.stewardWake
	now := time.Now()
	cache.mu.Lock()
	stale := cache.at.IsZero() || now.Sub(cache.at) >= stewardWakeTodayCacheTTL
	if stale && !cache.loading {
		cache.loading = true
		go loadStewardWakesToday(st)
	}
	used, limit, ok = cache.used, cache.limit, cache.ok
	cache.mu.Unlock()
	return used, limit, ok
}

// loadStewardWakesToday 在后台取一次今日提醒次数，写回缓存后重绘设置页。
func loadStewardWakesToday(st *SettingsState) {
	used, limit, ok := 0, 0, false
	st.mu.Lock()
	chat := st.chat
	st.mu.Unlock()
	if chat != nil {
		used, limit, ok = chat.stewardWakesToday()
	}

	cache := &st.stewardWake
	cache.mu.Lock()
	cache.loading = false
	cache.at = time.Now()
	cache.used = used
	cache.limit = limit
	cache.ok = ok
	cache.mu.Unlock()

	st.invalidate()
}

// suggestionScores 读一次建议计分；客户端不支持或读取失败时 ok 为 false。
func (s *chatState) suggestionScores(ctx context.Context) ([]SuggestionCategoryScore, bool) {
	if s == nil || s.client == nil {
		return nil, false
	}
	c, supported := s.client.(suggestionScoreClient)
	if !supported {
		return nil, false
	}
	rows, err := c.StewardSuggestionStats(ctx)
	if err != nil {
		return nil, false
	}
	return rows, true
}

// stewardScoreSection 是设置页总管分区里的「建议计分」分节：一致率摘要占满整行，
// 统计范围单独一行。客户端不支持或数据不可用时整节不渲染，宁可没有也不显示假 0%。
var stewardScoreSection = settingsSection{
	ID:      "sec-steward-score",
	Title:   "建议计分",
	Visible: stewardScoreVisible,
	Rows: []setting{
		{ID: "block-steward-score",
			Aliases: []string{"总管建议与你的一致率", "总管", "建议", "一致率", "计分"},
			Control: customFullWidth(renderStewardScoreSummary)},
		{ID: "row-steward-score-window",
			Title:   "统计范围",
			Aliases: []string{"总管", "建议", "一致率", "计分"},
			Control: custom(renderStewardScoreWindow)},
	},
}

// stewardScoreVisible 判断建议计分是否有数据可展示；读取缓存的同时会在后台发起刷新。
func stewardScoreVisible(st *SettingsState) bool {
	_, ok := stewardScoreCached(st)
	return ok
}

// renderStewardScoreSection 渲染「建议计分」分节，单独渲染时用（设置页由分节声明统一渲染）。
func renderStewardScoreSection(c *ui.Context, st *SettingsState) {
	if !stewardScoreVisible(st) {
		return
	}
	renderSettingsTreeSection(c, st, stewardScoreSection, true)
}

// stewardScoreWindow 返回当前统计范围，空串按「全部」处理。
func stewardScoreWindow(st *SettingsState) string {
	if st.stewardScoreWindow == "" {
		return suggestionScoreWindowAll
	}
	return st.stewardScoreWindow
}

// renderStewardScoreSummary 画一致率摘要、未答复尾句；渲染路径只读缓存，不直接查库。
func renderStewardScoreSummary(c *ui.Context, st *SettingsState) {
	scores, ok := stewardScoreCached(st)
	if !ok {
		return
	}
	window := stewardScoreWindow(st)
	ui.Column(c.Key("sec-steward-score-body")).Padding(Space4, Space5).Gap(Space1).Children(func() {
		ui.Text(c, "总管建议与你的一致率").
			FontSize(TypeProse).
			FontWeight(500).
			TextColor(ThemePrimaryText(c))
		for i, line := range formatSuggestionScoreLines(scores, window) {
			ui.Row(c.Key("row-steward-score-line-" + strconv.Itoa(i))).Children(func() {
				ui.Text(c, line).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
			})
		}
		if note := suggestionScoreUnansweredNote(scores, window); note != "" {
			ui.Text(c, note).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
		}
	})
}

// renderStewardScoreWindow 画统计范围下拉框：最近 50 条更贴近当前状态，全部看长期趋势。
func renderStewardScoreWindow(c *ui.Context, st *SettingsState) {
	current := suggestionScoreWindowLabel(stewardScoreWindow(st))
	sel := ui.Select(c.Key("sel-steward-score-window"), &current, []string{
		suggestionScoreWindowAllLabel,
		suggestionScoreWindowRecentLabel,
	}).Label("统计范围").Width(120)
	if sel.Changed() {
		st.stewardScoreWindow = suggestionScoreWindowValue(current)
		st.invalidate()
	}
}
