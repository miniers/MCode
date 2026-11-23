package mcodeui

import (
	"context"
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

type reviewAtFake struct {
	*FakeClient
	atMu  sync.Mutex
	heads map[string][]string
}

func (f *reviewAtFake) ReviewGetAt(ctx context.Context, tid, scope, turn, head, path string) (ReviewSnapshot, error) {
	f.atMu.Lock()
	f.heads[path] = append(f.heads[path], head)
	f.atMu.Unlock()
	return f.FakeClient.ReviewGet(ctx, tid, scope, turn, path)
}

// 一屏放下的卡片多于缓存默认上限时，正文不能互相挤出缓存、反复取数；正文固定读取列表时的 head。
func TestReviewCardsFetchEachBodyOnceAtListedHead(t *testing.T) {
	testenv.TempHome(t)
	client := &reviewAtFake{FakeClient: NewFakeClient(), heads: map[string][]string{}}
	s := newChatState(client)
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.projects = []Project{{ID: "p1", Path: "/workspace"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}}
	list := ReviewSnapshot{Scope: "turn", TurnID: "turn-1", BaseRevision: "base", HeadRevision: "head"}
	for i := range 30 {
		list.Files = append(list.Files, ReviewFile{Path: fmt.Sprintf("f%02d.go", i), Status: "M", Additions: 1})
	}
	client.SetReviewSnapshot("t1", "turn", "", "", list)
	for _, file := range list.Files {
		full := list
		full.File = &ReviewFileContent{Path: file.Path, OldText: "a\n", NewText: "b\n"}
		client.SetReviewSnapshot("t1", "turn", "turn-1", file.Path, full)
	}
	dp := getDiffPanelState(s)
	diffPanelMu.Lock()
	for _, file := range list.Files {
		setReviewCardOpenLocked(dp, file.Path, true)
	}
	diffPanelMu.Unlock()
	tt := ui.NewTester(func(c *ui.Context) { renderReviewPanel(c, s, dp) }, 800, 2400)
	calls := func() (n int) {
		client.atMu.Lock()
		defer client.atMu.Unlock()
		for path, heads := range client.heads {
			if len(heads) > 2 || slices.ContainsFunc(heads, func(h string) bool { return h != "head" }) {
				t.Fatalf("%s 取数 %v", path, heads)
			}
			n += len(heads)
		}
		return n
	}
	settle := func() {
		for range 40 {
			tt.Advance(20 * time.Millisecond)
			tt.Frame()
			time.Sleep(time.Millisecond)
		}
	}
	settle()
	before := calls()
	settle()
	if before == 0 || calls() != before {
		t.Fatalf("正文取数没有停下：%d -> %d", before, calls())
	}
}

// 改动卡片默认全部折叠：不读正文，点击文件头只读该文件，再点收起；Enter 与点击同一套开合。
func TestReviewCardsCollapsedByDefault(t *testing.T) {
	testenv.TempHome(t)
	client := &reviewAtFake{FakeClient: NewFakeClient(), heads: map[string][]string{}}
	s := newChatState(client)
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.projects = []Project{{ID: "p1", Path: "/workspace"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}}
	body := map[string]string{"a.go": "alpha-only-body", "b.go": "beta-only-body", "c.go": "gamma-only-body"}
	list := ReviewSnapshot{Scope: "turn", TurnID: "turn-1", BaseRevision: "base", HeadRevision: "head"}
	for _, path := range []string{"a.go", "b.go", "c.go"} {
		list.Files = append(list.Files, ReviewFile{Path: path, Status: "M", Additions: 1, Deletions: 1})
	}
	client.SetReviewSnapshot("t1", "turn", "", "", list)
	for path, text := range body {
		full := list
		full.File = &ReviewFileContent{Path: path, OldText: "old\n", NewText: text + "\n"}
		client.SetReviewSnapshot("t1", "turn", "turn-1", path, full)
	}
	dp := getDiffPanelState(s)
	tt := ui.NewTester(func(c *ui.Context) { renderReviewPanel(c, s, dp) }, 800, 600)
	reads := func() (total int, paths map[string]int) {
		client.atMu.Lock()
		defer client.atMu.Unlock()
		paths = map[string]int{}
		for path, calls := range client.heads {
			paths[path] = len(calls)
			total += len(calls)
		}
		return total, paths
	}
	waitFor(t, tt, func() bool { return tt.HasText("a.go") && tt.HasText("b.go") && tt.HasText("c.go") })
	for range 10 {
		tt.Advance(20 * time.Millisecond)
		tt.Frame()
	}
	if total, paths := reads(); total != 0 {
		t.Fatalf("默认折叠却读取了正文：%v", paths)
	}
	clickText(t, tt, "a.go")
	waitFor(t, tt, func() bool {
		diffPanelMu.Lock()
		defer diffPanelMu.Unlock()
		for _, view := range dp.views {
			if view != nil && strings.Contains(view.state.New.String(), body["a.go"]) {
				return true
			}
		}
		return false
	})
	tt.Advance(DurSlow)
	tt.Frame()
	if !tt.HasText("New text") {
		t.Fatal("展开后没有渲染 diff 正文")
	}
	total, paths := reads()
	if total != 1 || paths["a.go"] != 1 {
		t.Fatalf("展开 a.go 应只读取一次：%v", paths)
	}
	clickText(t, tt, "a.go")
	tt.Advance(DurSlow)
	tt.Frame()
	if tt.HasText("New text") {
		t.Fatal("再次点击文件头应收起正文")
	}
	if total, paths := reads(); total != 1 || paths["a.go"] != 1 {
		t.Fatalf("收起不应新增读取：%v", paths)
	}
	// 文件头仍持有焦点：Enter 与点击走同一段开合逻辑，且不重复读正文。
	tt.Key(0, ui.KeyEnter)
	tt.Advance(DurSlow)
	tt.Frame()
	if !tt.HasText("New text") {
		t.Fatal("Enter 应展开当前文件")
	}
	if total, paths := reads(); total != 1 || paths["a.go"] != 1 {
		t.Fatalf("再次展开不应重读正文：%v", paths)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Advance(DurSlow)
	tt.Frame()
	if tt.HasText("New text") {
		t.Fatal("再次 Enter 应收起当前文件")
	}
	tt.Advance(DurSlow)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("开合动画结束后仍在请求下一帧")
	}
}

func reviewUncommittedTester(t *testing.T) (*chatState, *gitChangesFakeClient, *diffPanelState, *ui.Tester) {
	t.Helper()
	s, ps, base := gitPaneTestState(t)
	client := &gitChangesFakeClient{gitPaneFakeClient: base}
	s.client = client
	root := t.TempDir()
	entries := []git.StatusEntry{{Path: "a.go", Index: ' ', Worktree: 'M'}}
	base.state, base.entries = GitState{WorkDir: root}, entries
	ps.state, ps.entries = GitState{WorkDir: root}, entries
	client.SetReviewSnapshot("git-test", "uncommitted", "", "", ReviewSnapshot{Scope: "uncommitted", HeadRevision: "head", Files: []ReviewFile{{Path: "a.go", Status: "M", Additions: 1, Deletions: 1}}})
	dp := getDiffPanelState(s)
	dp.scope = "all"
	tt := ui.NewTester(func(c *ui.Context) { renderReviewPanel(c, s, dp) }, 800, 600)
	waitFor(t, tt, func() bool { return tt.HasText("提交信息") })
	return s, client, dp, tt
}

func TestReviewUncommittedStageBox(t *testing.T) {
	_, client, _, tt := reviewUncommittedTester(t)
	if err := tt.Click("暂存"); err != nil {
		t.Fatal(err)
	}
	gitPaneWait(t, func() bool {
		client.paneMu.Lock()
		defer client.paneMu.Unlock()
		return slices.Contains(client.calls, "stage")
	})
	client.paneMu.Lock()
	defer client.paneMu.Unlock()
	if !slices.Equal(client.paths, []string{"a.go"}) {
		t.Fatalf("暂存路径 = %v", client.paths)
	}
}

// 文件快捷键挂在整个面板上，提交框获得焦点时 j、v、空格必须进入输入框。
func TestReviewShortcutsDoNotStealCommitInput(t *testing.T) {
	s, client, dp, tt := reviewUncommittedTester(t)
	if err := tt.Click("提交信息"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Type("j v")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	st := gitChangesStateFor(getGitPaneState(s))
	st.mu.Lock()
	draft := st.draft
	st.mu.Unlock()
	diffPanelMu.Lock()
	reads, selected := len(dp.readKeys), dp.selectedPath
	diffPanelMu.Unlock()
	client.paneMu.Lock()
	calls := slices.Clone(client.calls)
	client.paneMu.Unlock()
	if !strings.Contains(draft, "j v") || reads != 0 || selected != "" || slices.Contains(calls, "stage") {
		t.Fatalf("输入被快捷键截走：draft=%q reads=%d selected=%q calls=%v", draft, reads, selected, calls)
	}
}

func TestGitPaneShowsHistoryOnly(t *testing.T) {
	s, ps, client := gitPaneTestState(t)
	client.state = GitState{WorkDir: t.TempDir()}
	s.client = &gitChangesFakeClient{gitPaneFakeClient: client}
	ps.mu.Lock()
	ps.state = client.state
	ps.visible = true
	ps.mu.Unlock()
	tt := ui.NewTester(func(c *ui.Context) { renderGitPane(c, s) }, 480, 400)
	tt.Frame()
	if tt.HasText("提交信息") {
		t.Fatal("工作区干净时 Git 标签不应显示提交框")
	}
	client.entries = []git.StatusEntry{{Path: "a.go", Worktree: 'M'}}
	ps.mu.Lock()
	ps.entries = client.entries
	ps.mu.Unlock()
	tt.Frame()
	if !tt.HasText("提交信息") {
		t.Fatal("工作区有改动时全部范围应显示提交框")
	}
}
