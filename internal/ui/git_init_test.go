package mcodeui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/git"
	"mcode/internal/testenv"
)

func TestChatHeaderGitCapsuleSplit(t *testing.T) {
	testenv.TempHome(t)
	stubTerminals(t)

	fake := newGitFakeClient()
	fake.gitState.Ahead = 2
	fake.gitState.Behind = 1
	s, root := buildRoot(fake)
	t.Cleanup(func() {
		closeTerminals(s)
		s.close()
	})

	tt := ui.NewTester(root, 1200, 700)
	waitFor(t, tt, func() bool {
		_, hasPaneBtn := tt.Find("Git 面板")
		_, hasBranchBtn := tt.Find("切换分支")
		return hasPaneBtn && hasBranchBtn && tt.HasText("feature/login")
	})

	if !tt.HasText("↑2") || !tt.HasText("↓1") {
		t.Fatalf("胶囊左半缺少领先/落后标识: texts=%q", tt.Texts())
	}

	if err := tt.Click("Git 面板"); err != nil {
		t.Fatalf("点击胶囊左半失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		st := inspectorSession(s)
		return st != nil && st.Open && st.Active == sidePaneGitID
	})
	if _, ok := tt.Find("分支列表"); ok {
		t.Fatal("点击胶囊左半不应打开分支浮层")
	}

	if err := tt.Click("Git 面板"); err != nil {
		t.Fatalf("再次点击胶囊左半失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		st := inspectorSession(s)
		return st != nil && !st.Open
	})

	if err := tt.Click("切换分支"); err != nil {
		t.Fatalf("点击胶囊右半失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("分支列表")
		return ok
	})
	if st := inspectorSession(s); st != nil && st.Open {
		t.Fatalf("点击胶囊右半不应改变右栏关闭状态: open=%v active=%q", st.Open, st.Active)
	}
}

func TestChatHeaderGitInit(t *testing.T) {
	testenv.TempHome(t)
	stubTerminals(t)

	oldRunOnMain := gitInitRunOnMain
	var runOnMainCalls int
	gitInitRunOnMain = func(fn func()) {
		runOnMainCalls++
		fn()
	}
	t.Cleanup(func() { gitInitRunOnMain = oldRunOnMain })

	fake := NewFakeClient()
	fake.SetThreadWorkDir("/repo")
	fake.mu.Lock()
	fake.gitErr = git.ErrNotRepo
	fake.mu.Unlock()

	started := make(chan struct{})
	release := make(chan struct{})
	var closeOnce sync.Once
	releaseInit := func() { closeOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseInit)

	fake.mu.Lock()
	fake.gitInitHook = func(context.Context, string) error {
		close(started)
		<-release
		return nil
	}
	fake.mu.Unlock()

	s, root := buildRoot(fake)
	t.Cleanup(func() {
		closeTerminals(s)
		s.close()
	})

	tt := ui.NewTester(root, 1200, 700)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("初始化 Git")
		return ok
	})

	if err := tt.Click("初始化 Git"); err != nil {
		t.Fatalf("点击「初始化 Git」失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("初始化")
		return ok && tt.HasText("初始化 Git 仓库")
	})
	if calls := fake.GitInitCalls(); len(calls) != 0 {
		t.Fatalf("确认前不应调用 GitInit: %v", calls)
	}

	if err := tt.Click("初始化"); err != nil {
		t.Fatalf("点击确认「初始化」失败: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("GitInit 未在后台执行")
	}

	waitFor(t, tt, func() bool {
		node, ok := tt.Access("初始化 Git")
		return ok && node.Disabled && tt.HasText("正在初始化…")
	})

	fake.mu.Lock()
	fake.gitErr = nil
	fake.gitState = GitState{
		WorkDir:  "/repo",
		Branch:   "main",
		Head:     "abc1234",
		Branches: []string{"main"},
		Worktrees: []GitWorktree{
			{Path: "/repo", Branch: "main", Current: true},
		},
	}
	fake.mu.Unlock()
	releaseInit()

	waitFor(t, tt, func() bool {
		_, hasCapsule := tt.Find("Git 面板")
		st := inspectorSession(s)
		return hasCapsule && tt.HasText("main") && st != nil && st.Open && st.Active == sidePaneGitID
	})

	if calls := fake.GitInitCalls(); len(calls) != 1 || calls[0] != "t1" {
		t.Fatalf("GitInit 调用记录 = %v, want [t1]", calls)
	}
	if runOnMainCalls != 1 {
		t.Fatalf("gitInitRunOnMain 调用次数 = %d, want 1", runOnMainCalls)
	}

	tt.Advance(time.Second)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("Git 初始化完成且动画结束后不应再请求下一帧")
	}
}

func TestGitPaneEmptyInit(t *testing.T) {
	oldRunOnMain := gitInitRunOnMain
	gitInitRunOnMain = func(fn func()) { fn() }
	t.Cleanup(func() { gitInitRunOnMain = oldRunOnMain })

	s, ps, client := gitPaneTestState(t)
	client.paneMu.Lock()
	client.stateHook = func(context.Context) (GitState, error) {
		return GitState{}, git.ErrNotRepo
	}
	client.paneMu.Unlock()
	ps.mu.Lock()
	ps.visible = true
	ps.state = GitState{}
	ps.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) { renderGitPane(c, s) }, 480, 360)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("初始化 Git")
		return ok && tt.HasText("不是 Git 仓库")
	})

	if err := tt.Click("初始化 Git"); err != nil {
		t.Fatalf("点击空态「初始化 Git」失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		calls := client.GitInitCalls()
		return len(calls) == 1 && calls[0] == "git-test"
	})

	// 失败时应展示错误 toast 且复位进行中状态。
	client.mu.Lock()
	client.gitInitErr = errors.New("permission denied")
	client.mu.Unlock()
	waitFor(t, tt, func() bool {
		return !isGitInitBusy(s, "git-test")
	})
	if err := tt.Click("初始化 Git"); err != nil {
		t.Fatalf("再次点击空态「初始化 Git」失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		calls := client.GitInitCalls()
		s.mu.Lock()
		toast := s.toastMessage
		s.mu.Unlock()
		return len(calls) == 2 && toast == "初始化 Git 失败: permission denied"
	})
}
