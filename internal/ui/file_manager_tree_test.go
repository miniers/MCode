package mcodeui

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

type treeListCall struct {
	threadID string
	dir      string
}

// treeListRecorder 记录目录请求，并可按目录拦住回包与替换下一次的目录内容。
type treeListRecorder struct {
	*FakeClient

	mu       sync.Mutex
	calls    []treeListCall
	gates    map[string]chan chan struct{}
	results  map[string][][]FileEntry
	truncate map[string]bool
}

func newTreeListRecorder(fake *FakeClient, tree map[string][]FileEntry) *treeListRecorder {
	r := &treeListRecorder{
		FakeClient: fake,
		gates:      make(map[string]chan chan struct{}),
		results:    make(map[string][][]FileEntry),
		truncate:   make(map[string]bool),
	}
	for dir, entries := range tree {
		r.results[dir] = [][]FileEntry{entries}
	}
	return r
}

func (f *treeListRecorder) FilesList(ctx context.Context, threadID, subpath string) (FileListing, error) {
	f.mu.Lock()
	index := 0
	for _, call := range f.calls {
		if call.dir == subpath {
			index++
		}
	}
	f.calls = append(f.calls, treeListCall{threadID: threadID, dir: subpath})
	var gate chan struct{}
	if ch := f.gates[subpath]; ch != nil {
		gate = make(chan struct{})
		ch <- gate
	}
	f.mu.Unlock()

	if gate != nil {
		<-gate
	}

	f.mu.Lock()
	sequences := f.results[subpath]
	var result []FileEntry
	if len(sequences) > 0 {
		if index >= len(sequences) {
			index = len(sequences) - 1
		}
		result = append([]FileEntry(nil), sequences[index]...)
	}
	truncated := f.truncate[subpath]
	f.mu.Unlock()
	return FileListing{Path: subpath, Entries: result, Truncated: truncated}, nil
}

func (f *treeListRecorder) callsFor(dir string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		if call.dir == dir {
			n++
		}
	}
	return n
}

func (f *treeListRecorder) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *treeListRecorder) resetCalls() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

func (f *treeListRecorder) setResults(dir string, results ...[]FileEntry) {
	f.mu.Lock()
	f.results[dir] = results
	f.mu.Unlock()
}

func waitTreeGate(t *testing.T, ch chan chan struct{}) chan struct{} {
	t.Helper()
	select {
	case gate := <-ch:
		return gate
	case <-time.After(5 * time.Second):
		t.Fatal("目录请求未开始")
		return nil
	}
}

func treeTestBase() *FakeClient {
	fake := NewFakeClient()
	fake.SetThreadWorkDir("/tmp/mcode-file-tree-test")
	fake.SetFileTree(fileManagerTestTree())
	fake.SetFileContents(map[string]FileContent{
		"a.txt":    {Path: "a.txt", Kind: "text", Text: "hello", TotalSize: 5},
		"sub/b.go": {Path: "sub/b.go", Kind: "text", Text: "world", TotalSize: 5},
	})
	return fake
}

func openFileTreeForTest(t *testing.T, client Client) (*chatState, *ui.Tester) {
	t.Helper()
	testenv.TempHome(t)
	s, root := buildRoot(client)
	tt := ui.NewTester(root, 1400, 800)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	tt.Key(ui.Cmd|ui.Shift, ui.KeyF)
	waitFor(t, tt, func() bool { return isFileManagerOpen(s) })
	tt.Advance(DurSheet + 50*time.Millisecond)
	return s, tt
}

func treeFirstVisibleForTest(s *chatState) string {
	st := getFileManagerState(s)
	if st == nil {
		return ""
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	if st.tree == nil || st.tree.outline.Rows() == 0 {
		return ""
	}
	first, _ := st.tree.outline.List.Visible()
	if first < 0 || first >= st.tree.outline.Rows() {
		return ""
	}
	return st.tree.outline.Item(first)
}

func treeOpenForTest(s *chatState) map[string]bool {
	st := getFileManagerState(s)
	result := make(map[string]bool)
	if st == nil {
		return result
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	if st.tree == nil {
		return result
	}
	for path := range st.tree.outline.Open.All() {
		result[path] = true
	}
	return result
}

func TestFileTreeLazyExpansion(t *testing.T) {
	fake := treeTestBase()
	client := newTreeListRecorder(fake, fileManagerTestTree())
	s, tt := openFileTreeForTest(t, client)
	waitFor(t, tt, func() bool { return tt.HasText("sub") })

	if got := client.callsFor("sub"); got != 0 {
		t.Fatalf("未展开时不应请求子目录，得到 %d 次", got)
	}
	if err := tt.Click("sub"); err != nil {
		t.Fatalf("click sub failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("b.go") })
	if got := client.callsFor("sub"); got != 1 {
		t.Fatalf("展开子目录应请求一次，得到 %d 次", got)
	}

	// 收起再展开使用缓存，不再请求。
	if err := tt.Click("sub"); err != nil {
		t.Fatalf("collapse sub failed: %v", err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("b.go") })
	if err := tt.Click("sub"); err != nil {
		t.Fatalf("expand sub again failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("b.go") })
	if got := client.callsFor("sub"); got != 1 {
		t.Fatalf("重复展开不应重复请求，得到 %d 次", got)
	}
	_ = s
}

func TestFileTreeDropsStaleDirectoryResponse(t *testing.T) {
	fake := treeTestBase()
	client := newTreeListRecorder(fake, fileManagerTestTree())
	client.gates["sub"] = make(chan chan struct{}, 4)
	client.setResults("sub", []FileEntry{{Name: "old.go", Path: "sub/old.go"}}, []FileEntry{{Name: "new.go", Path: "sub/new.go"}})
	s, tt := openFileTreeForTest(t, client)
	waitFor(t, tt, func() bool { return tt.HasText("sub") })

	if err := tt.Click("sub"); err != nil {
		t.Fatal(err)
	}
	first := waitTreeGate(t, client.gates["sub"])
	threadID := fileManagerThreadID(s)
	fileTreeInvalidate(s, threadID, []string{"sub"})
	second := waitTreeGate(t, client.gates["sub"])
	close(first)
	close(second)

	waitFor(t, tt, func() bool { return tt.HasText("new.go") })
	if tt.HasText("old.go") {
		t.Fatal("旧代次的目录回包污染了缓存")
	}
	if got := client.callsFor("sub"); got != 2 {
		t.Fatalf("过期重载应请求两次，得到 %d 次", got)
	}
}

func TestFileTreeVisibleRowsBounded(t *testing.T) {
	const total = 10000
	entries := make([]FileEntry, 0, total)
	for i := 0; i < total; i++ {
		name := fmt.Sprintf("f%05d", i)
		entries = append(entries, FileEntry{Name: name, Path: name, Size: 1})
	}
	fake := treeTestBase()
	fake.SetFileTree(map[string][]FileEntry{"": entries})
	client := newTreeListRecorder(fake, map[string][]FileEntry{"": entries})
	s, tt := openFileTreeForTest(t, client)
	waitFor(t, tt, func() bool { return tt.HasText("f00000") })

	st := getFileManagerState(s)
	fileManagerMu.Lock()
	tree := ensureFileTreeLocked(st)
	rows := tree.outline.Rows()
	fileManagerMu.Unlock()
	if rows != total {
		t.Fatalf("树应有 %d 行，得到 %d", total, rows)
	}

	client.resetCalls()
	tt.Frame()
	fileManagerMu.Lock()
	built := tree.rowBuilds
	fileManagerMu.Unlock()
	if built == 0 || built > 40 {
		t.Fatalf("一帧应只构建视口行，得到 %d 行", built)
	}
	if calls := client.totalCalls(); calls != 0 {
		t.Fatalf("滚动/渲染不应触发目录请求，得到 %d 次", calls)
	}
}

func TestFileTreeSearchRestoresPosition(t *testing.T) {
	entries := []FileEntry{{Name: "sub", Path: "sub", IsDir: true}}
	for i := 0; i < 80; i++ {
		name := fmt.Sprintf("f%05d", i)
		entries = append(entries, FileEntry{Name: name, Path: name, Size: 1})
	}
	fake := treeTestBase()
	fake.SetFileTree(map[string][]FileEntry{"": entries, "sub": {{Name: "b.go", Path: "sub/b.go"}}})
	fake.SetFiles([]FileMatch{{Name: "match.go", Path: "match.go"}})
	s, tt := openFileTreeForTest(t, newTreeListRecorder(fake, map[string][]FileEntry{"": entries, "sub": {{Name: "b.go", Path: "sub/b.go"}}}))
	waitFor(t, tt, func() bool { return tt.HasText("f00000") })

	if err := tt.Click("sub"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("b.go") })

	row, ok := tt.Find("f00000")
	if !ok {
		t.Fatal("找不到根目录首行")
	}
	tt.Scroll(row.X+Space2, row.Y+row.H/2, 0, 400)
	tt.Frame()
	before := treeFirstVisibleForTest(s)
	if before == "" || before == "f00000" {
		t.Fatalf("滚动后首行应变化，得到 %q", before)
	}
	s.fileTreeSelect(before, 0)
	openBefore := treeOpenForTest(s)

	if err := tt.Click("搜索文件"); err != nil {
		t.Fatal(err)
	}
	tt.Type("match")
	waitFor(t, tt, func() bool { return tt.HasText("match.go") })
	if err := tt.Click("清除搜索"); err != nil {
		t.Fatal(err)
	}
	state := getFileManagerState(s)
	waitFor(t, tt, func() bool {
		fileManagerMu.Lock()
		defer fileManagerMu.Unlock()
		return state.query == "" && state.tree != nil && state.tree.outline.Rows() > 0
	})
	tt.Frame()

	if got := treeFirstVisibleForTest(s); got != before {
		t.Fatalf("清空搜索后滚动位置跳了：%q -> %q", before, got)
	}
	state = getFileManagerState(s)
	fileManagerMu.Lock()
	selected := ""
	for path := range state.tree.selection.All() {
		selected = path
	}
	fileManagerMu.Unlock()
	if selected != before {
		t.Fatalf("清空搜索后选中项变化：%q -> %q", before, selected)
	}
	if got := treeOpenForTest(s); len(got) != len(openBefore) || !got["sub"] {
		t.Fatalf("清空搜索后展开状态变化：%v -> %v", openBefore, got)
	}
}

func TestFileTreeNarrowNavigation(t *testing.T) {
	fake := treeTestBase()
	fake.SetFileContents(map[string]FileContent{
		"a.txt":    {Path: "a.txt", Kind: "text", Text: "hello", TotalSize: 5},
		"sub/b.go": {Path: "sub/b.go", Kind: "text", Text: "world", TotalSize: 5},
	})
	s, tt := openFileTreeForTest(t, fake)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	// 默认 480：树与内容二选一。
	if err := tt.Click("a.txt"); err != nil {
		t.Fatal(err)
	}
	waitForFileManagerText(t, tt, s, "a.txt", "hello")
	if _, ok := tt.Find("返回文件树"); !ok {
		t.Fatal("窄栏预览应提供返回文件树按钮")
	}
	if err := tt.Click("返回文件树"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("sub") })

	// 拉到 800：内容占满，用户主动展开导航后并排。
	session := inspectorSession(s)
	session.Width = 800
	s.invalidate()
	tt.Advance(DurSheet + 50*time.Millisecond)
	if err := tt.Click("a.txt"); err != nil {
		t.Fatal(err)
	}
	waitForFileManagerText(t, tt, s, "a.txt", "hello")
	if err := tt.Click("展开文件导航"); err != nil {
		t.Fatalf("click expand nav failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("sub") })
	if _, ok := tt.Find("收起文件导航"); !ok {
		t.Fatal("宽栏展开导航后应显示收起按钮")
	}
	if err := tt.Click("sub"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("b.go") })

	openTestFileManagerFile(t, s, "a.txt", true)
	openTestFileManagerFile(t, s, "sub/b.go", true)
	s.activateFileManagerTab("sub/b.go")
	s.splitFileManager()
	_, active, anchor, side, _, _ := fileManagerTabsForTest(s)
	if active != "sub/b.go" || anchor != "sub/b.go" || side != "a.txt" {
		t.Fatalf("拆分状态不符：active=%q anchor=%q side=%q", active, anchor, side)
	}

	// 缩窄后只显示活动文件，但活动文件与拆分偏好都保留。
	session.Width = 500
	s.invalidate()
	tt.Advance(DurSheet + 50*time.Millisecond)
	_, active, anchor, side, _, _ = fileManagerTabsForTest(s)
	if active != "sub/b.go" || anchor != "sub/b.go" || side != "a.txt" {
		t.Fatalf("缩窄后丢失活动文件或拆分偏好：active=%q anchor=%q side=%q", active, anchor, side)
	}
	if _, ok := tt.Find("返回文件树"); !ok {
		t.Fatal("缩窄后内容视图应提供返回按钮")
	}

	// 再拉宽，导航展开偏好仍然保留，树与内容重新并排。
	session.Width = 800
	s.invalidate()
	tt.Advance(DurSheet + 50*time.Millisecond)
	if _, ok := tt.Find("收起文件导航"); !ok {
		t.Fatal("重新拉宽后导航展开偏好丢失")
	}
	waitFor(t, tt, func() bool { return tt.HasText("sub") })
	_, active, anchor, side, _, _ = fileManagerTabsForTest(s)
	if active != "sub/b.go" || anchor != "sub/b.go" || side != "a.txt" {
		t.Fatalf("拉宽后丢失活动文件或拆分偏好：active=%q anchor=%q side=%q", active, anchor, side)
	}
}

func TestFileTreeInvalidatePreservesState(t *testing.T) {
	entries := []FileEntry{{Name: "sub", Path: "sub", IsDir: true}}
	for i := 0; i < 60; i++ {
		name := fmt.Sprintf("f%05d", i)
		entries = append(entries, FileEntry{Name: name, Path: name, Size: 1})
	}
	fake := treeTestBase()
	sub := []FileEntry{{Name: "b.go", Path: "sub/b.go"}}
	client := newTreeListRecorder(fake, map[string][]FileEntry{"": entries, "sub": sub})
	s, tt := openFileTreeForTest(t, client)
	waitFor(t, tt, func() bool { return tt.HasText("f00000") })

	if err := tt.Click("sub"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("b.go") })
	row, ok := tt.Find("f00000")
	if !ok {
		t.Fatal("找不到根目录首行")
	}
	tt.Scroll(row.X+Space2, row.Y+row.H/2, 0, 300)
	tt.Frame()
	first := treeFirstVisibleForTest(s)
	s.fileTreeSelect(first, 0)
	openBefore := treeOpenForTest(s)

	next := append([]FileEntry(nil), entries...)
	next = append(next, FileEntry{Name: "zz-extra.txt", Path: "zz-extra.txt", Size: 1})
	client.setResults("", next)
	threadID := fileManagerThreadID(s)
	fileTreeInvalidate(s, threadID, nil)
	st := getFileManagerState(s)
	waitFor(t, tt, func() bool {
		fileManagerMu.Lock()
		defer fileManagerMu.Unlock()
		return len(ensureFileTreeLocked(st).children[""]) == len(next)
	})
	tt.Frame()

	if got := treeFirstVisibleForTest(s); got != first {
		t.Fatalf("刷新后滚动位置跳了：%q -> %q", first, got)
	}
	fileManagerMu.Lock()
	selected := ""
	for path := range st.tree.selection.All() {
		selected = path
	}
	fileManagerMu.Unlock()
	if selected != first {
		t.Fatalf("刷新后选中项变化：%q -> %q", first, selected)
	}
	if got := treeOpenForTest(s); !got["sub"] || len(got) != len(openBefore) {
		t.Fatalf("刷新后展开状态变化：%v -> %v", openBefore, got)
	}
}

func TestFileManagerCloseLastTabReturnsToTree(t *testing.T) {
	_, s, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	openTestFileManagerFile(t, s, "a.txt", true)
	waitForFileManagerText(t, tt, s, "a.txt", "hello")
	s.closeFileManagerTab("a.txt")

	if !isFileManagerOpen(s) {
		t.Fatal("关闭最后一个标签后右栏不应收起")
	}
	paths, _, _, _, _, showList := fileManagerTabsForTest(s)
	if len(paths) != 0 || !showList {
		t.Fatalf("关闭最后一个标签后应回到文件树：paths=%v showList=%v", paths, showList)
	}
	waitFor(t, tt, func() bool { return tt.HasText("sub") })
}
