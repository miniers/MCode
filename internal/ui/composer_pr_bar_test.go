package mcodeui

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
	"mcode/internal/testenv"
)

type composerPRMergeCall struct {
	threadID string
	number   int
	method   string
	from     string
}

type composerPRFlowFake struct {
	*FakeClient

	mu        sync.Mutex
	stateByID map[string]git.PRThreadState
	okByID    map[string]bool
	errByID   map[string]error

	stateCalls atomic.Int32

	mergeCalls []composerPRMergeCall
	mergeFn    func(ctx context.Context, threadID string, number int, method, from string, onStep func(step string)) (string, error)
}

func newComposerPRFlowFake() *composerPRFlowFake {
	return &composerPRFlowFake{
		FakeClient: NewFakeClient(),
		stateByID:  make(map[string]git.PRThreadState),
		okByID:     make(map[string]bool),
		errByID:    make(map[string]error),
	}
}

func (f *composerPRFlowFake) setPRState(threadID string, st git.PRThreadState, ok bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stateByID[threadID] = st
	f.okByID[threadID] = ok
	if err != nil {
		f.errByID[threadID] = err
	} else {
		delete(f.errByID, threadID)
	}
}

func (f *composerPRFlowFake) GitPRThreadState(_ context.Context, threadID string) (git.PRThreadState, bool, error) {
	f.stateCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errByID[threadID]; err != nil {
		return git.PRThreadState{}, false, err
	}
	if !f.okByID[threadID] {
		return git.PRThreadState{}, false, nil
	}
	return f.stateByID[threadID], true, nil
}

func (f *composerPRFlowFake) GitPRMergeFlow(ctx context.Context, threadID string, number int, method, from string, onStep func(step string)) (string, error) {
	f.mu.Lock()
	f.mergeCalls = append(f.mergeCalls, composerPRMergeCall{
		threadID: threadID,
		number:   number,
		method:   method,
		from:     from,
	})
	fn := f.mergeFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, threadID, number, method, from, onStep)
	}
	for _, step := range composerPRMergeSteps {
		if onStep != nil {
			onStep(step.key)
		}
	}
	return "", nil
}

func (f *composerPRFlowFake) recordedMergeCalls() []composerPRMergeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]composerPRMergeCall, len(f.mergeCalls))
	copy(out, f.mergeCalls)
	return out
}

func composerPRTestHarness(t *testing.T, client Client, thread Thread, reduce bool) (*chatState, *ui.Tester) {
	t.Helper()
	testenv.TempHome(t)
	setAppReduceMotion(t, reduce)
	resetArchivePageStateForTest()

	s := newChatState(client)
	t.Cleanup(s.close)
	s.mu.Lock()
	s.projects = []Project{{ID: "p1", Name: "项目1", Path: "/repo"}}
	if thread.ID != "" {
		if thread.ProjectID == "" {
			thread.ProjectID = "p1"
		}
		s.threads = []Thread{thread}
		s.threadsByProj = map[string][]Thread{thread.ProjectID: {thread}}
		s.selectedThreadID = thread.ID
	}
	s.mu.Unlock()

	tt := ui.NewTesterWith(func(c *ui.Context) {
		s.setUIServices(c.Services())
		ui.Column(c.Key("composer-pr-test-root")).Fill().Justify(ui.End).Children(func() {
			renderComposer(c, s)
		})
	}, 900, 600, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduce}})
	tt.Frame()
	return s, tt
}

func waitComposerPRLoaded(t *testing.T, tt *ui.Tester, s *chatState, threadID string) {
	t.Helper()
	waitFor(t, tt, func() bool {
		composerPRBarMu.Lock()
		st := composerPRBarStates[s]
		var done bool
		if st != nil && st.threads[threadID] != nil {
			done = st.threads[threadID].loadedOnce && !st.threads[threadID].loading
		}
		composerPRBarMu.Unlock()
		return done
	})
	tt.Frame()
}

func TestComposerPRBarNonPRHidden(t *testing.T) {
	t.Run("无接口不显示", func(t *testing.T) {
		_, tt := composerPRTestHarness(t, NewFakeClient(), Thread{ID: "t1", Title: "普通会话"}, true)
		if tt.HasText("合并") || tt.HasText("推送并合并") || tt.HasText("#") {
			t.Fatalf("未实现 gitPRFlowClient 时不应显示 PR 条: %v", tt.Texts())
		}
		if tt.FramePending() {
			t.Fatal("非 PR 会话不应请求下一帧")
		}
	})

	t.Run("非PR分支不显示", func(t *testing.T) {
		fake := newComposerPRFlowFake()
		fake.setPRState("t1", git.PRThreadState{}, false, nil)
		s, tt := composerPRTestHarness(t, fake, Thread{ID: "t1", Title: "普通会话"}, true)
		waitComposerPRLoaded(t, tt, s, "t1")
		if tt.HasText("合并") || tt.HasText("推送并合并") {
			t.Fatalf("非 PR 分支不应显示 PR 条: %v", tt.Texts())
		}
		if tt.FramePending() {
			t.Fatal("非 PR 分支不应请求下一帧")
		}
	})

	t.Run("嵌入子代理输入框不显示", func(t *testing.T) {
		fake := newComposerPRFlowFake()
		fake.setPRState("bee-1", git.PRThreadState{Number: 31, LocalCommits: 1}, true, nil)
		testenv.TempHome(t)
		setAppReduceMotion(t, true)
		s := newChatState(fake)
		t.Cleanup(s.close)
		s.mu.Lock()
		s.embeddedBee = true
		s.threads = []Thread{{ID: "bee-1", ProjectID: "p1", IsHelper: true}}
		s.selectedThreadID = "bee-1"
		s.mu.Unlock()
		tt := ui.NewTesterWith(func(c *ui.Context) {
			s.setUIServices(c.Services())
			ui.Column(c.Key("bee-root")).Fill().Children(func() {
				renderComposer(c, s)
			})
		}, 600, 400, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
		tt.Frame()
		tt.Advance(50 * time.Millisecond)
		if tt.HasText("#31") || tt.HasText("推送并合并") || tt.HasText("合并") {
			t.Fatalf("嵌入子代理输入框不应显示 PR 条: %v", tt.Texts())
		}
		if fake.stateCalls.Load() != 0 {
			t.Fatalf("嵌入子代理输入框不应拉取 PR 状态，got %d", fake.stateCalls.Load())
		}
	})
}

func TestComposerPRBarDirtyDisabled(t *testing.T) {
	fake := newComposerPRFlowFake()
	fake.setPRState("t1", git.PRThreadState{
		Number:       31,
		Dirty:        true,
		LocalCommits: 2,
	}, true, nil)

	s, tt := composerPRTestHarness(t, fake, Thread{ID: "t1", Title: "审查 PR #31"}, true)
	waitComposerPRLoaded(t, tt, s, "t1")

	if !tt.HasText("#31") || !tt.HasText("本地 +2 提交") || !tt.HasText("有未提交的改动") {
		t.Fatalf("Dirty 状态显示异常: %v", tt.Texts())
	}
	// 点击禁用按钮不应弹出合并确认浮层。
	clickText(t, tt, "有未提交的改动")
	tt.Frame()
	if tt.HasText("确认合并") || tt.HasText("确认推送并合并") {
		t.Fatalf("Dirty 时主按钮应禁用，不应弹出确认浮层: %v", tt.Texts())
	}
}

func TestComposerPRBarLocalCommitsAndForkLabels(t *testing.T) {
	t.Run("本地提交同仓显示推送并合并", func(t *testing.T) {
		fake := newComposerPRFlowFake()
		fake.setPRState("t1", git.PRThreadState{
			Number:       31,
			LocalCommits: 2,
			CrossRepo:    false,
		}, true, nil)
		s, tt := composerPRTestHarness(t, fake, Thread{ID: "t1", Title: "审查 PR #31"}, true)
		waitComposerPRLoaded(t, tt, s, "t1")

		if !tt.HasText("#31") || !tt.HasText("本地 +2 提交") || !tt.HasText("推送并合并") {
			t.Fatalf("同仓本地提交条文案有误: %v", tt.Texts())
		}
		if tt.HasText("Fork") {
			t.Fatalf("同仓 PR 不应显示 Fork 标记: %v", tt.Texts())
		}
	})

	t.Run("ForkPR显示Fork标记且按钮为合并", func(t *testing.T) {
		fake := newComposerPRFlowFake()
		fake.setPRState("t1", git.PRThreadState{
			Number:       32,
			LocalCommits: 2,
			CrossRepo:    true,
		}, true, nil)
		s, tt := composerPRTestHarness(t, fake, Thread{ID: "t1", Title: "审查 PR #32"}, true)
		waitComposerPRLoaded(t, tt, s, "t1")

		if !tt.HasText("#32") || !tt.HasText("本地 +2 提交") || !tt.HasText("Fork") || !tt.HasText("合并") {
			t.Fatalf("Fork PR 条显示有误: %v", tt.Texts())
		}
		if tt.HasText("推送并合并") {
			t.Fatalf("Fork PR 不应显示推送并合并: %v", tt.Texts())
		}
	})

	t.Run("无本地提交显示合并", func(t *testing.T) {
		fake := newComposerPRFlowFake()
		fake.setPRState("t1", git.PRThreadState{
			Number:       33,
			LocalCommits: 0,
			CrossRepo:    false,
		}, true, nil)
		s, tt := composerPRTestHarness(t, fake, Thread{ID: "t1", Title: "审查 PR #33"}, true)
		waitComposerPRLoaded(t, tt, s, "t1")

		if !tt.HasText("#33") || !tt.HasText("合并") {
			t.Fatalf("无本地提交 PR 条显示有误: %v", tt.Texts())
		}
		if tt.HasText("推送并合并") {
			t.Fatalf("无本地提交不应显示推送并合并: %v", tt.Texts())
		}
	})
}

func TestComposerPRBarConfirmPopoverAndMergeFlow(t *testing.T) {
	fake := newComposerPRFlowFake()
	fake.setPRState("t1", git.PRThreadState{
		Number:       31,
		LocalCommits: 2,
		CrossRepo:    false,
	}, true, nil)

	stepMergeReached := make(chan struct{})
	allowFinish := make(chan struct{})
	fake.mergeFn = func(_ context.Context, threadID string, number int, method, from string, onStep func(step string)) (string, error) {
		onStep("push")
		onStep("merge")
		close(stepMergeReached)
		<-allowFinish
		onStep("sync")
		onStep("cleanup")
		return "", nil
	}

	s, tt := composerPRTestHarness(t, fake, Thread{ID: "t1", ProjectID: "p1", Title: "审查 PR #31"}, true)
	waitComposerPRLoaded(t, tt, s, "t1")

	// 打开确认浮层。
	clickText(t, tt, "推送并合并")
	tt.Frame()
	if !tt.HasText("合并 PR #31") || !tt.HasText("Squash 合并") || !tt.HasText("合并提交") || !tt.HasText("变基合并") {
		t.Fatalf("确认浮层缺少合并方式选项: %v", tt.Texts())
	}
	if !tt.HasText("将推送 2 个本地提交到 PR 分支") {
		t.Fatalf("确认浮层缺少推送提交数提示: %v", tt.Texts())
	}
	if tt.HasText("合并后清理") {
		t.Fatalf("确认浮层不应包含合并后清理勾选框: %v", tt.Texts())
	}

	// 切换合并方式并触发流程。
	clickText(t, tt, "合并提交")
	tt.Frame()
	clickText(t, tt, "确认推送并合并")

	select {
	case <-stepMergeReached:
	case <-time.After(2 * time.Second):
		t.Fatal("未进入 merge 步骤")
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("推送") && tt.HasText("合并 PR") && tt.HasText("更新本地 main") && tt.HasText("清理")
	})

	// 同一会话进行中不能重复触发流程。
	startComposerPRMergeFlow(s, "t1", 31, "squash", "")
	if got := len(fake.recordedMergeCalls()); got != 1 {
		t.Fatalf("同一会话进行中不应重复触发，calls=%d", got)
	}

	// 完成合并后变为只读已合并态，并可通过「新建会话」在同项目新建会话。
	close(allowFinish)
	waitFor(t, tt, func() bool {
		return tt.HasText("已合并 #31") && tt.HasText("新建会话")
	})
	if _, ok := tt.Find("消息输入框"); ok {
		t.Fatalf("已合并态不应再显示输入框编辑区: %v", tt.Texts())
	}

	calls := fake.recordedMergeCalls()
	if len(calls) != 1 || calls[0].threadID != "t1" || calls[0].number != 31 || calls[0].method != "merge" || calls[0].from != "" {
		t.Fatalf("调用参数不符合预期: %+v", calls)
	}

	clickText(t, tt, "新建会话")
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.creatingThread && s.selectedThreadID != "" && s.selectedThreadID != "t1"
	})
	s.mu.Lock()
	sel := s.selectedThreadID
	var selProj string
	for _, th := range s.threads {
		if th.ID == sel {
			selProj = th.ProjectID
		}
	}
	s.mu.Unlock()
	if sel == "" || sel == "t1" || selProj != "p1" {
		t.Fatalf("点击新建会话应在同一项目 p1 创建新会话，got selected=%q proj=%q", sel, selProj)
	}
}

func TestComposerPRBarRetryPassesFailedStepAsFrom(t *testing.T) {
	fake := newComposerPRFlowFake()
	fake.setPRState("t1", git.PRThreadState{
		Number:       31,
		LocalCommits: 1,
	}, true, nil)

	var attempt atomic.Int32
	fake.mergeFn = func(_ context.Context, threadID string, number int, method, from string, onStep func(step string)) (string, error) {
		n := attempt.Add(1)
		if n == 1 {
			onStep("push")
			onStep("merge")
			return "merge", errors.New("合并被远端拒绝")
		}
		onStep("merge")
		onStep("sync")
		onStep("cleanup")
		return "", nil
	}

	s, tt := composerPRTestHarness(t, fake, Thread{ID: "t1", ProjectID: "p1", Title: "审查 PR #31"}, true)
	waitComposerPRLoaded(t, tt, s, "t1")

	clickText(t, tt, "推送并合并")
	tt.Frame()
	clickText(t, tt, "确认推送并合并")

	waitFor(t, tt, func() bool {
		return tt.HasText("重试") && tt.HasText("合并被远端拒绝")
	})

	clickText(t, tt, "重试")
	waitFor(t, tt, func() bool {
		return tt.HasText("已合并 #31")
	})

	calls := fake.recordedMergeCalls()
	if len(calls) != 2 {
		t.Fatalf("期望调用 2 次 GitPRMergeFlow，实际 %d: %+v", len(calls), calls)
	}
	if calls[0].from != "" {
		t.Fatalf("首次调用 from 应为空，实际 %q", calls[0].from)
	}
	if calls[1].from != "merge" {
		t.Fatalf("重试传入的 from 应等于失败步 merge，实际 %q", calls[1].from)
	}
}

func TestComposerPRBarArchivedMergedShowsStatusBanner(t *testing.T) {
	t.Run("带已合并前缀会话快照", func(t *testing.T) {
		fake := newComposerPRFlowFake()
		_, tt := composerPRTestHarness(t, fake, Thread{
			ID:        "t1",
			ProjectID: "p1",
			Title:     "已合并 · 审查 PR #31",
		}, true)

		if !tt.HasText("已合并 #31") || !tt.HasText("新建会话") {
			t.Fatalf("带已合并前缀会话应显示已合并状态条: %v", tt.Texts())
		}
		if _, ok := tt.Find("消息输入框"); ok {
			t.Fatalf("已合并会话不应显示输入框编辑区: %v", tt.Texts())
		}
		if fake.stateCalls.Load() != 0 {
			t.Fatalf("已合并会话不应触发 GitPRThreadState IO，got %d", fake.stateCalls.Load())
		}
	})

	t.Run("归档列表快照命中", func(t *testing.T) {
		fake := newComposerPRFlowFake()
		s, tt := composerPRTestHarness(t, fake, Thread{}, true)
		globalArchiveState.mu.Lock()
		globalArchiveState.threads = []Thread{{
			ID:        "arch-1",
			ProjectID: "p1",
			Title:     "已合并 · 修复边界条件 #42",
		}}
		globalArchiveState.mu.Unlock()
		t.Cleanup(resetArchivePageStateForTest)

		s.mu.Lock()
		s.selectedThreadID = "arch-1"
		s.mu.Unlock()
		tt.Frame()

		if !tt.HasText("已合并 #42") || !tt.HasText("新建会话") {
			t.Fatalf("归档快照中的已合并会话应显示状态条: %v", tt.Texts())
		}
		if _, ok := tt.Find("消息输入框"); ok {
			t.Fatalf("归档已合并会话不应显示输入框编辑区: %v", tt.Texts())
		}
	})
}

func TestComposerPRBarExpandAnimationSettles(t *testing.T) {
	fake := newComposerPRFlowFake()
	fake.setPRState("t1", git.PRThreadState{}, false, nil)

	s, tt := composerPRTestHarness(t, fake, Thread{ID: "t1", Title: "审查 PR #31"}, false)
	waitComposerPRLoaded(t, tt, s, "t1")
	if tt.HasText("#31") {
		t.Fatalf("初始非 PR 状态不应显示条: %v", tt.Texts())
	}

	fake.setPRState("t1", git.PRThreadState{Number: 31, LocalCommits: 1}, true, nil)
	refreshComposerPRThreadState(s, "t1")
	waitComposerPRLoaded(t, tt, s, "t1")

	tt.Advance(DurBase + 80*time.Millisecond)
	if !tt.HasText("#31") || !tt.HasText("推送并合并") {
		t.Fatalf("展开后应显示 PR 条内容: %v", tt.Texts())
	}
	if tt.FramePending() {
		t.Fatal("展开动画结束后不应再请求下一帧")
	}
	tt.Advance(2 * time.Second)
	if tt.FramePending() {
		t.Fatal("静止 PR 条推进时钟后不应请求帧")
	}
}

func TestComposerPRBarRefreshesOnGitStateAndTurnEnd(t *testing.T) {
	fake := newComposerPRFlowFake()
	fake.setPRState("t1", git.PRThreadState{Number: 31, Dirty: true}, true, nil)

	s, tt := composerPRTestHarness(t, fake, Thread{ID: "t1", Title: "审查 PR #31", IsRunning: true}, true)
	waitComposerPRLoaded(t, tt, s, "t1")
	initialCalls := fake.stateCalls.Load()
	if initialCalls != 1 {
		t.Fatalf("首次加载应调用 1 次 GitPRThreadState，got %d", initialCalls)
	}

	// 空闲多帧不应重复轮询。
	tt.Frame()
	tt.Frame()
	if got := fake.stateCalls.Load(); got != initialCalls {
		t.Fatalf("静止帧不应重复刷新状态，got %d want %d", got, initialCalls)
	}

	// 回合结束（IsRunning: true -> false）应触发一次刷新。
	fake.setPRState("t1", git.PRThreadState{Number: 31, Dirty: false, LocalCommits: 1}, true, nil)
	s.applyThreadSummary(Thread{ID: "t1", IsRunning: false})
	waitFor(t, tt, func() bool { return fake.stateCalls.Load() >= initialCalls+1 })
	tt.Frame()
	if !tt.HasText("推送并合并") {
		t.Fatalf("回合结束刷新后应更新为推送并合并: %v", tt.Texts())
	}

	// Git 状态刷新（loadGitState）应触发一次刷新。
	beforeGit := fake.stateCalls.Load()
	fake.setPRState("t1", git.PRThreadState{Number: 31, LocalCommits: 3}, true, nil)
	s.loadGitState("t1")
	waitFor(t, tt, func() bool { return fake.stateCalls.Load() >= beforeGit+1 })
	waitFor(t, tt, func() bool {
		tt.Frame()
		return tt.HasText("本地 +3 提交")
	})
}

func TestComposerPRBarUsesDesignTokens(t *testing.T) {
	// 确保只检查一次：实际棘轮由 TestDesignTokenRatchet 在验收阶段执行；
	// 这里顺带验证关键文案构造与步骤标签无回归。
	if got := composerPRStepLabel("sync"); got != "更新本地 main" {
		t.Fatalf("sync label = %q", got)
	}
	text, disabled, _ := composerPRPrimaryButtonSpec(git.PRThreadState{Number: 1, Dirty: true, LocalCommits: 2})
	if text != "有未提交的改动" || !disabled {
		t.Fatalf("Dirty spec = (%q, %v)", text, disabled)
	}
}
