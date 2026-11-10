# 看板 UI 与交互重设计方案

日期：2026-11-10。定位与设计稿，只修改本文件，不包含实现。

推荐：保留服务端五列及生命周期，改成「项目范围 → 看板 / 列表 → 右侧详情」。优先修正范围、草稿和结果反馈，再减少卡片噪声，最后补键盘与批量操作。自动化与任务共用窗口外壳，自动化不再作为看板的第三种展示方式。

## 1. 证据范围与完成状态

已读取 AGENTS.md、任务指定的生命周期文件、UI 入口、动效和 token，补读直接关联的桌面转换层、命令面板、快捷键、协议和测试。已查看相关 git 历史。本文所有现状结论来自静态代码检查，未经运行复现。

本子任务明确禁止访问网络、启动 mcode、执行只读调研的构建和测试。因此未做在线竞品调研；CrychicUI 由主代理于 2026-11-10 联网补齐包与文件清单，见第 5.1 节，导出 API 待实现前读源码确认。第 4 节给出八个产品的交互候选和官方核对入口，第 5 节给出组件核验清单。竞品部分不能被当作截至本文日期的产品实测结果。主代理可在允许联网的环境补齐竞品部分，或者先交付复用仓库组件的阶段。

行号以当前工作副本为准。每个现状痛点与实施步骤均附单行源码锚点，引用保持原文；实施后用符号名重新定位。下文「新增文件」是建议的实现目标，没有现存行号，以对应旧实现锚点定位。

### 约束

- 简单目的、架构、设计与实现；删除口号、重复状态、装饰性说明，让布局表达层次。
- 纯 Go，禁止 cgo；延续现有存储格式和 JSON-RPC 2.0 契约。
- 弹窗使用 ui.DialogBase / modalDragModal，现有 Sheet 的底层也符合约束；浮层使用 ui.PopoverBase / appPopover。禁止 ui.Modal、ui.Popover 和 CrychicUI overlay。外层 panel 只设置尺寸上限和定位，外观与实际尺寸归唯一内层卡片。
- 所有动效，包括循环、交错和组件内部动画，都响应 ReduceMotion；关闭面板、隐藏窗口后停止循环与订阅。

依据：

AGENTS.md:16 `1. I want this to be as perfect as Linear's website or Apple Notes: simple purpose, simple architecture, simple design, simple implementation`

internal/ui/controls_sheet.go:16 `panel := ui.DialogBase(c, p.Open, func(backdrop, panel ui.Element) {`

## 2. 现有领域模型与执行边界

### 2.1 任务、展示列与优先级

TaskStatus 有九个值；DisplayColumn 只有五个值。视觉列不能作为任意可写状态。优先级为 urgent、high、medium、low、none；当前卡片已能呈现五档，首期只补筛选和一致排序，不引入优先级编辑协议。

internal/kanban/model.go:7 `TaskStatusTodo          TaskStatus = "todo"`

internal/kanban/model.go:15 `TaskStatusCancelled     TaskStatus = "cancelled"`

internal/kanban/model.go:54 `DisplayColumnTodo       DisplayColumn = "todo"`

internal/kanban/model.go:33 `PriorityUrgent Priority = "urgent"`

| 持久状态 / 信号 | 当前展示列 | 重设计的呈现和主动作 |
|---|---|---|
| todo，依赖完成 | 待办 | 开始；依赖未完成则显示锁与具体前置任务 |
| in_progress / 乐观派发中的 todo | 进行中 | 区分「启动中」「执行中」；只显示已有可靠信号 |
| waiting | 进行中 | 静态「等待条件 / 子任务 / 人」与已有 WakeAt，不冒充正在执行 |
| needs_approval / awaiting_input | 待你处理 | 打开对应会话或已有决策入口；不能通过拖入生成 |
| rejected / 运行失败信号 / 严重无活动 | 通常待你处理 | 原因与恢复动作；不把所有情况叫「失败」 |
| failed 持久状态 | 待办，优先于 failed attention 标志 | 显示「失败」，仍进入「待你处理」筛选，不擅自改服务端列 |
| review | 待审阅 | 阅读结果、验收标准与改动，接受或退回修改 |
| done / cancelled | 已完成 | 显示「完成 / 已取消」；已取消不可显示绿色成功勾 |

映射依据：

internal/kanban/derive.go:128 `if task.Status == string(TaskStatusFailed) {`

internal/kanban/derive.go:129 `return DisplayColumnTodo`

internal/kanban/derive.go:132 `return DisplayColumnDone`

internal/kanban/derive.go:150 `if task.Status == string(TaskStatusInProgress) || task.Status == string(TaskStatusWaiting) || (isOptimistic && task.Status == string(TaskStatusTodo)) {`

权限来自服务端 Derived：CanStart、CanMove、CanReview、CanApprove。运行中与审批中的任务不能按普通卡片移动；前置任务必须完成。UI 不重新实现一套调度状态机，只把现有权限和合法转移组合成统一动作策略。

internal/kanban/derive.go:47 `canStart := task.Status == string(TaskStatusTodo) && !blocked && !rt.Running && !rt.IsOptimistic`

internal/kanban/derive.go:48 `canMove := !rt.Running && !rt.IsOptimistic && task.Status != string(TaskStatusInProgress) && task.Status != string(TaskStatusNeedsApproval)`

internal/kanban/model.go:107 `func CanTransition(from, to TaskStatus) bool {`

### 2.2 目标与代理执行

目标是任务的组织与调度容器，不是另一类任务卡。GoalStatus：planning → plan_review → running → completed；还包含 plan_failed、paused、cancelled。previewPlan 关闭时可直接 running。规划产生任务、依赖和验收标准；规划会话完成后可继续担任协调员。

internal/kanban/model.go:76 `GoalStatusPlanning   GoalStatus = "planning"`

internal/kanban/planner.go:230 `// 规划完成后把计划会话保留为目标的协调员（D-KB-018）。`

internal/kanban/service.go:1566 `AcceptanceCriteria: formatAcceptanceCriteria(pt.AcceptanceCriteria),`

普通目标默认串行、预览计划开启、自动接受关闭、最多两次尝试。调度器默认每 10 秒推进；审批等待仍可占并发槽；暂停目标只阻止后续派发，不能承诺会立即停止正在运行的会话。取消目标与取消任务也不是同一个动作。

internal/kanban/service.go:1198 `"maxConcurrency": 1,`

internal/kanban/service.go:1200 `"autoAccept":     false,`

internal/kanban/scheduler.go:45 `interval = 10 * time.Second`

internal/kanban/scheduler.go:446 `// 如果 goal 在 (a) 步骤变为 paused，不再启动新任务`

启动任务准备工作树、启动执行会话、记录 ThreadID 和尝试次数。提交结果有 done、failed、waiting、delegated、needs_decision 等语义。会话停止不等于完成：缺回执时可追问两次，然后升级人工；有回执后才按策略送审或接受。总管常驻队列有专门保留和收尾规则，不按普通目标展示「全部完成」。

internal/kanban/service.go:639 `task.ThreadID = &threadID`

internal/kanban/receipt.go:23 `TaskReceiptPrompt = "这一轮还没有提交任务结果。请调用 kanban_submit_result，status 选择 done、failed、waiting、delegated 或 needs_decision，并填写对应结果。不要重做已完成的部分。"`

internal/kanban/receipt.go:27 `receiptMaxAsks = 2`

internal/kanban/scheduler.go:535 `if allCompleted && goal.Status == string(GoalStatusRunning) && !IsStewardQueue(goal) {`

### 2.3 评审和验收

ReviewTask 的 accept 先尝试合并；合并失败可以以「无 Go error、仍为 review、ReviewStatus=merge_failed」返回。reject 实际退回 todo 并记录反馈，不是把状态写成 rejected。requestReview 启动独立 ReviewThreadID。UI 必须读取返回任务状态，不能只看 RPC 是否报错。

internal/kanban/service.go:791 `// 先合并再置 done：合并失败时任务留在 review，不能出现卡片显示完成但代码没进主工作区。`

internal/kanban/service.go:793 `st := "merge_failed"`

internal/kanban/service.go:806 `task.Status = string(TaskStatusTodo)`

internal/kanban/service.go:844 `task.ReviewThreadID = &threadID`

验收标准中的 [命令] 与 [你验] 阻止自动接受。[命令] 的宿主执行能力已有，但桌面详情目前不能据此显示一份真实的验收运行记录。本期展示原文与已有评审结果，不新增「执行验收」按钮，不把阅读过标准伪装成验收通过。后续若做可执行验收与持久人工确认，必须另立协议和存储设计。

internal/kanban/acceptance.go:89 `func hasGatedAcceptance(task store.Task) bool {`

internal/kanban/acceptance.go:101 `func (s *Service) RunAcceptanceCommands(ctx context.Context, taskID string) ([]AcceptanceCommandResult, error) {`

### 2.4 git 历史结论

- f53231c（2026-11-09）已抽出 Sheet、motion_presets；应沿用，避免重写一套弹层框架。落点：internal/ui/controls_sheet.go:11 `func Sheet(c *ui.Context, p SheetProps, body func()) ui.Element {`
- 119a0e6（2026-11-09）把看板加载收口到 LoadingIndicator；骨架替换不能重新引入不受 ReduceMotion 控制的 spinner。落点：internal/ui/kanban_modal.go:877 `LoadingIndicator(c, nil, "正在加载看板…", IconLG)`
- eb148ce（2026-11-09）已处理规划审查跨项目状态保留；重做选中目标时应替换为显式导航，不能再丢目标选择。落点：internal/ui/kanban_modal.go:101 `*ks = kanbanState{watchStop: ks.watchStop, loadID: ks.loadID, detailLoadID: ks.detailLoadID, planReviewSeen: ks.planReviewSeen, planReviewGoalID: ks.planReviewGoalID, viewMode: ks.viewMode, searchText: ks.searchText, overview: ks.overview, selecting: ks.selecting}`

## 3. 现状盘点与根因

根因是领域数据、展示状态与渲染交织在大文件中；同一信息被多个层级重复表达，而动作没有共享的语义与忙碌反馈。现有能力可以复用，主要工作是整理信息与动作入口。

### P01　窗口和视图层级混用

入口包括侧栏、聊天头部和命令菜单，最终打开全局唯一任务窗口；降级路径仍在主窗口开大弹窗。窗口里把「看板 / 列表 / 自动化」放在同一视图轴，后者又嵌入独立工作区。问题：展示方式与功能目的混用，自动化页仍背负任务范围和新建按钮。

internal/ui/tasks_window.go:87 `if tasksWindow != nil && !tasksWindow.IsDestroyed() {`

internal/ui/kanban.go:35 `// kanbanViewAutomations 是分段切换里的「自动化」视图，与看板 / 列表共用本地视图状态。`

internal/ui/kanban_modal.go:831 `renderAutomationWorkspace(c, s)`

### P02　全部项目概览的数据范围不完整

UI 拉取当前 projectID 快照，再拿这份任务切出各项目卡。实际转换层按 projectID 过滤，其他项目容易显示「暂无任务」。概览行点击只切换项目，未直接打开所点任务。

internal/ui/kanban.go:309 `board, err := s.client.KanbanBoard(ctx, pid)`

internal/app/convert.go:585 `if tv.Task.ProjectID == nil || *tv.Task.ProjectID != projectID {`

internal/ui/kanban_modal.go:866 `renderKanbanOverview(c, s, p.visibleBoard.Tasks)`

internal/ui/kanban_modal.go:1132 `s.setWorkspaceProject(project.ID)`

### P03　工具栏过密且五列默认需要横滚

单行工具栏估算宽度含两个创建按钮、多重范围切换、搜索和三段视图。五列最低 270 DIP，加列间距已达 1406 DIP，默认窗口 1140 DIP；显示不了所有列是当前设计的必然结果。横滚本身可保留，但必须能定位当前列并照顾右侧详情空间。

internal/ui/kanban_modal.go:29 `kanbanHeaderSingleRowMinWidth float32 = 128 + 80 + 154 + 140 + 170 + 2*94 + 2*ControlMD + 9*Space4 + 2*Space5`

internal/ui/kanban_modal.go:24 `kanbanColumnMinWidth = float32(270)`

internal/ui/tasks_window.go:108 `Width:                1140,`

### P04　卡片元信息与动作盖过标题

当前项目视图仍显示项目胶囊；列名、状态 badge、attention 标志重复；备注、目标、活动和多排按钮持续占位。attentionFlags 还直接显示英文协议值。卡片的高度随菜单展开，打乱扫描节奏。

internal/ui/kanban.go:2433 `if name := projectNameFor(s, task.ProjectID); name != "" {`

internal/ui/kanban.go:2508 `ui.Text(c, fVal).`

internal/ui/kanban.go:2579 `ui.Row(c.Key("kanban-task-actions-" + task.ID)).`

### P05　会话入口标错并重复

「打开审查会话」实际上使用执行 ThreadID，之后又有「会话」「打开会话」入口。桌面 DTO 未保留 ReviewThreadID，用户难以区分执行与评审证据。

internal/ui/kanban.go:2566 `Text:    "打开审查会话",`

internal/ui/kanban.go:2573 `s.revealThread(task.ThreadID)`

internal/ui/client.go:361 `ID, ProjectID, ThreadID, GoalID, Title, Notes, Status, Priority, Column string`

### P06　空状态和加载不统一

全局空态有解释文案与两个创建按钮；每个空列重复「暂无任务」；列表在搜索无结果判断之前返回，导致搜索无结果可能变成空白。首次加载只有居中指示器，刷新时尚有旧快照则显示文本错误，缺少就地重试。

internal/ui/kanban_modal.go:919 `ui.Text(c, "新建一个任务，或用目标让 Agent 拆解规划").`

internal/ui/kanban_modal.go:948 `if p.viewMode == config.KanbanViewList {`

internal/ui/kanban_modal.go:953 `if p.searching && len(p.visibleBoard.Tasks) == 0 {`

internal/ui/kanban_modal.go:1334 `emptyLabel := "暂无任务"`

internal/ui/kanban_modal.go:837 `ui.Text(c, "刷新失败: "+p.errMsg).FontSize(TypeFootnote).TextColor(ThemeDanger(c))`

### P07　新建会提前丢失草稿，项目选择污染看板范围

新建任务与目标在请求成功前关闭表单并清空；失败后只剩 toast。表单项目选择还直接写 ks.projectID，与主面板依照工作区项目重建的逻辑竞争。快速新建可以 Enter/Esc，但没有请求忙碌防重。

internal/ui/kanban_sheets.go:378 `ks.showNewTask = false`

internal/ui/kanban_sheets.go:380 `ks.newTaskNotes = ""`

internal/ui/kanban_sheets.go:304 `ks.projectID = id`

internal/ui/kanban_sheets.go:555 `ks.newGoalPrompt = ""`

internal/ui/kanban.go:418 `go func() {`

### P08　目标页显示未接线的设置，且自动抢焦点

新建目标的胶囊只展示，自动接受显示选中，但服务端默认关闭。规划结果首次出现自动打开审查弹窗，可能打断搜索或拖拽。目标卡全部铺在任务上方，目标越多，任务可用高度越少。

internal/ui/kanban_sheets.go:480 `kanbanGoalOptionPill(c, "kanban-new-goal-auto-accept", "自动接受", true)`

internal/kanban/service.go:1200 `"autoAccept":     false,`

internal/ui/kanban_modal.go:142 `ks.planReviewGoalID = g.ID`

internal/ui/kanban_modal.go:856 `for _, g := range p.board.Goals {`

### P09　详情叠层、重复状态和多层滚动阻碍评审

详情是 660×620 居中弹窗，正文内另嵌最大 180 DIP 的日志和 220 DIP 的 diff 滚动区。标题下面同时写原始 Status 和视觉列；验收标准、依赖和结构化等待信息没有完整进入桌面 DTO。

internal/ui/kanban.go:2904 `modalWidth := float32(660)`

internal/ui/kanban.go:2956 `ui.Text(c, "状态: "+task.Status).`

internal/ui/kanban.go:2967 `ui.Text(c, "列: "+colLabel).`

internal/ui/kanban.go:3127 `MaxHeight(kanbanDetailLogMaxHeight).`

internal/ui/client.go:363 `Blocked, CanStart, CanMove, CanReview                                   bool`

### P10　评审返回成功不等于验收完成

UI 丢弃返回任务，成功后关闭详情；合并失败留在 review 也可能被当成操作成功。按钮禁用条件未检查 CanApprove，也没有按任务的评审提交忙碌态。

internal/ui/kanban.go:631 `_, err := s.client.ReviewTask(ctx, taskID, decision, feedback, revision)`

internal/ui/kanban.go:659 `ks.detailTaskID = ""`

internal/ui/kanban.go:3253 `Disabled: loading || boardLoading || (conflict && boardErr != ""),`

### P11　拖拽允许高亮与实际可执行性不一致

有原生拖放，移入进行中会调用 StartTask；移入普通 done 列调用 MoveTask；review 拖至 done 可绕开 ReviewTask 的合并语义。DragOver 只排除 needs_you，非法目标也会亮。还没有列内持久排序字段，不能宣称支持自由排序。

internal/ui/kanban_modal.go:1267 `s.startTask(payload.TaskID, payload.Revision)`

internal/ui/kanban_modal.go:1280 `s.moveTask(payload.TaskID, colKey, payload.Revision)`

internal/ui/kanban_modal.go:1287 `if _, over := ui.DragOver[kanbanDragPayload](colBox); over && colKey != "needs_you" {`

### P12　键盘仅覆盖局部，批量只是视觉状态

现有快捷键包括快速新建 Enter/Esc、详情/规划/产物 Esc；全局已有命令面板和打开任务命令。没有完整的任务焦点、相邻导航、状态选择快捷路径。多选入口已主动隐藏，选择条只有清除/完成，不能把它当成现成批量功能。

internal/ui/kanban.go:2237 `s.submitQuickAdd(projectID)`

internal/ui/palette.go:173 `id:    "cmd-open-tasks",`

internal/ui/kanban_modal.go:342 `// 批量动作接入前隐藏入口：多选目前没有任何批量操作，先不渲染「批量选择」按钮。`

internal/ui/kanban_modal.go:753 `Button(c.Key("kanban-selection-clear"), ButtonOpts{`

### P13　需要人处理的信号与入口不一致

底栏仅统计 review / CanReview，审批、输入、failed 和卡住任务可能没有入口提示。任务页已有等待摘要与 WakeAt，而看板卡主要显示 LastActivity / LastFailureReason，代理生命周期表达分散。服务端 Board 未向桌面传独立「评审会话正在运行」字段，不能仅凭 ReviewThreadID 非空画运行中动画。

internal/ui/sidebar_footer.go:254 `if task.Column == "review" || task.CanReview {`

internal/ui/steward_task_page.go:183 `if snapshot.Status == "waiting" {`

internal/ui/kanban.go:2552 `activity := task.LastFailureReason`

## 4. 竞品与开源参考：借交互，不移植产品复杂度

状态：以下为既有公开产品模式的设计参考，未联网核验最新版本；链接是实现前应核对的官方入口，不是本次抓取的引文。开源产品明确覆盖 Plane、Vibe Kanban；不复制它们的前端技术栈。

| 产品与核对入口 | 值得借鉴的具体交互（每项最多三条） | 适配 mcode 的理由 | 不借鉴及原因 |
|---|---|---|---|
| Linear：[文档](https://linear.app/docs)，重点 Views / Issue views / Keyboard shortcuts / Triage | ① Board/List 保留同一查询、选择与分组；② 命令面板和焦点内快捷键调用同一动作；③ 轻量详情保留任务上下文，Triage 式处理队列逐项处理 | 已有 Board/List、⌘K 和服务端 attentionFlags，可直接完善现有路径 | 不照搬团队、周期、估点、标签体系；不把 Triage 新增为持久任务状态；不把设计的右侧详情冒充 Linear 所有版本的固定布局 |
| GitHub Projects：[官方文档](https://docs.github.com/en/issues/planning-and-tracking-with-projects) | ① 同一数据的 Board/Table 呈现；② 行内快速添加；③ 拖放后的状态与详情同步 | 本地任务也需要快速录入和一致快照，减少打开多个表单 | 不做任意自定义字段、公式、Roadmap、大型表格配置；五列已有明确运行语义 |
| Plane（开源）：[仓库](https://github.com/makeplane/plane)、[文档](https://docs.plane.so/) | ① 视图顶栏集中筛选与分组；② 列头加号和紧凑卡片；③ 任务详情集中承载属性、依赖、活动 | 让目标不再挤占任务区；把属性从卡片转到详情 | 不复制空间/模块/周期/分析页；不引入 React/Django，也不直接迁移其组件源码；许可及复用方式须另核验 |
| Vibe Kanban（开源）：[仓库](https://github.com/BloopAI/vibe-kanban)、[产品](https://www.vibekanban.com/) | ① 任务与编码代理执行关联；② 从任务进入执行与代码差异；③ 把执行结束与人工 review 分开 | 与 mcode 的 ThreadID、工作树、review 最接近，优先研究其任务到执行的导航 | 不复制完整终端/浏览器/多尝试 IDE 布局；不把每次尝试扩展成新主页面；安装和资源开销不适合纯 Go 轻界面 |
| Cursor：[文档入口](https://cursor.com/docs)，核对 Background Agents / Cloud Agents | ① 后台任务列表优先标题、状态、最近结果；② 从任务直达代理会话；③ 明确需要输入与执行失败 | 用户关心哪件事需处理，而非每张卡的每个技术字段 | 不新增云沙箱、云机管理和 provider 控制台；不照搬未核实的新版命名或键位 |
| Codex：[官方文档](https://developers.openai.com/codex/) | ① 并行任务保持可扫描的列表；② 打开任务检查结果和 diff；③ review 后才进入完成动作 | 与隔离工作树和验收后合并一致；详情应该围绕结果 | 不默认接受全部变更；不把输出文本当作测试已经通过；不复制云任务执行机制 |
| Devin：[官方文档](https://docs.devin.ai/) | ① 会话状态突出阻塞与需要人回复；② 长任务展示最后活动或结果；③ 提供返回执行上下文的明确入口 | 能减少「看着在跑，其实在等」的判断成本 | 不显示推测百分比、不做实时代理视频面板；没有服务端证据的心跳和 ETA 不展示 |
| Apple Notes / Reminders：[Notes 指南](https://support.apple.com/guide/notes/welcome/mac)、[Reminders 指南](https://support.apple.com/guide/reminders/welcome/mac) | ① 范围、列表、详情的稳定层次；② 留白和标题优先，选中即查看；③ 快速录入与明确的完成反馈 | 用户量小、个人操作频繁，适合轻量原生桌面信息架构 | 不把编码任务做成一按即完成的普通待办；不引入日期、重复提醒、日历、富文本笔记系统 |

推荐取舍：用 Apple 的稳定布局、Linear 的操作一致性、Vibe Kanban 的执行与评审边界；其余只取单项交互。联网复核若发现界面已变化，也不影响本方案基于仓库生命周期的核心决策。

## 5. CrychicUI 复用核验与本地组件

已核实的依赖基线没有 CrychicUI。不得把 GitHub 仓库目录名直接当 Go module path，也不得凭「1500 个组件」推断有某个子包。

go.mod:8 `github.com/egoist/mygo v0.8.4-0.20261107102458-435d9411845d`

CrychicUI 官方核对入口：https://github.com/ELY-Open-Source/CrychicUI 。MIT、sound 默认开启等信息来自 brief 与 AGENTS.md；module、版本与文件清单已由主代理于 2026-11-10 联网核对，见 5.1。

| 候选能力 | 已核实可复用的本地实现 | CrychicUI 待查内容 / 决策 |
|---|---|---|
| 看板列、可拖拽列表、卡片 | github.com/egoist/mygo/ui 的 Column、Scroll、Drag、Drop；internal/ui/kanban_modal.go 的列结构 | 包与文件已核实（见 5.1），API 待实现前读源码确认。存在同等组件则评估直接用，不能先自研另一套拖放框架 |
| 标签 / chip | mcode/internal/ui 的 Capsule、kanbanToneBadge | 优先沿用现有 token；外库若只节省几行且强带主题/声音，不引入 |
| 命令菜单 | mcode/internal/ui 的 buildPaletteItems、renderCommandPalette | 复用全局菜单；不额外做看板专属命令面板 |
| 空状态 | mcode/internal/ui 的 EmptyState | 可直接用，Description 留空；只提供一个有意义的动作区 |
| 骨架屏 | mcode/internal/ui 的 timeline_skeleton.go 提供动画控制参考，不是通用看板组件 | 查实际 skeleton API 与内部循环；未核验前保持 LoadingIndicator，不能为视觉更新抢先写独立骨架库 |
| 弹窗 / 浮层 | mcode/internal/ui 的 Sheet、modalDragModal、appPopover | 无论库提供何种组件，均不用其 overlay 包 |

本地锚点：

internal/ui/controls_empty.go:13 `func EmptyState(c *ui.Context, p EmptyStateProps) ui.Element {`

internal/ui/palette.go:479 `func renderCommandPalette(c *ui.Context, s *chatState) {`

internal/ui/timeline_skeleton.go:14 `const timelineSkeletonMaxWait = 2 * motionSweep`

组件选型的交付要求：已记录 module、文件清单和上游依赖基线；实现前读取对应文件与最小示例，确认导出 API、可固定 commit 和与 mygo 当前版本的兼容性。若引入，统一禁用 sound 或接到 mcode 提示音配置，测试 ReduceMotion 实时切换、隐藏窗口无循环刷新、CGO_ENABLED=0；在变更说明写包路径与固定版本。如果 sound 无法统一关闭、ReduceMotion 无法控制或 mygo 版本冲突，明确记录拒绝理由并沿用本地组件。API 与兼容性未确认前，没有「已确认可引入」的 CrychicUI 导出清单。

### 5.1 联网核验结果（2026-11-10）

- module：`github.com/ELY-Open-Source/CrychicUI`。仓库没有 release 或 tag，只有 main 分支，引入时必须固定到具体 commit。
- 版本基线：CrychicUI 的 go.mod 依赖 `github.com/egoist/mygo v0.8.3`，要求 Go 1.27.3。mcode 的 go.mod:8 使用 `github.com/egoist/mygo v0.8.4-0.20261107102458-435d9411845d`；兼容性在引入时用 `CGO_ENABLED=0 go build` 验证。
- 共用底层包：sound、icon、kit；另有 overlay，按 AGENTS.md 不使用。
- project 包文件：board.go、task_card.go、task_row.go、quick_add.go、priority.go、status.go、dependencies.go、subtasks.go、comments.go、checklist.go、labels.go、timeline.go。

候选对应关系：

| 看板能力 | CrychicUI 候选文件 |
|---|---|
| 看板列 | project/board.go |
| 卡片 | project/task_card.go |
| 列表行 | project/task_row.go |
| 快速新建 | project/quick_add.go |
| 优先级 | project/priority.go |
| 状态 | project/status.go |
| 依赖 | project/dependencies.go |

实现前必须读上述文件的导出 API，确认是否支持拖放、ReduceMotion 和关闭 sound；不把猜测的导出名写入代码或 go.mod。

## 6. 目标信息架构与文字线框

### 6.1 共享窗口外壳

窗口名称推荐「任务」。顶部目的导航为「任务」「自动化」，二者有独立选中项、数据和订阅；保留现有单例窗口，不新增窗口。任务内部再选择看板或列表。侧栏原有任务、自动化入口仍直达对应目的。窗口复用时保留各自视图、搜索、滚动与详情；打开入口不重置用户刚选的展示方式。

    任务窗口                                     [任务] [自动化]
      [全部项目 ▾]       [看板 | 列表]      [搜索] [筛选] [＋ ▾]
      [全部] [待你处理 3] [目标 ▾]
      内容区                                  可选的右侧详情

「＋」默认新建任务，下拉提供新建目标。没有第二排副标题、总任务数装饰或常驻刷新按钮；刷新放在错误条和菜单。项目选择器包含「全部项目」，替代当前相互叠加的范围胶囊与项目下拉。新建时需明确真实项目，选择「全部项目」也能打开新建表单并选项目；无有效项目时提供「添加项目」动作。

### 6.2 看板

默认保留五列顺序，不引入第六个持久状态。列头弱底色、图标、名称和计数，卡片使用唯一表面层级，不做「卡片套重底卡片」。列宽维持约 270–300 DIP，水平滚动可见；窄宽度时有列定位菜单，键盘左右切换可把目标列滚入可视区。不能压成五条不可读窄列。

    待办 4          进行中 2         待你处理 1       待审阅 2        已完成 6
      任务标题        任务标题         任务标题          任务标题        任务标题
      依赖 2          等待子任务       需要回复          已有评审结论    完成摘要
      ＋ 任务

每张卡只保留标题与一条必要信号：无异常时可无第二行。当前项目下不显示项目标签；混合项目时显示项目名；目标筛选内不再重复目标名。优先级用小图标与可访问名称，none 不显示。只在状态与列不同或有行动需要时显示状态词。「开始」「处理」「审阅」仅保留一个上下文主动作，出现在 hover、键盘聚焦或触摸可用的固定菜单中；更多动作统一到右键 / 更多浮层。

已完成列默认折叠，保留名称和数量；点击展开完整数据，不隐式删除旧卡，不引入归档策略。取消用中性色与「已取消」，不能使用成功色。搜索命中的已完成卡自动展开其列；清除搜索后恢复用户原折叠偏好。

### 6.3 列表

    待办 4
      优先级    任务标题                        依赖 2
    进行中 2
      状态图标  任务标题                        等待子任务        项目
    待你处理 3
      状态图标  任务标题                        需要回复          项目
    ＋ 任务

列表与看板使用相同数据、筛选、动作和五列分组顺序；用户选择「待你处理」筛选后，仅显示需处理项目。分组可折叠，行不再套卡片，不重复「更新于」长时间戳；需要时在详情显示时间。组内排序首期固定为优先级降序、UpdatedAt 降序、ID 升序，更新不能重排正在编辑/拖拽的任务；暂停交互结束后应用一次排序。

### 6.4 右侧详情

宽布局在内容区右侧占约 440–520 DIP，左侧仍可点另一张卡。窄布局（可用内容宽度小于约 980 DIP，具体由布局测试校准）用 DialogBase 内的单卡 sheet；保持同一内容函数和焦点规则。右侧面板本身是布局子树，不用弹窗 API；一旦变成浮层或遮罩 sheet，必须使用规定的基础容器。

    [返回 / 关闭]       任务标题                         [⋯]
    唯一状态           所属目标 / 项目（可点击）
    [结果 | 改动 | 会话]
      结果摘要
      需求
      验收标准（原文；[命令] / [审阅] / [你验]）
      前置依赖（可点击标题，缺失项明确显示）
      评审反馈 / 失败原因 / 等待条件与唤醒时间
    固定底部动作：[开始] 或 [打开会话] 或 [退回修改] [接受]

默认 tab：待审阅任务打开「改动」，其他任务打开「结果」。每个 tab 只有一个主滚动区，文件 diff 可折叠但不嵌固定高度滚动窗。「会话」内区分执行与评审，后者只有真实 ReviewThreadID 时出现。保留已有 revealThread，把执行上下文交给主窗口，任务窗口不被关闭；返回后选中项、过滤和滚动保持。

### 6.5 目标

目标放在筛选器下拉中，不把所有目标卡铺在看板上。选择目标后，标题区显示「目标名 · 已完成/总数」和符合当前状态的动作。点击目标名进入详情：规划摘要、按依赖层次的任务列表、产物、暂停/继续、重新规划与取消。目标详情与任务详情使用同一位置，导航栈允许「目标 → 任务 → 返回目标」，不叠三层 modal。

规划完成时只更新「待你处理」计数与目标状态，不自动弹窗。用户点击「审阅计划」后看依赖层次和验收标准，确认才 StartGoal。目标暂停按钮的行为仍是停止后续派发；其 tooltip 或确认处精确说明，不增加常驻说明段。重新规划前就地展示将替换的未执行任务数量，保留已有运行与完成结果；请求失败保留反馈。

### 6.6 全部项目与状态反馈

「全部项目」直接使用同一看板 / 列表，不再用六条截断的项目卡。打开任务详情不必先切主会话项目；详情返回源列表。失去权限、任务被删除或项目消失时显示明确状态和返回动作，不能静默关掉用户正在阅读的详情。

| 场景 | 目标反馈 |
|---|---|
| 首次加载 | 保持顶栏与列布局；核实可复用组件后用静态骨架，之前沿用统一 LoadingIndicator |
| 后台刷新 | 保留内容、选择、草稿、滚动与焦点；不反复播放入场动画 |
| 无项目 | 「添加项目」单一动作 |
| 当前项目无任务 | 「新建任务」主动作，新建目标在同一新建菜单内 |
| 空列 | 保留列头和计数；待办有加号，其余不重复写「暂无任务」 |
| 筛选无结果 | 「未找到任务」与「清除筛选」；看板/列表完全一致 |
| 首次失败 | 内容区错误、重试；错误详情可展开，避免长路径撑满布局 |
| 刷新失败 | 顶部窄条「刷新失败 · 重试」，旧内容继续可读 |
| 写入中 | 对应任务/表单禁用重复动作，保留文本；不冻结整个看板 |
| revision 冲突 | 保留草稿，重新读取并展示冲突；由用户在最新内容上重新执行，不能自动重放写操作 |

## 7. 交互清单

### 7.1 键盘、焦点与可访问性

复用现有快捷键系统与命令面板，先核对自定义快捷键覆盖，再注册任务窗口内的绑定。下表是建议默认值，Windows/Linux 用项目既有主修饰键映射，不把 Cmd 字符串硬编码进动作逻辑。

| 按键 | 作用 / 生效范围 |
|---|---|
| ⌘K | 现有命令面板；增加新建任务、新建目标、筛选需处理、切换看板/列表及当前任务可用动作 |
| ⌘F | 任务窗口激活时聚焦搜索；聊天中维持现有会话查找 |
| N | 列表/看板拥有焦点且不在文本输入或输入法组合时快速新建任务；无项目则先选项目 |
| Shift+N | 相同作用域下新建目标；不覆盖输入文本 |
| ↑ / ↓，可选 J / K | 在当前可见任务序列移动焦点；长列表自动滚入可视区；J/K 仅非输入焦点有效 |
| ← / → | 看板中切列并定位附近任务；输入框、diff 选文、浮层优先消费自己的按键 |
| Enter | 打开聚焦任务；快速新建中提交单行标题 |
| Space | 聚焦任务切换选中；文本区保留空格输入 |
| Shift+点击 / Shift+↑↓ | 按当前可见顺序范围选择，不跨隐藏筛选结果 |
| ⌘A | 仅任务集合拥有焦点时选择当前可见项；输入框中仍全选文字 |
| ⌘Enter | 新建表单提交；评审场景只在显式动作按钮聚焦时执行，不能在反馈框误接受 |
| Escape | 逐层关闭：当前菜单 → 输入编辑/选择 → 详情 → 主窗口内的任务 sheet；独立任务窗口不因一次 Esc 直接退出 |
| Tab / Shift+Tab | 到搜索、筛选、新建、任务集合、详情操作；集合内部方向键导航，避免每张卡所有按钮都进 Tab 序列 |

不新增全局 ⌘1/2 切视图，已有 ⌘1…9 直达会话；不默认给删除、接受、跳过设置单键快捷键。焦点和选中分开保存，刷新按任务 ID 保留；任务消失时移至邻近任务并提示，不随机跳到第一项。所有纯图标有可访问名称；颜色、动画和 hover 均不是唯一状态线索。

internal/ui/shortcuts.go:56 `{id: "select-thread-1-9", title: "直达第 N 个会话", group: "会话", keys: "⌘1…9"},`

### 7.2 拖拽与动作语义

统一策略函数供卡片菜单、右键、键盘、拖拽调用，输入是最新 KanbanTask、目标意图和忙碌状态，输出可用性、动作与阻塞原因。允许性仍以服务端权限和 CanTransition 为基础；UI 可以更保守。

| 意图 | 处理 |
|---|---|
| todo → 进行中 | 仅 CanStart 且无实际依赖阻塞时调用 StartTask；检查具体阻塞原因，不因任意 attentionFlag 一刀切禁止重试 |
| 任意 → 待你处理 | 不接受拖放，列由运行信号派生；hover 不高亮，释放不发 RPC |
| review → 已完成 | 打开详情「改动」，聚焦接受按钮；不直接 MoveTask(done)，读完并确认后 ReviewTask(accept) |
| todo → 已完成 | 仅无执行记录的普通待办允许「标记完成」；有执行/评审证据或验收标准时走审阅流程，不能绕过合并 |
| 运行中 / 审批中 → 待办 | 禁止普通拖放；使用明确「中断并重置」动作，确认后调用现有救援路径 |
| done / cancelled / failed → 待办 | 按 CanMove 和合法转移重新打开；不自动启动 |
| 同列拖动 | 不改变顺序；显示当前按优先级/更新时间排序，不能出现可插入占位暗示 |

只高亮真正可用的落点。拖拽预览显示标题即可，不复制所有 badge；视觉随指针立即变化，不加延迟。提交期间原任务显示忙碌标记，等返回快照确认后迁移；失败回到原位置。失效 revision 保留原选择与详情，提示刷新，不自动重试。鼠标操作之外必须可通过菜单完成全部等价动作。

### 7.3 快速新建与批量

- 待办列加号、列表末尾加号与 N 打开同一个快速输入。Enter 成功后保持输入，可连续新建；Escape 退出，失败保留原文并就地重试。完整表单含项目、标题、需求；标题可从需求首行生成，延续现有能力。
- 表单的项目是独立草稿字段，不改当前看板范围。保存期间锁定提交按钮；只在成功且草稿版本仍匹配时清理，旧请求不得清掉用户后来输入的新草稿。
- 目标表单删掉未接线选项和口号，仅保留项目、目标输入与「规划」。以后真要调整策略，必须接已有服务端策略并给真实值，不显示假开关。
- 批量首期仅提供「重新打开为待办」「删除」，基于每个任务当前权限逐项计算。无批量接受、启动、救援、跳过，避免一次触发多个执行/合并副作用。
- 选择条只在确实有操作时出现；计数、动作、清除。混合选择显示可操作数，不静默忽略不可操作项；删除弹窗明确数量和限制。
- 复用单任务 API 串行提交，一项失败继续处理独立项，结束显示成功/失败数量；保留失败项选中，可查看具体原因。窗口关闭后停止尚未发送的批量请求；已提交的服务端任务不假装撤销。没有事务原子性承诺。

### 7.4 代理运行与评审

卡片最多一条主信号，优先级为「需要用户行动 → 失败/无活动 → 等待 → 最新结果」。attentionFlags 翻译为具体中文：等待审批、需要回复、无活动、待审阅、执行失败；详情仍可看原始诊断值，不在主卡堆协议字段。

「待你处理」是只读筛选谓词：needs_you 列、failed/rejected 状态、awaiting_approval / awaiting_input / failed / stuck 标志、WaitKind=human 的 waiting，以及具备人工评审权限的 review；另合并 plan_review、plan_failed、需要处理原因的 paused 目标，按目标/任务 ID 分别去重。显示「自动评审中」必须有可靠运行信号；仅有 ReviewThreadID 时只能写「已发起评审」并提供会话入口。不能用 UpdatedAt 作为真实代理心跳，也不提供百分比和 ETA。

服务端现有 CanReview/CanApprove 不保证精确描述独立评审线程运行状态；本期能显示的是权限与已知请求状态，不为填满 UI 新增运行字段。若后续要求精确「评审中」，另评估服务端权能推导，不能在客户端推测。

评审固定底栏：退回修改、接受。只在 CanApprove 且非忙碌、非未解决冲突时允许接受；退回修改用多行反馈并保留草稿。接受后以返回 Status/ReviewStatus 为准：done 才提示完成；merge_failed 留在详情，展示错误和工作树保留信息，允许核对后重试。requestReview 后保持详情，出现真正 ReviewThreadID 入口，不自动标记通过。

## 8. 动效与 token 清单

所有默认间距、字号、圆角、颜色采用现有 token；字体经过 scaledFont，颜色经过 Theme*，发丝线用 Hairline。底色只分窗口、面板、交互层，不为每种元信息再加一层有色卡片。

internal/ui/tokens.go:74 `TypeCaption   float32 = 11`

internal/ui/tokens.go:95 `DurSheet = 280 * time.Millisecond`

| 状态变化 | 复用预设及锚点 | 正常行为 | ReduceMotion 开启 |
|---|---|---|---|
| 右侧详情进入/退出 | motionSidePanel，internal/ui/motion_presets.go:88 `func motionSidePanel(c *ui.Context, fromLeft bool) ui.ElementTransition {` | 280ms 侧向进入，关闭 200ms；同位置切换任务不重复滑出再滑入 | 直接到最终位置，不延迟内容和焦点 |
| 新建、窄屏详情、删除确认 | motionDialog，internal/ui/motion_presets.go:80 `func motionDialog(c *ui.Context) ui.ElementTransition {` | 轻弹簧和淡入，外层无额外外观 | 立即显示/关闭，不缩放 |
| 筛选、项目、更多菜单 | motionPopover，internal/ui/motion_presets.go:72 `func motionPopover(c *ui.Context, offsetY float32) ui.ElementTransition {` | 小幅抬起与淡入 | 立即到终点 |
| 目标摘要、分组、完成列展开 | motionCollapse，internal/ui/motion_presets.go:100 `func motionCollapse(c *ui.Context) ui.ElementTransition {` | 高度折叠；未知内容高度复用 motionUnfoldBody | 直接完整展开/收起，隐藏内容不留可聚焦节点 |
| 新建成功、卡片跨列、列表删除 | motionListItem，internal/ui/motion_presets.go:108 `func motionListItem(c *ui.Context) ui.ElementTransition {` | 短淡入/退出，只作用实际增删，稳定任务 ID | 直接更新布局，不留退出占位 |
| 首次可见卡片 | motionEnter，internal/ui/motion_presets.go:64 `func motionEnter(c *ui.Context, e ui.Element, key any, index int) float32 {` | 限首屏与最多八级交错，刷新不重播 | 无交错，opacity=1 |
| hover、焦点、选中与合法落点 | motionFeedback，internal/ui/motion_presets.go:124 `func motionFeedback(c *ui.Context) ui.ElementTransition {` | DurSnap 颜色变化，边框宽度稳定，焦点环立即可见 | 颜色立即变化，焦点可见 |
| 按钮按压 | motionPressScale，internal/ui/motion_presets.go:21 `func motionPressScale(c *ui.Context, e ui.Element, key any, pressed bool) float32 {` | 仅按钮轻压，不让整张卡 hover 缩放扰乱对齐 | scale=1 |
| 列计数和目标完成数 | motionNumberProgress，internal/ui/motion_presets.go:43 `func motionNumberProgress(c *ui.Context, e ui.Element, changed bool) (progress, fade float32) {` | 仅实际变化时更新，频繁刷新合并变化，避免数字持续滚动 | Jump 到最终数字 |
| 加载 / 有证据的运行状态 | motionLoop，internal/ui/motion_presets.go:57 `func motionLoop(c *ui.Context, e ui.Element, key any, period time.Duration, ease ui.Easing) float32 {` | 复用 LoadingIndicator；只在可见且加载/执行中循环，不给等待状态旋转 | 静态图标与文字，不调用 Loop、不持续请求重绘 |
| 写入失败/成功反馈 | motionToast，internal/ui/motion_presets.go:116 `func motionToast(c *ui.Context) ui.ElementTransition {` | 低频完成反馈；需恢复的错误留在表单/详情 | 即时出现，保留可读时长，无移动 |

拖拽跟随指针和滚动不加弹簧；ReduceMotion 不妨碍直接操纵。运行中切换系统偏好也要立刻终止现有循环，而非只影响下次打开。优先复用预设已有保护，不在每个组件散落新动画参数。

## 9. 分阶段实施计划

顺序按价值和风险安排。阶段 0 是共享接线与拆分；完成后阶段 1 的适配/读写、阶段 2 的卡片/布局、阶段 3 的详情、阶段 4 的键盘可按文件所有权并行。最终统一接入和阶段 5 集成必须串行。不是把同一个 kanban.go 分给多个人同时改。

### 阶段 0：固定语义和文件边界

**0.1　核实组件与冻结小接口。**

锚点：go.mod:8 `github.com/egoist/mygo v0.8.4-0.20261107102458-435d9411845d`

改法：按第 5.1 节读取 CrychicUI 候选文件的导出 API；记录可复用组件、固定 commit、sound 关闭方法和 ReduceMotion 能力。API 或兼容性未确认时沿用既有组件，不编造「组件不存在」结论。冻结只含 board 快照、任务 ID、视图查询、动作回调的最小接口；不要引入事件总线或第二套 store。

验收：组件选择可追溯；新依赖不提升 cgo 要求；没有核实前不引入新第三方包、不自研它可能已有的复杂组件。

测试：若引入新包，在最小适配测试验证无声音、减少动效、当前 mygo 编译兼容；若不引入不新增无意义测试。记录依赖变更，CGO_ENABLED=0 构建由实现者按阶段范围执行。

**0.2　机械拆出职责，保留行为与测试。**

锚点：internal/ui/kanban.go:146 `type kanbanState struct {`

锚点：internal/ui/kanban_modal.go:254 `func renderKanbanModalHeader(c *ui.Context, s *chatState, ks *kanbanState, p kanbanModalBodyProps, width float32) {`

改法：集成负责人一次性把状态/加载/写入放到新增 internal/ui/kanban_state.go、kanban_data.go；动作策略放到 kanban_actions.go；卡片/列表/列放到 kanban_cards.go；详情内容放到 kanban_detail.go；目标内容放到 kanban_goals.go；保留 kanban_modal.go 管挂载和框架、kanban_sheets.go 管创建。现有 kanban.go 可保留共享纯辅助，避免同包重复定义。先机械移动，下一阶段再改行为。

验收：视图、调用次数、watch 释放和 Escape 行为保持；每个大文件有明确唯一责任人。新增文件名是本方案建议，需在分工时一次冻结。

测试：运行既有 TestKanbanWorkspace、TestKanbanHeaderButtonsStayWithinWindow、TestNoStyledModalOrPopover；把 kanban_style_test.go 文件名单扩展到拆出的全部生产文件，不复制测试到多个文件。

### 阶段 1：修正数据、草稿与结果反馈

**1.1　补齐桌面 DTO，沿用现有协议字段。**

锚点：internal/ui/client.go:360 `type KanbanTask struct {`

锚点：internal/app/convert.go:444 `func KanbanTaskToUI(t store.Task, d kanban.Derived) mcodeui.KanbanTask {`

改法：KanbanTask 增加 AcceptanceCriteria、ReviewThreadID、ReviewFeedback、ReviewStatus、CanApprove；转换层从已有 store.Task 和 Derived 映射。需要显示目标真实策略时再补 KanbanGoal 对应已有字段；首期删除假选项，不增加策略编辑。同步 FakeClient 与已有 DTO 构造处，旧值为空时有安全展示。

验收：执行/评审会话不混淆，标准和合并失败原因到达桌面；不更改 TaskStatus、DisplayColumn、数据库或 RPC 字段。

测试：在 internal/app/client_test.go 扩展 TestKanbanBoardToUI_FilterAndProgress，覆盖 nil 会话、空标准、merge_failed、CanApprove=false 及项目过滤；在详情新测试覆盖新增 DTO 的渲染，而非只测赋值。

**1.2　把范围变成明确查询，并修复全部项目。**

锚点：internal/ui/kanban.go:286 `func (s *chatState) loadKanban(projectID string) {`

锚点：internal/ui/kanban.go:261 `if s == nil || s.client == nil || projectID == "" {`

锚点：internal/ui/client.go:1074 `// KanbanBoard 获取看板数据，projectID 为空返回全部。`

改法：拆出的数据层区分「尚未选择项目」与「全部项目范围」，后者合法调用 KanbanBoard(ctx, "")；watch 也允许全部范围。查询含 scope、projectID、goalID、attentionOnly、search、priority；看板与列表只消费一次过滤/排序结果。以请求序号+范围标识拒收过期返回，关闭停止订阅；watch 触发的重复刷新合并，避免并发拉取风暴。先保证真实范围，再移除旧 overview 项目卡渲染。

验收：p1、p2 和无项目任务在全部范围可见且不重复；切项目时不会闪回旧任务；无项目不阻断全部范围；目标过滤与搜索计数一致。

测试：新增 TestKanbanScope_AllProjects、LateResponseIgnored、WatchClosed、QuerySharedByViews；FakeClient 需真实按参数过滤并可控制返回顺序，不能总返回同一全量看板掩盖缺陷。可在新增 kanban_data_test.go 归该负责人维护。

**1.3　修复草稿隔离与写入忙碌态。**

锚点：internal/ui/kanban_sheets.go:347 `func submitKanbanNewTask(s *chatState) {`

锚点：internal/ui/kanban_sheets.go:534 `func submitKanbanNewGoal(s *chatState) {`

锚点：internal/ui/kanban.go:402 `// submitQuickAdd 提交待办列底部的快速添加，成功后清空草稿并收起输入框。`

改法：新建项目、标题、需求、目标输入独立保存；提交只标记该表单 busy，不提前关闭清空。成功且草稿版本匹配才清理；失败表单内显示错误和重试。快速添加成功保持输入以连续创建。删除假策略胶囊、口号和不准确说明；提交期间防双击/Enter 重复请求。数据层负责返回，表单层负责展示，用阶段 0 冻结回调接口连接。

验收：失败不丢草稿，表单改项目不改看板范围；连续 Enter 只发一个进行中请求；切项目后的旧返回不清新草稿；完整表单保留从需求首行生成标题能力。

测试：在新增 kanban_creation_test.go 覆盖失败/重试、双提交、项目隔离、旧响应、备注生成标题与快速连续新建；更新旧空态与目标表单文案断言，保留可访问标签校验。

### 阶段 2：做清楚看板、列表和目标层次

**2.1　简化窗口框架和任务工具栏。**

锚点：internal/ui/kanban_modal.go:319 `// 看板 / 列表 / 自动化：只影响当前会话，不写回配置`

锚点：internal/ui/kanban_modal.go:840 `// 3. 目标区域 (Goals)`

改法：按第 6 节区分窗口目的导航与任务展示方式；任务区只保留项目选择、Board/List、搜索、筛选、新建菜单。目标入口做下拉筛选及选中目标摘要，移除全量目标卡占位。框架提供右侧详情插槽与窄屏 sheet 位置；先接旧详情内容再替换。搜索无结果判断移到看板/列表分支之前；全部范围使用阶段 1 全量快照。

验收：840×520、1140×720、1440×900 下操作不溢出、标题可读；隐藏筛选菜单不留下可聚焦控件；没有目标时界面无空目标区。

测试：扩展 internal/ui/kanban_modal_test.go 的 TestKanbanHeaderButtonsStayWithinWindow，覆盖三种尺寸和长项目名；新增两视图空搜索一致性、目标筛选后任务计数和详情布局测试。

**2.2　精简卡片，统一列表和状态词。**

锚点：internal/ui/kanban.go:2327 `func renderTaskCard(c *ui.Context, s *chatState, ks *kanbanState, task KanbanTask, st kanbanTaskCardState) {`

锚点：internal/ui/kanban.go:2140 `func renderKanbanListRow(c *ui.Context, s *chatState, task KanbanTask) {`

改法：由拆出的 kanban_cards.go 单独负责，标题优先、单条主信号、上下文主动作和更多菜单。删除当前项目标签、同列重复状态、备注常驻预览和多排动作；混合范围才显示项目。attention 翻译集中到纯展示函数；waiting 使用 ResultSummary/WaitKind/WakeAt；failed 与 cancelled 保留真实语义。列表用分组行而非带边框卡片，Board/List 共用查询与排序，已完成列折叠可恢复。

验收：普通卡片无英文枚举和重复状态；仅等待的任务不旋转；取消不呈成功色；一个任务同一动作只有一个主入口；长标题最多两行、完整标题可访问。

测试：新增 kanban_cards_test.go 覆盖九种任务状态、混合范围项目名、五档优先级、标志中文映射、取消配色、筛选后的完成列展开；更新 kanban_style_test.go 检查新文件继续使用 Theme* / token；保留现有 blocked/start/delete 行为测试。

**2.3　把拖放与菜单接到同一动作策略。**

锚点：internal/ui/kanban_modal.go:1263 `if payload, ok := ui.Drop[kanbanDragPayload](colBox); ok {`

锚点：internal/ui/kanban.go:1065 `func moveTargetsFor(from string) []moveTargetOption {`

改法：由 kanban_actions.go 计算 canStart/canMove/CanTransition、当前 busy、是否有执行/验收证据及目标意图。DragOver 和 Drop 使用同一结果；review→done 只导航审阅，不能 MoveTask(done)。菜单与快捷键同样禁止绕过 ReviewTask；needs_you 只读，不支持同列排序。移除按任意 attentionFlags 禁止 todo 重试的客户端特例，保留实际依赖与运行权限检查。

验收：非法落点无高亮、无 RPC；合法动作携带原任务 revision；操作中不重复发起；拖拽不会跳过评审/合并。依赖阻塞能打开具体前置任务。

测试：更新 internal/ui/kanban_test.go 中 DragTransitions、DragRejectsInvalidTransition、MoveTask、BlockedStartShowsReason；新增 review→done 不产生 MoveTask、只打开审阅和过期 revision 拖放测试。覆盖鼠标菜单与拖拽意图相同的表驱动用例。

### 阶段 3：详情、目标与评审闭环

**3.1　实现保持上下文的任务详情。**

锚点：internal/ui/kanban.go:2847 `func renderTaskDetail(c *ui.Context, s *chatState, ks *kanbanState) {`

锚点：internal/ui/kanban.go:684 `func (s *chatState) loadTaskDetail(task KanbanTask, reset bool) {`

改法：kanban_detail.go 提供同一详情内容函数，宽布局嵌入右侧、窄布局挂到 DialogBase 内唯一 sheet。结果/改动/会话 tab 每页一个主滚动区，补标准、依赖、反馈、等待信息。保留现有 10s 详情读取上限与 loadID 拒收旧响应；切任务取消旧读取，日志/diff 分区呈现加载错误和重试。元信息 revision 更新不清空已成功内容；按选中 tab 延迟加载，禁止每次活动更新都重置整个详情。用真实 ReviewThreadID 打开评审会话。

验收：点不同任务只替换详情内容；左右浏览和关闭回到原焦点，滚动/查询不丢；无会话也能看需求与标准；任务被删除有明确返回状态；打开会话不关闭任务窗口。

测试：迁移并保留 OpenTaskDetail、DetailDiffFailureReason，新增宽/窄同内容、执行与评审路由、A→B 慢响应、刷新保留反馈/滚动、单区失败重试和任务消失测试，放新增 kanban_detail_test.go。

**3.2　按返回任务状态完成评审，不按 error 推断成功。**

锚点：internal/ui/kanban.go:631 `_, err := s.client.ReviewTask(ctx, taskID, decision, feedback, revision)`

锚点：internal/kanban/service.go:799 `break`

改法：保留 ReviewTask 返回值；accept 只有 done 才执行完成反馈，merge_failed 仍显示 review 和原因。详情固定动作条使用 CanApprove/CanReview 和本任务 busy，不依赖全局 boardLoading 一刀切。requestReview 保持详情，reject 成功转待办并保留可读反馈；冲突保留用户草稿并重新拉取，禁止自动重放。评审策略和写入放在阶段 1 数据层的既定入口，由集成负责人串行接入，详情实现者不同时改数据层。

验收：合并失败可见、可重试、不丢 diff 和反馈；只有一次在途评审请求；未授权时无可用接受按钮；含 [你验] 的标准清楚展示，UI 不声称已自动验证。

测试：扩展 DetailAccept、DetailRejectWithFeedback、DetailRevisionConflict、DetailConflictRefreshFailure；新增返回 review+merge_failed 无 error 时面板不关闭、CanApprove=false、双击接受只调用一次、requestReview 返回新线程的测试。保留 internal/kanban/auto_accept_test.go 的合并失败回归。

**3.3　目标详情替代自动弹窗和多层叠加。**

锚点：internal/ui/kanban_modal.go:132 `// plan_review 目标首次出现时自动打开一次规划审查，记录已弹出过的目标避免重复弹出`

锚点：internal/ui/kanban.go:1298 `func renderGoalPlanReviewModal(c *ui.Context, s *chatState) {`

锚点：internal/ui/kanban.go:1693 `func goalOutputs(board KanbanBoard, goalID string) []goalOutputTask {`

改法：kanban_goals.go 复用 planLayers 和 goalOutputs，统一为目标详情中的计划、任务、产物内容；目标→任务有返回栈，不再互相叠 modal。planning/plan_failed/plan_review/running/paused/completed/cancelled 都有明确动作集合；计划完成只更新提示，不自动开。暂停、取消、重新规划文案严格反映服务端能力；总管常驻目标单独作为队列呈现。

验收：后台计划完成不抢焦点；开始只调用 StartGoal；重新规划失败保留反馈；任务详情返回目标后原选中层次保持；运行中的目标不提供非法重规划。

测试：更新 ReplanStates，新增计划完成不弹窗、目标导航往返、按 GoalID 汇总产物、paused 不显示「已停止会话」、总管队列不显示普通完成按钮等测试，放新增 kanban_goals_test.go。

### 阶段 4：键盘优先和可用批量

**4.1　复用全局命令系统，补作用域内导航。**

锚点：internal/ui/palette.go:90 `func buildPaletteItems(s *chatState) []paletteItem {`

锚点：internal/ui/shortcuts.go:67 `{id: "command-palette", title: "命令面板", group: "窗口", keys: "⌘K", mods: ui.Cmd, key: ui.KeyK, bound: true, customizable: true},`

改法：新增 kanban_keyboard.go 处理任务集合的焦点和导航，命令候选由现有 buildPaletteItems 追加，动作调用 kanban_actions.go。N、Shift+N、方向键、Space、⌘A 仅集合焦点有效；⌘F 在任务窗口路由搜索；文本输入和 IME 优先。Escape 按第 7 节层级处理；保留自定义快捷键检测和全局会话键位。

验收：不用鼠标可新建、查找、选任务、打开详情、找到合法动作并返回；输入 N/J/K、中文组合与多行 Enter 不触发任务动作；两个窗口焦点互不串联。

测试：新增 kanban_keyboard_test.go 的聚焦导航、越界、空组、过滤、文本输入、IME、Escape 层次和命令菜单权限测试；补 palette_test.go 的任务命令；保留 shortcuts 既有冲突检测测试。

**4.2　让多选有真实且受限的操作。**

锚点：internal/ui/kanban_modal.go:719 `func renderKanbanSelectionBar(c *ui.Context, s *chatState, ks *kanbanState) {`

锚点：internal/ui/kanban.go:161 `selectedTaskIDs map[string]bool`

改法：新增 kanban_selection.go 持有可见范围的选中和批量执行状态，范围选择和⌘A 不选隐藏项。只接重新打开到待办、删除；逐项校验并串行调用既有客户端，保留失败项与原因。运行/审批任务删除仍由服务端拒绝；确认中任务状态变化则重新计算，不沿用过期资格。清除筛选/换项目要显式清理不可见选择，不暗留跨项目批量状态。

验收：混合权限有明确可操作数量；部分失败不会显示全成功；双击不会重复批次；关闭窗口停止后续请求，不残留 goroutine 或后台循环；没有批量接受按钮。

测试：新增 kanban_selection_test.go，覆盖 Shift 范围、只选可见项、权限变化、部分失败、revision 冲突、重复提交、关闭取消及删除确认；保留现有单卡删除测试。

### 阶段 5：共用入口、动效和回归收口

**5.1　统一任务入口与待处理计数，协调自动化共用文件。**

锚点：internal/ui/sidebar_footer.go:254 `if task.Column == "review" || task.CanReview {`

锚点：internal/ui/tasks_window.go:68 `func (s *chatState) setTasksWindowView(view string) {`

锚点：internal/ui/chat_header.go:2207 `func (s *chatState) headerOpenKanban() {`

锚点：internal/ui/steward_task_page.go:178 `label := kanbanStatusBadge(task)`

改法：入口显示与查询复用统一需处理谓词，任务/目标分别去重，不额外重复加 review 数；在已有低频缓存基础上监听变更合并刷新，关闭释放。窗口目的与 Board/List 状态分离，重开和复用不丢搜索/详情。聊天头部、任务信息带沿用同一状态文案，打开执行线程继续走 revealThread。设置保留原配置值，仅将「完成后自动转为 Review」改为真实且简洁的中文显示；不得借文案修改改变 AutoReview 行为。

验收：审批、输入、失败、待审阅、待确认计划都可从入口定位；处理后计数下降；两个目的切换不叠加订阅；原 tasks/automations 入口和默认视图仍兼容。

测试：扩展 sidebar_footer_test.go 的计数/节流/错误缓存、steward_task_page_test.go 的等待与审批刷新；新增窗口复用与自动化切换契约测试；更新 settings_page_workbench_test.go 文案断言，验证配置值不变。

**5.2　按动效表补齐并做必要回归。**

锚点：internal/ui/motion_presets.go:14 `func motionTransition(c *ui.Context, t ui.ElementTransition) ui.ElementTransition {`

锚点：internal/ui/motion_presets.go:58 `if reduceMotion(c) || period <= 0 {`

锚点：internal/ui/modal_style_guard_test.go:13 `func TestNoStyledModalOrPopover(t *testing.T) {`

改法：把第 8 节预设接到各已拆分组件，不修改预设的全局含义；只为真实变化播放动画。对新增文件扩展样式守卫；所有浮层、窄屏详情和确认只用基础外层；面板只设最大尺寸、内部单卡画外观。关闭窗口、切目的、ReduceMotion 实时切换时停掉循环与无用请求。

验收：减少动效时无位移/缩放/交错/循环，状态仍完整；无双层黑边、颜色硬编码和新 cgo 依赖；关闭任务窗口后无遗留订阅/刷新任务。实机满足第 12 节后再合并。

测试：扩展 motion_system_test.go 的即时偏好切换和关闭循环行为；必要测试名为 TestKanbanMotionReduceMotion、TestKanbanClosedStopsWork。运行 UI 的看板/样式/守卫针对性测试，及受影响适配层测试；不为纯文案新增实现镜像测试。

### 文件所有权与并行安排

| 工作包 | 独占文件 | 依赖 / 合并规则 |
|---|---|---|
| 集成负责人 | 阶段 0 拆分、kanban_modal.go、tasks_window.go、最终接线 | 自动化负责人不得同时改共享外壳；统一接口后接各模块 |
| 数据与创建 | kanban_state.go、kanban_data.go、kanban_sheets.go、client.go、internal/app/convert.go、对应新增测试 | 阶段 1；详情只读约定 DTO，不能自行改数据层 |
| 卡片与列 | kanban_cards.go、kanban_actions.go、对应新增测试 | 阶段 2；使用框架插槽，尽量不动 kanban_modal.go |
| 详情与目标 | kanban_detail.go、kanban_goals.go、对应新增测试 | 阶段 3；评审返回处理由数据负责人或集成人串行改 |
| 键盘与选择 | kanban_keyboard.go、kanban_selection.go、palette.go、shortcuts.go、对应新增测试 | 阶段 4；阶段 0 先约定所需选择/焦点字段 |
| 入口收口 | sidebar_footer.go、chat_header.go、steward_task_page.go、settings_page_workbench.go | 阶段 5；与自动化协调后一次合并 |

阶段间复用同一文件的地方已列为串行集成：阶段 2.1/5.1 的外壳、阶段 1.3/3.2 的写入结果、阶段 2.3/4 的动作策略。不要为形式上的并行引入通用框架。既有 kanban_test.go 由集成负责人更新，其余新增测试按模块分开，避免测试文件也冲突。

## 10. 与自动化重设计的共用点

| 文件 | 两边都会触达的原因 | 推荐归属和契约 |
|---|---|---|
| internal/ui/kanban_modal.go | 当前第三段自动化切换、嵌入工作区、全窗口标题和范围 | 共享外壳负责人统一改；自动化提供 body/header 内容，不直接重写任务 header |
| internal/ui/tasks_window.go | 单例窗口、目的路由、窗口复用和关闭 | 共享外壳负责人维护；tasks/automations 入口字符串兼容，目的与展示方式分离 |
| internal/ui/sidebar_footer.go | 任务和自动化相邻入口、各自未读/待处理提示 | 统一命名和可访问标签，计数独立，不能把自动化运行失败加成看板任务 |
| internal/ui/palette.go | 打开任务与自动化，以及新增动作命令 | 共享命令区一次接入，不能各实现一个命令面板 |
| internal/ui/kanban.go / 拆出的 kanban_state.go | 当前 kanbanViewAutomations 与任务查询混放 | 只在阶段 0/共享接线迁移；自动化本体状态仍由 automation 工作区维护 |
| internal/ui/kanban_sheets.go | 不是自动化业务本身，但两边可能共用 Sheet、项目选择、提交忙碌规范 | 看板创建由看板负责人独占；可复用视觉规范，不抽象成万能任务表单 |
| internal/ui/chat_header.go | 看板入口的打开与激活 | 入口统一调用共享路由；自动化不另加一排头部控件 |
| internal/ui/settings_page_workbench.go | 看板策略与自动化入口说明可能一起清理 | 仅文案/配置显示协调；调度规则分属各自服务，不能互改默认值 |
| internal/ui/motion_presets.go / motion.go / tokens.go | 动效、字号、密度和容器规范 | 首期只复用不改全局默认；新增共同 token 由一个负责人提交 |

锚点：internal/ui/tasks_window.go:70 `if view == "automations" {`

锚点：internal/ui/sidebar_footer.go:123 `s.openTasksWindow("automations")`

自动化的「计划配置 / 运行记录 / 开启状态」不能直接套 TaskStatus；任务的「评审 / 合并」也不应出现在自动化列表动作里。共用的是外壳、焦点、空错态、写入反馈和动效，不是领域状态机。

## 11. 协议影响、风险与最多五个待拍板问题

### 协议影响

推荐方案不新增持久字段、不新增 RPC、不增加 TaskStatus 或 DisplayColumn。新增的桌面 DTO 字段全部来自已有 Task/Derived，属于内存适配补齐。没有列内自由排序、可执行验收、持久人工确认、优先级批量修改、真实心跳或 ETA，因此不需要为这些功能提前建模。

已存在的契约锚点：

docs/remote-control.schema.json:1146 `"reviewThreadID": {`

docs/remote-control.schema.json:1172 `"acceptanceCriteria": {`

docs/remote-control.schema.json:1200 `"reviewStatus": {`

docs/remote-control.schema.json:1215 `"canApprove": {`

若实施范围增加下列任一项，须另作协议步骤，不能私自在 UI 字典扩展：持久手工排序需要排序字段及更新接口；验收执行与持久用户确认需要运行/确认记录和相应 RPC；精准评审运行指示需要服务端可信信号。届时同步 docs/remote-control.schema.json、Go wire、桌面/远程适配与 schema 契约测试，并给旧客户端兼容策略。本文未把这些扩展排入实施阶段。

### 主要风险

- 静态检查确认数据与入口分离问题，但未运行复现；实现先加行为回归，不能把本稿当成已经复现的缺陷报告。
- 服务端 CanReview / CanApprove 对独立评审会话的运行信息有限；首期只表示权限，不凭 ThreadID 推测忙碌。评审状态机若有独立缺陷，单独修服务端与协议，不扩大 UI 项目。
- 从居中详情改为侧栏会影响短宽窗口与焦点；必须通过 840×520 和大字号实机验收，仍保留基础 sheet 降级路径。
- CrychicUI 已核实包与文件，API 与 mygo 兼容性待实现前确认；未确认前不承诺可用的导出组件或无声音。组件 API 未确认时用现有组件交付基础阶段，复杂新组件保持待确认。
- 改变拖拽完成路径会更新既有测试和用户习惯，但能避免 review 绕过合并；需要用「拖到完成后打开审阅」的真实反馈验证操作可理解。

### 需要用户拍板（不阻塞本稿；括号为推荐默认）

1. 任务和自动化是否改为窗口的两个目的导航，Board/List 留在任务内部？（推荐是，保留原入口直达。）
2. 默认是否收起「已完成」列，只有展开或搜索命中才显示卡片？（推荐是，不更改数据保留策略。）
3. 「待你处理」入口是否同时包含任务审批/失败/评审与目标计划待确认？（推荐是，分别按实体 ID 去重；不混入自动化失败。）
4. 是否接受宽屏右侧详情、窄屏单卡 sheet，替换当前多层居中弹窗？（推荐是，先保留同一详情内容与返回焦点。）
5. 是否确认 review→完成的拖拽只打开审阅，不直接接受和合并？（推荐是；批量接受同样不纳入首期。）

## 12. 给用户的实机验证步骤

以下由用户或主代理在隔离 dev 环境执行。本任务没有启动应用。不要在正在工作的正式窗口和真实任务上试删除、救援和合并；使用项目约定的隔离 dev sandbox 与种子数据。本节仅规定验收操作，不暗示脚本已运行。

1. 打开任务入口，再从侧栏打开自动化，应复用同一窗口。返回任务后视图、搜索和选中项保持；关闭再开不能有多份 watch 导致重复刷新。
2. 准备 p1、p2 两个项目各一个任务，选「全部项目」应同时看到；点 p2 任务直接看其详情，不能先误显示 p1 的任务。切到 p1 后列表与看板计数相同。
3. 在 840×520、1140×720 和大窗口操作；工具栏不出界，五列可定位/横滚；打开详情后内容保持可读，窄宽度切为单卡 sheet，无双层黑边。
4. 新建任务只填需求，确认自动标题仍正确。制造一次创建失败，再重试；文本不丢，连续双击只生成一个任务。表单换项目不应改变背后看板。
5. 待办列快速添加连续建三项；Enter 成功后仍能输入下一项，Esc 只收起输入。中文输入法选词 Enter 不应误提交；编辑文字时 N/J/K 不触发看板动作。
6. 造普通执行、等待条件、等待子任务、等待审批、等待输入、失败、驳回、待审阅、完成和取消任务。看板列沿用服务端；状态词有区分，等待不旋转、取消不呈成功；「待你处理」筛选及入口提示一致。
7. 对依赖未完成任务尝试开始或拖入进行中，必须给出具体依赖且不启动；完成依赖后允许开始。拖入待你处理无有效落点，拖同列不出现虚假的排序占位。
8. 打开任务详情，在结果/改动/会话切换。执行会话与评审会话应打开各自线程；返回任务窗口后原位置保持。快速切 A→B，A 的慢日志不能覆盖 B。
9. 让一个目标规划完成；当前编辑不被弹窗打断。主动打开计划，检查依赖与标准，确认后才开始。暂停只停止新派发；重新规划失败仍保留反馈。
10. 对带 [命令] / [审阅] / [你验] 的任务检查原文，不应出现没有运行证据的「已通过」。拖 review 卡到完成只打开审阅；接受成功后才完成，模拟合并失败则保留详情和错误。
11. 制造 revision 冲突：一处编辑后在旧详情接受/退回。应提示更新并保留反馈，不能自动再次执行。再次确认时使用最新 revision。
12. 用键盘完成搜索、新建、切任务、开关详情、范围选择。Esc 按层关闭；⌘A 在文本框只选文字，在任务集合只选可见项；全局原有快捷键不被抢占。
13. 多选混合状态任务，确认仅有真实可用的重新打开/删除；模拟部分删除失败，成功项消失、失败项保留选中并可查看原因；不存在批量接受。
14. 在动画运行时开启 ReduceMotion。侧栏/浮层立即落定，加载和运行图标静止；状态仍可读。关窗口后无持续动画或额外提示音。若引入 CrychicUI，还应验证它的内部循环与 sound 同样停止/关闭。

## 13. 验证状态与执行者检查命令

本次只写方案，未跑构建、vet、lint、Go 测试或 GUI。执行者应按实际变更选择下列有范围测试，单条命令给外层工具 120s 上限，Go 也带 -timeout 120s；阶段通过后不反复跑无关测试。

- UI 基线：go test ./internal/ui/ -run 'Test(Kanban|Goal|SidebarFooter|StewardTask|NoStyledModalOrPopover)' -timeout 120s
- 动效：go test ./internal/ui/ -run 'Test(MotionSystem|KanbanMotion|KanbanClosed)' -timeout 120s
- 适配：go test ./internal/app/ -run 'TestKanbanBoardToUI' -timeout 120s
- 生命周期回归：go test ./internal/kanban/ -run 'Test(Derive|TaskStatusTransitions|AutoAccept|ReviewAcceptMergeFailure|OccupiedSlots)' -timeout 120s

新增测试名以实施时的最终模块名为准。主代理负责最后的全量检查、依赖核验与实机检查。本稿未完成的证据仅包括：在线竞品现状核实、CrychicUI API 与 mygo 兼容性核实、运行复现及实机验证；已将它们显式列入前置核验与交付验收，不能在实现汇报中标成已通过。
