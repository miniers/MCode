package app

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/kanban"
)

// fakeWakeStore 是内存版设置存储，用来验证唤醒去重键与日报日期。
type fakeWakeStore struct {
	mu    sync.Mutex
	items map[string]string
}

func newFakeWakeStore() *fakeWakeStore {
	return &fakeWakeStore{items: make(map[string]string)}
}

func (s *fakeWakeStore) GetSetting(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.items[key]
	return value, ok, nil
}

func (s *fakeWakeStore) SetSetting(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[key] = value
	return nil
}

func (s *fakeWakeStore) get(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.items[key]
	return value, ok
}

// fakeHost 记录投递给总管的宿主事件。
type fakeHost struct {
	mu   sync.Mutex
	sent []fakeHostEvent
}

type fakeHostEvent struct {
	threadID string
	text     string
	// kind 记录走哪条入口：host_event 是可见的宿主事件，steward_wake 是隐藏的总管唤醒。
	kind string
}

func (h *fakeHost) SendHostEvent(_ context.Context, threadID, text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sent = append(h.sent, fakeHostEvent{threadID: threadID, text: text, kind: "host_event"})
	return nil
}

// SendStewardWake 记录隐藏唤醒投递；总管唤醒已全部改走这条入口。
func (h *fakeHost) SendStewardWake(_ context.Context, threadID, text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sent = append(h.sent, fakeHostEvent{threadID: threadID, text: text, kind: "steward_wake"})
	return nil
}

// wakes 返回走隐藏入口（steward_wake）的投递，供断言唤醒不再走可见的宿主事件。
func (h *fakeHost) wakes() []fakeHostEvent {
	var out []fakeHostEvent
	for _, ev := range h.events() {
		if ev.kind == "steward_wake" {
			out = append(out, ev)
		}
	}
	return out
}

func (h *fakeHost) events() []fakeHostEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]fakeHostEvent, len(h.sent))
	copy(out, h.sent)
	return out
}

func testWakeTargets() map[string]goalWakeTarget {
	return map[string]goalWakeTarget{
		"goal-1": {ThreadID: "thread-plan", Title: "目标一"},
	}
}

// TestDiffGoalWake 验证唤醒判定：任务新进入 review/needs_approval/rejected、目标进入 plan_review、
// 目标暂停与任务全部完成时各产生一条动作；状态没变化时不重复唤醒。
func TestDiffGoalWake(t *testing.T) {
	targets := testWakeTargets()
	task := func(status string, revision int) map[string]goalTaskSnapshot {
		return map[string]goalTaskSnapshot{
			"task-1": {GoalID: "goal-1", Title: "任务一", Status: status, Revision: revision},
		}
	}

	cases := []struct {
		name      string
		prevTasks map[string]goalTaskSnapshot
		nextTasks map[string]goalTaskSnapshot
		prevGoals map[string]goalSnapshot
		nextGoals map[string]goalSnapshot
		wantKind  goalWakeKind
		wantNone  bool
	}{
		{
			name:      "任务进入验收",
			prevTasks: task(string(kanban.TaskStatusInProgress), 2),
			nextTasks: task(string(kanban.TaskStatusReview), 3),
			wantKind:  goalWakeReview,
		},
		{
			name:      "验收状态未变化不唤醒",
			prevTasks: task(string(kanban.TaskStatusReview), 3),
			nextTasks: task(string(kanban.TaskStatusReview), 3),
			wantNone:  true,
		},
		{
			name:      "任务重试耗尽被打回",
			prevTasks: task(string(kanban.TaskStatusReview), 3),
			nextTasks: task(string(kanban.TaskStatusRejected), 4),
			wantKind:  goalWakeRejected,
		},
		{
			name:      "任务等待审批",
			prevTasks: task(string(kanban.TaskStatusInProgress), 5),
			nextTasks: task(string(kanban.TaskStatusNeedsApproval), 6),
			wantKind:  goalWakeNeedsApproval,
		},
		{
			name:      "计划待审批首次出现",
			prevGoals: nil,
			nextGoals: map[string]goalSnapshot{"goal-1": {Status: string(kanban.GoalStatusPlanReview), Revision: 1}},
			wantKind:  goalWakePlanReview,
		},
		{
			name:      "计划待审批同状态同 Revision 不重复",
			prevGoals: map[string]goalSnapshot{"goal-1": {Status: string(kanban.GoalStatusPlanReview), Revision: 3}},
			nextGoals: map[string]goalSnapshot{"goal-1": {Status: string(kanban.GoalStatusPlanReview), Revision: 3}},
			wantNone:  true,
		},
		{
			name:      "计划待审批转入运行后不再唤醒",
			prevGoals: map[string]goalSnapshot{"goal-1": {Status: string(kanban.GoalStatusPlanReview), Revision: 3}},
			nextGoals: map[string]goalSnapshot{"goal-1": {Status: string(kanban.GoalStatusRunning), Revision: 4}},
			wantNone:  true,
		},
		{
			name:      "目标暂停",
			prevGoals: map[string]goalSnapshot{"goal-1": {Status: string(kanban.GoalStatusRunning), Total: 2, Done: 0}},
			nextGoals: map[string]goalSnapshot{"goal-1": {Status: string(kanban.GoalStatusPaused), Total: 2, Done: 1}},
			wantKind:  goalWakeGoalPaused,
		},
		{
			name:      "目标任务全部完成",
			prevGoals: map[string]goalSnapshot{"goal-1": {Status: string(kanban.GoalStatusRunning), Total: 2, Done: 1}},
			nextGoals: map[string]goalSnapshot{"goal-1": {Status: string(kanban.GoalStatusRunning), Total: 2, Done: 2}},
			wantKind:  goalWakeGoalDone,
		},
		{
			name:      "目标完成",
			prevGoals: map[string]goalSnapshot{"goal-1": {Status: string(kanban.GoalStatusRunning), Total: 2, Done: 1}},
			nextGoals: map[string]goalSnapshot{"goal-1": {Status: string(kanban.GoalStatusCompleted), Total: 2, Done: 2}},
			wantKind:  goalWakeGoalDone,
		},
		{
			name:      "全部完成后状态未变化不重复",
			prevGoals: map[string]goalSnapshot{"goal-1": {Status: string(kanban.GoalStatusRunning), Total: 2, Done: 2}},
			nextGoals: map[string]goalSnapshot{"goal-1": {Status: string(kanban.GoalStatusRunning), Total: 2, Done: 2}},
			wantNone:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actions := diffGoalWake(tc.prevTasks, tc.nextTasks, tc.prevGoals, tc.nextGoals, targets)
			if tc.wantNone {
				if len(actions) != 0 {
					t.Fatalf("期望没有动作，实际 %+v", actions)
				}
				return
			}
			if len(actions) != 1 {
				t.Fatalf("期望 1 条动作，实际 %d 条：%+v", len(actions), actions)
			}
			if actions[0].Kind != tc.wantKind {
				t.Fatalf("动作类型 = %s, 期望 %s", actions[0].Kind, tc.wantKind)
			}
			if actions[0].ThreadID != "thread-plan" {
				t.Fatalf("投递会话 = %q, 期望 thread-plan", actions[0].ThreadID)
			}
		})
	}
}

// TestDiffGoalWakePlanReview 验证计划待审批的去重键与取值：重启后首轮快照为空也要补发一次，
// 同一目标同一 Revision 由 claim 落库去重。
func TestDiffGoalWakePlanReview(t *testing.T) {
	nextGoals := map[string]goalSnapshot{"goal-1": {Status: string(kanban.GoalStatusPlanReview), Revision: 7}}
	actions := diffGoalWake(nil, nil, nil, nextGoals, testWakeTargets())
	if len(actions) != 1 {
		t.Fatalf("期望 1 条动作，实际 %d 条：%+v", len(actions), actions)
	}
	act := actions[0]
	if act.Kind != goalWakePlanReview {
		t.Fatalf("动作类型 = %s, 期望 %s", act.Kind, goalWakePlanReview)
	}
	if want := settingGoalWakePlanPrefix + "goal-1"; act.SettingKey != want {
		t.Fatalf("去重键 = %q, 期望 %q", act.SettingKey, want)
	}
	if act.SettingValue != "plan_review@7" {
		t.Fatalf("去重值 = %q, 期望 plan_review@7", act.SettingValue)
	}
	if act.ThreadID != "thread-plan" {
		t.Fatalf("投递会话 = %q, 期望 thread-plan", act.ThreadID)
	}
}

// TestBuildGoalWakeTextPlanReview 验证计划待审批的正文引导总管自审并开工，拿不准时问用户。
func TestBuildGoalWakeTextPlanReview(t *testing.T) {
	text := buildGoalWakeText(goalWakeAction{Kind: goalWakePlanReview, GoalID: "goal-1", GoalTitle: "目标一"})
	if !strings.Contains(text, "steward_start_goal") {
		t.Fatalf("正文应引导调用 steward_start_goal: %q", text)
	}
	if !strings.Contains(text, "notify_owner") {
		t.Fatalf("正文应引导拿不准时 notify_owner: %q", text)
	}
}

// TestBuildGoalWakeTextGoalDone 验证结项报告写完要求发一条一句话的摘要通知。
func TestBuildGoalWakeTextGoalDone(t *testing.T) {
	text := buildGoalWakeText(goalWakeAction{Kind: goalWakeGoalDone, GoalID: "goal-1", GoalTitle: "目标一", ThreadID: "thread-topic-1"})
	if strings.Contains(text, "不要 notify_owner") {
		t.Fatalf("结项正文不应再禁止 notify_owner: %q", text)
	}
	if !strings.Contains(text, "notify_owner") {
		t.Fatalf("结项正文应要求用 notify_owner 发摘要通知: %q", text)
	}
	if !strings.Contains(text, "目标一") || !strings.Contains(text, "thread-topic-1") {
		t.Fatalf("结项正文应以话题名作标题、带上会话 ID: %q", text)
	}
}

// TestDiffGoalWakeSkipsOtherGoals 验证不是总管创建（或没有话题会话）的目标不产生唤醒。
func TestDiffGoalWakeSkipsOtherGoals(t *testing.T) {
	nextTasks := map[string]goalTaskSnapshot{
		"task-1": {GoalID: "goal-plain", Title: "任务一", Status: string(kanban.TaskStatusReview), Revision: 2},
	}
	if actions := diffGoalWake(nil, nextTasks, nil, nil, testWakeTargets()); len(actions) != 0 {
		t.Fatalf("普通目标不应唤醒总管，实际 %+v", actions)
	}
}

// TestDiffGoalReceipt 验证目标进入 completed 或 cancelled 时产出一次回执目标，
// 状态没变化或不在总管目标表里的不产出；没有规划会话的目标也跳过。
func TestDiffGoalReceipt(t *testing.T) {
	targets := map[string]goalWakeTarget{
		"goal-1": {ThreadID: "thread-topic", Title: "目标一"},
		"goal-2": {ThreadID: "thread-other", Title: "目标二"},
		"goal-3": {Title: "没有会话的目标"},
	}
	prev := map[string]goalSnapshot{
		"goal-1": {Status: string(kanban.GoalStatusRunning)},
		"goal-2": {Status: string(kanban.GoalStatusCompleted)},
	}
	next := map[string]goalSnapshot{
		"goal-1": {Status: string(kanban.GoalStatusCompleted)},
		"goal-2": {Status: string(kanban.GoalStatusCompleted)},
		"goal-3": {Status: string(kanban.GoalStatusCancelled)},
	}
	got := diffGoalReceipt(prev, next, targets)
	if len(got) != 1 || got[0] != "thread-topic" {
		t.Fatalf("应当只为新完成的话题回执，实际 %+v", got)
	}

	// 重启后首轮快照为空：已完成的话题仍要试一次，由话题上的回执标记去重。
	if got := diffGoalReceipt(nil, map[string]goalSnapshot{
		"goal-1": {Status: string(kanban.GoalStatusCancelled)},
	}, targets); len(got) != 1 || got[0] != "thread-topic" {
		t.Fatalf("首轮扫描应当补发回执，实际 %+v", got)
	}

	// 目标还在跑时不回执。
	if got := diffGoalReceipt(nil, map[string]goalSnapshot{
		"goal-1": {Status: string(kanban.GoalStatusRunning)},
	}, targets); len(got) != 0 {
		t.Fatalf("未完成的目标不应回执，实际 %+v", got)
	}
}

// TestGoalReviewWakeDedup 验证去重：同一「状态 + Revision」只发一次，重启后靠设置键兜底。
func TestGoalReviewWakeDedup(t *testing.T) {
	store := newFakeWakeStore()
	w := newGoalReviewWaker(goalReviewWakeOptions{Store: store, Host: &fakeHost{}})

	key := settingGoalWakePrefix + "task-1"
	if !w.claim(key, "review@3") {
		t.Fatal("首次唤醒应放行")
	}
	if w.claim(key, "review@3") {
		t.Fatal("同一状态与 Revision 不应重复唤醒")
	}
	if !w.claim(key, "rejected@4") {
		t.Fatal("状态变化后应重新唤醒")
	}

	// 模拟投递成功后的落库，以及随后的重启：新实例内存为空，但设置键里已有记录。
	if err := store.SetSetting(context.Background(), key, "rejected@4"); err != nil {
		t.Fatalf("写入设置键失败: %v", err)
	}
	restarted := newGoalReviewWaker(goalReviewWakeOptions{Store: store, Host: &fakeHost{}})
	if restarted.claim(key, "rejected@4") {
		t.Fatal("重启后不应重复发送已记录的事件")
	}
	if !restarted.claim(key, "review@5") {
		t.Fatal("重启后新事件仍应发送")
	}
}

// TestGoalReviewWakeMergeThrottle 验证同一目标 30 秒内的多条事件合并成一条宿主事件。
func TestGoalReviewWakeMergeThrottle(t *testing.T) {
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.Local)
	host := &fakeHost{}
	w := newGoalReviewWaker(goalReviewWakeOptions{
		Store: newFakeWakeStore(),
		Host:  host,
		Now:   func() time.Time { return now },
	})

	w.enqueue("goal-1", "thread-plan", goalWakeItem{text: "事件一"})
	if got := len(host.events()); got != 1 {
		t.Fatalf("首条事件应立即发送，实际发送 %d 条", got)
	}

	w.enqueue("goal-1", "thread-plan", goalWakeItem{text: "事件二"})
	w.enqueue("goal-1", "thread-plan", goalWakeItem{text: "事件三"})
	now = now.Add(10 * time.Second)
	w.flushDue(now)
	if got := len(host.events()); got != 1 {
		t.Fatalf("合并窗口内不应发送，实际发送 %d 条", got)
	}

	now = now.Add(goalWakeMinGap)
	w.flushDue(now)
	events := host.events()
	if len(events) != 2 {
		t.Fatalf("窗口结束后应合并发送 1 条，累计 2 条，实际 %d 条", len(events))
	}
	if !strings.Contains(events[1].text, "事件二") || !strings.Contains(events[1].text, "事件三") {
		t.Fatalf("合并后的事件应包含两条正文: %q", events[1].text)
	}
	if events[1].threadID != "thread-plan" {
		t.Fatalf("投递会话 = %q, 期望 thread-plan", events[1].threadID)
	}
}

// TestGoalReviewWakeUsesHiddenKind 验证验收唤醒与日报都走隐藏入口 SendStewardWake，不走可见的 SendHostEvent。
func TestGoalReviewWakeUsesHiddenKind(t *testing.T) {
	reportTime := time.Date(2026, 10, 30, 9, 30, 0, 0, time.Local)
	host := &fakeHost{}
	w := newGoalReviewWaker(goalReviewWakeOptions{
		Store:           newFakeWakeStore(),
		Host:            host,
		Now:             func() time.Time { return reportTime },
		StewardThreadID: func() string { return "thread-steward" },
		ReportSettings: func(time.Time) dailyReportSettings {
			return dailyReportSettings{Enabled: true, Hour: 9, Minute: 0}
		},
	})

	w.enqueue("goal-1", "thread-plan", goalWakeItem{text: "任务进入验收"})
	w.checkDailyReport(reportTime)

	events := host.events()
	if len(events) != 2 {
		t.Fatalf("应投递 2 条（验收唤醒与日报），实际 %d 条", len(events))
	}
	for _, ev := range events {
		if ev.kind != "steward_wake" {
			t.Fatalf("唤醒应走 SendStewardWake，实际 kind = %q", ev.kind)
		}
	}
	if got := len(host.wakes()); got != 2 {
		t.Fatalf("隐藏唤醒条数 = %d, 期望 2", got)
	}
}

// TestGoalReviewWakeDailyReport 验证日报触发条件：开关、时刻、安静时段、总管会话与当天去重。
func TestGoalReviewWakeDailyReport(t *testing.T) {
	reportTime := time.Date(2026, 10, 30, 9, 30, 0, 0, time.Local)

	cases := []struct {
		name     string
		settings dailyReportSettings
		now      time.Time
		threadID string
		want     bool
	}{
		{
			name:     "默认关闭不发送",
			settings: dailyReportSettings{Enabled: false, Hour: 9, Minute: 0},
			now:      reportTime,
			threadID: "thread-steward",
			want:     false,
		},
		{
			name:     "未到时刻不发送",
			settings: dailyReportSettings{Enabled: true, Hour: 18, Minute: 0},
			now:      reportTime,
			threadID: "thread-steward",
			want:     false,
		},
		{
			name:     "安静时段推迟",
			settings: dailyReportSettings{Enabled: true, Hour: 9, Minute: 0, Quiet: true},
			now:      reportTime,
			threadID: "thread-steward",
			want:     false,
		},
		{
			name:     "总管会话未就绪推迟",
			settings: dailyReportSettings{Enabled: true, Hour: 9, Minute: 0},
			now:      reportTime,
			threadID: "",
			want:     false,
		},
		{
			name:     "到点发送",
			settings: dailyReportSettings{Enabled: true, Hour: 9, Minute: 0},
			now:      reportTime,
			threadID: "thread-steward",
			want:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeWakeStore()
			host := &fakeHost{}
			w := newGoalReviewWaker(goalReviewWakeOptions{
				Store:           store,
				Host:            host,
				StewardThreadID: func() string { return tc.threadID },
				ReportSettings:  func(time.Time) dailyReportSettings { return tc.settings },
			})
			w.checkDailyReport(tc.now)
			events := host.events()
			if got := len(events) > 0; got != tc.want {
				t.Fatalf("发送 = %v, 期望 %v", got, tc.want)
			}
			if !tc.want {
				return
			}
			if events[0].threadID != "thread-steward" {
				t.Fatalf("日报会话 = %q, 期望 thread-steward", events[0].threadID)
			}
			if strings.Contains(events[0].text, "不要 notify_owner") {
				t.Fatalf("日报正文不应再禁止 notify_owner: %q", events[0].text)
			}
			if !strings.Contains(events[0].text, "notify_owner") || !strings.Contains(events[0].text, "今日要点") {
				t.Fatalf("日报正文应要求用 notify_owner 发一条今日要点摘要: %q", events[0].text)
			}
			if !strings.Contains(events[0].text, "thread-steward") {
				t.Fatalf("日报正文应带上总管会话 ID 作为通知跳转目标: %q", events[0].text)
			}
			// 同一天再检查不应重复发送。
			w.checkDailyReport(tc.now.Add(time.Minute))
			if got := len(host.events()); got != 1 {
				t.Fatalf("同一天重复发送了日报，共 %d 条", got)
			}
			if day, ok := store.get(settingDailyReportDay); !ok || day != tc.now.Format(dailyReportDateLayout) {
				t.Fatalf("日报日期未落库: %q %v", day, ok)
			}
		})
	}
}

// TestGoalReviewWakeDailyReportOnLoopStart 验证启动后不必等一分钟的 ticker：
// loop 进入循环前先检查一次日报，满足条件的场景立即投递。
func TestGoalReviewWakeDailyReportOnLoopStart(t *testing.T) {
	reportTime := time.Date(2026, 10, 30, 9, 30, 0, 0, time.Local)
	store := newFakeWakeStore()
	host := &fakeHost{}
	w := newGoalReviewWaker(goalReviewWakeOptions{
		Store:           store,
		Host:            host,
		Now:             func() time.Time { return reportTime },
		StewardThreadID: func() string { return "thread-steward" },
		ReportSettings: func(time.Time) dailyReportSettings {
			return dailyReportSettings{Enabled: true, Hour: 9, Minute: 0}
		},
	})
	w.wg.Add(1)
	go w.loop()
	defer w.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for len(host.events()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("启动后没有立即发送日报，等到了 ticker")
		}
		time.Sleep(5 * time.Millisecond)
	}
	events := host.events()
	if len(events) != 1 {
		t.Fatalf("日报条数 = %d, 期望 1", len(events))
	}
	if events[0].threadID != "thread-steward" {
		t.Fatalf("日报会话 = %q, 期望 thread-steward", events[0].threadID)
	}
	if strings.Contains(events[0].text, "不要 notify_owner") {
		t.Fatalf("日报正文不应再禁止 notify_owner: %q", events[0].text)
	}
	if !strings.Contains(events[0].text, "notify_owner") || !strings.Contains(events[0].text, "今日要点") {
		t.Fatalf("日报正文应要求用 notify_owner 发一条今日要点摘要: %q", events[0].text)
	}
}

// waitAutoCloseCalls 等自动结项的后台调用达到期望次数，超时即失败。
func waitAutoCloseCalls(t *testing.T, mu *sync.Mutex, calls *int, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := *calls
		mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	got := *calls
	mu.Unlock()
	t.Fatalf("自动结项触发次数 = %d，期望 %d", got, want)
}

// TestGoalReviewWakeAutoCloseTopicsHourly 验证话题自动结项一小时内最多触发一次。
func TestGoalReviewWakeAutoCloseTopicsHourly(t *testing.T) {
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.Local)
	var mu sync.Mutex
	calls := 0
	w := newGoalReviewWaker(goalReviewWakeOptions{
		Store: newFakeWakeStore(),
		Host:  &fakeHost{},
		Now:   func() time.Time { return now },
		AutoCloseTopics: func(context.Context) (int, error) {
			mu.Lock()
			calls++
			mu.Unlock()
			return 0, nil
		},
	})

	w.maybeAutoCloseTopics()
	waitAutoCloseCalls(t, &mu, &calls, 1)

	// 一小时内再扫描多次都不应重复触发。
	now = now.Add(30 * time.Minute)
	w.maybeAutoCloseTopics()
	now = now.Add(29 * time.Minute)
	w.maybeAutoCloseTopics()
	waitAutoCloseCalls(t, &mu, &calls, 1)

	// 满一小时后放行一次。
	now = now.Add(2 * time.Minute)
	w.maybeAutoCloseTopics()
	waitAutoCloseCalls(t, &mu, &calls, 2)

	// 没接自动结项时不做任何事（nil 安全）。
	plain := newGoalReviewWaker(goalReviewWakeOptions{Store: newFakeWakeStore(), Host: &fakeHost{}})
	plain.maybeAutoCloseTopics()
}
