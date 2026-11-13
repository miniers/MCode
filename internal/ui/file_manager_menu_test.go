package mcodeui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

// fileManagerTestShowList 读取面板当前是否显示目录列表。
func fileManagerTestShowList(s *chatState) bool {
	st := getFileManagerState(s)
	if st == nil {
		return false
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	return st.showList
}

// 右键文件行弹出菜单，菜单项按当前能力拼装，「复制相对路径」写入剪贴板。
func TestFileManagerEntryMenuCopyRelativePath(t *testing.T) {
	_, _, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	if err := tt.RightClick("a.txt"); err != nil {
		t.Fatalf("right click file row failed: %v", err)
	}
	want := fmt.Sprint([]string{
		"打开", "在右侧分栏打开", "文件历史", "逐行追溯", "引用到输入框", "复制路径", "复制相对路径",
		"-", "重命名…", "复制一份", trashActionLabel() + "…",
	})
	if got := fmt.Sprint(tt.Menu()); got != want {
		t.Fatalf("文件菜单应等于 %s，得到 %s", want, got)
	}

	if err := tt.ChooseMenuItem("复制相对路径"); err != nil {
		t.Fatalf("choose copy relative path failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.Clipboard() == "a.txt" })
}

// 头部「新建」按钮打开菜单，新建文件成功后关闭弹窗、刷新列表并打开新文件的 tab。
func TestFileManagerNewFileDialogCreatesAndOpens(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	if err := tt.Click("新建文件或文件夹"); err != nil {
		t.Fatalf("click new button failed: %v", err)
	}
	if err := tt.ChooseMenuItem("新建文件…"); err != nil {
		t.Fatalf("choose new file failed: %v", err)
	}
	waitFor(t, tt, func() bool { return fileManagerOpsOpen(s) && tt.HasText("新建文件") })

	tt.Command("selectAll")
	tt.Type("new.txt")
	tt.Key(0, ui.KeyEnter)

	waitFor(t, tt, func() bool {
		calls := fake.FileOpCalls()
		return len(calls) == 1 && calls[0].Op == "create" && calls[0].Path == "new.txt" && !calls[0].Dir
	})
	waitFor(t, tt, func() bool { return !fileManagerOpsOpen(s) })
	waitFor(t, tt, func() bool { return fmt.Sprint(fileManagerTabPaths(s)) == "[new.txt]" })
}

// 名字里带斜杠时弹窗就地报错，不发请求。
func TestFileManagerNewFileRejectsSlash(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	if err := tt.Click("新建文件或文件夹"); err != nil {
		t.Fatalf("click new button failed: %v", err)
	}
	if err := tt.ChooseMenuItem("新建文件…"); err != nil {
		t.Fatalf("choose new file failed: %v", err)
	}
	waitFor(t, tt, func() bool { return fileManagerOpsOpen(s) && tt.HasText("文件名") })

	tt.Command("selectAll")
	tt.Type("bad/name")
	tt.Key(0, ui.KeyEnter)

	waitFor(t, tt, func() bool { return tt.HasText("名字不能包含斜杠") })
	if calls := fake.FileOpCalls(); len(calls) != 0 {
		t.Fatalf("校验失败时不应发请求，得到 %v", calls)
	}
	if !fileManagerOpsOpen(s) {
		t.Fatal("校验失败后弹窗应保持打开")
	}
}

// 重命名已打开的 tab 指向的文件后，tab 的路径跟着换到新路径。
// 搜索结果行的可见文字是完整相对路径，与 tab 标签（只有文件名）不冲突，
// 所以用搜索结果行做右键目标。
func TestFileManagerRenameUpdatesTabPath(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	fake.SetFiles([]FileMatch{{Name: "b.go", Path: "sub/b.go"}})
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	if err := tt.Click("搜索文件"); err != nil {
		t.Fatalf("click search input failed: %v", err)
	}
	tt.Type("b.go")
	waitFor(t, tt, func() bool { return tt.HasText("sub/b.go") })

	if err := tt.Click("sub/b.go"); err != nil {
		t.Fatalf("click search result failed: %v", err)
	}
	waitFor(t, tt, func() bool { return fmt.Sprint(fileManagerTabPaths(s)) == "[sub/b.go]" })

	if err := tt.Click("返回文件树"); err != nil {
		t.Fatalf("click back to list failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("sub/b.go") })

	if err := tt.RightClick("sub/b.go"); err != nil {
		t.Fatalf("right click result row failed: %v", err)
	}
	if err := tt.ChooseMenuItem("重命名…"); err != nil {
		t.Fatalf("choose rename failed: %v", err)
	}
	waitFor(t, tt, func() bool { return fileManagerOpsOpen(s) && tt.HasText("重命名") })

	tt.Command("selectAll")
	tt.Type("c.go")
	tt.Key(0, ui.KeyEnter)

	waitFor(t, tt, func() bool {
		calls := fake.FileOpCalls()
		return len(calls) == 1 && calls[0].Op == "rename" && calls[0].Path == "sub/b.go" && calls[0].NewPath == "sub/c.go"
	})
	waitFor(t, tt, func() bool { return fmt.Sprint(fileManagerTabPaths(s)) == "[sub/c.go]" })
}

// 移到废纸篓：失败时弹窗显示错误且不关闭，成功后关掉对应 tab 并保持面板打开。
func TestFileManagerTrashClosesTabAndKeepsDialogOnError(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	fake.SetFiles([]FileMatch{{Name: "b.go", Path: "sub/b.go"}})
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	if err := tt.Click("搜索文件"); err != nil {
		t.Fatalf("click search input failed: %v", err)
	}
	tt.Type("b.go")
	waitFor(t, tt, func() bool { return tt.HasText("sub/b.go") })
	if err := tt.Click("sub/b.go"); err != nil {
		t.Fatalf("click search result failed: %v", err)
	}
	waitFor(t, tt, func() bool { return fmt.Sprint(fileManagerTabPaths(s)) == "[sub/b.go]" })
	if err := tt.Click("返回文件树"); err != nil {
		t.Fatalf("click back to list failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("sub/b.go") })

	if err := tt.RightClick("sub/b.go"); err != nil {
		t.Fatalf("right click result row failed: %v", err)
	}
	if err := tt.ChooseMenuItem(trashActionLabel() + "…"); err != nil {
		t.Fatalf("choose trash failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("确定把 sub/b.go " + trashActionLabel() + "吗？") })

	fake.SetFileOpsError(errors.New("删除失败"))
	if err := tt.Click("确认" + trashActionLabel()); err != nil {
		t.Fatalf("click confirm trash failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("删除失败") })
	if !fileManagerOpsOpen(s) {
		t.Fatal("删除失败后弹窗应保持打开")
	}
	if got := fmt.Sprint(fileManagerTabPaths(s)); got != "[sub/b.go]" {
		t.Fatalf("删除失败后 tab 不应关闭，得到 %s", got)
	}

	fake.SetFileOpsError(nil)
	if err := tt.Click("确认" + trashActionLabel()); err != nil {
		t.Fatalf("click confirm trash again failed: %v", err)
	}
	waitFor(t, tt, func() bool { return !fileManagerOpsOpen(s) })
	waitFor(t, tt, func() bool {
		calls := fake.FileOpCalls()
		return len(calls) == 2 && calls[1].Op == "trash" && calls[1].Path == "sub/b.go"
	})
	waitFor(t, tt, func() bool { return len(fileManagerTabPaths(s)) == 0 })
	if !isFileManagerOpen(s) {
		t.Fatal("移到废纸篓后面板应保持打开")
	}
}

// tab 右键菜单：关闭其他只留目标 tab，全部关闭清空 tab 并回到目录列表。
func TestFileManagerTabMenuCloseOthersAndAll(t *testing.T) {
	_, s, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	// 「打开」是固定打开（非临时 tab），后面点开的 b.go 才会另开一个 tab。
	if err := tt.RightClick("a.txt"); err != nil {
		t.Fatalf("right click a.txt failed: %v", err)
	}
	if err := tt.ChooseMenuItem("打开"); err != nil {
		t.Fatalf("choose open failed: %v", err)
	}
	waitFor(t, tt, func() bool { return fmt.Sprint(fileManagerTabPaths(s)) == "[a.txt]" })

	if err := tt.Click("返回文件树"); err != nil {
		t.Fatalf("click back to list failed: %v", err)
	}
	if err := tt.Click("sub"); err != nil {
		t.Fatalf("click sub dir failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("b.go") })
	if err := tt.Click("b.go"); err != nil {
		t.Fatalf("click b.go failed: %v", err)
	}
	waitFor(t, tt, func() bool { return fmt.Sprint(fileManagerTabPaths(s)) == "[a.txt sub/b.go]" })

	if err := tt.RightClick("a.txt"); err != nil {
		t.Fatalf("right click tab failed: %v", err)
	}
	want := fmt.Sprint([]string{
		"关闭", "关闭其他", "关闭右侧", "全部关闭",
		"-", "复制路径", "复制相对路径",
	})
	if got := fmt.Sprint(tt.Menu()); got != want {
		t.Fatalf("tab 菜单应等于 %s，得到 %s", want, got)
	}
	if err := tt.ChooseMenuItem("关闭其他"); err != nil {
		t.Fatalf("choose close others failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		paths := fileManagerTabPaths(s)
		_, _, active := fileManagerSnapshot(s)
		return fmt.Sprint(paths) == "[a.txt]" && active == "a.txt"
	})

	if err := tt.RightClick("a.txt"); err != nil {
		t.Fatalf("right click tab again failed: %v", err)
	}
	if err := tt.ChooseMenuItem("全部关闭"); err != nil {
		t.Fatalf("choose close all failed: %v", err)
	}
	waitFor(t, tt, func() bool { return len(fileManagerTabPaths(s)) == 0 && fileManagerTestShowList(s) })
	if !isFileManagerOpen(s) {
		t.Fatal("全部关闭后面板应保持打开")
	}
}

// 受保护条目只给只读操作，不出现写操作菜单项。
func TestFileManagerProtectedEntryHidesWriteItems(t *testing.T) {
	_, _, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("secret.env") })

	if err := tt.RightClick("secret.env"); err != nil {
		t.Fatalf("right click protected row failed: %v", err)
	}
	want := fmt.Sprint([]string{
		"打开", "在右侧分栏打开", "文件历史", "逐行追溯", "引用到输入框", "复制路径", "复制相对路径",
	})
	if got := fmt.Sprint(tt.Menu()); got != want {
		t.Fatalf("受保护条目的菜单应等于 %s，得到 %s", want, got)
	}
	tt.CloseMenu()
}

// 本机存在的文件在各平台都提供「在访达/资源管理器中显示」与「用默认应用打开」（走 mygo.Shell）。
func TestFileManagerRevealLocalFile(t *testing.T) {
	workDir := t.TempDir()
	localPath := filepath.Join(workDir, "a.txt")
	if err := os.WriteFile(localPath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write temp file failed: %v", err)
	}

	fake, _, tt := openFileManagerForTest(t)
	fake.SetThreadWorkDir(workDir)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	if err := tt.RightClick("a.txt"); err != nil {
		t.Fatalf("right click file row failed: %v", err)
	}
	got := tt.Menu()
	for _, want := range []string{revealInFileManagerLabel(), "用默认应用打开"} {
		if !slices.Contains(got, want) {
			t.Fatalf("本机文件菜单应包含「%s」，得到 %v", want, got)
		}
	}
	tt.CloseMenu()
}
