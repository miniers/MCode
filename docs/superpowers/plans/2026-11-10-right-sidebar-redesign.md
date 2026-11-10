# 右侧栏架构与交互重设计方案

日期：2026-11-10。定位与设计稿。只新增本文，不实施代码。

| 现状 | 目标 |
|---|---|
| 文件、预览、改动各自开关，互斥关闭 | 一个右栏，顶部「文件 / 改动 / 浏览器」标签 |
| 只保存各面板宽度，切换会话丢上下文 | 按会话保存开关、当前标签、统一宽度与定位 |
| 切到其他面板销毁 WebView | 隐藏页面但保留实例，关闭网页标签或窗口才释放 |
| 单层文件列表，手动刷新 | 虚拟文件树、Git 标记、只看改动、监听刷新 |
| 文件树与内容互相替换，窄栏拆分拥挤 | 文件导航与只读内容分开；窄栏用返回导航 |
| 两套自研 diff 行模型，只支持统一视图 | 共享 editor.DiffView，回合优先，支持并排与行内高亮 |
| 整轮撤销藏在时间线；评论无法定位 | 安全的 hunk 回滚，定位评论作为待发送附件 |
| 单网页，无历史、地址不同步、只采集错误 | 多网页标签、历史、地址同步、console 与截图 |
| 标注按钮说发送，实际只追加草稿 | 「添加到对话」；附件进入 composer，用户决定发送 |
| 头部七个工具入口混排，命名与空态各异 | 一个右栏开关、终端、更多菜单；统一头部和空态 |

**摘要**

- 推荐名称为「右栏」；代码名称用 Inspector，不在界面新增英文标题。
- 根因是三个面板分别拥有布局、开关、资源生命周期与数据状态，缺少会话级容器。
- P0 先收拢外壳并修数据竞争、搜索与状态丢失；不先重写所有功能。
- P1 做文件，P2 做审阅，P3 做浏览器，P4 再联动 agent。
- 每阶段独立可用；不做自由停靠，也不把聊天产品做成 IDE。
- 默认复用 pinned mygo 与仓库组件，不引入 CrychicUI；具体核对与理由见第 4 节。
- 所有上下文先进入 composer 附件；不自动发送，不把网页内容当可信指令。
- 本文依据静态源码与有限 git 历史。未运行应用、构建或测试。

## 1. 证据范围与根因

行号以当前工作副本为准，引用为该行源码原文。新文件均标注「拟新增」，没有虚构现存行号。外部依赖的文件位置用 import path 与符号定位，不冒充仓库内路径。竞品结论采用主代理提供的已完成调研摘要，本文不声称再次实测竞品。

### 1.1 容器只统一了分栏，没有统一状态与资源所有权

sidePane 注册表同时描述尺寸、锁、开关、菜单、快捷键、渲染；互斥依赖逐个调用 close。各面板的 open/visible 与 width 又各自保留，无法表示「隐藏但保留」。文件和改动缓存按窗口而非会话保存；文件切换会话会清空 tabs。右栏只挂载默认聊天分支。

internal/ui/side_pane.go:24 `type sidePane struct {`

internal/ui/side_pane.go:250 `// closeOtherSidePanes 关闭除 keepID 之外所有已打开的右侧面板，是所有互斥关系的唯一入口。`

internal/ui/file_manager.go:188 `st.tabs = nil`

internal/ui/root.go:356 `renderSidePaneSplit(c, state, renderChatArea)`

internal/ui/ui_state.go:40 ``SidePaneWidths map[string]float32 `json:"side_pane_widths,omitempty"` ``

头部把文档、记忆、文件、终端、浏览器、看板、改动放在同一组。标题栏依靠允许溢出避免点击被裁剪，不能只换图标而不消除拥挤根因。

internal/ui/chat_header.go:2271 `groups := []headerGroup{`

internal/ui/side_pane.go:471 `// 辅助组按钮会越出主区右边界。主区一旦 Clip，这些按钮的命中矩形就被裁掉，点击会落到`

### 1.2 文件能力已有，问题是导航、刷新和查询状态边界

当前单层目录 + 面包屑。目录请求有 gen，搜索请求有 searchGen，但两者共用 err；清空搜索未清除搜索错误，成功搜索也未清除旧 err。searchTruncated 始终赋 false；更深层原因是 app 转换层丢弃了后端已经返回的 Truncated。不能用「结果数等于上限」猜测截断。

internal/ui/file_manager.go:169 `func (s *chatState) loadFileManagerDir(threadID, dir string) {`

internal/ui/file_manager.go:272 `st.err = err.Error()`

internal/ui/file_manager.go:278 `st.searchTruncated = false`

internal/app/client.go:544 `return matches, nil`

internal/remote/wire/workspace_types.go:80 ``Truncated bool              `json:"truncated"` ``

保留临时/固定文件标签、行定位、只读代码、Markdown 和现有文件操作。PDF 无法从当前 FilesRead 获得二进制正文；不能只把后缀路由到浏览器就宣称支持。HTML 当前进入文本分支，只显示源码。SVG 不能按普通位图解码假定成功。

internal/ui/file_manager_tabs.go:18 `type fileManagerTab struct {`

internal/ui/file_manager.go:968 `markdown.View(c.Key(keyPrefix+"file-manager-md-view"), tab.content, swarmReportMarkdownOptions(c, s, fileManagerPreviewFontSize(s)+1)).`

internal/ui/file_manager.go:952 `ui.Text(c, "该文件为二进制格式，暂不支持预览").`

internal/remote/files.go:587 `Kind:       wire.RemoteFileContentKindBinary,`

### 1.3 浏览器把 UI 可见性与页面存活混为一谈

切换面板调用 closePreview，继而 Destroy 页面。渲染读取 st.viewport、st.annotations、st.consoleErrors，后台轮询却在 previewMu 内写切片；部分抽屉复制了快照，外层条件没有复制。根因是没有统一的不可变渲染快照和页面代次校验，不能靠把整次渲染包在锁内解决。

internal/ui/preview.go:527 `web.Destroy()`

internal/ui/preview.go:602 `selected := st.viewport == mode`

internal/ui/preview.go:280 `st.consoleErrors = errors`

internal/ui/preview.go:783 `if st.annotOpen && len(st.annotations) > 0 {`

内部跳转没有地址同步，错误收集器依赖加载完成后 Eval 安装及 700ms 拉取，丢失早期日志。标注实际追加 draft，不会发送消息。静态端口不能代表当前项目正在运行的服务。

internal/ui/preview.go:28 `previewConsolePollInterval = 700 * time.Millisecond`

internal/ui/preview.go:416 `page.OnDidFinishLoad(func() {`

internal/ui/preview_annotate.go:563 `previewAnnotateIntoComposer(s, prompt)`

internal/ui/preview_annotate.go:602 `Label("发送给 Agent").`

### 1.4 改动 UI 重复实现，数据契约也不足以直接接 DiffView

右栏与时间线各维护显示行、折叠和缓存。现有 Diff 只提供 unified patch；editor.DiffState 需要 Old/New 完整 Buffer，不能把 patch 的上下文片段拼成整文件，否则行号、语法和回滚都会出错。整个会话又在自建 worktree 情况下被 UI 改成工作区 diff，使「整个会话」与「未提交」混淆。

internal/ui/diff_rows.go:50 `type diffDisplayRow struct {`

internal/ui/timeline_diff.go:69 `type toolDiffDisplayLine struct {`

internal/ui/client.go:346 `Diff          string`

internal/ui/diff_panel.go:171 `if worktreePath != "" && turn == "" {`

现有整轮撤销会撤销该轮及之后的历史，不是单块回滚。mygo 默认 hunk Revert 只改内存 Buffer，不写工作区。两者都不能直接充当新 hunk 按钮的后端。

internal/ui/timeline_menu.go:50 `if menu.Item("撤销此轮改动…").Danger().Chosen() {`

internal/remote/memory_backend.go:6121 `// ExecuteTurnRevert 执行撤销指定轮次及其之后所有轮次的代码变更，并同步回滚历史。`

### 1.5 一致性与历史

头部为文件 40、改动 40、预览工具条 38 DIP；关闭 tooltip 写死默认键位。正常空态、无工作区、错误、内容不支持属于不同状态，不得全部变成「暂无内容」。删除说明副标题，不删除真实错误原因与恢复动作。

internal/ui/file_manager.go:27 `fileManagerHeaderHeight  float32 = 40  // 列表头与文件头的高度`

internal/ui/diff_panel.go:28 `diffHeaderHeight           float32 = 40`

internal/ui/preview.go:36 `previewToolbarHeight       float32 = 38`

internal/ui/diff_panel.go:481 `Key: "diff-close-btn", Icon: IconX(), Label: "关闭", Tooltip: "关闭改动 (⌘D)",`

有限历史检查：`git log -6 --format='%h %s' -- internal/ui/side_pane.go internal/ui/preview.go internal/ui/file_manager.go internal/ui/diff_panel.go`；另查 side_pane 的 3 条历史与两个提交的目标文件 stat。side_pane 当前追溯到 cdceb3b（2026-11-07 初始提交）；2fbd39d 已对文件面板做公共组件抽离（该文件 13 行新增、98 行删除）；后续 119a0e6 的主题包含加载入口动效收口。由此采用现成 EmptyState、LoadingIndicator 和 motion 预设，不重造外壳。历史只支持以上事实，不将提交标题当作 bug 复现证据。

## 2. 产品决策与目标布局

### 2.1 一个容器，三个目的

```text
聊天头部                                  [终端] [右栏] [更多]
聊天 + composer                │ 文件   改动   浏览器         [关闭]
                               │ 当前标签的工具条
                               │ 当前标签的内容
底部终端仍只属于聊天主区         │
```

- 顶部标签顺序固定：文件 / 改动 / 浏览器。Inspector 只是代码名称；不再加「上下文工作台」或解释副标题。
- 统一头部 40 DIP；每个功能只提供 body 与必要的次级工具条，不再自带总关闭。浏览器内部网页标签与文件内容标签各占自己的次级行，不能与三个目的标签混在同一轴。
- 统一共享宽度：初始 480 DIP，最小 320，最大 800；再受窗口可用空间夹取。保留现有 7 DIP 手柄、方向键调整、拖窄收起与悬停反馈，不另换 Split。
- 窗口不足以同时留出 360 DIP 聊天与 320 DIP 右栏时，右栏临时覆盖聊天内容区。使用 ui.DialogBase 的唯一内层卡片；不覆盖左项目栏，不新增第四种布局状态，空间恢复即回到 split。恢复宽度偏好不被窄窗口覆盖。
- 总开关恢复最后标签；标签点击只选择，不关闭。沿用三个 shortcut id：隐藏时打开对应标签；已打开另一标签时切换；再次按当前标签的快捷键收起整个右栏。
- 新增总开关 action id `toggle-inspector`，首期默认不绑定，允许用户自定义；避免未经确认抢占现有组合。关闭 tooltip 从有效 shortcut binding 生成，无绑定不显示括号。
- 文件 / 浏览器可用于未创建会话页：文件显示「未选择工作区」，浏览器允许用户主动导航；该临时页只存窗口内存，不以空字符串落盘。改动显示「暂无改动」。

### 2.2 明确边界

- **终端不纳入右栏。** 终端是持续执行面，需同时对照文件、网页和改动；继续放聊天底部 dock，保留 toggle-terminal。
- **项目文档、记忆不纳入。** 两者是项目知识管理和审核弹窗，不是当前会话产物。保留原弹窗与快捷键，移到聊天头部「更多」菜单；待审记忆数挂在更多入口 badge 与对应菜单项，不丢信号。
- **看板不纳入。** 保留「任务」窗口，与本计划不合并。头部更多菜单提供「任务」，原 app menu / palette 入口不删除。
- 右栏挂载于聊天主区（包含旧 tasks 回退时背后的聊天）；自动化页、独立任务窗口与设置窗口不挂载，但也不销毁会话缓存。不要把 Inspector 搬到所有根视图外层，避免吞占非聊天页面。
- 文件树和内容解耦，但首期不增加另一条永久竖分栏：窄于 640 DIP 的右栏，树导航与内容通过返回切换并各自保留滚动；达到 640 DIP 且用户主动展开导航时，树与内容共存，导航宽 200 DIP。保留现有双文件拆分在菜单内，不新增第三块内容窗格；宽度不足时只显示活动文件，不丢拆分偏好。

### 2.3 竞品取舍

| 候选 | 采用 | 不采用及原因 |
|---|---|---|
| Cursor Agents Window / Codex / Windsurf | 单右栏、顶部标签、保留上下文 | 不照搬 IDE 活动栏与多 dock |
| Codex | 本轮 / 整个会话 / 未提交，文件已读，hunk 回滚 | 首期不做暂存，避免与现有提交窗口重复 |
| Claude / Conductor | 评论作为附件批量回到对话 | 不让「添加」隐式触发 agent 运行 |
| Zed | 跟随 agent、用户可接管 | 不做 Keep 后把比较基线改成已接受的隐式状态 |
| VS Code / Codex / Claude 浏览器 | 多标签、历史、地址同步、服务发现、截图 | 不做完整 DevTools 替代品 |
| Cursor Design Mode | 元素 selector、note、截图定位 | 不将任意页面指令信任为应用动作 |
| VS Code / Zed / Claude 自由 pane | 不采用 | 聊天产品不需要自由停靠与多 pane 拖拽配置 |

反向验收重点：不能有空白右栏、切换丢页面、Git 与审阅列表不同步、窄栏三窗互挤、无地址的 iframe、重启丢布局。竞品明细来自任务简报，不新增未经核实的产品能力断言。

## 3. 最小目标架构与迁移关系

以下结构为设计草图，标识符可按 Go 风格微调，但不要再加入通用插件系统。

```go
// side_pane.go：注册表只描述目的与入口，不再持有 open、width、mutex。
type sidePane struct {
    id, title, actionID, paletteID string
    render func(*ui.Context, *chatState) // 只画当前目的的内容
}
// id 沿用 file / diff / preview；preview 的显示名称为「浏览器」。
// sidePanes 顺序变为 file / diff / preview。

type inspectorSessionState struct {
    Open bool
    Active string
    Width float32
    Files *fileManagerState
    Review *diffPanelState
    Browser *previewState // P3 内部再改成网页 tabs
}
type inspectorState struct {
    Sessions map[string]*inspectorSessionState
    // 每个窗口独立，UI 线程拥有；后台只能提交带代次的结果。
}
type InspectorPreference struct {
    Open bool `json:"open"`
    Active string `json:"active"`
    Width float32 `json:"width"`
    // 后续只保存相对路径、折叠与滚动锚点；不保存网页内容与日志。
}
```

### 3.1 状态和生命周期

1. 每个 chatState 一份 inspectorState；内部按 threadID 隔离。不同窗口不共享 WebView、编辑器状态和 UI 焦点。空 threadID 用单独临时状态，不进 Sessions 落盘键。
2. Open/Active/Width 只有容器能写。getFileManagerState / getDiffPanelState / getPreviewState 保留为兼容访问器，转向当前会话槽；去掉各自 open/visible/width 真源。旧 isOpen 包装函数根据容器判断，逐步删除直接字段读写。
3. UI 线程拥有容器、页面创建/销毁和 editor 状态。后台结果携带 window、threadID、workspace identity、request/page generation；锁内替换数据，渲染先一次深复制可变切片。不得持锁调用 native API、client 或渲染；不得靠关闭 ReduceMotion 修数据竞争。
4. 标签隐藏/总收起：不销毁数据与 WebView，只不构建对应 ui.WebView，停止活跃轮询与拾取。导航时清除旧页未提交拾取状态，已进 composer 的附件不清除。
5. 会话切换：切走前保存偏好，切入恢复槽。关网页标签、删会话、关窗口才 Destroy、注销事件和取消请求。P0 保留当前窗口访问过的会话资源；P3 对 inactive 会话实施上限，默认至多保留最近 3 个会话的 native 页面组，超限释放最旧组，保留 URL/历史元数据并标记待重载，不能假装 DOM 仍在。
6. 删除会话清理偏好；启动后按存在的会话清理过期记录。滚动不写像素截图，写相对路径/side/行号或稳定列表 key 加 offset。文件读取与 DiffView Buffer 保留现有大小上限与懒加载，不以「保状态」无限驻留全文。

### 3.2 持久化与旧格式

- 在 ui-state.json 增加 `inspector_sessions`：threadID → InspectorPreference。继续使用当前串行、按字段合并的写者，不能单独覆盖整个 JSON。
- 每个会话共用一个 Width；旧 `side_pane_widths` 只读迁移。旧数据没有 Active/Open：取 preview 宽度，其次 file、diff，再取 480；默认 Active=file、Open=false。新偏好优先。夹取 NaN/Inf/负值、未知标签与窗口宽度；有效偏好不因临时夹取而丢失。
- 迁移时保留旧宽度键至少一个兼容周期，防旧版本丢偏好；新写者只更新新会话条目，不通过 nil 把全表抹掉。新增字段必须接入 load/apply/pending merge/save 所有路径，保留项目顺序、last_model、last_swarm 和 drafts。
- P0 保存开关、标签和宽度；P1/P2 添加文件与改动定位锚点。网页 URL、console、截图、DOM 与敏感表单不写 ui-state.json；P3 同进程保留网页滚动与 DOM，跨重启只恢复浏览器目的而不自动联网。

### 3.3 入口迁移表

| 现有位置 | 改法 |
|---|---|
| side_pane.go / sidePanes | 保留 ID 与入口元数据；一个容器统一 split、宽度、开关与 active |
| file_manager / diff_panel / preview 的 toggle | 改成 select-or-toggle 对应目的的兼容包装，不调用 closeOtherSidePanes |
| ui_state.go | 加会话偏好与合并迁移；沿用 WithUIStatePersistence 保护测试和副窗口 |
| chat_header.go | 文件/改动/预览三个按钮合成一个右栏按钮；保留终端；知识与任务进入更多 |
| app_menu.go / palette.go | 仍从同一注册表生成三个目的入口；新增总开关，不重复写名称和绑定 |
| shortcuts.go | 保留 toggle-file-manager、toggle-diff、toggle-preview 与用户 override；新增无默认绑定的总开关 |
| root.go | 聊天分支共用 renderSidePaneSplit，包含旧 tasks 聊天回退；非聊天页面不挂载 |
| model.go / windows.go | 会话选择触发 stash/restore；窗口关闭触发所有会话槽的 dispose，不再只 closePreview 当前槽 |
| timeline / composer / 外部文件入口 | 统一走文件 reveal / review reveal / browser open 方法；不直接改子面板 open |

### 3.4 待发送上下文只有一条通路

复用 composerAttachment、UploadAttachment、draft stash、上传重试与 SendWithAttachments，不新造第二个发送队列。拟新增 `internal/ui/inspector_context.go`，作为薄适配器接收来源与定位元数据；文件引用、评论/console/标注以可读 UTF-8 附件，截图以 PNG 附件进入既有链路。附件标题直接用路径、页面标题或「评论」，不另加说明标签。

- 文件引用携带 threadID、工作区 identity、相对路径和可选行范围；普通拖动只引用，不悄悄上传大文件；显式「附加文件」才读文件并走原限额。
- 评论携带 scope、base/new revision、path、old/new side、1-based 行范围、片段指纹与用户文字；页面标注携带 URL、page generation、selector、rect、评论，可选样式摘要与截图。
- 所有异步添加在发起时绑定 origin threadID；用户切会话后结果进入原会话草稿，不能追加到当前会话。成功入队后才移除原选中项；失败保持原内容。
- 原有正文草稿、附件数量/大小/token 上限、上传失败重试、发送失败保留、排队消息必须保留。不自动 Send，不改变 agent 对话轮次。
- 网页内容按现有随机边界与字段清洗包装为不可信资料，console 和 diff 片段同样不作为可执行指令。附件由 agent 真正接收的路径渲染，不能只在 UI 留一张不可读取的卡片。

internal/ui/composer_attach.go:96 `type composerAttachment struct {`

internal/ui/composer_attach.go:378 `func (s *chatState) addAttachment(name, mime string, data []byte) bool {`

internal/ui/preview_annotate.go:327 `func previewAnnotationsPrompt(items []previewAnnotation) string {`

## 4. 组件与底层 API 核对

pinned 版本以仓库 go.mod 为准，实施不升级 mygo，不增加 cgo。

go.mod:8 `github.com/egoist/mygo v0.8.4-0.20261107102458-435d9411845d`

### 4.1 直接复用的 mygo 与仓库组件

- `github.com/egoist/mygo/ui` 的 Outline / OutlineState：虚拟构建可见行；children 回调仍会访问已展开节点，必须返回缓存，不在渲染回调做目录 I/O；首次展开异步补子目录。
- TabsBase：目的标签、网页标签共用基础键盘与 AX 语义，但样式沿用 mcode tokens。editor.View、markdown.View、ui.Image、现有文件 tab 和 Drag/Drop/DroppedFiles、ContextMenu/PopoverMenu 不重写。
- `github.com/egoist/mygo/ui/editor` 的 DiffView / DiffState：统一/并排、Inline、Annotations、OnGutterButton、RenderHunkActions。DiffLineRange 为从 0 开始且 End 包含；DiffHunk 为半开区间，协议与附件需显式转换并测试。
- DiffState.Old/New 必须是固定 revision 的完整文本，Editable=false。自定义 RenderHunkActions，不调用默认 Revert/Keep 冒充磁盘操作。时间线使用同一适配器、统一布局与受限高度，不让 FitContent 为超大文件创建无界高度。
- `github.com/egoist/mygo` 的 WebView.CapturePage 返回 PNG；Page.GoBack/GoForward/CanGoBack/CanGoForward/URL/OnDidNavigate/OnDidFinishLoad 已存在。PageOptions.PreloadScript 在页面自身脚本前运行；不是名为 Page.PreloadScript 的运行时 setter。
- pinned `ui/webview.go` 明确：本帧不构建元素会隐藏 native view，页面保留。无需新造 Hide API。native 遮挡、动画与焦点仍需 dev 实测，不把源码保证扩大为所有平台实测。
- 只确认 PreloadScript，不假定有 console 推送通道。P3 默认预加载安装、定时只 drain，后续有可靠、隔离的通道再替换；不为去轮询而对任意网站开放 Go 绑定。
- 复用 EmptyState（Description 留空）、LoadingIndicator、IconButton 和 token；菜单/浮层用 ui.PopoverBase / appPopover，确认与覆盖卡片用 ui.DialogBase / modalDragModal。

internal/ui/controls_empty.go:13 `func EmptyState(c *ui.Context, p EmptyStateProps) ui.Element {`

internal/ui/motion_presets.go:88 `func motionSidePanel(c *ui.Context, fromLeft bool) ui.ElementTransition {`

internal/ui/motion_presets.go:100 `func motionCollapse(c *ui.Context) ui.ElementTransition {`

internal/ui/motion_presets.go:108 `func motionListItem(c *ui.Context) ui.ElementTransition {`

internal/ui/motion_presets.go:72 `func motionPopover(c *ui.Context, offsetY float32) ui.ElementTransition {`

internal/ui/motion_presets.go:10 `springSnappy = ui.Animation{Spring: ui.SpringOptions{Duration: DurSheet, Bounce: 0.15}}`

### 4.2 CrychicUI 已查，推荐不新增依赖

核对版本为无 tag 的提交 `a908de9849cb1484c6ed95703ab1c759967a8317`；以下均为只读核对，未引入包：

| 候选包与组件 | 决定与理由 | 源码 |
|---|---|---|
| github.com/ELY-Open-Source/CrychicUI/files.FileCrumbs | 不引入；现有面包屑已满足，重构后以树为主；该组件附带文件夹图标、每级菜单与声音，不能替代树 | https://raw.githubusercontent.com/ELY-Open-Source/CrychicUI/a908de9849cb/files/file_crumbs.go |
| github.com/ELY-Open-Source/CrychicUI/navigation.ClosableTabs | 不引入；按名称和索引拥有标签增删，不适合稳定 tabID 与 native 页面 dispose；使用 TabsBase 与现有 mcode tab 行 | https://raw.githubusercontent.com/ELY-Open-Source/CrychicUI/a908de9849cb/navigation/closable_tabs.go |
| github.com/ELY-Open-Source/CrychicUI/feedback.EmptyState | 不引入；仓库 EmptyState 已有相同能力，避免重复主题和动效层 | https://raw.githubusercontent.com/ELY-Open-Source/CrychicUI/a908de9849cb/feedback/empty_state.go |
| github.com/ELY-Open-Source/CrychicUI/code.DiffView | 不引入；它是 editor.DiffView 的带固定高度、外边框与头部的外壳，会重复 Inspector 头部 | https://raw.githubusercontent.com/ELY-Open-Source/CrychicUI/a908de9849cb/code/diff_view.go |

`sound` 源码默认 `enabled = !testing.Testing()`：https://raw.githubusercontent.com/ELY-Open-Source/CrychicUI/a908de9849cb/sound/sound.go 。未来若改变决定，必须记录包路径和上述完整提交，启动时统一关闭声音或接入提示音配置；必须在 dev 版验证 ReduceMotion；不得使用 overlay 包。当前方案不添加依赖，不另写这些通用组件，只用已安装 mygo 与仓库封装。

### 4.3 Git 与文件访问边界

- GitChangedFiles 已通过 Client 暴露，包含 OrigPath 和单字符状态；P1 不另写 git status 解析器。复杂的 staged/unstaged 明细可以在未来扩展，不在首期增添第三种标记轴。
- NewWatcher 返回 generation，不是文件事件流；代次变化后重新取已加载目录、可见文件和 GitChangedFiles。FSEvents 支持非 Git 普通文件；fallback 遍历工作区，必须放后台、限频、带 deadline，隐藏后停查，不能每帧调用。
- 监听以实际 worktree root 为键，跨窗口共享后端订阅、引用计数；工作区变更时取消旧订阅并换 identity。不要把主项目目录和 agent 自建 worktree 混为一处。
- 所有文件读取继续经 threadWorkspaceRoot / resolveWorkspacePath 与保护规则；浏览器不能用 file:// 绕过 .git、protected、符号链接和路径越界校验。

internal/ui/client.go:1211 `GitChangedFiles(ctx context.Context, threadID string) ([]GitChangedFile, error)`

internal/git/watch.go:18 `Generation(ctx context.Context) (uint64, error)`

internal/git/watch.go:76 `err := filepath.WalkDir(w.root, func(path string, d fs.DirEntry, err error) error {`

internal/git/watch_darwin.go:19 `"github.com/ebitengine/purego"`

internal/remote/files.go:455 `targetFile, err := resolveWorkspacePath(root, p.Path)`

## 5. 分阶段实施步骤

每步只有一名 owner，包含本步涉及的实现、适配和测试，不把同一步拆给两人同时改同一文件。下列测试名为拟新增；写明「更新」的保留既有测试意图并替换旧模型断言。新文件仅为建议落点。每步先写失败测试，再实现最小改动，最后补 dev 验收。所有步骤均禁止全量测试与隐式联网。

### P0：外壳与缺陷修复（5 步）

#### P0.1　浏览器快照和异步代次

**文件/锚点**

internal/ui/preview.go:564 `func renderPreviewPanel(c *ui.Context, s *chatState) {`

internal/ui/preview.go:276 `previewMu.Lock()`

internal/ui/preview_annotate.go:419 `previewMu.Lock()`

**具体改法**：新增 previewSnapshotLocked，复制 annotations、consoleErrors 与所有渲染条件；事件回调使用稳定 tab/page identity，不捕获可变下标。渲染、viewport、抽屉仅读快照。网页创建/导航/重试/释放统一增代；轮询结束前检查 identity 与代次，旧结果不得回写新页。所有 native 调用放锁外，错误路径也要结束 polling 标记；不以锁覆盖整个 render。

**测试**：在 `internal/ui/preview_test.go` 新增 TestPreviewSnapshotConcurrentUpdates、TestPreviewPollDropsStalePage；在 `internal/ui/preview_annotate_test.go` 新增 TestPreviewAnnotationsSnapshotIsolation。测试通过可控页面适配器阻塞回调，不依赖真实 WebView；实施时运行对应 race 子集。

**验收**：并发采集时标签计数与抽屉一致；导航后不显示旧页错误/标注；-race 子集无 preview 字段竞争。

**动效**：数据采集本身不播动画；抽屉增删复用 motionListItem，开合 motionCollapse；ReduceMotion 直接到终点。

**依赖**：无。可与 P0.5 并行。

#### P0.2　单容器与会话内存槽

**文件/锚点**

internal/ui/side_pane.go:52 `var sidePanes []sidePane`

internal/ui/side_pane.go:474 `func renderSidePaneSplit(c *ui.Context, s *chatState, renderChat func()) {`

internal/ui/file_manager.go:99 `func getFileManagerState(s *chatState) *fileManagerState {`

internal/ui/diff_panel.go:82 `func getDiffPanelState(s *chatState) *diffPanelState {`

internal/ui/preview.go:509 `func closePreview(s *chatState) {`

internal/ui/model.go:1290 `func (s *chatState) selectThread(threadID string) {`

internal/ui/windows.go:496 `closePreview(s)`

**具体改法**：按第 3 节实现 Inspector session slots。注册表排序 file/diff/preview；新增统一 header 的 TabsBase 框架和 body 插槽，暂时沿用现有 body 功能。把各 toggle/openExternal/openPreview 入口收口到容器；删除 closeOtherSidePanes 的互斥销毁路径。拆 hide 与 dispose，切标签不构建原 native 元素即可隐藏；窗口释放遍历全部槽。会话切换保留目录、文件 tabs、diff 和浏览器，切 root/worktree 时作废不相容缓存。root 的聊天和旧 tasks 回退共用挂载；加第 2 节的窄窗覆盖规则，保留拖拽手柄算法和键盘能力。

**测试**：更新 `internal/ui/side_pane_test.go` 的 TestSidePaneMutualExclusion、TestActiveSidePanePriority 为新单 Active 模型；新增 TestInspectorTabSwitchPreservesState、TestInspectorThreadIsolation、TestInspectorHideKeepsWebView、TestInspectorWindowDisposeAllSessions、TestInspectorNarrowLayout。更新 `internal/ui/file_manager_test.go` 中原互斥断言与 `internal/ui/preview_test.go` 的 TestPreviewState。native 调用通过薄接口计数，不能只断言 visible=false 就认为未销毁。

**验收**：一次只显示一个目的；文件→浏览器→改动→浏览器仍是同一页；总收起/展开不重载；A/B 会话互不串页；关窗口所有 native 实例恰好释放一次；旧 tasks 回退聊天可用，自动化不受影响。

**动效**：外壳开合 motionSidePanel(c,false)；标签选择用 springSnappy 驱动指示条，禁止对 native 网页做 crossfade；覆盖卡片用 motionSidePanel 并遵守 DialogBase 规则。ReduceMotion 跳到终值且不等待 exit 后才执行必要清理。

**依赖**：P0.1、P0.5 完成后开始；本步同时拥有 side_pane/root/model/windows 与三个子面板适配，不能交给多个并行 owner。

#### P0.3　会话偏好与旧数据迁移

**文件/锚点**

internal/ui/ui_state.go:33 `type UIState struct {`

internal/ui/ui_state.go:708 `if ui.SidePaneWidths == nil {`

internal/ui/ui_state.go:890 `func applyUIState(s *chatState, st UIState) {`

internal/ui/ui_state.go:922 `func persistUIState(s *chatState) {`

internal/ui/side_pane.go:559 `func sidePaneWidthsForPersist(s *chatState) map[string]float32 {`

**具体改法**：增加 inspector_sessions、复制与字段级合并，按第 3.2 节迁移。开关/切标签/拖拽松手触发持久化；会话选择前 stash、选择后 restore，不能每帧读盘。区分 desired width 与窗口内实际 width。保存当前条目时合并其他会话条目，删除会话用显式删除操作，不用空 map 表示删除。维持 WithUIStatePersistence 与串行写者。

**测试**：在 `internal/ui/ui_state_test.go` 新增 TestInspectorPreferencesRoundTrip、TestInspectorLegacyWidthsMigration、TestInspectorPreferenceMergePreservesOtherFields、TestInspectorPreferenceInvalidValues；在 `internal/ui/side_pane_test.go` 新增 TestInspectorThreadPreferenceRestore、TestInspectorTransientThreadNotPersisted。断言原 SidePaneWidths 测试兼容行为；使用 t.TempDir，不碰用户数据。

**验收**：重启恢复各会话 Open/Active/Width；迁移旧文件不自动开浏览器；窄窗口拉回宽屏后宽度不变；模型偏好、项目排序、终端高度不被覆盖；没有空 threadID 键。

**动效**：启动恢复不播入场，用户主动切换才播外壳与标签动效；ReduceMotion 所有恢复直接终值。

**依赖**：P0.2。可与 P0.4 并行，P0.4 不改本步 ui_state/side_pane/model；本步只在 P0.2 预留的容器回调中接持久化。

#### P0.4　统一入口、头部、名称与空态

**文件/锚点**

internal/ui/chat_header.go:2224 `func renderChatHeaderAuxGroup(c *ui.Context, s *chatState) {`

internal/ui/app_menu.go:424 `func sidePaneMenuItems() []*mygo.MenuItem {`

internal/ui/palette.go:70 `func sidePanePaletteItems() []paletteItem {`

internal/ui/shortcuts.go:73 `{id: "toggle-preview", title: "显示/隐藏预览", group: "窗口", keys: "⇧⌘B", mods: ui.Cmd | ui.Shift, key: ui.KeyB, bound: true, customizable: true, registered: true},`

internal/ui/file_manager_tabs_view.go:169 `Tooltip: "关闭文件面板 (⇧⌘F)", Size: ControlXS, Variant: ButtonGhost,`

**具体改法**：新增 `internal/ui/inspector_header.go`（拟新增），接 P0.2 的 header 插槽，统一高度、标签、总关闭与 AX label；删三个 body 的重复总标题/关闭，功能工具条仍在 body 内。chat_header 常驻只留终端/右栏/更多，更多承接文档/记忆/任务与 badge。所有 tooltip 使用有效 binding，不写死默认键；沿用旧 shortcut/palette id，显示名称统一「文件 / 改动 / 浏览器」。加总开关 action 与无默认绑定配置。EmptyState 只给图标和短标题；错误保留短原因+重试，无 helper text；浏览器空页保留可直接输入的地址栏。root 菜单动作分发与 ⌘W 优先关闭文件/网页活动标签的逻辑一起核对，不让菜单只改名称不改 handler。

**测试**：更新 `internal/ui/app_menu_test.go`、`internal/ui/shortcuts_test.go`、`internal/ui/chat_header_test.go` 与三个面板 UI 测试；新增 TestInspectorEntryRouting、TestInspectorHeaderShortcutOverride、TestInspectorHeaderAccessibleTabs、TestInspectorEmptyStatesNoDescriptions、TestInspectorMoreMenuPreservesMemoryBadge。布局测窄头部命中区，不只检查文本。

**验收**：菜单/命令/旧快捷键同一结果；改键后 tooltip 同步；三个目的可键盘左右切换，VoiceOver 可读选中态；没有七按钮组或重复总关闭；记忆待审与任务入口不消失。

**动效**：更多菜单 motionPopover；badge 数字用现有数字动效；头部选择 springSnappy，加载用 LoadingIndicator。ReduceMotion 无弹簧与循环，仍显示静态状态。

**依赖**：P0.2；与 P0.3 按文件所有权并行。子面板旧 header 删除全由本步 owner 完成。

#### P0.5　搜索截断与错误隔离

**文件/锚点**

internal/ui/file_manager.go:232 `func (s *chatState) fileManagerSearch(threadID, query string) {`

internal/app/client.go:528 `func (c *Client) SearchFiles(ctx context.Context, threadID, query string) ([]mcodeui.FileMatch, error) {`

internal/ui/client.go:1149 `SearchFiles(ctx context.Context, threadID, query string) ([]FileMatch, error)`

**具体改法**：保留 SearchFiles 给 composer completion 等旧调用者；为文件面板增加可选搜索能力 SearchFileEntries，返回 FileSearchResult{Entries,Truncated}，app 直接透传既有 wire 字段，旧 SearchFiles 委托它取 Entries。FakeClient 增加对应可控结果；旧 client 无新能力时走旧方法但不谎报截断。拆 directoryErr/searchErr；新查询、清空、成功结果清除本域错误。目录切换、会话切换同时失效 searchGen；结果检查 threadID、目录代次与 query。删除恒 false 分支与误导注释，不按条数推断截断。

**测试**：更新 `internal/ui/file_manager_test.go` 的 TestFileManagerSearch；新增 TestFileManagerSearchTruncation、TestFileManagerSearchErrorDoesNotLeakToDirectory、TestFileManagerSearchSuccessClearsError、TestFileManagerSearchDropsPreviousThread；app 转换层补 TestSearchFileEntriesPreservesTruncation（`internal/app/client_test.go`）。

**验收**：真实截断可见短状态；清搜索后目录不显示搜索错误；旧慢结果不覆盖新会话；composer 文件补全行为保持不变。

**动效**：搜索结果进入 motionListItem（仅可见首批，不对上千项排队）；加载走现有组件；ReduceMotion 无错峰与循环。

**依赖**：无，可与 P0.1 并行；必须先于 P0.2，避免共同改 file_manager.go。

**P0 合入门槛**：以上 5 步完成即可发布；三个功能仍保留旧能力，容器状态与浏览器存活已正确。不得以 P1–P4 尚未实现为由保留旧互斥真源。

### P1：文件（4 步）

#### P1.1　Outline 树与内容导航解耦

**文件/锚点**

internal/ui/file_manager.go:364 `ui.Column(c.Key("file-manager-panel")).`

internal/ui/file_manager_tabs.go:127 `func (s *chatState) openFileManagerFileFromAt(threadID, filePath string, pin, fromList bool, first, last int) {`

internal/ui/file_manager_tabs_view.go:282 `func renderFileManagerPanes(c *ui.Context, s *chatState, snap fileManagerPanelSnapshot) {`

**具体改法**：新增 `internal/ui/file_manager_tree.go`（拟新增），OutlineState[string] 按相对路径稳定 key；异步按目录懒加载，children 只读缓存。目录先于文件，展开/选择/滚动互不重置；搜索是独立结果模式，退出恢复树。按第 2 节实现窄栏返回与宽栏可选导航，不增加永久第三窗格。保留临时/固定 tabs、Markdown/source、行定位、新建/重命名/废纸篓和原保护规则。多选复用 Outline 的 Selection，仅用于引用和拖动；不在首期扩展批量删除。保存每会话展开路径、活动文件和滚动锚点，不落盘文件正文；关闭最后内容标签返回树，不再根据 fromList 关闭总右栏。

**测试**：新增 `internal/ui/file_manager_tree_test.go` 的 TestFileTreeLazyExpansion、TestFileTreeVisibleRowsBounded、TestFileTreeSearchRestoresPosition、TestFileTreeNarrowNavigation；更新 `internal/ui/file_manager_tabs_test.go` 的临时/固定/拆分/关闭末标签测试；增加 TestFileTreeRestoresSessionPosition（`internal/ui/ui_state_test.go`）。

**验收**：1 万个缓存节点时只构建视口行，不在一帧遍历磁盘；展开不会重复请求；返回文件树位置不跳；宽度来回变化不丢活动文件与拆分偏好；现有文件操作可用。

**动效**：树箭头 springSnappy，展开区域 motionCollapse；大树不逐条动画；内容导航只做短场景过渡。ReduceMotion 箭头与布局直接终值。

**依赖**：P0 全部。先串行完成此步再派发 P1.2–P1.4。

#### P1.2　Git 标记、只看改动与监听刷新

**文件/锚点**

internal/ui/client.go:1211 `GitChangedFiles(ctx context.Context, threadID string) ([]GitChangedFile, error)`

internal/git/commit_select.go:20 `func (r *Runner) ChangedFiles(ctx context.Context, root string) ([]ChangedFile, error) {`

internal/git/watch.go:25 `func NewWatcher(root string) Watcher {`

internal/ui/windows.go:513 `fileManagerMu.Lock()`

**具体改法**：新增 `internal/ui/file_manager_watch.go` 与 `internal/app/inspector_watch.go`（均拟新增），以可选 client 订阅能力暴露工作区 generation；后端复用 watcher 生命周期，不在 UI 直接遍历 root。root 变化后刷新已加载目录与打开的文件，后台限并发、合并短时突发；非 Git 目录仍可刷新。GitChangedFiles 结果给树加 M/A/D/R/? 标记（颜色以外有 AX 名称）；只看改动保留必要父目录，删除文件显示可导航的只读占位。fallback watcher 跳过 .git，不能仅凭 generation 判断 index/HEAD 变化；接既有 Git 动作完成通知，并对可见右栏限频检查实际 git dir 的 HEAD/index 元数据，变化时补拉状态。重命名同时处理 OrigPath；变化时只刷新过期 tab，不替换用户选区。隐藏右栏暂停 UI 刷新，重新打开做一次 catch-up；释放订阅与取消所有计时器。

**测试**：新增 `internal/ui/file_manager_watch_test.go` 的 TestFileTreeWatcherRefreshPreservesSelection、TestFileTreeWatcherStopsWhenHidden、TestFileTreeOnlyChangedKeepsParents、TestFileTreeRenameAndDeleteStatus；在 `internal/app/inspector_watch_test.go`（拟新增）补 TestInspectorWatchSharedWorkspace、TestInspectorWatchRootChange、TestInspectorWatchNonGitFiles、TestInspectorWatchIndexOnlyChange；fallback 测试用 fake generation，不做长时间真实轮询。

**验收**：外部编辑、agent 写文件和 Git 命令完成后能刷新；非 Git 普通目录同样刷新；切 worktree 无旧树残留；目录/文件内容/Git 标记同批更新；连续保存不重置滚动；关闭窗口订阅数归零。

**动效**：新增/删除可见行 motionListItem；变更 badge 短颜色反馈，不整树闪烁。ReduceMotion 静态替换，刷新机制不依赖动画帧。

**依赖**：P1.1。可与 P1.3/P1.4 并行；本步不修改 file_manager_tree.go 与 file_manager_tabs.go，通过 P1.1 预留的刷新回调接入。

#### P1.3　共享附件适配器与拖到 composer

**文件/锚点**

internal/ui/file_manager.go:308 `func appendFileReferenceToDraft(s *chatState, relPath string) {`

internal/ui/composer_attach.go:378 `func (s *chatState) addAttachment(name, mime string, data []byte) bool {`

internal/ui/model.go:1353 `d := s.takeDraftLocked(threadID)`

**具体改法**：实现第 3.4 节 `inspector_context.go`（拟新增），接现有 composerAttachment 而非新队列。新增 `internal/ui/file_manager_drag.go`（拟新增），tree 行使用 mygo Drag/Drop 传稳定的工作区相对路径+多选集，composer drop 区区分内部引用和系统 DroppedFiles。内部拖动与显式「添加到对话」进入文件引用附件；系统文件仍走现有 addAttachmentFiles。校验引用路径与来源会话，异步准备时固定目标草稿；需要时在 composer_attach/composer_drafts 中抽最小的按会话添加能力，保持当前上传与重试协议。

**测试**：新增 `internal/ui/inspector_context_test.go` 的 TestInspectorContextAddsWithoutSending、TestInspectorContextStaysInOriginThread、TestInspectorContextFailureKeepsSource、TestInspectorContextRespectsAttachmentLimits；新增 TestFileTreeDragReference、TestFileTreeMultiSelectionDropOrder、TestComposerExternalFileDropUnchanged（`internal/ui/file_manager_drag_test.go`，拟新增）。

**验收**：拖一个或多个文件出现可移除附件，正文不被塞进长字符串；用户发送前 agent 收不到；切会话不会把引用加错草稿；外部文件拖拽上传与失败重试不退化。

**动效**：drop 命中高亮与附件 motionListItem；吸附 springSnappy 不做飞行轨迹。ReduceMotion 直接高亮/新增，保留成功反馈。

**依赖**：P1.1。可与 P1.2/P1.4 并行；只在本步修改 composer 相关文件与 tree 行挂载，不修改 P1.4 的打开文件路由。

#### P1.4　HTML/PDF 到浏览器，SVG 安全预览

**文件/锚点**

internal/ui/file_manager_tabs.go:191 `func (s *chatState) readFileManagerTab(st *fileManagerState, tab *fileManagerTab, threadID, filePath string, gen int) {`

internal/ui/file_manager.go:930 `switch tab.kind {`

internal/remote/files.go:455 `targetFile, err := resolveWorkspacePath(root, p.Path)`

internal/remote/files.go:584 `if bytes.IndexByte(header, 0) != -1 {`

internal/ui/preview.go:479 `func openPreviewHTML(s *chatState, html string) {`

**具体改法**：在文件打开路由区分源码与查看：HTML 默认源码，显式「在浏览器打开」；PDF 点击直接进入浏览器目的，P3 前复用单页面，后续无感迁到网页 tabs。新增 `internal/remote/inspector_files.go` 与 `internal/app/inspector_preview.go`（拟新增），提供专用、短生命周期的 loopback 内容服务；后端每次请求都经现有工作区根、保护规则与路径解析，禁止 file://。仅提供用户选择文件及 HTML 合法相对资源，绑定随机、按会话/工作区撤销的 capability token；校验 Host、路径编码、符号链接、Content-Type、大小、Range，设置 no-store/nosniff，无目录列举。内容服务与 remote 管理服务隔离，页面以 Untrusted=true 创建、无 Go 绑定，不开放 TrustedOrigins="*"；HTML 默认禁脚本，运行脚本需显式信任当前工作区，不能借 localhost 默认信任越权。PDF 使用平台 WebView 原生支持；不支持的平台保留浏览器标签与「系统打开」动作，验收写清平台。SVG 用有大小上限的 mygo SVG 解析/绘制并保留源码切换；解析不支持或含外链则只显示源码，不在特权 WebView 执行。不要改 FilesRead 的 binary 返回为无界 base64。

**测试**：在拟新增 `internal/remote/inspector_files_test.go` 补 TestInspectorPreviewRejectsTraversalSymlinkProtected、TestInspectorPreviewTokenScopeAndRevocation、TestInspectorPreviewPDFRangeAndLimit、TestInspectorPreviewHTMLResourcePolicy；UI 补 TestFilePreviewRoutesPDFAndHTML、TestFilePreviewSVGFallback、TestLocalPreviewUntrustedNoBindings（`internal/ui/file_manager_preview_test.go`，拟新增）。

**验收**：PDF/HTML 在浏览器目的可查看；源码入口仍在；SVG 能预览或明确源码回退；.git、protected、跨工作区、编码越界与失效 token 不可读；会话/窗口释放关闭服务；HTML 不因运行在 loopback 获得应用权限。

**动效**：转入浏览器只播目的标签反馈，加载复用 LoadingIndicator；SVG 不循环缩放。ReduceMotion 无位移与循环。

**依赖**：P1.1、P0.1。可与 P1.2/P1.3 并行；本步拥有 file_manager_tabs.go、文件内容路由、preview 本地内容适配及新增服务文件。

### P2：改动审阅（4 步）

#### P2.1　明确作用域与完整文本快照

**文件/锚点**

internal/ui/diff_panel.go:136 `func (s *chatState) loadDiff(threadID, turnID string) {`

internal/ui/client.go:345 `type ThreadDiff struct {`

internal/remote/memory_backend.go:8880 `func (b *MemoryBackend) GetDiff(ctx context.Context, params wire.ThreadsGetDiffParams) (wire.ThreadsGetDiffResult, error) {`

internal/remote/memory_backend.go:9165 `dt, trunc, dErr := gitRunner.DiffText(ctx, repoRoot, diffFrom, diffTo, diffPaths, maxDiffBytes)`

docs/remote-control.schema.json:7604 `"RemoteThreadsGetDiffParams": {`

**具体改法**：新增 `internal/remote/review.go`、`internal/remote/wire/review_types.go`（拟新增）及 app 的最小 review 适配，旧 Diff 接口保持兼容。作用域固定本轮/整个会话/未提交：本轮是当前运行轮或最近可审轮 start→end（运行中用带版本的当前快照）；整个会话是首个有效 start→最后有效 end；未提交为 HEAD→当前工作树，包含 staged/unstaged 合并后的净变化及未跟踪文件，未出生分支用空树。旧会话缺 checkpoint 明确不可用，不偷换为未提交；自建 worktree 统一由后端解析，不再由 UI 改数据语义。按文件懒取旧/新完整 blob、revision、hash、语言、状态与大小；列表先返回元数据，binary/过大/缺 blob 用显式状态，不伪造完整 Buffer。新增 `threads.review.get` JSON-RPC 扩展，严格参数与 DTO 同步 schema、注册与 capability allowlist，按既有 RegisterMethod 方式；禁止 UI 执行任意 git 命令。

**测试**：新增 `internal/remote/review_test.go` 的 TestReviewScopeTurnSessionUncommitted、TestReviewWorktreeScopeNotOverridden、TestReviewUntrackedAndUnbornBranch、TestReviewSnapshotFullTextAndLimits、TestReviewMissingCheckpointNotWorkingTree；协议补 TestReviewSchemaAndInvalidParams；app 补 TestReviewSnapshotConversion。修正 diff_panel 与 timeline 的 scope 获取测试，旧 Diff 契约仍通过。

**验收**：三个作用域名称与真实数据一致；切作用域不串结果；文件元数据先到，选中后才取全文；binary/截断不会显示成空 diff；checkpoint 不可用有真实原因而非「暂无改动」。

**动效**：作用域选择用 PopoverBase + motionPopover；旧内容保持到新批次就绪并显示忙碌态，不整栏白闪。ReduceMotion 静态忙碌图标，无循环。

**依赖**：P1 完成。此步包含协议、app 与 FakeClient 适配，不能只给 UI 留不存在的方法。

#### P2.2　DiffView 替换并统一时间线

**文件/锚点**

internal/ui/diff_panel.go:242 `func renderDiffPanel(c *ui.Context, s *chatState) {`

internal/ui/diff_rows.go:50 `type diffDisplayRow struct {`

internal/ui/timeline_diff.go:69 `type toolDiffDisplayLine struct {`

internal/ui/timeline_diff.go:755 `func renderInlineToolDiffFileData(c *ui.Context, s *chatState, msgID string, fileIdx int, file git.FileDiff, stats toolDiffFileStats, hunkLines func(hunkIdx int) []toolDiffDisplayLine) {`

**具体改法**：新增 `internal/ui/review_diff_view.go`（拟新增），统一 snapshot→editor.Buffer/DiffState 的适配与缓存 key（workspace/scope/revision/path），只读、语法、Inline 与行号共用主题。右栏先文件列表再单文件审阅，窄栏用返回列表；默认统一视图，宽度足够且用户选择才并排，窄时临时回统一但记偏好。时间线复用同一个渲染适配器与行号坐标，不再独立实现折叠行；小片段不能获得完整历史 blob 时显示有限 patch 摘要与「在改动中打开」，不把摘要传给整文件 DiffState。移除两套死行模型前核对全部调用与 benchmark，保留大文件懒展开和缓存上限；不把所有文件 FitContent 后塞进无界 Scroll。

**测试**：更新 `internal/ui/diff_panel_test.go`、`internal/ui/timeline_diff_test.go`；新增 TestReviewDiffFullLineCoordinates、TestReviewUnifiedSplitSharedState、TestTimelineAndInspectorUseSameRevision、TestReviewBinaryAndLargeFileFallback、TestReviewHiddenStateCacheBounded。更新现有 diff/timeline bench 输入，比较可见节点/分配与固定数据缓存命中，不宣称未经测量的 FPS。

**验收**：同一 revision 在时间线和右栏行号、内容一致；长行与语法高亮可读；统一/并排切换保留定位；滚动一文件不创建全项目 Buffer；原超大/延迟补丁不会卡整栏。

**动效**：列表项 motionListItem，文件展开 motionCollapse；DiffView 内代码滚动跟随系统 ReduceMotion，禁止逐行滑入。ReduceMotion 不播布局插值。

**依赖**：P2.1。删除重复模型及相应测试改写由本步同一 owner 完成。

#### P2.3　安全 hunk 回滚，不改会话历史

**文件/锚点**

internal/ui/timeline_menu.go:54 `s.openRevertPreview(threadID, m.TurnID)`

internal/remote/memory_backend.go:6054 `busyReason := b.revertWorkspaceBusyLocked(threadID, root)`

internal/remote/revert_ext_methods.go:28 `RegisterMethod("threads.revert.preview", handleThreadsRevertPreview)`

internal/git/apply.go:75 `func (r *Runner) ApplyPatch3Way(ctx context.Context, root string, patch []byte) (ApplyResult, error) {`

**具体改法**：新增 `internal/git/revert_hunk.go`、`internal/remote/review_revert.go`、`internal/ui/review_hunk_actions.go`（均拟新增）。自定义 DiffState.RenderHunkActions 请求后端 `threads.review.revertHunk`，不得调用默认 a.Revert 后显示成功；复用已有 busy 检查思想，但不调用 ExecuteTurnRevert。请求只发 scope/revisions/path/hunk identity 与 expected file hash，不信任 UI 提供任意 patch；后端重建/核对选中块，用工作区操作锁、预览与确认、写前重新校验和可恢复快照，只更改相应工作树文本，不改真实 index、不删除消息/检查点。默认拒绝正在运行或共享工作区 busy；历史快照的新侧与当前文件不一致则禁用并提示刷新。首期不支持 binary、rename、整文件增删的 hunk 回滚，保留现有整轮撤销为独立危险菜单并准确说明后果；失败不乐观删除块。现有 ApplyPatch3Way 的隔离索引语义不能直接当成纯工作树回滚，实施应提供专用实现。

**测试**：拟新增 `internal/git/revert_hunk_test.go` 的 TestRevertHunkTouchesOnlySelectedBlock、TestRevertHunkPreservesIndexAndOtherEdits、TestRevertHunkExpectedHashConflict、TestRevertHunkCRLFAndNoFinalNewline；remote 补 TestReviewRevertRejectsBusyStaleAndTraversal、TestReviewRevertDoesNotRewriteHistory；UI 补 TestReviewHunkRevertWaitsForServer、TestReviewHunkRevertFailureKeepsDiff。加 schema/allowlist/invalid params 契约测试。

**验收**：确认后只有选中块回滚，index 和其他块不动；并行 agent/过期文件拒绝；失败保留原内容；A 会话发起的请求不会改 B 会话工作区；回滚成功后树、改动和时间线一起失效刷新。

**动效**：确认用 DialogBase + motionPopover 或现有 modal 预设；服务端成功后块折叠 motionCollapse；请求中静态禁用避免重复。ReduceMotion 直接移除块但不省略确认和成功反馈。

**依赖**：P2.1、P2.2。按顺序实施，避免与 P2.2 共同改 DiffView 接入点；协议变更由本步同步完成。

#### P2.4　行内评论附件与文件已读

**文件/锚点**

internal/ui/composer_attach.go:439 `s.attachments = append(s.attachments, att)`

internal/ui/timeline_diff.go:383 `func toolEditedPaths(m Message) []string {`

internal/ui/ui_state.go:45 `type uiStateFile struct {`

**具体改法**：在 P2.2 共享组件中接 OnGutterButton/LineSelection 与 Annotations，使用 PopoverBase 写评论，按第 3.4 节生成附件；批量「添加到对话」，不自动发言。注解以 old/new side 与 revision 定位；刷新后无法匹配显示过期标记，不把评论挪到相邻行。已读用用户显式勾选/快捷动作，key 由工作区、作用域、路径和内容指纹组成，内容变更自动未读；滚过文件不自动冒充审完。将定位锚点与已读指纹并入既有偏好写者，不保存全文或评论到 ui-state；评论属于现有 draft 附件持久化。新增 `internal/ui/review_comments.go`（拟新增），不制造另一份 Send 实现。

**测试**：新增 `internal/ui/review_comments_test.go` 的 TestReviewCommentOldNewCoordinates、TestReviewCommentsBatchAttachmentNoSend、TestReviewCommentBecomesStaleOnRevisionChange、TestReviewReadMarkerInvalidatesOnContentChange、TestReviewReadMarkerPersistsPerScope；复用 P1.3 的切会话/上传失败/限额测试。

**验收**：选删除行和新增行得到正确定位附件；多评论能一次交给 agent；刷新不偷移评论；已读与验收通过不是同一语义；重启恢复已读与选中文件，agent 再改会变未读。

**动效**：评论浮层 motionPopover，注解展开 motionCollapse，附件 motionListItem，已读勾选短颜色反馈；ReduceMotion 直接终值。

**依赖**：P2.2、P2.3、P1.3。P2 内顺序交付，避免共享 review 组件冲突。

### P3：浏览器（4 步）

#### P3.1　多网页标签、导航历史与地址同步

**文件/锚点**

internal/ui/preview.go:162 `type previewState struct {`

internal/ui/preview.go:401 `wv, err := win.NewWebView(mygo.WebViewOptions{})`

internal/ui/preview.go:444 `func openPreviewURL(s *chatState, raw string) {`

internal/ui/preview.go:741 `target := st.url`

**具体改法**：previewState 内部改成稳定 tabID 的 browserTab 列表（URL/title/address editing buffer、WebView、page generation、历史可用性、viewport、console/annotation 状态与事件 off）；提取到 `internal/ui/preview_tabs.go`（拟新增），原 preview.go 保留兼容入口与渲染调度。TabsBase 不让标签名称充当身份；重复标题/重定向不换 ID。接 Page.OnDidNavigate 与 URL，前进/后退基于 CanGoBack/CanGoForward；用户正在编辑地址时只更新 committed URL，不覆盖未提交输入。用本地 fixture 核对 pinned API 对 pushState/replaceState/hashchange 的通知覆盖；未覆盖时只对当前可见页限频读取 URL 作为兜底，P3.2 再接预加载脚本的 location 序号，不能把普通导航测试当作 SPA 已通过。刷新、停止、系统打开、⌘W 与关闭末网页标签的行为一致，末标签回到地址空态而非关应用。本地 HTML/PDF 路由接入同一 tab 生命周期。按第 3.1 节加入 inactive 会话资源上限，当前会话切目的不淘汰。解绑所有回调，重启不开旧远程网站。

**测试**：新增 `internal/ui/preview_tabs_test.go` 的 TestBrowserTabsStableIdentityAndClose、TestBrowserNavigationSyncPreservesEditingAddress、TestBrowserBackForwardUsesPageHistory、TestBrowserHiddenTabRetainsPage、TestBrowserSessionEvictionReleasesViews；更新 `internal/ui/preview_test.go` 的 URL、state、关闭断言。

**验收**：开两个网页切换无重载，回退可用且地址正确；页面内跳转/重定向更新地址；编辑地址不被后台导航覆盖；隐藏 native 不拦截聊天点击与键盘；关闭标签与窗口无孤儿回调。

**动效**：网页标签增删 motionListItem、选中 springSnappy；不动画网页内容，不缩放 native 页面。ReduceMotion 标签变化直接终值。

**依赖**：P2 完成与 P1.4。先完成此步再派发 P3.2–P3.4。

#### P3.2　PreloadScript 与完整 console

**文件/锚点**

internal/ui/preview.go:112 ``const previewConsoleScript = `(function () {``

internal/ui/preview.go:269 `if raw, err := web.Page().Eval(previewConsoleScript); err == nil {`

internal/ui/preview_annotate.go:411 `_, _ = web.Page().Eval(previewAnnotateStartScript)`

**具体改法**：将 collector/picker 迁到 `internal/ui/preview_scripts.go`（拟新增），创建 WebView 时通过 PageOptions.PreloadScript 在每次新文档预装。捕获 console.log/info/debug/warn/error、error、unhandledrejection，记录时间、级别、来源、tab/page/sequence，保留原 console 方法与 this。安全序列化循环对象和异常 getter，页面端 ring buffer 限条数与单条字节，UI 只 drain 新序号，不反复 Eval 安装整套脚本；未启用采集时不注入，切配置受控重建该页并提示，不盲目每帧重试。隐藏页保留 bounded buffer，停止轮询；恢复时拉增量。清空按 sequence 水位，不让旧异步回包带回历史。console 过滤/复制/勾选「添加到对话」走 P1.3；完整指日志级别，不承诺完整 Network 面板。HTTP 请求摘要可后续用 fetch/XHR/resource timing 补，不伪装抓到 WebSocket/所有跨源请求；本阶段不新增 Go 绑定权限。

**测试**：新增 `internal/ui/preview_console_test.go` 的 TestBrowserPreloadInstalledBeforeNavigation、TestBrowserConsoleAllLevelsAndBounds、TestBrowserConsoleClearRejectsOldDrain、TestBrowserConsoleCaptureOffNoInjection、TestBrowserConsoleAttachmentSanitizesUntrustedContent；脚本夹具覆盖 circular object/getter/大日志，不引入联网 JS 测试依赖。dev 本地 fixture 验证页面首个同步 console.log 能捕获。

**验收**：加载早期日志可见，各级过滤正确；导航不串页；大量 console 不无界涨内存；console 添加仅产生附件；采集关闭不残留后台轮询。

**动效**：抽屉 motionCollapse、错误数量短数字反馈；高速日志追加不逐条弹入。ReduceMotion 静态计数与条目，无加载循环。

**依赖**：P3.1。与 P3.3/P3.4 仅在完成 collector/导航回调接口预留后并行；本步只拥有 scripts/console adapter 与预加载配置接入，不改网页 tab 工具条。

#### P3.3　截图与元素标注统一为附件

**文件/锚点**

internal/ui/preview_annotate.go:550 `func previewSendAnnotations(s *chatState, st *previewState) {`

internal/ui/preview_annotate.go:601 `sendBtn := ui.ButtonBase(c.Key("preview-annotate-send")).`

internal/ui/preview_annotate.go:303 `func previewSanitizeField(raw string) string {`

**具体改法**：新增 `internal/ui/preview_capture.go`（拟新增），CapturePage 取得 PNG 后复用 addAttachment，限制像素/字节，带 origin thread/tab/page identity；截图中途导航则拒绝或明确旧页时间，不附到新页面。默认只截用户当前可见网页，失败保留动作与错误，不拿整桌面替代。标注按钮文案统一「添加到对话」，调用 P1.3 的 context 适配而不是追加正文；批量标注转带 selector/URL/rect/评论的可读附件，可选截图。主按钮添加现有选择，旁边 PopoverBase 菜单选择「截图」「选择元素」，不是自动发送拆分按钮。保留随机不可信边界与清洗，移除成功前清空逻辑；仅本步修改 preview_annotate 的附件转换与工具条，P3.2 对脚本的修改在合入前完成或由本步调用已预留函数，不两人同时改该文件。

**测试**：新增 `internal/ui/preview_capture_test.go` 的 TestBrowserScreenshotAddsPNGAttachment、TestBrowserScreenshotRejectsStalePageAndOversize、TestBrowserCaptureFailureKeepsSelection；更新 `internal/ui/preview_annotate_test.go`，新增 TestBrowserAnnotationAddsAttachmentNotDraftOrSend、TestBrowserAnnotationFailureKeepsItems、TestBrowserAnnotationBoundarySanitization；断言原 draft 文本保持不变。

**验收**：截图/标注可预览、删除、重试，agent 在用户发送后能读取；添加动作不触发新轮；切会话不串附件；标注导航后不会引用错 selector。

**动效**：选取高亮短颜色过渡、菜单 motionPopover、附件 motionListItem；截图完成给一次状态反馈，不闪白屏。ReduceMotion 关闭高亮插值和位移，反馈仍可见。

**依赖**：P3.1、P1.3；脚本迁移接入与 P3.2 交界必须先定 owner，默认 P3.2 完成后实施此步，避免 preview_annotate.go 冲突。

#### P3.4　开发服务器自动发现

**文件/锚点**

internal/ui/preview.go:368 `func getBrowserQuickPorts(s *chatState) []string {`

internal/ui/terminal.go:604 `func attachTerminalListener(s *chatState, threadID string, tab *terminalTab, term *terminal.Terminal) <-chan struct{} {`

internal/ui/model.go:1573 `func (s *chatState) onWatchUpdate(update ThreadUpdate) {`

**具体改法**：新增 `internal/ui/preview_servers.go`（拟新增），只读当前会话新增工具输出与终端输出的有界尾部，解析明确 localhost/127.0.0.1/[::1] HTTP(S) 地址；复用现有 quick ports 作为候选补充，不做系统全端口扫描。终端现入口只是退出监听，需先核对终端组件是否有无副作用输出订阅；没有时先从已有可读 scrollback 尾部限频采样，不虚构订阅 API。仅对有证据候选做 loopback、短 deadline、低并发健康探测；0.0.0.0 转本机，禁止跟随重定向访问任意内网。记录 thread/workspace identity、last seen 与进程退出/不可达状态，去重后在地址栏下拉显示；发现只加 badge，不抢开页面，不把其他会话服务混进来。用户配置端口仍可手动打开；关闭面板/切会话停止采样和探测。

**测试**：新增 `internal/ui/preview_servers_test.go` 的 TestBrowserServerDiscoveryFromToolOutput、TestBrowserServerDiscoveryLoopbackOnly、TestBrowserServerDiscoveryDedupAndExpiry、TestBrowserServerDiscoveryDoesNotAutoNavigate、TestBrowserServerDiscoveryStopsOnThreadChange；用 httptest 本地服务和 fake terminal tail，不执行真实 dev server 或扫描网络。

**验收**：当前会话运行 dev server 后下拉出现可打开地址；没有 URL 证据时不把所有常见端口称为服务；服务退出/端口变更不保留错误可用状态；发现不会改变当前标签或焦点。

**动效**：候选项 motionListItem，入口 badge 短数字反馈；不常驻雷达扫描动画。ReduceMotion 直接出现候选与 badge。

**依赖**：P3.1；可与 P3.2 并行，本步只改服务器发现新文件、terminal 输出适配与 model 的独立消息观察 hook，不改 preview_tabs/preview_annotate。

### P4：agent 联动（2 步）

#### P4.1　默认自动跟随，但用户接管后只给 badge

**文件/锚点**

internal/ui/model.go:1573 `func (s *chatState) onWatchUpdate(update ThreadUpdate) {`

internal/ui/client.go:234 `ToolName        string // 工具名称（如 bash、edit_file 等）`

internal/ui/timeline_rows.go:5161 `func toolCallPath(m Message) string {`

internal/ui/timeline_diff.go:383 `func toolEditedPaths(m Message) []string {`

internal/ui/file_manager_tabs.go:117 `func (s *chatState) openFileManagerFileExternalAt(threadID, filePath string, first, last int) {`

**具体改法**：新增 `internal/ui/inspector_follow.go`（拟新增），只订阅当前会话的结构化工具事件，复用现有工具名/路径提取与 reveal 能力；读文件定位到文件，成功写文件刷新并定位改动。事件去重到 messageID/status/revision，限制频率并合并多文件批次。默认 follow=true 但不自动打开已收起的右栏、不切目的、不抢 composer/native 输入焦点；仅当前目的且未被用户接管时更新内容位置。用户滚动/选文件/选作用域即暂停跟随，后续变化仅给目的 badge；点击 badge/跟随按钮恢复并跳最新。手动打开聊天中的文件卡片属于用户主动 reveal，可打开并切文件目的。不执行从工具文本抽出的指令；截断/不合法/越界路径只显示活动提示，不猜路径。

**测试**：新增 `internal/ui/inspector_follow_test.go` 的 TestInspectorFollowRevealsAgentFileWithoutFocusSteal、TestInspectorFollowPausesOnUserNavigation、TestInspectorFollowBadgeResumesAtLatest、TestInspectorFollowDoesNotOpenHiddenPane、TestInspectorFollowDeduplicatesToolUpdates、TestInspectorFollowRejectsStaleThreadAndUnsafePath。

**验收**：可看到 agent 正在读/改的文件位置；用户审旧文件不会被抢走；收起右栏不被 agent 打开；点击 badge 才跳转；A/B 会话活动完全隔离。

**动效**：定位用受限滚动过渡，选中高亮短淡入，badge 用已有数字动效；ReduceMotion 定位直接滚到锚点，无脉冲与循环。

**依赖**：P1.1、P1.2、P2.2、P3 完成。model 与 timeline 提取调用由本步单 owner 接入，避免并行重复事件解析。

#### P4.2　只读状态快照先落地，MCP 桥接单独设门槛

**文件/锚点**

internal/ui/client.go:1055 `Watch(threadID string, onUpdate func(ThreadUpdate), onApprovals func([]Approval)) (stop func())`

internal/mcp/registry.go:10 `type ToolContext struct {`

internal/mcp/registry.go:64 `r.registerMemoryTools(s)`

docs/remote-control.schema.json:4 `"description": "JSON Schema Draft 2020-12 specification for mcode Remote Control Phase 1 JSON-RPC 2.0 communication, session lifecycle, and kanban DTOs.",`

**具体改法**：新增 `internal/ui/inspector_snapshot.go`（拟新增），从容器发布不可变、有限大小、带 timestamp/generation 的 InspectorSnapshot：threadID、windowID、Open、Active、实际工作区 identity、当前相对路径/行范围、review scope/revisions、follow/paused、待处理 badge；URL 默认仅 origin 或经用户允许的脱敏 path。不得包含 draft、评论正文、console 正文、页面 DOM、cookies、截图、地址 query/hash/token。供本地 AX 测试和以后桥接消费；读快照无副作用、不加载网页、不获取焦点。

**AX 接口门槛与结论**：已确认现有 Client.Watch 是后端→UI，MCP Registry 没有现成的右栏服务注册；本文未确认 UI→MCP 的已认证反向桥，不直接承诺工具可用。推荐 P4 首期只发布进程内只读 snapshot，不新增 MCP 工具。若主代理在实施前确认现有宿主可注入只读 provider，再追加独立任务：定义 `inspector.getState` JSON-RPC schema、host provider、按调用 thread/project/window 校验、无窗口返回明确 unavailable，再注册只读 MCP 工具（名称候选 `inspector_read_state`）。不得让 MCP import UI，不写临时 JSON 文件冒充实时状态，不在本步骤绕过授权。多窗口不猜当前焦点；请求无唯一 window 时返回候选元数据或明确 ambiguity。

**测试**：新增 `internal/ui/inspector_snapshot_test.go` 的 TestInspectorSnapshotImmutableAndBounded、TestInspectorSnapshotRedactsSensitiveFields、TestInspectorSnapshotReadHasNoUIEffects、TestInspectorSnapshotWindowAndThreadIsolation。若满足桥接门槛，另补 TestInspectorReadStateCallerScope、TestInspectorReadStateUnavailableWithoutWindow、TestInspectorReadStateSchema，未补齐不得对 agent 宣称工具存在。

**验收**：AX 可检查当前目的/定位而不读隐私内容；关窗口快照失效；不启动 UI 的 MCP 不依赖 UI 包或报 panic。工具桥未做时在发布说明明确「仅本地快照，无 agent 工具」。

**动效**：只读快照无动效、不产生新动画帧；ReduceMotion 无额外分支。

**依赖**：P4.1；只读工具桥另立小任务，不阻塞 P4.1 与本步骤的本地快照交付。

## 6. 依赖图、并行与独立交付

| 阶段 | 步骤数 | 必须顺序 | 可并行与文件所有权 |
|---|---:|---|---|
| P0 | 5 | P0.1/P0.5 → P0.2 → P0.3/P0.4 | 1 与 5 并行；3 与 4 并行但 4 不改 side_pane/ui_state/model |
| P1 | 4 | P0 → P1.1 → P1.2/P1.3/P1.4 | 2 拥有 watcher/app 订阅；3 拥有 composer/drag/tree 行；4 拥有文件打开路由/本地预览服务 |
| P2 | 4 | P1 → P2.1 → P2.2 → P2.3 → P2.4 | 默认串行，避免 review、schema 与 timeline 交叉改动 |
| P3 | 4 | P2 → P3.1 → P3.2 → P3.3；P3.1 → P3.4 | 2 与 4 可并行；3 等 2 的脚本接口落定；共享 preview.go 接线统一由后开始的 owner 完成 |
| P4 | 2 | P3 → P4.1 → P4.2 | 默认串行；MCP 桥接不包含在确定交付内 |

共 19 步。阶段门槛是保留旧功能、完成本阶段测试与 dev 验收，不依赖下一阶段才让入口可用。每步单独保持可编译、不可用能力隐藏或给真实失败动作；不留下永远 disabled 的假功能。阶段合入前移除本阶段旧真源/重复渲染，不长期双写 open/visible/width。跨步涉及同一文件按依赖串行，不能仅因章节不同就分派并行修改。

## 7. 验证计划与验收方式

本文只做静态检查。以下为执行档命令模板，未运行；GOPROXY=off 保证不拉网络，CGO_ENABLED=0 保证不引入 cgo。环境缺依赖立即停止，不为测试修改任务范围外文件。

```sh
GOPROXY=off CGO_ENABLED=0 go test ./internal/ui -run 'TestInspector|TestFileManagerSearch|TestFileTree' -timeout 120s
GOPROXY=off CGO_ENABLED=0 go test -race ./internal/ui -run 'TestPreviewSnapshotConcurrentUpdates|TestPreviewPollDropsStalePage|TestPreviewAnnotationsSnapshotIsolation' -timeout 120s
GOPROXY=off CGO_ENABLED=0 go test ./internal/remote -run 'TestReview|TestInspectorPreview' -timeout 120s
GOPROXY=off CGO_ENABLED=0 go test ./internal/git -run 'TestRevertHunk' -timeout 120s
GOPROXY=off CGO_ENABLED=0 go test ./internal/app -run 'TestSearchFileEntries|TestInspectorWatch|TestReviewSnapshot' -timeout 120s
GOPROXY=off CGO_ENABLED=0 go test ./internal/ui -run 'TestReview|TestTimelineAndInspector|TestBrowser' -timeout 120s
```

**race 限制**：部分 Go 平台的 race runtime 要求 cgo。上面的 race 命令若在纯 Go 配置下不受支持，记录为未验证，不开启 CGO 规避项目约束；改用确定性快照/代次并发测试，并由主代理选择项目既有的受支持 race 环境执行。不得将普通 go test 通过写成无竞争的证明。

实施方用有超时的命令执行器包住上述命令，go test 的 -timeout 仅限制测试进程，不代替构建阶段的外部 deadline。测试文件与符号由步骤列出；schema、allowlist 和 adapter 测试归引入该协议的同一步 owner。全量检查由主代理执行，本计划不要求子代理启动 mcode。

实施后的 dev 验收由主代理/执行档在受控环境完成，必须记录平台与状态：

1. 宽/窄窗口、浅/深主题、ReduceMotion 开/关，键盘/VoiceOver 可走完整流程。
2. 连续切三个目的、开关右栏、切 A/B 会话、窗口关闭；native 页不遮挡隐藏后的聊天，不残留资源。
3. 临时目录含大树、非 Git、symlink、受保护路径、Unicode/空格名、PDF、SVG、HTML 相对资源。
4. 审阅覆盖新建/删除/重命名、binary、CRLF、缺末尾换行、大文件、旧 checkpoint、未跟踪、已暂存+未暂存。
5. 回滚前外部再改/agent 仍运行必须拒绝，不丢本地编辑，不改变 index 或会话历史。
6. 本地 fixture 首屏 console、重定向、SPA 地址变化、页面表单/滚动、多网页重复标题；截图与标注需用户发送。
7. 重启恢复会话布局，不自动联网打开网页，不将 console/DOM/draft 写入 ui-state。

## 8. 明确不做

- 不做多 dock、自由停靠、任意 pane 拖拽布局或布局编辑器。
- 不做完整 IDE 编辑、LSP、断点调试、工作区搜索替换；继续使用只读查看，编辑交既有编辑器入口。
- 不做第二套文件上传、发送队列或 composer；不自动发送评论、console、截图和标注。
- 不做首期 hunk 暂存/提交、binary hunk 回滚、rename/整文件增删回滚；整轮撤销保留独立危险语义。
- 不做完整 Network/DevTools、浏览器同步/密码管理/下载中心，不保证所有平台内嵌 PDF 能力。
- 不把项目知识、记忆管理、任务和终端吞进右栏；不改变这些领域的生命周期。
- 不扫描全端口，不让任意网页访问应用 Go 绑定，不把 file:// 作为本地预览捷径。
- 不升级 mygo，不引入 cgo，不为装饰外壳引入 CrychicUI；不承诺未经核实的 MCP 反向桥。

## 9. 风险与控制

| 风险 | 控制与发布门槛 |
|---|---|
| native WebView 与 UI 过渡/遮挡/焦点不同步 | 使用不构建即隐藏的现成机制；不 crossfade 网页；dev 分平台验收点击穿透、弹窗遮挡和回收 |
| 按会话保存后资源数量上升 | Buffer 懒加载、现有大小上限；inactive 页面组 LRU；窗口/会话释放；测试 native count、订阅数与取消 |
| fallback watcher 遍历大目录 | 后台 deadline、代次合并、限频、隐藏停查；只刷新缓存目录；不宣称 generation 是文件事件流 |
| 旧 checkpoint 不完整，patch 无法还原整文件 | 新快照契约，固定 revision；缺正文显示摘要并允许右栏定位；不拼造 Old/New |
| hunk 回滚覆盖外部编辑 | busy 检查、固定 scope、hash、工作区锁、写前再检与可恢复快照；外部进程不服从应用锁，仍有检查到替换的并发窗口，验收不能声称绝对无竞态；冲突默认拒绝，不自动三方解决 |
| 本地 HTML/PDF 服务扩大可读文件面 | 独立 loopback/token、protected/path 校验、相对资源白名单与上限、Untrusted、无 Go 绑定、销毁即撤销 token；安全测试未通过不开放入口 |
| 新协议与老 Client 不匹配 | 可选能力接口、旧 ID 与旧 Diff 兼容；新 JSON-RPC 同步 schema/allowlist/DTO/测试；无能力时不显示伪按钮 |
| 日志、地址、截图泄露隐私或注入提示 | 日志有界且主动添加；URL 脱敏；不自动截图/发送；沿用随机不可信边界；快照不含正文与 token |
| 多 owner 同改大文件 | 按第 6 节串行共享文件；每步单 owner 包含适配与测试；先提取必要小文件，不做顺手大清理 |

## 10. 待用户拍板（4 项，均有默认推荐）

1. **总开关默认键位**：推荐首期无默认绑定、支持自定义，保留三个既有键；备选增加新默认组合，但要先全表查冲突。该决定不阻塞容器和原快捷键。
2. **窄窗口行为**：推荐临时覆盖聊天内容区，保留右栏宽度偏好；备选只允许更宽窗口打开，会增加限制提示与失败点击。
3. **自动跟随默认状态**：推荐默认开启但只作用于已展开、当前目的、未接管内容，用户交互后暂停并给 badge；备选默认关闭，避免任何自动定位，但降低即时可见性。
4. **本地 HTML 脚本信任**：推荐默认静态、安全查看，首次执行脚本需信任当前工作区；备选仅通过用户启动的 dev server 打开可执行页面，安全面更小但本地文件体验受限。禁止无提示信任所有 loopback 页面。

未拍板时按推荐值实施规划，不扩展配置面。MCP 反向桥是技术门槛，不作为第五个产品选项；未确认可行前只承诺本地只读快照。
