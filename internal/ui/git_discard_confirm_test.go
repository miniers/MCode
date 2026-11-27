package mcodeui

import (
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"mcode/internal/git"
)

func diffCallCount(f *gitDiffFakeClient, name string) int {
	f.diffMu.Lock()
	defer f.diffMu.Unlock()
	n := 0
	for _, call := range f.diffCalls {
		if call == name {
			n++
		}
	}
	return n
}

func paneCallCount(f *gitPaneFakeClient, name string) int {
	f.paneMu.Lock()
	defer f.paneMu.Unlock()
	n := 0
	for _, call := range f.calls {
		if call == name {
			n++
		}
	}
	return n
}

func TestGitDiscardHunkConfirm(t *testing.T) {
	t.Run("confirm", func(t *testing.T) {
		_, ps, f, tt := newGitDiffTester(t, gitDiffFixture(t), "")
		hoverGitDiffHunk(t, tt, ps)
		clickWhenSettled(t, tt, "丢弃此块")
		waitFor(t, tt, func() bool {
			return tt.HasText("丢弃此块改动？") && tt.HasText("确认丢弃")
		})
		if n := diffCallCount(f, "discard"); n != 0 {
			t.Fatalf("确认前不应调用 GitDiscardLines，实际调用 %d 次", n)
		}
		clickWhenSettled(t, tt, "确认丢弃")
		waitFor(t, tt, func() bool { return diffCallCount(f, "discard") == 1 })
		f.diffMu.Lock()
		sels := append([]git.LineSel(nil), f.sels...)
		f.diffMu.Unlock()
		want := []git.LineSel{{Old: true, Start: 11, End: 11}, {Start: 11, End: 12}}
		if !reflect.DeepEqual(sels, want) {
			t.Fatalf("丢弃块范围 = %+v，期望 %+v", sels, want)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		_, ps, f, tt := newGitDiffTester(t, gitDiffFixture(t), "")
		hoverGitDiffHunk(t, tt, ps)
		clickWhenSettled(t, tt, "丢弃此块")
		waitFor(t, tt, func() bool { return tt.HasText("丢弃此块改动？") })
		if n := diffCallCount(f, "discard"); n != 0 {
			t.Fatalf("确认前不应调用 GitDiscardLines，实际调用 %d 次", n)
		}
		clickWhenSettled(t, tt, "取消")
		tt.Frame()
		if tt.HasText("丢弃此块改动？") {
			t.Fatal("取消后确认框未关闭")
		}
		if n := diffCallCount(f, "discard"); n != 0 {
			t.Fatalf("取消后不应调用 GitDiscardLines，实际调用 %d 次", n)
		}
	})
}

func TestGitDiscardLinesConfirm(t *testing.T) {
	t.Run("confirm", func(t *testing.T) {
		_, ps, f, tt := newGitDiffTester(t, gitDiffFixture(t), "")
		d := gitDiffStateFor(ps).view
		d.SetSelectedLines(editor.DiffLineRange{Side: editor.DiffNew, Start: 1, End: 2}, true)
		tt.Frame()
		clickWhenSettled(t, tt, "丢弃")
		waitFor(t, tt, func() bool {
			return tt.HasText("丢弃选中行改动？") && tt.HasText("确认丢弃")
		})
		if n := diffCallCount(f, "discard"); n != 0 {
			t.Fatalf("确认前不应调用 GitDiscardLines，实际调用 %d 次", n)
		}
		clickWhenSettled(t, tt, "确认丢弃")
		waitFor(t, tt, func() bool { return diffCallCount(f, "discard") == 1 })
		f.diffMu.Lock()
		sels := append([]git.LineSel(nil), f.sels...)
		f.diffMu.Unlock()
		want := []git.LineSel{{Start: 11, End: 12}}
		if !reflect.DeepEqual(sels, want) {
			t.Fatalf("丢弃行范围 = %+v，期望 %+v", sels, want)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		_, ps, f, tt := newGitDiffTester(t, gitDiffFixture(t), "")
		d := gitDiffStateFor(ps).view
		d.SetSelectedLines(editor.DiffLineRange{Side: editor.DiffNew, Start: 1, End: 2}, true)
		tt.Frame()
		clickWhenSettled(t, tt, "丢弃")
		waitFor(t, tt, func() bool { return tt.HasText("丢弃选中行改动？") })
		if n := diffCallCount(f, "discard"); n != 0 {
			t.Fatalf("确认前不应调用 GitDiscardLines，实际调用 %d 次", n)
		}
		clickWhenSettled(t, tt, "取消")
		tt.Frame()
		if tt.HasText("丢弃选中行改动？") {
			t.Fatal("取消后确认框未关闭")
		}
		if n := diffCallCount(f, "discard"); n != 0 {
			t.Fatalf("取消后不应调用 GitDiscardLines，实际调用 %d 次", n)
		}
	})
}

func hoverReviewCardRow(t *testing.T, tt *ui.Tester, path string) {
	t.Helper()
	waitFor(t, tt, func() bool {
		_, ok := tt.Find(path)
		return ok
	})
	rowRect, ok := tt.Find(path)
	if !ok {
		t.Fatalf("找不到审阅文件行 %q", path)
	}
	tt.Move(rowRect.X+rowRect.W/2, rowRect.Y+rowRect.H/2)
	tt.Frame()
}

func TestReviewCardRevertFileConfirm(t *testing.T) {
	t.Run("confirm", func(t *testing.T) {
		_, client, _, tt := reviewUncommittedTester(t)
		hoverReviewCardRow(t, tt, "a.go")
		clickWhenSettled(t, tt, "撤销文件改动")
		waitFor(t, tt, func() bool {
			return tt.HasText("撤销 a.go 的改动？") && tt.HasText("确认撤销")
		})
		if n := paneCallCount(client.gitPaneFakeClient, "discard"); n != 0 {
			t.Fatalf("确认前不应调用 GitDiscardFiles，实际调用 %d 次", n)
		}
		clickWhenSettled(t, tt, "确认撤销")
		waitFor(t, tt, func() bool {
			return paneCallCount(client.gitPaneFakeClient, "discard") == 1 && !tt.HasText("撤销 a.go 的改动？")
		})
		client.paneMu.Lock()
		paths := append([]string(nil), client.paths...)
		client.paneMu.Unlock()
		if !reflect.DeepEqual(paths, []string{"a.go"}) {
			t.Fatalf("撤销路径 = %v，期望 [a.go]", paths)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		_, client, _, tt := reviewUncommittedTester(t)
		hoverReviewCardRow(t, tt, "a.go")
		clickWhenSettled(t, tt, "撤销文件改动")
		waitFor(t, tt, func() bool { return tt.HasText("撤销 a.go 的改动？") })
		if n := paneCallCount(client.gitPaneFakeClient, "discard"); n != 0 {
			t.Fatalf("确认前不应调用 GitDiscardFiles，实际调用 %d 次", n)
		}
		clickWhenSettled(t, tt, "取消")
		tt.Frame()
		if tt.HasText("撤销 a.go 的改动？") {
			t.Fatal("取消后确认框未关闭")
		}
		if n := paneCallCount(client.gitPaneFakeClient, "discard"); n != 0 {
			t.Fatalf("取消后不应调用 GitDiscardFiles，实际调用 %d 次", n)
		}
	})
}
