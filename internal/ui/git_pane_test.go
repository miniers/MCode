package mcodeui

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
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
	t.Setenv("HOME", t.TempDir())
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
			if node, ok := tt.Access(tc.label); !ok || node.Disabled {
				t.Fatalf("同步按钮 = %+v，%v", node, ok)
			}
			if tc.action != "fetch" && !tt.HasText(tc.label) {
				t.Fatalf("缺少同步文案 %s", tc.label)
			}
			if err := tt.Click(tc.label); err != nil {
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
	s, ps, _ := gitPaneTestState(t)
	state := GitState{WorkDir: "/repo", Detached: true, Head: "0123456789", HasUpstream: true, Ahead: 1}
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneToolbar(c, s, ps, state, 350) }, 350, 100)
	if node, ok := tt.Access("推送 ↑1"); !ok || !node.Disabled || node.Description != "分离 HEAD 时无法同步" {
		t.Fatalf("分离 HEAD 按钮 = %+v，%v", node, ok)
	}
	labels := 0
	for _, text := range tt.Texts() {
		if text == "推送 ↑1" {
			labels++
		}
	}
	if labels != 1 || !tt.HasText("0123456") {
		t.Fatalf("窄面板文案或短哈希错误：%q", tt.Texts())
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
	_, ps, client := gitPaneTestState(t)
	ps.showUndo("已丢弃改动", &git.Snapshot{})
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneUndo(c, ps) }, 400, 80)
	if !tt.HasText("撤销") {
		t.Fatal("缺少撤销入口")
	}
	if err := tt.Click("撤销"); err != nil {
		t.Fatal(err)
	}
	gitPaneWait(t, func() bool { ps.mu.Lock(); defer ps.mu.Unlock(); return !ps.syncing && ps.undoSnap == nil })
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
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneChanges(c, s, ps.threadID, ps) }, 600, 600)
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
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneChanges(c, s, ps.threadID, ps) }, 600, 600)
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
	ps.segment = 1
	ps.op = git.OpState{Kind: git.OpRebase, Conflicts: []git.ConflictFile{{Path: "first.go"}, {Path: "second.go"}}}
	st := gitChangesStateFor(ps)
	st.selected, st.staged, st.group = "old.go", true, 2
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneOpBanner(c, s, ps) }, 800, 120)
	if err := tt.Click("· 2 个冲突"); err != nil {
		t.Fatal(err)
	}
	if ps.segment != 0 || st.selected != "first.go" || st.staged || st.group != 0 || !st.detail {
		t.Fatalf("冲突跳转状态 = %d/%+v", ps.segment, st)
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
