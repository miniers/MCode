# 任务窗口独立外壳与看板视觉改版

日期：2026-11-10。状态：调研与实施设计；不包含代码修改。

## 0. 范围与推荐

推荐：保留 `chatState` 作为现有业务能力的适配对象，新增专用任务根视图。窗口左侧承载目的、项目范围与任务视图，右侧只承载当前内容。不要再通过聊天根视图提前返回来模拟独立外壳。

本方案只设计外壳、看板与任务/目标详情的视觉容器。不改任务生命周期、JSON-RPC 协议、存储目录、自动化内容区或调度语义。不添加依赖，不引入 cgo。

`docs/superpowers/plans/2026-11-10-kanban-ux-redesign.md` 第 0 节 D1–D6 继续有效。本轮只把 D1 的「任务 / 自动化」目的导航由顶部迁到侧栏；不把自动化放回看板/列表的展示轴。D2 的拖入完成先审阅、D6 的待处理范围及完成列默认收起均保留。自动化内容区按并行方案 `docs/superpowers/plans/2026-11-10-automation-visual-redesign.md` 实施，该文件在本工作副本尚未出现；本文冻结共用接口，内容由另一位实现者提供。

### 证据边界

- 当前工作副本 HEAD：`2e9ddcd`。读取了下文列出的源码、相关测试及 git 历史。根因结论来自静态检查，不是启动应用后的复现结果。
- 实际浅克隆并只读查看了 dsh-super-kanban、Vibe Kanban、Planka、Focalboard。每次 clone/fetch 均由 `subprocess.run(..., timeout=60)` 限时；调研目录为新建的系统临时目录。没有安装依赖，没有运行仓库脚本。
- 实际读取了 Linear 官方 board-layout、display-options、Improved peek 页面。没有登录 Linear，没有运行竞品。README 中的截图链接已记录，但本次工具不能直接审阅图片，不把图片链接当成看过截图。
- `yab guide` 失败：CLI 指向不存在的 `/Applications/yab.app/Contents/MacOS/yab`。因此改用只读网页抓取与源码调研。Linear issue 侧栏的细节仅作记忆参考，明确标注，不作为当前版本事实。
- 本任务只写文档，未运行构建、测试、mcode 或 GUI 自动化。没有后台任务。

## A. 调研与结论

### A1. 实际资料与固定版本

| 产品 | 本次方式、版本 | 可复查资料 |
|---|---|---|
| dsh-super-kanban（必看） | 实际 `git clone --depth 1`；`4c77a4d3c067d2f5e45d16c432b7fcabe0bd4265`；README 与 `client.js` | [README](https://github.com/topmindspace/dsh-super-kanban/blob/4c77a4d3c067d2f5e45d16c432b7fcabe0bd4265/README.en.md)、[client.js](https://github.com/topmindspace/dsh-super-kanban/blob/4c77a4d3c067d2f5e45d16c432b7fcabe0bd4265/client.js)、[截图目录](https://github.com/topmindspace/dsh-super-kanban/tree/4c77a4d3c067d2f5e45d16c432b7fcabe0bd4265/docs/screenshots) |
| Linear | 实际读取官方文档；没有访问登录后界面 | [Board layout](https://linear.app/docs/board-layout)、[Display options](https://linear.app/docs/display-options)、[Improved peek（2019，历史资料）](https://linear.app/changelog/2019-09-23-improved-peek) |
| Vibe Kanban | 实际浅克隆与稀疏取源码；`6883fac358ac655289ac10c459d5c2fb0640d89e` | [看板说明](https://github.com/BloopAI/vibe-kanban/blob/6883fac358ac655289ac10c459d5c2fb0640d89e/docs/cloud/kanban-board.mdx)、[KanbanBoard](https://github.com/BloopAI/vibe-kanban/blob/6883fac358ac655289ac10c459d5c2fb0640d89e/packages/ui/src/components/KanbanBoard.tsx)、[KanbanIssuePanel](https://github.com/BloopAI/vibe-kanban/blob/6883fac358ac655289ac10c459d5c2fb0640d89e/packages/ui/src/components/KanbanIssuePanel.tsx) |
| Planka | 实际浅克隆与稀疏取源码；`2cf423824a09f84df7dc3acf0ded9a700292d882` | [Card 样式](https://github.com/plankanban/planka/blob/2cf423824a09f84df7dc3acf0ded9a700292d882/client/src/components/cards/Card/Card.module.scss)、[List 样式](https://github.com/plankanban/planka/blob/2cf423824a09f84df7dc3acf0ded9a700292d882/client/src/components/lists/List/List.module.scss)、[CardModal](https://github.com/plankanban/planka/blob/2cf423824a09f84df7dc3acf0ded9a700292d882/client/src/components/cards/CardModal/CardModal.module.scss)、[Header](https://github.com/plankanban/planka/blob/2cf423824a09f84df7dc3acf0ded9a700292d882/client/src/components/common/Header/Header.jsx) |
| Focalboard | 实际浅克隆与稀疏取源码；`a84bbb65e32edf972856b329417096ac413518e9` | [kanbanCard.scss](https://github.com/mattermost-community/focalboard/blob/a84bbb65e32edf972856b329417096ac413518e9/webapp/src/components/kanban/kanbanCard.scss)、[sidebar.scss](https://github.com/mattermost-community/focalboard/blob/a84bbb65e32edf972856b329417096ac413518e9/webapp/src/components/sidebar/sidebar.scss)、[viewHeader](https://github.com/mattermost-community/focalboard/blob/a84bbb65e32edf972856b329417096ac413518e9/webapp/src/components/viewHeader/viewHeader.tsx)、[cardDialog](https://github.com/mattermost-community/focalboard/blob/a84bbb65e32edf972856b329417096ac413518e9/webapp/src/components/cardDialog.tsx) |

以上五项满足「必看 + 另外四项」。没有把 Plane 搜索摘要算作完成调研。未调研 Huly、AFFiNE，不补写未经查看的当前界面结论。外部代码只供结构观察，尤其 Planka 当前源码标注 Fair Use License，不复制代码或资源。

### A2. 结构与顶栏对比

| 产品 | 窗口、侧栏 | 顶栏实际放什么 | 详情形态及可辨识性 |
|---|---|---|---|
| dsh-super-kanban | README 说明全页四列与聊天右侧紧凑布局；入口在宿主左侧栏，不是插件自建原生窗口 | v3 主行：标题/扫描统计、搜索、主按钮、图标/更多菜单；下一条 slimbar 放工作区与筛选。并非「所有控件完全消失」 | `client.js` 的 `.sk-drawer` 固定右侧、最高 600px/94vw；实心底色、左边框、左向阴影、独立 head/body/foot、slide-in。紧凑侧栏中 drawer 填满宿主。这些明确的边界比圆角本身重要 |
| Linear | 官方文档确认跨视图的 board/list、分组、隐藏列；侧栏组织与 issue 右侧属性区的细节为记忆，未验证当前外壳 | 官方文档确认右上 Display options、看板/列表图标；列头 + 新建，列菜单隐藏列。顶栏其余项目未验证 | 官方确认卡片 Space peek 与打开完整 issue 是两条路径。记忆中的完整 issue/属性侧栏不等于本任务要做的常驻右侧详情。不可声称 Linear 当前采用同一款 drawer |
| Vibe Kanban | 看板在项目上下文；源码拆分 board、filter bar、right sidebar container，右侧 panel 可表示 issue、workspace 与创建流程 | 官方仓库文档明确顶端 search/filter/sort；状态 tabs 与隐藏状态分开；不把项目列表塞进每张卡的操作区 | 官方文档明确点卡打开右侧 issue panel；源码独立头部、底部分隔、ID、更多、X 关闭、内部滚动。打开卡与多选使用不同 ring |
| Planka | 当前 Header 有返回项目、项目名、收藏/项目列表切换、用户与通知；左侧是否常驻取决于页面，不能推断为 Linear 式固定项目树 | 全局 Header 与列内操作分开；每列有标题与悬停更多，列底有添加卡片。未完整读取 board 局部工具条，不断言其所有按钮 | `CardModal` 居中，常规宽 880px，小屏 95%；不是右侧栏。独立弹窗避免用户把内容误认为一列，但会遮挡看板；不采用其宽屏详情形态 |
| Focalboard | 240px 侧栏，工作区标题、board 导航、独立背景和滚动；主体是 board/view | viewHeader 明确拆出属性显隐、group、sort、search、filter、新建，工具偏多，适合通用数据库，不适合照搬到 mcode | CardDialog 使用独立 Dialog，详情可 fullwidth；大字号正文和独立外层强调阅读区域。不是常驻右栏，不当成 drawer 依据 |

### A3. 列、卡片与反馈对比

| 产品 | 卡片密度：字段、间距、圆角、阴影、边框 | 列头与空列 | 拖拽、颜色与字号 |
|---|---|---|---|
| dsh-super-kanban | 必须读后出现的 **v3 覆盖规则**，不能用旧 CSS 下结论：12px 圆角，padding 12/14/10px，gap 6px，0.5px 边框，无阴影；标题两行 13px/600；元信息 11px，工作区、时间、指标、细进度可出现 | 列无边框/底色/圆角；列头 12px/600 + 裸数字 11px，细底线；卡间 8px；空列左对齐短文字，无虚线插画框 | 拖动源 opacity .4，目标列头底线和列体 outline，不上浮、不缩放；单色主层次，语义色点到为止。drawer 沿用前面的左边界/阴影规则，不能误以为 v3 删除了它 |
| Linear | 官方确认 **不显示 description**，字段可选；大量字段不能全部放进卡片。精确 padding、radius、shadow、font 未取得当前 CSS，不填数字 | 官方确认列头新建、隐藏列、Show empty groups；空列当前外观未验证 | 官方确认拖跨组采用目标属性、手动排序、横向滚动与键盘选择；实际拖拽 ghost 和颜色未查看。记忆参考：弱边框、标题优先；不能据此指定复制数值 |
| Vibe Kanban | 源码包含 ID、标题、可选 description preview、priority、assignees、最多两项 tag 与溢出数、PR/关系信号；明显比本方案密。卡壳 `p-base`、border，颜色用语义 class；本次未解析 Tailwind 数值，因此不声称确切 DIP/圆角值 | sticky 列头，状态点 + 名称 + ghost +；grid 列可在 200–400px 区间。空列保留可投放容器与创建入口 | 拖动时 shadow-lg；多选 accent ring、已打开 neutral ring；文档说明目标高亮/占位。字体 `text-sm`、正文/低对比文字分层；不照搬彩色标签堆叠 |
| Planka | 白底卡片，3px 圆角，0 1px 0 阴影；卡间 8px；操作钮平时 opacity 0、悬停出现。卡内容 CSS 本次未完整读取，字段清单和精确字号未验证 | 列宽 272px、灰底/3px 圆角、内 padding 8px；标题加更多，列底添加卡片。空列保留最小可布局容器，不放大插画 | 本次读到 draggable wrapper，仅确认 cursor/outline；拖动 ghost 未验证。深灰标题、灰色辅助控件；其卡片与列的边界做法可比较，不复制固定色值或阴影 |
| Focalboard | 4px 圆角、padding 12/16px、卡间 16px、1px 轮廓阴影 + 2/4px 阴影；标题 600、20px 行高、最多五行；可见属性由 view 决定 | 列头属性 label + count/calculation + 新建/菜单；空列具体提示文案未核实，不凭记忆补写 | 目标列淡色背景；选中蓝色低透明底，悬停显示 options；卡片正文用主题变量，详情标题 32px。五行标题、强卡影和大字号详情不适合直接迁入本项目 |

### A4. 五条采用结论

1. **从 dsh-super-kanban 借鉴「轻列、实心卡、独立 drawer」**。列框与卡框不能同样抢眼。mcode 去掉整列卡片化容器，只保留列头、空白和可投放边界；详情必须有自己的背景、分隔、头部。收益：用户看懂层级；代码只有一层卡片外观；agent 能通过独立 Label 定位。
2. **从 Linear 借鉴「卡片不是缩小的详情页」**。保留两行标题与一条非重复信号；description、依赖链接和恢复操作进入详情/菜单。收益：扫描更快；同一 `kanbanTaskSignal` 决定文本；不会产生多套 agent 解析入口。
3. **从 Vibe Kanban 借鉴「选中与打开是不同状态」**。详情打开卡有轻底色与常驻边框；多选有勾选；键盘焦点有外圈。收益：不再把三种状态混成一个蓝框；减少状态视觉分支冲突；UI 测试能分别断言。
4. **从 Planka 借鉴「操作按需出现」而不借鉴其模态详情**。更多与主动作只在 hover/focus 出现，空间始终预留，点击不改变卡高；宽屏详情仍保留看板。收益：低噪声、无跳动；复用现有动作策略；坐标与键盘序稳定。
5. **从 Focalboard 借鉴「范围导航与内容工具分开」而不复制通用数据库工具条**。设置式 220 DIP 侧栏负责目的/项目/视图/目标；内容顶栏只留标题、展示方式、搜索、新建。收益：840 宽仍可操作；业务内容不感知聊天 chrome；自动化只提供目的内容，不再造第二个项目选择器。

## B. 现状根因与代码证据

### B1. 设置侧栏是复用基准，不是把 SettingsState 搬到任务窗口

- 原生窗口、全局复用和尺寸记忆已有可靠样板：

internal/ui/settings_window.go:135 `func openSettingsWindowNative(owner *chatState, page string) *mygo.Window {`

internal/ui/settings_window.go:170 `TrafficLightPosition: &mygo.Point{X: 18, Y: 16},`

internal/ui/settings_window.go:172 `StateKey:             "settings-window",`

- 侧栏宽度为 `min(220, width*0.35)`；在任务窗口最小宽 840 时为 220。列表底色、行高、内缩、圆角和选中态直接沿用数值及 Theme token：

internal/ui/settings.go:1549 `ui.Column(c.Key("settings-dialog-nav")).Width(min(float32(220), width*0.35)).FillHeight().Children(func() {`

internal/ui/settings.go:1207 `func renderSettingsNav(c *ui.Context, st *SettingsState) {`

internal/ui/settings.go:1280 `ui.Column(c.Key("settings-nav-col")).Fill().Background(ThemeSidebarBg(c)).Children(func() {`

internal/ui/settings.go:1332 `func renderSettingsNavItem(c *ui.Context, st *SettingsState, item settingsNavDef, hits []settingsItemHit) {`

internal/ui/settings.go:1336 `Height(RowHeight).`

internal/ui/settings.go:1339 `Radius(RowCornerRadius).`

internal/ui/settings.go:1358 `bg = bg.Mix(ThemeSelected(c), sel)`

internal/ui/settings.go:1369 `iconColor = ThemeAccent(c)`

internal/ui/settings.go:1388 `tElem.FontWeight(500).TextColor(ThemePrimaryText(c))`

不直接调用 `renderSettingsNavItem`：它绑定 `SettingsState`、页搜索和懒加载。首期复用视觉写法与 token，不为两种导航引入通用路由/导航框架，也不修改设置文件。设置当前仍有横跨窗口的标题占位；任务窗口按本轮约束把交通灯占位收入左侧栏，不机械复制整棵设置树。

### B2. 独立原生窗口已存在，但根构建仍依赖聊天路径

internal/ui/windows.go:312 `WithOpenTasksWindow(func(view string) {`

internal/ui/tasks_window.go:104 `state, content := buildRoot(client, allOpts...)`

internal/ui/tasks_window.go:113 `StateKey:             "tasks-window",`

internal/ui/root.go:44 `func buildRoot(client Client, opts ...Option) (*chatState, func(c *ui.Context)) {`

internal/ui/root.go:72 `state.init(c)`

internal/ui/root.go:77 `if state.tasksWindowView != "" {`

internal/ui/root.go:86 `renderKanbanWindow(c, state)`

internal/ui/model.go:660 `s.startQueueWatch()`

internal/ui/model.go:663 `s.loadProjects()`

internal/ui/model.go:1037 `ths, err := s.client.Threads(ctx, pid)`

`state.init` 在任务分支之前运行；`loadProjects` 还拉所有项目会话、供应商并自动选会话。不是「任务窗口完全显示了聊天 UI」，而是任务根依赖聊天初始化、局部布局及测试入口。这是需要去掉的结构耦合。保留 `newChatState` 与现有业务方法，不做独立业务状态的全面迁移。

### B3. 顶栏和右栏缺少空间层级

internal/ui/kanban_modal.go:267 `parts := ui.SegmentedBase(c.Key("kanban-destination"), &idx, 2)`

internal/ui/kanban_modal.go:289 `ui.Row(c.Key("kanban-header-filters")).FillWidth().Height(kanbanTopBarHeight).Padding(0, Space5).`

internal/ui/kanban_modal.go:292 `kanbanProjectMenu(c, s, p.scopeID, func(id string) { s.setKanbanScope(id, id == "") })`

internal/ui/kanban_modal.go:309 `kanbanGoalMenu(c, s, ks, p)`

internal/ui/kanban_modal.go:122 `wideDetail := contentWidth >= 980 && p.destination != "automations" && (detailTaskID != "" || goalDetailOpen)`

internal/ui/kanban_modal.go:130 `ui.Column(c.Key("kanban-detail-pane")).Width(480).Shrink(0).FillHeight().Background(ThemeSurface(c)).Transition(motionSidePanel(c, false)).Children(func() {`

internal/ui/kanban_detail.go:102 `Padding(Space5).Gap(Space3).Transition(motionSidePanel(c, false)).Children(func() {`

当前两条 52 DIP 顶栏再加 32 DIP titlebar 占位。详情固定 480，没有左分隔与拖拽手柄；面板外层和内容层各套一次滑入。功能已存在，但容器没有建立清晰身份。移除内容层滑动，保留面板层一次滑动。

### B4. 列和卡同样卡片化，且仍有高度跳动

internal/ui/kanban_cards.go:373 `Background(ThemeSurface(c).Alpha(0.55)).`

internal/ui/kanban_cards.go:487 `if card.Hovered() || card.FocusWithin() {`

internal/ui/kanban_cards.go:521 `Button(c.Key("kanban-task-"+task.ID+"-more"), ButtonOpts{`

internal/ui/kanban_cards.go:545 `if task.Blocked && len(task.DependsOn) > 0 {`

internal/ui/kanban_cards.go:597 `if st.rescueMenuTaskID == task.ID && !kanbanTaskBusy(ks, task.ID) {`

internal/ui/kanban_cards.go:795 `avail := winWidth - 2*16 // 内容区左右各 16pt padding`

internal/ui/kanban_cards.go:876 `Background(ThemeSurfaceCard(c)).`

internal/ui/kanban_cards.go:942 `emptyLabel := "暂无任务"`

卡片已采用标题和信号，不能把旧方案中「每卡还有多排元数据」当作当前事实。但依赖按钮、卡内展开菜单和常驻 chevron 仍增加密度；半透明卡片叠在实心列上，层级反转。列宽又按整个窗口算，新增左栏后会继续算错，需要用实际 board viewport。

### B5. 选定范围存在双源，不能只挪项目按钮

internal/ui/kanban_data.go:15 `changed := !ks.initialized || ks.projectID != projectID`

internal/ui/kanban_data.go:25 `func (s *chatState) setKanbanScope(projectID string, all bool) {`

internal/ui/workspace.go:154 `if len(s.projects) > 0 {`

internal/ui/workspace.go:156 `ws.projectID = s.projects[0].ID`

空字符串既表示「全部项目」，又在 workspace helper 中表示「尚未选择，回退首项目」。新外壳必须以 `ks.scopeID` 为唯一范围，不能通过 `setWorkspaceProject("")` 代表全部。卡片混合项目、详情宽度和键盘项目上下文也必须改读同一范围。

### B6. git 历史说明本次应做增量重构

- `8d32a65` 已拆开 destination 与 viewMode 并整理 Esc；保留这两个状态轴，不重新混合。
- `1e59041` 引入宽屏右侧详情、目标详情与窄屏 sheet；本轮增强容器，不推翻结果、改动、会话和审阅闭环。
- `b80fcfd` 补目标返回、多选与键盘；卡片不能删掉 `Focusable`、详情关闭后的焦点回归及安全动作策略。
- `f53231c` 抽离 Sheet 与 motion_presets；继续使用，不另造弹窗框架。

## C. 外壳设计

### C1. 宽窗口线框（1440 × 900）

```text
┌──────────────────────┬───────────────────────────────────────────────────────────┐
│ ● ● ●                │ 当前标题                    [看板/列表] [搜索] [新建 ▾] │
│                      ├───────────────────────────────┬─┬─────────────────────────┤
│ ▣ 任务               │ 待办 12     进行中 3   …      │↔│ 任务详情       […] [×] │
│ ◷ 自动化             │ ┌────────┐ ┌────────┐         │ │ 标题（最多两行）        │
│                      │ │标题两行│ │标题两行│         │ │ 状态 / 目标             │
│ 全部项目             │ │一条信号│ │一条信号│         │ │ 结果  改动  会话         │
│   mcode              │ └────────┘ └────────┘         │ │ ─────────────────────── │
│   another-project    │                               │ │ 独立滚动内容            │
│                      │ 列内纵滚；看板横滚            │ │                         │
│ 全部任务             │ 已完成折叠轨在最右            │ │                         │
│ 待你处理          7  │                               │ │                         │
│                      │                               │ │                         │
│ 目标                 │                               │ │ ─────────────────────── │
│   原生任务窗口       │                               │ │ 当前唯一主动作          │
│   发布版本           │                               │ │                         │
└──────────────────────┴───────────────────────────────┴─┴─────────────────────────┘
      220 DIP                  弹性 board                 7       默认 480 DIP
```

右栏从内容顶栏下方开始，绝不挤压顶栏。图示不表示所有列能同时露出；四个展开列与完成折叠轨仍可横滚。禁止为了「全列同时显示」把卡片压到最小宽以下。

### C2. 840 × 520 窄窗口线框

```text
┌──────────────────────┬──────────────────────────────────────────────────────────┐
│ ● ● ●                │ 全部任务       [▦/☷] [搜索] [＋▾]                      │
│ 任务                 ├──────────────────────────────────────────────────────────┤
│ 自动化               │ 待办 12                 进行中 3     → 横向滚动          │
│                      │ ┌─────────────────┐ ┌─────────────────┐                  │
│ 全部项目             │ │标题             │ │标题             │                  │
│   mcode              │ │一条信号         │ │一条信号         │                  │
│                      │ └─────────────────┘ └─────────────────┘                  │
│ 全部任务             │                                                          │
│ 待你处理          7  │                                                          │
│                      │                                                          │
│ 目标                 │                                                          │
│   原生任务窗口       │                                                          │
└──────────────────────┴──────────────────────────────────────────────────────────┘
        220 DIP                              619 DIP（另有 1 DIP 分隔）

点卡后：整窗 sheet，不再把 board 压成不可用的一条。
┌─────────────────────────────────────────────────────────────────────────────────┐
│                       主题 scrim                                                 │
│          ┌──────────────────────────────────────────────────────┐                 │
│          │任务详情                                    […] [×]│                 │
│          │标题 / 状态 / tabs                                │                 │
│          │独立滚动内容                                      │                 │
│          │──────────────────────────────────────────────────│                 │
│          │当前主动作                                        │                 │
│          └──────────────────────────────────────────────────────┘                 │
└─────────────────────────────────────────────────────────────────────────────────┘
```

840 宽时不新增汉堡菜单、不折叠项目到工具条。侧栏仍为设置式 220 DIP。右侧标题弹性截断；看板/列表用已有图标和完整无障碍名；搜索保留 120 DIP；新建可缩成图标分裂按钮。搜索输入不因缩窄自动清空。字体放大后先截断标题，再将搜索改成聚焦展开的输入，不换成第二条常驻顶栏；展开搜索时可临时替换标题区域，Esc 还原。

### C3. 侧栏区段与状态

- 顶部交通灯占位 `HeaderHeight`（45 DIP），空白区域 `DragWindow`。背景 `ThemeSidebarBg` 贯穿到顶部。非 macOS 延续现有平台 titlebar helper，不假定所有平台都画交通灯。原生 `TrafficLightPosition{18,16}`、`StateKey: "tasks-window"`、1140×720 默认尺寸及 840×520 最小尺寸不变。
- 目的区只有「任务」「自动化」。项目区位于目的区下；固定「全部项目」，再按现有项目顺序列项目。不要在每个目的下复制项目列表。项目过多时项目区自身滚动；不做新排序或项目管理能力。
- 任务专属区：`全部任务`、`待你处理`，再是目标列表。只在存在目标时出现「目标」分组头；它负责区别视图和目标，不是说明文案。无目标时整组省略。目标标题单行截断，hover Tooltip 可显示完整文本。
- 「待你处理」右端裸数字；零时不显示数字。计数来自当前项目范围的原始快照，不受搜索影响；任务按 ID 去重，包括审批、失败、评审，加 `plan_review` 的目标，排除自动化失败。点击后内容过滤，但目标计划确认入口仍在同一列表区里可定位，不能只增加计数却没有可点击项。目标行以警示图标 + `Label("确认目标计划 "+ID)` 表示，不新增长说明。
- `全部任务` 清除 goalID 与 attention 视图；点目标清除 attention 视图并设置 goalID。项目切换时保留任务/自动化目的、看板/列表方式与两份搜索；清除不属于新范围的目标/详情/多选。跨目的切换只隐藏原目的详情，不销毁内容滚动状态；切回同范围恢复。项目真正切换才取消过时详情请求。
- 每个区段内各有选中态：目的选中、项目选中、任务视图/目标选中是三条不同轴，不强求整条侧栏只亮一项。行与设置一致：`RowHeight` 28、`Space3` 水平 padding、`Hairline` 上下 margin、`RowCornerRadius` 7、`Space3` 图文间距、`IconMD`、`TypeBody`。选中 `ThemeSelected`、500 字重/主文字、强调色图标；未选次文字；hover `ThemeHover`，不整行填强调蓝。
- 区段用 `Space5` 竖向空白，不画三四条粗分隔；目的区固定，项目区与专属区分配受限滚动高度，底部不堆设置/刷新/帮助按钮。项目区最多约三分之一可用侧栏高，其余空间给专属区；在 520 高保住两个目的、全部项目、当前项目与待处理入口。

### C4. 外壳/自动化的接口

不是注册插件系统。固定两个目的，用 switch 调用。建议在新 `tasks_window_root.go` 定义一个小型 `tasksDestinationView`，只含 `Title string`、`Sidebar func()`、`HeaderActions func()`、`Body func()`；闭包由目的适配函数按当前 `s`、scope 快照构造。

- 外壳拥有：原生窗口、目的切换、`ks.scopeID`、公共项目列表、titlebar、内容顶栏布局、toast、公共 Esc 优先级和根生命周期。
- 目的提供：标题、专属侧栏区段、顶栏动作、内容。任务 `HeaderActions` 只画看板/列表、搜索、新建；自动化只画其搜索、新建，不显示看板/列表。
- 自动化消费相同的 `scopeID`（空字符串明确代表全部）和自己的 `automationSearch`，不能另调用 `currentWorkspaceProjectID` 回退首项目，也不能在内容里再画项目 picker/目的导航/第二份搜索。
- 外壳方在 `tasks_window_root.go` 定义并接入适配，自动化方只在其 `automation*.go` 提供 `renderAutomationDestinationSidebar(c,s,scopeID)` 与目的动作/内容函数。具体自动化侧栏项归并行方案；首期接口可为空，不放占位说明。
- 共用文件 `tasks_window*.go`、`kanban_modal.go`、`kanban_state.go`、`ui_state.go` 由本方案指定的单一批次持有，自动化实现者不编辑。只需一个调用适配，不允许两方各建一套 shell。

## D. 看板视觉与 token 映射

### D1. 基础层次

| 元素 | 实施规范 | 已有 token / Theme |
|---|---|---|
| 左栏 | 与设置一致的实心底；右侧 1 DIP 线 | `ThemeSidebarBg`、`ThemeBorder`、`Hairline`、`ListInset` |
| 内容底 | 主内容平面，无「套一张大卡片」的圆角/阴影 | `ThemeSurfaceMain`；标题 `TypeTitle`、500；主文 `ThemePrimaryText` |
| board 外边距/列间距 | 四周 16；列间 16；不再 14 的散落常量 | `Space5` |
| 列 | 透明、无外边框、无圆角、无阴影；头和 body 分开 | `ui.Transparent`、`ElevNone` |
| 列头 | 高 32；14 图标 + 12pt/600 列名 + 裸数量；底部细线；计数不做胶囊 | `ControlLG`、`IconSM`、`TypeFootnote`、`TypeCaption`、`Space2`、`ThemeDivider`、`Hairline` |
| 卡片 | 实心底，1 DIP 常规边框，8 圆角，内边距 12；卡间 8；内 gap 6 | `ThemeSurfaceCard`、`ThemeBorder`、`Hairline`、`RadiusMD`、`Space4`、`Space3`、`Space2`、`ElevNone` |
| 标题 | 最多两行，13pt/500，长文本完整 Label/Tooltip；优先级只保留现有单个图标 | `TypeBody`、`ThemePrimaryText`、`IconSM`；字重走已有主题适配 |
| 信号 | 最多一行、11pt；没有信号不强留空行 | `TypeCaption`、`ThemeSecondaryText` / `ThemeWarning` / `ThemeDanger` |
| 搜索 | 28 高；140 常态宽、120 下限；中性底与细线 | `ControlMD`、`RadiusSM`、`ThemeSurfaceCard`、`ThemeBorder`、`Space3` |
| 空列 | 列头计数 0 + 留白，待办保留现有快速添加；不重复五遍「暂无任务」 | 不新增插图、虚线盒或说明；空投放区最小 64 DIP 用命名 token |
| 全局无任务/无匹配/错误 | 保留一处短结果和一个恢复入口；错误信息不当作装饰性说明删除 | 现有 `TypeBody`、`ThemeSecondaryText`、`ThemeDanger` 与 Button |
| 已完成 | 默认折叠成 44 DIP 轨，保留名称、数量、展开按钮和合法 drop 命中区；展开后恢复普通列 | 新几何 token；色用 `ThemeSecondaryText`，不整列刷绿 |

`ThemeWindowBg` 与 `ThemeSidebarBg` 当前都映射 canvas，不依赖函数名称假设它们自带色差。主区改用 `ThemeSurfaceMain`，详情用 `ThemeSurfaceRaised`；任何主题下仍靠常驻分隔线和头部建立身份，不能只靠微小色差。

internal/ui/theme.go:393 `return ThemeSurfaceCanvas(c)`

internal/ui/theme.go:398 `return ThemeSurfaceCanvas(c)`

### D2. 信息密度与稳定布局

- 沿用 `kanbanTaskSignal` 的优先规则。单项目卡只显示标题、优先级图标及一条信号；全部项目时将项目名作为同一信号行的末尾上下文，不再加第二行、胶囊或重复项目图标。无信号时项目名占该行。
- 依赖数量保留在信号里，前置任务的逐项按钮移到已有详情依赖区。不新增依赖编辑功能。
- hover/focus 的主动作和更多菜单放进预留的尾部操作槽，视觉浮于标题行尾部，不改变标题可用宽度/换行/卡高。更多改用水平省略号，保留旧稳定 Label。正常态操作不绘制、不拦截输入、不进入 Tab；聚焦卡片后可 Tab 到主动作与更多。
- `moveMenuTaskID` / `rescueMenuTaskID` 对应的操作内容改为 `appPopover` 内唯一卡片或现有 ContextMenu，不在卡内插入 Wrap 行。保留提醒、中断、重新打开、删除与忙碌/权限校验，不能以「精简」为由删功能。
- 选择工具条只在选择非空时出现，保留已选数与有能力执行的动作；去掉常驻「仅待办任务可多选」文字。不能增加批量接受。

### D3. 拖拽、打开、选择与焦点

| 状态 | 样式与行为 |
|---|---|
| hover | 常规边框改强调色，底色用 `ThemeHover` 叠加；`motionFeedback(c)`，不缩放/上浮 |
| 详情打开的卡 | `ThemeSelected` 弱底 + 常规边框；不显示多选勾。详情和卡片通过同一 task ID 对应 |
| 多选 | 勾选图标 + `ThemeSelected` 底，边框 `ThemeAccent`；不依赖颜色单独表达 |
| 键盘焦点 | 2 DIP 外圈 `ThemeAccent`，与多选勾分离。外圈画在预留 inset 内，避免卡高随 1→2 DIP 边框变化 |
| 拖动源 | 原卡降透明度至新 token `.4`；保留布局位置，不插入重复自绘 ghost。MyGo 原生 drag preview 的实际能力需实现者读已安装版本确认，首期不承诺可定制 shadow |
| 合法 drop 列 | 预留 2 DIP 内描边；强调色轮廓 + `ThemeSelected` 弱底；布局不移动；沿用 `kanbanActions(...).targets` 判定 |
| 非法 drop 列 | 不亮可投放框，不制造成功预览；沿用现有拒绝逻辑。needs_you 不能因有空位就接受任意任务 |
| 拖到已完成 | 完成轨可命中；review 只打开审阅，不乐观消失、不显示绿色成功；确认且服务端返回 done 后才进入完成列 |
| 完成列展开/收起 | `motionCollapse`，不对整个 board 做弹跳。键盘可聚焦折叠轨并 Enter 展开；隐藏的 done 卡从视觉焦点序列剔除 |

### D4. 必须新增的命名 token（集中于 tokens.go）

只新增几何与状态常量，不新增色盘、不修改系统主题。以下为设计初值，运行验收可微调，但只能改 token，不在调用处插数字。

| 建议名称 | 值 | 理由 |
|---|---:|---|
| `TasksSidebarWidth` | 220 | 与设置实际侧栏一致；最小窗口仍有 619 内容宽 |
| `TasksToolbarHeight` | 52 | 延续现有工具条舒适高度，但只保留一条；不与交通灯冲突 |
| `TasksSearchWidth` / `TasksSearchMinWidth` | 140 / 120 | 延续已验证布局预算；更窄只折叠搜索展示，不删搜索状态 |
| `KanbanColumnMinWidth` | 270 | 保留原有卡片可读下限，不为塞列而改交互密度 |
| `KanbanDoneRailWidth` | 44 | 只够折叠名称/计数/展开；不占整列 270 |
| `KanbanEmptyDropMinHeight` | 64 | 空列有足够可命中的投放区域，不靠空文字撑高 |
| `KanbanCardActionSlotWidth` | 80 | 容纳现有短主动作和更多；hover 前后不改变两行标题布局 |
| `KanbanFocusRingWidth` | 2 | 独立键盘轮廓/合法投放轮廓；不得在调用处 `Hairline*2` 绕棘轮 |
| `KanbanDragSourceOpacity` | .4 | 与源卡可见且不冒充实体位置的反馈一致 |
| `KanbanBoardPaneMinWidth` | 400 | 打开详情后至少能看到一张 270 卡及下一列方向 |
| `KanbanDetailMinWidth` / `KanbanDetailDefaultWidth` / `KanbanDetailMaxWidth` | 360 / 480 / 640 | 360 可读；480 延续现状；640 防详情吞掉任务上下文 |

现成 `sidePaneHandleWidth` 7、`sidePaneHandleStep` 8 直接用于详情拖拽的几何计算；不要新增同值的第二套常量。`Space*`、`Radius*`、`Type*`、`Hairline` 直接用于样式调用，不写 `Space3/2`。当前 token guard 只显式列出基础 token，新 token 的检测名单与测试由批次 1 一并补齐，不能提高 baseline 或加 exemption 掩盖新增裸值。

internal/ui/design_token_guard_test.go:68 `var designTokenNames = map[string]bool{`

internal/ui/design_token_guard_test.go:277 `func TestDesignTokenRatchet(t *testing.T) {`

internal/ui/side_pane.go:267 `sidePaneHandleWidth float32 = 7`

internal/ui/side_pane.go:269 `sidePaneHandleStep float32 = 8`

## E. 详情侧栏

### E1. 身份与头部

- 真正的右侧 pane，非「看板旁又一列」。外层 `ThemeSurfaceRaised`、左侧常驻 `Hairline/ThemeDivider`、无圆角、无环绕阴影。board 继续 `ThemeSurfaceMain`。分隔手柄中间的线就是 pane 左边界，不叠画两条线。
- 独立头部：`任务详情` 或 `目标详情` + 更多 + 右上 X；标题在下一行最多两行。目标返回入口是头部左侧带箭头短按钮，关闭 X 始终存在，不再用同一个「关闭」按钮同时承担返回与关闭。
- 保留已有 `结果 / 改动 / 会话`、元信息、冲突提示、评审反馈和主动作。头部、tabs、底部动作固定；只有正文滚动。不要在整个 pane 外放统一 `Padding(Space5)` 把分隔线/头部缩进去；padding 放头/body/footer。
- 保留现有测试定位 Label，例如 `kanban-task-detail`、`kanban-task-detail-close`；关闭按钮可用 X 图标但 Label 稳定。补充可读 Tooltip「关闭任务详情」，不能通过给每处都叫「关闭」制造歧义。

### E2. 宽度与持久化

用右侧实际宽 `R = 窗口宽 - TasksSidebarWidth - Hairline`，不是 `c.Size()` 的整个窗口。允许内联详情的条件：`R >= KanbanBoardPaneMinWidth + sidePaneHandleWidth + KanbanDetailMinWidth`，即 767 DIP；任务窗口整体约 988 DIP 起可内联。在 1140 默认窗口，R=919，默认详情 480、手柄 7、board 432；在 840，R=619，必须 sheet。不要沿用 `contentWidth >= 980` 的旧整窗判断。

- 实际详情宽 `clamp(偏好宽, 360, min(640, R-400-7))`；因窗口变窄的自动 clamp **不写回偏好**。拉大窗口恢复用户原宽。
- 手柄 7 DIP 命中区、`RoleSplitter`、`CursorResizeEW`；常驻 1 DIP 线；hover/drag 亮强调线。直接使用已有 `Dragged` / `DragEnded` 模式；焦点状态左右键每次 8 DIP，左增右减，拖动和键盘同一 clamp。
- 不采用聊天分栏「压到一半自动关闭」语义，拖到最小值只停住；用户关闭交给 X。复用 `sidePaneDraggedWidth` 纯计算，不能直接把任务注册进聊天的 `sidePanes`，否则会多出聊天菜单/快捷键与错误的顶栏 inset。
- 偏好只保存到既有 `ui-state.json` 的新增可选字段 `tasks_detail_width`，挂在 `uiStateFile`，不挂在会被主窗口整段赋值的 `UIState`。复用唯一串行 writer、按字段 dirty/generation 合并、临时文件替换与失败处理，不另开路径/写者。
- 只在用户拖完/按键调整时异步记忆；默认 headless 不写盘，真实 `OpenTasksWindow` 显式启用任务偏好持久化。**不要**为了写详情宽而调用 `WithUIStatePersistence`：那会启用聊天侧栏/草稿恢复并覆盖其他窗口字段。
- 读缺失/非法值取 480；损坏 JSON 放弃写盘；主窗口保存不覆盖 tasks_detail_width；任务宽保存不覆盖主窗口 sidebar、模型、项目顺序。测试使用现有数据目录覆盖与写者等待，不接触真实用户数据。

internal/ui/ui_state.go:44 `type uiStateFile struct {`

internal/ui/ui_state.go:608 `func writeLastSelections(onErr func(error)) {`

internal/ui/ui_state.go:665 `file.UIState = ui`

internal/ui/ui_state.go:674 `file.UIState = pending.ui`

internal/ui/side_pane.go:388 `func renderSidePaneHandle(c *ui.Context, s *chatState, p *sidePane, width *float32) {`

### E3. 动效与窄窗

- 只有最外层 pane 用 `motionSidePanel(c, false)`。`kanban-task-detail` 和 `kanban-goal-detail` 内容根不再加 sidePanel transition。同一 pane 内切 task ID 只用 `motionFeedback`，不反复滑整栏。
- 进入沿用 `DurSheet` 280ms/EaseSnap，退出沿用 `DurBase` 200ms/EaseOut；不额外改 motion_presets。ReduceMotion 经 `motionTransition` 直接到终点，不能出现先位移一帧再跳回。
- 840 宽用 `ui.DialogBase` + 唯一内层卡片，外层 panel 只有 MaxWidth/MaxHeight 与布局约束；卡片 `min(settingsSheetWidth, windowWidth-Space8)`，高受窗高限制；背景来自 Theme，遮罩走既有 ScrimOpacity 表达，不引入 RGB/Hex。sheet 只用 `motionDialog` 一次，不叠加 sidePanel。
- 内联↔sheet 临界切换不清 task ID、tab、反馈草稿或请求 revision；同一时刻只挂一个可交互详情实例。转换本身不做双面板飞行动画。打开后焦点到详情头/首个可操作项，关闭回原卡；原卡消失则按现有邻近焦点规则回退。
- 自动化目的不能意外挂出任务 sheet。当前窄窗任务详情判断没有目的检查，需要与宽屏一致加保护。

internal/ui/motion_presets.go:88 `func motionSidePanel(c *ui.Context, fromLeft bool) ui.ElementTransition {`

internal/ui/motion_presets.go:15 `if reduceMotion(c) {`

internal/ui/kanban_modal.go:207 `if !wideDetail && detailTaskID != "" {`

### E4. 组件取舍

实际查看 [CrychicUI](https://github.com/ELY-Open-Source/CrychicUI) 的公开目录/README；它提供大量组件，但本任务明确不添加依赖。这里不是开发新的通用组件：导航复用设置行的现有视觉，splitter 复用仓库 `side_pane.go` 的输入事件与几何，sheet/popover 复用仓库已有底层。只加任务局部容器和最小拖拽 glue，不重写动画或 overlay。没有引入的包路径与版本。

## F. 实施批次（文件互不重叠）

六批是**文件所有权**，不是要求按编号并行提交。建议依赖：1 → 2 →（4、5 可并行）→ 3 组装 → 6。批次 3 的作者最后接线，避免多人同时改 `kanban_modal.go`。所有「新增文件」都是建议目标，不代表本次已创建；锚点均来自改动前现存代码。

冻结交接约定：批次 2 在 `kanbanState` 准备 `viewFilter`（空/attention）、`doneExpanded`（默认 false）、`detailWidth` 及任务持久化启用标志；范围仍用 scopeID。批次 4 提供 `kanbanColumnWidthFor(boardWidth, expandedCount, collapsedCount)` 与一致的卡片视觉；批次 5 提供 `kanbanDetailLayout(availableWidth, preferredWidth)` 和 `renderKanbanDetailSplit(c,s,ks,availableWidth,board func(float32),detail func())`。批次 3 是唯一调用者/接线者，不让这些 helper 自己重新读取整窗宽度。

### 批次 1：设计 token 与守卫

**文件清单**：`internal/ui/tokens.go`、`internal/ui/design_token_guard_test.go`；新增 `internal/ui/tasks_visual_tokens_test.go`。

**锚点**：

internal/ui/tokens.go:24 `// 设置界面的间距与尺寸（DIP）。`

internal/ui/design_token_guard_test.go:68 `var designTokenNames = map[string]bool{`

**改法**：集中加入 D4 token；守卫显式识别新 token 及其算术，不放宽颜色、字号、圆角、间距扫描；不修改 `theme.go` 色盘，不提高 baseline。旧局部常量迁移由调用文件所有者完成。

**验收**：每个新样式有唯一命名值；新生产文件零裸色/字号/圆角/间距；原有 token ratchet 与 modal guard 不需要豁免。

**补测**：新 token 的正值和几何关系；新 token 算术仍被计为违规；`TestDesignTokenRatchet`、`TestNoStyledModalOrPopover` 保持通过。

### 批次 2：共享展示状态与宽度持久化

**文件清单**：`internal/ui/kanban_state.go`、`internal/ui/ui_state.go`；新增 `internal/ui/tasks_window_persistence_test.go`。

**锚点**：

internal/ui/kanban_state.go:79 `type kanbanState struct {`

internal/ui/kanban_state.go:98 `viewMode         string`

internal/ui/ui_state.go:44 `type uiStateFile struct {`

internal/ui/ui_state.go:608 `func writeLastSelections(onErr func(error)) {`

internal/ui/ui_state.go:632 `// 文件存在但解析失败：这次放弃写盘，绝不把零值整体写回去覆盖用户的文件。`

**改法**：加入冻结的三项展示字段及持久化标志；新增 `loadTasksDetailWidth`、`rememberTasksDetailWidth` 的最小专用入口，按 E2 接入现有 writer 的缓存、路径切换、dirty/generation、成功清脏、失败保留；字段挂在 uiStateFile 而非 UIState。读取一次，禁止每帧读盘。新状态跟既有 kanbanStates 一起释放，不开第二张全局窗口表。

**验收**：关闭再开恢复手动宽度；改变窗宽不会污染偏好；任务窗口不开聊天持久化；两窗口交错保存不覆盖对方字段；损坏文件不覆写；新临时目录不复用旧缓存。

**补测**：缺失/0/负值/超范围/非有限值处理；旧 JSON 向后兼容；tasks 宽和主 sidebar/last_model/project_order 交错写；写入中再次更新 width 的 generation；写失败后重试；默认 headless 零写盘。

### 批次 3：专用外壳、侧栏与组装（唯一共享接线者）

**文件清单**：`internal/ui/tasks_window.go`、`internal/ui/root.go`、`internal/ui/windows.go`、`internal/ui/model.go`、`internal/ui/kanban_modal.go`、`internal/ui/kanban_data.go`、`internal/ui/kanban_shell_test.go`、`internal/ui/kanban_modal_test.go`、`internal/ui/kanban_data_test.go`；新增 `internal/ui/tasks_window_root.go`、`internal/ui/tasks_window_sidebar.go`、`internal/ui/tasks_window_root_test.go`。

**锚点**：

internal/ui/tasks_window.go:79 `func OpenTasksWindow(client Client, view string, opts ...Option) *mygo.Window {`

internal/ui/tasks_window.go:104 `state, content := buildRoot(client, allOpts...)`

internal/ui/root.go:44 `func buildRoot(client Client, opts ...Option) (*chatState, func(c *ui.Context)) {`

internal/ui/model.go:650 `func (s *chatState) init(c *ui.Context) {`

internal/ui/model.go:960 `func (s *chatState) loadProjects() {`

internal/ui/kanban_modal.go:55 `func renderKanbanPanel(c *ui.Context, s *chatState, windowMode bool) {`

internal/ui/kanban_modal.go:252 `func renderKanbanModalHeader(c *ui.Context, s *chatState, ks *kanbanState, p kanbanModalBodyProps, width float32) {`

internal/ui/kanban_modal.go:682 `func renderKanbanModalBody(c *ui.Context, s *chatState, ks *kanbanState, p kanbanModalBodyProps) {`

internal/ui/kanban_data.go:25 `func (s *chatState) setKanbanScope(projectID string, all bool) {`

internal/ui/windows.go:440 `func (s *chatState) close() {`

**改法**：

1. 新增 `buildTasksRoot`：创建同类业务状态、应用 opts，绑定 Services/theme/workspace updates/toast，渲染专用 shell、目的内容与新建 sheet。不得调用 buildRoot、reportForegroundThread 或聊天 init。`OpenTasksWindow` 改调用该入口；保留单例/restore/show/focus/StateKey/OnClosed 清理，补同设置的 TitleBarHeight helper。
2. 保留 `WithTasksWindowMode` 兼容测试/嵌入调用：`buildRoot` 应在聊天恢复与 init **之前**识别该 Option，并委托给同一任务根构建函数；不能保留两份任务 UI 实现。生产 OpenTasksWindow 不再经过 buildRoot。
3. 在 `model.go` 的加载入口增加任务模式的窄分支或抽出最小项目加载 helper，直接取 Projects 并按 workspace update 机制应用；不加载每个项目 Threads、不启动 queue/bees/summary watch、不自动选聊天、不自动 onboarding。新建任务/目标要用到的 providers/config 由原表单按需加载，不能只删启动加载而让新建 Agent 选择空白；补表单打开集成用例。不重构其他聊天路径。
4. 侧栏按 C3 实现。设置只作视觉参照，不编辑 settings.go。固定两个目的 switch；定义 C4 接口，调用自动化方提供的函数，不编辑 automation*.go。移除原 window titlebar 空白行、顶部目的分段、项目/目标 picker；顶栏按实际右侧宽布局。移除独立窗口多余「关闭任务」按钮，用原生交通灯关闭。
5. 任务根初始化时一次确定范围，后续只读 ks.scopeID；空值为全部。`setKanbanScope` 统一取消过时详情、复位不合法 goal/selection 与启动正确 watch，保留 destination、viewMode、两份搜索。保留 `syncKanbanProject` 对非任务窗口旧 fallback 的行为，不让专用根每帧被聊天当前项目覆盖。
6. 快照同时供侧栏、计数、board/list 与 keyboard 使用。attention 任务/目标计划入口统一从当前 scope 的原始快照派生；搜索/goal/viewFilter 组合只有一处；把完成折叠后的可见序列传给 keyboard。调用批次 4 的 viewport 列宽计算和批次 5 的 pane/sheet 判定；右侧内容栏宽按真实布局传参，不用 c.Size 冒充局部宽。自动化时不挂任务详情，即使 task ID 暂存。
7. 在专用根处理原有命令/键盘入口与 Esc：popover/菜单、表单、详情、多选、搜索按活动层消费，空闲 Esc 不关任务窗。不要把聊天快捷键 switch 整段复制进来。`close` 保留订阅/请求取消，删除新增状态时遵循现有锁顺序；特别不能持 kanbanMu 再调用会获取它的 closeTaskDetail。

**验收**：840/1140/1440 三档有固定侧栏与单条内容顶栏，长项目不挤控件；原生单例及会话跳主窗有效；打开任务窗不出现聊天 chrome/订阅、前台会话上报；任务/自动化共用项目且空值不跳首项目；切目的保留两份搜索与滚动；新建任务/目标的 Agent 选择和提交仍可用。

**补测与旧测迁移**：

- `TestTasksWindowEntrancesPreserveView` 保持语义不变；`TestTasksWindowEscapeKeepsWindow` 继续通过，使用 `Root(...WithTasksWindowMode...)` 证明兼容委托有效。
- `TestKanbanShellDestinationsAndSearch` 不再只渲染 Header，改渲染专用 root/shell；仍点击旧 `kanban-destination-tasks/automations` Label，位置变为侧栏。`TestKanbanShellAllProjects` 改点侧栏行；`NoGoals` 改断言整个目标区不存在；GoalFilterAndDetail 改从目标行进入，不再找「目标筛选」下拉。
- `TestKanbanHeaderButtonsStayWithinWindow` 取消旧“两条 52”断言，改断言单条顶栏、sidebar 220、工具条在右区内；自动化目的不出现任务视图切换。不要删除边界检查，只改变合法结构。
- 新 fake 计数确认任务 root 不调用 Threads/WatchQueue/WatchBees/前台会话上报；Projects 成功/失败、全部项目、项目删去、过时请求回流、独立 toast；scope 切换任务与自动化均正确。
- 旧 `renderKanbanWorkspace` 弹窗 fallback 保留 `ui.DialogBase`；它仍是无原生 opener 时的兼容路径，不允许用于证明生产专用根正确。保留其独立测试；不修改并行方案持有的 `automation_test.go` 里的 newWorkspaceTester。

### 批次 4：卡片、列、焦点与投放反馈

**文件清单**：`internal/ui/kanban_cards.go`、`internal/ui/kanban_keyboard.go`、`internal/ui/kanban_cards_test.go`、`internal/ui/kanban_keyboard_test.go`、`internal/ui/kanban_style_test.go`、`internal/ui/kanban_test.go`；新增 `internal/ui/kanban_visual_test.go`。

**锚点**：

internal/ui/kanban_cards.go:361 `func renderTaskCard(c *ui.Context, s *chatState, ks *kanbanState, task KanbanTask, st kanbanTaskCardState) {`

internal/ui/kanban_cards.go:545 `if task.Blocked && len(task.DependsOn) > 0 {`

internal/ui/kanban_cards.go:793 `func kanbanBoardColumnWidth(c *ui.Context, s *chatState) float32 {`

internal/ui/kanban_cards.go:837 `func renderKanbanColumn(`

internal/ui/kanban_keyboard.go:23 `func kanbanKeyboardUpdate(s *chatState, tasks []KanbanTask, view string) {`

internal/ui/kanban_cards_test.go:89 `func TestKanbanCardKeyboardActions(t *testing.T) {`

**改法**：按 D1–D3 实施轻列与实心卡、裸计数、稳定动作槽、Popover 菜单、空列留白和完成轨。mixed 项目上下文改读 scopeID；动作策略/CanStart/CanMove/Review 语义不变。新增实际 board viewport 的纯列宽计算；保留旧 `kanbanBoardColumnWidth` 签名的兼容薄层直到批次 3 接线，不能临时让整个包不可编译。键盘遍历忽略折叠 done 的隐藏卡，轨可展开；所有焦点/选中样式在相同几何 inset 内。`kanban_test.go` 的依赖卡内按钮断言改为打开详情后检查依赖，不删除依赖导航覆盖。

**验收**：hover/focus/展开更多均不改变卡高、标题换行与列宽；卡片只有标题加一行信号上下文；空列仍可接收合法投放；review 拖完成仍只打开审阅；低噪声不等于删除恢复操作；Tab 能到原有主动作。

**补测**：280/400 宽卡长标题、有无信号、全部项目上下文、主动作出现前后 bounds 相同；更多 Popover 不增卡高且 Esc 关闭；常驻 card Label 不变；打开/多选/焦点可独立表达；列宽随 board viewport 而非整窗变化；空列 drop、非法 needs_you drop、完成轨 review drop；ReduceMotion 下 hover、完成折叠不遗留帧。调整旧 mixed 测试的 ks.projectID 夹具为 scopeID，保留 signal/priority 的既有断言。

### 批次 5：任务与目标详情容器、手柄和动效

**文件清单**：`internal/ui/kanban_detail.go`、`internal/ui/kanban_goals.go`、`internal/ui/kanban_detail_test.go`、`internal/ui/kanban_goals_test.go`；新增 `internal/ui/kanban_detail_pane.go`、`internal/ui/kanban_detail_pane_test.go`。

**锚点**：

internal/ui/kanban_detail.go:65 `func renderTaskDetail(c *ui.Context, s *chatState, ks *kanbanState) {`

internal/ui/kanban_detail.go:82 `func renderTaskDetailContent(c *ui.Context, s *chatState, ks *kanbanState) {`

internal/ui/kanban_detail.go:103 `ui.Row(c.Key("kanban-detail-header")).FillWidth().AlignItems(ui.Center).Gap(Space3).Children(func() {`

internal/ui/kanban_goals.go:33 `func renderGoalDetailContent(c *ui.Context, s *chatState, ks *kanbanState) {`

internal/ui/side_pane.go:406 `dx, _, held := handle.Dragged()`

internal/ui/motion_presets.go:88 `func motionSidePanel(c *ui.Context, fromLeft bool) ui.ElementTransition {`

**改法**：新文件只含任务局部分栏容器、几何函数与轻量手柄；不注册进聊天 sidePanes。头/body/footer 分层；任务返回与关闭分开，目标详情同一头部风格；删内容根重复滑动；sheet 只保留 motionDialog。使用批次 2 宽度记忆入口与显式 opt-in。详情布局不拥有项目选择，不重新拉业务数据。对外提供上述两个冻结 helper，由批次 3 替换旧 480 固定栏。测试独立使用这些 helper，因此可先交付，不必编辑 kanban_modal.go。

**验收**：默认 480、360/640 边界和 board 400 下限正确；拖完关窗再开恢复；窗口自动 clamp 后拉宽能恢复偏好；目标返回保持 goal、scroll 与任务焦点；宽窄切换只保留一个详情实例；普通动效只有一次滑入，ReduceMotion 一帧到终点。

**补测**：R=619/766/767/919/1219 的布局纯函数；手柄 drag/keyboard/焦点、边界与无自动关闭；头/body/footer bounds、关闭和返回按钮各自可达；普通与减少动效下进入/退出；目标→任务→目标恢复；既有慢响应丢弃、接受防重、合并失败、反馈保留测试不改语义。

### 批次 6：跨模块验收，不再改生产文件

**文件清单**：新增 `internal/ui/tasks_window_acceptance_test.go`。如需快照，新增专属 fixture，不改设置树或既有 automation fixture。

**锚点**：

internal/ui/kanban_shell_test.go:12 `func TestKanbanShellDestinationsAndSearch(t *testing.T) {`

internal/ui/kanban_shell_test.go:144 `func TestTasksWindowEscapeKeepsWindow(t *testing.T) {`

internal/ui/modal_style_guard_test.go:13 `func TestNoStyledModalOrPopover(t *testing.T) {`

**改法**：以最终专用 root 做黑盒用例，不能只测旧 Header helper；组合有两项目、任务审批/失败/review、目标 plan_review、自动化结果的固定 fake。用可见文字或唯一 Label，不用 c.Key 调 tt.Find。自动化内容仅验证 scope/插槽与不重复 chrome，不断言并行方案尚未冻结的内部文案。

**验收**：840×520、1140×720、1440×900，浅/深色、ReduceMotion 两态，长标题/长项目、空/有任务、搜索无结果、项目错误均有边界断言；侧栏与详情一眼可分；无覆盖顶栏或点击穿透；原生单例与尺寸记忆另由隔离 dev GUI 验收，headless 不冒充原生验证。

**补测**：完整目的切换 + 项目切换 + 搜索恢复 + 列表滚动 + 详情 tab/宽窄切换；独立窗关闭后订阅/详情请求与包级状态释放；新建弹层/更多浮层无双边框；整窗 Esc 不意外关闭、表单编辑快捷键不触发任务移动。

### F1. 有范围的实现验证命令（本次未执行）

在有缓存依赖的工作副本运行，禁止为了检查而联网安装；每条命令由执行工具再加 130 秒外部超时，Go 自身也限 120 秒：

```sh
GOPROXY=off GOSUMDB=off CGO_ENABLED=0 go test ./internal/ui/ -run '^(TestTasks|TestKanbanShell|TestKanbanHeader|TestKanbanCard|TestKanbanTaskSignals|TestKanbanWaiting|TestKanbanList|TestKanbanKeyboard|TestKanbanDetail|TestKanbanGoal|TestKanbanVisual|TestDesignTokenRatchet|TestNoStyledModalOrPopover)' -timeout 120s
GOPROXY=off GOSUMDB=off CGO_ENABLED=0 go test ./internal/ui/ -run '^(TestUIState|TestSaveUIState|TestLastModel|TestTasksWindowPersistence)' -timeout 120s
```

实际新增测试名以各批次命名为准；第二条如未覆盖既有 writer 的真实测试名，实施者先定位再补精确 regex，不声称只跑了空集也算通过。全量检查由主代理执行。

原生 GUI 检查只在隔离 dev 数据目录进行；本轮设计不启动应用，不操作用户正在运行的窗口。实现者至少提供 840/1440 两张窗口与详情截图，分别验收浅深色及 ReduceMotion。后台 dev 任务完成即停止。

## G. 方案取舍与未确认项

| 选项 | 取舍 | 结论 |
|---|---|---|
| 继续 buildRoot + 加左栏 | 改动最少，但聊天 init/前台上报/持久化与任务根仍耦合 | 不推荐，不满足专用根约束 |
| 专用任务根 + 复用 chatState 业务方法 | UI 完全独立；一次切入口；不重写 Client/DTO/服务层 | **推荐** |
| 新建 TasksState 并迁移所有业务逻辑 | 类型更纯，但 kanban/automation/表单/测试需要大面积换签名 | 本轮不做 |
| 宽屏也用居中模态详情 | 易辨识，但遮掉看板，与既定 D6 冲突 | 不采用 |
| 引第三方 board/sidebar/splitter | 与当前 Go UI 和禁止新依赖约束不合；会重复主题和 overlay | 不采用 |
| 从聊天 sidePanes 直接注册任务详情 | 可复用持久化，但引入聊天菜单、顶栏 inset、自动收起语义与全量 UIState 写回 | 不采用；只复用几何/事件模式 |

未确认：MyGo 当前原生 drag preview 可定制程度、真实窗口 TitleBarHeight 在各平台的显示、主题与字号放大后的像素层次。源码与 headless 不能代替 GUI 结果。实现时按最小方案：不自绘第二份 ghost，不添加新主题色；原生窗口与视觉最终由隔离 dev 验收。其余需求不留待用户决定，按本文推荐执行。
