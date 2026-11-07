package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/steward"
	mcodeui "mcode/internal/ui"
)

// fakeTopicRuntime 是话题能力的测试替身：同时实现 stewardThreadRuntime 与 stewardTopicRuntime。
type fakeTopicRuntime struct {
	threadID  string
	topics    []steward.Topic
	topicsErr error

	openedProject string
	openedTitle   string
	openedRequest string
	confirmed     []string
	closed        []string
	deleted       []string
	replanned     []string
	startedGoals  []string
	deleteErr     error

	mu       sync.Mutex
	watchers []func()
}

// ThreadID 返回当前会话 ID。
func (f *fakeTopicRuntime) ThreadID() string { return f.threadID }

// Ensure 原样返回会话 ID。
func (f *fakeTopicRuntime) Ensure(context.Context) (string, error) { return f.threadID, nil }

// Topics 返回预设的话题。
func (f *fakeTopicRuntime) Topics(context.Context) ([]steward.Topic, error) {
	return append([]steward.Topic(nil), f.topics...), f.topicsErr
}

// OpenTopic 记录开话题参数。
func (f *fakeTopicRuntime) OpenTopic(_ context.Context, projectID, title, request string) (string, error) {
	f.openedProject, f.openedTitle, f.openedRequest = projectID, title, request
	return "topic-new", nil
}

// ConfirmTopic 记录确认开工。
func (f *fakeTopicRuntime) ConfirmTopic(_ context.Context, threadID string) error {
	f.confirmed = append(f.confirmed, threadID)
	return nil
}

// CloseTopic 记录关闭话题。
func (f *fakeTopicRuntime) CloseTopic(_ context.Context, threadID string) error {
	f.closed = append(f.closed, threadID)
	return nil
}

// DeleteTopic 记录删除话题，并按需返回预设错误。
func (f *fakeTopicRuntime) DeleteTopic(_ context.Context, threadID string) (bool, error) {
	if f.deleteErr != nil {
		return false, f.deleteErr
	}
	f.deleted = append(f.deleted, threadID)
	return true, nil
}

// ReplanTopic 记录重新规划。
func (f *fakeTopicRuntime) ReplanTopic(_ context.Context, threadID string) error {
	f.replanned = append(f.replanned, threadID)
	return nil
}

// StartTopicGoal 记录直接开工。
func (f *fakeTopicRuntime) StartTopicGoal(_ context.Context, threadID, _ string) error {
	f.startedGoals = append(f.startedGoals, threadID)
	return nil
}

// WatchTopics 记录订阅者并返回取消函数。
func (f *fakeTopicRuntime) WatchTopics(fn func()) (cancel func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watchers = append(f.watchers, fn)
	index := len(f.watchers) - 1
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.watchers[index] = nil
	}
}

// watcherCount 返回仍有效的订阅者数量。
func (f *fakeTopicRuntime) watcherCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, fn := range f.watchers {
		if fn != nil {
			n++
		}
	}
	return n
}

// TestStewardTopicPendingUser 覆盖待用户处理件数的四种来源与已关闭的情况。
func TestStewardTopicPendingUser(t *testing.T) {
	cases := []struct {
		name          string
		phase         string
		brief         string
		openQuestions int
		reviewTasks   int
		closed        bool
		want          int
	}{
		{name: "任务书就绪", phase: mcodeui.StewardTopicDrafting, brief: "## 目标\n做点什么", openQuestions: 0, want: 1},
		{name: "还有待定问题", phase: mcodeui.StewardTopicDrafting, brief: "## 目标\n做点什么", openQuestions: 2, want: 0},
		{name: "任务书为空", phase: mcodeui.StewardTopicDrafting, brief: "   ", openQuestions: 0, want: 0},
		{name: "待验收任务", phase: mcodeui.StewardTopicRunning, reviewTasks: 2, want: 2},
		{name: "待审计计划", phase: mcodeui.StewardTopicPlanReview, want: 0},
		{name: "待确认结项", phase: mcodeui.StewardTopicCompleted, want: 1},
		{name: "已关闭不计", phase: mcodeui.StewardTopicCompleted, reviewTasks: 3, closed: true, want: 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := stewardTopicPendingUser(c.phase, c.brief, c.openQuestions, c.reviewTasks, c.closed)
			if got != c.want {
				t.Fatalf("PendingUser = %d，期望 %d", got, c.want)
			}
		})
	}
}

// TestStewardTopicTitle 覆盖标题取首行并截到 30 个字。
func TestStewardTopicTitle(t *testing.T) {
	if got := stewardTopicTitle("  第一行\n第二行"); got != "第一行" {
		t.Fatalf("标题 = %q，期望首行", got)
	}
	long := strings.Repeat("长", 40)
	if got := stewardTopicTitle(long); got != strings.Repeat("长", 30) {
		t.Fatalf("超长标题应截到 30 个字，实际 %d 个字", len([]rune(got)))
	}
	if got := stewardTopicTitle("短需求"); got != "短需求" {
		t.Fatalf("标题 = %q，期望原样", got)
	}
}

// TestClientStewardTopicsNotWired 覆盖未接线：返回空列表且不报错，界面据此隐藏卡片。
func TestClientStewardTopicsNotWired(t *testing.T) {
	topics, err := (&Client{}).StewardTopics(context.Background())
	if err != nil || len(topics) != 0 {
		t.Fatalf("未接线时应返回空列表，实际 topics=%v err=%v", topics, err)
	}
}

// TestClientStewardTopicsDrafting 覆盖没有看板时的话题列表：阶段按追问中、待办按任务书就绪算。
func TestClientStewardTopicsDrafting(t *testing.T) {
	updated := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	rt := &fakeTopicRuntime{topics: []steward.Topic{
		{
			ThreadID:  "topic-1",
			ProjectID: "proj-1",
			Title:     "整理登录流程",
			Brief:     "## 目标\n把登录流程理顺",
			UpdatedAt: updated,
		},
		{
			ThreadID:      "topic-2",
			ProjectID:     "proj-1",
			Title:         "写文档",
			OpenQuestions: 1,
			UpdatedAt:     updated.Add(-time.Hour),
		},
	}}
	client := &Client{}
	client.SetSteward(rt)

	topics, err := client.StewardTopics(context.Background())
	if err != nil {
		t.Fatalf("读话题失败: %v", err)
	}
	if len(topics) != 2 {
		t.Fatalf("应当返回两条话题，实际 %d 条", len(topics))
	}
	first := topics[0]
	if first.ThreadID != "topic-1" || first.Title != "整理登录流程" {
		t.Errorf("话题字段不对: %+v", first)
	}
	if first.Phase != mcodeui.StewardTopicDrafting {
		t.Errorf("阶段 = %q，期望 %q", first.Phase, mcodeui.StewardTopicDrafting)
	}
	if first.PendingUser != 1 {
		t.Errorf("任务书就绪时 PendingUser 应为 1，实际 %d", first.PendingUser)
	}
	if first.TasksTotal != 0 || first.TasksDone != 0 || first.GoalID != "" {
		t.Errorf("没有目标时不该有任务计数: %+v", first)
	}
	if topics[1].PendingUser != 0 {
		t.Errorf("还有待定问题时 PendingUser 应为 0，实际 %d", topics[1].PendingUser)
	}
}

// TestClientStewardTopicsError 覆盖读取失败：错误要向上返回，界面才能提示。
func TestClientStewardTopicsError(t *testing.T) {
	boom := errors.New("设置读不到")
	rt := &fakeTopicRuntime{topicsErr: boom}
	client := &Client{}
	client.SetSteward(rt)
	if _, err := client.StewardTopics(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("应返回原始错误，实际 %v", err)
	}
}

// TestClientStartStewardTopic 覆盖开话题：标题取需求首行，参数原样传下去。
func TestClientStartStewardTopic(t *testing.T) {
	rt := &fakeTopicRuntime{}
	client := &Client{}
	client.SetSteward(rt)

	threadID, err := client.StartStewardTopic(context.Background(), "proj-1", "把登录流程理顺\n顺便补错误提示")
	if err != nil {
		t.Fatalf("开话题失败: %v", err)
	}
	if threadID != "topic-new" {
		t.Fatalf("话题会话 ID = %q，期望 topic-new", threadID)
	}
	if rt.openedProject != "proj-1" || rt.openedTitle != "把登录流程理顺" {
		t.Errorf("开话题参数不对: project=%q title=%q", rt.openedProject, rt.openedTitle)
	}
	if rt.openedRequest != "把登录流程理顺\n顺便补错误提示" {
		t.Errorf("需求原文应原样传下去，实际 %q", rt.openedRequest)
	}

	if _, err := client.StartStewardTopic(context.Background(), "proj-1", "   "); err == nil {
		t.Error("空需求应当报错")
	}
	if _, err := (&Client{}).StartStewardTopic(context.Background(), "proj-1", "需求"); err == nil {
		t.Error("未接线时应当报错")
	}
}

// TestClientConfirmAndCloseStewardTopic 覆盖确认开工与关闭话题都转发给运行时。
func TestClientConfirmAndCloseStewardTopic(t *testing.T) {
	rt := &fakeTopicRuntime{}
	client := &Client{}
	client.SetSteward(rt)

	if err := client.ConfirmStewardTopic(context.Background(), "topic-1"); err != nil {
		t.Fatalf("确认开工失败: %v", err)
	}
	if len(rt.confirmed) != 1 || rt.confirmed[0] != "topic-1" {
		t.Errorf("确认开工没有转发: %+v", rt.confirmed)
	}
	if err := client.CloseStewardTopic(context.Background(), "topic-1"); err != nil {
		t.Fatalf("关闭话题失败: %v", err)
	}
	if len(rt.closed) != 1 || rt.closed[0] != "topic-1" {
		t.Errorf("关闭话题没有转发: %+v", rt.closed)
	}

	if err := (&Client{}).ConfirmStewardTopic(context.Background(), "topic-1"); err == nil {
		t.Error("未接线时确认开工应当报错")
	}
	if err := (&Client{}).CloseStewardTopic(context.Background(), "topic-1"); err == nil {
		t.Error("未接线时关闭话题应当报错")
	}
}

// TestClientStartStewardTopicGoal 覆盖卡片上的「直接开工」转发与未接线报错。
func TestClientStartStewardTopicGoal(t *testing.T) {
	rt := &fakeTopicRuntime{}
	client := &Client{}
	client.SetSteward(rt)

	if err := client.StartStewardTopicGoal(context.Background(), "topic-1"); err != nil {
		t.Fatalf("直接开工失败: %v", err)
	}
	if len(rt.startedGoals) != 1 || rt.startedGoals[0] != "topic-1" {
		t.Errorf("直接开工没有转发: %+v", rt.startedGoals)
	}
	if err := (&Client{}).StartStewardTopicGoal(context.Background(), "topic-1"); err == nil {
		t.Error("未接线时直接开工应当报错")
	}
}

// TestClientWatchStewardTopics 覆盖同时订阅话题与看板，取消后两边都停。
func TestClientWatchStewardTopics(t *testing.T) {
	rt := &fakeTopicRuntime{}
	client := &Client{}
	client.SetSteward(rt)

	cancel := client.WatchStewardTopics(func() {})
	if cancel == nil {
		t.Fatal("订阅应当返回取消函数")
	}
	if rt.watcherCount() != 1 {
		t.Fatalf("应当订阅话题变化一次，实际 %d 次", rt.watcherCount())
	}
	client.kanbanMu.Lock()
	kanbanWatchers := len(client.kanbanWatchers)
	client.kanbanMu.Unlock()
	if kanbanWatchers != 1 {
		t.Fatalf("应当订阅看板变化一次，实际 %d 次", kanbanWatchers)
	}

	cancel()
	if rt.watcherCount() != 0 {
		t.Errorf("取消后话题订阅应当清空，实际 %d 个", rt.watcherCount())
	}
	client.kanbanMu.Lock()
	kanbanWatchers = len(client.kanbanWatchers)
	client.kanbanMu.Unlock()
	if kanbanWatchers != 0 {
		t.Errorf("取消后看板订阅应当清空，实际 %d 个", kanbanWatchers)
	}
}

// TestClientWatchStewardTopicsNotWired 覆盖未接线：仍可订阅看板变化，取消不报错。
func TestClientWatchStewardTopicsNotWired(t *testing.T) {
	client := &Client{}
	cancel := client.WatchStewardTopics(func() {})
	cancel()
	if len(client.kanbanWatchers) != 0 {
		t.Errorf("取消后看板订阅应当清空，实际 %d 个", len(client.kanbanWatchers))
	}
}

// TestClientDeleteStewardTopic 覆盖删除话题：先删话题行再删会话，参数校验与未接线报错。
func TestClientDeleteStewardTopic(t *testing.T) {
	hub := remote.NewStreamHub(remote.StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	backend := remote.NewMemoryBackend(hub)
	defer backend.Close()
	backend.AddThread(wire.ThreadSummary{ID: "topic-1"}, nil)
	backend.AddThread(wire.ThreadSummary{ID: "topic-2"}, nil)

	rt := &fakeTopicRuntime{}
	client := &Client{backend: backend}
	client.SetSteward(rt)
	ctx := context.Background()

	if err := client.DeleteStewardTopic(ctx, "topic-1"); err != nil {
		t.Fatalf("删除话题失败: %v", err)
	}
	if len(rt.deleted) != 1 || rt.deleted[0] != "topic-1" {
		t.Errorf("删除话题没有转发: %+v", rt.deleted)
	}
	if _, err := backend.GetThread(ctx, "topic-1"); err == nil {
		t.Error("话题对应的会话应当一并删除")
	}

	// 话题删除失败时不能删会话：顺序必须是先话题后会话。
	rt.deleteErr = errors.New("话题删不掉")
	if err := client.DeleteStewardTopic(ctx, "topic-2"); err == nil {
		t.Fatal("话题删除失败时应当向上返回错误")
	}
	if _, err := backend.GetThread(ctx, "topic-2"); err != nil {
		t.Errorf("话题没删掉时会话必须保留: %v", err)
	}
	rt.deleteErr = nil

	if err := client.DeleteStewardTopic(ctx, "   "); err == nil {
		t.Error("空话题 ID 应当报错")
	}
	if err := (&Client{}).DeleteStewardTopic(ctx, "topic-1"); err == nil {
		t.Error("未接线时删除话题应当报错")
	}
}

// TestClientReplanStewardTopic 覆盖重新规划转发与未接线报错。
func TestClientReplanStewardTopic(t *testing.T) {
	rt := &fakeTopicRuntime{}
	client := &Client{}
	client.SetSteward(rt)

	if err := client.ReplanStewardTopic(context.Background(), "topic-1"); err != nil {
		t.Fatalf("重新规划失败: %v", err)
	}
	if len(rt.replanned) != 1 || rt.replanned[0] != "topic-1" {
		t.Errorf("重新规划没有转发: %+v", rt.replanned)
	}
	if err := (&Client{}).ReplanStewardTopic(context.Background(), "topic-1"); err == nil {
		t.Error("未接线时重新规划应当报错")
	}
}

// TestStewardTopicPendingUserCountsPlanFailed 覆盖规划失败也计入待用户处理。
func TestStewardTopicPendingUserCountsPlanFailed(t *testing.T) {
	if got := stewardTopicPendingUser(steward.PhasePlanFailed, "## 目标\n做点什么", 0, 0, false); got != 1 {
		t.Fatalf("规划失败的 PendingUser = %d，期望 1", got)
	}
	if got := stewardTopicPendingUser(steward.PhasePlanFailed, "## 目标\n做点什么", 0, 0, true); got != 0 {
		t.Fatalf("已关闭话题的 PendingUser = %d，期望 0", got)
	}
}
