package mcodeui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func dragTreeToComposer(t *testing.T, tt *ui.Tester, s *chatState, name string) {
	t.Helper()
	from, ok := tt.Find(name)
	if !ok {
		t.Fatalf("未找到拖动源 %s", name)
	}
	x, y := from.X+from.W/2, from.Y+from.H/2
	tt.Press(x, y)
	tt.Move(x-15, y+8)
	// 1400×800 测试窗口的聊天区，避开右栏和左侧会话列表。
	tt.Move(550, 180)
	if !composerFileDragOver(s) {
		t.Fatal("内部引用未复用拖入高亮")
	}
	tt.Release(550, 180)
	tt.Advance(DurSheet + 50*time.Millisecond)
}

func TestFileTreeDragReference(t *testing.T) {
	client := &inspectorContextClient{FakeClient: treeTestBase(), requests: make(chan inspectorUploadRequest, 8)}
	s, tt := openFileTreeForTest(t, client)
	t.Cleanup(s.close)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })
	dragTreeToComposer(t, tt, s, "a.txt")
	req := inspectorUpload(t, client)
	if req.name != "a.txt" || req.mime != "text/plain" || string(req.data) == "hello" {
		t.Fatalf("拖动上传了文件正文或类型错误：%+v", req)
	}
	req.finish <- nil
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.attachments) == 1 && s.attachments[0].ID != "" && s.draft == ""
	})
	if err := tt.Click("删除附件 a.txt"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.attachments) == 0
	})
}

func TestFileTreeMultiSelectionDropOrder(t *testing.T) {
	client := &inspectorContextClient{FakeClient: treeTestBase(), requests: make(chan inspectorUploadRequest, 8)}
	s, tt := openFileTreeForTest(t, client)
	t.Cleanup(s.close)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	st.tree.outline.Open.Add("sub")
	fileManagerMu.Unlock()
	waitFor(t, tt, func() bool { return tt.HasText("b.go") })
	fileManagerMu.Lock()
	st.tree.selection.Add("a.txt")
	st.tree.selection.Add("sub/b.go")
	st.tree.selection.Add("sub")
	fileManagerMu.Unlock()
	tt.Frame()
	dragTreeToComposer(t, tt, s, "sub")
	for range 3 {
		inspectorUpload(t, client).finish <- nil
	}
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.attachments) == 3 && s.attachments[0].ID != "" && s.attachments[1].ID != "" && s.attachments[2].ID != ""
	})
	s.mu.Lock()
	names := []string{s.attachments[0].Name, s.attachments[1].Name, s.attachments[2].Name}
	s.mu.Unlock()
	if !slices.Equal(names, []string{"sub", "sub/b.go", "a.txt"}) {
		t.Fatalf("多选引用未按树顺序添加：%v", names)
	}
}

func TestFileTreeDragRejectsProtectedAndDeleted(t *testing.T) {
	s, _ := inspectorContextSetup(t)
	if _, ok := fileTreeDragReference(s, "t1", FileEntry{Path: "secret.env", Protected: true}); ok {
		t.Fatal("受保护条目可拖动")
	}
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	st.tree.onlyChanged = true
	st.tree.git = map[string]GitChangedFile{"gone.go": {Path: "gone.go", Status: "D"}}
	fileManagerMu.Unlock()
	if _, ok := fileTreeDragReference(s, "t1", FileEntry{Path: "gone.go"}); ok {
		t.Fatal("已删除占位行可拖动")
	}
}

func TestFileTreeMultiSelectionMenuReference(t *testing.T) {
	s, tt := openFileTreeForTest(t, treeTestBase())
	t.Cleanup(s.close)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	st.tree.selection.Add("a.txt")
	st.tree.selection.Add("sub")
	fileManagerMu.Unlock()
	tt.Frame()
	if err := tt.RightClick("sub"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("引用到输入框"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.attachments) == 2 && s.attachments[0].Name == "sub" && s.attachments[1].Name == "a.txt" && s.draft == ""
	})
}

func TestFileManagerHeaderReferenceIncludesLines(t *testing.T) {
	s, tt := openFileTreeForTest(t, treeTestBase())
	t.Cleanup(s.close)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })
	threadID, _ := getSelectedThreadWorkDir(s)
	s.openFileManagerFileExternalAt(threadID, "a.txt", 2, 4)
	waitForFileManagerText(t, tt, s, "a.txt", "hello")
	tt.Advance(DurSheet + 50*time.Millisecond)
	if err := tt.Click("引用到输入框"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.attachments) == 1 && s.attachments[0].Name == "a.txt:2-4" && s.attachments[0].ID != "" && s.draft == ""
	})
}

func TestComposerExternalFileDropUnchanged(t *testing.T) {
	client := &inspectorContextClient{FakeClient: treeTestBase(), requests: make(chan inspectorUploadRequest, 8)}
	s, tt := openFileTreeForTest(t, client)
	t.Cleanup(s.close)
	path := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(path, []byte("外部文件正文"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !tt.DropFiles(550, 180, path) {
		t.Fatal("聊天区未接收系统文件")
	}
	req := inspectorUpload(t, client)
	if req.name != "external.txt" || string(req.data) != "外部文件正文" {
		t.Fatalf("系统文件未走原上传链路：%+v", req)
	}
	req.finish <- errors.New("外部上传失败")
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.attachments) == 1 && s.attachments[0].Err != "" && s.attachments[0].InspectorRef == nil
	})
	tt.Advance(DurSheet + 50*time.Millisecond)
	if err := tt.Click("重试上传 external.txt"); err != nil {
		t.Fatal(err)
	}
	retry := inspectorUpload(t, client)
	if string(retry.data) != string(req.data) || retry.mime != req.mime {
		t.Fatal("系统文件重试未保留原数据")
	}
	retry.finish <- nil
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.attachments[0].ID != "" && s.attachments[0].Err == "" && !s.attachments[0].Uploading
	})
}

func TestFileTreeDragLabel(t *testing.T) {
	client := newWatchTestClient(treeTestBase())
	client.setChanged([]GitChangedFile{
		{Path: "a.txt", Status: "M"},
		{Path: "gone.go", Status: "D"},
	})
	s, tt := openFileTreeForTest(t, client)
	t.Cleanup(s.close)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") && tt.HasText("sub") })

	st := getFileManagerState(s)
	fileManagerMu.Lock()
	st.tree.outline.Open.Add("sub")
	fileManagerMu.Unlock()
	waitFor(t, tt, func() bool { return tt.HasText("b.go") })

	threadID := fileManagerThreadID(s)
	fileManagerMu.Lock()
	aIdx := fileTreeRowIndexLocked(st.tree, "a.txt")
	subIdx := fileTreeRowIndexLocked(st.tree, "sub")
	bIdx := fileTreeRowIndexLocked(st.tree, "sub/b.go")
	outline := &st.tree.outline
	fileManagerMu.Unlock()
	if aIdx < 0 || subIdx < 0 || bIdx < 0 {
		t.Fatalf("未找到文件树行下标：a=%d sub=%d b=%d", aIdx, subIdx, bIdx)
	}

	if got := outline.List.DragLabel([]int{bIdx}); got != "b.go" {
		t.Fatalf("单个嵌套文件 DragLabel = %q, want %q", got, "b.go")
	}
	if got := outline.List.DragLabel([]int{aIdx, subIdx, bIdx}); got != "3 个项目" {
		t.Fatalf("多选 DragLabel = %q, want %q", got, "3 个项目")
	}

	s.fileTreeToggleOnlyChanged(threadID)
	waitFor(t, tt, func() bool { return tt.HasText("gone.go") })

	fileManagerMu.Lock()
	changedOutline := fileTreeChangedOutlineLocked(st.tree)
	goneIdx := -1
	changedAIdx := -1
	for row := 0; row < changedOutline.Rows(); row++ {
		switch changedOutline.Item(row) {
		case "gone.go":
			goneIdx = row
		case "a.txt":
			changedAIdx = row
		}
	}
	fileManagerMu.Unlock()
	if goneIdx < 0 || changedAIdx < 0 {
		t.Fatalf("未找到只看改动视图行下标：gone=%d a=%d", goneIdx, changedAIdx)
	}
	if got := changedOutline.List.DragRows([]int{goneIdx}); got != nil {
		t.Fatalf("删除态行 DragRows = %#v, want nil", got)
	}
	if got := changedOutline.List.DragLabel([]int{goneIdx}); got != "" {
		t.Fatalf("删除态行 DragLabel = %q, want empty", got)
	}
	if got := changedOutline.List.DragLabel([]int{changedAIdx}); got != "a.txt" {
		t.Fatalf("改动文件 DragLabel = %q, want %q", got, "a.txt")
	}
}

func TestFileTreeDragInvalidPathsUpdateAndClear(t *testing.T) {
	if fileTreeUpdateDragInvalidPathsLocked(nil, true, []string{"a.txt"}) {
		t.Fatal("nil tree 更新返回 true，want false")
	}

	tree := &fileTreeState{}
	if fileTreeUpdateDragInvalidPathsLocked(tree, false, nil) {
		t.Fatal("初始空集合清空返回 true，want false")
	}

	if !fileTreeUpdateDragInvalidPathsLocked(tree, true, []string{"a.txt", "sub"}) {
		t.Fatal("首次设置非法拖动路径集合返回 false，want true")
	}
	if len(tree.dragInvalidPaths) != 2 || !tree.dragInvalidPaths["a.txt"] || !tree.dragInvalidPaths["sub"] {
		t.Fatalf("dragInvalidPaths = %#v, want a.txt and sub", tree.dragInvalidPaths)
	}

	if fileTreeUpdateDragInvalidPathsLocked(tree, true, []string{"sub", "a.txt", "sub"}) {
		t.Fatal("相同路径集合重复更新返回 true，want false")
	}

	if !fileTreeUpdateDragInvalidPathsLocked(tree, true, []string{"b.go"}) {
		t.Fatal("切换路径集合返回 false，want true")
	}
	if len(tree.dragInvalidPaths) != 1 || !tree.dragInvalidPaths["b.go"] || tree.dragInvalidPaths["a.txt"] {
		t.Fatalf("dragInvalidPaths = %#v, want only b.go", tree.dragInvalidPaths)
	}

	if !fileTreeUpdateDragInvalidPathsLocked(tree, false, []string{"b.go"}) {
		t.Fatal("目标合法时清空路径集合返回 false，want true")
	}
	if len(tree.dragInvalidPaths) != 0 {
		t.Fatalf("清空后 dragInvalidPaths = %#v, want empty", tree.dragInvalidPaths)
	}
	if fileTreeUpdateDragInvalidPathsLocked(tree, false, nil) {
		t.Fatal("已清空后再次清空返回 true，want false")
	}

	if !fileTreeUpdateDragInvalidPathsLocked(tree, true, []string{"c.go"}) {
		t.Fatal("重新设置非法拖动路径集合返回 false，want true")
	}
	if !fileTreeUpdateDragInvalidPathsLocked(tree, true, nil) {
		t.Fatal("空切片更新未清空旧集合，want true")
	}
	if len(tree.dragInvalidPaths) != 0 {
		t.Fatalf("空切片清空后 dragInvalidPaths = %#v, want empty", tree.dragInvalidPaths)
	}
}
