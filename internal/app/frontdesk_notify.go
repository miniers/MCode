package app

import "errors"

// ErrOwnerNotifySuppressed 表示桌面通知被前台或安静时段抑制。
var ErrOwnerNotifySuppressed = errors.New("桌面通知被抑制（应用在前台或处于安静时段）")

// SetOwnerNotifier 设置向用户发桌面通知的实现。
func (a *App) SetOwnerNotifier(fn func(title, body, threadID string) error) {
	a.notifyMu.Lock()
	defer a.notifyMu.Unlock()
	a.ownerNotify = fn
}

// notifyAttention 同时通知已订阅的网页设备与桌面端；桌面端自身负责前台和安静时段规则。
func (a *App) notifyAttention(title, body, threadID string) {
	if a.remote != nil {
		a.remote.SendOwnerPush(title, body, threadID)
	}
	a.notifyMu.RLock()
	fn := a.ownerNotify
	a.notifyMu.RUnlock()
	if fn != nil {
		_ = fn(title, body, threadID)
	}
}
