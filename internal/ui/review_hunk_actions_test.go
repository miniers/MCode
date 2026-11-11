package mcodeui

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
	"weak"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

func newReviewHunkTester(t *testing.T, client Client, snapshot ReviewSnapshot, file ReviewFile, content ReviewFileContent) (*chatState, *ui.Tester, *reviewDiffView) {
	t.Helper()
	s := newChatState(client)
	t.Cleanup(s.close)
	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.mu.Unlock()
	dp := getDiffPanelState(s)
	fm := getFileManagerState(s)
	fileManagerMu.Lock()
	fm.threadID = "t1"
	ensureFileTreeLocked(fm)
	fileManagerMu.Unlock()
	view := newReviewDiffView(content, "go")
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() {
			configureReviewHunkActions(c, s, dp, "t1", snapshot, file, content, view)
			renderReviewDiffView(c, "hunk-test", view, false, 800).Label("review-hunk-viewer")
		})
	}, 800, 400)
	tt.Frame()
	tt.Advance(DurSheet + 50*time.Millisecond)
	// 点击新侧变更行，让光标保持该块为活动块，弹窗接管指针后也不会丢失。
	y := float32(view.state.LineTop(editor.DiffNew, 1)) + 5
	tt.ClickAt(200, y)
	tt.Frame()
	return s, tt, view
}

func hoverReviewHunk(t *testing.T, tt *ui.Tester, view *reviewDiffView) {
	t.Helper()
	tt.Advance(DurSheet + 50*time.Millisecond)
	r, ok := tt.Find("review-hunk-viewer")
	if !ok {
		t.Fatal("缺少 diff 视口")
	}
	tt.Move(r.X+200, r.Y+float32(view.state.LineTop(editor.DiffNew, 1))+5)
	tt.Frame()
}

func reviewHunkTreeGeneration(s *chatState) int {
	fm := getFileManagerState(s)
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	return fm.tree.dirGen[""]
}

func confirmReviewHunkRevert(t *testing.T, tt *ui.Tester, fake *FakeClient) {
	t.Helper()
	clickText(t, tt, "撤销此块")
	if !tt.HasText("撤销此块？") || len(fake.ReviewRevertHunkCalls()) != 0 {
		t.Fatal("点击块按钮必须先确认，不能提前调用服务端")
	}
	tt.Advance(DurSheet + 50*time.Millisecond)
	clickText(t, tt, "撤销")
}

func TestReviewHunkRevertWaitsForServer(t *testing.T) {
	fake := NewFakeClient()
	list, full := reviewFixture()
	fake.SetReviewSnapshot("t1", "turn", "turn-1", "", list)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	fake.SetReviewRevertHunkHook(func(ctx context.Context, _ ReviewRevertHunkParams) (ReviewRevertHunkResult, error) {
		close(started)
		select {
		case <-release:
			return ReviewRevertHunkResult{FileHash: "saved"}, nil
		case <-ctx.Done():
			return ReviewRevertHunkResult{}, ctx.Err()
		}
	})
	s, tt, view := newReviewHunkTester(t, fake, list, list.Files[0], *full.File)
	beforeTree := reviewHunkTreeGeneration(s)
	beforeCalls := len(fake.ReviewGetCalls())
	oldBuffer, newBuffer := view.state.Old, view.state.New
	confirmReviewHunkRevert(t, tt, fake)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("确认后未请求服务端")
	}
	tt.Frame()
	hoverReviewHunk(t, tt, view)
	state := reviewHunkState(view, 0)
	state.mu.Lock()
	pending := state.pending
	state.mu.Unlock()
	if !pending {
		t.Fatal("请求中未进入禁用状态")
	}
	button, ok := tt.Find("撤销此块")
	if !ok {
		t.Fatal("请求中缺少块按钮")
	}
	tt.ClickAt(button.X+button.W/2, button.Y+button.H/2)
	tt.Frame()
	if tt.HasText("撤销此块？") {
		t.Fatal("请求中仍能点击块按钮")
	}
	if view.state.Old != oldBuffer || view.state.New != newBuffer || view.state.New.String() != full.File.NewText || len(view.state.Hunks()) != 1 || reviewHunkTreeGeneration(s) != beforeTree || len(fake.ReviewGetCalls()) != beforeCalls {
		t.Fatal("服务端成功前提前刷新了 diff 或文件树")
	}
	calls := fake.ReviewRevertHunkCalls()
	hash, err := reviewHunkHash(full.File.NewText, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := ReviewRevertHunkParams{ThreadID: "t1", Scope: list.Scope, TurnID: list.TurnID, Path: "a.go", BaseRevision: list.BaseRevision, HeadRevision: list.HeadRevision, StartLine: 2, LineCount: 1, HunkHash: hash, ExpectedFileHash: reviewContentHash(full.File.NewText)}
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("请求丢失审阅身份: %+v", calls)
	}
	// 禁用期间的重复操作不能再发请求。
	tt.Key(0, ui.KeyEnter)
	if len(fake.ReviewRevertHunkCalls()) != 1 {
		t.Fatal("请求中重复调用了后端")
	}
	unblock()
	waitFor(t, tt, func() bool {
		return reviewHunkTreeGeneration(s) > beforeTree && len(fake.ReviewGetCalls()) > beforeCalls
	})
	if call := fake.ReviewGetCalls()[beforeCalls]; call.ThreadID != "t1" || call.TurnID != list.TurnID {
		t.Fatalf("成功后没有重载原会话审阅: %+v", call)
	}
}

func TestReviewHunkRevertFailureKeepsDiff(t *testing.T) {
	fake := NewFakeClient()
	list, full := reviewFixture()
	fake.SetReviewRevertHunkResult(ReviewRevertHunkResult{}, errors.New("工作区文件已改变"))
	s, tt, view := newReviewHunkTester(t, fake, list, list.Files[0], *full.File)
	beforeTree := reviewHunkTreeGeneration(s)
	oldBuffer, newBuffer := view.state.Old, view.state.New
	confirmReviewHunkRevert(t, tt, fake)
	waitFor(t, tt, func() bool { return tt.HasText("工作区文件已改变") })
	if view.state.Old != oldBuffer || view.state.New != newBuffer || view.state.New.String() != full.File.NewText || len(view.state.Hunks()) != 1 || reviewHunkTreeGeneration(s) != beforeTree || len(fake.ReviewGetCalls()) != 0 {
		t.Fatal("失败时修改了 diff 或刷新了文件树")
	}
	hoverReviewHunk(t, tt, view)
	clickText(t, tt, "撤销此块")
	if !tt.HasText("撤销此块？") || len(fake.ReviewRevertHunkCalls()) != 1 {
		t.Fatal("失败后按钮不可重试或跳过确认")
	}
}

func TestReviewHunkActionsHiddenWhenUnsupported(t *testing.T) {
	for _, name := range []string{"client", "running", "binary", "rename", "added", "deleted", "content-binary", "too-large", "missing"} {
		t.Run(name, func(t *testing.T) {
			fake := NewFakeClient()
			list, full := reviewFixture()
			file, content := list.Files[0], *full.File
			var client Client = fake
			switch name {
			case "client":
				client = reviewHookClient{Client: fake, source: fake}
			case "running":
				list.Running = true
			case "binary":
				file.Binary = true
			case "rename":
				file.OrigPath, file.Status = "old.go", "R"
			case "added":
				file.Status = "A"
			case "deleted":
				file.Status = "D"
			case "content-binary":
				content.Binary = true
			case "too-large":
				content.TooLarge = true
			case "missing":
				content.Missing = true
			}
			// 使用可显示的 Buffer，只让配置函数检查不支持标记。
			s := newChatState(client)
			t.Cleanup(s.close)
			view := newReviewDiffView(*full.File, "go")
			view.state.HunkActions = true
			tt := ui.NewTester(func(c *ui.Context) {
				configureReviewHunkActions(c, s, &diffPanelState{}, "t1", list, file, content, view)
				renderReviewDiffView(c, "unsupported", view, false, 800)
			}, 800, 400)
			tt.Frame()
			tt.Advance(DurSheet + 50*time.Millisecond)
			tt.ClickAt(200, float32(view.state.LineTop(editor.DiffNew, 1))+5)
			tt.Frame()
			if view.state.HunkActions || view.state.RenderHunkActions != nil || tt.HasText("撤销此块") || tt.HasText("Revert") || tt.HasText("Keep") {
				t.Fatal("不支持撤销时显示了自定义或默认块动作")
			}
		})
	}
}

func TestReviewHunkStateBounded(t *testing.T) {
	content := ReviewFileContent{OldText: "a\nb\n", NewText: "a\nB\n"}
	view := newReviewDiffView(content, "")
	state := reviewHunkState(view, 0)
	if state != reviewHunkState(view, 0) || state == reviewHunkState(view, 1) {
		t.Fatal("块状态没有按视图和块隔离")
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range reviewHunkStateLimit * 2 {
				next := newReviewDiffView(content, "")
				reviewHunkState(next, 0)
			}
		}()
	}
	wg.Wait()
	reviewHunkStates.Lock()
	entries, order := len(reviewHunkStates.entries), len(reviewHunkStates.order)
	reviewHunkStates.Unlock()
	if entries > reviewHunkStateLimit || order > reviewHunkStateLimit || entries != order {
		t.Fatalf("状态缓存无上限或顺序泄漏: %d %d", entries, order)
	}
	// 新视图不能继承旧地址的按钮状态；弱引用也不能阻止全文回收。
	view2 := newReviewDiffView(content, "")
	if reviewHunkState(view2, 0) == state {
		t.Fatal("新视图继承了旧按钮状态")
	}
	collected := func() weak.Pointer[reviewDiffView] {
		v := newReviewDiffView(content, "")
		reviewHunkState(v, 0)
		return weak.Make(v)
	}()
	runtime.GC()
	if collected.Value() != nil {
		t.Fatal("按钮缓存阻止已淘汰视图回收")
	}
	runtime.KeepAlive(view)
}
