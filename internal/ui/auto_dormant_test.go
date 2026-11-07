package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// waitForAutoDormant 轮询等待异步休眠（fake client 的调用在 goroutine 中完成）。
func waitForAutoDormant(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within deadline")
}

// 1. 候选筛选：只有超期且未休眠/未置顶/未运行/无待审批的会话入选；days<=0 关闭。
func TestAutoDormantCandidates(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-8 * 24 * time.Hour)
	fresh := now.Add(-2 * 24 * time.Hour)
	boundary := now.Add(-7 * 24 * time.Hour) // 恰好 7 天，不算超期

	threads := []Thread{
		{ID: "stale", UpdatedAt: stale},
		{ID: "fresh", UpdatedAt: fresh},
		{ID: "boundary", UpdatedAt: boundary},
		{ID: "pinned", Pinned: true, UpdatedAt: stale},
		{ID: "dormant", Dormant: true, UpdatedAt: stale},
		{ID: "running", IsRunning: true, UpdatedAt: stale},
		{ID: "swarm-busy", SwarmBusy: true, UpdatedAt: stale}, // 主会话空闲但子线程未结束
		{ID: "pending", HasPendingRequest: true, UpdatedAt: stale},
		{ID: "no-time"}, // UpdatedAt 零值：无法判定空闲时长，跳过
	}

	got := autoDormantCandidates(threads, 7, now)
	if len(got) != 1 || got[0] != "stale" {
		t.Fatalf("autoDormantCandidates(days=7) = %v, want [stale]", got)
	}

	// 关闭：0 与负数都返回空
	if got := autoDormantCandidates(threads, 0, now); len(got) != 0 {
		t.Fatalf("autoDormantCandidates(days=0) = %v, want empty", got)
	}
	if got := autoDormantCandidates(threads, -1, now); len(got) != 0 {
		t.Fatalf("autoDormantCandidates(days=-1) = %v, want empty", got)
	}

	// 阈值放大到 30 天：stale 也不再入选
	if got := autoDormantCandidates(threads, 30, now); len(got) != 0 {
		t.Fatalf("autoDormantCandidates(days=30) = %v, want empty", got)
	}
}

// 2. 节流：1 小时内的第二次调用不重复扫描，超过 1 小时后恢复扫描。
func TestMaybeAutoDormantThrottle(t *testing.T) {
	fake := NewFakeClient()
	now := time.Now()
	old := now.Add(-10 * 24 * time.Hour)

	state := newChatState(fake)
	state.threads = []Thread{
		{ID: "t-old", ProjectID: "p1", UpdatedAt: old},
	}

	// 首次扫描：超期会话被休眠
	state.maybeAutoDormant(now, 7)
	waitForAutoDormant(t, func() bool {
		calls := fake.SetThreadDormantCalls()
		return len(calls) == 1 && calls[0].ThreadID == "t-old" && calls[0].Dormant
	})
	waitForAutoDormant(t, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		for _, th := range state.threads {
			if th.ID == "t-old" {
				return th.Dormant
			}
		}
		return false
	})

	// 新出现的超期会话，但距上次扫描仅 30 分钟：被节流跳过
	state.mu.Lock()
	state.threads = append(state.threads, Thread{ID: "t-new-old", ProjectID: "p1", UpdatedAt: old})
	state.mu.Unlock()

	state.maybeAutoDormant(now.Add(30*time.Minute), 7)
	time.Sleep(50 * time.Millisecond)
	if calls := fake.SetThreadDormantCalls(); len(calls) != 1 {
		t.Fatalf("second scan within an hour should be throttled, got %d calls", len(calls))
	}

	// 超过 1 小时后：恢复扫描并休眠新的超期会话
	state.maybeAutoDormant(now.Add(61*time.Minute), 7)
	waitForAutoDormant(t, func() bool {
		calls := fake.SetThreadDormantCalls()
		return len(calls) == 2 && calls[1].ThreadID == "t-new-old" && calls[1].Dormant
	})
}

// 3. days<=0 时 maybeAutoDormant 不扫描（也不占用节流窗口）。
func TestMaybeAutoDormantDisabled(t *testing.T) {
	fake := NewFakeClient()
	now := time.Now()

	state := newChatState(fake)
	state.threads = []Thread{
		{ID: "t-old", ProjectID: "p1", UpdatedAt: now.Add(-10 * 24 * time.Hour)},
	}

	state.maybeAutoDormant(now, 0)
	time.Sleep(50 * time.Millisecond)
	if calls := fake.SetThreadDormantCalls(); len(calls) != 0 {
		t.Fatalf("days=0 should not settle anything, got %d calls", len(calls))
	}
}

// 4. 会话页的自动休眠设置项：默认显示 7 天，改选后写入 config.json（含关闭的 0）。
func TestSettingsConversation_AutoDormantDaysRow(t *testing.T) {
	resetArchivePageStateForTest()
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)

	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 2000)

	waitFor(t, tt, func() bool {
		return tt.HasText("设置")
	})
	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("会话")
	})
	if err := tt.Click("会话"); err != nil {
		t.Fatalf("click '会话' failed: %v", err)
	}
	settingsRevealInDetail(t, tt, "7 天")

	// 默认 nil 视为 7 天，选择器显示“7 天”
	waitFor(t, tt, func() bool {
		return tt.HasText("自动休眠") && tt.HasText("无消息多少天后自动休眠") && tt.HasText("7 天")
	})

	// 改为 14 天
	if err := settingsClick(t, tt, "7 天"); err != nil {
		t.Fatalf("click select '7 天' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("14 天")
	})
	if err := tt.Click("14 天"); err != nil {
		t.Fatalf("click option '14 天' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		cfg, err := config.Load(dataDir)
		return err == nil && cfg.Archive.SidebarAutoDormantDays != nil && *cfg.Archive.SidebarAutoDormantDays == 14
	})

	// 改为关闭 (0)：0 也要落到磁盘
	if err := settingsClick(t, tt, "14 天"); err != nil {
		t.Fatalf("click select '14 天' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("关闭 (0)")
	})
	if err := tt.Click("关闭 (0)"); err != nil {
		t.Fatalf("click option '关闭 (0)' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		cfg, err := config.Load(dataDir)
		return err == nil && cfg.Archive.SidebarAutoDormantDays != nil && *cfg.Archive.SidebarAutoDormantDays == 0 && cfg.Archive.AutoDormantDays() == 0
	})
}
