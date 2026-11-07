# Codex Provider (codex app-server)

本包实现了基于 OpenAI Codex CLI `codex app-server` 的 `harness.Harness` 适配器。

## 1. 协议版本

- 基于本机安装的 **`codex-cli 0.165.0`**。
- 协议定义以 `codex app-server generate-json-schema --experimental` 导出的 JSON-RPC 2.0 schema 为准。
- 基线方法名快照见 `testdata/app_server_methods_0165.json`，由 `schema_contract_test.go` 的契约测试校验（见第 8 节）。

## 2. 导出符号与方法签名

```go
type Options struct {
    Binary    string
    ExtraArgs []string
}

func New(opts Options) *Harness
func (h *Harness) Kind() harness.Kind // 返回 "codex"
func (h *Harness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error
```

此外，为方便单测及协议独立验证，本包导出了纯数据流解析器：
```go
type ProtocolHandler struct { ... }
func NewProtocolHandler(turn harness.Turn) *ProtocolHandler
func (h *ProtocolHandler) HandleLine(ctx context.Context, line []byte, emit func(harness.Event)) (*JSONRPCMessage, error)
func (h *ProtocolHandler) HandleNotification(msg *JSONRPCMessage, emit func(harness.Event))
func (h *ProtocolHandler) HandleServerRequest(ctx context.Context, msg *JSONRPCMessage) (*JSONRPCMessage, error)
func (h *ProtocolHandler) IsTurnDone() bool
```

## 3. 使用的 app-server 方法与通知清单

### Client -> Server 请求 (Requests)
- `initialize`：握手协商客户端信息（`clientInfo`）与能力（`experimentalApi: true`）。
- `thread/start`：当 `turn.SessionID == ""` 时新建线程。
- `thread/resume`：当 `turn.SessionID != ""` 时恢复已有线程。
- `turn/start`：发送用户 Prompt，开启新一轮交互。
- `turn/interrupt`：在 `ctx` 取消时发送中断请求（入参为 `threadId` 与 `turnId`）。
- `thread/compact/start`：Prompt 恰为 `/compact` 且会话已有可恢复 thread 时改发此请求做原生压缩。
- `review/start`：Prompt 以 `/review` 开头时改发此请求做原生审阅。

### Client -> Server 通知 (Notifications)
- `initialized`：在 `initialize` 请求应答后立即发送，完成 LSP 风格握手。

### Server -> Client 通知 (Notifications)
- `item/agentMessage/delta`：助手正文流式增量 -> `EventText`。
- `item/reasoning/textDelta`、`item/reasoning/summaryTextDelta`：思考推理流式增量 -> `EventReasoning`。
- `item/started`：条目开始（`commandExecution`, `fileChange`, `mcpToolCall`, `dynamicToolCall` 等）-> `EventToolStart`。
- `item/plan/delta`：计划正文流式增量 -> `EventText`。
- `item/completed`：条目完成 -> `EventToolEnd`；对于 `fileChange` 额外派发 `EventFileChange`；对于 `agentMessage` 执行前缀对比去重补齐。
- `item/fileChange/patchUpdated`：文件变更增量 -> `EventFileChange`（按 item 与路径去重）。
- `thread/tokenUsage/updated`：Token 消耗更新 -> `EventUsage`。
- `turn/completed`：回合正常或失败结束 -> `EventDone`（若带错误额外派发 `EventError`）。
- `turn/failed`：回合失败 -> `EventError` + `EventDone`。
- `error`：服务端报错 -> `EventError`。

### Server -> Client 请求与审批 (Server Requests)
- `item/commandExecution/requestApproval`：命令执行审批，回复 `{"decision": "accept"|"decline"}`。
- `item/fileChange/requestApproval`：文件写入审批，回复 `{"decision": "accept"|"decline"}`。
- `applyPatchApproval`：补丁审批，回复 `{"decision": "accept"|"decline"}`。
- `execCommandApproval`：命令执行审批，回复 `{"decision": "accept"|"decline"}`。
- `item/permissions/requestApproval`：权限提权请求，回复空权限拒绝 `{"permissions": {}, "scope": "turn"}`。
- `item/tool/requestUserInput`：补充信息提问，转交 `harness.Interactor`，按 `answers` 回写。
- `mcpServer/elicitation/request`：MCP elicitation 交互，一律回 `{"action": "decline"}`。
- 未知请求：统一回写 JSON-RPC `-32601` 错误，避免 CLI 阻塞挂死。

## 4. 权限模式映射

| mcode `harness.PermissionMode` | Codex `sandbox` | Codex `approvalPolicy` | 审批行为（无 Approver 时） |
| :--- | :--- | :--- | :--- |
| `ModeReadOnly` (`"readonly"`) | `"read-only"` | `"on-request"` | 自动拒绝 (`"decline"`) |
| `ModeSupervised` (`"supervised"`) | `"read-only"` | `"untrusted"` | 自动拒绝 (`"decline"`) |
| `ModeAutoAcceptEdits` (`"autoAcceptEdits"`) / `ModeAgent` (`"agent"`) / 空 | `"workspace-write"` | `"on-request"` | 自动拒绝 (`"decline"`) |
| `ModeAuto` (`"auto"`) | `"workspace-write"` | `"on-request"` | 自动拒绝 (`"decline"`) |
| `ModeFullAccess` (`"fullAccess"`) | `"danger-full-access"` | `"never"` | 自动同意 (`"accept"`) |
| 未知模式 | `"read-only"` | `"on-request"` | 自动拒绝 (`"decline"`) |

## 5. 与 mygo-agent 的协议差异

1. **生命周期中断控制**：mygo-agent 未调用 `turn/interrupt`，在取消时直接关闭标准输入或强杀进程组；当前适配器在 `ctx` 取消时优先调用 schema 中的 `turn/interrupt` RPC，给进程 2 秒优雅中止时间后再收尾。
2. **初始化握手**：按照 0.162.1 schema，在 `initialize` 返回后补发 `initialized` 通知，补全了标准 JSON-RPC 初始化流程。
3. **审批协议丰富度**：支持新版扩展的 `applyPatchApproval` 与 `execCommandApproval`；且在 Approver 等待时采用独立 goroutine 回复，写 stdin 加锁隔离，保证读循环不被卡死。
4. **Token 用量规范**：mygo-agent 仅将 tokenUsage 归纳为单条 Note 文本；当前适配器规范提取 `total.inputTokens`、`total.outputTokens`、`total.cachedInputTokens`，按 mcode 规范生成标准的 `harness.EventUsage` 事件。
5. **强类型事件与校验**：所有派发事件严格走 `harness.Emit`，经 `Event.Validate()` 校验。
6. **多工具类型适配**：不仅处理 `commandExecution` 和 `fileChange`，还全面支持了 `mcpToolCall`、`dynamicToolCall` 等新 item 类型的生命周期事件映射。

## 6. 未确认的假设

1. **`grantRoot` 稳定性**：Codex schema 中标注 `grantRoot` 为 `[UNSTABLE]`，在 `fileChange` 审批中保留了该字段读取与 Summary 呈现，但其持久生效策略取决于 Codex 底层。
2. **`TurnInterruptResponse`**：schema 中 `TurnInterruptResponse` 为 `{}`，假设 2 秒内即使未收到响应也按常规超时处理，不阻塞子进程清理。

## 7. 当前实现概览

- **追加指令 `turn/steer`**：实现 `harness.Steerer`。先在本 Harness 的常驻进程里找到正持有该 `(threadId, turnId)` 的忙碌进程，再以 `ExpectedTurnID` 发起 `turn/steer`，把追加文本送进当轮；找不到在跑的轮次时返回 `harness.ErrNotRunning`，避免把文本误投到下一轮。
- **模型目录 `model/list`**：`FetchModels` 启动一次 app-server，握手后分页调用 `model/list`（最多 10 页，整体超时 15 秒，结束回收子进程），解析模型 id、展示名、`supportedReasoningEfforts`、`defaultReasoningEffort` 与 `serviceTiers`，结果经 `modelcache` 缓存。
- **推理强度（reasoning effort）**：`turn.Effort` 非空时随 `turn/start` 以 `effort` 字段下发；可选值来自模型目录，本包不做硬编码白名单。
- **快速档位（fast tier）**：`turn.FastMode` 为真时以 `serviceTierForTurn` 下发档位 ID。优先用调用方给的 `turn.SpeedTier`，缺省时从模型目录的 `serviceTiers` 里挑 `id`/`name` 含 `fast` 或 `priority` 的档位（`Model.FastTierID`）；模型无快速档位或目录未知时不传该字段，交由服务端默认。
- **额度双来源**：`resolveQuotaRoute` 用 `config/read` 得到的有效配置判定来源。真正直连 OpenAI 的 ChatGPT 登录走 `account/rateLimits/read` 读官方额度；自定义网关只有在网关自身响应确认是 Magpie 时才读该网关的账号卡片接口（`/v1/magpie/quotas/cards`）；其余网关、认证方式或读不到的情况一律报错，不猜测。额度结果按 60 秒 TTL 做内存缓存，单次读取（含启动 app-server）整体超时 15 秒。
- **常驻进程池**：按 `(workDir, sandbox)` 复用 `codex app-server` 子进程，同一组合的空闲进程上限为 2，忙碌进程独占，因此切换项目或同项目并发会话都互不阻塞；线程按 `(threadId, permission mode)` 缓存，命中时直接 `turn/start`，未命中走 `thread/resume` 并带 `excludeTurns: true` 以免回传全量历史。进程死亡或 `turn/interrupt` 超时会杀掉进程组并重建。
- **`CODEX_HOME` 与共享布局**：`Options.CodexHome` 非空时以该环境变量启动 app-server，用于多账号切换；`EnsureSharedLayout` 把主机 `~/.codex` 下的 `config.toml`、`AGENTS.md`、`prompts`、`skills`、`rules`、`sessions`、`archived_sessions` 软链接进账号目录（目标已存在则跳过、绝不覆盖）。`auth.json` 等私有凭据文件不在共享清单内。
- **图片输入**：`turn.Images` 里的本地图片路径逐个转成 `turn/start` 的 `input` 项 `{"type": "localImage", "path": ...}`，排在文本项之后；空白路径直接丢弃。Codex 只接受本地路径，图片不走 base64。
- **plan 模式**：`turn.InteractionMode` 为 `plan` 且 `turn.Model` 非空时，`turn/start` 额外下发 `collaborationMode{ mode: "plan", settings: { model, reasoning_effort } }`。schema 要求 `settings.model` 必填，因此 `turn.Model` 为空时不下发该参数、退化为普通回合。`settings.developer_instructions` 一律留空（语义为使用该模式的内置指令），plan 的约束文本由上层写进 Prompt，这里不重复下发。
- **原生 `/compact` 与 `/review`**：`planSlashRequest` 只认 trim 后整段等于 `/compact`，或以 `/review` 开头且其后紧跟空白（避免把 `/reviewx` 当命令）的 Prompt，且要求会话已有可恢复 thread（`turn.SessionID` 非空或本进程已缓存该线程）；否则按普通文本走 `turn/start`。命中 `/compact` 改发 `thread/compact/start`；命中 `/review` 改发 `review/start`，无附加文字时审阅工作区未提交改动（`target.type = "uncommittedChanges"`），带文字时作为 `custom.instructions` 下发，`delivery` 固定 `inline`。`/compact` 由 memory backend 原样透传，用户人设等装饰不会污染这段文本。compact 的响应是空对象，`turnId` 只能从随后的 `turn/started` 通知补齐，因此拿不到 `turnId` 时中断退化为进程级强停。
- **启动失败有限重试**：`thread/start`、`thread/resume`、`turn/start` 以及原生 compact/review 失败，或常驻进程在本轮尚未向上发出内容事件就退出时，丢弃进程重建并重试；最多 3 次尝试，重试前退避 1s、2s（首次尝试不退避）。本轮一旦已发出内容事件（仅 `EventSession` 除外）或 `ctx` 已取消，立即返回错误不再重试，避免重复输出。
- **进程回收**：`EvictThread` 关闭并移出缓存了该会话的空闲常驻进程，codex 的 `threadId` 与上层 mcode 会话 ID 两个键都认，忙碌进程不动，供会话或项目删除时立即释放内存；`turn.Transient` 的一次性会话（如 Swarm bee）本轮结束即关闭进程，不放回空闲池。
- **服务端版本记录**：`initialize` 响应里的 `userAgent`（以及 `platformOs`、`platformFamily`、`codexHome`）记到 `ServerVersion`，首次或变化时打一条日志，仅用于观测，不参与能力门控。
- **通知侧语义**：
  - 用量口径：`EventUsage` 的 `InputTokens`/`OutputTokens`/`CachedTokens` 报本轮累计，由 `total` 减去「轮前基线」得出（基线取本轮首次上报的 `total - last`），重复收到同一份 `total` 不会重复计数；不带 `total` 的旧报文退化为逐次累加 `last`。`outputTokens` 已含推理输出。`ContextTokens` 取 `last` 的输入加输出，`ContextWindow` 取 `modelContextWindow`，属单次请求口径，与累计计费量无关。
  - 失败/中断收尾：`turn/completed` 状态为 `failed`/`interrupted`、`turn/failed`，以及非重试的 `error` 通知，都会为已发 `EventToolStart` 但没等到 `item/completed` 的工具补发一条 `IsError` 的 `EventToolEnd`；补发后即从待完成集合移除，不会重复补发。
  - 未知通知：按方法名只记一次日志，避免刷屏。
  - 展示类 item：`plan` 按文本输出（已流过 `item/plan/delta` 时只补尾部）、`exitedReviewMode` 输出审阅正文、`contextCompaction` 输出「上下文已压缩」、`enteredReviewMode` 只标记状态不发事件；四者都不发 `EventToolStart`。
  - 文件变更：`item/fileChange/patchUpdated` 增量与 `item/completed` 的 `fileChange` 都转成 `EventFileChange`，按 `(itemId, path)` 记住最近一次已发出的 patch，内容相同就不再重复发。
  - 进行中：`item/commandExecution/outputDelta` 尚未转成 `EventToolOutput`，目前按「已知但有意忽略」处理。
- **Server Request 侧交互**：
  - `item/tool/requestUserInput`：转成 `harness.RequestKindQuestion` 的提问请求交给 `harness.Interactor`（即 `turn.Approver` 实现了 `Interact` 时），按 `ToolRequestUserInputResponse.answers` 回写；无 Interactor、用户跳过或交互出错时回空 `answers`（schema 要求该字段必填）。schema 没有多选字段，`AllowsMultiple` 固定为 false。
  - `mcpServer/elicitation/request`：一律回 `{"action": "decline"}`，让 codex 继续本轮而不是报请求失败。
  - 无活跃轮次（无 Approver）时按请求方法返回形状正确的拒绝应答：`applyPatchApproval`/`execCommandApproval` 的 `decision` 是 `ReviewDecision`，拒绝写成 `{"denied": {"rejection": ...}}`；`item/permissions/requestApproval` 用空权限档 `{"permissions": {}, "scope": "turn"}`；命令与文件变更审批用 `{"decision": "decline"}`。

### 已知限制

- **多账号 `CodexHome` 未接到运行路径**：`Options.CodexHome` 与共享布局已就绪，但 `internal/app` 构造 Harness 时没有传该字段，实际仍用进程环境里的默认 `~/.codex`；额度读取走的是另一条 `QuotaOptions.CodexHome`。
- **未做 `trust_level` 自动信任**：不写 `config.toml` 的信任项，首次进入新目录仍可能被 Codex 询问。
- **审批只有允许/拒绝两档**：`harness.Decision` 没有「本会话允许」，应答里的 `acceptForSession` 未使用，同一请求每次都要重新表态。
- **未实现 `thread/revert`（undo）**：回退历史只能靠上层重建会话。
- **重试不回写新 `threadId`**：重试时若新建了 thread，新 id 不会回传上层，失败那次的线程会在 codex 侧成为孤儿。
- **`patchUpdated` 的 `Patch` 不带 `diff --git` 头**：`EventFileChange.Patch` 取 item 里的 unified diff 原文；补头只发生在审批请求一侧（`item/fileChange/requestApproval` 与 `applyPatchApproval`）。

## 8. 协议基线更新方法

基线漂移（方法改名、增删）必须靠快照加契约测试发现，不能只靠人读 schema。

**1）导出 schema**

```bash
codex --version    # 记录实际版本，例如 codex-cli 0.165.0
codex app-server generate-json-schema --experimental --out /tmp/codex-schema-0165
```

输出目录必须放在仓库之外，也不要在这个目录里跑解释器或构建工具，避免生成物被误提交。

**2）提取方法名**

只需三个文件：`ClientRequest.json`（我们发出的请求）、`ServerRequest.json`（服务端发来的审批类请求）、`ServerNotification.json`（服务端通知）。三者都是 `oneOf` 数组，每一项的 `properties.method` 给出该方法名（形式为 `enum: ["方法名"]`，个别为 `const`）；遇到 `$ref` 时到同文件的 `definitions` 里取实体再读 `properties.method`。

提取结果去重、按字典序排序后写入 `testdata/app_server_methods_<版本>.json`，结构固定为：

```json
{
  "version": "0.165.0",
  "clientRequests": ["..."],
  "serverRequests": ["..."],
  "serverNotifications": ["..."]
}
```

**只提交方法名清单，不要把整份 schema 拷进仓库**（单份 `ClientRequest.json` 就有数百 KB），也不要手写或删改方法名。

**3）改名到新版本**

新快照按新版本命名（如 `app_server_methods_0166.json`），并同步三处：删除旧快照文件、更新本 README 第 1 节的基线版本、更新 `schema_contract_test.go` 里指向快照文件名的常量。

**4）契约测试的作用**

- 断言本包**发送的请求**、**处理的通知**、**应答的服务端请求**三类方法都还在快照里；任何缺失会直接 `Fatal`，并写明是哪一类、哪个方法，把升级 `codex-cli` 后的改名/移除变成一次可定位的测试失败，而不是线上静默丢事件。
- `turn/failed` 属于历史遗留通知：0.165 已不再出现在清单里，测试只输出带说明的日志，不失败，用于提示保留的兼容分支可以择机清理。
- 快照本身也做结构校验（非空、已排序、无重复、无空方法名），防止人工更新时漏排或漏项。

