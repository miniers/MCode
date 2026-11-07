package native

import (
	"github.com/egoist/mygo"
)

// Notifier 封装系统级桌面通知能力。
type Notifier struct{}

// NewNotifier 创建并返回 Notifier 实例。
func NewNotifier() *Notifier {
	return &Notifier{}
}

// Notify 发送一条系统桌面通知。
// 不支持通知（!mygo.NotificationsSupported()）时直接返回 nil；
// 构造 NotificationOptions{ID: id, Title: title, Body: body, Group: id}，并切到主线程执行 Show，
// 可以从任意 goroutine 调用。
func (n *Notifier) Notify(id, title, body string) error {
	if !mygo.NotificationsSupported() {
		return nil
	}

	notif := mygo.NewNotification(mygo.NotificationOptions{
		ID:    id,
		Title: title,
		Body:  body,
		Group: id,
		// 完成音由 PlayFinishSound 单独控制，避免叠加系统默认提示音。
		Silent: true,
	})

	var err error
	mygo.RunOnMain(func() {
		err = notif.Show()
	})
	return err
}

// OnClick 包装 mygo.App.OnNotificationClick，注册全局通知点击回调。
func (n *Notifier) OnClick(fn func(id string)) (off func()) {
	return mygo.App.OnNotificationClick(fn)
}
