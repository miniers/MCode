package app

import (
	"context"
	"errors"
	"fmt"
)

var errOwnerNotifierUnavailable = errors.New("桌面通知未接入")

// ErrOwnerNotifySuppressed 表示桌面通知被前台或安静时段抑制。
var ErrOwnerNotifySuppressed = errors.New("桌面通知被抑制（应用在前台或处于安静时段）")

var errOwnerNotifyHidden = errors.New("用户正在看 mcode（或处于安静时段），桌面通知没有发出；请直接在主聊天里告诉用户")

// SetOwnerNotifier 设置向用户发桌面通知的实现。
func (a *App) SetOwnerNotifier(fn func(title, body, threadID string) error) {
	a.notifyMu.Lock()
	defer a.notifyMu.Unlock()
	a.ownerNotify = fn
}

func (a *App) notifyOwner(_ context.Context, title, body, threadID string) error {
	a.notifyMu.RLock()
	fn := a.ownerNotify
	a.notifyMu.RUnlock()
	var notifyErr error
	if fn != nil {
		notifyErr = fn(title, body, threadID)
	}
	pushed := a.remote != nil && a.remote.SendOwnerPush(title, body, threadID)
	if pushed {
		return nil
	}
	if fn == nil {
		return errOwnerNotifierUnavailable
	}
	if notifyErr != nil {
		if errors.Is(notifyErr, ErrOwnerNotifySuppressed) {
			return errOwnerNotifyHidden
		}
		return fmt.Errorf("桌面通知没有发出（原因：%v）；请直接在主聊天里告诉用户", notifyErr)
	}
	return nil
}
