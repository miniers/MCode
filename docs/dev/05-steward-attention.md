# 05 总管与收件箱

## 总管是什么

总管（`steward`，旧称「默默」）是一个常驻的「同事」会话。它接收各子系统的事件，判断要不要处理，并把工作转交给话题会话或看板。它有自己的账本，主会话会定期换新以控制上下文。

- 运行时：`internal/steward`。
- 和其他子系统的接线：`internal/app/steward_*.go`。
- 写权限跟随全局默认档（`StewardRuntimeMode=""`）。
- 主目录：`<dataDir>/steward/`。

配置在 `orchestration.steward`：`enabled`、`name`、`avatar`、`provider`、`model`、`effort`、`plan_*`、`daily_wake_limit`（默认 30）、`home_dir`、`quiet_start`、`quiet_end`、`daily_report`、`event_policies`、`rotate_idle_minutes`、`rotate_context_tokens`（默认 120000）、`no_progress_minutes`（默认 45）、`no_progress_off`、`passthrough_off`、`auto_collapse_reasoning_off`。

## 唤醒

事件批量唤醒总管。常量在 `internal/steward/steward.go`，消息拼装在 `internal/steward/wake.go`：

| 项 | 常量 | 值 |
|---|---|---|
| 聚合窗口 | `aggregateWindow` | 20 秒 |
| 最小唤醒间隔 | `minWakeGap` | 2 分钟 |
| 每批事件数 | `maxBatchEvents` | 20 条 |
| 巡检检查周期 | `patrolInterval` | 10 分钟 |
| 巡检唤醒最小间隔 | `patrolWakeGap` | 2 小时 |
| 用户忙时推迟 | `userBusyDeferral` | 1 分钟 |
| 每日上限 | `daily_wake_limit` | 默认 30 次 |

安静时段不唤醒。唤醒通过 `MemoryBackend.SendStewardWake` 发送，`Kind=steward_wake`，客户端不显示正文。

## 身份提示词

`identityTemplate`（`internal/steward/prompt.go`）每轮由装饰器注入，包含两个 UI 依赖的固定写法：

- `[[静默]]`：本轮不需要处理。时间线会把这类回合折叠（`internal/ui/steward_timeline.go`）。
- `需要你决定：`：决策行的固定前缀，`internal/ui/steward_decision_bar.go` 解析它。

改这些写法时要同步改 UI 和测试。

## 账本

`internal/steward/ledger.go`。条目分三段：`in_progress`、`decision`、`pending_user`。总管通过 MCP 工具 `steward_ledger_get`、`steward_ledger_update` 读写。`renderLedger` 有总长预算，超出时从 `UpdatedAt` 最旧的条目开始截断。

## 话题

`internal/steward/topics.go`。话题是总管为一件具体工作开的独立会话和计划。

- 存储在 `app_settings` 的 `steward.topics` 键。
- 阶段：`drafting`、`planning`、`plan_failed` 等。
- 起草期用 grill-me 式追问，通过 `steward_update_brief` 更新简报。`OpenQuestions` 清零后才开工。
- 回执（`receipt.go`）后 72 小时自动结项。
- RPC：`steward.open`、`steward.topics.list`、`start`、`replan`、`confirm`、`close`、`delete`、`steward.topics.tasks.review`，通知 `steward.changed`（`internal/remote/steward_topics.go`）。

## 事件直通

`internal/steward/passthrough.go`：属于某个话题的事件直接发给话题会话，不唤醒总管主会话。判定只依赖事件字段、话题表和看板。可用 `passthrough_off` 关闭。

## 主会话换新

`internal/steward/rotation.go`：

- `ShouldRotate`：按空闲时长（`rotate_idle_minutes`）和上下文用量（`rotate_context_tokens`）判断。
- `RotationPreamble`、`SplitPreamble`：新会话的开场内容，包含账本、话题索引、进行中的子代理和近期事件。

## 动作和护栏

| 文件 | 内容 |
|---|---|
| `internal/app/steward_actions.go` | `SendMessage`、`InterruptThread`、`RunAutomation`、`MoveKanbanTask`，都带护栏 |
| `internal/steward/actions.go` | 24 小时动作上限 |
| `internal/app/steward_assign.go` | 转交任务（`steward_assign_task`） |
| `internal/app/steward_plan.go` | `plan_request`：用配置的方案模型派一个只写方案文档的子代理，项目为空时派到总管主目录 |
| `internal/app/steward_suggestions.go` | 审批建议（`approval_suggest`） |
| `internal/steward/hooks.go`、`rename.go` | 钩子和改名 |

## 任务验收

总管按 `internal/steward/prompt.go` 的执行阶段指令处理任务待验收事件卡（`[命令]`、`[审阅]`、`[你验]` 的定义见 [06-kanban-automation.md](06-kanban-automation.md#验收)）：

1. 宿主先在 worktree 里跑 `[命令]`（`internal/app/goal_review_wake.go`），结果放进事件卡。
2. 总管用 `kanban_task_diff` 看改动，读相关文件做 `[审阅]`。
3. 全部通过且没有 `[你验]` 项时，调 `kanban_review_task` 接受。
4. 第二次打回、任务含 `[你验]` 项，或改动会改变范围时，用 `notify_owner` 问用户。

## 看护

- `internal/app/goal_review_wake.go`：目标验收时唤醒总管，发日报。
- `internal/app/progress_watchdog.go`：会话长时间无进展时产生 `thread_no_progress` 事件，默认 45 分钟。

## 收件箱（Attention）

`internal/attention` 定义事件和服务。事件存 `attention_events` 表，用 `dedupe_key` 去重。

事件类型：

| Kind | 来源 |
|---|---|
| `thread_stalled`、`thread_failed`、`thread_quota`、`thread_approval`、`thread_no_progress` | 会话 |
| `kanban_failed`、`kanban_needs_approval`、`kanban_review` | 看板 |
| `automation_failed`、`automation_finished` | 自动化 |
| `bee_failed` | 编排 |

`OwnerActionKinds` 是需要用户亲自处理的类型（approval、needs_approval、review）。总管启用时收件箱只列这三类，其余交给总管。

接线在 `internal/app/attention_wiring.go`，它通过 `StreamHub.AddObserver` 和各子系统回调收集事件。

RPC：`attention.list`、`attention.resolve`、`attention.confirmSuggestion`，通知 `attention.changed`。

## UI

| 位置 | 文件 |
|---|---|
| 侧栏总管段 | `internal/ui/sidebar_steward.go`（客户端无总管能力时退回只显示「待处理」） |
| 时间线紧凑形态 | `internal/ui/steward_timeline.go` |
| 决策栏 | `internal/ui/steward_decision_bar.go` |
| 收件箱 | `internal/ui/attention_inbox.go` |
| 设置 | `internal/ui/settings_page_steward.go` |
| Web | `web/src/components/Steward*`、`AttentionSheet.tsx` |

桌面 `Client` 的可选接口：`AttentionClient`、`StewardClient`、`StewardTopicsClient`，实现在 `internal/app/client_steward.go`。

