# 01 架构总览

## 产品形态

MCode 是一个 AI 编码桌面客户端，在一个窗口里管理多个 AI 编码工具的会话。它有三种运行形态，共用同一套会话内核：

| 形态 | 入口 | 说明 |
|---|---|---|
| 桌面应用 | `cmd/mcode` | MyGo 绘制原生窗口，进程内调用后端，可选开启远程服务 |
| 无头服务 | `cmd/mcode-server` | 无界面，只提供远程服务 |
| Web 远程端 | `web/` | React 单页应用，构建后内嵌到 Go 二进制，通过 WebSocket 连接上面两者 |

技术栈：

- 纯 Go，禁止 cgo。调用系统 API 只能用 purego、预置动态库或 Swift CLI 子进程。
- UI 框架 `github.com/egoist/mygo`（包名 `ui`），GPU 绘制，不使用 webview。
- 存储 `modernc.org/sqlite`，WebSocket `coder/websocket`。
- Web 端 React 18、TypeScript、Vite 5、Tailwind、react-markdown、KaTeX（懒加载）、`@tanstack/react-virtual`。

## 分层

```
┌────────────────────┐     ┌───────────────────────────┐
│ internal/ui        │     │ web/（React）             │
│ 桌面界面           │     │ 远程界面                  │
└─────────┬──────────┘     └──────────────┬────────────┘
          │ mcodeui.Client                │ JSON-RPC 2.0 / WebSocket
┌─────────▼──────────┐     ┌──────────────▼────────────┐
│ internal/app       │────▶│ internal/remote           │
│ 装配、Client 实现  │     │ server / dispatcher        │
└─────────┬──────────┘     └──────────────┬────────────┘
          │                               │
┌─────────▼───────────────────────────────▼────────────┐
│ remote.MemoryBackend（会话运行内核）+ StreamHub       │
└─┬──────────┬──────────┬───────────┬──────────┬───────┘
  │          │          │           │          │
harness    store      swarm       kanban     automation
  │                     │           │
providers             git         steward（经 app 接线）
```

`internal/app` 是唯一知道所有子系统的包，子系统之间尽量不直接依赖，靠 `app` 注入回调和接口接线。这也是 `Set*Func`、`registerRemoteWiring` 这类写法很多的原因。

桌面端和远程端走同一个 `MemoryBackend`。桌面端通过 `internal/app/client.go` 进程内调用，它实现了 `internal/ui/client.go` 定义的 `mcodeui.Client` 接口；远程端通过 `internal/remote/dispatcher.go` 路由 JSON-RPC。一个新功能如果要同时出现在两端，通常要接两条路径。

## 包地图

行数是非测试代码的约数，只用于判断规模。

| 包 | 行数 | 职责 |
|---|---|---|
| `internal/ui` | 112000 | 桌面界面，包名 `mcodeui` |
| `internal/providers` | 37000 | 各 Provider 的 harness 实现和辅助包 |
| `internal/remote` | 37000 | 会话内核、JSON-RPC、WebSocket、配对、推送 |
| `internal/app` | 20000 | 应用装配、`Client` 实现、子系统接线 |
| `internal/mcp` | 10700 | 内置 MCP 服务、上游代理、OAuth |
| `internal/swarm` | 9200 | 编排 |
| `internal/git` | 6300 | Git 操作、检查点、worktree、PR |
| `internal/store` | 5400 | SQLite 持久化 |
| `internal/kanban` | 4000 | 看板、目标、规划、验收 |
| `internal/steward` | 3600 | 总管运行时 |
| `internal/config` | 3500 | 配置和密钥 |
| `internal/agentconf` | 3300 | 扫描各工具的技能和 MCP 配置 |
| `internal/projctx` | 2800 | 项目文档、guidance、技能索引 |
| `internal/automation` | 1800 | 定时任务 |
| `internal/harness` | 900 | Provider 抽象和提示词 `policy` |
| `internal/nmem`、`internal/memspace` | 1100 | Nowledge Mem 客户端和空间解析 |
| `internal/textgen` | 700 | 标题、提交信息、分支名生成 |
| `internal/native` | 700 | 通知、快捷键、提示音、托盘、keyring |
| `internal/procs` | 500 | 按会话定位并终止命令子进程树 |
| `internal/modelsdev` | 400 | models.dev 快照和缓存 |
| `internal/attention` | 300 | 提醒事件定义和服务 |
| `internal/startuptrace`、`internal/procattr`、`internal/redact`、`internal/webassets` | 少量 | 启动打点、进程属性、日志脱敏、内嵌静态资源 |

## 依赖规则

由 `scripts/check-deps.sh` 检查：

1. `harness`、`store`、`git`、`kanban`、`remote`、`runtime`、`providers`、`automation`、`projctx`、`mcp`、`swarm` 不得 import `internal/ui` 和 mygo。
2. `internal/ui` 不得 import `store` 和 `remote/wire`。可以 import 顶层 `remote`（例如 `LANIPv4s`）。
3. `internal/native` 不得 import `ui`、`remote`、`config`。

UI 要用的数据由 `internal/app/convert.go` 这类转换函数从 `wire` 类型转成 `mcodeui` 类型。

## 启动流程

### `cmd/mcode/main.go`

1. 非 darwin 平台抢单实例锁。
2. 补全 `PATH`（`augmentedPath`），后台预热 `loginenv.Environ`。
3. 解析 flag：`-data -provider -workdir -mode -claude-bin -codex-bin`。对应环境变量 `MCODE_DATA`、`MCODE_PROVIDER`、`MCODE_WORKDIR`、`MCODE_MODE`、`MCODE_CLAUDE_BIN`、`MCODE_CODEX_BIN`，命中的键进入 `lockedKeys`。
4. `initStartupLog`，然后 `config.Load`、`ApplyEnv`、`Validate`。
5. Provider 回退：claude 和 codex 互相回退，`memory` 只有显式指定才保留。
6. 迁移旧密钥，设置 MCP 工具策略路径和 worktree purge 根目录。
7. 后台 goroutine 跑 `app.Open`，不挡首屏。
8. `WhenReady` 里等 `Open` 结果，然后 `InstallAppMenu`、`OpenThreadWindow`、`setupNative`（`cmd/mcode/native.go`）、`SetOwnerNotifier`、`SetAutomationNotifier`。数据库发生自愈时弹窗告知备份路径。

### `internal/app/app.go` 的 `Open`

顺序有依赖关系，调整前要读懂前后段：

1. 日志（`redactWriter` 脱敏），读配置，后台 `probeClaudeBinary`。
2. `store.Open`（`mcode.db`）。
3. `StreamHub`、`MemoryBackend`、`modelsdev.Start`、`AttachStore`。
4. Git Runner（并发 4）。
5. swarm Manager 和 Pipeline：`Restore`、`ResumeBatch`、`SweepOrphanCopies`。
6. kanban Service、`GoalScheduler`（10s）、`Watchdog`（5s）。
7. MCP OAuth、Hub、Proxy，nmem 客户端，内置 MCP Server（`mcp.New`）。
8. 向 Provider 注入 builtin、external、blocked MCP（`providerMCP`）。
9. 迁移旧 magpie 端点和 swarm profiles。
10. `wireSwarmWithModes` 或 `prefaceDecorator` 安装提示词装饰器，`SetFirstTurnExtra` 安装总管移交提示。
11. 一批 `backend.SetXxxFunc`，`wireAutoTitle`，更新检查。
12. 构造 harness：`SetHarness`、`RegisterHarness`、`registerExtraProviders`。
13. automation Service 和防休眠 `SleepGuard`（20s）。
14. 首次启动种演示项目（`demo_seeded`）。
15. `NewClient`、attention、`NewRemoteService`，注入 Pi Magpie、额度读取、人设等配置，按配置启动远程服务。
16. `wireSteward`、`wireGoalReviewWake`、`wireRemoteExtensions`。

### 关闭顺序

`App.Close` 依次关闭：总管 → 验收唤醒 → attention 接线 → mcpProxy → mcpHub → mcp → automationSleep → automation → scheduler → kanban → swarmPipeline → swarm → remote → hub → backend → store。

新增后台组件时，要放到它依赖的组件之前关闭。

### 配置生效时机

大多数配置每次使用时现读（`config.Load` 加 `ApplyEnv` 的闭包），改设置下一次使用就生效。少数在 `Open` 时确定的项（例如 Provider 注册）需要重启。

## 无头服务

`cmd/mcode-server` 的 flag：`-listen -port -data -provider -workdir -mode -claude-bin -codex-bin -tls-listen -tls-domain -acme-email -acme-directory`。`headlessSwarmExtras` 提供无界面时的编排接线。

## 数据目录

默认 `~/.mcode`，可用 `MCODE_DATA` 或 `-data` 修改。

| 路径 | 内容 | 所有者 |
|---|---|---|
| `config.json` | 配置，迁移时生成 `config.json.bak`、`config.v7.json` | `internal/config` |
| `mcode.db` | SQLite 主库 | `internal/store` |
| `logs/` | `mcode.log`、`startup.log`、`crash.log` | `internal/app`、`cmd/mcode` |
| `worktrees/` | 看板任务的工作副本 | `internal/kanban` |
| `swarm-worktrees/` | 子代理的工作副本（旧版 `hydra-worktrees`） | `internal/swarm` |
| `shadow-git/` | 非 git 项目的影子仓库 | `internal/git` |
| `steward/` | 总管主目录 | `internal/app` |
| `remote/project-scripts.json`、`remote/script-favorites.json` | 项目脚本和收藏 | `internal/remote` |
| `remote-devices.json` | 已配对设备和推送订阅 | `internal/remote` |
| `remote-webpush-vapid.pem` | Web Push 私钥 | `internal/app/remote_service.go` |
| `remote-action-ledger.json` | 远程动作幂等账本 | `internal/remote` |
| `always_allow.json` | 始终允许的审批规则 | `internal/remote/always_allow_store.go` |
| `mcp_tool_policy.json` | MCP 工具停用策略 | `internal/agentconf` |
| `models_dev_cache.json` | models.dev 缓存 | `internal/modelsdev` |
| `agy-accounts.json`、`agy-bindings.json`、`agy-routing.json` | Antigravity 多账号 | `internal/providers/agy/accounts` |
| `ui-state.json` | 窗口和面板状态 | `internal/ui/ui_state.go` |
| `cert.pem`、`key.pem`、`selfsigned-*.pem`、`account.key` | TLS 证书和 ACME 账号 | `internal/remote` |

项目级数据放在 `<project>/.mcode/context/`，见 [07-mcp-context-memory.md](07-mcp-context-memory.md)。
