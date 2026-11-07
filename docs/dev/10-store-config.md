# 10 存储与配置

## SQLite 存储

代码在 `internal/store`，驱动 `modernc.org/sqlite`，数据库文件 `mcode.db`。

- `MaxOpenConns(1)`，所有访问串行。
- 错误：`ErrNotFound`、`ErrRevisionConflict`（乐观并发）。

### 表

| 表 | 内容 | 文件 |
|---|---|---|
| `projects`、`threads`、`messages` | 项目、会话、消息 | `store.go` |
| `turns`、`turn_usage` | 回合和用量 | `turns.go`、`recall.go` |
| `thread_follow_ups` | 排队消息 | `store.go` |
| `quarantined_threads` | 隔离的会话 | `store.go` |
| `tasks`、`goals`、`goal_tasks` | 看板 | `kanban.go` |
| `automations`、`automation_runs` | 自动化 | `automation.go` |
| `hydra_leads` | 编排状态（旧名） | `swarm.go` |
| `swarm_reports` | 子代理汇报存档 | `swarm_reports.go` |
| `attention_events` | 提醒事件 | `attention.go` |
| `memory_proposals` | 记忆提议 | `memory_proposals.go` |
| `app_settings` | 键值设置（如 `steward.topics`） | `settings.go` |

`thread_notes`、`pinned_messages` 已删除。消息搜索在 `message_search.go`，回合撤回在 `recall.go`。

### 迁移规则

版本用 `PRAGMA user_version`，迁移是 `migrations` 切片：

- 只能追加到末尾，不能修改已发布的项。
- `ALTER TABLE` 每列一个版本，保证幂等。
- 表和列的物理名保持不变（`hydra_leads`、`heads_json`、`head_tiers`、`hydra_pair_id`、`hydra_mode`），代码层用新名字。不要为了改名写迁移。

### 启动和自愈

1. 常规启动跳过同步 `quick_check`，后台用只读连接检查。
2. 检查失败时写 `.needs-check` 标记。
3. 下次启动看到标记就同步检查。库损坏时改名备份并新建（`RecoveredFrom`），桌面端弹窗告知备份路径。
4. 启动时清扫孤儿 provisional 会话，`finalizeOrphanTurns` 收尾未结束的回合。

## 配置

代码在 `internal/config`，文件 `config.json`。

### API

| 函数 | 用途 |
|---|---|
| `Load`、`LoadWithWarnings` | 读取。`LoadWithWarnings` 经 `repairInvalidFields` 修复非法字段并返回警告 |
| `Save`、`Update(dataDir, fn)` | 写入。修改已有配置优先用 `Update` |
| `ApplyEnv` | 叠加环境变量 |
| `Validate`、`Defaults` | 校验和默认值 |

布尔字段多用 `*bool`，`nil` 取默认值，配 `XxxEnabled()` 方法。这样可以区分「未设置」和「显式关闭」，也能在之后改默认值。

### 顶层结构

| 段 | 内容 |
|---|---|
| `session` | 会话默认值 |
| `models` | `roles`（`lead`、`coder`、`research`、`vision`、`utility`）、`pins`、`preferences` |
| `providers` | `claude`、`codex`、`agy`、`copilot`、`pi`、`omp`、`commandcode`、`acp`（map）、`openai`、`presets`、`custom` |
| `orchestration` | 编排（见 [04-swarm.md](04-swarm.md)），含 `steward` 子段（见 [05-steward-attention.md](05-steward-attention.md)） |
| `memory`、`startup`、`notifications`、`permissions` | 记忆、启动、通知、权限 |
| `appearance` | `theme`、文本缩放 |
| `workbench`、`quick_replies`、`shortcuts` | 工作台、快捷回复、快捷键覆盖 |
| `remote` | 默认 `0.0.0.0:43117`，TLS 建议 8443，`push_enabled` |
| `source_control` | 版本控制 |
| `archive` | 自动休眠，默认 7 天 |
| `automation` | `prevent_sleep` |

### 迁移

`internal/config/migrate.go` 的 `migrateRaw` 在原始 JSON 上做版本迁移，当前版本是 9。迁移时备份 `config.json.bak`，并留 `config.v7.json` 快照。

旧键到新键的对照也在这个文件里，例如 `general.swarm_send_to_bees` → `orchestration.always_bees`，`general.swarm_lead_plans_only` → `orchestration.lead_plans_only`。

### 新增配置项

1. 在对应结构体加字段和 JSON tag，用 `*bool` 或零值代表默认。
2. 必要时加 `XxxEnabled()` 或取值方法。
3. 只增加字段不需要提升版本。移动或改名字段时才写迁移并加测试（参照 `migrate_v8_test.go`）。
4. 需要校验时改 `Validate` 和 `repairInvalidFields`。
5. 接 UI，见 [09-desktop-ui.md](09-desktop-ui.md#设置页)。Web 通过 `settings.get`、`settings.set`（`internal/app/remote_settings.go`）访问，敏感字段要确认不会暴露。

## 密钥

`config.NewSecretStore(service)` 封装 keyring。darwin 上通过 `security` CLI 访问（`internal/native/keyring`），不使用 cgo。启动时迁移旧存储。服务名：`mcode`（Provider 密钥）、`mcode.mcp-oauth`（MCP OAuth 令牌）。

日志经 `internal/redact` 脱敏。新增会打印请求或环境的代码时，确认敏感信息会被覆盖。
