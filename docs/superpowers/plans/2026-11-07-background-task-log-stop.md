# 后台任务日志与停止方案

目标：在现有后台任务浮层内展开最近约 20 行日志，按秒刷新并滚到底部；悬停行时提供「停止」，确认停止后播放退出动效。本文仅为实施方案，未修改程序，也未运行验证。

## 现状与证据

- 两类条目来自同一消息列表，另外还有 Swarm 子智能体；本方案只改变任务和命令行，保留子智能体原有导航。`internal/ui/timeline_background_tasks.go:206` `		tasks:    backgroundProviderTasks(messages),`；`internal/ui/timeline_background_tasks.go:207` `		commands: backgroundRunningCommands(messages, threadRunning, runningTurn),`
- provider 任务按 `background` 状态选择，不限定工具类别。`internal/ui/timeline_background_tasks.go:135` `		if isToolBackground(m) {`
- 后台命令必须仍在运行，且当前会话未运行或属于更早回合。`internal/ui/timeline_background_tasks.go:150` `		if !toolIsRunningCommand(m) {`；`internal/ui/timeline_background_tasks.go:153` `		if threadRunning && m.TurnIndex >= runningTurn {`
- UI Message 的 `ID` 是消息 ID，`Text` 为显示内容；目前没有独立 `ToolID`、`TaskID`、`Output` 字段。`internal/ui/client.go:223` `	ID, Role, Kind, Text string`；`internal/ui/client.go:236` `	ToolStatus      string // 执行状态（running、background、completed、failed、blocked）`。wire 消息亦以 Text 承载输出和工具状态。`internal/remote/wire/types.go:416` ``	Text            string  `json:"text"` ``；`internal/remote/wire/types.go:433` ``	ToolStatus *string `json:"toolStatus,omitempty"` ``
- Claude `task_started` 事件传递工具 ID 与任务 ID；结束事件可按任务 ID 回查。`internal/providers/claude/parse.go:429` `					Type:   harness.EventBackgroundTaskStart,`；`internal/providers/claude/parse.go:431` `					TaskID: sys.TaskID,`；`internal/remote/memory_backend.go:178` `	backgroundTaskTools map[string]string`
- Claude tool_result 的文本会解析并作为 `EventToolEnd.Output` 发送，路径如果存在于该文本就随文本保留。`internal/providers/claude/parse.go:704` `			output := parseToolResultContent(blk.Content)`；`internal/providers/claude/parse.go:708` `				Output:  output,`。后端对输出有长度截断，路径并不保证保留，且现有解析没有专门提取输出路径。`internal/remote/memory_backend.go:6867` `								out = string(runes[:toolOutputStreamMaxRunes])`
- 运行中增量只在 provider 实际发出 `EventToolOutput` 时进入消息；Codex 明确尚未接入 outputDelta，不能承诺所有 provider 可实时取日志。`internal/remote/memory_backend.go:6831` `			case harness.EventToolOutput:`；`internal/providers/codex/protocol.go:264` `		// outputDelta 类待接入；turn/diff/updated 是整轮聚合 diff，改动已由 item 级事件覆盖，不发事件。`
- 已有停止接口只在 `runTurnID` 非空时工作，按时间杀进程树而非按消息唯一定位；不能安全用于回合结束后的目标任务，也可能影响并发命令。`internal/remote/memory_backend.go:5451` `	if td.runTurnID == "" {`；`internal/remote/memory_backend.go:5480` `		n, err := procs.KillTreesStartedAfter(root, since, killRunningCommandGrace)`。UI 已有接口可供新接口仿照。`internal/ui/client.go:1040` `	KillRunningCommand(ctx context.Context, threadID, messageID string) (killed int, err error)`
- Claude 常驻进程已有通用控制帧发送和应答解析，但仓库中未发现 `stop_task` 调用。`internal/providers/claude/resident.go:1536` `func (p *residentProc) sendControl(subtype string, fields map[string]any) (string, error) {`；`internal/providers/claude/resident.go:1015` `func parseControlResponse(line []byte) (id string, success bool, reason string, isResp bool) {`。第三方 [CLI 控制协议源码镜像](https://github.com/claude-code-best/claude-code/blob/632f3e19/src/entrypoints/sdk/controlSchemas.ts) 声明 `stop_task`、`task_id`；这是可行线索，并非本仓库或当前安装 CLI 的兼容性证明。必须用目标版本的 CLI 实测确认，旧版拒绝时返回不支持，不能声称停止成功。
- 胶囊和浮层复用现有容器。`internal/ui/composer_capsules.go:125` `		appPopoverAbove(c.Key(capsule.PopoverKey), btn, capsule.PopoverOpen, capsule.PopoverDismiss, capsule.PopoverBody)`；`internal/ui/timeline_background_tasks.go:290` `	return popoverCard(c, ui.Column(c.Key(key)).`
- 现有结束事件会设置终态并使计数失效。`internal/remote/memory_backend.go:7024` `				td.bgCount.invalidate()`；浮层的计数和等待条按消息重新计算，不应另存一份计数。
- [CrychicUI 仓库](https://github.com/ELY-Open-Source/CrychicUI) 的公开搜索未发现可确认的日志尾部视图；搜索到的 LogViewer 属于另一个 Rust GPUI 项目。GitHub 内容 API 此次无法读取，不能证明 CrychicUI 没有该组件。实施前须检查其组件目录与 API；若合适则复用并记录包路径和版本、关闭音效并验证 ReduceMotion，否则采用现有滚动与文本组件。

## 设计选择与边界

1. UI 只传 `threadID`、`messageID`，绝不传任意文件路径或 PID。后端建立消息到工具 ID、任务 ID、输出路径的可信映射；在事件创建时持久化，历史数据缺字段时明确不可用，不以内容猜测 PID。task_started 和 tool_result 可能任意先后或重复，按消息 ID 合并并保持幂等。
2. 在 Claude parser 原始 `tool_result` 未截断前，从 `Output is being written to: ...` 中提取路径并保存在事件/消息的专用字段中。输出路径为空、格式变化、非文件任务时不提供文件日志；不把 `Text` 中的状态说明误当日志。解析路径时约束可信 Claude 任务目录、规范化路径，拒绝目录逃逸、符号链接和非普通文件；远程读取在运行 provider 的服务端进行。
3. 新增 `threads.backgroundTaskLog`，参数 `{threadID, messageID}`，结果 `{text, available}`。服务端仅打开已关联的文件，向末尾 seek，最多读 64 KiB，再取最后约 20 行并处理 UTF-8 边界；文件短时取全部，极长单行仍受字节上限约束，文件不存在、轮转及截断可重试。只在浮层打开且行展开时异步每秒请求，最多一个请求在途；收起、切线程、关闭浮层或退出时取消，过期响应不能覆盖新任务。若来源确实发出增量事件，可从受限的消息输出尾部展示；未接入增量且没有路径时显示空日志，不能声称支持实时输出。避免将通用 `Text` 截断逻辑误当作尾部缓存。
4. 新增 `threads.stopBackgroundTask`，参数 `{threadID, messageID}`，结果 `{stopped}`。Claude 在能关联 task ID 的常驻进程上发送 `stop_task`；以 request ID 路由响应，发送成功不等于停止成功，等待成功应答及结束事件，按实际确认状态处理。CLI 不支持、进程已退出或任务映射不可用时返回明确错误；不使用按文件名盲找 PID 或现有按时间范围杀树。未来若实现退路，必须在启动时记录并验证单任务 PID/进程组身份和存活时间，纯 Go 发信号，防 PID 复用，且不能杀其他任务；目前缺乏证据，暂不启用。回合结束后仍运行的其他 provider 命令，若没有精确的任务控制能力，则不显示可操作停止按钮；不伪装为成功。
5. 正常收到 `EventBackgroundTaskEnd` 时走现有终态、持久化、推送及计数失效路径；如 stop 成功但结束事件不及时，需由后端以同一幂等收尾函数回写终态并发布，随后迟到通知不得重复计数。停止失败保持原状态并展示错误。退出动画使用 UI 中短暂保留的快照，不篡改后端状态；最后一项结束时也要延迟关闭浮层直到退出动画完成，ReduceMotion 下立即关闭。
6. 删除底部「完成后 AI 会自动继续」：该句是次级说明，且并非所有后台来源都适用；实际续跑行为不变。日志展开区限制高度并可滚动，键盘可展开和停止，停止按钮事件不冒泡触发展开。列表既有五行上限可能隐藏目标，实施时改为有高度上限的滚动列表以保证所有条目可操作。

## 分工及依赖

按文件独占分三位执行者；先约定下述字段与方法契约，再并行实施，B 依赖 A 的 provider 接口，C 依赖 B 的 wire 契约。测试由各自执行者负责，集成时再运行全套相关测试。实现中不引入 cgo。

### 1. 执行者 A：provider 与事件（先于 B 集成）

文件：`internal/harness/event.go`、`internal/harness/harness.go`、`internal/providers/claude/parse.go`、`internal/providers/claude/resident.go` 及这些目录对应测试。

- 新增事件字段 `OutputPath string`，以及可选接口 `type BackgroundTaskStopper interface { StopBackgroundTask(ctx context.Context, threadID, taskID string) error }`；根据实际生命周期定义不暴露任意路径的内部能力。扩展 parser，在原始输出中提取路径，给相关 task start 事件携带，保留原始输出；两种事件先后顺序都能关联。
- resident 使用 `sendControl("stop_task", map[string]any{"task_id": taskID})`，新增 `func (h *Harness) StopBackgroundTask(ctx context.Context, threadID, taskID string) error`；在现有 stdout 读取路径按 request ID 分发结果，兼容运行与空闲状态，处理超时、关闭、未知请求及 CLI 拒绝，不另起消费者抢读。先确认空闲进程仍能消费控制应答；若现有生命周期不允许，调整为唯一 reader 的分发器，不能假设空闲状态会自动读取。
- 测试：字符串与块内容路径提取、缺失及非法路径、start/result 两种顺序；模拟 CLI 接受/拒绝/超时、并发设置和停止应答交错、运行及空闲阶段、重复通知；使用目标版本 CLI 的受控集成场景确认 stop_task 真正只结束指定任务。无法取得 CLI 时标记集成验证未完成。

### 2. 执行者 B：消息持久化、远程服务与协议（依赖 A）

文件：`internal/remote/memory_backend.go`、`internal/remote/dispatcher.go`、`internal/remote/capabilities.go`、`internal/remote/wire/types.go`、`internal/app/client.go`、`docs/remote-control.schema.json`、必要的消息存储/传输映射文件及对应测试；如 schema 由生成器维护，同时更新其源并重新生成。

- 在 `wire.ThreadMessage` 和对应存储模型新增可选 `toolID`、`taskID`、`outputPath`，或将路径仅保存在服务端持久模型中；UI 只需消息 ID，不应接收可被调用方操纵的路径。调整存储编码、历史加载、订阅快照、后台任务索引恢复和复制映射；兼容旧消息。持久化 task ID 与可信路径，不能仅依赖当前内存索引。
- 增加 `func (b *MemoryBackend) BackgroundTaskLog(ctx context.Context, threadID, messageID string) (text string, available bool, err error)`，按上面的目录和文件校验及 64 KiB 上限读取。绝不持锁进行磁盘 I/O，读取前后验证消息仍关联同一任务；在历史恢复后也能工作。
- 增加 `func (b *MemoryBackend) StopBackgroundTask(ctx context.Context, threadID, messageID string) (bool, error)`，从消息解析 provider、task ID 并调用 `BackgroundTaskStopper`；成功后的终态更新与 `EventBackgroundTaskEnd` 共用一个幂等函数。不要持锁等待 provider；等待期间和自然完成竞争时核验当前消息状态。
- 在 dispatcher 增加 `threads.backgroundTaskLog`、`threads.stopBackgroundTask` 的严格参数校验、结果及错误映射；wire 定义 `BackgroundTaskLogParams`、`BackgroundTaskLogResult`、`StopBackgroundTaskParams`、`StopBackgroundTaskResult`；同步 capabilities、app client 和 `docs/remote-control.schema.json` 的 JSON-RPC request/result 定义与方法枚举。远程客户端支持能力探测，旧服务端不提供时保持可用并明确不可用。
- 测试：大文件实际仅读尾部、20 行、长行、UTF-8、缺失/轮转/截断、路径穿越和符号链接拒绝；两任务互不干扰、未知消息与不支持 provider、远程 RPC 参数及结果、历史恢复、旧消息、并发停止和自然结束只发布一次，计数及等待条随终态消失。

### 3. 执行者 C：客户端与浮层（依赖 B 契约，可先用 fake 并行）

文件：`internal/ui/client.go`、实际远程 client 适配文件、`internal/ui/timeline_background_tasks.go`、必要的会话视图状态文件、`internal/ui/fake_client_test.go` 和对应 UI 测试。继续使用 `appPopoverAbove` 与 `popoverCard`，不引入 `ui.Popover`。

- `Client` 新增 `BackgroundTaskLog(ctx context.Context, threadID, messageID string) (text string, available bool, err error)` 与 `StopBackgroundTask(ctx context.Context, threadID, messageID string) (bool, error)`；适配本地和远程实现、fake，暴露能力状态。
- 扩展 `renderBackgroundTaskRow` 接收状态及动作，key 使用消息 ID 而非列表序号；行点击切换展开，限制日志高度、等宽字体并更新时滚到底部；按秒异步请求，处理取消、过期及加载错误。仅明确可停止的任务在悬停或键盘焦点时显示方块图标按钮，Tooltip 和无障碍标签为「停止」。保留 bee 的既有交互。
- 展开/收起使用 `DurBase`，退出使用 `DurFast` 或 `DurSnap`；所有动画时长经 `animateDuration(c, d)`，并遵循 ReduceMotion。退出时保留快照直到动画结束，列表变化、切线程、最后一项都可正确清理；停止按钮阻止行点击。
- 删除底部提示及其只供该处使用的常量；列表改为限高可滚动，保证超过五项仍可访问。
- 测试：展开/收起、每秒刷新和滚到底部、单请求在途、切线程和关闭时取消、过期回包忽略；悬停/焦点显示、按钮不触发展开、停止失败留行、成功退出、最后一行与 ReduceMotion、超过五项可访问、bee 行行为保持。由主代理在 dev 版本实测视觉、滚动、动效与远程场景。
