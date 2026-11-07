# internal/providers/commandcode

Command Code CLI（可执行文件 `cmd`）的 `harness.Harness` 适配器，覆盖
会话托管与通信、方案额度与用量。

## 协议要点

- CLI 没有常驻服务协议：每个 turn 启动一个 `cmd -p` 进程，prompt 写到 stdin 后关闭。
- 固定参数：`--output-format json --skip-onboarding --no-auto-update --trust
  --tools-enable todo_write --max-turns 400`；可选 `--model`、`--effort`（受
  `takesEffort` 表约束，表外模型不带 effort）、`--resume <sessionId>`。
- 计划模式加 `--permission-mode plan`，其他模式加 `--yolo`。
- stdout 是按行分隔的 JSON 帧：顶帧 `type` 为 `event`（`run_start`、`message_start`、
  `text_delta`、`thinking_*`、`message_end`、`model_request_end`、`tool_queued`、
  `tool_completed`、`tool_errored`、`tool_hook_blocked`、`run_error`、`interrupted`
  等）或 `result`（`subtype`、`stopReason`、`error`、`sessionId`）。
- 第一个 turn 的 `run_start` 给出 CLI 会话 id，之后用 `--resume` 恢复；Harness 按
  `turn.SessionID` 记住 CLI 报出的新 id（`EventSession` 同时把 id 交给调用方）。
- 退出码：130 为中止（按 interrupt 收尾，不算错误）；3 未登录、4 被拒绝授权、
  5 被限流、6 连不上、7 服务端错误、8 达到往返上限（按完成）、9 无回复、10 额度用尽。
- 审批门禁：headless 不能提问，CLI 以 `--yolo` 放行；除完全访问外的模式都会加载内嵌
  mod（`approval_mod.ts`，`--mod`），mod 把可变更工具调用写成审批目录里的
  `<toolCallId>.request.json`，gate 轮询目录、按模式自动放行或经
  `harness.Approver` / `Interactor` 询问，再把 `<toolCallId>.reply.json` 写回去。
  权限模式映射：Supervised / ReadOnly 一律询问（无审批方时拒绝），Auto-accept edits
  放行写改，Auto 再放行只读 shell 与停 shell 类工具，Full access 不加载 gate。
- 方案额度：`GET /alpha/billing/credits` 的 `windowLimits`（fiveHour、weekly）按
  美元 cap/used 折算成 `credits.Window`；余额由 `internal/providers/credits` 提供，
  这里不重复实现。

## 实现位置

- 会话托管与通信：`commandcode.go`（`Options`、`Harness`、`New`、`buildArgs`、`Run`、
  `ForceStopTurn`、会话 id 映射）、`events.go`（帧解析与 `finishRun` 收尾）、
  `approval.go`（mod 内嵌与 gate）
- 方案额度与用量：`planlimits.go`（`PlanLimits(ctx, opts)`、`parseCommandCodeWindows`、
  可注入的 `PlanLimitsOptions`）

## 尚未实现的功能

- `--list-models` / `cmd status` / `compact` / `title`：模型目录、登录状态检查、
  上下文压缩与标题生成（尚未实现）
- `session_titled`、`compaction_*`、`notice`、`planCompleted`、`usageLimit`、
  `tool_update`（部分输出）、`subagent_progress`、`todos`：harness 没有对应事件，
  帧被忽略（计划模式下最终文本仍以普通文本事件流出）
- `isFinalReport`（`--max-turns 1`）：`harness.Turn` 没有该字段，一律用 `maxTurns = 400`
- 转录存在性检查（`transcriptExists`）：恢复失效会话时由 CLI 自身的错误与退出码兜底
- `/alpha/billing/subscriptions` 的方案名（`planName`）：`credits.Window` 没有对应字段
- 内置 Kanban 工具本体（`MCODE_MCP_BUILTIN` 的服务端）、编辑前后快照（`EditSnapshots`，
  仅在上报路径的 `EventFileChange` 里给 path，没有 before/after patch）
  （MCP 桥接本身已实现：`mcp.go` + `mcp_bridge.ts`，走 `MCODE_MCP_SERVERS`）
- 会话级放行（"Approve for session"）按进程内会话缓存（`Harness.grants`），
  不落盘，重启后失效

## 状态

- 注册、配置字段与设置页已完成；CLI 版本号解析尚未覆盖。
- 实机联调尚未进行（本包不启动真实 `cmd`，测试全部用替身进程、fixture 与
  `httptest`）。
- 版本与路径配置尚未实现。
