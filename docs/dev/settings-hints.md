# 设置项悬停说明（审阅稿）

鼠标悬停在设置项标题上时，显示一句说明。说明按 ASD-STE100 原则写：条件在前，一句一事，不写实现细节。

用词约定：
- 「打开后」「关闭后」描述开关。
- 「会话」指一次对话，「AI」指会话里干活的智能体，「模型」只在选模型时用。
- 「必须重启应用」只写在代码确认需要重启的项上。

没有列出的设置项不加说明。原因是标题已经说清，例如名称、密钥、接口地址、颜色、纯列表项、跳转行。

## 一、建议改名（待用户决定）

| 行 ID | 当前标题 | 建议标题 | 理由 |
|---|---|---|---|
| row-automation-prevent-sleep | 保持唤醒 | 自动化运行时保持唤醒 | 只在自动化运行前后生效，不是一直防睡眠 |
| row-bee-message-retention | 压缩 30 天前的子代理消息 | 自动压缩历史消息 | 还会压缩主会话的工具和思考记录 |
| row-kanban-entry-ordinary | 普通会话暴露单一入口 | 普通会话可用看板 | 「暴露单一入口」是开发用语 |
| row-agy-channel | 通道 | 连接方式 | 「通道」不说明连接的是什么 |
| row-provider-balance-breakdown | 明细 | 余额明细 | 「明细」不说明是什么的明细 |
| sec-agy-routing-sink | 冷却账号 | 冷却后的账号排到最后 | 容易读成「手动冷却账号」 |
| row-magpie-gateway-operations | 操作 | 管理 | 行里只有「刷新」「删除」 |
| row-remote-push-enabled | 应用关闭时也推送 | 手机离线时推送 | 「应用」容易读成桌面端 |
| row-nowledge-working-memory-budget | 工作记忆预算 | Working Memory 字数上限 | 与上一行用词统一，「字数上限」更直白 |

## 二、说明文案

### 通用

| 行 ID | 标题 | 说明 |
|---|---|---|
| row-mode | 默认权限 | 决定新会话里 AI 执行命令和改文件前是否先问你。修改后，必须重启应用。 |
| row-notify | 完成后通知 | AI 回复完成或自动化运行结束时，发送系统通知。 |
| row-notify-foreground | 前台也发送通知 | 打开后，窗口在前台时也发送通知。关闭后，只在窗口不在前台时发送。 |
| row-notify-approval | 待审批时通知 | AI 等你批准操作时，发送系统通知。 |
| row-finish-sound | 完成提示音 | AI 回复完成时，播放提示音。 |
| row-chime-attention | 需要介入时提示音 | 会话等你批准或长时间没有进展时，播放提示音。 |
| row-dormant-sound | 会话休眠提示音 | 会话进入休眠时，播放提示音。 |
| row-notify-stalled | 会话卡住时提醒 | 运行中的会话长时间没有进展时，发送通知。 |
| row-stalled-minutes | 多久算卡住 | 会话超过这个分钟数没有进展，就算卡住。 |
| row-tray | 菜单栏图标 / 托盘图标 | 显示应用图标和运行中的会话数。修改后，必须重启应用。 |
| row-tray-quota | 菜单栏显示额度百分比 | 在菜单栏图标旁显示账号已用额度的百分比。 |
| row-quit-action | 关闭窗口时 | 选择关闭主窗口时退出应用，还是留在托盘里继续运行。 |
| row-automation-prevent-sleep | 保持唤醒 | 自动化正在运行或 5 分钟内要运行时，电脑不进入睡眠。 |
| row-general-backup | 备份 | 把配置和密钥导出为加密文件。可以选择包含会话历史。 |
| row-general-restore | 恢复 | 用备份文件替换当前数据。恢复后，应用自动重启。 |
| row-global-shortcut | 全局快捷键 | 在任何应用里按这个组合键，都可以唤起主窗口。修改后，必须重启应用。 |
| （备份弹窗）包含会话历史 | 包含会话历史 | 打开后，备份包含全部会话记录和附件。关闭后，只备份配置和密钥。 |

### 外观

| 行 ID | 标题 | 说明 |
|---|---|---|
| row-conversation-density | 界面密度 | 调整会话、侧边栏和控件的行距。紧凑档一屏显示更多内容。 |
| row-content-width | 内容宽度 | 限制会话消息列的最大宽度。 |
| row-content-width-percent | 比例宽度 | 内容宽度选「按比例」时，消息列占会话区域的百分比。 |
| row-sidebar-compact | 紧凑侧边栏 | 打开后，侧边栏的行高和间距变小。 |
| row-motion | 动效 | 关闭后，界面不播放过渡动画，终端光标不闪烁。 |
| row-code-font | 代码字体 | 代码块和行内代码使用的字体。留空时，使用系统等宽字体。 |
| row-theme-accent-system | 跟随系统 | 打开后，强调色使用系统设置的颜色。 |

### 会话

| 行 ID | 标题 | 说明 |
|---|---|---|
| row-show-reasoning | 显示思考过程 | 在会话中显示 AI 的思考步骤。 |
| row-auto-collapse-reasoning | 自动折叠思考过程 | 打开后，AI 思考完成时自动收起思考和工具调用的细节。 |
| row-concise-replies | 简洁 AI 回复 | 打开后，AI 不写客套话和重复总结，只给结果。 |
| row-queue-while-running | 运行中排队消息 | AI 回复时发的新消息，等这一轮结束再发出。关闭后，新消息立即打断这一轮。 |
| row-insert-into-running-turn | 新消息插入当前回合 | AI 回复时发的新消息，直接加入这一轮。服务商不支持时，改为排队。 |
| row-composer-send-key | 发送消息快捷键 | 选择用 Enter 还是 Shift+Enter 发送消息。另一个组合键用来换行。 |
| row-composer-attachment-limit | 附件数量上限 | 一条消息最多可以带的图片和文件数，范围 1 到 20。 |
| row-pin-sent-message | 发送后置顶消息 | 打开后，刚发出的消息停在会话顶部，回复在下方展开。 |
| row-auto-continue-after-limit | 用尽限额后自动继续 | 额度用完而中断时，到恢复时间自动让 AI 继续。 |
| row-auto-dormant-days | 无消息多少天后自动休眠 | 会话超过这个天数没有新消息时，自动进入休眠。 |
| row-confirm-delete | 删除会话前确认 | 打开后，删除会话前先弹出确认框。 |
| row-ask-compact-before-resume | 恢复旧会话前询问压缩 | 打开超过 1 小时没动、上下文占用较高的旧会话时，先问你是否压缩。 |
| row-compact-suggest-percent | 建议压缩比例 | 上下文占用达到模型窗口的这个比例时建议压缩。 |
| row-compact-suggest-max | 建议压缩上限 | 占用达到这个大小就建议压缩，窗口未知时也按它判断。 |
| row-bee-message-retention | 压缩 30 天前的子代理消息 | 后台压缩已结束会话里的工具和思考记录，内容不丢。满 30 天的子代理思考内容将被删除。 |

### 工作台

| 行 ID | 标题 | 说明 |
|---|---|---|
| row-global-instructions | AGENTS.md | 编辑全局指令。指令对所有项目和会话生效。 |
| row-context-guidance | 注入项目说明 | 打开后，新会话第一轮自动把项目说明文档发给 AI。 |
| row-context-doc- | 注入文档 | 选择还要发给 AI 的项目文档。 |
| row-context-inject-budget | 注入字符上限 | 第一轮发给 AI 的项目文档总字数上限，避免占满上下文。 |
| row-kanban-default-view | 默认视图 | 打开看板时，先显示看板列还是列表。 |
| row-tasks-auto-merge-worktree | 验收后自动合并工作树 | 看板任务验收通过后，自动把改动合并回主工作区。 |
| row-kanban-auto-review | 完成后自动送审 | 任务执行完成后，自动移到待验收。关闭后，由你手动移动。 |
| row-kanban-run-in-worktree | 在独立工作树中执行 | 打开后，每个看板任务在单独的工作树和分支里执行，不改动主工作区。 |
| row-kanban-auto-start-on-create | 创建后自动启动 | 新建任务且没有未完成的依赖时，立即开始执行。 |
| row-kanban-auto-rescue-stuck | 卡死任务自动救援 | 任务长时间没有动静时，先催促一次。仍没有动静，就自动中断。 |
| row-kanban-stuck-nudge-timeout | 无活动提醒 | 任务超过这个秒数没有动静时，催促一次。 |
| row-kanban-stuck-interrupt-timeout | 无活动中断 | 催促后又超过这个秒数仍没有动静时，自动中断任务。 |
| row-kanban-mcp-tools | 注册看板 MCP 工具 | 打开后，AI 可以读写看板上的任务。 |
| row-kanban-entry-ordinary | 普通会话暴露单一入口 | 打开后，普通会话里的 AI 可以通过一个入口查看和操作看板。 |
| row-terminal-shell | 默认终端 | 点「在终端中打开」时启动的终端应用。内嵌终端不受影响。 |
| row-command-shell | 命令 shell | AI 在 Windows 上执行命令用的 shell。 |
| row-terminal-scrollback-lines | 回滚行数 | 内嵌终端最多保留的历史输出行数。 |
| row-browser-console-capture | 捕获控制台输出 | 打开后，网页预览记录网页的控制台日志和报错。 |
| row-browser-quick-ports | 快捷端口 | 常用的本地端口，用逗号分隔。网页预览里可以一键打开。 |

### 版本控制

| 行 ID | 标题 | 说明 |
|---|---|---|
| row-sc-generate-commit-msg | 自动生成提交信息 | 打开后，AI 根据改动写提交信息和 PR 描述。 |
| row-sc-commit-instructions | 提交信息附加说明 | AI 写提交信息和 PR 描述时，遵守这里的要求。 |
| row-sc-default-workspace-mode | 默认工作区模式 | 新建会话时，直接在项目目录工作，还是新建独立工作树。 |
| row-sc-worktree-location | 工作树位置 | 新建的工作树放在应用数据目录，还是项目目录里。 |
| row-sc-auto-cleanup-delete | 删除会话时自动清理工作树 | 删除会话时，一起删除它的工作树和分支。 |
| row-sc-auto-cleanup-merged | 自动清理已合并的工作树 | 看板任务合并后，自动删除它的工作树和分支。 |
| row-sc-show-change-badge | 显示改动数角标 | 打开后，提交按钮旁显示未提交的文件数。 |
| row-sc-init-submodules | 新建工作树时初始化子模块 | 打开后，新建工作树时自动拉取 Git 子模块。 |
| row-sc-clone-ignored | 新建工作树时克隆依赖目录 | 打开后，新建工作树时复制被 Git 忽略的依赖目录。 |
| row-sc-clone-ignored-patterns | 复制的目录名单 | 要复制到新工作树的目录或文件，用逗号分隔。 |

### 编排

| 行 ID | 标题 | 说明 |
|---|---|---|
| row-swarm-enabled | 启用编排 | 打开后，主代理可以把任务拆给多个子代理同时做。 |
| row-swarm-workspace-mode | 工作空间模式 | 子代理在独立副本里改代码，还是直接改当前工作区。 |
| row-swarm-retry-failed | 失败时自动重试 | 子代理出错时，先在原会话里继续。仍然失败，就新建子代理重做一次。 |
| row-swarm-auto-clear-finished | 自动清理已完成子代理 | 同一任务的子代理全部汇报后，从团队面板移除它们。 |
| row-swarm-auto-hides-idle-bees | 侧边栏自动隐藏空闲子代理 | 有子代理在运行时，侧边栏收起已结束的子代理。 |
| row-swarm-auto-pops-bees | 自动弹出子代理面板 | 打开后，空间足够时，每个子代理单独显示一个浮窗。 |
| row-swarm-block-compaction | 分块压缩上下文 | 打开后，会话变长时分段压缩上下文。关闭后，接近上限才一次压缩。 |
| row-swarm-archives-codex-bees | Codex 子会话历史 | 选「自动归档」时，成功完成的 Codex 子代理汇报后自动归档。归档后可以恢复。 |
| row-swarm-auto-delegate | 自动委托 | 打开后，主代理拆分任务后自动派给子代理。 |
| row-swarm-lead-plans-only | 主代理只规划 | 打开后，主代理不直接改文件，所有改动交给子代理。 |
| row-swarm-send-to-bees | 消息直接交给子代理 | 打开后，你发的每条消息不经过主代理，直接派给子代理。 |
| row-swarm-auto-forward-question | 无人回答时转给主代理 | 子代理的提问超过设定时间没人回答时，交给主代理回答。 |
| row-swarm-default-model | 默认提供方与模型 | 主代理派任务时没指定模型，就用这里的模型。 |
| row-swarm-review-bees | 合并前审查 | 子代理的改动合并前，先由 AI 或你审查。 |
| row-swarm-review-model | 审查模型 | AI 审查用的模型。留空时，自动选最便宜的可用模型。 |
| row-swarm-review-check-command | 检查命令（本项目） | 子代理改完后运行这条命令，失败就退回。留空时，使用全局默认命令。 |
| row-swarm-review-max-lines | 自动通过行数上限 | 改动行数不超过这个值时，跳过 AI 审查。0 表示关闭。 |
| row-swarm-auto-merge | 自动合并 | 子代理完成后，把改动提交到当前本地分支。 |
| row-swarm-auto-push | 自动推送 | 自动合并后，把分支推到远端并创建 PR。 |
| row-swarm-max-bees | 并发上限 | 同一任务最多同时运行的子代理数。 |
| row-swarm-max-rounds | 最多派发轮次 | 主代理在一次请求里最多派发子代理的轮数。0 表示不限。 |
| row-swarm-bee-timeout | 子代理运行超时 | 单个子代理运行超过这个时长时，自动停止。等你回答的时间不计入。 |
| row-swarm-request-timeout | 请求总时长预算 | 一次请求里所有子代理的运行时间合计超过这个时长时，停止派发。 |
| （配对编辑）主代理模型 | 主代理模型 | 负责拆分任务和审查的模型。 |
| （配对编辑）子代理模型 | 子代理模型 | 执行子任务的模型。不选时，跟随主代理。 |
| （档位）默认档 | 默认档 | 主代理派任务时没指定档位，就用这个档位。 |
| （档位编辑）用途说明 | 用途说明 | 告诉主代理这个档位适合做什么，帮它选档位。 |
| （档位编辑）写入白名单 | 写入白名单 | 这个档位只能合并匹配这些路径的改动。留空表示不限。 |
| （档位编辑）模型配置 | 模型配置 | 这个档位用的模型。不选时，跟随子代理模型。 |
| （档位编辑）档位要求 | 档位要求 (可选) | 给这个档位的子代理追加的要求。 |

### 总管

| 行 ID | 标题 | 说明 |
|---|---|---|
| row-steward-enabled | 启用总管 | 打开后，总管在专属会话里分派看板任务并汇总结果。 |
| row-steward-cli-fallback | 没有 API Key 时用本机 CLI | 总管没有 API Key 或模型出错时，改用本机命令行工具回复。 |
| row-steward-model | 前台模型 | 总管和你对话时用的模型。 |
| row-steward-task-model | 任务模型 | 总管派发看板任务时默认用的模型。选模型配对时，自动开启编排。 |
| row-steward-no-progress | 无进展提醒 | 任务或子代理长时间没有进展时，在收件箱提醒你。 |
| row-steward-quiet-enabled | 启用安静时段 | 打开后，这段时间内总管不发桌面通知和手机推送。 |
| row-steward-home-dir | 主目录 | 总管默认的工作目录。 |

### 模型与供应商

| 行 ID | 标题 | 说明 |
|---|---|---|
| row-provider-bin / row-claude-bin / row-codex-bin | 可执行文件路径 | 程序所在的路径。留空时，自动查找。 |
| row-provider-pi-cache | 长缓存 | 打开后，pi 把提示词缓存保留更久。 |
| row-provider-pi-vision | 视觉模型 | 当前模型看不了图片时，用这个模型识别图片。 |
| row-provider-tray-quota | 菜单栏额度 | 打开后，菜单栏面板显示这个供应商的剩余额度。 |
| row-agy-channel | 通道 | mcode 和 agy 通信的方式。选「自动」时，按 agy 版本选择。 |
| sec-agy-routing-strategy | 调度策略 | 有多个账号时，决定每次请求用哪个账号。 |
| sec-agy-routing-affinity | 会话保持模式 | 决定同一会话的请求是否一直用同一个账号。 |
| sec-agy-routing-sink | 冷却账号 | 打开后，被限流冷却过的账号恢复后排在最后使用。 |
| sec-agy-auto-warmup | 到期自动预热 | Gemini 周额度到期但没有刷新时，自动发一次请求开启新周期。 |

### Magpie

| 行 ID | 标题 | 说明 |
|---|---|---|
| row-magpie-agent-claude / -codex / -pi | Claude / Codex / pi | 这个智能体怎样连接 Magpie 网关，以及默认模型。 |
| row-magpie-gateway-enabled | 启用 | 打开后，智能体和模型列表可以使用这个网关。 |
| row-magpie-persona-weburl | 人设管理地址 | Magpie 网页管理地址。填写后，自动拉取人设列表。 |
| row-magpie-persona-json | 手动人设列表 | 手动填写人设列表。必须勾选下方「人设规则」才生效。 |
| row-magpie-persona-rules | 人设规则 | 网关已安装人设规则插件时勾选。勾选后，手动人设列表才生效。 |
| row-magpie-gateway-quota | 菜单栏额度 | 勾选要在菜单栏面板显示额度的供应商。 |

### 远程控制

| 行 ID | 标题 | 说明 |
|---|---|---|
| row-remote-enabled | 启用远程控制服务 | 打开后，已配对的手机或浏览器可以查看和操作会话。 |
| row-remote-push-enabled | 应用关闭时也推送 | 手机没有打开远程页面时，也推送审批和完成提醒。 |
| row-remote-port | 监听端口 | 局域网访问用的端口。修改后，必须重启服务。 |
| row-remote-hosts | 额外允许的访问域名 | 通过自定义域名或代理访问时，在这里填写域名。修改后，必须重启服务。 |
| row-remote-tailscale-enabled | 通过 Tailscale 访问 | 打开后，同一 Tailscale 网络里的设备可以通过加密地址连接。 |
| row-remote-tailscale-hostname | 节点名称 | 本机在 Tailscale 网络里的名字。 |
| row-remote-tailscale-reset | 重置节点 | 让本机退出 Tailscale 网络并清除登录状态。之后必须重新授权。 |
| row-remote-tls-enabled | 启用 HTTPS | 打开后，使用你自己的域名和证书加密远程访问。 |
| row-remote-tls-port | HTTPS 端口 | 自定义域名访问用的端口。修改后，必须重启服务。 |
| row-remote-tls-token | Cloudflare API 令牌 | 申请证书时，用这个令牌自动验证域名。 |
| row-remote-pairing-target | 二维码入口 | 选择二维码和链接使用哪种访问地址。 |

### 扩展（MCP、技能、记忆）

| 行 ID | 标题 | 说明 |
|---|---|---|
| row-mcp-fold-tools | 折叠大量工具 | 服务的工具超过 40 个时，合并成一个入口，节省上下文。 |
| row-mcp-newthread-〈服务〉 | 新会话默认启用 | 打开后，新会话自动加载这个服务。关闭后，需要时手动打开。 |
| row-form-transport | 传输方式 | 在本机运行命令启动服务，还是连接远程服务地址。 |
| 〈服务〉-tools-batch-row | 全部禁用 | 打开后，AI 不能调用这个服务的任何工具。 |
| 〈服务〉-tools-batch-row | 全部免审批 | 打开后，AI 调用这个服务的工具时不再请你批准。 |
| 〈工具〉 | 禁用 | 打开后，AI 不能调用这个工具。 |
| 〈工具〉 | 免审批 | 打开后，AI 调用这个工具时不再请你批准。 |
| row-memory-backend | 记忆后端 | 选择记忆存在哪里。修改后，必须重启应用。 |
| row-nowledge-inject-working-memory | 新会话注入 Working Memory | 打开后，新会话第一轮附带当前项目的每日记忆简报。 |
| row-nowledge-working-memory-budget | 工作记忆预算 | 每日记忆简报的最大字数。超出的部分不发送。 |
| row-nowledge-inject-memory-tools-note | 向新会话说明记忆工具用法 | 打开后，新会话第一轮告诉 AI 怎样检索和保存记忆。 |
| row-nowledge-bee-recall | 派发子代理时附带相关记忆 | 打开后，派子代理前自动找出最多 5 条相关记忆交给它。 |
| row-nowledge-sync-threads | 同步会话到 Nowledge | 打开后，会话空闲时自动存入记忆库，供以后检索。 |
| row-nowledge-sync-helper-threads | 同步编排子代理会话 | 打开后，子代理的会话也存入记忆库。 |
| row-nowledge-sync-automation-threads | 同步自动化运行会话 | 打开后，自动化运行的会话也存入记忆库。 |
| row-memory-space-〈序号〉 | 〈项目名〉 | 这个项目用哪个记忆空间。选「自动匹配」时，按目录名匹配。 |
| row-about-auto-update | 自动检查更新 | 打开后，应用定期检查新版本。 |
| row-about-datadir | 数据目录 | 应用数据的存放位置。更改后，重启时把数据迁到新位置。 |

## 三、暂不处理

- 存储页的分组行（已删会话残留、无主补丁、旧备份、旧日志）：每组含义不同，需要逐组写，本稿未起草。
- `internal/ui/settings_plan_limits.go:185` 「储存的重置次数」：当前所有调用方都传 0，这一行不会显示，含义未确认。
- `row-agy-channel-update`「需要 1.5.8 以上」：这是状态提示行，不是设置项，不加说明。
- MCP 服务开关、技能开关：开关含义一看就懂，不加说明。
- 配对编辑里的档位映射行：标题是档位胶囊，不是文字，不加说明。
