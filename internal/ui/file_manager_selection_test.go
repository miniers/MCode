package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type selectionOpsTestClient struct {
	*FakeClient

	mu       sync.Mutex
	tree     map[string][]FileEntry
	failPath string
	failErr  error
}

func newSelectionOpsTestClient(fake *FakeClient, tree map[string][]FileEntry) *selectionOpsTestClient {
	copied := make(map[string][]FileEntry, len(tree))
	for dir, entries := range tree {
		list := make([]FileEntry, len(entries))
		copy(list, entries)
		copied[dir] = list
	}
	fake.SetFileTree(copied)
	return &selectionOpsTestClient{
		FakeClient: fake,
		tree:       copied,
	}
}

func (c *selectionOpsTestClient) FilesList(ctx context.Context, threadID, subpath string) (FileListing, error) {
	c.mu.Lock()
	entries := append([]FileEntry(nil), c.tree[subpath]...)
	c.mu.Unlock()
	return FileListing{Path: subpath, Entries: entries}, nil
}

func (c *selectionOpsTestClient) FilesRename(ctx context.Context, threadID, oldPath, newPath string) (string, error) {
	res, err := c.FakeClient.FilesRename(ctx, threadID, oldPath, newPath)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	parent := fileManagerParentPath(oldPath)
	newParent := fileManagerParentPath(newPath)
	list := c.tree[parent]
	for i := range list {
		if list[i].Path == oldPath {
			list[i].Path = newPath
			list[i].Name = fileManagerTabName(newPath)
			break
		}
	}
	c.tree[parent] = list
	if parent != newParent {
		c.tree[newParent] = append(c.tree[newParent], FileEntry{Name: fileManagerTabName(newPath), Path: newPath})
	}
	c.mu.Unlock()
	return res, nil
}

func (c *selectionOpsTestClient) FilesTrash(ctx context.Context, threadID, path string) error {
	c.mu.Lock()
	failPath, failErr := c.failPath, c.failErr
	c.mu.Unlock()
	if failErr != nil && path == failPath {
		return failErr
	}
	if err := c.FakeClient.FilesTrash(ctx, threadID, path); err != nil {
		return err
	}
	c.mu.Lock()
	parent := fileManagerParentPath(path)
	filtered := make([]FileEntry, 0, len(c.tree[parent]))
	for _, entry := range c.tree[parent] {
		if entry.Path != path {
			filtered = append(filtered, entry)
		}
	}
	c.tree[parent] = filtered
	delete(c.tree, path)
	c.mu.Unlock()
	return nil
}

func openWideFileTreeForSelectionTest(t *testing.T, client Client) (*chatState, *ui.Tester) {
	t.Helper()
	s, tt := openFileTreeForTest(t, client)
	inspectorSession(s).Width = 800
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	st.treeNavOpen = true
	fileManagerMu.Unlock()
	s.invalidate()
	tt.Advance(DurSheet + 50*time.Millisecond)
	return s, tt
}

func TestFileTreeSelectionBatchMenu(t *testing.T) {
	tree := map[string][]FileEntry{
		"": {
			{Name: "a.txt", Path: "a.txt", Size: 5},
			{Name: "b.txt", Path: "b.txt", Size: 6},
			{Name: "c.txt", Path: "c.txt", Size: 7},
			{Name: "secret.env", Path: "secret.env", Size: 12, Protected: true},
		},
	}
	workDir := filepath.Join(t.TempDir(), "workspace")
	fake := treeTestBase()
	fake.SetThreadWorkDir(workDir)
	fake.SetFileTree(tree)
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "alpha", TotalSize: 5},
		"b.txt": {Path: "b.txt", Kind: "text", Text: "beta", TotalSize: 4},
		"c.txt": {Path: "c.txt", Kind: "text", Text: "gamma", TotalSize: 5},
	})

	s, tt := openWideFileTreeForSelectionTest(t, newTreeListRecorder(fake, tree))
	waitFor(t, tt, func() bool {
		return tt.HasText("a.txt") && tt.HasText("b.txt") && tt.HasText("c.txt")
	})

	if err := tt.Click("a.txt"); err != nil {
		t.Fatalf("click a.txt failed: %v", err)
	}
	waitForFileManagerText(t, tt, s, "a.txt", "alpha")
	if err := tt.ClickWith(ui.Cmd, "b.txt"); err != nil {
		t.Fatalf("cmd click b.txt failed: %v", err)
	}
	tt.Frame()

	// 右键选区内条目 -> 批量菜单，「复制路径」写入换行分隔的多条路径。
	if err := tt.RightClick("a.txt"); err != nil {
		t.Fatalf("right click a.txt failed: %v", err)
	}
	wantBatch := []string{
		"打开", "重命名…",
		"-", "引用到输入框", "复制路径", "复制相对路径",
		"-", trashActionLabel() + "…",
	}
	if got := tt.Menu(); !slices.Equal(got, wantBatch) {
		t.Fatalf("批量右键菜单 = %v，期望 %v", got, wantBatch)
	}
	if err := tt.ChooseMenuItem("复制路径"); err != nil {
		t.Fatalf("choose copy paths failed: %v", err)
	}
	wantClipboard := filepath.Join(workDir, "a.txt") + "\n" + filepath.Join(workDir, "b.txt")
	waitFor(t, tt, func() bool { return tt.Clipboard() == wantClipboard })

	// 右键选区外条目 -> 单条菜单。
	if err := tt.RightClick("c.txt"); err != nil {
		t.Fatalf("right click c.txt failed: %v", err)
	}
	gotSingle := tt.Menu()
	tt.CloseMenu()
	if !slices.Contains(gotSingle, "在右侧分栏打开") || !slices.Contains(gotSingle, "复制一份") {
		t.Fatalf("右键选区外条目应显示单条菜单，得到 %v", gotSingle)
	}
}

func TestFileTreeSelectionBatchTrash(t *testing.T) {
	tree := map[string][]FileEntry{
		"": {
			{Name: "sub", Path: "sub", IsDir: true},
			{Name: "a.txt", Path: "a.txt", Size: 5},
			{Name: "b.txt", Path: "b.txt", Size: 6},
			{Name: "c.txt", Path: "c.txt", Size: 7},
		},
		"sub": {
			{Name: "d.go", Path: "sub/d.go", Size: 8},
		},
	}
	fake := treeTestBase()
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "alpha", TotalSize: 5},
		"b.txt": {Path: "b.txt", Kind: "text", Text: "beta", TotalSize: 4},
		"c.txt": {Path: "c.txt", Kind: "text", Text: "gamma", TotalSize: 5},
	})
	client := newSelectionOpsTestClient(fake, tree)
	s, tt := openWideFileTreeForSelectionTest(t, client)
	waitFor(t, tt, func() bool {
		return tt.HasText("sub") && tt.HasText("a.txt") && tt.HasText("b.txt")
	})

	if err := tt.Click("sub"); err != nil {
		t.Fatalf("expand sub failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("d.go") })
	tt.Advance(motionUnfold + time.Second)

	// 选中目录 sub、其内部文件 sub/d.go 和根目录文件 a.txt。
	if err := tt.ClickWith(ui.Cmd, "d.go"); err != nil {
		t.Fatalf("cmd click d.go failed: %v", err)
	}
	if err := tt.ClickWith(ui.Cmd, "a.txt"); err != nil {
		t.Fatalf("cmd click a.txt failed: %v", err)
	}
	tt.Frame()

	if err := tt.Click(trashActionLabel()); err != nil {
		t.Fatalf("click trash button on selection bar failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return fileManagerOpsOpen(s) && tt.HasText(fmt.Sprintf("把 2 项%s？", trashActionLabel()))
	})
	if err := tt.Click("确认" + trashActionLabel()); err != nil {
		t.Fatalf("confirm batch trash failed: %v", err)
	}

	waitFor(t, tt, func() bool { return !fileManagerOpsOpen(s) })
	var trashed []string
	for _, call := range fake.FileOpCalls() {
		if call.Op == "trash" {
			trashed = append(trashed, call.Path)
		}
	}
	if !slices.Equal(trashed, []string{"sub", "a.txt"}) {
		t.Fatalf("同时选中目录与子文件时应只删除目录和其余条目，实际调用 %v", trashed)
	}

	// 中途失败时停止，显示失败项与已删除数，并更新已成功的条目。
	waitFor(t, tt, func() bool { return tt.HasText("b.txt") && tt.HasText("c.txt") })
	client.mu.Lock()
	client.failPath = "c.txt"
	client.failErr = errors.New("权限不足")
	client.mu.Unlock()

	if err := tt.Click("b.txt"); err != nil {
		t.Fatalf("click b.txt failed: %v", err)
	}
	waitForFileManagerText(t, tt, s, "b.txt", "beta")
	if err := tt.ClickWith(ui.Cmd, "c.txt"); err != nil {
		t.Fatalf("cmd click c.txt failed: %v", err)
	}
	tt.Frame()

	if err := tt.Click(trashActionLabel()); err != nil {
		t.Fatalf("click trash button failed: %v", err)
	}
	waitFor(t, tt, func() bool { return fileManagerOpsOpen(s) })
	if err := tt.Click("确认" + trashActionLabel()); err != nil {
		t.Fatalf("confirm partial fail trash failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("已删除 1 项，c.txt 失败：权限不足")
	})
	if !fileManagerOpsOpen(s) {
		t.Fatal("中途失败后弹窗应保持打开")
	}
	waitFor(t, tt, func() bool { return !tt.HasText("b.txt") })
	if !tt.HasText("c.txt") {
		t.Fatal("失败项 c.txt 应仍在树中")
	}
	if calls := fake.FileOpCalls(); len(calls) != 3 {
		t.Fatalf("第二次批量删除只应成功调用 b.txt 一次（共 3 次），得到 %v", calls)
	}
}

func TestFileTreeSelectionBar(t *testing.T) {
	setAppReduceMotion(t, false)
	tree := map[string][]FileEntry{
		"": {
			{Name: "a.txt", Path: "a.txt", Size: 5},
			{Name: "b.txt", Path: "b.txt", Size: 15},
			{Name: "sub", Path: "sub", IsDir: true},
		},
	}
	fake := treeTestBase()
	fake.SetFileTree(tree)
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "alpha", TotalSize: 5},
		"b.txt": {Path: "b.txt", Kind: "text", Text: "beta", TotalSize: 4},
	})
	s, tt := openWideFileTreeForSelectionTest(t, newTreeListRecorder(fake, tree))
	waitFor(t, tt, func() bool {
		return tt.HasText("a.txt") && tt.HasText("b.txt") && tt.HasText("sub")
	})

	// 单选时不显示选区条。
	if err := tt.Click("a.txt"); err != nil {
		t.Fatalf("click a.txt failed: %v", err)
	}
	waitForFileManagerText(t, tt, s, "a.txt", "alpha")
	tt.Advance(DurBase + time.Second)
	if _, ok := tt.Find("清空"); ok {
		t.Fatal("单选时不应显示底部选区条")
	}

	// 多选 2 个文件时显示「2 项 · <总大小>」。
	if err := tt.ClickWith(ui.Cmd, "b.txt"); err != nil {
		t.Fatalf("cmd click b.txt failed: %v", err)
	}
	wantSummary := "2 项 · " + formatFileSize(20)
	waitFor(t, tt, func() bool { return tt.HasText(wantSummary) })
	tt.Advance(DurBase + 50*time.Millisecond)
	if tt.FramePending() {
		t.Fatal("选区条入场动画结束后不应继续请求下一帧")
	}

	// 选区包含目录时只显示「N 项」，不累加大小。
	if err := tt.ClickWith(ui.Cmd, "sub"); err != nil {
		t.Fatalf("cmd click sub failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("3 项") && !tt.HasText(wantSummary) })

	// 点击「清空」收回到单选焦点行，选区条消失并在动画结束后停止重绘。
	if err := tt.Click("清空"); err != nil {
		t.Fatalf("click clear selection failed: %v", err)
	}
	tt.Advance(DurBase + 50*time.Millisecond)
	if _, ok := tt.Find("清空"); ok {
		t.Fatal("清空选区后选区条应消失")
	}
	if tt.FramePending() {
		t.Fatal("选区条退场动画结束后不应继续请求下一帧")
	}
	sel := treeSelectionSnapshotForTest(s)
	if len(sel) != 1 || !sel["sub"] {
		t.Fatalf("清空后应只剩当前焦点行 sub，得到 %v", sel)
	}
}

func TestFileTreeSelectionShortcuts(t *testing.T) {
	tree := map[string][]FileEntry{
		"": {
			{Name: "a.txt", Path: "a.txt", Size: 5},
			{Name: "b.txt", Path: "b.txt", Size: 6},
			{Name: "secret.env", Path: "secret.env", Size: 12, Protected: true},
		},
	}
	fake := treeTestBase()
	fake.SetFileTree(tree)
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "alpha", TotalSize: 5},
		"b.txt": {Path: "b.txt", Kind: "text", Text: "beta", TotalSize: 4},
	})
	s, tt := openWideFileTreeForSelectionTest(t, newTreeListRecorder(fake, tree))
	waitFor(t, tt, func() bool {
		return tt.HasText("a.txt") && tt.HasText("b.txt") && tt.HasText("secret.env")
	})

	// 点击 a.txt 让树获得焦点。
	if err := tt.Click("a.txt"); err != nil {
		t.Fatalf("click a.txt failed: %v", err)
	}
	waitForFileManagerText(t, tt, s, "a.txt", "alpha")
	tt.Advance(motionUnfold + time.Second)

	// ⌘A 全选当前可见非受保护行。
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Frame()
	sel := treeSelectionSnapshotForTest(s)
	if len(sel) != 2 || !sel["a.txt"] || !sel["b.txt"] || sel["secret.env"] {
		t.Fatalf("⌘A 应全选可见非受保护行 a.txt 与 b.txt，得到 %v", sel)
	}

	// 搜索框聚焦时 ⌘A 不能抢走搜索框按键。
	if err := tt.Click("搜索文件"); err != nil {
		t.Fatalf("click search input failed: %v", err)
	}
	tt.Type("a")
	tt.Frame()
	if err := tt.Click("清除搜索"); err != nil {
		t.Fatalf("click clear search failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("b.txt") })

	// 重新点击 a.txt 并全选，再按 Esc 收回到只剩当前焦点行。
	if err := tt.Click("a.txt"); err != nil {
		t.Fatalf("click a.txt failed: %v", err)
	}
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Frame()
	if sel := treeSelectionSnapshotForTest(s); len(sel) != 2 {
		t.Fatalf("⌘A 后应选中 2 项，得到 %v", sel)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	sel = treeSelectionSnapshotForTest(s)
	if len(sel) != 1 || !sel["a.txt"] {
		t.Fatalf("Esc 后应只剩当前焦点行 a.txt，得到 %v", sel)
	}

	// ⌘⌫ 打开废纸篓确认弹窗。
	tt.Key(ui.Cmd, ui.KeyBackspace)
	waitFor(t, tt, func() bool {
		return fileManagerOpsOpen(s) && tt.HasText("确定把 a.txt "+trashActionLabel()+"吗？")
	})
}

func TestFileTreeInlineRename(t *testing.T) {
	tree := map[string][]FileEntry{
		"": {
			{Name: "a.txt", Path: "a.txt", Size: 5},
			{Name: "b.txt", Path: "b.txt", Size: 6},
		},
	}
	fake := treeTestBase()
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "alpha", TotalSize: 5},
		"b.txt": {Path: "b.txt", Kind: "text", Text: "beta", TotalSize: 4},
	})
	client := newSelectionOpsTestClient(fake, tree)
	s, tt := openWideFileTreeForSelectionTest(t, client)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") && tt.HasText("b.txt") })

	if err := tt.Click("a.txt"); err != nil {
		t.Fatalf("click a.txt failed: %v", err)
	}
	waitForFileManagerText(t, tt, s, "a.txt", "alpha")
	tt.Advance(motionUnfold + time.Second)

	// F2 进入行内重命名。
	tt.Key(0, ui.KeyF2)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("重命名")
		return ok
	})

	// 输入同目录已有文件名，显示行内错误且不调用 FilesRename。
	tt.Command("selectAll")
	tt.Type("b.txt")
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool { return tt.HasText("当前目录已存在同名文件或文件夹") })
	if _, ok := tt.Find("重命名"); !ok {
		t.Fatal("校验失败时重命名输入框应保持打开")
	}
	if calls := fake.FileOpCalls(); len(calls) != 0 {
		t.Fatalf("重名校验失败不应调用 FilesRename，得到 %v", calls)
	}

	// 输入合法新名按 Enter，调用 FilesRename 且树里显示新名。
	tt.Command("selectAll")
	tt.Type("renamed.txt")
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		calls := fake.FileOpCalls()
		return len(calls) == 1 && calls[0].Op == "rename" && calls[0].Path == "a.txt" && calls[0].NewPath == "renamed.txt"
	})
	waitFor(t, tt, func() bool { return tt.HasText("renamed.txt") })
	if _, ok := tt.Find("重命名"); ok {
		t.Fatal("重命名成功后输入框应关闭")
	}

	// 对 b.txt 按 F2，修改后按 Esc 取消，不调用 FilesRename。
	if err := tt.Click("b.txt"); err != nil {
		t.Fatalf("click b.txt failed: %v", err)
	}
	waitForFileManagerText(t, tt, s, "b.txt", "beta")
	tt.Key(0, ui.KeyF2)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("重命名")
		return ok
	})
	tt.Command("selectAll")
	tt.Type("cancelled.txt")
	tt.Key(0, ui.KeyEscape)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("重命名")
		return !ok
	})
	if !tt.HasText("b.txt") || strings.Contains(strings.Join(tt.Texts(), "\n"), "cancelled.txt") {
		t.Fatalf("Esc 取消后应恢复原名 b.txt，当前文本：%v", tt.Texts())
	}
	if calls := fake.FileOpCalls(); len(calls) != 1 {
		t.Fatalf("Esc 取消不应调用 FilesRename，得到 %v", calls)
	}
}
