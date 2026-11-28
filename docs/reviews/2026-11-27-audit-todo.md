# 2026-11-27 全仓审查剩余项 todo

来源：会话「App代码分批分模块详细审查」12 份 bee 报告（`~/.mcode/mcode.db` 的 `swarm_reports`），2026-11-27 按 HEAD 复核。已修与已决定不修的条目不列。

## 第 1 轮
- [x] UI 丢弃块/选中行/单文件撤销加确认框（git_pane_diff.go、review_cards.go）
- [x] UI 发送失败恢复 @thread 引用、resync 旧响应不覆盖、输入法组字时 Enter/Esc 不触发（model.go、composer.go、chat_header.go、chat_find.go）
- [x] store 写事务改 IMMEDIATE、.needs-check 0600；config 全部档位配 write_paths 时 DefaultTier 不被清空；config.json.bak 原子写；app 启动失败先停后台再关库
- [x] remote 重命名/删除符号链接只动链接本身、配对设备名按字符截断、送达失败不落盘「始终允许」

## 第 2 轮
- [x] mcp/agentconf：SaveMCPServer 保留 SecretEnvKeys、tools_kanban taskBlocked 错误（插件路径逃逸、ScanMCP 吞错、ServeThread WorkDir 未修）
- [x] kanban：规划落地失败标 plan_failed、worktree setup 协程纳入 Close 等待、清理失败记日志
- [x] kanban：回执与 pendingDone 持久化到任务 Meta（asks/blocked 未持久化）
- [ ] kanban：CheckWaits/WakeParents 加锁
- [x] providers：codex kill/close 竞态、claude approval_mcp 协程泄漏、acp probe/launch 失败分支关管道
- [x] web DiffViewer 请求竞态、RemoteRequestPanel 监听重绑、可点击 div 改 button、MarkdownView `<a>` 不放行 file:（swarm ClearFinished 未修：worktree 已删，单加 running 判断不够）

## 第 3 轮
- [ ] UI 渲染期无锁读写（composer_quick_replies、automation_shell、kanban_modal、session_stats_popover、steward_model_picker、swarm.go、sidebar.go bulkPopoverProjectID、settings_backup backupUI、composer_pr_bar s.closed）
- [ ] UI 设置保存失败不清草稿/回滚（settings_page_open_in、settings_page_swarm、settings_swarm_tiers、settings_page_mcp 工具策略）
- [x] UI 渲染期 config.Load/os.Stat 移出、tool diffstat 缓存与展开截断（每帧排序缓存未做）

## 暂不修
- web 令牌存 localStorage：CSP 严格、无 rehype-raw，改存储会破坏 PWA 与 SW 内联作答
- 死代码、重复 helper、理论触发项（crypto/rand 失败、Windows 专属降级路径等）
