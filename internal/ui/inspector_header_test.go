package mcodeui

import (
	"fmt"
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
	if err := tt.Click("更多工具"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(DurSheet + 50*time.Millisecond)
	if _, ok := tt.Find(memoryEntryLabel(s.client)); !ok {
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
	if !tt.HasText(diffEmptyTitle) || tt.HasText("每轮对话后，Agent 改动的文件会显示在这里。") {
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
		menuLabel, paletteTitle := "显示/隐藏"+pane.title, "显示/隐藏"+pane.title
		if pane.id == sidePaneGitID {
			menuLabel, paletteTitle = pane.menuLabel, pane.paletteTitle
		}
		if menu[i].Label != menuLabel || palette[i].title != paletteTitle {
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
	tt := ui.NewTester(func(c *ui.Context) { renderInspectorHeader(c, s, inspectorSession(s), false) }, 500, 60)
	if node, ok := tt.Access("关闭右栏"); !ok || node.Description != "关闭" {
		t.Fatalf("默认关闭按钮说明：%+v %v", node, ok)
	}
	if got := shortcutTitleWithKey("toggle-inspector", "关闭"); got != "关闭" {
		t.Fatalf("默认提示：%q", got)
	}
	setShortcutOverrides(map[string]string{"toggle-inspector": "Cmd+Shift+Y"})
	tt.Frame()
	if node, ok := tt.Access("关闭右栏"); !ok || node.Description != "关闭（"+shortcutDisplay("⇧⌘Y")+"）" {
		t.Fatalf("覆盖后的关闭按钮说明：%+v %v", node, ok)
	}
	if got := shortcutTitleWithKey("toggle-inspector", "关闭"); got != "关闭（"+shortcutDisplay("⇧⌘Y")+"）" {
		t.Fatalf("覆盖提示：%q", got)
	}
	if id, _, ok := matchShortcut(ui.Cmd|ui.Shift, ui.KeyY); !ok || id != "toggle-inspector" {
		t.Fatalf("覆盖绑定：%q %v", id, ok)
	}
}

func TestInspectorHeaderCloseRightInset(t *testing.T) {
	for _, topAligned := range []bool{false, true} {
		t.Run(fmt.Sprintf("topAligned=%t", topAligned), func(t *testing.T) {
			s := sidePaneTestState(t)
			defer s.close()
			const width = 500
			tt := ui.NewTester(func(c *ui.Context) { renderInspectorHeader(c, s, inspectorSession(s), topAligned) }, width, 60)
			img := tt.Image()
			y := int(diffHeaderHeight) - 1
			if topAligned {
				y = int(HeaderHeight) - 1
			}
			if img.At(width-3, y) == img.At(width-3, y-1) {
				t.Error("找不到右栏头部分隔线")
			}
			button, ok := tt.Find("关闭右栏")
			if !ok || width-(button.X+button.W) < Space3 {
				t.Fatalf("关闭按钮右侧间距不足：%+v，找到=%v", button, ok)
			}
		})
	}
}

func TestInspectorHeaderAccessibleTabs(t *testing.T) {
	s := sidePaneTestState(t)
	defer s.close()
	inspectorSelect(s, sidePaneFileID, false)
	tt := ui.NewTester(func(c *ui.Context) { renderInspectorHeader(c, s, inspectorSession(s), false) }, 500, 60)
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
	if st := inspectorSession(s); st.Active != sidePaneGitID {
		t.Fatalf("右方向键后标签：%s", st.Active)
	}
	tt.Key(0, ui.KeyRight)
	tt.Advance(0)
	if st := inspectorSession(s); st.Active != sidePanePreviewID {
		t.Fatalf("右方向键后标签：%s", st.Active)
	}
}
