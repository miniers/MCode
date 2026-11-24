# MCode 技术参考

本文记录 MCode 的术语、子系统规格、目录结构与开发发布流程。产品概览见 [README.md](../README.md)。

## 术语

| 术语 | 含义 | 代码名 |
| :--- | :--- | :--- |
| 会话 | 一条绑定项目与 Provider 的对话上下文 | `thread` |
| 回合 | 用户或宿主发送消息后，Provider 完成一次响应的过程 | `turn` |
| Provider | MCode 接入的 AI 编码工具或模型端点 | `harness.Harness` |
| 主代理 | 开启编排功能并派发任务的主会话 | `lead` |
| 子代理 | 主代理派出的独立工作会话 | `bee` |
| 档位 | 为子代理预设 Provider、模型、推理强度与写入白名单的配置 | `tier` |
| 总管 | 主会话的无状态前台，负责读取状态与调度看板任务 | `MoCo · 总管` / `steward` |
| 目标 | 看板上由规划器拆解为多个依赖任务的顶层目标 | `Goal` |
| 收件箱 | 聚合异常、待审批与待确认事件的统一提醒队列 | `attention` |

## 跨 Agent 编排规格

### 跨 Provider 按档位混编

MCode 内置 5 个默认档位：

- 只读（`read`）
- 杂活（`chore`）
- 执行（`edit`）
- 攻坚（`deep`）
- 设计（`design`）

每个档位可以绑定独立的 Provider、模型、推理强度与写入白名单（`WritePaths`）。

MCode 按以下 5 层顺序解析子代理模型，后一层覆盖前一层：

- 档位配置或旧 `Profile`
- 会话子代理模型
- 配对覆盖（`pair`）
- 会话覆盖
- 委派直属指定

直属指定 `provider`、`model` 或 `pair` 前，主代理必须先获得用户消息的明确授权。

派发条目指定 `project` 字段时，主代理可以跨项目派出子代理。

主代理支持 2 种派发方式：

- 调用 `swarm_delegate` 工具即时派发（由 `orchestration.delegate_tool` 控制，默认开启）
- 在回复末尾输出 `swarm` JSON 代码块，MCode 在回合结束时解析派发

### 文件冲突排队与并行调度

派发条目声明 `files` 路径后，MCode 检查运行中与审查中子代理的路径集合。各条目路径不重叠时，子代理并行运行。路径重叠时，后续条目自动进入排队。

### 工作副本隔离与重启保护

`workspace_mode` 支持 `worktree` 与 `shared` 两种模式。

非 Git 目录启用影子仓库（`ShadowRepo`）。无法使用 `ShadowRepo` 时，MCode 退回 `SharedNoGit` 模式。

MCode 强制隔离只读子代理与白名单子代理的工作副本。合入更改前，MCode 过滤白名单之外的文件更改。

MCode 将子代理状态写入数据目录 `mcode.db` 的 `hydra_leads` 表。应用重启时，MCode 将未结束的子代理标为 `Interrupted`。MCode 不合入这些子代理的半成品。

### 自动验收回炉与分级审查合入

派发条目可以附带 `acceptance` 验收命令。子代理汇报后，MCode 在该子代理的工作副本内运行该命令。

验收命令退出码非 0 时，MCode 将失败输出发回该子代理续修，最多 1 次。用户也可以在界面重跑验收。

`orchestration.review_mode` 支持 3 种审查模式：

- `off`（默认）：验收通过后自动合入
- `human`：验收通过后转交人工批准或放弃
- `ai`：依次执行 4 级审查：
  - L0 确定性检查
  - L1 规则分级
  - L2 批量模型审查
  - L3 只读审查会话

合入阶段，MCode 先创建本地提交，再用 `merge-tree` 预检冲突。`merge-tree` 检出 `HEAD` 冲突时，MCode 将冲突交回该子代理解决，最多 1 次。

`AutoPush` 开启时，MCode 才推送分支并创建 PR。`AutoPush` 未开启时，MCode 不推送也不创建 PR。

### 交互式设计定稿与全量派发模式

`design` 档位启用共享工作区与交互定稿流程：

- 子代理调用 `open_preview` 打开 HTML 设计稿，状态转为 `AwaitingFinalize`
- 主代理用 `swarm_message` 向该子代理转达用户意见
- 子代理调用 `design_finalize` 确认定稿

用户也可以在 Web 远程端预览设计稿并定稿。

数据目录 `config.json` 的 `orchestration` 段提供 `always_bees` 配置项。该项默认为 `false`，由旧键 `general.swarm_send_to_bees` 自动迁移。

将 `always_bees` 设为 `true` 后，非 `plan` 模式下已开启编排的主会话将普通纯文本消息直接交给子代理。此时 `threads.queue.interruptAndSend` 返回 `sent_to_swarm`。以下 4 类消息仍发往主会话：

- 显式排队消息
- 附件消息
- `/` 命令
- `[编排]` 通知（兼容旧 `[Hydra]` 前缀）

### 常用 `orchestration` 配置键

| 配置键 | 说明 | 默认值或取值 |
| :--- | :--- | :--- |
| `delegate_tool` | 开启 `swarm_delegate` 即时派发工具 | 默认 `true` |
| `review_mode` | 合入前的审查模式 | 默认 `off`；支持 `off`、`human`、`ai` |
| `max_bees` | 同时运行的子代理数量上限 | 按配置限制并发 |
| `max_rounds` | 单次编排的轮次上限 | 按配置限制轮次 |
| `bee_timeout_minutes` | 单个子代理的运行超时时间 | 默认 `60` 分钟 |
| `request_timeout_minutes` | 单次请求的编排总时长上限 | 按配置限制总时长 |
| `workspace_mode` | 子代理工作副本的隔离模式 | 支持 `worktree` 与 `shared` |
| `tiers` / `default_tier` | 档位配置表与默认档位 | 内置 5 个默认档位 |
| `always_bees` | 将普通纯文本消息直接派发给子代理 | 默认 `false` |

## Provider 清单、多账号路由与会话内核

### 已接入的 Provider 清单

| 接入类别 | Provider |
| :--- | :--- |
| 主力 CLI | `Claude Code`、`Codex`、`Antigravity`（`agy`） |
| 其他 CLI | `Copilot`、`Pi`、`OMP`、`Command Code` |
| ACP 协议接入 | `Cline`、`Cursor`、`OpenCode`、`Grok`、`Devin`、`Factory Droid` |
| 内置引擎与 API 来源 | `mcode` 内置引擎、`Anthropic`、`OpenAI`、`DeepSeek`、`Z.ai`、`Meta`、自定义端点（`custom-<id>`）、`Magpie` 网关（`magpie`、`magpie2`–`magpie4`） |

### 多账号路由与用量监控

`multiaccount.Router` 提供 6 种账号路由策略，并启用指数退避冷却：

- `smart`
- `order`
- `rotate`
- `usage`
- `pace`
- `weight`

`agy` 支持账号亲和性绑定。

用量面板汇总各工具的额度与费用数据：

- 额度窗口聚合：`codex`、`claude`、`pi`、`zai`、`cline`、`droid`、`agy`
- 余额读取：`deepseek`、`cline`、`commandcode`
- 会话费用统计：按会话累计调用费用

### 可控会话运行内核

- **队列与插队**：会话运行中发送消息时，若 Provider 实现 `Steerer` 且开启插入，MCode 将消息注入当前回合。未满足上述条件时，消息进入队列。队列支持 5 种操作：
  - 立即发送
  - 插队
  - 重排
  - 编辑
  - 删除
- **撤回与回退**：用户可以调用 `RecallTurn` 撤回回合。MCode 在回合前后写入 Git 检查点（`refs/mcode/checkpoints/<threadID>/<turnID>/{start,end}`，最多保留 50 个）。用户可以预览并回退文件改动。
- **分割与自愈**：MCode 支持手动分割会话（`split_session`）与上下文轮换。MCode 按项目持久化非敏感的 `always_allow` 审批规则。发生会话失效或进程崩溃时，MCode 重建上下文并重跑 1 次。发生网络瞬断时，MCode 最多重试 3 次。

## 总管、看板、自动化与防休眠机制

### 总管前台

总管是主会话的无状态前台，按串行顺序接管用户消息。

每次调用模型前，总管现场拼装上限 24000 字的快照。总管在最多 4 轮内执行读工具。仅当某轮无读工具且无校验错误时，总管才将回复落库并顺序执行写工具。

总管遵守明确的职责边界：

- 不写代码，不改文件，不推送远端
- 将执行工作交给看板任务会话
- 硬锁拦截高危操作，用户答复决策卡（`D-xxxx`）后写入一次性解锁标记

### 收件箱

收件箱（`attention`）聚合 4 类来源的事件，并按 `dedupe_key` 去重（单对象 24 小时上限 6 条）：

- 会话事件
- 看板事件
- 自动化失败事件
- 编排事件

任务中心「待你处理」视图聚焦 5 类必须由用户决定的事件：

- 会话待审批
- 看板待审批
- 看板待验收
- 目标规划失败
- 设计稿待确认

### 目标与看板

创建 `Goal` 后，规划器在 `plan` 模式下生成任务 JSON。

用户确认计划后，`GoalScheduler`（默认每 10 秒轮询 1 次）按依赖关系与并发槽位启动任务。

任务默认在独立 `worktree` 中执行。`Watchdog` 每 5 秒巡视 1 次，检测任务状态与运行卡死。

任务含 `[命令]` 标签时，MCode 在该 `worktree` 内运行验收命令。审核通过后，MCode 执行 `squash` 合并并清理 `worktree`。

### 定时自动化与防休眠

定时自动化支持 6 类调度规则与 3 种运行模式（`standalone`、`dedicated`、`heartbeat`）。跨进程文件锁触发每次执行，MCode 随后评估 `stopWhen` 条件并结算结果。

`SleepGuard` 每 20 秒检查 1 次运行状态。检测到活跃远程会话、运行中的自动化或 5 分钟内到期的自动化时，`SleepGuard` 阻止系统休眠：

- macOS：启动 `caffeinate -i -w`
- Windows：调用 `SetThreadExecutionState`

## 桌面工作台、远程服务、MCP 与加密备份

### 性能与渲染

桌面端采用纯 Go（`CGO_ENABLED=0`）构建，不依赖 cgo。GPU 原生的 `MyGo` 框架负责绘制全部界面。工作台包含以下渲染与存储机制：

- 长会话基于消息指纹增量重建时间线
- 支持回合步骤清单卡片
- 支持 Mermaid 图表渲染与源码切换
- 支持 Markdown 相对图片路径解析
- 集成原生 `mygo.WebView` 预览与 macOS 快速查看
- 空闲会话支持 `zstd` 压缩与存储清理

### 统一 Git 与 PR 面板

Git 面板在同一工作区整合代码审阅与日常操作：

- 查看本轮改动、Diff 与提交历史搜索
- 初始化 Git 仓库与切换分支
- 每 5 分钟在后台执行 1 次 `fetch`
- 搜索 PR、发表行内审阅并创建解决冲突的 `worktree`
- 内置终端与文件管理器

### 跨会话全文搜索

命令面板等待 200ms 防抖后查询 SQLite 消息记录，并定位历史会话中的任意消息。

### 远程控制服务与离线推送

`web/` 目录下的 React + TypeScript 远程前端打包内嵌在 `internal/webassets` 中。

远程服务默认监听 `0.0.0.0:43117`，经 `/ws` 提供 JSON-RPC 2.0 服务，契约见 [remote-control.schema.json](remote-control.schema.json) 与 [remote-control-protocol.md](remote-control-protocol.md)。

仓库另提供无桌面界面的独立服务端 `cmd/mcode-server/`。

远程端支持 3 种接入方式：

- 局域网直连
- TLS / ACME 证书访问
- 桌面端内嵌 `tsnet` Tailscale 入口（`cmd/mcode-server/` 不包含 Tailscale）

服务端拒收转发头，并校验 `Host` 与 `Origin`。客户端配对支持 64 位 hex 令牌或 6 位短码。10 分钟内连续出错 5 次时，服务端封禁该来源 5 分钟。

服务端支持 RFC 8291 与 VAPID Web Push 离线推送，并支持在通知内经 `/api/remote/answer` 作答。

Web 端覆盖以下 12 个功能模块：

- 会话
- 编排
- 设计稿预览与定稿
- 看板与目标
- 自动化
- Git 与 PR
- 文件浏览
- 脚本
- 项目文档
- 实时日志
- 收件箱
- 记忆审批

### MCP 代理、知识记忆与加密备份

- **MCP 服务与代理**：普通会话将内置工具折叠为 `kanban`、`automation`、`memory` 三个入口。上游 MCP 工具数量超出阈值时，MCode 将其折叠为 `call_tool`，并在空闲 15 分钟后回收连接。
- **项目文档与技能**：`projctx` 用 SHA-256 CAS 读写项目文档，生成上限 32000 字符的 `<project_context>`。MCode 同时自动释放内置技能。
- **长期记忆**：MCode 支持 Nowledge Mem 与本地 `localmem` 双后端。Agent 提议的新记忆先进入收件箱等待确认。仅当用户明确要求时，MCode 才立即写入记忆。
- **加密备份**：MCode 使用 `MCODEBK1` 容器格式保存备份，并用 Argon2id 与 XChaCha20-Poly1305 加密配置与钥匙串凭据。

## 目录结构

| 路径 | 说明 |
| :--- | :--- |
| `cmd/mcode/` | 桌面客户端主程序入口 |
| `cmd/mcode-server/` | 无桌面界面的远程控制服务端入口 |
| `internal/` | Go 后端子系统与基于 `MyGo` 的原生桌面界面实现 |
| `web/` | 基于 React 与 TypeScript 的远程控制前端源码 |
| `scripts/` | 校验、打包、发布与开发辅助脚本 |
| `resources/` | 应用图标、字体与 macOS `entitlements` 内嵌资源 |
| `docs/` | 用户指南、开发文档、远程协议契约与第三方许可声明 |
| `.agents/` | 项目级 Agent 技能目录 |
| `.github/` | GitHub Actions 发布工作流配置 |

## 开发、调试与发布

### 平台支持与开发环境

- **macOS（14.0+）**：主要支持平台。
- **Windows**：尽力支持。主要功能正常运行，发布前不做完整验证，平台相关缺陷按优先级修复。
- **Go 版本**：`1.27.3`。
- **开发运行**：执行 `go tool mygo dev`。
- **依赖检查**：执行 `scripts/check-deps.sh`。
- **统一检查**：执行 `scripts/check.sh [--race]`。脚本依次执行 8 项检查：
  - `gofmt`
  - 构建
  - `go vet`
  - `mygo vet`
  - 依赖检查
  - 单元测试
  - 前端类型检查
  - 前端测试
  脚本自动排除 `build.noindex/` 与 `dist/` 目录。传入 `--race` 时，脚本用 race 测试替换常规单元测试。
- **发布打包**：执行 `scripts/package_release.sh [--platform <target>] [--out <dir>] [--skip-notarize]`。默认目标为 `darwin/arm64`，产物输出到 `dist`。
- **个人打包（macOS）**：执行 `scripts/package_personal.sh`。存在证书时脚本用 Developer ID 签名，否则执行临时签名且不做公证。安装包写入 `/Applications` 后默认重启应用。脚本支持 `--clean`、`--bump`、`--dmg`、`--windows` 与 `--no-relaunch` 参数。
- **个人打包（Windows）**：执行 `scripts/package_windows.ps1`，支持 `-Clean`、`-Bump`、`-NoRelaunch`。
- **数据与日志**：数据目录默认 `~/.mcode`（优先级依次为 `-data`、`MCODE_DATA`、设置项）。日志存放在 `<数据目录>/logs`，包含 `mcode.log`、`startup.log` 与 `crash.log`。
- **调试**：设置 `MCODE_PPROF=host:port` 启动仅绑定本机回环地址的 pprof 服务。之后用 `go tool pprof http://<host>:<port>/debug/pprof/profile` 抓取 CPU profile。设置 `MCODE_STARTUP_TRACE=1` 后，MCode 将启动阶段耗时写入 `mcode.log`（搜索 `startup:`）。
- **测试**：运行 `go test -timeout 60s ./...` 或 `go test -race -timeout 120s ./...`。
- **更新截图**：运行 `MCODE_README_SHOTS=1 go test -timeout 600s -run TestReadmeShots ./internal/ui`。该命令用 headless 渲染生成 9 张桌面端截图到 `docs/images/`。`swarm-floats.png` 与 `remote-web.png` 不在该命令生成范围内。

### 发布步骤

1. 切换到干净的 `main` 分支。
2. 运行 `scripts/release.sh [patch|minor|major|版本号] [--notes 文件] [--dry-run]`。
   - 未传入 `--notes` 时，脚本读取 `.env` 中的 `MCODE_RELEASE_AGENT`（默认 `claude`）与 `MCODE_RELEASE_MODEL` 生成中文亮点。
   - AI 生成失败时，脚本回退到提交标题。
3. 终端显示更新内容后，按 `Enter` 或输入 `y` 使用，输入 `e` 编辑，输入 `n` 取消。
   - 60 秒无输入或处于非终端环境时，脚本自动确认内容。
   - 确认后，脚本更新 `CHANGELOG.md` 与 `mygo.json`。
   - 随后脚本向远端推送 `main` 分支与标签。
4. 推送标签未触发工作流时，运行 `gh workflow run release.yml --ref v<版本号>` 补触发。

工作流构建 macOS（`arm64` / `amd64`）与 Windows（`amd64`）产物。发布正文采用 `CHANGELOG.md` 的对应版本章节。macOS 产物未公证时，正文追加 Gatekeeper 提示。Windows 产物未签名，正文追加 SmartScreen 提示。

发布工作流必须配置 `MYGO_UPDATER_PRIVATE_KEY`。启用 macOS 签名与公证时，必须配置以下密钥：

- **签名密钥**：`MACOS_CERTIFICATE`、`MACOS_CERTIFICATE_PASSWORD`、`MACOS_SIGN_IDENTITY`
- **公证密钥**：`APPLE_ID`、`APPLE_TEAM_ID`、`APPLE_APP_PASSWORD`

### 相关文档与许可

- 新用户指南：[user-guide.md](user-guide.md)
- AI Agent 自动安装：[agent-install.md](agent-install.md)
- 开发文档（基于 `v0.1.158` 整理；与代码不一致时以代码为准）：[dev/README.md](dev/README.md)
- 远程控制契约与协议：[remote-control.schema.json](remote-control.schema.json)、[remote-control-protocol.md](remote-control-protocol.md)
- 版本更新日志：[../CHANGELOG.md](../CHANGELOG.md)
- 许可证与声明：[../LICENSE](../LICENSE)、[../NOTICE](../NOTICE)、[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)
