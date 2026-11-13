package mcodeui

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"mcode/internal/git"
)

type gitHistoryFakeClient struct {
	*gitPaneFakeClient
	historyMu  sync.Mutex
	log        []git.LogCommit
	logCalls   []git.LogOptions
	logHook    func(context.Context, git.LogOptions) ([]git.LogCommit, error)
	detailHook func(context.Context, string) (git.CommitDetail, error)
	diffHook   func(context.Context) ([]git.FileDiff, error)
	resets     []string
}

func (f *gitHistoryFakeClient) GitLog(ctx context.Context, _ string, opts git.LogOptions) ([]git.LogCommit, error) {
	f.historyMu.Lock()
	f.logCalls = append(f.logCalls, opts)
	hook := f.logHook
	var commits []git.LogCommit
	if opts.Skip < len(f.log) {
		commits = append(commits, f.log[opts.Skip:min(len(f.log), opts.Skip+opts.Limit)]...)
	}
	f.historyMu.Unlock()
	if hook != nil {
		return hook(ctx, opts)
	}
	return commits, nil
}
func (f *gitHistoryFakeClient) GitShowCommit(ctx context.Context, _ string, sha string) (git.CommitDetail, error) {
	f.historyMu.Lock()
	hook := f.detailHook
	f.historyMu.Unlock()
	if hook != nil {
		return hook(ctx, sha)
	}
	return git.CommitDetail{LogCommit: git.LogCommit{SHA: sha, Subject: "提交详情", Author: "测试", AuthorTime: time.Now()}, Body: "提交详情\n\n正文", Files: []git.CommitFile{{Path: "src/file.go", Status: "M", Added: 1, Deleted: 1}}}, nil
}
func (f *gitHistoryFakeClient) GitCommitDiff(ctx context.Context, _ string, _ string, _ string) ([]git.FileDiff, error) {
	f.historyMu.Lock()
	hook := f.diffHook
	f.historyMu.Unlock()
	if hook != nil {
		return hook(ctx)
	}
	return []git.FileDiff{{NewPath: "src/file.go", Hunks: []git.Hunk{{Header: "@@ -1 +1 @@", Lines: []git.Line{{Kind: '-', Text: "旧内容", OldNo: 1}, {Kind: '+', Text: "新内容", NewNo: 1}}}}}}, nil
}
func (f *gitHistoryFakeClient) GitCreateBranchAt(context.Context, string, string, string, bool) error {
	return nil
}
func (f *gitHistoryFakeClient) GitTagCreate(context.Context, string, string, string, string) error {
	return nil
}
func (f *gitHistoryFakeClient) GitCherryPick(context.Context, string, string) error   { return nil }
func (f *gitHistoryFakeClient) GitRevertCommit(context.Context, string, string) error { return nil }
func (f *gitHistoryFakeClient) GitReset(_ context.Context, _ string, sha, mode string) error {
	f.historyMu.Lock()
	f.resets = append(f.resets, sha+":"+mode)
	f.historyMu.Unlock()
	return nil
}

func gitHistoryTestState(t *testing.T) (*chatState, *gitPaneState, *gitHistoryState, *gitHistoryFakeClient) {
	t.Helper()
	s, ps, base := gitPaneTestState(t)
	client := &gitHistoryFakeClient{gitPaneFakeClient: base}
	s.client = client
	h := gitHistoryStateFor(ps)
	t.Cleanup(func() {
		h.mu.Lock()
		if h.cancel != nil {
			h.cancel()
		}
		if h.detailCancel != nil {
			h.detailCancel()
		}
		if h.diffCancel != nil {
			h.diffCancel()
		}
		if h.debounce != nil {
			h.debounce.Stop()
		}
		h.mu.Unlock()
		gitHistoryStates.Delete(ps)
	})
	return s, ps, h, client
}

func gitHistoryWaitLoaded(t *testing.T, h *gitHistoryState) {
	t.Helper()
	gitPaneWait(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.initialized && !h.loading })
}

func TestGitGraphLaneCoordinatesAndSegments(t *testing.T) {
	if got := gitGraphLaneX(0); got != 10 {
		t.Fatalf("首泳道 x=%v", got)
	}
	if got := gitGraphLaneX(5) - gitGraphLaneX(4); got != 12 {
		t.Fatalf("泳道间距=%v", got)
	}
	tests := []struct {
		name    string
		commits []git.LogCommit
		at      int
		want    []gitGraphSegment
	}{
		{"直线", []git.LogCommit{{SHA: "c", Parents: []string{"p"}}, {SHA: "p"}}, 0, []gitGraphSegment{{0, 0, 0, true}}},
		{"分叉", []git.LogCommit{{SHA: "c", Parents: []string{"p", "q"}}, {SHA: "p"}, {SHA: "q"}}, 0, []gitGraphSegment{{0, 0, 0, true}, {0, 1, 1, true}, {1, 1, 1, true}}},
		{"合并", []git.LogCommit{{SHA: "c", Parents: []string{"p", "q"}}, {SHA: "p", Parents: []string{"q"}}, {SHA: "q"}}, 1, []gitGraphSegment{{0, 0, 0, false}, {1, 1, 1, false}, {0, 1, 1, true}, {1, 1, 1, true}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows := git.ComputeGraph(tc.commits, 6)
			if got := gitGraphSegments(rows[tc.at]); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("线段=%+v，预期=%+v", got, tc.want)
			}
			if width := gitGraphWidth(rows); width != float32(1+boolInt(tc.name != "直线"))*12+8 {
				t.Fatalf("图宽=%v", width)
			}
		})
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestGitHistoryRefLimitAndHeadPriority(t *testing.T) {
	refs, extra := gitHistoryRefs([]git.LogRef{{Name: "HEAD", Kind: "head"}, {Name: "v1", Kind: "tag"}, {Name: "origin/main", Kind: "remote"}, {Name: "main", Kind: "local"}, {Name: "feature", Kind: "local"}}, "main")
	if len(refs) != 2 || extra != 2 || refs[0].Name != "main" || refs[1].Name != "v1" {
		t.Fatalf("标签=%+v +%d", refs, extra)
	}
	if refs, extra := gitHistoryRefs(nil, ""); len(refs) != 0 || extra != 0 {
		t.Fatalf("空标签=%v +%d", refs, extra)
	}
}

func TestGitHistoryPaginationAtBottom(t *testing.T) {
	s, ps, h, client := gitHistoryTestState(t)
	for i := 0; i < 201; i++ {
		client.log = append(client.log, git.LogCommit{SHA: fmt.Sprintf("sha-%03d", i), Subject: fmt.Sprintf("提交 %03d", i), AuthorTime: time.Now()})
	}
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneHistory(c, s, ps.threadID, ps) }, 500, 360)
	gitHistoryWaitLoaded(t, h)
	tt.Frame()
	client.historyMu.Lock()
	count := len(client.logCalls)
	client.historyMu.Unlock()
	if count != 1 {
		t.Fatalf("到达底部前请求数=%d", count)
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
	if calls[0].Limit != 200 || calls[0].Skip != 0 || calls[1].Skip != 200 {
		t.Fatalf("分页参数=%+v", calls)
	}
	h.mu.Lock()
	total, more := len(h.commits), h.more
	h.mu.Unlock()
	if total != 201 || more {
		t.Fatalf("末页=%d，more=%v", total, more)
	}
}

func TestGitHistoryOpenFilePassesPath(t *testing.T) {
	s, ps, h, client := gitHistoryTestState(t)
	openGitFileHistory(ps, "src/file.go")
	ps.mu.Lock()
	segment := ps.segment
	ps.mu.Unlock()
	if segment != 1 {
		t.Fatal("没有切换历史分段")
	}
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneHistory(c, s, ps.threadID, ps) }, 500, 320)
	gitHistoryWaitLoaded(t, h)
	tt.Frame()
	client.historyMu.Lock()
	calls := append([]git.LogOptions(nil), client.logCalls...)
	client.historyMu.Unlock()
	if len(calls) != 1 || calls[0].Path != "src/file.go" {
		t.Fatalf("文件过滤=%+v", calls)
	}
	if !tt.HasText("file.go") {
		t.Fatal("没有文件过滤 chip")
	}
	if err := tt.Click("清除文件过滤"); err != nil {
		t.Fatal(err)
	}
	gitHistoryWaitLoaded(t, h)
	h.mu.Lock()
	path := h.opts.Path
	h.mu.Unlock()
	if path != "" {
		t.Fatal("文件过滤没有清除")
	}
}

func TestGitHistoryHardResetRequiresConfirmation(t *testing.T) {
	s, ps, h, client := gitHistoryTestState(t)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.ButtonBase(c.Key("reset-test-row")).Label("提交行").Size(300, RowHeight).ContextMenu(gitHistoryMenu(c, s, ps, h, client, git.LogCommit{SHA: "abcdef012345"})).Children(func() { ui.Text(c, "提交行") })
		renderGitHistoryDialog(c, s, ps, h, client)
	}, 600, 360)
	if err := tt.RightClick("提交行"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("重置到此", "丢弃全部…（hard）"); err != nil {
		t.Fatal(err)
	}
	client.historyMu.Lock()
	count := len(client.resets)
	client.historyMu.Unlock()
	if count != 0 {
		t.Fatal("确认前执行了重置")
	}
	tt.Advance(DurSlow)
	if !tt.HasText("重置到 abcdef0 并丢弃全部改动？") || !tt.Focused("取消") {
		t.Fatal("确认标题或默认焦点错误")
	}
	if err := tt.Click("丢弃并重置"); err != nil {
		t.Fatal(err)
	}
	gitPaneWait(t, func() bool { client.historyMu.Lock(); defer client.historyMu.Unlock(); return len(client.resets) == 1 })
	client.historyMu.Lock()
	got := client.resets[0]
	client.historyMu.Unlock()
	if got != "abcdef012345:hard" {
		t.Fatalf("重置参数=%q", got)
	}
	gitPaneWait(t, func() bool { return !h.isWriting() })
}

func TestGitHistoryAgentBusyDisablesHardReset(t *testing.T) {
	s, ps, h, client := gitHistoryTestState(t)
	h.resetTo(ps, client, "abcdef0", "hard")
	s.mu.Lock()
	s.threads[0].IsRunning = true
	s.mu.Unlock()
	tt := ui.NewTester(func(c *ui.Context) { renderGitHistoryDialog(c, s, ps, h, client) }, 600, 360)
	if node, ok := tt.Access("丢弃并重置"); !ok || !node.Disabled || node.Description != "Agent 正在修改此工作区" {
		t.Fatalf("重置按钮=%+v %v", node, ok)
	}
	h.resetTo(ps, client, "abcdef0", "soft")
	client.historyMu.Lock()
	defer client.historyMu.Unlock()
	if len(client.resets) != 0 {
		t.Fatal("Agent 运行时执行了重置")
	}
}

func TestGitHistoryNarrowHidesTime(t *testing.T) {
	for _, width := range []float32{399, 400} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			s, ps, h, client := gitHistoryTestState(t)
			at := time.Now().Add(-3 * time.Hour)
			h.mu.Lock()
			h.dirty = false
			h.commits = []git.LogCommit{{SHA: "abc", Subject: "一个提交", AuthorTime: at}}
			h.graph = git.ComputeGraph(h.commits, 6)
			h.mu.Unlock()
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c).Fill().Children(func() { renderGitHistoryList(c, s, ps, h, client, width, "") })
			}, int(width), 240)
			if got := tt.HasText(formatRelativeTime(at)); got != (width >= 400) {
				t.Fatalf("宽度=%v，显示时间=%v", width, got)
			}
		})
	}
}

func TestGitHistoryDiscardStaleLogDuringSearch(t *testing.T) {
	_, ps, h, client := gitHistoryTestState(t)
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	client.logHook = func(ctx context.Context, opts git.LogOptions) ([]git.LogCommit, error) {
		if opts.Query == "" {
			close(started)
			<-release
			close(finished)
			return []git.LogCommit{{SHA: "old"}}, nil
		}
		return []git.LogCommit{{SHA: "new", Subject: opts.Query}}, nil
	}
	h.load(ps, client, false)
	<-started
	h.setSearch(ps, "新查询", true)
	h.load(ps, client, false)
	gitHistoryWaitLoaded(t, h)
	close(release)
	<-finished
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.commits) != 1 || h.commits[0].SHA != "new" {
		t.Fatalf("旧查询覆盖新查询=%+v", h.commits)
	}
}

func TestGitHistoryDebounceAndHeadRefresh(t *testing.T) {
	s, ps, h, client := gitHistoryTestState(t)
	h.setSearch(ps, "a", false)
	h.setSearch(ps, "ab", false)
	h.mu.Lock()
	dirty := h.dirty
	h.mu.Unlock()
	if dirty {
		t.Fatal("防抖期间不应发起查询")
	}
	gitPaneWait(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.dirty })
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneHistory(c, s, ps.threadID, ps) }, 500, 320)
	gitHistoryWaitLoaded(t, h)
	ps.mu.Lock()
	ps.state.Head = "new-head"
	ps.mu.Unlock()
	tt.Frame()
	gitHistoryWaitLoaded(t, h)
	client.historyMu.Lock()
	defer client.historyMu.Unlock()
	if len(client.logCalls) != 2 || client.logCalls[0].Query != "ab" || client.logCalls[1].Skip != 0 {
		t.Fatalf("防抖/HEAD 刷新=%+v", client.logCalls)
	}
}

func TestGitHistoryDetailAndDiffNavigation(t *testing.T) {
	s, ps, h, _ := gitHistoryTestState(t)
	openGitCommitDetail(ps, "abcdef0")
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneHistory(c, s, ps.threadID, ps) }, 500, 420)
	gitPaneWait(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return !h.detailLoading && h.detail.SHA == "abcdef0" })
	tt.Frame()
	tt.Advance(DurSlow)
	if !tt.HasText("提交详情") || !tt.HasText("正文") {
		t.Fatal("缺少提交正文")
	}
	if err := tt.Click("src/file.go"); err != nil {
		t.Fatal(err)
	}
	gitPaneWait(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return !h.diffLoading })
	tt.Frame()
	if len(h.diffViews) != 1 || !strings.Contains(h.diffViews[0].Old.String(), "旧内容") || !strings.Contains(h.diffViews[0].New.String(), "新内容") || h.diffViews[0].Editable {
		t.Fatal("缺少只读补丁")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	h.mu.Lock()
	path, mode := h.diffPath, h.mode
	h.mu.Unlock()
	if path != "" || mode != "commit" {
		t.Fatalf("返回文件列表=%q %q", path, mode)
	}
}

func TestGitHistoryMenuExplainFillsDraft(t *testing.T) {
	s, ps, h, client := gitHistoryTestState(t)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.ButtonBase(c).Label("提交行").Size(300, RowHeight).ContextMenu(gitHistoryMenu(c, s, ps, h, client, git.LogCommit{SHA: "abcdef0", Subject: "示例提交"})).Children(func() { ui.Text(c, "提交行") })
	}, 600, 500)
	if err := tt.RightClick("提交行"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(DurSlow)
	if err := tt.ChooseMenuItem("让 AI 解释此提交"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	draft := s.draft
	s.mu.Unlock()
	if draft != "解释提交 abcdef0：示例提交" {
		t.Fatalf("草稿=%q", draft)
	}
}

func TestGitHistoryBlameEntryAndCommitReturn(t *testing.T) {
	_, ps, h, _ := gitHistoryTestState(t)
	openGitBlame(ps, "src/file.go")
	h.mu.Lock()
	mode, path := h.mode, h.blamePath
	h.mu.Unlock()
	if mode != "blame" || !strings.HasSuffix(path, "file.go") {
		t.Fatal("没有进入 blame")
	}
	openGitCommitDetail(ps, "abcdef0")
	h.back(ps)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.mode != "blame" {
		t.Fatal("提交详情没有返回 blame")
	}
}

func TestGitHistoryDiscardStaleDetail(t *testing.T) {
	_, ps, h, client := gitHistoryTestState(t)
	started, release := make(chan struct{}), make(chan struct{})
	client.detailHook = func(_ context.Context, sha string) (git.CommitDetail, error) {
		if sha == "old" {
			close(started)
			<-release
		}
		return git.CommitDetail{LogCommit: git.LogCommit{SHA: sha}}, nil
	}
	openGitCommitDetail(ps, "old")
	h.loadDetail(ps, client)
	<-started
	openGitCommitDetail(ps, "new")
	h.loadDetail(ps, client)
	gitPaneWait(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return !h.detailLoading && h.detail.SHA == "new" })
	close(release)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.detail.SHA != "new" {
		t.Fatalf("旧详情覆盖新详情=%s", h.detail.SHA)
	}
}

func TestGitHistoryDisposedPaneRejectsResult(t *testing.T) {
	_, ps, h, client := gitHistoryTestState(t)
	started, release := make(chan struct{}), make(chan struct{})
	client.logHook = func(context.Context, git.LogOptions) ([]git.LogCommit, error) {
		close(started)
		<-release
		return []git.LogCommit{{SHA: "late"}}, nil
	}
	h.load(ps, client, false)
	<-started
	ps.dispose()
	close(release)
	gitPaneWait(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return !h.loading })
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.commits) != 0 {
		t.Fatal("销毁后接收了历史结果")
	}
}

func TestGitHistoryWritesSharePaneOperationSlot(t *testing.T) {
	_, ps, h, client := gitHistoryTestState(t)
	started, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	second := false
	h.write(ps, client, true, "已重置", func(context.Context) error { close(started); <-release; return nil })
	<-started
	ps.mu.Lock()
	syncing := ps.syncing
	ps.mu.Unlock()
	if !syncing {
		t.Fatal("历史写操作没有占用面板操作槽")
	}
	h.write(ps, client, true, "已重置", func(context.Context) error { mu.Lock(); second = true; mu.Unlock(); return nil })
	close(release)
	gitPaneWait(t, func() bool { return !h.isWriting() })
	mu.Lock()
	defer mu.Unlock()
	if second {
		t.Fatal("并行执行了两个写操作")
	}
}

func TestGitHistoryWriteFeedback(t *testing.T) {
	s, ps, h, client := gitHistoryTestState(t)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	h.write(ps, client, true, "已重置", func(context.Context) error { close(started); <-release; return nil })
	<-started
	gitPaneWait(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.writing && h.writeIndicator })
	tt := ui.NewTester(func(c *ui.Context) { renderGitHistoryFilters(c, ps, h) }, 500, 100)
	tt.Frame()
	if _, ok := tt.Find("加载中"); !ok {
		t.Fatal("写入时未显示加载状态")
	}
	once.Do(func() { close(release) })
	gitPaneWait(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.toastMessage == "已重置" })
}

func TestGitHistoryKeyboardOpensSelectedCommit(t *testing.T) {
	s, ps, h, client := gitHistoryTestState(t)
	client.log = []git.LogCommit{{SHA: "first", Subject: "第一条"}, {SHA: "second", Subject: "第二条"}}
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneHistory(c, s, ps.threadID, ps) }, 500, 360)
	gitHistoryWaitLoaded(t, h)
	tt.Frame()
	if err := tt.Click("第一条"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	h.mu.Lock()
	mode, sha := h.mode, h.sha
	h.mu.Unlock()
	if mode != "commit" || sha != "second" {
		t.Fatalf("键盘打开=%q %q", mode, sha)
	}
}

func TestGitHistorySearchShortcutAndEscape(t *testing.T) {
	s, ps, h, client := gitHistoryTestState(t)
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneHistory(c, s, ps.threadID, ps) }, 500, 360)
	gitHistoryWaitLoaded(t, h)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyF)
	tt.Type("needle")
	h.mu.Lock()
	search := h.search
	h.mu.Unlock()
	if search != "needle" {
		t.Fatalf("搜索框输入=%q", search)
	}
	gitPaneWait(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.dirty })
	tt.Frame()
	gitHistoryWaitLoaded(t, h)
	client.historyMu.Lock()
	opts := client.logCalls[len(client.logCalls)-1]
	client.historyMu.Unlock()
	if opts.Query != "needle" {
		t.Fatalf("查询=%+v", opts)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	h.mu.Lock()
	open, query := h.searchOpen, h.opts.Query
	h.mu.Unlock()
	if open || query != "" {
		t.Fatalf("Esc 未收起并清空=%v %q", open, query)
	}
}

func TestGitHistoryDiffViewPreservesPatchCoordinates(t *testing.T) {
	view := newGitHistoryDiffView(git.FileDiff{OldPath: "file.go", NewPath: "file.go", Hunks: []git.Hunk{{OldStart: 40, OldLines: 1, NewStart: 41, NewLines: 1, Header: "@@ -40 +41 @@ 函数", Lines: []git.Line{{Kind: '-', Text: "旧内容"}, {Kind: '+', Text: "新内容"}}}}})
	tt := ui.NewTester(func(c *ui.Context) { editor.DiffView(c, view).Fill() }, 500, 300)
	tt.Frame()
	if view.Editable || view.HunkActions || view.Old.String() != "旧内容" || view.New.String() != "新内容" {
		t.Fatal("补丁内容或只读设置错误")
	}
	if adds, dels := view.Stats(); adds != 1 || dels != 1 {
		t.Fatalf("补丁统计=+%d -%d", adds, dels)
	}
	if !tt.HasText("@@ -40,1 +41,1 @@ 函数") {
		t.Fatalf("补丁没有保留真实行号：%v", tt.Texts())
	}
}

func TestGitHistoryCanceledDiffRetriesOnReturn(t *testing.T) {
	s, ps, h, client := gitHistoryTestState(t)
	var mu sync.Mutex
	calls := 0
	client.diffHook = func(context.Context) ([]git.FileDiff, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return nil, context.Canceled
		}
		return []git.FileDiff{{NewPath: "file.go"}}, nil
	}
	openGitCommitDetail(ps, "abcdef0")
	h.openDiff(ps, client, "file.go")
	gitPaneWait(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.diffDirty })
	tt := ui.NewTester(func(c *ui.Context) { renderGitPaneHistory(c, s, ps.threadID, ps) }, 500, 360)
	gitPaneWait(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return !h.diffLoading && !h.diffDirty })
	tt.Frame()
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("取消后重试次数=%d", calls)
	}
}
