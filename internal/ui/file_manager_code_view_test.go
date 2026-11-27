package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// newTestFileManagerState 返回一份独立的文件管理面板状态，用来验证缓存按窗口隔离。
func newTestFileManagerState() *fileManagerState {
	return &fileManagerState{}
}

// 高亮透明度只按淡入进度缩放，不再与基础色的柔和透明度相乘。
func TestFileManagerLineHighlightColor(t *testing.T) {
	base := ui.Color{R: 10, G: 20, B: 30, A: 31}
	if got := fileManagerLineHighlightColor(base, 1); got != base {
		t.Fatalf("progress = 1 时颜色 = %+v，期望 %+v", got, base)
	}
	if got := fileManagerLineHighlightColor(base, 0); got.A != 0 {
		t.Fatalf("progress = 0 时 A = %d，期望 0", got.A)
	}
	if got := fileManagerLineHighlightColor(base, 2); got != base {
		t.Fatalf("progress = 2 时颜色 = %+v，期望 %+v", got, base)
	}
	half := fileManagerLineHighlightColor(base, 0.5)
	if diff := int(half.A) - int(base.A)/2; diff < -1 || diff > 1 {
		t.Fatalf("progress = 0.5 时 A = %d，期望约为 %d", half.A, base.A/2)
	}
}

// 同一个栏位、同一路径的内容没变时，复用同一个编辑器状态（保留选区与滚动位置）。
func TestFileManagerCodeViewReusesState(t *testing.T) {
	st := newTestFileManagerState()
	first := fileManagerCodeViewFor(st, "file-manager-pane-0-a.go", "a.go", "package a\n", 13)
	second := fileManagerCodeViewFor(st, "file-manager-pane-0-a.go", "a.go", "package a\n", 13)
	if first != second {
		t.Fatal("内容没变时应复用同一个编辑器状态")
	}
	if got := second.Buffer.String(); got != "package a\n" {
		t.Fatalf("缓冲区文本 = %q，期望 %q", got, "package a\n")
	}
	if !second.ReadOnly {
		t.Fatal("预览编辑器应为只读")
	}
	if second.LineNumbers != editor.LineNumbersOn {
		t.Fatalf("LineNumbers = %v，期望显示行号", second.LineNumbers)
	}
	if second.Label != "a.go" {
		t.Fatalf("Label = %q，期望文件名 a.go", second.Label)
	}
	if second.Font.Size != 13 {
		t.Fatalf("字号 = %v，期望 13", second.Font.Size)
	}
}

// 两栏各自独立：同一路径在不同栏位前缀下是两个状态。
func TestFileManagerCodeViewPerPane(t *testing.T) {
	st := newTestFileManagerState()
	left := fileManagerCodeViewFor(st, "file-manager-pane-0-a.go", "a.go", "package a\n", 13)
	right := fileManagerCodeViewFor(st, "file-manager-pane-1-a.go", "a.go", "package a\n", 13)
	if left == right {
		t.Fatal("两栏应各自缓存，不应共用同一个编辑器状态")
	}
}

// 不同窗口（不同 fileManagerState）各自缓存：同一 key 拿到不同状态，prune 互不影响。
func TestFileManagerCodeViewIsolatedPerState(t *testing.T) {
	first := newTestFileManagerState()
	second := newTestFileManagerState()

	firstState := fileManagerCodeViewFor(first, "file-manager-pane-0-a.go", "a.go", "package a\n", 13)
	secondState := fileManagerCodeViewFor(second, "file-manager-pane-0-a.go", "a.go", "package a\n", 13)
	if firstState == secondState {
		t.Fatal("不同窗口应各自缓存，不应共用同一个编辑器状态")
	}

	// 第一个窗口关掉 tab 后只清自己的缓存，另一个窗口的缓存与状态都不受影响。
	pruneFileManagerCodeViews(first, map[string]bool{})
	if len(first.codeViews) != 0 {
		t.Fatalf("第一个窗口的缓存应被清空，剩 %d 项", len(first.codeViews))
	}
	if _, ok := second.codeViews["file-manager-pane-0-a.go"]; !ok {
		t.Fatal("清理一个窗口不应删掉另一个窗口的缓存项")
	}
	if got := fileManagerCodeViewFor(second, "file-manager-pane-0-a.go", "a.go", "package a\n", 13); got != secondState {
		t.Fatal("另一个窗口的编辑器状态应保持不变")
	}
}

// 内容变化后缓冲区换成最新文本，但编辑器状态仍然复用。
func TestFileManagerCodeViewUpdatesContent(t *testing.T) {
	st := newTestFileManagerState()
	first := fileManagerCodeViewFor(st, "file-manager-pane-0-a.go", "a.go", "package a\n", 13)
	second := fileManagerCodeViewFor(st, "file-manager-pane-0-a.go", "a.go", "package b\n", 16)
	if first != second {
		t.Fatal("内容变化时应复用同一个编辑器状态")
	}
	if got := second.Buffer.String(); got != "package b\n" {
		t.Fatalf("缓冲区文本 = %q，期望 %q", got, "package b\n")
	}
	if second.Font.Size != 16 {
		t.Fatalf("字号 = %v，期望跟随设置更新为 16", second.Font.Size)
	}
}

// 「加载更多」追加的内容以旧内容为前缀：增量插入到末尾，State 指针不变，也不标成已修改。
func TestFileManagerCodeViewAppendsContent(t *testing.T) {
	st := newTestFileManagerState()
	const oldContent = "package a\n"
	const newContent = "package a\n\nfunc main() {}\n"

	first := fileManagerCodeViewFor(st, "file-manager-pane-0-a.go", "a.go", oldContent, 13)
	second := fileManagerCodeViewFor(st, "file-manager-pane-0-a.go", "a.go", newContent, 13)
	if first != second {
		t.Fatal("追加内容时应复用同一个编辑器状态")
	}
	if got := second.Buffer.String(); got != newContent {
		t.Fatalf("缓冲区文本 = %q，期望 %q", got, newContent)
	}
	if second.Buffer.Modified() {
		t.Fatal("追加内容后不应把缓冲区标成已修改")
	}
	// 增量插入会留下一步撤销记录（Reset 会清空历史），可据此确认没有走 Reset。
	if !second.Buffer.CanUndo() {
		t.Fatal("追加内容应走增量插入，撤销历史不应被清空")
	}
}

// 新建窗口状态时缓存按需懒创建。
func TestFileManagerCodeViewLazyCache(t *testing.T) {
	st := newTestFileManagerState()
	if st.codeViews != nil {
		t.Fatal("新建状态不应预分配缓存")
	}
	fileManagerCodeViewFor(st, "file-manager-pane-0-a.go", "a.go", "package a\n", 13)
	if len(st.codeViews) != 1 {
		t.Fatalf("缓存项数 = %d，期望 1", len(st.codeViews))
	}
}

// st 为 nil 时退化为每帧新建临时状态，不缓存也不报错。
func TestFileManagerCodeViewWithoutState(t *testing.T) {
	first := fileManagerCodeViewFor(nil, "file-manager-pane-0-a.go", "a.go", "package a\n", 13)
	second := fileManagerCodeViewFor(nil, "file-manager-pane-0-a.go", "a.go", "package a\n", 13)
	if first == second {
		t.Fatal("没有窗口状态时不应缓存编辑器状态")
	}
	if got := second.Buffer.String(); got != "package a\n" {
		t.Fatalf("缓冲区文本 = %q，期望 %q", got, "package a\n")
	}
}

// prune 删除不在 live 集合里的缓存项，保留仍要渲染的项。
func TestFileManagerCodeViewPrune(t *testing.T) {
	st := newTestFileManagerState()
	kept := fileManagerCodeViewFor(st, "file-manager-pane-0-a.go", "a.go", "package a\n", 13)
	fileManagerCodeViewFor(st, "file-manager-pane-1-b.txt", "b.txt", "b\n", 13)

	pruneFileManagerCodeViews(st, map[string]bool{"file-manager-pane-0-a.go": true})

	if _, ok := st.codeViews["file-manager-pane-0-a.go"]; !ok {
		t.Fatal("存活项不应被删除")
	}
	if _, ok := st.codeViews["file-manager-pane-1-b.txt"]; ok {
		t.Fatal("不在 live 集合里的缓存项应被删除")
	}
	if got := fileManagerCodeViewFor(st, "file-manager-pane-0-a.go", "a.go", "package a\n", 13); got != kept {
		t.Fatal("存活项删除后重建会丢掉状态，期望仍复用原状态")
	}
}

// .go 文件按文件名识别出 Go 语言，用于语法高亮。
func TestFileManagerCodeViewLanguageForGo(t *testing.T) {
	st := newTestFileManagerState()
	state := fileManagerCodeViewFor(st, "file-manager-pane-0-a.go", "a.go", "package a\n", 13)
	if state.Buffer.Language() == nil {
		t.Fatal(".go 文件应识别出语言")
	}
}

// 未知扩展名不设语言，按纯文本显示。
func TestFileManagerCodeViewUnknownLanguage(t *testing.T) {
	st := newTestFileManagerState()
	state := fileManagerCodeViewFor(st, "file-manager-pane-0-a.qqq", "a.qqq", "plain\n", 13)
	if state.Buffer.Language() != nil {
		t.Fatalf("未知扩展名不应识别语言，得到 %v", state.Buffer.Language())
	}
}

func TestFileManagerCodeRevealBounded(t *testing.T) {
	s := newChatState(nil)
	defer s.close()
	tab := fileManagerTabSnapshot{
		path:      "a.go",
		kind:      "text",
		content:   "package a\nfunc A() {}\n",
		hlFirst:   2,
		hlLast:    2,
		reveal:    true,
		revealSeq: 1,
	}
	before := s.invalidateGen.Load()
	tt := ui.NewTester(func(c *ui.Context) {
		s.setUIServices(c.Services())
		ui.Box(c).Width(400).Height(0).Children(func() {
			renderFileManagerCodePreview(c, s, tab, "file-manager-pane-0-")
		})
	}, 400, 300)

	for range 10 {
		tt.Frame()
	}
	got := s.invalidateGen.Load() - before
	if got <= 0 || got > fileManagerRevealMaxRetries {
		t.Fatalf("视口高度为 0 时同一 revealSeq 触发重绘 %d 次，期望 1..%d", got, fileManagerRevealMaxRetries)
	}

	// 新 revealSeq 允许再次重试，同样不超过上限。
	tab.revealSeq = 2
	before = s.invalidateGen.Load()
	for range 10 {
		tt.Frame()
	}
	got2 := s.invalidateGen.Load() - before
	if got2 <= 0 || got2 > fileManagerRevealMaxRetries {
		t.Fatalf("新 revealSeq 触发重绘 %d 次，期望 1..%d", got2, fileManagerRevealMaxRetries)
	}
}

func TestFileManagerSaveUpdatesLineCount(t *testing.T) {
	fake, s, tt := openFileManagerForTest(t)
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "line1\nline2", TotalSize: 11},
	})
	openTestFileManagerFileExternal(t, s, "a.txt")
	waitForFileManagerText(t, tt, s, "a.txt", "line1\nline2")
	waitFor(t, tt, func() bool { return tt.HasText("11 B · 2 行") })

	st := getFileManagerState(s)
	var preview *fileManagerCodePreview
	waitFor(t, tt, func() bool {
		preview = st.codeViews["file-manager-pane-0-a.txt"]
		return preview != nil && preview.state != nil && preview.state.Buffer != nil
	})
	preview.state.Buffer.Edit(editor.Edit{Start: len("line1\nline2"), End: len("line1\nline2"), Text: "\nline3"})
	waitFor(t, tt, func() bool {
		_, hasSave := tt.Find("保存")
		return hasSave && preview.state.Buffer.Modified()
	})
	tt.Advance(DurBase + 50*time.Millisecond)

	if err := tt.Click("保存"); err != nil {
		t.Fatalf("点击保存失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return !preview.state.Buffer.Modified() && fileManagerTabText(s, "a.txt") == "line1\nline2\nline3"
	})
	waitFor(t, tt, func() bool { return tt.HasText("17 B · 3 行") })
}
