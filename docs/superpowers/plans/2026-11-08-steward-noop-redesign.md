# 总管全面转向 noop 模式（以内置引擎为底座）

状态：已拍板，待实施
适用仓库：`/Users/liukai/code/github/mcode`
参考实现：`/Users/liukai/code/github/noop`（只读）
前置计划：`docs/superpowers/plans/2026-11-08-unified-api-engine.md`

本文把「总管」（代码里的 `steward`）从常驻会话改成 noop 那样的前台：每条用户消息做一次便宜、短、无状态的模型调用，由代码现拼上下文，模型只碰少量读工具与安全写工具；真正干活的是任务会话。旧的总管运行时、账本、话题、直通、唤醒、巡检、动作工具、置顶决策条全部退役。

---

## 一、目标与非目标

新总管是一个「前台」而不是一个「同事会话」：它在主会话里回答问题、把工作变成任务、把用户的话接回已有任务、把需要拍板的事做成带编号的决策卡；它自己不写代码、不合并、不发布、不推远端、不改设置、不花钱。每条用户消息都新开一次便宜快的小模型调用，不保留长会话，上下文由 Go 代码每次现拼；模型通过原生工具调用读写任务、决策和记忆，最多 4 轮，读工具立即执行并把结果喂回，写工具只在干净轮执行，文字回复先落库再执行写操作。执行者仍可使用任意 provider，结束时必须交五态结果（`done`、`failed`、`waiting`、`delegated`、`needs_decision`），结果直接进入主会话与任务页面，不经总管转述。

非目标：不做新的常驻会话；不做总管自己的账本；不做话题会话生命周期；不做事件攒批唤醒；不做巡检；不做 `notify_owner` 式转述；不做审批建议；不做方案派发；不做合并/发布/推送/设置/支付等前台写能力；不引入 cgo；不新建第二套任务执行器。

现状依据：总管当前是一个常驻会话，`internal/steward/steward.go:375` `func (r *Runtime) Ensure(ctx context.Context) (string, error) {` 负责创建并复用会话，事件按批唤醒并在 `internal/steward/steward.go:554` `func (r *Runtime) loop(ctx context.Context, events, resolved <-chan attention.Event) {` 里聚合投递；主会话会按空闲与上下文换新，`internal/steward/rotation.go:64` `func (r *Runtime) ShouldRotate(threadID string, idle time.Duration, contextTokens int64) bool {`。这些都要删除。

---

## 二、新旧对照表

| 旧部件 | 旧实现锚点 | 处置 | 理由 |
|---|---|---|---|
| 长驻总管主会话 | `internal/steward/steward.go:375` `func (r *Runtime) Ensure(ctx context.Context) (string, error) {` | 删除 | 前台不再保留长会话；每条消息一次调用，上下文由代码拼。 |
| 主会话换新 | `internal/steward/rotation.go:64` `func (r *Runtime) ShouldRotate(threadID string, idle time.Duration, contextTokens int64) bool {` | 删除 | 没有长上下文，不需要 rotation。 |
| 账本 | `internal/steward/ledger.go:21` `const settingLedger = "steward.ledger"` | 删除 | 进行中、待决策、待用户事项分别由任务、决策卡、注意力收件箱承载；长期知识走记忆。 |
| 话题 | `internal/steward/topics.go:23` `const settingTopics = "steward.topics"` | 删除 | 一个任务就是一段对话；需要多任务协作时用 `delegated` 子任务，不另造话题层。 |
| 事件直通 | `internal/steward/passthrough.go:207` `func (r *Runtime) routeEvent(ctx context.Context, ev attention.Event) bool {` | 删除 | 没有总管主会话可绕过；事件由任务结果路由和注意力收件箱直接处理。 |
| 攒批唤醒 | `internal/steward/steward.go:47` `const (` 中的 `aggregateWindow`、`minWakeGap` | 删除 | 前台只在用户说话时运行；后台事件不再唤醒模型。 |
| 巡检 | `internal/steward/steward.go:929` `func (r *Runtime) patrol(ctx context.Context) {` | 删除 | 任务等待由 wait 调度器处理，卡住由看板 watchdog 处理。 |
| `ActionGuard` 与四个动作工具 | `internal/steward/actions.go:41` `type ActionGuard struct {`；`internal/mcp/tools_steward.go:870` 的动作工具注册 | 删除 | 前台不能催、不能中断、不能重跑、不能移卡；这些动作要么由用户做，要么由任务执行者自己做。 |
| `notify_owner` | `internal/mcp/tools_steward.go:826` 的 `notify_owner` 注册 | 删除 | 结果直接进主会话；只有需要拍板或用户亲自交代的失败才推送。 |
| `approval_suggest` | `internal/mcp/tools_steward.go:847` 的 `approval_suggest` 注册 | 删除 | 建议不执行会混淆责任；后续 judge 阶段再做自动答卡，且只答有先例的卡。 |
| `plan_request` | `internal/mcp/tools_steward.go:734` 的 `plan_request` 注册 | 删除 | 规划是任务的一种执行方式，不是前台特权；需要方案就创建一个方案任务。 |
| `steward_assign_task` | `internal/mcp/tools_steward.go:754` 的 `steward_assign_task` 注册 | 删除 | 由 `create_task` 与执行者的 `delegated` 结果承担。 |
| 决策置顶条 | `internal/ui/steward_decision_bar.go:14` `const stewardDecisionPrefix = "需要你决定："` | 删除 | 决策改成带编号的卡片消息，不需要从自由文本里猜「需要你决定：」。 |
| 注意力收件箱 | `internal/attention/attention.go:48` `var OwnerActionKinds = []Kind{KindThreadApproval, KindKanbanNeedsApproval, KindKanbanReview, KindKanbanPlanFailed}` | 原样保留 | 需要用户亲手处理的审批、验收、规划失败仍由收件箱兜底；总管不再代为分诊。 |
| 总管专属 MCP 工具集 | `internal/mcp/tools_steward.go:657` `func (r *Registry) registerStewardTools(s *Server) {` | 改造成前台工具面 | 只保留读工具与少量安全写工具；与前台共用一份定义。 |
| `[[静默]]` 回合折叠 | `internal/ui/timeline_rows.go:50` `const stewardSilentMarker = "[[静默]]"` | 删除 | 前台没有文字就不落消息，不需要静默标记。 |
| `steward_wake` 消息 Kind | `internal/ui/timeline_rows.go:55` `const stewardWakeKind = "steward_wake"` | 删除 | 没有唤醒消息。 |
| 总管时间线紧凑形态 | `internal/ui/steward_timeline.go:1` 整体 | 删除 | 前台消息只有单行灰字，不需要唤醒原因、静默回合折叠、思考摘要。 |
| 话题 UI 与侧栏话题卡 | `internal/ui/steward_topic_card.go:1`、`internal/ui/steward_topics.go:1` | 删除 | 任务页面取代话题页面。 |
| 侧栏总管段 | `internal/ui/sidebar_steward.go:17` `func renderSidebarStewardSection(c *ui.Context, s *chatState) {` | 保留入口，重做内容 | 保留「总管」常驻入口；不再显示唤醒次数、未启用提示副文案。 |
| 设置页总管模型/唤醒/直通/日报 | `internal/ui/settings_page_steward.go:67` `func stewardSections(st *SettingsState) []settingsSection {` | 重做 | 只留启用、昵称、头像、前台模型、安静时段（只控推送）。 |
| 审批建议计分 | `internal/store/steward_scores.go:35` `type StewardApprovalScore struct {` | 原样保留但停止写入 | 历史数据留作 judge 阶段的影子评分基线。 |
| 看板回执 | `internal/kanban/receipt.go:16` `	ReceiptOutcomeDone    = "done"`、`internal/kanban/receipt.go:17` `	ReceiptOutcomeBlocked = "blocked"` | 扩展 | 从两态扩成五态，继续用现有 watchdog/scheduler 收尾。 |
| 注意力事件 | `internal/attention/attention.go:16` `type Kind string` | 原样保留 | 事件源和去重写入不需要重写，只改消费方式。 |
| 内置引擎 | `internal/agent/llm/types.go:77` `type Client interface {` | 原样复用 | 前台只用 `llm.Client`，不用 `internal/providers/mcode` 的完整 Harness。 |
| 危险操作门禁 | `internal/agent/gate/gate.go:131` `func Check(in Input) Verdict {` | 扩展 | 继续做工具级门禁；新增硬锁模式与一次性解锁。 |
| 记忆 | `internal/mcp/tools_memory.go:114` `Name: "memory_search",` | 原样复用 | 前台记忆读写走现有记忆提议与检索，不新建记忆库。 |
| Swarm / Bee | `internal/swarm/swarm.go:1` | 原样保留 | 执行者的子代理能力与总管无关。 |

---

## 三、目标架构

### 3.1 调用链路

```
桌面 UI / 网页 / 手机
        │
        │ threads.sendMessage（只对主会话 feed 线程）
        ▼
internal/remote.MemoryBackend.SendMessage
        │  internal/remote/memory_backend.go:2876
        │  发现 threadID == steward.feed_thread_id
        ▼
internal/frontdesk.Desk.HandleMessage
        │  1. 裸决策答复？ D-0001 A  → AnswerDecision，直接结束
        │  2. 建上下文快照（任务/决策/记忆/最近对话/用户原话）
        │  3. 取一个便宜快的 llm.Client（apisource.Source）
        ▼
frontdesk.loop（最多 4 轮，原生 tool_calls）
        │
        ├─ 读工具：立即执行，把 ToolResult 作为 tool 消息喂回，再来一轮
        │
        ├─ 写工具 + 未知工具 + 参数错误：本轮回退写调用，
        │     告诉模型「尚未执行，需要的话再发一次」，然后继续
        │
        └─ 干净轮：先 AppendAssistantKind(KindStewardReply) 落库并推送，
              再执行写工具；执行完结束
        │
        ▼
主会话时间线：前台回复 = 单行灰字
任务页面：执行者自己的会话
```

后台结果链路：

```
执行者会话结束
        │  kanban_submit_result（五态）
        ▼
internal/kanban.Service.SubmitTaskResult
        │  internal/kanban/receipt.go:103
        ▼
watchdog / scheduler 消费结果
        │
        ├─ done → review / done；主会话一行结果，owner 任务推送
        ├─ failed → failed；只有 owner 直接交代的任务推送一句白话
        ├─ waiting → waiting + wait_at/wait_check；wait 调度器按 5/10/20/40 分钟检查
        ├─ delegated → 建子任务，父任务 waiting；子任务全部结束后父任务带结果重开
        └─ needs_decision → 建决策卡 D-xxxx；任务 needs_approval
```

### 3.2 前台循环的 Go 类型与函数签名草案

新包：`internal/frontdesk`。不导入 `internal/mcp`，这样 `internal/mcp` 可以反过来复用它的工具定义而不形成循环。旧 `internal/steward` 在阶段 4 删除。

```go
package frontdesk

type Desk struct {
    Client    llm.Client
    Model     string
    Tools     *Toolbox
    Feed      FeedStore
    Tasks     TaskService
    Decisions DecisionService
    Memory    MemoryService
    Projects  ProjectService
    Now       func() time.Time
    MaxRounds int // 默认 4
}

type Inbound struct {
    ThreadID string
    Text     string
    ReplyTo  string // 任务页回复的任务 ID，可空
    SentAt   time.Time
    Images   []llm.Part
}

type Snapshot struct {
    Text            string
    OpenTaskIDs     []string
    OpenDecisionIDs []string
    Answered        string // 5 分钟内刚答过的决策 ID，可空
}

func (d *Desk) HandleMessage(ctx context.Context, in Inbound) error
func (d *Desk) Enqueue(in Inbound) // 单 worker 串行，保证同一 feed 的消息按到达顺序处理
func (d *Desk) loop(ctx context.Context, in Inbound, snap Snapshot) error

func (d *Desk) stream(ctx context.Context, req llm.Request) (*llm.Response, string, error)
func (d *Desk) executeReads(ctx context.Context, calls []llm.ToolCall) []llm.Message
func (d *Desk) executeWrites(ctx context.Context, calls []llm.ToolCall) []llm.Message

type Tool struct {
    Name        string
    Description string
    Parameters  json.RawMessage
    Write       bool
    Run         func(ctx context.Context, args json.RawMessage) (any, error)
}

type Toolbox struct{ tools map[string]Tool }
func (t *Toolbox) Specs() []llm.ToolSpec
func (t *Toolbox) Get(name string) (Tool, bool)

type LoopError struct {
    Round int
    Err   error
}
```

`loop` 的精确语义：

1. 第 0 轮前把 `Snapshot.Text` 作为 system 消息、用户原话作为 user 消息。
2. 每轮调用 `Client.Stream`，完整收集 `delta.Text`；`delta.Reasoning` 丢弃。
3. 从 `response.Message.ToolCalls` 分类：`Read`、`Write`、`Unknown`、`Invalid`（JSON 参数无法解析或必填缺失）。
4. 只要出现 `Read`、`Unknown`、`Invalid` 中任意一种：丢弃本轮文本，执行读工具；把未知工具和参数错误作为 tool 结果；把所有写工具作为「尚未执行，需要的话再发一次」的 tool 结果；本轮不执行写操作；继续下一轮。
5. 没有上述三类、只有写工具，或没有任何工具调用：这是干净轮。先把本轮 `/assistant` 文本用 `Feed.AppendAssistant(threadID, KindStewardReply, text)` 落库并推送；文本为空则不落消息。然后按顺序执行写工具，每个写工具的结果只记日志，不改变已经发出的回复。
6. 达到 `MaxRounds` 仍没有干净轮：记一条 `frontdesk` 日志，吞掉本轮（不落回复、不执行写调用），并按第 7 条走模型失败兜底。
7. `Client.Stream` 返回错误（无来源、鉴权失败、超时、上下文溢出且无法恢复）：不重试超过一次；按第 3.9 节尝试 CLI 降级，否则把消息静默变成任务或追加到最近任务。

每轮文本只在判定为干净轮后作为一行灰字落库并推送；其他轮次的文本全部丢弃，保证「先回复后写」只发生一次。

### 3.3 任务模型：扩展现有看板任务，不新建任务模型

**推荐：扩展现有 `tasks` 表。** 现有看板任务已经具备「一个任务一段会话」的骨架：`internal/store/kanban.go:13` `type Task struct {` 有 `ThreadID`、`ProjectID`、`Status`、`Priority`、`Revision`；`internal/kanban/service.go:471` `func (s *Service) StartTask(ctx context.Context, taskID string, expectedRevision int) (store.Task, error) {` 会创建执行会话与 worktree；`internal/kanban/scheduler.go:104` `func (g *GoalScheduler) Tick(ctx context.Context) {` 与 `internal/kanban/watchdog.go:118` `func (w *Watchdog) Check(ctx context.Context) error {` 已经在驱动任务生命周期。新建一套精简任务模型会复制会话、worktree、回执、看护、调度五套逻辑，违背极简原则。

**不新建 `steward_tasks` 表**，而是在 `tasks` 加一列 `meta TEXT NOT NULL DEFAULT '{}'`，把执行期字段放在 JSON 里。理由：字段会随阶段演进，JSON 迁移成本最低；看板查询只按 `status` 过滤，不需要为每个字段建索引；历史任务读回时 `{}` 即默认值。

`meta` 的 Go 视图：

```go
type TaskMeta struct {
    Origin       string   `json:"origin"`        // owner | routine | followup | delegated
    ParentID     string   `json:"parent,omitempty"`
    Children     []string `json:"children,omitempty"`
    ResultStatus string   `json:"result_status,omitempty"` // done|failed|waiting|delegated|needs_decision
    WaitKind     string   `json:"wait_kind,omitempty"`     // human|machine
    WakeAt       string   `json:"wake_at,omitempty"`       // RFC3339
    WaitCheck    string   `json:"wait_check,omitempty"`
    WaitBase     string   `json:"wait_check_base,omitempty"`
    Wakes        int      `json:"wakes,omitempty"`
    DecisionID   string   `json:"decision_id,omitempty"`
    GateOverride string   `json:"gate_override,omitempty"` // D-xxxx，只对本次运行有效
    Follows      string   `json:"follows,omitempty"`
    Provider     string   `json:"provider,omitempty"`
    Model        string   `json:"model,omitempty"`
    NoChange     bool     `json:"no_change,omitempty"`
}
```

**状态与转换表**。`status` 仍是看板唯一状态；新增 `waiting`、`failed`、`cancelled`，保留 `rejected` 作为旧数据别名。`CanTransition` 在 `internal/kanban/model.go:104` `func CanTransition(from, to TaskStatus) bool {` 里扩展。

| 状态 | 含义 | 允许进入 | 允许离开 | 看板列 |
|---|---|---|---|---|
| `todo` | 排队 | 新建、`amend_task` 唤醒、子任务完成唤醒、wait 到期 | `in_progress`、`cancelled` | 待办 |
| `in_progress` | 执行中 | 启动、用户催办 | `waiting`、`needs_approval`、`review`、`done`、`failed`、`cancelled` | 进行中 |
| `waiting` | 等机器或等人 | `waiting` 结果、`delegated` 结果 | `todo`（check 变化/子任务完成）、`cancelled` | 进行中（等待标识） |
| `needs_approval` | 等用户拍板 | `needs_decision` 结果 | `todo`（用户答卡）、`cancelled` | 待你处理 |
| `review` | 完成待验收 | `done` 结果且开自动评审 | `done`、`rejected` | 待审阅 |
| `done` | 已完成 | 验收通过或自动接受 | — | 已完成 |
| `failed` | 失败 | `failed` 结果且不再重试 | `todo`（用户重开） | 待办（失败标识） |
| `cancelled` | 取消 | 用户取消、父任务取消 | — | 已完成（取消标识） |
| `rejected` | 旧数据别名 | 旧库 | 读时映射为 `cancelled` | 已完成 |

同一任务允许「重开」：`done`、`failed`、`cancelled` 收到用户新消息时回到 `todo`，保留同一 ID 与同一 `ThreadID`；历史留在这段会话里。这正是 noop 的 `reopenTask` 语义，`src/brain.ts:44` `export function reopenTask(file: string, text: string) {`。

### 3.4 五态结果协议与提交工具

Go 类型放在 `internal/kanban/result.go`：

```go
type ResultStatus string

const (
    ResultDone          ResultStatus = "done"
    ResultFailed        ResultStatus = "failed"
    ResultWaiting       ResultStatus = "waiting"
    ResultDelegated     ResultStatus = "delegated"
    ResultNeedsDecision ResultStatus = "needs_decision"
)

type ExecutorResult struct {
    Status       ResultStatus     `json:"status"`
    Summary      string           `json:"summary"`
    WaitingOn    string           `json:"waiting_on,omitempty"`
    WaitKind     string           `json:"wait_kind,omitempty"`     // human | machine
    WaitMinutes  int              `json:"wait_minutes,omitempty"`  // 5..120
    WaitCheck    string           `json:"wait_check,omitempty"`
    Question     string           `json:"question,omitempty"`
    Options      []DecisionOption `json:"options,omitempty"`
    Recommendation string         `json:"recommendation,omitempty"`
    Followups    []Followup      `json:"followups,omitempty"`
    Lessons      []string         `json:"lessons,omitempty"`
    Attachments  []string         `json:"attachments,omitempty"`
}

type DecisionOption struct {
    Key  string `json:"key"`  // A/B/C
    Text string `json:"text"`
}

type Followup struct {
    Title    string `json:"title"`
    Body     string `json:"body,omitempty"`
    Project  string `json:"project,omitempty"`
    Priority string `json:"priority,omitempty"`
    Agent    string `json:"agent,omitempty"` // 预留，mcode 暂不暴露 agent 名册
}

func ParseExecutorResult(text string) (ExecutorResult, error) // 取最后一个 ```json 块
```

提交工具继续叫 `kanban_submit_result`，避免执行者提示词和 MCP 白名单大改。`internal/mcp/tools_kanban.go:421` 的 `kanban_submit_result` 注册 的参数从 `outcome=done|blocked` 扩成五态，并新增上面字段。旧执行者仍可传 `outcome=blocked`，服务端映射为 `needs_decision`（问题取 `summary`，选项默认 A 继续 / B 停下），保证向后兼容。

`internal/kanban/receipt.go:103` `func (s *Service) SubmitTaskResult(ctx context.Context, taskID, callerThreadID, outcome, summary string) error {` 改为接收 `ExecutorResult`，保留一个只收 `outcome/summary` 的包装函数给旧测试。结果由 `internal/kanban/watchdog.go:215` `decision := w.svc.decideFinished(ctx, t, now, st.HostToolsUnavailable)` 与 `internal/kanban/scheduler.go:244` `decision := g.svc.decideFinished(ctx, t, now, st.HostToolsUnavailable)` 共同消费。

各状态的落点：

| 结果 | 任务状态 | 动作 |
|---|---|---|
| `done` | `review` 或 `done` | 摘要进 `TaskMeta` 与活动文本；owner 任务推送，其余只留一行。 |
| `failed` | `failed` | 记录失败原因；owner 且非子任务推一句白话；否则只记日志。 |
| `waiting` | `waiting` | 写 `wait_kind`、`wake_at`、`wait_check`；`human` 没有定时器，只等事件或用户消息。 |
| `delegated` | `waiting` | 建子任务，写 `children`；父任务不再运行，全部子任务结束后重开汇总。 |
| `needs_decision` | `needs_approval` | 建决策卡，写 `decision_id`；无卡不执行危险动作。 |

结果分流规则（与 noop 对齐）：

- `done` 且是用户亲自交代、不是子任务、不是例行无变化：主会话留一条结果行并推送一次。参考 `noop src/cli.ts:248` 的 `else if (res.status === "done" && ownerWork && !meta.parent)`。
- `done` 且是例行、子任务或补充步骤：主会话只留一行 `steward_result`，不推送。参考 `noop src/cli.ts:251` 的 `else if (res.status === "done")` 分支。
- `done` 且是例行任务且摘要表示没有变化：只写日志，不产生主会话消息。参考 `noop src/waits.ts:42` `export const isNoChangeRoutine = (source: string | undefined, summary: string) =>`。
- `failed`：只有用户亲自交代且不是子任务时推一句白话；其余只写日志。参考 `noop src/cli.ts:427` `async function handleFailure` 的 `ownerCares` 判断。
- `waiting`：不推送；任务页面显示等待原因与下次检查时间。
- `delegated`：不推送；父任务汇总完成后再按 `done` 规则分流。
- `needs_decision`：主会话出现卡片并推送一次，因为需要用户拍板。

等待与父任务唤醒放在 `internal/kanban/waits.go`（新增）并挂到现有 `GoalScheduler.Tick`：`wait_check` 用 `exec.CommandContext` 跑，单次超时 60 秒，退避 5/10/20/40 分钟，最多 6 次；超过次数转 `needs_approval` 并生成一张「卡住」卡。参考 noop 的 `src/waits.ts:14` `export const backoffMinutes = (checks: number) => 5 * 2 ** Math.min(3, Math.max(0, checks));` 与 `src/waits.ts:17` `export const MAX_WAKES = 6;`。

### 3.5 决策卡

数据模型放在 `internal/store/steward_decisions.go`（新增），表 `steward_decisions`：

```go
type StewardDecision struct {
    ID          string    // D-0001
    TaskID      string
    ThreadID    string
    Question    string
    Context     string
    OptionsJSON string    // [{"key":"A","text":"..."}]
    Recommended string
    Status      string    // open | answered | ignored | superseded
    Answer      string
    AnsweredBy  string    // user:desktop | user:web | user:push | frontdesk
    AnsweredAt  *time.Time
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

编号规则：`CreateStewardDecision` 在一个事务里取 `SELECT MAX(CAST(SUBSTR(id,3) AS INTEGER)) FROM steward_decisions`，格式化为 `D-%04d`；不足四位补零，超过四位自然增长。并发由 SQLite 事务串行化保证。

所有回答归一成一条用户消息 `D-0001 <answer>`：

- 点卡片按钮：桌面 `Client.AnswerStewardDecision(ctx, id, key)` → 远程 `steward.decisions.answer` → `frontdesk.AnswerDecision`。
- 键盘 1-9：复用 `internal/ui/approval.go:92` `func approvalShortcutAction(` 的按键判定，但目标换成决策卡；1-9 映射到选项下标。
- 通知按钮：`PushPayload` 已有 `RequestID` 与 `Answers` 字段，`internal/remote/webpush.go:292` `RequestID string`、`internal/remote/webpush.go:298` `Answers []PushAnswerOption`；service worker 把按钮点击转成 `steward.decisions.answer`。
- 手打 `D-0001 A`：`MemoryBackend.SendMessage` 进入前台前先匹配 `^\s*(D-\d{4,})\b`；命中直接回答，不调用模型。参考 `noop src/server.ts:293` `const ID = /^\s*([DLP]-[A-Z]?\d{4})\b[\s:：,，\-]*([\s\S]*)$/;`。
- 通知点开：带 `ref=D-0001` 定位到卡片。

5 分钟内的改口：`Desk` 在内存里记 `lastAnswer map[string]lastAnswer{ID, At}`。5 分钟内的新消息没有写 ID 时，快照里带一行「刚答过 D-0001，这条可能是在改答」；前台判断后调用 `answer_decision` 改答，原卡保持已答记录并追加一行「已改答为 …」。超过 5 分钟不改，按新工作处理。

卡片消息：主会话追加 `role=assistant`、`kind=steward_decision` 的消息，正文是 JSON 载荷 `{id,task,question,options,recommended}`；回答后追加 `kind=steward_decision_answer` 的 JSON `{id,answer}`。UI 用消息历史还原卡片；原卡变成只读记录，和 `internal/ui/timeline_answered_question.go:25` `const questionAnswerMessageKind = "question_answer"` 的做法一致。

### 3.6 硬锁与一次性解锁

硬锁清单放在 `internal/agent/gate/lock.go`（新增），由内置引擎、MCP 工具、外部队列共用：

```go
type HardLock struct {
    ID      string   // merge | release | push-remote | secret | destructive
    Command string   // 正则，只匹配 bash 命令
    Path    string   // 可选，匹配写文件路径
}

func CheckHardLock(command, path string, override string) (HardLock, bool)
```

默认清单与 noop 对齐：合并 PR、强制推送、推 main/master、发布（tag、cargo/npm/bun publish）、`gh secret`、删除仓库、改仓库设置、`rm -rf /`、`rm -rf ~`。参考 `noop src/gates.ts:9` `export const DEFAULT_GATES = [` 与 `noop src/gates.ts:27` `export function claudeDisallowed(cfg: Cfg, override = false): string[] {`。

落点：

- 内置引擎：`internal/agent/gate/gate.go:131` `func Check(in Input) Verdict {` 增加 `in.HardLockOverride`；命中硬锁且无 override 时返回 `Deny`，理由说明「需要用户答决策卡」。执行者启动时的 `agent.Options.Gate` 已有字段，`internal/agent/agent.go:32` `Gate      *gate.Session`，把 override 值放进去。
- Claude CLI：`internal/harness/harness.go:68` `type Turn struct {` 新增 `DeniedPatterns []string`；`internal/providers/claude/claude.go:154` `"--disallowed-tools", nativeSubagentTools,` 改为把 `Agent,Task` 与硬锁模式合并传入。Claude 的 `--disallowed-tools` 支持 `Bash(gh pr merge:*)` 形式。
- Codex / ACP：协议没有逐命令禁列表；在 `internal/remote/memory_backend.go:6778` `		Approver:        &threadApprover{b: b, threadID: threadID, turnID: turnID, sessionEpoch: recordedEpoch, confirmsDelivery: confirmsDelivery},` 外面包一层 `hardLockApprover`，收到 `ApprovalRequest.Kind == command` 时按 `CheckHardLock` 直接 `Deny`。若 provider 在 `ModeAuto` 下不触发审批，则该任务强制 `ModeSupervised`（Codex 映射为 `read-only`），代价是不能写文件；对需要合并/发布的活，改为在任务里只准备、不执行，等用户答卡后再换 provider 重跑。参考 `internal/providers/codex/codex.go:596` `func mapPermissionMode(mode harness.PermissionMode) (sandbox string, approvalPolicy string) {`。
- MCP 工具：`internal/mcp/tools_kanban.go` 里能造成外部可见动作的 `kanban_review_task` 自动接受只在用户验收路径出现；执行者不得调用 `kanban_review_task` 自己接受。硬锁不依赖模型自觉。
- 解锁：用户答卡时 `frontdesk.AnswerDecision` 只在决策卡属于该任务时写 `meta.gate_override=D-xxxx`，并只对下一次运行有效。运行开始前读取，任务进入 `done|failed|waiting|needs_decision` 后清空；`waiting` 延续同一任务时保留，和 noop 的 `gate_override` 跨等待保留一致，`src/decisions.ts:84` `export function closeDecision`。
- `internal/harness/permission.go:135` `func Decide(mode PermissionMode, rules []Rule, toolName string, arg string, readOnlyTool bool) Decision {` 能承载「按工具名与参数模式拒绝」的规则，但它只是调用方的建议接口，不是所有外部 CLI 都会经过它；因此硬锁必须同时在 gate 与审批层执行，`Decide` 只作为内建引擎与自带工具的第二道防线。参考 `internal/harness/permission.go:196` `if readOnlyTool || toolName == "write_file" || toolName == "edit_file" ||` 的现有放行名单，硬锁规则要排在它之前。
- 前台工具面没有合并、发布、推送远端、设置、钱，所以这几类动作不会被前台模型直接发起；它们只能由执行者在硬锁下请求决策卡。

### 3.7 后台事件在新模式下的处理

新增 `internal/app/frontdesk_events.go`，订阅 `attention.Service.Watch`，不再唤醒任何模型。规则表：

| 事件 | 对 owner 直接交代的任务 | 对 routine / 子任务 |
|---|---|---|
| `thread_failed` | 主会话一行结果，推一句白话 | 只写日志 |
| `thread_quota` | 主会话一行「额度用尽，已停下」，推送一次 | 只写日志 |
| `thread_stalled` / `thread_no_progress` | 主会话一行，不推送 | 只写日志 |
| `kanban_failed` | 任务页面标记失败，主会话一行；推送一次 | 只写日志 |
| `kanban_review` / `kanban_needs_approval` | 保留收件箱与看板卡片，主会话一行 | 同左，不推送 |
| `kanban_plan_failed` | 保留收件箱，主会话一行 | 同左 |
| `automation_failed` / `automation_finished` | 只在收件箱 | 只在收件箱 |
| `bee_failed` | 只在收件箱 | 只在收件箱 |

`OwnerActionKinds` 原样保留，`internal/attention/attention.go:48`。事件的 `DedupeKey` 与每日上限继续由 attention 服务负责，`internal/attention/attention.go:95` `type Input struct {`。

### 3.8 记忆读写

前台只做三件事：搜索、记住、改掉。实现放在 `internal/frontdesk/memory.go`，通过接口注入，不直接依赖 `internal/mcp`：

```go
type MemoryService interface {
    Search(ctx context.Context, projectPath, query string, limit int) ([]MemoryHit, error)
    Get(ctx context.Context, projectPath, id string) (MemoryHit, error)
    Propose(ctx context.Context, req ProposeRequest) (ProposeResult, error)
    Commit(ctx context.Context, id string) (ProposeResult, error)
    Forget(ctx context.Context, projectPath, id string) (bool, error)
}
```

app 层 `internal/app/frontdesk_memory.go`（新增）实现它：搜索与读取复用 `mcp.Options.MemorySearch`、`MemoryGet`（`internal/mcp/server.go:55` `MemorySearch func(ctx context.Context, projectPath string, q nmem.SearchQuery) ([]nmem.Memory, error)`、`internal/mcp/server.go:57` `MemoryGet func(ctx context.Context, projectPath, id string) (nmem.Memory, error)`）；写入复用 `store.CreateMemoryProposal`（`internal/store/memory_proposals.go:67`）与 `MemoryCommit`（`internal/mcp/server.go:67` `MemoryCommit func(ctx context.Context, proposalID string) (store.MemoryProposal, string, error)`）。`remember` 带 `UserRequested=true`，与 `internal/mcp/tools_memory.go:701` `if userRequested && tc.Server.opts.MemoryCommit != nil {` 一致，立即写长期记忆；失败时保留提议并在回复里说一句。`edit_memory` 走 `target_memory_id` 改写，不新建重复记忆。

### 3.9 前台模型来源、默认模型与无 API 来源退路

**来源解析**：只依赖统一 API 引擎计划里的 `internal/apisource`。`internal/apisource/source.go:25` `type Source struct {`、`internal/apisource/source.go:112` `func Resolve(cfg config.Config, secrets config.SecretStore) []Source {`。前台构造客户端的函数放在 `internal/frontdesk/client.go`：

```go
type Sources interface {
    Resolve() []apisource.Source
    Secrets() config.SecretStore
}

func ClientFor(src apisource.Source, secrets config.SecretStore) (llm.Client, string, error)
```

- `ProtocolAnthropic` → `llm.NewAnthropic`（`internal/agent/llm/anthropic.go:26` `func NewAnthropic(cfg AnthropicConfig) Client {`）。
- 否则 → `llm.NewOpenAI`（`internal/agent/llm/openai.go:35` `func NewOpenAI(cfg OpenAIConfig) Client {`）。
- Key 用 `src.APIKey(secrets)`（`internal/apisource/source.go:79` `func (s Source) APIKey(secrets config.SecretStore) string {`）。

**默认模型**：便宜快优先。`internal/frontdesk/model.go` 的选择顺序：

1. 用户在设置里显式选的 `steward.model`（新语义：前台模型）。
2. 当前配置的 `steward.provider` 对应的来源；模型为空时在该来源的 `Source.Models` 里选第一个命中 `haiku`、`mini`、`flash`、`deepseek-chat`、`glm-4-flash` 的模型。Magpie 端点的模型来自网关目录缓存，`internal/providers/magpie/magpie.go:358` `func CachedModels(baseURL, apiKey string) ([]Model, bool) {`。
3. 没有显式来源时，按 `apisource.Resolve` 的固定顺序取第一个有 Key 的来源，再按第 2 条选模型。
4. 都没有时，取第一个来源的 `DefaultModel`。
5. 一个来源都没有：模型不可用，走退路。

**API Key 来源**：与统一 API 引擎一致，keyring 账号 `anthropic.apiKey`、`openai.apiKey`、各预设的账号名，最后回退环境变量；见 `internal/apisource/source.go:79`。设置页复用「API Key」分组和「Magpie」页，不再为总管单独维护 provider/model 两套。

**CLI 降级**：

- 设置 `steward.cli_fallback` 为布尔值，默认 `false`；设置页增加开关，不写说明副文案。
- 没有可用 `apisource` 来源，或 `Client.Stream` 鉴权、网络失败时，若开关开启，则通过注入的接口调用 `internal/textgen/generate.go` 的 `GenerateWithOptions`，使用 `CheapestModel` 做单次调用；输入使用与第 3.2 节相同的快照文本及用户原话。
- 输出只允许一个 JSON 对象：`{"reply":"...","calls":[{"tool":"create_task","args":{}}]}`。`reply` 必须是字符串，`calls` 必须是数组；每项包含 `tool` 与 `args`。只允许写工具 `create_task`、`amend_task`、`remember`、`answer_decision`，没有读轮次；状态类问题只能用快照内容回答。
- 解析成功后先将非空 `reply` 作为灰字落库并推送，再按顺序执行 `calls`；未知工具、读工具名或参数错误的项丢弃并记日志。
- 开关关闭、CLI 不可用、调用失败或 JSON 解析失败时，静默变任务：短消息且 10 分钟内有最近任务则追加，否则新建任务。
- 新增 `internal/frontdesk/cli_fallback.go`，通过接口注入 textgen，便于测试。测试覆盖：开关关闭不调用 CLI；JSON 正常时先回复后写；JSON 损坏时变任务；读工具名在 `calls` 中被丢弃。

---

## 四、分阶段实施

阶段 0、1 与统一 API 引擎计划无关，可先做；阶段 2 起依赖统一 API 引擎计划阶段 2 的 `internal/apisource`。先完成不接前台的底座、前台包和决策后端，再在阶段 4 一次切换；回退靠版本回退。

### 阶段 0：任务元数据与决策卡存储（可并行 A）

**目标**：只加数据底座，不改任何用户可见行为。

**依赖**：无。

**文件与步骤**：

1. `internal/store/store.go:44` `var migrations = []string{` 末尾追加一条迁移：`ALTER TABLE tasks ADD COLUMN meta TEXT NOT NULL DEFAULT '{}';` 并新建 `steward_decisions` 表与索引 `idx_steward_decisions_task`、`idx_steward_decisions_status`。迁移版本号自然加一。
2. `internal/store/kanban.go:13` `type Task struct {` 增加 `Meta string \`json:"meta"\``；同步 `internal/store/kanban.go:96` `func createTask(`、`internal/store/kanban.go:163` `func (s *Store) GetTask(ctx context.Context, id string) (Task, error) {`、`internal/store/kanban.go:226` `func (s *Store) ListTasks(ctx context.Context, projectID string) ([]Task, error) {`、`internal/store/kanban.go:304` `func (s *Store) UpdateTask(ctx context.Context, t Task, expectedRevision int) (Task, error) {` 的列与扫描。
3. 新增 `internal/store/steward_decisions.go`：`CreateStewardDecision`、`GetStewardDecision`、`ListStewardDecisions`、`AnswerStewardDecision`、`SupersedeOpenDecisionsForTask`。编号规则按第 3.5 节。
4. 新增 `internal/kanban/taskmeta.go`：`ParseTaskMeta`、`EncodeTaskMeta`、`PatchTaskMeta`（读改写带 revision）。只处理 JSON，不碰数据库。
5. `internal/kanban/service.go:341` `func (s *Service) CreateTask(ctx context.Context, in CreateTaskInput) (store.Task, error) {` 增加 `Origin`、`Provider`、`Model` 入参并写入 `meta`；旧调用方传空值得到 `{}`。
6. 新增 `internal/store/steward_decisions_test.go`、`internal/kanban/taskmeta_test.go`，更新 `internal/store/kanban_test.go`。

**删除**：无。

**可并行**：步骤 1 与步骤 3 可并行（新表不依赖 `tasks` 列）；步骤 2 与步骤 4 必须在步骤 1 之后；步骤 5 依赖步骤 2；步骤 6 最后做。每个编号步骤单独提交，便于回退。

**测试（行为）**：迁移从旧版本升上来后旧任务 `meta` 为 `{}`；`UpdateTask` 写入 meta 后 revision 冲突仍返回 `store.ErrRevisionConflict`；决策编号连续、并发创建不重号；同一任务的新卡会把旧 open 卡置为 `superseded`。

**协议**：本阶段不改 `docs/remote-control.schema.json` 与 `docs/remote-control-protocol.md`。

**验收**：`go test ./internal/store/... ./internal/kanban/...` 通过；用户打开旧数据库无报错，看板显示与旧版一致。

**回退**：回退代码即可；迁移新增列对旧代码无影响，旧代码忽略 `meta` 与新表。

### 阶段 1：五态结果、等待调度、父子任务（可并行 B）

**目标**：执行者可以交五态结果，任务能等待、能拆子任务、能等父任务汇总。前台还没接。

**依赖**：阶段 0。

**文件与步骤**：

1. 新增 `internal/kanban/result.go`：`ExecutorResult`、`ParseExecutorResult`、`Validate`。参考 `noop src/runner.ts:11` `export type Result = {`。
2. `internal/kanban/receipt.go:16` `	ReceiptOutcomeDone    = "done"` 与 `internal/kanban/receipt.go:17` `	ReceiptOutcomeBlocked = "blocked"` 保留常量，新增五态常量；`internal/kanban/receipt.go:103` `func (s *Service) SubmitTaskResult(ctx context.Context, taskID, callerThreadID, outcome, summary string) error {` 改成 `SubmitExecutorResult(ctx, taskID, callerThreadID string, res ExecutorResult) error`，旧签名改成包装。
3. `internal/kanban/receipt.go:328` `func (s *Service) decideFinished(ctx context.Context, t store.Task, now time.Time, hostToolsUnavailable bool) finishDecision {` 的 `finishDecision` 增加 `result ExecutorResult`，让 watchdog 与 scheduler 能区分 `waiting|failed|delegated|needs_decision`。
4. `internal/kanban/watchdog.go:215` `decision := w.svc.decideFinished(ctx, t, now, st.HostToolsUnavailable)` 与 `internal/kanban/scheduler.go:244` `decision := g.svc.decideFinished(ctx, t, now, st.HostToolsUnavailable)` 按五态落库：`waiting` 写 `meta.wait_*`，`delegated` 建子任务并写 `children`，`needs_decision` 调 `CreateStewardDecision`，`failed` 落 `failed`。
5. 新增 `internal/kanban/waits.go`：`CheckWaits(ctx, now)`、`WakeParents(ctx)`、`BackoffMinutes`。`internal/kanban/scheduler.go:104` `func (g *GoalScheduler) Tick(ctx context.Context) {` 开头调用它们。
6. `internal/kanban/service.go:341` 增加 `CreateChildTask`（内部方法即可），子任务 `DependsOn` 为空、`meta.parent` 指向父任务；父任务 `waiting` 不占用执行槽。
7. `internal/mcp/tools_kanban.go:421` 的 `kanban_submit_result` 注册 的 schema 扩成五态与第 3.4 节字段；`internal/mcp/tools_kanban.go:1390` `func handleKanbanSubmitResult(tc ToolContext, args map[string]any) ToolResult {` 解析并提交。旧 `outcome=blocked` 映射为 `needs_decision`。
8. 更新 `internal/kanban/receipt_test.go`、新增 `internal/kanban/result_test.go`、`internal/kanban/waits_test.go`、`internal/kanban/delegation_test.go`。

**可并行**：步骤 1、2、8 可并行（类型与解析独立）；步骤 3、4 在步骤 2 之后；步骤 5 与步骤 7 互不依赖；步骤 6 在步骤 4 之后。

**删除**：`internal/kanban/receipt.go:22` `TaskReceiptPrompt` 的「只许 done/blocked」文案改成五态；旧两态判断分支删除。

**测试（行为）**：
- `done` 按自动评审开关落 `review`/`done`；`failed` 落 `failed` 并记录原因。
- `waiting` 写入 `wake_at`，`CheckWaits` 在命令退出码变化时把任务改回 `todo`；连续 7 次未就绪转 `needs_approval`。
- `delegated` 建 2 个子任务，父任务 `waiting`；两个子任务都 `done` 后父任务回 `todo`，提示词包含子任务摘要。
- `needs_decision` 建 `D-0001`，任务 `needs_approval`，重复提交不建第二张卡。
- 旧 `outcome=blocked` 仍能收尾，不会把任务留在 `in_progress`。

**协议**：`kanban_submit_result` 是 MCP 工具，不在 JSON-RPC schema 里；不改 `docs/remote-control.schema.json`。`docs/remote-control-protocol.md` 不需要新增方法。

**验收**：用户在 dev 版本里让一个任务返回 `waiting`，看到任务进入等待；创建一个文件触发 `wait_check` 后任务自动继续；让任务返回 `delegated`，看到两个子任务与父任务汇总。

**回退**：回退 `internal/kanban` 与 `internal/mcp/tools_kanban.go`；`meta` 列保留但旧代码不读。

### 阶段 2：前台包本体（依赖统一 API 引擎阶段 2）

**目标**：完成前台循环、工具面与降级，不接入 app 与 `MemoryBackend`，只用假 `llm.Client` 测试。

**依赖**：阶段 0、阶段 1；统一 API 引擎计划的阶段 2 必须已提供 `internal/apisource` 与 `llm.Client` 构建。若该阶段尚未合入，可以基于当前仓库已有的 `internal/apisource` 开发，但发布必须等统一计划阶段 2 合入，避免建在随后会被替换的接线上（`docs/superpowers/plans/2026-11-08-unified-api-engine.md` 的「任务 2.1 来源描述层」与「任务 2.2 引擎按来源构建」）。不要依赖将被删除的 `internal/providers/openai` 或 `internal/providers/mcode` 完整 Harness。

**文件与步骤**：

1. 新增 `internal/frontdesk/desk.go`：`Desk`、`Inbound`、`HandleMessage`、`loop`、`snapshot`。`llm.Client` 只用 `internal/agent/llm/types.go:77` `type Client interface {`，消息与工具用 `internal/agent/llm/types.go:11` `type Message struct {`、`internal/agent/llm/types.go:37` `type ToolSpec struct {`、`internal/agent/llm/types.go:44` `type Request struct {`。
2. 新增 `internal/frontdesk/context.go`：按第 3.2 节拼快照。任务来自 `kanban.Service.Board`，决策来自 `ListStewardDecisions`，最近对话来自 feed 历史，记忆来自 `MemoryService.Search`，项目来自 `ProjectService.List`。
3. 新增 `internal/frontdesk/tools.go`：唯一一份工具定义与 `Toolbox`。读：`get_status`、`list_tasks`、`get_task`、`list_decisions`、`read_conversation`、`read_memory`、`search`、`list_projects`。写：`create_task`、`amend_task`、`cancel_task`、`set_priority`、`reassign_task`、`remember`、`edit_memory`、`answer_decision`。每个工具带 `Write` 布尔。mcode 没有 noop 那样的 agent 名册，`reassign_task` 的参数是 `provider` 与 `model`（只对未开始或等待中的任务生效），不是 agent 名。工具描述参考 `noop cloud/src/tools.ts:228` `export const TOOLS: Tool[] = [`。
4. 新增 `internal/frontdesk/exec.go`：实现第 3.2 节循环。`MaxRounds` 必须硬编码默认 4，参考 `noop src/brain.ts:165` `const MAX_ROUNDS = 4;`。
5. 新增 `internal/frontdesk/client.go` 与 `internal/frontdesk/model.go`：按第 3.9 节选来源与模型；失败返回 `ErrNoModel`。
6. 新增 `internal/frontdesk/fallback.go`：模型失败时，短消息（<=40 字符、单行）且最近 10 分钟有任务 → `amend_task`；否则 `create_task`，标题取第一行前 80 字符。参考 `noop src/brain.ts:320` `export function followUpTarget(text: string, events: { from: string; ts: string; ref?: string }[], now = Date.now()): string | undefined {` 与 `noop src/cli.ts:836` 的 `} catch (err) {`。新增 `internal/frontdesk/cli_fallback.go`，按第 3.9 节通过接口注入 textgen，支持可选 CLI 降级与最终静默变任务。
7. 新增 `internal/frontdesk/memory.go` 的记忆接口，按第 3.8 节；app 层实现留到阶段 4。
8. 新增 `internal/frontdesk/desk_test.go`：用假 `llm.Client` 测第 3.2 节 7 条语义，并用假 textgen 测 CLI 降级。

**可并行**：步骤 1、2、3、4 按类型→快照→工具→循环推进；步骤 5、6、7 可并行；步骤 8 随接口完成后编写。

**删除**：无。

**测试（行为，全部用假 `llm.Client`）**：
- 第一轮返回 1 个读工具，第二轮返回文字：模型被调用 2 次，读结果作为 tool 消息喂回，文字只在干净轮落库。
- 第一轮同时返回读和写：只执行读；写给模型的结果是「尚未执行，需要的话再发一次」；第二轮同一写再发才执行。
- 一轮只有写：先调用 `AppendAssistant`，再执行写；用记录顺序断言。
- 第 4 轮仍有读工具：模型恰好调用 4 次，不执行任何写，不落回复，走 fallback。
- `Client.Stream` 返回 `401`：不重试，消息变成任务。
- 参数 JSON 无效或必填缺失：算 `Invalid`，不执行写，喂回错误。
- `create_task` 成功后 `amend_task` 同一任务能追加；`cancel_task` 能取消子任务。
- `answer_decision` 写卡后任务回 `todo`，`meta.gate_override` 只在这次运行存在。
- CLI 开关关闭时不调用；正常 JSON 先回复后写；损坏 JSON 静默变任务；读工具名被丢弃并记日志。

**协议**：本阶段不改 schema。

**验收**：前台包单测通过；dev 端到端验证在阶段 4 切换后进行。

### 阶段 3：决策卡后端与硬锁（依赖阶段 2）

**目标**：完成决策回答与远程协议；硬锁立即对现有看板任务生效，不依赖前台接线。

**依赖**：阶段 0、1、2。

**文件与步骤**：

1. 新增 `internal/frontdesk/decision.go`：`AnswerDecision(ctx, id, answer, by)`、`NormalizeAnswer`、`lastAnswer`。先关闭决策，再按任务状态处理：`needs_approval` → `todo` + `gate_override`；`waiting` → `todo`；`done|failed|cancelled` → 重开同一任务；`in_progress` → 追加消息。
2. 新增 `internal/frontdesk/snapshot` 的 `Answered` 字段：5 分钟内刚答过的 `D-xxxx` 写进快照。
3. 新增 `internal/app/steward_decisions.go`：`Client.AnswerStewardDecision`、`Client.ListStewardDecisions`，并注册远程方法。
4. 新增 `internal/remote/steward_decisions.go`：`steward.decisions.list`、`steward.decisions.answer`；`steward.changed` 载荷增加可选 `decisionID`。
8. 新增 `internal/agent/gate/lock.go`：硬锁清单与 `CheckHardLock`。
9. `internal/agent/gate/gate.go:131` `func Check(in Input) Verdict {` 增加 `HardLockOverride`；命中硬锁返回 `Deny`，理由包含决策卡编号。
10. `internal/harness/harness.go:68` `type Turn struct {` 增加 `DeniedPatterns []string`；`internal/providers/claude/claude.go:154` 合并到 `--disallowed-tools`。
11. `internal/remote/memory_backend.go:6778` 的 `threadApprover` 外面包 `hardLockApprover`，对 `command` 类型请求执行 `CheckHardLock`；解锁值来自任务 `meta.gate_override`。
12. `internal/kanban/service.go:471` `StartTask` 启动前把 `meta.gate_override` 传进运行环境；结果落库后清空。
13. 测试：新增 `internal/frontdesk/decision_test.go`、`internal/agent/gate/lock_test.go`、`internal/providers/claude/hardlock_test.go`、`internal/remote/hardlock_approver_test.go`。

**可并行**：步骤 1–4 的决策后端与步骤 8–12 的硬锁可并行；硬锁按 gate→provider→approver→任务启动顺序推进，步骤 13 随实现完成。

**删除**：无；旧 UI 在阶段 4 删除。

**测试（行为）**：
- 远程回答调用一次 `AnswerDecision`，且只关闭一次卡；按钮、键盘、裸答复和通知路径在阶段 4 验证。
- 已答卡再点无效；5 分钟内第二句「其实选 B」改答，卡片记录两次答案。
- `gh pr merge 12` 在无 override 时被 `gate.Check` 拒绝；有 `D-0001` override 时放行一次；任务结束后再跑又被拒绝。
- Claude 启动参数包含 `Bash(gh pr merge:*)`；Codex 的审批请求被 `hardLockApprover` 拒绝并留下一条系统消息。

**协议**：
- `docs/remote-control.schema.json` 新增 `$defs`：`RemoteStewardDecisionsListParams`、`RemoteStewardDecisionsListResult`、`RemoteStewardDecision`、`RemoteStewardDecisionsAnswerParams`、`RemoteStewardDecisionsAnswerResult`、对应 request/response；`RemoteStewardChangedParams` 增加 `decisionID`（可选）。话题 defs 在阶段 4 删除。
- `docs/remote-control-protocol.md` 的方法清单增加 `steward.decisions.list`、`steward.decisions.answer`，说明 `steward.changed` 可能带 `decisionID`。

**验收（dev 版本）**：现有看板任务尝试 `gh pr merge` 时被硬锁拒绝；决策后端与协议通过对应测试。

### 阶段 4：一次切换前台、界面与旧数据

**目标**：一次完成新前台接线、界面、迁移与旧总管删除。

**依赖**：阶段 0、1、2、3。

**文件与步骤**：

1. 新增 `internal/app/frontdesk_wiring.go`：构造 `frontdesk.Desk`，从 `config.Load` + `apisource.Resolve` 现读来源，不缓存 Key；新增 `internal/app/frontdesk_memory.go`，按第 3.8 节实现记忆接口。`internal/app/app.go:1399` `stewardRT = wireSteward(appInstance, opts.DataDir)` 直接替换为构造 `frontdesk.Desk` 并调用 `backend.SetStewardDesk(desk)`；`internal/app/app.go:154` `func (a *App) Steward() *steward.Runtime {` 改为返回 `*frontdesk.Desk` 或删除。
2. `internal/remote/memory_backend.go:2876` `func (b *MemoryBackend) SendMessage(ctx context.Context, params wire.SendMessageParams) (wire.SendMessageResult, error) {` 开头加钩子：`if b.stewardDesk != nil && b.stewardDesk.IsFeed(params.ThreadID) { return b.acceptStewardMessage(ctx, params) }`。`acceptStewardMessage` 在 app 层追加用户消息、把消息投进前台串行队列，并立即返回受理结果；钩子必须在 `/compact`、`/plan` 与 provider 启动之前。
3. `internal/remote/memory_backend.go` 新增 `SetStewardDesk`、`EnsureStewardFeedThread`、`AppendAssistantKind`、`AppendUserMessage`。feed 线程用普通 `threads` 行，`provider="steward"`，`title=总管`，不创建执行会话；`EnsureStewardFeedThread` 在不存在时通过 store 直接插入，不调用 `createThread`（`internal/remote/memory_backend.go:3981`）。
4. 新增 `internal/store/frontdesk_migration.go`，切换版本启动时幂等迁移：旧主会话重命名为「总管 · 归档」，`steward.thread_id` 写入 `steward.archived_thread_id`，新 feed 线程 ID 写入 `steward.feed_thread_id`；旧键备份到 `steward.legacy_backup` 一行 JSON，保留一个版本，旧账本与话题不再读取。
5. 新增 `internal/ui/steward_decision_card.go`：渲染编号、问题、选项按钮、推荐标记、「忽略」、「其他指示」；按键映射复用 `internal/ui/approval.go:92` `func approvalShortcutAction(` 的思路，只读态显示已答/已忽略。`internal/ui/timeline_rows.go:2766` `case m.Role == "system":` 增加 `kind=steward_decision` 与 `kind=steward_decision_answer` 分支。`internal/remote/webpush.go:292` `RequestID string` 与 `internal/remote/webpush.go:298` `Answers []PushAnswerOption` 接到决策卡，通知按钮通过 `steward.decisions.answer` 回传；只在 `needs_decision` 创建时推送一次。

界面步骤：

1. `internal/ui/timeline_rows.go:48` 附近新增 `stewardReplyKind = "steward_reply"` 与 `stewardResultKind = "steward_result"`。`isHiddenTimelineMessage` 不变；新增分支：`steward_reply` 渲染成单行灰字，无头像、无气泡、无消息操作按钮；`steward_result` 渲染成普通助手消息，带任务链接。
2. `internal/ui/timeline_rows.go:2766` `case m.Role == "system":` 的 `steward_decision` 分支接阶段 3 的卡片。
3. 未读计算：在消息被标记未读的路径里跳过 `kind=steward_reply`；服务端 `HasUnread` 也不因前台灰字置位（在 `internal/remote/memory_backend.go` 追加灰字的方法里不调用未读置位逻辑）。
4. 新增 `internal/ui/steward_task_page.go`：任务页面顶部只显示标题、状态、结果摘要；正文复用现有会话时间线；输入框发到任务线程，不经过前台。
5. `internal/ui/sidebar_steward.go:17` 重做侧栏：只保留「总管」一行与未处理决策数；删除唤醒次数、未启用副文案、话题卡。
6. `internal/ui/settings_page_steward.go:67` 重做分节：启用、昵称、头像、前台模型、安静时段与 `steward.cli_fallback` 开关，不写说明副文案；删除唤醒上限、日报、直通、换新、事件策略、自动折叠思考。
7. 删除 `internal/ui/steward_timeline.go`、`internal/ui/steward_decision_bar.go`、`internal/ui/steward_topic_card.go`、`internal/ui/steward_topics.go`、`internal/ui/steward_topic_types.go`、`internal/ui/steward_split.go` 及其引用。
8. `internal/ui/client.go:222` `type Message struct {` 不变，`Kind` 已能承载新值；更新 `web/src/types/generated.ts` 的消息 kind 与决策类型。
9. `web/src/context/remoteStewardApi.ts:17` 增加决策方法；`web/src/components/Steward*` 替换成决策卡与任务页。
10. 测试：`internal/ui/steward_reply_test.go`、`steward_decision_card_test.go`、`steward_task_page_test.go`、`web` 侧类型检查。

**可并行**：接线步骤 1–3、迁移步骤 4、决策卡步骤 5 可并行开发；界面步骤 1–3 与 5–6 可并行，界面步骤 4 依赖 1–3；旧运行时与 MCP 删除可并行，最终接线、迁移、UI、旧代码与协议删除必须一同切换验证。

**测试（行为）**：
- `steward_reply` 不渲染头像/气泡，不进入未读统计。
- `steward_result` 点击能打开任务线程。
- 决策卡在只读态不可再提交。
- 设置页没有唤醒次数、直通、换新、日报等行。

**删除旧总管的步骤**：

3. 删除 `internal/steward/` 下非测试文件：`steward.go`、`hooks.go`、`ledger.go`、`passthrough.go`、`prompt.go`、`receipt.go`、`rename.go`、`rotation.go`、`topic_scope.go`、`topics.go`、`wake.go`、`actions.go`。
4. 删除对应测试：`internal/steward/*_test.go` 全部。
5. 删除 `internal/app/steward_wiring.go`、`steward_actions.go`、`steward_assign.go`、`steward_plan.go`、`steward_suggestions.go`、`steward_suggestion_stats.go`、`goal_review_wake.go` 中总管唤醒分支；保留 `internal/app/progress_watchdog.go` 与 attention 接线。
6. 删除 `internal/app/client_steward.go` 的话题方法、`internal/app/remote_steward.go` 的话题转发；保留 `OpenStewardThread` 改为返回 feed 线程。
7. 删除 `internal/mcp/tools_steward.go` 的旧专属工具与 `StewardHooks`；保留 `internal/mcp` 对新 `frontdesk` 工具定义的注册。`internal/mcp/server.go:90` `stewardMu sync.RWMutex` 与 `SetSteward` 删除。
8. 删除 `internal/ui/steward_client.go` 的话题接口；`internal/ui/steward_*.go` 只留新组件。
9. 删除 `internal/remote/steward_topics.go` 与 schema 里 `steward.topics.*` 全部 defs；`docs/remote-control-protocol.md:56`、`:57`、`:58`、`:59`、`:60`、`:61`、`:62` 的 topic 方法删除。
10. 删除 `internal/store/steward_scores.go` 的写入路径，`steward_approval_scores` 表保留只读一个版本，供历史统计与 judge 使用。
11. 更新 `internal/config/config.go:1285`、`internal/config/config.go:1286`、`internal/config/config.go:1294`、`internal/config/config.go:1295`、`internal/config/config.go:1297`、`internal/config/config.go:1299` 的旧字段：删除 `RotateIdleMinutes`、`RotateContextTokens`、`PassthroughOff`、`AutoCollapseReasoningOff`、`DailyReport`、`DailyReportAt`、`EventPolicies`、`DailyWakeLimit`；增加默认关闭的 `steward.cli_fallback`。
12. 全仓 `grep -rn "steward_wake\|notify_owner\|plan_request\|steward_assign_task\|steward_open_topic\|steward.ledger\|steward.topics"` 清零（历史文档与 changelog 除外）。
13. 测试：删除旧测试；新增 `internal/app/frontdesk_wiring_test.go`、`internal/mcp/frontdesk_tools_test.go`、`internal/remote/steward_feed_test.go`。

**删除**：上表全部旧部件。

**测试（行为）**：
- 旧 `steward.thread_id` 存在时也能升级：新 feed 线程创建，旧线程作为归档保留。
- 旧话题会话仍能在普通会话列表打开，但不再出现在总管入口。
- 全仓编译通过，旧符号无引用。

**协议**：`docs/remote-control.schema.json` 更新消息 kind 与决策卡类型，删除 `steward.topics.*` 与话题类型；`steward.open` 的 `threadID` 语义改为 feed 线程；新增 `steward.feed` 可选方法返回 feed 线程 ID。

**验收（dev 版本）**：
1. 配置 API Key 后询问状态、交代任务、说「谢谢」、请求记忆；确认灰字、任务和记忆正确，刷新后回复仍在。
2. 清空 API Key 并关闭 CLI 开关，确认消息静默变任务；打开开关验证可用 CLI 的回答和写入，CLI 不可用或输出损坏时仍静默变任务。
3. 验证灰字无头像、气泡与未读圆点；执行者结果可打开任务页面，页面输入只进任务；侧栏显示决策数，设置页包含 CLI 开关且无旧字段。
4. 让任务返回 `needs_decision`，分别通过按钮、键盘、裸答复、通知回答；确认卡片只关闭一次，5 分钟内改答有记录；硬锁在答卡后只放行一次。
5. 首次启动确认新总管为 feed，旧总管显示「总管 · 归档」且历史可读；旧话题不再显示在入口，原看板目标与任务仍可打开，旧键已备份。

**回退**：回退到切换前版本；归档与备份保留一个版本。

### 阶段 5：文档重写与旧表、旧键清理

**目标**：文档与新行为一致；归档保留一个版本后清理旧表、旧键。

**依赖**：阶段 4；清理须等归档保留满一个版本。

**文件与步骤**：

1. 重写 `docs/dev/05-steward-attention.md`（见第六节）。
2. 更新 `docs/dev/01-architecture.md:61` 的包表、`docs/dev/02-session-runtime.md:37` 的 `SendStewardWake`、`docs/dev/06-kanban-automation.md:47` 的验收流程、`docs/dev/07-mcp-context-memory.md:36` 的总管工具表、`docs/dev/09-desktop-ui.md:69` 的 UI 文件表、`docs/dev/10-store-config.md:24` 的设置键、`docs/dev/11-dev-guide.md:94` 的「默默」别名、`docs/dev/README.md:17`、`docs/dev/README.md:35`。
3. 更新 `docs/user-guide.md:297` 的总管章节：只写「按 ⇧⌘T 打开总管，把工作交给它；需要拍板的事会变成卡片」。
4. 更新 `docs/remote-control-protocol.md:55` 的方法清单与 `docs/remote-control.schema.json` 的 defs。
5. 迁移在阶段 4 完成；归档保留满一个版本后，清理 `steward.ledger`、`steward.topics` 与 `steward.legacy_backup` 等旧键。
6. 删除 `steward.decision_seq` 以外的旧设置键：`steward.wake_day`、`steward.wake_count`、`steward.last_wake_at`、`steward.wake_limit_notice.*`；阶段 4 已备份，保留满一个版本后清理备份。
7. `steward_approval_scores` 表保留只读用于历史统计，停止写入；其他无用旧表在保留满一个版本后清理。
8. 更新 `README.md` 能力列表（若提到总管）与 `CHANGELOG.md`。

**可并行**：步骤 1–4（文档）与步骤 5–7（到期清理）的准备可并行；步骤 8 最后做一次全仓校对。

**测试**：迁移幂等；旧键迁移后新键存在；`grep` 文档无旧常量引用。

**验收**：文档中的链接可跳转；`steward.decisions.*` 在协议文档与 schema 中一致。

### 阶段 6（可选）：judge、渐进放权、空闲菜单、supervisor

**目标**：在新总管稳定后增加「自动答有先例的卡」与「空闲时给下一步菜单」。

**依赖**：阶段 3、4、5。

**文件与步骤**：

1. 新增 `internal/frontdesk/judge.go`：按 `noop src/decisions.ts:25` `export async function judge(id: string, cfg: Cfg): Promise<Verdict | null> {` 的提示词结构，读记忆、原则、过往决策，给 `decided|sent_back|hold`；默认 `hold`，只对非合并、非钱、非密钥的卡生效。
2. 决策卡增加 `shadow_answer` 字段，记录 judge 的答案但不下发；用户答后计算一致率，复用 `internal/store/steward_scores.go:35` 的评分模型。
3. 渐进放权：新增 `steward.autonomy` 配置 `onboarding|assisted|autonomous`，参考 `noop src/autonomy.ts:9`；默认 `onboarding`。
4. 空闲菜单：新增 `internal/frontdesk/idle.go`，只在无运行中任务且无未答卡时生成一张 `kind=menu` 的决策卡，冷却 3 小时，参考 `noop src/cli.ts:543` `export function maybeProposeNext(cfg: Cfg, host: string, state: Record<string, string>) {`。
5. supervisor 巡检：复用 attention 的 `thread_no_progress`，只生成一行日志/卡片，不自动改任务。

**删除**：无。

**测试**：judge 只答有先例的卡；merge 卡永远 `hold`；空闲菜单不重复；supervisor 不自动重跑。

**验收**：用户连续答 10 张卡后，judge 影子一致率可见；没有越权自动答卡。

**回退**：关掉 `steward.autonomy` 与 judge 开关。

### 并行关系

- 阶段 0 先完成；阶段 1 依赖阶段 0。
- 阶段 2 依赖阶段 0/1 与统一 API 引擎阶段 2；阶段 3 依赖阶段 2，决策后端和硬锁可并行。
- 阶段 4 等阶段 2/3 完成后一次切换；接线、迁移、界面和删除旧实现可并行开发，一同验收。
- 阶段 5 在阶段 4 后重写文档，旧表和旧键保留满一个版本后清理；阶段 6 可选，最后做。

---

## 五、数据迁移

升级后用户看到的变化：

1. 首次启动创建新的主会话 feed 线程，标题「总管」。旧总管会话重命名为「总管 · 归档」，保留在会话列表，可打开看历史；它不再接收事件，也不再被唤醒。
2. 旧账本、旧话题不进入新界面。旧账本条目保留在 `steward.ledger` 设置里一个版本；旧话题对应的看板目标与任务原样保留，仍可在看板打开。话题会话本身是普通会话，仍在列表里。
3. 旧唤醒计数、上次唤醒时间不再显示；设置页不再有「今日已提醒」。
4. 旧建议统计保留表但停止写入；设置页的准确率分节删除。
5. 新任务从升级后开始带 `meta`；旧任务 `meta` 为 `{}`，`Origin` 视为 `routine`（不推送），用户可手动重开。

阶段 4 执行迁移与备份；阶段 5 在保留满一个版本后清理旧键和无用旧表。具体迁移：

| 数据 | 位置 | 处理 |
|---|---|---|
| 总管主会话 | `steward.thread_id`（`internal/steward/steward.go:34` 附近） | 读到后写入 `steward.archived_thread_id`，标题改为「总管 · 归档」；新 feed ID 写 `steward.feed_thread_id`。 |
| 账本 | `steward.ledger`（`internal/steward/ledger.go:21`） | 不读，不删；备份到 `steward.legacy_backup`。 |
| 话题 | `steward.topics`（`internal/steward/topics.go:23`） | 不读，不删；话题会话与 Goal 原样保留。 |
| 唤醒计数 | `steward.wake_day`、`steward.wake_count`、`steward.last_wake_at`（`internal/steward/steward.go:34` 附近） | 阶段 4 备份，阶段 5 清理。 |
| 唤醒上限提示去重 | `steward.wake_limit_notice.*` | 阶段 4 备份，阶段 5 清理。 |
| 审批建议计分 | `steward_approval_scores`（`internal/store/store.go:496`） | 保留只读，停止写入。 |
| 旧配置 | `internal/config/config.go:1264` | 保留 `enabled`、`name`、`avatar`、`provider`、`model`、`quiet_start`、`quiet_end`；删除其余旧字段。`provider`/`model` 新语义是前台来源与模型。 |
| 旧任务 | `tasks` 表 | 加 `meta='{}'`；`status=rejected` 读时映射为 `cancelled`。 |
| 旧消息 | `messages` 表 | 不迁移；旧 `steward_wake`、`[[静默]]` 消息渲染时隐藏或按普通系统消息显示。 |

---

## 六、要推翻重写的文档清单

1. `docs/dev/05-steward-attention.md`
   - 删除：总管是什么、唤醒常量表、身份提示词 `[[静默]]`、账本、话题、事件直通、主会话换新、动作和护栏、任务验收的总管分诊、总管时间线。
   - 新大纲：总管是前台；一条消息一次模型调用；上下文快照；工具面；任务即对话；五态结果；决策卡；硬锁；后台事件路由；记忆；配置与设置；UI 文件。
2. `docs/dev/01-architecture.md`
   - 删除：`steward` 包表与「总管经 app 接线」描述、`App.Close` 里的总管关闭顺序。
   - 新增：`internal/frontdesk` 包与调用链。
3. `docs/dev/02-session-runtime.md`
   - 删除：`SendStewardWake`、`SessionRotator` 的总管用途、`stewardAdditionalDirs`。
   - 新增：feed 线程的 `SendMessage` 钩子。
4. `docs/dev/06-kanban-automation.md`
   - 删除：任务验收由总管处理的段落、`[审阅]` 由总管读改动。
   - 新增：五态结果、等待调度、父子任务、决策卡与用户验收。
5. `docs/dev/07-mcp-context-memory.md`
   - 删除：总管专属工具表、话题工具表。
   - 新增：前台工具定义与 MCP 共用、决策工具、记忆工具。
6. `docs/dev/08-remote-web.md`
   - 删除：`steward_topics.go`、`steward.topics.*`、`steward.changed` 的话题语义。
   - 新增：`steward.decisions.*`、feed 线程、`steward.changed`。
7. `docs/dev/09-desktop-ui.md`
   - 删除：总管时间线紧凑形态、决策置顶条、话题卡。
   - 新增：灰字回复、决策卡、任务页面。
8. `docs/dev/10-store-config.md`
   - 删除：`steward.topics` 设置键、总管旧配置字段。
   - 新增：`tasks.meta`、`steward_decisions`、`steward.feed_thread_id`。
9. `docs/dev/11-dev-guide.md`
   - 删除：「默默」别名与旧设计文档命名。
   - 新增：`frontdesk` 命名约定。
10. `docs/dev/README.md`
    - 删除：总管常驻会话、话题、账本三行术语。
    - 新增：前台、任务、决策卡、注意力收件箱。
11. `docs/user-guide.md`
    - 删除：总管是常驻会话、唤醒、话题的描述。
    - 新增：⇧⌘T 打开总管；说一句话变成任务；需要拍板时点卡片。
12. `docs/remote-control-protocol.md`
    - 删除：`steward.topics.*` 六个方法。
    - 新增：`steward.decisions.list`、`steward.decisions.answer`、`steward.feed`（可选）。
13. `docs/remote-control.schema.json`
    - 删除：`RemoteStewardTopics*` 与话题类型。
    - 新增：决策卡类型、feed 线程、消息 kind 枚举。
14. `README.md` 与 `CHANGELOG.md`
    - 删除：总管是常驻同事、唤醒与账本的描述。
    - 新增：新总管的一句话说明与迁移说明。

---

## 七、已拍板的决定

1. **Q1：可选 CLI 降级，默认关闭。** 后果：CLI 不可用或解析失败时静默变任务。
2. **Q2：扩展现有看板 `tasks`，增加 `meta` 列。** 后果：复用现有调度与 worktree。
3. **Q3：前台模型自动选。** 后果：按第 3.9 节选择来源与便宜快的模型。
4. **Q4：旧数据归档保留一个版本。** 后果：旧主会话可查看，旧键备份后再清理。
5. **Q5：Codex / ACP 硬锁在审批层拦截。** 后果：无法拦截时强制 `ModeSupervised`，只准备不执行。
6. **Q6：按方案 A 推送。** 后果：`needs_decision` 与 owner 直接交代的任务按第 3.4 节规则推送，其余不推送。
7. **Q7：直接替换，不保留模式开关；回退靠版本回退。** 后果：阶段 4 一次切换并删除旧实现。

---

## 八、风险与验证

### 风险

1. **前台延迟**：一次消息最多 4 轮模型调用，读工具多时可能 5-10 秒。缓解：限制读工具结果长度；快照只带必要行；默认便宜快模型；超过 4 轮直接走 fallback。参考 `noop src/brain.ts:165` 的 4 轮上限。
2. **上下文快照太长**：任务、决策、记忆、对话一起塞会挤爆小模型窗口。缓解：任务 40 条、完成 8 条、决策 15 条、记忆 12 行、对话 12 条；每段按字符截断。参考 `noop src/brain.ts:223` `export function context`。
3. **写工具提前执行**：如果模型在带读工具的轮次里发写，必须回退而不是执行。测试要覆盖「读 + 写同轮」。
4. **先回复后写失败**：回复已经落库，写操作失败时用户看到「已办」但实际没办。缓解：写工具结果只用于日志；关键写失败追加一条灰字更正。参考 noop 的 `post` 在写之前，`src/brain.ts:271`。
5. **五态结果解析失败**：外部 CLI 可能不调用工具。缓解：`ParseExecutorResult` 从最后一条助手文本的 ```json 块兜底；没有结果时沿用现有追问两次再升级，`internal/kanban/receipt.go:328`。
6. **等待命令执行风险**：`wait_check` 是执行者给的 shell 命令。缓解：只允许只读命令；用 `gate.Check` 的 `onlyReadsCommand` 逻辑先判定；60 秒超时；不通过就转决策卡。
7. **硬锁绕过**：外部 CLI 的原生命令可能不经过 mcode 审批。缓解：Claude 用 `--disallowed-tools`；Codex/ACP 在审批层拒绝，无法拦截则强制 `ModeSupervised`；不在前台工具面暴露危险能力；所有 unlock 只对一次运行有效。
8. **旧数据丢失**：迁移只加列、不删表；旧键先备份。详见第五节。
9. **界面噪音**：容易加回副标题和说明文字。约束：前台灰字只有一句话；任务页面只显示标题、状态、结果摘要；设置页只保留必要设置和 CLI 降级开关。

### 每阶段在 dev 版本的手动验证

**阶段 0**
1. 用旧数据库启动 dev 版本，确认不弹迁移错误，看板任务数量与升级前一致。
2. 新建一个任务，重启应用，确认任务仍在。
3. 让执行者提交一个普通 `done`，确认与之前行为一致。

**阶段 1**
1. 让任务执行者返回 `waiting`，写 `wait_check` 为 `test -f /tmp/mcode-wait-ready`，`wait_minutes` 为 5。观察任务进入等待。
2. 在终端 `touch /tmp/mcode-wait-ready`。等待调度器下一轮后任务回到待办并继续。
3. 让任务返回 `delegated`，两个 `followups`。观察两个子任务出现，父任务等待；两个子任务都完成后父任务自动重开并汇总。
4. 让任务返回 `needs_decision`。观察任务进入待你处理，数据库出现 `D-0001`（此阶段还没有卡片，可在日志或 DB 查看）。
5. 让任务返回 `failed`，观察任务进入失败，不会卡在进行中。

**阶段 2**
前台包尚未接线；使用假 `llm.Client` 和假 textgen 完成单测，端到端手动验证放到阶段 4。

**阶段 3**
1. 让现有看板任务尝试 `gh pr merge`，确认硬锁拒绝。
2. 验证决策远程方法与审批层拦截；界面回答与一次性解锁在阶段 4 验证。

**阶段 4**
按第四节阶段 4 的统一 dev 手动验证清单，验证前台、CLI 降级、决策卡、硬锁、界面与旧数据迁移。

**阶段 5**
1. 打开 `docs/dev/05-steward-attention.md`，确认没有 `[[静默]]`、账本、话题、唤醒。
2. 打开协议文档与 schema，确认 `steward.decisions.list` 有 request/response，`steward.topics.*` 已删除。
3. 确认旧键与旧表在保留满一个版本后才清理。

**阶段 6**
可选实施后，确认 judge 影子一致率可见、没有越权自动答卡，空闲菜单不重复。

---

## 附：与统一 API 引擎计划的先后依赖与衔接点

1. **先决条件**：统一 API 引擎计划阶段 2 的 `internal/apisource`（`internal/apisource/source.go:25` `type Source struct {`、`internal/apisource/source.go:112` `func Resolve(cfg config.Config, secrets config.SecretStore) []Source {`）必须先落地。本计划阶段 2 开发前台包，阶段 4 接线；阶段 0/1 不受影响。
2. **稳定接口**：前台只依赖 `internal/agent/llm.Client`（`internal/agent/llm/types.go:77` `type Client interface {`）、`llm.Message`（`internal/agent/llm/types.go:11` `type Message struct {`）、`llm.ToolSpec`（`internal/agent/llm/types.go:37` `type ToolSpec struct {`）、`llm.Request`（`internal/agent/llm/types.go:44` `type Request struct {`）、`llm.Response`（`internal/agent/llm/types.go:62` `type Response struct {`）和 `apisource.Source`。不依赖 `internal/providers/openai`（统一计划阶段 6 删除）、不依赖 `internal/providers/mcode` 的完整 Harness（`internal/providers/mcode/mcode.go:121` `func (h *Harness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) (retErr error) {` 带会话持久化、压缩、bash，前台不用）。
3. **客户端构造**：用 `llm.NewOpenAI`（`internal/agent/llm/openai.go:35`）与 `llm.NewAnthropic`（`internal/agent/llm/anthropic.go:26`），Key 用 `Source.APIKey`（`internal/apisource/source.go:79`）。这样统一计划后续把来源目录与请求统一到同一份 `[]Source` 后，前台自动跟随，不需要改代码。
4. **不要建在将被删除的代码上**：不调用 `resolveMcodeClient`（`internal/app/mcode_agent.go:137`）作为长期接口；可以在阶段 2 开发期用一个临时适配器，但必须在统一计划阶段 2 落地后删除，最终只保留 `apisource` 路径。不调用 `mcodeDefaultModel`（`internal/app/mcode_agent.go:100`）作为模型选择器。
5. **发布顺序**：本计划阶段 0 → 阶段 1；统一计划阶段 1/2 发布后 → 本计划阶段 2 → 阶段 3 → 阶段 4 一次切换 → 阶段 5 文档与保留一个版本后的清理 → 阶段 6（可选）。若统一计划阶段 6 先删旧包，本计划阶段 2 必须已经结束，否则前台会编译失败。
