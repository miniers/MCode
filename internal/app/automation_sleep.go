package app

// automationKeepAwake 判定自动化是否需要阻止电脑休眠：
// 开关关闭时一律不阻止；否则只要有自动化正在运行，或 5 分钟内有启用的自动化到期，就阻止休眠。
// 把判定抽成纯函数，方便单独测试。
func automationKeepAwake(enabled bool, running bool, dueSoon func() bool) bool {
	if !enabled {
		return false
	}
	if running {
		return true
	}
	if dueSoon == nil {
		return false
	}
	return dueSoon()
}
