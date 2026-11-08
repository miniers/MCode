# 07 MCP、技能、项目 Context、记忆

## MCP

代码在 `internal/mcp`。MCode 同时扮演两个角色：给 Provider 提供内置工具的 MCP 服务，以及连接用户配置的上游 MCP 服务的代理。

| 文件 | 内容 |
|---|---|
| `server.go`、`registry.go` | 内置服务和工具注册表（`Registry.Register`） |
| `tools_*.go` | 按域拆分的工具实现：看板、自动化、项目文档、记忆、编排 |
| `proxy.go` | 按会话路由；`filterDisabledTools` 应用工具策略；工具过多时折叠成 `call_tool` |
| `hub.go` | 上游连接和空闲回收 |
| `oauth.go` | 上游 OAuth，令牌存 keyring（服务名 `mcode.mcp-oauth`） |
| `session.go` | `SessionRouter.Endpoints`、`UntrustedServerNames` |
| `sse_client.go`、`probe.go` | SSE 传输和探测 |
| `idempotency.go` | 幂等 |

### 注入到 Provider

`internal/app/app.go` 的 `providerMCP` 算出三类列表，交给各 Provider：

- builtin：内置服务。
- external：用户配置的上游。
- blocked：屏蔽列表（如 Codex 的 `BlockedMCP`）。

不是所有 Provider 都支持 MCP，例如 `omp` 不接。Pi 通过 `mcp_bridge.ts` 接入。

### 内置工具

| 分类 | 工具 |
|---|---|
| 看板 | `kanban`（入口）、`kanban_create_draft`、`kanban_create_goal`、`kanban_create_task`、`kanban_delete_card`、`kanban_delete_task`、`kanban_goal_status`、`kanban_list_tasks`、`kanban_move_card`、`kanban_move_task`、`kanban_read_board`、`kanban_read_card`、`kanban_read_goal`、`kanban_review_task`、`kanban_set_goal`、`kanban_start_task`、`kanban_task_diff`、`kanban_update_card`、`kanban_update_task` |
| 自动化 | `automation`（入口）、`automation_cancel`、`automation_create`、`automation_list`、`automation_report_result`、`automation_run_now`、`automation_update` |
| 会话 | `thread_fetch_messages`、`thread_interrupt`、`thread_search`、`thread_send_message` |
| 编排 | `swarm_delegate`、`swarm_report_get` |
| 项目文档 | `context_list_documents`、`context_read_document` |
| 记忆 | `memory`（入口）、`memory_get`、`memory_propose`、`memory_search`、`memory_forget` |
| 代理 | `call_tool` |

普通会话只看到入口工具 `kanban`、`automation`、`memory`，用 `action` 调用族内操作（`action=help` 查看清单）；类型化工具只给看板线程与无会话作用域的共享端点。入口只负责改写调用，参数校验、能力裁剪、幂等与写串行化仍按内部工具名执行。

## 技能和外部配置（agentconf）

`internal/agentconf` 扫描 Claude、Codex、Cursor、Factory、Antigravity、Gemini、Pi 的技能和 MCP 配置，统一展示。

- 技能的停用表、插件技能。
- `mcp_catalog.go`：MCP 目录预设。
- 未信任判定：来自当前仓库内配置文件的 MCP 服务标为 `Untrusted`，用户确认信任前不得执行其命令。外部 agent 可能自己读取工作目录的配置并拉起命令，所以 `UntrustedServerNames` 的结果要在线程配置里强制关闭。
- `mcp_tool_policy.go`：工具策略，存 `mcp_tool_policy.json`。

技能如何到达模型：

- 大多数 Provider 由自身 CLI 发现技能。
- 首轮 guidance 里带技能目录（子代理用精简目录）。
- Pi 用 `piSkillManifest` 显式传 `--no-skills` 加 `--skill`。

## 项目 Context（projctx）

`internal/projctx` 管理 `<project>/.mcode/context/` 下的文档：`instructions.md`、`overview.md`、`decisions.md`、`notes.md`、`docs/*.md`。

- 文档写入用哈希做比较并交换，冲突返回 `ConflictError`。
- `guidance.go` 生成首轮注入内容，预算 32000，包在 `<project_context>` 里。
- memo 提议已合并到 `memory_propose`，由记忆面板审核；`memo.go` 的 `DraftMemo` 已无生产调用方，仅保留给旧 inbox 迁移测试；旧 inbox 文件在启动时迁入待审提议。
- skills 索引。
- watch 防抖：文件变化后更新 UI。

UI：`internal/ui/context_panel*.go`（快捷键 ⇧⌘K）。

## 记忆（Nowledge Mem）

MCode 对接外部的 Nowledge Mem 服务。

| 位置 | 内容 |
|---|---|
| `internal/nmem` | REST 客户端：`WorkingMemory`、`ImportThread`、`Search`、`AddMemory`、`DistillPreview` 等 |
| `internal/memspace` | 解析 `NMEM_SPACE`，只匹配已有空间 |
| `internal/remote/nmem_sync.go` 及同前缀文件 | 首轮注入工作记忆，同步会话 |
| `internal/app/nowledge_tools.go` | 内置 `memory_*` 工具的实现 |
| `memory_proposals` 表 | 记忆提议收件箱 |

代理调用 `memory_propose` 产生提议，用户接受后写入。`user_requested` 的提议表示用户明确要求记住，和接受提议共用写入逻辑，不再等确认。RPC：`memory.proposals.list`、`accept`、`reject`，通知 `memory.proposals.changed`。

UI：`internal/ui/memory_panel*.go`、`internal/ui/settings_page_nowledge.go`；Web 的 `MemoryProposalsSheet.tsx`。

### 本地记忆后端

本地后端将项目记忆写入项目的 `.mcode/context`，全局记忆写入 `~/.mcode/context`。条目 ID 格式为 `<scope>:<doc>#<标题>`。本地后端与 Nowledge Mem 二选一，切换后需重启。

- 小节首行可带 `<!-- mcode:meta {...} -->` 注释，保存类型、重要度与标签。
- `project:overview#当前重点` 在文档或小节缺失时会自动创建。
- 全局规则也注入 bee，工具说明只给非 bee 会话。
- `thread_search` 覆盖同一主 checkout 的所有 worktree，返回的匹配编号可直接作 `thread_fetch_messages` 的 offset。
- 全局层只在本地后端显示（Context 面板的「项目 / 全局」切换）。

## 文本生成（textgen）

`internal/textgen` 生成会话标题、提交信息和分支 slug。接线在 `internal/app/textgen_wiring.go`、`internal/app/textgen_dispatch.go`。提交信息的模型按此顺序取：设置页指定的模型，通用 Provider 的默认模型。`models.roles.utility` 已弃用，运行时不再读取，仅为兼容旧配置保留。

## 改动提示

- 新增内置工具：实现放对应的 `tools_*.go`，用 `Registry.Register` 注册，并检查工具是否需要出现在 `policy` 提示词里。
- 工具涉及写操作时要考虑 `idempotency.go`。
