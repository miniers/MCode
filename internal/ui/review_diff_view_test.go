package mcodeui

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

func reviewFixture() (ReviewSnapshot, ReviewSnapshot) {
	list := ReviewSnapshot{Scope: "turn", TurnID: "turn-1", BaseRevision: "base", HeadRevision: "head", Files: []ReviewFile{{Path: "a.go", Status: "modified", Language: "go"}, {Path: "b.go", Status: "added"}}}
	full := list
	full.File = &ReviewFileContent{Path: "a.go", OldText: "package main\nvar a = 1\n", NewText: "package main\nvar a = 2\n"}
	return list, full
}

func newReviewTester(t *testing.T, client Client) (*chatState, *ui.Tester, *diffPanelState) {
	t.Helper()
	s, view := buildRoot(client)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1200, 750)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	tt.Key(ui.Cmd, ui.KeyD)
	tt.Advance(DurSheet + 50*time.Millisecond)
	dp := getDiffPanelState(s)
	waitFor(t, tt, func() bool { diffPanelMu.Lock(); defer diffPanelMu.Unlock(); return dp.requestGen > 0 && !dp.loading })
	return s, tt, dp
}

func TestReviewDiffFullLineCoordinates(t *testing.T) {
	prefix := strings.Repeat("unchanged\n", 1233)
	view := newReviewDiffView(ReviewFileContent{Path: "a.go", OldText: prefix + "old\n", NewText: prefix + "new\n"}, "go")
	hunks := view.state.Hunks()
	if len(hunks) != 1 || hunks[0].OldStart != 1233 || hunks[0].NewStart != 1233 {
		t.Fatalf("全文坐标错误: %+v", hunks)
	}
	if view.state.Editable || view.state.NoLineNumbers || view.state.Inline == editor.DiffInlineNone || view.state.FitContent {
		t.Fatal("只读、行号、行内高亮或有界视口配置错误")
	}
	if view.state.New.Language() == nil {
		t.Fatal("未识别 Go 语法")
	}
	unknown := newReviewDiffView(ReviewFileContent{Path: "a.unknown", NewText: "plain"}, "")
	if unknown.state.New.Language() != nil {
		t.Fatal("未知扩展名不应伪造语言")
	}
}

func TestReviewUnifiedSplitSharedState(t *testing.T) {
	content := benchReviewContent(200)
	view := newReviewDiffView(content, "go")
	old, next := view.state.Old, view.state.New
	split, width := false, float32(800)
	tt := ui.NewTester(func(c *ui.Context) { renderReviewDiffView(c, "diff", view, split, width).Fill() }, 800, 300)
	tt.Frame()
	view.state.SetScrollY(1000)
	tt.Frame()
	tt.Frame()
	anchorY := view.state.ScrollY()
	if anchorY <= 0 {
		t.Fatal("未滚动")
	}
	view.state.SetSelectedLines(editor.DiffLineRange{Side: editor.DiffNew, Start: 50, End: 51}, true)
	split = true
	tt.Frame()
	tt.Frame()
	tt.Frame()
	if view.state.Layout != editor.DiffSplit || view.state.New != next || view.state.Old != old {
		t.Fatal("布局切换替换了全文状态")
	}
	width = 400
	tt.Frame()
	tt.Frame()
	tt.Frame()
	if view.state.Layout != editor.DiffUnified || !split {
		t.Fatal("窄栏应临时统一且保留并排偏好")
	}
	if math.Abs(view.state.ScrollY()-anchorY) > 50 {
		t.Fatalf("切换丢失定位: %v -> %v", anchorY, view.state.ScrollY())
	}
	if selection, ok := view.state.SelectedLines(); !ok || selection.Start != 50 {
		t.Fatal("切换丢失行选区")
	}
	width = 800
	tt.Frame()
	tt.Frame()
	if view.state.Layout != editor.DiffSplit {
		t.Fatal("加宽后未恢复并排")
	}
}

func TestReviewFileContentFetchedOnSelect(t *testing.T) {
	fake := NewFakeClient()
	list, full := reviewFixture()
	fake.SetReviewSnapshot("t1", "turn", "", "", list)
	fake.SetReviewSnapshot("t1", "turn", "turn-1", "a.go", full)
	_, tt, dp := newReviewTester(t, fake)
	if !tt.HasText("a.go") || !tt.HasText("b.go") {
		t.Fatal("应先显示元数据文件列表")
	}
	for _, call := range fake.ReviewGetCalls() {
		if call.Path != "" {
			t.Fatal("未选中就请求全文")
		}
	}
	if len(dp.views) != 0 {
		t.Fatal("列表阶段不应创建 Buffer")
	}
	clickText(t, tt, "a.go")
	waitFor(t, tt, func() bool {
		diffPanelMu.Lock()
		defer diffPanelMu.Unlock()
		for _, entry := range dp.contents {
			if entry.content != nil {
				return true
			}
		}
		return false
	})
	tt.Frame()
	if len(dp.views) != 1 {
		t.Fatalf("只应为选中的文件创建 Buffer，得到 %d", len(dp.views))
	}
	if !tt.HasText("返回改动列表") {
		t.Fatal("窄栏缺少返回按钮")
	}
	tt.Advance(DurBase + 50*time.Millisecond)
	clickText(t, tt, "返回改动列表")
	if !tt.HasText("b.go") {
		t.Fatal("返回后未恢复列表")
	}
	for _, call := range fake.ReviewGetCalls() {
		if call.Path == "b.go" {
			t.Fatal("滚动或返回列表不应加载其他全文")
		}
	}
}

func TestReviewBinaryAndLargeFileFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		file ReviewFileContent
	}{
		{"二进制文件", ReviewFileContent{Binary: true}}, {"文件过大", ReviewFileContent{TooLarge: true}}, {"文件版本不可用", ReviewFileContent{Missing: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if newReviewDiffView(tc.file, "") != nil {
				t.Fatal("不可用正文不应成为空 diff")
			}
			fake := NewFakeClient()
			list, _ := reviewFixture()
			list.Files = []ReviewFile{{Path: "a.go", Binary: tc.file.Binary, TooLarge: tc.file.TooLarge, Missing: tc.file.Missing}}
			fake.SetReviewSnapshot("t1", "turn", "", "", list)
			_, tt, dp := newReviewTester(t, fake)
			clickText(t, tt, "a.go")
			waitFor(t, tt, func() bool { return tt.HasText(tc.name) })
			if len(dp.views) != 0 {
				t.Fatal("不可用文件不应创建 Buffer")
			}
			for _, call := range fake.ReviewGetCalls() {
				if call.Path != "" {
					t.Fatal("元数据已标记不可用，不应读取全文")
				}
			}
		})
	}
}

func TestReviewUnavailableShowsReason(t *testing.T) {
	fake := NewFakeClient()
	reason := "该轮缺少起始 checkpoint"
	fake.SetReviewSnapshot("t1", "turn", "", "", ReviewSnapshot{Unavailable: reason})
	_, tt, _ := newReviewTester(t, fake)
	if !tt.HasText(reason) || tt.HasText(diffEmptyTitle) {
		t.Fatal("应显示后端真实原因，不能伪装成无改动")
	}
}

func TestReviewHiddenStateCacheBounded(t *testing.T) {
	dp := &diffPanelState{}
	for i := 0; i < reviewDiffCacheLimit*4; i++ {
		key := reviewDiffKey{workspace: "root", scope: "turn", head: fmt.Sprint(i), path: "a.go"}
		reviewViewFor(dp, key, ReviewFileContent{Path: "a.go", OldText: "old", NewText: "new"}, "go")
	}
	if len(dp.views) != reviewDiffCacheLimit || len(dp.viewOrder) != reviewDiffCacheLimit {
		t.Fatal("隐藏面板编辑器缓存没有上限")
	}
}

type reviewHookClient struct {
	Client
	source reviewClient
	get    func(context.Context, string, string, string, string) (ReviewSnapshot, error)
}

func (f reviewHookClient) ReviewGet(ctx context.Context, tid, scope, turn, path string) (ReviewSnapshot, error) {
	if f.get != nil {
		return f.get(ctx, tid, scope, turn, path)
	}
	return f.source.ReviewGet(ctx, tid, scope, turn, path)
}

func TestReviewScopeSwitchKeepsOldUntilReady(t *testing.T) {
	fake := NewFakeClient()
	list, _ := reviewFixture()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	client := reviewHookClient{Client: fake, get: func(ctx context.Context, tid, scope, turn, path string) (ReviewSnapshot, error) {
		switch scope {
		case "session":
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return ReviewSnapshot{}, ctx.Err()
			}
			defer close(done)
			return ReviewSnapshot{Scope: scope, HeadRevision: "stale", Files: []ReviewFile{{Path: "stale.go"}}}, nil
		case "uncommitted":
			return ReviewSnapshot{Scope: scope, HeadRevision: "work", Files: []ReviewFile{{Path: "work.go"}}}, nil
		default:
			return list, nil
		}
	}}
	s, tt, dp := newReviewTester(t, client)
	s.selectReviewScope(dp, "session")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("未请求整个会话")
	}
	tt.Frame()
	if !tt.HasText("a.go") {
		t.Fatal("新批次就绪前不应清空旧内容")
	}
	s.selectReviewScope(dp, "uncommitted")
	waitFor(t, tt, func() bool { return tt.HasText("work.go") })
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("旧请求未结束")
	}
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	diffPanelMu.Lock()
	head := dp.snapshot.HeadRevision
	diffPanelMu.Unlock()
	if head != "work" || tt.HasText("stale.go") {
		t.Fatal("过期回包覆盖了当前作用域")
	}
}

func TestTimelineAndInspectorUseSameRevision(t *testing.T) {
	fake := NewFakeClient()
	list, full := reviewFixture()
	fake.SetReviewSnapshot("t1", "turn", "turn-1", "", list)
	fake.SetReviewSnapshot("t1", "turn", "turn-1", "a.go", full)
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.mu.Unlock()
	s.loadToolReview(fake, "t1", 0, "turn-1", s.toolDiff.generation)
	dp := getDiffPanelState(s)
	s.loadDiff("t1", "turn-1")
	tt := ui.NewTester(func(c *ui.Context) { renderReviewPanel(c, s, dp) }, 480, 600)
	waitFor(t, tt, func() bool {
		diffPanelMu.Lock()
		defer diffPanelMu.Unlock()
		return !dp.loading && dp.snapshot.HeadRevision == "head"
	})
	s.mu.Lock()
	timeline := *s.toolDiff.reviews["t1|turn-1"].snapshot
	s.mu.Unlock()
	key := reviewKey(s, "t1", timeline, "a.go")
	entry := reviewContentFor(s, dp, key)
	if !entry.loading {
		t.Fatal("首次选择应懒加载")
	}
	waitFor(t, tt, func() bool { entry = reviewContentFor(s, dp, key); return !entry.loading })
	diffPanelMu.Lock()
	inspectorKey := reviewKey(s, "t1", dp.snapshot, "a.go")
	diffPanelMu.Unlock()
	if key != inspectorKey || entry.content == nil {
		t.Fatal("时间线和右栏没有使用同一版本坐标")
	}
	a := reviewViewFor(dp, key, *entry.content, "go")
	timelineKey := key
	timelineKey.surface = "message"
	b := reviewViewFor(dp, timelineKey, *entry.content, "go")
	if fmt.Sprint(a.state.Hunks()) != fmt.Sprint(b.state.Hunks()) || a.state.New.String() != b.state.New.String() {
		t.Fatal("两处全文或行号不同")
	}
	if a.state == b.state {
		t.Fatal("两个同时显示的视口不能共用滚动状态")
	}
}

func TestReviewRejectsChangedRevision(t *testing.T) {
	fake := NewFakeClient()
	list, full := reviewFixture()
	full.HeadRevision = "new-head"
	fake.SetReviewSnapshot("t1", "turn", "turn-1", "a.go", full)
	s := newChatState(fake)
	t.Cleanup(s.close)
	dp := getDiffPanelState(s)
	key := reviewKey(s, "t1", list, "a.go")
	tt := ui.NewTester(func(c *ui.Context) { ui.Text(c, "wait") }, 400, 300)
	reviewContentFor(s, dp, key)
	waitFor(t, tt, func() bool { return !reviewContentFor(s, dp, key).loading })
	entry := reviewContentFor(s, dp, key)
	if entry.content != nil || entry.err != "改动已更新，请刷新" {
		t.Fatalf("不应接纳漂移版本: %+v", entry)
	}
}

func TestReviewScopeMenuAndTurnIsolation(t *testing.T) {
	fake := NewFakeClient()
	list, _ := reviewFixture()
	fake.SetReviewSnapshot("t1", "turn", "", "", list)
	fake.SetReviewSnapshot("t1", "session", "", "", ReviewSnapshot{HeadRevision: "session-head", Files: []ReviewFile{{Path: "session.go"}}})
	fake.SetReviewSnapshot("t1", "uncommitted", "", "", ReviewSnapshot{HeadRevision: "working-head", Files: []ReviewFile{{Path: "working.go"}}})
	s, tt, dp := newReviewTester(t, fake)
	clickText(t, tt, "本轮")
	tt.Advance(DurBase + 50*time.Millisecond)
	if !tt.HasText("整个会话") || !tt.HasText("未提交") {
		t.Fatal("作用域菜单不完整")
	}
	clickText(t, tt, "整个会话")
	waitFor(t, tt, func() bool { return tt.HasText("session.go") })
	diffPanelMu.Lock()
	dp.turnID = "old-turn"
	diffPanelMu.Unlock()
	s.selectReviewScope(dp, "uncommitted")
	waitFor(t, tt, func() bool { return tt.HasText("working.go") })
	for _, call := range fake.ReviewGetCalls() {
		if call.Scope != "turn" && call.TurnID != "" {
			t.Fatal("非本轮作用域泄漏了轮次参数")
		}
	}
	s.mu.Lock()
	s.selectedThreadID = "t2"
	s.mu.Unlock()
	other := getDiffPanelState(s)
	if reviewScope(other.scope) != "turn" {
		t.Fatal("新会话作用域应为本轮")
	}
	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.mu.Unlock()
	if getDiffPanelState(s) != dp || dp.scope != "uncommitted" {
		t.Fatal("返回会话未恢复作用域")
	}
}

func TestReviewContentCacheBoundedAndInvalidated(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	dp := getDiffPanelState(s)
	for i := 0; i < reviewDiffCacheLimit*3; i++ {
		key := reviewDiffKey{threadID: "t1", scope: "turn", head: fmt.Sprint(i), path: "a.go"}
		reviewContentFor(s, dp, key)
	}
	diffPanelMu.Lock()
	if len(dp.contents) > reviewDiffCacheLimit || len(dp.contentOrder) > reviewDiffCacheLimit {
		diffPanelMu.Unlock()
		t.Fatal("正文缓存未受上限约束")
	}
	diffPanelMu.Unlock()
	started, release := make(chan struct{}), make(chan struct{})
	client := reviewHookClient{Client: fake, get: func(ctx context.Context, tid, scope, turn, path string) (ReviewSnapshot, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return ReviewSnapshot{}, ctx.Err()
		}
		return ReviewSnapshot{HeadRevision: "late", File: &ReviewFileContent{Path: path, NewText: "stale"}}, nil
	}}
	s.client = client
	key := reviewDiffKey{threadID: "t1", scope: "turn", head: "late", path: "late.go"}
	reviewContentFor(s, dp, key)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("正文请求没有启动")
	}
	diffPanelMu.Lock()
	dp.requestGen++
	diffPanelMu.Unlock()
	close(release)
	// 读取门闩完成后等待发布路径；任何已失效请求都不能成为可渲染正文。
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		diffPanelMu.Lock()
		content := dp.contents[key].content
		diffPanelMu.Unlock()
		if content != nil {
			t.Fatal("已失效的正文回包仍被接纳")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTimelineReviewOpensInspectorPath(t *testing.T) {
	fake := NewFakeClient()
	list, full := reviewFixture()
	fake.SetReviewSnapshot("t1", "turn", "turn-1", "", list)
	fake.SetReviewSnapshot("t1", "turn", "turn-1", "a.go", full)
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.mu.Unlock()
	m := Message{ID: "tool", Role: "tool", ToolName: "edit", ToolInput: `{"path":"a.go"}`, TurnID: "turn-1"}
	dp := getDiffPanelState(s)
	tt := ui.NewTester(func(c *ui.Context) { ui.Column(c).Fill().Children(func() { renderTimelineReview(c, s, m) }) }, 800, 600)
	waitFor(t, tt, func() bool { return len(dp.views) == 1 && tt.HasText("在改动中打开") })
	clickText(t, tt, "在改动中打开")
	waitFor(t, tt, func() bool {
		diffPanelMu.Lock()
		defer diffPanelMu.Unlock()
		return !dp.loading && dp.selectedPath == "a.go"
	})
	if !inspectorPaneOpen(s, sidePaneDiffID) {
		t.Fatal("未打开右栏改动标签")
	}
	for key, view := range dp.views {
		if key.head != "head" || view.state.New.String() != full.File.NewText {
			t.Fatal("时间线未使用固定版本全文")
		}
	}
}
