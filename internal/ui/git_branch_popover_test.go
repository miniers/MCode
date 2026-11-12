package mcodeui

import (
	"context"
	"errors"
	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
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
	return []git.BranchInfo{{Name: "feature/login", Head: true}, {Name: "other", WorktreePath: "/repo-wt/hotfix"}, {Name: "unmerged"}}, nil
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
