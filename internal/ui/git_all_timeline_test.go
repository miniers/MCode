package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

func gitAllTimelineSetup(t *testing.T, width, height int) (*chatState, *gitPaneState, *gitHistoryState, *gitTabMergedFakeClient, *diffPanelState, func() *ui.Tester) {
	t.Helper()
	s, ps, h, histClient := gitHistoryTestState(t)
	client := &gitTabMergedFakeClient{gitHistoryFakeClient: histClient}
	s.client = client
	histClient.state = GitState{WorkDir: t.TempDir(), Branch: "main", Head: "sha-head"}
	ps.mu.Lock()
	ps.state = histClient.state
	ps.visible = true
	ps.mu.Unlock()
	dp := getDiffPanelState(s)
	diffPanelMu.Lock()
	dp.scope = "all"
	diffPanelMu.Unlock()
	start := func() *ui.Tester {
		return ui.NewTester(func(c *ui.Context) { renderGitPane(c, s) }, width, height)
	}
	return s, ps, h, client, dp, start
}

func TestGitAllTimeline_DirtyOrderAndCleanWorkspace(t *testing.T) {
	setAppReduceMotion(t, true)
	s, ps, h, client, dp, start := gitAllTimelineSetup(t, 480, 760)
	now := time.Now()
	client.log = []git.LogCommit{
		{SHA: "sha-1", Subject: "最新提交", AuthorTime: now.Add(-time.Hour)},
		{SHA: "sha-0", Subject: "初始提交", AuthorTime: now.Add(-2 * time.Hour)},
	}
	client.entries = []git.StatusEntry{
		{Path: "a.go", Worktree: 'M'},
		{Path: "b.go", Index: 'A'},
	}
	ps.mu.Lock()
	ps.entries = slices.Clone(client.entries)
	ps.mu.Unlock()
	client.SetReviewSnapshot("git-test", "uncommitted", "", "", ReviewSnapshot{
		Scope:        "uncommitted",
		HeadRevision: "work-head",
		Files: []ReviewFile{
			{Path: "a.go", Status: "M", Additions: 3, Deletions: 1},
			{Path: "b.go", Status: "A", Additions: 5},
		},
	})

	tt := start()
	gitHistoryWaitLoaded(t, h)
	waitFor(t, tt, func() bool {
		return tt.HasText("未提交") && tt.HasText("a.go") && tt.HasText("b.go") && tt.HasText("提交信息") && tt.HasText("最新提交") && tt.HasText("初始提交")
	})

	if !tt.HasText("2 个文件") || !tt.HasText("+8") || !tt.HasText("−1") {
		t.Fatalf("工作区节点头统计缺失：texts=%v", tt.Texts())
	}
	if _, ok := tt.Find("git-pane-uncommitted"); ok {
		t.Fatal("不应再存在旧两段式 git-pane-uncommitted 容器")
	}

	wipRect, _ := tt.Find("未提交")
	aRect, _ := tt.Find("a.go")
	bRect, _ := tt.Find("b.go")
	compRect, _ := tt.Find("提交信息")
	c1Rect, _ := tt.Find("最新提交")
	c2Rect, _ := tt.Find("初始提交")
	if !(wipRect.Y < aRect.Y && aRect.Y < bRect.Y && bRect.Y < compRect.Y && compRect.Y < c1Rect.Y && c1Rect.Y < c2Rect.Y) {
		t.Fatalf("统一列表行序异常：wip=%v a=%v b=%v comp=%v c1=%v c2=%v", wipRect.Y, aRect.Y, bRect.Y, compRect.Y, c1Rect.Y, c2Rect.Y)
	}
	if tt.FramePending() {
		t.Fatal("静止状态下不应持续请求下一帧")
	}

	// 清空工作区后只保留提交行
	client.entries = nil
	ps.mu.Lock()
	ps.entries = nil
	ps.mu.Unlock()
	client.SetReviewSnapshot("git-test", "uncommitted", "", "", ReviewSnapshot{Scope: "uncommitted", HeadRevision: "clean-head"})
	s.loadDiff(ps.threadID, "")
	waitFor(t, tt, func() bool {
		return !tt.HasText("未提交") && !tt.HasText("提交信息") && tt.HasText("最新提交")
	})
	if tt.HasText("a.go") || tt.HasText("b.go") {
		t.Fatalf("工作区干净时不应显示文件行：texts=%v", tt.Texts())
	}
	diffPanelMu.Lock()
	cleanFiles := len(dp.snapshot.Files)
	diffPanelMu.Unlock()
	if cleanFiles != 0 {
		t.Fatalf("干净后快照文件应为空，got %d", cleanFiles)
	}
}

func TestGitAllTimeline_OffsetPaginationAndSubmitted(t *testing.T) {
	setAppReduceMotion(t, true)
	_, ps, h, client, _, start := gitAllTimelineSetup(t, 700, 420)
	for i := 0; i < 201; i++ {
		client.log = append(client.log, git.LogCommit{
			SHA:        fmt.Sprintf("sha-%03d", i),
			Subject:    fmt.Sprintf("提交 %03d", i),
			AuthorTime: time.Now(),
		})
	}
	client.entries = []git.StatusEntry{{Path: "f0.go", Worktree: 'M'}, {Path: "f1.go", Worktree: 'M'}}
	ps.mu.Lock()
	ps.entries = slices.Clone(client.entries)
	ps.mu.Unlock()
	client.SetReviewSnapshot("git-test", "uncommitted", "", "", ReviewSnapshot{
		Scope:        "uncommitted",
		HeadRevision: "work",
		Files:        []ReviewFile{{Path: "f0.go", Status: "M"}, {Path: "f1.go", Status: "M"}},
	})

	tt := start()
	gitHistoryWaitLoaded(t, h)
	waitFor(t, tt, func() bool {
		return tt.HasText("f0.go") && tt.HasText("提交 000")
	})

	client.historyMu.Lock()
	beforeCount := len(client.logCalls)
	client.historyMu.Unlock()
	if beforeCount != 1 {
		t.Fatalf("到达底部前请求数=%d，期望 1", beforeCount)
	}

	h.list.ScrollToEnd()
	tt.Frame()
	tt.Frame()
	gitPaneWait(t, func() bool {
		client.historyMu.Lock()
		defer client.historyMu.Unlock()
		return len(client.logCalls) >= 2
	})
	gitHistoryWaitLoaded(t, h)
	client.historyMu.Lock()
	calls := append([]git.LogOptions(nil), client.logCalls...)
	client.historyMu.Unlock()
	if calls[0].Skip != 0 || calls[1].Skip != 200 {
		t.Fatalf("分页偏移异常=%+v", calls)
	}

	// 滚回顶部选中提交并用键盘 Enter 打开，验证偏移换算后打开正确 SHA 且宽屏分栏保留统一列表
	h.list.ScrollTo(0, ui.Start)
	tt.Frame()
	waitFor(t, tt, func() bool {
		return tt.HasText("提交 000") && tt.HasText("提交 001")
	})
	if err := tt.Click("提交 000"); err != nil {
		t.Fatal(err)
	}
	const commitOffset = 6 // 节点头(1) + 2*2文件(4) + 提交框(1)
	if h.selected != commitOffset {
		t.Fatalf("选中第 0 条提交行索引=%d，期望 %d", h.selected, commitOffset)
	}
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	h.mu.Lock()
	mode, sha := h.mode, h.sha
	h.mu.Unlock()
	if mode != "commit" || sha != "sha-001" {
		t.Fatalf("偏移换算打开提交错误：mode=%q sha=%q", mode, sha)
	}
	// 宽屏分栏时上半部分仍保留统一列表中的工作区与提交
	waitFor(t, tt, func() bool {
		return tt.HasText("提交详情") && tt.HasText("未提交")
	})
}

func TestGitAllTimeline_ThreadTouchedDot(t *testing.T) {
	setAppReduceMotion(t, true)
	s, ps, h, client, dp, start := gitAllTimelineSetup(t, 480, 600)
	client.log = []git.LogCommit{{SHA: "sha-1", Subject: "初始提交", AuthorTime: time.Now()}}
	client.entries = []git.StatusEntry{{Path: "touched.go", Worktree: 'M'}, {Path: "other.go", Worktree: 'M'}}
	ps.mu.Lock()
	ps.entries = slices.Clone(client.entries)
	ps.mu.Unlock()
	client.SetReviewSnapshot("git-test", "uncommitted", "", "", ReviewSnapshot{
		Scope:        "uncommitted",
		HeadRevision: "work",
		Files: []ReviewFile{
			{Path: "touched.go", Status: "M", Additions: 1},
			{Path: "other.go", Status: "M", Additions: 1},
		},
	})
	client.SetReviewSnapshot("git-test", "session", "", "", ReviewSnapshot{
		Scope:        "session",
		HeadRevision: "sess",
		Files:        []ReviewFile{{Path: "touched.go", Status: "M"}},
	})

	tt := start()
	gitHistoryWaitLoaded(t, h)
	waitFor(t, tt, func() bool {
		_, hasDot := tt.Find("本线程改动:touched.go")
		return tt.HasText("touched.go") && tt.HasText("other.go") && hasDot
	})

	if _, ok := tt.Find("本线程改动:other.go"); ok {
		t.Fatal("非本线程文件不应显示圆点")
	}
	dotRect, _ := tt.Find("本线程改动:touched.go")
	rowRect, _ := tt.Find("touched.go")
	if dotRect.W != Space2 || dotRect.H != Space2 || dotRect.X <= rowRect.X || dotRect.X >= rowRect.X+rowRect.W/2 || dotRect.Y < rowRect.Y || dotRect.Y+dotRect.H > rowRect.Y+rowRect.H {
		t.Fatalf("本线程圆点尺寸或位置异常：dot=%+v row=%+v", dotRect, rowRect)
	}

	// session 获取失败时不显示圆点且不报错
	s.client = reviewHookClient{Client: client, get: func(ctx context.Context, tid, scope, turn, path string) (ReviewSnapshot, error) {
		if scope == "session" {
			return ReviewSnapshot{}, errors.New("session fetch failed")
		}
		return client.ReviewGet(ctx, tid, scope, turn, path)
	}}
	s.loadDiff(ps.threadID, "")
	waitFor(t, tt, func() bool {
		_, hasDot := tt.Find("本线程改动:touched.go")
		return !hasDot
	})
	diffPanelMu.Lock()
	errMsg := dp.err
	diffPanelMu.Unlock()
	if errMsg != "" {
		t.Fatalf("session 获取失败不应报错：%q", errMsg)
	}
}

func TestGitAllTimeline_CommitHoverMoreMenu(t *testing.T) {
	setAppReduceMotion(t, true)
	s, _, h, client, _, start := gitAllTimelineSetup(t, 480, 400)
	at := time.Now().Add(-3 * time.Hour)
	relTime := formatRelativeTime(at)
	client.log = []git.LogCommit{{SHA: "abcdef012345", Subject: "悬停测试提交", AuthorTime: at}}

	tt := start()
	gitHistoryWaitLoaded(t, h)
	waitFor(t, tt, func() bool { return tt.HasText("悬停测试提交") })

	if !tt.HasText(relTime) {
		t.Fatalf("静止时应显示相对时间 %q，texts=%v", relTime, tt.Texts())
	}
	if _, ok := tt.Find("提交操作"); ok {
		t.Fatal("静止时不应显示「提交操作」按钮")
	}

	rowRect, ok := tt.Find("悬停测试提交")
	if !ok {
		t.Fatal("找不到提交行")
	}
	tt.Move(rowRect.X+rowRect.W/2, rowRect.Y+rowRect.H/2)
	tt.Frame()

	if tt.HasText(relTime) {
		t.Fatalf("悬停时应用更多按钮替换相对时间 %q", relTime)
	}
	moreRect, ok := tt.Find("提交操作")
	if !ok {
		t.Fatal("悬停时应显示「提交操作」按钮")
	}
	tt.ClickAt(moreRect.X+moreRect.W/2, moreRect.Y+moreRect.H/2)
	tt.Frame()
	menu := tt.Menu()
	if menu == nil || !slices.Contains(menu, "复制 SHA") || !slices.Contains(menu, "让 AI 解释此提交") {
		t.Fatalf("点击「提交操作」应弹出 gitHistoryMenu，got=%v", menu)
	}
	if err := tt.ChooseMenuItem("让 AI 解释此提交"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	draft := s.draft
	s.mu.Unlock()
	if draft != "解释提交 abcdef012345：悬停测试提交" {
		t.Fatalf("菜单操作未生效：draft=%q", draft)
	}
}
