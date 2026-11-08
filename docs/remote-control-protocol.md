# 远程控制协议

本文档描述 mcode 远程控制的 JSON-RPC 2.0 协议，机器可读契约见 `remote-control.schema.json`。

## Web Push 离线推送

宿主在会话事件触发提醒时，经 RFC 8291（aes128gcm）加密、用 VAPID（ES256）签名发送到手机浏览器登记的推送端点。

- **RPC 方法**（与 schema 中 `RemotePush*` 定义一致，且仅在宿主启用推送时出现在 `capabilities.get` 的 `methods` / `features` 里）：
  - `push.publicKey`：无参数；返回 `{ "publicKey": String }`，Base64URL 编码的 65 字节未压缩 P-256 公钥。
  - `push.subscribe`：`{ "endpoint", "p256dh", "auth" }`；`endpoint` 必须为已知推送服务的 https 地址（`web.push.apple.com`、`fcm.googleapis.com`、`updates.push.services.mozilla.com`、`notify.windows.com` 及其子域），`p256dh` 解码后 65 字节、`auth` 解码后 16 字节。订阅按当前已认证设备的设备 ID 保存，同一 endpoint 只属于一台设备。
  - `push.unsubscribe`：`{ "endpoint" }`；只移除当前设备名下的该 endpoint。
- **推送载荷**（JSON 明文，aes128gcm 加密后发送）：
  ```json
  { "title": "会话标题", "body": "Finished." | "需要你的审批" 等, "threadID": "UUID", "silent": true, "workDir": "/path/to/repo" }
  ```
  - `silent`：由宿主自己响铃的提醒（如轮次完成）为 `true`，service worker 据此不重复响铃。
  - `workDir`：会话工作目录，浏览器据此判断自己是否正停在同一仓库上。
- **触发场景**：
  - 新的待审批/待回答请求（`requests.changed` 新增 request），tag `approval:<threadID>`。
  - 会话由运行中转为结束（`thread.summary` / `threads.changed`），`attention` 为 `failed` 时按“出错”发送，否则按“完成”发送。
- **去重与节流**：同一 requestID 只提醒一次；同一会话同类提醒 30 秒内只推送一次。
- **在线跳过**：设备若有已认证且订阅了至少一个会话的 WebSocket 连接，视为浏览器在线，跳过该设备的推送，避免手机上重复两条通知。
- **失效清理**：推送服务返回 404 或 410 时删除对应订阅；撤销单台设备时清除该设备全部订阅，撤销全部设备时清空所有订阅。
- **存储**：订阅随配对设备保存在宿主的设备记录（`remote-devices.json`）中，VAPID 私钥保存在数据目录的 `remote-webpush-vapid.pem`（0600）。
- **前提**：浏览器要求 `https` 安全上下文，局域网 HTTP 入口不能订阅；iOS 需添加到主屏幕后从主屏幕启动。
- **在线跳过开关**：`capabilities.get` 的 `features` 中带 `push.offline` 表示宿主的推送只发给离线设备（在线判定见上）；没有该 feature 时宿主会对所有订阅推送，页面应自行判断当前是否在前台，避免重复提醒。

## 传输入口与 `transport.tailscale`

远程服务有三种入口，由 `internal/remote/conn.go` 的 `Entry` 表示，并随每个连接写入 `ClientInfo.Entry`：

- `lan`：局域网 HTTP 入口，非安全上下文。
- `tls`：带证书的 HTTPS 入口，安全上下文。
- `tailscale`：经 Tailscale（tsnet）的 HTTPS 入口，同为安全上下文。`Entry.Secure()` 对 `tls` 与 `tailscale` 返回 `true`。

行为约定：

- 安全上下文（`tls`、`tailscale`）可订阅 Web Push（`push.*`），也可读取机密答案（`requests.respond` 中 `isSecret` 的请求）；`lan` 入口仍要求 `http` 同源 Origin，读取机密答案仍返回 `-32012`（`ErrCodeRequiresHTTPS`）。
- 校验 Host 与 Origin 时缺省端口按 443 处理：请求不带端口即视为 443，监听端口为 `:443` 时可直接通过；主机名比较忽略大小写，Origin 必须为 `https`。
- `capabilities.get` 的 `features` 末尾追加 `transport.tailscale`，客户端据此判断宿主是否支持 tailnet 入口；`features` 是自由字符串数组，未知项应直接忽略。
- `session.newPairingCode` 的结果新增 `tailscalePairingURL`（`omitempty`）：tailnet 入口可用时给出该链接；主链接按 Tailscale、TLS、LAN 的顺序回退，`pairingURL` 与 `lanPairingURL` 语义不变。
- 连接建立后，宿主会尽力调用 tsnet `WhoIs` 把 tailnet 登录名与节点名写入 `ClientInfo` 的 `TailnetUser`、`TailnetNode`，超时 2 秒；该信息只用于展示与审计，不参与鉴权，失败时留空且不拒绝连接。
- 不使用 Tailscale Funnel：入口只对 tailnet 内节点（以及显式共享的节点）开放。
- `docs/remote-control.schema.json` 的 `SessionNewPairingCodeResult` 已声明可选字段 `lanPairingURL`、`tailscalePairingURL`；`features` 无需枚举；字段缺失时客户端按不支持 Tailscale 入口处理。

## 扩展方法

远程层的方法是插件式的：新增一个 RPC 不需要改 `internal/remote/dispatcher.go` 的 `switch`，也不需要改 `internal/remote/capabilities.go` 的方法清单。只要在 `internal/remote` 下新建文件、在 `init()` 里注册即可。

### 方法清单

下列方法只列出「做什么」；参数与结果的字段以 `docs/remote-control.schema.json` 中的 `$defs` 为准（命名规则见「capabilities 与 schema」）。

- `steward.open`：打开全局总管前台 feed，`projectID` 表示入口来源，结果为 `{ "threadID" }`。
- `steward.decisions.list`：列出决策卡。

回答决策卡是向主会话发送「D-编号 选项」消息，不是单独的方法。
- `app.update.get`：参数为 `{}`，返回最近一次更新状态及发布说明，不联网；参数错误返回 `InvalidParams`，服务未注入返回 `MethodNotFound`。
- `app.update.check`：参数为 `{}`，联网检查并返回更新状态及发布说明；参数错误返回 `InvalidParams`，服务未注入返回 `MethodNotFound`，检查失败返回内部错误。
- `app.update.install`：参数为 `{}`，受理后返回 `{ "started": true }`，安装会重启桌面端并断开远程连接；参数错误或没有可安装更新返回 `InvalidParams`，服务未注入返回 `MethodNotFound`。
- `settings.get`：返回设置值快照、可写键白名单和快捷回复。
- `settings.set`：写入一行键值，任一键不在白名单或值非法时整体拒绝。
- `settings.changed`：通知，载荷只带变化的键。
- `automations.create`：新建自动化，返回创建后的对象。
- `automations.update`：按 `id` 更新自动化，可带 `revision` 做乐观并发。
- `automations.delete`：删除自动化。
- `automations.setEnabled`：启用或暂停自动化。
- `automations.reset`：清空自动化的运行历史与失败计数。
- `threads.swarm.report`：取某只 bee 的汇报全文，过长时 `truncated` 为真。
- `threads.revert.preview`：预览撤销某一轮的文件改动，参数 `threadID`、`turnID`，不落盘；结果带 `total`、`restorableCount`、`keptCount`、`kept`（轮次后被改过、回退时保留的路径）与 `files`（每项 `path`、`additions`、`deletions`、`kept`、`inBase`、`restorable`）。会话运行中返回 `OperationRejected`（`-32000`）。
- `threads.revert.apply`：执行同一轮的回退，参数与结果字段同预览，额外带 `ok`，表示已按预览落地。
- `files.write`：覆盖保存工作区里已存在的文本文件，参数 `threadID`、`path`（相对工作区）、`content` 与可选 `expectedHash`（64 位小写十六进制 SHA-256，取自 `files.read` 结果的 `hash`）；结果为 `{ "hash", "size" }`，`hash` 可直接作为下一次写入的 `expectedHash`。不创建新文件、不写二进制、内容上限 512 KiB；路径不存在回 `FileNotFound`（`-32004`），二进制、受保护文件或写入失败回 `OperationRejected`（`-32000`），`expectedHash` 与磁盘内容不一致回版本冲突（`-32009`）。
- `threads.splitSession`：在会话里追加一条分割线，参数 `threadID`；结果为 `{ "threadID", "pending" }`，`pending` 恒为 `true`，表示从下一条消息起生效。之后发给模型的上下文不再包含分割线以上的历史，账本等交接内容照常带上。回合运行中或空会话返回业务错误 `OperationRejected`（`-32000`）。
- `mcp.servers.list`：按会话或项目列出 MCP 服务器及本会话的开关状态。
- `session.devices.list`：列出本机全部已授权设备，参数必须是空对象 `{}`；结果为 `{ "devices" }`，每项含 `deviceID`、`label`、`pairedAt`、`lastSeenAt`（从未活动时为空串）、可选的 `entry`（`lan` / `tls` / `tailscale`）、`platform`、`userAgent`、`lastAddress`，以及 `online`、`current`。按配对时间升序，不含任何凭据材料。
- `session.devices.revoke`：按 `deviceID` 撤销一台设备，结果为 `{ "deviceID", "revoked" }`；撤销当前设备回 `InvalidParams`（`-32602`，提示改用 `session.revokeDevice`），未知设备回 `ResourceNotFound`（`-32002`）。成功后宿主广播 `session.devices.changed`。
- `session.devices.changed`：通知，载荷为 `{ "deviceID", "revoked" }`，客户端收到后重新拉一次 `session.devices.list`。
- `git.push` / `git.pull` / `git.fetch`：分别推送当前分支、快进拉取上游、抓取远程分支；参数只有 `threadID`，结果为 `{ "ok": true }`。推送与拉取失败回 `InternalError`，文案为 git 原始错误（已脱敏）；网络操作在宿主侧有超时上限。
- `git.checkout`：切换到本地已有分支，参数 `threadID`、`branch`；`git.createBranch`：从当前分支新建并切换，参数 `threadID`、`name`；`git.merge`：把 `branch` 合并进当前分支，参数 `threadID`、`branch`。分支名必须是合法 git 引用（非空、不以 `-` 开头、不含空白与控制字符，也不含 `~ ^ : ? * [ \ .. // @{` 等），否则回 `InvalidParams`。
- `git.generateCommitMessage`：为未提交改动生成一条提交信息，参数 `threadID`，结果为 `{ "message" }`；未开启该功能或没有改动时回 `InternalError`，文案沿用客户端的原始提示。
- `git.pr.createUrl`：按会话所在仓库的远端地址与当前分支生成创建 PR/MR 的网页地址，参数 `threadID`，结果为 `{ "url", "title"?, "body"? }`。地址规则与桌面端「创建合并请求」一致：GitHub 等平台拼 `<repo>/compare/<branch>?expand=1`，GitLab 拼 `<repo>/-/merge_requests/new?merge_request[source_branch]=<branch>`；`title`、`body` 是宿主预填的表单字段，宿主没有预填内容时省略。没有远端、识别不到托管平台或处于游离 HEAD 时回 `InvalidParams`。
- `git.commitSelected` 的参数新增可选 `pushAfter`：为 `true` 时提交成功后继续推送当前分支。推送失败不改变结果的 `ok`（仍表示已提交），失败原因放在 `pushError`（已脱敏），客户端据此提示「已提交，推送失败」。
- `projects.add`：把宿主上一个已存在的目录登记为项目，参数 `path`，结果为 `{ "project" }`；路径不存在或不是目录回 `InvalidParams`（`-32602`）。成功后宿主广播 `projects.changed`。
- `projects.validatePath`：校验一个路径能否作为项目目录，参数 `path`，结果为 `{ "valid", "kind", "reason"? }`；不返回错误，校验不通过时用 `valid: false` 与 `reason` 说明。
- `projects.clone`：在宿主上 `git clone` 并登记成项目，参数 `url`、可选 `parentPath`、可选 `name`，结果与 `projects.add` 同型 `{ "project" }`。`url` 只接受 `https://` 与 `git@host:owner/repo.git`，`parentPath` 必须位于用户主目录之内，`name` 缺省取仓库名且只允许字母数字与 `-_.+`；参数不合法或目标目录已存在回 `InvalidParams`（`-32602`），克隆失败回 `OperationRejected`（`-32000`）。宿主侧同步执行，客户端超时应放宽到 10 分钟。成功后广播 `projects.changed`。
- `projects.listDir`：浏览宿主的子目录，参数可选 `path`（缺省用户主目录）、可选 `showHidden`，结果为 `{ "path", "parent"?, "entries", "truncated" }`，每项 `{ "name", "path", "isGitRepo" }`。只列目录、不列文件，按名称不区分大小写升序，超过 500 项时截断并置 `truncated`；`path` 解析符号链接后必须位于用户主目录之内，否则回 `InvalidParams`（`-32602`）。
- `scripts.save`：整表替换某个项目的项目级脚本，参数 `projectID`、`scripts`（最多 500 项，每项 `id`?、`name`、`command`、`symbol`?、`runOnWorktreeCreate`?）、可选 `expectedRevision`，结果与 `scripts.list` 同型。`id` 留空表示新建；`expectedRevision` 取自 `scripts.list` 结果的 `revision`，不一致时整表不写入并回版本冲突（`-32009`）；项目不存在回 `ResourceNotFound`（`-32002`），参数不合法回 `InvalidParams`（`-32602`），宿主不支持回 `OperationRejected`（`-32000`）。
- `scripts.list` 的结果新增 `revision`，`projectScripts` 每项新增 `command` 与 `runOnWorktreeCreate`，写回时照抄即可。
- `kanban.deleteTask`：删除一条任务，参数 `taskID`、`expectedRevision`、`actionID`，结果为 `KanbanTaskMutationResult`（`actionID`、可选 `taskID` / `goalID` / `revision`）。`taskID` 与 `actionID` 必须是 UUID，`expectedRevision` 为 0 表示不校验；同一个 `actionID` 重放返回首次结果，不重复删除也不重复广播。修订号冲突回版本冲突（`-32009`），任务不存在回 `ResourceNotFound`（`-32002`），运行中任务被拒绝或看板服务不可用回 `OperationRejected`（`-32000`），宿主不支持回 `MethodNotFound`（`-32601`）。
- `kanban.rescueTask`：执行救援菜单动作，参数 `taskID`、`action`（`interrupt` 中断并重置为待办，`nudge` 提醒 Agent）、`expectedRevision`、`actionID`，结果与错误码同 `kanban.deleteTask`；动作不被支持或任务状态非法回 `OperationRejected`（`-32000`）。
- `memory.create`：新建一条待审记忆提议（等价于桌面端「新建记忆」），参数 `projectID`、可选 `threadID`、`title`、可选 `content` / `type` / `importance` / `scope`，结果为创建出的 `MemoryExtProposal`。只入待审队列，不直接写入 Nowledge Mem；`projectID` 必须是已知项目，`threadID` 必须是 UUID，`importance` 在 0 到 1 之间，`scope` 取 `project` 或 `global`，否则回 `InvalidParams`（`-32602`）。成功后广播 `memory.proposals.changed`。
- `memory.search`：搜索记忆，参数可选 `projectID`（为空表示全部项目）、`query`、可选 `limit`（缺省 20，取值 1 到 50），结果为 `{ "results" }`，每项含 `id`、`title`、`content`、`unitType`?、`importance`、`labels`?、`createdAt`；无结果时 `results` 为空数组。参数不合法回 `InvalidParams`（`-32602`），服务未注入回 `MethodNotFound`（`-32601`）。
- `memory.proposals.accept` 的参数新增可选 `title`、`content`、`type`、`importance`：提供时先按编辑后的内容更新提议再接受，等价于桌面端「保存编辑，再点接受」；字段为空串或 `importance` 越界回 `InvalidParams`（`-32602`）。`MemoryExtProposal` 新增可选 `scope`（`project` / `global`）。
- `capabilities.get` 的结果新增可选 `appVersion`：宿主应用版本号，缺失时客户端按未知处理。

### 新增一个远程方法

新建 `internal/remote/xxx_methods.go`：

```go
package remote

import (
	"context"
	"encoding/json"
)

func init() {
	RegisterMethod("xxx.list", handleXXXList)
}

func handleXXXList(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p struct {
		Limit int `json:"limit"`
	}
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err // 已转成 wire.InvalidParams
	}
	svc, ok := backendService[*xxxService](d.backend, "xxx")
	if !ok {
		return nil, ExtNotImplemented()
	}
	return svc.List(ctx, c.Info().DeviceID, p.Limit)
}
```

导出 API：

- `type ExtensionHandler func(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error)`
- `type ExtError struct { Code int; Message string }`（实现 `error`）
- `func ExtInvalidParams(msg string) *ExtError`（`wire.InvalidParams`）
- `func ExtNotImplemented() *ExtError`（`wire.MethodNotFound`，消息 `Method not implemented`）
- `func RegisterMethod(name string, h ExtensionHandler)`（包级注册表，互斥锁保护；方法名重复、为空或与内置方法重名时 panic；只在 `init()` 中调用）
- `func decodeExtParams(raw json.RawMessage, v any) error`（包内用，复用 `decodeStrict`，失败返回 `InvalidParams`）
- `func (b *MemoryBackend) SetService(name string, svc any)` / `func (b *MemoryBackend) Service(name string) any`
- `func backendService[T any](backend Backend, name string) (T, bool)`（包内用，先断言后端支持 `Service(string) any`，再把服务断言为 `T`）
- `func (d *Dispatcher) NotifyAuthed(method string, params any)`（向所有已认证连接广播通知，推送失败的连接会被移除）

执行与错误约定：

- 路由：`NewDispatcher` 把内置方法名与已注册扩展方法名一起并入路由表；内置 `switch` 没命中时查扩展注册表。
- 返回值：错误为 `nil` 时按成功结果回包；错误为 `*ExtError` 时按其 `Code` 回包；其它错误按内部错误回包（`wire.InternalError`，消息脱敏）。
- 参数：扩展方法的参数同样走严格解码（`decodeStrict`，未知字段直接拒绝），因此扩展只需 `decodeExtParams`，不必自己拼错误文案。
- 通知：扩展要广播状态变化时用 `d.NotifyAuthed("xxx.changed", payload)`，与 `threads.changed` 的广播写法一致。

### app 层注入服务

新建 `internal/app/xxx_remote.go`，用 `registerRemoteWiring` 登记接线，由 `New` 在末尾统一执行：

```go
package app

func init() {
	registerRemoteWiring(func(a *App) {
		a.backend.SetService("xxx", newXXXService(a))
	})
}
```

接线时机在 `internal/app/app.go` 的 `wireRemoteExtensions(appInstance)`：此时 `backend` 的全部 `SetXxx` 与 Swarm、看板、目标调度、自动化、Nowledge、MCP、账号管理器构造都已完成，`a.backend`、`a.client`、`a.hub`、`a.store`、`a.kanban`、`a.scheduler`、`a.automation`、`a.swarm`、`a.swarmPipeline`、`a.mcp`、`a.mcpHub`、`a.mcpProxy`、`a.remote` 都可用。接线函数只应做注入，不要启动后台任务。

handler 里取服务用 `backendService`：先断言后端实现 `Service(string) any`，再断言成目标类型，未注入或类型不符时返回 `false`，据此回 `ExtNotImplemented()`。

### capabilities 与 schema

- `capabilities.get` 的 `methods` 由 `ImplementedMethods()` 生成：内置方法保持声明顺序，扩展方法按名字升序**追加**在后面，输出确定；`features` 仍由 `DefaultFeatures()` 给出，扩展需要新 feature 时自行追加。
- 新增方法必须同步补充 `docs/remote-control.schema.json`（本次改动不涉及 schema，由 schema 的维护者合入）。
- 扩展方法是同一份协议的一部分：参数与结果结构一旦对外发布，就按 schema 的兼容规则演进。

### 调研结论：`Client` 与 `App` 的关系

`Client` 是进程内 `mcodeui.Client` 适配器，包着 `*remote.MemoryBackend`；`App` 持有 `client` 字段并通过 `Client()` 暴露。所以 **App 侧永远拿得到 `*Client`**，下面这些能力都能通过 `a.remoteClient()`（或 `a.Client()`）调到；需要一个辅助函数只是为了加空指针保护。

- `GitPRList`（`internal/app/client_pr.go:11`）：依赖 `gitStashTarget` 定位仓库根，内部的 `gitRunner()` 是 Client 自持的执行器 → 需要 `*Client`。
- `GitStashList`（`internal/app/client_stash.go:30`）：同上，走 `gitStashTarget` → 需要 `*Client`。
- `ResumeInterruptedSwarm`（`internal/app/client_swarm_resume.go:10`）/ `DismissInterruptedSwarm`（`internal/app/client_swarm_resume.go:26`）：只用 Swarm 管理器（经 `kanbanMu` 取快照）→ 需要 `*Client`；App 也有同源的 `a.swarm`，但用它就要复制那把锁的取快照逻辑。
- `AutomationRunsState`（`internal/app/client_automation_runs.go:12`）：用 Client 的自动化服务句柄加 UI 转换 → 用 `*Client` 最省事，`a.automation` 是同一实例。
- `MemoryProposals`（`internal/app/client.go:3069`）：依赖 `memoryStoreConn()` 懒建的第二个 store 连接 → 必须 `*Client`。
- 人设：列人设的 `MagpiePersonas`（`internal/app/persona.go:157`）依赖 `loadConfig()` 与 Client 的密钥库 → 必须 `*Client`；设置页测试用的 `TestMagpiePersonas`（`internal/app/persona.go:183`）与设标签的 `personaTag`（`internal/app/persona.go:288`）同样在 Client 上。包级 helper `personaSourceFor`（`internal/app/persona.go:47`）、`personasFor`（`internal/app/persona.go:113`）、`personaTagFor`（`internal/app/persona.go:258`）只吃 `cfg`/`secrets`，App 侧自己取配置也能调。
