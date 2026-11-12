package mcodeui

import (
	"context"
	"errors"
	"fmt"
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
	paths    []string
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

func (f *gitChangesFakeClient) GitStageFiles(_ context.Context, _ string, paths []string) error {
	f.paneMu.Lock()
	f.paths = append([]string(nil), paths...)
	f.paneMu.Unlock()
	f.record("stage")
	return nil
}
func (f *gitChangesFakeClient) GitUnstageFiles(_ context.Context, _ string, paths []string) error {
	f.paneMu.Lock()
	f.paths = append([]string(nil), paths...)
	f.paneMu.Unlock()
	f.record("unstage")
	return nil
}
func (f *gitChangesFakeClient) GitDiscardFiles(_ context.Context, _ string, paths []string) (*git.Snapshot, error) {
	f.paneMu.Lock()
	f.paths = append([]string(nil), paths...)
	f.paneMu.Unlock()
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

func TestGitChangesEmptyMessageCanAmend(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	s.client = &gitChangesFakeClient{gitPaneFakeClient: base}
	ps.state = GitState{WorkDir: t.TempDir()}
	ps.entries = []git.StatusEntry{{Path: "a.txt", Worktree: 'M'}}
	st := gitChangesStateFor(ps)
	st.root, st.autoGenerated = ps.state.WorkDir, true
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneChanges(c, s, "git-test", ps) }, 800, 600)
	tt.Frame()
	if err := tt.Click("提交选项"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("修改上次提交"); err != nil {
		t.Fatal(err)
	}
	gitPaneWait(t, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.amend
	})
}

func TestGitChangesClickSelectsFile(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	s.client = &gitChangesFakeClient{gitPaneFakeClient: base}
	ps.state = GitState{WorkDir: t.TempDir()}
	ps.entries = []git.StatusEntry{{Path: "a.txt", Worktree: 'M'}}
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneChanges(c, s, "git-test", ps) }, 800, 600)
	tt.Frame()
	if _, ok := tt.Find("a.txt"); !ok {
		t.Fatal("未显示文件行")
	}
	if err := tt.Click("a.txt"); err != nil {
		t.Fatal(err)
	}
	st := gitChangesStateFor(ps)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.selected != "a.txt" || !st.selection["2:a.txt"] {
		t.Fatalf("选中文件 = %q，选择集 = %v", st.selected, st.selection)
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

type gitChangesGenerateClient struct {
	*gitChangesFakeClient
	calls int
}

type gitChangesDelayedClient struct {
	*gitChangesFakeClient
	release chan struct{}
	err     error
	message string
}

func (f *gitChangesDelayedClient) GenerateCommitMessage(ctx context.Context, _ string) (string, error) {
	select {
	case <-f.release:
		return "生成的信息", f.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (f *gitChangesDelayedClient) GitCommitIndex(_ context.Context, _ string, message string, _ bool) (string, error) {
	f.paneMu.Lock()
	f.message = message
	f.calls = append(f.calls, "commit")
	f.paneMu.Unlock()
	return "1234567", nil
}

func TestGitChangesCommitAfterGeneration(t *testing.T) {
	for _, tc := range []struct {
		name               string
		cancel, fail, push bool
	}{
		{name: "commit"},
		{name: "cancel", cancel: true},
		{name: "error", fail: true},
		{name: "push", push: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ps, base := gitPaneTestState(t)
			client := &gitChangesDelayedClient{gitChangesFakeClient: &gitChangesFakeClient{gitPaneFakeClient: base}, release: make(chan struct{})}
			if tc.fail {
				client.err = errors.New("generation failed")
			}
			s.client = client
			ps.state = GitState{WorkDir: t.TempDir()}
			ps.entries = []git.StatusEntry{{Path: "a", Index: 'M'}}
			st := gitChangesStateFor(ps)
			tt := ui.NewTester(func(c *ui.Context) { renderGitPaneChanges(c, s, "git-test", ps) }, 600, 500)
			tt.Frame()
			tt.Frame()
			if tc.push {
				if err := tt.Click("提交选项"); err != nil {
					t.Fatal(err)
				}
				tt.Frame()
				if err := tt.Click("提交并推送"); err != nil {
					t.Fatal(err)
				}
			} else if err := tt.Click("提交 1 个文件"); err != nil {
				t.Fatal(err)
			}
			st.mu.Lock()
			queued := st.commitQueued
			st.mu.Unlock()
			if !queued {
				t.Fatal("未排队")
			}
			base.paneMu.Lock()
			calls := append([]string(nil), base.calls...)
			base.paneMu.Unlock()
			if len(calls) != 0 {
				t.Fatalf("生成期间调用 Git: %v", calls)
			}
			if tc.cancel {
				tt.Frame()
				if err := tt.Click("生成后提交"); err != nil {
					t.Fatal(err)
				}
			}
			close(client.release)
			gitPaneWait(t, func() bool { st.mu.Lock(); defer st.mu.Unlock(); return st.cancel == nil })
			tt.Frame()
			if !tc.cancel && !tc.fail {
				gitPaneWait(t, func() bool {
					base.paneMu.Lock()
					defer base.paneMu.Unlock()
					return len(base.calls) >= 1 && (!tc.push || len(base.calls) >= 2)
				})
			}
			base.paneMu.Lock()
			defer base.paneMu.Unlock()
			want := []string(nil)
			if !tc.cancel && !tc.fail {
				want = []string{"commit"}
				if tc.push {
					want = append(want, "push")
				}
			}
			if !reflect.DeepEqual(base.calls, want) || (len(want) > 0 && client.message != "生成的信息") {
				t.Fatalf("调用 = %v，信息 = %q，期望调用 = %v", base.calls, client.message, want)
			}
		})
	}
}

func (f *gitChangesGenerateClient) GenerateCommitMessage(context.Context, string) (string, error) {
	f.paneMu.Lock()
	f.calls++
	f.paneMu.Unlock()
	return "生成的信息", nil
}

func TestGitChangesAutoGenerate(t *testing.T) {
	for _, initial := range []string{"", "已有草稿"} {
		t.Run(initial, func(t *testing.T) {
			s, ps, base := gitPaneTestState(t)
			client := &gitChangesGenerateClient{gitChangesFakeClient: &gitChangesFakeClient{gitPaneFakeClient: base}}
			s.client = client
			ps.state = GitState{WorkDir: t.TempDir()}
			ps.entries = []git.StatusEntry{{Path: "a", Worktree: 'M'}}
			st := gitChangesStateFor(ps)
			st.root, st.draft = ps.state.WorkDir, initial
			tt := ui.NewTester(func(c *ui.Context) { renderGitPaneChanges(c, s, "git-test", ps) }, 600, 500)
			tt.Frame()
			if initial == "" {
				gitPaneWait(t, func() bool { st.mu.Lock(); defer st.mu.Unlock(); return st.draft == "生成的信息" })
			}
			for i := 0; i < 3; i++ {
				tt.Frame()
			}
			base.paneMu.Lock()
			calls := client.calls
			base.paneMu.Unlock()
			want := 0
			if initial == "" {
				want = 1
			}
			if calls != want {
				t.Fatalf("生成次数 = %d，期望 %d", calls, want)
			}
		})
	}
}

func TestGitChangesCommitButtons(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	s.client = &gitChangesFakeClient{gitPaneFakeClient: base}
	ps.state = GitState{WorkDir: t.TempDir()}
	ps.entries = []git.StatusEntry{{Path: "a", Worktree: 'M'}}
	st := gitChangesStateFor(ps)
	st.root, st.draft = ps.state.WorkDir, "已有草稿"
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneChanges(c, s, "git-test", ps) }, 600, 500)
	tt.Frame()
	tt.Frame()
	for _, label := range []string{"生成提交信息", "提交选项"} {
		r, ok := tt.Find(label)
		if !ok || r.W <= 0 || r.H <= 0 {
			t.Fatalf("按钮 %s 的尺寸 = %+v，存在 = %v", label, r, ok)
		}
	}
	if err := tt.Click("提交选项"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("提交并推送"); !ok {
		t.Fatal("未显示提交并推送")
	}
}

func TestGitChangesFilterBehavior(t *testing.T) {
	groups := gitChangesGroups([]git.StatusEntry{{Path: "Dir/Alpha.go", Worktree: 'M'}, {Path: "beta.go", Worktree: 'M'}})
	if got := gitChangesFilter(groups, ""); !reflect.DeepEqual(got, groups) {
		t.Fatalf("空过滤 = %v", got)
	}
	if got := gitChangesPaths(gitChangesFilter(groups, "ALPHA")[2].entries); !reflect.DeepEqual(got, []string{"Dir/Alpha.go"}) {
		t.Fatalf("大小写过滤 = %v", got)
	}
}

func TestGitChangesCommitConfirmationAndShortcut(t *testing.T) {
	for _, tc := range []struct {
		name   string
		busy   bool
		action string
	}{
		{"idle", false, "click"}, {"confirm", true, "提交"}, {"cancel", true, "取消"},
		{"message shortcut", false, "提交信息"}, {"list shortcut", false, "a.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ps, base := gitPaneTestState(t)
			s.client = &gitChangesFakeClient{gitPaneFakeClient: base}
			s.threads[0].IsRunning = tc.busy
			ps.state = GitState{WorkDir: t.TempDir()}
			ps.entries = []git.StatusEntry{{Path: "a.txt", Index: 'M'}}
			st := gitChangesStateFor(ps)
			st.root, st.draft = ps.state.WorkDir, "message"
			tt := ui.NewTester(func(c *ui.Context) { renderGitPaneChanges(c, s, "git-test", ps) }, 800, 600)
			tt.Frame()
			tt.Frame()
			if tc.action == "提交信息" || tc.action == "a.txt" {
				if err := tt.Click(tc.action); err != nil {
					t.Fatal(err)
				}
				tt.Key(ui.Cmd, ui.KeyEnter)
			} else if err := tt.Click("提交 1 个文件"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if tc.busy {
				if _, ok := tt.Find("Agent 仍在修改，仍要提交？"); !ok {
					t.Fatal("未显示确认窗")
				}
				base.paneMu.Lock()
				n := len(base.calls)
				base.paneMu.Unlock()
				if n != 0 {
					t.Fatalf("确认前调用 Git: %d", n)
				}
				if err := tt.Click(tc.action); err != nil {
					t.Fatal(err)
				}
			}
			if tc.action != "取消" {
				gitPaneWait(t, func() bool { base.paneMu.Lock(); defer base.paneMu.Unlock(); return len(base.calls) > 0 })
			}
			base.paneMu.Lock()
			calls := append([]string(nil), base.calls...)
			base.paneMu.Unlock()
			want := []string{"commit"}
			if tc.action == "取消" {
				want = nil
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("调用 = %v，期望 %v", calls, want)
			}
		})
	}
}

func TestGitChangesFilterShortcutAndWideNavigation(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	s.client = &gitChangesFakeClient{gitPaneFakeClient: base}
	ps.state = GitState{WorkDir: t.TempDir()}
	ps.entries = []git.StatusEntry{{Path: "a.txt", Worktree: 'M'}, {Path: "b.txt", Worktree: 'M'}}
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneChanges(c, s, "git-test", ps) }, 800, 600)
	tt.Frame()
	tt.Frame()
	if err := tt.Click("a.txt"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyBracketRight)
	tt.Frame()
	st := gitChangesStateFor(ps)
	st.mu.Lock()
	selected := st.selected
	st.mu.Unlock()
	if selected != "b.txt" {
		t.Fatalf("向后选择 = %q", selected)
	}
	tt.Key(0, ui.KeyBracketLeft)
	tt.Frame()
	st.mu.Lock()
	selected = st.selected
	st.mu.Unlock()
	if selected != "a.txt" {
		t.Fatalf("向前选择 = %q", selected)
	}
	tt.Key(ui.Cmd, ui.KeyF)
	tt.Frame()
	if _, ok := tt.Find("过滤"); !ok {
		t.Fatal("未显示过滤框")
	}
	st.mu.Lock()
	st.filter = "a.txt"
	st.mu.Unlock()
	tt.Frame()
	if _, ok := tt.Find("b.txt"); ok {
		t.Fatal("过滤后仍显示 b.txt")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if _, ok := tt.Find("过滤"); ok {
		t.Fatal("过滤框未关闭")
	}
	if _, ok := tt.Find("b.txt"); !ok {
		t.Fatal("列表未恢复")
	}
}

func TestGitChangesWriteFailureState(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			_, ps, _ := gitPaneTestState(t)
			ps.failure = &git.Failure{Title: "旧错误"}
			st := gitChangesStateFor(ps)
			done := make(chan struct{})
			gitChangesRun(ps, st, func(context.Context) error {
				defer close(done)
				if fail {
					return errors.New("写入失败")
				}
				return nil
			})
			<-done
			gitPaneWait(t, func() bool {
				ps.mu.Lock()
				defer ps.mu.Unlock()
				return !fail && ps.failure == nil || fail && ps.failure != nil && ps.failure.Title != "旧错误"
			})
		})
	}
}

func TestGitChangesFilteredGroupActions(t *testing.T) {
	for _, action := range []string{"全部暂存", "全部取消暂存", "全部丢弃"} {
		t.Run(action, func(t *testing.T) {
			s, ps, base := gitPaneTestState(t)
			client := &gitChangesFakeClient{gitPaneFakeClient: base, snapshot: &git.Snapshot{}}
			s.client = client
			ps.state = GitState{WorkDir: t.TempDir()}
			ps.entries = []git.StatusEntry{{Path: "show.txt", Worktree: 'M'}, {Path: "hide.txt", Worktree: 'M'}}
			if action == "全部取消暂存" {
				ps.entries = []git.StatusEntry{{Path: "show.txt", Index: 'M'}, {Path: "hide.txt", Index: 'M'}}
			}
			st := gitChangesStateFor(ps)
			st.root, st.filter = ps.state.WorkDir, "show"
			tt := ui.NewTester(func(c *ui.Context) { renderGitPaneChanges(c, s, "git-test", ps) }, 800, 600)
			tt.Frame()
			tt.Frame()
			if err := tt.Click(action); err != nil {
				t.Fatal(err)
			}
			if action == "全部丢弃" {
				tt.Frame()
				if _, ok := tt.Find("丢弃全部 1 个文件的更改？"); !ok {
					t.Fatal("确认数量错误")
				}
				if err := tt.Click("丢弃"); err != nil {
					t.Fatal(err)
				}
			}
			gitPaneWait(t, func() bool { base.paneMu.Lock(); defer base.paneMu.Unlock(); return len(base.calls) > 0 })
			base.paneMu.Lock()
			paths := append([]string(nil), client.paths...)
			base.paneMu.Unlock()
			if !reflect.DeepEqual(paths, []string{"show.txt"}) {
				t.Fatalf("操作范围 = %v", paths)
			}
		})
	}
}
