package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"mcode/internal/git"
)

func TestGitDiffHunkKeys(t *testing.T) {
	patch := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old1\n+new1\n@@ -10 +10 @@\n-old2\n+new2\n"
	files, err := git.ParseUnifiedDiff([]byte(patch))
	if err != nil {
		t.Fatal(err)
	}
	_, ps, f, tt := newGitDiffTester(t, files[0], "")
	if err := tt.Click("文件差异"); err != nil {
		t.Fatal(err)
	}
	st := gitDiffStateFor(ps)
	tt.Key(0, ui.KeyK)
	tt.Frame()
	if st.currentHunk != 0 {
		t.Fatalf("上界 = %d", st.currentHunk)
	}
	tt.Key(0, ui.KeyJ)
	tt.Frame()
	tt.Key(0, ui.KeyJ)
	tt.Frame()
	if st.currentHunk != 1 {
		t.Fatalf("下界 = %d", st.currentHunk)
	}
	tt.Key(0, ui.KeyS)
	tt.Frame()
	waitFor(t, tt, func() bool { f.diffMu.Lock(); defer f.diffMu.Unlock(); return len(f.sels) > 0 })
	f.diffMu.Lock()
	sels := append([]git.LineSel(nil), f.sels...)
	f.diffMu.Unlock()
	want := []git.LineSel{{Old: true, Start: 10, End: 10}, {Start: 10, End: 10}}
	if !reflect.DeepEqual(sels, want) {
		t.Fatalf("暂存行 = %v，期望 %v", sels, want)
	}
}

func TestGitDiffSelectionToConversation(t *testing.T) {
	s, _, f, _ := newGitDiffTester(t, gitDiffFixture(t), "")
	key := gitDiffKey{threadID: "diff-test", path: "a.go"}
	sel := editor.DiffLineRange{Side: editor.DiffNew, Start: 1, End: 2}
	if !addGitDiffSelectionToConversation(s, key, gitDiffFixture(t), sel) {
		t.Fatal("添加失败")
	}
	gitPaneWait(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.uploadCalls) > 0 })
	f.mu.Lock()
	call := f.uploadCalls[0]
	f.mu.Unlock()
	if call.Name != "a.go:11-12.txt" || !strings.Contains(string(call.Data), "new\nplus") {
		t.Fatalf("附件 = %+v", call)
	}
	key.threadID = "other"
	if addGitDiffSelectionToConversation(s, key, gitDiffFixture(t), sel) {
		t.Fatal("非当前会话添加成功")
	}
}

func TestGitDiffDeferredSmallPatch(t *testing.T) {
	patch := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"
	_, ps, _, tt := newGitDiffTester(t, git.FileDiff{NewPath: "a.go", IsDeferred: true, DeferredPatch: patch}, "")
	st := gitDiffStateFor(ps)
	st.mu.Lock()
	file := st.result.file
	st.mu.Unlock()
	if file.IsDeferred || len(file.Hunks) != 1 {
		t.Fatalf("未展开: %+v", file)
	}
	if _, ok := tt.Find("文件差异"); !ok {
		t.Fatal("未显示差异")
	}
	if _, ok := tt.Find("文件过大"); ok {
		t.Fatal("误报文件过大")
	}
}

func TestGitDiffKeysDoNotInterruptTextInput(t *testing.T) {
	patch := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old1\n+new1\n@@ -10 +10 @@\n-old2\n+new2\n"
	files, err := git.ParseUnifiedDiff([]byte(patch))
	if err != nil {
		t.Fatal(err)
	}
	s, ps, f, _ := newGitDiffTester(t, files[0], "")
	var value string
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() {
			TextField(c, TextFieldProps{Key: "input", Label: "输入", Value: &value})
			renderGitPaneDiff(c, s, "diff-test", ps, "a.go", false, nil)
		})
	}, 900, 500)
	tt.Frame()
	if err := tt.Click("输入"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []ui.Key{ui.KeyK, ui.KeyS, ui.KeyJ} {
		tt.Key(0, key)
		tt.Frame()
	}
	f.diffMu.Lock()
	calls := append([]string(nil), f.diffCalls...)
	f.diffMu.Unlock()
	for _, call := range calls {
		if call == "stage" {
			t.Fatal("输入时触发暂存")
		}
	}
	if gitDiffStateFor(ps).currentHunk != 0 {
		t.Fatal("输入时切换差异块")
	}
}

func TestGitDiffWriteFailureState(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			s, ps, _, _ := newGitDiffTester(t, gitDiffFixture(t), "")
			ps.mu.Lock()
			ps.failure = &git.Failure{Title: "旧错误"}
			ps.mu.Unlock()
			st := gitDiffStateFor(ps)
			runGitDiffWrite(s, ps, st, st.key, false, func(context.Context) (*git.Snapshot, error) {
				if fail {
					return nil, errors.New("写入失败")
				}
				return nil, nil
			}, "")
			gitPaneWait(t, func() bool {
				ps.mu.Lock()
				defer ps.mu.Unlock()
				return !fail && ps.failure == nil || fail && ps.failure != nil && ps.failure.Title != "旧错误"
			})
		})
	}
}
