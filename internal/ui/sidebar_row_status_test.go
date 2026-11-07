package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestSidebarStatusSlotShowsSingleMarker 验证行尾状态位按优先级只展示一个：
// 待审批 > 未读 > 置顶 > 最近活动。三个标记同时存在时只应看到待审批。
func TestSidebarStatusSlotShowsSingleMarker(t *testing.T) {
	fake := NewFakeClient()
	activity := time.Now().Add(-2 * time.Hour)
	fake.threads["p1"] = []Thread{
		{
			ID:                "t1",
			ProjectID:         "p1",
			Title:             "全部标记都在",
			UpdatedAt:         activity,
			HasPendingRequest: true,
			HasUnread:         true,
			Pinned:            true,
		},
		{
			ID:        "t2",
			ProjectID: "p1",
			Title:     "只有未读和置顶",
			UpdatedAt: activity,
			HasUnread: true,
			Pinned:    true,
		},
		{
			ID:        "t3",
			ProjectID: "p1",
			Title:     "只置顶",
			UpdatedAt: activity,
			Pinned:    true,
		},
		{
			ID:        "t4",
			ProjectID: "p1",
			Title:     "什么都没有",
			UpdatedAt: activity,
		},
	}

	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		return tt.HasText("全部标记都在") && tt.HasText("什么都没有")
	})

	// 待审批行尾是警示三角，带「等你审批」无障碍标签（不再是黄色圆点）。
	if _, ok := tt.Find("等你审批"); !ok {
		t.Fatalf("pending thread should render the 等你审批 triangle label, texts: %q", tt.Texts())
	}

	state.mu.Lock()
	threads := append([]Thread(nil), state.threads...)
	state.mu.Unlock()
	byID := make(map[string]Thread, len(threads))
	for _, th := range threads {
		byID[th.ID] = th
	}

	want := []struct {
		id   string
		kind sidebarStatusSlot
	}{
		{"t1", sidebarStatusPending},
		{"t2", sidebarStatusUnread},
		{"t3", sidebarStatusPinned},
		{"t4", sidebarStatusTime},
	}
	for _, w := range want {
		th, ok := byID[w.id]
		if !ok {
			t.Fatalf("thread %s not loaded", w.id)
		}
		if got := state.sidebarStatusSlotKind(th); got != w.kind {
			t.Fatalf("thread %s slot kind = %v, want %v", w.id, got, w.kind)
		}
	}

	// 有更高优先级的标记时，行尾不再渲染相对时间。
	relTime := formatRelativeTime(activity)
	if relTime == "" {
		t.Fatalf("expected a relative time label for %v", activity)
	}
	if count := countText(tt, relTime); count != 1 {
		t.Fatalf("expected the relative time only on the plain thread (%d found), texts: %q", count, tt.Texts())
	}
}

// TestSidebarStatusSlotPinnedOnly 验证只有置顶标记时，行尾显示置顶而不再显示时间：
// 时间是最低优先级，被置顶挤掉后就完全消失。
func TestSidebarStatusSlotPinnedOnly(t *testing.T) {
	fake := NewFakeClient()
	activity := time.Now().Add(-3 * time.Hour)
	fake.threads["p1"] = []Thread{
		{ID: "t1", ProjectID: "p1", Title: "只置顶的会话", UpdatedAt: activity, Pinned: true},
	}

	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("只置顶的会话") })

	if tt.HasText(formatRelativeTime(activity)) {
		t.Fatalf("pinned thread should hide the relative time, texts: %q", tt.Texts())
	}

	// 取消置顶后时间重新出现，说明两者确实共用同一个位置。
	state.mu.Lock()
	var pinned Thread
	for _, th := range state.threads {
		if th.ID == "t1" {
			pinned = th
		}
	}
	state.mu.Unlock()
	if got := state.sidebarStatusSlotKind(pinned); got != sidebarStatusPinned {
		t.Fatalf("pinned thread slot kind = %v, want %v", got, sidebarStatusPinned)
	}
	pinned.Pinned = false
	if got := state.sidebarStatusSlotKind(pinned); got != sidebarStatusTime {
		t.Fatalf("unpinned thread slot kind = %v, want %v", got, sidebarStatusTime)
	}
}

// TestSidebarStatusSlotExplainsReason 验证待审批与运行中的行尾都给出可读原因：
// 待审批在三角旁显示「等你审批」小字，运行中显示运行时长（不足 1 分钟只说「运行中」）。
func TestSidebarStatusSlotExplainsReason(t *testing.T) {
	fake := NewFakeClient()
	now := time.Now()
	fake.threads["p1"] = []Thread{
		{ID: "t-pending", ProjectID: "p1", Title: "等待审批的会话", UpdatedAt: now, HasPendingRequest: true},
		{ID: "t-running", ProjectID: "p1", Title: "正在运行的会话", UpdatedAt: now, LastUserMessageAt: now.Add(-3 * time.Minute), IsRunning: true},
	}
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		return tt.HasText("等待审批的会话") && tt.HasText("正在运行的会话")
	})

	if !tt.HasText("等你审批") {
		t.Fatalf("pending thread should show 等你审批 text, texts: %q", tt.Texts())
	}
	// 「运行 3 分钟」只可能来自行尾文案，不会和分组头的「运行中  1」或底栏汇总混淆。
	if !tt.HasText("运行 3 分钟") {
		t.Fatalf("running thread should show 运行 3 分钟 text, texts: %q", tt.Texts())
	}

	state.mu.Lock()
	byID := make(map[string]Thread, len(state.threads))
	for _, th := range state.threads {
		byID[th.ID] = th
	}
	state.mu.Unlock()
	if got := state.sidebarStatusSlotKind(byID["t-pending"]); got != sidebarStatusPending {
		t.Fatalf("pending thread slot kind = %v, want %v", got, sidebarStatusPending)
	}
	if got := state.sidebarStatusSlotKind(byID["t-running"]); got != sidebarStatusRunning {
		t.Fatalf("running thread slot kind = %v, want %v", got, sidebarStatusRunning)
	}
}

// TestSidebarRunningLabel 验证运行时长文案：不足 1 分钟或没有起点时间只说「运行中」，
// 起点优先取用户最后一条消息的时间，其次退回最近活动时间。
func TestSidebarRunningLabel(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name string
		th   Thread
		want string
	}{
		{"没有时间", Thread{}, "运行中"},
		{"刚发出不到一分钟", Thread{LastUserMessageAt: now.Add(-20 * time.Second)}, "运行中"},
		{"跑了三分钟", Thread{LastUserMessageAt: now.Add(-3 * time.Minute)}, "运行 3 分钟"},
		{"跑了两个小时", Thread{LastUserMessageAt: now.Add(-2 * time.Hour)}, "运行 2 小时"},
		{"退回最近活动时间", Thread{UpdatedAt: now.Add(-5 * time.Minute)}, "运行 5 分钟"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sidebarRunningLabel(tc.th, now); got != tc.want {
				t.Fatalf("sidebarRunningLabel = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSidebarStatusSlotWaitingBackground 验证「回合已结束但后台任务仍在跑」的会话行尾显示
// 强调色时钟与「后台 N」，且运行中、待审批的优先级不变。
func TestSidebarStatusSlotWaitingBackground(t *testing.T) {
	fake := NewFakeClient()
	now := time.Now()
	fake.threads["p1"] = []Thread{
		{ID: "t-bg-two", ProjectID: "p1", Title: "两个后台任务", UpdatedAt: now, BackgroundTasks: 2},
		{ID: "t-bg-one", ProjectID: "p1", Title: "一个后台任务", UpdatedAt: now, BackgroundTasks: 1},
		{ID: "t-bg-running", ProjectID: "p1", Title: "后台且运行中", UpdatedAt: now, LastUserMessageAt: now.Add(-2 * time.Minute), IsRunning: true, BackgroundTasks: 1},
		{ID: "t-bg-pending", ProjectID: "p1", Title: "后台且待审批", UpdatedAt: now, HasPendingRequest: true, BackgroundTasks: 3},
	}
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		return tt.HasText("两个后台任务") && tt.HasText("后台 2")
	})

	// 行尾用「等待后台任务」作为无障碍标签，按标签即可定位时钟与文案。
	if _, ok := tt.Find("等待后台任务"); !ok {
		t.Fatalf("waiting thread should render the 等待后台任务 label, texts: %q", tt.Texts())
	}
	texts := make(map[string]int)
	for _, tx := range tt.Texts() {
		texts[tx]++
	}
	if texts["后台"] != 1 {
		t.Fatalf("single background task should show 后台 exactly once, texts: %q", tt.Texts())
	}
	if texts["后台 2"] != 1 {
		t.Fatalf("two background tasks should show 后台 2 once, texts: %q", tt.Texts())
	}
	// 运行中、待审批的行尾不显示后台标记，优先级不变。
	if texts["后台 3"] != 0 {
		t.Fatalf("pending thread must not render the waiting marker, texts: %q", tt.Texts())
	}
	if !tt.HasText("运行 2 分钟") {
		t.Fatalf("running thread should still show its running label, texts: %q", tt.Texts())
	}

	state.mu.Lock()
	byID := make(map[string]Thread, len(state.threads))
	for _, th := range state.threads {
		byID[th.ID] = th
	}
	state.mu.Unlock()
	want := map[string]sidebarStatusSlot{
		"t-bg-two":     sidebarStatusWaiting,
		"t-bg-one":     sidebarStatusWaiting,
		"t-bg-running": sidebarStatusRunning,
		"t-bg-pending": sidebarStatusPending,
	}
	for id, kind := range want {
		if got := state.sidebarStatusSlotKind(byID[id]); got != kind {
			t.Fatalf("thread %s slot kind = %v, want %v", id, got, kind)
		}
	}
}
