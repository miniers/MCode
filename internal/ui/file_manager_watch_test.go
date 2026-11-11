package mcodeui

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// watchTestClient 在 FakeClient 上补工作区代次与可控的 Git 改动列表。
type watchTestClient struct {
	*FakeClient

	mu         sync.Mutex
	generation map[string]uint64
	genCalls   map[string]int
	changed    []GitChangedFile
	genErr     error

	// block 与 entered 用于模拟在途检查；ctxDone 在请求因取消返回时关闭。
	block   chan struct{}
	entered chan struct{}
	ctxDone chan struct{}
}

func newWatchTestClient(fake *FakeClient) *watchTestClient {
	return &watchTestClient{
		FakeClient: fake,
		generation: make(map[string]uint64),
		genCalls:   make(map[string]int),
		entered:    make(chan struct{}, 8),
		ctxDone:    make(chan struct{}),
	}
}

func (f *watchTestClient) WorkspaceGeneration(ctx context.Context, threadID string) (uint64, error) {
	f.mu.Lock()
	gen := f.generation[threadID]
	if gen == 0 {
		gen = 1
	}
	f.genCalls[threadID]++
	block, entered := f.block, f.entered
	f.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			f.mu.Lock()
			select {
			case <-f.ctxDone:
			default:
				close(f.ctxDone)
			}
			f.mu.Unlock()
			return 0, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.genErr != nil {
		return 0, f.genErr
	}
	return gen, nil
}

func (f *watchTestClient) GitChangedFiles(ctx context.Context, threadID string) ([]GitChangedFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]GitChangedFile(nil), f.changed...), nil
}

func (f *watchTestClient) setGeneration(threadID string, generation uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.generation[threadID] = generation
}

func (f *watchTestClient) generationCalls(threadID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.genCalls[threadID]
}

func (f *watchTestClient) setChanged(files []GitChangedFile) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changed = append([]GitChangedFile(nil), files...)
}

func (f *watchTestClient) startBlocking() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.block = make(chan struct{})
}

func (f *watchTestClient) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-f.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("WorkspaceGeneration 未开始")
	}
}

func (f *watchTestClient) resetStarted() {
	for {
		select {
		case <-f.entered:
		default:
			return
		}
	}
}

func (f *watchTestClient) waitCanceled(t *testing.T) {
	t.Helper()
	select {
	case <-f.ctxDone:
	case <-time.After(5 * time.Second):
		t.Fatal("在途 WorkspaceGeneration 未被取消")
	}
}

func openFileTreeWatchForTest(t *testing.T, client Client) (*chatState, *ui.Tester) {
	t.Helper()
	s, tt := openFileTreeForTest(t, client)
	t.Cleanup(func() {
		st := getFileManagerState(s)
		fileTreeWatchDispose(st)
		fileManagerMu.Lock()
		delete(fileManagerStates, s)
		fileManagerMu.Unlock()
	})
	return s, tt
}

func fileWatchStateForTest(t *testing.T, st *fileManagerState) *fileTreeWatchState {
	t.Helper()
	fileTreeWatchMu.Lock()
	defer fileTreeWatchMu.Unlock()
	w := fileTreeWatches[st]
	if w == nil {
		t.Fatal("文件面板缺少监听状态")
	}
	return w
}

func treeSelectionForTest(s *chatState, path string) bool {
	st := getFileManagerState(s)
	if st == nil {
		return false
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	return st.tree != nil && st.tree.selection.Has(path)
}

func treeOpenDirForTest(s *chatState, dir string) bool {
	st := getFileManagerState(s)
	if st == nil {
		return false
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	return st.tree != nil && st.tree.outline.Open.Has(dir)
}

func treeHasChildForTest(s *chatState, dir, child string) bool {
	st := getFileManagerState(s)
	if st == nil {
		return false
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	if st.tree == nil {
		return false
	}
	for _, path := range st.tree.children[dir] {
		if path == child {
			return true
		}
	}
	return false
}

// 监听刷新目录与内容时保留树的选中项与展开状态，连续保存不重建整棵树。
func TestFileTreeWatcherRefreshPreservesSelection(t *testing.T) {
	client := newWatchTestClient(treeTestBase())
	s, tt := openFileTreeWatchForTest(t, client)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	if err := tt.Click("sub"); err != nil {
		t.Fatalf("click sub failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("b.go") })
	if err := tt.Click("b.go"); err != nil {
		t.Fatalf("click b.go failed: %v", err)
	}
	waitForFileManagerText(t, tt, s, "sub/b.go", "world")

	nextTree := fileManagerTestTree()
	nextTree["sub"] = append(nextTree["sub"], FileEntry{Name: "c.go", Path: "sub/c.go", Size: 10})
	client.SetFileTree(nextTree)
	client.SetFileContents(map[string]FileContent{
		"a.txt":    {Path: "a.txt", Kind: "text", Text: "hello", TotalSize: 5},
		"sub/b.go": {Path: "sub/b.go", Kind: "text", Text: "world2", TotalSize: 6},
		"sub/c.go": {Path: "sub/c.go", Kind: "text", Text: "new", TotalSize: 3},
	})
	client.setGeneration("t1", 2)

	waitFor(t, tt, func() bool { return treeHasChildForTest(s, "sub", "sub/c.go") })
	waitForFileManagerText(t, tt, s, "sub/b.go", "world2")
	if !treeSelectionForTest(s, "sub/b.go") {
		t.Fatal("刷新后选中项丢失")
	}
	if !treeOpenDirForTest(s, "sub") {
		t.Fatal("刷新后展开状态丢失")
	}
	_, _, active := fileManagerSnapshot(s)
	if active != "sub/b.go" {
		t.Fatalf("刷新不应切换活动标签，得到 %q", active)
	}
}

// 右栏隐藏后不再发起检查，重新显示时立即补检。
func TestFileTreeWatcherStopsWhenHidden(t *testing.T) {
	client := newWatchTestClient(treeTestBase())
	s, tt := openFileTreeWatchForTest(t, client)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })
	waitFor(t, tt, func() bool { return client.generationCalls("t1") >= 1 })

	inspectorClose(s)
	tt.Frame()
	hiddenCalls := client.generationCalls("t1")
	deadline := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(10 * time.Millisecond)
	}
	if got := client.generationCalls("t1"); got != hiddenCalls {
		t.Fatalf("隐藏后仍在检查：%d -> %d", hiddenCalls, got)
	}

	inspectorSelect(s, sidePaneFileID, false)
	waitFor(t, tt, func() bool { return client.generationCalls("t1") > hiddenCalls })
}

// 只看改动保留必要父目录，数据变化时默认全部展开。
func TestFileTreeOnlyChangedKeepsParents(t *testing.T) {
	client := newWatchTestClient(treeTestBase())
	client.setChanged([]GitChangedFile{
		{Path: "a.txt", Status: "M"},
		{Path: "sub/b.go", Status: "A"},
		{Path: "sub/deep/c.go", Status: "?"},
	})
	s, tt := openFileTreeWatchForTest(t, client)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	if err := tt.Click("只看改动"); err != nil {
		t.Fatalf("click only changed failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("c.go") })
	for _, want := range []string{"sub", "deep", "b.go", "c.go"} {
		if !tt.HasText(want) {
			t.Fatalf("只看改动缺少 %q，当前文本：%q", want, tt.Texts())
		}
	}

	st := getFileManagerState(s)
	fileManagerMu.Lock()
	roots, children, _ := fileTreeChangedRowsLocked(st.tree)
	openSub := st.tree.changedOutline.Open.Has("sub")
	openDeep := st.tree.changedOutline.Open.Has("sub/deep")
	fileManagerMu.Unlock()
	if len(roots) != 2 || roots[0] != "sub" || roots[1] != "a.txt" {
		t.Fatalf("只看改动根条目 = %v", roots)
	}
	if got := children["sub"]; len(got) != 2 || got[0] != "sub/deep" || got[1] != "sub/b.go" {
		t.Fatalf("sub 子项 = %v", got)
	}
	if !openSub || !openDeep {
		t.Fatalf("只看改动应默认展开父目录，sub=%v deep=%v", openSub, openDeep)
	}

	if err := tt.Click("只看改动"); err != nil {
		t.Fatalf("toggle off failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("secret.env") })
}

// 重命名同时保留来源路径，删除文件渲染为不可打开的灰色占位行。
func TestFileTreeRenameAndDeleteStatus(t *testing.T) {
	if got := fileTreeStatusLabel("R"); got != "已重命名" {
		t.Fatalf("R 状态名称 = %q", got)
	}
	if got := fileTreeAccessibilityLabel(GitChangedFile{Path: "new.txt", OrigPath: "old.txt", Status: "R"}); got != "已重命名，原路径 old.txt" {
		t.Fatalf("重命名无障碍名称 = %q", got)
	}
	if got := fileTreeStatusLabel("D"); got != "已删除" {
		t.Fatalf("D 状态名称 = %q", got)
	}

	client := newWatchTestClient(treeTestBase())
	client.setChanged([]GitChangedFile{
		{Path: "a.txt", Status: "M"},
		{Path: "added.md", Status: "A"},
		{Path: "gone.txt", Status: "D"},
		{Path: "renamed.txt", OrigPath: "old.txt", Status: "R"},
		{Path: "tmp.log", Status: "?"},
	})
	s, tt := openFileTreeWatchForTest(t, client)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	if err := tt.Click("只看改动"); err != nil {
		t.Fatalf("click only changed failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("gone.txt") })
	if err := tt.Click("gone.txt"); err != nil {
		t.Fatalf("click deleted file failed: %v", err)
	}
	tt.Frame()
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	snap := fileManagerSnapshotLocked(st)
	deletedTab := st.findTabLocked("gone.txt")
	fileManagerMu.Unlock()
	if snap.active == "gone.txt" || deletedTab != nil {
		t.Fatal("已删除文件不应能打开")
	}
}

// 会话释放后监听状态被移除，在途请求被取消，计时器清零。
func TestFileTreeWatcherDisposeCancels(t *testing.T) {
	client := newWatchTestClient(treeTestBase())
	s, tt := openFileTreeWatchForTest(t, client)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })
	waitFor(t, tt, func() bool { return client.generationCalls("t1") >= 1 })

	st := getFileManagerState(s)
	w := fileWatchStateForTest(t, st)
	waitFor(t, tt, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return !w.inFlight && w.timer != nil
	})

	client.startBlocking()
	client.setGeneration("t1", 2)
	client.resetStarted()
	w.mu.Lock()
	w.stopTimerLocked()
	w.lastCheck = time.Time{}
	w.mu.Unlock()
	tt.Frame()
	client.waitStarted(t)

	inspectorDisposeSession(s, inspectorSession(s))
	client.waitCanceled(t)

	fileTreeWatchMu.Lock()
	_, exists := fileTreeWatches[st]
	fileTreeWatchMu.Unlock()
	if exists {
		t.Fatal("会话释放后监听状态仍留在表里")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.disposed || w.visible || w.timer != nil || w.cancel != nil || w.inFlight {
		t.Fatalf("释放后监听状态未清理：disposed=%v visible=%v timer=%v cancel=%v inFlight=%v",
			w.disposed, w.visible, w.timer != nil, w.cancel != nil, w.inFlight)
	}
}
