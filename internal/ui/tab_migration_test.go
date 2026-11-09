package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestFileManagerTabClickAndClose(t *testing.T) {
	s, _ := buildRoot(newGitFakeClient())
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	st.tabs = []*fileManagerTab{{path: "a.txt"}, {path: "b.txt"}}
	st.active = "a.txt"
	fileManagerMu.Unlock()
	t.Cleanup(func() {
		fileManagerMu.Lock()
		delete(fileManagerStates, s)
		fileManagerMu.Unlock()
	})
	tester := ui.NewTester(func(c *ui.Context) {
		fileManagerMu.Lock()
		snap := fileManagerSnapshotLocked(st)
		fileManagerMu.Unlock()
		ui.Row(c).Children(func() {
			for _, tab := range snap.tabs {
				renderFileManagerTabItem(c, s, snap, tab)
			}
		})
	}, 400, 100)
	if err := tester.Click("b.txt"); err != nil {
		t.Fatal(err)
	}
	_, active, _, _, _, _ := fileManagerTabsForTest(s)
	if active != "b.txt" {
		t.Fatalf("点击后选中 %q", active)
	}
	other, ok := tester.Find("a.txt")
	if !ok {
		t.Fatal("找不到另一个标签")
	}
	tester.Move(other.X+other.W/2, other.Y+other.H/2)
	tester.Frame()
	if err := tester.Click("关闭 a.txt"); err != nil {
		t.Fatal(err)
	}
	paths, active, _, _, _, _ := fileManagerTabsForTest(s)
	if active != "b.txt" || len(paths) != 1 || paths[0] != "b.txt" {
		t.Fatalf("关闭非选中标签后路径=%v，选中=%q", paths, active)
	}
}

func TestFileManagerSameNameTabsKeepSeparateState(t *testing.T) {
	s, _ := buildRoot(newGitFakeClient())
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	st.tabs = []*fileManagerTab{{path: "one/file.txt"}, {path: "two/file.txt", temporary: true}}
	st.active = "one/file.txt"
	fileManagerMu.Unlock()
	t.Cleanup(func() {
		fileManagerMu.Lock()
		delete(fileManagerStates, s)
		fileManagerMu.Unlock()
	})
	tester := ui.NewTester(func(c *ui.Context) {
		fileManagerMu.Lock()
		snap := fileManagerSnapshotLocked(st)
		fileManagerMu.Unlock()
		ui.Row(c).Children(func() {
			for _, tab := range snap.tabs {
				ui.Row(c).Label(tab.path).Children(func() {
					renderFileManagerTabItem(c, s, snap, tab)
				})
			}
		})
	}, 400, 100)
	second, ok := tester.Find("two/file.txt")
	if !ok {
		t.Fatal("找不到第二个标签")
	}
	x, y := second.X+Space2, second.Y+second.H/2
	tester.ClickAt(x, y)
	tester.Frame()
	_, active, _, _, _, _ := fileManagerTabsForTest(s)
	if active != "two/file.txt" {
		t.Fatalf("选中的同名文件 = %q", active)
	}
	tester.ClickAt(x, y)
	tester.Frame()
	tester.ClickAt(x, y)
	tester.Frame()
	if temporary, ok := fileManagerTabTemporaryForTest(s, "two/file.txt"); !ok || temporary {
		t.Fatalf("双击后临时状态 = %v，存在 = %v", temporary, ok)
	}
	if temporary, ok := fileManagerTabTemporaryForTest(s, "one/file.txt"); !ok || temporary {
		t.Fatalf("另一个标签状态 = %v，存在 = %v", temporary, ok)
	}
}

func TestFileManagerHiddenCloseKeepsTabWidth(t *testing.T) {
	s, _ := buildRoot(newGitFakeClient())
	snap := fileManagerPanelSnapshot{active: "one/file.txt"}
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Row(c).Children(func() {
			for _, path := range []string{"one/file.txt", "two/file.txt"} {
				ui.Row(c).Label(path).Children(func() {
					renderFileManagerTabItem(c, s, snap, fileManagerTabSnapshot{path: path})
				})
			}
		})
	}, 400, 100)
	active, ok := tester.Find("one/file.txt")
	if !ok {
		t.Fatal("找不到选中标签")
	}
	inactive, ok := tester.Find("two/file.txt")
	if !ok || active.W != inactive.W {
		t.Fatalf("关闭按钮隐藏时宽度变化：选中=%v，未选中=%v", active.W, inactive.W)
	}
}
