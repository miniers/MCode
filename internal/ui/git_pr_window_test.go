package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

func resetGitPRWindowStateForTest() {
	gitPRWindowMu.Lock()
	gitPRWindow = nil
	gitPRWindowOwner = nil
	gitPRWindowServices = ui.Services{}
	gitPRWindowClosing = false
	gitPRWindowMu.Unlock()
}

func stubGitPRWindowHooks(t *testing.T) (openCalls, closeCalls *int) {
	t.Helper()
	prevOpener := gitPRWindowOpener
	prevCloser := gitPRWindowCloser
	resetGitPRWindowStateForTest()
	var opens, closes int
	gitPRWindowOpener = func(*chatState) { opens++ }
	gitPRWindowCloser = func() { closes++ }
	t.Cleanup(func() {
		gitPRWindowOpener = prevOpener
		gitPRWindowCloser = prevCloser
		resetGitPRWindowStateForTest()
	})
	return &opens, &closes
}

// TestGitPRWindowRenderAndWidth 覆盖独立 PR 窗口渲染列表与详情、无 DialogBase 遮罩且内容宽度跟随窗口宽度。
func TestGitPRWindowRenderAndWidth(t *testing.T) {
	_, closeCalls := stubGitPRWindowHooks(t)
	f := &gitPRDialogFake{
		FakeClient: NewFakeClient(),
		prs: []git.PRSummary{
			{Number: 7, Title: "第一条 PR", Author: "alice", HeadBranch: "feat/a", BaseBranch: "main", State: "open"},
		},
		detail: git.PRDetail{
			PRSummary: git.PRSummary{Number: 7, Title: "第一条 PR", Author: "alice", HeadBranch: "feat/a", BaseBranch: "main", State: "open"},
			Body:      "这是 PR 详情正文",
			Mergeable: "MERGEABLE",
		},
	}
	s := newGitPRDialogTestState(t, f)
	s.openGitPRDialog()
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool {
		return st.selected == 7 && st.detail != nil && !st.detailLoading
	})

	ttNarrow := ui.NewTester(func(c *ui.Context) { renderGitPRWindow(c, s) }, 900, 720)
	ttNarrow.Advance(time.Second)

	ttWide := ui.NewTester(func(c *ui.Context) { renderGitPRWindow(c, s) }, 1300, 720)
	ttWide.Advance(time.Second)

	for _, text := range []string{"Pull Request", "第一条 PR", "这是 PR 详情正文", "可以合并"} {
		if !ttWide.HasText(text) {
			t.Fatalf("PR 窗口缺少文案 %q", text)
		}
	}
	if _, ok := ttWide.Find("PR 弹窗卡片"); ok {
		t.Fatal("PR 窗口不应再包含弹窗卡片")
	}
	if _, ok := ttWide.Access("关闭"); ok {
		t.Fatal("PR 窗口标题栏不应包含应用内关闭按钮")
	}
	if ttWide.FramePending() {
		t.Fatal("PR 窗口渲染静止后仍在请求下一帧")
	}

	narrowRoot, ok := ttNarrow.Find("PR 窗口")
	if !ok || narrowRoot.W != 900 || narrowRoot.H != 720 {
		t.Fatalf("窄窗口根视图尺寸 = (%+v, ok=%v)，期望 900×720", narrowRoot, ok)
	}
	wideRoot, ok := ttWide.Find("PR 窗口")
	if !ok || wideRoot.W != 1300 || wideRoot.H != 720 {
		t.Fatalf("宽窗口根视图尺寸 = (%+v, ok=%v)，期望 1300×720", wideRoot, ok)
	}
	narrowBody, ok := ttNarrow.Find("PR 内容区")
	if !ok {
		t.Fatal("找不到窄窗口 PR 内容区")
	}
	wideBody, ok := ttWide.Find("PR 内容区")
	if !ok {
		t.Fatal("找不到宽窗口 PR 内容区")
	}
	if wantNarrow, wantWide := 900-2*Space5, 1300-2*Space5; narrowBody.W != wantNarrow || wideBody.W != wantWide {
		t.Fatalf("PR 内容区宽度 = (%v, %v)，期望跟随窗口宽度 (%v, %v)", narrowBody.W, wideBody.W, wantNarrow, wantWide)
	}

	// 没有 DialogBase 遮罩：点击窗口角落不会关闭窗口。
	ttWide.ClickAt(1, 1)
	ttWide.Frame()
	st := getGitPRDialogState(s)
	st.mu.Lock()
	openAfterClick := st.open
	st.mu.Unlock()
	if !openAfterClick || *closeCalls != 0 {
		t.Fatalf("点击窗口角落不应触发遮罩关闭：open=%v closeCalls=%d", openAfterClick, *closeCalls)
	}

	// Esc 仍关闭 PR 窗口。
	ttWide.Key(0, ui.KeyEscape)
	ttWide.Frame()
	st.mu.Lock()
	openAfterEsc := st.open
	st.mu.Unlock()
	if openAfterEsc || *closeCalls != 1 {
		t.Fatalf("按 Esc 后状态 open=%v closeCalls=%d，期望 (false, 1)", openAfterEsc, *closeCalls)
	}
}

// TestGitPRWindowClosesOnThreadChange 覆盖主窗口切换会话后同步检查会关闭 PR 状态与窗口。
func TestGitPRWindowClosesOnThreadChange(t *testing.T) {
	openCalls, closeCalls := stubGitPRWindowHooks(t)
	s := newGitPRDialogTestState(t, &gitPRDialogFake{FakeClient: NewFakeClient()})
	s.openGitPRDialog()
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool { return !st.listLoading })

	if *openCalls != 1 || *closeCalls != 0 {
		t.Fatalf("打开后调用次数 = (open=%d, close=%d)，期望 (1, 0)", *openCalls, *closeCalls)
	}

	// 模拟主窗口切换到新会话 thread-2。
	s.mu.Lock()
	s.selectedThreadID = "thread-2"
	s.threads = append(s.threads, Thread{ID: "thread-2", ProjectID: "p1"})
	s.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		syncChatHeaderGit(c, s, "thread-2")
	}, 800, 120)
	tt.Frame()

	st := getGitPRDialogState(s)
	st.mu.Lock()
	open := st.open
	st.mu.Unlock()
	if open {
		t.Fatal("主窗口切换会话后 PR 状态应关闭")
	}
	if *closeCalls != 1 {
		t.Fatalf("主窗口切换会话后关闭窗口桩调用次数 = %d，期望 1", *closeCalls)
	}
}

// TestGitPRWindowOpenCloseAndOnClosedNoReentry 覆盖打开/关闭桩调用以及 OnClosed 路径不重入。
func TestGitPRWindowOpenCloseAndOnClosedNoReentry(t *testing.T) {
	openCalls, closeCalls := stubGitPRWindowHooks(t)
	s := newGitPRDialogTestState(t, &gitPRDialogFake{FakeClient: NewFakeClient()})

	s.openGitPRDialog()
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool { return !st.listLoading })
	if *openCalls != 1 || *closeCalls != 0 {
		t.Fatalf("openGitPRDialog 后调用次数 = (open=%d, close=%d)，期望 (1, 0)", *openCalls, *closeCalls)
	}

	s.closeGitPRDialog()
	if *closeCalls != 1 {
		t.Fatalf("closeGitPRDialog 后关闭桩调用次数 = %d，期望 1", *closeCalls)
	}

	// 再次打开并模拟系统窗口触发 OnClosed：应清空状态与包级句柄，且不重入 closeGitPRWindow。
	s.openGitPRDialog()
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool { return !st.listLoading })
	if *openCalls != 2 {
		t.Fatalf("第二次 openGitPRDialog 后打开桩调用次数 = %d，期望 2", *openCalls)
	}

	dummyWin := &mygo.Window{}
	gitPRWindowMu.Lock()
	gitPRWindow = dummyWin
	gitPRWindowOwner = s
	gitPRWindowMu.Unlock()

	handleGitPRWindowClosed(dummyWin, s)

	st := getGitPRDialogState(s)
	st.mu.Lock()
	open := st.open
	st.mu.Unlock()
	if open {
		t.Fatal("OnClosed 后 PR 对话框状态应关闭")
	}
	if *closeCalls != 1 {
		t.Fatalf("OnClosed 路径不应重入调用关闭窗口桩：closeCalls = %d，期望保持 1", *closeCalls)
	}
	gitPRWindowMu.Lock()
	remWin, remOwner, closing := gitPRWindow, gitPRWindowOwner, gitPRWindowClosing
	gitPRWindowMu.Unlock()
	if remWin != nil || remOwner != nil || closing {
		t.Fatalf("OnClosed 后包级状态未清理干净: win=%v owner=%v closing=%v", remWin, remOwner, closing)
	}

	// 重复触发同一窗口的 OnClosed 也应安全忽略。
	handleGitPRWindowClosed(dummyWin, s)
	if *closeCalls != 1 {
		t.Fatalf("重复 OnClosed 不应触发关闭桩：closeCalls = %d", *closeCalls)
	}
}
