package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestFileManagerEmptyAndLoadingWithEntries(t *testing.T) {
	_, s, tester := openFileManagerForTest(t)
	waitFor(t, tester, func() bool { return tester.HasText("a.txt") })
	st := getFileManagerState(s)

	fileManagerMu.Lock()
	st.entries = nil
	st.loading = false
	fileManagerMu.Unlock()
	tester.Frame()
	if !tester.HasText("空目录") {
		t.Fatal("空目录未显示空态")
	}

	fileManagerMu.Lock()
	st.entries = []FileEntry{{Name: "old.txt", Path: "old.txt"}}
	st.loading = true
	fileManagerMu.Unlock()
	tester.Frame()
	if !tester.HasText("old.txt") || tester.HasText("空目录") || tester.HasText("正在读取目录…") {
		t.Fatal("加载期间应保留旧目录内容")
	}
}

func TestTerminalTabsSwitchAndCloseInactive(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	s.projects = []Project{{ID: "p1", Path: "/project"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}}
	st := getTerminalState(s)
	terminalMu.Lock()
	st.visible = true
	st.tabs["t1"] = []*terminalTab{{id: 1, dir: "/project/one"}, {id: 2, dir: "/project/two"}, {id: 3, dir: "/project/three"}}
	st.active["t1"] = 1
	terminalMu.Unlock()
	t.Cleanup(func() { closeTerminals(s) })

	tester := ui.NewTester(func(c *ui.Context) { renderTerminalDock(c, s) }, 800, 600)
	if err := tester.Click("2 two"); err != nil {
		t.Fatal(err)
	}
	terminalMu.Lock()
	active := st.active["t1"]
	terminalMu.Unlock()
	if active != 2 {
		t.Fatalf("切换后激活标签 = %d", active)
	}
	if err := tester.Click("3 three"); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click("关闭标签"); err != nil {
		t.Fatal(err)
	}
	terminalMu.Lock()
	active = st.active["t1"]
	tabs := append([]*terminalTab(nil), st.tabs["t1"]...)
	terminalMu.Unlock()
	if active != 3 || len(tabs) != 2 || tabs[0].id != 2 || tabs[1].id != 3 {
		t.Fatalf("关闭非活动标签后激活标签 = %d，剩余标签 = %v", active, tabs)
	}
}
