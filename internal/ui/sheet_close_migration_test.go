package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

func TestSheetShortcutHelpSearch(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.toggleShortcutHelp()
	tester := ui.NewTester(func(c *ui.Context) { renderShortcutHelp(c, s) }, 1000, 700)
	waitFor(t, tester, func() bool { return tester.HasText("显示快捷键") && tester.HasText("新建会话") })
	if err := tester.Click("搜索快捷键"); err != nil {
		t.Fatal(err)
	}
	tester.Type("新建")
	waitFor(t, tester, func() bool {
		return tester.HasText("新建会话") && !tester.HasText("显示快捷键")
	})
}

func TestSheetTablePopupClose(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	openTablePopup(s, "sheet-close-test", "| 名称 |\n| --- |\n| 甲 |\n", &markdown.Options{}, nil)
	t.Cleanup(func() { closeTablePopup(s, "sheet-close-test") })
	tester := ui.NewTester(func(c *ui.Context) { renderTablePopupOverlay(c, s) }, 1000, 700)
	if !tester.HasText("表格") {
		t.Fatal("表格弹窗未显示")
	}
	if err := tester.Click("关闭"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tester, func() bool { return !tester.HasText("表格") })
}

func TestSettingsNavigationSectionHeadings(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	tester := ui.NewTester(func(c *ui.Context) { renderSettingsNav(c, st) }, 400, 1600)
	for _, heading := range []string{"应用", "代理", "工作区"} {
		if !tester.HasText(heading) {
			t.Errorf("设置导航缺少分组标题 %q", heading)
		}
	}
}
