# 子代理档位编辑器改用原生 Flyout

日期：2026-11-10。状态：设计完成，未修改实现；现有包测试被缺失的前端嵌入资源阻断。本文的源码行号以本工作副本当前内容为准。

## 结论与根因

推荐使用锁定版本的 `mygo.NewFlyout`，无需升级 mygo。档位编辑器与模型选择器分别使用父子原生 Flyout；复用现有表单、模型选择器状态和配置保存逻辑。正常情况下按内容高度出窗；只有屏幕工作区限制实际窗口高度时，编辑器的 Scroll 才产生滚动范围。

internal/ui/settings_swarm_tiers.go:232 `ui.Scroll(c.Key("swarm-tier-editor-scroll-" + popKey)).Width(360).MaxHeight(swarmTierEditorMaxHeight(winH, anchor.Bounds())).Children(func() {`

当前高度来自设置窗口，而非屏幕工作区；同时编辑器在父窗口的绘制面中，调整 MaxHeight 或翻转位置都不能突破父窗口裁切。历史提交 `db7b66dd2f3db1787d75f0211b39a43690f36e44` 为适应窗口空间加入了该高度计算，本次应移除这个约束来源。

internal/ui/settings_swarm_tiers.go:580 `appPopover(c.Key("popover-swarm-tier-"+popKey), rowBtn, &isPopOpen, nil, func() {`

internal/ui/settings_page_swarm.go:911 `appPopover(c.Key("popover-swarm-tier-"+popKey), btnAdd, &isAddOpen, nil, func() {`

两处入口都必须迁移。仅替换其中一处会导致新增和编辑行为不一致。

## 依赖核对

mygo 版本见 `go.mod`

已按要求浅克隆 GitHub 最新源码到 `/tmp/mygo-latest`。对照 HEAD 为 `2834714907cbf2587bc22f6b2d69b6dda832b5b1`。锁定源码来自 `go env GOMODCACHE`，实际模块缓存位于 `/Users/liukai/.version-fox/sdks/golang/packages/pkg/mod`，不是默认的 `~/go/pkg/mod`。除这次 clone 外，调研均读取本地源码。

| 对照文件 | 结果与本需求的关系 |
| --- | --- |
| `flyout.go` | 锁定版与最新逐字相同。已有 Parent、Anchor、FitContent、Focusable、Shadow、CornerRadius、SetAnchor。 |
| `fit_content.go` | 逐字相同。原生 UI 的 FitContent 会测量自然内容、限制到所在显示器工作区并重新布局；高度受限时需要内容自身提供 Scroll。 |
| `internal/platform/flyout.go` | 逐字相同。定位顺序为翻转、移回工作区、缩小；不是只取锚点上下两侧的空间。 |
| `ui/context.go` 与 `ui/services.go` | 核查未发现可直接取得所属 `*mygo.Window` 的公开入口。最新 context 的变化主要是构建、hover 和双击处理，不能代替宿主窗口绑定。 |
| `ui/element.go`、`ui/transform.go`、`ui/zoom.go` | Bounds 已包含视觉变换，原点属于当前窗口内容区；SetZoom 非 1 时 Bounds 是内容 DIP，传给 Flyout 前必须乘 `c.Zoom()`。不能另加侧栏、滚动条、标题栏或屏幕原点。 |
| `internal/darwin/window.go` | 锁定版已支持父子 Flyout：失焦关闭会沿 key window 的 parent 链检查，焦点转到后代窗口不会关闭祖先。最新另有最小化窗口可见性、输入区域、系统 popover 外观更新，未发现本方案所需的新增定位或嵌套接口。 |

因此不修改 go.mod/go.sum。升级会扩大 UI、输入、窗口生命周期等回归范围，却不能省掉本项目的宿主定位、共享状态和关闭管理。

原生 Flyout 是现有窗口能力接入，不新增控件库；直接复用仓库表单和模型选择器，无需引入 CrychicUI 包或新依赖，也不引入 cgo。

## 1. 增加编辑会话与准确的宿主窗口解析

internal/ui/settings_swarm_tiers.go:12 `type swarmTiersUIState struct {`

internal/ui/settings_window.go:10 `type settingsWindowEntry struct {`

internal/ui/settings_window.go:118 `func inlineSettingsWindowState(owner *chatState) *SettingsState {`

internal/ui/windows.go:105 `func registerChatWindow(win *mygo.Window, state *chatState) {`

internal/ui/model.go:726 `func (s *chatState) uiServices() ui.Services {`

具体改法：

- 在 `swarmTiersUIState` 中增加当前原生编辑会话的指针。会话保存 parent、hostServices、编辑窗指针、编辑窗 services、模型子窗状态、最后 Anchor 和会话序号；草稿、writePathsText、saveError 仍只有原来的这一份。会话不复制 SettingsState，不重新从磁盘加载配置。
- 窗口生命周期代码集中放在新增的 `internal/ui/settings_swarm_tier_flyout.go`；该文件是执行阶段新增目标，当前尚不存在。不要建立全局通用浮窗注册中心。
- 增加宿主解析 helper，参数为当前 `c.Services()` 和 SettingsState。独立设置窗口先核对 `settingsWindow.state == st` 且 `entry.services == services`，返回有效的 `entry.win`。
- 内联情况下，仅在 `settingsWindowInline` 模式下，从现有 chatWindows 注册表按 `chat.uiServices() == services` 查找当前绘制窗口。不要使用 `st.chat`、`entry.owner` 或 `FocusedWindow()` 猜测；共享设置可能由 visitor 主窗口渲染，聚焦窗口也可能已经是子浮窗。
- 窗口解析在触发点击的那一帧完成，并把该帧 services 固定到会话。后续只允许这个宿主的渲染更新 Anchor；其他 visitor 的重绘不能抢走浮窗。
- NewFlyout、SetAnchor 和销毁都在主线程执行，沿用 mygo.RunOnMain；排队前登记会话，防止连续点击创建两个窗口。调用 RunOnMain 前释放状态锁，不能假设它一定异步执行。
- 复制全局条目与注册表信息时加原有锁；在锁外调用窗口方法。没有真实父窗口的 ui.NewTester 使用注入的窗口创建边界，不调用 NewFlyout(nil parent)，也不在生产环境悄悄回退为旧弹层。

验收与测试：独立设置页、内联主窗口、visitor 主窗口分别解析到实际提供该 Anchor 的窗口；焦点在模型子窗时结果不变；零 services、已销毁窗口、纯 headless 环境不误绑任意聊天窗口。重复点击同一入口只聚焦现有会话；已有草稿时点另一档位或添加，不直接覆盖当前草稿。

## 2. 抽出表单，删除父窗口高度约束，接通两个入口

internal/ui/settings_swarm_tiers.go:215 `func swarmTierEditorMaxHeight(winH float32, anchor ui.Rect) float32 {`

internal/ui/settings_swarm_tiers.go:230 `func renderSwarmTierEditorPopover(c *ui.Context, st *SettingsState, uiSt *swarmTiersUIState, anchor ui.Element, popKey string, providers []ProviderInfo) {`

internal/ui/settings_swarm_tiers.go:556 `func renderSwarmTierRow(c *ui.Context, st *SettingsState, uiSt *swarmTiersUIState, tier config.SwarmTier, providers []ProviderInfo) {`

internal/ui/settings_page_swarm.go:891 `btnAdd := ui.ButtonBase(c.Key("btn-swarm-tier-add")).`

具体改法：

- 将编辑函数改名为表单渲染函数，去掉 anchor 参数、外层固定 650 的滚动限制和窗口高度读取。保留字段 key、标题、布局、校验、删除守卫、默认档重置与保存函数。
- 两处入口共用 open helper：先初始化新建或编辑草稿，再创建原生窗；替换 appPopover 与 wasOpen/isPopOpen/isAddOpen 强制恢复开关的代码。不要保留第二份隐形表单。
- 使用 `Parent: 实际宿主`、`Placement: mygo.PlacementBottomStart`、`Gap: Space1 对应的 DIP`、`FitContent: true`、`Focusable: true`、`Shadow: true`、`CornerRadius: RadiusMD 对应的 DIP`、`Popover: false`。初始 Width 可为 360；MaxHeight 设 0，不加 650 或父窗口高度上限；不设超过小屏可用高度的 MinHeight。
- 内容根使用普通 Column/Box，宽度最多 360，填满实际可用宽度。根内保留一个 `Scroll(...).Grow(1).FillWidth()` 包住自然高度表单。根可以 Fill，表单本身不能固定高度；正文溢出时让 Scroll 收缩。依据锁定 mygo 的原生 FitContent 滚动测试结构实现，不能把根和所有子节点都定为当前 `c.Size()`，否则会把测量值锁死在初始尺寸。
- 将表单卡片 Width(360) 与内部 Width(328) 的硬宽度改为受根宽度约束的 FillWidth/MaxWidth，保持正常屏幕下原尺寸；屏幕工作区比卡片窄时不让输入框横向越界。
- Anchor 使用触发元素 Bounds，按 `c.Zoom()` 转成父窗口 DIP，再将左上向下取整、右下向上取整组成 mygo.Rectangle。默认 Zoom=1 时直接对应 Bounds；不要按 Retina backing scale 再乘一次。Bounds 未布局时延至下一帧，不用零矩形开在屏幕角落。
- 后续宿主帧在同一触发元素仍可见时比较矩形，变化才调用 SetAnchor；不在每帧无条件重定位。元素滚出可见区域、原档位消失或宿主不再拥有此入口时进入第 5 步清理。不要把 `ui.Context` 或 `ui.Element` 留给异步回调使用。

验收与测试：将设置窗口缩到 680 高、屏幕工作区仍足够高时，完整编辑器可超出设置窗口且无需滚动；展开写入白名单、增加提示词行数和显示 saveError 后，窗口按内容重测。工作区不足时滚动可到达保存/取消，窗口不超出工作区。测试 Anchor 的普通、滚动后、带视觉变换、Zoom=1.3、负坐标显示器和未布局情况；移动/缩放宿主后位置跟随，跨显示器按新工作区约束。

## 3. 共享草稿，并把重绘送到正确的窗口

internal/ui/settings.go:441 `func (s *SettingsState) invalidate() {`

internal/ui/settings_window.go:206 `func invalidateSettingsWindow(st *SettingsState) {`

internal/ui/swarm_bee_window.go:141 `func renderBeeWindow(c *ui.Context, parent *chatState, beeID string, child *chatState) {`

internal/ui/git_form_windows_ui.go:218 `func (f *gitForm) invalidate() {`

internal/ui/git_form_windows_ui.go:309 `func (f *gitForm) view(c *ui.Context) {`

具体改法：

- 两个原生窗的 Content 每帧先 applyAppTheme，各自登记自己的 c.Services()。窗口不共用 ui.Context，也不能把子窗 services 写进 settingsWindowEntry 或 chatState 的宿主 services。
- 保留 UI 草稿只在 UI 主线程读写的方式。后台配置/目录刷新继续遵守现有锁；读取配置或模型目录用现有快照/目录 helper，不额外共享可变 slice 缓冲。新建会话时编辑已有档位的切片字段使用独立副本，防止取消前写入底层配置。
- 扩展 SettingsState.invalidate：在原来的聊天窗和独立设置窗之外，取得当前会话及其模型子窗 services 快照，在锁外分别 Invalidate。编辑会话直接保存 hostServices，确保内联 visitor 宿主也重绘，不能只依赖 st.chat。
- 所有控件回调沿用 st.invalidate；只修改 uiSt 草稿，不自动写盘。编辑窗保存/重置、模型回调、后台模型目录刷新都会触发两边更新。纯 UI 输入由子窗自身事件驱动出帧；影响父页的回调仍显式 invalidate。
- OnClosed 依据会话身份/序号清理自身窗口和 services。旧窗口的晚到回调不能清掉新会话。排队创建时检查同一序号、页面、宿主仍存活后再建窗。

验收与测试：在子窗输入后保存，宿主档位行立即更新；保存失败时错误在子窗可见且草稿不丢；切换模型、Effort、Fast 后两个窗口一致；关闭后收到后台刷新不会重绘已销毁窗。增加失效旧会话回调测试，以及 visitor 宿主重绘测试。

## 4. 模型选择器改为编辑窗的子 Flyout

internal/ui/settings_swarm_tiers.go:375 `renderSwarmModelCapsuleEx(c, st, "swarm-tier-model-capsule", "tier-model-"+popKey, tierSel, "跟随会话子代理模型", "选择档位模型", func(next ModelSelection) {`

internal/ui/swarm_model_capsule.go:35 `func renderSwarmModelCapsuleCatalog(c *ui.Context, st *SettingsState, key, scopeKey string, sel ModelSelection, emptyLabel, triggerLabel string, onChange func(ModelSelection), catalog modelPickerCatalog, noEffort bool) {`

internal/ui/model_picker.go:84 `type ModelPickerProps struct {`

internal/ui/model_picker.go:1158 `ui.PopoverBase(c.Key(p.KeyPrefix+"model-popover"), anchor, &state.MenuOpen, func(panel ui.Element) {`

internal/ui/model_picker.go:1228 `func renderModelPickerMenuBody(c *ui.Context, p ModelPickerProps, state *ModelPickerState) {`

具体改法：

- 不能只把外层编辑器出窗；其内模型 PopoverBase 是 Absolute 内容，既不计入 FitContent，又会被编辑窗裁切。推荐保留现有胶囊和选择器主体，把该入口的面板改为第二个原生 Flyout。
- 从 renderSwarmModelCapsuleCatalog 提取构造 ModelPickerProps 的内部函数，现有普通调用仍构造 props 后走 renderModelPicker。仅档位编辑入口附加可选的原生浮窗控制状态，其余模型胶囊保持当前路径。
- 控制状态由编辑会话持有，包含 Parent=编辑器原生窗、模型窗、services 和取最新 props 的工厂函数。renderModelPicker 的同一触发胶囊在这个可选分支中打开子窗；未启用分支仍使用 PopoverBase。子窗每帧从共享 uiSt 草稿及 settingsModelPickerCatalog 构造最新 props，再调用 renderModelPickerMenuBody；不要捕获首次打开时的 tierSel 或 providers 作为长期快照，否则 Fast 连续切换和模型切换会读到旧值。
- 子窗设置 FitContent、Focusable、Shadow、CornerRadius、Popover=false；Parent 必须是编辑器原生窗。Anchor 来自编辑窗的胶囊 Bounds，使用第 2 步的坐标转换。不要用设置窗口的 Anchor。
- 子窗允许外部点击/Esc 关闭，只复位 ModelPickerState 的 MenuOpen、列表展开、搜索/更多页与胶囊快照，不丢弃档位草稿。选模型后的现有关闭语义通过关闭子窗实现；调整 Effort/Fast 保持子窗和草稿打开。关闭子窗后，把键盘焦点还给仍存活的编辑器；不能在外部应用已获焦时强制抢焦点。
- 使用 Simple 模式现有的卡片、列表和返回页面。这里没有执行侧并发数弹层，更多模型/供应商更多是原面板内换页或展开，无需再造第三层。常规 Tooltip 不承担可操作表单，关键内容不得只在可能被裁的 Tooltip 中呈现。
- 列表原有 440 高上限可以作为模型菜单本身的产品约束保留；该上限不再限制档位编辑器。子窗根仍须在工作区更矮时提供可达全部项目的滚动，不依赖固定 440 能放下。
- 原面板主体固定 330 宽。原生分支将这一级改为 MaxWidth(330)/FillWidth，并检查列表、强度卡片的固定子宽，确保极窄工作区下不从已缩小的子窗中横向溢出；普通胶囊路径保持现有宽度。

验收与测试：在编辑器上下左右边缘打开模型面板，面板能超出编辑器边界并留在工作区内；输入搜索、选模型、强度、Fast、重置与返回均可操作。打开子窗不触发草稿关闭；点击编辑器其他字段只关闭模型子窗；Esc 优先关闭模型子窗。至少补连续两次 Fast 切换、模型选中后再调整强度、重开不残留旧列表状态的测试。

## 5. 统一关闭策略与生命周期清理

internal/ui/settings_swarm_tiers.go:519 `if btnCancel.Clicked() {`

internal/ui/settings_swarm_tiers.go:533 `if btnSave.Clicked() {`

internal/ui/settings_swarm_tiers.go:125 `func deleteTier(st *SettingsState, id string) bool {`

internal/ui/settings.go:419 `func (s *SettingsState) Close() {`

internal/ui/settings.go:929 `func (s *SettingsState) switchSettingsPageLocked(id string) bool {`

internal/ui/settings_window.go:135 `func openSettingsWindowNative(owner *chatState, page string) *mygo.Window {`

具体改法：

- 编辑器用 Focusable=true 接受键盘。其 OnClose 仅在仍是当前活动编辑会话时 PreventDefault，阻止外部点击、失焦和未消费的 Esc 丢草稿；不循环 Focus/Show，不阻止用户操作设置页或别的应用。失效会话不再拦截关闭。
- 显式取消、保存成功、删除成功、父窗口关闭、切离 swarm 页面统一走 dispose helper。先在状态锁下摘除会话、使序号失效、清开关和 services；解锁后在主线程先 Destroy 模型子窗，再 Destroy 编辑窗。Destroy 不走 OnClose，因此不会被自己的保护拦住。保存或删除失败不 dispose，原有错误仍显示。
- 父窗关闭由 mygo 的 Parent 链销毁子窗，该路径不会向子窗征求 OnClose；保留父窗现有 prepareWindowClose 的文本/快捷键保存检查，不额外用档位草稿锁死父窗。SettingsState.Close 在清 swarmTiersPage 之前摘出并销毁会话，作为内联模式及重复关闭的兜底。
- 编辑窗 OnClosed 检查 parent.IsDestroyed()：父窗已经销毁时直接结束会话并清草稿，不能走下面“意外关闭保留草稿”的分支。这样内联宿主关闭即使没有调用 SettingsState.Close，也不会留下带失效 parent 的活动会话。
- 切页必须在中央 switchSettingsPageLocked 处理，不能只改侧栏点击。建议把返回值改为 `(loadProviders bool, disposeEditor func())`：在持锁区摘出待关会话；三个现有调用点在解锁后先执行 disposeEditor，再加载页面数据。三个入口分别是普通 selectSettingsPage、搜索/项目跳转及 openMagpieSettingsPage。避免在持有 s.mu 时执行 RunOnMain 或 Destroy，也不要只等下一帧销毁一个已失去渲染入口的窗口。
- deleteTier 仅在保存成功后、目标等于当前编辑 ID 时清理会话；覆盖表单按钮以外调用删除的情况。配置刷新使目标档位消失时，宿主/子窗下一帧做存在性检查并 dispose。正常重置只是更新草稿，不能关窗。
- 点另一档位时已有活动草稿则聚焦现有编辑器，不默默替换。宿主关闭、切页和目标删除属于明确生命周期终止，会清理草稿；普通外部点击保持原草稿。
- 意外原生关闭只清实际 window/services；保留活动草稿，下一次点击对应入口按“窗口不存在但草稿存在”重新开窗。必须替换原来仅比较 openPopoverKey 的点击守卫，否则会出现草稿存在却再也打不开的状态。Wayland 的合成器关闭不受 OnClose veto 保证，此路径也不丢内存草稿；本次 macOS 桌面验收仍为主。

验收与测试：外部点击/Esc 不丢编辑器草稿；取消、保存成功确实关窗；保存失败可继续编辑。关闭设置窗、内联宿主、切页（含搜索与直接跳转）、删除当前档位均无遗留两层窗口。反复清理幂等，延迟创建与旧 OnClosed 不重开窗口。关闭父窗不被草稿 veto 卡住；意外关闭后可恢复同一草稿。最后一个可写档位的现有删除守卫和配对级联回滚测试继续保留。

## 6. 外观与减少动效

internal/ui/settings_swarm_tiers.go:240 `applyElevation(c, card, ElevDialog).Children(func() {`

internal/ui/motion_presets.go:72 `func motionPopover(c *ui.Context, offsetY float32) ui.ElementTransition {`

internal/ui/motion.go:43 `func reduceMotion(c *ui.Context) bool {`

internal/ui/modal_style_guard_test.go:13 `func TestNoStyledModalOrPopover(t *testing.T) {`

具体改法：

- 两个 Flyout 的背景、内边距、圆角由一个内容根负责；原生窗口本身是无背景的宿主，不再套 ui.Modal/ui.Popover，也不为虚设的 PopoverBase 留白。
- 采用 Shadow=true 的系统窗阴影。macOS 已自带 hairline，移除原卡片 Border 和 ElevDialog；模型子窗同样不复用旧 PopoverBase 的 Border/DrawOver 箭头/阴影。Windows 11 使用系统描边与圆角；Linux 没有系统描边时可条件补一条内容边线，但不能叠加两套阴影。正常浅/深色模式只出现一层轮廓。
- Popover=false 保留现有自绘卡片风格，也避免 NSPopover 自带材料、箭头和不受应用统一控制的出入动画。
- 使用 motionPopover 为内容根提供轻微进入反馈；通过 motionTransition/ReduceMotion 将减少动效时的变化直接置终点。优先淡入，避免内容平移/放大出透明原生窗后被裁。不要动画系统窗口的位置或尺寸，也不要为了退出动画延迟生命周期销毁；关闭可直接完成。

验收与测试：浅/深色、正常/高对比下无第二层黑框、双阴影和裁切箭头；ReduceMotion=true 时编辑器、模型面板及其内部换页都没有持续或入场动画。样式守卫无需豁免：AST 只禁止调用 ui.Modal/ui.Popover，mygo.NewFlyout 不命中它；保留守卫原样。

## 7. 替换旧测试并完成有范围的验证

internal/ui/settings_swarm_tiers_test.go:11 `func TestSwarmTierEditorMaxHeight(t *testing.T) {`

internal/ui/settings_swarm_tiers_test.go:95 `func TestSwarmTiers_AddTierPersists(t *testing.T) {`

internal/ui/settings_window_test.go:9 `func TestSettingsWindowInlineRendersInTests(t *testing.T) {`

具体改法与测试边界：

- 删除 swarmTierEditorMaxHeight 及旧表驱动测试，不把窗口高度算法改名为“屏幕高度算法”。屏幕选择、翻转、滑动和缩小属于已锁定 mygo 的职责；项目测试不复制库内部算法。
- 用小范围可注入的原生创建/销毁边界捕获 FlyoutOptions、Content 和事件处理，测试一个真实表单 view 加两个 ui.NewTester 窗口的共享状态。若使用真实 *Window，必须采用项目已有可行的无原生循环测试入口；不得让普通包测试调用 native NewFlyout 或为了测试启动 mcode。
- 自动化断言 Parent、Anchor、FitContent=true、MaxHeight=0、Focusable=true、Popover=false、Shadow=true；覆盖两入口与第 1～6 步列出的状态、关闭和回调竞态。该边界只服务这两个窗口，不建立通用窗口接口层。
- 用独立表单 tester 在足够高与受限高度分别渲染，断言全表单可见/可滚至操作按钮。自然高度超过原 650 时仍应完整展示。实际屏幕工作区缩窗属于原生集成验收，不能用手动 SetSize 的 tester 假装已经验证 FitContent 或系统定位。
- 保留 saveTier、deleteTier、WritePaths、默认档重置、配对覆盖与样式守卫测试；调整依赖旧弹层位置的 UI 查找，使它们对编辑窗 Content 建立 tester，不把新内容塞回宿主视图来兼容旧断言。
- 最后仅运行 internal/ui 中相关测试及样式守卫，每条命令加 Go `-timeout` 和外层命令超时，使用 GOPROXY=off/GOSUMDB=off/GOTOOLCHAIN=local/CGO_ENABLED=0，不运行全量测试或启动本体。实现完成后的手工原生验收交给有可运行开发构建的执行环境。

当前验证记录：已尝试以下有范围测试，设置外层 60 秒和 Go 40 秒超时；尚未执行到测试函数：

```sh
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local CGO_ENABLED=0 go test ./internal/ui -run '^(TestSwarmTierEditorMaxHeight|TestNoStyledModalOrPopover|TestSettingsWindowInlineRendersInTests|TestSwarmTierDeleteGuard_UsesRuntimeReadOnly)$' -count=1 -timeout=40s
```

包准备阶段失败：`internal/webassets/embed.go` 的 `all:dist` 没有匹配文件。本次没有生成 dist、修改嵌入配置或绕过这个阻塞。原生定位、焦点、输入法与动效均未运行验证，不能宣称测试通过。

## 方案取舍与主要风险

| 方案 | 取舍 |
| --- | --- |
| 推荐：编辑器 Flyout + 模型子 Flyout | 保留现有模型菜单 UX；锁定版本支持父子焦点链；需要明确两层窗口关闭和重绘。 |
| 编辑器 Flyout + 模型面板在表单内展开 | 窗口更少，但菜单展开会改变整张表单高度；小屏更易滚动，且要改变胶囊交互。若原生集成验收出现无法解决的子窗焦点问题，这是最近的替代方案；复用 renderModelPickerMenuBody，以普通流式内容展开，不直接用 Inline=true，因为该模式没有进入完整模型列表的入口。 |
| 继续父窗内 Popover，只改 MaxHeight | 无法突破设置窗口裁切，不满足目标。 |
| 新普通独立窗口 | 能越过父窗口，却失去 Anchor/工作区自动约束，需要自行管理更多行为，不推荐。 |

最大的风险是两个独立 UI 树之间的状态失效和清理时序，其次是 Zoom 坐标与 macOS 原生焦点/输入法实际行为。最新 mygo 的最小化可见性修复不作为本次升级理由，但最小化、恢复与跨屏移动需列入集成验收。当前缺少嵌入资源，只能完成源码可行性判断与测试设计；实现、编译通过、原生实测均未完成。
