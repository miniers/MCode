package remote

// implementedMethods 包含 Dispatcher 路由支持的方法名列表。
var implementedMethods = []string{
	"session.pair",
	"session.authenticate",
	"session.newPairingCode",
	"session.revokeDevice",
	"session.renameDevice",
	"capabilities.get",
	"kanban.getBoard",
	"projects.list",
	"providers.list",
	"providers.rememberSelection",
	"threads.list",
	"threads.get",
	"threads.create",
	"threads.update",
	"threads.title.regenerate",
	"threads.history",
	"threads.subscribe",
	"threads.unsubscribe",
	"threads.sendMessage",
	"threads.splitSession",
	"threads.interrupt",
	"threads.killRunningCommand",
	"threads.turn.recall",
	"threads.queue.edit",
	"threads.provider.limitText",
	"threads.worktree.preview",
	"threads.worktree.execute",
	"threads.compact",
	"threads.actionStatus",
	"requests.list",
	"requests.respond",
	"threads.getDiff",
	"kanban.createTask",
	"kanban.moveTask",
	"kanban.startTask",
	"kanban.reviewTask",
	"kanban.getDiff",
	"goals.create",
	"goals.start",
	"goals.pause",
	"goals.cancel",
	"goals.replan",
	"goals.retryTask",
	"goals.skipTask",
	"threads.swarm.get",
	"threads.swarm.set",
	"threads.swarm.setBeeTiers",
	"threads.swarm.stopBee",
	"threads.swarm.stopAll",
	"threads.swarm.clearFinished",
	"threads.swarm.setPlansOnly",
	"threads.swarm.setMode",
	"threads.swarm.forwardQuestion",
	"threads.swarm.mergeDiff",
	"threads.swarm.approveMerge",
	"threads.swarm.discardMerge",
	"swarm.tiers.get",
	"swarm.tiers.set",
	"threads.move",
	"threads.branch",
	"threads.continue",
	"threads.commands",
	"threads.settleAction",
	"threads.retain",
	"threads.provisional.release",
	"threads.bulk.preview",
	"threads.bulk.archive",
	"threads.bulk.markDormant",
	"threads.bulk.markRead",
	"threads.bulk.undo",
	"threads.queue.list",
	"threads.queue.update",
	"threads.queue.remove",
	"threads.queue.sendNow",
	"threads.queue.move",
	"threads.mcpDisabled.set",
	"threads.queue.interruptAndSend",
	"files.list",
	"files.read",
	"files.search",
	"git.status",
	"git.branches",
	"scripts.list",
	"scripts.save",
	"scripts.run",
	"scripts.runStatus",
	"scripts.setFavorite",
	"composer.quota",
	"attachments.upload",
	"threads.media.read",
	"push.publicKey",
	"push.subscribe",
	"push.unsubscribe",
}

// ImplementedMethods 返回 Dispatcher 路由表中的方法名副本。
// 内置方法保持声明顺序，扩展方法按名字升序追加在后，保证 capabilities.get 的输出确定。
func ImplementedMethods() []string {
	extensions := extensionMethodNames()
	res := make([]string, 0, len(implementedMethods)+len(extensions))
	res = append(res, implementedMethods...)
	seen := make(map[string]bool, len(implementedMethods))
	for _, name := range implementedMethods {
		seen[name] = true
	}
	for _, name := range extensions {
		if seen[name] {
			continue
		}
		res = append(res, name)
	}
	return res
}

// defaultFeatures 声明当前已实现能力的 features 清单。
// kanban.linking 与 kanban.createTaskFromThread 已就绪：kanban.createTask 接受可选 threadID
// 并落库为任务关联会话（见 kanban_methods.go 的 handleKanbanCreateTask），看板回包也带 threadID，
// Web 端据此显示「新建任务」与「存为看板任务」入口。未实现的项仍必须安全排除。
var defaultFeatures = []string{
	"threadHistoryPaging",
	"multiDevicePairing",
	"structuredThreadHistory",
	"threads.events",
	"threads.attention",
	"kanban.goals",
	"kanban.linking",
	"kanban.createTaskFromThread",
	"threads.swarm",
	"threads.queue",
	"threads.commands",
	"threads.continue",
	"threads.settleAction",
	"threads.retain",
	"threads.provisional.release",
	"threads.composerOptions",
	"threads.review",
	"requests.skip",
	"scripts.run",
	"threads.media",
	"files.read",
	"git.readonly",
	"threads.attachments",
	"push",
	"push.offline",
	"transport.tailscale",
}

// DefaultFeatures 返回当前已就绪的 features 列表副本。
func DefaultFeatures() []string {
	res := make([]string, len(defaultFeatures))
	copy(res, defaultFeatures)
	return res
}
