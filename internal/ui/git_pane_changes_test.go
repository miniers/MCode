package mcodeui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

type gitChangesFakeClient struct {
	*gitPaneFakeClient
	snapshot *git.Snapshot
}

func TestGitChangesSelectionAcrossGroups(t *testing.T) {
	groups := gitChangesGroups([]git.StatusEntry{{Path: "a", Index: 'M'}, {Path: "b", Worktree: 'M'}, {Path: "c", Worktree: 'M'}})
	items := gitChangesVisibleItems(groups, [3]bool{})
	if got := gitChangesMove(items, "1:a", 1); got != (gitChangesItem{2, "b"}) {
		t.Fatalf("跨组移动 = %+v", got)
	}
	want := map[string]bool{"1:a": true, "2:b": true, "2:c": true}
	if got := gitChangesRange(items, "1:a", "2:c"); !reflect.DeepEqual(got, want) {
		t.Fatalf("范围选择 = %v", got)
	}
	if got := gitChangesRange(items, "1:a", "2:b"); len(got) != 2 || got["2:c"] {
		t.Fatalf("收缩范围 = %v", got)
	}
}

func TestGitChangesTruncateStart(t *testing.T) {
	got := gitChangesTruncateStart("长目录/文件夹", func(s string) bool { return len([]rune(s)) <= 4 })
	if got != "…文件夹" {
		t.Fatalf("截断 = %q", got)
	}
}

func TestGitChangesIgnoreEscapesPatterns(t *testing.T) {
	root := t.TempDir()
	if err := gitChangesIgnore(root, "a[b]*.txt"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "/a\\[b]\\*.txt\n" {
		t.Fatalf("忽略规则 = %q", data)
	}
}

func TestGitChangesDiscardConfirmFocus(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	s.client = &gitChangesFakeClient{gitPaneFakeClient: base}
	ps.state = GitState{WorkDir: t.TempDir()}
	ps.entries = []git.StatusEntry{{Path: "a", Worktree: 'M'}}
	st := gitChangesStateFor(ps)
	st.confirm = true
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneChanges(c, s, "git-test", ps) }, 600, 500)
	tt.Frame()
	if !tt.Focused("取消") {
		t.Fatal("默认焦点应在取消")
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	st.mu.Lock()
	open := st.confirm
	st.mu.Unlock()
	if open {
		t.Fatal("回车应取消丢弃")
	}
	base.paneMu.Lock()
	defer base.paneMu.Unlock()
	if len(base.calls) != 0 {
		t.Fatalf("取消后不应调用 Git：%v", base.calls)
	}
}

func (f *gitChangesFakeClient) GitStageFiles(context.Context, string, []string) error {
	f.record("stage")
	return nil
}
func (f *gitChangesFakeClient) GitUnstageFiles(context.Context, string, []string) error {
	f.record("unstage")
	return nil
}
func (f *gitChangesFakeClient) GitDiscardFiles(context.Context, string, []string) (*git.Snapshot, error) {
	f.record("discard")
	return f.snapshot, nil
}
func (f *gitChangesFakeClient) GitCommitIndex(context.Context, string, string, bool) (string, error) {
	f.record("commit")
	return "1234567", nil
}
func (f *gitChangesFakeClient) GitLastCommitMessage(context.Context, string) (string, error) {
	return "message", nil
}
func (f *gitChangesFakeClient) GitHeadPushed(context.Context, string) (bool, error) {
	return false, nil
}
func (f *gitChangesFakeClient) GitLog(context.Context, string, git.LogOptions) ([]git.LogCommit, error) {
	return nil, nil
}

func TestGitChangesGroups(t *testing.T) {
	groups := gitChangesGroups([]git.StatusEntry{
		{Path: "both", Index: 'M', Worktree: 'M'},
		{Path: "conflict", Index: 'U', Worktree: 'U'},
		{Path: "new", Index: '?', Worktree: '?'},
	})
	for i, want := range [][]string{{"conflict"}, {"both"}, {"both", "new"}} {
		got := gitChangesPaths(groups[i].entries)
		if len(got) != len(want) {
			t.Fatalf("group %d = %v", i, got)
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("group %d = %v", i, got)
			}
		}
	}
}

func TestGitChangesCommitLabel(t *testing.T) {
	for _, tc := range []struct {
		staged, changed int
		amend           bool
		want            string
	}{
		{2, 3, false, "提交 2 个文件"}, {0, 3, false, "提交全部 3 个文件"}, {2, 3, true, "修改上次提交"},
	} {
		if got := gitChangesCommitLabel(tc.staged, tc.changed, tc.amend); got != tc.want {
			t.Fatalf("got %q, want %q", got, tc.want)
		}
	}
}

func TestGitChangesCommitDisabled(t *testing.T) {
	if !gitChangesCommitDisabled("  \n", 1, false, false) {
		t.Fatal("空提交信息应禁用提交")
	}
	if gitChangesCommitDisabled("message", 1, false, false) {
		t.Fatal("有信息与改动时应允许提交")
	}
}

func TestGitChangesDiscardShowsUndo(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	snap := &git.Snapshot{}
	client := &gitChangesFakeClient{gitPaneFakeClient: base, snapshot: snap}
	gitChangesDiscard(ps, gitChangesStateFor(ps), client, "git-test", []string{"dir/a.txt"})
	gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return ps.undoSnap == snap })
	ps.mu.Lock()
	label := ps.undoLabel
	ps.mu.Unlock()
	if label != "已丢弃 a.txt" {
		t.Fatalf("undo label = %q", label)
	}
	_ = s
}

func TestGitChangesStageSelectsNext(t *testing.T) {
	_, ps, base := gitPaneTestState(t)
	client := &gitChangesFakeClient{gitPaneFakeClient: base}
	st := gitChangesStateFor(ps)
	st.selected = "a"
	gitChangesStageSelected(ps, st, client, "git-test", gitChangesGroups([]git.StatusEntry{{Path: "a", Worktree: 'M'}, {Path: "b", Worktree: 'M'}}), map[string]bool{"2:a": true}, gitChangesItem{2, "a"})
	gitPaneWait(t, func() bool { st.mu.Lock(); defer st.mu.Unlock(); return st.selected == "b" })
	base.paneMu.Lock()
	defer base.paneMu.Unlock()
	if len(base.calls) == 0 || base.calls[0] != "stage" {
		t.Fatalf("calls = %v", base.calls)
	}
}

func TestGitChangesKeyboardNavigation(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	s.client = &gitChangesFakeClient{gitPaneFakeClient: base}
	ps.state = GitState{WorkDir: t.TempDir()}
	ps.entries = []git.StatusEntry{{Path: "staged.txt", Index: 'M'}, {Path: "a.txt", Worktree: 'M'}, {Path: "b.txt", Worktree: 'M'}}
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneChanges(c, s, "git-test", ps) }, 800, 600)
	tt.Frame()
	tt.Frame()
	if err := tt.Click("staged.txt"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	st := gitChangesStateFor(ps)
	st.mu.Lock()
	selected, group := st.selected, st.group
	st.mu.Unlock()
	if selected != "a.txt" || group != 2 {
		t.Fatalf("向下选择 = %q，组 %d", selected, group)
	}
	tt.Key(ui.Shift, ui.KeyDown)
	tt.Frame()
	st.mu.Lock()
	selectedRange := len(st.selection)
	st.mu.Unlock()
	if selectedRange != 2 {
		t.Fatalf("范围选择数量 = %d", selectedRange)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Frame()
	st.mu.Lock()
	selectedAll := len(st.selection)
	st.mu.Unlock()
	if selectedAll != 3 {
		t.Fatalf("全选数量 = %d", selectedAll)
	}
}

func TestGitChangesSpaceStagesAndMoves(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	s.client = &gitChangesFakeClient{gitPaneFakeClient: base}
	ps.state = GitState{WorkDir: t.TempDir()}
	ps.entries = []git.StatusEntry{{Path: "a.txt", Worktree: 'M'}, {Path: "b.txt", Worktree: 'M'}}
	base.state, base.entries = ps.state, append([]git.StatusEntry(nil), ps.entries...)
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneChanges(c, s, "git-test", ps) }, 800, 600)
	tt.Frame()
	tt.Frame()
	if err := tt.Click("a.txt"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeySpace)
	tt.Frame()
	st := gitChangesStateFor(ps)
	gitPaneWait(t, func() bool { st.mu.Lock(); defer st.mu.Unlock(); return st.selected == "b.txt" })
	base.paneMu.Lock()
	defer base.paneMu.Unlock()
	if len(base.calls) == 0 || base.calls[0] != "stage" {
		t.Fatalf("调用 = %v", base.calls)
	}
}

func TestGitChangesEmptyMessageButtonDoesNotCommit(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	s.client = &gitChangesFakeClient{gitPaneFakeClient: base}
	ps.state = GitState{WorkDir: t.TempDir()}
	ps.entries = []git.StatusEntry{{Path: "a.txt", Worktree: 'M'}}
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneChanges(c, s, "git-test", ps) }, 800, 600)
	if err := tt.Click("提交全部 1 个文件"); err != nil {
		t.Fatal(err)
	}
	base.paneMu.Lock()
	defer base.paneMu.Unlock()
	if len(base.calls) != 0 {
		t.Fatalf("空信息时调用了 Git：%v", base.calls)
	}
}

func TestGitChangesNarrowDiffKeyboard(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	s.client = &gitChangesFakeClient{gitPaneFakeClient: base}
	ps.state = GitState{WorkDir: t.TempDir()}
	ps.entries = []git.StatusEntry{{Path: "a.txt", Worktree: 'M'}, {Path: "b.txt", Worktree: 'M'}}
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneChanges(c, s, "git-test", ps) }, 500, 600)
	tt.Frame()
	tt.Frame()
	if err := tt.Click("a.txt"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(0, ui.KeyBracketRight)
	tt.Frame()
	st := gitChangesStateFor(ps)
	st.mu.Lock()
	selected, detail := st.selected, st.detail
	st.mu.Unlock()
	if selected != "b.txt" || !detail {
		t.Fatalf("diff 选择 = %q，详情 = %v", selected, detail)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	st.mu.Lock()
	detail = st.detail
	st.mu.Unlock()
	if detail {
		t.Fatal("Esc 应返回文件列表")
	}
}

func TestGitChangesNarrowDiffKeyboardAfterReopen(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	s.client = &gitChangesFakeClient{gitPaneFakeClient: base}
	ps.state = GitState{WorkDir: t.TempDir()}
	ps.entries = []git.StatusEntry{{Path: "a.txt", Worktree: 'M'}, {Path: "b.txt", Worktree: 'M'}}
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneChanges(c, s, "git-test", ps) }, 500, 600)
	tt.Frame()
	tt.Frame()
	if err := tt.Click("a.txt"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if err := tt.Click("b.txt"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(0, ui.KeyBracketLeft)
	tt.Frame()
	st := gitChangesStateFor(ps)
	st.mu.Lock()
	selected, detail := st.selected, st.detail
	st.mu.Unlock()
	if selected != "a.txt" || !detail {
		t.Fatalf("重新进入后 diff 选择 = %q，详情 = %v", selected, detail)
	}
}
