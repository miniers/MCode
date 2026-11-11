package mcodeui

import "testing"

func TestFileManagerExternalLineRange(t *testing.T) {
	_, s, _ := openFileManagerForTest(t)
	st := getFileManagerState(s)
	check := func(first, last, seq int, reveal, preview bool) {
		t.Helper()
		fileManagerMu.Lock()
		defer fileManagerMu.Unlock()
		tab := st.findTabLocked("notes.md")
		if tab == nil || tab.hlFirst != first || tab.hlLast != last || tab.revealSeq != seq || tab.reveal != reveal || tab.mdPreview != preview {
			t.Fatalf("tab 状态不符：%+v", tab)
		}
		snap, ok := fileManagerSnapshotLocked(st).tabByPath("notes.md")
		if !ok || snap.hlFirst != first || snap.hlLast != last || snap.revealSeq != seq || snap.reveal != reveal {
			t.Fatalf("快照状态不符：%+v", snap)
		}
	}
	s.openFileManagerFileExternalAt("", "notes.md", 12, 20)
	check(12, 20, 1, true, false)
	fileManagerMu.Lock()
	st.findTabLocked("notes.md").reveal = false
	st.findTabLocked("notes.md").mdPreview = true
	fileManagerMu.Unlock()
	s.openFileManagerFileExternalAt("", "notes.md", 30, 40)
	check(30, 40, 2, true, false)
	s.openFileManagerFileExternal("", "notes.md")
	check(30, 40, 2, true, false)
	s.openFileManagerFileExternalAt("", "notes.md", 30, 40)
	check(30, 40, 3, true, false)
}

func TestFileManagerTemporaryTabClearsLineRange(t *testing.T) {
	st := &fileManagerState{}
	tab, _ := st.openFileManagerTabLocked("a.go", false)
	tab.hlFirst, tab.hlLast, tab.reveal = 12, 20, true
	replaced, fresh := st.openFileManagerTabLocked("b.go", false)
	if !fresh || replaced != tab || tab.hlFirst != 0 || tab.hlLast != 0 || tab.reveal {
		t.Fatalf("替换临时标签时应清除行号：%+v", tab)
	}
}

// fileManagerTabsForTest 取面板的 tab 与拆分状态快照，供断言使用。
func fileManagerTabsForTest(s *chatState) (paths []string, active, anchor, side string, vertical, showList bool) {
	st := getFileManagerState(s)
	if st == nil {
		return nil, "", "", "", false, false
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	paths = make([]string, 0, len(st.tabs))
	for _, tab := range st.tabs {
		paths = append(paths, tab.path)
	}
	return paths, st.active, st.splitAnchor, st.splitSide, st.splitVertical, st.showList
}

// fileManagerTabTemporaryForTest 报告某个 tab 是否仍然是临时预览。
func fileManagerTabTemporaryForTest(s *chatState, path string) (temporary bool, ok bool) {
	st := getFileManagerState(s)
	if st == nil {
		return false, false
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	tab := st.findTabLocked(path)
	if tab == nil {
		return false, false
	}
	return tab.temporary, true
}

// openTestFileManagerFile 直接调用打开入口，返回当前会话的 threadID。
func openTestFileManagerFile(t *testing.T, s *chatState, path string, pin bool) {
	t.Helper()
	threadID, _ := getSelectedThreadWorkDir(s)
	s.openFileManagerFile(threadID, path, pin)
}

// openTestFileManagerFileExternal 从外部入口（聊天卡片、终端路径）打开文件预览。
func openTestFileManagerFileExternal(t *testing.T, s *chatState, path string) {
	t.Helper()
	threadID, _ := getSelectedThreadWorkDir(s)
	s.openFileManagerFileExternal(threadID, path)
}

// 单击打开的文件落在同一个临时 tab 里：先开 a.txt 再开 b.txt 只剩 b.txt 一个 tab。
func TestFileManagerTemporaryTabReplaced(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "hello", TotalSize: 5},
		"b.txt": {Path: "b.txt", Kind: "text", Text: "world", TotalSize: 5},
	})
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	openTestFileManagerFile(t, s, "a.txt", false)
	waitForFileManagerText(t, tt, s, "a.txt", "hello")
	openTestFileManagerFile(t, s, "b.txt", false)
	waitForFileManagerText(t, tt, s, "b.txt", "world")

	paths, active, _, _, _, showList := fileManagerTabsForTest(s)
	if len(paths) != 1 || paths[0] != "b.txt" {
		t.Fatalf("临时预览应原地替换，得到 %v", paths)
	}
	if active != "b.txt" {
		t.Fatalf("替换后应激活 b.txt，得到 %q", active)
	}
	if showList {
		t.Fatal("预览时不应显示目录列表")
	}
	if temporary, ok := fileManagerTabTemporaryForTest(s, "b.txt"); !ok || !temporary {
		t.Fatalf("单击打开的 tab 应为临时预览，得到 temporary=%v ok=%v", temporary, ok)
	}
}

// 固定 tab 不会被单击打开的文件替换。
func TestFileManagerPinnedTabNotReplaced(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "hello", TotalSize: 5},
		"b.txt": {Path: "b.txt", Kind: "text", Text: "world", TotalSize: 5},
	})
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	openTestFileManagerFile(t, s, "a.txt", true)
	waitForFileManagerText(t, tt, s, "a.txt", "hello")
	openTestFileManagerFile(t, s, "b.txt", false)
	waitForFileManagerText(t, tt, s, "b.txt", "world")

	paths, active, _, _, _, _ := fileManagerTabsForTest(s)
	if len(paths) != 2 || paths[0] != "a.txt" || paths[1] != "b.txt" {
		t.Fatalf("固定 tab 之外应新增临时 tab，得到 %v", paths)
	}
	if active != "b.txt" {
		t.Fatalf("应激活新打开的 b.txt，得到 %q", active)
	}
}

// 同一个路径只保留一个 tab；临时 tab 用 pin=true 再打开一次后被固定。
func TestFileManagerTabDedupAndPin(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "hello", TotalSize: 5},
	})
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	openTestFileManagerFile(t, s, "a.txt", false)
	openTestFileManagerFile(t, s, "a.txt", false)
	waitForFileManagerText(t, tt, s, "a.txt", "hello")

	if paths, _, _, _, _, _ := fileManagerTabsForTest(s); len(paths) != 1 {
		t.Fatalf("同一个路径不应产生重复 tab，得到 %v", paths)
	}

	openTestFileManagerFile(t, s, "a.txt", true)
	if temporary, ok := fileManagerTabTemporaryForTest(s, "a.txt"); !ok || temporary {
		t.Fatalf("再次以 pin=true 打开后应固定，得到 temporary=%v ok=%v", temporary, ok)
	}
}

// 关闭当前 tab 后激活相邻 tab；关完最后一个回到目录列表，面板仍然打开。
func TestFileManagerCloseTabActivatesNeighbor(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "hello", TotalSize: 5},
		"b.txt": {Path: "b.txt", Kind: "text", Text: "world", TotalSize: 5},
		"c.txt": {Path: "c.txt", Kind: "text", Text: "again", TotalSize: 5},
	})
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	for _, path := range []string{"a.txt", "b.txt", "c.txt"} {
		openTestFileManagerFile(t, s, path, true)
	}
	s.activateFileManagerTab("c.txt")

	s.closeFileManagerTab("c.txt")
	if _, active, _, _, _, _ := fileManagerTabsForTest(s); active != "b.txt" {
		t.Fatalf("关闭 c.txt 后应激活 b.txt，得到 %q", active)
	}

	s.closeFileManagerTab("b.txt")
	if _, active, _, _, _, _ := fileManagerTabsForTest(s); active != "a.txt" {
		t.Fatalf("关闭 b.txt 后应激活 a.txt，得到 %q", active)
	}

	s.closeFileManagerTab("a.txt")
	paths, active, _, _, _, showList := fileManagerTabsForTest(s)
	if len(paths) != 0 || active != "" {
		t.Fatalf("关完最后一个 tab 后应清空，得到 %v / %q", paths, active)
	}
	if !showList {
		t.Fatal("关完最后一个 tab 后应回到目录列表")
	}
	if !isFileManagerOpen(s) {
		t.Fatal("关完最后一个 tab 后文件面板应保持打开")
	}
	waitFor(t, tt, func() bool { return tt.HasText("sub") })
}

// 外部入口打开的最后一个 tab 关掉后回到文件树，侧栏保持打开。
func TestFileManagerCloseLastExternalTabReturnsToTree(t *testing.T) {
	_, s, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	openTestFileManagerFileExternal(t, s, "a.txt")
	waitForFileManagerText(t, tt, s, "a.txt", "hello")

	s.closeFileManagerTab("a.txt")
	if !isFileManagerOpen(s) {
		t.Fatal("关掉最后一个 tab 后侧栏应保持打开")
	}
	if paths, _, _, _, _, showList := fileManagerTabsForTest(s); len(paths) != 0 || !showList {
		t.Fatalf("关掉最后一个 tab 后应回到树，得到 %v / showList=%v", paths, showList)
	}
	waitFor(t, tt, func() bool { return tt.HasText("sub") })
}

// 从侧栏打开的最后一个 tab 关掉后回到目录列表，面板保持打开。
func TestFileManagerCloseLastListTabKeepsPanel(t *testing.T) {
	for _, tc := range []struct {
		name string
		pin  bool
	}{
		{name: "pinned", pin: true},
		{name: "temporary", pin: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, s, tt := openFileManagerForTest(t)
			waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

			openTestFileManagerFile(t, s, "a.txt", tc.pin)
			waitForFileManagerText(t, tt, s, "a.txt", "hello")

			s.closeFileManagerTab("a.txt")
			paths, _, _, _, _, showList := fileManagerTabsForTest(s)
			if len(paths) != 0 {
				t.Fatalf("关掉最后一个 tab 后不应剩下 tab，得到 %v", paths)
			}
			if !isFileManagerOpen(s) {
				t.Fatal("关掉侧栏打开的最后一个 tab 后文件面板应保持打开")
			}
			if !showList {
				t.Fatal("关掉侧栏打开的最后一个 tab 后应回到目录列表")
			}
		})
	}
}

// 外部与侧栏各开一个 tab：先关外部那个，面板保持打开；再关侧栏那个，回到列表。
func TestFileManagerCloseExternalTabBeforeListTab(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "hello", TotalSize: 5},
		"b.txt": {Path: "b.txt", Kind: "text", Text: "world", TotalSize: 5},
	})
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	openTestFileManagerFileExternal(t, s, "b.txt")
	waitForFileManagerText(t, tt, s, "b.txt", "world")
	openTestFileManagerFile(t, s, "a.txt", true)
	waitForFileManagerText(t, tt, s, "a.txt", "hello")

	s.closeFileManagerTab("b.txt")
	if !isFileManagerOpen(s) {
		t.Fatal("还剩侧栏打开的 tab 时面板不应收起")
	}

	s.closeFileManagerTab("a.txt")
	paths, _, _, _, _, showList := fileManagerTabsForTest(s)
	if len(paths) != 0 {
		t.Fatalf("关完两个 tab 后不应剩下 tab，得到 %v", paths)
	}
	if !isFileManagerOpen(s) {
		t.Fatal("关掉侧栏打开的 tab 后文件面板应保持打开")
	}
	if !showList {
		t.Fatal("关掉侧栏打开的 tab 后应回到目录列表")
	}
}

// 拆分：anchor 是原 active，另一栏取相邻 tab；激活第三个 tab 只替换第二栏；
// 关闭 anchor 取消拆分；方向按钮翻转上下/左右。
func TestFileManagerSplitTabs(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "hello", TotalSize: 5},
		"b.txt": {Path: "b.txt", Kind: "text", Text: "world", TotalSize: 5},
		"c.txt": {Path: "c.txt", Kind: "text", Text: "again", TotalSize: 5},
	})
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	for _, path := range []string{"a.txt", "b.txt", "c.txt"} {
		openTestFileManagerFile(t, s, path, true)
	}
	s.activateFileManagerTab("a.txt")
	s.splitFileManager()

	_, _, anchor, side, vertical, _ := fileManagerTabsForTest(s)
	if anchor != "a.txt" || side != "b.txt" {
		t.Fatalf("拆分后应为 a.txt / b.txt，得到 %q / %q", anchor, side)
	}
	if vertical {
		t.Fatal("默认应为左右分栏")
	}

	s.activateFileManagerTab("c.txt")
	if _, active, anchor, side, _, _ := fileManagerTabsForTest(s); anchor != "a.txt" || side != "c.txt" || active != "c.txt" {
		t.Fatalf("激活第三个 tab 应只替换第二栏，得到 anchor=%q side=%q active=%q", anchor, side, active)
	}

	s.toggleFileManagerSplitDirection()
	if _, _, _, _, vertical, _ := fileManagerTabsForTest(s); !vertical {
		t.Fatal("切换方向后应为上下分栏")
	}

	s.closeFileManagerTab("a.txt")
	_, active, anchor, side, _, _ := fileManagerTabsForTest(s)
	if anchor != "" || side != "" {
		t.Fatalf("关闭固定栏应取消拆分，得到 anchor=%q side=%q", anchor, side)
	}
	if active != "c.txt" {
		t.Fatalf("关闭固定栏后应激活原第二栏，得到 %q", active)
	}
}

// 只有一个 tab 时拆分让两栏显示同一个文件。
func TestFileManagerSplitSingleTab(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "hello", TotalSize: 5},
	})
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	openTestFileManagerFile(t, s, "a.txt", true)
	s.splitFileManager()
	if _, _, anchor, side, _, _ := fileManagerTabsForTest(s); anchor != "a.txt" || side != "a.txt" {
		t.Fatalf("单 tab 拆分后两栏应相同，得到 %q / %q", anchor, side)
	}

	s.unsplitFileManager()
	if _, _, anchor, side, _, _ := fileManagerTabsForTest(s); anchor != "" || side != "" {
		t.Fatalf("取消拆分后应清空，得到 %q / %q", anchor, side)
	}
}

// 界面上点「拆分」后两栏的文件都能看到；点「返回文件树」树可见且 tab 还在，
// 再点 tab 又回到预览。
func TestFileManagerSplitUI(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	fake.SetFileContents(map[string]FileContent{
		"a.txt":    {Path: "a.txt", Kind: "text", Text: "hello", TotalSize: 5},
		"sub/b.go": {Path: "sub/b.go", Kind: "text", Text: "world", TotalSize: 5},
	})
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	openTestFileManagerFile(t, s, "a.txt", true)
	openTestFileManagerFile(t, s, "sub/b.go", true)
	waitForFileManagerText(t, tt, s, "sub/b.go", "world")

	if err := tt.Click("拆分"); err != nil {
		t.Fatalf("click split button failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return fileManagerTabText(s, "a.txt") == "hello" && fileManagerTabText(s, "sub/b.go") == "world"
	})

	if err := tt.Click("返回文件树"); err != nil {
		t.Fatalf("click back button failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("sub") })
	if paths, _, _, _, _, _ := fileManagerTabsForTest(s); len(paths) != 2 {
		t.Fatalf("返回文件树后应保留 tab，得到 %v", paths)
	}

	if err := tt.Click("sub"); err != nil {
		t.Fatalf("click sub dir failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("b.go") })
	if err := tt.Click("b.go"); err != nil {
		t.Fatalf("click file row failed: %v", err)
	}
	waitForFileManagerText(t, tt, s, "sub/b.go", "world")
	if _, _, _, _, _, showList := fileManagerTabsForTest(s); showList {
		t.Fatal("点击文件后应回到预览")
	}
}

// 同一切换目录保留 tab，切换会话（threadID 变了）才清空。
func TestFileManagerSessionSwitchClearsTabs(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "hello", TotalSize: 5},
	})
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	openTestFileManagerFile(t, s, "a.txt", true)
	threadID, _ := getSelectedThreadWorkDir(s)

	s.loadFileManagerDir(threadID, "sub")
	if paths, _, _, _, _, _ := fileManagerTabsForTest(s); len(paths) != 1 {
		t.Fatalf("同一会话内切换目录应保留 tab，得到 %v", paths)
	}

	s.loadFileManagerDir("another-thread", "")
	if paths, _, _, _, _, showList := fileManagerTabsForTest(s); len(paths) != 0 {
		t.Fatalf("切换会话应清空 tab，得到 %v", paths)
	} else if !showList {
		t.Fatal("切换会话后应显示目录列表")
	}
}
