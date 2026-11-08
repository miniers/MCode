# 06 看板与自动化

## 看板

代码在 `internal/kanban`，数据在 `tasks`、`goals`、`goal_tasks` 表。

### 任务状态

`TaskStatus`：`todo`、`in_progress`、`needs_approval`、`review`、`done`、`waiting`、`failed`、`cancelled`。`rejected` 作为旧数据别名读成 `cancelled`。合法转移由 `CanTransition` 决定。

看板显示列 `DisplayColumn`：`todo`、`in_progress`、`needs_you`、`review`、`done`。显示列由任务状态派生（`derive.go`），不是一一对应。

优先级分 5 档。

### 文件

| 文件 | 内容 |
|---|---|
| `service.go` | `CreateTask`、`MoveTask`、`StartTask`、`ReviewTask`、`TaskDiff`、`CreateGoal*`、`StartGoal`、`PauseGoal`、`CancelGoal`、`ReplanGoal`、`RetryGoalTask`、`SkipGoalTask`、`RecoverOrphanTasks` |
| `service_edit.go` | `UpdateTask`、`DeleteTask`、`CreateDraft`、`SetGoal`、`ReadGoal` |
| `planner.go` | 规划 JSON，含 `dependsOn`、`acceptanceCriteria` |
| `scheduler.go` | `GoalScheduler`，每 10 秒调度 |
| `watchdog.go` | 每 5 秒检查，自动转 `review`、`needs_approval`；无活动 600 秒发催促，1800 秒自动中断（`GetStuckNudgeTimeout`、`GetStuckInterruptTimeout`） |
| `acceptance.go` | 验收 |
| `coordinator.go` | `CoordinatorPreface` |
| `derive.go`、`concurrency.go`、`goal_prompt_store.go` | 派生状态、并发、目标提示词 |

### 任务开工

`StartTask` 启动任务会话。`RunInWorktree`（默认开启）时在 `worktrees/` 下准备工作副本，并运行项目的 worktree 初始化脚本。`AutoMerge` 控制验收通过后是否自动合并，`AutoCleanupMergedWorktrees` 控制合并后是否清理，二者默认开启。

- `ensureDependenciesDone` 校验前置依赖，未就绪的任务不能启动。已完成依赖的信息会注入启动提示词。
- 驳回时附带的反馈让任务退回 `todo`，重启时写入执行提示词。
- 打回次数只增不减，accept 不清零。
- `RecoverOrphanTasks` 在启动时恢复孤儿任务。

### 目标

`GoalStatus`：`planning`、`plan_failed`、`plan_review`、`running`、`paused`、`completed`、`cancelled`。

流程：

1. 创建目标，规划器在计划会话里生成任务 JSON。
2. 进入 `plan_review`，用户确认后 `StartGoal`。
3. `GoalScheduler` 按依赖和并发限制启动任务。
4. 规划完成后，计划会话转为目标的协调员。`CoordinatorPreface` 在每次提示词前注入目标状态、进度、任务清单和 MCP 协调规则。
5. 任务待验收时进入 `review`，由用户验收；含 `[命令]` 或 `[你验]` 的任务不能自动接受。

目标不存在、仍在规划、规划失败或已取消时，`CoordinatorPreface` 返回空字符串。

### 验收

验收标准的每一行带一个标签：

| 标签 | 含义 |
|---|---|
| `[命令]` | 宿主在任务 worktree 里执行。命令写在第一对反引号之间 |
| `[审阅]` | 需要阅读改动后判断 |
| `[你验]` | 必须问用户 |

`planner.go` 要求规划时原样保留标签。`acceptance.go` 只解析 `[命令]`，限制：单条命令 5 分钟，合计 15 分钟，输出保留末尾 4KB。执行结果与决策卡见 [05-steward-attention.md](05-steward-attention.md)。

### 工具和 UI

- MCP 工具：`kanban` 入口和 `kanban_*` 系列（见 [07-mcp-context-memory.md](07-mcp-context-memory.md)）。
- `internal/mcp/tools_kanban_review_guard_test.go` 防止审阅类工具被改坏。
- RPC：`kanban.*`、`goals.*`。
- UI：`internal/ui/kanban*.go`、`internal/ui/tasks_window.go`；Web 的 `KanbanBoard.tsx`、`TaskDetailModal.tsx`。

## 自动化

代码在 `internal/automation`，数据在 `automations`、`automation_runs` 表。

### 调度类型

`interval`、`once`、`daily`、`weekdays`、`weekly`、`cron`。

### 运行模式

| 模式 | 含义 |
|---|---|
| `standalone` | 每次运行新建会话。默认模式，空值和未知值都按它处理 |
| `dedicated` | 复用自动化专属的同一个会话，首次运行时创建并写入 `DedicatedThreadID` |
| `heartbeat` | 每轮投递回创建自动化时的来源会话（`OriginThreadID`） |

`Provider`、`Model`、`Effort`、`RuntimeMode` 可以覆盖默认值。`heartbeat` 投递回已有会话，所以这些覆盖对它不生效。

### 结算

`Service` 周期检查运行中的任务：

- 等待输入表示会话没有结束，不能计成功，也不能清零失败计数。
- `dedicated`、`heartbeat` 把提示词投递到已有线程，投递到开跑之间线程是空闲的。结算要宽限一个调度间隔，否则会把未开跑的运行误判为成功。

`Run` 状态：`running`、`succeeded`、`failed`、`stopped`。

### 停止条件

`stopWhen` 配合 `Evaluator`：先用启发式（如 `NoCodeChangesMarker`），再用模型评估。置信度阈值 `ConfidenceThreshold` 为 0.8。连续失败上限 `MaxConsecutiveFailures` 为 3。

### 防休眠

配置 `automation.prevent_sleep`，默认开启。任务运行中，或 5 分钟内有任务到期时，`SleepGuard`（`internal/remote/sleepguard*.go`）阻止系统休眠，每 20 秒检查一次。

### 接线

- `OnRunSettled` 把结果接到 attention（`automation_failed`、`automation_finished`）。
- `cmd/mcode` 的 `SetAutomationNotifier` 发桌面通知。
- RPC：`automations.create`、`update`、`delete`、`list`、`run`、`runs`、`setEnabled`、`reset`、`markRead`、`archiveRun`，通知 `automations.changed`。接线在 `internal/app/remote_automation.go`。
- MCP 工具：`automation_create`、`update`、`list`、`cancel`、`run_now`、`report_result`。
- UI：`internal/ui/automation*.go`；Web 的 `web/src/components/Automation*`。
