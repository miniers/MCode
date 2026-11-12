package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
	"reflect"
	"strings"
	"testing"
)

func TestGitBranchPopoverFilter(t *testing.T) {
	if !branchMatches("Feature/Login", "LOGIN") || branchMatches("main", "login") {
		t.Fatal("分支搜索应忽略大小写并按子串匹配")
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
	tt := ui.NewTester(Root(f), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
	if err := tt.Click("Git 分支"); err != nil {
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
	if err := tt.Click("Git 分支"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("分支操作 other"); return ok })
	waitFor(t, tt, func() bool { r, ok := tt.Find("分支操作 other"); return ok && r.W > 0 })
	before, ok := tt.Find("切换到分支 unmerged")
	if !ok {
		t.Fatal("未找到后续分支")
	}
	if _, ok := tt.Find("复制名称"); ok {
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
	waitFor(t, tt, func() bool { _, ok := tt.Find("复制名称"); return ok })
	after, ok := tt.Find("切换到分支 unmerged")
	if !ok || after.Y != before.Y {
		t.Fatal("打开菜单不应改变列表中后续分支的位置")
	}
	if err := tt.Click("复制名称"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("复制名称"); return !ok })
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
			tt := ui.NewTester(Root(f), 1000, 700)
			waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
			if err := tt.Click("Git 分支"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, tt, func() bool { _, ok := tt.Find("切换到分支 unmerged"); return ok })
			if err := tt.Click("切换到分支 unmerged"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, tt, func() bool { return tt.HasText("先提交或贮藏改动") })
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
