package mcodeui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type moveOpsTestClient struct {
	*FakeClient

	mu       sync.Mutex
	tree     map[string][]FileEntry
	failPath string
	failErr  error
}

func newMoveOpsTestClient(fake *FakeClient, tree map[string][]FileEntry) *moveOpsTestClient {
	copied := make(map[string][]FileEntry, len(tree))
	for dir, entries := range tree {
		list := make([]FileEntry, len(entries))
		copy(list, entries)
		copied[dir] = list
	}
	fake.SetFileTree(copied)
	return &moveOpsTestClient{
		FakeClient: fake,
		tree:       copied,
	}
}

func (c *moveOpsTestClient) FilesList(ctx context.Context, threadID, subpath string) (FileListing, error) {
	c.mu.Lock()
	entries := append([]FileEntry(nil), c.tree[subpath]...)
	c.mu.Unlock()
	return FileListing{Path: subpath, Entries: entries}, nil
}

func (c *moveOpsTestClient) FilesRename(ctx context.Context, threadID, oldPath, newPath string) (string, error) {
	c.mu.Lock()
	failPath, failErr := c.failPath, c.failErr
	c.mu.Unlock()
	if failErr != nil && oldPath == failPath {
		return "", failErr
	}
	res, err := c.FakeClient.FilesRename(ctx, threadID, oldPath, newPath)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	oldParent := fileManagerParentPath(oldPath)
	newParent := fileManagerParentPath(newPath)
	var movedEntry FileEntry
	found := false
	kept := make([]FileEntry, 0, len(c.tree[oldParent]))
	for _, entry := range c.tree[oldParent] {
		if entry.Path == oldPath {
			movedEntry = entry
			found = true
			continue
		}
		kept = append(kept, entry)
	}
	c.tree[oldParent] = kept
	if !found {
		movedEntry = FileEntry{Name: fileManagerTabName(newPath), Path: newPath}
	} else {
		movedEntry.Name = fileManagerTabName(newPath)
		movedEntry.Path = newPath
	}
	c.tree[newParent] = append(c.tree[newParent], movedEntry)
	if movedEntry.IsDir {
		oldPrefix := oldPath + "/"
		newPrefix := newPath + "/"
		for dir, entries := range c.tree {
			if dir == oldPath || strings.HasPrefix(dir, oldPrefix) {
				nextDir := newPath + dir[len(oldPath):]
				updated := make([]FileEntry, len(entries))
				for i, entry := range entries {
					entry.Path = newPrefix + strings.TrimPrefix(entry.Path, oldPrefix)
					updated[i] = entry
				}
				delete(c.tree, dir)
				c.tree[nextDir] = updated
			}
		}
	}
	c.mu.Unlock()
	return res, nil
}

func dragElementToPoint(tt *ui.Tester, from ui.Rect, tx, ty float32) {
	x, y := from.X+from.W/2, from.Y+from.H/2
	tt.Press(x, y)
	for step := 1; step <= 4; step++ {
		fraction := float32(step) / 4
		tt.Move(x+(tx-x)*fraction, y+(ty-y)*fraction)
	}
	tt.Release(tx, ty)
	tt.Frame()
}

func dragElementToElement(tt *ui.Tester, from, to ui.Rect) {
	dragElementToPoint(tt, from, to.X+to.W/2, to.Y+to.H/2)
}

func renameOpCalls(fake *FakeClient) []FileOpCall {
	var out []FileOpCall
	for _, call := range fake.FileOpCalls() {
		if call.Op == "rename" {
			out = append(out, call)
		}
	}
	return out
}

func TestFileTreeMoveTargetRules(t *testing.T) {
	entries := map[string]FileEntry{
		"a.txt":          {Name: "a.txt", Path: "a.txt"},
		"b.txt":          {Name: "b.txt", Path: "b.txt"},
		"sub":            {Name: "sub", Path: "sub", IsDir: true},
		"sub/b.go":       {Name: "b.go", Path: "sub/b.go"},
		"sub/inner":      {Name: "inner", Path: "sub/inner", IsDir: true},
		"sub/inner/c.go": {Name: "c.go", Path: "sub/inner/c.go"},
		"dest":           {Name: "dest", Path: "dest", IsDir: true},
		"dest/dup.txt":   {Name: "dup.txt", Path: "dest/dup.txt"},
		"dup.txt":        {Name: "dup.txt", Path: "dup.txt"},
		"secret":         {Name: "secret", Path: "secret", IsDir: true, Protected: true},
		"secret/sub":     {Name: "sub", Path: "secret/sub", IsDir: true},
		"unloaded":       {Name: "unloaded", Path: "unloaded", IsDir: true},
	}
	children := map[string][]string{
		"":           {"a.txt", "b.txt", "dup.txt", "sub", "dest", "secret", "unloaded"},
		"sub":        {"sub/b.go", "sub/inner"},
		"sub/inner":  {"sub/inner/c.go"},
		"dest":       {"dest/dup.txt"},
		"secret":     {"secret/sub"},
		"secret/sub": {},
	}

	cases := []struct {
		name       string
		paths      []string
		targetDir  string
		wantMove   []string
		wantReason string
	}{
		{
			name:      "同时含目录与内部文件只移动目录",
			paths:     []string{"sub", "sub/b.go", "sub/inner/c.go"},
			targetDir: "dest",
			wantMove:  []string{"sub"},
		},
		{
			name:      "内部文件在前目录在后同样折叠",
			paths:     []string{"sub/b.go", "sub"},
			targetDir: "dest",
			wantMove:  []string{"sub"},
		},
		{
			name:       "目标不能是载荷目录本身",
			paths:      []string{"sub"},
			targetDir:  "sub",
			wantReason: "不可移入自身或子目录",
		},
		{
			name:       "目标不能是载荷目录的子目录",
			paths:      []string{"a.txt", "sub"},
			targetDir:  "sub/inner",
			wantReason: "不可移入自身或子目录",
		},
		{
			name:       "目标不能是受保护目录",
			paths:      []string{"a.txt"},
			targetDir:  "secret",
			wantReason: "不能移动到受保护的文件夹",
		},
		{
			name:       "目标祖先不能是受保护目录",
			paths:      []string{"a.txt"},
			targetDir:  "secret/sub",
			wantReason: "不能移动到受保护的文件夹",
		},
		{
			name:      "全部父目录已是根目录时不移动也不提示",
			paths:     []string{"a.txt", "b.txt"},
			targetDir: "",
		},
		{
			name:      "全部父目录已是目标目录时不移动也不提示",
			paths:     []string{"sub/b.go"},
			targetDir: "sub",
		},
		{
			name:      "部分父目录已是目标时跳过这些路径",
			paths:     []string{"a.txt", "sub/b.go"},
			targetDir: "",
			wantMove:  []string{"sub/b.go"},
		},
		{
			name:      "部分父目录已是目标目录时跳过已在该目录的项",
			paths:     []string{"sub/b.go", "a.txt"},
			targetDir: "sub",
			wantMove:  []string{"a.txt"},
		},
		{
			name:       "目标已加载子项存在同名条目整体不合法",
			paths:      []string{"a.txt", "dup.txt"},
			targetDir:  "dest",
			wantReason: "目标文件夹已有同名项：dup.txt",
		},
		{
			name:      "目标未加载子项允许移动",
			paths:     []string{"dup.txt"},
			targetDir: "unloaded",
			wantMove:  []string{"dup.txt"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotMove, gotReason := fileTreePlanMove(tc.paths, tc.targetDir, entries, children)
			if !slices.Equal(gotMove, tc.wantMove) {
				t.Fatalf("movePaths = %v, want %v", gotMove, tc.wantMove)
			}
			if gotReason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", gotReason, tc.wantReason)
			}
		})
	}
}

func TestFileTreeDragMoveIntoDir(t *testing.T) {
	tree := map[string][]FileEntry{
		"": {
			{Name: "a.txt", Path: "a.txt", Size: 5},
			{Name: "c.txt", Path: "c.txt", Size: 6},
			{Name: "sub", Path: "sub", IsDir: true},
		},
		"sub": {
			{Name: "b.go", Path: "sub/b.go", Size: 20},
		},
	}
	fake := treeTestBase()
	fake.SetFileContents(map[string]FileContent{
		"a.txt":    {Path: "a.txt", Kind: "text", Text: "alpha", TotalSize: 5},
		"c.txt":    {Path: "c.txt", Kind: "text", Text: "gamma", TotalSize: 5},
		"sub/b.go": {Path: "sub/b.go", Kind: "text", Text: "beta", TotalSize: 4},
	})
	client := newMoveOpsTestClient(fake, tree)
	s, tt := openFileTreeForTest(t, client)
	t.Cleanup(s.close)

	threadID := fileManagerThreadID(s)
	s.openFileManagerFile(threadID, "a.txt", true)
	waitForFileManagerText(t, tt, s, "a.txt", "alpha")
	s.showFileManagerList()
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") && tt.HasText("sub") })

	from := mustFindRect(t, tt, "a.txt")
	to := mustFindRect(t, tt, "sub")
	dragElementToElement(tt, from, to)

	waitFor(t, tt, func() bool {
		calls := renameOpCalls(fake)
		return len(calls) == 1 && calls[0].Path == "a.txt" && calls[0].NewPath == "sub/a.txt"
	})
	waitFor(t, tt, func() bool {
		return treeOpenForTest(s)["sub"] && tt.HasText("a.txt") && tt.HasText("b.go")
	})

	if tabs := fileManagerTabPaths(s); !slices.Equal(tabs, []string{"sub/a.txt"}) {
		t.Fatalf("打开的标签路径未更新：%v", tabs)
	}
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	selectedA := st.tree.selection.Has("sub/a.txt")
	inRoot := slices.Contains(st.tree.children[""], "a.txt")
	inSub := slices.Contains(st.tree.children["sub"], "sub/a.txt")
	fileManagerMu.Unlock()
	if inRoot || !inSub || !selectedA {
		t.Fatalf("树缓存或选区未同步：inRoot=%v inSub=%v selected=%v", inRoot, inSub, selectedA)
	}

	// 多选两个文件（sub/a.txt 与 c.txt）拖到空白区域（根目录）。
	// 先选中两个文件。
	fileManagerMu.Lock()
	st.tree.selection.Clear()
	st.tree.selection.Add("sub/a.txt")
	st.tree.selection.Add("c.txt")
	fileManagerMu.Unlock()
	tt.Frame()

	// 再测试多选拖动两个文件到另一个目录前，先把 sub/a.txt 与 sub/b.go 一起拖到空白区域移回根目录，
	// 随后多选 a.txt 与 c.txt 拖入 sub，覆盖「多选两个文件拖动时，两个都被移动」。
	fileManagerMu.Lock()
	st.tree.selection.Clear()
	st.tree.selection.Add("sub/a.txt")
	st.tree.selection.Add("sub/b.go")
	fileManagerMu.Unlock()
	tt.Frame()

	dragFrom := mustFindRect(t, tt, "a.txt")
	cRect := mustFindRect(t, tt, "c.txt")
	blankX, blankY := cRect.X+cRect.W/2, cRect.Y+cRect.H+80
	dragElementToPoint(tt, dragFrom, blankX, blankY)

	waitFor(t, tt, func() bool {
		return len(renameOpCalls(fake)) == 3
	})
	waitFor(t, tt, func() bool {
		fileManagerMu.Lock()
		defer fileManagerMu.Unlock()
		return slices.Contains(st.tree.children[""], "a.txt") && slices.Contains(st.tree.children[""], "b.go")
	})

	fileManagerMu.Lock()
	st.tree.selection.Clear()
	st.tree.selection.Add("a.txt")
	st.tree.selection.Add("c.txt")
	fileManagerMu.Unlock()
	tt.Frame()

	fromMulti := mustFindRect(t, tt, "a.txt")
	toSub := mustFindRect(t, tt, "sub")
	dragElementToElement(tt, fromMulti, toSub)

	waitFor(t, tt, func() bool {
		calls := renameOpCalls(fake)
		if len(calls) != 5 {
			return false
		}
		got := []string{calls[3].Path + "->" + calls[3].NewPath, calls[4].Path + "->" + calls[4].NewPath}
		return slices.Contains(got, "a.txt->sub/a.txt") && slices.Contains(got, "c.txt->sub/c.txt")
	})
	waitFor(t, tt, func() bool {
		fileManagerMu.Lock()
		defer fileManagerMu.Unlock()
		return slices.Contains(st.tree.children["sub"], "sub/a.txt") &&
			slices.Contains(st.tree.children["sub"], "sub/c.txt") &&
			st.tree.selection.Has("sub/a.txt") &&
			st.tree.selection.Has("sub/c.txt")
	})
}

func TestFileTreeDragMoveRejected(t *testing.T) {
	tree := map[string][]FileEntry{
		"": {
			{Name: "sub", Path: "sub", IsDir: true},
		},
		"sub": {
			{Name: "inner", Path: "sub/inner", IsDir: true},
		},
		"sub/inner": {
			{Name: "c.go", Path: "sub/inner/c.go", Size: 10},
		},
	}
	fake := treeTestBase()
	client := newMoveOpsTestClient(fake, tree)
	s, tt := openFileTreeForTest(t, client)
	t.Cleanup(s.close)

	st := getFileManagerState(s)
	fileManagerMu.Lock()
	st.tree.outline.Open.Add("sub")
	fileManagerMu.Unlock()
	waitFor(t, tt, func() bool { return tt.HasText("sub") && tt.HasText("inner") })

	from := mustFindRect(t, tt, "sub")
	to := mustFindRect(t, tt, "inner")
	x, y := from.X+from.W/2, from.Y+from.H/2
	tx, ty := to.X+to.W/2, to.Y+to.H/2
	tt.Press(x, y)
	for step := 1; step <= 4; step++ {
		fraction := float32(step) / 4
		tt.Move(x+(tx-x)*fraction, y+(ty-y)*fraction)
	}
	if got := tt.Cursor(); got != ui.CursorNotAllowed {
		t.Fatalf("悬停在非法目录目标上时指针样式 = %v, want ui.CursorNotAllowed", got)
	}
	tt.Release(tx, ty)
	tt.Frame()

	if calls := renameOpCalls(fake); len(calls) != 0 {
		t.Fatalf("非法目标不应调用 FilesRename，得到 %v", calls)
	}
	waitFor(t, tt, func() bool { return tt.HasText("不可移入自身或子目录") })
}

func TestFileTreeDragHoverExpands(t *testing.T) {
	setAppReduceMotion(t, false)
	tree := map[string][]FileEntry{
		"": {
			{Name: "a.txt", Path: "a.txt", Size: 5},
			{Name: "sub", Path: "sub", IsDir: true},
		},
		"sub": {
			{Name: "b.go", Path: "sub/b.go", Size: 20},
		},
	}
	fake := treeTestBase()
	client := newMoveOpsTestClient(fake, tree)
	s, tt := openFileTreeForTest(t, client)
	t.Cleanup(s.close)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") && tt.HasText("sub") })
	if tt.HasText("b.go") {
		t.Fatal("初始折叠目录下不应提前显示 b.go")
	}

	from := mustFindRect(t, tt, "a.txt")
	to := mustFindRect(t, tt, "sub")
	x, y := from.X+from.W/2, from.Y+from.H/2
	tx, ty := to.X+to.W/2, to.Y+to.H/2
	tt.Press(x, y)
	tt.Move(x, y+6)
	tt.Move(tx, ty)

	tt.Advance(200 * time.Millisecond)
	if tt.HasText("b.go") {
		t.Fatal("悬停未满 600ms 时不应展开目录")
	}

	tt.Advance(450 * time.Millisecond)
	waitFor(t, tt, func() bool { return tt.HasText("b.go") })

	tt.Release(tx, ty)
	waitFor(t, tt, func() bool { return len(renameOpCalls(fake)) == 1 })
	waitFor(t, tt, func() bool {
		st := getFileManagerState(s)
		fileManagerMu.Lock()
		defer fileManagerMu.Unlock()
		return slices.Contains(st.tree.children["sub"], "sub/a.txt")
	})

	tt.Advance(DurSheet + time.Second)
	if tt.FramePending() {
		t.Fatal("拖动及展开动画结束后不应继续请求下一帧")
	}
}

func TestFileTreeDragPartialFailureShowsToast(t *testing.T) {
	tree := map[string][]FileEntry{
		"": {
			{Name: "a.txt", Path: "a.txt", Size: 5},
			{Name: "c.txt", Path: "c.txt", Size: 6},
			{Name: "sub", Path: "sub", IsDir: true},
		},
		"sub": {
			{Name: "b.go", Path: "sub/b.go", Size: 20},
		},
	}
	fake := treeTestBase()
	client := newMoveOpsTestClient(fake, tree)
	client.failPath = "c.txt"
	client.failErr = errors.New("permission denied")
	s, tt := openFileTreeForTest(t, client)
	t.Cleanup(s.close)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") && tt.HasText("c.txt") })

	st := getFileManagerState(s)
	fileManagerMu.Lock()
	st.tree.selection.Clear()
	st.tree.selection.Add("a.txt")
	st.tree.selection.Add("c.txt")
	fileManagerMu.Unlock()
	tt.Frame()

	dragElementToElement(tt, mustFindRect(t, tt, "a.txt"), mustFindRect(t, tt, "sub"))

	waitFor(t, tt, func() bool {
		return tt.HasText("已移动 1 项，c.txt 失败：permission denied")
	})
}
