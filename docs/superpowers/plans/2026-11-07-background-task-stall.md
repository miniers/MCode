# Claude 后台任务结束回合后的界面停滞：诊断与修复方案

## 结论与证据

已确认存在回合边界丢帧：`result` 之后的 `task_started` 可能进入无人消费的旧通道，或在无接收者时被直接丢弃；即使计数增加，后台任务开始事件也未必到达后端。这个缺陷可以解释后台任务胶囊和等待条缺失。**尚不能确认它就是用户这次停滞的唯一根因**：没有实际 stdout 帧序列、运行状态和 UI 更新记录；尤其是“发消息后积压正文一起出现”，仅凭丢失 `task_started` 无法证明。以下结论来自静态代码分析，未运行测试。

- 常驻进程有独立的 stdout 读取循环，回合结束并不会等下一次 `Run` 才开始读取。`internal/providers/claude/resident.go:1322` `		raw, err := reader.ReadBytes('\n')`
- 当前回合的消费者看到 `result` 后返回；独立读循环仍可能继续将后续行送入旧 sink。`internal/providers/claude/resident.go:904` `			if parser.sawResult {`
- sink 在返回时才注销，因此读循环可在返回与注销之间向旧通道投递；读取者已经返回后，通道中剩余行不会被解析。`internal/providers/claude/resident.go:741` `	defer p.detach(sink)`
- 当前实现的可挂接自发轮次只由完成通知或 init 发起，不包括 `task_started`。`internal/providers/claude/resident.go:1415` `	candidate := typ == "system" && (subtype == "task_notification" || subtype == "init")`
- `task_started` 即使在无 sink 时被读到，也只增加计数，不能单凭计数生成 UI 事件。`internal/providers/claude/resident.go:1426` `		p.bgTasks++`
- 无 sink 时只有已经处于自发输出状态的行才进入 backlog；独立迟到的 `task_started` 不会建立该状态。`internal/providers/claude/resident.go:1438` `		if p.unsolicited {`
- 在 result 之前收到且有 `tool_use_id` 的 `task_started` 可以通过当前 parser 正常发开始事件；parser 并不保证 CLI 何时发送该帧。`internal/providers/claude/parse.go:421` `			if sys.ToolUseID != "" && (sys.IsBackgrounded == nil || *sys.IsBackgrounded) {`
- 当前还有从工具结果文本识别开始事件的路径，但只匹配特定文案；不能假定所有 Bash 后台结果均命中。`internal/providers/claude/parse.go:16` `	backgroundTaskPattern   = regexp.MustCompile(`moved to the background as task ([A-Za-z0-9]+)(\s|\.|$)`)`
- 完成通知或 init 成功进入自发 backlog 后，由通知回调请求后端开 `Attach` 轮；回调只触发一次，若正处于回合收尾，后端会直接拒绝，当前代码没有在空闲后重新通知。`internal/remote/memory_backend.go:3394` `	if !ok || td.isHelper || td.runTurnID != "" || td.summary.IsRunning || td.pendingSend != nil || td.takingBack {`
- 自发输出已积压时，下一次普通轮会先排空 backlog，因而“下一次发消息才出现”与这个竞态相符，但需要实测证据区分其他原因。`internal/providers/claude/resident.go:277` `	ok := !p.busy && !p.dead && (p.unsolicited || len(p.backlog) > 0 || p.unsolicitedDone)`
- 后端的工具 ID 到消息 ID 映射按每次运行新建；迟到的开始事件即使放到新 `Attach` 轮，也不能自动找到先前普通轮已完成的工具。`internal/remote/memory_backend.go:6514` `			toolMsgIDs            = make(map[string]string)`
- 找不到工具消息时，开始事件会被忽略。`internal/remote/memory_backend.go:6941` `				if toolMsgID == "" || idx < 0 {`
- `Run` 返回后旧回调不再接受事件，因此不能仅保留旧轮次的 `emit` 用于异步投递。`internal/remote/memory_backend.go:7111` `		acceptEvents = false`
- 可发布的消息经 StreamHub 定时 flush，默认间隔 16 ms；该机制本身没有要求下一条用户消息来触发。`internal/remote/stream_hub.go:275` `		st.timer = time.AfterFunc(h.interval, func() {`
- 本地客户端收到投影后调用界面更新回调。`internal/app/client.go:856` `			onUpdate(ThreadUpdateFromWire(ev))`
- UI 合并消息并请求重绘；因此收到有效投影后，并无必须等待用户输入的代码路径。`internal/ui/model.go:1526` `		s.messages = UpsertMessages(s.messages, update.Messages, update.RemovedIDs)`
- UI 更新会调度重绘。`internal/ui/model.go:1590` `	s.invalidate()`
- 胶囊从消息中的 `background` 工具现算；等待条还要求线程和其 Swarm 子线程都不忙。`internal/ui/timeline_background_tasks.go:206` `		tasks:    backgroundProviderTasks(messages),`
- 等待条的判断如下；因此即使后台消息已到，`SwarmBusy` 尚未清除也会隐藏等待条，不能据此单独断言 UI 卡死。`internal/ui/timeline_background_tasks.go:219` `	return !running && !data.empty()`
- 正常收尾在执行 Swarm 钩子后才将线程置为空闲；若停在钩子或其他收尾操作，也会影响最终运行状态，需采集状态才能定论。`internal/remote/memory_backend.go:7459` `			turnHook(threadID, hookProjectID, hookLastAssistantText, hookCompleted)`

## 修复步骤

1. 修改 `internal/providers/claude/resident.go` 的 `residentProc.deliver`、`runOnResident`、`attach` 和 `detach`：在持有同一把进程锁时明确按 stdout 的 `result` 边界切换接收归属，保证 result 后的行不会留在已返回轮次的 sink；对结束前已入队但未消费的行按原顺序转入待处理队列。保留 steer 链式 result 的现有语义，不能仅凭读循环看到第一个 result 就无条件切换。覆盖 sink 已关闭、发送者与 detach 竞争、进程退出、backlog 上限及重复回调等边界。
2. 为回合外独立 `task_started` 增加可跨回合投递的后台任务状态通知：解析时复用 `internal/providers/claude/parse.go` 的字段与过滤规则，携带 thread ID、tool ID、task ID 和描述，不把单个开始帧当作必须等另一个 result 的 `Attach` 轮。用 `task_id` 去重计数，并确保 `is_backgrounded:false`、子代理帧、无效 ID 不错误占用驻留进程。新增的 notifier 契约放在 `internal/harness/event.go` 所属 harness 接口层；实现与生命周期只限现有 Claude 常驻进程。避免直接调用已关闭的旧轮 `emit`。
3. 修改 `internal/remote/memory_backend.go` 的后台任务开始处理，提取能由独立通知和轮内事件共同调用的更新逻辑：在线程级保存工具 ID 到消息 ID 的索引，工具创建时登记，任务开始时回查并更新原工具消息；若开始先于工具创建则按 ID 暂存，创建后消化。需处理工具结果先后到达、任务已结束、重复 start/end、线程删除、进程退出和持久化回载，不能把已结束任务重新标为运行。更新状态、失效后台计数缓存，解锁后落库并调用 `hub.Publish`，不依赖下一轮运行。
4. 修改 `internal/remote/memory_backend.go` 的 `handleUnsolicitedTurn` 与回合收尾衔接：收到自发续跑通知但线程尚在收尾时记录待处理状态，在真正空闲且没有排队发送、接管或新回合竞争时重新尝试；保持每段输出至多一个 attach 消费者。线程被删除、进程退出或 backlog 被普通轮接走时清除待处理状态；不要持锁调用 provider 或启动回合。通知顺序与结果边界须一起处理，避免重复开轮或漏唤醒。
5. 在 `internal/providers/claude/resident_test.go` 按现有假 CLI 方式增加确定性帧序列和同步屏障：分别覆盖 `tool_start → tool_result → result → task_started` 在旧 sink 存在、detach/release 间以及完全空闲时到达；断言开始通知恰好一次、不等下一次用户输入、后台进程不被空闲回收。另覆盖 `result → task_notification → init → assistant → result` 在收尾窗口到达，断言顺序完整、无重复、无额外 user 帧，且 steer 链式轮次不回归。在 `internal/providers/claude/parse_test.go` 覆盖 result 前后帧解析、两个开始来源去重所需字段及子代理和非后台过滤。
6. 在 `internal/remote/tool_output_stream_test.go` 和 `internal/remote/unsolicited_turn_test.go` 增加跨轮开始事件及被忙态拒绝后的重试场景：断言原工具状态从 completed 变为 background、计数和发布消息同步更新；收到结束事件后状态落定、计数归零；自主续跑的文本及时到达且只开一轮。覆盖 task_started 早于工具建立及重复、乱序结束，检查写穿持久化与冷会话回载。
7. 在 `internal/ui/timeline_background_wait_test.go` 及现有 Watch 更新测试中构造运行中到空闲的投影：断言消息变为 background 后胶囊数据非空，`IsRunning=false` 且 `SwarmBusy=false` 时等待条出现，结束后消失；断言 Watch 调用合并消息并请求 invalidate，不依赖新的用户输入。另测 `SwarmBusy=true` 时等待条按现有契约隐藏。
8. 主代理执行针对上述包的测试，并用 Claude 常驻模式在 Swarm 主会话复现。记录带时间戳的 stdout 帧类型及 task/tool ID、`Run` 返回和 attach 通知、线程 `IsRunning`/`SwarmBusy`/后台计数、projection 与 invalidate；避免记录命令正文和敏感信息。对照 `result`、任务启动、通知及用户再次发消息的先后顺序，确认是否还有与本次丢帧不同的阻塞或重绘问题。

## 未验证事项

没有运行构建、测试、lint 或 UI 实测，也没有取得用户此次 CLI 原始帧。实际 `task_started` 是否在 `result` 后、工具结果的文案、是否存在自发续跑被拒绝、是否有 Swarm 钩子或 UI 调度阻塞，均需按步骤 8 验证。以上修复覆盖代码中可确定的丢帧和漏唤醒路径；不能据此保证已解释用户所见的全部积压现象。
