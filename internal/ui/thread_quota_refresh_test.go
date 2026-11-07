package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestModel_TurnEndRefreshesThreadQuota 校验 pi 线程从运行中变为空闲时，先按会话
// 让网关重读本会话实际应答账号的额度，然后再强制重取面板数据。
func TestModel_TurnEndRefreshesThreadQuota(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	state := newChatState(fake)
	t.Cleanup(state.close)

	now := time.Now()
	th := Thread{
		ID:        "t1",
		ProjectID: "p1",
		Title:     "会话",
		UpdatedAt: now,
		IsRunning: true,
		Provider:  "pi",
		Model:     "magpie-jp/auto/deepseek-v4.1-flash",
	}
	state.mu.Lock()
	state.threads = []Thread{th}
	state.threadsByProj["p1"] = []Thread{th}
	state.selectedThreadID = "t1"
	state.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {}, 400, 300)

	ended := th
	ended.IsRunning = false
	ended.UpdatedAt = time.Now()
	state.onWatchUpdate(ThreadUpdate{ThreadID: th.ID, Thread: ended})

	waitFor(t, tt, func() bool { return len(fake.RefreshThreadQuotaCalls()) >= 1 })
	calls := fake.RefreshThreadQuotaCalls()
	if calls[0].ThreadID != "t1" || calls[0].Provider != "pi:magpie-jp" {
		t.Fatalf("RefreshThreadQuota 参数 = %+v，想要 t1/pi:magpie-jp", calls[0])
	}

	waitFor(t, tt, func() bool {
		for _, provider := range fake.ProviderQuotaCalls() {
			if provider == "pi:magpie-jp" {
				return true
			}
		}
		return false
	})
}

// TestModel_TurnEndRefreshesClaudeQuota 校验 claude 线程从运行中变为空闲时，同样按会话
// 让网关重读额度，并把强制重取发给 claude。
func TestModel_TurnEndRefreshesClaudeQuota(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	state := newChatState(fake)
	t.Cleanup(state.close)

	now := time.Now()
	th := Thread{
		ID:        "t1",
		ProjectID: "p1",
		Title:     "会话",
		UpdatedAt: now,
		IsRunning: true,
		Provider:  "claude",
		Model:     "claude-sonnet-4",
	}
	state.mu.Lock()
	state.threads = []Thread{th}
	state.threadsByProj["p1"] = []Thread{th}
	state.selectedThreadID = "t1"
	state.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {}, 400, 300)

	ended := th
	ended.IsRunning = false
	ended.UpdatedAt = time.Now()
	state.onWatchUpdate(ThreadUpdate{ThreadID: th.ID, Thread: ended})

	waitFor(t, tt, func() bool { return len(fake.RefreshThreadQuotaCalls()) >= 1 })
	calls := fake.RefreshThreadQuotaCalls()
	if calls[0].ThreadID != "t1" || calls[0].Provider != "claude" {
		t.Fatalf("RefreshThreadQuota 参数 = %+v，想要 t1/claude", calls[0])
	}

	waitFor(t, tt, func() bool {
		for _, provider := range fake.ProviderQuotaCalls() {
			if provider == "claude" {
				return true
			}
		}
		return false
	})
}
