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
