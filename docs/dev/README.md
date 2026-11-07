# MCode 开发文档

这套文档记录 MCode 当前的功能和实现方式，供后续开发迭代查阅。内容基于 `v0.1.158`（2026-11-02）的代码整理。

文档和代码不一致时，以代码为准。改动子系统的行为或入口时，请同步更新对应章节。协议细节以 [`docs/remote-control.schema.json`](../remote-control.schema.json) 为准，这里不重复字段定义。

## 阅读顺序

第一次接触这个仓库，建议先读 1、2、11，再按要改的子系统查对应章节。

| 文档 | 内容 |
|---|---|
| [01-architecture.md](01-architecture.md) | 产品形态、分层、依赖规则、启动与关闭、数据目录 |
| [02-session-runtime.md](02-session-runtime.md) | 会话运行内核：发送链路、回合循环、队列、审批、自愈、提示词注入 |
| [03-providers.md](03-providers.md) | `harness` 抽象、各 Provider 的接入方式、新增 Provider 的步骤 |
| [04-swarm.md](04-swarm.md) | 编排：主代理、子代理、工作副本、合并、汇报 |
| [05-steward-attention.md](05-steward-attention.md) | 总管、话题、账本、收件箱 |
| [06-kanban-automation.md](06-kanban-automation.md) | 看板、目标、验收、自动化 |
| [07-mcp-context-memory.md](07-mcp-context-memory.md) | MCP、技能、项目 Context、Nowledge 记忆 |
| [08-remote-web.md](08-remote-web.md) | 远程协议、服务端、配对、推送、Web 前端 |
| [09-desktop-ui.md](09-desktop-ui.md) | 桌面 UI 的结构、各功能面板、设置页、快捷键 |
| [10-store-config.md](10-store-config.md) | SQLite 存储、迁移规则、配置结构、密钥 |
| [11-dev-guide.md](11-dev-guide.md) | 构建、测试、打包、守卫测试、常见改动清单、已知差异 |

## 术语

| 术语 | 含义 | 代码中的名字 |
|---|---|---|
| 会话 | 一条对话线程，绑定项目和 Provider | `thread` |
| 回合 | 用户或宿主发一条消息后，Provider 跑完一次的过程 | `turn` |
| Provider | 接入的 AI 编码工具或模型端点 | `harness.Harness` 的实现 |
| 主代理 | 开启编排的主会话 | `lead` |
| 子代理 | 主代理派出的工作会话 | `bee`（旧名 `head`） |
| 编排 | 主代理派发和合并子代理的整套机制 | `swarm`（旧名 `hydra`） |
| 总管 | 常驻的「同事」会话，负责跟踪事件和转交工作（旧称「默默」） | `steward` |
| 话题 | 总管为一件具体工作开的独立会话和计划 | `steward.Topic` |
| 账本 | 总管维护的进行中、待决定、待用户事项 | `steward.LedgerEntry` |
| 收件箱 | 需要处理的提醒事件 | `attention` |
| 目标 | 看板上由规划器拆成多个任务的大目标 | `kanban` 的 `Goal` |
| 宿主 | MCode 自身，相对于 Provider 而言 | `host` |

历史命名的详细对照见 [11-dev-guide.md](11-dev-guide.md#历史命名)。

## 功能 → 代码入口

### 会话与聊天

| 功能 | 后端入口 | 桌面 UI | Web |
|---|---|---|---|
| 发送消息、回合循环 | `internal/remote/memory_backend.go`（`SendMessage`、`runHarness`） | `internal/ui/composer.go`、`internal/ui/chat.go` | `web/src/components/ThreadComposer.tsx` |
| 时间线渲染 | 同上 | `internal/ui/timeline_rows.go`、`internal/ui/timeline_incremental.go` | `web/src/components/ThreadTimeline.tsx` |
| 排队和插入当前回合 | `internal/remote/memory_backend.go`、`internal/remote/runtime_prompts.go` | `internal/ui/composer_queue.go` | `ThreadComposer.tsx` |
| 审批和提问 | `internal/remote/approvals.go`、`internal/remote/always_allow_store.go` | `internal/ui/approval.go` | `requests.*` 方法 |
| 撤回和回退回合 | `RecallTurn`、`PreviewTurnRevert`、`ExecuteTurnRevert`；`internal/git/checkpoint.go` | `internal/ui/timeline_menu.go` | `threads.turn.recall` |
| 分割会话 | `RequestSplit`、`internal/remote/session_rotation.go` | 时间线分割线 | `threads.splitSession` |
| 跨会话搜索 | `internal/store/message_search.go`、`internal/remote/message_search_methods.go` | `internal/ui/palette_message_search.go` | `messages.search` |
| 会话导出 | | `internal/ui/thread_export.go` | |
| 自动标题、提交信息 | `internal/textgen`、`internal/app/textgen_wiring.go` | | |

### 编排、总管、看板、自动化

| 功能 | 后端入口 | 桌面 UI | Web |
|---|---|---|---|
| 子代理派发和合并 | `internal/swarm/manager.go`、`internal/app/swarm_wiring.go` | `internal/ui/swarm*.go`、`internal/ui/sidebar_bees.go` | `web/src/components/Swarm*` |
| 自动合并流水线 | `internal/swarm/automerge.go` | | |
| 总管运行时 | `internal/steward`、`internal/app/steward_wiring.go` | `internal/ui/steward_*.go`、`internal/ui/sidebar_steward.go` | `web/src/components/Steward*` |
| 收件箱 | `internal/attention`、`internal/app/attention_wiring.go` | `internal/ui/attention_inbox.go` | `AttentionSheet.tsx` |
| 看板和目标 | `internal/kanban` | `internal/ui/kanban*.go`、`internal/ui/tasks_window.go` | `KanbanBoard.tsx`、`TaskDetailModal.tsx` |
| 自动化 | `internal/automation`、`internal/remote/sleepguard*.go` | `internal/ui/automation*.go` | `web/src/components/Automation*` |

### 扩展、代码、设置

| 功能 | 后端入口 | 桌面 UI | Web |
|---|---|---|---|
| MCP 服务与代理 | `internal/mcp`、`internal/agentconf` | `internal/ui/settings_page_mcp.go`、`internal/ui/composer_mcp_menu.go` | `mcp.servers.list` |
| 技能 | `internal/agentconf`、`internal/projctx` | `internal/ui/settings_page_skills.go`、`internal/ui/composer_skills.go` | |
| 项目文档 | `internal/projctx` | `internal/ui/context_panel*.go` | |
| 记忆 | `internal/nmem`、`internal/memspace`、`internal/remote/nmem_sync.go`、`internal/app/nowledge_tools.go` | `internal/ui/memory_panel*.go`、`internal/ui/settings_page_nowledge.go` | `MemoryProposalsSheet.tsx` |
| Git、PR、worktree | `internal/git` | `internal/ui/git_*_ui.go`、`internal/ui/diff_panel.go` | `web/src/components/Git*` |
| 终端和预览 | | `internal/ui/terminal*.go`、`internal/ui/preview*.go` | |
| 文件和脚本 | `internal/remote/files.go`、`internal/remote/scripts.go`、`internal/remote/project_scripts.go` | `internal/ui/file_manager.go` | `FileBrowserSheet.tsx`、`ScriptsSheet.tsx` |
| 额度和余额 | `internal/remote/quota.go`、`internal/providers/credits` | `internal/ui/composer_usage_panel.go`、`internal/ui/tray_quota_panel.go` | `composer.quota` |
| 远程服务 | `internal/remote/server.go`、`internal/app/remote_service.go` | `internal/ui/settings_page_remote.go` | `web/src/context/RemoteContext.tsx` |
| 设置 | `internal/config` | `internal/ui/settings_pages.go`、`internal/ui/settings_page_*.go` | `web/src/components/SettingsForm.tsx` |
| 通知、托盘、快捷键 | `internal/native`、`cmd/mcode/native.go` | `internal/ui/shortcuts.go`、`internal/ui/app_menu.go` | |
