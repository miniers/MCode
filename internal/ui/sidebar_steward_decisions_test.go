package mcodeui

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
)

type stewardDecisionCountFakeClient struct {
	*stewardFakeClient
	count int
}

func (f *stewardDecisionCountFakeClient) StewardOpenDecisionCount(context.Context) (int, error) {
	return f.count, nil
}

func TestSidebarStewardDecisionCountBadge(t *testing.T) {
	base := stewardSectionFake()
	fake := &stewardDecisionCountFakeClient{stewardFakeClient: base, count: 2}
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("待处理 2 条") })
	if !tt.HasText("2") {
		t.Fatalf("未答决策数徽标应显示 2，当前文本: %q", tt.Texts())
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
