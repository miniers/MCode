# 自动化视觉与布局改版

日期：2026-11-10。状态：设计方案，可交实现者。仅新增本文，不修改程序。

**推荐：设置式共用侧栏 + 平面自动化列表 + 结果详情。** 项目从内容顶栏移到共用侧栏。自动化侧栏区段放「全部 / 失败 / 已暂停」和一个可叠加的「未读」开关，不放自动化对象列表。右侧只有一层工具栏，不做卡片套卡片。

本文只覆盖视觉、布局和接入新外壳所必需的范围适配。选中先看运行记录、配置 tab、弹窗编辑、未保存确认、立即运行打开返回的 runID、最近 100 条、现有启停与调度语义全部保留。不扩展触发器、运行模型或协议。

## 0. 证据、范围与未完成核验

- 本地：读取 `automation*.go` 的任务指定实现、相关测试、token、动效、设置侧栏、宿主工具栏与范围调用链。
- Git：实际读取最近 7 条相关提交，并查看 `d7818e8`、`8d32a65`、`11d83f8`、`1e59041`、`8f9c06c` 的限定文件统计或 diff。拆文件、结果优先与关闭保护已经实施，不能再按旧交互方案的旧行号或「尚未实现」描述开工。
- 外部：实际读取下文 6 个产品的公开官方文档；另外读取 Trigger.dev 的 inspector、运行表格和状态源码。只读 HTTP，未下载仓库，未运行外部代码、脚本或安装依赖。网络 shell 请求均限定 20–25 秒、200 KB；其余使用网页读取工具。
- 浏览器限制：`yab guide` 返回退出码 126，启动器指向的应用不存在。未启用其他浏览器，未登录产品、未查看渲染后的截图。文档的截图替代文字不等于实际看过图片。对颜色/字号的记忆参考均单独标明，不能当成当期实测值。
- `docs/superpowers/plans/2026-11-10-tasks-window-shell-kanban-visual.md` 在此工作副本不存在。外壳以派发说明的四条已定约束为准；本文给出接口需求，不虚构另一设计者已经采用的 API。合并前由主代理对齐其文档。
- 未运行应用、构建和测试。本任务是文档调研；文末命令与验收均是实现者待执行项。
- 源码引用格式为 `path:line` 后跟该行原文；行号取本工作副本。新增函数签名、文件和测试名均标为设计，不伪造新文件行号。

## 1. 根因与现有控件定位

### 1.1 根因

1. **项目是横向按钮全集，不是紧凑选择器。** 自动化在自己的工具栏调用项目渲染器；渲染器把每个项目变成按钮并允许换行。项目数量直接决定头部高度，这是「所有项目都在顶栏」的代码原因。

   `internal/ui/automation.go:1181` — `renderWorkspaceProjectPicker(c, s, curPID)`

   `internal/ui/workspace.go:182` — `ui.Row(c.Key("workspace-projects")).Wrap().Gap(Space2).AlignItems(ui.Center).Children(func() {`

   `internal/ui/workspace.go:183` — `for _, p := range projects {`

2. **外壳、自动化、列表各自拥有头部。** 宿主有目的导航和搜索；自动化又有项目与刷新；列表内部再放标题、新建、状态分段和未读开关。不是缺少留白，而是控件归属重复。

   `internal/ui/kanban_modal.go:267` — `parts := ui.SegmentedBase(c.Key("kanban-destination"), &idx, 2)`

   `internal/ui/kanban_modal.go:289` — `ui.Row(c.Key("kanban-header-filters")).FillWidth().Height(kanbanTopBarHeight).Padding(0, Space5).`

   `internal/ui/kanban_modal.go:319` — `kanbanSearchRow(c, s, ks, &query)`

   `internal/ui/automation.go:1183` — `IconButton(c.Key("automation-refresh"), IconButtonProps{`

   `internal/ui/automation.go:1283` — `newButton := Button(c.Key("automation-new-btn"), ButtonOpts{`

   `internal/ui/automation.go:1299` — `Segmented(c, "automation-status-filter", &as.filter, []Segment{{Label: "全部"}, {Label: "失败"}, {Label: "暂停"}}, func(i int) { as.filter = i; s.invalidate() })`

   `internal/ui/automation.go:1301` — `if ui.Switch(c.Key("automation-unread-filter"), &unreadOnly).Label("未读").Changed() {`

3. **浏览器式分栏套着表单式卡片。** 左栏有背景、边框、圆角、内边距，内部每个对象再画相同的卡片。右栏又是一张卡。扫描边界比名称更醒目。当前行的名称使用 14 DIP、700 字重，时间却是 11 DIP；没有足够清楚的日程与最近结果信息。

   `internal/ui/automation.go:1258` — `Radius(RadiusMD).`

   `internal/ui/automation.go:1379` — `Padding(Space4, Space4).`

   `internal/ui/automation.go:1381` — `BorderWidth(Hairline).`

   `internal/ui/automation.go:1433` — `FontWeight(700).`

   `internal/ui/automation.go:1451` — `ui.Text(c, automationShortTime(autoVal.NextRunAt.Local(), time.Now().Local())).FontSize(TypeCaption).TextColor(ThemeSecondaryText(c)).SingleLine()`

4. **运行历史重复表达标题、状态与筛选。** 上方已有「运行记录 / 配置」，历史又显示「运行记录」、已归档文字和开关、刷新、三项筛选；固定宽度状态/时间/耗时列只剩空白，没有结果摘要。编辑器则把每种日程参数包在浅底卡中，时区和预览在卡外，割裂了同一规则。

   `internal/ui/automation_run_history.go:160` — `ui.Text(c, "运行记录").FontSize(TypeBody).FontWeight(600).TextColor(ThemePrimaryText(c)).Grow(1)`

   `internal/ui/automation_run_history.go:162` — `ui.Text(c, "显示已归档").FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))`

   `internal/ui/automation_run_history.go:244` — `ui.Row(c.Key("run-status-" + run.ID)).Width(90).Shrink(0).AlignItems(ui.Center).Gap(Space1).Children(func() {`

   `internal/ui/automation_schedule_ui.go:309` — `Background(ThemeOverlay(c, 0.05)).`

   `internal/ui/automation_schedule_ui.go:437` — `ui.TextInput(c.Key("automation-timezone-input"), &as.timezone).Label("时区").Placeholder("时区（IANA）").FillWidth()`

### 1.2 不要重做已完成的交互

- 搜索已从宿主传入自动化，不再是旧文档所述的断线。

  `internal/ui/kanban_modal.go:730` — `renderAutomationWorkspaceWithSearch(c, s, p.automationSearch)`

- Cmd+N 已存在；改版把按钮归给外壳，不添加第二个快捷键处理者。

  `internal/ui/automation.go:1145` — `if !as.editorOpen && c.Shortcut(ui.Cmd, ui.KeyN) && curPID != "" && !saving {`

- 选择对象默认运行记录，编辑走弹窗，立即运行使用服务端返回的 runID。

  `internal/ui/automation.go:332` — `as.detailTab = 0`

  `internal/ui/automation.go:1549` — `modalDragModal(c, "automation-editor-modal", &open, func() {`

  `internal/ui/automation.go:734` — `openAutomationRun(as, run.ID)`

- 运行已读、归档失败的回滚及请求序号已经存在。本轮不改它们。

  `internal/ui/automation_runs_ui.go:208` — `if as.projectID != projectID || as.selectedID != automationID || as.editSession != session || automationRunsViews[as] != view || view.pending[key] != seq {`

### 1.3 设置侧栏的准确参考

原生设置窗口只负责创建窗口，真正的侧栏在 `settings.go`。参考其平面背景、28 DIP 导航行、图标加名称、低对比悬停与单一选中底色；不复制设置搜索框，因为任务外壳搜索已定在内容顶栏。

`internal/ui/settings_window.go:135` — `func openSettingsWindowNative(owner *chatState, page string) *mygo.Window {`

`internal/ui/settings_window.go:178` — `renderSettingsWindow(c, st)`

`internal/ui/settings.go:1549` — `ui.Column(c.Key("settings-dialog-nav")).Width(min(float32(220), width*0.35)).FillHeight().Children(func() {`

`internal/ui/settings.go:1280` — `ui.Column(c.Key("settings-nav-col")).Fill().Background(ThemeSidebarBg(c)).Children(func() {`

`internal/ui/settings.go:1336` — `Height(RowHeight).`

`internal/ui/settings.go:1358` — `bg = bg.Mix(ThemeSelected(c), sel)`

## 2. A — 调研对比与取舍

### 2.1 来源与证据等级

所有页面于 2026-11-10 实际读取。D = 官方文档正文；S = 源码；M = 既有产品视觉记忆，未实机核验；U = 未确认。网页发生重定向时采用最终地址。下面不用未读取的搜索摘要作事实依据。

| 编号 | 产品 | 实际来源 |
|---|---|---|
| R1 | OpenAI Codex Automations；当前入口重定向为 Scheduled tasks | [官方正文](https://learn.chatgpt.com/docs/automations?surface=app)。D：侧栏 Scheduled、结果收件箱、未读、active/paused/completed、自定义调度与高级 RRULE。不能把旧名称或旧截图当作当前 UI |
| R2 | GitHub Actions | [历史与侧栏](https://docs.github.com/en/actions/how-tos/monitor-workflows/view-workflow-run-history)、[Run workflow 位置](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manually-run-a-workflow)。D：左侧 workflow，点击 run 进入 summary，手动运行在 runs 上方 |
| R3 | Trigger.dev | [Scheduled tasks](https://trigger.dev/docs/tasks/scheduled)、[Runs](https://trigger.dev/docs/runs)；S：[ScheduleInspector.tsx](https://github.com/triggerdotdev/trigger.dev/blob/main/apps/webapp/app/components/schedules/ScheduleInspector.tsx)、[TaskRunsTable.tsx](https://github.com/triggerdotdev/trigger.dev/blob/main/apps/webapp/app/components/runs/v3/TaskRunsTable.tsx)、[TaskRunStatus.tsx](https://github.com/triggerdotdev/trigger.dev/blob/main/apps/webapp/app/components/runs/v3/TaskRunStatus.tsx)。最新文档明确不再有独立 Schedules 侧栏页，不能沿用 2024 年 changelog 的旧位置 |
| R4 | Inngest | [Inspect events and runs](https://www.inngest.com/docs/platform-and-operations/inspect-events-and-runs)、[createFunction](https://www.inngest.com/docs/reference/typescript/v4/functions/create)。D：Runs、Functions、环境范围、状态与时间筛选、详情 step timeline；创建由代码完成 |
| R5 | n8n Executions | [单 workflow 历史](https://docs.n8n.io/build/understand-workflows/understand-executions/view-executions-for-a-single-workflow)、[Schedule Trigger](https://docs.n8n.io/integrations/builtin/core-nodes/n8n-nodes-base.scheduletrigger)。D：Executions tab、筛选、重试入口、按间隔单位显示字段、workflow 或实例时区 |
| R6 | Vercel Cron | [Managing Cron Jobs](https://vercel.com/docs/schedules/cron-jobs/manage-cron-jobs)。D：项目下 Schedules → Cron Jobs、View Logs、修改配置后部署；页面把 Cron Jobs 标为 legacy，本方案不把旧 Settings 路径当作当期事实 |

### 2.2 列表、详情、字段、历史

| 产品 | 列表与详情分栏 | 行字段：名称 / 调度 / 下次 / 最近结果 | 运行历史与状态 |
|---|---|---|---|
| Codex | D：Scheduled 汇集任务与 runs；具体几栏 U。M：自动化与待审结果分离，不把编辑表单当默认页 | D：能审阅 active、paused、completed 与 recent runs；四项是否同一行 U | D：有 findings 的 run 进入收件箱，未读提示需要关注；日期分组/时间线形态 U |
| GitHub Actions | D：左 workflow 导航，主区 runs 列表；点击后进入 run summary，而非常驻第三栏 | M：run 名称、workflow、branch、actor、时间、耗时、状态；调度/下次不在主 run 行。文档未穷举列，列集合非本次实测 | D：按 workflow 查看 recent runs，详情含 jobs/steps/logs；M：状态图标、时间顺序；日期分组 U |
| Trigger.dev | D：Tasks → scheduled task → Schedules → inspector；S：inspector 可嵌入 sheet 或独立详情，头部固定、正文滚动、底部动作固定 | D：schedule 行有 type、cron、external id、next/last run、status。S：run table 有 task、status、started、duration，还支持机器/费用/版本列 | S：最近 5 次与未来 5 次分区；run 用图标+文字+语义色。运行中 Spinner，成功 CheckCircle、失败 XCircle、暂停 PauseCircle |
| Inngest | D：环境下 Runs 看跨 app 结果，Functions 看单 function；打开 run 检查，详情可新 tab 全页。固定分栏宽度 U | D：按 status、queued/started time、app 筛选；详情有 trigger、input/output、timing。四项具体行布局 U | D：step timeline，失败步骤展开每次 retry 的错误与耗时；不只是整页日志 |
| n8n | D：workflow 顶部 Executions tab 预览全部执行。M：历史窄栏与执行画布/数据详情并列；当前尺寸 U | D：执行状态、execution start 可筛选；M：行有状态、起始时间、时长，workflow 名依全局/局部页面不同；调度/下次不是执行行字段 | D：执行历史与 workflow 版本历史明确分开；失败可重试已保存/原 workflow。M：状态图标与紧凑时间行；日期分组 U |
| Vercel Cron | D：先选项目，再进 Schedules 的 Cron Jobs tab；View Logs 转日志页，不在 cron 卡堆日志 | D：维护对象是路径与 cron 配置；M：以路径、规则、日志入口为主；下次/最近结果同列与否 U | D：从 job 的 View Logs 查调用结果；不把配置列表伪装成完整运行历史。时间线/日期分组 U |

### 2.3 空状态、新建、调度编辑、颜色字号

| 产品 | 空状态与新建入口 | 调度编辑器 | 颜色与字号层次（证据严格区分） |
|---|---|---|---|
| Codex | D：可在 chat 创建/更新；Scheduled 页按钮位置、空状态文案 U | D：自定义 cadence 控件，高级 RRULE；不是 mcode 的五段 Cron | M：中性底、正文标题强于时间、少量未读强调；当期颜色/字号 U |
| GitHub Actions | D：Run workflow 在列表上方；它是运行，不是新建 workflow。空状态 U；不据此设计「新建自动化」为「运行」 | 本次读取页面没有日程编辑器；YAML schedule 是既有知识 M，不列为本次查看事实 | M：中性列表、名称强于 metadata、成功/失败状态色；正文/元数据精确字号 U |
| Trigger.dev | D：没有 task 时是 deploy onboarding；Create schedule 在 scheduled task 页面。S：暂停预览有 Schedule disabled，next runs 空有独立 TableBlankRow | D：表单含 task、cron、timezone、window、external id、deduplication、environment；可自然语言生成 cron，mcode 不新增此能力 | S：inspector 的 `bg-background-bright`、`border-grid-dimmed`、`text-sm`/`text-xs`；run label 有 success/error/pending 语义 class，system 主题可取消 label 着色。最终像素与 hex U |
| Inngest | D：函数由代码 createFunction；监控页新建按钮、空状态 U | D：triggers 参数，事件/cron helper 属代码配置；不声称提供人类表单编辑器 | M：低对比表格、强调失败节点、正文和时间分层；当期字号/颜色 U |
| n8n | D：Executions 不是新建 workflow；文档没有空执行插图或新建按钮位置，U | D：Schedule Trigger 参数按秒/分钟/小时/天/周/月/Custom Cron 分组，依单位显示相关字段；时区继承 workflow/实例 | M：中性执行列表，成功/失败/等待具有不同图标与状态色，次级时间弱于标题；当期字号/颜色 U |
| Vercel Cron | D：配置变更后 redeploy，不能从监控「新建」直接建 job；空态 U | D：`vercel.json` 或函数配置；本次是 legacy Cron，不把新 Schedules 的表单混进来 | M：黑白灰主体、路径/规则比日志入口强；当前 UI 字号、状态 hex U |

**限制结论：** 六个产品的资料都实际读取，只有 Trigger.dev 的视觉组成有源码证据；其余颜色字号是明确标注的记忆，不是截图比较。这里足够支持信息架构和自有 token 设计，不足以宣称「像素级竞品对齐」。不为补齐表格而编造未知空状态。

### 2.4 五条可执行结论

1. **范围在侧栏，对象在内容。** 借鉴 GitHub 的 workflow 导航与 Vercel 的项目先行，消除项目按钮全集。任务与自动化共用项目选择，避免两份范围漂移。不同于 GitHub，不再把自动化名称挤进同一侧栏，否则项目一多又产生双重长列表。
2. **结果是默认阅读面，配置是按需查看面。** 借鉴 Codex 的结果收件箱和 n8n 的 Executions/版本区分，保留现有结果优先。没有改写任务的意图就不展示输入框，避免误改与布局噪声。
3. **行是有节奏的文本，不是小仪表盘。** 借鉴 Trigger.dev 的 status/start/duration 对齐，但删掉机器、费用、版本、常驻 ID。名称、规则和下次时间足够找目标；最近结果用短文字和不同形状图标表达。只在全部项目时加项目名。
4. **历史和真实日志分开。** 借鉴 Inngest 的从结果定位错误、GitHub 的详情到日志。mcode 保留日期分组+摘要行，点击看全文，日志打开真实会话；不照搬不存在的 step graph 或 retry trace。
5. **时间参数与预览是一组。** 借鉴 n8n 的按类型出现字段、Trigger.dev 的未来运行区，保持一个「频率+参数+时区」区域，下方紧接三次预览。用 mcode token 表达层次，不复制任何品牌底色，不引入库。

## 3. B — 布局

### 3.1 共用侧栏中的自动化区段

外壳顺序固定为目的 → 项目范围 → 当前目的区段：

```text
任务
自动化     [未读数，仅数据已知时]

全部项目
mcode
文档站
客户端

全部        ← 以下只在「自动化」目的出现
失败
已暂停

未读   [开关]
```

- 全部/失败/已暂停互斥；分别复用 `filter=0/1/2`，已暂停仍排除已完成。未读是独立叠加条件，复用 `unreadOnly`，保持既有组合筛选。
- 不新增「运行中」视图：当前已有三种筛选足够；运行状态在行内明确显示。首轮不增加一个新状态维度和测试矩阵。
- 不在每个视图后放数字；只在目的「自动化」显示已加载范围的未读总数。未加载/读取失败为未知，不能当 0。搜索不改变该数字。
- 不放自动化对象列表：否则侧栏同时混入目的、项目、状态和对象，长名称挤占导航，窄窗也难复用。内容列表是对象的唯一位置。
- 搜索是内容搜索，不过滤项目导航；每个目的保留自己的搜索状态。清除筛选必须经外壳回调清自动化搜索，不能写 `getKanbanState(s).searchText`。
- 侧栏样式由外壳的设置式行组件承担；自动化只提供区段与事件，不修改设置文件。

### 3.2 宽窗口：窗口 1440，侧栏示意 220，内容 1220 DIP

```text
┌──────────────────┬──────────────────────────────────────────────────────────┐
│ 任务             │ 自动化                       [搜索             ] [＋新建] │
│ 自动化       3   ├──────────────────┬───────────────────────────────────────┤
│                  │ ✓  每周安全审计 •│ 每周安全审计             [启用] [运行][⋯]│
│ 全部项目         │ mcode · 周一09:00│ 周一 09:00 · Asia/Shanghai · 明天 09:00   │
│ mcode            │ 明天09:00 · 上次成功│ [运行记录 | 配置]                    │
│ 文档站           │                  │ [全部▾]                       [⋯]      │
│ 客户端           │ ×  文档巡检      │ 今天                                  │
│                  │ 文档站 · 每2小时 │ × 失败  09:00  32s  未提交运行回执      │
│ 全部             │ 今天12:00 · 上次失败│ ✓ 成功  07:00  28s  未发现过期链接  │
│ 失败             │                  │ 昨天                                  │
│ 已暂停           │ ○  依赖检查      │ ✓ 成功  18:00  26s  检查完成            │
│                  │ mcode · 工作日   │                                       │
│ 未读       [开关]│ 已暂停           │                                       │
└──────────────────┴──────────────────┴───────────────────────────────────────┘
```

- 只有三块：共用侧栏、对象列表、详情。列表和详情用一条 `Hairline` 分隔，无外围卡片、投影、圆角和栏间空隙。
- 对象列表沿用 `automationListWidth=320`；右栏填余下空间。详情水平/垂直内边距 `Space6`，列表水平 inset `Space3`，行内水平 `Space4`。
- 内容区可用宽度达到现有 `automationNarrowWidth=1000` 才并排，按**扣除侧栏后的实际内容宽度**判断，不用窗口总宽。1000 时仍给详情约 679 DIP；大字偏好导致内容不容纳时允许提前切单栏，不缩小文字。
- 内容顶栏由外壳绘制：标题固定「自动化」、搜索、新建；本轮无需第四个全局视图控件。范围、失败/暂停/未读已在侧栏，不复制到顶栏。刷新移进详情或结果的更多菜单，后台订阅继续工作。
- 默认维持未选中态，不为了美观自动标已读；右侧用一个静态 `IconCalendarClock()` 和「未选择自动化」，不写教学说明。
- 选中对象后的头部分两行：名称行；调度+时区+真实 `NextRunAt` 行。启停、运行和更多在名称行右侧。最长名称 `Grow(1)/MinWidth(0)/SingleLine`，详情和无障碍名保留全名。
- 配置 tab 保留「编辑」入口。单次 run 详情在右栏替换历史列表，不增加第四栏；返回仍保留历史筛选和滚动。

### 3.3 窄窗口：840×520 DIP

侧栏示意仍为 220，内容约 620。侧栏不改为顶栏，不藏掉项目范围。

```text
┌──────────────────┬─────────────────────────────────────────────┐
│ 任务             │ 自动化             [搜索          ] [＋新建] │
│ 自动化       3   ├─────────────────────────────────────────────┤
│                  │ ✓ 每周安全审计                            • │
│ 全部项目         │ mcode · 周一 09:00                            │
│ mcode            │ 明天 09:00 · 上次成功                         │
│ 文档站           │                                             │
│ 客户端           │ × 文档巡检                                  │
│                  │ 文档站 · 每 2 小时                           │
│ 全部             │ 今天 12:00 · 上次失败                         │
│ 失败             │                                             │
│ 已暂停           │                                             │
│ 未读       [开关]│                                             │
└──────────────────┴─────────────────────────────────────────────┘

选中后同一个内容区替换为详情；侧栏与顶栏不动：
┌──────────────────┬─────────────────────────────────────────────┐
│ 共用侧栏         │ 自动化             [搜索          ] [＋新建] │
│                  ├─────────────────────────────────────────────┤
│                  │ [← 自动化]                                  │
│                  │ 每周安全审计              [启用] [运行] [⋯] │
│                  │ 周一 09:00 · Asia/Shanghai · 明天 09:00        │
│                  │ [运行记录 | 配置]                            │
│                  │ [全部▾]                              [⋯]    │
│                  │ 今天                                        │
│                  │ × 失败   09:00   32s                         │
│                  │   未提交运行回执                             │
└──────────────────┴─────────────────────────────────────────────┘
```

- 返回列表保留对象选择、查询和滚动；Esc 先关浮层，再回本次运行、再回对象列表，最后才让外壳关闭窗口。编辑器 dirty 时仍先确认。
- 详情头部操作不与长标题争抢同一窄行：不足宽度时操作另起一行，但不复制按钮。历史行的摘要改第二行；时间/耗时不换到难扫读的随机位置。
- 搜索可伸缩，不设会把新建挤出可视区的硬最小宽度。图标按钮必须有中文无障碍名；不靠 hover 才能访问。
- 编辑弹窗按整个窗口可用范围居中，不限制在右侧 620 宽内；最大 700 DIP，840 宽下留 `Space6` 外边距，520 高下头尾固定、主体单独滚动。

### 3.4 三种空状态（宽窄一致，内容居中，侧栏与顶栏保留）

```text
A. 全部项目，加载成功，自动化总数 0
┌ 内容 ──────────────────────────────┐
│           [静态日历图标]           │
│           还没有自动化             │
│           [新建自动化]             │
└────────────────────────────────────┘

B. 选中「文档站」，该项目加载成功为 0
┌ 内容 ──────────────────────────────┐
│           [静态日历图标]           │
│           此项目没有自动化         │
│           [新建自动化]             │
└────────────────────────────────────┘

C. 搜索或状态/未读条件导致结果为 0
┌ 内容 ──────────────────────────────┐
│           [静态搜索图标]           │
│           无匹配结果               │
│           [清除筛选]               │
└────────────────────────────────────┘
```

- A/B 都使用同一个新建动作，不增加第二套创建逻辑。顶栏新建是固定位置；空态按钮只是同一动作的就地入口，非另一层工具栏。无项目时按钮禁用，显示「暂无项目」；不捏造 ProjectID。
- A 只有在范围确为全部时写全局空；B 不查询所有项目来证明「其他项目有」，只陈述当前项目事实。状态/搜索优先判 C，不能显示「还没有自动化」。
- 空结果时不保留一个空详情卡；若因刷新或条件变化隐藏了选中对象，隐藏详情但保留 selection，不用渲染动作把它标已读。清条件可恢复。
- 加载失败单独显示错误和「重试」；已有数据时保留列表并显示局部失败条。不得把失败转换为 A/B/C。
- 已有对象没有运行记录时，详情显示「暂无运行记录」及「立即运行」；与主界面没有自动化不是同一个空态。

## 4. C — 行、历史与 token

### 4.1 自动化行字段

每个对象固定三层，信息上限明确；不是把领域字段全部塞回来：

1. 第一行：状态形状图标、名称、末端未读标记。名称 `TypeBody`、通常 400，选中时 `weightFor(c, 500)`；不再每行 700。未读不单靠色点：加 `未读` 无障碍名，并使名称具有 500 字重；聚焦可读到未读计数。
2. 第二行：`automationScheduleText`，仅全部项目范围时前缀项目名。`TypeFootnote`。不显示 Prompt、StopWhen、模型、权限、累计次数、运行按钮或开关。名称/项目/规则均单行截断，全称可从详情/无障碍名读取。
3. 第三行：启用且有下一次时显示「今天 14:30」；后跟最近结果「上次失败/上次成功/已停止」。暂停/完成时优先写「已暂停/已完成」，不显示陈旧的 NextRunAt；仍在运行则明确写「运行中」。不额外写「状态」「调度」「下次运行」字段名，所在行已说明其用途。

列表时间保持现有查看端本地时间展示，完整日期与时区在详情或 tooltip 提供；调度规则的 IANA 时区不能因显示本地时间而被覆写。跨年时间必须包含年份，避免把明年误作本年。时区、上次失败并未被删除，只是移到能阅读它们的位置。

### 4.2 状态不只靠颜色

| 真实状态 | 形状 / 文本 | 颜色 |
|---|---|---|
| 运行中 | 列表用静态 `IconLoader()` +「运行中」；当前详情用 `LoadingIndicator`，减少动效时静态 | `ThemeAccent` |
| 最近成功 | `IconCircleCheck()` +「上次成功」或 run 的「成功」 | 图标 `ThemeSuccess`，正文 `ThemePrimaryText` |
| 最近失败 | `IconCircleX()` +「上次失败/失败」 | 图标 `ThemeDanger`，正文 `ThemePrimaryText` |
| 暂停 | 静态 `IconCircleSmall()` +「已暂停」；连续失败时保留「连续失败后暂停」 | 图标/一般文字 `ThemeSecondaryText`，真实失败原因 `ThemeDanger` |
| 完成 | `IconCheck()` +「已完成」；不能被暂停覆盖 | `ThemeSecondaryText`，无需整行绿色 |
| 停止/跳过 | `IconCircleStop()` +「已停止/已跳过」 | `ThemeSecondaryText` |
| 从未运行的正常计划 | `IconCalendarClock()`；第三行只列下一次 | `ThemeTertiaryText` |
| 未知运行状态 | 原始非空 status 或「未知」，不映射成成功 | `ThemeSecondaryText` |

计划启用状态与最近运行结果是两个事实。一条仍启用、上次失败的计划同时保留下一次和失败结果，不能为节省一个文字把它写成「已暂停」。运行中的暂停计划同时保留「运行中」与「已暂停」，不得暗示本轮停止。

### 4.3 平面样式与 token 映射

下列数值是 mcode 设计值，不是竞品像素测量。字号遵守现有缩放链，使用 `Type*`，不得把它们转成新的裸数字或重复乘缩放。

| 部位 | 现有 token 与调用规则 |
|---|---|
| 主区 / 侧栏 / 内容面 | `ThemeWindowBg` / `ThemeSidebarBg` / `ThemeSurface`；右侧列表与详情无各自外围卡片；用 `ThemeDivider` + `Hairline` 分栏 |
| 名称 / 时间 / 结果正文 | `TypeBody=13` / `TypeFootnote=12` / `TypeBody=13`；日期分组用 `TypeFootnote`、`weightFor(c,500)`；不再用 `TypeCaption=11` 承载主要时间 |
| 页面标题 / 对象标题 / 编辑标题 | `TypeTitle=15` / `TypeTitle=15` / `TypePageTitle=17`；`weightFor(c,600)`，不新增大号 hero 标题 |
| 行内上下 / 左右 | 自动化行 `Padding(Space4, Space4)`，三行之间 `Space1`；行间 `Space0`。行高由文本自然撑开，不写死会裁字的 Height |
| 列表 inset / 详情 inset / 组间距 | `Space3=8` / `Space6=20` / `Space7=24`；历史日期标题前 `Space5`、标题后 `Space2` |
| 行内图标 / 未读标记 | `IconSM=14` / `Space2=6`；图标与文本 `Space2`，未读标记 `RadiusPill` |
| 行圆角 / 输入与菜单 / 弹窗卡片 | `RadiusSM=6` / 既有控件样式 / `RadiusLG=12`；列表/详情外围无 Radius |
| 默认 / hover / selected | 默认透明；hover `ThemeHover`；selected `ThemeSelected`。hover 不使用强调色边框，选中不叠加 hover；键盘焦点使用既有 Focus 样式，不能去掉焦点环 |
| 状态块 / 错误 | 默认不用状态胶囊。错误就地 `ThemeDanger`；需要面底时 `ThemeOverlay(c, OverlaySubtle)`，正文可复制；不得使用裸 `ui.Hex/RGB/RGBA` |
| 控件 | 主操作 `ControlMD=28`，局部菜单 `ControlSM=24`；运行行更多入口可用 `ControlXS=20`，Tab 焦点与中文 Label 必须存在 |

**不新增全局 token。** 现有尺寸、间距、字号、颜色足够。保留原有 `automationListWidth=320`、`automationNarrowWidth=1000` 作为该页面几何尺寸；编辑最大宽沿用 700 并命名为 `automationEditorMaxWidth`，不是新的视觉样式体系。跨行高度一律自然布局；不用 `Padding(Space1+Space2)` 这类算术拼出新间距规避棘轮。若实机确实缺 token，另提明确值与场景，本轮不先增加。

现有 token 的证据：

`internal/ui/tokens.go:18` — `Space5 float32 = 16`

`internal/ui/tokens.go:50` — `RadiusSM   float32 = 6`

`internal/ui/tokens.go:76` — `TypeBody      float32 = 13`

`internal/ui/tokens.go:172` — `func weightForTheme(w int, dark bool) int {`

### 4.4 运行记录

- 不再在 tab 下重复「运行记录」标题和整条分隔线。只留一行局部工具：左边一个筛选浮层，右边更多。这个控件不占外壳全局顶栏的「一个视图控件」配额。
- 筛选浮层保留「全部/失败」互斥、「未读」叠加、「显示已归档」叠加；顶层按钮显示当前选择，有叠加条件时显示选中指示，不增加每个条件的说明段。更多收纳「全部标为已读」「刷新记录」。操作与请求逻辑复用现有方法。
- 继续按今天/昨天/日期分组，逆序，最多 100 条。全是同一天只显示一个组名，不加竖时间轴；本产品没有 steps 数据，不画伪步骤时间线。
- 常规行：`[状态图标+文字] [HH:MM] [耗时] [摘要一行] [⋯]`。优先显示 `Error`（失败时）否则 `Summary`；两者空则不造「执行完成」文案。秒级时间留给详情，耗时保留现有格式。运行中不在耗时列再重复一次「运行中」，用「—」并保留首列状态。
- 窄行：前三项固定首行，摘要第二行，与文本左边缘对齐；不得横向滚动。摘要单行截断，详情全文 `Selectable()`；长错误、无空格长串不撑宽。
- 常规背景透明，只有悬停 `ThemeHover`；无一行一张灰卡，无常驻边框。已读不降到看不清；未读同时有标记与 500 字重。行本身需 Focusable，Enter/Space 与点击共用 open 动作。
- 点击打开才标已读，不因 hover 或切换对象清空未读。已读/归档失败的回滚、重试和旧请求隔离保持原样。
- run 详情保留返回、真实状态、时间、来源、摘要/错误全文、复制、再次运行和有 ThreadID 时的打开会话。运行 ID 只在更多中复制，不常驻。摘要/错误分区只有内容存在才出现，错误图标及复制无障碍名保留。
- 满 100 条时保留「仅显示最近 100 条」；筛选空保留「最近 100 条中无匹配」。这是必要限制，不是可删除的说明噪声。

## 5. D — 编辑弹窗与调度编辑器

### 5.1 弹窗：一张表单，一套头尾

```text
┌ 新建自动化 / 当前名称 ──────────────────────────────────────┐
│ 名称  [_______________________________________________]    │
│ 项目  [文档站]  ← 只在「全部项目」新建时需要；选好后只读      │
│ 要执行的任务                                              │
│ [____________________________________________________]    │
│ [____________________________________________________]    │
│                                                          │
│ 计划                                                     │
│ [每周 ▾] [周一 ▾] [09:00]                                 │
│ 时区 [Asia/Shanghai_________________]                    │
│ 预计运行   11月16日 周一 09:00                             │
│            11月23日 周一 09:00                             │
│            11月30日 周一 09:00                             │
│                                                          │
│ 执行                                                     │
│ 模型 [跟随默认 ▾]         权限 [只读 ▾]                    │
│                                                          │
│ 停止与补跑                                               │
│ 达到条件 [___________________________________________]    │
│ 最多运行次数 [0____]       连续失败 [3] 次后暂停            │
│ 错过后补跑一次 [开关]                                     │
│ 创建状态 [创建并启用 ▾]   ← 仅新建，复用现有选择语义        │
├──────────────────────────────────────────────────────────┤
│ [立即运行，仅已有对象]                    [取消] [保存/创建]│
└──────────────────────────────────────────────────────────┘
```

- 头部只放标题；底部固定取消和唯一提交。已有对象的立即运行作为左侧次要动作保留，不另造提交即运行的默认行为。保存按钮的现有 enabled/dirty/校验语义不变，Cmd+S 与按钮共用方法。
- 创建状态沿用现有 Select 和两种值，只换位置，不新增分裂按钮组件；选状态不自动提交。实际提交文案仍为「创建并启用/创建为暂停」。未保存确认继续给「继续编辑/放弃」，不以视觉改版顺手改写草稿流程。
- 名称、任务、计划、执行、停止与补跑依次阅读；小组标题用 `TypeBody`、500，组间 `Space7`。本轮不引入新的折叠交互，不让旧任务字段因为折叠丢焦点。
- 普通字段一行标签+输入/选择；标签列同宽且不压缩，长标签在窄弹窗改上下两行。提示词为主要正文输入；删除示例长占位、标题重复解释，但保留可访问字段名和必要格式占位。
- heartbeat 的模型/权限仍禁用，保留「心跳模式沿用原会话设置」这条必要原因；不能为砍文字而让禁用无法理解。原数值范围与 MaxRuns=0 语义均保留，不新增限额模型。
- 冲突、字段错误、未保存确认在头部下方/相关字段处出现；大错误允许换行，不把底栏挤出窗口。复制、确认、校验不改协议字段。
- 外层只用 `modalDragModal` 或 `ui.DialogBase`，只设尺寸约束；外观只画在内容卡上。页面本体不伪装为弹窗。最大宽 700，用实际可用宽高收敛；不同时设一个无法收缩的硬宽导致 840 宽溢出。

### 5.2 调度区域：只整理层次，不改字段

- 把类型和当前参数放同一可 Wrap 行：每天/工作日 `[类型][HH:MM]`；每周 `[类型][星期单选][HH:MM]`；间隔 `[类型][数字][秒/分钟/小时]`；单次 `[类型][现有日期时间输入]`；高级 Cron 类型占一行，表达式独占下一行。
- 下方一行显式「时区」+现有 IANA 输入；不依赖 placeholder 充当唯一标签。空值继续表示既有继承语义，不能因重排而默认为查看端时区。
- 三次预览紧贴时区，`Space3` 间隔，不另画灰底卡。标题「预计运行」只出现一次；单次只有一条；结束/无候选保持现有消息。字段错误就地显示并清除过时预览。
- 删除「运行时间 (HH:MM)」「执行时间 (2006-01-02 15:04)」的技术说明标题；必要格式留在输入 placeholder 与无障碍 Label，Cron 的五段语义和完整验证不变。
- `automationScheduleFromDraft`、类型切换保留各自时间、任意秒数与单次秒精度、200ms 合并请求、三次上限、迟到结果丢弃、关闭取消均不改。
- 组件复用检查：实际读取 [CrychicUI datetime 目录](https://github.com/ELY-Open-Source/CrychicUI/tree/main/datetime) 与 [recurrence.go](https://github.com/ELY-Open-Source/CrychicUI/blob/main/datetime/recurrence.go)。其 `Rule` 提供 day/week/month/year 与一周多选，和当前六种调度及 weekly 单选不等价。这里不新建 picker/Recurrence 组件，只重排现有 TextInput、Select、Stepper、Switch、Row/Column；本轮不引入包和版本，不触发 sound/overlay 接入。其他 picker 的兼容性未验证，不把它们当本轮依赖。

### 5.3 只读配置

配置 tab 保留，不把它变成另一张编辑表单。Prompt 用 `TypeBody` 可选中文本；计划、执行、停止与补跑分组，与编辑器同序。模型、权限、停止条件的字段名是辨认值所必需的，保留；不再给每条数据相同字号、相同权重。`automationConfigLines` 可保留供已有测试和文本消费；渲染器改读同一个 Automation 的结构化值，不通过解析这些中文行还原字段。不改运行模式、供应商、Effort、MaxIterations 回退和补跑展示语义。

## 6. E — 动效与减少动效

| 场景 | 既有预设 | 正常模式 | ReduceMotion |
|---|---|---|---|
| 新建、复制、删除、归档导致行增删 | `motionListItem(c)` | 按稳定 automationID/runID 做 `DurBase` 进入、`DurSnap` 退出；只有实际增删，不在每次刷新重播 | 即时增删，不等动画再恢复焦点 |
| 选中、悬停、焦点、状态文字色 | `motionFeedback(c)` | `DurSnap` 颜色过渡，无缩放、无悬停抬升 | 直接终态，焦点一直可见 |
| 换对象、run 详情、窄窗返回 | `motionTransition(c, ...)` | `DurBase` 短淡入，按选中 ID 键控；不横滑整屏，不重复推动侧栏 | 即时替换并恢复焦点、滚动 |
| 编辑器、未保存确认 | `motionDialog(c)`，由现有壳统一承担 | 现有 `springGentle`；内容不叠第二次弹出 | 即时出现/消失 |
| 历史筛选和更多 | `motionPopover(c, ...)`，沿用 `appPopover` | 轻升起，无新动画参数 | 即时出现/消失 |
| 三次预览内容变化 | `motionTransition` + `DurBase` | 同一区块短淡入，不把三行当列表连续上下弹跳 | 直接显示新结果 |
| 运行中 | 现有 `LoadingIndicator`；自定义循环只能走 `motionLoop` | 只在正在阅读的详情/历史中保留必要忙碌标志，不让每个列表项持续转动 | 静态形状+「运行中」，不调 Loop、不续帧 |

数字角标默认直接更新，不为未读变化额外滚动数字。普通订阅刷新不重排入场；列表排序维持现有启用优先/下一次/名称，避免把运行状态变更当作新增。

`internal/ui/motion_presets.go:14` — `func motionTransition(c *ui.Context, t ui.ElementTransition) ui.ElementTransition {`

`internal/ui/motion_presets.go:57` — `func motionLoop(c *ui.Context, e ui.Element, key any, period time.Duration, ease ui.Easing) float32 {`

`internal/ui/motion_presets.go:108` — `func motionListItem(c *ui.Context) ui.ElementTransition {`

`internal/ui/motion_presets.go:124` — `func motionFeedback(c *ui.Context) ui.ElementTransition {`

减少动效必须覆盖先有动画再切偏好的场景。不能只在页面创建时读取一次。已有 `TestAutomationMotionReduceMotion` 是基线，实施后需要补运行态、行增删、编辑弹窗和动态偏好切换检查。

## 7. 依赖外壳 — 明确接口和范围边界

以下是**期望签名，不是现成 API**。由自动化集成批次在新的 `internal/ui/automation_shell.go` 提供自动化侧适配；外壳只消费这些函数并接入其项目导航，不要求外壳采用新的通用页面框架。

```go
// ScopeID == "" 明确表示全部项目，不是未初始化。
type automationWorkspaceProps struct {
    ScopeID       string
    SearchText    string
    ClearSearch   func() // 只清自动化搜索，不清任务目的的搜索
}

// 区段不发请求；复用当前 scope 已加载的状态。
func renderAutomationSidebarSection(c *ui.Context, s *chatState)

// c 的宽度必须是扣除共用侧栏、工具栏后的实际内容宽度。
func renderAutomationWorkspaceInScope(c *ui.Context, s *chatState, p automationWorkspaceProps)

// 由外壳的新建按钮或 Cmd+N 唯一调用；返回值用于禁用按钮。
func automationCanCreate(s *chatState) bool
func requestNewAutomation(s *chatState, scopeID string,
    chooseProject func(onChosen func(projectID string)))

// 外壳项目/目的切换及原生 OnClose 都必须先过离开保护。
// true：可以立即执行 next；false：自动化已接管，用户确认后自行执行 next。
// next 必须至多调用一次；继续编辑/保存失败不能调用。
func prepareAutomationLeave(s *chatState, next func()) bool

// 最上层先消费 Escape；消费后外壳不得再关闭原生窗口。
func handleAutomationEscape(s *chatState) bool

// known=false 不显示 0；数字只表示当前范围已知未读。
func automationScopeUnread(s *chatState) (count int, known bool)
```

外壳责任：原生独立窗口生命周期、设置式侧栏行组件、任务/自动化目的、共用项目范围列表、内容顶栏标题/搜索/新建、项目选择浮层和键盘入口。自动化不得修改 `tasks_window.go`、`root.go`、`kanban*.go`、`settings*.go`。

**`chooseProject` 合约：** 单项目新建直接绑定该 ScopeID；全部项目新建才请求外壳的单项目选择浮层。取消不回调；选择有效项目后回调一次并打开编辑器，不改变共用 ScopeID，不暗选第一个项目。项目选择浮层必须用 `ui.PopoverBase/appPopover`。当前没有项目时 `automationCanCreate=false`。这个接入行为是全部范围支持的必要条件，不新增后端字段。

**`prepareAutomationLeave` 合约：** 无 dirty 时返回 true，由调用者执行 next；dirty 时返回 false，复用当前编辑器的确认 UI，保留待执行 next。放弃后执行；继续编辑不执行；如果现有交互提供保存则只在保存成功后执行。原生 close 要先 PreventDefault，确认完成再请求一次关闭，并防止递归二次确认。所有确认都用同一弹窗卡片，不新增第二套草稿状态。

### 7.1 不得只搬项目按钮而保留错误范围

当前 UI 只允许非空项目，且会从主工作区回退第一个项目。外壳的「全部项目」不能经这个函数解析，否则用户看的是全部、请求却查单项目。

`internal/ui/workspace.go:154` — `if len(s.projects) > 0 {`

`internal/ui/workspace.go:158` — `return s.projects[0].ID`

`internal/ui/automation.go:192` — `if s == nil || s.client == nil || projectID == "" {`

`internal/ui/automation_runs_ui.go:129` — `if projectID == "" {`

后端列表与未读已有空字符串查全部的契约，无需改服务端或新 RPC：

`internal/automation/service.go:293` — `// List 列出指定项目的自动化任务，projectID 为空时列出全部。`

`internal/automation/service.go:476` — `// UnreadCounts 返回项目下每个自动化的未读运行记录数（automationID -> 未读数）；projectID 为空统计全部。`

`internal/store/automation.go:240` — `if projectID != "" {`

接入改法：

- `as.projectID` 继续作为**列表 scope**，允许空；`initialized` 负责区分未初始化。新外壳路径只读 props.ScopeID，不读 currentWorkspaceProjectID。
- 为草稿增加 `editorProjectID string`：编辑绑定 `selected.ProjectID`；新建绑定明确选定的项目。列表 scope 不等于提交项目，不能给 Update/Create 填空 ProjectID。
- save 中的 input.ProjectID 改取 editorProjectID；迟到保存防护仍用捕获的 scope、load/saveID、editSession，不拿 editorProjectID 替代 scope guard。复制保留原对象 ProjectID，不移入侧栏选中项目。
- 列表和未读移除空 scope 的提前 return，Watch、refresh、run now 的一致性检查也只比较明确 scope + request/selection 序号；删除 run now 对 currentWorkspaceProjectID 的二次推断。
- `automationUnreadRequests` 的迟到丢弃、已读/归档回滚、previewEpoch 均保留。known 独立表达读取成功，不能因为 map 为 nil 就当全已读。
- 外壳切范围先执行离开保护，再同步重置 scope/选择/请求序号，随后请求新范围。目的切换共用同一 scope，不改主聊天选择。
- 旧 `renderAutomationWorkspace` / WithSearch 可留兼容包装，最终调用新 scoped body；兼容路径也不得重新画项目按钮。旧包装不作为新外壳全范围入口。

## 8. F — 实现批次（文件所有权不重叠）

总顺序：**批次 1 → 批次 2/3/4/5 并行 → 批次 6 接线与回归**。第 2–5 批次在新组件文件或现有独立渲染函数工作，不改 automation.go；集成者最后一次性替换主体渲染。新增文件行号尚不存在，用现有提取点定位。各批次都不得动外壳、服务端或全局 token 文件。

### 批次 1：共用视觉值，不引入新状态仓库

**文件：** 新建 `internal/ui/automation_visual.go`、`internal/ui/automation_visual_test.go`。

**锚点：**

`internal/ui/automation.go:956` — `// automationCardStatus 生成卡片状态胶囊：运行中、已结束（含失败原因）或上次运行结果。`

`internal/ui/automation.go:1048` — `func filterAutomationStatus(autos []Automation, query string, filter int, unreadOnly bool, unread map[string]int) []Automation {`

`internal/ui/automation_run_history.go:25` — `func automationRunStatus(status string) (string, string) {`

**具体改法：** 新文件只增加本轮必要的纯展示 helper/props：对象行的名称、规则/项目、下一次、最近结果；运行行的状态/摘要/时间。复用现有 status、schedule、duration 函数，不复制状态机或调度算法。需要当前时间时显式传 now 便于跨年测试。定义 `automationEditorMaxWidth` 等页面几何名字，不新增全局风格 token。对计划状态与上次结果分别取值，别继续用单一 pillText 丢掉其中一个事实。

**验收：** 所有字段来自已有 Automation/AutomationRun；相同输入有相同输出；无存储、请求、UI 状态 mutation 或新增依赖。普通有效计划不显示「正常」标签。

**补测：** `TestAutomationVisualRowFields`、`TestAutomationVisualStatusIndependence`、`TestAutomationVisualTimeAcrossYear`；覆盖暂停仍在运行、完成而非暂停、启用但上次失败、未知状态、零时间、全部/单项目字段差异、跨时区/跨年展示。仅测新 helper，不移动旧函数来制造文件冲突。

### 批次 2：平面对象列表

**文件：** 新建 `internal/ui/automation_list.go`、`internal/ui/automation_list_visual_test.go`。

**锚点：**

`internal/ui/automation.go:1362` — `ui.Scroll(c.Key("automation-scroll-list")).TrackScroll(&as.listScroll).`

`internal/ui/automation.go:1375` — `itemRow := ui.Column(c.Key("automation-item-"+autoVal.ID)).Focusable().`

`internal/ui/automation.go:1407` — `itemRow.Background(ThemeSelected(c)).BorderColor(ThemeAccent(c).Alpha(0.4))`

**具体改法：** 以原滚动/键盘选择区域为提取模板，新建 `renderAutomationList`，通过显式 props/callback 接收 visible、selectedID、未读、运行态、scope 和 onSelect/onEdit；稳定 ID 与滚动跟踪不变。按 4.1 的三层字段画平面行，去掉每项边框/常驻底色/700 字重。不渲染列表标题、新建、项目、状态分段或未读 switch。列表上下键/Enter 的现行行为保持，不能借视觉改版改变 Enter 打开编辑器的现状。

**验收：** 宽窗列表 320，窄窗填满；长名称和日程不撑宽；只有全部范围带项目名；hover 无强调边框；选中与焦点可区分；仅实际新增/移除行播放 motionListItem；无按钮重复。

**补测：** 新文件补 `TestAutomationListVisualHierarchy`、`TestAutomationListVisualBounds`、`TestAutomationListVisualStatusWithoutColor`；在 320/620 宽、长中文/无空格名称、无最近结果、未读、暂停运行中下检查边界和可访问名。组件回调次数测试防行内点击重复触发。

### 批次 3：运行历史与 run 详情

**文件：** `internal/ui/automation_run_history.go`、`internal/ui/automation_runs_test.go`；新建 `internal/ui/automation_history_visual_test.go`。

**锚点：**

`internal/ui/automation_run_history.go:90` — `func renderAutomationRunHistory(c *ui.Context, s *chatState, as *automationState, isNew bool, selectedID string, selectedUnread int, includeArchived, runsLoading bool, runsErr string, runs []AutomationRun, runRead map[string]time.Time, runArchived map[string]bool, openRunID string) {`

`internal/ui/automation_run_history.go:175` — `ui.Row(c.Key("automation-runs-filters")).Gap(Space2).Children(func() {`

`internal/ui/automation_run_history.go:233` — `row := ui.Row(c.Key("run-item-"+run.ID)).Label("运行记录 "+run.ID).FillWidth().Padding(Space2, Space3).Radius(RadiusSM).AlignItems(ui.Center).Gap(Space2).Transition(motionListItem(c))`

**具体改法：** 删除重复标题与灰卡行，筛选合入 `appPopover`，更多合入原已读/刷新动作；保留原回调、查询限制与筛选组合。行改图标+状态/时间/耗时/摘要；窄容器摘要第二行。详情全文保留，移除空区段，错误保持复制入口。运行行加键盘焦点与打开动作，图标不替代文字。组件参数保持不变，集成不再额外画重复分隔线。

**验收：** 620 宽与宽栏均无横滚；最近 100 条、归档、失败、未读组合仍工作；点击一次只标一次已读；错误失败回滚测试不改请求逻辑；无 ThreadID 不显示打开会话；完整错误可复制。

**补测：** 更新 `TestAutomationRunHistoryDetailsAndFilters`、SummaryAndUnread 的操作路径以进入浮层，保留原行为断言；新增 `TestAutomationHistoryVisualNarrowRows`、`TestAutomationHistoryVisualEmptyAndLimit`、`TestAutomationHistoryKeyboardOpen`。覆盖长摘要/长错误、零时长、运行中不双写状态、日期分组和无条件/筛选空两种历史空态。

### 批次 4：编辑器与配置的阅读层次

**文件：** `internal/ui/automation_editor.go`、`internal/ui/automation_config.go`；新建 `internal/ui/automation_editor_visual_test.go`、`internal/ui/automation_config_visual_test.go`。

**锚点：**

`internal/ui/automation_editor.go:28` — `ui.Row(c.Key("automation-detail-header")).`

`internal/ui/automation_editor.go:133` — `ui.Scroll(c.Key("automation-form-scroll")).`

`internal/ui/automation_editor.go:172` — `renderAutomationScheduleFields(c, s, as)`

`internal/ui/automation_config.go:49` — `func renderAutomationConfig(c *ui.Context, a Automation) {`

**具体改法：** 保持 renderAutomationEditor 签名和现有字段 key，头部只有标题，原按钮移至固定底栏，表单内部按计划/执行/停止与补跑分组。创建模式 Select 只移动、不自动提交；确认 UI、校验、模型选择和 heartbeat 禁用继续复用。配置渲染改成同序的可读区段，保留 `automationConfigLines` 的可消费文本兼容。编辑最大尺寸与 modal 卡片外观由批次 6 在唯一弹窗调用处落地。

**验收：** 840×520 宿主下头尾可达，正文长时只有主体滚动；Tab 可遍历隐藏在滚动区外的输入；新建仍自动聚焦名称；保存/创建/取消/立即运行各一处；取消 dirty 仍确认，所有配置字段语义未变化。

**补测：** `TestAutomationEditorVisualFooterAtMinimumWindow`、`TestAutomationEditorVisualTabOrder`、`TestAutomationConfigVisualGroups`。测试创建启用/暂停、heartbeat 原因、模型权限、MaxRuns 与旧 MaxIterations、补跑开关；未保存流程继续由批次 6 的既有回归覆盖。

### 批次 5：紧凑调度与三次预览

**文件：** `internal/ui/automation_schedule_ui.go`、`internal/ui/automation_schedule_ui_test.go`；新建 `internal/ui/automation_schedule_visual_test.go`。

**锚点：**

`internal/ui/automation_schedule_ui.go:273` — `func renderAutomationScheduleFields(c *ui.Context, s *chatState, as *automationState) {`

`internal/ui/automation_schedule_ui.go:305` — `ui.Column(c.Key("form-sched-params")).`

`internal/ui/automation_schedule_ui.go:442` — `if as.previewVisible {`

**具体改法：** 只重排渲染函数，类型+参数同组、时区紧接、预览紧接；删外围浅底卡与重复格式标题；六种调度仍用原控件和 key。Cron 可独占一行，不新增 picker、依赖、语法或客户端计算器。预览只短淡入，不每行上下入场。校验和请求函数保持原样。

**验收：** 六种调度在编辑内容宽 360/660 时都能换行而不裁字段；时区有可见标签；单次至多一条、其他至多三条；输入错误不残留旧预览；切换类型仍保留各自时间。

**补测：** 保留 DraftParsing/Invalid/RoundTrip/SwitchKeepsTimes；新增 `TestAutomationScheduleVisualAllTypes`、`TestAutomationScheduleVisualPreviewPlacement`。覆盖秒级间隔、单次秒精度、空/有效/非法时区、无候选、三条预览与字段错误。迟到/关闭请求回归由批次 6 统一跑，不重写其实现。

### 批次 6：唯一接线、全部范围、空状态与回归

**文件：** `internal/ui/automation.go`、`internal/ui/automation_runs_ui.go`、`internal/ui/automation_layout_test.go`、`internal/ui/automation_test.go`、`internal/ui/automation_runnow_test.go`、`internal/ui/automation_motion_test.go`、`internal/ui/automation_style_test.go`；新建 `internal/ui/automation_shell.go`、`internal/ui/automation_shell_test.go`、`internal/ui/automation_scope_test.go`。

**锚点：**

`internal/ui/automation.go:1090` — `func renderAutomationWorkspaceWithSearch(c *ui.Context, s *chatState, searchText string) {`

`internal/ui/automation.go:495` — `curPID := as.projectID`

`internal/ui/automation.go:724` — `activeProject := s.currentWorkspaceProjectID()`

`internal/ui/automation_runs_ui.go:128` — `func (s *chatState) loadAutomationUnread(projectID string) {`

`internal/ui/automation.go:1197` — `if !loading && errMsg == "" && len(autos) == 0 && !isNew && selectedID == "" && strings.TrimSpace(searchText) == "" && as.filter == 0 && !as.unreadOnly {`

`internal/ui/automation.go:1551` — `ui.Column(c.Key("automation-editor-card")).Width(700).MaxWidth(700).MaxHeight(height-modalDragPanelInset).Background(ThemeSurface(c)).Radius(RadiusMD).Padding(Space5, Space5).Gap(Space4).Children(func() {`

`internal/ui/automation_style_test.go:44` — `func TestAutomationRenderBoundariesAndKeys(t *testing.T) {`

**具体改法：**

1. 在 automation_shell.go 提供第 7 节接口；scope 与 editorProjectID 分离，改已有空 scope guard、save ProjectID、run now scope 判断、未读 known 与统一离开回调。所有请求保留原序号防护；给新加载路径限定 context 超时，不因布局发额外并行全项目查询。
2. automation.go 统一调用批次 2 列表与现有详情函数；删除内部项目 rail、刷新 toolbar、列表标题/新建/筛选；布局改平面分栏、1000 内容断点和单栏替换。三种空态、加载、错误各独立分支。没有显式 scope 的兼容包装不承担全部项目入口。
3. 新建与 Cmd+N 只留一处入口，由外壳调用 requestNewAutomation；自动化主体不再抢窗口快捷键。空态与同入口复用。弹窗约束改 MaxWidth/可用宽高，单张内容卡外观用 ThemeSurface、RadiusLG；不改壳组件。
4. scoped body 的清除筛选回调只改自动化查询；保留窄窗返回/焦点/滚动，筛选隐藏选中项时不渲染残留动作。状态区段移到侧栏后仍复用 filter/unreadOnly。
5. 更新既有布局/AST 测试的结构期望，不为满足旧随机 key 与顺序保留空的装饰元素。测试稳定业务 key（automation-item/run-item/editor/input）继续保留；`TestAutomationRenderBoundariesAndKeys` 改为新渲染边界和禁止内部 toolbar 的断言。动效测试集中补 ReduceMotion 与无遗留续帧。

**依赖外壳：** 外壳实现者接入上面的函数，不在本批文件清单里。需要它实际提供共用 ScopeID、chooseProject、搜索所有权、独立窗口 close/shortcut 的优先级和设置式 sidebar 行。没有这些接线时自动化组件测试通过也不能声称独立窗口验收完成。

**验收：**

- 20 个项目不增加内容头部高度；内容顶栏只有标题/搜索/新建，没有项目 rail 或第二工具栏。
- 1440×900 能列表+详情；840×520 为同一侧栏加内容单栏。名称、操作、编辑底栏均在边界内；大字号不裁字、不改写已有数据。
- 全部范围实际请求 `Automations(ctx, "")` 和未读全范围；编辑其中 p2 对象仍提交 p2，运行返回 runID 能直达；选择 p1 新建不改变全部 scope。单项目与全范围间的迟到响应均不能覆盖新状态。
- 搜索与侧栏状态组合、点击结果已读、归档失败回滚、未保存取消/关闭、预览关闭取消、立即运行防重入、滚动与键盘行为继续工作。
- 无新增依赖、cgo、裸颜色或 ui.Modal/ui.Popover；TestAutomation*、TestDesignTokenRatchet、TestNoStyledModalOrPopover 均通过后才宣称代码验证完成。

**补测：** 新增 `TestAutomationScopeAllProjects`、`TestAutomationScopeEditAndCreateProject`、`TestAutomationScopeLateResult`、`TestAutomationShellActionsAndLeaveGuard`、`TestAutomationShellClearSearchIsolation`、`TestAutomationVisualEmptyStates`；更新最小窗口测试以测包含侧栏的真实布局，不能只在 840 宽的裸自动化主体里测。已有 RunNowDoubleClickAndLateProject、RunNowDirtyChoices、LayoutEditorDismiss、ClosedStopsWork、MotionReduceMotion 全部保留并补 scope=空的分支。支持动态偏好设置的测试 API 要先核对，不编造 setter；自动测试做初始偏好两态，运行中切换在 dev 实机验收。

## 9. 自测与人工验收交接

本轮未执行下列命令。实施者在无网络依赖环境运行，外层命令超时 150 秒，Go 测试自身超时 120 秒。macOS 若没有 `timeout`，使用已安装的 `gtimeout` 或 provider 的命令 timeout，不安装工具：

```sh
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off CGO_ENABLED=0 go test ./internal/ui/ -run '^TestAutomation' -count=1 -timeout 120s
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off CGO_ENABLED=0 go test ./internal/ui/ -run '^(TestDesignTokenRatchet|TestNoStyledModalOrPopover|TestApprovalAutomationInboxNoRawColor)$' -count=1 -timeout 120s
```

设计 token 棘轮按文件检查计数，automation.go 和 automation_runs_ui.go 的现有基线均为 0；本轮继续保持 0，新文件直接使用 token，不刷新或抬高基线。不改 `design_token_guard_test.go` 或 JSON 基线绕过检查。

`internal/ui/design_token_guard_test.go:277` — `func TestDesignTokenRatchet(t *testing.T) {`

`internal/ui/modal_style_guard_test.go:13` — `func TestNoStyledModalOrPopover(t *testing.T) {`

人工验收由主代理/实现者在 dev 版本进行，本文未启动 mcode：

- 0/1/20 个项目，0/1/100 个自动化，全部/单项目范围。
- 1440×900、840×520，浅色/深色，默认/放大字号，ReduceMotion 开/关及动画中途开启。
- 三种主空态、历史空态、首次加载、已有数据刷新失败；长中文、长无空格错误、跨年时间、未知状态。
- 编辑保存/取消/点击背景/目的切换/项目切换/原生关闭；dirty 确认不丢稿。
- 启停、立即运行与返回这次 run、打开真实会话；未读/归档失败回滚；键盘 Tab/上下/Enter/Esc/Cmd+N/Cmd+S。
- 截图记录新外壳与自动化组合后的两个尺寸；不能用竞品记忆或组件单测代替真实窗口视觉验收。

**未完成与风险：** 当期竞品渲染截图及多数精确字号颜色未验证；另一份外壳方案在本副本缺失，签名待主代理合并对齐；原生窗口与动态 ReduceMotion 均需 dev 实机确认。这些不阻止交付实施文档，但在确认前不得汇报为视觉验收通过。
