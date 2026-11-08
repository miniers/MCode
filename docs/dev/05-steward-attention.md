# 05 总管与收件箱

## 总管是前台

总管是主会话消息的前台：每条用户消息触发一次短模型调用，由 Go 代码现拼上下文，模型只读取状态、创建或调整任务、记录决策答复。它不执行代码、不改文件、不合并、不发布、不推送远端、不改设置、不花钱；真正干活的是看板任务会话。它不在进程里保留对话状态，也不维护自己的工作记录。

代码在 `internal/frontdesk`，消息入口是 `Desk.HandleMessage`。

## 一条消息一次模型调用

每条消息最多 4 轮模型调用。`Desk.Enqueue` 用单 worker 串行处理，保证同一 feed 的消息按到达顺序执行。

每轮把工具调用分为三类：

- 读工具：立即执行，结果作为 tool 消息喂回，进入下一轮。
- 写工具：只在干净轮执行。
- 未知工具、参数错误：本轮变脏，不执行任何写操作，把「尚未执行，需要的话再发一次」作为 tool 结果。

干净轮指本轮没有读工具、未知工具和参数错误，只有写工具或没有工具调用。干净轮先落库并推送本轮文字，再按顺序执行写工具；写工具结果只写日志，不改变已发出的回复。非干净轮的模型文字全部丢弃。

用满 4 轮仍没有干净轮，或模型调用出错，不重试，走静默兜底：单行且不超过 40 字、最近 10 分钟有任务时追加到该任务，否则用第一行前 80 字新建任务。

`steward.cli_fallback` 默认关闭。开启且没有可用 API 来源或模型鉴权、网络失败时，用最便宜的 CLI 模型做一次调用；输出只接受 `{"reply":"...","calls":[{"tool":"create_task","args":{}}]}`，只允许 `create_task`、`amend_task`、`remember`、`answer_decision` 四个写工具，没有读轮次。CLI 不可用、开关关闭或 JSON 解析失败时，同样静默变任务。

## 上下文快照

`snapshot` 每次现拼 system 文本，包含以下段落：

| 段落 | 条数上限 | 单条上限 |
|---|---|---|
| 系统提示与当前时间 | — | — |
| 进行中任务 | 40 | 标题 160 字 |
| 最近完成任务 | 8 | 标题 160 字 |
| 待决策卡 | 15 | 问题 200 字 |
| 相关记忆 | 12 | 内容 200 字 |
| 最近对话 | 12 条消息 | 每条 300 字 |
| 回复任务 | 1 | 200 字 |
| 用户原话 | 1 | 4000 字 |

段落总长分别截断为：进行中任务 8000 字、最近完成 2000 字、待决策 3000 字、相关记忆 3000 字、最近对话 4000 字。

## 工具面

读工具 8 个：`get_status`、`list_tasks`、`get_task`、`list_decisions`、`read_conversation`、`read_memory`、`search`、`list_projects`。

写工具 8 个：`create_task`、`amend_task`、`cancel_task`、`set_priority`、`reassign_task`、`remember`、`edit_memory`、`answer_decision`。

`reassign_task` 只改 `provider` 与 `model`，且只对 `todo`、`waiting` 任务生效。`remember` 以 `UserRequested=true` 直接写长期记忆；`edit_memory` 按 `id` 改写，不新建重复记忆。

前台没有合并、发布、推送远端、修改设置、支付这类能力。这些动作只能由任务执行者在硬锁下请求决策卡。

## 任务即对话

一个任务就是一段对话：任务有独立执行会话和 worktree，用户消息、执行过程、结果都留在这段会话里。

`tasks.meta` 存执行期字段：`origin`、`parent`、`children`、`result_status`、`summary`、`wait_kind`、`wake_at`、`wait_check`、`wait_check_base`、`wakes`、`decision_id`、`gate_override`、`follows`、`provider`、`model`、`no_change`。

看板状态新增 `waiting`、`failed`、`cancelled`；`rejected` 作为旧数据别名读成 `cancelled`。`waiting` 表示等机器检查或等人，`failed` 表示不再自动重试，`cancelled` 表示用户或父任务取消。

已完成、失败、已取消的任务收到用户新消息时回到 `todo`，保留同一任务 ID 和同一执行会话。

`AmendTask` 按状态分流：`todo`、`in_progress` 追加消息；`waiting` 清空等待字段并回到 `todo`；`done`、`failed`、`cancelled` 重开同一任务；`needs_approval` 只追加消息，等用户答卡。

## 五态结果

执行者结束时用 `kanban_submit_result` 交一个结果：

| 状态 | 含义 |
|---|---|
| `done` | 工作完成，按评审策略进入 `review` 或 `done` |
| `failed` | 失败且不自动重试，任务进入 `failed` |
| `waiting` | 等机器条件或等人，任务进入 `waiting` |
| `delegated` | 拆成子任务，父任务等待全部子任务结束 |
| `needs_decision` | 需要用户拍板，建决策卡，任务进入 `needs_approval` |

`kanban_submit_result` 继续接受旧的 `outcome`：`done` 映射为 `done`，`blocked` 映射为 `needs_decision`，问题取摘要，选项默认「继续」「停下」，保证旧执行者能收尾。

`waiting` 的 `wait_kind` 为 `human` 或 `machine`。`human` 不设定时器，只等事件或用户消息。`machine` 必须有 `wait_check`，首次等待 5 至 120 分钟；到期后由 `CheckWaits` 在项目目录执行，单次超时 60 秒，退避为 5、10、20、40 分钟，40 分钟封顶，最多 6 次。检查命令必须是只读命令，否则直接转决策卡。超过次数转 `needs_approval` 并生成「等待超时」决策卡。

## 决策卡

决策卡编号从 `D-0001` 起，不足四位补零，超过四位自然增长；编号在事务里取当前最大值加一。

所有回答方式最终都归成主会话里一条用户消息「D-编号 答案」：

- 点卡片按钮：发送 `D-0001 A`。
- 键盘 1 至 9：映射到选项下标，发送同样格式的消息。
- 通知按钮：service worker 把按钮转成同样格式的消息。
- 手打：`HandleMessage` 先匹配 `D-\d{4,}`，命中后直接答卡，不调用模型。

答卡后卡片只读，显示「已选 A · 选项文本」或「已选 A」；「忽略」发送「D-编号 忽略」，「其他指示」把编号预填进输入框。最后一张未答卡才接受数字快捷键。

5 分钟内重复回答同一张卡视为改答，卡片保留改答记录，以最后一条回答为准。

`steward.decisions.list` 按 `status` 和 `limit` 列出决策卡，默认 50 条，最多 200 条；状态为空表示全部。

## 硬锁与一次性解锁

硬锁清单在 `internal/agent/gate/lock.go`：

- 合并 PR：`gh pr merge`。
- 强制推送：`git push --force`、`--force-with-lease`、`-f`。
- 推送标签：`git push --tags`、`--follow-tags`。
- 推送 `main` 或 `master`。
- 打标签后推送。
- 发布软件包：`npm publish`、`bun publish`、`cargo publish`。
- 创建发布：`gh release create`。
- 修改仓库密钥：`gh secret`。
- 删除仓库：`gh repo delete`。
- 修改仓库设置：`gh repo edit`。
- 删除根目录或用户目录：`rm -rf /`、`rm -rf ~`。

三条拦截路径：

1. 内置引擎与自有工具：`gate.Check` 在 `HardLocked` 且没有 override 时返回 `Deny`，理由包含「需要用户答决策卡」。
2. Claude CLI：启动参数把 `Agent,Task` 与硬锁模式合并进 `--disallowed-tools`，由 CLI 在命令层拒绝。
3. Codex 与 ACP：`hardLockApprover` 在审批层按命令文本拒绝；任务处于硬锁且没有 override 时，Codex 的全权限降为 `workspace-write` 加 `on-request`。

`gate_override` 的写入与清空：用户答卡且卡属于该任务时，仅在任务 `needs_approval` 状态下写入 `meta.gate_override=D-xxxx`，同时清空 `decision_id`；任务启动前读取该值，传给 gate、CLI 参数与审批层。任务进入 `done`、`failed`、`needs_decision` 或 `delegated` 时清空；`waiting` 延续同一任务时保留，等待结束后继续有效，直到下一次结果落库。

## 结果分流

- `done`，用户亲自交代、不是子任务、不是无变化例行任务：主会话留一行结果并推送一次。
- `done`，例行任务、子任务或补充步骤：主会话只留一行，不推送。
- `done`，例行任务且摘要表示没有变化：只写日志，不产生主会话消息。
- `failed`，用户亲自交代且不是子任务：推一句白话；其余只写日志。
- `waiting`：不推送，任务页显示等待原因与下次检查时间。
- `delegated`：不推送，父任务汇总完成后按 `done` 规则分流。
- `needs_decision`：主会话出现决策卡并推送一次。

## 后台事件

注意力收件箱原样保留：`internal/attention` 负责事件存储、`dedupe_key` 去重和每日上限，接线在 `internal/app/attention_wiring.go`。

`OwnerActionKinds` 仍是会话等审批、看板待审批、看板待验收、目标规划失败四类，由用户亲手处理。其他事件由任务结果和收件箱直接处理，不触发模型调用。

## 关键文件

- `internal/frontdesk`：前台包，含快照、循环、工具、模型来源、CLI 降级与静默兜底。
- `internal/kanban/result.go`：五态结果类型、JSON 解析与字段校验。
- `internal/kanban/waits.go`：等待检查、退避与父任务汇总。
- `internal/kanban/decisions.go`：答卡后关闭卡片，并按任务状态重开、清等待或写 `gate_override`。
- `internal/kanban/taskmeta.go`：`tasks.meta` 的解析、编码与带 revision 的补丁。
- `internal/store/steward_decisions.go`：决策卡表、编号生成、列表、答卡与旧卡作废。
- `internal/agent/gate/lock.go`：硬锁清单与 `CheckHardLock`。
- `internal/remote/hardlock_approver.go`：在审批层拒绝命中硬锁的命令，override 取自任务 meta。
- `internal/remote/steward_decisions.go`：远程方法 `steward.decisions.list`。
- `internal/ui/steward_decision_card.go`：决策卡渲染、只读态、数字快捷键与「其他指示」预填。
- `internal/ui/steward_reply.go`：前台回复渲染成单行灰字。
- `internal/remote/steward_feed.go`：feed 线程的远程读写（接线中）。
- `internal/app/frontdesk_wiring.go`：构造 `Desk`、读取模型来源并接到 `MemoryBackend`（接线中）。
