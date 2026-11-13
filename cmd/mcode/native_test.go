package main

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"mcode/internal/app"
	"mcode/internal/config"
	"mcode/internal/remote"
)

// alertTestConfig 返回关闭安静时段的默认配置：安静时段按本地时钟生效，
// 默认 00:00–06:00 会让夜间运行的这些用例收不到通知。
func alertTestConfig() config.Config {
	cfg := config.Defaults()
	cfg.Orchestration.Steward.QuietStart = ""
	cfg.Orchestration.Steward.QuietEnd = ""
	return cfg
}

func TestDesktopAlertSettings(t *testing.T) {
	bTrue := true
	bFalse := false

	for _, tc := range []struct {
		name                                string
		kind                                remote.SessionAlertKind
		finished, approval                  bool
		finishSound                         bool
		attentionSound                      *bool
		wantNotification                    bool
		wantFinishSound, wantAttentionSound bool
	}{
		{"done with sound", remote.SessionAlertDone, true, true, true, nil, true, true, false},
		{"done silent", remote.SessionAlertDone, true, true, false, nil, true, false, false},
		{"done sound only", remote.SessionAlertDone, false, true, true, nil, false, true, false},
		{"done disabled", remote.SessionAlertDone, false, true, false, nil, false, false, false},
		{"approval with default sound", remote.SessionAlertApproval, true, true, true, nil, true, false, true},
		{"approval with explicit sound", remote.SessionAlertApproval, true, true, true, &bTrue, true, false, true},
		{"approval silenced", remote.SessionAlertApproval, true, true, true, &bFalse, true, false, false},
		{"approval disabled notify with sound", remote.SessionAlertApproval, true, false, true, &bTrue, false, false, true},
		{"failed with sound", remote.SessionAlertFailed, true, true, true, nil, true, true, false},
		{"failed sound only", remote.SessionAlertFailed, false, true, true, nil, false, true, false},
		{"failed disabled", remote.SessionAlertFailed, false, true, false, nil, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			cfg := alertTestConfig()
			cfg.Notifications.NotifyWhenFinished = tc.finished
			cfg.Notifications.NotifyOnApproval = tc.approval
			cfg.Notifications.PlayFinishSound = tc.finishSound
			cfg.Notifications.ChimeWhenAttentionNeeded = tc.attentionSound
			if err := config.Save(dataDir, cfg); err != nil {
				t.Fatal(err)
			}
			alert := remote.SessionAlert{Kind: tc.kind, Tag: "tag", Title: "title", Body: "body"}
			var notified, finishSoundPlayed, attentionSoundPlayed bool
			handleDesktopAlert(dataDir, config.Defaults(), alert, func(id, title, body string) error {
				notified = true
				if id != alert.Tag || title != alert.Title || body != alert.Body {
					t.Fatal("notification lost alert content")
				}
				return nil
			}, func() {
				finishSoundPlayed = true
			}, func() {
				attentionSoundPlayed = true
			}, func() bool {
				return false // 后台
			})

			if notified != tc.wantNotification {
				t.Fatalf("notification: got %v, want %v", notified, tc.wantNotification)
			}
			if finishSoundPlayed != tc.wantFinishSound {
				t.Fatalf("finish sound: got %v, want %v", finishSoundPlayed, tc.wantFinishSound)
			}
			if attentionSoundPlayed != tc.wantAttentionSound {
				t.Fatalf("attention sound: got %v, want %v", attentionSoundPlayed, tc.wantAttentionSound)
			}
		})
	}
}

func TestDesktopAlertForegroundNotification(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		isForeground         bool
		notifyWhenForeground bool
		wantNotification     bool
	}{
		{"background always notifies", false, false, true},
		{"foreground muted when setting is false", true, false, false},
		{"foreground notifies when setting is true", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			cfg := alertTestConfig()
			cfg.Notifications.NotifyWhenFinished = true
			cfg.Notifications.NotifyWhenForeground = tc.notifyWhenForeground
			if err := config.Save(dataDir, cfg); err != nil {
				t.Fatal(err)
			}
			alert := remote.SessionAlert{Kind: remote.SessionAlertDone, Tag: "t1", Title: "done", Body: "finished"}
			var notified bool
			handleDesktopAlert(dataDir, config.Defaults(), alert, func(string, string, string) error {
				notified = true
				return nil
			}, func() {}, func() {}, func() bool {
				return tc.isForeground
			})

			if notified != tc.wantNotification {
				t.Fatalf("got notification %v, want %v", notified, tc.wantNotification)
			}
		})
	}
}

func TestDesktopAlertForegroundUsesThread(t *testing.T) {
	dataDir := t.TempDir()
	cfg := alertTestConfig()
	cfg.Notifications.NotifyWhenFinished = true
	cfg.Notifications.NotifyWhenForeground = false
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}
	oldViewing := viewingThreadForAlert
	t.Cleanup(func() { viewingThreadForAlert = oldViewing })
	viewingThreadForAlert = func(threadID string) bool { return threadID == "t1" }

	for _, tc := range []struct {
		threadID string
		want     bool
	}{
		{threadID: "t1", want: false},
		{threadID: "t2", want: true},
	} {
		notified := false
		handleDesktopAlert(dataDir, config.Defaults(), remote.SessionAlert{
			Kind: remote.SessionAlertDone, ThreadID: tc.threadID, Tag: "done:" + tc.threadID,
		}, func(string, string, string) error {
			notified = true
			return nil
		}, func() {}, func() {}, func() bool {
			t.Fatalf("会话提醒不应读取全局前台状态")
			return true
		})
		if notified != tc.want {
			t.Fatalf("thread %q: got notification %v, want %v", tc.threadID, notified, tc.want)
		}
	}
}

func TestNotificationThreadID(t *testing.T) {
	for _, tc := range []struct {
		tag  string
		want string
	}{
		{"done:thread-1", "thread-1"},
		{"approval:thread-2", "thread-2"},
		{"failed:thread-3", "thread-3"},
		{"thread-4", "thread-4"},
		{"", ""},
	} {
		if got := notificationThreadID(tc.tag); got != tc.want {
			t.Fatalf("tag %q: got %q, want %q", tc.tag, got, tc.want)
		}
	}
}

func TestDesktopAlertReloadsSettings(t *testing.T) {
	dataDir := t.TempDir()
	cfg := alertTestConfig()
	calls := 0
	alert := remote.SessionAlert{Kind: remote.SessionAlertDone}
	run := func() {
		handleDesktopAlert(dataDir, config.Defaults(), alert, func(string, string, string) error {
			calls++
			return nil
		}, func() {}, func() {}, func() bool { return false })
	}
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}
	run()
	cfg.Notifications.NotifyWhenFinished = false
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}
	run()
	if calls != 1 {
		t.Fatalf("setting change must apply without restart: %d notifications", calls)
	}
}

func TestDesktopAlertStalled(t *testing.T) {
	bTrue := true
	bFalse := false
	for _, tc := range []struct {
		name       string
		setting    *bool
		wantNotify bool
	}{
		{"stalled 开关开启", &bTrue, true},
		{"stalled 默认开启", nil, true},
		{"stalled 开关关闭", &bFalse, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			cfg := alertTestConfig()
			cfg.Notifications.NotifyWhenStalled = tc.setting
			if err := config.Save(dataDir, cfg); err != nil {
				t.Fatal(err)
			}
			alert := remote.SessionAlert{
				Kind:  remote.SessionAlertStalled,
				Tag:   "stalled:t-1",
				Title: "会话可能卡住了",
				Body:  "任务 · 已 10 分钟没有新进展",
			}
			var notified, attentionSoundPlayed bool
			handleDesktopAlert(dataDir, config.Defaults(), alert, func(id, title, body string) error {
				notified = true
				if id != alert.Tag || title != alert.Title || body != alert.Body {
					t.Fatal("notification lost alert content")
				}
				return nil
			}, func() {}, func() {
				attentionSoundPlayed = true
			}, func() bool {
				return false // 后台
			})

			if notified != tc.wantNotify {
				t.Fatalf("notification: got %v, want %v", notified, tc.wantNotify)
			}
			if !attentionSoundPlayed {
				t.Fatal("stalled 提醒应播放需要介入的提示音")
			}
		})
	}
}

func TestDesktopAlertQuota(t *testing.T) {
	for _, tc := range []struct {
		name            string
		autoContinue    bool
		wantNotify      bool
		wantFinishSound bool
	}{
		{"自动续跑开启时不通知", true, false, false},
		{"自动续跑关闭时通知", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			cfg := alertTestConfig()
			cfg.Notifications.NotifyWhenFinished = true
			cfg.Notifications.PlayFinishSound = true
			cfg.Session.AutoContinueAfterLimit = tc.autoContinue
			if err := config.Save(dataDir, cfg); err != nil {
				t.Fatal(err)
			}
			alert := remote.SessionAlert{
				Kind:  remote.SessionAlertQuota,
				Tag:   "quota:t-1",
				Title: "额度已用完",
				Body:  "任务 · 预计 15:04 恢复",
			}
			var notified, finishSoundPlayed bool
			handleDesktopAlert(dataDir, config.Defaults(), alert, func(string, string, string) error {
				notified = true
				return nil
			}, func() {
				finishSoundPlayed = true
			}, func() {}, func() bool {
				return false // 后台
			})

			if notified != tc.wantNotify {
				t.Fatalf("notification: got %v, want %v", notified, tc.wantNotify)
			}
			if finishSoundPlayed != tc.wantFinishSound {
				t.Fatalf("finish sound: got %v, want %v", finishSoundPlayed, tc.wantFinishSound)
			}
		})
	}
}

// 总管通知：前台抑制规则与 handleDesktopAlert 一致，通知 id 固定为 steward 以便同一时段合并。
func TestOwnerNotificationSender(t *testing.T) {
	type sent struct{ id, title, body string }
	for _, tc := range []struct {
		name                 string
		threadID             string
		isForeground         bool
		notifyWhenForeground bool
		wantID               string
		wantSend             bool
	}{
		{"后台发送", "thread-1", false, false, stewardNotificationID, true},
		{"前台默认不发", "thread-1", true, false, "", false},
		{"前台允许时发", "thread-1", true, true, stewardNotificationID, true},
		{"会话为空也发", "", false, false, stewardNotificationID, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			cfg := alertTestConfig()
			cfg.Notifications.NotifyWhenForeground = tc.notifyWhenForeground
			if err := config.Save(dataDir, cfg); err != nil {
				t.Fatal(err)
			}
			ch := make(chan sent, 1)
			send, _ := ownerNotificationSender(dataDir, config.Defaults(), func(id, title, body string) error {
				ch <- sent{id: id, title: title, body: body}
				return nil
			}, func() bool { return tc.isForeground }, nil)

			if err := send("总管汇报", "3 条待处理事件", tc.threadID); err != nil {
				if tc.wantSend || !errors.Is(err, app.ErrOwnerNotifySuppressed) {
					t.Fatalf("send: %v", err)
				}
			}
			if !tc.wantSend {
				select {
				case got := <-ch:
					t.Fatalf("前台不应发通知，却发了 %+v", got)
				case <-time.After(100 * time.Millisecond):
				}
				return
			}
			select {
			case got := <-ch:
				if got.id != tc.wantID {
					t.Fatalf("通知 id = %q, 期望 %q", got.id, tc.wantID)
				}
				if got.title != "总管汇报" || got.body != "3 条待处理事件" {
					t.Fatalf("通知内容错误: %+v", got)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("等待通知超时")
			}
		})
	}
}

// 总管通知的 id 固定，同一时段的多条通知才能在系统通知中心合并成一条。
func TestStewardNotificationIDIsStable(t *testing.T) {
	if stewardNotificationID != "steward" {
		t.Fatalf("stewardNotificationID = %q, 期望 steward", stewardNotificationID)
	}
}

// stewardSentNotification 是一条记录下来的总管通知。
type stewardSentNotification struct{ title, body string }

// stewardNotifyRecorder 记录发送结果；发送在 goroutine 里进行，读取要加锁。
type stewardNotifyRecorder struct {
	mu   sync.Mutex
	sent []stewardSentNotification
}

func (r *stewardNotifyRecorder) notify(_ string, title, body string) error {
	r.mu.Lock()
	r.sent = append(r.sent, stewardSentNotification{title: title, body: body})
	r.mu.Unlock()
	return nil
}

func (r *stewardNotifyRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

// last 返回最后记录的一条通知，还没有记录时返回 false。
func (r *stewardNotifyRecorder) last() (stewardSentNotification, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sent) == 0 {
		return stewardSentNotification{}, false
	}
	return r.sent[len(r.sent)-1], true
}

// waitLast 等到最后一条通知与期望内容一致后返回：发送在 goroutine 里进行且会丢弃被
// 更新汇总覆盖的旧内容，调用次数不再等于发送次数，所以只断言「最后一条横幅是最新的
// 汇总」。匹配后再观察一小段时间，确认它没有被更旧的汇总顶掉。正文传空表示只比较标题。
func (r *stewardNotifyRecorder) waitLast(t *testing.T, wantTitle, wantBody string) stewardSentNotification {
	t.Helper()
	match := func(got stewardSentNotification) bool {
		if got.title != wantTitle {
			return false
		}
		return wantBody == "" || got.body == wantBody
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		last, ok := r.last()
		if ok && match(last) {
			time.Sleep(20 * time.Millisecond)
			if again, still := r.last(); still && match(again) {
				return again
			}
		}
		if time.Now().After(deadline) {
			if !ok {
				t.Fatalf("等待通知 %q 超时，没有记录到任何通知", wantTitle)
			}
			t.Fatalf("等待通知 %q 超时，最后一条是 %+v", wantTitle, last)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestStewardNotificationSummary 验证总管通知的累积与汇总：单条沿用原标题正文，
// 多条按时间倒序汇总、最多列三条，超过 30 分钟的条目丢弃，清空后重新计数。
func TestStewardNotificationSummary(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.Defaults()
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 30, 10, 0, 0, 0, time.Local)
	foreground := false
	rec := &stewardNotifyRecorder{}
	send, reset := ownerNotificationSender(dataDir, cfg, rec.notify, func() bool { return foreground }, func() time.Time { return clock })

	// 单条：照旧发原标题与正文。
	send("汇报一", "第一条正文\n第二行", "")
	rec.waitLast(t, "汇报一", "第一条正文\n第二行")

	// 两条：标题带总数，正文按时间倒序并只取首行。
	clock = clock.Add(time.Minute)
	send("汇报二", "第二条正文", "")
	rec.waitLast(t, cfg.Orchestration.Steward.DisplayName()+" · 2 条新消息", "汇报二：第二条正文\n汇报一：第一条正文")

	// 超过三条：只列最近三条，再加一行剩余数量。
	for _, item := range []stewardSentNotification{{"汇报三", "第三条正文"}, {"汇报四", "第四条正文"}, {"汇报五", "第五条正文"}} {
		clock = clock.Add(time.Minute)
		send(item.title, item.body, "")
	}
	got := rec.waitLast(t, cfg.Orchestration.Steward.DisplayName()+" · 5 条新消息", "汇报五：第五条正文\n汇报四：第四条正文\n汇报三：第三条正文\n另有 2 条")
	// 最后留下的必须是最新的五条汇总，不能被旧的四条汇总顶掉。
	if old := cfg.Orchestration.Steward.DisplayName() + " · 4 条新消息"; got.title == old {
		t.Fatalf("最后一条通知是旧汇总 %q", got.title)
	}

	// 前台抑制跳过的通知不计入累积，也不改动已经发出去的横幅。
	foreground = true
	before := rec.count()
	send("汇报六", "第六条正文", "")
	time.Sleep(50 * time.Millisecond)
	if got := rec.count(); got != before {
		t.Fatalf("前台抑制的通知不应发送，已发 %d 条，抑制前 %d 条", got, before)
	}
	if last, ok := rec.last(); !ok || last.title != cfg.Orchestration.Steward.DisplayName()+" · 5 条新消息" {
		t.Fatalf("前台抑制不应改动最后的横幅: %+v", last)
	}
	foreground = false

	// 清空后重新从单条开始计数。
	reset()
	send("汇报七", "第七条正文", "")
	rec.waitLast(t, "汇报七", "第七条正文")

	// 超过 30 分钟的条目在下次发送时丢弃。
	clock = clock.Add(20 * time.Minute)
	send("汇报八", "第八条正文", "")
	rec.waitLast(t, cfg.Orchestration.Steward.DisplayName()+" · 2 条新消息", "汇报八：第八条正文\n汇报七：第七条正文")
	clock = clock.Add(31 * time.Minute)
	send("汇报九", "第九条正文", "")
	rec.waitLast(t, "汇报九", "第九条正文")
}

// TestStewardNotificationSummaryOrder 验证连续快速发送后，最终记录到的通知仍是最新的
// 汇总：每条通知各在自己的 goroutine 里发，序号更小的旧汇总不能在新横幅之后落到通知中心。
func TestStewardNotificationSummaryOrder(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.Defaults()
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 30, 10, 0, 0, 0, time.Local)
	rec := &stewardNotifyRecorder{}
	send, _ := ownerNotificationSender(dataDir, cfg, rec.notify, func() bool { return false }, func() time.Time { return clock })

	const total = 12
	for i := 1; i <= total; i++ {
		send(fmt.Sprintf("汇报%d", i), fmt.Sprintf("第%d条正文", i), "")
	}
	wantTitle := fmt.Sprintf("%s · %d 条新消息", cfg.Orchestration.Steward.DisplayName(), total)
	got := rec.waitLast(t, wantTitle, "汇报12：第12条正文\n汇报11：第11条正文\n汇报10：第10条正文\n另有 9 条")
	if got.title != wantTitle {
		t.Fatalf("最后一条通知标题 = %q, 期望 %q", got.title, wantTitle)
	}
}

func TestAutomationNotificationSender(t *testing.T) {
	for _, tc := range []struct {
		name         string
		whenFinished bool
		threadID     string
		viewing      bool
		wantNotify   bool
		wantID       string
	}{
		{"开启通知且用户没在看", true, "thread-1", false, true, "done:thread-1"},
		{"关闭完成后通知", false, "thread-1", false, false, ""},
		{"用户正在看该会话", true, "thread-1", true, false, ""},
		{"没有会话时用固定 id", true, "", false, true, "automation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			cfg := config.Defaults()
			cfg.Notifications.NotifyWhenFinished = tc.whenFinished
			if err := config.Save(dataDir, cfg); err != nil {
				t.Fatal(err)
			}
			var gotID, gotTitle, gotBody string
			send := automationNotificationSender(dataDir, config.Defaults(), func(id, title, body string) error {
				gotID, gotTitle, gotBody = id, title, body
				return nil
			}, func(threadID string) bool {
				if threadID != tc.threadID {
					t.Fatalf("viewing 收到 %q, 期望 %q", threadID, tc.threadID)
				}
				return tc.viewing
			})
			if err := send("自动化完成：夜间巡检", "全部检查通过", tc.threadID); err != nil {
				t.Fatalf("send: %v", err)
			}
			notified := gotID != ""
			if notified != tc.wantNotify {
				t.Fatalf("notification: got %v, want %v", notified, tc.wantNotify)
			}
			if !tc.wantNotify {
				return
			}
			if gotID != tc.wantID {
				t.Fatalf("id = %q, 期望 %q", gotID, tc.wantID)
			}
			if gotTitle != "自动化完成：夜间巡检" || gotBody != "全部检查通过" {
				t.Fatalf("title/body = %q/%q, 通知内容被改动", gotTitle, gotBody)
			}
		})
	}
}

// 通知 id 与会话已完成通知共用 done:<会话 ID>，同一会话的新通知覆盖旧横幅。
func TestAutomationNotificationID(t *testing.T) {
	if got := automationNotificationID("thread-9"); got != "done:thread-9" {
		t.Fatalf("id = %q, 期望 done:thread-9", got)
	}
	if got := automationNotificationID(""); got != "automation" {
		t.Fatalf("id = %q, 期望 automation", got)
	}
}

func TestDesktopAlertBackgroundLinger(t *testing.T) {
	for _, tc := range []struct {
		name       string
		notify     bool
		wantNotify bool
	}{
		{"完成通知开关开启", true, true},
		{"完成通知开关关闭", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			cfg := alertTestConfig()
			cfg.Notifications.NotifyWhenFinished = tc.notify
			cfg.Notifications.PlayFinishSound = true
			if err := config.Save(dataDir, cfg); err != nil {
				t.Fatal(err)
			}
			alert := remote.SessionAlert{
				Kind:  remote.SessionAlertBackgroundLinger,
				Tag:   "bg-linger:t-1",
				Title: "后台任务仍在运行",
				Body:  "任务 · 回合已结束，1 个后台任务仍在运行",
			}
			var notified, finishSoundPlayed, attentionSoundPlayed bool
			handleDesktopAlert(dataDir, config.Defaults(), alert, func(id, title, body string) error {
				notified = true
				if id != alert.Tag || title != alert.Title || body != alert.Body {
					t.Fatal("notification lost alert content")
				}
				return nil
			}, func() {
				finishSoundPlayed = true
			}, func() {
				attentionSoundPlayed = true
			}, func() bool {
				return false // 后台
			})

			if notified != tc.wantNotify {
				t.Fatalf("notification: got %v, want %v", notified, tc.wantNotify)
			}
			if finishSoundPlayed || attentionSoundPlayed {
				t.Fatal("后台久跑提醒不应播放提示音")
			}
		})
	}
}
