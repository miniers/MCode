package app

import (
	"context"
	"sort"

	"mcode/internal/harness"
	"mcode/internal/store"
	mcodeui "mcode/internal/ui"
)

// stewardScoreRecentLimit 是「最近」口径的条数，与设置页文案「最近 50 条」一致。
const stewardScoreRecentLimit = 50

// 编译期确认 *Client 提供设置页读取建议计分所需的能力。
var _ interface {
	StewardSuggestionStats(ctx context.Context) ([]mcodeui.SuggestionCategoryScore, error)
} = (*Client)(nil)

// StewardSuggestionStats 读「全部」与「最近 50 条」两个窗口的计分记录，按请求类别聚合，
// 返回值已按 command → fileChange → tool → permissions 排序。
// 只读本地存储：存储不可用或读取失败时按没有数据返回（空切片 + nil），
// 不向设置页报错，也不显示假的 0%。
func (c *Client) StewardSuggestionStats(ctx context.Context) ([]mcodeui.SuggestionCategoryScore, error) {
	out := []mcodeui.SuggestionCategoryScore{}
	if c == nil || c.backend == nil || c.backend.Store() == nil {
		return out, nil
	}
	st := c.backend.Store()
	all, err := st.ListStewardApprovalScores(ctx, 0)
	if err != nil {
		return out, nil
	}
	recent, err := st.ListStewardApprovalScores(ctx, stewardScoreRecentLimit)
	if err != nil {
		return out, nil
	}
	return aggregateStewardScores(all, recent), nil
}

// stewardScoreKindOrder 是建议计分的展示顺序：固定顺序，不随数据出现顺序变化。
var stewardScoreKindOrder = []string{
	string(harness.RequestKindCommand),
	string(harness.RequestKindFileChange),
	string(harness.RequestKindTool),
	string(harness.RequestKindPermissions),
}

// aggregateStewardScores 把两个窗口的记录按请求类别计数，返回固定类别顺序的结果。
// 一致率的分母只算 agree + disagree；pending 与 unanswered 都不进分母，
// 其中 unanswered 单独计数，供界面尾句提示。两个窗口都没有可计分记录的类别不返回。
func aggregateStewardScores(all, recent []store.StewardApprovalScore) []mcodeui.SuggestionCategoryScore {
	type kindCounts struct {
		all    mcodeui.SuggestionScoreWindow
		recent mcodeui.SuggestionScoreWindow
	}
	byKind := make(map[string]*kindCounts, len(stewardScoreKindOrder))
	add := func(rows []store.StewardApprovalScore, pick func(*kindCounts) *mcodeui.SuggestionScoreWindow) {
		for _, row := range rows {
			bucket := byKind[row.Kind]
			if bucket == nil {
				bucket = &kindCounts{}
				byKind[row.Kind] = bucket
			}
			window := pick(bucket)
			switch row.Outcome {
			case store.StewardScoreAgree:
				window.Agree++
				window.Answered++
			case store.StewardScoreDisagree:
				window.Answered++
			case store.StewardScoreUnanswered:
				window.Unanswered++
			}
		}
	}
	add(all, func(cc *kindCounts) *mcodeui.SuggestionScoreWindow { return &cc.all })
	add(recent, func(cc *kindCounts) *mcodeui.SuggestionScoreWindow { return &cc.recent })

	kinds := make([]string, 0, len(byKind))
	for kind, bucket := range byKind {
		// 只有未答复记录的类别（answered 与 unanswered 都为 0）没有可展示的一致率，跳过。
		if bucket.all.Answered+bucket.all.Unanswered == 0 && bucket.recent.Answered+bucket.recent.Unanswered == 0 {
			continue
		}
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(i, j int) bool {
		ri, rj := stewardScoreKindRank(kinds[i]), stewardScoreKindRank(kinds[j])
		if ri != rj {
			return ri < rj
		}
		return kinds[i] < kinds[j]
	})

	out := make([]mcodeui.SuggestionCategoryScore, 0, len(kinds))
	for _, kind := range kinds {
		bucket := byKind[kind]
		out = append(out, mcodeui.SuggestionCategoryScore{
			Kind:   kind,
			All:    bucket.all,
			Recent: bucket.recent,
		})
	}
	return out
}

// stewardScoreKindRank 给已知类别按固定顺序排名，未知类别排在最后。
func stewardScoreKindRank(kind string) int {
	for i, known := range stewardScoreKindOrder {
		if kind == known {
			return i
		}
	}
	return len(stewardScoreKindOrder)
}
