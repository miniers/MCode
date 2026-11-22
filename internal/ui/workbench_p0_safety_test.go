package mcodeui

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"mcode/internal/git"
	"mcode/internal/testenv"
)

type p0HistoryFakeClient struct {
	*gitHistoryFakeClient
	p0Mu        sync.Mutex
	cherryPicks []string
	reverts     []string
}

func (f *p0HistoryFakeClient) GitCherryPick(_ context.Context, _ string, sha string) error {
	f.p0Mu.Lock()
	f.cherryPicks = append(f.cherryPicks, sha)
	f.p0Mu.Unlock()
	return nil
}

func (f *p0HistoryFakeClient) GitRevertCommit(_ context.Context, _ string, sha string) error {
	f.p0Mu.Lock()
	f.reverts = append(f.reverts, sha)
	f.p0Mu.Unlock()
	return nil
}

func TestGitPaneForcePushLock(t *testing.T) {
	s, ps, client := gitPaneTestState(t)
	ps.mu.Lock()
	ps.state = GitState{WorkDir: t.TempDir(), Branch: "feature", RemoteURL: "https://example.com/repo.git"}
	ps.failure = &git.Failure{Kind: git.FailureLease, Title: "远端分支已更新"}
	ps.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		renderGitPaneToolbar(c, s, ps, ps.state, 420)
		renderGitPaneFailure(c, ps)
		renderGitPaneForceConfirm(c, ps)
	}, 700, 400)
	tt.Frame()

	// 从失败卡片点「强制推送…」应加锁写入 forceOpen 并触发重绘。
	beforeGen := s.invalidateGen.Load()
	if err := tt.Click("强制推送…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if s.invalidateGen.Load() <= beforeGen {
		t.Fatal("打开强推确认后未触发重绘")
	}
	ps.mu.Lock()
	open, branch := ps.forceOpen, ps.forceBranch
	ps.mu.Unlock()
	if !open || branch != "feature" {
		t.Fatalf("强推弹窗状态异常: open=%v branch=%q", open, branch)
	}
	if !tt.HasText("强制推送 feature？") || !tt.Focused("取消") {
		t.Fatal("强推确认弹窗标题或默认焦点不在取消")
	}

	// 点取消应加锁关闭弹窗并触发重绘，不执行强推。
	beforeGen = s.invalidateGen.Load()
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if s.invalidateGen.Load() <= beforeGen {
		t.Fatal("取消强推确认后未触发重绘")
	}
	ps.mu.Lock()
	open = ps.forceOpen
	ps.mu.Unlock()
	if open {
		t.Fatal("取消后 forceOpen 未关闭")
	}

	// 从「Git 更多操作」菜单触发强推确认。
	if err := tt.Click("Git 更多操作"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	beforeGen = s.invalidateGen.Load()
	if err := tt.Click("强制推送…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if s.invalidateGen.Load() <= beforeGen {
		t.Fatal("菜单打开强推确认后未触发重绘")
	}
	ps.mu.Lock()
	open = ps.forceOpen
	ps.mu.Unlock()
	if !open {
		t.Fatal("菜单触发强推确认后 forceOpen 应为 true")
	}

	// 并发读 ps.forceOpen / ps.forceBranch 同时渲染确认弹窗，验证无数据竞争。
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					ps.mu.Lock()
					_ = ps.forceOpen
					_ = ps.forceBranch
					ps.mu.Unlock()
				}
			}
		}()
	}
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	close(stop)
	wg.Wait()

	client.paneMu.Lock()
	calls := slices.Clone(client.calls)
	client.paneMu.Unlock()
	if slices.Contains(calls, "force") {
		t.Fatalf("未确认不应触发强制推送: %v", calls)
	}
}

func TestScriptsSheetUnsavedConfirm(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	if err := fake.SetProjectScripts(context.Background(), "thread", []ProjectScript{
		{ID: "s1", Name: "lint", Command: "npm run lint", RunOnWorktreeCreate: false},
	}); err != nil {
		t.Fatal(err)
	}
	s := newChatState(fake)
	t.Cleanup(s.close)
	t.Cleanup(func() { modalDragReset(modalDragScriptsSheet) })
	s.mu.Lock()
	s.selectedThreadID = "thread"
	s.threads = []Thread{{ID: "thread"}}
	s.mu.Unlock()

	openScriptsSheet(s, "thread")
	st := getScriptsSheetState(s)
	gitPaneWait(t, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.loading && len(st.rows) == 1
	})

	tt := ui.NewTester(func(c *ui.Context) { renderScriptsSheet(c, s) }, 1000, 800)
	tt.Frame()

	// 干净表单点取消应直接关闭，不弹出确认框。
	if err := tt.Click("取消脚本编辑"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	st.mu.Lock()
	open, confirmDiscard := st.open, st.confirmDiscard
	st.mu.Unlock()
	if open || confirmDiscard {
		t.Fatalf("干净表单关闭异常: open=%v confirmDiscard=%v", open, confirmDiscard)
	}

	// 重新打开并制造脏改动，点取消 / 按 Esc 应弹出放弃确认框。
	openScriptsSheet(s, "thread")
	gitPaneWait(t, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.loading && len(st.rows) == 1
	})
	tt.Frame()

	st.mu.Lock()
	st.rows[0].command = "npm test"
	st.mu.Unlock()
	tt.Frame()

	if err := tt.Click("取消脚本编辑"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	st.mu.Lock()
	open, confirmDiscard = st.open, st.confirmDiscard
	st.mu.Unlock()
	if !open || !confirmDiscard {
		t.Fatalf("脏表单应触发确认框: open=%v confirmDiscard=%v", open, confirmDiscard)
	}
	if !tt.HasText("放弃未保存修改？") || !tt.Focused("取消") {
		t.Fatal("未显示放弃确认弹窗或默认焦点不在取消")
	}

	// 点确认框「取消」应保留编辑状态并返回脚本弹窗。
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	st.mu.Lock()
	open, confirmDiscard = st.open, st.confirmDiscard
	cmd := st.rows[0].command
	st.mu.Unlock()
	if !open || confirmDiscard || cmd != "npm test" {
		t.Fatalf("取消放弃后状态异常: open=%v confirmDiscard=%v cmd=%q", open, confirmDiscard, cmd)
	}

	// 按 Esc 关闭脏表单同样触发确认框，点「放弃修改」后真正关闭。
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	st.mu.Lock()
	open, confirmDiscard = st.open, st.confirmDiscard
	st.mu.Unlock()
	if !open || !confirmDiscard {
		t.Fatalf("Esc 关闭脏表单应触发确认框: open=%v confirmDiscard=%v", open, confirmDiscard)
	}
	if err := tt.Click("放弃修改"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	st.mu.Lock()
	open, confirmDiscard = st.open, st.confirmDiscard
	rowsLen := len(st.rows)
	st.mu.Unlock()
	if open || confirmDiscard || rowsLen != 0 {
		t.Fatalf("确认放弃后应关闭弹窗: open=%v confirmDiscard=%v rows=%d", open, confirmDiscard, rowsLen)
	}
}

func TestWorkbenchP0DestructiveConfirm(t *testing.T) {
	t.Run("history_confirm", func(t *testing.T) {
		s, ps, h, base := gitHistoryTestState(t)
		client := &p0HistoryFakeClient{gitHistoryFakeClient: base}
		s.client = client
		sha := "abcdef012345"

		tt := ui.NewTester(func(c *ui.Context) {
			ui.ButtonBase(c.Key("p0-history-row")).Label("提交行").Size(300, RowHeight).
				ContextMenu(gitHistoryMenu(c, s, ps, h, client, git.LogCommit{SHA: sha})).
				Children(func() { ui.Text(c, "提交行") })
			renderGitHistoryDialog(c, s, ps, h, client)
		}, 600, 360)

		cases := []struct {
			menu       []string
			wantTitle  string
			confirmBtn string
			verify     func() bool
		}{
			{
				menu:       []string{"检出（分离 HEAD）"},
				wantTitle:  "检出 abcdef0（分离 HEAD）？",
				confirmBtn: "检出",
				verify: func() bool {
					client.FakeClient.mu.Lock()
					defer client.FakeClient.mu.Unlock()
					return slices.Contains(client.FakeClient.gitCheckoutCalls, sha)
				},
			},
			{
				menu:       []string{"cherry-pick"},
				wantTitle:  "将提交 abcdef0 cherry-pick 到当前分支？",
				confirmBtn: "cherry-pick",
				verify: func() bool {
					client.p0Mu.Lock()
					defer client.p0Mu.Unlock()
					return slices.Contains(client.cherryPicks, sha)
				},
			},
			{
				menu:       []string{"还原（revert）"},
				wantTitle:  "还原提交 abcdef0（revert）？",
				confirmBtn: "还原",
				verify: func() bool {
					client.p0Mu.Lock()
					defer client.p0Mu.Unlock()
					return slices.Contains(client.reverts, sha)
				},
			},
			{
				menu:       []string{"重置到此", "保留改动（soft）"},
				wantTitle:  "重置到 abcdef0（soft）？",
				confirmBtn: "重置",
				verify: func() bool {
					client.historyMu.Lock()
					defer client.historyMu.Unlock()
					return slices.Contains(client.resets, sha+":soft")
				},
			},
			{
				menu:       []string{"重置到此", "保留工作区（mixed）"},
				wantTitle:  "重置到 abcdef0（mixed）？",
				confirmBtn: "重置",
				verify: func() bool {
					client.historyMu.Lock()
					defer client.historyMu.Unlock()
					return slices.Contains(client.resets, sha+":mixed")
				},
			},
			{
				menu:       []string{"重置到此", "丢弃全部…（hard）"},
				wantTitle:  "重置到 abcdef0 并丢弃全部改动？",
				confirmBtn: "丢弃并重置",
				verify: func() bool {
					client.historyMu.Lock()
					defer client.historyMu.Unlock()
					return slices.Contains(client.resets, sha+":hard")
				},
			},
		}

		for _, tc := range cases {
			if err := tt.RightClick("提交行"); err != nil {
				t.Fatal(err)
			}
			if len(tc.menu) == 1 {
				if err := tt.ChooseMenuItem(tc.menu[0]); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := tt.ChooseMenuItem(tc.menu[0], tc.menu[1]); err != nil {
					t.Fatal(err)
				}
			}
			tt.Advance(DurSlow)
			if tc.verify() {
				t.Fatalf("%s 在确认前不应执行", tc.wantTitle)
			}
			if !tt.HasText(tc.wantTitle) {
				t.Fatalf("缺少确认标题 %q，实际文本：%v", tc.wantTitle, tt.Texts())
			}
			if !tt.Focused("取消") {
				t.Fatalf("%s 默认焦点不在取消", tc.wantTitle)
			}
			if err := tt.Click(tc.confirmBtn); err != nil {
				t.Fatal(err)
			}
			gitPaneWait(t, tc.verify)
			gitPaneWait(t, func() bool { return !h.isWriting() })
			tt.Frame()
		}
	})

	t.Run("conflict_confirm", func(t *testing.T) {
		_, ps, f, tt := newGitDiffTester(t, git.FileDiff{}, "<<<<<<< ours\nA\n=======\nB\n>>>>>>> theirs\n")
		conflicts := []git.ConflictFile{{Path: "a.go"}, {Path: "b.go"}}
		f.gitPaneFakeClient.paneMu.Lock()
		f.gitPaneFakeClient.op = git.OpState{Conflicts: conflicts}
		f.gitPaneFakeClient.paneMu.Unlock()
		ps.mu.Lock()
		ps.op.Conflicts = conflicts
		ps.mu.Unlock()
		tt.Frame()

		// 整文件覆盖先弹窗确认，取消不调用客户端。
		if err := tt.Click("差异菜单"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if err := tt.Click("整个文件用 当前"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if !tt.HasText("整个文件用「当前」覆盖？") || !tt.Focused("取消") {
			t.Fatalf("整文件覆盖确认弹窗异常，文本：%v", tt.Texts())
		}
		f.diffMu.Lock()
		called := slices.Contains(f.diffCalls, "ours")
		f.diffMu.Unlock()
		if called {
			t.Fatal("确认前不应执行整文件覆盖")
		}
		if err := tt.Click("取消"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()

		// 全部文件覆盖先弹窗确认，确认后再执行。
		if err := tt.Click("差异菜单"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if err := tt.Click("全部用 当前"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if !tt.HasText("全部 2 个冲突文件用「当前」覆盖？") || !tt.Focused("取消") {
			t.Fatalf("全部覆盖确认弹窗异常，文本：%v", tt.Texts())
		}
		if err := tt.Click("全部覆盖"); err != nil {
			t.Fatal(err)
		}
		st := gitDiffStateFor(ps)
		waitFor(t, tt, func() bool {
			st.mu.Lock()
			defer st.mu.Unlock()
			return !st.pending && !st.loading
		})
		f.diffMu.Lock()
		calls := slices.Clone(f.diffCalls)
		f.diffMu.Unlock()
		if !slices.Contains(calls, "ours") || !slices.Contains(calls, "mark") {
			t.Fatalf("确认后未执行冲突覆盖：%v", calls)
		}
	})

	t.Run("pr_merge_label_and_approve_confirm", func(t *testing.T) {
		f := &gitPRActionFake{FakeClient: NewFakeClient(), calls: make(chan gitPRActionCall, 2)}
		s := newChatState(f)
		t.Cleanup(s.close)
		s.mu.Lock()
		s.selectedThreadID = "thread-1"
		s.threads = []Thread{{ID: "thread-1", ProjectID: "p1"}}
		s.mu.Unlock()
		pr := git.PRDetail{PRSummary: git.PRSummary{Number: 42, State: "open"}}
		st := getGitPRDialogState(s)
		st.mu.Lock()
		st.open, st.threadID, st.selected = true, "thread-1", 42
		st.detail = &pr
		st.mu.Unlock()

		tt := ui.NewTester(func(c *ui.Context) { renderGitPRDetail(c, s, pr, 600) }, 760, 620)
		tt.Frame()

		for _, item := range []struct {
			menuLabel string
		}{
			{"Squash 合并"},
			{"合并提交"},
			{"变基合并"},
		} {
			if err := tt.Click("选择合并方式"); err != nil {
				t.Fatal(err)
			}
			tt.Advance(time.Second)
			if err := tt.Click(item.menuLabel); err != nil {
				t.Fatal(err)
			}
			tt.Advance(time.Second)
			if err := tt.Click(item.menuLabel); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if !tt.HasText("确认合并") || !tt.HasText("取消") {
				t.Fatalf("合并后未进入二次确认：%v", tt.Texts())
			}
			select {
			case got := <-f.calls:
				t.Fatalf("未确认前不应合并 PR：%v", got)
			default:
			}
			if err := tt.Click("取消"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
		}

		// 二次确认后才发出 merge。
		if err := tt.Click("变基合并"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if err := tt.Click("确认合并"); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-f.calls:
			if got.kind != "merge" || got.number != 42 || got.value != "rebase" {
				t.Fatalf("合并确认后调用参数异常：%v", got)
			}
		case <-time.After(time.Second):
			t.Fatal("合并确认后未发起请求")
		}
	})

	t.Run("review_comment_delete_confirm", func(t *testing.T) {
		s, _, dp, key, _ := reviewCommentsSetup(t)
		diffPanelMu.Lock()
		dp.comments = []*reviewComment{{
			id:    1,
			key:   key,
			lines: editor.DiffLineRange{Side: editor.DiffNew, Start: 0, End: 0},
			body:  "请检查空指针风险",
		}}
		diffPanelMu.Unlock()

		tt := ui.NewTester(func(c *ui.Context) {
			diffPanelMu.Lock()
			var list []reviewComment
			for _, item := range dp.comments {
				list = append(list, *item)
			}
			diffPanelMu.Unlock()
			for _, item := range list {
				renderReviewComment(c, s, dp, item)
			}
		}, 480, 120)
		tt.Frame()

		if err := tt.Click("删除评论"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		diffPanelMu.Lock()
		count := len(dp.comments)
		diffPanelMu.Unlock()
		if count != 1 || !tt.HasText("确认删除") {
			t.Fatalf("首次点击删除不应立即删掉评论：count=%d texts=%v", count, tt.Texts())
		}

		if err := tt.Click("取消删除"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		diffPanelMu.Lock()
		count = len(dp.comments)
		diffPanelMu.Unlock()
		if count != 1 || tt.HasText("确认删除") {
			t.Fatalf("取消删除后状态异常：count=%d texts=%v", count, tt.Texts())
		}

		if err := tt.Click("删除评论"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if err := tt.Click("确认删除"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		diffPanelMu.Lock()
		count = len(dp.comments)
		diffPanelMu.Unlock()
		if count != 0 {
			t.Fatalf("确认删除后评论应移除，剩余=%d", count)
		}
	})

	t.Run("review_diff_view_width_lock_and_blame_hover", func(t *testing.T) {
		dp := &diffPanelState{}
		if !updateReviewPanelWidth(dp, 440.1) {
			t.Fatal("首次宽度变化应返回 true")
		}
		if updateReviewPanelWidth(dp, 440.4) {
			t.Fatal("亚像素抖动不应触发重绘")
		}
		if !updateReviewPanelWidth(dp, 441.2) {
			t.Fatal("整像素变化应返回 true")
		}
		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for i := 0; i < 100; i++ {
					updateReviewPanelWidth(dp, float32(400+((id+i)%5))+0.25)
					diffPanelMu.Lock()
					_ = dp.reviewWidth
					diffPanelMu.Unlock()
				}
			}(g)
		}
		wg.Wait()

		// Blame 视图静止时不重绘。
		testenv.TempHome(t)
		client := &gitBlameFakeClient{FakeClient: NewFakeClient()}
		s := newChatState(client)
		t.Cleanup(s.close)
		ps := &gitPaneState{owner: s}
		t.Cleanup(func() { gitBlameStates.Delete(ps) })
		tt := ui.NewTester(func(c *ui.Context) {
			renderGitBlameView(c, s, "thread", ps, "file.go", nil)
		}, 600, 300)
		st := gitBlameStateFor(ps)
		gitPaneWait(t, func() bool {
			st.mu.Lock()
			defer st.mu.Unlock()
			return len(st.lines) == 3
		})
		tt.Frame()
		tt.Advance(DurSlow)
		tt.Frame()
		if tt.FramePending() {
			t.Fatal("Blame 视图静止后仍在请求下一帧")
		}
	})
}
