# 设置独立窗口实施计划

> 面向实施者：按文件所有权拆分任务；先约定下面的接口，再并行修改互不重叠的文件，最后集成验证。

**目标：** 设置成为可拖出主窗口的系统窗口，现有入口和设置内的二级面板保持可用。

**架构：** 单例窗口复用首次打开它的聊天窗口已有 `SettingsState`，设置窗口只拥有自身的 `ui.Services`；配置、项目上下文及主窗口仍使用原有状态。关闭设置窗口时先完成待保存输入，成功后清理界面状态；所属聊天窗口关闭时先关闭设置窗口，避免引用已经释放的会话。

**技术：** Go、`github.com/egoist/mygo`、`github.com/egoist/mygo/ui`，纯 Go，无 cgo。

**约束：** 设置窗口使用 `TitleBarHiddenInset` 和 `StateKey`；二级对话框与浮层继续使用 `ui.DialogBase` / `ui.PopoverBase`。动效继续使用 `internal/ui/tokens.go` 的时长 token、`animateDuration` 与 `ReduceMotion` 规则。不要引入新依赖。

## 接口与文件所有权

- A 组独占 `internal/ui/settings_window.go`、`internal/ui/settings_window_test.go`、`internal/ui/windows.go`、`internal/ui/app_menu.go`。
- B 组独占 `internal/ui/settings.go`、`internal/ui/modal_drag.go`、`internal/ui/settings_layout_test.go`、`internal/ui/settings_dialog_shell_test.go`、`internal/ui/settings_models_test.go`、`internal/ui/settings_sheet_jump_test.go`、`internal/ui/settings_nav_magpie_test.go`；必要的保存行为测试放在新的 `internal/ui/settings_window_save_test.go`。
- C 组独占 `internal/ui/root.go`、`internal/ui/sidebar_footer.go`、`internal/ui/palette.go`、`internal/ui/chat_header_tools.go`、`internal/ui/shortcut_help.go`、`internal/ui/onboarding.go`、`internal/ui/sidebar_steward.go` 和与这些入口对应的测试，包含 `internal/ui/settings_dialog_root_test.go`。
- D 组独占 `internal/ui/settings_sheet_shell_test.go`、`internal/ui/settings_sheet_fit_test.go` 及新的 `internal/ui/settings_window_sheet_test.go`；只调用 B 组的渲染接口，不修改 `internal/ui/settings_tree.go`。
- 接口约定：`openSettingsWindow(owner *chatState, page string) *mygo.Window`；空 `page` 保持当前页，非空 page 沿用 `OpenPage` 的旧页面及 sheet 映射。`renderSettingsWindow(c *ui.Context, st *SettingsState)` 负责完整的设置窗口内容及二级层。由 A 组提供 `invalidateSettingsWindow(st *SettingsState)`、`closeSettingsWindowForOwner(owner *chatState) bool`；后者仅对所属窗口生效，保存失败时返回 false。B 组提供 `(*SettingsState).prepareWindowClose() bool`，强制保存并在失败时保留草稿和错误信息；现有 `Close()` 在成功路径执行清理。
- 并行实施前先确认接口；A、B、C、D 合并前可能无法独立编译。禁止两个组同时修改同一文件；发现额外测试文件时交对应组处理。

## 1. A 组：系统窗口与生命周期

锚点：`internal/ui/tasks_window.go:79` 单例复用；`internal/ui/tasks_window.go:106` 窗口选项；`internal/ui/swarm_bee_window.go:100` 子窗口独立内容；`internal/ui/windows.go:351` 系统标题栏；`internal/ui/windows.go:385` 主窗口关闭拦截；`internal/ui/windows.go:508` 状态解绑；`internal/ui/app_menu.go:138` 菜单路由。

1. 在 `internal/ui/settings_window.go` 新建单例条目，保存窗口、首次打开时的 owner 和其 `getChatSettings(owner)`。在 `mygo.RunOnMain` 内创建 `960×680`、最小 `640×420` 窗口，设置标题、`TitleBarHiddenInset`、交通灯位置、跨平台标题栏高度和 `StateKey: "settings-window"`，内容调用 `renderSettingsWindow`。不可用的 owner 或设置状态直接返回 nil。
2. 已打开时复用现有窗口，避免再次 `Open()` 重读磁盘覆盖防抖草稿；传入非空 page 时对现有状态执行切页及旧 sheet 映射，然后 `Restore`、`Show`、`Focus`。单例生命周期内固定首次 owner：其他聊天窗口打开时只定位同一窗口，不更换项目上下文或状态；关闭后下次打开才选择新的 owner。
3. `OnClose` 调用 `prepareWindowClose()`，失败则 `PreventDefault()`，错误留在窗口内；成功才允许关闭。`OnClosed` 调用 `Close()` 清理 sheet 和页面临时状态，清空单例与窗口 services，并请求 owner 重绘以恢复新手引导。防止旧窗口回调清理新窗口条目。
4. 在 `internal/ui/windows.go` 的聊天窗口 `OnClose` 中先维持现有托盘隐藏行为；真正关闭所属聊天窗口之前调用 `closeSettingsWindowForOwner`，若保存失败则阻止聊天窗口关闭。确保 `OnClosed` 清理顺序不会让设置窗口持有已释放的 owner。其他聊天窗口关闭不影响设置窗口。
5. 在 `internal/ui/app_menu.go` 处理设置窗口聚焦时的「偏好设置…」与 Cmd+,：直接聚焦现有设置窗口，不依赖未聚焦聊天窗口排队后重绘；没有设置窗口时沿用 `DispatchMenuAction` 和现有无聊天窗口回退。避免菜单 accelerator 与窗口快捷键重复派发。
6. 在 `internal/ui/settings_window_test.go` 用可注入窗口操作或生命周期协调器测试：首次创建、重复打开复用、最小化恢复、非空 page 跳转、不重载未保存草稿、旧关闭回调不清新条目、所属窗口关闭与非所属窗口关闭、保存失败阻止关闭；菜单聚焦回归覆盖设置窗口已聚焦和无聊天窗口两种情况。原生系统窗口可拖动和跨平台标题栏需 dev 版本实测。

## 2. B 组：窗口内容、保存与刷新

锚点：`internal/ui/settings.go:56` 状态映射；`internal/ui/settings.go:268` 配置订阅；`internal/ui/settings.go:392` Open 重读配置；`internal/ui/settings.go:425` Close 强制保存；`internal/ui/settings.go:448` invalidate；`internal/ui/settings.go:511` 保存错误；`internal/ui/settings.go:1241` 拖动手柄；`internal/ui/settings.go:1344` 正文及防抖；`internal/ui/settings.go:1381` 标题手柄；`internal/ui/settings.go:1475` 顶层 DialogBase。

1. 保留每个聊天窗口的 `SettingsState`、`cfg/base`、`chat`、锁定项及现有配置订阅，避免破坏主窗口大量 `getChatSettings` 读取。设置窗口第一次打开才调用 `Open`；既有状态仍以原有配置变更通知刷新。`SettingsState.invalidate()` 同时请求 owner 的 `chat.invalidate()` 和 A 组的 `invalidateSettingsWindow(st)`；后者仅当单例绑定该状态且服务有效时刷新，不调用 owner 的 `setUIServices`，避免覆盖主窗口服务。异步加载、文本防抖和配置通知因此都能重绘两个窗口。
2. 将 `renderSettingsDialog` 改为 B 组定义的 `renderSettingsWindow`：应用现有主题，根容器铺满窗口，直接排列导航和详情；去掉顶层遮罩、圆角卡片、自绘拖拽和缩放手柄。导航和标题的原 `modalDragHandle` 改为系统窗口 `DragWindow()`，交通灯和其他平台系统按钮区域通过 `c.TitleBar()` 留白，交互控件不可被拖动区域遮挡。系统窗口负责尺寸与位置。
3. 保留 `renderSettingsTreeSheet`、重置确认和原焦点清理，顺序位于窗口根内容之后；原二级 sheet 仍独立消费 Esc。不要删除 `internal/ui/settings_tree.go:276` 的 `ui.DialogBase` 或内部浮层。删除 `internal/ui/modal_drag.go` 中仅供顶层设置使用的 `modalDragSettings`，其他 modal 拖拽不动。
4. 强制保存要可观察失败：修改文本和快捷键的 flush / 保存链路，将校验和 `config.Update` 错误传回 `prepareWindowClose`；失败时保留待保存标记及草稿，显示原有 `errorMessage`，禁止关闭，重试成功后才清理。不要只依赖 `Close()`：当前 `saveSnapshot` 出错后无返回值，且 `flushTextSave` 提前清掉 pending。对于本来需要显式点击保存的 sheet 草稿，关闭窗口按现有关闭 sheet 的语义丢弃未提交草稿，不自动提交；已提交的异步操作保留原语义。
5. 迁移 `internal/ui/settings_layout_test.go:60`、`internal/ui/settings_dialog_shell_test.go:10`：删除顶层遮罩、应用内拖拽、缩放及居中断言；保留内容布局与二级 sheet 的 Esc 测试，增加小窗口、标题栏留白、主内容铺满与窗口关闭清理测试。把 `internal/ui/settings_models_test.go:880`、`internal/ui/settings_sheet_jump_test.go:20`、`internal/ui/settings_nav_magpie_test.go:53` 的渲染入口迁至 `renderSettingsWindow`，保留原业务断言。
6. 在 `internal/ui/settings_window_save_test.go` 覆盖尚在防抖窗口的文本和快捷键关闭即落盘、校验失败及写盘失败保留窗口和草稿、修正后重试成功；覆盖设置修改后 owner 和设置窗口均收到刷新，以及关闭窗口后延迟回调安全。配置更新后的其他聊天窗口继续走现有 `WatchConfig`，测试包含第二个聊天状态收到通知。

## 3. C 组：所有入口与主窗口行为

锚点：`internal/ui/root.go:192` 原模态限制；`internal/ui/root.go:230` 菜单队列；`internal/ui/root.go:332` 快捷键入口；`internal/ui/root.go:428` 根层设置渲染；`internal/ui/sidebar_footer.go:123` 侧栏入口；`internal/ui/palette.go:121` 命令面板入口；`internal/ui/palette.go:450` 选择命令时关闭设置；`internal/ui/chat_header_tools.go:312` 打开方式；`internal/ui/shortcut_help.go:270` 快捷键设置；`internal/ui/onboarding.go:154` 引导跳转；`internal/ui/sidebar_steward.go:79` 行定位。

1. `root.go` 删除设置作为模态时对主窗口快捷键、Escape 和动作队列的屏蔽，并删除顶层设置渲染。`open-settings` 动作调用 `openSettingsWindow(state, "")`；保留 `getChatSettings` 供其余状态消费者使用。主窗口在设置窗口打开时仍正常处理自己的动作。
2. 侧栏、命令面板、标题栏「自定义」、快捷键帮助分别调用统一入口；移除 `palette.go` 中执行其他命令即 `st.Close()` 的旧模态逻辑。保留页面参数 `openIn`、`shortcuts`。
3. 新手引导先设置 suspended，再用统一入口打开目标页；若建窗失败恢复 suspended，避免引导永远消失。gateway 的 `openMagpieGatewaySheet` 对实际窗口绑定的状态调用，而不是盲用发起者自己的状态。总管入口同样在实际窗口状态上调用 `focusSettingsItem`，确保单例已有其他 owner 时跳转有效。窗口关闭后 owner 重绘使 `onboardingResume` 根据 `IsOpen()` 恢复。
4. 更新 `internal/ui/settings_dialog_root_test.go:9`：主窗口不再显示设置模态层，打开设置期间侧栏动作正常执行；菜单、侧栏、命令面板、Cmd+, 重复触发都命中同一窗口。更新对应 `palette_test.go`、`shortcut_help_test.go`、`onboarding_test.go` 等受入口变化影响的测试，覆盖页和 sheet 深链接、无设置状态、跨聊天窗口复用，以及引导关闭后恢复。

## 4. D 组：二级面板验证迁移

锚点：`internal/ui/settings_tree.go:242` 二级 sheet；`internal/ui/settings_tree.go:276` DialogBase；`internal/ui/settings_tree.go:647` 打开面板；`internal/ui/settings_sheet_shell_test.go:9` 头尾插槽；`internal/ui/settings_sheet_fit_test.go:9` 自适应。

1. 保留 `settings_sheet_shell_test.go` 的 Header/Footer、无障碍标签与供应商最小宽度断言；保留 `settings_sheet_fit_test.go` 的内容贴合、滚动和自绘二级面板拖动尺寸断言。这些测试针对的是仍存在的二级 sheet，不能因顶层改为窗口而整文件删除。
2. 在 `settings_window_sheet_test.go` 使用 `ui.Tester` 挂载 `renderSettingsWindow`，覆盖 sheet 在新窗口上下文中显示、关闭或 Esc 只关闭 sheet、长内容不超窗口、切页关闭旧 sheet、关闭设置清除二级状态；覆盖 ReduceMotion 时没有额外过渡。`ui.PopoverBase` 的关闭状态仍在组件返回后读取。

## 5. 集成与验收

1. 整合各组，检查 `Open()`、`OpenPage()`、`renderSettingsDialog` 和 `modalDragSettings` 的全部调用，确认仅保留有意保留的状态方法和测试；检查没有组外文件冲突。
2. 实施者运行设置、入口和窗口相关 Go 测试，以及 `internal/ui` 的 modal/motion 守护测试；出现失败先定位再修复，不把旧模态断言直接删除了事。
3. 启动 dev 版本实测：从侧栏、菜单、Cmd+,、命令面板和深链接打开；把设置拖出主窗口、缩放并关闭重开核对位置；验证二级 sheet、引导恢复、文本与快捷键防抖、主窗口实时刷新、关闭所属聊天窗口和 ReduceMotion。检查 macOS 交通灯与 Windows 系统按钮留白。

**设计选择和边界：** 单例固定首次 owner，避免跨项目切换时隐式迁移状态；关闭 owner 必须先关闭设置窗口。关闭时只强制保存原有自动保存输入，显式保存型 sheet 草稿沿用取消语义。系统退出或平台窗口关闭回调是否均可阻止关闭，应在实现前核对 mygo 行为并在 dev 版本验证；若系统强制退出无法拦截，需明确记录平台限制。
