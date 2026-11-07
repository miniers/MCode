# 03 Harness 与 Provider

## Harness 抽象

`internal/harness` 定义内核和 Provider 之间的接口。

```go
type Harness interface {
    Kind() Kind
    Run(ctx context.Context, turn Turn, emit func(Event)) error
}
```

`Turn` 的字段：`ID`、`ThreadID`、`Prompt`、`WorkDir`、`AdditionalDirs`、`SessionID`、`Model`、`Mode`、`Approver`、`Sandbox`、`Env`、`Images`、`InteractionMode`（`build`、`plan`）、`Effort`、`FastMode`、`SpeedTier`、`Transient`。

### 可选接口

调用方用类型断言探测。

由 Harness 实现：

| 接口 | 能力 |
|---|---|
| `DeliveryConfirmer` | 声明本 Provider 会回报审批答复是否写入底层 CLI |
| `Steerer` | 回合中插入消息 |
| `TurnStopper` | 只强停指定回合，不影响同一 Provider 的其他会话 |
| `ProcessRooter` | 按会话返回 agent 进程 PID，用于定位命令子进程树。`ProcessRoots` 是现成的辅助实现 |
| `ThreadEvictor` | 会话或项目删除时释放空闲常驻进程 |

由审批方实现（内核的 `threadApprover`），通过 `Turn.Approver` 交给 Provider：

| 接口 | 能力 |
|---|---|
| `Approver` | `Approve`：处理审批请求 |
| `Interactor` | `Interact`：处理提问等交互 |
| `DeliveryAcker` | 接收答复写入 CLI 的结果。Provider 用 `harness.AckDelivery` 回报 |
| `WorkDirProvider` | 提供当前工作目录 |

`Sandbox` 由 `Turn.Sandbox` 传入，按权限模式包装子进程命令。

### 事件

`text`、`reasoning`、`tool_start`、`tool_output`（增量）、`tool_end`、`file_change`、`session`、`usage`、`error`、`done`、`text_retract`、`context_compacted`。

`error` 带 `ErrKind`：`auth_required`、`rate_limited`、`context_exhausted`、`max_turns`、`cancelled`、`api_error`。内核靠这些类型决定自愈和提醒，新 Provider 要尽量分类准确。

`Usage` 含 `InputTokens`、`OutputTokens`、`CachedTokens`、`ContextTokens`、`DurationMs`、`ContextWindow`、`CostUSD`。

`NonInteractiveEnv` 给子进程设置 `GIT_TERMINAL_PROMPT=0`、`EDITOR=true`、`PAGER=cat`、`CI=1` 等，防止命令卡在交互上。

## Provider 一览

代码都在 `internal/providers/<name>/`。

| Provider | 传输方式 | 备注 |
|---|---|---|
| `claude` | `claude -p --output-format stream-json` | `resident.go` 实现常驻进程（`--input-format stream-json`，空闲回收由 `providers.claude.residentIdleMinutes` 控制，用 `control_request` 中断）；`approval_mcp.go` 审批；`auth.go`；`gateway.go`；`planlimits.go` |
| `codex` | `codex app-server --stdio`（JSON-RPC 2.0） | README 基于 codex-cli 0.165.0；有 schema 契约测试；`BlockedMCP`；用 `thread/start`、`thread/resume`、`turn/start`、`interrupt`、`compact`、`review` |
| `agy` | `--input-format/--output-format stream-json`，`--mode plan/accept-edits --dangerously-skip-permissions` | `accounts/` 多账号 OAuth、路由、warmup、token lock；`quota/`；`question_bridge.go`；`mcp_settings.go` |
| `copilot` | `copilot --headless --stdio --no-auto-update` | |
| `pi` | `pi --mode rpc` | 门禁扩展 `mcode-gate.ts`，`magpie_bridge.ts`、`mcp_bridge.ts`；空闲回收 10 分钟；`piSkillManifest`（`--no-skills` 加 `--skill`） |
| `omp` | `omp --mode rpc-ui --approval-mode yolo` | 门禁扩展装不上就不注册；不接 MCP |
| `commandcode`（`cmd`） | `cmd -p --output-format json`，每轮一个进程 | 审批靠 mod 和目录文件 |
| `openai` | Chat Completions SSE | 预设 deepseek、zai、meta；`custom_endpoints.go` 自定义端点 |
| `acp` | Agent Client Protocol | cline、cursor（`cursor-agent`）、opencode、grok、devin、droid |

部分 Provider 有自己的说明文档：`internal/providers/{cline,codex,commandcode,credits,omp}/README.md`。

## 辅助包

| 包 | 内容 |
|---|---|
| `magpie` | 网关模型目录、额度、人设 |
| `multiaccount` | 多账号调度（smart、order、rotate、usage、pace、weight）和冷却 |
| `credits` | 余额和方案额度（cline、commandcode、deepseek、factory、zai） |
| `cliupdate` | CLI 工具更新 |
| `modelcache` | 模型列表缓存 |
| `loginenv` | 登录 Shell 环境缓存和代理探测。GUI 启动的进程没有 Shell 环境，子进程要用它 |
| `readhttp` | HTTP 读取 |
| `cline` | Cline 模型目录 |

## 注册

`internal/app/app.go`：

- `SetHarness`：默认 Provider。
- `RegisterHarness`：其他 Provider。
- `registerExtraProviders`：遍历 `acp.Agents()` 和配置，构造额外 Provider，并注入 `mcpInjection`（`BuiltinMCP`、`ExternalMCP`）。

## 模型与默认值

- `internal/remote/models.go`、`cli_catalog.go`：对外的模型列表。
- `internal/modelsdev`：内置快照（`scripts/fetch_models_dev_snapshot.sh` 更新）加运行时缓存，提供上下文窗口和价格。
- 配置 `models.roles` 已弃用，运行时不再读取，仅为兼容旧配置保留；另有 `pins`、`preferences`。

## 新增 Provider 的步骤

以下是通常涉及的位置，具体可对照 `copilot`（较简单）或 `acp`（适合走 ACP 的工具）：

1. 在 `internal/providers/<name>/` 实现 `harness.Harness`。把原生事件映射成 `harness.Event`，错误映射到 `ErrKind`，子进程环境用 `loginenv` 和 `NonInteractiveEnv`。
2. 按能力实现可选接口。最好支持 `Steerer` 和 `TurnStopper`，否则插入和停止的体验会降级。
3. 处理权限模式。CLI 没有审批协议时，要明确说明 `supervised` 的实际效果。
4. 在 `internal/config` 增加配置段和默认值，涉及结构变化时按 [10-store-config.md](10-store-config.md) 写迁移。
5. 在 `registerExtraProviders` 或 `Open` 里注册。如果支持 MCP，接入 `providerMCP` 的注入结果。
6. 在 `internal/remote/models.go`、`cli_catalog.go` 接入模型列表，在 `thread_ops.go` 按需加原生命令。
7. 需要额度显示时接 `internal/remote/quota.go` 和 `internal/providers/credits`。
8. 在 `internal/ui/settings_page_providers_agents.go` 和 Web 的设置页加入口。
9. 写测试。解析类逻辑放 fixture，参照 `internal/providers/codex` 的 schema 契约测试。
