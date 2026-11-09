# internal/ui 公共组件收敛方案

日期：2026-11-09。状态：设计完成，尚未实施。本文只依据当前工作副本做静态阅读；本次没有运行构建、测试、lint、类型检查或 GUI 验证。

## 目标与边界

保持 package mcodeui，把重复的绘制、命中、反馈和布局规则放到 `internal/ui/controls_<名称>.go`。业务状态、锁、网络请求、持久化、菜单权限和未保存确认仍由调用方持有。已有 Button、IconButton、SearchField、Badge、Capsule、Segmented 等继续使用。

不建立新的组件包、依赖注入层、表单 schema、全局复制状态或通用拖拽引擎。不改协议、配置格式、go.mod、go.sum、theme.go、tokens.go 或动效系统。尺寸直接复用当前 token；确有业务尺寸时沿用当前已有命名常量。所有新增动效只用 `animateDuration(c, DurSnap)`、`animateDuration(c, DurFast)` 或 `animateDuration(c, DurBase)`，遵循 ReduceMotion；不新增循环动画。

弹窗外层只用 ui.DialogBase 或 modalDragModal；浮层只用 ui.PopoverBase 或现有 appPopover。外层只处理定位、尺寸上限和事件，圆角、背景、边框、阴影放在内层卡片。禁止 ui.Modal / ui.Popover。

### 外部组件调查

已查阅 lobe-mygo 的 skills.md 与 CopyButton 参考页，只借鉴「复制后反馈」和 Context + Props + Element 的 API 形状，不引入依赖，也不照搬其固定两秒反馈。

已按 AGENTS.md 查阅 CrychicUI README 和以下源码，参考版本为 commit `bf15ddf6600a9e760584ef3991ec4bdc7cee347c`：button/copy_button.go、display/avatar.go、feedback/empty_state.go、feedback/progress_bar.go、navigation/closable_tabs.go、form/field_box.go。这些现成组件不能直接满足本轮的保真要求：复制按钮自带音效且缺少禁用和现有反馈状态接入；空态固定插图盘和入场；进度条固定高度及动画；可关闭标签直接修改名称切片并自动新增；头像使用库内主题尺寸。直接引入后仍需改写交互和主题，收益低于收敛已有代码。因此本方案无新增依赖、无引入包路径或发布版本；不启用 sound，也不用 overlay。以后若改变这个决定，必须重新评估 ReduceMotion、统一关闭 sound，并在变更说明记录确切包路径与版本。

参考链接：

- https://mygo.lobehub.com/skills.md
- https://mygo.lobehub.com/skills/components/copy-button.md
- https://github.com/ELY-Open-Source/CrychicUI/tree/bf15ddf6600a9e760584ef3991ec4bdc7cee347c

## 已核实的重复与保留边界

以下锚点均为设计时的单行源码；实施时用函数名定位，避免前一轮迁移后行号偏移。

### 复制：八段实现

正文与代码块、计划、汇报、远程、AGY、错误、禁用复制均有独立绘制。代码块还带生成中禁用及 toast；远程和 AGY 有独立反馈时间与重绘机制。这些业务行为必须保留。

- internal/ui/timeline_rows.go:2800 `func renderCopyMessageButton(c *ui.Context, s *chatState, m Message, key string) {`
- internal/ui/timeline_rows.go:2965 `btn := ui.ButtonBase(c.Key(copyKey)).`
- internal/ui/approval.go:877 `func renderPlanCopyButton(c *ui.Context, key, planText string) {`
- internal/ui/swarm.go:2772 `func renderSwarmReportCopyButton(c *ui.Context, s *chatState, key, text string) {`
- internal/ui/settings_remote_tailscale.go:263 `func renderRemoteCopyButton(c *ui.Context, st *SettingsState, key, label, copiedLabel, tooltip, value, copiedKey string, copiedAt time.Time) {`
- internal/ui/settings_agy.go:1218 `func renderAgyCopyButton(c *ui.Context, st *SettingsState, key, text, label string) {`
- internal/ui/timeline_error_row.go:199 `func renderErrorCopyButton(c *ui.Context, s *chatState, m Message) {`
- internal/ui/settings_remote.go:1538 `func disabledCopyButton(c *ui.Context, key, label string) {`

其中禁用入口本身不能写剪贴板。计划当前没有复制反馈，本轮不为它引入新全局计时器；允许保持无反馈，仅统一外观。共享组件只在有效点击时调用 Value，再写剪贴板并执行 OnCopy。

### 头像：七个入口，不是七个等价头像

共享圆形首字、图标、底色和描边；业务包装仍保留。叠放布局、状态角标和点击导航不塞进 Avatar。

- internal/ui/swarm.go:2608 `func renderSwarmAvatarStack(c *ui.Context, keyPrefix string, entries []swarmReportEntry) {`
- internal/ui/swarm.go:2751 `func renderSwarmReportAvatar(c *ui.Context, key, name string, size, fontSize float32) {`
- internal/ui/swarm_mentions.go:460 `func renderSwarmPersonaIcon(c *ui.Context, key, name string, size float32) {`
- internal/ui/sidebar_steward.go:286 `func renderStewardSectionAvatar(c *ui.Context, key, avatar string) {`
- internal/ui/sidebar_steward.go:325 `func renderStewardSectionAvatarBusy(c *ui.Context, key, avatar string, color ui.Color) {`
- internal/ui/sidebar_bees.go:662 `func renderSidebarSingleBeeAvatar(c *ui.Context, s *chatState, parentID string, bee Thread, status string, isSelected bool) {`
- internal/ui/effort_slider.go:367 `func renderProviderRainbowAvatar(c *ui.Context, p EffortSliderCardProps) {`

彩虹入口是编排开关，含 Checked、Value、圆锥渐变和专属深色背景，保留原实现。mention 已经是简单的图标/色点回退，保留这个业务包装，不强制换成首字头像。总管忙碌角标的绘制归动效 bee 处理，本轮只替换其内部静态头像。

### 空态与加载：覆盖六个以上实际位置，保留品牌欢迎页

Diff 的错误/非 Git/空态已经共享 renderDiffStatusState，适合作为标准空态基准；加载另有 spinner 行。技能是紧凑文本；编排、文件目录是轻量空态；记忆是带图标的居中状态。公共空态只画内容，居中、Grow、Margin、Padding 留给页面，避免为三种容器再造 layout 参数。

- internal/ui/diff_panel.go:447 `func renderDiffStatusState(c *ui.Context, key string, icon *ui.SVG, title, subtitle string, onRetry func()) {`
- internal/ui/settings_page_skills.go:497 `func renderSkillsEmpty(c *ui.Context, isFiltered bool) {`
- internal/ui/swarm_bee_floats.go:947 `ui.Column(c.Key("swarm-empty-body")).Grow(1).FillWidth().Justify(ui.Center).AlignItems(ui.Center).Children(func() {`
- internal/ui/memory_panel_views.go:373 `ui.Text(c, "暂无待审记忆").`
- internal/ui/memory_panel_views.go:1048 `ui.Text(c, memorySearchEmptyTitle(snap)).`
- internal/ui/file_manager.go:411 `ui.Text(c, "空目录").`
- internal/ui/file_manager.go:657 `ui.Text(c, "没有匹配的文件").`
- internal/ui/sidebar_activity.go:160 `func renderSidebarNoResultsCard(c *ui.Context) {`
- internal/ui/chat.go:769 `func renderStewardEmptyPrompt(c *ui.Context, s *chatState) {`

总管欢迎页有位图、品牌图标回退和起步体验，保留独立实现。既有描述原文可保留，但不新增帮助句和次级标签。不要为了提取 EmptyState 顺便改写欢迎语。

文件目录加载与搜索加载确有同一套 spinner + 文案绘制，可共享 LoadingIndicator；记忆加载不带文案，也可复用其空字符串形式。

- internal/ui/file_manager.go:400 `ui.Row(c.Key("file-manager-loading")).`
- internal/ui/file_manager.go:644 `ui.Row(c.Key("file-manager-search-loading")).`
- internal/ui/memory_panel_views.go:369 `ui.Spinner(c).Size(IconMD, IconMD).TextColor(ThemeAccent(c))`

### Tabs：真正重复的是标签按钮，不统一所有导航布局

- internal/ui/memory_panel_views.go:280 `func renderMemoryTabButton(c *ui.Context, s *chatState, tab int, label string, active bool, count int) {`
- internal/ui/file_manager_tabs_view.go:203 `func renderFileManagerTabItem(c *ui.Context, s *chatState, snap fileManagerPanelSnapshot, tab fileManagerTabSnapshot) {`
- internal/ui/terminal.go:1340 `tabBtn := ui.ButtonBase(c.Key(fmt.Sprintf("terminal-tab-%d", tb.id))).`
- internal/ui/model_picker.go:1292 `func renderModelPickerSideTabs(c *ui.Context, p ModelPickerProps, state *ModelPickerState) {`
- internal/ui/composer.go:2364 `renderModelPickerSideTabs(c, ModelPickerProps{`
- internal/ui/composer_changes_tab.go:136 `func renderThreadChangesTab(c *ui.Context, s *chatState, add, del, files int, capsules []composerCapsule) {`

Composer 侧切换已复用模型选择器，无需再迁移一次。模型选择器是带滑块、深色底和「点执行侧先开启编排」行为的分段控件，继续保留。改动统计是打开 Diff 的动作胶囊，没有可切换内容页，保留。公共 TabButton 只统一记忆/文件/终端的按钮底色、圆角、悬停和标签语义；水平滚动、计数、双击固定、右键菜单、关闭动作由页面管理。

### 拖拽：两套 native Drop 可共享，另两套是不同交互

- internal/ui/sidebar.go:684 `headerRow.Drag(pVal.ID)`
- internal/ui/composer.go:229 `if draggedID, dropped := ui.Drop[string](queueList); dropped {`
- internal/ui/composer.go:252 `draggedID, over := ui.DragOver[string](row)`
- internal/ui/composer_drop_overlay.go:195 `Drag(item.ID)`
- internal/ui/settings_page_models.go:750 `if _, dy, held := handle.Dragged(); held {`
- internal/ui/settings_page_models.go:775 `target := modelDragTargetIndex(idx, ps.dragDy, 59, len(visiblePins))`
- internal/ui/swarm_bee_floats.go:1093 `func trackSwarmPanelDrag(s *chatState, f swarmFloatFrame, id panelID, key string, rest panelPoint, handle ui.Element) {`

侧栏和排队都用 native Drag/Drop，抽 scoped payload 和 Drop 过滤；不强制侧栏也用手柄。模型按累计位移算索引，浮窗按二维位置吸附角落和槽位，不替换成 SortableList。保留原来的模型持久化、过滤映射、失败回报与浮窗锁/停靠逻辑。

### 弹层和菜单：收敛底层，不把系统菜单改成自绘菜单

- internal/ui/controls_popover.go:39 `func PopoverCard(c *ui.Context, p PopoverProps, content func()) ui.Element {`
- internal/ui/popover.go:55 `func appPopover(c *ui.Context, anchor ui.Element, open *bool, onDismiss func(), fn func()) ui.Element {`
- internal/ui/popover.go:75 `func appPopoverAbove(c *ui.Context, anchor ui.Element, open *bool, onDismiss func(), fn func()) ui.Element {`
- internal/ui/popover.go:97 `func anchoredMenu(c *ui.Context, key string, trigger ui.Element, open *bool, onDismiss func(), minWidth, maxWidth, maxHeight float32, children func()) {`

这四个入口的层次不同：PopoverCard 提供卡片；其他入口只提供锚定与生命周期。保留已有调用签名作为薄适配器，所有适配器委托一个 anchoredPopover，消除重复的 Base 构建、AttachTo、Margin、入场和 Dismissed 检查。现有几十处业务调用无需机械改名。卡片外观也只保留一处，popoverCard 委托它。需要继续保留 styled 与 unstyled 两层，避免双重卡片。

静态检索到六个具名 `func(*ui.Menu)` 工厂，brief 的七个未得到证实；另有内联 ContextMenu 回调和 localFileLinkItems 项目填充函数。它们包含不同权限和业务动作，保留 MyGo Menu，不创建通用菜单项模型。

- internal/ui/file_manager_menu.go:90 `func fileManagerEntryMenu(c *ui.Context, s *chatState, threadID string, entry FileEntry) func(*ui.Menu) {`
- internal/ui/file_manager_menu.go:157 `func fileManagerTabMenu(c *ui.Context, s *chatState, snap fileManagerPanelSnapshot, tab fileManagerTabSnapshot) func(*ui.Menu) {`
- internal/ui/file_manager_menu.go:204 `func fileManagerDirMenu(c *ui.Context, s *chatState) func(*ui.Menu) {`
- internal/ui/sidebar.go:1380 `func threadContextMenu(c *ui.Context, s *chatState, projectID string, thVal Thread) func(*ui.Menu) {`
- internal/ui/timeline_menu.go:15 `func messageContextMenu(c *ui.Context, s *chatState, m Message) func(*ui.Menu) {`
- internal/ui/turn_files_card.go:333 `func turnFileContextMenu(c *ui.Context, s *chatState, threadID, workDir string, f turnEditedFile) func(*ui.Menu) {`
- internal/ui/local_file_link.go:132 `func localFileLinkItems(s *chatState, menu *ui.Menu, path string) {`

### 进度条和骨架：只抽真正同构的绘制

- internal/ui/timeline_rows.go:3890 `func renderTodoProgressBar(c *ui.Context, m Message, items []todoItem) {`
- internal/ui/composer_attach.go:1048 `func renderAttachmentUploadBar(c *ui.Context, a composerAttachment) {`
- internal/ui/quota_bar.go:13 `func renderQuotaBarSegments(c *ui.Context, keyPrefix string, segs []quotaBarSegment, fill ui.Color, markerLabel string) {`
- internal/ui/timeline_skeleton.go:104 `func renderTimelineSkeleton(c *ui.Context) {`
- internal/ui/controls_surface.go:160 `func SkeletonLine(c *ui.Context, key any, width, height float32) ui.Element {`
- internal/ui/controls_surface.go:165 `func SkeletonBlock(c *ui.Context, key any, height float32) ui.Element {`

待办与附件的已知比例轨道共用 ProgressBar。附件进度为零意味着没有 RPC 中间进度，继续显示 spinner 和静态轨道，不能误画成任务 0%。额度条已有共用分段函数，含时间标记、虚影、固定 marker 宽度，保留。删除零调用 SkeletonLine/Block 及其私有实现，保留真实 timelineSkeleton 的尺寸、对齐和状态机；不重建一个零调用的 Skeleton API。已有循环呼吸的收敛属于动效 bee，本轮不改 timeline_skeleton.go。

### 补充发现：列表行、分组标题、设置行、计数、关闭与搜索

- internal/ui/file_manager.go:721 `func renderFileManagerRow(c *ui.Context, s *chatState, props fileManagerRowProps) {`
- internal/ui/file_manager.go:675 `renderFileManagerRow(c, s, fileManagerRowProps{`
- internal/ui/settings_tree.go:501 `func renderSettingRow(c *ui.Context, key, title string, control func()) {`
- internal/ui/settings_tree.go:529 `func renderStackedSettingRow(c *ui.Context, key, title string, control func()) {`
- internal/ui/settings_tree.go:392 `func renderSettingsSection(c *ui.Context, key string, title string, content func()) {`
- internal/ui/settings_page_skills.go:269 `ui.Text(c, title).`
- internal/ui/memory_panel_views.go:302 `ui.Text(c, strconv.Itoa(count)).`
- internal/ui/file_manager_tabs_view.go:159 `closeBtn := ui.ButtonBase(c.Key("file-manager-tabbar-close-btn")).`
- internal/ui/file_manager.go:553 `in := ui.TextInput(c.Key("file-manager-search-input"), query).`
- internal/ui/memory_panel_views.go:984 `ui.TextInput(c.Key("memory-search-input"), &query).`
- internal/ui/settings_sheet_skills.go:10 `field := SearchField(c, TextFieldProps{`

文件目录与搜索结果已经共用一条业务行；不要再抽一个带二十个字段的 ListRow。设置行移动到 controls_setting.go，保留 focus/jump 包装在 settings_tree.go；技能行只复用布局原语，错误与启用写盘仍在技能页面。分组标题统一 SectionHeading，不把侧栏活动标题的不同排版强拉进来。计数直接复用 Badge，不加 CountBadge；零数量由调用方不渲染。关闭按钮直接用现有 IconButton，不造 CloseButton。普通搜索直接用现有 SearchField，保留文件搜索防抖、记忆搜索的显式提交和既有 Label。终端搜索在实际输入元素上注册 Escape Shortcut，当前 SearchField 返回外部行，不能等价承接该事件，因此这处输入保留，终端本轮只迁标签。

## 公共组件表

本表列出本轮新增、移动或直接迁移使用的公共入口；未列出的现有公共 API 保持原位置与签名。类型的精确字段见后文，不能在实施中增加假想扩展项。新文件均位于 internal/ui。

| 名称 | 目标文件 | Go 函数签名 | 替换锚点（每项后为原源码） |
| --- | --- | --- | --- |
| CopyButton | controls_copy.go（新） | `func CopyButton(c *ui.Context, p CopyButtonProps) ui.Element` | internal/ui/timeline_rows.go:2800 `func renderCopyMessageButton(c *ui.Context, s *chatState, m Message, key string) {`<br>internal/ui/timeline_rows.go:2965 `btn := ui.ButtonBase(c.Key(copyKey)).`<br>internal/ui/approval.go:877 `func renderPlanCopyButton(c *ui.Context, key, planText string) {`<br>internal/ui/swarm.go:2772 `func renderSwarmReportCopyButton(c *ui.Context, s *chatState, key, text string) {`<br>internal/ui/settings_remote_tailscale.go:263 `func renderRemoteCopyButton(c *ui.Context, st *SettingsState, key, label, copiedLabel, tooltip, value, copiedKey string, copiedAt time.Time) {`<br>internal/ui/settings_agy.go:1218 `func renderAgyCopyButton(c *ui.Context, st *SettingsState, key, text, label string) {`<br>internal/ui/timeline_error_row.go:199 `func renderErrorCopyButton(c *ui.Context, s *chatState, m Message) {`<br>internal/ui/settings_remote.go:1538 `func disabledCopyButton(c *ui.Context, key, label string) {` |
| Avatar | controls_avatar.go（新） | `func Avatar(c *ui.Context, p AvatarProps) ui.Element` | internal/ui/sidebar_steward.go:286 `func renderStewardSectionAvatar(c *ui.Context, key, avatar string) {`<br>internal/ui/sidebar_bees.go:698 `ui.Box(c.Key(btnKey+"-glyph")).`<br>internal/ui/swarm.go:2751 `func renderSwarmReportAvatar(c *ui.Context, key, name string, size, fontSize float32) {`<br>internal/ui/swarm.go:2637 `ui.Row(c.Key(avatarKey)).` |
| EmptyState | controls_empty.go（新） | `func EmptyState(c *ui.Context, p EmptyStateProps) ui.Element` | internal/ui/diff_panel.go:447 `func renderDiffStatusState(c *ui.Context, key string, icon *ui.SVG, title, subtitle string, onRetry func()) {`<br>internal/ui/sidebar_activity.go:160 `func renderSidebarNoResultsCard(c *ui.Context) {`<br>internal/ui/settings_page_skills.go:497 `func renderSkillsEmpty(c *ui.Context, isFiltered bool) {`<br>internal/ui/memory_panel_views.go:373 `ui.Text(c, "暂无待审记忆").`<br>internal/ui/memory_panel_views.go:1048 `ui.Text(c, memorySearchEmptyTitle(snap)).`<br>internal/ui/swarm_bee_floats.go:948 `ui.Text(c, "暂无子代理").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))`<br>internal/ui/file_manager.go:411 `ui.Text(c, "空目录").`<br>internal/ui/file_manager.go:657 `ui.Text(c, "没有匹配的文件").` |
| LoadingIndicator | controls_loading.go（新） | `func LoadingIndicator(c *ui.Context, key any, text string, size float32) ui.Element` | internal/ui/file_manager.go:400 `ui.Row(c.Key("file-manager-loading")).`<br>internal/ui/file_manager.go:644 `ui.Row(c.Key("file-manager-search-loading")).`<br>internal/ui/memory_panel_views.go:369 `ui.Spinner(c).Size(IconMD, IconMD).TextColor(ThemeAccent(c))`<br>internal/ui/diff_panel.go:346 `ui.Row(c.Key("diff-loading-row")).` |
| ProgressBar | controls_progress.go（新） | `func ProgressBar(c *ui.Context, p ProgressBarProps) ui.Element` | internal/ui/timeline_rows.go:3890 `func renderTodoProgressBar(c *ui.Context, m Message, items []todoItem) {`<br>internal/ui/composer_attach.go:1079 `track := ui.Row(c.Key("composer-attach-progress-track-" + a.LocalID)).` |
| TabButton | controls_tabs.go（新） | `func TabButton(c *ui.Context, p TabButtonProps, body func()) ui.Element` | internal/ui/file_manager_tabs_view.go:213 `btn := ui.ButtonBase(c.Key("file-manager-tab-"+tab.path)).`<br>internal/ui/memory_panel_views.go:289 `contextBaseButton(c, "memory-tab-"+strconv.Itoa(tab), label).`<br>internal/ui/terminal.go:1340 `tabBtn := ui.ButtonBase(c.Key(fmt.Sprintf("terminal-tab-%d", tb.id))).` |
| anchoredPopover（私有共享底层） | controls_popover.go（扩展） | `func anchoredPopover(c *ui.Context, p anchoredPopoverProps, content func(ui.Element)) ui.Element` | internal/ui/popover.go:59 `panel := ui.PopoverBase(c, anchor, open, func(panel ui.Element) {`<br>internal/ui/popover.go:79 `panel := ui.PopoverBase(c, anchor, open, func(panel ui.Element) {`<br>internal/ui/popover.go:101 `panel := ui.PopoverBase(c.Key(key), trigger, open, func(panel ui.Element) {`<br>internal/ui/controls_popover.go:47 `panel := ui.PopoverBase(ctx, p.Anchor, p.Open, func(panel ui.Element) {` |
| PopoverCard（保留签名） | controls_popover.go | `func PopoverCard(c *ui.Context, p PopoverProps, content func()) ui.Element` | internal/ui/controls_popover.go:68 `card := ui.Column(c).Radius(RadiusLG).Padding(Space2).Background(ThemeSurfacePopover(c)).Border(Space1/4, ThemeHairline(c))`<br>internal/ui/popover.go:119 `func popoverCard(c *ui.Context, column ui.Element) ui.Element {`（抽共用私有卡片样式，调用者已有 padding 不再叠加） |
| Sheet | controls_sheet.go（新） | `func Sheet(c *ui.Context, p SheetProps, body func()) ui.Element` | internal/ui/settings_tree.go:276 `ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {`<br>internal/ui/kanban_sheets.go:274 `ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {`<br>internal/ui/kanban_sheets.go:438 `ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {`<br>internal/ui/scripts_sheet.go:261 `ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {`<br>internal/ui/context_panel_views.go:40 `ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {`<br>internal/ui/memory_panel_views.go:163 `ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {`<br>internal/ui/file_manager_ops.go:306 `modalDragPanel(c, modalDragFileManagerOp, ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {` |
| FormActions | controls_form.go（新） | `func FormActions(c *ui.Context, key any, body func()) ui.Element` | internal/ui/git_form_windows_ui.go:944 `func renderGitFormActions(c *ui.Context, busy bool, primaryLabel string, primaryDisabled bool, primaryKey string, primary, onCancel func()) {`<br>internal/ui/file_manager_ops.go:379 `ui.Row(c.Key("file-manager-ops-actions")).`<br>internal/ui/memory_panel_views.go:943 `ui.Row(c.Key("memory-create-actions")).`<br>internal/ui/context_panel_views.go:603 `ui.Row(c.Key("context-create-actions")).` |
| SectionHeading | controls_section.go（新） | `func SectionHeading(c *ui.Context, key any, title string) ui.Element` | internal/ui/settings_tree.go:395 `ui.Text(c, title).`<br>internal/ui/settings_page_skills.go:269 `ui.Text(c, title).` |
| SettingRow / StackedSettingRow | controls_setting.go（新） | `func SettingRow(c *ui.Context, key, title string, control func()) ui.Element`<br>`func StackedSettingRow(c *ui.Context, key, title string, control func()) ui.Element` | internal/ui/settings_tree.go:501 `func renderSettingRow(c *ui.Context, key, title string, control func()) {`<br>internal/ui/settings_tree.go:510 `func renderSettingRowContent(c *ui.Context, key, title string, control func()) {`<br>internal/ui/settings_tree.go:529 `func renderStackedSettingRow(c *ui.Context, key, title string, control func()) {` |
| SortableSource / SortableTarget | controls_sortable.go（新） | `func SortableSource(handle ui.Element, scope, id string)`<br>`func SortableTarget(row ui.Element, scope, targetID string) (overID, droppedID string)` | internal/ui/sidebar.go:684 `headerRow.Drag(pVal.ID)`<br>internal/ui/composer.go:229 `if draggedID, dropped := ui.Drop[string](queueList); dropped {`<br>internal/ui/composer.go:252 `draggedID, over := ui.DragOver[string](row)`<br>internal/ui/composer_drop_overlay.go:195 `Drag(item.ID)` |
| Badge（复用，无新函数） | controls_chip.go（不修改） | `func Badge(c *ui.Context, key any, text string, tone Tone) ui.Element` | internal/ui/memory_panel_views.go:302 `ui.Text(c, strconv.Itoa(count)).` |
| IconButton（复用，无新函数） | controls_button.go（不修改） | `func IconButton(c *ui.Context, p IconButtonProps) ui.Element` | internal/ui/file_manager_tabs_view.go:159 `closeBtn := ui.ButtonBase(c.Key("file-manager-tabbar-close-btn")).`<br>internal/ui/settings_tree.go:304 `closeBtn := ui.ButtonBase(c.Key("settings-sheet-close-"+sh.ID)).`<br>internal/ui/swarm_bee_floats.go:925 `closeBtn := ui.ButtonBase(c.Key("swarm-strip-close")).`<br>internal/ui/diff_panel.go:520 `Label("关闭").` |
| SearchField / TextField（复用，无新函数） | controls_input.go（不修改） | `func SearchField(c *ui.Context, p TextFieldProps) ui.Element`<br>`func TextField(c *ui.Context, p TextFieldProps) ui.Element` | internal/ui/file_manager.go:553 `in := ui.TextInput(c.Key("file-manager-search-input"), query).`<br>internal/ui/memory_panel_views.go:984 `ui.TextInput(c.Key("memory-search-input"), &query).`<br>internal/ui/git_form_windows_ui.go:690 `in := ui.TextInput(c.Key("git-form-branch-search"), &query).`<br>internal/ui/file_manager_ops.go:340 `ui.TextInput(c.Key("file-manager-ops-input"), &st.ops.name).`<br>internal/ui/context_panel_views.go:589 `ui.TextInput(c.Key("context-new-name"), &name).` |

## API 的最小契约与视觉选择

1. **CopyButtonProps**：`Key any; Label, Tooltip, Text string; Value func() string; Mode CopyButtonMode; Copied, Disabled bool; OnCopy func()`。Mode 只有 CopyInline、CopyIcon、CopyText，分别对应实际存在的图标加字、仅图标、仅文字。Text 是调用方算好的当前显示文字，因此 pairingCopyLabel 和 AGY 的反馈寿命不用迁入控件。Value 必须延迟到点击才调用，避免每帧计算 b.Code()。禁用或 Value 为 nil 时不得写剪贴板或调用 OnCopy。使用现有 ControlXS / RadiusSM / Space0 / Space2 / TypeCaption，图标采用 IconSM；Copied 用 ThemeSuccess。正文、计划原来的常驻 hover 底色统一为悬停时才出现。远程/AGY 的 accent 常态保留在 CopyText 模式，成功统一 success。OnCopy 负责既有 markCopyFeedback、markRemoteCopied、markAgyClipboardCopied 和代码块 toast，不能删除计时及失效重绘。旧业务函数可保留为数据适配器，其内部只调用 CopyButton。
2. **AvatarProps**：`Key any; Name, Text string; Icon *ui.SVG; Size, FontSize float32; Foreground, Background, Border ui.Color`。Icon 优先，其次显式 Text，再取 TrimSpace(Name) 的首个 rune；Name 为空则不画字。Text 用于保留总管的完整 emoji/字样，不截断它。Radius 由 Size/2 得到；透明 Border 不画描边。颜色由现有 theme/persona/swarmAvatarColor 提供，不建立新调色板；Size、FontSize 保留当前业务尺寸。返回 Element，叠放的负 Margin 与 Surface ring 由现有 stack 设置。没有 OnClick、Busy、Selected、StackLimit 等字段；这些已由业务包装处理。
3. **EmptyStateProps**：`Key any; Icon *ui.SVG; Title, Description string; Compact bool; Action func()`。Compact 对应当前技能、空目录、暂无子代理的紧凑 TypeFootnote 状态；默认沿用 Diff 的 TypeTitle、Space3 和现有 diffStatusIconSize。图标为空就不留图标槽。Description 为空就不留占位。Action 是已有重试按钮等内容，不带新文案。返回内容列，页面保留自己的 Fill/Grow/Center/Margin。记忆/侧栏图标与标题细微大小统一到默认样式；技能标题从 TypeProse 统一到紧凑样式。所有已有说明字符串原样保留，不新增次级标签。
4. **LoadingIndicator**：只画当前 spinner 和可选原有文字，gap 用 Space3；size 仅接受调用处已有 IconSM/IconMD 或现有命名尺寸，文本为空不留间距。减少动效时使用静态状态图形并保留可访问的加载 Label。生命周期、刷新、错误、超时由页面负责。
5. **ProgressBarProps**：`Key any; Label string; Ratio, Height float32; Track, Fill ui.Color`。Ratio 归一到 [0,1]，NaN 当作 0，Height 使用已有命名常量，Fill/Track 从 Theme 函数传入。只画已知比例、不加闪光、阶段标签、百分比文字或默认过渡。保留零值轨道、满值不多出空白、圆角与 Grow 分配；外部 Absolute/Bottom/容器 Grow 由调用方在返回 Element 上设置。为辅助功能提供比例与 Label；具体 Range 能力以当前 MyGo 已支持 API 为准，不引入版本升级。
6. **TabButtonProps**：`Key any; Label, Tooltip string; Active bool`。body 为空时画 Label；有 body 时由调用方画计数、关闭等。公共按钮采用 RadiusSM、Space1/Space2、ThemeSelected/ThemeHover，文字常规 TypeFootnote；记忆选中底色从 accent 薄底统一到 ThemeSelected。返回 ButtonBase 元素，不接受 OnClick，调用方在构建完 Children 后按「关闭 > 双击固定 > 激活」处理，避免嵌套关闭触发外层激活。文件 temporary、不在当前 pane、终端错误文字颜色仍在 body 中处理。保留水平滚动与不收缩行为。
7. **anchoredPopoverProps**：`Key any; Anchor ui.Element; Open *bool; Align PopoverAlign; MinWidth, MaxWidth, MaxHeight float32; OnDismiss func()`。这是私有底层，统一空指针/关闭短路、AttachTo、对应方向 Space1 间隔、popoveRise 及 Base 返回后的 Dismissed 回调。Width 继续是卡片宽度，不能错施到裸面板。以现有 appPopover 的 rise 为统一入场，删除 PopoverCard 独立缩放入场。共享卡片函数只设置 RadiusLG、主题 surface、Hairline、ElevPopover，不设置 padding；PopoverCard 自己保留 Space2，旧 popoverCard 调用点保留各自 padding。不把裸面板设背景/边框。PopoverCard 外观优先沿用其当前 ThemeSurfacePopover/ThemeHairline；旧轻量卡片跟随这一选择，需人工比较。
8. **SheetProps**：`Key, PanelKey, PanelLabel, CardKey, Label string; Open *bool; Width, Height float32`。Key 是原 modalDrag 身份；其余 Key/Label 原样保留现有面板与卡片的定位身份，PanelKey/PanelLabel 为空则不附加。Width/Height 由调用方现有 modalResizeSize 计算；控件只限制窗口可见上限，不接管业务默认尺寸、FitContent 测量或用户 resize 状态。外层 DialogBase 统一遮罩，调用 modalDragPanel；内层卡片统一 RadiusLG、ThemeWindowBg、Hairline、ElevDialog，不加统一 Padding/Gap，调用者用现有内部容器保留它们。返回值是内层 card，原来需要 Clip 的设置/知识库等调用在返回元素上继续 Clip，其他窗口不被迫裁切。body 内保留 modalDragHandle、Scroll、Footer 和 modalResizeGrips/Min。Sheet 不新增 OnDismiss，而由现有 `if !open` 分支继续执行未保存确认、清草稿及 modalDragReset，避免双回调。现有取消按钮也走原关闭函数。创建记忆、创建文档两处 modalDragModal 卡片也迁到同一 Sheet；它们的标题拖动、最小高度与原回调不变。所谓 sheet 在当前代码中是居中、可拖动的 DialogBase 卡片，不引入贴底吸附或向下拖关闭。
9. **FormActions**：只是 FillWidth + Justify(End) + AlignItems(Center) + Gap(Space3) 的行容器；body 里继续放现有 Button、业务 busy 提示和按键逻辑。没有 Submit、Cancel、校验器、错误 schema 或全局快捷键。输入直接使用现有 TextField/SearchField；多行且有 monospace/特殊行高的记忆编辑框保留 TextArea，不扩展 TextFieldProps。Git 原生独立窗口继续是窗口，不能套进 Sheet。
10. **SectionHeading / SettingRow**：SectionHeading 返回当前 TypeCaption/500/ThemeTertiaryText 文本，位置和右侧动作由页面负责。SettingRow 返回布局元素并使用私有 `settingRowContent` 共用左右栏；StackedSettingRow 是上下布局。原 renderSettingRow 和 renderStackedSettingRow 保留薄包装：公共布局构建时先对根元素应用现有 applySettingsRowFocus，再构建子元素；可用内部 `settingRow(c, key, title, stacked, decorate func(ui.Element) ui.Element, control)` 实现，decorate 只留包内，避免公共 API 依赖 focus 全局。原树中直接调用 renderSettingRowContent 的路径改调用同一私有内容函数。不要移动 settingsRowFocus 及 applySettingsRowFocus，也不改变 ScrollIntoView 时序。技能自绘行保留其开关和描述布局，仅适配能够等价使用的左右栏部分。
11. **SortableSource / SortableTarget**：使用私有 `sortablePayload{Scope, ID string}`，scope 由界面实例和列表域共同组成，例如 `window/thread/queue` 与 `window/projects`。Target 只过滤非本域、空 ID、自身 drop 并返回 overID/droppedID，不执行业务移动。targetID 为空代表队列容器落点，继续从 queueDropBeforeFor 取最近目标；不能把空值理解为移到末尾。队列手柄调用 Source，整行和容器调用 Target；侧栏整行调用 Source/Target。排序事务、成功后清理落点、错误和 invalidate 仍由调用方负责。不新增指示线样式、不改变过滤态侧栏禁止排序规则。

## 删除零生产调用组件，保留活逻辑

已排除测试和注释检查生产调用。MessageCard、ToolChip、MenuItem、WorkspacePanel、SkeletonLine/Block 没有生产调用。另发现 Dialog 与 ConfirmDialogPreset 也没有生产调用，后者只调用前者，均删除，避免新 Sheet 与第二套确认弹窗继续并存。RenderConfirmDialog / ConfirmDialog 保持原实现与签名。

不能直接整文件删除 controls_message.go，因为两个纯函数被生产使用：

- internal/ui/content_width.go:55 `return bubbleMaxWidthFor(timelineContentMaxWidthWide, setting, percent, padX)`
- internal/ui/timeline_rows.go:4468 `shape, tone := toolStateMarker(toolMessageState(m))`
- internal/ui/controls_message.go:50 `func bubbleMaxWidthFor(columnWidth float32, setting string, percent int, padX float32) float32 {`
- internal/ui/controls_message.go:107 `func toolStateMarker(state ToolState) (toolMarkerShape, Tone) {`

A 批将 bubbleMaxWidthFor 和 userBubbleRatio 移至 content_width.go，将 toolMarkerShape、常量、toolStateMarker 移至 timeline_rows.go；两者都是业务内部算法，不为了搬家新建公共组件。将相关测试转入本批新增的专门测试文件，再删除 controls_message.go 和 controls_message_test.go。ToolState 仍有生产使用，tokens.go 不动。

C 批删除 controls_surface.go 中 PanelProps、WorkspacePanel、skeletonFill、skeleton、SkeletonLine/Block，保留 SectionCard、Banner、Tooltip。删除 controls_popover.go 中 MenuItemOpts/MenuItem、DialogProps/Dialog、ConfirmProps/ConfirmDialogPreset 及只有被删组件使用的辅助代码。测试随删除精确裁剪，不删除 PopoverCard、Banner、SectionCard、Tooltip 的测试。

## 实施编排：三轮九批

**文件所有权规则比按组件分工更优先：下列九批的可修改文件集合两两不交叉，跨轮也不交叉。每一批是一个 bee 的完整任务。新文件也有唯一所有者；不允许迁移 bee 回头修改公共组件。** 第一轮四批互不依赖；第二轮四批只依赖已合并的第一轮，不互相依赖；第三轮只有一批。不要在同一轮通过临时导出、共享测试工具或顺手改 theme/tokens 建立依赖。

测试文件名标为“新”即计划创建，并非声称当前存在。不更新公共 guard 测试来放行；若守护测试失败，应修实现。实现 bee 只在各自清单内写测试；主代理在合并后统一执行测试与 dev 确认。

### 第一轮 A：复制、已知进度与消息死组件清理

可修改完整清单（16 个）：

- internal/ui/controls_copy.go（新）
- internal/ui/controls_copy_test.go（新）
- internal/ui/controls_progress.go（新）
- internal/ui/controls_progress_test.go（新）
- internal/ui/timeline_rows.go
- internal/ui/approval.go
- internal/ui/timeline_error_row.go
- internal/ui/settings_remote_tailscale.go
- internal/ui/settings_remote.go
- internal/ui/settings_agy.go
- internal/ui/controls_message.go（删除）
- internal/ui/controls_message_test.go（删除）
- internal/ui/content_width.go
- internal/ui/content_width_consolidation_test.go（新）
- internal/ui/timeline_tool_marker_test.go（新）
- internal/ui/copy_migration_test.go（新）

步骤：

1. 新建 CopyButton 和 ProgressBar，冻结本文契约。先迁正文/代码/计划/错误/远程/AGY/禁用入口，保留原包装函数和状态回调。
2. 迁待办比例轨道，业务 todoProgressRatio 与完成条件原地不动；附件调用留 E 批。
3. 按前述位置迁活的纯函数与测试，删 MessageCard/ToolChip 及其专属类型和文件。不要删除 ToolState，不动 contentColumnWidth 等业务布局算法。
4. 新测试按现有 ui.NewTester 写法验证：Copy 三种显示、正确剪贴板文本、Disabled 和 nil Value 不触发、只有点击才求 Value、OnCopy 一次、反馈图标；代码流式 Open 时不能复制，关闭后可以且保留 toast。进度负值/NaN/零/中间/超一，轨道尺寸与 fill 不越界。原 bubble 与 marker 参数用例保留，不为已删除 UI 保留孤立测试。

关键锚点：

- internal/ui/timeline_rows.go:2970 `Disabled(b.Open)`
- internal/ui/timeline_rows.go:2977 `s.setToast("已复制")`
- internal/ui/settings_agy.go:1247 `time.AfterFunc(agyClipboardNoticeDuration, st.invalidate)`
- internal/ui/controls_message.go:50 `func bubbleMaxWidthFor(columnWidth float32, setting string, percent int, padX float32) float32 {`

### 第一轮 B：头像、空态与加载原语

可修改完整清单（11 个）：

- internal/ui/controls_avatar.go（新）
- internal/ui/controls_avatar_test.go（新）
- internal/ui/controls_empty.go（新）
- internal/ui/controls_empty_test.go（新）
- internal/ui/controls_loading.go（新）
- internal/ui/controls_loading_test.go（新）
- internal/ui/sidebar_steward.go
- internal/ui/sidebar_bees.go
- internal/ui/sidebar_activity.go
- internal/ui/diff_panel.go
- internal/ui/avatar_empty_migration_test.go（新）

步骤：

1. 新建三个原语。迁总管静态头像与侧栏 bee 内部 glyph；保留按钮、选中描边、状态角标、导航和未知人设回退。
2. 用 EmptyState 替换 Diff 状态内容与侧栏无结果卡片；页面的中心布局/间距及 retry 回调原地保留。Diff 现有加载呈现可用 LoadingIndicator，但不改变加载判定；LoadingIndicator 在本批至少由这个实际分支使用，不能成为零调用控件。
3. Diff 关闭直接使用现有 IconButton，保留原回调；不修改 swarm_mentions.go 和 effort_slider.go，它们保留业务特化形态。
4. 测试覆盖中文首 rune、空名字、Text 优先及完整 emoji、Icon 优先、尺寸与可见 label；无图标/无说明时无空槽，重试可点击一次，紧凑空态仅显示标题；加载开关及 ReduceMotion 的静态替代。复用现有截图/Tester 的几何检查方式，确认头像点击不受内层元素影响。

关键锚点：

- internal/ui/sidebar_steward.go:331 `renderStewardSectionAvatar(c, key+"-img", avatar)`
- internal/ui/sidebar_bees.go:725 `renderSidebarBeeBadge(c, btnKey, status)`
- internal/ui/diff_panel.go:468 `if onRetry != nil {`
- internal/ui/sidebar_activity.go:169 `ui.Text(c, "无结果").`

### 第一轮 C：弹层底层、Sheet 与死容器清理

可修改完整清单（12 个）：

- internal/ui/controls_popover.go
- internal/ui/controls_popover_test.go
- internal/ui/popover.go
- internal/ui/popover_test.go
- internal/ui/controls_sheet.go（新）
- internal/ui/controls_sheet_test.go（新）
- internal/ui/controls_surface.go
- internal/ui/controls_surface_test.go
- internal/ui/kanban_sheets.go
- internal/ui/scripts_sheet.go
- internal/ui/kanban_modal_test.go
- internal/ui/sheet_migration_test.go（新）

步骤：

1. 引入 anchoredPopover，所有四个现有入口只委托这个底层。popoverRise / popoverCardShadow 原辅助逻辑可留原文件，禁止改动效基础设施。删 PopoverCard 的独立缩放入场，统一 rise；只在 Base 返回后处理 Dismissed，回调至多一次。
2. 抽共用卡片样式，保留 PopoverCard Space2 和旧卡片自定义 padding，检查不得多出第二圈外框。
3. 引入 Sheet，并迁看板新任务、新目标和脚本三个窗口。保留 CardKey/Label、宽高算法、取消、异步 busy、验证、scroll 与 resize grips；本批不用下一批 FormActions，原 footer 内容原封不动放入 Sheet。
4. 删除本方案列出的零调用 MenuItem、Dialog、ConfirmDialogPreset、WorkspacePanel 和 Skeleton 变体；精确移除其专属测试，保留活组件测试。
5. 新增/更新 ui.NewTester 用例：浮层上/下与右对齐、Min/Max 尺寸、外部点击/Escape 写回 open 且回调一次、关闭时不渲染、卡片只有一层外观；Sheet 外部点击/Escape、内部点击不关闭、窗口尺寸上限、拖动/resize 仍可用。看板取消不提交，脚本无效时不保存。ReduceMotion 不注册新入场。

关键锚点：

- internal/ui/popover.go:66 `if onDismiss != nil && panel.Dismissed() {`
- internal/ui/kanban_sheets.go:283 `modalDragPanel(c, modalDragKanbanNewTask, panel)`
- internal/ui/scripts_sheet.go:259 `width, height := modalResizeSize(modalDragScriptsSheet, 620, max(scriptsSheetMinHeight, winHeight-scriptsSheetChromeHeight), 360, 240, winWidth, winHeight)`
- internal/ui/controls_surface.go:39 `func WorkspacePanel(c *ui.Context, p PanelProps, body func()) ui.Element {`

### 第一轮 D：标签按钮与表单操作布局

可修改完整清单（8 个）：

- internal/ui/controls_tabs.go（新）
- internal/ui/controls_tabs_test.go（新）
- internal/ui/controls_form.go（新）
- internal/ui/controls_form_test.go（新）
- internal/ui/file_manager_tabs_view.go
- internal/ui/git_form_windows_ui.go
- internal/ui/tab_migration_test.go（新）
- internal/ui/git_form_consolidation_test.go（新）

步骤：

1. 新建 TabButton，先迁文件标签。右键菜单继续使用原工厂，temporary、另一分栏、标题最大宽度保留。closeClicked 在 Children 内先判断，之后按关闭、双击、单击顺序分派。
2. 普通关闭按钮用现有 IconButton；文件 tab 内需要 Invisible 占位的微型关闭控件保留本地绘制，不把 IconButton 的最小 ControlXS 强塞成原有 IconSM 槽位。
3. 新建 FormActions 并迁 Git 各表单按钮行，原按钮 key/Label、busy、AI 生成、验证、取消行为不变。单行字段用 TextField；分支过滤用 SearchField，OnChange 继续写 f.query；从旧 OnTextKey 迁移到 OnKeyDown，保留 k.Text()、IME、Escape、无修饰 Enter 的原条件。多行提交编辑器及特殊字体继续原组件。
4. ui.NewTester 覆盖标签选择、关闭不激活、双击固定、隐藏关闭仍占位、同名文件不同路径的独立状态；FormActions footer 顺序、禁用/处理中不提交、Enter 取输入最新文本、Escape 关闭。此批仅改变单行输入视觉，不改表单有效性算法。

关键锚点：

- internal/ui/file_manager_tabs_view.go:265 `// 关闭按钮先于外层 tab 评估点击，用标记拦下外层的激活。`
- internal/ui/file_manager_tabs_view.go:269 `closeBtn.Invisible()`
- internal/ui/git_form_windows_ui.go:672 `f.branchName = k.Text()`
- internal/ui/git_form_windows_ui.go:944 `func renderGitFormActions(c *ui.Context, busy bool, primaryLabel string, primaryDisabled bool, primaryKey string, primary, onCancel func()) {`

### 第二轮 E：Swarm 与附件调用迁移

只依赖第一轮 A/B；不依赖 F/G/H。

可修改完整清单（4 个）：

- internal/ui/swarm.go
- internal/ui/swarm_bee_floats.go
- internal/ui/composer_attach.go
- internal/ui/swarm_attachment_consolidation_test.go（新）

步骤：

1. 汇报复制改用 CopyButton，保留原 copyKey、markCopyFeedback 与短暂对勾；汇报单头像和 stack 内头像改用 Avatar。stack 的四枚上限、重叠 margin、+N、边框、name 颜色规则不变。
2. 编排空团队内容使用 Compact EmptyState；面板关闭使用 IconButton，保留 dismissSwarmFloatPanel 与 invalidate。trackSwarmPanelDrag、trackSwarmResize、停靠与尺寸持久化原样保留。
3. 附件已知比例轨道改 ProgressBar，外部叠加容器保持原 Absolute、Bottom、背景和 padding。零比例 RPC spinner 分支不迁为 determinate；可减少动效时显示静态反馈。上传动作、重试、取消与附件状态不变。
4. 新测试覆盖 stack 0/1/4/5、Unicode 首字、复制目标及反馈、空面板关闭；附件进度 0/0.5/1 及超界，零比例仍显示未知进度状态，取消/失败不呈现成功进度。

关键锚点：

- internal/ui/swarm.go:2609 `const maxAvatars = 4`
- internal/ui/swarm_bee_floats.go:940 `dismissSwarmFloatPanel(f.leadID, f.bees)`
- internal/ui/composer_attach.go:1068 `if progress <= 0 {`

### 第二轮 F：记忆、知识库与文件操作表单

依赖第一轮 B/C/D；不依赖 E/G/H。

可修改完整清单（6 个）：

- internal/ui/memory_panel_views.go
- internal/ui/context_panel_views.go
- internal/ui/file_manager_ops.go
- internal/ui/memory_panel_test.go
- internal/ui/context_panel_test.go
- internal/ui/form_panel_consolidation_test.go（新）

步骤：

1. 将记忆/知识库主窗口以及创建记忆、创建文档、文件操作弹窗改用 Sheet；保持已有尺寸计算、header/footer 测量、resize 最小尺寸和滚动策略。原 modalDragModal 调用必须一起移除，不能再嵌套套一层 DialogBase。
2. 保留知识库 requestClose 未保存确认及根层确认弹窗渲染顺序；记忆取消/创建后的 reset 不变。文件操作 Escape/Enter 仍走原业务入口，废纸篓确认 Label 唯一，错误不关闭弹窗。
3. 创建表单 footer 使用 FormActions，单行字段用 TextField。记忆正文保留有 monospace/指定高度的 TextArea，不扩展公共输入 API。记忆搜索用 SearchField，继续点「执行搜索」才请求；不加新的实时网络搜索。
4. 记忆标签用 TabButton，pending count >0 时用现有 Badge，零不渲染；带数量的状态颜色用已有 ToneAccent/ToneNeutral。待审与搜索空态用 EmptyState，加载用 LoadingIndicator。
5. 更新现有测试并新增组合测试：新建后加入待审、取消不写入、文档名称同步、未保存关闭仍需确认、文件操作 Enter 仅一次提交；标签切换及计数零/非零、显式搜索不随键入请求、后台结果错误保持原错误条。验证两个确认框并存时 Escape 顺序不倒置。

关键锚点：

- internal/ui/context_panel_views.go:79 `cp.requestClose(s)`
- internal/ui/context_panel_views.go:82 `// 未保存修改的离开确认挂在根层、且在面板弹窗之后渲染：嵌在面板卡片里会被裁剪，`
- internal/ui/memory_panel_views.go:929 `ui.TextArea(c.Key("memory-new-content"), &content).`
- internal/ui/file_manager_ops.go:353 `s.submitFileManagerOpsDialog()`

### 第二轮 G：设置布局与技能页面

依赖第一轮 B/C；不依赖 E/F/H。

可修改完整清单（8 个）：

- internal/ui/controls_setting.go（新）
- internal/ui/controls_setting_test.go（新）
- internal/ui/controls_section.go（新）
- internal/ui/controls_section_test.go（新）
- internal/ui/settings_tree.go
- internal/ui/settings_tree_test.go
- internal/ui/settings_page_skills.go
- internal/ui/settings_page_skills_test.go

步骤：

1. 提取 SettingRow、StackedSettingRow、共用标题/控件布局以及 SectionHeading。原私有包装继续承担定位和 highlight。保留 settingsDivider、SectionCard、动态 Jump 回调和控件闭包的调用时机。
2. 设置 sheet 改用 Sheet；FitContent、BodyOwnsScroll、header/content/footer Bounds 测量及用户 resize 优先级全部原地保留。head close 用 IconButton，保留唯一「关闭+标题」Label。
3. 技能分组用 SectionHeading，保留右侧两个批量按钮和已有分组数量文案。技能空态用 Compact EmptyState；行布局只复用可等价部分，不丢弃路径提示、scope/provider 分类、写入错误和部分成功处理。技能搜索已使用 SearchField，不再迁移。
4. ui.NewTester 验证左右/上下布局、长标题不挤出开关、header 操作存在、jump 一次与高亮后焦点；延用设置 sheet 测量测试，覆盖 FitContent 与手动高度。技能测试保留过滤、批量启停、部分失败、零结果、唯一关闭按钮。

关键锚点：

- internal/ui/settings_tree.go:271 `if sh.FitContent && !modalResizeUserHeight(key) && previous.head > 0 && previous.content > 0 {`
- internal/ui/settings_tree.go:328 `if sh.Body != nil && sh.BodyOwnsScroll {`
- internal/ui/settings_tree.go:507 `row.Children(func() { renderSettingRowContent(c, key, title, control) })`
- internal/ui/settings_page_skills.go:338 `st.setAgentConfWriteErr(fmt.Sprintf("%d 个技能切换失败：%v", failed, firstErr))`

### 第二轮 H：文件面板与终端调用迁移

依赖第一轮 B/D；不依赖 E/F/G。

可修改完整清单（5 个）：

- internal/ui/file_manager.go
- internal/ui/file_manager_test.go
- internal/ui/terminal.go
- internal/ui/file_terminal_consolidation_test.go（新）
- internal/ui/search_migration_test.go（新）

步骤：

1. 文件面板目录和搜索结果的空态/加载用 EmptyState/LoadingIndicator；保留 listLoading && len(entries)==0 的判断，有旧内容时不换整块骨架。
2. 文件搜索改 SearchField，清除使用 ClearLabel「清除搜索」，OnChange 委托原 fileManagerSearch，原线程/目录隔离和防抖不动。不要再外套旧输入框背景/边框。普通关闭用 IconButton；renderFileManagerRow 继续统一目录与搜索结果，保留右键菜单、保护项、单击预览、双击固定及 @引用 点击优先。
3. 终端标签用 TabButton；保留目录 Tooltip、主目录标识、错误文字色、关闭调用、创建菜单和 terminalMu。终端搜索输入保持原实现：它在实际输入元素上注册 Shortcut，当前 SearchField 返回外部行，替换会改变 Escape 的处理优先级，不为本次收敛扩展输入 API。原查找计数、Enter/Shift+Enter 与 Escape/全局快捷键及焦点恢复都不变。
4. 测试清除后回目录、只对当前 thread 搜索、防抖仍由业务实现、旧内容加载不闪空；终端多标签/关闭不会激活旧标签、查找步进和 Escape 焦点。原文件写操作测试继续使用原入口，不更改后端行为。

关键锚点：

- internal/ui/file_manager.go:399 `case listLoading && len(entries) == 0:`
- internal/ui/file_manager.go:568 `s.fileManagerSearch(threadID, *query)`
- internal/ui/file_manager.go:814 `if !props.protected && !mentionClicked && !morePressed && props.onOpen != nil {`
- internal/ui/terminal.go:1063 `func renderTerminalSearchBar(c *ui.Context, s *chatState, st *terminalState, tab *terminalTab) {`
- internal/ui/terminal.go:1118 `if in.Shortcut(0, ui.KeyEscape) {`

### 第三轮 I：native 列表拖放收敛

无同轮依赖；只使用既有 MyGo API，不依赖其他迁移批的内部实现。

可修改完整清单（6 个）：

- internal/ui/controls_sortable.go（新）
- internal/ui/controls_sortable_test.go（新）
- internal/ui/sidebar.go
- internal/ui/composer.go
- internal/ui/composer_drop_overlay.go
- internal/ui/sortable_migration_test.go（新）

步骤：

1. 新建 scoped payload、Source 与 Target。侧栏项目行与队列手柄、行、容器必须在本批一次迁完，不能让 string payload 与新结构混用。
2. 队列保留最近悬停落点和插入线，仅替换 typed DragOver/Drop 获取；拖行间空隙仍走 queueDropBeforeFor。原来拖动接管点击的行为不变。
3. 侧栏只在 lowerQuery 为空启用拖拽；用原 reorderProjectsByDrop、持久化和 invalidate。scope 必须区分窗口及业务列表，禁止项目 ID 拖到排队列表后调用 moveQueued。
4. 测试纯 payload 过滤，以及 ui.NewTester 已支持的拖放序列：自身 drop 无动作、跨 scope 无动作、同域一次移动、队列 handle 外部不成为拖拽源、行间落点、过滤侧栏不排序。若 Tester 版本不能合成原生拖放，则纯函数检查必须保留，并明确把完整手势交给 dev 人工验证，不能声称 GUI 拖放已自动覆盖。

关键锚点：

- internal/ui/sidebar.go:683 `if lowerQuery == "" {`
- internal/ui/sidebar.go:686 `if reorderProjectsByDrop(s, projects, droppedID, idx) {`
- internal/ui/composer.go:230 `if before := queueDropBeforeFor(s, draggedID); before != "" && before != draggedID {`
- internal/ui/composer_drop_overlay.go:187 `func renderQueueGrip(c *ui.Context, item QueuedFollowUp) {`

## 验证交接

现有测试模式应沿用以下做法；定位用可见文字或 Label，不用 c.Key 字符串。并存两个相同可见文案时，在测试场景中分开构建或使用唯一可访问 Label，不增加可见辅助说明。

- internal/ui/controls_input_test.go:11 `tester := ui.NewTester(func(c *ui.Context) {`
- internal/ui/controls_input_test.go:14 `if err := tester.Click("输入内容"); err != nil {`
- internal/ui/controls_input_test.go:28 `if _, ok := tester.Find("清空"); ok {`

每轮合并后由主代理在自己的检出执行相关 controls 测试和迁移域测试，随后执行现有 modal_style_guard、motion_guard 以及 UI 包回归。只使用项目支持的纯 Go 构建路径，禁止为了跑测试引入 cgo。本文列出的新测试是实施要求，本次并未创建或执行它们。

需要截图/交互验证时，由主代理用 scripts/ui_dev_sandbox.sh 启动隔离 dev 数据与窗口，再操作该窗口；不要对用户日常会话窗口做拖动、关闭或剪贴板验证。无后台任务由本方案留存。

## 风险与 dev 人工确认

| 风险 | 本方案选择 | 必须确认的场景 |
| --- | --- | --- |
| 小复制按钮变大、底色改变、反馈颜色改变 | 图标统一 IconSM，行高 ControlXS，圆角 RadiusSM；常驻底色统一悬停；文字型成功统一 ThemeSuccess | 时间线密集消息、流式代码禁用、远程长地址、AGY 多个按钮、窄错误卡；确认布局不撑高及复制对象正确 |
| 复制反馈状态键串扰或失效 | 原业务 key 与计时系统不迁移，复制内容延迟求值 | 两个按钮连续复制、不同窗口、反馈恢复、代码复制 toast；禁用时无剪贴板变化 |
| 首字头像、emoji 与重叠差异 | 显式 Text 保留完整总管 avatar，其余按 rune；保留 stack 原尺寸和负 margin | 中英文、空名、emoji、五个以上 bee、选中/忙碌/失败角标；角标不被裁切 |
| 空态标题与图标细微统一 | 默认取 Diff 样式，轻量页采用 Compact；不改描述文案 | 小侧栏、Diff 错误重试、记忆待审/搜索、技能筛选、空团队；无滚动区被内容撑高 |
| 弹层入场、surface 和锚点变化 | rise 统一；卡片只一层，旧/新入口共享定位 | composer 上方浮层、右对齐 header 菜单、hover card、窗口边缘翻转、内容滚动、外部点击/Escape 一次关闭；ReduceMotion 静态 |
| Sheet 遮罩与卡片表面统一 | ThemeWindowBg/RadiusLG/Hairline；透明自绘 backdrop 改为统一 scrim | 看板/脚本、设置、记忆、知识库、创建框与废纸篓；窗口小于默认尺寸、拖动/缩放、取消和提交中关闭 |
| 设置 FitContent、窗口高度及未保存确认 | 测量/resize/确认留业务层，按原渲染顺序 | 设置自动适高、用户拖高后不回缩、BodyOwnsScroll、知识库修改后点遮罩与 Escape、叠加确认只处理一次 |
| 标签选中底色和微型关闭事件 | ThemeSelected 统一；保留 file tab Invisible 微型关闭 | 同名文件、双击固定、关闭当前/非当前、另一分栏、终端主目录标签；关闭不能触发激活 |
| 输入框结构改变导致焦点/IME/按键问题 | 复用现有 TextField/SearchField，保留原 OnKeyDown 逻辑与最新 k.Text() | 中文输入、Enter 提交、Escape 关闭、记忆显式搜索、文件防抖和清除、终端 Shift+Enter 与焦点恢复 |
| 进度 0 与未知进度混淆 | 未知附件分支保留；额度条不迁 | 上传初始无中间值、完成/失败/取消、待办 0/满值与窄轨道，深浅主题 |
| native 拖放跨域或插入位置变化 | scoped payload；模型位移/浮窗停靠不改 | 多窗口项目重排、过滤态不可拖、队列手柄、行间松手、拖后不误点击、跨列表无动作；模型与浮窗仍做回归 |
| 删除死组件误删共享逻辑 | bubble/marker 迁移后才删 controls_message.go | 宽内容气泡比例、工具 pending/running/blocked/error，原确认弹窗仍可用 |
| 与动效 bee 同时改文件 | 不修改 tokens/theme/motion 基础文件；共同文件先由 lead 确认单一所有者 | sidebar_steward.go 的忙碌角标、popover.go 的 rise、timeline_rows.go 的状态标记可能有交集，按最终 motion API 重定位，禁止双方覆盖 |

## 明确保留与未完成事项

本轮完整迁移的是上述真实同构绘制，保留的业务特化不是待补一个万能组件：彩虹编排开关、mention 图标回退、品牌欢迎页、模型 picker 滑块、Composer 已复用的侧切换、改动统计胶囊、模型位移排序、浮窗停靠、额度分段、时间线骨架状态机和六个业务菜单工厂均保持其现有边界。

尚未验证当前 MyGo Tester 对剪贴板读取、原生拖放、resize 与 IME 合成的支持；实现时按已有能力写有意义的测试，不发明 Tester API。不具备自动能力的手势必须列到 dev 报告。本次没有执行任何检查，也没有修改本文以外的文件。主代理需协调动效 bee 的共享文件所有权并完成所有实际回归与人工视觉确认。
