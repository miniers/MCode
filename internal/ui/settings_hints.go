package mcodeui

import "strings"

// settingHints 保存各设置项标题悬停时显示的说明文案（按行 ID 索引）。
var settingHints = map[string]string{
	// 通用
	"row-mode":                     "决定新会话里 AI 执行命令和改文件前是否先问你。修改后，必须重启应用。",
	"row-notify":                   "AI 回复完成或自动化运行结束时，发送系统通知。",
	"row-notify-foreground":        "打开后，窗口在前台时也发送通知。关闭后，只在窗口不在前台时发送。",
	"row-notify-approval":          "AI 等你批准操作时，发送系统通知。",
	"row-finish-sound":             "AI 回复完成时，播放提示音。",
	"row-chime-attention":          "会话等你批准或长时间没有进展时，播放提示音。",
	"row-dormant-sound":            "会话进入休眠时，播放提示音。",
	"row-notify-stalled":           "运行中的会话长时间没有进展时，发送通知。",
	"row-stalled-minutes":          "会话超过这个分钟数没有进展，就算卡住。",
	"row-tray":                     "显示应用图标和运行中的会话数。修改后，必须重启应用。",
	"row-tray-quota":               "在菜单栏图标旁显示账号已用额度的百分比。",
	"row-quit-action":              "选择关闭主窗口时退出应用，还是留在托盘里继续运行。",
	"row-automation-prevent-sleep": "自动化正在运行或 5 分钟内要运行时，电脑不进入睡眠。",
	"row-general-backup":           "把配置和密钥导出为加密文件。可以选择包含会话历史。",
	"row-general-restore":          "用备份文件替换当前数据。恢复后，应用自动重启。",
	"row-global-shortcut":          "在任何应用里按这个组合键，都可以唤起主窗口。修改后，必须重启应用。",
	"backup-include-history":       "打开后，备份包含全部会话记录和附件。关闭后，只备份配置和密钥。",

	// 外观
	"row-conversation-density":  "调整会话、侧边栏和控件的行距。紧凑档一屏显示更多内容。",
	"row-content-width":         "限制会话消息列的最大宽度。",
	"row-content-width-percent": "内容宽度选「按比例」时，消息列占会话区域的百分比。",
	"row-sidebar-compact":       "打开后，侧边栏的行高和间距变小。",
	"row-motion":                "关闭后，界面不播放过渡动画，终端光标不闪烁。",
	"row-code-font":             "代码块和行内代码使用的字体。留空时，使用系统等宽字体。",
	"row-theme-accent-system":   "打开后，强调色使用系统设置的颜色。",

	// 会话
	"row-show-reasoning":            "在会话中显示 AI 的思考步骤。",
	"row-auto-collapse-reasoning":   "打开后，AI 思考完成时自动收起思考和工具调用的细节。",
	"row-concise-replies":           "打开后，AI 不写客套话和重复总结，只给结果。",
	"row-queue-while-running":       "AI 回复时发的新消息，等这一轮结束再发出。关闭后，新消息立即打断这一轮。",
	"row-insert-into-running-turn":  "AI 回复时发的新消息，直接加入这一轮。服务商不支持时，改为排队。",
	"row-composer-send-key":         "选择用 Enter 还是 Shift+Enter 发送消息。另一个组合键用来换行。",
	"row-composer-attachment-limit": "一条消息最多可以带的图片和文件数，范围 1 到 20。",
	"row-pin-sent-message":          "打开后，刚发出的消息停在会话顶部，回复在下方展开。",
	"row-auto-continue-after-limit": "额度用完而中断时，到恢复时间自动让 AI 继续。",
	"row-auto-dormant-days":         "会话超过这个天数没有新消息时，自动进入休眠。",
	"row-confirm-delete":            "打开后，删除会话前先弹出确认框。",
	"row-ask-compact-before-resume": "打开超过 1 小时没动、上下文占用较高的旧会话时，先问你是否压缩。",
	"row-bee-message-retention":     "后台压缩已结束会话里的工具和思考记录，内容不丢。满 30 天的子代理思考内容将被删除。",
	"row-archived-bee-cleanup":      "后台自动清理已归档且超过 30 天未活动的子代理会话，保留任务结论。",

	// 工作台
	"row-global-instructions":            "编辑全局指令。指令对所有项目和会话生效。",
	"row-context-guidance":               "打开后，新会话第一轮自动把项目说明文档发给 AI。",
	"row-context-doc-":                   "选择还要发给 AI 的项目文档。",
	"row-context-inject-budget":          "第一轮发给 AI 的项目文档总字数上限，避免占满上下文。",
	"row-kanban-default-view":            "打开看板时，先显示看板列还是列表。",
	"row-tasks-auto-merge-worktree":      "看板任务验收通过后，自动把改动合并回主工作区。",
	"row-kanban-auto-review":             "任务执行完成后，自动移到待验收。关闭后，由你手动移动。",
	"row-kanban-run-in-worktree":         "打开后，每个看板任务在单独的工作树和分支里执行，不改动主工作区。",
	"row-kanban-auto-start-on-create":    "新建任务且没有未完成的依赖时，立即开始执行。",
	"row-kanban-auto-rescue-stuck":       "任务长时间没有动静时，先催促一次。仍没有动静，就自动中断。",
	"row-kanban-stuck-nudge-timeout":     "任务超过这个秒数没有动静时，催促一次。",
	"row-kanban-stuck-interrupt-timeout": "催促后又超过这个秒数仍没有动静时，自动中断任务。",
	"row-kanban-mcp-tools":               "打开后，AI 可以读写看板上的任务。",
	"row-kanban-entry-ordinary":          "打开后，普通会话里的 AI 可以通过一个入口查看和操作看板。",
	"row-terminal-shell":                 "点「在终端中打开」时启动的终端应用。内嵌终端不受影响。",
	"row-command-shell":                  "AI 在 Windows 上执行命令用的 shell。",
	"row-terminal-scrollback-lines":      "内嵌终端最多保留的历史输出行数。",
	"row-browser-console-capture":        "打开后，网页预览记录网页的控制台日志和报错。",
	"row-browser-quick-ports":            "常用的本地端口，用逗号分隔。网页预览里可以一键打开。",

	// 版本控制
	"row-sc-generate-commit-msg":    "打开后，AI 根据改动写提交信息和 PR 描述。",
	"row-sc-commit-instructions":    "AI 写提交信息和 PR 描述时，遵守这里的要求。",
	"row-sc-default-workspace-mode": "新建会话时，直接在项目目录工作，还是新建独立工作树。",
	"row-sc-worktree-location":      "新建的工作树放在应用数据目录，还是项目目录里。",
	"row-sc-auto-cleanup-delete":    "删除会话时，一起删除它的工作树和分支。",
	"row-sc-auto-cleanup-merged":    "看板任务合并后，自动删除它的工作树和分支。",
	"row-sc-show-change-badge":      "打开后，提交按钮旁显示未提交的文件数。",
	"row-sc-init-submodules":        "打开后，新建工作树时自动拉取 Git 子模块。",
	"row-sc-clone-ignored":          "打开后，新建工作树时复制被 Git 忽略的依赖目录。",
	"row-sc-clone-ignored-patterns": "要复制到新工作树的目录或文件，用逗号分隔。",

	// 编排
	"row-swarm-enabled":               "打开后，主代理可以把任务拆给多个子代理同时做。",
	"row-swarm-workspace-mode":        "子代理在独立副本里改代码，还是直接改当前工作区。",
	"row-swarm-retry-failed":          "子代理出错时，先在原会话里继续。仍然失败，就新建子代理重做一次。",
	"row-swarm-auto-clear-finished":   "同一任务的子代理全部汇报后，从团队面板移除它们。",
	"row-swarm-auto-hides-idle-bees":  "有子代理在运行时，侧边栏收起已结束的子代理。",
	"row-swarm-auto-pops-bees":        "打开后，空间足够时，每个子代理单独显示一个浮窗。",
	"row-swarm-block-compaction":      "打开后，会话变长时分段压缩上下文。关闭后，接近上限才一次压缩。",
	"row-swarm-archives-codex-bees":   "选「自动归档」时，成功完成的 Codex 子代理汇报后自动归档。归档后可以恢复。",
	"row-swarm-auto-delegate":         "打开后，主代理拆分任务后自动派给子代理。",
	"row-swarm-lead-plans-only":       "打开后，主代理不直接改文件，所有改动交给子代理。",
	"row-swarm-send-to-bees":          "打开后，你发的每条消息不经过主代理，直接派给子代理。",
	"row-swarm-auto-forward-question": "子代理的提问超过设定时间没人回答时，交给主代理回答。",
	"row-swarm-default-model":         "主代理派任务时没指定模型，就用这里的模型。",
	"row-swarm-review-bees":           "子代理的改动合并前，先由 AI 或你审查。",
	"row-swarm-review-model":          "AI 审查用的模型。留空时，自动选最便宜的可用模型。",
	"row-swarm-review-check-command":  "子代理改完后运行这条命令，失败就退回。留空时，使用全局默认命令。",
	"row-swarm-review-max-lines":      "改动行数不超过这个值时，跳过 AI 审查。0 表示关闭。",
	"row-swarm-auto-merge":            "子代理完成后，把改动提交到当前本地分支。",
	"row-swarm-auto-push":             "自动合并后，把分支推到远端并创建 PR。",
	"row-swarm-max-bees":              "同一任务最多同时运行的子代理数。",
	"row-swarm-max-rounds":            "主代理在一次请求里最多派发子代理的轮数。0 表示不限。",
	"row-swarm-bee-timeout":           "单个子代理运行超过这个时长时，自动停止。等你回答的时间不计入。",
	"row-swarm-request-timeout":       "一次请求里所有子代理的运行时间合计超过这个时长时，停止派发。",
	"swarm-pair-lead-model":           "负责拆分任务和审查的模型。",
	"swarm-pair-bee-model":            "执行子任务的模型。不选时，跟随主代理。",
	"swarm-tiers-default":             "主代理派任务时没指定档位，就用这个档位。",
	"swarm-tier-purpose":              "告诉主代理这个档位适合做什么，帮它选档位。",
	"swarm-tier-write-allow":          "这个档位只能合并匹配这些路径的改动。留空表示不限。",
	"swarm-tier-model":                "这个档位用的模型。不选时，跟随子代理模型。",
	"swarm-tier-requirements":         "给这个档位的子代理追加的要求。",

	// 总管
	"row-steward-enabled":       "打开后，总管在专属会话里分派看板任务并汇总结果。",
	"row-steward-cli-fallback":  "总管没有 API Key 或模型出错时，改用本机命令行工具回复。",
	"row-steward-model":         "总管和你对话时用的模型。",
	"row-steward-task-model":    "总管派发看板任务时默认用的模型。选模型配对时，自动开启编排。",
	"row-steward-no-progress":   "任务或子代理长时间没有进展时，在收件箱提醒你。",
	"row-steward-quiet-enabled": "打开后，这段时间内总管不发桌面通知和手机推送。",
	"row-steward-home-dir":      "总管默认的工作目录。",

	// 模型与供应商
	"row-provider-bin":         "程序所在的路径。留空时，自动查找。",
	"row-claude-bin":           "程序所在的路径。留空时，自动查找。修改后，必须重启应用。",
	"row-codex-bin":            "程序所在的路径。留空时，自动查找。修改后，必须重启应用。",
	"row-provider-auth":        "选择 droid 的登录方式。修改后，必须重启应用。",
	"row-provider-pi-cache":    "打开后，pi 把提示词缓存保留更久。",
	"row-provider-pi-vision":   "当前模型看不了图片时，用这个模型识别图片。",
	"row-provider-tray-quota":  "打开后，菜单栏面板显示这个供应商的剩余额度。",
	"row-agy-channel":          "mcode 和 agy 通信的方式。选「自动」时，按 agy 版本选择。",
	"sec-agy-routing-strategy": "有多个账号时，决定每次请求用哪个账号。",
	"sec-agy-routing-affinity": "决定同一会话的请求是否一直用同一个账号。",
	"sec-agy-routing-sink":     "打开后，被限流冷却过的账号恢复后排在最后使用。",
	"sec-agy-auto-warmup":      "Gemini 周额度到期但没有刷新时，自动发一次请求开启新周期。",

	// Magpie
	"row-magpie-gateway-enabled": "打开后，智能体和模型列表可以使用这个网关。",
	"row-magpie-persona-weburl":  "Magpie 网页管理地址。填写后，自动拉取人设列表。",
	"row-magpie-persona-json":    "手动填写人设列表。必须勾选下方「人设规则」才生效。",
	"row-magpie-persona-rules":   "网关已安装人设规则插件时勾选。勾选后，手动人设列表才生效。",
	"row-magpie-gateway-quota":   "勾选要在菜单栏面板显示额度的供应商。",

	// 远程控制
	"row-remote-enabled":            "打开后，已配对的手机或浏览器可以查看和操作会话。",
	"row-remote-push-enabled":       "手机没有打开远程页面时，也推送审批和完成提醒。",
	"row-remote-port":               "局域网访问用的端口。修改后，必须重启服务。",
	"row-remote-hosts":              "通过自定义域名或代理访问时，在这里填写域名。修改后，必须重启服务。",
	"row-remote-tailscale-enabled":  "打开后，同一 Tailscale 网络里的设备可以通过加密地址连接。",
	"row-remote-tailscale-hostname": "本机在 Tailscale 网络里的名字。",
	"row-remote-tailscale-reset":    "让本机退出 Tailscale 网络并清除登录状态。之后必须重新授权。",
	"row-remote-tls-enabled":        "打开后，使用你自己的域名和证书加密远程访问。",
	"row-remote-tls-port":           "自定义域名访问用的端口。修改后，必须重启服务。",
	"row-remote-tls-token":          "申请证书时，用这个令牌自动验证域名。",
	"row-remote-pairing-target":     "选择二维码和链接使用哪种访问地址。",

	// 扩展（MCP、技能、记忆）
	"row-mcp-fold-tools":                    "服务的工具超过 40 个时，合并成一个入口，节省上下文。",
	"row-form-transport":                    "在本机运行命令启动服务，还是连接远程服务地址。",
	"mcp-tool-disable":                      "打开后，AI 不能调用这个工具。",
	"mcp-tool-approve":                      "打开后，AI 调用这个工具时不再请你批准。",
	"row-memory-backend":                    "选择记忆存在哪里。修改后，必须重启应用。",
	"row-nowledge-inject-working-memory":    "打开后，新会话第一轮附带当前项目的每日记忆简报。",
	"row-nowledge-working-memory-budget":    "每日记忆简报的最大字数。超出的部分不发送。",
	"row-nowledge-inject-memory-tools-note": "打开后，新会话第一轮告诉 AI 怎样检索和保存记忆。",
	"row-nowledge-bee-recall":               "打开后，派子代理前自动找出最多 5 条相关记忆交给它。",
	"row-nowledge-sync-threads":             "打开后，会话空闲时自动存入记忆库，供以后检索。",
	"row-nowledge-sync-helper-threads":      "打开后，子代理的会话也存入记忆库。",
	"row-nowledge-sync-automation-threads":  "打开后，自动化运行的会话也存入记忆库。",
	"row-memory-space":                      "这个项目用哪个记忆空间。选「自动匹配」时，按目录名匹配。",
	"row-about-auto-update":                 "打开后，应用定期检查新版本。",
	"row-about-datadir":                     "应用数据的存放位置。更改后，重启时把数据迁到新位置。",
}

// settingHintPrefixes 保存带动态后缀设置行的说明文案（按前缀匹配）。
var settingHintPrefixes = map[string]string{
	"row-mcp-newthread-": "打开后，新会话自动加载这个服务。关闭后，需要时手动打开。",
	"row-magpie-agent-":  "这个智能体怎样连接 Magpie 网关，以及默认模型。",
}

// settingHintSuffixes 保存带动态前缀设置行的说明文案（按后缀匹配）。
var settingHintSuffixes = map[string]string{
	"-row-tools-available": "关闭后，AI 不能调用这个服务的任何工具。",
	"-row-tools-approve":   "打开后，AI 调用这个服务的工具时不再请你批准。",
}

// settingHint 按行 ID 返回悬停说明文案；未配置时返回空串。
func settingHint(key string) string {
	if key == "" {
		return ""
	}
	if h, ok := settingHints[key]; ok {
		return h
	}
	for prefix, h := range settingHintPrefixes {
		if strings.HasPrefix(key, prefix) {
			return h
		}
	}
	for suffix, h := range settingHintSuffixes {
		if strings.HasSuffix(key, suffix) {
			return h
		}
	}
	return ""
}
