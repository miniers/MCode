package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
	"mcode/internal/testenv"
)

type gitPaneFakeClient struct {
	*FakeClient
	paneMu    sync.Mutex
	state     GitState
	entries   []git.StatusEntry
	op        git.OpState
	stateHook func(context.Context) (GitState, error)
	pullHook  func(context.Context) error
	fetchHook func(context.Context) error
	pullErr   error
	calls     []string
}

func (f *gitPaneFakeClient) GitState(ctx context.Context, _ string) (GitState, error) {
	f.paneMu.Lock()
	hook, state := f.stateHook, f.state
	f.paneMu.Unlock()
	if hook != nil {
		return hook(ctx)
	}
	return state, nil
}
func (f *gitPaneFakeClient) GitStatusEntries(context.Context, string) ([]git.StatusEntry, error) {
	f.paneMu.Lock()
	defer f.paneMu.Unlock()
	return append([]git.StatusEntry(nil), f.entries...), nil
}
func (f *gitPaneFakeClient) GitOpState(context.Context, string) (git.OpState, error) {
	f.paneMu.Lock()
	defer f.paneMu.Unlock()
	return f.op, nil
}
func (f *gitPaneFakeClient) record(action string) {
	f.paneMu.Lock()
	f.calls = append(f.calls, action)
	f.paneMu.Unlock()
}
func (f *gitPaneFakeClient) GitPullWith(ctx context.Context, _, strategy string) error {
	f.record("pull:" + strategy)
	f.paneMu.Lock()
	hook, err := f.pullHook, f.pullErr
	f.paneMu.Unlock()
	if hook != nil {
		return hook(ctx)
	}
	return err
}
func (f *gitPaneFakeClient) GitPush(context.Context, string) error { f.record("push"); return nil }
func (f *gitPaneFakeClient) GitFetch(ctx context.Context, _ string) error {
	f.record("fetch")
	f.paneMu.Lock()
	hook := f.fetchHook
	f.paneMu.Unlock()
	if hook != nil {
		return hook(ctx)
	}
	return nil
}
func (f *gitPaneFakeClient) GitPushForceWithLease(context.Context, string) error {
	f.record("force")
	return nil
}
func (f *gitPaneFakeClient) GitContinueOp(context.Context, string) error {
	f.record("continue")
	return nil
}
func (f *gitPaneFakeClient) GitSkipOp(context.Context, string) error  { f.record("skip"); return nil }
func (f *gitPaneFakeClient) GitAbortOp(context.Context, string) error { f.record("abort"); return nil }
func (f *gitPaneFakeClient) GitRestoreSnapshot(context.Context, string, *git.Snapshot) error {
	f.record("undo")
	return nil
}

func gitPaneTestState(t *testing.T) (*chatState, *gitPaneState, *gitPaneFakeClient) {
	t.Helper()
	testenv.TempHome(t)
	client := &gitPaneFakeClient{FakeClient: NewFakeClient()}
	s := newChatState(client)
	s.mu.Lock()
	s.selectedThreadID = "git-test"
	s.threads = []Thread{{ID: "git-test"}}
	s.mu.Unlock()
	t.Cleanup(s.close)
	return s, getGitPaneState(s), client
}

func gitPaneWait(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !check() {
		select {
		case <-deadline.C:
			t.Fatal("等待 Git 面板状态超时")
		case <-tick.C:
		}
	}
}

func TestGitPaneShortcutOpensPane(t *testing.T) {
	setShortcutOverrides(nil)
	t.Cleanup(func() { setShortcutOverrides(nil) })
	s, _, _ := gitPaneTestState(t)
	id, _, ok := matchShortcut(ui.Cmd|ui.Shift, ui.KeyG)
	if !ok || id != "toggle-git-pane" {
		t.Fatalf("快捷键动作 = %q，%v", id, ok)
	}
	sidePaneByAction(id).toggle(s)
	if !inspectorPaneOpen(s, sidePaneGitID) {
		t.Fatal("快捷键没有打开 Git 面板")
	}
	sidePaneByAction(id).toggle(s)
	if inspectorPaneOpen(s, sidePaneGitID) {
		t.Fatal("重复快捷键没有关闭 Git 面板")
	}
	if pref := normalizeInspectorPreference(InspectorPreference{Active: sidePaneGitID}); pref.Active != sidePaneGitID {
		t.Fatal("Git 面板偏好被丢弃")
	}
}

func TestGitPaneSyncButtonLabels(t *testing.T) {
	for _, tc := range []struct {
		name          string
		state         GitState
		label, action string
	}{
		{"无上游", GitState{}, "发布", "push"},
		{"落后", GitState{HasUpstream: true, Behind: 3}, "拉取 ↓3", "pull"},
		{"领先", GitState{HasUpstream: true, Ahead: 2}, "推送 ↑2", "push"},
		{"分叉", GitState{HasUpstream: true, Ahead: 2, Behind: 3}, "同步", "sync"},
		{"同步", GitState{HasUpstream: true}, "抓取", "fetch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ps, client := gitPaneTestState(t)
			tc.state.WorkDir = "/repo"
			tc.state.Branch = "feature"
			ps.state = tc.state
			label, action := gitPaneSyncAction(tc.state)
			if label != tc.label || action != tc.action {
				t.Fatalf("同步状态 = %s/%s", label, action)
			}
			tt := ui.NewTester(func(c *ui.Context) { renderGitPaneToolbar(c, s, ps, tc.state, 600) }, 600, 100)
			btnLabel := tc.label
			if tc.action == "fetch" {
				btnLabel = "刷新"
			}
			if node, ok := tt.Access(btnLabel); !ok || node.Disabled || node.Description != btnLabel {
				t.Fatalf("同步按钮 = %+v，%v", node, ok)
			}
			if tc.action != "fetch" && !tt.HasText(tc.label) {
				t.Fatalf("缺少同步文案 %s", tc.label)
			}
			if err := tt.Click(btnLabel); err != nil {
				t.Fatal(err)
			}
			gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return !ps.syncing })
			want := []string{tc.action}
			switch tc.action {
			case "pull":
				want = []string{"pull:"}
			case "sync":
				want = []string{"pull:", "push"}
			}
			client.paneMu.Lock()
			calls := append([]string(nil), client.calls...)
			client.paneMu.Unlock()
			if !slices.Equal(calls, want) {
				t.Fatalf("同步操作 = %v，预期 %v", calls, want)
			}
		})
	}
}

func TestGitPaneToolbarBackgroundRefresh(t *testing.T) {
	s, ps, _ := gitPaneTestState(t)
	ps.state = GitState{WorkDir: "/repo", Branch: "feature"}
	ps.loading = true
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneToolbar(c, s, ps, ps.state, 600) }, 600, 100)
	if _, ok := tt.Find("加载中"); ok {
		t.Fatal("后台刷新时分支按钮不应显示加载指示器")
	}
	if !tt.HasText("feature") {
		t.Fatal("后台刷新时分支名应保持可见")
	}
}

func TestGitPaneDetachedAndNarrowToolbar(t *testing.T) {
	s, ps, client := gitPaneTestState(t)
	state := GitState{WorkDir: "/repo", Detached: true, Head: "0123456789", HasUpstream: true, Ahead: 1}
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneToolbar(c, s, ps, state, 350) }, 350, 100)
	if node, ok := tt.Access("刷新"); !ok || node.Disabled || node.Description != "刷新" {
		t.Fatalf("分离 HEAD 按钮 = %+v，%v", node, ok)
	}
	if tt.HasText("推送 ↑1") || !tt.HasText("0123456") {
		t.Fatalf("分离 HEAD 文案或短哈希错误：%q", tt.Texts())
	}
	if err := tt.Click("刷新"); err != nil {
		t.Fatal(err)
	}
	gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return !ps.refreshing })
	client.paneMu.Lock()
	calls := append([]string(nil), client.calls...)
	client.paneMu.Unlock()
	if slices.Contains(calls, "fetch") || slices.Contains(calls, "push") {
		t.Fatalf("分离 HEAD 点击刷新不应触发远端操作：%v", calls)
	}
}

func TestGitPaneOpConflictAndAgentBusy(t *testing.T) {
	s, ps, _ := gitPaneTestState(t)
	ps.op = git.OpState{Kind: git.OpRebase, Branch: "feature", Onto: "main", Step: 2, Total: 5, Conflicts: []git.ConflictFile{{}}}
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneOpBanner(c, s, ps) }, 700, 100)
	if node, ok := tt.Access("继续"); !ok || !node.Disabled || node.Description != "还有 1 个冲突" {
		t.Fatalf("继续按钮 = %+v，%v", node, ok)
	}
	ps.mu.Lock()
	ps.op.Conflicts = nil
	ps.mu.Unlock()
	tt.Frame()
	if node, ok := tt.Access("继续"); !ok || node.Disabled {
		t.Fatal("冲突清零后继续仍禁用")
	}
	s.mu.Lock()
	s.threads[0].IsRunning = true
	s.mu.Unlock()
	tt.Frame()
	for _, label := range []string{"中止", "跳过", "继续"} {
		if node, ok := tt.Access(label); !ok || !node.Disabled || node.Description != "Agent 正在修改此工作区" {
			t.Fatalf("Agent 运行时按钮 = %+v，%v", node, ok)
		}
	}
	ps.mu.Lock()
	ps.op.Kind = git.OpMerge
	ps.mu.Unlock()
	tt.Frame()
	if _, ok := tt.Find("跳过"); ok {
		t.Fatal("合并不应显示跳过")
	}
}

func TestGitPaneCanceledFailureHidden(t *testing.T) {
	_, ps, _ := gitPaneTestState(t)
	ps.fail(errors.New("failed to push some refs"))
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneFailure(c, ps) }, 600, 240)
	if !tt.HasText("远端有新的提交") || !tt.HasText("拉取并重试") {
		t.Fatal("推送拒绝后缺少错误横幅")
	}
	ps.fail(context.Canceled)
	tt.Frame()
	tt.Advance(DurSheet)
	if tt.HasText("远端有新的提交") || tt.HasText("操作已取消") {
		t.Fatal("取消后仍显示错误横幅")
	}
	ps.mu.Lock()
	ps.failure = &git.Failure{Kind: git.FailureCanceled, Title: "操作已取消"}
	ps.mu.Unlock()
	tt.Frame()
	if tt.HasText("操作已取消") {
		t.Fatal("canceled 错误不应渲染")
	}
}

func TestGitPaneRefreshCoalesces(t *testing.T) {
	_, ps, client := gitPaneTestState(t)
	started := make(chan int, 4)
	release := make(chan struct{}, 4)
	var calls, active, maxActive int
	client.stateHook = func(ctx context.Context) (GitState, error) {
		client.paneMu.Lock()
		calls++
		n := calls
		active++
		maxActive = max(active, maxActive)
		client.paneMu.Unlock()
		started <- n
		select {
		case <-release:
		case <-ctx.Done():
		}
		client.paneMu.Lock()
		active--
		client.paneMu.Unlock()
		return GitState{Branch: "feature"}, nil
	}
	ps.requestRefresh()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("首次刷新未开始")
	}
	for range 20 {
		ps.requestRefresh()
	}
	client.paneMu.Lock()
	n := calls
	client.paneMu.Unlock()
	if n != 1 {
		t.Fatalf("在途刷新未合并：%d", n)
	}
	release <- struct{}{}
	select {
	case n := <-started:
		if n != 2 {
			t.Fatalf("补跑序号 = %d", n)
		}
	case <-time.After(time.Second):
		t.Fatal("刷新没有补跑")
	}
	release <- struct{}{}
	gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return !ps.refreshing })
	client.paneMu.Lock()
	defer client.paneMu.Unlock()
	if calls != 2 || maxActive != 1 {
		t.Fatalf("刷新次数 = %d，最大并发 = %d", calls, maxActive)
	}
}

func TestGitPaneRefreshHiddenDiscardsResult(t *testing.T) {
	_, ps, client := gitPaneTestState(t)
	client.mu.Lock()
	baseline := len(client.summaryWatchers)
	client.mu.Unlock()
	started := make(chan struct{})
	release := make(chan struct{})
	client.stateHook = func(ctx context.Context) (GitState, error) {
		close(started)
		<-release
		return GitState{Branch: "过期"}, nil
	}
	ps.setVisible(true)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("刷新未开始")
	}
	ps.setVisible(false)
	close(release)
	gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return !ps.refreshing })
	ps.mu.Lock()
	state, visible := ps.state, ps.visible
	ps.mu.Unlock()
	if visible || state.Branch != "" {
		t.Fatalf("隐藏后写入过期状态：%+v", state)
	}
	gitPaneWait(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return len(client.summaryWatchers) == baseline
	}) // 隐藏后恢复原有订阅数。
}

func TestGitPaneSyncPullFailureDoesNotPush(t *testing.T) {
	_, ps, client := gitPaneTestState(t)
	client.pullErr = errors.New("network is unreachable")
	ps.run("sync")
	gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return !ps.syncing && !ps.refreshing })
	client.paneMu.Lock()
	calls := append([]string(nil), client.calls...)
	client.paneMu.Unlock()
	if len(calls) != 1 || calls[0] != "pull:" {
		t.Fatalf("拉取失败后仍推送：%v", calls)
	}
	ps.mu.Lock()
	failure := ps.failure
	ps.mu.Unlock()
	if failure == nil || failure.Kind != git.FailureNetwork {
		t.Fatalf("失败分类 = %+v", failure)
	}
}

func TestGitPaneSyncCancellation(t *testing.T) {
	_, ps, client := gitPaneTestState(t)
	started := make(chan struct{})
	client.pullHook = func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() }
	ps.run("sync")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("同步未开始")
	}
	ps.mu.Lock()
	cancel := ps.cancel
	ps.mu.Unlock()
	cancel()
	gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return !ps.syncing && !ps.refreshing })
	ps.mu.Lock()
	failure := ps.failure
	ps.mu.Unlock()
	if failure != nil {
		t.Fatalf("取消显示错误：%+v", failure)
	}
	client.paneMu.Lock()
	defer client.paneMu.Unlock()
	if len(client.calls) != 1 {
		t.Fatalf("取消后仍推送：%v", client.calls)
	}
}

func TestGitPaneForceConfirmFocusesCancel(t *testing.T) {
	_, ps, _ := gitPaneTestState(t)
	ps.state = GitState{Branch: "feature"}
	ps.forceOpen = true
	ps.forceBranch = "feature"
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneForceConfirm(c, ps) }, 600, 300)
	tt.Frame()
	if !tt.Focused("取消") {
		t.Fatal("强制推送确认默认焦点不在取消")
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	ps.mu.Lock()
	syncing := ps.syncing
	ps.mu.Unlock()
	if syncing || ps.forceOpen {
		t.Fatal("默认回车没有取消确认")
	}
}

func TestGitPaneUndoExpiresAndRestores(t *testing.T) {
	s, ps, client := gitPaneTestState(t)
	ps.showUndo("已丢弃改动", &git.Snapshot{})
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneUndo(c, ps) }, 400, 80)
	if !tt.HasText("撤销") {
		t.Fatal("缺少撤销入口")
	}
	if err := tt.Click("撤销"); err != nil {
		t.Fatal(err)
	}
	gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return !ps.syncing && ps.undoSnap == nil })
	gitPaneWait(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.toastMessage == "已撤销" })
	client.paneMu.Lock()
	calls := append([]string(nil), client.calls...)
	client.paneMu.Unlock()
	if len(calls) != 1 || calls[0] != "undo" {
		t.Fatalf("撤销调用 = %v", calls)
	}
	ps.showUndo("已丢弃改动", &git.Snapshot{})
	ps.mu.Lock()
	ps.undoTimer.Reset(time.Millisecond)
	ps.mu.Unlock()
	gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return ps.undoSnap == nil })
}

func TestGitStatusPresentationAndConflicts(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		for _, tc := range []struct {
			code   byte
			letter string
			color  ui.Color
		}{
			{'M', "M", ThemeWarning(c)}, {'A', "A", ThemeSuccess(c)}, {'D', "D", ThemeDanger(c)},
			{'R', "R", ThemeAccent(c)}, {'C', "C", ThemeAccent(c)}, {'?', "?", ThemeSuccess(c).Alpha(0.7)}, {'U', "!", ThemeDanger(c)},
		} {
			letter, color := gitStatusPresentation(c, tc.code)
			if letter != tc.letter || color != tc.color {
				t.Errorf("状态 %c = %q/%v", tc.code, letter, color)
			}
		}
		if got := gitLaneColors(c); got != [6]ui.Color{ThemeAccent(c), ThemeSuccess(c), ThemeWarning(c), ThemePlan(c), ThemeCaution(c), ThemeSecondaryText(c)} {
			t.Error("提交图轨道颜色错误")
		}
	}, 100, 100)
	_ = tt
	for _, code := range []string{"UU", "AU", "UD", "AA", "DD"} {
		if !gitEntryConflicted(git.StatusEntry{Index: code[0], Worktree: code[1]}) {
			t.Errorf("%s 应识别为冲突", code)
		}
	}
	if gitEntryConflicted(git.StatusEntry{Index: 'A', Worktree: 'M'}) {
		t.Fatal("普通修改被误报冲突")
	}
}

func TestGitPaneWatchIgnore(t *testing.T) {
	for _, path := range []string{".git/objects", ".git/objects/ab", ".git/logs/HEAD", "node_modules", "src/node_modules/x", "src/node_modules"} {
		if !gitPaneWatchIgnore(path, true) {
			t.Errorf("没有忽略 %s", path)
		}
	}
	for _, path := range []string{".git/HEAD", ".git/index", "src/main.go", ".github/workflows"} {
		if gitPaneWatchIgnore(path, true) {
			t.Errorf("不应忽略 %s", path)
		}
	}
}

func TestGitPaneQueuedRefreshStopsWhenHidden(t *testing.T) {
	_, ps, client := gitPaneTestState(t)
	calls := 0
	client.stateHook = func(context.Context) (GitState, error) { calls++; return GitState{}, nil }
	// 模拟刷新已排队、工作 goroutine 尚未取得锁时隐藏。
	ps.visible, ps.refreshing, ps.refreshSeq = true, true, 1
	ps.setVisible(false)
	ps.refresh(client, 1)
	if calls != 0 || ps.refreshing {
		t.Fatalf("隐藏后排队刷新仍执行：calls=%d refreshing=%v", calls, ps.refreshing)
	}
}

func TestGitPaneRefreshLoadingDelay(t *testing.T) {
	_, ps, client := gitPaneTestState(t)
	started, release := make(chan struct{}), make(chan struct{})
	client.stateHook = func(ctx context.Context) (GitState, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return GitState{}, nil
	}
	ps.setVisible(true)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("刷新未启动")
	}
	ps.mu.Lock()
	loading := ps.loading
	ps.mu.Unlock()
	if loading {
		t.Fatal("快速刷新提前显示加载态")
	}
	gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return ps.loading })
	close(release)
	gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return !ps.refreshing && !ps.loading })
}

func TestGitPaneRoundEndRefreshAndSessionLifecycle(t *testing.T) {
	s, ps, client := gitPaneTestState(t)
	client.stateHook = func(context.Context) (GitState, error) {
		client.record("refresh")
		return GitState{Branch: "main"}, nil
	}
	inspectorSelect(s, sidePaneGitID, false)
	gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return !ps.refreshing })
	client.mu.Lock()
	var callbacks []func(Thread)
	for _, cb := range client.summaryWatchers {
		callbacks = append(callbacks, cb)
	}
	client.mu.Unlock()
	if len(callbacks) == 0 {
		t.Fatal("Git 面板没有订阅轮次结束")
	}
	for _, cb := range callbacks {
		cb(Thread{ID: ps.threadID, IsRunning: true})
		cb(Thread{ID: "other"})
	}
	client.paneMu.Lock()
	count := len(client.calls)
	client.paneMu.Unlock()
	if count != 1 {
		t.Fatalf("无关运行态触发了刷新：%d", count)
	}
	for _, cb := range callbacks {
		cb(Thread{ID: ps.threadID})
	}
	gitPaneWait(t, func() bool { client.paneMu.Lock(); defer client.paneMu.Unlock(); return len(client.calls) == 2 })
	gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return !ps.refreshing })
	s.mu.Lock()
	s.selectedThreadID = "other"
	s.mu.Unlock()
	inspectorSwitchThread(s, ps.threadID)
	ps.mu.Lock()
	visible := ps.visible
	ps.mu.Unlock()
	if visible {
		t.Fatal("切换会话后旧 Git 面板仍在监听")
	}
	for _, cb := range callbacks {
		cb(Thread{ID: ps.threadID})
	}
	client.paneMu.Lock()
	count = len(client.calls)
	client.paneMu.Unlock()
	if count != 2 {
		t.Fatal("旧会话回调仍触发刷新")
	}
	other := getGitPaneState(s)
	if other == ps || other.threadID != "other" {
		t.Fatal("Git 状态没有按会话隔离")
	}
	s.close()
	ps.mu.Lock()
	disposed := ps.disposed
	ps.mu.Unlock()
	other.mu.Lock()
	otherDisposed := other.disposed
	other.mu.Unlock()
	if !disposed || !otherDisposed {
		t.Fatal("关窗后 Git 状态未释放")
	}
}

func TestGitPaneMissingCapabilityEmpty(t *testing.T) {
	s := sidePaneTestState(t)
	defer s.close()
	tt := ui.NewTester(func(c *ui.Context) { renderGitPane(c, s) }, 480, 300)
	if !tt.HasText("Git") {
		t.Fatal("缺少 Git 空状态")
	}
	if _, ok := tt.Find("Git 更多操作"); ok {
		t.Fatal("缺少能力时不应显示工具条")
	}
}

func TestGitPaneEmptyStates(t *testing.T) {
	s, ps, _ := gitPaneTestState(t)
	ps.mu.Lock()
	ps.visible = true
	ps.mu.Unlock()
	tt := ui.NewTester(func(c *ui.Context) { renderGitPane(c, s) }, 480, 300)
	if !tt.HasText("不是 Git 仓库") {
		t.Fatal("缺少非仓库空状态")
	}
	s.mu.Lock()
	s.selectedThreadID = ""
	s.mu.Unlock()
	tt.Frame()
	if !tt.HasText("未选中会话") {
		t.Fatal("缺少未选中会话空状态")
	}
}

func TestGitStatusSharedPresentation(t *testing.T) {
	ui.NewTester(func(c *ui.Context) {
		for _, code := range []string{"M", "A", "D", "R", "C", "?", "U"} {
			letter, _, color := fileTreeStatusPresentation(c, code)
			want, wantColor := gitStatusPresentation(c, code[0])
			if letter != want || color != wantColor {
				t.Errorf("文件树状态 %s 未复用 Git 状态色", code)
			}
		}
	}, 100, 100)
}

func TestGitPaneWatchUsesRepositoryRoot(t *testing.T) {
	state := GitState{WorkDir: "/repo/src", Worktrees: []GitWorktree{{Path: "/other"}, {Path: "/repo", Current: true}}}
	if root := gitPaneWatchRoot(state); root != "/repo" {
		t.Fatalf("仓库监听根目录 = %q", root)
	}
	state.Worktrees = nil
	if root := gitPaneWatchRoot(state); root != state.WorkDir {
		t.Fatalf("监听回退目录 = %q", root)
	}
}

func TestGitPaneMoreMenuConsolidatedActions(t *testing.T) {
	s, ps, _ := gitPaneTestState(t)
	state := GitState{WorkDir: "/repo", Branch: "feature"}
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneToolbar(c, s, ps, state, 600) }, 1000, 800)
	if err := tt.Click("Git 更多操作"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"创建 PR…", "查看 Pull Request…", "贮藏…"} {
		if _, ok := tt.Access(label); !ok {
			t.Fatalf("菜单缺少 %q", label)
		}
	}
	if _, ok := tt.Access("合并子代理改动（PR）"); ok {
		t.Fatal("没有可合并子代理时不应显示 PR 合并入口")
	}
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.setStateLocked(SwarmState{LeadThreadID: ps.threadID, Bees: []SwarmBee{{Status: "completed"}}})
	swarmPanelMu.Unlock()
	tt.Frame()
	if _, ok := tt.Access("合并子代理改动（PR）"); !ok {
		t.Fatal("缺少 PR 合并入口")
	}
	if node, ok := tt.Access("创建 PR…"); !ok || !node.Disabled || node.Description != "没有可用的远端仓库地址" {
		t.Fatalf("无远端创建 PR 状态 = %+v", node)
	}
	if tt.HasText("打开仓库") {
		t.Fatal("无远端时不应显示打开仓库")
	}
	if !tt.HasText("远端…") {
		t.Fatal("接入远端弹窗后应显示入口")
	}
	state.RemoteURL = "git@github.com:owner/repo.git"
	tt.Frame()
	if node, ok := tt.Access("创建 PR…"); !ok || node.Disabled {
		t.Fatal("有远端时创建 PR 仍禁用")
	}
	if err := tt.Click("打开仓库"); err != nil {
		t.Fatal(err)
	}
	if urls := tt.OpenedURLs(); len(urls) != 1 || urls[0] != "https://github.com/owner/repo" {
		t.Fatalf("仓库 URL = %v", urls)
	}
}

// 拉取合并成一项：菜单里只有「拉取」，点击后按默认策略拉取（strategy 为空）。
func TestGitPaneMenuSinglePull(t *testing.T) {
	s, ps, client := gitPaneTestState(t)
	state := GitState{WorkDir: "/repo", Branch: "feature", HasUpstream: true, Ahead: 1}
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneToolbar(c, s, ps, state, 600) }, 1000, 800)
	if err := tt.Click("Git 更多操作"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"拉取（变基）", "拉取（合并）"} {
		if _, ok := tt.Access(label); ok {
			t.Fatalf("菜单不应再有 %q", label)
		}
	}
	for _, label := range []string{"抓取", "拉取"} {
		if _, ok := tt.Access(label); !ok {
			t.Fatalf("菜单缺少「%s」", label)
		}
	}
	if err := tt.Click("拉取"); err != nil {
		t.Fatal(err)
	}
	gitPaneWait(t, func() bool {
		client.paneMu.Lock()
		defer client.paneMu.Unlock()
		return len(client.calls) > 0
	})
	client.paneMu.Lock()
	calls := append([]string(nil), client.calls...)
	client.paneMu.Unlock()
	if !slices.Equal(calls, []string{"pull:"}) {
		t.Fatalf("拉取调用 = %v，预期 [pull:]", calls)
	}
}

type gitPaneStageFailureClient struct{ *gitChangesFakeClient }

func (f *gitPaneStageFailureClient) GitStageFiles(context.Context, string, []string) error {
	f.record("stage")
	return errors.New("暂存被拒绝")
}

// 迁移旧表单的保护断言：选取文件失败后不得继续提交或退回全量提交。
func TestGitPaneStageFailureDoesNotCommit(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	client := &gitPaneStageFailureClient{&gitChangesFakeClient{gitPaneFakeClient: base}}
	s.client = client
	root := t.TempDir()
	ps.state = GitState{WorkDir: root}
	ps.entries = []git.StatusEntry{{Path: "a.go", Worktree: 'M'}}
	st := gitChangesStateFor(ps)
	st.root, st.draft = root, "保留草稿"
	tt := ui.NewTester(func(c *ui.Context) { renderGitComposerForTest(c, s, ps) }, 600, 600)
	if err := tt.Click("提交全部 1 个文件"); err != nil {
		t.Fatal(err)
	}
	gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return ps.failure != nil })
	base.paneMu.Lock()
	calls := append([]string(nil), base.calls...)
	base.paneMu.Unlock()
	if len(calls) != 1 || calls[0] != "stage" {
		t.Fatalf("暂存失败后执行了提交：%v", calls)
	}
	st.mu.Lock()
	draft := st.draft
	st.mu.Unlock()
	if draft != "保留草稿" {
		t.Fatalf("暂存失败后草稿丢失：%q", draft)
	}
	ps.mu.Lock()
	failure := ps.failure
	ps.mu.Unlock()
	if failure == nil {
		t.Fatal("暂存失败没有保留错误")
	}
}

type gitStashPaneTestClient struct {
	*gitPaneFakeClient
	list func(context.Context) ([]git.StashEntry, error)
	push func(context.Context, string, string) error
	pop  func(context.Context, int) error
}

func (f *gitStashPaneTestClient) GitStashList(ctx context.Context, _ string) ([]git.StashEntry, error) {
	if f.list != nil {
		return f.list(ctx)
	}
	return nil, nil
}
func (f *gitStashPaneTestClient) GitStashPush(ctx context.Context, threadID, message string) error {
	if f.push != nil {
		return f.push(ctx, threadID, message)
	}
	return nil
}
func (f *gitStashPaneTestClient) GitStashApply(context.Context, string, int) error { return nil }
func (f *gitStashPaneTestClient) GitStashDrop(context.Context, string, int) error  { return nil }
func (f *gitStashPaneTestClient) GitStashPopAt(ctx context.Context, _ string, index int) error {
	if f.pop != nil {
		return f.pop(ctx, index)
	}
	return nil
}

func TestGitStashDialogCreatesWithMessage(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	calls := make(chan [2]string, 2)
	release := make(chan struct{})
	defer close(release)
	s.client = &gitStashPaneTestClient{gitPaneFakeClient: base, push: func(ctx context.Context, threadID, message string) error {
		calls <- [2]string{threadID, message}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	d := &gitStashDialog{s: s, threadID: ps.threadID}
	ps.stash = d
	d.openStashDialog()
	gitPaneWait(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.loading })
	tt := ui.NewTester(func(c *ui.Context) { renderGitStashDialog(c, d) }, 800, 700)
	tt.Advance(DurSheet)
	if err := tt.Click("贮藏信息"); err != nil {
		t.Fatal(err)
	}
	tt.Type("  用户说明  ")
	tt.Frame()
	if tt.HasText("Esc 关闭") {
		t.Fatal("弹窗不应显示快捷键提示")
	}
	if err := tt.Click("贮藏"); err != nil {
		t.Fatal(err)
	}
	select {
	case call := <-calls:
		if call != [2]string{ps.threadID, "用户说明"} {
			t.Fatalf("贮藏参数 = %v", call)
		}
	case <-time.After(time.Second):
		t.Fatal("没有创建贮藏")
	}
	if err := tt.Click("贮藏"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-calls:
		t.Fatal("重复提交了贮藏")
	default:
	}
	d.closeStashDialog()
}

func TestGitStashDialogCloseDiscardsLateList(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	started := make(chan context.Context, 1)
	release, returned := make(chan struct{}), make(chan struct{})
	s.client = &gitStashPaneTestClient{gitPaneFakeClient: base, list: func(ctx context.Context) ([]git.StashEntry, error) {
		started <- ctx
		<-release
		defer close(returned)
		return []git.StashEntry{{Message: "迟到记录"}}, nil
	}}
	d := &gitStashDialog{s: s, threadID: ps.threadID}
	ps.stash = d
	d.openStashDialog()
	var ctx context.Context
	select {
	case ctx = <-started:
	case <-time.After(time.Second):
		t.Fatal("列表请求未开始")
	}
	ps.dispose()
	close(release)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("列表请求未结束")
	}
	d.mu.Lock()
	open, entries, loading := d.open, append([]git.StashEntry(nil), d.entries...), d.loading
	d.mu.Unlock()
	if ctx.Err() != context.Canceled || open || loading || len(entries) != 0 {
		t.Fatalf("关闭后状态被旧请求覆盖：%v %v %+v", open, loading, entries)
	}
}

type gitPaneCommitFailureClient struct {
	*gitChangesFakeClient
	started chan [2]string
	release chan struct{}
}

func (f *gitPaneCommitFailureClient) GitCommitIndex(ctx context.Context, threadID, message string, amend bool) (string, error) {
	f.started <- [2]string{threadID, message}
	select {
	case <-f.release:
		return "", errors.New("提交被拒绝")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestGitPaneCommitFailureKeepsThreadDraft(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	client := &gitPaneCommitFailureClient{gitChangesFakeClient: &gitChangesFakeClient{gitPaneFakeClient: base}, started: make(chan [2]string, 1), release: make(chan struct{})}
	s.client = client
	ps.state = GitState{WorkDir: t.TempDir()}
	ps.entries = []git.StatusEntry{{Path: "a.go", Index: 'M'}}
	st := gitChangesStateFor(ps)
	st.root, st.draft = ps.state.WorkDir, "原会话草稿"
	tt := ui.NewTester(func(c *ui.Context) { renderGitComposerForTest(c, s, ps) }, 600, 600)
	if err := tt.Click("提交 1 个文件"); err != nil {
		t.Fatal(err)
	}
	select {
	case call := <-client.started:
		if call != [2]string{ps.threadID, "原会话草稿"} {
			t.Fatalf("提交参数 = %v", call)
		}
	case <-time.After(time.Second):
		t.Fatal("提交未开始")
	}
	s.mu.Lock()
	s.selectedThreadID = "other"
	s.mu.Unlock()
	other := getGitPaneState(s)
	close(client.release)
	gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return ps.failure != nil })
	st.mu.Lock()
	draft := st.draft
	st.mu.Unlock()
	other.mu.Lock()
	failure := other.failure
	other.mu.Unlock()
	if draft != "原会话草稿" || failure != nil {
		t.Fatalf("提交失败丢失草稿或污染其他会话：%q，%+v", draft, failure)
	}
	base.mu.Lock()
	fullCommits := len(base.gitCommitMessages)
	base.mu.Unlock()
	if fullCommits != 0 {
		t.Fatal("索引提交失败退回了全量提交")
	}
}

func TestGitPaneFailureRecoveryButtons(t *testing.T) {
	for _, tc := range []struct {
		kind        git.FailureKind
		label, call string
	}{
		{git.FailureDiverged, "变基拉取", "pull:rebase"},
		{git.FailureDiverged, "合并拉取", "pull:merge"},
		{git.FailureNoUpstream, "发布", "push"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			_, ps, client := gitPaneTestState(t)
			ps.failure = &git.Failure{Kind: tc.kind, Title: "操作失败"}
			ps.syncing = true
			tt := ui.NewTester(func(c *ui.Context) { renderGitPaneFailure(c, ps) }, 600, 240)
			if node, ok := tt.Access(tc.label); !ok || !node.Disabled {
				t.Fatalf("同步时按钮未禁用：%+v", node)
			}
			ps.mu.Lock()
			ps.syncing = false
			ps.mu.Unlock()
			tt.Frame()
			if err := tt.Click(tc.label); err != nil {
				t.Fatal(err)
			}
			gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return !ps.syncing && !ps.refreshing })
			client.paneMu.Lock()
			defer client.paneMu.Unlock()
			if !slices.Equal(client.calls, []string{tc.call}) {
				t.Fatalf("恢复动作 = %v", client.calls)
			}
		})
	}
}

func TestGitPaneConflictOpensFirstFile(t *testing.T) {
	s, ps, _ := gitPaneTestState(t)
	ps.op = git.OpState{Kind: git.OpRebase, Conflicts: []git.ConflictFile{{Path: "first.go"}, {Path: "second.go"}}}
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneOpBanner(c, s, ps) }, 800, 120)
	if err := tt.Click("· 2 个冲突"); err != nil {
		t.Fatal(err)
	}
	dp := getDiffPanelState(s)
	diffPanelMu.Lock()
	scope, detail := dp.scope, dp.gitDetail
	diffPanelMu.Unlock()
	if !inspectorPaneOpen(s, sidePaneGitID) || scope != "all" || detail != "first.go" {
		t.Fatalf("冲突跳转：open=%v scope=%q detail=%q", inspectorPaneOpen(s, sidePaneGitID), scope, detail)
	}
}

func TestGitPaneOpBannerAIResolve(t *testing.T) {
	s, ps, fake := gitPaneTestState(t)
	ps.op = git.OpState{Kind: git.OpRebase, Conflicts: []git.ConflictFile{{Path: "first.go"}, {Path: "second.go"}}}
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneOpBanner(c, s, ps) }, 800, 120)
	if _, ok := tt.Access("交给 AI 解决"); !ok {
		t.Fatal("有冲突时缺少交给 AI 解决")
	}
	if err := tt.Click("交给 AI 解决"); err != nil {
		t.Fatal(err)
	}
	gitPaneWait(t, func() bool { return fake.SendCalls() > 0 })
	sent := fake.LastSentText()
	for _, want := range []string{"- first.go", "- second.go", "不要执行 merge --continue"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("草稿缺少 %q：%q", want, sent)
		}
	}
	ps.mu.Lock()
	ps.op.Conflicts = nil
	ps.mu.Unlock()
	tt.Frame()
	if _, ok := tt.Access("交给 AI 解决"); ok {
		t.Fatal("无冲突时不应显示交给 AI 解决")
	}
}

func TestGitPaneFetchThrottle(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, tc := range []struct {
		last time.Time
		want bool
	}{
		{time.Time{}, true}, {now.Add(-4 * time.Minute), false},
		{now.Add(-5 * time.Minute), true}, {now.Add(-6 * time.Minute), true}, {now.Add(time.Minute), false},
	} {
		if got := gitPaneFetchDue(tc.last, now); got != tc.want {
			t.Errorf("节流 %v = %v", tc.last, got)
		}
	}
}

func TestGitPaneBackgroundFetchLifecycle(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "成功", true: "失败静默"}[fail], func(t *testing.T) {
			s, ps, client := gitPaneTestState(t)
			root := t.TempDir()
			client.state = GitState{WorkDir: root, Branch: "main", Ahead: 2, Behind: 3}
			started, release := make(chan context.Context, 1), make(chan struct{})
			client.fetchHook = func(ctx context.Context) error {
				started <- ctx
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
				if fail {
					return errors.New("network is unreachable")
				}
				return nil
			}
			ps.setVisible(true)
			var ctx context.Context
			select {
			case ctx = <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("激活后没有抓取")
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 30*time.Second {
				t.Fatal("抓取没有 30 秒超时")
			}
			ps.mu.Lock()
			if ps.syncing || ps.failure != nil {
				t.Error("后台抓取改变了前台状态")
			}
			ps.mu.Unlock()
			var wg sync.WaitGroup
			for range 12 {
				wg.Add(1)
				go func() { defer wg.Done(); ps.backgroundFetch() }()
			}
			wg.Wait()
			client.paneMu.Lock()
			if !slices.Equal(client.calls, []string{"fetch"}) {
				t.Errorf("同仓库重复抓取：%v", client.calls)
			}
			client.paneMu.Unlock()
			close(release)
			gitPaneWait(t, func() bool { return ctx.Err() != nil })
			if !fail {
				hs := getChatHeaderState(s)
				gitPaneWait(t, func() bool { chatHeaderMu.Lock(); defer chatHeaderMu.Unlock(); return hs.git[ps.threadID].Behind == 3 })
			}
			ps.mu.Lock()
			defer ps.mu.Unlock()
			if ps.syncing || ps.failure != nil {
				t.Fatal("后台抓取结果改变了错误或同步状态")
			}
		})
	}
}

func TestGitPaneBackgroundFetchEligibilityAndCancellation(t *testing.T) {
	_, ps, client := gitPaneTestState(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ps.visible, ps.lifeCtx, ps.lifeCancel = true, ctx, cancel
	for _, state := range []GitState{{}, {WorkDir: t.TempDir(), Detached: true}} {
		ps.state = state
		ps.backgroundFetch()
	}
	ps.state = GitState{WorkDir: t.TempDir(), Branch: "main"}
	ps.syncing = true
	ps.backgroundFetch()
	client.paneMu.Lock()
	if len(client.calls) != 0 {
		t.Error("不满足条件仍抓取")
	}
	client.paneMu.Unlock()
	ps.syncing = false
	started := make(chan context.Context, 1)
	client.fetchHook = func(ctx context.Context) error { started <- ctx; <-ctx.Done(); return ctx.Err() }
	ps.backgroundFetch()
	var fetchCtx context.Context
	select {
	case fetchCtx = <-started:
	case <-time.After(time.Second):
		t.Fatal("抓取未开始")
	}
	ps.setVisible(false)
	gitPaneWait(t, func() bool { return fetchCtx.Err() == context.Canceled })
}

func TestGitPaneSwitchStashMatch(t *testing.T) {
	entries := []git.StashEntry{
		{Index: 0, Branch: "other", Message: gitSwitchStashMarker},
		{Index: 1, Branch: "main", Message: "普通贮藏"},
		{Index: 4, Branch: "main", Message: gitSwitchStashMarker},
		{Index: 2, Branch: "main", Message: "prefix " + gitSwitchStashMarker},
		{Index: 3, Branch: "main", Message: gitSwitchStashMarker + " suffix"},
	}
	if got := gitPaneSwitchStash(entries, "main"); got == nil || got.Index != 2 {
		t.Fatalf("最新贮藏 = %+v", got)
	}
	for _, branch := range []string{"", "missing"} {
		if gitPaneSwitchStash(entries, branch) != nil {
			t.Errorf("误匹配分支 %q", branch)
		}
	}
	if gitPaneSwitchStash(nil, "main") != nil {
		t.Fatal("空列表误匹配")
	}
}

func TestGitPaneSwitchStashRefreshAndRestore(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "恢复成功", true: "冲突横幅"}[fail], func(t *testing.T) {
			s, ps, base := gitPaneTestState(t)
			base.state = GitState{WorkDir: t.TempDir(), Branch: "main"}
			var mu sync.Mutex
			lists, popped := 0, -1
			s.client = &gitStashPaneTestClient{gitPaneFakeClient: base,
				list: func(context.Context) ([]git.StashEntry, error) {
					mu.Lock()
					defer mu.Unlock()
					lists++
					return []git.StashEntry{{Index: lists, Branch: "main", Message: gitSwitchStashMarker}}, nil
				},
				pop: func(_ context.Context, index int) error {
					mu.Lock()
					popped = index
					mu.Unlock()
					if fail {
						return errors.New("unmerged files")
					}
					base.paneMu.Lock()
					base.state.HasStash = false
					base.paneMu.Unlock()
					return nil
				},
			}
			ps.requestRefresh()
			gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return !ps.refreshing })
			mu.Lock()
			count := lists
			mu.Unlock()
			if count != 0 {
				t.Fatal("没有贮藏时读取列表")
			}
			base.paneMu.Lock()
			base.state.HasStash = true
			base.paneMu.Unlock()
			ps.requestRefresh()
			gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return !ps.refreshing && ps.switchStash != nil })
			tt := ui.NewTester(func(c *ui.Context) { renderGitPaneSwitchStash(c, ps) }, 600, 100)
			if !tt.HasText("有切换分支前的改动") {
				t.Fatal("缺少恢复横幅")
			}
			if err := tt.Click("恢复"); err != nil {
				t.Fatal(err)
			}
			gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return !ps.syncing && !ps.refreshing })
			mu.Lock()
			index := popped
			mu.Unlock()
			if index != 2 {
				t.Fatalf("没有重新读取最新序号：%d", index)
			}
			ps.mu.Lock()
			failure, entry := ps.failure, ps.switchStash
			ps.mu.Unlock()
			if fail {
				if failure == nil || failure.Kind != git.FailureConflict {
					t.Fatalf("失败分类 = %+v", failure)
				}
			} else if entry != nil || failure != nil {
				t.Fatal("恢复后没有刷新横幅")
			}
		})
	}
}

func TestGitPaneHasUnmergedBees(t *testing.T) {
	for _, tc := range []struct {
		state SwarmState
		want  bool
	}{
		{SwarmState{LeadThreadID: "lead", Bees: []SwarmBee{{Status: "completed"}}}, true},
		{SwarmState{LeadThreadID: "other", Bees: []SwarmBee{{Status: "completed"}}}, false},
		{SwarmState{LeadThreadID: "lead", IsBee: true, Bees: []SwarmBee{{Status: "completed"}}}, false},
		{SwarmState{LeadThreadID: "lead", Bees: []SwarmBee{{Status: "completed", Merged: true}, {Status: "running"}}}, false},
		{SwarmState{LeadThreadID: "lead"}, false},
	} {
		if got := gitPaneHasUnmergedBees("lead", tc.state); got != tc.want {
			t.Errorf("合并入口判定 = %v，状态 %+v", got, tc.state)
		}
	}
	if gitPaneHasUnmergedBees("", SwarmState{Bees: []SwarmBee{{Status: "completed"}}}) {
		t.Fatal("空会话误匹配")
	}
}

func TestGitPaneHeaderSingleRefreshAndMore(t *testing.T) {
	setAppReduceMotion(t, true)
	_, ps, h, client, _, start := gitAllTimelineSetup(t, 480, 600)
	client.state.HasUpstream = true
	client.log = []git.LogCommit{{SHA: "sha-1", Subject: "初始提交", AuthorTime: time.Now()}}
	client.entries = []git.StatusEntry{{Path: "a.go", Worktree: 'M'}}
	ps.mu.Lock()
	ps.state.HasUpstream = true
	ps.entries = slices.Clone(client.entries)
	ps.mu.Unlock()
	client.SetReviewSnapshot("git-test", "uncommitted", "", "", ReviewSnapshot{
		Scope:        "uncommitted",
		HeadRevision: "work",
		Files:        []ReviewFile{{Path: "a.go", Status: "M", Additions: 1}},
	})

	tt := start()
	gitHistoryWaitLoaded(t, h)
	waitFor(t, tt, func() bool { return tt.HasText("a.go") && tt.HasText("初始提交") })

	for _, forbidden := range []string{"diff-refresh-btn", "diff-more-btn", "git-all-wip-more", "改动更多操作", "未提交操作", "刷新改动"} {
		if _, ok := tt.Find(forbidden); ok {
			t.Fatalf("Git 面板不应再存在旧控件 %q", forbidden)
		}
	}
	refreshCount, moreCount := 0, 0
	for _, a := range tt.AccessAll() {
		if a.Role == ui.RoleButton && a.Name == "刷新" {
			refreshCount++
		}
		if a.Role == ui.RoleButton && a.Name == "Git 更多操作" {
			moreCount++
		}
	}
	if refreshCount != 1 || moreCount != 1 {
		t.Fatalf("应只有一个刷新/同步按钮和一个 ⋯：refresh=%d more=%d", refreshCount, moreCount)
	}

	// !hasGit && modern 时第 2 行继续使用 renderDiffHeaderActions。
	noGitFake := NewFakeClient()
	noGitFake.SetReviewSnapshot("t1", "uncommitted", "", "", ReviewSnapshot{
		Scope:        "uncommitted",
		HeadRevision: "h1",
		Files:        []ReviewFile{{Path: "x.go", Status: "M"}},
	})
	noGitState := newChatState(noGitFake)
	t.Cleanup(noGitState.close)
	noGitState.mu.Lock()
	noGitState.selectedThreadID = "t1"
	noGitState.mu.Unlock()
	noGitDP := getDiffPanelState(noGitState)
	diffPanelMu.Lock()
	noGitDP.scope = "all"
	diffPanelMu.Unlock()
	noGitState.loadDiff("t1", "")
	ttNoGit := ui.NewTester(func(c *ui.Context) { renderGitPane(c, noGitState) }, 480, 400)
	waitFor(t, ttNoGit, func() bool { return ttNoGit.HasText("x.go") })
	if _, ok := ttNoGit.Find("Git 更多操作"); ok {
		t.Fatal("无 Git 能力时不应渲染第 1 行")
	}
	if _, ok := ttNoGit.Access("改动更多操作"); !ok {
		t.Fatal("无 Git 能力但为 modern 客户端时第 2 行应保留 renderDiffHeaderActions")
	}
}

func TestGitPaneHeaderUnifiedMoreMenu(t *testing.T) {
	setAppReduceMotion(t, true)
	s, ps, h, client, dp, _ := gitAllTimelineSetup(t, 760, 720)
	client.state.RemoteURL = "git@github.com:owner/repo.git"
	client.log = []git.LogCommit{{SHA: "sha-1", Subject: "初始提交", AuthorTime: time.Now()}}
	client.entries = []git.StatusEntry{{Path: "a.go", Worktree: 'M'}}
	ps.mu.Lock()
	ps.state = client.state
	ps.entries = slices.Clone(client.entries)
	ps.mu.Unlock()
	client.SetReviewSnapshot("git-test", "uncommitted", "", "", ReviewSnapshot{
		Scope:        "uncommitted",
		HeadRevision: "work",
		Files:        []ReviewFile{{Path: "a.go", Status: "M"}},
	})
	client.SetReviewSnapshot("git-test", "turn", "", "", ReviewSnapshot{
		Scope:        "turn",
		HeadRevision: "turn-h",
		Files:        []ReviewFile{{Path: "a.go", Status: "M"}},
	})
	s.mu.Lock()
	s.messages = []Message{
		{ID: "m1", TurnID: "turn-1", TurnIndex: 0},
		{ID: "m2", TurnID: "turn-2", TurnIndex: 1},
	}
	s.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) { renderGitPane(c, s) }, 760, 720)
	gitHistoryWaitLoaded(t, h)
	waitFor(t, tt, func() bool { return tt.HasText("a.go") })

	if err := tt.Click("Git 更多操作"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, want := range []string{
		"刷新改动", "抓取", "拉取", "强制推送…",
		"并排显示", "全部展开", "全部收起",
		"全部暂存", "全部取消暂存", "贮藏…", "全部撤销…",
		"创建 PR…", "查看 Pull Request…", "打开仓库", "远端…",
	} {
		if _, ok := tt.Access(want); !ok {
			t.Fatalf("⋯ 浮层缺少菜单项 %q，texts=%v", want, tt.Texts())
		}
	}
	if _, ok := tt.Access("轮次"); ok {
		t.Fatal("all 范围不应显示轮次")
	}

	// 切到 turn 范围时显示「轮次」，且不显示仅 all 可用的批量暂存/撤销项。
	s.selectReviewScope(dp, "turn")
	waitFor(t, tt, func() bool {
		diffPanelMu.Lock()
		defer diffPanelMu.Unlock()
		return !dp.loading && dp.scope == "turn"
	})
	tt.Frame()
	if _, ok := tt.Access("全部暂存"); ok {
		t.Fatal("turn 范围不应显示全部暂存")
	}
	if _, ok := tt.Access("轮次"); !ok {
		t.Fatal("turn 范围且多轮时应显示轮次")
	}
	if err := tt.Click("轮次"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Access("最新一轮"); !ok {
		t.Fatal("展开轮次后缺少「最新一轮」")
	}
	if _, ok := tt.Access("第 2 轮"); !ok {
		t.Fatal("展开轮次后缺少「第 2 轮」")
	}
}

type gitHeaderStopClient struct {
	*gitTabMergedFakeClient
	diffStarted chan struct{}
	diffRelease chan struct{}
	diffDone    chan struct{}
}

func (f *gitHeaderStopClient) ReviewGet(ctx context.Context, tid, scope, turn, path string) (ReviewSnapshot, error) {
	if scope == "uncommitted" && f.diffStarted != nil {
		close(f.diffStarted)
		<-f.diffRelease
		defer close(f.diffDone)
		return ReviewSnapshot{
			Scope:        "uncommitted",
			HeadRevision: "late",
			Files:        []ReviewFile{{Path: "late.go", Status: "M"}},
		}, nil
	}
	return f.gitTabMergedFakeClient.ReviewGet(ctx, tid, scope, turn, path)
}

func TestGitPaneHeaderStopCancelsAll(t *testing.T) {
	setAppReduceMotion(t, true)
	s, ps, _, base, dp, _ := gitAllTimelineSetup(t, 600, 480)
	client := &gitHeaderStopClient{
		gitTabMergedFakeClient: base,
		diffStarted:            make(chan struct{}),
		diffRelease:            make(chan struct{}),
		diffDone:               make(chan struct{}),
	}
	s.client = client

	pullStarted := make(chan context.Context, 1)
	refreshStarted := make(chan context.Context, 1)
	var pullOnce, refreshOnce sync.Once
	base.pullHook = func(ctx context.Context) error {
		pullOnce.Do(func() { pullStarted <- ctx })
		<-ctx.Done()
		return ctx.Err()
	}
	base.stateHook = func(ctx context.Context) (GitState, error) {
		refreshOnce.Do(func() { refreshStarted <- ctx })
		<-ctx.Done()
		return GitState{}, ctx.Err()
	}

	diffPanelMu.Lock()
	dp.threadID = ps.threadID
	dp.requestGen = 1
	diffPanelMu.Unlock()

	s.loadDiff(ps.threadID, "")
	select {
	case <-client.diffStarted:
	case <-time.After(time.Second):
		t.Fatal("diff 请求未开始")
	}
	ps.run("sync")
	var pullCtx context.Context
	select {
	case pullCtx = <-pullStarted:
	case <-time.After(time.Second):
		t.Fatal("sync 未开始")
	}
	ps.requestRefresh()
	var refreshCtx context.Context
	select {
	case refreshCtx = <-refreshStarted:
	case <-time.After(time.Second):
		t.Fatal("状态刷新未开始")
	}

	tt := ui.NewTester(func(c *ui.Context) { renderGitPane(c, s) }, 600, 480)
	tt.Frame()
	if err := tt.Click("停止"); err != nil {
		t.Fatalf("点击停止失败: %v", err)
	}

	gitPaneWait(t, func() bool { return pullCtx.Err() == context.Canceled && refreshCtx.Err() == context.Canceled })
	diffPanelMu.Lock()
	loadingAfterStop := dp.loading
	diffPanelMu.Unlock()
	if loadingAfterStop {
		t.Fatal("点击停止后 dp.loading 未复位为 false")
	}

	close(client.diffRelease)
	select {
	case <-client.diffDone:
	case <-time.After(time.Second):
		t.Fatal("迟到的 diff 请求未结束")
	}
	tt.Frame()

	diffPanelMu.Lock()
	loadingFinal := dp.loading
	files := append([]ReviewFile(nil), dp.snapshot.Files...)
	diffPanelMu.Unlock()
	if loadingFinal {
		t.Fatal("迟到的 diff 回包不应重新把 dp.loading 设为 true")
	}
	for _, f := range files {
		if f.Path == "late.go" {
			t.Fatal("迟到的 diff 结果未被丢弃")
		}
	}
}

func TestGitPaneHeaderDetachedOnlyRefreshesLocally(t *testing.T) {
	setAppReduceMotion(t, true)
	s, ps, h, client, _, start := gitAllTimelineSetup(t, 480, 500)
	client.state = GitState{WorkDir: client.state.WorkDir, Detached: true, Head: "0123456789abcdef", HasUpstream: true, Ahead: 2}
	client.SetReviewSnapshot("git-test", "uncommitted", "", "", ReviewSnapshot{Scope: "uncommitted", HeadRevision: "h1"})
	ps.mu.Lock()
	ps.state = client.state
	ps.mu.Unlock()

	tt := start()
	gitHistoryWaitLoaded(t, h)
	waitFor(t, tt, func() bool { return tt.HasText("0123456") })

	node, ok := tt.Access("刷新")
	if !ok || node.Disabled || node.Description != "刷新" {
		t.Fatalf("分离 HEAD 刷新按钮状态错误: %+v ok=%v", node, ok)
	}
	beforeReview := len(client.ReviewGetCalls())
	stateCalls := 0
	var stateMu sync.Mutex
	client.stateHook = func(context.Context) (GitState, error) {
		stateMu.Lock()
		stateCalls++
		stateMu.Unlock()
		return client.state, nil
	}
	if err := tt.Click("刷新"); err != nil {
		t.Fatal(err)
	}
	gitPaneWait(t, func() bool {
		stateMu.Lock()
		defer stateMu.Unlock()
		return stateCalls > 0 && len(client.ReviewGetCalls()) > beforeReview
	})
	gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return !ps.refreshing })
	client.paneMu.Lock()
	calls := append([]string(nil), client.calls...)
	client.paneMu.Unlock()
	if slices.Contains(calls, "fetch") {
		t.Fatalf("分离 HEAD 点击刷新不应触发 fetch：%v", calls)
	}

	// 无工作目录时禁用。
	ps.mu.Lock()
	ps.state = GitState{}
	ps.mu.Unlock()
	tt.Frame()
	if node, ok := tt.Access("刷新"); !ok || !node.Disabled {
		t.Fatalf("无工作目录时应禁用刷新按钮: %+v ok=%v", node, ok)
	}
	_ = s
}

func TestGitPaneHeaderSearchAndBranchFilterOnlyInAllScope(t *testing.T) {
	setAppReduceMotion(t, true)
	s, ps, h, client, dp, start := gitAllTimelineSetup(t, 480, 600)
	client.log = []git.LogCommit{{SHA: "sha-1", Subject: "初始提交", AuthorTime: time.Now()}}
	client.SetReviewSnapshot("git-test", "uncommitted", "", "", ReviewSnapshot{Scope: "uncommitted", HeadRevision: "h1"})
	client.SetReviewSnapshot("git-test", "turn", "", "", ReviewSnapshot{Scope: "turn", HeadRevision: "t1"})
	client.SetReviewSnapshot("git-test", "session", "", "", ReviewSnapshot{Scope: "session", HeadRevision: "s1"})

	tt := start()
	gitHistoryWaitLoaded(t, h)
	tt.Frame()

	if _, ok := tt.Find("git-history-filters"); ok {
		t.Fatal("不应再存在第 3 层独立的 git-history-filters")
	}
	allRect, okAll := tt.Find("全部")
	searchRect, okSearch := tt.Find("搜索提交")
	branchRect, okBranch := tt.Find("当前分支")
	if !okAll || !okSearch || !okBranch {
		t.Fatalf("all 范围第 2 行缺少分段或历史筛选控件: all=%v search=%v branch=%v", okAll, okSearch, okBranch)
	}
	if !(allRect.Y < searchRect.Y+searchRect.H && searchRect.Y < allRect.Y+allRect.H && allRect.Y < branchRect.Y+branchRect.H && branchRect.Y < allRect.Y+allRect.H) {
		t.Fatalf("搜索与分支筛选应与分段控件在同一行: all=%+v search=%+v branch=%+v", allRect, searchRect, branchRect)
	}

	// 点 🔍 后整行换成 SearchField + 当前分支，按 Esc 恢复分段控件。
	if err := tt.Click("搜索提交"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("全部"); ok {
		t.Fatal("展开搜索后分段控件应暂时隐藏")
	}
	if _, ok := tt.Find("当前分支"); !ok {
		t.Fatal("展开搜索后「当前分支」应保留在右侧")
	}
	tt.Type("初始")
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if _, ok := tt.Find("全部"); !ok {
		t.Fatal("按 Esc 后应恢复分段控件")
	}

	// 窄宽下带文件过滤 chip 时不溢出。
	openGitFileHistory(ps, "very/long/nested/directory/some_very_long_component_filename.go")
	tt.SetSize(360, 600)
	tt.Frame()
	chipRect, okChip := tt.Find("清除文件过滤")
	scopeRect, okScope := tt.Find("当前分支")
	if !okChip || !okScope {
		t.Fatalf("窄宽下缺少文件过滤 chip 或分支筛选: chip=%v scope=%v", okChip, okScope)
	}
	if chipRect.X+chipRect.W > 360 || scopeRect.X+scopeRect.W > 360 {
		t.Fatalf("窄宽下第 2 行溢出 360: chip=%+v scope=%+v", chipRect, scopeRect)
	}

	// turn 与 session 范围不显示搜索和分支筛选。
	for _, target := range []string{"turn", "session"} {
		s.selectReviewScope(dp, target)
		tt.Frame()
		if _, ok := tt.Find("搜索提交"); ok {
			t.Fatalf("%s 范围不应显示搜索提交", target)
		}
		if _, ok := tt.Find("当前分支"); ok {
			t.Fatalf("%s 范围不应显示当前分支筛选", target)
		}
	}
}

func TestGitPaneHeaderCommentsStripAnimatesAndSettles(t *testing.T) {
	for _, reduced := range []bool{false, true} {
		t.Run(fmt.Sprintf("reduce=%v", reduced), func(t *testing.T) {
			setAppReduceMotion(t, reduced)
			s, ps, h, client, dp, _ := gitAllTimelineSetup(t, 480, 600)
			client.log = []git.LogCommit{{SHA: "sha-1", Subject: "初始提交", AuthorTime: time.Now()}}
			client.SetReviewSnapshot("git-test", "uncommitted", "", "", ReviewSnapshot{Scope: "uncommitted", HeadRevision: "h1"})

			builds := 0
			tt := ui.NewTesterWith(func(c *ui.Context) {
				builds++
				renderGitPane(c, s)
			}, 480, 600, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduced}})
			gitHistoryWaitLoaded(t, h)
			tt.Frame()
			tt.Advance(2 * DurSlow)
			tt.Frame()
			if tt.FramePending() {
				t.Fatal("初始静止时不应继续请求下一帧")
			}

			// 添加未附着批注，触发批注提示条展开。
			diffPanelMu.Lock()
			dp.comments = append(dp.comments, &reviewComment{
				id:   1,
				key:  reviewDiffKey{threadID: ps.threadID, workspace: client.state.WorkDir, scope: "all", head: "h1", path: "a.go"},
				body: "需要调整",
			})
			diffPanelMu.Unlock()
			tt.Frame()
			if !tt.HasText("添加到对话 1") {
				t.Fatalf("有未添加批注时应显示提示条，texts=%v", tt.Texts())
			}

			tt.Advance(2 * DurSlow)
			tt.Frame()
			if tt.FramePending() {
				t.Fatal("批注提示条展开动画结束后仍在请求下一帧")
			}
			settled := builds
			tt.Advance(time.Second)
			if tt.FramePending() || builds != settled {
				t.Fatalf("静止后不应重绘: pending=%v builds=%d->%d", tt.FramePending(), settled, builds)
			}

			// 移除批注，收起动画结束后同样静止。
			diffPanelMu.Lock()
			dp.comments = nil
			diffPanelMu.Unlock()
			tt.Frame()
			tt.Advance(2 * DurSlow)
			tt.Frame()
			if tt.HasText("添加到对话 1") {
				t.Fatal("清空批注后提示条应消失")
			}
			if tt.FramePending() {
				t.Fatal("批注提示条收起动画结束后仍在请求下一帧")
			}
		})
	}
}
