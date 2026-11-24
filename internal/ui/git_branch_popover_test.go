package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
	"mcode/internal/testenv"
)

func TestGitBranchPopoverFilter(t *testing.T) {
	if !branchMatches("Feature/Login", "LOGIN") || branchMatches("main", "login") {
		t.Fatal("分支搜索应忽略大小写并按子串匹配")
	}
}

type blockedBranchLoadFake struct {
	*branchPopoverFake
	release chan struct{}
}

func (f *blockedBranchLoadFake) GitBranches(ctx context.Context, _ string) ([]git.BranchInfo, error) {
	select {
	case <-f.release:
		return []git.BranchInfo{{Name: "new-branch"}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestGitBranchPopoverReopenCached(t *testing.T) {
	f := &blockedBranchLoadFake{branchPopoverFake: &branchPopoverFake{newGitFakeClient()}, release: make(chan struct{})}
	s := newChatState(f)
	defer s.close()
	defer close(f.release)
	p := branchPopoverFor(s, "thread")
	p.loaded = true
	p.branches = []git.BranchInfo{{Name: "cached-branch"}}
	openBranchPopover(s, "thread")
	p.mu.Lock()
	p.loadStarted = time.Now().Add(-time.Second)
	p.mu.Unlock()
	open := true
	tt := ui.NewTester(func(c *ui.Context) { renderGitBranchPopoverContent(c, s, "thread", 300, &open) }, 800, 600)
	if !tt.HasText("cached-branch") || tt.HasText("正在加载…") {
		t.Fatal("后台刷新时应显示缓存分支且不显示加载行")
	}
	p.mu.Lock()
	p.loaded = false
	p.mu.Unlock()
	tt.Frame()
	if !tt.HasText("正在加载…") {
		t.Fatal("首次加载应显示加载行")
	}
}

func TestGitBranchPopoverOtherWorktree(t *testing.T) {
	b := git.BranchInfo{WorktreePath: "/repo/other"}
	if !branchOtherWorktree(b, "/repo/main") || branchOtherWorktree(b, "/repo/other/.") {
		t.Fatal("worktree 路径判断错误")
	}
}

type branchPopoverFake struct{ *FakeClient }

func (f *branchPopoverFake) GitBranches(context.Context, string) ([]git.BranchInfo, error) {
	return []git.BranchInfo{{Name: "feature/login", Head: true}, {Name: "other", WorktreePath: "/repo-wt/hotfix"}, {Name: "unmerged"}, {Name: strings.Repeat("a", 60)}}, nil
}
func (f *branchPopoverFake) GitRecentBranches(context.Context, string, int) ([]string, error) {
	return nil, nil
}
func (f *branchPopoverFake) GitBranchRename(context.Context, string, string, string) error {
	return nil
}
func (f *branchPopoverFake) GitBranchDelete(_ context.Context, _ string, _ string, force bool) error {
	if !force {
		return errors.New("not fully merged")
	}
	return nil
}
func (f *branchPopoverFake) GitCreateBranchAt(context.Context, string, string, string, bool) error {
	return nil
}
func (f *branchPopoverFake) GitCheckoutRemoteBranch(context.Context, string, string) (string, error) {
	return "", nil
}
func (f *branchPopoverFake) GitTags(context.Context, string) ([]git.TagInfo, error)   { return nil, nil }
func (f *branchPopoverFake) GitTagDelete(context.Context, string, string) error       { return nil }
func (f *branchPopoverFake) GitPushTag(context.Context, string, string, string) error { return nil }
func (f *branchPopoverFake) GitRebase(context.Context, string, string) error          { return nil }

func TestGitBranchPopoverWorktree(t *testing.T) {
	f := &branchPopoverFake{newGitFakeClient()}
	f.gitState.Changes = 0
	tt := ui.NewTester(Root(f), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
	if err := tt.Click("切换分支与同步"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("切换到分支 other"); return ok })
	if err := tt.Click("切换到分支 other"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.gitWorktreeCalls) == 1 && f.gitWorktreeCalls[0] == "/repo-wt/hotfix"
	})
}

func TestGitBranchPopoverForceDelete(t *testing.T) {
	if !branchDeleteNeedsForce(errors.New("branch is not fully merged")) || branchDeleteNeedsForce(errors.New("permission denied")) || branchDeleteNeedsForce(nil) {
		t.Fatal("强制删除确认判断错误")
	}
}

func TestGitBranchPopoverActionMenu(t *testing.T) {
	f := &branchPopoverFake{newGitFakeClient()}
	tt := ui.NewTester(Root(f), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
	if err := tt.Click("切换分支与同步"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("分支操作 other"); return ok })
	waitFor(t, tt, func() bool { r, ok := tt.Find("分支操作 other"); return ok && r.W > 0 })
	before, ok := tt.Find("切换到分支 unmerged")
	if !ok {
		t.Fatal("未找到后续分支")
	}
	if _, ok := tt.Find("复制分支名"); ok {
		t.Fatal("菜单打开前不应显示操作项")
	}
	row, ok := tt.Find("切换到分支 other")
	if !ok {
		t.Fatal("未找到分支 other")
	}
	tt.Move(row.X+row.W/2, row.Y+row.H/2)
	tt.Frame()
	more, _ := tt.Find("分支操作 other")
	if more.W <= 0 {
		t.Fatalf("分支操作按钮宽度应大于零：%v，行 %v", more, row)
	}
	longMore, ok := tt.Find("分支操作 " + strings.Repeat("a", 60))
	card, cardOK := tt.Find("分支列表")
	if !ok || !cardOK || longMore.W <= 0 || longMore.X+longMore.W > card.X+card.W {
		t.Fatalf("长分支操作按钮应位于卡片内且有正宽度：按钮 %v，卡片 %v", longMore, card)
	}
	if err := tt.Click("分支操作 other"); err != nil {
		t.Fatal(err)
	}
	// 比较菜单打开前后的布局时保持动画时钟不变。
	deadline := time.Now().Add(uiWaitTimeout)
	for time.Now().Before(deadline) {
		tt.Frame()
		if _, ok := tt.Find("复制分支名"); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := tt.Find("复制分支名"); !ok {
		t.Fatal("菜单未打开")
	}
	after, ok := tt.Find("切换到分支 unmerged")
	if !ok || after.Y != before.Y {
		t.Fatal("打开菜单不应改变列表中后续分支的位置")
	}
	if err := tt.Click("复制分支名"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("复制分支名"); return !ok })
}

type branchStashFake struct {
	*branchPopoverFake
	calls     []string
	failRetry bool
}

func (f *branchStashFake) GitCheckout(_ context.Context, _ string, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "checkout:"+branch)
	if len(f.calls) == 1 || f.failRetry {
		return errors.New("would be overwritten by checkout")
	}
	return nil
}
func (f *branchStashFake) GitStashPush(_ context.Context, _ string, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "push:"+message)
	return nil
}
func (f *branchStashFake) GitStashList(context.Context, string) ([]git.StashEntry, error) {
	return nil, nil
}
func (f *branchStashFake) GitStashApply(context.Context, string, int) error { return nil }
func (f *branchStashFake) GitStashDrop(context.Context, string, int) error  { return nil }
func (f *branchStashFake) GitStashPopAt(_ context.Context, _ string, index int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("pop:%d", index))
	return nil
}

func TestGitBranchPopoverStashSwitch(t *testing.T) {
	for _, failRetry := range []bool{false, true} {
		t.Run(fmt.Sprintf("retry_failure_%v", failRetry), func(t *testing.T) {
			f := &branchStashFake{branchPopoverFake: &branchPopoverFake{newGitFakeClient()}, failRetry: failRetry}
			f.gitState.Changes = 0
			tt := ui.NewTester(Root(f), 1000, 700)
			waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
			if err := tt.Click("切换分支与同步"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, tt, func() bool { _, ok := tt.Find("切换到分支 unmerged"); return ok })
			if err := tt.Click("切换到分支 unmerged"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, tt, func() bool { return tt.HasText("当前有未提交改动") })
			if err := tt.Click("贮藏后切换"); err != nil {
				t.Fatal(err)
			}
			want := []string{"checkout:unmerged", "push:" + gitSwitchStashMarker, "checkout:unmerged"}
			if failRetry {
				want = append(want, "pop:0")
			}
			waitFor(t, tt, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) >= len(want) })
			f.mu.Lock()
			got := append([]string(nil), f.calls...)
			f.mu.Unlock()
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("调用顺序：%v，期望 %v", got, want)
			}
		})
	}
}

type delayedBranchPopoverFake struct {
	*branchPopoverFake
	started chan struct{}
	release chan struct{}
}

func (f *delayedBranchPopoverFake) GitCheckout(context.Context, string, string) error {
	close(f.started)
	<-f.release
	return nil
}

func TestGitBranchPopoverBusyAndToast(t *testing.T) {
	f := &delayedBranchPopoverFake{branchPopoverFake: &branchPopoverFake{newGitFakeClient()}, started: make(chan struct{}), release: make(chan struct{})}
	f.gitState.Changes = 0
	s, root := buildRoot(f)
	tt := ui.NewTester(root, 1000, 700)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(f.release) }) }
	defer release()
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
	if err := tt.Click("切换分支与同步"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("切换到分支 unmerged"); return ok })
	if err := tt.Click("切换到分支 unmerged"); err != nil {
		t.Fatal(err)
	}
	<-f.started
	p := branchPopoverFor(s, "t1")
	p.mu.Lock()
	busy, name := p.busy, p.busyName
	p.mu.Unlock()
	if !busy || name != "unmerged" {
		t.Fatalf("busy = %v, busyName = %q", busy, name)
	}
	release()
	waitFor(t, tt, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return !p.busy && p.busyName == "" })
	waitFor(t, tt, func() bool { return tt.HasText("已切换到 unmerged") })
}

type designPopoverFake struct {
	*branchStashFake
}

func (f *designPopoverFake) GitBranches(context.Context, string) ([]git.BranchInfo, error) {
	return []git.BranchInfo{
		{Name: "feature/login", Head: true},
		{Name: "other", WorktreePath: "/repo-wt/hotfix"},
		{Name: "main"},
		{Name: "origin/main", Remote: true},
		{Name: "origin/feature/login", Remote: true},
	}, nil
}

func (f *designPopoverFake) GitRecentBranches(context.Context, string, int) ([]string, error) {
	return []string{"feature/login", "other"}, nil
}

func (f *designPopoverFake) GitTags(context.Context, string) ([]git.TagInfo, error) {
	return []git.TagInfo{{Name: "v1.0.0"}}, nil
}

func TestGitBranchPopoverMatchesDesign(t *testing.T) {
	testenv.TempHome(t)
	stubTerminals(t)

	base := newGitFakeClient()
	base.gitState.RemoteURL = "https://github.com/org/repo"
	base.gitState.HasUpstream = false
	base.gitState.Changes = 2
	f := &designPopoverFake{branchStashFake: &branchStashFake{branchPopoverFake: &branchPopoverFake{base}}}

	s, root := buildRoot(f)
	t.Cleanup(func() {
		closeTerminals(s)
		s.close()
	})
	tt := ui.NewTesterWith(root, 1000, 700, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1}})
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })

	if err := tt.Click("切换分支与同步"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("最近") && tt.HasText("全部本地")
	})

	if node, ok := tt.Access("搜索分支"); !ok || node.Placeholder != "搜索或输入新分支名…" {
		t.Fatalf("搜索框占位符不匹配：ok=%v node=%+v", ok, node)
	}
	if _, ok := tt.Find("新建分支"); !ok {
		t.Fatal("缺少「新建分支」按钮")
	}
	if !tt.HasText("发布分支") || !tt.HasText("尚未发布到远端 · 2 改动") {
		t.Fatalf("同步条缺少「发布分支」或状态文案，texts=%q", tt.Texts())
	}
	for _, tab := range []string{"本地", "远端", "标签", "Worktree"} {
		if _, ok := tt.Find(tab); !ok {
			t.Fatalf("缺少 Tab %q，texts=%q", tab, tt.Texts())
		}
	}
	if _, ok := tt.Find("打开 Git 面板"); !ok {
		t.Fatal("缺少底部「打开 Git 面板」行")
	}
	if tt.HasText("创建 PR") || tt.HasText("迁入 Worktree…") {
		t.Fatalf("浮层不应再包含「创建 PR」或「迁入 Worktree…」，texts=%q", tt.Texts())
	}

	// 最近分支在本地 Tab 只出现一次，且不出现远端分支/标签。
	otherCount := 0
	for _, txt := range tt.Texts() {
		if txt == "other" {
			otherCount++
		}
	}
	if otherCount != 1 {
		t.Fatalf("最近分支 other 在本地 Tab 出现 %d 次，期望 1 次，texts=%q", otherCount, tt.Texts())
	}
	if tt.HasText("origin/main") || tt.HasText("v1.0.0") {
		t.Fatalf("本地 Tab 不应显示远端或标签，texts=%q", tt.Texts())
	}

	// 切到远端 Tab 后只显示远端分支。
	if err := tt.Click("远端"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("origin/main") })
	if _, ok := tt.Find("切换到分支 main"); ok {
		t.Fatalf("远端 Tab 不应显示本地分支 main，texts=%q", tt.Texts())
	}
	if _, ok := tt.Find("切换到分支 other"); ok {
		t.Fatalf("远端 Tab 不应显示本地分支 other，texts=%q", tt.Texts())
	}

	// 切回本地 Tab，有改动时点非当前分支出现「贮藏后切换」「查看改动」且未调用切换。
	if err := tt.Click("本地"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("切换到分支 main"); return ok })
	if err := tt.Click("切换到分支 main"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("贮藏后切换") && tt.HasText("查看改动")
	})
	f.mu.Lock()
	checkoutCalls := len(f.gitCheckoutCalls)
	stashCalls := len(f.calls)
	f.mu.Unlock()
	if checkoutCalls != 0 || stashCalls != 0 {
		t.Fatalf("有改动点击非当前分支不应调用切换：checkoutCalls=%d stashCalls=%d", checkoutCalls, stashCalls)
	}
}

func TestGitBranchPopoverPRRow(t *testing.T) {
	newEnv := func(t *testing.T, remote string, pr *git.PRSummary) (*chatState, *ui.Tester, *bool) {
		t.Helper()
		f := &branchPopoverFake{newGitFakeClient()}
		s := newChatState(f)
		t.Cleanup(s.close)
		hs := getChatHeaderState(s)
		st := f.gitState
		st.RemoteURL = remote
		chatHeaderMu.Lock()
		hs.git["thread"] = st
		if pr != nil {
			hs.pr["thread"] = pr
		}
		chatHeaderMu.Unlock()
		open := true
		tt := ui.NewTester(func(c *ui.Context) { renderGitBranchPopoverContent(c, s, "thread", 300, &open) }, 800, 600)
		return s, tt, &open
	}

	t.Run("有远端且有开放 PR", func(t *testing.T) {
		s, tt, open := newEnv(t, "https://github.com/org/repo", &git.PRSummary{Number: 42, State: "OPEN"})
		if _, ok := tt.Find("查看 PR"); !ok || !tt.HasText("#42") {
			t.Fatalf("应渲染「查看 PR」行并显示 #42，texts=%q", tt.Texts())
		}
		if err := tt.Click("查看 PR"); err != nil {
			t.Fatal(err)
		}
		if *open {
			t.Fatal("点击后浮层应关闭")
		}
		ds := getGitPRDialogState(s)
		ds.mu.Lock()
		dialogOpen, selected := ds.open, ds.selected
		ds.mu.Unlock()
		if !dialogOpen || selected != 42 {
			t.Fatalf("应打开 PR 弹窗并选中 #42：open=%v selected=%d", dialogOpen, selected)
		}
	})

	t.Run("有远端但无 PR", func(t *testing.T) {
		s, tt, open := newEnv(t, "https://github.com/org/repo", nil)
		if _, ok := tt.Find("查看 PR"); !ok || tt.HasText("#") {
			t.Fatalf("应渲染「查看 PR」行且不显示编号，texts=%q", tt.Texts())
		}
		if err := tt.Click("查看 PR"); err != nil {
			t.Fatal(err)
		}
		ds := getGitPRDialogState(s)
		ds.mu.Lock()
		dialogOpen, selected := ds.open, ds.selected
		ds.mu.Unlock()
		if *open || !dialogOpen || selected != 0 {
			t.Fatalf("应关闭浮层并打开 PR 弹窗且不选中：popover=%v open=%v selected=%d", *open, dialogOpen, selected)
		}
	})

	t.Run("无远端", func(t *testing.T) {
		_, tt, _ := newEnv(t, "", nil)
		if _, ok := tt.Find("查看 PR"); ok {
			t.Fatalf("无远端不应渲染「查看 PR」行，texts=%q", tt.Texts())
		}
		if _, ok := tt.Find("打开 Git 面板"); !ok {
			t.Fatal("底部「打开 Git 面板」行应保留")
		}
	})
}

func TestGitBranchPopoverRowHighlightSpansMoreButton(t *testing.T) {
	f := &branchPopoverFake{newGitFakeClient()}
	branch := f.gitState.Branch
	s := newChatState(f)
	defer s.close()
	s.loadGitState("t1")
	openBranchPopover(s, "t1")
	open := true
	tt := ui.NewTesterWith(func(c *ui.Context) {
		ui.Box(c).Fill().Padding(24).Children(func() {
			gitPaneBranchPopover(c, s, "t1", 320, &open)
		})
	}, 600, 500, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1}})
	p := branchPopoverFor(s, "t1")
	waitFor(t, tt, func() bool {
		p.mu.Lock()
		loaded := p.loaded
		p.mu.Unlock()
		_, btnOK := tt.Find("切换到分支 " + branch)
		_, moreOK := tt.Find("分支操作 " + branch)
		return loaded && btnOK && moreOK
	})
	btn, ok := tt.Find("切换到分支 " + branch)
	if !ok {
		t.Fatalf("找不到当前分支按钮: %s", branch)
	}
	more, ok := tt.Find("分支操作 " + branch)
	if !ok {
		t.Fatalf("找不到当前分支操作按钮: %s", branch)
	}
	img := tt.Image()
	btnPx := img.At(int(btn.X)+4, int(btn.Y)+3)
	morePx := img.At(int(more.X+more.W)-3, int(more.Y)+3)
	if btnPx != morePx {
		t.Fatalf("当前分支高亮应覆盖 ⋯ 按钮区域：btnPx=%v morePx=%v", btnPx, morePx)
	}
}
