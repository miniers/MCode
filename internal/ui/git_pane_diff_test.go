package mcodeui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"mcode/internal/git"
)

type gitDiffFakeClient struct {
	*gitPaneFakeClient
	diffMu    sync.Mutex
	files     []git.FileDiff
	diffHook  func(context.Context, string, bool) ([]git.FileDiff, error)
	diffCalls []string
	sels      []git.LineSel
	content   []byte
	err       error
	writeErr  error
	snap      *git.Snapshot
	writeHook func(context.Context) error
}

func (f *gitDiffFakeClient) GitIndexDiff(ctx context.Context, _ string, path string, staged bool) ([]git.FileDiff, error) {
	f.diffMu.Lock()
	f.diffCalls = append(f.diffCalls, "diff:"+path)
	hook, files := f.diffHook, f.files
	f.diffMu.Unlock()
	if hook != nil {
		return hook(ctx, path, staged)
	}
	return files, nil
}
func (f *gitDiffFakeClient) lineCall(name string, sels []git.LineSel) error {
	f.diffMu.Lock()
	defer f.diffMu.Unlock()
	f.diffCalls = append(f.diffCalls, name)
	f.sels = append([]git.LineSel(nil), sels...)
	return f.err
}
func (f *gitDiffFakeClient) GitStageLines(_ context.Context, _, _ string, sels []git.LineSel) error {
	return f.lineCall("stage", sels)
}
func (f *gitDiffFakeClient) GitUnstageLines(_ context.Context, _, _ string, sels []git.LineSel) error {
	return f.lineCall("unstage", sels)
}
func (f *gitDiffFakeClient) GitDiscardLines(_ context.Context, _, _ string, sels []git.LineSel) (*git.Snapshot, error) {
	err := f.lineCall("discard", sels)
	return f.snap, err
}
func (f *gitDiffFakeClient) GitStageFiles(context.Context, string, []string) error {
	return f.lineCall("stage-file", nil)
}
func (f *gitDiffFakeClient) GitUnstageFiles(context.Context, string, []string) error {
	return f.lineCall("unstage-file", nil)
}
func (f *gitDiffFakeClient) GitResolveFileSide(_ context.Context, _, _ string, ours bool) error {
	if ours {
		return f.lineCall("ours", nil)
	}
	return f.lineCall("theirs", nil)
}
func (f *gitDiffFakeClient) GitMarkResolved(context.Context, string, []string) error {
	return f.lineCall("mark", nil)
}
func (f *gitDiffFakeClient) GitWriteResolved(ctx context.Context, _, _ string, content []byte) error {
	f.diffMu.Lock()
	f.diffCalls = append(f.diffCalls, "write")
	f.content = append([]byte(nil), content...)
	err, hook := f.writeErr, f.writeHook
	f.diffMu.Unlock()
	if hook != nil {
		return hook(ctx)
	}
	return err
}

func gitDiffFixture(t *testing.T) git.FileDiff {
	t.Helper()
	files, err := git.ParseUnifiedDiff([]byte("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -10,3 +10,4 @@\n context\n-old\n+new\n+plus\n tail\n"))
	if err != nil {
		t.Fatal(err)
	}
	return files[0]
}

func newGitDiffTester(t *testing.T, file git.FileDiff, conflictText string) (*chatState, *gitPaneState, *gitDiffFakeClient, *ui.Tester) {
	t.Helper()
	root := t.TempDir()
	base := &gitPaneFakeClient{FakeClient: NewFakeClient(), state: GitState{WorkDir: root}}
	f := &gitDiffFakeClient{gitPaneFakeClient: base, files: []git.FileDiff{file}, snap: &git.Snapshot{Root: root}}
	if conflictText != "" {
		if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(conflictText), 0o644); err != nil {
			t.Fatal(err)
		}
		base.entries = []git.StatusEntry{{Path: "a.go", Index: 'U', Worktree: 'U'}}
	}
	s := newChatState(f)
	s.mu.Lock()
	s.selectedThreadID = "diff-test"
	s.threads = []Thread{{ID: "diff-test"}}
	s.mu.Unlock()
	ps := getGitPaneState(s)
	ps.mu.Lock()
	ps.state = base.state
	ps.entries = base.entries
	ps.mu.Unlock()
	t.Cleanup(func() { s.close(); gitDiffStates.Delete(ps) })
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneDiff(c, s, "diff-test", ps, "a.go", false, nil) }, 900, 500)
	waitFor(t, tt, func() bool {
		st := gitDiffStateFor(ps)
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.result != nil && !st.loading
	})
	tt.Frame()
	return s, ps, f, tt
}

func TestGitDiffBackButton(t *testing.T) {
	s, ps, _, withoutBack := newGitDiffTester(t, gitDiffFixture(t), "")
	if _, ok := withoutBack.Find("返回"); ok {
		t.Fatal("unexpected back button without callback")
	}
	called := false
	withBack := ui.NewTester(func(c *ui.Context) {
		renderGitPaneDiff(c, s, "diff-test", ps, "a.go", false, func() { called = true })
	}, 900, 500)
	withBack.Frame()
	if err := withBack.Click("返回"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("back callback was not called")
	}
}

func TestGitConflictBackButton(t *testing.T) {
	s, ps, _, withoutBack := newGitDiffTester(t, git.FileDiff{}, "<<<<<<< ours\nA\n=======\nB\n>>>>>>> theirs\n")
	if _, ok := withoutBack.Find("返回"); ok {
		t.Fatal("unexpected back button without callback")
	}
	called := false
	withBack := ui.NewTester(func(c *ui.Context) {
		renderGitPaneDiff(c, s, "diff-test", ps, "a.go", false, func() { called = true })
	}, 500, 400)
	withBack.Frame()
	if err := withBack.Click("返回"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("back callback was not called")
	}
}

func TestGitDiffHunkLineSelections(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []git.Line
		want  []git.LineSel
	}{
		{"新增", []git.Line{{Kind: '+', NewNo: 3}, {Kind: '+', NewNo: 4}}, []git.LineSel{{Start: 3, End: 4}}},
		{"删除", []git.Line{{Kind: '-', OldNo: 7}, {Kind: '-', OldNo: 8}}, []git.LineSel{{Old: true, Start: 7, End: 8}}},
		{"混合", []git.Line{{Kind: ' ', OldNo: 1, NewNo: 1}, {Kind: '-', OldNo: 2}, {Kind: '+', NewNo: 2}, {Kind: '\\'}}, []git.LineSel{{Old: true, Start: 2, End: 2}, {Start: 2, End: 2}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hunkLineSelections(git.Hunk{Lines: tc.lines}); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%+v != %+v", got, tc.want)
			}
		})
	}
}

func TestGitDiffRangeSelections(t *testing.T) {
	f := gitDiffFixture(t)
	f.Hunks = append(f.Hunks, git.Hunk{OldStart: 100, OldLines: 1, NewStart: 101, NewLines: 2, Lines: []git.Line{{Kind: ' ', OldNo: 100, NewNo: 101}, {Kind: '+', NewNo: 102}}})
	for _, tc := range []struct {
		name string
		r    editor.DiffLineRange
		want []git.LineSel
	}{
		{"新增", editor.DiffLineRange{Side: editor.DiffNew, Start: 1, End: 2}, []git.LineSel{{Start: 11, End: 12}}},
		{"删除", editor.DiffLineRange{Side: editor.DiffOld, Start: 0, End: 2}, []git.LineSel{{Old: true, Start: 11, End: 11}}},
		{"跨补丁", editor.DiffLineRange{Side: editor.DiffNew, Start: 2, End: 5}, []git.LineSel{{Start: 12, End: 12}, {Start: 102, End: 102}}},
		{"上下文", editor.DiffLineRange{Side: editor.DiffNew, Start: 0, End: 0}, nil},
		{"无效", editor.DiffLineRange{Side: editor.DiffNew, Start: -1, End: 1}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := diffRangeSelections(f, tc.r); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%+v != %+v", got, tc.want)
			}
		})
	}
}

func hoverGitDiffHunk(t *testing.T, tt *ui.Tester, ps *gitPaneState) {
	t.Helper()
	d := gitDiffStateFor(ps).view
	d.GoToHunk(0)
	tt.Frame()
	r, ok := tt.Find("文件差异")
	if !ok {
		t.Fatal("缺少差异视图")
	}
	tt.Move(r.X+200, r.Y+float32(d.LineTop(editor.DiffNew, 1))+5)
	tt.Frame()
}

func TestGitDiffHunkStageAndDiscard(t *testing.T) {
	for _, discard := range []bool{false, true} {
		t.Run(map[bool]string{false: "暂存", true: "丢弃"}[discard], func(t *testing.T) {
			_, ps, f, tt := newGitDiffTester(t, gitDiffFixture(t), "")
			hoverGitDiffHunk(t, tt, ps)
			label := "暂存此块"
			want := "stage"
			if discard {
				label = "丢弃此块"
				want = "discard"
			}
			if err := tt.Click(label); err != nil {
				t.Fatal(err)
			}
			waitFor(t, tt, func() bool {
				f.diffMu.Lock()
				defer f.diffMu.Unlock()
				for _, c := range f.diffCalls {
					if c == want {
						return true
					}
				}
				return false
			})
			f.diffMu.Lock()
			sels := append([]git.LineSel(nil), f.sels...)
			f.diffMu.Unlock()
			expected := []git.LineSel{{Old: true, Start: 11, End: 11}, {Start: 11, End: 12}}
			if !reflect.DeepEqual(sels, expected) {
				t.Fatalf("块选择 = %+v", sels)
			}
			if discard {
				waitFor(t, tt, func() bool {
					ps.mu.Lock()
					defer ps.mu.Unlock()
					return ps.undoSnap == f.snap && ps.undoLabel == "已丢弃 1 块"
				})
			}
		})
	}
}

func TestGitDiffSpaceAndEscape(t *testing.T) {
	_, ps, f, tt := newGitDiffTester(t, gitDiffFixture(t), "")
	d := gitDiffStateFor(ps).view
	d.SetSelectedLines(editor.DiffLineRange{Side: editor.DiffNew, Start: 1, End: 2}, true)
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if _, ok := d.SelectedLines(); ok {
		t.Fatal("Esc 没有清空选择")
	}
	d.SetSelectedLines(editor.DiffLineRange{Side: editor.DiffOld, Start: 1, End: 1}, true)
	tt.Frame()
	tt.Key(0, ui.KeySpace)
	tt.Frame()
	waitFor(t, tt, func() bool {
		f.diffMu.Lock()
		defer f.diffMu.Unlock()
		for _, call := range f.diffCalls {
			if call == "stage" {
				return true
			}
		}
		return false
	})
}

func TestGitConflictSelectionsAndSave(t *testing.T) {
	text := "before\n<<<<<<< ours\nA\n=======\nB\n>>>>>>> theirs\nmiddle\n<<<<<<< ours\nC\n=======\nD\n>>>>>>> theirs\nafter\n"
	_, ps, f, tt := newGitDiffTester(t, git.FileDiff{}, text)
	check := func(disabled bool) {
		t.Helper()
		node, ok := tt.Access("保存并标记已解决")
		if !ok || node.Disabled != disabled {
			t.Fatalf("保存禁用状态 = %+v", node)
		}
	}
	check(true)
	if err := tt.Click("第 1 处 保留 当前"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	check(true)
	if err := tt.Click("第 2 处 都保留"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	check(false)
	if err := tt.Click("保存并标记已解决"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { f.diffMu.Lock(); defer f.diffMu.Unlock(); return len(f.diffCalls) >= 2 })
	f.diffMu.Lock()
	calls, content := append([]string(nil), f.diffCalls...), string(f.content)
	f.diffMu.Unlock()
	if !reflect.DeepEqual(calls, []string{"write", "mark"}) || content != "before\nA\nmiddle\nC\nD\nafter\n" {
		t.Fatalf("调用=%v,内容=%q", calls, content)
	}
	_ = ps
}

func TestGitConflictWriteFailureDoesNotMark(t *testing.T) {
	_, ps, f, tt := newGitDiffTester(t, git.FileDiff{}, "<<<<<<< x\na\n=======\nb\n>>>>>>> y\n")
	f.diffMu.Lock()
	f.writeErr = errors.New("无法写盘")
	f.diffMu.Unlock()
	if err := tt.Click("第 1 处 保留 当前"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("保存并标记已解决"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return ps.failure != nil })
	f.diffMu.Lock()
	defer f.diffMu.Unlock()
	if !reflect.DeepEqual(f.diffCalls, []string{"write"}) {
		t.Fatalf("写入失败后仍标记: %v", f.diffCalls)
	}
}

func TestGitDiffStaleLoadAndRefresh(t *testing.T) {
	s, ps, f, tt := newGitDiffTester(t, gitDiffFixture(t), "")
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	f.diffMu.Lock()
	f.diffHook = func(ctx context.Context, path string, _ bool) ([]git.FileDiff, error) {
		if path == "old.go" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return []git.FileDiff{{NewPath: "old.go"}}, nil
		}
		return []git.FileDiff{{NewPath: path}}, nil
	}
	f.diffMu.Unlock()
	st := gitDiffStateFor(ps)
	old := gitDiffKey{threadID: "diff-test", root: ps.state.WorkDir, path: "old.go"}
	loadGitDiff(s, ps, st, f, old, 0, false)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("未启动加载")
	}
	key := old
	key.path = "new.go"
	loadGitDiff(s, ps, st, f, key, 0, false)
	unblock()
	gitPaneWait(t, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.loading && st.result != nil && st.result.file.NewPath == "new.go"
	})
	st.mu.Lock()
	st.dirty = true
	st.mu.Unlock()
	loadGitDiff(s, ps, st, f, key, 0, false)
	gitPaneWait(t, func() bool { st.mu.Lock(); defer st.mu.Unlock(); return !st.loading })
	_ = tt
}

func TestGitDiffHunkIncludesSeparatedRuns(t *testing.T) {
	f := gitDiffFixture(t)
	f.Hunks[0].Lines = append(f.Hunks[0].Lines, git.Line{Kind: '-', OldNo: 13}, git.Line{Kind: '+', NewNo: 14})
	got := diffHunkSelections(f, editor.DiffHunk{OldStart: 1, OldEnd: 2, NewStart: 1, NewEnd: 3})
	if want := hunkLineSelections(f.Hunks[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("没有选择原始完整块: %+v != %+v", got, want)
	}
}

func TestGitDiffGutterShiftSelection(t *testing.T) {
	_, ps, _, tt := newGitDiffTester(t, gitDiffFixture(t), "")
	d := gitDiffStateFor(ps).view
	r, ok := tt.Find("文件差异")
	if !ok {
		t.Fatal("缺少差异视图")
	}
	tt.ClickAt(r.X+Space5, r.Y+float32(d.LineTop(editor.DiffNew, 1))+5)
	tt.ClickAtWith(ui.Shift, r.X+Space5, r.Y+float32(d.LineTop(editor.DiffNew, 2))+5)
	if got, ok := d.SelectedLines(); !ok || got != (editor.DiffLineRange{Side: editor.DiffNew, Start: 1, End: 2}) {
		t.Fatalf("Shift 选择 = %+v,%v", got, ok)
	}
}

func TestGitDiffEmptySelectionIsQuiet(t *testing.T) {
	s, ps, f, tt := newGitDiffTester(t, gitDiffFixture(t), "")
	f.diffMu.Lock()
	f.err = git.ErrEmptySelection
	f.diffMu.Unlock()
	st := gitDiffStateFor(ps)
	applyGitSelections(s, ps, st, f, st.key, nil, false, false)
	waitFor(t, tt, func() bool { st.mu.Lock(); defer st.mu.Unlock(); return !st.pending })
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.failure != nil {
		t.Fatalf("空选择显示错误: %+v", ps.failure)
	}
}

func TestGitConflictRejectsChangedContentAndDuplicateSave(t *testing.T) {
	text := "<<<<<<< x\na\n=======\nb\n>>>>>>> y\n"
	s, ps, f, tt := newGitDiffTester(t, git.FileDiff{}, text)
	st := gitDiffStateFor(ps)
	cs := st.result.conflict
	cs.choices[0] = git.ConflictBoth
	if err := os.WriteFile(filepath.Join(st.key.root, st.key.path), []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}
	saveGitConflict(s, ps, st, f, st.key, cs)
	waitFor(t, tt, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return ps.failure != nil })
	f.diffMu.Lock()
	calls := append([]string(nil), f.diffCalls...)
	f.diffMu.Unlock()
	if len(calls) > 0 {
		t.Fatalf("覆盖了外部修改: %v", calls)
	}
	if err := os.WriteFile(filepath.Join(st.key.root, st.key.path), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	gitPaneWait(t, func() bool { st.mu.Lock(); defer st.mu.Unlock(); return !st.loading && !st.pending })
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	f.diffMu.Lock()
	f.writeHook = func(ctx context.Context) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.diffMu.Unlock()
	saveGitConflict(s, ps, st, f, st.key, cs)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("未开始写入")
	}
	saveGitConflict(s, ps, st, f, st.key, cs)
	f.diffMu.Lock()
	if len(f.diffCalls) != 1 {
		t.Fatalf("重复保存: %v", f.diffCalls)
	}
	f.diffMu.Unlock()
	once.Do(func() { close(release) })
	gitPaneWait(t, func() bool { st.mu.Lock(); defer st.mu.Unlock(); return !st.pending })
}

func TestGitDiffSpecialFilesAndConflictFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		file git.FileDiff
		text string
	}{{"二进制文件", git.FileDiff{Binary: true}, ""}, {"文件过大", git.FileDiff{IsDeferred: true, DeferredPatch: strings.Repeat("x", 3<<20)}, ""}, {"二进制文件", git.FileDiff{}, "a\x00b"}} {
		t.Run(tc.name+tc.text, func(t *testing.T) {
			_, _, _, tt := newGitDiffTester(t, tc.file, tc.text)
			if !tt.HasText(tc.name) {
				t.Fatalf("缺少 %s", tc.name)
			}
		})
	}
	if !gitDiffTooLarge(git.FileDiff{Hunks: []git.Hunk{{Lines: []git.Line{{Kind: '+', Text: strings.Repeat("x", gitDiffMaxBytes)}}}}}) {
		t.Fatal("未限制补丁大小")
	}
}

func TestGitDiffDeferredSizeAndHunkMovement(t *testing.T) {
	if gitDiffTooLarge(git.FileDiff{IsDeferred: true, DeferredPatch: strings.Repeat("x", 1<<20)}) || !gitDiffTooLarge(git.FileDiff{IsDeferred: true, DeferredPatch: strings.Repeat("x", 3<<20)}) {
		t.Fatal("延迟补丁大小判断错误")
	}
	for _, tc := range []struct{ current, delta, count, want int }{{0, 1, 3, 1}, {0, -1, 3, 0}, {2, 1, 3, 2}, {4, 0, 3, 2}, {0, 1, 0, 0}} {
		if got := moveGitHunk(tc.current, tc.delta, tc.count); got != tc.want {
			t.Fatalf("moveGitHunk(%d, %d, %d) = %d", tc.current, tc.delta, tc.count, got)
		}
	}
}

func TestGitDiffRefreshReloadsSameStatus(t *testing.T) {
	_, ps, f, tt := newGitDiffTester(t, gitDiffFixture(t), "")
	f.diffMu.Lock()
	before := len(f.diffCalls)
	f.diffMu.Unlock()
	ps.requestRefresh()
	waitFor(t, tt, func() bool { f.diffMu.Lock(); defer f.diffMu.Unlock(); return len(f.diffCalls) > before })
	st := gitDiffStateFor(ps)
	oldView := st.view
	waitFor(t, tt, func() bool { st.mu.Lock(); defer st.mu.Unlock(); return !st.loading })
	tt.Frame()
	if st.view != oldView {
		t.Fatal("相同内容刷新丢失滚动与选择状态")
	}
}

func TestGitDiffStagedAndNarrowLayout(t *testing.T) {
	s, ps, f, _ := newGitDiffTester(t, gitDiffFixture(t), "")
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneDiff(c, s, "diff-test", ps, "a.go", true, nil) }, 500, 400)
	waitFor(t, tt, func() bool {
		st := gitDiffStateFor(ps)
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.key.staged && !st.loading
	})
	tt.Frame()
	node, ok := tt.Access("并排")
	if !ok || !node.Disabled {
		t.Fatalf("窄视图并排未禁用: %+v", node)
	}
	hoverGitDiffHunk(t, tt, ps)
	if err := tt.Click("取消暂存此块"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		f.diffMu.Lock()
		defer f.diffMu.Unlock()
		for _, call := range f.diffCalls {
			if call == "unstage" {
				return true
			}
		}
		return false
	})
}

func TestGitDiffLoadingDelay(t *testing.T) {
	s, ps, f, _ := newGitDiffTester(t, gitDiffFixture(t), "")
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	f.diffMu.Lock()
	f.diffHook = func(ctx context.Context, _ string, _ bool) ([]git.FileDiff, error) {
		close(started)
		select {
		case <-release:
			return []git.FileDiff{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.diffMu.Unlock()
	st := gitDiffStateFor(ps)
	key := st.key
	key.path = "slow.go"
	loadGitDiff(s, ps, st, f, key, 0, false)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("未启动加载")
	}
	st.mu.Lock()
	shown := st.showLoading
	st.mu.Unlock()
	if shown {
		t.Fatal("150ms 前显示了加载态")
	}
	gitPaneWait(t, func() bool { st.mu.Lock(); defer st.mu.Unlock(); return st.showLoading })
	unblock()
	gitPaneWait(t, func() bool { st.mu.Lock(); defer st.mu.Unlock(); return !st.loading && !st.showLoading })
}

func TestGitConflictAISendsMessage(t *testing.T) {
	text := "before\n<<<<<<< ours\n**A**\n=======\nB\n>>>>>>> theirs\nafter\n"
	s, ps, f, tt := newGitDiffTester(t, git.FileDiff{}, text)
	ps.mu.Lock()
	ps.op.Conflicts = []git.ConflictFile{{Path: "a.go"}, {Path: "b.go"}}
	ps.mu.Unlock()
	if got := gitConflictPaths(ps); !reflect.DeepEqual(got, []string{"a.go", "b.go"}) {
		t.Fatalf("冲突路径: %v", got)
	}
	if err := tt.Click("差异菜单"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(DurBase + DurFast)
	if err := tt.Click("交给 AI 解决"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return len(sendCallsOf(f.FakeClient)) == 1 })
	calls := sendCallsOf(f.FakeClient)
	if calls[0].ThreadID != "diff-test" {
		t.Fatalf("发送会话: %q", calls[0].ThreadID)
	}
	for _, raw := range []string{"a.go", "<<<<<<< ours", "=======", ">>>>>>> theirs", "**A**"} {
		if !strings.Contains(calls[0].Text, raw) {
			t.Fatalf("消息缺少原文 %q: %q", raw, calls[0].Text)
		}
	}
	s.mu.Lock()
	draft := s.draft
	s.mu.Unlock()
	if draft != "" {
		t.Fatalf("草稿不应改变: %q", draft)
	}
}

func TestGitDiffSelectionEscapePrecedesWindowShortcut(t *testing.T) {
	s, ps, _, _ := newGitDiffTester(t, gitDiffFixture(t), "")
	escaped := false
	tt := ui.NewTester(func(c *ui.Context) {
		if c.Shortcut(0, ui.KeyEscape) {
			escaped = true
		}
		renderGitPaneDiff(c, s, "diff-test", ps, "a.go", false, nil)
	}, 900, 500)
	tt.Frame()
	d := gitDiffStateFor(ps).view
	r, ok := tt.Find("文件差异")
	if !ok {
		t.Fatal("缺少差异视图")
	}
	tt.ClickAt(r.X+Space5, r.Y+float32(d.LineTop(editor.DiffNew, 1))+5)
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if _, has := d.SelectedLines(); has || escaped {
		t.Fatalf("Esc 未由选择处理: selected=%v, window=%v", has, escaped)
	}
}

func TestGitDiffReloadAfterInFlightPaneRefresh(t *testing.T) {
	s, ps, f, _ := newGitDiffTester(t, gitDiffFixture(t), "")
	st := gitDiffStateFor(ps)
	key := st.key
	key.path = "during-refresh.go"
	loadGitDiff(s, ps, st, f, key, 1, true)
	gitPaneWait(t, func() bool { st.mu.Lock(); defer st.mu.Unlock(); return !st.loading })
	f.diffMu.Lock()
	f.files = []git.FileDiff{{NewPath: "latest.go"}}
	f.diffMu.Unlock()
	loadGitDiff(s, ps, st, f, key, 1, false)
	gitPaneWait(t, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.loading && st.result.file.NewPath == "latest.go"
	})
}
