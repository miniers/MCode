# internal/providers/omp

omp CLI（oh-my-pi，可执行文件 `omp`，Pi 的分支）的 harness 适配器。

## 协议要点

- 一轮一个 `omp --mode rpc-ui` 子进程；stdin 写 JSON 行请求，stdout 读 JSON 行帧（LF 分隔）。
- 请求：`get_available_commands`、`get_state`、`prompt`（可带 images）、`abort`、`steer`、`extension_ui_response`。
- 当轮终点是携带同一个 prompt id 的 `prompt_result`（`completed` / `error` / `aborted`）；
  `agent_end` 不是终点，omp 在一次失败的尝试与重试之间也会发它。
- `get_state` 应答里的 `sessionFile` 是恢复 id，下一轮以 `--resume <sessionFile>` 启动，
  经 `EventSession` 上报。
- 运行模式：omp 在每个权限模式下都以 `--approval-mode yolo` 启动（omp 自身的审批不是门禁，
  用户配置能把工具直接放行）；裁决在门禁扩展（`-e`）里，它按启动前写好的运行模式文件
  （环境变量 `MCODE_OMP_GATE`）决定放行、询问还是拦截，询问以 select 标题
  `mcode-gate {…}` 回到适配器走 `turn.Approver`。配置了 `Options.Extension` 时，适配器
  先确认命令表里有 `mcode-gate`，否则不启动这一轮（启动前校验）；没有配
  `Options.Extension` 时除 `fullAccess` 以外的权限模式直接拒绝启动（见 `omp.go:Harness.Run`），
  不带门禁裸跑等于把工具全部放行。
- 计划模式（`turn.InteractionMode == "plan"`）把只读规则追加进 `--append-system-prompt`。
- 额度：`omp usage --json --no-extensions` 的 `reports[].limits[]` 归一化为
  `credits.Window`；断网时的 last-good 报告超过 6 小时即丢弃。

## 实现落点

| 能力 | 落点 |
| :--- | :--- |
| 进程托管与协议解析 | `omp.go:Harness.Run`、`omp.go:buildArgs`、`rpc.go:parseEvent` / `streamState.translate`、`omp.go:Steer` / `ForceStopTurn` |
| 底层 provider 凭证转发与方案额度 | `planlimits.go:PlanLimits`、`omp.go:buildEnv` / `prepareLaunchFiles`（凭证与登录都由 omp 自己持有，适配器只转发环境与启动配置） |
| 运行时模式与交互模式启动注入 | `omp.go:buildArgs`（恒 `--approval-mode yolo` + 计划模式系统提示词）、`omp.go:prepareLaunchFiles` / `gateContent` / `runtimeModeName`（`MCODE_OMP_GATE` 门禁文件）、`gate.go:InstallGate`（内嵌 `mcode-gate.ts`，`internal/app` 启动时写到临时目录并以 `-e` 传入） |
| 工具门禁裁决 | `rpc.go:parseGatePayload` / `answerApproval`（门禁 select 解析与应答；计划模式下非只读调用直接拒绝，supervised 抛给 `turn.Approver`） |

## 尚未实现的能力（逐条）

- **rollback**（`get_branch_messages` + `branch` 的会话切片回退）：harness 没有对应接口。
- **requestTitle**（`/rename` 让 omp 给会话取名，`session_info_update` 回报标题）：没有标题事件。
- **compact**（RPC `compact` 请求）：harness 没有对应接口，自动压缩的
  `auto_compaction_*` 帧也没有对应事件。
- **stopAgent**（`cancel_subagent` 取消单个子代理）：没有该能力。
- **子代理 Head 与 Channel**（`subagent_lifecycle` / `subagent_progress` / `subagent_event` 映射
  为独立 head）：尚未实现，相关帧忽略。
- **常驻进程复用**：本适配器与 pi 适配器一致按轮启停，不复用常驻进程。
- **斜杠命令列表**（`get_available_commands` 的 `commands` 除门禁校验外未上报）。
- **用量限额识别**（`usageLimit` 事件与重置时间）：没有对应事件。
- **窗口的 detail、planName 与窗口 id**（`$x.xx / $y.yy` 一类文案与 notes 拼接，
  以及 `planName` 的 "Codex Pro 20x" 一类文案）：`credits.Window` 只有 `Label` / `UsedPercent` /
  `ResetsAt`，没有承载这三项的字段。
- **context 窗口上报**（`get_state` 的 `model.contextWindow` 与用量面板占比）：`harness.Usage` 没有窗口字段。
- **notice 类帧**（`notice`、`auto_retry_*`、`extension_error`）：不映射为错误事件，避免把可重试的
  notice 当成当轮失败。
- **tool_execution_update 的部分输出**、`command_output`、`session_info_update`、
  `tool_execution_end.result.details`（exitCode、oldText/newText 的 diff 快照）。
- **editDiff 的文件改动前后对照**：只上报改动路径（`EventFileChange`），不含新旧文本。
- **steer**：按 omp 继承自 Pi 的 RPC 协议实现，
  实机行为待联调校准。

## 状态

- 注册（`internal/app` 接线、`Providers.Omp` 配置字段、设置页）已完成；SystemPrompt 尚未接入配置。
- 实机联调（真实 omp、门禁扩展资源、端到端审批）**尚未进行**。
- 本包不 import 其他 provider 包，只复用 `internal/providers/credits` 的额度契约。
