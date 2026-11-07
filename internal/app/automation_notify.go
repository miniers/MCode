package app

// SetAutomationNotifier 设置自动化运行成功后的桌面通知实现，并发安全；传 nil 表示未接入。
// 桌面端在 setupNative 之后接入，远程端与无桌面环境的启动方式保持未接入。
// fn 收到的 threadID 可能为空字符串，表示这次运行还没有对应会话。
func (a *App) SetAutomationNotifier(fn func(title, body, threadID string)) {
	if a == nil || a.attentionSrc == nil {
		return
	}
	a.attentionSrc.setAutomationNotify(fn)
}
