# 11 开发指南

## 协作约定

项目规则在根目录 `AGENTS.md`（`CLAUDE.md` 内容相同），要点：

- 对话和文档用简体中文，代码、标识符、路径、命令保持原文。
- 禁止 cgo。
- 严禁自行 `git push` 或修改远程分支。
- 弹窗和浮层规则见 [09-desktop-ui.md](09-desktop-ui.md#必须遵守的-ui-规则)。

## 常用命令

```sh
# 开发运行
go tool mygo dev

# 构建和检查
CGO_ENABLED=0 go build ./...
go vet ./...
scripts/check-deps.sh

# 测试
go test -timeout 60s ./...
go test -race -timeout 120s ./...

# Web
cd web && npm run build
cd web && npm run generate:contract
```

Go 代码内嵌 `internal/webassets/dist`，改了 Web 要重新构建才能在桌面和远程服务里看到。

## 脚本

| 脚本 | 用途 |
|---|---|
| `scripts/check-deps.sh` | 依赖方向检查 |
| `scripts/package_personal.sh` | 个人包：Developer ID 签名、不公证，先构建 Web，默认沿用 `mygo.json` 的版本，传 `--bump` 或 `BUMP=1` 时版本 patch 加 1，装到 `/Applications`。参数 `--clean --dmg --relaunch/--no-relaunch` |
| `scripts/package_release.sh` | 发布包 |
| `scripts/package_windows.ps1` | Windows 个人包：先构建 Web 与 Windows 安装程序，默认沿用 `mygo.json` 的版本，静默覆盖安装并重启 MCode。参数 `-Clean -Bump -NoRelaunch` |
| `scripts/relaunch_app.sh` | 重启应用。调用方可能是 MCode 自己（内置终端或脚本菜单），应用退出时会杀掉子进程，所以脚本先用 `setsid` 脱离当前会话 |
| `scripts/relaunch_app.ps1` | Windows 重启应用：等调用方退出后静默运行安装程序，再启动安装目录里的 MCode |
| `scripts/fetch_models_dev_snapshot.sh` | 更新 models.dev 快照 |

## 守卫测试

| 测试 | 拦截 |
|---|---|
| `internal/ui/modal_style_guard_test.go` | 使用 `ui.Modal`、`ui.Popover` |
| `internal/ui/text_scale_guard_test.go` | 聊天区字号没走 `scaledFont` |
| `internal/mcp/tools_kanban_review_guard_test.go` | 看板审阅类工具的约束 |

Provider 的契约测试放在各自的包里（如 `internal/providers/codex`）。

## 常见改动清单

### 给会话加一个新能力（两端都要）

1. `internal/remote/memory_backend.go` 或 `thread_ops*.go` 实现。
2. 桌面：`internal/ui/client.go` 加 `Client` 方法，`internal/app/client.go` 实现。
3. 远程：按 [08-remote-web.md](08-remote-web.md#新增-rpc-的流程) 加 RPC，更新 schema，生成类型，改 `web/`。
4. 需要时在 `capabilities.go` 加 feature。

### 加一个设置项

见 [10-store-config.md](10-store-config.md#新增配置项)。

### 加一张表或一列

在 `internal/store` 的 `migrations` 末尾追加，每列一个版本。见 [10-store-config.md](10-store-config.md#迁移规则)。

### 改注入给模型的提示词

修改文案或顺序后递增 `internal/harness/policy` 的 `Version`。见 [02-session-runtime.md](02-session-runtime.md#提示词注入)。

### 加一个 Provider

见 [03-providers.md](03-providers.md#新增-provider-的步骤)。

### 改 Web 分包

四处同步，见 [08-remote-web.md](08-remote-web.md#拆包时要同步四处)。

### 加一个 MCP 工具

在 `internal/mcp/registry.go` 注册，见 [07-mcp-context-memory.md](07-mcp-context-memory.md#改动提示)。

## 历史命名

| 旧名 | 新名 | 残留位置 |
|---|---|---|
| Hydra | Swarm | 表 `hydra_leads`，列 `hydra_pair_id`、`hydra_mode`；`[Hydra]` 汇报前缀兼容；`hydra-worktrees` 目录 |
| head | bee | 列 `heads_json`、`head_tiers`；会话模式旧值 `alwaysHeads` 归一化为 `alwaysBees` |
| `general.swarm_*` | `orchestration.*` | 仅 `internal/config/migrate.go` |

## 已知差异

文档和代码有出入的地方，读旧文档时注意：

| 位置 | 问题 |
|---|---|
| 根目录 `README.md`「编排」段 | 仍让用户设置 `general.swarm_send_to_bees`。这个键已由配置迁移改为 `orchestration.always_bees` |
| 根目录 `README.md` | 能力列表和目录结构未提到总管和收件箱 |

## 平台

macOS 14.0 以上是主要支持平台。Windows 尽力支持，发布前不做完整验证。平台差异放在带平台后缀的文件里，例如 `internal/remote/sleepguard_windows.go`、`internal/git/watch_darwin.go`、`internal/git/watch_other.go`（FSEvents 监听在其他平台退回轮询）。
