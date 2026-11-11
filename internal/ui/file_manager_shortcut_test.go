package mcodeui

import "testing"

// 面板关闭时没有可关闭的预览。
func TestFileManagerShortcutPanelClosed(t *testing.T) {
	_, s, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	closeFileManager(s)
	waitFor(t, tt, func() bool { return !isFileManagerOpen(s) })

	if fileManagerHasClosableTab(s) {
		t.Fatal("面板关闭时不应报告可关闭的预览")
	}
	if fileManagerCloseActiveTab(s) {
		t.Fatal("面板关闭时不应关闭任何内容")
	}
}

// 面板打开但停在目录列表时，⌘W 没有预览可关，应落回关闭窗口。
func TestFileManagerShortcutDirectoryListNotClosable(t *testing.T) {
	_, s, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	if !isFileManagerOpen(s) {
		t.Fatal("面板应处于打开状态")
	}
	if fileManagerHasClosableTab(s) {
		t.Fatal("目录列表不应报告可关闭的预览")
	}
	if fileManagerCloseActiveTab(s) {
		t.Fatal("目录列表不应关闭任何内容")
	}
}

// 外部入口（聊天卡片、终端路径）打开的最后一个预览被 ⌘W 关掉后回到文件树，侧栏保持打开。
func TestFileManagerShortcutExternalTabReturnsToTree(t *testing.T) {
	_, s, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	openTestFileManagerFileExternal(t, s, "a.txt")
	waitForFileManagerText(t, tt, s, "a.txt", "hello")

	if !fileManagerHasClosableTab(s) {
		t.Fatal("预览文件时应报告可关闭的预览")
	}
	if !fileManagerCloseActiveTab(s) {
		t.Fatal("预览文件时应关闭预览")
	}
	if !isFileManagerOpen(s) {
		t.Fatal("关掉最后一个预览后侧栏应保持打开")
	}
	if _, _, _, _, _, showList := fileManagerTabsForTest(s); !showList {
		t.Fatal("关掉最后一个预览后应回到文件树")
	}
}

// 从侧栏预览文件时 ⌘W 只关闭预览 tab：回到目录列表，面板保持打开。
func TestFileManagerShortcutClosesPreviewOnly(t *testing.T) {
	_, s, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	if err := tt.Click("a.txt"); err != nil {
		t.Fatalf("click file row failed: %v", err)
	}
	waitForFileManagerText(t, tt, s, "a.txt", "hello")

	if !fileManagerHasClosableTab(s) {
		t.Fatal("预览文件时应报告可关闭的预览")
	}
	if !fileManagerCloseActiveTab(s) {
		t.Fatal("预览文件时应关闭预览")
	}

	dir, _, active := fileManagerSnapshot(s)
	if active != "" {
		t.Fatalf("关闭预览后应没有激活的 tab，得到 %q", active)
	}
	if paths := fileManagerTabPaths(s); len(paths) != 0 {
		t.Fatalf("关闭最后一个 tab 后不应剩下 tab，得到 %v", paths)
	}
	if dir != "" {
		t.Fatalf("关闭预览后应回到文件所在目录，得到 %q", dir)
	}
	if !isFileManagerOpen(s) {
		t.Fatal("关闭预览后文件面板应保持打开")
	}
	waitFor(t, tt, func() bool { return tt.HasText("sub") })
}
