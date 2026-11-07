# 加密配置备份与恢复实施计划

> 执行方式：按文件所有权并行实现三个任务；以本计划中的公开接口为唯一契约，集成后再运行各任务的验收检查。

**目标：** 用密码保护单个 `.mcodebackup`，在不同操作系统之间恢复配置、密钥和可选的会话历史。

**架构：** `internal/backup` 的容器层负责流式认证加密及安全解包，编排层负责采集、暂存、启动恢复；`internal/store` 负责一致快照和无历史数据合并；设置页只调用稳定 API。恢复分为未确认的暂存、已确认的启动应用、数据库合并和结果展示，均可在中断后重试。

**技术栈：** Go、SQLite、`golang.org/x/crypto/argon2`、`golang.org/x/crypto/chacha20poly1305`、`archive/tar`、MyGo。

**规格来源：** 本任务 brief；没有另设规格文件。

## 全局约束

- 纯 Go、禁止 cgo；不增加依赖；现有 `golang.org/x/crypto` 已包含所需算法。
- 不做远程 JSON-RPC；不执行提交或推送。
- 路径均相对仓库根目录；本文现有源码引用使用单个行号。
- 备份允许存在密码之外的源应用原有明文配置，但容器内任何内容，包括清单、密钥和数据库，都必须加密。
- 测试中使用独立临时目录和内存密钥库，禁止触碰真实用户数据；运行测试由执行实现者或主代理负责。

## 公开契约与文件所有权

任务 1 独占 `internal/backup/format.go`、`internal/backup/format_test.go`；任务 2 独占 `internal/backup/backup.go`、`internal/backup/collect.go`、`internal/backup/restore.go`、`internal/backup/backup_test.go`、`internal/store/backup.go`、`internal/store/backup_test.go`、`internal/agentconf/backup.go`、`internal/agentconf/backup_test.go`、`internal/app/app.go`、`internal/app/backup_restore_test.go`；任务 3 独占 `internal/ui/settings_page_general.go`、`internal/ui/settings_backup.go`、`internal/ui/settings_backup_test.go` 及实现项目失效标记所需的 `internal/ui/chat.go`、`internal/ui/settings_backup_test.go`；项目选择入口为 `internal/ui/chat.go:799`。

任务 1 提供下列低层 API；任务 2 使用它，任务 3 不直接使用它：

```go
package backup

type Header struct {
    Version uint16
    Time uint32
    MemoryKiB uint32
    Threads uint8
    Salt [16]byte
}
func Encrypt(ctx context.Context, dst io.Writer, password []byte, src io.Reader) error
func Decrypt(ctx context.Context, dst io.Writer, password []byte, src io.Reader) (Header, error)
func ReadHeader(src io.Reader) (Header, error)
var ErrWrongPassword error
var ErrUnsupportedVersion error
var ErrInvalidArchive error
```

任务 2 提供以下 API；这些声明只由任务 2 写入 `internal/backup/backup.go`，任务 3 据此独立实现，集成时链接。`Create`、`Inspect`、`Stage` 都在后台调用；`Inspect` 只读外层头部，不证明密码或正文有效。密码不写日志，调用者完成后清空字节切片。

```go
package backup

type CreateOptions struct {
    DataDir string
    Destination string
    Password []byte
    IncludeHistory bool
}
type StageOptions struct {
    DataDir string
    Source string
    Password []byte
}
type Info struct { Version uint16 }
type SecretFailure struct { Service, Account, Reason string }
type Result struct {
    Applied bool
    RollbackDir string
    SecretFailures []SecretFailure
    InvalidProjectIDs []string
}
func Create(ctx context.Context, opts CreateOptions) error
func Inspect(path string) (Info, error)
func Stage(ctx context.Context, opts StageOptions) (Result, error)
func ConfirmPending(dataDir string) error
func DiscardPending(dataDir string) error
func ApplyPending(dataDir string) (Result, error)
func CompletePending(ctx context.Context, dataDir string, s *store.Store) (Result, error)
func TakeResult(dataDir string) (Result, error)
```

`Stage` 返回预览，仅完整解密、认证、校验后的临时结果；`ConfirmPending` 原子写入已确认标记，只有此标记允许启动时应用。取消确认执行 `DiscardPending`；用户确认并触发重启后如退出被取消，保留标记等待下次正常启动。`ApplyPending` 不调用 `store.Open`，`CompletePending` 在迁移后的已打开 Store 上完成无历史合并与密钥恢复，落盘结果供 UI 一次读取；无待恢复项均返回零值。错误使用 `errors.Is` 可识别；密码错误与首次认证块损坏在密码学上不可区分，统一返回 `ErrWrongPassword`，后续块认证失败返回 `ErrInvalidArchive`，UI 文案分别为“密码错误或备份文件已损坏”和“备份文件已损坏”。

## 任务 1：容器格式、分块加密和解密

**允许文件：** `internal/backup/format.go`、`internal/backup/format_test.go`。

1. [ ] 定义版本 1 的固定二进制格式：8 字节 magic `MCODEBK1`，大端 `uint16` 版本 1，`uint32` Argon2 时间、内存 KiB、`uint8` 并行度、16 字节随机 salt、16 字节随机 nonce 前缀。加密使用 `argon2.IDKey(password, salt, 3, 65536, 1, 32)`；头部完整字节作为每块附加认证数据的一部分。读取头时仅接受版本 1 和设定安全上限：时间 1–6、内存 8192–262144 KiB、并行度 1–8；拒绝恶意资源参数。
2. [ ] 正文每帧编码为大端 `uint32` 明文长度、`uint8` 末块标志、密文和 16 字节标签，普通块 64 KiB；nonce 为随机前缀 16 字节加大端 `uint64` 块序号，附加数据为完整头部、块序号、长度和末块标志。空流也写一个空末块；序号溢出、超长块、非法标志、缺末块、末块后有字节均拒绝。确保密钥随机 salt 和前缀、每块 nonce 唯一，取消时尽早返回。
3. [ ] 使用 `io.Reader`/`io.Writer` 流式处理；读头不消费正文以外的数据；任意 I/O 失败按原错误向上传播；认证失败如契约映射。`Encrypt` 返回前擦除派生密钥，不复用密码或 nonce。
4. [ ] 新增测试：空流、跨块往返；相同密码与内容两次产物不同；错误密码；头部、首块、中块、末块篡改；截断、追加、重排和重复块；格式版本、Argon2 参数越界；取消及读写错误。命令：`go test ./internal/backup -run 'TestFormat'`。

**验收：** 未认证的数据不能被视为完整备份；合法流跨平台可解密，所有异常均按契约返回，内存占用与数据大小无关。

## 任务 2：采集、密钥、数据库和启动恢复

**允许文件：** `internal/backup/backup.go`、`internal/backup/collect.go`、`internal/backup/restore.go`、`internal/backup/backup_test.go`、`internal/store/backup.go`、`internal/store/backup_test.go`、`internal/agentconf/backup.go`、`internal/agentconf/backup_test.go`、`internal/app/app.go`、`internal/app/backup_restore_test.go`。

1. [ ] 按白名单采集数据目录中的 `config.json`、`ui-state.json`、`mcp.json`、`mcp_tool_policy.json`、`skills.json`、`skills-projects/`、`always_allow.json`、`remote/project-scripts.json`、`remote/script-favorites.json`、`review-checks/`、`agentcfg/`；目录递归仅收普通文件和目录，不跟随链接。缺失文件在加密清单中记录为缺失，恢复不删除目标上对应文件。明确排除 `remote-devices.json`、`remote-webpush-vapid.pem`、`remote-tls/`、`tailnet/`、日志、缓存、锁文件、`attachments/`、`worktrees/`、`swarm-worktrees/`、`shadow-git/`、`swarm-patches/`、`swarm-landed/`、`swarm-reviews/`、`config.v7.json`、`config.json.bak`；白名单目录内部也执行排除。勾选历史时增加 `goals/`、`media/` 和完整数据库。
2. [ ] 在 `internal/store/backup.go` 增加 `func (s *Store) BackupSnapshot(ctx context.Context, destination string) error` 和 `func (s *Store) MergeBackup(ctx context.Context, snapshotPath string) error`。从运行中的独立 SQLite 连接用 `VACUUM INTO` 生成一致副本，参考 `internal/store/storage_compact.go:187`；绝不复制活跃的 db/WAL。编排层提供私有的按数据目录生成快照入口，调用方可打开短暂独立连接；不能打开第二个应用 Store 或触发正常启动迁移、副作用。无历史时也先取一致快照，再在副本中清除非白名单表中的数据并执行 `VACUUM`，保证密文中确实没有历史内容。数据库自身的用户版本保留，备份版本高于目标迁移数时拒绝。
3. [ ] 确切的数据表：`projects`、`tasks`、`goals`、`goal_tasks`、`automations`、`app_settings`。`automation_runs` 属于历史，默认不备份；其随 `automations` 外键级联，见 `internal/store/store.go:155`。`threads`、`messages`、`turn_usage`、`turns`、`thread_follow_ups`、`thread_follow_up_events`、`thread_deliveries`、`pending_asks`、`thread_archives`、`message_zstd_dicts`、`quarantined_threads`、`hydra_leads`、swarm 报告、`memory_proposals`、`attention_events`、`steward_approval_scores` 均视为历史或运行态，默认排除。当前 schema 以 `internal/store/store.go:42` 和全部迁移为准；按 SQLite 实际表集合过滤，而不是误删 schema。
4. [ ] 无历史恢复保留目标历史：先建立目标数据库一致回滚副本，再在单连接 `ATTACH DATABASE ? AS incoming`，`BEGIN IMMEDIATE`。不得 `DELETE FROM projects` 或 `INSERT OR REPLACE`，因为 `threads.project_id` 对 `projects` 有 `ON DELETE CASCADE`（`internal/store/store.go:53`）。对 `projects` 按 `id` 用 `INSERT ... SELECT ... WHERE true ON CONFLICT(id) DO UPDATE SET name=excluded.name,path=excluded.path,updated_at=excluded.updated_at`；保留目标独有项目和它们的会话。对 `app_settings` 按 `key` upsert 备份值，保留目标独有项。先删除 `goal_tasks`，再删除 `tasks` 和 `goals`，再按父表、子表顺序插入；`goal_tasks` 对两表均级联（`internal/store/store.go:137`）。`automations` 也按 `id` upsert 所有列，保留目标独有自动化及全部已有 `automation_runs`，避免整表删除触发级联；不导入源运行记录。导入的 `tasks.thread_id`、`tasks.review_thread_id`、`goals.plan_thread_id` 一律设 NULL，避免偶然同 ID 错连目标历史；原有 `automation_runs.thread_id` 不改。恢复中状态为运行中的任务或目标按项目现有状态语义处理为可重新操作状态，不能凭旧 thread 恢复执行。运行 `PRAGMA foreign_key_check`；有行则回滚，成功提交后 detach。列列表基于迁移后 `PRAGMA table_info` 相交并显式处理新增列；对备份副本在独立临时目录先使用当前迁移升级，避免旧版本列缺失，拒绝高版本，禁止对源备份就地迁移。
5. [ ] 增加 `func DiscoverSecretRefs(dataDir string, cfg config.Config, projectPaths []string) ([]config.SecretRef, error)`，实现放在 `internal/backup/collect.go`；在 `internal/agentconf/backup.go` 提供 `func BackupMCPSecretRefs(dataDir string, projectPaths []string) ([]config.SecretRef, error)`，只读全局及各现存项目 `.mcode/mcp.json`。从 `internal/config/secrets.go:216` 的旧引用开始，加入固定 `mcode` 账户 `cloudflare_api_token`、`openai.apiKey`、`droid.apiKey`、`deepseek.apiKey`、`zai.apiKey`、`meta.apiKey`、`magpie1.apiKey` 至 `magpie4.apiKey`；还要遍历配置的 ACP、presets、运行时配额 provider ID 产生 `<provider>.apiKey`，覆盖 `internal/remote/quota.go:61`。对每个 custom endpoint ID 同时调用 `config.CustomEndpointSecretAccount`、`config.CustomEndpointWebKeySecretAccount`，见 `internal/app/custom_endpoints.go:93`。对 MCP 配置的每个 server 名及 `secretEnvKeys`、`secretHeaderKeys` 生成 `mcode.mcp` 下 `mcp.<name>.<key>` 和 `mcp.<name>.header.<key>`，依据 `internal/agentconf/mcp.go:427` 和 `internal/agentconf/mcp.go:447`；对可扫描到的所有 MCP server ID 生成 `mcode.mcp-oauth` 下 `mcp-oauth:<serverID>:token`、`:client`、`:cimd`，依据 `internal/mcp/oauth.go:344`。server ID 必须复用运行时真实 ID 构造与扫描规则，包括全局、项目和其他配置来源，不要假设恒等于显示名；遍历数据库记录的存在项目路径，严格只读。去重、排序，对每个账户 `Get`：未找到跳过，其他错误使备份失败，不能静默丢密钥。项目不存在、外部配置不可读或被删除导致账户无法推导时向调用方返回明确错误，不声称已枚举不可发现的孤儿账户；SecretStore 本身无法枚举，历史孤儿密钥无法完整保证。
6. [ ] `Create` 将清单、白名单文件、筛选后的数据库快照和密钥记录组成正斜杠 tar，流式传给任务 1 的 `Encrypt`；输出同目录临时文件，完成后 fsync 并原子替换目标，错误或取消删除临时文件。对目录遍历、文件读取和 tar 总大小设置明确上限；不把明文密钥写入临时文件。备份包含历史时的快照可能包含会话引用的 attachments/worktrees，但这些目录仍按排除规则不包含。
7. [ ] `Stage` 在数据目录私有临时目录中解密、解 tar，目录权限 0700、文件 0600；解密可能先输出未认证数据，所以只落入未发布的临时目录，整流成功后才改名为 `restore-pending/`。拒绝绝对路径、`..`、Windows 盘符、反斜杠、重复路径、软硬链接、设备文件、白名单外路径、解压炸弹、缺清单和缺末块；不得跟随目标路径的符号链接。清单包括版本、历史选项、文件列表和大小哈希；完整核对清单和数据库 `quick_check`。在写入已确认标记前解析备份 `config.json`，若版本高于 `internal/config/config.go:19` 的 `Version`，返回 `ErrUnsupportedVersion`。失败、取消均安全清理临时明文，不暴露密码及密钥值。
8. [ ] 恢复目标机字段时，按 JSON 合并而非反序列化再重编码以免丢未知字段：保留目标 `providers` 中各 provider（含 ACP map）的 `bin`、`workbench.terminal_shell`、`steward.home_dir` 及 `remote` 整个对象，目标原本没有的字段保持缺省，严禁从源导入这些字段；核对实际 JSON 层级，以 `internal/config/config.go:421`、`internal/config/config.go:840`、`internal/config/config.go:1254` 和 `internal/config/config.go:1903` 为准。目标配置不存在时以上字段均缺省。项目路径按原字符串保存，跨平台不强制转成本机路径；用 `os.Stat` 和目录检查找出不可用项目，保留数据并返回 ID 给 UI。
9. [ ] 在 `internal/app/app.go:346` 的 `config.Load` 前调用 `ApplyPending`；在同一启动周期的 `internal/app/app.go:381` 的 `store.Open` 成功后、`EnsureIncrementalAutoVacuum` 和后台 inbox 迁移之前立即调用 `CompletePending`；这也是已经完成数据库迁移且其他组件尚未读取 store 的最早安全位置。失败则关闭 store 并停止启动，不让组件观察半恢复数据。完整历史在打开 store 前替换数据库，必须先移动旧 db、`-wal`、`-shm` 作为一组，避免旧 WAL 污染新库；无历史保留目标库并通过步骤 4 的事务合并。
10. [ ] 启动恢复以 `pre-restore-<时间戳>/` 保存所有将覆盖的目标文件及数据库一致副本；每个路径记录原先存在与否、操作进度和恢复模式的持久日志，先 fsync 日志再移动，原子替换并 fsync 父目录。失败或进程中断后下次启动按日志回滚已变更文件并重试，数据库事务失败时保持旧库；数据库提交与日志状态之间通过目标库事务内写入唯一 restore ID 的 `app_settings` 标记解决重复应用，完成后删除标记。密钥通过各 service 的 `SecretStore.Set` 写回；逐个失败记录 service、account、错误类别并继续，不输出值。日志及明文暂存权限 0700/0600，成功写入结果后清理 `restore-pending`；结果不含密钥内容，`TakeResult` 读取后删除。回滚目录长期保留由用户处置。
11. [ ] 新增测试：白名单与排除；账户固定、custom、MCP 环境变量/请求头、OAuth 三后缀、项目及去重；Get 失败拒绝备份、Set 失败收集；带 WAL 的快照一致；无历史备份不含历史数据；目标有线程、目标独有项目和自动化、运行记录时合并及 `foreign_key_check`；同 ID 线程引用清理；完整历史 WAL 替换；旧 schema 升级与新 schema 拒绝；跨平台路径、目标字段保留；恶意 tar 和新配置版本；各恢复阶段注入崩溃后重试和回滚。执行 `go test ./internal/backup ./internal/store ./internal/agentconf ./internal/app`。

**验收：** 启动只应用确认过的有效备份；无历史恢复不会删除目标历史；历史模式用一致数据库；失败可恢复旧文件；不可枚举的孤儿密钥限制明确报告。

## 任务 3：通用设置、弹窗与项目失效状态

**允许文件：** `internal/ui/settings_page_general.go`、`internal/ui/settings_backup.go`、`internal/ui/settings_backup_test.go`，以及项目选择入口 `internal/ui/chat.go`。不得修改任务 1、2 拥有的文件。

1. [ ] 在 `internal/ui/settings_page_general.go:33` 的 `pageGeneral.Sections` 加“备份与恢复”区块，只有“备份…”和“恢复…”两行，无说明文字；复用现有设置行和按钮模式。状态放在独立的 `internal/ui/settings_backup.go`，后台任务持有取消上下文，UI 线程刷新状态，按钮防重复触发。
2. [ ] 备份弹窗使用 `modalDragModal` 或 `ui.DialogBase`，参考 `internal/ui/sidebar.go:1426`；密码输入两次且不一致时禁用确认，提供“包含会话历史”开关。确认后调用 `mygo.Dialog.Save`，参考 `internal/ui/settings_page_about.go:352`，过滤并默认后缀 `.mcodebackup`；取消对话框不创建文件。后台调用 `backup.Create`，结束时清空密码并展示成功或可理解的错误。
3. [ ] 恢复先调用 `mygo.Dialog.Open` 选择 `.mcodebackup`，然后弹出密码输入；后台 `backup.Stage` 成功才展示 `internal/ui/confirm_dialog.go:65` 的 `RenderConfirmDialog`，`Tone: ConfirmDanger`，确认调用 `ConfirmPending` 后调用 `mygo.App.Relaunch()`；取消调用 `DiscardPending`。密码错误显示“密码错误或备份文件已损坏”；不支持版本和其他失败分别展示具体错误。MyGo 当前模块明确提供 `Application.Relaunch`，其先执行正常退出并保留参数；macOS 用 bundle、Windows/Linux 重启可执行文件、dev 由开发宿主重启。不能直接 `os.Exit`，以免跳过正常关闭；退出被取消则暂存保留。
4. [ ] 启动后读取 `backup.TakeResult`，有失败密钥时列出 service/account 和原因，不展示密钥值；项目列表对 `os.Stat` 不存在或非目录的项目显示“路径失效”，禁止自动删除或重写项目路径，目录随后恢复时自然消除标记。完整历史及无历史模式都适用此规则；不要把失效检查放在创建/导入时导致项目被过滤。
5. [ ] 新增 UI 测试：两行且无说明、密码不一致、密码清除、保存/打开取消、历史开关、密码错误、二次确认取消不重启、确认成功只调用一次重启、退出取消、后台失败、密钥失败列表、失效项目保留和重新可用。注入对话框、备份接口、重启函数和路径检查以避免真实桌面及凭据。弹窗只能使用允许的外壳；所有动效使用 `internal/ui/tokens.go` 中时长并经 `animateDuration`，尊重 ReduceMotion；运行 `go test ./internal/ui`，包含 `internal/ui/modal_style_guard_test.go` 与 `internal/ui/motion_guard_test.go`。

**验收：** 可完成备份、选择文件、验证密码、确认和正常重启；无附加说明文字；恢复结果和失效项目可见，且不会泄露密码。

## 集成与注意事项

- 先按公开契约合并三个任务，再由主代理运行各任务测试与 `go test ./internal/backup ./internal/store ./internal/agentconf ./internal/app ./internal/ui`，并在隔离 dev 数据目录验证 macOS 原生对话框；Windows/Linux 的文件权限与重启需各平台验证。
- 不可枚举的已删除配置对应孤儿密钥，以及完全不可访问的项目配置，无法由 `SecretStore` 推导：这是现有接口的真实边界，不允许用“全部密钥已备份”掩盖。对不可访问的已知项目必须在备份前报错；孤儿密钥在汇报中明确说明。
- 恢复暂存目录为本机明文敏感数据：确认前后都只授予当前用户权限；崩溃时为可重试会暂留，恢复完成或取消立即清理。回滚目录中原文件也可能含敏感配置，应限制权限。
