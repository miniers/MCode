package mcodeui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

const stewardBriefSevenSections = `# 导出功能

## 目标

把导出做成 CSV，一键下载。

## 现状

现在只能截图。

## 范围

只做 CSV，不做 Excel。

## 已定决策

复用现有下载组件。

## 验收标准

导出的文件能在 Excel 里打开。

## 约束

不许引入新依赖。

## 待定问题

导出上限多少行？
`

// 任务书卡片只铺「目标」「范围」「验收」三节，其余四节留在库里给模型看。
func TestStewardBriefUserSectionsKeepsOnlyUserSections(t *testing.T) {
	got := stewardBriefUserSections(stewardBriefSevenSections)

	for _, want := range []string{
		"## 目标",
		"把导出做成 CSV，一键下载。",
		"## 范围",
		"只做 CSV，不做 Excel。",
		"## 验收标准",
		"导出的文件能在 Excel 里打开。",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("抽取结果应包含 %q，实际:\n%s", want, got)
		}
	}
	for _, drop := range []string{"## 现状", "现在只能截图。", "## 已定决策", "## 约束", "不许引入新依赖。", "## 待定问题", "导出上限多少行？"} {
		if strings.Contains(got, drop) {
			t.Fatalf("抽取结果不该包含 %q，实际:\n%s", drop, got)
		}
	}
	// 按原顺序拼回：目标在范围前，范围在验收前。
	if !(strings.Index(got, "## 目标") < strings.Index(got, "## 范围") &&
		strings.Index(got, "## 范围") < strings.Index(got, "## 验收标准")) {
		t.Fatalf("三节顺序应与原文一致，实际:\n%s", got)
	}
}

// 标题带序号、关键词宽松（「验收标准」也算验收）时同样能切出来。
func TestStewardBriefUserSectionsLooseHeadings(t *testing.T) {
	brief := "## 1. 目标\n\n先能导出。\n\n## 2、范围\n\n只做 CSV。\n\n## 3. 验收标准\n\n能打开。\n\n## 4. 约束\n\n不加依赖。\n"
	got := stewardBriefUserSections(brief)
	if !strings.Contains(got, "先能导出。") || !strings.Contains(got, "只做 CSV。") || !strings.Contains(got, "能打开。") {
		t.Fatalf("带序号的三节都应保留，实际:\n%s", got)
	}
	if strings.Contains(got, "不加依赖。") {
		t.Fatalf("约束一节不该保留，实际:\n%s", got)
	}
}

// 没有二级标题时原样返回，避免卡片变空。
func TestStewardBriefUserSectionsWithoutHeadings(t *testing.T) {
	brief := "把导出做成 CSV。\n只做这一件事。"
	if got := stewardBriefUserSections(brief); got != brief {
		t.Fatalf("没有二级标题应原样返回，实际:\n%s", got)
	}
}

// 有二级标题但一节都没命中时也原样返回。
func TestStewardBriefUserSectionsNoWantedSection(t *testing.T) {
	brief := "## 现状\n\n现在只能截图。\n\n## 约束\n\n不加依赖。"
	if got := stewardBriefUserSections(brief); got != brief {
		t.Fatalf("一节都没命中应原样返回，实际:\n%s", got)
	}
}

// 每个阶段卡片上只有一个主按钮，映射集中在一处。
func TestStewardTopicPrimaryActionFor(t *testing.T) {
	cases := []struct {
		name  string
		topic StewardTopic
		want  string
	}{
		{"追问阶段", StewardTopic{Phase: StewardTopicDrafting, Brief: "## 目标\n\n导出。"}, "直接开工"},
		{"追问阶段没有任务书", StewardTopic{Phase: StewardTopicDrafting}, ""},
		{"计划待审", StewardTopic{Phase: StewardTopicPlanReview, GoalID: "g-1"}, "直接开工"},
		{"计划待审没有目标", StewardTopic{Phase: StewardTopicPlanReview}, ""},
		{"规划失败", StewardTopic{Phase: StewardTopicPlanFailed}, "重新规划"},
		{"已完成待结项", StewardTopic{Phase: StewardTopicCompleted}, "确认结项"},
		{"已完成且已关闭", StewardTopic{Phase: StewardTopicCompleted, Closed: true}, ""},
		{"执行中", StewardTopic{Phase: StewardTopicRunning}, ""},
		{"暂停", StewardTopic{Phase: StewardTopicPaused}, ""},
	}
	for _, tc := range cases {
		action, ok := stewardTopicPrimaryActionFor(tc.topic)
		if tc.want == "" {
			if ok {
				t.Fatalf("%s 不该有主按钮，实际 %q", tc.name, action.Text)
			}
			continue
		}
		if !ok {
			t.Fatalf("%s 应有主按钮 %q", tc.name, tc.want)
		}
		if action.Text != tc.want {
			t.Fatalf("%s 主按钮应为 %q，实际 %q", tc.name, tc.want, action.Text)
		}
		if action.Key == "" || action.Run == nil {
			t.Fatalf("%s 主按钮缺少键或回调", tc.name)
		}
	}
}

// 任务书还有未定问题时主按钮改成「按推荐答案开工」，点了先弹轻确认，确认前不调开工；
// 没有未定问题时仍是「直接开工」并直接开工。
func TestStewardTopicDraftingActionWithOpenQuestions(t *testing.T) {
	draftingTopic := func(questions int) StewardTopic {
		return StewardTopic{
			ThreadID:      "t-draft",
			ProjectID:     "p1",
			ProjectName:   "普通项目",
			Title:         "写导出功能",
			Brief:         "## 目标\n\n把导出做成 CSV。",
			OpenQuestions: questions,
			Phase:         StewardTopicDrafting,
			UpdatedAt:     time.Now(),
		}
	}

	// 纯映射断言：文案与回调都归 stewardTopicPrimaryActionFor 管。
	withQuestions, ok := stewardTopicPrimaryActionFor(draftingTopic(3))
	if !ok || withQuestions.Text != "按推荐答案开工" {
		t.Fatalf("有 3 个未定问题时主按钮应为「按推荐答案开工」，实际 %q (ok=%v)", withQuestions.Text, ok)
	}
	withoutQuestions, ok := stewardTopicPrimaryActionFor(draftingTopic(0))
	if !ok || withoutQuestions.Text != "直接开工" {
		t.Fatalf("没有未定问题时主按钮应为「直接开工」，实际 %q (ok=%v)", withoutQuestions.Text, ok)
	}

	t.Run("有未定问题先确认再开工", func(t *testing.T) {
		fake := newStewardTopicsFixture(draftingTopic(3))
		state, view := buildRoot(fake)
		t.Cleanup(state.close)
		tt := ui.NewTester(view, 1000, 900)

		waitFor(t, tt, func() bool { return tt.HasText("按推荐答案开工") })
		if tt.HasText("直接开工") {
			t.Fatalf("有未定问题时不该出现「直接开工」，当前文本: %q", tt.Texts())
		}
		if err := tt.Click("按推荐答案开工"); err != nil {
			t.Fatalf("点击按推荐答案开工失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			st := getStewardTopicsState(state)
			st.mu.Lock()
			open := st.startConfirmOpen
			st.mu.Unlock()
			return open && tt.HasText("按推荐答案开工？")
		})
		if !tt.HasText("还有 3 个问题没定，总管会先按推荐答案做，之后可以随时改。") {
			t.Fatalf("确认弹窗应说明未定问题数，当前文本: %q", tt.Texts())
		}
		if calls := fake.confirmedThreads(); len(calls) != 0 {
			t.Fatalf("确认前不该调用开工，实际 %v", calls)
		}
		if err := tt.Click("开工"); err != nil {
			t.Fatalf("点击确认开工失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			calls := fake.confirmedThreads()
			return len(calls) == 1 && calls[0] == "t-draft"
		})
	})

	t.Run("再想想不开工", func(t *testing.T) {
		fake := newStewardTopicsFixture(draftingTopic(1))
		state, view := buildRoot(fake)
		t.Cleanup(state.close)
		tt := ui.NewTester(view, 1000, 900)

		waitFor(t, tt, func() bool { return tt.HasText("按推荐答案开工") })
		if err := tt.Click("按推荐答案开工"); err != nil {
			t.Fatalf("点击按推荐答案开工失败: %v", err)
		}
		waitFor(t, tt, func() bool { return tt.HasText("按推荐答案开工？") })
		if err := tt.Click("再想想"); err != nil {
			t.Fatalf("点击再想想失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			st := getStewardTopicsState(state)
			st.mu.Lock()
			open := st.startConfirmOpen
			st.mu.Unlock()
			return !open
		})
		if calls := fake.confirmedThreads(); len(calls) != 0 {
			t.Fatalf("再想想不该开工，实际 %v", calls)
		}
	})

	t.Run("没有未定问题直接开工", func(t *testing.T) {
		fake := newStewardTopicsFixture(draftingTopic(0))
		state, view := buildRoot(fake)
		t.Cleanup(state.close)
		tt := ui.NewTester(view, 1000, 900)

		waitFor(t, tt, func() bool { return tt.HasText("直接开工") })
		if tt.HasText("按推荐答案开工") {
			t.Fatalf("没有未定问题时不该改文案，当前文本: %q", tt.Texts())
		}
		if err := tt.Click("直接开工"); err != nil {
			t.Fatalf("点击直接开工失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			calls := fake.confirmedThreads()
			return len(calls) == 1 && calls[0] == "t-draft"
		})
	})
}

// 「审查计划」只在计划阶段且已有目标时收进菜单。
func TestStewardTopicPlanReviewInMenu(t *testing.T) {
	cases := []struct {
		topic StewardTopic
		want  bool
	}{
		{StewardTopic{Phase: StewardTopicPlanReview, GoalID: "g-1"}, true},
		{StewardTopic{Phase: StewardTopicPlanReview}, false},
		{StewardTopic{Phase: StewardTopicDrafting, GoalID: "g-1"}, false},
		{StewardTopic{Phase: StewardTopicRunning, GoalID: "g-1"}, false},
	}
	for _, tc := range cases {
		if got := stewardTopicPlanReviewInMenu(tc.topic); got != tc.want {
			t.Fatalf("阶段 %q 目标 %q 期望 %v，实际 %v", tc.topic.Phase, tc.topic.GoalID, tc.want, got)
		}
	}
}
