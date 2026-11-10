# 子代理 todo_write 的显示断点与最小修复方案

日期：2026-11-10  
工作副本基线：`3f78f06`  
状态：**静态定位完成；测试在包初始化前被缺失的嵌入资源阻塞。未修改任何 Go 文件。**

## 1. 根因

这是两个独立的 UI 断点，不是 `todo_write` 需要额外注册。

### 1.1 待办卡片：工具组的节拍绕过专用渲染分支

internal/ui/timeline_rows.go:4097 `	if items, ok := todoItemsFromMessage(m); ok {`

单条工具消息在这里识别待办，并复用 `renderTodoCard`。但是，同一回合中相邻的思考与工具会聚合成工具组，展开后生成节拍。节拍逐项直接渲染普通工具胶囊，没有调用上述分支。

internal/ui/timeline_rows.go:5985 `									renderToolChip(c, s, toolMsg, beatTools)`

internal/ui/timeline_rows.go:5988 `								renderToolDetail(c, s, toolMsg)`

所以即使消息角色正确、输入 JSON 完整、待办解析成功，组内仍显示 `todo_write` 胶囊。点击胶囊会出现通用工具详情，符合截图的结构。这个断点也影响主会话中的组内待办，不是蜂独有；单条待办恰好走了正确路径。

### 1.2 输入框进度胶囊：嵌入式子会话被主动排除

实际输入框走的是 `composerCapsules → composerPlanCapsule`，不是旧的 `renderComposerPlanPill`。

internal/ui/composer.go:738 `			capsules := composerCapsules(c, s, currentThreadID, pillKind, continueState.lastMessageID, isRunning, isEditingQueued, hasContent)`

internal/ui/composer_capsules.go:183 `	if s == nil || s.embeddedBee {`

蜂浮窗和蜂独立窗口使用的轻量状态都设置 `embeddedBee=true`，因此待办进度在读取消息之前就被禁止。

internal/ui/swarm_embedded.go:83 `	child.embeddedBee = true`

这不是「只读取主会话消息」：解除此判断后，现有计算会读取该子会话自己的 `s.messages`。

internal/ui/composer_capsules.go:189 `	view := buildPlanPillView(s.messages, running)`

旧函数也有相同判断，但当前生产代码没有调用它；检索只找到其定义和 `motion_busy_test.go` 中的调用。只改旧函数不会修复实际输入框。

internal/ui/composer_plan_pill.go:278 `	if s == nil || s.embeddedBee {`

## 2. 从工具产生到显示的路径

以下字段是依据当前代码重建的值，不是从用户实际会话导出的运行时抓包。尚未验证用户截图对应消息是否另有截断或历史导入问题。

### 2.1 自定义 API 来源与内置工具

API Key 来源通过 `newSourceEngineHarness` 配置 provider kind，最终构造内置引擎 harness；不需要为 deepseek、gpt 单独实现待办 UI。

internal/app/engine.go:112 `	return providermcode.New(opts)`

harness 将内置工具加入本轮工具集。

internal/providers/mcode/mcode.go:176 `	toolset = append(toolset, agent.BuiltinTools(turn.Approver)...)`

`todo_write` 接收 `todos` 数组，执行结果只是完成数摘要。待办原始数据来自工具输入，不是结果文本。

internal/agent/builtin_tools.go:54 `			Name:        "todo_write",`

internal/agent/builtin_tools.go:77 `				return tools.Result{Text: fmt.Sprintf("Todos updated: %d/%d completed", completed, len(args.Todos))}, nil`

agent 发出 `EventToolStart`，携带原始工具名和参数；结束时发出 `EventToolEnd`，携带文本结果。并行工具分支使用同样的事件字段。

internal/agent/agent.go:461 `				emit(harness.Event{Type: harness.EventToolStart, ToolID: call.ID, ToolName: call.Name, Input: call.Arguments})`

internal/agent/agent.go:482 `				emit(harness.Event{Type: harness.EventToolEnd, ToolID: call.ID, ToolName: call.Name, Output: result.Text, IsError: result.IsError})`

### 2.2 后端消息与传输

后端在 `EventToolStart` 创建工具消息，保存工具 ID 到消息 ID 的映射。开始事件中：

| 字段 | 当前构造值 |
| --- | --- |
| `Role` | `tool` |
| `Kind` | `tool` |
| `ToolName` | `todo_write` |
| `ToolInput` | 原始 `e.Input` 字符串；超过 16000 个 rune 截断 |
| `Text` | `todo_write` + 空格 + 原始输入的前 200 个 rune |
| `ToolStatus` | `running` |

internal/remote/memory_backend.go:7085 `				kind := "tool"`

internal/remote/memory_backend.go:7088 `					Role:       "tool",`

internal/remote/memory_backend.go:7095 `					ToolInput:  toolInput,`

结束事件通过同一工具 ID 找到原消息。保留角色、类型、工具名和输入；更新正文为输入摘要与结果，更新完成状态和结束时间。正常结束后：`ToolStatus=completed`，`Text` 包含 `Todos updated: 2/3 completed`。

internal/remote/memory_backend.go:7159 `								td.messages[idx].Text = stream.override(out)`

后端按本轮 `threadID` 发布消息，蜂的消息属于蜂线程，不是写入 lead 消息列表。

internal/remote/memory_backend.go:7388 `				b.hub.Publish(threadID, msgToPublish, nil, currentSummary)`

历史加载和实时更新都使用共享 wire→UI 转换。实时事件的转换入口是 `ThreadEventToUI` 所在的 `convert.go`；首次订阅快照同样使用 `MessagesWireToUI`。转换保留工具输入。

internal/app/convert.go:203 `	toolInput := derefString(m.ToolInput)`

internal/app/convert.go:230 `		Role:            m.Role,`

internal/app/convert.go:231 `		Kind:            uiKind(m.Role, kind),`

internal/app/convert.go:232 `		Text:            m.Text,`

internal/app/convert.go:241 `		ToolName:        toolName,`

internal/app/convert.go:242 `		ToolInput:       toolInput,`

internal/app/client.go:911 `				Messages: MessagesWireToUI(snap.History.Messages),`

### 2.3 蜂视图与时间线

`sidebar_bees.go` 管理侧栏蜂列表，不是工具详情专用渲染器。蜂面板取得自己的 `chatState`，首次同步选择蜂线程，随后加载与订阅该线程。

internal/ui/swarm_embedded.go:163 `	child.selectThread(bee.ThreadID)`

蜂时间线走共享的增量构建器和共享行渲染器。

internal/ui/swarm_bee_floats.go:1389 `		items, fps := s.rebuildTimelineIncremental(selectedID, messages, expanded, liveToolGroupID(messages, bee.Status == "running"))`

internal/ui/swarm_bee_floats.go:1464 `			renderTimelineItem(c, s, item, liveID, lastLiveID, isLastAssistant, hasUsage, turnUsage, timelineItemTopGap(items, i, 12))`

共享构建先折叠同轮旧待办，但不会将待办排除在工具组之外。

internal/ui/timeline_rows.go:918 `	return buildTimelineItemsFiltered(collapseTodoMessages(messages), exp, liveGroupID, groupsOpenByDefault)`

internal/ui/timeline_rows.go:945 `			if len(run) >= 2 && hasTool {`

展开工具组构造 `timelineItemToolBeat`，`renderTimelineItem` 将其分派给节拍渲染函数。

internal/ui/timeline_rows.go:1878 `					renderToolBeatItem(c, s, item, liveID)`

完成回合的工作过程折叠区也重建/复用这些节拍；因此修节拍入口可同时覆盖蜂、主会话和完成回合展开体，不需要蜂专用组件。

### 2.4 排除的方向与边界

- 角色没有在正常路径丢失：后端明确生成 `Role=tool`、`Kind=tool`。
- 解析器不依赖工具名是否叫 `TodoWrite`：它查找输入中第一个 `{`，解码 `todos` 或 `plan`，`content/status` 正好符合格式。
- 工具结果追加在 JSON 后不是问题：解码器只解码首个对象。
- 缓存不是一个只按消息 ID 永久命中否定结果的缓存；条目匹配包含 `Text` 和 `ToolInput`。

internal/ui/tool_parse_cache.go:65 `		e.Text == m.Text &&`

internal/ui/tool_parse_cache.go:67 `		e.ToolInput == m.ToolInput &&`

- 超过 16000 rune 的输入仍可能被截断，正文的 200 rune 摘要通常不能补救。这是独立的数据上限问题；本次不修改存储协议或截断策略。
- 不把蜂待办汇总进 lead 胶囊。那会改变会话归属，超出本次目标。

## 3. 分步修复方案（供实施代理直接执行）

### 第一步：组内待办复用现有卡片

**文件与锚点：**

internal/ui/timeline_rows.go:5980 `						for _, toolMsg := range beatTools {`

**具体改法：**在循环最前面调用 `todoItemsFromMessage(toolMsg)`。识别成功时直接调用 `renderTodoCard(c, s, toolMsg, items)`，随后 `continue`。识别失败时保留现有工具行、普通详情和 `opened` 控制逻辑原样。不要把卡片放进 `AlignItems(ui.Center)` 的普通工具 Row；应直接放在现有节拍 Column 中，保留卡片的 `FillWidth`。

待办卡片使用已有 `todo-<ID>` 开合状态，不消耗普通工具详情的 `opened` 名额。无需新增组件、工具名白名单或第二套解析器。

**新增测试：**在 `internal/ui/timeline_rows_test.go` 新增 `TestTimelineRows_GroupedTodoCard`，参考既有 `TestTimelineRows_ToolBeatChips` 和 `TestTimelineRows_TodoCard`。

internal/ui/timeline_rows_test.go:451 `func TestTimelineRows_ToolBeatChips(t *testing.T) {`

internal/ui/timeline_rows_test.go:1703 `func TestTimelineRows_TodoCard(t *testing.T) {`

测试消息固定为：同一回合的 user、reasoning、read、todo_write、bash。待办输入含三项，其中两项完成、一项进行中；`Text` 带结果尾行。用真实 `buildTimelineItems` 展开组，随后通过 `renderTimelineItem` 渲染，不只直接调用 `renderTodoCard`。分别设置 `embeddedBee=false/true`。

**验收：**两种状态都出现“待办清单”和 `2/3`；点击卡片能列出三项；该待办不再显示普通 `todo_write` 胶囊/JSON 详情。read、bash 仍是原胶囊，展开一个普通工具后，操作待办卡片不改变它的详情状态。运行中最新卡片默认展开，非运行中默认收起。完成回合折叠区重新展开后，仍出现同一卡片且没有重复 Key。

**回归测试：**继续运行既有 `TestTimelineRows_ToolBeatChips`、`TestTimelineRows_TodoCard`、`TestBuildTimelineItemsCollapsesTodo` 及 `TestTimelineRows_TurnWorkFoldUnfoldWithExpandedToolGroup`。不调整组构建规则或折叠策略。

### 第二步：开放蜂自身的待办进度胶囊

**文件与锚点：**

internal/ui/composer_capsules.go:183 `	if s == nil || s.embeddedBee {`

**具体改法：**改为仅检查 `s == nil`。删除紧邻的“子代理输入框空间太小，不显示待办胶囊”注释。保留后面的 `buildPlanPillView(s.messages, running)`，该函数已经调用 `latestTodoPlan`。保留现有胶囊、弹层与点击处理，不读 parent 消息，不改变后台任务/继续/运行中改动胶囊的蜂限制。

不要仅修改 `internal/ui/composer_plan_pill.go` 的旧入口；它不是当前输入框调用链。最小方案保留此旧函数不动，避免扩大到旧测试组件清理。

**新增测试：**在 `internal/ui/composer_capsules_test.go` 新增 `TestComposerPlanCapsuleEmbeddedBee`。参考现有胶囊行测试构造子会话，设置 `embeddedBee=true`、选中蜂线程和该线程自己的待办。先直接断言 `composerPlanCapsule(nil, child, beeID)` 返回 `ok=true` 与 `Label=待办进度 2/3`，再通过实际 `renderComposer` 路径验证输入框上方可见及点击展开清单。

internal/ui/composer_capsules_test.go:100 `func TestComposerCapsulesShareOneRow(t *testing.T) {`

**验收：**蜂浮窗与蜂独立窗口都可见自身胶囊；子会话内容与父会话不同，确保胶囊显示的是子会话的项。三项全部完成时消失并关闭浮层；切换蜂不串清单；无待办仍不占行。宽度至少覆盖 360pt 与 640pt，长项截断、不挤出发送按钮。复用原 `appPopover` 弹层，不新增说明文字。

**回归测试：**既有 `TestComposerCapsulesShareOneRow`、`TestComposerCapsuleRowHiddenWhenEmpty`、`TestComposerBackgroundTasksCapsule`、`TestPlanPill_AllCompletedHidden`、`TestPlanPill_StaleAfterUserMessage` 保持通过。后两项验证完成与跨轮显示规则不变。

### 第三步：补输入/缓存与减少动效的回归，不改生产解析逻辑

**文件与锚点：**

internal/ui/tool_parse_cache_test.go:373 `func TestTodoItemsFromMessageInvalidatesOnFieldChange(t *testing.T) {`

**具体改法：**补一个 `todo_write` 实际形状用例：同一 ID 先只收到 `todo_write`、无输入，缓存否定；随后补齐 `ToolInput` 与 `Text`，必须变为三项成功结果；再合并 `Todos updated: 2/3 completed`，解析结果保持一致。另测 `ToolInput` 为空、`Text` 含完整 JSON 加结果尾行的兼容路径。只补测试，不改缓存键或放宽 JSON 解析。

**验收：**`todoItemsFromMessage` 返回三项，`latestTodoPlan` 选择正确消息，`buildPlanPillView` 返回 `Done=2`、`Total=3` 和进行中项。无效 JSON 仍回退普通工具，不能 panic。

**动效测试文件与锚点：**

internal/ui/motion_busy_test.go:10 `func TestMotionBusyLoops(t *testing.T) {`

给真实的 `composerPlanCapsule → renderComposerCapsule` 链路补嵌入蜂用例，避免只测旧 `renderComposerPlanPill`。给第一步的组内待办用例启用 `ReduceMotion`，确认展开/状态更新可完成。**验收：**减少动效时无持续帧请求；不新建动效实现。

## 4. 方案取舍

**推荐：**仅修改 `renderToolBeatItem` 的待办分支与 `composerPlanCapsule` 的蜂限制，生产修改集中在两个现有文件。共享入口修一次，各会话受益。

**不推荐把待办从工具组构建中拆出去：**这会涉及活动组分段、增量切点、消息下标、回合折叠和默认展开策略。对于“组内渲染错误”改动过大。

**不推荐修改 provider 或新增蜂专用组件：**当前事件已带齐角色和输入，单条渲染与待办进度计算也已有实现。新组件会复制现有逻辑，不能解决共享节拍入口遗漏。

**不推荐扩大解析兼容或盲目清缓存：**完整标准输入即可触发节拍问题；缓存字段变化会失效。没有证据支持把它们作为本次根因。

## 5. Git 历史核查

已读取近期提交，并针对 `composer_plan_pill.go` 的 `embeddedBee`、`timeline_rows.go` 的 `todoItemsFromMessage` 和节拍直接 `renderToolChip` 做 `git log -S`。这些逻辑在可用历史中均追溯到 `cdceb3b`（初始提交）。`git show cdceb3b:internal/ui/composer_capsules.go` 确认旧版本已有蜂限制。因此不能把问题归因于最近的模型切换或 provider 合并提交。

## 6. 本次验证与阻塞

为遵守“不修改任何 .go 文件”，尝试将最小测试源码与 `go test -overlay` 映射都写在允许目录下的临时 `.md` 文件中。overlay 只向编译器提供虚拟测试文件，未创建或修改实体 Go 文件。测试设计对比：

1. 标准输入/结果尾行与否定缓存失效。
2. 主状态和蜂状态的单条待办卡片。
3. 相同消息进入展开工具组后是否仍出现卡片。
4. 主状态和蜂状态的 `composerPlanCapsule` 是否都返回进度。

执行命令设置了工具调用上限 120 秒、Go 测试上限 90 秒，并禁用 cgo 与网络依赖下载：

```sh
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off go test \
  -overlay docs/superpowers/plans/2026-11-10-subagent-todo-repro-overlay.md \
  ./internal/ui/ \
  -run '^(TestAspenSubagentTodoRepro|TestParseTodoItems|TestTodoItemsFromMessage.*|TestPlanPill_.*|TestTimelineRows_TodoCard)$' \
  -count=1 -timeout=90s -v
```

**实际结果：**包 setup failed。`internal/webassets/embed.go` 的 `all:dist` 找不到匹配资源。没有任何测试开始执行，不能声称上述最小测试已通过或已运行复现失败断言。

按任务要求，遇到缺文件阻塞后停止验证，没有生成前端资源、联网安装依赖、修改嵌入声明或使用替代资源绕过。临时 overlay 与源码 Markdown 已删除，只保留本方案。上面的命令是尝试记录，不是可直接重跑命令；临时文件已不存在。

实施代理应在具备项目合法构建资源的环境中新增第三节的正式测试，再限定运行：

```sh
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off go test ./internal/ui/ \
  -run 'TestTimelineRows_(GroupedTodoCard|ToolBeatChips|TodoCard|TurnWorkFoldUnfoldWithExpandedToolGroup)|TestComposerPlanCapsuleEmbeddedBee|TestComposerCapsulesShareOneRow|TestComposerCapsuleRowHiddenWhenEmpty|TestComposerBackgroundTasksCapsule|TestBuildTimelineItemsCollapsesTodo|TestTodoItemsFromMessage|TestPlanPill_|TestMotionBusyLoops' \
  -count=1 -timeout=90s
```

该命令外层仍需配置 120 秒执行上限。当前机器未发现 `timeout` 命令，所以本次通过执行工具的 `timeout=120000` 限制总时间。

## 7. 注意事项与未完成部分

- 仅设计，未实施两处生产代码修改，未添加正式 Go 测试。
- 两个断点有直接静态源码证据；运行时最小复现因缺少嵌入资源未完成。
- 未读取用户本地真实蜂消息记录，未启动 mcode 本体，也未做 GUI 验收。因此没有声称截图中原始 `ToolInput` 的实际长度。
- 当前方案保持工具组/完成回合的原有折叠行为。组收起时保留已有只读预览，不强制把待办卡片移到组外常驻。
- 正式验证需覆盖蜂浮窗、蜂独立窗口和普通主会话；不能只看单条渲染测试。
- 不新增依赖或组件，不修改 JSON-RPC 契约，不扩大到所有蜂胶囊，不增加辅助说明文字。
- 未启动后台服务，没有需要终止的后台任务。
