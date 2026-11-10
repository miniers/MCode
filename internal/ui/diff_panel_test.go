package mcodeui

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

func TestDiffPanel(t *testing.T) {
	fake := NewFakeClient()

	unifiedDiff := `diff --git a/pkg/math.go b/pkg/math.go
--- a/pkg/math.go
+++ b/pkg/math.go
@@ -1,3 +1,3 @@
 func Add(a, b int) int {
-	return a - b
+	return a + b
 }
diff --git a/pkg/new.go b/pkg/new.go
new file mode 100644
--- /dev/null
+++ b/pkg/new.go
@@ -0,0 +1,2 @@
+package pkg
+var NewVal = 1
`

	fake.SetThreadDiff("t1", "", ThreadDiff{
		Diff:         unifiedDiff,
		FilesChanged: []string{"pkg/math.go", "pkg/new.go"},
	})

	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	// 1. 按 ⌘D 打开改动面板，显示“改动”标题、两个文件名与 +N/-N
	tt.Key(ui.Cmd, ui.KeyD)

	waitFor(t, tt, func() bool {
		return tt.HasText("改动") &&
			tt.HasText("pkg/math.go") &&
			tt.HasText("pkg/new.go") &&
			tt.HasText("+1") &&
			tt.HasText("-1") &&
			tt.HasText("+2")
	})
	tt.Advance(DurSheet + 50*time.Millisecond)

	// 验证差异行内容可见
	if !tt.HasText("return a - b") {
		t.Fatalf("expected diff line 'return a - b' to be visible, got texts: %q", tt.Texts())
	}
	if !tt.HasText("package pkg") {
		t.Fatalf("expected diff line 'package pkg' to be visible, got texts: %q", tt.Texts())
	}

	// 2. 点击文件标题折叠，行内容消失
	if err := tt.Click("pkg/math.go"); err != nil {
		t.Fatalf("click 'pkg/math.go' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return !tt.HasText("return a - b")
	})

	// 另一个未折叠的文件行内容依然可见
	if !tt.HasText("package pkg") {
		t.Fatalf("expected 'package pkg' to remain visible")
	}

	// 3. FailureReason 非空时显示该文案
	reasonText := "工作区无法获取 git 差异"
	fake.SetThreadDiff("t1", "", ThreadDiff{
		FailureReason: reasonText,
	})

	if err := tt.Click("刷新"); err != nil {
		t.Fatalf("click '刷新' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText(reasonText)
	})

	// 4. 空 diff 时显示无改动提示
	fake.SetThreadDiff("t1", "", ThreadDiff{})

	if err := tt.Click("刷新"); err != nil {
		t.Fatalf("click '刷新' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText(diffEmptyTitle) && !tt.HasText(reasonText)
	})

	// 5. 再按 ⌘D 面板关闭
	tt.Key(ui.Cmd, ui.KeyD)

	waitFor(t, tt, func() bool {
		return !tt.HasText(diffEmptyTitle) && !tt.HasText("pkg/new.go")
	})
}

// TestDiffPanelViewState 覆盖五种主视图状态：加载、错误、非 Git 工作区、空态与有文件。
func TestDiffPanelViewState(t *testing.T) {
	tests := []struct {
		name      string
		loading   bool
		err       string
		fileCount int
		want      string
	}{
		{name: "loading", loading: true, err: "", fileCount: 0, want: "loading"},
		{name: "loading with files", loading: true, err: "", fileCount: 2, want: "files"},
		{name: "empty", loading: false, err: "", fileCount: 0, want: "empty"},
		{name: "files", loading: false, err: "", fileCount: 3, want: "files"},
		{name: "error", loading: false, err: "解析 diff 失败: unexpected EOF", fileCount: 0, want: "error"},
		{name: "error with stale files", loading: false, err: "rpc timeout", fileCount: 1, want: "error"},
		{name: "not git english", loading: false, err: "fatal: not a git repository (or any of the parent directories): .git", fileCount: 0, want: "not-git"},
		{name: "not git english upper", loading: false, err: "FATAL: Not A Git Repository", fileCount: 0, want: "not-git"},
		{name: "not git chinese", loading: false, err: "当前目录不是 Git 仓库", fileCount: 0, want: "not-git"},
		{name: "not git short chinese", loading: false, err: "非 Git 目录", fileCount: 0, want: "not-git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := diffPanelViewState(tt.loading, tt.err, tt.fileCount); got != tt.want {
				t.Fatalf("diffPanelViewState(%v, %q, %d) = %q, want %q", tt.loading, tt.err, tt.fileCount, got, tt.want)
			}
		})
	}
}

// TestDiffPanel_EmptyState 覆盖空态文案常量与无改动文件时的图文空态卡片渲染分支。
func TestDiffPanel_EmptyState(t *testing.T) {
	// 1. 文案常量（标题 + 副标题）
	if diffEmptyTitle != "暂无代码变更" {
		t.Fatalf("diffEmptyTitle = %q, want 暂无代码变更", diffEmptyTitle)
	}
	if diffEmptySubtitle != "每轮对话后，Agent 改动的文件会显示在这里。" {
		t.Fatalf("diffEmptySubtitle = %q", diffEmptySubtitle)
	}

	// 2. 无改动文件时渲染图标 + 标题 + 副标题的空态卡片
	fake := NewFakeClient()
	fake.SetThreadDiff("t1", "", ThreadDiff{})

	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	tt.Key(ui.Cmd, ui.KeyD)

	waitFor(t, tt, func() bool {
		return tt.HasText(diffEmptyTitle) && tt.HasText(diffEmptySubtitle)
	})
}

func TestDiffPanel_HeaderButton(t *testing.T) {
	fake := NewFakeClient()
	unifiedDiff := `diff --git a/pkg/hello.go b/pkg/hello.go
new file mode 100644
--- /dev/null
+++ b/pkg/hello.go
@@ -0,0 +1,1 @@
+println("hello")
`
	fake.SetThreadDiff("t1", "", ThreadDiff{
		Diff:         unifiedDiff,
		FilesChanged: []string{"pkg/hello.go"},
	})

	tt := ui.NewTester(Root(fake), 1200, 700)
	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	// 点击头部“改动”图标按钮打开面板
	if err := tt.Click("改动"); err != nil {
		t.Fatalf("click '改动' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("pkg/hello.go") && tt.HasText("+1")
	})
	tt.Advance(DurSheet + 50*time.Millisecond)

	// 点击关闭按钮关闭面板
	if err := tt.Click("关闭"); err != nil {
		t.Fatalf("click '关闭' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return !tt.HasText("pkg/hello.go")
	})
}

// diffTestPatch 构造单文件、全为新增行的统一补丁，行数为 lines。
func diffTestPatch(path string, lines int) string {
	var b strings.Builder
	b.WriteString("diff --git a/" + path + " b/" + path + "\n")
	b.WriteString("new file mode 100644\n")
	b.WriteString("--- /dev/null\n")
	b.WriteString("+++ b/" + path + "\n")
	b.WriteString("@@ -0,0 +1," + strconv.Itoa(lines) + " @@\n")
	for i := 0; i < lines; i++ {
		b.WriteString("+line " + strconv.Itoa(i) + "\n")
	}
	return b.String()
}

// TestDiffPanel_LargeFileCollapsed 覆盖大文件默认折叠：超过 diffLargeFileLineThreshold
// 行的文件初始只显示文件头与折叠占位行，点开后展开，展开状态保留。
func TestDiffPanel_LargeFileCollapsed(t *testing.T) {
	const lines = diffLargeFileLineThreshold + 100
	patch := diffTestPatch("big/gen.go", lines)

	fake := NewFakeClient()
	fake.SetThreadDiff("t1", "", ThreadDiff{
		Diff:         patch,
		FilesChanged: []string{"big/gen.go"},
	})

	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})
	tt.Key(ui.Cmd, ui.KeyD)

	waitFor(t, tt, func() bool {
		return tt.HasText("big/gen.go")
	})
	tt.Advance(DurSheet + 50*time.Millisecond)

	// 1. 默认折叠：只有文件头与折叠占位行，代码行都不在渲染树里
	placeholder := "该文件改动 " + strconv.Itoa(lines) + " 行，已折叠；点击展开全部"
	if !tt.HasText(placeholder) {
		t.Fatalf("expected large-file placeholder %q, got texts: %q", placeholder, tt.Texts())
	}
	if tt.HasText("line 0") {
		t.Fatalf("expected large file to stay collapsed by default, got texts: %q", tt.Texts())
	}

	// 2. 点开占位行，视口内可见真实行内容
	if err := tt.Click(placeholder); err != nil {
		t.Fatalf("click large-file placeholder failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("line 0") && !tt.HasText(placeholder)
	})

	// 3. 虚拟化：视口外的行不渲染，滚动前不会出现在渲染树里
	if tt.HasText("line " + strconv.Itoa(lines-1)) {
		t.Fatalf("expected far-away row to stay out of the render tree")
	}

	// 4. 折叠后再展开：展开状态仍是用户显式展开的（不会被大文件默认折叠覆盖）
	if err := tt.Click("big/gen.go"); err != nil {
		t.Fatalf("click file header failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return !tt.HasText("line 0")
	})
	if err := tt.Click("big/gen.go"); err != nil {
		t.Fatalf("click file header again failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("line 0") && !tt.HasText(placeholder)
	})
}

// TestDiffPanel_DeferredFileExpand 覆盖延迟解析的大文件：先显示占位行，
// 点开后后台解析并把真实行内容渲染出来。
func TestDiffPanel_DeferredFileExpand(t *testing.T) {
	patch := diffTestPatch("big/deferred.go", 5100)

	files, err := git.ParseUnifiedDiff([]byte(patch))
	if err != nil {
		t.Fatalf("parse patch failed: %v", err)
	}
	if len(files) != 1 || !files[0].IsDeferred {
		t.Fatalf("expected deferred file diff, got %+v", files)
	}

	fake := NewFakeClient()
	fake.SetThreadDiff("t1", "", ThreadDiff{
		Diff:         patch,
		FilesChanged: []string{"big/deferred.go"},
	})

	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})
	tt.Key(ui.Cmd, ui.KeyD)

	placeholder := "文件内容较大，点击加载详细改动"
	waitFor(t, tt, func() bool {
		return tt.HasText("big/deferred.go") && tt.HasText(placeholder)
	})
	tt.Advance(DurSheet + 50*time.Millisecond)
	if tt.HasText("line 0") {
		t.Fatalf("expected deferred file rows to stay hidden before expanding")
	}

	// 重复点击只排队一次解析（第二次点击不应让内容闪退）
	if err := tt.Click(placeholder); err != nil {
		t.Fatalf("click deferred placeholder failed: %v", err)
	}
	_ = tt.Click(placeholder)

	waitFor(t, tt, func() bool {
		return tt.HasText("line 0") && !tt.HasText(placeholder)
	})
}

// diffTestMultiHunkPatch 构造多 hunk 的统一补丁：每个 hunk 都是纯新增行，
// 行文本带 hN- 前缀，便于断言具体哪一块的哪些行在行模型/渲染树里。
func diffTestMultiHunkPatch(path string, hunkSizes []int) string {
	var b strings.Builder
	b.WriteString("diff --git a/" + path + " b/" + path + "\n")
	b.WriteString("--- a/" + path + "\n")
	b.WriteString("+++ b/" + path + "\n")
	start := 1
	for hunkIdx, size := range hunkSizes {
		b.WriteString("@@ -" + strconv.Itoa(start) + "," + strconv.Itoa(size) +
			" +" + strconv.Itoa(start) + "," + strconv.Itoa(size) + " @@\n")
		for i := 0; i < size; i++ {
			b.WriteString("+h" + strconv.Itoa(hunkIdx) + "-line " + strconv.Itoa(i) + "\n")
		}
		start += size
	}
	return b.String()
}

// openDiffPanelWithPatch 用给定补丁打开改动面板，等首次加载落地后返回会话、测试器与面板状态。
func openDiffPanelWithPatch(t *testing.T, filePath, patch string) (*chatState, *ui.Tester, *diffPanelState) {
	t.Helper()
	fake := NewFakeClient()
	fake.SetThreadDiff("t1", "", ThreadDiff{
		Diff:         patch,
		FilesChanged: []string{filePath},
	})
	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	tt.Key(ui.Cmd, ui.KeyD)
	dp := getDiffPanelState(s)
	waitFor(t, tt, func() bool {
		diffPanelMu.Lock()
		defer diffPanelMu.Unlock()
		return dp.threadID == "t1" && !dp.loading && len(dp.files) > 0
	})
	waitFor(t, tt, func() bool { return tt.HasText(filePath) })
	tt.Advance(DurSheet + 50*time.Millisecond)
	return s, tt, dp
}

// setDiffTestWorktree 直接写入 AI 自建 worktree 的识别缓存，模拟「这一帧识别出来的是 path」。
// revs 与当前 messagesRev 对齐，避免被当成过期缓存又重新识别。
func setDiffTestWorktree(t *testing.T, s *chatState, threadID, path string) {
	t.Helper()
	st := getAIWorktreeState(s)
	s.mu.Lock()
	rev := s.messagesRev
	s.mu.Unlock()
	st.mu.Lock()
	// 让在途识别作废，避免它把注入的结果覆盖成空
	st.gens[threadID]++
	st.found[threadID] = aiWorktreeInfo{Path: path}
	st.revs[threadID] = rev
	st.mu.Unlock()
}

// diffPanelRowsSnapshot 复制当前行模型，断言时不必持锁。
func diffPanelRowsSnapshot(dp *diffPanelState) []diffDisplayRow {
	diffPanelMu.Lock()
	defer diffPanelMu.Unlock()
	return append([]diffDisplayRow(nil), dp.rows...)
}

// countDiffHunkLineRows 数某个 hunk 在行模型里铺开的代码行数。
func countDiffHunkLineRows(rows []diffDisplayRow, hunkIdx int) int {
	n := 0
	for _, row := range rows {
		if row.Kind == diffRowLine && row.ID.hunk == hunkIdx {
			n++
		}
	}
	return n
}

// hasDiffHunkMoreRow 判断行模型里是否还有某个 hunk 的「展开此块」占位行。
func hasDiffHunkMoreRow(rows []diffDisplayRow, hunkIdx int) bool {
	for _, row := range rows {
		if row.Kind == diffRowHunkMore && row.ID.hunk == hunkIdx {
			return true
		}
	}
	return false
}

// scrollDiffPanelTo 在改动面板里向下滚动，直到 text 进入渲染树。
// scrollDiffPanelTo 在改动面板里向下滚动直到文本出现。
func scrollDiffPanelTo(t *testing.T, tt *ui.Tester, text string) {
	t.Helper()
	for i := 0; i < 60; i++ {
		if tt.HasText(text) {
			return
		}
		tt.Scroll(800, 400, 0, 120)
		tt.Frame()
	}
	t.Fatalf("滚动后仍未看到 %q，当前文本：%q", text, tt.Texts())
}

// clickDiffPanelText 滚动到文本可点击后再点击：HasText 只说明元素在树里，
// 虚拟列表里的元素可能仍落在窗口下沿之外，直接点击会报 outside the window。
func clickDiffPanelText(t *testing.T, tt *ui.Tester, text string) {
	t.Helper()
	scrollDiffPanelTo(t, tt, text)
	var err error
	for i := 0; i < 10; i++ {
		if err = tt.Click(text); err == nil {
			return
		}
		tt.Scroll(800, 400, 0, 120)
		tt.Frame()
	}
	t.Fatalf("click %q failed: %v", text, err)
}

// TestDiffWorktreeResolvedNoReload 覆盖 worktree 识别抖动：识别出的路径在空串与同一路径之间
// 交替时不再重复取数，已加载的 worktree 归属保持不变。
func TestDiffWorktreeResolvedNoReload(t *testing.T) {
	s, tt, dp := openDiffPanelWithPatch(t, "pkg/wt.go", diffTestPatch("pkg/wt.go", 5))
	fake := s.client.(*FakeClient)
	worktree := filepath.Join(t.TempDir(), "ai-worktree")

	// 首次从「没有 worktree」变成「有 worktree」：只重取一次改动并记已确认
	before := len(fake.DiffCalls())
	setDiffTestWorktree(t, s, "t1", worktree)
	waitFor(t, tt, func() bool {
		diffPanelMu.Lock()
		defer diffPanelMu.Unlock()
		return !dp.loading && dp.worktreePath == worktree && dp.worktreeResolved
	})
	if got := len(fake.DiffCalls()); got != before+1 {
		t.Fatalf("首次识别出 worktree 应只重取一次改动：Diff 调用 %d → %d", before, got)
	}

	settled := len(fake.DiffCalls())
	for i := 0; i < 60; i++ {
		if i%2 == 0 {
			setDiffTestWorktree(t, s, "t1", worktree)
		} else {
			setDiffTestWorktree(t, s, "t1", "")
		}
		tt.Frame()
	}
	time.Sleep(30 * time.Millisecond)
	tt.Frame()
	if got := len(fake.DiffCalls()); got != settled {
		t.Fatalf("worktree 识别短暂为空时不该重取改动：Diff 调用 %d → %d", settled, got)
	}
	diffPanelMu.Lock()
	gotPath := dp.worktreePath
	diffPanelMu.Unlock()
	if gotPath != worktree {
		t.Fatalf("识别短暂为空后应保留已加载的 worktree 归属，got %q，want %q", gotPath, worktree)
	}
}

// TestDiffWorktreePathChangeReloadsOnce 覆盖 worktree 路径换成另一个非空值：只重取一次。
func TestDiffWorktreePathChangeReloadsOnce(t *testing.T) {
	s, tt, dp := openDiffPanelWithPatch(t, "pkg/wt.go", diffTestPatch("pkg/wt.go", 5))
	fake := s.client.(*FakeClient)
	dir := t.TempDir()
	first := filepath.Join(dir, "wt-one")
	second := filepath.Join(dir, "wt-two")

	setDiffTestWorktree(t, s, "t1", first)
	waitFor(t, tt, func() bool {
		diffPanelMu.Lock()
		defer diffPanelMu.Unlock()
		return !dp.loading && dp.worktreePath == first
	})
	settled := len(fake.DiffCalls())

	setDiffTestWorktree(t, s, "t1", second)
	waitFor(t, tt, func() bool {
		diffPanelMu.Lock()
		defer diffPanelMu.Unlock()
		return !dp.loading && dp.worktreePath == second
	})
	if got := len(fake.DiffCalls()); got != settled+1 {
		t.Fatalf("worktree 路径变成另一个非空值时应只重取一次：Diff 调用 %d → %d", settled, got)
	}

	for i := 0; i < 30; i++ {
		setDiffTestWorktree(t, s, "t1", second)
		tt.Frame()
	}
	time.Sleep(30 * time.Millisecond)
	tt.Frame()
	if got := len(fake.DiffCalls()); got != settled+1 {
		t.Fatalf("worktree 路径稳定后不该继续重取：Diff 调用 %d → %d", settled+1, got)
	}
}

// TestDiffPanel_LargeFileHunksOnly 覆盖大文件「只显示改动块」：每个 hunk 只铺开前
// diffHunkPreviewLines 行加一条「展开此块」占位行，整文件的折叠行数提示仍在。
func TestDiffPanel_LargeFileHunksOnly(t *testing.T) {
	const path = "big/multi.go"
	hunkSizes := []int{1200, 900}
	patch := diffTestMultiHunkPatch(path, hunkSizes)
	_, tt, dp := openDiffPanelWithPatch(t, path, patch)

	hunksOnlyLabel := "只显示改动块（每块先显示前 " + strconv.Itoa(diffHunkPreviewLines) + " 行）"
	waitFor(t, tt, func() bool { return tt.HasText(hunksOnlyLabel) })
	if err := tt.Click(hunksOnlyLabel); err != nil {
		t.Fatalf("click %q failed: %v", hunksOnlyLabel, err)
	}

	waitFor(t, tt, func() bool {
		rows := diffPanelRowsSnapshot(dp)
		return countDiffHunkLineRows(rows, 0) == diffHunkPreviewLines &&
			countDiffHunkLineRows(rows, 1) == diffHunkPreviewLines &&
			hasDiffHunkMoreRow(rows, 0) && hasDiffHunkMoreRow(rows, 1)
	})

	// 文件头/整文件占位行保留总行数提示，「展开全部」仍可用
	collapsedText := "该文件改动 " + strconv.Itoa(hunkSizes[0]+hunkSizes[1]) + " 行，已折叠；点击展开全部"
	if !tt.HasText(collapsedText) {
		t.Fatalf("「只显示改动块」后应保留已折叠行数提示 %q，当前文本：%q", collapsedText, tt.Texts())
	}
	// 预览之外的行走不到：第一块第 41 行（下标 40）没有渲染
	if tt.HasText("h0-line " + strconv.Itoa(diffHunkPreviewLines)) {
		t.Fatalf("未展开时不该看到 hunk 内第 %d 行之后的改动", diffHunkPreviewLines)
	}
}

// TestDiffPanel_LargeFileHunkExpand 覆盖按块展开：点开某一块只摊平该块，
// 其它块保持「前 N 行 + 展开占位」。
func TestDiffPanel_LargeFileHunkExpand(t *testing.T) {
	const path = "big/multi.go"
	hunkSizes := []int{1200, 900}
	patch := diffTestMultiHunkPatch(path, hunkSizes)
	_, tt, dp := openDiffPanelWithPatch(t, path, patch)

	hunksOnlyLabel := "只显示改动块（每块先显示前 " + strconv.Itoa(diffHunkPreviewLines) + " 行）"
	waitFor(t, tt, func() bool { return tt.HasText(hunksOnlyLabel) })
	if err := tt.Click(hunksOnlyLabel); err != nil {
		t.Fatalf("click %q failed: %v", hunksOnlyLabel, err)
	}

	firstHunkMore := "展开此块（共 " + strconv.Itoa(hunkSizes[0]) + " 行）"
	clickDiffPanelText(t, tt, firstHunkMore)

	waitFor(t, tt, func() bool {
		rows := diffPanelRowsSnapshot(dp)
		return countDiffHunkLineRows(rows, 0) == hunkSizes[0] &&
			countDiffHunkLineRows(rows, 1) == diffHunkPreviewLines &&
			!hasDiffHunkMoreRow(rows, 0) && hasDiffHunkMoreRow(rows, 1)
	})
	// 该块后续行进入行模型并可见（展开前 h0-line 40 根本不存在）
	scrollDiffPanelTo(t, tt, "h0-line "+strconv.Itoa(diffHunkPreviewLines))
}
