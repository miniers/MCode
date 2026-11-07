package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestSettingsControls_AsyncFuncs 验证三种回调控件：渲染只调用 get、不调用 set；
// 忙碌时控件禁用且点击无效；可用时把新值交给 set，控件自身不写配置。
func TestSettingsControls_AsyncFuncs(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))

	asyncOn := false
	asyncBusy := false
	asyncSets := 0
	modeIdx := 1
	modeBusy := false
	modeSets := 0
	draft := "初始草稿"
	draftBusy := false
	draftSets := 0

	page := settingsPage{
		ID:    "general",
		Title: "通用",
		Sections: []settingsSection{{
			ID:    "general.async",
			Title: "异步",
			Rows: []setting{
				{ID: "row-async-switch", Title: "异步开关", Control: toggleFunc(
					func(st *SettingsState) (bool, bool) { return asyncOn, asyncBusy },
					func(_ *SettingsState, on bool) { asyncSets++; asyncOn = on },
				)},
				{ID: "row-async-mode", Title: "异步档位", Control: choiceFunc(
					[]string{"标准", "简洁", "紧凑"},
					func(st *SettingsState) (int, bool) { return modeIdx, modeBusy },
					func(_ *SettingsState, idx int) { modeSets++; modeIdx = idx },
				)},
				{ID: "row-async-text", Title: "异步文本", Control: textFunc(
					func(st *SettingsState) (string, bool) { return draft, draftBusy },
					func(_ *SettingsState, text string) { draftSets++; draft = text },
					"输入草稿",
				)},
			},
		}},
	}

	tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreePage(c, st, page) }, 1000, 800)
	tt.Frame()
	tt.Frame()

	// 渲染两帧只读回调，没写任何值。
	if asyncSets != 0 || modeSets != 0 || draftSets != 0 {
		t.Fatalf("渲染期间不应调用 set：switch=%d choice=%d text=%d", asyncSets, modeSets, draftSets)
	}
	for _, want := range []string{"异步开关", "异步档位", "异步文本", "标准", "简洁", "紧凑"} {
		if !tt.HasText(want) {
			t.Fatalf("页面缺少 %q: %v", want, tt.Texts())
		}
	}

	// 初始档位是「简洁」：点已选中的分段不算新选择。
	if err := tt.Click("简洁"); err != nil {
		t.Fatalf("点击已选中的分段失败: %v", err)
	}
	if modeSets != 0 || modeIdx != 1 {
		t.Fatalf("点已选中的分段不应调用 set：sets=%d idx=%d", modeSets, modeIdx)
	}

	// 初始文本显示在输入框里：全选复制能拿到原值。
	if err := tt.Click("编辑异步文本"); err != nil {
		t.Fatalf("聚焦文本输入失败: %v", err)
	}
	tt.Command("selectAll")
	tt.Command("copy")
	if got := tt.Clipboard(); got != "初始草稿" {
		t.Fatalf("输入框初始值 = %q，想要 %q", got, "初始草稿")
	}

	// 初始开关关闭：点一次把 true 交给 set。
	if err := tt.Click("切换异步开关"); err != nil {
		t.Fatalf("点击异步开关注失败: %v", err)
	}
	waitFor(t, tt, func() bool { return asyncSets == 1 && asyncOn })

	// 忙碌时三个控件都禁用：点击与输入都不调用 set。
	asyncBusy, modeBusy, draftBusy = true, true, true
	tt.Frame()
	busySwitch, busyMode, busyText := asyncSets, modeSets, draftSets
	_ = tt.Click("切换异步开关")
	_ = tt.Click("标准")
	_ = tt.Click("编辑异步文本")
	tt.Type("忙碌输入")
	tt.Frame()
	if asyncSets != busySwitch || modeSets != busyMode || draftSets != busyText {
		t.Fatalf("忙碌时不应调用 set：switch=%d->%d choice=%d->%d text=%d->%d",
			busySwitch, asyncSets, busyMode, modeSets, busyText, draftSets)
	}

	// 忙碌结束后恢复可写：分段换档、文本改写、开关翻回去都交付新值。
	asyncBusy, modeBusy, draftBusy = false, false, false
	tt.Frame()
	if err := tt.Click("标准"); err != nil {
		t.Fatalf("点击未选中的分段失败: %v", err)
	}
	waitFor(t, tt, func() bool { return modeSets == 1 && modeIdx == 0 })

	if err := tt.Click("编辑异步文本"); err != nil {
		t.Fatalf("重新聚焦文本输入失败: %v", err)
	}
	tt.Command("selectAll")
	tt.Type("新草稿")
	waitFor(t, tt, func() bool { return draftSets == 1 && draft == "新草稿" })

	if err := tt.Click("切换异步开关"); err != nil {
		t.Fatalf("再次点击异步开关失败: %v", err)
	}
	waitFor(t, tt, func() bool { return asyncSets == 2 && !asyncOn })
}
