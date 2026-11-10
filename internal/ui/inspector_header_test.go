package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestInspectorToggleRestoresLastTab(t *testing.T) {
	s := sidePaneTestState(t)
	defer s.close()
	inspectorSelect(s, sidePaneDiffID, false)
	toggleInspector(s)
	if st := inspectorSession(s); st.Open || st.Active != sidePaneDiffID {
		t.Fatalf("关闭后状态错误：%+v", st)
	}
	toggleInspector(s)
	if st := inspectorSession(s); !st.Open || st.Active != sidePaneDiffID {
		t.Fatalf("恢复后状态错误：%+v", st)
	}
}

func TestInspectorMoreMenuPreservesMemoryBadge(t *testing.T) {
	s := sidePaneTestState(t)
	defer s.close()
	memory := getMemoryPanelState(s)
	memoryPanelMu.Lock()
	memory.pendingCount, memory.pendingCountAt = 17, time.Now()
	memoryPanelMu.Unlock()
	tt := ui.NewTester(func(c *ui.Context) { renderChatHeaderAuxGroup(c, s) }, 500, 200)
	if !tt.HasText("17") {
		t.Fatal("更多入口缺少待审数量")
	}
	if err := tt.Click("更多"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(DurSheet + 50*time.Millisecond)
	if _, ok := tt.Find("记忆"); !ok {
		t.Fatal("更多菜单缺少记忆入口")
	}
	if _, ok := tt.Find("任务"); !ok {
		t.Fatal("更多菜单缺少任务入口")
	}
	if !tt.HasText("17") {
		t.Fatal("更多菜单缺少待审数量")
	}
}

func TestInspectorEmptyStatesNoDescriptions(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) { renderDiffEmptyState(c) }, 400, 300)
	if !tt.HasText("暂无改动") || tt.HasText(diffEmptySubtitle) {
		t.Fatal("改动空态应只有简短标题")
	}
}

func TestInspectorEntryRouting(t *testing.T) {
	s := sidePaneTestState(t)
	defer s.close()
	window := &mygo.Window{}
	registerChatWindow(window, s)
	defer unregisterChatWindow(window)
	menu := sidePaneMenuItems()
	palette := sidePanePaletteItems()
	for i, pane := range sidePanes {
		if menu[i].Label != "显示/隐藏"+pane.title || palette[i].title != menu[i].Label {
			t.Fatalf("入口名称不一致：%s", pane.id)
		}
		inspectorClose(s)
		menu[i].Click(nil, nil)
		menuID, _, ok := s.popPendingAction()
		if !ok || sidePaneByAction(menuID) != &sidePanes[i] {
			t.Fatalf("菜单路由错误：%s", pane.id)
		}
		sidePaneByAction(menuID).toggle(s)
		if !inspectorPaneOpen(s, pane.id) {
			t.Fatalf("菜单没有打开 %s", pane.id)
		}
		inspectorClose(s)
		palette[i].run(s)
		if !inspectorPaneOpen(s, pane.id) {
			t.Fatalf("命令面板没有打开 %s", pane.id)
		}
		inspectorClose(s)
		mods, key, ok := shortcutBindingFor(pane.actionID)
		if !ok {
			t.Fatalf("快捷键未绑定：%s", pane.actionID)
		}
		id, _, ok := matchShortcut(mods, key)
		if !ok || sidePaneByAction(id) != &sidePanes[i] {
			t.Fatalf("快捷键路由错误：%s", pane.id)
		}
		sidePaneByAction(id).toggle(s)
		if !inspectorPaneOpen(s, pane.id) {
			t.Fatalf("快捷键没有打开 %s", pane.id)
		}
	}
}

func TestInspectorHeaderShortcutOverride(t *testing.T) {
	setShortcutOverrides(nil)
	t.Cleanup(func() { setShortcutOverrides(nil) })
	s := sidePaneTestState(t)
	defer s.close()
	tt := ui.NewTester(func(c *ui.Context) { renderInspectorHeader(c, s, inspectorSession(s)) }, 500, 60)
	if node, ok := tt.Access("关闭右栏"); !ok || node.Description != "关闭" {
		t.Fatalf("默认关闭按钮说明：%+v %v", node, ok)
	}
	if got := shortcutTitleWithKey("toggle-inspector", "关闭"); got != "关闭" {
		t.Fatalf("默认提示：%q", got)
	}
	setShortcutOverrides(map[string]string{"toggle-inspector": "Cmd+Shift+Y"})
	tt.Frame()
	if node, ok := tt.Access("关闭右栏"); !ok || node.Description != "关闭（⇧⌘Y）" {
		t.Fatalf("覆盖后的关闭按钮说明：%+v %v", node, ok)
	}
	if got := shortcutTitleWithKey("toggle-inspector", "关闭"); got != "关闭（⇧⌘Y）" {
		t.Fatalf("覆盖提示：%q", got)
	}
	if id, _, ok := matchShortcut(ui.Cmd|ui.Shift, ui.KeyY); !ok || id != "toggle-inspector" {
		t.Fatalf("覆盖绑定：%q %v", id, ok)
	}
}

func TestInspectorHeaderAccessibleTabs(t *testing.T) {
	s := sidePaneTestState(t)
	defer s.close()
	inspectorSelect(s, sidePaneFileID, false)
	tt := ui.NewTester(func(c *ui.Context) { renderInspectorHeader(c, s, inspectorSession(s)) }, 500, 60)
	if err := tt.Click("文件"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyRight)
	tt.Advance(0)
	if st := inspectorSession(s); st.Active != sidePaneDiffID {
		t.Fatalf("右方向键后标签：%s", st.Active)
	}
	if node, ok := tt.AccessByRole(ui.RoleTab, "改动"); !ok || !node.Selected {
		t.Fatalf("改动标签的无障碍状态：%+v %v", node, ok)
	}
	tt.Key(0, ui.KeyRight)
	tt.Advance(0)
	if st := inspectorSession(s); st.Active != sidePanePreviewID {
		t.Fatalf("右方向键后标签：%s", st.Active)
	}
}
