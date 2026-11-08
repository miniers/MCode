# 02 会话运行内核

会话的全部运行逻辑集中在 `internal/remote/memory_backend.go` 的 `MemoryBackend`。名字带 memory 是因为运行态放在内存，落盘通过 `AttachStore` 接到 `internal/store`。

## 核心对象

- `MemoryBackend`：持有所有会话、已注册的 harness、各种 `SetXxxFunc` 注入的回调。
- `memoryThread`：单条会话的运行态。关键字段：
  - `summary`：会话摘要（对外的 `wire` 结构）。
  - `followUps`：排队消息。
  - `priorityFollowUps`：以 priority 入队的条目 ID（编排汇报和提问转交）。
  - `pending`：等待回复的审批和提问。
  - `alwaysAllow`：本会话的始终允许规则。
  - `runTurnID`、`phase`（`idle`、`starting`、`running`）、`sessionEpoch`。
  - `touchedPaths`、`lastFileChangeAt`：本回合读写的路径。
  - `messagesEvicted`：冷归档会话的消息已从内存淘汰，用时再载入。
- `StreamHub`：按会话聚合发布事件，每条流带 `runID` 和 `sequence`。单帧上限 512KB，超限改发 resync。`AddObserver` 允许 attention 这类组件订阅。

## 发送链路

```
SendMessage / SendSwarmReport / SendHostEvent
  → sendMessage
  → startTurnLocked（空闲时开回合，运行中转排队或 steer）
  → launchTurnRun
  → runHarness（主循环，约 1100 行）
  → updateTurnFinish
```

几种入口的差别：

| 入口 | 行为 |
|---|---|
| `SendMessage` | 用户消息 |
| `SendSwarmReport` | 子代理汇报。会话忙时按 priority 排队，之后开新一轮 |
| `SendHostEvent` | 行为同 `SendSwarmReport`，落库的消息 `Kind=host_event`，客户端显示成事件通知 |

`SendMessage` 命中前台 feed 时交给 `StewardDesk.Enqueue`，由 `internal/frontdesk` 处理，不启动 Provider 回合。

## `runHarness` 要点

- 原生 `/compact` 不加任何装饰，直接送达 Provider。
- 人设前缀只进 `Turn.Prompt`，不进用户看到的消息。
- 首轮 guidance：项目 Context 加技能目录。子代理使用精简目录。
- `SetFirstTurnExtra`：首轮追加内容（普通会话的「可以交给总管」提示）。
- `SessionRotator`：会话换新的钩子。

修改这个函数前，先确认改动不会影响 `/compact` 的原样送达和 `Transient` 回合。

## 运行中发送

会话正在运行时再发消息有两种走向：

- 打开「插入当前回合」且 Provider 实现了 `harness.Steerer`：直接插入当前回合。
- 否则进入 `followUps` 排队。回合结束后 `bundleFollowUpText`（`internal/remote/runtime_prompts.go`）把排队消息合并成下一轮。

队列相关 RPC：`threads.queue.list`、`update`、`edit`、`remove`、`sendNow`、`move`、`interruptAndSend`。桌面 UI 在 `internal/ui/composer_queue.go`。

## 事件处理

Provider 通过 `emit` 发 `harness.Event`，`runHarness` 处理后写消息并发布：

- `text`：清洗破折号（`stream_typography.go`），跳过前导空增量。
- `text_retract`：裁掉已发出的残文。
- `context_compacted`：落独立系统消息。
- `tool_output`：合并节流。
- 工具事件：记录读写路径到 `touchedPaths`。
- `usage`：写 `turn_usage`。
- `error`：按 `ErrKind` 分类处理。

## 审批和提问

`internal/remote/approvals.go`：

- `threadApprover` 实现 `harness.Approver`，把 Provider 的请求转成 `pendingRequest`，等客户端回复。
- `RequestKind`：`command`、`fileChange`、`tool`、`plan`、`question`、`commit`、`permissions`。
- 支持送达确认（`DeliveryAcker`、`DeliveryConfirmer`），用于判断请求是否真的到达客户端。
- 始终允许规则存到 `always_allow.json`（`always_allow_store.go`），远程可通过 `requests.alwaysAllowed.list`、`revoke` 管理。

权限模式：`readonly`、`supervised`、`autoAcceptEdits`（别名 `agent`）、`auto`、`fullAccess`（别名 `full_access`）。

## 自愈

回合失败时按错误类型分别处理：

| 判定函数 | 情况 | 处理 |
|---|---|---|
| `isLostSessionError` | Provider 侧会话失效 | 用历史重建上下文后重跑 |
| `isProviderCrashError` | Provider 进程崩溃 | 恢复重跑 |
| `isTransientNetworkError` | 网络瞬断 | 最多重试 3 次 |

`store.finalizeOrphanTurns` 在启动时收尾上次未结束的回合。

## 检查点和回退

- `internal/git/checkpoint.go` 在回合前后打快照，引用为 `refs/mcode/checkpoints/<thread>/<turn>/{start,end}`，每会话保留 50 个。
- 回合记录的 `BaseCheckpoint`、`FinishCheckpoint` 落到 `turns` 表。
- `PreviewTurnRevert`、`ExecuteTurnRevert` 回退文件。`base` 取目标回合的 `BaseCheckpoint`，缺失时用 start 引用。`finish` 取最后一个非空的 `FinishCheckpoint`，再回退到最后一轮的 end 引用，都没有时对当前工作区打临时快照（`revert-now`）。
- `RecallTurn` 收回正在运行的一轮：打断并等待空闲，从内存和数据库删除该轮消息，把 `turns` 状态改为 `taken_back`，返回原 Prompt。
- `KillRunningCommand` 只终止正在跑的命令（`SIGTERM`，宽限后 `SIGKILL`），用 `ProcessRooter` 和 `internal/procs` 定位子进程树。

## 提示词注入

宿主给 Provider 的固定提示词统一由 `internal/harness/policy` 渲染：

- `policy.Render` 按固定顺序输出段落：`SwarmLead`、`SwarmTiers`、`SwarmLimits`、`SwarmRoster`、`Kanban`、`ProjectContext`、`Automation`。
- 输出带 `<mcode_harness_policy version=.../>` 标记。文案或顺序变化时要递增 `policy.Version`。

`internal/remote/runtime_prompts.go` 的预算：

| 函数 | 用途 | 预算 |
|---|---|---|
| `buildMentionContext` | `@` 引用其他会话 | 12000 |
| `buildProviderHandoffPrompt` | 切换 Provider 时重建上下文 | 32000 |
| `BuildLimitHandoffText` | 额度耗尽后转交 | |
| `bundleFollowUpText` | 合并排队消息 | |

`internal/kanban/coordinator.go` 的 `CoordinatorPreface` 和 `internal/app/swarm_wiring.go` 的装饰器也会往提示词前面加内容。

## 会话分割

`RequestSplit` 在时间线末尾追加一条 `split_session` 分割线。从下一条消息起，Provider 只看到分割线之后的历史。分割线本身是持久标记，重启后状态一致；已处于待分割状态时幂等返回。

`internal/remote/session_rotation.go` 定义换新钩子。`SessionRotationInfo.Manual` 为真表示用户主动分割，钩子应无条件同意，并给出分割版前言。

## 持久化和归档

- `internal/remote/persist.go` 把运行态写入 `store`。
- 自动休眠按 `archive` 配置（默认 7 天）。冷归档会话的消息从内存淘汰（`messagesEvicted`）。
- 会话行、排队消息或消息解码失败时，`persist.go` 调 `QuarantineThread` 写入 `quarantined_threads` 表并记录原因，避免一条坏数据挡住启动。

## 会话提醒

`internal/remote/session_alerts.go` 的 `SessionAlertDetector` 产生 `approval`、`done`、`failed`、`stalled`、`quota` 五类提醒。`stalled` 每 30 秒查一次，24 小时内最多 3 次。

检测器有多个独立实例：`internal/remote/webpush.go`（Web Push）、`internal/app/attention_wiring.go`（收件箱）。

## 斜杠命令

`internal/remote/thread_ops.go` 定义内置命令：`mode`、`plan`、`compact`、`clear`、`model`、`effort`、`fast`、`pair`、`export`。同一文件按 Provider 给出各自的原生命令（例如 claude 的 `review`、`pr_comments`，agy 的 `goal`、`grill-me`），并合并技能。同名时内置命令优先。

## 其他文件

| 文件 | 内容 |
|---|---|
| `thread_ops*.go` | 会话操作：移动、分叉、继续、批量等 |
| `thread_undo.go` | 撤销操作 |
| `worktree_setup.go` | 会话 worktree 的创建和脚本 |
| `files.go`、`media.go` | 文件读取和媒体 |
| `action_ledger.go` | 幂等账本 |
| `models.go`、`cli_catalog.go` | 模型列表和 CLI 目录 |
| `quota.go`、`quota_pi.go` | 额度 |
| `sleepguard*.go` | 防休眠 |

## 改动提示

- 所有对 `memoryThread` 的修改都要持有后端锁。回调到 harness、驱动、swarm 时不要持锁，否则容易和反向取锁形成死锁。`swarm` 注释里多处说明了这一点。
- 新增消息 `Kind` 时，检查桌面时间线、Web 时间线和导出是否要特殊处理。
- 排队和编排汇报的时序是常见的 bug 来源，改动前读 `internal/swarm/manager.go` 的 `checkReports` 注释。
