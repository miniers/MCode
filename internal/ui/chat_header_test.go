package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestChatHeaderRenameOnBlur(t *testing.T) {
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	startHeaderTitleEdit(t, tt)
	tt.Type("失焦标题")
	if err := tt.Click("显示/隐藏侧边栏 ⌘B"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		calls := renameCallsOf(fake)
		return len(calls) == 1 && calls[0].Title == "失焦标题"
	})
}

func TestChatHeaderRenameOnThreadSwitch(t *testing.T) {
	fake := NewFakeClient()
	fake.threads["p1"] = append(fake.threads["p1"], Thread{ID: "t2", ProjectID: "p1", Title: "第二个会话"})
	s, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	startHeaderTitleEdit(t, tt)
	tt.Type("切换前的标题")
	s.selectThread("t2")
	waitFor(t, tt, func() bool {
		calls := renameCallsOf(fake)
		return len(calls) == 1 && calls[0].ThreadID == "t1" && calls[0].Title == "切换前的标题"
	})
}

func TestChatHeaderGitRefreshOnOpen(t *testing.T) {
	fake := newGitFakeClient()
	stubGitFormWindow(t)
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
	fake.mu.Lock()
	fake.gitState.Changes = 3
	fake.gitState.Branches = append(fake.gitState.Branches, "new-branch")
	fake.mu.Unlock()
	if err := tt.Click("Git 分支"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("切换分支…")
		return ok
	})
	// 弹层打开时重新拉取 Git 状态：切换分支窗口的分支列表能看到新分支
	if err := tt.Click("切换分支…"); err != nil {
		t.Fatal(err)
	}
	f := gitFormOf("t1", gitFormCheckout)
	if f == nil {
		t.Fatal("切换分支窗口没有打开")
	}
	wt := gitFormTester(t, f)
	waitFor(t, wt, func() bool { return wt.HasText("new-branch") })

	if err := tt.Click("提交与推送"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("已更改 3 个文件") })
}

func TestChatHeaderGitRefreshAfterTurn(t *testing.T) {
	fake := newGitFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
	fake.TriggerRunningUpdate("t1", true)
	fake.mu.Lock()
	fake.gitState.Branch = "agent-branch"
	fake.mu.Unlock()
	fake.TriggerRunningUpdate("t1", false)
	waitFor(t, tt, func() bool { return tt.HasText("agent-branch") })
}

func TestChatHeaderGitLongMenuScrolls(t *testing.T) {
	fake := newGitFakeClient()
	for i := 0; i < 50; i++ {
		fake.gitState.Branches = append(fake.gitState.Branches, fmt.Sprintf("branch-%02d", i))
	}
	stubGitFormWindow(t)
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
	if err := tt.Click("Git 分支"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("切换分支…")
		return ok
	})
	if err := tt.Click("切换分支…"); err != nil {
		t.Fatal(err)
	}
	f := gitFormOf("t1", gitFormCheckout)
	if f == nil {
		t.Fatal("切换分支窗口没有打开")
	}
	wt := gitFormTester(t, f)
	waitFor(t, wt, func() bool { return wt.HasText("branch-00") })

	// 分支列表在窗口内滚动：滚到底后最后一个分支可见并能选中
	r, ok := wt.Find("branch-01")
	if !ok {
		t.Fatalf("窗口里找不到分支项: %q", wt.Texts())
	}
	wt.Scroll(r.X+r.W/2, r.Y+r.H/2, 0, 100000)
	wt.Frame()
	r, ok = wt.Find("branch-49")
	_, height := f.windowSize()
	if !ok || r.Y < 0 || int(r.Y+r.H) > height {
		t.Fatalf("滚动后最后一个分支仍不在窗口内: %+v", r)
	}
	if err := wt.Click("branch-49"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.gitCheckoutCalls) == 1 && fake.gitCheckoutCalls[0] == "branch-49"
	})
}

type delayedHeaderCommitClient struct {
	*FakeClient
	started chan struct{}
	release chan struct{}
}

func (f delayedHeaderCommitClient) GitCommit(context.Context, string, string) error {
	close(f.started)
	<-f.release
	return errors.New("旧会话提交失败")
}

func TestChatHeaderGitResultStaysWithThread(t *testing.T) {
	fake := newGitFakeClient()
	fake.threads["p1"] = append(fake.threads["p1"], Thread{ID: "t2", ProjectID: "p1", Title: "第二个会话"})
	client := delayedHeaderCommitClient{fake, make(chan struct{}), make(chan struct{})}
	stubGitFormWindow(t)
	s, root := buildRoot(client)
	tt := ui.NewTester(root, 1000, 700)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(client.release) }) }
	defer release()
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
	if err := tt.Click("提交与推送"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("已更改 2 个文件") })
	if err := tt.Click("提交…"); err != nil {
		t.Fatal(err)
	}
	f := gitFormOf("t1", gitFormCommit)
	if f == nil {
		t.Fatal("提交窗口没有打开")
	}
	wt := gitFormTester(t, f)
	waitFor(t, wt, func() bool {
		_, ok := wt.Find("提交信息")
		return ok
	})
	if err := wt.Click("提交信息"); err != nil {
		t.Fatal(err)
	}
	wt.Type("旧会话提交")
	wt.Frame()
	if err := wt.Click("提交"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wt, func() bool {
		select {
		case <-client.started:
			return true
		default:
			return false
		}
	})

	// 切到另一个会话：旧会话的进行中提交不会污染新会话的头部状态
	s.selectThread("t2")
	waitFor(t, tt, func() bool {
		chatHeaderMu.Lock()
		defer chatHeaderMu.Unlock()
		return chatHeaderStates[s] != nil && chatHeaderStates[s].gitThreadID == "t2"
	})
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	busy := hs.commitBusy
	chatHeaderMu.Unlock()
	if busy != "" {
		t.Fatal("旧会话的提交状态污染了新会话")
	}
	release()

	// 提交失败只留在旧会话自己的窗口里，提交信息不丢
	waitFor(t, wt, func() bool { return wt.HasText("提交失败: 旧会话提交失败") })
	if got := f.commitMsg; got != "旧会话提交" {
		t.Fatalf("提交失败后窗口丢了提交信息: %q", got)
	}
	if got := gitFormOf("t1", gitFormCommit); got != f {
		t.Fatal("提交失败后窗口应保持打开")
	}
}

type failedHeaderClient struct{ *FakeClient }

func (failedHeaderClient) RenameThread(string, string) error {
	return errors.New("重命名被拒绝")
}

func (failedHeaderClient) GitCommit(context.Context, string, string) error {
	return errors.New("提交被拒绝")
}

func TestChatHeaderRenameFailureRollsBack(t *testing.T) {
	s, root := buildRoot(failedHeaderClient{NewFakeClient()})
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	startHeaderTitleEdit(t, tt)
	tt.Type("失败的标题")
	tt.Key(0, ui.KeyEnter)
	hs := getChatHeaderState(s)
	waitFor(t, tt, func() bool {
		chatHeaderMu.Lock()
		idle := !hs.renameBusy
		chatHeaderMu.Unlock()
		return idle && tt.HasText("测试会话1") && !tt.HasText("失败的标题")
	})
}

func TestChatHeaderGitCommitFailureKeepsDraft(t *testing.T) {
	stubGitFormWindow(t)
	_, root := buildRoot(failedHeaderClient{newGitFakeClient()})
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
	if err := tt.Click("提交与推送"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("已更改 2 个文件") })
	if err := tt.Click("提交…"); err != nil {
		t.Fatal(err)
	}
	f := gitFormOf("t1", gitFormCommit)
	if f == nil {
		t.Fatal("提交窗口没有打开")
	}
	wt := gitFormTester(t, f)
	waitFor(t, wt, func() bool {
		_, ok := wt.Find("提交信息")
		return ok
	})
	if err := wt.Click("提交信息"); err != nil {
		t.Fatal(err)
	}
	wt.Type("保留草稿")
	wt.Frame()
	if err := wt.Click("提交"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wt, func() bool { return wt.HasText("提交失败: 提交被拒绝") })
	if got := f.commitMsg; got != "保留草稿" {
		t.Fatalf("提交失败后丢失草稿: %q", got)
	}
}

// editingHeaderState 返回正在编辑指定会话标题的头部状态，没有时返回 nil。
func editingHeaderState(threadID string) *chatHeaderState {
	chatHeaderMu.Lock()
	defer chatHeaderMu.Unlock()
	for _, hs := range chatHeaderStates {
		if hs.editing && hs.editThreadID == threadID {
			return hs
		}
	}
	return nil
}

// renameCallsOf 获取假 Client 收到的 RenameThread 调用。
func renameCallsOf(fake *FakeClient) []renameCall {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]renameCall(nil), fake.renameCalls...)
}

// newGitFakeClient 返回带 Git 状态的假 Client：当前在主工作树的 feature/login 分支，有 2 个未提交变更。
func newGitFakeClient() *FakeClient {
	fake := NewFakeClient()
	fake.gitState = GitState{
		WorkDir:  "/repo",
		Branch:   "feature/login",
		Head:     "abc1234",
		Branches: []string{"feature/login", "main"},
		Worktrees: []GitWorktree{
			{Path: "/repo", Branch: "feature/login", Current: true},
			{Path: "/repo-wt/hotfix", Branch: "hotfix", Current: false},
		},
		Changes: 2,
	}
	return fake
}

// stubGitFormWindow 让 Git 菜单里的窗口路径在测试里可用：用不放真窗口的桩代替开窗，
// 并在测试结束后清空注册表。
func stubGitFormWindow(t *testing.T) {
	t.Helper()
	orig := gitFormWindowHook
	gitFormWindowHook = func(*gitForm) {}
	t.Cleanup(func() {
		gitFormWindowHook = orig
		gitFormMu.Lock()
		for key := range gitFormWindows {
			delete(gitFormWindows, key)
		}
		gitFormMu.Unlock()
	})
}

// gitFormOf 返回指定会话与操作类型已打开的 Git 操作窗口。
func gitFormOf(threadID string, kind gitFormKind) *gitForm {
	gitFormMu.Lock()
	defer gitFormMu.Unlock()
	return gitFormWindows[gitFormKey{threadID: threadID, kind: kind}]
}

// gitFormTester 为窗口内容单独建一个 tester，尺寸用窗口自身的默认尺寸。
func gitFormTester(t *testing.T, f *gitForm) *ui.Tester {
	t.Helper()
	width, height := f.windowSize()
	return ui.NewTester(f.view, width, height)
}

// startHeaderTitleEdit 右键标题打开菜单并点击「重命名」进入编辑态，并把输入框清空。
func startHeaderTitleEdit(t *testing.T, tt *ui.Tester) *chatHeaderState {
	t.Helper()
	startHeaderTitleEditOf(t, tt, "测试会话1")
	return editingHeaderState("t1")
}

// startHeaderTitleEditOf 右键指定标题打开菜单并点击「重命名」进入编辑态。
func startHeaderTitleEditOf(t *testing.T, tt *ui.Tester, title string) {
	t.Helper()
	// 侧栏会话名先于异步选中完成出现，需等头部按钮就绪再操作。
	waitFor(t, tt, func() bool { return tt.HasText("重命名会话") })
	if err := tt.RightClick("重命名会话"); err != nil {
		t.Fatalf("right click title failed: %v", err)
	}
	if err := tt.ChooseMenuItem("重命名"); err != nil {
		t.Fatalf("choose rename in context menu failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("会话标题")
		return ok && editingHeaderState("t1") != nil
	})
	hs := editingHeaderState("t1")
	if hs.editTitle != title {
		t.Fatalf("title input not prefilled, got %q", hs.editTitle)
	}
	hs.editTitle = ""
	tt.Frame()
}

// 点标题、输入新标题、回车后 RenameThread 收到正确参数，界面乐观更新。
func TestChatHeaderRenameOnEnter(t *testing.T) {
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	startHeaderTitleEdit(t, tt)
	tt.Type("新标题")
	tt.Key(0, ui.KeyEnter)

	waitFor(t, tt, func() bool {
		calls := renameCallsOf(fake)
		return len(calls) == 1 && calls[0].ThreadID == "t1" && calls[0].Title == "新标题"
	})
	waitFor(t, tt, func() bool { return tt.HasText("新标题") })
	if _, ok := tt.Find("会话标题"); ok {
		t.Fatal("title input still visible after Enter")
	}
}

// 按 Esc 取消编辑，不调用 RenameThread。
func TestChatHeaderRenameEscapeCancels(t *testing.T) {
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	startHeaderTitleEdit(t, tt)
	tt.Type("不会提交")
	tt.Key(0, ui.KeyEscape)

	waitFor(t, tt, func() bool {
		_, ok := tt.Find("会话标题")
		return !ok
	})
	tt.Frame()
	if calls := renameCallsOf(fake); len(calls) != 0 {
		t.Fatalf("RenameThread called after Escape: %+v", calls)
	}
	if !tt.HasText("测试会话1") || tt.HasText("不会提交") {
		t.Fatalf("title changed after Escape, texts: %q", tt.Texts())
	}
}

// 胶囊显示分支名；「切换分支…」在独立窗口里选另一个分支，调用 GitCheckout 并刷新胶囊。
func TestChatHeaderGitCheckout(t *testing.T) {
	fake := newGitFakeClient()
	stubGitFormWindow(t)
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })

	if err := tt.Click("Git 分支"); err != nil {
		t.Fatalf("click branch capsule failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("切换分支…")
		return ok
	})
	if err := tt.Click("切换分支…"); err != nil {
		t.Fatalf("click checkout entry failed: %v", err)
	}
	f := gitFormOf("t1", gitFormCheckout)
	if f == nil {
		t.Fatal("切换分支窗口没有打开")
	}
	wt := gitFormTester(t, f)
	waitFor(t, wt, func() bool {
		_, ok := wt.Find("切换到分支 main")
		return ok
	})

	// 搜索框过滤分支列表
	if err := wt.Click("搜索分支"); err != nil {
		t.Fatalf("click branch search failed: %v", err)
	}
	wt.Type("mai")
	wt.Frame()
	if _, ok := wt.Find("切换到分支 feature/login"); ok {
		t.Fatalf("搜索应过滤掉不匹配的分支: %q", wt.Texts())
	}

	if err := wt.Click("切换到分支 main"); err != nil {
		t.Fatalf("click branch main failed: %v", err)
	}
	waitFor(t, wt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.gitCheckoutCalls) == 1 && fake.gitCheckoutCalls[0] == "main"
	})
	// 成功后窗口关闭并刷新胶囊
	waitFor(t, tt, func() bool { return tt.HasText("main") && !tt.HasText("feature/login") })
	if got := gitFormOf("t1", gitFormCheckout); got != nil {
		t.Fatal("切换成功后窗口应关闭并从注册表移除")
	}
}

// 在 Popover 里点另一个 worktree 调用 GitSwitchWorktree，胶囊附上 worktree 目录名。
func TestChatHeaderGitSwitchWorktree(t *testing.T) {
	fake := newGitFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })

	if err := tt.Click("Git 分支"); err != nil {
		t.Fatalf("click branch capsule failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("切换到 worktree hotfix")
		return ok
	})
	if err := tt.Click("切换到 worktree hotfix"); err != nil {
		t.Fatalf("click worktree failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.gitWorktreeCalls) == 1 && fake.gitWorktreeCalls[0] == "/repo-wt/hotfix"
	})
	waitFor(t, tt, func() bool { return tt.HasText("hotfix · hotfix") })
}

// 同一会话的同一操作重复点击时复用已打开的窗口，不再开第二个。
func TestGitFormReusesOpenWindow(t *testing.T) {
	fake := newGitFakeClient()
	stubGitFormWindow(t)
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })

	openNewBranch := func() {
		t.Helper()
		if err := tt.Click("Git 分支"); err != nil {
			t.Fatalf("click branch capsule failed: %v", err)
		}
		waitFor(t, tt, func() bool {
			_, ok := tt.Find("新建分支…")
			return ok
		})
		if err := tt.Click("新建分支…"); err != nil {
			t.Fatalf("click new branch entry failed: %v", err)
		}
	}

	openNewBranch()
	first := gitFormOf("t1", gitFormNewBranch)
	if first == nil {
		t.Fatal("新建分支窗口没有打开")
	}
	openNewBranch()
	if second := gitFormOf("t1", gitFormNewBranch); second != first {
		t.Fatal("重复点击新建了第二个窗口，应该聚焦已打开的窗口")
	}
}

// 新建分支窗口：填分支名点「创建分支」调用 GitCreateBranch，成功后关闭窗口并刷新胶囊。
func TestGitFormNewBranch(t *testing.T) {
	fake := newGitFakeClient()
	stubGitFormWindow(t)
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })

	if err := tt.Click("Git 分支"); err != nil {
		t.Fatalf("click branch capsule failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("新建分支…")
		return ok
	})
	if err := tt.Click("新建分支…"); err != nil {
		t.Fatalf("click new branch entry failed: %v", err)
	}
	f := gitFormOf("t1", gitFormNewBranch)
	if f == nil {
		t.Fatal("新建分支窗口没有打开")
	}
	wt := gitFormTester(t, f)
	waitFor(t, wt, func() bool {
		_, ok := wt.Find("新分支名")
		return ok
	})
	if err := wt.Click("新分支名"); err != nil {
		t.Fatalf("focus new branch input failed: %v", err)
	}
	wt.Type("feat/window")
	wt.Frame()
	if err := wt.Click("创建分支"); err != nil {
		t.Fatalf("click create branch failed: %v", err)
	}
	waitFor(t, wt, func() bool {
		calls := fake.GitCreateBranchCalls()
		return len(calls) == 1 && calls[0] == [2]string{"t1", "feat/window"}
	})
	// 成功后窗口关闭，胶囊刷新到新分支
	if got := gitFormOf("t1", gitFormNewBranch); got != nil {
		t.Fatal("新建分支成功后窗口应关闭并从注册表移除")
	}
	waitFor(t, tt, func() bool { return tt.HasText("feat/window") })
}

// 提交窗口里输入信息、点提交后 GitCommit 收到这条信息；菜单里点推送后 push 计数为 1。
func TestChatHeaderGitCommitAndPush(t *testing.T) {
	fake := newGitFakeClient()
	stubGitFormWindow(t)
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })

	if err := tt.Click("提交与推送"); err != nil {
		t.Fatalf("click commit button failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("已更改 2 个文件") })
	if err := tt.Click("提交…"); err != nil {
		t.Fatalf("click commit item failed: %v", err)
	}
	f := gitFormOf("t1", gitFormCommit)
	if f == nil {
		t.Fatal("提交窗口没有打开")
	}
	wt := gitFormTester(t, f)
	waitFor(t, wt, func() bool {
		_, ok := wt.Find("提交信息")
		return ok
	})
	if err := wt.Click("提交信息"); err != nil {
		t.Fatalf("focus commit input failed: %v", err)
	}
	wt.Type("修复登录")
	wt.Frame()
	if err := wt.Click("提交"); err != nil {
		t.Fatalf("click commit failed: %v", err)
	}
	waitFor(t, wt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.gitCommitMessages) == 1 && fake.gitCommitMessages[0] == "修复登录"
	})
	// 提交成功后窗口关闭，菜单里的改动数归零
	if got := gitFormOf("t1", gitFormCommit); got != nil {
		t.Fatal("提交成功后窗口应关闭并从注册表移除")
	}
	// 改动数归零后 badge 消失，右侧动作组整体右移；等新布局画出来再点，否则会按旧坐标点空
	waitFor(t, tt, func() bool {
		_, hasBadge := tt.Find("2")
		return !hasBadge
	})
	if err := tt.Click("提交与推送"); err != nil {
		t.Fatalf("reopen commit menu failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("已更改 0 个文件") })

	if err := tt.Click("推送"); err != nil {
		t.Fatalf("click push failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.gitPushCalls == 1
	})
}

func TestChatHeaderCopyTitle(t *testing.T) {
	fake := NewFakeClient()
	s, root := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	// 首帧之后标题栏还会异步补入 Git、用量、Swarm 等控件，布局未稳定就点击会按上一帧的坐标点空。
	// 等标题按钮的位置连续几帧不变再点（整包运行时其它用例的残留异步会放大这种抖动）。
	var prev ui.Rect
	stable := 0
	waitFor(t, tt, func() bool {
		r, ok := tt.Find("重命名会话")
		if !ok {
			prev, stable = ui.Rect{}, 0
			return false
		}
		if r == prev {
			stable++
		} else {
			prev, stable = r, 1
		}
		return stable >= 3
	})

	if err := tt.RightClick("重命名会话"); err != nil {
		t.Fatalf("right click title failed: %v", err)
	}
	if err := tt.ChooseMenuItem("复制名称"); err != nil {
		t.Fatalf("copy title failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.Clipboard() == "测试会话1" && tt.HasText("已复制会话名称")
	})
}

// 右键标题打开菜单，菜单里的「重命名」进入编辑态，单击不会打开菜单。
func TestChatHeaderTitleContextMenuRename(t *testing.T) {
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	if err := tt.RightClick("重命名会话"); err != nil {
		t.Fatalf("right click title failed: %v", err)
	}
	menu := tt.Menu()
	want := []string{"复制名称", "在侧栏定位", "重命名"}
	if fmt.Sprint(menu) != fmt.Sprint(want) {
		t.Fatalf("context menu = %+v, want %+v", menu, want)
	}
	if err := tt.ChooseMenuItem("重命名"); err != nil {
		t.Fatalf("choose rename failed: %v", err)
	}
	waitFor(t, tt, func() bool { return editingHeaderState("t1") != nil })
	tt.Command("selectAll")
	tt.Type("右键改名")
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		calls := renameCallsOf(fake)
		return len(calls) == 1 && calls[0].Title == "右键改名"
	})
}

// 新的 Git 菜单：标题行显示改动文件数，各操作项齐全，点击抓取/贮藏调用对应 Client 方法并刷新状态。
func TestChatHeaderGitMenuActions(t *testing.T) {
	fake := newGitFakeClient()
	fake.mu.Lock()
	fake.gitState.ChangedFiles = 2
	fake.gitState.HasUpstream = true
	fake.gitState.CanFastForward = true
	fake.gitState.Behind = 1
	fake.gitState.HasStash = true
	fake.gitState.RemoteURL = "https://github.com/example/repo.git"
	fake.mu.Unlock()

	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
	if err := tt.Click("提交与推送"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("已更改 2 个文件") })
	for _, label := range []string{"提交…", "推送", "创建合并请求…", "抓取", "拉取（快进） ↓1", "合并…", "贮藏改动", "恢复贮藏"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("Git 菜单缺少 %q: %q", label, tt.Texts())
		}
	}

	if err := tt.Click("抓取"); err != nil {
		t.Fatalf("click fetch failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.gitOpCalls) == 1 && fake.gitOpCalls[0] == "GitFetch"
	})
	// 抓取成功后菜单关闭，重新打开再点贮藏
	waitFor(t, tt, func() bool {
		_, open := tt.Find("贮藏改动")
		return !open
	})
	if err := tt.Click("提交与推送"); err != nil {
		t.Fatalf("reopen commit menu failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("贮藏改动")
		return ok
	})

	if err := tt.Click("贮藏改动"); err != nil {
		t.Fatalf("click stash failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.gitOpCalls) == 2 && fake.gitOpCalls[1] == "GitStash"
	})
	// 贮藏成功后菜单关闭、状态刷新：重新打开菜单，改动数已归零
	if err := tt.Click("提交与推送"); err != nil {
		t.Fatalf("reopen commit menu failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("已更改 0 个文件") })
}

// 本地还没抓取过（CanFastForward=false、Behind=0）时，配了上游的「拉取（快进）」仍可点击并调用 GitPull。
func TestChatHeaderGitPullEnabledWithoutFetchedState(t *testing.T) {
	fake := newGitFakeClient()
	fake.mu.Lock()
	fake.gitState.HasUpstream = true
	fake.gitState.CanFastForward = false
	fake.gitState.Behind = 0
	fake.mu.Unlock()

	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
	if err := tt.Click("提交与推送"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("拉取（快进）")
		return ok
	})

	if err := tt.Click("拉取（快进）"); err != nil {
		t.Fatalf("click pull failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		for _, call := range fake.gitOpCalls {
			if call == "GitPull" {
				return true
			}
		}
		return false
	})
	waitFor(t, tt, func() bool { return tt.HasText("已快进拉取") })
}

// 点「合并…」打开独立的合并窗口：窗口里能选方向、搜索并选中分支，不立即执行。
func TestChatHeaderGitMergeOpensForm(t *testing.T) {
	fake := newGitFakeClient()
	stubGitFormWindow(t)
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
	if err := tt.Click("提交与推送"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("合并…") })
	if err := tt.Click("合并…"); err != nil {
		t.Fatalf("click merge item failed: %v", err)
	}
	waitFor(t, tt, func() bool { return gitFormOf("t1", gitFormMerge) != nil })
	f := gitFormOf("t1", gitFormMerge)
	if f == nil {
		t.Fatal("合并窗口没有打开")
	}

	wt := gitFormTester(t, f)
	waitFor(t, wt, func() bool { return wt.HasText("main") })
	if err := wt.Click("快进合并到所选分支"); err != nil {
		t.Fatalf("click fast-forward mode failed: %v", err)
	}
	wt.Frame()
	if !f.mergeFastForward {
		t.Fatal("点「快进合并到所选分支」后没有切到快进模式")
	}
	if err := wt.Click("main"); err != nil {
		t.Fatalf("click branch main failed: %v", err)
	}
	wt.Frame()
	if f.mergeBranch != "main" {
		t.Fatalf("选中的分支 = %q, want main", f.mergeBranch)
	}
	// 选中分支不立即执行，要再点主按钮
	fake.mu.Lock()
	calls := len(fake.gitOpCalls)
	fake.mu.Unlock()
	if calls != 0 {
		t.Fatalf("选中分支就执行了 Git 操作: %v", fake.gitOpCalls)
	}
	if err := wt.Click("快进合并"); err != nil {
		t.Fatalf("click submit failed: %v", err)
	}
	waitFor(t, wt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.gitOpCalls) == 1 && fake.gitOpCalls[0] == "GitFastForwardInto:main"
	})
}

// submitMerge 按窗口里的方向调对应的 Client 方法：快进模式快进到所选分支，非快进模式合并进当前分支。
func TestGitFormSubmitMerge(t *testing.T) {
	fake := newGitFakeClient()
	s := newChatState(fake)

	ff := &gitForm{s: s, threadID: "t1", kind: gitFormMerge, mergeFastForward: true, mergeBranch: "main"}
	wt := gitFormTester(t, ff)
	ff.submitMerge()
	waitFor(t, wt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.gitOpCalls) == 1 && fake.gitOpCalls[0] == "GitFastForwardInto:main"
	})

	into := &gitForm{s: s, threadID: "t1", kind: gitFormMerge, mergeBranch: "main"}
	wt2 := gitFormTester(t, into)
	into.submitMerge()
	waitFor(t, wt2, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.gitOpCalls) == 2 && fake.gitOpCalls[1] == "GitMergeFrom:main"
	})
}

func TestNormalizeGitRemoteURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		// SSH 规范 (SCP-like)
		{
			name: "ssh standard github",
			raw:  "git@github.com:owner/repo.git",
			want: "https://github.com/owner/repo",
		},
		{
			name: "ssh without git extension",
			raw:  "git@github.com:owner/repo",
			want: "https://github.com/owner/repo",
		},
		// HTTPS 规范
		{
			name: "https standard github",
			raw:  "https://github.com/owner/repo.git",
			want: "https://github.com/owner/repo",
		},
		{
			name: "https without git extension",
			raw:  "https://github.com/owner/repo",
			want: "https://github.com/owner/repo",
		},
		{
			name: "http normalized to https",
			raw:  "http://github.com/owner/repo.git",
			want: "https://github.com/owner/repo",
		},
		// GitLab 子组 (Subgroups)
		{
			name: "gitlab ssh single subgroup",
			raw:  "git@gitlab.com:group/subgroup/repo.git",
			want: "https://gitlab.com/group/subgroup/repo",
		},
		{
			name: "gitlab ssh deep nested subgroups",
			raw:  "git@gitlab.com:org/dept/team/project.git",
			want: "https://gitlab.com/org/dept/team/project",
		},
		{
			name: "gitlab https nested subgroups",
			raw:  "https://gitlab.com/org/dept/team/project.git",
			want: "https://gitlab.com/org/dept/team/project",
		},
		// 非标准主机与私有自建服务
		{
			name: "non standard host ssh",
			raw:  "git@git.company.internal:team/project.git",
			want: "https://git.company.internal/team/project",
		},
		{
			name: "non standard host https",
			raw:  "https://code.internal.net/engineering/core-service.git",
			want: "https://code.internal.net/engineering/core-service",
		},
		{
			name: "ssh protocol with port",
			raw:  "ssh://git@git.company.internal:2222/team/project.git",
			want: "https://git.company.internal/team/project",
		},
		{
			name: "ssh protocol without port",
			raw:  "ssh://git@custom.host/group/repo.git",
			want: "https://custom.host/group/repo",
		},
		// 边界情况
		{
			name: "empty url",
			raw:  "",
			want: "",
		},
		{
			name: "whitespace url",
			raw:  "  git@github.com:foo/bar.git \n",
			want: "https://github.com/foo/bar",
		},
		{
			name: "invalid url",
			raw:  "invalid-remote-format",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeGitRemoteURL(tt.raw)
			if got != tt.want {
				t.Errorf("normalizeGitRemoteURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestGitPullRequestURL(t *testing.T) {
	tests := []struct {
		name    string
		repoURL string
		branch  string
		want    string
	}{
		{
			name:    "github simple branch",
			repoURL: "https://github.com/owner/repo",
			branch:  "main",
			want:    "https://github.com/owner/repo/compare/main?expand=1",
		},
		{
			name:    "github feature branch with slash",
			repoURL: "https://github.com/owner/repo",
			branch:  "feature/login",
			want:    "https://github.com/owner/repo/compare/feature/login?expand=1",
		},
		{
			name:    "github repo url with trailing slash",
			repoURL: "https://github.com/owner/repo/",
			branch:  "fix-123",
			want:    "https://github.com/owner/repo/compare/fix-123?expand=1",
		},
		{
			name:    "gitlab simple branch",
			repoURL: "https://gitlab.com/group/subgroup/repo",
			branch:  "main",
			want:    "https://gitlab.com/group/subgroup/repo/-/merge_requests/new?merge_request[source_branch]=main",
		},
		{
			name:    "gitlab branch with slash query escaped",
			repoURL: "https://gitlab.com/group/subgroup/repo",
			branch:  "feature/login",
			want:    "https://gitlab.com/group/subgroup/repo/-/merge_requests/new?merge_request[source_branch]=feature%2Flogin",
		},
		{
			name:    "gitlab self hosted instance",
			repoURL: "https://gitlab.company.internal/dept/project",
			branch:  "feature/oauth",
			want:    "https://gitlab.company.internal/dept/project/-/merge_requests/new?merge_request[source_branch]=feature%2Foauth",
		},
		{
			name:    "empty branch",
			repoURL: "https://github.com/owner/repo",
			branch:  "",
			want:    "",
		},
		{
			name:    "empty repo url",
			repoURL: "",
			branch:  "main",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gitPullRequestURL(tt.repoURL, tt.branch)
			if got != tt.want {
				t.Errorf("gitPullRequestURL(%q, %q) = %q, want %q", tt.repoURL, tt.branch, got, tt.want)
			}
		})
	}
}

// RemoteURL 不为空时，Git 菜单提供创建合并请求与打开仓库；
// 创建合并请求在独立窗口里填写标题，再把地址连同标题一起打开。
func TestChatHeaderGitPullRequestWhenRemoteURLPresent(t *testing.T) {
	fake := newGitFakeClient()
	fake.mu.Lock()
	fake.gitState.RemoteURL = "https://github.com/example/repo.git"
	fake.gitState.Branch = "feature/login"
	fake.mu.Unlock()
	stubGitFormWindow(t)

	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })

	if err := tt.Click("提交与推送"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("创建合并请求…") && tt.HasText("打开仓库")
	})
	if err := tt.Click("打开仓库"); err != nil {
		t.Fatal(err)
	}

	if err := tt.Click("提交与推送"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("创建合并请求…") })
	if err := tt.Click("创建合并请求…"); err != nil {
		t.Fatal(err)
	}
	f := gitFormOf("t1", gitFormPullRequest)
	if f == nil {
		t.Fatal("创建合并请求窗口没有打开")
	}
	wt := gitFormTester(t, f)
	waitFor(t, wt, func() bool {
		_, ok := wt.Find("标题")
		return ok
	})
	if err := wt.Click("标题"); err != nil {
		t.Fatal(err)
	}
	wt.Type("add login")
	wt.Frame()
	if err := wt.Click("打开创建页"); err != nil {
		t.Fatal(err)
	}

	if got, want := tt.OpenedURLs(), []string{"https://github.com/example/repo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("opened URLs = %q, want %q", got, want)
	}
	want := []string{"https://github.com/example/repo/compare/feature/login?expand=1&title=add+login"}
	if got := wt.OpenedURLs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("opened URLs = %q, want %q", got, want)
	}
	if got := gitFormOf("t1", gitFormPullRequest); got != nil {
		t.Fatal("打开创建页后窗口应关闭并从注册表移除")
	}
}

// 验证「AI 帮我写」按钮会把生成结果填入提交窗口的提交信息输入框
func TestChatHeaderAIGenerateCommitMessageFillsInput(t *testing.T) {
	fake := newGitFakeClient()
	fake.SetGenerateCommitMessageResult("feat: 自动生成的提交信息", nil)
	stubGitFormWindow(t)
	_, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })

	if err := tt.Click("提交与推送"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("已更改 2 个文件") })
	if err := tt.Click("提交…"); err != nil {
		t.Fatal(err)
	}
	f := gitFormOf("t1", gitFormCommit)
	if f == nil {
		t.Fatal("提交窗口没有打开")
	}
	wt := gitFormTester(t, f)
	waitFor(t, wt, func() bool {
		_, ok := wt.Find("提交信息")
		return ok && wt.HasText("AI 帮我写")
	})

	if err := wt.Click("AI 帮我写"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, wt, func() bool {
		return f.commitMsg == "feat: 自动生成的提交信息"
	})
}

// generateCommitCounter 在内嵌假 Client 之外统计 GenerateCommitMessage 的调用次数，
// 其余接口方法全部沿用假 Client 的实现。
type generateCommitCounter struct {
	*FakeClient
	genMu    sync.Mutex
	genCalls int
}

// GenerateCommitMessage 记录一次调用后交给假 Client 返回配置的结果。
func (c *generateCommitCounter) GenerateCommitMessage(ctx context.Context, threadID string) (string, error) {
	c.genMu.Lock()
	c.genCalls++
	c.genMu.Unlock()
	return c.FakeClient.GenerateCommitMessage(ctx, threadID)
}

// GenerateCommitMessageCalls 返回已收到的生成请求次数。
func (c *generateCommitCounter) GenerateCommitMessageCalls() int {
	c.genMu.Lock()
	defer c.genMu.Unlock()
	return c.genCalls
}

// 验证生成功能关闭时，提交窗口里的「AI 帮我写」按钮仍然可见，但点击不会触发生成
func TestChatHeaderAIGenerateCommitMessageHiddenWhenDisabled(t *testing.T) {
	fake := &generateCommitCounter{FakeClient: newGitFakeClient()}
	s, root := buildRoot(fake, WithSettings(settingsTestDir(t), nil))
	stubGitFormWindow(t)

	falseVal := false
	st := getChatSettings(s)
	if st == nil {
		t.Fatal("expected settings to be initialized")
	}
	st.mu.Lock()
	st.cfg.SourceControl.GenerateCommitMessage = &falseVal
	st.mu.Unlock()

	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })

	if err := tt.Click("提交与推送"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("已更改 2 个文件") })
	if err := tt.Click("提交…"); err != nil {
		t.Fatal(err)
	}
	f := gitFormOf("t1", gitFormCommit)
	if f == nil {
		t.Fatal("提交窗口没有打开")
	}
	wt := gitFormTester(t, f)
	waitFor(t, wt, func() bool {
		_, ok := wt.Find("提交信息")
		return ok && wt.HasText("AI 帮我写")
	})

	// 按钮可见但处于禁用状态：点击不会发出生成请求，也不会填入提交信息。
	if err := wt.Click("AI 帮我写"); err != nil {
		t.Fatal(err)
	}
	wt.Frame()
	if got := fake.GenerateCommitMessageCalls(); got != 0 {
		t.Fatalf("生成功能关闭时点击按钮不应调用生成接口, 实际调用 %d 次", got)
	}
	if got := f.commitMsg; got != "" {
		t.Fatalf("生成功能关闭时提交信息不应被填入, got %q", got)
	}
}

// 验证提交窗口打开时会自动生成一次提交信息：重复调用只触发一次，用户已有草稿时不触发。
func TestGitCommitFormAutoGeneratesOnOpen(t *testing.T) {
	// openCommitForm 走头部 Git 菜单打开提交窗口，此时头部已缓存改动数（2 个文件）。
	openCommitForm := func(t *testing.T) (*generateCommitCounter, *gitForm, *ui.Tester) {
		t.Helper()
		fake := &generateCommitCounter{FakeClient: newGitFakeClient()}
		fake.SetGenerateCommitMessageResult("feat: 打开窗口自动生成", nil)
		stubGitFormWindow(t)
		_, root := buildRoot(fake)
		tt := ui.NewTester(root, 1000, 700)
		waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
		if err := tt.Click("提交与推送"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, tt, func() bool { return tt.HasText("已更改 2 个文件") })
		if err := tt.Click("提交…"); err != nil {
			t.Fatal(err)
		}
		f := gitFormOf("t1", gitFormCommit)
		if f == nil {
			t.Fatal("提交窗口没有打开")
		}
		wt := gitFormTester(t, f)
		waitFor(t, wt, func() bool {
			_, ok := wt.Find("提交信息")
			return ok
		})
		return fake, f, wt
	}

	t.Run("只自动生成一次", func(t *testing.T) {
		fake, f, wt := openCommitForm(t)
		f.autoGenerateOnOpen()
		f.autoGenerateOnOpen()
		waitFor(t, wt, func() bool {
			return f.commitMsg == "feat: 打开窗口自动生成"
		})
		if got := fake.GenerateCommitMessageCalls(); got != 1 {
			t.Fatalf("autoGenerateOnOpen 应只触发一次生成, 实际调用 %d 次", got)
		}
	})

	t.Run("已有草稿时不生成", func(t *testing.T) {
		fake, f, wt := openCommitForm(t)
		f.commitMsg = "feat: 用户草稿"
		f.autoGenerateOnOpen()
		wt.Frame()
		if got := fake.GenerateCommitMessageCalls(); got != 0 {
			t.Fatalf("提交信息已有内容时不应触发生成, 实际调用 %d 次", got)
		}
		if got := f.commitMsg; got != "feat: 用户草稿" {
			t.Fatalf("提交信息已有内容时应保留草稿, got %q", got)
		}
	})
}

// ---------------------------------------------------------------------------
// 标题栏项目动作：权限模式胶囊、在其它应用中打开、脚本菜单与手动推送
// ---------------------------------------------------------------------------

// triggerAutoMergeCallsOf 获取假 Client 收到的 TriggerAutoMerge 调用。
func triggerAutoMergeCallsOf(fake *FakeClient) []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]string(nil), fake.triggerAutoMergeCalls...)
}

// pushBusyOf 读取头部状态上的 pushBusy 标志。
func pushBusyOf(s *chatState) bool {
	chatHeaderMu.Lock()
	defer chatHeaderMu.Unlock()
	hs := chatHeaderStates[s]
	return hs != nil && hs.pushBusy
}

// detectOpenInApps：访达始终第一项，其余按固定顺序只保留已安装的应用。
func TestDetectOpenInApps(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	installed := map[string]bool{
		"/Applications/Cursor.app":       true,
		"/Applications/Sublime Text.app": true,
	}
	got := detectOpenInApps(func(path string) bool { return installed[path] })
	want := []openInApp{
		{Name: "访达"},
		{Name: "Cursor", AppPath: "/Applications/Cursor.app"},
		{Name: "Sublime Text", AppPath: "/Applications/Sublime Text.app"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detectOpenInApps = %+v, want %+v", got, want)
	}

	// 一个都没装：只有访达
	if got := detectOpenInApps(func(string) bool { return false }); len(got) != 1 || got[0].Name != "访达" {
		t.Fatalf("没有已安装应用时应只返回访达, got %+v", got)
	}
	// statFn 为 nil 时同样只有访达
	if got := detectOpenInApps(nil); len(got) != 1 || got[0].Name != "访达" {
		t.Fatalf("statFn 为 nil 时应只返回访达, got %+v", got)
	}
}

// Windows 上的打开方式：资源管理器第一项，按 exe 路径检测，启动时不再调用 open。
func TestOpenInWindows(t *testing.T) {
	origGOOS := openInGOOS
	defer func() { openInGOOS = origGOOS }()
	origRunner := openInRunner
	defer func() { openInRunner = origRunner }()
	origLaunch := openInLaunchRunner
	defer func() { openInLaunchRunner = origLaunch }()
	origStat := openInStatFn
	defer func() { openInStatFn = origStat }()

	openInGOOS = "windows"
	localAppData := filepath.Join("C:", "Users", "me", "AppData", "Local")
	programFiles := filepath.Join("C:", "Program Files")
	t.Setenv("LOCALAPPDATA", localAppData)
	t.Setenv("ProgramFiles", programFiles)

	var launches [][]string
	openInLaunchRunner = func(exePath string, args ...string) error {
		launches = append(launches, append([]string{exePath}, args...))
		return nil
	}
	var opens [][]string
	openInRunner = func(args ...string) error {
		opens = append(opens, append([]string(nil), args...))
		return nil
	}
	openInStatFn = func(string) bool { return false }

	// 检测：资源管理器第一项，已安装的 exe 按固定顺序列出
	cursorPath := filepath.Join(localAppData, "Programs", "cursor", "Cursor.exe")
	sublimePath := filepath.Join(programFiles, "Sublime Text", "sublime_text.exe")
	installed := map[string]bool{cursorPath: true, sublimePath: true}
	got := detectOpenInApps(func(path string) bool { return installed[path] })
	want := []openInApp{
		{Name: "资源管理器"},
		{Name: "Cursor", AppPath: cursorPath},
		{Name: "Sublime Text", AppPath: sublimePath},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detectOpenInApps(windows) = %+v, want %+v", got, want)
	}
	// 一个都没装：只有资源管理器
	if got := detectOpenInApps(func(string) bool { return false }); len(got) != 1 || got[0].Name != "资源管理器" {
		t.Fatalf("windows 没有已安装应用时应只返回资源管理器, got %+v", got)
	}

	// 资源管理器：explorer.exe <dir>
	openInWithApp(openInApp{Name: "资源管理器"}, `C:\repo`)
	// 应用：直接启动可执行文件
	openInWithApp(openInApp{Name: "Cursor", AppPath: cursorPath}, `C:\repo`)
	// 路径不是可执行文件且不存在：退回资源管理器
	openInWithApp(openInApp{Name: "旧应用", AppPath: `C:\Tools\Old.app`}, `C:\repo`)

	wantLaunches := [][]string{
		{"explorer.exe", `C:\repo`},
		{cursorPath, `C:\repo`},
		{"explorer.exe", `C:\repo`},
	}
	if !reflect.DeepEqual(launches, wantLaunches) {
		t.Fatalf("windows 启动参数 = %+v, want %+v", launches, wantLaunches)
	}
	if len(opens) != 0 {
		t.Fatalf("windows 上不应调用 open, got %+v", opens)
	}
}

// 打开方式菜单：点访达传目录，点应用传 -a 加应用路径，复制路径写入剪贴板并 toast。
func TestChatHeaderOpenInMenu(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := newGitFakeClient()
	_, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })

	origRunner := openInRunner
	defer func() { openInRunner = origRunner }()
	origStat := openInStatFn
	defer func() { openInStatFn = origStat }()

	var calls [][]string
	openInRunner = func(args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	openInStatFn = func(path string) bool { return path == "/Applications/Cursor.app" }

	if err := tt.Click("在其它应用中打开"); err != nil {
		t.Fatalf("click open-in button failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("打开方式")
		return ok
	})
	if !tt.HasText("访达") || !tt.HasText("Cursor") {
		t.Fatalf("打开方式列表不完整, texts: %q", tt.Texts())
	}

	// 访达：open <dir>
	if err := tt.Click("访达"); err != nil {
		t.Fatalf("click 访达 failed: %v", err)
	}
	waitFor(t, tt, func() bool { return len(calls) == 1 })
	if !reflect.DeepEqual(calls[0], []string{"/repo"}) {
		t.Fatalf("访达打开参数 = %q, want [%q]", calls[0], "/repo")
	}

	// 其它应用：open -a <app> <dir>
	if err := tt.Click("在其它应用中打开"); err != nil {
		t.Fatalf("reopen open-in button failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("Cursor")
		return ok
	})
	if err := tt.Click("Cursor"); err != nil {
		t.Fatalf("click Cursor failed: %v", err)
	}
	waitFor(t, tt, func() bool { return len(calls) == 2 })
	if !reflect.DeepEqual(calls[1], []string{"-a", "/Applications/Cursor.app", "/repo"}) {
		t.Fatalf("Cursor 打开参数 = %q", calls[1])
	}

	// 复制路径
	if err := tt.Click("在其它应用中打开"); err != nil {
		t.Fatalf("reopen open-in button failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("复制路径")
		return ok
	})
	if err := tt.Click("复制路径"); err != nil {
		t.Fatalf("click 复制路径 failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.Clipboard() == "/repo" })
	waitFor(t, tt, func() bool { return tt.HasText("已复制路径") })
}

// 权限圆钮：Label 为「档位 · 说明」，菜单单行只显示勾选列、档位图标与标题；
// 选「自动」后调用设置函数并持久化，圆钮 Label 随之更新。
func TestChatHeaderPermissionSwitch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	s, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	const initialLabel = "自动 · 改文件和只读命令不询问"
	waitFor(t, tt, func() bool {
		_, ok := tt.Find(initialLabel)
		return ok
	})

	if err := tt.Click(initialLabel); err != nil {
		t.Fatalf("click permission capsule failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("权限") })
	// 单行菜单：不显示说明行
	if tt.HasText("由 Agent 自行判断常规操作") {
		t.Fatalf("权限菜单不应显示说明行: %q", tt.Texts())
	}
	for _, title := range []string{"监督", "自动", "完全访问"} {
		if !tt.HasText(title) {
			t.Fatalf("权限菜单缺少档位 %q: %q", title, tt.Texts())
		}
	}

	if err := tt.Click("自动"); err != nil {
		t.Fatalf("click 自动 failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		tid, mode := lastRuntimeModeCall(fake)
		return tid == "t1" && mode == "auto"
	})
	// 持久化到本地会话列表
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, th := range s.threads {
			if th.ID == "t1" {
				return th.RuntimeMode == "auto"
			}
		}
		return false
	})
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("自动 · 由 Agent 自行判断常规操作")
		return ok
	})
}

// 会话总 token 不再出现在标题栏（改为时间线底部的 session usage 行）。
func TestChatHeaderOmitsSessionTokenTotal(t *testing.T) {
	fake := NewFakeClient()
	fake.SetThreadUsage("t1", ThreadUsage{Total: TokenUsage{Input: 3700, Output: 956, Cached: 8400}})
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	tt.Frame()
	if tt.HasText("本会话 共") {
		t.Fatalf("标题栏不应再显示会话总 token: %q", tt.Texts())
	}
}

// 执行命令菜单：有项目工作目录就显示按钮，无脚本时菜单给出空状态与「添加脚本…」入口；
// loadHeaderScripts 把 fake 返回的脚本缓存在头部状态里，菜单会列出脚本：只有 package.json 脚本时
// 仍是「添加脚本…」，有项目脚本时改为「编辑脚本…」并排在 package 脚本之前。
func TestChatHeaderScriptsMenu(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := newGitFakeClient()
	s, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("运行项目脚本")
		return ok
	})

	// 没有脚本时按钮仍显示，菜单内是空状态与添加入口
	if err := tt.Click("运行项目脚本"); err != nil {
		t.Fatalf("click scripts button failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("添加脚本…") })
	if !tt.HasText("暂无可用脚本") {
		t.Fatalf("没有脚本时菜单应显示空状态, texts: %q", tt.Texts())
	}
	if err := tt.Click("添加脚本…"); err != nil {
		t.Fatalf("click add-script item failed: %v", err)
	}
	// 「添加脚本…」改为在应用内弹窗里编辑，不再跳设置页
	waitFor(t, tt, func() bool { return tt.HasText("项目脚本") })
	if err := tt.Click("取消脚本编辑"); err != nil {
		t.Fatalf("close scripts sheet failed: %v", err)
	}

	fake.SetScripts([]Script{{Name: "dev", Command: "npm run dev", Runner: "npm"}})
	s.loadHeaderScripts("t1")
	waitFor(t, tt, func() bool {
		chatHeaderMu.Lock()
		defer chatHeaderMu.Unlock()
		hs := chatHeaderStates[s]
		return hs != nil && len(hs.scripts["t1"]) == 1 && hs.scripts["t1"][0].Name == "dev"
	})
	if err := tt.Click("运行项目脚本"); err != nil {
		t.Fatalf("reopen scripts button failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("npm run dev") })
	// 只有 package.json 脚本时仍给「添加脚本…」入口：它只负责维护项目脚本。
	if !tt.HasText("添加脚本…") {
		t.Fatalf("只有 package 脚本时菜单应显示「添加脚本…」, texts: %q", tt.Texts())
	}
	if tt.HasText("编辑脚本…") {
		t.Fatalf("没有项目脚本时不应显示「编辑脚本…」, texts: %q", tt.Texts())
	}
	if tt.HasText("暂无可用脚本") {
		t.Fatalf("有脚本时不应显示空状态, texts: %q", tt.Texts())
	}

	// 加一个项目脚本后改为「编辑脚本…」，且菜单里项目脚本排在 package 脚本前面。
	if err := fake.SetProjectScripts(context.Background(), "t1", []ProjectScript{{Name: "lint", Command: "make lint"}}); err != nil {
		t.Fatalf("set project scripts failed: %v", err)
	}
	s.loadHeaderScripts("t1")
	waitFor(t, tt, func() bool {
		chatHeaderMu.Lock()
		defer chatHeaderMu.Unlock()
		hs := chatHeaderStates[s]
		return hs != nil && len(hs.scripts["t1"]) == 2
	})
	waitFor(t, tt, func() bool { return tt.HasText("lint") && tt.HasText("编辑脚本…") })
	if !tt.HasText("make lint") {
		t.Fatalf("菜单应显示项目脚本命令, texts: %q", tt.Texts())
	}
	if tt.HasText("添加脚本…") {
		t.Fatalf("有项目脚本时不再显示「添加脚本…」, texts: %q", tt.Texts())
	}
	lintIdx, devIdx := -1, -1
	texts := tt.Texts()
	menuStart := -1
	for i, txt := range texts {
		if txt == "执行命令" {
			menuStart = i
			break
		}
	}
	if menuStart < 0 {
		t.Fatalf("菜单应包含「执行命令」标题, texts: %q", texts)
	}
	for i, txt := range texts[menuStart:] {
		switch txt {
		case "lint":
			if lintIdx < 0 {
				lintIdx = i
			}
		case "dev":
			if devIdx < 0 {
				devIdx = i
			}
		}
	}
	if lintIdx < 0 || devIdx < 0 || lintIdx > devIdx {
		t.Fatalf("项目脚本应排在 package 脚本之前, lint 下标=%d dev 下标=%d texts: %q", lintIdx, devIdx, texts)
	}
}

// 项目动作组的分隔线只在两组都非空且相邻时出现：只有 Git 一组时不画线。
// 规则本身集中在 headerRenderDividerBetween 的渲染计数里，这里直接校验计数逻辑。
func TestChatHeaderActionGroupDivider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// 按 headerRenderDividerBetween 的规则计算：分隔线数 = 非空分组数 - 1，且只在相邻的两个非空组之间。
	dividerCount := func(visible []bool) (order []string, divs int) {
		for i, v := range visible {
			if !v {
				continue
			}
			if len(order) > 0 {
				divs++
			}
			order = append(order, string(rune('a'+i)))
		}
		return order, divs
	}

	for _, tt := range []struct {
		name    string
		visible []bool
		order   []string
		divs    int
	}{
		{"全部非空", []bool{true, true, true}, []string{"a", "b", "c"}, 2},
		{"只有 Git 一组", []bool{false, false, true}, []string{"c"}, 0},
		{"只有打开方式", []bool{true, false, false}, []string{"a"}, 0},
		{"首组为空", []bool{false, true, true}, []string{"b", "c"}, 1},
		{"末组为空", []bool{true, true, false}, []string{"a", "b"}, 1},
		{"全部为空", []bool{false, false, false}, nil, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			order, divs := dividerCount(tt.visible)
			if !reflect.DeepEqual(order, tt.order) || divs != tt.divs {
				t.Fatalf("order=%v divs=%d, want %v/%d", order, divs, tt.order, tt.divs)
			}
		})
	}

	// 端到端：Git 仓库一定有工作目录，打开方式与执行命令按钮都出现；
	// 没有工作目录（ThreadWorkDir 也为空）时不显示。
	fake := newGitFakeClient()
	s, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("在其它应用中打开")
		return ok
	})
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("运行项目脚本")
		return ok
	})
	if !s.headerScriptsButtonVisible("t1") {
		t.Fatal("有项目工作目录时应显示执行命令按钮")
	}
	if s.headerScriptsButtonVisible("") || s.headerScriptsButtonVisible("missing") {
		t.Fatal("没有项目工作目录时不应显示执行命令按钮")
	}
}

// 非 Git 项目只隐藏 Git 相关控件：只要有工作目录，打开方式与执行命令按钮照常显示。
func TestChatHeaderNonGitProjectKeepsOpenInAndScripts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.SetThreadWorkDir("/path/to/test")
	_, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("在其它应用中打开")
		return ok
	})
	if _, ok := tt.Find("运行项目脚本"); !ok {
		t.Fatal("非 Git 会话有工作目录时应显示执行命令按钮")
	}
	if _, ok := tt.Find("Git 分支"); ok {
		t.Fatal("非 Git 会话不应显示分支胶囊")
	}
	if _, ok := tt.Find("提交与推送"); ok {
		t.Fatal("非 Git 会话不应显示提交按钮")
	}
}

type delayedPushPRClient struct {
	*FakeClient
	release chan struct{}
}

func (f *delayedPushPRClient) TriggerAutoMerge(ctx context.Context, leadThreadID string) error {
	f.mu.Lock()
	f.triggerAutoMergeCalls = append(f.triggerAutoMergeCalls, leadThreadID)
	f.mu.Unlock()
	<-f.release
	return nil
}

// 提交弹层里的「推送并建 PR/MR」先弹确认区，确认后用当前会话 ID 调 TriggerAutoMerge，执行中不重复调用。
func TestChatHeaderManualPushPR(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	client := &delayedPushPRClient{
		FakeClient: newGitFakeClient(),
		release:    make(chan struct{}),
	}
	s, root := buildRoot(client)
	tt := ui.NewTester(root, 1000, 700)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(client.release) }) }
	defer release()

	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
	if err := tt.Click("提交与推送"); err != nil {
		t.Fatalf("click commit button failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("推送并建 PR/MR") })
	if err := tt.Click("推送并建 PR/MR"); err != nil {
		t.Fatalf("click push-pr failed: %v", err)
	}
	// 点击后先在菜单内弹出确认区，确认后才推送
	waitFor(t, tt, func() bool { return tt.HasText("确认推送") })
	if calls := triggerAutoMergeCallsOf(client.FakeClient); len(calls) != 0 {
		t.Fatalf("确认前不应触发推送, calls=%v", calls)
	}
	if err := tt.Click("确认推送"); err != nil {
		t.Fatalf("click push-pr confirm failed: %v", err)
	}
	waitFor(t, tt, func() bool { return len(triggerAutoMergeCallsOf(client.FakeClient)) == 1 })
	if got := triggerAutoMergeCallsOf(client.FakeClient)[0]; got != "t1" {
		t.Fatalf("TriggerAutoMerge lead = %q, want t1", got)
	}

	// 执行中（尚未返回）再触发不会重复调用
	s.triggerManualPushPR("t1")
	tt.Frame()
	if n := len(triggerAutoMergeCallsOf(client.FakeClient)); n != 1 {
		t.Fatalf("执行中重复触发, calls=%d", n)
	}
	if !pushBusyOf(s) {
		t.Fatal("执行中 pushBusy 应为 true")
	}

	// 完成后可以再次触发
	release()
	waitFor(t, tt, func() bool { return !pushBusyOf(s) })
	s.triggerManualPushPR("t1")
	waitFor(t, tt, func() bool { return len(triggerAutoMergeCallsOf(client.FakeClient)) == 2 })
	waitFor(t, tt, func() bool { return tt.HasText("已推送并创建 PR/MR") })
}

// Swarm bee 会话里的手动推送用 swarmTargetLeadID 返回的 lead ID。
func TestChatHeaderManualPushPRUsesSwarmLeadID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := newGitFakeClient()
	fake.SetSwarmState("t1", SwarmState{LeadThreadID: "t9", Available: true, Enabled: true})
	s, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })

	s.loadSwarm("t1")
	waitFor(t, tt, func() bool { return s.swarmTargetLeadID() == "t9" })

	if err := tt.Click("提交与推送"); err != nil {
		t.Fatalf("click commit button failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("推送并建 PR/MR") })
	if err := tt.Click("推送并建 PR/MR"); err != nil {
		t.Fatalf("click push-pr failed: %v", err)
	}
	// 点击后先在菜单内弹出确认区，确认后才推送
	waitFor(t, tt, func() bool { return tt.HasText("确认推送") })
	if err := tt.Click("确认推送"); err != nil {
		t.Fatalf("click push-pr confirm failed: %v", err)
	}
	waitFor(t, tt, func() bool { return len(triggerAutoMergeCallsOf(fake)) == 1 })
	if got := triggerAutoMergeCallsOf(fake)[0]; got != "t9" {
		t.Fatalf("TriggerAutoMerge lead = %q, want t9", got)
	}
}

// 二次确认流程的进入与取消都不触发推送。
func TestChatHeaderPushPRConfirm(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)

	if pushConfirmOf(s) {
		t.Fatal("pushConfirm 初始应为 false")
	}
	s.requestPushPRConfirm()
	if !pushConfirmOf(s) {
		t.Fatal("requestPushPRConfirm 后 pushConfirm 应为 true")
	}
	if calls := triggerAutoMergeCallsOf(fake); len(calls) != 0 {
		t.Fatalf("确认前不应触发推送, calls=%v", calls)
	}
	s.cancelPushPRConfirm()
	if pushConfirmOf(s) {
		t.Fatal("cancelPushPRConfirm 后 pushConfirm 应为 false")
	}
	if calls := triggerAutoMergeCallsOf(fake); len(calls) != 0 {
		t.Fatalf("取消后不应触发推送, calls=%v", calls)
	}
}

// pushConfirmOf 读取头部状态上的 pushConfirm 标志。
func pushConfirmOf(s *chatState) bool {
	chatHeaderMu.Lock()
	defer chatHeaderMu.Unlock()
	hs := chatHeaderStates[s]
	return hs != nil && hs.pushConfirm
}

// headerParentThread 依据 Swarm 面板状态判定父会话（lead）并从会话列表取标题。
func TestChatHeaderParentThread(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "bee1"
	s.threads = []Thread{
		{ID: "bee1", Title: "子会话"},
		{ID: "lead1", Title: "主会话 A"},
	}
	hp := getSwarmPanelState(s)

	// 面板状态不是为当前会话加载的：不算父会话
	hp.loadedFor = "other"
	hp.state.LeadThreadID = "lead1"
	if parent, ok := s.headerParentThread(); ok {
		t.Fatalf("面板未加载时不应返回父会话, parent=%+v", parent)
	}

	// 已加载且 lead 与选中会话不同：返回 lead 对应的 Thread
	hp.loadedFor = "bee1"
	parent, ok := s.headerParentThread()
	if !ok || parent.ID != "lead1" || parent.Title != "主会话 A" {
		t.Fatalf("parent=%+v ok=%v, want lead1/主会话 A", parent, ok)
	}

	// lead 等于选中会话：不算父会话
	hp.state.LeadThreadID = "bee1"
	if parent, ok := s.headerParentThread(); ok {
		t.Fatalf("lead 等于选中会话时不应返回父会话, parent=%+v", parent)
	}

	// lead 不在本地会话列表：用标题「主会话」兜底
	hp.state.LeadThreadID = "lead2"
	parent, ok = s.headerParentThread()
	if !ok || parent.ID != "lead2" || parent.Title != "主会话" {
		t.Fatalf("兜底 parent=%+v ok=%v, want lead2/主会话", parent, ok)
	}
}

// 脚本拉取失败后按指数退避，不再按帧重试：失败后写入重试时间戳，退避期内 loadHeaderScripts
// 直接返回（scriptsSeq 不递增），退避到期后才允许再发起，成功后清空失败记录。
func TestChatHeaderScripts_RetryBackoff(t *testing.T) {
	if got := headerScriptsRetryDelay(1); got != 2*time.Second {
		t.Fatalf("首次失败应退避 2s，got %v", got)
	}
	if got := headerScriptsRetryDelay(2); got != 4*time.Second {
		t.Fatalf("连续第二次失败应退避 4s，got %v", got)
	}
	if got := headerScriptsRetryDelay(10); got != headerScriptsRetryMax {
		t.Fatalf("退避应封顶 %v，got %v", headerScriptsRetryMax, got)
	}

	fake := NewFakeClient()
	fake.SetScriptsErr(errors.New("boom"))
	s := newChatState(fake)
	snapshot := func() (seq uint64, failures int, retryAt time.Time) {
		hs := getChatHeaderState(s)
		chatHeaderMu.Lock()
		defer chatHeaderMu.Unlock()
		return hs.scriptsSeq["t1"], hs.scriptsFailures["t1"], hs.scriptsRetryAt["t1"]
	}
	// waitHeader 轮询头部状态直到 cond 成立；返回最后一次快照。
	waitHeader := func(desc string, cond func(seq uint64, failures int, retryAt time.Time) bool) (uint64, int, time.Time) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			seq, failures, retryAt := snapshot()
			if cond(seq, failures, retryAt) {
				return seq, failures, retryAt
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s：超时未达成，seq=%d failures=%d retryAt=%v", desc, seq, failures, retryAt)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	// expireScriptsRetry 把退避时间推到过去，模拟退避到期。
	expireScriptsRetry := func() {
		hs := getChatHeaderState(s)
		chatHeaderMu.Lock()
		hs.scriptsRetryAt["t1"] = time.Now().Add(-time.Second)
		chatHeaderMu.Unlock()
	}

	hasCachedScripts := func() bool {
		hs := getChatHeaderState(s)
		chatHeaderMu.Lock()
		defer chatHeaderMu.Unlock()
		_, ok := hs.scripts["t1"]
		return ok
	}

	s.loadHeaderScripts("t1")
	waitHeader("首次失败应记录退避时间", func(seq uint64, failures int, retryAt time.Time) bool {
		return seq == 1 && failures == 1 && !retryAt.IsZero()
	})
	if hasCachedScripts() {
		t.Fatal("失败时不应缓存脚本列表")
	}

	// 退避期内再调用不应发起新的拉取（只增 scriptsSeq 表示真正发起了请求）
	s.loadHeaderScripts("t1")
	if seq, _, _ := snapshot(); seq != 1 {
		t.Fatalf("退避期内不应重新拉取脚本，scriptsSeq=%d", seq)
	}

	// 退避到期后允许再试，且连续失败计数递增
	expireScriptsRetry()
	s.loadHeaderScripts("t1")
	waitHeader("退避到期后应重新拉取并再次失败", func(seq uint64, failures int, retryAt time.Time) bool {
		return seq == 2 && failures == 2 && !retryAt.IsZero()
	})

	// 成功后清空失败记录与退避时间
	fake.SetScriptsErr(nil)
	fake.SetScripts([]Script{{Name: "dev", Command: "npm run dev"}})
	expireScriptsRetry()
	s.loadHeaderScripts("t1")
	waitHeader("成功后应清除退避记录", func(seq uint64, failures int, retryAt time.Time) bool {
		return failures == 0 && retryAt.IsZero()
	})
	if got := len(scriptsOf(s, "t1")); got != 1 {
		t.Fatalf("成功后应缓存 1 个脚本，got %d", got)
	}
}

// scriptsOf 在锁内读取脚本缓存条数。
func scriptsOf(s *chatState, threadID string) []Script {
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	defer chatHeaderMu.Unlock()
	return append([]Script(nil), hs.scripts[threadID]...)
}

// 验证非 Git 项目下 Bee 线程与 Lead 线程正确渲染提示胶囊，Git 项目不渲染。
func TestChatHeaderNonGitSwarmCapsule(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Run("BeeSharedNoGit", func(t *testing.T) {
		fake := NewFakeClient()
		fake.threads["p1"] = []Thread{
			{ID: "t1", ProjectID: "p1", Title: "Lead 会话"},
			{ID: "bee-1", ProjectID: "p1", Title: "Bee 会话", IsHelper: true, ParentThreadID: "t1"},
		}
		fake.SetSwarmState("bee-1", SwarmState{
			LeadThreadID: "t1",
			Available:    true,
			IsBee:        true,
			Bees: []SwarmBee{
				{
					ThreadID:          "bee-1",
					Name:              "Bee 1",
					SharedNoGit:       true,
					SharedNoGitReason: "影子仓库初始化失败",
				},
			},
		})
		s, root := buildRoot(fake)
		s.selectThread("bee-1")
		s.loadSwarm("bee-1")
		tt := ui.NewTester(root, 1000, 700)
		waitFor(t, tt, func() bool { return tt.HasText("Bee 会话") })
		if tt.HasText("共享工作区 · 非 Git 项目") {
			t.Fatal("非 Git 项目不应显示分支胶囊")
		}
	})

	t.Run("BeeShadowRepo", func(t *testing.T) {
		fake := NewFakeClient()
		fake.threads["p1"] = []Thread{
			{ID: "t1", ProjectID: "p1", Title: "Lead 会话"},
			{ID: "bee-2", ProjectID: "p1", Title: "Bee 会话", IsHelper: true, ParentThreadID: "t1"},
		}
		fake.SetSwarmState("bee-2", SwarmState{
			LeadThreadID: "t1",
			Available:    true,
			IsBee:        true,
			Bees: []SwarmBee{
				{
					ThreadID:   "bee-2",
					Name:       "Bee 2",
					ShadowRepo: true,
				},
			},
		})
		s, root := buildRoot(fake)
		s.selectThread("bee-2")
		s.loadSwarm("bee-2")
		tt := ui.NewTester(root, 1000, 700)
		waitFor(t, tt, func() bool { return tt.HasText("Bee 会话") })
		if tt.HasText("影子仓库 · 非 Git 项目") {
			t.Fatal("非 Git 项目不应显示分支胶囊")
		}
	})

	t.Run("LeadWithBeesSharedNoGitPriority", func(t *testing.T) {
		fake := NewFakeClient()
		fake.threads["p1"] = []Thread{
			{ID: "t1", ProjectID: "p1", Title: "Lead 会话"},
		}
		fake.SetSwarmState("t1", SwarmState{
			LeadThreadID: "t1",
			Available:    true,
			IsBee:        false,
			Bees: []SwarmBee{
				{
					ThreadID:   "bee-a",
					Name:       "Bee A",
					ShadowRepo: true,
				},
				{
					ThreadID:          "bee-b",
					Name:              "Bee B",
					SharedNoGit:       true,
					SharedNoGitReason: "未配置影子仓库",
				},
			},
		})
		s, root := buildRoot(fake)
		s.selectThread("t1")
		s.loadSwarm("t1")
		tt := ui.NewTester(root, 1000, 700)
		waitFor(t, tt, func() bool { return tt.HasText("Lead 会话") })
		if tt.HasText("共享工作区 · 非 Git 项目") {
			t.Fatal("非 Git 项目不应显示分支胶囊")
		}
	})

	t.Run("GitRepoDoesNotShow", func(t *testing.T) {
		fake := newGitFakeClient()
		fake.SetSwarmState("t1", SwarmState{
			LeadThreadID: "t1",
			Available:    true,
			Bees: []SwarmBee{
				{
					ThreadID:    "bee-1",
					Name:        "Bee 1",
					SharedNoGit: true,
				},
			},
		})
		s, root := buildRoot(fake)
		s.selectThread("t1")
		s.loadSwarm("t1")
		tt := ui.NewTester(root, 1000, 700)
		waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
		if tt.HasText("共享工作区 · 非 Git 项目") || tt.HasText("影子仓库 · 非 Git 项目") {
			t.Fatal("Git 项目中不应显示非 Git 胶囊")
		}
	})
}

func TestHeaderCollapseLevelBoundary(t *testing.T) {
	if got := headerCollapseLevel(760); got != 0 {
		t.Errorf("760 宽度的收起级别 = %d，期望 0", got)
	}
	if got := headerCollapseLevel(759); got == 0 {
		t.Error("759 宽度应收起头部控件")
	}
}

// TestChatHeaderLeftClusterOrder 验证头部左右布局：编排、权限、分支胶囊依次排在标题左侧，
// 右侧是两个胶囊组（动作组、辅助面板组）。
func TestChatHeaderLeftClusterOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubTerminals(t)
	fake := newGitFakeClient()
	s, root := buildRoot(fake)
	t.Cleanup(func() { closeTerminals(s) })
	tt := ui.NewTester(root, 1400, 700)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find(headerPermissionCapsuleLabel)
		return ok && tt.HasText("feature/login")
	})

	swarm, ok := tt.Find("编排")
	if !ok {
		t.Fatal("头部缺少编排按钮")
	}
	perm, ok := tt.Find(headerPermissionCapsuleLabel)
	if !ok {
		t.Fatal("头部缺少权限胶囊")
	}
	branch, ok := tt.Find("Git 分支")
	if !ok {
		t.Fatal("头部缺少分支胶囊")
	}
	// 标题按钮按 Grow(1) 占满左侧剩余宽度，左边紧接分支胶囊。
	title, ok := tt.Find("重命名会话")
	if !ok {
		t.Fatal("头部缺少会话标题")
	}
	if !(swarm.X < perm.X && perm.X+perm.W <= branch.X && branch.X+branch.W <= title.X) {
		t.Fatalf("编排/权限/分支应依次排在标题左侧：编排 %+v 权限 %+v 分支 %+v 标题 %+v", swarm, perm, branch, title)
	}

	commit, ok := tt.Find("提交与推送")
	if !ok {
		t.Fatal("头部缺少动作胶囊组的提交按钮")
	}
	if title.X+title.W > commit.X {
		t.Fatalf("动作胶囊组应排在标题之后：标题 %+v 提交 %+v", title, commit)
	}
	contextBtn, ok := tt.Find("项目文档 (.mcode/context) ⇧⌘K")
	if !ok {
		t.Fatal("辅助面板组缺少项目文档按钮")
	}
	if contextBtn.X <= commit.X {
		t.Fatalf("辅助面板胶囊组应在动作胶囊组右侧：项目文档 %+v 提交 %+v", contextBtn, commit)
	}
	if contextBtn.W != headerCapsuleButtonSize || contextBtn.H != headerCapsuleButtonSize {
		t.Fatalf("辅助面板按钮尺寸 = %v×%v，期望 %v×%v", contextBtn.W, contextBtn.H, headerCapsuleButtonSize, headerCapsuleButtonSize)
	}

	// 按钮外侧一格是胶囊内边距，再往外一格是头部底色，两者不同。
	img := tt.Image()
	pixel := func(x, y int) [3]int {
		i := img.PixOffset(x, y)
		return [3]int{int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])}
	}
	y := int(contextBtn.Y + contextBtn.H/2)
	inside, outside := pixel(int(contextBtn.X)-1, y), pixel(int(contextBtn.X)-3, y)
	if colorNear(inside, outside, 2) {
		t.Fatalf("辅助面板组没有胶囊底色：胶囊内 %v 与头部底色 %v 相同", inside, outside)
	}
	y = int(commit.Y + commit.H/2)
	inside, outside = pixel(int(commit.X+commit.W)+1, y), pixel(int(commit.X+commit.W)+3, y)
	if colorNear(inside, outside, 2) {
		t.Fatalf("动作组没有胶囊底色：胶囊内 %v 与头部底色 %v 相同", inside, outside)
	}
}
