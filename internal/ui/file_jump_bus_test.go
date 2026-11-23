package mcodeui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"mcode/internal/fsutil"
	"mcode/internal/testenv"
)

func fileJumpTestState(t *testing.T, workDir string) (*FakeClient, *chatState) {
	t.Helper()
	testenv.TempHome(t)
	client := NewFakeClient()
	client.SetThreadWorkDir(workDir)
	s := newChatState(client)
	s.selectedThreadID = "t1"
	s.projects = []Project{{ID: "p1", Path: workDir}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}}
	t.Cleanup(s.close)
	return client, s
}

func assertFileManagerTabHighlight(t *testing.T, s *chatState, wantPath string, wantFirst, wantLast int) {
	t.Helper()
	st := getFileManagerState(s)
	if st == nil {
		t.Fatal("fileManagerState 为空")
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	if st.active != wantPath {
		t.Fatalf("active tab = %q，期望 %q", st.active, wantPath)
	}
	tab := st.findTabLocked(wantPath)
	if tab == nil {
		t.Fatalf("未打开 tab %q", wantPath)
	}
	if tab.hlFirst != wantFirst || tab.hlLast != wantLast || !tab.reveal {
		t.Fatalf("tab 行号高亮不符：got (%d-%d, reveal=%v), want (%d-%d, true)", tab.hlFirst, tab.hlLast, tab.reveal, wantFirst, wantLast)
	}
}

func TestFileJumpBusBuiltInAndExternalFallback(t *testing.T) {
	workDir := t.TempDir()
	outsideDir := t.TempDir()
	inWorkFile := filepath.Join(workDir, "src", "main.go")
	if err := os.MkdirAll(filepath.Dir(inWorkFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inWorkFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outsideDir, "outside.go")
	if err := os.WriteFile(outsideFile, []byte("package outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	client, s := fileJumpTestState(t, workDir)
	client.SetFileContents(map[string]FileContent{
		"src/main.go": {Path: "src/main.go", Kind: "text", Text: "package main\n", TotalSize: 13},
	})

	// 1. fileJumpBuiltIn：工作区内文件进入内置文件面板并规范化 EndLine < Line，同时高亮跳行。
	if !s.openFileAtLocation(fileJumpRequest{
		Path:    inWorkFile,
		Line:    12,
		EndLine: 3,
		Target:  fileJumpBuiltIn,
	}) {
		t.Fatal("工作区内内置跳转应成功")
	}
	if !isFileManagerOpen(s) {
		t.Fatal("内置跳转后文件面板应打开")
	}
	assertFileManagerTabHighlight(t, s, "src/main.go", 12, 12)

	// 2. 路径穿越工作区外不得误判为工作区内。
	var localOpened []string
	origLocalFn := fileJumpOpenLocalFn
	t.Cleanup(func() {
		fileJumpOpenLocalFn = origLocalFn
	})
	fileJumpOpenLocalFn = func(p string) { localOpened = append(localOpened, p) }
	withOpenLineStubs(t, nil, func(string) error { return nil })

	traversal := filepath.Join("..", filepath.Base(outsideDir), "outside.go")
	if !s.openFileAtLocation(fileJumpRequest{
		Path:   traversal,
		Line:   9,
		Target: fileJumpBuiltIn,
	}) {
		t.Fatal("工作区外回退 openLocalFile 应返回 true")
	}
	if len(localOpened) != 1 || localOpened[0] != outsideFile {
		t.Fatalf("穿越路径应回退外部打开 %q，实际 %v", outsideFile, localOpened)
	}
	if st := getFileManagerState(s); st != nil {
		fileManagerMu.Lock()
		hasOutside := st.findTabLocked(traversal) != nil || st.findTabLocked("outside.go") != nil
		fileManagerMu.Unlock()
		if hasOutside {
			t.Fatal("穿越工作区外的路径不得在内置文件面板打开")
		}
	}

	// 3. 终端外部跳行失败时，工作区内文件回退内置文件面板并保留行号高亮。
	withOpenLineStubs(t, []openInApp{{Name: "Cursor"}}, func(string) error {
		return errors.New("editor failed")
	})
	openTerminalLinkTarget(s, inWorkFile, 27, 4)
	assertFileManagerTabHighlight(t, s, "src/main.go", 27, 27)

	// 4. 外部跳行失败且不在工作区内时，回退 fileJumpOpenLocalFn。
	localOpened = nil
	openTerminalLinkTarget(s, outsideFile, 15, 2)
	if len(localOpened) != 1 || localOpened[0] != outsideFile {
		t.Fatalf("工作区外外部跳行失败应回退 fileJumpOpenLocalFn(%q)，实际 %v", outsideFile, localOpened)
	}
}

func TestFileJumpBusConsoleSourceLocation(t *testing.T) {
	resetConsoleSourceCache()
	t.Cleanup(resetConsoleSourceCache)

	workDir := t.TempDir()
	outsideDir := t.TempDir()
	appFile := filepath.Join(workDir, "src", "app.ts")
	if err := os.MkdirAll(filepath.Dir(appFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(appFile, []byte("console.log('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outsideDir, "secret.ts")
	if err := os.WriteFile(outsideFile, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	statCalls := 0
	origStat := consoleSourceStatFn
	t.Cleanup(func() { consoleSourceStatFn = origStat })
	consoleSourceStatFn = func(name string) (os.FileInfo, error) {
		statCalls++
		return os.Stat(name)
	}

	// 1. file:// URL 带 :line:col 与 query/hash。
	fileURL := fsutil.FileURL(appFile).String() + ":14:3?v=1#top"
	gotAbs, gotLine, ok := parseConsoleSourceLocation(fileURL, 0, workDir)
	if !ok || gotAbs != appFile || gotLine != 14 {
		t.Fatalf("file:// 解析失败: got (%q, %d, %v), want (%q, 14, true)", gotAbs, gotLine, ok, appFile)
	}

	// 2. http(s)://host:port/path/to/file.ts 去 query/hash 映射工作区文件 + 缓存命中不重复 stat。
	httpURL := "http://localhost:3000/src/app.ts?t=123#L5"
	gotAbs, gotLine, ok = parseConsoleSourceLocation(httpURL, 21, workDir)
	if !ok || gotAbs != appFile || gotLine != 21 {
		t.Fatalf("http URL 解析失败: got (%q, %d, %v)", gotAbs, gotLine, ok)
	}
	callsAfterFirst := statCalls
	gotAbs, gotLine, ok = parseConsoleSourceLocation(httpURL, 33, workDir)
	if !ok || gotAbs != appFile || gotLine != 33 {
		t.Fatalf("缓存命中后解析失败: got (%q, %d, %v)", gotAbs, gotLine, ok)
	}
	if statCalls != callsAfterFirst {
		t.Fatalf("缓存命中不应重复调用 os.Stat：first=%d, second=%d", callsAfterFirst, statCalls)
	}

	// 3. 相对路径与 :line:col。
	gotAbs, gotLine, ok = parseConsoleSourceLocation("./src/app.ts:9:2", 0, workDir)
	if !ok || gotAbs != appFile || gotLine != 9 {
		t.Fatalf("相对路径解析失败: got (%q, %d, %v)", gotAbs, gotLine, ok)
	}

	// 4. 目录穿越、工作区外文件、目录本身与未知 scheme 均拒绝。
	rejected := []string{
		"http://localhost:3000/../../etc/passwd",
		"../" + filepath.Base(outsideDir) + "/secret.ts",
		outsideFile,
		fsutil.FileURL(outsideFile).String(),
		filepath.Join(workDir, "src"),
		"webpack://app/src/app.ts",
	}
	for _, src := range rejected {
		if abs, ln, ok := parseConsoleSourceLocation(src, 10, workDir); ok {
			t.Fatalf("非法或工作区外路径 %q 不应解析成功，得到 (%q, %d)", src, abs, ln)
		}
	}
}

func TestFileJumpBusReviewCommentAndConsoleJump(t *testing.T) {
	resetConsoleSourceCache()
	t.Cleanup(resetConsoleSourceCache)

	workDir := t.TempDir()
	goFile := filepath.Join(workDir, "a.go")
	if err := os.WriteFile(goFile, []byte("package a\nvar a = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tsFile := filepath.Join(workDir, "src", "app.ts")
	if err := os.MkdirAll(filepath.Dir(tsFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tsFile, []byte("throw new Error('boom')\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	client, s := fileJumpTestState(t, workDir)
	client.SetFileContents(map[string]FileContent{
		"a.go":       {Path: "a.go", Kind: "text", Text: "package a\nvar a = 1\n", TotalSize: 20},
		"src/app.ts": {Path: "src/app.ts", Kind: "text", Text: "throw new Error('boom')\n", TotalSize: 24},
	})

	// 1. 审阅正常评论与过期评论点击跳转，触发文件面板目标行高亮。
	dp := getDiffPanelState(s)
	key := reviewDiffKey{workspace: workDir, threadID: "t1", scope: "turn", turnID: "turn-1", path: "a.go"}
	content := ReviewFileContent{Path: "a.go", OldText: "package a\nvar a = 1\n", NewText: "package a\nvar a = 2\n"}
	addReviewTestComment(t, dp, key, content, editor.DiffLineRange{Side: editor.DiffNew, Start: 1, End: 1}, "正常评论")
	diffPanelMu.Lock()
	dp.commentSerial++
	dp.comments = append(dp.comments, &reviewComment{
		id:       dp.commentSerial,
		key:      key,
		lines:    editor.DiffLineRange{Side: editor.DiffNew, Start: 9, End: 11},
		original: "old",
		body:     "过期评论",
		stale:    true,
	})
	normal := *dp.comments[0]
	diffPanelMu.Unlock()

	reviewTT := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("review-jump-host")).FillWidth().Children(func() {
			renderReviewComment(c, s, dp, normal)
			renderReviewOrphanComments(c, s, dp, key, content)
		})
	}, 600, 320)
	reviewTT.Advance(DurSheet + 50*time.Millisecond)

	if err := reviewTT.Click("跳转到 a.go:2"); err != nil {
		t.Fatalf("点击正常评论跳转按钮失败: %v", err)
	}
	assertFileManagerTabHighlight(t, s, "a.go", 2, 2)

	if err := reviewTT.Click("跳转到 a.go:10"); err != nil {
		t.Fatalf("点击过期评论跳转按钮失败: %v", err)
	}
	assertFileManagerTabHighlight(t, s, "a.go", 10, 12)

	// 2. 控制台报错行渲染紧凑跳转按钮，点击跳转至内置文件面板对应行，动画结束后无空闲重绘。
	tab := getPreviewState(s)
	previewMu.Lock()
	tab.consoleOpen = true
	tab.consoleErrors = []previewConsoleError{
		{Sequence: 1, Time: "12:00:00", Kind: "error", Message: "boom", Source: "http://localhost:3000/src/app.ts", Line: 42},
		{Sequence: 2, Time: "12:00:01", Kind: "warn", Message: "outside", Source: "http://localhost:3000/../../etc/passwd", Line: 1},
	}
	previewMu.Unlock()

	consoleTT := ui.NewTester(func(c *ui.Context) {
		previewMu.Lock()
		snap := previewSnapshotLocked(tab)
		previewMu.Unlock()
		renderPreviewConsoleDrawer(c, s, tab, snap)
	}, 720, 260)
	consoleTT.Advance(DurSheet + 50*time.Millisecond)

	if _, ok := consoleTT.Find("app.ts:42"); !ok {
		t.Fatal("控制台工作区报错行应渲染 app.ts:42 跳转按钮")
	}
	if _, ok := consoleTT.Find("passwd:1"); ok {
		t.Fatal("工作区外报错行不应渲染跳转按钮")
	}
	if err := consoleTT.Click("app.ts:42"); err != nil {
		t.Fatalf("点击控制台跳转按钮失败: %v", err)
	}
	assertFileManagerTabHighlight(t, s, "src/app.ts", 42, 42)

	consoleTT.Advance(DurSlow)
	consoleTT.Frame()
	if consoleTT.FramePending() {
		t.Fatal("控制台抽屉静止后不应继续请求下一帧")
	}
}
