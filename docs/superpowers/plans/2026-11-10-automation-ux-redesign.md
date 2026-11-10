# 自动化 UI 与交互重设计方案

日期：2026-11-10。状态：设计稿，可交执行档拆分实施。基于当前工作副本与 Git 历史静态核查；本次只新增本文，不修改程序。

推荐方向：保留现有调度器，把自动化做成「列表 → 运行结果 → 按需编辑」。先修正保存、筛选和退出行为，再重排界面，随后补齐调度预览与失败恢复。默认界面只回答三件事：做什么、何时运行、结果如何。

## 0. 已定决策（2026-11-10）

1. **D1** 任务窗口名称为「任务」，顶部只有「任务」「自动化」两个目的导航；看板/列表切换只在「任务」内部；侧栏和命令面板的原入口仍直达对应目的。
2. **D2** 把待审阅任务拖到「已完成」只打开审阅，确认后才接受并合并；首期不做批量接受。
3. **D3** 自动化「立即运行」保留现有语义：计入 MaxRuns，可能顺延下次运行；不新增接口。
4. **D4** 自动化首发范围：结构化调度（每天/工作日/每周/间隔/单次/高级 Cron）加接下来 3 次运行预览；运行历史最近 100 条，看日志跳转到对应会话；不做自然语言调度；不开放新建 dedicated/heartbeat，既有的只读展示。
5. **D5** 自动化选中项默认先看运行结果，配置通过 tab 和编辑弹窗进入。
6. **D6** 看板「已完成」列默认收起；「待你处理」包含任务审批、失败、评审和目标计划待确认，不混入自动化失败；宽屏用右侧详情，窄屏用单卡 sheet。

## 1. 核查范围与证据等级

- 已读：任务指定的领域模型、调度、服务、评估器、锁、自动化 UI、运行状态 UI、关联入口、动效和 token，以及必要的 app 适配、远程类型、协议和已有测试。
- 已查 Git：自动化相关最近 8 条提交。`cdceb3b` 为初始实现；`b1a2030` 主要归一化权限；`bc63d79` 将删除菜单标为危险；`ca66ea4` 复用模型目录；`119a0e6` 将加载提示收口到 LoadingIndicator。当前布局与领域功能多数已在初始提交中，近期变更没有重做信息架构。历史只能说明变更范围，不能代替运行验证。
- 未运行：构建、测试、应用和桌面操作。本次按只读定位任务约束进行静态分析。下文中的测试是交给执行档的要求，不是已通过的结果。
- 未访问网络：本次工作方式明确禁止网络。竞品部分是离线交互参考，给出官方入口供主代理核验，不声称已浏览当前产品；CrychicUI 的包与文件已由主代理于 2026-11-10 联网核实（见 5.2），导出 API、ReduceMotion 行为与 mygo 兼容性待实现前读源码确认，不编造导出名。
- 本地组件检查：仓库 `go.mod`、`go.sum`、文档与自动化代码未发现 CrychicUI 引用；默认 Go 模块下载缓存的限定查询也未发现该库。不能由此推断用户其他目录不存在副本。

引用锚点均为本工作副本现有行号；每个引用后紧跟该行原文。实施新增文件以既有提取点为锚，不虚构新增文件的行号。

## 2. 现状与根因

### 2.1 已有能力，重设计必须保留

- 六种时间调度：间隔、单次、每天、工作日、每周、标准五段 cron。没有自然语言调度解析入口，也没有文件变化、Webhook 等事件调度类型；Agent 可代用户生成结构化配置，不等于调度器支持自然语言。首轮不新增事件引擎。

  `internal/automation/model.go:163` — `case "interval", "once", "daily", "weekdays", "weekly", "cron":`

  `internal/ui/automation.go:1009` — `{key: "interval", label: "每隔N分钟"},`

- 三种会话模式：每次新建、复用专属会话、投递来源会话。UI 可保留并展示已有模式，但当前表单没有模式选择器；模型与权限对 heartbeat 不生效。

  `internal/automation/model.go:42` — `RunModeStandalone = "standalone"`

  `internal/automation/model.go:44` — `RunModeDedicated = "dedicated"`

  `internal/automation/model.go:46` — `RunModeHeartbeat = "heartbeat"`

  `internal/ui/automation.go:2072` — `heartbeat := as.runMode == "heartbeat"`

- MaxRuns 为规范字段，0 表示不限；兼容 MaxIterations。CatchUp 缺省开启；补跑合并为一次运行，周期任务关闭补跑时直接推进，单次任务错过且不补跑时留 skipped 记录后结束。

  `internal/automation/model.go:83` — ``MaxRuns int `json:"maxRuns,omitempty"` ``

  `internal/automation/model.go:125` — `// CatchUpEnabled 返回是否允许补跑：未设置时按开启处理，兼容旧数据。`

  `internal/automation/service.go:836` — `s.createSkippedRun(ctx, auto, missed)`

  `internal/automation/service.go:850` — `_, _ = s.trigger(ctx, auto, now, kind, missed)`

- 同一自动化禁止并发重入；跨进程只有持锁实例调度，手动运行也受锁保护。暂停后仍能手动运行，但已达次数上限时不能继续。

  `internal/automation/service.go:486` — `return store.AutomationRun{}, ErrSchedulerPassive`

  `internal/automation/service.go:492` — `return store.AutomationRun{}, ErrAlreadyRunning`

  `internal/automation/service.go:947` — `return store.AutomationRun{}, ErrMaxRunsReached`

- 运行结算依赖回执和实际会话状态，不能由 UI 用「会话空闲」推断成功。等待审批、输入或排队不算完成；未回执会追问，超过次数记失败。

  `internal/automation/service.go:701` — `if st.Running || st.AwaitingApproval {`

  `internal/automation/service.go:749` — `s.settleRun(ctx, autoID, runID, RunFailed, "", "Agent 未提交运行回执（已追问 2 次）", nil, false)`

  `internal/automation/service.go:1053` — `return fmt.Sprintf("%s\n\n本轮结束前必须调用 automation_report_result 工具，参数 automation_id=%s、run_id=%s、status 取 succeeded 或 failed、summary 写一两句结论；没调用就不算完成。", prompt, automationID, runID)`

- 停止条件可以由报告方确认，或由评估器依据摘要和变更验证。模型评估报错时是未验证，不能展示为条件达成。LastEvaluation 只有自动化级的最近评估，不是每次运行的评估快照。

  `internal/automation/evaluator.go:218` — `Reason:     fmt.Sprintf("评估失败，停止条件未验证: %v", err),`

  `internal/automation/model.go:70` — ``LastEvaluation      *EvaluationResult `json:"lastEvaluation,omitempty"` ``

### 2.2 用户流程与具体痛点

**P01：列表是窄卡片，信息与操作过载。** 左栏固定 240 DIP，却同时容纳开关、标题、状态胶囊、调度、下次/上次、任务预览、停止条件、失败计数以及三个操作。根因是把详情和动作复制到列表，卡片装饰又消耗空间。

`internal/ui/automation.go:1334` — `Width(240).`

`internal/ui/automation.go:1581` — `// Prompt 预览，最多两行`

`internal/ui/automation.go:1635` — `// 操作：立即运行、运行记录、复制；卡片窄时换行，避免「复制」被裁掉。`

**P02：创建入口重复，默认页顶部存在多层工具栏。** 页面内已有汇总条和列表头两个新建按钮，还内置项目选择器；宿主也有项目与搜索栏。功能没有少，视觉主次不清。

`internal/ui/automation.go:1192` — `renderWorkspaceProjectPicker(c, s, curPID)`

`internal/ui/automation.go:1279` — `Button(c.Key("automation-summary-new-btn"), ButtonOpts{`

`internal/ui/automation.go:1356` — `Button(c.Key("automation-new-btn"), ButtonOpts{`

`internal/ui/kanban_modal.go:281` — `kanbanProjectMenu(c, s, p.projectID, func(id string) { s.setWorkspaceProject(id) })`

**P03：编辑与查结果没有区分。** 点击列表直接回填编辑草稿；「查看运行记录」和「编辑」都调用同一个选择函数。记录只能在全部表单之后看到，日常查看结果被迫经过配置。

`internal/ui/automation.go:280` — `as.fillAutomation(auto)`

`internal/ui/automation.go:1467` — `if m.Item("查看运行记录").Chosen() {`

`internal/ui/automation.go:1470` — `if m.Item("编辑").Chosen() {`

`internal/ui/automation.go:2276` — `ui.Column(c.Key("automation-runs-section")).`

**P04：草稿切换没有保护。** 选择另一项会覆盖现有草稿，新建和取消也直接改状态；没有 dirty 基线与离开处理。订阅刷新目前会保护草稿和 revision，这项已有行为必须保留。

`internal/ui/automation.go:293` — `func (as *automationState) fillAutomation(auto Automation) {`

`internal/ui/automation.go:1783` — `as.selectedID = ""`

`internal/ui/automation_test.go:190` — `func TestAutomationWorkspace_RefreshConflictAndDraftRevision(t *testing.T) {`

**P05：保存带有隐蔽的调度副作用。** UI 保存总传 Enabled；app 适配总传非 nil 的 Enabled；Service 在 Enabled=true 时清失败数、清停止原因并从当前时间算下次运行。同时只要 Spec 非 nil 就认为日程变化。仅改名称也可能移动下一次运行并清掉失败痕迹。结论来自调用链静态核查，尚未实测复现。

`internal/ui/automation.go:539` — `Enabled:                enabled,`

`internal/app/client.go:1956` — `Enabled: &in.Enabled,`

`internal/automation/service.go:359` — `scheduleChanged = true`

`internal/automation/service.go:366` — `spec.State.ConsecutiveFailures = 0`

`internal/automation/service.go:368` — `if next, ok := spec.Schedule.Next(now); ok {`

**P06：列表启停后草稿版本可能过时。** 成功后只同步 as.enabled，没有将此次写操作返回的 revision 合并到无冲突草稿；接着刷新列表刻意不刷新表单。下一次保存可能制造本窗口自己的版本冲突。应区分「本窗口成功写入」与「外部更新」。

`internal/ui/automation.go:732` — `as.enabled = enabled`

`internal/ui/automation.go:736` — `s.loadAutomationList(projectID, false)`

**P07：时间输入是技术格式，缺少就地预览。** 六段调度控件占一整行；每天/每周输入 HH:MM，单次输入 Go 布局格式文字；cron UI 只校验五段数量，完整范围校验在后端才发生。现有非预设秒数、单次秒精度已经有保留逻辑，不能在新编辑器中丢失。

`internal/ui/automation.go:1885` — `Segmented(c, "automation-sched-type-seg", &curTypeIdx, typeSegments, func(idx int) {`

`internal/ui/automation.go:2044` — `ui.Text(c, "执行时间 (2006-01-02 15:04)").`

`internal/ui/automation.go:495` — `if len(strings.Fields(cronExpr)) != 5 {`

`internal/ui/automation.go:516` — `sched.Date = originalSchedule.Date`

**P08：下次运行已有显示，但不够可判断。** 卡片与汇总只显示相对时间，编辑器没有未来几次预览；Timezone 被保存却没有可见的编辑入口。Schedule.Next 的空时区使用调度进程本地时区，不能随意用查看者设备时区解释既有任务。

`internal/ui/automation.go:1562` — `ui.Text(c, "下次 "+formatRelativeUntil(autoVal.NextRunAt.Local())).`

`internal/ui/automation.go:454` — `sched.Timezone = originalSchedule.Timezone`

`internal/automation/schedule.go:308` — `loc := time.Local`

**P09：间隔不是严格的墙上时钟周期。** 启动和结算都会按当前时间计算下一次；尤其结算后是完成时刻加间隔。未来三次只能是规则预览，不能承诺精确开始时间。cron 的查找也有 366 天范围，Validate 成功不保证该范围内有下一次。

`internal/automation/service.go:1305` — `if next, ok := spec.Schedule.Next(now); ok {`

`internal/automation/schedule.go:301` — `// nextCron 在 cron 表达式基础上逐分钟向后查找下一次触发时间，最多查找 366 天。`

**P10：共享窗口的搜索没有接到自动化列表。** 宿主读写 ks.searchText，但嵌入调用的是传空查询的包装函数。仓库中 WithSearch 的调用只有这个空参数入口。因此现有筛选函数能工作，不代表用户在实际入口能筛选。

`internal/ui/kanban_modal.go:285` — `searchQuery := ks.searchText`

`internal/ui/kanban_modal.go:831` — `renderAutomationWorkspace(c, s)`

`internal/ui/automation.go:1103` — `renderAutomationWorkspaceWithSearch(c, s, "")`

**P11：只有文本筛选，没有状态筛选。** 搜索覆盖名称、任务、停止条件和日程，排序启用优先、下次时间、名称；没有失败、暂停、未读快捷筛选。搜索结果不该改变项目级通知总数。

`internal/ui/automation.go:1047` — `func filterAutomations(autos []Automation, query string) []Automation {`

`internal/ui/automation.go:1163` — `totalUnread := automationUnreadTotal(unread, visible)`

**P12：历史是摘要列表，不是日志系统。** 当前详情展开只是全量摘要、错误、记录 ID 和已读状态；打开会话能看真实执行内容。一次最多取 100 条，没有游标分页。现有字段不支持为专属/来源会话精确切出本次日志区间，也不支持历史配置快照重放。

`internal/ui/automation.go:254` — `runs, runRead, runArchived, err := s.fetchAutomationRuns(ctx, id, 100, includeArchived)`

`internal/ui/automation.go:2503` — `// 详情：记录 ID 与已读/归档状态，摘要与错误改为整段展示`

`internal/ui/automation.go:2451` — `s.revealThread(threadID)`

`internal/ui/client.go:426` — `ID, AutomationID, ThreadID, Status, Summary, Error string`

**P13：没有运行级重跑入口；立即运行没有直达结果。** RunNow 返回新记录，但 UI 丢弃它，只刷新当前选中的运行列表。手动运行实际会更新计划与累计次数，不能伪装成不影响计划的测试按钮。

`internal/ui/automation.go:656` — `_, err := s.client.RunAutomation(ctx, id)`

`internal/automation/service.go:509` — `return s.trigger(ctx, auto, s.now(), RunTriggerManual, 0)`

`internal/automation/service.go:955` — `spec.State.TotalRunCount++`

**P14：失败反馈存在，但恢复动作表达有误。** 行内失败计数、错误、toast、锁提示都已具备；「重置」实质会启用自动化。重设计应把动作写为「恢复计划」或在仍启用时写为「清除失败计数」，并保留其真实语义。锁被占用表示由另一个实例调度，不应笼统暗示全局已停。

`internal/ui/automation.go:1621` — `s.setAutomationEnabled(autoVal, true)`

`internal/ui/automation.go:976` — `return "定时任务已暂停：另一个 mcode 实例正在调度"`

`internal/automation/scheduler_lock.go:19` — `// SchedulerLockPassiveHeld 表示锁被其他进程持有，本进程不触发任何任务。`

**P15：已读、归档采用乐观更新，但失败静默。** 后端写失败时既无回滚也无提示，会让用户认为状态已持久化；全部已读的错误也直接返回。

`internal/ui/automation_runs_ui.go:143` — `as.runRead[runID] = time.Now().UTC()`

`internal/ui/automation_runs_ui.go:152` — `if err := client.MarkAutomationRunRead(context.Background(), id); err != nil {`

`internal/ui/automation_runs_ui.go:175` — `if _, err := client.MarkAllAutomationRunsRead(context.Background(), id); err != nil {`

`internal/ui/automation_runs_ui.go:205` — `if err := client.SetAutomationRunArchived(context.Background(), id, archived); err != nil {`

**P16：空状态有长宣传说明；加载与失败布局不统一。** 保留已有首次加载、加载失败重试、搜索无结果状态，删除常驻说明文字。历史为空时可直接运行，而不只给一行「暂无」。

`internal/ui/automation.go:1227` — `ui.Text(c, "自动化会按计划自动启动 Agent 会话，完成巡检、审计或看护等周期任务，并在满足停止条件时自动结束。").`

`internal/ui/automation.go:2342` — `ui.Text(c, "暂无运行记录").`

**P17：键盘只具备入口和基础控件能力，退出有抢占。** 命令面板能打开自动化；自动化文件没有自己的按键路由。独立任务窗口根视图先捕获 Esc 并关闭窗口，后续即使给编辑器加 Esc，也会被宿主抢走。

`internal/ui/palette.go:183` — `title: "打开自动化",`

`internal/ui/root.go:86` — `if c.Shortcut(0, ui.KeyEscape) {`

`internal/ui/root.go:87` — `state.closeTasksWindow()`

**P18：入口未承接未读结果，图标语义不一致。** 未读数只在自动化内加载和展示，侧栏自动化入口没有角标；侧栏用日历图标，通用图标别名却映射闪电。先统一入口，再考虑跨窗口同步角标，不能让未打开页面的零值冒充已读。

`internal/ui/sidebar_footer.go:118` — `Icon:    IconCalendarClock(),`

`internal/ui/icons.go:236` — `case "zap", "automation", "automations":`

`internal/ui/icons.go:237` — `return iconZap`

根因归纳：领域模型相对完整，UI 把浏览、编辑和历史放在一个大型渲染函数中；局部状态没有表达草稿生命周期与动作归属；宿主没有向自动化传递搜索和按键所有权；保存与恢复在适配层混用。单换颜色、圆角或引入组件库不会解决这些问题。

## 3. 设计原则与明确边界

1. **结果优先。** 选中已有自动化先看最近运行；只有创建或点击「编辑」才出现编辑器。成功、失败、暂停、完成各有明确位置，不再把所有信息做成胶囊。
2. **一处一个动作。** 工具栏只有一个新建；列表行是选择目标；详情负责立即运行与编辑；更多菜单承载复制、归档、删除。可见按钮与无障碍 Label 同源，但稳定测试 key 单独保留。
3. **结构解释内容。** 名称直接作为标题，任务正文直接作为编辑主体；删除「Prompt 提示词」「StopWhen」「0 = 不限制」等技术说明。用「不限 / 最多 N 次」控件表达，不用长句解释值。
4. **保留必要信息。** 日期、时区、执行权限、失败原因不是可以删除的辅助文字；在决策位置显示。提示只在错误、不可执行或语义容易误解时出现，不放宣传标语。
5. **不扩大执行能力。** 保留六种时间调度；新建仍使用 standalone；既有 dedicated/heartbeat 只读展示并保留。首轮不开放模式切换，不加入事件触发、可视化流程编排、自然语言解析器或历史配置重放。
6. **共同提升 UX / DX / AX。** 用户看同一份结构化规则，UI 与 RPC 调用同一调度算法；代理读到的预览和人看到的一致。组件按功能文件拆分，不新增通用页面框架或第二套状态仓库。
7. **保守扩展协议。** 布局、筛选、标签切换和基于当前配置的重跑不新增持久化字段。唯一推荐新增 RPC 是纯读的 `automations.preview`；后续日志精确切片或历史重放另立任务。
8. **沿用项目硬约束。** 保持纯 Go、禁止 cgo；弹窗只能用 `ui.DialogBase` / `modalDragModal`，浮层只能用 `ui.PopoverBase` / `appPopover`，禁止调用 `ui.Modal` / `ui.Popover`。外层只设尺寸上限，内容卡片承担外观。所有动效必须响应 ReduceMotion，包括已经运行的循环。

## 4. 竞品与开源交互参考（离线，待核验）

以下覆盖 10 个产品，其中 n8n、Windmill、Huginn 为开源项目。链接是核验入口，不代表本次已读取。特别是 Codex、Cursor、Linear 的当期功能名称、限制和时间语义，主代理应联网核对后再对外宣称是竞品事实。这里采用的是可独立判断价值的交互模式，不依赖复刻具体像素。

| 产品与来源入口 | 值得借鉴的具体交互（每项 1—3 条） | 在 mcode 的落点 | 不借鉴与理由 |
|---|---|---|---|
| ChatGPT Tasks：[官方说明](https://help.openai.com/en/articles/10291617-scheduled-tasks-in-chatgpt) | 从任务意图进入创建；把频率呈现为可编辑的自然语言摘要；结果完成后有返回入口 | 任务正文优先；「工作日 09:00」作为规则按钮；点击结果回到记录 | 不让自由文本静默变成日程；首轮不做自然语言解析，避免时区与歧义成本 |
| OpenAI Codex：[官方文档入口](https://developers.openai.com/codex/) | 自动任务与结果审阅放在同一工作区；任务能从结果跳回执行上下文 | 默认打开运行历史；详情一键打开对应会话 | 不照搬云端常驻或默认工作树隔离承诺，当前服务启动的是既有会话驱动 |
| Cursor 后台代理：[官方文档入口](https://cursor.com/docs) | 明确展示执行状态；从运行条目进入代理上下文；允许再次发起相同意图 | 运行行状态、打开会话、按当前配置再次运行 | 不把后台代理等同于 cron，也不引入独立云环境和 PR 流程 |
| Raycast：[官方手册](https://manual.raycast.com/) | 搜索直接定位动作；键盘导航列表；动作面板把次要操作收拢 | Cmd+F、上下选择、更多菜单与命令面板 | 不复制全局热键体系，不让字母单键在正文输入时触发动作 |
| Apple 快捷指令自动化：[官方指南](https://support.apple.com/guide/shortcuts/welcome/ios) | 「何时 / 做什么」两块；触发条件可视编辑；启用状态清楚 | 日程行加任务正文；启停独立于编辑提交 | 不复制设备事件目录和积木流程，也不添加当前后端没有的事件按钮 |
| Linear recurring issues：[官方文档入口](https://linear.app/docs) | 复用已有对象编辑方式；重复规则紧凑呈现；可以查看重复计划 | 单个轻量编辑器；调度摘要与预计时间靠近 | 不照搬 issue 完成后再生成下一张的对象生命周期；自动化与运行记录是两个现有实体 |
| GitHub Actions：[定时事件](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows)、[重新运行](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/re-run-workflows-and-jobs) | 按时间排列状态列表；运行详情与日志分开；从失败结果再次运行 | 历史列表、结果摘要、打开会话、再次运行 | 不以 YAML 或 cron 为默认编辑方式，不复制 jobs/steps 树，因为当前没有该运行模型 |
| n8n（开源，fair-code）：[源码](https://github.com/n8n-io/n8n)、[Schedule Trigger](https://docs.n8n.io/integrations/builtin/core-nodes/n8n-nodes-base.scheduletrigger/)、[Executions](https://docs.n8n.io/workflows/executions/) | 简单频率与高级 cron 分开；执行历史按状态筛选；失败运行可检查后再执行 | 日程高级入口、失败筛选、结果恢复动作 | 不引入节点画布、节点级数据回放或源码；许可不能按 MIT 对待，本方案只参考交互 |
| Windmill（开源）与 Huginn（开源）：[Windmill 源码](https://github.com/windmill-labs/windmill)、[Windmill 文档](https://www.windmill.dev/docs/intro)、[Huginn 源码](https://github.com/huginn/huginn) | Windmill 的日程与运行分层、运行日志定位；Huginn 的 Agent 状态与事件审计思路 | 日程和历史分开；显式记录手动/补跑来源 | 不引入脚本平台、worker 队列或事件图；不能把当前 summary 伪装成结构化执行日志 |

推荐组合：Linear/Apple 的界面密度 + GitHub Actions/Windmill 的运行历史组织 + Raycast 的局部键盘动作。n8n 只参考简单/高级调度分层。自然语言创建可继续通过现有对话工具完成，最终必须进入结构化日程预览。

## 5. 组件复用与 CrychicUI 核验门槛

### 5.1 已核实、可直接复用的本地实现

| 需求 | 当前可用实现 | 决策 |
|---|---|---|
| 分段控件 | 本地 Segmented / SegmentedFill，底层 mygo SegmentedBase | 列表状态筛选最多三项，日程类型改下拉，不再六段挤在一起 |
| 开关 | 本地 Switch / SwitchDisabled | 启停请求期间禁用；沿用系统偏好与主题 |
| 状态点 | 本地 StatusDot | 运行中、失败、未读有文字或无障碍名，不仅靠颜色 |
| 加载 | 本地 LoadingIndicator | 第一版复用，不为装饰新增骨架系统 |
| 浮层 | appPopover / anchoredPopover | 日程和更多动作；只保留内容卡片一层外观 |
| 弹窗 | modalDragModal / ui.DialogBase | 编辑器与草稿离开确认；外层仅设尺寸上限 |
| 时间线 | 现有 Row / Column + StatusDot + 稳定 runID | 首轮用日期分组的记录行；不引入专门时间线框架 |
| 日期 / 时间输入 | 当前 TextInput 可作为手输后备 | 优先读 CrychicUI datetime picker 源码；API 未确认前不自研完整日历 |

`internal/ui/controls_toggle.go:32` — `func Segmented(c *ui.Context, key any, selected *int, segments []Segment, onChange func(int)) ui.Element {`

`internal/ui/controls_toggle.go:88` — `func Switch(c *ui.Context, key any, on *bool, label string, onChange func(bool)) ui.Element {`

`internal/ui/controls_chip.go:155` — `func StatusDot(c *ui.Context, key any, st Status, label string) ui.Element {`

`internal/ui/controls_loading.go:5` — `func LoadingIndicator(c *ui.Context, key any, text string, size float32, colors ...ui.Color) ui.Element {`

`internal/ui/popover.go:37` — `func appPopover(c *ui.Context, anchor ui.Element, open *bool, onDismiss func(), fn func()) ui.Element {`

`internal/ui/modal_drag.go:85` — `modalDragPanel(c, key, ui.DialogBase(c, open, func(backdrop, panel ui.Element) {`

`go.mod:8` — `github.com/egoist/mygo v0.8.4-0.20261107102458-435d9411845d`

### 5.2 CrychicUI：必须完成核验才能引入

来源：[ELY-Open-Source/CrychicUI](https://github.com/ELY-Open-Source/CrychicUI)。AGENTS.md 提供其 MIT、纯 Go、同源 UI 的背景；主代理已于 2026-11-10 联网核对 module、版本与文件清单（见本节末）。**包与文件已核实，导出 API 与 mygo 兼容性待实现前读源码确认；不允许把猜测路径写入 go.mod。**

执行档按下列候选只查七类组件：日期、时间、分段、开关、时间线、空状态、骨架。对每个候选读源码记录完整包路径、可固定的 commit、导出 API、mygo 兼容版本、许可和 ReduceMotion 路径；有合适实现则按 AGENTS.md 直接复用。没有合适实现或 API 无法确认时，保留现有本地控件和可手输时间，说明阻塞，不自行造日历替代。

引入的硬门槛：关闭 sound 的默认播放或接入 mcode 声音开关，并验证关闭时零播放；禁止引入 overlay 包作为外壳；验证包括循环动画在内的 ReduceMotion；确认没有 cgo 和隐藏的平台编译依赖。组件声明的「减少动效」不够，必须在 dev 版本切换偏好后验证已有动画立即停止。

版本策略：不升级 mygo 来迁就单个 picker，不全库导入，不为状态点/开关引入新依赖。CrychicUI 日期/时间组件的包与文件已核实；导出 API 与 mygo 兼容性仍是未完成项，实现前读源码确认后再启动该子步骤。

#### 联网核验结果（2026-11-10）

- module：`github.com/ELY-Open-Source/CrychicUI`。仓库没有 release 或 tag，只有 main 分支，引入时必须固定到具体 commit。
- 版本基线：CrychicUI 依赖 `github.com/egoist/mygo v0.8.3`，要求 Go 1.27.3；mcode 的 `go.mod:8` 是 `github.com/egoist/mygo v0.8.4-0.20261107102458-435d9411845d`。兼容性在引入时用 `CGO_ENABLED=0 go build` 验证。
- 共用底层包：sound、icon、kit；另有 overlay，按 AGENTS.md 不使用。
- datetime 包文件：cron_field.go、recurrence.go、time_picker.go、date_picker.go、date_time_picker.go、duration_picker.go、zone_select.go、relative_date.go、time_ago.go、due_chip.go。
- schedule 包文件：upcoming.go、next_event.go、week_view.go、month_view.go。

候选对应关系：

| 自动化能力 | CrychicUI 候选文件 |
|---|---|
| 高级 Cron | datetime/cron_field.go |
| 重复规则 | datetime/recurrence.go |
| 时间 | datetime/time_picker.go |
| 单次日期 | datetime/date_picker.go 或 datetime/date_time_picker.go |
| 间隔 | datetime/duration_picker.go |
| 时区 | datetime/zone_select.go |
| 相对时间（如「明天 09:00」） | datetime/relative_date.go |
| 即将运行列表 | schedule/upcoming.go |

实现前读这些文件的导出 API，确认签名、ReduceMotion 与 sound 行为；API 与 mygo 兼容性未确认时保留现有本地控件，不猜导出名。

## 6. 目标信息架构与文字线框

窗口名称为「任务」，顶部只有「任务」「自动化」两个目的导航（见 D1），看板/列表切换只在「任务」内部。宿主负责项目、视图切换、搜索和窗口关闭；自动化主体负责列表、结果与创建。项目选择器不再重复。任务数量徽标在自动化视图隐藏，避免把看板数字解释为自动化数量。

**列表与详情，宽窗（建议内容可用宽度 ≥ 1000 DIP，最终用实机验证调整）：**

    任务    [项目 ▾]    [任务 | 自动化]    [搜索]    [+ 新建]
    ─────────────────────────────────────────────────────────────
    [全部 | 失败 | 暂停]   [未读]                 每周安全审计    [启用] [运行] [⋯]
    名称                下次运行                 工作日 09:00 · Asia/Shanghai
    ● 每周安全审计       明天 09:00              [运行记录 | 配置]
    ○ 依赖更新           周五 18:00              今天
    ● 构建检查           运行中                  ● 失败    09:00   32s          [查看]
                                              ✓ 成功    昨天    28s          [查看]
                                              [显示已归档]

左栏目标约 300–340 DIP、可收缩；行高按两行内容决定，不塞任务预览。下次时间使用「今天 14:30 / 明天 09:00 / 11月16日 09:00」，hover 或详情显示完整日期及偏移。只有运行中、失败、暂停、完成这些需要辨认的状态文字出现；正常待运行不重复显示「正常」。启用状态与上次结果分列，避免一次失败被误解为计划已停。

**窄窗（覆盖现有 840×520 最小窗口）：**

    [项目 ▾] [自动化] [搜索] [+]
    [全部 | 失败 | 暂停] [未读]
    自动化列表填满内容区
    选择后进入详情： [← 自动化]    名称    [⋯]
                   调度    [启用] [运行]
                   [运行记录 | 配置]

不在窄窗保留空的第三列。返回列表保留搜索、滚动和选中项。840 DIP 是现有窗口下限，不要求为了本方案修改窗口限制。

`internal/ui/tasks_window.go:110` — `MinWidth:             840,`

**编辑器（一个弹窗，一张内容卡片）：**

    新建自动化                                      [取消] [创建并启用 ▾]
    名称 __________________________________________
    要执行的任务
    ______________________________________________
    ______________________________________________
    [每天 ▾] [09:00] [Asia/Shanghai ▾]
    预计运行  11月11日 周三 09:00
              11月12日 周四 09:00
              11月13日 周五 09:00
    ▸ 执行设置
       [模型 ▾] [权限 ▾]
    ▸ 停止与补跑
       达到条件 _________________________________
       [不限 ▾] / [最多 N 次]
       连续失败 [3] 次后暂停
       错过后补跑一次 [开关]

这里的「要执行的任务」是一个必要字段标题；不再加副标题与说明段。编辑已有对象时标题为对象名称，提交为「保存」；状态开关留在详情，保存配置不隐式恢复计划。创建按钮菜单只有「创建并启用」「创建为暂停」两项，默认沿用现有 enabled=true，避免无意改变已有新建语义。

**日程浮层：**

    [每天 ▾] 选项：每天 / 工作日 / 每周 / 间隔 / 单次 / 高级 Cron
    每周：[周一 ▾] [09:00]
    间隔：[15] [分钟 ▾]，可切秒/小时，保留任意既有秒数
    单次：[2026年11月11日 ▾] [09:00]
    高级：[0 9 * * 1-5________________]
    [Asia/Shanghai ▾]
    预计运行：三条日期；单次只有一条；错误显示在对应输入下

星期选择仍为单选，不能用多选星期暗示后端 weekly 已支持多天。工作日固定周一至周五，不等于节假日日历。时区使用 IANA 标识，列表可附当前偏移，但不存偏移量代替时区。

**运行详情（宽窗在结果区就地替换，窄窗进入下一层；不叠第三个侧栏）：**

    [← 运行记录]    失败 · 11月10日 09:00    [再次运行] [⋯]
    [手动 / 补跑（合并 3 次错过）]
    09:00:00 → 09:00:32    32s
    未提交运行回执，已追问 2 次。
    结果摘要正文，可选择复制
    [打开会话] [复制错误]

运行 ID 放更多菜单的「复制运行 ID」，不常驻展示「已读」。没有 ThreadID 时不显示虚假的日志入口；错误仍可复制。打开复用会话时显示完整会话，不给它贴「仅本轮日志」标签。

## 7. 交互规格

### 创建、编辑与离开

- 新建后聚焦名称；Tab 顺序为名称 → 任务 → 日程 → 高级设置 → 提交。提供少量本地模板菜单：空白、工作日巡检、每周依赖检查。模板只填草稿，不能直接创建或启动。
- 创建成功选中返回对象并进入其详情；订阅刷新不能再创建一次。保留当前防重复保存和项目隔离 token。
- 编辑使用独立 draft + base revision；只有字段值变化才 dirty。相同值提交不写入。对话、外部刷新、列表启停都不得偷偷覆盖 dirty 字段。
- 切对象、项目、视图、取消和关闭时走同一离开处理。dirty 时显示「保存 / 放弃 / 继续编辑」；无改动直接离开。保存失败留在编辑器；显式放弃才丢草稿。
- 版本冲突保留草稿，提供「查看最新」而不自动覆盖；确认「载入最新」后再替换。首轮不做字段自动合并。

### 调度编辑、时区与预览

- 常用调度用类型下拉 + 当前参数，不默认展示 cron。可手输时间，picker 是辅助入口；输入错误就地指出字段，并聚焦首个错误。
- 采用同一后端 Schedule.Validate / Next 计算未来三次；修改参数后 200ms 合并请求，取消前次请求并用请求序号丢弃迟到结果。预览不获取调度锁，不创建运行，不修改 nextRunAt。
- 新建显式写入调度端解析后的 IANA 时区；旧任务空时区仍保留「跟随调度端」，不能打开表单就固化为查看端时区。后端无法提供可靠 IANA 名时保留空值并展示解析到的区域/偏移，不捏造城市。
- 预览是规则候选，不保证准点执行。间隔预览在其模式内显示「完成后间隔 N 分钟」；当前已保存任务的真实下一次先用 nextRunAt。不能把三次规则预览当作运行完成后的严格承诺。
- 过去单次创建禁止提交，并给「选择未来时间」；旧的已完成单次可以改名称/任务而不改时间，显示「已结束」，不会被强制改成明天。cron 合法但 366 天内无候选时显示「未来 366 天内无运行」，阻止启用而允许保存为暂停。

### 启停、立即运行与再次运行

- 详情开关只改计划；启停不覆盖编辑草稿。请求中禁用当前对象的重复动作，失败回到服务器状态，并在操作附近保留错误。
- 暂停文案只代表后续计划；在运行中的会话不会因此中止。若正运行，在开关旁显示「本次继续运行」这一条必要反馈。停止本次通过「打开会话」使用现有停止行为，首轮不加一个假停止 API。
- 普通「运行」执行已保存配置；有 dirty 草稿时不能悄悄运行旧配置，提供「保存并运行 / 运行已保存配置 / 取消」明确选择。
- 使用 RunAutomation 返回的 runID，成功后转到新运行详情；返回记录已是 failed 时立即显示真实错误，不先显示成功 toast。跨进程锁或重复在途时阻止提交并指出原因。
- 失败记录的「再次运行」创建一条 manual 记录，使用当前已保存配置；原记录保持原状。按钮附近或首次动作确认中说明「使用当前配置」。不声称重放历史配置。
- 暂停任务可手动运行且仍保持暂停；手动运行按现有语义计入 MaxRuns，并可能移动下次运行。执行档必须验证此规则，不能在 UI 里自动 reset 来绕过次数上限。
- 达 MaxRuns / 单次已完成 / 停止条件达成时，「再次运行」不能绕过结束状态：到达次数上限引导修改限额或复制；过期单次建议复制后选未来时间。首轮推荐不改 RunNow 调度语义。

### 历史、日志与失败恢复

- 默认最近运行，按日期分组；可切「全部 / 失败」并叠加「未读」，归档开关放筛选菜单。列表筛选只作用于当前已加载窗口，空结果写「最近 100 条中无匹配」，避免暗示全历史已查完。
- 第一版取最近 100 条，不加游标字段；固定显示「最近 100 条」当返回满额，明确此限制。分页和跨自动化全量运行收件箱后续单列需求。
- 点击运行详情才标已读；刷新/列表选中不清未读。运行尚在进行时的已读沿用现有存储语义，不承诺其结束后自动再次变未读。
- 已读和归档写失败回滚本地快照；对象已切换时仍按 automationID/runID 处理，不能扣减新项目角标。针对同一对象的后续操作用序号保护，旧失败不能回滚较新的成功。
- 区分「执行失败」「未提交回执」「等待输入超时」「用户停止」「错过未补跑」；文本由真实 error/status/trigger 决定。running 只有一个状态字段时只写「运行中」，不凭猜测展示「等待审批」等子状态。
- 失败未到阈值保持启用；达到阈值后给「恢复计划」。清除失败计数或恢复均是显式控制动作，不夹在保存里。已有 attention 的 automation_failed / automation_finished 通知接线继续使用，不再另建通知系统。
- 日志首轮通过 ThreadID 打开会话。摘要和错误可选择复制。dedicated/heartbeat 共享会话没有逐次边界，首轮不承诺定位到本次第一条消息。

通知复用依据（文档说明，实际通知展示仍需主代理实机核对）：

`docs/dev/06-kanban-automation.md:107` — ``- `OnRunSettled` 把结果接到 attention（`automation_failed`、`automation_finished`）。``

### 键盘与无障碍

| 范围 | 按键 | 行为与所有权 |
|---|---|---|
| 任务窗口处于自动化视图 | Cmd+N（其他平台采用既有平台主修饰键） | 新建自动化；只在该窗口/视图生效，不改变主聊天窗口的新会话 |
| 自动化列表 | Cmd+F | 聚焦自动化搜索；输入不触发全局会话查找 |
| 列表拥有焦点 | ↑ / ↓、Enter | 移动选择、打开详情；不拦截文本输入或下拉控件 |
| 编辑器 | Cmd+S | 保存；提交校验与按钮相同；不把回车当任务提交 |
| 日程浮层、菜单、编辑器 | Esc | 按最上层先关闭；dirty 编辑器进入离开确认 |
| 详情内无浮层 | Esc | 先回历史/列表；再由宿主执行原有关闭行为 |
| 所有控件 | Tab / Shift+Tab / Space | 合理顺序、焦点边框、开关与按钮可触达 |

立即运行不默认绑定 Cmd+Enter，避免与审批和任务正文的既有语义冲突。所有图标按钮有中文无障碍名；状态包含文本，颜色只作辅助。测试用稳定 key，不靠像素坐标。

## 8. 动效清单与 ReduceMotion

不新建动画引擎。主要反馈来自现有 token、Theme* 和预设；不做整页瀑布入场、循环骨架扫光或每秒倒计时刷新。

| 场景 | 复用预设 | 正常行为 | ReduceMotion 开启 |
|---|---|---|---|
| 行选中、焦点、启停状态落定 | motionFeedback | 颜色在 DurSnap 内变化，焦点立即可见 | 即时终态，焦点仍可见 |
| 新增、复制、归档移除行 | motionListItem | 稳定 ID，只对实际增删行做小位移与高度过渡 | 即时增删，不延迟焦点恢复 |
| 首次加载后的有限条目入场（可选） | motionEnter | 最多现有 staggerLimit 条；普通刷新不重复 | 直接 1，无延迟 |
| 日程与更多菜单 | motionPopover（由已有浮层壳处理） | 跟随锚点轻微升起与淡入 | 无位移、缩放与渐隐 |
| 编辑器 / 离开确认 | motionDialog（由壳处理） | 轻微位移与 springGentle | 即时显示/关闭 |
| 窄窗列表到详情 | motionTransition | DurBase 的短淡入；不叠加第三个面板动画 | 直接切换，返回焦点不变 |
| 高级设置折叠 | motionCollapse 或已有 motionUnfoldBody，择一 | 高度和透明度一起变化，收起后移出焦点树 | 直接到目标高度；不保留隐藏输入焦点 |
| 保存、失败与恢复反馈 | motionToast + motionFeedback | 复用现有 toast 与就地状态，避免重复响铃 | 静态 toast，内容完整 |
| 未读计数更新（确需数字动效时） | motionNumberProgress | 仅入口数字变化，不影响列表排序 | 直接显示新数字 |
| 运行中 / 加载 | LoadingIndicator；自定义循环只能用 motionLoop | 使用现有 spinner，最多一个忙碌指示 | 静态点 +「运行中」，不安排循环续帧 |

预设与偏好锚点：

`internal/ui/motion_presets.go:14` — `func motionTransition(c *ui.Context, t ui.ElementTransition) ui.ElementTransition {`

`internal/ui/motion_presets.go:43` — `func motionNumberProgress(c *ui.Context, e ui.Element, changed bool) (progress, fade float32) {`

`internal/ui/motion_presets.go:57` — `func motionLoop(c *ui.Context, e ui.Element, key any, period time.Duration, ease ui.Easing) float32 {`

`internal/ui/motion_presets.go:64` — `func motionEnter(c *ui.Context, e ui.Element, key any, index int) float32 {`

`internal/ui/motion_presets.go:72` — `func motionPopover(c *ui.Context, offsetY float32) ui.ElementTransition {`

`internal/ui/motion_presets.go:80` — `func motionDialog(c *ui.Context) ui.ElementTransition {`

`internal/ui/motion_presets.go:100` — `func motionCollapse(c *ui.Context) ui.ElementTransition {`

`internal/ui/motion_presets.go:108` — `func motionListItem(c *ui.Context) ui.ElementTransition {`

`internal/ui/motion_presets.go:116` — `func motionToast(c *ui.Context) ui.ElementTransition {`

`internal/ui/motion_presets.go:124` — `func motionFeedback(c *ui.Context) ui.ElementTransition {`

`internal/ui/motion.go:44` — `return c.Preferences().ReduceMotion`

`internal/ui/controls_loading.go:22` — `if reduceMotion(c) {`

`internal/ui/tokens.go:94` — `DurBase  = 200 * time.Millisecond`

## 9. 数据与协议变更清单

### 保持不变

持久化 Schedule、Spec、RunState、运行表结构及运行状态不变。draft、选中 tab、筛选、预览序号、dirty、离开确认都只在 UI 内存，不写进 Schedule JSON。保存与显式恢复分开属于适配语义修复，不增加远程可写运行状态。

新增局部能力接口可以沿用 automationRunsStateClient 的可选接口方式，减少扩充巨大 Client 接口和所有 Fake 的成本；生产 app 必须实现，旧客户端不支持预览时显示「预览不可用」，不能自己写第二套 Next 算法。

`internal/ui/automation_runs_ui.go:28` — `type automationRunsStateClient interface {`

`docs/remote-control.schema.json:14781` — `"description": "automations.create 与 automations.update 共用的可写字段；项目只能随 create 指定，运行时状态一律不可写",`

### 推荐唯一新增的 RPC：automations.preview

- 请求：JSON-RPC 2.0；params 为 `schedule`（现有 RemoteAutomationExtSchedule）、`after`（可选 RFC3339，缺省服务端 now）、`count`（可选整数，缺省 3，范围 1..5）。无需 automationID，不写数据库，不发 changed，不获取调度锁。
- 响应 result：`times`（UTC RFC3339 数组，允许空）、`timezone`（实际采用的时区名称）、`after`（实际基准 RFC3339）、`horizonDays`（cron 为 366，其余可省略）。数组不足 count 表示在算法搜索范围内无更多候选，不用 `hasMore=false` 误称永无未来。
- 校验：完整调用 Schedule.Validate；非法输入使用既有 InvalidParams；超长 cron、过大 count、非法 after 均拒绝。服务端为循环设置 context 截止与数量上限，后端已有搜索范围不能被预览放大。
- desktop app 用同一个领域 helper，UI 通过可选预览接口拿结果；远程 handler 调同一 helper。远程旧版本 method-not-found 时不拦住合法旧配置的普通保存，只关闭预览能力。
- schema 增加 request/response/result/params 定义，并加入顶层 oneOf 和对应 JSON-RPC 消息并集；同步 wire Go DTO 和远程注册/处理。不要只改 schema 的 $defs 后漏掉消息入口。
- 调度预览不返回「等待审批 / 等待回执」子状态；这些运行时数据目前没有在协议暴露。
- 重跑复用 `automations.run`，归档复用 `automations.archiveRun`，启停/恢复复用 `automations.setEnabled/reset`；没有新增 rerun RPC。

`docs/remote-control.schema.json:14641` — `"RemoteAutomationExtSchedule": {`

`docs/remote-control.schema.json:15173` — `"const": "automations.run"`

`internal/remote/automation_ext_methods.go:95` — `RegisterMethod("automations.list", handleAutomationsList)`

不包含在本轮：运行快照、rerunOf、事件触发器、日志偏移与分页游标。如未来要精确按历史配置重跑，必须新增运行配置快照及关联字段，同时设计权限、迁移和远程协议，不能只给按钮换名字。

## 10. 分阶段实施计划

顺序按收益和风险排列。阶段零与阶段一先顺序完成，形成稳定边界；阶段二与阶段三此后可并行；阶段四由共享窗口所有者统一整合。阶段二只负责日程文件，阶段三只负责运行文件，避免同时重写 automation.go。

### 阶段零：先修保存语义与草稿一致性（高价值，中风险）

**步骤 01：把「保存配置」与「恢复 / 清失败计数」拆开。**

锚点：

`internal/app/client.go:1952` — `def, err := a.Update(ctx, id, automation.UpdateInput{`

`internal/automation/service.go:359` — `scheduleChanged = true`

`internal/app/remote_automation.go:175` — `updated, err := s.client.UpdateAutomation(ctx, automationID, automationExtInputFromUI(current, enabled), revision)`

`internal/ui/automation.go:709` — `saved, err := s.client.UpdateAutomation(ctx, a.ID, input, a.Revision)`

具体改法：普通 UpdateAutomation 比较 existing.Enabled，仅在状态发生变化时传 Enabled 指针；Service 用 Schedule 的字段语义比较判断 scheduleChanged，Date 比较时间点，不使用 pointer 地址。名字、正文、模型或限额变化不重算 NextRunAt。新增一个局部可选控制接口 `SetAutomationEnabled(ctx,id,enabled,revision)`，生产 app 实现时只传 Service.UpdateInput.Enabled，不携带 Spec；UI 启停/重置和远程 SetEnabled/reset 走此入口，保留显式 Enabled=true 的清失败语义。不要改变普通启用仍清失败计数的既有规则。

验收：只改名称不移动 nextRunAt、不清失败、不恢复暂停/完成任务；恢复与 reset 仍清失败且重算下一次；Model/RunMode/Timezone、MaxRuns、旧 MaxIterations 和 CatchUp 全保留。expectedRevision 必须原样校验。

测试：在现有 service/app/remote 自动化测试中补「名称保存无调度副作用」「同值日程无变化」「真实日程变化才重算」「显式 reset 与普通保存不同」「暂停编辑后仍暂停」。生产控制接口与旧 Fake 回退都要覆盖，不让 Fake 的简化实现掩盖真实调用链。

`internal/automation/service_test.go:376` — `func TestSettlement(t *testing.T) {`

`internal/ui/automation_test.go:373` — `func TestAutomationWorkspace_TogglePause(t *testing.T) {`

**步骤 02：修正本窗口写入的 revision 合并，并建立草稿生命周期。**

锚点：

`internal/ui/automation.go:296` — `as.editRevision = auto.Revision`

`internal/ui/automation.go:732` — `as.enabled = enabled`

`internal/ui/automation.go:567` — `if as.projectID == curPID && as.selectedID == selectedID && as.isNew == isNew && err == nil {`

具体改法：增加草稿基线和 dirty 判断、待离开动作；成功的本窗口启停只在返回 revision 与操作前基线一致时合并对应状态及新 revision，不覆盖用户正文。外部更新仍保留旧草稿 revision 并要求冲突处理。增加编辑会话序号，迟到的保存成功不能覆盖保存发出之后的新输入；保存时禁用编辑器或按序号保留更新后的草稿，二选一，推荐保存中禁用编辑器。离开确认状态先建立，具体弹窗在步骤 04 接入。

验收：本窗口启停后继续编辑可保存；外部更新仍触发真实冲突；切项目、复制、删除、保存迟到都不串对象；已有通知不覆盖 dirty 草稿。

测试：扩展现有草稿版本、项目隔离测试，补「dirty 后启停」「快速换对象后请求返回」「保存中重复点击」「保存中输入策略」「冲突后草稿仍在」。

`internal/ui/automation_test.go:190` — `func TestAutomationWorkspace_RefreshConflictAndDraftRevision(t *testing.T) {`

`internal/ui/automation_test.go:231` — `func TestAutomationWorkspace_ProjectIsolation(t *testing.T) {`

### 阶段一：结果优先的列表、详情与编辑器（最高可见价值，低至中风险）

**步骤 03：只按职责提取现有文件，建立后续并行边界。**

锚点：

`internal/ui/automation.go:33` — `// automationState 维护自动化工作区列表、表单与运行记录的本地状态。`

`internal/ui/automation.go:1109` — `func renderAutomationWorkspaceWithSearch(c *ui.Context, s *chatState, searchText string) {`

`internal/ui/automation.go:1867` — `ui.Column(c.Key("form-sched-type-group")).`

`internal/ui/automation.go:2276` — `ui.Column(c.Key("automation-runs-section")).`

具体改法：automation.go 保留列表与数据协调；从现有块提取到拟新增的 `internal/ui/automation_editor.go`、`internal/ui/automation_schedule_ui.go`、`internal/ui/automation_run_history.go`。automation_runs_ui.go 继续负责已读/归档请求，不重复造 store。约定 editor 只交付草稿，schedule 只修改调度字段并产生预览，history 只消费 runs/selection 与动作回调。提取时不顺手更换 API 或清理其他文件。

验收：行为与现有 key 保持，差异仅文件归属；每个渲染区有明确入口；后续 schedule 与 history 的实现者不必修改同一个大函数。

测试：现有自动化测试不因提取失效；将颜色守卫扩成匹配 automation*.go 的生产文件，防止新文件绕过守卫；弹窗守卫继续全包扫描。

`internal/ui/automation_style_test.go:13` — `files := []string{"approval.go", "automation.go", "attention_inbox.go"}`

`internal/ui/modal_style_guard_test.go:13` — `func TestNoStyledModalOrPopover(t *testing.T) {`

**步骤 04：替换窄卡片，默认详情显示运行记录，编辑改为独立弹窗。**

锚点：

`internal/ui/automation.go:1334` — `Width(240).`

`internal/ui/automation.go:1699` — `ui.Text(c, "从左侧选择一项自动化查看，或点击“新建”创建").`

`internal/ui/automation.go:1844` — `ui.TextInput(c.Key("automation-name-input"), &as.name).`

`internal/ui/modal_drag.go:80` — `func modalDragModal(c *ui.Context, key string, open *bool, fn func()) {`

具体改法：按线框建立二列列表/结果；列表只留名称、状态、下次运行和未读点，复制/删除收进更多菜单。已有对象默认运行记录，配置 tab 只读摘要加编辑；新建/编辑通过 modalDragModal 和单层卡片承载。接入步骤 02 的离开确认，取消后恢复触发按钮焦点。宽窗允许列表约 320 DIP，窄窗改列表/详情层级；阈值用 token 或局部常量统一，禁止散落数值。

验收：常规查看结果无需滚过表单；一眼辨认暂停和失败；840×520 无横向溢出，1140×720 主按钮完整；只有一个页面新建入口；弹窗无黑色外框。

测试：更新 Create/Empty/Summary 测试，使断言对齐新布局；补首次聚焦、dirty 取消确认、宽/窄布局、关闭后焦点恢复、查看历史不创建/更新配置。不得靠删除已有版本隔离测试让布局测试通过。

`internal/ui/automation_test.go:31` — `func TestAutomationWorkspace_Create(t *testing.T) {`

`internal/ui/automation_test.go:298` — `func TestAutomationWorkspace_Summary(t *testing.T) {`

**步骤 05：列表筛选、空状态与文案收口。**

锚点：

`internal/ui/automation.go:1047` — `func filterAutomations(autos []Automation, query string) []Automation {`

`internal/ui/automation.go:1163` — `totalUnread := automationUnreadTotal(unread, visible)`

`internal/ui/automation.go:1223` — `ui.Text(c, "还没有自动化").`

具体改法：增加局部全部/失败/暂停状态筛选与未读条件；失败匹配最近失败或阈值停用，不把手动暂停都算失败。入口角标按项目全量 autos 统计，筛选计数另算。删除空状态长说明，保留「还没有自动化」与「新建」；搜索为空给清除动作，加载失败给重试。排序仍沿用既有优先级，并在动作进行中稳定当前焦点，避免开关落定后行跳走。

验收：搜索和状态条件可组合，过滤不影响入口未读总数；空列表、搜索无结果、加载失败是三个独立状态；不隐藏当前对象的失败原因。

测试：补纯筛选组合、相同时间排序、已完成/暂停区分、未读总数不随过滤变化；更新 Empty/Completed 对应测试。

`internal/ui/automation_test.go:11` — `func TestAutomationWorkspace_Empty(t *testing.T) {`

`internal/ui/automation_test.go:409` — `func TestAutomationCardStatusCompleted(t *testing.T) {`

### 阶段二：可视日程与可信预览（中高价值，中风险；可与阶段三并行）

**步骤 06：实现领域层的有界预览，不改变调度行为。**

锚点：

`internal/automation/schedule.go:23` — `func (s Schedule) Validate() error {`

`internal/automation/schedule.go:266` — `func (s Schedule) Next(after time.Time) (time.Time, bool) {`

`internal/automation/schedule.go:319` — `limit := cursor.AddDate(0, 0, 366)`

具体改法：拟新增 `internal/automation/schedule_preview.go`，暴露有 context、count 上限和固定 after 的纯预览 helper；循环只调用现有 Next 并检查严格递增，返回候选与搜索范围信息。不要拷贝 cron 解析器，不更改 DST 或补跑实现；预览的时间定位与 Validate 使用同一时区逻辑。

验收：六种调度、合法但无候选、count 越界与取消都有确定行为；零存储写入、零 SessionDriver 调用；相同输入与 after 结果相同。

测试：新增预览测试并复用既有时区/DST 用例，覆盖每 90 秒、闰日超出搜索范围、五段 cron 范围/步长、单次过去/未来、count 1/3/5、严格递增、context 取消。原 Schedule.Next 测试必须保持。

`internal/automation/schedule_test.go:329` — `func TestScheduleNextTimezones(t *testing.T) {`

`internal/automation/schedule_test.go:352` — `func TestScheduleNextDST(t *testing.T) {`

**步骤 07：接通本地预览能力与 JSON-RPC 契约。**

锚点：

`internal/ui/automation_runs_ui.go:28` — `type automationRunsStateClient interface {`

`internal/remote/wire/automation_ext_types.go:11` — `type AutomationExtSchedule struct {`

`internal/remote/automation_ext_methods.go:94` — `func init() {`

`internal/app/remote_automation.go:321` — `func automationExtSchedule(sch mcodeui.AutomationSchedule) wire.AutomationExtSchedule {`

`docs/remote-control.schema.json:14641` — `"RemoteAutomationExtSchedule": {`

具体改法：按第 9 节新增 optional preview client、app 桥接、wire DTO、handler、schema 定义和消息并集；优先新增专用 preview 文件，让本阶段不重写阶段零的 app 控制入口。转换复用现有 ScheduleTo/FromUI，不把 UTC 候选改成无时区字符串。老服务 method-not-found 回退为无预览，字段错误仍可由保存后端校验。

验收：本地 UI 与远程相同参数输出相同 UTC 候选；未知字段、非法数量/时间/cron 返回规范错误；preview 不广播 changed、不创建任务、不要求锁；新字段均通过 schema。顺带核对 maxRuns 的旧 maxIterations 镜像上限：当前 schema 为 50 而服务常量为 1000，应把同义旧字段 maximum 同步为 1000，保持已有 maxRuns 0..1000 契约。

测试：在现有 remote/app 自动化测试旁新增 preview 测试，覆盖 JSON-RPC 请求/响应、额外字段拒绝、老服务降级、schema 入口注册、50/51/1000 的规范字段与兼容镜像往返。不得让 preview 依赖 UI 包中的 formatter 算日程。

`docs/remote-control.schema.json:14809` — `"maximum": 50,`

`internal/remote/automation_ext_methods.go:61` — `automationExtMaxIterations = 1000`

**步骤 08：重做日程编辑和错误反馈；先核验日期/时间组件。**

锚点：

`internal/ui/automation.go:452` — `var sched AutomationSchedule`

`internal/ui/automation.go:1939` — `intervalSel := ui.Select(c.Key("automation-interval-select"), &curIntervalLabel, intervalLabels).`

`internal/ui/automation.go:2033` — `ui.TextInput(c.Key("automation-cron-input"), &as.cronExpr).`

`AGENTS.md:12` — ``8. **新组件先查现成实现**：需要新 UI 组件时，先查看 CrychicUI（https://github.com/ELY-Open-Source/CrychicUI，MIT，纯 Go，基于同一个 `github.com/egoist/mygo/ui`，1500 个组件、38 个包）是否已有合适的组件。有合适的就直接复用对应包，不从头自研；没有合适的再自己开发。引入时注意：该库的 `sound` 包默认开启，必须统一关闭或接入 mcode 的提示音配置；弹窗与浮层不使用其 `overlay` 包，仍按第 5 条用 `ui.DialogBase` / `ui.PopoverBase`；接入后要在 dev 版本里确认减少动效（`ReduceMotion`）时会关闭。变更说明里要写明引入的包路径与版本。``

具体改法：在步骤 03 提取后的 automation_schedule_ui.go 中实现类型下拉、当前参数、时区和三次预览；六种类型的 draft 值相互保留；自定义间隔可输入，单位换算只在提交时做。给每个字段单独错误，保存与预览共用一次草稿解析。按第 5.2 节候选读 CrychicUI 源码确认导出 API，真实包路径与固定版本写入实施变更说明；API 或兼容性未确认时保留手输入口，picker 子项明确阻塞，不猜导出名。所有浮层用 appPopover。

验收：仅改名称时原 90 秒、once 秒精度、cron 字符串与旧空时区保持；快速改日程不会显示旧预览；新建过去单次阻止启用；无候选的合法 cron 可暂停保存。heartbeat 不出现可编辑模型/权限设置，已有运行模式不丢失。

测试：更新 PreservesScheduleOnSave、MaxRunsAndCatchUp；新增时区与单次秒精度往返、0/NaN/Infinity 间隔拒绝、非法 cron 就地错误、预览迟到丢弃、不支持预览、无网络不影响已有编辑。picker 如引入，必须测键盘、声音关闭、ReduceMotion。

`internal/ui/automation_test.go:253` — `func TestAutomationWorkspace_PreservesScheduleOnSave(t *testing.T) {`

`internal/ui/automation_test.go:431` — `func TestAutomationWorkspace_MaxRunsAndCatchUp(t *testing.T) {`

### 阶段三：运行历史、再次运行与状态可靠性（高价值，中风险；可与阶段二并行）

**步骤 09：将运行历史做成有焦点的日期列表与结果详情。**

锚点：

`internal/ui/automation.go:2346` — `ui.Column(c.Key("runs-list-col")).`

`internal/ui/automation.go:2356` — `toggleRunDetail := func() {`

`internal/ui/automation.go:2497` — `errText := ui.Text(c, runVal.Error).FontSize(TypeCaption).TextColor(ThemeDanger(c))`

具体改法：在 automation_run_history.go 渲染按日期分组的紧凑列表，状态、开始时间、耗时形成固定列；详情就地替换运行区，不展开卡片里的长文本。明确 manual/catch_up/skipped 来源；摘要、错误可复制；ThreadID 为空隐藏打开会话。增加失败/未读筛选与最近 100 条范围提示；只标记真正打开的 runID 已读。

验收：看失败记录无需经过编辑表单；摘要不会挤掉操作；shared thread 只跳转整段会话；无法提供的历史评估和逐步日志不伪造。切筛选保留可定位焦点。

测试：新增运行详情、日期分组、空摘要、超长错误、无 ThreadID、running 无 FinishedAt、skipped、满 100 条范围提示；保留和更新来源徽标测试。

`internal/ui/automation_test.go:476` — `func TestAutomationWorkspace_RunTriggerBadges(t *testing.T) {`

**步骤 10：接住 RunNow 返回值，完成运行和再次运行闭环。**

锚点：

`internal/ui/automation.go:629` — `func (s *chatState) runAutomationNow(id string) {`

`internal/ui/automation.go:656` — `_, err := s.client.RunAutomation(ctx, id)`

`internal/automation/service.go:509` — `return s.trigger(ctx, auto, s.now(), RunTriggerManual, 0)`

具体改法：请求协调仍放现有 automation.go 的一个入口，history 仅调用回调，整合由阶段一负责人一次接入，避免并行编辑同文件。保存返回的 runID，校验项目/对象后进入新详情；返回已结算失败直接显示错误。再次运行复用该入口；dirty 配置先明确选择；到达 MaxRuns 不自动恢复或归零。UI 预防双击，但仍依赖后端锁与 ErrAlreadyRunning。

验收：一次点击最多一条 manual 记录；旧失败记录不变；暂停状态不因手动运行开启；计数上限正确；切项目后迟到返回不跳错对象；被动实例明确说明本窗口不能启动。

测试：补「RunNow 返回立即失败」「再次运行用当前配置」「双击」「已达 MaxRuns」「暂停手动运行」「dirty 三种选择」「切项目迟到」。运行详情入口测试与后端不重入测试分别覆盖，不只检查按钮文案。

`internal/automation/service_test.go:627` — `func TestRunNowConcurrentWithTick(t *testing.T) {`

`internal/ui/automation_test.go:569` — `func TestAutomationWorkspace_RunNowBlockedWhenPassive(t *testing.T) {`

**步骤 11：修复已读 / 归档的失败回滚与跨对象错误归属。**

锚点：

`internal/ui/automation_runs_ui.go:126` — `func (s *chatState) markAutomationRunRead(automationID, runID string) {`

`internal/ui/automation_runs_ui.go:184` — `func (s *chatState) setAutomationRunArchived(runID string, archived bool) {`

`internal/ui/automation_runs_ui.go:175` — `if _, err := client.MarkAllAutomationRunsRead(context.Background(), id); err != nil {`

具体改法：保存操作前快照、项目/自动化 ID 与请求序号；后端失败在 UI 更新队列中回滚对应对象并显示可重试错误，再拉权威未读数。全部已读失败也给反馈。请求使用有界 context；同一条记录后续成功不被早先请求失败覆盖。保留旧客户端 nil reads 按已读处理的兼容逻辑。

验收：失败后角标与归档可见性回到真实值；多窗口刷新和快速切项目不扣错数；重试后只有一次最终状态。

测试：在新运行状态测试文件补失败注入、先失败后成功乱序、切项目/对象、旧客户端能力缺失、全已读失败。正常成功路径不能额外刷新整个编辑草稿。

`internal/ui/automation_runs_ui.go:76` — `func automationRunUnread(reads map[string]time.Time, runID string) bool {`

### 阶段四：宿主整合、键盘与动效验收（共享改动，统一负责人）

**步骤 12：接通实际搜索入口，移除宿主重复控件。**

锚点：

`internal/ui/kanban_modal.go:287` — `kanbanSearchRow(c, s, ks, &searchQuery)`

`internal/ui/kanban_modal.go:831` — `renderAutomationWorkspace(c, s)`

`internal/ui/kanban_modal.go:306` — `kanbanCountBadge(c, "kanban-modal-task-count", len(p.board.Tasks), nil)`

具体改法：自动化视图把其查询传入 renderAutomationWorkspaceWithSearch；查询状态与看板分开，切视图互不串词；搜索 Label/Placeholder 随视图变为「筛选自动化」。宿主项目选择器保留，删除自动化内重复项目条；自动化视图隐藏看板任务数与看板新建按钮，转接一个自动化新建动作。统一的项目切换经过 dirty 离开处理。

验收：从侧栏、命令面板、任务/自动化目的导航进入均能实际筛选；切回看板保持看板自身查询；自动化无任务数混淆；一套项目与新建入口。

测试：补通过真实 tasks-window/kanban embed 的自动化搜索集成测试，不只直接测 WithSearch；覆盖空结果、清空、项目切换、任务/自动化目的导航以及看板原路径回归。

**步骤 13：修复 Esc 优先级并增加局部快捷键。**

锚点：

`internal/ui/root.go:86` — `if c.Shortcut(0, ui.KeyEscape) {`

`internal/ui/tasks_window.go:58` — `func (s *chatState) closeTasksWindow() {`

`internal/ui/shortcuts.go:39` — `{id: "new-thread", title: "新建会话", group: "会话", keys: "⌘N", mods: ui.Cmd, key: ui.KeyN, bound: true, customizable: true, registered: true},`

具体改法：根视图把 Esc 交给当前任务视图的局部处理后再决定关闭窗口；顺序为浮层 → 编辑/离开确认 → 运行详情/窄窗详情 → 关闭。自动化视图局部接 Cmd+N/Cmd+F/Cmd+S 和列表导航，复用现有键位解析；不要改主聊天 Cmd+N 或吞掉 TextInput 的方向键。真正窗口关闭也接 dirty 检查，不能只保护取消按钮。

验收：Esc 不再直接丢草稿关窗口；所有主要流程可只用键盘完成；主聊天与看板快捷键行为保持。

测试：新增真实任务窗口根路径键盘测试：浮层 Esc、dirty Esc、连续 Esc、文本区上下键、Tab 顺序、Cmd+S 校验、主聊天 Cmd+N、看板原退出路径。检查原生关闭入口是否可取消；如当前 Window API 不支持，明确将「标题栏关闭保护」列为实施阻塞，不用仅拦 Esc 冒充完成。

**步骤 14：统一入口、通知标记与动效守卫。**

锚点：

`internal/ui/sidebar_footer.go:117` — `Key:     "sidebar-automations-btn",`

`internal/ui/palette.go:181` — `id:    "cmd-open-automations",`

`internal/ui/icons.go:236` — `case "zap", "automation", "automations":`

`internal/ui/settings_page_general.go:174` — `ID:      "row-automation-prevent-sleep",`

具体改法：自动化别名统一日历时钟，但保留字面 zap 映射；命令面板增加「新建自动化」且通过同一窗口动作。入口增加未读标记前，先在共享窗口拥有者接通独立于页面打开的项目未读加载/订阅，主窗口和任务窗口都从权威结果派生；未加载不伪显示零。自动化更多菜单可直接定位既有「保持唤醒」设置，使用现有设置导航，不新增配置。按第 8 节逐个挂现有动效，不改 motion_presets.go 的全局参数。

验收：没打开自动化页也能发现已知未读；角标不随页面筛选变动；入口一致且无多余说明；ReduceMotion 切换时现有动画和循环立即停，仍能看见运行状态；应用未运行或系统睡眠不被文案承诺为云端常驻。

测试：入口真实路径与跨窗口未读、图标别名、设置定位；将自动化新文件纳入主题/动效守卫；借用已有 ReduceMotion 测试方式验证静态帧无循环续帧，正常模式有效。运行状态切换、浮层关闭焦点需真实 UI tester 验证，不只检查源码出现某函数名。

`internal/ui/motion_system_test.go:10` — `func TestMotionSystemReduceMotion(t *testing.T) {`

`internal/ui/modal_style_guard_test.go:13` — `func TestNoStyledModalOrPopover(t *testing.T) {`

### 并行与交付约定

- 阶段零负责人拥有 service.go、app/client.go 和控制接口接线，完成后再开始布局调整。
- 阶段一负责人一次性提取 UI 文件，并固定 editor/schedule/history 回调边界；负责 automation.go 的后续小整合。
- 阶段二负责人拥有 schedule_preview.go、automation_schedule_ui.go、preview RPC/schema 与专用测试。
- 阶段三负责人拥有 automation_run_history.go、automation_runs_ui.go 与专用测试；步骤 10 的协调入口由阶段一负责人整合一次。
- 阶段四负责人拥有所有共享窗口文件；与看板重设计负责人协商文件所有权，避免双方同时改 kanban_modal.go/root.go。
- 新测试尽量放功能专用文件；既有 automation_test.go 由阶段一负责人统一更新名称、入口和表单定位，其他阶段提交新增用例，减少同文件冲突。

实施时的定向检查命令（本次未执行；外层命令工具也设 130s 超时）：

- `go test ./internal/automation -run 'Test(Schedule|Missed|Settlement|RunNow|ReportRun|.*Preview)' -timeout 120s`
- `go test ./internal/ui -run 'Test(Automation|ApprovalAutomation|NoStyledModalOrPopover|MotionSystem|MotionLoadingMigration)' -timeout 120s`
- `go test ./internal/app ./internal/remote -run 'Test.*Automation' -timeout 120s`

命令应在每个阶段改动后有针对性执行；只测纯文案变化不需要补实现镜像测试。执行档不得启动应用、访问网络或跑全量套件，除非主代理另行调整工作范围；CrychicUI 联网核验由有网络授权的主代理承担。

## 11. 与看板重设计的共用点与冲突文件

| 共用点 | 双方可能修改的文件 | 协作约定 |
|---|---|---|
| 宿主头部、项目、搜索、任务/自动化目的导航、窄窗 | internal/ui/kanban_modal.go | 高冲突；由共享外壳负责人改一次。看板和自动化各提供内容/动作，不各自复制头部 |
| 独立任务窗口复用、返回主会话、窗口关闭 | internal/ui/tasks_window.go | 高冲突；继续全局唯一窗口，不给自动化另开第二套窗口 |
| Esc、浮层、编辑器退出与根布局 | internal/ui/root.go | 新增的必要关联范围；统一事件所有权，不能双方分别吞 Esc |
| 侧栏入口、未读点、提示 | internal/ui/sidebar_footer.go | 高冲突；看板 review 与自动化 unread 各有来源，视觉组件共用，不混成一个数 |
| 命令面板与快捷键 | internal/ui/palette.go、internal/ui/shortcuts.go | 中冲突；命令通过窗口动作进入，局部快捷键不抢主聊天 |
| 主题、图标、动效 | internal/ui/icons.go、internal/ui/tokens.go、internal/ui/motion_presets.go | 优先只用不改；需要改公共 token 时由一方统一提交 |
| 保持唤醒设置定位 | internal/ui/settings_page_general.go | 通常只引用现有 row ID，不应重复新增开关 |
| 弹窗、浮层外壳 | internal/ui/modal_drag.go、internal/ui/popover.go | 共同复用，首轮不修改；只能 DialogBase/PopoverBase 或现有包装 |

共用不意味着合并两个领域状态。看板任务的 needs_approval/review 与自动化的运行状态不可互换；自动化的记录筛选也不写回看板 searchText。

## 12. 取舍、推荐与待用户拍板（最多五项）

方案取舍：

- **推荐：列表 + 默认结果详情 + 弹窗编辑器。** 比当前列表 + 常驻长表单减少日常滚动；比「列表 / 运行 / 配置」三套全局页面少一层导航。窄窗采用层级返回，避免三栏被挤扁。
- **推荐：结构化日程 + 同源预览。** 比自然语言解析更容易验证，代理也能调用 preview。自由文本解释可以继续由会话完成；不要加入第二套确定性不明的调度文本存储。
- **推荐：基于当前配置再次运行。** 复用现有 API，代价是不能精确重放旧模型、任务和权限。完整重放需要运行快照，应另立任务。
- **推荐：日志跳转现有会话。** 零重复日志存储；代价是共享会话不能定位单轮。先验证是否足够，再考虑 run 事件边界。
- **推荐：先复用本地组件，日期时间 picker 核验后再引入。** 不以未知依赖阻塞最有价值的布局与可靠性修复。

已拍板（2026-11-10，均采纳推荐）：

1. **默认详情是否以运行结果为先？** 推荐是；配置通过 tab 与编辑弹窗进入。 → 已采纳
2. **立即运行是否保留现有计数与调度语义？** 推荐保留：计入 MaxRuns，可能移动下一次；若要求「测试且不影响计划」，必须单独设计新执行路径和 RPC，不能只改文案。 → 已采纳
3. **首轮是否需要直接创建 dedicated/heartbeat？** 推荐不需要，既有任务只读展示并保留；若需要，应另补来源会话选择与可写协议字段。 → 已采纳
4. **首轮是否接受最近 100 条历史与日志跳会话？** 推荐接受；若必须全历史分页或逐轮日志，另增游标/运行事件边界，扩大存储与协议范围。 → 已采纳
5. **自然语言调度是否为首发硬需求？** 推荐不是，首发先做结构化规则与三次预览；若是，必须明确解析失败、歧义确认、时区与可解释回填的独立范围。 → 已采纳

风险与限制：普通保存语义修复会触及 app、service 与远程适配，必须用真实调用链测，不能只依赖 UI Fake。时区与间隔完成后计算的语义必须如实呈现。原生窗口关闭保护 API 尚未核实。跨窗口未读同步不能依赖页面初始化。CrychicUI 已核实包与文件，API 与 mygo 兼容性待实现前确认；竞品当期事实仍待联网核验。

## 13. 用户实机验证步骤（由用户或主代理在 dev 版本执行）

1. 从侧栏、命令面板、任务窗口三种入口打开自动化。确认始终复用同一任务窗口，项目与视图正确，只有一个新建入口。缩到 840×520，再放大到 1140×720，检查长标题、按钮和滚动。
2. 在真实窗口搜索已知名称，再筛失败/暂停/未读；清空搜索。切回看板确认看板搜索未被覆盖，自动化未读数不随筛选缩小。
3. 新建一个巡检任务，只填名称、任务、每天 09:00。检查默认焦点和 Tab 顺序，预览三次日期与时区，再「创建为暂停」。确认不会自动运行。
4. 分别测试每天、工作日、每周、间隔、单次和高级 cron。测试每 90 秒、含秒的已有单次、非法 25:00、`61 * * * *`、无未来候选 cron；检查旧数据仅改名称不会丢秒或时区。
5. 选择一个存在连续失败记录的启用任务，仅改名称并保存。核对失败计数与原 nextRunAt 没变；再显式恢复/清计数，确认只有该动作重置失败。暂停后改任务，确认仍暂停。
6. 编辑正文后切对象、切项目、切看板，按 Esc、取消及标题栏关闭。每条离开路径都应保护草稿；继续编辑后正文仍在。用另一窗口/代理更新对象，再保存确认保留草稿并显示版本冲突。
7. 手动运行暂停任务，立即看到新 runID 的详情，任务保持暂停。快速双击只能产生一轮。再测试立即启动失败、次数到上限与另一个进程持锁，不应有假成功。
8. 打开一条失败运行，复制错误、打开会话、再次运行。确认旧记录不变，新记录来源为手动，采用当前配置。复用会话显示整个会话，不伪装成单轮日志。
9. 打开记录观察未读减少；归档后打开已归档筛选并恢复。通过执行档的失败注入构建验证读状态/归档失败会回滚并可重试；不要求用户破坏实际数据库来制造失败。
10. 测试自动化运行时的审批/输入等待、用户停止、回执缺失、成功和达到失败阈值。检查「运行中」「已停止」「失败」「因失败暂停」可区分；成功/失败通知沿用已有入口。
11. 开启系统 ReduceMotion，在浮层开合、编辑器、增删、运行中、计数变化期间观察；所有位移、缩放、循环都停止，状态和焦点仍清楚。若引入 CrychicUI，关闭提示音后逐个操作日期、时间和开关，确认完全无声音。
12. 暂停/恢复系统睡眠后查看补跑来源与合并次数；一次性任务关闭补跑时应显示已跳过并结束。多实例下被动窗口应说明由其他实例调度，不能宣称全局任务都已暂停。

最终交付要求：执行档提供前后截图或录屏、定向测试结果、协议差异、引入组件的真实包路径和固定版本、声音/ReduceMotion 验证结果。本文没有进行实机验证；竞品联网核验尚未完成，CrychicUI 已核实包与文件、API 与 mygo 兼容性待实现前确认。
