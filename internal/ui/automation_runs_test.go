package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type automationRunsTestClient struct {
	*FakeClient
	mu                          sync.Mutex
	runs                        []AutomationRun
	reads                       []string
	readErr, allErr, archiveErr error
	archiveCalls                chan chan error
}

func (f *automationRunsTestClient) AutomationRunsState(_ context.Context, _ string, _ int, _ bool) (AutomationRunsState, error) {
	return AutomationRunsState{Runs: f.runs}, nil
}
func (f *automationRunsTestClient) MarkAutomationRunRead(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, id)
	return f.readErr
}
func (f *automationRunsTestClient) MarkAllAutomationRunsRead(context.Context, string) (int, error) {
	return 0, f.allErr
}
func (f *automationRunsTestClient) SetAutomationRunArchived(ctx context.Context, _ string, _ bool) error {
	if f.archiveCalls != nil {
		result := make(chan error, 1)
		select {
		case f.archiveCalls <- result:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case err := <-result:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.archiveErr
}
func (f *automationRunsTestClient) AutomationUnreadCounts(context.Context, string) (map[string]int, error) {
	return nil, errors.New("unavailable")
}

func automationRunsTester(t *testing.T, f *automationRunsTestClient) (*chatState, *automationState) {
	t.Helper()
	f.SetAutomations([]Automation{testAutomation("auto-1", "p1")})
	tt, s := newWorkspaceTester(t, f, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("auto-1") })
	if err := tt.Click("auto-1"); err != nil {
		t.Fatal(err)
	}
	as := getAutomationState(s)
	waitFor(t, tt, func() bool {
		automationMu.Lock()
		defer automationMu.Unlock()
		return !as.runsLoading && as.runRead != nil
	})
	return s, as
}

func waitAutomationRunsUpdate(t *testing.T, s *chatState, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.applyWorkspaceUpdates()
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("等待运行状态更新超时")
}

func waitAutomationRunsQueued(t *testing.T, s *chatState) {
	t.Helper()
	ws := getWorkspaceState(s)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		workspaceMu.Lock()
		ready := len(ws.updates) > 0
		workspaceMu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("等待运行请求结束超时")
}

func TestAutomationRunDateAndDuration(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		date time.Time
		want string
	}{{now, "今天"}, {now.AddDate(0, 0, -1), "昨天"}, {now.AddDate(0, 0, -2), "2026-11-08"}} {
		if got := automationRunDate(tc.date, now); got != tc.want {
			t.Errorf("日期 = %q, want %q", got, tc.want)
		}
	}
	if got := automationRunDuration(AutomationRun{Status: "running", StartedAt: now}); got != "运行中" {
		t.Fatal(got)
	}
	if automationRunUnread(nil, "old") {
		t.Fatal("旧客户端应按已读处理")
	}
	if got := automationRunTriggerBadge(AutomationRun{Trigger: "skipped"}); got != "已跳过" {
		t.Fatal(got)
	}
}

func TestAutomationRunReadFailureRollback(t *testing.T) {
	f := &automationRunsTestClient{FakeClient: NewFakeClient(), runs: []AutomationRun{{ID: "r1", StartedAt: time.Now()}, {ID: "r2", StartedAt: time.Now()}}, readErr: errors.New("故障")}
	s, as := automationRunsTester(t, f)
	automationMu.Lock()
	as.unread = map[string]int{"auto-1": 2}
	automationMu.Unlock()
	s.markAutomationRunRead("auto-1", "r1")
	waitAutomationRunsUpdate(t, s, func() bool {
		automationMu.Lock()
		defer automationMu.Unlock()
		return automationRunsViewFor(as, "auto-1").err != ""
	})
	automationMu.Lock()
	defer automationMu.Unlock()
	if !automationRunUnread(as.runRead, "r1") || !automationRunUnread(as.runRead, "r2") || as.unread["auto-1"] != 2 {
		t.Fatal("已读回滚失败")
	}
	if !strings.Contains(automationRunsViewFor(as, "auto-1").err, "故障") || automationRunsViewFor(as, "auto-1").retry == nil {
		t.Fatal("缺少重试反馈")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if fmt.Sprint(f.reads) != "[r1]" {
		t.Fatal(f.reads)
	}
}

func TestAutomationRunArchiveFailureAndOrder(t *testing.T) {
	f := &automationRunsTestClient{FakeClient: NewFakeClient(), runs: []AutomationRun{{ID: "r1", StartedAt: time.Now()}}, archiveCalls: make(chan chan error, 2)}
	s, as := automationRunsTester(t, f)
	s.setAutomationRunArchived("r1", true)
	first := <-f.archiveCalls
	first <- errors.New("故障")
	waitAutomationRunsUpdate(t, s, func() bool {
		automationMu.Lock()
		defer automationMu.Unlock()
		return automationRunsViewFor(as, "auto-1").err != ""
	})
	automationMu.Lock()
	if as.runArchived["r1"] {
		t.Fatal("归档没有回滚")
	}
	automationMu.Unlock()
	s.setAutomationRunArchived("r1", true)
	first = <-f.archiveCalls
	s.setAutomationRunArchived("r1", false)
	second := <-f.archiveCalls
	second <- nil
	waitAutomationRunsQueued(t, s)
	s.applyWorkspaceUpdates()
	first <- errors.New("旧故障")
	waitAutomationRunsQueued(t, s)
	s.applyWorkspaceUpdates()
	automationMu.Lock()
	defer automationMu.Unlock()
	if as.runArchived["r1"] || automationRunsViewFor(as, "auto-1").err != "" {
		t.Fatal("旧请求覆盖了新请求")
	}
}

func TestAutomationRunAllReadFailureAndSwitch(t *testing.T) {
	f := &automationRunsTestClient{FakeClient: NewFakeClient(), runs: []AutomationRun{{ID: "r1", StartedAt: time.Now()}}, allErr: errors.New("故障"), archiveCalls: make(chan chan error, 1)}
	s, as := automationRunsTester(t, f)
	automationMu.Lock()
	as.unread = map[string]int{"auto-1": 1}
	automationMu.Unlock()
	s.markAllAutomationRunsRead("auto-1")
	waitAutomationRunsUpdate(t, s, func() bool {
		automationMu.Lock()
		defer automationMu.Unlock()
		return automationRunsViewFor(as, "auto-1").err != ""
	})
	automationMu.Lock()
	if !automationRunUnread(as.runRead, "r1") || as.unread["auto-1"] != 1 {
		t.Fatal("全部已读未回滚")
	}
	automationMu.Unlock()
	s.setAutomationRunArchived("r1", true)
	reply := <-f.archiveCalls
	automationMu.Lock()
	as.selectedID = "auto-2"
	as.editSession++
	as.runArchived = map[string]bool{"r1": true}
	automationMu.Unlock()
	reply <- errors.New("旧故障")
	waitAutomationRunsQueued(t, s)
	s.applyWorkspaceUpdates()
	automationMu.Lock()
	defer automationMu.Unlock()
	if !as.runArchived["r1"] {
		t.Fatal("旧请求修改了新对象")
	}
}

func TestAutomationRunHistoryDetailsAndFilters(t *testing.T) {
	now := time.Now()
	longError := strings.Repeat("错误详情 ", 200)
	f := &automationRunsTestClient{FakeClient: NewFakeClient(), runs: []AutomationRun{
		{ID: "r1", Status: "failed", Error: longError, StartedAt: now},
		{ID: "r2", Status: "running", StartedAt: now.AddDate(0, 0, -1)},
	}}
	s := &chatState{client: f}
	as := getAutomationState(s)
	as.projectID, as.selectedID, as.runRead, as.runs = "p1", "auto-1", map[string]time.Time{}, f.runs
	tt := ui.NewTesterWith(func(c *ui.Context) {
		s.applyWorkspaceUpdates()
		automationMu.Lock()
		open := as.openRunID
		reads := make(map[string]time.Time, len(as.runRead))
		for id, at := range as.runRead {
			reads[id] = at
		}
		automationMu.Unlock()
		ui.Scroll(c).Fill().Children(func() {
			renderAutomationRunHistory(c, s, as, false, "auto-1", 2, false, false, "", f.runs, reads, nil, open)
		})
	}, 1000, 800, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	waitFor(t, tt, func() bool { return tt.HasText("今天") && tt.HasText("昨天") && tt.HasText("运行中") })
	if err := tt.Click("筛选运行记录"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("筛选失败"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("昨天") })
	if err := tt.Click("运行记录 r1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("返回运行记录") || tt.HasText("← 运行记录") })
	if tt.HasText("打开会话") || tt.HasText("摘要") {
		t.Fatal("空会话或摘要不应显示")
	}
	if r, ok := tt.Find(longError); !ok || r.W > 1000 {
		t.Fatalf("错误内容撑破布局: %+v", r)
	}
	if err := tt.Click("复制错误"); err != nil {
		r, _ := tt.Find("复制错误")
		t.Fatalf("%v: %+v", err, r)
	}
	if got := tt.Clipboard(); got != longError {
		t.Fatal("错误复制不完整")
	}
	waitFor(t, tt, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.reads) == 1 })
	f.mu.Lock()
	if f.reads[0] != "r1" {
		t.Fatal(f.reads)
	}
	f.mu.Unlock()
	if err := tt.Click("返回运行记录"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("今天") && !tt.HasText("昨天") })
}

func TestAutomationRunHistorySummaryAndUnread(t *testing.T) {
	runs := []AutomationRun{
		{ID: "r1", Status: "succeeded", ThreadID: "thread-1", Summary: "执行结果", StartedAt: time.Now()},
		{ID: "r2", Status: "failed", StartedAt: time.Now().Add(-time.Minute)},
	}
	f := &automationRunsTestClient{FakeClient: NewFakeClient(), runs: runs}
	s := &chatState{client: f}
	as := getAutomationState(s)
	as.projectID, as.selectedID, as.runRead, as.runs = "p1", "auto-1", map[string]time.Time{"r2": time.Now()}, runs
	tt := ui.NewTesterWith(func(c *ui.Context) {
		s.applyWorkspaceUpdates()
		automationMu.Lock()
		open := as.openRunID
		reads := make(map[string]time.Time, len(as.runRead))
		for id, at := range as.runRead {
			reads[id] = at
		}
		automationMu.Unlock()
		ui.Scroll(c).Fill().Children(func() {
			renderAutomationRunHistory(c, s, as, false, "auto-1", 1, false, false, "", runs, reads, nil, open)
		})
	}, 1000, 800, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	tt.Frame()
	if err := tt.Click("筛选运行记录"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("筛选未读"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("运行记录 r2"); ok {
		t.Fatal("未读筛选包含已读记录")
	}
	if err := tt.Click("运行记录 r1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("打开会话") && tt.HasText("执行结果") })
	if err := tt.Click("复制摘要"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "执行结果" {
		t.Fatal("摘要复制失败")
	}
}

func TestAutomationRunHistoryLimit(t *testing.T) {
	f := &automationRunsTestClient{FakeClient: NewFakeClient()}
	for i := 0; i < 100; i++ {
		f.runs = append(f.runs, AutomationRun{ID: fmt.Sprint(i), StartedAt: time.Now().Add(-time.Duration(i) * time.Minute)})
	}
	f.SetAutomations([]Automation{testAutomation("auto-1", "p1")})
	tt, _ := newWorkspaceTester(t, f, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("auto-1") })
	if err := tt.Click("auto-1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("仅显示最近 100 条") })
}

func TestAutomationRefreshLoading(t *testing.T) {
	for _, runs := range [][]AutomationRun{nil, {{ID: "r1", Status: "running", StartedAt: time.Now()}}} {
		s := &chatState{client: NewFakeClient()}
		as := getAutomationState(s)
		tt := ui.NewTester(func(c *ui.Context) {
			renderAutomationRunHistory(c, s, as, false, "a", 0, false, true, "", runs, nil, nil, "")
		}, 1000, 800)
		tt.Frame()
		if err := tt.Click("运行记录更多操作"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if node, ok := tt.Access("刷新记录"); !ok || !node.Disabled {
			t.Fatalf("刷新按钮未显示加载禁用状态: %+v", node)
		}
	}
}
