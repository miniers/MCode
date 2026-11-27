package mcodeui

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestFileManagerSplitThreshold(t *testing.T) {
	fake := treeTestBase()
	s, tt := openFileTreeForTest(t, fake)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	// 宽度 480（小于 560）：打开文件后只显示正文，不并排。
	inspectorSession(s).Width = 480
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	st.treeNavOpen = true
	fileManagerMu.Unlock()
	s.invalidate()
	tt.Advance(DurSheet + 50*time.Millisecond)

	if err := tt.Click("a.txt"); err != nil {
		t.Fatalf("click a.txt failed: %v", err)
	}
	waitForFileManagerText(t, tt, s, "a.txt", "hello")
	if _, ok := tt.Find("返回文件树"); !ok {
		t.Fatal("宽度 480 打开文件后应显示「返回文件树」")
	}
	if _, ok := tt.Find("文件导航"); ok {
		t.Fatal("宽度 480 不应并排显示文件导航")
	}

	// 宽度 560：树与正文并排显示。
	inspectorSession(s).Width = 560
	s.invalidate()
	tt.Advance(DurSheet + DurBase + 50*time.Millisecond)
	if _, ok := tt.Find("文件导航"); !ok {
		t.Fatal("宽度 560 应并排显示文件导航")
	}
	if _, ok := tt.Find("返回文件树"); ok {
		t.Fatal("宽度 560 并排时不应显示窄栏返回按钮")
	}
	if _, ok := tt.Find("收起文件导航"); !ok {
		t.Fatal("宽度 560 并排时应显示「收起文件导航」按钮")
	}
}

func TestFileManagerNavWidthDragAndPersist(t *testing.T) {
	setAppReduceMotion(t, true)
	fake := treeTestBase()
	s, tt := openFileTreeForTest(t, fake)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	inspectorSession(s).Width = 800
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	st.treeNavOpen = true
	fileManagerMu.Unlock()
	s.invalidate()
	tt.Frame()

	openTestFileManagerFile(t, s, "a.txt", true)
	waitForFileManagerText(t, tt, s, "a.txt", "hello")
	tt.Frame()

	navRect := mustFindRect(t, tt, "文件导航")
	if math.Abs(float64(navRect.W-fileTreeNavWidth)) > 1 {
		t.Fatalf("默认导航宽度应为 %v，得到 %v", fileTreeNavWidth, navRect.W)
	}

	handleAcc, ok := tt.AccessByRole(ui.RoleSplitter, fileTreeNavHandleLabel)
	if !ok {
		t.Fatalf("找不到手柄 %q: %v", fileTreeNavHandleLabel, tt.AccessAll())
	}
	hx, hy := handleAcc.Bounds.X+handleAcc.Bounds.W/2, handleAcc.Bounds.Y+handleAcc.Bounds.H/2
	dragHandle := func(dx float32) {
		curAcc, ok := tt.AccessByRole(ui.RoleSplitter, fileTreeNavHandleLabel)
		if !ok {
			t.Fatalf("拖动前找不到手柄 %q", fileTreeNavHandleLabel)
		}
		x := curAcc.Bounds.X + curAcc.Bounds.W/2
		y := curAcc.Bounds.Y + curAcc.Bounds.H/2
		tt.Press(x, y)
		for step := 1; step <= 4; step++ {
			tt.Move(x+dx*float32(step)/4, y)
		}
		tt.Release(x+dx, y)
		tt.Frame()
	}
	_ = hx
	_ = hy

	// 向右拖动 60 DIP -> 约 260 DIP。
	dragHandle(60)
	widened := mustFindRect(t, tt, "文件导航").W
	if math.Abs(float64(widened-260)) > 2 {
		t.Fatalf("拖动后导航宽度应约为 260，得到 %v", widened)
	}

	// 拖超上限 -> 夹到 360 DIP。
	dragHandle(400)
	maxed := mustFindRect(t, tt, "文件导航").W
	if math.Abs(float64(maxed-fileTreeNavMaxWidth)) > 1 {
		t.Fatalf("宽 800 时导航上限应为 %v，得到 %v", fileTreeNavMaxWidth, maxed)
	}

	// 拖超下限 -> 夹到 160 DIP。
	dragHandle(-400)
	mined := mustFindRect(t, tt, "文件导航").W
	if math.Abs(float64(mined-fileTreeNavMinWidth)) > 1 {
		t.Fatalf("导航下限应为 %v，得到 %v", fileTreeNavMinWidth, mined)
	}

	// 面板 560 时上限受正文至少留 320 限制。
	inspectorSession(s).Width = 560
	s.invalidate()
	tt.Frame()
	dragHandle(300)
	clamped560 := mustFindRect(t, tt, "文件导航").W
	want560Max := 560 - fileTreeNavContentMinWidth - sidePaneHandleWidth
	if math.Abs(float64(clamped560-want560Max)) > 1 {
		t.Fatalf("面板 560 时导航上限应为 %v，得到 %v", want560Max, clamped560)
	}

	// 持久化与恢复。
	s.mu.Lock()
	pref := s.inspector.Preferences["t1"]
	s.mu.Unlock()
	if math.Abs(float64(pref.FileNavWidth-clamped560)) > 1 {
		t.Fatalf("偏好 FileNavWidth = %v，期望 %v", pref.FileNavWidth, clamped560)
	}

	st2 := &fileManagerState{threadID: "t1"}
	fileManagerMu.Lock()
	fileTreeSeedPreference(st2, pref)
	restoredW, _ := fileTreeViewPreferenceLocked(st2)
	fileManagerMu.Unlock()
	if math.Abs(float64(restoredW-clamped560)) > 1 {
		t.Fatalf("seed 恢复的宽度 = %v，期望 %v", restoredW, clamped560)
	}

	// normalize 把 NaN、Inf 和负数归 0。
	for _, invalid := range []float32{float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1)), -20} {
		got := normalizeInspectorPreference(InspectorPreference{FileNavWidth: invalid})
		if got.FileNavWidth != 0 {
			t.Fatalf("normalizeInspectorPreference(%v).FileNavWidth = %v，期望 0", invalid, got.FileNavWidth)
		}
	}
}

func TestFileTreeMoreMenuCollapseAll(t *testing.T) {
	tree := map[string][]FileEntry{
		"": {
			{Name: "dir1", Path: "dir1", IsDir: true},
			{Name: "dir2", Path: "dir2", IsDir: true},
			{Name: "root.txt", Path: "root.txt", Size: 5},
		},
		"dir1": {{Name: "f1.go", Path: "dir1/f1.go", Size: 5}},
		"dir2": {{Name: "f2.go", Path: "dir2/f2.go", Size: 5}},
	}
	fake := treeTestBase()
	fake.SetFileTree(tree)
	_, tt := openFileTreeForTest(t, newTreeListRecorder(fake, tree))
	waitFor(t, tt, func() bool { return tt.HasText("dir1") && tt.HasText("dir2") })

	if err := tt.Click("dir1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("f1.go") })
	if err := tt.Click("dir2"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("f2.go") })

	if err := tt.Click("更多"); err != nil {
		t.Fatalf("点击「更多」按钮失败: %v", err)
	}
	if err := tt.ChooseMenuItem("全部折叠"); err != nil {
		t.Fatalf("选择「全部折叠」失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return !tt.HasText("f1.go") && !tt.HasText("f2.go")
	})
}

func TestFileTreeMoreMenuRevealActive(t *testing.T) {
	tree := map[string][]FileEntry{
		"": {
			{Name: "sub", Path: "sub", IsDir: true},
			{Name: "a.txt", Path: "a.txt", Size: 5},
		},
		"sub":      {{Name: "deep", Path: "sub/deep", IsDir: true}},
		"sub/deep": {{Name: "c.go", Path: "sub/deep/c.go", Size: 5}},
	}
	fake := treeTestBase()
	fake.SetFileTree(tree)
	fake.SetFiles([]FileMatch{{Name: "c.go", Path: "sub/deep/c.go"}})
	fake.SetFileContents(map[string]FileContent{
		"a.txt":         {Path: "a.txt", Kind: "text", Text: "hello", TotalSize: 5},
		"sub/deep/c.go": {Path: "sub/deep/c.go", Kind: "text", Text: "package deep\n", TotalSize: 13},
	})
	s, tt := openFileTreeForTest(t, newTreeListRecorder(fake, tree))
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	inspectorSession(s).Width = 800
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	st.treeNavOpen = true
	fileManagerMu.Unlock()
	s.invalidate()
	tt.Advance(DurSheet + 50*time.Millisecond)

	// 用搜索打开深层文件。
	if err := tt.Click("搜索文件"); err != nil {
		t.Fatal(err)
	}
	tt.Type("c.go")
	waitFor(t, tt, func() bool { return tt.HasText("sub/deep/c.go") })
	if err := tt.Click("sub/deep/c.go"); err != nil {
		t.Fatal(err)
	}
	waitForFileManagerText(t, tt, s, "sub/deep/c.go", "package deep\n")

	// 清除搜索后先展开一层再全部折叠，确认树里不再有深层行。
	if err := tt.Click("清除搜索"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("sub") })
	if err := tt.Click("sub"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("deep") })
	s.fileTreeCollapseAll()
	waitFor(t, tt, func() bool { return !tt.HasText("deep") })

	hasNavText := func(text string) bool {
		inNav := false
		for _, item := range tt.Texts() {
			if item == "文件导航" {
				inNav = true
				continue
			}
			if item == "调整文件导航宽度" {
				break
			}
			if inNav && item == text {
				return true
			}
		}
		return false
	}
	if hasNavText("c.go") {
		t.Fatal("全部折叠后导航树不应看到深层文件 c.go")
	}

	// 通过「更多」菜单执行「定位当前文件」。
	if err := tt.Click("更多"); err != nil {
		t.Fatalf("点击「更多」失败: %v", err)
	}
	if err := tt.ChooseMenuItem("定位当前文件"); err != nil {
		t.Fatalf("选择「定位当前文件」失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("deep") && hasNavText("c.go") && treeSelectionSnapshotForTest(s)["sub/deep/c.go"]
	})
}

func TestFileTreeHideIgnored(t *testing.T) {
	tree := map[string][]FileEntry{
		"": {
			{Name: "a.txt", Path: "a.txt", Size: 5},
			{Name: "ignored.log", Path: "ignored.log", Size: 8, Ignored: true},
			{Name: "dist", Path: "dist", IsDir: true, Ignored: true},
		},
	}
	fake := treeTestBase()
	fake.SetFileTree(tree)
	client := newTreeListRecorder(fake, tree)
	s, tt := openFileTreeForTest(t, client)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") && tt.HasText("ignored.log") && tt.HasText("dist") })

	s.fileTreeSelect("ignored.log", 0)
	if !treeSelectionSnapshotForTest(s)["ignored.log"] {
		t.Fatal("选中 ignored.log 失败")
	}
	callsBefore := client.totalCalls()

	// 关闭「显示忽略的文件」：不重新读盘，Ignored 条目隐藏并移出选区。
	if err := tt.Click("更多"); err != nil {
		t.Fatalf("点击「更多」失败: %v", err)
	}
	if err := tt.ChooseMenuItem("显示忽略的文件"); err != nil {
		t.Fatalf("选择「显示忽略的文件」失败: %v", err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("ignored.log") && !tt.HasText("dist") })
	if !tt.HasText("a.txt") {
		t.Fatal("非忽略文件 a.txt 仍应显示")
	}
	if treeSelectionSnapshotForTest(s)["ignored.log"] {
		t.Fatal("被隐藏的 ignored.log 应移出选区")
	}
	if got := client.totalCalls(); got != callsBefore {
		t.Fatalf("切换显示忽略文件不应重新读盘：before=%d after=%d", callsBefore, got)
	}

	s.mu.Lock()
	pref := s.inspector.Preferences["t1"]
	s.mu.Unlock()
	if !pref.FileHideIgnored {
		t.Fatal("偏好 FileHideIgnored 应为 true")
	}

	st2 := &fileManagerState{threadID: "t1"}
	fileManagerMu.Lock()
	fileTreeSeedPreference(st2, pref)
	_, restoredHide := fileTreeViewPreferenceLocked(st2)
	fileManagerMu.Unlock()
	if !restoredHide {
		t.Fatal("seed 恢复的 FileHideIgnored 应为 true")
	}

	// 「只看改动」视图也隐藏 Ignored 条目。
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	ft := ensureFileTreeLocked(st)
	ft.git = map[string]GitChangedFile{
		"a.txt":       {Path: "a.txt", Status: "M"},
		"ignored.log": {Path: "ignored.log", Status: "?"},
	}
	ft.gitRev++
	ft.changedCacheOK = false
	roots, _, _ := fileTreeChangedRowsLocked(ft)
	fileManagerMu.Unlock()
	if len(roots) != 1 || roots[0] != "a.txt" {
		t.Fatalf("只看改动视图应隐藏 Ignored 条目，得到 %v", roots)
	}

	// 再打开：不重新读盘，Ignored 条目重新显示。
	if err := tt.Click("更多"); err != nil {
		t.Fatalf("再次点击「更多」失败: %v", err)
	}
	if err := tt.ChooseMenuItem("显示忽略的文件"); err != nil {
		t.Fatalf("再次选择「显示忽略的文件」失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("ignored.log") && tt.HasText("dist") })
	if got := client.totalCalls(); got != callsBefore {
		t.Fatalf("重新显示忽略文件不应重新读盘：before=%d after=%d", callsBefore, got)
	}
}
