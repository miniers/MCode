package mcodeui

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type stewardDecisionCountFakeClient struct {
	*stewardFakeClient
	count int
}

func (f *stewardDecisionCountFakeClient) StewardOpenDecisionCount(context.Context) (int, error) {
	return f.count, nil
}

type attentionDecisionCountFakeClient struct {
	*attentionFakeClient
	count int
}

func (f *attentionDecisionCountFakeClient) StewardOpenDecisionCount(context.Context) (int, error) {
	return f.count, nil
}

func TestSidebarStewardDecisionCountBadge(t *testing.T) {
	base := stewardSectionFake()
	fake := &stewardDecisionCountFakeClient{stewardFakeClient: base, count: 2}
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("未答决策 2 张") })
	if !tt.HasText("未答决策 2 张") {
		t.Fatalf("未答决策数徽标应显示「未答决策 2 张」，当前文本: %q", tt.Texts())
	}
}

func TestSidebarStewardDecisionCountBadgeHiddenWhenEmpty(t *testing.T) {
	base := stewardSectionFake()
	fake := &stewardDecisionCountFakeClient{stewardFakeClient: base, count: 0}
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("总管") })
	if tt.HasText("0") {
		t.Fatalf("未答决策数为 0 时不应显示徽标，当前文本: %q", tt.Texts())
	}
}

func TestSidebarStewardDecisionAndAttentionBadges(t *testing.T) {
	base := newAttentionFakeClient(
		attentionItem("a1", "thread_failed", "thread", "t1", "会话1出错", time.Minute),
	)
	base.stewardID = "t1"
	fake := &attentionDecisionCountFakeClient{attentionFakeClient: base, count: 2}
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("未答决策 2 张") && tt.HasText("待处理 1 条")
	})
	if !tt.HasText("未答决策 2 张") || !tt.HasText("待处理 1 条") {
		t.Fatalf("决策与待处理徽标都应显示，当前文本: %q", tt.Texts())
	}
	if err := tt.Click("待处理 1 条"); err != nil {
		t.Fatalf("点击待处理徽标失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("标记已处理") })
}
