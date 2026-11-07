package app

// remoteWirings 保存所有远程扩展接线函数。
// 约定：各扩展在 internal/app 下的新文件里用 init() 调 registerRemoteWiring 登记，
// 由 New 在 App 构造完成后统一执行，避免改动 New 本身。
var remoteWirings []func(a *App)

// registerRemoteWiring 登记一个远程扩展接线函数；约定只在 init() 中调用。
// fn 通常只做 a.backend.SetService(...) 这类注入，不应启动后台任务。
func registerRemoteWiring(fn func(a *App)) {
	if fn == nil {
		panic("app: registerRemoteWiring 不接受 nil")
	}
	remoteWirings = append(remoteWirings, fn)
}

// wireRemoteExtensions 按登记顺序执行全部远程扩展接线。
func wireRemoteExtensions(a *App) {
	for _, fn := range remoteWirings {
		fn(a)
	}
}

// remoteClient 返回 App 内部的远程适配器。
// 配置、密钥库、git 执行器、懒加载的 store 连接都挂在 Client 上，
// 远程扩展需要这些能力时从这里取；参数为 nil 时返回 nil。
func (a *App) remoteClient() *Client {
	if a == nil {
		return nil
	}
	return a.client
}
