package mcodeui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"mcode/internal/testenv"
)

func clickFileManagerEditorForTest(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	var info ui.AccessInfo
	var ok bool
	waitFor(t, tt, func() bool {
		info, ok = tt.AccessByRole(ui.RoleTextField, name)
		return ok && info.Bounds.W > 0 && info.Bounds.H > 0
	})
	tt.ClickAt(info.Bounds.X+info.Bounds.W-4, info.Bounds.Y+min(float32(12), info.Bounds.H/2))
	tt.Frame()
}

func TestFileManagerEdit_CompleteTextEditable(t *testing.T) {
	_, s, tt := openFileManagerForTest(t)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	openTestFileManagerFileExternal(t, s, "a.txt")
	waitForFileManagerText(t, tt, s, "a.txt", "hello")

	st := getFileManagerState(s)
	preview := st.codeViews["file-manager-pane-0-a.txt"]
	if preview == nil || preview.state == nil {
		t.Fatal("编辑器状态未创建")
	}
	if preview.state.ReadOnly {
		t.Fatal("完整加载的文本文件应可编辑（ReadOnly=false）")
	}

	clickFileManagerEditorForTest(t, tt, "a.txt")
	tt.Type("!")
	waitFor(t, tt, func() bool {
		_, hasDot := tt.Find("未保存")
		return hasDot && preview.state.Buffer.Modified()
	})
	if got := preview.state.Buffer.String(); got != "hello!" {
		t.Fatalf("输入后缓冲区文本 = %q，期望 %q", got, "hello!")
	}

	// 关闭 dirty 的 tab 时弹出确认弹窗，取消后保留 tab 与圆点，确认后关闭。
	if err := tt.Click("关闭 a.txt"); err != nil {
		t.Fatalf("点击关闭按钮失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("放弃未保存的修改？") })
	if paths := fileManagerTabPaths(s); len(paths) != 1 || paths[0] != "a.txt" {
		t.Fatalf("未确认丢弃前不应关闭 tab，得到 %v", paths)
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatalf("点击取消失败: %v", err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("放弃未保存的修改？") })
	if _, hasDot := tt.Find("未保存"); !hasDot {
		t.Fatal("取消关闭后应保留未保存圆点")
	}

	if err := tt.Click("关闭 a.txt"); err != nil {
		t.Fatalf("再次点击关闭按钮失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("放弃并关闭") })
	if err := tt.Click("放弃并关闭"); err != nil {
		t.Fatalf("点击放弃并关闭失败: %v", err)
	}
	waitFor(t, tt, func() bool { return len(fileManagerTabPaths(s)) == 0 })
}

func TestFileManagerEdit_PartialOrNonTextReadOnly(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	fake.SetFileContents(map[string]FileContent{
		"partial.txt": {Path: "partial.txt", Kind: "text", Text: "part1", TotalSize: 100, HasMore: true, NextOffset: 5},
	})
	openTestFileManagerFileExternal(t, s, "partial.txt")
	waitForFileManagerText(t, tt, s, "partial.txt", "part1")

	st := getFileManagerState(s)
	preview := st.codeViews["file-manager-pane-0-partial.txt"]
	if preview == nil || preview.state == nil {
		t.Fatal("未完整加载文本也应建立编辑器状态")
	}
	if !preview.state.ReadOnly {
		t.Fatal("存在「加载更多」剩余内容时编辑器必须只读")
	}

	clickFileManagerEditorForTest(t, tt, "partial.txt")
	tt.Type("x")
	tt.Key(ui.Cmd, ui.KeyS)
	tt.Frame()
	if got := preview.state.Buffer.String(); got != "part1" {
		t.Fatalf("只读状态下输入不应修改缓冲区，得到 %q", got)
	}
	for _, call := range fake.FileOpCalls() {
		if call.Op == "write" {
			t.Fatalf("只读状态下不应调用 FilesWrite，得到 %+v", call)
		}
	}

	// 达到预览上限（limitReached）同样只读。
	if fileManagerTabEditable(fileManagerTabSnapshot{path: "huge.txt", kind: "text", content: "x", limitReached: true}) {
		t.Fatal("达到预览上限时不应允许编辑")
	}
	if fileManagerTabEditable(fileManagerTabSnapshot{path: "bin.dat", kind: "binary", content: "x"}) {
		t.Fatal("非文本文件不应允许编辑")
	}
}

func TestFileManagerEdit_DirtyNotOverwrittenByNewContent(t *testing.T) {
	st := newTestFileManagerState()
	state := fileManagerCodeViewFor(st, "file-manager-pane-0-a.go", "a.go", "package a\n", 13)
	state.Buffer.Edit(editor.Edit{Start: len("package a\n"), End: len("package a\n"), Text: "// local edit\n"})
	if !state.Buffer.Modified() {
		t.Fatal("编辑后缓冲区应为已修改状态")
	}

	reused := fileManagerCodeViewFor(st, "file-manager-pane-0-a.go", "a.go", "package b\n", 13)
	if reused != state {
		t.Fatal("dirty 时仍应复用同一个编辑器状态")
	}
	const wantBuffer = "package a\n// local edit\n"
	if got := reused.Buffer.String(); got != wantBuffer {
		t.Fatalf("dirty 时新 content 不得覆盖缓冲区: got %q, want %q", got, wantBuffer)
	}
	if got := st.codeViews["file-manager-pane-0-a.go"].content; got != "package a\n" {
		t.Fatalf("dirty 时记录的加载原文不应被替换: got %q, want %q", got, "package a\n")
	}
}

func TestFileManagerEdit_SaveWithExpectedHashAndClearDirty(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	openTestFileManagerFileExternal(t, s, "a.txt")
	waitForFileManagerText(t, tt, s, "a.txt", "hello")

	st := getFileManagerState(s)
	preview := st.codeViews["file-manager-pane-0-a.txt"]

	clickFileManagerEditorForTest(t, tt, "a.txt")
	tt.Type(" world")
	waitFor(t, tt, func() bool {
		_, hasDot := tt.Find("未保存")
		return hasDot && preview.state.Buffer.Modified()
	})
	edited := preview.state.Buffer.String()

	tt.Key(ui.Cmd, ui.KeyS)
	var writeCall FileOpCall
	waitFor(t, tt, func() bool {
		for _, call := range fake.FileOpCalls() {
			if call.Op == "write" {
				writeCall = call
				return true
			}
		}
		return false
	})

	if writeCall.Path != "a.txt" || writeCall.Content != edited {
		t.Fatalf("FilesWrite 参数不符: %+v, want content=%q", writeCall, edited)
	}
	if wantHash := fakeFileSHA256Hex("hello"); writeCall.ExpectedHash != wantHash {
		t.Fatalf("expectedHash = %q, want %q", writeCall.ExpectedHash, wantHash)
	}

	waitFor(t, tt, func() bool {
		_, hasDot := tt.Find("未保存")
		return !hasDot && !preview.state.Buffer.Modified() && fileManagerTabText(s, "a.txt") == edited
	})
	if preview.content != edited {
		t.Fatalf("保存后 preview.content = %q, want %q", preview.content, edited)
	}
}

func TestFileManagerEdit_SaveConflictKeepsBufferAndShowsToast(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	openTestFileManagerFileExternal(t, s, "a.txt")
	waitForFileManagerText(t, tt, s, "a.txt", "hello")

	st := getFileManagerState(s)
	preview := st.codeViews["file-manager-pane-0-a.txt"]

	clickFileManagerEditorForTest(t, tt, "a.txt")
	tt.Type("!")
	waitFor(t, tt, func() bool { return preview.state.Buffer.Modified() })
	edited := preview.state.Buffer.String()

	fake.SetFileWriteError(errors.New("rpc error -32009: file content changed"))
	tt.Key(ui.Cmd, ui.KeyS)
	waitFor(t, tt, func() bool { return tt.HasText("文件已在磁盘上变化，未保存") })

	if got := preview.state.Buffer.String(); got != edited {
		t.Fatalf("保存失败不应丢弃编辑内容: got %q, want %q", got, edited)
	}
	if !preview.state.Buffer.Modified() {
		t.Fatal("保存失败后缓冲区仍应保持 dirty")
	}
	if _, hasDot := tt.Find("未保存"); !hasDot {
		t.Fatal("保存失败后应保留未保存圆点")
	}
	if got := fileManagerTabText(s, "a.txt"); got != "hello" {
		t.Fatalf("保存失败后 tab 原文不应改变，得到 %q", got)
	}
}

type chunkedFileReadClient struct {
	*FakeClient
	files map[string]string
}

func (c *chunkedFileReadClient) FilesRead(ctx context.Context, threadID, path string, offset, length int64) (FileContent, error) {
	if text, ok := c.files[path]; ok {
		total := int64(len(text))
		if offset < 0 {
			offset = 0
		}
		if offset > total {
			offset = total
		}
		end := total
		if length > 0 && offset+length < end {
			end = offset + length
		}
		return FileContent{
			Path:       path,
			Kind:       "text",
			Text:       text[offset:end],
			TotalSize:  total,
			HasMore:    end < total,
			NextOffset: end,
		}, nil
	}
	return c.FakeClient.FilesRead(ctx, threadID, path, offset, length)
}

type blockingFileWriteClient struct {
	*FakeClient
	started chan struct{}
	release chan struct{}
}

func (c *blockingFileWriteClient) FilesWrite(ctx context.Context, threadID, path, content, expectedHash string) (string, error) {
	select {
	case <-c.started:
	default:
		close(c.started)
	}
	<-c.release
	return c.FakeClient.FilesWrite(ctx, threadID, path, content, expectedHash)
}

func TestFileManagerMidSizeTextEditable(t *testing.T) {
	midText := strings.Repeat("a", 200*1024)
	hugeText := strings.Repeat("b", fileManagerTextMaxBytes+1024)
	client := &chunkedFileReadClient{
		FakeClient: NewFakeClient(),
		files: map[string]string{
			"mid.txt":  midText,
			"huge.txt": hugeText,
		},
	}
	s := newInspectorPreviewState(t, client)

	s.openFileManagerFile("t1", "mid.txt", true)
	midTab := waitInspectorPreviewTab(t, s, "mid.txt")
	if midTab.hasMore || midTab.limitReached {
		t.Fatalf("200 KiB 文本应一次读完，得到 hasMore=%v limitReached=%v", midTab.hasMore, midTab.limitReached)
	}
	if !fileManagerTabEditable(midTab) {
		t.Fatal("200 KiB 文本读取完成后应可编辑")
	}
	if len(midTab.content) != len(midText) {
		t.Fatalf("200 KiB 文本长度 = %d，期望 %d", len(midTab.content), len(midText))
	}

	s.openFileManagerFile("t1", "huge.txt", true)
	hugeTab := waitInspectorPreviewTab(t, s, "huge.txt")
	if !hugeTab.limitReached {
		t.Fatalf(">512 KiB 文本应标记 limitReached=true，得到 hasMore=%v limitReached=%v", hugeTab.hasMore, hugeTab.limitReached)
	}
	if hugeTab.hasMore {
		t.Fatal(">512 KiB 文本达到上限后 hasMore 应为 false")
	}
	if fileManagerTabEditable(hugeTab) {
		t.Fatal(">512 KiB 文本达到上限后不应可编辑")
	}
}

func TestFileManagerHeaderSaveButton(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	workDir := filepath.Join(t.TempDir(), "workspace")
	fake.SetThreadWorkDir(workDir)
	fake.SetFileTree(fileManagerTestTree())
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "hello", TotalSize: 5},
	})
	client := &blockingFileWriteClient{
		FakeClient: fake,
		started:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	s, root := buildRoot(client)
	defer s.close()
	tt := ui.NewTester(root, 1200, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	tt.Key(ui.Cmd|ui.Shift, ui.KeyF)
	waitFor(t, tt, func() bool { return isFileManagerOpen(s) })
	tt.Advance(DurSheet + 50*time.Millisecond)

	openTestFileManagerFileExternal(t, s, "a.txt")
	waitForFileManagerText(t, tt, s, "a.txt", "hello")
	tt.Frame()

	if _, ok := tt.Find("保存"); ok {
		t.Fatal("非 dirty 时头部不应渲染保存按钮")
	}

	st := getFileManagerState(s)
	preview := st.codeViews["file-manager-pane-0-a.txt"]
	clickFileManagerEditorForTest(t, tt, "a.txt")
	tt.Type(" world")
	waitFor(t, tt, func() bool {
		_, hasSave := tt.Find("保存")
		return hasSave && preview.state.Buffer.Modified()
	})
	tt.Advance(DurBase + 50*time.Millisecond)
	edited := preview.state.Buffer.String()

	if node, ok := tt.Access("保存"); !ok || node.Disabled {
		t.Fatalf("dirty 时保存按钮应存在且可用: ok=%v disabled=%v", ok, node.Disabled)
	}
	if err := tt.Click("保存"); err != nil {
		t.Fatalf("点击头部保存按钮失败: %v", err)
	}

	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		t.Fatal("点击保存按钮后未触发 FilesWrite")
	}
	waitFor(t, tt, func() bool {
		node, ok := tt.Access("保存")
		return ok && node.Disabled
	})

	close(client.release)
	var writeCall FileOpCall
	waitFor(t, tt, func() bool {
		for _, call := range fake.FileOpCalls() {
			if call.Op == "write" {
				writeCall = call
				return true
			}
		}
		return false
	})
	if writeCall.Path != "a.txt" || writeCall.Content != edited {
		t.Fatalf("FilesWrite 参数不符: %+v, want content=%q", writeCall, edited)
	}
	if wantHash := fakeFileSHA256Hex("hello"); writeCall.ExpectedHash != wantHash {
		t.Fatalf("expectedHash = %q, want %q", writeCall.ExpectedHash, wantHash)
	}

	tt.Advance(DurSnap + 50*time.Millisecond)
	waitFor(t, tt, func() bool {
		_, hasSave := tt.Find("保存")
		return !hasSave && !preview.state.Buffer.Modified() && fileManagerTabText(s, "a.txt") == edited
	})

	// 动效结束与减少动效下不应持续请求重绘。
	snap := fileManagerPanelSnapshot{
		tabs:   []fileManagerTabSnapshot{{path: "a.txt", kind: "text", dirty: true}},
		active: "a.txt",
	}
	headerTester := ui.NewTester(func(c *ui.Context) {
		activeTab, _ := snap.activeTab()
		renderFileManagerHeaderActions(c, s, snap, activeTab, false)
	}, 400, 100)
	headerTester.Frame()
	headerTester.Advance(DurBase + 50*time.Millisecond)
	if _, ok := headerTester.Find("保存"); !ok {
		t.Fatal("头部动作区 dirty 时应渲染保存按钮")
	}
	if headerTester.FramePending() {
		t.Fatal("保存按钮入场动画结束后不应继续请求重绘")
	}
	snap.tabs[0].dirty = false
	headerTester.Frame()
	headerTester.Advance(DurSnap + 50*time.Millisecond)
	if _, ok := headerTester.Find("保存"); ok {
		t.Fatal("取消 dirty 且退场动画结束后不应再保留保存按钮")
	}
	if headerTester.FramePending() {
		t.Fatal("保存按钮退场动画结束后不应继续请求重绘")
	}
}

func TestFileManagerSaveButtonUsesModifiedPane(t *testing.T) {
	if got := fileManagerModifiedPaneKeyPrefix(nil, "a.txt"); got != "file-manager-pane-0-" {
		t.Fatalf("nil state 前缀 = %q，期望 file-manager-pane-0-", got)
	}
	if got := fileManagerModifiedPaneKeyPrefix(&fileManagerState{}, "a.txt"); got != "file-manager-pane-0-" {
		t.Fatalf("nil codeViews 前缀 = %q，期望 file-manager-pane-0-", got)
	}

	pane1Only := newTestFileManagerState()
	pane1Buf := fileManagerCodeViewFor(pane1Only, "file-manager-pane-1-a.txt", "a.txt", "hello", 13)
	pane1Buf.Buffer.Edit(editor.Edit{Start: len("hello"), End: len("hello"), Text: "!"})
	if got := fileManagerModifiedPaneKeyPrefix(pane1Only, "a.txt"); got != "file-manager-pane-1-" {
		t.Fatalf("仅第 1 栏修改时前缀 = %q，期望 file-manager-pane-1-", got)
	}

	testenv.TempHome(t)
	fake := NewFakeClient()
	workDir := filepath.Join(t.TempDir(), "workspace")
	fake.SetThreadWorkDir(workDir)
	fake.SetFileTree(fileManagerTestTree())
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "hello", TotalSize: 5},
	})
	s, root := buildRoot(fake)
	defer s.close()
	tt := ui.NewTester(root, 1500, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	tt.Key(ui.Cmd|ui.Shift, ui.KeyF)
	waitFor(t, tt, func() bool { return isFileManagerOpen(s) })
	s.mu.Lock()
	if sess := inspectorExistingSessionLocked(s, "t1"); sess != nil {
		sess.Width = fileTreeSplitMinWidth + 80
	}
	s.mu.Unlock()
	tt.Advance(DurSheet + 50*time.Millisecond)

	openTestFileManagerFileExternal(t, s, "a.txt")
	waitForFileManagerText(t, tt, s, "a.txt", "hello")
	s.splitFileManager()

	st := getFileManagerState(s)
	var pane0, pane1 *fileManagerCodePreview
	waitFor(t, tt, func() bool {
		pane0 = st.codeViews["file-manager-pane-0-a.txt"]
		pane1 = st.codeViews["file-manager-pane-1-a.txt"]
		return pane0 != nil && pane0.state != nil && pane0.state.Buffer != nil &&
			pane1 != nil && pane1.state != nil && pane1.state.Buffer != nil
	})

	pane1.state.Buffer.Edit(editor.Edit{Start: len("hello"), End: len("hello"), Text: " world"})
	if pane0.state.Buffer.Modified() {
		t.Fatal("第 0 栏缓冲区不应被标记为已修改")
	}
	waitFor(t, tt, func() bool {
		_, hasSave := tt.Find("保存")
		return hasSave && pane1.state.Buffer.Modified()
	})
	tt.Advance(DurBase + 50*time.Millisecond)

	if err := tt.Click("保存"); err != nil {
		t.Fatalf("点击头部保存按钮失败: %v", err)
	}

	var writeCall FileOpCall
	waitFor(t, tt, func() bool {
		for _, call := range fake.FileOpCalls() {
			if call.Op == "write" {
				writeCall = call
				return true
			}
		}
		return false
	})
	const wantContent = "hello world"
	if writeCall.Path != "a.txt" || writeCall.Content != wantContent {
		t.Fatalf("FilesWrite 参数不符: %+v, want content=%q", writeCall, wantContent)
	}
	if wantHash := fakeFileSHA256Hex("hello"); writeCall.ExpectedHash != wantHash {
		t.Fatalf("expectedHash = %q, want %q", writeCall.ExpectedHash, wantHash)
	}

	tt.Advance(DurSnap + 50*time.Millisecond)
	waitFor(t, tt, func() bool {
		_, hasSave := tt.Find("保存")
		return !hasSave && !pane1.state.Buffer.Modified() && fileManagerTabText(s, "a.txt") == wantContent
	})
}

func TestFileManagerEmptyTextFileEditable(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	fake.SetFileContents(map[string]FileContent{
		"empty.txt": {Path: "empty.txt", Kind: "text", Text: "", TotalSize: 0},
	})
	openTestFileManagerFileExternal(t, s, "empty.txt")
	waitFor(t, tt, func() bool {
		st := getFileManagerState(s)
		if st == nil {
			return false
		}
		fileManagerMu.Lock()
		snap := fileManagerSnapshotLocked(st)
		fileManagerMu.Unlock()
		tab, ok := snap.tabByPath("empty.txt")
		return ok && !tab.fileLoading && tab.kind == "text"
	})
	tt.Frame()

	if tt.HasText("空文件") {
		t.Fatal("已加载的空文本文件不应再显示「空文件」提示")
	}

	var info ui.AccessInfo
	var ok bool
	waitFor(t, tt, func() bool {
		info, ok = tt.AccessByRole(ui.RoleTextField, "empty.txt")
		return ok && info.Bounds.W > 0 && info.Bounds.H > 0
	})

	st := getFileManagerState(s)
	preview := st.codeViews["file-manager-pane-0-empty.txt"]
	if preview == nil || preview.state == nil {
		t.Fatal("空文本文件编辑器状态未创建")
	}
	if preview.state.ReadOnly {
		t.Fatal("已加载的空文本文件应可编辑（ReadOnly=false）")
	}

	clickFileManagerEditorForTest(t, tt, "empty.txt")
	tt.Type("hello")
	waitFor(t, tt, func() bool {
		_, hasDot := tt.Find("未保存")
		return hasDot && preview.state.Buffer.Modified()
	})
	if got := preview.state.Buffer.String(); got != "hello" {
		t.Fatalf("输入后缓冲区文本 = %q，期望 %q", got, "hello")
	}

	tt.Key(ui.Cmd, ui.KeyS)
	var writeCall FileOpCall
	waitFor(t, tt, func() bool {
		for _, call := range fake.FileOpCalls() {
			if call.Op == "write" {
				writeCall = call
				return true
			}
		}
		return false
	})
	if writeCall.Path != "empty.txt" || writeCall.Content != "hello" {
		t.Fatalf("FilesWrite 参数不符: %+v, want content=%q", writeCall, "hello")
	}
	if wantHash := fakeFileSHA256Hex(""); writeCall.ExpectedHash != wantHash {
		t.Fatalf("expectedHash = %q, want %q", writeCall.ExpectedHash, wantHash)
	}

	waitFor(t, tt, func() bool {
		_, hasDot := tt.Find("未保存")
		return !hasDot && !preview.state.Buffer.Modified() && fileManagerTabText(s, "empty.txt") == "hello"
	})
}
