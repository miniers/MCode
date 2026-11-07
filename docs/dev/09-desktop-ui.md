# 09 桌面 UI

代码在 `internal/ui`，包名 `mcodeui`。框架是 MyGo，立即模式：`view := func(c *ui.Context)` 每帧描述界面。

## 基础结构

| 文件 | 内容 |
|---|---|
| `root.go` | `buildRoot`，整体布局和侧栏过渡 |
| `windows.go` | `OpenThreadWindow`、`OpenNewThreadWindow`、`ActivateThread`、`OpenMiniEditor` |
| `client.go` | `Client` 接口和 UI 数据类型；可选接口 `AttentionClient`、`StewardClient`、`StewardTopicsClient`、`ForegroundThreadReporter` |
| `model.go` | `chatState`（`s.mu` 保护）、`UpsertMessages` |
| `theme.go`、`motion.go`、`text_scale.go` | 主题、动效 token、字号缩放 `scaledFont` |
| `ui_state.go` | `ui-state.json`：侧栏宽度（200 到 420pt）、隐藏状态、终端高度、各分栏宽度 |

`Client` 由 `internal/app/client.go` 实现（200 多个方法）。UI 不直接接触 `store` 和 `wire`。新增后端能力时，通常要在 `Client` 加方法、在 `internal/app` 实现，并在 `internal/app/convert.go` 这类文件里转换类型。

## 聊天区

| 文件 | 内容 |
|---|---|
| `chat.go`、`chat_header*.go` | 聊天主体和顶部 |
| `chat_anchor.go` | 发送后的滚动锚定 |
| `chat_find.go` | 会话内查找（⌘F） |
| `timeline_rows.go` | 时间线行渲染，整个包最大的文件 |
| `timeline_incremental.go` | 增量构建。`chatState` 用 `messagesRev` 和指纹判断要重建哪些部分 |
| `timeline_minimap.go`、`timeline_diff*.go`、`timeline_banner*.go`、`timeline_error*.go`、`timeline_menu.go` | 缩略图、差异、横幅、错误、菜单 |
| `content_width.go` | 内容宽度 |

### 性能约定

时间线每帧都要跑。`chatState` 里有一批按 `messagesRev` 失效的缓存（如 `maxTurnValue`、`lastAsstByTurn`），避免每帧重扫消息。新增每帧逻辑时，遇到要扫全部消息的计算，按同样方式缓存。

## Composer

| 文件 | 内容 |
|---|---|
| `composer.go` | 主体 |
| `composer_attach.go`、`composer_complete.go`、`composer_drafts.go`、`composer_queue.go` | 附件、补全、草稿、排队 |
| `composer_usage_panel.go`、`composer_mcp_menu.go`、`composer_skills.go`、`composer_plan_pill.go` | 用量面板、MCP 菜单、技能、Plan 标记 |
| `model_picker.go`、`effort_slider.go` | 模型选择和推理强度 |
| `approval.go`、`plan_reader.go` | 审批卡片和计划阅读 |

## 侧栏

`sidebar*.go`：`sidebar_bees.go`（子代理）、`sidebar_steward.go`（总管）、`sidebar_bulk.go`（批量）、`sidebar_activity.go`（动态，⌥⌘U）、`sidebar_footer.go`、`sidebar_hover_card.go`。

## 右侧面板

`side_pane.go` 管理右侧分栏，各面板互斥。

| 面板 | 文件 | 快捷键 |
|---|---|---|
| 改动 | `diff_panel.go` | ⌘D |
| 文件 | `file_manager.go` | ⇧⌘F |
| 预览 | `preview*.go` | ⇧⌘B |
| 终端 | `terminal*.go` | ⌘J |
| 项目文档 | `context_panel*.go` | ⇧⌘K |
| 记忆 | `memory_panel*.go` | |
| Git | `git_*_ui.go`、`github_clone.go` | |

## 其他功能区

| 功能 | 文件 |
|---|---|
| 编排面板 | `swarm*.go` |
| 看板 | `kanban*.go`、`tasks_window.go` |
| 自动化 | `automation*.go` |
| 总管和收件箱 | `steward_*.go`、`sidebar_steward.go`、`attention_inbox.go` |
| 命令面板（⌘K） | `palette.go`、`palette_message_search.go` |
| 快捷键 | `shortcuts.go`（`shortcutDefaults`，可在设置覆盖） |
| 应用菜单 | `app_menu.go` |
| 托盘额度 | `tray_quota_panel.go` |
| 会话导出和 `@` 引用 | `thread_export.go`、`thread_mention.go` |

## 设置页

`settings_pages.go` 定义页面，`settings_index.go` 定义搜索索引。每页一个文件 `settings_page_<name>.go`：`about`、`appearance`、`archive`、`conversation`、`general`、`mcp`、`models`、`nowledge`、`open_in`、`providers_agents`、`quick_replies`、`remote`、`skills`、`source_control`、`steward`、`swarm`、`workbench`。

新增设置项时要同时加：配置字段（`internal/config`）、页面控件、`settings_index.go` 里的条目。如果 Web 也要显示，再改 `web/src/components/SettingsForm.tsx` 和 `web/src/components/settings/settingsIndex.ts`。

## 快捷键

| 快捷键 | 功能 | 快捷键 | 功能 |
|---|---|---|---|
| ⌘N | 新建会话 | ⌥⌘N | 用 worktree 新建 |
| ⌘. | 停止 | ⇧⌘P | Plan 模式 |
| ⌥⌘↑ / ⌥⌘↓ | 切换会话 | ⇧⌘T | 交给总管 |
| ⇧⌘D | 转发 | ⌥⌘R | 脚本 |
| ⇧⌘S | 休眠 | ⌃⌘A | 归档 |
| ⌘F | 查找 | ⌘B | 侧栏 |
| ⌘K | 命令面板 | ⌘, | 设置 |
| ⌘D | 改动 | ⌘J | 终端 |
| ⇧⌘B | 预览 | ⇧⌘F | 文件 |
| ⇧⌘E | 编辑器 | ⌥⌘U | 动态 |
| ⇧⌘O | 添加项目 | ⇧⌘K | 项目文档 |

## 必须遵守的 UI 规则

由守卫测试拦截：

- 弹窗和浮层：自绘卡片的弹窗用 `ui.DialogBase`（或 `modalDragModal`），浮层用 `ui.PopoverBase`（或 `appPopover`）。面板只设尺寸上限、不设外观。禁止使用 `ui.Modal`、`ui.Popover`，否则卡片外会多一圈黑边（`modal_style_guard_test.go`）。
- 字号：聊天区字号必须走 `scaledFont`（`text_scale_guard_test.go`）。

其他约定：

- 动效优先用 `motion.go` 的 token。
- 所有 `chatState` 的读写持有 `s.mu`。
- 实机 UI 验证由用户操作。开发时把操作步骤写清楚，交给用户验证。
