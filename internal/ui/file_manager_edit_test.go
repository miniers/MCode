package mcodeui

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
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
