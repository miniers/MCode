package mcodeui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
	"mcode/internal/testenv"
)

// fileManagerTestTree 构造测试用的目录树：根目录含一个文件、一个子目录和一个受保护文件，
// 子目录 sub 含一个文件。
func fileManagerTestTree() map[string][]FileEntry {
	return map[string][]FileEntry{
		"": {
			{Name: "a.txt", Path: "a.txt", Size: 5},
			{Name: "sub", Path: "sub", IsDir: true},
			{Name: "secret.env", Path: "secret.env", Size: 12, Protected: true},
		},
		"sub": {
			{Name: "b.go", Path: "sub/b.go", Size: 2048},
		},
	}
}

// fileManagerSnapshot 取面板状态的只读快照（目录、根目录条目数、当前激活的 tab 路径）。
func fileManagerSnapshot(s *chatState) (dir string, entries int, active string) {
	st := getFileManagerState(s)
	if st == nil {
		return "", 0, ""
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	if st.tree != nil {
		entries = len(st.tree.children[""])
	}
	return st.dir, entries, st.active
}

// fileManagerTabPaths 取面板当前所有 tab 的路径（按打开顺序）。
func fileManagerTabPaths(s *chatState) []string {
	st := getFileManagerState(s)
	if st == nil {
		return nil
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	paths := make([]string, 0, len(st.tabs))
	for _, tab := range st.tabs {
		paths = append(paths, tab.path)
	}
	return paths
}

// fileManagerTabText 取某个 tab 已加载的预览文本。代码预览改成编辑器自绘后不再进
// UI 的可查文字，测试改为直接等面板状态里的内容。
func fileManagerTabText(s *chatState, path string) string {
	st := getFileManagerState(s)
	if st == nil {
		return ""
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	tab := st.findTabLocked(path)
	if tab == nil {
		return ""
	}
	return tab.content
}

// waitForFileManagerText 等待某个 tab 的预览文本加载到期望值。
func waitForFileManagerText(t *testing.T, tt *ui.Tester, s *chatState, path, want string) {
	t.Helper()
	waitFor(t, tt, func() bool { return fileManagerTabText(s, path) == want })
}

// openFileManagerForTest 启动界面、设置工作目录与目录树并点开文件面板。
func openFileManagerForTest(t *testing.T) (*FakeClient, *chatState, *ui.Tester) {
	t.Helper()
	testenv.TempHome(t)
	fake := NewFakeClient()
	workDir := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake.SetThreadWorkDir(workDir)
	fake.SetFileTree(fileManagerTestTree())
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "hello", TotalSize: 5},
	})
	s, root := buildRoot(fake)
	tt := ui.NewTester(root, 1200, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	tt.Key(ui.Cmd|ui.Shift, ui.KeyF)
	waitFor(t, tt, func() bool { return isFileManagerOpen(s) })
	tt.Advance(DurSheet + 50*time.Millisecond)
	return fake, s, tt
}

// 头部文件按钮打开面板，关闭按钮收起面板。
func TestFileManagerToggle(t *testing.T) {
	_, s, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	if err := tt.Click("关闭右栏"); err != nil {
		t.Fatalf("click close button failed: %v", err)
	}
	waitFor(t, tt, func() bool { return !isFileManagerOpen(s) })
}

// 打开面板即列出工作目录根目录的条目。
func TestFileManagerListRoot(t *testing.T) {
	_, s, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") && tt.HasText("sub") })

	dir, entries, _ := fileManagerSnapshot(s)
	if dir != "" {
		t.Fatalf("初始目录应为根目录，得到 %q", dir)
	}
	if entries != 3 {
		t.Fatalf("根目录应有 3 个条目，得到 %d", entries)
	}
}

// 点目录在树里展开，再点一次收起；根目录的条目仍然保留。
func TestFileManagerTreeExpandAndCollapse(t *testing.T) {
	_, s, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("sub") })

	if err := tt.Click("sub"); err != nil {
		t.Fatalf("click sub dir failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("b.go") })
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	tree := ensureFileTreeLocked(st)
	expanded := tree.outline.Open.Has("sub")
	fileManagerMu.Unlock()
	if !expanded {
		t.Fatal("点击目录后应展开")
	}

	if err := tt.Click("sub"); err != nil {
		t.Fatalf("click sub dir again failed: %v", err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("b.go") && tt.HasText("a.txt") })
}

// 搜索结果显示相对路径；清空后回到目录列表。
func TestFileManagerSearch(t *testing.T) {
	fake, _, tt := openFileManagerForTest(t)
	fake.SetFiles([]FileMatch{{Name: "beta.go", Path: "src/beta.go"}})
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	if err := tt.Click("搜索文件"); err != nil {
		t.Fatalf("click search input failed: %v", err)
	}
	tt.Type("beta")

	waitFor(t, tt, func() bool { return tt.HasText("src/beta.go") })

	if err := tt.Click("清除搜索"); err != nil {
		t.Fatalf("click clear search failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })
}

func TestFileManagerSearchTruncation(t *testing.T) {
	fake, _, tt := openFileManagerForTest(t)
	fake.SetFiles([]FileMatch{{Name: "beta.go", Path: "src/beta.go"}})
	fake.mu.Lock()
	fake.filesTruncated = true
	fake.mu.Unlock()
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })
	if err := tt.Click("搜索文件"); err != nil {
		t.Fatal(err)
	}
	tt.Type("beta")
	waitFor(t, tt, func() bool { return tt.HasText("src/beta.go") && tt.HasText("搜索结果已截断") })
}

func TestFileManagerSearchErrorDoesNotLeakToDirectory(t *testing.T) {
	fake, _, tt := openFileManagerForTest(t)
	fake.mu.Lock()
	fake.searchFilesErr = errors.New("搜索失败测试")
	fake.mu.Unlock()
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })
	if err := tt.Click("搜索文件"); err != nil {
		t.Fatal(err)
	}
	tt.Type("beta")
	waitFor(t, tt, func() bool { return tt.HasText("搜索失败测试") })
	if err := tt.Click("清除搜索"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })
	if tt.HasText("搜索失败测试") {
		t.Fatal("搜索错误残留在目录中")
	}
}

func TestFileManagerSearchSuccessClearsError(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	fake.SetFiles([]FileMatch{{Name: "beta.go", Path: "src/beta.go"}})
	fake.mu.Lock()
	fake.searchFilesErr = errors.New("搜索失败测试")
	fake.mu.Unlock()
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })
	if err := tt.Click("搜索文件"); err != nil {
		t.Fatal(err)
	}
	tt.Type("bet")
	waitFor(t, tt, func() bool { return tt.HasText("搜索失败测试") })
	fake.mu.Lock()
	fake.searchFilesErr = nil
	fake.mu.Unlock()
	tt.Type("a")
	waitFor(t, tt, func() bool { return tt.HasText("src/beta.go") })
	fileManagerMu.Lock()
	errText := getFileManagerStateForTest(s).searchErr
	fileManagerMu.Unlock()
	if errText != "" || tt.HasText("搜索失败测试") {
		t.Fatalf("成功后搜索错误未清除：%q", errText)
	}
}

func getFileManagerStateForTest(s *chatState) *fileManagerState {
	return fileManagerStates[s]
}

type delayedFileManagerSearchClient struct {
	*FakeClient
	started chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (f *delayedFileManagerSearchClient) SearchFileEntries(ctx context.Context, threadID, query string) (FileSearchResult, error) {
	if threadID == "old" {
		close(f.started)
		<-f.release
		defer close(f.done)
		return FileSearchResult{Entries: []FileMatch{{Path: "old.go"}}, Truncated: true}, nil
	}
	return FileSearchResult{Entries: []FileMatch{{Path: "new.go"}}}, nil
}

func TestFileManagerSearchDropsPreviousThread(t *testing.T) {
	fake := &delayedFileManagerSearchClient{FakeClient: NewFakeClient(), started: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
	s := &chatState{client: fake}
	st := getFileManagerState(s)
	s.loadFileManagerDir("old", "")
	s.fileManagerSearch("old", "go")
	select {
	case <-fake.started:
	case <-time.After(5 * time.Second):
		t.Fatal("旧搜索未启动")
	}
	s.loadFileManagerDir("new", "")
	s.fileManagerSearch("new", "go")
	deadline := time.After(5 * time.Second)
	for {
		fileManagerMu.Lock()
		ready := !st.searching && len(st.searchResults) == 1 && st.searchResults[0].Path == "new.go"
		fileManagerMu.Unlock()
		if ready {
			break
		}
		select {
		case <-deadline:
			t.Fatal("新搜索未完成")
		case <-time.After(10 * time.Millisecond):
		}
	}
	close(fake.release)
	<-fake.done
	time.Sleep(50 * time.Millisecond)
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	if st.threadID != "new" || len(st.searchResults) != 1 || st.searchResults[0].Path != "new.go" || st.searchTruncated {
		t.Fatalf("旧会话结果覆盖了新会话：%+v", st.searchResults)
	}
}

// 点文件读取文本预览，「引用到输入框」生成引用附件并保留正文。
func TestFileManagerPreviewTextAndMention(t *testing.T) {
	_, s, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	if err := tt.Click("a.txt"); err != nil {
		t.Fatalf("click file row failed: %v", err)
	}
	waitForFileManagerText(t, tt, s, "a.txt", "hello")

	if err := tt.Click("引用到输入框"); err != nil {
		t.Fatalf("click mention button failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.draft == "" && len(s.attachments) == 1 && s.attachments[0].Name == "a.txt" && s.attachments[0].ID != ""
	})

	// 返回按钮回到目录列表。
	if err := tt.Click("返回文件树"); err != nil {
		t.Fatalf("click back button failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("sub") })
}

// 预览视图里的关闭按钮（标签栏最右侧）收起文件面板。
func TestFileManagerPreviewCloseButton(t *testing.T) {
	_, s, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	if err := tt.Click("a.txt"); err != nil {
		t.Fatalf("click file row failed: %v", err)
	}
	waitForFileManagerText(t, tt, s, "a.txt", "hello")

	if err := tt.Click("关闭右栏"); err != nil {
		t.Fatalf("click preview close button failed: %v", err)
	}
	waitFor(t, tt, func() bool { return !isFileManagerOpen(s) })
}

// 预览字号跟随设置里的编辑器字号，未设置回退默认值，越界时被夹到上下限。
func TestFileManagerPreviewFontSize(t *testing.T) {
	t.Cleanup(func() { setActiveTheme(config.AppearanceConfig{}) })
	tests := []struct {
		name       string
		configured int
		want       float32
	}{
		{"跟随设置", 16, 16},
		{"未设置回退默认", 0, float32(config.DefaultEditorFontSize)},
		{"超过上限被夹取", config.MaxEditorFontSize + 5, float32(config.MaxEditorFontSize)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newChatState(nil)
			settings := NewSettingsState(settingsTestDir(t), nil, s)
			setActiveTheme(config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: config.ThemeLight, Fonts: map[string]int{"editor": tc.configured}}}})
			setChatSettings(s, settings)
			if got := fileManagerPreviewFontSize(s); got != tc.want {
				t.Errorf("EditorFontSize=%d 时预览字号 = %v, want %v", tc.configured, got, tc.want)
			}
		})
	}
}

// 读取失败时面板内显示错误行，不弹窗。
func TestFileManagerPreviewError(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	fake.SetFilesError(errors.New("读取文件失败"))
	if err := tt.Click("a.txt"); err != nil {
		t.Fatalf("click file row failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("无法读取") })
	waitFor(t, tt, func() bool { return tt.HasText("读取文件失败") })

	if _, _, active := fileManagerSnapshot(s); active != "a.txt" {
		t.Fatalf("读取失败后仍应停在文件视图，得到 %q", active)
	}
}

// 受保护条目点击无效：不进入目录、不打开文件。
func TestFileManagerProtectedEntryIgnoresClick(t *testing.T) {
	_, s, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("secret.env") })

	if err := tt.Click("secret.env"); err != nil {
		t.Fatalf("click protected row failed: %v", err)
	}
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if _, _, active := fileManagerSnapshot(s); active != "" {
		t.Fatalf("受保护条目不应打开预览，得到 %q", active)
	}
}

// 目录条目被截断时显示截断提示。
func TestFileManagerTruncatedNotice(t *testing.T) {
	_, s, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	if st := getFileManagerState(s); st != nil {
		fileManagerMu.Lock()
		ensureFileTreeLocked(st).rootTruncated = true
		fileManagerMu.Unlock()
	}
	s.invalidate()

	waitFor(t, tt, func() bool { return tt.HasText("目录条目已截断（仅显示前 1000 个条目）") })
}

// 目的切换只改变 Active，文件状态仍在原槽中。
func TestFileManagerClosedByOtherPanels(t *testing.T) {
	_, s, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })
	files := getFileManagerState(s)

	s.toggleDiffPanel()
	waitFor(t, tt, func() bool { return !isFileManagerOpen(s) })

	// 再次打开文件面板后切到预览，文件面板同样关闭。
	toggleFileManager(s)
	waitFor(t, tt, func() bool { return isFileManagerOpen(s) })

	togglePreview(s)
	waitFor(t, tt, func() bool { return !isFileManagerOpen(s) })

	// 预览打开时打开改动面板：预览被关掉（旧实现里二者可以同时为真，预览赢、改动被静默隐藏）。
	s.toggleDiffPanel()
	waitFor(t, tt, func() bool {
		previewMu.Lock()
		defer previewMu.Unlock()
		st := previewStates[s]
		return st == nil || !st.visible
	})
	if !getDiffPanelState(s).IsOpen() {
		t.Fatal("打开改动面板后它应处于展开状态")
	}
	if getFileManagerState(s) != files {
		t.Fatal("切换目的后文件状态被替换")
	}
}
