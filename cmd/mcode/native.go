package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"mcode/internal/app"
	"mcode/internal/config"
	"mcode/internal/native"
	"mcode/internal/remote"
	"mcode/internal/startuptrace"
	mcodeui "mcode/internal/ui"
	"mcode/resources"
)

// setupNative 初始化桌面端系统能力（通知、托盘、全局快捷键），并监听 StreamHub 的会话提醒。
// trayActive 表示托盘是否真的建起来了：配置里开启了托盘但创建失败时为 false，调用方据此
// 决定最后一个窗口关闭后是退出还是留在后台。
// sendOwnerNotification 是总管汇报的桌面通知发送函数，调用方交给 app.App.SetOwnerNotifier；
// 总管只在桌面端才有通知器，其它启动方式不调用它。
// sendAutomationNotification 是自动化运行成功的通知发送函数，调用方交给 app.App.SetAutomationNotifier。
func setupNative(hub *remote.StreamHub, client *app.Client, dataDir string, cfg config.Config, showWindow func(), activateThread func(threadID string), onForceQuit func()) (cleanup func(), trayActive bool, sendOwnerNotification func(title, body, threadID string) error, sendAutomationNotification func(title, body, threadID string) error) {
	// Dock 图标设置在主线程上要花几十毫秒，挪到首个聊天窗口绘制完首帧之后再设，不挡首屏。
	mcodeui.AfterFirstFrame(func() {
		if len(resources.IconPNG) > 0 {
			if err := mygo.App.Dock.SetIcon(resources.IconPNG); err != nil {
				log.Printf("failed to set dock icon: %v", err)
			}
		}
		startuptrace.Mark("dock icon set")
	})

	notifier := native.NewNotifier()
	// 总管通知的累积由发送函数维护，清理函数在下面赋值；点击汇总通知即视为已读。
	var resetStewardNotifications func()
	offClick := notifier.OnClick(func(tag string) {
		showWindow()
		if tag == stewardNotificationID && resetStewardNotifications != nil {
			resetStewardNotifications()
		}
		// 通知点击后切到对应会话：tag 形如 "done:<会话 ID>"。
		// 总管的汇总通知没有具体会话，点了只把主窗口叫到前面。
		if activateThread != nil && tag != stewardNotificationID {
			if id := notificationThreadID(tag); id != "" {
				activateThread(id)
			}
		}
	})
	startuptrace.Mark("notifier ready")

	quit := func() {
		if onForceQuit != nil {
			onForceQuit()
		} else {
			mygo.App.Quit()
		}
	}

	var panel *mcodeui.TrayQuotaPanel
	var tray *native.Tray
	if cfg.Startup.ShowTrayIcon {
		var err error
		tray, err = native.NewTray(native.TrayOptions{
			OnShowWindow: showWindow,
			OnOpenSettings: func() {
				showWindow()
				mcodeui.DispatchMenuAction("open-settings", 0)
			},
			OnCheckForUpdates: func() {
				showWindow()
				mcodeui.DispatchMenuAction("check-update", 0)
			},
			OnLeftClick: func() {
				if panel != nil {
					panel.Toggle()
				}
			},
			OnQuit: quit,
		})
		if err != nil {
			log.Printf("failed to create tray icon: %v", err)
		}
		if tray != nil && client != nil {
			panel = mcodeui.NewTrayQuotaPanel(mcodeui.TrayQuotaPanelOptions{
				Tray: tray.Raw(),
				Fetch: func(ctx context.Context) []mcodeui.TrayQuotaGroup {
					return toUITrayQuotaGroups(client.TrayQuotaGroups(ctx))
				},
				OnShowWindow: showWindow,
				OnQuit:       quit,
				OnSummary: func(text string) {
					latestCfg, loadErr := config.Load(dataDir)
					if loadErr != nil {
						latestCfg = cfg
					}
					if latestCfg.Startup.TrayShowQuota {
						tray.SetTitle(text)
					} else {
						tray.SetTitle("")
					}
				},
			})
			panel.Start()
		}
	}
	startuptrace.Mark("tray ready")

	var unregisterShortcut func()
	if cfg.Shortcuts.GlobalShortcutEnabled && cfg.Shortcuts.GlobalShortcut != "" {
		var err error
		unregisterShortcut, err = native.RegisterGlobalShortcut(cfg.Shortcuts.GlobalShortcut, showWindow)
		if err != nil {
			log.Printf("failed to register global shortcut %q: %v", cfg.Shortcuts.GlobalShortcut, err)
		}
	}
	startuptrace.Mark("global shortcut ready")

	isForeground := func() bool {
		if mygo.App == nil || !mygo.App.IsReady() || mygo.App.IsHidden() {
			return false
		}
		win := mygo.FocusedWindow()
		return win != nil && win.IsFocused()
	}

	sendOwnerNotification, resetStewardNotifications = ownerNotificationSender(dataDir, cfg, notifier.Notify, isForeground, nil)
	// 自动化运行成功通知：与「会话已完成」共用 done:<会话 ID> 的 tag，后到的一条覆盖前一条。
	sendAutomationNotification = automationNotificationSender(dataDir, cfg, notifier.Notify, mcodeui.IsViewingThread)
	// 应用回到前台也说明用户已经看到汇总，清空累积，下一条从「1 条」重新开始。
	var offActivate func()
	if mygo.App != nil {
		offActivate = mygo.App.OnDidBecomeActive(func() {
			if resetStewardNotifications != nil {
				resetStewardNotifications()
			}
		})
	}

	stopWatch := remote.WatchSessionAlerts(hub, func() time.Duration {
		// 每次巡检都现读配置，改设置后无需重启；读失败时退回启动时的配置。
		latestCfg, loadErr := config.Load(dataDir)
		if loadErr != nil {
			latestCfg = cfg
		}
		if !latestCfg.Notifications.StalledAlertEnabled() {
			return 0
		}
		return latestCfg.Notifications.StalledAlertAfter()
	}, func(alert remote.SessionAlert) {
		// 系统通知可能等待用户授权，不能阻塞审批事件下发或会话结束流程。
		go handleDesktopAlert(dataDir, cfg, alert, notifier.Notify, native.PlayFinishChime, native.PlayAttentionChime, isForeground)
	}, func(n int) {
		if tray != nil {
			tray.SetRunningCount(n)
		}
	})
	startuptrace.Mark("session alerts watched")

	return func() {
		if stopWatch != nil {
			stopWatch()
		}
		if unregisterShortcut != nil {
			unregisterShortcut()
		}
		if panel != nil {
			panel.Stop()
		}
		if tray != nil {
			tray.Destroy()
		}
		if offClick != nil {
			offClick()
		}
		if offActivate != nil {
			offActivate()
		}
	}, tray != nil, sendOwnerNotification, sendAutomationNotification
}

// stewardNotificationID 是总管 owner 通知的固定 id：同一时段的多条通知在系统通知中心
// 按 id（同时用作分组）合并成一条，避免一次汇总弹出十几条横幅。
const stewardNotificationID = "steward"

const (
	// stewardNotificationRetention 是累积条目的保留时长：超过这个时间还没被清空的通知
	// 不再计入汇总，用户长时间没看时不会看到早就过期的正文。
	stewardNotificationRetention = 30 * time.Minute
	// stewardNotificationSummaryLines 是汇总正文最多列出的条数，超出的只报剩余数量。
	stewardNotificationSummaryLines = 3
)

// stewardNotification 是一条待汇总的总管通知。
type stewardNotification struct {
	title string
	body  string
	at    time.Time
}

// stewardNotificationStore 累积最近未读的总管通知：系统通知 id 固定，macOS 上新横幅会
// 替掉旧横幅，只有把多条汇总成一条，用户才能看到前面几条的正文。
type stewardNotificationStore struct {
	mu      sync.Mutex
	now     func() time.Time
	entries []stewardNotification
	// seq 是逐次递增的汇总序号：在锁内随 add 一起加一，与到达顺序一致，异步发送时
	// 用它丢弃会被更新汇总覆盖的旧内容。reset 不清零，否则旧汇总会重新变得「更新」。
	seq uint64
}

// newStewardNotificationStore 构造累积器，now 为空时使用 time.Now。
func newStewardNotificationStore(now func() time.Time) *stewardNotificationStore {
	if now == nil {
		now = time.Now
	}
	return &stewardNotificationStore{now: now}
}

// add 先丢掉过期条目，再记下这条通知，返回当前未读的快照（按时间正序）与这次汇总的序号。
func (s *stewardNotificationStore) add(title, body string) ([]stewardNotification, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	keep := s.entries[:0]
	for _, entry := range s.entries {
		if now.Sub(entry.at) < stewardNotificationRetention {
			keep = append(keep, entry)
		}
	}
	s.entries = append(keep, stewardNotification{title: title, body: body, at: now})
	s.seq++
	out := make([]stewardNotification, len(s.entries))
	copy(out, s.entries)
	return out, s.seq
}

// reset 清空累积：用户点击总管通知或主窗口回到前台后调用。
func (s *stewardNotificationStore) reset() {
	s.mu.Lock()
	s.entries = nil
	s.mu.Unlock()
}

// stewardNotificationText 把未读通知组装成一条横幅：只有一条时沿用原标题与正文；
// 多条时标题带上总数，正文按时间倒序列出最近的几条，超出部分只报数量。
func stewardNotificationText(name string, entries []stewardNotification) (title, body string) {
	if len(entries) == 0 {
		return "", ""
	}
	if len(entries) == 1 {
		return entries[0].title, entries[0].body
	}
	if name = strings.TrimSpace(name); name == "" {
		name = config.DefaultStewardName
	}
	title = fmt.Sprintf("%s · %d 条新消息", name, len(entries))
	lines := make([]string, 0, stewardNotificationSummaryLines+1)
	for i := len(entries) - 1; i >= 0 && len(lines) < stewardNotificationSummaryLines; i-- {
		lines = append(lines, fmt.Sprintf("%s：%s", entries[i].title, firstNotificationLine(entries[i].body)))
	}
	if rest := len(entries) - stewardNotificationSummaryLines; rest > 0 {
		lines = append(lines, fmt.Sprintf("另有 %d 条", rest))
	}
	return title, strings.Join(lines, "\n")
}

// firstNotificationLine 取正文首行作为汇总里的摘要。
func firstNotificationLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	return strings.TrimSpace(text)
}

// ownerNotificationSender 构造总管汇报的桌面通知发送函数及清空累积的函数：前台抑制与
// 安静时段规则与 handleDesktopAlert 一致（NotifyWhenForeground 为假且主窗口在前台时不发；
// 安静时段只留徽标，徽标由 attention 数据驱动），配置每次现读，改设置后无需重启。
// 丢弃的通知不计入累积；通知 id 固定为 "steward"，多条合并成一条。
// 前台抑制或安静时段不弹横幅，但返回包装了 app.ErrOwnerNotifySuppressed 的错误，
// 让 notify_owner 知道这次汇报没送到用户眼前。
// 系统通知可能等待用户授权，发送放在 goroutine 里，不阻塞总管的工具调用。
// now 用于取当前时间，为空时使用 time.Now。
func ownerNotificationSender(dataDir string, fallback config.Config, notify func(id, title, body string) error, isForeground func() bool, now func() time.Time) (func(title, body, threadID string) error, func()) {
	if now == nil {
		now = time.Now
	}
	store := newStewardNotificationStore(now)
	// 每次发送都在各自的 goroutine 里进行，启动顺序不确定：用互斥锁让 notify 串行执行，
	// 并记下已发出的最大序号，序号不比它新的汇总直接跳过，避免旧横幅顶掉更新的横幅。
	var sendMu sync.Mutex
	var lastSent uint64
	send := func(title, body, threadID string) error {
		if notify == nil {
			return nil
		}
		cfg, err := config.Load(dataDir)
		if err != nil {
			cfg = fallback
		}
		if !cfg.Notifications.NotifyWhenForeground && isForeground != nil && isForeground() {
			// 应用在前台且配置为不打扰：用户大概正在看 mcode，桌面横幅不发。
			// 返回哨兵错误，让总管知道这次汇报没有送到用户眼前。
			return fmt.Errorf("用户正在看 mcode（应用在前台），桌面通知未弹出：%w", app.ErrOwnerNotifySuppressed)
		}
		if inQuiet, _ := cfg.Orchestration.Steward.QuietWindow(now()); inQuiet {
			// 安静时段只留徽标：同样报回哨兵错误，总管据此改在主聊天里说一遍。
			return fmt.Errorf("当前处于安静时段，桌面通知未弹出：%w", app.ErrOwnerNotifySuppressed)
		}
		// 先在当前 goroutine 里算出汇总与序号，保证多条并发到达时顺序与到达顺序一致。
		entries, seq := store.add(title, body)
		summaryTitle, summaryBody := stewardNotificationText(cfg.Orchestration.Steward.DisplayName(), entries)
		if summaryTitle == "" {
			return nil
		}
		go func() {
			sendMu.Lock()
			defer sendMu.Unlock()
			if seq <= lastSent {
				return
			}
			lastSent = seq
			if err := notify(stewardNotificationID, summaryTitle, summaryBody); err != nil {
				log.Printf("failed to send steward notification: %v", err)
			}
		}()

		return nil
	}
	return send, store.reset
}

// automationNotificationSender 构造自动化运行成功的桌面通知发送函数。
// 每次发送现读配置，关闭「完成后通知」时不发，改设置后无需重启；用户正在看该会话时
// （viewing 为真）也不发，避免给眼前正在刷新的内容再弹一条。
// 通知 id 与「会话已完成」通知相同（done:<会话 ID>），同一会话不会堆出两条通知；
// 没有会话时用固定 id "automation"。
func automationNotificationSender(dataDir string, fallback config.Config, notify func(id, title, body string) error, viewing func(threadID string) bool) func(title, body, threadID string) error {
	if notify == nil {
		return nil
	}
	return func(title, body, threadID string) error {
		cfg, err := config.Load(dataDir)
		if err != nil {
			cfg = fallback
		}
		if !cfg.Notifications.NotifyWhenFinished {
			return nil
		}
		if threadID != "" && viewing != nil && viewing(threadID) {
			return nil
		}
		return notify(automationNotificationID(threadID), title, body)
	}
}

// automationNotificationID 返回自动化通知在系统通知中心的 id：
// 有会话时与「会话已完成」共用 done:<会话 ID>，新通知覆盖旧横幅；没有会话时用固定 id。
func automationNotificationID(threadID string) string {
	if threadID == "" {
		return "automation"
	}
	return "done:" + threadID
}

func handleDesktopAlert(dataDir string, fallback config.Config, alert remote.SessionAlert, notify func(id, title, body string) error, playFinishSound func(), playAttentionSound func(), isForeground func() bool) {
	cfg, err := config.Load(dataDir)
	if err != nil {
		cfg = fallback
	}
	var enabled bool
	switch alert.Kind {
	case remote.SessionAlertDone, remote.SessionAlertFailed:
		// 出错与正常结束都代表这一轮结束：共用完成提示音与「轮次结束」通知开关。
		if cfg.Notifications.PlayFinishSound && playFinishSound != nil {
			playFinishSound()
		}
		enabled = cfg.Notifications.NotifyWhenFinished
	case remote.SessionAlertApproval:
		if cfg.Notifications.ChimeOnAttention() && playAttentionSound != nil {
			playAttentionSound()
		}
		enabled = cfg.Notifications.NotifyOnApproval
	case remote.SessionAlertStalled:
		// 卡住属于需要用户留意的情况，沿用「需要介入」的提示音与开关。
		if cfg.Notifications.ChimeOnAttention() && playAttentionSound != nil {
			playAttentionSound()
		}
		enabled = cfg.Notifications.StalledAlertEnabled()
	case remote.SessionAlertQuota:
		// 自动续跑会自行恢复并继续执行，不再重复打扰用户。
		if cfg.Session.AutoContinueAfterLimit {
			return
		}
		if cfg.Notifications.PlayFinishSound && playFinishSound != nil {
			playFinishSound()
		}
		enabled = cfg.Notifications.NotifyWhenFinished
	case remote.SessionAlertBackgroundLinger:
		// 后台任务久跑只提示一次，沿用「轮次结束」的开关，但不播提示音。
		enabled = cfg.Notifications.NotifyWhenFinished
	}
	if enabled {
		if !cfg.Notifications.NotifyWhenForeground && alertInForeground(alert, isForeground) {
			return
		}
		// 安静时段不弹系统通知：徽标与收件箱照常由 attention 数据驱动。
		if inQuiet, _ := cfg.Orchestration.Steward.QuietWindow(time.Now()); inQuiet {
			return
		}
		if err := notify(alert.Tag, alert.Title, alert.Body); err != nil {
			log.Printf("failed to send %s notification: %v", alert.Kind, err)
		}
	}
}

// viewingThreadForAlert 可在测试中替换；有会话 ID 时只抑制用户正在看的那条会话。
var viewingThreadForAlert = mcodeui.IsViewingThread

func alertInForeground(alert remote.SessionAlert, isForeground func() bool) bool {
	if alert.ThreadID != "" {
		return viewingThreadForAlert != nil && viewingThreadForAlert(alert.ThreadID)
	}
	return isForeground != nil && isForeground()
}

// notificationThreadID 从通知 tag 中取回会话 ID：tag 形如 "done:<会话 ID>"、"failed:<会话 ID>"，
// 不含分隔符时原样返回（兼容直接传会话 ID 的调用）。
func notificationThreadID(tag string) string {
	_, id, ok := strings.Cut(tag, ":")
	if !ok {
		return tag
	}
	return id
}

// toUITrayQuotaGroups 将 app.TrayQuotaGroup 列表转换为 UI 展示用的 mcodeui.TrayQuotaGroup 列表。
func toUITrayQuotaGroups(groups []app.TrayQuotaGroup) []mcodeui.TrayQuotaGroup {
	if len(groups) == 0 {
		return nil
	}
	res := make([]mcodeui.TrayQuotaGroup, len(groups))
	for i, g := range groups {
		res[i] = mcodeui.TrayQuotaGroup{
			Provider: g.Provider,
			Title:    g.Title,
			Windows:  g.Windows,
			Err:      g.Err,
		}
	}
	return res
}
